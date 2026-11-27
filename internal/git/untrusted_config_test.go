package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// untrustedGit runs a plain git command in dir with only the repository's
// own configuration in effect, as a user typing in a terminal would. It is
// the control of these tests: what the repository's configuration does to a
// command that does not defend itself.
func untrustedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := untrustedGitMayFail(t, dir, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func untrustedGitMayFail(t *testing.T, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd.CombinedOutput()
}

// fsmonitorHook writes a script that does nothing but touch marker, and
// returns the script's path.
func fsmonitorHook(t *testing.T, marker string) string {
	t.Helper()
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	script := "#!/bin/sh\ntouch " + git.ShellQuote(marker) + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return hook
}

// hostileFSMonitorRepo builds a repository whose own configuration names an
// fsmonitor command: one committed file, one modified and one untracked in
// the worktree. It returns the repository's directory and the marker the
// configured command touches when something runs it.
func hostileFSMonitorRepo(t *testing.T) (dir, marker string) {
	t.Helper()
	scratch := t.TempDir()
	dir = filepath.Join(scratch, "repo")
	marker = filepath.Join(scratch, "fsmonitor-ran")
	untrustedGit(t, "", "init", "-q", "-b", "main", dir)
	untrustedGit(t, dir, "config", "user.name", "Branchline Test")
	untrustedGit(t, dir, "config", "user.email", "test@branchline.invalid")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untrustedGit(t, dir, "add", "a.txt", "tracked.txt")
	untrustedGit(t, dir, "commit", "-q", "-m", "initial commit")
	// The skip-worktree bit gives the ls-files read below something to
	// prove itself with: IndexFlags only lists flagged paths.
	untrustedGit(t, dir, "update-index", "--skip-worktree", "tracked.txt")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untrustedGit(t, dir, "config", "core.fsmonitor", fsmonitorHook(t, marker))
	return dir, marker
}

// Opening a repository must never execute the program its own configuration
// names in core.fsmonitor. The executor's status and ls-files reads leave
// the marker untouched and still report the worktree, while plain git in the
// same repository does run the program, which proves the trap is live.
func TestUntrustedConfigFSMonitorLocal(t *testing.T) {
	dir, marker := hostileFSMonitorRepo(t)

	r := git.New(&git.LocalExecutor{Dir: dir})
	st, err := r.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	flags, err := r.IndexFlags()
	if err != nil {
		t.Fatalf("IndexFlags: %v", err)
	}
	if len(flags) != 1 || flags[0].Path != "tracked.txt" || !flags[0].SkipWorktree {
		t.Errorf("IndexFlags: %+v, want tracked.txt with skip-worktree", flags)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the executor's reads executed the repository's core.fsmonitor program")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	var unstaged []string
	for _, f := range st.Unstaged {
		unstaged = append(unstaged, string(f.Status)+" "+f.Path)
	}
	if strings.Join(unstaged, ",") != "M a.txt,? untracked.txt" {
		t.Errorf("unstaged: %v", unstaged)
	}

	// Control: the same reads without the executor's protection do run the
	// program, so the absence above cannot be a broken trap.
	untrustedGit(t, dir, "status", "--porcelain")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control read did not execute core.fsmonitor: the trap itself is broken: %v", err)
	}
}

// Commands that must keep Git's default pathspec mode — those with an
// explicit glob pathspec, and stash commands without a pathspec — drop
// --literal-pathspecs but must carry every defensive -c of the shared
// configuration.
func TestUntrustedConfigGlobAndStashArgs(t *testing.T) {
	ex := &git.SSHExecutor{Host: "example.com", Dir: "/repo", NoMultiplex: true}
	for _, args := range [][]string{
		{"log", "--", ":(glob)**/*.txt"},
		{"stash", "list"},
		{"stash", "push", "-m", "wip", "--", "a.txt"},
	} {
		remote := ex.RemoteCommand(args...)
		for _, want := range []string{"core.fsmonitor=false", "protocol.ext.allow=never"} {
			if !strings.Contains(remote, want) {
				t.Errorf("%v: remote command lacks %s: %s", args, want, remote)
			}
		}
	}
	if remote := ex.RemoteCommand("log", "--", ":(glob)**/*.txt"); strings.Contains(remote, "--literal-pathspecs") {
		t.Errorf("a glob pathspec command kept --literal-pathspecs: %s", remote)
	}
	if remote := ex.RemoteCommand("stash", "list"); strings.Contains(remote, "--literal-pathspecs") {
		t.Errorf("a pathspec-less stash command kept --literal-pathspecs: %s", remote)
	}
	if remote := ex.RemoteCommand("stash", "push", "-m", "wip", "--", "a.txt"); !strings.Contains(remote, "--literal-pathspecs") {
		t.Errorf("a stash command with pathspecs lost --literal-pathspecs: %s", remote)
	}
}

// A repository can turn the ext transport back on in its own configuration
// and point a remote at an ext:: address, which runs the command in that
// address. Commands through the executor must refuse it; plain git does not.
func TestUntrustedConfigExtTransport(t *testing.T) {
	scratch := t.TempDir()
	dir := filepath.Join(scratch, "repo")
	marker := filepath.Join(scratch, "ext-ran")
	untrustedGit(t, "", "init", "-q", "-b", "main", dir)
	untrustedGit(t, dir, "config", "user.name", "Branchline Test")
	untrustedGit(t, dir, "config", "user.email", "test@branchline.invalid")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	untrustedGit(t, dir, "add", "a.txt")
	untrustedGit(t, dir, "commit", "-q", "-m", "initial commit")
	untrustedGit(t, dir, "config", "protocol.ext.allow", "always")
	untrustedGit(t, dir, "remote", "add", "evil", "ext::touch "+marker)

	ctx := context.Background()
	ex := &git.LocalExecutor{Dir: dir}
	if _, err := ex.Run(ctx, nil, "ls-remote", "evil"); err == nil {
		t.Error("ls-remote of an ext:: remote succeeded through the executor")
	}
	if _, err := ex.Run(ctx, nil, "fetch", "evil"); err == nil {
		t.Error("fetch of an ext:: remote succeeded through the executor")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the executor's remote command executed the ext:: address")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Control: plain git honors the repository's protocol.ext.allow and runs
	// the command in the address, so the trap itself is live.
	if out, err := untrustedGitMayFail(t, dir, "ls-remote", "evil"); err == nil {
		t.Fatalf("control ls-remote unexpectedly succeeded:\n%s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control ls-remote did not execute the ext:: address: the trap itself is broken: %v", err)
	}
}

// The defense reaches the server as well: a repository on the other end of
// SSH cannot make the executor's reads run its core.fsmonitor program.
func TestUntrustedConfigFSMonitorSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	workDir := srv.InitRepo(t, "untrusted-fsmonitor")
	scratch := srv.TempDir(t, "untrusted-fsmonitor-scratch")
	marker := scratch + "/fsmonitor-ran"
	srv.WriteFile(t, scratch, "fsmonitor-hook", []byte("#!/bin/sh\ntouch "+git.ShellQuote(marker)+"\n"))
	srv.RunShell(t, "chmod +x "+git.ShellQuote(scratch+"/fsmonitor-hook"))
	srv.WriteFile(t, workDir, "untracked.txt", []byte("new\n"))
	srv.RunGit(t, workDir, "config", "core.fsmonitor", scratch+"/fsmonitor-hook")

	repo := srv.Repo(t, workDir)
	st, err := repo.Status()
	if err != nil {
		t.Fatalf("Status over SSH: %v", err)
	}
	found := false
	for _, f := range st.Unstaged {
		if f.Path == "untracked.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("untracked.txt missing in Status over SSH: %+v", st.Unstaged)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the executor's reads over SSH executed the repository's core.fsmonitor program")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
