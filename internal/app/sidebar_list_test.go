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
