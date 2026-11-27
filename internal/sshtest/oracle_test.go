package sshtest

import (
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// TestOracleHelpersMultiplexAndConfig verifies that the oracle helpers
// (InitRepo, RunGit, WriteFile, ReadFile) run over their own multiplexed
// ControlMaster connection — distinct from the executors under test and
// cleaned up on teardown — and that InitRepo/InitBareRepo apply exactly the
// repo-local Git settings the per-key git config calls set before.
func TestOracleHelpersMultiplexAndConfig(t *testing.T) {
	var (
		sshdPID      int
		oracleSocket string
		repoSocket   string
	)

	t.Run("active", func(t *testing.T) {
		srv := StartLoopback(t)
		sshdPID = srv.PID

		// No test executor is ever built yet, so the active ControlMaster
		// socket must belong to the oracle helpers.
		dir := srv.InitRepo(t, "oracle")
		bare := srv.InitBareRepo(t, "oracle-origin")
		srv.WriteFile(t, dir, "sub/note.txt", []byte("note\n"))
		if got := string(srv.ReadFile(t, dir, "sub/note.txt")); got != "note\n" {
			t.Fatalf("ReadFile = %q, want note", got)
		}

		oracleSocket = srv.oracleExecutor().ControlPath()
		if !slices.Contains(srv.ControlPaths(), oracleSocket) {
			t.Fatalf("ControlPaths() = %v, missing oracle socket %q", srv.ControlPaths(), oracleSocket)
		}
		if info, err := os.Stat(oracleSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("expected active oracle ControlMaster socket %s after oracle-only use (err=%v)", oracleSocket, err)
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

		// The oracle ControlPath must never coincide with the code-under-test
		// ControlPath (with or without explicit Identity).
		repoEx := srv.Executor(t, dir)
		repoSocket = repoEx.ControlPath()
		noIDSocket := (&git.SSHExecutor{
			Host: srv.Host,
			Port: srv.Port,
			User: srv.User,
			Dir:  dir,
		}).ControlPath()
		if oracleSocket == repoSocket {
			t.Fatalf("oracle ControlPath %q collides with target Executor ControlPath", oracleSocket)
		}
		if oracleSocket == noIDSocket {
			t.Fatalf("oracle ControlPath %q collides with identity-less ControlPath", oracleSocket)
		}

		// The initial commit is unchanged and signing stays off in practice:
		// committing without any key configured must succeed.
		repo := git.New(repoEx)
		if err := repo.Stage("sub/note.txt"); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := repo.Commit("second commit", false); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if info, err := os.Stat(repoSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("expected active target ControlMaster socket %s after repo commands (err=%v)", repoSocket, err)
		}
		if subj := srv.RunGit(t, dir, "log", "-1", "--format=%s"); subj != "second commit" {
			t.Fatalf("log subject = %q, want second commit", subj)
		}
		if first := srv.RunGit(t, dir, "log", "--format=%s", "--reverse"); !strings.HasPrefix(first, "initial commit") {
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
	for _, cp := range []string{oracleSocket, repoSocket} {
		if _, err := os.Stat(cp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ControlMaster socket %s still exists after cleanup (err=%v)", cp, err)
		}
	}
}

// TestOracleExecutorConfiguredLinuxUnmultiplexed verifies that non-loopback
// targets (configured-linux) and loopback targets without an oracle key copy
// fall back to unmultiplexed probe connections.
func TestOracleExecutorConfiguredLinuxUnmultiplexed(t *testing.T) {
	t.Setenv(EnvLinuxHost, "bl-sshlab-e1")
	t.Setenv(EnvLinuxRepo, "/tmp/branchline-test-e1")
	t.Setenv(EnvRequireLinux, "1")

	linuxTarget := configuredLinux(t, false)
	if ex := linuxTarget.oracleExecutor(); !ex.NoMultiplex {
		t.Fatalf("configured-linux oracleExecutor().NoMultiplex = false, want true")
	}

	loopbackWithoutOracleKey := &Target{
		Kind:    KindLoopback,
		Host:    "127.0.0.1",
		Port:    2222,
		User:    "tester",
		RootDir: "/tmp/branchline-test-unused",
	}
	if ex := loopbackWithoutOracleKey.oracleExecutor(); !ex.NoMultiplex {
		t.Fatalf("loopback target without oracleIdentity oracleExecutor().NoMultiplex = false, want true")
	}
}
