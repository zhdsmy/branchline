package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoteTraceLoopbackSSHD runs git on a repository through a real sshd
// with the SSH trace on: the server's shell must read the quoted
// GIT_SSH_COMMAND='ssh -vvv' as one assignment, so the commands succeed
// instead of failing with "-vvv: command not found".
func TestRemoteTraceLoopbackSSHD(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)

	ex := srv.executor(t, dir)
	ex.Trace = Trace{SSH: true}
	r := New(ex)
	if _, err := r.Status(); err != nil {
		t.Fatalf("status with the SSH trace: %v", err)
	}
	if _, err := r.Log(LogOptions{Limit: 1}); err != nil {
		t.Fatalf("log with the SSH trace: %v", err)
	}

	every := srv.executor(t, dir)
	every.Trace = AllTraces()
	r = New(every)
	if _, err := r.Status(); err != nil {
		t.Fatalf("status with every trace: %v", err)
	}
	if _, err := r.Log(LogOptions{Limit: 1}); err != nil {
		t.Fatalf("log with every trace: %v", err)
	}
}

// TestRemoteTraceServerGitEnv runs git through a stand-in ssh that parses
// the remote command with sh, the way the server's shell does, with the
// git of the server's PATH a recording stand-in: the git that then runs
// must find GIT_SSH_COMMAND=ssh -vvv in its environment as one value.
func TestRemoteTraceServerGitEnv(t *testing.T) {
	dir := testRepo(t)
	log := filepath.Join(t.TempDir(), "server-git-env.log")
	real, err := exec.LookPath("git")
	must(t, err)

	bin := t.TempDir()
	recorder := "#!/bin/sh\nprintf '%s\\n' \"${GIT_SSH_COMMAND-}\" >> " + ShellQuote(log) + "\nexec " + ShellQuote(real) + " \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(bin, "git"), []byte(recorder), 0o755))

	ssh := filepath.Join(t.TempDir(), "ssh")
	wrapper := `#!/bin/sh
while [ $# -gt 1 ]; do
  case "$1" in
    -o|-p|-i|-l) shift 2 ;;
    *) break ;;
  esac
done
host=$1; shift
[ "$host" = "devbox" ] || { echo "bad host $host" >&2; exit 255; }
export PATH=` + ShellQuote(bin) + `:"$PATH"
exec sh -c "$1"
`
	must(t, os.WriteFile(ssh, []byte(wrapper), 0o755))

	ex := &SSHExecutor{Host: "devbox", Dir: dir, SSH: ssh, NoMultiplex: true, Trace: Trace{SSH: true}}
	if _, err := New(ex).Status(); err != nil {
		t.Fatalf("status with the SSH trace: %v", err)
	}
	data, err := os.ReadFile(log)
	must(t, err)
	if strings.TrimRight(string(data), "\n") == "" {
		t.Fatal("no git of the server recorded its environment")
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "ssh -vvv" {
			t.Fatalf("GIT_SSH_COMMAND of the server's git %q, want one whole %q", line, "ssh -vvv")
		}
	}
}

// TestRemoteTraceLocalEnvUnquoted checks that a repository on this machine
// keeps the SSH trace unquoted: the value goes into the process
// environment of git, where the quotes the server's shell needs would
// become part of it.
func TestRemoteTraceLocalEnvUnquoted(t *testing.T) {
	log := filepath.Join(t.TempDir(), "local-git-env.log")
	real, err := exec.LookPath("git")
	must(t, err)
	recorder := "#!/bin/sh\nprintf '%s\\n' \"${GIT_SSH_COMMAND-}\" >> " + ShellQuote(log) + "\nexec " + ShellQuote(real) + " \"$@\"\n"
	fake := filepath.Join(t.TempDir(), "git")
	must(t, os.WriteFile(fake, []byte(recorder), 0o755))

	r := New(&LocalExecutor{Dir: testRepo(t), Git: fake, Trace: Trace{SSH: true}})
	if _, err := r.Status(); err != nil {
		t.Fatalf("local status with the SSH trace: %v", err)
	}
	data, err := os.ReadFile(log)
	must(t, err)
	if strings.TrimRight(string(data), "\n") == "" {
		t.Fatal("no local git recorded its environment")
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "ssh -vvv" {
			t.Fatalf("GIT_SSH_COMMAND of the local git %q, want unquoted %q", line, "ssh -vvv")
		}
	}
}
