//go:build unix

package git

import (
	"os/exec"
	"syscall"
	"time"
)

func configureShellCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// configureGitCancel makes canceling a git command on this Mac stop the
// whole process group: the children git starts, git-remote-https or the
// ssh of a remote among them, keep the pipes open after git dies, and the
// wait for them would not end.
func configureGitCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = cancelWait
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
