package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// laneColors color the lanes of the commit graph, as v1's commits.go did.
var laneColors = []ui.Color{
	ui.Hex("#3b82f6"), ui.Hex("#ef4444"), ui.Hex("#10b981"), ui.Hex("#f59e0b"),
	ui.Hex("#8b5cf6"), ui.Hex("#ec4899"), ui.Hex("#06b6d4"), ui.Hex("#84cc16"),
}

func laneColor(i int) ui.Color {
	if i < 0 {
		i = 0
	}
	return laneColors[i%len(laneColors)]
}

const (
	historyLaneWidth = 12.0
	historyMaxLanes  = 12
	historyRowHeight = 24.0
	// historyGraphPad is how far past its row the graph lines reach so that
	// lines connect seamlessly across rows.
	historyGraphPad = 3.0
)

// historyDetailResult is the background-worker payload of one commit-detail
// read: the full commit metadata and the files it changes.
type historyDetailResult struct {
	detail git.CommitDetail
	files  []git.FileChange
}

// historyPaneMode is what the detail pane shows for the current selection:
// nothing, one commit's details, a two-commit comparison, or a placeholder
// for three or more selected commits.
type historyPaneMode uint8

const (
	historyNoSelection historyPaneMode = iota
	historySingleCommit
	historyCompare
	historyMultiSelect
)

// historyState holds the History page state on app. All fields are
// owned by the UI thread; background reads publish through sessionRead with
// the fixed scopes "history-detail", "history-compare" and "history-diff".
type historyState struct {
	// repoGen is the repository generation the state below belongs to. When
	// session.Generation() moves past it the page resets itself.
	repoGen uint64

	// sel is the hash of the chosen commit, "" for none. commit is a copy of
	// the chosen git.Commit captured on the UI thread (with cloned Parents),
	// passed to background reads so workers never read live UI state. With
	// several commits chosen, sel is the row the keys move from — the last
	// commit the user acted on.
	sel    string
	commit git.Commit

	detail        git.CommitDetail
	files         []git.FileChange
	detailLoading bool
	detailErr     error

	fileSel int
	// fileList is the scroll state of the commit's changed-file list; it
	// resets wherever fileSel does, so a newly chosen commit's list starts
	// at its first row.
	fileList ui.ListState

	// graphWidth caches the widest lane count of the snapshot's graph rows,
	// keyed on the rows slice itself, so a frame of an unchanged snapshot
	// never scans them again. The key self-invalidates when a load replaces
	// the slice, and a repository switch drops the entry with the rest of
	// the state.
	graphWidth graphWidthCache

	// treeMode groups the changed files in folders; it is the repository's
	// saved choice, read when the repository opens, independently of the
	// Local Changes view choice. The collapse state of the folder rows lives
	// in treeCollapsed keyed by the merged folder path, and treeCollapseRev
	// is the version the row derivation rebuilds on. None of it persists,
	// and treeCollapsed survives choosing another commit — paths repeat
	// across the commits of one repository — but not the repository switch.
	treeMode        bool
	treeCollapsed   map[string]bool
	treeCollapseRev uint64
	// treeRows is the cached derivation of the tree rows, valid for one
	// commit's files and one collapse revision.
	treeRows historyTreeCache
	// treeRowSel is the selected row of the tree, a row index rather than
	// the file index fileSel is in the flat list. treeFocus is the merged
	// path of the folder row the selection rests on, "" while it rests on
	// the file fileSel names; treeCommit is the commit the focus belongs
	// to, so choosing another commit drops it.
	treeRowSel int
	treeFocus  string
	treeCommit string

	// mode is the detail pane's current mode; selection holds every chosen
	// commit hash, as the list's multi-selection does. compare is the
	// two-commit comparison state, meaningful only in historyCompare mode.
	mode      historyPaneMode
	selection ui.Selection[string]
	compare   historyCompareState

	// selCache is the one-entry cache syncHistorySelection derives the
	// detail pane from: the chosen commits in list order, and the
	// hash-to-index table of the snapshot slice they were collected
	// against. It resets with the rest of the state on a repository
	// switch.
	selCache historySelectionCache

	diff diffPanel

	splitSize float32
	// listSel is the selected row of the commit list widget, kept in sync
	// with sel through the list's ListState.Key identity.
	listSel int
	list    ui.ListState
}

func (h *historyState) resetForRepo(gen uint64) {
	split := h.splitSize
	*h = historyState{repoGen: gen, splitSize: split, listSel: -1}
}

// graphWidthCache is the one-entry cache of the widest lane count of a
// commit graph's rows: the rows slice the width was scanned from, and the
// width. The snapshot's graph rows are regenerated whole on every load, so
// the identity of the slice is the identity of its contents: while the same
// backing array and length arrive, the scan cannot have a different answer.
type graphWidthCache struct {
	src   []git.GraphRow
	width int
	valid bool
}

// historyGraphWidth is the widest lane count of the graph rows, at least one
// so the graph column keeps room for the dot of a lane-less row. Scanning
// every row of a long history on every frame costs real time once Load More
// deepens the walk, so the answer is cached on the slice itself: a hit —
// the very slice a previous scan answered for — skips the scan, a miss
// scans and replaces the entry. Retaining the source in the cache keeps its
// backing array alive, so a new graph can never reuse its address.
func historyGraphWidth(c *graphWidthCache, rows []git.GraphRow) int {
	if c.valid && sameBackingSlice(c.src, rows) {
		return c.width
	}
	width := 1
	for _, r := range rows {
		width = max(width, r.Width)
	}
	c.src = rows
	c.width = width
	c.valid = true
	return width
}

// historyPage renders the History page: a branch-graph commit
// list with Load More pagination, commit detail with a changed-file list, and
// a shared diff panel for the chosen file.
func (a *app) historyPage(c *ui.Context) {
	p := a.pal
	top := a.pageTopInset(c)
	hv := &a.historyView

	if hv.splitSize <= 0 {
		hv.splitSize = 340
	}

	// Repository switches reset every selection and read of the previous
	// repository, before any state of it can be read below.
	if gen := a.session.Generation(); hv.repoGen != gen {
		a.session.cancelRead("history-detail")
		hv.diff.Clear(a.session, "history-diff")
		hv.resetForRepo(gen)
		if a.store != nil {
			if meta, ok := a.store.Get(a.session.Meta().ID); ok {
				hv.treeMode = meta.Local.Diff.HistoryFileListTree
			}
		}
	}

	c.OnWindowShortcut(ui.Cmd, ui.KeyF, a.openHistorySearch)

	ui.Column(c.Key("history-page")).Fill().Padding(top, 0, 0, 0).Children(func() {
		a.startMark(c)
		a.commitOperationBanner(c, p)
		switch {
		case a.session.LoadErr() != nil:
			placeholderCard(c, "Failed to load repository snapshot", a.session.LoadErr().Error(), "", p)
		case a.session.Loading() && !a.session.Loaded():
			placeholderCard(c, "Loading commit history…", a.session.Meta().Location(), "", p)
		case len(a.session.Snapshot().Commits) == 0:
			if a.session.HistoryCurrentBranch() {
				a.historyEmptyFilteredPlaceholder(c, p)
			} else {
				placeholderCard(c, "No commits yet", "This repository has no commits in its history.", a.session.Meta().Location(), p)
			}
		default:
			if a.fileInspectorActive(pageHistory) {
				a.fileInspectorView(c, p)
			} else if a.isHistorySearchOpen() {
				a.historySearchView(c, p)
			} else {
				ui.SplitVertical(c, &hv.splitSize, func() {
					a.historyCommitList(c, p)
				}, func() {
					a.historyDetailPane(c, p)
				}).Grow(1).FillWidth()
			}
		}
		a.commitActionModals(c)
	})
}

// historyFilterControls renders the two history filters at the right end of
// the History list header, ahead of the Search button: the branch scope
// drop-down and the first-parent check box, independent of one another. Both
// report their change into chooseHistoryFilters, which applies it for this
// opening and writes it to the repository's settings. Their values are
// derived fresh from the session on every frame, as the interactive rebase
// action selects derive theirs; a change reloads the history, and the answer
// shows the choice the session then holds.
func (a *app) historyFilterControls(c *ui.Context, p palette) {
	ui.Spacer(c)
	choice := "All Branches"
	if a.session.HistoryCurrentBranch() {
		choice = "Current Branch"
	}
	if ui.Select(c.Key("history-branch-filter"), &choice, []string{"All Branches", "Current Branch"}).
		Height(22).FontSize(11.5).
		Label("History Branches").
		Tooltip("Which branches the history shows").
		Changed() {
		a.chooseHistoryFilters(choice == "Current Branch", a.session.HistoryFirstParent())
	}
	firstParent := a.session.HistoryFirstParent()
	if ui.Checkbox(c.Key("history-first-parent"), &firstParent, "First Parent").
		Height(22).FontSize(11.5).
		Label("First Parent Only").
		Tooltip("Follow only the first parent of a merge").
		Changed() {
		a.chooseHistoryFilters(a.session.HistoryCurrentBranch(), firstParent)
	}
}

// chooseHistoryFilters applies the two history filter choices for this
// opening — the session reloads the history with them — and writes them to
// the repository's settings, preserving every other setting, as
// toggleChangesTree does for its file list. A failed write surfaces as the
// settings toast while the opening keeps the user's choice.
func (a *app) chooseHistoryFilters(currentBranch, firstParent bool) {
	a.session.SetHistoryFilters(currentBranch, firstParent)
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
	local.HistoryCurrentBranch = currentBranch
	local.HistoryFirstParent = firstParent
	if err := a.store.UpdateLocal(id, local); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
	}
}

// historyEmptyFilteredPlaceholder is the empty state of the history filtered
// to the current branch when HEAD reaches no commits — an empty repository
// or a not yet committed orphan branch. The header and its branch drop-down
// are not drawn without a commit list, so the placeholder carries the way
// back to the unfiltered history itself.
func (a *app) historyEmptyFilteredPlaceholder(c *ui.Context, p palette) {
	ui.Column(c).
		Grow(1).
		Center().
		Padding(24).
		Gap(10).
		Children(func() {
			ui.Text(c, "No commits on the current branch").FontSize(18).FontWeight(600).TextColor(p.text).TextAlign(ui.Center)
			ui.Text(c, "HEAD points at a branch without commits yet; the other branches' history is filtered out.").FontSize(13).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(480)
			if ui.Button(c.Key("history-show-all-branches"), "Show All Branches").Label("Show All Branches").Clicked() {
				a.chooseHistoryFilters(false, a.session.HistoryFirstParent())
			}
		})
}

// historyCommitList renders the virtualized list of commits: graph lane,
// ref badges, subject, author, short hash and date per row, with a Load More
// footer while the snapshot reports more commits. Rows are built only for
// the window in view, so a 1000-commit first page stays fast.
func (a *app) historyCommitList(c *ui.Context, p palette) {
	hv := &a.historyView
	snap := a.session.Snapshot()
	head := snap.Status.Head

	graphW := historyGraphWidth(&hv.graphWidth, snap.Graph)
	graphW = min(graphW, historyMaxLanes)
	graphColW := float32(graphW)*historyLaneWidth + 10

	hv.list.Key = func(row int) any {
		return snap.Commits[row].Hash
	}
	hv.list.Label = func(row int) string {
		cm := snap.Commits[row]
		return "Commit " + cm.Short() + " " + cm.Subject
	}
	hv.list.Selected = &hv.listSel
	// The list chooses several rows by their commit-hash keys: a click
	// chooses one row, Cmd-click adds a row or takes it out, Shift-click
	// chooses a range. MenuKeepsSelection keeps a right-click on a row from
	// changing the choice: the menu acts on the row it was opened for.
	hv.list.Selection = &hv.selection
	hv.list.MenuKeepsSelection = true

	ui.Column(c.Key("history-list")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("history-list-header")).Height(30).FillWidth().PaddingX(14).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "History").FontSize(15).FontWeight(700).TextColor(p.text).SingleLine()
			ui.Text(c, fmt.Sprintf("%d commits", len(snap.Commits))).FontSize(11.5).TextColor(p.faint).SingleLine()
			a.historyFilterControls(c, p)
			a.historySearchHeaderButton(c, p)
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline)

		// The list paints the chosen row through the theme accent; the local
		// theme narrows that accent to the macOS-style selection blue without
		// touching the page header, the Load More button or any other widget,
		// and Rows hands each row its selection state so the row can choose
		// readable text and badge colors over the filled background.
		listTheme := *c.Theme()
		listTheme.Accent = p.listSelected
		listTheme.AccentText = p.onAccent
		a.applyPendingHistoryReveal(snap)
		ui.Themed(c.Key("history-list-theme"), &listTheme).
			Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
			Children(func() {
				list := ui.List(c.Key("history-scroll"), &hv.list, len(snap.Commits)).
					Grow(1).FillWidth().MinHeight(0).Label("Commit History").
					Rows(func(r ui.ListRow) {
						a.historyCommitRow(r.Context, p, r.Index, r.Selected(), snap.Commits[r.Index], snap.Graph, head, graphColW)
					})
				if list.Changed() {
					// The user chose rows herself: a pending Show in History
					// reveal must not take the selection back. The selection
					// change itself is picked up by syncHistorySelection
					// below, which reacts to the chosen rows no matter how
					// they changed.
					a.cancelPendingHistoryReveal()
				}
			})
		// Rows can change without any user input — a snapshot reload dropping
		// an amended or rebased-away commit, Load More, a repository switch —
		// so the detail pane is derived from the chosen rows on every frame,
		// not only when the user clicks.
		a.syncHistorySelection(snap)

		if snap.More {
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
			a.historyLoadMoreRow(c, p)
		}
	})
}

// historyCommitRow renders one commit row inside the virtual list. Row
// clicks and selection painting are the list's own concern (ListState); the
// row only draws the commit, in colors readable over the filled selection
// background when selected tells it the row is the chosen one.
func (a *app) historyCommitRow(c *ui.Context, p palette, i int, selected bool, cm git.Commit, graph []git.GraphRow, head string, graphColW float32) {
	subjectColor, metaColor := p.text, p.muted
	rowBg, halo := p.bg, ui.Transparent
	if selected {
		subjectColor, metaColor = p.onAccent, p.onAccentMuted
		rowBg, halo = p.listSelected, p.onAccent
		if !c.WindowActive() {
			// The list fades the selection fill to a neutral gray in an
			// inactive window, as mygo's inactiveAccent does; the hollow HEAD
			// dot must match whatever the row is actually filled with.
			rowBg = ui.Hex("#737373")
		}
	}
	row := ui.Row(c).
		Height(historyRowHeight).
		FillWidth().
		PaddingX(6).
		Gap(10).
		AlignItems(ui.Center).
		Children(func() {
			var row git.GraphRow
			if i < len(graph) {
				row = graph[i]
			}
			isHead := cm.Hash == head && head != ""
			ui.Box(c.Key("graph")).Width(graphColW).Height(historyRowHeight - 4).Shrink(0).Draw(func(pt *ui.Painter, r ui.Rect) {
				drawHistoryGraph(pt, r, row, historyGraphPad, isHead, rowBg, halo)
			})

			ui.Row(c).Gap(5).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				historyRefBadges(c, cm.Refs, p, selected)
				weight := 400
				if isHead {
					weight = 600
				}
				ui.Text(c, cm.Subject).FontSize(12.5).FontWeight(weight).TextColor(subjectColor).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			})
			ui.Text(c, cm.Author).FontSize(11.5).TextColor(metaColor).SingleLine().Width(110).Shrink(0).Ellipsis("…")
			ui.Text(c, cm.Short()).Font("monospace").FontSize(11).TextColor(metaColor).SingleLine().Width(64).Shrink(0)
			ui.Text(c, historyRelDate(cm.Date)).FontSize(11.5).TextColor(metaColor).SingleLine().Width(104).Shrink(0)
		})
	row.ContextMenu(func(m *ui.Menu) {
		a.commitContextMenu(m, cm)
	})
}

// historyLoadMoreRow renders the bottom-of-list pagination control. It is
// disabled while a snapshot read or a write is in flight, so repeated clicks
// cannot accumulate logLimit while busy.
func (a *app) historyLoadMoreRow(c *ui.Context, p palette) {
	busy := a.session.Loading() || a.session.Busy()
	ui.Row(c.Key("load-more-row")).FillWidth().Padding(6, 14).AlignItems(ui.Center).Children(func() {
		b := ui.ButtonBase(c.Key("load-more")).
			Height(26).
			PaddingX(12).
			Radius(6).
			Center().
			Label("Load More").
			Disabled(busy).
			FocusRing(false).
			Border(1, p.border)
		switch {
		case busy:
			b.Background(p.hover)
		case b.Hovered():
			b.Background(p.hover)
		default:
			b.Background(p.surface)
		}
		fg := p.text
		if busy {
			fg = p.faint
		}
		b.Children(func() {
			label := "Load More"
			if a.session.Loading() {
				label = "Loading…"
			}
			ui.Text(c, label).FontSize(12).FontWeight(500).TextColor(fg).SingleLine()
		})
		if b.Clicked() {
			a.session.LoadMore(0)
		}
	})
}

// selectHistoryCommit chooses a commit and starts the asynchronous detail
// read. The commit is copied on the UI thread with its parents cloned so the
// background worker reads no live UI state. Choosing one commit narrows a
// wider selection to it, which is what a Show in History reveal needs to
// leave a comparison behind; the user-driven narrowing already happened in
// the list itself, so the writes below are no-ops then.
func (a *app) selectHistoryCommit(cm git.Commit) {
	hv := &a.historyView
	hv.sel = cm.Hash
	cm.Parents = append([]string(nil), cm.Parents...)
	hv.commit = cm
	if hv.selection.Len() != 1 || !hv.selection.Has(cm.Hash) {
		hv.selection.Clear()
		hv.selection.Add(cm.Hash)
	}
	hv.detailLoading = true
	// Drop the previous commit's detail, error, file list and file selection
	// at once: while the new detail read is in flight the pane must not show
	// the old commit's files or failure.
	hv.detail = git.CommitDetail{}
	hv.detailErr = nil
	hv.files = nil
	hv.fileSel = -1
	hv.fileList = ui.ListState{}
	hv.diff.Clear(a.session, "history-diff")
	a.startHistoryDetailLoad(cm)
}

// startHistoryDetailLoad begins the asynchronous read of the commit's detail
// and changed-file list under the fixed "history-detail" scope. Results of
// superseded reads are discarded by sessionRead itself; done additionally
// checks the selection still points at the same commit before applying.
func (a *app) startHistoryDetailLoad(cm git.Commit) {
	hv := &a.historyView
	hash := cm.Hash
	sessionRead(a.session, "history-detail", func(repo *git.Repo) (historyDetailResult, error) {
		d, err := repo.Detail(cm.Hash)
		if err != nil {
			return historyDetailResult{}, err
		}
		files, err := repo.CommitFiles(cm)
		if err != nil {
			return historyDetailResult{}, err
		}
		return historyDetailResult{detail: d, files: files}, nil
	}, func(res historyDetailResult, err error) {
		if hv.sel != hash {
			return
		}
		hv.detailLoading = false
		if err != nil {
			hv.detailErr = err
			hv.files = nil
			hv.detail = git.CommitDetail{}
			hv.fileSel = -1
			hv.fileList = ui.ListState{}
			hv.diff.Clear(a.session, "history-diff")
			return
		}
		hv.detail = res.detail
		hv.files = res.files
		hv.detailErr = nil
		if len(res.files) == 0 {
			hv.fileSel = -1
			hv.fileList = ui.ListState{}
			hv.diff.Clear(a.session, "history-diff")
			return
		}
		hv.fileSel = 0
		hv.fileList = ui.ListState{}
		hv.diff.Load(a.session, "history-diff", commitFileDiffRequest(hv.commit, res.files[0]))
	})
}

// historyDetailPane renders the bottom split: commit metadata plus the list
// of changed files on the left, and the shared diff panel on the right. With
// two commits chosen the left column carries the comparison header and the
// files changed between them; with three or more it shows a placeholder.
func (a *app) historyDetailPane(c *ui.Context, p palette) {
	hv := &a.historyView
	ui.Row(c.Key("history-detail-pane")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("history-detail-left")).Width(320).FillHeight().Shrink(0).Children(func() {
			switch hv.mode {
			case historyCompare:
				a.historyCompareInfo(c, p)
				ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
				a.historyCompareFileList(c, p)
			case historyMultiSelect:
				a.historyMultiSelectInfo(c, p)
			default:
				a.historyCommitInfo(c, p)
				ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
				a.historyFileList(c, p)
			}
		})
		ui.Box(c).Width(1).FillHeight().Background(p.hairline).Shrink(0)
		ui.Box(c.Key("history-detail-right")).Grow(1).FillHeight().MinWidth(0).Children(func() {
			hv.diff.View(c, a.session, "history-diff", p)
		})
	})
}

func (a *app) historyCommitInfo(c *ui.Context, p palette) {
	hv := &a.historyView
	switch {
	case hv.sel == "":
		ui.Column(c.Key("history-info-empty")).Padding(14).Gap(4).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "No commit selected").FontSize(12.5).FontWeight(600).TextColor(p.muted)
			ui.Text(c, "Choose a commit in the list above to see its details.").FontSize(11.5).TextColor(p.faint)
		})
	case hv.detailLoading:
		ui.Column(c.Key("history-info-loading")).Padding(14).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "Loading commit details…").FontSize(12.5).TextColor(p.muted)
		})
	case hv.detailErr != nil:
		ui.Column(c.Key("history-info-error")).Padding(14).Gap(6).FillWidth().Shrink(0).Children(func() {
			ui.Text(c, "Failed to load commit details").FontSize(12.5).FontWeight(600).TextColor(p.danger)
			ui.Text(c, hv.detailErr.Error()).FontSize(11.5).TextColor(p.muted)
			if ui.Button(c.Key("history-detail-retry"), "Retry").Label("Retry Commit Details").Clicked() {
				hv.detailErr = nil
				hv.detailLoading = true
				a.startHistoryDetailLoad(hv.commit)
			}
		})
	default:
		d := hv.detail
		ui.Scroll(c.Key("history-info-scroll")).FillWidth().MaxHeight(190).Shrink(0).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(7).FillWidth().Children(func() {
				ui.Text(c, firstLineOf(d.Message)).FontSize(12.5).FontWeight(600).TextColor(p.text)
				if body := restOfMessage(d.Message); body != "" {
					ui.Text(c, body).FontSize(11.5).TextColor(p.muted)
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Commit").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Hash).Font("monospace").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Author").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Author+" <"+d.AuthorEmail+">").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Date").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
					ui.Text(c, d.Date.Format("Jan 2, 2006 15:04")).FontSize(11).TextColor(p.muted).SingleLine()
				})
				if len(d.Parents) > 0 {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "Parents").FontSize(11).TextColor(p.faint).SingleLine().Width(52).Shrink(0)
						var shorts []string
						for _, par := range d.Parents {
							if len(par) > 7 {
								par = par[:7]
							}
							shorts = append(shorts, par)
						}
						ui.Text(c, strings.Join(shorts, " ")).Font("monospace").FontSize(11).TextColor(p.muted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					})
				}
			})
		})
	}
}

// historyFileList renders the files the selected commit changes. Clicking a
// file loads its diff into the shared diff panel. The header carries the
// icon button switching the list between the flat list and the tree, its
// label the view a click turns on.
func (a *app) historyFileList(c *ui.Context, p palette) {
	hv := &a.historyView
	ui.Column(c.Key("history-files")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("history-files-header")).Height(26).FillWidth().PaddingX(14).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, "Changed files").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine()
			if len(hv.files) > 0 {
				ui.Text(c, fmt.Sprint(len(hv.files))).FontSize(11).TextColor(p.faint).SingleLine()
			}
			ui.Spacer(c)
			label := "Show as Tree"
			icon, fb := "list.bullet.indent", fallbackTreeList
			if hv.treeMode {
				label = "Show as List"
				icon, fb = "list.bullet", fallbackFlatList
			}
			if iconBtn(c.Key("history-files-view-toggle"), icon, fb, 13, p.muted, label, p).Clicked() {
				a.toggleHistoryTree()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		switch {
		case hv.sel == "" || hv.detailLoading || hv.detailErr != nil:
			// Nothing to list yet; the info section above reports the state.
		case len(hv.files) == 0:
			ui.Row(c.Key("history-files-empty")).Padding(8, 14).FillWidth().Shrink(0).Children(func() {
				ui.Text(c, "No files changed").FontSize(11.5).TextColor(p.faint).SingleLine()
			})
		case hv.treeMode:
			a.historyFileTree(c.Key("history-files"), p)
		default:
			changedFileList(c.Key("history-files"), &hv.fileList, hv.files, p, &hv.fileSel,
				func(m *ui.Menu, f git.FileChange) {
					a.historyFileContextMenu(m, hv.commit, f)
				},
				func(i int, f git.FileChange) {
					if hv.sel != "" {
						hv.diff.Load(a.session, "history-diff", commitFileDiffRequest(hv.commit, f))
					}
				})
		}
	})
}

func historyStatusColor(p palette, s byte) ui.Color {
	switch s {
	case 'A', '?':
		if p.dark {
			return ui.Hex("#3fb950")
		}
		return ui.Hex("#1a7f37")
	case 'D':
		return p.danger
	case 'R', 'C':
		return ui.Hex("#8b5cf6")
	}
	return p.accent
}

// drawHistoryGraph draws one row of the commit graph in r: the lane lines
// passing through it, the lines merging into or branching out of the commit,
// and the commit's dot — hollow for HEAD. Lines reach pad past the row's top
// and bottom so rows connect seamlessly. On a selected row halo is the 1pt
// light ring drawn under the dot so a blue lane dot stays visible against
// the blue selection fill, and rowBg fills the hollow HEAD center with the
// row's own background; unselected rows pass a transparent halo and the page
// background, which leaves them exactly as before.
func drawHistoryGraph(p *ui.Painter, r ui.Rect, g git.GraphRow, pad float32, head bool, rowBg ui.Color, halo ui.Color) {
	x := func(lane int) float32 { return r.X + 5 + float32(lane)*historyLaneWidth + historyLaneWidth/2 }
	top, bottom, mid := r.Y-pad, r.Y+r.H+pad, r.Y+r.H/2
	for _, s := range g.Segments {
		if s.From >= historyMaxLanes || s.To >= historyMaxLanes {
			continue
		}
		col := laneColor(s.Color)
		switch s.Kind {
		case git.SegmentFull:
			p.Line(x(s.From), top, x(s.To), bottom, 1.5, col)
		case git.SegmentTop:
			if s.From == g.Lane {
				p.Line(x(s.From), top, x(g.Lane), mid, 1.5, col)
			} else {
				p.StrokePolyline([][2]float32{{x(s.From), top}, {x(s.From), mid - historyLaneWidth/2}, {x(g.Lane), mid}}, 1.5, col)
			}
		case git.SegmentBottom:
			if s.To == g.Lane {
				p.Line(x(g.Lane), mid, x(s.To), bottom, 1.5, col)
			} else {
				p.StrokePolyline([][2]float32{{x(g.Lane), mid}, {x(s.To), mid + historyLaneWidth/2}, {x(s.To), bottom}}, 1.5, col)
			}
		}
	}
	if g.Lane >= historyMaxLanes {
		return
	}
	cx, d := x(g.Lane), float32(7)
	col := laneColor(g.Color)
	if halo.A != 0 {
		ring := d + 2
		p.Fill(ui.Rect{X: cx - ring/2, Y: mid - ring/2, W: ring, H: ring}, halo, ring/2)
	}
	p.Fill(ui.Rect{X: cx - d/2, Y: mid - d/2, W: d, H: d}, col, d/2)
	if head {
		p.Fill(ui.Rect{X: cx - 1.8, Y: mid - 1.8, W: 3.6, H: 3.6}, rowBg, 1.8)
	}
}

// historyRefBadges renders the branch, remote branch, tag and HEAD badges of
// one commit, distinguishing the four kinds by color. Unselected rows keep
// the original translucent tints; on a selected row those tints would sit on
// the filled selection blue and become unreadable or invisible, so the
// badges switch to high-contrast pills: light frosted pills with deep
// semantic text in the light theme, deep semantic pills with white text in
// the dark theme.
func historyRefBadges(c *ui.Context, refs []git.Ref, p palette, selected bool) {
	for _, r := range refs {
		if r.Name == "" {
			continue
		}
		var bg, fg ui.Color
		label := r.Name
		isBranch := false
		switch r.Kind {
		case git.RefTag:
			bg = ui.Hex("#8b5cf6").Alpha(0.16)
			fg = ui.Hex("#8b5cf6")
		case git.RefRemote:
			bg = p.danger.Alpha(0.13)
			fg = p.danger
		case git.RefHead:
			// Detached HEAD decorates the commit as plain "HEAD" — always
			// with Current set, because %D never tells the two apart; a
			// branch-pointing HEAD arrives as "HEAD -> branch" and lands in
			// RefBranch instead, so nothing here is ever redundant.
			bg = p.accent.Alpha(0.16)
			fg = p.accent
			label = "HEAD"
			isBranch = true
		default: // RefBranch
			bg = p.accent.Alpha(0.16)
			fg = p.accent
			if r.Current {
				label = "HEAD → " + r.Name
			}
			isBranch = true
		}
		if selected {
			bg, fg = selectedRefBadgeColors(p, r.Kind, isBranch)
		}
		ui.Box(c).Padding(1, 5).Radius(4).Background(bg).Shrink(0).Children(func() {
			ui.Text(c, label).FontSize(10).FontWeight(600).TextColor(fg).SingleLine()
		})
	}
}

// selectedRefBadgeColors picks the pill and text colors of a badge on a
// selected commit row: light frosted pills with deep semantic text in the
// light theme, deep semantic pills with white text in the dark theme, the
// three semantic hues (branch blue, remote red, tag purple) still distinct
// at a glance.
func selectedRefBadgeColors(p palette, kind git.RefKind, isBranch bool) (bg, fg ui.Color) {
	switch {
	case !p.dark:
		switch {
		case isBranch:
			return ui.RGBA(255, 255, 255, 0.92), ui.Hex("#1756ad")
		case kind == git.RefRemote:
			return ui.RGBA(254, 242, 242, 0.94), ui.Hex("#b42327")
		default: // RefTag
			return ui.RGBA(245, 243, 255, 0.94), ui.Hex("#6d28d9")
		}
	default:
		switch {
		case isBranch:
			return ui.RGBA(10, 22, 48, 0.42), p.onAccent
		case kind == git.RefRemote:
			return ui.Hex("#b42327"), p.onAccent
		default: // RefTag
			return ui.Hex("#6d28d9"), p.onAccent
		}
	}
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func restOfMessage(s string) string {
	_, rest, ok := strings.Cut(s, "\n")
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}

// historyRelDate formats a commit time compactly for the commit list.
func historyRelDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("Jan 2, 2006")
}
