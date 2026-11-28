package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// branchCreateFixture builds a repository with main checked out at its tip
// and feature at its own older commit, returning the fixture and the hash
// feature points at.
func branchCreateFixture(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CheckoutNewBranch("feature")
	featureTip := repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Checkout("main")
	repo.CommitFile("main.txt", "main\n", "main work")
	return repo, featureTip
}

// openBranchCreateMenu right-clicks the local branch row and verifies the
// menu offers the create items.
func openBranchCreateMenu(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
}

// chooseMenuItemFromBranch right-clicks the branch row, chooses item, and
// renders the result.
func chooseMenuItemFromBranch(t *testing.T, h *testHarness, branch, item string) {
	t.Helper()
	openBranchCreateMenu(t, h, branch)
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q: %v\nmenu=%q", item, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// openNewBranchFromDialog opens the New Branch dialog of the branch through
// the real context menu and verifies it renders.
func openNewBranchFromDialog(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	chooseMenuItemFromBranch(t, h, branch, "New Branch…")
	if !hasExactText(h.ui, "Start point:") {
		t.Fatalf("New Branch dialog did not open; texts=%q", h.ui.Texts())
	}
}

// typeBranchCreateName types name into the dialog's autofocused name field.
func typeBranchCreateName(t *testing.T, h *testHarness, name string) {
	t.Helper()
	h.ui.Type(name)
	h.ui.Frame()
}

// confirmBranchCreateButton returns the state of the dialog's create button.
func confirmBranchCreateButton(t *testing.T, h *testHarness) (ui.AccessInfo, bool) {
	t.Helper()
	return h.ui.AccessByRole(ui.RoleButton, "Confirm New Branch From")
}

// The context menu of a local branch offers the interactive rebase entry and
// the create entries after the merge and rebase entries, separated once:
// the interactive rebase directly behind the plain rebase, then a separator
// before New Branch and New Tag.
func TestBranchCreateMenuItemsOrder(t *testing.T) {
	repo, _ := branchCreateFixture(t, "bc-menu-order")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openBranchCreateMenu(t, h, "feature")
	menu := h.ui.Menu()
	ir := slices.Index(menu, "Interactively Rebase on 'feature'…")
	nb := slices.Index(menu, "New Branch…")
	nt := slices.Index(menu, "New Tag…")
	if ir < 0 || nb < 0 || nt < 0 {
		t.Fatalf("menu %q missing the create items", menu)
	}
	sepOffset := slices.Index(menu[ir+1:], "-")
	if sepOffset < 0 {
		t.Fatalf("menu %q missing separator after interactive rebase", menu)
	}
	sep := ir + 1 + sepOffset
	if !(ir < sep && sep < nb && nb < nt) {
		t.Fatalf("menu order %q: want interactive rebase, separator, New Branch, New Tag", menu)
	}
	rebase := slices.Index(menu, "Rebase 'main' on 'feature'…")
	if rebase < 0 || rebase+1 != ir {
		t.Fatalf("menu %q: interactive rebase must directly follow the plain rebase item", menu)
	}
	h.ui.CloseMenu()
}

// The New Branch dialog of a branch names its start point, validates the
// typed name live against git's rules and the existing branches — disabling
// Create Branch on every defect — and creates the branch at exactly the
// commit the source branch pointed at, checking it out when the box stays
// ticked. The dialog centers horizontally in the window with its buttons
// inside.
func TestBranchCreateDialogValidatesAndCreatesWithCheckout(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-create-checkout")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")

	// The start point names the source branch and its commit.
	if !h.ui.HasText("feature (" + short7(featureTip) + ")") {
		t.Fatalf("start point hint missing; texts=%q", h.ui.Texts())
	}

	// Empty name: submit disabled.
	if btn, ok := confirmBranchCreateButton(t, h); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with empty name = %+v (found=%v), want disabled", btn, ok)
	}

	// Illegal characters disable submit and show the problem.
	typeBranchCreateName(t, h, "bad..name")
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("illegal name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := confirmBranchCreateButton(t, h); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with illegal name = %+v, want disabled", btn)
	}

	// Collision with an existing branch disables submit.
	clearInput(t, h.ui)
	typeBranchCreateName(t, h, "main")
	if !h.ui.HasText("already exists") {
		t.Fatalf("duplicate name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := confirmBranchCreateButton(t, h); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with duplicate name = %+v, want disabled", btn)
	}

	// A dash-leading name and a whitespace name are rejected the same way.
	clearInput(t, h.ui)
	typeBranchCreateName(t, h, "-l")
	if !h.ui.HasText("cannot begin with '-'") {
		t.Fatalf("dash-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := confirmBranchCreateButton(t, h); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with dash name = %+v, want disabled", btn)
	}
	clearInput(t, h.ui)
	typeBranchCreateName(t, h, "has space")
	if !h.ui.HasText("cannot contain whitespace") {
		t.Fatalf("whitespace problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, ok := confirmBranchCreateButton(t, h); !ok || !btn.Disabled {
		t.Fatalf("Create Branch with whitespace name = %+v, want disabled", btn)
	}

	// Layout: the dialog centers horizontally in the 1200-wide window and
	// both buttons sit inside it.
	assertDialogLayout(t, h, "Cancel", "Confirm New Branch From")

	// A valid name enables submit; the create lands exactly on the source
	// commit even though main has moved past it.
	clearInput(t, h.ui)
	typeBranchCreateName(t, h, "release-1")
	if btn, ok := confirmBranchCreateButton(t, h); !ok || btn.Disabled {
		t.Fatalf("Create Branch with valid name = %+v, want enabled", btn)
	}
	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if !branchExists(t, repo, "release-1") {
		t.Fatal("release-1 was not created in the repository")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "release-1")); got != featureTip {
		t.Fatalf("release-1 points at %q, want the source commit %q", got, featureTip)
	}
	if got := currentBranch(t, repo); got != "release-1" {
		t.Fatalf("current branch after create = %q, want release-1 (checkout was ticked)", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if hasExactText(h.ui, "Start point:") {
		t.Fatal("New Branch dialog stayed open after successful create")
	}
}

// Creating without the checkout box ticked adds the branch at the source
// commit but keeps the current branch unchanged.
func TestBranchCreateWithoutCheckout(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-no-checkout")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "topic-only")
	if err := h.ui.Click("Checkout after create"); err != nil {
		t.Fatalf("untick Checkout after create: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if !branchExists(t, repo, "topic-only") {
		t.Fatal("topic-only was not created")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "topic-only")); got != featureTip {
		t.Fatalf("topic-only points at %q, want the source commit %q", got, featureTip)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch = %q, want main unchanged", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
}

// Pressing Enter in the name field submits the dialog exactly like the
// create button.
func TestBranchCreateEnterSubmits(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-enter")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "enter-branch")
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if !branchExists(t, repo, "enter-branch") {
		t.Fatal("enter-branch was not created by pressing Enter")
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "enter-branch")); got != featureTip {
		t.Fatalf("enter-branch points at %q, want %q", got, featureTip)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
}

// While a write is in flight the create items of the branch menu disable:
// a busy session must never start a second write.
func TestBranchCreateMenuItemsDisabledWhileBusy(t *testing.T) {
	repo, _ := branchCreateFixture(t, "bc-busy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}

	openBranchCreateMenu(t, h, "feature")
	for _, item := range []string{"Interactively Rebase on 'feature'…", "New Branch…", "New Tag…"} {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
}

// --- protected execution ---

// A source branch deleted after the dialog opened refuses the create: the
// worker re-reads the source ref on the repository itself and finds it gone,
// so no branch is created and the repository is exactly as before.
func TestBranchCreateGuardSourceBranchDeleted(t *testing.T) {
	repo, _ := branchCreateFixture(t, "bc-guard-deleted")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "copy")

	repo.Git("branch", "-D", "feature")

	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a deleted source branch")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a deleted source produced no error")
	} else if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("refusal = %q, want it to say the source branch no longer exists", err)
	}
}

// A source branch moved after the dialog opened refuses the create: the
// exact commit it pointed at is the user's start point, and a moved branch
// is a different start point the user never confirmed.
func TestBranchCreateGuardSourceBranchMoved(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-guard-moved")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "copy")

	// The branch gains a commit externally after the dialog opened.
	repo.Git("checkout", "-q", "feature")
	movedTip := repo.CommitFile("late.txt", "late\n", "late feature work")
	repo.Checkout("main")
	if movedTip == featureTip {
		t.Fatal("fixture: the external move did not move feature")
	}

	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created from a moved source branch")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create from a moved source produced no error")
	} else if !strings.Contains(err.Error(), "moved after the dialog opened") {
		t.Fatalf("refusal = %q, want it to say the source branch moved", err)
	}
	// The moved branch itself is untouched.
	if got := strings.TrimSpace(repo.Git("rev-parse", "feature")); got != movedTip {
		t.Fatalf("feature = %q after the refusal, want its external tip %q", got, movedTip)
	}
}

// A name taken externally between the dialog and the submit is never
// overwritten: the worker refuses, the error surfaces, and the external
// branch keeps its commit.
func TestBranchCreateGuardNameTakenExternally(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-guard-taken")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "taken")

	// The name is taken externally after validation passed, at main's tip
	// rather than the source commit.
	mainTip := strings.TrimSpace(repo.Git("rev-parse", "main"))
	repo.Git("branch", "taken", "main")

	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("overwriting create produced no error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("refusal = %q, want it to say the branch exists", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "taken")); got != mainTip {
		t.Fatalf("external branch moved to %q, want the original %q untouched", got, mainTip)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "taken")); got == featureTip {
		t.Fatal("external branch was overwritten onto the source commit")
	}
}

// A HEAD moving after the dialog opened — here an external commit on the
// current branch — refuses a checked-out create: the checkout the user
// confirmed was against the repository as they saw it.
func TestBranchCreateGuardHeadMoved(t *testing.T) {
	repo, _ := branchCreateFixture(t, "bc-guard-head")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "copy")

	repo.CommitFile("external.txt", "external\n", "external commit")

	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if branchExists(t, repo, "copy") {
		t.Fatal("copy was created after HEAD moved")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("create after a HEAD move produced no error")
	} else if !strings.Contains(err.Error(), "HEAD or the current branch changed") {
		t.Fatalf("refusal = %q, want it to say HEAD or the branch changed", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("the refusal switched the branch: %q, want main", got)
	}
}

// Creating with checkout when local changes would be overwritten is refused
// by git itself: the error reaches the user verbatim, the working tree keeps
// every byte, the current branch does not move — and because git creates the
// branch only as part of a successful checkout, no branch is left behind.
func TestBranchCreateCheckoutOverwriteRefused(t *testing.T) {
	repo := newGitRepo(t, "bc-overwrite")
	repo.WriteFile("conflict.txt", "main version\n")
	repo.Git("add", "conflict.txt")
	repo.Git("commit", "-q", "-m", "main conflict version")
	repo.CheckoutNewBranch("feature")
	repo.WriteFile("conflict.txt", "feature version\n")
	repo.Git("add", "conflict.txt")
	repo.Git("commit", "-q", "-m", "feature conflict version")
	repo.Checkout("main")
	// Uncommitted local edit the checkout would have to overwrite.
	repo.WriteFile("conflict.txt", "local uncommitted edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "copy")
	// The checkout box stays ticked.
	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()

	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main unchanged", got)
	}
	data, err := os.ReadFile(filepath.Join(repo.Path(), "conflict.txt"))
	if err != nil {
		t.Fatalf("read conflict.txt: %v", err)
	}
	if string(data) != "local uncommitted edits\n" {
		t.Fatalf("uncommitted content = %q, want preserved %q", string(data), "local uncommitted edits\n")
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("checkout overwrite produced no ActionErr")
	} else if !strings.Contains(err.Error(), "conflict.txt") && !strings.Contains(strings.ToLower(err.Error()), "local changes") {
		t.Fatalf("ActionErr = %q, want git checkout conflict wording verbatim", err)
	}
	if branchExists(t, repo, "copy") {
		t.Fatal("copy was left behind by the refused checkout")
	}
}

// Switching repositories closes the New Branch dialog and discards its
// draft: reopening in the other repository starts empty.
func TestBranchCreateDialogResetOnRepoSwitch(t *testing.T) {
	repoA, _ := branchCreateFixture(t, "bc-switch-a")
	repoB := newGitRepo(t, "bc-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "draft")

	h.openRepo(repoB.StoreRepo())
	h.drain()

	st := h.app.branchCreateUI()
	if st.open {
		t.Fatal("New Branch dialog survived a repository switch")
	}
	if st.name != "" || st.expectedOID != "" {
		t.Fatalf("draft survived the switch: %+v", st)
	}
	if hasExactText(h.ui, "Start point:") {
		t.Fatal("New Branch dialog rendered again after the repository switch")
	}
}

// --- New Tag… from a branch ---

// New Tag on a branch opens the tag dialog with the target pre-filled to the
// exact commit the branch pointed at and the branch hint below it; a
// lightweight and an annotated tag both land on exactly that commit.
func TestBranchCreateNewTagPrefilledTarget(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-tag-pre")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseMenuItemFromBranch(t, h, "feature", "New Tag…")
	if got := h.app.tagsUI().newTarget; got != featureTip {
		t.Fatalf("target prefill = %q, want the branch commit %q", got, featureTip)
	}
	if !h.ui.HasText("feature (" + short7(featureTip) + ")") {
		t.Fatalf("branch hint not shown; texts=%q", h.ui.Texts())
	}

	// Lightweight: no message.
	h.ui.Type("branch-lw")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()
	if !tagRefExists(t, repo, "branch-lw") {
		t.Fatal("branch-lw was not created")
	}
	if got := tagTypeOf(t, repo, "branch-lw"); got != "commit" {
		t.Fatalf("branch-lw object type = %q, want commit (lightweight)", got)
	}
	if got := tagPointsAt(t, repo, "branch-lw"); got != featureTip {
		t.Fatalf("branch-lw target = %q, want the branch commit %q", got, featureTip)
	}

	// Annotated: a message makes it a tag object on the same commit.
	chooseMenuItemFromBranch(t, h, "feature", "New Tag…")
	h.ui.Type("branch-ann")
	h.ui.Frame()
	focusField(t, h, "Tag message")
	h.ui.Type("tagging feature")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()
	if got := tagTypeOf(t, repo, "branch-ann"); got != "tag" {
		t.Fatalf("branch-ann object type = %q, want tag (annotated)", got)
	}
	if got := tagPointsAt(t, repo, "branch-ann"); got != featureTip {
		t.Fatalf("branch-ann target = %q, want the branch commit %q", got, featureTip)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected tag error: %v", err)
	}
}

// The tag target is the exact commit captured when the menu opened, never
// the branch name: a source branch moved externally before the submit still
// lands the tag on the original commit.
func TestBranchCreateNewTagSurvivesSourceMove(t *testing.T) {
	repo, featureTip := branchCreateFixture(t, "bc-tag-move")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseMenuItemFromBranch(t, h, "feature", "New Tag…")
	h.ui.Type("pinned")
	h.ui.Frame()

	// The source branch moves externally after the dialog opened.
	repo.Git("checkout", "-q", "feature")
	repo.CommitFile("late.txt", "late\n", "late feature work")
	repo.Checkout("main")

	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected tag error: %v", err)
	}
	if got := tagPointsAt(t, repo, "pinned"); got != featureTip {
		t.Fatalf("pinned target = %q, want the original branch commit %q", got, featureTip)
	}
}

// --- Interactively Rebase on '<branch>'… ---

// branchIRFixture builds a repository with feature at the initial commit and
// main two commits ahead of it, so feature is an ancestor of HEAD and the
// interactive rebase onto feature replays the two commits of main. It
// returns the fixture, the feature tip and the two commit hashes, oldest
// first.
func branchIRFixture(t *testing.T, name string) (*gitRepoFixture, string, []string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	featureTip := strings.TrimSpace(repo.Git("rev-parse", "feature"))
	hashes := irLinearFixture(t, repo, "ir one", "ir two")
	return repo, featureTip, hashes
}

// openBranchIRDialog right-clicks the branch row, chooses the interactive
// rebase item and verifies the form opened with the branch as its base.
func openBranchIRDialog(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	chooseMenuItemFromBranch(t, h, branch, "Interactively Rebase on '"+branch+"'…")
	if !hasExactText(h.ui, "Interactive Rebase") {
		t.Fatalf("interactive rebase form did not open; texts=%q", h.ui.Texts())
	}
}

// The interactive rebase item of the branch menu disables on the current
// branch itself, during an operation, while a write is busy and with a
// detached HEAD — none of those states can host a plan the user could
// confirm — and is offered on an ordinary non-current branch.
func TestBranchInteractiveRebaseMenuItemDisabledStates(t *testing.T) {
	repo, _, hashes := branchIRFixture(t, "bc-ir-menu")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	item := "Interactively Rebase on 'feature'…"

	// The current branch rebases onto itself: its plan would be empty.
	openBranchCreateMenu(t, h, "main")
	if err := h.ui.ChooseMenuItem("Interactively Rebase on 'main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item on the current branch = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	// During an operation — here a cherry-pick stopped on a conflict — the
	// item disables.
	repo.Git("checkout", "-qb", "other", hashes[0])
	repo.WriteFile("conflict.txt", "one\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-qm", "conflict one")
	side := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")
	repo.WriteFile("conflict.txt", "two\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-qm", "conflict two")
	if out, err := tryGitCommand(repo.Path(), "cherry-pick", side); err == nil {
		t.Fatalf("conflicting cherry-pick unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "cherry-pick" {
		t.Fatalf("fixture: state = %q, want cherry-pick", got)
	}
	openBranchCreateMenu(t, h, "feature")
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item during an operation = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Git("cherry-pick", "--abort")
	h.app.session.Reload()
	h.drain()

	// While a write is busy the item disables too.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	openBranchCreateMenu(t, h, "feature")
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	// A detached HEAD has no branch for the rebase to move.
	repo.Git("checkout", "-q", "--detach", "HEAD")
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}
	openBranchCreateMenu(t, h, "feature")
	if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing the item while detached = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	// On an ordinary non-current branch the item is offered.
	openBranchCreateMenu(t, h, "feature")
	if !slices.Contains(h.ui.Menu(), item) {
		t.Fatalf("menu %q missing %q", h.ui.Menu(), item)
	}
	h.ui.CloseMenu()
}

// From the Local Changes page the item opens the interactive rebase form —
// mounted there by the sidebar because the History page is not showing —
// names the branch as its base, and a drop through the form replays on the
// real repository exactly as the plan showed.
func TestBranchInteractiveRebaseFromChangesPage(t *testing.T) {
	repo, featureTip, hashes := branchIRFixture(t, "bc-ir-changes")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if got := h.app.route.page; got == pageHistory {
		t.Fatal("fixture: the app opened on the History page")
	}

	openBranchIRDialog(t, h, "feature")
	drainIRPlan(t, h)

	// The base is the branch's exact commit, labeled with the branch name.
	if !h.ui.HasText(short7(featureTip) + " feature") {
		t.Fatalf("onto label missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok {
		t.Fatalf("plan rows missing; texts=%q", h.ui.Texts())
	}

	chooseIRAction(t, h, hashes[0], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase from the Changes page failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir two", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:ir1.txt"); err == nil {
		t.Fatalf("ir1.txt of the dropped commit survived: %s", out)
	}
	assertIRDialogClosed(t, h)
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after the rebase = %q, want empty", got)
	}
}

// From the History page the same item opens exactly one form — the commit
// actions mount it there and the sidebar must not duplicate it — and the
// rebase completes through it.
func TestBranchInteractiveRebaseFromHistoryPageSingleForm(t *testing.T) {
	repo, _, hashes := branchIRFixture(t, "bc-ir-history")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchIRDialog(t, h, "feature")
	drainIRPlan(t, h)

	// Confirm the form opens and exposes one dialog panel named after its
	// title (counting title text would see both the heading and the panel's
	// accessible name). Because the accessibility tree only exposes the
	// topmost dialog, mounting the form twice would fail earlier during
	// build with a mygo panic (duplicate element key or shared ListState).
	count := 0
	for _, a := range h.ui.AccessAll() {
		if a.Role == ui.RoleDialog && a.Name == "Interactive Rebase" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Interactive Rebase form rendered %d times, want exactly 1; texts=%q", count, h.ui.Texts())
	}

	chooseIRAction(t, h, hashes[1], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop rebase from the History page failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"ir one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after drop = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)
}

// Right-clicking a branch that has diverged from the current branch opens
// the form with the honest refusal of the plan read — the base is not an
// ancestor of HEAD — the Rebase button stays disabled, and the repository
// is exactly as before.
func TestBranchInteractiveRebaseDivergedBranchRefused(t *testing.T) {
	repo := newGitRepo(t, "bc-ir-diverged")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CreateBranch("feature")
	featureTip := strings.TrimSpace(repo.Git("rev-parse", "feature"))
	mainTip := repo.CommitFile("main.txt", "main\n", "main work")
	repo.Git("checkout", "-q", "feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Checkout("main")
	// main and feature diverged: feature is no ancestor of HEAD.

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openBranchIRDialog(t, h, "feature")
	drainIRPlan(t, h)

	if !h.ui.HasText("not an ancestor") {
		t.Fatalf("non-ancestor refusal missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Rebase"); !ok || !btn.Disabled {
		t.Fatalf("Rebase with a diverged base = %+v found=%v, want present and disabled", btn, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(featureTip)); ok {
		t.Fatal("plan rows rendered for a diverged base")
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertIRRepoUnchanged(t, repo, mainTip, "main", "2")
	if got := strings.TrimSpace(repo.Git("rev-parse", "feature")); got != strings.TrimSpace(repo.Git("rev-parse", "feature^{commit}")) {
		t.Fatal("fixture inconsistency reading feature")
	}
}

// --- Loopback SSH ---

// Over a real SSH connection the branch menu's New Branch creates the branch
// on the server at the source commit and checks it out, and the New Tag
// dialog pre-fills the target with the exact commit and lands the tag there.
func TestBranchCreateMenuNewBranchAndTagLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repo.Path, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repo.Path, "add", "feature.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "feature work")
	featureTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.WriteFile(t, repo.Path, "main.txt", []byte("main\n"))
	srv.RunGit(t, repo.Path, "add", "main.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "main work")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// New Branch from the branch row, checkout ticked.
	openNewBranchFromDialog(t, h, "feature")
	typeBranchCreateName(t, h, "ssh-copy")
	if err := h.ui.Click("Confirm New Branch From"); err != nil {
		t.Fatalf("click Confirm New Branch From: %v", err)
	}
	h.drain()
	if out := srv.RunGit(t, repo.Path, "branch", "--list", "ssh-copy"); strings.TrimSpace(out) == "" {
		t.Fatal("ssh-copy was not created on the server")
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "ssh-copy")); got != featureTip {
		t.Fatalf("ssh-copy points at %q, want the source commit %q", got, featureTip)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "ssh-copy" {
		t.Fatalf("SSH current branch after create = %q, want ssh-copy", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH create error: %v", err)
	}

	// Back on main, New Tag from the branch row tags the exact commit.
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	h.app.session.Reload()
	h.drain()
	chooseMenuItemFromBranch(t, h, "feature", "New Tag…")
	if got := h.app.tagsUI().newTarget; got != featureTip {
		t.Fatalf("SSH target prefill = %q, want the branch commit %q", got, featureTip)
	}
	h.ui.Type("ssh-tag")
	h.ui.Frame()
	if err := h.ui.Click("Create Tag"); err != nil {
		t.Fatalf("click Create Tag: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH tag error: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "refs/tags/ssh-tag^{}")); got != featureTip {
		t.Fatalf("ssh-tag target = %q, want the branch commit %q", got, featureTip)
	}
}

// Over a real SSH connection the interactive rebase item of the branch menu
// opens the form, and a drop replays on the server exactly as locally.
func TestBranchInteractiveRebaseDropLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "branch", "feature")
	featureTip := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	_, hashes := irRemoteLinear(t, srv, repo.Path, "ir one", "ir two")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	openBranchIRDialog(t, h, "feature")
	drainIRPlan(t, h)
	if !h.ui.HasText(short7(featureTip) + " feature") {
		t.Fatalf("SSH onto label missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok {
		t.Fatalf("SSH plan rows missing; texts=%q", h.ui.Texts())
	}

	chooseIRAction(t, h, hashes[0], "Pick", "Drop")
	if err := h.ui.Click("Rebase"); err != nil {
		t.Fatalf("click Rebase: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH drop rebase failed: %v", err)
	}
	out := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "--format=%s", "main"))
	subjects := strings.Split(out, "\n")
	want := []string{"ir two", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after drop = %v, want %v", subjects, want)
	}
	assertIRDialogClosed(t, h)
}
