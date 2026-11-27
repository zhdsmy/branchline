package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// changesRowKind distinguishes the rows of the single virtual list the
// Local Changes file pane is built of: section headers, the hairlines
// between sections, empty-section hints, the file rows and, in the tree,
// the folder rows of the unstaged and staged sections.
type changesRowKind uint8

const (
	changesRowConflictHeader changesRowKind = iota + 1
	changesRowConflicted
	changesRowDividerConflict
	changesRowUnstagedHeader
	changesRowUnstaged
	changesRowUnstagedFolder
	changesRowUnstagedEmpty
	changesRowDividerUnstaged
	changesRowStagedHeader
	changesRowStaged
	changesRowStagedFolder
	changesRowStagedEmpty
)

// changesRow is one row of the Local Changes file list. File rows carry
// their change; folder rows carry their depth, merged display name, full
// path and the number of files under them; structural rows are identified
// by their kind alone.
type changesRow struct {
	kind       changesRowKind
	file       git.FileChange
	depth      int
	display    string
	folderPath string
	count      int
}

// changesRowKey is the stable identity of one row: the section it belongs
// to and the file or folder path inside that section. Structural rows use
// their kind as the section and an empty path, which is unique because no
// file path is empty.
type changesRowKey struct {
	section changesRowKind
	path    string
}

func (r changesRow) key() changesRowKey {
	if r.kind == changesRowUnstagedFolder || r.kind == changesRowStagedFolder {
		return changesRowKey{section: r.kind, path: r.folderPath}
	}
	return changesRowKey{section: r.kind, path: r.file.Path}
}

// changesListCache derives everything the Local Changes file pane computes
// from one snapshot and one filter text: the status signature
// syncChangesRepoState compares, the conflicted/ordinary split of the
// unstaged files, the filtered file lists of the three sections, the row
// sequence of the virtual list, and path indexes that replace the linear
// scans the selection lookup and the conflict rows ran every frame. The
// derivation is valid only for the exact snapshot slices it was built from:
// a snapshot reload replaces the slices wholesale rather than mutating them
// in place, so sharing a backing array and length with the current snapshot
// is proof the contents are unchanged, and any new read misses and
// recomputes. Keeping the source slices in the cache keeps their backing
// arrays alive, so a new snapshot can never reuse a cached array's address.
// The signature, the split and the path indexes do not depend on the
// filter, the view mode or the collapse state; only the shown lists and the
// row sequence do. The collapse sets enter the derivation by reference but
// are compared only through collapseRev, which every collapse choice bumps:
// comparing the maps every frame would cost what the tree saves.
type changesListCache struct {
	valid bool

	head      string
	unstaged  []git.FileChange
	staged    []git.FileChange
	conflicts []git.ConflictStage
	filter    string

	tree              bool
	collapseRev       uint64
	collapsedUnstaged map[string]bool
	collapsedStaged   map[string]bool

	signature       string
	conflicted      []git.FileChange
	ordinary        []git.FileChange
	shownConflicted []git.FileChange
	shownOrdinary   []git.FileChange
	shownStaged     []git.FileChange
	rows            []changesRow
	unstagedByPath  map[string]git.FileChange
	unstagedByOrig  map[string]git.FileChange
	stagedByPath    map[string]git.FileChange
	stagedByOrig    map[string]git.FileChange
	stageByPath     map[string]git.ConflictStage
}

// invalidate drops the cached derivation, for example when the repository
// session resets its per-repository state.
func (c *changesListCache) invalidate() {
	*c = changesListCache{}
}

// lookup returns the derivation for st, building it when the snapshot
// slices it was derived from are gone. Head movement alone also rebuilds:
// the signature covers it for amend prefill and staged diffs. A rebuild
// keeps the filter, view mode and collapse version of the previous
// derivation: the signature, split and path indexes lookup callers read are
// the same for every filter, and the rows are consumed only through
// lookupFiltered, which rebuilds again when any of them changed.
func (c *changesListCache) lookup(st git.Status) *changesListCache {
	if c.valid &&
		c.head == st.Head &&
		sameBackingSlice(c.unstaged, st.Unstaged) &&
		sameBackingSlice(c.staged, st.Staged) &&
		sameBackingSlice(c.conflicts, st.Conflicts) {
		return c
	}
	c.build(st, c.filter, c.tree, c.collapseRev, c.collapsedUnstaged, c.collapsedStaged)
	return c
}

// lookupFiltered returns the derivation for st under the given raw filter
// text, view mode and collapse state, rebuilding when any of them moved on
// since the last build. Typing, staging, toggling the view, collapsing a
// folder and reloading recompute the row sequence once; the frames in
// between reuse it without allocating.
func (c *changesListCache) lookupFiltered(st git.Status, rawFilter string, tree bool, collapseRev uint64, collapsedUnstaged, collapsedStaged map[string]bool) *changesListCache {
	filter := normalizeChangesFilter(rawFilter)
	cached := c.lookup(st)
	if cached.filter == filter &&
		cached.tree == tree &&
		cached.collapseRev == collapseRev {
		return cached
	}
	c.build(st, filter, tree, collapseRev, collapsedUnstaged, collapsedStaged)
	return c
}

// normalizeChangesFilter is the matching form of the raw text typed into
// the filter box: surrounding space gone, case folded. An empty result
// means no filter.
func normalizeChangesFilter(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// changeMatchesFilter reports whether f stays in the list under the
// normalized filter: a case-insensitive substring of its path or, for a
// rename, of the path it was renamed from.
func changeMatchesFilter(f git.FileChange, filter string) bool {
	if filter == "" {
		return true
	}
	if strings.Contains(strings.ToLower(f.Path), filter) {
		return true
	}
	return f.OrigPath != "" && strings.Contains(strings.ToLower(f.OrigPath), filter)
}

// filterFileChanges is files reduced to the entries the normalized filter
// keeps, in their original order. Without a filter it is files itself.
func filterFileChanges(files []git.FileChange, filter string) []git.FileChange {
	if filter == "" {
		return files
	}
	var shown []git.FileChange
	for _, f := range files {
		if changeMatchesFilter(f, filter) {
			shown = append(shown, f)
		}
	}
	return shown
}

// build recomputes every derived value from st, the normalized filter, the
// view mode and the collapse state at collapseRev.
func (c *changesListCache) build(st git.Status, filter string, tree bool, collapseRev uint64, collapsedUnstaged, collapsedStaged map[string]bool) {
	c.valid = true
	c.head = st.Head
	c.unstaged = st.Unstaged
	c.staged = st.Staged
	c.conflicts = st.Conflicts
	c.filter = filter
	c.tree = tree
	c.collapseRev = collapseRev
	c.collapsedUnstaged = collapsedUnstaged
	c.collapsedStaged = collapsedStaged

	c.signature = changesStatusSignature(st)
	c.conflicted, c.ordinary = splitConflictedChanges(st.Unstaged)

	c.unstagedByPath = make(map[string]git.FileChange, len(st.Unstaged))
	c.unstagedByOrig = make(map[string]git.FileChange, len(st.Unstaged))
	for _, f := range st.Unstaged {
		c.unstagedByPath[f.Path] = f
		if f.OrigPath != "" {
			c.unstagedByOrig[f.OrigPath] = f
		}
	}
	c.stagedByPath = make(map[string]git.FileChange, len(st.Staged))
	c.stagedByOrig = make(map[string]git.FileChange, len(st.Staged))
	for _, f := range st.Staged {
		c.stagedByPath[f.Path] = f
		if f.OrigPath != "" {
			c.stagedByOrig[f.OrigPath] = f
		}
	}
	c.stageByPath = make(map[string]git.ConflictStage, len(st.Conflicts))
	for _, cs := range st.Conflicts {
		c.stageByPath[cs.Path] = cs
	}

	c.shownConflicted = filterFileChanges(c.conflicted, filter)
	c.shownOrdinary = filterFileChanges(c.ordinary, filter)
	c.shownStaged = filterFileChanges(st.Staged, filter)

	// The tree groups the unstaged and staged sections; the conflicted
	// section stays flat either way. Under a filter every folder is
	// expanded, so the collapse sets do not reach the build.
	expandAll := filter != ""
	var unstagedRows, stagedRows []changesTreeRow
	if tree {
		unstagedRows = buildChangesTree(c.shownOrdinary, collapsedUnstaged, expandAll)
		stagedRows = buildChangesTree(c.shownStaged, collapsedStaged, expandAll)
	}

	rows := make([]changesRow, 0, len(c.shownOrdinary)+len(c.shownConflicted)+len(c.shownStaged)+7)
	if len(c.shownConflicted) > 0 {
		rows = append(rows, changesRow{kind: changesRowConflictHeader})
		for _, f := range c.shownConflicted {
			rows = append(rows, changesRow{kind: changesRowConflicted, file: f})
		}
		rows = append(rows, changesRow{kind: changesRowDividerConflict})
	}
	rows = append(rows, changesRow{kind: changesRowUnstagedHeader})
	if len(c.shownOrdinary) == 0 {
		rows = append(rows, changesRow{kind: changesRowUnstagedEmpty})
	} else if tree {
		rows = appendTreeRows(rows, unstagedRows, changesRowUnstagedFolder, changesRowUnstaged)
	} else {
		for _, f := range c.shownOrdinary {
			rows = append(rows, changesRow{kind: changesRowUnstaged, file: f, display: changeDisplayName(f)})
		}
	}
	rows = append(rows, changesRow{kind: changesRowDividerUnstaged})
	rows = append(rows, changesRow{kind: changesRowStagedHeader})
	if len(c.shownStaged) == 0 {
		rows = append(rows, changesRow{kind: changesRowStagedEmpty})
	} else if tree {
		rows = appendTreeRows(rows, stagedRows, changesRowStagedFolder, changesRowStaged)
	} else {
		for _, f := range c.shownStaged {
			rows = append(rows, changesRow{kind: changesRowStaged, file: f, display: changeDisplayName(f)})
		}
	}
	c.rows = rows
}

// appendTreeRows translates the flattened tree rows of one section into the
// list rows of its folder and file kinds.
func appendTreeRows(rows []changesRow, treeRows []changesTreeRow, folderKind, fileKind changesRowKind) []changesRow {
	for _, tr := range treeRows {
		if tr.folder {
			rows = append(rows, changesRow{kind: folderKind, depth: tr.depth, display: tr.display, folderPath: tr.fullPath, count: tr.count})
		} else {
			rows = append(rows, changesRow{kind: fileKind, file: tr.file, depth: tr.depth, display: tr.display})
		}
	}
	return rows
}

// lookupUnstaged finds path among the unstaged entries, matching Path first
// and OrigPath second, exactly as findChange does, without a scan.
func (c *changesListCache) lookupUnstaged(path string) (git.FileChange, bool) {
	if f, ok := c.unstagedByPath[path]; ok {
		return f, true
	}
	if f, ok := c.unstagedByOrig[path]; ok {
		return f, true
	}
	return git.FileChange{}, false
}

// lookupStaged finds path among the staged entries, as lookupUnstaged does
// for the unstaged ones.
func (c *changesListCache) lookupStaged(path string) (git.FileChange, bool) {
	if f, ok := c.stagedByPath[path]; ok {
		return f, true
	}
	if f, ok := c.stagedByOrig[path]; ok {
		return f, true
	}
	return git.FileChange{}, false
}

// lookupConflictStage returns the unmerged stage metadata of path, replacing
// the per-row linear scan of Status.ConflictStage.
func (c *changesListCache) lookupConflictStage(path string) (git.ConflictStage, bool) {
	cs, ok := c.stageByPath[path]
	return cs, ok
}

// changesFilesList renders the Conflicted, Unstaged and Staged sections as
// one virtual list that builds only the rows in view. The row sequence
// reproduces the previous three-section layout exactly: a gap of 4 inside
// each section, the hairline between two sections wrapped in 10 of space,
// and the conflict section with its hairline present only when conflicts
// exist. A divider row is 6 + 1 + 6 high so the list's uniform 4-gap adds
// up to the original 10 on each side of the line.
func (a *app) changesFilesList(c *ui.Context, cache *changesListCache) {
	s := &a.changesView
	s.list.Key = func(row int) any {
		return cache.rows[row].key()
	}
	ui.List(c.Key("changes-lists-scroll"), &s.list, len(cache.rows)).
		Grow(1).FillWidth().MinHeight(0).
		Padding(0, 12, 10, 12).Gap(4).
		Rows(func(r ui.ListRow) {
			a.changesListRow(r.Context, cache, cache.rows[r.Index])
		})
}

// changesListRow builds one row of the file list, dispatching on the row
// kind the cache recorded.
func (a *app) changesListRow(c *ui.Context, cache *changesListCache, row changesRow) {
	p := a.pal
	filtered := cache.filter != ""
	switch row.kind {
	case changesRowConflictHeader:
		if filtered {
			a.changesConflictHeaderFiltered(c, len(cache.shownConflicted), len(cache.conflicted))
		} else {
			a.changesConflictHeader(c, len(cache.conflicted))
		}
	case changesRowConflicted:
		a.changesConflictRow(c, row.file, cache, p)
	case changesRowDividerConflict, changesRowDividerUnstaged:
		ui.Column(c).Height(13).FillWidth().Children(func() {
			ui.Box(c).Height(6).FillWidth()
			ui.Box(c).Height(1).FillWidth().Background(p.hairline)
			ui.Box(c).Height(6).FillWidth()
		})
	case changesRowUnstagedHeader:
		a.changesFileSectionHeader(c, false, cache.shownOrdinary, len(cache.ordinary), filtered)
	case changesRowUnstaged:
		a.changesFileRow(c, row.file, false, row.depth, row.display)
	case changesRowUnstagedFolder:
		a.changesFolderRow(c, row)
	case changesRowUnstagedEmpty:
		a.changesFileEmptyHint(c, false, filtered && len(cache.ordinary) > 0)
	case changesRowStagedHeader:
		a.changesFileSectionHeader(c, true, cache.shownStaged, len(cache.staged), filtered)
	case changesRowStaged:
		a.changesFileRow(c, row.file, true, row.depth, row.display)
	case changesRowStagedFolder:
		a.changesFolderRow(c, row)
	case changesRowStagedEmpty:
		a.changesFileEmptyHint(c, true, filtered && len(cache.staged) > 0)
	}
}

// changesConflictHeaderFiltered renders the title row of the Conflicted
// section while a filter is active, counting the files the filter keeps
// against all conflicted ones. Without a filter the section header is
// changesConflictHeader.
func (a *app) changesConflictHeaderFiltered(c *ui.Context, shown, total int) {
	p := a.pal
	ui.Row(c.Key("changes-section-header-conflicted")).Height(30).FillWidth().Padding(0, 2).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("Conflicted (%d of %d)", shown, total)).FontSize(12).FontWeight(650).TextColor(p.danger).SingleLine()
	})
}
