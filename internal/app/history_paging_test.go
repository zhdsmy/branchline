package app

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/repository"
	"github.com/zhdsmy/branchline/internal/store"
)

// historyPagingImport feeds stream to git fast-import in the fixture's
// repository. It duplicates frameCostImport because that helper builds only
// without the race detector while these tests run under it.
func historyPagingImport(t *testing.T, repo *gitRepoFixture, stream string) {
	t.Helper()
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = repo.Path()
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(stream)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, out)
	}
}

// historyPagingLinearStream builds a fast-import stream of n commits in one
// chain on refs/heads/main, commit i writing f.txt with distinct content.
// base, when not empty, is the commit the chain starts from.
func historyPagingLinearStream(n int, base string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		msg := fmt.Sprintf("commit %d", i)
		content := fmt.Sprintf("content %d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		switch {
		case i > 1:
			fmt.Fprintf(&b, "from :%d\n", i-1)
		case base != "":
			fmt.Fprintf(&b, "from %s\n", base)
		}
		fmt.Fprintf(&b, "M 100644 inline f.txt\ndata %d\n%s\n", len(content), content)
	}
	return b.String()
}

// historyPagingMergeStream builds a fast-import stream of n mainline commits
// on refs/heads/main where every stride-th commit merges a side commit
// branched off the previous mainline commit. Side commit timestamps are
// skewed far into the future so a date-order walk interleaves them
// differently than a topo-order walk. base, when not empty, is the commit
// the chain starts from.
func historyPagingMergeStream(n, stride int, base string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i%stride == 0 {
			side := 200000 + i
			msg := fmt.Sprintf("side %d", i)
			content := fmt.Sprintf("side content %d\n", i)
			fmt.Fprintf(&b, "commit refs/heads/side\nmark :%d\n", side)
			fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i+500000)
			fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i+500000)
			fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
			if i > 1 {
				fmt.Fprintf(&b, "from :%d\n", i-1)
			}
			fmt.Fprintf(&b, "M 100644 inline side.txt\ndata %d\n%s\n", len(content), content)
		}
		msg := fmt.Sprintf("commit %d", i)
		content := fmt.Sprintf("content %d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		switch {
		case i > 1:
			fmt.Fprintf(&b, "from :%d\n", i-1)
		case base != "":
			fmt.Fprintf(&b, "from %s\n", base)
		}
		if i%stride == 0 {
			fmt.Fprintf(&b, "merge :%d\n", 200000+i)
		}
		fmt.Fprintf(&b, "M 100644 inline f.txt\ndata %d\n%s\n", len(content), content)
	}
	return b.String()
}

// historyPagingInterleavedStream builds a fast-import stream of main
// mainline commits merged with a topic branch of topic commits forked from
// the second mainline commit, where topic commit timestamps interleave with
// the mainline ones. A date-order walk weaves the topic commits between the
// mainline commits by date while a topo-order walk keeps the topic in one
// contiguous block, so the two walks genuinely differ.
func historyPagingInterleavedStream(main, topic int) string {
	var b strings.Builder
	ts0 := 1700000000
	for i := 1; i <= main; i++ {
		msg := fmt.Sprintf("main %d", i)
		content := fmt.Sprintf("main content %d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts0+i*100)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts0+i*100)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline f.txt\ndata %d\n%s\n", len(content), content)
	}
	// Topic timestamps weave between the mainline ones: mainline commits
	// sit 100 units apart while topic commits step by 150 starting at 205,
	// so a topic commit lands strictly between two mainline commits with no
	// timestamp ever colliding.
	for j := 1; j <= topic; j++ {
		mark := 100000 + j
		ts := ts0 + 205 + j*150
		msg := fmt.Sprintf("topic %d", j)
		content := fmt.Sprintf("topic content %d\n", j)
		fmt.Fprintf(&b, "commit refs/heads/topic\nmark :%d\n", mark)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if j == 1 {
			fmt.Fprintf(&b, "from :2\n")
		} else {
			fmt.Fprintf(&b, "from :%d\n", mark-1)
		}
		fmt.Fprintf(&b, "M 100644 inline g.txt\ndata %d\n%s\n", len(content), content)
	}
	merge := 200000
	ts := ts0 + main*100 + 25
	fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", merge)
	fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts)
	fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts)
	msg := "merge topic"
	fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
	fmt.Fprintf(&b, "from :%d\n", main)
	fmt.Fprintf(&b, "merge :%d\n", 100000+topic)
	return b.String()
}

// historyPagingTwoRootStream builds a fast-import stream of n mainline
// commits on refs/heads/main, one orphan root commit on refs/heads/orphan
// with no parent at all, and a final merge commit joining the two roots.
func historyPagingTwoRootStream(n int) string {
	var b strings.Builder
	orphan := 300001
	msg := "orphan root"
	content := "orphan content\n"
	fmt.Fprintf(&b, "commit refs/heads/orphan\nmark :%d\n", orphan)
	fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000500)
	fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000500)
	fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
	fmt.Fprintf(&b, "M 100644 inline orphan.txt\ndata %d\n%s\n", len(content), content)
	b.WriteString(historyPagingLinearStream(n, ""))
	merge := 300002
	msg = "merge orphan"
	fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", merge)
	fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+n+1)
	fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+n+1)
	fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
	fmt.Fprintf(&b, "from :%d\n", n)
	fmt.Fprintf(&b, "merge :%d\n", orphan)
	return b.String()
}

// fullLoadCommits reads the history of meta in one shot with the given limit
// and ordering, the way a complete reload does, and returns its commits and
// graph: the reference every paged result must match exactly.
func fullLoadCommits(t *testing.T, meta store.Repo, limit int, dateOrder bool) ([]git.Commit, []git.GraphRow) {
	t.Helper()
	repo := defaultOpenGitRepo(meta)
	snap, err := repository.Load(repo, repository.Options{
		SkipCapabilities: true,
		Log:              git.LogOptions{Limit: limit, DateOrder: dateOrder},
	})
	if err != nil {
		t.Fatalf("reference full load: %v", err)
	}
	return snap.Commits, snap.Graph
}

// firstCommitDiff locates the first index where a and b differ, for
// failure messages.
func firstCommitDiff(a, b []git.Commit) string {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			return fmt.Sprintf("first difference at index %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("one slice is a prefix of the other: %d vs %d commits", len(a), len(b))
}

// assertMatchesFullLoad verifies the session snapshot's commits and graph
// equal those of a single full load of count commits with the same ordering.
func assertMatchesFullLoad(t *testing.T, h *testHarness, count int, dateOrder bool) {
	t.Helper()
	snap := h.app.session.Snapshot()
	wantCommits, wantGraph := fullLoadCommits(t, h.app.session.Meta(), count, dateOrder)
	if !reflect.DeepEqual(snap.Commits, wantCommits) {
		t.Fatalf("paged commits differ from a full load of %d: %s", count, firstCommitDiff(snap.Commits, wantCommits))
	}
	if !reflect.DeepEqual(snap.Graph, wantGraph) {
		t.Fatalf("paged graph differs from a full load of %d: %d vs %d rows", count, len(snap.Graph), len(wantGraph))
	}
}

// loadCommitsThroughPaging narrows the session page size, reloads, clicks
// through rounds pages of step commits each by calling LoadMore directly, and
// returns the number of commits loaded.
func loadCommitsThroughPaging(t *testing.T, h *testHarness, step, rounds int) int {
	t.Helper()
	h.app.session.logLimit = step
	h.app.session.Reload()
	h.drain()
	if got := len(h.app.session.Snapshot().Commits); got != step {
		t.Fatalf("first page = %d commits, want %d", got, step)
	}
	if !h.app.session.Snapshot().More {
		t.Fatal("first page unexpectedly reached the end of the history")
	}
	for i := 0; i < rounds; i++ {
		h.app.session.LoadMore(step)
		h.drain()
	}
	if h.app.session.Loading() {
		t.Fatal("session still loading after drain")
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after paging: %v", err)
	}
	return len(h.app.session.Snapshot().Commits)
}

// Load More on a linear history: paging page by page must produce exactly
// the commits and graph of reading the same number of commits in one load.
func TestHistoryPagingMatchesFullLoadLinear(t *testing.T) {
	repo := newGitRepo(t, "paging-linear")
	historyPagingImport(t, repo, historyPagingLinearStream(2200, ""))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	loaded := loadCommitsThroughPaging(t, h, 500, 3)
	if loaded != 2000 {
		t.Fatalf("loaded commits = %d, want 2000", loaded)
	}
	assertMatchesFullLoad(t, h, 2000, false)
}

// The same on a history with a merge every 50 commits, so pages join across
// branching lanes.
func TestHistoryPagingMatchesFullLoadMerges(t *testing.T) {
	repo := newGitRepo(t, "paging-merges")
	historyPagingImport(t, repo, historyPagingMergeStream(1500, 50, ""))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	loaded := loadCommitsThroughPaging(t, h, 500, 2)
	if loaded != 1500 {
		t.Fatalf("loaded commits = %d, want 1500", loaded)
	}
	assertMatchesFullLoad(t, h, 1500, false)
}

// The same on a history with two roots: an orphan branch merged into main,
// so the walk crosses the join of two unrelated commit chains.
func TestHistoryPagingMatchesFullLoadTwoRoots(t *testing.T) {
	repo := newGitRepo(t, "paging-two-roots")
	historyPagingImport(t, repo, historyPagingTwoRootStream(800))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// 802 commits total: paging past the end must stop exactly at the full
	// history and match it.
	loaded := loadCommitsThroughPaging(t, h, 500, 1)
	if loaded != 802 {
		t.Fatalf("loaded commits = %d, want all 802", loaded)
	}
	if h.app.session.Snapshot().More {
		t.Fatal("More still true after the whole history loaded")
	}
	assertMatchesFullLoad(t, h, 802, false)
}

// The same with the history sorted by committer date rather than topology.
// The merge fixture skews side-commit dates far into the future so the two
// orders genuinely differ; the paging must follow the active order either
// way.
func TestHistoryPagingMatchesFullLoadDateOrder(t *testing.T) {
	for _, dateOrder := range []bool{false, true} {
		name := "topo"
		if dateOrder {
			name = "date"
		}
		t.Run(name, func(t *testing.T) {
			repo := newGitRepo(t, "paging-order-"+name)
			historyPagingImport(t, repo, historyPagingMergeStream(1500, 50, ""))

			h := newTestApp(t)
			h.openRepo(repo.StoreRepo())
			h.drain()
			if dateOrder {
				h.app.session.SetDateOrder(true)
				h.drain()
			}

			loaded := loadCommitsThroughPaging(t, h, 500, 2)
			if loaded != 1500 {
				t.Fatalf("loaded commits = %d, want 1500", loaded)
			}
			assertMatchesFullLoad(t, h, 1500, dateOrder)
		})
	}
}

// A page boundary landing exactly on a merge commit: the first page ends on
// a commit with two parents, and the seam check joins the next page over it.
func TestHistoryPagingMatchesFullLoadBoundaryOnMerge(t *testing.T) {
	repo := newGitRepo(t, "paging-boundary")
	historyPagingImport(t, repo, historyPagingMergeStream(1200, 50, ""))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Find the first merge in the default first page at a depth of a hundred
	// commits or more — the newest commit is itself a merge here — and end
	// the page on it.
	snap := h.app.session.Snapshot()
	boundary := -1
	for i, cm := range snap.Commits {
		if len(cm.Parents) == 2 && i >= 100 {
			boundary = i
			break
		}
	}
	if boundary < 0 {
		t.Fatal("no deep merge commit in the first 500 commits of the fixture")
	}

	step := 500
	h.app.session.logLimit = boundary + 1
	h.app.session.Reload()
	h.drain()

	snap = h.app.session.Snapshot()
	if got := len(snap.Commits); got != boundary+1 {
		t.Fatalf("first page = %d commits, want %d", got, boundary+1)
	}
	if got := len(snap.Commits[boundary].Parents); got != 2 {
		t.Fatalf("page boundary commit parents = %d, want 2 (a merge)", got)
	}

	for i := 0; i < 2; i++ {
		h.app.session.LoadMore(step)
		h.drain()
	}
	loaded := len(h.app.session.Snapshot().Commits)
	want := boundary + 1 + 2*step
	if loaded != want {
		t.Fatalf("loaded commits = %d, want %d", loaded, want)
	}
	assertMatchesFullLoad(t, h, want, false)
}

// When the history moves after the first page was read — a commit added
// outside the app, so no refresh has noticed — the seam commit of the next
// page no longer matches the loaded tail. The page must be dropped and the
// whole snapshot reloaded, ending exactly as one full read.
func TestHistoryPagingHistoryChangedFallsBackToReload(t *testing.T) {
	repo := newGitRepo(t, "paging-changed")
	historyPagingImport(t, repo, historyPagingLinearStream(90, ""))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	step := 30
	h.app.session.logLimit = step
	h.app.session.Reload()
	h.drain()
	if got := len(h.app.session.Snapshot().Commits); got != step {
		t.Fatalf("first page = %d commits, want %d", got, step)
	}

	// Add one commit on top of the branch behind the app's back: no file
	// watch or refresh tick runs in this test, so the session still believes
	// in the old walk when Load More starts.
	repo.CommitFile("outside.txt", "outside\n", "commit outside the app")

	h.app.session.LoadMore(step)
	h.drain()
	// The dropped page schedules a full reload inside the same drain; run
	// its completion too.
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after fallback: %v", err)
	}
	loaded := len(h.app.session.Snapshot().Commits)
	// logLimit grew to 60 while the history holds 91 commits: a correct
	// fallback reload reads exactly 60 including the outside commit.
	if loaded != 60 {
		t.Fatalf("loaded commits = %d, want 60 after the fallback reload", loaded)
	}
	if h.app.session.Snapshot().Commits[0].Subject != "commit outside the app" {
		t.Fatalf("newest commit = %q, want the commit added outside the app", h.app.session.Snapshot().Commits[0].Subject)
	}
	assertMatchesFullLoad(t, h, 60, false)
}

// A detached HEAD moving without any reference: no tip changes, so only
// the seam overlap notices the walk no longer reaches the loaded tail. The
// page is dropped and the whole snapshot reloaded, as with a moved branch.
func TestHistoryPagingDetachedHeadMoveFallsBackToReload(t *testing.T) {
	repo := newGitRepo(t, "paging-detached")
	historyPagingImport(t, repo, historyPagingLinearStream(90, ""))

	// Detach HEAD at the tip and remove the branch, so the walk starts
	// from HEAD alone and its moves leave every reference untouched.
	repo.Git("checkout", "--detach", "HEAD")
	repo.Git("branch", "-D", "main")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	step := 30
	h.app.session.logLimit = step
	h.app.session.Reload()
	h.drain()
	if got := len(h.app.session.Snapshot().Commits); got != step {
		t.Fatalf("first page = %d commits, want %d", got, step)
	}
	before := h.app.session.Snapshot().Refs
	if len(before.Branches) != 0 {
		t.Fatalf("fixture still has branches: %+v", before.Branches)
	}

	// Move the detached HEAD thirty commits back behind the app's back.
	repo.Git("checkout", "-q", "--detach", "HEAD~30")

	h.app.session.LoadMore(step)
	h.drain()
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after fallback: %v", err)
	}
	// logLimit grew to 60 while the walk from the moved HEAD holds exactly
	// 60 commits: the fallback reload reads them all.
	loaded := len(h.app.session.Snapshot().Commits)
	if loaded != 60 {
		t.Fatalf("loaded commits = %d, want 60 after the fallback reload", loaded)
	}
	assertMatchesFullLoad(t, h, 60, false)
}

// A page fetch crossing a repository switch is discarded: the new
// repository's history must not have anything appended to it.
func TestHistoryPagingDiscardedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "paging-switch-a")
	historyPagingImport(t, repoA, historyPagingLinearStream(300, ""))
	repoB := newGitRepo(t, "paging-switch-b")
	repoB.CommitFile("b.txt", "b\n", "init b")

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	h.app.session.logLimit = 100
	h.app.session.Reload()
	h.drain()

	h.queue.PauseWorkers()
	h.app.session.LoadMore(100)
	worker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Switch repositories while the page fetch is held; the new repository's
	// initial load applies before the stale page finishes.
	h.openRepo(repoB.StoreRepo())
	h.queue.RunNext(t)
	h.queue.ReleaseWorker(worker)
	h.drain()

	if h.app.session.Meta().Name != "paging-switch-b" {
		t.Fatalf("active repo = %q, want paging-switch-b", h.app.session.Meta().Name)
	}
	snap := h.app.session.Snapshot()
	if len(snap.Commits) != 1 || snap.Commits[0].Subject != "init b" {
		t.Fatalf("repo B commits after switch = %d, want exactly [init b]; first subject %q",
			len(snap.Commits), snap.Commits[0].Subject)
	}
}

// A full reload finishing while a page fetch is in flight invalidates the
// page: its completion is dropped instead of overwriting the fresher
// snapshot. The reload here also flips the ordering to committer date, so a
// stale page applied on top would visibly mismatch the date-ordered walk.
func TestHistoryPagingDiscardedWhenReloadIntervenes(t *testing.T) {
	repo := newGitRepo(t, "paging-intervene")
	historyPagingImport(t, repo, historyPagingInterleavedStream(250, 10))

	// The fixture must genuinely order differently by date than by topology
	// within the window the test inspects, or it proves nothing.
	meta := store.Repo{Kind: store.Local, Path: repo.Path(), Name: "paging-intervene"}
	probe := defaultOpenGitRepo(meta)
	topo, err := probe.Log(git.LogOptions{Limit: 200})
	if err != nil {
		t.Fatalf("probe topo walk: %v", err)
	}
	dated, err := probe.Log(git.LogOptions{Limit: 200, DateOrder: true})
	if err != nil {
		t.Fatalf("probe date walk: %v", err)
	}
	if reflect.DeepEqual(topo, dated) {
		t.Fatal("fixture orders identically by topology and date in the first 200 commits")
	}

	h := newTestApp(t)
	h.openRepo(meta)
	h.drain()

	h.app.session.logLimit = 100
	h.app.session.Reload()
	h.drain()

	h.queue.PauseWorkers()
	h.app.session.LoadMore(100)
	worker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// The reload grows the generation at once and re-reads with the flipped
	// ordering; its completion must run before the stale page is released.
	// A full drain would deadlock on the still-held page worker, so only the
	// reload's completion runs here.
	h.app.session.SetDateOrder(true)
	h.queue.RunNext(t)
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertMatchesFullLoad(t, h, 200, true)
	if !h.app.session.DateOrder() {
		t.Fatal("date order did not stay enabled")
	}
}

// LoadMore during a busy write only records the larger log limit: no read
// starts, and the automatic reload after the write loads exactly that many.
func TestHistoryPagingDeferredWhileWriteBusy(t *testing.T) {
	repo := newGitRepo(t, "paging-busy")
	historyPagingImport(t, repo, historyPagingLinearStream(20, ""))

	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	s := newRepoSession(q.post, q.spawn, defaultOpenGitRepo)
	s.Open(repo.StoreRepo())
	q.Drain(t)

	s.logLimit = 5
	s.Reload()
	q.Drain(t)
	if got := len(s.Snapshot().Commits); got != 5 {
		t.Fatalf("first page = %d commits, want 5", got)
	}

	q.PauseWorkers()
	if ok := s.do("Write", func(r *git.Repo) error {
		return r.Run("branch", "busy-write")
	}); !ok {
		t.Fatal("write was rejected unexpectedly")
	}
	writeWorker := q.WaitWorker(t)
	q.ResumeWorkers()

	s.LoadMore(3)
	if s.Loading() {
		t.Fatal("LoadMore during a busy write started a read")
	}

	q.ReleaseWorker(writeWorker)
	q.Drain(t)

	if got := len(s.Snapshot().Commits); got != 8 {
		t.Fatalf("commits after write = %d, want 8 (log limit 5+3)", got)
	}
	want, wantGraph := fullLoadCommits(t, repo.StoreRepo(), 8, false)
	if !reflect.DeepEqual(s.Snapshot().Commits, want) {
		t.Fatalf("post-write commits differ from a full load: %s", firstCommitDiff(s.Snapshot().Commits, want))
	}
	if !reflect.DeepEqual(s.Snapshot().Graph, wantGraph) {
		t.Fatal("post-write graph differs from a full load")
	}
}

// Load More over a loopback SSH connection: two clicks page the remote
// history to exactly what one full read of the same number of commits
// returns, including the short final page at the end of the history.
func TestHistoryPagingLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// 1300 imported commits plus the initial one: the SSH first page holds
	// 1000, one full page of 500 ends exactly at the history's end — the
	// second click then walks over the seam into a one-commit page.
	stream := historyPagingLinearStream(1300, srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.RunShellInput(t,
		"cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet",
		[]byte(stream))
	srv.RunGit(t, repo.Path, "reset", "--hard", "HEAD")

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if got := len(h.app.session.Snapshot().Commits); got != defaultSSHLogLimit {
		t.Fatalf("SSH first page = %d commits, want %d", got, defaultSSHLogLimit)
	}

	for i := 0; i < 2; i++ {
		h.app.session.LoadMore(0)
		h.drain()
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after SSH paging: %v", err)
	}
	loaded := len(h.app.session.Snapshot().Commits)
	if loaded != 1301 {
		t.Fatalf("SSH loaded commits = %d, want all 1301", loaded)
	}
	if h.app.session.Snapshot().More {
		t.Fatal("More still true after the whole SSH history loaded")
	}
	assertMatchesFullLoad(t, h, 1301, false)
}

// Show in History pages the loaded history until the commit appears: with
// the target on the third page the reveal still finds, selects and details
// it through the paged loading.
func TestHistoryPagingSearchFindNextAcrossPages(t *testing.T) {
	repo := newGitRepo(t, "paging-reveal")
	// The target sits 1100 commits deep: past the first page of 500 and the
	// second page another 500 wide, on the third page of the walk.
	target := repo.CommitFile("needle.txt", "needle\n", "needle target commit")
	historyPagingImport(t, repo, historyPagingLinearStream(1100, target))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.app.session.logLimit = 500
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	for _, cm := range h.app.session.Snapshot().Commits {
		if cm.Hash == target {
			t.Fatal("target commit unexpectedly in the first page: the test proves nothing")
		}
	}

	openSearchView(t, h)
	submitSearchQuery(t, h, "needle target")
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != target {
		t.Fatalf("results = %q, want exactly the target commit", searchResultSubjects(st))
	}

	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v\ntexts=%q", err, h.ui.Texts())
	}
	// The reveal grows the history one page per frame: the target sits past
	// 1000 commits, so at least two Load More rounds must interleave with
	// the frames before the reveal completes.
	for i := 0; i < 12 && st.pendingRevealHash != ""; i++ {
		h.ui.Frame()
		h.drain()
	}
	h.ui.Frame()
	h.drain()

	if st.pendingRevealHash != "" {
		t.Fatalf("pending reveal not cleared: %q", st.pendingRevealHash)
	}
	loaded := len(h.app.session.Snapshot().Commits)
	if loaded < 1101 {
		t.Fatalf("loaded commits = %d, want the third page (1101+) holding the target", loaded)
	}
	found := false
	for _, cm := range h.app.session.Snapshot().Commits {
		if cm.Hash == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("target commit not in the paged history")
	}
	if hv := &h.app.historyView; hv.sel != target {
		t.Fatalf("history selection = %q, want the revealed target %q", hv.sel, target)
	}
	assertMatchesFullLoad(t, h, loaded, false)
}

// Paging git.Repo.Log with an explicit branch filter (Revs) and Skip using
// the same overlap join as loadMorePage produces no duplicate or missing
// commits, excludes commits reachable only from other branches, and matches a
// single-shot Log with the same Revs and ordering.
func TestHistoryPagingLogRevsWithSkip(t *testing.T) {
	repo := newGitRepo(t, "paging-revs")
	var b strings.Builder
	ts0 := 1700000000
	for i := 1; i <= 40; i++ {
		msg := fmt.Sprintf("main %d", i)
		content := fmt.Sprintf("m%d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts0+i*100)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts0+i*100)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline m.txt\ndata %d\n%s\n", len(content), content)
	}
	// feature forks from main commit 10, merges main commit 25 mid-way, and
	// interleaves timestamps with main so topo and date order differ.
	for j := 1; j <= 35; j++ {
		mark := 1000 + j
		msg := fmt.Sprintf("feature %d", j)
		content := fmt.Sprintf("f%d\n", j)
		fmt.Fprintf(&b, "commit refs/heads/feature\nmark :%d\n", mark)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts0+1050+j*130)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts0+1050+j*130)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if j == 1 {
			fmt.Fprintf(&b, "from :10\n")
		} else {
			fmt.Fprintf(&b, "from :%d\n", mark-1)
		}
		if j == 20 {
			fmt.Fprintf(&b, "merge :25\n")
		}
		fmt.Fprintf(&b, "M 100644 inline f.txt\ndata %d\n%s\n", len(content), content)
	}
	// other forks from main commit 5 and must never appear when Revs selects
	// feature or main+feature.
	for k := 1; k <= 15; k++ {
		mark := 2000 + k
		msg := fmt.Sprintf("other %d", k)
		content := fmt.Sprintf("o%d\n", k)
		fmt.Fprintf(&b, "commit refs/heads/other\nmark :%d\n", mark)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", ts0+5000+k*100)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", ts0+5000+k*100)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if k == 1 {
			fmt.Fprintf(&b, "from :5\n")
		} else {
			fmt.Fprintf(&b, "from :%d\n", mark-1)
		}
		fmt.Fprintf(&b, "M 100644 inline o.txt\ndata %d\n%s\n", len(content), content)
	}
	historyPagingImport(t, repo, b.String())

	r := defaultOpenGitRepo(repo.StoreRepo())
	refs, err := r.Refs()
	if err != nil {
		t.Fatalf("read refs: %v", err)
	}

	cases := []struct {
		name      string
		revs      []string
		dateOrder bool
		wantCount int
	}{
		{"single-branch-topo", []string{"refs/heads/feature"}, false, 60},
		{"single-branch-date", []string{"refs/heads/feature"}, true, 60},
		{"multi-branch-topo", []string{"refs/heads/main", "refs/heads/feature"}, false, 75},
		{"multi-branch-date", []string{"refs/heads/main", "refs/heads/feature"}, true, 75},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const step = 17
			base, err := r.Log(git.LogOptions{Limit: step, Revs: tc.revs, DateOrder: tc.dateOrder})
			if err != nil {
				t.Fatalf("first page: %v", err)
			}
			git.FixRefKinds(base, refs)

			for {
				n := len(base)
				page, err := r.Log(git.LogOptions{
					Limit:     step + 1,
					Skip:      n - 1,
					Revs:      tc.revs,
					DateOrder: tc.dateOrder,
				})
				if err != nil {
					t.Fatalf("page at skip %d: %v", n-1, err)
				}
				if len(page) == 0 || page[0].Hash != base[n-1].Hash {
					t.Fatalf("seam mismatch at skip %d: got %d commits", n-1, len(page))
				}
				git.FixRefKinds(page, refs)
				merged := make([]git.Commit, 0, n+len(page)-1)
				merged = append(merged, base...)
				merged = append(merged, page[1:]...)
				base = merged
				if len(page) < step+1 {
					break
				}
			}

			if len(base) != tc.wantCount {
				t.Fatalf("paged count = %d, want %d", len(base), tc.wantCount)
			}
			seen := make(map[string]bool, len(base))
			for i, cm := range base {
				if seen[cm.Hash] {
					t.Fatalf("duplicate commit %s at index %d", cm.Hash, i)
				}
				seen[cm.Hash] = true
				if strings.HasPrefix(cm.Subject, "other ") {
					t.Fatalf("excluded branch commit leaked into filtered walk: %q", cm.Subject)
				}
			}

			full, err := r.Log(git.LogOptions{Limit: tc.wantCount + 10, Revs: tc.revs, DateOrder: tc.dateOrder})
			if err != nil {
				t.Fatalf("full filtered log: %v", err)
			}
			git.FixRefKinds(full, refs)
			if !reflect.DeepEqual(base, full) {
				t.Fatalf("filtered paged commits differ from full log: %s", firstCommitDiff(base, full))
			}
			if !reflect.DeepEqual(git.Graph(base), git.Graph(full)) {
				t.Fatal("filtered paged graph differs from full log graph")
			}
		})
	}
}

// Load More and automatic refresh running concurrently discard stale results
// through loadGen: an in-flight auto-refresh poll started before Load More is
// ignored once Load More advances loadGen, and while Load More is loading an
// auto-refresh tick does not launch a second concurrent check.
func TestHistoryPagingAutoRefreshConcurrency(t *testing.T) {
	repo := newGitRepo(t, "paging-autorefresh")
	historyPagingImport(t, repo, historyPagingLinearStream(90, ""))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	step := 30
	h.app.session.logLimit = step
	h.app.session.Reload()
	h.drain()

	st := autoRefresh(h.app)
	// Start a passive auto-refresh poll while the session is idle and settled,
	// pausing its worker before it finishes.
	h.queue.PauseWorkers()
	startAutoRefreshCheck(h.app, st, false)
	pollWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Start Load More while the passive poll is still in flight. Load More
	// advances loadGen and marks the session loading.
	h.queue.PauseWorkers()
	h.app.session.LoadMore(step)
	pageWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// While Load More is in flight, autoRefreshCanCheck stays false so a tick
	// cannot start another poll.
	if autoRefreshCanCheck(h.app, st) {
		t.Fatal("autoRefreshCanCheck = true while Load More is in flight")
	}

	// Release the older poll first: its completion must be dropped by loadGen
	// mismatch without clearing Loading or interrupting Load More.
	h.queue.ReleaseWorker(pollWorker)
	h.queue.RunNext(t)
	if !h.app.session.Loading() {
		t.Fatal("stale auto-refresh poll cleared session.Loading while Load More was in flight")
	}

	// Now let Load More finish and verify the full 60-commit paged result.
	h.queue.ReleaseWorker(pageWorker)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after concurrent poll and Load More: %v", err)
	}
	if got := len(h.app.session.Snapshot().Commits); got != 60 {
		t.Fatalf("loaded commits = %d, want 60", got)
	}
	assertMatchesFullLoad(t, h, 60, false)
}
