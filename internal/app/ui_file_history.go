package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// fileBlameBatchWindow is the longest the streaming blame read collects
// hunks before another batch goes to the UI: attribution fills in steps of
// at most one batch per window instead of one post per hunk.
const fileBlameBatchWindow = 100 * time.Millisecond

// errBlameIncomplete ends a blame read whose hunks did not cover every line
// of the file.
var errBlameIncomplete = errors.New("blame did not cover every line")

// fileBlameNow and fileBlameWrapHunk are test seams, as the package's other
// package-level hooks are: the clock the batching of blame hunks reads, and
// a decorator around the hunk delivery of the streaming read. A read
// captures both on the UI thread when it starts; its background worker never
// reads the variables. Tests replace them and restore the originals with
// their cleanup.
var fileBlameNow = time.Now
var fileBlameWrapHunk func(func(git.BlameHunk)) func(git.BlameHunk)

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

	// Blame fields. blameLines follows the same nil-until-loaded convention,
	// except that it fills in two steps now: the file's own lines arrive
	// first, without attribution, and the blame's hunks fill their commit
	// cells in batches while the blame is still computing. blameCovered
	// counts the lines a hunk filled, and the read is complete only when it
	// reaches the line count.
	blameLines   []git.BlameLine
	blameCovered int
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

// fileBlameBatch is one piece of the streaming blame read as it reaches the
// UI thread. The first batch carries the file's own lines in content — every
// line of it, attribution empty — and every later batch carries blame hunks
// to spread over them. content is nil exactly in hunk batches: an empty file
// arrives as an empty non-nil slice.
type fileBlameBatch struct {
	content []git.BlameLine
	hunks   []git.BlameHunk
}

// splitBlameLines cuts the whole content of a file into lines exactly as the
// lines of a blame read them: split at line feeds, the carriage return of a
// CRLF line staying part of the line's text, and nothing after a trailing
// line feed counting as a line. An empty file has no lines.
func splitBlameLines(content []byte) []git.BlameLine {
	if len(content) == 0 {
		return []git.BlameLine{}
	}
	parts := strings.Split(string(content), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]git.BlameLine, len(parts))
	for i, text := range parts {
		lines[i] = git.BlameLine{Number: i + 1, Text: text}
	}
	return lines
}

// startFileBlameLoad begins the asynchronous read of the line-by-line blame
// of the inspected file at st.blameRev. The one read first loads the file's
// content at the revision and emits it whole, so the view shows the lines at
// once; then the blame runs incrementally and its hunks fill the commit
// cells in batches of at most one per fileBlameBatchWindow. An empty file
// needs no blame: git blame of it annotates nothing, so the read ends after
// the content without running the blame command.
//
// A revisionless blame — the Local Changes entries pass "HEAD" — pins itself
// to the snapshot's HEAD commit here on the UI thread before the read
// starts: the file's blame at one commit never changes, so the read runs at
// the exact commit and its finished result caches under it, and a HEAD that
// moved on since never poisons a later lookup. The badge keeps showing the
// original "HEAD" either way. Only a snapshot without commits leaves the
// revision unresolved: the read then runs at the literal HEAD and its result
// is never cached.
//
// A cached finished blame of the same file at the same commit needs no read
// at all: the inspector fills from the cache at once, with no progress row
// and no git invocation.
func (a *app) startFileBlameLoad() {
	st := a.fileInspectorUI()
	if !st.open || !a.session.HasRepo() {
		return
	}
	path, rev := st.path, st.blameRev

	// Resolve the revision on the UI thread, where the snapshot is stable:
	// the background worker never re-reads it.
	at := rev
	if at == "" || at == "HEAD" {
		if head := a.session.Snapshot().Status.Head; head != "" {
			at = head
		} else {
			at = "HEAD"
		}
	}

	if at != "HEAD" {
		if lines, ok := a.fileBlameCache().get(at, path, a.session.Generation()); ok {
			st.blameLines = lines
			st.blameCovered = len(lines)
			st.blameLoading = false
			st.blameErr = nil
			if len(lines) == 0 {
				st.blameSel = -1
			}
			return
		}
	}

	now, wrapHunk := fileBlameNow, fileBlameWrapHunk
	st.blameLoading = true
	started := sessionReadStream(a.session, fileBlameScope,
		func(repo *git.Repo, emit func(fileBlameBatch)) (struct{}, error) {
			content, err := repo.BlobContent(git.BlobSide{Rev: at, Path: path})
			if err != nil {
				return struct{}{}, err
			}
			lines := splitBlameLines(content)
			emit(fileBlameBatch{content: lines})
			if len(lines) == 0 {
				return struct{}{}, nil
			}

			// Batching is driven by the arriving hunks, with no timer beside
			// the read: a hunk finding nothing collected and the last batch
			// a window or more behind ships at once, so a slow stream shows
			// every hunk as it comes; a hunk inside the window collects, and
			// the first hunk past the window flushes the collection ahead of
			// itself before joining the next one. At most one batch ships
			// per window either way. The flush after the loop ships what is
			// left, so no hunk is lost however the stream ends.
			var pending []git.BlameHunk
			var lastFlush time.Time
			flushed := false
			flush := func() {
				if len(pending) == 0 {
					return
				}
				batch := append([]git.BlameHunk(nil), pending...)
				pending = pending[:0]
				lastFlush = now()
				flushed = true
				emit(fileBlameBatch{hunks: batch})
			}
			deliver := func(h git.BlameHunk) {
				elapsed := now().Sub(lastFlush)
				if len(pending) == 0 && (!flushed || elapsed >= fileBlameBatchWindow) {
					pending = append(pending, h)
					flush()
					return
				}
				if len(pending) > 0 && elapsed >= fileBlameBatchWindow {
					flush()
				}
				pending = append(pending, h)
			}
			if wrapHunk != nil {
				deliver = wrapHunk(deliver)
			}
			err = repo.BlameIncremental(path, at, deliver)
			flush()
			return struct{}{}, err
		},
		func(batch fileBlameBatch) {
			if !st.open || st.path != path || st.blameRev != rev {
				return
			}
			a.applyFileBlameBatch(batch)
		},
		func(_ struct{}, err error) {
			if !st.open || st.path != path || st.blameRev != rev {
				st.blameLoading = false
				return
			}
			st.blameLoading = false
			if err != nil {
				// The file on screen and the attribution already filled both
				// stay: the error takes the progress row's place above the
				// list. When the file itself never arrived blameLines is nil,
				// and the failure replaces the whole view as before.
				st.blameErr = err
				return
			}
			st.blameErr = nil
			if st.blameLines != nil && st.blameCovered != len(st.blameLines) {
				st.blameErr = errBlameIncomplete
				return
			}
			if len(st.blameLines) == 0 {
				st.blameSel = -1
			}
			// Only a finished read — no error, every line covered — is worth
			// caching, and only at a resolved commit: a read that still ran at
			// the literal HEAD would cache its result under a moving target.
			// The stored slice is never written again (see fileBlameCache).
			if at != "HEAD" {
				a.fileBlameCache().put(at, path, st.blameLines, a.session.Generation())
			}
		})
	if !started {
		st.blameLoading = false
	}
}

// applyFileBlameBatch applies one batch of the streaming blame read on the
// UI thread. A content batch replaces the lines wholesale; a hunk batch
// touches only the lines its hunks cover: their commit fields fill in, and
// First is recomputed only where it can change — the first line of each
// hunk and the line right after it. Every other line keeps a First that no
// new hunk can affect, so no batch ever scans the whole file.
func (a *app) applyFileBlameBatch(batch fileBlameBatch) {
	st := a.fileInspectorUI()
	if batch.content != nil {
		st.blameLines = batch.content
		st.blameCovered = 0
		if len(batch.content) == 0 {
			st.blameSel = -1
		}
		return
	}
	if len(st.blameLines) == 0 {
		return
	}
	recompute := func(number int) {
		if number < 1 || number > len(st.blameLines) {
			return
		}
		l := &st.blameLines[number-1]
		if l.Hash == "" {
			l.First = false
			return
		}
		l.First = number == 1 || st.blameLines[number-2].Hash != l.Hash
	}
	for _, h := range batch.hunks {
		for i := 0; i < h.Lines; i++ {
			n := h.Line + i
			if n < 1 || n > len(st.blameLines) {
				continue
			}
			l := &st.blameLines[n-1]
			if l.Hash == "" {
				st.blameCovered++
			}
			l.Hash = h.Hash
			l.Author = h.Author
			l.Date = h.Date
			l.Subject = h.Subject
			l.First = false
		}
	}
	for _, h := range batch.hunks {
		recompute(h.Line)
		recompute(h.Line + h.Lines)
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
	st.blameCovered = 0
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
	a.historyExternalDiffMenuItem(m, commit, f)
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
	a.changesExternalDiffMenuItem(m, f)
	m.Separator()
	// Stashing a file needs a commit to reset it to, and git cannot write
	// any stash while the index has unmerged entries or against an ignored
	// file it never touches.
	noStash := a.session.Busy() || unborn || f.Status == 'U' || f.Status == '!'
	if m.Item("Stash Changes…").ID("stash-changes").Disabled(noStash).Chosen() {
		a.openSaveStashForFile(f)
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
// While the blame computes, the lines already show with their commit cells
// empty and a progress row above the list counts the attribution filled so
// far; a blame that fails partway keeps the lines and the filled cells and
// the row reads the error instead, with a retry beside it.
func (a *app) fileBlameModeView(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	switch {
	case st.blameLoading && st.blameLines == nil:
		placeholderCard(c, "Loading blame…", st.path, "", p)
	case st.blameErr != nil && st.blameLines == nil:
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
			a.fileBlameProgressRow(c, p)
			a.fileBlameList(c, p)
			a.fileBlameInfoBar(c, p)
		})
	}
}

// fileBlameProgressRow is the single row above the blame list while the
// read is unsettled: the count of attributed lines while the blame computes,
// or the failure it stopped with and a retry. A finished read draws nothing,
// so the settled view is exactly the one a whole-file blame always had.
func (a *app) fileBlameProgressRow(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	if !st.blameLoading && st.blameErr == nil {
		return
	}
	ui.Row(c.Key("file-blame-progress")).Height(28).FillWidth().PaddingX(12).Gap(10).AlignItems(ui.Center).Shrink(0).Children(func() {
		if st.blameErr != nil {
			ui.Text(c, "Blame stopped: "+st.blameErr.Error()).FontSize(12).TextColor(p.danger).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			if ui.Button(c.Key("file-blame-retry-inline"), "Retry").Label("Retry Blame").Clicked() {
				a.retryFileBlameLoad()
			}
			return
		}
		ui.Text(c, fmt.Sprintf("Loading blame… %d of %d lines", st.blameCovered, len(st.blameLines))).
			FontSize(12).TextColor(p.muted).SingleLine().Shrink(0)
	})
	ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
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
// after the first of a run — shows what commit it belongs to. A line whose
// blame hunk has not arrived yet has no commit to describe, and the bar
// stays away until one has.
func (a *app) fileBlameInfoBar(c *ui.Context, p palette) {
	st := a.fileInspectorUI()
	if st.blameSel < 0 || st.blameSel >= len(st.blameLines) {
		return
	}
	l := st.blameLines[st.blameSel]
	if l.Hash == "" {
		return
	}
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
