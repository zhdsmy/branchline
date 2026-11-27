package app

import (
	"fmt"
	"os"
	"testing"
)

// originalTempDir is the temporary directory the test process inherited,
// before TestMain gives it a private one. TestTempDirIsPerProcess reads it.
var originalTempDir string

// TestMain keeps the tests away from the developer's ssh-agent. Without it,
// ssh started with AddKeysToAgent=yes adds each throwaway test key to that
// agent, and the agent's keys are offered to the loopback servers.
//
// The tests also run with their own temporary directory: on macOS every
// process of one user shares the same default, and the leak checks count
// the temporary directories a run leaves behind. A concurrent test process
// creating one of those directories in between would look exactly like a
// leak. git, ssh, and everything else the tests spawn inherit TMPDIR, so
// their temporary files land inside it as well. The directory is removed
// when the tests end.
func TestMain(m *testing.M) {
	os.Unsetenv("SSH_AUTH_SOCK")

	originalTempDir = os.TempDir()
	dir, err := os.MkdirTemp("", "branchline-apptest-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create per-process temporary directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", dir); err != nil {
		fmt.Fprintf(os.Stderr, "set TMPDIR to per-process directory: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "remove per-process temporary directory %s: %v\n", dir, err)
	}
	os.Exit(code)
}
