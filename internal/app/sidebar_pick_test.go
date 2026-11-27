package app

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// pickFixture builds a repository with main checked out and the branches
// alpha, beta, master, release/one and zeta at or ahead of the initial
// commit, so every sorting of them has enough rows to range over.
func pickFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"alpha", "beta", "master", "release/one", "zeta"} {
		repo.CreateBranch(br)
	}
	return repo
}

// clickBranchRow selects a branch row with a plain click, as the sidebar's
// primary action does.
func clickBranchRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click(name); err != nil {
		t.Fatalf("click branch row %q: %v\ntexts=%q", name, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// cmdClickBranchRow toggles a branch row in the selection, as a Cmd-click
// on it does.
func cmdClickBranchRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Cmd, name); err != nil {
		t.Fatalf("cmd-click branch row %q: %v\ntexts=%q", name, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// shiftClickBranchRow extends the selection to a branch row, as a Shift-click
// on it does.
func shiftClickBranchRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Shift, name); err != nil {
		t.Fatalf("shift-click branch row %q: %v\ntexts=%q", name, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// pickedBranchNames returns the local branch names in the sidebar selection,
// sorted so assertions compare the set.
func pickedBranchNames(h *testHarness) []string {
	var out []string
	for key := range h.app.refsView.pick {
		if key.kind == sidebarRefLocalBranch {
			out = append(out, key.name)
		}
	}
	sort.Strings(out)
	return out
}

// wantPicked fails the test unless exactly the wanted branches are selected.
func wantPicked(t *testing.T, h *testHarness, want ...string) {
	t.Helper()
	got := pickedBranchNames(h)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("sidebar selection = %q, want %q", got, want)
	}
}

// A plain click on a branch row selects exactly that row: another plain
// click moves the single-row selection over, and the row becomes the anchor
// range clicks start from.
func TestSidebarPickPlainClickSelectsOnlyThatRow(t *testing.T) {
	repo := pickFixture(t, "pick-plain")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	wantPicked(t, h, "alpha")

	clickBranchRow(t, h, "beta")
	wantPicked(t, h, "beta")

	// The selection is single-row, so no row shows the multi-select state:
	// alpha reads unselected, and only the current branch carries the mark.
	if row, ok := findAccessNode(t, h.ui, "alpha"); !ok || row.Selected {
		t.Fatalf("alpha row Selected = %+v (found=%v), want false for a single-row selection", row, ok)
	}
	if row, ok := findAccessNode(t, h.ui, "beta"); !ok || row.Selected {
		t.Fatalf("beta row Selected = %+v (found=%v), want false for a single-row selection", row, ok)
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("current branch main row Selected = %+v (found=%v), want true as before", row, ok)
	}
}

// Cmd-clicking rows adds them to the selection or takes them back out; when
// the last row was toggled out the anchor joins an otherwise empty selection
// again instead of leaving a bare row without its anchor.
func TestSidebarPickCmdClickTogglesRows(t *testing.T) {
	repo := pickFixture(t, "pick-cmd")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "master")
	wantPicked(t, h, "alpha", "master")

	// A second Cmd-click on a selected row removes only it.
	cmdClickBranchRow(t, h, "alpha")
	wantPicked(t, h, "master")

	// Toggling the last row out empties the selection entirely.
	cmdClickBranchRow(t, h, "master")
	wantPicked(t, h)

	// Cmd-clicking a different row with nothing selected seeds the anchor —
	// master, the last anchor — alongside it.
	cmdClickBranchRow(t, h, "beta")
	wantPicked(t, h, "beta", "master")
}

// Shift-clicking selects the range from the anchor row to the clicked one in
// the order the sidebar shows the branches, never in the order git reports:
// the same clicks select a different set when the sort order is backward.
func TestSidebarPickShiftClickRangeFollowsDisplayOrder(t *testing.T) {
	repo := pickFixture(t, "pick-shift")

	h := newTestApp(t)
	// Pin the alphabetical order: a fresh store defaults to backward.
	settings := h.store.Settings()
	settings.LocalBranchSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical: alpha, beta, main, master, release/one, zeta — the range
	// alpha→master covers alpha, beta, main, master.
	assertSidebarOrder(t, h, "alpha", "beta", "main", "master", "release/one", "zeta")
	clickBranchRow(t, h, "alpha")
	shiftClickBranchRow(t, h, "master")
	wantPicked(t, h, "alpha", "beta", "main", "master")

	// The anchor does not move on a Shift-click: extending toward zeta
	// widens the range to everything from alpha down.
	shiftClickBranchRow(t, h, "zeta")
	wantPicked(t, h, "alpha", "beta", "main", "master", "release/one", "zeta")

	// Backward sorting with master and folders first shows master, main,
	// release/one, zeta, beta, alpha: the range beta→release/one now covers
	// release/one, zeta, beta — the backward-displayed adjacency. Following
	// the snapshot's alphabetical order instead would select beta, main,
	// master, release/one.
	settings.LocalBranchSort = refSortBackwardMasterFolders
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save backward sort: %v", err)
	}
	h.ui.Frame()
	assertSidebarOrder(t, h, "master", "main", "release/one", "zeta", "beta", "alpha")
	clickBranchRow(t, h, "beta")
	shiftClickBranchRow(t, h, "release/one")
	wantPicked(t, h, "beta", "zeta", "release/one")
}

// A Shift-click without an anchor in the shown list — a branch a refresh
// dropped, or no click ever landed — acts as a plain click, and rows that
// disappear from the snapshot stop counting as selected.
func TestSidebarPickShiftClickWithoutAnchorActsPlain(t *testing.T) {
	repo := pickFixture(t, "pick-no-anchor")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// No click ever landed: the first Shift-click selects only its row.
	shiftClickBranchRow(t, h, "beta")
	wantPicked(t, h, "beta")

	// Delete the anchor externally and reload: the key stays in the
	// selection until it is used, but no longer counts as a selected row.
	repo.Git("branch", "-D", "beta")
	h.app.session.Reload()
	h.drain()

	// With beta gone the selection holds only a stale key, so beta's row
	// reads exactly like a single-row selection: nothing multi-selected.
	for _, name := range []string{"alpha", "main", "zeta"} {
		row, ok := findAccessNode(t, h.ui, name)
		if !ok {
			t.Fatalf("branch row %q missing after deleting beta; texts=%q", name, h.ui.Texts())
		}
		if name == "main" {
			if !row.Selected {
				t.Fatalf("current branch main row Selected = %+v, want true", row)
			}
		} else if row.Selected {
			t.Fatalf("%s row Selected = %+v, want false: stale keys never select a row", name, row)
		}
	}

	// A Shift-click whose anchor is no longer in the list acts as a plain
	// click: only the clicked row is selected, the stale key is dropped.
	shiftClickBranchRow(t, h, "zeta")
	wantPicked(t, h, "zeta")
}

// Two or more selected rows read selected to assistive technology — including
// rows that are not the current branch — while a single-row selection changes
// nothing, and the current branch's row stays selected throughout.
func TestSidebarPickAccessSelectedReflectsMultiSelection(t *testing.T) {
	repo := pickFixture(t, "pick-access")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "zeta")

	for _, name := range []string{"alpha", "zeta"} {
		if row, ok := findAccessNode(t, h.ui, name); !ok || !row.Selected {
			t.Fatalf("%s row Selected = %+v (found=%v), want true in a multi-selection", name, row, ok)
		}
	}
	if row, ok := findAccessNode(t, h.ui, "beta"); !ok || row.Selected {
		t.Fatalf("unselected beta row Selected = %+v (found=%v), want false", row, ok)
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("current branch main row Selected = %+v (found=%v), want true", row, ok)
	}

	// Back to one row: the multi-select marks clear again.
	clickBranchRow(t, h, "beta")
	if row, ok := findAccessNode(t, h.ui, "beta"); !ok || row.Selected {
		t.Fatalf("beta row Selected = %+v (found=%v), want false in a single-row selection", row, ok)
	}
	if row, ok := findAccessNode(t, h.ui, "alpha"); !ok || row.Selected {
		t.Fatalf("alpha row Selected = %+v (found=%v), want false after narrowing", row, ok)
	}
}

// Clicking a row's delete button does not change the selection: the row the
// button deletes never enters the selection the other rows hold.
func TestSidebarPickDeleteButtonKeepsSelection(t *testing.T) {
	repo := pickFixture(t, "pick-delbtn")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "master")
	wantPicked(t, h, "alpha", "master")

	if err := h.ui.Click("Delete branch beta"); err != nil {
		t.Fatalf("click Delete branch beta: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	wantPicked(t, h, "alpha", "master")

	// The single-branch delete dialog opened for beta; canceling it leaves
	// the branch and the selection exactly as they were.
	drainDeleteCheck(t, h)
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel delete dialog: %v", err)
	}
	h.ui.Frame()
	wantPicked(t, h, "alpha", "master")
	if !branchExists(t, repo, "beta") {
		t.Fatal("beta is gone even though the delete dialog was canceled")
	}
}

// Switching repositories clears the selection: the rows of the previous
// repository never carry over into the new one.
func TestSidebarPickClearsOnRepoSwitch(t *testing.T) {
	repoA := pickFixture(t, "pick-switch-a")
	repoB := newGitRepo(t, "pick-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "master")
	wantPicked(t, h, "alpha", "master")

	h.openRepo(repoB.StoreRepo())
	h.drain()

	wantPicked(t, h)
	if got := h.app.refsView.pickAnchor; got.name != "" {
		t.Fatalf("pick anchor survived the repository switch: %+v", got)
	}
}

// Right-clicking a row inside a multi-selection shows the batch menu of the
// selection — batch delete and copy, one line per selected branch — while
// right-clicking a row outside it first narrows the selection to that row
// and shows the row's own single-branch menu.
func TestSidebarPickRightClickChoosesBatchOrSingleMenu(t *testing.T) {
	repo := pickFixture(t, "pick-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "zeta")

	// Inside the selection: the batch menu of the two selected branches.
	if err := h.ui.RightClick("zeta"); err != nil {
		t.Fatalf("right-click selected row zeta: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	want := []string{"Delete 2 Branches…", "-", "Copy Branch Names"}
	if !slices.Equal(menu, want) {
		t.Fatalf("batch menu = %q, want exactly %q (no selected branch has an upstream)", menu, want)
	}
	wantPicked(t, h, "alpha", "zeta")
	h.ui.CloseMenu()
	h.ui.Frame()

	// Outside the selection: the selection narrows to the clicked row and
	// the menu is its single-branch menu, ending in the management items.
	if err := h.ui.RightClick("beta"); err != nil {
		t.Fatalf("right-click unselected row beta: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu = h.ui.Menu()
	if len(menu) < 5 {
		t.Fatalf("single-branch menu %q too short for the management tail", menu)
	}
	tail := menu[len(menu)-5:]
	wantTail := []string{"-", "Rename…", "Delete…", "-", "Copy Branch Name"}
	if !slices.Equal(tail, wantTail) {
		t.Fatalf("single-branch menu %q tail = %q, want %q", menu, tail, wantTail)
	}
	if slices.Contains(menu, "Copy Branch Names") {
		t.Fatalf("batch menu item Copy Branch Names leaked into the single menu %q", menu)
	}
	wantPicked(t, h, "beta")
	h.ui.CloseMenu()
	h.ui.Frame()
}

// Drawing frames with a multi-selection active runs no git command at all:
// the rows paint from the cached sorted list and the selection state alone.
func TestSidebarPickFramesRunNoGitCommands(t *testing.T) {
	repo := pickFixture(t, "pick-frames")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	cmdClickBranchRow(t, h, "zeta")

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = counting.real
	})

	for i := 0; i < 6; i++ {
		h.ui.Frame()
	}
	if counting.reads != 0 {
		t.Fatalf("redrawing a multi-selected sidebar started %d git commands", counting.reads)
	}
	// The selection survived the frames and still marks its rows.
	wantPicked(t, h, "alpha", "zeta")
	if row, ok := findAccessNode(t, h.ui, "zeta"); !ok || !row.Selected {
		t.Fatalf("zeta row Selected = %+v (found=%v), want true after redrawing", row, ok)
	}
}

// Double-clicking a non-current branch still checks it out with multi-
// selection in place: the first click selects the row and the checkout runs
// as before.
func TestSidebarPickDoubleClickStillChecksOut(t *testing.T) {
	repo := pickFixture(t, "pick-dblclick")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickBranchRow(t, h, "alpha")
	doubleClickCheckout(t, h, "beta")

	if got := currentBranch(t, repo); got != "beta" {
		t.Fatalf("branch after double click = %q, want beta", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}
	// The double click's first click selected beta alone.
	wantPicked(t, h, "beta")
	if row, ok := findAccessNode(t, h.ui, "beta"); !ok || !row.Selected {
		t.Fatalf("checked-out beta row Selected = %+v (found=%v), want true as the current branch", row, ok)
	}
	if !strings.Contains(strings.Join(h.ui.Texts(), "\n"), "beta") {
		t.Fatalf("beta missing from the sidebar after checkout; texts=%q", h.ui.Texts())
	}
}
