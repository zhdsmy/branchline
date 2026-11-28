package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureExecutor answers at once and remembers the context of every Run,
// for measuring the deadline a Repo gives each command.
type captureExecutor struct {
	mu    sync.Mutex
	ctxs  []context.Context
	ready chan struct{}
	once  sync.Once
}

func newCaptureExecutor() *captureExecutor {
	return &captureExecutor{ready: make(chan struct{})}
}

func (e *captureExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.ctxs = append(e.ctxs, ctx)
	e.mu.Unlock()
	e.once.Do(func() { close(e.ready) })
	return nil, nil
}

func (e *captureExecutor) Describe() string { return "capture" }

// timeoutOfFirst measures the time until the deadline of the context of the
// executor's first invocation.
func timeoutOfFirst(t *testing.T, e *captureExecutor) time.Duration {
	t.Helper()
	select {
	case <-e.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never ran a command")
	}
	e.mu.Lock()
	ctx := e.ctxs[0]
	e.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the command's context has no deadline")
	}
	return time.Until(deadline)
}

// File history and blame in a large repository can take minutes, so
// FileLog, Blame and BlameLines give their commands the slow timeout of the
// network commands, while quick reads such as Status keep the short one.
func TestReadCancelFileReadTimeouts(t *testing.T) {
	const slowFloor = 9 * time.Minute
	slowCalls := map[string]func(r *Repo) error{
		"FileLog":    func(r *Repo) error { _, err := r.FileLog("a.txt", 10); return err },
		"Blame":      func(r *Repo) error { _, err := r.Blame("a.txt", "HEAD"); return err },
		"BlameLines": func(r *Repo) error { _, err := r.BlameLines("a.txt", "HEAD"); return err },
	}
	for name, call := range slowCalls {
		t.Run(name, func(t *testing.T) {
			ex := newCaptureExecutor()
			if err := call(New(ex)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := timeoutOfFirst(t, ex); got <= slowFloor || got > slow {
				t.Errorf("%s timeout = %s, want over %s and at most %s", name, got.Round(time.Second), slowFloor, slow)
			}
		})
	}

	t.Run("Status stays quick", func(t *testing.T) {
		ex := newCaptureExecutor()
		// The executor answers empty output, so Status parses an empty
		// porcelain and returns no error.
		if _, err := New(ex).Status(); err != nil {
			t.Fatalf("Status: %v", err)
		}
		got := timeoutOfFirst(t, ex)
		if got > quick || got < quick-10*time.Second {
			t.Errorf("Status timeout = %s, want about %s", got.Round(time.Second), quick)
		}
	})
}

// blockingExecutor runs nothing: every invocation waits for its context to
// end or for the executor's release, so a test can observe exactly when the
// context of a running command is canceled. It is not a *LocalExecutor or
// *SSHExecutor, so WithoutPrompts would return its Repo unchanged, and the
// copy-chain test uses real typed executors instead.
type blockingExecutor struct {
	release chan struct{}

	mu   sync.Mutex
	ctxs []context.Context
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{release: make(chan struct{})}
}

// close releases every invocation still waiting; tests register it in
// cleanup so nothing outlives the test.
func (e *blockingExecutor) close() {
	select {
	case <-e.release:
	default:
		close(e.release)
	}
}

func (e *blockingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.ctxs = append(e.ctxs, ctx)
	e.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return nil, nil
	}
}

func (e *blockingExecutor) Describe() string { return "blocking" }

// ctxOf returns the context of the n-th invocation (0-based) once it has
// started, failing the test after five seconds.
func (e *blockingExecutor) ctxOf(t *testing.T, n int) context.Context {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		if len(e.ctxs) > n {
			ctx := e.ctxs[n]
			e.mu.Unlock()
			return ctx
		}
		seen := len(e.ctxs)
		e.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("invocation %d never started (%d seen)", n, seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitCtxDone passes once ctx is done, failing after the given bound.
func waitCtxDone(t *testing.T, ctx context.Context, what string, bound time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(bound):
		t.Fatalf("%s: the context was not canceled within %s", what, bound)
	}
}

// Canceling the parent of a Repo copy made by WithContext ends the context
// of the copy's command in flight within a second, while the original
// Repo's commands keep running unaffected.
func TestReadCancelWithContextStopsCommands(t *testing.T) {
	ex := newBlockingExecutor()
	t.Cleanup(ex.close)
	orig := New(ex)

	parent, cancel := context.WithCancel(context.Background())
	copyRepo := orig.WithContext(parent)

	copyDone := make(chan error, 1)
	go func() {
		err := copyRepo.Run("probe-copy")
		copyDone <- err
	}()
	copyCtx := ex.ctxOf(t, 0)

	origDone := make(chan error, 1)
	go func() {
		err := orig.Run("probe-original")
		origDone <- err
	}()
	origCtx := ex.ctxOf(t, 1)

	cancel()

	waitCtxDone(t, copyCtx, "copy command", time.Second)
	select {
	case err := <-copyDone:
		if err == nil {
			t.Fatal("the canceled command of the copy returned without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("the canceled command of the copy did not return within a second")
	}

	// The original Repo's command is unaffected: its context is still
	// alive, and once the executor is released it returns successfully.
	if origCtx.Err() != nil {
		t.Fatalf("the original Repo's command was canceled: %v", origCtx.Err())
	}
	ex.close()
	select {
	case err := <-origDone:
		if err != nil {
			t.Fatalf("the original Repo's command failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the original Repo's command did not return after release")
	}
}

// writeFakeGit writes a git program that blocks on probe arguments: an
// invocation holding an argument that starts with probePrefix waits until
// the file releaseDir/<token> exists, where the token is the rest of the
// argument, checking twice a second. Every other invocation delegates to
// the real git. The probe shell keeps the argument in its command line and
// never sleeps longer than half a second at a time, so a canceled command
// dies with its process group and a released one ends on its own.
func writeFakeGit(t *testing.T, probePrefix, releaseDir string) string {
	t.Helper()
	realGit, err := lookUpGit()
	if err != nil {
		t.Fatalf("no real git for the fake to delegate to: %v", err)
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for _a in \"$@\"; do\n" +
		"  case \"$_a\" in\n" +
		"    " + probePrefix + "*)\n" +
		"      _tok=${_a#" + probePrefix + "}\n" +
		"      while [ ! -f " + ShellQuote(releaseDir) + "/\"$_tok\" ]; do sleep 0.5; done\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + ShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return fake
}

func lookUpGit() (string, error) {
	if p, err := exec.LookPath("git"); err == nil {
		return p, nil
	}
	for _, cand := range []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", os.ErrNotExist
}

// releaseProbe drops the release file of one probe token so the fake git
// invocation holding it ends on its own.
func releaseProbe(t *testing.T, releaseDir, token string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(releaseDir, token), nil, 0o644); err != nil {
		t.Fatalf("release probe %q: %v", token, err)
	}
}

// countProbeProcesses counts processes whose command line carries the probe
// argument prefix.
func countProbeProcesses(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "readcancelprobe-") {
			n++
		}
	}
	return n
}

// Copies made by WithCommandLog, WithExecutor and WithoutPrompts keep the
// context WithContext gave the Repo: canceling the parent stops the copies'
// commands in flight, real git processes included, while the original Repo
// is unaffected.
func TestReadCancelCopiesKeepContext(t *testing.T) {
	releaseDir := t.TempDir()
	fake := writeFakeGit(t, "readcancelprobe-", releaseDir)
	dir := t.TempDir()
	newLocal := func() *Repo {
		return New(&LocalExecutor{Dir: dir, Git: fake})
	}

	parent, cancel := context.WithCancel(context.Background())
	orig := newLocal()
	base := orig.WithContext(parent)

	// One derived Repo per copy constructor, each chained in both orders.
	derived := map[string]*Repo{
		"WithCommandLog":          base.WithCommandLog(NewCommandLog()),
		"WithCommandLogBeforeCtx": newLocal().WithCommandLog(NewCommandLog()).WithContext(parent),
		"WithExecutor":            base.WithExecutor(&LocalExecutor{Dir: dir, Git: fake}),
		"WithExecutorBeforeCtx":   newLocal().WithExecutor(&LocalExecutor{Dir: dir, Git: fake}).WithContext(parent),
		"WithoutPrompts":          base.WithoutPrompts(),
		"WithoutPromptsBeforeCtx": newLocal().WithoutPrompts().WithContext(parent),
	}

	type running struct {
		done  chan error
		token string
	}
	runs := map[string]*running{}
	for name, repo := range derived {
		done := make(chan error, 1)
		go func(r *Repo, tok string) {
			err := r.Run("readcancelprobe-" + tok)
			done <- err
		}(repo, name)
		runs[name] = &running{done: done, token: name}
	}
	// The original Repo's command runs under no parent: it must survive the
	// cancellation and end only when released.
	origDone := make(chan error, 1)
	go func() {
		err := orig.Run("readcancelprobe-original")
		origDone <- err
	}()

	// Give the spawns a bounded moment so the cancellation meets real git
	// processes in flight rather than goroutines not yet started.
	deadline := time.Now().Add(10 * time.Second)
	for countProbeProcesses(t) < len(derived)+1 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d probe processes of %d started", countProbeProcesses(t), len(derived)+1)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	for name, run := range runs {
		select {
		case err := <-run.done:
			if err == nil {
				t.Errorf("%s: the canceled command returned without an error", name)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the command did not return within five seconds of the cancellation", name)
		}
	}

	// The original Repo's command is unaffected: still running past the
	// cancellation, then released by hand it ends without an error.
	select {
	case err := <-origDone:
		t.Fatalf("the original Repo's command ended with the cancellation: %v", err)
	default:
	}
	releaseProbe(t, releaseDir, "original")
	select {
	case err := <-origDone:
		if err != nil {
			t.Fatalf("the original Repo's command failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the original Repo's command did not end after release")
	}

	// No probe process of this test may linger: every canceled command died
	// with its process group, and the released original notices its file
	// within half a second of its poll loop.
	deadline = time.Now().Add(5 * time.Second)
	for countProbeProcesses(t) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d probe processes linger after cancellation and release", countProbeProcesses(t))
		}
		time.Sleep(25 * time.Millisecond)
	}
}
