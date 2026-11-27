package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

// compareFixture is a three-commit chain made for comparison tests: c1 seeds
// alpha.txt, c2 grows it, c3 shrinks it back and adds beta.txt. Comparing c1
// with c2 changes alpha.txt alone; comparing c2 with c3 changes both files.
type compareFixture struct {
	c1, c2, c3 string
}

// newCompareRepo builds the three-commit compare fixture.
func newCompareRepo(t *testing.T, name string) (*gitRepoFixture, compareFixture) {
	t.Helper()
	repo := newGitRepo(t, name)
	var f compareFixture
	f.c1 = repo.CommitFile("alpha.txt", "alpha v1\n", "first seed")
	repo.WriteFile("alpha.txt", "alpha v1\nalpha v2\n")
	repo.Git("add", "alpha.txt")
	repo.Git("commit", "-m", "second grow")
	f.c2 = strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("alpha.txt", "alpha v1\n")
	repo.WriteFile("beta.txt", "beta v1\n")
	repo.Git("add", "alpha.txt", "beta.txt")
	repo.Git("commit", "-m", "third shrink")
	f.c3 = strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	return repo, f
}

// cmdClickCommitRow adds the commit to the selection or removes it, as a
// Cmd-click on its row does.
func cmdClickCommitRow(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Cmd, commitRowLabel(hash, subject)); err != nil {
		t.Fatalf("cmd-click commit row %q: %v", subject, err)
	}
}

// shiftClickCommitRow extends the selection to the commit's row, as a
// Shift-click on it does.
func shiftClickCommitRow(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Shift, commitRowLabel(hash, subject)); err != nil {
		t.Fatalf("shift-click commit row %q: %v", subject, err)
	}
}

// selectComparePair enters compare mode on the two commits through real
// clicks and drains every read it starts.
func selectComparePair(t *testing.T, h *testHarness, upperHash, upperSubject, lowerHash, lowerSubject string) {
	t.Helper()
	clickCommitRow(t, h, upperHash, upperSubject)
	h.drain()
	cmdClickCommitRow(t, h, lowerHash, lowerSubject)
	h.ui.Frame()
	h.drain()
}

// gitNameStatus runs the user-facing git diff --name-status for the range and
// parses it into "STATUS\tPATH" lines in git's order.
func gitNameStatus(t *testing.T, repo *gitRepoFixture, base, target string) []string {
	t.Helper()
	out := repo.Git("diff", "--name-status", base, target)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// compareFileLines renders a file-change list as git diff --name-status
// would: one "STATUS\tPATH" line per entry, in order.
func compareFileLines(files []git.FileChange) []string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		path := f.Path
		if f.OrigPath != "" && f.OrigPath != f.Path {
			path = f.OrigPath + "\t" + f.Path
		}
		lines = append(lines, string(f.Status)+"\t"+path)
	}
	return lines
}

// gitDiffContentLines extracts the content lines of one file's patch in a
// range diff: added lines without their leading '+' for sign '+', removed
// lines without their leading '-' for sign '-'. Patch headers never qualify.
func gitDiffContentLines(t *testing.T, repo *gitRepoFixture, base, target, path string, sign byte) []string {
	t.Helper()
	out := repo.Git("diff", base, target, "--", path)
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if len(l) < 2 || l[0] != sign {
			continue
		}
		if strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---") {
			continue
		}
		lines = append(lines, l[1:])
	}
	return lines
}

// assertDiffMatchesRange asserts the diff panel shows exactly the content
// lines of git diff <base> <target> -- <path>: every added line appears as an
// insertion, every removed line as a deletion, and nothing else.
func assertDiffMatchesRange(t *testing.T, h *testHarness, repo *gitRepoFixture, base, target, path string) {
	t.Helper()
	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	del := diffAccessLines(h.ui, ui.RoleDeletion)
	wantIns := gitDiffContentLines(t, repo, base, target, path, '+')
	wantDel := gitDiffContentLines(t, repo, base, target, path, '-')
	for _, l := range wantIns {
		if !slices.Contains(ins, l) {
			t.Fatalf("insertions %q miss range-diff added line %q", ins, l)
		}
		if slices.Contains(del, l) {
			t.Fatalf("added line %q wrongly shows as a deletion too", l)
		}
	}
	for _, l := range wantDel {
		if !slices.Contains(del, l) {
			t.Fatalf("deletions %q miss range-diff removed line %q", del, l)
		}
		if slices.Contains(ins, l) {
			t.Fatalf("removed line %q wrongly shows as an insertion too", l)
		}
	}
	if len(ins) != len(wantIns) {
		t.Fatalf("insertions %q are not exactly the range-diff added lines %q", ins, wantIns)
	}
	if len(del) != len(wantDel) {
		t.Fatalf("deletions %q are not exactly the range-diff removed lines %q", del, wantDel)
	}
}

// Selecting two commits with a click and a Cmd-click enters compare mode:
// the commit lower in the list is the base and the one higher up the target.
// The comparison lists exactly the files of git diff --name-status <base>
// <target>, the first file is selected, and its diff matches git diff <base>
// <target> -- <file>.
func TestHistoryCompareSelectionAndDirection(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-basic")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d, want historyCompare (%d)", hv.mode, historyCompare)
	}
	if hv.compare.base.Hash != f.c1 || hv.compare.target.Hash != f.c2 {
		t.Fatalf("direction = %s…%s, want base %s target %s",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash), short7(f.c1), short7(f.c2))
	}

	// Both chosen rows read selected, the third row does not.
	for hash, subject := range map[string]string{f.c1: "first seed", f.c2: "second grow"} {
		if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(hash, subject)); !ok || !row.Selected {
			t.Fatalf("chosen row %q access = %+v (found=%v), want Selected=true", subject, row, ok)
		}
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c3, "third shrink")); !ok || row.Selected {
		t.Fatalf("unchosen row access = %+v (found=%v), want Selected=false", row, ok)
	}

	// The header carries both commits: the base row above the target row.
	baseRect, ok := h.ui.Find("Comparison Base " + short7(f.c1))
	if !ok {
		t.Fatalf("comparison base row missing; texts=%q", h.ui.Texts())
	}
	targetRect, ok := h.ui.Find("Comparison Target " + short7(f.c2))
	if !ok {
		t.Fatalf("comparison target row missing; texts=%q", h.ui.Texts())
	}
	if targetRect.Y <= baseRect.Y {
		t.Fatalf("target row (y=%.1f) is not below the base row (y=%.1f)", targetRect.Y, baseRect.Y)
	}
	if !h.ui.HasText("Branchline Test") {
		t.Fatalf("compare rows miss the author; texts=%q", h.ui.Texts())
	}

	// The file list is git diff --name-status <base> <target> exactly.
	want := gitNameStatus(t, repo, f.c1, f.c2)
	if len(want) == 0 {
		t.Fatal("fixture produces no range diff")
	}
	got := compareFileLines(hv.compare.files)
	if !slices.Equal(got, want) {
		t.Fatalf("compare files = %q, want git diff --name-status output %q", got, want)
	}

	// The first file is selected and its diff matches the range diff.
	if hv.compare.fileSel != 0 {
		t.Fatalf("compare fileSel = %d, want 0 (first file auto-selected)", hv.compare.fileSel)
	}
	first := hv.compare.files[0]
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+first.Path); !ok || !row.Selected {
		t.Fatalf("first compare file row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if hv.diff.req.Kind != diffRangeFile {
		t.Fatalf("diff kind = %v, want diffRangeFile", hv.diff.req.Kind)
	}
	assertDiffMatchesRange(t, h, repo, f.c1, f.c2, first.Path)
}

// The direction of a comparison follows the list order, not the click order:
// clicking the lower commit first and Cmd-clicking the one above it still
// compares from the lower commit to the upper one.
func TestHistoryCompareDirectionIgnoresClickOrder(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-order")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	// Click the lower row first, then Cmd-click the row above it.
	selectComparePair(t, h, f.c1, "first seed", f.c2, "second grow")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d, want historyCompare (%d)", hv.mode, historyCompare)
	}
	if hv.compare.base.Hash != f.c1 || hv.compare.target.Hash != f.c2 {
		t.Fatalf("direction = %s…%s, want base %s target %s regardless of click order",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash), short7(f.c1), short7(f.c2))
	}
	assertDiffMatchesRange(t, h, repo, f.c1, f.c2, "alpha.txt")
}

// The Swap Comparison Direction button exchanges base and target: the file
// statuses flip (an added file becomes a deletion) and the diff shows the
// inverted patch.
func TestHistoryCompareSwapDirection(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-swap")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	hv := &h.app.historyView
	if hv.compare.base.Hash != f.c1 {
		t.Fatalf("setup: base = %q, want %q", short7(hv.compare.base.Hash), short7(f.c1))
	}

	if err := h.ui.Click("Swap Comparison Direction"); err != nil {
		t.Fatalf("click Swap Comparison Direction: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if hv.compare.base.Hash != f.c2 || hv.compare.target.Hash != f.c1 {
		t.Fatalf("after swap direction = %s…%s, want base %s target %s",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash), short7(f.c2), short7(f.c1))
	}
	// The header rows carry the new direction: the base row names c2 now.
	if _, ok := h.ui.Find("Comparison Base " + short7(f.c2)); !ok {
		t.Fatalf("base row not swapped to c2; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("Comparison Target " + short7(f.c1)); !ok {
		t.Fatalf("target row not swapped to c1; texts=%q", h.ui.Texts())
	}

	// The swapped file list matches git diff --name-status <c2> <c1>: the
	// line c2 added now reads as removed.
	want := gitNameStatus(t, repo, f.c2, f.c1)
	if got := compareFileLines(hv.compare.files); !slices.Equal(got, want) {
		t.Fatalf("swapped compare files = %q, want %q", got, want)
	}
	assertDiffMatchesRange(t, h, repo, f.c2, f.c1, "alpha.txt")
}

// Swapping the direction flips an added file into a deletion, both in the
// file list's status letters and in the file's diff.
func TestHistoryCompareSwapFlipsAddedAndDeleted(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-flip")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	// Compare c2 with c3: beta.txt is added from c2 to c3.
	selectComparePair(t, h, f.c3, "third shrink", f.c2, "second grow")

	hv := &h.app.historyView
	if hv.compare.base.Hash != f.c2 || hv.compare.target.Hash != f.c3 {
		t.Fatalf("setup direction = %s…%s, want base c2 target c3",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
	statusOf := func(path string) byte {
		for _, fl := range hv.compare.files {
			if fl.Path == path {
				return fl.Status
			}
		}
		return 0
	}
	if got := statusOf("beta.txt"); got != 'A' {
		t.Fatalf("beta.txt status = %q, want 'A' in c2..c3", got)
	}

	// Load beta.txt's diff through its file row before swapping.
	if err := h.ui.Click("File beta.txt"); err != nil {
		t.Fatalf("click File beta.txt: %v", err)
	}
	h.drain()
	assertDiffMatchesRange(t, h, repo, f.c2, f.c3, "beta.txt")
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "beta v1") {
		t.Fatalf("forward diff insertions = %q, want beta v1", ins)
	}

	if err := h.ui.Click("Swap Comparison Direction"); err != nil {
		t.Fatalf("click Swap Comparison Direction: %v", err)
	}
	h.ui.Frame()
	h.drain()

	// After the swap beta.txt is deleted, and the auto-selected file's diff
	// comes from the inverted direction.
	if got := statusOf("beta.txt"); got != 'D' {
		t.Fatalf("beta.txt status after swap = %q, want 'D'", got)
	}
	if got := statusOf("alpha.txt"); got != 'M' {
		t.Fatalf("alpha.txt status after swap = %q, want 'M'", got)
	}
	if err := h.ui.Click("File beta.txt"); err != nil {
		t.Fatalf("click File beta.txt after swap: %v", err)
	}
	h.drain()
	assertDiffMatchesRange(t, h, repo, f.c3, f.c2, "beta.txt")
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "beta v1") {
		t.Fatalf("inverted diff deletions = %q, want beta v1", del)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); slices.Contains(ins, "beta v1") {
		t.Fatalf("inverted diff still inserts beta v1: %q", ins)
	}
}

// Shift-clicking a row extends the selection from the row last chosen to it,
// so two rows can be compared without any Cmd-click.
func TestHistoryCompareShiftClickRange(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-shift")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, f.c2, "second grow")
	h.drain()
	shiftClickCommitRow(t, h, f.c1, "first seed")
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d after shift-click, want historyCompare (%d)", hv.mode, historyCompare)
	}
	if hv.compare.base.Hash != f.c1 || hv.compare.target.Hash != f.c2 {
		t.Fatalf("direction = %s…%s, want base c1 target c2",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
}

// A plain click inside a comparison narrows the selection to that one
// commit, and the pane returns to the single-commit detail exactly as before
// multi-selection existed.
func TestHistoryComparePlainClickReturnsToSingle(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-back")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	clickCommitRow(t, h, f.c3, "third shrink")
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after plain click, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c3 {
		t.Fatalf("sel = %q, want %q (third shrink)", hv.sel, f.c3)
	}
	if len(hv.files) == 0 {
		t.Fatal("single-commit file list empty after leaving compare mode")
	}
	if hv.diff.req.Kind != diffCommitFile {
		t.Fatalf("diff kind = %v after leaving compare, want diffCommitFile", hv.diff.req.Kind)
	}
	if h.ui.HasText("Comparing") {
		t.Fatalf("compare header still rendered in single-commit mode; texts=%q", h.ui.Texts())
	}
	// The auto-selected first file of the commit is alpha.txt, whose c2→c3
	// change drops the added line.
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "alpha v2") {
		t.Fatalf("single-commit diff deletions = %q, want alpha v2", del)
	}
}

// With three or more commits chosen the pane shows how many are selected and
// a hint, and no comparison or diff is loaded.
func TestHistoryCompareThreeSelectedShowsPlaceholder(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-multi")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, f.c2, "second grow")
	h.drain()
	shiftClickCommitRow(t, h, f.c1, "first seed")
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}
	// Cmd-clicking a third row adds it to the two already chosen.
	cmdClickCommitRow(t, h, f.c3, "third shrink")
	h.ui.Frame()
	h.drain()

	if hv.mode != historyMultiSelect {
		t.Fatalf("mode = %d with three rows chosen, want historyMultiSelect (%d)", hv.mode, historyMultiSelect)
	}
	if !h.ui.HasText("3 commits selected") {
		t.Fatalf("missing the 3-commits-selected placeholder; texts=%q", h.ui.Texts())
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff loaded with three rows chosen: %+v", hv.diff.req)
	}
	if len(hv.compare.files) != 0 {
		t.Fatalf("compare files loaded with three rows chosen: %+v", hv.compare.files)
	}

	// Cmd-clicking one row back out returns to the comparison.
	cmdClickCommitRow(t, h, f.c3, "third shrink")
	h.ui.Frame()
	h.drain()
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d after deselecting the third row, want historyCompare (%d)", hv.mode, historyCompare)
	}
	if hv.compare.base.Hash != f.c1 || hv.compare.target.Hash != f.c2 {
		t.Fatalf("direction after deselect = %s…%s, want base c1 target c2",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
}

// Cmd-clicking a chosen row removes it from the selection: removing one of
// two compared commits shows the other commit's single-commit detail.
func TestHistoryCompareCmdClickDeselectFallsBackToSingle(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-deselect")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}
	cmdClickCommitRow(t, h, f.c1, "first seed")
	h.ui.Frame()
	h.drain()

	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after deselecting one of two, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c2 {
		t.Fatalf("sel = %q, want the remaining commit %q", hv.sel, f.c2)
	}
	if !strings.HasPrefix(hv.detail.Message, "second grow") {
		t.Fatalf("detail = %q, want the remaining commit's detail", hv.detail.Message)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c2, "second grow")); !ok || !row.Selected {
		t.Fatalf("remaining row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c1, "first seed")); !ok || row.Selected {
		t.Fatalf("deselected row access = %+v (found=%v), want Selected=false", row, ok)
	}
}

// Selecting every commit with Cmd+A chooses more than two rows, so the pane
// shows the count placeholder and no diff.
func TestHistoryCompareSelectAllShowsPlaceholder(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-all")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, f.c2, "second grow")
	h.drain()

	h.ui.Key(ui.Cmd, ui.KeyA)
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.selection.Len() != 3 {
		t.Fatalf("selection size after Cmd+A = %d, want 3", hv.selection.Len())
	}
	if hv.mode != historyMultiSelect {
		t.Fatalf("mode = %d after Cmd+A, want historyMultiSelect (%d)", hv.mode, historyMultiSelect)
	}
	if !h.ui.HasText("3 commits selected") {
		t.Fatalf("missing the count placeholder after Cmd+A; texts=%q", h.ui.Texts())
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff loaded with all rows chosen: %+v", hv.diff.req)
	}
}

// Two commits whose trees are identical compare to an empty file list, which
// reads as No differences instead of an error or a stale file list.
func TestHistoryCompareNoDifferences(t *testing.T) {
	repo := newGitRepo(t, "cmp-nodiff")
	c1 := repo.CommitFile("same.txt", "same\n", "seed same")
	repo.Git("commit", "--allow-empty", "-m", "empty twin")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, c2, "empty twin", c1, "seed same")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d, want compare", hv.mode)
	}
	if len(hv.compare.files) != 0 {
		t.Fatalf("compare files = %+v, want none for identical trees", hv.compare.files)
	}
	if !h.ui.HasText("No differences") {
		t.Fatalf("missing the No differences hint; texts=%q", h.ui.Texts())
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff loaded for identical trees: %+v", hv.diff.req)
	}
}

// Leaving compare mode while the comparison's file list is still being read
// discards the read: the slow comparison result never surfaces under the
// single commit the selection moved to.
func TestHistoryCompareRapidSwitchDiscardsStaleResult(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-race")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	clickCommitRow(t, h, f.c2, "second grow")
	h.ui.Frame()
	cmdClickCommitRow(t, h, f.c1, "first seed")
	h.ui.Frame()
	// Workers surface in spawn order: the first click's detail read, then the
	// comparison's file-list read — the one this test holds back.
	detailWorker := h.queue.WaitWorker(t)
	compareWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Move to a single commit before the comparison read finishes.
	clickCommitRow(t, h, f.c3, "third shrink")
	h.ui.Frame()
	h.queue.RunNext(t) // the single-commit detail applies first
	h.ui.Frame()

	hv := &h.app.historyView
	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d, want single commit", hv.mode)
	}
	if !strings.HasPrefix(hv.detail.Message, "third shrink") {
		t.Fatalf("detail = %q, want third shrink", hv.detail.Message)
	}

	h.queue.ReleaseWorker(detailWorker)
	h.queue.ReleaseWorker(compareWorker)
	h.drain()

	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after the stale compare read finished, want single commit", hv.mode)
	}
	if len(hv.compare.files) != 0 {
		t.Fatalf("stale compare files surfaced under the single selection: %+v", hv.compare.files)
	}
	if !strings.HasPrefix(hv.detail.Message, "third shrink") {
		t.Fatalf("detail overwritten by stale compare read: %q", hv.detail.Message)
	}
	if len(hv.files) == 0 {
		t.Fatal("single-commit file list missing after the stale read finished")
	}
}

// Amending the tip away while it is one of the compared commits drops it
// from the comparison on reload: the pane falls back to the other commit's
// single-commit detail and shows nothing of the rewritten commit.
func TestHistoryCompareAmendedCommitFallsBackToSingle(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-amend")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	// Compare c2 with the tip c3.
	selectComparePair(t, h, f.c3, "third shrink", f.c2, "second grow")

	hv := &h.app.historyView
	if hv.compare.base.Hash != f.c2 || hv.compare.target.Hash != f.c3 {
		t.Fatalf("setup direction = %s…%s, want c2…c3", short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}

	// Amend the tip externally, then reload: c3 no longer exists.
	repo.Git("commit", "--amend", "-m", "third rewritten")
	h.app.session.Reload()
	h.drain()

	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after the target was amended away, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c2 {
		t.Fatalf("sel = %q, want the surviving commit %q", hv.sel, f.c2)
	}
	// The surviving commit's detail read starts when the reload's frame
	// prunes the selection, so it lands in a second drain round.
	h.drain()
	if !strings.HasPrefix(hv.detail.Message, "second grow") {
		t.Fatalf("detail = %q, want the surviving commit's detail", hv.detail.Message)
	}
	if hv.selection.Len() != 1 || !hv.selection.Has(f.c2) {
		t.Fatalf("selection after amend = %d keys, want only c2", hv.selection.Len())
	}
	if h.ui.HasText("Comparing") {
		t.Fatalf("compare header survived the amend; texts=%q", h.ui.Texts())
	}
	if hv.diff.req.Kind != diffCommitFile {
		t.Fatalf("diff kind = %v after falling back, want diffCommitFile", hv.diff.req.Kind)
	}
}

// When a rewrite removes both compared commits from the history, the
// comparison and the selection clear entirely instead of showing commits the
// list no longer has.
func TestHistoryCompareBothCommitsVanishClearsPane(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-vanish")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c3, "third shrink", f.c2, "second grow")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}

	// Reset the branch to c1 externally: c2 and c3 leave the walk.
	repo.Git("reset", "--hard", f.c1)
	h.app.session.Reload()
	h.drain()

	if hv.selection.Len() != 0 {
		t.Fatalf("selection not cleared after both commits vanished: %d keys", hv.selection.Len())
	}
	if hv.mode != historyNoSelection {
		t.Fatalf("mode = %d, want historyNoSelection (%d)", hv.mode, historyNoSelection)
	}
	if hv.sel != "" {
		t.Fatalf("sel = %q, want empty", hv.sel)
	}
	if hv.diff.req.Kind != diffNone {
		t.Fatalf("diff not cleared after both commits vanished: %+v", hv.diff.req)
	}
	if !h.ui.HasText("No commit selected") {
		t.Fatalf("missing empty-detail hint; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Comparing") {
		t.Fatalf("compare header survived the rewrite; texts=%q", h.ui.Texts())
	}
}

// A right-click on a commit row opens the menu of that row's commit without
// moving the selection: the comparison stays exactly as it was.
func TestHistoryCompareContextMenuActsOnRowKeepsSelection(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-menu")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}

	// Right-click the unselected third row: the menu acts on it, and the
	// comparison selection does not move.
	row := commitRowLabel(f.c3, "third shrink")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click third row: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Reset 'main' to Here…"); err != nil {
		t.Fatalf("choose Reset 'main' to Here…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()

	// The modal targets the right-clicked commit, not either compared one.
	if !hasExactText(h.ui, short7(f.c3)+" third shrink") {
		t.Fatalf("reset modal does not target the right-clicked commit; texts=%q", h.ui.Texts())
	}
	if hv.selection.Len() != 2 || !hv.selection.Has(f.c1) || !hv.selection.Has(f.c2) {
		t.Fatalf("selection moved on right-click: len=%d hasC1=%v hasC2=%v",
			hv.selection.Len(), hv.selection.Has(f.c1), hv.selection.Has(f.c2))
	}
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d after right-click, want compare", hv.mode)
	}
	if hv.compare.base.Hash != f.c1 || hv.compare.target.Hash != f.c2 {
		t.Fatalf("comparison direction changed on right-click: %s…%s",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}

	// Right-clicking one of the compared rows likewise leaves the selection
	// untouched.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel reset modal: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick(commitRowLabel(f.c2, "second grow")); err != nil {
		t.Fatalf("right-click compared row: %v", err)
	}
	h.ui.Frame()
	if hv.selection.Len() != 2 || !hv.selection.Has(f.c1) || !hv.selection.Has(f.c2) {
		t.Fatalf("selection moved when right-clicking a chosen row: len=%d", hv.selection.Len())
	}
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d after right-clicking a chosen row, want compare", hv.mode)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The shared diff panel's Split/Unified toggle and its ignore-whitespace
// toggle work on a range diff exactly as on a commit diff: the whitespace-
// only change disappears from the diff with -w on.
func TestHistoryCompareDiffPanelControls(t *testing.T) {
	repo := newGitRepo(t, "cmp-controls")
	c1 := repo.CommitFile("spaced.txt", "line one\n", "seed spaced")
	repo.WriteFile("spaced.txt", "line  one\n")
	repo.Git("add", "spaced.txt")
	repo.Git("commit", "-m", "inner whitespace")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, c2, "inner whitespace", c1, "seed spaced")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("mode = %d, want compare", hv.mode)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "line  one") {
		t.Fatalf("whitespace diff insertions = %q, want the double-spaced line", ins)
	}

	// -w drops the whitespace-only change: the diff empties out.
	if err := h.ui.Click("Ignore Whitespace"); err != nil {
		t.Fatalf("click Ignore Whitespace: %v", err)
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); len(ins) != 0 {
		t.Fatalf("insertions with -w on = %q, want none", ins)
	}
	if !h.ui.HasText("No diff to display") {
		t.Fatalf("whitespace-only diff not empty with -w on; texts=%q", h.ui.Texts())
	}
	// The file list is unaffected: --name-status does not ignore whitespace.
	if len(hv.compare.files) != 1 || hv.compare.files[0].Path != "spaced.txt" {
		t.Fatalf("compare files with -w on = %+v, want exactly [spaced.txt]", hv.compare.files)
	}

	// Split and Unified both render the diff once -w is off again.
	if err := h.ui.Click("Ignore Whitespace"); err != nil {
		t.Fatalf("click Ignore Whitespace again: %v", err)
	}
	h.drain()
	if err := h.ui.Click("Unified"); err != nil {
		t.Fatalf("click Unified: %v", err)
	}
	h.ui.Frame()
	if hv.diff.Layout() != editor.DiffUnified {
		t.Fatalf("layout after Unified click = %v, want DiffUnified", hv.diff.Layout())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "line  one") {
		t.Fatalf("unified range diff insertions = %q, want the double-spaced line", ins)
	}
}

// The base commit's short hash renders red and the target's green in the
// comparison header, in both themes.
func TestHistoryCompareHeaderHashColors(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-colors")

	h := newTestApp(t)
	h.ui.SetScale(2)
	h.ui.SetWindowActive(true)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")
	settleFrame(h.ui)

	// hashTextInRow finds the short-hash text inside the labeled compare row
	// and returns the dominant saturated hue of its rendered pixels.
	hashHue := func(rowLabel, hash string, dark bool) (red, green int) {
		h.ui.SetDark(dark)
		settleFrame(h.ui)
		img := copyImage(h.ui.Image())
		h.ui.SetDark(false)
		h.ui.Frame()

		all := h.ui.AccessAll()
		var rowRect ui.Rect
		found := false
		for _, a := range all {
			if a.Name == rowLabel {
				rowRect, found = a.Bounds, true
			}
		}
		if !found {
			t.Fatalf("compare row %q not in the access tree", rowLabel)
		}
		var textRect ui.Rect
		for _, a := range all {
			if a.Name != short7(hash) {
				continue
			}
			cx, cy := a.Bounds.X+a.Bounds.W/2, a.Bounds.Y+a.Bounds.H/2
			if cx >= rowRect.X && cx <= rowRect.X+rowRect.W && cy >= rowRect.Y && cy <= rowRect.Y+rowRect.H {
				textRect = a.Bounds
			}
		}
		if textRect.W == 0 {
			t.Fatalf("hash text %q not found inside %q", short7(hash), rowLabel)
		}
		px := dipRectToPixels(img, textRect, 2)
		for y := px.Min.Y; y < px.Max.Y; y++ {
			for x := px.Min.X; x < px.Max.X; x++ {
				cl := img.RGBAAt(x, y)
				mx := max(max(int(cl.R), int(cl.G)), int(cl.B))
				mn := min(min(int(cl.R), int(cl.G)), int(cl.B))
				if mx-mn < 40 {
					continue // background or unsaturated glyph edge
				}
				if int(cl.R) > int(cl.G)+30 && int(cl.R) > int(cl.B)+30 {
					red++
				}
				if int(cl.G) > int(cl.R)+20 && int(cl.G) > int(cl.B)+20 {
					green++
				}
			}
		}
		return red, green
	}

	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		red, green := hashHue("Comparison Base "+short7(f.c1), f.c1, dark)
		if red == 0 || red <= green {
			t.Fatalf("%s: base hash hue red=%d green=%d, want dominantly red", theme, red, green)
		}
		red, green = hashHue("Comparison Target "+short7(f.c2), f.c2, dark)
		if green == 0 || green <= red {
			t.Fatalf("%s: target hash hue red=%d green=%d, want dominantly green", theme, red, green)
		}
	}
}

// --- SSH loopback ---

// The comparison works over a real SSH connection: selecting two commits of
// a loopback SSH repository lists the files changed between them and shows
// the range diff, with every git command running on the server.
func TestHistoryCompareLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)

	// Two more commits on the server: one grows a.txt, one adds extra.txt.
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\nsecond line\n"))
	srv.RunGit(t, repo.Path, "add", "a.txt")
	srv.RunGit(t, repo.Path, "commit", "-m", "grow a")
	srv.WriteFile(t, repo.Path, "extra.txt", []byte("extra line\n"))
	srv.RunGit(t, repo.Path, "add", "extra.txt")
	srv.RunGit(t, repo.Path, "commit", "-m", "add extra")
	tip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	root := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-list", "--max-parents=0", "HEAD"))

	h := newTestApp(t, repo)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()

	clickCommitRow(t, h, tip, "add extra")
	h.drain()
	cmdClickCommitRow(t, h, root, "initial commit")
	h.ui.Frame()
	h.drain()

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("SSH mode = %d, want historyCompare (%d)", hv.mode, historyCompare)
	}
	if hv.compare.base.Hash != root || hv.compare.target.Hash != tip {
		t.Fatalf("SSH direction = %s…%s, want base %s target %s",
			short7(hv.compare.base.Hash), short7(hv.compare.target.Hash), short7(root), short7(tip))
	}
	if hv.compare.err != nil {
		t.Fatalf("SSH compare file list error: %v", hv.compare.err)
	}
	paths := compareFileLines(hv.compare.files)
	if !slices.Contains(paths, "M\ta.txt") || !slices.Contains(paths, "A\textra.txt") {
		t.Fatalf("SSH compare files = %q, want M a.txt and A extra.txt", paths)
	}

	// Clicking the added file loads its range diff from the server.
	if err := h.ui.Click("File extra.txt"); err != nil {
		t.Fatalf("click File extra.txt: %v", err)
	}
	h.drain()
	if hv.diff.req.Kind != diffRangeFile {
		t.Fatalf("SSH diff kind = %v, want diffRangeFile", hv.diff.req.Kind)
	}
	if hv.diff.err != nil {
		t.Fatalf("SSH diff error: %v", hv.diff.err)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "extra line") {
		t.Fatalf("SSH diff insertions = %q, want extra line", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); len(del) != 0 {
		t.Fatalf("SSH diff deletions = %q, want none for an added file", del)
	}

	// The swap button inverts the comparison over SSH too.
	if err := h.ui.Click("Swap Comparison Direction"); err != nil {
		t.Fatalf("click Swap Comparison Direction: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if hv.compare.base.Hash != tip || hv.compare.target.Hash != root {
		t.Fatalf("SSH direction after swap = %s…%s, want inverted", short7(hv.compare.base.Hash), short7(hv.compare.target.Hash))
	}
	paths = compareFileLines(hv.compare.files)
	if !slices.Contains(paths, "D\textra.txt") {
		t.Fatalf("SSH files after swap = %q, want extra.txt deleted", paths)
	}
}

// Show in History replaces an active comparison with the revealed commit: the
// search result becomes the single selection and the comparison is gone.
func TestHistoryCompareSearchRevealLeavesComparison(t *testing.T) {
	repo, f := newCompareRepo(t, "cmp-reveal")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	selectComparePair(t, h, f.c2, "second grow", f.c1, "first seed")

	hv := &h.app.historyView
	if hv.mode != historyCompare {
		t.Fatalf("setup: mode = %d, want compare", hv.mode)
	}

	openSearchView(t, h)
	submitSearchQuery(t, h, "third shrink")
	st := searchState(h)
	if len(st.results) != 1 || st.results[0].Hash != f.c3 {
		t.Fatalf("search results = %q, want third shrink", searchResultSubjects(st))
	}
	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	h.drain() // the reveal selects inside a frame; drain its detail chain too

	if hv.mode != historySingleCommit {
		t.Fatalf("mode = %d after the reveal, want historySingleCommit (%d)", hv.mode, historySingleCommit)
	}
	if hv.sel != f.c3 {
		t.Fatalf("sel = %q after the reveal, want %q", hv.sel, f.c3)
	}
	if hv.selection.Len() != 1 || !hv.selection.Has(f.c3) {
		t.Fatalf("selection after reveal = %d keys, want only the revealed commit", hv.selection.Len())
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(f.c3, "third shrink")); !ok || !row.Selected {
		t.Fatalf("revealed row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if h.ui.HasText("Comparing") {
		t.Fatalf("compare header survived the reveal; texts=%q", h.ui.Texts())
	}
}
