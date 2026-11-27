package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// The choices of the checkout-blocked dialog: stashing the local changes
// aside for the checkout and reapplying them afterwards, or discarding them
// in the checkout itself.
const (
	checkoutBlockedStash   = "stash"
	checkoutBlockedDiscard = "discard"
)

// checkoutBlockedUIState is the per-app state of the dialog a blocked
// checkout opens: the target it is about, the files git named, the user's
// choice, and the retry the buttons run. Everything belongs to one
// repository generation: a switch or close makes the captured checkout
// meaningless, so nothing of the previous repository carries over.
type checkoutBlockedUIState struct {
	repoGen uint64

	open      bool
	label     string
	target    string
	changed   []string
	untracked []string
	choice    string

	// run is the checkout the dialog retries — the same closure the first
	// attempt ran, captured on the UI thread. It re-validates everything it
	// guarded before touching the repository, force only deciding whether
	// git discards what is in the way.
	run func(r *git.Repo, force bool) error
}

// checkoutBlockedUI returns the app's checkout-blocked dialog state,
// creating it on first use and discarding every captured value when the
// repository generation moved past it.
func checkoutBlockedUI(a *app) *checkoutBlockedUIState {
	st := featureState(a, func() *checkoutBlockedUIState { return &checkoutBlockedUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clear()
	}
	return st
}

// clear closes the dialog and drops every value captured for it, so a
// reopened dialog starts from the repository as it is then.
func (st *checkoutBlockedUIState) clear() {
	*st = checkoutBlockedUIState{repoGen: st.repoGen}
}

// checkoutOfferingStash runs a checkout and, when git refuses it because
// local changes or untracked files would be overwritten, opens the dialog
// offering to stash them aside or to discard them instead of showing the
// error. Any other failure surfaces through the usual error banner.
//
// label is the busy label of both the first attempt and the retry the
// dialog runs, target how the dialog names what is being checked out, and
// run the checkout itself, told by the retry whether git may discard what
// is in the way. The blocked verdict is read inside the write worker: the
// error after sees is not necessarily the original git error, so the files
// travel in worker-written variables.
func (a *app) checkoutOfferingStash(label, target string, run func(r *git.Repo, force bool) error) {
	var blocked git.CheckoutBlocked
	wasBlocked := false
	a.session.doThen(label, func(r *git.Repo) error {
		err := run(r, false)
		blocked, wasBlocked = git.CheckoutBlockedBy(err)
		return err
	}, func(err error) {
		if !wasBlocked {
			return
		}
		a.session.ClearActionErr()
		st := checkoutBlockedUI(a)
		st.open = true
		st.label = label
		st.target = target
		st.changed = blocked.Changed
		st.untracked = blocked.Untracked
		st.choice = checkoutBlockedStash
		st.run = run
	})
}

// checkoutBlockedDialog is the modal shown when a checkout would overwrite
// local changes: it lists the files in the way and offers to stash them
// aside and reapply them after the checkout, or to discard them in the
// checkout. Stashing is the default and confirms with Enter; discarding is
// destructive, so Enter never arms it.
func (a *app) checkoutBlockedDialog(c *ui.Context) {
	p := a.pal
	st := checkoutBlockedUI(a)
	if !st.open {
		return
	}
	stashChosen := st.choice != checkoutBlockedDiscard
	primaryText := "Stash and Checkout"
	if !stashChosen {
		primaryText = "Discard and Checkout"
	}
	_, cancelled, confirmed := dialogFrame(c, p, "checkout-blocked-modal", &st.open, "Local Changes Would Be Overwritten", 460, func() {
		ui.Text(c, "Checking out '"+st.target+"' would overwrite:").FontSize(12.5).TextColor(p.text).MaxWidth(424)
		if len(st.changed) > 0 {
			ui.Text(c.Key("checkout-blocked-changed-header"), "Changed files").FontSize(12).FontWeight(600).TextColor(p.text)
			for i, f := range st.changed {
				if i >= checkoutBlockedMaxFiles {
					ui.Text(c.Key("checkout-blocked-changed-more"), fmt.Sprintf("and %d more", len(st.changed)-checkoutBlockedMaxFiles)).FontSize(11.5).TextColor(p.muted)
					break
				}
				ui.Text(c.Key(fmt.Sprintf("checkout-blocked-changed-%d", i)), f).Font("monospace").FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…")
			}
		}
		if len(st.untracked) > 0 {
			ui.Text(c.Key("checkout-blocked-untracked-header"), "Untracked files").FontSize(12).FontWeight(600).TextColor(p.text)
			for i, f := range st.untracked {
				if i >= checkoutBlockedMaxFiles {
					ui.Text(c.Key("checkout-blocked-untracked-more"), fmt.Sprintf("and %d more", len(st.untracked)-checkoutBlockedMaxFiles)).FontSize(11.5).TextColor(p.muted)
					break
				}
				ui.Text(c.Key(fmt.Sprintf("checkout-blocked-untracked-%d", i)), f).Font("monospace").FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…")
			}
		}
		ui.RadioGroup(c, func() {
			ui.Radio(c.Key("checkout-blocked-stash"), &st.choice, checkoutBlockedStash, "Stash local changes, check out, then reapply them").Label("Stash local changes, check out, then reapply them")
			ui.Text(c.Key("checkout-blocked-stash-note"), "If reapplying conflicts, the changes stay in the stash list.").FontSize(11).TextColor(p.muted).MaxWidth(408).Padding(0, 0, 0, 22)
			ui.Radio(c.Key("checkout-blocked-discard"), &st.choice, checkoutBlockedDiscard, "Discard local changes").Label("Discard local changes")
			ui.Text(c.Key("checkout-blocked-discard-note"), "Uncommitted changes to all tracked files are lost, and the untracked files listed are overwritten.").FontSize(11).TextColor(p.danger).MaxWidth(408).Padding(0, 0, 0, 22)
		}).Label("How to check out")
	},
		dialogAction{key: "checkout-blocked-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "checkout-blocked-confirm", text: primaryText, label: primaryText, disabled: a.session.Busy(), destructive: !stashChosen, autoFocus: true})
	if cancelled {
		a.closeCheckoutBlocked()
	}
	if confirmed {
		a.submitCheckoutBlocked()
	}
	if !st.open {
		a.closeCheckoutBlocked()
	}
}

// checkoutBlockedMaxFiles is how many paths of one group the dialog lists
// before folding the rest into "and N more".
const checkoutBlockedMaxFiles = 8

// closeCheckoutBlocked closes the dialog and drops every value captured for
// it. Canceling, dismissing, submitting and switching repositories all end
// here, so no captured checkout survives the dialog.
func (a *app) closeCheckoutBlocked() {
	st := checkoutBlockedUI(a)
	st.clear()
}

// submitCheckoutBlocked runs the retry the dialog captured through the
// session's write path: with the stash choice the changes are saved aside,
// the checkout runs plain — anything still refusing it leaves the changes
// back in the working tree — and the reapply follows, while the discard
// choice retries the checkout forced.
func (a *app) submitCheckoutBlocked() {
	st := checkoutBlockedUI(a)
	if a.session.Busy() {
		return
	}
	label := st.label
	run := st.run
	choice := st.choice
	a.closeCheckoutBlocked()
	if label == "" || run == nil {
		return
	}
	if choice == checkoutBlockedDiscard {
		a.session.do(label, func(r *git.Repo) error {
			return run(r, true)
		})
		return
	}
	a.session.do(label, func(r *git.Repo) error {
		return r.WithSavedChanges("Branchline checkout", func() error {
			return run(r, false)
		})
	})
}
