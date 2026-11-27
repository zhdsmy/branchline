package sshtest

import (
	"os"
	"testing"
)

// TestMain keeps the tests away from the developer's ssh-agent. Without it,
// ssh started with AddKeysToAgent=yes adds each throwaway test key to that
// agent, and the agent's keys are offered to the loopback servers.
func TestMain(m *testing.M) {
	os.Unsetenv("SSH_AUTH_SOCK")
	os.Exit(m.Run())
}
