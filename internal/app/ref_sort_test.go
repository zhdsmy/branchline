package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// newRefSortTestApp builds the harness with the window drawing its own
// menus: only then are the menu items and their check marks part of the
// frame, where assertions can reach them.
func newRefSortTestApp(t *testing.T, repos ...store.Repo) *testHarness {
	t.Helper()
	h := newTestApp(t, repos...)
	h.ui = ui.NewTester(func(c *ui.Context) {
		c.SetMenuPresentation(ui.PopoverMenus)
		h.app.view(c)
	}, 1200, 800)
	h.ui.Frame()
	return h
}

// refSortFixture builds a repository exercising every grouping of the sort
// orders: main and master side by side, branches with and without a slash,
// mixed-case tags including an annotated one and one with a slash, and two
// remotes whose branches sort differently per order.
func refSortFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CreateBranch("master")
	repo.CreateBranch("alpha")
	repo.CreateBranch("zeta")
	repo.CreateBranch("release/one")
	repo.CreateBranch("feature/two")

	repo.Git("tag", "v2")
	repo.Git("tag", "release/r1")
	repo.Git("tag", "nightly")
	repo.Git("tag", "-a", "-m", "annotated v1", "v1")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGitCommand(t, "", "init", "--bare", originDir)
	repo.Git("remote", "add", "origin", originDir)
	repo.Git("push", "-q", "origin", "alpha:refs/heads/alpha", "zeta:refs/heads/zeta", "master:refs/heads/release/x")

	upstreamDir := filepath.Join(t.TempDir(), "upstream.git")
	runGitCommand(t, "", "init", "--bare", upstreamDir)
	repo.Git("remote", "add", "upstream", upstreamDir)
	repo.Git("push", "-q", "upstream", "alpha:refs/heads/beta", "master:refs/heads/feature/y")

	repo.Git("fetch", "-q", "origin")
	repo.Git("fetch", "-q", "upstream")
	return repo
}

// sidebarRowOrder lists the accessible labels of the sidebar's list rows in
// the order they render, top to bottom. The virtual list wraps every row
// it builds in its own item node, which takes the row's name from its
// content but never the focus: a row whose content keeps the row's own
// focusable element — as the sidebar's reference rows do — would be
// counted twice, once for the wrapper and once for the row inside it. A
// non-focusable item node is therefore skipped when a focusable element
// of the same name sits inside its bounds, so every such row counts once,
// as the row it wraps. Rows of one name are stacked without overlap, so
// the bounds of a row never hold a like-named row of another entry.
func sidebarRowOrder(t *testing.T, h *testHarness) []string {
	t.Helper()
	all := h.ui.AccessAll()
	type row struct {
		y    float32
		name string
	}
	var rows []row
outer:
	for _, a := range all {
		if a.Role != ui.RoleListItem {
			continue
		}
		if !a.Focusable && a.Name != "" {
			for _, b := range all {
				if !b.Focusable || b.Name != a.Name {
					continue
				}
				ab, bb := a.Bounds, b.Bounds
				if bb.X >= ab.X-1 && bb.Y >= ab.Y-1 && bb.X+bb.W <= ab.X+ab.W+1 && bb.Y+bb.H <= ab.Y+ab.H+1 {
					continue outer
				}
			}
		}
		rows = append(rows, row{a.Bounds.Y, a.Name})
	}
	slices.SortStableFunc(rows, func(x, y row) int {
		switch {
		case x.y < y.y:
			return -1
		case x.y > y.y:
			return 1
		}
		return strings.Compare(x.name, y.name)
	})
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
	}
	return out
}

// assertSidebarOrder fails the test unless the rows of the sidebar list the
// wanted names in exactly the wanted order, ignoring every other row.
func assertSidebarOrder(t *testing.T, h *testHarness, want ...string) {
	t.Helper()
	rows := sidebarRowOrder(t, h)
	set := map[string]bool{}
	for _, name := range want {
		set[name] = true
	}
	var got []string
	for _, r := range rows {
		if set[r] {
			got = append(got, r)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sidebar order of %q = %q, want %q (all rows %q)", want, got, want, rows)
	}
}

// openSectionMenu right-clicks a sidebar section header and fails the test
// when no menu opens.
func openSectionMenu(t *testing.T, h *testHarness, section string) {
	t.Helper()
	if err := h.ui.RightClick(section); err != nil {
		t.Fatalf("right-click %s header: %v\ntexts=%q", section, err, h.ui.Texts())
	}
	h.ui.Frame()
	if len(h.ui.Menu()) == 0 {
		t.Fatalf("no context menu on the %s header; texts=%q", section, h.ui.Texts())
	}
}

// assertSortMenu fails the test unless the open menu is exactly the wanted
// labels, the heading item is disabled, and of the sort options exactly the
// current one carries a check mark.
func assertSortMenu(t *testing.T, h *testHarness, wantMenu []string, heading, checked string, options []string) {
	t.Helper()
	if menu := h.ui.Menu(); !slices.Equal(menu, wantMenu) {
		t.Fatalf("section menu = %q, want exactly %q", menu, wantMenu)
	}
	head, ok := menuItemAccess(h, heading)
	if !ok {
		t.Fatalf("heading item %q missing from the menu; texts=%q", heading, h.ui.Texts())
	}
	if !head.Disabled {
		t.Fatalf("heading item %q is enabled, want disabled", heading)
	}
	for _, label := range options {
		item, ok := menuItemAccess(h, label)
		if !ok {
			t.Fatalf("sort option %q missing from the menu; texts=%q", label, h.ui.Texts())
		}
		want := label == checked
		if item.Checked != want {
			t.Fatalf("sort option %q Checked = %v, want %v (current %q)", label, item.Checked, want, checked)
		}
	}
}

// chooseSortOption chooses a sort option of the open section menu and
// renders the frame carrying the reordered sidebar.
func chooseSortOption(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.ChooseMenuItem(label); err != nil {
		t.Fatalf("choose %q: %v", label, err)
	}
	h.ui.Frame()
}

// --- Pure ordering ---

// sortRefNames orders case-insensitively with a case-sensitive tie-break,
// groups the exact main and master branches and the slashed names the way
// each stored value asks, and maps unknown values onto alphabetical.
func TestRefSortNamesTable(t *testing.T) {
	input := []string{"zeta", "mainly", "Alpha", "beta", "feature/two", "main", "alpha", "master", "release/one"}
	alpha := []string{"Alpha", "alpha", "beta", "feature/two", "main", "mainly", "master", "release/one", "zeta"}
	for _, tc := range []struct {
		order string
		want  []string
	}{
		{"", alpha},
		{"backward", []string{"zeta", "release/one", "master", "mainly", "main", "feature/two", "beta", "alpha", "Alpha"}},
		{"alpha-master-folders", []string{"main", "master", "feature/two", "release/one", "Alpha", "alpha", "beta", "mainly", "zeta"}},
		{"backward-master-folders", []string{"master", "main", "release/one", "feature/two", "zeta", "mainly", "beta", "alpha", "Alpha"}},
		{"alpha-folders", []string{"feature/two", "release/one", "Alpha", "alpha", "beta", "main", "mainly", "master", "zeta"}},
		{"backward-folders", []string{"release/one", "feature/two", "zeta", "master", "mainly", "main", "beta", "alpha", "Alpha"}},
		{"date", alpha},
		{"recent", alpha},
		{"nonsense", alpha},
	} {
		before := slices.Clone(input)
		got := sortRefNames(input, tc.order)
		if !slices.Equal(got, tc.want) {
			t.Errorf("sortRefNames order %q = %q, want %q", tc.order, got, tc.want)
		}
		if !slices.Equal(input, before) {
			t.Errorf("sortRefNames order %q mutated its input: %q, was %q", tc.order, input, before)
		}
		if len(got) == len(input) && len(input) > 0 && &got[0] == &input[0] {
			t.Errorf("sortRefNames order %q returned the input slice itself", tc.order)
		}
	}
}

// A branch named feature/main is a folder, never part of the master group:
// only a branch named exactly main or master goes first.
func TestRefSortMasterGroupExactNamesOnly(t *testing.T) {
	got := sortRefNames([]string{"feature/main", "aaa", "feature/master"}, "alpha-master-folders")
	want := []string{"feature/main", "feature/master", "aaa"}
	if !slices.Equal(got, want) {
		t.Fatalf("sortRefNames alpha-master-folders = %q, want %q", got, want)
	}
	got = sortRefNames([]string{"Main", "MASTER", "aaa"}, "alpha-master-folders")
	want = []string{"aaa", "Main", "MASTER"}
	if !slices.Equal(got, want) {
		t.Fatalf("capitalized Main/MASTER must not join the master group: got %q, want %q", got, want)
	}
}

// sortRemoteBranches keys on the branch name without the remote prefix, so
// the slash of the prefix never makes every remote branch a folder.
func TestRefSortRemoteBranchesStripPrefix(t *testing.T) {
	input := []git.Branch{
		{Name: "origin/alpha", FullName: "refs/remotes/origin/alpha", Remote: "origin"},
		{Name: "up/zzz", FullName: "refs/remotes/up/zzz", Remote: "up"},
		{Name: "origin/release/x", FullName: "refs/remotes/origin/release/x", Remote: "origin"},
	}
	before := slices.Clone(input)
	got := sortRemoteBranches(input, "alpha-folders")
	var names []string
	for _, rb := range got {
		names = append(names, rb.Name)
	}
	want := []string{"origin/release/x", "origin/alpha", "up/zzz"}
	if !slices.Equal(names, want) {
		t.Fatalf("sortRemoteBranches alpha-folders = %q, want %q", names, want)
	}
	if !slices.Equal(input, before) {
		t.Fatalf("sortRemoteBranches mutated its input: %+v, was %+v", input, before)
	}

	gotAlpha := sortRemoteBranches(input, "")
	names = names[:0]
	for _, rb := range gotAlpha {
		names = append(names, rb.Name)
	}
	wantAlpha := []string{"origin/alpha", "origin/release/x", "up/zzz"}
	if !slices.Equal(names, wantAlpha) {
		t.Fatalf("sortRemoteBranches alphabetical = %q, want %q", names, wantAlpha)
	}
}

// sortBranches and sortTags copy before sorting, like sortRefNames does.
func TestRefSortTypedCopiesNeverMutateInput(t *testing.T) {
	branches := []git.Branch{{Name: "zeta"}, {Name: "alpha"}, {Name: "main"}}
	branchesBefore := slices.Clone(branches)
	sorted := sortBranches(branches, "")
	if !slices.Equal(branches, branchesBefore) {
		t.Fatalf("sortBranches mutated its input: %+v, was %+v", branches, branchesBefore)
	}
	if sorted[0].Name != "alpha" || sorted[1].Name != "main" || sorted[2].Name != "zeta" {
		t.Fatalf("sortBranches alphabetical = %+v", sorted)
	}

	tags := []git.Tag{{Name: "v2"}, {Name: "nightly"}}
	tagsBefore := slices.Clone(tags)
	sortedTags := sortTags(tags, "backward")
	if !slices.Equal(tags, tagsBefore) {
		t.Fatalf("sortTags mutated its input: %+v, was %+v", tags, tagsBefore)
	}
	if sortedTags[0].Name != "v2" || sortedTags[1].Name != "nightly" {
		t.Fatalf("sortTags backward = %+v", sortedTags)
	}
}

// --- Menus ---

// The Branches section header menu is exactly New Branch, a separator, the
// disabled Sort Branches heading and the four orders, with the current order
// checked: Alphabetically backward on a fresh store.
func TestRefSortBranchesMenuContent(t *testing.T) {
	repo := refSortFixture(t, "sort-menu-branches")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openSectionMenu(t, h, "Branches")
	options := []string{"Alphabetically", "Alphabetically, master and folders on top", "Alphabetically backward", "Alphabetically backward, master and folders on top"}
	want := append([]string{"New Branch…", "-", "Sort Branches:"}, options...)
	assertSortMenu(t, h, want, "Sort Branches:", "Alphabetically backward", options)
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The Remotes section header menu is exactly Add New Remote, a separator,
// the disabled Sort Branches heading and the three orders, with
// Alphabetically, folders on top checked on a fresh store.
func TestRefSortRemotesMenuContent(t *testing.T) {
	repo := refSortFixture(t, "sort-menu-remotes")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openSectionMenu(t, h, "Remotes")
	options := []string{"Alphabetically", "Alphabetically, folders on top", "Alphabetically backward, folders on top"}
	want := append([]string{"Add New Remote…", "-", "Sort Branches:"}, options...)
	assertSortMenu(t, h, want, "Sort Branches:", "Alphabetically, folders on top", options)
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The Tags section header menu is exactly New Tag, a separator, the disabled
// Sort Tags heading and the four orders, with Alphabetically checked on a
// fresh store.
func TestRefSortTagsMenuContent(t *testing.T) {
	repo := refSortFixture(t, "sort-menu-tags")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openSectionMenu(t, h, "Tags")
	options := []string{"Alphabetically", "Alphabetically, folders on top", "Alphabetically backward", "Alphabetically backward, folders on top"}
	want := append([]string{"New Tag…", "-", "Sort Tags:"}, options...)
	assertSortMenu(t, h, want, "Sort Tags:", "Alphabetically", options)
	h.ui.CloseMenu()
	h.ui.Frame()
}

// --- Choosing reorders the sidebar ---

// Choosing each order of the Branches section menu reorders the local
// branches of the sidebar accordingly, starting from the backward default
// of a fresh store.
func TestRefSortBranchesOrderChoices(t *testing.T) {
	repo := refSortFixture(t, "sort-order-branches")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// A fresh store sorts local branches backward.
	assertSidebarOrder(t, h, "zeta", "release/one", "master", "main", "feature/two", "alpha")

	steps := []struct {
		label string
		want  []string
	}{
		{"Alphabetically", []string{"alpha", "feature/two", "main", "master", "release/one", "zeta"}},
		{"Alphabetically, master and folders on top", []string{"main", "master", "feature/two", "release/one", "alpha", "zeta"}},
		{"Alphabetically backward", []string{"zeta", "release/one", "master", "main", "feature/two", "alpha"}},
		{"Alphabetically backward, master and folders on top", []string{"master", "main", "release/one", "feature/two", "zeta", "alpha"}},
	}
	for _, step := range steps {
		openSectionMenu(t, h, "Branches")
		chooseSortOption(t, h, step.label)
		assertSidebarOrder(t, h, step.want...)
	}

	// The last choice is the one the menu checks now.
	openSectionMenu(t, h, "Branches")
	options := []string{"Alphabetically", "Alphabetically, master and folders on top", "Alphabetically backward", "Alphabetically backward, master and folders on top"}
	want := append([]string{"New Branch…", "-", "Sort Branches:"}, options...)
	assertSortMenu(t, h, want, "Sort Branches:", "Alphabetically backward, master and folders on top", options)
	h.ui.CloseMenu()
	h.ui.Frame()
}

// Choosing each order of the Remotes section menu reorders the branches of
// every remote independently — the remotes themselves keep their order —
// keyed on the branch names without their remote prefix.
func TestRefSortRemotesOrderChoices(t *testing.T) {
	repo := refSortFixture(t, "sort-order-remotes")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	assertRemoteOrder := func(origin, upstream []string) {
		t.Helper()
		assertSidebarOrder(t, h, origin...)
		assertSidebarOrder(t, h, upstream...)
		// The remotes themselves never reorder: origin stays before upstream.
		assertSidebarOrder(t, h, "origin", "upstream")
	}

	// A fresh store sorts remote branches alphabetically with folders on
	// top, per remote.
	assertRemoteOrder(
		[]string{"origin/release/x", "origin/alpha", "origin/zeta"},
		[]string{"upstream/feature/y", "upstream/beta"},
	)

	openSectionMenu(t, h, "Remotes")
	chooseSortOption(t, h, "Alphabetically")
	assertRemoteOrder(
		[]string{"origin/alpha", "origin/release/x", "origin/zeta"},
		[]string{"upstream/beta", "upstream/feature/y"},
	)

	openSectionMenu(t, h, "Remotes")
	chooseSortOption(t, h, "Alphabetically backward, folders on top")
	assertRemoteOrder(
		[]string{"origin/release/x", "origin/zeta", "origin/alpha"},
		[]string{"upstream/feature/y", "upstream/beta"},
	)

	openSectionMenu(t, h, "Remotes")
	chooseSortOption(t, h, "Alphabetically, folders on top")
	assertRemoteOrder(
		[]string{"origin/release/x", "origin/alpha", "origin/zeta"},
		[]string{"upstream/feature/y", "upstream/beta"},
	)
}

// Choosing each order of the Tags section menu reorders the tags of the
// sidebar accordingly, the annotated tag and the slashed tag among them.
func TestRefSortTagsOrderChoices(t *testing.T) {
	repo := refSortFixture(t, "sort-order-tags")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// A fresh store sorts tags alphabetically.
	assertSidebarOrder(t, h, "nightly", "release/r1", "v1", "v2")

	steps := []struct {
		label string
		want  []string
	}{
		{"Alphabetically, folders on top", []string{"release/r1", "nightly", "v1", "v2"}},
		{"Alphabetically backward", []string{"v2", "v1", "release/r1", "nightly"}},
		{"Alphabetically backward, folders on top", []string{"release/r1", "v2", "v1", "nightly"}},
		{"Alphabetically", []string{"nightly", "release/r1", "v1", "v2"}},
	}
	for _, step := range steps {
		openSectionMenu(t, h, "Tags")
		chooseSortOption(t, h, step.label)
		assertSidebarOrder(t, h, step.want...)
	}
}

// --- Display-only writes ---

// The sort options stay enabled while a repository write is in flight:
// sorting changes nothing but the display.
func TestRefSortChoosableWhileBusy(t *testing.T) {
	repo := refSortFixture(t, "sort-busy")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	doubleClick(t, h.ui, "alpha")
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the checkout worker is held")
	}

	openSectionMenu(t, h, "Branches")
	chooseSortOption(t, h, "Alphabetically")
	assertSidebarOrder(t, h, "alpha", "feature/two", "main", "master", "release/one", "zeta")

	h.queue.ReleaseWorker(write)
	h.queue.ResumeWorkers()
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}
}

// Sorting never reorders the session snapshot itself: the worker and the
// other views keep the references in the order git reported.
func TestRefSortSnapshotSlicesNeverMutated(t *testing.T) {
	repo := refSortFixture(t, "sort-snapshot")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	names := func() (branches, remote, tags []string) {
		refs := h.app.session.Snapshot().Refs
		for _, br := range refs.Branches {
			branches = append(branches, br.Name)
		}
		for _, rb := range refs.Remote {
			remote = append(remote, rb.Name)
		}
		for _, tag := range refs.Tags {
			tags = append(tags, tag.Name)
		}
		return branches, remote, tags
	}
	branchesBefore, remoteBefore, tagsBefore := names()

	// The fixture's git order is not the backward default, so an unchanged
	// snapshot is observable rather than accidental.
	assertSidebarOrder(t, h, "zeta", "release/one", "master", "main", "feature/two", "alpha")
	if slices.Equal(branchesBefore, []string{"zeta", "release/one", "master", "main", "feature/two", "alpha"}) {
		t.Fatalf("fixture premise broken: the snapshot itself is already backward ordered: %q", branchesBefore)
	}

	for _, step := range []struct{ section, label string }{
		{"Branches", "Alphabetically"},
		{"Branches", "Alphabetically backward, master and folders on top"},
		{"Remotes", "Alphabetically backward, folders on top"},
		{"Tags", "Alphabetically backward"},
	} {
		openSectionMenu(t, h, step.section)
		chooseSortOption(t, h, step.label)
	}
	h.ui.Frame()

	branchesAfter, remoteAfter, tagsAfter := names()
	if !slices.Equal(branchesAfter, branchesBefore) {
		t.Fatalf("snapshot branches reordered: %q, was %q", branchesAfter, branchesBefore)
	}
	if !slices.Equal(remoteAfter, remoteBefore) {
		t.Fatalf("snapshot remote branches reordered: %q, was %q", remoteAfter, remoteBefore)
	}
	if !slices.Equal(tagsAfter, tagsBefore) {
		t.Fatalf("snapshot tags reordered: %q, was %q", tagsAfter, tagsBefore)
	}
}

// --- Stored values ---

// A stored value the menu does not offer — a leftover of an older version —
// sorts and checks as Alphabetically without rewriting the stored value.
func TestRefSortInvalidStoredValuesReadAsAlphabetical(t *testing.T) {
	repo := refSortFixture(t, "sort-invalid")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	settings := h.store.Settings()
	settings.LocalBranchSort = "date"
	settings.RemoteBranchSort = "recent"
	settings.TagSort = "nonsense"
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("store invalid sort values: %v", err)
	}
	h.ui.Frame()

	assertSidebarOrder(t, h, "alpha", "feature/two", "main", "master", "release/one", "zeta")
	assertSidebarOrder(t, h, "origin/alpha", "origin/release/x", "origin/zeta")
	assertSidebarOrder(t, h, "nightly", "release/r1", "v1", "v2")

	openSectionMenu(t, h, "Branches")
	branchOptions := []string{"Alphabetically", "Alphabetically, master and folders on top", "Alphabetically backward", "Alphabetically backward, master and folders on top"}
	assertSortMenu(t, h, append([]string{"New Branch…", "-", "Sort Branches:"}, branchOptions...), "Sort Branches:", "Alphabetically", branchOptions)
	h.ui.CloseMenu()
	h.ui.Frame()

	openSectionMenu(t, h, "Tags")
	tagOptions := []string{"Alphabetically", "Alphabetically, folders on top", "Alphabetically backward", "Alphabetically backward, folders on top"}
	assertSortMenu(t, h, append([]string{"New Tag…", "-", "Sort Tags:"}, tagOptions...), "Sort Tags:", "Alphabetically", tagOptions)
	h.ui.CloseMenu()
	h.ui.Frame()

	got := h.store.Settings()
	if got.LocalBranchSort != "date" || got.RemoteBranchSort != "recent" || got.TagSort != "nonsense" {
		t.Fatalf("invalid stored values were rewritten: %+v", got)
	}
}

// The chosen orders survive a restart: reopening the store and building the
// app again shows the same sidebar order and the same check mark.
func TestRefSortChoicesPersistAcrossRestart(t *testing.T) {
	repo := refSortFixture(t, "sort-persist")

	storeDir := t.TempDir()
	storePath := filepath.Join(storeDir, "repositories.json")

	buildHarness := func(t *testing.T) *testHarness {
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
		h.ui = ui.NewTester(func(c *ui.Context) {
			c.SetMenuPresentation(ui.PopoverMenus)
			h.app.view(c)
		}, 1200, 800)
		h.ui.Frame()
		return h
	}

	h := buildHarness(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openSectionMenu(t, h, "Branches")
	chooseSortOption(t, h, "Alphabetically backward, master and folders on top")
	openSectionMenu(t, h, "Tags")
	chooseSortOption(t, h, "Alphabetically backward, folders on top")

	// Restart: reopen the store file and build the app again.
	h2 := buildHarness(t)
	h2.openRepo(repo.StoreRepo())
	h2.drain()

	assertSidebarOrder(t, h2, "master", "main", "release/one", "feature/two", "zeta", "alpha")
	assertSidebarOrder(t, h2, "release/r1", "v2", "v1", "nightly")

	openSectionMenu(t, h2, "Branches")
	branchOptions := []string{"Alphabetically", "Alphabetically, master and folders on top", "Alphabetically backward", "Alphabetically backward, master and folders on top"}
	assertSortMenu(t, h2, append([]string{"New Branch…", "-", "Sort Branches:"}, branchOptions...), "Sort Branches:", "Alphabetically backward, master and folders on top", branchOptions)
	h2.ui.CloseMenu()
	h2.ui.Frame()

	if got := h2.store.Settings().TagSort; got != "backward-folders" {
		t.Fatalf("persisted TagSort = %q, want backward-folders", got)
	}
}

// When the settings save fails the sidebar order stays unchanged and the
// failure surfaces as a toast.
func TestRefSortSaveFailureKeepsOrder(t *testing.T) {
	repo := refSortFixture(t, "sort-savefail")

	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{t: t, app: a, store: s, queue: q}
	h.ui = ui.NewTester(func(c *ui.Context) {
		c.SetMenuPresentation(ui.PopoverMenus)
		h.app.view(c)
	}, 1200, 800)
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	backward := []string{"zeta", "release/one", "master", "main", "feature/two", "alpha"}
	assertSidebarOrder(t, h, backward...)

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}

	openSectionMenu(t, h, "Branches")
	chooseSortOption(t, h, "Alphabetically")

	assertSidebarOrder(t, h, backward...)
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
	if got := h.store.Settings().LocalBranchSort; got != "backward" {
		t.Fatalf("LocalBranchSort after the failed save = %q, want the unchanged backward", got)
	}
}

// --- Loopback SSH ---

// On a loopback SSH server repository the sort choice reorders the sidebar
// exactly as it does for a local one.
func TestRefSortSidebarOrderLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	srv.RunGit(t, repoDir, "branch", "alpha")
	srv.RunGit(t, repoDir, "branch", "zeta")

	h := newRefSortTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// The fresh-store default is backward: zeta, main, alpha.
	assertSidebarOrder(t, h, "zeta", "main", "alpha")

	openSectionMenu(t, h, "Branches")
	chooseSortOption(t, h, "Alphabetically")
	assertSidebarOrder(t, h, "alpha", "main", "zeta")

	openSectionMenu(t, h, "Branches")
	chooseSortOption(t, h, "Alphabetically backward")
	assertSidebarOrder(t, h, "zeta", "main", "alpha")

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected action error: %v", err)
	}
}

// BenchmarkRefSort5000 measures one sort of 5000 branch names exercising
// every grouping: mixed case, slashes, and a main in the list.
func BenchmarkRefSort5000(b *testing.B) {
	names := make([]string, 5000)
	for i := range names {
		switch i % 5 {
		case 0:
			names[i] = "feature/item" + itoa5(i)
		case 1:
			names[i] = "Release/Candidate" + itoa5(i)
		case 2:
			names[i] = "plainbranch" + itoa5(i)
		case 3:
			names[i] = "UPPER" + itoa5(i)
		default:
			names[i] = "lower" + itoa5(i)
		}
	}
	names[0] = "main"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkStrings = sortRefNames(names, "backward-master-folders")
	}
}

var sinkStrings []string

// itoa5 formats i as a zero-padded five digit decimal string.
func itoa5(i int) string {
	var buf [5]byte
	for p := 4; p >= 0; p-- {
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[:])
}

// --- Sort cache ---

// Repeated reads of an unchanged snapshot with unchanged settings return
// the very slice the first sort produced — the cache hits for the local
// branches, the remote branches and the tags alike.
func TestRefSortCacheHitReturnsSameBackingArray(t *testing.T) {
	repo := refSortFixture(t, "sort-cache-hit")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	refs := h.app.session.Snapshot().Refs

	localFirst := h.app.sortedLocalBranches(refs.Branches)
	localSecond := h.app.sortedLocalBranches(refs.Branches)
	if &localFirst[0] != &localSecond[0] {
		t.Fatal("second sortedLocalBranches call returned a different backing array: the cache did not hit")
	}

	remoteFirst := h.app.sortedRemoteBranches(refs.Remote)
	remoteSecond := h.app.sortedRemoteBranches(refs.Remote)
	if &remoteFirst[0] != &remoteSecond[0] {
		t.Fatal("second sortedRemoteBranches call returned a different backing array: the cache did not hit")
	}

	tagsFirst := h.app.sortedTags(refs.Tags)
	tagsSecond := h.app.sortedTags(refs.Tags)
	if &tagsFirst[0] != &tagsSecond[0] {
		t.Fatal("second sortedTags call returned a different backing array: the cache did not hit")
	}
}

// The cache keys on the order as well as the snapshot: changing the setting
// re-sorts the very same snapshot slice, and the new order shows.
func TestRefSortCacheInvalidatesOnSettingsChange(t *testing.T) {
	repo := refSortFixture(t, "sort-cache-settings")

	h := newRefSortTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	refs := h.app.session.Snapshot().Refs
	backward := h.app.sortedLocalBranches(refs.Branches)
	if backward[0].Name != "zeta" {
		t.Fatalf("fresh-store backward order starts with %q, want zeta", backward[0].Name)
	}

	if err := h.app.saveLocalBranchSort(""); err != nil {
		t.Fatalf("save alphabetical order: %v", err)
	}
	alpha := h.app.sortedLocalBranches(refs.Branches)
	if alpha[0].Name != "alpha" {
		t.Fatalf("order after switching to alphabetical starts with %q, want alpha: the cache did not invalidate", alpha[0].Name)
	}
	if &backward[0] == &alpha[0] {
		t.Fatal("changed settings returned the cached backing array")
	}
}

// A new snapshot brings new slices, which the cache never confuses with the
// old ones: a branch created outside the app lands in its sorted position
// once the reload after a write picks it up.
func TestRefSortCacheInvalidatesOnNewSnapshot(t *testing.T) {
	repo := refSortFixture(t, "sort-cache-snapshot")

	h := newRefSortTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	assertSidebarOrder(t, h, "zeta", "release/one", "master", "main", "feature/two", "alpha")

	// Created outside the app: nothing in the app knows, so the sidebar
	// still shows the old snapshot until a reload.
	repo.CreateBranch("midway")
	h.ui.Frame()
	rows := sidebarRowOrder(t, h)
	if slices.Contains(rows, "midway") {
		t.Fatal("midway visible before any reload: the premise of an external change is broken")
	}

	// Fetch is a write of the toolbar that triggers a full reload even when
	// it downloads nothing new.
	if err := h.ui.Click("Fetch"); err != nil {
		t.Fatalf("click Fetch: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}

	// backward order: midway sorts between release/one and master.
	assertSidebarOrder(t, h, "zeta", "release/one", "midway", "master", "main", "feature/two", "alpha")

	// The new sorted list must come from a fresh sort of the new snapshot,
	// never the cached copy of the old one.
	refs := h.app.session.Snapshot().Refs
	current := h.app.sortedLocalBranches(refs.Branches)
	names := make([]string, len(current))
	for i, br := range current {
		names[i] = br.Name
	}
	want := []string{"zeta", "release/one", "midway", "master", "main", "feature/two", "alpha"}
	if !slices.Equal(names, want) {
		t.Fatalf("sorted branches of the new snapshot = %q, want %q", names, want)
	}
}
