//go:build !race

package app

import (
	"testing"
)

// frameCostChangesRepo builds a repository of n untracked files, all of them
// unstaged, or staged when staged is set.
func frameCostChangesRepo(t *testing.T, name string, n int, staged bool) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "initial")
	writeUnstagedFiles(t, repo, n)
	if staged {
		repo.Git("add", "-A")
	}
	return repo
}

// frameCostSelectChange opens the Local Changes page on repo and selects one
// row, so the measured frames walk the whole selection path of the page.
func frameCostSelectChange(t *testing.T, h *testHarness, repo *gitRepoFixture, label string) {
	t.Helper()
	h.openRepo(repo.StoreRepo())
	h.drain()
	if h.app.route.page != pageChanges {
		t.Fatalf("initial page = %v, want pageChanges", h.app.route.page)
	}
	mustClick(t, h, label)
	h.drain()
	if !h.app.changesView.selected {
		t.Fatalf("fixture: clicking %q did not select a row", label)
	}
}

// The file list of Local Changes is a virtual list: with 2000 unstaged
// files, drawing a frame with a row selected must cost no more than with 20,
// and no frame may run a Git command.
func TestFrameCostChangesUnstaged(t *testing.T) {
	small, large := frameCostPair(t, "ChangesUnstaged", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo := frameCostChangesRepo(t, "changes-unstaged-"+scale, n, false)
		h := newTestApp(t)
		frameCostSelectChange(t, h, repo, "Unstaged file-0000.txt")
		return h
	})
	frameCostAssert(t, "ChangesUnstaged", small, large)
}

// The same guard for a large staged section.
func TestFrameCostChangesStaged(t *testing.T) {
	small, large := frameCostPair(t, "ChangesStaged", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo := frameCostChangesRepo(t, "changes-staged-"+scale, n, true)
		h := newTestApp(t)
		frameCostSelectChange(t, h, repo, "Staged file-0000.txt")
		return h
	})
	frameCostAssert(t, "ChangesStaged", small, large)
}

// The same guard for a merge in progress: the Conflicted section splits off
// the unstaged rows, and the selected row resolves through the conflict
// lookups instead of a scan.
func TestFrameCostChangesConflicted(t *testing.T) {
	small, large := frameCostPair(t, "ChangesConflicted", func(t *testing.T, scale string) *testHarness {
		n := 5
		if scale == "large" {
			n = 500
		}
		repo := newGitRepo(t, "changes-conflict-"+scale)
		mergeConflictingFiles(t, repo, n)
		h := newTestApp(t)
		frameCostSelectChange(t, h, repo, "Conflicted c-0000.txt")
		return h
	})
	frameCostAssert(t, "ChangesConflicted", small, large)
}
