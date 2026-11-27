package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

func isRefsCommand(args []string) bool {
	return len(args) > 0 && args[0] == "for-each-ref"
}

// pushPeerCommit clones bareDir into a fresh temporary checkout, commits
// fileName with content on branch, pushes it to bareDir, and returns the new
// commit hash.
func pushPeerCommit(t *testing.T, bareDir, branch, fileName, content, msg string) string {
	t.Helper()
	peerDir := filepath.Join(t.TempDir(), "peer-"+strings.ReplaceAll(branch, "/", "-"))
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Branchline Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	runGitCommand(t, peerDir, "checkout", branch)
	if err := os.WriteFile(filepath.Join(peerDir, fileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write peer %s: %v", fileName, err)
	}
	runGitCommand(t, peerDir, "add", "--", fileName)
	runGitCommand(t, peerDir, "commit", "-m", msg)
	runGitCommand(t, peerDir, "push", "origin", branch)
	return strings.TrimSpace(runGitCommand(t, peerDir, "rev-parse", "HEAD"))
}

// addSecondBareRemote creates a bare repository and wires it as remoteName
// without pushing HEAD or changing any branch's upstream.
func addSecondBareRemote(t *testing.T, repo *gitRepoFixture, remoteName string) string {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), remoteName+".git")
	runGitCommand(t, "", "init", "--bare", bareDir)
	repo.Git("remote", "add", remoteName, bareDir)
	return bareDir
}

// splitBranchUpstream resolves an upstream name against configured remotes,
// preferring the longest remote prefix when one remote name is a prefix of
// another.
func TestBranchMenuPushSplitUpstreamLongestRemotePrefix(t *testing.T) {
	remotes := []string{"origin", "origin/mirror", "upstream"}
	rem, br, ok := splitBranchUpstream("origin/mirror/feature/x", remotes)
	if !ok || rem != "origin/mirror" || br != "feature/x" {
		t.Fatalf("split(origin/mirror/feature/x) = (%q, %q, %v), want (origin/mirror, feature/x, true)", rem, br, ok)
	}
	rem, br, ok = splitBranchUpstream("origin/main", remotes)
	if !ok || rem != "origin" || br != "main" {
		t.Fatalf("split(origin/main) = (%q, %q, %v), want (origin, main, true)", rem, br, ok)
	}
	if _, _, ok := splitBranchUpstream("origin/", remotes); ok {
		t.Fatal("split(origin/) unexpectedly succeeded with empty branch name")
	}
	if _, _, ok := splitBranchUpstream("other/main", remotes); ok {
		t.Fatal("split(other/main) unexpectedly matched unknown remote")
	}
}

// The local branch context menu adapts Pull and Push to entries to currentness,
// upstream state, remote count, in-progress operations, and busy writes.
func TestBranchMenuPullAndPushMenuVisibilityAndDisabledStates(t *testing.T) {
	// 1. Repository with no remotes hides both Pull and Push to.
	noRemRepo := newGitRepo(t, "branch-sync-noremote")
	noRemRepo.CommitFile("a.txt", "one\n", "init")
	hNoRem := newTestApp(t)
	openSyncRepo(t, hNoRem, noRemRepo.StoreRepo())

	if err := hNoRem.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main (no remote): %v", err)
	}
	for _, item := range hNoRem.ui.Menu() {
		if strings.HasPrefix(item, "Pull ") || strings.HasPrefix(item, "Push to") {
			t.Fatalf("unexpected sync menu item %q without remotes; menu=%q", item, hNoRem.ui.Menu())
		}
	}
	hNoRem.ui.CloseMenu()

	// 2. Single remote: current tracked, non-current tracked, untracked, gone upstream.
	repo := newGitRepo(t, "branch-sync-vis")
	repo.CommitFile("a.txt", "one\n", "init")
	bareOrigin := repo.AddBareRemote("origin")

	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "f\n", "feature commit")
	repo.Git("push", "-u", "origin", "feature")

	repo.Checkout("main")
	repo.CheckoutNewBranch("stale")
	repo.CommitFile("s.txt", "s\n", "stale commit")
	repo.Git("push", "-u", "origin", "stale")
	// Delete refs/heads/stale on the bare remote and prune so upstream is marked gone.
	runGitCommand(t, bareOrigin, "update-ref", "-d", "refs/heads/stale")
	repo.Git("fetch", "--prune", "origin")

	repo.Checkout("main")
	repo.CreateBranch("local")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Current tracked branch main: both Pull and Push to '<upstream>'… appear.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Pull 'origin/main'") {
		t.Fatalf("main menu %q missing Pull 'origin/main'", menu)
	}
	if !slices.Contains(menu, "Push to 'origin/main'…") {
		t.Fatalf("main menu %q missing Push to 'origin/main'…", menu)
	}
	if slices.Contains(menu, "Push to 'origin'…") || slices.Contains(menu, "Push to") {
		t.Fatalf("tracked main menu %q should not show untracked Push to items", menu)
	}
	h.ui.CloseMenu()

	// Non-current tracked branch feature: no Pull, shows Push to 'origin/feature'….
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	menu = h.ui.Menu()
	if slices.Contains(menu, "Pull 'origin/feature'") {
		t.Fatalf("non-current branch feature menu %q must not show Pull", menu)
	}
	if !slices.Contains(menu, "Push to 'origin/feature'…") {
		t.Fatalf("feature menu %q missing Push to 'origin/feature'…", menu)
	}
	h.ui.CloseMenu()

	// Non-current untracked branch local with 1 remote: Push to 'origin'….
	if err := h.ui.RightClick("local"); err != nil {
		t.Fatalf("right-click local: %v", err)
	}
	menu = h.ui.Menu()
	for _, item := range menu {
		if strings.HasPrefix(item, "Pull ") {
			t.Fatalf("untracked branch local menu %q must not show Pull", menu)
		}
	}
	if !slices.Contains(menu, "Push to 'origin'…") {
		t.Fatalf("local menu %q missing Push to 'origin'…", menu)
	}
	h.ui.CloseMenu()

	// Gone upstream branch stale: no Pull, falls back to Push to 'origin'….
	if err := h.ui.RightClick("stale"); err != nil {
		t.Fatalf("right-click stale: %v", err)
	}
	menu = h.ui.Menu()
	for _, item := range menu {
		if strings.HasPrefix(item, "Pull ") {
			t.Fatalf("gone-upstream branch stale menu %q must not show Pull", menu)
		}
	}
	if !slices.Contains(menu, "Push to 'origin'…") {
		t.Fatalf("stale menu %q missing fallback Push to 'origin'…", menu)
	}
	h.ui.CloseMenu()

	// Current branch without upstream: no Pull, shows Push to 'origin'….
	repo.Checkout("local")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.RightClick("local"); err != nil {
		t.Fatalf("right-click checked-out local: %v", err)
	}
	menu = h.ui.Menu()
	for _, item := range menu {
		if strings.HasPrefix(item, "Pull ") {
			t.Fatalf("checked-out untracked branch local menu %q must not show Pull", menu)
		}
	}
	if !slices.Contains(menu, "Push to 'origin'…") {
		t.Fatalf("checked-out local menu %q missing Push to 'origin'…", menu)
	}
	h.ui.CloseMenu()

	// 3. Multiple remotes: untracked branch shows Push to submenu; tracked branch still shows single item.
	repo.Checkout("main")
	addSecondBareRemote(t, repo, "upstream")
	h.app.session.Reload()
	h.drain()

	if err := h.ui.RightClick("local"); err != nil {
		t.Fatalf("right-click local (multi-remote): %v", err)
	}
	menu = h.ui.Menu()
	if !slices.Contains(menu, "Push to") {
		t.Fatalf("multi-remote untracked local menu %q missing Push to submenu", menu)
	}
	if slices.Contains(menu, "Push to 'origin'…") {
		t.Fatalf("multi-remote untracked local menu %q should use submenu, not single item", menu)
	}
	h.ui.CloseMenu()

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main (multi-remote): %v", err)
	}
	menu = h.ui.Menu()
	if !slices.Contains(menu, "Pull 'origin/main'") || !slices.Contains(menu, "Push to 'origin/main'…") {
		t.Fatalf("multi-remote tracked main menu %q missing Pull or Push to 'origin/main'…", menu)
	}
	h.ui.CloseMenu()

	// 4. Operation in progress (merge conflict) disables Pull 'origin/main'.
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
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main during merge: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Pull 'origin/main' during merge = %v, want disabled error", err)
	}
	h.ui.CloseMenu()
	repo.Git("merge", "--abort")
	h.app.session.Reload()
	h.drain()

	// 5. Busy write disables Pull, tracked Push to, and multi-remote Push to submenu items.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	stageWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while stage worker is held")
	}

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Pull 'origin/main' while busy = %v, want disabled error", err)
	}
	h.ui.CloseMenu()

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main for push while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/main'…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Push to 'origin/main'… while busy = %v, want disabled error", err)
	}
	h.ui.CloseMenu()

	if err := h.ui.RightClick("local"); err != nil {
		t.Fatalf("right-click local while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to", "origin…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Push to -> origin… while busy = %v, want disabled error", err)
	}
	h.ui.CloseMenu()

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(stageWorker)
	h.drain()
}

// Choosing Pull '<upstream>' on the current branch brings in new remote
// commits and updates HEAD and the working tree; pulling again when already up
// to date toasts Already up to date without an error.
func TestBranchMenuPullFastForwardAndUpToDateToast(t *testing.T) {
	repo := newGitRepo(t, "branch-pull-ff")
	repo.CommitFile("a.txt", "one\n", "init")
	bareDir := repo.AddBareRemote("origin")

	peerHead := pushPeerCommit(t, bareDir, "main", "peer.txt", "from peer\n", "peer commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
		t.Fatalf("choose Pull 'origin/main': %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("pull failed: %v", err)
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("HEAD after pull = %q, want %q", got, peerHead)
	}
	assertFileBytes(t, repo, "peer.txt", "from peer\n")
	if got := h.app.session.Snapshot().Status.Head; got != peerHead {
		t.Fatalf("snapshot HEAD after pull = %q, want %q", got, peerHead)
	}

	// Pulling again shows the up-to-date toast with no error.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main again: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
		t.Fatalf("choose Pull 'origin/main' again: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date pull returned error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePull) {
		t.Fatalf("up-to-date pull toast %q missing; texts=%q", git.UpToDatePull, h.ui.Texts())
	}
}

// When pulling would overwrite an uncommitted local modification, git refuses,
// Repository Error surfaces the failure, HEAD stays put, and the modified file
// bytes remain intact.
func TestBranchMenuPullLocalChangesConflictPreservesBytes(t *testing.T) {
	repo := newGitRepo(t, "branch-pull-conflict")
	headBefore := repo.CommitFile("a.txt", "one\n", "init")
	bareDir := repo.AddBareRemote("origin")
	pushPeerCommit(t, bareDir, "main", "a.txt", "remote change\n", "remote edits a.txt")

	localBytes := "local uncommitted bytes\nkeep me intact\n"
	repo.WriteFile("a.txt", localBytes)

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
		t.Fatalf("choose Pull 'origin/main': %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected action error when pull would overwrite local changes, got nil")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("Repository Error missing after refused pull; texts=%q", h.ui.Texts())
	}
	if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("HEAD moved during refused pull: %q, want %q", got, headBefore)
	}
	assertFileBytes(t, repo, "a.txt", localBytes)
}

// The pull guard refuses without touching the repository when HEAD moved, the
// checked-out branch switched, the upstream changed, or status could not be
// read after the menu captured the branch state.
func TestBranchMenuPullGuardsExternalStateChanges(t *testing.T) {
	t.Run("HEAD moved externally", func(t *testing.T) {
		repo := newGitRepo(t, "pull-guard-head")
		repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("main"); err != nil {
			t.Fatalf("right-click main: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
			t.Fatalf("choose Pull 'origin/main': %v", err)
		}
		worker := h.queue.WaitWorker(t)

		moved := repo.CommitFile("ext.txt", "ext\n", "external commit")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "HEAD or the current branch changed")
		if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != moved {
			t.Fatalf("HEAD after refused pull = %q, want %q", got, moved)
		}
		if _, err := os.Stat(filepath.Join(repo.Path(), "peer.txt")); !os.IsNotExist(err) {
			t.Fatalf("peer.txt appeared despite refused pull: err=%v", err)
		}
	})

	t.Run("branch switched externally", func(t *testing.T) {
		repo := newGitRepo(t, "pull-guard-branch")
		head := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("main"); err != nil {
			t.Fatalf("right-click main: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
			t.Fatalf("choose Pull 'origin/main': %v", err)
		}
		worker := h.queue.WaitWorker(t)

		repo.CheckoutNewBranch("other")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "HEAD or the current branch changed")
		if got := currentBranch(t, repo); got != "other" {
			t.Fatalf("current branch after refused pull = %q, want other", got)
		}
		if got := strings.TrimSpace(repo.Git("rev-parse", "main")); got != head {
			t.Fatalf("main moved during refused pull: %q, want %q", got, head)
		}
	})

	t.Run("upstream changed externally", func(t *testing.T) {
		repo := newGitRepo(t, "pull-guard-upstream")
		head := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		repo.Git("push", "origin", "refs/heads/main:refs/heads/alt")
		repo.Git("fetch", "origin")
		pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("main"); err != nil {
			t.Fatalf("right-click main: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
			t.Fatalf("choose Pull 'origin/main': %v", err)
		}
		worker := h.queue.WaitWorker(t)

		repo.Git("branch", "--set-upstream-to=origin/alt", "main")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "upstream changed")
		if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("HEAD moved after upstream changed: %q, want %q", got, head)
		}
		if _, err := os.Stat(filepath.Join(repo.Path(), "peer.txt")); !os.IsNotExist(err) {
			t.Fatalf("peer.txt appeared despite upstream change refusal: err=%v", err)
		}
	})

	t.Run("status read fails", func(t *testing.T) {
		repo := newGitRepo(t, "pull-guard-status")
		head := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		pushPeerCommit(t, bareDir, "main", "peer.txt", "peer\n", "peer commit")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
		if err := h.ui.RightClick("main"); err != nil {
			t.Fatalf("right-click main: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
			t.Fatalf("choose Pull 'origin/main': %v", err)
		}
		h.drain()

		assertRepoErrorShown(t, h, "reading the repository state failed")
		if got := strings.TrimSpace(repo.Git("rev-parse", "HEAD")); got != head {
			t.Fatalf("HEAD moved despite status failure: %q, want %q", got, head)
		}
		if _, err := os.Stat(filepath.Join(repo.Path(), "peer.txt")); !os.IsNotExist(err) {
			t.Fatalf("peer.txt appeared despite status failure: err=%v", err)
		}
	})
}

// Push to '<upstream>'… pushes tracked current branches, tracked non-current
// branches without switching HEAD, and local branches tracking a differently
// named remote branch, and toasts Nothing to push when already up to date.
func TestBranchMenuPushTrackedCurrentNonCurrentAndRenamedUpstream(t *testing.T) {
	repo := newGitRepo(t, "branch-push-tracked")
	repo.CommitFile("a.txt", "one\n", "init")
	bareDir := repo.AddBareRemote("origin")

	// Set up tracked non-current branch feature with an unpushed commit.
	repo.CheckoutNewBranch("feature")
	repo.Git("push", "-u", "origin", "feature")
	featureTip := repo.CommitFile("f.txt", "feature\n", "feature commit")

	// Set up non-current branch work tracking origin/release (different name).
	repo.Checkout("main")
	repo.CheckoutNewBranch("work")
	repo.Git("push", "-u", "origin", "refs/heads/work:refs/heads/release")
	workTip := repo.CommitFile("w.txt", "work\n", "work for release")

	// Return to main and make an unpushed commit on current branch main.
	repo.Checkout("main")
	mainTip := repo.CommitFile("m.txt", "main\n", "main commit")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// 1. Push tracked current branch main.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/main'…"); err != nil {
		t.Fatalf("choose Push to 'origin/main'…: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push main failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != mainTip {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, mainTip)
	}

	// Pushing main again toasts UpToDatePush.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main again: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/main'…"); err != nil {
		t.Fatalf("choose Push to 'origin/main'… again: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("up-to-date push returned error: %v", err)
	}
	if !h.ui.HasText(git.UpToDatePush) {
		t.Fatalf("up-to-date push toast %q missing; texts=%q", git.UpToDatePush, h.ui.Texts())
	}

	// 2. Push tracked non-current branch feature without switching HEAD.
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/feature'…"); err != nil {
		t.Fatalf("choose Push to 'origin/feature'…: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push feature failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != featureTip {
		t.Fatalf("bare refs/heads/feature = %q, want %q", got, featureTip)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("pushing non-current feature switched HEAD to %q, want main", got)
	}

	// 3. Push non-current branch work tracking origin/release to refs/heads/release.
	if err := h.ui.RightClick("work"); err != nil {
		t.Fatalf("right-click work: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/release'…"); err != nil {
		t.Fatalf("choose Push to 'origin/release'…: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push work->release failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/release")); got != workTip {
		t.Fatalf("bare refs/heads/release = %q, want %q", got, workTip)
	}
	if _, err := tryGitCommand(bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/work"); err == nil {
		t.Fatal("pushing work to origin/release unexpectedly created refs/heads/work on remote")
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("pushing non-current work switched HEAD to %q, want main", got)
	}
}

// Pushing an untracked branch with a single remote uses Push to '<remote>'…
// and sets upstream tracking; with multiple remotes the Push to submenu pushes
// only to the chosen remote and leaves the other remote untouched.
func TestBranchMenuPushUntrackedSingleAndMultiRemoteSetsUpstream(t *testing.T) {
	repo := newGitRepo(t, "branch-push-untracked")
	repo.CommitFile("a.txt", "one\n", "init")
	bareOrigin := repo.AddBareRemote("origin")

	repo.CheckoutNewBranch("solo")
	soloTip := repo.CommitFile("solo.txt", "solo\n", "solo commit")
	repo.Checkout("main")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	// Single remote: Push to 'origin'… publishes solo and sets upstream.
	if err := h.ui.RightClick("solo"); err != nil {
		t.Fatalf("right-click solo: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin'…"); err != nil {
		t.Fatalf("choose Push to 'origin'…: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push solo failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareOrigin, "rev-parse", "refs/heads/solo")); got != soloTip {
		t.Fatalf("bare origin refs/heads/solo = %q, want %q", got, soloTip)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.solo.remote")); got != "origin" {
		t.Fatalf("branch.solo.remote = %q, want origin", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.solo.merge")); got != "refs/heads/solo" {
		t.Fatalf("branch.solo.merge = %q, want refs/heads/solo", got)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("pushing non-current solo switched HEAD to %q, want main", got)
	}

	// Add a second remote and push a new untracked branch through the Push to submenu.
	bareUpstream := addSecondBareRemote(t, repo, "upstream")
	repo.CheckoutNewBranch("multi")
	multiTip := repo.CommitFile("multi.txt", "multi\n", "multi commit")
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	if err := h.ui.RightClick("multi"); err != nil {
		t.Fatalf("right-click multi: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to", "upstream…"); err != nil {
		t.Fatalf("choose Push to -> upstream…: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push multi to upstream failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareUpstream, "rev-parse", "refs/heads/multi")); got != multiTip {
		t.Fatalf("bare upstream refs/heads/multi = %q, want %q", got, multiTip)
	}
	if _, err := tryGitCommand(bareOrigin, "rev-parse", "--verify", "--quiet", "refs/heads/multi"); err == nil {
		t.Fatal("pushing multi to upstream unexpectedly wrote refs/heads/multi to origin")
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.multi.remote")); got != "upstream" {
		t.Fatalf("branch.multi.remote = %q, want upstream", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.multi.merge")); got != "refs/heads/multi" {
		t.Fatalf("branch.multi.merge = %q, want refs/heads/multi", got)
	}
}

// A non-fast-forward push is never forced: git rejects it, Repository Error
// displays the failure, and the remote branch ref remains at the peer's commit.
func TestBranchMenuPushNonFastForwardRefusedWithoutForce(t *testing.T) {
	repo := newGitRepo(t, "branch-push-nonff")
	repo.CommitFile("a.txt", "one\n", "init")
	bareDir := repo.AddBareRemote("origin")

	repo.CheckoutNewBranch("feature")
	repo.Git("push", "-u", "origin", "feature")
	peerTip := pushPeerCommit(t, bareDir, "feature", "peer.txt", "peer\n", "peer advances feature")
	// Fetch so a force-with-lease would have matched if force were wrongly enabled!
	repo.Git("fetch", "origin")
	repo.CommitFile("local.txt", "local\n", "diverged local commit")
	repo.Checkout("main")

	h := newTestApp(t)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin/feature'…"); err != nil {
		t.Fatalf("choose Push to 'origin/feature'…: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected action error on non-fast-forward push, got nil")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("Repository Error missing after rejected push; texts=%q", h.ui.Texts())
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != peerTip {
		t.Fatalf("bare refs/heads/feature = %q, want unchanged peer commit %q", got, peerTip)
	}
}

// The push guard refuses without writing to any remote when the local branch
// moved, the target remote was removed, the branch's upstream changed, or
// reading references failed after the menu item was chosen.
func TestBranchMenuPushGuardsExternalStateChanges(t *testing.T) {
	t.Run("branch moved externally", func(t *testing.T) {
		repo := newGitRepo(t, "push-guard-moved")
		initHash := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		repo.CheckoutNewBranch("feature")
		repo.Git("push", "-u", "origin", "feature")
		repo.CommitFile("f1.txt", "f1\n", "feature commit 1")
		repo.Checkout("main")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("feature"); err != nil {
			t.Fatalf("right-click feature: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Push to 'origin/feature'…"); err != nil {
			t.Fatalf("choose Push to 'origin/feature'…: %v", err)
		}
		worker := h.queue.WaitWorker(t)

		// Externally move feature to a new commit before the worker runs.
		repo.Checkout("feature")
		repo.CommitFile("f2.txt", "f2\n", "external feature commit 2")
		repo.Checkout("main")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "branch moved")
		if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != initHash {
			t.Fatalf("bare refs/heads/feature moved to %q, want unchanged %q", got, initHash)
		}
	})

	t.Run("remote deleted externally", func(t *testing.T) {
		repo := newGitRepo(t, "push-guard-remote")
		repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		repo.CheckoutNewBranch("topic")
		repo.CommitFile("t.txt", "t\n", "topic commit")
		repo.Checkout("main")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("topic"); err != nil {
			t.Fatalf("right-click topic: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Push to 'origin'…"); err != nil {
			t.Fatalf("choose Push to 'origin'…: %v", err)
		}
		worker := h.queue.WaitWorker(t)

		repo.Git("remote", "remove", "origin")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "no longer exists")
		if _, err := tryGitCommand(bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/topic"); err == nil {
			t.Fatal("bare origin unexpectedly received refs/heads/topic after remote removal")
		}
	})

	t.Run("upstream changed externally", func(t *testing.T) {
		repo := newGitRepo(t, "push-guard-upstream")
		initHash := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		repo.CheckoutNewBranch("feature")
		repo.Git("push", "-u", "origin", "feature")
		repo.Git("push", "origin", "refs/heads/feature:refs/heads/other")
		repo.Git("fetch", "origin")
		repo.CommitFile("f.txt", "f\n", "feature commit")
		repo.Checkout("main")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		h.queue.PauseWorkers()
		if err := h.ui.RightClick("feature"); err != nil {
			t.Fatalf("right-click feature: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Push to 'origin/feature'…"); err != nil {
			t.Fatalf("choose Push to 'origin/feature'…: %v", err)
		}
		worker := h.queue.WaitWorker(t)

		repo.Git("branch", "--set-upstream-to=origin/other", "feature")
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(worker)
		h.drain()

		assertRepoErrorShown(t, h, "upstream changed")
		if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != initHash {
			t.Fatalf("bare refs/heads/feature moved to %q, want unchanged %q", got, initHash)
		}
		if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/other")); got != initHash {
			t.Fatalf("bare refs/heads/other moved to %q, want unchanged %q", got, initHash)
		}
	})

	t.Run("refs read fails", func(t *testing.T) {
		repo := newGitRepo(t, "push-guard-refs-err")
		initHash := repo.CommitFile("a.txt", "one\n", "init")
		bareDir := repo.AddBareRemote("origin")
		repo.CheckoutNewBranch("feature")
		repo.Git("push", "-u", "origin", "feature")
		repo.CommitFile("f.txt", "f\n", "feature commit")
		repo.Checkout("main")

		h := newTestApp(t)
		openSyncRepo(t, h, repo.StoreRepo())

		installFailFirstExecutor(t, h, isRefsCommand, errors.New("injected refs failure"))
		if err := h.ui.RightClick("feature"); err != nil {
			t.Fatalf("right-click feature: %v", err)
		}
		if err := h.ui.ChooseMenuItem("Push to 'origin/feature'…"); err != nil {
			t.Fatalf("choose Push to 'origin/feature'…: %v", err)
		}
		h.drain()

		assertRepoErrorShown(t, h, "reading repository references failed")
		if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/feature")); got != initHash {
			t.Fatalf("bare refs/heads/feature moved despite refs failure: %q, want %q", got, initHash)
		}
	})
}

// --- SSH loopback ---

// Over a real loopback SSH connection, choosing Pull 'origin/main' on the
// current branch pulls a peer's commit from the server's bare remote and
// updates both HEAD and the server working tree.
func TestBranchMenuPullLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	peerDir := srv.TempDir(t, "peer")
	srv.RunShell(t, "set -eu\nexport GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1\n"+
		"git clone -q "+shellQ(bareDir)+" "+shellQ(peerDir)+"\n"+
		"cd "+shellQ(peerDir)+"\n"+
		"git config user.name 'Branchline Peer'\ngit config user.email peer@branchline.invalid\n"+
		"printf 'ssh peer\\n' > sshpeer.txt\ngit add -- sshpeer.txt\ngit commit -q -m 'ssh peer commit'\n"+
		"git push -q origin main\n")
	peerHead := strings.TrimSpace(srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"))

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main over SSH: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Pull 'origin/main'"); err != nil {
		t.Fatalf("choose Pull 'origin/main' over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH branch menu pull failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "HEAD")); got != peerHead {
		t.Fatalf("remote HEAD after pull = %q, want %q", got, peerHead)
	}
	if got := string(srv.ReadFile(t, repoDir, "sshpeer.txt")); got != "ssh peer\n" {
		t.Fatalf("remote sshpeer.txt = %q, want %q", got, "ssh peer\n")
	}
}

// Over a real loopback SSH connection, choosing Push to 'origin'… on an
// untracked non-current branch pushes it to the server's bare remote, configures
// upstream tracking, and leaves the server's checked-out branch untouched.
func TestBranchMenuPushUntrackedNonCurrentLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")

	srv.RunGit(t, repoDir, "checkout", "-q", "-b", "topic")
	srv.WriteFile(t, repoDir, "topic.txt", []byte("topic over ssh\n"))
	srv.RunGit(t, repoDir, "add", "--", "topic.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "topic commit")
	topicHead := strings.TrimSpace(srv.RunGit(t, repoDir, "rev-parse", "HEAD"))
	srv.RunGit(t, repoDir, "checkout", "-q", "main")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	if err := h.ui.RightClick("topic"); err != nil {
		t.Fatalf("right-click topic over SSH: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push to 'origin'…"); err != nil {
		t.Fatalf("choose Push to 'origin'… over SSH: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH branch menu push failed: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, bareDir, "rev-parse", "refs/heads/topic")); got != topicHead {
		t.Fatalf("remote bare refs/heads/topic = %q, want %q", got, topicHead)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "config", "branch.topic.remote")); got != "origin" {
		t.Fatalf("remote branch.topic.remote = %q, want origin", got)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "config", "branch.topic.merge")); got != "refs/heads/topic" {
		t.Fatalf("remote branch.topic.merge = %q, want refs/heads/topic", got)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repoDir, "symbolic-ref", "--short", "HEAD")); got != "main" {
		t.Fatalf("remote checked-out branch = %q, want main", got)
	}
}
