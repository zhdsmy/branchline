//go:build !race

package app

import (
	"testing"
)

// The filtered file list must cost no more per frame than the filter box
// and the visible rows: the derivation is built once when the filter text
// or the snapshot changes, never per frame, however many files it hides
// or keeps. Frames drawn with the filter active select a row first, so
// the selection path and the constant-time filtered-selection check run
// inside the measurement too.
func TestFrameCostChangesFilter(t *testing.T) {
	small, large := frameCostPair(t, "ChangesFilter", func(t *testing.T, scale string) *testHarness {
		n := 5
		if scale == "large" {
			n = 5000
		}
		repo := frameCostChangesRepo(t, "changes-filter-"+scale, n, false)
		h := newTestApp(t)
		frameCostSelectChange(t, h, repo, "Unstaged file-0000.txt")
		typeChangesFilter(t, h, "file-")
		if got := len(h.app.changesView.listCache.rows); got != n+4 {
			t.Fatalf("fixture: filtered rows = %d, want %d files plus the section rows", got, n)
		}
		if !h.app.changesView.selected {
			t.Fatal("fixture: the selected file must survive a filter it matches")
		}
		return h
	})
	frameCostAssert(t, "ChangesFilter", small, large)
}
