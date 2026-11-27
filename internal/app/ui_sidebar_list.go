package app

// The sidebar's virtual row list. Everything the sidebar scroll area shows
// — the navigation rows, the section headers, the entry rows, and the
// reference and stash rows of the Branches, Remotes, Tags and Stashes
// sections — is one flat table of rows, drawn by a ui.List that builds only
// the rows in view. The table, the grouping of the remote-tracking branches
// by their remote, and the multi-select counts are derived state: they are
// rebuilt only when one of the inputs they are read from changes, so a
// frame of an unchanged repository walks no reference at all.
//
// The inputs of a rebuild are: the sorted reference slices (by backing
// identity, the way the sort caches key themselves), the snapshot's remotes
// and stash slices, the collapse flags of the sections, whether a
// repository is open, the selection's version and length, and the selected
// stash row. The dialogs of the sidebar never enter the list: refsSidebar
// and stashSidebar mount them outside it, as they always did.

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// sidebarRowKind is the class of one row of the sidebar's virtual list.
type sidebarRowKind int

const (
	sidebarRowNav sidebarRowKind = iota
	sidebarRowSpacer
	sidebarRowSection
	sidebarRowNewBranch
	sidebarRowNewRemote
	sidebarRowRemote
	sidebarRowBranch
	sidebarRowRemoteBranch
	sidebarRowNewTag
	sidebarRowPushAllTags
	sidebarRowTag
	sidebarRowTagActions
	sidebarRowSaveStash
	sidebarRowStash
	sidebarRowStashActions
	sidebarRowPlaceholder
)

// sidebarRowEntry is one row of the sidebar's virtual list in display
// order: its key — the list's row identity, stable across the rebuilds rows
// above it shift — and the data its kind draws from. Rows of one kind read
// only the fields of their kind.
type sidebarRowEntry struct {
	kind sidebarRowKind
	key  string
	// accessName is the row's accessible name, what the row inside the
	// list's row element is labeled: the list's Label reports it for the
	// row element itself, so assistive technology meets the row once, with
	// the name it always had.
	accessName string

	// Navigation rows.
	label    string
	sfName   string
	fallback *ui.SVG
	target   route

	// Section headers: title and count as shown, collapsed the flag the
	// header toggles, menu the header's context menu — nil for Stashes,
	// which has none.
	title     string
	count     int
	collapsed *bool
	menu      func(*ui.Menu)

	// Reference and stash rows.
	branch git.Branch
	remote string
	tag    git.Tag
	stash  git.Stash

	// Placeholder rows.
	text string
}

// sidebarRowCache is the cached row table of the sidebar's virtual list:
// the inputs the table was built from, so an unchanged frame answers with
// the rows as they are, and the derived state the rows read — the rows in
// display order, the sorted reference slices they were built from, and the
// multi-select counts of the three reference kinds. mirror is the
// selection the list reports for its rows: the same set of rows the row
// builders mark selected, keyed by the rows' keys, so that the row element
// the list wraps every row in reads selected exactly when the row inside
// it does.
type sidebarRowCache struct {
	valid        bool
	localSorted  []git.Branch
	remoteSorted []git.Branch
	tagSorted    []git.Tag
	remotes      []string
	stashes      []git.Stash
	branchesColl bool
	remotesColl  bool
	tagsColl     bool
	stashesColl  bool
	hasRepo      bool
	pickVersion  uint64
	pickLen      int
	stashSelKey  string
	routePage    pageKind
	rows         []sidebarRowEntry
	mirror       ui.Selection[any]
	localPicks   int
	remotePicks  int
	tagPicks     int
}

// sameSidebarInputs reports whether the cache was built from exactly these
// inputs: the sorted reference slices by backing identity — a hit of the
// sort caches returns the very slice the rebuild kept — the snapshot's
// remotes and stashes, the collapse flags, the repository's presence, the
// selection, and the selected stash row.
func (rc *sidebarRowCache) sameSidebarInputs(
	localSorted, remoteSorted []git.Branch, tagSorted []git.Tag,
	remotes []string, stashes []git.Stash,
	branchesColl, remotesColl, tagsColl, stashesColl, hasRepo bool,
	pickVersion uint64, pickLen int, stashSelKey string, routePage pageKind,
) bool {
	return rc.valid &&
		sameBackingSlice(rc.localSorted, localSorted) &&
		sameBackingSlice(rc.remoteSorted, remoteSorted) &&
		sameBackingSlice(rc.tagSorted, tagSorted) &&
		sameBackingSlice(rc.remotes, remotes) &&
		sameBackingSlice(rc.stashes, stashes) &&
		rc.branchesColl == branchesColl &&
		rc.remotesColl == remotesColl &&
		rc.tagsColl == tagsColl &&
		rc.stashesColl == stashesColl &&
		rc.hasRepo == hasRepo &&
		rc.pickVersion == pickVersion &&
		rc.pickLen == pickLen &&
		rc.stashSelKey == stashSelKey &&
		rc.routePage == routePage
}

// sidebarRows is the sidebar's row table, rebuilt when any of its inputs
// changed since the last rebuild and returned cached otherwise. It runs on
// every frame before the list builds, so the generation resets and the
// stash selection pruning happen even while every section is collapsed —
// the selection and dialog drafts of a switched-away repository never
// linger. A frame whose inputs are unchanged walks no reference at all:
// reading the sorted slices back out of the sort caches is their own
// one-entry lookup.
func (a *app) sidebarRows() []sidebarRowEntry {
	a.refsResetForRepoGen()
	stash := a.stashUI()
	a.stashResetForRepoGen()
	a.tagsUI()

	refs := a.session.Snapshot().Refs
	stashes := a.session.Snapshot().Stashes
	localSorted := a.sortedLocalBranches(refs.Branches)
	remoteSorted := a.sortedRemoteBranches(refs.Remote)
	tagSorted := a.sortedTags(refs.Tags)

	rc := &a.refsView.sidebarRows
	if rc.sameSidebarInputs(localSorted, remoteSorted, tagSorted, refs.Remotes, stashes,
		a.branchesCollapsed, a.remotesCollapsed, a.tagsCollapsed, stash.collapsed, a.session.HasRepo(),
		a.refsView.pickVersion, len(a.refsView.pick), stash.selectedKey, a.route.page) {
		return rc.rows
	}
	a.rebuildSidebarRows(rc, refs, stashes, localSorted, remoteSorted, tagSorted, stash)
	return rc.rows
}

// rebuildSidebarRows recomputes the cached row table from the current
// inputs: the multi-select counts, the remote-tracking branches grouped by
// their remote in one pass over the sorted list, and the rows themselves in
// the order the sidebar shows them. The stash selection prunes here: it is
// the only read of it that walks the stash list, and the rebuild runs
// exactly when the list or the selection changed. The row count is
// len(refs)+len(stashes)+a constant, so the rebuild is proportional to the
// data once, not per frame.
func (a *app) rebuildSidebarRows(rc *sidebarRowCache, refs git.Refs, stashes []git.Stash, localSorted, remoteSorted []git.Branch, tagSorted []git.Tag, stash *stashUIState) {
	pick := a.refsView.pick
	localPicks := sidebarPickCount(pick, localSorted)
	remotePicks := sidebarRemotePickCount(pick, remoteSorted)
	tagPicks := sidebarTagPickCount(pick, tagSorted)

	// The lone selected tag is the one row an action bar unfolds under;
	// with two or more tags selected — or none — no bar shows.
	expandedTag := ""
	if tagPicks == 1 {
		for _, tag := range tagSorted {
			if pick[tagPickKey(tag.Name)] {
				expandedTag = tag.Name
				break
			}
		}
	}

	if stash.selectedKey != "" && a.session.Loaded() {
		present := false
		for i := range stashes {
			if stashRowKey(stashes[i]) == stash.selectedKey {
				present = true
				break
			}
		}
		if !present {
			stash.selectedKey = ""
		}
	}

	// Group the remote-tracking branches by their remote in one pass over
	// the sorted list: the display walks the remotes in the snapshot's
	// order, each remote's branches in their sorted subsequence — never one
	// scan of the whole list per remote.
	byRemote := make(map[string][]git.Branch, len(refs.Remotes))
	for _, rb := range remoteSorted {
		byRemote[rb.Remote] = append(byRemote[rb.Remote], rb)
	}

	rows := make([]sidebarRowEntry, 0, len(localSorted)+len(remoteSorted)+len(tagSorted)+len(stashes)+24)
	rows = append(rows,
		sidebarRowEntry{kind: sidebarRowNav, key: "nav-Changes", accessName: "Changes", label: "Changes", sfName: "square.and.pencil", fallback: fallbackDiff, target: route{page: pageChanges}},
		sidebarRowEntry{kind: sidebarRowNav, key: "nav-History", accessName: "History", label: "History", sfName: "clock", fallback: fallbackHist, target: route{page: pageHistory}},
		sidebarRowEntry{kind: sidebarRowSpacer, key: "nav-gap"},
	)

	rows = append(rows, sidebarRowEntry{kind: sidebarRowSection, key: "sec-Branches", accessName: "Branches", title: "Branches", count: len(refs.Branches), collapsed: &a.branchesCollapsed, menu: a.branchesSectionContextMenu})
	if !a.branchesCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewBranch, key: "row-new-branch", accessName: "New Branch"})
		}
		if len(refs.Branches) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-branches", accessName: "No branches", text: "No branches"})
		} else {
			for _, br := range localSorted {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowBranch, key: "br-" + br.FullName, accessName: br.Name, branch: br})
			}
		}
	}

	rows = append(rows, sidebarRowEntry{kind: sidebarRowSection, key: "sec-Remotes", accessName: "Remotes", title: "Remotes", count: len(refs.Remotes), collapsed: &a.remotesCollapsed, menu: a.remotesSectionContextMenu})
	if !a.remotesCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewRemote, key: "row-new-remote", accessName: "New Remote"})
		}
		if len(refs.Remotes) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-remotes", accessName: "No remotes", text: "No remotes"})
		} else {
			for _, rem := range refs.Remotes {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowRemote, key: "rem-" + rem, accessName: rem, remote: rem})
				for _, rb := range byRemote[rem] {
					rows = append(rows, sidebarRowEntry{kind: sidebarRowRemoteBranch, key: "rem-br-" + rb.FullName, accessName: rb.Name, branch: rb})
				}
			}
		}
	}

	rows = append(rows, sidebarRowEntry{kind: sidebarRowSection, key: "sec-Tags", accessName: "Tags", title: "Tags", count: len(refs.Tags), collapsed: &a.tagsCollapsed, menu: a.tagsSectionContextMenu})
	if !a.tagsCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewTag, key: "row-new-tag", accessName: "New Tag"})
		}
		if a.session.HasRepo() && len(refs.Tags) > 0 && len(refs.Remotes) > 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPushAllTags, key: "row-push-all-tags", accessName: "Push All Tags"})
		}
		if len(refs.Tags) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-tags", accessName: "No tags", text: "No tags"})
		} else {
			for _, tag := range tagSorted {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowTag, key: "tag-" + tag.Name, accessName: tag.Name, tag: tag})
				if tag.Name == expandedTag {
					rows = append(rows, sidebarRowEntry{kind: sidebarRowTagActions, key: "tagrow-actions-" + tag.Name, tag: tag})
				}
			}
		}
	}

	rows = append(rows, sidebarRowEntry{kind: sidebarRowSection, key: "sec-Stashes", accessName: "Stashes", title: "Stashes", count: len(stashes), collapsed: &stash.collapsed})
	if !stash.collapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowSaveStash, key: "row-save-stash", accessName: "Save Stash"})
		}
		if len(stashes) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-stashes", accessName: "No stashes", text: "No stashes"})
		} else {
			for _, s := range stashes {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowStash, key: "stashrow-" + stashRowKey(s), accessName: "Stash " + s.Name, stash: s})
				if stash.selectedKey != "" && stashRowKey(s) == stash.selectedKey {
					rows = append(rows, sidebarRowEntry{kind: sidebarRowStashActions, key: "stashrow-actions-" + stashRowKey(s), stash: s})
				}
			}
		}
	}

	*rc = sidebarRowCache{
		valid:        true,
		localSorted:  localSorted,
		remoteSorted: remoteSorted,
		tagSorted:    tagSorted,
		remotes:      refs.Remotes,
		stashes:      stashes,
		branchesColl: a.branchesCollapsed,
		remotesColl:  a.remotesCollapsed,
		tagsColl:     a.tagsCollapsed,
		stashesColl:  stash.collapsed,
		hasRepo:      a.session.HasRepo(),
		pickVersion:  a.refsView.pickVersion,
		pickLen:      len(pick),
		stashSelKey:  stash.selectedKey,
		routePage:    a.route.page,
		rows:         rows,
		localPicks:   localPicks,
		remotePicks:  remotePicks,
		tagPicks:     tagPicks,
	}
	a.rebuildSidebarMirror(rc)
}

// rebuildSidebarMirror fills the cache's selection mirror with the keys of
// exactly the rows the row builders mark selected: a navigation row of the
// page showing, a branch row current or inside a multi-selection, a
// remote-tracking or tag row inside one, and the selected stash row. The
// list's row element reads its rows chosen from the mirror, so the node
// assistive technology meets first tells what the row inside it tells, and
// nothing else — the mirror is rebuilt from the selection's own state
// wherever the table is, never mutated by hand.
func (a *app) rebuildSidebarMirror(rc *sidebarRowCache) {
	pick := a.refsView.pick
	rc.mirror = ui.Selection[any]{}
	for _, entry := range rc.rows {
		chosen := false
		switch entry.kind {
		case sidebarRowNav:
			chosen = a.route.page == entry.target.page
		case sidebarRowBranch:
			chosen = entry.branch.Current || rc.localPicks >= 2 && pick[localBranchPickKey(entry.branch.Name)]
		case sidebarRowRemoteBranch:
			chosen = rc.remotePicks >= 2 && pick[remoteBranchPickKey(entry.branch.FullName)]
		case sidebarRowTag:
			chosen = pick[tagPickKey(entry.tag.Name)]
		case sidebarRowStash:
			chosen = rc.stashSelKey != "" && stashRowKey(entry.stash) == rc.stashSelKey
		}
		if chosen {
			rc.mirror.Add(entry.key)
		}
	}
}

// sidebarRowView draws one row of the sidebar's virtual list. The row
// builders are the ones the scroll area always used, unchanged in what they
// paint; the sorted slices and the pick counts they read come from the
// cache the row table was built with, so drawing walks nothing. rowTheme
// is the theme the rest of the window uses: the action bars' buttons build
// inside it again, since the list's own copy hides the fill and hover it
// would paint for its rows, and theirs has to stay visible.
func (a *app) sidebarRowView(c *ui.Context, entry sidebarRowEntry, rowTheme *ui.Theme) {
	rc := &a.refsView.sidebarRows
	switch entry.kind {
	case sidebarRowNav:
		a.navSidebarRow(c, entry)
	case sidebarRowSpacer:
		ui.Box(c).Height(6)
	case sidebarRowSection:
		hdr := sidebarSectionHeader(c, entry.title, entry.count, entry.collapsed, a.pal)
		if entry.menu != nil {
			hdr.ContextMenu(entry.menu)
		}
	case sidebarRowNewBranch:
		a.newBranchRow(c)
	case sidebarRowNewRemote:
		a.newRemoteRow(c)
	case sidebarRowRemote:
		a.remoteRow(c, entry.remote)
	case sidebarRowBranch:
		inflight := a.refsView.delOpen && a.refsView.delChecking
		a.branchRow(c, entry.branch, inflight, rc.localSorted, rc.localPicks)
	case sidebarRowRemoteBranch:
		a.remoteBranchRow(c, entry.branch, a.session.Snapshot().Refs, rc.remoteSorted, rc.remotePicks)
	case sidebarRowNewTag:
		a.tagsSidebarHeaderActions(c)
	case sidebarRowPushAllTags:
		a.pushAllTagsSidebarEntry(c)
	case sidebarRowTag:
		a.tagRow(c, entry.tag, rc.tagSorted, rc.tagPicks)
	case sidebarRowTagActions:
		ui.Themed(c, rowTheme).Children(func() {
			a.tagRowActions(c, entry.tag)
		})
	case sidebarRowSaveStash:
		a.saveStashRow(c)
	case sidebarRowStash:
		a.stashRow(c, entry.stash)
	case sidebarRowStashActions:
		ui.Themed(c, rowTheme).Children(func() {
			a.stashActionBar(c, entry.stash, a.session.Busy())
		})
	case sidebarRowPlaceholder:
		a.refPlaceholderRow(c, entry.text)
	}
}

// navSidebarRow is one navigation row of the sidebar, reading the entries
// the row table carries: exactly the Changes and History rows the sidebar
// always opened with.
func (a *app) navSidebarRow(c *ui.Context, entry sidebarRowEntry) {
	badge := ""
	if entry.target.page == pageChanges && a.session.HasRepo() && a.session.Loaded() {
		st := a.session.Snapshot().Status
		if n := len(st.Unstaged) + len(st.Staged); n > 0 {
			badge = fmt.Sprint(n)
		}
	}
	a.navRow(c, entry.label, entry.sfName, entry.fallback, entry.target, badge)
}
