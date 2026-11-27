package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// assertRefPresent fails the test when the repository does not resolve ref.
func assertRefPresent(t *testing.T, repo *gitRepoFixture, ref string) {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		t.Fatalf("%s missing: %v\n%s", ref, err, out)
	}
}

// assertRefGone fails the test when the repository still resolves ref.
func assertRefGone(t *testing.T, repo *gitRepoFixture, ref string) {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		t.Fatalf("%s still resolves: %s", ref, out)
	}
}

// assertTagPresent fails the test when the repository does not resolve the tag.
func assertTagPresent(t *testing.T, repo *gitRepoFixture, name string) {
	t.Helper()
	assertRefPresent(t, repo, "refs/tags/"+name)
}

// assertTagGone fails the test when the repository still resolves the tag.
func assertTagGone(t *testing.T, repo *gitRepoFixture, name string) {
	t.Helper()
	assertRefGone(t, repo, "refs/tags/"+name)
}

// dismissMenu closes a context menu left open by a refused or finished choice
// so the next right-click opens a fresh one.
func dismissMenu(t *testing.T, h *testHarness) {
	t.Helper()
	h.ui.CloseMenu()
	h.ui.Frame()
}

// chooseRemoteFetch right-clicks the remote row and chooses its Fetch item,
// failing the test when either step does not reach the menu.
func chooseRemoteFetch(t *testing.T, h *testHarness, remote string) {
	t.Helper()
	if err := h.ui.RightClick(remote); err != nil {
		t.Fatalf("right-click %s: %v", remote, err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Fetch '" + remote + "'"); err != nil {
		t.Fatalf("choose Fetch '%s': %v", remote, err)
	}
}

// newTwoRemoteRepo builds the single-remote sidebar fixture, wires a second
// bare remote as upstream, and pre-fetches it so its remote-tracking branches
// exist before any test action: a fetch of origin that wrongly pulls from
// upstream is then observable as upstream refs changing.
func newTwoRemoteRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := setupSidebarRepo(t, name)
	upstreamDir := newBareRemote(t, repo, "upstream")
	repo.Git("remote", "add", "upstream", upstreamDir)
	repo.Git("fetch", "upstream")
	return repo
}

// The Branches section header opens the New Branch dialog from its context
// menu in both the expanded and the collapsed state, and the dialog creates
// and checks out the branch exactly as clicking the New Branch row does.
func TestSidebarHeaderMenuBranchesNewBranch(t *testing.T) {
	repo := setupSidebarRepo(t, "sec-menu-branches")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	createViaHeader := func(branch string) {
		t.Helper()
		if err := h.ui.RightClick("Branches"); err != nil {
			t.Fatalf("right-click Branches header: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.ChooseMenuItem("New Branch…"); err != nil {
			t.Fatalf("choose New Branch…: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("Branch name") {
			t.Fatalf("New Branch dialog did not open; texts=%q", h.ui.Texts())
		}
		h.ui.Type(branch)
		h.ui.Frame()
		if err := h.ui.Click("Create Branch"); err != nil {
			t.Fatalf("click Create Branch: %v", err)
		}
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected create error for %s: %v", branch, err)
		}
		if !branchExists(t, repo, branch) {
			t.Fatalf("%s was not created in the repository", branch)
		}
		if got := currentBranch(t, repo); got != branch {
			t.Fatalf("current branch after create = %q, want %q (checkout was ticked)", got, branch)
		}
	}

	// Expanded: the rows are visible and the header menu creates the branch.
	createViaHeader("sec-expanded")
	if _, ok := h.ui.Find("sec-expanded"); !ok {
		t.Fatal("sec-expanded missing from the sidebar after create")
	}

	// Collapsed: the rows are hidden and the same menu entry still opens the
	// dialog and creates the branch.
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("collapse Branches: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("sec-expanded"); ok {
		t.Fatal("sec-expanded stayed visible after collapsing Branches")
	}
	createViaHeader("sec-collapsed")
	if err := h.ui.Click("Branches"); err != nil {
		t.Fatalf("re-expand Branches: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("sec-collapsed"); !ok {
		t.Fatal("sec-collapsed missing from the sidebar after re-expanding")
	}
}

// The Remotes section header opens the Add Remote dialog from its context
// menu in both the expanded and the collapsed state, and the dialog adds the
// remote with its branches fetched exactly as clicking the New Remote row
// does.
func TestSidebarHeaderMenuRemotesAddRemote(t *testing.T) {
	repo := setupSidebarRepo(t, "sec-menu-remotes")
	bareDir := newBareRemote(t, repo, "upstream")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	openViaHeader := func() {
		t.Helper()
		if err := h.ui.RightClick("Remotes"); err != nil {
			t.Fatalf("right-click Remotes header: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.ChooseMenuItem("Add New Remote…"); err != nil {
			t.Fatalf("choose Add New Remote…: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("URL:") {
			t.Fatalf("Add Remote dialog did not open; texts=%q", h.ui.Texts())
		}
	}

	// Collapsed first: the rows are hidden and the header menu still opens
	// the dialog; canceling leaves the repository untouched.
	if err := h.ui.Click("Remotes"); err != nil {
		t.Fatalf("collapse Remotes: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "origin"); ok {
		t.Fatal("origin row stayed visible after collapsing Remotes")
	}
	openViaHeader()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Add Remote: %v", err)
	}
	h.ui.Frame()
	if got := remoteList(t, repo); strings.Contains(got, "upstream") {
		t.Fatalf("upstream added even though the dialog was canceled: %q", got)
	}

	// Expanded: the same menu entry opens the dialog and a valid submission
	// adds the remote and fetches its branches.
	if err := h.ui.Click("Remotes"); err != nil {
		t.Fatalf("re-expand Remotes: %v", err)
	}
	h.ui.Frame()
	openViaHeader()
	h.ui.Type("upstream")
	typeField(t, h, "URL", bareDir, false)
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected add error: %v", err)
	}
	if got := remoteURLOf(t, repo, "upstream"); got != bareDir {
		t.Fatalf("git remote get-url upstream = %q, want %q", got, bareDir)
	}
	if _, ok := h.ui.Find("upstream/main"); !ok {
		t.Fatalf("upstream/main missing after the add's fetch; texts=%q", h.ui.Texts())
	}
}

// With no repository open the section header menus are inert: their items
// are disabled and choosing them opens nothing.
func TestSidebarHeaderMenuDisabledWithoutRepo(t *testing.T) {
	h := newTestApp(t)
	h.ui.Frame()

	if err := h.ui.RightClick("Branches"); err != nil {
		t.Fatalf("right-click Branches header without repo: %v", err)
	}
	h.ui.Frame()
	err := h.ui.ChooseMenuItem("New Branch…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("New Branch… without repo: err=%v, want a disabled refusal", err)
	}
	dismissMenu(t, h)
	if h.ui.HasText("Branch name") {
		t.Fatal("New Branch dialog opened without a repository")
	}

	if err := h.ui.RightClick("Remotes"); err != nil {
		t.Fatalf("right-click Remotes header without repo: %v", err)
	}
	h.ui.Frame()
	err = h.ui.ChooseMenuItem("Add New Remote…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Add New Remote… without repo: err=%v, want a disabled refusal", err)
	}
	dismissMenu(t, h)
	if h.ui.HasText("URL:") {
		t.Fatal("Add Remote dialog opened without a repository")
	}
}

// While a write is in flight the header menu items and the remote row's
// Fetch item disable and refuse the choice; once the write completes they
// work again.
func TestSidebarHeaderMenuDisabledWhileBusy(t *testing.T) {
	repo := setupSidebarRepo(t, "sec-menu-busy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// Hold a real checkout write behind the worker gate so the busy state is
	// deterministic.
	h.queue.PauseWorkers()
	doubleClick(t, h.ui, "feature")
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the checkout worker is held")
	}

	refuse := func(target, item string) {
		t.Helper()
		if err := h.ui.RightClick(target); err != nil {
			t.Fatalf("right-click %s while busy: %v", target, err)
		}
		h.ui.Frame()
		err := h.ui.ChooseMenuItem(item)
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("menu item %q while busy: err=%v, want a disabled refusal", item, err)
		}
		dismissMenu(t, h)
	}
	refuse("Branches", "New Branch…")
	refuse("Remotes", "Add New Remote…")
	refuse("origin", "Fetch 'origin'")

	h.queue.ReleaseWorker(write)
	h.queue.ResumeWorkers()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after the gated checkout finished")
	}
	if got := currentBranch(t, repo); got != "feature" {
		t.Fatalf("current branch after the write = %q, want feature", got)
	}
}

// The remote row menu leads with Fetch '<remote>' ahead of a separator and
// the manage entries, and choosing it fetches only that remote: the new
// branch of origin arrives, upstream's new branch does not, and a branch
// deleted in origin is pruned while upstream's tracking refs stay.
func TestRemoteFetchSingleRemoteOnly(t *testing.T) {
	repo := newTwoRemoteRepo(t, "fetch-single")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The menu order: Fetch first, a separator, then the manage entries.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	got := h.ui.Menu()
	wantPrefix := []string{"Fetch 'origin'", "-", "Edit 'origin'…", "Delete 'origin'…"}
	if len(got) < len(wantPrefix) {
		t.Fatalf("origin menu = %q, want at least the prefix %q", got, wantPrefix)
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("origin menu[%d] = %q, want %q (full menu %q)", i, got[i], want, got)
		}
	}
	dismissMenu(t, h)

	// origin gains a branch; upstream gains one too. Both mutations happen
	// inside the bare repositories, because pushing from the test repository
	// would opportunistically update its own remote-tracking refs and spoil
	// the negative assertion. Fetching origin must download only origin's.
	originURL := remoteURLOf(t, repo, "origin")
	upstreamURL := remoteURLOf(t, repo, "upstream")
	if originURL == "" || upstreamURL == "" {
		t.Fatal("fixture remotes have no URLs")
	}
	runGitCommand(t, originURL, "update-ref", "refs/heads/gain", "refs/heads/main")
	runGitCommand(t, upstreamURL, "update-ref", "refs/heads/gain", "refs/heads/main")
	chooseRemoteFetch(t, h, "origin")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	assertRefPresent(t, repo, "refs/remotes/origin/gain")
	assertRefGone(t, repo, "refs/remotes/upstream/gain")

	// A branch deleted in origin is pruned; upstream's tracking refs stay.
	runGitCommand(t, originURL, "update-ref", "refs/heads/stale", "refs/heads/main")
	chooseRemoteFetch(t, h, "origin")
	h.drain()
	assertRefPresent(t, repo, "refs/remotes/origin/stale")

	runGitCommand(t, originURL, "update-ref", "-d", "refs/heads/stale")
	chooseRemoteFetch(t, h, "origin")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected prune fetch error: %v", err)
	}
	assertRefGone(t, repo, "refs/remotes/origin/stale")
	assertRefPresent(t, repo, "refs/remotes/upstream/main")
}

// The Fetch '<remote>' worker honors the Fetch Tags preference captured at
// click time: the tag of the remote, pointing at a commit no branch of the
// remote reaches, is not downloaded while the preference is off and is
// downloaded once it is on.
func TestRemoteFetchTagsRespectSetting(t *testing.T) {
	repo := setupSidebarRepo(t, "fetch-tags")
	bareDir := remoteURLOf(t, repo, "origin")
	if bareDir == "" {
		t.Fatal("origin has no URL in the fixture")
	}

	// Push only a tag ref from a peer clone: the tag's commit is unreachable
	// from any branch of the bare remote, so downloading it takes --tags.
	peerDir := filepath.Join(t.TempDir(), "peer")
	runGitCommand(t, "", "clone", bareDir, peerDir)
	runGitCommand(t, peerDir, "config", "user.name", "Peer")
	runGitCommand(t, peerDir, "config", "user.email", "peer@branchline.local")
	runGitCommand(t, peerDir, "checkout", "-q", "-b", "scratch", "origin/main")
	if err := os.WriteFile(filepath.Join(peerDir, "tagonly.txt"), []byte("tagonly\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, peerDir, "add", "--", "tagonly.txt")
	runGitCommand(t, peerDir, "commit", "-q", "-m", "tagonly commit")
	runGitCommand(t, peerDir, "tag", "tagonly")
	runGitCommand(t, peerDir, "push", "-q", "origin", "refs/tags/tagonly")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if h.store.Settings().FetchTags {
		t.Fatal("test assumes Fetch Tags off by default in the isolated store")
	}
	assertTagGone(t, repo, "tagonly")

	// Fetch Tags off: the tag stays away.
	chooseRemoteFetch(t, h, "origin")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected fetch error with tags off: %v", err)
	}
	assertTagGone(t, repo, "tagonly")

	// Fetch Tags on: the same fetch downloads the tag.
	settings := h.store.Settings()
	settings.FetchTags = true
	if err := h.store.SetSettings(settings); err != nil {
		t.Fatalf("enable Fetch Tags: %v", err)
	}
	chooseRemoteFetch(t, h, "origin")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected fetch error with tags on: %v", err)
	}
	assertTagPresent(t, repo, "tagonly")
}

// The worker refuses what the repository no longer matches: a remote removed
// after the menu item was chosen is never fetched, and one whose URL was
// changed elsewhere is never fetched either — proven by the new branch of
// the other remote the URL was pointed at never arriving under origin.
func TestRemoteFetchGuardURLChanged(t *testing.T) {
	repo := newTwoRemoteRepo(t, "fetch-guard-url")
	upstreamURL := remoteURLOf(t, repo, "upstream")
	if upstreamURL == "" {
		t.Fatal("upstream has no URL in the fixture")
	}
	runGitCommand(t, upstreamURL, "update-ref", "refs/heads/gain", "refs/heads/main")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The menu captures the snapshot URL on the UI thread; changing the URL
	// externally afterwards — without any UI refresh — must refuse the fetch.
	repo.Git("remote", "set-url", "origin", upstreamURL)
	chooseRemoteFetch(t, h, "origin")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("stale fetch produced no error")
	}
	if !strings.Contains(err.Error(), "URL changed") {
		t.Fatalf("stale fetch error = %q, want the URL-changed refusal", err)
	}
	// Proof the fetch never ran: upstream's gain branch would have landed
	// under origin/gain without the guard, because the URL was pointed there.
	assertRefGone(t, repo, "refs/remotes/origin/gain")
	assertRefGone(t, repo, "refs/remotes/upstream/gain")
}

// A remote removed after the menu item was chosen is never fetched: the
// worker's re-read fails and the refusal shows instead of git's own error.
func TestRemoteFetchGuardRemoteRemoved(t *testing.T) {
	repo := newTwoRemoteRepo(t, "fetch-guard-removed")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	repo.Git("remote", "remove", "origin")
	chooseRemoteFetch(t, h, "origin")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("fetch of a removed remote produced no error")
	}
	if !strings.Contains(err.Error(), "reading its URL failed") {
		t.Fatalf("removed-remote fetch error = %q, want the read failure refusal", err)
	}
	if got := remoteList(t, repo); strings.Contains(got, "origin") {
		t.Fatalf("remotes after the refused fetch = %q, the remote was recreated", got)
	}
}

// A failing fetch of a credentialed remote never leaks the credentials: not
// into the session error and not into the toolbar.
func TestRemoteFetchCredentialsRedacted(t *testing.T) {
	repo := setupSidebarRepo(t, "fetch-redact")
	repo.Git("remote", "set-url", "origin", "https://fetchuser:fetchsecretpw@127.0.0.1:1/repo.git")

	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()

	chooseRemoteFetch(t, h, "origin")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("expected ActionErr after fetch against the unreachable host")
	} else if strings.Contains(err.Error(), "fetchsecretpw") {
		t.Fatalf("session error leaks the URL credential: %q", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error after the failed fetch; texts=%q", h.ui.Texts())
	}
	for _, text := range h.ui.Texts() {
		if strings.Contains(text, "fetchsecretpw") {
			t.Fatalf("toolbar leaks the URL credential: %q", text)
		}
	}
}

// Switching repositories mid-fetch drops the stale completion: the fetch
// itself still ran in the repository it was started for, but the new
// repository shows neither the error nor a busy state of the old one.
func TestRemoteFetchStaleResultDroppedOnRepoSwitch(t *testing.T) {
	repoA := newTwoRemoteRepo(t, "fetch-switch-a")
	originURL := remoteURLOf(t, repoA, "origin")
	runGitCommand(t, originURL, "update-ref", "refs/heads/gain", "refs/heads/main")
	assertRefGone(t, repoA, "refs/remotes/origin/gain")
	repoB := newGitRepo(t, "fetch-switch-b")
	repoB.CommitFile("b.txt", "b\n", "b commit")

	h := newTestApp(t, repoB.StoreRepo())
	closeSync(t, h)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	chooseRemoteFetch(t, h, "origin")
	fetchWorker := h.queue.WaitWorker(t)

	// Switch before releasing the worker: the completion arrives for a
	// repository that is no longer open. The switch itself starts the new
	// repository's load behind the same gate.
	h.openRepo(repoB.StoreRepo())
	loadWorker := h.queue.WaitWorker(t)
	h.queue.ReleaseWorker(fetchWorker)
	h.queue.ReleaseWorker(loadWorker)
	h.queue.ResumeWorkers()
	h.drain()

	if h.app.session.Busy() {
		t.Fatal("new repository busy after the old fetch completed")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("new repository shows the old fetch error: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("new repository did not load after the switch")
	}
	// The fetch itself ran for the old repository regardless.
	assertRefPresent(t, repoA, "refs/remotes/origin/gain")
}

// --- Loopback SSH ---

// On a loopback SSH server repository the remote row fetch downloads the
// remote's new branches and prunes the deleted ones, and behind a password
// gate it succeeds only because the fetch runs on the askpass-compatible
// path: the gate denies every connection that cannot answer its password
// question, so a fetch without the askpass channel never gets through.
func TestRemoteFetchLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bare)
	srv.RunGit(t, repoDir, "push", "-q", "-u", "origin", "main")
	srv.RunGit(t, repoDir, "push", "-q", "origin", "main:refs/heads/stale")
	srv.RunGit(t, repoDir, "fetch", "-q", "origin")

	// The server-side state for the fetch: stale gone from the bare remote,
	// gain new in it.
	srv.RunGit(t, bare, "update-ref", "-d", "refs/heads/stale")
	srv.RunGit(t, bare, "update-ref", "refs/heads/gain", "refs/heads/main")
	serverRefGone := func(ref string) {
		t.Helper()
		out := srv.RunShell(t, "cd "+shellQ(bare)+"; git rev-parse --verify --quiet "+shellQ(ref)+" >/dev/null 2>&1; echo rc=$?")
		if strings.TrimSpace(out) != "rc=1" {
			t.Fatalf("server %s still resolves: %q", ref, out)
		}
	}
	serverRefGone("refs/heads/stale")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	// Gate every new connection behind password authentication: from here on
	// only the askpass-enabled sync path can reach the server.
	const password = "hunter2t0ps3cret"
	gateDir := passwordGateWrapper(t, srv.SSHProgram(), password)
	t.Setenv("PATH", gateDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	answerPassword := func() {
		t.Helper()
		waitAskPass(t, h)
		if !h.ui.HasText("The remote host requires password") {
			t.Fatalf("password dialog title missing for the remote fetch; texts=%q", h.ui.Texts())
		}
		h.ui.Type(password)
		h.ui.Frame()
		if err := h.ui.Click("OK"); err != nil {
			t.Fatalf("click OK in password dialog: %v", err)
		}
		h.ui.Frame()
	}

	chooseRemoteFetch(t, h, "origin")
	h.ui.Frame()
	// The password gate stands in front of every ssh invocation, and the
	// fetch is two of them — the worker's URL verification and the fetch
	// itself — so the question is answered twice. A real server asks only
	// once here: the second invocation reuses the shared connection the
	// first authentication established.
	answerPassword()
	answerPassword()
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH remote fetch behind the password gate failed: %v", err)
	}
	// Server-side proof through the gate-independent probe executor: the new
	// branch arrived and the deleted one is pruned.
	if got := srv.RunGit(t, repoDir, "rev-parse", "origin/gain"); got == "" {
		t.Fatal("server origin/gain missing after the SSH remote fetch")
	}
	out := srv.RunShell(t, "cd "+shellQ(repoDir)+"; git rev-parse --verify --quiet refs/remotes/origin/stale >/dev/null 2>&1; echo rc=$?")
	if strings.TrimSpace(out) != "rc=1" {
		t.Fatalf("server refs/remotes/origin/stale not pruned: %q", out)
	}
}

// The Tags section header opens the New Tag dialog from its context menu in
// both the expanded and the collapsed state, and the dialog creates the tag
// on the default target exactly as clicking the New Tag sidebar row does.
func TestSidebarHeaderTagsNewTag(t *testing.T) {
	repo := setupSidebarRepo(t, "sec-menu-tags")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	createViaHeader := func(tag string) {
		t.Helper()
		if err := h.ui.RightClick("Tags"); err != nil {
			t.Fatalf("right-click Tags header: %v", err)
		}
		h.ui.Frame()
		wantMenu := []string{"New Tag…", "-", "Sort Tags:", "Alphabetically", "Alphabetically, folders on top", "Alphabetically backward", "Alphabetically backward, folders on top"}
		if menu := h.ui.Menu(); !slices.Equal(menu, wantMenu) {
			t.Fatalf("Tags header menu = %q, want exactly %q", menu, wantMenu)
		}
		if err := h.ui.ChooseMenuItem("New Tag…"); err != nil {
			t.Fatalf("choose New Tag…: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("Tag name") {
			t.Fatalf("New Tag dialog did not open; texts=%q", h.ui.Texts())
		}
		h.ui.Type(tag)
		h.ui.Frame()
		clickDialogButton(t, h, "Create Tag")
		h.drain()
		if err := h.app.session.ActionErr(); err != nil {
			t.Fatalf("unexpected create error for %s: %v", tag, err)
		}
		assertTagPresent(t, repo, tag)
		head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
		if got := gitOutTrim(t, repo.Path(), "rev-parse", "refs/tags/"+tag); got != head {
			t.Fatalf("tag %s = %q, want the default target HEAD %q", tag, got, head)
		}
	}

	// Collapsed first: the rows are hidden and the header menu still opens the
	// dialog and creates the tag.
	if err := h.ui.Click("Tags"); err != nil {
		t.Fatalf("collapse Tags: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.Find("v1.0"); ok {
		t.Fatal("tag row v1.0 stayed visible after collapsing Tags")
	}
	createViaHeader("sec-tags-collapsed")

	// Expanded: the same menu entry opens the dialog and creates the tag.
	if err := h.ui.Click("Tags"); err != nil {
		t.Fatalf("re-expand Tags: %v", err)
	}
	h.ui.Frame()
	createViaHeader("sec-tags-expanded")
	if _, ok := h.ui.Find("sec-tags-expanded"); !ok {
		t.Fatal("sec-tags-expanded missing from the sidebar after create")
	}
}

// With no repository open the Tags header menu item is disabled and choosing
// it opens nothing; while a write is in flight it is disabled the same way
// and works again once the write completes.
func TestSidebarHeaderTagsNewTagDisabledStates(t *testing.T) {
	// Without a repository.
	h := newTestApp(t)
	h.ui.Frame()
	if err := h.ui.RightClick("Tags"); err != nil {
		t.Fatalf("right-click Tags header without repo: %v", err)
	}
	h.ui.Frame()
	err := h.ui.ChooseMenuItem("New Tag…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("New Tag… without repo: err=%v, want a disabled refusal", err)
	}
	dismissMenu(t, h)
	if h.ui.HasText("Tag name") {
		t.Fatal("New Tag dialog opened without a repository")
	}

	// While a write is in flight: hold a real checkout behind the worker gate
	// so the busy state is deterministic.
	repo := setupSidebarRepo(t, "sec-menu-tags-busy")
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	doubleClick(t, h.ui, "feature")
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the checkout worker is held")
	}
	if err := h.ui.RightClick("Tags"); err != nil {
		t.Fatalf("right-click Tags header while busy: %v", err)
	}
	h.ui.Frame()
	err = h.ui.ChooseMenuItem("New Tag…")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("New Tag… while busy: err=%v, want a disabled refusal", err)
	}
	dismissMenu(t, h)
	if h.ui.HasText("Tag name") {
		t.Fatal("New Tag dialog opened while a write was in flight")
	}

	h.queue.ReleaseWorker(write)
	h.queue.ResumeWorkers()
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected checkout error: %v", err)
	}

	// The write finished: the menu entry works again.
	if err := h.ui.RightClick("Tags"); err != nil {
		t.Fatalf("right-click Tags header after the write: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("New Tag…"); err != nil {
		t.Fatalf("choose New Tag… after the write: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Tag name") {
		t.Fatalf("New Tag dialog did not open after the write finished; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel New Tag: %v", err)
	}
	h.ui.Frame()
}
