//go:build !unix

package git

import (
	"os/exec"
	"time"
)

func configureShellCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
}
