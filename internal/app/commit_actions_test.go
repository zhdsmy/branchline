package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// gitOutTrim runs git in dir and returns its stdout trimmed, for direct
// repository assertions that complement what the UI shows.
func gitOutTrim(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitCommand(t, dir, args...))
}

// openResetModal right-clicks the commit row of hash and chooses the Reset
// menu item of the given menu label, then verifies the dialog opened.
func openResetModal(t *testing.T, h *testHarness, hash, subject, menuLabel, title string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if !slices.Contains(h.ui.Menu(), menuLabel) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), menuLabel)
	}
	if err := h.ui.ChooseMenuItem(menuLabel); err != nil {
		t.Fatalf("choose %q: %v", menuLabel, err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, title) {
		t.Fatalf("reset dialog %q did not open; texts=%q", title, h.ui.Texts())
	}
}

func openResetModalOnMain(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	openResetModal(t, h, hash, subject, "Reset 'main' to Here…", "Reset Current Branch to Revision")
}

// confirmResetButton returns the dialog's confirm button access info.
func confirmResetButton(t *testing.T, h *testHarness) ui.AccessInfo {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Reset")
	if !ok {
		t.Fatalf("Confirm Reset button missing; texts=%q", h.ui.Texts())
	}
	return btn
}

// assertRepoErrorShown verifies a failed write surfaces both in the session
// and in the toolbar's Repository Error status.
func assertRepoErrorShown(t *testing.T, h *testHarness, fragment string) {
	t.Helper()
	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected an action error, got nil")
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("action error %q missing %q", err.Error(), fragment)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("Repository Error status missing; texts=%q", h.ui.Texts())
	}
}

// assertFileBytes verifies the exact bytes of a file of the local fixture.
func assertFileBytes(t *testing.T, repo *gitRepoFixture, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(repo.Path(), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if string(got) != want {
		t.Fatalf("%s bytes = %q, want %q", rel, string(got), want)
	}
}

// A commit row's context menu offers Reset for the branch, and confirming the
// default Mixed mode resets to the commit whose row was right-clicked — even
// when a different row is the selected one. The index is reset to the target
// while the working tree bytes stay untouched, and the refreshed snapshot
// moves HEAD with it.
func TestCommitResetMixedThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "reset-mixed")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	tip := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Select the tip row, then right-click the older commit: the menu and the
	// dialog must act on the clicked row, not on the selection.
	clickCommitRow(t, h, tip, "update f to v2")
	h.drain()
	openResetModalOnMain(t, h, c1, "add f v1")

	// The dialog names the branch and the clicked commit, not the selected tip.
	if !hasExactText(h.ui, short7(c1)+" add f v1") {
		t.Fatalf("dialog missing target line for clicked commit; texts=%q", h.ui.Texts())
	}
	if hasExactText(h.ui, short7(tip)+" update f to v2") {
		t.Fatal("dialog targets the selected row instead of the right-clicked one")
	}
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Mixed"); !ok || !radio.Checked {
		t.Fatalf("Mixed radio = %+v found=%v, want present and checked", radio, ok)
	}
	if !hasExactText(h.ui, "Keep all changes and unstage differences.") {
		t.Fatalf("Mixed explanation missing; texts=%q", h.ui.Texts())
	}
	if btn := confirmResetButton(t, h); btn.Disabled {
		t.Fatal("Confirm Reset disabled for the default Mixed mode while idle")
	}

	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("HEAD after mixed reset = %q, want %q", got, c1)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("staged after mixed reset = %q, want empty", out)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--name-only"); !strings.Contains(out, "f.txt") {
		t.Fatalf("unstaged after mixed reset = %q, want f.txt", out)
	}
	assertFileBytes(t, repo, "f.txt", "v2\n")

	// The snapshot reload moved HEAD and dropped the orphaned tip commit.
	if got := h.app.session.Snapshot().Status.Head; got != c1 {
		t.Fatalf("snapshot HEAD = %q, want %q", got, c1)
	}
	for _, cm := range h.app.session.Snapshot().Commits {
		if cm.Hash == tip {
			t.Fatal("orphaned tip commit still listed after the reset")
		}
	}
	if hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("reset dialog still open after a successful reset")
	}
}

// The Soft mode moves HEAD and keeps the difference staged, leaving the
// working tree bytes as they were.
func TestCommitResetSoftKeepsDifferencesStaged(t *testing.T) {
	repo := newGitRepo(t, "reset-soft")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")

	if err := h.ui.Click("Soft"); err != nil {
		t.Fatalf("click Soft radio: %v", err)
	}
	h.ui.Frame()
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Soft"); !ok || !radio.Checked {
		t.Fatalf("Soft radio = %+v found=%v, want present and checked", radio, ok)
	}
	if !hasExactText(h.ui, "Keep all changes and stage differences.") {
		t.Fatalf("Soft explanation missing; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("soft reset failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("HEAD after soft reset = %q, want %q", got, c1)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "f.txt") {
		t.Fatalf("staged after soft reset = %q, want f.txt", out)
	}
	if out := gitOutTrim(t, repo.Path(), "diff", "--name-only"); out != "" {
		t.Fatalf("unstaged after soft reset = %q, want empty", out)
	}
	assertFileBytes(t, repo, "f.txt", "v2\n")
}

// The Hard mode is gated by an explicit second agreement: its checkbox only
// shows for Hard, the confirm button stays disabled while it is unchecked —
// clicking it then neither runs the reset nor closes the dialog — and
// switching away from Hard and back resets the box to unchecked. Once agreed,
// the hard reset discards the tracked modification and the commits after the
// target while untracked files survive.
func TestCommitResetHardRequiresAgreement(t *testing.T) {
	repo := newGitRepo(t, "reset-hard")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("f.txt", "dirty\n")
	repo.WriteFile("untracked.txt", "keep me\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")

	// The agreement checkbox only appears once Hard is chosen.
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "I understand, discard all local changes"); ok {
		t.Fatal("hard-mode agreement visible before Hard is chosen")
	}
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "I understand, discard all local changes")
	if !ok {
		t.Fatalf("agreement checkbox missing in Hard mode; texts=%q", h.ui.Texts())
	}
	if box.Checked {
		t.Fatal("agreement checkbox starts checked")
	}
	if !hasExactText(h.ui, "Discard all uncommitted tracked changes and commits after the target revision.") {
		t.Fatalf("Hard warning missing; texts=%q", h.ui.Texts())
	}
	if btn := confirmResetButton(t, h); !btn.Disabled {
		t.Fatal("Confirm Reset enabled while the Hard agreement is unchecked")
	}

	// Clicking the disabled confirm runs no write and leaves the dialog open.
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click disabled Confirm Reset: %v", err)
	}
	h.drain()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c2 {
		t.Fatalf("HEAD moved by a disabled confirm click: %q, want %q", got, c2)
	}
	assertFileBytes(t, repo, "f.txt", "dirty\n")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("disabled confirm click produced an action error: %v", err)
	}
	if !hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("disabled confirm click dismissed the dialog")
	}

	// Ticking and then switching modes resets the agreement.
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()
	if btn := confirmResetButton(t, h); btn.Disabled {
		t.Fatal("Confirm Reset still disabled after ticking the agreement")
	}
	if err := h.ui.Click("Mixed"); err != nil {
		t.Fatalf("click Mixed radio: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "I understand, discard all local changes"); ok {
		t.Fatal("agreement checkbox still visible in Mixed mode")
	}
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio again: %v", err)
	}
	h.ui.Frame()
	box, ok = h.ui.AccessByRole(ui.RoleCheckBox, "I understand, discard all local changes")
	if !ok || box.Checked {
		t.Fatalf("agreement after mode round-trip = %+v found=%v, want present and unchecked", box, ok)
	}
	if btn := confirmResetButton(t, h); !btn.Disabled {
		t.Fatal("Confirm Reset enabled after the agreement reset itself")
	}

	// Agree for real this time: the hard reset discards the tracked
	// modification and the tip commit; the untracked file survives.
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("hard reset failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("HEAD after hard reset = %q, want %q", got, c1)
	}
	assertFileBytes(t, repo, "f.txt", "v1\n")
	assertFileBytes(t, repo, "untracked.txt", "keep me\n")
	if out := gitOutTrim(t, repo.Path(), "status", "--porcelain"); strings.Contains(out, "f.txt") {
		t.Fatalf("f.txt still dirty after hard reset: %q", out)
	}
	if got := h.app.session.Snapshot().Status.Head; got != c1 {
		t.Fatalf("snapshot HEAD = %q, want %q", got, c1)
	}
}

// Canceling the dialog — however far the draft was driven — writes nothing
// and clears the draft, so reopening starts again at the Mixed default.
func TestCommitResetCancelLeavesRepositoryUntouched(t *testing.T) {
	repo := newGitRepo(t, "reset-cancel")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("f.txt", "dirty\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")

	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("dialog still open after Cancel")
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c2 {
		t.Fatalf("HEAD moved by a canceled dialog: %q, want %q", got, c2)
	}
	assertFileBytes(t, repo, "f.txt", "dirty\n")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cancel produced an action error: %v", err)
	}

	// Reopening starts from a clean draft: Mixed is the default and ticking
	// Hard again finds the agreement unchecked.
	openResetModalOnMain(t, h, c1, "add f v1")
	if radio, ok := h.ui.AccessByRole(ui.RoleRadio, "Mixed"); !ok || !radio.Checked {
		t.Fatalf("Mixed radio after reopen = %+v found=%v, want the checked default", radio, ok)
	}
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "I understand, discard all local changes"); !ok || box.Checked {
		t.Fatalf("agreement after reopen = %+v found=%v, want present and unchecked", box, ok)
	}
	if btn := confirmResetButton(t, h); !btn.Disabled {
		t.Fatal("Confirm Reset armed by the canceled dialog's agreement")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// Reset --keep refuses to run when a locally modified file also differs
// between HEAD and the target: the error shows in the toolbar, and HEAD, the
// index and every byte of the local modifications survive. Once the user
// resolves the conflict themselves, the same reset keeps the unrelated local
// modification.
func TestCommitResetKeepProtectsConflictedAndKeepsUnrelated(t *testing.T) {
	repo := newGitRepo(t, "reset-keep")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.WriteFile("keep.txt", "original\n")
	repo.WriteFile("unrelated.txt", "u\n")
	repo.Git("add", "--", "keep.txt", "unrelated.txt")
	repo.Git("commit", "-m", "add files")
	c1 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("keep.txt", "changed\n")
	repo.Git("add", "--", "keep.txt")
	repo.Git("commit", "-m", "change keep file")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("keep.txt", "local edits\n")
	repo.WriteFile("unrelated.txt", "u dirty\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add files")

	if err := h.ui.Click("Keep"); err != nil {
		t.Fatalf("click Keep radio: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Keep local changes; stop if a modified file differs between revisions.") {
		t.Fatalf("Keep explanation missing; texts=%q", h.ui.Texts())
	}
	if btn := confirmResetButton(t, h); btn.Disabled {
		t.Fatal("Confirm Reset disabled for Keep, which needs no agreement")
	}
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	// Git refuses because keep.txt is locally modified and differs between
	// the revisions; nothing may move or change.
	assertRepoErrorShown(t, h, "")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c2 {
		t.Fatalf("HEAD moved by the refused reset: %q, want %q", got, c2)
	}
	assertFileBytes(t, repo, "keep.txt", "local edits\n")
	assertFileBytes(t, repo, "unrelated.txt", "u dirty\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused reset: %q", out)
	}

	// The user discards the conflicting local edit themselves, as Git
	// suggests; the unrelated modification stays. The same reset now runs and
	// keeps the unrelated local change.
	repo.Git("checkout", "--", "keep.txt")
	openResetModalOnMain(t, h, c1, "add files")
	if err := h.ui.Click("Keep"); err != nil {
		t.Fatalf("click Keep radio again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset again: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("keep reset without conflict failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("HEAD after keep reset = %q, want %q", got, c1)
	}
	assertFileBytes(t, repo, "keep.txt", "original\n")
	assertFileBytes(t, repo, "unrelated.txt", "u dirty\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--name-only"); !strings.Contains(out, "unrelated.txt") {
		t.Fatalf("unstaged after keep reset = %q, want unrelated.txt", out)
	}
}

// While a merge is in progress the Reset menu item is disabled and cannot be
// chosen, because moving HEAD mid-operation would corrupt it.
func TestCommitResetMenuItemDisabledDuringMerge(t *testing.T) {
	repo := newGitRepo(t, "reset-merge")
	repo.CommitFile("conflict.txt", "base\n", "base commit")
	repo.CreateBranch("side")
	repo.WriteFile("conflict.txt", "main\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "main edits")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("side")
	repo.WriteFile("conflict.txt", "side\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "side edits")
	repo.Checkout("main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	row := commitRowLabel(head, "main edits")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if !slices.Contains(h.ui.Menu(), "Reset 'main' to Here…") {
		t.Fatalf("menu %q missing the disabled reset item", h.ui.Menu())
	}
	err := h.ui.ChooseMenuItem("Reset 'main' to Here…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the reset item during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("reset dialog opened from a disabled menu item")
	}
}

// While a repository write is busy the Reset menu item is disabled, so a
// second write cannot be started over the running one.
func TestCommitResetMenuItemDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "reset-busy")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.Git("commit", "--allow-empty", "-m", "tip commit")
	repo.WriteFile("pending.txt", "pending\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
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
	row := commitRowLabel(c1, "add f v1")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row while busy: %v", err)
	}
	err := h.ui.ChooseMenuItem("Reset 'main' to Here…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the reset item while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// When the background guard cannot even read the repository state, the Hard
// reset never runs: HEAD, the index and every byte of the working tree are
// exactly as before, and the failure shows in the toolbar.
func TestCommitResetHardGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "reset-guard-status")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("f.txt", "dirty\n")
	repo.WriteFile("untracked.txt", "keep me\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()

	// The guard's very first status read fails; the reset must never run.
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c2 {
		t.Fatalf("HEAD moved after a failed status read: %q, want %q", got, c2)
	}
	assertFileBytes(t, repo, "f.txt", "dirty\n")
	assertFileBytes(t, repo, "untracked.txt", "keep me\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched after a failed status read: %q", out)
	}
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the Hard reset the user confirmed against the old state:
// nothing is discarded, and the error explains why.
func TestCommitResetHardGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "reset-guard-head")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	repo.WriteFile("f.txt", "dirty\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()

	// An external actor commits on top before the write reaches the worker.
	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD moved by the refused reset: %q, want %q", got, moved)
	}
	assertFileBytes(t, repo, "f.txt", "dirty\n")
	assertFileBytes(t, repo, "other.txt", "other\n")
}

// When the branch itself changed after the dialog opened, the guard refuses
// the Hard reset even though HEAD's hash did not move.
func TestCommitResetHardGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "reset-guard-branch")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.CreateBranch("feature")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()

	// An external actor switches the branch before the write reaches the
	// worker; HEAD's hash stays the same, so only the branch check catches it.
	repo.Checkout("feature")

	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c2 {
		t.Fatalf("HEAD moved by the refused reset: %q, want %q", got, c2)
	}
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Fatalf("branch after the refused reset = %q, want feature", got)
	}
}

// On a detached HEAD the menu resets "HEAD" rather than a branch, the dialog
// is titled accordingly, and the reset leaves the checkout detached.
func TestCommitResetDetachedHead(t *testing.T) {
	repo := newGitRepo(t, "reset-detached")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.WriteFile("f.txt", "v2\n")
	repo.Git("add", "--", "f.txt")
	repo.Git("commit", "-m", "update f to v2")
	c2 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Git("checkout", "--detach", c2)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}

	openResetModal(t, h, c1, "add f v1", "Reset HEAD to Here…", "Reset HEAD to Revision")
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("detached reset failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != c1 {
		t.Fatalf("HEAD after detached reset = %q, want %q", got, c1)
	}
	if out, err := tryGitCommand(repo.Path(), "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatalf("checkout no longer detached after the reset: %q", out)
	}
}

// Switching the repository closes an open Reset dialog and clears its draft:
// the captured target, HEAD and branch belong to the repository that is gone.
// The switch is driven through the session because the modal swallows every
// click outside itself — including the repository switcher's — by design.
func TestCommitResetModalClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "reset-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	c1 := repoA.CommitFile("f.txt", "v1\n", "add f v1")
	repoB := newGitRepo(t, "reset-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := commitActionsUI(h.app)
	if st.resetOpen {
		t.Fatal("reset dialog still open after the repository switch")
	}
	if st.resetTarget.Hash != "" || st.resetExpectedHead != "" || st.resetBranch != "" {
		t.Fatalf("reset draft survived the switch: %+v", st)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("reset dialog rendered again after the repository switch")
	}
}

// --- SSH loopback ---

// openSSHHistoryPage opens the seeded SSH repository through the Repository
// Switcher and navigates to the History page.
func openSSHHistoryPage(t *testing.T, h *testHarness, repo store.Repo) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo %q: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
}

// remoteCommitFile writes, stages and commits one file on the server,
// returning the new HEAD hash.
func remoteCommitFile(t *testing.T, srv *sshtest.Target, dir, rel, content, subject string) string {
	t.Helper()
	srv.WriteFile(t, dir, rel, []byte(content))
	srv.RunGit(t, dir, "add", "--", rel)
	srv.RunGit(t, dir, "commit", "-q", "-m", subject)
	return strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))
}

// remoteResetFixture builds a linear history on the server: the initial
// commit of sshtest plus one commit per subject modifying f.txt, returning
// the hash of each subject's commit oldest-first.
func remoteResetFixture(t *testing.T, srv *sshtest.Target, dir string, versions ...string) []string {
	t.Helper()
	hashes := make([]string, 0, len(versions))
	for _, content := range versions {
		hashes = append(hashes, remoteCommitFile(t, srv, dir, "f.txt", content+"\n", "f version "+content))
	}
	return hashes
}

// Over a real SSH connection the full reset chain works through the context
// menu: Soft keeps differences staged, Mixed unstages them, and Hard — gated
// by its agreement — discards the tracked modification while the untracked
// file survives. Every assertion reads the server's repository through a
// second SSH channel.
func TestCommitResetModesLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	hashes := remoteResetFixture(t, srv, repo.Path, "v1", "v2", "v3", "v4")
	c1, c2, c3 := hashes[0], hashes[1], hashes[2]

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Soft to the v3 commit: the v4 difference stays staged.
	openResetModalOnMain(t, h, c3, "f version v3")
	if err := h.ui.Click("Soft"); err != nil {
		t.Fatalf("click Soft radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH soft reset failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c3 {
		t.Fatalf("remote HEAD after soft reset = %q, want %q", got, c3)
	}
	if out := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); !strings.Contains(out, "f.txt") {
		t.Fatalf("remote staged after soft reset = %q, want f.txt", out)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "v4\n" {
		t.Fatalf("remote f.txt after soft reset = %q, want v4", string(got))
	}

	// Mixed to the v2 commit: the difference is unstaged, the bytes stay.
	openResetModalOnMain(t, h, c2, "f version v2")
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH mixed reset failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c2 {
		t.Fatalf("remote HEAD after mixed reset = %q, want %q", got, c2)
	}
	if out := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); strings.TrimSpace(out) != "" {
		t.Fatalf("remote staged after mixed reset = %q, want empty", out)
	}
	if out := srv.RunGit(t, repo.Path, "diff", "--name-only"); !strings.Contains(out, "f.txt") {
		t.Fatalf("remote unstaged after mixed reset = %q, want f.txt", out)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "v4\n" {
		t.Fatalf("remote f.txt after mixed reset = %q, want v4", string(got))
	}

	// Hard to the v1 commit: gated by the agreement even over SSH, then the
	// tracked modification is discarded while the untracked file survives.
	srv.WriteFile(t, repo.Path, "untracked.txt", []byte("keep me\n"))
	openResetModalOnMain(t, h, c1, "f version v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if btn := confirmResetButton(t, h); !btn.Disabled {
		t.Fatal("Confirm Reset enabled over SSH before agreeing")
	}
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click disabled Confirm Reset: %v", err)
	}
	h.drain()
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c2 {
		t.Fatalf("remote HEAD moved by a disabled confirm click: %q, want %q", got, c2)
	}
	if !hasExactText(h.ui, "Reset Current Branch to Revision") {
		t.Fatal("disabled confirm click dismissed the dialog over SSH")
	}
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH hard reset failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c1 {
		t.Fatalf("remote HEAD after hard reset = %q, want %q", got, c1)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "v1\n" {
		t.Fatalf("remote f.txt after hard reset = %q, want v1", string(got))
	}
	if got := srv.ReadFile(t, repo.Path, "untracked.txt"); string(got) != "keep me\n" {
		t.Fatalf("remote untracked.txt after hard reset = %q, want keep me", string(got))
	}
	if got := h.app.session.Snapshot().Status.Head; got != c1 {
		t.Fatalf("snapshot HEAD = %q, want %q", got, c1)
	}
}

// Over SSH, Reset --keep refuses to run while a locally modified file differs
// between HEAD and the target — the error shows and every byte survives — and
// once the conflict is gone it keeps the unrelated local modification.
func TestCommitResetKeepLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "keep.txt", []byte("original\n"))
	srv.WriteFile(t, repo.Path, "unrelated.txt", []byte("u\n"))
	srv.RunGit(t, repo.Path, "add", "--", "keep.txt", "unrelated.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "add files")
	c1 := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.WriteFile(t, repo.Path, "keep.txt", []byte("changed\n"))
	srv.RunGit(t, repo.Path, "add", "--", "keep.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "change keep file")
	c2 := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.WriteFile(t, repo.Path, "keep.txt", []byte("local edits\n"))
	srv.WriteFile(t, repo.Path, "unrelated.txt", []byte("u dirty\n"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openResetModalOnMain(t, h, c1, "add files")
	if err := h.ui.Click("Keep"); err != nil {
		t.Fatalf("click Keep radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c2 {
		t.Fatalf("remote HEAD moved by the refused reset: %q, want %q", got, c2)
	}
	if got := srv.ReadFile(t, repo.Path, "keep.txt"); string(got) != "local edits\n" {
		t.Fatalf("remote keep.txt after refused reset = %q, want local edits", string(got))
	}
	if got := srv.ReadFile(t, repo.Path, "unrelated.txt"); string(got) != "u dirty\n" {
		t.Fatalf("remote unrelated.txt after refused reset = %q, want u dirty", string(got))
	}

	srv.RunGit(t, repo.Path, "checkout", "--", "keep.txt")
	openResetModalOnMain(t, h, c1, "add files")
	if err := h.ui.Click("Keep"); err != nil {
		t.Fatalf("click Keep radio again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset again: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH keep reset without conflict failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c1 {
		t.Fatalf("remote HEAD after keep reset = %q, want %q", got, c1)
	}
	if got := srv.ReadFile(t, repo.Path, "unrelated.txt"); string(got) != "u dirty\n" {
		t.Fatalf("remote unrelated.txt after keep reset = %q, want u dirty", string(got))
	}
	if got := srv.ReadFile(t, repo.Path, "keep.txt"); string(got) != "original\n" {
		t.Fatalf("remote keep.txt after keep reset = %q, want original", string(got))
	}
}

// Over SSH the Hard mode guards protect the working tree the same as locally:
// a failed status read and a HEAD moved after the dialog opened both refuse
// the reset without touching a byte, and the failure shows in the toolbar.
func TestCommitResetHardGuardsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	hashes := remoteResetFixture(t, srv, repo.Path, "v1", "v2")
	c1, c2 := hashes[0], hashes[1]
	srv.WriteFile(t, repo.Path, "f.txt", []byte("dirty\n"))

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	openResetModalOnMain(t, h, c1, "f version v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement: %v", err)
	}
	h.ui.Frame()

	// The guard's first status read fails: the reset must never run.
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c2 {
		t.Fatalf("remote HEAD moved after a failed status read: %q, want %q", got, c2)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "dirty\n" {
		t.Fatalf("remote f.txt after a failed status read = %q, want dirty", string(got))
	}

	// HEAD moved externally after the dialog opened: the guard refuses the
	// reset the user confirmed against the old state.
	openResetModalOnMain(t, h, c1, "f version v1")
	if err := h.ui.Click("Hard"); err != nil {
		t.Fatalf("click Hard radio again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("I understand, discard all local changes"); err != nil {
		t.Fatalf("tick agreement again: %v", err)
	}
	h.ui.Frame()
	remoteCommitFile(t, srv, repo.Path, "other.txt", "other\n", "external commit")
	moved := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	if err := h.ui.Click("Confirm Reset"); err != nil {
		t.Fatalf("click Confirm Reset again: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != moved {
		t.Fatalf("remote HEAD moved by the refused reset: %q, want %q", got, moved)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "dirty\n" {
		t.Fatalf("remote f.txt after the refused reset = %q, want dirty", string(got))
	}
}
