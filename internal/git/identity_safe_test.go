package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// isolatedGlobalConfig points the whole test process at a throwaway home and
// global git configuration file, so no test here ever reads or writes the
// real user configuration, and returns the configuration file path.
func isolatedGlobalConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return cfg
}

// gateExecutor wraps an executor, records every invocation, and can inject a
// failure — or silently drop a call while pretending success — for the
// invocations failOn and dropOn match.
type gateExecutor struct {
	inner  Executor
	failOn func(args []string) bool
	dropOn func(args []string) bool

	mu    sync.Mutex
	calls [][]string
}

func (g *gateExecutor) Describe() string { return g.inner.Describe() }

func (g *gateExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cp := append([]string(nil), args...)
	g.mu.Lock()
	g.calls = append(g.calls, cp)
	g.mu.Unlock()
	if g.dropOn != nil && g.dropOn(cp) {
		return nil, nil
	}
	if g.failOn != nil && g.failOn(cp) {
		return nil, &Error{Args: cp, ExitCode: 1, Stderr: "gateExecutor injected failure"}
	}
	return g.inner.Run(ctx, stdin, args...)
}

func (g *gateExecutor) invocations() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([][]string(nil), g.calls...)
}

// isConfigWrite reports whether args write to the git configuration rather
// than read it.
func isConfigWrite(args []string) bool {
	hasConfig, hasGlobal, isRead := false, false, false
	for _, a := range args {
		switch a {
		case "config":
			hasConfig = true
		case "--global":
			hasGlobal = true
		case "--get", "--list":
			isRead = true
		}
	}
	return hasConfig && hasGlobal && !isRead
}

func writesKey(args []string, key string) bool {
	return isConfigWrite(args) && slices.Contains(args, key)
}

func countConfigWrites(g *gateExecutor) int {
	n := 0
	for _, call := range g.invocations() {
		if isConfigWrite(call) {
			n++
		}
	}
	return n
}

func runGitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{}, args...)
	cmd := exec.Command("git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func readConfigBytes(t *testing.T, cfg string) []byte {
	t.Helper()
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read %s: %v", cfg, err)
	}
	return b
}

// commitInNoUserRepo builds a repository with no local user.name or
// user.email, so a commit's author can only come from the global
// configuration, commits one file and returns the %an/%ae the commit carries.
func commitInNoUserRepo(t *testing.T, message string) (name, email string) {
	t.Helper()
	dir := t.TempDir()
	runGitCLI(t, dir, "init", "-q", "-b", "main")
	for _, key := range []string{"user.name", "user.email"} {
		cmd := exec.Command("git", "config", "--local", "--get", key)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("fixture repository unexpectedly has a local %s: %q", key, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, dir, "add", "--", "f.txt")
	runGitCLI(t, dir, "commit", "-q", "-m", message)
	out := runGitCLI(t, dir, "log", "-1", "--format=%an%x00%ae")
	name, email, ok := strings.Cut(strings.TrimSpace(out), "\x00")
	if !ok {
		t.Fatalf("unexpected author format %q", out)
	}
	return name, email
}

// Writing the global author identity through SafeSetGlobalIdentity lands in
// the real global configuration file, a commit in a repository without its
// own user.name/user.email immediately carries the new author, values are
// trimmed before writing, and clearing a field unsets it for good.
func TestGlobalIdentitySafeWriteCommitAuthorAndClear(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	r := New(&LocalExecutor{Dir: t.TempDir()})

	live, err := r.GlobalIdentity()
	if err != nil || live != (Identity{}) {
		t.Fatalf("initial identity = %+v, %v; want empty", live, err)
	}

	// Surrounding whitespace is trimmed before writing.
	if err := r.SafeSetGlobalIdentity(Identity{Name: "  Grace Hopper  ", Email: "\tgrace@example.com "}, live, nil); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	id, err := r.GlobalIdentity()
	if err != nil || id.Name != "Grace Hopper" || id.Email != "grace@example.com" {
		t.Fatalf("after write = %+v, %v", id, err)
	}

	// A real commit in a repository without a local identity uses it.
	name, email := commitInNoUserRepo(t, "first")
	if name != "Grace Hopper" || email != "grace@example.com" {
		t.Fatalf("commit author = %q <%q>, want Grace Hopper <grace@example.com>", name, email)
	}

	// Changing both keys at once works, and the new commits follow at once.
	if err := r.SafeSetGlobalIdentity(Identity{Name: "Alan Turing", Email: "alan@example.com"}, id, nil); err != nil {
		t.Fatalf("rewrite identity: %v", err)
	}
	name, email = commitInNoUserRepo(t, "second")
	if name != "Alan Turing" || email != "alan@example.com" {
		t.Fatalf("commit author after rewrite = %q <%q>, want Alan Turing <alan@example.com>", name, email)
	}

	// Clearing the email unsets the key while the name survives.
	if err := r.SafeSetGlobalIdentity(Identity{Name: "Alan Turing"}, Identity{Name: "Alan Turing", Email: "alan@example.com"}, nil); err != nil {
		t.Fatalf("clear email: %v", err)
	}
	id, err = r.GlobalIdentity()
	if err != nil || id.Name != "Alan Turing" || id.Email != "" {
		t.Fatalf("after clearing email = %+v, %v", id, err)
	}
	cmd := exec.Command("git", "config", "--global", "--get", "user.email")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("user.email still set after clearing: %q", out)
	}
	content := string(readConfigBytes(t, cfg))
	if !strings.Contains(content, "Alan Turing") {
		t.Fatalf("name missing from config file:\n%s", content)
	}
	if strings.Contains(content, "alan@example.com") {
		t.Fatalf("cleared email still in config file:\n%s", content)
	}
}

// When the identity to write equals the live one, nothing is written at all:
// not even a no-op rewrite, proven with a read-only configuration file that
// any write attempt would fail against.
func TestGlobalIdentitySafeUnchangedSkipsWrite(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	gate := &gateExecutor{inner: &LocalExecutor{Dir: t.TempDir()}}
	r := New(gate)

	live, err := r.GlobalIdentity()
	if err != nil {
		t.Fatalf("initial read: %v", err)
	}
	id := Identity{Name: "Grace Hopper", Email: "grace@example.com"}
	if err := r.SafeSetGlobalIdentity(id, live, nil); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	writesBefore := countConfigWrites(gate)
	if writesBefore == 0 {
		t.Fatal("the initial write never reached git")
	}

	// Read-only file: a write would fail with an error, the skip must not.
	if err := os.Chmod(cfg, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })
	if err := r.SafeSetGlobalIdentity(id, id, nil); err != nil {
		t.Fatalf("unchanged write against a read-only file = %v, want nil (no write attempted)", err)
	}
	if got := countConfigWrites(gate); got != writesBefore {
		t.Fatalf("unchanged write attempted %d config writes, want no new writes beyond %d", got, writesBefore)
	}
}

// A read error carried in from the caller refuses the write before anything
// is validated or attempted: the configuration file keeps its exact bytes.
func TestGlobalIdentitySafeReadErrRefuses(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	r := New(&LocalExecutor{Dir: t.TempDir()})

	if err := r.SafeSetGlobalIdentity(Identity{Name: "Original", Email: "orig@example.com"}, Identity{}, nil); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	before := readConfigBytes(t, cfg)

	readErr := errors.New("simulated read failure")
	err := r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "new@example.com"}, Identity{}, readErr)
	if !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want it to wrap the read error", err)
	}
	if after := readConfigBytes(t, cfg); string(after) != string(before) {
		t.Fatalf("config file changed despite the read error:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Names and emails with control characters git cannot store are rejected
// before any write, with the configuration file untouched.
func TestGlobalIdentitySafeRejectsControlCharacters(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	r := New(&LocalExecutor{Dir: t.TempDir()})

	if err := r.SafeSetGlobalIdentity(Identity{Name: "Original", Email: "orig@example.com"}, Identity{}, nil); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	before := readConfigBytes(t, cfg)
	live, err := r.GlobalIdentity()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for _, id := range []Identity{
		{Name: "line\nbreak", Email: "ok@example.com"},
		{Name: "ok", Email: "carriage\rreturn@example.com"},
		{Name: "null\x00byte", Email: "ok@example.com"},
	} {
		if err := r.SafeSetGlobalIdentity(id, live, nil); !errors.Is(err, ErrGlobalIdentityInvalid) {
			t.Fatalf("write %+v err = %v, want ErrGlobalIdentityInvalid", id, err)
		}
	}
	if after := readConfigBytes(t, cfg); string(after) != string(before) {
		t.Fatalf("config file changed despite invalid input:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A syntactically broken global configuration file makes git config fail
// with a fatal error; the write is refused and the broken file is left
// exactly as it is for the user to repair.
func TestGlobalIdentitySafeBrokenConfigRefuses(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	broken := []byte("[user\n name = broken\n")
	if err := os.WriteFile(cfg, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: t.TempDir()})

	if _, err := r.GlobalIdentity(); err == nil {
		t.Fatal("reading a broken configuration unexpectedly succeeded")
	}
	err := r.SafeSetGlobalIdentity(Identity{Name: "New", Email: "new@example.com"}, Identity{}, nil)
	if err == nil {
		t.Fatal("write over a broken configuration unexpectedly succeeded")
	}
	if errors.Is(err, ErrGlobalIdentityChanged) {
		t.Fatalf("broken read misreported as external change: %v", err)
	}
	if after := readConfigBytes(t, cfg); string(after) != string(broken) {
		t.Fatalf("broken config file overwritten:\nbefore:\n%s\nafter:\n%s", broken, after)
	}
}

// A configuration file that cannot be read refuses the write even though git
// answers the read with the same exit code as a key that was never set: the
// warning on standard error betrays the unreadable file, and overwriting it —
// losing the identity the user cannot currently read — is refused. This is
// the dangerous case an exit-code-only read would miss.
func TestGlobalIdentitySafeUnreadableConfigRefuses(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	original := []byte("[user]\n\tname = Original\n\temail = orig@example.com\n")
	if err := os.WriteFile(cfg, original, 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: t.TempDir()})

	// The dangerous shape: git answers the key as absent (exit code 1), which
	// the lenient GlobalIdentity reads as an empty identity without error.
	if err := os.Chmod(cfg, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o644) })
	if id, err := r.GlobalIdentity(); err != nil || id != (Identity{}) {
		t.Logf("note: lenient read of the unreadable file = %+v, %v", id, err)
	}

	err := r.SafeSetGlobalIdentity(Identity{Name: "New", Email: "new@example.com"}, Identity{}, nil)
	if err == nil {
		t.Fatal("write over an unreadable configuration unexpectedly succeeded")
	}
	if errors.Is(err, ErrGlobalIdentityChanged) {
		t.Fatalf("unreadable file misreported as an external change: %v", err)
	}
	// Restore readability to compare bytes: nothing may have been written.
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if after := readConfigBytes(t, cfg); string(after) != string(original) {
		t.Fatalf("unreadable config file changed:\nbefore:\n%s\nafter:\n%s", original, after)
	}
}

// When the live identity no longer matches what the caller read, the write
// is refused with ErrGlobalIdentityChanged and the external edit survives.
func TestGlobalIdentitySafeExternalChangeRefuses(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	r := New(&LocalExecutor{Dir: t.TempDir()})

	initial := Identity{Name: "Original", Email: "orig@example.com"}
	if err := r.SafeSetGlobalIdentity(initial, Identity{}, nil); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	before := readConfigBytes(t, cfg)

	// Someone else edits the configuration after the caller read it.
	runGitCLI(t, "", "config", "--global", "user.name", "Mallory")
	tampered := readConfigBytes(t, cfg)
	if string(tampered) == string(before) {
		t.Fatal("the simulated external edit never landed in the configuration file")
	}

	err := r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "orig@example.com"}, initial, nil)
	if !errors.Is(err, ErrGlobalIdentityChanged) {
		t.Fatalf("err = %v, want ErrGlobalIdentityChanged", err)
	}
	if after := readConfigBytes(t, cfg); string(after) != string(tampered) {
		t.Fatalf("external edit overwritten:\nwant:\n%s\ngot:\n%s", tampered, after)
	}
	id, idErr := r.GlobalIdentity()
	if idErr != nil || id.Name != "Mallory" || id.Email != "orig@example.com" {
		t.Fatalf("identity after refused write = %+v, %v; want the external edit", id, idErr)
	}
}

// When the configuration cannot be written — its directory is read-only —
// the write fails with an error and the original content survives byte for
// byte.
func TestGlobalIdentitySafeWriteFailureKeepsOriginal(t *testing.T) {
	cfg := isolatedGlobalConfig(t)
	r := New(&LocalExecutor{Dir: t.TempDir()})

	initial := Identity{Name: "Original", Email: "orig@example.com"}
	if err := r.SafeSetGlobalIdentity(initial, Identity{}, nil); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	before := readConfigBytes(t, cfg)
	live, err := r.GlobalIdentity()
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	dir := filepath.Dir(cfg)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err = r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "new@example.com"}, live, nil)
	if err == nil {
		t.Fatal("write into a read-only directory unexpectedly succeeded")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if after := readConfigBytes(t, cfg); string(after) != string(before) {
		t.Fatalf("config file changed despite the write failure:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	id, idErr := r.GlobalIdentity()
	if idErr != nil || id != live {
		t.Fatalf("identity after failed write = %+v, %v; want %+v", id, idErr, live)
	}
}

// When the first of two key writes succeeds and the second fails, the first
// is rolled back: a changed name returns to its original value, a newly added
// name is unset again, and the rollback tolerates the key having vanished in
// between. Either way the file keeps its exact original bytes.
func TestGlobalIdentitySafeRollbackOnPartialFailure(t *testing.T) {
	newIsolated := func(t *testing.T, seed Identity) (string, *gateExecutor, *Repo) {
		t.Helper()
		cfg := isolatedGlobalConfig(t)
		gate := &gateExecutor{inner: &LocalExecutor{Dir: t.TempDir()}}
		r := New(gate)
		if err := r.SafeSetGlobalIdentity(seed, Identity{}, nil); err != nil {
			t.Fatalf("seed identity %+v: %v", seed, err)
		}
		return cfg, gate, r
	}

	// A rewritten user.name is restored to its original value when the
	// user.email write fails.
	t.Run("restores the original name", func(t *testing.T) {
		cfg, gate, r := newIsolated(t, Identity{Name: "Original", Email: "orig@example.com"})
		before := readConfigBytes(t, cfg)
		live, err := r.GlobalIdentity()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		gate.failOn = func(args []string) bool { return writesKey(args, "user.email") }

		err = r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "new@example.com"}, live, nil)
		if err == nil || !strings.Contains(err.Error(), "injected failure") {
			t.Fatalf("err = %v, want the injected user.email write failure", err)
		}
		if after := readConfigBytes(t, cfg); string(after) != string(before) {
			t.Fatalf("config file left half-written:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		id, idErr := r.GlobalIdentity()
		if idErr != nil || id != live {
			t.Fatalf("identity after rollback = %+v, %v; want original %+v", id, idErr, live)
		}
	})

	// A user.name that did not exist before is unset again when the
	// user.email write fails.
	t.Run("unsets a newly added name", func(t *testing.T) {
		cfg, gate, r := newIsolated(t, Identity{Email: "orig@example.com"})
		before := readConfigBytes(t, cfg)
		live, err := r.GlobalIdentity()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if live.Name != "" {
			t.Fatalf("fixture live identity has a name: %+v", live)
		}
		gate.failOn = func(args []string) bool { return writesKey(args, "user.email") }

		err = r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "new@example.com"}, live, nil)
		if err == nil || !strings.Contains(err.Error(), "injected failure") {
			t.Fatalf("err = %v, want the injected user.email write failure", err)
		}
		if after := readConfigBytes(t, cfg); string(after) != string(before) {
			t.Fatalf("config file left half-written:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		id, idErr := r.GlobalIdentity()
		if idErr != nil || id.Name != "" || id.Email != live.Email {
			t.Fatalf("identity after rollback = %+v, %v; want name unset and original email", id, idErr)
		}
	})

	// If the key the rollback unsets is already gone — here because the name
	// write itself was silently dropped — git's exit code 5 is tolerated and
	// the original write error is still the one returned.
	t.Run("rollback tolerates a key already gone", func(t *testing.T) {
		cfg, gate, r := newIsolated(t, Identity{Email: "orig@example.com"})
		before := readConfigBytes(t, cfg)
		live, err := r.GlobalIdentity()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		gate.dropOn = func(args []string) bool {
			// The write of user.name, but not the rollback --unset.
			return writesKey(args, "user.name") && !slices.Contains(args, "--unset")
		}
		gate.failOn = func(args []string) bool { return writesKey(args, "user.email") }

		err = r.SafeSetGlobalIdentity(Identity{Name: "Replacement", Email: "new@example.com"}, live, nil)
		if err == nil || !strings.Contains(err.Error(), "injected failure") {
			t.Fatalf("err = %v, want the injected user.email write failure", err)
		}
		if strings.Contains(err.Error(), "rolling back") {
			t.Fatalf("rollback of an already-absent key reported as failed: %v", err)
		}
		if after := readConfigBytes(t, cfg); string(after) != string(before) {
			t.Fatalf("config file changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}
