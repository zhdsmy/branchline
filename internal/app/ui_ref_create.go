package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// interactiveRebaseRefDisabled reports whether the Interactive Rebase entry
// of a sidebar reference row must be disabled: rewriting the commits after
// the row's commit needs a writable branch on top of a HEAD it does not
// already equal, with no operation in progress and no other write in flight.
// The commit itself missing from the row — an empty hash — leaves nothing to
// rebase onto and disables the same way.
func interactiveRebaseRefDisabled(a *app, hash string) bool {
	snap := a.session.Snapshot()
	return a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == "" || hash == "" || hash == snap.Status.Head
}

// remoteBranchCreateMenuItems adds the interactive rebase and the create
// actions to the context menu of one remote-tracking branch of the sidebar,
// directly behind the plain rebase. The items name the branch as
// <remote>/<name>, exactly as the row does; the new branch and tag start at
// exactly the commit the branch points at, so creating needs nothing but a
// commit and stays enabled wherever one exists.
func (a *app) remoteBranchCreateMenuItems(m *ui.Menu, rb git.Branch) {
	rebaseDisabled := interactiveRebaseRefDisabled(a, rb.Hash)
	if m.Item("Interactively Rebase on '" + rb.Name + "'…").Disabled(rebaseDisabled).Chosen() {
		a.openInteractiveRebaseModal(git.Commit{Hash: rb.Hash, Subject: rb.Name})
	}
	m.Separator()
	createDisabled := a.session.Busy() || rb.Hash == ""
	if m.Item("New Branch…").Disabled(createDisabled).Chosen() {
		a.openBranchCreateDialog(rb.FullName, rb.Name, rb.Hash)
	}
	if m.Item("New Tag…").Disabled(createDisabled).Chosen() {
		a.openNewTagDialog(rb.Hash)
		a.tagsUI().newTargetHint = rb.Name + " (" + shortTagHash(rb.Hash) + ")"
	}
}

// tagCreateMenuItems adds the interactive rebase and the create actions to
// the context menu of one tag of the sidebar. The tag's hash is already the
// commit the tag names — the create dialog and the rebase never see the tag
// object of an annotated tag — and the create guard re-reads the reference
// peeled to its commit, so a tag moved behind the dialog's back never tags
// or branches from a commit the user never confirmed.
func (a *app) tagCreateMenuItems(m *ui.Menu, tag git.Tag) {
	rebaseDisabled := interactiveRebaseRefDisabled(a, tag.Hash)
	if m.Item("Interactively Rebase on '" + tag.Name + "'…").Disabled(rebaseDisabled).Chosen() {
		a.openInteractiveRebaseModal(git.Commit{Hash: tag.Hash, Subject: tag.Name})
	}
	m.Separator()
	createDisabled := a.session.Busy() || tag.Hash == ""
	if m.Item("New Branch…").Disabled(createDisabled).Chosen() {
		a.openBranchCreateDialog("refs/tags/"+tag.Name, tag.Name, tag.Hash)
	}
	if m.Item("New Tag…").Disabled(createDisabled).Chosen() {
		a.openNewTagDialog(tag.Hash)
		a.tagsUI().newTargetHint = tag.Name + " (" + shortTagHash(tag.Hash) + ")"
	}
}
