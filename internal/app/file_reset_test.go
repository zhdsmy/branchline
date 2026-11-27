package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// fileResetHashes are the commits of the shared file-reset fixture.
type fileResetHashes struct {
	c1, c2, c3, c4, c5, c6, c7 string
}

// fileResetFixture builds a linear history exercising every file outcome a
// reset can meet: a.txt is created in the root commit and modified twice
// before its v4; b.txt is created in the root, deleted in c4 and re-created
// in c5; c.txt is renamed to d.txt in c6, the rename being the commit's only
// change so the rename detection is exact; z.txt is added in c7.
func fileResetFixture(t *testing.T, name string) (*gitRepoFixture, fileResetHashes) {
	t.Helper()
	repo := newGitRepo(t, name)
	var h fileResetHashes
	repo.WriteFile("a.txt", "v1\n")
	repo.WriteFile("b.txt", "b one\n")
	repo.WriteFile("c.txt", "c content\n")
	repo.Git("add", "--", "a.txt", "b.txt", "c.txt")
	repo.Git("commit", "-m", "initial files")
	h.c1 = headOf(t, repo)
	h.c2 = repo.CommitFile("a.txt", "v2\n", "a v2")
	h.c3 = repo.CommitFile("a.txt", "v3\n", "a v3")
	repo.Git("rm", "-q", "b.txt")
	repo.Git("commit", "-m", "delete b")
	h.c4 = headOf(t, repo)
	h.c5 = repo.CommitFile("b.txt", "b restored\n", "restore b")
	repo.Git("mv", "c.txt", "d.txt")
	repo.Git("commit", "-m", "rename c to d")
	h.c6 = headOf(t, repo)
	repo.WriteFile("a.txt", "v4\n")
	repo.WriteFile("z.txt", "z\n")
	repo.Git("add", "--", "a.txt", "z.txt")
	repo.Git("commit", "-m", "a v4 with z")
	h.c7 = headOf(t, repo)
	return repo, h
}

// assertFileMatchesRev verifies the file is exactly the revision's version of
// it in both the working tree and the index.
func assertFileMatchesRev(t *testing.T, repo *gitRepoFixture, path, rev string) {
	t.Helper()
	want := repo.Git("show", rev+":"+path)
	got, err := os.ReadFile(filepath.Join(repo.Path(), filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read working-tree %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("working-tree %s = %q, want %q (the version of %s)", path, string(got), want, short7(rev))
	}
	if idx := repo.Git("show", ":"+path); idx != want {
		t.Fatalf("index %s = %q, want %q (the version of %s)", path, idx, want, short7(rev))
	}
}

// assertFileResetGone verifies the file is in neither the working tree nor
// the index.
func assertFileResetGone(t *testing.T, repo *gitRepoFixture, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo.Path(), filepath.FromSlash(path))); !os.IsNotExist(err) {
		t.Fatalf("working-tree %s still present (err=%v), want deleted", path, err)
	}
	if out := strings.TrimSpace(repo.Git("ls-files", "--", path)); out != "" {
		t.Fatalf("index still lists %s: %q, want gone", path, out)
	}
}

// chooseFileReset clicks the commit's row, opens the context menu of the file
// row in its detail, chooses the reset item, and verifies the dialog opened
// with the expected title.
func chooseFileReset(t *testing.T, h *testHarness, hash, subject, path, item, title string) {
	t.Helper()
	clickCommitRow(t, h, hash, subject)
	h.drain()
	chooseFileMenuItem(t, h, fileRowLabel(path), item)
	h.ui.Frame()
	if !hasExactText(h.ui, title) {
		t.Fatalf("file reset dialog %q did not open; texts=%q", title, h.ui.Texts())
	}
}

// confirmFileReset clicks the dialog's Reset button and drains the write and
// the reload it schedules.
func confirmFileReset(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
}

// The file row's context menu ends with a separator and the two reset items
// after File History and Blame, and resetting a modified file to its state at
// the commit puts exactly that version into the working tree and the index.
func TestFileResetAtCommitRestoresModifiedFile(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-at-modified")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, fx.c3, "a v3")
	h.drain()

	// The menu order is exact: the two read-only entries, a separator, then
	// the two reset entries.
	if err := h.ui.RightClick(fileRowLabel("a.txt")); err != nil {
		t.Fatalf("right-click %q: %v\ntexts=%q", fileRowLabel("a.txt"), err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	wantMenu := []string{
		"Open in External Diff Tool", "File History…", "Blame…", "-",
		"Reset File to State at Commit…", "Reset File to State Before Commit…",
	}
	if !slices.Equal(menu, wantMenu) {
		t.Fatalf("file row menu = %q, want %q", menu, wantMenu)
	}
	if err := h.ui.ChooseMenuItem("Reset File to State at Commit…"); err != nil {
		t.Fatalf("choose reset-at item: %v", err)
	}
	h.ui.Frame()

	if !hasExactText(h.ui, "Reset File to State at Commit") {
		t.Fatalf("reset-at dialog did not open; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "a.txt will be reset to its state in "+short7(fx.c3)+".") {
		t.Fatalf("dialog missing the outcome line; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("will be lost") {
		t.Fatalf("warning shown for a clean file; texts=%q", h.ui.Texts())
	}

	confirmFileReset(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("file reset failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "a.txt", fx.c3)
	// HEAD and the selection did not move; the dialog is gone.
	if got := headOf(t, repo); got != fx.c7 {
		t.Fatalf("HEAD moved by a file reset: %q, want %q", got, fx.c7)
	}
	if h.app.historyView.sel != fx.c3 {
		t.Fatalf("history selection moved by the reset: %q, want %q", h.app.historyView.sel, fx.c3)
	}
	if hasExactText(h.ui, "Reset File to State at Commit") {
		t.Fatal("dialog still open after the reset")
	}
}

// Resetting a modified file to its state before the commit undoes exactly the
// commit's change to it: a.txt goes back from v3 to v2 in the working tree
// and the index.
func TestFileResetBeforeCommitRestoresModifiedFile(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-before-modified")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")

	if !hasExactText(h.ui, "a.txt will be reset to its state in "+short7(fx.c2)+".") {
		t.Fatalf("dialog names the first parent, not the commit; texts=%q", h.ui.Texts())
	}

	confirmFileReset(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("file reset failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "a.txt", fx.c2)
	if got := repo.Git("show", fx.c3+":a.txt"); got == "v2\n" {
		t.Fatal("fixture: v2 must differ from the commit's own version")
	}
}

// A file the commit deleted does not exist at the commit: resetting to that
// state deletes it from the working tree and the index, while resetting to
// before the commit brings the version of the first parent back.
func TestFileResetDeletedFileAtAndBefore(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-deleted")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// At the deleting commit, b.txt does not exist: it is deleted again, now
	// dropping the re-created "b restored" version of c5.
	chooseFileReset(t, h, fx.c4, "delete b", "b.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")
	if !hasExactText(h.ui, "b.txt does not exist in "+short7(fx.c4)+" and will be deleted.") {
		t.Fatalf("dialog missing the deletion line; texts=%q", h.ui.Texts())
	}
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("at-reset of deleted file failed: %v", err)
	}
	assertFileResetGone(t, repo, "b.txt")

	// Before the deleting commit b.txt still exists: the pre-deletion content
	// of the first parent comes back into the working tree and the index.
	chooseFileReset(t, h, fx.c4, "delete b", "b.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !hasExactText(h.ui, "b.txt will be reset to its state in "+short7(fx.c3)+".") {
		t.Fatalf("dialog missing the restore line; texts=%q", h.ui.Texts())
	}
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("before-reset of deleted file failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "b.txt", fx.c3)
	assertFileBytes(t, repo, "b.txt", "b one\n")
}

// A file the commit added does not exist before the commit: resetting to the
// state before deletes it, tracked as it is, from the working tree and the
// index. For a file added in the root commit the target is the empty tree,
// and the file is deleted exactly the same way.
func TestFileResetBeforeCommitDeletesAddedFile(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-added")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// z.txt enters in c7: before c7 it does not exist.
	chooseFileReset(t, h, fx.c7, "a v4 with z", "z.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !hasExactText(h.ui, "z.txt does not exist in "+short7(fx.c6)+" and will be deleted.") {
		t.Fatalf("dialog missing the deletion line for an added file; texts=%q", h.ui.Texts())
	}
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("before-reset of added file failed: %v", err)
	}
	assertFileResetGone(t, repo, "z.txt")
	// The reset touched only z.txt: a.txt keeps its v4.
	assertFileMatchesRev(t, repo, "a.txt", fx.c7)

	// a.txt enters in the root commit: before it there is no commit at all,
	// and the reset against the empty tree deletes it too.
	chooseFileReset(t, h, fx.c1, "initial files", "a.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !hasExactText(h.ui, "a.txt does not exist in the state before "+short7(fx.c1)+" and will be deleted.") {
		t.Fatalf("dialog missing the root-commit deletion line; texts=%q", h.ui.Texts())
	}
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("before-reset of root-added file failed: %v", err)
	}
	assertFileResetGone(t, repo, "a.txt")
}

// Undoing a rename resets both of its paths: the file at the new name did not
// exist before the commit and is deleted, while the original name comes back
// with the first parent's version.
func TestFileResetBeforeCommitUndoesRename(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-rename")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, fx.c6, "rename c to d")
	h.drain()

	// The fixture's rename commit must read as a pure rename carrying the
	// original path.
	if len(h.app.historyView.files) != 1 {
		t.Fatalf("fixture: files of the rename commit = %+v, want exactly one", h.app.historyView.files)
	}
	if f := h.app.historyView.files[0]; f.Status != 'R' || f.Path != "d.txt" || f.OrigPath != "c.txt" {
		t.Fatalf("fixture: rename file row = %+v, want R c.txt -> d.txt", f)
	}

	chooseFileMenuItem(t, h, fileRowLabel("d.txt"), "Reset File to State Before Commit…")
	h.ui.Frame()
	if !hasExactText(h.ui, "Reset File to State Before Commit") {
		t.Fatalf("dialog did not open; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "d.txt does not exist in "+short7(fx.c5)+" and will be deleted.") {
		t.Fatalf("dialog missing the new-name deletion line; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "c.txt will be reset to its state in "+short7(fx.c5)+".") {
		t.Fatalf("dialog missing the original-name restore line; texts=%q", h.ui.Texts())
	}

	confirmFileReset(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rename undo failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "c.txt", fx.c5)
	assertFileBytes(t, repo, "c.txt", "c content\n")
	assertFileResetGone(t, repo, "d.txt")
}

// The dialog warns about uncommitted changes of the reset paths — staged,
// unstaged or untracked ones alike — and only then; a clean file shows no
// warning.
func TestFileResetModalWarnsAboutUncommittedChanges(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-warning")
	repo.WriteFile("a.txt", "dirty\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")

	if !hasExactText(h.ui, "Uncommitted changes to a.txt will be lost.") {
		t.Fatalf("warning missing for a dirty file; texts=%q", h.ui.Texts())
	}

	// The warning tells the truth: confirming discards the dirty bytes.
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reset of dirty file failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "a.txt", fx.c3)
}

// Canceling the dialog and dismissing it with Escape both write nothing: the
// working tree, the index and HEAD stay exactly as they were.
func TestFileResetCancelAndEscapeLeaveRepositoryUntouched(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-cancel")
	repo.WriteFile("a.txt", "dirty\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	assertUntouched := func() {
		t.Helper()
		assertFileBytes(t, repo, "a.txt", "dirty\n")
		if got := repo.Git("show", ":a.txt"); got != "v4\n" {
			t.Fatalf("index a.txt = %q, want the untouched v4", got)
		}
		if got := headOf(t, repo); got != fx.c7 {
			t.Fatalf("HEAD moved by a canceled dialog: %q, want %q", got, fx.c7)
		}
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("canceled dialog produced an action error: %v", err)
		}
		if hasExactText(h.ui, "Reset File to State at Commit") {
			t.Fatal("dialog still open after Cancel or Escape")
		}
	}

	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertUntouched()

	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	assertUntouched()
}

// When the file changes outside the app after the dialog opened, the write
// worker's guard refuses the reset: nothing is written, the external content
// survives, and the failure shows in the toolbar's error area.
func TestFileResetGuardRefusesExternallyChangedFile(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-guard")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")

	// The external actor edits the file before the write reaches the worker.
	repo.WriteFile("a.txt", "external edit\n")

	confirmFileReset(t, h)

	assertRepoErrorShown(t, h, "changed after the dialog opened")
	assertFileBytes(t, repo, "a.txt", "external edit\n")
	if got := repo.Git("show", ":a.txt"); got != "v4\n" {
		t.Fatalf("index a.txt = %q after the refused reset, want the untouched v4", got)
	}
	if got := headOf(t, repo); got != fx.c7 {
		t.Fatalf("HEAD moved by the refused reset: %q, want %q", got, fx.c7)
	}
}

// While a repository write is in flight both reset entries of the file row's
// menu are disabled, so a second write cannot start over the running one.
func TestFileResetMenuItemsDisabledWhileBusy(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-busy")
	repo.WriteFile("pending.txt", "pending\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, fx.c3, "a v3")
	h.drain()

	// Start a write and hold its worker: the session stays busy while the
	// menu is opened.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v\ntexts=%q", err, h.ui.Texts())
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick(fileRowLabel("a.txt")); err != nil {
		t.Fatalf("right-click file row while busy: %v\ntexts=%q", err, h.ui.Texts())
	}
	for _, item := range []string{"Reset File to State at Commit…", "Reset File to State Before Commit…"} {
		if !slices.Contains(h.ui.Menu(), item) {
			t.Fatalf("menu %q missing the disabled item %q", h.ui.Menu(), item)
		}
		err := h.ui.ChooseMenuItem(item)
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
		if fileResetUI(h.app).open {
			t.Fatalf("reset dialog opened from the disabled %q", item)
		}
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// The reset touches only the file it was asked for: other modified files keep
// their exact bytes and status, and untracked files are outside the operation
// either way.
func TestFileResetLeavesOtherFilesUntouched(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-scope")
	repo.WriteFile("a.txt", "a dirty\n")
	repo.WriteFile("b.txt", "b dirty\n")
	repo.WriteFile("untracked.txt", "untracked\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")
	confirmFileReset(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("file reset failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "a.txt", fx.c3)
	assertFileBytes(t, repo, "b.txt", "b dirty\n")
	assertFileBytes(t, repo, "untracked.txt", "untracked\n")
	status := repo.Git("status", "--porcelain")
	if !strings.Contains(status, "b.txt") {
		t.Fatalf("b.txt modification lost: status=%q", status)
	}
	if !strings.Contains(status, "?? untracked.txt") {
		t.Fatalf("untracked.txt lost: status=%q", status)
	}
}

// Resetting a file to before a merge commit resolves the revision to the
// merge's first parent: the second parent plays no role.
func TestFileResetMergeCommitUsesFirstParent(t *testing.T) {
	repo := newGitRepo(t, "fr-merge")
	repo.CommitFile("m.txt", "top\nbottom\n", "base commit")
	repo.CreateBranch("side")
	repo.WriteFile("m.txt", "top\nmain line\nbottom\n")
	repo.Git("add", "--", "m.txt")
	repo.Git("commit", "-m", "main edits")
	repo.Checkout("side")
	repo.WriteFile("m.txt", "top\nbottom\nside line\n")
	repo.Git("add", "--", "m.txt")
	repo.Git("commit", "-m", "side edits")
	repo.Checkout("main")
	repo.Git("merge", "--no-edit", "-m", "merge side", "side")
	merge := headOf(t, repo)
	if got := repo.Git("show", merge+":m.txt"); !strings.Contains(got, "main line") || !strings.Contains(got, "side line") {
		t.Fatalf("fixture: merged m.txt = %q, want the union of both lines", got)
	}
	repo.WriteFile("m.txt", "after merge\n")
	repo.Git("add", "--", "m.txt")
	repo.Git("commit", "-m", "post merge")
	postMerge := headOf(t, repo)

	parents := strings.Fields(repo.Git("rev-parse", merge+"^1", merge+"^2"))
	if len(parents) != 2 {
		t.Fatalf("fixture: %q is not a merge commit", merge)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, merge, "merge side", "m.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !hasExactText(h.ui, "m.txt will be reset to its state in "+short7(parents[0])+".") {
		t.Fatalf("dialog names %q instead of the first parent; texts=%q", parents[0], h.ui.Texts())
	}
	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("before-reset at a merge failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "m.txt", parents[0])
	assertFileBytes(t, repo, "m.txt", "top\nmain line\nbottom\n")
	if got := headOf(t, repo); got != postMerge {
		t.Fatalf("HEAD moved by a file reset: %q, want %q", got, postMerge)
	}
}

// Staged modifications and untracked files at the reset path both produce the
// uncommitted-changes warning in the dialog, and confirming resets both the
// working tree and the index to the chosen revision.
func TestFileResetModalWarnsStagedAndUntracked(t *testing.T) {
	// Staged-only modification (working tree matches index, differs from HEAD).
	repo, fx := fileResetFixture(t, "fr-warn-staged")
	repo.WriteFile("a.txt", "staged edit\n")
	repo.Git("add", "--", "a.txt")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")

	if !hasExactText(h.ui, "Uncommitted changes to a.txt will be lost.") {
		t.Fatalf("warning missing for a staged-only change; texts=%q", h.ui.Texts())
	}
	// Both dialog buttons fit cleanly inside the 1200x800 window without overlap.
	cancelBtn, ok := h.ui.Access("Cancel")
	if !ok || cancelBtn.Bounds.W <= 0 || cancelBtn.Bounds.H <= 0 {
		t.Fatalf("Cancel button missing or empty bounds: ok=%v bounds=%+v", ok, cancelBtn.Bounds)
	}
	confirmBtn, ok := h.ui.Access("Confirm Reset")
	if !ok || confirmBtn.Bounds.W <= 0 || confirmBtn.Bounds.H <= 0 {
		t.Fatalf("Confirm Reset button missing or empty bounds: ok=%v bounds=%+v", ok, confirmBtn.Bounds)
	}
	if cancelBtn.Bounds.X < 0 || confirmBtn.Bounds.X+confirmBtn.Bounds.W > 1200 || cancelBtn.Bounds.X+cancelBtn.Bounds.W > confirmBtn.Bounds.X {
		t.Fatalf("dialog action buttons laid out poorly: cancel=%+v confirm=%+v", cancelBtn.Bounds, confirmBtn.Bounds)
	}

	confirmFileReset(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reset of staged file failed: %v", err)
	}
	assertFileMatchesRev(t, repo, "a.txt", fx.c3)

	// Untracked file occupying a path the commit deleted: resetting to before
	// that commit warns and overwrites the untracked file with the parent version.
	repo2 := newGitRepo(t, "fr-warn-untracked")
	c1 := repo2.CommitFile("b.txt", "parent b\n", "add b")
	repo2.Git("rm", "-q", "b.txt")
	repo2.Git("commit", "-m", "remove b")
	c2 := headOf(t, repo2)
	repo2.WriteFile("b.txt", "untracked b\n")

	h2 := newTestApp(t)
	openHistoryPage(t, h2, repo2.StoreRepo())
	chooseFileReset(t, h2, c2, "remove b", "b.txt",
		"Reset File to State Before Commit…", "Reset File to State Before Commit")
	if !hasExactText(h2.ui, "Uncommitted changes to b.txt will be lost.") {
		t.Fatalf("warning missing for an untracked file; texts=%q", h2.ui.Texts())
	}
	confirmFileReset(t, h2)
	if err := h2.app.session.ActionErr(); err != nil {
		t.Fatalf("reset over untracked file failed: %v", err)
	}
	assertFileMatchesRev(t, repo2, "b.txt", c1)
}

// When the write worker cannot read repository status, the guard refuses the
// reset: the toolbar shows the error, and neither the working tree nor the
// index is modified.
func TestFileResetGuardRefusesStatusFailure(t *testing.T) {
	repo, fx := fileResetFixture(t, "fr-guard-status")
	repo.WriteFile("a.txt", "local keep\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, fx.c3, "a v3", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	confirmFileReset(t, h)

	assertRepoErrorShown(t, h, "reading the repository state failed")
	assertFileBytes(t, repo, "a.txt", "local keep\n")
	if got := repo.Git("show", ":a.txt"); got != "v4\n" {
		t.Fatalf("index a.txt = %q after status failure, want untouched v4", got)
	}
	if got := headOf(t, repo); got != fx.c7 {
		t.Fatalf("HEAD moved after status failure: %q, want %q", got, fx.c7)
	}
}

// When the target commit disappears after the dialog opened, the guard refuses
// the reset without touching the working tree or the index.
func TestFileResetGuardRefusesMissingCommit(t *testing.T) {
	repo := newGitRepo(t, "fr-guard-missing-rev")
	repo.CommitFile("a.txt", "base\n", "base commit")
	repo.CreateBranch("temp")
	repo.Checkout("temp")
	tempCommit := repo.CommitFile("a.txt", "temp version\n", "temp commit")
	repo.Checkout("main")
	mainTip := repo.CommitFile("a.txt", "main version\n", "main commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	chooseFileReset(t, h, tempCommit, "temp commit", "a.txt",
		"Reset File to State at Commit…", "Reset File to State at Commit")

	// Drop the branch and remove the unreachable loose commit object so
	// rev^{commit} no longer resolves while main stays intact.
	repo.Git("branch", "-D", "temp")
	objPath := filepath.Join(repo.Path(), ".git", "objects", tempCommit[:2], tempCommit[2:])
	if err := os.Remove(objPath); err != nil {
		t.Fatalf("remove unreachable commit object %s: %v", objPath, err)
	}

	confirmFileReset(t, h)

	assertRepoErrorShown(t, h, "no longer exists")
	assertFileMatchesRev(t, repo, "a.txt", mainTip)
	if got := headOf(t, repo); got != mainTip {
		t.Fatalf("HEAD moved after missing-commit refusal: %q, want %q", got, mainTip)
	}
}

// --- SSH loopback ---

// Over a real loopback SSH connection the before-reset restores the file on
// the server: the working tree and the index there match the first parent's
// version exactly.
func TestFileResetBeforeCommitLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunShell(t, "set -eu; cd "+git.ShellQuote(repo.Path)+`
printf 'v1\n' > a.txt
git add a.txt
git commit -q -m 'a v1'
printf 'v2\n' > a.txt
git commit -q -am 'a v2'
printf 'v3\n' > a.txt
git commit -q -am 'a v3'
`)
	c1 := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD~2"))
	c2 := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD~1"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	clickCommitRow(t, h, c2, "a v2")
	h.drain()
	chooseFileMenuItem(t, h, fileRowLabel("a.txt"), "Reset File to State Before Commit…")
	h.ui.Frame()
	if !hasExactText(h.ui, "Reset File to State Before Commit") {
		t.Fatalf("SSH reset dialog did not open; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "a.txt will be reset to its state in "+short7(c1)+".") {
		t.Fatalf("SSH dialog missing the outcome line; texts=%q", h.ui.Texts())
	}
	confirmFileReset(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH before-reset failed: %v", err)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "v1\n" {
		t.Fatalf("remote working-tree a.txt = %q, want v1", string(got))
	}
	wantRev := srv.RunGit(t, repo.Path, "show", c1+":a.txt")
	if got := srv.RunGit(t, repo.Path, "show", ":a.txt"); got != wantRev {
		t.Fatalf("remote index a.txt = %q, want %q (the version of %s)", got, wantRev, short7(c1))
	}
	if head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); head == c1 || head == c2 {
		t.Fatalf("remote HEAD moved by a file reset: %q", head)
	}
}
