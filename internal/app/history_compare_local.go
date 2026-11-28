package app

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/repository"
)

// historyCompareLocalState is the commit-to-working-tree comparison state of
// the History page. All fields are owned by the UI thread; the background
// file-list read publishes through sessionRead with the fixed scope
// "history-compare-local".
type historyCompareLocalState struct {
	// commit is the commit the working tree is compared against, captured
	// with cloned parents when the comparison opened. Unlike the two-commit
	// comparison there is no direction: the commit is always the base and
	// the working tree the target.
	commit git.Commit

	files   []git.FileChange
	loading bool
	err     error
	fileSel int
	// fileList is the scroll state of the comparison's changed-file list;
	// it resets wherever fileSel does, as in the two-commit comparison.
	fileList ui.ListState

	// refresh counts the snapshot loads the comparison re-read for. It is
	// the revision of the file's diff requests, so a refresh reloads the
	// diff even when the same file stays selected.
	refresh int
	// snapKey is the commit slice of the snapshot the file list was last
	// read for. Snapshot loads replace the slice whole and never mutate it,
	// so its backing-array identity decides whether a new snapshot arrived,
	// exactly as historySelectionCache keys on it. Retaining it keeps the
	// backing array alive, so a later snapshot can never reuse the address.
	snapKey []git.Commit
}

// startHistoryCompareLocal opens the comparison of cm against the working
// tree: the menu action of Compare to Local Changes. The list's selection
// narrows to the one commit, the detail pane switches to the comparison,
// and the file list reads in the background.
func (a *app) startHistoryCompareLocal(cm git.Commit) {
	hv := &a.historyView
	if a.session.Busy() || !a.session.Loaded() {
		return
	}
	cm.Parents = append([]string(nil), cm.Parents...)
	a.exitHistoryMode()
	hv.sel = cm.Hash
	hv.commit = cm
	hv.selection.Clear()
	hv.selection.Add(cm.Hash)
	if idx := slices.IndexFunc(a.session.Snapshot().Commits, func(c git.Commit) bool { return c.Hash == cm.Hash }); idx >= 0 {
		hv.listSel = idx
		hv.list.ScrollTo(idx, ui.Center)
	}
	hv.mode = historyCompareLocal
	hv.compareLocal = historyCompareLocalState{
		commit:  cm,
		snapKey: a.session.Snapshot().Commits,
	}
	a.startHistoryCompareLocalLoad()
}

// startHistoryCompareLocalLoad begins the asynchronous read of the files the
// working tree changes relative to the comparison's commit under the
// "history-compare-local" scope. Only the hash crosses into the worker.
// Results of superseded reads are discarded by sessionRead itself; done
// additionally checks the state still compares the same commit before
// applying, so a slow read cannot surface after the selection changed. The
// previously selected file keeps its selection when it still exists after a
// refresh, so a working-tree change does not take the diff the user reads
// away; otherwise the first file is selected.
func (a *app) startHistoryCompareLocalLoad() {
	hv := &a.historyView
	cl := &hv.compareLocal
	hash := cl.commit.Hash
	rev := strconv.Itoa(cl.refresh)
	prevPath := ""
	if cl.fileSel >= 0 && cl.fileSel < len(cl.files) {
		prevPath = cl.files[cl.fileSel].Path
	}
	cl.loading = true
	cl.err = nil
	cl.files = nil
	cl.fileSel = -1
	cl.fileList = ui.ListState{}
	hv.diff.Clear(a.session, "history-diff")
	started := sessionRead(a.session, "history-compare-local", func(repo *git.Repo) ([]git.FileChange, error) {
		return repo.WorktreeFiles(hash)
	}, func(files []git.FileChange, err error) {
		if hv.mode != historyCompareLocal || cl.commit.Hash != hash {
			return
		}
		cl.loading = false
		if err != nil {
			cl.err = err
			cl.files = nil
			cl.fileSel = -1
			cl.fileList = ui.ListState{}
			return
		}
		cl.files = files
		cl.err = nil
		if len(files) == 0 {
			cl.fileSel = -1
			cl.fileList = ui.ListState{}
			return
		}
		sel := 0
		for i, f := range files {
			if f.Path == prevPath {
				sel = i
				break
			}
		}
		cl.fileSel = sel
		cl.fileList = ui.ListState{}
		hv.diff.Load(a.session, "history-diff", worktreeFileDiffRequest(cl.commit, files[sel], rev))
	})
	if !started {
		cl.loading = false
	}
}

// maybeRefreshHistoryCompareLocal re-reads the comparison's file list when a
// new snapshot arrived since the list was last read: every snapshot load
// reads the status of the working tree as it is then, so a new snapshot is
// the signal the working tree may have changed — through the automatic
// refresh or a write operation of the app itself. The snapshot's identity is
// its commit slice, which every load rebuilds whole; an unchanged slice
// means no load completed since the last read, and nothing is re-read.
func (a *app) maybeRefreshHistoryCompareLocal(snap repository.Snapshot) {
	hv := &a.historyView
	cl := &hv.compareLocal
	if hv.mode != historyCompareLocal || cl.commit.Hash == "" {
		return
	}
	if sameBackingSlice(cl.snapKey, snap.Commits) {
		return
	}
	cl.snapKey = snap.Commits
	cl.refresh++
	a.startHistoryCompareLocalLoad()
}

// showHistoryCompareLocalCommit leaves the comparison for the commit's
// ordinary detail, the action of the header's Show Commit button: the same
// commit stays selected, so the pane reads exactly as choosing its row does.
func (a *app) showHistoryCompareLocalCommit() {
	hv := &a.historyView
	if hv.mode != historyCompareLocal {
		return
	}
	cm := hv.compareLocal.commit
	a.exitHistoryMode()
	hv.mode = historySingleCommit
	a.selectHistoryCommit(cm)
}

// historyCompareLocalInfo renders the comparison header in the detail pane's
// left column: the commit's row with its short hash in red, the Local
// Changes row in green beneath it, and the button returning to the commit's
// detail. There is no direction to swap: the working tree is always the
// target.
func (a *app) historyCompareLocalInfo(c *ui.Context, p palette) {
	hv := &a.historyView
	cl := &hv.compareLocal
	ui.Column(c.Key("history-compare-local-info")).Padding(10, 14).Gap(8).FillWidth().Shrink(0).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Comparing").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			ui.Spacer(c)
			b := ui.ButtonBase(c.Key("compare-local-show-commit")).
				Height(22).
				PaddingX(8).
				Radius(5).
				Center().
				Label("Show Commit").
				FocusRing(false)
			if b.Hovered() {
				b.Background(p.hover)
			}
			b.Children(func() {
				ui.Text(c, "Show Commit").FontSize(11.5).FontWeight(500).TextColor(p.muted).SingleLine()
			})
			if b.Clicked() {
				a.showHistoryCompareLocalCommit()
			}
		})
		a.historyCompareCommitRow(c, p, cl.commit, p.danger, "Comparison Commit "+cl.commit.Short())
		ui.Row(c.Key("compare-local-row-worktree")).FillWidth().Gap(6).AlignItems(ui.Center).Label("Comparison Local Changes").Children(func() {
			ui.Text(c, "Local Changes").FontSize(11).FontWeight(600).TextColor(c.Theme().Success).SingleLine().Shrink(0)
		})
	})
}

// historyCompareLocalFileList renders the files the working tree changes
// relative to the comparison's commit, styled as the two-commit comparison's
// file list. Clicking a file loads its worktree diff into the shared diff
// panel. Rows carry no context menu: the entries of the other file lists —
// the external diff of two commits, the file history and blame of one —
// have no meaning for a comparison against uncommitted content.
func (a *app) historyCompareLocalFileList(c *ui.Context, p palette) {
	hv := &a.historyView
	cl := &hv.compareLocal
	ui.Column(c.Key("history-compare-local-files")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("history-compare-local-files-header")).Height(26).FillWidth().PaddingX(14).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "Changed files").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			if len(cl.files) > 0 {
				ui.Text(c, fmt.Sprint(len(cl.files))).FontSize(11).TextColor(p.faint).SingleLine()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		switch {
		case cl.loading:
			ui.Row(c.Key("compare-local-files-loading")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "Loading changed files…").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		case cl.err != nil:
			ui.Column(c.Key("compare-local-files-error")).Padding(8, 14).Gap(6).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "Failed to load changed files").FontSize(11.5).FontWeight(600).TextColor(p.danger)
				ui.Text(c, cl.err.Error()).FontSize(11).TextColor(p.muted)
				if ui.Button(c.Key("compare-local-files-retry"), "Retry").Label("Retry Comparison").Clicked() {
					a.startHistoryCompareLocalLoad()
				}
			})
		case len(cl.files) == 0:
			ui.Row(c.Key("compare-local-files-empty")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "No differences").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		default:
			changedFileList(c.Key("compare-local-files"), &cl.fileList, cl.files, p, &cl.fileSel,
				nil,
				func(i int, f git.FileChange) {
					if cl.commit.Hash != "" {
						hv.diff.Load(a.session, "history-diff", worktreeFileDiffRequest(cl.commit, f, strconv.Itoa(cl.refresh)))
					}
				})
		}
	})
}
