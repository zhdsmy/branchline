//go:build !race

package app

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
)

// frameCostSpec is one measured size of one view: small and large of every
// view run through the same measurement so their costs compare directly.
type frameCostSpec struct {
	allocs  float64
	bytes   uint64
	ms      float64
	gitCmds int
}

// frameCostStream assembles one fast-import stream of n commits on
// refs/heads/main, chaining each commit on the previous one. files returns
// the paths and contents commit i writes, subject its message. One stream
// builds thousands of commits in a fraction of a second, where one git
// commit per shell-out would take minutes.
func frameCostStream(t *testing.T, repo *gitRepoFixture, n int, files func(i int) [][2]string, subject func(i int) string) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		msg := subject(i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		for _, f := range files(i) {
			fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", f[0], len(f[1]), f[1])
		}
	}
	frameCostImport(t, repo, b.String())
}

// frameCostImport feeds stream to git fast-import in the fixture's
// repository.
func frameCostImport(t *testing.T, repo *gitRepoFixture, stream string) {
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

// frameCostMeasure renders frames at the steady state the caller drove the
// view into and reports the per-frame cost: allocation count, allocated
// bytes, wall time, and the number of Git commands 20 frames started. The
// pointer is parked over a corner of the window background first so no row
// stays hovered.
func frameCostMeasure(t *testing.T, h *testHarness) frameCostSpec {
	t.Helper()
	h.ui.Move(600, 780)
	settleFrame(h.ui)
	h.drain()

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = counting.real
	})
	settleFrame(h.ui)

	const frames = 20
	allocs := testing.AllocsPerRun(frames, func() { h.ui.Frame() })

	h.ui.Frame()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := 0; i < frames; i++ {
		h.ui.Frame()
	}
	ms := float64(time.Since(start).Microseconds()) / 1000 / frames
	runtime.ReadMemStats(&after)

	// A read started inside the measurement window can still be in flight
	// when it ends: drain whatever it posted so its commands are counted
	// too, then keep the total.
	h.queue.Drain(t)

	return frameCostSpec{
		allocs:  allocs,
		bytes:   (after.TotalAlloc - before.TotalAlloc) / frames,
		ms:      ms,
		gitCmds: counting.reads,
	}
}

// frameCostReport logs one measurement row in a uniform format.
func frameCostReport(t *testing.T, view string, small, large frameCostSpec) {
	t.Helper()
	t.Logf("%s | small %.0f allocs/frame %d B/frame %.2f ms/frame | large %.0f allocs/frame %d B/frame %.2f ms/frame | git commands %d/%d",
		view,
		small.allocs, small.bytes, small.ms,
		large.allocs, large.bytes, large.ms,
		small.gitCmds, large.gitCmds)
}

// frameCostAssert fails the test when the large measurement grows beyond
// small×1.5+200 allocations a frame, or when either size ran a Git command
// in its measured frames.
func frameCostAssert(t *testing.T, view string, small, large frameCostSpec) {
	t.Helper()
	if small.gitCmds != 0 || large.gitCmds != 0 {
		t.Fatalf("%s ran git commands in measured frames: small %d, large %d", view, small.gitCmds, large.gitCmds)
	}
	limit := small.allocs*1.5 + 200
	if large.allocs > limit {
		t.Fatalf("%s large = %.0f allocs/frame grows past small %.0f×1.5+200 = %.0f", view, large.allocs, small.allocs, limit)
	}
}

// frameCostLinearRepo builds a chain of n commits on main, commit i writing
// one file and carrying the subject "commit <i>".
func frameCostLinearRepo(t *testing.T, name string, n int) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	frameCostStream(t, repo, n, func(i int) [][2]string {
		return [][2]string{{"f.txt", fmt.Sprintf("content %d\n", i)}}
	}, func(i int) string {
		return fmt.Sprintf("commit %d", i)
	})
	return repo
}

// frameCostPair measures one view at its small and large sizes, the driving
// of each size done by the caller, and logs the row.
func frameCostPair(t *testing.T, view string, drive func(t *testing.T, scale string) *testHarness) (small, large frameCostSpec) {
	t.Helper()
	hs := drive(t, "small")
	small = frameCostMeasure(t, hs)
	hl := drive(t, "large")
	large = frameCostMeasure(t, hl)
	frameCostReport(t, view, small, large)
	return small, large
}

// The commit list of the History page is a virtualized ui.List: drawing
// frames over the first page must not cost more when the repository is
// 10000 commits deep than when it is 50, and no frame may run a Git
// command.
func TestFrameCostHistoryList(t *testing.T) {
	small, large := frameCostPair(t, "HistoryList", func(t *testing.T, scale string) *testHarness {
		n := 50
		if scale == "large" {
			n = 10000
		}
		repo := frameCostLinearRepo(t, "hist-list-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		return h
	})
	frameCostAssert(t, "HistoryList", small, large)
}

// frameCostDiffRepo builds two commits rewriting every one of lines lines
// of big.txt, so the diff of the second commit against the first carries
// lines changed lines.
func frameCostDiffRepo(t *testing.T, name string, lines int) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	frameCostStream(t, repo, 2, func(i int) [][2]string {
		var b strings.Builder
		for j := 1; j <= lines; j++ {
			fmt.Fprintf(&b, "version%d line %d\n", i, j)
		}
		return [][2]string{{"big.txt", b.String()}}
	}, func(i int) string {
		return fmt.Sprintf("write %d", i)
	})
	hash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	return repo, hash
}

// The diff panel in Split layout draws frames over a file whose every line
// changed; its cost must not grow with the line count of the diff.
func TestFrameCostDiffSplit(t *testing.T) {
	small, large := frameCostPair(t, "DiffSplit", func(t *testing.T, scale string) *testHarness {
		n := 200
		if scale == "large" {
			n = 20000
		}
		repo, hash := frameCostDiffRepo(t, "diff-split-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		clickCommitRow(t, h, hash, "write 2")
		h.drain()
		if d := &h.app.historyView.diff; d.state == nil || d.added != n || d.deleted != n {
			t.Fatalf("fixture: diff state = %+v added=%d deleted=%d, want %d/%d loaded", d.err, d.added, d.deleted, n, n)
		}
		return h
	})
	frameCostAssert(t, "DiffSplit", small, large)
}

// The diff panel in Unified layout under the same diff.
func TestFrameCostDiffUnified(t *testing.T) {
	small, large := frameCostPair(t, "DiffUnified", func(t *testing.T, scale string) *testHarness {
		n := 200
		if scale == "large" {
			n = 20000
		}
		repo, hash := frameCostDiffRepo(t, "diff-unified-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		clickCommitRow(t, h, hash, "write 2")
		h.drain()
		if err := h.ui.Click("Unified"); err != nil {
			t.Fatalf("click Unified: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.drain()
		if h.app.historyView.diff.Layout() != editor.DiffUnified {
			t.Fatal("fixture: Unified layout did not engage")
		}
		if d := &h.app.historyView.diff; d.state == nil || d.added != n || d.deleted != n {
			t.Fatalf("fixture: diff state = %+v added=%d deleted=%d, want %d/%d loaded", d.err, d.added, d.deleted, n, n)
		}
		return h
	})
	frameCostAssert(t, "DiffUnified", small, large)
}

// The commit search results list is a virtualized ui.List; with 2000
// results paged in through More Results, frames must cost no more than
// with 20.
func TestFrameCostSearchResults(t *testing.T) {
	small, large := frameCostPair(t, "SearchResults", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo := frameCostLinearRepo(t, "search-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		openSearchView(t, h)
		submitSearchQuery(t, h, "commit")
		// Every commit matches. The default page is 500 results; large
		// needs three More Results rounds to reach 2000.
		if scale == "large" {
			for i := 0; i < 3; i++ {
				mustClick(t, h, "More Results")
				h.drain()
			}
		}
		if got := len(searchState(h).results); got != n {
			t.Fatalf("fixture: search results = %d, want %d", got, n)
		}
		return h
	})
	frameCostAssert(t, "SearchResults", small, large)
}
