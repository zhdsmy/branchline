package sshtest

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestOracleHelpersMultiplexAndConfig verifies that the oracle helpers
// (InitRepo, RunGit, WriteFile, ReadFile) run over the multiplexed
// ControlMaster connection — observable because only a multiplexed ssh client
// leaves a ControlPath socket — and that InitRepo/InitBareRepo apply exactly
// the repo-local Git settings the per-key git config calls set before.
func TestOracleHelpersMultiplexAndConfig(t *testing.T) {
	var sshdPID int

	t.Run("active", func(t *testing.T) {
		srv := StartLoopback(t)
		sshdPID = srv.PID

		// No test executor is ever built, so any ControlMaster socket must
		// come from the oracle helpers multiplexing.
		dir := srv.InitRepo(t, "oracle")
		bare := srv.InitBareRepo(t, "oracle-origin")
		srv.WriteFile(t, dir, "sub/note.txt", []byte("note\n"))
		if got := string(srv.ReadFile(t, dir, "sub/note.txt")); got != "note\n" {
			t.Fatalf("ReadFile = %q, want note", got)
		}

		sockets := 0
		for _, cp := range srv.ControlPaths() {
			if info, err := os.Stat(cp); err == nil && info.Mode()&os.ModeSocket != 0 {
				sockets++
			}
		}
		if sockets == 0 {
			t.Fatalf("no active ControlMaster socket after oracle-only use, checked %v: oracle helpers are not multiplexing", srv.ControlPaths())
		}

		// The oracle connection must answer through the same server and shell
		// semantics as before: git reads the appended settings.
		for _, kv := range [][2]string{
			{"user.name", "Branchline Test"},
			{"user.email", "test@branchline.invalid"},
			{"commit.gpgsign", "false"},
			{"tag.gpgsign", "false"},
			{"core.hooksPath", "/dev/null"},
			{"core.autocrlf", "false"},
			{"gc.auto", "0"},
			{"maintenance.auto", "false"},
		} {
			if got := srv.RunGit(t, dir, "config", "--get", kv[0]); got != kv[1] {
				t.Fatalf("repo config %s = %q, want %q", kv[0], got, kv[1])
			}
		}
		for _, kv := range [][2]string{
			{"core.hooksPath", "/dev/null"},
			{"gc.auto", "0"},
			{"maintenance.auto", "false"},
		} {
			if got := srv.RunGit(t, bare, "config", "--get", kv[0]); got != kv[1] {
				t.Fatalf("bare config %s = %q, want %q", kv[0], got, kv[1])
			}
		}
		if head := srv.RunGit(t, bare, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
			t.Fatalf("bare HEAD = %q, want refs/heads/main", head)
		}

		// The initial commit is unchanged and signing stays off in practice:
		// committing without any key configured must succeed.
		repo := srv.Repo(t, dir)
		if err := repo.Stage("sub/note.txt"); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := repo.Commit("second commit", false); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if subj := srv.RunGit(t, dir, "log", "-1", "--format=%s"); subj != "second commit" {
			t.Fatalf("log subject = %q, want second commit", subj)
		}
		if first := srv.RunGit(t, dir, "log", "--format=%s", "--reverse"); first == "" || first[:len("initial commit")] != "initial commit" {
			t.Fatalf("first commit subject missing in %q", first)
		}

		// RunShell stays the fresh-connection canary: it answers without
		// multiplexing, so it still exercises new TCP connections.
		if got := srv.RunShell(t, "printf canary"); got != "canary" {
			t.Fatalf("RunShell canary = %q, want canary", got)
		}
	})

	if sshdPID == 0 {
		t.Skip("StartLoopback skipped in subtest")
	}
	if err := syscall.Kill(-sshdPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("sshd process group %d still alive after cleanup: %v", sshdPID, err)
	}
}
