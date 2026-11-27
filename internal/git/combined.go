package git

import (
	"sort"
	"strings"
)

// CombinedFile is a file of the combined view of Local Changes, where
// the staged and the unstaged changes share one list: a file changed in
// both places has one row.
type CombinedFile struct {
	Path string
	// Staged and Unstaged say which lists the file is in.
	Staged, Unstaged bool
	// Change is the change the row stands for: the unstaged one when the
	// file has both, since that is the work still to stage.
	Change FileChange
	// Index is the position of Change in the list it came from.
	Index int
}

// Checked is the row's checkbox: on when everything of the file is staged.
func (f CombinedFile) Checked() bool { return f.Staged && !f.Unstaged }

// Mixed is the checkbox of a file with changes both staged and not.
func (f CombinedFile) Mixed() bool { return f.Staged && f.Unstaged }

// Combine merges the lists of Local Changes by path, sorted by name.
func Combine(staged, unstaged []FileChange) []CombinedFile {
	by := make(map[string]*CombinedFile, len(staged)+len(unstaged))
	var order []string
	for i, f := range staged {
		by[f.Path] = &CombinedFile{Path: f.Path, Staged: true, Change: f, Index: i}
		order = append(order, f.Path)
	}
	for i, f := range unstaged {
		if c, ok := by[f.Path]; ok {
			c.Unstaged, c.Change, c.Index = true, f, i
			continue
		}
		by[f.Path] = &CombinedFile{Path: f.Path, Unstaged: true, Change: f, Index: i}
		order = append(order, f.Path)
	}
	sort.SliceStable(order, func(i, j int) bool { return strings.ToLower(order[i]) < strings.ToLower(order[j]) })
	out := make([]CombinedFile, len(order))
	for i, p := range order {
		out[i] = *by[p]
	}
	return out
}
