package app

// The sidebar's virtual row list. Everything the sidebar scroll area shows
// — the navigation rows, the section headers, the entry rows, and the
// reference and stash rows of the Branches, Remotes, Tags and Stashes
// sections — is one flat table of rows, drawn by a ui.List that builds only
// the rows in view. The table, the folder grouping of the references, the
// grouping of the remote-tracking branches by their remote, and the
// multi-select counts are derived state: they are rebuilt only when one of
// the inputs they are read from changes, so a frame of an unchanged
// repository walks no reference at all.
//
// The inputs of a rebuild are: the sorted reference slices (by backing
// identity, the way the sort caches key themselves), the snapshot's remotes
// and stash slices, the collapse flags of the sections, the revision of the
// collapsed folder set, whether a repository is open, the selection's
// version and length, and the selected stash row. The dialogs of the
// sidebar never enter the list: refsSidebar and stashSidebar mount them
// outside it, as they always did.

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
	sidebarRowFolder
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

	// Navigation rows.
	label    string
	sfName   string
	fallback *ui.SVG
	target   route

	// Section headers: title and count as shown, collapsed the flag the
	// header toggles, menu the header's context menu — nil for Stashes,
	// which has none — and toggled the callback the header runs after the
	// flip, which remembers the choice in the repository's settings.
	title     string
	count     int
	collapsed *bool
	menu      func(*ui.Menu)
	toggled   func(collapsed bool)

	// Folder rows: the stable ID the collapse remembers, the full path the
	// row reads to assistive technology, the segment it shows, whether the
	// folder is open, and the number of references inside it. count is
	// shared with the section headers.
	folderID   string
	folderPath string
	folderSeg  string
	folderOpen bool

	// depth is the folder nesting of a folder or reference row — 0 at the
	// section's top level — and padLeft the row's left padding with that
	// nesting applied. padLeft is computed when the rows are built, where
	// the section's base indent is known.
	depth   int
	padLeft float32

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
// display order, the sorted reference slices they were built from, the
// visible references of every section in display order, and the
// multi-select counts of the three reference kinds.
type sidebarRowCache struct {
	valid         bool
	localSorted   []git.Branch
	remoteSorted  []git.Branch
	tagSorted     []git.Tag
	remotes       []string
	stashes       []git.Stash
	branchesColl  bool
	remotesColl   bool
	tagsColl      bool
	stashesColl   bool
	closedRev     uint64
	hasRepo       bool
	pickVersion   uint64
	pickLen       int
	stashSelKey   string
	rows          []sidebarRowEntry
	localVisible  []git.Branch
	remoteVisible []git.Branch
	tagVisible    []git.Tag
	localPicks    int
	remotePicks   int
	tagPicks      int
}

// sameSidebarInputs reports whether the cache was built from exactly these
// inputs: the sorted reference slices by backing identity — a hit of the
// sort caches returns the very slice the rebuild kept — the snapshot's
// remotes and stashes, the collapse flags of the sections, the revision of
// the collapsed folder set, the repository's presence, the selection, and
// the selected stash row.
func (rc *sidebarRowCache) sameSidebarInputs(
	localSorted, remoteSorted []git.Branch, tagSorted []git.Tag,
	remotes []string, stashes []git.Stash,
	branchesColl, remotesColl, tagsColl, stashesColl bool, closedRev uint64, hasRepo bool,
	pickVersion uint64, pickLen int, stashSelKey string,
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
		rc.closedRev == closedRev &&
		rc.hasRepo == hasRepo &&
		rc.pickVersion == pickVersion &&
		rc.pickLen == pickLen &&
		rc.stashSelKey == stashSelKey
}

// sidebarRows is the sidebar's row table, rebuilt when any of its inputs
// changed since the last rebuild and returned cached otherwise. It runs on
// every frame before the list builds, so the generation resets, the sidebar
// settings load of a freshly opened repository, and the stash selection
// pruning happen even while every section is collapsed — the selection and
// dialog drafts of a switched-away repository never linger. A frame whose
// inputs are unchanged walks no reference at all: reading the sorted slices
// back out of the sort caches is their own one-entry lookup.
func (a *app) sidebarRows() []sidebarRowEntry {
	a.refsResetForRepoGen()
	stash := a.stashUI()
	a.stashResetForRepoGen()
	a.tagsUI()
	a.loadSidebarRefSettings()

	refs := a.session.Snapshot().Refs
	stashes := a.session.Snapshot().Stashes
	localSorted := a.sortedLocalBranches(refs.Branches)
	remoteSorted := a.sortedRemoteBranches(refs.Remote)
	tagSorted := a.sortedTags(refs.Tags)

	rc := &a.refsView.sidebarRows
	if rc.sameSidebarInputs(localSorted, remoteSorted, tagSorted, refs.Remotes, stashes,
		a.branchesCollapsed, a.remotesCollapsed, a.tagsCollapsed, stash.collapsed,
		a.refsView.sidebarClosedRev, a.session.HasRepo(),
		a.refsView.pickVersion, len(a.refsView.pick), stash.selectedKey) {
		return rc.rows
	}
	a.rebuildSidebarRows(rc, refs, stashes, localSorted, remoteSorted, tagSorted, stash)
	return rc.rows
}

// sidebarBranchFolderRow builds the folder row entry of one folder item:
// the stable ID the collapse remembers, the full path the row reads to
// assistive technology — the remote included for a remote's folders — the
// segment it shows, and the left padding of its nesting on the section's
// base indent.
func sidebarBranchFolderRow(item sidebarFolderItem[git.Branch], prefix, labelPrefix string, closed map[string]bool, basePad float32) sidebarRowEntry {
	id := prefix + item.path
	return sidebarRowEntry{
		kind:       sidebarRowFolder,
		key:        "fold-" + id,
		folderID:   id,
		folderPath: labelPrefix + item.path,
		folderSeg:  item.seg,
		folderOpen: !closed[id],
		count:      item.count,
		depth:      item.depth,
		padLeft:    basePad + float32(item.depth)*sidebarFolderIndent,
	}
}

// sidebarTagFolderRow is sidebarBranchFolderRow for the tag tree.
func sidebarTagFolderRow(item sidebarFolderItem[git.Tag], prefix string, closed map[string]bool, basePad float32) sidebarRowEntry {
	id := prefix + item.path
	return sidebarRowEntry{
		kind:       sidebarRowFolder,
		key:        "fold-" + id,
		folderID:   id,
		folderPath: item.path,
		folderSeg:  item.seg,
		folderOpen: !closed[id],
		count:      item.count,
		depth:      item.depth,
		padLeft:    basePad + float32(item.depth)*sidebarFolderIndent,
	}
}

// rebuildSidebarRows recomputes the cached row table from the current
// inputs: the folder grouping of the references, the multi-select counts,
// the remote-tracking branches grouped by their remote in one pass over the
// sorted list, and the rows themselves in the order the sidebar shows them.
// The stash selection prunes here: it is the only read of it that walks the
// stash list, and the rebuild runs exactly when the list or the selection
// changed. The row count is len(refs)+len(folders)+len(stashes)+a constant,
// so the rebuild is proportional to the data once, not per frame.
func (a *app) rebuildSidebarRows(rc *sidebarRowCache, refs git.Refs, stashes []git.Stash, localSorted, remoteSorted []git.Branch, tagSorted []git.Tag, stash *stashUIState) {
	closed := a.refsView.sidebarClosed
	if closed == nil {
		closed = map[string]bool{}
	}

	// The grouping of every section flattens the sorted references into the
	// folder and leaf rows in display order; the visible slices are the
	// leaves of the open folders, the list range selections and the
	// multi-select counts read.
	localItems, localVisibleIdx := sidebarGroupRefs(localSorted, func(br git.Branch) string { return br.Name }, "branches:", closed)
	localVisible := make([]git.Branch, 0, len(localVisibleIdx))
	for _, i := range localVisibleIdx {
		localVisible = append(localVisible, localSorted[i])
	}

	// Group the remote-tracking branches by their remote in one pass over
	// the sorted list: the display walks the remotes in the snapshot's
	// order, each remote's branches in their sorted subsequence — never one
	// scan of the whole list per remote.
	byRemote := make(map[string][]git.Branch, len(refs.Remotes))
	for _, rb := range remoteSorted {
		byRemote[rb.Remote] = append(byRemote[rb.Remote], rb)
	}

	tagItems, tagVisibleIdx := sidebarGroupRefs(tagSorted, func(tag git.Tag) string { return tag.Name }, "tags:", closed)
	tagVisible := make([]git.Tag, 0, len(tagVisibleIdx))
	for _, i := range tagVisibleIdx {
		tagVisible = append(tagVisible, tagSorted[i])
	}

	pick := a.refsView.pick
	localPicks := sidebarPickCount(pick, localVisible)
	tagPicks := sidebarTagPickCount(pick, tagVisible)

	// The lone selected tag is the one row an action bar unfolds under;
	// with two or more tags selected — or none — no bar shows.
	expandedTag := ""
	if tagPicks == 1 {
		for _, tag := range tagVisible {
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

	rows := make([]sidebarRowEntry, 0, len(localSorted)+len(remoteSorted)+len(tagSorted)+len(stashes)+24)
	rows = append(rows,
		sidebarRowEntry{kind: sidebarRowNav, key: "nav-Changes", label: "Changes", sfName: "square.and.pencil", fallback: fallbackDiff, target: route{page: pageChanges}},
		sidebarRowEntry{kind: sidebarRowNav, key: "nav-History", label: "History", sfName: "clock", fallback: fallbackHist, target: route{page: pageHistory}},
		sidebarRowEntry{kind: sidebarRowSpacer, key: "nav-gap"},
	)

	rows = append(rows, sidebarRowEntry{
		kind: sidebarRowSection, key: "sec-Branches", title: "Branches", count: len(refs.Branches),
		collapsed: &a.branchesCollapsed, menu: a.branchesSectionContextMenu,
		toggled: func(collapsed bool) { a.sidebarSectionToggled(sidebarSectionBranches, collapsed) },
	})
	if !a.branchesCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewBranch, key: "row-new-branch"})
		}
		if len(refs.Branches) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-branches", text: "No branches"})
		} else {
			for _, item := range localItems {
				if item.folder {
					rows = append(rows, sidebarBranchFolderRow(item, "branches:", "", closed, 10))
				} else {
					rows = append(rows, sidebarRowEntry{
						kind: sidebarRowBranch, key: "br-" + localSorted[item.leaf].FullName,
						branch: localSorted[item.leaf], depth: item.depth, padLeft: 10 + float32(item.depth)*sidebarFolderIndent,
					})
				}
			}
		}
	}

	rows = append(rows, sidebarRowEntry{
		kind: sidebarRowSection, key: "sec-Remotes", title: "Remotes", count: len(refs.Remotes),
		collapsed: &a.remotesCollapsed, menu: a.remotesSectionContextMenu,
		toggled: func(collapsed bool) { a.sidebarSectionToggled(sidebarSectionRemotes, collapsed) },
	})
	var remoteVisible []git.Branch
	if !a.remotesCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewRemote, key: "row-new-remote"})
		}
		if len(refs.Remotes) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-remotes", text: "No remotes"})
		} else {
			for _, rem := range refs.Remotes {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowRemote, key: "rem-" + rem, remote: rem})
				remoteItems, remoteVisibleIdx := sidebarGroupRefs(byRemote[rem], remoteBranchSortName, "remotes:"+rem+":", closed)
				for _, i := range remoteVisibleIdx {
					remoteVisible = append(remoteVisible, byRemote[rem][i])
				}
				for _, item := range remoteItems {
					if item.folder {
						rows = append(rows, sidebarBranchFolderRow(item, "remotes:"+rem+":", rem+"/", closed, 29))
					} else {
						rows = append(rows, sidebarRowEntry{
							kind: sidebarRowRemoteBranch, key: "rem-br-" + byRemote[rem][item.leaf].FullName,
							branch: byRemote[rem][item.leaf], depth: item.depth, padLeft: 29 + float32(item.depth)*sidebarFolderIndent,
						})
					}
				}
			}
		}
	} else {
		// The remote grouping still computes the visible slice for the
		// counts and ranges: collapsing the section hides rows without
		// changing what the flat display order would be.
		for _, rem := range refs.Remotes {
			_, remoteVisibleIdx := sidebarGroupRefs(byRemote[rem], remoteBranchSortName, "remotes:"+rem+":", closed)
			for _, i := range remoteVisibleIdx {
				remoteVisible = append(remoteVisible, byRemote[rem][i])
			}
		}
	}
	remotePicks := sidebarRemotePickCount(pick, remoteVisible)

	rows = append(rows, sidebarRowEntry{
		kind: sidebarRowSection, key: "sec-Tags", title: "Tags", count: len(refs.Tags),
		collapsed: &a.tagsCollapsed, menu: a.tagsSectionContextMenu,
		toggled: func(collapsed bool) { a.sidebarSectionToggled(sidebarSectionTags, collapsed) },
	})
	if !a.tagsCollapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowNewTag, key: "row-new-tag"})
		}
		if a.session.HasRepo() && len(refs.Tags) > 0 && len(refs.Remotes) > 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPushAllTags, key: "row-push-all-tags"})
		}
		if len(refs.Tags) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-tags", text: "No tags"})
		} else {
			for _, item := range tagItems {
				if item.folder {
					rows = append(rows, sidebarTagFolderRow(item, "tags:", closed, 10))
				} else {
					tag := tagSorted[item.leaf]
					rows = append(rows, sidebarRowEntry{
						kind: sidebarRowTag, key: "tag-" + tag.Name,
						tag: tag, depth: item.depth, padLeft: 10 + float32(item.depth)*sidebarFolderIndent,
					})
					if tag.Name == expandedTag {
						rows = append(rows, sidebarRowEntry{kind: sidebarRowTagActions, key: "tagrow-actions-" + tag.Name, tag: tag})
					}
				}
			}
		}
	}

	rows = append(rows, sidebarRowEntry{
		kind: sidebarRowSection, key: "sec-Stashes", title: "Stashes", count: len(stashes), collapsed: &stash.collapsed,
		toggled: func(collapsed bool) { a.sidebarSectionToggled(sidebarSectionStashes, collapsed) },
	})
	if !stash.collapsed {
		if a.session.HasRepo() {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowSaveStash, key: "row-save-stash"})
		}
		if len(stashes) == 0 {
			rows = append(rows, sidebarRowEntry{kind: sidebarRowPlaceholder, key: "ph-stashes", text: "No stashes"})
		} else {
			for _, s := range stashes {
				rows = append(rows, sidebarRowEntry{kind: sidebarRowStash, key: "stashrow-" + stashRowKey(s), stash: s})
				if stash.selectedKey != "" && stashRowKey(s) == stash.selectedKey {
					rows = append(rows, sidebarRowEntry{kind: sidebarRowStashActions, key: "stashrow-actions-" + stashRowKey(s), stash: s})
				}
			}
		}
	}

	*rc = sidebarRowCache{
		valid:         true,
		localSorted:   localSorted,
		remoteSorted:  remoteSorted,
		tagSorted:     tagSorted,
		remotes:       refs.Remotes,
		stashes:       stashes,
		branchesColl:  a.branchesCollapsed,
		remotesColl:   a.remotesCollapsed,
		tagsColl:      a.tagsCollapsed,
		stashesColl:   stash.collapsed,
		closedRev:     a.refsView.sidebarClosedRev,
		hasRepo:       a.session.HasRepo(),
		pickVersion:   a.refsView.pickVersion,
		pickLen:       len(pick),
		stashSelKey:   stash.selectedKey,
		rows:          rows,
		localVisible:  localVisible,
		remoteVisible: remoteVisible,
		tagVisible:    tagVisible,
		localPicks:    localPicks,
		remotePicks:   remotePicks,
		tagPicks:      tagPicks,
	}
}

// sidebarRowView draws one row of the sidebar's virtual list. The row
// builders are the ones the scroll area always used, unchanged in what they
// paint beyond the folder indentation and the shortened reference text the
// entries carry; the sorted slices and the pick counts they read come from
// the cache the row table was built with, so drawing walks nothing.
func (a *app) sidebarRowView(c *ui.Context, entry sidebarRowEntry) {
	rc := &a.refsView.sidebarRows
	switch entry.kind {
	case sidebarRowNav:
		a.navSidebarRow(c, entry)
	case sidebarRowSpacer:
		ui.Box(c).Height(6)
	case sidebarRowSection:
		hdr := sidebarSectionHeader(c, entry.title, entry.count, entry.collapsed, a.pal, entry.toggled)
		if entry.menu != nil {
			hdr.ContextMenu(entry.menu)
		}
	case sidebarRowNewBranch:
		a.newBranchRow(c)
	case sidebarRowNewRemote:
		a.newRemoteRow(c)
	case sidebarRowRemote:
		a.remoteRow(c, entry.remote)
	case sidebarRowFolder:
		a.sidebarFolderRow(c, entry)
	case sidebarRowBranch:
		inflight := a.refsView.delOpen && a.refsView.delChecking
		a.branchRow(c, entry.branch, inflight, rc.localVisible, rc.localPicks, entry.padLeft)
	case sidebarRowRemoteBranch:
		a.remoteBranchRow(c, entry.branch, a.session.Snapshot().Refs, rc.remoteVisible, rc.remotePicks, entry.padLeft)
	case sidebarRowNewTag:
		a.tagsSidebarHeaderActions(c)
	case sidebarRowPushAllTags:
		a.pushAllTagsSidebarEntry(c)
	case sidebarRowTag:
		a.tagRow(c, entry.tag, rc.tagVisible, rc.tagPicks, entry.padLeft)
	case sidebarRowTagActions:
		a.tagRowActions(c, entry.tag)
	case sidebarRowSaveStash:
		a.saveStashRow(c)
	case sidebarRowStash:
		a.stashRow(c, entry.stash)
	case sidebarRowStashActions:
		a.stashActionBar(c, entry.stash, a.session.Busy())
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
