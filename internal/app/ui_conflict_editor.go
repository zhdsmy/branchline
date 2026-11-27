package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// conflictInspectScope is the sessionRead scope of the asynchronous conflict
// inspection: one in-flight read per repository, superseded by the next
// selection or invalidated when the selection leaves the conflicted file.
const conflictInspectScope = "conflict-inspect"

// conflictEditorState is the per-app state of the conflict block inspector
// and merge editor: the cached inspection of the selected conflicted file,
// the per-hunk side choices, and the editable resolution draft. All fields
// are owned by the UI thread; background workers receive only captured
// values.
type conflictEditorState struct {
	// Cache identity: the repository generation, the selected path, and the
	// status revision the published inspection was read against. A read
	// starts only when this triple no longer matches the selection.
	repoGen    uint64
	path       string
	loadedRev  uint64
	loading    bool
	loaded     bool
	loadErr    error
	inspection git.ConflictInspection

	// View and draft: the pane mode (0 overview, 1 merge hunks), the split
	// layout of the merge view, the hunk the navigation focuses, the per-hunk
	// side choices, and the user-editable result text. draftSynced shadows the
	// last value the editor itself wrote into draft, so a user edit — even
	// emptying the draft to zero bytes — is detected exactly by draftDirty.
	mode        int
	columns     bool
	activeHunk  int
	scrollHunk  bool
	choices     []git.ConflictChoice
	draft       string
	draftSynced string
	draftDirty  bool

	// saveErr is the error of the last Save & Mark Resolved write for this
	// conflict, shown next to the button until the next attempt or reload.
	saveErr error

	// maxBytes overrides the inspection read limit in tests. Zero uses the
	// domain default. It survives every reset so a test can install it before
	// the first load.
	maxBytes int
}

// conflictEditor returns the app's conflict editor state, creating it on
// first use. A repository switch resets everything the state holds for the
// previous repository: the captured identity of another repository's conflict
// is meaningless.
func conflictEditor(a *app) *conflictEditorState {
	st := featureState(a, func() *conflictEditorState {
		return &conflictEditorState{repoGen: a.session.Generation()}
	})
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.resetAll()
	}
	return st
}

// resetAll drops the cached inspection, the draft and every view preference
// of the editor, keeping the repository generation and the test read-limit
// override. In-flight reads are not cancelled here; callers cancel the scope
// when the selection leaves the conflicted file.
func (st *conflictEditorState) resetAll() {
	maxBytes := st.maxBytes
	repoGen := st.repoGen
	*st = conflictEditorState{repoGen: repoGen, maxBytes: maxBytes}
}

// resetDraft drops the resolution draft and the hunk choices without touching
// the cached inspection: a re-inspection of the same conflict reseeds them.
func (st *conflictEditorState) resetDraft() {
	st.choices = nil
	st.draft = ""
	st.draftSynced = ""
	st.draftDirty = false
	st.activeHunk = 0
	st.scrollHunk = false
}

// resetConflictEditor drops the entire conflict editor state: the cached
// inspection, the resolution draft and the choices, and invalidates any
// in-flight inspection read so a late completion cannot repopulate the state
// of a conflict the user is no longer looking at. It is the cleanup the quick
// resolution success paths and the repository switch call for.
func (a *app) resetConflictEditor() {
	a.session.cancelRead(conflictInspectScope)
	conflictEditor(a).resetAll()
}

// userEditSynced folds any direct edit of the draft text area into the dirty
// tracking: when the draft no longer matches what the editor last wrote into
// it, the user edited it by hand — including emptying it to zero bytes.
func (st *conflictEditorState) userEditSynced() {
	if st.draft != st.draftSynced {
		st.draftDirty = true
		st.draftSynced = st.draft
	}
}

// writeDraft records a value the editor itself composed or loaded, so a later
// mismatch against the draft means the user typed it.
func (st *conflictEditorState) writeDraft(text string) {
	st.draft = text
	st.draftSynced = text
}

// recompute composes the resolution text from the inspected worktree text and
// the current hunk choices, and marks the draft user-editable state dirty:
// even reverting every choice leaves a draft the user asked for.
func (st *conflictEditorState) recompute() {
	st.writeDraft(git.ComposeConflictHunks(st.inspection.WorktreeText, st.choices))
	st.draftDirty = true
}

// mergeable reports whether the inspection supports the hunk merge editor:
// text on both sides, conflict blocks to work through, and none of the
// states the merge view cannot represent.
func mergeableInspection(ins git.ConflictInspection) bool {
	if ins.IsSubmodule || ins.Binary || ins.TooLarge {
		return false
	}
	if !ins.HaveOurs || !ins.HaveTheirs {
		return false
	}
	return len(ins.Hunks) > 0
}

// syncConflictEditor starts the asynchronous inspection of the selected
// conflicted file when the cache key — repository generation, path and status
// revision — no longer matches the cached or in-flight read. It never retries
// a failed read: only the Retry button clears the failure and reloads.
func (a *app) syncConflictEditor(st *conflictEditorState, f git.FileChange) {
	if a.session.Busy() || a.session.Loading() || !a.session.Loaded() {
		return
	}
	gen := a.session.Generation()
	path := f.Path
	rev := a.changesView.statusRev
	if st.loading {
		return
	}
	if st.loaded && st.repoGen == gen && st.path == path && st.loadedRev == rev {
		return
	}
	maxBytes := st.maxBytes
	startedRev := rev
	st.loading = true
	sessionRead(a.session, conflictInspectScope, func(repo *git.Repo) (git.ConflictInspection, error) {
		return repo.InspectConflict(path, maxBytes)
	}, func(ins git.ConflictInspection, err error) {
		// Publish only into the state of the exact selection this read belongs
		// to: a late completion of a superseded or deselected read is dropped
		// by the sessionRead sequence, and this identity check drops a read
		// whose repository, path or revision moved on after it started.
		if st.repoGen != gen || st.path != path || a.changesView.statusRev != startedRev {
			// The dropped read was the in-flight one; without clearing, the
			// state would wait forever for a completion that never publishes.
			st.loading = false
			return
		}
		st.loading = false
		st.loaded = true
		st.loadedRev = startedRev
		if err != nil {
			st.loadErr = err
			return
		}
		st.loadErr = nil
		previous := st.inspection
		st.inspection = ins
		identityChanged := previous.Identity != ins.Identity &&
			(previous.Identity != git.ConflictIdentity{} || len(st.choices) > 0 || st.draftDirty)
		// Typing of the current frame is not folded into draftDirty until the
		// next one, so the edit check runs here too: a publish that lands
		// between the keystroke and its frame must not reseed over the input.
		st.userEditSynced()
		if st.draftDirty && !identityChanged {
			// A status refresh of the same conflict keeps the user's draft —
			// even one emptied to zero bytes — and only renews the inspection
			// whose identity the save re-verifies. A changed identity means
			// the conflict itself moved on: the draft belongs to a conflict
			// that no longer exists and is reseeded from the new content.
			return
		}
		st.resetDraft()
		st.choices = make([]git.ConflictChoice, len(ins.Hunks))
		st.writeDraft(ins.WorktreeText)
	})
}

// conflictEditorPane renders the right-hand conflict inspector of a selected
// conflicted file: the header with the conflict type, the resolution progress
// and the mode switch, the quick resolutions of the conflict type, and the
// overview or the merge hunk editor below.
func (a *app) conflictEditorPane(c *ui.Context, f git.FileChange, p palette) {
	st := conflictEditor(a)

	// The selection lifecycle of the editor: arriving at another conflicted
	// file drops everything the previous file left, and a read for it is
	// superseded. The deselected state is unreachable from here — the pane
	// only renders for the selected conflict — so the quick resolutions and
	// the repository switch perform the same reset through resetConflictEditor.
	gen := a.session.Generation()
	if st.repoGen != gen {
		st.repoGen = gen
		st.resetAll()
	}
	if st.path != f.Path {
		if st.path != "" {
			// Leaving one conflicted file for another drops its draft and
			// choices outright and invalidates its in-flight read, so nothing
			// of it can publish into the next file's editor.
			a.session.cancelRead(conflictInspectScope)
		}
		st.resetAll()
		st.repoGen = gen
		st.path = f.Path
	}
	a.syncConflictEditor(st, f)
	st.userEditSynced()

	ins := st.inspection
	busy := a.session.Busy()

	ui.Column(c.Key("conflict-editor")).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Background(p.bg).Children(func() {
		a.conflictEditorHeader(c, f, st, ins, p)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline)

		switch {
		case st.loading && !st.loaded:
			placeholderCard(c, "Loading conflict…", f.Path, "", p)
		case st.loaded && st.loadErr != nil:
			a.conflictLoadErrorCard(c, st, p)
		case !st.loaded:
			placeholderCard(c, "Loading conflict…", f.Path, "", p)
		case ins.IsSubmodule:
			placeholderCard(c,
				"Submodule conflict",
				"This conflict is on a submodule pointer. Resolve it inside the submodule, or take a whole side with the actions above.",
				"", p)
		case ins.Binary:
			placeholderCard(c,
				"Binary file conflict",
				"Binary files cannot be merged line by line. Take one whole version with the actions above.",
				"", p)
		case ins.TooLarge:
			placeholderCard(c,
				"Conflict file too large to display inline",
				"The file exceeds the inline inspection limit. Take one whole version with the actions above.",
				"", p)
		case st.mode == 1 && mergeableInspection(ins):
			a.conflictMergeView(c, f, st, ins, p, busy)
		default:
			a.conflictOverview(c, f, st, ins, p)
		}
	})
}

// conflictEditorHeader renders the file path, the conflict type badge, the
// resolution progress, the mode switch and the quick resolution actions the
// conflict type allows. Both rows wrap so the mode toggle and long quick
// actions stay inside the pane at default and narrow window widths.
func (a *app) conflictEditorHeader(c *ui.Context, f git.FileChange, st *conflictEditorState, ins git.ConflictInspection, p palette) {
	busy := a.session.Busy()
	ui.Column(c.Key("conflict-editor-head")).FillWidth().Shrink(0).Children(func() {
		ui.Row(c.Key("conflict-editor-title")).MinHeight(36).FillWidth().Padding(6, 12).GapX(8).GapY(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, changeDisplayName(f)).FontSize(12.5).FontWeight(600).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			ui.Box(c).Padding(2, 6).Radius(5).Background(p.danger.Alpha(0.14)).Shrink(0).Tooltip(f.Conflict).Children(func() {
				ui.Text(c, git.ConflictTypeLabel(f.Conflict)).FontSize(11).FontWeight(600).TextColor(p.danger).SingleLine()
			})
			if st.loaded && st.loadErr == nil && !ins.IsSubmodule && !ins.Binary && !ins.TooLarge {
				a.conflictProgressBadge(c, st, ins, p)
			}
			ui.Spacer(c)
			canMerge := st.loaded && st.loadErr == nil && mergeableInspection(ins)
			modeBtn := func(label string, target int) {
				active := st.mode == target
				disabled := target == 1 && !canMerge
				b := ui.ButtonBase(c.Key("conflict-mode-" + label)).
					Height(22).
					PaddingX(8).
					Radius(5).
					Center().
					Label(label).
					AccessSelected(active).
					Disabled(disabled).
					FocusRing(false)
				if disabled {
					b.Tooltip("Line-by-line merging needs a text file with conflict blocks on both sides")
				}
				bg := ui.Transparent
				switch {
				case active && !disabled:
					bg = p.selected
				case !disabled && b.Hovered():
					bg = p.hover
				}
				b.Background(bg)
				fg := p.muted
				if active {
					fg = p.text
				}
				if disabled {
					fg = p.faint
				}
				b.Children(func() {
					ui.Text(c, label).FontSize(11.5).FontWeight(500).TextColor(fg).SingleLine()
				})
				if b.Clicked() && !disabled {
					st.mode = target
					if target == 1 {
						st.scrollHunk = true
					}
				}
			}
			ui.Row(c.Key("conflict-mode-toggle")).Padding(2).Gap(2).Radius(7).Background(p.hover).AlignItems(ui.Center).Shrink(0).Children(func() {
				modeBtn("Overview", 0)
				modeBtn("Merge Hunks", 1)
			})
		})
		ui.Row(c.Key("conflict-editor-actions")).FillWidth().Padding(2, 12, 8, 12).GapX(8).GapY(6).AlignItems(ui.Center).Wrap().Children(func() {
			worktreeExists := ins.WorktreeExists
			if !st.loaded || st.loadErr != nil {
				if stage, ok := a.session.Snapshot().Status.ConflictStage(f.Path); ok {
					worktreeExists = stage.WorktreeExists()
				}
			}
			for _, action := range conflictResolutionActions(f, worktreeExists) {
				action := action
				key := fmt.Sprintf("conflict-action-%x-%d", []byte(f.Path), action.kind)
				if action.kind == 0 {
					if ui.Button(c.Key(key+"-mark"), action.panelLabel).Label("Mark Resolved").FontSize(12).Padding(3, 8).Disabled(busy).Clicked() {
						a.markConflictResolved(f)
					}
				} else {
					if ui.Button(c.Key(key), action.panelLabel).Label(action.panelLabel).FontSize(12).Padding(3, 8).Disabled(busy).Clicked() {
						a.askResolveConflict(f, action.kind)
					}
				}
			}
		})
	})
}

// conflictProgressText summarizes the remaining text conflict markers in draft.
// Delete/modify and delete/delete conflicts have no text merge blocks on both
// sides, so they return no progress badge and keep only their conflict type.
// When both sides exist, the count reflects the actual markers currently in
// draft (including any typed back by hand), reporting "All conflicts resolved"
// only when no conflict markers remain.
func conflictProgressText(ins git.ConflictInspection, draft string) string {
	if !ins.HaveOurs || !ins.HaveTheirs {
		return ""
	}
	if !git.HasConflictMarkers(draft) {
		return "All conflicts resolved"
	}
	remaining := git.CountConflictHunks(draft)
	if remaining == 0 {
		// Unpaired or truncated markers still block saving, so never display
		// "0 conflicts" while markers remain.
		remaining = 1
	}
	if remaining == 1 {
		return "1 conflict"
	}
	return fmt.Sprintf("%d conflicts", remaining)
}

// conflictProgressBadge renders the resolution progress of the merge editor:
// the count of conflict blocks still present in the draft, or the all-resolved
// note once both sides exist and the draft carries no conflict markers.
func (a *app) conflictProgressBadge(c *ui.Context, st *conflictEditorState, ins git.ConflictInspection, p palette) {
	text := conflictProgressText(ins, st.draft)
	if text == "" {
		return
	}
	ui.Box(c).Padding(2, 6).Radius(5).Background(p.hover).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(11).FontWeight(500).TextColor(p.muted).SingleLine()
	})
}

// conflictLoadErrorCard renders the inspection failure with its error text
// and the manual retry. Nothing retries on its own: a frame never starts a
// read whose failure is already cached.
func (a *app) conflictLoadErrorCard(c *ui.Context, st *conflictEditorState, p palette) {
	ui.Column(c.Key("conflict-load-error")).Grow(1).FillWidth().Center().Padding(24).Gap(8).Children(func() {
		ui.Text(c, "Failed to load conflict").FontSize(16).FontWeight(600).TextColor(p.danger).TextAlign(ui.Center)
		ui.Text(c, st.loadErr.Error()).FontSize(12.5).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(520)
		if ui.Button(c.Key("conflict-load-retry"), "Retry").Label("Retry Conflict Load").Clicked() {
			st.loaded = false
			st.loadErr = nil
		}
	})
}

// conflictOverview renders the two full versions of the conflicted file side
// by side: the exact bytes of ours and theirs, a deletion note for the side
// that removed the file, or the empty-file note for a present but zero-byte
// side. When the file can be merged line by line, the hunk resolver entry
// leads into the merge view.
func (a *app) conflictOverview(c *ui.Context, f git.FileChange, st *conflictEditorState, ins git.ConflictInspection, p palette) {
	ui.Column(c.Key("conflict-overview")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("conflict-overview-intro")).FillWidth().Shrink(0).Padding(10, 16).Gap(8).Children(func() {
			ui.Text(c, conflictExplanation(f.Conflict)).FontSize(12.5).TextColor(p.text).MaxWidth(560)
			if mergeableInspection(ins) {
				ui.Row(c.Key("conflict-overview-merge-entry")).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
					if ui.PrimaryButton(c.Key("conflict-open-hunks"), "Open Hunk Resolver").Label("Open Hunk Resolver").Clicked() {
						st.mode = 1
						st.scrollHunk = true
					}
					blockLabel := fmt.Sprintf("%d conflict blocks", len(ins.Hunks))
					if len(ins.Hunks) == 1 {
						blockLabel = "1 conflict block"
					}
					ui.Text(c, blockLabel).FontSize(11.5).TextColor(p.muted).SingleLine()
				})
			} else {
				hint := "Merge Hunks is unavailable for this conflict."
				switch {
				case !ins.HaveOurs || !ins.HaveTheirs:
					hint = "Merge Hunks is unavailable because one side deleted this file."
				case len(ins.Hunks) == 0:
					hint = "Merge Hunks is unavailable because the file has no conflict blocks."
				}
				ui.Text(c, hint).FontSize(11.5).TextColor(p.muted).MaxWidth(560)
			}
		})
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		ui.Row(c.Key("conflict-overview-sides")).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			conflictSidePreview(c, ins, true, p)
			ui.Box(c).Width(1).FillHeight().Background(p.hairline)
			conflictSidePreview(c, ins, false, p)
		})
	})
}

// conflictSidePreview renders one version of the conflicted file in the
// overview. The deletion of a side and a present zero-byte file are different
// states: the first means the side removed the file, the second is a real
// empty version the merge recorded.
func conflictSidePreview(c *ui.Context, ins git.ConflictInspection, ours bool, p palette) {
	title := "Ours"
	have := ins.HaveOurs
	body := ins.OursText
	if !ours {
		title = "Theirs"
		have = ins.HaveTheirs
		body = ins.TheirsText
	}
	key := "conflict-side-theirs"
	if ours {
		key = "conflict-side-ours"
	}
	ui.Column(c.Key(key)).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
		ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(p.muted).Padding(8, 10, 0, 10).SingleLine()
		ui.Scroll(c.Key(key+"-scroll")).Grow(1).FillWidth().MinHeight(0).Padding(4, 10, 8, 10).Children(func() {
			switch {
			case !have:
				ui.Text(c, "This side deleted the file.").FontSize(12).TextColor(p.muted)
			case body == "":
				ui.Text(c, "Empty file (0 bytes).").FontSize(12).TextColor(p.muted)
			default:
				ui.Text(c, body).Font("monospace").FontSize(12).TextColor(p.text)
			}
		})
	})
}

// conflictMergeView renders the hunk-by-hunk merge editor: the toolbar with
// the progress, navigation, bulk side choices and the save action; below it
// every conflict block with its per-side choices, and the editable result.
func (a *app) conflictMergeView(c *ui.Context, f git.FileChange, st *conflictEditorState, ins git.ConflictInspection, p palette, busy bool) {
	hunks := ins.Hunks
	if len(st.choices) != len(hunks) {
		choices := make([]git.ConflictChoice, len(hunks))
		copy(choices, st.choices)
		st.choices = choices
	}
	recompute := func() { st.recompute() }

	ui.Column(c.Key("conflict-merge")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		a.conflictMergeToolbar(c, f, st, ins, p, busy, recompute)
		ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
		if st.saveErr != nil {
			ui.Text(c, st.saveErr.Error()).FontSize(11.5).TextColor(p.danger).Padding(6, 12, 0, 12).MaxWidth(720).Shrink(0)
		}
		if git.HasConflictMarkers(st.draft) {
			ui.Text(c, "The result still contains conflict markers.").FontSize(11.5).TextColor(p.danger).Padding(6, 12, 0, 12).MaxWidth(720).Shrink(0)
		}
		versions := func() {
			a.conflictHunkList(c, st, ins, p, recompute)
		}
		result := func() {
			a.conflictResultEditor(c, st, p)
		}
		if st.columns {
			ui.Row(c.Key("conflict-merge-columns")).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				versions()
				ui.Box(c).Width(1).FillHeight().Background(p.hairline)
				result()
			})
		} else {
			versions()
			ui.Box(c).Height(1).FillWidth().Background(p.hairline).Shrink(0)
			result()
		}
	})
}

// conflictMergeToolbar renders the merge editor's control strip in two
// wrapping rows — navigation and save on the first row, bulk side choices and
// layout toggle on the second — so the primary Save button stays inside the
// pane at default and narrow window widths.
func (a *app) conflictMergeToolbar(c *ui.Context, f git.FileChange, st *conflictEditorState, ins git.ConflictInspection, p palette, busy bool, recompute func()) {
	hunks := ins.Hunks
	progress := ""
	if !git.HasConflictMarkers(st.draft) {
		progress = "All conflicts resolved"
	} else if len(hunks) > 0 {
		progress = fmt.Sprintf("%d/%d", min(st.activeHunk, len(hunks)-1)+1, len(hunks))
	}

	ui.Column(c.Key("conflict-merge-toolbar")).FillWidth().Padding(6, 10).Gap(6).Shrink(0).Children(func() {
		ui.Row(c.Key("conflict-merge-toolbar-primary")).FillWidth().GapX(8).GapY(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, progress).FontSize(12).TextColor(p.muted).SingleLine().Shrink(0)

			navDisabled := len(hunks) < 2
			if ui.Button(c.Key("conflict-prev"), "Previous").Label("Previous Conflict").FontSize(11.5).Padding(2, 7).Disabled(navDisabled).Clicked() {
				st.activeHunk = (st.activeHunk + len(hunks) - 1) % len(hunks)
				st.scrollHunk = true
			}
			if ui.Button(c.Key("conflict-next"), "Next").Label("Next Conflict").FontSize(11.5).Padding(2, 7).Disabled(navDisabled).Clicked() {
				st.activeHunk = (st.activeHunk + 1) % len(hunks)
				st.scrollHunk = true
			}

			ui.Spacer(c)
			if ui.PrimaryButton(c.Key("conflict-save"), "Save & Mark Resolved").Label("Save and Mark Resolved").Disabled(busy || git.HasConflictMarkers(st.draft)).Clicked() {
				a.saveResolvedConflict(f, st)
			}
		})

		ui.Row(c.Key("conflict-merge-toolbar-options")).FillWidth().GapX(10).GapY(6).AlignItems(ui.Center).Wrap().Children(func() {
			allOurs := len(st.choices) > 0
			allTheirs := len(st.choices) > 0
			for _, ch := range st.choices {
				allOurs = allOurs && ch.Ours
				allTheirs = allTheirs && ch.Theirs
			}
			if cb := ui.Checkbox(c.Key("conflict-all-ours"), &allOurs, "All Ours").Label("All Ours"); cb.Changed() {
				for i := range st.choices {
					st.choices[i].Ours = allOurs
				}
				recompute()
			}
			if cb := ui.Checkbox(c.Key("conflict-all-theirs"), &allTheirs, "All Theirs").Label("All Theirs"); cb.Changed() {
				for i := range st.choices {
					st.choices[i].Theirs = allTheirs
				}
				recompute()
			}
			ui.Checkbox(c.Key("conflict-columns"), &st.columns, "Column layout").Label("Column layout")
		})
	})
}

// conflictHunkList renders every conflict block of the inspection: its title,
// the two versions with the choice checkboxes, and the focus highlight of the
// navigation target. The navigation scroll runs exactly on the frame the user
// asked for it, then disarms, so the list never locks its scroll position.
func (a *app) conflictHunkList(c *ui.Context, st *conflictEditorState, ins git.ConflictInspection, p palette, recompute func()) {
	hunks := ins.Hunks
	ui.Column(c.Key("conflict-hunks-pane")).Grow(1).Basis(0).MinWidth(0).MinHeight(0).FillHeight().Children(func() {
		ui.Scroll(c.Key("conflict-hunks-scroll")).Grow(1).FillWidth().MinHeight(0).Padding(6, 10, 10, 10).Children(func() {
			ui.Column(c.Key("conflict-hunks")).FillWidth().Gap(10).Children(func() {
				for i, h := range hunks {
					i, h := i, h
					focused := i == st.activeHunk
					border := p.hairline
					if focused {
						border = p.accent
					}
					block := ui.Column(c.Key(fmt.Sprintf("conflict-hunk-%d", i))).FillWidth().Gap(6).Padding(10).Radius(8).Background(p.surface).Border(1, border)
					if focused && st.scrollHunk {
						st.scrollHunk = false
						block.ScrollIntoView()
					}
					block.Children(func() {
						ui.Row(c.Key(fmt.Sprintf("conflict-hunk-head-%d", i))).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, fmt.Sprintf("Conflict %d", i+1)).FontSize(11.5).FontWeight(650).TextColor(p.text).SingleLine()
							if h.BaseStart >= 0 {
								ui.Box(c).Padding(1, 5).Radius(4).Background(p.hover).Shrink(0).Children(func() {
									ui.Text(c, "diff3").FontSize(10).FontWeight(500).TextColor(p.faint).SingleLine()
								})
							}
						})
						ui.Row(c.Key(fmt.Sprintf("conflict-hunk-sides-%d", i))).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
							conflictHunkSide(c, st, i, true, h.Ours, p, recompute)
							conflictHunkSide(c, st, i, false, h.Theirs, p, recompute)
						})
					})
				}
			})
		})
	})
}

// conflictHunkSide renders one side of one conflict block: the checkbox that
// keeps the side's lines in the result, and the lines themselves — or the
// deletion note for a side that removed them.
func conflictHunkSide(c *ui.Context, st *conflictEditorState, index int, ours bool, body string, p palette, recompute func()) {
	label := fmt.Sprintf("Ours %d", index+1)
	if !ours {
		label = fmt.Sprintf("Theirs %d", index+1)
	}
	key := fmt.Sprintf("conflict-hunk-%d-ours", index)
	if !ours {
		key = fmt.Sprintf("conflict-hunk-%d-theirs", index)
	}
	ui.Row(c.Key(key)).Grow(1).Basis(0).MinWidth(0).Gap(6).AlignItems(ui.Start).Children(func() {
		var keep *bool
		if ours {
			keep = &st.choices[index].Ours
		} else {
			keep = &st.choices[index].Theirs
		}
		if cb := ui.Checkbox(c.Key(key+"-keep"), keep, label).Label(label); cb.Changed() {
			recompute()
		}
		if body == "" {
			ui.Text(c, "This side deleted these lines.").FontSize(12).TextColor(p.muted).Grow(1).MinWidth(0)
			return
		}
		ui.Text(c, body).Font("monospace").FontSize(12).TextColor(p.text).Grow(1).MinWidth(0)
	})
}

// conflictResultEditor renders the editable merge result: the resolution the
// checkboxes compose, freely editable by hand down to a zero-byte file.
func (a *app) conflictResultEditor(c *ui.Context, st *conflictEditorState, p palette) {
	ui.Column(c.Key("conflict-result-pane")).Grow(1).Basis(0).MinWidth(0).MinHeight(0).FillHeight().Padding(8, 10, 10, 10).Gap(6).Children(func() {
		ui.Text(c, "Result").FontSize(11).FontWeight(600).TextColor(p.muted).SingleLine().Shrink(0)
		ui.TextArea(c.Key("conflict-result"), &st.draft).Grow(1).Basis(0).FillWidth().MinHeight(80).FontSize(12).Font("monospace").Label("Conflict result")
	})
}

// saveResolvedConflict writes the resolution draft as the file's resolution
// and stages it. Everything the write needs is captured here on the UI
// thread: the path, the exact bytes, and the conflict identity the inspection
// was read against — the safe write primitive re-reads the repository and
// refuses to overwrite anything when the conflict changed underneath the
// editor. A failed save keeps the draft for another attempt and reports the
// error beside the button.
func (a *app) saveResolvedConflict(f git.FileChange, st *conflictEditorState) {
	if a.session.Busy() {
		return
	}
	if !st.loaded || st.loadErr != nil {
		return
	}
	path := f.Path
	st.userEditSynced()
	content := st.draft
	expected := st.inspection.Identity
	gen := a.session.Generation()
	st.saveErr = nil
	a.session.doThen("Resolve "+path, func(repo *git.Repo) error {
		return repo.SafeWriteResolvedConflict(path, content, expected)
	}, func(err error) {
		st := conflictEditor(a)
		// The completion belongs to the conflict it started from: a switch of
		// repository or file in the meantime must not inherit its error or
		// lose the draft it just seeded.
		if st.repoGen != gen || st.path != path {
			return
		}
		if err != nil {
			st.saveErr = err
			return
		}
		st.saveErr = nil
		st.resetDraft()
	})
}
