package git

import (
	"strings"
	"testing"
)

func TestWithoutPromptsKeepsSSHConnectionSettings(t *testing.T) {
	original := &SSHExecutor{Host: "example", User: "user", Dir: "~/repo", Port: 2222, Identity: "/key", AskPass: &AskPass{}}
	passive := New(original).WithoutPrompts().Exec.(*SSHExecutor)
	if passive.AskPass != nil || original.AskPass == nil || passive.controlPath() != original.controlPath() {
		t.Fatal("poll changed interactive auth or connection identity")
	}
	args := strings.Join(passive.SSHArgs("git status"), " ")
	if !strings.Contains(args, "BatchMode=yes") || !strings.Contains(args, "-p 2222") || !strings.Contains(args, "-i /key") {
		t.Fatalf("passive SSH args: %s", args)
	}
	if passive.Dir != original.Dir || passive.User != original.User {
		t.Fatal("poll changed repository location")
	}
}
