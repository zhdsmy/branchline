package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// tagMergeRebaseMenuItems adds the Merge and Rebase items to the context menu
// of one tag of the sidebar, between the push and the delete items.
//
// Both dialogs receive the bare tag name as the revision and the commit the
// tag names as the pinned object id. git.Tag.Hash is the peeled commit — the
// for-each-ref parse replaces an annotated tag's object id with the target
// commit — and the guards of the merge and the rebase resolve the revision
// with an explicit "^{commit}" peel before comparing, so a bare tag name and
// the peeled commit hash describe exactly the same object for lightweight
// and annotated tags alike. A bare name is also what Git itself names in the
// merge commit subject ("Merge tag '<name>'"), and it cannot be shadowed by
// a branch: refs of the two kinds live in disjoint namespaces, and the name
// check of the guards compares against the checked-out branch only.
func (a *app) tagMergeRebaseMenuItems(m *ui.Menu, tag git.Tag) {
	snap := a.session.Snapshot()
	m.Separator()
	// Merging the commit HEAD already is changes nothing, so the Merge item
	// disables on the tag naming it, exactly as the commit entry does —
	// besides the shared write, operation and unborn-repository conditions.
	mergeLabel := "Merge into " + mergeIntoLabel(snap.Status.Branch) + "…"
	mergeDisabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || tag.Hash == snap.Status.Head
	if m.Item(mergeLabel).Disabled(mergeDisabled).Chosen() {
		a.openMergeDialog(tag.Name, tag.Name, tag.Hash)
	}
	// Rebasing onto the commit HEAD already is can never apply, and a rebase
	// needs a branch to move, so a detached HEAD disables the item the same
	// way the branch and commit entries do.
	current := rebaseCurrentLabel(snap.Status.Branch)
	rebaseLabel := "Rebase on '" + tag.Name + "'…"
	if current != "" {
		rebaseLabel = "Rebase " + current + " on '" + tag.Name + "'…"
	}
	rebaseDisabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == "" || tag.Hash == snap.Status.Head
	if m.Item(rebaseLabel).Disabled(rebaseDisabled).Chosen() {
		a.openRebaseModal(tag.Name, tag.Name, tag.Hash)
	}
}
