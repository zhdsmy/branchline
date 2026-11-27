//go:build !race

package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// frameCostHistoryTreeRepo builds a repository whose second commit changes
// n files spread over folders two levels deep in one fast-import stream, so
// the tree groups them: leaf ff of folder dd holds the files
// tree-dd/leaf-ff/file-0000.txt onward. It returns the fixture and the hash
// of the big commit.
func frameCostHistoryTreeRepo(t *testing.T, name string, n int) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	width := 10
	frameCostStream(t, repo, 2, func(i int) [][2]string {
		if i == 1 {
			return [][2]string{{"seed.txt", "seed\n"}}
		}
		files := make([][2]string, 0, n)
		for j := 0; j < n; j++ {
			outer := j / (width * width)
			inner := (j / width) % width
			files = append(files, [2]string{
				fmt.Sprintf("tree-%02d/leaf-%02d/file-%04d.txt", outer, inner, j),
				fmt.Sprintf("content %d\n", j),
			})
		}
		return files
	}, func(i int) string {
		return fmt.Sprintf("commit %d", i)
	})
	hash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	return repo, hash
}

// frameCostHistoryTreeHarness drives the changed-file tree of the big
// commit over n files and scrolls the list to its middle, the steady state
// the measurement starts from.
func frameCostHistoryTreeHarness(t *testing.T, scale string, small, large int) *testHarness {
	t.Helper()
	n := small
	if scale == "large" {
		n = large
	}
	repo, hash := frameCostHistoryTreeRepo(t, "hist-tree-"+scale, n)
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, hash, "commit 2")
	h.drain()
	if got := len(h.app.historyView.files); got != n {
		t.Fatalf("fixture: files = %d, want %d", got, n)
	}
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	rows := len(h.app.historyView.treeRows.rows)
	if rows < n {
		t.Fatalf("fixture: rows = %d, want at least the %d files", rows, n)
	}
	h.app.historyView.fileList.ScrollTo(rows/2, ui.Center)
	h.ui.Frame()
	h.ui.Frame()
	if first, _ := h.app.historyView.fileList.Visible(); first == 0 {
		t.Fatal("fixture: the list did not scroll to its middle")
	}
	return h
}

// The tree of a commit's changed files builds once when the commit, the
// view or a collapse choice changes — never per frame: drawing frames over
// the middle of a 5000-file tree must cost no more in allocations, bytes
// and time than over a 500-file one, and no frame may run a Git command.
func TestFrameCostHistoryTree(t *testing.T) {
	small, large := frameCostPair(t, "HistoryTree", func(t *testing.T, scale string) *testHarness {
		return frameCostHistoryTreeHarness(t, scale, 500, 5000)
	})
	frameCostAssert(t, "HistoryTree", small, large)
	if large.bytes > small.bytes*3/2+4096 {
		t.Fatalf("HistoryTree large = %d B/frame grows past small %d×1.5+4096 = %d", large.bytes, small.bytes, small.bytes*3/2+4096)
	}
}
