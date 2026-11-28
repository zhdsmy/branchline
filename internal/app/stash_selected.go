package app

import (
	"fmt"

	"github.com/zhdsmy/branchline/internal/git"
)

// Reasons the Selected files scope of the Save Stash dialog cannot submit,
// shared by the dialog's live explanation and the write worker's last-moment
// verification so the user hears one wording for one cause.
const (
	stashErrSelectFiles   = "Select at least one file to stash."
	stashErrConflicts     = "Cannot stash while files have conflicts."
	stashErrSelectedEmpty = "The selected files have no changes to stash."
)

// stashFileEntry is one row of the Save Stash dialog's Selected files list:
// a file with uncommitted changes, keyed by its path. A staged rename keeps
// its original path so the stash can take both halves.
type stashFileEntry struct {
	path     string
	origPath string
	status   byte
}

// stashStatusHasConflicts reports whether the status holds any unmerged
// file. Git cannot write a partial stash while the index has unmerged
// entries at all — even for paths the conflict does not touch — so one
// conflicted file blocks the operation whichever files are chosen.
func stashStatusHasConflicts(st git.Status) bool {
	for _, f := range st.Unstaged {
		if f.Status == 'U' {
			return true
		}
	}
	return false
}

// stashSelectedFiles builds the file list of the Save Stash dialog's
// Selected files scope from one status: every staged or unstaged change to
// a tracked file, plus the untracked files when they are included. Ignored
// files never enter the list — git stash never touches them. A file changed
// both staged and unstaged appears once, under the staged entry, whose
// rename carries the original path the stash needs.
func stashSelectedFiles(st git.Status, includeUntracked bool) []stashFileEntry {
	var files []stashFileEntry
	seen := map[string]bool{}
	add := func(f git.FileChange) {
		if seen[f.Path] {
			return
		}
		seen[f.Path] = true
		files = append(files, stashFileEntry{path: f.Path, origPath: f.OrigPath, status: f.Status})
	}
	for _, f := range st.Staged {
		if f.Status == '!' {
			continue
		}
		add(f)
	}
	for _, f := range st.Unstaged {
		if f.Status == '!' || (f.Status == '?' && !includeUntracked) {
			continue
		}
		add(f)
	}
	return files
}

// stashFilePaths returns the path arguments one entry contributes to the
// stash: its path, and for a rename the original path as well, because
// stashing the new path alone would leave the old path's deletion behind
// in the index.
func stashFilePaths(e stashFileEntry) []string {
	if e.status == 'R' && e.origPath != "" && e.origPath != e.path {
		return []string{e.origPath, e.path}
	}
	return []string{e.path}
}

// stashPathsToSave re-reads the live working tree at the last moment before
// a partial stash and keeps only the selected paths that still have changes
// the chosen options would stash: tracked changes always, untracked files
// only when they are included. It returns false when the live repository
// cannot be stashed by these paths at all, and the caller must not push.
func stashPathsToSave(live git.Status, selected []string, includeUntracked bool) ([]string, bool) {
	if live.Head == "" || stashStatusHasConflicts(live) {
		return nil, false
	}
	type change struct {
		status   byte
		origPath string
	}
	changes := map[string]change{}
	for _, f := range live.Staged {
		if f.Status == '!' {
			continue
		}
		if _, ok := changes[f.Path]; !ok {
			changes[f.Path] = change{status: f.Status, origPath: f.OrigPath}
		}
	}
	for _, f := range live.Unstaged {
		if f.Status == '!' || f.Status == 'U' {
			continue
		}
		if _, ok := changes[f.Path]; !ok {
			changes[f.Path] = change{status: f.Status, origPath: f.OrigPath}
		}
	}
	var paths []string
	kept := map[string]bool{}
	for _, path := range selected {
		if kept[path] {
			continue
		}
		c, ok := changes[path]
		if !ok || (c.status == '?' && !includeUntracked) {
			continue
		}
		kept[path] = true
		if c.status == 'R' && c.origPath != "" && c.origPath != path && !kept[c.origPath] {
			kept[c.origPath] = true
			paths = append(paths, c.origPath)
		}
		paths = append(paths, path)
	}
	return paths, true
}

// stashSaveSelectedWithGuard re-reads the live working tree before pushing
// because the dialog's snapshot answer can be stale by the time the write
// runs: the paths the user checked may have been reverted behind the
// dialog's back, and a partial stash of nothing must be an error instead of
// the empty success git would report.
func stashSaveSelectedWithGuard(repo *git.Repo, message string, selected []string, includeUntracked bool) error {
	live, err := repo.Status()
	if err != nil {
		return err
	}
	if live.Head == "" {
		return fmt.Errorf("%s", stashErrUnborn)
	}
	if stashStatusHasConflicts(live) {
		return fmt.Errorf("%s", stashErrConflicts)
	}
	paths, ok := stashPathsToSave(live, selected, includeUntracked)
	if !ok || len(paths) == 0 {
		return fmt.Errorf("%s", stashErrSelectedEmpty)
	}
	return repo.StashPaths(message, paths)
}

// stashSaveSelectedProblem explains why the Selected files scope cannot
// submit right now, or "" when it can: a repository before its first
// commit, a conflicted working tree, or no file checked. It reads the
// loaded snapshot only; the write worker re-checks the live repository
// before pushing.
func stashSaveSelectedProblem(st git.Status, files []stashFileEntry, checked map[string]bool) string {
	if st.Head == "" {
		return stashErrUnborn
	}
	if stashStatusHasConflicts(st) {
		return stashErrConflicts
	}
	for _, f := range files {
		if checked[f.path] {
			return ""
		}
	}
	return stashErrSelectFiles
}

// stashSaveReconcile drops the check marks of files no longer in the list,
// so a file whose changes were reverted while the dialog was open cannot be
// stashed, and one reappearing afterwards starts unchecked. Files new to
// the list are simply absent from the map, which reads as unchecked.
func stashSaveReconcile(files []stashFileEntry, checked map[string]bool) {
	for path := range checked {
		present := false
		for _, f := range files {
			if f.path == path {
				present = true
				break
			}
		}
		if !present {
			delete(checked, path)
		}
	}
}

// stashSaveSubmitPaths collects the paths the dialog's check marks cover,
// in the list's order, expanding renames to both of their paths.
func stashSaveSubmitPaths(files []stashFileEntry, checked map[string]bool) []string {
	var paths []string
	for _, f := range files {
		if checked[f.path] {
			paths = append(paths, stashFilePaths(f)...)
		}
	}
	return paths
}

// stashSaveAllChecked marks every file of the list checked: the starting
// point of the Selected files scope chosen from the sidebar.
func stashSaveAllChecked(files []stashFileEntry, checked map[string]bool) {
	for path := range checked {
		delete(checked, path)
	}
	for _, f := range files {
		checked[f.path] = true
	}
}

// stashScopeAll and stashScopeSelected are the values of the dialog's
// saveScope: stashing the whole working tree, or only the checked files.
const (
	stashScopeAll      = 0
	stashScopeSelected = 1
)
