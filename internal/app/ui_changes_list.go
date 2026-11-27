package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// changesRowKind distinguishes the rows of the single virtual list the
// Local Changes file pane is built of: section headers, the hairlines
// between sections, empty-section hints, and the three kinds of file rows.
type changesRowKind uint8

const (
	changesRowConflictHeader changesRowKind = iota + 1
	changesRowConflicted
	changesRowDividerConflict
	changesRowUnstagedHeader
	changesRowUnstaged
	changesRowUnstagedEmpty
	changesRowDividerUnstaged
	changesRowStagedHeader
	changesRowStaged
	changesRowStagedEmpty
)

// changesRow is one row of the Local Changes file list. File rows carry
// their change; structural rows are identified by their kind alone.
type changesRow struct {
	kind changesRowKind
	file git.FileChange
}

// changesRowKey is the stable identity of one row: the section it belongs
// to and the file path inside that section. Structural rows use their kind
// as the section and an empty path, which is unique because no file path
// is empty.
type changesRowKey struct {
	section changesRowKind
	path    string
}

func (r changesRow) key() changesRowKey {
	return changesRowKey{section: r.kind, path: r.file.Path}
}

// changesListCache derives everything the Local Changes file pane computes
// from one snapshot: the status signature syncChangesRepoState compares,
// the conflicted/ordinary split of the unstaged files, the row sequence of
// the virtual list, and path indexes that replace the linear scans the
// selection lookup and the conflict rows ran every frame. The derivation is
// valid only for the exact snapshot slices it was built from: a snapshot
// reload replaces the slices wholesale rather than mutating them in place,
// so sharing a backing array and length with the current snapshot is proof
// the contents are unchanged, and any new read misses and recomputes.
// Keeping the source slices in the cache keeps their backing arrays alive,
// so a new snapshot can never reuse a cached array's address.
type changesListCache struct {
	valid bool

	head      string
	unstaged  []git.FileChange
	staged    []git.FileChange
	conflicts []git.ConflictStage

	signature      string
	conflicted     []git.FileChange
	ordinary       []git.FileChange
	rows           []changesRow
	unstagedByPath map[string]git.FileChange
	unstagedByOrig map[string]git.FileChange
	stagedByPath   map[string]git.FileChange
	stagedByOrig   map[string]git.FileChange
	stageByPath    map[string]git.ConflictStage
}

// invalidate drops the cached derivation, for example when the repository
// session resets its per-repository state.
func (c *changesListCache) invalidate() {
	*c = changesListCache{}
}

// lookup returns the derivation for st, building it when the snapshot
// slices it was derived from are gone. Head movement alone also rebuilds:
// the signature covers it for amend prefill and staged diffs.
func (c *changesListCache) lookup(st git.Status) *changesListCache {
	if c.valid &&
		c.head == st.Head &&
		sameBackingSlice(c.unstaged, st.Unstaged) &&
		sameBackingSlice(c.staged, st.Staged) &&
		sameBackingSlice(c.conflicts, st.Conflicts) {
		return c
	}
	c.build(st)
	return c
}

// build recomputes every derived value from st.
func (c *changesListCache) build(st git.Status) {
	c.valid = true
	c.head = st.Head
	c.unstaged = st.Unstaged
	c.staged = st.Staged
	c.conflicts = st.Conflicts

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

	rows := make([]changesRow, 0, len(st.Unstaged)+len(st.Staged)+7)
	if len(c.conflicted) > 0 {
		rows = append(rows, changesRow{kind: changesRowConflictHeader})
		for _, f := range c.conflicted {
			rows = append(rows, changesRow{kind: changesRowConflicted, file: f})
		}
		rows = append(rows, changesRow{kind: changesRowDividerConflict})
	}
	rows = append(rows, changesRow{kind: changesRowUnstagedHeader})
	if len(c.ordinary) == 0 {
		rows = append(rows, changesRow{kind: changesRowUnstagedEmpty})
	} else {
		for _, f := range c.ordinary {
			rows = append(rows, changesRow{kind: changesRowUnstaged, file: f})
		}
	}
	rows = append(rows, changesRow{kind: changesRowDividerUnstaged})
	rows = append(rows, changesRow{kind: changesRowStagedHeader})
	if len(st.Staged) == 0 {
		rows = append(rows, changesRow{kind: changesRowStagedEmpty})
	} else {
		for _, f := range st.Staged {
			rows = append(rows, changesRow{kind: changesRowStaged, file: f})
		}
	}
	c.rows = rows
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
	switch row.kind {
	case changesRowConflictHeader:
		a.changesConflictHeader(c, len(cache.conflicted))
	case changesRowConflicted:
		a.changesConflictRow(c, row.file, cache, p)
	case changesRowDividerConflict, changesRowDividerUnstaged:
		ui.Column(c).Height(13).FillWidth().Children(func() {
			ui.Box(c).Height(6).FillWidth()
			ui.Box(c).Height(1).FillWidth().Background(p.hairline)
			ui.Box(c).Height(6).FillWidth()
		})
	case changesRowUnstagedHeader:
		a.changesFileSectionHeader(c, false, cache.ordinary)
	case changesRowUnstaged:
		a.changesFileRow(c, row.file, false)
	case changesRowUnstagedEmpty:
		a.changesFileEmptyHint(c, false)
	case changesRowStagedHeader:
		a.changesFileSectionHeader(c, true, cache.staged)
	case changesRowStaged:
		a.changesFileRow(c, row.file, true)
	case changesRowStagedEmpty:
		a.changesFileEmptyHint(c, true)
	}
}
