package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/repository"
)

// historyCompare is the two-commit comparison state of the History page.
// All fields are owned by the UI thread; the background file-list read
// publishes through sessionRead with the fixed scope "history-compare".
type historyCompareState struct {
	// base is the older side of the comparison, target the newer one: the
	// files and diffs are those of git diff <base> <target>. By default base
	// is the commit lower in the list and target the one higher up; the swap
	// button exchanges them.
	base   git.Commit
	target git.Commit

	files   []git.FileChange
	loading bool
	err     error
	fileSel int
}

// syncHistorySelection reconciles the History detail pane with the rows
// chosen in the commit list. It runs on every frame the list builds, because
// the choice can change without user input: keys of commits a reload dropped
// (amended away, rebased out, reset off the walk) are pruned here, since the
// selection itself keeps keys of rows that are gone. Pruning to one commit
// falls back to the single-commit view; pruning to none clears the pane.
func (a *app) syncHistorySelection(snap repository.Snapshot) {
	hv := &a.historyView

	if hv.selection.Len() > 0 {
		present := make(map[string]struct{}, len(snap.Commits))
		for _, cm := range snap.Commits {
			present[cm.Hash] = struct{}{}
		}
		for k := range hv.selection.All() {
			if _, ok := present[k]; !ok {
				hv.selection.Remove(k)
			}
		}
	}

	var ordered []git.Commit
	for _, cm := range snap.Commits {
		if hv.selection.Has(cm.Hash) {
			ordered = append(ordered, cm)
		}
	}

	switch len(ordered) {
	case 0:
		if hv.mode != historyNoSelection {
			a.exitHistoryMode()
			hv.mode = historyNoSelection
		}
		hv.sel = ""
		hv.commit = git.Commit{}
		// With nothing chosen the list's cursor row is reset, so the keys
		// start over from the first row.
		hv.listSel = -1
	case 1:
		// prevSel is the commit the single-commit detail was last started
		// for; sel is overwritten below, so it must be captured first. The
		// reload is skipped only when nothing about the selection changed —
		// clicking the row that is already shown does not re-read its detail.
		prevSel := hv.sel
		hv.sel = ordered[0].Hash
		cm := ordered[0]
		cm.Parents = append([]string(nil), cm.Parents...)
		hv.commit = cm
		if hv.mode != historySingleCommit || prevSel != cm.Hash {
			a.exitHistoryMode()
			hv.mode = historySingleCommit
			a.selectHistoryCommit(ordered[0])
		}
	case 2:
		// The default direction follows the list order, not the order the
		// rows were clicked in: the commit lower in the list (the older one
		// in a newest-first history) is the base, the one higher up the
		// target.
		base, target := ordered[1], ordered[0]
		lead := -1
		if hv.listSel >= 0 && hv.listSel < len(snap.Commits) {
			lead = hv.listSel
		}
		if lead >= 0 {
			hv.sel = snap.Commits[lead].Hash
			cm := snap.Commits[lead]
			cm.Parents = append([]string(nil), cm.Parents...)
			hv.commit = cm
		} else {
			hv.sel = target.Hash
			cm := target
			cm.Parents = append([]string(nil), cm.Parents...)
			hv.commit = cm
		}
		cmp := &hv.compare
		samePair := cmp.base.Hash != "" && cmp.target.Hash != "" &&
			((cmp.base.Hash == base.Hash && cmp.target.Hash == target.Hash) ||
				(cmp.base.Hash == target.Hash && cmp.target.Hash == base.Hash))
		if hv.mode != historyCompare || !samePair {
			a.exitHistoryMode()
			hv.mode = historyCompare
			hv.compare.base = base
			hv.compare.target = target
			a.startHistoryCompareLoad()
		}
	default:
		if hv.mode != historyMultiSelect {
			a.exitHistoryMode()
			hv.mode = historyMultiSelect
		}
		hv.sel = ""
		hv.commit = git.Commit{}
		if hv.listSel >= 0 && hv.listSel < len(snap.Commits) {
			hv.sel = snap.Commits[hv.listSel].Hash
			cm := snap.Commits[hv.listSel]
			cm.Parents = append([]string(nil), cm.Parents...)
			hv.commit = cm
		}
	}
}

// exitHistoryMode tears down the pane mode the selection is leaving: every
// in-flight read is invalidated and the detail, comparison and diff content
// is dropped, so nothing of it can surface under the new mode.
func (a *app) exitHistoryMode() {
	hv := &a.historyView
	a.session.cancelRead("history-detail")
	a.session.cancelRead("history-compare")
	hv.diff.Clear(a.session, "history-diff")
	hv.detail = git.CommitDetail{}
	hv.detailErr = nil
	hv.detailLoading = false
	hv.files = nil
	hv.fileSel = -1
	hv.compare = historyCompareState{fileSel: -1}
}

// startHistoryCompareLoad begins the asynchronous read of the files changed
// between the comparison's base and target under the "history-compare"
// scope. Only the hashes cross into the worker. Results of superseded reads
// are discarded by sessionRead itself; done additionally checks the state
// still compares the same commits in the same direction before applying, so
// a slow read cannot surface after the selection changed or the direction
// was swapped.
func (a *app) startHistoryCompareLoad() {
	hv := &a.historyView
	baseHash := hv.compare.base.Hash
	targetHash := hv.compare.target.Hash
	hv.compare.loading = true
	hv.compare.err = nil
	hv.compare.files = nil
	hv.compare.fileSel = -1
	hv.diff.Clear(a.session, "history-diff")
	started := sessionRead(a.session, "history-compare", func(repo *git.Repo) ([]git.FileChange, error) {
		return repo.RangeFiles(baseHash, targetHash)
	}, func(files []git.FileChange, err error) {
		if hv.compare.base.Hash != baseHash || hv.compare.target.Hash != targetHash {
			return
		}
		hv.compare.loading = false
		if err != nil {
			hv.compare.err = err
			hv.compare.files = nil
			hv.compare.fileSel = -1
			return
		}
		hv.compare.files = files
		hv.compare.err = nil
		if len(files) == 0 {
			hv.compare.fileSel = -1
			return
		}
		hv.compare.fileSel = 0
		hv.diff.Load(a.session, "history-diff", rangeFileDiffRequest(hv.compare.base, hv.compare.target, files[0]))
	})
	if !started {
		hv.compare.loading = false
	}
}

// swapComparisonDirection exchanges the base and target of the active
// comparison and reloads the file list and diff. The selection itself is not
// re-read: the swap is a pure inversion of the same pair.
func (a *app) swapComparisonDirection() {
	hv := &a.historyView
	if hv.mode != historyCompare {
		return
	}
	hv.compare.base, hv.compare.target = hv.compare.target, hv.compare.base
	a.startHistoryCompareLoad()
}

// historyCompareInfo renders the comparison header in the detail pane's left
// column: one row per commit with its short hash, author, date and subject —
// the base's hash in red, the target's in green — and the button that swaps
// the comparison direction.
func (a *app) historyCompareInfo(c *ui.Context, p palette) {
	hv := &a.historyView
	cmp := &hv.compare
	ui.Column(c.Key("history-compare-info")).Padding(10, 14).Gap(8).FillWidth().Shrink(0).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Comparing").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			ui.Spacer(c)
			b := ui.ButtonBase(c.Key("compare-swap")).
				Height(22).
				PaddingX(8).
				Radius(5).
				Center().
				Label("Swap Comparison Direction").
				FocusRing(false)
			if b.Hovered() {
				b.Background(p.hover)
			}
			b.Children(func() {
				ui.Text(c, "Swap").FontSize(11.5).FontWeight(500).TextColor(p.muted).SingleLine()
			})
			if b.Clicked() {
				a.swapComparisonDirection()
			}
		})
		a.historyCompareCommitRow(c, p, cmp.base, p.danger, "Comparison Base "+cmp.base.Short())
		a.historyCompareCommitRow(c, p, cmp.target, c.Theme().Success, "Comparison Target "+cmp.target.Short())
	})
}

// historyCompareCommitRow renders one commit of the comparison on a single
// row: its short hash in the given direction color, then subject, author and
// date. The row carries a label naming the comparison side so tests and
// assistive technology can tell the two rows apart.
func (a *app) historyCompareCommitRow(c *ui.Context, p palette, cm git.Commit, hashColor ui.Color, label string) {
	ui.Row(c.Key("compare-row-" + cm.Hash)).FillWidth().Gap(6).AlignItems(ui.Center).Label(label).Children(func() {
		ui.Text(c, cm.Short()).Font("monospace").FontSize(11).TextColor(hashColor).SingleLine().Shrink(0)
		ui.Text(c, cm.Subject).FontSize(11.5).FontWeight(500).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		ui.Text(c, cm.Author).FontSize(10.5).TextColor(p.faint).SingleLine().Width(64).Shrink(0).Ellipsis("…")
		ui.Text(c, historyRelDate(cm.Date)).FontSize(10.5).TextColor(p.faint).SingleLine().Width(72).Shrink(0)
	})
}

// historyMultiSelectInfo renders the placeholder for three or more chosen
// commits: how many are chosen and how to compare two of them.
func (a *app) historyMultiSelectInfo(c *ui.Context, p palette) {
	hv := &a.historyView
	ui.Column(c.Key("history-multi-info")).Padding(14).Gap(4).FillWidth().Shrink(0).Children(func() {
		ui.Text(c, fmt.Sprintf("%d commits selected", hv.selection.Len())).FontSize(12.5).FontWeight(600).TextColor(p.muted)
		ui.Text(c, "Select exactly two commits to compare them.").FontSize(11.5).TextColor(p.faint)
	})
}

// historyCompareFileList renders the files changed between the compared
// commits, styled as the single commit's changed-file list. Clicking a file
// loads its range diff into the shared diff panel.
func (a *app) historyCompareFileList(c *ui.Context, p palette) {
	hv := &a.historyView
	cmp := &hv.compare
	ui.Column(c.Key("history-compare-files")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("history-compare-files-header")).Height(26).FillWidth().PaddingX(14).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "Changed files").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			if len(cmp.files) > 0 {
				ui.Text(c, fmt.Sprint(len(cmp.files))).FontSize(11).TextColor(p.faint).SingleLine()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		switch {
		case cmp.loading:
			ui.Row(c.Key("compare-files-loading")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "Loading changed files…").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		case cmp.err != nil:
			ui.Column(c.Key("compare-files-error")).Padding(8, 14).Gap(6).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "Failed to load changed files").FontSize(11.5).FontWeight(600).TextColor(p.danger)
				ui.Text(c, cmp.err.Error()).FontSize(11).TextColor(p.muted)
				if ui.Button(c.Key("compare-files-retry"), "Retry").Label("Retry Comparison").Clicked() {
					a.startHistoryCompareLoad()
				}
			})
		case len(cmp.files) == 0:
			ui.Row(c.Key("compare-files-empty")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "No differences").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		default:
			ui.Scroll(c.Key("compare-files-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
				ui.Column(c).FillWidth().Padding(4, 0).Children(func() {
					for i, f := range cmp.files {
						a.historyCompareFileRow(c, p, i, f)
					}
				})
			})
		}
	})
}

// historyCompareFileRow renders one file of the comparison. Compare-mode
// file rows carry no context menu: the File History and Blame entries of the
// single-commit rows belong to one commit, not to a range.
func (a *app) historyCompareFileRow(c *ui.Context, p palette, i int, f git.FileChange) {
	hv := &a.historyView
	cmp := &hv.compare
	selected := cmp.fileSel == i
	b := ui.ButtonBase(c.Key("cmp-file-" + cmp.base.Hash + "-" + cmp.target.Hash + "-" + f.Path)).
		Height(24).
		FillWidth().
		PaddingX(14).
		Gap(7).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Role(ui.RoleListItem).
		Label("File " + f.Path).
		AccessSelected(selected).
		FocusRing(false)
	switch {
	case selected:
		b.Background(p.selected)
	case b.Hovered():
		b.Background(p.hover)
	}
	b.Children(func() {
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
	if b.Clicked() {
		if cmp.fileSel != i && cmp.base.Hash != "" {
			cmp.fileSel = i
			hv.diff.Load(a.session, "history-diff", rangeFileDiffRequest(cmp.base, cmp.target, f))
		}
	}
}
