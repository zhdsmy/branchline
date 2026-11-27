package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remoteBranchPullUIState is the per-app state of the Pull into '<branch>'
// dialog of the remote branch context menu: the remote and branch it pulls,
// the current branch and HEAD it was opened against, and the two options.
// Every captured value belongs to one repository generation.
type remoteBranchPullUIState struct {
	repoGen uint64

	pullOpen         bool
	pullRemote       string
	pullBranch       string // the branch on the remote, such as feature
	pullRemoteBranch string // the remote branch as the row shows it, such as origin/feature
	pullInto         string // the current branch the pull merges into
	pullExpectHead   string
	pullRebase       bool
	pullStash        bool
}

// remoteBranchPullUI returns the app's pull-into dialog state, creating it on
// first use and discarding every captured value when the repository
// generation moved past it: a switch or close makes the captured branch, HEAD
// and remote meaningless, so nothing of the previous repository carries over.
func remoteBranchPullUI(a *app) *remoteBranchPullUIState {
	st := featureState(a, func() *remoteBranchPullUIState { return &remoteBranchPullUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearDraft()
	}
	return st
}

// clearDraft closes the dialog and drops every value captured for it, so a
// reopened dialog starts from the repository as it is then.
func (st *remoteBranchPullUIState) clearDraft() {
	st.pullOpen = false
	st.pullRemote = ""
	st.pullBranch = ""
	st.pullRemoteBranch = ""
	st.pullInto = ""
	st.pullExpectHead = ""
	st.pullRebase = false
	st.pullStash = false
}

// remoteBranchPullMenuItems adds the Pull into '<current branch>' item and
// its separator to the context menu of one remote-tracking branch of the
// sidebar, directly behind Checkout. The item needs a current branch to pull
// into, so it hides on a detached HEAD; the remote's HEAD symbolic reference
// names no branch of its own and never shows it. It is disabled while a
// write or sync is busy and while a merge, rebase, cherry-pick or revert is
// in progress.
func (a *app) remoteBranchPullMenuItems(m *ui.Menu, rb git.Branch) {
	if remoteBranchIsHEADSymref(rb) {
		return
	}
	snap := a.session.Snapshot()
	cur := snap.Status.Branch
	if cur == "" {
		return
	}
	disabled := a.session.Busy() || snap.Status.State != ""
	if m.Item("Pull into '" + cur + "'…").Disabled(disabled).Chosen() {
		a.openRemoteBranchPull(rb)
	}
	m.Separator()
}

// openRemoteBranchPull opens the Pull dialog for the remote-tracking branch.
// Everything the dialog or its worker needs later is captured now, on the UI
// thread: the remote and the branch on it, and the current branch name and
// HEAD hash the pull must still find when it runs — the background guard
// refuses the write when any of them has moved since.
func (a *app) openRemoteBranchPull(rb git.Branch) {
	st := remoteBranchPullUI(a)
	snap := a.session.Snapshot()
	st.pullOpen = true
	st.pullRemote = rb.Remote
	st.pullBranch = strings.TrimPrefix(rb.Name, rb.Remote+"/")
	st.pullRemoteBranch = rb.Name
	st.pullInto = snap.Status.Branch
	st.pullExpectHead = snap.Status.Head
	st.pullRebase = false
	st.pullStash = false
}

// remoteBranchPullDialog is the modal pulling one branch of a remote into the
// current branch: it names the source and the target, offers rebase and
// stash as the two choices of the pull, and its confirm button stays
// disabled while a write or an operation is in progress.
func (a *app) remoteBranchPullDialog(c *ui.Context) {
	p := a.pal
	st := remoteBranchPullUI(a)
	if !st.pullOpen {
		return
	}
	ui.Modal(c.Key("rb-pull-modal"), &st.pullOpen, func() {
		ui.Column(c.Key("rb-pull-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Pull").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "From:", func() {
				ui.Text(c, st.pullRemoteBranch).Font("monospace").FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			ui.Field(c, "Into:", func() {
				ui.Text(c, st.pullInto).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Checkbox(c.Key("rb-pull-rebase"), &st.pullRebase, "Rebase instead of merge").Label("Rebase instead of merge")
			ui.Checkbox(c.Key("rb-pull-stash"), &st.pullStash, "Stash and reapply local changes").Label("Stash and reapply local changes")
			ui.Row(c.Key("rb-pull-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("rb-pull-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeRemoteBranchPull()
				}
				if ui.PrimaryButton(c.Key("rb-pull-confirm"), "Pull").Label("Confirm Pull Remote Branch").Disabled(a.session.Busy() || a.session.Snapshot().Status.State != "").Clicked() {
					a.submitRemoteBranchPull()
				}
			})
		})
	})
	if !st.pullOpen {
		a.closeRemoteBranchPull()
	}
}

// closeRemoteBranchPull closes the dialog and drops every value captured for
// it. Canceling, dismissing, submitting and switching repositories all end
// here, so no captured expectation survives the dialog.
func (a *app) closeRemoteBranchPull() {
	st := remoteBranchPullUI(a)
	st.clearDraft()
}

// submitRemoteBranchPull runs the pull the dialog describes through the
// session's write path, on the sync repository copy whose executor asks
// through the in-memory askpass dialog — a pull needs the server's
// credentials exactly like a fetch. Everything the worker needs was captured
// when the dialog opened: it re-validates the repository against those values
// before pulling. The completion is dropped when it belongs to a repository
// the user has since switched away from.
func (a *app) submitRemoteBranchPull() {
	st := remoteBranchPullUI(a)
	if a.session.Busy() {
		return
	}
	remote := st.pullRemote
	branch := st.pullBranch
	expectBranch := st.pullInto
	expectHead := st.pullExpectHead
	rebase := st.pullRebase
	stash := st.pullStash
	a.closeRemoteBranchPull()
	if remote == "" || branch == "" || expectBranch == "" {
		return
	}
	syncSt := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	syncSt.syncErr = ""
	a.session.ClearActionErr()
	syncSt.syncWorkerStarted()
	if !a.session.doThen("Pulling", func(*git.Repo) error {
		defer syncSt.syncWorkerDone()
		note, err := remoteBranchPullWithGuard(repo, remote, branch, expectBranch, expectHead, rebase, stash)
		return noted(note, err)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
		if note := syncNote(err); note != "" {
			a.session.ClearActionErr()
			a.toast = note
		}
	}) {
		syncSt.syncWorkerDone()
	}
}

// remoteBranchPullWithGuard re-validates the repository against what the Pull
// dialog captured before it pulls anything: no Git operation may be in
// progress, HEAD and the checked-out branch must still be exactly where the
// dialog saw them, and the remote must still exist. Any check failing returns
// the error without touching HEAD, the index, the working tree or any ref.
// Only then does the pull run, merging or rebasing the remote branch into the
// current one. Pull errors are redacted before returning so embedded remote
// URL credentials never reach any error display.
func remoteBranchPullWithGuard(repo *git.Repo, remote, branch, expectBranch, expectHead string, rebase, stash bool) (string, error) {
	if remote == "" || branch == "" || expectBranch == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n\x00") || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, "/") || strings.ContainsAny(branch, " \t\r\n\x00") {
		return "", fmt.Errorf("cannot pull: invalid remote branch")
	}
	st, err := repo.Status()
	if err != nil {
		return "", fmt.Errorf("cannot pull %s/%s: reading the repository state failed: %w", remote, branch, err)
	}
	if st.State != "" {
		return "", fmt.Errorf("cannot pull %s/%s: a %s is in progress; finish or abort it first", remote, branch, st.State)
	}
	if st.Branch != expectBranch || st.Head != expectHead {
		return "", fmt.Errorf("cannot pull %s/%s: HEAD or the current branch changed after the dialog opened; review the repository and try again", remote, branch)
	}
	refs, err := repo.Refs()
	if err != nil {
		return "", fmt.Errorf("cannot pull %s/%s: reading repository references failed: %w", remote, branch, err)
	}
	if !slices.Contains(refs.Remotes, remote) {
		return "", fmt.Errorf("cannot pull %s/%s: remote %q no longer exists", remote, branch, remote)
	}
	note, err := repo.PullNote(git.PullOptions{Remote: remote, Branch: branch, Rebase: rebase, Stash: stash})
	if err != nil {
		return "", errors.New(git.RedactText(err.Error()))
	}
	return note, nil
}
