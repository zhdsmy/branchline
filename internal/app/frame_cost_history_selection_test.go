//go:build !race

package app

import (
	"fmt"
	"strings"
	"testing"
)

// frameCostHistorySelectionBytesAssert is the byte guard of the two
// selected-commit views in this file, and of nothing else: frameCostAssert
// judges allocation counts only, and the per-frame byte growth of the
// History selection sync showed nowhere in them. The limit is the small
// measurement scaled by 1.5 plus a fixed slack of 4096 bytes a frame, so a
// cost flat in the loaded-commit count passes at any size while a cost
// proportional to it — a per-frame table of every loaded commit — fails.
func frameCostHistorySelectionBytesAssert(t *testing.T, view string, small, large frameCostSpec) {
	t.Helper()
	limit := small.bytes*3/2 + 4096
	if large.bytes > limit {
		t.Fatalf("%s large = %d B/frame grows past small %d×1.5+4096 = %d", view, large.bytes, small.bytes, limit)
	}
}

// frameCostHistorySelectionRepo builds the pagination fixture both
// selected-commit views share: n commits on main, of which the test pages
// loaded in through the Load More button. The two newest commits and their
// subjects come back for the selection clicks.
func frameCostHistorySelectionRepo(t *testing.T, scale string) (*gitRepoFixture, int, string, string, string, string) {
	t.Helper()
	n := 500
	loaded := 500
	if scale == "large" {
		n = 10500
		loaded = 10000
	}
	repo := frameCostLinearRepo(t, "hist-sel-"+scale, n)
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	prev := strings.TrimSpace(repo.Git("rev-parse", "HEAD~1"))
	return repo, loaded, head, fmt.Sprintf("commit %d", n), prev, fmt.Sprintf("commit %d", n-1)
}

// frameCostLoadHistoryPages paginates the open History page through its Load
// More button until loaded commits are in the snapshot.
func frameCostLoadHistoryPages(t *testing.T, h *testHarness, loaded int) {
	t.Helper()
	for len(h.app.session.Snapshot().Commits) < loaded {
		mustClick(t, h, "Load More")
		h.drain()
	}
	if got := len(h.app.session.Snapshot().Commits); got != loaded {
		t.Fatalf("fixture: loaded commits = %d, want %d", got, loaded)
	}
}

// The History page with one commit chosen: frames over 10000 loaded commits
// must cost no more per frame than over 500, in allocations and — this
// view's own guard — in bytes, and no frame may run a Git command.
func TestFrameCostHistorySelectedOne(t *testing.T) {
	small, large := frameCostPair(t, "HistorySelectedOne", func(t *testing.T, scale string) *testHarness {
		repo, loaded, head, headSubject, _, _ := frameCostHistorySelectionRepo(t, scale)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		frameCostLoadHistoryPages(t, h, loaded)
		clickCommitRow(t, h, head, headSubject)
		h.drain()
		if h.app.historyView.sel != head {
			t.Fatalf("fixture: sel = %q, want %q", h.app.historyView.sel, head)
		}
		if len(h.app.historyView.files) == 0 {
			t.Fatal("fixture: detail file list empty after selecting the tip commit")
		}
		return h
	})
	frameCostAssert(t, "HistorySelectedOne", small, large)
	frameCostHistorySelectionBytesAssert(t, "HistorySelectedOne", small, large)
}

// The History page with two commits chosen for comparison: frames over
// 10000 loaded commits must cost no more per frame than over 500, in
// allocations and — this view's own guard — in bytes, and no frame may run
// a Git command.
func TestFrameCostHistorySelectedTwo(t *testing.T) {
	small, large := frameCostPair(t, "HistorySelectedTwo", func(t *testing.T, scale string) *testHarness {
		repo, loaded, head, headSubject, prev, prevSubject := frameCostHistorySelectionRepo(t, scale)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		frameCostLoadHistoryPages(t, h, loaded)
		selectComparePair(t, h, head, headSubject, prev, prevSubject)
		hv := &h.app.historyView
		if hv.mode != historyCompare {
			t.Fatalf("fixture: mode = %d, want historyCompare (%d)", hv.mode, historyCompare)
		}
		if len(hv.compare.files) == 0 {
			t.Fatal("fixture: comparison file list empty after selecting two commits")
		}
		return h
	})
	frameCostAssert(t, "HistorySelectedTwo", small, large)
	frameCostHistorySelectionBytesAssert(t, "HistorySelectedTwo", small, large)
}
