package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// fakeClock is the refresh clock tests drive by hand: ticks and completions
// read it through the state, and no test ever waits on wall-clock time.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) time() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeWatcher is the test file watch: Next blocks until the test sends a
// hint batch or the watch's context is canceled, exactly as a real watch
// ends when its directory goes away.
type fakeWatcher struct {
	mu      sync.Mutex
	cond    *sync.Cond
	batches [][]mygo.FileEvent
	closed  chan struct{}
	once    sync.Once
}

func newFakeWatcher() *fakeWatcher {
	w := &fakeWatcher{closed: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *fakeWatcher) send(events ...mygo.FileEvent) {
	w.mu.Lock()
	w.batches = append(w.batches, events)
	w.cond.Broadcast()
	w.mu.Unlock()
}

func (w *fakeWatcher) hint() {
	w.send(mygo.FileEvent{Op: mygo.FileHint, Path: "."})
}

func (w *fakeWatcher) Next(ctx context.Context) ([]mygo.FileEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	defer stop()
	for len(w.batches) == 0 && ctx.Err() == nil {
		w.cond.Wait()
	}
	if len(w.batches) == 0 {
		return nil, ctx.Err()
	}
	batch := w.batches[0]
	w.batches = w.batches[1:]
	return batch, nil
}

func (w *fakeWatcher) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// waitClosed blocks until the watcher's Close runs, or fails the test. It is
// the synchronization point for the watch loop's exit, which the tests never
// approximate with sleeps.
func waitClosed(t *testing.T, w *fakeWatcher) {
	t.Helper()
	select {
	case <-w.closed:
	case <-time.After(queueTimeout):
		t.Fatal("timed out waiting for the file watch to close")
	}
}

// fakeWatchFactory records the watches a test app's refresh created.
type fakeWatchFactory struct {
	mu      sync.Mutex
	watches []*fakeWatcher
	paths   []string
}

func (f *fakeWatchFactory) make(_ context.Context, path string) (autoRefreshWatcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := newFakeWatcher()
	f.watches = append(f.watches, w)
	f.paths = append(f.paths, path)
	return w, nil
}

func (f *fakeWatchFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.watches)
}

func (f *fakeWatchFactory) watch(t *testing.T, i int) *fakeWatcher {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.watches) {
		t.Fatalf("watch index %d missing, only %d created", i, len(f.watches))
	}
	return f.watches[i]
}

func (f *fakeWatchFactory) path(t *testing.T, i int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.paths) {
		t.Fatalf("watch path index %d missing, only %d created", i, len(f.paths))
	}
	return f.paths[i]
}

// autoRefreshRig bundles the substitutions one test app runs the refresh
// with: the hand-driven clock and the fake watch factory.
type autoRefreshRig struct {
	clock   *fakeClock
	factory *fakeWatchFactory
	state   *autoRefreshState
}

// installAutoRefresh replaces the app's refresh clock and watch factory and
// registers the teardown that ends the test's watches. It runs first in the
// cleanup order, so the watch loops have exited before the queue teardown
// waits for workers.
func installAutoRefresh(t *testing.T, h *testHarness) *autoRefreshRig {
	t.Helper()
	rig := &autoRefreshRig{
		clock:   &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
		factory: &fakeWatchFactory{},
	}
	st := autoRefresh(h.app)
	st.clock = rig.clock.time
	st.watchFactory = rig.factory.make
	st.foreground = true
	rig.state = st
	t.Cleanup(func() {
		closeAutoRefresh(h.app)
	})
	return rig
}

// tickAutoRefresh runs one refresh tick on the test goroutine, as the
// production ticker posts it to the UI thread, and drains the check and
// follow-up work it started.
func tickAutoRefresh(t *testing.T, h *testHarness) {
	t.Helper()
	autoRefreshTick(h.app)
	h.drain()
}

// awaitHintPost consumes the UI callback the watch loop posts for a hint.
// Unlike RunNext it does not expect a tracked worker: the watch loop is a
// plain goroutine whose post is the only signal the test waits for.
func awaitHintPost(t *testing.T, h *testHarness) {
	t.Helper()
	q := h.queue
	var timedOut bool
	timer := time.AfterFunc(queueTimeout, func() {
		q.mu.Lock()
		timedOut = true
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer timer.Stop()

	q.mu.Lock()
	for len(q.pending) == 0 && !timedOut {
		q.cond.Wait()
	}
	if len(q.pending) == 0 {
		q.mu.Unlock()
		t.Fatal("timed out waiting for the watch hint to reach the UI thread")
	}
	fn := q.pending[0]
	q.pending = q.pending[1:]
	q.mu.Unlock()

	fn()
}

// openAutoRefreshRepo opens a local repository in a rigged harness and runs
// the first tick, which the zero last-success time makes due. After it the
// last success stamps the rig clock's start, so every later deadline is
// measured from a controlled baseline.
func openAutoRefreshRepo(t *testing.T, h *testHarness, rig *autoRefreshRig, meta store.Repo) {
	t.Helper()
	h.openRepo(meta)
	h.drain()
	tickAutoRefresh(t, h)
	if !rig.state.lastSuccess.Equal(rig.clock.now) {
		t.Fatalf("baseline check did not stamp the clock start: %v, want %v", rig.state.lastSuccess, rig.clock.now)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
}

// installCountingExecutor wraps the open repository's executor so every git
// invocation counts. The counter is unsynchronized, so it proves a window
// ran no git at all — and, in a window whose only correct traffic is one
// check's sequential poll reads, that a check ran. Windows that can
// legitimately fan out a full snapshot load must not be measured with it:
// proof there comes from the refresh state's own stamps.
func installCountingExecutor(t *testing.T, h *testHarness) *countingExecutor {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("no open repository to wrap")
	}
	counting := &countingExecutor{real: repo.Exec}
	repo.Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		repo.Exec = counting.real
	})
	return counting
}

// uninstallCountingExecutor takes the wrapper off the repository again
// before a phase whose fanning-out loads would race its counter.
func uninstallCountingExecutor(h *testHarness, counting *countingExecutor) {
	repo := h.app.session.Repo()
	if repo != nil && repo.Exec == counting {
		repo.Exec = counting.real
	}
}

// buildAutoRefreshRepo is the default repository construction test
// repository factories delegate to, mirroring what a nil openRepoFn does.
func buildAutoRefreshRepo(a *app, meta store.Repo) *git.Repo {
	if a.store != nil {
		prefs := a.store.Settings()
		return git.New(meta.ExecutorWith(prefs.GitPath, prefs.Trace(), prefs.EnvPath))
	}
	return defaultOpenGitRepo(meta)
}

// checkStamped asserts the last successful check completed exactly at the
// rig clock's current time — which every test advances before a tick it
// expects to check, so a stamp is proof a check ran and finished.
func checkStamped(t *testing.T, rig *autoRefreshRig, what string) {
	t.Helper()
	if !rig.state.lastSuccess.Equal(rig.clock.now) {
		t.Fatalf("%s: last successful check = %v, want the clock's %v — no check ran", what, rig.state.lastSuccess, rig.clock.now)
	}
}

// A check is due only once the interval has passed since the last check
// finished: exactly 60 seconds after completion is not due, 60 seconds plus
// a millisecond is, the completion of a slow check — proven with a gated
// worker — is what the interval counts from, and ticks inside the interval
// run no git command at all.
func TestAutoRefreshIntervalBoundaryFromCompletion(t *testing.T) {
	repo := newGitRepo(t, "ar-interval")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	counting := installCountingExecutor(t, h)
	counting.reads = 0

	// Exactly 60 seconds after the completion: not due.
	rig.clock.advance(60 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("tick exactly 60s after completion ran %d git commands, want 0", counting.reads)
	}
	if rig.state.lastSuccess.Equal(rig.clock.now) {
		t.Fatal("a check ran exactly 60s after completion — the boundary must be strict")
	}

	// One millisecond past it: due.
	rig.clock.advance(time.Millisecond)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "tick 60s+1ms after completion")

	// A slow check stamps its completion, not its start: hold the check's
	// worker behind the gate, advance the clock while it is still running,
	// and the tick after its completion inside completion+60s must not
	// check — even though start+60s has long passed.
	h.queue.PauseWorkers()
	rig.clock.advance(60*time.Second + time.Millisecond)
	autoRefreshTick(h.app)
	poll := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	rig.clock.advance(30 * time.Second)
	h.queue.ReleaseWorker(poll)
	h.drain()
	checkStamped(t, rig, "gated check completion")
	counting.reads = 0

	// 90 seconds after the check started, but only 29 since it completed:
	// still not due — the interval never counts the check's own duration.
	rig.clock.advance(29 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("tick inside completion+60s ran %d git commands; the interval counted from the start, not the completion", counting.reads)
	}

	// completion+60s+1ms: due again.
	rig.clock.advance(31*time.Second + time.Millisecond)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "tick at completion+60s+1ms")
}

// A hint of the local working-tree watch checks at the next tick without
// waiting for the interval, and the file the editor wrote appears in Local
// Changes; without a hint and inside the interval a tick checks nothing.
func TestAutoRefreshLocalFileHintChecks(t *testing.T) {
	repo := newGitRepo(t, "ar-hint")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	counting := installCountingExecutor(t, h)

	if got := rig.factory.count(); got != 1 {
		t.Fatalf("watches created = %d, want 1 for the open local repository", got)
	}
	watch := rig.factory.watch(t, 0)
	if got := rig.factory.path(t, 0); got != repo.Path() {
		t.Fatalf("watch path = %q, want the working tree root %q", got, repo.Path())
	}

	// Inside the interval and without a hint a tick runs nothing.
	counting.reads = 0
	rig.clock.advance(time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("tick without a hint inside the interval ran %d git commands, want 0", counting.reads)
	}
	// The hint check reloads the snapshot, whose fan-out the unsynchronized
	// counter cannot survive; what the check did is proven by the file list.
	uninstallCountingExecutor(h, counting)

	// An editor writes a new file; the watch reports its hint. The next
	// tick checks at once, far inside the interval, and the file shows.
	repo.WriteFile("edited.txt", "written in an editor\n")
	watch.hint()
	awaitHintPost(t, h) // the hint reaches the UI thread and marks the tree dirty
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the tick after a file hint")
	if !h.ui.HasText("edited.txt") {
		t.Fatalf("edited.txt missing in Local Changes after the hint check; texts=%q", h.ui.Texts())
	}
}

// A commit made in a terminal changes only .git, which no hint reports: the
// new commit appears in History once the interval passes, and at once when
// the application becomes active.
func TestAutoRefreshTerminalCommitByIntervalAndActivation(t *testing.T) {
	repo := newGitRepo(t, "ar-terminal")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	openHistoryPageAfterLoad(t, h)
	counting := installCountingExecutor(t, h)

	// The terminal commit produces no hint; inside the interval nothing runs.
	repo.CommitFile("b.txt", "two\n", "terminal commit")
	counting.reads = 0
	rig.clock.advance(time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("tick inside the interval ran %d git commands, want 0", counting.reads)
	}
	if h.ui.HasText("terminal commit") {
		t.Fatal("terminal commit showed before any check ran")
	}
	uninstallCountingExecutor(h, counting)

	// Once the interval passes the tick picks it up.
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the tick past the interval")
	if !h.ui.HasText("terminal commit") {
		t.Fatalf("terminal commit missing in History after the interval check; texts=%q", h.ui.Texts())
	}

	// A second terminal commit shows immediately when the app becomes active,
	// long before the new interval ends.
	repo.CommitFile("c.txt", "three\n", "active commit")
	rig.clock.advance(5 * time.Second)
	autoRefreshForeground(h.app, true)
	h.drain()
	checkStamped(t, rig, "becoming active")
	if !h.ui.HasText("active commit") {
		t.Fatalf("active commit missing in History after activation; texts=%q", h.ui.Texts())
	}
}

// openHistoryPageAfterLoad navigates to the History page of the already
// loaded repository.
func openHistoryPageAfterLoad(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History nav row: %v", err)
	}
	h.ui.Frame()
}

// With automatic refresh off neither hints nor ticks check, but becoming
// active still does; while the app is in the background ticks never check.
func TestAutoRefreshDisabledAndBackground(t *testing.T) {
	repo := newGitRepo(t, "ar-disabled")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	counting := installCountingExecutor(t, h)

	prefs := h.store.Settings()
	prefs.DisableStatusUpdate = true
	if err := h.store.SetSettings(prefs); err != nil {
		t.Fatalf("disable automatic refresh: %v", err)
	}

	// Neither a hint nor a tick past the interval checks while refresh is
	// off. The hint arrives before the first tick retires the watch, so it
	// lands exactly as a real late hint would: buffered, never acted on.
	counting.reads = 0
	rig.factory.watch(t, 0).hint()
	awaitHintPost(t, h)
	tickAutoRefresh(t, h)
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("ticks with refresh off ran %d git commands, want 0", counting.reads)
	}

	// Becoming active checks even with automatic refresh off.
	autoRefreshForeground(h.app, true)
	h.drain()
	checkStamped(t, rig, "becoming active with refresh off")

	// In the background ticks stay silent even past the interval with
	// refresh on again — a hint delivered in the background included — and
	// the next activation checks. Nothing in the repository ever changes,
	// so correct code never fans a load out through the counter.
	prefs.DisableStatusUpdate = false
	if err := h.store.SetSettings(prefs); err != nil {
		t.Fatalf("re-enable automatic refresh: %v", err)
	}
	autoRefreshForeground(h.app, false)
	tickAutoRefresh(t, h) // recreates the watch, still in the background
	counting.reads = 0
	rig.factory.watch(t, 1).hint()
	awaitHintPost(t, h)
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("tick in the background ran %d git commands, want 0", counting.reads)
	}
	autoRefreshForeground(h.app, true)
	h.drain()
	checkStamped(t, rig, "becoming active after the background")
}

// While a write operation runs, neither ticks nor hints start a check; when
// the write ends a single check runs for the hint the write hid, and the
// following ticks inside the interval stay silent — the post-write reload
// and the check never chase each other in a loop.
func TestAutoRefreshBusyWriteGatesChecks(t *testing.T) {
	repo := newGitRepo(t, "ar-busy")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\nmodified\n")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	// Start a stage write with the workers paused so it stays in flight.
	h.queue.PauseWorkers()
	mustClick(t, h, "Stage a.txt")
	h.ui.Frame()
	writeWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	if !h.app.session.Busy() {
		t.Fatal("session is not busy with the write worker held")
	}

	// Ticks and hints while busy start no check; the checking flag is the
	// proof, read on the test goroutine the ticks themselves ran on. The
	// ticks cannot drain: the write worker is what the drain would wait for.
	autoRefreshTick(h.app)
	rig.factory.watch(t, 0).hint()
	awaitHintPost(t, h)
	autoRefreshTick(h.app)
	if rig.state.checking {
		t.Fatal("a check started while a write was in flight")
	}

	// The write finishes; its automatic reload applies. The next tick runs
	// exactly one check for the hint — whose poll is the only git traffic of
	// the window, so the counting executor measures it safely — finding what
	// the reload already loaded.
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	counting := installCountingExecutor(t, h)
	counting.reads = 0
	rig.clock.advance(30 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the tick after the write")
	if counting.reads == 0 {
		t.Fatal("the tick after the write ran no git command, want the one check for the hint")
	}

	// Inside the interval the following ticks run nothing: no refresh loop.
	counting.reads = 0
	tickAutoRefresh(t, h)
	rig.clock.advance(2 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("ticks inside the interval after the post-write check ran %d git commands, want 0", counting.reads)
	}
	if rig.state.lastSuccess.Equal(rig.clock.now) {
		t.Fatal("a check re-stamped inside the interval — the refresh is looping")
	}
}

// A check that reads exactly what the snapshot already holds does not
// reload: the session's load generation never advances. A check that finds
// a real difference reloads exactly once.
func TestAutoRefreshUnchangedSkipsReload(t *testing.T) {
	repo := newGitRepo(t, "ar-unchanged")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	// An unchanged check leaves the load generation untouched: no reload.
	genBefore := h.app.session.loadGen
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the unchanged check")
	if got := h.app.session.loadGen - genBefore; got != 0 {
		t.Fatalf("an unchanged check advanced the load generation by %d, want 0 — it must not reload", got)
	}

	// A real difference — a new terminal commit — reloads exactly once.
	repo.CommitFile("b.txt", "two\n", "terminal commit")
	genBefore = h.app.session.loadGen
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the changed check")
	if got := h.app.session.loadGen - genBefore; got != 1 {
		t.Fatalf("a changed check advanced the load generation by %d, want exactly the reload's 1", got)
	}
}

// A hint re-reads the diff of a modified file whose status letter stays M:
// content the editor changed again appears. When the content did not
// change, the re-read applies nothing — the same diff state object and its
// selection survive.
func TestAutoRefreshDiffRefreshKeepsUnchangedState(t *testing.T) {
	repo := newGitRepo(t, "ar-diff")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.WriteFile("a.txt", "one\nfirst edit\n")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "first edit") {
		t.Fatalf("initial diff missing the first edit; insertions=%q", ins)
	}
	firstState := h.app.changesView.diff.state
	if firstState == nil {
		t.Fatal("no diff state after selecting the modified file")
	}
	firstPatch := h.app.changesView.diff.patch

	// The editor writes more into the file; the status letter stays M, so
	// nothing but the hint-triggered diff re-read can show it.
	repo.WriteFile("a.txt", "one\nfirst edit\nsecond edit\n")
	rig.factory.watch(t, 0).hint()
	awaitHintPost(t, h)
	tickAutoRefresh(t, h)
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "second edit") {
		t.Fatalf("diff did not pick up the second edit after the hint; insertions=%q", ins)
	}
	if h.app.changesView.diff.state == firstState {
		t.Fatal("a changed diff kept the same state object, want a fresh one")
	}
	if reflect.DeepEqual(h.app.changesView.diff.patch, firstPatch) {
		t.Fatal("a changed diff kept the same patch content")
	}

	// Another hint without an edit: the re-read finds the same content and
	// keeps everything, object identity included.
	keptState := h.app.changesView.diff.state
	rig.factory.watch(t, 0).hint()
	awaitHintPost(t, h)
	tickAutoRefresh(t, h)
	if h.app.changesView.diff.state != keptState {
		t.Fatal("an unchanged diff re-read replaced the state object; selection and scroll would be lost")
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "second edit") {
		t.Fatalf("diff lost its content after the unchanged re-read; insertions=%q", ins)
	}
}

// failStatusExecutor wraps an executor and fails git status while armed,
// the first read of every poll, so the whole check fails.
type failStatusExecutor struct {
	real  git.Executor
	armed atomic.Bool
}

func (e *failStatusExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "status" {
			if e.armed.Load() {
				return nil, errors.New("injected status failure")
			}
			break
		}
	}
	return e.real.Run(ctx, stdin, args...)
}

func (e *failStatusExecutor) Describe() string { return e.real.Describe() }

// A failed check is silent: nothing shows, an existing operation error is
// untouched, and neither the interval nor a hint retries within a minute —
// while becoming active still checks.
func TestAutoRefreshFailureBackoffSilent(t *testing.T) {
	repo := newGitRepo(t, "ar-failure")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())

	// Seed an operation error through the session's own failing write; the
	// silent check failure must never overwrite it. The write's own reload
	// completes before the executor starts failing.
	wantErr := errors.New("operation error already shown")
	if ok := h.app.session.do("Failing write", func(*git.Repo) error {
		return wantErr
	}); !ok {
		t.Fatal("the seeded write was rejected")
	}
	h.drain()
	if err := h.app.session.ActionErr(); !errors.Is(err, wantErr) {
		t.Fatalf("fixture: ActionErr = %v, want %v", err, wantErr)
	}

	failing := &failStatusExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = failing
	counting := installCountingExecutor(t, h)
	failing.armed.Store(true)

	// The failing check runs — the poll dies in its first read — and backs
	// off. No fan-out ever passes the counter in this test: the repository
	// never changes.
	counting.reads = 0
	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	if counting.reads == 0 {
		t.Fatal("the check that should fail never ran")
	}
	if !rig.state.failedUntil.Equal(rig.clock.now.Add(time.Minute)) {
		t.Fatalf("failure backoff = %v, want one minute past the failure %v", rig.state.failedUntil, rig.clock.now)
	}
	if err := h.app.session.ActionErr(); !errors.Is(err, wantErr) {
		t.Fatalf("a failed check touched the operation error: got %v, want %v", err, wantErr)
	}
	if h.ui.HasText("injected status failure") {
		t.Fatalf("the silent check failure is visible; texts=%q", h.ui.Texts())
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("a failed check became the load error: %v", err)
	}

	// Within a minute neither the interval nor a hint retries.
	failing.armed.Store(false)
	counting.reads = 0
	rig.clock.advance(30 * time.Second)
	tickAutoRefresh(t, h)
	rig.factory.watch(t, 0).hint()
	awaitHintPost(t, h)
	tickAutoRefresh(t, h)
	if counting.reads != 0 {
		t.Fatalf("checks inside the failure backoff ran %d git commands, want 0", counting.reads)
	}

	// Becoming active ignores the backoff and checks at once.
	autoRefreshForeground(h.app, true)
	h.drain()
	checkStamped(t, rig, "becoming active inside the backoff")
}

// Switching repositories closes the old watch and opens one for the new
// local repository, and a check of the old repository still in flight is
// dropped: it neither reloads the new repository nor loses the hint it
// consumed.
func TestAutoRefreshSwitchRepoClosesWatchDropsResult(t *testing.T) {
	repoA := newGitRepo(t, "ar-switch-a")
	repoA.CommitFile("a.txt", "a\n", "commit a")
	repoB := newGitRepo(t, "ar-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit b")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repoA.StoreRepo())
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}
	firstWatch := rig.factory.watch(t, 0)

	// Hold the next check in flight behind the gate after feeding it a hint
	// to consume, then switch repositories before it finishes.
	h.queue.PauseWorkers()
	firstWatch.hint()
	awaitHintPost(t, h)
	rig.clock.advance(61 * time.Second)
	autoRefreshTick(h.app)
	pollA := h.queue.WaitWorker(t)

	h.openRepo(repoB.StoreRepo())
	genAfterOpen := h.app.session.loadGen
	loadWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(pollA)
	h.queue.ReleaseWorker(loadWorker)
	h.drain()

	if rig.state.checking {
		t.Fatal("the dropped check never released the checking flag")
	}
	if !rig.state.dirty {
		t.Fatal("the hint a dropped check consumed was not restored")
	}
	if got := h.app.session.Meta().Path; got != repoB.Path() {
		t.Fatalf("active repository = %q, want %q", got, repoB.Path())
	}
	if got := h.app.session.Snapshot().Status.Head; got != headOf(t, repoB) {
		t.Fatalf("snapshot HEAD = %q, want repo B's %q", got, headOf(t, repoB))
	}
	// The generation increments of Open and its initial load all happened
	// synchronously; anything past them is a reload. Had the dropped check
	// applied, its comparison against repo A's state would have forced one.
	if got := h.app.session.loadGen - genAfterOpen; got != 0 {
		t.Fatalf("the load generation advanced by %d after the switch, want 0 — the dropped check reloaded the new repository", got)
	}

	// The next tick retires the old watch, opens one for repo B, and runs
	// the check the restored hint asks for.
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the hint-driven check after the switch")
	waitClosed(t, firstWatch)
	if got := rig.factory.count(); got != 2 {
		t.Fatalf("watches created = %d, want 2 (one per local repository)", got)
	}
	if got := rig.factory.path(t, 1); got != repoB.Path() {
		t.Fatalf("second watch path = %q, want %q", got, repoB.Path())
	}
	if rig.state.dirty {
		t.Fatal("the restored hint was never consumed by the check after the switch")
	}
}

// The production watch's ignore rule covers .git at the top and inside
// nested trees, but not .gitignore files or .github directories.
func TestAutoRefreshWatchIgnoresOnlyGitDirs(t *testing.T) {
	for _, tc := range []struct {
		path  string
		isDir bool
		want  bool
	}{
		{".git", true, true},
		{"sub/.git", true, true},
		{"sub/deep/.git", true, true},
		{".git", false, false},
		{".gitignore", false, false},
		{".gitignore", true, false},
		{".github", true, false},
		{".github/workflows", true, false},
		{"src", true, false},
		{"src/.gitmodules", false, false},
	} {
		if got := watchIgnoreGitDir(tc.path, tc.isDir); got != tc.want {
			t.Errorf("watchIgnoreGitDir(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

// The General page's refresh controls write the store and take effect at
// once: unchecking stops the open repository's watch, checking starts it
// again, and the interval input saves numbers inside 10–3600 while keeping
// — and reverting — anything outside.
func TestAutoRefreshSettingsPersistAndApply(t *testing.T) {
	repo := newGitRepo(t, "ar-settings")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	if got := rig.factory.count(); got != 1 {
		t.Fatalf("fixture: watches = %d, want 1", got)
	}

	tt := openSettingsTester(t, h)

	// The interval input starts enabled at the effective default, the
	// checkbox checked.
	input, ok := tt.AccessByRole(ui.RoleTextField, "Refresh interval (seconds)")
	if !ok {
		t.Fatalf("no refresh interval input; texts=%q", tt.Texts())
	}
	if input.Disabled {
		t.Fatal("the interval input starts disabled with refresh on")
	}
	if box, ok := tt.AccessByRole(ui.RoleCheckBox, "Refresh repository status automatically"); !ok || !box.Checked {
		t.Fatalf("refresh checkbox = %+v found=%v, want checked", box, ok)
	}

	// A valid interval saves; out-of-range input is rejected and reverts.
	typeInSettingsField(t, tt, "Refresh interval (seconds)", "300")
	if got := h.store.Settings().StatusUpdateSeconds; got != 300 {
		t.Fatalf("interval after typing 300 = %d, want 300", got)
	}
	typeInSettingsField(t, tt, "Refresh interval (seconds)", "5")
	if got := h.store.Settings().StatusUpdateSeconds; got != 300 {
		t.Fatalf("interval after typing 5 = %d, want the unchanged 300", got)
	}
	typeInSettingsField(t, tt, "Refresh interval (seconds)", "3601")
	if got := h.store.Settings().StatusUpdateSeconds; got != 300 {
		t.Fatalf("interval after typing 3601 = %d, want the unchanged 300", got)
	}
	typeInSettingsField(t, tt, "Refresh interval (seconds)", "3600")
	if got := h.store.Settings().StatusUpdateSeconds; got != 3600 {
		t.Fatalf("interval after typing 3600 = %d, want 3600", got)
	}

	// Unchecking writes the store and closes the open repository's watch at
	// once, disabling the interval input with it.
	if err := tt.Click("Refresh repository status automatically"); err != nil {
		t.Fatalf("uncheck refresh: %v", err)
	}
	tt.Frame()
	if !h.store.Settings().DisableStatusUpdate {
		t.Fatal("unchecking the checkbox did not write DisableStatusUpdate")
	}
	waitClosed(t, rig.factory.watch(t, 0))
	if input2, ok := tt.AccessByRole(ui.RoleTextField, "Refresh interval (seconds)"); !ok || !input2.Disabled {
		t.Fatalf("interval input = %+v found=%v with refresh off, want disabled", input2, ok)
	}

	// Checking again restarts the watch for the open repository.
	if err := tt.Click("Refresh repository status automatically"); err != nil {
		t.Fatalf("re-check refresh: %v", err)
	}
	tt.Frame()
	if h.store.Settings().DisableStatusUpdate {
		t.Fatal("re-checking the checkbox did not clear DisableStatusUpdate")
	}
	if got := rig.factory.count(); got != 2 {
		t.Fatalf("watches after re-enabling = %d, want 2", got)
	}

	// The new interval counts from the next tick: 10 seconds is enough now.
	typeInSettingsField(t, tt, "Refresh interval (seconds)", "10")
	if got := h.store.Settings().StatusUpdateSeconds; got != 10 {
		t.Fatalf("interval after typing 10 = %d, want 10", got)
	}
	rig.clock.advance(11 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the tick past the new 10s interval")
}

// Redrawing frames with automatic refresh on and a repository open starts no
// git command: the refresh lives in the tick, never in the paint path.
func TestAutoRefreshFramesCostNothing(t *testing.T) {
	repo := newGitRepo(t, "ar-frames")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, repo.StoreRepo())
	counting := installCountingExecutor(t, h)
	counting.reads = 0

	for i := 0; i < 8; i++ {
		h.ui.Frame()
	}
	if counting.reads != 0 {
		t.Fatalf("painting frames with automatic refresh on ran %d git commands, want 0", counting.reads)
	}
}

// On an SSH repository the interval check picks up a commit made on the
// server and History shows it. The poll never asks for credentials: with an
// AskPass-bearing executor — as a sync operation leaves behind — the check
// is the only ssh traffic allowed BatchMode=yes, because the refresh's copy
// strips the prompting setup while the ordinary reload keeps it. The SSH
// repository never gets a file watch.
func TestAutoRefreshLoopbackSSHInterval(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// Log the BatchMode option of every ssh invocation; the wrapper then
	// delegates to the loopback target's own ssh. ssh receives BatchMode=no
	// exactly when the executor carries an AskPass, so a "yes" line is a
	// promptless invocation.
	logFile := filepath.Join(t.TempDir(), "ssh-batch.log")
	wrapper := "#!/bin/sh\n" +
		"_bm=none\n" +
		"for _arg in \"$@\"; do\n" +
		"  case \"$_arg\" in\n" +
		"    BatchMode=*) _bm=${_arg#BatchMode=} ;;\n" +
		"  esac\n" +
		"done\n" +
		"printf '%s\\n' \"$_bm\" >> " + git.ShellQuote(logFile) + "\n" +
		"exec " + git.ShellQuote(srv.SSHProgram()) + " \"$@\"\n"
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir ssh wrapper bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write ssh wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The repositories this app opens carry an AskPass, as they do once the
	// sync path has lent one to the executor chain; the refresh's copy must
	// strip it, or every ssh of a check would go out capable of asking.
	ask := git.NewAskPass(func(prompt string) (string, bool) { return "", false })
	h := newTestApp(t, repo)
	rig := installAutoRefresh(t, h)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		base := buildAutoRefreshRepo(h.app, meta)
		if ex, ok := base.Exec.(*git.SSHExecutor); ok {
			copy := *ex
			copy.AskPass = ask
			return base.WithExecutor(&copy)
		}
		return base
	}
	t.Cleanup(func() {
		teardownSync(h)
		ask.Close()
	})

	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial SSH load failed: %v", err)
	}
	openHistoryPageAfterLoad(t, h)

	// The baseline tick runs the first check; an SSH repository builds no watch.
	tickAutoRefresh(t, h)
	if got := rig.factory.count(); got != 0 {
		t.Fatalf("watches created for an SSH repository = %d, want 0", got)
	}

	// A commit lands on the server through the shell, invisible to the client.
	srv.WriteFile(t, repo.Path, "server.txt", []byte("server side\n"))
	srv.RunGit(t, repo.Path, "add", "--", "server.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "server side commit")

	// The check below is the only git traffic of the window.
	if err := os.WriteFile(logFile, nil, 0o600); err != nil {
		t.Fatalf("reset ssh batch log: %v", err)
	}

	rig.clock.advance(61 * time.Second)
	tickAutoRefresh(t, h)
	checkStamped(t, rig, "the SSH interval check")
	if !h.ui.HasText("server side commit") {
		t.Fatalf("server commit missing in History after the interval check; texts=%q", h.ui.Texts())
	}

	// The poll that saw the new commit went out promptless: ssh invocations
	// with BatchMode=yes exist. Had the check kept the AskPass, every ssh of
	// the window would read BatchMode=no like the reload's do.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read ssh batch log: %v", err)
	}
	yes := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "yes" {
			yes++
		}
	}
	if yes == 0 {
		t.Fatalf("no promptless ssh invocation in the check window (BatchMode log: %q) — the poll must strip the credential prompts", strings.TrimSpace(string(data)))
	}
}
