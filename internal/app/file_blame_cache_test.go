package app

import (
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// The tests of this file drive the blame cache through the real UI of real
// repositories, opening Blame through the real context menu entries, and count
// git blame invocations through blameCountingExecutor: a finished blame of one
// file at one commit never changes, so a second open of it must not run git
// again, while anything that could change the answer — a moved HEAD, another
// commit, another repository, a failed or canceled read — must.

// installBlameCacheCaps shrinks the cache's file and line caps for one test,
// restoring the originals with the test's cleanup.
func installBlameCacheCaps(t *testing.T, maxFiles, maxLines int) {
	t.Helper()
	prevFiles, prevLines := fileBlameCacheMaxFiles, fileBlameCacheMaxLines
	t.Cleanup(func() {
		fileBlameCacheMaxFiles = prevFiles
		fileBlameCacheMaxLines = prevLines
	})
	fileBlameCacheMaxFiles = maxFiles
	fileBlameCacheMaxLines = maxLines
}

// closeInspector closes the file inspector through its Close button and
// renders the frame the close settles in.
func closeInspector(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// Opening the same file's blame a second time from Local Changes while HEAD
// has not moved runs no git blame at all: the very first frame shows the
// finished view without a progress row, and the lines the cache serves are
// field by field the ones a direct blame read of the revision produces.
func TestFileBlameCacheSecondOpenHitsWithoutGit(t *testing.T) {
	repo := blameStreamFixture(t, "fbc-second-open")
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := openBlameHarness(t, repo, "code.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("first blame errored: %v", st.blameErr)
	}
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after the first open = %d, want 1", n)
	}
	local := git.New(&git.LocalExecutor{Dir: repo.Path()})
	want, err := local.BlameLines("code.txt", "HEAD")
	if err != nil {
		t.Fatalf("reference BlameLines: %v", err)
	}
	if len(st.blameLines) != len(want) {
		t.Fatalf("first open blame lines = %d, direct read = %d", len(st.blameLines), len(want))
	}
	for i := range want {
		if st.blameLines[i] != want[i] {
			t.Fatalf("first open blame line %d = %+v, direct read = %+v", i+1, st.blameLines[i], want[i])
		}
	}
	firstLines := len(st.blameLines)

	closeInspector(t, h)

	// The second open serves the cache: no blame command runs, and the first
	// frame after the menu choice already shows the finished view — the
	// progress row of a computing blame never appears.
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.ui.Frame()
	h.drain()

	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after the cached reopen = %d, want still 1", n)
	}
	if st.blameLoading {
		t.Fatal("blame loading after a cache hit")
	}
	if st.blameErr != nil {
		t.Fatalf("cache hit surfaced an error: %v", st.blameErr)
	}
	if len(st.blameLines) != firstLines {
		t.Fatalf("cached blame lines = %d, want the first open's %d", len(st.blameLines), firstLines)
	}
	for i := range want {
		if st.blameLines[i] != want[i] {
			t.Fatalf("cached blame line %d = %+v, direct read = %+v", i+1, st.blameLines[i], want[i])
		}
	}
	if h.ui.HasText("Loading blame") {
		t.Fatalf("progress row rendered on a cache hit; texts=%q", h.ui.Texts())
	}
}

// A blame opened from Local Changes pins itself to the snapshot's HEAD
// commit: after a new commit rewriting the file lands and the snapshot
// refreshes, reopening the blame runs git blame again and the rewritten line
// belongs to the new commit.
func TestFileBlameCacheNewCommitInvalidatesHead(t *testing.T) {
	repo := newGitRepo(t, "fbc-head-move")
	repo.CommitFile("code.txt", blameLinesText("alpha", "beta", "gamma"), "create code")
	repo.WriteFile("code.txt", blameLinesText("alpha", "beta", "gamma", "dirty"))
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := openBlameHarness(t, repo, "code.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("first blame errored: %v", st.blameErr)
	}
	if got := st.blameLines[0].Subject; got != "create code" {
		t.Fatalf("line 1 of the first blame belongs to %q, want create code", got)
	}
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after the first open = %d, want 1", n)
	}
	closeInspector(t, h)

	// A new commit rewrites line 1; the file is dirtied again so it keeps its
	// row in Local Changes, and the snapshot refresh learns the new HEAD.
	second := repo.CommitFile("code.txt", blameLinesText("alpha changed", "beta", "gamma"), "change alpha")
	repo.WriteFile("code.txt", blameLinesText("alpha changed", "beta", "gamma", "dirty"))
	h.app.session.Reload()
	h.drain()

	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()

	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after HEAD moved = %d, want 2: the new HEAD must be re-blamed", n)
	}
	if st.blameErr != nil {
		t.Fatalf("blame at the new HEAD errored: %v", st.blameErr)
	}
	if len(st.blameLines) != 3 {
		t.Fatalf("blame lines at the new HEAD = %d, want 3", len(st.blameLines))
	}
	if st.blameLines[0].Hash != second {
		t.Fatalf("line 1 belongs to %s, want the new commit %s", st.blameLines[0].Hash, second)
	}
	if got := st.blameLines[0].Subject; got != "change alpha" {
		t.Fatalf("line 1 of the new blame belongs to %q, want change alpha", got)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))

	// The new HEAD's blame caches in turn: closing and reopening runs nothing.
	closeInspector(t, h)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after reopening at the same HEAD = %d, want still 2", n)
	}
}

// A blame pinned at a commit of the History page caches under that commit:
// opening commit X and then X's parent runs git blame once each, and
// returning to X serves the cache without another run.
func TestFileBlameCacheHistoryCommitsHitPerCommit(t *testing.T) {
	repo := newGitRepo(t, "fbc-history-pins")
	first := repo.CommitFile("target.txt", "l1\nl2\n", "add target")
	second := repo.CommitFile("target.txt", "l1\nl2\nl3\n", "extend target")

	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}
	h := newTestApp(t)
	h.app.openRepoFn = func(store.Repo) *git.Repo { return git.New(ex) }
	openHistoryPage(t, h, repo.StoreRepo())

	clickCommitRow(t, h, second, "extend target")
	h.drain()
	chooseFileMenuItem(t, h, fileRowLabel("target.txt"), "Blame…")
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("blame at extend target errored: %v", st.blameErr)
	}
	if st.blameRev != second {
		t.Fatalf("blame rev = %q, want the shown commit %q", st.blameRev, second)
	}
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after opening extend target = %d, want 1", n)
	}
	closeInspector(t, h)

	clickCommitRow(t, h, first, "add target")
	h.drain()
	chooseFileMenuItem(t, h, fileRowLabel("target.txt"), "Blame…")
	h.drain()

	if st.blameRev != first {
		t.Fatalf("blame rev = %q, want the shown commit %q", st.blameRev, first)
	}
	if len(st.blameLines) != 2 {
		t.Fatalf("blame lines at add target = %d, want 2", len(st.blameLines))
	}
	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after opening add target = %d, want 2: another commit, another blame", n)
	}
	closeInspector(t, h)

	// Back at extend target the cached blame serves: no third invocation, and
	// the lines are exactly the ones the first open computed.
	clickCommitRow(t, h, second, "extend target")
	h.drain()
	chooseFileMenuItem(t, h, fileRowLabel("target.txt"), "Blame…")
	h.drain()

	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after returning to extend target = %d, want still 2", n)
	}
	if st.blameErr != nil || st.blameLoading {
		t.Fatalf("cached blame of extend target: err=%v loading=%v, want settled", st.blameErr, st.blameLoading)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", second, "--", "target.txt"))
	if h.ui.HasText("Loading blame") {
		t.Fatalf("progress row rendered on a cache hit; texts=%q", h.ui.Texts())
	}
}

// A blame whose read fails is never cached: closing the inspector and
// opening the same file again runs git blame again, and once the rerun
// finishes cleanly its result caches like any other.
func TestFileBlameCacheFailureNotCached(t *testing.T) {
	repo := blameStreamFixture(t, "fbc-failure")
	fake := writeBlameFakeGit(t, syntheticPartialHunk(8, 3), true)
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path(), Git: fake}}

	h := openBlameHarness(t, repo, "code.txt", ex)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.blameErr == nil {
		t.Fatal("expected the first blame to fail")
	}
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after the failure = %d, want 1", n)
	}
	closeInspector(t, h)

	// Reopening — rather than hitting a cached failure — runs the blame
	// again, and the fake git now delegates to the real one.
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after reopening the failed blame = %d, want 2: a failure must not cache", n)
	}
	if st.blameErr != nil {
		t.Fatalf("blame after the reopen errored: %v", st.blameErr)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))

	// The finished rerun cached: one more close and open runs nothing.
	closeInspector(t, h)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after the successful rerun = %d, want still 2", n)
	}
}

// A blame read canceled by closing the inspector is never cached: reopening
// the same file runs git blame again, and the fresh read completes with the
// full attribution.
func TestFileBlameCacheCanceledReadNotCached(t *testing.T) {
	repo := blameStreamFixture(t, "fbc-cancel")
	blocker := newBlameBlockerExecutor(t, &git.LocalExecutor{Dir: repo.Path()})
	ex := &blameCountingExecutor{delegate: blocker}

	h := openBlameHarness(t, repo, "code.txt", ex)
	// The read posts the file's content and then stands inside the blocked
	// blame command; the invocation already counts.
	h.queue.RunNext(t) // the content batch
	h.ui.Frame()
	blocker.awaitEntered(t)
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations while blocked = %d, want 1", n)
	}

	closeInspector(t, h)
	// The close cancels the read's context, so the blocked command ends in
	// error; releasing the gate lets everything settle.
	blocker.unblock()
	h.drain()

	st := h.app.fileInspectorUI()
	if st.open {
		t.Fatal("inspector still open after Close")
	}

	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if n := ex.blames.Load(); n != 2 {
		t.Fatalf("git blame invocations after reopening the canceled blame = %d, want 2: a canceled read must not cache", n)
	}
	if st.blameErr != nil {
		t.Fatalf("blame after the reopen errored: %v", st.blameErr)
	}
	if st.blameLoading {
		t.Fatal("blame still loading after the reopened read drained")
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
}

// Switching to another repository and back empties the cache: reopening the
// same file of the first repository — same commit, same path — runs git
// blame again.
func TestFileBlameCacheRepoSwitchClears(t *testing.T) {
	repoA := newGitRepo(t, "fbc-switch-a")
	repoA.CommitFile("code.txt", "a1\n", "commit in a")
	repoA.WriteFile("code.txt", "a1\na2 dirty\n")
	repoB := newGitRepo(t, "fbc-switch-b")
	repoB.CommitFile("code.txt", "b1\n", "commit in b")
	repoB.WriteFile("code.txt", "b1\nb2 dirty\n")

	// Every open of a repository builds its own counting executor, so the
	// count of the second session of repository A is exactly the blame
	// invocations that session ran.
	executors := map[string]*blameCountingExecutor{}
	h := newTestApp(t, repoB.StoreRepo())
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: meta.Path}}
		executors[meta.Path] = ex
		return git.New(ex)
	}
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial snapshot load failed: %v", err)
	}

	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if n := executors[repoA.Path()].blames.Load(); n != 1 {
		t.Fatalf("git blame invocations of the first open in repository A = %d, want 1", n)
	}
	closeInspector(t, h)

	// To repository B and back: two generation changes.
	for _, name := range []string{"fbc-switch-b", "fbc-switch-a"} {
		if err := h.ui.Click("Repository Switcher"); err != nil {
			t.Fatalf("open Repository Switcher: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.Click(name); err != nil {
			t.Fatalf("select %q: %v", name, err)
		}
		h.ui.Frame()
		h.drain()
		if err := h.app.session.LoadErr(); err != nil {
			t.Fatalf("snapshot load of %q failed: %v", name, err)
		}
	}

	// The reopening of repository A built a fresh executor; a cache that
	// survived the switch would run no blame in it at all.
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("blame after the repository roundtrip errored: %v", st.blameErr)
	}
	if n := executors[repoA.Path()].blames.Load(); n != 1 {
		t.Fatalf("git blame invocations of the second session of repository A = %d, want 1: the cache must not survive a repository switch", n)
	}
	if len(st.blameLines) != 1 || st.blameLines[0].Text != "a1" {
		t.Fatalf("blame lines after the roundtrip = %+v, want repository A's one line", st.blameLines)
	}
}

// With the file cap shrunk to two, the entry unused the longest is evicted
// first: opening f1, f2, then f1 again — a hit, making f2 the oldest — and
// then f3 evicts f2, so f1 still hits while f2 must be read again.
func TestFileBlameCacheEvictsLeastRecentlyUsed(t *testing.T) {
	installBlameCacheCaps(t, 2, 200000)
	repo := newGitRepo(t, "fbc-evict")
	for _, f := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		repo.CommitFile(f, f+" line one\n"+f+" line two\n"+f+" line three\n", "add "+f)
	}
	for _, f := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		repo.WriteFile(f, f+" line one\n"+f+" line two\n"+f+" line three\ndirty\n")
	}
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := openBlameHarness(t, repo, "f1.txt", ex)
	h.drain()
	closeInspector(t, h)

	open := func(f string) {
		t.Helper()
		chooseFileMenuItem(t, h, "Unstaged "+f, "Blame…")
		h.drain()
		if st := h.app.fileInspectorUI(); st.blameErr != nil {
			t.Fatalf("blame of %s errored: %v", f, st.blameErr)
		}
		closeInspector(t, h)
	}
	wantBlames := func(want int32, what string) {
		t.Helper()
		if n := ex.blames.Load(); n != want {
			t.Fatalf("git blame invocations %s = %d, want %d", what, n, want)
		}
	}

	wantBlames(1, "after opening f1")

	open("f2.txt")
	wantBlames(2, "after opening f2")

	// The second open of f1 hits, and the hit counts as a use: f1 is now the
	// most recently used entry, f2 the least.
	open("f1.txt")
	wantBlames(2, "after reopening f1")

	// f3 enters a full cache: f2, unused the longest, is the entry evicted.
	open("f3.txt")
	wantBlames(3, "after opening f3")

	open("f1.txt")
	wantBlames(3, "after reopening f1 again")

	open("f2.txt")
	wantBlames(4, "after reopening the evicted f2")
}

// A file longer than the line cap is never stored — and storing it evicts
// nothing either, so the entries already cached survive it untouched.
func TestFileBlameCacheOversizedFileNotStored(t *testing.T) {
	installBlameCacheCaps(t, 16, 5)
	repo := newGitRepo(t, "fbc-oversized")
	repo.CommitFile("small.txt", "s1\ns2\ns3\n", "add small")
	var big string
	for i := 1; i <= 10; i++ {
		big += "big line\n"
	}
	repo.CommitFile("big.txt", big, "add big")
	repo.WriteFile("small.txt", "s1\ns2\ns3\ndirty\n")
	repo.WriteFile("big.txt", big+"dirty\n")
	ex := &blameCountingExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := openBlameHarness(t, repo, "small.txt", ex)
	h.drain()
	closeInspector(t, h)
	if n := ex.blames.Load(); n != 1 {
		t.Fatalf("git blame invocations after opening small = %d, want 1", n)
	}

	// big.txt's ten lines exceed the five-line cap: neither of its opens
	// leaves anything cached.
	for i := 0; i < 2; i++ {
		chooseFileMenuItem(t, h, "Unstaged big.txt", "Blame…")
		h.drain()
		if st := h.app.fileInspectorUI(); st.blameErr != nil {
			t.Fatalf("blame of big.txt errored: %v", st.blameErr)
		}
		closeInspector(t, h)
	}
	if n := ex.blames.Load(); n != 3 {
		t.Fatalf("git blame invocations after opening big twice = %d, want 3: an oversized file must never cache", n)
	}

	// small.txt, cached before the oversized file opened, still hits: the
	// oversized file never entered the cache, so it evicted nothing.
	chooseFileMenuItem(t, h, "Unstaged small.txt", "Blame…")
	h.drain()
	if n := ex.blames.Load(); n != 3 {
		t.Fatalf("git blame invocations after reopening small = %d, want still 3: the oversized file must not evict the cached one", n)
	}
}
