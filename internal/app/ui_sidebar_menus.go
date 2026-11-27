package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// branchesSectionContextMenu is the context menu of the Branches section
// header: creating a branch is possible with the section collapsed, as it
// does not depend on any row the collapse hides. The sort block behind it
// only changes the display, so its items stay enabled while a write is in
// flight.
func (a *app) branchesSectionContextMenu(m *ui.Menu) {
	disabled := !a.session.HasRepo() || a.session.Busy()
	if m.Item("New Branch…").Disabled(disabled).Chosen() {
		a.openNewBranchDialog()
	}
	a.branchesSortMenuItems(m)
}

// openNewBranchDialog opens the New Branch dialog with the same fresh draft
// clicking the New Branch sidebar row starts from.
func (a *app) openNewBranchDialog() {
	a.refsView.newOpen = true
	a.refsView.newName = ""
	a.refsView.newCheckout = true
	a.refsView.newNameChanged = false
}

// remotesSectionContextMenu is the context menu of the Remotes section
// header: adding a remote is possible with the section collapsed, as it does
// not depend on any row the collapse hides.
func (a *app) remotesSectionContextMenu(m *ui.Menu) {
	disabled := !a.session.HasRepo() || a.session.Busy()
	if m.Item("Add New Remote…").Disabled(disabled).Chosen() {
		a.openAddRemoteDialog()
	}
	a.remotesSortMenuItems(m)
}

// tagsSectionContextMenu is the context menu of the Tags section header:
// creating a tag is possible with the section collapsed, as it does not
// depend on any row the collapse hides. The tag starts from the default
// target — the commit selected on the History page, HEAD otherwise — exactly
// as clicking the New Tag sidebar entry does.
func (a *app) tagsSectionContextMenu(m *ui.Menu) {
	disabled := !a.session.HasRepo() || a.session.Busy()
	if m.Item("New Tag…").Disabled(disabled).Chosen() {
		a.openNewTagDialog("")
	}
	a.tagsSortMenuItems(m)
}

// remoteFetchMenuItem is the first entry of a remote row's context menu:
// fetching only that remote, downloading its new branches and pruning the
// ones deleted there, never the other remotes the repository knows. The
// separator behind it is emitted here as well, so the menu's head stays one
// call in the row.
func (a *app) remoteFetchMenuItem(m *ui.Menu, remote string) {
	if m.Item("Fetch '" + remote + "'").Disabled(a.session.Busy()).Chosen() {
		a.fetchRemote(remote)
	}
	m.Separator()
}

// fetchRemote fetches one remote through the sync worker and its askpass
// dialog, as the toolbar Fetch does for every remote. The remote's name, its
// verified URL and the Fetch Tags preference are captured on the UI thread,
// and the worker re-verifies the remote against the repository before
// fetching: one that was removed since the click, or whose URL no longer
// matches what the user chose, refuses instead of fetching. The fetch is
// strictly scoped to the captured remote — never the whole repository — and
// honors the captured tags preference.
func (a *app) fetchRemote(remote string) {
	if a.session.Busy() {
		return
	}
	expectedRemote := remote
	expectedURL := ""
	if urls := a.session.Snapshot().RemoteURLs; urls != nil {
		expectedURL = urls[remote]
	}
	fetchTags := a.store.Settings().FetchTags
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	st.syncWorkerStarted()
	if !a.session.doThen("Fetching", func(*git.Repo) error {
		defer st.syncWorkerDone()
		liveURL, err := repo.RemoteURL(expectedRemote)
		if err != nil {
			return redactRemoteErr(fmt.Errorf("cannot fetch remote %s: reading its URL failed, so nothing was fetched: %w", expectedRemote, err))
		}
		if expectedURL != "" && liveURL != expectedURL {
			return fmt.Errorf("cannot fetch remote %s: its URL changed after the menu item was chosen (was %s), so nothing was fetched; review the repository and try again", expectedRemote, git.RedactText(expectedURL))
		}
		return redactRemoteErr(repo.FetchOpts(git.FetchOptions{Remote: expectedRemote, Prune: true, Tags: fetchTags}))
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
	}) {
		st.syncWorkerDone()
	}
}
