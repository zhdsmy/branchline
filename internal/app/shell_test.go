package app

import (
	"errors"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// Clicking sidebar navigation rows switches between Local Changes and History
// and updates the active page route and sidebar collapsible sections.
func TestSidebarNavigationSwitchesPagesAndTogglesSections(t *testing.T) {
	repo := newGitRepo(t, "alpha")
	repo.CommitFile("README.md", "hello\n", "initial commit")
	repo.CreateBranch("feature-ui")
	repo.CreateTag("v0.1.0")
	repo.AddBareRemote("origin")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if h.app.route.page != pageChanges {
		t.Fatalf("initial page = %v, want pageChanges", h.app.route.page)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History nav row: %v", err)
	}
	h.ui.Frame()
	if h.app.route.page != pageHistory {
		t.Fatalf("after clicking History: page = %v, want pageHistory", h.app.route.page)
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "History"); !ok || info.Current != ui.CurrentPage {
		t.Fatalf("History accessible current state = %+v (found=%v), want CurrentPage", info, ok)
	}

	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes nav row: %v", err)
	}
	h.ui.Frame()
	if h.app.route.page != pageChanges {
		t.Fatalf("after clicking Changes: page = %v, want pageChanges", h.app.route.page)
	}

	// Collapsing Branches hides its rows without hiding Remotes or Tags.
	if _, ok := h.ui.Find("feature-ui"); !ok {
		t.Fatal("expected branch feature-ui visible before collapse")
	}
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("click Branches header: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("feature-ui"); ok {
		t.Fatal("branch feature-ui stayed visible after collapsing Branches")
	}
	if _, ok := h.ui.Find("origin"); !ok {
		t.Fatal("collapsing Branches hid Remotes")
	}
	if _, ok := h.ui.Find("v0.1.0"); !ok {
		t.Fatal("collapsing Branches hid Tags")
	}
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("re-expand Branches header: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("feature-ui"); !ok {
		t.Fatal("branch feature-ui did not reappear after expanding Branches")
	}
}

// Back and Forward buttons and shortcuts navigate route history, and opening a
// new page clears the forward stack.
func TestBackAndForwardNavigation(t *testing.T) {
	repo := newGitRepo(t, "nav-repo")
	repo.CommitFile("a.txt", "1\n", "first")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Back"); !ok || !info.Disabled {
		t.Fatalf("Back should be disabled initially: %+v (found=%v)", info, ok)
	}
	if info, ok := h.ui.AccessByRole(ui.RoleButton, "Forward"); !ok || !info.Disabled {
		t.Fatalf("Forward should be disabled initially: %+v (found=%v)", info, ok)
	}

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if h.app.route.page != pageHistory {
		t.Fatalf("page = %v, want pageHistory", h.app.route.page)
	}

	if err := h.ui.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	h.ui.Frame()
	if h.app.route.page != pageChanges {
		t.Fatalf("after Back click: page = %v, want pageChanges", h.app.route.page)
	}

	if err := h.ui.Click("Forward"); err != nil {
		t.Fatalf("click Forward: %v", err)
	}
	h.ui.Frame()
	if h.app.route.page != pageHistory {
		t.Fatalf("after Forward click: page = %v, want pageHistory", h.app.route.page)
	}

	// Keyboard shortcuts navigate the same stack, and a fresh navigation clears Forward.
	h.ui.Key(ui.Cmd, ui.KeyBracketLeft)
	h.ui.Frame()
	if h.app.route.page != pageChanges {
		t.Fatalf("after Cmd+[: page = %v, want pageChanges", h.app.route.page)
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History to replace forward stack: %v", err)
	}
	h.ui.Frame()
	if len(h.app.forward) != 0 {
		t.Fatalf("forward stack not cleared after new navigation: %+v", h.app.forward)
	}
	h.ui.Key(ui.Cmd, ui.KeyBracketRight)
	h.ui.Frame()
	if h.app.route.page != pageHistory {
		t.Fatalf("Cmd+] with empty forward stack moved to %v", h.app.route.page)
	}
}

// Switching repositories via the Repository Switcher discards late snapshot
// results from the previously open repository even when the old worker
// completes after the new repository has already loaded.
func TestSwitchRepositoryDiscardsStaleSessionResult(t *testing.T) {
	repoA := newGitRepo(t, "repo-alpha")
	hashA := repoA.CommitFile("alpha.txt", "alpha\n", "alpha commit")
	repoA.CreateBranch("only-on-alpha")

	repoB := newGitRepo(t, "repo-beta")
	hashB := repoB.CommitFile("beta.txt", "beta\n", "beta commit")
	repoB.CreateBranch("only-on-beta")

	h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())

	// Pause the load of repo-alpha so it finishes strictly after repo-beta.
	h.queue.PauseWorkers()
	h.app.openStartupRepo()
	h.ui.Frame()
	oldWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Switch to the other repository through the Repository Switcher UI.
	firstID := h.app.activeRepoID()
	var targetName, targetHash, staleBranch, targetBranch string
	if h.app.session.Meta().Name == "repo-alpha" {
		targetName, targetHash, staleBranch, targetBranch = "repo-beta", hashB, "only-on-alpha", "only-on-beta"
	} else {
		targetName, targetHash, staleBranch, targetBranch = "repo-alpha", hashA, "only-on-beta", "only-on-alpha"
	}

	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(targetName); err != nil {
		t.Fatalf("choose %s in Repository Switcher: %v", targetName, err)
	}
	h.ui.Frame()

	if h.app.activeRepoID() == firstID {
		t.Fatal("Repository Switcher did not switch active repository")
	}

	// Let the new repository's load complete and apply first.
	h.queue.RunNext(t)
	h.ui.Frame()
	if got := h.app.session.Snapshot().Status.Head; got != targetHash {
		t.Fatalf("active repo HEAD = %q, want %q", got, targetHash)
	}

	// Now unblock the old repository's worker and drain its stale callback.
	h.queue.ReleaseWorker(oldWorker)
	h.drain()

	if h.app.session.Meta().Name != targetName {
		t.Fatalf("active repo changed to %q after stale callback", h.app.session.Meta().Name)
	}
	if got := h.app.session.Snapshot().Status.Head; got != targetHash {
		t.Fatalf("stale snapshot overwrote HEAD: got %q, want %q", got, targetHash)
	}
	if _, ok := h.ui.Find(staleBranch); ok {
		t.Fatalf("stale branch %q leaked into active sidebar", staleBranch)
	}
	if _, ok := h.ui.Find(targetBranch); !ok {
		t.Fatalf("expected active branch %q to remain visible", targetBranch)
	}
}

// A write operation marks the session busy, exposes busy state to the UI,
// rejects concurrent re-entrant writes, reloads the snapshot on completion,
// and retains errors separately from snapshot loads.
func TestSessionDoBusyRejectsReentryReloadsAndRetainsError(t *testing.T) {
	repo := newGitRepo(t, "write-repo")
	repo.CommitFile("base.txt", "base\n", "base commit")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if len(h.app.session.Snapshot().Refs.Branches) != 1 {
		t.Fatalf("initial branches = %+v, want 1", h.app.session.Snapshot().Refs.Branches)
	}

	started := make(chan struct{})
	proceed := make(chan struct{})
	var releaseProceed sync.Once
	release := func() { releaseProceed.Do(func() { close(proceed) }) }
	defer release()
	if ok := h.app.session.do("Create branch", func(r *git.Repo) error {
		close(started)
		<-proceed
		return r.Run("branch", "created-by-do")
	}); !ok {
		t.Fatal("first session.do was rejected unexpectedly")
	}
	<-started
	h.ui.Frame()

	if !h.app.session.Busy() || h.app.session.BusyLabel() != "Create branch" {
		t.Fatalf("busy state = (%v, %q), want (true, %q)", h.app.session.Busy(), h.app.session.BusyLabel(), "Create branch")
	}
	if status, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Status"); !ok || !status.Busy {
		t.Fatalf("toolbar busy status = %+v (found=%v), want Busy=true", status, ok)
	}
	if fetchBtn, ok := h.ui.AccessByRole(ui.RoleButton, "Fetch"); !ok || !fetchBtn.Disabled {
		t.Fatalf("Fetch button while busy = %+v (found=%v), want Disabled=true", fetchBtn, ok)
	}

	reentrantRan := false
	if ok := h.app.session.do("Second write", func(r *git.Repo) error {
		reentrantRan = true
		return r.Run("branch", "must-not-exist")
	}); ok {
		t.Fatal("re-entrant session.do succeeded while session was busy")
	}

	release()
	h.drain()

	if reentrantRan {
		t.Fatal("re-entrant write worker ran despite rejection")
	}
	if h.app.session.Busy() {
		t.Fatal("session remained busy after write completed")
	}
	if h.app.session.ActionErr() != nil {
		t.Fatalf("unexpected action error: %v", h.app.session.ActionErr())
	}
	if _, ok := h.ui.Find("created-by-do"); !ok {
		t.Fatalf("post-write reload did not pick up created-by-do; branches=%+v", h.app.session.Snapshot().Refs.Branches)
	}

	// A failing write clears busy, reloads snapshot, and retains ActionErr.
	wantErr := errors.New("simulated git push rejected")
	if ok := h.app.session.do("Push", func(*git.Repo) error {
		return wantErr
	}); !ok {
		t.Fatal("write after idle was rejected")
	}
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("session stayed busy after failed write")
	}
	if !errors.Is(h.app.session.ActionErr(), wantErr) {
		t.Fatalf("ActionErr = %v, want %v", h.app.session.ActionErr(), wantErr)
	}
	if h.app.session.LoadErr() != nil {
		t.Fatalf("LoadErr = %v, want nil after reload succeeded", h.app.session.LoadErr())
	}
	if !h.app.session.Loaded() || len(h.app.session.Snapshot().Refs.Branches) != 2 {
		t.Fatalf("failed write erased snapshot: loaded=%v branches=%d", h.app.session.Loaded(), len(h.app.session.Snapshot().Refs.Branches))
	}
}

// Switching repositories while a write is in flight discards the old write's
// completion so it cannot clear the new repository's state or record a stale error.
func TestSwitchRepositoryDiscardsStaleWriteCompletion(t *testing.T) {
	repoA := newGitRepo(t, "write-a")
	repoA.CommitFile("a.txt", "a\n", "a")
	repoB := newGitRepo(t, "write-b")
	repoB.CommitFile("b.txt", "b\n", "b")

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	started := make(chan struct{})
	proceed := make(chan struct{})
	var releaseProceed sync.Once
	release := func() { releaseProceed.Do(func() { close(proceed) }) }
	defer release()
	if ok := h.app.session.do("Old failing write", func(*git.Repo) error {
		close(started)
		<-proceed
		return errors.New("old repo error")
	}); !ok {
		t.Fatal("write on repoA rejected")
	}
	<-started

	// Switch to repoB while repoA's write is still running.
	h.openRepo(repoB.StoreRepo())
	h.queue.RunNext(t) // repoB's initial load completes
	h.ui.Frame()

	release()
	h.drain()

	if h.app.session.Meta().Name != "write-b" {
		t.Fatalf("active repo = %q, want write-b", h.app.session.Meta().Name)
	}
	if h.app.session.ActionErr() != nil {
		t.Fatalf("stale write error leaked into new session: %v", h.app.session.ActionErr())
	}
}

// Startup opens BRANCHLINE_OPEN (supporting both local paths and SSH addresses),
// restores the last-opened store repository otherwise, and shows an empty
// state when no repository is configured.
func TestStartupRepoSelectionAndSSHExecutorWiring(t *testing.T) {
	// Empty store shows the empty state.
	t.Setenv("BRANCHLINE_OPEN", "")
	emptyH := newTestApp(t)
	emptyH.app.openStartupRepo()
	emptyH.ui.Frame()
	if emptyH.app.session.HasRepo() {
		t.Fatal("empty store unexpectedly opened a repository")
	}
	if !emptyH.ui.HasText("No Repository Open") {
		t.Fatalf("empty state not rendered; texts=%q", emptyH.ui.Texts())
	}

	// BRANCHLINE_OPEN with a local repo path opens it and loads its commits.
	localRepo := newGitRepo(t, "env-local")
	hash := localRepo.CommitFile("f.txt", "data\n", "env commit")
	t.Setenv("BRANCHLINE_OPEN", localRepo.Path())
	localH := newTestApp(t)
	localH.app.openStartupRepo()
	localH.drain()
	if got := localH.app.session.Snapshot().Status.Head; got != hash {
		t.Fatalf("BRANCHLINE_OPEN local HEAD = %q, want %q", got, hash)
	}
	if _, ok := localH.app.session.Repo().Exec.(*git.LocalExecutor); !ok {
		t.Fatalf("local repo executor = %T, want *git.LocalExecutor", localH.app.session.Repo().Exec)
	}

	// Without BRANCHLINE_OPEN, startup restores the last active repository from store.
	t.Setenv("BRANCHLINE_OPEN", "")
	firstRepo := newGitRepo(t, "store-first")
	firstRepo.CommitFile("a.txt", "a\n", "first commit")
	lastRepo := newGitRepo(t, "store-last")
	lastHash := lastRepo.CommitFile("b.txt", "b\n", "last commit")
	storeH := newTestApp(t)
	savedFirst, err := storeH.store.Add(firstRepo.StoreRepo())
	if err != nil {
		t.Fatalf("add first repo: %v", err)
	}
	savedLast, err := storeH.store.Add(lastRepo.StoreRepo())
	if err != nil {
		t.Fatalf("add last repo: %v", err)
	}
	if err := storeH.store.SetTabSession([]string{savedFirst.ID, savedLast.ID}, savedLast.ID, true); err != nil {
		t.Fatalf("set active repo: %v", err)
	}
	storeH.app.openStartupRepo()
	storeH.drain()
	if storeH.app.session.Meta().ID != savedLast.ID || storeH.app.session.Snapshot().Status.Head != lastHash {
		t.Fatalf("restored repo = (%q, %q), want (%q, %q)", storeH.app.session.Meta().ID, storeH.app.session.Snapshot().Status.Head, savedLast.ID, lastHash)
	}

	// BRANCHLINE_OPEN with an SSH address constructs a real *git.SSHExecutor without dialing the network.
	t.Setenv("BRANCHLINE_OPEN", "alice@example.com:/srv/project.git")
	sshH := newTestApp(t)
	sshH.app.spawnFn = func(func()) {}
	sshH.app.openStartupRepo()
	sshH.ui.Frame()
	if !sshH.app.session.HasRepo() || sshH.app.session.Meta().Kind != store.SSH {
		t.Fatalf("expected SSH repo open, got %+v", sshH.app.session.Meta())
	}
	sshExec, ok := sshH.app.session.Repo().Exec.(*git.SSHExecutor)
	if !ok {
		t.Fatalf("SSH repo executor = %T, want *git.SSHExecutor", sshH.app.session.Repo().Exec)
	}
	if sshExec.User != "alice" || sshExec.Host != "example.com" || sshExec.Dir != "/srv/project.git" {
		t.Fatalf("unexpected SSHExecutor fields: %+v", sshExec)
	}
}

// Scoped asynchronous reads via sessionRead discard superseded results for the
// same scope as well as results from a previous repository.
func TestSessionReadDiscardsSupersededAndCrossRepoResults(t *testing.T) {
	repoA := newGitRepo(t, "read-a")
	repoA.CommitFile("a.txt", "a\n", "a")
	repoB := newGitRepo(t, "read-b")
	repoB.CommitFile("b.txt", "b\n", "b")

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	var applied []string
	sessionRead(h.app.session, "detail", func(*git.Repo) (string, error) {
		return "first", nil
	}, func(v string, _ error) {
		applied = append(applied, v)
	})
	firstWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	sessionRead(h.app.session, "detail", func(*git.Repo) (string, error) {
		return "second", nil
	}, func(v string, _ error) {
		applied = append(applied, v)
	})
	h.queue.RunNext(t)
	h.queue.ReleaseWorker(firstWorker)
	h.drain()

	if len(applied) != 1 || applied[0] != "second" {
		t.Fatalf("applied scoped reads = %v, want [second]", applied)
	}

	// Switching repositories also discards an in-flight scoped read.
	h.queue.PauseWorkers()
	sessionRead(h.app.session, "detail", func(*git.Repo) (string, error) {
		return "stale-after-switch", nil
	}, func(v string, _ error) {
		applied = append(applied, v)
	})
	crossRepoWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	h.openRepo(repoB.StoreRepo())
	h.queue.RunNext(t)
	h.queue.ReleaseWorker(crossRepoWorker)
	h.drain()

	if len(applied) != 1 || applied[0] != "second" {
		t.Fatalf("cross-repo scoped read leaked: applied = %v", applied)
	}
}

func pixelLuma(img *image.RGBA, x, y int) float64 {
	c := img.RGBAAt(x, y)
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}

// Dark mode sidebar selected and hovered navigation rows stay clearly
// distinguishable from the sidebar background at 2x scale, both when starting
// in dark mode and immediately after switching from light to dark mode.
func TestDarkModeSidebarSelectionAndHoverContrast(t *testing.T) {
	repo := newGitRepo(t, "contrast-repo")
	repo.CommitFile("README.md", "hi\n", "init")

	checkDark := func(t *testing.T, tt *ui.Tester) {
		t.Helper()
		tt.SetScale(2)
		tt.Frame()

		changesRect, ok := tt.Find("Changes")
		if !ok {
			t.Fatal("Changes nav row not found")
		}
		historyRect, ok := tt.Find("History")
		if !ok {
			t.Fatal("History nav row not found")
		}

		// Sample selected row IMMEDIATELY (before advancing the clock) to prove
		// light->dark theme switches do not bleed the previous theme's fill.
		bgY := int((historyRect.Y + historyRect.H + 40) * 2)
		bgX := int((changesRect.X + changesRect.W - 14) * 2)
		selY := int((changesRect.Y + changesRect.H/2) * 2)
		hovY := int((historyRect.Y + historyRect.H/2) * 2)

		imgNow := tt.Image()
		bgNow := pixelLuma(imgNow, bgX, bgY)
		selNow := pixelLuma(imgNow, bgX, selY)
		if delta := selNow - bgNow; delta < 25 || delta > 90 {
			t.Fatalf("dark selected nav row immediate contrast delta = %.1f (sel=%.1f, bg=%.1f), want in [25, 90]", delta, selNow, bgNow)
		}

		// Hover History and advance past the 110ms background animation.
		tt.Move(historyRect.X+historyRect.W-12, historyRect.Y+historyRect.H/2)
		tt.Advance(200 * time.Millisecond)
		tt.Frame()

		img := tt.Image()
		bgLuma := pixelLuma(img, bgX, bgY)
		selLuma := pixelLuma(img, bgX, selY)
		hovLuma := pixelLuma(img, bgX, hovY)

		if delta := selLuma - bgLuma; delta < 25 || delta > 90 {
			t.Fatalf("dark selected nav row contrast delta = %.1f (sel=%.1f, bg=%.1f), want in [25, 90]", delta, selLuma, bgLuma)
		}
		if delta := hovLuma - bgLuma; delta < 12 || hovLuma >= selLuma {
			t.Fatalf("dark hovered nav row contrast delta = %.1f (hov=%.1f, sel=%.1f, bg=%.1f), want >=12 and <selected", delta, hovLuma, selLuma, bgLuma)
		}
	}

	t.Run("switched from light to dark", func(t *testing.T) {
		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()
		// Switching without advancing the clock must snap cleanly to the dark
		// selected fill rather than bleeding the light theme's dark alpha.
		h.ui.SetDark(true)
		checkDark(t, h.ui)
	})

	t.Run("started directly in dark mode", func(t *testing.T) {
		h := newTestApp(t)
		h.openRepo(repo.StoreRepo())
		h.drain()
		darkTT := ui.NewTesterWith(h.app.view, 1200, 800, ui.TesterOptions{Dark: true})
		checkDark(t, darkTT)
	})
}
