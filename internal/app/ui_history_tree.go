package app

import (
	"fmt"
	"path"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// historyTreeCache derives the rows of the changed-file tree of one commit
// from its file list and the collapse state, exactly as changesListCache
// does for Local Changes: the derivation is valid only for the exact files
// slice it was built from, which detail loads replace wholesale rather than
// mutate, so sharing a backing array and length proves the contents
// unchanged. The collapse set enters by revision: comparing the map every
// frame would cost what the tree saves. The indexes below are built once
// with the rows and answer the per-frame selection and click lookups
// without scanning the list.
type historyTreeCache struct {
	valid       bool
	commit      string
	files       []git.FileChange
	collapseRev uint64

	rows        []changesTreeRow
	rowByFile   map[string]int
	rowByFolder map[string]int
	flatByPath  map[string]int
}

// lookup returns the derivation for the commit's files at collapseRev,
// building it when the slice or the revision moved on since the last build.
// Choosing another commit, collapsing a folder and retrying a failed detail
// read each recompute the rows once; the frames in between reuse them
// without allocating.
func (c *historyTreeCache) lookup(commit string, files []git.FileChange, collapseRev uint64, collapsed map[string]bool) *historyTreeCache {
	if c.valid && c.commit == commit && sameBackingSlice(c.files, files) && c.collapseRev == collapseRev {
		return c
	}
	c.valid = true
	c.commit = commit
	c.files = files
	c.collapseRev = collapseRev
	c.rows = buildChangesTree(files, collapsed, false)
	c.rowByFile = make(map[string]int, len(files))
	c.rowByFolder = make(map[string]int)
	for i, r := range c.rows {
		if r.folder {
			c.rowByFolder[r.fullPath] = i
		} else {
			c.rowByFile[r.file.Path] = i
		}
	}
	c.flatByPath = make(map[string]int, len(files))
	for i, f := range files {
		c.flatByPath[f.Path] = i
	}
	return c
}

// rowOfFile is the row the selection rests on while file is the chosen one:
// its own row when it shows, the nearest collapsed ancestor folder when it
// is hidden inside one, or -1 when it is nowhere. The first ancestor found
// walking the path upward is the nearest one visible: an expanded ancestor
// would show everything above it, so no folder closer to the file can sit
// in the rows.
func (c *historyTreeCache) rowOfFile(filePath string) int {
	if row, ok := c.rowByFile[filePath]; ok {
		return row
	}
	for dir := path.Dir(filePath); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if row, ok := c.rowByFolder[dir]; ok {
			return row
		}
	}
	return -1
}

// historyTreeRowKey is the stable identity of one tree row: the folder's
// merged path or the file's path, distinguished by kind so a file never
// collides with the folder it sits in. It differs from the flat list's
// plain path keys, so switching the view never carries a selection index
// across by accident.
type historyTreeRowKey struct {
	folder bool
	path   string
}

// toggleHistoryTree switches the commit's changed-file list between the
// flat list and the tree, as toggleChangesTree does for Local Changes: the
// view changes either way and the choice is then written to the
// repository's settings under its own field, a failed write surfacing as
// the settings toast while the view keeps the choice for this opening. The
// folder focus leaves with the view: the same file stays selected, and the
// diff keeps showing.
func (a *app) toggleHistoryTree() {
	hv := &a.historyView
	hv.treeMode = !hv.treeMode
	hv.treeFocus = ""
	if a.store == nil {
		return
	}
	id := a.session.Meta().ID
	if id == "" {
		return
	}
	meta, ok := a.store.Get(id)
	if !ok {
		return
	}
	local := meta.Local
	local.Diff.HistoryFileListTree = hv.treeMode
	if err := a.store.UpdateLocal(id, local); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
}

// setHistoryFolderCollapsed records the collapse choice of one folder row,
// keyed by the merged folder path. A choice already in effect is a no-op,
// so Right on an open folder and Left on a collapsed one keep the
// derivation cached.
func (a *app) setHistoryFolderCollapsed(folderPath string, collapsed bool) {
	hv := &a.historyView
	if hv.treeCollapsed == nil {
		hv.treeCollapsed = map[string]bool{}
	}
	if hv.treeCollapsed[folderPath] == collapsed {
		return
	}
	hv.treeCollapsed[folderPath] = collapsed
	hv.treeCollapseRev++
}

// historyFileTree renders the files of the selected commit grouped in
// folders, as one virtual list of folder and file rows. The selection the
// list shows is derived from the file selection and the folder focus
// before the list builds: it rests on the folder treeFocus names while
// that folder is a row, and on the chosen file otherwise — its own row,
// or the nearest collapsed folder hiding it, so collapsing the folder of
// the selected file moves the selection onto the folder row while the
// diff keeps showing. Choosing another commit drops the focus: its first
// file is the selected one then. Up and Down move between the rows
// through the list itself; landing on a file row loads its diff, landing
// on a folder row moves only the focus.
func (a *app) historyFileTree(c *ui.Context, p palette) {
	hv := &a.historyView
	cache := hv.treeRows.lookup(hv.sel, hv.files, hv.treeCollapseRev, hv.treeCollapsed)
	if hv.treeCommit != hv.sel {
		hv.treeCommit = hv.sel
		hv.treeFocus = ""
	}

	desired := -1
	if hv.treeFocus != "" {
		if row, ok := cache.rowByFolder[hv.treeFocus]; ok {
			desired = row
		}
	}
	if desired < 0 && hv.fileSel >= 0 && hv.fileSel < len(hv.files) {
		desired = cache.rowOfFile(hv.files[hv.fileSel].Path)
	}
	hv.fileList.Key = func(row int) any {
		r := cache.rows[row]
		return historyTreeRowKey{folder: r.folder, path: rowKeyPath(r)}
	}
	hv.fileList.Label = func(row int) string {
		r := cache.rows[row]
		if r.folder {
			return historyTreeFolderLabel(r)
		}
		return "File " + r.file.Path
	}
	hv.fileList.Selected = &hv.treeRowSel
	// Choose rather than a bare write: the list otherwise takes a selection
	// left at the row it chose last for its item staying put, and moves it
	// with that item as rows come and go. Here the selection is derived
	// fresh every frame, so the list's own tracking must never apply.
	hv.fileList.Choose(desired)

	listTheme := *c.Theme()
	listTheme.Accent = ui.Transparent
	listTheme.SurfaceHover = ui.Transparent
	ui.Themed(c.Key("files-theme"), &listTheme).
		Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
		Children(func() {
			list := ui.List(c.Key("files-scroll"), &hv.fileList, len(cache.rows)).
				Grow(1).FillWidth().MinHeight(0).Padding(4, 0).
				Rows(func(r ui.ListRow) {
					a.historyTreeRow(r.Context, r.Hovered(), p, cache, cache.rows[r.Index], r.Selected())
				})
			if list.Changed() && hv.treeRowSel >= 0 && hv.treeRowSel < len(cache.rows) {
				a.historyTreeRowChosen(cache, hv.treeRowSel)
			}
			// Right expands a collapsed folder row and Left collapses an open
			// one, on the row the selection rests on. A file row answers
			// neither: the keys move rows in an outline, but the tree rows
			// here carry no nesting of their own under a file.
			if hv.treeRowSel >= 0 && hv.treeRowSel < len(cache.rows) {
				if row := cache.rows[hv.treeRowSel]; row.folder {
					collapsed := hv.treeCollapsed[row.fullPath]
					if collapsed {
						if hv.fileList.Shortcut(c, 0, ui.KeyRight) {
							a.setHistoryFolderCollapsed(row.fullPath, false)
						}
					} else if hv.fileList.Shortcut(c, 0, ui.KeyLeft) {
						a.setHistoryFolderCollapsed(row.fullPath, true)
					}
				}
			}
		})
}

// rowKeyPath is the path half of a row's key: the merged path of a folder
// row, the file path of a file row.
func rowKeyPath(r changesTreeRow) string {
	if r.folder {
		return r.fullPath
	}
	return r.file.Path
}

// historyTreeFolderLabel is the accessibility name of a folder row: its
// merged path and the count of files under it at every level.
func historyTreeFolderLabel(r changesTreeRow) string {
	noun := "files"
	if r.count == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%s, %d %s", r.fullPath, r.count, noun)
}

// historyTreeRowChosen applies a selection the list reports: a file row
// becomes the chosen file and its diff loads; a folder row moves only the
// focus, the diff of the file chosen before keeps showing.
func (a *app) historyTreeRowChosen(cache *historyTreeCache, row int) {
	hv := &a.historyView
	r := cache.rows[row]
	if r.folder {
		hv.treeFocus = r.fullPath
		return
	}
	hv.treeFocus = ""
	if flat, ok := cache.flatByPath[r.file.Path]; ok {
		hv.fileSel = flat
	}
	if hv.sel != "" {
		hv.diff.Load(a.session, "history-diff", commitFileDiffRequest(hv.commit, r.file))
	}
}

// historyTreeRow builds one row of the changed-file tree, dispatching on
// the row kind.
func (a *app) historyTreeRow(c *ui.Context, hovered bool, p palette, cache *historyTreeCache, row changesTreeRow, selected bool) {
	hv := &a.historyView
	if row.folder {
		a.historyTreeFolderRow(c, hovered, p, row, selected)
		return
	}
	changedFileTreeRow(c, hovered, p, row, selected,
		func(m *ui.Menu, f git.FileChange) {
			a.historyFileContextMenu(m, hv.commit, f)
		})
}

// historyTreeFolderRow renders one folder of the tree: its indentation,
// the collapse arrow, the merged name and the count of files under it at
// every level. Clicking the row, or pressing space or enter on it, toggles
// the collapse and rests the selection on the row; it carries no context
// menu. The row paints the selection the list hands it, so the folder
// hiding the selected file reads as the selected one. The button's name
// carries the Folder word the row's own list-item label does not, so a
// click by name lands on the control unambiguously.
func (a *app) historyTreeFolderRow(c *ui.Context, hovered bool, p palette, row changesTreeRow, selected bool) {
	hv := &a.historyView
	collapsed := hv.treeCollapsed[row.fullPath]

	b := ui.ButtonBase(c.Key("history-folder-" + row.fullPath)).
		FillWidth().
		MinWidth(0).
		Height(24).
		PaddingStart(14 + changesTreeIndent*float32(row.depth)).
		PaddingEnd(14).
		Gap(4).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label("Folder " + historyTreeFolderLabel(row)).
		Expanded(!collapsed).
		FocusRing(false)
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.selected
	case hovered || b.Hovered():
		bg = p.hover
	}
	b.Background(bg)
	b.Children(func() {
		ui.Box(c).Width(14).Shrink(0).Center().Children(func() {
			icon, fb := "chevron.down", fallbackDown
			if collapsed {
				icon, fb = "chevron.right", fallbackFwd
			}
			symbolIcon(c, icon, fb, 10, p.faint)
		})
		ui.Text(c, row.display).FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		ui.Spacer(c)
		noun := "files"
		if row.count == 1 {
			noun = "file"
		}
		ui.Text(c, fmt.Sprintf("%d %s", row.count, noun)).FontSize(11).TextColor(p.faint).SingleLine().Shrink(0)
	})
	if b.Clicked() {
		hv.treeFocus = row.fullPath
		a.setHistoryFolderCollapsed(row.fullPath, !collapsed)
	}
}
