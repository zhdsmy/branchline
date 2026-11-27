package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// pullDialogState is the Pull dialog draft inside the app's sync state: the
// remote list and remote-tracking branches captured from the snapshot when
// the dialog opened, the chosen remote and branch with the branch list of
// the chosen remote, the current branch and HEAD the pull must still find
// when it runs, and the two check boxes. It lives in the sync state the
// generation check already resets on a repository switch or close, so
// nothing captured for one repository carries over to the next.
type pullDialogState struct {
	open       bool
	remote     string
	remotes    []string
	branch     string
	branches   []string
	remoteRefs []git.Branch
	into       string
	expectHead string
	upRemote   string
	upBranch   string
	rebase     bool
	stash      bool
}

// Pull pre-flight validation messages.
const (
	errMsgPullNoRemote = "The repository has no remote to pull from."
	errMsgPullDetached = "HEAD is detached: check out a branch to pull into it."
)

// pullDialogBranches is the branch choice of one remote: the remote-tracking
// branches captured when the dialog opened, without the remote prefix and
// without the remote's HEAD symbolic reference, in the order the snapshot
// listed them.
func pullDialogBranches(remoteRefs []git.Branch, remote string) []string {
	var names []string
	seen := map[string]bool{}
	for _, rb := range remoteRefs {
		if rb.Remote != remote {
			continue
		}
		short := strings.TrimPrefix(rb.Name, rb.Remote+"/")
		if short == "" || short == "HEAD" || seen[short] {
			continue
		}
		seen[short] = true
		names = append(names, short)
	}
	return names
}

// pullDialogBranchDefault picks the branch the choice starts on: the current
// branch's upstream branch when it is on this remote and the choice lists
// it, else the branch sharing the current branch's name, else the first.
func pullDialogBranchDefault(branches []string, upRemote, upBranch, remote, into string) string {
	if upRemote == remote && upBranch != "" && slices.Contains(branches, upBranch) {
		return upBranch
	}
	if into != "" && slices.Contains(branches, into) {
		return into
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

// pullDialogResetBranches rebuilds the branch choice of the dialog's remote
// from the captured remote-tracking branches and re-selects its default. It
// runs when the dialog opens and when the remote choice changes — never per
// frame — so the list the user sees is stable while the dialog is open.
func pullDialogResetBranches(st *pullDialogState) {
	st.branches = pullDialogBranches(st.remoteRefs, st.remote)
	st.branch = pullDialogBranchDefault(st.branches, st.upRemote, st.upBranch, st.remote, st.into)
}

// openPullDialog opens the toolbar Pull dialog. A busy session or a missing
// repository leaves every state untouched, and a snapshot that never loaded
// defers to the quick pull's live probe instead of opening an empty dialog,
// exactly as the quick pull handles it. A repository without remotes or a
// detached HEAD is a pre-flight error in the dismissible toolbar strip
// rather than a dialog. Everything the dialog and its worker need later is
// captured here, on the UI thread: the remote list, the remote-tracking
// branches, the current branch name and HEAD hash the pull must still find
// when it runs — the background guard refuses the write when any of them
// has moved since — and the remembered check boxes.
func (a *app) openPullDialog() {
	if a.session.Busy() || !a.session.HasRepo() {
		return
	}
	if !a.session.Loaded() || a.session.LoadErr() != nil {
		// The snapshot never loaded — an SSH repository whose encrypted key
		// had no askpass yet, or a repository that broke after opening: the
		// quick pull probes the real state through the askpass channel and
		// reports what it finds in the toolbar.
		a.syncPull()
		return
	}
	snap := a.session.Snapshot()
	st := syncStateFor(a)
	st.syncErr = ""
	a.session.ClearActionErr()
	if len(snap.Refs.Remotes) == 0 {
		st.syncErr = errMsgPullNoRemote
		return
	}
	if snap.Status.Branch == "" {
		st.syncErr = errMsgPullDetached
		return
	}
	dlg := &st.pullDlg
	*dlg = pullDialogState{}
	dlg.open = true
	dlg.remotes = slices.Clone(snap.Refs.Remotes)
	dlg.remoteRefs = slices.Clone(snap.Refs.Remote)
	dlg.remote = fetchDialogRemoteDefault(currentBranchOf(snap.Refs), dlg.remotes)
	dlg.into = snap.Status.Branch
	dlg.expectHead = snap.Status.Head
	if cur := currentBranchOf(snap.Refs); cur != nil && cur.Upstream != "" {
		dlg.upRemote, dlg.upBranch, _ = splitBranchUpstream(cur.Upstream, dlg.remotes)
	}
	dlg.rebase = a.store.Sheet(store.PullRebase, false)
	dlg.stash = a.store.Sheet(store.PullStashAndReapply, false)
	pullDialogResetBranches(dlg)
}

// closePullDialog closes the Pull dialog and clears its draft. Canceling,
// dismissing, submitting and switching repositories all end here so no
// captured branch, remote or HEAD survives the dialog.
func (a *app) closePullDialog() {
	syncStateFor(a).pullDlg = pullDialogState{}
}

// pullDialogCanSubmit reports whether the dialog's confirm button can run:
// no repository write running, no merge, rebase, cherry-pick or revert in
// progress, and a branch chosen from a non-empty list.
func pullDialogCanSubmit(st *pullDialogState, busy bool, opState string) bool {
	return !busy && opState == "" && len(st.branches) > 0
}

// pullDialog is the modal of the toolbar Pull: the remote and the branch on
// it to pull into the current branch, with rebase and stash as the two
// choices of the pull. Enter submits like the confirm button — except while
// the button is disabled — and Cancel, Escape, the backdrop and a
// repository switch close without pulling or remembering anything.
func (a *app) pullDialog(c *ui.Context) {
	p := a.pal
	st := &syncStateFor(a).pullDlg
	if !st.open {
		return
	}
	if ui.Modal(c.Key("pull-dialog-modal"), &st.open, func() {
		ui.Column(c.Key("pull-dialog-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Pull").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Remote:", func() {
				if ui.Select(c, &st.remote, st.remotes).FillWidth().Label("Pull Remote").Changed() {
					// A different remote lists its own branches and the choice
					// starts from that remote's default again.
					pullDialogResetBranches(st)
				}
			})
			ui.Field(c, "Branch:", func() {
				ui.Select(c, &st.branch, st.branches).FillWidth().Label("Pull Branch")
			})
			ui.Field(c, "Into:", func() {
				ui.Text(c, st.into).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Checkbox(c.Key("pull-dialog-rebase"), &st.rebase, "Rebase instead of merge").Label("Rebase instead of merge").Tooltip("--rebase")
			ui.Checkbox(c.Key("pull-dialog-stash"), &st.stash, "Stash and reapply local changes").Label("Stash and reapply local changes")
			ui.Row(c.Key("pull-dialog-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("pull-dialog-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closePullDialog()
				}
				if ui.PrimaryButton(c.Key("pull-dialog-confirm"), "Pull").
					Label("Confirm Pull").
					Disabled(!pullDialogCanSubmit(st, a.session.Busy(), a.session.Snapshot().Status.State)).
					AutoFocus().
					Clicked() {
					a.submitPullDialog()
				}
			})
			// Enter is the confirm button, as in every dialog: a focused
			// control that takes the key — an open remote or branch choice, a
			// focused button — consumes it first, so this fires only for the
			// Enter nothing else claimed.
			if c.Shortcut(0, ui.KeyEnter) && pullDialogCanSubmit(st, a.session.Busy(), a.session.Snapshot().Status.State) {
				a.submitPullDialog()
			}
		})
	}).Dismissed() {
		a.closePullDialog()
	}
}

// submitPullDialog captures the dialog's choices on the UI thread, closes
// the dialog first — an askpass modal of the pull must never stack on it —
// remembers the two check boxes in the store, and pulls through the
// session's sync lifecycle: the askpass-enabled repository copy, the worker
// counting, and the up-to-date note surfacing as a toast instead of an
// error. A failed save surfaces as a toast and never holds the pull back.
// The worker re-validates the repository against the captured expectations
// before pulling anything.
func (a *app) submitPullDialog() {
	st := syncStateFor(a)
	dlg := st.pullDlg
	if !dlg.open || a.session.Busy() {
		return
	}
	a.closePullDialog()
	if err := a.store.SetSheets(map[string]bool{
		store.PullRebase:          dlg.rebase,
		store.PullStashAndReapply: dlg.stash,
	}); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
	remote := dlg.remote
	branch := dlg.branch
	expectBranch := dlg.into
	expectHead := dlg.expectHead
	rebase := dlg.rebase
	stash := dlg.stash
	if remote == "" || branch == "" || expectBranch == "" {
		return
	}
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	a.session.ClearActionErr()
	st.syncWorkerStarted()
	if !a.session.doThen("Pulling", func(*git.Repo) error {
		defer st.syncWorkerDone()
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
		st.syncWorkerDone()
	}
}
