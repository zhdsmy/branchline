package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// rebaseUIState is the per-app state of the non-interactive rebase: the
// dialog draft with the HEAD, branch and target it was opened against, the
// asynchronous pre-flight inspection running behind it, and the repository
// generation they belong to.
type rebaseUIState struct {
	repoGen uint64

	open            bool
	branch          string
	ontoRev         string
	ontoLabel       string
	expectedOntoOID string
	expectedHead    string
	stash           bool
	updateRefs      bool

	inspecting   bool
	caps         git.GitCapabilities
	capsErr      error
	dependents   []string
	predictState string
}

// rebaseUI returns the app's rebase state, creating it on first use and
// discarding every open dialog draft when the repository generation moved
// past it: a switch or close makes the captured target, HEAD and branch
// meaningless, so nothing of the previous repository carries over. The
// inspection of the closing dialog is canceled with it, so its late result
// can never land in the next repository's dialog.
func rebaseUI(a *app) *rebaseUIState {
	st := featureState(a, func() *rebaseUIState { return &rebaseUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		if st.open || st.inspecting {
			a.session.cancelRead("rebase-inspect")
		}
		st.clearRebaseDraft()
	}
	return st
}

// clearRebaseDraft closes the Rebase dialog and drops every value captured
// for it, so a reopened dialog starts from the repository as it is then.
func (st *rebaseUIState) clearRebaseDraft() {
	st.open = false
	st.branch = ""
	st.ontoRev = ""
	st.ontoLabel = ""
	st.expectedOntoOID = ""
	st.expectedHead = ""
	st.stash = false
	st.updateRefs = false
	st.inspecting = false
	st.caps = git.GitCapabilities{}
	st.capsErr = nil
	st.dependents = nil
	st.predictState = ""
}

// rebaseCurrentNames the current branch the way the rebase menu items do:
// its name in quotes, or nothing when HEAD is detached.
func rebaseCurrentLabel(branch string) string {
	if branch == "" {
		return ""
	}
	return "'" + branch + "'"
}

// branchRebaseMenuItems adds the Rebase item to the context menu of one
// local branch of the sidebar: rebasing the current branch onto that branch.
// The item is disabled on the current branch itself — rebasing a branch onto
// itself can never apply — while a write is busy, while an operation is in
// progress, and in a detached or unborn repository, because the guard the
// write runs through requires a branch to move.
func (a *app) branchRebaseMenuItems(m *ui.Menu, br git.Branch) {
	snap := a.session.Snapshot()
	current := rebaseCurrentLabel(snap.Status.Branch)
	label := "Rebase on '" + br.Name + "'…"
	if current != "" {
		label = "Rebase " + current + " on '" + br.Name + "'…"
	}
	disabled := br.Current || a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == ""
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openRebaseModal(br.Name, br.Name, br.Hash)
	}
}

// remoteBranchRebaseMenuItems adds the Rebase item to the context menu of
// one remote-tracking branch of the sidebar. Unlike a local branch the
// target can never be the checked-out branch, so only the shared operation
// and repository conditions disable it.
func (a *app) remoteBranchRebaseMenuItems(m *ui.Menu, rb git.Branch) {
	snap := a.session.Snapshot()
	current := rebaseCurrentLabel(snap.Status.Branch)
	label := "Rebase on '" + rb.Name + "'…"
	if current != "" {
		label = "Rebase " + current + " on '" + rb.Name + "'…"
	}
	disabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == ""
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openRebaseModal(rb.Name, rb.Name, rb.Hash)
	}
}

// commitRebaseMenuItems adds the Rebase item to the context menu of one
// commit of the History list: rebasing the current branch so it replays on
// top of that commit. Rebasing onto HEAD itself cannot apply, so the item
// disables on the HEAD commit row besides the shared conditions.
func (a *app) commitRebaseMenuItems(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	label := "Rebase HEAD to Here…"
	if snap.Status.Branch != "" {
		label = "Rebase '" + snap.Status.Branch + "' to Here…"
	}
	disabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == "" || cm.Hash == snap.Status.Head
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openRebaseModal(cm.Hash, cm.Short()+" "+cm.Subject, cm.Hash)
	}
}

// openRebaseModal opens the Rebase dialog for the target revision ontoRev.
// Everything the dialog or its worker needs later is captured now, on the UI
// thread: the target revision with the commit hash it named and the label
// the dialog shows for it, and the HEAD hash the rebase must still find when
// it runs — the background guard refuses the write when either has moved
// since. The pre-flight inspection starts here so the capabilities, the
// dependent branches and the conflict prediction are on their way before the
// dialog first renders.
func (a *app) openRebaseModal(ontoRev, ontoLabel, expectedOntoOID string) {
	st := rebaseUI(a)
	snap := a.session.Snapshot()
	st.open = true
	st.branch = snap.Status.Branch
	st.ontoRev = ontoRev
	st.ontoLabel = ontoLabel
	st.expectedOntoOID = expectedOntoOID
	st.expectedHead = snap.Status.Head
	st.stash = false
	st.updateRefs = false
	st.inspecting = false
	st.caps = git.GitCapabilities{}
	st.capsErr = nil
	st.dependents = nil
	st.predictState = ""
	a.startRebaseInspection(st)
}

// rebaseInspection is the pre-flight result of one dialog: the capabilities
// of the Git that would run the rebase, the local branches a rebase with
// --update-refs would move, and whether the replay is predicted to conflict.
// Every piece is independent: a failure of one never discards the others.
type rebaseInspection struct {
	caps       git.GitCapabilities
	capsErr    error
	dependents []string
	predict    string
}

// startRebaseInspection reads the three pre-flight answers of the open
// dialog in one background read: the capabilities of the executor's own Git
// — never the local Git standing in for a server's — the dependent branches
// of the target, and the conflict prediction. None of them is decisive for
// the others, so each failure is recorded on its own: an unpredictable
// conflict only hides the prediction line, while a failed capability probe
// keeps --update-refs disabled.
func (a *app) startRebaseInspection(st *rebaseUIState) {
	onto := st.ontoRev
	st.inspecting = true
	sessionRead(a.session, "rebase-inspect", func(repo *git.Repo) (rebaseInspection, error) {
		res := rebaseInspection{}
		caps, err := repo.Capabilities()
		res.caps = caps
		res.capsErr = err
		if names, derr := repo.DependentBranches(onto); derr == nil {
			res.dependents = names
		}
		conflicts, perr := repo.RebaseConflicts(onto)
		switch {
		case perr != nil:
			// A Git without merge-tree --write-tree cannot predict, and any
			// other failure is just as undecided: the prediction line hides.
		case conflicts:
			res.predict = "conflicts"
		default:
			res.predict = "clean"
		}
		return res, nil
	}, func(res rebaseInspection, err error) {
		if !st.open || st.ontoRev != onto {
			return
		}
		st.inspecting = false
		st.caps = res.caps
		st.capsErr = res.capsErr
		st.dependents = res.dependents
		st.predictState = res.predict
	})
}

// rebaseDialog is the Rebase modal: it replays the commits of the current
// branch onto the branch, remote branch or commit it was opened for. The
// Update dependent branches checkbox only appears when the target actually
// has dependent branches, and stays disabled until the executor's Git is
// verified to know --update-refs: a version that cannot be verified is never
// treated as a new one.
func (a *app) rebaseDialog(c *ui.Context) {
	p := a.pal
	st := rebaseUI(a)
	if !st.open {
		return
	}

	canUpdateRefs := !st.inspecting && st.capsErr == nil && st.caps.RebaseUpdateRefs
	if !canUpdateRefs {
		st.updateRefs = false
	}

	_, cancelled, confirmed := dialogFrame(c, p, "rebase-modal", &st.open, "Rebase", 460, func() {
		ui.Text(c, "Replay commits from the current branch onto another base.").FontSize(12).TextColor(p.muted).MaxWidth(424)
		ui.Field(c, "Rebase:", func() {
			ui.Text(c, st.branch).Font("monospace").FontSize(12).TextColor(p.text)
		})
		ui.Field(c, "On:", func() {
			ui.Text(c, st.ontoLabel).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
		})
		if len(st.dependents) > 0 {
			ui.Checkbox(c.Key("rebase-update-refs"), &st.updateRefs, "Update dependent branches").Label("Update dependent branches").Disabled(!canUpdateRefs)
			if !canUpdateRefs && !st.inspecting {
				reason := "Git version could not be verified; requires Git 2.38 or newer."
				if st.capsErr == nil && st.caps.RawVersion != "" {
					reason = "Requires Git 2.38 or newer (found " + st.caps.RawVersion + ")."
				}
				ui.Text(c, reason).FontSize(12).TextColor(p.muted).MaxWidth(424)
			}
			if st.updateRefs {
				ui.Text(c, "These branches move with the rebase:").FontSize(11).TextColor(p.muted)
				for _, name := range st.dependents {
					ui.Text(c.Key("rebase-dep-"+name), name).Font("monospace").FontSize(12).TextColor(p.text)
				}
			}
		}
		ui.Checkbox(c.Key("rebase-autostash"), &st.stash, "Stash and reapply local changes").Label("Stash and reapply local changes")
		ui.Text(c, "Uncommitted changes are put aside before the rebase and reapplied after it.").FontSize(12).TextColor(p.muted).MaxWidth(424)
		switch st.predictState {
		case "conflicts":
			ui.Text(c, "Rebase will cause conflicts").FontSize(12).TextColor(p.danger)
		case "clean":
			ui.Text(c, "Rebase can be done without conflicts").FontSize(12).TextColor(p.muted)
		}
	},
		dialogAction{key: "rebase-cancel", text: "Cancel", label: "Cancel"},
		dialogAction{key: "rebase-confirm", text: "Confirm Rebase", label: "Confirm Rebase", disabled: a.session.Busy(), autoFocus: true})
	if cancelled {
		a.closeRebaseModal()
	}
	if confirmed {
		a.submitRebase()
	}
	if !st.open {
		a.closeRebaseModal()
	}
}

// closeRebaseModal closes the Rebase dialog, cancels its inspection and
// clears the draft. Canceling, dismissing and submitting all end here so no
// captured target or inspection survives the dialog.
func (a *app) closeRebaseModal() {
	st := rebaseUI(a)
	if st.open || st.inspecting {
		a.session.cancelRead("rebase-inspect")
	}
	st.clearRebaseDraft()
}

// submitRebase runs the rebase the dialog describes through the session's
// write path. The background worker re-validates everything the dialog
// assumed — the HEAD, the branch, the absence of an operation in progress,
// the cleanliness of the index and working tree, the target revision and the
// --update-refs capability — because any of it can change between the click
// and the write.
func (a *app) submitRebase() {
	st := rebaseUI(a)
	if a.session.Busy() {
		return
	}
	opts := git.RebaseOntoOptions{
		Stash:      st.stash,
		UpdateRefs: st.updateRefs && len(st.dependents) > 0,
	}
	ontoRev := st.ontoRev
	expectedOntoOID := st.expectedOntoOID
	expectedHead := st.expectedHead
	expectedBranch := st.branch
	a.closeRebaseModal()
	a.session.do("Rebasing", func(repo *git.Repo) error {
		return rebaseWithGuard(repo, expectedHead, expectedBranch, ontoRev, expectedOntoOID, opts)
	})
}

// rebaseHasTrackedLocalChanges reports uncommitted changes of tracked files:
// any staged entry, or an unstaged entry that is neither untracked nor
// ignored.
func rebaseHasTrackedLocalChanges(st git.Status) bool {
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

// rebaseWithGuard re-validates the repository against what the Rebase dialog
// captured before it replays the current branch: the status must still be
// readable, no merge, rebase, cherry-pick or revert may be in progress, HEAD
// and the branch must still be exactly where the dialog saw them, the target
// revision must still name the commit it named then, and --update-refs asks
// the executor's Git itself whether it knows the flag. Without autostash the
// index and working tree must carry no uncommitted tracked changes. Only
// then does the rebase run. Any check failing returns the error without
// touching the working tree, the index, HEAD or any branch — the dialog's
// snapshot of the repository can be stale by the time the write starts, and
// a rebase that moved onto a moved target or over local changes would destroy
// state the user never confirmed against.
func rebaseWithGuard(repo *git.Repo, expectedHead, expectedBranch, ontoRev, expectedOntoOID string, opts git.RebaseOntoOptions) error {
	if ontoRev == "" || strings.HasPrefix(ontoRev, "-") || strings.ContainsAny(ontoRev, "\x00\r\n") {
		return fmt.Errorf("cannot rebase: invalid target revision")
	}
	if expectedHead == "" || expectedBranch == "" {
		return fmt.Errorf("cannot rebase: the repository is not on a branch")
	}
	if ontoRev == expectedBranch {
		return fmt.Errorf("cannot rebase: the target is the current branch")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot rebase: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot rebase: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot rebase: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if !opts.Stash && rebaseHasTrackedLocalChanges(st) {
		return fmt.Errorf("cannot rebase with uncommitted local changes; commit or stash them first, or choose Stash and reapply local changes")
	}
	gotOID, err := repo.PathHash(ontoRev + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot rebase: the target revision %s no longer resolves to a commit: %w", ontoRev, err)
	}
	if expectedOntoOID != "" && gotOID != expectedOntoOID {
		return fmt.Errorf("cannot rebase: rebase target changed after the dialog opened; review the repository and try again")
	}
	if opts.UpdateRefs {
		caps, err := repo.Capabilities()
		if err != nil {
			return fmt.Errorf("cannot rebase with --update-refs: the Git version could not be verified: %w", err)
		}
		if !caps.RebaseUpdateRefs {
			return fmt.Errorf("cannot rebase with --update-refs: it requires Git 2.38 or newer")
		}
	}
	return repo.RebaseOnto(ontoRev, opts)
}
