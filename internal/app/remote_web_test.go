package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// captureRemoteWebOpener injects an opener that only records the URLs asked
// to open, proving the tests never touch a real browser; it returns the
// slice the records append to.
func captureRemoteWebOpener(t *testing.T, h *testHarness) *[]string {
	t.Helper()
	opened := &[]string{}
	setRemoteWebOpener(h.app, func(target string) error {
		*opened = append(*opened, target)
		return nil
	})
	t.Cleanup(func() { setRemoteWebOpener(h.app, nil) })
	return opened
}

// hostedRepoFixture wires origin to a hosted fetch URL whose pushes land in
// a real local bare repository: git push uses the explicit push URL, which
// the insteadOf expansion of the fetch URL never rewrites, so the whole
// fixture works offline while menus and dialogs see the hosted URL.
func hostedRepoFixture(t *testing.T, name, fetchURL string) (*gitRepoFixture, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := newBareRemote(t, repo, "bare")
	repo.Git("remote", "add", "origin", bareDir)
	repo.Git("fetch", "-q", "origin")
	repo.Git("remote", "set-url", "origin", fetchURL)
	repo.Git("remote", "set-url", "--push", "origin", bareDir)
	return repo, bareDir
}

// openHostedRepo opens the fixture in a test app and drains the initial load.
func openHostedRepo(t *testing.T, repo *gitRepoFixture) *testHarness {
	t.Helper()
	h := newTestApp(t)
	closeSync(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	return h
}

// choosePushPRItem right-clicks the branch row and chooses Push and Create
// Pull Request, verifying the dialog opened.
func openPushPRDialog(t *testing.T, h *testHarness, branch string) {
	t.Helper()
	if err := h.ui.RightClick(branch); err != nil {
		t.Fatalf("right-click branch row %q: %v\ntexts=%q", branch, err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Push and Create Pull Request…"); err != nil {
		t.Fatalf("choose Push and Create Pull Request…: %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push and Create Pull Request"); !ok {
		t.Fatalf("Push and Create Pull Request dialog did not open; texts=%q", h.ui.Texts())
	}
}

// confirmPushPR clicks the dialog's confirm button.
func confirmPushPR(t *testing.T, h *testHarness) {
	t.Helper()
	clickDialogButton(t, h, "Confirm Push and Create Pull Request")
}

// Parsing recognizes every common remote URL shape of the four hosting
// services and builds the repository page out of host and path alone.
func TestRemoteWebParseHostedURLs(t *testing.T) {
	cases := []struct {
		name, rawurl, service, home string
	}{
		{"github https", "https://github.com/owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"github http", "http://github.com/owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"github enterprise", "https://github.corp.example/owner/repo.git", "GitHub", "https://github.corp.example/owner/repo"},
		{"github scp", "git@github.com:owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"github scp no .git", "git@github.com:owner/repo", "GitHub", "https://github.com/owner/repo"},
		{"github ssh", "ssh://git@github.com/owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"github ssh port", "ssh://git@github.com:2222/owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"github git protocol", "git://github.com/owner/repo.git", "GitHub", "https://github.com/owner/repo"},
		{"gitlab subgroup", "https://gitlab.com/group/sub/repo.git", "GitLab", "https://gitlab.com/group/sub/repo"},
		{"gitlab scp subgroup", "git@gitlab.com:group/sub/repo.git", "GitLab", "https://gitlab.com/group/sub/repo"},
		{"gitlab selfhosted", "https://gitlab.example.com/group/repo", "GitLab", "https://gitlab.example.com/group/repo"},
		{"bitbucket https", "https://bitbucket.org/owner/repo.git", "Bitbucket", "https://bitbucket.org/owner/repo"},
		{"bitbucket scp", "git@bitbucket.org:owner/repo.git", "Bitbucket", "https://bitbucket.org/owner/repo"},
		{"gitea ssh", "ssh://git@gitea.example.com/owner/repo.git", "Gitea", "https://gitea.example.com/owner/repo"},
		{"codeberg scp", "git@codeberg.org:owner/repo.git", "Gitea", "https://codeberg.org/owner/repo"},
		{"github scp path with at", "git@github.com:owner/repo@v2.git", "GitHub", "https://github.com/owner/repo@v2"},
		{"github https drops query", "https://github.com/owner/repo.git?token=secret#frag", "GitHub", "https://github.com/owner/repo"},
		{"host case insensitive", "https://GitHub.COM/owner/repo.git", "GitHub", "https://GitHub.COM/owner/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, ok := parseHostedRepo(tc.rawurl)
			if !ok {
				t.Fatalf("parseHostedRepo(%q) = false, want %q", tc.rawurl, tc.service)
			}
			if repo.service != tc.service {
				t.Fatalf("parseHostedRepo(%q).service = %q, want %q", tc.rawurl, repo.service, tc.service)
			}
			if got := repo.home(); got != tc.home {
				t.Fatalf("home of %q = %q, want %q", tc.rawurl, got, tc.home)
			}
		})
	}
}

// Input that is not a hosted remote URL — empty, local paths, file URLs,
// other schemes, a missing repository path or an unknown host — never
// parses, so the menu entries hide for it.
func TestRemoteWebParseRejectsNonHostedURLs(t *testing.T) {
	for _, rawurl := range []string{
		"",
		"   ",
		"/tmp/some/repo",
		"../relative/repo",
		"~/repo",
		"./repo",
		"file:///tmp/repo.git",
		"https://github.com/",
		"https://github.com",
		"git@github.com:",
		"https://example.com/a/b.git",
		"svn://github.com/owner/repo",
		"host-without-colon",
		"/tmp/foo@github.com:owner/repo",
		"./dir/git@github.com:owner/repo.git",
		"git@git hub.com:owner/repo.git",
	} {
		if repo, ok := parseHostedRepo(rawurl); ok {
			t.Errorf("parseHostedRepo(%q) = %+v, want rejected", rawurl, repo)
		}
	}
}

// Credentials in a remote URL never reach the web URL or anywhere else:
// they are dropped while parsing, whatever the scheme.
func TestRemoteWebParseStripsCredentials(t *testing.T) {
	for _, rawurl := range []string{
		"https://user:secret@github.com/owner/repo.git",
		"https://token@gitlab.com/group/repo.git",
		"ssh://user:secret@bitbucket.org/owner/repo.git",
		"git@github.com:owner/repo.git",
	} {
		repo, ok := parseHostedRepo(rawurl)
		if !ok {
			t.Fatalf("parseHostedRepo(%q) = false, want parsed", rawurl)
		}
		for _, page := range []string{repo.home(), repo.branchPage("b"), repo.pullRequestPage("b")} {
			if strings.Contains(page, "user") || strings.Contains(page, "secret") || strings.Contains(page, "token") || strings.Contains(page, "@") {
				t.Fatalf("page %q of %q carries credentials or userinfo", page, rawurl)
			}
		}
	}
}

// Branch and pull request pages follow the service's URL shape, keep the
// slashes of a branch name in path segments and escape what URLs cannot
// carry literally.
func TestRemoteWebBranchAndPullRequestPages(t *testing.T) {
	services := []struct {
		rawurl, service, branch, pr string
	}{
		{"https://github.com/o/r.git", "GitHub",
			"https://github.com/o/r/tree/feature/login",
			"https://github.com/o/r/pull/new/feature/login"},
		{"https://gitlab.com/g/s/r.git", "GitLab",
			"https://gitlab.com/g/s/r/-/tree/feature/login",
			"https://gitlab.com/g/s/r/-/merge_requests/new?merge_request%5Bsource_branch%5D=feature%2Flogin"},
		{"https://bitbucket.org/o/r.git", "Bitbucket",
			"https://bitbucket.org/o/r/src/feature/login",
			"https://bitbucket.org/o/r/pull-requests/new?source=feature%2Flogin"},
		{"https://gitea.example.com/o/r.git", "Gitea",
			"https://gitea.example.com/o/r/src/branch/feature/login",
			"https://gitea.example.com/o/r/compare/feature/login"},
	}
	for _, tc := range services {
		t.Run(tc.service, func(t *testing.T) {
			repo, ok := parseHostedRepo(tc.rawurl)
			if !ok {
				t.Fatalf("parseHostedRepo(%q) = false", tc.rawurl)
			}
			if got := repo.branchPage("feature/login"); got != tc.branch {
				t.Errorf("branchPage = %q, want %q", got, tc.branch)
			}
			if got := repo.pullRequestPage("feature/login"); got != tc.pr {
				t.Errorf("pullRequestPage = %q, want %q", got, tc.pr)
			}
		})
	}

	// Characters a URL path cannot carry are percent-encoded.
	repo, ok := parseHostedRepo("https://github.com/o/r.git")
	if !ok {
		t.Fatal("github URL did not parse")
	}
	if got := repo.branchPage("a b%"); !strings.Contains(got, "a%20b%25") {
		t.Errorf("branchPage with space and percent = %q, want percent-encoded", got)
	}
	if got := repo.branchPage("q?x#y"); !strings.Contains(got, "q%3Fx%23y") {
		t.Errorf("branchPage with ? and # = %q, want percent-encoded", got)
	}
}

// A URL rewritten through url.insteadOf reaches the sidebar already
// expanded by git remote get-url: the remote row menu of such a repository
// offers View on 'GitHub' opening the expanded repository page.
func TestRemoteWebInsteadOfExpandsThroughSnapshot(t *testing.T) {
	repo := newGitRepo(t, "web-insteadof")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := newBareRemote(t, repo, "bare")
	repo.Git("remote", "add", "origin", "gh:demo.git")
	repo.Git("config", "url.https://github.com/example/.insteadOf", "gh:")
	repo.Git("remote", "set-url", "--push", "origin", bareDir)

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	if got := h.app.session.Snapshot().RemoteURLs["origin"]; got != "https://github.com/example/demo.git" {
		t.Fatalf("snapshot URL = %q, want the insteadOf expansion", got)
	}
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !slices.Equal(*opened, []string{"https://github.com/example/demo"}) {
		t.Fatalf("opened = %q, want the expanded repository page", *opened)
	}
}

// View on '<service>' of a remote row opens the repository's main page, is
// unaffected by repository writes being busy, and hides for a remote whose
// URL no known service hosts.
func TestRemoteWebViewOnRemoteRow(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-view-remote", "https://github.com/example/demo.git")
	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !slices.Equal(*opened, []string{"https://github.com/example/demo"}) {
		t.Fatalf("opened = %q, want the repository page", *opened)
	}

	// The same menu item hides entirely when the URL is not hosted: repoint
	// origin at the bare repository path and reload through a write-free
	// switch reopen.
	repo.Git("remote", "set-url", "origin", repo.Path()+"/../bare.git")
	h.app.openRepo(repo.StoreRepo())
	h.ui.Frame()
	h.drain()
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin with local URL: %v", err)
	}
	h.ui.Frame()
	for _, item := range h.ui.Menu() {
		if strings.HasPrefix(item, "View on ") {
			t.Fatalf("menu %q offers View on for a local remote URL", h.ui.Menu())
		}
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// View on '<service>' of a remote branch row opens the branch's page with
// the slashes of its name intact, the remote prefix removed.
func TestRemoteWebViewOnRemoteBranchRow(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-view-branch", "https://gitlab.com/example/demo.git")
	repo.Git("branch", "feature/login")
	repo.Git("push", "-q", "origin", "feature/login")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	if _, ok := h.ui.Find("origin/feature/login"); !ok {
		t.Fatalf("origin/feature/login missing from the sidebar; texts=%q", h.ui.Texts())
	}
	if err := h.ui.RightClick("origin/feature/login"); err != nil {
		t.Fatalf("right-click remote branch row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitLab'"); err != nil {
		t.Fatalf("choose View on 'GitLab': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	want := "https://gitlab.com/example/demo/-/tree/feature/login"
	if !slices.Equal(*opened, []string{want}) {
		t.Fatalf("opened = %q, want %q", *opened, want)
	}
}

// A local branch tracking a hosted remote branch offers Create Pull Request
// on '<service>', opening the service's new pull request page for the
// upstream branch; it stays clickable while a write is in flight.
func TestRemoteWebCreatePROnBranchWithUpstream(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-create-pr", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "--set-upstream-to", "origin/feature", "feature")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Create Pull Request on 'GitHub'"); err != nil {
		t.Fatalf("choose Create Pull Request on 'GitHub': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	want := "https://github.com/example/demo/pull/new/feature"
	if !slices.Equal(*opened, []string{want}) {
		t.Fatalf("opened = %q, want %q", *opened, want)
	}
	if len(h.ui.OpenedURLs()) != 0 {
		t.Fatalf("the injected opener must be the only opener; tester opened %q", h.ui.OpenedURLs())
	}
}

// Without the injected opener the same click goes through the production
// path: the frame's ui.Context opens it, which the headless tester records
// without any browser.
func TestRemoteWebCreatePRDefaultOpenerThroughUI(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-create-pr-default", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "--set-upstream-to", "origin/feature", "feature")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Create Pull Request on 'GitHub'"); err != nil {
		t.Fatalf("choose Create Pull Request on 'GitHub': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	want := "https://github.com/example/demo/pull/new/feature"
	if got := h.ui.OpenedURLs(); !slices.Equal(got, []string{want}) {
		t.Fatalf("OpenedURLs = %q, want %q", got, want)
	}
}

// A failing open shows the error in the UI instead of swallowing it.
func TestRemoteWebOpenFailureShowsError(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-open-fail", "https://github.com/example/demo.git")
	h := openHostedRepo(t, repo)
	setRemoteWebOpener(h.app, func(target string) error {
		return errors.New("no application can open the URL")
	})
	t.Cleanup(func() { setRemoteWebOpener(h.app, nil) })

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub': %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("no application can open the URL") {
		t.Fatalf("open failure not visible in the UI; texts=%q", h.ui.Texts())
	}
}

// Push and Create Pull Request appears exactly for a branch without a valid
// upstream when a hosted remote exists: hidden for one with a valid hosted
// upstream, hidden when nothing hosted exists, and never for a repository
// without remotes.
func TestRemoteWebMenuVisibility(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-visibility", "https://github.com/example/demo.git")
	// main tracks origin/main, but through the local bare URL the fixture
	// pushed with; set the upstream the same way the feature branch gets it.
	repo.Git("branch", "--set-upstream-to", "origin/main", "main")
	repo.CheckoutNewBranch("loose")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)

	// Loose branch without upstream: the push entry shows.
	if err := h.ui.RightClick("loose"); err != nil {
		t.Fatalf("right-click loose: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Push and Create Pull Request…") {
		t.Fatalf("menu %q lacks Push and Create Pull Request… for a branch without upstream", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	// main tracks a hosted branch: Create Pull Request instead of pushing.
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main: %v", err)
	}
	h.ui.Frame()
	menu = h.ui.Menu()
	if !slices.Contains(menu, "Create Pull Request on 'GitHub'") {
		t.Fatalf("menu %q lacks Create Pull Request on 'GitHub' for a tracked branch", menu)
	}
	if slices.Contains(menu, "Push and Create Pull Request…") {
		t.Fatalf("menu %q still offers Push and Create Pull Request… for a tracked branch", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	// With no hosted remote at all neither entry shows: repoint origin at a
	// local path and reopen.
	repo.Git("remote", "set-url", "origin", repo.Path()+"/../bare.git")
	h.app.openRepo(repo.StoreRepo())
	h.ui.Frame()
	h.drain()
	if err := h.ui.RightClick("loose"); err != nil {
		t.Fatalf("right-click loose after repointing: %v", err)
	}
	h.ui.Frame()
	menu = h.ui.Menu()
	if slices.Contains(menu, "Push and Create Pull Request…") {
		t.Fatalf("menu %q offers Push and Create Pull Request… without a hosted remote", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click origin/main after repointing: %v", err)
	}
	h.ui.Frame()
	for _, item := range h.ui.Menu() {
		if strings.HasPrefix(item, "View on ") {
			t.Fatalf("remote branch menu %q offers View on for a local URL", h.ui.Menu())
		}
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// A branch whose upstream is gone counts as having no valid upstream: it
// offers Push and Create Pull Request again.
func TestRemoteWebGoneUpstreamOffersPushPR(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-gone-upstream", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Git("push", "-q", "origin", "feature")
	repo.Git("branch", "--set-upstream-to", "origin/feature", "feature")
	repo.Checkout("main")
	// The upstream goes away on the server; the local tracking ref still
	// knows it was origin/feature until a fetch prunes it, which never runs
	// here, so remove the ref itself to mark the upstream gone.
	repo.Git("update-ref", "-d", "refs/remotes/origin/feature")

	h := openHostedRepo(t, repo)
	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Push and Create Pull Request…") {
		t.Fatalf("menu %q lacks Push and Create Pull Request… for a gone upstream", menu)
	}
	if slices.Contains(menu, "Create Pull Request on 'GitHub'") {
		t.Fatalf("menu %q still offers Create Pull Request for a gone upstream", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// The Push and Create Pull Request dialog names the branch and the service,
// centers horizontally in a 1200 wide window, and its buttons stay inside
// it.
func TestRemoteWebPushPRDialogLayout(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-dialog-layout", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	openPushPRDialog(t, h, "feature")
	if !h.ui.HasText("Push the branch") && !h.ui.HasText("sets it as the branch's upstream") {
		t.Fatalf("dialog explanation missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("origin (GitHub)") {
		t.Fatalf("single remote not shown read-only with its service; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Confirm Push and Create Pull Request")
	clickDialogButton(t, h, "Cancel")
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Push and Create Pull Request"); ok {
		t.Fatal("dialog stayed open after Cancel")
	}
}

// Confirming Push and Create Pull Request pushes the branch to the bare
// repository for real, makes it the branch's upstream, and opens the pull
// request page of the pushed branch.
func TestRemoteWebPushPRPushesAndOpens(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-push-pr", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	confirmPushPR(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	wantHash := strings.TrimSpace(repo.Git("rev-parse", "feature"))
	if got := strings.TrimSpace(runGitCommand(t, "", "--git-dir", bareDir, "rev-parse", "refs/heads/feature")); got != wantHash {
		t.Fatalf("bare feature = %q, want %q", got, wantHash)
	}
	if got := upstreamOf(t, repo, "feature"); got != "origin/feature" {
		t.Fatalf("feature upstream = %q, want origin/feature", got)
	}
	want := "https://github.com/example/demo/pull/new/feature"
	if !slices.Equal(*opened, []string{want}) {
		t.Fatalf("opened = %q, want %q", *opened, want)
	}
}

// With several hosted remotes the dialog offers the choice, prefers origin,
// and a different choice pushes to that remote and opens its service's page.
func TestRemoteWebPushPRMultipleRemotesSelect(t *testing.T) {
	repo, originBare := hostedRepoFixture(t, "web-push-multi", "https://github.com/example/demo.git")
	upstreamBare := newBareRemote(t, repo, "upstream-bare")
	repo.Git("remote", "add", "upstream", upstreamBare)
	repo.Git("remote", "set-url", "upstream", "https://gitlab.com/example/demo.git")
	repo.Git("remote", "set-url", "--push", "upstream", upstreamBare)
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	// origin exists and is hosted, so it is the default choice.
	trigger, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Push PR Remote")
	if !ok {
		t.Fatalf("no Push PR Remote select; texts=%q", h.ui.Texts())
	}
	h.ui.ClickAt(trigger.Bounds.X+trigger.Bounds.W/2, trigger.Bounds.Y+trigger.Bounds.H/2)
	h.ui.Frame()
	if err := h.ui.Click("upstream (GitLab)"); err != nil {
		t.Fatalf("choose upstream (GitLab) in the select popup: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()

	confirmPushPR(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	wantHash := strings.TrimSpace(repo.Git("rev-parse", "feature"))
	if got := strings.TrimSpace(runGitCommand(t, "", "--git-dir", upstreamBare, "rev-parse", "refs/heads/feature")); got != wantHash {
		t.Fatalf("upstream bare feature = %q, want %q", got, wantHash)
	}
	if _, err := tryGitCommand("", "--git-dir", originBare, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); err == nil {
		t.Fatal("feature landed on origin although upstream was chosen")
	}
	if got := upstreamOf(t, repo, "feature"); got != "upstream/feature" {
		t.Fatalf("feature upstream = %q, want upstream/feature", got)
	}
	want := "https://gitlab.com/example/demo/-/merge_requests/new?merge_request%5Bsource_branch%5D=feature"
	if !slices.Equal(*opened, []string{want}) {
		t.Fatalf("opened = %q, want %q", *opened, want)
	}
}

// A push the remote rejects — the same branch already there with other
// commits — shows git's error scrubbed and opens no browser.
func TestRemoteWebPushPRNonFastForwardShowsError(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-push-nonff", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	// The server gains its own feature commit the local branch cannot
	// fast-forward to.
	clone := newGitRepo(t, "web-push-nonff-other")
	clone.Git("remote", "add", "origin", bareDir)
	clone.Git("fetch", "-q", "origin")
	clone.Git("checkout", "-q", "-b", "feature", "origin/main")
	clone.CommitFile("g.txt", "other\n", "other work")
	clone.Git("push", "-q", "origin", "feature")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("the non-fast-forward push produced no error")
	}
	if !strings.Contains(err.Error(), "failed to push some refs") || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("error %q is not git's non-fast-forward refusal", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a failed push", *opened)
	}
	if got := upstreamOf(t, repo, "feature"); got != "" {
		t.Fatalf("feature upstream = %q, want none after the refused push", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after the failed push; texts=%q", h.ui.Texts())
	}
}

// The entry disables while a write is in flight, while the read-only View on
// and Create Pull Request entries stay clickable.
func TestRemoteWebPushPRDisabledWhileBusy(t *testing.T) {
	repo, _ := hostedRepoFixture(t, "web-push-busy", "https://github.com/example/demo.git")
	repo.Git("branch", "--set-upstream-to", "origin/main", "main")
	repo.CheckoutNewBranch("feature")
	repo.Checkout("main")
	repo.WriteFile("pending.txt", "pending\n")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)
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

	if err := h.ui.RightClick("feature"); err != nil {
		t.Fatalf("right-click feature while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Push and Create Pull Request…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Push and Create Pull Request… while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub' on remote while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick("origin/main"); err != nil {
		t.Fatalf("right-click origin/main while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub' on remote branch while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick("main"); err != nil {
		t.Fatalf("right-click main while busy: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Create Pull Request on 'GitHub'"); err != nil {
		t.Fatalf("choose Create Pull Request on 'GitHub' while busy: %v", err)
	}
	h.ui.Frame()

	wantOpened := []string{
		"https://github.com/example/demo",
		"https://github.com/example/demo/tree/main",
		"https://github.com/example/demo/pull/new/main",
	}
	if !slices.Equal(*opened, wantOpened) {
		t.Fatalf("opened while busy = %q, want %q", *opened, wantOpened)
	}

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()
}

// A branch that moved after the dialog opened is never pushed: the worker
// refuses, the repository keeps its state and no browser opens.
func TestRemoteWebPushPRGuardBranchMoved(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-guard-moved", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	// An outside actor moves the branch between the dialog opening and the
	// confirmation.
	repo.Checkout("feature")
	repo.CommitFile("moved.txt", "moved\n", "outside move")
	repo.Checkout("main")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "moved after the dialog opened") {
		t.Fatalf("ActionErr = %v, want the moved-branch refusal", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a refused push", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the moved branch was pushed to the bare repository")
	}
	if got := upstreamOf(t, repo, "feature"); got != "" {
		t.Fatalf("feature upstream = %q, want none", got)
	}
}

// A branch deleted after the dialog opened is never pushed either.
func TestRemoteWebPushPRGuardBranchDeleted(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-guard-deleted", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	repo.Git("branch", "-D", "feature")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("ActionErr = %v, want the deleted-branch refusal", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a refused push", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the deleted branch was pushed to the bare repository")
	}
}

// A remote whose URL changed after the dialog opened is never pushed to.
func TestRemoteWebPushPRGuardRemoteURLChanged(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-guard-url", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	repo.Git("remote", "set-url", "origin", "https://github.com/example/other.git")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "changed after the dialog opened") {
		t.Fatalf("ActionErr = %v, want the changed-URL refusal", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error %q leaks credentials", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a refused push", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the branch was pushed although the remote URL changed")
	}
	if got := remoteURLOf(t, repo, "origin"); got != "https://github.com/example/other.git" {
		t.Fatalf("the guard rewrote the remote URL to %q", got)
	}
}

// A remote removed after the dialog opened is never pushed to, and the
// repository configuration stays as the outside actor left it.
func TestRemoteWebPushPRGuardRemoteRemoved(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-guard-removed", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	repo.Git("remote", "remove", "origin")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("ActionErr = %v, want the removed-remote refusal", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a refused push", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the branch was pushed although the remote is gone")
	}
	if out := remoteList(t, repo); strings.Contains(out, "origin") {
		t.Fatalf("remotes = %q, want origin still removed", out)
	}
}

// Canceling the dialog pushes nothing, opens nothing, and clears the draft.
func TestRemoteWebPushPRCancel(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-push-cancel", "https://github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	clickDialogButton(t, h, "Cancel")
	h.drain()

	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a canceled dialog", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the branch was pushed for a canceled dialog")
	}
	if st := remoteWebUI(h.app); st.pushOpen || st.pushBranch != "" || st.pushExpectHash != "" || len(st.pushRemotes) != 0 || st.pushSelText != "" {
		t.Fatalf("dialog draft survived cancel: %+v", st)
	}
}

// Guard refusals and URL-open errors scrub credentials before showing them,
// and a URL-open failure stays visible even when an earlier action error was
// on the toolbar.
func TestRemoteWebErrorsRedactCredentialsAndSurfaceOverActionErr(t *testing.T) {
	repo, bareDir := hostedRepoFixture(t, "web-redact-err", "https://user:secret@github.com/example/demo.git")
	repo.CheckoutNewBranch("feature")
	repo.CommitFile("f.txt", "feature\n", "feature work")
	repo.Checkout("main")

	h := openHostedRepo(t, repo)
	opened := captureRemoteWebOpener(t, h)

	openPushPRDialog(t, h, "feature")
	repo.Git("remote", "set-url", "origin", "https://github.com/example/other.git")
	confirmPushPR(t, h)
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "changed after the dialog opened") {
		t.Fatalf("ActionErr = %v, want changed-URL refusal", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("changed-URL error %q leaks credentials", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a refused push", *opened)
	}
	if _, e := tryGitCommand("", "--git-dir", bareDir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); e == nil {
		t.Fatal("the branch was pushed although the credentialed remote URL changed")
	}

	// With that ActionErr still on the toolbar, an open-URL error replaces
	// it cleanly and scrubs credentials from the message.
	setRemoteWebOpener(h.app, func(target string) error {
		return errors.New("cannot open https://user:secret@github.com/example/other")
	})
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub': %v", err)
	}
	h.ui.Frame()
	if h.app.session.ActionErr() != nil {
		t.Fatalf("stale ActionErr %v was not cleared by open-URL failure", h.app.session.ActionErr())
	}
	if !h.ui.HasText("cannot open") {
		t.Fatalf("open-URL failure hidden by prior ActionErr; texts=%q", h.ui.Texts())
	}
	for _, txt := range h.ui.Texts() {
		if strings.Contains(txt, "secret") || strings.Contains(txt, "user:") {
			t.Fatalf("toolbar text %q leaks credentials", txt)
		}
	}
}

// Switching repositories closes the dialog, drops its draft, and a push
// completing late for the old repository never opens its page.
func TestRemoteWebPushPRRepoSwitchDropsEverything(t *testing.T) {
	repoA, _ := hostedRepoFixture(t, "web-switch-a", "https://github.com/example/demo.git")
	repoA.CheckoutNewBranch("feature")
	repoA.CommitFile("f.txt", "feature\n", "feature work")
	repoA.Checkout("main")
	repoB := newGitRepo(t, "web-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	closeSync(t, h)
	opened := captureRemoteWebOpener(t, h)
	h.openRepo(repoA.StoreRepo())
	h.drain()

	openPushPRDialog(t, h, "feature")

	// The dialog closes on the switch with its draft gone.
	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()
	st := remoteWebUI(h.app)
	if st.pushOpen {
		t.Fatal("dialog still open after the repository switch")
	}
	if st.pushBranch != "" || st.pushExpectHash != "" || len(st.pushRemotes) != 0 {
		t.Fatalf("dialog draft survived the switch: %+v", st)
	}

	// A push confirmed on the old repository completes after the switch:
	// its page never opens.
	h.app.openRepo(repoA.StoreRepo())
	h.ui.Frame()
	h.drain()
	openPushPRDialog(t, h, "feature")
	h.queue.PauseWorkers()
	confirmPushPR(t, h)
	pushWorker := h.queue.WaitWorker(t)
	h.ui.Frame()

	// The push worker is the only worker held: the switch itself must spawn
	// its reload ungated, so new workers resume before the old repository is
	// left behind, and the held push releases before draining — a drain
	// waits for every active worker, including a gated one.
	h.queue.ResumeWorkers()
	h.app.openRepo(saved)
	h.ui.Frame()
	h.queue.ReleaseWorker(pushWorker)
	h.drain()

	if len(*opened) != 0 {
		t.Fatalf("browser opened %q for a push of the previous repository", *opened)
	}
	if st := remoteWebUI(h.app); st.pendingURL != "" {
		t.Fatalf("a URL of the previous repository waits to open: %q", st.pendingURL)
	}
}

// On a loopback SSH repository the same entries work end to end: View on
// opens the insteadOf-expanded page, and Push and Create Pull Request pushes
// the branch for real over SSH and opens the pull request page.
func TestRemoteWebLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// The remote's fetch URL is the hosted address through an insteadOf
	// shorthand; pushes go to a bare repository on the server.
	bare := srv.TempDir(t, "bare")
	srv.RunShell(t, "git init --bare "+bare+" && cd "+bare+" && git symbolic-ref HEAD refs/heads/main")
	srv.RunGit(t, repo.Path, "config", "url.https://github.com/example/.insteadOf", "gh:")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", "gh:demo.git")
	srv.RunGit(t, repo.Path, "remote", "set-url", "--push", "origin", bare)
	srv.RunGit(t, repo.Path, "branch", "feature")

	h := newTestApp(t)
	closeSync(t, h)
	opened := captureRemoteWebOpener(t, h)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if got := h.app.session.Snapshot().RemoteURLs["origin"]; got != "https://github.com/example/demo.git" {
		t.Fatalf("snapshot URL = %q, want the insteadOf expansion", got)
	}

	// View on 'GitHub' of the remote row opens the expanded repository page.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("View on 'GitHub'"); err != nil {
		t.Fatalf("choose View on 'GitHub': %v\nmenu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	if !slices.Equal(*opened, []string{"https://github.com/example/demo"}) {
		t.Fatalf("opened = %q, want the repository page", *opened)
	}
	*opened = nil

	// Push and Create Pull Request pushes the branch over SSH for real.
	openPushPRDialog(t, h, "feature")
	confirmPushPR(t, h)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH push failed: %v", err)
	}
	wantHash := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "feature"))
	if got := strings.TrimSpace(srv.RunGit(t, bare, "rev-parse", "refs/heads/feature")); got != wantHash {
		t.Fatalf("server bare feature = %q, want %q", got, wantHash)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "feature@{upstream}")); got != "origin/feature" {
		t.Fatalf("feature upstream = %q, want origin/feature", got)
	}
	want := "https://github.com/example/demo/pull/new/feature"
	if !slices.Equal(*opened, []string{want}) {
		t.Fatalf("opened = %q, want %q", *opened, want)
	}
}
