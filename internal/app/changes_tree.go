package app

import (
	"path"
	"sort"

	"github.com/zhdsmy/branchline/internal/git"
)

// changesTreeRow is one row of a file section of Local Changes rendered as
// a tree: either a folder row or a file row. Depth counts rows, not path
// segments, so the children of a folder chain merged into one row are only
// one level deeper than it.
type changesTreeRow struct {
	// depth is the indentation level of the row: files at the root and the
	// topmost folders sit at 0, and every nested row is one deeper.
	depth int
	// folder is set on a folder row, whose display is its name and whose
	// count is the number of files under it at every level.
	folder  bool
	display string
	// fullPath is the folder's complete path inside the repository. A chain
	// of folders collapsed into one row carries the merged path, a/b/c, which
	// is what the collapse state keys on. Empty on file rows.
	fullPath string
	count    int
	// file is the change of a file row.
	file git.FileChange
}

// changesTreeFolder is the mutable tree buildChangesTree assembles before
// flattening: files of the folder itself, its subfolders by their next
// segment, and the running count of files at every level below.
type changesTreeFolder struct {
	files   []git.FileChange
	folders map[string]*changesTreeFolder
	order   []string
	count   int
}

func (f *changesTreeFolder) child(segment string) *changesTreeFolder {
	if f.folders == nil {
		f.folders = make(map[string]*changesTreeFolder)
	}
	sub, ok := f.folders[segment]
	if !ok {
		sub = &changesTreeFolder{}
		f.folders[segment] = sub
		f.order = append(f.order, segment)
	}
	return sub
}

// splitSlash divides a repository-relative path into its segments. Paths of
// Local Changes never carry leading, trailing or doubled slashes, so every
// segment is a real name.
func splitSlash(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

// buildChangesTree flattens the already filtered files of one section into
// the rows a tree shows: folder rows before file rows inside every folder,
// each ordered by byte-wise name, with chains of single-child folders merged
// into one row. collapsed is keyed by the merged folder path, and its rows
// stay in the output while everything under them leaves it; expandAll, the
// filtering behavior, ignores the collapse state entirely. The count of a
// folder row covers its files at every depth.
func buildChangesTree(files []git.FileChange, collapsed map[string]bool, expandAll bool) []changesTreeRow {
	root := &changesTreeFolder{}
	for _, f := range files {
		insertTreeChange(root, f)
	}
	var rows []changesTreeRow
	flattenTreeFolder(root, "", 0, collapsed, expandAll, &rows)
	return rows
}

// insertTreeChange files change under the path it has now. Renames keep no
// tree entry at their original path: the rename marker belongs to the file
// row, not to where the file used to be.
func insertTreeChange(root *changesTreeFolder, change git.FileChange) {
	segments := splitSlash(change.Path)
	folder := root
	for _, segment := range segments[:len(segments)-1] {
		folder = folder.child(segment)
	}
	folder.files = append(folder.files, change)
	folder = root
	folder.count++
	for _, segment := range segments[:len(segments)-1] {
		folder = folder.folders[segment]
		folder.count++
	}
}

// flattenTreeFolder appends the rows of folder, already at depth, in display
// order: subfolders first, byte-ordered by the segment the user types them
// by, then the folder's own files in the same order. A subfolder chain where
// every folder holds nothing but the next single folder merges into one row
// carrying the joined path, which ends where files or a second subfolder
// appear. Rows under a collapsed folder stay out, and expandAll reads every
// folder as open.
func flattenTreeFolder(folder *changesTreeFolder, prefix string, depth int, collapsed map[string]bool, expandAll bool, rows *[]changesTreeRow) {
	segments := append([]string(nil), folder.order...)
	sort.Strings(segments)
	for _, segment := range segments {
		sub := folder.folders[segment]
		name := segment
		full := segment
		if prefix != "" {
			full = prefix + "/" + segment
		}
		// Merge the chain while the folder holds only the single subfolder
		// that continues it: no files of its own, no second subfolder.
		for len(sub.files) == 0 && len(sub.order) == 1 {
			next := sub.order[0]
			name += "/" + next
			full += "/" + next
			sub = sub.folders[next]
		}
		*rows = append(*rows, changesTreeRow{
			depth:    depth,
			folder:   true,
			display:  name,
			fullPath: full,
			count:    sub.count,
		})
		if expandAll || !collapsed[full] {
			flattenTreeFolder(sub, full, depth+1, collapsed, expandAll, rows)
		}
	}

	sorted := append([]git.FileChange(nil), folder.files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, f := range sorted {
		*rows = append(*rows, changesTreeRow{
			depth:   depth,
			display: treeFileDisplay(f),
			file:    f,
		})
	}
}

// treeFileDisplay is the text of a file row of the tree: the bare name, the
// folders being rows of their own. A renamed file keeps its origin visible
// because the row names only the name it has now.
func treeFileDisplay(f git.FileChange) string {
	name := path.Base(f.Path)
	if f.OrigPath != "" && f.OrigPath != f.Path {
		return name + " ← " + f.OrigPath
	}
	return name
}
