//go:build !unix

package git

import (
	"os/exec"
	"time"
)

func configureShellCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}

// configureGitCancel bounds the wait for the pipes of a canceled git
// command; without process groups there is no way to reach its children.
func configureGitCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = cancelWait
}
