package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// remotePullFixture builds a repository with main checked out tracking a bare
// origin, and a feature branch one commit ahead of main pushed to it: the
// remote-tracking branch origin/feature exists while no local branch named
// feature does. It returns the repository and the bare remote's path.
func remotePullFixture(t *testing.T, name string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("feature.txt", "feature\n", "feature work")
	repo.Checkout("main")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	return repo, bareDir
}

// divergeMainFromFeature commits main.txt on main, so main and the remote
// feature each have one commit the other does not.
func divergeMainFromFeature(t *testing.T, repo *gitRepoFixture) string {
	t.Helper()
	return repo.CommitFile("main.txt", "main\n", "main work")
}

// chooseRemotePullInto right-clicks the remote branch row of the sidebar and
// chooses its Pull into item, failing the test when either step does not
// reach the menu.
func chooseRemotePullInto(t *testing.T, h *testHarness, rb, into string) {
	t.Helper()
	if err := h.ui.RightClick(rb); err != nil {
		t.Fatalf("right-click remote branch row %q: %v\ntexts=%q", rb, err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Pull into '" + into + "'…"); err != nil {
		t.Fatalf("choose Pull into '%s'…: %v", into, err)
	}
	h.ui.Frame()
}

// assertPullDialog verifies the Pull dialog is open naming the source remote
// branch and the target branch, in the dialog's expected layout.
func assertPullDialog(t *testing.T, h *testHarness, rb, into string) {
	t.Helper()
	if !hasExactText(h.ui, "Pull") {
		t.Fatalf("pull dialog title missing; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, rb) {
		t.Fatalf("pull dialog missing the source line %q; texts=%q", rb, h.ui.Texts())
	}
	if !hasExactText(h.ui, into) {
		t.Fatalf("pull dialog missing the target line %q; texts=%q", into, h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Pull Remote Branch")
}

// confirmRemotePull clicks the dialog's Pull button and drains the worker.
func confirmRemotePull(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Confirm Pull Remote Branch"); err != nil {
		t.Fatalf("click Confirm Pull Remote Branch: %v", err)
	}
	h.drain()
}

// featureMergedIntoMain reports whether the remote feature tip is an ancestor
// of HEAD, proving the pull merged it into the current branch.
func featureMergedIntoMain(t *testing.T, repo *gitRepoFixture) bool {
	t.Helper()
	tip := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/feature")
	_, err := tryGitCommand(repo.Path(), "merge-base", "--is-ancestor", tip, "HEAD")
	return err == nil
}

// A divergent pull of a remote branch through the menu creates a merge commit
// containing both sides when neither box is ticked, the menu shows the item
// between Checkout and Merge behind separators, and pulling again when
// everything is merged already toasts Already up to date.
func TestRemoteBranchPullIntoMergeAndUpToDateToast(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-merge")
	localTip := divergeMainFromFeature(t, repo)

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The menu order: Checkout, a separator, Pull into, a separator, Merge.
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	h.ui.Frame()
	want := []string{
		"Checkout…",
		"-",
		"Pull into 'main'…",
		"-",
		"Merge into 'main'…",
		"Rebase 'main' on 'origin/feature'…",
		"Interactively Rebase on 'origin/feature'…",
		"-",
		"New Branch…",
		"New Tag…",
		"-",
		"Delete 'origin/feature'…",
		"-",
		"Copy Branch Name",
	}
	if menu := h.ui.Menu(); !slices.Equal(menu, want) {
		t.Fatalf("remote branch menu = %q, want %q", menu, want)
	}
	if err := h.ui.ChooseMenuItem("Pull into 'main'…"); err != nil {
		t.Fatalf("choose Pull into 'main'…: %v", err)
	}
	h.ui.Frame()
	assertPullDialog(t, h, "origin/feature", "main")

	// Canceling leaves the repository untouched.
	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	if hasExactText(h.ui, "Confirm Pull Remote Branch") {
		t.Fatal("pull dialog stayed open after Cancel")
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD after canceling = %q, want unchanged %q", got, headBefore)
	}

	// A real pull with both boxes unticked creates the merge commit.
	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")
	confirmRemotePull(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge pull failed: %v", err)
	}
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	parents := strings.Fields(gitOutTrim(t, repo.Path(), "rev-list", "--parents", "-1", "HEAD"))
	if len(parents) != 3 {
		t.Fatalf("HEAD parents after merge pull = %q, want a merge commit with two parents", parents)
	}
	if !featureMergedIntoMain(t, repo) {
		t.Fatal("origin/feature is not an ancestor of HEAD after the merge pull")
	}
	assertFileBytes(t, repo, "feature.txt", "feature\n")
	assertFileBytes(t, repo, "main.txt", "main\n")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("main = %q, want HEAD %q — the pull must move the checked-out branch", got, head)
	}
	if localTip == head {
		t.Fatal("fixture broken: the local commit never happened")
	}

	// Pulling again finds nothing to bring and toasts the up-to-date note.
	chooseRemotePullInto(t, h, "origin/feature", "main")
	confirmRemotePull(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date pull returned error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePull) {
		t.Fatalf("up-to-date pull toast %q missing; texts=%q", git.UpToDatePull, h.ui.Texts())
	}
}

// Ticking Rebase instead of merge replays the local commit on top of the
// pulled remote branch: the history stays linear with the local work last.
func TestRemoteBranchPullIntoRebase(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-rebase")
	divergeMainFromFeature(t, repo)

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")
	if err := h.ui.Click("Rebase instead of merge"); err != nil {
		t.Fatalf("tick Rebase instead of merge: %v", err)
	}
	h.ui.Frame()
	confirmRemotePull(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase pull failed: %v", err)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--merges", "--count", "HEAD"); got != "0" {
		t.Fatalf("merge commits after rebase pull = %q, want 0 — the history must stay linear", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("commit count after rebase pull = %q, want 3 (base, feature work, replayed main work)", got)
	}
	subjects := gitOutTrim(t, repo.Path(), "log", "--format=%s", "-3")
	lines := strings.Split(subjects, "\n")
	if len(lines) != 3 || lines[0] != "main work" || lines[1] != "feature work" || lines[2] != "initial commit" {
		t.Fatalf("log subjects after rebase pull = %q, want main work / feature work / initial commit", lines)
	}
	assertFileBytes(t, repo, "feature.txt", "feature\n")
	assertFileBytes(t, repo, "main.txt", "main\n")
}

// Ticking Stash and reapply local changes carries uncommitted edits across
// the pull even when they touch a file the pull updates: the remote branch
// and the working tree edit different lines of the same file, git would
// refuse the pull over the local changes without the stash, and after the
// pull both sides are in the file with no stash entry lingering.
func TestRemoteBranchPullIntoStash(t *testing.T) {
	// The remote feature edits line 1 of a.txt and the uncommitted local edit
	// touches line 6: git refuses to merge over any local change to a file
	// the pull updates, however disjoint the lines, so without the stash the
	// pull fails before it starts.
	repo := newGitRepo(t, "rbpull-stash")
	repo.CommitFile("a.txt", "1\n2\n3\n4\n5\n6\n", "initial commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("a.txt", "feature-1\n2\n3\n4\n5\n6\n", "feature edits line one")
	repo.Checkout("main")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	localEdit := "1\n2\n3\n4\n5\nlocal-6\n"
	repo.WriteFile("a.txt", localEdit)

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	if err := h.ui.Click("Stash and reapply local changes"); err != nil {
		t.Fatalf("tick Stash and reapply local changes: %v", err)
	}
	h.ui.Frame()
	confirmRemotePull(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stash pull failed: %v", err)
	}
	tip := gitOutTrim(t, repo.Path(), "rev-parse", "refs/remotes/origin/feature")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != tip {
		t.Fatalf("HEAD after stash pull = %q, want the fast-forwarded feature tip %q", got, tip)
	}
	// Line 1 came from the remote branch, line 6 survived from the stash.
	assertFileBytes(t, repo, "a.txt", "feature-1\n2\n3\n4\n5\nlocal-6\n")
	if out := strings.TrimSpace(repo.Git("stash", "list")); out != "" {
		t.Fatalf("stash list after the pull = %q, want empty — the reapply must not linger", out)
	}
	assertRefGone(t, repo, "refs/worktree/branchline-sync-stash")
}

// When the current branch changed after the dialog opened — here an external
// checkout — confirming the pull is refused and the repository stays exactly
// as the external change left it.
func TestRemoteBranchPullIntoGuardBranchSwitched(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-guard-branch")
	repo.CreateBranch("other")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")

	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main")
	repo.Checkout("other")

	confirmRemotePull(t, h)

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := currentBranch(t, repo); got != "other" {
		t.Fatalf("current branch after the refused pull = %q, want other", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved to %q despite the refused pull, want %q", got, mainBefore)
	}
	if featureMergedIntoMain(t, repo) {
		t.Fatal("origin/feature merged into HEAD even though the pull was refused")
	}
	assertFileBytes(t, repo, "a.txt", "one\n")
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Fatal("a merge is in progress even though the pull was refused")
	}
}

// When HEAD moved on the same branch after the dialog opened — here an
// external commit — confirming the pull is refused and the moved HEAD stays
// exactly where the external commit put it.
func TestRemoteBranchPullIntoGuardHeadMoved(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-guard-head")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")

	moved := repo.CommitFile("ext.txt", "ext\n", "external commit")

	confirmRemotePull(t, h)

	assertRepoErrorShown(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD after the refused pull = %q, want the external commit %q", got, moved)
	}
	if featureMergedIntoMain(t, repo) {
		t.Fatal("origin/feature merged into HEAD even though the pull was refused")
	}
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Fatal("a merge is in progress even though the pull was refused")
	}
}

// When the remote was removed after the dialog opened, confirming the pull is
// refused before anything is fetched or merged and the branch stays put.
func TestRemoteBranchPullIntoGuardRemoteRemoved(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-guard-remote")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")

	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main")
	repo.Git("remote", "remove", "origin")

	confirmRemotePull(t, h)

	assertRepoErrorShown(t, h, "no longer exists")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/heads/main"); got != mainBefore {
		t.Fatalf("main moved to %q despite the refused pull, want %q", got, mainBefore)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != mainBefore {
		t.Fatalf("HEAD after the refused pull = %q, want %q", got, mainBefore)
	}
}

// A pull whose merge conflicts stops in the conflicted merge state: the
// operation-in-progress banner with its recovery actions takes over.
func TestRemoteBranchPullIntoConflictBanner(t *testing.T) {
	repo := newGitRepo(t, "rbpull-conflict")
	repo.CommitFile("a.txt", "line\n", "base commit")
	bareDir := repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("a.txt", "theirs\n", "feature edits")
	repo.Checkout("main")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	repo.CommitFile("a.txt", "ours\n", "main edits")
	_ = bareDir

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	confirmRemotePull(t, h)

	st := h.app.session.Snapshot().Status
	if st.State != "merge" {
		t.Fatalf("state after conflicting pull = %q, want merge", st.State)
	}
	if !st.Conflicted() {
		t.Fatalf("conflicting pull did not stop on conflicts: %+v", st)
	}
	if _, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err != nil {
		t.Fatalf("MERGE_HEAD missing after the conflicting pull: %v", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Operation in progress"); !ok {
		t.Fatalf("operation banner missing after the conflicting pull; texts=%q", h.ui.Texts())
	}
	for _, label := range []string{"Continue Operation", "Abort Operation"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("banner button %q missing; texts=%q", label, h.ui.Texts())
		}
	}
	// The conflicted file carries both sides, nothing resolved silently.
	data := repo.Git("diff", "--name-only", "--diff-filter=U")
	if !strings.Contains(data, "a.txt") {
		t.Fatalf("unmerged files after the conflicting pull = %q, want a.txt", data)
	}
}

// The Pull into item hides where there is no current branch to pull into —
// on a detached HEAD — and the remote's HEAD symbolic reference is not a
// branch row at all, so nothing can ever pull it.
func TestRemoteBranchPullIntoMenuHiddenDetachedAndHEADSymref(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-hidden")
	repo.Git("remote", "set-head", "origin", "main")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Attached: the item is there.
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature: %v", err)
	}
	h.ui.Frame()
	if menu := h.ui.Menu(); !slices.Contains(menu, "Pull into 'main'…") {
		t.Fatalf("menu %q missing Pull into 'main'… on an attached HEAD", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	// The remote HEAD symref is not a branch row and cannot be right-clicked.
	if err := h.ui.RightClick("origin/HEAD"); err == nil {
		t.Fatal("origin/HEAD was right-clickable as a branch row")
	}

	// Detached: the item is gone because there is no branch to pull into.
	repo.Git("checkout", head)
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: status branch = %q, want detached", got)
	}
	if err := h.ui.RightClick("origin/feature"); err != nil {
		t.Fatalf("right-click origin/feature while detached: %v", err)
	}
	h.ui.Frame()
	for _, item := range h.ui.Menu() {
		if strings.HasPrefix(item, "Pull into") {
			t.Fatalf("detached menu shows %q even though there is no current branch; menu=%q", item, h.ui.Menu())
		}
	}
	if menu := h.ui.Menu(); !slices.Contains(menu, "Checkout…") {
		t.Fatalf("detached menu %q lost its other entries", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// While a write is in flight and while a merge is in progress, the Pull into
// item is disabled and refusing the choice opens nothing.
func TestRemoteBranchPullIntoMenuDisabledStates(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-disabled")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	refuse := func() {
		t.Helper()
		if err := h.ui.RightClick("origin/feature"); err != nil {
			t.Fatalf("right-click origin/feature: %v", err)
		}
		h.ui.Frame()
		if menu := h.ui.Menu(); !slices.Contains(menu, "Pull into 'main'…") {
			t.Fatalf("menu %q missing the Pull into item", menu)
		}
		err := h.ui.ChooseMenuItem("Pull into 'main'…")
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing Pull into 'main'… = %v, want a disabled error", err)
		}
		h.ui.CloseMenu()
		h.ui.Frame()
		if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Pull Remote Branch"); ok {
			t.Fatal("pull dialog opened from a disabled menu item")
		}
	}

	// Busy: hold a real stage write behind the worker gate. The snapshot
	// reload picks up the untracked file the fixture writes now.
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
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	refuse()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("stage write failed: %v", err)
	}

	// Operation in progress: a conflicting merge started externally.
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("a.txt", "side\n", "side edit")
	repo.Checkout("main")
	repo.CommitFile("a.txt", "main\n", "main edit")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "merge" {
		t.Fatalf("fixture: status state = %q, want merge", got)
	}
	refuse()
	repo.Git("merge", "--abort")
}

// Over a real loopback SSH connection, pulling a remote branch of the server
// repository through the menu fast-forwards the server's current branch and
// working tree.
func TestRemoteBranchPullIntoLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	// feature on the server, pushed to a bare repo of the same server, and
	// removed locally: only the remote-tracking branch remains.
	srv.RunGit(t, repoDir, "checkout", "-q", "-b", "feature")
	srv.WriteFile(t, repoDir, "feature.txt", []byte("feature\n"))
	srv.RunGit(t, repoDir, "add", "feature.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "feature work")
	srv.RunGit(t, repoDir, "checkout", "-q", "main")
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bare)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")
	srv.RunGit(t, repoDir, "push", "-q", "origin", "feature")
	srv.RunGit(t, repoDir, "branch", "-D", "feature")
	featureTip := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "refs/remotes/origin/feature"))

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if _, ok := h.ui.Find("origin/feature"); !ok {
		t.Fatalf("origin/feature missing from the SSH sidebar; texts=%q", h.ui.Texts())
	}

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")
	confirmRemotePull(t, h)

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH pull into failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "HEAD")); got != featureTip {
		t.Fatalf("server HEAD after the pull = %q, want the feature tip %q", got, featureTip)
	}
	if got := string(srv.ReadFile(t, repoDir, "feature.txt")); got != "feature\n" {
		t.Fatalf("server feature.txt = %q, want feature\\n", got)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Fatalf("server checked-out branch after the pull = %q, want main", got)
	}
}

// When a merge conflict starts externally after the pull dialog opened, HEAD
// has not moved and the branch is unchanged, but the guard still refuses the
// pull because an operation is in progress.
func TestRemoteBranchPullIntoGuardOperationInProgress(t *testing.T) {
	repo := newGitRepo(t, "rbpull-guard-op")
	repo.CommitFile("a.txt", "line\n", "base commit")
	repo.AddBareRemote("origin")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("a.txt", "theirs\n", "feature edits")
	repo.Checkout("main")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "-D", "feature")
	repo.CommitFile("a.txt", "ours\n", "main edits")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	assertPullDialog(t, h, "origin/feature", "main")

	// Start a conflicting merge externally while the dialog is open: the
	// feature branch edits the same line main already diverged on, so the
	// merge stops in the conflicted state with HEAD and the branch exactly
	// where the dialog captured them.
	headBefore := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "origin/feature"); err == nil {
		t.Fatalf("conflicting merge unexpectedly succeeded: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("fixture broken: the conflicting merge moved HEAD to %q", got)
	}

	confirmRemotePull(t, h)

	assertRepoErrorShown(t, h, "merge is in progress")
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("current branch after the refused pull = %q, want main", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD after the refused pull = %q, want %q", got, headBefore)
	}
	// The pull never ran: the only merge in progress is the external one, so
	// the conflicted file carries the external merge's markers and nothing
	// was pulled on top of it — proof enough is that the merge the external
	// command started is still the unresolved one of origin/feature.
	if data := repo.Git("diff", "--name-only", "--diff-filter=U"); !strings.Contains(data, "a.txt") {
		t.Fatalf("unmerged files after the refused pull = %q, want the external merge's a.txt", data)
	}
}

// A failed pull of a credentialed remote never leaks the credentials: not
// into the session error and not into any text the interface renders.
func TestRemoteBranchPullIntoErrorRedactsCredentials(t *testing.T) {
	repo, _ := remotePullFixture(t, "rbpull-redact")
	// Re-point origin to an unreachable HTTPS URL with embedded credentials
	// while keeping the existing refs/remotes/origin/feature tracking ref.
	// The password rides in the userinfo — which git itself anonymizes in
	// its error output — and in a token query parameter, which it echoes
	// verbatim: only the application's own redaction can scrub the second,
	// so removing it makes this test fail.
	repo.Git("remote", "set-url", "origin", "https://pulluser:pullsecretpw@127.0.0.1:9/repo.git?token=pullsecretpw")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	// Also open Changes view so both the toolbar and Changes error displays
	// render ActionErr.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()

	chooseRemotePullInto(t, h, "origin/feature", "main")
	confirmRemotePull(t, h)

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected ActionErr after pull against unreachable remote")
	}
	if strings.Contains(err.Error(), "pullsecretpw") {
		t.Fatalf("ActionErr leaks URL credential: %q", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after failed pull; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "pullsecretpw") {
			t.Fatalf("UI error leaks URL credential: %q", text)
		}
	}
}
