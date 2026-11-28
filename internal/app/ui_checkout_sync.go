package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// checkoutSyncUIState is the per-app state of the Checkout and Sync dialog:
// the branch it switches to with the commit and upstream it was opened
// against, the chosen action, and the repository generation they belong to.
type checkoutSyncUIState struct {
	repoGen uint64

	open       bool
	branch     string
	expectHash string
	upstream   string
	action     string
	stash      bool
}

// checkoutSyncUI returns the app's Checkout and Sync dialog state, creating
// it on first use and discarding every captured value when the repository
// generation moved past it: a switch or close makes the captured branch,
// commit and upstream meaningless, so nothing of the previous repository
// carries over.
func checkoutSyncUI(a *app) *checkoutSyncUIState {
	st := featureState(a, func() *checkoutSyncUIState { return &checkoutSyncUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearCheckoutSyncDraft()
	}
	return st
}

// clearCheckoutSyncDraft closes the dialog and drops every value captured
// for it, so a reopened dialog starts from the repository as it is then.
func (st *checkoutSyncUIState) clearCheckoutSyncDraft() {
	st.open = false
	st.branch = ""
	st.expectHash = ""
	st.upstream = ""
	st.action = ""
	st.stash = false
}

// checkoutSyncChoices are the four actions of the dialog's Action drop-down,
// in the drop-down's order.
var checkoutSyncChoices = []struct {
	action git.SyncAction
	title  string
}{
	{git.SyncFastForward, "Fast-Forward"},
	{git.SyncMerge, "Merge"},
	{git.SyncRebase, "Rebase"},
	{git.SyncReset, "Reset"},
}

func checkoutSyncActionTitle(action git.SyncAction) string {
	for _, ch := range checkoutSyncChoices {
		if ch.action == action {
			return ch.title
		}
	}
	return ""
}

func checkoutSyncActionOf(title string) (git.SyncAction, bool) {
	for _, ch := range checkoutSyncChoices {
		if ch.title == title {
			return ch.action, true
		}
	}
	return "", false
}

func checkoutSyncActionTitles() []string {
	titles := make([]string, 0, len(checkoutSyncChoices))
	for _, ch := range checkoutSyncChoices {
		titles = append(titles, ch.title)
	}
	return titles
}

// checkoutSyncNote is the one-line explanation shown under the Action
// drop-down for the chosen action.
func checkoutSyncNote(title, branch, upstream string) string {
	switch title {
	case "Merge":
		return "Checkout '" + branch + "' and merge '" + upstream + "' into it"
	case "Rebase":
		return "Checkout '" + branch + "' and rebase it on '" + upstream + "'"
	case "Reset":
		return "Checkout '" + branch + "' and reset it (--hard) to '" + upstream + "'"
	}
	return "Checkout '" + branch + "' and fast-forward it to '" + upstream + "'"
}

// openCheckoutSync opens the Checkout and Sync dialog for the branch. Everything
// the dialog or its worker needs later is captured now, on the UI thread: the
// branch name, the commit it points at, and the upstream it follows — the
// background guard refuses the write when any of them has moved since.
func (a *app) openCheckoutSync(br git.Branch, action git.SyncAction) {
	st := checkoutSyncUI(a)
	st.open = true
	st.branch = br.Name
	st.expectHash = br.Hash
	st.upstream = br.Upstream
	st.action = checkoutSyncActionTitle(action)
	st.stash = a.store.Sheet(store.CheckoutSyncAutostash, false)
}

// checkoutSyncDialog is the modal switching to a branch and bringing it level
// with its upstream in one step: by fast-forward, merge, rebase or hard reset.
// The confirm button stays disabled while a write or an operation is in
// progress, and Enter submits exactly like it.
func (a *app) checkoutSyncDialog(c *ui.Context) {
	p := a.pal
	st := checkoutSyncUI(a)
	if !st.open {
		return
	}
	_, cancelled, confirmed := dialogFrame(c, p, "checkout-sync-modal", &st.open, "Checkout and Sync", 460, func() {
		ui.Field(c, "Branch:", func() {
			ui.Text(c, st.branch).Font("monospace").FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
		})
		ui.Field(c, "Upstream:", func() {
			ui.Text(c, st.upstream).Font("monospace").FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
		})
		ui.Field(c, "Action:", func() {
			ui.Select(c.Key("checkout-sync-action"), &st.action, checkoutSyncActionTitles()).FillWidth().Label("Action")
		})
		ui.Text(c, checkoutSyncNote(st.action, st.branch, st.upstream)).FontSize(12).TextColor(p.muted).MaxWidth(424)
		if st.action == "Reset" {
			ui.Text(c, "Commits on '"+st.branch+"' that '"+st.upstream+"' does not have will no longer be on any branch. Local changes are discarded unless stashed.").FontSize(12).TextColor(p.danger).MaxWidth(424)
		}
		ui.Checkbox(c.Key("checkout-sync-stash"), &st.stash, "Stash and reapply local changes").Label("Stash and reapply local changes")
	},
		dialogAction{key: "checkout-sync-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "checkout-sync-confirm", text: "Checkout", label: "Confirm Checkout and Sync", disabled: a.checkoutSyncBusy(), autoFocus: true})
	if cancelled {
		a.closeCheckoutSync()
	}
	if confirmed {
		a.submitCheckoutSync()
	}
	if !st.open {
		a.closeCheckoutSync()
	}
}

// checkoutSyncBusy reports whether the dialog's confirm must hold off: a
// write is busy or a merge, rebase, cherry-pick or revert is in progress.
func (a *app) checkoutSyncBusy() bool {
	return a.session.Busy() || a.session.Snapshot().Status.State != ""
}

// closeCheckoutSync closes the dialog and drops every value captured for it.
// Canceling, dismissing, submitting and switching repositories all end here,
// so no captured expectation survives the dialog.
func (a *app) closeCheckoutSync() {
	st := checkoutSyncUI(a)
	st.clearCheckoutSyncDraft()
}

// submitCheckoutSync runs the checkout and sync the dialog describes through
// the session's write path: the operation is local — it needs no server
// credentials — so it does not go through the sync repository copy. The
// checkbox goes back to the store first; a failed save is a toast and never
// stops the operation. Everything the worker needs was captured when the
// dialog opened: it re-validates the repository against those values before
// touching anything.
func (a *app) submitCheckoutSync() {
	st := checkoutSyncUI(a)
	if a.session.Busy() {
		return
	}
	branch := st.branch
	expectHash := st.expectHash
	upstream := st.upstream
	action, ok := checkoutSyncActionOf(st.action)
	stash := st.stash
	a.closeCheckoutSync()
	if branch == "" || upstream == "" || !ok {
		return
	}
	if err := a.store.SetSheets(map[string]bool{store.CheckoutSyncAutostash: stash}); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
	a.session.do("Checking out "+branch, func(repo *git.Repo) error {
		return checkoutSyncWithGuard(repo, branch, expectHash, upstream, action, stash)
	})
}

// checkoutSyncWithGuard re-validates the repository against what the Checkout
// and Sync dialog captured before it switches anything: no merge, rebase,
// cherry-pick or revert may be in progress, the branch must still exist and
// point at the commit the dialog opened against, and its upstream must still
// be the same live upstream. A fast-forward additionally refuses a branch
// that has commits the upstream does not, since only merge, rebase or reset
// can reconcile those. Any check failing returns the error without touching
// HEAD, the index, the working tree or any ref.
func checkoutSyncWithGuard(repo *git.Repo, branch, expectHash, upstream string, action git.SyncAction, stash bool) error {
	switch action {
	case git.SyncFastForward, git.SyncMerge, git.SyncRebase, git.SyncReset:
	default:
		return fmt.Errorf("cannot checkout %s: unknown action", branch)
	}
	if branch == "" || upstream == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\r\n\x00") || strings.HasPrefix(upstream, "-") || strings.ContainsAny(upstream, "\x00\r\n") {
		return fmt.Errorf("cannot checkout: invalid branch or upstream")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot checkout %s: reading the repository state failed: %w", branch, err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot checkout %s: a %s is in progress; finish or abort it first", branch, st.State)
	}
	refs, err := repo.Refs()
	if err != nil {
		return fmt.Errorf("cannot checkout %s: reading repository references failed: %w", branch, err)
	}
	br := findLocalBranch(refs, branch)
	if br == nil || br.Hash != expectHash || br.Upstream != upstream || br.Gone {
		return fmt.Errorf("cannot checkout %s: the branch or its upstream changed after the dialog opened; review the repository and try again", branch)
	}
	if action == git.SyncFastForward {
		ahead, err := repo.CommitsBetween(upstream, branch)
		if err != nil {
			return fmt.Errorf("cannot checkout %s: comparing it with %s failed: %w", branch, upstream, err)
		}
		if ahead > 0 {
			return fmt.Errorf("cannot fast-forward %s: it has commits that %s does not; choose Merge, Rebase or Reset instead", branch, upstream)
		}
	}
	return repo.CheckoutAndSync(branch, upstream, action, stash, false)
}
