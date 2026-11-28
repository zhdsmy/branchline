package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the Enter key of the sync and branch dialogs
// — Fetch, Pull, Push, the remote branch Pull into, Push Tag, New Branch,
// Rename Branch and Delete Branches — through the real UI on real
// repositories. Their rule is the one the shared frame implements: Enter is
// the primary button of a non-destructive dialog and never of a destructive
// one, so long as the controls of the dialog do not take the key first —
// the single-line inputs of Push, New Branch and Rename Branch submit
// through their own Submitted, exactly once.

// Fetch is not destructive: Enter confirms exactly like the Confirm Fetch
// click, the peer commit of the remote reaches the local tracking ref, and
// the dialog closes behind the write.
func TestDialogSyncBranchFetchEnterFetches(t *testing.T) {
	repo := newGitRepo(t, "dsb-fetch-enter")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarFetchDialog(t, h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Fetch"); !ok || !btn.Focused {
		t.Fatalf("Confirm Fetch not focused at open: %+v found=%v", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if syncStateFor(h.app).fetchDlg.open {
		t.Fatal("the Fetch dialog stayed open after Enter")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fetch by Enter failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "refs/remotes/origin/main")); got != peerHead {
		t.Fatalf("origin/main = %q, want %q after the Enter-submitted fetch", got, peerHead)
	}
}

// Pull is not destructive: Enter confirms exactly like the Confirm Pull
// click, HEAD fast-forwards to the peer commit, and the dialog closes.
func TestDialogSyncBranchPullEnterPulls(t *testing.T) {
	repo, bareDir := pullDialogFixture(t, "dsb-pull-enter")
	peerHead := pushPeerCommit(t, bareDir, "main", "p.txt", "p\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPullDialog(t, h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Pull"); !ok || !btn.Focused {
		t.Fatalf("Confirm Pull not focused at open: %+v found=%v", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if syncStateFor(h.app).pullDlg.open {
		t.Fatal("the Pull dialog stayed open after Enter")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull by Enter failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("HEAD = %q, want %q after the Enter-submitted pull", got, peerHead)
	}
}

// The remote branch Pull into is not destructive: Enter confirms it like
// the click, the remote feature merges into the current branch, and the
// dialog closes.
func TestDialogSyncBranchRemotePullEnterPulls(t *testing.T) {
	repo, _ := remotePullFixture(t, "dsb-rbpull-enter")
	divergeMainFromFeature(t, repo)

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Pull Remote Branch"); !ok || !btn.Focused {
		t.Fatalf("Confirm Pull Remote Branch not focused at open: %+v found=%v", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if remoteBranchPullUI(h.app).pullOpen {
		t.Fatal("the Pull into dialog stayed open after Enter")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull into by Enter failed: %v", err)
	}
	if !featureMergedIntoMain(t, repo) {
		t.Fatal("origin/feature is not merged into main after the Enter-submitted pull")
	}
}

// Enter in the single-line destination input of Push submits exactly like
// the confirm button: the input takes the key before every window shortcut,
// so its own Submitted is the one consumer and the push runs once — which a
// push cannot prove by count, since pushing twice lands the same ref, but
// the existing validation suite pins the no-double-submit frame behavior.
func TestDialogSyncBranchPushInputEnterPushesOnce(t *testing.T) {
	repo, bareDir, head := aheadFixture(t, "dsb-push-enter")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	if err := h.ui.Click("Push Remote Branch"); err != nil {
		t.Fatalf("focus remote branch input: %v\ntexts=%q", err, h.ui.Texts())
	}
	box, ok := h.ui.AccessByRole(ui.RoleTextField, "Push Remote Branch")
	if !ok || !box.Focused {
		t.Fatalf("destination input not focused after its click: %+v found=%v", box, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if pushDialogUI(h.app).open {
		t.Fatal("the Push dialog stayed open after Enter in the input")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push by Enter failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
}

// A ticked force push is destructive: Enter never confirms it, in the
// destination input included. With the agreement ticked the button is
// enabled — so the refusal rests on the destructive rule, not on a disabled
// button — the diverged remote branch keeps every commit, and the dialog
// stays open.
func TestDialogSyncBranchPushForceEnterNeverPushes(t *testing.T) {
	repo, bareDir, _ := aheadFixture(t, "dsb-push-force-enter")
	pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer diverging commit")
	before := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main"))

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openToolbarPushDialog(t, h)
	tickBox(t, h, "Force push (with lease)")
	tickBox(t, h, "Confirm force push with lease")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push"); !ok || btn.Disabled {
		t.Fatalf("Confirm Force Push = %+v (found=%v), want enabled — the refusal must not rest on a disabled button", btn, ok)
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Force Push button holds the focus")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if !pushDialogUI(h.app).open {
		t.Fatal("Enter closed the destructive force-push dialog")
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != before {
		t.Fatalf("bare refs/heads/main = %q, want unchanged %q — Enter force-pushed", got, before)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The dialog closes by its cancel, as always.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("dialog is gone or its cancel is unreachable after Enter: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// Push Tag unticked is not destructive: Enter confirms it like the click,
// the tag reaches the remote, and the dialog closes.
func TestDialogSyncBranchPushTagEnterPushes(t *testing.T) {
	repo, bare, _, second := tagsPushSidebarRepo(t, "dsb-pushtag-enter")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push Tag"); !ok || !btn.Focused {
		t.Fatalf("Confirm Push Tag not focused at open: %+v found=%v", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if h.app.tagsPushUI().pushOpen {
		t.Fatal("the Push Tag dialog stayed open after Enter")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push tag by Enter failed: %v", err)
	}
	if got := bareTagOID(t, bare, "lw"); got != second {
		t.Fatalf("remote lw = %q, want %q after the Enter-submitted push", got, second)
	}
}

// A ticked force push of a tag is destructive: with the probe answered and
// the overwrite agreement ticked the button is enabled — so the refusal
// rests on the destructive rule — Enter changes nothing on the remote, and
// the dialog stays open.
func TestDialogSyncBranchPushTagForceEnterNeverPushes(t *testing.T) {
	repo, bare, first, second := tagsPushSidebarRepo(t, "dsb-pushtag-force-enter")
	repo.Git("push", "origin", "refs/tags/lw")
	// The remote carries the older spelling of the tag, so a force push
	// would observably move it.
	setBareTag(t, bare, "lw", first)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	openTagPushDialog(t, h, "lw")
	clickForcePushBox(t, h)
	h.drain()
	if !h.ui.HasText("will be overwritten") {
		t.Fatalf("overwrite warning missing after the probe; texts=%q", h.ui.Texts())
	}
	clickForceAgreement(t, h)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Force Push Tag"); !ok || btn.Disabled {
		t.Fatalf("Confirm Force Push Tag = %+v (found=%v), want enabled — the refusal must not rest on a disabled button", btn, ok)
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Force Push Tag button holds the focus")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if !h.app.tagsPushUI().pushOpen {
		t.Fatal("Enter closed the destructive force-push-tag dialog")
	}
	if got := bareTagOID(t, bare, "lw"); got != first {
		t.Fatalf("remote lw = %q, want unchanged %q — Enter force-pushed the tag", got, first)
	}
	if got := bareTagOID(t, bare, "lw"); got == second {
		t.Fatal("remote lw moved to the local object id although Enter must never confirm a force push")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("dialog is gone or its cancel is unreachable after Enter: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// Enter in the name field of New Branch creates the branch exactly once:
// the input takes the key before every window shortcut, so the frame's
// Enter cannot fire for the same press, and the one submit the input
// reports closes the dialog — a second submit would meet an empty draft
// and no-op even if it somehow ran, so no "already exists" error can
// surface either way.
func TestDialogSyncBranchNewBranchEnterCreatesOnce(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "dsb-nb-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "enter-branch")
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if !branchExists(t, repo, "enter-branch") {
		t.Fatal("enter-branch was not created by pressing Enter")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "enter-branch")); got != featureTip {
		t.Fatalf("enter-branch points at %q, want %q", got, featureTip)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter created the branch with an error — a double submit would report it here: %v", err)
	}
	if st := h.app.branchCreateUI(); st.open {
		t.Error("the New Branch dialog stayed open after the Enter submit")
	}
}

// Enter in the name field of Rename Branch renames exactly once: the new
// name points at the commit the branch had, the old name is gone, and no
// error surfaces.
func TestDialogSyncBranchRenameEnterRenamesOnce(t *testing.T) {
	repo, tip := renameFixture(t, "dsb-rename-enter", "topic")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openRenameDialog(t, h, "topic")
	clearInput(t, h.ui)
	h.ui.Type("topic-2")
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter renamed the branch with an error — a double submit would report it here: %v", err)
	}
	if got := branchHash(t, repo, "topic-2"); got != tip {
		t.Fatalf("refs/heads/topic-2 = %q, want the original commit %q", got, tip)
	}
	if branchExists(t, repo, "topic") {
		t.Fatal("old name topic survived the rename")
	}
	if st := branchManageUI(h.app); st.renOpen {
		t.Error("the Rename Branch dialog stayed open after the Enter submit")
	}
}

// Delete Branches is destructive: Enter never confirms it. Focused on a row
// checkbox the key belongs to the checkbox — the frame registers no Enter
// shortcut for a destructive dialog — so both branches survive and the
// dialog stays open; clicking the red Delete still deletes.
func TestDialogSyncBranchBatchDeleteEnterNeverDeletes(t *testing.T) {
	repo := pickFixture(t, "dsb-batch-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectBranchesForBatch(t, h, "alpha", "beta")
	chooseBatchItem(t, h, "alpha", "Delete 2 Branches…")
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Delete Branches"); !ok || btn.Disabled {
		t.Fatalf("Confirm Delete Branches = %+v (found=%v), want enabled — the refusal must not rest on a disabled button", btn, ok)
	} else if btn.Focused {
		t.Fatal("the destructive Confirm Delete Branches button holds the focus at open")
	}

	// Normalize the focus onto the alpha row checkbox without changing its
	// state: two clicks untick and retick it, so Enter lands where a user
	// reviewing the rows has it.
	for range 2 {
		if err := h.ui.Click("alpha"); err != nil {
			t.Fatalf("click alpha row checkbox: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "alpha")
	if !ok || !box.Focused {
		t.Fatalf("alpha row checkbox not focused after its clicks: %+v found=%v", box, ok)
	}
	if !box.Checked {
		t.Fatal("the normalization clicks left alpha unchecked")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	h.drain()

	if !hasExactText(h.ui, "Delete Branches") {
		t.Fatal("Enter closed the destructive Delete Branches dialog")
	}
	for _, br := range []string{"alpha", "beta"} {
		if !branchExists(t, repo, br) {
			t.Fatalf("%s is gone after Enter — the key must never confirm a deletion", br)
		}
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Enter ran a write that failed: %v", err)
	}

	// The click on the red Delete still deletes: re-check the row the Enter
	// unticked if it did, then confirm.
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "alpha"); ok && !box.Checked {
		if err := h.ui.Click("alpha"); err != nil {
			t.Fatalf("re-check alpha row: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
	}
	if err := h.ui.Click("Confirm Delete Branches"); err != nil {
		t.Fatalf("click Confirm Delete Branches: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batch delete error: %v", err)
	}
	for _, br := range []string{"alpha", "beta"} {
		if branchExists(t, repo, br) {
			t.Fatalf("%s survived the confirmed delete", br)
		}
	}
}

// Escape closes every one of the eight dialogs while no write runs, exactly
// as it did before the migration, and nothing of the dialog's operation
// starts.
func TestDialogSyncBranchEscapeCloses(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		repo := newGitRepo(t, "dsb-esc-fetch")
		repo.CommitFile("a.txt", "one\n", "initial commit")
		repo.AddBareRemote("origin")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarFetchDialog(t, h)
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if syncStateFor(h.app).fetchDlg.open {
			t.Fatal("Escape did not close the Fetch dialog")
		}
	})

	t.Run("pull", func(t *testing.T) {
		repo, _ := pullDialogFixture(t, "dsb-esc-pull")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPullDialog(t, h)
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if syncStateFor(h.app).pullDlg.open {
			t.Fatal("Escape did not close the Pull dialog")
		}
	})

	t.Run("push", func(t *testing.T) {
		repo, _, _ := aheadFixture(t, "dsb-esc-push")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openToolbarPushDialog(t, h)
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if pushDialogUI(h.app).open {
			t.Fatal("Escape did not close the Push dialog")
		}
	})

	t.Run("remote branch pull into", func(t *testing.T) {
		repo, _ := remotePullFixture(t, "dsb-esc-rbpull")

		h := newTestApp(t)
		closeSync(t, h)
		h.openRepo(repo.StoreRepo())
		h.drain()
		chooseRemotePullInto(t, h, "origin/feature", "main")
		assertPullDialog(t, h, "origin/feature", "main")
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if remoteBranchPullUI(h.app).pullOpen {
			t.Fatal("Escape did not close the Pull into dialog")
		}
	})

	t.Run("push tag", func(t *testing.T) {
		repo, _, _, _ := tagsPushSidebarRepo(t, "dsb-esc-pushtag")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		openTagPushDialog(t, h, "lw")
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if h.app.tagsPushUI().pushOpen {
			t.Fatal("Escape did not close the Push Tag dialog")
		}
	})

	t.Run("new branch", func(t *testing.T) {
		repo, _ := branchCreateFixture(t, "dsb-esc-nb")

		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()
		openNewBranchFromDialog(t, h, "feature")
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if h.app.branchCreateUI().open {
			t.Fatal("Escape did not close the New Branch dialog")
		}
	})

	t.Run("rename branch", func(t *testing.T) {
		repo, _ := renameFixture(t, "dsb-esc-rename", "topic")

		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()
		openRenameDialog(t, h, "topic")
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if branchManageUI(h.app).renOpen {
			t.Fatal("Escape did not close the Rename Branch dialog")
		}
	})

	t.Run("delete branches", func(t *testing.T) {
		repo := pickFixture(t, "dsb-esc-batch")

		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()
		selectBranchesForBatch(t, h, "alpha", "beta")
		chooseBatchItem(t, h, "alpha", "Delete 2 Branches…")
		if !hasExactText(h.ui, "Delete Branches") {
			t.Fatalf("Delete Branches dialog did not open; texts=%q", h.ui.Texts())
		}
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if h.app.refsView.batchDelOpen {
			t.Fatal("Escape did not close the Delete Branches dialog")
		}
		for _, br := range []string{"alpha", "beta"} {
			if !branchExists(t, repo, br) {
				t.Fatalf("%s is gone after Escape closed the dialog", br)
			}
		}
	})
}
