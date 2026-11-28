package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// checkoutBlockedFixture builds a repository whose checkout of feature is
// blocked: shared.txt differs on feature in line six and carries a local
// edit of line one on main, far enough apart for the stashed edit to
// reapply cleanly after the checkout.
func checkoutBlockedFixture(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n", "initial commit")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n", "feature edits line six")
	repo.Checkout("main")
	repo.WriteFile("shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	return repo
}

// assertCheckoutBlockedPanel verifies the checkout-blocked dialog is open
// for target, lists exactly the given files in their groups, and shows no
// error banner anywhere.
func assertCheckoutBlockedPanel(t *testing.T, h *testHarness, target string, changed, untracked []string) {
	t.Helper()
	if !hasExactText(h.ui, "Local Changes Would Be Overwritten") {
		t.Fatalf("checkout-blocked dialog title missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Checking out '" + target + "' would overwrite:") {
		t.Fatalf("dialog missing the target line for %q; texts=%q", target, h.ui.Texts())
	}
	if len(changed) > 0 && !hasExactText(h.ui, "Changed files") {
		t.Fatalf("dialog missing the Changed files group; texts=%q", h.ui.Texts())
	}
	for _, f := range changed {
		if !hasExactText(h.ui, f) {
			t.Fatalf("dialog missing changed file %q; texts=%q", f, h.ui.Texts())
		}
	}
	if len(untracked) > 0 && !hasExactText(h.ui, "Untracked files") {
		t.Fatalf("dialog missing the Untracked files group; texts=%q", h.ui.Texts())
	}
	for _, f := range untracked {
		if !hasExactText(h.ui, f) {
			t.Fatalf("dialog missing untracked file %q; texts=%q", f, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatalf("Repository Error banner visible while the checkout-blocked dialog is open; texts=%q", h.ui.Texts())
	}
}

// assertNoCheckoutBlockedPanel verifies the dialog is closed and no error
// banner is showing.
func assertNoCheckoutBlockedPanel(t *testing.T, h *testHarness) {
	t.Helper()
	if hasExactText(h.ui, "Local Changes Would Be Overwritten") {
		t.Fatalf("checkout-blocked dialog still open; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatalf("Repository Error banner visible; texts=%q", h.ui.Texts())
	}
}

// clickStashAndCheckout confirms the dialog with the stash choice, the
// default it opens with.
func clickStashAndCheckout(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Stash and Checkout"); err != nil {
		t.Fatalf("click Stash and Checkout: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
}

// chooseDiscardAndConfirm picks the discard radio and confirms with its
// destructive button.
func chooseDiscardAndConfirm(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Discard local changes"); err != nil {
		t.Fatalf("click the discard radio: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Discard and Checkout"); err != nil {
		t.Fatalf("click Discard and Checkout: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
}

// cancelCheckoutBlocked closes the dialog with its Cancel button.
func cancelCheckoutBlocked(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// A blocked double-click checkout of a sidebar branch opens the dialog
// instead of the error banner: it names the branch and lists the tracked
// file in the way.
func TestCheckoutBlockedBranchDoubleClickPanel(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-panel-branch")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")

	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch after the blocked checkout = %q, want main — nothing moved yet", got)
	}
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	cancelCheckoutBlocked(t, h)
	assertNoCheckoutBlockedPanel(t, h)
}

// A blocked checkout of a remote-tracking branch opens the dialog listing
// the untracked file in the way.
func TestCheckoutBlockedRemoteBranchPanel(t *testing.T) {
	repo := remoteMenuFixture(t, "cb-panel-remote")
	repo.WriteFile("feature.txt", "local uncommitted edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()

	assertCheckoutBlockedPanel(t, h, "feature", nil, []string{"feature.txt"})
	if hasExactText(h.ui, "Changed files") {
		t.Fatalf("Changed files group visible although only an untracked file blocks; texts=%q", h.ui.Texts())
	}
	if branchExists(t, repo, "feature") {
		t.Fatal("a feature branch was created although the checkout has not run yet")
	}
	cancelCheckoutBlocked(t, h)
	assertNoCheckoutBlockedPanel(t, h)
}

// A blocked checkout of a tag opens the dialog naming the tag.
func TestCheckoutBlockedTagPanel(t *testing.T) {
	repo := newGitRepo(t, "cb-panel-tag")
	first := repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\n", "initial commit")
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\n", "extend shared")
	repo.Git("tag", "lw", first)
	repo.WriteFile("shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	assertCheckoutBlockedPanel(t, h, "lw", []string{"shared.txt"}, nil)
	cancelCheckoutBlocked(t, h)
	assertNoCheckoutBlockedPanel(t, h)
}

// A blocked checkout of a commit from the History page opens the dialog on
// that very page: the sidebar mounts it everywhere.
func TestCheckoutBlockedCommitPanel(t *testing.T) {
	repo := newGitRepo(t, "cb-panel-commit")
	c1 := repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\n", "add shared")
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\n", "extend shared")
	repo.WriteFile("shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if h.app.route.page != pageHistory {
		t.Fatalf("route page = %v, want pageHistory", h.app.route.page)
	}
	openCheckoutModal(t, h, c1, "add shared")
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()

	if h.app.route.page != pageHistory {
		t.Fatalf("route page after the blocked checkout = %v, want still pageHistory", h.app.route.page)
	}
	assertCheckoutBlockedPanel(t, h, shortTagHash(c1), []string{"shared.txt"}, nil)
	cancelCheckoutBlocked(t, h)
	assertNoCheckoutBlockedPanel(t, h)
}

// Confirming with the stash choice checks the branch out and reapplies the
// local edit on top of it, leaving nothing in the stash list.
func TestCheckoutBlockedStashAndCheckout(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-stash")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
	clickStashAndCheckout(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stash and checkout failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("branch after stash and checkout = %q, want feature", got)
	}
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list = %q, want empty after the clean reapply", out)
	}
	assertNoCheckoutBlockedPanel(t, h)
}

// Confirming with the discard choice checks the branch out and drops the
// local edit: the file is exactly what the branch carries.
func TestCheckoutBlockedDiscardAndCheckout(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-discard")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
	chooseDiscardAndConfirm(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard and checkout failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("branch after discard and checkout = %q, want feature", got)
	}
	assertFileBytes(t, repo, "shared.txt", "l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list = %q, want empty — discarding stashes nothing", out)
	}
	assertNoCheckoutBlockedPanel(t, h)
}

// Discarding also overwrites the untracked files in the way.
func TestCheckoutBlockedDiscardDropsUntracked(t *testing.T) {
	repo := remoteMenuFixture(t, "cb-discard-untracked")
	repo.WriteFile("feature.txt", "local uncommitted edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Checkout…"); err != nil {
		t.Fatalf("choose Checkout…: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Checkout Remote Branch"); err != nil {
		t.Fatalf("click Confirm Checkout Remote Branch: %v", err)
	}
	h.drain()
	assertCheckoutBlockedPanel(t, h, "feature", nil, []string{"feature.txt"})

	chooseDiscardAndConfirm(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("discard and checkout failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("branch after discard and checkout = %q, want feature", got)
	}
	assertFileBytes(t, repo, "feature.txt", "feature\n")
	assertNoCheckoutBlockedPanel(t, h)
}

// Canceling leaves the branch, the file bytes and every untracked file
// exactly as they were, with no error banner.
func TestCheckoutBlockedCancel(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-cancel")
	featureTip := strings.TrimSpace(repo.Git("rev-parse", "feature"))
	mainTip := strings.TrimSpace(repo.Git("rev-parse", "main"))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
	cancelCheckoutBlocked(t, h)
	h.drain()

	assertNoCheckoutBlockedPanel(t, h)
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch after canceling = %q, want main", got)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != mainTip {
		t.Fatalf("HEAD after canceling = %q, want %q", got, mainTip)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "feature")); got != featureTip {
		t.Fatalf("feature after canceling = %q, want %q", got, featureTip)
	}
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after canceling = %q, want empty", out)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("canceling left an action error: %v", err)
	}
}

// Enter confirms the dialog only while the stash choice is selected. With
// the discard choice selected Enter does nothing at all: no checkout, no
// close, no error.
func TestCheckoutBlockedEnterKeys(t *testing.T) {
	t.Run("enter confirms the stash choice", func(t *testing.T) {
		repo := checkoutBlockedFixture(t, "cb-enter-stash")

		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()

		doubleClickCheckout(t, h, "feature")
		assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
		// Focus the radio first: Enter then reaches the window-level
		// shortcut instead of a focused button, exactly like the other
		// non-destructive dialogs.
		if err := h.ui.Click("Stash local changes, check out, then reapply them"); err != nil {
			t.Fatalf("click the stash radio: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
		h.ui.Key(0, ui.KeyEnter)
		h.drain()

		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("stash and checkout by Enter failed: %v", err)
		}
		if got := currentBranch(t, repo); got != "feature" {
			t.Fatalf("branch after Enter = %q, want feature — Enter must confirm the stash choice", got)
		}
		assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n")
	})

	t.Run("enter with the discard choice does nothing", func(t *testing.T) {
		repo := checkoutBlockedFixture(t, "cb-enter-discard")

		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()

		doubleClickCheckout(t, h, "feature")
		assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
		if err := h.ui.Click("Discard local changes"); err != nil {
			t.Fatalf("click the discard radio: %v\ntexts=%q", err, h.ui.Texts())
		}
		h.ui.Frame()
		if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Discard and Checkout"); !ok {
			t.Fatalf("Discard and Checkout button missing after choosing discard; texts=%q", h.ui.Texts())
		} else if btn.Focused {
			t.Fatal("the destructive Discard and Checkout button holds the focus")
		}

		h.ui.Key(0, ui.KeyEnter)
		h.drain()

		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("branch after Enter with discard selected = %q, want main — Enter must do nothing", got)
		}
		assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("Enter with discard selected ran a write that failed: %v", err)
		}
		assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)
		cancelCheckoutBlocked(t, h)
	})
}

// A group of more than eight files folds the rest into "and N more".
func TestCheckoutBlockedManyFilesShowsAndMore(t *testing.T) {
	repo := newGitRepo(t, "cb-many")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	// Ten files tracked on feature but not on main, all untracked on main.
	repo.CheckoutNewBranch("feature")
	for i := 1; i <= 10; i++ {
		repo.WriteFile(fmt.Sprintf("f%02d.txt", i), fmt.Sprintf("feature %d\n", i))
	}
	repo.Git("add", "--", ".")
	repo.Git("commit", "-q", "-m", "feature files")
	repo.Git("checkout", "-q", "main")
	for i := 1; i <= 10; i++ {
		repo.WriteFile(fmt.Sprintf("f%02d.txt", i), fmt.Sprintf("mine %d\n", i))
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")

	assertCheckoutBlockedPanel(t, h, "feature", nil, []string{"f01.txt", "f02.txt", "f08.txt"})
	if !hasExactText(h.ui, "and 2 more") {
		t.Fatalf("folded count missing; texts=%q", h.ui.Texts())
	}
	// The Changes page lists every untracked file by name, so only the
	// dialog's own texts — everything after its title — prove the fold.
	texts := h.ui.Texts()
	title := -1
	for i, s := range texts {
		if s == "Checking out 'feature' would overwrite:" {
			title = i
		}
	}
	if title < 0 {
		t.Fatalf("dialog target line missing; texts=%q", texts)
	}
	for _, f := range []string{"f09.txt", "f10.txt"} {
		for _, s := range texts[title:] {
			if s == f {
				t.Fatalf("file %q beyond the first eight is listed instead of folded; texts=%q", f, texts)
			}
		}
	}
	cancelCheckoutBlocked(t, h)
}

// The retry re-runs every guard: when HEAD moved after the dialog opened,
// the stash choice is refused before the checkout, and the saved changes
// come back to the working tree untouched.
func TestCheckoutBlockedGuardRecheckedOnRetry(t *testing.T) {
	repo := newGitRepo(t, "cb-guard")
	c1 := repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\n", "add shared")
	head := repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\n", "extend shared")
	repo.WriteFile("shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\n")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openCheckoutModal(t, h, c1, "add shared")
	if err := h.ui.Click("Confirm Checkout Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Commit: %v", err)
	}
	h.drain()
	assertCheckoutBlockedPanel(t, h, shortTagHash(c1), []string{"shared.txt"}, nil)

	// HEAD moves externally while the dialog waits.
	moved := repo.CommitFile("other.txt", "other\n", "external commit")
	if moved == head {
		t.Fatal("fixture broken: the external commit did not move HEAD")
	}

	clickStashAndCheckout(t, h)

	assertRepoErrorShown(t, h, "changed after the dialog opened")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch after the refused retry = %q, want main", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD after the refused retry = %q, want the external commit %q — the checkout must not have run", got, moved)
	}
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after the refused retry = %q, want empty — the restore must not linger", out)
	}
	assertRefGone(t, repo, "refs/worktree/branchline-sync-stash")
}

// Switching repositories closes the dialog and drops the captured checkout.
func TestCheckoutBlockedClosesOnRepoSwitch(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-switch")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)

	other := newGitRepo(t, "cb-switch-other")
	other.CommitFile("o.txt", "o\n", "other initial commit")
	h.openRepo(other.StoreRepo())
	h.drain()

	if hasExactText(h.ui, "Local Changes Would Be Overwritten") {
		t.Fatal("dialog stayed open after switching repositories")
	}
	if st := checkoutBlockedUI(h.app); st.open {
		t.Fatal("dialog state still open after switching repositories")
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch of the first repository after switching = %q, want main", got)
	}
}

// A checkout failing for any other reason than local changes in the way
// still surfaces through the error banner, with no dialog.
func TestCheckoutBlockedOtherErrorStillBanner(t *testing.T) {
	repo := newGitRepo(t, "cb-other-error")
	first := repo.CommitFile("f.txt", "v1\n", "add f v1")
	repo.CommitFile("f.txt", "v2\n", "update f to v2")
	repo.Git("tag", "lw", first)
	repo.WriteFile("f.txt", "local edits\n")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseTagMenuItem(t, h, "lw", "Checkout Commit…", "Checkout Commit")
	// The tag moves externally before the confirmation runs.
	repo.Git("tag", "-f", "lw", "HEAD")
	if err := h.ui.Click("Confirm Checkout Tag Commit"); err != nil {
		t.Fatalf("click Confirm Checkout Tag Commit: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "tag lw moved to another commit")
	if hasExactText(h.ui, "Local Changes Would Be Overwritten") {
		t.Fatal("checkout-blocked dialog opened for a guard error")
	}
}

// The checkout-blocked dialog state resets cleanly when the repository is
// switched mid-dialog: reopening on the new repository starts fresh.
func TestCheckoutBlockedRepoSwitchDropsCapturedRun(t *testing.T) {
	repo := checkoutBlockedFixture(t, "cb-switch-run")
	featureTip := strings.TrimSpace(repo.Git("rev-parse", "feature"))

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	doubleClickCheckout(t, h, "feature")
	assertCheckoutBlockedPanel(t, h, "feature", []string{"shared.txt"}, nil)

	other := newGitRepo(t, "cb-switch-run-other")
	other.CommitFile("o.txt", "o\n", "other initial commit")
	h.openRepo(other.StoreRepo())
	h.drain()

	// The captured retry of the first repository is gone: nothing can run it
	// anymore, and the first repository is untouched.
	if st := checkoutBlockedUI(h.app); st.run != nil || st.label != "" {
		t.Fatalf("captured checkout survived the switch: label %q run %v", st.label, st.run != nil)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "feature")); got != featureTip {
		t.Fatalf("feature of the first repository = %q after the switch, want %q", got, featureTip)
	}
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	if _, err := os.Stat(filepath.Join(repo.Path(), "shared.txt")); err != nil {
		t.Fatalf("shared.txt missing on the first repository: %v", err)
	}
}
