package app

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// The two points of a commit a changed file can be reset to: exactly the
// state the commit left it in, or the state it had before the commit —
// which undoes the commit's change to the file alone.
const (
	fileResetAt = iota
	fileResetBefore
)

// fileResetUIState is the UI state of one app instance's file-reset
// confirmation dialog. It lives outside the shared app struct, which feature
// files must not extend. Every value the dialog or its write worker needs is
// captured on the UI thread when the menu item is chosen: the revision to
// restore from, the paths the reset touches, and the local status entries of
// exactly those paths as the snapshot showed them then. The worker re-reads
// the status and refuses the write when any of the captured entries changed.
type fileResetUIState struct {
	repoGen uint64

	open bool
	mode int

	commit git.Commit
	file   git.FileChange

	// rev is the revision the file is reset to. It is "" when the target is
	// the state before a root commit, because the empty tree's hash is read
	// in the write worker, where reading it costs no UI thread time.
	rev string
	// revIsCommit reports whether rev must still resolve to a commit when
	// the write runs. It is false only for the empty tree.
	revIsCommit bool
	// revDesc names the target in the dialog: the short hash of the revision
	// the file is restored to, or "" for the state before a root commit.
	revDesc string

	// paths are the paths handed to ResetFiles: the file's own path, plus the
	// original path of a rename when undoing the commit that renamed it, so
	// the old name comes back and the new name disappears.
	paths []string

	// local is the local status of the paths at the time the dialog opened:
	// their entries of the snapshot's Staged and Unstaged lists, staged
	// entries first, in the order the status reports them. A write that finds
	// anything else refuses to run.
	local []git.FileChange
}

var fileResetUIStates = struct {
	sync.Mutex
	m map[*app]*fileResetUIState
}{m: map[*app]*fileResetUIState{}}

// fileResetUI returns the app's file-reset state, dropping every value
// captured for a dialog of a repository generation that has moved on: the
// captured revision and local status belong to the repository that is gone.
func fileResetUI(a *app) *fileResetUIState {
	fileResetUIStates.Lock()
	defer fileResetUIStates.Unlock()
	st := fileResetUIStates.m[a]
	if st == nil {
		st = &fileResetUIState{}
		fileResetUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		*st = fileResetUIState{repoGen: gen}
	}
	return st
}

// clearFileResetDraft closes the dialog and drops every value captured for
// it, so a reopened dialog starts from the repository as it is then.
func (st *fileResetUIState) clear() {
	*st = fileResetUIState{repoGen: st.repoGen}
}

// historyFileResetMenuItems adds the two file-reset entries to the context
// menu of a changed-file row of the commit detail: resetting the file to the
// state the commit left it in, and resetting it to the state before the
// commit. Both are writes of the working tree and the index, so they are
// disabled while a write is already running or no repository is open.
func (a *app) historyFileResetMenuItems(m *ui.Menu, commit git.Commit, f git.FileChange) {
	// Copy the inputs at once, as the menu's caller does for the commit: the
	// menu's function runs again when an item is chosen, and the dialog must
	// capture the row the click happened on, never state the detail pane may
	// have moved on from since.
	commit.Parents = append([]string(nil), commit.Parents...)
	disabled := a.session.Busy() || !a.session.HasRepo()
	m.Separator()
	if m.Item("Reset File to State at Commit…").ID("file-reset-at").Disabled(disabled).Chosen() {
		a.openFileResetModal(fileResetAt, commit, f)
	}
	if m.Item("Reset File to State Before Commit…").ID("file-reset-before").Disabled(disabled).Chosen() {
		a.openFileResetModal(fileResetBefore, commit, f)
	}
}

// fileResetPaths are the paths a reset of the file touches: the file itself,
// plus the original path when undoing a rename, because the state before the
// commit has the file at its old name and nothing at the new one. A copy is
// not a rename: the original keeps existing in the commit itself, so undoing
// it must not delete anything the commit never touched.
func fileResetPaths(mode int, f git.FileChange) []string {
	paths := []string{f.Path}
	if mode == fileResetBefore && f.Status == 'R' && f.OrigPath != "" && f.OrigPath != f.Path {
		paths = append(paths, f.OrigPath)
	}
	return paths
}

// fileResetTarget resolves where the reset takes the file: the revision it
// is restored to, whether that revision is a commit the guard must still
// find, and how the dialog names it. "At" is the commit itself; "before" is
// its first parent — of a merge commit as of any other — or, for a root
// commit, the empty tree read in the write worker.
func fileResetTarget(mode int, commit git.Commit) (rev string, revIsCommit bool, revDesc string) {
	if mode == fileResetAt {
		return commit.Hash, true, shortChangesHash(commit.Hash)
	}
	if len(commit.Parents) > 0 {
		return commit.Parents[0], true, shortChangesHash(commit.Parents[0])
	}
	return "", false, ""
}

// fileResetLocalEntries are the local status entries of exactly the paths
// given, staged entries first in the order the status reports them. A path
// is matched by its current name and by the original name of a rename, so
// undoing a rename guards both ends of it. The same function reads the
// snapshot when the dialog opens and the live status in the write worker,
// so only a real change of these paths can differ.
func fileResetLocalEntries(st git.Status, paths []string) []git.FileChange {
	want := func(path, orig string) bool {
		return slices.Contains(paths, path) || (orig != "" && slices.Contains(paths, orig))
	}
	var out []git.FileChange
	for _, f := range st.Staged {
		if want(f.Path, f.OrigPath) {
			out = append(out, f)
		}
	}
	for _, f := range st.Unstaged {
		if want(f.Path, f.OrigPath) {
			out = append(out, f)
		}
	}
	return out
}

// openFileResetModal opens the confirmation dialog of a file reset. The
// target revision is decided here, on the UI thread, from the copied commit
// of the row the menu was opened on; the write worker never reads the commit
// list or any other interface state. The local status entries of the reset
// paths are captured the same way: the snapshot is what the user confirmed
// against, so the guard compares the write against exactly this state.
func (a *app) openFileResetModal(mode int, commit git.Commit, f git.FileChange) {
	st := fileResetUI(a)
	commit.Parents = append([]string(nil), commit.Parents...)
	rev, revIsCommit, revDesc := fileResetTarget(mode, commit)
	paths := fileResetPaths(mode, f)
	st.clear()
	st.open = true
	st.mode = mode
	st.commit = commit
	st.file = f
	st.rev = rev
	st.revIsCommit = revIsCommit
	st.revDesc = revDesc
	st.paths = paths
	st.local = fileResetLocalEntries(a.session.Snapshot().Status, paths)
}

func (a *app) closeFileResetModal() {
	fileResetUI(a).clear()
}

// fileResetTitle is the dialog's title: the menu item's text without the
// ellipsis, as the item's ellipsis stands for exactly this confirmation.
func fileResetTitle(mode int) string {
	if mode == fileResetBefore {
		return "Reset File to State Before Commit"
	}
	return "Reset File to State at Commit"
}

// fileResetStateIn names the revision the dialog restores the file to, for
// use inside a sentence: the short hash of the commit or its first parent,
// or the state before the root commit for a root commit's "before" reset.
func fileResetStateIn(st *fileResetUIState) string {
	if st.revDesc != "" {
		return "in " + st.revDesc
	}
	return "in the state before " + shortChangesHash(st.commit.Hash)
}

// fileResetBodyLines compose the dialog's result lines from the change the
// file is in the commit: what the target revision does with each path the
// reset touches. A commit that deletes the file has no version of it at all,
// so "at" deletes it again, while "before" brings the version of the parent
// back. Undoing a rename deletes the new name and restores the old one.
func fileResetBodyLines(st *fileResetUIState) []string {
	where := fileResetStateIn(st)
	existsAt := func() bool {
		if st.rev == "" {
			// The empty tree has no files at all.
			return false
		}
		switch st.file.Status {
		case 'D':
			return st.mode == fileResetBefore
		case 'A', 'C':
			// A file the commit added or copied into being exists in it but
			// not before it.
			return st.mode == fileResetAt
		case 'R':
			// The new name of a rename exists in the commit; before the
			// commit the file still went by its original name.
			return st.mode == fileResetAt
		default:
			return true
		}
	}
	var lines []string
	if existsAt() {
		lines = append(lines, fmt.Sprintf("%s will be reset to its state %s.", st.file.Path, where))
	} else {
		lines = append(lines, fmt.Sprintf("%s does not exist %s and will be deleted.", st.file.Path, where))
	}
	if st.mode == fileResetBefore && st.file.Status == 'R' && st.file.OrigPath != "" && st.file.OrigPath != st.file.Path {
		lines = append(lines, fmt.Sprintf("%s will be reset to its state %s.", st.file.OrigPath, where))
	}
	return lines
}

// fileResetDirtyPaths are the paths of the reset with uncommitted changes,
// in the order the captured status entries report them: the paths the dialog
// warns about because the reset discards what is not committed.
func fileResetDirtyPaths(local []git.FileChange) []string {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for _, f := range local {
		add(f.Path)
		add(f.OrigPath)
	}
	return out
}

// fileResetModal is the confirmation dialog of a file reset: it names the
// outcome per path, warns when uncommitted changes of them would be lost,
// and its Reset button submits the write through the session. The confirm
// button stays disabled while a write is running, because a busy session
// would refuse the write anyway — and a disabled control must never be the
// only protection, so the submission checks the same condition itself.
func (a *app) fileResetModal(c *ui.Context) {
	p := a.pal
	st := fileResetUI(a)
	if !st.open {
		return
	}
	title := fileResetTitle(st.mode)
	if ui.Modal(c.Key("file-reset-modal"), &st.open, func() {
		ui.Column(c.Key("file-reset-modal-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			for _, line := range fileResetBodyLines(st) {
				ui.Text(c, line).FontSize(13).TextColor(p.muted).MaxWidth(420)
			}
			if dirty := fileResetDirtyPaths(st.local); len(dirty) > 0 {
				ui.Text(c, "Uncommitted changes to "+strings.Join(dirty, ", ")+" will be lost.").FontSize(13).TextColor(p.danger).MaxWidth(420)
			}
			ui.Row(c.Key("file-reset-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("file-reset-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeFileResetModal()
				}
				if ui.PrimaryButton(c.Key("file-reset-confirm"), "Reset").Label("Confirm Reset").Disabled(a.session.Busy()).Clicked() {
					a.submitFileReset()
				}
			})
		})
	}).Dismissed() {
		a.closeFileResetModal()
	}
}

// submitFileReset runs the file reset the dialog describes through the
// session's write path. Everything the worker needs was captured when the
// dialog opened; the guard re-validates it against the live repository
// because any of it can change between the click and the write. The dialog
// closes before the write starts: its outcome shows in the toolbar.
func (a *app) submitFileReset() {
	st := fileResetUI(a)
	if a.session.Busy() {
		return
	}
	rev := st.rev
	revIsCommit := st.revIsCommit
	paths := append([]string(nil), st.paths...)
	local := append([]git.FileChange(nil), st.local...)
	if len(paths) == 0 {
		a.closeFileResetModal()
		return
	}
	a.closeFileResetModal()
	a.session.do("Resetting file", func(repo *git.Repo) error {
		return resetFileWithGuard(repo, rev, revIsCommit, paths, local)
	})
}

// resetFileWithGuard re-validates the repository against what the dialog
// captured before it restores the files: the status must still be readable,
// the revision must still resolve when it is a commit, and the local entries
// of exactly the reset paths must be unchanged — nothing the write then
// discards may have moved since the user confirmed it. Only then does the
// reset run, reading the empty tree's hash here when the target is the state
// before a root commit. Any check failing returns the error without touching
// the working tree or the index.
func resetFileWithGuard(repo *git.Repo, rev string, revIsCommit bool, paths []string, local []git.FileChange) error {
	if len(paths) == 0 {
		return fmt.Errorf("cannot reset file: no paths")
	}
	live, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot reset file: reading the repository state failed: %w", err)
	}
	if revIsCommit {
		if rev == "" {
			return fmt.Errorf("cannot reset file: no target revision")
		}
		if _, err := repo.PathHash(rev + "^{commit}"); err != nil {
			return fmt.Errorf("cannot reset file: the target commit %s no longer exists: %w", shortChangesHash(rev), err)
		}
	}
	if !slices.Equal(fileResetLocalEntries(live, paths), local) {
		return fmt.Errorf("cannot reset file: the local state of %s changed after the dialog opened; review the repository and try again", paths[0])
	}
	if rev == "" {
		rev, err = repo.EmptyTree()
		if err != nil {
			return fmt.Errorf("cannot reset file: reading the empty tree failed: %w", err)
		}
	}
	return repo.ResetFiles(rev, paths...)
}
