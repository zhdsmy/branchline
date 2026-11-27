package app

// Sidebar multi-selection of references. The selection model keys every
// pickable row by its reference kind and name: local branches by their short
// name, remote branches by their full reference name so same-named branches
// of different remotes — or a branch layout whose slashes cut both ways —
// never collide, and tags by their name. The selection holds rows of one
// kind at a time: clicking a row of another kind, with or without modifier
// keys, acts as a plain click and the selection restarts from that row.
// Clicking a row selects it alone, Cmd-click adds the row or takes it out,
// Shift-click selects the range from the anchor row to the clicked one in
// the order the sidebar shows the rows — remote branches may span remotes —
// and a right click on a row outside the selection narrows the selection to
// that row before its menu opens.

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// sidebarRefKind is the class of a pickable sidebar reference row.
type sidebarRefKind int

const (
	// sidebarRefLocalBranch is a local branch row of the Branches section.
	sidebarRefLocalBranch sidebarRefKind = iota
	// sidebarRefRemoteBranch is a tracking branch row of the Remotes section.
	sidebarRefRemoteBranch
	// sidebarRefTag is a tag row of the Tags section.
	sidebarRefTag
)

// sidebarPickKey identifies one pickable sidebar row within the selection:
// its reference kind and its name — the short name for local branches and
// tags, the full reference name for remote branches. The zero value never
// identifies a row because reference names are never empty.
type sidebarPickKey struct {
	kind sidebarRefKind
	name string
}

// localBranchPickKey is the selection key of one local branch row.
func localBranchPickKey(name string) sidebarPickKey {
	return sidebarPickKey{kind: sidebarRefLocalBranch, name: name}
}

// remoteBranchPickKey is the selection key of one remote-tracking branch
// row, keyed by the full reference name so the same short name under another
// remote — or under a remote whose own name holds slashes — is a different
// row.
func remoteBranchPickKey(fullName string) sidebarPickKey {
	return sidebarPickKey{kind: sidebarRefRemoteBranch, name: fullName}
}

// tagPickKey is the selection key of one tag row.
func tagPickKey(name string) sidebarPickKey {
	return sidebarPickKey{kind: sidebarRefTag, name: name}
}

// sidebarPickKindOf is the kind the selection currently belongs to: the kind
// of its rows while it holds any, the anchor's kind while it is empty — the
// anchor still seeds Cmd-clicks, so it owns the kind too — and false when
// nothing was ever picked.
func sidebarPickKindOf(pick map[sidebarPickKey]bool, anchor sidebarPickKey) (sidebarRefKind, bool) {
	for key := range pick {
		return key.kind, true
	}
	if anchor.name != "" {
		return anchor.kind, true
	}
	return 0, false
}

// clickSidebarRef applies one click on a reference row to the selection. A
// click on a row of a different kind than the selection belongs to acts as a
// plain click whatever modifiers it carried: the selection holds rows of one
// kind at a time, so it restarts from this row. Otherwise a plain click
// selects only this row and makes it the anchor; Cmd-click toggles this row
// and becomes the anchor, seeding the anchor into an empty selection first;
// Shift-click selects the range from the anchor row to this one along the
// keys in the order the sidebar shows the rows. A Shift-click whose anchor
// is no longer in the shown list — a row a refresh dropped, or no click ever
// landed — acts as a plain click.
//
// orderedKeys builds the rows' keys in display order; it runs only for a
// Shift-click with a live anchor, so painting never builds the list, and a
// plain or Cmd-click never pays for it either. Every path changes the
// selection or the anchor, so the pick version steps once per click,
// invalidating the sidebar's cached row list and pick counts.
func (a *app) clickSidebarRef(key sidebarPickKey, mods ui.Modifiers, orderedKeys func() []sidebarPickKey) {
	st := &a.refsView
	defer func() { st.pickVersion++ }()
	if st.pick == nil {
		st.pick = map[sidebarPickKey]bool{}
	}
	if kind, ok := sidebarPickKindOf(st.pick, st.pickAnchor); ok && kind != key.kind {
		// Rows of one kind at a time: the click restarts the selection here,
		// exactly as a plain click.
		mods = 0
	}
	switch {
	case mods&ui.Shift != 0 && st.pickAnchor.name != "":
		keys := orderedKeys()
		from, to := -1, -1
		for i, k := range keys {
			if k == st.pickAnchor {
				from = i
			}
			if k == key {
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
		for _, k := range keys[from : to+1] {
			st.pick[k] = true
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

// clickSidebarBranch applies one click on a local branch row: the range keys
// of a Shift-click follow ordered, the branches in the order the sidebar
// shows them.
func (a *app) clickSidebarBranch(name string, mods ui.Modifiers, ordered []git.Branch) {
	a.clickSidebarRef(localBranchPickKey(name), mods, func() []sidebarPickKey {
		keys := make([]sidebarPickKey, 0, len(ordered))
		for _, br := range ordered {
			keys = append(keys, localBranchPickKey(br.Name))
		}
		return keys
	})
}

// clickSidebarRemoteBranch applies one click on a remote-tracking branch
// row: the range keys of a Shift-click follow the order the sidebar shows
// the rows — remote by remote as refs lists them, each remote's branches in
// the order the settings ask for — so a range may span remotes.
func (a *app) clickSidebarRemoteBranch(rb git.Branch, mods ui.Modifiers, refs git.Refs) {
	a.clickSidebarRef(remoteBranchPickKey(rb.FullName), mods, func() []sidebarPickKey {
		return a.remoteBranchDisplayKeys(refs)
	})
}

// remoteBranchDisplayKeys is the key list of the remote branch rows in the
// order the sidebar shows them: the remotes in the order the snapshot lists
// them, each remote's branches in the sorted order. It runs from click and
// menu handling only — never from painting — because building the list
// allocates. The sorted branch copy itself is the frame's cached one.
func (a *app) remoteBranchDisplayKeys(refs git.Refs) []sidebarPickKey {
	sorted := a.sortedRemoteBranches(refs.Remote)
	keys := make([]sidebarPickKey, 0, len(sorted))
	for _, rem := range refs.Remotes {
		for _, rb := range sorted {
			if rb.Remote == rem {
				keys = append(keys, remoteBranchPickKey(rb.FullName))
			}
		}
	}
	return keys
}

// clickSidebarTag applies one click on a tag row: the range keys of a
// Shift-click follow sorted, the tags in the order the sidebar shows them.
func (a *app) clickSidebarTag(name string, mods ui.Modifiers, sorted []git.Tag) {
	a.clickSidebarRef(tagPickKey(name), mods, func() []sidebarPickKey {
		keys := make([]sidebarPickKey, 0, len(sorted))
		for _, tag := range sorted {
			keys = append(keys, tagPickKey(tag.Name))
		}
		return keys
	})
}

// clearSidebarPick empties the selection, keeping the anchor where it is: a
// range click after the deselection still starts from the row the selection
// last centered on, exactly as it does after rows are toggled out one by
// one.
func (a *app) clearSidebarPick() {
	a.refsView.pick = map[sidebarPickKey]bool{}
	a.refsView.pickVersion++
}

// selectOnlySidebarBranch narrows the selection to the one branch a right
// click landed on outside the selection: the menu then offered is the
// branch's own, exactly as if the row had been clicked first.
func (a *app) selectOnlySidebarBranch(name string) {
	a.selectOnlySidebarRef(localBranchPickKey(name))
}

// selectOnlySidebarRef narrows the selection to the one row a right click
// landed on outside the selection: the menu then offered is the row's own,
// exactly as if the row had been clicked first.
func (a *app) selectOnlySidebarRef(key sidebarPickKey) {
	a.refsView.pick = map[sidebarPickKey]bool{key: true}
	a.refsView.pickAnchor = key
	a.refsView.pickVersion++
}

// sidebarPickCount is how many keys of the selection still name branches of
// the shown list: rows a refresh dropped are filtered out wherever the
// selection is read, and the multi-select look and menu only apply to the
// survivors. Counting never allocates, so it may run on every painted frame.
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

// sidebarRemotePickCount is sidebarPickCount for the remote-tracking
// branches of sorted: how many selected keys still name a row the sidebar
// shows.
func sidebarRemotePickCount(pick map[sidebarPickKey]bool, sorted []git.Branch) int {
	if len(pick) == 0 {
		return 0
	}
	n := 0
	for _, rb := range sorted {
		if pick[remoteBranchPickKey(rb.FullName)] {
			n++
		}
	}
	return n
}

// sidebarTagPickCount is sidebarPickCount for the tags of sorted: how many
// selected keys still name a row the sidebar shows.
func sidebarTagPickCount(pick map[sidebarPickKey]bool, sorted []git.Tag) int {
	if len(pick) == 0 {
		return 0
	}
	n := 0
	for _, tag := range sorted {
		if pick[tagPickKey(tag.Name)] {
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

// pickedRemoteBranches returns the selected remote-tracking branches still
// present in the snapshot, in the order the sidebar shows them: the remotes
// in the snapshot's order, each remote's branches sorted as the settings ask
// — the working set of the batch menu and of Copy Branch Names. Rows a
// refresh dropped can neither be deleted nor copied. It runs from click and
// menu handling only, never from painting.
func pickedRemoteBranches(pick map[sidebarPickKey]bool, refs git.Refs, sorted []git.Branch) []git.Branch {
	var out []git.Branch
	for _, rem := range refs.Remotes {
		for _, rb := range sorted {
			if rb.Remote == rem && pick[remoteBranchPickKey(rb.FullName)] {
				out = append(out, rb)
			}
		}
	}
	return out
}

// pickedTags returns the selected tags still present in sorted, in the order
// the sidebar shows them: the working set of the batch menu and of Copy Tag
// Names, so a tag the last refresh dropped can neither be pushed, deleted,
// nor copied.
func pickedTags(pick map[sidebarPickKey]bool, sorted []git.Tag) []git.Tag {
	var out []git.Tag
	for _, tag := range sorted {
		if pick[tagPickKey(tag.Name)] {
			out = append(out, tag)
		}
	}
	return out
}
