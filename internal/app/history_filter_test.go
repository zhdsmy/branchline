package app

import (
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// newTestAppAtStore mirrors newTestApp but opens the store file at storePath
// instead of a fresh temporary one, so a harness can reopen the settings a
// previous harness wrote, as the restarted app reads them.
func newTestAppAtStore(t *testing.T, storePath string) *testHarness {
	t.Helper()
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("reopen test store %s: %v", storePath, err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(instantView(a.view), 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	return h
}

// historyFilterRepo builds the two-branch fixture the filter tests share:
// base and main work on main, side work only on the feature branch, with
// main checked out. It returns the feature-only commit's hash.
func historyFilterRepo(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	featureHash := repo.CommitFile("feat.txt", "feat\n", "feature only work")
	repo.Checkout("main")
	repo.CommitFile("main.txt", "main\n", "main work")
	return repo, featureHash
}

// driveHistoryBranchSelect switches the history branch filter to the option
// at toIdx (0 is All Branches, 1 Current Branch) through the real select
// control, draining the snapshot reload the change starts.
func driveHistoryBranchSelect(t *testing.T, h *testHarness, toIdx int) {
	t.Helper()
	fromIdx := 0
	if h.app.session.HistoryCurrentBranch() {
		fromIdx = 1
	}
	if fromIdx == toIdx {
		t.Fatalf("branch filter already at option %d: the test proves nothing", toIdx)
	}
	driveSelect(t, h, "History Branches", fromIdx, toIdx)
	h.drain()
}

// historySubjects lists the subjects of the loaded snapshot commits.
func historySubjects(h *testHarness) []string {
	var out []string
	for _, cm := range h.app.session.Snapshot().Commits {
		out = append(out, cm.Subject)
	}
	return out
}

// TestHistoryFilterCurrentBranch checks the branch scope drop-down: All
// Branches lists the commits of every branch, Current Branch only the ones
// HEAD reaches, and switching back restores the full list.
func TestHistoryFilterCurrentBranch(t *testing.T) {
	repo, featureHash := historyFilterRepo(t, "hf-branch")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if !h.ui.HasText("feature only work") {
		t.Fatalf("setup: feature commit missing in All Branches; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main work") {
		t.Fatalf("setup: main commit missing in All Branches; texts=%q", h.ui.Texts())
	}

	driveHistoryBranchSelect(t, h, 1)

	if h.ui.HasText("feature only work") {
		t.Fatalf("feature commit still visible in Current Branch; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main work") || !h.ui.HasText("base commit") {
		t.Fatalf("main history incomplete in Current Branch; texts=%q", h.ui.Texts())
	}
	for _, cm := range h.app.session.Snapshot().Commits {
		if cm.Hash == featureHash {
			t.Fatal("feature commit in the filtered snapshot")
		}
	}

	driveHistoryBranchSelect(t, h, 0)

	if !h.ui.HasText("feature only work") {
		t.Fatalf("feature commit missing again after switching back; texts=%q", h.ui.Texts())
	}
	if h.app.session.HistoryCurrentBranch() {
		t.Fatal("session still filtered after switching back to All Branches")
	}
}

// TestHistoryFilterFirstParent checks the first-parent filter in the
// Current Branch + First Parent combination: the merged-in side branch
// commits leave the walk while the merge commit stays, the merge row
// continues with exactly one downward line, and the merge commit's parents
// still drive the Revert dialog's mainline choice.
func TestHistoryFilterFirstParent(t *testing.T) {
	repo := newGitRepo(t, "hf-firstparent")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	repo.CommitFile("feat.txt", "feat\n", "feature only work")
	repo.Checkout("main")
	repo.CommitFile("main.txt", "main\n", "main work")
	repo.Git("merge", "--no-ff", "-m", "merge feature into main", "feature")
	mergeHash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if !h.ui.HasText("feature only work") {
		t.Fatalf("setup: feature commit missing before the filter; texts=%q", h.ui.Texts())
	}

	driveHistoryBranchSelect(t, h, 1)
	if err := h.ui.Click("First Parent Only"); err != nil {
		t.Fatalf("click First Parent Only: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if h.ui.HasText("feature only work") {
		t.Fatalf("feature commit still visible with First Parent on; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("merge feature into main") {
		t.Fatalf("merge commit missing with First Parent on; texts=%q", h.ui.Texts())
	}

	snap := h.app.session.Snapshot()
	mergeRow := -1
	for i, cm := range snap.Commits {
		if cm.Hash == mergeHash {
			mergeRow = i
		}
	}
	if mergeRow < 0 {
		t.Fatalf("merge commit %s missing from the filtered snapshot", mergeHash)
	}
	row := snap.Graph[mergeRow]
	bottoms := 0
	for _, s := range row.Segments {
		if s.Kind == git.SegmentBottom {
			bottoms++
		}
		if s.From != 0 || s.To != 0 {
			t.Fatalf("merge row segment %+v reaches outside lane 0: a second-parent line survived", s)
		}
	}
	if bottoms != 1 {
		t.Fatalf("merge row bottom segments = %d, want the single first-parent line", bottoms)
	}
	if len(snap.Commits[mergeRow].Parents) != 2 {
		t.Fatalf("merge commit parents = %+v, want both parents kept on the commit", snap.Commits[mergeRow].Parents)
	}

	// The commit itself is unchanged: the Revert of the merge still offers
	// the mainline choice, which only a commit with several parents has.
	clickLabel := commitRowLabel(mergeHash, "merge feature into main")
	if err := h.ui.RightClick(clickLabel); err != nil {
		t.Fatalf("right-click merge row: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Revert Commit…"); err != nil {
		t.Fatalf("choose Revert Commit…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !h.ui.HasText("Mainline parent:") {
		t.Fatalf("revert dialog missing the mainline choice; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Parent 1") || !h.ui.HasText("Parent 2") {
		t.Fatalf("revert dialog missing the parent options; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// Unchecking the filter while the branch scope stays Current Branch
	// brings the feature commit back: HEAD reaches it through the merge's
	// second parent, and only the first-parent filter kept it out — the two
	// controls act independently of one another.
	if err := h.ui.Click("First Parent Only"); err != nil {
		t.Fatalf("uncheck First Parent Only: %v", err)
	}
	h.drain()
	if cb, ok := h.ui.AccessByRole(ui.RoleCheckBox, "First Parent Only"); !ok || cb.Checked {
		t.Fatalf("First Parent checkbox access = %+v (found=%v), want unchecked", cb, ok)
	}
	if !h.app.session.HistoryCurrentBranch() {
		t.Fatal("unchecking First Parent moved the branch scope: the controls are not independent")
	}
	if !h.ui.HasText("feature only work") {
		t.Fatalf("feature commit missing after unchecking First Parent: HEAD reaches it through the merge; texts=%q", h.ui.Texts())
	}
}

// assertNoDanglingGraphLanes verifies every lane the graph rows open is
// closed again by the commit it waits for: a lane a bottom segment opens
// toward a commit the walk never lists would hang without an end — exactly
// what the first-parent layout exists to prevent. The simulation reads only
// the segments: a row's SegmentTop lines close every lane merging into the
// row's commit, and each SegmentBottom opens the lane its line continues
// toward. Closing lanes are deleted before the row's bottoms open so a lane
// reused for another commit in the same row tracks the new one.
func assertNoDanglingGraphLanes(t *testing.T, graph []git.GraphRow) {
	t.Helper()
	open := map[int]bool{}
	for _, row := range graph {
		delete(open, row.Lane)
		for _, s := range row.Segments {
			if s.Kind == git.SegmentTop {
				delete(open, s.From)
			}
		}
		for _, s := range row.Segments {
			if s.Kind == git.SegmentBottom {
				open[s.To] = true
			}
		}
	}
	if len(open) != 0 {
		lanes := []int{}
		for lane := range open {
			lanes = append(lanes, lane)
		}
		t.Fatalf("graph lanes %v never close: lines hang toward commits the walk does not list", lanes)
	}
}

// TestHistoryFilterFirstParentAllBranches checks the All Branches +
// First Parent combination with the merged-in branch still alive: the
// branch's tip starts its own first-parent chain, so its commits stay in the
// list, while every merge commit still continues with exactly one downward
// line and no graph line hangs toward a commit the walk does not list.
func TestHistoryFilterFirstParentAllBranches(t *testing.T) {
	repo := newGitRepo(t, "hf-firstparent-all")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	featureHash := repo.CommitFile("feat.txt", "feat\n", "feature only work")
	repo.Checkout("main")
	repo.CommitFile("main.txt", "main\n", "main work")
	repo.Git("merge", "--no-ff", "-m", "merge feature into main", "feature")
	mergeHash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if err := h.ui.Click("First Parent Only"); err != nil {
		t.Fatalf("click First Parent Only: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	// All Branches stays the branch scope: the living feature branch's tip
	// walks its own first-parent chain, so its commit stays visible.
	if h.app.session.HistoryCurrentBranch() {
		t.Fatal("branch scope moved to Current Branch: the controls are not independent")
	}
	if !h.ui.HasText("feature only work") {
		t.Fatalf("feature commit missing in All Branches + First Parent; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("merge feature into main") {
		t.Fatalf("merge commit missing in All Branches + First Parent; texts=%q", h.ui.Texts())
	}

	snap := h.app.session.Snapshot()
	foundFeature, foundMerge := false, false
	for i, cm := range snap.Commits {
		if cm.Hash == featureHash {
			foundFeature = true
		}
		if cm.Hash != mergeHash {
			continue
		}
		foundMerge = true
		bottoms := 0
		for _, s := range snap.Graph[i].Segments {
			if s.Kind == git.SegmentBottom {
				bottoms++
			}
		}
		if bottoms != 1 {
			t.Fatalf("merge row %d bottom segments = %d, want the single first-parent line", i, bottoms)
		}
	}
	if !foundFeature || !foundMerge {
		t.Fatalf("snapshot missing feature=%v merge=%v", foundFeature, foundMerge)
	}
	if len(snap.Graph) != len(snap.Commits) {
		t.Fatalf("graph rows = %d, commits = %d, want equal", len(snap.Graph), len(snap.Commits))
	}
	assertNoDanglingGraphLanes(t, snap.Graph)

	// The control is still git's own --first-parent: both chains of the
	// fixture are linear, so the list holds every commit the unfiltered walk
	// has — the filter narrows the graph, not this fixture's walk.
	if len(snap.Commits) != 4 {
		t.Fatalf("commits = %d (%q), want all 4 of the two linear chains", len(snap.Commits), historySubjects(h))
	}
}

// TestHistoryFilterFirstParentLoadMore checks pagination of the first-parent
// walk in the All Branches + First Parent combination. The fixture's feature
// branch is deleted after merging, as a merged branch usually is: its commit
// is then reachable only through the merge's second parent, so the
// first-parent walk leaves it out and the walk is the trunk's single chain.
// The page Load More appends holds only commits of that chain, and the
// merged graph of both pages keeps every row on the single lane — no line
// hangs toward a commit the walk never lists.
func TestHistoryFilterFirstParentLoadMore(t *testing.T) {
	repo := newGitRepo(t, "hf-paging")
	makeCommits(t, repo, "m.txt", "main", 5)
	repo.Git("checkout", "-q", "-b", "feature", "HEAD~2")
	repo.CommitFile("feat.txt", "feat\n", "feature only work")
	repo.Checkout("main")
	repo.Git("merge", "--no-ff", "-m", "merge feature into main", "feature")
	repo.Git("branch", "-q", "-d", "feature")
	repo.CommitFile("after1.txt", "after1\n", "after merge one")
	repo.CommitFile("after2.txt", "after2\n", "after merge two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if err := h.ui.Click("First Parent Only"); err != nil {
		t.Fatalf("click First Parent Only: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	h.app.session.logLimit = 3
	h.app.session.Reload()
	h.drain()

	snap := h.app.session.Snapshot()
	if len(snap.Commits) != 3 || !snap.More {
		t.Fatalf("first page = %d commits (More=%v), want 3 (More=true)", len(snap.Commits), snap.More)
	}
	if h.ui.HasText("main 1") {
		t.Fatalf("oldest commit visible before Load More; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Load More"); err != nil {
		t.Fatalf("click Load More: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	snap = h.app.session.Snapshot()
	if len(snap.Commits) != 8 {
		t.Fatalf("commits after Load More = %d, want the 8 of the first-parent chain: %q", len(snap.Commits), historySubjects(h))
	}
	if snap.More {
		t.Fatal("snapshot.More still true after the chain loaded fully")
	}
	for _, cm := range snap.Commits {
		if cm.Subject == "feature only work" {
			t.Fatal("feature commit paged into the first-parent walk")
		}
	}
	if !h.ui.HasText("main 1") {
		t.Fatalf("oldest chain commit missing after Load More; texts=%q", h.ui.Texts())
	}
	if len(snap.Graph) != len(snap.Commits) {
		t.Fatalf("graph rows = %d, commits = %d, want equal", len(snap.Graph), len(snap.Commits))
	}
	for i, row := range snap.Graph {
		if row.Width != 1 || row.Lane != 0 {
			t.Fatalf("page-merged graph row %d lane %d width %d, want lane 0 width 1: a line hangs outside the chain", i, row.Lane, row.Width)
		}
		for _, s := range row.Segments {
			if s.From != 0 || s.To != 0 {
				t.Fatalf("page-merged graph row %d segment %+v outside lane 0", i, s)
			}
		}
	}
}

// TestHistoryFilterPersistedPerRepository checks the choices survive a
// restart of the app — the reopened store reads them back into the same
// repository — while another repository keeps the unfiltered defaults.
func TestHistoryFilterPersistedPerRepository(t *testing.T) {
	repoA, _ := historyFilterRepo(t, "hf-persist-a")
	repoB, featureB := historyFilterRepo(t, "hf-persist-b")

	h := newTestApp(t)
	openHistoryPage(t, h, repoA.StoreRepo())
	driveHistoryBranchSelect(t, h, 1)
	if err := h.ui.Click("First Parent Only"); err != nil {
		t.Fatalf("click First Parent Only: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	// The store write landed before the harness is rebuilt.
	id := h.app.session.Meta().ID
	meta, ok := h.store.Get(id)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if !meta.Local.HistoryCurrentBranch || !meta.Local.HistoryFirstParent {
		t.Fatalf("repo settings = %+v, want both filters saved", meta.Local)
	}

	// A new app instance over the same store file reopens the repository:
	// both filters apply again.
	h2 := newTestAppAtStore(t, h.store.Path())
	openHistoryPage(t, h2, repoA.StoreRepo())
	if !h2.app.session.HistoryCurrentBranch() || !h2.app.session.HistoryFirstParent() {
		t.Fatalf("reopened session filters current=%v firstParent=%v, want both on",
			h2.app.session.HistoryCurrentBranch(), h2.app.session.HistoryFirstParent())
	}
	if cb, ok := h2.ui.AccessByRole(ui.RoleCheckBox, "First Parent Only"); !ok || !cb.Checked {
		t.Fatalf("reopened First Parent checkbox access = %+v (found=%v), want checked", cb, ok)
	}
	if sel, ok := h2.ui.AccessByRole(ui.RolePopUpButton, "History Branches"); !ok || sel.Value != "Current Branch" {
		t.Fatalf("reopened branch select access = %+v (found=%v), want value Current Branch", sel, ok)
	}
	if h2.ui.HasText("feature only work") {
		t.Fatalf("feature commit visible in the reopened filtered history; texts=%q", h2.ui.Texts())
	}

	// The other repository of the same store never inherited the choices.
	openHistoryPage(t, h2, repoB.StoreRepo())
	if h2.app.session.HistoryCurrentBranch() || h2.app.session.HistoryFirstParent() {
		t.Fatal("the second repository opened filtered, want the unfiltered defaults")
	}
	foundB := false
	for _, cm := range h2.app.session.Snapshot().Commits {
		if cm.Hash == featureB {
			foundB = true
		}
	}
	if !foundB || !h2.ui.HasText("feature only work") {
		t.Fatalf("the second repository did not open on All Branches; texts=%q", h2.ui.Texts())
	}
}

// TestHistoryFilterUnbornHeadEmptyState checks the filtered empty state:
// with HEAD on a fresh orphan branch the Current Branch walk answers no
// commits instead of a load failure, the placeholder explains it and carries
// the way back, and Show All Branches restores the other branches' commits —
// a choice a restarted app still holds.
func TestHistoryFilterUnbornHeadEmptyState(t *testing.T) {
	repo, _ := historyFilterRepo(t, "hf-unborn")
	repo.Git("checkout", "-q", "--orphan", "fresh")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("setup: unfiltered load failed: %v", err)
	}
	if !h.ui.HasText("main work") {
		t.Fatalf("setup: main commits missing in All Branches; texts=%q", h.ui.Texts())
	}

	driveHistoryBranchSelect(t, h, 1)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("filtered load of the unborn HEAD failed: %v", err)
	}
	if h.ui.HasText("Failed to load repository snapshot") {
		t.Fatalf("load failure shown for the unborn HEAD; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("No commits on the current branch") {
		t.Fatalf("missing the filtered empty state; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("0 commits") {
		t.Fatalf("the header is drawn without commits, leaving no way back; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("No commits yet") {
		t.Fatalf("the unfiltered empty copy shows for a filtered walk; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Show All Branches"); err != nil {
		t.Fatalf("click Show All Branches: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if !h.ui.HasText("main work") {
		t.Fatalf("main commits missing after Show All Branches; texts=%q", h.ui.Texts())
	}
	if h.app.session.HistoryCurrentBranch() {
		t.Fatal("session still filtered after Show All Branches")
	}

	// The button wrote the choice: a restarted app opens on All Branches.
	h2 := newTestAppAtStore(t, h.store.Path())
	openHistoryPage(t, h2, repo.StoreRepo())
	if h2.app.session.HistoryCurrentBranch() {
		t.Fatal("the reopened app still filters to the current branch")
	}
	if !h2.ui.HasText("main work") {
		t.Fatalf("main commits missing in the reopened app; texts=%q", h2.ui.Texts())
	}
}

// TestHistoryFilterRevealNotFoundToast checks Show in History naming the
// filter when it hides the commit: the toast explains the filtered history,
// and with the filter off the same reveal finds the commit.
func TestHistoryFilterRevealNotFoundToast(t *testing.T) {
	repo, featureHash := historyFilterRepo(t, "hf-reveal")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	driveHistoryBranchSelect(t, h, 1)

	openSearchView(t, h)
	submitSearchQuery(t, h, "only")
	st := searchState(h)
	foundTarget := false
	for _, cm := range st.results {
		if cm.Hash == featureHash {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatalf("search results = %q, want the feature commit", searchResultSubjects(st))
	}
	// The reveal must not settle for the first result: choose the feature
	// commit's row itself.
	if err := h.ui.Click(searchRowLabel(featureHash, "feature only work")); err != nil {
		t.Fatalf("click the feature result row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	h.ui.Frame()
	h.drain()

	if !h.ui.HasText("Could not find the commit in the filtered history.") {
		t.Fatalf("missing the filtered-history toast; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Could not find the commit in the loaded history.") {
		t.Fatal("the unfiltered not-found copy shows although a filter is on")
	}
	if h.app.historyView.sel == featureHash {
		t.Fatal("the filtered-out commit was selected after all")
	}

	// With the filter off the same reveal reaches the commit.
	driveHistoryBranchSelect(t, h, 0)
	openSearchView(t, h)
	submitSearchQuery(t, h, "only")
	if err := h.ui.Click(searchRowLabel(featureHash, "feature only work")); err != nil {
		t.Fatalf("click the feature result row again: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if err := h.ui.Click("Show in History"); err != nil {
		t.Fatalf("click Show in History again: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	h.drain()

	if h.app.historyView.sel != featureHash {
		t.Fatalf("history selection = %q, want %q with the filter off", h.app.historyView.sel, featureHash)
	}
	// The tester's clock holds the first reveal's filtered-history toast on
	// screen, but a failed second reveal would add the unfiltered copy.
	if h.ui.HasText("Could not find the commit in the loaded history.") {
		t.Fatalf("the unfiltered not-found toast shows although the reveal found the commit; texts=%q", h.ui.Texts())
	}
}

// TestHistoryFilterSaveFailure keeps the user's choice for the opening when
// the repository settings cannot be written, surfacing the failure as the
// settings toast instead of reverting the filter.
func TestHistoryFilterSaveFailure(t *testing.T) {
	repo, _ := historyFilterRepo(t, "hf-savefail")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.store.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(h.store.Path() + ".tmp") })

	driveHistoryBranchSelect(t, h, 1)

	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("the save failure did not surface as the settings toast; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("feature only work") {
		t.Fatalf("the view reverted the choice after the failed save; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main work") {
		t.Fatalf("main history incomplete after the failed save; texts=%q", h.ui.Texts())
	}
	if !h.app.session.HistoryCurrentBranch() {
		t.Fatal("the session did not keep the user's choice for this opening")
	}
	got, ok := h.store.Get(h.app.session.Meta().ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if got.Local.HistoryCurrentBranch {
		t.Fatal("the failed save reached the repository settings")
	}
}

// TestHistoryFilterCopyCommitSubject checks the copy item between Copy
// Commit SHA and Copy Commit Info: it puts exactly the commit's subject line
// on the clipboard, never the message body.
func TestHistoryFilterCopyCommitSubject(t *testing.T) {
	repo := newGitRepo(t, "hf-copysubject")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.WriteFile("f.txt", "v1\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "the subject line\n\nand a body paragraph")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	row := commitRowLabel(head, "the subject line")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	shaIdx, subjectIdx, infoIdx := -1, -1, -1
	for i, item := range menu {
		switch item {
		case "Copy Commit SHA":
			shaIdx = i
		case "Copy Commit Subject":
			subjectIdx = i
		case "Copy Commit Info":
			infoIdx = i
		}
	}
	if shaIdx < 0 || subjectIdx < 0 || infoIdx < 0 {
		t.Fatalf("menu %q misses a copy item", menu)
	}
	if !(shaIdx < subjectIdx && subjectIdx < infoIdx) {
		t.Fatalf("menu %q: Copy Commit Subject is not between Copy Commit SHA and Copy Commit Info", menu)
	}
	if err := h.ui.ChooseMenuItem("Copy Commit Subject"); err != nil {
		t.Fatalf("choose Copy Commit Subject: %v", err)
	}
	if got := h.ui.Clipboard(); got != "the subject line" {
		t.Fatalf("clipboard = %q, want exactly the subject line", got)
	}
}
