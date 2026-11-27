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

// pickedRemoteBranchNames returns the short names — origin/feature — of the
// remote branches in the sidebar selection, sorted so assertions compare the
// set. The keys hold the full reference names, so the refs/remotes prefix is
// cut away.
func pickedRemoteBranchNames(h *testHarness) []string {
	var out []string
	for key := range h.app.refsView.pick {
		if key.kind == sidebarRefRemoteBranch {
			out = append(out, strings.TrimPrefix(key.name, "refs/remotes/"))
		}
	}
	sort.Strings(out)
	return out
}

// pickedTagNames returns the tag names in the sidebar selection, sorted so
// assertions compare the set.
func pickedTagNames(h *testHarness) []string {
	var out []string
	for key := range h.app.refsView.pick {
		if key.kind == sidebarRefTag {
			out = append(out, key.name)
		}
	}
	sort.Strings(out)
	return out
}

// wantPickedRefs fails the test unless the selection holds exactly the wanted
// rows of every reference kind, each list sorted before comparing.
func wantPickedRefs(t *testing.T, h *testHarness, branches, remoteBranches, tags []string) {
	t.Helper()
	sort.Strings(branches)
	sort.Strings(remoteBranches)
	sort.Strings(tags)
	if got := pickedBranchNames(h); !slices.Equal(got, branches) {
		t.Fatalf("selected local branches = %q, want %q", got, branches)
	}
	if got := pickedRemoteBranchNames(h); !slices.Equal(got, remoteBranches) {
		t.Fatalf("selected remote branches = %q, want %q", got, remoteBranches)
	}
	if got := pickedTagNames(h); !slices.Equal(got, tags) {
		t.Fatalf("selected tags = %q, want %q", got, tags)
	}
}

// clickRefRow plain-clicks any sidebar reference row by its label.
func clickRefRow(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click row %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// cmdClickRefRow Cmd-clicks any sidebar reference row by its label.
func cmdClickRefRow(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Cmd, label); err != nil {
		t.Fatalf("cmd-click row %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// shiftClickRefRow Shift-clicks any sidebar reference row by its label.
func shiftClickRefRow(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.ClickWith(ui.Shift, label); err != nil {
		t.Fatalf("shift-click row %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
	h.ui.Frame()
}

// refPickFixture builds a repository with two bare remotes — origin and
// upstream, origin's main and upstream's master as their default branches —
// with the remote-tracking branches origin/alpha, origin/beta,
// upstream/gamma and upstream/delta fetched in, plus the tags rel-1, rel-2
// and rel-3 and the local branches the pick tests need. Every branch points
// at the initial commit, so no checkout or upstream setup gets in the way.
func refPickFixture(t *testing.T, name string) (repo *gitRepoFixture, origin, upstream string) {
	t.Helper()
	repo = newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"alpha", "beta"} {
		repo.CreateBranch(br)
	}
	origin = repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "alpha:refs/heads/alpha", "beta:refs/heads/beta")
	repo.Git("fetch", "-q", "origin")

	// upstream carries the same commits under its own branch names; the two
	// remotes never share a branch name, so a short name names exactly one
	// row of the sidebar.
	upDir := t.TempDir() + "/upstream.git"
	runGitCommand(t, "", "init", "-q", "--bare", upDir)
	repo.Git("remote", "add", "upstream", upDir)
	repo.Git("push", "-q", "upstream", "alpha:refs/heads/gamma", "beta:refs/heads/delta", "main:refs/heads/master")
	repo.Git("fetch", "-q", "upstream")

	for _, tag := range []string{"rel-1", "rel-2", "rel-3"} {
		repo.CreateTag(tag)
	}
	return repo, origin, upDir
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

// Shift-clicking a remote branch selects the range from the anchor along the
// rows as the sidebar shows them — remote by remote, each remote's branches
// in the sorted order — so a range reaching into another remote covers the
// branches of both, in display order.
func TestSidebarPickRemoteShiftClickSpansRemotes(t *testing.T) {
	repo, _, _ := refPickFixture(t, "pick-remote-span")

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.RemoteBranchSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical remote sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical inside each remote: origin/alpha, origin/beta, origin/main,
	// then upstream/delta, upstream/gamma, upstream/master — the range
	// origin/alpha → upstream/gamma spans the first five.
	clickRefRow(t, h, "origin/alpha")
	shiftClickRefRow(t, h, "upstream/gamma")
	wantPickedRefs(t, h, nil, []string{"origin/alpha", "origin/beta", "origin/main", "upstream/delta", "upstream/gamma"}, nil)

	// The anchor does not move on a Shift-click: narrowing back to
	// origin/beta covers only the two origin branches.
	shiftClickRefRow(t, h, "origin/beta")
	wantPickedRefs(t, h, nil, []string{"origin/alpha", "origin/beta"}, nil)

	// Every selected row reads selected to assistive technology; an
	// unselected row of the same remote does not.
	for _, name := range []string{"origin/alpha", "origin/beta"} {
		if row, ok := findAccessNode(t, h.ui, name); !ok || !row.Selected {
			t.Fatalf("%s row Selected = %+v (found=%v), want true in a multi-selection", name, row, ok)
		}
	}
	if row, ok := findAccessNode(t, h.ui, "upstream/delta"); !ok || row.Selected {
		t.Fatalf("upstream/delta row Selected = %+v (found=%v), want false after narrowing", row, ok)
	}

	// Back to a single row: the multi-select marks clear again.
	clickRefRow(t, h, "origin/alpha")
	if row, ok := findAccessNode(t, h.ui, "origin/alpha"); !ok || row.Selected {
		t.Fatalf("origin/alpha row Selected = %+v (found=%v), want false in a single-row selection", row, ok)
	}
}

// Shift-clicking a tag selects the range in the order the sidebar shows the
// tags: with the backward sort order the same clicks select a different set,
// because the display order — not the order git reports — drives the range.
func TestSidebarPickTagShiftClickFollowsDisplayOrder(t *testing.T) {
	repo := pickFixture(t, "pick-tag-order")
	for _, tag := range []string{"rel-1", "rel-2", "rel-3", "rel-4"} {
		repo.CreateTag(tag)
	}

	h := newTestApp(t)
	settings := h.store.Settings()
	settings.TagSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save alphabetical tag sort: %v", err)
	}
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical: rel-1, rel-2, rel-3, rel-4 — the range rel-1 → rel-3
	// covers the first three.
	clickRefRow(t, h, "rel-1")
	shiftClickRefRow(t, h, "rel-3")
	wantPickedRefs(t, h, nil, nil, []string{"rel-1", "rel-2", "rel-3"})

	// Backward: rel-4, rel-3, rel-2, rel-1 — the range rel-4 → rel-2 now
	// covers rel-4, rel-3, rel-2, the backward-displayed adjacency.
	settings.TagSort = refSortBackward
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("save backward tag sort: %v", err)
	}
	h.ui.Frame()
	clickRefRow(t, h, "rel-4")
	shiftClickRefRow(t, h, "rel-2")
	wantPickedRefs(t, h, nil, nil, []string{"rel-2", "rel-3", "rel-4"})
}

// The selection holds rows of one kind at a time: clicking a row of another
// kind — with or without modifier keys — acts as a plain click, replacing
// the selection with that row and moving the anchor to it.
func TestSidebarPickOtherKindReplacesSelection(t *testing.T) {
	repo, _, _ := refPickFixture(t, "pick-kinds")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Two remote branches selected; Cmd-clicking a tag replaces them all.
	clickRefRow(t, h, "origin/alpha")
	cmdClickRefRow(t, h, "origin/beta")
	wantPickedRefs(t, h, nil, []string{"origin/alpha", "origin/beta"}, nil)
	cmdClickRefRow(t, h, "rel-1")
	wantPickedRefs(t, h, nil, nil, []string{"rel-1"})

	// From the tag, Cmd-clicking a remote branch replaces it again.
	cmdClickRefRow(t, h, "origin/beta")
	wantPickedRefs(t, h, nil, []string{"origin/beta"}, nil)

	// Shift-clicking across kinds acts as a plain click too: the range never
	// mixes kinds, so only the clicked row survives.
	shiftClickRefRow(t, h, "rel-2")
	wantPickedRefs(t, h, nil, nil, []string{"rel-2"})

	// Two tags selected; a plain click on a local branch replaces them.
	cmdClickRefRow(t, h, "rel-3")
	wantPickedRefs(t, h, nil, nil, []string{"rel-2", "rel-3"})
	clickBranchRow(t, h, "alpha")
	wantPickedRefs(t, h, []string{"alpha"}, nil, nil)

	// And a Shift-click back on a tag restarts from that tag alone.
	shiftClickRefRow(t, h, "rel-1")
	wantPickedRefs(t, h, nil, nil, []string{"rel-1"})
}

// A lone selected tag behaves exactly as the single selection always did:
// the action bar unfolds under the row, and a plain click on the same row
// takes the selection off again.
func TestSidebarPickSingleTagActionBarToggles(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "pick-tag-single")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "lw")
	wantPickedRefs(t, h, nil, nil, []string{"lw"})
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); !ok {
		t.Fatalf("Delete tag lw action missing under the lone selected tag; texts=%q", h.ui.Texts())
	}
	if row, ok := findAccessNode(t, h.ui, "lw"); !ok || !row.Selected {
		t.Fatalf("lw row Selected = %+v (found=%v), want true as the lone selection", row, ok)
	}

	clickRefRow(t, h, "lw")
	wantPickedRefs(t, h, nil, nil, nil)
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); ok {
		t.Fatal("Delete tag lw action visible after the lone tag was deselected")
	}
	if row, ok := findAccessNode(t, h.ui, "lw"); !ok || row.Selected {
		t.Fatalf("lw row Selected = %+v (found=%v), want false after deselecting", row, ok)
	}
}

// Two or more selected tags all carry the selected state and read selected
// to assistive technology, and no action bar unfolds under any of them.
func TestSidebarPickMultiTagsHideActionBar(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "pick-tag-multi")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "lw")
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); !ok {
		t.Fatalf("Delete tag lw missing while lw is the lone selection; texts=%q", h.ui.Texts())
	}
	cmdClickRefRow(t, h, "ann")
	wantPickedRefs(t, h, nil, nil, []string{"ann", "lw"})

	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); ok {
		t.Fatal("Delete tag lw visible while two tags are selected")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag ann"); ok {
		t.Fatal("Delete tag ann visible while two tags are selected")
	}
	for _, name := range []string{"lw", "ann"} {
		if row, ok := findAccessNode(t, h.ui, name); !ok || !row.Selected {
			t.Fatalf("%s row Selected = %+v (found=%v), want true in a multi-selection", name, row, ok)
		}
	}
}

// Switching repositories clears the remote branch and tag selection exactly
// as the local branch one: nothing of the previous repository carries over.
func TestSidebarPickRefsClearOnRepoSwitch(t *testing.T) {
	repoA, _, _ := refPickFixture(t, "pick-refs-switch-a")
	repoB := newGitRepo(t, "pick-refs-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")
	repoB.CreateTag("other-tag")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	clickRefRow(t, h, "origin/alpha")
	cmdClickRefRow(t, h, "origin/beta")
	wantPickedRefs(t, h, nil, []string{"origin/alpha", "origin/beta"}, nil)

	h.openRepo(repoB.StoreRepo())
	h.drain()

	wantPickedRefs(t, h, nil, nil, nil)
	if got := h.app.refsView.pickAnchor; got.name != "" {
		t.Fatalf("pick anchor survived the repository switch: %+v", got)
	}
}

// Drawing frames with remote branches or tags multi-selected runs no git
// command at all: the rows paint from the cached sorted lists and the
// selection state alone.
func TestSidebarPickRefFramesRunNoGitCommands(t *testing.T) {
	repo, _, _ := refPickFixture(t, "pick-ref-frames")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "origin/alpha")
	cmdClickRefRow(t, h, "origin/beta")
	wantPickedRefs(t, h, nil, []string{"origin/alpha", "origin/beta"}, nil)

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
		t.Fatalf("redrawing a multi-selected remote branch sidebar started %d git commands", counting.reads)
	}

	// The same holds with two tags selected instead.
	clickRefRow(t, h, "rel-1")
	cmdClickRefRow(t, h, "rel-2")
	wantPickedRefs(t, h, nil, nil, []string{"rel-1", "rel-2"})
	counting.reads = 0
	for i := 0; i < 6; i++ {
		h.ui.Frame()
	}
	if counting.reads != 0 {
		t.Fatalf("redrawing a multi-selected tag sidebar started %d git commands", counting.reads)
	}
	wantPickedRefs(t, h, nil, nil, []string{"rel-1", "rel-2"})
}
