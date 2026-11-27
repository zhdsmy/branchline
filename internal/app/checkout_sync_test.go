package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// checkoutSyncBase builds a repository with main checked out tracking a bare
// origin.
func checkoutSyncBase(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	return repo, bareDir
}

// trackFeature creates the feature branch at main, publishes it with upstream
// tracking, and returns to main.
func trackFeature(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CheckoutNewBranch("feature")
	repo.Git("push", "-q", "-u", "origin", "feature")
	repo.Checkout("main")
}

// featureBehindUpstream leaves feature one commit behind origin/feature: the
// upstream tip adds ups.txt, and feature is reset back to the push's base.
func featureBehindUpstream(t *testing.T, repo *gitRepoFixture) (featureTip, upstreamTip string) {
	t.Helper()
	trackFeature(t, repo)
	repo.Checkout("feature")
	upstreamTip = repo.CommitFile("ups.txt", "ups\n", "upstream work")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	featureTip = strings.TrimSpace(repo.Git("rev-parse", "feature"))
	repo.Checkout("main")
	return featureTip, upstreamTip
}

// featureDivergedFromUpstream leaves feature and origin/feature each one
// commit ahead of the other: the upstream commit adds ups.txt, the feature
// commit adds feat.txt.
func featureDivergedFromUpstream(t *testing.T, repo *gitRepoFixture) (featureTip, upstreamTip string) {
	t.Helper()
	trackFeature(t, repo)
	repo.Checkout("feature")
	upstreamTip = repo.CommitFile("ups.txt", "ups\n", "upstream work")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	featureTip = repo.CommitFile("feat.txt", "feat\n", "feature work")
	repo.Checkout("main")
	return featureTip, upstreamTip
}

// chooseCheckoutAnd right-clicks the local branch row of the sidebar and
// chooses the given item of its Checkout and… submenu, failing the test when
// either step does not reach the menu.
func chooseCheckoutAnd(t *testing.T, h *testHarness, branch, item string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Checkout and…", item); err != nil {
		t.Fatalf("choose Checkout and… > %s: %v", item, err)
	}
	h.ui.Frame()
}

// assertCheckoutSyncDialog verifies the Checkout and Sync dialog is open
// naming the branch and its upstream, in the dialog's expected layout.
func assertCheckoutSyncDialog(t *testing.T, h *testHarness, branch, upstream string) {
	t.Helper()
	if !hasExactText(h.ui, "Checkout and Sync") {
		t.Fatalf("dialog title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, branch) {
		t.Fatalf("dialog missing the branch line %q; texts=%q", branch, h.ui.Texts())
	}
	if !hasExactText(h.ui, upstream) {
		t.Fatalf("dialog missing the upstream line %q; texts=%q", upstream, h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Checkout and Sync")
}

// confirmCheckoutSync clicks the dialog's Checkout button and drains the
// worker.
func confirmCheckoutSync(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Checkout and Sync"); err != nil {
		t.Fatalf("click Confirm Checkout and Sync: %v", err)
	}
	h.drain()
}

// cancelCheckoutSync closes the dialog with its Cancel button.
func cancelCheckoutSync(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
		t.Fatal("dialog stayed open after Cancel")
	}
}

// chooseCheckoutSyncAction switches the dialog's Action drop-down to title,
// as keyboard selection in the open drop-down does.
func chooseCheckoutSyncAction(t *testing.T, h *testHarness, from, title string) {
	t.Helper()
	if err := h.ui.Click("Action"); err != nil {
		t.Fatalf("open Action drop-down: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	names := checkoutSyncActionTitles()
	steps := slices.Index(names, title) - slices.Index(names, from)
	if steps < 0 {
		t.Fatalf("chooseCheckoutSyncAction only moves down the list: %q to %q", from, title)
	}
	for range steps {
		h.ui.Key(0, ui.KeyDown)
	}
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
}

// gitCallRecorder records every Git invocation through the executor it wraps,
// proving a UI path started no repository command at all.
type gitCallRecorder struct {
	real git.Executor

	mu   sync.Mutex
	args [][]string
}

func (e *gitCallRecorder) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.mu.Lock()
	e.args = append(e.args, append([]string(nil), args...))
	e.mu.Unlock()
	return e.real.Run(ctx, stdin, args...)
}

func (e *gitCallRecorder) Describe() string { return e.real.Describe() }

func (e *gitCallRecorder) calls() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.args...)
}

// installGitCallRecorder wraps the open repository's executor so every later
// Git invocation is recorded.
func installGitCallRecorder(t *testing.T, h *testHarness) *gitCallRecorder {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("cannot install the recording executor without an open repository")
	}
	rec := &gitCallRecorder{real: repo.Exec}
	repo.Exec = rec
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		repo.Exec = rec.real
	})
	return rec
}

// The Checkout and… submenu shows on a non-current branch with a live
// upstream — between Pull and Push to, with all four actions opening the
// dialog — and hides on the current branch, on a branch without upstream, and
// on one whose upstream is gone; while a write is busy or a merge is in
// progress its items are disabled.
func TestCheckoutSyncMenuVisibilityAndDisabledStates(t *testing.T) {
	repo, bareOrigin := checkoutSyncBase(t, "cs-menu")
	featureDivergedFromUpstream(t, repo)

	// stale tracks an upstream deleted on the remote, so it reads as gone.
	repo.CheckoutNewBranch("stale")
	repo.CommitFile("s.txt", "s\n", "stale commit")
	repo.Git("push", "-q", "-u", "origin", "stale")
	runGitCommand(t, bareOrigin, "update-ref", "-d", "refs/heads/stale")
	repo.Git("fetch", "--prune", "origin")
	repo.Checkout("main")
	repo.CreateBranch("local")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// feature: the submenu is there with its four items, before Push to.
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Checkout and…") {
		t.Fatalf("feature menu %q missing Checkout and…", menu)
	}
	if !slices.Contains(menu, "Push to 'origin/feature'…") {
		t.Fatalf("feature menu %q missing Push to 'origin/feature'…", menu)
	}
	if slices.Index(menu, "Checkout and…") > slices.Index(menu, "Push to 'origin/feature'…") {
		t.Fatalf("feature menu %q has Checkout and… after Push to", menu)
	}
	for _, item := range []string{"Fast-Forward…", "Merge…", "Rebase…", "Reset…"} {
		if err := h.ui.ChooseMenuItem("Checkout and…", item); err != nil {
			t.Fatalf("choose Checkout and… > %s: %v", item, err)
		}
		h.ui.Frame()
		assertCheckoutSyncDialog(t, h, "feature", "origin/feature")
		cancelCheckoutSync(t, h)
		if err := h.ui.RightClick("feature"); err != nil {
			t.Fatalf("right-click feature: %v", err)
		}
		h.ui.Frame()
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	// The chosen item preselects the dialog's action.
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Checkout and…", "Reset…"); err != nil {
		t.Fatalf("choose Checkout and… > Reset…: %v", err)
	}
	h.ui.Frame()
	if sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action"); !ok || sel.Value != "Reset" {
		t.Fatalf("Action drop-down = %+v found=%v after Reset…, want value Reset", sel, ok)
	}
	if !h.ui.HasText("Checkout 'feature' and reset it (--hard) to 'origin/feature'") {
		t.Fatalf("reset note missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Commits on 'feature' that 'origin/feature' does not have will no longer be on any branch. Local changes are discarded unless stashed.") {
		t.Fatalf("reset danger note missing; texts=%q", h.ui.Texts())
	}
	cancelCheckoutSync(t, h)

	// main is current: no submenu, however live its upstream is.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	if menu := h.ui.Menu(); slices.Contains(menu, "Checkout and…") {
		t.Fatalf("current branch main menu %q must not show Checkout and…", menu)
	}
	h.ui.CloseMenu()

	// local has no upstream: no submenu.
	if err := h.ui.RightClick("local"); err != nil {
		t.Fatalf("right-click local: %v", err)
	}
	if menu := h.ui.Menu(); slices.Contains(menu, "Checkout and…") {
		t.Fatalf("untracked branch local menu %q must not show Checkout and…", menu)
	}
	h.ui.CloseMenu()

	// stale's upstream is gone: no submenu.
	if err := h.ui.RightClick("stale"); err != nil {
		t.Fatalf("right-click stale: %v", err)
	}
	if menu := h.ui.Menu(); slices.Contains(menu, "Checkout and…") {
		t.Fatalf("gone-upstream branch stale menu %q must not show Checkout and…", menu)
	}
	h.ui.CloseMenu()

	refuse := func() {
		t.Helper()
		if err := h.ui.RightClick("feature"); err != nil {
			t.Fatalf("right-click feature: %v", err)
		}
		h.ui.Frame()
		if menu := h.ui.Menu(); !slices.Contains(menu, "Checkout and…") {
			t.Fatalf("menu %q missing the Checkout and… submenu", menu)
		}
		for _, item := range []string{"Fast-Forward…", "Merge…", "Rebase…", "Reset…"} {
			err := h.ui.ChooseMenuItem("Checkout and…", item)
			if err == nil || !strings.Contains(err.Error(), "disabled") {
				t.Fatalf("choosing Checkout and… > %s = %v, want a disabled error", item, err)
			}
		}
		h.ui.CloseMenu()
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
			t.Fatal("dialog opened from a disabled menu item")
		}
	}

	// Operation in progress: a conflicting merge started externally.
	repo.CheckoutNewBranch("conflict-side")
	repo.CommitFile("a.txt", "side\n", "side edit")
	repo.Checkout("main")
	repo.CommitFile("a.txt", "main\n", "main edit")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "conflict-side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: status state = %q, want merge", got)
	}
	refuse()
	repo.Git("merge", "--abort")
	h.app.session.Reload()
	h.drain()

	// Busy: hold a real stage write behind the worker gate.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	stageWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	refuse()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(stageWorker)
	h.drain()
}

// Checkout and… > Fast-Forward… on a branch behind its upstream switches to
// it and fast-forwards it to the upstream tip: HEAD sits on the branch and
// the branch equals the upstream. Enter submits the dialog exactly like the
// Checkout button.
func TestCheckoutSyncFastForwardBehindUpstream(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-ff")
	featureTip, upstreamTip := featureBehindUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")
	if sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action"); !ok || sel.Value != "Fast-Forward" {
		t.Fatalf("Action drop-down = %+v found=%v after Fast-Forward…, want value Fast-Forward", sel, ok)
	}
	if !h.ui.HasText("Checkout 'feature' and fast-forward it to 'origin/feature'") {
		t.Fatalf("fast-forward note missing; texts=%q", h.ui.Texts())
	}

	// Enter submits the dialog like the Checkout button.
	h.ui.Key(0, ui.KeyEnter)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and fast-forward failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != upstreamTip {
		t.Fatalf("feature after the fast-forward = %q, want the upstream tip %q", got, upstreamTip)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != upstreamTip {
		t.Fatalf("HEAD after the fast-forward = %q, want %q", got, upstreamTip)
	}
	assertFileBytes(t, repo, "ups.txt", "ups\n")
	if featureTip == upstreamTip {
		t.Fatal("fixture broken: feature never lagged the upstream")
	}
}

// Checkout and… with the Merge action on diverged branches switches to the
// branch and records a merge commit whose second parent is the upstream tip.
func TestCheckoutSyncMergeDiverged(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-merge")
	_, upstreamTip := featureDivergedFromUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Merge…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")
	if !h.ui.HasText("Checkout 'feature' and merge 'origin/feature' into it") {
		t.Fatalf("merge note missing; texts=%q", h.ui.Texts())
	}
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and merge failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	parents := strings.Fields(gitOutTrim(t, repo.Path(), "rev-list", "--parents", "-1", "feature"))
	if len(parents) != 3 {
		t.Fatalf("feature parents after the merge = %q, want a merge commit with two parents", parents)
	}
	if parents[2] != upstreamTip {
		t.Fatalf("second parent of the merge = %q, want the upstream tip %q", parents[2], upstreamTip)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != parents[0] {
		t.Fatalf("HEAD after the merge = %q, want the merge commit %q", got, parents[0])
	}
	assertFileBytes(t, repo, "ups.txt", "ups\n")
	assertFileBytes(t, repo, "feat.txt", "feat\n")
}

// Checkout and… with the Rebase action on diverged branches switches to the
// branch and replays its own commit on top of the upstream: the history stays
// linear with the upstream as the branch's ancestor.
func TestCheckoutSyncRebaseDiverged(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-rebase")
	_, upstreamTip := featureDivergedFromUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Rebase…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")
	if !h.ui.HasText("Checkout 'feature' and rebase it on 'origin/feature'") {
		t.Fatalf("rebase note missing; texts=%q", h.ui.Texts())
	}
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and rebase failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	if _, err := tryGitCommand(repo.Path(), "merge-base", "--is-ancestor", upstreamTip, "feature"); err != nil {
		t.Fatalf("upstream tip %q is not an ancestor of feature after the rebase: %v", upstreamTip, err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--merges", "--count", "feature"); got != "0" {
		t.Fatalf("merge commits after the rebase = %q, want 0 — the history must stay linear", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "feature"); got != "3" {
		t.Fatalf("commit count after the rebase = %q, want 3 (base, upstream work, replayed feature work)", got)
	}
	subjects := strings.Split(gitOutTrim(t, repo.Path(), "log", "--format=%s", "-3", "feature"), "\n")
	if len(subjects) != 3 || subjects[0] != "feature work" || subjects[1] != "upstream work" || subjects[2] != "initial commit" {
		t.Fatalf("log subjects after the rebase = %q, want feature work / upstream work / initial commit", subjects)
	}
	assertFileBytes(t, repo, "ups.txt", "ups\n")
	assertFileBytes(t, repo, "feat.txt", "feat\n")
}

// Checkout and… with the Reset action switches to the branch and hard-resets
// it to the upstream tip: the commits only the branch had belong to no branch
// anymore.
func TestCheckoutSyncResetDiverged(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-reset")
	featureTip, upstreamTip := featureDivergedFromUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Open through Fast-Forward… and switch the drop-down to Reset, as a user
	// reconsidering the action in the dialog does.
	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	chooseCheckoutSyncAction(t, h, "Fast-Forward", "Reset")
	if sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action"); !ok || sel.Value != "Reset" {
		t.Fatalf("Action drop-down = %+v found=%v after choosing Reset, want value Reset", sel, ok)
	}
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and reset failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != upstreamTip {
		t.Fatalf("feature after the reset = %q, want the upstream tip %q", got, upstreamTip)
	}
	if out := strings.TrimSpace(repo.Git("branch", "--all", "--contains", featureTip)); out != "" {
		t.Fatalf("feature-only commit %q is still on branches %q after the reset, want on none", featureTip, out)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "feat.txt")); !os.IsNotExist(err) {
		t.Fatalf("feat.txt survived the hard reset: err=%v", err)
	}
}

// Choosing Fast-Forward for a branch that has commits the upstream does not
// is refused before anything happens: the error names the other actions, and
// neither HEAD nor the current branch moved — the checkout never happened.
func TestCheckoutSyncFastForwardDivergedRefused(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-ff-diverged")
	featureTip, _ := featureDivergedFromUpstream(t, repo)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "main")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	confirmCheckoutSync(t, h)

	assertRepoErrorShown(t, h, "cannot fast-forward feature: it has commits that origin/feature does not; choose Merge, Rebase or Reset instead")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused fast-forward = %q, want main — the checkout must not have happened", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
		t.Fatalf("HEAD after the refused fast-forward = %q, want %q", got, mainTip)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != featureTip {
		t.Fatalf("feature moved to %q despite the refused fast-forward, want %q", got, featureTip)
	}
}

// Without the stash checkbox a local modification that the checkout would
// overwrite makes Git refuse the switch: the error surfaces, HEAD stays on
// the original branch, and the modification is intact.
func TestCheckoutSyncLocalChangesRefuseCheckout(t *testing.T) {
	repo := newGitRepo(t, "cs-dirty-refuse")
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("shared.txt", "l1\nfeature-l2\nl3\nl4\nl5\nl6\n", "feature edits line two")
	repo.Git("push", "-q", "-u", "origin", "feature")
	repo.Checkout("main")
	localEdit := "dirty-l1\nl2\nl3\nl4\nl5\nl6\n"
	repo.WriteFile("shared.txt", localEdit)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected an action error when the checkout would overwrite local changes, got nil")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("Repository Error missing after the refused checkout; texts=%q", h.ui.Texts())
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused checkout = %q, want main", got)
	}
	assertFileBytes(t, repo, "shared.txt", localEdit)
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after the refused checkout = %q, want empty", out)
	}
}

// With the stash checkbox the same modification is put aside, the branch is
// checked out and fast-forwarded, and the modification returns to the working
// tree with no entry left in the stash list.
func TestCheckoutSyncStashReappliesLocalChanges(t *testing.T) {
	repo := newGitRepo(t, "cs-dirty-stash")
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	// Feature edits line six and the local edit touches line one: enough
	// unchanged lines apart for the stash reapply to merge cleanly — while Git
	// refuses the bare checkout over any local change to the file either way.
	repo.CommitFile("shared.txt", "l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n", "feature edits line six")
	repo.Git("push", "-q", "-u", "origin", "feature")
	repo.Checkout("main")
	repo.WriteFile("shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stash checkout and fast-forward failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}
	// Line 6 came from the fast-forwarded feature, line 1 survived from the stash.
	assertFileBytes(t, repo, "shared.txt", "dirty-l1\nl2\nl3\nl4\nl5\nfeature-l6\nl7\nl8\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after the sync = %q, want empty — the reapply must not linger", out)
	}
	assertRefGone(t, repo, "refs/worktree/branchline-sync-stash")
}

// A merge that conflicts stops in the merge state with the local changes held
// in the stash list; aborting through the operation banner returns them to
// the working tree.
func TestCheckoutSyncMergeConflictStashAndAbort(t *testing.T) {
	repo := newGitRepo(t, "cs-conflict")
	repo.CommitFile("a.txt", "base\n", "initial commit")
	repo.CommitFile("notes.txt", "notes\n", "notes commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("a.txt", "upstream edits\n", "upstream edits line one")
	repo.Git("push", "-q", "-u", "origin", "feature")
	// feature goes back to the base and diverges from the upstream it still
	// tracks: both sides edited a.txt's one line since.
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	repo.CommitFile("a.txt", "feature edits\n", "feature edits line one")
	repo.Checkout("main")
	// notes.txt is identical in both branches, so the stash reapply after the
	// abort is clean; the merge conflict comes from a.txt alone.
	repo.WriteFile("notes.txt", "dirty notes\n")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Merge…")
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()
	confirmCheckoutSync(t, h)

	st := h.app.session.Snapshot().Status
	if st.State != "merge" {
		t.Fatalf("state after the conflicting merge = %q, want merge", st.State)
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch while stopped on the conflict = %q, want feature", got)
	}
	if out := strings.TrimSpace(repo.Git("stash", "list")); out == "" {
		t.Fatal("stash list is empty while stopped on the conflict — the local changes must be held there")
	}
	if data := repo.Git("diff", "--name-only", "--diff-filter=U"); !strings.Contains(data, "a.txt") {
		t.Fatalf("unmerged files while stopped = %q, want a.txt", data)
	}

	if err := h.ui.Click("Abort Operation"); err != nil {
		t.Fatalf("click Abort Operation: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.Click("Confirm Abort"); err != nil {
		t.Fatalf("click Confirm Abort: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("aborting the merge failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after the abort = %q, want none", got)
	}
	assertFileBytes(t, repo, "notes.txt", "dirty notes\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after the abort = %q, want empty — the reapply must not linger", out)
	}
}

// trackBehindUpstream creates branch at main, publishes it with upstream
// tracking, advances the upstream with one commit, resets the branch back
// behind it, and returns to main.
func trackBehindUpstream(t *testing.T, repo *gitRepoFixture, branch, fileName string) {
	t.Helper()
	repo.CheckoutNewBranch(branch)
	repo.Git("push", "-q", "-u", "origin", branch)
	repo.CommitFile(fileName, branch+"\n", branch+" work")
	repo.Git("push", "-q", "origin", branch)
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	repo.Checkout("main")
}

// The stash checkbox remembers its state across uses: the submitted choice
// greets the reopened dialog, while canceling never writes it.
func TestCheckoutSyncStashCheckboxRemembered(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-remember")
	trackBehindUpstream(t, repo, "feature", "ups.txt")
	trackBehindUpstream(t, repo, "topic", "topic.txt")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	stashBox := func() ui.AccessInfo {
		t.Helper()
		box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Stash and reapply local changes")
		if !ok {
			t.Fatalf("stash checkbox missing; texts=%q", h.ui.Texts())
		}
		return box
	}

	// Untouched, the checkbox starts unchecked.
	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	if box := stashBox(); box.Checked {
		t.Fatal("stash checkbox starts checked, want unchecked")
	}

	// Ticking and submitting writes the choice to the store.
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()
	confirmCheckoutSync(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and fast-forward failed: %v", err)
	}
	if !h.store.Sheet(store.CheckoutSyncAutostash, false) {
		t.Fatal("store did not remember the ticked stash checkbox")
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the sync = %q, want feature", got)
	}

	// Reopening on the other branch shows the remembered tick.
	chooseCheckoutAnd(t, h, "topic", "Fast-Forward…")
	if box := stashBox(); !box.Checked {
		t.Fatal("reopened dialog did not remember the ticked stash checkbox")
	}

	// Unticking and canceling writes nothing: the store keeps the tick.
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("untick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()
	if box := stashBox(); box.Checked {
		t.Fatal("unticking the stash checkbox did not take")
	}
	cancelCheckoutSync(t, h)
	if !h.store.Sheet(store.CheckoutSyncAutostash, false) {
		t.Fatal("canceling overwrote the remembered stash checkbox")
	}
	chooseCheckoutAnd(t, h, "topic", "Fast-Forward…")
	if box := stashBox(); !box.Checked {
		t.Fatal("dialog reopened after canceling did not keep the remembered tick")
	}
	cancelCheckoutSync(t, h)
}

// When the store cannot be written the submit still runs the checkout and
// sync: the save failure surfaces as a toast and the operation completes.
func TestCheckoutSyncStashSaveFailureToastsAndSyncs(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-savefail")
	_, upstreamTip := featureBehindUpstream(t, repo)

	// Build the harness by hand so the store's directory can be made
	// read-only after the initial snapshot: SetSheets then fails on write.
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}
	confirmCheckoutSync(t, h)
	if err := os.Chmod(storeDir, 0o755); err != nil {
		t.Fatalf("restore store dir permissions: %v", err)
	}

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout and fast-forward failed although only the save did: %v", err)
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != upstreamTip {
		t.Fatalf("feature after the sync = %q, want the upstream tip %q — the operation must complete despite the failed save", got, upstreamTip)
	}
}

// Committing on the branch or dropping its upstream configuration after the
// dialog opened refuses the checkout: the error says the branch or its
// upstream changed, and the repository is exactly as the external change left
// it.
func TestCheckoutSyncGuardsExternalStateChanges(t *testing.T) {
	t.Run("branch moved externally", func(t *testing.T) {
		repo, _ := checkoutSyncBase(t, "cs-guard-moved")
		featureBehindUpstream(t, repo)

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		// Merge… keeps the fast-forward precheck out of the way, so the
		// branch-tip recheck is the only guard that can refuse this write.
		chooseCheckoutAnd(t, h, "feature", "Merge…")
		assertCheckoutSyncDialog(t, h, "feature", "origin/feature")

		repo.Checkout("feature")
		moved := repo.CommitFile("ext.txt", "ext\n", "external feature commit")
		repo.Checkout("main")

		confirmCheckoutSync(t, h)

		assertRepoErrorShown(t, h, "the branch or its upstream changed after the dialog opened")
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch after the refused checkout = %q, want main", got)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != moved {
			t.Fatalf("feature after the refused checkout = %q, want the external commit %q", got, moved)
		}
		if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/feature"); err != nil {
			t.Fatalf("origin/feature missing after the refused checkout: %v", err)
		}
	})

	t.Run("upstream dropped externally", func(t *testing.T) {
		repo, _ := checkoutSyncBase(t, "cs-guard-upstream")
		featureTip, _ := featureBehindUpstream(t, repo)
		mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "main")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
		assertCheckoutSyncDialog(t, h, "feature", "origin/feature")

		repo.Git("branch", "--unset-upstream", "feature")

		confirmCheckoutSync(t, h)

		assertRepoErrorShown(t, h, "the branch or its upstream changed after the dialog opened")
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch after the refused checkout = %q, want main", got)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
			t.Fatalf("HEAD after the refused checkout = %q, want %q", got, mainTip)
		}
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "feature"); got != featureTip {
			t.Fatalf("feature moved to %q despite the refused checkout, want %q", got, featureTip)
		}
	})
}

// Canceling, Escape, clicking the backdrop and switching repositories all
// close the dialog without running a single Git command: the recording
// executor on the repository never fires.
func TestCheckoutSyncCloseRunsNoGitCommands(t *testing.T) {
	newFixtureHarness := func(t *testing.T, name string) (*gitRepoFixture, *testHarness) {
		t.Helper()
		repo, _ := checkoutSyncBase(t, name)
		featureBehindUpstream(t, repo)
		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())
		return repo, h
	}

	t.Run("cancel", func(t *testing.T) {
		repo, h := newFixtureHarness(t, "cs-close-cancel")
		rec := installGitCallRecorder(t, h)
		chooseCheckoutAnd(t, h, "feature", "Merge…")
		cancelCheckoutSync(t, h)
		h.drain()
		if calls := rec.calls(); len(calls) != 0 {
			t.Fatalf("canceling ran git commands %v, want none", calls)
		}
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch after canceling = %q, want main", got)
		}
	})

	t.Run("escape", func(t *testing.T) {
		repo, h := newFixtureHarness(t, "cs-close-escape")
		rec := installGitCallRecorder(t, h)
		chooseCheckoutAnd(t, h, "feature", "Merge…")
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
			t.Fatal("dialog stayed open after Escape")
		}
		h.drain()
		if calls := rec.calls(); len(calls) != 0 {
			t.Fatalf("Escape ran git commands %v, want none", calls)
		}
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch after Escape = %q, want main", got)
		}
	})

	t.Run("backdrop", func(t *testing.T) {
		repo, h := newFixtureHarness(t, "cs-close-backdrop")
		rec := installGitCallRecorder(t, h)
		chooseCheckoutAnd(t, h, "feature", "Merge…")
		// The modal is centered in the 1200x800 test window; the top-left
		// corner is well inside the dimmed backdrop, outside the dialog.
		h.ui.ClickAt(20, 20)
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
			t.Fatal("dialog stayed open after the backdrop click")
		}
		h.drain()
		if calls := rec.calls(); len(calls) != 0 {
			t.Fatalf("the backdrop click ran git commands %v, want none", calls)
		}
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch after the backdrop click = %q, want main", got)
		}
	})

	t.Run("switch repository", func(t *testing.T) {
		repo, h := newFixtureHarness(t, "cs-close-switch")
		rec := installGitCallRecorder(t, h)
		chooseCheckoutAnd(t, h, "feature", "Merge…")

		other := newGitRepo(t, "cs-close-other")
		other.CommitFile("o.txt", "o\n", "other initial commit")
		h.openRepo(other.StoreRepo())
		h.drain()

		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); ok {
			t.Fatal("dialog stayed open after switching repositories")
		}
		if st := checkoutSyncUI(h.app); st.open {
			t.Fatal("dialog state still open after switching repositories")
		}
		if calls := rec.calls(); len(calls) != 0 {
			t.Fatalf("switching repositories ran git commands on the first repository %v, want none", calls)
		}
		if got := currentBranch(t, repo); got != "main" {
			t.Fatalf("current branch of the first repository after switching = %q, want main", got)
		}
	})
}

// While an operation is in progress the Checkout button is disabled and
// Enter does not submit either: the dialog stays open and nothing runs.
func TestCheckoutSyncEnterDisabledDuringOperation(t *testing.T) {
	repo, _ := checkoutSyncBase(t, "cs-enter-disabled")
	featureBehindUpstream(t, repo)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	chooseCheckoutAnd(t, h, "feature", "Fast-Forward…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")

	// Start a conflicting merge externally while the dialog is open.
	repo.CheckoutNewBranch("conflict-side")
	repo.CommitFile("a.txt", "side\n", "side edit")
	repo.Checkout("main")
	repo.CommitFile("a.txt", "main\n", "main edit")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "conflict-side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: status state = %q, want merge", got)
	}

	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); !ok || !btn.Disabled {
		t.Fatalf("confirm button = %+v found=%v during the merge, want disabled", btn, ok)
	}

	rec := installGitCallRecorder(t, h)
	mainTip := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	h.ui.Key(0, ui.KeyEnter)
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Checkout and Sync"); !ok {
		t.Fatal("dialog closed although Enter must not submit while disabled")
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("Enter while disabled ran git commands %v, want none", calls)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainTip {
		t.Fatalf("HEAD after Enter while disabled = %q, want unchanged %q", got, mainTip)
	}
	repo.Git("merge", "--abort")
}

// Over a real loopback SSH connection, Checkout and… with the Merge action
// switches the server repository to the branch and records the merge commit
// there.
func TestCheckoutSyncMergeLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")

	// feature on the server, pushed, then diverged: the upstream commit adds
	// ups.txt, the feature commit adds feat.txt.
	srv.RunGit(t, repoDir, "checkout", "-q", "-b", "feature")
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "feature")
	srv.WriteFile(t, repoDir, "ups.txt", []byte("ups\n"))
	srv.RunGit(t, repoDir, "add", "--", "ups.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "upstream work")
	upstreamTip := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "HEAD"))
	srv.RunGit(t, repoDir, "push", "-q", "origin", "feature")
	srv.RunGit(t, repoDir, "reset", "-q", "--hard", "HEAD~1")
	srv.WriteFile(t, repoDir, "feat.txt", []byte("feat\n"))
	srv.RunGit(t, repoDir, "add", "--", "feat.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "feature work")
	srv.RunGit(t, repoDir, "checkout", "-q", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("feature"); !ok {
		t.Fatalf("feature missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	chooseCheckoutAnd(t, h, "feature", "Merge…")
	assertCheckoutSyncDialog(t, h, "feature", "origin/feature")
	confirmCheckoutSync(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH checkout and merge failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("server checked-out branch after the sync = %q, want feature", got)
	}
	parents := strings.Fields(strings.TrimSpace(srv.RunGit(t, repoDir, "rev-list", "--parents", "-1", "feature")))
	if len(parents) != 3 {
		t.Fatalf("server feature parents after the merge = %q, want a merge commit with two parents", parents)
	}
	if parents[2] != upstreamTip {
		t.Fatalf("second parent of the server merge = %q, want the upstream tip %q", parents[2], upstreamTip)
	}
	if got := string(srv.ReadFile(t, repoDir, "feat.txt")); got != "feat\n" {
		t.Fatalf("server feat.txt = %q, want feat\\n", got)
	}
	if got := string(srv.ReadFile(t, repoDir, "ups.txt")); got != "ups\n" {
		t.Fatalf("server ups.txt = %q, want ups\\n", got)
	}
}
