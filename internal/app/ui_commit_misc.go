package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// commitMiscUIState is the per-app state of the commit context menu items
// this file owns: the Checkout Commit dialog draft with the HEAD and branch
// it was confirmed against, and the clipboard text a copy menu item asked
// for.
type commitMiscUIState struct {
	repoGen uint64

	checkoutOpen         bool
	checkoutExpectedHead string
	checkoutBranch       string
	checkoutTarget       git.Commit

	pendingClipboard    string
	hasPendingClipboard bool
}

// commitMiscUI returns the app's state of the misc commit actions, creating
// it on first use and discarding every open dialog draft when the repository
// generation moved past it: a switch or close makes the captured target, HEAD
// and branch meaningless, so the dialog of the previous repository never
// carries over.
func commitMiscUI(a *app) *commitMiscUIState {
	st := featureState(a, func() *commitMiscUIState { return &commitMiscUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearCheckoutDraft()
	}
	return st
}

// clearCheckoutDraft closes the Checkout Commit dialog and drops every value
// captured for it, so a reopened dialog starts from the repository as it is
// then.
func (st *commitMiscUIState) clearCheckoutDraft() {
	st.checkoutOpen = false
	st.checkoutExpectedHead = ""
	st.checkoutBranch = ""
	st.checkoutTarget = git.Commit{}
}

// commitInfo is the multi-line text Copy Commit Info puts on the clipboard:
// the full hash, the author with their email, the author time formatted to
// the minute — an empty line for a commit without one — and, separated by a
// blank line, the subject.
func commitInfo(cm git.Commit) string {
	when := ""
	if !cm.Date.IsZero() {
		when = cm.Date.Format("2006-01-02 15:04")
	}
	return strings.Join([]string{cm.Hash, cm.Author + " <" + cm.AuthorEmail + ">", when, "", cm.Subject}, "\n")
}

// copyCommitMenuItems adds the copy items at the tail of the commit context
// menu, behind a separator. They never disable: copying reads the commit the
// menu was opened on, nothing of the repository's moving state.
//
// A menu item's Chosen runs inside the menu's build, which receives no
// *ui.Context — and mygo's clipboard is only writable with one, on the main
// thread, in the view or an input handler. The choice therefore records the
// text, and checkoutCommitModal, the one mount point of this file every
// History frame passes, writes it with the frame's own context.
func (a *app) copyCommitMenuItems(m *ui.Menu, cm git.Commit) {
	st := commitMiscUI(a)
	m.Separator()
	if m.Item("Copy Commit SHA").Chosen() {
		st.pendingClipboard = cm.Hash
		st.hasPendingClipboard = true
	}
	if m.Item("Copy Commit Info").Chosen() {
		st.pendingClipboard = commitInfo(cm)
		st.hasPendingClipboard = true
	}
}

// checkoutCommitMenuItem is the top item of the commit context menu: checking
// out the commit detaches HEAD at it. It disables while a repository write
// runs or another operation is in progress, because moving HEAD then would
// corrupt it, and while HEAD already is detached at exactly this commit,
// where the checkout would be a no-op.
func (a *app) checkoutCommitMenuItem(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	detachedThere := snap.Status.Branch == "" && snap.Status.Head == cm.Hash
	disabled := a.session.Busy() || snap.Status.State != "" || detachedThere
	if m.Item("Checkout Commit…").Disabled(disabled).Chosen() {
		a.openCheckoutModal(cm)
	}
}

// openCheckoutModal opens the Checkout Commit dialog for the commit cm.
// Everything the dialog or its worker needs later is captured now, on the UI
// thread: the commit itself with its parents cloned, and the HEAD hash and
// branch the checkout must still find when it runs — the background guard
// refuses the write when either has moved since, even if a background reload
// updated the snapshot while the dialog was open.
func (a *app) openCheckoutModal(cm git.Commit) {
	st := commitMiscUI(a)
	snap := a.session.Snapshot()
	cm.Parents = append([]string(nil), cm.Parents...)
	st.checkoutOpen = true
	st.checkoutExpectedHead = snap.Status.Head
	st.checkoutBranch = snap.Status.Branch
	st.checkoutTarget = cm
}

// checkoutCommitModal mounts the Checkout Commit dialog — the composition
// root commitActionModals calls on every History frame — and flushes the
// clipboard text a copy menu item recorded, which needs a frame's
// *ui.Context the menu build never receives. The flush precedes the dialog
// so a copy chosen on any History frame reaches the clipboard in the same
// frame whether or not the dialog is open.
func (a *app) checkoutCommitModal(c *ui.Context) {
	p := a.pal
	st := commitMiscUI(a)
	if st.hasPendingClipboard {
		c.WriteClipboard(st.pendingClipboard)
		st.pendingClipboard = ""
		st.hasPendingClipboard = false
	}
	if !st.checkoutOpen {
		return
	}

	_, cancelled, confirmed := dialogFrame(c, p, "commit-checkout-modal", &st.checkoutOpen, "Checkout Commit", 460, func() {
		ui.Text(c, "HEAD will be detached at "+st.checkoutTarget.Short()+" "+st.checkoutTarget.Subject).FontSize(12.5).TextColor(p.muted).SingleLine().Ellipsis("…")
	},
		dialogAction{key: "checkout-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "checkout-confirm", text: "Checkout", label: "Confirm Checkout Commit", disabled: a.session.Busy(), autoFocus: true})
	if cancelled {
		a.closeCheckoutModal()
	}
	if confirmed {
		a.submitCheckout()
	}
	if !st.checkoutOpen {
		a.closeCheckoutModal()
	}
}

// closeCheckoutModal closes the Checkout Commit dialog and clears its draft.
// Canceling, dismissing and submitting all end here so no captured target
// survives the dialog.
func (a *app) closeCheckoutModal() {
	st := commitMiscUI(a)
	st.clearCheckoutDraft()
}

// submitCheckout runs the checkout the dialog describes through the session's
// write path. The target hash, HEAD and branch captured when the dialog
// opened are passed by value to the background worker, which re-reads the
// repository and refuses the write when what it finds differs.
func (a *app) submitCheckout() {
	st := commitMiscUI(a)
	if a.session.Busy() {
		return
	}
	target := st.checkoutTarget.Hash
	expectedHead := st.checkoutExpectedHead
	expectedBranch := st.checkoutBranch
	a.closeCheckoutModal()
	a.checkoutOfferingStash("Checking out commit", shortTagHash(target), func(repo *git.Repo, force bool) error {
		return checkoutWithGuard(repo, expectedHead, expectedBranch, target, force)
	})
}

// checkoutWithGuard re-validates the repository against what the Checkout
// Commit confirmation captured before it detaches HEAD: the status must still
// be readable, no merge, rebase, cherry-pick or revert may be in progress,
// HEAD and the branch must still be exactly where the confirmation saw them,
// and the target commit must still exist. Only then does the checkout run —
// plain, so Git itself refuses when local changes would be overwritten,
// without touching the working tree, the index or HEAD. The checks run on
// every attempt, including the retry a blocked checkout offers: force is the
// retry's choice alone and decides whether git may discard what is in the
// way.
func checkoutWithGuard(repo *git.Repo, expectedHead, expectedBranch, targetHash string, force bool) error {
	if targetHash == "" {
		return fmt.Errorf("cannot check out: no target commit")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot check out: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot check out: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot check out: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if _, err := repo.PathHash(targetHash + "^{commit}"); err != nil {
		return fmt.Errorf("cannot check out: the target commit %s no longer exists: %w", targetHash, err)
	}
	if force {
		return repo.CheckoutForce(targetHash)
	}
	return repo.Checkout(targetHash)
}
