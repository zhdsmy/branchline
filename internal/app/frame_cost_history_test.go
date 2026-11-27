//go:build !race

package app

import (
	"fmt"
	"strings"
	"testing"
)

// The History commit list after Load More pagination: frames over a history
// of 500 loaded commits must cost no more per frame than one of 10000, and
// no frame may run a Git command.
func TestFrameCostHistoryLoadMore(t *testing.T) {
	small, large := frameCostPair(t, "HistoryLoadMore", func(t *testing.T, scale string) *testHarness {
		n := 500
		loaded := 500
		if scale == "large" {
			n = 10500
			loaded = 10000
		}
		repo := frameCostLinearRepo(t, "hist-more-"+scale, n)
		h := newTestApp(t)
		openHistoryPage(t, h, repo.StoreRepo())
		h.drain()
		// Every click grows the loaded history by the 500-commit step of the
		// Load More button, as the user paginates deeper into the walk.
		for len(h.app.session.Snapshot().Commits) < loaded {
			mustClick(t, h, "Load More")
			h.drain()
		}
		if got := len(h.app.session.Snapshot().Commits); got != loaded {
			t.Fatalf("fixture: loaded commits = %d, want %d", got, loaded)
		}
		return h
	})
	frameCostAssert(t, "HistoryLoadMore", small, large)
}

// The File History commit list of one file: frames over a file 50 commits
// changed must cost no more per frame than over one 5000 commits changed,
// the larger list paged in through Load More File History.
func TestFrameCostFileHistory(t *testing.T) {
	small, large := frameCostPair(t, "FileHistory", func(t *testing.T, scale string) *testHarness {
		n := 50
		if scale == "large" {
			n = 5000
		}
		repo := newGitRepo(t, "filehist-"+scale)
		fastImportFileCommits(t, repo, "f.txt", "imp", n)
		// fast-import updates refs without the index; reset it so the working
		// tree modification reads as an unstaged change.
		repo.Git("reset")
		repo.WriteFile("f.txt", fmt.Sprintf("imp content %d\ndirty\n", n))

		h := openChangesHarness(t, repo)
		chooseFileMenuItem(t, h, "Unstaged f.txt", "File History…")
		h.drain()

		st := h.app.fileInspectorUI()
		for len(st.histCommits) < n {
			mustClick(t, h, "Load More File History")
			h.drain()
		}
		if len(st.histCommits) != n {
			t.Fatalf("fixture: file history commits = %d, want %d", len(st.histCommits), n)
		}
		return h
	})
	frameCostAssert(t, "FileHistory", small, large)
}

// The Blame line list: frames over a 200-line file must cost no more per
// frame than over a 20000-line one. Five commits write scattered stretches
// of the lines so the blame carries real runs rather than one commit.
func TestFrameCostBlame(t *testing.T) {
	small, large := frameCostPair(t, "Blame", func(t *testing.T, scale string) *testHarness {
		lines := 200
		if scale == "large" {
			lines = 20000
		}
		repo := newGitRepo(t, "blame-"+scale)
		frameCostStream(t, repo, 5, func(i int) [][2]string {
			var b strings.Builder
			for j := 1; j <= lines; j++ {
				if j%5 == i%5 {
					fmt.Fprintf(&b, "version%d line %d\n", i, j)
				} else {
					fmt.Fprintf(&b, "line %d\n", j)
				}
			}
			return [][2]string{{"b.txt", b.String()}}
		}, func(i int) string {
			return fmt.Sprintf("write %d", i)
		})
		repo.Git("reset")
		content := repo.Git("show", "HEAD:b.txt")
		repo.WriteFile("b.txt", content+"dirty\n")

		h := openChangesHarness(t, repo)
		chooseFileMenuItem(t, h, "Unstaged b.txt", "Blame…")
		h.drain()

		st := h.app.fileInspectorUI()
		if len(st.blameLines) != lines {
			t.Fatalf("fixture: blame lines = %d, want %d", len(st.blameLines), lines)
		}
		return h
	})
	frameCostAssert(t, "Blame", small, large)
}
