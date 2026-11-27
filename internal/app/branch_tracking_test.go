package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// newTrackingTestApp builds the harness with the window drawing its own
// menus: only then are the submenu's items and their check marks part of the
// frame, where assertions can reach them.
func newTrackingTestApp(t *testing.T, repos ...store.Repo) *testHarness {
	t.Helper()
	h := newTestApp(t, repos...)
	h.ui = ui.NewTester(func(c *ui.Context) {
		c.SetMenuPresentation(ui.PopoverMenus)
		h.app.view(c)
	}, 1200, 800)
	h.ui.Frame()
	return h
}

// openBranchTrackingMenu right-clicks the branch row of the sidebar and
// verifies the context menu opened.
func openBranchTrackingMenu(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	h.ui.Frame()
	if len(h.ui.Menu()) == 0 {
		t.Fatalf("no context menu on branch row %q; texts=%q", branch, h.ui.Texts())
	}
}

// fastForwardLabel is the menu label of the fast-forward item of a branch
// tracking upstream.
func fastForwardLabel(upstream string) string {
	return "Fast-Forward to '" + upstream + "'"
}

// menuItemAccess returns the access info of the open menu's item named name,
// or false when the menu has no such item.
func menuItemAccess(h *testHarness, name string) (ui.AccessInfo, bool) {
	for _, a := range h.ui.AccessAll() {
		if a.Name == name && (a.Role == ui.RoleMenuItem || a.Role == ui.RoleMenuItemCheckBox) {
			return a, true
		}
	}
	return ui.AccessInfo{}, false
}

// expandTrackingMenu opens the Tracking submenu of the open branch menu with
// the keyboard, so its items render and enter the accessibility tree.
func expandTrackingMenu(t *testing.T, h *testHarness) {
	t.Helper()
	r, ok := h.ui.Find("Tracking")
	if !ok {
		t.Fatalf("Tracking submenu missing from the menu; texts=%q", h.ui.Texts())
	}
	h.ui.Move(r.X+r.W/2, r.Y+r.H/2)
	h.ui.Key(0, ui.KeyRight)
	h.ui.Frame()
	if _, ok := menuItemAccess(h, "Remove tracking reference"); !ok {
		t.Fatalf("Tracking submenu did not expand; texts=%q", h.ui.Texts())
	}
}

// trackingFixture builds main on the initial commit with a bare origin, and
// topic one commit behind its upstream origin/topic. It returns the hash of
// topic's tip and of origin/topic.
func trackingFixture(t *testing.T, repo *gitRepoFixture) (topicHash, upstreamHash string) {
	t.Helper()
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("topic")
	repo.Git("push", "-q", "-u", "origin", "topic")
	// The upstream commit both adds a file and edits the shared one, so a
	// fast-forward has bytes to bring in and bytes a local edit can collide
	// with.
	repo.WriteFile("t.txt", "t\n")
	repo.WriteFile("a.txt", "one\ntwo\n")
	repo.Git("add", "t.txt", "a.txt")
	repo.Git("commit", "-q", "-m", "topic 2")
	repo.Git("push", "-q", "origin", "topic")
	upstreamHash = strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	topicHash = strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")
	return topicHash, upstreamHash
}

// trackingConfig reads the tracking configuration keys of a branch of the
// local fixture, "" when unset.
func trackingConfig(t *testing.T, repo *gitRepoFixture, branch, key string) string {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "config", "--get", "branch."+branch+"."+key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// sshTrackingConfig reads the tracking configuration keys on the server.
func sshTrackingConfig(t *testing.T, srv *sshtest.Target, dir, branch, key string) string {
	t.Helper()
	out := srv.RunShell(t, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(dir)+" config --get "+git.ShellQuote("branch."+branch+"."+key)+" 2>/dev/null || true")
	return strings.TrimSpace(out)
}

// A non-current branch behind its upstream fast-forwards through the branch
// menu without a checkout: its ref moves to the upstream's commit while the
// working tree stays on main.
func TestBranchMenuFastForwardNonCurrentBranch(t *testing.T) {
	repo := newGitRepo(t, "ff-noncurrent")
	_, upstreamHash := trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	if !slices.Contains(h.ui.Menu(), fastForwardLabel("origin/topic")) {
		t.Fatalf("menu %q missing the fast-forward item", h.ui.Menu())
	}
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fast-forward failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != upstreamHash {
		t.Fatalf("topic after fast-forward = %q, want upstream %q", got, upstreamHash)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after fast-forward = %q, want main", got)
	}
	assertFileBytes(t, repo, "a.txt", "one\n")
}

// The current branch fast-forwards through the same item: HEAD and the
// branch move to the upstream's commit together.
func TestBranchMenuFastForwardCurrentBranch(t *testing.T) {
	repo := newGitRepo(t, "ff-current")
	_, upstreamHash := trackingFixture(t, repo)
	repo.Checkout("topic")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fast-forward of the current branch failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != upstreamHash {
		t.Fatalf("HEAD after fast-forward = %q, want upstream %q", got, upstreamHash)
	}
	if got := currentBranch(t, repo); got != "topic" {
		t.Fatalf("current branch after fast-forward = %q, want topic", got)
	}
	assertFileBytes(t, repo, "t.txt", "t\n")
}

// The fast-forward item is disabled when the branch is not behind its
// upstream, disabled when it diverged, and absent when the upstream is gone.
func TestBranchMenuFastForwardItemStates(t *testing.T) {
	repo := newGitRepo(t, "ff-states")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("topic")
	repo.Git("push", "-q", "-u", "origin", "topic")
	repo.Checkout("main")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// topic is level with origin/topic: nothing to fast-forward.
	openBranchTrackingMenu(t, h, "topic")
	item, ok := menuItemAccess(h, fastForwardLabel("origin/topic"))
	if !ok {
		t.Fatalf("fast-forward item missing for a level branch; menu %q", h.ui.Menu())
	}
	if !item.Disabled {
		t.Fatal("fast-forward item enabled for a branch that is not behind")
	}
	h.ui.CloseMenu()

	// Diverged: the upstream gains a commit of its own, and topic one of its
	// own too — one ahead, one behind, never fast-forwardable.
	repo.Checkout("topic")
	repo.CommitFile("t.txt", "t\n", "topic 2")
	repo.Git("push", "-q", "origin", "topic")
	repo.Git("reset", "-q", "--hard", "HEAD~1")
	repo.CommitFile("own.txt", "own\n", "own work")
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	openBranchTrackingMenu(t, h, "topic")
	item, ok = menuItemAccess(h, fastForwardLabel("origin/topic"))
	if !ok {
		t.Fatalf("fast-forward item missing for a diverged branch; menu %q", h.ui.Menu())
	}
	if !item.Disabled {
		t.Fatal("fast-forward item enabled for a diverged branch")
	}
	h.ui.CloseMenu()

	// Upstream gone: the item does not exist at all.
	repo.Git("update-ref", "-d", "refs/remotes/origin/topic")
	h.app.session.Reload()
	h.drain()

	openBranchTrackingMenu(t, h, "topic")
	for _, label := range h.ui.Menu() {
		if strings.HasPrefix(label, "Fast-Forward to") {
			t.Fatalf("fast-forward item %q shown for a gone upstream; menu %q", label, h.ui.Menu())
		}
	}
	h.ui.CloseMenu()
}

// While a repository write is busy the fast-forward item is disabled, so a
// fast-forward cannot start over the running write.
func TestBranchMenuFastForwardDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "ff-busy")
	trackingFixture(t, repo)
	repo.WriteFile("pending.txt", "pending\n")

	h := newTrackingTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}

	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing fast-forward while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got == gitOutTrim(t, repo.Path(), "rev-parse", "origin/topic") {
		t.Fatal("topic fast-forwarded while the repository was busy")
	}
}

// While a merge is in progress the fast-forward item is disabled: moving a
// branch underneath an operation would corrupt it.
func TestBranchMenuFastForwardDisabledDuringOperation(t *testing.T) {
	repo := newGitRepo(t, "ff-operation")
	trackingFixture(t, repo)
	// A conflicting merge of main's own history: conflicting.txt differs
	// between main and side.
	repo.WriteFile("conflict.txt", "base\n")
	repo.Git("add", "conflict.txt")
	repo.Git("commit", "-q", "-m", "conflict base")
	repo.CheckoutNewBranch("side")
	repo.WriteFile("conflict.txt", "theirs\n")
	repo.Git("commit", "-q", "-am", "side edits")
	repo.Checkout("main")
	repo.WriteFile("conflict.txt", "ours\n")
	repo.Git("commit", "-q", "-am", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly clean: %s", out)
	}

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: state = %q, want merge", got)
	}

	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing fast-forward during a merge = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Git("merge", "--abort")
}

// When the branch moves after the menu item was chosen — here an external
// commit on it — the worker refuses the fast-forward the user asked for
// against the old commit.
func TestBranchMenuFastForwardGuardBranchMovedExternally(t *testing.T) {
	repo := newGitRepo(t, "ff-guard-branch")
	trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	worker := h.queue.WaitWorker(t)

	// The branch gains its own commit before the worker runs.
	repo.Checkout("topic")
	repo.CommitFile("late.txt", "late\n", "late work")
	repo.Checkout("main")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "topic")

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertRepoErrorShown(t, h, "the branch moved")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != moved {
		t.Fatalf("topic overwritten by the refused fast-forward: %q, want %q", got, moved)
	}
}

// When the upstream moves after the menu item was chosen — here an external
// fetch advancing it — the worker refuses to fast-forward to a commit the
// user never saw.
func TestBranchMenuFastForwardGuardUpstreamMovedExternally(t *testing.T) {
	repo := newGitRepo(t, "ff-guard-upstream")
	topicHash, upstreamHash := trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	worker := h.queue.WaitWorker(t)

	// The upstream gains a commit before the worker runs: a detached commit
	// on top of it becomes the new origin/topic.
	repo.Git("checkout", "-q", "--detach", "origin/topic")
	repo.WriteFile("up.txt", "up\n")
	repo.Git("add", "up.txt")
	repo.Git("commit", "-q", "-m", "upstream work")
	repo.Git("update-ref", "refs/remotes/origin/topic", "HEAD")
	repo.Checkout("main")

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertRepoErrorShown(t, h, "origin/topic moved")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != topicHash {
		t.Fatalf("topic moved by the refused fast-forward: %q, want %q", got, topicHash)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "origin/topic"); got == upstreamHash {
		t.Fatalf("fixture: origin/topic did not move: %q", got)
	}
}

// A branch checked out in another worktree is never fast-forwarded without
// the checkout: the write is refused and the ref stays exactly where it was.
func TestBranchMenuFastForwardRefusedInOtherWorktree(t *testing.T) {
	repo := newGitRepo(t, "ff-worktree")
	topicHash, _ := trackingFixture(t, repo)
	wt := filepath.Join(t.TempDir(), "topic-checkout")
	repo.Git("worktree", "add", "-q", wt, "topic")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "checked out")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != topicHash {
		t.Fatalf("checked out topic moved: %q, want %q", got, topicHash)
	}
}

// Fast-forwarding the current branch over local changes that would be
// overwritten makes Git refuse: the error shows verbatim, the working tree
// bytes are exactly as before, and HEAD does not move.
func TestBranchMenuFastForwardCurrentBranchKeepsLocalChanges(t *testing.T) {
	repo := newGitRepo(t, "ff-dirty")
	topicHash, upstreamHash := trackingFixture(t, repo)
	repo.Checkout("topic")
	// a.txt is a tracked file the fast-forward would overwrite; edit it
	// locally without committing.
	repo.WriteFile("a.txt", "precious local edits\n")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	h.drain()

	assertRepoErrorShown(t, h, "")
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("action error = %v, want git's local-changes refusal", err)
	}
	assertFileBytes(t, repo, "a.txt", "precious local edits\n")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != topicHash {
		t.Fatalf("HEAD moved by the refused fast-forward: %q, want %q", got, topicHash)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "origin/topic"); got != upstreamHash {
		t.Fatalf("fixture: upstream = %q, want %q", got, upstreamHash)
	}
}

// Choosing a remote branch of the Tracking submenu makes the branch follow
// it: the configuration gains the remote and the merge ref.
func TestBranchMenuTrackingSetUpstream(t *testing.T) {
	repo := newGitRepo(t, "tracking-set")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("topic")
	repo.Checkout("main")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "origin/main"); err != nil {
		t.Fatalf("choose origin/main: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("setting the upstream failed: %v", err)
	}
	if got := trackingConfig(t, repo, "topic", "remote"); got != "origin" {
		t.Fatalf("branch.topic.remote = %q, want origin", got)
	}
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/main" {
		t.Fatalf("branch.topic.merge = %q, want refs/heads/main", got)
	}
}

// Choosing a different remote branch moves the tracking reference to it.
func TestBranchMenuTrackingSwitchUpstream(t *testing.T) {
	repo := newGitRepo(t, "tracking-switch")
	trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/topic" {
		t.Fatalf("fixture: branch.topic.merge = %q, want refs/heads/topic", got)
	}

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "origin/main"); err != nil {
		t.Fatalf("choose origin/main: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("switching the upstream failed: %v", err)
	}
	if got := trackingConfig(t, repo, "topic", "remote"); got != "origin" {
		t.Fatalf("branch.topic.remote = %q, want origin", got)
	}
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/main" {
		t.Fatalf("branch.topic.merge = %q, want refs/heads/main", got)
	}
}

// The Tracking submenu marks the current upstream with a check and leaves
// the others unmarked.
func TestBranchMenuTrackingCheckedUpstream(t *testing.T) {
	repo := newGitRepo(t, "tracking-checked")
	trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)

	checked, ok := menuItemAccess(h, "origin/topic")
	if !ok {
		t.Fatalf("origin/topic missing from the Tracking submenu; texts=%q", h.ui.Texts())
	}
	if !checked.Checked {
		t.Fatal("the current upstream origin/topic has no check mark")
	}
	other, ok := menuItemAccess(h, "origin/main")
	if !ok {
		t.Fatalf("origin/main missing from the Tracking submenu; texts=%q", h.ui.Texts())
	}
	if other.Checked {
		t.Fatal("origin/main carries a check mark though topic does not track it")
	}
	remove, ok := menuItemAccess(h, "Remove tracking reference")
	if !ok || remove.Disabled {
		t.Fatalf("Remove tracking reference = %+v found=%v, want present and enabled", remove, ok)
	}
	h.ui.CloseMenu()
}

// Choosing the item that is already checked — the upstream the branch has —
// writes nothing: no worker ever runs.
func TestBranchMenuTrackingCheckedItemIsNoop(t *testing.T) {
	repo := newGitRepo(t, "tracking-noop")
	trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "origin/topic"); err != nil {
		t.Fatalf("choose the checked origin/topic: %v", err)
	}
	if ran := h.queue.Drain(t); ran != 0 {
		t.Fatalf("choosing the checked item ran %d writes, want 0", ran)
	}
	h.ui.Frame()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("no-op choice produced an error: %v", err)
	}
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/topic" {
		t.Fatalf("branch.topic.merge after the no-op = %q, want refs/heads/topic", got)
	}
}

// Remove tracking reference unsets the upstream: both configuration keys go
// and the branch keeps its commits.
func TestBranchMenuTrackingRemoveReference(t *testing.T) {
	repo := newGitRepo(t, "tracking-remove")
	topicHash, _ := trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "Remove tracking reference"); err != nil {
		t.Fatalf("choose Remove tracking reference: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("removing the tracking reference failed: %v", err)
	}
	if got := trackingConfig(t, repo, "topic", "remote"); got != "" {
		t.Fatalf("branch.topic.remote after removal = %q, want empty", got)
	}
	if got := trackingConfig(t, repo, "topic", "merge"); got != "" {
		t.Fatalf("branch.topic.merge after removal = %q, want empty", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != topicHash {
		t.Fatalf("topic moved by removing its upstream: %q, want %q", got, topicHash)
	}
}

// A repository without remote branches shows only Remove tracking reference
// in the Tracking submenu.
func TestBranchMenuTrackingOnlyRemoveWithoutRemotes(t *testing.T) {
	repo := newGitRepo(t, "tracking-noremote")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CheckoutNewBranch("topic")
	repo.Checkout("main")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)

	var items []string
	for _, a := range h.ui.AccessAll() {
		if a.Role == ui.RoleMenuItem || a.Role == ui.RoleMenuItemCheckBox {
			items = append(items, a.Name)
		}
	}
	if !slices.Contains(items, "Remove tracking reference") {
		t.Fatalf("Remove tracking reference missing; items %q", items)
	}
	for _, name := range items {
		if strings.Contains(name, "/") {
			t.Fatalf("remote branch item %q shown without any remote; items %q", name, items)
		}
	}
	// Without an upstream Remove tracking reference is disabled.
	remove, ok := menuItemAccess(h, "Remove tracking reference")
	if !ok || !remove.Disabled {
		t.Fatalf("Remove tracking reference without upstream = %+v found=%v, want present and disabled", remove, ok)
	}
	h.ui.CloseMenu()
}

// While a repository write is busy every item of the Tracking submenu is
// disabled.
func TestBranchMenuTrackingDisabledWhileBusy(t *testing.T) {
	repo := newGitRepo(t, "tracking-busy")
	trackingFixture(t, repo)
	repo.WriteFile("pending.txt", "pending\n")

	h := newTrackingTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	for _, item := range []string{"Remove tracking reference", "origin/main", "origin/topic"} {
		if err := h.ui.ChooseMenuItem("Tracking", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while busy = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/topic" {
		t.Fatalf("tracking reference changed while busy: merge = %q", got)
	}
}

// When the tracking reference changes after the menu item was chosen — here
// the branch is made to follow another branch externally — the worker
// refuses the write the user asked for against the old reference.
func TestBranchMenuTrackingGuardUpstreamChangedExternally(t *testing.T) {
	repo := newGitRepo(t, "tracking-guard")
	trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: setting a new upstream is refused when the reference moved.
	h.queue.PauseWorkers()
	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "origin/main"); err != nil {
		t.Fatalf("choose origin/main: %v", err)
	}
	worker := h.queue.WaitWorker(t)

	repo.Git("branch", "--set-upstream-to", "origin/main", "topic")

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertRepoErrorShown(t, h, "tracking reference changed")
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/main" {
		t.Fatalf("branch.topic.merge overwritten by the refused write: %q, want refs/heads/main", got)
	}

	// Round 2: removing the reference is refused the same way. The branch
	// now follows origin/main; return it to origin/topic through the UI is
	// not needed — the refusal only compares what the user saw.
	h.app.session.ClearActionErr()
	h.app.session.Reload()
	h.drain()

	h.queue.PauseWorkers()
	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "Remove tracking reference"); err != nil {
		t.Fatalf("choose Remove tracking reference: %v", err)
	}
	worker = h.queue.WaitWorker(t)

	repo.Git("branch", "--set-upstream-to", "origin/topic", "topic")

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertRepoErrorShown(t, h, "tracking reference changed")
	if got := trackingConfig(t, repo, "topic", "merge"); got != "refs/heads/topic" {
		t.Fatalf("branch.topic.merge removed by the refused write: %q, want refs/heads/topic", got)
	}
}

// --- Loopback SSH ---

// Over a real SSH connection the branch menu fast-forwards a branch on the
// server, and the Tracking submenu sets an upstream there. Every assertion
// reads the server's repository through a second SSH channel.
func TestBranchMenuFastForwardLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// topic behind its upstream on the server, mirroring the local fixture.
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "topic")
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	srv.RunGit(t, repo.Path, "push", "-q", "-u", "origin", "topic")
	remoteCommitFile(t, srv, repo.Path, "t.txt", "t\n", "topic 2")
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "topic")
	upstreamHash := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.RunGit(t, repo.Path, "reset", "-q", "--hard", "HEAD~1")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTrackingTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)
	if _, ok := h.ui.Find("topic"); !ok {
		t.Fatalf("topic missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	openBranchTrackingMenu(t, h, "topic")
	if !slices.Contains(h.ui.Menu(), fastForwardLabel("origin/topic")) {
		t.Fatalf("SSH menu %q missing the fast-forward item", h.ui.Menu())
	}
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH fast-forward failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "topic")); got != upstreamHash {
		t.Fatalf("remote topic after fast-forward = %q, want upstream %q", got, upstreamHash)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("remote current branch after fast-forward = %q, want main", got)
	}
}

// Over SSH the Tracking submenu sets the upstream of a branch of the server
// repository: the configuration keys land in the server's repository.
func TestBranchMenuTrackingLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "topic")
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "main")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	h := newTrackingTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	openBranchTrackingMenu(t, h, "topic")
	expandTrackingMenu(t, h)
	if err := h.ui.ChooseMenuItem("Tracking", "origin/main"); err != nil {
		t.Fatalf("choose origin/main over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH setting the upstream failed: %v", err)
	}
	if got := sshTrackingConfig(t, srv, repo.Path, "topic", "remote"); got != "origin" {
		t.Fatalf("remote branch.topic.remote = %q, want origin", got)
	}
	if got := sshTrackingConfig(t, srv, repo.Path, "topic", "merge"); got != "refs/heads/main" {
		t.Fatalf("remote branch.topic.merge = %q, want refs/heads/main", got)
	}
}

// When the branch's configured upstream changes externally between clicking
// Fast-Forward and the worker running — even if the old remote-tracking ref
// itself has not moved — the worker refuses and leaves the branch untouched.
func TestBranchMenuFastForwardGuardUpstreamConfigChangedExternally(t *testing.T) {
	repo := newGitRepo(t, "ff-guard-upstream-cfg")
	topicHash, _ := trackingFixture(t, repo)

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	h.queue.PauseWorkers()
	openBranchTrackingMenu(t, h, "topic")
	if err := h.ui.ChooseMenuItem(fastForwardLabel("origin/topic")); err != nil {
		t.Fatalf("choose fast-forward: %v", err)
	}
	worker := h.queue.WaitWorker(t)

	// Unset the tracking configuration externally while refs/remotes/origin/topic
	// still points at the same commit.
	repo.Git("branch", "--unset-upstream", "topic")

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	assertRepoErrorShown(t, h, "tracking reference changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "topic"); got != topicHash {
		t.Fatalf("topic moved after its tracking configuration was removed: %q, want %q", got, topicHash)
	}
}

// The local branch context menu orders Merge, Rebase, Fast-Forward/Tracking,
// Pull/Push, and Rename/Delete/Copy with single separators between sections
// both with and without remotes.
func TestBranchMenuOrderWithTrackingSyncAndManage(t *testing.T) {
	repo := newGitRepo(t, "branch-menu-order")
	trackingFixture(t, repo)
	repo.Checkout("topic")

	h := newTrackingTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	openBranchTrackingMenu(t, h, "topic")
	menu := h.ui.Menu()
	h.ui.CloseMenu()

	wantOrder := []string{
		"Merge into 'topic'…",
		"Rebase 'topic' on 'topic'…",
		"Interactively Rebase on 'topic'…",
		"-",
		"New Branch…",
		"New Tag…",
		"-",
		fastForwardLabel("origin/topic"),
		"Tracking",
		"-",
		"Pull 'origin/topic'",
		"Push to 'origin/topic'…",
		"-",
		"Rename…",
		"Delete…",
		"-",
		"Copy Branch Name",
	}
	if !slices.Equal(menu, wantOrder) {
		t.Fatalf("branch context menu order = %q, want %q", menu, wantOrder)
	}

	// Without remotes the Pull/Push section omits itself without leaving a
	// duplicate separator behind.
	noRem := newGitRepo(t, "branch-menu-order-noremote")
	noRem.CommitFile("a.txt", "one\n", "initial commit")
	hNoRem := newTrackingTestApp(t)
	openHistoryPage(t, hNoRem, noRem.StoreRepo())

	openBranchTrackingMenu(t, hNoRem, "main")
	menuNoRem := hNoRem.ui.Menu()
	hNoRem.ui.CloseMenu()

	wantNoRem := []string{
		"Merge into 'main'…",
		"Rebase 'main' on 'main'…",
		"Interactively Rebase on 'main'…",
		"-",
		"New Branch…",
		"New Tag…",
		"-",
		"Tracking",
		"-",
		"Rename…",
		"Delete…",
		"-",
		"Copy Branch Name",
	}
	if !slices.Equal(menuNoRem, wantNoRem) {
		t.Fatalf("branch context menu without remotes = %q, want %q", menuNoRem, wantNoRem)
	}
}
