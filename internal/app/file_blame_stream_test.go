package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// The tests of this file drive the streaming blame through the real UI of a
// real repository. Two test hooks of the inspector state make the stream
// deterministic without any sleep: a fake clock the batching reads, so a
// test decides exactly when a batch window elapses, and a decorator around
// the hunk delivery, so a test holds the stream at chosen hunks, reorders or
// splits them — the incremental format explicitly promises no order — and
// watches every delivery complete.
//
// While a worker is held inside a delivery, h.drain would wait for it
// forever, so every choreography consumes the posts it expects with
// h.queue.RunNext one at a time, in order, and drains only once nothing is
// held back anymore.

// blameFakeClock is the manually advanced clock a test installs as the
// inspector's blameNow. It starts at the zero time; advancing moves it in
// one step. The worker goroutine reads it while the test writes it, so both
// go through the mutex.
type blameFakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *blameFakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *blameFakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// blameTurnstile holds every delivery of a blame stream until the test
// permits it, one token per hunk, and acknowledges every completed delivery
// on acks. Acknowledgments are cumulative: the n-th received acknowledgment
// means n deliveries completed since the stream started, however they
// batched. permit never blocks and open releases everything, so a test that
// fails midway cannot strand the worker goroutine.
type blameTurnstile struct {
	tokens   chan struct{}
	acks     chan struct{}
	openOnce sync.Once
	// consumed counts the acknowledgments the test goroutine received so
	// far, so awaitAcks can wait for a cumulative total. Only the test
	// goroutine reads and writes it.
	consumed int
}

func newBlameTurnstile(t *testing.T) *blameTurnstile {
	t.Helper()
	ts := &blameTurnstile{
		tokens: make(chan struct{}, 512),
		acks:   make(chan struct{}, 512),
	}
	t.Cleanup(ts.open)
	return ts
}

func (ts *blameTurnstile) permit(n int) {
	for i := 0; i < n; i++ {
		ts.tokens <- struct{}{}
	}
}

func (ts *blameTurnstile) open() {
	ts.openOnce.Do(func() { close(ts.tokens) })
}

// awaitAcks waits until n delivery acknowledgments in total have been
// received across the test's calls, failing after the shared queue timeout
// rather than hanging a broken test.
func (ts *blameTurnstile) awaitAcks(t *testing.T, n int) {
	t.Helper()
	deadline := time.NewTimer(queueTimeout)
	defer deadline.Stop()
	for ts.consumed < n {
		select {
		case <-ts.acks:
			ts.consumed++
		case <-deadline.C:
			t.Fatalf("timed out waiting for hunk delivery acknowledgment %d of %d", ts.consumed+1, n)
		}
	}
}

// wrap returns the hunk decorator the inspector's blameWrapHunk hook
// receives: every delivery waits for one token and acknowledges its
// completion.
func (ts *blameTurnstile) wrap(deliver func(git.BlameHunk)) func(git.BlameHunk) {
	return func(h git.BlameHunk) {
		<-ts.tokens
		deliver(h)
		ts.acks <- struct{}{}
	}
}

// queuePendingLen counts the UI callbacks posted and not yet consumed. A
// zero proves no batch was posted, as when hunks accumulate inside a batch
// window; counting consumes nothing, so the choreography can go on.
func queuePendingLen(q *uiQueue) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// installBlameStreamHooks wires the fake clock and the hunk decorator into
// the app's inspector state, where closeFileInspector keeps them across the
// closes of a test.
func installBlameStreamHooks(h *testHarness, clock *blameFakeClock, wrap func(func(git.BlameHunk)) func(git.BlameHunk)) {
	st := h.app.fileInspectorUI()
	st.blameNow = clock.now
	st.blameWrapHunk = wrap
}

// incrementalHunksOf collects the hunks git blame --incremental emits for
// path at rev, in emission order, through the same git layer the read uses.
// The order is deterministic for one git binary and one repository, so a
// test choreographs its stream against it.
func incrementalHunksOf(t *testing.T, repoPath, path, rev string) []git.BlameHunk {
	t.Helper()
	r := git.New(&git.LocalExecutor{Dir: repoPath})
	var hunks []git.BlameHunk
	if err := r.BlameIncremental(path, rev, func(h git.BlameHunk) { hunks = append(hunks, h) }); err != nil {
		t.Fatalf("BlameIncremental %s: %v", path, err)
	}
	return hunks
}

// blameBlockerExecutor blocks the first git blame invocation until release
// is closed or the command's context ends, and delegates everything —
// including every later blame — to the real executor. entered closes when
// the first blame invocation starts, so a test knows the worker reached the
// blame after emitting the file's content.
type blameBlockerExecutor struct {
	delegate    git.Executor
	release     chan struct{}
	entered     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newBlameBlockerExecutor(t *testing.T, delegate git.Executor) *blameBlockerExecutor {
	t.Helper()
	e := &blameBlockerExecutor{
		delegate: delegate,
		release:  make(chan struct{}),
		entered:  make(chan struct{}),
	}
	t.Cleanup(e.unblock)
	return e
}

func (e *blameBlockerExecutor) Describe() string { return e.delegate.Describe() }

func (e *blameBlockerExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "blame" {
		e.enteredOnce.Do(func() { close(e.entered) })
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return e.delegate.Run(ctx, stdin, args...)
}

func (e *blameBlockerExecutor) unblock() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *blameBlockerExecutor) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-e.entered:
	case <-time.After(queueTimeout):
		t.Fatal("timed out waiting for the blame command to start")
	}
}

// blameCountingExecutor counts git blame invocations while delegating
// everything to the real executor.
type blameCountingExecutor struct {
	delegate git.Executor
	blames   atomic.Int32
}

func (e *blameCountingExecutor) Describe() string { return e.delegate.Describe() }

func (e *blameCountingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "blame" {
		e.blames.Add(1)
	}
	return e.delegate.Run(ctx, stdin, args...)
}

// openBlameHarness builds the test app with its repository opened through
// the wrapped executor and opens path's blame through the real context menu
// of its Local Changes row. Nothing is drained after the menu choice: the
// caller decides how the read's posts are consumed.
func openBlameHarness(t *testing.T, repo *gitRepoFixture, path string, ex git.Executor) *testHarness {
	t.Helper()
	h := newTestApp(t)
	if ex != nil {
		h.app.openRepoFn = func(store.Repo) *git.Repo { return git.New(ex) }
	}
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial snapshot load failed: %v", err)
	}
	chooseFileMenuItem(t, h, "Unstaged "+path, "Blame…")
	return h
}

// blameStreamFixture builds the repository every controlled-stream test
// shares: ten lines committed as base, line 4 rewritten in tweak four, line
// 7 in tweak seven, and an eleventh uncommitted line so the file lists in
// Local Changes. git blame --incremental of it emits newest-first:
//
//	h0  tweak seven  lines 7..7    (1 line)
//	h1  tweak four   lines 4..4    (1 line)
//	h2  base         lines 1..3    (3 lines)
//	h3  base         lines 5..6    (2 lines)
//	h4  base         lines 8..10   (3 lines)
func blameStreamFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	base := b.String()
	repo.CommitFile("code.txt", base, "base")
	tweaked := strings.Replace(base, "line 4\n", "line 4 tweaked\n", 1)
	repo.WriteFile("code.txt", tweaked)
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "tweak four")
	tweaked = strings.Replace(tweaked, "line 7\n", "line 7 tweaked\n", 1)
	repo.WriteFile("code.txt", tweaked)
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "tweak seven")
	repo.WriteFile("code.txt", tweaked+"line 11 uncommitted\n")
	return repo
}

// requireBlameStreamHunkShape pins the fixture's hunk emission order the
// choreographies are written against; a git that emits differently fails
// here rather than halfway through a test.
func requireBlameStreamHunkShape(t *testing.T, hunks []git.BlameHunk) {
	t.Helper()
	type shape struct {
		line, lines int
		subject     string
	}
	want := []shape{
		{7, 1, "tweak seven"},
		{4, 1, "tweak four"},
		{1, 3, "base"},
		{5, 2, "base"},
		{8, 3, "base"},
	}
	if len(hunks) != len(want) {
		t.Fatalf("fixture hunks = %+v, want %d hunks shaped %v", hunks, len(want), want)
	}
	for i, w := range want {
		if hunks[i].Line != w.line || hunks[i].Lines != w.lines || hunks[i].Subject != w.subject {
			t.Fatalf("fixture hunk %d = %+v, want lines %d..%d of %q", i, hunks[i], w.line, w.line+w.lines-1, w.subject)
		}
	}
}

// blameLinesText renders lines joined with line feeds, including the
// trailing one.
func blameLinesText(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// The finished streamed blame of a file written over several commits —
// including one commit that only rewrites whitespace, a CRLF file and a file
// without a trailing newline — matches git blame --porcelain field by field.
func TestFileBlameStreamMatchesPorcelain(t *testing.T) {
	repo := newGitRepo(t, "fbs-match")
	repo.CommitFile("code.txt", blameLinesText("alpha", "beta", "gamma", "delta", "epsilon"), "create code")
	repo.WriteFile("code.txt", blameLinesText("alpha", "beta", "gamma", "delta  spaced", "epsilon"))
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "space out delta")
	// A whitespace-only rewrite: blame -w keeps the line on the older commit.
	repo.WriteFile("code.txt", blameLinesText("alpha", "beta", "gamma", "delta   spaced", "epsilon"))
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "whitespace only")
	repo.WriteFile("code.txt", blameLinesText("alpha changed", "beta", "gamma", "delta   spaced", "epsilon"))
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "change alpha")
	repo.CommitFile("crlf.txt", "c1\r\nc2\r\nc3\r\n", "create crlf")
	repo.CommitFile("noeol.txt", "n1\nn2", "create noeol")
	for _, path := range []string{"code.txt", "crlf.txt", "noeol.txt"} {
		repo.WriteFile(path, "uncommitted dirt\n")
	}

	h := openChangesHarness(t, repo)

	for _, path := range []string{"code.txt", "crlf.txt", "noeol.txt"} {
		chooseFileMenuItem(t, h, "Unstaged "+path, "Blame…")
		h.drain()

		st := h.app.fileInspectorUI()
		if st.blameErr != nil {
			t.Fatalf("blame of %s errored: %v", path, st.blameErr)
		}
		assertBlameLinesMatchPorcelain(t, st.blameLines,
			repo.Git("blame", "--porcelain", "-w", "HEAD", "--", path))

		// The whitespace-only rewrite of delta stays on "space out delta":
		// with -w the purely whitespace change never owns a line.
		if path == "code.txt" {
			for _, l := range st.blameLines {
				if l.Subject == "whitespace only" {
					t.Fatalf("line %+v is owned by the whitespace-only commit: -w did not apply", l)
				}
			}
		}
		// CRLF keeps its carriage return inside the line's text, exactly as
		// the porcelain output carries it.
		if path == "crlf.txt" {
			if len(st.blameLines) != 3 || st.blameLines[0].Text != "c1\r" {
				t.Fatalf("CRLF blame lines = %+v, want 3 lines each ending in a carriage return", st.blameLines)
			}
		}
		if path == "noeol.txt" {
			if len(st.blameLines) != 2 || st.blameLines[1].Text != "n2" {
				t.Fatalf("no-final-newline blame lines = %+v, want exactly 2 lines", st.blameLines)
			}
		}

		if err := h.ui.Click("Close File Inspector"); err != nil {
			t.Fatalf("close inspector: %v", err)
		}
		h.ui.Frame()
	}
}

// While git blame is still computing, the file's lines already fill the
// list with empty commit cells and a progress row counts zero attributed
// lines; when the blame is released the read completes and the row is gone.
func TestFileBlameStreamShowsContentBeforeAttribution(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-content-first")
	h := newTestApp(t)
	// The blocker is built after the harness so its cleanup — releasing the
	// blocked blame command — runs before the harness waits for its workers.
	ex := newBlameBlockerExecutor(t, &git.LocalExecutor{Dir: repo.Path()})
	h.app.openRepoFn = func(store.Repo) *git.Repo { return git.New(ex) }
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.queue.RunNext(t) // the content batch; the blame worker is blocked in its command
	h.ui.Frame()

	st := h.app.fileInspectorUI()
	if !st.blameLoading {
		t.Fatal("blame finished while its command is blocked")
	}
	if len(st.blameLines) != 10 {
		t.Fatalf("blame lines while blocked = %d, want the file's 10 lines", len(st.blameLines))
	}
	if st.blameCovered != 0 {
		t.Fatalf("covered lines while blocked = %d, want 0", st.blameCovered)
	}
	for i, l := range st.blameLines {
		if l.Hash != "" || l.Author != "" || l.Subject != "" || !l.Date.IsZero() || l.First {
			t.Fatalf("line %d carries attribution before the blame ran: %+v", i+1, l)
		}
		want := fmt.Sprintf("line %d", i+1)
		if i+1 == 4 {
			want = "line 4 tweaked"
		}
		if i+1 == 7 {
			want = "line 7 tweaked"
		}
		if l.Text != want {
			t.Fatalf("line %d text = %q, want %q", i+1, l.Text, want)
		}
	}
	// Every line's text is on screen while no commit subject is: the runs
	// have no attribution yet.
	for _, text := range []string{"line 1", "line 4 tweaked", "line 7 tweaked", "line 10"} {
		if !h.ui.HasText(text) {
			t.Fatalf("line text %q missing while the blame computes; texts=%q", text, h.ui.Texts())
		}
	}
	for _, subject := range []string{"tweak seven", "tweak four"} {
		if h.ui.HasText(subject) {
			t.Fatalf("commit subject %q rendered before its hunk arrived; texts=%q", subject, h.ui.Texts())
		}
	}
	if !h.ui.HasText("Loading blame… 0 of 10 lines") {
		t.Fatalf("missing progress row; texts=%q", h.ui.Texts())
	}
	// The blocked blame command proves the content did not wait for it.
	ex.awaitEntered(t)

	ex.unblock()
	h.drain()

	if st.blameLoading {
		t.Fatal("blame still loading after the release drained")
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
	if h.ui.HasText("Loading blame…") {
		t.Fatalf("progress row still rendered after completion; texts=%q", h.ui.Texts())
	}
}

// Hunks fill the attribution in batches: the first hunk ships at once,
// hunks inside one batch window arrive at the UI together, and a window
// elapsing flushes what collected. A hunk delivered in two pieces — the
// incremental format allows any subdivision of a run — shows First only on
// the piece opening the commit's run, and a piece landing after the lines
// behind it flips the follower line's First off.
func TestFileBlameStreamBatchesFillProgressively(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-batches")
	hunks := incrementalHunksOf(t, repo.Path(), "code.txt", "HEAD")
	requireBlameStreamHunkShape(t, hunks)

	h := newTestApp(t)
	clock := &blameFakeClock{}
	// The turnstile is built after the harness so its cleanup — releasing
	// every held delivery — runs before the harness waits for its workers.
	ts := newBlameTurnstile(t)
	// The stream is reshaped ahead of the gate: h2 — base's lines 1-3 —
	// splits into piece B (lines 2-3) at its own position and piece A
	// (line 1) deferred past h3, so the deliveries run
	//
	//	1: h0 tweak seven @7    2: h1 tweak four @4    3: pB base @2-3
	//	4: h3 base @5-6         5: pA base @1          6: h4 base @8-10
	//
	// and piece B fills before the piece behind it exists.
	wrap := func(deliver func(git.BlameHunk)) func(git.BlameHunk) {
		gated := ts.wrap(deliver)
		idx := 0
		var deferredA git.BlameHunk
		return func(h git.BlameHunk) {
			idx++
			switch idx {
			case 3:
				deferredA = git.BlameHunk{Hash: h.Hash, Line: 1, Lines: 1, Author: h.Author, Date: h.Date, Subject: h.Subject, Boundary: h.Boundary}
				gated(git.BlameHunk{Hash: h.Hash, Line: 2, Lines: 2, Author: h.Author, Date: h.Date, Subject: h.Subject, Boundary: h.Boundary})
			case 4:
				gated(h)
				gated(deferredA)
			default:
				gated(h)
			}
		}
	}

	installBlameStreamHooks(h, clock, wrap)
	h.openRepo(repo.StoreRepo())
	h.drain()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	st := h.app.fileInspectorUI()

	h.queue.RunNext(t) // content
	h.ui.Frame()
	if len(st.blameLines) != 10 || st.blameCovered != 0 {
		t.Fatalf("after content: lines=%d covered=%d, want 10 and 0", len(st.blameLines), st.blameCovered)
	}

	// The first hunk ships at once, alone.
	ts.permit(1)
	h.queue.RunNext(t) // batch{h0}
	h.ui.Frame()
	ts.awaitAcks(t, 1)
	if st.blameCovered != 1 {
		t.Fatalf("covered after the first hunk = %d, want 1", st.blameCovered)
	}
	if !h.ui.HasText("Loading blame… 1 of 10 lines") {
		t.Fatalf("missing progress 1 of 10; texts=%q", h.ui.Texts())
	}
	if line := st.blameLines[6]; line.Subject != "tweak seven" || !line.First {
		t.Fatalf("line 7 after its hunk = %+v, want tweak seven with First", line)
	}
	if got := countExactText(h.ui, "tweak seven"); got != 1 {
		t.Fatalf("tweak seven rendered %d times, want exactly 1 on its run's first line", got)
	}

	// The bottom bar describes an attributed line and stays away for one
	// whose hunk has not arrived: choosing line 7 fills it, line 2 empties it.
	if err := h.ui.Click("Blame Line 7"); err != nil {
		t.Fatalf("click Blame Line 7: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText(short7(st.blameLines[6].Hash)) {
		t.Fatalf("bottom bar missing the commit of attributed line 7; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Blame Line 2"); err != nil {
		t.Fatalf("click Blame Line 2: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText(short7(st.blameLines[6].Hash)) {
		t.Fatalf("bottom bar describes a commit while unattributed line 2 is chosen; texts=%q", h.ui.Texts())
	}

	// h1, piece B and h3 collect inside the window: their processing
	// completes without a single batch posted.
	ts.permit(3)
	ts.awaitAcks(t, 4)
	if n := queuePendingLen(h.queue); n != 0 {
		t.Fatalf("%d UI callbacks posted inside the batch window, want none", n)
	}
	if st.blameCovered != 1 {
		t.Fatalf("covered inside the window = %d, want the first hunk's 1", st.blameCovered)
	}

	// The window elapses; piece A's arrival flushes the collection ahead of
	// itself: one batch of h1, piece B and h3.
	clock.advance(150 * time.Millisecond)
	ts.permit(1)
	ts.awaitAcks(t, 5)
	h.queue.RunNext(t) // batch{h1, pB, h3}
	h.ui.Frame()
	if st.blameCovered != 6 {
		t.Fatalf("covered after the window batch = %d, want 6", st.blameCovered)
	}
	if !h.ui.HasText("Loading blame… 6 of 10 lines") {
		t.Fatalf("missing progress 6 of 10; texts=%q", h.ui.Texts())
	}
	// Piece B filled lines 2-3 while line 1 was still empty: line 2 reads as
	// the first line of its run, line 3 as its continuation.
	if line := st.blameLines[1]; line.Subject != "base" || !line.First {
		t.Fatalf("line 2 after piece B = %+v, want base with First while line 1 is empty", line)
	}
	if line := st.blameLines[2]; line.First {
		t.Fatalf("line 3 after piece B = %+v, want a continuation line", line)
	}
	// h1's line 4 follows piece B's different commit, and h3's line 5
	// follows h1's: both read as run starts, and the second line of h3's
	// run does not.
	if line := st.blameLines[3]; line.Subject != "tweak four" || !line.First {
		t.Fatalf("line 4 after the window batch = %+v, want tweak four with First", line)
	}
	if line := st.blameLines[4]; line.Subject != "base" || !line.First {
		t.Fatalf("line 5 after the window batch = %+v, want base with First", line)
	}
	if line := st.blameLines[5]; line.First {
		t.Fatalf("line 6 after the window batch = %+v, want a continuation line", line)
	}

	// h4 collects with piece A; the end of the stream flushes them together.
	// Piece A landing behind piece B flips line 2's First off: the run of
	// base lines 1-3 shows only on line 1.
	ts.permit(1)
	ts.awaitAcks(t, 6)
	h.queue.RunNext(t) // batch{pA, h4}
	h.ui.Frame()
	if st.blameCovered != 10 {
		t.Fatalf("covered after the final batch = %d, want 10", st.blameCovered)
	}
	if line := st.blameLines[0]; line.Subject != "base" || !line.First {
		t.Fatalf("line 1 after piece A = %+v, want base with First", line)
	}
	if line := st.blameLines[1]; line.First {
		t.Fatalf("line 2 after piece A = %+v, want First flipped off: base's run starts at line 1", line)
	}
	if line := st.blameLines[7]; line.Subject != "base" || !line.First {
		t.Fatalf("line 8 after the final batch = %+v, want base with First after tweak seven", line)
	}
	h.queue.RunNext(t) // done
	h.ui.Frame()
	if st.blameLoading {
		t.Fatal("blame still loading after the final batch and done")
	}
	if h.ui.HasText("Loading blame…") {
		t.Fatalf("progress row still rendered after completion; texts=%q", h.ui.Texts())
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
}

// Hunks arriving inside one batch window reach the UI in a single batch:
// after the first hunk's immediate batch, the rest of the stream bursts
// through and exactly one more coverage batch ever lands.
func TestFileBlameStreamThrottleCoalescesBurst(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-throttle")
	hunks := incrementalHunksOf(t, repo.Path(), "code.txt", "HEAD")
	requireBlameStreamHunkShape(t, hunks)

	h := newTestApp(t)
	clock := &blameFakeClock{}
	ts := newBlameTurnstile(t)
	installBlameStreamHooks(h, clock, ts.wrap)
	h.openRepo(repo.StoreRepo())
	h.drain()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	st := h.app.fileInspectorUI()

	h.queue.RunNext(t) // content
	ts.permit(1)
	h.queue.RunNext(t) // batch{h0}: the first hunk ships at once
	h.ui.Frame()
	if st.blameCovered != 1 {
		t.Fatalf("covered after the first hunk = %d, want 1", st.blameCovered)
	}

	// The remaining four hunks burst through inside the frozen window.
	// Exactly one batch carries them: the next coverage jump is the whole
	// rest of the file, with nothing posted in between.
	ts.permit(4)
	ts.awaitAcks(t, 5)
	h.queue.RunNext(t) // the one batch of the burst
	h.ui.Frame()
	if st.blameCovered != 10 {
		t.Fatalf("covered after the burst's batch = %d, want all 10 lines in one batch", st.blameCovered)
	}
	if n := queuePendingLen(h.queue); n != 1 {
		t.Fatalf("%d UI callbacks pending after the burst's batch, want just the done callback", n)
	}
	h.queue.RunNext(t) // done
	h.ui.Frame()
	if st.blameLoading {
		t.Fatal("blame still loading after the burst completed")
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
}

// writeBlameFakeGit writes a git that answers blame --incremental itself
// instead of delegating: when failOnce is set the first invocation prints
// partialOutput and exits 1 — recording the attempt in a marker file so
// later invocations reach the real git — and otherwise every invocation
// prints partialOutput and exits 0, so the stream ends successfully without
// covering the file. Commands that are not an incremental blame delegate to
// the real git.
func writeBlameFakeGit(t *testing.T, partialOutput string, failOnce bool) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		realGit = "/usr/bin/git"
	}
	marker := filepath.Join(t.TempDir(), "blame-failed-once")
	outFile := filepath.Join(t.TempDir(), "partial-blame.txt")
	if err := os.WriteFile(outFile, []byte(partialOutput), 0o644); err != nil {
		t.Fatalf("write partial blame output: %v", err)
	}
	script := "#!/bin/sh\n" +
		"for _a in \"$@\"; do\n" +
		"  if [ \"$_a\" = \"--incremental\" ]; then\n"
	if failOnce {
		script += "    if [ ! -f " + git.ShellQuote(marker) + " ]; then\n" +
			"      touch " + git.ShellQuote(marker) + "\n" +
			"      cat " + git.ShellQuote(outFile) + "\n" +
			"      echo 'fatal: synthetic stream failure' >&2\n" +
			"      exit 1\n" +
			"    fi\n" +
			"    break\n"
	} else {
		script += "    cat " + git.ShellQuote(outFile) + "\n" +
			"    exit 0\n"
	}
	script += "  fi\n" +
		"done\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write blame fake git: %v", err)
	}
	return fake
}

// syntheticPartialHunk is one valid git blame --incremental entry covering
// count lines starting at line, attributed to a made-up commit.
func syntheticPartialHunk(line, count int) string {
	return fmt.Sprintf("%040d %d %d %d\nauthor Synthetic Author\nauthor-time 1700000000\nsummary synthetic partial\nfilename code.txt\n", 1, line, line, count)
}

// A blame that fails after the file and some attribution already showed
// keeps both: the progress row reads the failure with a retry beside it,
// and the retry completes the blame.
func TestFileBlameStreamMidStreamFailureKeepsContent(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-failure")
	// The partial stream covers lines 8-10 and then the command dies.
	fake := writeBlameFakeGit(t, syntheticPartialHunk(8, 3), true)
	ex := &git.LocalExecutor{Dir: repo.Path(), Git: fake}

	h := openBlameHarness(t, repo, "code.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr == nil {
		t.Fatal("expected the blame failure to surface")
	}
	if st.blameLoading {
		t.Fatal("blame still loading after the stream failed")
	}
	if len(st.blameLines) != 10 {
		t.Fatalf("lines after the failure = %d, want the file's 10 lines kept", len(st.blameLines))
	}
	if st.blameCovered != 3 {
		t.Fatalf("covered after the failure = %d, want the partial stream's 3 lines kept", st.blameCovered)
	}
	if line := st.blameLines[7]; line.Subject != "synthetic partial" {
		t.Fatalf("line 8 after the failure = %+v, want the partial stream's attribution kept", line)
	}
	if line := st.blameLines[0]; line.Hash != "" {
		t.Fatalf("line 1 after the failure = %+v, want no attribution", line)
	}
	if !h.ui.HasText("line 4 tweaked") {
		t.Fatalf("file content missing after the failure; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Blame stopped:") {
		t.Fatalf("missing the stopped row; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("synthetic stream failure") {
		t.Fatalf("failure detail missing from the stopped row; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Retry Blame"); err != nil {
		t.Fatalf("click Retry Blame: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if st.blameErr != nil {
		t.Fatalf("blame error after retry: %v", st.blameErr)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
}

// A blame whose stream ends cleanly without covering every line reads as a
// failure: the read cannot have annotated the file, however successful the
// command claimed to be.
func TestFileBlameStreamIncompleteCoverageIsError(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-incomplete")
	fake := writeBlameFakeGit(t, syntheticPartialHunk(8, 3), false)
	ex := &git.LocalExecutor{Dir: repo.Path(), Git: fake}

	h := openBlameHarness(t, repo, "code.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr == nil || !strings.Contains(st.blameErr.Error(), "blame did not cover every line") {
		t.Fatalf("blame error = %v, want blame did not cover every line", st.blameErr)
	}
	if st.blameLoading {
		t.Fatal("blame still loading after the incomplete stream ended")
	}
	if len(st.blameLines) != 10 || st.blameCovered != 3 {
		t.Fatalf("lines=%d covered=%d after the incomplete stream, want the partial 10 and 3 kept",
			len(st.blameLines), st.blameCovered)
	}
	if !h.ui.HasText("Blame stopped: blame did not cover every line") {
		t.Fatalf("missing the coverage failure row; texts=%q", h.ui.Texts())
	}
}

// Closing the inspector halfway through the stream cancels the read: the
// late batches it still posts change nothing — blameLoading is false from
// the close on — and reopening the same file reads the blame afresh and
// completely, even while the canceled read's batches land in between. Only
// the read sequence tells the two reads of the same file apart, so the test
// holds both streams and consumes the canceled read's two remaining posts —
// its last flush and its completion — before the fresh read may emit
// anything.
func TestFileBlameStreamCancelDiscardsLateBatches(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-cancel")
	hunks := incrementalHunksOf(t, repo.Path(), "code.txt", "HEAD")
	requireBlameStreamHunkShape(t, hunks)

	h := newTestApp(t)
	clock := &blameFakeClock{}
	ts1 := newBlameTurnstile(t)
	ts2 := newBlameTurnstile(t)
	var reads atomic.Int32
	wrap := func(deliver func(git.BlameHunk)) func(git.BlameHunk) {
		if reads.Add(1) == 1 {
			return ts1.wrap(deliver)
		}
		return ts2.wrap(deliver)
	}
	installBlameStreamHooks(h, clock, wrap)
	h.openRepo(repo.StoreRepo())
	h.drain()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	st := h.app.fileInspectorUI()

	h.queue.RunNext(t) // content of the first read
	ts1.permit(1)
	h.queue.RunNext(t) // batch{h0}
	h.ui.Frame()
	ts1.awaitAcks(t, 1)
	if st.blameCovered != 1 {
		t.Fatalf("covered before the close = %d, want 1", st.blameCovered)
	}

	// The first stream stands inside the delivery of its second hunk.
	// Closing the inspector cancels the read.
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v", err)
	}
	h.ui.Frame()
	if st.open {
		t.Fatal("inspector still open after Close")
	}
	if st.blameLoading {
		t.Fatal("blameLoading stuck after the close")
	}

	// Reopening the same file starts a fresh read; it posts its content and
	// then stands inside the delivery of its first hunk.
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.queue.RunNext(t) // content of the second read
	h.ui.Frame()
	if len(st.blameLines) != 10 || st.blameCovered != 0 {
		t.Fatalf("second read after content: lines=%d covered=%d, want 10 and 0", len(st.blameLines), st.blameCovered)
	}

	// Releasing the first stream lets the canceled read finish: the hunk it
	// stood inside completes its delivery, and however much more of the
	// stream the parser still reads, exactly one last flush and the
	// completion are posted. Both run now, before the fresh read emits
	// anything — and neither may touch the state.
	ts1.open()
	h.queue.RunNext(t) // the canceled read's last flush
	h.queue.RunNext(t) // the canceled read's completion
	h.ui.Frame()
	if st.blameCovered != 0 {
		t.Fatalf("canceled read's late batches filled attribution: covered = %d, want 0", st.blameCovered)
	}
	for i, l := range st.blameLines {
		if l.Hash != "" {
			t.Fatalf("line %d carries attribution from the canceled read: %+v", i+1, l)
		}
	}
	if st.blameErr != nil {
		t.Fatalf("the canceled read left an error behind: %v", st.blameErr)
	}
	if !st.blameLoading {
		t.Fatal("the fresh read stopped loading when the canceled one ended")
	}

	// The fresh read completes untouched.
	ts2.open()
	h.drain()
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
	if st.blameErr != nil {
		t.Fatalf("the fresh read ended in error after the canceled read's batches landed: %v", st.blameErr)
	}
}

// Switching to File History halfway through the stream keeps the blame read
// running in the background — batches keep landing — and switching back
// does not restart it: git blame runs exactly once for the whole exercise.
func TestFileBlameStreamModeSwitchKeepsReading(t *testing.T) {
	repo := blameStreamFixture(t, "fbs-modeswitch")
	hunks := incrementalHunksOf(t, repo.Path(), "code.txt", "HEAD")
	requireBlameStreamHunkShape(t, hunks)

	h := newTestApp(t)
	clock := &blameFakeClock{}
	ts := newBlameTurnstile(t)
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}
	h.app.openRepoFn = func(store.Repo) *git.Repo { return git.New(ex) }
	installBlameStreamHooks(h, clock, ts.wrap)
	h.openRepo(repo.StoreRepo())
	h.drain()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	st := h.app.fileInspectorUI()

	h.queue.RunNext(t) // content
	ts.permit(1)
	h.queue.RunNext(t) // batch{h0}
	h.ui.Frame()
	ts.awaitAcks(t, 1)
	if st.blameCovered != 1 {
		t.Fatalf("covered before the switch = %d, want 1", st.blameCovered)
	}

	// To File History: its own read runs — the log, then the diff it chains
	// — while the blame stream stands inside its second hunk's delivery.
	if err := h.ui.Click("File History"); err != nil {
		t.Fatalf("click File History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.queue.RunNext(t) // file history log
	h.queue.RunNext(t) // the diff the selection chains
	h.ui.Frame()
	if len(st.histCommits) == 0 {
		t.Fatal("file history did not load in history mode")
	}
	if !st.blameLoading || st.blameCovered != 1 {
		t.Fatalf("blame state in history mode: loading=%v covered=%d, want loading with 1 covered",
			st.blameLoading, st.blameCovered)
	}

	// The window elapses and the stream goes on while history shows: the
	// hunk past the window ships at once, the one after collects.
	clock.advance(150 * time.Millisecond)
	ts.permit(2)
	ts.awaitAcks(t, 3)
	h.queue.RunNext(t) // batch{h1}, emitted while the inspector is in history mode
	h.ui.Frame()
	if st.blameCovered != 2 {
		t.Fatalf("covered while in history mode = %d, want 2: the read went on in the background", st.blameCovered)
	}

	// Back to Blame: the read in flight is picked up, not restarted, and
	// the progress row counts what the background filled.
	if err := h.ui.Click("Blame"); err != nil {
		t.Fatalf("click Blame: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if st.blameCovered != 2 {
		t.Fatalf("covered after switching back = %d, want the 2 already filled", st.blameCovered)
	}
	if !h.ui.HasText("Loading blame… 2 of 10 lines") {
		t.Fatalf("missing progress after switching back; texts=%q", h.ui.Texts())
	}

	ts.open()
	h.drain()
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations = %d, want exactly 1 across both mode switches", n)
	}
}

// An empty file at the blamed revision needs no blame at all: the read ends
// after the content and git blame never runs.
func TestFileBlameStreamEmptyFileRunsNoBlame(t *testing.T) {
	repo := newGitRepo(t, "fbs-empty")
	repo.CommitFile("empty.txt", "", "add empty file")
	repo.WriteFile("empty.txt", "dirty\n")
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := openBlameHarness(t, repo, "empty.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("blame of the empty file errored: %v", st.blameErr)
	}
	if st.blameLines == nil || len(st.blameLines) != 0 {
		t.Fatalf("blame lines of the empty file = %+v, want empty", st.blameLines)
	}
	if !h.ui.HasText("This file has no lines") {
		t.Fatalf("missing empty-file state; texts=%q", h.ui.Texts())
	}
	if n := ex.blames.Load(); n != 0 {
		t.Fatalf("git blame invocations for the empty file = %d, want 0", n)
	}
}

// Over a real loopback SSH connection the streamed blame completes as it
// does locally, and the finished attribution matches a local read of the
// same repository line for line.
func TestFileBlameStreamLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	sshHistoryFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHRepoInApp(t, h, repo)

	chooseFileMenuItem(t, h, "Unstaged history.txt", "Blame…")
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("SSH blame errored: %v", st.blameErr)
	}
	if st.blameLoading {
		t.Fatal("SSH blame still loading after the drain")
	}
	// The loopback server's repository is a real directory of this machine:
	// a plain local read of the same file at the same revision is the
	// reference the SSH result must match field by field.
	local := git.New(&git.LocalExecutor{Dir: repo.Path})
	want, err := local.BlameLines("history.txt", "HEAD")
	if err != nil {
		t.Fatalf("local BlameLines: %v", err)
	}
	if len(st.blameLines) != len(want) {
		t.Fatalf("SSH blame lines = %d, local read = %d", len(st.blameLines), len(want))
	}
	for i := range want {
		if st.blameLines[i] != want[i] {
			t.Fatalf("SSH blame line %d = %+v, local read = %+v", i+1, st.blameLines[i], want[i])
		}
	}
	if len(want) != 4 {
		t.Fatalf("fixture: blame lines = %d, want 4", len(want))
	}
}
