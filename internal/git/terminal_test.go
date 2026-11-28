package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TerminalCommand of a fully configured executor is exactly the ssh program,
// -t, the reuse-only multiplexing options, the port, the identity key, the
// user, the host, and one server command — without the batch-oriented
// options of the app's own connections.
func TestTerminalCommandArgsExact(t *testing.T) {
	ex := &SSHExecutor{
		Host:     "box.example.test",
		User:     "alice",
		Port:     2222,
		Identity: "~/keys/id_test",
		Dir:      "/srv/app",
		SSH:      "/opt/test/ssh",
	}
	argv := ex.TerminalCommand()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/test/ssh", "-t",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=" + ex.controlPath(),
		"-p", "2222",
		"-i", home + "/keys/id_test", "-o", "IdentitiesOnly=yes",
		"-l", "alice",
		"box.example.test",
		argv[len(argv)-1],
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want exactly %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q; full argv %q", i, argv[i], want[i], argv)
		}
	}
	for _, a := range argv {
		for _, banned := range []string{"BatchMode", "StrictHostKeyChecking", "ConnectTimeout", "ServerAliveInterval", "ControlMaster=auto", "ControlPersist"} {
			if strings.Contains(a, banned) {
				t.Fatalf("argv holds %q, which must not appear in an interactive login: %q", banned, argv)
			}
		}
	}
	remote := argv[len(argv)-1]
	if !strings.HasPrefix(remote, "/bin/sh -c ") {
		t.Fatalf("server command %q does not start with /bin/sh -c : a fish or tcsh login shell would run it with its own syntax", remote)
	}
	if !strings.Contains(remote, `exec "${SHELL:-/bin/sh}" -l`) {
		t.Fatalf("server command %q does not end in the login shell's exec", remote)
	}
}

// Without a custom program the first element is the ssh sshProgram() finds;
// the active keys replace the identity file, the reuse-only multiplexing
// options vanish with NoMultiplex, and the login parts drop out when empty.
func TestTerminalCommandVariants(t *testing.T) {
	ex := &SSHExecutor{Host: "box.example.test", Dir: "/srv/app"}
	argv := ex.TerminalCommand()
	if argv[0] != sshProgram() {
		t.Fatalf("argv[0] = %q, want the found ssh program %q", argv[0], sshProgram())
	}
	for _, unwanted := range []string{"-p", "-i", "-l"} {
		for _, a := range argv[1 : len(argv)-2] {
			if a == unwanted {
				t.Fatalf("argv holds a lone %q without its value being set: %q", unwanted, argv)
			}
		}
	}

	withKeys := &SSHExecutor{Host: "box.example.test", Dir: "/srv/app", SSHKeys: []string{"/keys/one", "/keys/t wo"}}
	argv = withKeys.TerminalCommand()
	want := []string{"-i", "/keys/one", "-i", "/keys/t wo", "-o", "AddKeysToAgent=yes"}
	at := -1
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j := range want {
			if argv[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("argv %q does not hold the key arguments %q", argv, want)
	}
	if strings.Contains(strings.Join(argv, " "), "IdentitiesOnly") {
		t.Fatalf("keys from the settings must not pin the identity: %q", argv)
	}

	noMux := &SSHExecutor{Host: "box.example.test", Dir: "/srv/app", NoMultiplex: true}
	for _, a := range noMux.TerminalCommand() {
		if strings.Contains(a, "ControlPath") || strings.Contains(a, "ControlMaster") {
			t.Fatalf("NoMultiplex argv holds %q: %q", a, noMux.TerminalCommand())
		}
	}
}

// The server command really lands in the repository's directory however the
// directory is named: spaces, quotes, dollar signs and non-ASCII bytes reach
// the cd unharmed, and a leading ~/ expands on the server side. The shell
// the exec falls into is faked with one that prints its working directory.
func TestTerminalCommandShellExecution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fakeShell := filepath.Join(home, "fakeshell")
	must(t, os.WriteFile(fakeShell, []byte("#!/bin/sh\npwd -P\n"), 0o755))
	t.Setenv("SHELL", fakeShell)

	base := t.TempDir()
	resolvedBase, err := filepath.EvalSymlinks(base)
	must(t, err)
	resolvedHome, err := filepath.EvalSymlinks(home)
	must(t, err)

	dirs := []struct {
		name string
		make func() (dir, want string)
	}{
		{"space", func() (string, string) {
			d := filepath.Join(base, "repo with space")
			return d, filepath.Join(resolvedBase, "repo with space")
		}},
		{"quote", func() (string, string) {
			d := filepath.Join(base, "it's here")
			return d, filepath.Join(resolvedBase, "it's here")
		}},
		{"dollar", func() (string, string) {
			d := filepath.Join(base, "pri$e `whoami`")
			return d, filepath.Join(resolvedBase, "pri$e `whoami`")
		}},
		{"chinese", func() (string, string) {
			d := filepath.Join(base, "仓库 目录")
			return d, filepath.Join(resolvedBase, "仓库 目录")
		}},
		{"tilde", func() (string, string) {
			return "~/under home", filepath.Join(resolvedHome, "under home")
		}},
	}

	runShell := func(t *testing.T, shell, remote, dir string) string {
		t.Helper()
		cmd := exec.Command(shell, "-c", remote)
		cmd.Dir = home
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s -c of the server command for %q: %v\n%s", shell, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	for _, tc := range dirs {
		t.Run(tc.name, func(t *testing.T) {
			dir, want := tc.make()
			// want is the real directory the cd must land in, already
			// resolved; a dir starting with ~/ is never a literal path.
			must(t, os.MkdirAll(want, 0o755))
			ex := &SSHExecutor{Host: "box.example.test", Dir: dir}
			argv := ex.TerminalCommand()
			remote := argv[len(argv)-1]
			if !strings.HasPrefix(remote, "/bin/sh -c ") {
				t.Fatalf("server command %q does not start with /bin/sh -c", remote)
			}
			got := runShell(t, "/bin/sh", remote, dir)
			if got != want {
				t.Fatalf("sh landed in %q, want exactly %q", got, want)
			}
			if zsh, err := exec.LookPath("zsh"); err == nil {
				if got := runShell(t, zsh, remote, dir); got != want {
					t.Fatalf("zsh landed in %q, want exactly %q", got, want)
				}
			}
		})
	}
}

// A real login through the loopback sshd lands in the repository's
// directory: pwd — fed to the login shell's standard input — prints it back
// over the connection even when its name holds a space and a quote. ssh
// notes the missing terminal for -t on its own, which does not stop the
// session.
func TestTerminalCommandLoopbackLogin(t *testing.T) {
	srv := startSSHD(t)
	dir := filepath.Join(t.TempDir(), "repo with 'quote'")
	must(t, os.MkdirAll(dir, 0o755))

	ex := srv.executor(t, dir)
	argv := ex.TerminalCommand()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader("pwd\nexit\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("interactive login through the loopback sshd: %v\n%s", err, out)
	}
	resolved, rerr := filepath.EvalSymlinks(dir)
	if rerr != nil {
		resolved = dir
	}
	text := string(out)
	if !strings.Contains(text, dir) && !strings.Contains(text, resolved) {
		t.Fatalf("the remote shell printed no line holding the repository directory %q:\n%s", dir, text)
	}
}
