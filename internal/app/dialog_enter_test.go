package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the Enter key of the frame's dialogs through
// the real UI: menus, buttons and key presses, on real repositories. Their
// rule is the one the shared frame implements — Enter is the primary button
// of a non-destructive dialog and never of a destructive one, so long as the
// controls of the dialog do not take the key first.

// Merge is not destructive: once the focus is on a control that leaves Enter
// to the window — a radio of the Merge Type group — the key confirms exactly
// like the Confirm Merge button. The dialog opens with the focus on the
// primary button itself, where the button takes the key before any shortcut;
// clicking a radio first is what measures the window-level shortcut, the
// only Enter path the frame itself implements.
func TestDialogEnterMergeConfirms(t *testing.T) {
	repo := newGitRepo(t, "enter-merge")
	mergeFFFixture(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openMergeDialogFromBranch(t, h, "feature")

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Merge"); !ok || !btn.Focused {
		t.Fatalf("Confirm Merge not focused at open: %+v found=%v", btn, ok)
	}
	if err := h.ui.Click("Fast-forward if possible"); err != nil {
		t.Fatalf("click the fast-forward radio: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge by Enter failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != gitOutTrim(t, repo.Path(), "rev-parse", "feature") {
		t.Fatalf("main after the Enter merge = %q, want the feature tip — the fast-forward did not happen", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Merge"); ok {
		t.Error("the Merge dialog is still open after the confirmed merge")
	}
	if st := mergeUI(h.app); st.open {
		t.Error("the Merge dialog state is still open after the confirmed merge")
	}
}

// Reset is destructive: Enter never confirms it. The dialog opens with the
// focus in the body's first control — never on the primary — so the key
// cannot even reach the button, and the window shortcut the frame registers
// for non-destructive dialogs does not exist here. HEAD and the branch stay
// exactly where they were.
func TestDialogEnterResetDoesNothing(t *testing.T) {
	repo := newGitRepo(t, "enter-reset")
	repo.CommitFile("base.txt", "base\n", "base commit")
	c1 := repo.CommitFile("f.txt", "v1\n", "add f v1")
	tip := repo.CommitFile("f.txt", "v2\n", "update f to v2")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openResetModalOnMain(t, h, c1, "add f v1")

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Reset"); !ok {
		t.Fatalf("Confirm Reset button missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Reset button holds the focus at open")
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); ok && cancel.Focused {
		t.Fatal("focus is on cancel — the test would measure the cancel click, not the destructive protection")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != tip {
		t.Fatalf("HEAD after Enter = %q, want unchanged %q", got, tip)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != tip {
		t.Fatalf("main after Enter = %q, want unchanged %q", got, tip)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The dialog survives the key: it closes by its cancel, as always.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("dialog is gone or its cancel is unreachable after Enter: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// Drop is destructive: its primary never holds the focus at open and the
// frame registers no Enter shortcut for it, so the key is the focused
// cancel's own — the dialog closes and the commit stays on the branch. The
// modal keeps the focus on a control of the dialog at all times, so cancel
// is where Enter always lands here.
func TestDialogEnterDropDoesNotDrop(t *testing.T) {
	repo := newGitRepo(t, "enter-drop")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	tip := strings.TrimSpace(repo.Git("rev-parse", "main"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, hashes[1], "q two", "Drop…")
	if !hasExactText(h.ui, "The commit is removed from the current branch. Later commits are replayed.") {
		t.Fatalf("drop confirmation did not open; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Drop"); !ok {
		t.Fatalf("Drop button missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Fatal("the destructive Drop button holds the focus at open")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != tip {
		t.Fatalf("main after Enter = %q, want unchanged %q — Enter dropped the commit", got, tip)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); count != "3" {
		t.Fatalf("commit count after Enter = %q, want 3", count)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}
	assertQuickDialogClosed(t, h)
}

// The confirmation of Fixup is not destructive: opening it puts the focus on
// the primary button, and Enter executes the fold — the commit is folded
// into its parent, whose message survives unchanged.
func TestDialogEnterFixupConfirms(t *testing.T) {
	repo := newGitRepo(t, "enter-fixup")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, hashes[1], "q two", "Fixup into Parent…")
	if !hasExactText(h.ui, "The commit is folded into its parent, which keeps its original message. Later commits are replayed.") {
		t.Fatalf("fixup confirmation did not open; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Fixup"); !ok || !btn.Focused {
		t.Fatalf("the Fixup button is not focused at open: %+v found=%v", btn, ok)
	}

	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fixup by Enter failed: %v", err)
	}
	assertQuickDialogClosed(t, h)
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); count != "2" {
		t.Fatalf("commit count after the fixup = %q, want 2", count)
	}
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "q one" {
		t.Fatalf("tip message after the fixup = %q, want the parent's %q", msg, "q one")
	}
	assertFileBytes(t, repo, "q2.txt", "q two\n")
}

// Enter in the multi-line message editor of Reword inserts a line break: the
// editor takes the key before every shortcut, so nothing submits and the
// branch is untouched.
func TestDialogEnterRewordInsertsNewline(t *testing.T) {
	repo := newGitRepo(t, "enter-reword")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickLinearFixture(t, repo, "q one")[0]
	headBefore := strings.TrimSpace(repo.Git("rev-parse", "main"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one", "Reword Message…")
	drainQuickRead(t, h)

	// The editor does not exist during the loading frames, so the dialog
	// opens with the focus on cancel instead of in the text; clicking the
	// editor puts it where the user editing a message has it.
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click the message editor: %v\ntexts=%q", err, h.ui.Texts())
	}
	box, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message")
	if !ok {
		t.Fatalf("message editor missing; texts=%q", h.ui.Texts())
	}
	if !box.Focused {
		t.Fatal("the message editor is not focused after its click — Enter would go to the window instead of the text")
	}
	before := box.Value

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	box, ok = h.ui.AccessByRole(ui.RoleTextField, "Commit message")
	if !ok {
		t.Fatalf("message editor gone after Enter; texts=%q", h.ui.Texts())
	}
	if len(box.Value) != len(before)+1 || strings.Count(box.Value, "\n") != strings.Count(before, "\n")+1 {
		t.Errorf("message after Enter = %q, want %q with one line break inserted", box.Value, before)
	}
	if st := quickRebaseUI(h.app); !st.open {
		t.Error("Enter in the message editor submitted the reword")
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != headBefore {
		t.Fatalf("main moved after Enter in the editor: %q, want %q", got, headBefore)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter in the editor ran a write that failed: %v", err)
	}
	cancelQuickDialog(t, h)
}

// With the focus on the stash checkbox of Checkout and Sync, Enter submits
// exactly once: the checkbox does not take the key — toggles take Space —
// and the frame's one window-level shortcut is the only consumer, so one
// write runs and the dialog closes behind it.
func TestDialogEnterCheckoutSyncSubmitsOnce(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "enter-checkout-sync")
	featureBehindUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")

	// Focus the checkbox without changing the operation: tick it on and
	// back off, so the fast-forward the dialog describes stays the plain one.
	for range 2 {
		if err := h.ui.Click("Stash and reapply local changes"); err != nil {
			t.Fatalf("click the stash checkbox: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes")
	if !ok || !box.Focused {
		t.Fatalf("stash checkbox not focused after its clicks: %+v found=%v", box, ok)
	}
	if box.Checked {
		t.Fatal("stash checkbox did not toggle back off")
	}

	h.queue.PauseWorkers()
	h.ui.Key(0, ui.KeyEnter)
	// The one write the Enter must start. A second consumer of the same key
	// would meet the session's busy state and go nowhere, so the worker
	// count is the exact measure: one write, no more.
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the write is held")
	}
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and fast-forward by Enter failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	upstreamTip := gitOutTrim(t, repo.Path(), "rev-parse", "origin/feature")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != upstreamTip {
		t.Fatalf("feature after the fast-forward = %q, want the upstream tip %q", got, upstreamTip)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
		t.Error("the dialog is still open after the submitted sync")
	}
}

// A focused button takes Enter before every shortcut: tabbing onto cancel
// and pressing Enter is the cancel click — the dialog closes and nothing
// runs — wherever the window-level shortcut would otherwise go. This is the
// toolkit's rule the frame's shortcut lives under, pinned here because the
// frame's design depends on it.
func TestDialogEnterTabbedCancelCancels(t *testing.T) {
	repo := newGitRepo(t, "enter-cancel")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	tip := strings.TrimSpace(repo.Git("rev-parse", "main"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, hashes[1], "q two", "Fixup into Parent…")
	if !hasExactText(h.ui, "The commit is folded into its parent, which keeps its original message. Later commits are replayed.") {
		t.Fatalf("fixup confirmation did not open; texts=%q", h.ui.Texts())
	}

	// The dialog has two focusable controls, cancel first and the primary
	// second; the primary holds the focus at open, so one Tab moves to cancel.
	h.ui.Key(0, ui.KeyTab)
	h.ui.Frame()
	cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel")
	if !ok {
		t.Fatalf("cancel button missing; texts=%q", h.ui.Texts())
	}
	if !cancel.Focused {
		t.Fatal("Tab did not move the focus onto cancel")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()

	assertQuickDialogClosed(t, h)
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != tip {
		t.Fatalf("main after Enter on the focused cancel = %q, want unchanged %q", got, tip)
	}
	if count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); count != "3" {
		t.Fatalf("commit count after Enter on the focused cancel = %q, want 3", count)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter on the focused cancel ran a write that failed: %v", err)
	}
}
