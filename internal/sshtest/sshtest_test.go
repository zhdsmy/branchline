package sshtest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// TestLoopbackLifecycleAndCleanup verifies that StartLoopback starts an
// isolated sshd, runs real Git commands over a multiplexed ControlMaster
// connection, and leaves no sshd process, ControlMaster socket, or temporary
// directory behind after t.Cleanup runs, without modifying ~/.ssh.
func TestLoopbackLifecycleAndCleanup(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	beforeCfg := fileDigest(t, filepath.Join(homeDir, ".ssh", "config"))
	beforeKH := fileDigest(t, filepath.Join(homeDir, ".ssh", "known_hosts"))

	var (
		sshdPID      int
		baseDir      string
		rootDir      string
		controlPaths []string
	)

	t.Run("active", func(t *testing.T) {
		srv := StartLoopback(t)
		sshdPID = srv.PID
		baseDir = srv.baseDir
		rootDir = srv.RootDir

		if srv.Kind != KindLoopback {
			t.Fatalf("Kind = %q, want %q", srv.Kind, KindLoopback)
		}
		if srv.Host != "127.0.0.1" || srv.Port <= 0 || srv.User == "" || srv.Identity == "" || srv.SSH == "" {
			t.Fatalf("incomplete loopback target: %+v", srv)
		}
		if sshdPID <= 0 {
			t.Fatalf("expected positive sshd PID, got %d", sshdPID)
		}

		// Verify Address round-trips through store.ParseAddress and works
		// without setting Identity (the isolated wrapper supplies the default key).
		repoDir := srv.InitRepo(t, "lifecycle")
		addr := srv.Address(repoDir)
		parsed, err := store.ParseAddress(addr)
		if err != nil {
			t.Fatalf("store.ParseAddress(%q): %v", addr, err)
		}
		if parsed.Kind != store.SSH || parsed.Host != "127.0.0.1" || parsed.Port != srv.Port || parsed.User != srv.User || parsed.Path != repoDir {
			t.Fatalf("parsed address = %+v, want host=127.0.0.1 port=%d user=%q path=%q", parsed, srv.Port, srv.User, repoDir)
		}
		parsedEx := parsed.Executor().(*git.SSHExecutor)
		parsedEx.SSH = srv.SSHProgram()
		parsedRepo := git.New(parsedEx)
		top, err := parsedRepo.TopLevel()
		if err != nil || top != repoDir {
			t.Fatalf("parsedRepo.TopLevel() = (%q, %v), want (%q, nil)", top, err, repoDir)
		}

		// Verify srv.Repo uses ControlMaster multiplexing and creates an active socket.
		repo := srv.Repo(t, repoDir)
		st, err := repo.Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.Branch != "main" || st.Head == "" {
			t.Fatalf("unexpected initial status: %+v", st)
		}
		srv.WriteFile(t, repoDir, "sub/hello.txt", []byte("world\n"))
		got := srv.ReadFile(t, repoDir, "sub/hello.txt")
		if string(got) != "world\n" {
			t.Fatalf("ReadFile = %q, want %q", got, "world\n")
		}
		if err := repo.Stage("sub/hello.txt"); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		if err := repo.Commit("add hello", false); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if subj := srv.RunGit(t, repoDir, "log", "-1", "--format=%s"); subj != "add hello" {
			t.Fatalf("server oracle log subject = %q, want %q", subj, "add hello")
		}

		controlPaths = srv.ControlPaths()
		if len(controlPaths) == 0 {
			t.Fatal("expected at least one tracked ControlPath")
		}
		activeSockets := 0
		for _, cp := range controlPaths {
			if info, err := os.Stat(cp); err == nil && info.Mode()&os.ModeSocket != 0 {
				activeSockets++
			}
		}
		if activeSockets == 0 {
			t.Fatalf("expected active ControlMaster socket before cleanup, checked %v", controlPaths)
		}
	})

	// If StartLoopback skipped on an environment without sshd, do not run post-cleanup assertions.
	if sshdPID == 0 {
		t.Skip("StartLoopback skipped in subtest")
	}

	// Verify sshd and its process group have exited.
	if err := syscall.Kill(sshdPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("sshd pid %d still alive after t.Cleanup: %v", sshdPID, err)
	}
	if err := syscall.Kill(-sshdPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("sshd process group %d still has live processes after t.Cleanup: %v", sshdPID, err)
	}

	// Verify no ControlMaster socket survived cleanup.
	for _, cp := range controlPaths {
		if _, err := os.Stat(cp); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ControlMaster socket %s still exists after t.Cleanup (err=%v)", cp, err)
		}
	}

	// Verify temporary directories were removed.
	for _, p := range []string{baseDir, rootDir} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temp dir %s still exists after t.Cleanup (err=%v)", p, err)
		}
	}

	// Verify user ~/.ssh files were untouched.
	afterCfg := fileDigest(t, filepath.Join(homeDir, ".ssh", "config"))
	afterKH := fileDigest(t, filepath.Join(homeDir, ".ssh", "known_hosts"))
	if afterCfg != beforeCfg {
		t.Fatalf("~/.ssh/config changed during test: before=%s after=%s", beforeCfg, afterCfg)
	}
	if afterKH != beforeKH {
		t.Fatalf("~/.ssh/known_hosts changed during test: before=%s after=%s", beforeKH, afterKH)
	}
}

// TestLoopbackParallelInstances verifies that two loopback sshd instances can
// start, run independent Git operations, and clean up in parallel without
// port, key, or ControlMaster collisions.
func TestLoopbackParallelInstances(t *testing.T) {
	t.Run("group", func(t *testing.T) {
		for _, name := range []string{"instance-a", "instance-b"} {
			name := name
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				srv := StartLoopback(t)
				dir := srv.InitRepo(t, name)
				bare := srv.InitBareRepo(t, name)
				repo := srv.Repo(t, dir)

				srv.WriteFile(t, dir, name+".txt", []byte(name+"\n"))
				if err := repo.Stage(name + ".txt"); err != nil {
					t.Fatalf("Stage: %v", err)
				}
				if err := repo.Commit("commit "+name, false); err != nil {
					t.Fatalf("Commit: %v", err)
				}
				if err := repo.RemoteAdd("origin", bare, false); err != nil {
					t.Fatalf("RemoteAdd: %v", err)
				}
				if err := repo.Push("origin", "main", true, false, false); err != nil {
					t.Fatalf("Push: %v", err)
				}
				wantHead := srv.RunGit(t, dir, "rev-parse", "HEAD")
				gotBare := srv.RunGit(t, bare, "rev-parse", "refs/heads/main")
				if gotBare != wantHead {
					t.Fatalf("bare main = %q, want %q", gotBare, wantHead)
				}
			})
		}
	})
}

// TestLoopbackPassphraseKeyIsolation verifies that GenerateKey creates an
// encrypted key accepted by loopback sshd and that passing a wrong passphrase
// fails rather than falling back to the unlocked default key.
func TestLoopbackPassphraseKeyIsolation(t *testing.T) {
	srv := StartLoopback(t)
	dir := srv.InitRepo(t, "passphrase")
	lockedKey := srv.GenerateKey(t, "locked", "correct-pass")

	ex := srv.Executor(t, dir)
	ex.Identity = lockedKey
	ex.NoMultiplex = true
	var prompts []string
	ap := git.NewAskPass(func(prompt string) (string, bool) {
		prompts = append(prompts, prompt)
		return "correct-pass", true
	})
	defer ap.Close()
	ex.AskPass = ap

	if _, err := git.New(ex).Status(); err != nil {
		t.Fatalf("Status with correct passphrase failed: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatal("expected AskPass to be prompted for encrypted key")
	}

	// Wrong passphrase must fail instead of falling back to default user key.
	exWrong := srv.Executor(t, dir)
	exWrong.Identity = lockedKey
	exWrong.NoMultiplex = true
	apWrong := git.NewAskPass(func(string) (string, bool) { return "wrong-pass", true })
	defer apWrong.Close()
	exWrong.AskPass = apWrong

	done := make(chan error, 1)
	go func() {
		_, err := git.New(exWrong).Status()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected authentication failure with wrong passphrase, got nil")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("authentication with wrong passphrase timed out")
	}
}

// TestLoopbackRepeatedAuthFailuresDoNotTriggerPenalty verifies that repeated
// failed authentication attempts from 127.0.0.1 (such as promptless loads and
// cancelled askpass prompts with an encrypted key) do not cause OpenSSH 9.8+
// PerSourcePenalties to reset subsequent loopback connections or cleanup probes,
// and that a ControlMaster opened with a generated key is tracked and closed.
func TestLoopbackRepeatedAuthFailuresDoNotTriggerPenalty(t *testing.T) {
	var (
		sshdPID      int
		lockedSocket string
	)

	t.Run("active", func(t *testing.T) {
		srv := StartLoopback(t)
		sshdPID = srv.PID
		dir := srv.InitRepo(t, "penalty")
		lockedKey := srv.GenerateKey(t, "locked", "s3cret")

		lockedEx := srv.Executor(t, dir)
		lockedEx.Identity = lockedKey
		lockedSocket = lockedEx.ControlPath()

		// Trigger 5 rapid auth failures from 127.0.0.1 (5 * 5s = 25s > default
		// OpenSSH PerSourcePenalties min:15s threshold).
		for i := 0; i < 5; i++ {
			failEx := srv.Executor(t, dir)
			failEx.Identity = lockedKey
			failEx.NoMultiplex = true
			if _, err := git.New(failEx).Status(); err == nil {
				t.Fatalf("attempt %d unexpectedly succeeded with locked key in BatchMode", i+1)
			}
		}

		// Immediate probe connection must not be reset by PerSourcePenalties.
		if got := srv.RunShell(t, "printf alive"); got != "alive" {
			t.Fatalf("probe after repeated auth failures = %q, want alive", got)
		}

		// GenerateKey must pre-track the generated identity's ControlPath even
		// before any multiplexed connection is made.
		foundTracked := false
		for _, cp := range srv.ControlPaths() {
			if cp == lockedSocket {
				foundTracked = true
				break
			}
		}
		if !foundTracked {
			t.Fatalf("ControlPaths() = %v, missing generated key control path %q", srv.ControlPaths(), lockedSocket)
		}

		// Opening a multiplexed connection with the generated key's passphrase
		// must succeed and create the tracked ControlMaster socket.
		ap := git.NewAskPass(func(string) (string, bool) { return "s3cret", true })
		defer ap.Close()
		lockedEx.AskPass = ap
		if _, err := git.New(lockedEx).Status(); err != nil {
			t.Fatalf("multiplexed Status after repeated auth failures failed: %v", err)
		}
		if info, err := os.Stat(lockedSocket); err != nil || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("expected active ControlMaster socket %s after unlock (err=%v)", lockedSocket, err)
		}
	})

	if sshdPID == 0 {
		t.Skip("StartLoopback skipped in subtest")
	}
	if err := syscall.Kill(-sshdPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("sshd process group %d still alive after cleanup: %v", sshdPID, err)
	}
	if _, err := os.Stat(lockedSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated-key ControlMaster socket %s survived cleanup (err=%v)", lockedSocket, err)
	}
}

// TestStartLoopbackSkipAndRequirePolicy verifies that StartLoopback skips by
// default when sshd is unavailable and fails when BRANCHLINE_REQUIRE_SSH=1.
func TestStartLoopbackSkipAndRequirePolicy(t *testing.T) {
	missingSSHD := defaultLoopbackDeps()
	missingSSHD.findSSHD = func() (string, error) { return "", errors.New("simulated missing sshd") }

	t.Run("skip-by-default", func(t *testing.T) {
		t.Setenv(EnvRequireSSH, "")
		res := runWithStubTB(t, func(tb testing.TB) {
			startLoopback(tb, missingSSHD)
		})
		if !res.skipped || res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want skipped=true failed=false", res.skipped, res.failed, res.message)
		}
		if !strings.Contains(res.message, "simulated missing sshd") {
			t.Fatalf("skip reason = %q, want cause detail", res.message)
		}
	})

	t.Run("fail-when-required", func(t *testing.T) {
		t.Setenv(EnvRequireSSH, "1")
		res := runWithStubTB(t, func(tb testing.TB) {
			startLoopback(tb, missingSSHD)
		})
		if res.skipped || !res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want skipped=false failed=true", res.skipped, res.failed, res.message)
		}
		if !strings.Contains(res.message, EnvRequireSSH+"=1") {
			t.Fatalf("failure message = %q, want %s=1", res.message, EnvRequireSSH)
		}
	})

	t.Run("non-port-conflict-stops-immediately", func(t *testing.T) {
		t.Setenv(EnvRequireSSH, "1")
		fakeSSHD := filepath.Join(t.TempDir(), "fake-sshd")
		if err := os.WriteFile(fakeSSHD, []byte("#!/bin/sh\necho 'bad sshd config' >&2\nexit 2\n"), 0o755); err != nil {
			t.Fatalf("write fake sshd: %v", err)
		}
		listenCalls := 0
		deps := defaultLoopbackDeps()
		deps.findSSHD = func() (string, error) { return fakeSSHD, nil }
		deps.listen = func(network, address string) (net.Listener, error) {
			listenCalls++
			return net.Listen(network, address)
		}
		res := runWithStubTB(t, func(tb testing.TB) {
			startLoopback(tb, deps)
		})
		if res.skipped || !res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want failure", res.skipped, res.failed, res.message)
		}
		if listenCalls != 1 {
			t.Fatalf("listenCalls = %d, want 1 (must stop retrying on non-port-conflict error); msg=%q", listenCalls, res.message)
		}
	})

	t.Run("sanitize-prefix-rejects-url-delimiters", func(t *testing.T) {
		for _, bad := range []string{"repo#1", "repo?q", "repo%20"} {
			if _, err := sanitizePrefix(bad); err == nil {
				t.Fatalf("sanitizePrefix(%q) succeeded, want error", bad)
			}
		}
	})

	t.Run("persourcepenalties-detection-handles-supported-and-legacy-sshd", func(t *testing.T) {
		dir := t.TempDir()
		dummyKey := filepath.Join(dir, "host_key")
		if err := os.WriteFile(dummyKey, []byte("dummy"), 0o600); err != nil {
			t.Fatalf("write dummy key: %v", err)
		}
		modernSSHD := filepath.Join(dir, "sshd-modern")
		modernScript := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"PerSourcePenalties=no\" ]; then exit 0; fi\ndone\nexit 1\n"
		if err := os.WriteFile(modernSSHD, []byte(modernScript), 0o755); err != nil {
			t.Fatalf("write modern fake sshd: %v", err)
		}
		legacySSHD := filepath.Join(dir, "sshd-legacy")
		if err := os.WriteFile(legacySSHD, []byte("#!/bin/sh\necho 'Bad configuration option: PerSourcePenalties' >&2\nexit 255\n"), 0o755); err != nil {
			t.Fatalf("write legacy fake sshd: %v", err)
		}
		if !sshdSupportsPerSourcePenalties(modernSSHD, dummyKey) {
			t.Fatal("expected modern fake sshd to report PerSourcePenalties support")
		}
		if sshdSupportsPerSourcePenalties(legacySSHD, dummyKey) {
			t.Fatal("expected legacy fake sshd to report no PerSourcePenalties support")
		}
	})
}

// TestLinuxTargetSkipRequireAndPrefixPolicy verifies ConfiguredLinux skip,
// BRANCHLINE_REQUIRE_LINUX=1 failure, partial configuration rejection, and
// disposable /tmp/branchline-test-* prefix enforcement.
func TestLinuxTargetSkipRequireAndPrefixPolicy(t *testing.T) {
	t.Run("skip-when-unconfigured", func(t *testing.T) {
		t.Setenv(EnvLinuxHost, "")
		t.Setenv(EnvLinuxRepo, "")
		t.Setenv(EnvRequireLinux, "")
		res := runWithStubTB(t, func(tb testing.TB) {
			configuredLinux(tb, false)
		})
		if !res.skipped || res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want skipped=true failed=false", res.skipped, res.failed, res.message)
		}
	})

	t.Run("fail-when-required-and-unconfigured", func(t *testing.T) {
		t.Setenv(EnvLinuxHost, "")
		t.Setenv(EnvLinuxRepo, "")
		t.Setenv(EnvRequireLinux, "1")
		res := runWithStubTB(t, func(tb testing.TB) {
			configuredLinux(tb, false)
		})
		if res.skipped || !res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want skipped=false failed=true", res.skipped, res.failed, res.message)
		}
	})

	t.Run("fail-on-partial-config", func(t *testing.T) {
		t.Setenv(EnvLinuxHost, "bl-sshlab-e1")
		t.Setenv(EnvLinuxRepo, "")
		t.Setenv(EnvRequireLinux, "")
		res := runWithStubTB(t, func(tb testing.TB) {
			configuredLinux(tb, false)
		})
		if res.skipped || !res.failed {
			t.Fatalf("got skipped=%v failed=%v msg=%q, want failure on partial config", res.skipped, res.failed, res.message)
		}
	})

	t.Run("reject-non-disposable-prefix", func(t *testing.T) {
		for _, bad := range []string{"/tmp/other", "/tmp/branchline-test-", "/tmp/branchline-test-e1/../escape", "/home/user/repo"} {
			t.Setenv(EnvLinuxHost, "bl-sshlab-e1")
			t.Setenv(EnvLinuxRepo, bad)
			t.Setenv(EnvRequireLinux, "")
			res := runWithStubTB(t, func(tb testing.TB) {
				configuredLinux(tb, false)
			})
			if res.skipped || !res.failed {
				t.Fatalf("bad repo %q: got skipped=%v failed=%v msg=%q, want failure", bad, res.skipped, res.failed, res.message)
			}
		}
	})

	t.Run("accept-valid-disposable-prefix", func(t *testing.T) {
		t.Setenv(EnvLinuxHost, "bl-sshlab-e1")
		t.Setenv(EnvLinuxRepo, "/tmp/branchline-test-e1")
		t.Setenv(EnvRequireLinux, "1")
		srv := configuredLinux(t, false)
		if srv.Kind != KindConfiguredLinux || srv.Host != "bl-sshlab-e1" || srv.RootDir != "/tmp/branchline-test-e1" {
			t.Fatalf("unexpected configured Linux target: %+v", srv)
		}
		if got, want := srv.Address("/tmp/branchline-test-e1/demo"), "bl-sshlab-e1:/tmp/branchline-test-e1/demo"; got != want {
			t.Fatalf("Address = %q, want %q", got, want)
		}
	})
}

type stubOutcome struct {
	skipped bool
	failed  bool
	message string
}

type stubTB struct {
	testing.TB
	outcome stubOutcome
}

func (s *stubTB) Helper() {}
func (s *stubTB) Skip(args ...any) {
	s.outcome.skipped = true
	s.outcome.message = fmt.Sprint(args...)
	runtime.Goexit()
}
func (s *stubTB) Skipf(format string, args ...any) {
	s.outcome.skipped = true
	s.outcome.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}
func (s *stubTB) Fatal(args ...any) {
	s.outcome.failed = true
	s.outcome.message = fmt.Sprint(args...)
	runtime.Goexit()
}
func (s *stubTB) Fatalf(format string, args ...any) {
	s.outcome.failed = true
	s.outcome.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func runWithStubTB(t *testing.T, fn func(testing.TB)) stubOutcome {
	t.Helper()
	st := &stubTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(st)
	}()
	<-done
	return st.outcome
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
