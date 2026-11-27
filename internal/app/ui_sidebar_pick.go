package app

// Sidebar multi-selection of references. The selection model keys every
// pickable row by its reference kind and name, so remote branches and tags
// can join the same model later without re-keying local branches. Only local
// branches are wired so far: clicking a branch row selects it alone,
// Cmd-click adds the row or takes it out, Shift-click selects the range from
// the anchor row to the clicked one in the order the sidebar shows the
// branches, and a right click on a row outside the selection narrows the
// selection to that row before its menu opens.

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// sidebarRefKind is the class of a pickable sidebar reference row.
type sidebarRefKind int

const (
	// sidebarRefLocalBranch is a local branch row of the Branches section.
	sidebarRefLocalBranch sidebarRefKind = iota
)

// sidebarPickKey identifies one pickable sidebar row within the selection:
// its reference kind and its short name. The zero value never identifies a
// row because reference names are never empty.
type sidebarPickKey struct {
	kind sidebarRefKind
	name string
}

// localBranchPickKey is the selection key of one local branch row.
func localBranchPickKey(name string) sidebarPickKey {
	return sidebarPickKey{kind: sidebarRefLocalBranch, name: name}
}

// clickSidebarBranch applies one click on a local branch row to the
// selection: a plain click selects only this row and makes it the anchor;
// Cmd-click toggles this row and becomes the anchor, seeding the anchor into
// an empty selection first; Shift-click selects the range from the anchor
// row to this one along ordered, the branches in the order the sidebar shows
// them. A Shift-click whose anchor is no longer in the shown list — a branch
// a refresh dropped, or no click ever landed — acts as a plain click.
//
// The range is computed from the cached sorted slice the frame already
// holds: the click handler never builds a list of its own, and painting
// builds nothing at all.
func (a *app) clickSidebarBranch(name string, mods ui.Modifiers, ordered []git.Branch) {
	st := &a.refsView
	key := localBranchPickKey(name)
	if st.pick == nil {
		st.pick = map[sidebarPickKey]bool{}
	}
	switch {
	case mods&ui.Shift != 0 && st.pickAnchor.name != "":
		from, to := -1, -1
		for i, br := range ordered {
			if br.Name == st.pickAnchor.name {
				from = i
			}
			if br.Name == name {
				to = i
			}
		}
		if from < 0 || to < 0 {
			st.pick = map[sidebarPickKey]bool{key: true}
			st.pickAnchor = key
			return
		}
		if from > to {
			from, to = to, from
		}
		st.pick = map[sidebarPickKey]bool{}
		for _, br := range ordered[from : to+1] {
			st.pick[localBranchPickKey(br.Name)] = true
		}
	case mods&ui.Cmd != 0:
		if len(st.pick) == 0 && st.pickAnchor.name != "" && st.pickAnchor != key {
			// Nothing is selected — every row was toggled back out — so the
			// anchor the range clicks still start from joins first, as it
			// would have been had the rows never left the selection.
			st.pick[st.pickAnchor] = true
		}
		if st.pick[key] {
			delete(st.pick, key)
		} else {
			st.pick[key] = true
		}
		st.pickAnchor = key
	default:
		st.pick = map[sidebarPickKey]bool{key: true}
		st.pickAnchor = key
	}
}

// selectOnlySidebarBranch narrows the selection to the one branch a right
// click landed on outside the selection: the menu then offered is the
// branch's own, exactly as if the row had been clicked first.
func (a *app) selectOnlySidebarBranch(name string) {
	key := localBranchPickKey(name)
	a.refsView.pick = map[sidebarPickKey]bool{key: true}
	a.refsView.pickAnchor = key
}

// sidebarPickCount is how many keys of the selection still name branches of
// the shown list: rows a refresh dropped are filtered out wherever the
// selection is read, and the multi-select look and menu only apply to the
// survivors.
func sidebarPickCount(pick map[sidebarPickKey]bool, ordered []git.Branch) int {
	if len(pick) == 0 {
		return 0
	}
	n := 0
	for _, br := range ordered {
		if pick[localBranchPickKey(br.Name)] {
			n++
		}
	}
	return n
}

// pickedLocalBranches returns the selected branches still present in
// ordered, in the order the sidebar shows them: the working set of the batch
// menu and of Copy Branch Names. The snapshot drives both, so a branch the
// last refresh dropped can neither be fast-forwarded, deleted, nor copied.
func pickedLocalBranches(pick map[sidebarPickKey]bool, ordered []git.Branch) []git.Branch {
	var out []git.Branch
	for _, br := range ordered {
		if pick[localBranchPickKey(br.Name)] {
			out = append(out, br)
		}
	}
	return out
}
