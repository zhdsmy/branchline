//go:build !race

package app

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

// frameCostTreeRepo builds a repository of n untracked files spread over
// folders two levels deep, so the tree groups them: leaf ff of folder dd
// holds the files tree-dd/ff-0000.txt onward.
func frameCostTreeRepo(t *testing.T, name string, n int) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "initial")
	width := 10
	for i := 0; i < n; i++ {
		outer := i / (width * width)
		inner := (i / width) % width
		repo.WriteFile(fmt.Sprintf("tree-%02d/leaf-%02d/file-%04d.txt", outer, inner, i), fmt.Sprintf("content %d\n", i))
	}
	return repo
}

// frameCostTreeHarness drives a tree-mode Local Changes page over n
// untracked files and scrolls the list to its middle, the steady state the
// measurement starts from.
func frameCostTreeHarness(t *testing.T, scale string, small, large int) *testHarness {
	t.Helper()
	n := small
	if scale == "large" {
		n = large
	}
	repo := frameCostTreeRepo(t, "changes-tree-"+scale, n)
	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	rows := len(h.app.changesView.listCache.rows)
	if rows < n {
		t.Fatalf("fixture: rows = %d, want at least the %d files", rows, n)
	}
	if h.app.changesView.listCache.filter != "" || !h.app.changesView.treeMode {
		t.Fatal("fixture: tree mode did not engage")
	}
	h.app.changesView.list.ScrollTo(rows/2, ui.Center)
	h.ui.Frame()
	h.ui.Frame()
	if first, _ := h.app.changesView.list.Visible(); first == 0 {
		t.Fatal("fixture: the list did not scroll to its middle")
	}
	return h
}

// The tree of Local Changes builds once when the snapshot, the filter, the
// view mode or a collapse choice changes — never per frame: drawing frames
// over the middle of a 10000-file tree must cost no more in allocations,
// bytes and time than over a 500-file one, and no frame may run a Git
// command.
func TestFrameCostChangesTree(t *testing.T) {
	small, large := frameCostPair(t, "ChangesTree", func(t *testing.T, scale string) *testHarness {
		return frameCostTreeHarness(t, scale, 500, 10000)
	})
	frameCostAssert(t, "ChangesTree", small, large)
	if large.bytes > small.bytes*3/2+4096 {
		t.Fatalf("ChangesTree large = %d B/frame grows past small %d×1.5+4096 = %d", large.bytes, small.bytes, small.bytes*3/2+4096)
	}
}
