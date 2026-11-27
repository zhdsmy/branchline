package git

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps the tests away from the developer's ssh-agent. Without it,
// ssh started with AddKeysToAgent=yes adds each throwaway test key to that
// agent, and the agent's keys are offered to the loopback servers.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		fmt.Fprintf(os.Stderr, "cannot unset SSH_AUTH_SOCK: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
