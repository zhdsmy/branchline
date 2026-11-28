package app

import (
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// changedFileList renders the files of a commit or a comparison as a
// virtual list: only the rows in view are built, so a frame costs the same
// however many files the commit changed. sel is the view's file selection,
// which the list owns while it shows: clicking a row — or moving to it with
// the arrow keys — writes it, and the list's Changed then runs selectFile
// for the views to load the file's diff. Choosing the row already chosen
// reports no change, so a second click never reloads a diff.
//
// The rows paint the selection themselves in the neutral wash, exactly as
// the hand-rolled rows did; the list's own selection and hover fills are
// made transparent in the theme copy, so nothing paints twice and the
// inactive-window fade the accent would take never shows. The scroll state
// is the caller's; resetting it together with the file selection puts the
// list back at its first row, and the row keys being plain paths is safe
// because the state never survives its files.
//
// menu is the context menu of a row (nil for none) and selectFile the
// action of choosing one. What differs between the three views — the diff
// they load and whether rows carry a menu at all — stays in those handlers;
// the rows themselves are identical.
func changedFileList(c *ui.Context, state *ui.ListState, files []git.FileChange, p palette, sel *int, menu func(m *ui.Menu, f git.FileChange), selectFile func(i int, f git.FileChange)) {
	state.Key = func(row int) any { return files[row].Path }
	state.Label = func(row int) string { return "File " + files[row].Path }
	state.Selected = sel

	listTheme := *c.Theme()
	listTheme.Accent = ui.Transparent
	listTheme.SurfaceHover = ui.Transparent
	ui.Themed(c.Key("files-theme"), &listTheme).
		Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
		Children(func() {
			list := ui.List(c.Key("files-scroll"), state, len(files)).
				Grow(1).FillWidth().MinHeight(0).Padding(4, 0).
				Rows(func(r ui.ListRow) {
					changedFileRow(r.Context, r.Hovered(), p, files[r.Index], r.Selected(), menu)
				})
			if list.Changed() && *sel >= 0 && *sel < len(files) {
				selectFile(*sel, files[*sel])
			}
		})
}

// changedFileRow renders one file row: the status badge, the path — both
// paths of a rename — and the selected or hovered background. The list the
// row is in owns the interaction; the row only draws, and carries the
// context menu where the view gives one.
func changedFileRow(c *ui.Context, hovered bool, p palette, f git.FileChange, selected bool, menu func(m *ui.Menu, file git.FileChange)) {
	row := ui.Row(c).
		Height(24).
		FillWidth().
		PaddingX(14).
		Gap(7).
		AlignItems(ui.Center)
	switch {
	case selected:
		row.Background(p.selected)
	case hovered:
		row.Background(p.hover)
	}
	if menu != nil {
		row.ContextMenu(func(m *ui.Menu) {
			menu(m, f)
		})
	}
	row.Children(func() {
		col := historyStatusColor(p, f.Status)
		ui.Box(c).Size(16, 16).Radius(4).Background(col.Alpha(0.18)).Center().Shrink(0).Children(func() {
			ui.Text(c, string(f.Status)).FontSize(9.5).FontWeight(700).TextColor(col).SingleLine()
		})
		name := f.Path
		if f.OrigPath != "" && f.OrigPath != f.Path {
			name = f.OrigPath + " → " + f.Path
		}
		ui.Text(c, name).FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
}

// changedFileTreeRow renders one file row of a tree view: the same status
// badge, background and context menu as the flat row, but indented by the
// row's depth and displaying the tree's display name — the bare file name,
// with the original path of a rename after the arrow — because the folders
// are rows of their own.
func changedFileTreeRow(c *ui.Context, hovered bool, p palette, r changesTreeRow, selected bool, menu func(m *ui.Menu, file git.FileChange)) {
	f := r.file
	row := ui.Row(c).
		Height(24).
		FillWidth().
		PaddingStart(14 + changesTreeIndent*float32(r.depth)).
		PaddingEnd(14).
		Gap(7).
		AlignItems(ui.Center)
	switch {
	case selected:
		row.Background(p.selected)
	case hovered:
		row.Background(p.hover)
	}
	if menu != nil {
		row.ContextMenu(func(m *ui.Menu) {
			menu(m, f)
		})
	}
	row.Children(func() {
		col := historyStatusColor(p, f.Status)
		ui.Box(c).Size(16, 16).Radius(4).Background(col.Alpha(0.18)).Center().Shrink(0).Children(func() {
			ui.Text(c, string(f.Status)).FontSize(9.5).FontWeight(700).TextColor(col).SingleLine()
		})
		ui.Text(c, r.display).FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
}
