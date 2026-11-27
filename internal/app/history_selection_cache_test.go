package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// historySelectionImportRepo builds a fast-import chain of n commits in one
// file, then one final commit through the porcelain so the working tree and
// index carry the tip — the parts git commit --amend needs later. Subjects
// are "<prefix> <i>" counting upward; the returned hash is the tip.
func historySelectionImportRepo(t *testing.T, repo *gitRepoFixture, prefix string, n int) string {
	t.Helper()
	fastImportFileCommits(t, repo, "f.txt", prefix, n-1)
	repo.WriteFile("f.txt", prefix+" final\n")
	repo.Git("add", "f.txt")
	repo.Git("commit", "-m", prefix+" "+strconv.Itoa(n))
	return strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
}

// Amending the chosen commit away externally and refreshing the snapshot
// through the UI's own Load More button prunes the rewritten hash from the
// selection: the detail pane clears instead of showing a commit the history
// no longer has. The snapshot slice the reload delivers is a new one, so the
// prune must notice it on the very first frame.
func TestHistorySelectionCacheAmendClearsDetail(t *testing.T) {
	repo := newGitRepo(t, "hsc-amend")
	head := historySelectionImportRepo(t, repo, "commit", 520)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "commit 520")
	h.drain()

	if h.app.historyView.sel != head {
		t.Fatalf("setup: sel = %q, want %q", h.app.historyView.sel, head)
	}
	if !h.ui.HasText("f.txt") {
		t.Fatalf("setup: missing file list; texts=%q", h.ui.Texts())
	}

	// Amend the tip away externally, as a user rewriting history in her
	// terminal would.
	repo.WriteFile("f.txt", "commit amended\n")
	repo.Git("add", "f.txt")
	repo.Git("commit", "--amend", "-m", "commit 520 amended")

	// Refresh the snapshot through the UI: Load More starts a fresh
	// snapshot read, and the pruned selection must react to it.
	mustClick(t, h, "Load More")
	h.drain()

	hv := &h.app.historyView
	if len(h.app.session.Snapshot().Commits) != 520 {
		t.Fatalf("commits after amend = %d, want 520", len(h.app.session.Snapshot().Commits))
	}
	if h.app.session.Snapshot().Commits[0].Hash == head {
		t.Fatal("amend did not replace the commit hash")
	}
	if hv.selection.Len() != 0 {
		t.Fatalf("selection keys after amend = %d, want none", hv.selection.Len())
	}
	if hv.sel != "" {
		t.Fatalf("selection not cleared after amend: %q", hv.sel)
	}
	if hv.listSel != -1 {
		t.Fatalf("list selection = %d, want -1", hv.listSel)
	}
	if len(hv.files) != 0 {
		t.Fatalf("files not cleared after amend: %+v", hv.files)
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff not cleared after amend: %+v", hv.diff.req)
	}
	if !h.ui.HasText("No commit selected") {
		t.Fatalf("missing empty-detail hint; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("commit 520 amended") {
		t.Fatalf("amended commit not listed; texts=%q", h.ui.Texts())
	}
}

// Two commits chosen for comparison keep comparing across a Load More
// refresh: the snapshot slice the reload delivers is new, but neither the
// pair nor its direction may change, and the comparison must not restart.
func TestHistorySelectionCacheCompareSurvivesLoadMore(t *testing.T) {
	repo := newGitRepo(t, "hsc-loadmore")
	historySelectionImportRepo(t, repo, "commit", 520)
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	prev := strings.TrimSpace(repo.Git("rev-parse", "HEAD~1"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, head, "commit 520", prev, "commit 519")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}
	if hv.compare.base.Hash != prev || hv.compare.target.Hash != head {
		t.Fatalf("setup direction = %s…%s, want base commit 519 target commit 520",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
	filesBefore := len(hv.compare.files)
	if filesBefore == 0 {
		t.Fatal("setup: comparison file list empty")
	}

	mustClick(t, h, "Load More")
	h.drain()

	if got := len(h.app.session.Snapshot().Commits); got != 520 {
		t.Fatalf("commits after Load More = %d, want all 520", got)
	}
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d after Load More, want compare", hv.mode)
	}
	if hv.compare.base.Hash != prev || hv.compare.target.Hash != head {
		t.Fatalf("direction after Load More = %s…%s, want unchanged base commit 519 target commit 520",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
	if hv.compare.loading {
		t.Fatal("comparison file list reloading after Load More: the comparison restarted")
	}
	if len(hv.compare.files) != filesBefore {
		t.Fatalf("compare files after Load More = %d, want %d", len(hv.compare.files), filesBefore)
	}
	if !h.ui.HasText("Comparing") {
		t.Fatalf("compare header missing after Load More; texts=%q", h.ui.Texts())
	}
}

// Clicking another commit while one is chosen moves the detail to it even
// though the selection count never changes — a plain click replaces the one
// chosen key with another inside the frame that processes it: the pane must
// follow the keys, not their number.
func TestHistorySelectionCacheSameCountKeyChange(t *testing.T) {
	repo := newGitRepo(t, "hsc-keychange")
	hashA := repo.CommitFile("a.txt", "alpha\n", "commit alpha")
	hashB := repo.CommitFile("b.txt", "beta\n", "commit beta")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, hashA, "commit alpha")
	h.drain()

	hv := &h.app.historyView
	if hv.sel != hashA || !strings.HasPrefix(hv.detail.Message, "commit alpha") {
		t.Fatalf("setup: sel = %q detail = %q, want commit alpha", hv.sel, hv.detail.Message)
	}

	// The plain click replaces alpha with beta inside the frame that
	// processes it: the key set changes at a constant count of one.
	clickCommitRow(t, h, hashB, "commit beta")
	h.ui.Frame()
	h.drain()

	if hv.selection.Len() != 1 || !hv.selection.Has(hashB) {
		t.Fatalf("selection = %d keys, want exactly beta", hv.selection.Len())
	}
	if hv.sel != hashB {
		t.Fatalf("sel = %q after clicking beta, want %q: the pane tracked only the selection count, not its keys", hv.sel, hashB)
	}
	if !strings.HasPrefix(hv.detail.Message, "commit beta") {
		t.Fatalf("detail = %q, want commit beta", hv.detail.Message)
	}
	if len(hv.files) != 1 || hv.files[0].Path != "b.txt" {
		t.Fatalf("file list = %+v, want exactly [b.txt]", hv.files)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(hashB, "commit beta")); !ok || !row.Selected {
		t.Fatalf("beta row access = %+v (found=%v), want Selected=true", row, ok)
	}
}
