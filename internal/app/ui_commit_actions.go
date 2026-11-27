package app

import (
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// commitActionsUIState is the per-app state of the commit context actions:
// the Reset dialog draft with the HEAD and branch it was opened against, and
// the repository generation they belong to. It lives outside the shared app
// struct, keyed by the app instance, exactly as the repository switcher and
// sync states do.
type commitActionsUIState struct {
	repoGen uint64

	resetOpen         bool
	resetBranch       string
	resetExpectedHead string
	resetTarget       git.Commit
	resetMode         string
	resetHardAgreed   bool
}

var commitActionsUIStates = struct {
	sync.Mutex
	m map[*app]*commitActionsUIState
}{m: map[*app]*commitActionsUIState{}}

// commitActionsUI returns the app's commit-action state, creating it on first
// use and discarding every open dialog draft when the repository generation
// moved past it: a switch or close makes the captured target, HEAD and branch
// meaningless, so the dialogs of the previous repository never carry over.
func commitActionsUI(a *app) *commitActionsUIState {
	commitActionsUIStates.Lock()
	defer commitActionsUIStates.Unlock()
	st := commitActionsUIStates.m[a]
	if st == nil {
		st = &commitActionsUIState{}
		commitActionsUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearResetDraft()
	}
	return st
}

// clearResetDraft closes the Reset dialog and drops every value captured for
// it, so a reopened dialog starts from the repository as it is then.
func (st *commitActionsUIState) clearResetDraft() {
	st.resetOpen = false
	st.resetBranch = ""
	st.resetExpectedHead = ""
	st.resetTarget = git.Commit{}
	st.resetMode = ""
	st.resetHardAgreed = false
}

// resetModeDescriptions are the four git reset modes the dialog offers, in
// the dialog's order, with the one-line explanation shown under the radio
// group for the chosen one.
var resetModeDescriptions = []struct {
	mode  string
	label string
	note  string
}{
	{"soft", "Soft", "Keep all changes and stage differences."},
	{"mixed", "Mixed", "Keep all changes and unstage differences."},
	{"hard", "Hard", "Discard all uncommitted tracked changes and commits after the target revision."},
	{"keep", "Keep", "Keep local changes; stop if a modified file differs between revisions."},
}

func resetModeNote(mode string) string {
	for _, m := range resetModeDescriptions {
		if m.mode == mode {
			return m.note
		}
	}
	return ""
}

// commitContextMenu builds the context menu of one commit of the History
// list. The menu acts on the commit whose row was clicked — captured here by
// value, never read back from the selection — because a right-click does not
// change which row is selected. Further commit actions (cherry-pick, revert,
// tagging) add their items to this one menu so every commit action shares a
// single composition root.
func (a *app) commitContextMenu(m *ui.Menu, cm git.Commit) {
	a.checkoutCommitMenuItem(m, cm)
	snap := a.session.Snapshot()
	branch := snap.Status.Branch
	current := "HEAD"
	if branch != "" {
		current = "'" + branch + "'"
	}
	disabled := a.session.Busy() || snap.Status.Head == "" || snap.Status.State != ""
	a.commitMergeMenuItems(m, cm)
	a.commitRebaseMenuItems(m, cm)
	a.commitInteractiveRebaseMenuItems(m, cm)
	a.commitRebaseQuickMenuItems(m, cm)
	if m.Item("Reset " + current + " to Here…").Disabled(disabled).Chosen() {
		a.openResetModal(cm)
	}
	m.Separator()
	a.commitReplayMenuItems(m, cm)
	a.copyCommitMenuItems(m, cm)
}

// openResetModal opens the Reset dialog for the commit cm. Everything the
// dialog or its worker needs later is captured now, on the UI thread: the
// commit itself with its parents cloned, and the HEAD hash and branch the
// reset must still find when it runs — the background guard refuses the write
// when either has moved since.
func (a *app) openResetModal(cm git.Commit) {
	st := commitActionsUI(a)
	snap := a.session.Snapshot()
	cm.Parents = append([]string(nil), cm.Parents...)
	st.resetOpen = true
	st.resetBranch = snap.Status.Branch
	st.resetExpectedHead = snap.Status.Head
	st.resetTarget = cm
	st.resetMode = "mixed"
	st.resetHardAgreed = false
}

// commitActionModals mounts the modals of the commit context actions. It is
// the single composition root for every modal the commit context menu opens:
// the Reset dialog now, cherry-pick and revert later. It renders on the
// History page regardless of which page section is showing, because a modal
// belongs to the window, not to the list the click happened in.
func (a *app) commitActionModals(c *ui.Context) {
	a.resetModal(c)
	a.commitReplayModals(c)
	a.interactiveRebaseModal(c)
	a.checkoutCommitModal(c)
	a.commitRebaseQuickModals(c)
}

// resetModal is the Reset dialog: it moves the current branch — or the
// detached HEAD — to the commit it was opened for, in one of the four reset
// modes. The destructive Hard mode asks for an explicit second agreement, and
// its confirmation also stays disabled while a repository write is running,
// because a busy session would refuse the write anyway.
func (a *app) resetModal(c *ui.Context) {
	p := a.pal
	st := commitActionsUI(a)
	if !st.resetOpen {
		return
	}
	// The second agreement belongs to Hard alone: switching to another mode
	// puts the box back to unchecked, so a leftover tick never arms a later
	// Hard reset by accident.
	if st.resetMode != "hard" {
		st.resetHardAgreed = false
	}

	branchLabel := st.resetBranch
	if branchLabel == "" {
		branchLabel = "HEAD"
	}
	title := "Reset Current Branch to Revision"
	if st.resetBranch == "" {
		title = "Reset HEAD to Revision"
	}
	confirmDisabled := a.session.Busy() || (st.resetMode == "hard" && !st.resetHardAgreed)

	ui.Modal(c.Key("commit-reset-modal"), &st.resetOpen, func() {
		ui.Column(c.Key("commit-reset-modal-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Branch:", func() {
				ui.Text(c, branchLabel).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Field(c, "Move to:", func() {
				ui.Text(c, st.resetTarget.Short()+" "+st.resetTarget.Subject).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			ui.RadioGroup(c, func() {
				for _, m := range resetModeDescriptions {
					ui.Radio(c, &st.resetMode, m.mode, m.label)
				}
			}).Label("Reset Type")
			noteColor := p.muted
			if st.resetMode == "hard" {
				noteColor = p.danger
			}
			ui.Text(c, resetModeNote(st.resetMode)).FontSize(12).TextColor(noteColor)
			if st.resetMode == "hard" {
				ui.Checkbox(c.Key("reset-hard-agree"), &st.resetHardAgreed, "I understand, discard all local changes")
			}
			ui.Row(c.Key("commit-reset-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("reset-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeResetModal()
				}
				if ui.PrimaryButton(c.Key("reset-confirm"), "Confirm Reset").Label("Confirm Reset").Disabled(confirmDisabled).Clicked() {
					a.submitReset()
				}
			})
		})
	})
	if !st.resetOpen {
		a.closeResetModal()
	}
}

// closeResetModal closes the Reset dialog and clears its draft. Canceling,
// dismissing and submitting all end here so no captured target or agreement
// survives the dialog.
func (a *app) closeResetModal() {
	st := commitActionsUI(a)
	st.clearResetDraft()
}

// submitReset runs the reset the dialog describes through the session's write
// path. The background worker re-validates everything the dialog assumed —
// the HEAD, the branch, the absence of an operation in progress and the
// target commit itself — because any of it can change between the click and
// the write. A second guard against the mode and the Hard agreement stands in
// front even though the button already enforces both: a disabled control must
// never be the only thing protecting the working tree.
func (a *app) submitReset() {
	st := commitActionsUI(a)
	if a.session.Busy() {
		return
	}
	mode := st.resetMode
	if mode == "hard" && !st.resetHardAgreed {
		return
	}
	target := st.resetTarget.Hash
	expectedHead := st.resetExpectedHead
	expectedBranch := st.resetBranch
	a.closeResetModal()
	a.session.do("Resetting", func(repo *git.Repo) error {
		return resetWithGuard(repo, expectedHead, expectedBranch, target, mode)
	})
}

// resetWithGuard re-validates the repository against what the Reset dialog
// captured before it moves HEAD: the status must still be readable, no merge,
// rebase, cherry-pick or revert may be in progress, HEAD and the branch must
// still be exactly where the dialog saw them, and the target commit must
// still exist. Only then does the reset run. Any check failing returns the
// error without touching the working tree, the index or HEAD — the dialog's
// snapshot of the repository can be stale by the time the write starts, and
// in particular a Hard reset on a moved HEAD must never discard changes the
// user confirmed against a different state.
func resetWithGuard(repo *git.Repo, expectedHead, expectedBranch, targetHash, mode string) error {
	switch mode {
	case "soft", "mixed", "hard", "keep":
	default:
		return fmt.Errorf("unknown reset mode %q", mode)
	}
	if expectedHead == "" {
		return fmt.Errorf("cannot reset: the repository has no commits")
	}
	if targetHash == "" {
		return fmt.Errorf("cannot reset: no target commit")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot reset: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot reset: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot reset: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if _, err := repo.PathHash(targetHash + "^{commit}"); err != nil {
		return fmt.Errorf("cannot reset: the target commit %s no longer exists: %w", targetHash, err)
	}
	return repo.Reset(targetHash, mode)
}
