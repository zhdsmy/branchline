package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

const (
	changesDiffScope  = "changes-diff"
	changesAmendScope = "changes-amend-msg"
)

// changesState holds the Local Changes page state on app.
// All fields are owned by the UI thread. Background workers capture only
// immutable values (paths, messages, expected HEAD hashes) before starting.
type changesState struct {
	split float32

	selectedPath   string
	selectedStaged bool
	selected       bool

	// statusRev increments every time the working-tree/index signature of the
	// current repository generation changes. It is folded into diff requests
	// so a mutation reloads the selected file's diff, while an unchanged
	// status does not retrigger reads.
	statusRev uint64
	statusSig string
	repoGen   uint64

	confirmDiscard bool
	discardTargets []git.FileChange

	message   string
	amend     bool
	amendHead string
	amendErr  string

	diff diffPanel

	// filter is the raw text of the filter box above the file list. The
	// derivation keeps its normalized form, so the matching never depends
	// on case or surrounding space.
	filter string

	// list is the place of the virtual file list, and listCache the
	// derivation of the current snapshot's status the list and the
	// selection lookups share. Both are owned by the UI thread.
	list      ui.ListState
	listCache changesListCache
}

func (s *changesState) ensureDefaults() {
	if s.split <= 0 {
		s.split = 372
	}
}

// changesStatusSignature identifies the working tree and index contents that
// can change file rows or diffs. HEAD movement matters for amend prefill and
// staged diffs, so it is part of the signature too.
func changesStatusSignature(st git.Status) string {
	var b strings.Builder
	b.WriteString(st.Head)
	for _, f := range st.Unstaged {
		fmt.Fprintf(&b, "\x00U%c\x00%s\x00%s\x00%s", f.Status, f.Path, f.OrigPath, f.Conflict)
	}
	for _, f := range st.Staged {
		fmt.Fprintf(&b, "\x00S%c\x00%s\x00%s", f.Status, f.Path, f.OrigPath)
	}
	return b.String()
}

func changesRevision(rev uint64) string {
	return "status-" + strconv.FormatUint(rev, 10)
}

func findChange(files []git.FileChange, path string) (git.FileChange, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	for _, f := range files {
		if f.OrigPath != "" && f.OrigPath == path {
			return f, true
		}
	}
	return git.FileChange{}, false
}

func cloneFileChanges(files []git.FileChange) []git.FileChange {
	out := make([]git.FileChange, len(files))
	copy(out, files)
	return out
}

func discardValidationError(want, got git.FileChange) error {
	switch {
	case got.Status != want.Status:
		return fmt.Errorf("discard validation failed for %s: status changed from %q to %q", want.Path, want.Status, got.Status)
	case got.OrigPath != want.OrigPath:
		return fmt.Errorf("discard validation failed for %s: rename source changed", want.Path)
	default:
		return fmt.Errorf("discard validation failed for %s: working-tree entry changed", want.Path)
	}
}

// validateDiscardTargets rereads the live working tree in the write worker and
// proves every captured target still exists with the same tracked/untracked
// status before repo.Discard may run. Any read or comparison failure returns
// an error and never discards or overwrites a file.
func validateDiscardTargets(repo *git.Repo, targets []git.FileChange) error {
	live, err := repo.Status()
	if err != nil {
		return fmt.Errorf("discard validation failed to read status: %w", err)
	}
	for _, want := range targets {
		got, ok := findChange(live.Unstaged, want.Path)
		if !ok {
			return fmt.Errorf("discard validation failed: %s is no longer unstaged", want.Path)
		}
		if got.Status != want.Status || got.OrigPath != want.OrigPath {
			return discardValidationError(want, got)
		}
	}
	return nil
}

// discardWithGuard revalidates the captured unstaged targets against a fresh
// status read, then discards exactly those entries. It never falls back to a
// broader discard: stale, missing, or unreadable status aborts the operation.
func discardWithGuard(repo *git.Repo, targets []git.FileChange) error {
	if len(targets) == 0 {
		return fmt.Errorf("discard validation failed: no files selected")
	}
	if err := validateDiscardTargets(repo, targets); err != nil {
		return err
	}
	return repo.Discard(targets...)
}

// amendWithGuard verifies HEAD is readable and still equals expectedHead
// before committing with --amend. A failed read, missing HEAD, or moved HEAD
// aborts before Git can overwrite the original commit or the working tree.
func amendWithGuard(repo *git.Repo, message, expectedHead string, signedOff bool) error {
	head, err := repo.PathHash("HEAD")
	if err != nil {
		return fmt.Errorf("amend validation failed to read HEAD: %w", err)
	}
	if head == "" {
		return fmt.Errorf("amend validation failed: HEAD is empty")
	}
	if expectedHead == "" {
		return fmt.Errorf("amend validation failed: no expected HEAD was recorded")
	}
	if head != expectedHead {
		return fmt.Errorf("amend validation failed: HEAD moved from %s to %s", shortChangesHash(expectedHead), shortChangesHash(head))
	}
	return repo.CommitWith(message, true, git.CommitOptions{SignedOff: signedOff})
}

func shortChangesHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func changeDisplayName(f git.FileChange) string {
	if f.OrigPath != "" && f.OrigPath != f.Path {
		return f.OrigPath + " → " + f.Path
	}
	return f.Path
}

func changeStatusColor(p palette, status byte) ui.Color {
	switch status {
	case 'A', '?':
		if p.dark {
			return ui.Oklch(0.66, 0.13, 145)
		}
		return ui.Oklch(0.56, 0.13, 145)
	case 'D', 'U':
		return p.danger
	case 'R', 'C', 'T':
		return p.accent
	default:
		return p.muted
	}
}

// resetChangesForRepo clears all repository-scoped local state when the
// session generation changes, including the shared diff panel content.
func (a *app) resetChangesForRepo() {
	s := &a.changesView
	s.selectedPath = ""
	s.selectedStaged = false
	s.selected = false
	s.statusRev = 0
	s.statusSig = ""
	s.confirmDiscard = false
	s.discardTargets = nil
	s.message = ""
	s.amend = false
	s.amendHead = ""
	s.amendErr = ""
	s.diff.Clear(a.session, changesDiffScope)
	s.repoGen = a.session.Generation()
	s.filter = ""
	s.listCache.invalidate()
}

// syncChangesRepoState advances repository-generation state and derives the
// status revision from the latest loaded snapshot. It also prunes a selection
// whose file disappeared so the diff panel cannot keep showing a stale file.
func (a *app) syncChangesRepoState() {
	s := &a.changesView
	gen := a.session.Generation()
	if s.repoGen != gen {
		a.resetChangesForRepo()
	}
	if !a.session.Loaded() || a.session.Loading() || a.session.Busy() {
		return
	}
	st := a.session.Snapshot().Status
	cache := s.listCache.lookup(st)
	sig := cache.signature
	if s.statusSig == sig {
		return
	}
	changed := s.statusSig != ""
	s.statusSig = sig
	if changed {
		s.statusRev++
	}
	if !s.selected {
		return
	}
	var ok bool
	if s.selectedStaged {
		_, ok = cache.lookupStaged(s.selectedPath)
	} else {
		_, ok = cache.lookupUnstaged(s.selectedPath)
	}
	if !ok {
		s.selectedPath = ""
		s.selectedStaged = false
		s.selected = false
		s.diff.Clear(a.session, changesDiffScope)
		return
	}
	// A revision change is picked up by the diff panel in the next frame. It
	// cannot be loaded from here: the shared panel renders through a per-frame
	// snapshot later in this same frame, and starting the read now would be
	// overwritten before its key could stick.
}

func (a *app) selectChange(f git.FileChange, staged bool) {
	s := &a.changesView
	if a.session.Busy() {
		return
	}
	s.selectedPath = f.Path
	s.selectedStaged = staged
	s.selected = true
	rev := changesRevision(s.statusRev)
	if staged {
		ff := f
		ff.Staged = true
		s.diff.Load(a.session, changesDiffScope, stagedDiffRequest(ff, rev))
		return
	}
	ff := f
	ff.Staged = false
	s.diff.Load(a.session, changesDiffScope, unstagedDiffRequest(ff, rev))
}

func (a *app) stageChanges(paths []string, all bool) {
	if len(paths) == 0 && !all {
		return
	}
	captured := append([]string(nil), paths...)
	a.session.do("Stage", func(repo *git.Repo) error {
		if all {
			return repo.StageAll()
		}
		return repo.Stage(captured...)
	})
}

func (a *app) unstageChanges(paths []string, all bool) {
	if len(paths) == 0 && !all {
		return
	}
	captured := append([]string(nil), paths...)
	a.session.do("Unstage", func(repo *git.Repo) error {
		if all {
			return repo.UnstageAll()
		}
		return repo.Unstage(captured...)
	})
}

func (a *app) askDiscardChanges(targets []git.FileChange) {
	s := &a.changesView
	if len(targets) == 0 || a.session.Busy() {
		return
	}
	s.discardTargets = cloneFileChanges(targets)
	s.confirmDiscard = true
}

func (a *app) confirmDiscardChanges() {
	s := &a.changesView
	targets := cloneFileChanges(s.discardTargets)
	s.confirmDiscard = false
	s.discardTargets = nil
	if len(targets) == 0 {
		return
	}
	a.session.do("Discard", func(repo *git.Repo) error {
		return discardWithGuard(repo, targets)
	})
}

func (a *app) cancelDiscardChanges() {
	s := &a.changesView
	s.confirmDiscard = false
	s.discardTargets = nil
}

func (a *app) amendToggled() {
	s := &a.changesView
	if !s.amend {
		s.amendHead = ""
		s.amendErr = ""
		a.session.cancelRead(changesAmendScope)
		return
	}
	s.amendErr = ""
	head := ""
	if a.session.Loaded() {
		head = a.session.Snapshot().Status.Head
	}
	if head == "" {
		s.amendHead = ""
		s.amendErr = "Amend is unavailable before the first commit."
		return
	}
	s.amendHead = head
	draftEmpty := strings.TrimSpace(s.message) == ""
	sessionRead(a.session, changesAmendScope, func(repo *git.Repo) (string, error) {
		return repo.LastMessage()
	}, func(msg string, err error) {
		if err != nil {
			s.amendErr = "Failed to read last commit message: " + err.Error()
			return
		}
		s.amendErr = ""
		if draftEmpty && strings.TrimSpace(s.message) == "" && s.amend && s.amendHead == head {
			s.message = msg
		}
	})
}

func (a *app) canCommitChanges() bool {
	if !a.session.HasRepo() || a.session.Busy() {
		return false
	}
	if a.session.Snapshot().Status.Conflicted() {
		return false
	}
	if strings.TrimSpace(a.changesView.message) == "" {
		return false
	}
	if a.changesView.amend {
		return a.changesView.amendHead != ""
	}
	return len(a.session.Snapshot().Status.Staged) > 0
}

func (a *app) commitChanges() {
	s := &a.changesView
	if !a.canCommitChanges() {
		return
	}
	msg := s.message
	amend := s.amend
	expectedHead := s.amendHead
	signedOff := a.store != nil && a.store.Settings().SignedOffBy
	a.session.doThen("Commit", func(repo *git.Repo) error {
		if amend {
			return amendWithGuard(repo, msg, expectedHead, signedOff)
		}
		return repo.CommitWith(msg, false, git.CommitOptions{SignedOff: signedOff})
	}, func(err error) {
		if err != nil {
			return
		}
		// Only a draft unchanged since capture belongs to this commit. Text or
		// the amend choice edited while Git was running belongs to the next one.
		if s.message == msg && s.amend == amend {
			s.message = ""
			s.amend = false
			s.amendHead = ""
			s.amendErr = ""
		}
	})
}

func (a *app) changesPage(c *ui.Context) {
	p := a.pal
	s := &a.changesView
	s.ensureDefaults()
	a.syncChangesRepoState()
	if s.selected {
		if st := a.session.Snapshot().Status; a.session.Loaded() && !a.session.Busy() {
			cache := s.listCache.lookup(st)
			var f git.FileChange
			var ok bool
			if s.selectedStaged {
				f, ok = cache.lookupStaged(s.selectedPath)
			} else {
				f, ok = cache.lookupUnstaged(s.selectedPath)
			}
			if ok {
				if !s.selectedStaged && f.Status == 'U' {
					// A conflicted file has no readable diff: its combined diff
					// is not a patch the panel can show, so the right pane shows
					// the conflict summary instead.
					s.diff.Clear(a.session, changesDiffScope)
				} else {
					rev := changesRevision(s.statusRev)
					if s.selectedStaged {
						f.Staged = true
						s.diff.Load(a.session, changesDiffScope, stagedDiffRequest(f, rev))
					} else {
						f.Staged = false
						s.diff.Load(a.session, changesDiffScope, unstagedDiffRequest(f, rev))
					}
				}
			}
		}
	}

	c.OnShortcut(ui.Cmd, ui.KeyEnter, a.commitChanges)

	top := a.pageTopInset(c) + pageTopGap
	meta := a.session.Meta()

	ui.Column(c.Key("changes-page")).Fill().Padding(top, 0, 0, 0).Children(func() {
		a.startMark(c)
		ui.Row(c.Key("changes-heading")).Padding(0, 24, 10, 24).AlignItems(ui.Baseline).Gap(10).Children(func() {
			ui.Text(c, "Local Changes").FontSize(22).FontWeight(700).TextColor(p.text)
			ui.Text(c, meta.Name).FontSize(13).TextColor(p.muted).SingleLine()
		})
		a.commitOperationBanner(c, p)

		switch {
		case a.session.LoadErr() != nil:
			placeholderCard(c, "Failed to load repository status", a.session.LoadErr().Error(), "", p)
			return
		case a.session.Loading() && !a.session.Loaded():
			placeholderCard(c, "Loading working tree status…", meta.Location(), "", p)
			return
		}

		if a.fileInspectorActive(pageChanges) {
			a.fileInspectorView(c, p)
		} else {
			ui.Split(c.Key("changes-split"), &s.split, func() {
				a.changesFilesPane(c)
			}, func() {
				if f, ok := a.selectedUnstagedConflict(); ok {
					a.conflictDetailPane(c, f, p)
				} else {
					s.diff.View(c, a.session, changesDiffScope, p)
				}
			}).Grow(1).FillWidth().MinHeight(0)
		}

		a.changesDiscardModal(c)
		a.commitReplayModals(c)
		a.conflictResolveModal(c)
	})
}

func (a *app) changesFilesPane(c *ui.Context) {
	p := a.pal
	st := a.session.Snapshot().Status
	s := &a.changesView

	ui.Column(c.Key("changes-files")).Fill().MinWidth(0).FillHeight().Background(p.bg).Children(func() {
		a.changesFilterRow(c)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline)
		// The input above has already committed this frame's typing into
		// s.filter, so the derivation the list draws matches what the box
		// shows.
		cache := s.listCache.lookupFiltered(st, s.filter)
		a.pruneFilteredSelection(cache)
		a.changesFilesList(c, cache)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline)
		a.commitBox(c, st)
	})
}

// changesFilterRow renders the filter box above the file list: a
// full-width input whose text the three sections filter by, with a button
// clearing it while it holds any text.
func (a *app) changesFilterRow(c *ui.Context) {
	s := &a.changesView
	ui.Row(c.Key("changes-filter")).FillWidth().Padding(8, 12, 6, 12).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c.Key("changes-filter-input"), &s.filter).
			Grow(1).
			Placeholder("Filter").
			Label("Filter Changes")
		if s.filter != "" {
			if ui.Button(c.Key("changes-filter-clear"), "Clear").Label("Clear Filter").FontSize(11).Padding(2, 6).Clicked() {
				s.filter = ""
			}
		}
	})
}

// pruneFilteredSelection drops the selection when the filter hides the
// selected file, so the diff panel returns to its unselected placeholder
// rather than showing a file the list no longer offers. The lookup is two
// map probes a frame: the per-file work happened once, when the snapshot
// or the filter changed.
func (a *app) pruneFilteredSelection(cache *changesListCache) {
	s := &a.changesView
	if !s.selected || cache.filter == "" {
		return
	}
	var f git.FileChange
	var ok bool
	if s.selectedStaged {
		f, ok = cache.lookupStaged(s.selectedPath)
	} else {
		f, ok = cache.lookupUnstaged(s.selectedPath)
	}
	if !ok {
		return
	}
	if changeMatchesFilter(f, cache.filter) {
		return
	}
	s.selectedPath = ""
	s.selectedStaged = false
	s.selected = false
	s.diff.Clear(a.session, changesDiffScope)
}

// changesFileSectionHeader renders the title row of the Unstaged or Staged
// section: its file count and the bulk action buttons of the section.
// files is what the section shows, total the files it holds without the
// filter. While the filter is active the count reads "shown of total" and
// the bulk buttons act on, and speak of, the shown files alone.
func (a *app) changesFileSectionHeader(c *ui.Context, staged bool, files []git.FileChange, total int, filtered bool) {
	p := a.pal
	title := "Unstaged"
	if staged {
		title = "Staged"
	}
	count := fmt.Sprintf("%s (%d)", title, len(files))
	if filtered {
		count = fmt.Sprintf("%s %d of %d", title, len(files), total)
	}

	ui.Row(c.Key("changes-section-header-"+title)).Height(30).FillWidth().Padding(0, 2).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, count).FontSize(12).FontWeight(650).TextColor(p.muted).SingleLine()
		ui.Spacer(c)
		if staged {
			verb := "Unstage All"
			if filtered {
				verb = "Unstage Shown"
			}
			if ui.Button(c.Key("unstage-all"), verb).Label(verb).FontSize(11).Padding(2, 6).Disabled(a.session.Busy() || len(files) == 0).Clicked() {
				if filtered {
					a.unstageChanges(unstageBulkPaths(files), false)
				} else {
					a.unstageChanges(nil, true)
				}
			}
		} else {
			discardLabel := "Discard All"
			discardText := "Discard All…"
			if filtered {
				discardLabel = "Discard Shown"
				discardText = "Discard Shown…"
			}
			if ui.Button(c.Key("discard-all"), discardText).Label(discardLabel).FontSize(11).Padding(2, 6).Disabled(a.session.Busy() || len(files) == 0).Clicked() {
				a.askDiscardChanges(files)
			}
			stageLabel := "Stage All"
			if filtered {
				stageLabel = "Stage Shown"
			}
			if ui.Button(c.Key("stage-all"), stageLabel).Label(stageLabel).FontSize(11).Padding(2, 6).Disabled(a.session.Busy() || len(files) == 0).Clicked() {
				if filtered || a.session.Snapshot().Status.Conflicted() {
					// Conflicted files must never be swept into the index by
					// a bulk stage: stage only the paths of this section,
					// which holds the non-conflicted files. A filter hides
					// the rest of the section too, so a shown-files stage
					// takes the same explicit path list.
					paths := make([]string, 0, len(files))
					for _, f := range files {
						if f.Status != 'U' {
							paths = append(paths, f.Path)
						}
					}
					a.stageChanges(paths, false)
				} else {
					a.stageChanges(nil, true)
				}
			}
		}
	})
}

// unstageBulkPaths lists the paths a bulk unstage of files passes to Git.
// A staged rename moves back only when its source path is unstaged with
// it, as the per-row Unstage button already does.
func unstageBulkPaths(files []git.FileChange) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.Status == 'R' && f.OrigPath != "" && f.OrigPath != f.Path {
			paths = append(paths, f.OrigPath)
		}
	}
	return paths
}

// changesFileEmptyHint renders the rounded placeholder of an empty Unstaged
// or Staged section.
func (a *app) changesFileEmptyHint(c *ui.Context, staged bool) {
	p := a.pal
	title := "Unstaged"
	if staged {
		title = "Staged"
	}
	empty := "Working tree is clean"
	if staged {
		empty = "No staged changes"
	} else if len(a.session.Snapshot().Status.Staged) > 0 || a.session.Snapshot().Status.Conflicted() {
		empty = "No unstaged changes"
	}
	ui.Box(c.Key("changes-empty-"+title)).FillWidth().Padding(8, 8).Radius(8).Background(p.hover).Children(func() {
		ui.Text(c, empty).FontSize(12).TextColor(p.faint).SingleLine()
	})
}

func (a *app) changesFileRow(c *ui.Context, f git.FileChange, staged bool) {
	p := a.pal
	s := &a.changesView
	title := "Unstaged"
	verb := "Stage"
	if staged {
		title = "Staged"
		verb = "Unstage"
	}
	rowLabel := title + " " + f.Path
	selected := s.selected && s.selectedStaged == staged && s.selectedPath == f.Path

	ui.Row(c.Key("change-row-" + title + "-" + f.Path)).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		b := ui.ButtonBase(c.Key("change-open-" + title + "-" + f.Path)).
			Grow(1).
			MinWidth(0).
			Height(28).
			PaddingX(6).
			Radius(7).
			AlignItems(ui.Center).
			Justify(ui.Start).
			Label(rowLabel).
			AccessSelected(selected).
			FocusRing(false)
		bg := ui.Transparent
		switch {
		case selected:
			bg = p.selected
		case b.Hovered():
			bg = p.hover
		}
		b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
		b.Children(func() {
			ui.Box(c).Width(16).Shrink(0).Center().Children(func() {
				ui.Text(c, string([]byte{f.Status})).FontSize(11).FontWeight(700).TextColor(changeStatusColor(p, f.Status)).SingleLine()
			})
			ui.Text(c, changeDisplayName(f)).FontSize(12.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		})
		b.ContextMenu(func(m *ui.Menu) {
			a.changesFileContextMenu(m, f)
		})
		if b.Clicked() {
			a.selectChange(f, staged)
		}

		if !staged {
			if ui.Button(c.Key("discard-"+f.Path), "Discard").Label("Discard "+f.Path).FontSize(11).Padding(2, 6).Disabled(a.session.Busy()).Clicked() {
				a.askDiscardChanges([]git.FileChange{f})
			}
		}
		if ui.Button(c.Key(verb+"-"+f.Path), verb).Label(verb+" "+f.Path).FontSize(11).Padding(2, 6).Disabled(a.session.Busy()).Clicked() {
			if staged {
				paths := []string{f.Path}
				if f.Status == 'R' && f.OrigPath != "" && f.OrigPath != f.Path {
					paths = append(paths, f.OrigPath)
				}
				a.unstageChanges(paths, false)
			} else {
				a.stageChanges([]string{f.Path}, false)
			}
		}
	})
}

func (a *app) commitBox(c *ui.Context, st git.Status) {
	p := a.pal
	s := &a.changesView

	ui.Column(c.Key("commit-box")).FillWidth().Padding(10, 12, 12, 12).Gap(8).Background(p.bg).Children(func() {
		ui.TextArea(c.Key("commit-message"), &s.message).
			Placeholder("Commit message").
			Label("Commit message").
			Height(86).
			FillWidth().
			Disabled(a.session.Busy())

		ui.Row(c.Key("commit-actions")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			amendBox := ui.Checkbox(c.Key("amend-checkbox"), &s.amend, "Amend").Label("Amend").Disabled(a.session.Busy())
			if amendBox.Changed() {
				a.amendToggled()
			}
			if s.amend && s.amendHead != "" {
				ui.Text(c, "HEAD "+shortChangesHash(s.amendHead)).FontSize(11).TextColor(p.faint).SingleLine()
			}
			ui.Spacer(c)
			if ui.PrimaryButton(c.Key("commit-button"), "Commit").Label("Commit").Tooltip("⌘↩").Disabled(!a.canCommitChanges()).Clicked() {
				a.commitChanges()
			}
		})

		// The commit message rules warn without blocking: an empty draft or one
		// inside every limit renders nothing at all, so the box keeps exactly
		// its default layout.
		prefs := store.Settings{}
		if a.store != nil {
			prefs = a.store.Settings()
		}
		if warning, severe := commitMessageWarning(s.message, prefs); warning != "" {
			color := p.muted
			if severe {
				color = p.danger
			}
			ui.Row(c.Key("commit-message-warning")).FillWidth().Justify(ui.Start).Role(ui.RoleStatus).Label("Commit Message Warning").Children(func() {
				ui.Text(c, warning).FontSize(11.5).TextColor(color).MaxWidth(520)
			})
		}

		if s.amendErr != "" {
			ui.Text(c, s.amendErr).FontSize(11.5).TextColor(p.danger).MaxWidth(520)
		}
		if err := a.session.ActionErr(); err != nil {
			ui.Text(c, err.Error()).FontSize(11.5).TextColor(p.danger).MaxWidth(520)
		}
		if st.Conflicted() {
			ui.Text(c, "Resolve all conflicts before committing.").FontSize(11.5).TextColor(p.danger).MaxWidth(520)
		}
		if len(st.Staged) == 0 && !s.amend {
			ui.Text(c, "Stage files to commit.").FontSize(11).TextColor(p.faint).SingleLine()
		}
	})
}

// discardConfirmMessage composes the body of the discard confirmation modal.
// Untracked files ('?' and ignored '!') are counted apart from tracked ones
// because discarding deletes them outright instead of reverting content, and
// every number gets correct English singular or plural.
func discardConfirmMessage(targets []git.FileChange) string {
	if len(targets) == 0 {
		return ""
	}
	untracked := 0
	for _, f := range targets {
		if f.Status == '?' || f.Status == '!' {
			untracked++
		}
	}
	tracked := len(targets) - untracked
	noun := func(n int) string {
		if n == 1 {
			return "file"
		}
		return "files"
	}
	switch {
	case tracked > 0 && untracked > 0:
		return fmt.Sprintf("Unstaged changes in %d %s will be lost, and %d untracked %s will be permanently deleted.",
			tracked, noun(tracked), untracked, noun(untracked))
	case untracked > 0:
		return fmt.Sprintf("%d untracked %s will be permanently deleted.", untracked, noun(untracked))
	default:
		return fmt.Sprintf("Unstaged changes in %d %s will be permanently lost.", tracked, noun(tracked))
	}
}

func (a *app) changesDiscardModal(c *ui.Context) {
	p := a.pal
	s := &a.changesView
	if !s.confirmDiscard {
		return
	}
	if ui.Modal(c.Key("discard-modal"), &s.confirmDiscard, func() {
		ui.Column(c.Key("discard-modal-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Discard changes?").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, discardConfirmMessage(s.discardTargets)).FontSize(13).TextColor(p.muted).MaxWidth(390)
			ui.Row(c.Key("discard-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("discard-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.cancelDiscardChanges()
				}
				if ui.PrimaryButton(c.Key("discard-confirm"), "Discard").Label("Confirm Discard").Clicked() {
					a.confirmDiscardChanges()
				}
			})
		})
	}).Dismissed() {
		a.cancelDiscardChanges()
	}
}
