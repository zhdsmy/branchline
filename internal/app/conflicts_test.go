package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// multiTypeMergeFixture builds a merge that conflicts in four different ways
// at once: uu.txt is modified on both sides, aa.txt is added on both sides,
// du.txt is deleted by us and modified on their side, and ud.txt is modified
// by us and deleted on their side. The merge is left unresolved with
// MERGE_HEAD in place.
func multiTypeMergeFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.WriteFile("uu.txt", "base\n")
	repo.WriteFile("du.txt", "mod\n")
	repo.WriteFile("ud.txt", "mod\n")
	repo.Git("add", "--", "uu.txt", "du.txt", "ud.txt")
	repo.Git("commit", "-m", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.WriteFile("uu.txt", "side-uu\n")
	repo.WriteFile("du.txt", "side-du\n")
	repo.Git("rm", "-q", "ud.txt")
	repo.WriteFile("aa.txt", "side-aa\n")
	repo.Git("add", "--", "uu.txt", "du.txt", "aa.txt")
	repo.Git("commit", "-m", "side work")
	repo.Git("checkout", "-q", "main")
	repo.WriteFile("uu.txt", "main-uu\n")
	repo.Git("rm", "-q", "du.txt")
	repo.WriteFile("ud.txt", "main-ud\n")
	repo.WriteFile("aa.txt", "main-aa\n")
	repo.Git("add", "--", "uu.txt", "ud.txt", "aa.txt")
	repo.Git("commit", "-m", "main work")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	out := gitOutTrim(t, repo.Path(), "ls-files", "-u")
	if out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
}

// uuMergeFixture builds a merge with a single both-modified conflict in f.txt:
// ours carries "main\n", theirs carries "side\n".
func uuMergeFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CommitFile("f.txt", "base\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", "side\n", "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", "main\n", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
}

// plumbDeleteDeleteEntry recreates the rare delete-delete conflict in dd.txt
// by index plumbing: git resolves a natural delete-delete cleanly, so the
// stage-1-only unmerged entry has to be written directly. Requires the blob of
// dd.txt at the merge base or at HEAD.
func plumbDeleteDeleteEntry(t *testing.T, repo *gitRepoFixture, rev string) {
	t.Helper()
	tree := repo.Git("ls-tree", rev, "dd.txt")
	fields := strings.Fields(tree)
	if len(fields) < 3 {
		t.Fatalf("fixture: dd.txt missing at %s: %q", rev, tree)
	}
	blob := fields[2]
	entry := fmt.Sprintf("0 0000000000000000000000000000000000000000\tdd.txt\n100644 %s 1\tdd.txt\n", blob)
	if out, err := tryGitCommandStdin(repo.Path(), entry, "update-index", "-q", "--index-info"); err != nil {
		t.Fatalf("fixture: plumb delete-delete entry: %v\n%s", err, out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); !strings.Contains(out, "dd.txt") {
		t.Fatalf("fixture: delete-delete entry missing after plumbing: %q", out)
	}
}

// deleteDeleteFixture builds a repository whose dd.txt carries the unmerged
// delete-delete entry, with the file absent from the working tree. The base
// commit has dd.txt, a second commit removes it cleanly, and plumbing turns
// the removal back into the stage-1-only conflict.
func deleteDeleteFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CommitFile("dd.txt", "dd-base\n", "add dd")
	repo.Git("rm", "-q", "dd.txt")
	repo.Git("commit", "-m", "remove dd")
	plumbDeleteDeleteEntry(t, repo, "HEAD~1")
}

// openConflictedMerge opens the Changes page of a repository with an in-progress
// conflicted merge and verifies the snapshot sees the conflict.
func openConflictedMerge(t *testing.T, repo *gitRepoFixture) *testHarness {
	t.Helper()
	h := openChangesHarness(t, repo)
	st := h.app.session.Snapshot().Status
	if !st.Conflicted() {
		t.Fatalf("fixture: snapshot does not report a conflict: %+v", st)
	}
	return h
}

// conflictQuickActionMenu opens the conflict row's context menu and chooses an
// item, verifying it is present first.
func chooseConflictMenuItem(t *testing.T, h *testHarness, path, item string) {
	t.Helper()
	row := "Conflicted " + path
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click conflict row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if !slices.Contains(h.ui.Menu(), item) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), item)
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q: %v", item, err)
	}
	h.ui.Frame()
}

// confirmResolveOpen verifies the resolve confirmation is visible with its two
// buttons inside the dialog.
func confirmResolveOpen(t *testing.T, h *testHarness) {
	t.Helper()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Resolve"); !ok {
		t.Fatalf("resolve confirmation did not open; texts=%q", h.ui.Texts())
	} else if btn.Disabled {
		t.Fatal("Confirm Resolve starts disabled")
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Resolve")
}

// clickConfirmResolve clicks through the open resolve confirmation and drains
// the write.
func clickConfirmResolve(t *testing.T, h *testHarness) {
	t.Helper()
	mustClick(t, h, "Confirm Resolve")
	h.drain()
}

// assertNotStaged verifies path carries no stage-0 index entry: git diff
// --cached lists unmerged entries too, so only ls-files proves a file was
// never staged.
func assertNotStaged(t *testing.T, repo *gitRepoFixture, path string) {
	t.Helper()
	out := repo.Git("ls-files", "-s", "--", path)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] == "0" {
			t.Fatalf("%s has a stage-0 index entry despite the refusal: %q", path, out)
		}
	}
}

// The Conflicted section groups every unmerged file above Unstaged with its
// conflict type badge, one row per file and no Discard or Stage buttons, while
// the ordinary sections are untouched.
func TestConflictsSectionTypesAndLayout(t *testing.T) {
	repo := newGitRepo(t, "conflicts-types")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	if !h.ui.HasText("Conflicted (4)") {
		t.Fatalf("Conflicted (4) section title missing; texts=%q", h.ui.Texts())
	}
	for _, path := range []string{"uu.txt", "aa.txt", "du.txt", "ud.txt"} {
		if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted "+path); !ok {
			t.Fatalf("conflict row for %s missing; texts=%q", path, h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstaged "+path); ok {
			t.Fatalf("conflicted %s still listed as an ordinary unstaged row; texts=%q", path, h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard "+path); ok {
			t.Fatalf("conflicted %s offers a Discard button; texts=%q", path, h.ui.Texts())
		}
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage "+path); ok {
			t.Fatalf("conflicted %s offers a Stage button; texts=%q", path, h.ui.Texts())
		}
	}
	for _, label := range []string{"Both Modified", "Both Added", "Deleted by Us", "Deleted by Them"} {
		if !h.ui.HasText(label) {
			t.Fatalf("conflict type label %q missing; texts=%q", label, h.ui.Texts())
		}
	}
	for _, path := range []string{"uu.txt", "aa.txt", "du.txt", "ud.txt"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Mark Resolved "+path); !ok {
			t.Fatalf("quick Mark Resolved button for %s missing; texts=%q", path, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted dd.txt"); ok {
		t.Fatal("unexpected dd.txt conflict row in this fixture")
	}

	// A delete-delete conflict shows its own type and only the Accept Deletion
	// quick action while the file is absent from the working tree.
	repo2 := newGitRepo(t, "conflicts-dd-type")
	deleteDeleteFixture(t, repo2)
	h2 := openChangesHarness(t, repo2)
	if !h2.ui.HasText("Conflicted (1)") {
		t.Fatalf("Conflicted (1) section title missing for delete-delete; texts=%q", h2.ui.Texts())
	}
	if !h2.ui.HasText("Both Deleted") {
		t.Fatalf("Both Deleted type label missing; texts=%q", h2.ui.Texts())
	}
	if _, ok := h2.ui.AccessByRole(ui.RoleButton, "Accept Deletion dd.txt"); !ok {
		t.Fatalf("quick Accept Deletion button missing; texts=%q", h2.ui.Texts())
	}
	if _, ok := h2.ui.AccessByRole(ui.RoleButton, "Mark Resolved dd.txt"); ok {
		t.Fatalf("delete-delete quick row must not offer Mark Resolved while the file is absent; texts=%q", h2.ui.Texts())
	}
}

// Selecting a conflicted file replaces the diff pane with the conflict
// summary: the type explanation, the quick resolution buttons, and no broken
// combined diff.
func TestConflictsDetailPaneAndSelection(t *testing.T) {
	repo := newGitRepo(t, "conflicts-detail")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	h.ui.Frame()

	if !h.app.changesView.selected || h.app.changesView.selectedPath != "f.txt" {
		t.Fatalf("conflict row click did not select f.txt: %+v", h.app.changesView)
	}
	if !h.ui.HasText("Both sides modified this file.") {
		t.Fatalf("conflict explanation missing; texts=%q", h.ui.Texts())
	}
	for _, label := range []string{"Use Ours", "Use Theirs", "Mark Resolved"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("pane action %q missing; texts=%q", label, h.ui.Texts())
		}
	}
	if h.ui.HasText("No File Selected") {
		t.Fatalf("diff placeholder leaked into the conflict pane; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("<<<<<<<") {
		t.Fatalf("raw conflict markers rendered as a diff; texts=%q", h.ui.Texts())
	}
	// The pane keeps rendering the summary across frames rather than falling
	// back to a combined diff once background reads settle.
	h.drain()
	if !h.ui.HasText("Both sides modified this file.") {
		t.Fatalf("conflict explanation lost after settling; texts=%q", h.ui.Texts())
	}
}

// Use Ours and Use Theirs overwrite the working tree file with the chosen
// version and stage it, through both the context menu and the summary pane,
// each time behind the confirmation dialog.
func TestConflictsUseOursAndTheirsByMenuAndPane(t *testing.T) {
	repo := newGitRepo(t, "conflicts-sides")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	// Use Ours through the row context menu: the merge main version lands.
	chooseConflictMenuItem(t, h, "f.txt", "Use Ours…")
	if !h.ui.HasText("Use our version?") {
		t.Fatalf("use-ours confirmation title missing; texts=%q", h.ui.Texts())
	}
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use ours failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "main\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived use ours: %q", out)
	}

	// Recreate the conflict and take theirs through the summary pane instead.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: second merge left no conflict: %+v", st)
	}

	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Theirs")
	h.ui.Frame()
	if !h.ui.HasText("Use their version?") {
		t.Fatalf("use-theirs confirmation title missing; texts=%q", h.ui.Texts())
	}
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use theirs failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "side\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived use theirs: %q", out)
	}
}

// Canceling the overwrite confirmation leaves every byte of the working tree
// and every unmerged stage exactly as they were.
func TestConflictsUseOursCancelPreservesBytes(t *testing.T) {
	repo := newGitRepo(t, "conflicts-cancel")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	before, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil || !strings.Contains(string(before), "<<<<<<<") {
		t.Fatalf("fixture: conflicted f.txt missing markers: %q err=%v", before, err)
	}
	stagesBefore := gitOutTrim(t, repo.Path(), "ls-files", "-u")

	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	mustClick(t, h, "Cancel")
	h.ui.Frame()

	if h.ui.HasText("Use our version?") {
		t.Fatal("resolve confirmation still open after Cancel")
	}
	after, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("Cancel altered the working tree file: before %q after %q err=%v", before, after, err)
	}
	if stages := gitOutTrim(t, repo.Path(), "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("Cancel altered the unmerged stages: before %q after %q", stagesBefore, stages)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cancel produced an action error: %v", err)
	}
	if st := conflictUI(h.app); st.confirmOpen || st.confirmPath != "" {
		t.Fatalf("confirmation state survived Cancel: %+v", st)
	}
}

// Delete/modify conflicts offer the keep and delete resolutions by side: the
// file modified by one side and deleted by the other can be kept with the
// modified content or deleted outright, each behind its confirmation.
func TestConflictsModifyDeleteResolutions(t *testing.T) {
	repo := newGitRepo(t, "conflicts-modify-delete")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	// du.txt is Deleted by Us: the working tree holds their modified version,
	// Use Ours deletes the file and Use Theirs keeps it. ud.txt is Deleted by
	// Them: Use Ours keeps our modified version, Use Theirs deletes the file.
	chooseConflictMenuItem(t, h, "du.txt", "Use Ours (Delete File)")
	if !h.ui.HasText("Delete du.txt?") {
		t.Fatalf("delete confirmation title missing; texts=%q", h.ui.Texts())
	}
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use ours (delete file) failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "du.txt")); !os.IsNotExist(err) {
		t.Fatalf("du.txt still exists after delete resolution: err=%v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", "du.txt"); out != "" {
		t.Fatalf("du.txt unmerged entries survived delete resolution: %q", out)
	}

	mustClick(t, h, "Conflicted ud.txt")
	h.drain()
	mustClick(t, h, "Use Theirs (Delete File)")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use theirs (delete file) failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "ud.txt")); !os.IsNotExist(err) {
		t.Fatalf("ud.txt still exists after delete resolution: err=%v", err)
	}

	// The keep direction on a fresh merge of the same shape: both files land
	// with the modified content of the side that kept them.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()

	chooseConflictMenuItem(t, h, "du.txt", "Use Theirs (Keep File)")
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use theirs (keep file) failed: %v", err)
	}
	assertFileBytes(t, repo, "du.txt", "side-du\n")

	chooseConflictMenuItem(t, h, "ud.txt", "Use Ours (Keep File)")
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use ours (keep file) failed: %v", err)
	}
	assertFileBytes(t, repo, "ud.txt", "main-ud\n")
}

// The delete-delete conflict accepts the deletion through its quick row action
// after confirmation, and a file the user put back can instead be staged as
// the resolution through Mark Resolved in the context menu.
func TestConflictsDeleteDeleteAcceptAndMarkResolved(t *testing.T) {
	repo := newGitRepo(t, "conflicts-dd")
	deleteDeleteFixture(t, repo)

	h := openChangesHarness(t, repo)

	// Cancel first: the unmerged entry and the absent file stay exactly so.
	mustClick(t, h, "Accept Deletion dd.txt")
	h.ui.Frame()
	if !h.ui.HasText("Accept deletion of dd.txt?") {
		t.Fatalf("accept-deletion confirmation title missing; texts=%q", h.ui.Texts())
	}
	confirmResolveOpen(t, h)
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); !strings.Contains(out, "dd.txt") {
		t.Fatalf("Cancel disturbed the delete-delete entry: %q", out)
	}

	// Confirm: the entry leaves the index, the file stays absent, and the
	// Conflicted section disappears entirely.
	mustClick(t, h, "Accept Deletion dd.txt")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("accept deletion failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); strings.Contains(out, "dd.txt") {
		t.Fatalf("delete-delete entry survived accept deletion: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "dd.txt")); !os.IsNotExist(err) {
		t.Fatalf("accept deletion resurrected dd.txt: err=%v", err)
	}
	if h.ui.HasText("Conflicted (") {
		t.Fatalf("Conflicted section still rendered with no conflicts left; texts=%q", h.ui.Texts())
	}

	// Recreate the entry and put a file back in the working tree: the context
	// menu now offers Mark Resolved, which stages exactly those bytes.
	plumbDeleteDeleteEntry(t, repo, "HEAD~1")
	repo.WriteFile("dd.txt", "revived\n")
	h.app.session.Reload()
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Accept Deletion dd.txt"); !ok {
		t.Fatalf("quick Accept Deletion button missing after re-plumbing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.RightClick("Conflicted dd.txt"); err != nil {
		t.Fatalf("right-click conflict row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	for _, item := range []string{"Accept Deletion", "Mark Resolved", "File History…", "Blame…"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("delete-delete menu %q missing %q", menu, item)
		}
	}
	if err := h.ui.ChooseMenuItem("Mark Resolved"); err != nil {
		t.Fatalf("choose Mark Resolved: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("mark resolved of revived dd.txt failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); strings.Contains(out, "dd.txt") {
		t.Fatalf("delete-delete entry survived mark resolved: %q", out)
	}
	assertFileBytes(t, repo, "dd.txt", "revived\n")
	if staged := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(staged, "dd.txt") {
		t.Fatalf("revived dd.txt not staged: %q", staged)
	}
}

// Mark Resolved refuses a file that still carries conflict markers — whole
// blocks or half-written ones — without staging anything, and once the markers
// are gone it stages the resolution and lights up the banner's Continue.
func TestConflictsMarkResolvedMarkerRejectionAndContinue(t *testing.T) {
	repo := newGitRepo(t, "conflicts-markers")
	c1, _ := replayConflictFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	triggerPickConflict(t, h, c1, "change f to picked")

	mustClick(t, h, "Changes")
	h.ui.Frame()

	// The conflicted file still holds its markers: Mark Resolved refuses.
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("mark resolved error = %v, want conflict markers refusal", err)
	}
	if !h.ui.HasText("conflict markers") {
		t.Fatalf("marker refusal not visible in the page; texts=%q", h.ui.Texts())
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("refused mark resolved still staged the file")
	}
	assertNotStaged(t, repo, "f.txt")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || !btn.Disabled {
		t.Fatalf("Continue Operation = %+v found=%v, want disabled while conflicted", btn, ok)
	}

	// A half-written marker line — a manual edit that dropped the closing
	// marker — refuses just the same.
	repo.WriteFile("f.txt", "resolved\n>>>>>>> leftover\n")
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("half-marker refusal error = %v, want conflict markers refusal", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("half-marker refusal still staged the file")
	}

	// Clean content stages as the resolution, the conflict leaves the list,
	// and Continue lights up and finishes the cherry-pick.
	repo.WriteFile("f.txt", "resolved\n")
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("mark resolved of clean file failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived clean mark resolved: %q", out)
	}
	if h.ui.HasText("Conflicted (") {
		t.Fatalf("Conflicted section still rendered after resolution; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation after resolution = %+v found=%v, want enabled", btn, ok)
	}
	mustClick(t, h, "Continue Operation")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue after resolution failed: %v", err)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "change f to picked" {
		t.Fatalf("continued commit subject = %q, want change f to picked", subject)
	}
	assertFileBytes(t, repo, "f.txt", "resolved\n")
	bannerGone(t, h)
}

// A squash merge conflict has no MERGE_HEAD and no operation in progress, yet
// the Conflicted section, the quick resolutions and Mark Resolved all work the
// same — including staging a file emptied to zero bytes as a legal resolution.
func TestConflictsSquashMergeWithoutMergeHead(t *testing.T) {
	repo := newGitRepo(t, "conflicts-squash")
	repo.CommitFile("f.txt", "base\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", "side\n", "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", "main\n", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--squash", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: squash merge unexpectedly succeeded: %s", out)
	}

	h := openConflictedMerge(t, repo)
	if st := h.app.session.Snapshot().Status; st.State != "" {
		t.Fatalf("fixture: squash conflict shows operation state %q, want empty", st.State)
	}
	if !h.ui.HasText("Conflicted (1)") {
		t.Fatalf("Conflicted section missing for squash conflict; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("squash conflict row missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); ok {
		t.Fatal("operation banner must not render for a squash conflict")
	}

	// Emptying the file is a legal resolution: zero bytes carry no markers.
	repo.WriteFile("f.txt", "")
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("mark resolved of emptied squash conflict failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived empty resolution: %q", out)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || len(content) != 0 {
		t.Fatalf("emptied resolution changed the working tree file: %q err=%v", content, err)
	}
	// The empty blob e69de29 is the staged content, and committing ends the squash.
	if staged := repo.Git("ls-files", "-s", "--", "f.txt"); !strings.Contains(staged, "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391") {
		t.Fatalf("staged content of emptied resolution is not the empty blob: %q", staged)
	}
	typeCommitMessage(t, h, "squash side work")
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash commit failed: %v", err)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "squash side work" {
		t.Fatalf("squash commit subject = %q, want squash side work", subject)
	}
	if parents := len(strings.Fields(repo.Git("rev-list", "--parents", "-n", "1", "HEAD"))); parents != 2 {
		t.Fatalf("squash commit has %d fields (commit+parents), want 2 — no merge parents", parents)
	}
}

// Stage All and Discard All never touch conflicted files: the bulk stage
// sweeps only the ordinary files into the index, the discard confirmation
// counts only the ordinary files, and the Commit button stays disabled with an
// explicit hint while any conflict is unresolved.
func TestConflictsStageAllDiscardAllIsolationAndCommitGuard(t *testing.T) {
	repo := newGitRepo(t, "conflicts-isolation")
	repo.CommitFile("tracked.txt", "t\n", "add tracked")
	uuMergeFixture(t, repo)
	repo.WriteFile("tracked.txt", "t\ndirty\n")
	repo.WriteFile("untracked.txt", "new\n")

	h := openConflictedMerge(t, repo)

	// The discard confirmation counts only the two ordinary files: the
	// conflicted f.txt never enters the discard set.
	mustClick(t, h, "Discard All")
	h.ui.Frame()
	if !h.ui.HasText("Unstaged changes in 1 file will be lost, and 1 untracked file will be permanently deleted.") {
		t.Fatalf("discard confirmation counted conflicted files; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard all failed: %v", err)
	}
	assertFileBytes(t, repo, "tracked.txt", "t\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file survived discard all: err=%v", err)
	}
	// The conflicted file and its stages are exactly as before.
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("discard all touched the conflicted file: %q err=%v", content, err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("discard all resolved the conflict stages")
	}

	// Stage All sweeps only the ordinary file into the index; the conflicted
	// file stays unmerged with its markers.
	repo.WriteFile("tracked.txt", "t\ndirty\n")
	repo.WriteFile("untracked.txt", "new\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage All")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage all failed: %v", err)
	}
	staged := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "tracked.txt") || !strings.Contains(staged, "untracked.txt") {
		t.Fatalf("stage all did not stage the ordinary files: %q", staged)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("stage all swept the conflicted file into the index")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("stage all rewrote the conflicted file: %q err=%v", content, err)
	}

	// Even with a message typed and files staged, Commit stays disabled behind
	// the explicit hint while the conflict is unresolved.
	typeCommitMessage(t, h, "merge side")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Commit"); !ok || !btn.Disabled {
		t.Fatalf("Commit = %+v found=%v, want disabled during conflict", btn, ok)
	}
	if !h.ui.HasText("Resolve all conflicts before committing.") {
		t.Fatalf("commit hint missing; texts=%q", h.ui.Texts())
	}

	// Resolving the last conflict enables the commit, which finishes the merge
	// as a real merge commit.
	chooseConflictMenuItem(t, h, "f.txt", "Use Ours…")
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use ours failed: %v", err)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Commit"); !ok || btn.Disabled {
		t.Fatalf("Commit after resolution = %+v found=%v, want enabled", btn, ok)
	}
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge commit failed: %v", err)
	}
	if fields := len(strings.Fields(repo.Git("rev-list", "--parents", "-n", "1", "HEAD"))); fields != 3 {
		t.Fatalf("merge commit has %d fields (commit+parents), want 3 — a real merge", fields)
	}
}

// When the background re-read of the repository state fails, no resolution
// write ever runs: the working tree file keeps every byte, the unmerged stages
// survive, and the error is visible in the page.
func TestConflictsReadFailureProtections(t *testing.T) {
	repo := newGitRepo(t, "conflicts-readfail")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("fixture: read conflicted f.txt: %v", err)
	}
	stagesBefore := gitOutTrim(t, repo.Path(), "ls-files", "-u")

	// A failed status read refuses Use Ours before checkout runs.
	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	clickConfirmResolve(t, h)
	assertRepoErrorShown(t, h, "")
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error after the injected status failure")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(conflictBytes) {
		t.Fatalf("use ours overwrote the file despite the failed status read: %q err=%v", content, err)
	}
	if stages := gitOutTrim(t, repo.Path(), "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("unmerged stages altered despite the failed status read: before %q after %q", stagesBefore, stages)
	}

	// A failed unmerged-stage read refuses Use Theirs the same way.
	mustClick(t, h, "Use Theirs")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	installFailFirstExecutor(t, h, func(args []string) bool {
		return slices.Contains(args, "ls-files")
	}, errors.New("injected ls-files failure"))
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error after the injected ls-files failure")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(conflictBytes) {
		t.Fatalf("use theirs overwrote the file despite the failed stage read: %q err=%v", content, err)
	}
	if stages := gitOutTrim(t, repo.Path(), "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("unmerged stages altered despite the failed stage read: before %q after %q", stagesBefore, stages)
	}

	// A failed working-tree read refuses Mark Resolved before staging.
	repo.WriteFile("f.txt", "resolved\n")
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("control mark resolved failed: %v", err)
	}
	// Re-conflict for the real check.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	repo.WriteFile("f.txt", "resolved\n")
	installFailFirstExecutor(t, h, func(args []string) bool {
		return len(args) > 0 && args[0] == "hash-object"
	}, errors.New("injected worktree read failure"))
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error after the injected worktree read failure")
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("mark resolved staged despite the failed worktree read")
	}
	assertFileBytes(t, repo, "f.txt", "resolved\n")
	assertNotStaged(t, repo, "f.txt")
}

// When the conflict changes underneath the open confirmation — resolved
// externally, or its stages replaced — confirming neither overwrites nor
// deletes the file the user is now looking at, and the error is visible.
func TestConflictsStaleStateChangeProtections(t *testing.T) {
	repo := newGitRepo(t, "conflicts-stale")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	// Externally resolved: the confirmation captured a conflict that no longer
	// exists, and the resolution the user applied by hand survives untouched.
	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	repo.Git("checkout", "--theirs", "--", "f.txt")
	repo.Git("add", "--", "f.txt")
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error for the externally resolved conflict")
	}
	if !h.ui.HasText("no longer unmerged") {
		t.Fatalf("external resolution error not visible in the page; texts=%q", h.ui.Texts())
	}
	assertFileBytes(t, repo, "f.txt", "side\n")
	if staged := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(staged, "f.txt") {
		t.Fatalf("the external resolution was overwritten: %q", staged)
	}

	// Stages replaced: the conflict is back, but its theirs-stage is a
	// different object than the one the user saw, so the write refuses and the
	// tampered stages survive.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Theirs")
	h.ui.Frame()
	confirmResolveOpen(t, h)

	stages := repo.Git("ls-files", "-u")
	tamperedOut, err := tryGitCommandStdin(repo.Path(), "tampered\n", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("fixture: write tampered blob: %v\n%s", err, tamperedOut)
	}
	tampered := strings.TrimSpace(tamperedOut)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(stages), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "3" {
			line = fields[0] + " " + tampered + " " + fields[2] + "\t" + fields[3]
		}
		lines = append(lines, line)
	}
	repo.Git("update-index", "-q", "--force-remove", "f.txt")
	if out, err := tryGitCommandStdin(repo.Path(), strings.Join(lines, "\n")+"\n", "update-index", "--index-info"); err != nil {
		t.Fatalf("fixture: rewrite stages: %v\n%s", err, out)
	}
	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("fixture: read conflicted f.txt: %v", err)
	}
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error for the replaced conflict stages")
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(conflictBytes) {
		t.Fatalf("use theirs overwrote the file despite the replaced stages: %q err=%v", content, err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); !strings.Contains(out, tampered) {
		t.Fatalf("replaced stages lost despite the refusal: %q", out)
	}

	// Mark Resolved against a conflict the user no longer faces refuses just
	// the same: an external resolution lands between the row's capture and the
	// write, and neither the working tree file nor the staged resolution moves.
	head = headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: third merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	// The external actor resolves with their version and stages it; the user
	// then edits the file on top. The stale Mark Resolved must neither stage
	// that edit nor rewrite the file.
	repo.Git("checkout", "--theirs", "--", "f.txt")
	repo.Git("add", "--", "f.txt")
	repo.WriteFile("f.txt", "side\nuser edit\n")
	stagedBefore := repo.Git("ls-files", "-s", "--", "f.txt")
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error for mark resolved after external resolution")
	}
	assertFileBytes(t, repo, "f.txt", "side\nuser edit\n")
	if staged := repo.Git("ls-files", "-s", "--", "f.txt"); staged != stagedBefore {
		t.Fatalf("mark resolved restaged the user's edit over the external resolution: before %q after %q", stagedBefore, staged)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("mark resolved re-conflicted the externally resolved file: %q", out)
	}
}

// Switching the repository closes an open resolve confirmation and drops the
// captured conflict identity, exactly as it does for the other dialogs.
func TestConflictsResolveModalClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "conflicts-switch-a")
	uuMergeFixture(t, repoA)
	repoB := newGitRepo(t, "conflicts-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()
	mustClick(t, h, "Conflicted f.txt")
	h.drain()
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := conflictUI(h.app)
	if st.confirmOpen {
		t.Fatal("resolve confirmation still open after the repository switch")
	}
	if st.confirmPath != "" || st.confirmTarget.Path != "" {
		t.Fatalf("captured conflict survived the switch: %+v", st)
	}
	if h.ui.HasText("Use our version?") {
		t.Fatal("resolve confirmation rendered again after the repository switch")
	}
}

// Added-by-us and added-by-them conflicts — plumbed, because git rarely
// produces them naturally — show their own type labels and the keep/delete
// actions of the side that has the file.
func TestConflictsAddedByUsThemTypesAndActions(t *testing.T) {
	repo := newGitRepo(t, "conflicts-au-ua")
	repo.CommitFile("base.txt", "base\n", "base commit")
	oursBlobOut, err := tryGitCommandStdin(repo.Path(), "ours-au\n", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("fixture: write ours blob: %v", err)
	}
	theirsBlobOut, err := tryGitCommandStdin(repo.Path(), "theirs-ua\n", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("fixture: write theirs blob: %v", err)
	}
	entry := fmt.Sprintf("100644 %s 2\tau.txt\n100644 %s 3\tua.txt\n", strings.TrimSpace(oursBlobOut), strings.TrimSpace(theirsBlobOut))
	if out, err := tryGitCommandStdin(repo.Path(), entry, "update-index", "-q", "--index-info"); err != nil {
		t.Fatalf("fixture: plumb added-by entries: %v\n%s", err, out)
	}
	// Git reports the worktree mode for files it finds, so put both contents
	// down to mirror a natural added-by conflict.
	repo.WriteFile("au.txt", "ours-au\n")
	repo.WriteFile("ua.txt", "theirs-ua\n")

	h := openChangesHarness(t, repo)
	if !h.ui.HasText("Added by Us") {
		t.Fatalf("Added by Us type label missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Added by Them") {
		t.Fatalf("Added by Them type label missing; texts=%q", h.ui.Texts())
	}

	// The menu of each row offers exactly the actions of its type.
	if err := h.ui.RightClick("Conflicted au.txt"); err != nil {
		t.Fatalf("right-click au.txt row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	for _, item := range []string{"Use Ours (Keep File)", "Use Theirs (Delete File)", "Mark Resolved"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("au.txt menu %q missing %q", menu, item)
		}
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	if err := h.ui.RightClick("Conflicted ua.txt"); err != nil {
		t.Fatalf("right-click ua.txt row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu = h.ui.Menu()
	for _, item := range []string{"Use Ours (Delete File)", "Use Theirs (Keep File)", "Mark Resolved"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("ua.txt menu %q missing %q", menu, item)
		}
	}
	if err := h.ui.ChooseMenuItem("Use Theirs (Keep File)"); err != nil {
		t.Fatalf("choose Use Theirs (Keep File): %v", err)
	}
	h.ui.Frame()

	// Choosing the keep resolution confirms and stages the file with exactly
	// the bytes it had.
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("use theirs (keep file) on ua.txt failed: %v", err)
	}
	assertFileBytes(t, repo, "ua.txt", "theirs-ua\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", "ua.txt"); out != "" {
		t.Fatalf("ua.txt unmerged entries survived keep resolution: %q", out)
	}
}

// When the user resolves a delete/modify conflict by deleting the file
// outright, Mark Resolved records the deletion: the path leaves the index and
// the unmerged entries with it.
func TestConflictsMarkResolvedAfterManualDeletion(t *testing.T) {
	repo := newGitRepo(t, "conflicts-manual-delete")
	repo.CommitFile("f.txt", "base\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", "side\n", "side edits")
	repo.Git("checkout", "-q", "main")
	repo.Git("rm", "-q", "f.txt")
	repo.Git("commit", "-m", "main deletes")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}

	h := openConflictedMerge(t, repo)
	if !h.ui.HasText("Deleted by Us") {
		t.Fatalf("Deleted by Us type label missing; texts=%q", h.ui.Texts())
	}
	// The user resolves by removing the file the merge left behind, then marks
	// the resolution: the deletion is staged.
	if err := os.Remove(filepath.Join(repo.Path(), "f.txt")); err != nil {
		t.Fatalf("fixture: remove f.txt: %v", err)
	}
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("mark resolved after manual deletion failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived deletion resolution: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "f.txt")); !os.IsNotExist(err) {
		t.Fatalf("f.txt resurrected after deletion resolution: err=%v", err)
	}
	if out := repo.Git("ls-files", "--", "f.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("f.txt still in the index after deletion resolution: %q", out)
	}
}

// While a repository write is busy, every conflict quick action — the row
// buttons, the summary pane buttons and the menu items — is disabled, so no
// resolution can start over the running write.
func TestConflictsQuickActionsDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "conflicts-busy")
	uuMergeFixture(t, repo)
	repo.WriteFile("other.txt", "other\n")

	h := openConflictedMerge(t, repo)
	mustClick(t, h, "Conflicted f.txt")
	h.drain()

	h.queue.PauseWorkers()
	mustClick(t, h, "Stage other.txt")
	worker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ReleaseWorker(worker)
		h.drain()
		t.Fatal("session not busy while the stage worker is held")
	}
	for _, label := range []string{"Mark Resolved f.txt", "Use Ours", "Use Theirs", "Mark Resolved"} {
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok || !btn.Disabled {
			t.Fatalf("busy conflict action %q = %+v found=%v, want present and disabled", label, btn, ok)
		}
	}
	if err := h.ui.RightClick("Conflicted f.txt"); err != nil {
		t.Fatalf("right-click conflict row while busy: %v", err)
	}
	for _, item := range []string{"Use Ours…", "Use Theirs…", "Mark Resolved"} {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
	if h.app.session.Busy() {
		t.Fatal("session still busy after releasing the write worker")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Mark Resolved f.txt"); !ok || btn.Disabled {
		t.Fatalf("conflict action after busy = %+v found=%v, want enabled", btn, ok)
	}
}

// The conflict row's context menu ends with the file history and blame
// entries every tracked file of Local Changes offers: choosing File History
// opens the inspector in place, and it is a read-only detour — the conflict
// survives it untouched.
func TestConflictsRowMenuFileHistoryLinkage(t *testing.T) {
	repo := newGitRepo(t, "conflicts-history")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	stagesBefore := gitOutTrim(t, repo.Path(), "ls-files", "-u")

	chooseConflictMenuItem(t, h, "f.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.path != "f.txt" || st.originPage != pageChanges {
		t.Fatalf("inspector state open=%v path=%q origin=%v, want open file history of f.txt from changes", st.open, st.path, st.originPage)
	}
	if len(st.histCommits) == 0 {
		t.Fatal("file history of the conflicted file lists no commits")
	}
	mustClick(t, h, "Close File Inspector")
	h.ui.Frame()

	if stages := gitOutTrim(t, repo.Path(), "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("the inspector detour altered the conflict stages: before %q after %q", stagesBefore, stages)
	}
	if !h.ui.HasText("Conflicted (1)") {
		t.Fatalf("Conflicted section missing after closing the inspector; texts=%q", h.ui.Texts())
	}
}

// --- SSH loopback ---

// remoteMergeShell runs a merge on the server that is expected to conflict,
// tolerating its nonzero exit, and verifies the unmerged entries exist.
func remoteMergeShell(t *testing.T, srv *sshtest.Target, dir, extraArgs string) {
	t.Helper()
	cmd := "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C " + git.ShellQuote(dir) + " merge --no-edit " + extraArgs + " >/dev/null 2>&1 || true"
	srv.RunShell(t, cmd)
	if out := srv.RunGit(t, dir, "ls-files", "-u"); strings.TrimSpace(out) == "" {
		t.Fatal("fixture: remote merge produced no unmerged entries")
	}
}

// remoteUUConflictFixture builds the both-modified merge conflict on the
// server: ours carries "main\n", theirs carries "side\n".
func remoteUUConflictFixture(t *testing.T, srv *sshtest.Target, dir string) {
	t.Helper()
	srv.RunGit(t, dir, "checkout", "-q", "-b", "side")
	remoteCommitFile(t, srv, dir, "a.txt", "side\n", "side edits")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	remoteCommitFile(t, srv, dir, "a.txt", "main\n", "main edits")
	remoteMergeShell(t, srv, dir, "side")
}

// remoteResetMerge undoes a conflicted merge on the server and starts it
// again, so each resolution direction replays against a fresh conflict.
func remoteResetMerge(t *testing.T, srv *sshtest.Target, dir string) {
	t.Helper()
	srv.RunGit(t, dir, "merge", "--abort")
	remoteMergeShell(t, srv, dir, "side")
}

// Over SSH the cherry-pick conflict flow completes end to end: the Conflicted
// section and type show over the wire, Mark Resolved refuses the markered file
// and stages the fixed one, and Continue finishes the operation.
func TestConflictsMarkResolvedContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	c1, _ := remoteReplayConflict(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	triggerPickConflict(t, h, c1, "change f to picked")

	mustClick(t, h, "Changes")
	h.ui.Frame()
	if !h.ui.HasText("Conflicted (1)") {
		t.Fatalf("SSH Conflicted section missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Both Modified") {
		t.Fatalf("SSH conflict type label missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "Conflicted f.txt"); !ok {
		t.Fatalf("SSH conflict row missing; texts=%q", h.ui.Texts())
	}

	// The markered file refuses.
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("SSH mark resolved error = %v, want conflict markers refusal", err)
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) == "" {
		t.Fatal("SSH refused mark resolved still staged the file")
	}

	// Fixed on the server, it stages and lights up Continue.
	srv.WriteFile(t, repo.Path, "f.txt", []byte("resolved\n"))
	mustClick(t, h, "Mark Resolved f.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH mark resolved of clean file failed: %v", err)
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived clean mark resolved: %q", out)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("SSH Continue Operation after resolution = %+v found=%v, want enabled", btn, ok)
	}
	mustClick(t, h, "Continue Operation")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue after resolution failed: %v", err)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "change f to picked" {
		t.Fatalf("SSH continued commit subject = %q, want change f to picked", subject)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "resolved\n" {
		t.Fatalf("SSH f.txt after continue = %q, want resolved", string(got))
	}
	bannerGone(t, h)
}

// Over SSH the quick resolutions overwrite and delete exactly what they say:
// Use Ours and Use Theirs on a both-modified conflict, and the keep/delete
// directions of a modify/delete conflict, each byte-verified on the server.
func TestConflictsQuickResolveLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteUUConflictFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: SSH snapshot does not report the conflict: %+v", st)
	}

	// Use Ours through the context menu: the main version lands on the server.
	chooseConflictMenuItem(t, h, "a.txt", "Use Ours…")
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH use ours failed: %v", err)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "main\n" {
		t.Fatalf("SSH a.txt after use ours = %q, want main", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived use ours: %q", out)
	}

	// Use Theirs through the summary pane on a fresh conflict.
	remoteResetMerge(t, srv, repo.Path)
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Conflicted a.txt")
	h.drain()
	mustClick(t, h, "Use Theirs")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH use theirs failed: %v", err)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "side\n" {
		t.Fatalf("SSH a.txt after use theirs = %q, want side", string(got))
	}
}

// Over SSH a modify/delete conflict keeps or deletes exactly as chosen, and
// both the read-failure and the external-change protections hold byte for byte
// on the remote working tree.
func TestConflictsModifyDeleteAndGuardsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// Deleted by us: main deletes m.txt, the side branch modifies it.
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "side")
	remoteCommitFile(t, srv, repo.Path, "a.txt", "side-modified\n", "side modifies")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.RunGit(t, repo.Path, "rm", "-q", "a.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "main deletes")
	remoteMergeShell(t, srv, repo.Path, "side")

	h := newTestApp(t, repo)
	h.openRepo(repo)
	h.drain()
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: SSH snapshot does not report the conflict: %+v", st)
	}
	if !h.ui.HasText("Deleted by Us") {
		t.Fatalf("SSH delete/modify type label missing; texts=%q", h.ui.Texts())
	}

	// Keep the file with their modified content.
	chooseConflictMenuItem(t, h, "a.txt", "Use Theirs (Keep File)")
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH use theirs (keep file) failed: %v", err)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "side-modified\n" {
		t.Fatalf("SSH a.txt after keep resolution = %q, want side-modified", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived keep resolution: %q", out)
	}

	// Delete it on a fresh conflict: the file leaves the remote working tree.
	srv.RunGit(t, repo.Path, "merge", "--abort")
	remoteMergeShell(t, srv, repo.Path, "side")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Conflicted a.txt")
	h.drain()
	mustClick(t, h, "Use Ours (Delete File)")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH use ours (delete file) failed: %v", err)
	}
	if got := srv.RunShell(t, "test -e "+git.ShellQuote(repo.Path+"/a.txt")+" && echo present || echo absent"); strings.TrimSpace(got) != "absent" {
		t.Fatalf("SSH a.txt still present after delete resolution: %q", got)
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived delete resolution: %q", out)
	}

	// Read failure protection: a fresh conflict, an injected status failure,
	// and the remote bytes survive byte for byte.
	srv.RunGit(t, repo.Path, "merge", "--abort")
	remoteMergeShell(t, srv, repo.Path, "side")
	h.app.session.Reload()
	h.drain()
	before := srv.ReadFile(t, repo.Path, "a.txt")
	stagesBefore := srv.RunGit(t, repo.Path, "ls-files", "-u")
	mustClick(t, h, "Conflicted a.txt")
	h.drain()
	mustClick(t, h, "Use Theirs (Keep File)")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("SSH: no action error after the injected status failure")
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != string(before) {
		t.Fatalf("SSH file overwritten despite the failed status read: before %q after %q", before, got)
	}
	if stages := srv.RunGit(t, repo.Path, "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("SSH unmerged stages altered despite the failed status read: before %q after %q", stagesBefore, stages)
	}

	// External resolution between capture and confirm: the write refuses and
	// the externally applied resolution survives untouched.
	mustClick(t, h, "Use Theirs (Keep File)")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	srv.RunGit(t, repo.Path, "checkout", "--theirs", "--", "a.txt")
	srv.RunGit(t, repo.Path, "add", "--", "a.txt")
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("SSH: no action error for the externally resolved conflict")
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "side-modified\n" {
		t.Fatalf("SSH file overwritten despite the external resolution: %q", string(got))
	}
	if staged := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); !strings.Contains(staged, "a.txt") {
		t.Fatalf("SSH external resolution disturbed by the refused write: %q", staged)
	}
}
