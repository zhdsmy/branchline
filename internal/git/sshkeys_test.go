package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The active SSH keys are offered by the ssh that a local git starts and
// by the ssh that reaches a server.
func TestActiveSSHKeysAreOffered(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	prog := filepath.Join(t.TempDir(), "fakegit")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nprintf '%s' \"$GIT_SSH_COMMAND\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ex := &LocalExecutor{Dir: t.TempDir(), Git: prog, SSHKeys: []string{"/k/work key", "/k/home"}}
	out, err := ex.Run(context.Background(), nil, "fetch")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "ssh -i '/k/work key' -i /k/home -o AddKeysToAgent=yes"; got != want {
		t.Fatalf("GIT_SSH_COMMAND %q, want %q", got, want)
	}

	ex.SSHKeys, ex.Trace = nil, Trace{SSH: true}
	out, _ = ex.Run(context.Background(), nil, "fetch")
	if string(out) != "ssh -vvv" {
		t.Fatalf("without keys the trace alone gives %q", out)
	}

	server := &SSHExecutor{Host: "example.com", Dir: "/r", SSHKeys: []string{"/k/a", "/k/b"}}
	args := strings.Join(server.SSHArgs("X"), " ")
	if !strings.Contains(args, "-i /k/a -i /k/b -o AddKeysToAgent=yes") {
		t.Fatalf("ssh args %q", args)
	}
	// A key chosen for the server itself is the only one offered.
	server.Identity = "/k/own"
	args = strings.Join(server.SSHArgs("X"), " ")
	if !strings.Contains(args, "-i /k/own -o IdentitiesOnly=yes") || strings.Contains(args, "/k/a") {
		t.Fatalf("ssh args %q", args)
	}
}
