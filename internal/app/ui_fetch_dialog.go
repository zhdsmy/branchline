package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// fetchDialogState is the Fetch dialog draft inside the app's sync state:
// the remote list captured from the snapshot when the dialog opened, the
// chosen remote, and the three check boxes. It lives in the sync state the
// generation check already resets on a repository switch or close, so
// nothing captured for one repository carries over to the next.
type fetchDialogState struct {
	open    bool
	remote  string
	remotes []string
	all     bool
	prune   bool
	tags    bool
}

// errMsgFetchNoRemote is the pre-flight validation error of a Fetch without
// any remote, the fetch spelling of the push one.
const errMsgFetchNoRemote = "The repository has no remote to fetch from."

// fetchDialogRemoteDefault derives the dialog's initial remote: the remote
// of the current branch's upstream when there is one, else the repository's
// default remote — origin when there is one, otherwise the first remote.
func fetchDialogRemoteDefault(cur *git.Branch, remotes []string) string {
	if cur != nil && cur.Upstream != "" {
		if remote, _, ok := splitBranchUpstream(cur.Upstream, remotes); ok {
			return remote
		}
	}
	return defaultRemoteOf(git.Refs{Remotes: remotes})
}

// openFetchDialog opens the toolbar Fetch dialog. A busy session or a
// missing repository leaves every state untouched, and a snapshot that
// never loaded defers to the quick fetch's live probe instead of opening an
// empty dialog, exactly as the quick fetch handles it. A repository without
// remotes is a pre-flight error in the dismissible toolbar strip rather
// than a dialog. Everything the dialog needs later is captured here, on the
// UI thread: the remote list, the default remote, and the remembered check
// boxes.
func (a *app) openFetchDialog() {
	if a.session.Busy() || !a.session.HasRepo() {
		return
	}
	if !a.session.Loaded() || a.session.LoadErr() != nil {
		// The snapshot never loaded — an SSH repository whose encrypted key
		// had no askpass yet, or a repository that broke after opening: the
		// quick fetch probes the real state through the askpass channel and
		// reports what it finds in the toolbar.
		a.syncFetch()
		return
	}
	snap := a.session.Snapshot()
	st := syncStateFor(a)
	st.syncErr = ""
	a.session.ClearActionErr()
	if len(snap.Refs.Remotes) == 0 {
		st.syncErr = errMsgFetchNoRemote
		return
	}
	dlg := &st.fetchDlg
	*dlg = fetchDialogState{}
	dlg.open = true
	dlg.remotes = slices.Clone(snap.Refs.Remotes)
	dlg.remote = fetchDialogRemoteDefault(currentBranchOf(snap.Refs), dlg.remotes)
	dlg.all = a.store.Sheet(store.FetchAllRemotes, false)
	dlg.prune = a.store.Sheet(store.FetchPrune, true)
	dlg.tags = a.store.Sheet(store.FetchAllTags, a.store.Settings().FetchTags)
}

// closeFetchDialog closes the Fetch dialog and clears its draft. Canceling,
// dismissing, submitting and switching repositories all end here so no
// captured remote or choice survives the dialog.
func (a *app) closeFetchDialog() {
	syncStateFor(a).fetchDlg = fetchDialogState{}
}

// fetchDialog is the modal of the toolbar Fetch: the remote to fetch from
// and the three fetch options. Enter submits like the confirm button —
// except while the repository is busy, when the button is disabled — and
// Cancel, Escape, the backdrop and a repository switch close without
// fetching or remembering anything.
func (a *app) fetchDialog(c *ui.Context) {
	p := a.pal
	st := &syncStateFor(a).fetchDlg
	if !st.open {
		return
	}
	if ui.Modal(c.Key("fetch-dialog-modal"), &st.open, func() {
		ui.Column(c.Key("fetch-dialog-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Fetch").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, "Fetch the latest changes from a remote.").FontSize(12).TextColor(p.muted).Wrap()
			ui.Field(c, "Remote:", func() {
				ui.Select(c, &st.remote, st.remotes).FillWidth().Label("Fetch Remote").Disabled(st.all)
			})
			ui.Checkbox(c.Key("fetch-dialog-all"), &st.all, "Fetch all remotes").Label("Fetch all remotes").Tooltip("--all")
			ui.Checkbox(c.Key("fetch-dialog-prune"), &st.prune, "Prune remote-tracking branches").Label("Prune remote-tracking branches").Tooltip("--prune")
			ui.Checkbox(c.Key("fetch-dialog-tags"), &st.tags, "Fetch tags").Label("Fetch tags").Tooltip("--tags")
			ui.Row(c.Key("fetch-dialog-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("fetch-dialog-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeFetchDialog()
				}
				if ui.PrimaryButton(c.Key("fetch-dialog-confirm"), "Fetch").
					Label("Confirm Fetch").
					Disabled(a.session.Busy()).
					AutoFocus().
					Clicked() {
					a.submitFetchDialog()
				}
			})
			// Enter is the confirm button, as in every dialog: a focused
			// control that takes the key — an open remote choice, a focused
			// button — consumes it first, so this fires only for the Enter
			// nothing else claimed.
			if c.Shortcut(0, ui.KeyEnter) && !a.session.Busy() {
				a.submitFetchDialog()
			}
		})
	}).Dismissed() {
		a.closeFetchDialog()
	}
}

// submitFetchDialog captures the dialog's choices on the UI thread, closes
// the dialog first — an askpass modal of the fetch must never stack on it —
// remembers the three check boxes in the store, and fetches through the
// session's sync lifecycle: the askpass-enabled repository copy, the worker
// counting, and the dismissible error display. A failed save surfaces as a
// toast and never holds the fetch back.
func (a *app) submitFetchDialog() {
	st := syncStateFor(a)
	dlg := st.fetchDlg
	if !dlg.open || a.session.Busy() {
		return
	}
	a.closeFetchDialog()
	if err := a.store.SetSheets(map[string]bool{
		store.FetchAllRemotes: dlg.all,
		store.FetchPrune:      dlg.prune,
		store.FetchAllTags:    dlg.tags,
	}); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
	opts := git.FetchOptions{All: dlg.all, Prune: dlg.prune, Tags: dlg.tags}
	if !dlg.all {
		opts.Remote = dlg.remote
	}
	a.syncFetchOpts(opts)
}
