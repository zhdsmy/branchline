package app

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// openCheckoutModal right-clicks the commit row of hash and chooses the
// Checkout Commit menu item, then verifies the dialog opened.
func openCheckoutModal(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Checkout Commit…"); err != nil {
		t.Fatalf("choose Checkout Commit…: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Checkout Commit") {
		t.Fatalf("checkout dialog did not open; texts=%q", h.ui.Texts())
	}
}

// confirmCheckoutButton returns the dialog's confirm button access info.
func confirmCheckoutButton(t *testing.T, h *testHarness) ui.AccessInfo {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout Commit")
	if !ok {
		t.Fatalf("Confirm Checkout Commit button missing; texts=%q", h.ui.Texts())
	}
	return btn
}

// assertDetached verifies HEAD is the hash and no branch is checked out.
func assertDetached(t *testing.T, dir, hash string) {
	t.Helper()
	if got := gitOutTrim(t, dir, "rev-parse", "HEAD"); got != hash {
		t.Fatalf("HEAD = %q, want %q", got, hash)
	}
	if out, err := tryGitCommand(dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatalf("HEAD not detached, symbolic-ref = %q", out)
	}
}

// assertStillOnBranch verifies HEAD is the hash and branch is checked out.
func assertStillOnBranch(t *testing.T, dir, branch, hash string) {
	t.Helper()
	if got := gitOutTrim(t, dir, "rev-parse", "HEAD"); got != hash {
		t.Fatalf("HEAD = %q, want %q", got, hash)
	}
	if got := gitOutTrim(t, dir, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Fatalf("branch = %q, want %q", got, branch)
	}
}

// A commit row's context menu opens with Checkout Commit as its first item
// and the two copy items at its tail behind a separator. Confirming the
// dialog detaches HEAD at the clicked commit: git rev-parse reports it,
// symbolic-ref fails, the snapshot reload leaves no current branch — the
// sidebar selects none of them — and the History list decorates the commit
// with the plain detached HEAD badge.
func TestCommitCheckoutThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "checkout-detach")
	c1 := repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CommitFile("f.txt", "v1\n", "add f v1")
	tip := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// The menu puts checkout first and the copy items last behind a separator.
	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	menu := h.ui.Menu()
	if len(menu) < 4 || menu[0] != "Checkout Commit…" {
		t.Fatalf("menu %q does not start with Checkout Commit…", menu)
	}
	tail := menu[len(menu)-4:]
	if tail[0] != "-" || tail[1] != "Copy Commit SHA" || tail[2] != "Copy Commit Subject" || tail[3] != "Copy Commit Info" {
		t.Fatalf("menu %q does not end with separator + the three copy items", menu)
	}
	if err := h.ui.ChooseMenuItem("Checkout Commit…"); err != nil {
		t.Fatalf("choose Checkout Commit…: %v", err)
	}
	h.ui.Frame()

	// The dialog names the clicked commit.
	if !hasExactText(h.ui, "HEAD will be detached at "+short7(c1)+" base commit") {
		t.Fatalf("dialog missing the detach line; texts=%q", h.ui.Texts())
	}
	if btn := confirmCheckoutButton(t, h); btn.Disabled {
		t.Fatal("Confirm Checkout Commit disabled while idle")
	}

	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	assertDetached(t, repo.Path(), c1)

	// The reloaded snapshot is detached at the target: no branch is current,
	// so the sidebar selects neither of them, and the History row shows the
	// plain HEAD badge instead of the attached HEAD → branch pill.
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("snapshot branch = %q, want detached", got)
	}
	if got := h.app.session.Snapshot().Status.Head; got != c1 {
		t.Fatalf("snapshot HEAD = %q, want %q", got, c1)
	}
	if row, ok := h.ui.Access("main"); ok && row.Selected {
		t.Fatal("sidebar still selects main while HEAD is detached")
	}
	if !h.ui.HasText("HEAD") {
		t.Fatalf("missing detached HEAD badge; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("HEAD → main") {
		t.Fatal("attached HEAD → main badge rendered while detached")
	}
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog still open after a successful checkout")
	}

	// The tip commit stays in history: the walk includes every ref.
	foundTip := false
	for _, cm := range h.app.session.Snapshot().Commits {
		if cm.Hash == tip {
			foundTip = true
		}
	}
	if !foundTip {
		t.Fatal("tip commit missing from history after detaching")
	}
}

// While HEAD already is detached at a commit its Checkout Commit item is
// disabled — the checkout would be a no-op — while the same item of the
// other commit stays enabled.
func TestCommitCheckoutMenuItemDisabledWhenDetachedThere(t *testing.T) {
	repo := newGitRepo(t, "checkout-detached-item")
	c1 := repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.Git("checkout", "--detach", c1)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}

	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click the detached-at commit row: %v", err)
	}
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing checkout of the detached-at commit = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog opened from a disabled menu item")
	}

	// The other commit's item stays enabled: choosing it opens the dialog.
	c2 := strings.TrimSpace(repo.Git("rev-parse", "main"))
	row = commitRowLabel(c2, "add f v1")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click the other commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout Commit…"); err != nil {
		t.Fatalf("choose Checkout Commit… of the other commit: %v", err)
	}
	h.ui.Frame()
	if !hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog did not open for the other commit")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// While a merge is in progress the Checkout Commit item is disabled, because
// moving HEAD mid-operation would corrupt it.
func TestCommitCheckoutMenuItemDisabledDuringMerge(t *testing.T) {
	repo := newGitRepo(t, "checkout-merge")
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
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing checkout during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog opened during a merge")
	}
}

// While a repository write is busy the Checkout Commit item is disabled, so a
// second write cannot be started over the running one; the copy items stay
// enabled, because they read nothing of the moving repository.
func TestCommitCheckoutMenuItemDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "checkout-busy")
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
	err := h.ui.ChooseMenuItem("Checkout Commit…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing checkout while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog opened while busy")
	}

	// Copy Commit SHA works while the write is busy.
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row again: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Commit SHA"); err != nil {
		t.Fatalf("Copy Commit SHA unavailable while busy: %v", err)
	}
	if got := h.ui.Clipboard(); got != c1 {
		t.Fatalf("clipboard while busy = %q, want %q", got, c1)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
}

// When HEAD moved after the dialog opened — here an external commit — the
// guard refuses the checkout the user confirmed against the old state: HEAD
// stays where the external actor left it, and the error explains why.
func TestCommitCheckoutGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "checkout-guard-head")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	// An external actor commits on top before the write reaches the worker.
	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	assertStillOnBranch(t, repo.Path(), "main", moved)
}

// Even when a background snapshot reload runs after an external commit while
// the Checkout Commit dialog is open, confirming the stale dialog still
// refuses because HEAD and branch were captured when the dialog opened.
func TestCommitCheckoutGuardHeadMovedAfterReload(t *testing.T) {
	repo := newGitRepo(t, "checkout-guard-head-reload")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	h.app.session.Reload()
	h.drain()

	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	assertStillOnBranch(t, repo.Path(), "main", moved)
}

// When the branch itself changed after the dialog opened, the guard refuses
// the checkout even though HEAD's hash did not move.
func TestCommitCheckoutGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "checkout-guard-branch")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.CreateBranch("feature")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	// An external actor switches the branch before the write reaches the
	// worker; HEAD's hash stays the same, so only the branch check catches it.
	repo.Checkout("feature")

	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	assertStillOnBranch(t, repo.Path(), "feature", head)
}

// When the guard cannot even read the repository state, the checkout never
// runs: HEAD and the branch stay exactly as before, and the failure shows in
// the toolbar.
func TestCommitCheckoutGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "checkout-guard-status")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	// The guard's very first status read fails; the checkout must never run.
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "reading the repository state failed")
	assertStillOnBranch(t, repo.Path(), "main", head)
}

// Checking out a commit whose version of a locally modified file differs
// opens the dialog offering to stash or discard the changes instead of an
// error banner. Canceling it keeps HEAD and the branch where they were, and
// every byte of the local modification survives.
func TestCommitCheckoutConflictRefusedWithBytesIntact(t *testing.T) {
	repo := newGitRepo(t, "checkout-conflict")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.CommitFile("f.txt", "v2\n", "update f to v2")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.WriteFile("f.txt", "local edits\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	// The dialog opens with the file in the way, instead of an error banner.
	assertCheckoutBlockedPanel(t, h, shortTagHash(c1), []string{"f.txt"}, nil)

	cancelCheckoutBlocked(t, h)
	h.drain()

	assertStillOnBranch(t, repo.Path(), "main", head)
	assertFileBytes(t, repo, "f.txt", "local edits\n")
	if got := h.app.session.Snapshot().Status.Branch; got != "main" {
		t.Fatalf("snapshot branch after the canceled checkout = %q, want main", got)
	}

	// Once the user discards the conflicting edit themselves, the same
	// checkout detaches at the target.
	repo.Git("checkout", "--", "f.txt")
	openCheckoutModal(t, h, c1, "add f v1")
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout after resolving the conflict failed: %v", err)
	}
	assertDetached(t, repo.Path(), c1)
	assertFileBytes(t, repo, "f.txt", "v1\n")
}

// Canceling the dialog writes nothing and clears the draft: reopening starts
// from the repository as it is then.
func TestCommitCheckoutCancelLeavesRepositoryUntouched(t *testing.T) {
	repo := newGitRepo(t, "checkout-cancel")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()

	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("dialog still open after Cancel")
	}
	assertStillOnBranch(t, repo.Path(), "main", head)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cancel produced an action error: %v", err)
	}

	// Reopening starts from a clean draft and confirms normally.
	openCheckoutModal(t, h, c1, "add f v1")
	if btn := confirmCheckoutButton(t, h); btn.Disabled {
		t.Fatal("Confirm Checkout Commit disabled after reopening")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel again: %v", err)
	}
	h.ui.Frame()
}

// Switching the repository closes an open Checkout Commit dialog and clears
// its draft: the captured target belongs to the repository that is gone. The
// switch is driven through the session because the modal swallows every click
// outside itself — including the repository switcher's — by design.
func TestCommitCheckoutModalClosesOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "checkout-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	c1 := repoA.CommitFile("f.txt", "v1\n", "add f v1")
	repoB := newGitRepo(t, "checkout-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := commitMiscUI(h.app)
	if st.checkoutOpen {
		t.Fatal("checkout dialog still open after the repository switch")
	}
	if st.checkoutTarget.Hash != "" {
		t.Fatalf("checkout draft survived the switch: %+v", st)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Checkout Commit") {
		t.Fatal("checkout dialog rendered again after the repository switch")
	}
}

// The dialog is horizontally centered in the 1200x800 test window, and both
// of its buttons lie inside its panel.
func TestCommitCheckoutDialogCenteredLayout(t *testing.T) {
	repo := newGitRepo(t, "checkout-layout")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add f v1")

	dialog, ok := h.ui.AccessByRole(ui.RoleDialog, "")
	if !ok {
		t.Fatalf("no dialog in the accessibility tree; texts=%q", h.ui.Texts())
	}
	d := dialog.Bounds
	center := d.X + d.W/2
	if center < 598 || center > 602 {
		t.Fatalf("dialog center x = %.2f, want 600 in the 1200-wide window (bounds %+v)", center, d)
	}
	if d.X < 0 || d.X+d.W > 1200 {
		t.Fatalf("dialog %+v outside the 1200-wide window", d)
	}
	for _, name := range []string{"Cancel", "Confirm Checkout Commit"} {
		btn, ok := h.ui.AccessByRole(ui.RoleButton, name)
		if !ok {
			t.Fatalf("button %q missing; texts=%q", name, h.ui.Texts())
		}
		b := btn.Bounds
		if b.X < d.X || b.X+b.W > d.X+d.W || b.Y < d.Y || b.Y+b.H > d.Y+d.H {
			t.Fatalf("button %q at %+v outside the dialog %+v", name, b, d)
		}
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// --- Copy ---

// Copy Commit SHA puts exactly the full hash of the right-clicked commit on
// the clipboard — even when a different row is the selected one.
func TestCommitCopySHAThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "copy-sha")
	c1 := repo.CommitFile("base.txt", "base\n", "base commit")
	c2 := repo.CommitFile("f.txt", "v1\n", "add f v1")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Select the tip row, then right-click the older commit: the copy must be
	// of the clicked row, not of the selection.
	clickCommitRow(t, h, c2, "add f v1")
	h.drain()
	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Commit SHA"); err != nil {
		t.Fatalf("choose Copy Commit SHA: %v", err)
	}
	if got := h.ui.Clipboard(); got != c1 {
		t.Fatalf("clipboard = %q, want the full hash %q", got, c1)
	}
	if len(h.ui.Clipboard()) != 40 {
		t.Fatalf("clipboard length = %d, want the full 40 characters", len(h.ui.Clipboard()))
	}
}

// Copy Commit Info writes the multi-line commit info of the clicked commit:
// the full hash, the author with their email, the author time formatted to
// the minute, a blank line and the subject.
func TestCommitCopyInfoThroughContextMenu(t *testing.T) {
	repo := newGitRepo(t, "copy-info")
	repo.WriteFile("base.txt", "base\n")
	repo.Git("add", "--", "base.txt")
	repo.Git("commit", "--date=@1704110400 +0000", "-m", "base commit")
	c1 := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	row := commitRowLabel(c1, "base commit")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Copy Commit Info"); err != nil {
		t.Fatalf("choose Copy Commit Info: %v", err)
	}

	// The expected time is derived from the repository's own author timestamp,
	// through the same unix conversion the log parser makes.
	at, err := strconv.ParseInt(gitOutTrim(t, repo.Path(), "log", "-1", "--format=%at"), 10, 64)
	if err != nil {
		t.Fatalf("parse author timestamp: %v", err)
	}
	when := time.Unix(at, 0).Format("2006-01-02 15:04")
	want := strings.Join([]string{
		c1,
		"Branchline Test <test@branchline.local>",
		when,
		"",
		"base commit",
	}, "\n")
	if got := h.ui.Clipboard(); got != want {
		t.Fatalf("clipboard = %q, want %q", got, want)
	}
}

// The commit info of a commit without an author time leaves the time line
// empty: hash, author line, two consecutive empty lines, subject.
func TestCommitCopyInfoZeroTime(t *testing.T) {
	cm := git.Commit{
		Hash:        "0123456789abcdef0123456789abcdef01234567",
		Author:      "A U Thor",
		AuthorEmail: "a@example.com",
		Subject:     "the subject",
	}
	want := strings.Join([]string{
		cm.Hash,
		"A U Thor <a@example.com>",
		"",
		"",
		"the subject",
	}, "\n")
	if got := commitInfo(cm); got != want {
		t.Fatalf("commitInfo without a time = %q, want %q", got, want)
	}
	cm.Date = time.Unix(1704110400, 0)
	when := time.Unix(1704110400, 0).Format("2006-01-02 15:04")
	want = strings.Join([]string{cm.Hash, "A U Thor <a@example.com>", when, "", "the subject"}, "\n")
	if got := commitInfo(cm); got != want {
		t.Fatalf("commitInfo with a time = %q, want %q", got, want)
	}
}

// --- SSH loopback ---

// Over a real SSH connection the checkout works through the context menu: the
// server's HEAD detaches at the target commit, the working tree bytes follow
// it, and the reloaded snapshot is detached there. Every server-side
// assertion reads the repository through a second SSH channel.
func TestCommitCheckoutLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	c1 := remoteCommitFile(t, srv, repo.Path, "f.txt", "v1\n", "f version v1")
	remoteCommitFile(t, srv, repo.Path, "f.txt", "v2\n", "f version v2")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	openCheckoutModal(t, h, c1, "f version v1")
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH checkout failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD")); got != c1 {
		t.Fatalf("remote HEAD after checkout = %q, want %q", got, c1)
	}
	// symbolic-ref -q HEAD exits non-zero while detached.
	detachProbe := srv.RunShell(t, "cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git symbolic-ref -q HEAD >/dev/null 2>&1; echo rc=$?")
	if !strings.Contains(detachProbe, "rc=1") {
		t.Fatalf("remote symbolic-ref probe = %q, want rc=1 (detached)", detachProbe)
	}
	if got := srv.ReadFile(t, repo.Path, "f.txt"); string(got) != "v1\n" {
		t.Fatalf("remote f.txt after checkout = %q, want v1", string(got))
	}
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("snapshot branch = %q, want detached", got)
	}
	if got := h.app.session.Snapshot().Status.Head; got != c1 {
		t.Fatalf("snapshot HEAD = %q, want %q", got, c1)
	}
}
