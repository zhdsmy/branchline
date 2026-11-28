package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestLoginShellQuote feeds quoted names to every shell a login can use:
// the shell must read back exactly the bytes of the name, and no backslash
// may sit inside a single-quoted span of the quoting, where fish would
// read it differently from a POSIX shell.
func TestLoginShellQuote(t *testing.T) {
	names := []string{"a'b", `a\`, `a\\b`, `a\'b`, `'`, `\'`, "$HOME", "`x`", "a b", "=ls", "*", "~x", ";", "\t", "中文"}
	for _, shell := range loginShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s on this machine", shell)
			}
			env, home := shellEnv(t)
			for _, name := range names {
				quoted := loginShellQuote(name)
				cmd := exec.Command(shell, "-c", "printf %s "+quoted)
				cmd.Env = env
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("%s reading %q (%s): %v", shell, name, quoted, err)
				}
				if string(out) != name {
					t.Errorf("%s read %s as %q, want %q", shell, quoted, out, name)
				}
				if bad := backslashInsideSingleQuotes(quoted); bad >= 0 {
					t.Errorf("%s has a backslash inside single quotes at byte %d: %s", shell, bad, quoted)
				}
			}
			dirEmpty(t, home)
		})
	}
}

// backslashInsideSingleQuotes is the index of a backslash inside a
// single-quoted span of s, or -1. \' outside the quotes reopens them after
// one character, and \\ outside them is one escaped backslash.
func backslashInsideSingleQuotes(s string) int {
	in := false
	for i := 0; i < len(s); i++ {
		switch {
		case !in && s[i] == '\\':
			i++
		case s[i] == '\'':
			in = !in
		case in && s[i] == '\\':
			return i
		}
	}
	return -1
}

// loginShellFakeSSH is an ssh that runs the remote command here through
// the login shell under test, as sshd would on a server whose account has
// that shell. It writes the command it receives to a log first, one line
// per command, and runs the shell with a private HOME and TMPDIR so that
// nothing of the user's files leaks in and everything the command leaves
// behind can be found.
func loginShellFakeSSH(t *testing.T, shell, log, home, tmpdir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\n" +
		"while [ $# -gt 1 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -o|-p|-i|-l) shift 2 ;;\n" +
		"    *) break ;;\n" +
		"  esac\n" +
		"done\n" +
		"host=$1; shift\n" +
		"[ \"$host\" = \"devbox\" ] || { echo \"bad host $host\" >&2; exit 255; }\n" +
		"printf '%s\\n' \"$1\" >> " + ShellQuote(log) + "\n" +
		"exec env -i HOME=" + ShellQuote(home) + " PATH=/usr/bin:/bin LC_ALL=C TMPDIR=" + ShellQuote(tmpdir) + " " + ShellQuote(shell) + " -c \"$1\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestLoginShellCommands runs the commands the app sends to a server
// through every shell a login can use: git commands, the askpass wiring,
// the prepared-command plumbing, the clone helpers, the compare read and
// the remote editing scripts. Every one must behave exactly as it does
// when the login shell is /bin/sh, and the command on the wire must always
// be the /bin/sh wrapping: one line, no newline, no !.
func TestLoginShellCommands(t *testing.T) {
	t.Setenv("GIT_EDITOR", "false")
	var baseline map[string]string
	for _, shell := range loginShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s on this machine", shell)
			}
			results := loginShellOps(t, shell)
			if shell == "/bin/sh" {
				baseline = results
				return
			}
			if baseline == nil {
				t.Fatal("the /bin/sh baseline did not run")
			}
			for key, want := range baseline {
				if results[key] != want {
					t.Errorf("%s: %s is %q, want the /bin/sh result %q", shell, key, results[key], want)
				}
			}
		})
	}
}

// loginShellOps runs the whole set of server operations through fake ssh
// answering as shell, and returns what they produced. It also checks what
// the wire protocol demands of every command: the /bin/sh wrapping, one
// line, no !; that nothing landed in the login's HOME; and that the
// askpass script the fetch wrote on the server is gone again.
func loginShellOps(t *testing.T, shell string) map[string]string {
	t.Helper()
	home := t.TempDir()
	tmpdir := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands.log")
	bin := loginShellFakeSSH(t, shell, log, home, tmpdir)

	results := map[string]string{}

	// The repository lives in a directory whose name the shell must read
	// whole, and carries a remote of a local bare repository for fetch.
	parent := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(filepath.Join(parent, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "repo")
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.name", "Ada")
	gitRun(t, dir, "config", "user.email", "ada@example.com")
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	writeFile(t, dir, "new é.txt", "n\n")
	gitRun(t, dir, "checkout", "-q", "-b", "work")
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "-u", "origin", "work")

	ex := &SSHExecutor{Host: "devbox", User: "ada", Port: 2222, Dir: dir, SSH: bin, NoMultiplex: true}
	r := New(ex)

	// Status and its answer.
	st, err := r.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var unstaged []string
	for _, f := range st.Unstaged {
		unstaged = append(unstaged, f.Path)
	}
	sort.Strings(unstaged)
	results["status"] = fmt.Sprintf("%s|%s", st.Branch, strings.Join(unstaged, ","))

	// git reads a working-tree file of the server for the compare sides.
	_, data, _, _, err := r.CompareSides("", "", "a.txt")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	results["compare"] = string(data)

	// A file whose name holds a quote and a space is staged, committed and
	// queried back as a pathspec.
	writeFile(t, dir, "a'b c", "q\n")
	if err := r.Stage("a'b c"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := r.Commit("add quoted file", false); err != nil {
		t.Fatalf("commit: %v", err)
	}
	out, err := r.Output("log", "--format=%s", "--", "a'b c")
	if err != nil {
		t.Fatalf("log of quoted file: %v", err)
	}
	results["log-quoted-file"] = strings.TrimRight(out, "\n")

	// Fetch with AskPass writes the askpass script on the server for the
	// duration of the command. The remote is local, so nothing is asked.
	rec := &recorder{}
	ask := NewAskPass(rec.prompt)
	defer ask.Close()
	aex := &SSHExecutor{Host: "devbox", User: "ada", Port: 2222, Dir: dir, SSH: bin, NoMultiplex: true, AskPass: ask}
	ar := New(aex)
	if err := ar.Fetch(); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	results["fetch"] = "ok"
	if len(rec.asked) != 0 {
		t.Errorf("the local remote made the user answer %q", rec.asked)
	}

	// A prepared command: the rebase reads its todo from stdin and rewords
	// a commit with a message holding the bytes quoting must survive.
	plan, err := ar.RebasePlan("HEAD~1")
	if err != nil {
		t.Fatalf("rebase plan: %v", err)
	}
	if len(plan) != 1 {
		t.Fatalf("rebase plan of one step: %+v", plan)
	}
	want := "Reworded ' $HOME `id`\n\nbody line"
	plan[0].PreparedMessage = want
	if err := ar.RebaseWith("HEAD~1", plan); err != nil {
		t.Fatalf("prepared rebase: %v", err)
	}
	msg, err := ar.Message("HEAD")
	if err != nil {
		t.Fatalf("message after rebase: %v", err)
	}
	results["rebase"] = msg
	if msg != want {
		t.Errorf("prepared message is %q, want %q", msg, want)
	}

	// The clone helpers: make the parent, look for a name that is there
	// and one that is not, and remove a leftover tree.
	pex := &SSHExecutor{Host: "devbox", User: "ada", Port: 2222, Dir: parent, SSH: bin, NoMultiplex: true}
	if err := EnsureDir(pex); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	results["ensuredir"] = "ok"
	results["pathexists-yes"] = fmt.Sprint(pathExists(pex, "repo"))
	results["pathexists-no"] = fmt.Sprint(pathExists(pex, "not-there"))
	if results["pathexists-yes"] != "true" || results["pathexists-no"] != "false" {
		t.Errorf("pathExists yes %s no %s", results["pathexists-yes"], results["pathexists-no"])
	}
	leftover := filepath.Join(parent, "half-clone")
	if err := os.MkdirAll(filepath.Join(leftover, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(pex, "half-clone"); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("removeTree left %s behind", leftover)
	}
	results["removetree"] = "gone"

	// The remote editing scripts download a file and write it back.
	copy, err := r.DownloadForEdit(context.Background(), "a.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download for edit: %v", err)
	}
	server, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile(copy.Local)
	if err != nil {
		t.Fatal(err)
	}
	results["download"] = string(local)
	if string(local) != string(server) {
		t.Errorf("downloaded %q, want %q", local, server)
	}
	if err := os.WriteFile(copy.Local, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveEdited(context.Background(), copy); err != nil {
		t.Fatalf("save edited: %v", err)
	}
	back, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	results["save"] = string(back)
	if string(back) != "edited\n" {
		t.Errorf("the server file holds %q after the save", back)
	}

	// Every command that reached the server is the /bin/sh wrapping: one
	// line, starting with the exec, holding no newline and no !.
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatalf("no commands reached the server: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no commands reached the server")
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "exec /bin/sh -c ") {
			t.Errorf("command not wrapped for /bin/sh: %q", line)
		}
		if strings.ContainsRune(line, '\n') {
			t.Errorf("command holds a newline: %q", line)
		}
		if strings.ContainsRune(line, '!') {
			t.Errorf("command holds a !: %q", line)
		}
	}
	results["commands"] = fmt.Sprint(len(lines))

	// Nothing the commands ran wrote into the login's HOME, and the
	// askpass script the fetch wrote on the server is gone again.
	dirEmpty(t, home)
	left, err := filepath.Glob(filepath.Join(tmpdir, "branchline-askpass.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("askpass files left on the server: %v", left)
	}
	results["askpass-clean"] = "yes"

	return results
}

// TestLoginShellAskpassFile checks that the printf format string writes
// the askpass script byte for byte as scriptFor builds it, through every
// shell a login can use.
func TestLoginShellAskpassFile(t *testing.T) {
	want := scriptFor("R")
	for _, shell := range loginShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s on this machine", shell)
			}
			env, home := shellEnv(t)
			tmpdir := t.TempDir()
			script := remoteAskSetup() + ` && cat "$__bl_ap" && rm -f "$__bl_ap"`
			cmd := exec.Command(shell, "-c", remoteShellCommand(script))
			cmd.Env = append(env, "TMPDIR="+tmpdir)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s: %v", shell, err)
			}
			if string(out) != want {
				t.Errorf("%s wrote a script of %d bytes, want %d byte for byte", shell, len(out), len(want))
			}
			left, err := filepath.Glob(filepath.Join(tmpdir, "branchline-askpass.*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 0 {
				t.Errorf("%s left the askpass file behind: %v", shell, left)
			}
			dirEmpty(t, home)
		})
	}
}
