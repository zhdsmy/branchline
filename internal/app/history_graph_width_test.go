package app

import (
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// The width of an empty graph is one, as the scan computes it: the graph
// column keeps room for the dot of a lane-less row.
func TestHistoryGraphWidthEmptyGraph(t *testing.T) {
	var c graphWidthCache
	if got := historyGraphWidth(&c, nil); got != 1 {
		t.Fatalf("width of nil rows = %d, want 1", got)
	}
	if got := historyGraphWidth(&c, []git.GraphRow{}); got != 1 {
		t.Fatalf("width of empty rows = %d, want 1", got)
	}
}

// Asking the width of the very slice a scan already answered for returns the
// cached answer without scanning: a row rewritten in place to a wider lane
// count still yields the original width. A reslice of the same backing array
// at the same length counts as the same slice.
func TestHistoryGraphWidthSameSliceScansOnce(t *testing.T) {
	rows := []git.GraphRow{{Lane: 0, Width: 3}, {Lane: 1, Width: 5}, {Lane: 0, Width: 2}}
	var c graphWidthCache
	if got := historyGraphWidth(&c, rows); got != 5 {
		t.Fatalf("first width = %d, want 5", got)
	}
	rows[1].Width = 9
	if got := historyGraphWidth(&c, rows); got != 5 {
		t.Fatalf("second width of the same slice = %d, want the cached 5", got)
	}
	if got := historyGraphWidth(&c, rows[:3]); got != 5 {
		t.Fatalf("width of a reslice of the same backing array = %d, want the cached 5", got)
	}
}

// A different rows slice is a miss even at the same length and contents:
// the cache never answers for a slice it did not scan, so a new snapshot
// load always computes its own width. A slice of the same backing array at
// a different length is a miss too.
func TestHistoryGraphWidthNewSliceRescans(t *testing.T) {
	first := []git.GraphRow{{Lane: 0, Width: 3}, {Lane: 1, Width: 5}}
	second := []git.GraphRow{{Lane: 0, Width: 3}, {Lane: 1, Width: 5}}
	var c graphWidthCache
	if got := historyGraphWidth(&c, first); got != 5 {
		t.Fatalf("first width = %d, want 5", got)
	}
	second[1].Width = 7
	if got := historyGraphWidth(&c, second); got != 7 {
		t.Fatalf("width of a new slice = %d, want the rescanned 7", got)
	}

	backing := make([]git.GraphRow, 3)
	backing[0] = git.GraphRow{Lane: 0, Width: 3}
	backing[1] = git.GraphRow{Lane: 1, Width: 4}
	backing[2] = git.GraphRow{Lane: 2, Width: 8}
	var c2 graphWidthCache
	if got := historyGraphWidth(&c2, backing[:2]); got != 4 {
		t.Fatalf("width of the shorter slice = %d, want 4", got)
	}
	if got := historyGraphWidth(&c2, backing[:3]); got != 8 {
		t.Fatalf("width of the same backing array at a longer length = %d, want the rescanned 8", got)
	}
}

// Rendering the History commit list fills the cache with the snapshot's own
// graph slice: the frame path looks the width up through the cache rather
// than scanning on its own.
func TestHistoryGraphWidthCacheUsedByFrame(t *testing.T) {
	repo := newGitRepo(t, "hist-width-cache")
	repo.CommitFile("a.txt", "one\n", "first commit")
	repo.CommitFile("b.txt", "two\n", "second commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	h.drain()

	cache := &h.app.historyView.graphWidth
	if !cache.valid {
		t.Fatal("graph width cache empty after rendering the history page")
	}
	if !sameBackingSlice(cache.src, h.app.session.Snapshot().Graph) {
		t.Fatal("graph width cache keyed on a slice other than the snapshot's graph rows")
	}
}
