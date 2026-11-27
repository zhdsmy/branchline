package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// mergeUIState is the per-app state of the merge dialog: the draft with the
// HEAD, branch and merge source it was opened against, the asynchronous
// conflict prediction, and the repository generation they belong to.
type mergeUIState struct {
	repoGen uint64

	open              bool
	sourceRev         string
	sourceLabel       string
	expectedSourceOID string
	expectedHead      string
	expectedBranch    string
	style             git.MergeStyle
	predictState      string // "", "clean" or "conflicts"
}

// mergeUI returns the app's merge-dialog state, creating it on first use and
// discarding every open dialog draft when the repository generation moved
// past it: a switch or close makes the captured source, HEAD and branch
// meaningless, so nothing of the previous repository carries over. The
// conflict prediction of the old repository is invalidated at the same time
// so its late result can never land on the new repository's dialog.
func mergeUI(a *app) *mergeUIState {
	st := featureState(a, func() *mergeUIState { return &mergeUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		a.session.cancelRead("merge-predict")
		st.clearDraft()
	}
	return st
}

// clearDraft closes the merge dialog and drops every value captured for it,
// so a reopened dialog starts from the repository as it is then.
func (st *mergeUIState) clearDraft() {
	st.open = false
	st.sourceRev = ""
	st.sourceLabel = ""
	st.expectedSourceOID = ""
	st.expectedHead = ""
	st.expectedBranch = ""
	st.style = git.MergeFastForward
	st.predictState = ""
}

// mergeStyleChoices are the four merge modes the dialog offers, in the
// dialog's order, with the one-line explanation shown under the radio group
// for the chosen one.
var mergeStyleChoices = []struct {
	style git.MergeStyle
	label string
	note  string
}{
	{git.MergeFastForward, "Fast-forward if possible", "Fast-forward when possible; otherwise create a merge commit."},
	{git.MergeNoFastForward, "No Fast-Forward", "Always create a merge commit (--no-ff)."},
	{git.MergeSquash, "Squash", "Stage squashed changes without creating a merge commit (--squash)."},
	{git.MergeNoCommit, "Don't Commit", "Merge without automatically committing (--no-commit)."},
}

func mergeStyleNote(style git.MergeStyle) string {
	for _, m := range mergeStyleChoices {
		if m.style == style {
			return m.note
		}
	}
	return ""
}

// mergeIntoLabel is how the menu names the merge target: the current branch
// in quotes, or bare HEAD for a detached checkout.
func mergeIntoLabel(branch string) string {
	if branch == "" {
		return "HEAD"
	}
	return "'" + branch + "'"
}

// branchMergeMenuItems adds the Merge item to the context menu of a local
// branch of the sidebar. Merging the checked-out branch into itself is
// meaningless, so the item disables on it as well as during a write, an
// operation in progress and an unborn repository.
func (a *app) branchMergeMenuItems(m *ui.Menu, br git.Branch) {
	snap := a.session.Snapshot()
	label := "Merge into " + mergeIntoLabel(snap.Status.Branch) + "…"
	disabled := br.Current || a.session.Busy() || snap.Status.State != "" || snap.Status.Head == ""
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openMergeDialog(br.Name, br.Name, br.Hash)
	}
}

// remoteBranchMergeMenuItems adds the Merge item to the context menu of a
// remote-tracking branch of the sidebar.
func (a *app) remoteBranchMergeMenuItems(m *ui.Menu, rb git.Branch) {
	snap := a.session.Snapshot()
	label := "Merge into " + mergeIntoLabel(snap.Status.Branch) + "…"
	disabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == ""
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openMergeDialog(rb.Name, rb.Name, rb.Hash)
	}
}

// commitMergeMenuItems adds the Merge item to the context menu of one commit
// of the History list. Merging HEAD into itself can never change anything, so
// the item additionally disables on the current commit.
func (a *app) commitMergeMenuItems(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	label := "Merge into " + mergeIntoLabel(snap.Status.Branch) + "…"
	disabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || cm.Hash == snap.Status.Head
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openMergeDialog(cm.Hash, cm.Short()+" "+cm.Subject, cm.Hash)
	}
}

// openMergeDialog opens the Merge dialog for the source revision. Everything
// the dialog or its worker needs later is captured now, on the UI thread: the
// revision handed to Git, its display label, the hash the source pointed at,
// and the HEAD hash and branch the merge must still find when it runs — the
// background guard refuses the write when any of them has moved since. The
// conflict prediction starts at the same time, scoped so closing the dialog
// or switching repositories discards its late result.
func (a *app) openMergeDialog(sourceRev, sourceLabel, sourceOID string) {
	st := mergeUI(a)
	snap := a.session.Snapshot()
	st.open = true
	st.sourceRev = sourceRev
	st.sourceLabel = sourceLabel
	st.expectedSourceOID = sourceOID
	st.expectedHead = snap.Status.Head
	st.expectedBranch = snap.Status.Branch
	st.style = git.MergeFastForward
	st.predictState = ""
	rev := sourceRev
	sessionRead(a.session, "merge-predict", func(r *git.Repo) (bool, error) {
		return r.MergeConflicts(rev, "HEAD")
	}, func(conflicts bool, err error) {
		if !st.open || st.sourceRev != rev {
			return
		}
		st.predictState = ""
		if err != nil {
			return
		}
		if conflicts {
			st.predictState = "conflicts"
		} else {
			st.predictState = "clean"
		}
	})
}

// mergeDialog is the Merge dialog: it merges the named branch,
// remote-tracking branch or commit into the checked-out branch — or the
// detached HEAD — with one of the four merge modes Git offers. The conflict
// prediction informs but never blocks: merges with conflicts are a normal
// workflow, and a git too old to predict them hides the line entirely.
func (a *app) mergeDialog(c *ui.Context) {
	p := a.pal
	st := mergeUI(a)
	if !st.open {
		return
	}
	into := st.expectedBranch
	if into == "" {
		into = "HEAD (detached)"
	}

	ui.Modal(c.Key("merge-modal"), &st.open, func() {
		ui.Column(c.Key("merge-modal-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Merge").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Merge:", func() {
				ui.Text(c, st.sourceLabel).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			ui.Field(c, "Into:", func() {
				ui.Text(c, into).FontSize(12).TextColor(p.text)
			})
			ui.RadioGroup(c, func() {
				for _, m := range mergeStyleChoices {
					ui.Radio(c, &st.style, m.style, m.label)
				}
			}).Label("Merge Type")
			ui.Text(c, mergeStyleNote(st.style)).FontSize(12).TextColor(p.muted)
			switch st.predictState {
			case "conflicts":
				ui.Text(c, "Merge will cause conflicts").FontSize(12).TextColor(p.danger)
			case "clean":
				ui.Text(c, "Merge can be done without conflicts").FontSize(12).TextColor(p.muted)
			}
			ui.Row(c.Key("merge-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("merge-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeMergeDialog()
				}
				if ui.PrimaryButton(c.Key("merge-confirm"), "Confirm Merge").Label("Confirm Merge").Disabled(a.session.Busy()).Clicked() {
					a.submitMerge()
				}
			})
		})
	})
	if !st.open {
		a.closeMergeDialog()
	}
}

// closeMergeDialog closes the Merge dialog, clears its draft and invalidates
// any conflict prediction still in flight so its late result is discarded.
// Canceling, dismissing, submitting and switching repositories all end here,
// so no captured source or answer survives the dialog.
func (a *app) closeMergeDialog() {
	st := mergeUI(a)
	a.session.cancelRead("merge-predict")
	st.clearDraft()
}

// submitMerge runs the merge the dialog describes through the session's write
// path. The background worker re-validates everything the dialog assumed —
// the HEAD, the branch, the absence of an operation in progress, a clean
// index and working tree, and the merge source itself — because any of it can
// change between the click and the write.
func (a *app) submitMerge() {
	st := mergeUI(a)
	if a.session.Busy() {
		return
	}
	sourceRev := st.sourceRev
	sourceOID := st.expectedSourceOID
	expectedHead := st.expectedHead
	expectedBranch := st.expectedBranch
	style := st.style
	a.closeMergeDialog()
	a.session.do("Merging", func(repo *git.Repo) error {
		return mergeWithGuard(repo, expectedHead, expectedBranch, sourceRev, sourceOID, style)
	})
}

// mergeHasTrackedLocalChanges reports uncommitted changes of tracked files:
// any staged entry, or an unstaged entry that is neither untracked nor
// ignored.
func mergeHasTrackedLocalChanges(st git.Status) bool {
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

// mergeWithGuard re-validates the repository against what the merge dialog
// captured before it merges: the source revision must be a safe argument, no
// merge, rebase, cherry-pick or revert may be in progress, the status must
// still be readable, HEAD and the branch must still be exactly where the
// dialog saw them, the index and working tree must carry no uncommitted
// tracked changes, and the source must still resolve to the commit the user
// saw. Only then does the merge run. Any check failing returns the error
// without touching the working tree, the index or HEAD — merging over local
// changes or onto a moved HEAD would either overwrite them or entangle them
// in a merge the user never asked for.
func mergeWithGuard(repo *git.Repo, expectedHead, expectedBranch, sourceRev, expectedSourceOID string, style git.MergeStyle) error {
	switch style {
	case git.MergeFastForward, git.MergeNoFastForward, git.MergeSquash, git.MergeNoCommit:
	default:
		return fmt.Errorf("unknown merge type")
	}
	if sourceRev == "" {
		return fmt.Errorf("cannot merge: no merge source")
	}
	if strings.HasPrefix(sourceRev, "-") || strings.ContainsAny(sourceRev, "\x00\r\n") {
		return fmt.Errorf("cannot merge: invalid source revision")
	}
	if expectedHead == "" {
		return fmt.Errorf("cannot merge: the repository has no commits")
	}
	if expectedBranch != "" && sourceRev == expectedBranch {
		return fmt.Errorf("cannot merge %s into itself", sourceRev)
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot merge: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot merge: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot merge: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if mergeHasTrackedLocalChanges(st) {
		return fmt.Errorf("cannot merge with uncommitted local changes; commit or stash them first")
	}
	gotOID, err := repo.PathHash(sourceRev + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot merge: merge target %s no longer resolves to a commit: %w", sourceRev, err)
	}
	if expectedSourceOID != "" && gotOID != expectedSourceOID {
		return fmt.Errorf("cannot merge: merge target changed after dialog opened")
	}
	return repo.MergeWith(sourceRev, style)
}
