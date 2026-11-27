package app

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// defaultMaxDiffBytes caps the raw unified diff size parsed into editor.DiffView
// so multi-megabyte generated or lock files show an explicit placeholder instead
// of blocking the UI thread.
const defaultMaxDiffBytes = 2 << 20

// diffKind identifies which Git diff source a diffRequest loads.
type diffKind uint8

const (
	diffNone diffKind = iota
	// diffCommitFile compares one file in a commit against its first parent
	// (or against the empty tree for a root commit).
	diffCommitFile
	// diffUnstaged compares one unstaged working-tree file against the index
	// (or against /dev/null for untracked and ignored files).
	diffUnstaged
	// diffStaged compares one staged file in the index against HEAD.
	diffStaged
)

// diffRequest describes one file diff to load asynchronously via sessionRead.
// Callers build it on the UI thread with plain value types so background
// workers never read live UI state.
type diffRequest struct {
	Kind   diffKind
	Commit git.Commit
	File   git.FileChange
	// Revision optionally disambiguates reloads (for example, a status or
	// snapshot version) so Load refreshes even when the same file path remains
	// selected after a working-tree or index mutation.
	Revision string
	// IgnoreSpace asks for a diff that ignores whitespace changes, as git
	// diff -w. Load copies the session's current choice into every request on
	// the UI thread so the background worker reads no live state.
	IgnoreSpace bool
	// EntireFile asks for a diff covering the whole file rather than only the
	// changes and their context, copied from the session's choice as
	// IgnoreSpace is.
	EntireFile bool
	// ContextLines is how many unchanged lines show around a change, as git
	// diff -U; it applies where EntireFile does not cover them all. Load
	// copies it from the session's choice as IgnoreSpace.
	ContextLines int
}

func commitFileDiffRequest(commit git.Commit, file git.FileChange) diffRequest {
	return diffRequest{
		Kind:   diffCommitFile,
		Commit: commit,
		File:   file,
	}
}

func unstagedDiffRequest(file git.FileChange, revision string) diffRequest {
	file.Staged = false
	return diffRequest{
		Kind:     diffUnstaged,
		File:     file,
		Revision: revision,
	}
}

func stagedDiffRequest(file git.FileChange, revision string) diffRequest {
	file.Staged = true
	return diffRequest{
		Kind:     diffStaged,
		File:     file,
		Revision: revision,
	}
}

func (r diffRequest) valid() bool {
	if r.Kind == diffNone || r.File.Path == "" {
		return false
	}
	if r.Kind == diffCommitFile && r.Commit.Hash == "" {
		return false
	}
	return true
}

func (r diffRequest) key() string {
	parent := ""
	if len(r.Commit.Parents) > 0 {
		parent = r.Commit.Parents[0]
	}
	return strconv.Itoa(int(r.Kind)) + "\x00" +
		r.Commit.Hash + "\x00" +
		parent + "\x00" +
		r.File.Path + "\x00" +
		r.File.OrigPath + "\x00" +
		string([]byte{r.File.Status}) + "\x00" +
		strconv.FormatBool(r.File.Staged) + "\x00" +
		r.Revision + "\x00" +
		strconv.FormatBool(r.IgnoreSpace) + "\x00" +
		strconv.FormatBool(r.EntireFile) + "\x00" +
		strconv.Itoa(r.ContextLines)
}

func (r diffRequest) title() string {
	if r.File.OrigPath != "" && r.File.OrigPath != r.File.Path {
		return r.File.OrigPath + " → " + r.File.Path
	}
	return r.File.Path
}

func (r diffRequest) badge() string {
	switch r.Kind {
	case diffCommitFile:
		return r.Commit.Short()
	case diffUnstaged:
		return "Unstaged"
	case diffStaged:
		return "Staged"
	default:
		return ""
	}
}

// diffLoadResult is the pure background-worker payload returned through
// sessionRead before building editor.DiffState on the UI thread.
type diffLoadResult struct {
	patch    editor.FilePatch
	hasPatch bool
	binary   bool
	tooLarge bool
	rawBytes int
}

// diffPanel is the shared file-diff viewer protocol used by the History and
// Local Changes pages. All fields are owned by the UI thread.
//
// Zero value is ready to use and defaults to editor.DiffSplit.
type diffPanel struct {
	layout   editor.DiffLayout
	maxBytes int

	repoGen   uint64
	req       diffRequest
	loadedKey string
	loading   bool
	err       error
	binary    bool
	tooLarge  bool
	empty     bool
	rawBytes  int
	patch     editor.FilePatch
	state     *editor.DiffState
	added     int
	deleted   int
}

func (d *diffPanel) limit() int {
	if d.maxBytes > 0 {
		return d.maxBytes
	}
	return defaultMaxDiffBytes
}

// Layout returns the active diff layout (editor.DiffSplit by default).
func (d *diffPanel) Layout() editor.DiffLayout {
	return d.layout
}

// SetLayout switches between editor.DiffSplit and editor.DiffUnified.
func (d *diffPanel) SetLayout(l editor.DiffLayout) {
	d.layout = l
	if d.state != nil {
		d.state.Layout = l
	}
}

// Load requests the diff for req via sessionRead under scope. The session's
// current diff choices — ignoring whitespace, the entire file, the context
// line count — are copied into the request on the UI thread, so a changed
// choice counts as a changed request and reloads.
// Calling Load repeatedly with an unchanged request on the same repository
// generation is a no-op, so callers can synchronize selection safely without
// triggering loops. When the selection changes, sessionRead discards any
// superseded in-flight result for scope.
func (d *diffPanel) Load(s *repoSession, scope string, req diffRequest) bool {
	if s == nil || !s.HasRepo() || !req.valid() {
		d.Clear(s, scope)
		return false
	}
	req.IgnoreSpace = s.IgnoreSpace()
	req.EntireFile = s.DiffEntireFile()
	req.ContextLines = s.DiffContextLines()
	gen := s.Generation()
	key := req.key()
	if d.repoGen == gen && d.loadedKey == key {
		return false
	}
	return d.startLoad(s, scope, req, key, gen)
}

// Reload forces a fresh background load of the current request, for example
// when the user clicks Retry after a transient error. The session's current
// diff choices are applied, as in Load.
func (d *diffPanel) Reload(s *repoSession, scope string) bool {
	if s == nil || !s.HasRepo() || !d.req.valid() {
		return false
	}
	req := d.req
	req.IgnoreSpace = s.IgnoreSpace()
	req.EntireFile = s.DiffEntireFile()
	req.ContextLines = s.DiffContextLines()
	return d.startLoad(s, scope, req, req.key(), s.Generation())
}

// Clear resets the displayed diff and invalidates any in-flight request for
// scope while preserving the user's Split/Unified layout choice.
func (d *diffPanel) Clear(s *repoSession, scope string) {
	if s != nil && scope != "" {
		s.cancelRead(scope)
	}
	d.resetContent()
	if s != nil {
		d.repoGen = s.Generation()
	} else {
		d.repoGen = 0
	}
}

func (d *diffPanel) resetContent() {
	d.req = diffRequest{}
	d.loadedKey = ""
	d.loading = false
	d.err = nil
	d.binary = false
	d.tooLarge = false
	d.empty = false
	d.rawBytes = 0
	d.patch = editor.FilePatch{}
	d.state = nil
	d.added = 0
	d.deleted = 0
}

func (d *diffPanel) startLoad(s *repoSession, scope string, req diffRequest, key string, gen uint64) bool {
	captured := req
	captured.Commit.Parents = slices.Clone(req.Commit.Parents)
	limit := d.limit()

	d.resetContent()
	d.repoGen = gen
	d.req = captured
	d.loadedKey = key
	d.loading = true

	started := sessionRead(s, scope, func(repo *git.Repo) (diffLoadResult, error) {
		return readDiffPatch(repo, captured, limit)
	}, func(res diffLoadResult, err error) {
		if d.repoGen != gen || d.loadedKey != key {
			return
		}
		d.loading = false
		if err != nil {
			d.err = err
			return
		}
		d.rawBytes = res.rawBytes
		if res.tooLarge {
			d.tooLarge = true
			return
		}
		if res.binary {
			d.binary = true
			return
		}
		if !res.hasPatch || len(res.patch.Hunks) == 0 {
			d.patch = res.patch
			d.empty = true
			return
		}
		st := &editor.DiffState{
			Layout:         d.layout,
			KeepScrollbars: true,
		}
		st.SetPatch(res.patch)
		applyDiffDisplayPrefs(st, s.Settings(), s.DiffWrap())
		d.patch = res.patch
		d.state = st
		d.added, d.deleted = st.Stats()
	})
	if !started {
		d.loading = false
	}
	return started
}

func readDiffPatch(repo *git.Repo, req diffRequest, limit int) (diffLoadResult, error) {
	var (
		raw string
		err error
	)
	opts := git.DiffOptions{IgnoreSpace: req.IgnoreSpace, Context: diffGitContext(req)}
	switch req.Kind {
	case diffCommitFile:
		raw, err = repo.CommitFileDiffOpts(req.Commit, req.File, opts)
	case diffUnstaged:
		f := req.File
		f.Staged = false
		raw, err = repo.FileDiffOpts(f, opts)
	case diffStaged:
		paths := []string{req.File.Path}
		if req.File.OrigPath != "" && req.File.OrigPath != req.File.Path {
			paths = append(paths, req.File.OrigPath)
		}
		raw, err = repo.StagedDiffOpts(paths, opts)
	default:
		return diffLoadResult{}, fmt.Errorf("unsupported diff kind %d", req.Kind)
	}
	if err != nil {
		return diffLoadResult{}, err
	}
	if len(raw) > limit {
		return diffLoadResult{
			tooLarge: true,
			rawBytes: len(raw),
		}, nil
	}
	files, err := editor.ParsePatch(raw)
	if err != nil {
		return diffLoadResult{}, fmt.Errorf("parse diff: %w", err)
	}
	if len(files) == 0 {
		if hasBinaryDiffMarker(raw) {
			return diffLoadResult{binary: true, rawBytes: len(raw)}, nil
		}
		return diffLoadResult{rawBytes: len(raw)}, nil
	}
	fp := files[0]
	for _, f := range files {
		if f.NewName == req.File.Path || (f.NewName == "" && f.OldName == req.File.Path) {
			fp = f
			break
		}
	}
	if fp.Binary || hasBinaryDiffMarker(raw) {
		return diffLoadResult{patch: fp, hasPatch: true, binary: true, rawBytes: len(raw)}, nil
	}
	return diffLoadResult{patch: fp, hasPatch: true, rawBytes: len(raw)}, nil
}

func hasBinaryDiffMarker(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" {
			return true
		}
	}
	return false
}

// applyDiffDisplayPrefs applies the display preferences to a diff state: the
// code font size — zero keeps the viewer's built-in default — the tab width
// the columns expand with — zero the framework default of 4 — whether
// changed lines are marked with + and - instead of bars, and whether long
// lines wrap rather than scroll sideways. The wrap choice lives on the
// session rather than in the persisted settings the other preferences read
// from, so callers pass it beside them. The state is updated where it is
// built and again where it renders, so a changed preference shows in an
// already open diff on the next frame.
func applyDiffDisplayPrefs(st *editor.DiffState, prefs store.Settings, wrap bool) {
	if sz := normalizedDiffFontSize(prefs); sz > 0 {
		st.Font = ui.Font{Family: "monospace", Size: sz}
	} else {
		st.Font = ui.Font{}
	}
	st.TabSize = prefs.DiffTabWidth
	if prefs.ChangeMarks {
		st.Indicators = editor.DiffClassic
	} else {
		st.Indicators = editor.DiffBars
	}
	st.Wrap = wrap
}

// View renders the shared diff header (file title, change badge, stats,
// Split/Unified toggle) and the editor.DiffView or placeholder body.
func (d *diffPanel) View(c *ui.Context, s *repoSession, scope string, p palette) {
	if s != nil && d.repoGen != 0 && d.repoGen != s.Generation() {
		d.resetContent()
		d.repoGen = s.Generation()
	}
	// The session's diff choices are part of the request key, so a change of
	// one — from the header's own controls or the Settings window — reloads
	// the active diff on the frame the panel renders in.
	if s != nil && d.req.valid() && !d.loading &&
		(d.req.IgnoreSpace != s.IgnoreSpace() ||
			d.req.EntireFile != s.DiffEntireFile() ||
			d.req.ContextLines != s.DiffContextLines()) {
		d.Load(s, scope, d.req)
	}

	panelKey := "diff-panel"
	if scope != "" {
		panelKey += "-" + scope
	}
	ui.Column(c.Key(panelKey)).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Background(p.bg).Children(func() {
		d.header(c, s, scope, p)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline)
		d.body(c, s, scope, p)
	})
}

// The diff header's compactness levels, richest first: the change
// statistics hide before the single row gives way to two, where the toolbar
// choices move under the title so every control keeps its room. The title
// shrinks and ellipsizes on its own at every level.
const (
	diffHeaderFull = iota
	diffHeaderNoStats
	diffHeaderCompact
)

// diffHeaderLevelFor picks the richest level whose fixed content fits
// avail DIPs of header content width. controls is the width the controls
// always take in one row; statsExtra and badgeExtra are the width the
// statistics and the badge add when they render, each including the row
// gap they bring. The level depends only on avail and the content, so a
// width that stays put keeps the same layout frame after frame.
func diffHeaderLevelFor(avail, controls, statsExtra, badgeExtra float32) int {
	if avail <= 0 {
		return diffHeaderFull
	}
	const slack = 2
	if avail >= controls+statsExtra+badgeExtra+slack {
		return diffHeaderFull
	}
	if avail >= controls+badgeExtra+slack {
		return diffHeaderNoStats
	}
	return diffHeaderCompact
}

// diffHeaderLevel measures the header's fixed content in the fonts it
// renders with and picks the level fitting avail. The controls' width is
// the toolbar's icon buttons while a diff is selected, the -w button and
// the Split/Unified capsule with its own padding and gap; the extras
// include the row gap each optional item adds beside its own width.
func diffHeaderLevel(c *ui.Context, avail float32, badge string, statsShown bool, added, deleted int, tools bool) int {
	measure := func(text string, size float32, weight int) float32 {
		w, _ := c.MeasureText(0, ui.Span{Text: text, Size: size, Weight: weight})
		return w
	}
	gaps := float32(3 * 8) // row gaps of the items that always render
	controls := measure("-w", 11.5, 500) + 10 +
		measure("Split", 11.5, 500) + 16 +
		measure("Unified", 11.5, 500) + 16 +
		4 + 2 // capsule padding and inner gap
	if tools {
		controls += diffToolbarGroupWidth()
		gaps += 8
	}
	controls += gaps
	statsExtra := float32(0)
	if statsShown {
		if added > 0 {
			statsExtra += measure(fmt.Sprintf("+%d", added), 11.5, 600)
		}
		if deleted > 0 {
			statsExtra += measure(fmt.Sprintf("-%d", deleted), 11.5, 600)
		}
		if added > 0 && deleted > 0 {
			statsExtra += 6 // gap inside the statistics row
		}
		statsExtra += 8
	}
	badgeExtra := float32(0)
	if badge != "" {
		badgeExtra = measure(badge, 11, 500) + 12 + 8
	}
	return diffHeaderLevelFor(avail, controls, statsExtra, badgeExtra)
}

func (d *diffPanel) header(c *ui.Context, s *repoSession, scope string, p palette) {
	box := ui.Column(c.Key("diff-header")).
		FillWidth().
		PaddingX(12).
		Label("Diff Header")

	// The header's bounds are the previous frame's: empty on the first
	// frame, for which the level reads as full, settling once the header
	// has laid out. Its width follows the panel, not the content, so the
	// level does not feed back into itself.
	avail := float32(0)
	if b := box.Bounds(); b.W > 0 {
		avail = b.W - 24 // PaddingX(12) on both sides
	}
	badge := d.req.badge()
	statsShown := d.state != nil && (d.added > 0 || d.deleted > 0)
	// The toolbar choices edit what a diff shows, so they render only while
	// a diff is selected: the empty header stays the quiet placeholder the
	// pages without a selection have always shown.
	tools := d.req.valid()
	level := diffHeaderLevel(c, avail, badge, statsShown, d.added, d.deleted, tools)

	if level == diffHeaderCompact && tools {
		// Two rows: the title, badge and layout choice keep the first, and
		// the content choices — the toolbar buttons and -w — move to the
		// second. At the minimum window width one row can no longer hold
		// them all, and clipping a control is worse than a taller header.
		box.Height(58).Gap(2).Children(func() {
			ui.Row(c.Key("diff-header-title")).
				Height(28).
				FillWidth().
				Gap(8).
				AlignItems(ui.Center).
				Children(func() {
					d.headerLeading(c, level, badge, statsShown, p)
					ui.Spacer(c)
					d.layoutCapsule(c, p)
				})
			ui.Row(c.Key("diff-header-options")).
				Height(26).
				FillWidth().
				Gap(8).
				AlignItems(ui.Center).
				Children(func() {
					d.diffOptionsGroup(c, s, scope, p)
					d.ignoreSpaceBtn(c, s, scope, p)
				})
		})
		return
	}

	box.Height(36).Children(func() {
		ui.Row(c.Key("diff-header-row")).
			Grow(1).
			FillWidth().
			Gap(8).
			AlignItems(ui.Center).
			Children(func() {
				d.headerLeading(c, level, badge, statsShown, p)

				ui.Spacer(c)

				if tools {
					d.diffOptionsGroup(c, s, scope, p)
				}

				d.ignoreSpaceBtn(c, s, scope, p)

				d.layoutCapsule(c, p)
			})
	})
}

// headerLeading renders the diff header's identifying content: the file
// title, which shrinks and ellipsizes, the change badge, and the change
// statistics while the level has room for them.
func (d *diffPanel) headerLeading(c *ui.Context, level int, badge string, statsShown bool, p palette) {
	title := d.req.title()
	if title == "" {
		title = "Diff"
	}
	ui.Text(c, title).
		FontSize(12.5).
		FontWeight(600).
		TextColor(p.text).
		SingleLine().
		Ellipsis("…").
		Shrink(1).
		MinWidth(0)

	if badge != "" {
		ui.Box(c).
			Padding(2, 6).
			Radius(5).
			Background(p.hover).
			Shrink(0).
			Children(func() {
				ui.Text(c, badge).FontSize(11).FontWeight(500).TextColor(p.muted).SingleLine()
			})
	}

	if statsShown && level == diffHeaderFull {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			if d.added > 0 {
				ui.Text(c, fmt.Sprintf("+%d", d.added)).FontSize(11.5).FontWeight(600).TextColor(c.Theme().Success).SingleLine()
			}
			if d.deleted > 0 {
				ui.Text(c, fmt.Sprintf("-%d", d.deleted)).FontSize(11.5).FontWeight(600).TextColor(p.danger).SingleLine()
			}
		})
	}
}

// layoutCapsule renders the Split/Unified layout toggle the header ends with.
func (d *diffPanel) layoutCapsule(c *ui.Context, p palette) {
	ui.Row(c.Key("diff-layout-toggle")).
		Padding(2).
		Gap(2).
		Radius(7).
		Background(p.hover).
		AlignItems(ui.Center).
		Shrink(0).
		Children(func() {
			d.layoutBtn(c, "Split", editor.DiffSplit, p)
			d.layoutBtn(c, "Unified", editor.DiffUnified, p)
		})
}

// ignoreSpaceBtn renders the header's ignore-whitespace toggle. It is global
// for every diff panel: the choice lives on the session, so every page sharing
// it reflects the same state. A failed save leaves the choice unchanged, so
// the diff is not reloaded; the error is surfaced by the save callback.
//
// The button reads "-w", the git diff flag it applies: the header's room at
// the minimum window width is nearly saturated once a file badge and the
// change stats render, and a full-length label would push the Split/Unified
// control out of the header. The tooltip names the action in full.
func (d *diffPanel) ignoreSpaceBtn(c *ui.Context, s *repoSession, scope string, p palette) {
	active := s != nil && s.IgnoreSpace()
	b := ui.ButtonBase(c.Key("diff-ignore-space")).
		Height(22).
		PaddingX(5).
		Radius(5).
		Center().
		Label("Ignore Whitespace").
		AccessSelected(active).
		FocusRing(false)
	b.Tooltip("Ignore whitespace changes")
	bg := ui.Transparent
	switch {
	case active:
		bg = p.selected
	case b.Hovered():
		bg = p.hover
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	fg := p.muted
	if active {
		fg = p.text
	}
	b.Children(func() {
		ui.Text(c, "-w").FontSize(11.5).FontWeight(500).TextColor(fg).SingleLine()
	})
	if b.Clicked() {
		d.toggleIgnoreSpace(s, scope)
	}
}

// toggleIgnoreSpace flips the session's ignore-whitespace choice and reloads
// the active diff with the new choice. When the choice fails to persist,
// SetIgnoreSpace leaves it unchanged and nothing is reloaded.
func (d *diffPanel) toggleIgnoreSpace(s *repoSession, scope string) {
	if s == nil {
		return
	}
	if err := s.SetIgnoreSpace(!s.IgnoreSpace()); err != nil {
		return
	}
	d.Load(s, scope, d.req)
}

func (d *diffPanel) layoutBtn(c *ui.Context, label string, target editor.DiffLayout, p palette) {
	active := d.layout == target
	b := ui.ButtonBase(c.Key("diff-mode-" + label)).
		Height(22).
		PaddingX(8).
		Radius(5).
		Center().
		Label(label).
		AccessSelected(active).
		FocusRing(false)
	bg := ui.Transparent
	switch {
	case active:
		bg = p.selected
	case b.Hovered():
		bg = p.hover
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	fg := p.muted
	if active {
		fg = p.text
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(11.5).FontWeight(500).TextColor(fg).SingleLine()
	})
	if b.Clicked() {
		d.SetLayout(target)
	}
}

func (d *diffPanel) body(c *ui.Context, s *repoSession, scope string, p palette) {
	switch {
	case d.req.Kind == diffNone:
		placeholderCard(c, "No File Selected", "Select a file to view its diff.", "", p)
	case d.loading:
		placeholderCard(c, "Loading diff…", d.req.title(), "", p)
	case d.err != nil:
		ui.Column(c.Key("diff-error")).Grow(1).FillWidth().Center().Padding(24).Gap(8).Children(func() {
			ui.Text(c, "Failed to load diff").FontSize(16).FontWeight(600).TextColor(p.danger).TextAlign(ui.Center)
			ui.Text(c, d.err.Error()).FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(520)
			if s != nil && s.HasRepo() {
				if ui.Button(c.Key("diff-retry"), "Retry").Label("Retry Diff").Clicked() {
					d.Reload(s, scope)
				}
			}
		})
	case d.tooLarge:
		placeholderCard(
			c,
			"Diff too large to display",
			fmt.Sprintf("%s exceeds the %s inline diff limit.", formatDiffBytes(d.rawBytes), formatDiffBytes(d.limit())),
			d.req.title(),
			p,
		)
	case d.binary:
		placeholderCard(c, "Binary file", "Diff preview is unavailable for binary files.", d.req.title(), p)
	case d.empty:
		d.emptyPlaceholder(c, p)
	case d.state != nil:
		d.state.Layout = d.layout
		if s != nil {
			applyDiffDisplayPrefs(d.state, s.Settings(), s.DiffWrap())
		}
		d.syncLineActions(s, scope, p)
		editor.DiffView(c.Key("diff-view"), d.state).Grow(1).FillWidth().MinHeight(0)
	default:
		placeholderCard(c, "No diff to display", "This file has no textual changes.", d.req.title(), p)
	}
}

func (d *diffPanel) emptyPlaceholder(c *ui.Context, p palette) {
	switch {
	case d.patch.OldName != "" && d.patch.NewName != "" && d.patch.OldName != d.patch.NewName:
		placeholderCard(c, "File renamed", d.patch.OldName+" → "+d.patch.NewName, "No content changes.", p)
	case d.patch.OldMode != "" && d.patch.NewMode != "" && d.patch.OldMode != d.patch.NewMode:
		placeholderCard(c, "File mode changed", d.patch.OldMode+" → "+d.patch.NewMode, d.req.title(), p)
	default:
		placeholderCard(c, "No diff to display", "This file has no textual changes.", d.req.title(), p)
	}
}

func formatDiffBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
