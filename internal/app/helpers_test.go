package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

const queueTimeout = 10 * time.Second

// testHarness bundles an app instance, a headless ui.Tester, an isolated
// store, and a controlled UI callback queue for deterministic tests.
type testHarness struct {
	t     *testing.T
	app   *app
	store *store.Store
	ui    *ui.Tester
	queue *uiQueue
}

// newTestApp builds an app backed by an isolated temporary store, a headless
// ui.Tester, and a controlled UI queue. Background workers run in real
// goroutines and deliver their UI callbacks into the queue instead of calling
// them directly or spawning ad-hoc goroutines.
func newTestApp(t *testing.T, repos ...store.Repo) *testHarness {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "repositories.json")
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	for _, r := range repos {
		if _, err := s.Add(r); err != nil {
			t.Fatalf("seed test store with %q: %v", r.Name, err)
		}
	}

	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)

	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn

	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(instantView(a.view), 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	return h
}

// instantView wraps view so every frame shows animations at their targets at
// once. The Tester's clock only moves under Advance, so an animation an input
// starts would otherwise never finish: the Tester would keep rendering its
// maximum of twenty frames after every input. The window's first frame is
// exempt inside the toolkit, which is fine since animations start at rest.
func instantView(view func(c *ui.Context)) func(c *ui.Context) {
	return func(c *ui.Context) {
		c.DisableAnimations()
		view(c)
	}
}

// openRepo opens r in the app and renders one frame without draining
// background work yet, allowing tests to observe loading state or interleave
// actions before calling Drain.
func (h *testHarness) openRepo(r store.Repo) store.Repo {
	h.t.Helper()
	saved, err := h.store.Add(r)
	if err != nil {
		h.t.Fatalf("add repo %q to store: %v", r.Name, err)
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	return saved
}

// drain runs all pending worker completions on the test goroutine and renders
// a fresh frame so UI assertions see the updated state.
func (h *testHarness) drain() {
	h.t.Helper()
	h.queue.Drain(h.t)
	h.ui.Frame()
}

// gatedWorker represents a single real background worker held before running
// its body so a test can deterministically control completion order.
type gatedWorker struct {
	once    sync.Once
	release chan struct{}
}

func (w *gatedWorker) unlock() {
	w.once.Do(func() { close(w.release) })
}

// uiQueue is a deterministic FIFO queue for UI-thread completion callbacks
// posted by background workers. It tracks active workers so Drain waits for
// real completion without time.Sleep polling.
type uiQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []func()
	active  int
	paused  bool
	gated   []*gatedWorker
	allGate []*gatedWorker
}

func newUIQueue() *uiQueue {
	q := &uiQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *uiQueue) post(fn func()) {
	q.mu.Lock()
	q.pending = append(q.pending, fn)
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *uiQueue) spawn(fn func()) {
	q.mu.Lock()
	q.active++
	var w *gatedWorker
	if q.paused {
		w = &gatedWorker{release: make(chan struct{})}
		q.gated = append(q.gated, w)
		q.allGate = append(q.allGate, w)
	}
	q.cond.Broadcast()
	q.mu.Unlock()

	go func() {
		if w != nil {
			<-w.release
		}
		defer func() {
			q.mu.Lock()
			q.active--
			q.cond.Broadcast()
			q.mu.Unlock()
		}()
		fn()
	}()
}

// PauseWorkers holds newly spawned workers until the test releases them with
// ReleaseWorker or ResumeWorkers.
func (q *uiQueue) PauseWorkers() {
	q.mu.Lock()
	q.paused = true
	q.mu.Unlock()
}

// ResumeWorkers stops pausing future workers. Previously captured gatedWorkers
// remain held until released individually.
func (q *uiQueue) ResumeWorkers() {
	q.mu.Lock()
	q.paused = false
	q.cond.Broadcast()
	q.mu.Unlock()
}

// WaitWorker waits for the next paused worker to be spawned and returns its
// handle so the test can release it at an exact point.
func (q *uiQueue) WaitWorker(t *testing.T) *gatedWorker {
	t.Helper()
	var timedOut bool
	timer := time.AfterFunc(queueTimeout, func() {
		q.mu.Lock()
		timedOut = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer timer.Stop()

	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.gated) == 0 && !timedOut {
		q.cond.Wait()
	}
	if len(q.gated) == 0 {
		t.Fatal("timed out waiting for a paused background worker")
	}
	w := q.gated[0]
	q.gated = q.gated[1:]
	return w
}

// ReleaseWorker unblocks one paused worker previously obtained from WaitWorker.
func (q *uiQueue) ReleaseWorker(w *gatedWorker) {
	if w != nil {
		w.unlock()
	}
}

// RunNext waits for a single UI callback posted by a worker and executes it on
// the calling (test/UI) goroutine.
func (q *uiQueue) RunNext(t *testing.T) {
	t.Helper()
	var timedOut bool
	timer := time.AfterFunc(queueTimeout, func() {
		q.mu.Lock()
		timedOut = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer timer.Stop()

	q.mu.Lock()
	for len(q.pending) == 0 && q.active > 0 && !timedOut {
		q.cond.Wait()
	}
	if len(q.pending) == 0 {
		didTimeout := timedOut
		q.mu.Unlock()
		if didTimeout {
			t.Fatal("timed out waiting for UI callback")
		}
		t.Fatal("no UI callback pending and no active workers")
	}
	fn := q.pending[0]
	q.pending = q.pending[1:]
	q.mu.Unlock()

	fn()
}

// Drain waits for all active workers to finish posting and executes every
// queued UI callback on the calling (test/UI) goroutine, including follow-up
// work scheduled by those callbacks (such as a reload triggered by a write).
func (q *uiQueue) Drain(t *testing.T) int {
	t.Helper()
	var timedOut bool
	timer := time.AfterFunc(queueTimeout, func() {
		q.mu.Lock()
		timedOut = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer timer.Stop()

	ran := 0
	for {
		q.mu.Lock()
		for len(q.pending) == 0 && q.active > 0 && !timedOut {
			q.cond.Wait()
		}
		if len(q.pending) == 0 {
			active := q.active
			didTimeout := timedOut
			q.mu.Unlock()
			if didTimeout && active > 0 {
				t.Fatalf("timed out draining UI queue with %d active worker(s)", active)
			}
			return ran
		}
		fn := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()

		fn()
		ran++
	}
}

func (q *uiQueue) releaseAllAndWait() {
	q.mu.Lock()
	q.paused = false
	for _, w := range q.allGate {
		w.unlock()
	}
	q.gated = nil
	q.allGate = nil
	q.cond.Broadcast()
	for q.active > 0 {
		q.cond.Wait()
	}
	q.pending = nil
	q.mu.Unlock()
}

// gitRepoFixture manages a real temporary Git repository initialized with
// isolated Git configuration for integration tests.
type gitRepoFixture struct {
	t    *testing.T
	name string
	dir  string
}

func newGitRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo %s: %v", name, err)
	}
	f := &gitRepoFixture{t: t, name: name, dir: dir}
	f.Git("init", "-b", "main")
	f.Git("config", "user.name", "Branchline Test")
	f.Git("config", "user.email", "test@branchline.local")
	return f
}

func (f *gitRepoFixture) Path() string { return f.dir }

func (f *gitRepoFixture) StoreRepo() store.Repo {
	return store.Repo{
		Kind: store.Local,
		Path: f.dir,
		Name: f.name,
	}
}

func (f *gitRepoFixture) WriteFile(relPath, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatalf("mkdir %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatalf("write %s: %v", relPath, err)
	}
}

func (f *gitRepoFixture) CommitFile(relPath, content, message string) string {
	f.t.Helper()
	f.WriteFile(relPath, content)
	f.Git("add", "--", relPath)
	f.Git("commit", "-m", message)
	return strings.TrimSpace(f.Git("rev-parse", "HEAD"))
}

func (f *gitRepoFixture) CreateBranch(name string) {
	f.t.Helper()
	f.Git("branch", name)
}

func (f *gitRepoFixture) Checkout(name string) {
	f.t.Helper()
	f.Git("checkout", name)
}

func (f *gitRepoFixture) CreateTag(name string) {
	f.t.Helper()
	f.Git("tag", name)
}

// AddBareRemote creates a real bare Git repository in a temp directory, wires
// it as remoteName, and pushes HEAD when the repository already has commits.
func (f *gitRepoFixture) AddBareRemote(remoteName string) string {
	f.t.Helper()
	bareDir := filepath.Join(f.t.TempDir(), remoteName+".git")
	runGitCommand(f.t, "", "init", "--bare", bareDir)
	f.Git("remote", "add", remoteName, bareDir)
	if _, err := tryGitCommand(f.dir, "rev-parse", "--verify", "HEAD"); err == nil {
		f.Git("push", "-u", remoteName, "HEAD")
	}
	return bareDir
}

func (f *gitRepoFixture) Git(args ...string) string {
	f.t.Helper()
	return runGitCommand(f.t, f.dir, args...)
}

func runGitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryGitCommand(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func tryGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
