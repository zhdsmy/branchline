package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// conflictResolveAction is the kind of quick resolution the user asked for:
// take one side of the conflict, or delete the file from the index and the
// working tree.
type conflictResolveAction uint8

const (
	resolveOurs conflictResolveAction = iota + 1
	resolveTheirs
	resolveDelete
)

// conflictUIState is the per-app state of the conflict resolution UI: the
// overwrite/delete confirmation with the conflict identity it was opened
// against, and the repository generation they belong to.
type conflictUIState struct {
	repoGen uint64

	confirmOpen   bool
	confirmKind   conflictResolveAction
	confirmTarget git.ConflictIdentity
	confirmPath   string
	confirmTitle  string
	confirmBody   string
}

// conflictUI returns the app's conflict resolution state, creating it on first
// use and discarding an open confirmation when the repository generation moved
// past it: a switch or close makes the captured identity meaningless, so
// nothing of the previous repository carries over.
func conflictUI(a *app) *conflictUIState {
	st := featureState(a, func() *conflictUIState { return &conflictUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearConfirm()
		a.resetConflictEditor()
	}
	return st
}

// clearConfirm closes the resolve confirmation and drops the captured conflict
// identity, so a reopened confirmation captures the conflict fresh.
func (st *conflictUIState) clearConfirm() {
	st.confirmOpen = false
	st.confirmKind = 0
	st.confirmTarget = git.ConflictIdentity{}
	st.confirmPath = ""
	st.confirmTitle = ""
	st.confirmBody = ""
}

// splitConflictedChanges partitions the unstaged entries of a status into the
// conflicted ones and the ordinary ones, keeping each list in its original
// order. The conflicted files render in their own section so a quick Stage or
// Discard never applies to a file Git has not merged yet.
func splitConflictedChanges(files []git.FileChange) (conflicted, unstaged []git.FileChange) {
	for _, f := range files {
		if f.Status == 'U' {
			conflicted = append(conflicted, f)
		} else {
			unstaged = append(unstaged, f)
		}
	}
	return conflicted, unstaged
}

// selectedUnstagedConflict returns the conflicted file the Local Changes page
// currently has selected, if any: the selection must be an unstaged row whose
// path still carries the unmerged status in the latest snapshot.
func (a *app) selectedUnstagedConflict() (git.FileChange, bool) {
	s := &a.changesView
	if !s.selected || s.selectedStaged || !a.session.Loaded() {
		return git.FileChange{}, false
	}
	f, ok := s.listCache.lookup(a.session.Snapshot().Status).lookupUnstaged(s.selectedPath)
	if !ok || f.Status != 'U' {
		return git.FileChange{}, false
	}
	return f, true
}

// conflictAction is one quick resolution offered for a conflicted file: its
// label on the summary panel and in the context menu, and the write it runs.
type conflictAction struct {
	panelLabel string
	menuLabel  string
	kind       conflictResolveAction
}

// conflictResolutionActions lists the quick resolutions a conflict code
// offers. Both-modified and both-added conflicts take either side. Delete and
// modify conflicts say what each side does to the file: keeping the version of
// the side that still has it, or accepting the deletion of the side that
// dropped it. A delete-delete conflict only accepts the deletion, unless the
// user put a file back in the working tree, which a checked Mark Resolved can
// stage as the resolution.
func conflictResolutionActions(f git.FileChange, worktreeExists bool) []conflictAction {
	markResolved := conflictAction{panelLabel: "Mark Resolved", menuLabel: "Mark Resolved", kind: 0}
	switch f.Conflict {
	case "DU", "UA":
		return []conflictAction{
			{panelLabel: "Use Ours (Delete File)", menuLabel: "Use Ours (Delete File)", kind: resolveOurs},
			{panelLabel: "Use Theirs (Keep File)", menuLabel: "Use Theirs (Keep File)", kind: resolveTheirs},
			markResolved,
		}
	case "UD", "AU":
		return []conflictAction{
			{panelLabel: "Use Ours (Keep File)", menuLabel: "Use Ours (Keep File)", kind: resolveOurs},
			{panelLabel: "Use Theirs (Delete File)", menuLabel: "Use Theirs (Delete File)", kind: resolveTheirs},
			markResolved,
		}
	case "DD":
		actions := []conflictAction{
			{panelLabel: "Accept Deletion", menuLabel: "Accept Deletion", kind: resolveDelete},
		}
		if worktreeExists {
			actions = append(actions, markResolved)
		}
		return actions
	default:
		return []conflictAction{
			{panelLabel: "Use Ours", menuLabel: "Use Ours…", kind: resolveOurs},
			{panelLabel: "Use Theirs", menuLabel: "Use Theirs…", kind: resolveTheirs},
			markResolved,
		}
	}
}

// conflictExplanation describes one conflict type in a sentence, for the
// summary panel of the selected conflicted file.
func conflictExplanation(code string) string {
	switch code {
	case "UU":
		return "Both sides modified this file."
	case "AA":
		return "Both sides added this file with different content."
	case "DU":
		return "This file was deleted on your side and modified on theirs."
	case "UD":
		return "This file was modified on your side and deleted on theirs."
	case "AU":
		return "This file was added on your side and does not exist on theirs."
	case "UA":
		return "This file does not exist on your side and was added on theirs."
	case "DD":
		return "Both sides deleted this file."
	default:
		return "This file is unmerged."
	}
}

// changesConflictHeader renders the title row of the Conflicted section,
// which exists only when the status holds unmerged files.
func (a *app) changesConflictHeader(c *ui.Context, count int) {
	p := a.pal
	title := fmt.Sprintf("Conflicted (%d)", count)
	ui.Row(c.Key("changes-section-header-conflicted")).Height(30).FillWidth().Padding(0, 2).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12).FontWeight(650).TextColor(p.danger).SingleLine()
	})
}

// changesConflictRow is one conflicted file of the Conflicted section. The row
// selects the file like every other change row, but it never offers Stage or
// Discard: staging an unmerged path would either fail or record the conflict
// markers, and discarding one cannot work, so the row offers only the quick
// resolution entries the conflict type allows.
func (a *app) changesConflictRow(c *ui.Context, f git.FileChange, cache *changesListCache, p palette) {
	s := &a.changesView
	rowLabel := "Conflicted " + f.Path
	selected := s.selected && !s.selectedStaged && s.selectedPath == f.Path
	stage, haveStage := cache.lookupConflictStage(f.Path)

	ui.Row(c.Key("conflict-row-" + f.Path)).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		b := ui.ButtonBase(c.Key("conflict-open-" + f.Path)).
			Grow(1).
			MinWidth(0).
			Height(28).
			PaddingX(6).
			Radius(7).
			AlignItems(ui.Center).
			Justify(ui.Start).
			Role(ui.RoleListItem).
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
				ui.Text(c, "U").FontSize(11).FontWeight(700).TextColor(p.danger).SingleLine()
			})
			ui.Text(c, changeDisplayName(f)).FontSize(12.5).TextColor(p.text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			ui.Box(c).Padding(1, 5).Radius(4).Background(p.danger.Alpha(0.14)).Shrink(0).Tooltip(f.Conflict).Children(func() {
				ui.Text(c, git.ConflictTypeLabel(f.Conflict)).FontSize(10).FontWeight(600).TextColor(p.danger).SingleLine()
			})
		})
		b.ContextMenu(func(m *ui.Menu) {
			a.conflictRowMenu(m, f, stage, haveStage)
		})
		if b.Clicked() {
			a.selectConflict(f)
		}

		if f.Conflict == "DD" {
			if ui.Button(c.Key("conflict-accept-deletion-"+f.Path), "Accept Deletion").Label("Accept Deletion "+f.Path).FontSize(11).Padding(2, 6).Disabled(a.session.Busy()).Clicked() {
				a.askResolveConflict(f, resolveDelete)
			}
		} else {
			if ui.Button(c.Key("conflict-mark-resolved-"+f.Path), "Mark Resolved").Label("Mark Resolved "+f.Path).FontSize(11).Padding(2, 6).Disabled(a.session.Busy()).Clicked() {
				a.markConflictResolved(f)
			}
		}
	})
}

// conflictRowMenu is the context menu of a conflicted file row: the quick
// resolutions the conflict type allows, then the file history and blame
// entries every tracked file of Local Changes offers.
func (a *app) conflictRowMenu(m *ui.Menu, f git.FileChange, stage git.ConflictStage, haveStage bool) {
	a.conflictMergeToolMenuItems(m, f, stage, haveStage)
	worktreeExists := haveStage && stage.WorktreeExists()
	for _, action := range conflictResolutionActions(f, worktreeExists) {
		action := action
		if action.kind == 0 {
			if m.Item(action.menuLabel).Disabled(a.session.Busy()).Chosen() {
				a.markConflictResolved(f)
			}
		} else {
			if m.Item(action.menuLabel).Disabled(a.session.Busy()).Chosen() {
				a.askResolveConflict(f, action.kind)
			}
		}
	}
	m.Separator()
	a.changesFileContextMenu(m, f)
}

// selectConflict selects a conflicted row for the conflict editor without
// kicking off a combined file diff read that the next frame would cancel.
func (a *app) selectConflict(f git.FileChange) {
	s := &a.changesView
	if a.session.Busy() {
		return
	}
	s.selectedPath = f.Path
	s.selectedStaged = false
	s.selected = true
	s.diff.Clear(a.session, changesDiffScope)
}

// conflictDetailPane is the mounting point for the right-hand pane of a
// selected conflicted file: the conflict block inspector and merge editor,
// which reuses the quick resolution actions and the overwrite confirmation
// of this file. It replaces the combined diff a conflicted file cannot
// meaningfully show without touching Local Changes again.
func (a *app) conflictDetailPane(c *ui.Context, f git.FileChange, p palette) {
	a.conflictEditorPane(c, f, p)
}

// askResolveConflict opens the overwrite/delete confirmation for a quick
// resolution, capturing everything the confirmation and the write worker need
// right here on the UI thread: the conflict identity the user is looking at,
// which the safe write primitive will re-verify before touching anything. A
// file whose identity cannot be read never reaches the confirmation.
func (a *app) askResolveConflict(f git.FileChange, action conflictResolveAction) {
	if a.session.Busy() {
		return
	}
	identity, ok := a.session.Snapshot().Status.ConflictIdentity(f.Path)
	if !ok {
		return
	}
	stage, haveStage := a.session.Snapshot().Status.ConflictStage(f.Path)
	worktreeExists := haveStage && stage.WorktreeExists()

	title := ""
	body := ""
	switch action {
	case resolveOurs, resolveTheirs:
		side := "ours"
		sideAdj := "our"
		sideNoun := "Your"
		if action == resolveTheirs {
			side = "theirs"
			sideAdj = "their"
			sideNoun = "Their"
		}
		haveSide := stage.HaveOurs()
		if action == resolveTheirs {
			haveSide = stage.HaveTheirs()
		}
		otherExists := stage.HaveOurs() || stage.HaveTheirs()
		if !haveSide && otherExists {
			title = "Delete " + f.Path + "?"
			body = fmt.Sprintf("%s side of the conflict deleted this file. Confirming deletes %s from the working tree and the index. This cannot be undone.", sideNoun, f.Path)
		} else {
			title = "Use " + sideAdj + " version?"
			body = fmt.Sprintf("The working tree file %s will be overwritten with the %s version and staged as the resolution. Any unmerged edits in the working tree will be lost.", f.Path, side)
		}
	case resolveDelete:
		title = "Accept deletion of " + f.Path + "?"
		body = fmt.Sprintf("Both sides deleted this file. Confirming removes %s from the working tree and the index. This cannot be undone.", f.Path)
		if worktreeExists {
			body = fmt.Sprintf("Both sides deleted this file. Confirming deletes the file you put back at %s and removes it from the working tree and the index. This cannot be undone.", f.Path)
		}
	default:
		return
	}

	st := conflictUI(a)
	st.confirmOpen = true
	st.confirmKind = action
	st.confirmTarget = identity
	st.confirmPath = f.Path
	st.confirmTitle = title
	st.confirmBody = body
}

// conflictResolveModal is the overwrite/delete confirmation of the conflict
// quick resolutions: it names the file, states exactly what confirming does to
// the working tree and the index, and only then lets the write start.
func (a *app) conflictResolveModal(c *ui.Context) {
	p := a.pal
	st := conflictUI(a)
	if !st.confirmOpen {
		return
	}
	ui.Modal(c.Key("conflict-resolve-modal"), &st.confirmOpen, func() {
		ui.Column(c.Key("conflict-resolve-modal-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, st.confirmTitle).FontSize(17).FontWeight(650).TextColor(p.text).MaxWidth(390)
			ui.Text(c, st.confirmBody).FontSize(13).TextColor(p.muted).MaxWidth(390)
			ui.Row(c.Key("conflict-resolve-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("conflict-resolve-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeConflictResolveModal()
				}
				if ui.PrimaryButton(c.Key("conflict-resolve-confirm"), "Confirm Resolve").Label("Confirm Resolve").Disabled(a.session.Busy()).Clicked() {
					a.submitConflictResolve()
				}
			})
		})
	})
	if !st.confirmOpen {
		a.closeConflictResolveModal()
	}
}

// closeConflictResolveModal closes the resolve confirmation and drops the
// captured conflict identity, so no stale target survives the dialog.
func (a *app) closeConflictResolveModal() {
	st := conflictUI(a)
	st.clearConfirm()
}

// submitConflictResolve starts the resolution the confirmation captured. The
// identity travels with the write: the safe primitive re-reads the repository
// in the worker and refuses to overwrite or delete anything when the conflict
// changed since the user opened the confirmation.
func (a *app) submitConflictResolve() {
	st := conflictUI(a)
	if a.session.Busy() {
		return
	}
	kind := st.confirmKind
	target := st.confirmTarget
	path := st.confirmPath
	a.closeConflictResolveModal()
	if kind == 0 || path == "" {
		return
	}
	gen := a.session.Generation()
	a.session.doThen("Resolve "+path, func(repo *git.Repo) error {
		switch kind {
		case resolveOurs:
			return repo.SafeTakeConflictSide(path, "ours", target)
		case resolveTheirs:
			return repo.SafeTakeConflictSide(path, "theirs", target)
		default:
			return repo.SafeDeleteConflictFile(path, target)
		}
	}, func(err error) {
		if err != nil {
			return
		}
		if st := conflictEditor(a); st.repoGen == gen && st.path == path {
			a.resetConflictEditor()
		}
	})
}

// markConflictResolved stages the working tree file of a conflicted path as
// its resolution, directly and without a confirmation, because the write
// itself is guarded twice: the safe primitive re-verifies the conflict
// identity in the worker and refuses to stage a file that still carries
// conflict markers.
func (a *app) markConflictResolved(f git.FileChange) {
	if a.session.Busy() {
		return
	}
	identity, ok := a.session.Snapshot().Status.ConflictIdentity(f.Path)
	if !ok {
		return
	}
	path := f.Path
	gen := a.session.Generation()
	a.session.doThen("Resolve "+path, func(repo *git.Repo) error {
		return repo.SafeMarkConflictResolved(path, identity)
	}, func(err error) {
		if err != nil {
			return
		}
		if st := conflictEditor(a); st.repoGen == gen && st.path == path {
			a.resetConflictEditor()
		}
	})
}
