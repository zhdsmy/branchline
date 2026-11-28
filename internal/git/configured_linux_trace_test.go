package git

import (
	"context"
	"testing"
	"time"
)

// TestConfiguredLinuxTraceSSH runs git on the configured Linux server with
// the SSH trace on: the server's shell — dash on most sshlab images, not
// the Mac's zsh — must read the quoted GIT_SSH_COMMAND='ssh -vvv' as one
// assignment, so Status succeeds.
func TestConfiguredLinuxTraceSSH(t *testing.T) {
	target := resolveConfiguredLinuxSSHTarget(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sh, err := target.directEx.RunShell(ctx, "ls -l /bin/sh")
	must(t, err)
	t.Logf("server git %q, /bin/sh: %s", target.gitVersionRaw, sh)

	ex := &SSHExecutor{Host: target.hostAlias, Dir: target.rootRepo, NoMultiplex: true, Trace: Trace{SSH: true}}
	if _, err := New(ex).Status(); err != nil {
		t.Fatalf("status with the SSH trace on the Linux server: %v", err)
	}
}
