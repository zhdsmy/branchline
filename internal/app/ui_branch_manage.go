package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// branchManageUIState is the per-app state of the branch management items
// this file owns: the Rename Branch dialog draft with the commit the branch
// pointed at when the dialog opened, and the clipboard text a copy menu item
// asked for.
type branchManageUIState struct {
	repoGen uint64

	renOpen         bool
	renOld          string
	renExpectedHash string
	renName         string
	renNameChanged  bool

	branchClipboard    string
	hasBranchClipboard bool
}

// branchManageUI returns the app's state of the branch management actions,
// creating it on first use and discarding every open dialog draft when the
// repository generation moved past it: a switch or close makes the captured
// branch and commit meaningless, so the dialog of the previous repository
// never carries over.
func branchManageUI(a *app) *branchManageUIState {
	st := featureState(a, func() *branchManageUIState { return &branchManageUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearRenameDraft()
	}
	return st
}

// clearRenameDraft closes the Rename Branch dialog and drops every value
// captured for it, so a reopened dialog starts from the repository as it is
// then.
func (st *branchManageUIState) clearRenameDraft() {
	st.renOpen = false
	st.renOld = ""
	st.renExpectedHash = ""
	st.renName = ""
	st.renNameChanged = false
}

// branchManageMenuItems adds the Rename, Delete and Copy Branch Name items to
// the context menu of one local branch of the sidebar, behind a separator
// after the merge and rebase items. Delete… never applies to the checked-out
// branch — Git refuses to remove it — so the item disables on it, while
// Rename… stays enabled because renaming the current branch is a normal Git
// operation. Both write entries disable while a repository write runs.
//
// A menu item's Chosen runs inside the menu's build, which receives no
// *ui.Context — and mygo's clipboard is only writable with one, on the main
// thread, in the view or an input handler. The copy choice therefore records
// the text, and renameBranchDialog, the one mount point of this file every
// frame passes, writes it with the frame's own context. Copying never
// disables: it reads the name the menu was opened on, nothing of the
// repository's moving state.
func (a *app) branchManageMenuItems(m *ui.Menu, br git.Branch) {
	st := branchManageUI(a)
	busy := a.session.Busy()
	m.Separator()
	if m.Item("Rename…").Disabled(busy).Chosen() {
		a.openRenameBranchDialog(br)
	}
	if m.Item("Delete…").Disabled(br.Current || busy).Chosen() {
		a.openDeleteBranchDialog(br.Name)
	}
	m.Separator()
	if m.Item("Copy Branch Name").Chosen() {
		st.branchClipboard = br.Name
		st.hasBranchClipboard = true
	}
}

// remoteBranchCopyMenuItem adds the Copy Branch Name item to the context menu
// of one remote-tracking branch of the sidebar, behind a separator. The text
// is the full short name, remote included, such as origin/feature.
func (a *app) remoteBranchCopyMenuItem(m *ui.Menu, rb git.Branch) {
	st := branchManageUI(a)
	m.Separator()
	if m.Item("Copy Branch Name").Chosen() {
		st.branchClipboard = rb.Name
		st.hasBranchClipboard = true
	}
}

// openRenameBranchDialog opens the Rename Branch dialog for br. The name and
// the commit the branch pointed at are captured now, on the UI thread: the
// background worker refuses the write when the branch no longer is exactly
// what the user saw, and the input starts from the old name.
func (a *app) openRenameBranchDialog(br git.Branch) {
	st := branchManageUI(a)
	st.renOpen = true
	st.renOld = br.Name
	st.renExpectedHash = br.Hash
	st.renName = br.Name
	st.renNameChanged = false
}

// renameBranchProblem says why the typed name cannot replace the old one,
// empty when it can. It shares the character and collision rules of the New
// Branch dialog and adds the two conditions renaming must answer on its own:
// a blank name, and the old name again, which is not a rename at all.
func (a *app) renameBranchProblem(old, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Enter a new branch name"
	}
	if name == old {
		return "The new name is the same as the current name"
	}
	return a.newBranchProblem(name)
}

// renameBranchCanSubmit reports whether the dialog's confirm button can run:
// a valid name, different from the old one, and no repository write running.
func (a *app) renameBranchCanSubmit() bool {
	st := branchManageUI(a)
	return a.renameBranchProblem(st.renOld, st.renName) == "" && !a.session.Busy()
}

// renameBranchDialog mounts the Rename Branch dialog — the composition point
// refsSidebar calls on every frame — and flushes the clipboard text a copy
// menu item recorded, which needs a frame's *ui.Context the menu build never
// receives. The flush precedes the dialog so a copy chosen on any frame
// reaches the clipboard in the same frame whether or not the dialog is open.
//
// The input starts from the old name, so the same-name problem holds on the
// first frame; it shows only once the user edits the field, like the New
// Branch dialog hides its problems until the name changed.
func (a *app) renameBranchDialog(c *ui.Context) {
	p := a.pal
	st := branchManageUI(a)
	if st.hasBranchClipboard {
		c.WriteClipboard(st.branchClipboard)
		st.branchClipboard = ""
		st.hasBranchClipboard = false
	}
	if !st.renOpen {
		return
	}

	ui.Modal(c.Key("branch-rename-modal"), &st.renOpen, func() {
		ui.Column(c.Key("branch-rename-modal-body")).Width(460).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Rename Branch").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Branch:", func() {
				ui.Text(c, st.renOld).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Field(c, "New name:", func() {
				in := ui.TextInput(c.Key("branch-rename-input"), &st.renName).FillWidth().AutoFocus().Label("New Branch Name")
				if in.Changed() {
					st.renNameChanged = true
				}
				if in.Submitted() && a.renameBranchCanSubmit() {
					a.submitRenameBranch()
				}
			})
			if problem := a.renameBranchProblem(st.renOld, st.renName); problem != "" && st.renNameChanged {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Row(c.Key("branch-rename-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("branch-rename-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeRenameBranchDialog()
				}
				if ui.PrimaryButton(c.Key("branch-rename-confirm"), "Rename").Label("Confirm Rename Branch").Disabled(!a.renameBranchCanSubmit()).Clicked() {
					a.submitRenameBranch()
				}
			})
		})
	})
	if !st.renOpen {
		a.closeRenameBranchDialog()
	}
}

// closeRenameBranchDialog closes the Rename Branch dialog and clears its
// draft. Canceling, dismissing and submitting all end here so no captured
// branch or commit survives the dialog.
func (a *app) closeRenameBranchDialog() {
	st := branchManageUI(a)
	st.clearRenameDraft()
}

// submitRenameBranch captures the dialog values on the UI thread, closes the
// dialog, and renames the branch through the session's write path. The
// background worker re-reads the repository and refuses the write when what
// it finds differs — the dialog's view of the repository can be stale by the
// time the write starts.
func (a *app) submitRenameBranch() {
	st := branchManageUI(a)
	if a.session.Busy() {
		return
	}
	old := st.renOld
	name := strings.TrimSpace(st.renName)
	expectedHash := st.renExpectedHash
	a.closeRenameBranchDialog()
	a.session.do("Renaming branch "+old, func(repo *git.Repo) error {
		return renameBranchWithGuard(repo, old, expectedHash, name)
	})
}

// renameBranchWithGuard re-validates the repository against what the Rename
// Branch dialog captured before it renames: the new name must still be a
// valid branch name, the old branch must still exist and point at exactly the
// commit the dialog saw, and the new name must still be free. Only then does
// the rename run. Any check failing returns the error without touching a
// single ref — a rename on top of a moved or replaced branch would transplant
// its commits onto a name the user never chose.
func renameBranchWithGuard(repo *git.Repo, old, expectedHash, name string) error {
	if old == "" {
		return fmt.Errorf("cannot rename: no branch")
	}
	if name == "" {
		return fmt.Errorf("cannot rename %s: no new name", old)
	}
	if problem := validateBranchName(name); problem != "" {
		return fmt.Errorf("cannot rename %s: %s", old, problem)
	}
	gotHash, err := repo.PathHash("refs/heads/" + old)
	if err != nil {
		return fmt.Errorf("cannot rename %s: the branch no longer exists: %w", old, err)
	}
	if gotHash != expectedHash {
		return fmt.Errorf("cannot rename %s: the branch moved after the dialog opened; review the repository and try again", old)
	}
	if _, err := repo.PathHash("refs/heads/" + name); err == nil {
		return fmt.Errorf("cannot rename %s: a branch named %s already exists", old, name)
	}
	return repo.RenameBranch(old, name)
}

// validateBranchName is the worker-side form of the branch name rules of the
// New Branch dialog: the characters Git forbids in a ref plus the spellings
// the command line would misread — a leading dash parsed as options,
// whitespace splitting into several arguments, a trailing slash meaning a
// directory, never a ref. Empty is not a problem here: the dialogs and the
// guard answer for it themselves.
func validateBranchName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "-") {
		return "name cannot begin with '-'"
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return "name cannot contain whitespace"
	}
	if strings.HasSuffix(name, "/") {
		return "name cannot end with '/'"
	}
	if p := git.RefNameProblem(name); p != "" {
		return p
	}
	return ""
}
