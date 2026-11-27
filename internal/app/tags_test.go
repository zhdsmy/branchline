package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// tagsSidebarRepo builds a repository with two commits on main: the
// lightweight tag lw and the annotated tag ann (message "annotated release")
// on HEAD, so every dialog flow starts from a sidebar showing both kinds.
// It returns the fixture and the two commit hashes, oldest first.
func tagsSidebarRepo(t *testing.T, name string) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	first := repo.CommitFile("a.txt", "one\n", "first commit")
	second := repo.CommitFile("b.txt", "two\n", "second commit")
	repo.Git("tag", "lw", "HEAD")
	repo.Git("tag", "-a", "ann", "-m", "annotated release", "HEAD")
	return repo, first, second
}

func tagRefExists(t *testing.T, repo *gitRepoFixture, name string) bool {
	t.Helper()
	_, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/tags/"+name)
	return err == nil
}

func tagTypeOf(t *testing.T, repo *gitRepoFixture, name string) string {
	t.Helper()
	return strings.TrimSpace(repo.Git("cat-file", "-t", "refs/tags/"+name))
}

func tagPointsAt(t *testing.T, repo *gitRepoFixture, name string) string {
	t.Helper()
	return strings.TrimSpace(repo.Git("rev-parse", "refs/tags/"+name+"^{}"))
}

// selectTagRow clicks a tag row so its action bar unfolds.
func selectTagRow(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click(name); err != nil {
		t.Fatalf("click tag row %q: %v", name, err)
	}
	h.ui.Frame()
}

// openTagDeleteDialog selects the tag row and opens the delete confirmation
// through the action bar button, as a user does. A row already selected is
// not clicked again: the click would toggle the selection — and its action
// bar — off.
func openTagDeleteDialog(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag "+name); !ok {
		selectTagRow(t, h, name)
	}
	if err := h.ui.Click("Delete tag " + name); err != nil {
		t.Fatalf("click Delete tag %s: %v", name, err)
	}
	h.ui.Frame()
}

// The Tags section shows the New Tag entry while a repository is open, tag
// rows are selectable list items unfolding a delete action, and the section
// keeps its collapse behavior.
func TestTagsSidebarRowsAndSelection(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-rows")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if _, ok := h.ui.Find("New Tag"); !ok {
		t.Fatalf("New Tag entry missing from the sidebar; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("lw"); !ok {
		t.Fatalf("lw missing from the sidebar; texts=%q", h.ui.Texts())
	}

	// Rows expose the selection state to assistive technology.
	row, ok := findAccessNode(t, h.ui, "lw")
	if !ok {
		t.Fatal("no accessible node labeled lw")
	}
	if row.Selected {
		t.Fatal("lw row selected before any click")
	}

	// Clicking selects the row and unfolds the action bar.
	selectTagRow(t, h, "lw")
	row, ok = findAccessNode(t, h.ui, "lw")
	if !ok || !row.Selected {
		t.Fatalf("lw row after click Selected = %+v (found=%v), want selected", row, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); !ok {
		t.Fatalf("Delete tag lw action missing under the selected row; texts=%q", h.ui.Texts())
	}
	// The action bar belongs to the selected tag only.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag ann"); ok {
		t.Fatal("Delete tag ann action visible while only lw is selected")
	}

	// Clicking again collapses the selection.
	if err := h.ui.Click("lw"); err != nil {
		t.Fatalf("click lw again: %v", err)
	}
	h.ui.Frame()
	row, ok = findAccessNode(t, h.ui, "lw")
	if !ok || row.Selected {
		t.Fatalf("lw row after second click Selected = %+v, want unselected", row)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); ok {
		t.Fatal("Delete tag lw action visible after the row was deselected")
	}

	// Collapsing the section hides the rows but keeps the header.
	if err := h.ui.Click("Tags"); err != nil {
		t.Fatalf("click Tags header: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("lw"); ok {
		t.Fatal("lw stayed visible after collapsing Tags")
	}
	if err := h.ui.Click("Tags"); err != nil {
		t.Fatalf("re-expand Tags header: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("lw"); !ok {
		t.Fatal("lw did not reappear after expanding Tags")
	}
}

// The New Tag dialog creates a lightweight tag at HEAD when no message is
// given: it appears in the sidebar and decorates the commit in History.
func TestTagsCreateLightweightAtHead(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tags-create-light")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	if got := h.app.tagsUI().newTarget; got != "HEAD" {
		t.Fatalf("target prefill = %q, want HEAD without a History selection", got)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with empty name = %+v (found=%v), want disabled", btn, ok)
	}

	h.ui.Type("v9")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || btn.Disabled {
		t.Fatalf("Create Tag with a valid name = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "v9") {
		t.Fatal("v9 was not created in the repository")
	}
	if got := tagTypeOf(t, repo, "v9"); got != "commit" {
		t.Fatalf("v9 object type = %q, want commit (lightweight)", got)
	}
	if got := tagPointsAt(t, repo, "v9"); got != second {
		t.Fatalf("v9 target = %q, want HEAD %q", got, second)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if h.ui.HasText("Enter Tag Name") {
		t.Fatal("New Tag dialog stayed open after the create")
	}
	if _, ok := h.ui.Find("v9"); !ok {
		t.Fatalf("v9 missing from the sidebar after create; texts=%q", h.ui.Texts())
	}
	// The reloaded snapshot decorates the tagged commit in History.
	var decorated bool
	for _, cm := range h.app.session.Snapshot().Commits {
		for _, r := range cm.Refs {
			if r.Kind == git.RefTag && r.Name == "v9" {
				decorated = true
			}
		}
	}
	if !decorated {
		t.Fatal("v9 does not decorate the HEAD commit in the reloaded snapshot")
	}
}

// A non-empty message makes the tag annotated and carries the text.
func TestTagsCreateAnnotated(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tags-create-ann")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("rel-2")
	h.ui.Frame()
	focusField(t, h, "Tag message")
	h.ui.Type("second release notes")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if got := tagTypeOf(t, repo, "rel-2"); got != "tag" {
		t.Fatalf("rel-2 object type = %q, want tag (annotated)", got)
	}
	if got := strings.TrimSpace(repo.Git("for-each-ref", "--format=%(contents)", "refs/tags/rel-2")); got != "second release notes" {
		t.Fatalf("rel-2 message = %q, want %q", got, "second release notes")
	}
	if got := tagPointsAt(t, repo, "rel-2"); got != second {
		t.Fatalf("rel-2 commit = %q, want HEAD %q", got, second)
	}
}

// Editing the target field tags the commit it names instead of HEAD: a full
// hash of a non-HEAD commit lands the tag on exactly that commit.
func TestTagsCreateAtManualTarget(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tags-create-target")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	focusField(t, h, "Target commit")
	clearInput(t, h.ui)
	h.ui.Type(first)
	h.ui.Frame()
	if got := h.app.tagsUI().newTarget; got != first {
		t.Fatalf("target field did not take the edit: %q", got)
	}
	focusField(t, h, "Tag name")
	h.ui.Type("on-first")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "on-first") {
		t.Fatal("on-first was not created")
	}
	if got := tagPointsAt(t, repo, "on-first"); got != first {
		t.Fatalf("on-first target = %q, want the first commit %q", got, first)
	}
	if got := tagPointsAt(t, repo, "on-first"); got == second {
		t.Fatal("on-first landed on HEAD instead of the typed commit")
	}
}

// With a commit selected on the History page, the dialog pre-fills the target
// with it and says what it is; an annotated tag lands exactly there.
func TestTagsCreateAtHistorySelection(t *testing.T) {
	repo, first, _ := tagsSidebarRepo(t, "tags-create-hist")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	clickCommitRow(t, h, first, "first commit")
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	if got := h.app.tagsUI().newTarget; got != first {
		t.Fatalf("target prefill = %q, want the selected commit %q", got, first)
	}
	if !h.ui.HasText("first commit") {
		t.Fatalf("selected commit subject hint not shown; texts=%q", h.ui.Texts())
	}

	h.ui.Type("sel-ann")
	h.ui.Frame()
	focusField(t, h, "Tag message")
	h.ui.Type("tagging the first commit")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if got := tagTypeOf(t, repo, "sel-ann"); got != "tag" {
		t.Fatalf("sel-ann object type = %q, want tag (annotated)", got)
	}
	if got := tagPointsAt(t, repo, "sel-ann"); got != first {
		t.Fatalf("sel-ann target = %q, want the selected commit %q", got, first)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
}

// The dialog validates the typed name live — illegal characters, whitespace,
// dash-leading names and collisions with existing tags disable Create Tag.
func TestTagsNewTagDialogValidation(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-validate")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()

	h.ui.Type("bad..name")
	h.ui.Frame()
	want := git.RefNameProblem("bad..name")
	if want == "" {
		t.Fatal(`git.RefNameProblem("bad..name") is empty; scenario invalid`)
	}
	if !h.ui.HasText(want) {
		t.Fatalf("illegal name problem %q not shown; texts=%q", want, h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with illegal name = %+v, want disabled", btn)
	}

	clearInput(t, h.ui)
	h.ui.Type("lw")
	h.ui.Frame()
	if !h.ui.HasText("already exists") {
		t.Fatalf("duplicate name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with duplicate name = %+v, want disabled", btn)
	}

	// A name starting with "-" is rejected: git would read it as options
	// (git tag -l lists instead of creating).
	clearInput(t, h.ui)
	h.ui.Type("-l")
	h.ui.Frame()
	if !h.ui.HasText("cannot begin with '-'") {
		t.Fatalf("dash-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with dash name = %+v, want disabled", btn)
	}

	// A name holding whitespace is rejected: it would split into arguments.
	clearInput(t, h.ui)
	h.ui.Type("has space")
	h.ui.Frame()
	if !h.ui.HasText("cannot contain whitespace") {
		t.Fatalf("whitespace problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with whitespace name = %+v, want disabled", btn)
	}

	// A nested name is valid and enables submit.
	clearInput(t, h.ui)
	h.ui.Type("release/x")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || btn.Disabled {
		t.Fatalf("Create Tag with nested name release/x = %+v, want enabled", btn)
	}
}

// A repository without commits cannot tag HEAD: the dialog says why and
// Create Tag stays disabled.
func TestTagsNewTagUnbornRepoBlocked(t *testing.T) {
	repo := newGitRepo(t, "tags-unborn")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("v1")
	h.ui.Frame()
	if !h.ui.HasText("no commits yet") {
		t.Fatalf("unborn repository note not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag on an unborn repository = %+v, want disabled", btn)
	}
}

// A tag created outside the app between the dialog and the submit is never
// overwritten: the worker refuses, the error surfaces, and the original tag
// keeps its commit.
func TestTagsCreateRefusesExistingTag(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tags-no-overwrite")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("external")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || btn.Disabled {
		t.Fatalf("Create Tag before the external create = %+v, want enabled", btn)
	}

	// The name is taken externally after validation passed, on the first
	// commit rather than HEAD.
	repo.Git("tag", "external", first)

	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("overwriting create produced no error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite refusal = %q, want it to say the tag exists", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after refused overwrite; texts=%q", h.ui.Texts())
	}
	if got := tagPointsAt(t, repo, "external"); got != first {
		t.Fatalf("external tag moved to %q, want the original %q untouched", got, first)
	}
	if got := tagPointsAt(t, repo, "external"); got == second {
		t.Fatal("external tag was overwritten onto HEAD")
	}
}

// Deleting a lightweight tag asks once: the dialog reports the kind and the
// commit, and Confirm removes it from the repository and the sidebar.
func TestTagsDeleteLightweight(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tags-delete-lw")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagDeleteDialog(t, h, "lw")
	h.drain()

	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(short7(second)) {
		t.Fatalf("target commit short hash not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if tagRefExists(t, repo, "lw") {
		t.Fatal("lw survived Confirm Delete Tag")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if _, ok := h.ui.Find("lw"); ok {
		t.Fatal("lw stayed visible in the sidebar after delete")
	}
	if !tagRefExists(t, repo, "ann") {
		t.Fatal("delete removed the wrong tag: ann is gone")
	}
}

// Deleting an annotated tag shows its kind and message summary before the
// confirmed delete removes it.
func TestTagsDeleteAnnotated(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-delete-ann")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagDeleteDialog(t, h, "ann")
	h.drain()

	if !h.ui.HasText("Annotated tag") {
		t.Fatalf("annotated kind not shown; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("annotated release") {
		t.Fatalf("annotated message summary not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if tagRefExists(t, repo, "ann") {
		t.Fatal("ann survived Confirm Delete Tag")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
}

// The context menu of a tag row opens the same confirmation dialog.
func TestTagsDeleteViaContextMenu(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-delete-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("choose Delete… in the context menu: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("delete dialog did not open from the context menu; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted even though the dialog was canceled")
	}
}

// While the verification is still running the dialog reports it and offers
// no confirm button at all: a tag whose state is not yet known is never
// deleted. The worker is held back with PauseWorkers so the in-progress state
// is deterministic, then released so the flow completes.
func TestTagsDeleteCheckingInProgressHasNoConfirm(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-delete-checking")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()

	openTagDeleteDialog(t, h, "lw")

	// The verification worker exists and is held before running.
	check := h.queue.WaitWorker(t)

	if !h.ui.HasText("Checking tag details") {
		t.Fatalf("checking-in-progress note not shown; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tag"); err == nil {
		t.Fatal("Confirm Delete Tag was clickable while the verification was still running")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); ok {
		t.Fatalf("Confirm Delete Tag present during the check: %+v", btn)
	}

	// Releasing the worker lets the verification answer and the flow complete.
	h.queue.ReleaseWorker(check)
	h.queue.ResumeWorkers()
	h.drain()

	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown after releasing the check; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag after the check: %v", err)
	}
	h.drain()
	if tagRefExists(t, repo, "lw") {
		t.Fatal("lw survived Confirm Delete Tag after the check completed")
	}
}

// When the verification itself fails the tag is never deleted: no confirm
// button appears, the error shows verbatim in the dialog, and canceling is
// the only way out. The failure is real: the repository's object store is
// moved away after the sidebar loaded, so git cannot even discover the
// repository anymore — while deleting the loose ref file would still be a
// file operation needing no objects, which is exactly why the check must
// gate the delete.
func TestTagsDeleteCheckFailureProtects(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-delete-checkfail")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	objects := filepath.Join(repo.Path(), ".git", "objects")
	backup := filepath.Join(repo.Path(), ".git", "objects-backup")
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("move object store away: %v", err)
	}
	// Verify with the Git CLI that the verification really fails before
	// declaring the scenario valid.
	if out, err := tryGitCommand(repo.Path(), "show-ref", "--verify", "refs/tags/lw"); err == nil {
		t.Fatalf("corrupted repo still passes show-ref; scenario invalid: %s", out)
	}

	openTagDeleteDialog(t, h, "lw")
	h.drain()

	if !h.ui.HasText("will not be deleted") {
		t.Fatalf("check failure protection note not shown; texts=%q", h.ui.Texts())
	}
	// The dialog shows the verification's error verbatim, not only a fixed note.
	if !h.ui.HasText("verify tag lw") {
		t.Fatalf("check failure error text not shown in the dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tag"); err == nil {
		t.Fatal("Confirm Delete Tag was clickable after the verification failed")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); ok {
		t.Fatalf("Confirm Delete Tag present after the check failed: %+v", btn)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	// The repository is corrupted on purpose, so git cannot answer here; what
	// proves nothing was deleted is the ref file surviving.
	if _, err := os.Stat(filepath.Join(repo.Path(), ".git", "refs", "tags", "lw")); err != nil {
		t.Fatalf("lw ref file is gone even though the verification failed: %v", err)
	}
}

// A stale answer cannot delete a tag that changed since the dialog opened:
// the worker re-verifies on the repository, so when the tag was retargeted
// after the check answered, the delete fails with an error and the tag
// survives on its new commit.
func TestTagsDeleteRefusedAfterExternalRetarget(t *testing.T) {
	repo, first, second := tagsSidebarRepo(t, "tags-delete-retarget")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagDeleteDialog(t, h, "lw")
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown; texts=%q", h.ui.Texts())
	}

	// The tag moves onto the first commit after the check answered.
	repo.Git("update-ref", "refs/tags/lw", first)

	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted after being retargeted since the check")
	}
	if got := tagPointsAt(t, repo, "lw"); got != first {
		t.Fatalf("lw target = %q, want the retargeted %q untouched", got, first)
	}
	if got := tagPointsAt(t, repo, "lw"); got == second {
		t.Fatal("fixture broken: lw was never retargeted away from HEAD")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale delete produced no error")
	} else if !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale delete refusal = %q, want it to say the tag changed", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after refused stale delete; texts=%q", h.ui.Texts())
	}
}

// A recreated annotated tag is a different tag object even onto the same
// commit: the delete confirmed against the original object is refused.
func TestTagsDeleteRefusedAfterRecreate(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "tags-delete-recreate")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openTagDeleteDialog(t, h, "ann")
	h.drain()
	if !h.ui.HasText("Annotated tag") {
		t.Fatalf("annotated kind not shown; texts=%q", h.ui.Texts())
	}

	// Delete and recreate the tag externally onto the very same commit with a
	// different message: the commit does not change but the tag object does.
	repo.Git("tag", "-d", "ann")
	repo.Git("tag", "-a", "ann", "-m", "recreated externally", "HEAD")
	if got := tagPointsAt(t, repo, "ann"); got != second {
		t.Fatalf("fixture: recreated ann commit = %q, want HEAD %q", got, second)
	}

	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if !tagRefExists(t, repo, "ann") {
		t.Fatal("recreated ann was deleted even though it is not the confirmed tag")
	}
	if got := strings.TrimSpace(repo.Git("for-each-ref", "--format=%(contents)", "refs/tags/ann")); got != "recreated externally" {
		t.Fatalf("ann message = %q, want the recreated tag untouched", got)
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("delete of a recreated tag produced no error")
	}
}

// A tag whose name starts with "-" — creatable through the ref store, as any
// ref is — is deleted as a tag, never parsed as command-line options.
func TestTagsDeleteDashNamedTag(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-delete-dash")
	repo.Git("update-ref", "refs/tags/-v", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if _, ok := h.ui.Find("-v"); !ok {
		t.Fatalf("dash tag -v missing from the sidebar; texts=%q", h.ui.Texts())
	}
	openTagDeleteDialog(t, h, "-v")
	h.drain()
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if tagRefExists(t, repo, "-v") {
		t.Fatal("dash tag -v survived Confirm Delete Tag: the name was treated as options")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected dash tag delete error: %v", err)
	}
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("delete removed the wrong tag: lw is gone")
	}
}

// Tag dialogs are scoped to the repository: switching repositories discards
// the drafts and the selection.
func TestTagsDialogsResetOnRepoSwitch(t *testing.T) {
	repoA, _, _ := tagsSidebarRepo(t, "tags-switch-a")
	repoB := newGitRepo(t, "tags-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	if !h.app.tagsUI().newOpen {
		t.Fatal("new tag dialog did not open")
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()

	st := h.app.tagsUI()
	if st.newOpen {
		t.Fatal("new tag dialog survived a repository switch")
	}
	if st.delOpen {
		t.Fatal("delete dialog survived a repository switch")
	}
	if got := pickedTagNames(h); len(got) != 0 {
		t.Fatalf("tag selection %q survived a repository switch", got)
	}
}

// A selected tag and an open delete dialog of one repository are gone after
// switching to another, and the in-flight verification is invalidated.
func TestTagsSelectionAndDeleteDialogResetOnRepoSwitch(t *testing.T) {
	repoA, _, _ := tagsSidebarRepo(t, "tags-switchsel-a")
	repoB := newGitRepo(t, "tags-switchsel-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	openTagDeleteDialog(t, h, "lw")
	h.drain()
	if !h.app.tagsUI().delOpen {
		t.Fatal("delete dialog did not open")
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()

	st := h.app.tagsUI()
	if got := pickedTagNames(h); len(got) != 0 {
		t.Fatalf("tag selection %q survived a repository switch", got)
	}
	if st.delOpen {
		t.Fatal("delete dialog survived a repository switch")
	}
}

// A repository without tags keeps the No tags placeholder, and without a
// repository open the New Tag entry is hidden entirely: there is nothing to
// tag.
func TestTagsPlaceholderAndHiddenEntryWithoutRepo(t *testing.T) {
	// No repository open: no entry at all.
	h := newTestApp(t)
	h.ui.Frame()
	if _, ok := h.ui.Find("New Tag"); ok {
		t.Fatal("New Tag entry visible without a repository open")
	}

	// A repository with commits but no tags shows the placeholder.
	repo := newGitRepo(t, "tags-placeholder")
	repo.CommitFile("a.txt", "one\n", "first commit")
	h.openRepo(repo.StoreRepo())
	h.drain()
	if !h.ui.HasText("No tags") {
		t.Fatalf("No tags placeholder missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("New Tag"); !ok {
		t.Fatalf("New Tag entry missing with a repository open; texts=%q", h.ui.Texts())
	}
}

// --- Loopback SSH variants ---

// sshTagRefExists reports whether refs/tags/<name> exists on the server.
func sshTagRefExists(t *testing.T, srv *sshtest.Target, remoteDir, name string) bool {
	t.Helper()
	out := srv.RunGit(t, remoteDir, "for-each-ref", "--format=%(refname)", "refs/tags/"+name)
	return strings.TrimSpace(out) == "refs/tags/"+name
}

// Opening a loopback SSH repository, creating lightweight and annotated tags
// through the real dialog — the annotated one on a non-HEAD commit typed into
// the target field — lands them on the server with the right kinds, messages
// and commits.
func TestTagsCreateLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// A second commit on the server, so the first one is a non-HEAD target.
	first := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	srv.WriteFile(t, repo.Path, "b.txt", []byte("two\n"))
	srv.RunGit(t, repo.Path, "add", "b.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "second commit")
	second := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// A lightweight tag at HEAD.
	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("ssh-lw")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if !sshTagRefExists(t, srv, repo.Path, "ssh-lw") {
		t.Fatal("ssh-lw was not created on the server")
	}
	if got := srv.RunGit(t, repo.Path, "cat-file", "-t", "refs/tags/ssh-lw"); got != "commit" {
		t.Fatalf("ssh-lw object type = %q, want commit (lightweight)", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "refs/tags/ssh-lw"); got != second {
		t.Fatalf("ssh-lw target = %q, want HEAD %q", got, second)
	}

	// An annotated tag on the first commit, typed into the target field.
	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag again: %v", err)
	}
	h.ui.Frame()
	focusField(t, h, "Target commit")
	clearInput(t, h.ui)
	h.ui.Type(first)
	h.ui.Frame()
	focusField(t, h, "Tag name")
	h.ui.Type("ssh-ann")
	h.ui.Frame()
	focusField(t, h, "Tag message")
	h.ui.Type("annotated over ssh")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if got := srv.RunGit(t, repo.Path, "cat-file", "-t", "refs/tags/ssh-ann"); got != "tag" {
		t.Fatalf("ssh-ann object type = %q, want tag (annotated)", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "refs/tags/ssh-ann^{}"); got != first {
		t.Fatalf("ssh-ann commit = %q, want the non-HEAD commit %q", got, first)
	}
	if got := srv.RunGit(t, repo.Path, "for-each-ref", "--format=%(contents)", "refs/tags/ssh-ann"); strings.TrimSpace(got) != "annotated over ssh" {
		t.Fatalf("ssh-ann message = %q, want %q", got, "annotated over ssh")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH create error: %v", err)
	}
	if _, ok := h.ui.Find("ssh-ann"); !ok {
		t.Fatalf("ssh-ann missing from the sidebar after create; texts=%q", h.ui.Texts())
	}
}

// The protected delete on a loopback SSH repository: a retargeted tag is
// never deleted and the refusal surfaces, while a tag still pointing at the
// verified object — including one whose name starts with "-" — deletes.
func TestTagsDeleteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// A second commit, then the tags the sidebar must load with.
	srv.WriteFile(t, repo.Path, "b.txt", []byte("two\n"))
	srv.RunGit(t, repo.Path, "add", "b.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "second commit")
	first := srv.RunGit(t, repo.Path, "rev-parse", "HEAD~1")
	srv.RunGit(t, repo.Path, "tag", "ssh-del", "HEAD")
	srv.RunGit(t, repo.Path, "update-ref", "refs/tags/-v", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, want := range []string{"ssh-del", "-v"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("SSH sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}

	// The verification answers, then the tag is retargeted on the server
	// behind the dialog's back: the delete is refused and the tag survives.
	openTagDeleteDialog(t, h, "ssh-del")
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("SSH lightweight kind not shown; texts=%q", h.ui.Texts())
	}
	srv.RunGit(t, repo.Path, "update-ref", "refs/tags/ssh-del", first)
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag: %v", err)
	}
	h.drain()

	if !sshTagRefExists(t, srv, repo.Path, "ssh-del") {
		t.Fatal("ssh-del was deleted after being retargeted on the server")
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "refs/tags/ssh-del"); got != first {
		t.Fatalf("ssh-del target = %q, want the retargeted %q untouched", got, first)
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale SSH delete produced no error")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after refused SSH delete; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("dismiss the refusal: %v", err)
	}
	h.ui.Frame()

	// Reopening the dialog re-verifies against the retargeted tag: the
	// confirmed delete removes it on the server.
	openTagDeleteDialog(t, h, "ssh-del")
	h.drain()
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag after re-verifying: %v", err)
	}
	h.drain()
	if sshTagRefExists(t, srv, repo.Path, "ssh-del") {
		t.Fatal("ssh-del survived a confirmed delete on the server")
	}

	// The dash-named tag deletes through the "--"-isolated command line.
	openTagDeleteDialog(t, h, "-v")
	h.drain()
	if err := h.ui.Click("Confirm Delete Tag"); err != nil {
		t.Fatalf("click Confirm Delete Tag for -v: %v", err)
	}
	h.drain()
	if sshTagRefExists(t, srv, repo.Path, "-v") {
		t.Fatal("dash tag -v survived Confirm Delete Tag on the server")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH delete error: %v", err)
	}
}

// pausedWorkerCount reports how many workers were spawned while the queue
// was paused, so a test can prove that pressing a control started none.
func pausedWorkerCount(h *testHarness) int {
	h.queue.mu.Lock()
	defer h.queue.mu.Unlock()
	return len(h.queue.allGate)
}

// While a repository write is in flight every tag write entry is disabled —
// the row's Delete… button, the row menu's Delete…, Create Tag in the New Tag
// dialog and Confirm Delete Tag in the delete dialog — and pressing them
// starts no second worker. The busy write is a real tag creation through the
// New Tag dialog whose worker is held with PauseWorkers.
func TestTagsWriteActionsDisabledWhileBusy(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "tags-busy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagRow(t, h, "lw")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); !ok || btn.Disabled {
		t.Fatalf("Delete tag lw before the write = %+v found=%v, want enabled", btn, ok)
	}

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("busy-first")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ReleaseWorker(write)
		t.Fatal("session not busy while the create worker is held")
	}
	spawned := pausedWorkerCount(h)

	// Row action bar: Delete… is disabled and pressing it opens nothing.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Delete tag lw"); !ok || !btn.Disabled {
		t.Fatalf("Delete tag lw while busy = %+v found=%v, want disabled", btn, ok)
	}
	_ = h.ui.Click("Delete tag lw")
	h.ui.Frame()
	if h.app.tagsUI().delOpen {
		t.Fatal("delete dialog opened from the disabled row button")
	}

	// Row context menu: Delete… is disabled.
	if err := h.ui.RightClick("lw"); err != nil {
		t.Fatalf("right-click lw row while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Delete… while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	if h.app.tagsUI().delOpen {
		t.Fatal("delete dialog opened from the disabled menu item")
	}

	// New Tag dialog: a valid, free name still leaves Create Tag disabled.
	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag while busy: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("busy-second")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag while busy = %+v found=%v, want disabled", btn, ok)
	}
	_ = h.ui.Click("Create Tag")
	h.ui.Frame()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if got := pausedWorkerCount(h); got != spawned {
		t.Fatalf("pressing disabled tag controls spawned %d worker(s)", got-spawned)
	}

	h.queue.ReleaseWorker(write)
	h.queue.ResumeWorkers()
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("create write failed: %v", err)
	}
	if !tagRefExists(t, repo, "busy-first") {
		t.Fatal("the held create write did not create busy-first")
	}
	if tagRefExists(t, repo, "busy-second") {
		t.Fatal("busy-second was created from the disabled Create Tag")
	}
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted while the session was busy")
	}

	// Delete dialog: verified first, then a write holds the session. The
	// dialog is modal, so no sidebar or toolbar entry can start that write;
	// it is started on the session directly to reach the state the guard
	// protects against.
	openTagDeleteDialog(t, h, "lw")
	h.drain()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Tag after the check = %+v found=%v, want enabled", btn, ok)
	}
	h.queue.PauseWorkers()
	if !h.app.session.do("Hold the session busy", func(*git.Repo) error { return nil }) {
		t.Fatal("session refused the holding write")
	}
	hold := h.queue.WaitWorker(t)
	h.ui.Frame()
	spawned = pausedWorkerCount(h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); !ok || !btn.Disabled {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("Confirm Delete Tag while busy = %+v found=%v, want disabled", btn, ok)
	}
	_ = h.ui.Click("Confirm Delete Tag")
	h.ui.Frame()
	if got := pausedWorkerCount(h); got != spawned {
		h.queue.ReleaseWorker(hold)
		t.Fatalf("pressing the disabled Confirm Delete Tag spawned %d worker(s)", got-spawned)
	}
	if !h.app.tagsUI().delOpen {
		h.queue.ReleaseWorker(hold)
		t.Fatal("the disabled Confirm Delete Tag closed the dialog")
	}

	h.queue.ReleaseWorker(hold)
	h.queue.ResumeWorkers()
	h.drain()
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted by the disabled Confirm Delete Tag")
	}
}
