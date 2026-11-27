package app

import (
	"strconv"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// Scopes of the file inspector's scoped background reads. They are fixed so
// a read of one kind never discards an in-flight read of another, while two
// reads of the same kind supersede each other.
const (
	fileHistoryLogScope  = "file-history-log"
	fileHistoryDiffScope = "file-history-diff"
	fileBlameScope       = "file-blame"
)

// Inspector modes. The zero value is File History.
const (
	inspectorModeHistory = iota
	inspectorModeBlame
)

const fileHistoryPageSize = 100

// fileInspectorState is the UI state of one app instance's file inspector:
// the unified File History and Blame viewer opened from a file's context
// menu. All fields are owned by the UI thread; background reads publish
// through sessionRead with the fixed scopes above.
type fileInspectorState struct {
	// repoGen is the repository generation the state below belongs to. When
	// session.Generation() moves past it the inspector closes itself.
	repoGen uint64

	open       bool
	originPage pageKind
	mode       int
	path       string
	blameRev   string

	// File History fields. histCommits is nil until the first read applies,
	// distinguishing "not yet loaded" from a legitimately empty result.
	histLimit   int
	histCommits []git.Commit
	histLoading bool
	histErr     error
	histSel     int
	histList    ui.ListState
	diff        diffPanel
	splitSize   float32

	// Blame fields. blameLines follows the same nil-until-loaded convention.
	blameLines   []git.BlameLine
	blameLoading bool
	blameErr     error
	blameSel     int
	blameList    ui.ListState
}

func (a *app) fileInspectorUI() *fileInspectorState {
	return featureState(a, func() *fileInspectorState {
		return &fileInspectorState{histSel: -1, blameSel: -1}
	})
}

// fileInspectorActive reports whether the inspector covers the given page:
// it renders only on the page it was opened from. A repository generation
// change closes it wherever it is, so no state of the previous repository
// can survive into the next one.
func (a *app) fileInspectorActive(page pageKind) bool {
	st := a.fileInspectorUI()
	if !st.open {
		return false
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		a.closeFileInspector()
		return false
	}
	return st.originPage == page
}

// closeFileInspector cancels every in-flight inspector read and resets the
// inspector, keeping the user's split position and diff layout choice for
// the next time it opens.
func (a *app) closeFileInspector() {
	st := a.fileInspectorUI()
	a.session.cancelRead(fileHistoryLogScope)
	a.session.cancelRead(fileBlameScope)
	st.diff.Clear(a.session, fileHistoryDiffScope)
	layout := st.diff.Layout()
	split := st.splitSize
	repoGen := st.repoGen
	*st = fileInspectorState{
		repoGen:   repoGen,
		histSel:   -1,
		blameSel:  -1,
		splitSize: split,
	}
	st.diff.SetLayout(layout)
}

// openFileInspector opens the inspector for path in mode on the current
// page, replacing any inspector already open, and starts its first read.
func (a *app) openFileInspector(page pageKind, mode int, path, blameRev string) {
	a.closeFileInspector()
	st := a.fileInspectorUI()
	st.repoGen = a.session.Generation()
	st.open = true
	st.originPage = page
	st.mode = mode
	st.path = path
	st.blameRev = blameRev
	a.fileInspectorModeEntered()
}

// fileInspectorModeEntered starts the read of the mode the inspector just
// switched to when its data is missing. Data already loaded stays, so
// switching back and forth between modes does not re-read Git.
func (a *app) fileInspectorModeEntered() {
	st := a.fileInspectorUI()
	if !st.open {
		return
	}
	switch st.mode {
	case inspectorModeBlame:
		if st.blameLines == nil && !st.blameLoading && st.blameErr == nil {
			a.startFileBlameLoad()
		}
	default:
		if st.histCommits == nil && !st.histLoading && st.histErr == nil {
			a.startFileHistoryLogLoad()
		}
	}
}

// startFileHistoryLogLoad begins the asynchronous read of the commits that
// changed the inspected file, walking HEAD up to st.histLimit commits.
func (a *app) startFileHistoryLogLoad() {
	st := a.fileInspectorUI()
	if !st.open || !a.session.HasRepo() {
		return
	}
	if st.histLimit < fileHistoryPageSize {
		st.histLimit = fileHistoryPageSize
	}
	path, limit := st.path, st.histLimit
	st.histLoading = true
	started := sessionRead(a.session, fileHistoryLogScope, func(repo *git.Repo) ([]git.Commit, error) {
		return repo.FileLog(path, limit)
	}, func(commits []git.Commit, err error) {
		if !st.open || st.path != path || st.mode != inspectorModeHistory {
			// The freshest read no longer matches what the inspector shows:
			// the mode moved on without starting another read of this scope,
			// so nothing is in flight and the next entry must start over.
			st.histLoading = false
			return
		}
		st.histLoading = false
		if err != nil {
			st.histErr = err
			st.histCommits = nil
			return
		}
		st.histErr = nil
		if commits == nil {
			commits = []git.Commit{}
		}
		st.histCommits = commits
		if len(commits) == 0 {
			st.histSel = -1
			st.diff.Clear(a.session, fileHistoryDiffScope)
			return
		}
		if st.histSel < 0 || st.histSel >= len(commits) {
			st.histSel = 0
		}
		cm := commits[st.histSel]
		cm.Parents = append([]string(nil), cm.Parents...)
		st.diff.Load(a.session, fileHistoryDiffScope, commitFileDiffRequest(cm, git.FileChange{Path: path}))
	})
	if !started {
		st.histLoading = false
	}
}

// startFileBlameLoad begins the asynchronous read of the line-by-line blame
// of the inspected file at st.blameRev.
func (a *app) startFileBlameLoad() {
	st := a.fileInspectorUI()
	if !st.open || !a.session.HasRepo() {
		return
	}
	path, rev := st.path, st.blameRev
	st.blameLoading = true
	started := sessionRead(a.session, fileBlameScope, func(repo *git.Repo) ([]git.BlameLine, error) {
		return repo.BlameLines(path, rev)
	}, func(lines []git.BlameLine, err error) {
		if !st.open || st.path != path || st.blameRev != rev || st.mode != inspectorModeBlame {
			st.blameLoading = false
			return
		}
		st.blameLoading = false
		if err != nil {
			st.blameErr = err
			st.blameLines = nil
			return
		}
		st.blameErr = nil
		if lines == nil {
			lines = []git.BlameLine{}
		}
		st.blameLines = lines
		if len(lines) == 0 {
			st.blameSel = -1
		}
	})
	if !started {
		st.blameLoading = false
	}
}

func (a *app) retryFileHistoryLoad() {
	st := a.fileInspectorUI()
	st.histErr = nil
	st.histCommits = nil
	a.startFileHistoryLogLoad()
}

func (a *app) retryFileBlameLoad() {
	st := a.fileInspectorUI()
	st.blameErr = nil
	st.blameLines = nil
	a.startFileBlameLoad()
}

// historyFileContextMenu is the context menu of a changed-file row in the
// commit detail of the History page: File History walks HEAD for the file,
// Blame pins the file at the shown commit, falling back to its first parent
// when the commit deleted the file, and the two reset entries restore the
// file in the working tree and the index to the state at or before the
// commit.
func (a *app) historyFileContextMenu(m *ui.Menu, commit git.Commit, f git.FileChange) {
	// Copy the commit and clone its parents at once: the menu's function runs
	// again when an item is chosen, and must never read the mutable commit
	// the detail pane may have moved on from.
	commit.Parents = append([]string(nil), commit.Parents...)
	blameRev, blameOK := commit.Hash, true
	if f.Status == 'D' {
		if len(commit.Parents) > 0 {
			blameRev = commit.Parents[0]
		} else {
			blameOK = false
		}
	}
	path := f.Path
	if m.Item("File History…").ID("file-history").Chosen() {
		a.openFileInspector(pageHistory, inspectorModeHistory, path, blameRev)
	}
	if m.Item("Blame…").ID("file-blame").Disabled(!blameOK).Chosen() {
		a.openFileInspector(pageHistory, inspectorModeBlame, path, blameRev)
	}
	a.historyFileResetMenuItems(m, commit, f)
}

// changesFileContextMenu is the context menu of a file row of the Local
// Changes page. Files that never entered a commit — untracked, ignored or
// newly added ones, and every file before the first commit — have neither
// history nor a revision to blame at, so both entries are disabled for them.
func (a *app) changesFileContextMenu(m *ui.Menu, f git.FileChange) {
	unborn := a.session.Snapshot().Status.Head == ""
	noHistory := unborn || f.Status == '?' || f.Status == '!' || f.Status == 'A'
	path := f.Path
	if f.Status == 'R' && f.OrigPath != "" {
		path = f.OrigPath
	}
	if m.Item("File History…").ID("file-history").Disabled(noHistory).Chosen() {
		a.openFileInspector(pageChanges, inspectorModeHistory, path, "HEAD")
	}
	if m.Item("Blame…").ID("file-blame").Disabled(noHistory).Chosen() {
		a.openFileInspector(pageChanges, inspectorModeBlame, path, "HEAD")
	}
	a.remoteEditMenuItems(m, f)
}

// fileInspectorView renders the open inspector in place of the origin page's
// split: a top bar with Close, the inspected path, the blame revision badge
// and the mode switch, over the active mode's view. Esc closes the inspector
// back to the page it came from.
func (a *app) fileInspectorView(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	if st.splitSize <= 0 {
		st.splitSize = 320
	}

	c.OnWindowShortcut(0, ui.KeyEscape, a.closeFileInspector)

	ui.Column(c.Key("file-inspector")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Row(c.Key("file-inspector-bar")).Height(36).FillWidth().PaddingX(12).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			if ui.Button(c.Key("file-inspector-close"), "Close").Label("Close File Inspector").Clicked() {
				a.closeFileInspector()
			}
			ui.Text(c, st.path).FontSize(12.5).FontWeight(600).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			badge := st.blameRev
			if badge != "HEAD" {
				badge = shortChangesHash(badge)
			}
			ui.Box(c).Padding(2, 6).Radius(5).Background(p.hover).Shrink(0).Children(func() {
				ui.Text(c, badge).Font("monospace").FontSize(11).FontWeight(500).TextColor(p.muted).SingleLine()
			})
			ui.Spacer(c)
			seg := ui.Segmented(c.Key("file-inspector-mode"), &st.mode, "File History", "Blame").Label("Inspector Mode")
			if seg.Changed() {
				a.fileInspectorModeEntered()
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)

		if !st.open {
			// The close click lands mid-frame; the page the inspector came
			// from renders again next frame, so the body draws nothing now.
			return
		}
		switch st.mode {
		case inspectorModeBlame:
			a.fileBlameModeView(c, p)
		default:
			a.fileHistoryModeView(c, p)
		}
	})
}

// fileHistoryModeView is the File History mode: the commits that changed the
// file above, the diff of the chosen commit in the shared diff panel below.
func (a *app) fileHistoryModeView(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	ui.SplitVertical(c, &st.splitSize, func() {
		a.fileHistoryListPane(c, p)
	}, func() {
		st.diff.View(c, a.session, fileHistoryDiffScope, p)
	}).Grow(1).FillWidth().MinHeight(0)
}

func (a *app) fileHistoryListPane(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	switch {
	case st.histLoading && st.histCommits == nil:
		placeholderCard(c, "Loading file history…", st.path, "", p)
	case st.histErr != nil:
		ui.Column(c.Key("file-history-error")).Grow(1).FillWidth().Center().Padding(24).Gap(8).Children(func() {
			ui.Text(c, "Failed to load file history").FontSize(16).FontWeight(600).TextColor(p.danger).TextAlign(ui.Center)
			ui.Text(c, st.histErr.Error()).FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(520)
			if ui.Button(c.Key("file-history-retry"), "Retry").Label("Retry File History").Clicked() {
				a.retryFileHistoryLoad()
			}
		})
	case st.histCommits != nil && len(st.histCommits) == 0:
		placeholderCard(c, "No commits changed this file", st.path, "", p)
	default:
		a.fileHistoryList(c, p)
	}
}

// fileHistoryList renders the virtualized commit list of the inspected file
// with its Load More footer. The list paints the chosen row through the
// theme accent exactly like the commit list of the History page.
func (a *app) fileHistoryList(c *ui.Context, p palette) {
	st := a.fileInspectorUI()

	st.histList.Key = func(row int) any {
		return st.histCommits[row].Hash
	}
	st.histList.Label = func(row int) string {
		cm := st.histCommits[row]
		return "File Commit " + cm.Short() + " " + cm.Subject
	}
	st.histList.Selected = &st.histSel

	ui.Column(c.Key("file-history-list")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		listTheme := *c.Theme()
		listTheme.Accent = p.listSelected
		listTheme.AccentText = p.onAccent
		ui.Themed(c.Key("file-history-list-theme"), &listTheme).
			Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
			Children(func() {
				list := ui.List(c.Key("file-history-scroll"), &st.histList, len(st.histCommits)).
					Grow(1).FillWidth().MinHeight(0).Label("File History").
					Rows(func(r ui.ListRow) {
						a.fileHistoryCommitRow(r.Context, p, r.Selected(), st.histCommits[r.Index])
					})
				if list.Changed() && st.histSel >= 0 && st.histSel < len(st.histCommits) {
					cm := st.histCommits[st.histSel]
					cm.Parents = append([]string(nil), cm.Parents...)
					st.diff.Load(a.session, fileHistoryDiffScope, commitFileDiffRequest(cm, git.FileChange{Path: st.path}))
				}
			})
		if len(st.histCommits) >= st.histLimit {
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
			a.fileHistoryLoadMoreRow(c, p)
		}
	})
}

// fileHistoryCommitRow draws one commit of the file's history: subject,
// author, short hash and date, in colors readable over the filled selection
// background when the row is the chosen one.
func (a *app) fileHistoryCommitRow(c *ui.Context, p palette, selected bool, cm git.Commit) {
	subjectColor, metaColor := p.text, p.muted
	if selected {
		subjectColor, metaColor = p.onAccent, p.onAccentMuted
	}
	ui.Row(c).
		Height(historyRowHeight).
		FillWidth().
		PaddingX(12).
		Gap(10).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, cm.Subject).FontSize(12.5).TextColor(subjectColor).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).Grow(1)
			ui.Text(c, cm.Author).FontSize(11.5).TextColor(metaColor).SingleLine().Width(110).Shrink(0).Ellipsis("…")
			ui.Text(c, cm.Short()).Font("monospace").FontSize(11).TextColor(metaColor).SingleLine().Width(64).Shrink(0)
			ui.Text(c, historyRelDate(cm.Date)).FontSize(11.5).TextColor(metaColor).SingleLine().Width(104).Shrink(0)
		})
}

// fileHistoryLoadMoreRow renders the pagination control growing the file's
// commit walk by one page. It is disabled while a read is in flight, so
// repeated clicks cannot accumulate histLimit while busy.
func (a *app) fileHistoryLoadMoreRow(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	busy := st.histLoading
	ui.Row(c.Key("file-history-more-row")).FillWidth().Padding(6, 14).AlignItems(ui.Center).Children(func() {
		b := ui.ButtonBase(c.Key("file-history-load-more")).
			Height(26).
			PaddingX(12).
			Radius(6).
			Center().
			Label("Load More File History").
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
			label := "Load More File History"
			if busy {
				label = "Loading…"
			}
			ui.Text(c, label).FontSize(12).FontWeight(500).TextColor(fg).SingleLine()
		})
		if b.Clicked() {
			st.histLimit += fileHistoryPageSize
			a.startFileHistoryLogLoad()
		}
	})
}

// fileBlameModeView is the Blame mode: the lines of the file with the commit
// that last changed each, over a bar describing the chosen line's commit.
func (a *app) fileBlameModeView(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	switch {
	case st.blameLoading && st.blameLines == nil:
		placeholderCard(c, "Loading blame…", st.path, "", p)
	case st.blameErr != nil:
		ui.Column(c.Key("file-blame-error")).Grow(1).FillWidth().Center().Padding(24).Gap(8).Children(func() {
			ui.Text(c, "Failed to load blame").FontSize(16).FontWeight(600).TextColor(p.danger).TextAlign(ui.Center)
			ui.Text(c, st.blameErr.Error()).FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(520)
			if ui.Button(c.Key("file-blame-retry"), "Retry").Label("Retry Blame").Clicked() {
				a.retryFileBlameLoad()
			}
		})
	case st.blameLines != nil && len(st.blameLines) == 0:
		placeholderCard(c, "This file has no lines", st.path, "", p)
	default:
		ui.Column(c.Key("file-blame-body")).Grow(1).FillWidth().MinHeight(0).Children(func() {
			a.fileBlameList(c, p)
			a.fileBlameInfoBar(c, p)
		})
	}
}

// fileBlameList renders the virtualized blame rows. A run of consecutive
// lines of one commit shows the commit's subject, date and author on its
// first line only, exactly as BlameLine.First marks it; the lines after it
// leave those columns empty.
func (a *app) fileBlameList(c *ui.Context, p palette) {
	st := a.fileInspectorUI()

	st.blameList.Key = func(row int) any {
		return st.blameLines[row].Number
	}
	st.blameList.Label = func(row int) string {
		return "Blame Line " + strconv.Itoa(st.blameLines[row].Number)
	}
	st.blameList.Selected = &st.blameSel

	listTheme := *c.Theme()
	listTheme.Accent = p.listSelected
	listTheme.AccentText = p.onAccent
	ui.Themed(c.Key("file-blame-list-theme"), &listTheme).
		Grow(1).FillWidth().MinHeight(0).Background(ui.Transparent).
		Children(func() {
			ui.List(c.Key("file-blame-scroll"), &st.blameList, len(st.blameLines)).
				Grow(1).FillWidth().MinHeight(0).Label("File Blame").
				Rows(func(r ui.ListRow) {
					a.fileBlameRow(r.Context, p, r.Selected(), st.blameLines[r.Index])
				})
		})
}

// fileBlameRow draws one line of the file: its number, the commit cells on
// the first line of a commit's run, and the line's text. The cells stay
// present but empty on the lines after it so the columns never shift. The
// line's monospace text follows the code font size preference; the default
// keeps the built-in 11.5pt face and the 20px row exactly.
func (a *app) fileBlameRow(c *ui.Context, p palette, selected bool, l git.BlameLine) {
	metaColor, textColor := p.muted, p.text
	if selected {
		metaColor, textColor = p.onAccentMuted, p.onAccent
	}
	subject, date, author := "", "", ""
	if l.First {
		subject = l.Subject
		if !l.Date.IsZero() {
			date = l.Date.Format("2006-01-02")
		}
		author = l.Author
	}
	rowHeight := float32(20)
	codeSize := float32(11.5)
	if sz := normalizedDiffFontSize(a.store.Settings()); sz > 0 {
		codeSize = sz
		// A larger face must not clip: the row grows by the face plus its
		// leading, never below the default height.
		rowHeight = max(rowHeight, sz+8.5)
	}
	ui.Row(c).
		Height(rowHeight).
		FillWidth().
		PaddingX(12).
		Gap(8).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, strconv.Itoa(l.Number)).Font("monospace").FontSize(11).TextColor(metaColor).SingleLine().Width(40).Shrink(0).TextAlign(ui.End)
			ui.Text(c, subject).FontSize(11.5).TextColor(metaColor).SingleLine().Ellipsis("…").Width(170).Shrink(0)
			ui.Text(c, date).FontSize(11.5).TextColor(metaColor).SingleLine().Width(80).Shrink(0)
			ui.Text(c, author).FontSize(11.5).TextColor(metaColor).SingleLine().Ellipsis("…").Width(100).Shrink(0)
			ui.Text(c, l.Text).Font("monospace").FontSize(codeSize).TextColor(textColor).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).Grow(1)
		})
}

// fileBlameInfoBar is the bottom bar describing the commit of the chosen
// blame line, so a click on any line — including the empty-celled lines
// after the first of a run — shows what commit it belongs to.
func (a *app) fileBlameInfoBar(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	if st.blameSel < 0 || st.blameSel >= len(st.blameLines) {
		return
	}
	l := st.blameLines[st.blameSel]
	ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
	ui.Row(c.Key("file-blame-info")).FillWidth().Padding(6, 12).Gap(14).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, shortChangesHash(l.Hash)).Font("monospace").FontSize(11.5).FontWeight(600).TextColor(p.text).SingleLine().Shrink(0)
		ui.Text(c, l.Author).FontSize(11.5).TextColor(p.muted).SingleLine().Shrink(0)
		if !l.Date.IsZero() {
			ui.Text(c, l.Date.Format("2006-01-02")).FontSize(11.5).TextColor(p.muted).SingleLine().Shrink(0)
		}
		ui.Text(c, l.Subject).FontSize(11.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).Grow(1)
	})
}
