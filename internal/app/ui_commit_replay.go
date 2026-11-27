package app

import (
	"fmt"
	"slices"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// operationIdentity is the cherry-pick, revert, merge or rebase the user is
// looking at when they choose Continue, Skip or Abort: its kind, the HEAD it
// sits on, and the commit it stopped on. The background worker re-reads the
// repository and refuses to act when any of the three no longer matches, so a
// recovery never lands on an operation the user did not see. stop records why
// a rebase paused, empty for the other kinds: it is informational, never part
// of the match the worker re-verifies.
type operationIdentity struct {
	state  string
	head   string
	target string
	stop   string
}

// valid reports an identity of a real cherry-pick, revert, merge or rebase in
// progress: the kind must be one of the four and the operation must name the
// commit it is replaying or merging.
func (id operationIdentity) valid() bool {
	if id.state != "cherry-pick" && id.state != "revert" && id.state != "merge" && id.state != "rebase" {
		return false
	}
	return id.target != ""
}

// captureOperationIdentity snapshots the operation the status is showing.
func captureOperationIdentity(st git.Status) operationIdentity {
	return operationIdentity{state: st.State, head: st.Head, target: st.Operation, stop: st.Stopped}
}

// commitReplayUIState is the per-app state of the commit replay actions: the
// Cherry-pick/Revert dialog draft with the HEAD and branch it was opened
// against, the Abort confirmation's captured operation, and the repository
// generation they belong to. It lives outside the shared app struct, keyed by
// the app instance, exactly as the reset dialog state does.
type commitReplayUIState struct {
	repoGen uint64

	replayOpen           bool
	replayKind           string
	replayExpectedHead   string
	replayExpectedBranch string
	replayTarget         git.Commit
	replayCommitIt       bool
	replayRecordOrigin   bool
	replayMainline       int

	abortOpen   bool
	abortTarget operationIdentity
}

var commitReplayUIStates = struct {
	sync.Mutex
	m map[*app]*commitReplayUIState
}{m: map[*app]*commitReplayUIState{}}

// commitReplayUI returns the app's commit-replay state, creating it on first
// use and discarding every open dialog draft when the repository generation
// moved past it: a switch or close makes the captured target, HEAD, branch
// and operation meaningless, so nothing of the previous repository carries
// over.
func commitReplayUI(a *app) *commitReplayUIState {
	commitReplayUIStates.Lock()
	defer commitReplayUIStates.Unlock()
	st := commitReplayUIStates.m[a]
	if st == nil {
		st = &commitReplayUIState{}
		commitReplayUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearReplayDraft()
		st.clearAbort()
	}
	return st
}

// clearReplayDraft closes the Cherry-pick/Revert dialog and drops every value
// captured for it, so a reopened dialog starts from the repository as it is
// then.
func (st *commitReplayUIState) clearReplayDraft() {
	st.replayOpen = false
	st.replayKind = ""
	st.replayExpectedHead = ""
	st.replayExpectedBranch = ""
	st.replayTarget = git.Commit{}
	st.replayCommitIt = false
	st.replayRecordOrigin = false
	st.replayMainline = 0
}

// clearAbort closes the Abort confirmation and drops the captured operation,
// so a reopened confirmation captures the operation fresh.
func (st *commitReplayUIState) clearAbort() {
	st.abortOpen = false
	st.abortTarget = operationIdentity{}
}

// commitReplayMenuItems adds the Cherry-pick and Revert items to the commit
// context menu. Neither may run while a write is busy or an operation is in
// progress, because a replay on top of one would corrupt it; cherry-picking
// HEAD onto itself can never apply, so that one item additionally disables
// on the current commit.
func (a *app) commitReplayMenuItems(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	busyOrState := a.session.Busy() || snap.Status.State != ""
	if m.Item("Cherry-pick Commit…").Disabled(busyOrState || cm.Hash == snap.Status.Head).Chosen() {
		a.openReplayModal(cm, "cherry-pick")
	}
	if m.Item("Revert Commit…").Disabled(busyOrState).Chosen() {
		a.openReplayModal(cm, "revert")
	}
}

// openReplayModal opens the Cherry-pick or Revert dialog for the commit cm.
// Everything the dialog or its worker needs later is captured now, on the UI
// thread: the commit itself with its parents cloned, and the HEAD hash and
// branch the replay must still find when it runs — the background guard
// refuses the write when either has moved since.
func (a *app) openReplayModal(cm git.Commit, kind string) {
	st := commitReplayUI(a)
	snap := a.session.Snapshot()
	cm.Parents = slices.Clone(cm.Parents)
	st.replayOpen = true
	st.replayKind = kind
	st.replayExpectedHead = snap.Status.Head
	st.replayExpectedBranch = snap.Status.Branch
	st.replayTarget = cm
	st.replayCommitIt = true
	st.replayRecordOrigin = false
	st.replayMainline = 1
}

// commitReplayModals mounts the modals of the commit replay actions: the
// Cherry-pick/Revert dialog and the Abort confirmation. It renders on the
// History page through commitActionModals and on the Local Changes page, and
// since only one page is on screen per frame the two mounts never both build
// a modal.
func (a *app) commitReplayModals(c *ui.Context) {
	a.replayModal(c)
	a.abortOperationModal(c)
}

// replayModal is the Cherry-pick/Revert dialog: it applies one commit onto
// the current branch — or its reverse — with the options Git offers for both.
// The Mainline parent group only appears for a merge commit, because -m is
// meaningless otherwise; the -x checkbox only appears for a cherry-pick,
// because revert ignores it.
func (a *app) replayModal(c *ui.Context) {
	p := a.pal
	st := commitReplayUI(a)
	if !st.replayOpen {
		return
	}
	title := "Cherry-pick Commit"
	confirm := "Confirm Cherry-pick"
	if st.replayKind == "revert" {
		title = "Revert Commit"
		confirm = "Confirm Revert"
	}

	ui.Modal(c.Key("commit-replay-modal"), &st.replayOpen, func() {
		ui.Column(c.Key("commit-replay-modal-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Commit:", func() {
				ui.Text(c, st.replayTarget.Short()+" "+st.replayTarget.Subject).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			if len(st.replayTarget.Parents) > 1 {
				ui.Field(c, "Mainline parent:", func() {
					ui.RadioGroup(c, func() {
						for i := range st.replayTarget.Parents {
							n := i + 1
							ui.Radio(c, &st.replayMainline, n, fmt.Sprintf("Parent %d", n))
						}
					}).Label("Mainline parent")
				})
			}
			ui.Checkbox(c.Key("replay-commit-it"), &st.replayCommitIt, "Commit the changes")
			if st.replayKind == "cherry-pick" {
				ui.Checkbox(c.Key("replay-record-origin"), &st.replayRecordOrigin, "Append origin commit SHA (-x)")
			}
			ui.Row(c.Key("commit-replay-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("replay-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeReplayModal()
				}
				if ui.PrimaryButton(c.Key("replay-confirm"), confirm).Label(confirm).Disabled(a.session.Busy()).Clicked() {
					a.submitReplay()
				}
			})
		})
	})
	if !st.replayOpen {
		a.closeReplayModal()
	}
}

// closeReplayModal closes the Cherry-pick/Revert dialog and clears its draft.
// Canceling, dismissing and submitting all end here so no captured target or
// option survives the dialog.
func (a *app) closeReplayModal() {
	st := commitReplayUI(a)
	st.clearReplayDraft()
}

// submitReplay runs the replay the dialog describes through the session's
// write path. The background worker re-validates everything the dialog
// assumed — the HEAD, the branch, the absence of an operation in progress,
// a clean index and working tree, and the target commit itself — because any
// of it can change between the click and the write.
func (a *app) submitReplay() {
	st := commitReplayUI(a)
	if a.session.Busy() {
		return
	}
	kind := st.replayKind
	opts := git.ReplayOptions{NoCommit: !st.replayCommitIt}
	if kind == "cherry-pick" {
		opts.RecordOrigin = st.replayRecordOrigin
	}
	if len(st.replayTarget.Parents) > 1 {
		opts.Mainline = st.replayMainline
	}
	target := st.replayTarget.Hash
	expectedHead := st.replayExpectedHead
	expectedBranch := st.replayExpectedBranch
	a.closeReplayModal()
	label := "Cherry-picking"
	if kind == "revert" {
		label = "Reverting"
	}
	a.session.do(label, func(repo *git.Repo) error {
		return replayWithGuard(repo, kind, expectedHead, expectedBranch, target, opts)
	})
}

// hasTrackedLocalChanges reports uncommitted changes of tracked files: any
// staged entry, or an unstaged entry that is neither untracked nor ignored.
func hasTrackedLocalChanges(st git.Status) bool {
	if len(st.Staged) > 0 {
		return true
	}
	for _, f := range st.Unstaged {
		if f.Status != '?' && f.Status != '!' {
			return true
		}
	}
	return false
}

// replayWithGuard re-validates the repository against what the replay dialog
// captured before it applies a commit: the status must still be readable, no
// merge, rebase, cherry-pick or revert may be in progress, HEAD and the
// branch must still be exactly where the dialog saw them, the index and
// working tree must carry no uncommitted tracked changes, and the target
// commit must still exist. Only then does the replay run. Any check failing
// returns the error without touching the working tree, the index or HEAD —
// a cherry-pick or revert over local changes would either overwrite them or
// leave a half-applied state the user never asked for.
func replayWithGuard(repo *git.Repo, kind, expectedHead, expectedBranch, targetHash string, opts git.ReplayOptions) error {
	if kind != "cherry-pick" && kind != "revert" {
		return fmt.Errorf("unknown replay kind %q", kind)
	}
	if targetHash == "" {
		return fmt.Errorf("cannot %s: no target commit", kind)
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot %s: reading the repository state failed: %w", kind, err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot %s: a %s is in progress; finish or abort it first", kind, st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot %s: HEAD or the current branch changed after the dialog opened; review the repository and try again", kind)
	}
	if hasTrackedLocalChanges(st) {
		return fmt.Errorf("cannot %s with uncommitted local changes; commit or stash them first", kind)
	}
	if _, err := repo.PathHash(targetHash + "^{commit}"); err != nil {
		return fmt.Errorf("cannot %s: the target commit %s no longer exists: %w", kind, targetHash, err)
	}
	if kind == "revert" {
		return repo.RevertWith(targetHash, opts)
	}
	return repo.CherryPickWith(targetHash, opts)
}

// operationBannerColors are the wash and edge of the operation banner: a
// translucent amber that reads as a warning without shouting, tuned per
// theme polarity so the border stays visible over the wash.
func operationBannerColors(p palette) (bg, edge ui.Color) {
	amber := ui.Hex("#f59e0b")
	if p.dark {
		return amber.Alpha(0.16), amber.Alpha(0.55)
	}
	return amber.Alpha(0.12), amber.Alpha(0.5)
}

// commitOperationBanner renders the operation-in-progress banner at the top
// of the History and Local Changes pages while a cherry-pick, revert, merge
// or rebase is in progress, with the recovery actions Git offers for it. A
// merge shows only Continue and Abort, because git merge has nothing to skip.
// Continue stays disabled while conflicts are unresolved, because Git would
// refuse it the same way. With no operation in progress the banner builds
// nothing at all, so the idle pages keep exactly the pixels they had.
func (a *app) commitOperationBanner(c *ui.Context, p palette) {
	st := a.session.Snapshot().Status
	show := st.State == "cherry-pick" || st.State == "revert" ||
		((st.State == "merge" || st.State == "rebase") && st.Operation != "")
	if !show {
		return
	}
	kind := "Cherry-pick"
	conflictHint := " Resolve conflicts and stage the files to continue, or abort."
	resumeHint := ""
	switch st.State {
	case "revert":
		kind = "Revert"
	case "merge":
		kind = "Merge"
		resumeHint = " All conflicts resolved; continue to commit the merge, or abort."
	case "rebase":
		kind = "Rebase"
		conflictHint = " Resolve conflicts and stage the files to continue, or skip/abort."
		resumeHint = " Continue, skip, or abort the rebase."
	}
	msg := kind + " in progress."
	conflicted := st.Conflicted()
	if conflicted {
		msg += conflictHint
	} else {
		msg += resumeHint
	}
	bg, edge := operationBannerColors(p)
	busy := a.session.Busy()
	// The outer box carries the page inset as padding: a margin on a
	// full-width row shifts it without shrinking it, pushing its right edge
	// and the Abort button past the window.
	ui.Box(c.Key("operation-banner-wrap")).FillWidth().Padding(0, 24, 8, 24).Children(func() {
		ui.Row(c.Key("operation-banner")).FillWidth().Padding(8, 12).Gap(8).AlignItems(ui.Center).Radius(6).
			Background(bg).Border(1, edge).
			Role(ui.RoleStatus).Label("Operation in progress").Children(func() {
			ui.Text(c, msg).FontSize(12).TextColor(p.text).Grow(1).Shrink(1).MinWidth(0)
			if ui.Button(c.Key("operation-continue"), "Continue").Label("Continue Operation").Disabled(busy || conflicted).Clicked() {
				a.recoverOperation("continue")
			}
			if st.State != "merge" {
				if ui.Button(c.Key("operation-skip"), "Skip").Label("Skip Commit").Disabled(busy).Clicked() {
					a.recoverOperation("skip")
				}
			}
			if ui.Button(c.Key("operation-abort"), "Abort").Label("Abort Operation").Disabled(busy).Clicked() {
				a.askAbortOperation()
			}
		})
	})
}

// recoverOperation starts a Continue or Skip of the operation the snapshot
// is showing. The identity is captured here, on the UI thread, from what the
// user actually sees; the worker re-reads the repository and refuses to act
// when the operation changed underneath.
func (a *app) recoverOperation(action string) {
	if a.session.Busy() {
		return
	}
	captured := captureOperationIdentity(a.session.Snapshot().Status)
	if !captured.valid() {
		return
	}
	label := "Continuing"
	if action == "skip" {
		label = "Skipping"
	}
	a.session.do(label, func(repo *git.Repo) error {
		return recoverOperationWithGuard(repo, action, captured)
	})
}

// askAbortOperation opens the Abort confirmation for the operation the
// snapshot is showing, capturing its identity now so the confirmation and
// the worker both act on what the user saw when they clicked.
func (a *app) askAbortOperation() {
	if a.session.Busy() {
		return
	}
	captured := captureOperationIdentity(a.session.Snapshot().Status)
	if !captured.valid() {
		return
	}
	st := commitReplayUI(a)
	st.abortOpen = true
	st.abortTarget = captured
}

// abortOperationModal is the Abort confirmation: discarding the uncommitted
// changes of a cherry-pick, revert, merge or rebase is destructive, so it
// asks once more before the worker may run. Confirming stays disabled while
// a repository write is running, because a busy session would refuse the
// write anyway.
func (a *app) abortOperationModal(c *ui.Context) {
	p := a.pal
	st := commitReplayUI(a)
	if !st.abortOpen {
		return
	}
	title := "Abort cherry-pick?"
	description := "All uncommitted changes from this operation will be discarded."
	switch st.abortTarget.state {
	case "revert":
		title = "Abort revert?"
	case "merge":
		title = "Abort merge?"
		description = "All uncommitted changes from this merge will be discarded."
	case "rebase":
		title = "Abort rebase?"
		description = "Undo the rebase and restore the original branch."
	}
	ui.Modal(c.Key("abort-operation-modal"), &st.abortOpen, func() {
		ui.Column(c.Key("abort-operation-modal-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, description).FontSize(13).TextColor(p.muted).MaxWidth(390)
			ui.Row(c.Key("abort-operation-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("abort-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeAbortModal()
				}
				if ui.PrimaryButton(c.Key("abort-confirm"), "Confirm Abort").Label("Confirm Abort").Disabled(a.session.Busy()).Clicked() {
					a.submitAbortOperation()
				}
			})
		})
	})
	if !st.abortOpen {
		a.closeAbortModal()
	}
}

// closeAbortModal closes the Abort confirmation and drops the captured
// operation, so no stale identity survives the dialog.
func (a *app) closeAbortModal() {
	st := commitReplayUI(a)
	st.clearAbort()
}

// submitAbortOperation starts the Abort the confirmation captured. The
// worker re-reads the repository and refuses when the operation changed
// since the user opened the confirmation.
func (a *app) submitAbortOperation() {
	st := commitReplayUI(a)
	if a.session.Busy() {
		return
	}
	captured := st.abortTarget
	a.closeAbortModal()
	if !captured.valid() {
		return
	}
	a.session.do("Aborting", func(repo *git.Repo) error {
		return recoverOperationWithGuard(repo, "abort", captured)
	})
}

// recoverOperationWithGuard re-reads the repository before it continues,
// skips or aborts the captured operation. A failed status read, an operation
// whose kind, HEAD or target no longer matches what the user saw, or — for
// Continue — unresolved conflicts each refuse the write before Git runs:
// aborting or advancing the wrong operation would destroy a conflicted state
// the user is still working through. A merge can only continue or abort,
// because git merge has nothing to skip.
func recoverOperationWithGuard(repo *git.Repo, action string, captured operationIdentity) error {
	switch action {
	case "continue", "skip", "abort":
	default:
		return fmt.Errorf("unknown recovery action %q", action)
	}
	if captured.state == "merge" && action == "skip" {
		return fmt.Errorf("a merge has nothing to skip")
	}
	live, err := repo.Status()
	if err != nil {
		return fmt.Errorf("%s refused: reading the repository state failed: %w", action, err)
	}
	if live.State != captured.state || live.Head != captured.head || live.Operation != captured.target {
		return fmt.Errorf("%s refused: active operation changed", action)
	}
	if action == "continue" && live.Conflicted() {
		return fmt.Errorf("continue refused: resolve conflicts and stage the files first")
	}
	switch action {
	case "continue":
		// A rebase interrupted without REBASE_HEAD is in an unknown state:
		// continuing it automatically could drop or duplicate commits, so the
		// repair check refuses before Git runs. Aborting it stays safe.
		if captured.state == "rebase" {
			if err := repo.RepairInterruptedRebaseTodo(); err != nil {
				return err
			}
		}
		return repo.Continue(captured.state)
	case "skip":
		return repo.Skip(captured.state)
	}
	return repo.Abort(captured.state)
}
