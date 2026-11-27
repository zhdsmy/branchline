package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/exttool"
	"github.com/zhdsmy/branchline/internal/git"
)

// externalDiffState is the run state of the external diff integration. The
// injectable fields belong to the UI thread: tests replace them before the
// state is first used, and the pipeline reads them where its clicks read
// the repository, so no tool of a test can ever be a real graphical program
// and no snapshot can land outside the test's temporary folder.
type externalDiffState struct {
	// find looks up the programs of the preset tools; nil uses
	// exttool.FindProgram.
	find exttool.Finder
	// snapshotParent is the folder snapshots are created in and the cleanup
	// sweep reads. "" uses the system temporary folder.
	snapshotParent string
}

// externalDiffUI returns the app's external diff state, creating it on first
// use. The creation also schedules the one sweep of stale snapshot folders a
// run performs: it posts onto the UI thread before it spawns, so the sweep's
// worker reads the state the same thread owns instead of racing it.
func externalDiffUI(a *app) *externalDiffState {
	return featureState(a, func() *externalDiffState {
		st := &externalDiffState{}
		a.post(func() {
			parent := st.snapshotParent
			if parent == "" {
				parent = os.TempDir()
			}
			a.spawn(func() {
				_, _ = exttool.CleanStale(parent, 24*time.Hour)
			})
		})
		return st
	})
}

// openExternalDiff compares the two sides of paths[0] — with paths[1] the
// pre-rename path — in the user's external diff tool. base and changed name
// the revisions exactly as CompareSides reads them: "index" for the index,
// "" for the working tree or the empty tree depending on the side, a commit
// hash otherwise. Everything happens in one background worker detached from
// the session: the tool may stay open for minutes, so the launch neither
// takes the write busy state nor follows the repository generation. The
// repository captured at the click serves the read even after the user has
// switched to another one, and the snapshot is deleted when the tool exits,
// whenever that happens.
func (a *app) openExternalDiff(base, changed string, paths ...string) {
	if len(paths) == 0 || paths[0] == "" {
		return
	}
	repo := a.session.Repo()
	if repo == nil {
		return
	}
	st := externalDiffUI(a)
	find := st.find
	parent := st.snapshotParent
	settings := a.store.Settings()
	toolName := settings.DiffTool
	if toolName == "" {
		toolName = "FileMerge"
	}
	merged := paths[0]
	captured := append([]string(nil), paths...)
	a.spawn(func() {
		fail := func(err error) {
			text := err.Error()
			a.post(func() {
				a.toast = "External diff failed: " + git.RedactText(text)
			})
		}
		tool, err := exttool.ResolveDiff(toolName, settings.CustomDiff.Path, settings.CustomDiff.Args, find)
		if err != nil {
			fail(err)
			return
		}
		localData, remoteData, localName, remoteName, err := repo.CompareSides(base, changed, captured...)
		if err != nil {
			fail(fmt.Errorf("%s: reading %s: %w", tool.Name, merged, err))
			return
		}
		if parent == "" {
			parent = os.TempDir()
		}
		snap, err := exttool.NewSnapshot(parent)
		if err != nil {
			fail(fmt.Errorf("%s: creating the snapshot: %w", tool.Name, err))
			return
		}
		// The snapshot is removed when the worker reaches its end — after the
		// tool exits — whatever happens between the creation and the run.
		defer func() { _ = snap.Remove() }()
		localFile, err := snap.Write("local", localName, localData, false)
		if err != nil {
			fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
			return
		}
		remoteFile, err := snap.Write("remote", remoteName, remoteData, false)
		if err != nil {
			fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
			return
		}
		if err := exttool.RunDiff(context.Background(), tool, exttool.Vars{
			Local:  localFile,
			Remote: remoteFile,
			Merged: merged,
		}); err != nil {
			var exitErr *exttool.ExitError
			if errors.As(err, &exitErr) {
				fail(fmt.Errorf("%s: %w", tool.Name, err))
			} else {
				fail(err)
			}
		}
	})
}

// changesExternalDiffMenuItem is the Local Changes half of the external diff
// menu entry: unstaged files compare the index with the working tree, staged
// files HEAD with the index, and untracked files — nothing of them ever
// entered the index — read an empty left side through the same call. A
// conflicted file offers no comparison, because its versions belong to the
// merge tool instead.
func (a *app) changesExternalDiffMenuItem(m *ui.Menu, f git.FileChange) {
	if m.Item("Open in External Diff Tool").ID("external-diff").Disabled(f.Status == 'U').Chosen() {
		if f.Staged {
			a.openExternalDiff("", "index", f.Path, f.OrigPath)
		} else {
			a.openExternalDiff("index", "", f.Path)
		}
	}
}

// historyExternalDiffMenuItem is the external diff entry of one file of the
// commit the History detail shows: the parent side in the comparison is the
// commit's first parent, or the empty tree before the root commit, exactly
// the sides the page's own diff of the file uses.
func (a *app) historyExternalDiffMenuItem(m *ui.Menu, commit git.Commit, f git.FileChange) {
	if m.Item("Open in External Diff Tool").ID("external-diff").Chosen() {
		base := ""
		if len(commit.Parents) > 0 {
			base = commit.Parents[0]
		}
		a.openExternalDiff(base, commit.Hash, f.Path, f.OrigPath)
	}
}

// compareExternalDiffMenuItem is the external diff entry of the two-commit
// comparison's file rows: the comparison's base on the left, its target on
// the right, the direction the comparison's own diff panel uses.
func (a *app) compareExternalDiffMenuItem(m *ui.Menu, base, target git.Commit, f git.FileChange) {
	if m.Item("Open in External Diff Tool").ID("external-diff").Chosen() {
		a.openExternalDiff(base.Hash, target.Hash, f.Path, f.OrigPath)
	}
}

// compareFileContextMenu is the context menu the comparison's file rows
// carry: only the external diff entry, because File History and Blame belong
// to one commit, not to a range.
func (a *app) compareFileContextMenu(m *ui.Menu, f git.FileChange) {
	hv := &a.historyView
	a.compareExternalDiffMenuItem(m, hv.compare.base, hv.compare.target, f)
}
