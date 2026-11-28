package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the shared-frame migration of the New Tag,
// Delete Tag and Interactive Rebase dialogs through the real UI: the sidebar
// entry, the tag row's context menu and the commit row's context menu open
// them, key presses go through the window's input path, and git itself is
// the measure of what ran. Their rules are the frame's: the name field of
// New Tag owns Enter while it is focused, the message editor takes it as a
// line break, Delete Tag is always destructive and never answers Enter, and
// the Interactive Rebase form is destructive exactly while its plan drops
// commits.

// focusIRBackupCheckbox puts the keyboard focus on the form's backup-branch
// checkbox — ticking it on and back off, so the plan the Enter measures
// stays the plain one. Toggles take Space, not Enter, so the key pressed
// with the focus here can only go to the frame's window-level shortcut, or
// nowhere when the dialog is destructive: exactly the distinction the Enter
// tests measure. Clicking plain text instead would clear the focus out of
// the dialog's scope entirely, where Enter reaches nothing either way and
// the test would prove nothing.
func focusIRBackupCheckbox(t *testing.T, h *testHarness) {
	t.Helper()
	for range 2 {
		if err := h.ui.Click("Create backup branch"); err != nil {
			t.Fatalf("click the backup branch checkbox: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Create backup branch")
	if !ok || !box.Focused {
		t.Fatalf("backup branch checkbox not focused after its clicks: %+v found=%v", box, ok)
	}
	if box.Checked {
		t.Fatal("backup branch checkbox did not toggle back off")
	}
}

// Opening New Tag puts the focus in the Tag name field, and Enter there —
// the field's own submit, the one channel the single-line input leaves to
// the keyboard — creates the tag exactly once: the write succeeds, the tag
// points at HEAD's commit, and the dialog closes behind it.
func TestDialogTagsRebaseNewTagEnterCreatesOnce(t *testing.T) {
	repo, _, second := tagsSidebarRepo(t, "dtr-newtag-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	if !h.ui.Focused("Tag name") {
		t.Fatalf("Tag name not focused at open; texts=%q", h.ui.Texts())
	}

	h.ui.Type("v9")
	h.ui.Frame()
	if !h.ui.Focused("Tag name") {
		t.Fatal("Tag name lost the focus while typing — Enter would not measure the field's own submit")
	}
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		// A second confirmation of the same Enter would meet the created tag
		// and fail the write, so a clean run is the exact measure of once.
		t.Fatalf("create tag by Enter failed or ran twice: %v", err)
	}
	if !tagRefExists(t, repo, "v9") {
		t.Fatal("v9 was not created by Enter in the name field")
	}
	if got := tagTypeOf(t, repo, "v9"); got != "commit" {
		t.Fatalf("v9 object type = %q, want commit (lightweight)", got)
	}
	if got := tagPointsAt(t, repo, "v9"); got != second {
		t.Fatalf("v9 target = %q, want HEAD %q", got, second)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "New Tag"); ok {
		t.Error("the New Tag dialog is still open after the Enter create")
	}
	if st := h.app.tagsUI(); st.newOpen {
		t.Error("the New Tag dialog state is still open after the Enter create")
	}
}

// Enter inside the Message text is its line break: the text grows by one
// newline, no tag is created even with a valid name typed, and the dialog
// stays open.
func TestDialogTagsRebaseNewTagMessageEnterNewline(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "dtr-newtag-message")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("msgnl")
	h.ui.Frame()
	focusField(t, h, "Tag message")
	box, ok := h.ui.AccessByRole(ui.RoleTextField, "Tag message")
	if !ok || !box.Focused {
		t.Fatalf("message editor not focused after its click: %+v found=%v", box, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	box, ok = h.ui.AccessByRole(ui.RoleTextField, "Tag message")
	if !ok {
		t.Fatalf("message editor gone after Enter; texts=%q", h.ui.Texts())
	}
	if box.Value != "\n" {
		t.Errorf("message after Enter = %q, want exactly one line break", box.Value)
	}
	if tagRefExists(t, repo, "msgnl") {
		t.Error("Enter in the message editor created the tag")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "New Tag"); !ok {
		t.Error("Enter in the message editor closed the dialog")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// A name git cannot carry — here the doubled dot — disables Create Tag, and
// Enter in the name field creates nothing: the dialog keeps its draft open
// and no write runs.
func TestDialogTagsRebaseNewTagInvalidNameEnterCreatesNothing(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "dtr-newtag-invalid")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Tag"); err != nil {
		t.Fatalf("click New Tag: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("bad..name")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Create Tag"); !ok || !btn.Disabled {
		t.Fatalf("Create Tag with an invalid name = %+v (found=%v), want disabled", btn, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if tagRefExists(t, repo, "bad..name") {
		t.Error("Enter with an invalid name created the tag")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter with an invalid name ran a write that failed: %v", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "New Tag"); !ok {
		t.Error("Enter with an invalid name closed the dialog")
	}
	if st := h.app.tagsUI(); strings.TrimSpace(st.newName) != "bad..name" {
		t.Errorf("name draft after Enter = %q, want the typed %q — the draft must survive", strings.TrimSpace(st.newName), "bad..name")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Delete Tag is destructive: while the verification runs its confirm button
// is present and disabled, and once the check answers the button enables in
// the theme's danger color — red, because the delete discards the ref.
func TestDialogTagsRebaseDeleteCheckingDisabledThenRed(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "dtr-delete-red")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()

	// The real entry is the tag row's context menu.
	chooseTagMenuItem(t, h, "lw", "Delete…", "Delete Tag")

	check := h.queue.WaitWorker(t)
	if !h.ui.HasText("Checking tag details") {
		t.Fatalf("checking-in-progress note not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); !ok || !btn.Disabled {
		t.Fatalf("Confirm Delete Tag during the check = %+v (found=%v), want present and disabled", btn, ok)
	}

	h.queue.ReleaseWorker(check)
	h.queue.ResumeWorkers()
	h.drain()

	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown after the check; texts=%q", h.ui.Texts())
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag")
	if !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Tag after the check = %+v (found=%v), want present and enabled", btn, ok)
	}
	fill := buttonFillSample(t, h.ui, "Confirm Delete Tag")
	danger := paletteForTheme(nil).danger
	if !fillWithin(fill, danger, 12) {
		t.Errorf("destructive confirm fill = %v, want within 12 of the danger color %v", fill, danger)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted even though the dialog was canceled")
	}
}

// Enter on the open Delete Tag dialog never deletes: the dialog is
// destructive, so the key is the focused cancel's own — the dialog closes
// and the tag is exactly where it was.
func TestDialogTagsRebaseDeleteEnterKeepsTag(t *testing.T) {
	repo, _, _ := tagsSidebarRepo(t, "dtr-delete-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Delete…", "Delete Tag")
	h.drain()
	if !h.ui.HasText("Lightweight tag") {
		t.Fatalf("lightweight kind not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Tag"); !ok {
		t.Fatalf("Confirm Delete Tag missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Delete Tag button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if !tagRefExists(t, repo, "lw") {
		t.Fatal("lw was deleted by Enter on the destructive dialog")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Delete Tag"); ok {
		t.Error("the dialog is still open after Enter landed on its focused cancel")
	}
}

// A plan keeping every commit is not destructive: the Rebase button is not
// red, and Enter — with the focus off the buttons, so only the frame's
// window-level shortcut can take it — starts the rebase exactly like the
// click, replaying the rows in the order the form shows.
func TestDialogTagsRebaseIREnterStartsRebase(t *testing.T) {
	repo := newGitRepo(t, "dtr-ir-enter")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || btn.Disabled {
		t.Fatalf("Rebase with a complete plan = %+v (found=%v), want present and enabled", btn, ok)
	}
	fill := buttonFillSample(t, h.ui, "Rebase")
	if danger := paletteForTheme(nil).danger; fillWithin(fill, danger, 12) {
		t.Errorf("Rebase fill %v is the danger color %v with no commit dropped — the plan is not destructive", fill, danger)
	}

	// Moving ir two down once makes the execution order two, one, three: a
	// reordering git's own log reads back unambiguously.
	if err := h.ui.Click("Move Down " + irShort(hashes[1])); err != nil {
		t.Fatalf("move ir two down: %v", err)
	}
	h.ui.Frame()
	focusIRBackupCheckbox(t, h)

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase by Enter failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir three", "ir one", "ir two", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after the Enter rebase = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)
}

// Choosing Drop on a row makes the dialog destructive: the Rebase button
// turns red and Enter changes nothing — the branch, the commit count and the
// open form are exactly as before — until the user clicks the button, which
// drops the commit.
func TestDialogTagsRebaseIRDropEnterDoesNotStart(t *testing.T) {
	repo := newGitRepo(t, "dtr-ir-drop-enter")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two", "ir three", "ir four")
	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[2], "Pick", "Drop")
	if !hasExactText(h.ui, "1 commit(s) will be dropped.") {
		t.Fatalf("drop notice missing; texts=%q", h.ui.Texts())
	}
	fill := buttonFillSample(t, h.ui, "Rebase")
	danger := paletteForTheme(nil).danger
	if !fillWithin(fill, danger, 12) {
		t.Errorf("Rebase fill = %v with a dropped commit, want within 12 of the danger color %v", fill, danger)
	}

	// With the focus on a control that leaves Enter to the window, a
	// non-destructive frame's shortcut would confirm here — so nothing
	// happening is the destructive protection itself, not a dead key.
	focusIRBackupCheckbox(t, h)
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main after Enter = %q, want unchanged %q — Enter started the destructive rebase", got, mainBefore)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); count != "5" {
		t.Fatalf("commit count after Enter = %q, want 5", count)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatal("Enter closed the destructive form")
	}

	// The click is the destructive confirmation: the drop it describes runs.
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir four", "ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)
}

// Cancel and Escape each close the form exactly once, clearing the draft:
// the rows, the base and the open flag are all gone afterwards, and a
// reopened form reads the plan fresh.
func TestDialogTagsRebaseIRCancelAndEscapeCloseOnce(t *testing.T) {
	repo := newGitRepo(t, "dtr-ir-close")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	irLinearFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRDialogClosed(t, h)
	if st := interactiveRebaseUI(h.app); st.open || st.rows != nil || st.onto != "" {
		t.Errorf("draft after Cancel: open=%v rows=%d onto=%q, want closed and cleared", st.open, len(st.rows), st.onto)
	}

	// Escape closes the same way: the form the menu reopens reads the plan
	// again, and the key clears every piece of the draft with it.
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)
	if rows := len(interactiveRebaseUI(h.app).rows); rows != 2 {
		t.Fatalf("reopened form rows = %d, want 2", rows)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	assertIRDialogClosed(t, h)
	if st := interactiveRebaseUI(h.app); st.open || st.rows != nil || st.onto != "" {
		t.Errorf("draft after Escape: open=%v rows=%d onto=%q, want closed and cleared", st.open, len(st.rows), st.onto)
	}

	// Neither closing ran a write.
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("closing the form ran a write that failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir two", "ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after canceling and escaping = %v, want unchanged %v", subjects, want)
	}
}
