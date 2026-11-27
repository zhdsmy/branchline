package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// branchCreateUIState is the per-app state of the New Branch dialog the
// context menu of a local branch, a remote-tracking branch or a tag opens:
// the source reference and the exact commit it pointed at when the dialog
// opened, the HEAD hash and branch a checked-out create must still find, the
// name and checkout draft, and the repository generation they belong to. It
// lives in the app-keyed table because app is a shared file.
type branchCreateUIState struct {
	repoGen uint64

	open            bool
	sourceRef       string
	startPoint      string
	expectedOID     string
	expectedHead    string
	expectedCurrent string
	name            string
	checkout        bool
	nameChanged     bool
}

var branchCreateUIStates = struct {
	sync.Mutex
	m map[*app]*branchCreateUIState
}{m: map[*app]*branchCreateUIState{}}

// branchCreateUI returns the app's branch-create state, creating it on first
// use and discarding an open draft when the repository generation moved past
// it: a switch makes the captured source commit and HEAD meaningless, so
// nothing of the previous repository carries over. It runs on the UI thread.
func (a *app) branchCreateUI() *branchCreateUIState {
	branchCreateUIStates.Lock()
	defer branchCreateUIStates.Unlock()
	st := branchCreateUIStates.m[a]
	if st == nil {
		st = &branchCreateUIState{}
		branchCreateUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		st.clearBranchCreateDraft()
	}
	return st
}

// clearBranchCreateDraft closes the dialog and drops every value captured
// for it, so a reopened dialog starts from the repository as it is then.
func (st *branchCreateUIState) clearBranchCreateDraft() {
	st.open = false
	st.sourceRef = ""
	st.startPoint = ""
	st.expectedOID = ""
	st.expectedHead = ""
	st.expectedCurrent = ""
	st.name = ""
	st.checkout = true
	st.nameChanged = false
}

// branchCreateMenuItems adds the create actions to the context menu of one
// local branch of the sidebar: the interactive rebase of the current branch
// onto it, then a new branch or tag starting at exactly the commit the row
// names. The interactive rebase item carries the same repository conditions
// as the plain rebase — its guard requires a branch to move — plus the
// current branch itself, whose plan would be empty. Creating needs only a
// commit to point at, so those two items stay enabled on the current branch
// and disable only while a write is in flight or the row names no commit.
func (a *app) branchCreateMenuItems(m *ui.Menu, br git.Branch) {
	snap := a.session.Snapshot()
	rebaseDisabled := br.Current || a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == ""
	if m.Item("Interactively Rebase on '" + br.Name + "'…").Disabled(rebaseDisabled).Chosen() {
		a.openInteractiveRebaseModal(git.Commit{Hash: br.Hash, Subject: br.Name})
	}
	m.Separator()
	createDisabled := a.session.Busy() || br.Hash == ""
	if m.Item("New Branch…").Disabled(createDisabled).Chosen() {
		a.openBranchCreateDialog(br.FullName, br.Name, br.Hash)
	}
	if m.Item("New Tag…").Disabled(createDisabled).Chosen() {
		a.openNewTagDialog(br.Hash)
		a.tagsUI().newTargetHint = br.Name + " (" + shortTagHash(br.Hash) + ")"
	}
}

// openBranchCreateDialog opens the New Branch dialog for the source revision
// a branch or tag row names: sourceRef is the full reference the source is
// re-read through in the write worker — refs/heads/… of a local branch,
// refs/remotes/… of a remote-tracking branch, refs/tags/… of a tag — label
// is how the dialog names it, and hash is the exact commit the reference
// pointed at. Everything is captured now, on the UI thread: the new branch
// is created at that commit even if the source moves afterwards, because the
// background guard refuses the write when the reference, HEAD or the current
// branch has moved since.
func (a *app) openBranchCreateDialog(sourceRef, label, hash string) {
	st := a.branchCreateUI()
	snap := a.session.Snapshot()
	st.open = true
	st.sourceRef = sourceRef
	st.startPoint = label + " (" + shortTagHash(hash) + ")"
	st.expectedOID = hash
	st.expectedHead = snap.Status.Head
	st.expectedCurrent = snap.Status.Branch
	st.name = ""
	st.checkout = true
	st.nameChanged = false
}

// branchCreateCanSubmit reports whether the dialog's Create Branch button is
// enabled: a valid name, checked live against git's rules and the local
// branches, while no write is in flight.
func (a *app) branchCreateCanSubmit() bool {
	st := a.branchCreateUI()
	name := strings.TrimSpace(st.name)
	return name != "" && a.newBranchProblem(name) == "" && !a.session.Busy()
}

// branchCreateDialog is the modal creating a branch at the exact commit the
// clicked reference — a local branch, a remote-tracking branch or a tag —
// pointed at when the dialog opened, checking it out when the box stays
// ticked. It shares no field with the sidebar's New Branch entry dialog,
// which always starts at HEAD.
func (a *app) branchCreateDialog(c *ui.Context) {
	p := a.pal
	st := a.branchCreateUI()
	if !st.open {
		return
	}
	if ui.Modal(c.Key("branch-create-modal"), &st.open, func() {
		ui.Column(c.Key("branch-create-body")).Width(440).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "New Branch").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Field(c, "Start point:", func() {
				ui.Text(c, st.startPoint).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Field(c, "Name:", func() {
				in := ui.TextInput(c, &st.name).FillWidth().AutoFocus().Placeholder("Branch name").Label("New Branch From Name")
				if in.Changed() {
					st.nameChanged = true
				}
				if in.Submitted() && a.branchCreateCanSubmit() {
					a.submitBranchCreate()
				}
			})
			if problem := a.newBranchProblem(st.name); problem != "" && (st.nameChanged || strings.TrimSpace(st.name) != "") {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger)
			}
			ui.Checkbox(c.Key("branch-create-checkout"), &st.checkout, "Checkout after create")
			ui.Row(c.Key("branch-create-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("branch-create-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeBranchCreateDialog()
				}
				if ui.PrimaryButton(c.Key("branch-create-confirm"), "Create Branch").Label("Confirm New Branch From").Disabled(!a.branchCreateCanSubmit()).Clicked() {
					a.submitBranchCreate()
				}
			})
		})
	}).Dismissed() {
		a.closeBranchCreateDialog()
	}
	if !st.open {
		a.closeBranchCreateDialog()
	}
}

// closeBranchCreateDialog closes the dialog and drops the captured source
// commit with it, so a closed dialog can never submit.
func (a *app) closeBranchCreateDialog() {
	a.branchCreateUI().clearBranchCreateDraft()
}

// submitBranchCreate captures the dialog values on the UI thread, closes the
// dialog, and creates the branch through the session's write path: the
// worker re-verifies on the repository itself that the source reference
// still points at the captured commit and the new name is still free before
// anything is created.
func (a *app) submitBranchCreate() {
	st := a.branchCreateUI()
	name := strings.TrimSpace(st.name)
	checkout := st.checkout
	sourceRef := st.sourceRef
	expectedOID := st.expectedOID
	expectedHead := st.expectedHead
	expectedCurrent := st.expectedCurrent
	a.closeBranchCreateDialog()
	if name == "" || expectedOID == "" {
		return
	}
	a.session.do("Create branch "+name, func(r *git.Repo) error {
		return createBranchFromGuarded(r, name, sourceRef, expectedOID, expectedHead, expectedCurrent, checkout)
	})
}

// refKindName reads the noun and short name a full reference is described
// with in the errors of the create guard: a local branch, a remote-tracking
// branch and a tag are all a "branch" or "tag" of the same short name the
// sidebar rows show, so the message names exactly what the user clicked.
func refKindName(ref string) (noun, short string) {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch", strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/remotes/"):
		return "branch", strings.TrimPrefix(ref, "refs/remotes/")
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag", strings.TrimPrefix(ref, "refs/tags/")
	}
	return "reference", ref
}

// createBranchFromGuarded re-validates the repository against what the New
// Branch dialog captured before it creates anything: the name must still be
// a legal, unused branch name, the source reference — a branch, a
// remote-tracking branch or a tag — must still exist and point at the exact
// commit the dialog opened with, and for a checked-out create the status
// must be readable, no operation in progress, and HEAD and the current
// branch exactly what the dialog saw. Only then does the branch create run —
// never forced — so a repository that moved on since the click is never
// written from a stale snapshot. When the checkout would overwrite
// uncommitted local work, git itself refuses and the working tree keeps
// every byte; the error reaches the user verbatim.
func createBranchFromGuarded(repo *git.Repo, name, sourceRef, expectedOID, expectedHead, expectedCurrent string, checkout bool) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("cannot create branch: invalid branch name %q", name)
	}
	if p := git.RefNameProblem(name); p != "" {
		return fmt.Errorf("cannot create branch: %s", p)
	}
	noun, short := refKindName(sourceRef)
	sourceOID, err := repo.PathHash(sourceRef + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot create branch: the source %s %s no longer exists; review the repository and try again", noun, short)
	}
	if sourceOID != expectedOID {
		return fmt.Errorf("cannot create branch: the source %s %s moved after the dialog opened; review the repository and try again", noun, short)
	}
	if _, err := repo.PathHash("refs/heads/" + name); err == nil {
		return fmt.Errorf("cannot create branch: a branch named %s already exists", name)
	}
	if checkout {
		st, err := repo.Status()
		if err != nil {
			return fmt.Errorf("cannot create branch: reading the repository state failed: %w", err)
		}
		if st.State != "" {
			return fmt.Errorf("cannot create branch: a %s is in progress; finish or abort it first", st.State)
		}
		if st.Head != expectedHead || st.Branch != expectedCurrent {
			return fmt.Errorf("cannot create branch: HEAD or the current branch changed after the dialog opened; review the repository and try again")
		}
	}
	return repo.CreateBranch(name, expectedOID, checkout)
}

// branchCreateModals mounts the modals of the branch context menu create
// actions: the New Branch dialog itself, and the interactive rebase form on
// every page but History — there the commit actions already mount it, and
// rendering it twice in one frame would duplicate the form.
func (a *app) branchCreateModals(c *ui.Context) {
	a.branchCreateDialog(c)
	if a.route.page != pageHistory {
		a.interactiveRebaseModal(c)
	}
}
