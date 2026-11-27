package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// quickRebaseUIState is the per-app state of the single-commit quick rebase
// actions: the message editor or confirmation draft with the commit, HEAD,
// branch and parent it was opened against, the asynchronous message read
// behind the editor, and the repository generation they belong to.
type quickRebaseUIState struct {
	repoGen uint64

	open           bool
	action         string
	expectedHead   string
	expectedBranch string
	target         git.Commit
	parentHash     string
	parentLabel    string

	loading       bool
	readErr       error
	message       string
	submitClicked bool
}

// quickRebaseUI returns the app's quick rebase state, creating it on first
// use and discarding every open dialog draft when the repository generation
// moved past it: a switch or close makes the captured commit, HEAD, branch
// and message meaningless, so nothing of the previous repository carries
// over. The message read of the closing dialog is canceled with it, so its
// late result can never land in the next repository's dialog.
func quickRebaseUI(a *app) *quickRebaseUIState {
	st := featureState(a, func() *quickRebaseUIState { return &quickRebaseUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		if st.open || st.loading {
			a.session.cancelRead("ir-quick-read")
		}
		st.clearQuickRebaseDraft()
	}
	return st
}

// clearQuickRebaseDraft closes the dialog and drops every value captured for
// it, so a reopened dialog starts from the repository as it is then.
func (st *quickRebaseUIState) clearQuickRebaseDraft() {
	st.open = false
	st.action = ""
	st.expectedHead = ""
	st.expectedBranch = ""
	st.target = git.Commit{}
	st.parentHash = ""
	st.parentLabel = ""
	st.loading = false
	st.readErr = nil
	st.message = ""
	st.submitClicked = false
}

// commitRebaseQuickMenuItems adds the Quick Rebase Actions submenu to the
// context menu of one commit of the History list: the single-commit rewrites
// of the interactive rebase, without opening the whole plan form. Every item
// shares the operation and repository conditions — a write running, an
// operation in progress, or a detached or unborn HEAD — and the items whose
// shape the commit cannot carry disable on top of it: rewriting a merge
// commit is not offered at all, folding needs exactly one parent, and the
// branch's only commit cannot be dropped without leaving the branch empty.
func (a *app) commitRebaseQuickMenuItems(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	shared := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == ""
	merge := len(cm.Parents) > 1
	fold := shared || len(cm.Parents) != 1
	drop := shared || merge || (len(cm.Parents) == 0 && cm.Hash == snap.Status.Head)
	m.Submenu("Quick Rebase Actions", func(m *ui.Menu) {
		if m.Item("Reword Message…").Disabled(shared || merge).Chosen() {
			a.openQuickRebaseModal(cm, "reword")
		}
		if m.Item("Edit…").Disabled(shared || merge).Chosen() {
			a.openQuickRebaseModal(cm, "edit")
		}
		if m.Item("Squash into Parent…").Disabled(fold).Chosen() {
			a.openQuickRebaseModal(cm, "squash-parent")
		}
		if m.Item("Fixup into Parent…").Disabled(fold).Chosen() {
			a.openQuickRebaseModal(cm, "fixup-parent")
		}
		if m.Item("Drop…").Disabled(drop).Chosen() {
			a.openQuickRebaseModal(cm, "drop")
		}
	})
}

// openQuickRebaseModal opens the quick rebase dialog of action for the commit
// cm. Everything the dialog or its worker needs later is captured now, on the
// UI thread: the commit itself with its parents cloned, its first parent for
// the fold actions, and the HEAD hash and branch the rebase must still find
// when it runs — the background guard refuses the write when either has moved
// since. The message actions start their full-message read here so the text
// is on its way before the dialog first renders.
func (a *app) openQuickRebaseModal(cm git.Commit, action string) {
	st := quickRebaseUI(a)
	snap := a.session.Snapshot()
	cm.Parents = slices.Clone(cm.Parents)
	st.open = true
	st.action = action
	st.expectedHead = snap.Status.Head
	st.expectedBranch = snap.Status.Branch
	st.target = cm
	st.parentHash = ""
	st.parentLabel = ""
	st.readErr = nil
	st.message = ""
	st.submitClicked = false
	if len(cm.Parents) > 0 {
		st.parentHash = cm.Parents[0]
	}
	if action == "squash-parent" {
		label := irShort(st.parentHash)
		for _, other := range snap.Commits {
			if other.Hash == st.parentHash {
				label = irShort(st.parentHash) + " " + other.Subject
				break
			}
		}
		st.parentLabel = label
	}
	if action == "reword" || action == "squash-parent" {
		a.startQuickRebaseMessageRead(st)
	}
}

// startQuickRebaseMessageRead reads the commit messages the open editor
// pre-fills in one background read: the target commit's complete multi-line
// message for a reword — never its one-line subject, which would silently
// drop the body — and the parent's and the target's messages joined oldest
// first for a squash into the parent. The result lands only when the dialog
// still waits for the messages of exactly that target: a retried read, a
// reopened dialog or a switched repository invalidates the sequence in
// between, and the target comparison rejects a stale callback of the same
// generation outright.
func (a *app) startQuickRebaseMessageRead(st *quickRebaseUIState) {
	action := st.action
	targetHash := st.target.Hash
	parentHash := st.parentHash
	st.loading = true
	st.readErr = nil
	sessionRead(a.session, "ir-quick-read", func(repo *git.Repo) (string, error) {
		switch action {
		case "reword":
			return repo.Message(targetHash)
		default:
			parentDetail, err := repo.Detail(parentHash)
			if err != nil {
				return "", err
			}
			if len(parentDetail.Parents) > 1 {
				return "", fmt.Errorf("the parent commit %s is a merge commit and cannot be squashed into", irShort(parentHash))
			}
			parentMessage, err := repo.Message(parentHash)
			if err != nil {
				return "", err
			}
			commitMessage, err := repo.Message(targetHash)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(parentMessage) + "\n\n" + strings.TrimSpace(commitMessage), nil
		}
	}, func(message string, err error) {
		if !st.open || st.target.Hash != targetHash {
			return
		}
		st.loading = false
		if err != nil {
			st.readErr = err
			return
		}
		st.readErr = nil
		st.message = message
	})
}

// commitRebaseQuickModals mounts the modals of the quick rebase actions: one
// dialog at a time, either the message editor of Reword Message and Squash
// into Parent or the confirmation of Edit, Fixup into Parent and Drop.
func (a *app) commitRebaseQuickModals(c *ui.Context) {
	a.quickRebaseMessageModal(c)
	a.quickRebaseConfirmModal(c)
}

// quickRebaseMessageModal is the multi-line message editor of Reword Message
// and Squash into Parent. While the full messages are still being read the
// editor shows a loading line, a failed read shows the error and a Retry —
// either way the confirmation stays disabled because a rewrite must never
// start from a message the user did not see — and a blank message disables it
// too, because Git would refuse it the same way.
func (a *app) quickRebaseMessageModal(c *ui.Context) {
	p := a.pal
	st := quickRebaseUI(a)
	if !st.open || (st.action != "reword" && st.action != "squash-parent") {
		return
	}
	title := "Reword Commit"
	confirm := "Reword"
	if st.action == "squash-parent" {
		title = "Squash into Parent"
		confirm = "Squash"
	}
	blank := strings.TrimSpace(st.message) == ""
	confirmDisabled := a.session.Busy() || st.loading || st.readErr != nil || blank

	ui.Modal(c.Key("quick-rebase-message-modal"), &st.open, func() {
		ui.Column(c.Key("quick-rebase-message-body")).Width(480).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Commit:", func() {
				ui.Text(c, st.target.Short()+" "+st.target.Subject).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			if st.action == "squash-parent" {
				ui.Field(c, "Into:", func() {
					ui.Text(c, st.parentLabel).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
				})
			}
			switch {
			case st.loading:
				ui.Text(c, "Loading commit message…").FontSize(12.5).TextColor(p.muted)
			case st.readErr != nil:
				ui.Text(c, st.readErr.Error()).FontSize(12).TextColor(p.danger).MaxWidth(444)
				if ui.Button(c.Key("quick-rebase-retry"), "Retry").Label("Retry").Clicked() {
					a.startQuickRebaseMessageRead(st)
				}
			default:
				ui.TextArea(c.Key("quick-rebase-message"), &st.message).
					Label("Commit message").
					Height(110).
					FillWidth()
				if blank {
					ui.Text(c, "The commit message cannot be empty.").FontSize(11.5).TextColor(p.danger)
				}
			}
			if err := a.session.ActionErr(); st.submitClicked && err != nil {
				ui.Text(c, err.Error()).FontSize(12).TextColor(p.danger).MaxWidth(444)
			}
			ui.Row(c.Key("quick-rebase-message-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("quick-rebase-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeQuickRebaseModal()
				}
				if ui.PrimaryButton(c.Key("quick-rebase-confirm"), confirm).Label(confirm).Disabled(confirmDisabled).Clicked() {
					a.submitQuickRebase()
				}
			})
		})
	})
	if !st.open {
		a.closeQuickRebaseModal()
	}
}

// quickRebaseConfirmModal is the second confirmation of the quick rebase
// actions that need no message: Edit stops the rebase on the commit for
// amending, Fixup into Parent folds it away keeping the parent's message, and
// Drop removes it from the branch. Each names the commit and says what the
// replay does before the user confirms.
func (a *app) quickRebaseConfirmModal(c *ui.Context) {
	p := a.pal
	st := quickRebaseUI(a)
	if !st.open || (st.action != "edit" && st.action != "fixup-parent" && st.action != "drop") {
		return
	}
	var title, description, confirm string
	switch st.action {
	case "edit":
		title = "Edit " + st.target.Short() + "?"
		description = "The rebase stops at the commit so it can be amended from Local Changes. Continue or abort it from the rebase banner."
		confirm = "Edit"
	case "fixup-parent":
		title = "Fixup " + st.target.Short() + " into its parent?"
		description = "The commit is folded into its parent, which keeps its original message. Later commits are replayed."
		confirm = "Fixup"
	default:
		title = "Drop " + st.target.Short() + "?"
		description = "The commit is removed from the current branch. Later commits are replayed."
		confirm = "Drop"
	}

	ui.Modal(c.Key("quick-rebase-confirm-modal"), &st.open, func() {
		ui.Column(c.Key("quick-rebase-confirm-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, description).FontSize(13).TextColor(p.muted).MaxWidth(394)
			if err := a.session.ActionErr(); st.submitClicked && err != nil {
				ui.Text(c, err.Error()).FontSize(12).TextColor(p.danger).MaxWidth(394)
			}
			ui.Row(c.Key("quick-rebase-confirm-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("quick-rebase-confirm-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeQuickRebaseModal()
				}
				if ui.PrimaryButton(c.Key("quick-rebase-confirm-confirm"), confirm).Label(confirm).Disabled(a.session.Busy()).Clicked() {
					a.submitQuickRebase()
				}
			})
		})
	})
	if !st.open {
		a.closeQuickRebaseModal()
	}
}

// closeQuickRebaseModal closes the quick rebase dialog, cancels its message
// read and clears the draft. Canceling, dismissing, switching the repository
// and finishing a rebase all end here so no captured commit or message
// survives the dialog, and a repository untouched by the write stays exactly
// as it was.
func (a *app) closeQuickRebaseModal() {
	st := quickRebaseUI(a)
	if st.open || st.loading {
		a.session.cancelRead("ir-quick-read")
	}
	st.clearQuickRebaseDraft()
}

// submitQuickRebase starts the rebase the dialog describes through the
// session's write path. The dialog stays open with its draft: the background
// worker re-validates everything it assumed, and a refusal is the user's to
// fix, so only a started-and-finished rebase closes the dialog. The action,
// the hashes, the branch and the message are captured here by value — the
// worker never reads mutable dialog state.
func (a *app) submitQuickRebase() {
	st := quickRebaseUI(a)
	if a.session.Busy() {
		return
	}
	if st.action == "reword" || st.action == "squash-parent" {
		if st.loading || st.readErr != nil || strings.TrimSpace(st.message) == "" {
			return
		}
	}
	st.submitClicked = true
	action := st.action
	expectedHead := st.expectedHead
	expectedBranch := st.expectedBranch
	targetHash := st.target.Hash
	parentHash := st.parentHash
	message := st.message
	a.session.doThen("Rebasing", func(repo *git.Repo) error {
		return quickRebaseWithGuard(repo, action, expectedHead, expectedBranch, targetHash, parentHash, message)
	}, func(err error) {
		if err == nil {
			a.closeQuickRebaseModal()
		}
	})
}

// quickRebaseHasTrackedLocalChanges reports uncommitted changes of tracked
// files: any staged entry, or an unstaged entry that is neither untracked nor
// ignored.
func quickRebaseHasTrackedLocalChanges(st git.Status) bool {
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

// quickRebaseWithGuard re-validates the repository against what the quick
// rebase dialog captured before it rewrites the branch: the captured values
// must be complete, the status must still be readable, no merge, rebase,
// cherry-pick or revert may be in progress, HEAD and the branch must still be
// exactly where the dialog saw them, and the index and working tree must
// carry no uncommitted tracked changes — the backend always autostashes, but
// these actions offer no such choice, so local work is the user's to commit
// or stash first. Then the target is resolved live: it must not be a merge
// commit — interactive rebase replays merges as their changes, which is not
// what a single-commit rewrite means — and, for the fold actions, the parent
// must still be the target's own first parent and no merge either. The fresh
// plan read refuses a base that is not an ancestor of HEAD — as when the
// commit was right-clicked on another forked branch — and a range with a
// merge inside it, and the captured commits must sit in it adjacent. Only
// then does the rebase run. Any check failing returns the error without
// touching the working tree, the index, HEAD or any branch: the dialog's
// snapshot of the repository can be stale by the time the write starts, and
// replaying a rewrite the user never confirmed against would destroy commits.
func quickRebaseWithGuard(repo *git.Repo, action, expectedHead, expectedBranch, targetHash, parentHash, message string) error {
	switch action {
	case "reword", "edit", "squash-parent", "fixup-parent", "drop":
	default:
		return fmt.Errorf("unknown quick rebase action %q", action)
	}
	if expectedHead == "" || expectedBranch == "" {
		return fmt.Errorf("cannot rebase: the repository is not on a branch")
	}
	if targetHash == "" {
		return fmt.Errorf("cannot rebase: no target commit")
	}
	if (action == "reword" || action == "squash-parent") && strings.TrimSpace(message) == "" {
		return fmt.Errorf("cannot rebase: the commit message is empty")
	}
	if (action == "squash-parent" || action == "fixup-parent") && parentHash == "" {
		return fmt.Errorf("cannot rebase: no parent commit to fold into")
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
	if quickRebaseHasTrackedLocalChanges(st) {
		return fmt.Errorf("cannot rebase with uncommitted local changes; commit or stash them first")
	}
	targetDetail, err := repo.Detail(targetHash)
	if err != nil {
		return fmt.Errorf("cannot rebase: the target commit %s no longer exists: %w", irShort(targetHash), err)
	}
	if len(targetDetail.Parents) > 1 {
		return fmt.Errorf("cannot rebase: the target commit %s is a merge commit", irShort(targetHash))
	}
	var onto string
	switch action {
	case "squash-parent", "fixup-parent":
		parentDetail, err := repo.Detail(parentHash)
		if err != nil {
			return fmt.Errorf("cannot rebase: the parent commit %s no longer exists: %w", irShort(parentHash), err)
		}
		if len(parentDetail.Parents) > 1 {
			return fmt.Errorf("cannot rebase: the parent commit %s is a merge commit", irShort(parentHash))
		}
		if len(targetDetail.Parents) == 0 || targetDetail.Parents[0] != parentHash {
			return fmt.Errorf("cannot rebase: the target commit's parent changed after the dialog opened; review the repository and try again")
		}
		onto = "--root"
		if len(parentDetail.Parents) > 0 {
			onto = parentDetail.Parents[0]
		}
	default:
		onto = "--root"
		if len(targetDetail.Parents) > 0 {
			onto = targetDetail.Parents[0]
		}
	}
	live, err := repo.RebaseRangePlan(onto)
	if err != nil {
		return fmt.Errorf("cannot rebase: the commit can no longer be rewritten: %w", err)
	}
	targetIndex := slices.IndexFunc(live, func(step git.RebaseTodo) bool { return step.Hash == targetHash })
	if targetIndex < 0 {
		return fmt.Errorf("cannot rebase: the target commit %s is no longer on the current branch", irShort(targetHash))
	}
	if action == "squash-parent" || action == "fixup-parent" {
		if targetIndex == 0 || live[targetIndex-1].Hash != parentHash {
			return fmt.Errorf("cannot rebase: the parent commit %s is no longer immediately before the target", irShort(parentHash))
		}
	}
	plan := make([]git.RebaseTodo, len(live))
	copy(plan, live)
	switch action {
	case "reword":
		plan[targetIndex].Action = "pick"
		plan[targetIndex].PreparedMessage = message
	case "edit":
		plan[targetIndex].Action = "edit"
	case "squash-parent":
		plan[targetIndex].Action = "fixup"
		plan[targetIndex].Subject = ""
		plan[targetIndex].PreparedMessage = message
	case "fixup-parent":
		plan[targetIndex].Action = "fixup"
		plan[targetIndex].Subject = ""
		plan[targetIndex].PreparedMessage = ""
	case "drop":
		plan[targetIndex].Action = "drop"
	}
	return repo.RebaseWithOptions(onto, plan, git.RebaseOptions{})
}
