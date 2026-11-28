package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// folderTestHarnessAt builds the harness on the store file at storePath, the
// way a restart of the app opens the same store again: reopening it in a new
// harness is how the tests check the collapse choices survive.
func folderTestHarnessAt(t *testing.T, storePath string) *testHarness {
	t.Helper()
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{t: t, app: a, store: s, queue: q}
	h.ui = ui.NewTester(instantView(a.view), 1200, 800)
	h.ui.Frame()
	return h
}

// sidebarFolderFixture builds a repository with main checked out and the
// branches feature/a, feature/b, feature/ui/menu and bugfix/x at the initial
// commit, so the Branches section groups into the folders feature and
// bugfix, feature nesting ui.
func sidebarFolderFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	for _, br := range []string{"feature/a", "feature/b", "feature/ui/menu", "bugfix/x"} {
		repo.CreateBranch(br)
	}
	return repo
}

// sidebarRefRowSummary is the test view of one folder or reference row of
// the sidebar's row table: its kind, the name it groups or shows by — the
// folder path or the reference name — and its nesting depth.
type sidebarRefRowSummary struct {
	kind  string
	name  string
	depth int
}

// sidebarRefRows lists the folder and reference rows of the row table in
// display order: folder rows by their accessible path, branch rows by their
// short name, remote rows by their name with the remote, tags by name. Every
// other row of the table is left out.
func sidebarRefRows(t *testing.T, h *testHarness) []sidebarRefRowSummary {
	t.Helper()
	var out []sidebarRefRowSummary
	for _, entry := range h.app.refsView.sidebarRows.rows {
		switch entry.kind {
		case sidebarRowFolder:
			out = append(out, sidebarRefRowSummary{kind: "folder", name: entry.folderPath, depth: entry.depth})
		case sidebarRowBranch:
			out = append(out, sidebarRefRowSummary{kind: "branch", name: entry.branch.Name, depth: entry.depth})
		case sidebarRowRemoteBranch:
			out = append(out, sidebarRefRowSummary{kind: "remotebranch", name: entry.branch.Name, depth: entry.depth})
		case sidebarRowTag:
			out = append(out, sidebarRefRowSummary{kind: "tag", name: entry.tag.Name, depth: entry.depth})
		}
	}
	return out
}

// wantSidebarRefRows fails the test unless the folder and reference rows of
// the row table are exactly the wanted sequence.
func wantSidebarRefRows(t *testing.T, h *testHarness, want []sidebarRefRowSummary) {
	t.Helper()
	got := sidebarRefRows(t, h)
	if !slices.Equal(got, want) {
		t.Fatalf("sidebar rows = %+v, want %+v", got, want)
	}
}

// wantFolderCount fails the test unless the row table's folder row of path
// carries exactly the wanted reference count.
func wantFolderCount(t *testing.T, h *testHarness, path string, count int) {
	t.Helper()
	for _, entry := range h.app.refsView.sidebarRows.rows {
		if entry.kind == sidebarRowFolder && entry.folderPath == path {
			if entry.count != count {
				t.Fatalf("folder %q count = %d, want %d", path, entry.count, count)
			}
			return
		}
	}
	t.Fatalf("no folder row %q in the row table", path)
}

// sidebarShowsText reports whether s is a visible text of the window: a
// text element, never the accessible label of a row, so the shortened texts
// of the reference rows can be told apart from the full names their rows
// are labeled with.
func sidebarShowsText(h *testHarness, s string) bool {
	for _, info := range h.ui.AccessAll() {
		if info.Role == ui.RoleText && info.Name == s {
			return true
		}
	}
	return false
}

// The Branches section groups slashed branch names into one folder row per
// name segment, folders standing where their first member sorts, references
// inside them indented one level deeper and showing only their last
// segment, and every folder counting the references of its whole subtree.
func TestSidebarFoldersGroupBranches(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-group")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Alphabetical: bugfix/x sorts first, then the feature branches, then
	// main. The folder rows stand ahead of their first members.
	wantSidebarRefRows(t, h, []sidebarRefRowSummary{
		{kind: "folder", name: "bugfix", depth: 0},
		{kind: "branch", name: "bugfix/x", depth: 1},
		{kind: "folder", name: "feature", depth: 0},
		{kind: "branch", name: "feature/a", depth: 1},
		{kind: "branch", name: "feature/b", depth: 1},
		{kind: "folder", name: "feature/ui", depth: 1},
		{kind: "branch", name: "feature/ui/menu", depth: 2},
		{kind: "branch", name: "main", depth: 0},
	})
	wantFolderCount(t, h, "bugfix", 1)
	wantFolderCount(t, h, "feature", 3)
	wantFolderCount(t, h, "feature/ui", 1)

	// The visible text of the reference rows is only the last segment: the
	// row of feature/ui/menu shows menu, and the full names show nowhere as
	// text. The folder rows show their segments — found through the
	// accessible labels, which no reference row of this fixture shares.
	for _, text := range []string{"x", "a", "b", "menu", "main"} {
		if !sidebarShowsText(h, text) {
			t.Fatalf("row text %q missing from the sidebar; texts=%q", text, h.ui.Texts())
		}
	}
	for _, segment := range []string{"bugfix", "feature", "ui"} {
		if _, ok := h.ui.Find(segment); !ok {
			t.Fatalf("folder segment %q missing from the sidebar; texts=%q", segment, h.ui.Texts())
		}
	}
	for _, text := range []string{"feature/a", "feature/b", "feature/ui/menu", "bugfix/x"} {
		if sidebarShowsText(h, text) {
			t.Fatalf("full branch name %q shows as visible text, want only the last segment", text)
		}
	}
}

// The branches of every remote group by their names without the remote: the
// feature folder of one remote is a row of its own under that remote, and
// collapsing it leaves the same-named folder of the other remote open with
// its rows visible.
func TestSidebarFoldersRemoteFoldersIndependent(t *testing.T) {
	repo := newGitRepo(t, "fold-remote")
	repo.CommitFile("f.txt", "content\n", "initial commit")
	repo.Git("remote", "add", "origin", repo.Path())
	repo.Git("remote", "add", "upstream", repo.Path())
	for _, ref := range []string{
		"refs/remotes/origin/feature/a", "refs/remotes/origin/feature/b", "refs/remotes/origin/plain",
		"refs/remotes/upstream/feature/c", "refs/remotes/upstream/main",
	} {
		repo.Git("update-ref", ref, "HEAD")
	}

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	wantSidebarRefRows(t, h, []sidebarRefRowSummary{
		{kind: "branch", name: "main", depth: 0},
		{kind: "folder", name: "origin/feature", depth: 0},
		{kind: "remotebranch", name: "origin/feature/a", depth: 1},
		{kind: "remotebranch", name: "origin/feature/b", depth: 1},
		{kind: "remotebranch", name: "origin/plain", depth: 0},
		{kind: "folder", name: "upstream/feature", depth: 0},
		{kind: "remotebranch", name: "upstream/feature/c", depth: 1},
		{kind: "remotebranch", name: "upstream/main", depth: 0},
	})
	wantFolderCount(t, h, "origin/feature", 2)
	wantFolderCount(t, h, "upstream/feature", 1)

	// Collapsing origin's feature folder hides only its rows: upstream's
	// same-named folder stays open with its branch visible.
	clickRefRow(t, h, "origin/feature")
	wantSidebarRefRows(t, h, []sidebarRefRowSummary{
		{kind: "branch", name: "main", depth: 0},
		{kind: "folder", name: "origin/feature", depth: 0},
		{kind: "remotebranch", name: "origin/plain", depth: 0},
		{kind: "folder", name: "upstream/feature", depth: 0},
		{kind: "remotebranch", name: "upstream/feature/c", depth: 1},
		{kind: "remotebranch", name: "upstream/main", depth: 0},
	})
	if _, ok := h.ui.Find("c"); !ok {
		t.Fatalf("upstream/feature/c row missing after collapsing origin's folder; texts=%q", h.ui.Texts())
	}
}

// A Shift-click range over remote-tracking branches follows the rows as
// shown: with origin's feature folder collapsed, a range reaching from the
// row before the folder to the row after it selects exactly the visible
// rows and never the hidden branches inside the folder.
func TestSidebarFoldersRemoteShiftRangeSkipsCollapsed(t *testing.T) {
	repo := newGitRepo(t, "fold-remote-range")
	repo.CommitFile("f.txt", "content\n", "initial commit")
	repo.Git("remote", "add", "origin", repo.Path())
	repo.Git("remote", "add", "upstream", repo.Path())
	for _, ref := range []string{
		"refs/remotes/origin/early", "refs/remotes/origin/feature/a", "refs/remotes/origin/feature/b", "refs/remotes/origin/plain",
		"refs/remotes/upstream/feature/c", "refs/remotes/upstream/main",
	} {
		repo.Git("update-ref", ref, "HEAD")
	}

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// origin shows, in order: early, the feature folder with a and b
	// inside, plain. Collapsing the folder hides a and b; the range from
	// early to plain then crosses exactly where they were.
	clickRefRow(t, h, "origin/feature")
	if sidebarShowsText(h, "a") {
		t.Fatal("origin/feature/a visible after collapsing origin/feature")
	}

	clickRefRow(t, h, "origin/early")
	shiftClickRefRow(t, h, "origin/plain")
	wantPickedRefs(t, h, nil, []string{"origin/early", "origin/plain"}, nil)
}

// Tags group by their slashed names exactly as branches do.
func TestSidebarFoldersGroupTags(t *testing.T) {
	repo := newGitRepo(t, "fold-tags")
	repo.CommitFile("f.txt", "content\n", "initial commit")
	for _, tag := range []string{"v1/rc1", "v1/rc2", "v2"} {
		repo.CreateTag(tag)
	}

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	wantSidebarRefRows(t, h, []sidebarRefRowSummary{
		{kind: "branch", name: "main", depth: 0},
		{kind: "folder", name: "v1", depth: 0},
		{kind: "tag", name: "v1/rc1", depth: 1},
		{kind: "tag", name: "v1/rc2", depth: 1},
		{kind: "tag", name: "v2", depth: 0},
	})
	wantFolderCount(t, h, "v1", 2)
}

// A reference row shows only the last slash segment of its name, while the
// accessible name stays the full one: the row of feature/ui/menu is found
// by its full name, a double click checks the branch out, and the
// right-click menu is the branch menu it always was.
func TestSidebarFoldersRowShowsLastSegment(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-segment")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The visible text is menu, never the full name.
	if !sidebarShowsText(h, "menu") {
		t.Fatalf("last-segment text menu missing; texts=%q", h.ui.Texts())
	}
	if sidebarShowsText(h, "feature/ui/menu") {
		t.Fatal("full branch name feature/ui/menu shows as visible text")
	}

	// The accessible row answers to the full name, and double-clicking it
	// checks the branch out.
	sidebarAccessInfo(t, h, "feature/ui/menu")
	doubleClickCheckout(t, h, "feature/ui/menu")
	if got := currentBranch(t, repo); got != "feature/ui/menu" {
		t.Fatalf("branch after double-clicking the grouped row = %q, want feature/ui/menu", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}

	// The row's context menu is the branch menu of the full name.
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.RightClick("feature/ui/menu"); err != nil {
		t.Fatalf("right-click feature/ui/menu: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	for _, want := range []string{"New Branch…", "Rename…", "Delete…", "Copy Branch Name"} {
		if !slices.Contains(menu, want) {
			t.Fatalf("branch menu %q missing %q", menu, want)
		}
	}
	if slices.Contains(menu, "Copy Branch Names") {
		t.Fatalf("batch item Copy Branch Names in the single-branch menu %q", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// Collapsing a folder hides its rows, and the choice survives closing the
// repository and opening it again from the same store: the reopened sidebar
// shows the folder collapsed. Expanding it there removes the folder from
// the repository's SidebarClosed settings.
func TestSidebarFoldersCollapsePersistsAcrossRestart(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-persist")
	storePath := filepath.Join(t.TempDir(), "repositories.json")

	h := folderTestHarnessAt(t, storePath)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "feature")
	if _, ok := h.ui.Find("menu"); ok {
		t.Fatal("feature/ui/menu row still visible after collapsing feature")
	}
	wantSidebarRefRows(t, h, []sidebarRefRowSummary{
		{kind: "folder", name: "bugfix", depth: 0},
		{kind: "branch", name: "bugfix/x", depth: 1},
		{kind: "folder", name: "feature", depth: 0},
		{kind: "branch", name: "main", depth: 0},
	})

	// Reopen the app on the same store file: the folder is still collapsed.
	h2 := folderTestHarnessAt(t, storePath)
	h2.openRepo(repo.StoreRepo())
	h2.drain()
	wantSidebarRefRows(t, h2, []sidebarRefRowSummary{
		{kind: "folder", name: "bugfix", depth: 0},
		{kind: "branch", name: "bugfix/x", depth: 1},
		{kind: "folder", name: "feature", depth: 0},
		{kind: "branch", name: "main", depth: 0},
	})

	// Expanding the folder removes it from the settings; a second reopen
	// shows it expanded with every row back.
	clickRefRow(t, h2, "feature")
	if _, ok := h2.ui.Find("menu"); !ok {
		t.Fatalf("feature/ui/menu row missing after expanding feature; texts=%q", h2.ui.Texts())
	}
	meta, ok := h2.store.Get(h2.app.session.Meta().ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if slices.Contains(meta.Local.SidebarClosed, "branches:feature") {
		t.Fatalf("branches:feature still in SidebarClosed after expanding: %q", meta.Local.SidebarClosed)
	}
}

// The collapse of the Branches and Stashes sections survives a restart:
// reopening the app on the same store shows both sections still collapsed,
// while the sections never touched stay expanded.
func TestSidebarFoldersSectionCollapsePersists(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-sections")
	repo.WriteFile("f.txt", "stashed\n")
	pushStashCLI(t, repo, "stashed work")
	storePath := filepath.Join(t.TempDir(), "repositories.json")

	h := folderTestHarnessAt(t, storePath)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("click Branches: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Stashes"); err != nil {
		t.Fatalf("click Stashes: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("main"); ok {
		t.Fatal("branch rows visible after collapsing Branches")
	}
	if _, ok := h.ui.Find("Stash stash@{0}"); ok {
		t.Fatal("stash rows visible after collapsing Stashes")
	}

	h2 := folderTestHarnessAt(t, storePath)
	h2.openRepo(repo.StoreRepo())
	h2.drain()
	if _, ok := h2.ui.Find("main"); ok {
		t.Fatal("branch rows visible after the restart even though Branches was collapsed")
	}
	if _, ok := h2.ui.Find("Stash stash@{0}"); ok {
		t.Fatal("stash rows visible after the restart even though Stashes was collapsed")
	}
	// Tags was never collapsed: its section stays expanded.
	if _, ok := h2.ui.Find("New Tag"); !ok {
		t.Fatal("Tags collapsed in the restart even though it was never collapsed")
	}

	meta, ok := h2.store.Get(h2.app.session.Meta().ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	sections := meta.Local.SidebarSections
	if sections.Branches == nil || *sections.Branches {
		t.Fatalf("SidebarSections.Branches = %+v, want a saved collapsed (expanded false)", sections.Branches)
	}
	if sections.Stashes == nil || *sections.Stashes {
		t.Fatalf("SidebarSections.Stashes = %+v, want a saved collapsed (expanded false)", sections.Stashes)
	}
}

// Collapsing the Branches section hides its rows without touching the
// selection: feature/a selected before the collapse is still selected after
// the section expands again, exactly as the flat sidebar always behaved.
func TestSidebarFoldersSectionCollapseKeepsSelection(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-section-selection")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "feature/a")
	wantPickedRefs(t, h, []string{"feature/a"}, nil, nil)

	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("collapse Branches: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("menu"); ok {
		t.Fatal("rows of feature visible after collapsing Branches")
	}
	wantPickedRefs(t, h, []string{"feature/a"}, nil, nil)

	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("expand Branches: %v", err)
	}
	h.ui.Frame()
	wantPickedRefs(t, h, []string{"feature/a"}, nil, nil)
}

// Collapsing a folder deselects the references inside it: of the selected
// feature/a and main only main survives. A Shift-click range reaching from
// bugfix/x to main across the collapsed folder selects exactly the visible
// rows, never the hidden branches inside feature.
func TestSidebarFoldersCollapseDropsSelection(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-selection")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "feature/a")
	cmdClickRefRow(t, h, "main")
	wantPickedRefs(t, h, []string{"feature/a", "main"}, nil, nil)

	clickRefRow(t, h, "feature")
	wantPickedRefs(t, h, []string{"main"}, nil, nil)

	// Shift-clicking from bugfix/x down to main crosses the collapsed
	// feature folder: the range covers the two visible ends and nothing
	// hidden inside the folder.
	clickRefRow(t, h, "bugfix/x")
	shiftClickRefRow(t, h, "main")
	wantPickedRefs(t, h, []string{"bugfix/x", "main"}, nil, nil)
}

// A folder collapsed in one repository has no effect on another: the
// same-named folder of the second repository shows expanded with its rows
// visible.
func TestSidebarFoldersCollapseIsPerRepository(t *testing.T) {
	repoA := sidebarFolderFixture(t, "fold-repos-a")
	repoB := sidebarFolderFixture(t, "fold-repos-b")
	storePath := filepath.Join(t.TempDir(), "repositories.json")

	h := folderTestHarnessAt(t, storePath)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	clickRefRow(t, h, "feature")
	if _, ok := h.ui.Find("menu"); ok {
		t.Fatal("feature/ui/menu of repository A visible after collapsing feature")
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()
	if _, ok := h.ui.Find("menu"); !ok {
		t.Fatalf("feature/ui/menu of repository B hidden by the collapse in repository A; texts=%q", h.ui.Texts())
	}
}

// When the settings write fails the folder still collapses for this opening
// — the user's choice is never ignored — and the failure surfaces as the
// settings toast.
func TestSidebarFoldersWriteFailureKeepsCollapse(t *testing.T) {
	repo := sidebarFolderFixture(t, "fold-writefail")
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	storePath := filepath.Join(storeDir, "repositories.json")

	h := folderTestHarnessAt(t, storePath)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}

	clickRefRow(t, h, "feature")
	if _, ok := h.ui.Find("menu"); ok {
		t.Fatal("feature/ui/menu row visible after the collapse the store could not save")
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// sidebarFolderUpdateRefs feeds the create directives of refs to one git
// update-ref --stdin call in the fixture's repository, so thousands of
// references cost one process.
func sidebarFolderUpdateRefs(t *testing.T, repo *gitRepoFixture, refs string) {
	t.Helper()
	cmd := exec.Command("git", "update-ref", "--stdin")
	cmd.Dir = repo.Path()
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(refs)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v\n%s", err, out)
	}
}

// Frames of an unchanged sidebar never rebuild the row table: with 2000
// branches grouped into 200 folders, repeated frames return the very slice
// the one rebuild produced. The rebuild itself is measured directly, so the
// report carries what one grouping of that size costs.
func TestSidebarFoldersFrameCostNoRebuild(t *testing.T) {
	repo := newGitRepo(t, "fold-cost")
	repo.CommitFile("f.txt", "content\n", "initial commit")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	var creates strings.Builder
	for folder := 0; folder < 200; folder++ {
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&creates, "create refs/heads/f%03d/b%04d %s\n", folder, i, head)
		}
	}
	sidebarFolderUpdateRefs(t, repo, creates.String())

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	rows := h.app.refsView.sidebarRows.rows
	if len(rows) == 0 {
		t.Fatal("row table empty after opening the repository")
	}
	first := &rows[0]
	rebuilds := 0
	for i := 0; i < 6; i++ {
		h.ui.Frame()
		current := h.app.refsView.sidebarRows.rows
		if len(current) == 0 || &current[0] != first {
			rebuilds++
			first = &current[0]
		}
	}
	if rebuilds != 0 {
		t.Fatalf("row table rebuilt %d times over 6 unchanged frames, want 0", rebuilds)
	}

	// Measure one rebuild of the 2000-branch, 200-folder table directly.
	rc := &h.app.refsView.sidebarRows
	refs := h.app.session.Snapshot().Refs
	stashes := h.app.session.Snapshot().Stashes
	localSorted := h.app.sortedLocalBranches(refs.Branches)
	remoteSorted := h.app.sortedRemoteBranches(refs.Remote)
	tagSorted := h.app.sortedTags(refs.Tags)
	stash := h.app.stashUI()
	const runs = 7
	var times []time.Duration
	for i := 0; i < runs; i++ {
		start := time.Now()
		h.app.rebuildSidebarRows(rc, refs, stashes, localSorted, remoteSorted, tagSorted, stash)
		times = append(times, time.Since(start))
	}
	slices.Sort(times)
	t.Logf("rebuild of 2000 branches in 200 folders: median %v, runs %v", times[runs/2], times)
}

// Collapsing a folder in one section only prunes selections of that reference
// kind: collapsing tag folder v1 leaves a selected local branch v1/a selected.
func TestSidebarFoldersCollapseScopeIsolated(t *testing.T) {
	repo := newGitRepo(t, "fold-scope-isolated")
	repo.CommitFile("f.txt", "content\n", "initial commit")
	repo.CreateBranch("v1/a")
	repo.CreateTag("v1/rc1")

	h := newTestApp(t)
	pinAlphabeticalSortOrders(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	clickRefRow(t, h, "v1/a")
	wantPickedRefs(t, h, []string{"v1/a"}, nil, nil)

	// Collapse Branches so the only visible "v1" folder row is under Tags;
	// section collapse keeps the branch selection intact.
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("collapse Branches: %v", err)
	}
	h.ui.Frame()
	clickRefRow(t, h, "v1")
	if sidebarShowsText(h, "rc1") {
		t.Fatal("tag v1/rc1 still visible after collapsing tag folder v1")
	}

	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("expand Branches: %v", err)
	}
	h.ui.Frame()
	wantPickedRefs(t, h, []string{"v1/a"}, nil, nil)
}
