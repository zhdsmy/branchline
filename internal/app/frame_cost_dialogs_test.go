//go:build !race

package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// frameCostIRRepo builds a chain of total commits on main, commit i writing
// ir.txt with unique content and the subject "ir commit <i>", and returns the
// hash of the oldest commit: the interactive rebase onto whose plan is every
// other commit.
func frameCostIRRepo(t *testing.T, name string, total int) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	frameCostStream(t, repo, total, func(i int) [][2]string {
		return [][2]string{{"ir.txt", fmt.Sprintf("ir content %d\n", i)}}
	}, func(i int) string {
		return fmt.Sprintf("ir commit %d", i)
	})
	// fast-import writes objects and refs only: check the tree out so the
	// working tree and the index match the tip, as a clone would leave them.
	repo.Git("reset", "--hard", "-q", "main")
	onto := strings.TrimSpace(repo.Git("rev-list", "--max-parents=0", "main"))
	return repo, onto
}

// revealOldestHistoryRow clicks the newest commit row, giving the commit
// list the keyboard focus, and presses End, which moves the choice to the
// oldest loaded commit and reveals its row: the commit row of onto can then
// be right-clicked through its accessibility label.
func revealOldestHistoryRow(t *testing.T, h *testHarness, onto string) {
	t.Helper()
	head := h.app.session.Snapshot().Status.Head
	if err := h.ui.Click(commitRowLabel(head, fmt.Sprintf("ir commit %d", len(h.app.session.Snapshot().Commits)))); err != nil {
		t.Fatalf("click newest commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnd)
	h.ui.Frame()
	label := commitRowLabel(onto, "ir commit 1")
	if err := h.ui.RightClick(label); err != nil {
		t.Fatalf("oldest commit row not revealed by End: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.CloseMenu()
}

// driveIRRows opens the interactive rebase form over a plan of rows commits
// and returns once the rows render, the state the measurement starts from.
func driveIRRows(t *testing.T, scale string) *testHarness {
	t.Helper()
	rows := 20
	if scale == "large" {
		rows = 1000
	}
	repo, onto := frameCostIRRepo(t, "ir-rows-"+scale, rows+1)
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	h.drain()
	for len(h.app.session.Snapshot().Commits) < rows+1 {
		mustClick(t, h, "Load More")
		h.drain()
	}
	revealOldestHistoryRow(t, h, onto)
	row := commitRowLabel(onto, "ir commit 1")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menuItem := "Interactively Rebase 'main' to Here…"
	if err := h.ui.ChooseMenuItem(menuItem); err != nil {
		t.Fatalf("choose %q: %v", menuItem, err)
	}
	h.ui.Frame()
	h.drain()
	st := interactiveRebaseUI(h.app)
	if len(st.rows) != rows {
		t.Fatalf("fixture: plan rows = %d, want %d", len(st.rows), rows)
	}
	return h
}

// frameCostConflictFile builds the lines of f.txt whose side and main edits
// of hunks distinct lines, separated by enough shared context, turn into one
// conflict block each when merged.
func frameCostConflictFile(prefix string, hunks int) string {
	var b strings.Builder
	b.WriteString("top\n")
	for h := 1; h <= hunks; h++ {
		fmt.Fprintf(&b, "%s-h%d-edit\n", prefix, h)
		for f := 1; f <= 8; f++ {
			fmt.Fprintf(&b, "h%d-filler%d\n", h, f)
		}
	}
	b.WriteString("tail\n")
	return b.String()
}

// frameCostConflictRepo merges side into main with hunks conflict blocks in
// f.txt: base carries one editable line per block, side and main rewrite it
// differently.
func frameCostConflictRepo(t *testing.T, name string, hunks int) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("f.txt", frameCostConflictFile("base", hunks), "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", frameCostConflictFile("side", hunks), "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", frameCostConflictFile("main", hunks), "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
	return repo
}

// driveConflictHunks opens the merge editor of a conflict with hunks hunks
// and returns with every block rendered.
func driveConflictHunks(t *testing.T, scale string) *testHarness {
	t.Helper()
	hunks := 5
	if scale == "large" {
		hunks = 500
	}
	repo := frameCostConflictRepo(t, "conflict-hunks-"+scale, hunks)
	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	if got := len(conflictEditor(h.app).inspection.Hunks); got != hunks {
		t.Fatalf("fixture: inspection parsed %d hunks, want %d", got, hunks)
	}
	return h
}

// frameCostPreviewRepo merges side into main with one conflict block at the
// top of a lines-line file, so both overview previews carry nearly lines
// lines of text.
func frameCostPreviewRepo(t *testing.T, name string, lines int) *gitRepoFixture {
	t.Helper()
	write := func(edit string) string {
		var b strings.Builder
		b.WriteString(edit + "\n")
		for i := 2; i <= lines; i++ {
			fmt.Fprintf(&b, "shared line %d\n", i)
		}
		return b.String()
	}
	repo := newGitRepo(t, name)
	repo.CommitFile("f.txt", write("base edit"), "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", write("side edit"), "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", write("main edit"), "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
	return repo
}

// driveConflictPreview opens the overview of one conflicted file of lines
// lines, both full versions shown side by side.
func driveConflictPreview(t *testing.T, scale string) *testHarness {
	t.Helper()
	lines := 200
	if scale == "large" {
		lines = 20000
	}
	repo := frameCostPreviewRepo(t, "conflict-preview-"+scale, lines)
	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	st := conflictEditor(h.app)
	if len(st.inspection.OursText) == 0 || len(st.inspection.TheirsText) == 0 {
		t.Fatalf("fixture: previews empty: ours %d bytes theirs %d bytes",
			len(st.inspection.OursText), len(st.inspection.TheirsText))
	}
	return h
}

// The commit rows of the interactive rebase form: frames over a plan of 1000
// commits must cost no more per frame than one of 20, and no frame may run a
// Git command. The form is modal, so the frames measured paint nothing but
// the window behind and the form.
func TestFrameCostInteractiveRebaseRows(t *testing.T) {
	small, large := frameCostPair(t, "InteractiveRebaseRows", driveIRRows)
	frameCostAssert(t, "InteractiveRebaseRows", small, large)
}

// The hunk blocks of the merge editor: frames over 500 conflict blocks must
// cost no more per frame than over 5, and no frame may run a Git command.
//
// The measurement separates the list from the header's progress scan: the
// scan parses the whole draft text for its count in every frame, so in the
// as-loaded state — every block still marked — the frame's cost grows with
// the file however the list draws. The pair measured for the report logs
// that state; the guard runs over the editor with every block's ours side
// chosen, a real editing state where the draft carries no markers and the
// scan leaves early, isolating the list the virtualization covers.
func TestFrameCostConflictHunks(t *testing.T) {
	asLoadedSmall, asLoadedLarge := frameCostPair(t, "ConflictHunksAsLoaded", driveConflictHunks)
	frameCostReport(t, "ConflictHunksAsLoaded", asLoadedSmall, asLoadedLarge)
	resolvedSmall, resolvedLarge := frameCostPair(t, "ConflictHunks", func(t *testing.T, scale string) *testHarness {
		h := driveConflictHunks(t, scale)
		mustClick(t, h, "All Ours")
		h.ui.Frame()
		if git.HasConflictMarkers(conflictEditor(h.app).draft) {
			t.Fatal("fixture: draft still marked after All Ours")
		}
		return h
	})
	frameCostAssert(t, "ConflictHunks", resolvedSmall, resolvedLarge)
}

// The two side previews of the overview, measured only: they render the
// conflicted file's full text once per side, so their cost follows the file
// they show rather than the rows in view.
func TestFrameCostConflictSidePreviews(t *testing.T) {
	frameCostPair(t, "ConflictSidePreviews", driveConflictPreview)
}
