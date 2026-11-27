//go:build !race

package app

import (
	"fmt"
	"strings"
	"testing"
)

// frameCostFilesRepo builds two commits on main: a seed commit and one
// changing files files at once, so the commit's detail, the comparison of
// the pair and a search for the commit's subject all list the same files
// changed files. It returns the fixture and the hashes of the seed and the
// big commit.
func frameCostFilesRepo(t *testing.T, name string, files int) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	frameCostStream(t, repo, 2, func(i int) [][2]string {
		if i == 1 {
			return [][2]string{{"seed.txt", "seed\n"}}
		}
		out := make([][2]string, 0, files)
		for j := 0; j < files; j++ {
			out = append(out, [2]string{fmt.Sprintf("dir/f%05d.txt", j), fmt.Sprintf("file %d\n", j)})
		}
		return out
	}, func(i int) string {
		return fmt.Sprintf("files-change %d", i)
	})
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	base := strings.TrimSpace(repo.Git("rev-parse", "HEAD^"))
	return repo, base, head
}

// The changed-file list of the History commit detail is a virtualized
// ui.List: drawing frames over a commit changing 2000 files must cost no
// more than over one changing 20, and no frame may run a Git command.
func TestFrameCostHistoryFiles(t *testing.T) {
	small, large := frameCostPair(t, "HistoryFiles", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo, _, head := frameCostFilesRepo(t, "hist-files-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		clickCommitRow(t, h, head, "files-change 2")
		h.drain()
		if got := len(h.app.historyView.files); got != n {
			t.Fatalf("fixture: files = %d, want %d", got, n)
		}
		return h
	})
	frameCostAssert(t, "HistoryFiles", small, large)
}

// The comparison's changed-file list is the same virtual list: comparing
// the seed with the big commit lists its files changed files, and frames
// must not grow with their number.
func TestFrameCostCompareFiles(t *testing.T) {
	small, large := frameCostPair(t, "CompareFiles", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo, base, head := frameCostFilesRepo(t, "cmp-files-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		selectComparePair(t, h, head, "files-change 2", base, "files-change 1")
		if got := len(h.app.historyView.compare.files); got != n {
			t.Fatalf("fixture: compare files = %d, want %d", got, n)
		}
		return h
	})
	frameCostAssert(t, "CompareFiles", small, large)
}

// The search detail's changed-file list is the same virtual list: the big
// commit is the newest match of its own subject, and frames over its files
// must not grow with their number.
func TestFrameCostSearchFiles(t *testing.T) {
	small, large := frameCostPair(t, "SearchFiles", func(t *testing.T, scale string) *testHarness {
		n := 20
		if scale == "large" {
			n = 2000
		}
		repo, _, head := frameCostFilesRepo(t, "search-files-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		openSearchView(t, h)
		submitSearchQuery(t, h, "files-change")
		st := searchState(h)
		if st.selHash != head {
			t.Fatalf("fixture: selected result = %q, want the big commit %q", st.selHash, head)
		}
		if got := len(st.files); got != n {
			t.Fatalf("fixture: search files = %d, want %d", got, n)
		}
		return h
	})
	frameCostAssert(t, "SearchFiles", small, large)
}
