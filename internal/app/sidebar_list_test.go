package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// sidebarListRepo builds a repository whose sidebar shows n local branches
// b00000… and, for each of the remotes remote-a, remote-b and remote-c
// asked for, n remote-tracking branches rb00000… — one fast-import commit
// and one update-ref batch, so even thousands of references cost seconds.
func sidebarListRepo(t *testing.T, name string, branches int, remotes []string, remoteBranches int) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("f.txt", "content\n", "initial commit")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	var refs strings.Builder
	for i := 0; i < branches; i++ {
		fmt.Fprintf(&refs, "create refs/heads/b%05d %s\n", i, head)
	}
	for _, remote := range remotes {
		repo.Git("remote", "add", remote, repo.Path())
		for i := 0; i < remoteBranches; i++ {
			fmt.Fprintf(&refs, "create refs/remotes/%s/rb%05d %s\n", remote, i, head)
		}
	}
	if refs.Len() > 0 {
		cmd := exec.Command("git", "update-ref", "--stdin")
		cmd.Dir = repo.Path()
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_TERMINAL_PROMPT=0",
		)
		cmd.Stdin = strings.NewReader(refs.String())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git update-ref: %v\n%s", err, out)
		}
	}
	return repo
}

// pinAlphabeticalSortOrders saves the alphabetical order of every
// reference section, so zero-padded fixture names display in creation
// order whatever the store's defaults are.
func pinAlphabeticalSortOrders(t *testing.T, h *testHarness) {
	t.Helper()
	settings := h.store.Settings()
	settings.LocalBranchSort = ""
	settings.RemoteBranchSort = ""
	settings.TagSort = ""
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("pin alphabetical sort orders: %v", err)
	}
}

// scrollSidebarRowIntoView scrolls the sidebar's virtual list so the row
// of key shows. Rows outside the view are not built, so a click on one
// must bring it into view first — exactly as a user scrolls before
// clicking. The key is the row table's, "br-" plus the full reference name
// of a branch, "tag-" plus the tag name, "stashrow-" plus the stash row
// key.
func scrollSidebarRowIntoView(t *testing.T, h *testHarness, key string) {
	t.Helper()
	rows := h.app.refsView.sidebarRows.rows
	idx := -1
	for i, entry := range rows {
		if entry.key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("sidebar row %q not in the row table of %d rows", key, len(rows))
	}
	h.app.refsView.sidebarList.ScrollIntoView(idx)
	h.ui.Frame()
	h.ui.Frame()
}

// Scrolling 5000 local branches to the end and double-clicking the last
// one checks it out: the virtual list builds the rows the scroll brings
// into view, and they click and act exactly as the rows always did.
func TestSidebarListScrollEndCheckoutLastBranch(t *testing.T) {
	repo := sidebarListRepo(t, "list-scroll-end", 5000, nil, 0)

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The last branch in the alphabetical order is nowhere near the view
	// at the top of 5000 rows: scrolling is what brings it in.
	if _, ok := h.ui.Find("b04999"); ok {
		t.Fatal("b04999 is visible before scrolling, the fixture does not exercise the list")
	}
	scrollSidebarRowIntoView(t, h, "br-refs/heads/b04999")
	if _, ok := h.ui.Find("b04999"); !ok {
		t.Fatalf("b04999 not visible after scrolling into view; texts=%q", h.ui.Texts())
	}

	doubleClickCheckout(t, h, "b04999")
	if got := currentBranch(t, repo); got != "b04999" {
		t.Fatalf("branch after double-clicking the last row = %q, want b04999", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}
}

// A Shift-click range reaching from the first branch of the first remote
// to the last of the third selects every remote-tracking branch in between
// in display order, remotes included; collapsing the Remotes section hides
// its rows, expanding it brings them back, and the same clicks select the
// same range afterwards.
func TestSidebarListShiftRangeSpansRemotes(t *testing.T) {
	repo := sidebarListRepo(t, "list-remote-span", 2, []string{"remote-a", "remote-b", "remote-c"}, 100)

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	want := []string{}
	for _, remote := range []string{"remote-a", "remote-b", "remote-c"} {
		for i := 0; i < 100; i++ {
			want = append(want, fmt.Sprintf("%s/rb%05d", remote, i))
		}
	}

	scrollSidebarRowIntoView(t, h, "rem-br-refs/remotes/remote-a/rb00000")
	clickRefRow(t, h, "remote-a/rb00000")
	scrollSidebarRowIntoView(t, h, "rem-br-refs/remotes/remote-c/rb00099")
	shiftClickRefRow(t, h, "remote-c/rb00099")
	wantPickedRefs(t, h, nil, want, nil)

	// Collapsing the section hides its rows; expanding it brings them back,
	// and the selection is untouched either way. The header is out of view
	// after the range clicks scrolled the list to the end, so the list
	// scrolls back to it first, as a user would.
	scrollSidebarRowIntoView(t, h, "sec-Remotes")
	mustClick(t, h, "Remotes")
	h.ui.Frame()
	if _, ok := h.ui.Find("remote-a/rb00000"); ok {
		t.Fatal("remote-a/rb00000 still visible after collapsing Remotes")
	}
	wantPickedRefs(t, h, nil, want, nil)
	scrollSidebarRowIntoView(t, h, "sec-Remotes")
	mustClick(t, h, "Remotes")
	h.ui.Frame()

	// The same clicks after the collapse and re-expansion select the same
	// range: hiding and showing rows never changes what a range covers.
	scrollSidebarRowIntoView(t, h, "rem-br-refs/remotes/remote-a/rb00000")
	clickRefRow(t, h, "remote-a/rb00000")
	scrollSidebarRowIntoView(t, h, "rem-br-refs/remotes/remote-c/rb00099")
	shiftClickRefRow(t, h, "remote-c/rb00099")
	wantPickedRefs(t, h, nil, want, nil)
}

// Selecting a lone tag unfolds its action bar inside the list: the rows
// below it move down by the bar's height, and the row directly under it
// still clicks and selects exactly where it shows.
func TestSidebarListTagActionBarShiftsRowsBelow(t *testing.T) {
	repo := pickFixture(t, "list-tag-shift")
	for _, tag := range []string{"rel-1", "rel-2", "rel-3"} {
		repo.CreateTag(tag)
	}

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	before, ok := h.ui.Find("rel-2")
	if !ok {
		t.Fatalf("rel-2 row missing before the selection; texts=%q", h.ui.Texts())
	}
	clickRefRow(t, h, "rel-1")
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag rel-1"); !ok {
		t.Fatalf("Delete tag rel-1 action missing under the lone selected tag; texts=%q", h.ui.Texts())
	}
	after, ok := h.ui.Find("rel-2")
	if !ok {
		t.Fatalf("rel-2 row missing under the action bar; texts=%q", h.ui.Texts())
	}
	if after.Y <= before.Y {
		t.Fatalf("rel-2 did not move down when the action bar unfolded: y %v before, %v after", before.Y, after.Y)
	}

	// The row below the bar clicks at its moved position, exactly as before.
	clickRefRow(t, h, "rel-2")
	wantPickedRefs(t, h, nil, nil, []string{"rel-2"})

	// The bar follows the selection: clicking rel-2 moved the lone
	// selection onto it — one row for one, the selection never changing
	// size — so its bar unfolds under rel-2 and rel-1's is gone.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag rel-2"); !ok {
		t.Fatalf("Delete tag rel-2 action missing under the moved selection; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag rel-1"); ok {
		t.Fatal("Delete tag rel-1 action still visible after the selection moved to rel-2")
	}
}

// Creating a branch through the New Branch dialog inserts its row at its
// sorted place in the list, deleting one through its confirmation dialog
// drops the row, and dropping a stash drops its row: the row table follows
// every mutation of what it shows.
func TestSidebarListRowsFollowMutations(t *testing.T) {
	repo := setupSidebarRepo(t, "list-mutations")
	repo.WriteFile("a.txt", "stashed\n")
	pushStashCLI(t, repo, "stashed work")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	assertSidebarOrder(t, h, "feature", "main", "merged", "unmerged")

	// Create aaa-new: alphabetical order puts it ahead of feature.
	mustClick(t, h, "New Branch")
	h.ui.Frame()
	h.ui.Type("aaa-new")
	h.ui.Frame()
	mustClick(t, h, "Create Branch")
	h.drain()
	if !branchExists(t, repo, "aaa-new") {
		t.Fatal("aaa-new missing from the repository after Create Branch")
	}
	assertSidebarOrder(t, h, "aaa-new", "feature", "main", "merged", "unmerged")

	// Delete merged through its row button and the confirmation dialog.
	mustClick(t, h, "Delete branch merged")
	h.ui.Frame()
	drainDeleteCheck(t, h)
	mustClick(t, h, "Confirm Delete")
	h.drain()
	if branchExists(t, repo, "merged") {
		t.Fatal("merged survived Confirm Delete")
	}
	assertSidebarOrder(t, h, "aaa-new", "feature", "main", "unmerged")
	if _, ok := h.ui.Find("merged"); ok {
		t.Fatal("merged row still in the list after the delete")
	}

	// Drop the stash through its action bar: the row disappears with it.
	if _, ok := h.ui.Find("Stash stash@{0}"); !ok {
		t.Fatalf("stash row missing before the drop; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Stash stash@{0}")
	h.ui.Frame()
	mustClick(t, h, "Drop…")
	h.ui.Frame()
	mustClick(t, h, "Confirm Drop Stash")
	h.drain()
	if got := stashCount(t, repo.Path()); got != 0 {
		t.Fatalf("stash list after the drop = %d entries, want 0", got)
	}
	if _, ok := h.ui.Find("Stash stash@{0}"); ok {
		t.Fatal("dropped stash row still in the list")
	}
}

// Switching between two repositories with the same number of branches
// never reuses rows of one for the other: the row table is rebuilt against
// the repository it shows.
func TestSidebarListRepoSwitchRowsNotReused(t *testing.T) {
	repoA := newGitRepo(t, "list-switch-a")
	repoA.CommitFile("a.txt", "a\n", "commit a")
	for _, br := range []string{"aaa-one", "aaa-two", "aaa-three", "aaa-four", "aaa-five"} {
		repoA.CreateBranch(br)
	}
	repoB := newGitRepo(t, "list-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit b")
	for _, br := range []string{"bbb-one", "bbb-two", "bbb-three", "bbb-four", "bbb-five"} {
		repoB.CreateBranch(br)
	}

	h := newTestApp(t, repoB.StoreRepo())
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, ok := h.ui.Find("aaa-three"); !ok {
		t.Fatalf("aaa-three missing in the first repository; texts=%q", h.ui.Texts())
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()
	if _, ok := h.ui.Find("bbb-three"); !ok {
		t.Fatalf("bbb-three missing after switching repositories; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("aaa-three"); ok {
		t.Fatal("aaa-three of the first repository still shows after switching")
	}
	if got := len(h.app.refsView.sidebarRows.rows); got == 0 {
		t.Fatal("the row table is empty after the switch")
	}
	for _, entry := range h.app.refsView.sidebarRows.rows {
		if entry.kind == sidebarRowBranch && strings.HasPrefix(entry.branch.Name, "aaa-") {
			t.Fatalf("row table holds %q of the first repository after switching", entry.branch.Name)
		}
	}
}

// sidebarAccessFixture builds the small repository the access tests read:
// three local branches, one remote with two tracking branches, two tags
// and one stash, every reference pointing at the initial commit.
func sidebarAccessFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("f.txt", "content\n", "initial commit")
	repo.CreateBranch("feature-a")
	repo.CreateBranch("feature-b")
	repo.Git("remote", "add", "origin", repo.Path())
	repo.Git("update-ref", "refs/remotes/origin/alpha", "HEAD")
	repo.Git("update-ref", "refs/remotes/origin/beta", "HEAD")
	repo.CreateTag("v1.0")
	repo.CreateTag("v2.0")
	repo.WriteFile("f.txt", "stashed\n")
	repo.Git("stash", "push", "-m", "access stash")
	return repo
}

// sidebarFocus returns the accessible name of the node the accessibility
// tree reports focused, or false when nothing is focused.
func sidebarFocus(h *testHarness) (string, bool) {
	for _, info := range h.ui.AccessAll() {
		if info.Focused {
			return info.Name, true
		}
	}
	return "", false
}

// sidebarTabFocus presses Tab once and returns the name of the node the
// focus lands on.
func sidebarTabFocus(t *testing.T, h *testHarness) (string, bool) {
	t.Helper()
	h.ui.Key(0, ui.KeyTab)
	h.ui.Frame()
	return sidebarFocus(h)
}

// sidebarTabBaseline is the sequence of accessible names one whole Tab
// cycle moves through, recorded on the tree before the sidebar's scroll
// area became a virtual list: the switcher, the navigation rows, the
// section headers and entry rows, every reference row and its buttons,
// and the controls of the Changes page. The virtual list must keep this
// sequence exactly.
var sidebarTabBaseline = []string{
	"Repository Switcher",
	"Changes",
	"History",
	"Branches",
	"New Branch",
	"main",
	"feature-b",
	"Delete branch feature-b",
	"feature-a",
	"Delete branch feature-a",
	"Remotes",
	"New Remote",
	"origin",
	"origin/alpha",
	"origin/beta",
	"Tags",
	"New Tag",
	"Push All Tags",
	"v1.0",
	"v2.0",
	"Stashes",
	"Save Stash",
	"Stash stash@{0}",
	"Filter Changes",
	"Show as Tree",
	"Commit message",
	"Amend",
	"Divider",
	"Ignore Whitespace",
	"Split",
	"Unified",
	"Fetch",
	"Pull",
	"Push",
}

// The list the sidebar's rows live in is named Sidebar, and one whole Tab
// cycle from nothing focused walks exactly the focus sequence the plain
// scroll container always walked: every step lands on an element with a
// non-empty accessible name, and the sequence wraps around to its start.
func TestSidebarListAccessTabCycle(t *testing.T) {
	repo := sidebarAccessFixture(t, "access-tab-cycle")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if _, ok := h.ui.AccessByRole(ui.RoleList, "Sidebar"); !ok {
		t.Fatal("no list named Sidebar in the accessibility tree")
	}

	if name, ok := sidebarFocus(h); ok {
		t.Fatalf("focus %q before the first Tab, want nothing focused", name)
	}
	got := make([]string, 0, len(sidebarTabBaseline)+1)
	for i := 0; i <= len(sidebarTabBaseline); i++ {
		name, ok := sidebarTabFocus(t, h)
		if !ok {
			t.Fatalf("Tab step %d focused nothing", i)
		}
		if name == "" {
			t.Fatalf("Tab step %d focused an element without a name", i)
		}
		got = append(got, name)
	}
	for i, want := range sidebarTabBaseline {
		if got[i] != want {
			t.Fatalf("Tab step %d focused %q, want %q; sequence %q", i, got[i], want, got)
		}
	}
	if got[len(sidebarTabBaseline)] != sidebarTabBaseline[0] {
		t.Fatalf("the cycle did not wrap to %q after %d steps: last step focused %q",
			sidebarTabBaseline[0], len(sidebarTabBaseline), got[len(sidebarTabBaseline)])
	}
}

// sidebarAccessInfo returns the accessibility information of the one
// focusable non-text node named name, failing the test unless there is
// exactly one. The sidebar's virtual list wraps every row it builds in
// its own item node, which takes the row's name from its content but
// never takes the focus: exactly one same-named non-focusable node of the
// list's item role — that wrapper — is allowed beside the focusable row,
// and anything more fails the test.
func sidebarAccessInfo(t *testing.T, h *testHarness, name string) ui.AccessInfo {
	t.Helper()
	var focusable []ui.AccessInfo
	wrappers := 0
	for _, info := range h.ui.AccessAll() {
		if info.Role == ui.RoleText || info.Name != name {
			continue
		}
		switch {
		case info.Focusable:
			focusable = append(focusable, info)
		case info.Role == ui.RoleListItem:
			wrappers++
		default:
			t.Fatalf("non-focusable %v node named %q that is not the list's row wrapper", info.Role, name)
		}
	}
	if len(focusable) != 1 {
		t.Fatalf("%d focusable non-text nodes named %q, want exactly 1", len(focusable), name)
	}
	if wrappers > 1 {
		t.Fatalf("%d non-focusable item nodes named %q, want at most the list's one row wrapper", wrappers, name)
	}
	return focusable[0]
}

// Every reference and stash row of the sidebar carries its name on
// exactly one focusable node — the row itself — and the rows a click
// selects report themselves selected there. The virtual list's one
// non-focusable item node wrapping each row shares the name, as
// sidebarAccessInfo allows.
func TestSidebarListAccessRowsNamedOnce(t *testing.T) {
	repo := sidebarAccessFixture(t, "access-rows-once")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	for _, name := range []string{"main", "feature-a", "feature-b", "origin", "origin/alpha", "origin/beta", "v1.0", "v2.0", "Stash stash@{0}"} {
		info := sidebarAccessInfo(t, h, name)
		if info.Selected && name != "main" {
			t.Fatalf("row %q reports selected before any click", name)
		}
	}
	if info := sidebarAccessInfo(t, h, "main"); !info.Selected {
		t.Fatal("the current branch row does not report selected")
	}

	// A plain click picks feature-a alone. A single picked branch row does
	// not highlight — only multi-selections paint and report selected, as
	// the sidebar's selection model defines — so it keeps reporting
	// unselected, exactly as it shows.
	clickRefRow(t, h, "feature-a")
	if info := sidebarAccessInfo(t, h, "feature-a"); info.Selected {
		t.Fatal("a lone picked branch row reports selected, want the multi-select-only reporting")
	}
	if info := sidebarAccessInfo(t, h, "feature-b"); info.Selected {
		t.Fatal("feature-b reports selected after feature-a was clicked")
	}

	// Cmd-clicking feature-b selects both rows of the multi-selection.
	cmdClickBranchRow(t, h, "feature-b")
	for _, name := range []string{"feature-a", "feature-b"} {
		if info := sidebarAccessInfo(t, h, name); !info.Selected {
			t.Fatalf("%q does not report selected inside the multi-selection", name)
		}
	}

	// Selecting a tag restarts the selection from it: the tag reports
	// selected, the branches no longer do.
	clickRefRow(t, h, "v1.0")
	if info := sidebarAccessInfo(t, h, "v1.0"); !info.Selected {
		t.Fatal("the clicked tag row does not report selected")
	}
	if info := sidebarAccessInfo(t, h, "feature-a"); info.Selected {
		t.Fatal("feature-a still reports selected after the tag was clicked")
	}

	// Clicking the stash row selects it independently of the reference
	// selection.
	clickRefRow(t, h, "Stash stash@{0}")
	if info := sidebarAccessInfo(t, h, "Stash stash@{0}"); !info.Selected {
		t.Fatal("the clicked stash row does not report selected")
	}
	clickRefRow(t, h, "Stash stash@{0}")
	if info := sidebarAccessInfo(t, h, "Stash stash@{0}"); info.Selected {
		t.Fatal("the stash row still reports selected after the second click")
	}
}

// sidebarAccessBaselineMenu is the context menu the feature-a branch row
// opened before the sidebar virtualized, item for item: merging, rebasing,
// creating, tracking, pushing, renaming, deleting and copying.
var sidebarAccessBaselineMenu = []string{
	"Merge into 'main'…",
	"Rebase 'main' on 'feature-a'…",
	"Interactively Rebase on 'feature-a'…",
	"-",
	"New Branch…",
	"New Tag…",
	"-",
	"Tracking",
	"-",
	"Push to 'origin'…",
	"-",
	"Rename…",
	"Delete…",
	"-",
	"Copy Branch Name",
}

// Clicking a branch row puts the keyboard focus on the row itself — so
// the menu key opens the row's own context menu, exactly the one it
// opened before the sidebar virtualized.
func TestSidebarListAccessClickFocusAndMenuKey(t *testing.T) {
	repo := sidebarAccessFixture(t, "access-click-focus")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "feature-a")
	if name, ok := sidebarFocus(h); !ok || name != "feature-a" {
		t.Fatalf("focus after clicking feature-a = %q (focused %v), want feature-a", name, ok)
	}

	h.ui.Key(ui.Shift, ui.KeyF10)
	h.ui.Frame()
	menu := h.ui.Menu()
	if len(menu) != len(sidebarAccessBaselineMenu) {
		t.Fatalf("Shift+F10 menu = %q, want %q", menu, sidebarAccessBaselineMenu)
	}
	for i, want := range sidebarAccessBaselineMenu {
		if menu[i] != want {
			t.Fatalf("Shift+F10 menu item %d = %q, want %q; menu %q", i, menu[i], want, menu)
		}
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The keys a list would move its own selection with — Up, Down, Shift+Up,
// Cmd+A, Space — change nothing of the sidebar: the rows never belonged
// to a list selection, so the multi-selection, the rows' selected state
// and the tag action bar are exactly as they were, and the focus stays on
// the row it was on.
func TestSidebarListAccessKeysKeepSelection(t *testing.T) {
	repo := sidebarAccessFixture(t, "access-keys")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	keys := []struct {
		name string
		mods ui.Modifiers
		key  ui.Key
	}{
		{"Up", 0, ui.KeyUp},
		{"Down", 0, ui.KeyDown},
		{"Shift+Up", ui.Shift, ui.KeyUp},
		{"Cmd+A", ui.Cmd, ui.KeyA},
		{"Space", 0, ui.KeySpace},
	}

	// With feature-a the only branch selected: every key leaves the
	// selection, the rows' selected state and the focus untouched. Space
	// activates the focused row, as it always did — a plain click on the
	// row already selected selects it again, changing nothing.
	clickRefRow(t, h, "feature-a")
	for _, step := range keys {
		h.ui.Key(step.mods, step.key)
		h.ui.Frame()
		wantPickedRefs(t, h, []string{"feature-a"}, nil, nil)
		if name, ok := sidebarFocus(h); !ok || name != "feature-a" {
			t.Fatalf("after %s the focus is %q (focused %v), want feature-a", step.name, name, ok)
		}
		if info := sidebarAccessInfo(t, h, "feature-a"); info.Selected {
			t.Fatalf("after %s the lone picked feature-a reports selected, want the multi-select-only reporting", step.name)
		}
	}

	// With v1.0 the lone selected tag, its action bar unfolded: the keys
	// that move a list selection leave the selection and the bar as they
	// are. Space is not one of them here: it activates the focused row,
	// and a plain click on the lone selected tag takes the selection off,
	// exactly as clicking the row does.
	clickRefRow(t, h, "v1.0")
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag v1.0"); !ok {
		t.Fatal("the tag action bar did not unfold for the lone selected tag")
	}
	for _, step := range keys {
		if step.name == "Space" {
			continue
		}
		h.ui.Key(step.mods, step.key)
		h.ui.Frame()
		wantPickedRefs(t, h, nil, nil, []string{"v1.0"})
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag v1.0"); !ok {
			t.Fatalf("after %s the tag action bar is gone", step.name)
		}
		if info := sidebarAccessInfo(t, h, "v1.0"); !info.Selected {
			t.Fatalf("after %s v1.0 no longer reports selected", step.name)
		}
		if name, ok := sidebarFocus(h); !ok || name != "v1.0" {
			t.Fatalf("after %s the focus is %q (focused %v), want v1.0", step.name, name, ok)
		}
	}
}

// Tabbing through a sidebar of fifty local branches reaches the last
// branch row: the virtual list builds the rows beyond its view as the
// focus comes near them, each Tab revealing the next.
func TestSidebarListAccessTabReachesLastBranch(t *testing.T) {
	repo := sidebarListRepo(t, "access-tab-end", 50, nil, 0)

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	const last = "b00049"
	if _, ok := h.ui.Find(last); ok {
		t.Fatalf("%s is visible before any scrolling, the fixture does not exercise the list", last)
	}
	reached := false
	for i := 0; i < 150 && !reached; i++ {
		name, ok := sidebarTabFocus(t, h)
		if !ok {
			t.Fatalf("Tab step %d focused nothing", i)
		}
		if name == "" {
			t.Fatalf("Tab step %d focused an element without a name", i)
		}
		reached = name == last
	}
	if !reached {
		t.Fatalf("150 Tab presses never focused %s", last)
	}
}
