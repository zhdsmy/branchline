package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// clickDialogButton presses the dialog button whose accessible name is label
// by its bounds, as clicking its text does, but without matching a dialog
// title of the same text first.
func clickDialogButton(t *testing.T, h *testHarness, label string) {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
	if !ok {
		t.Fatalf("no dialog button named %q; texts=%q", label, h.ui.Texts())
	}
	if btn.Disabled {
		t.Fatalf("dialog button %q is disabled; texts=%q", label, h.ui.Texts())
	}
	h.ui.ClickAt(btn.Bounds.X+btn.Bounds.W/2, btn.Bounds.Y+btn.Bounds.H/2)
	h.ui.Frame()
}

// dialogButton returns the accessible state of the button label, failing
// when no such button is rendered.
func dialogButton(t *testing.T, h *testHarness, label string) (accessInfo, bool) {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, label)
	if !ok {
		t.Fatalf("no button named %q; texts=%q", label, h.ui.Texts())
		return accessInfo{}, false
	}
	return accessInfo{Selected: btn.Selected, Disabled: btn.Disabled, Busy: btn.Busy}, true
}

// newBareRemote builds a bare repository holding the fixture's main branch,
// without wiring it into the fixture as a remote.
func newBareRemote(t *testing.T, repo *gitRepoFixture, name string) string {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), name+".git")
	runGitCommand(t, "", "init", "--bare", bareDir)
	repo.Git("push", "-q", bareDir, "main")
	return bareDir
}

// remoteList answers the remote names of the fixture repository.
func remoteList(t *testing.T, repo *gitRepoFixture) string {
	t.Helper()
	return strings.TrimSpace(repo.Git("remote"))
}

// remoteURLOf reads a remote's fetch URL through the Git CLI, or "" when the
// remote is gone.
func remoteURLOf(t *testing.T, repo *gitRepoFixture, name string) string {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "remote", "get-url", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// upstreamOf reads the upstream of a local branch, or "" when it has none.
func upstreamOf(t *testing.T, repo *gitRepoFixture, branch string) string {
	t.Helper()
	out, err := tryGitCommand(repo.Path(), "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// typeField focuses the text field named label as a user does and types into
// it, replacing what it holds when replace is set.
func typeField(t *testing.T, h *testHarness, label, text string, replace bool) {
	t.Helper()
	focusField(t, h, label)
	if replace {
		clearInput(t, h.ui)
	}
	if text != "" {
		h.ui.Type(text)
	}
	h.ui.Frame()
}

// The Remotes section opens with the New Remote row and the remote rows are
// focusable labeled entries whose context menu offers Edit, Delete and New
// Remote. With no repository open the entry hides entirely.
func TestRemoteManageSidebarEntries(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-entries")

	h := newTestApp(t)
	if _, ok := h.ui.Find("New Remote"); ok {
		t.Fatal("New Remote visible with no repository open")
	}

	h.openRepo(repo.StoreRepo())
	h.drain()

	if _, ok := h.ui.Find("New Remote"); !ok {
		t.Fatalf("New Remote entry missing; texts=%q", h.ui.Texts())
	}
	row, ok := h.ui.AccessByRole(ui.RoleListItem, "origin")
	if !ok {
		t.Fatalf("no accessible remote row labeled origin; texts=%q", h.ui.Texts())
	}
	if row.Selected {
		t.Fatal("remote row exposes a selected state it does not have")
	}

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click remote row: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
		t.Fatalf("choose Edit menu item: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Edit Remote") {
		t.Fatalf("Edit menu item did not open the dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Edit Remote: %v", err)
	}
	h.ui.Frame()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click remote row again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete menu item: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Delete Remote") {
		t.Fatalf("Delete menu item did not open the dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Delete Remote: %v", err)
	}
	h.ui.Frame()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click remote row a third time: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("New Remote…"); err != nil {
		t.Fatalf("choose New Remote menu item: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Add Remote") {
		t.Fatalf("New Remote menu item did not open the dialog; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Add Remote")
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Add Remote: %v", err)
	}
	h.ui.Frame()
}

// The Add Remote dialog validates the inputs live — an empty or illegal
// name, a collision with an existing remote and an empty URL all disable
// Add Remote — and a valid submission adds the remote.
func TestRemoteManageAddDialogValidates(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-add-valid")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	assertDialogLayout(t, h, "Cancel", "Add Remote")

	// Empty name: submit disabled.
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with empty name = %+v, want disabled", btn)
	}

	// Illegal characters disable submit and show the problem.
	h.ui.Type("bad..name")
	h.ui.Frame()
	if !h.ui.HasText("Must not contain '..'.") {
		t.Fatalf("illegal name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with illegal name = %+v, want disabled", btn)
	}

	// A name starting with "-" is rejected: git would read it as options.
	clearInput(t, h.ui)
	h.ui.Type("-x")
	h.ui.Frame()
	if !h.ui.HasText("cannot begin with '-'") {
		t.Fatalf("dash-name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with dash name = %+v, want disabled", btn)
	}

	// Collision with an existing remote disables submit.
	clearInput(t, h.ui)
	h.ui.Type("origin")
	h.ui.Frame()
	if !h.ui.HasText("already exists") {
		t.Fatalf("duplicate name problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with duplicate name = %+v, want disabled", btn)
	}

	// A valid name with the URL still empty disables submit.
	clearInput(t, h.ui)
	h.ui.Type("upstream")
	h.ui.Frame()
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with empty URL = %+v, want disabled", btn)
	}

	// A URL holding whitespace is rejected: it would split into arguments.
	typeField(t, h, "URL", "has space.git", false)
	if !h.ui.HasText("cannot contain whitespace") {
		t.Fatalf("whitespace URL problem not shown; texts=%q", h.ui.Texts())
	}
	if btn, _ := dialogButton(t, h, "Add Remote"); !btn.Disabled {
		t.Fatalf("Add Remote with whitespace URL = %+v, want disabled", btn)
	}

	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Add Remote: %v", err)
	}
	h.ui.Frame()
}

// Adding a remote with Fetch after adding ticked wires the URL into the
// repository configuration and downloads the remote branches into the
// sidebar.
func TestRemoteManageAddWithFetch(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-add-fetch")
	bareDir := newBareRemote(t, repo, "upstream")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("upstream")
	typeField(t, h, "URL", bareDir, false)
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if got := remoteURLOf(t, repo, "upstream"); got != bareDir {
		t.Fatalf("git remote get-url upstream = %q, want %q", got, bareDir)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected add error: %v", err)
	}
	if h.ui.HasText("Remote URL") {
		t.Fatal("Add Remote dialog stayed open after a successful add")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "upstream"); !ok {
		t.Fatalf("upstream missing from the sidebar after add; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("upstream/main"); !ok {
		t.Fatalf("upstream/main missing after fetch; texts=%q", h.ui.Texts())
	}
}

// Adding a remote without fetching still wires it but downloads nothing:
// the remote is in the sidebar without any remote-tracking branches.
func TestRemoteManageAddWithoutFetch(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-add-nofetch")
	bareDir := newBareRemote(t, repo, "upstream")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("upstream")
	typeField(t, h, "URL", bareDir, false)
	if err := h.ui.Click("Fetch after adding"); err != nil {
		t.Fatalf("untick Fetch after adding: %v", err)
	}
	h.ui.Frame()
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if got := remoteURLOf(t, repo, "upstream"); got != bareDir {
		t.Fatalf("git remote get-url upstream = %q, want %q", got, bareDir)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected add error: %v", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "upstream"); !ok {
		t.Fatalf("upstream missing from the sidebar after add; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("upstream/main"); ok {
		t.Fatal("upstream/main appeared even though fetch was unticked")
	}
}

// A failing fetch does not undo the add: the remote stays wired in the
// configuration and the error shows verbatim in the UI.
func TestRemoteManageAddFetchFailureKeepsRemote(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-add-failfetch")
	// The URL is a real directory inside the test fixture that is not a Git
	// repository, so the fetch fails offline with git's own message.
	notARepo := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatalf("mkdir not-a-repo: %v", err)
	}

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("upstream")
	typeField(t, h, "URL", notARepo, false)
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if got := remoteURLOf(t, repo, "upstream"); got != notARepo {
		t.Fatalf("git remote get-url upstream = %q, want %q: a failed fetch must not undo the add", got, notARepo)
	}
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("failing fetch produced no ActionErr")
	} else if !strings.Contains(err.Error(), "does not appear to be a git repository") {
		t.Fatalf("ActionErr = %q, want git's fetch failure verbatim", err)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error status after failing fetch; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("does not appear to be a git repository") {
		t.Fatalf("fetch failure not visible in the UI; texts=%q", h.ui.Texts())
	}
}

// Editing a remote supports renaming it, changing its URL, and both at
// once. A rename moves the remote-tracking branches and the upstream
// configuration of the local branches with it, exactly as git remote rename.
func TestRemoteManageEditRemote(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-edit")
	bareDir := newBareRemote(t, repo, "other")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	wantURL := remoteURLOf(t, repo, "origin")
	if wantURL == "" {
		t.Fatal("origin has no URL in the fixture")
	}

	// Rename only: origin becomes upstream, URL unchanged.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
		t.Fatalf("choose Edit 'origin'…: %v", err)
	}
	h.ui.Frame()
	assertDialogLayout(t, h, "Cancel", "Save")
	typeField(t, h, "Name", "upstream", true)
	clickDialogButton(t, h, "Save")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected rename error: %v", err)
	}
	if got := remoteList(t, repo); strings.Contains(got, "origin") {
		t.Fatalf("remotes after rename = %q, want origin gone", got)
	}
	if got := remoteURLOf(t, repo, "upstream"); got != wantURL {
		t.Fatalf("URL after rename = %q, want unchanged %q", got, wantURL)
	}
	if got := upstreamOf(t, repo, "main"); got != "upstream/main" {
		t.Fatalf("main upstream after rename = %q, want upstream/main", got)
	}
	if out, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/upstream/main"); err != nil {
		t.Fatalf("upstream/main tracking ref missing after rename: %v\n%s", err, out)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "upstream"); !ok {
		t.Fatalf("upstream missing from the sidebar after rename; texts=%q", h.ui.Texts())
	}

	// URL only: the name stays, the URL changes.
	if err := h.ui.RightClick("upstream"); err != nil {
		t.Fatalf("right-click upstream: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'upstream'…"); err != nil {
		t.Fatalf("choose Edit 'upstream'…: %v", err)
	}
	h.ui.Frame()
	// The inputs are prefilled with the remote's current name and URL.
	if fld, ok := h.ui.AccessByRole(ui.RoleTextField, "URL"); !ok || fld.Value != wantURL {
		v := ""
		if ok {
			v = fld.Value
		}
		t.Fatalf("URL input prefill = %q (found=%v), want %q", v, ok, wantURL)
	}
	typeField(t, h, "URL", bareDir, true)
	clickDialogButton(t, h, "Save")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected URL edit error: %v", err)
	}
	if got := remoteURLOf(t, repo, "upstream"); got != bareDir {
		t.Fatalf("URL after edit = %q, want %q", got, bareDir)
	}
	if got := remoteList(t, repo); !strings.Contains(got, "upstream") {
		t.Fatalf("remotes after URL edit = %q, want upstream kept", got)
	}

	// Both at once: upstream becomes origin again pointing at the first URL.
	if err := h.ui.RightClick("upstream"); err != nil {
		t.Fatalf("right-click upstream again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'upstream'…"); err != nil {
		t.Fatalf("choose Edit 'upstream'… again: %v", err)
	}
	h.ui.Frame()
	typeField(t, h, "Name", "origin", true)
	typeField(t, h, "URL", wantURL, true)
	clickDialogButton(t, h, "Save")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected rename-and-URL error: %v", err)
	}
	if got := remoteURLOf(t, repo, "origin"); got != wantURL {
		t.Fatalf("URL after rename-and-URL = %q, want %q", got, wantURL)
	}
	if got := remoteList(t, repo); strings.Contains(got, "upstream") {
		t.Fatalf("remotes after rename-and-URL = %q, want upstream gone", got)
	}
	if got := upstreamOf(t, repo, "main"); got != "origin/main" {
		t.Fatalf("main upstream after rename-and-URL = %q, want origin/main", got)
	}
}

// Deleting a remote through the confirmation dialog removes it and its
// remote-tracking branches, and the local branches tracking them lose their
// upstream. Canceling first leaves everything in place.
func TestRemoteManageDeleteRemote(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-delete")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if got := upstreamOf(t, repo, "main"); got != "origin/main" {
		t.Fatalf("main upstream before delete = %q, want origin/main", got)
	}

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'…: %v", err)
	}
	h.ui.Frame()
	assertDialogLayout(t, h, "Cancel", "Confirm Delete Remote")
	if !h.ui.HasText("remote-tracking branches") {
		t.Fatalf("remote-tracking note not shown; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("lose their upstream") {
		t.Fatalf("upstream loss note not shown; texts=%q", h.ui.Texts())
	}

	// Canceling first leaves everything in place.
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Delete Remote: %v", err)
	}
	h.ui.Frame()
	if remoteURLOf(t, repo, "origin") == "" {
		t.Fatal("origin was deleted even though the dialog was canceled")
	}

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'… again: %v", err)
	}
	h.ui.Frame()
	clickDialogButton(t, h, "Confirm Delete Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if got := remoteList(t, repo); strings.Contains(got, "origin") {
		t.Fatalf("remotes after delete = %q, want origin gone", got)
	}
	if out, err := tryGitCommand(repo.Path(), "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main"); err == nil {
		t.Fatalf("origin/main tracking ref survived the delete: %s", out)
	}
	if got := upstreamOf(t, repo, "main"); got != "" {
		t.Fatalf("main upstream after delete = %q, want none", got)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "origin"); ok {
		t.Fatalf("origin stayed visible in the sidebar after delete; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("origin/main"); ok {
		t.Fatal("origin/main stayed visible in the sidebar after delete")
	}
}

// The write guards refuse what the repository no longer matches: a remote
// whose URL moved since the dialog opened is neither edited nor deleted,
// a removed remote is never touched, and a same-named remote added
// elsewhere is never overwritten by Add.
func TestRemoteManageGuardsRefuseStaleWrites(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-guards")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	wantURL := remoteURLOf(t, repo, "origin")

	// Edit refuses when the remote's URL changed after the dialog opened.
	openEdit := func() {
		t.Helper()
		if err := h.ui.RightClick("origin"); err != nil {
			t.Fatalf("right-click origin: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
			t.Fatalf("choose Edit 'origin'…: %v", err)
		}
		h.ui.Frame()
	}
	openEdit()
	repo.Git("remote", "set-url", "origin", wantURL+"-changed")
	typeField(t, h, "Name", "renamed", true)
	clickDialogButton(t, h, "Save")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale edit produced no error")
	} else if !strings.Contains(err.Error(), "URL changed") {
		t.Fatalf("stale edit error = %q, want the URL-changed refusal", err)
	}
	if got := remoteList(t, repo); strings.Contains(got, "renamed") {
		t.Fatalf("remotes after refused edit = %q, the remote was renamed anyway", got)
	}
	if got := remoteURLOf(t, repo, "origin"); got != wantURL+"-changed" {
		t.Fatalf("origin URL after refused edit = %q, want the external change %q preserved", got, wantURL+"-changed")
	}

	// Edit refuses when the remote was removed after the dialog opened.
	openEdit()
	repo.Git("remote", "remove", "origin")
	clickDialogButton(t, h, "Save")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("edit of a removed remote produced no error")
	} else if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("removed-remote edit error = %q, want the no-longer-exists refusal", err)
	}
	if got := remoteList(t, repo); got != "" {
		t.Fatalf("remotes after refused edit of removed remote = %q, want empty: nothing was recreated", got)
	}

	// Delete refuses when the remote's URL changed after the dialog opened.
	repo.Git("remote", "add", "origin", wantURL)
	// The sidebar shows the re-added remote only after a reload; the dialogs
	// capture from the session snapshot, so the menu needs the reload.
	h.app.session.Reload()
	h.drain()
	openDelete := func() {
		t.Helper()
		if err := h.ui.RightClick("origin"); err != nil {
			t.Fatalf("right-click origin for delete: %v", err)
		}
		h.ui.Frame()
		if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
			t.Fatalf("choose Delete 'origin'…: %v", err)
		}
		h.ui.Frame()
	}
	openDelete()
	repo.Git("remote", "set-url", "origin", wantURL+"-moved")
	clickDialogButton(t, h, "Confirm Delete Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale delete produced no error")
	} else if !strings.Contains(err.Error(), "URL changed") {
		t.Fatalf("stale delete error = %q, want the URL-changed refusal", err)
	}
	if got := remoteURLOf(t, repo, "origin"); got != wantURL+"-moved" {
		t.Fatalf("origin URL after refused delete = %q, want %q: the remote was deleted anyway", got, wantURL+"-moved")
	}

	// Add refuses when the same-named remote appeared after the dialog opened.
	repo.Git("remote", "remove", "origin")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("upstream")
	typeField(t, h, "URL", wantURL, false)
	repo.Git("remote", "add", "upstream", wantURL+"-other")
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale add produced no error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("stale add error = %q, want the already-exists refusal", err)
	}
	if got := remoteURLOf(t, repo, "upstream"); got != wantURL+"-other" {
		t.Fatalf("upstream URL after refused add = %q, want %q: the external remote was overwritten", got, wantURL+"-other")
	}
}

// While a write is in flight the three entries disable: the New Remote row
// swallows its click and the menu items refuse. Once the write completes the
// entries work again.
func TestRemoteManageEntriesDisabledWhileBusy(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-busy")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	h.queue.PauseWorkers()
	defer h.queue.ResumeWorkers()

	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'…: %v", err)
	}
	h.ui.Frame()
	clickDialogButton(t, h, "Confirm Delete Remote")
	// The write's worker exists and is held before running.
	write := h.queue.WaitWorker(t)

	// The write is busy and held: the menu items disable.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin while busy: %v", err)
	}
	h.ui.Frame()
	for _, item := range []string{"Edit 'origin'…", "Delete 'origin'…", "New Remote…"} {
		if err := h.ui.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("menu item %q while busy: err=%v, want a disabled refusal", item, err)
		}
	}
	h.ui.Frame()

	// The New Remote row opens nothing while the write is in flight.
	_ = h.ui.Click("New Remote")
	h.ui.Frame()
	if h.ui.HasText("Remote URL") {
		t.Fatal("New Remote opened the dialog while a write was in flight")
	}

	h.queue.ReleaseWorker(write)
	h.queue.ResumeWorkers()
	h.drain()
	if remoteURLOf(t, repo, "origin") != "" {
		t.Fatal("origin survived the confirmed delete after the busy write finished")
	}
}

// A remote URL holding credentials never shows them outside the input the
// user edits: the sidebar and the delete confirmation scrub them, and a
// refusal quoting the URL scrubs them too. The edit input itself prefills
// the raw URL, because editing it is what the dialog is for.
func TestRemoteManageCredentialsRedacted(t *testing.T) {
	repo := setupSidebarRepo(t, "remote-redact")
	credURL := "https://user:secret@example.invalid/x.git"
	// The fixture's origin points at a local bare repository; point it at a
	// credentialed URL that never connects: nothing fetches it here.
	repo.Git("remote", "set-url", "origin", credURL)

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	if h.ui.HasText("secret") {
		t.Fatalf("credentials visible in the sidebar; texts=%q", h.ui.Texts())
	}

	// The delete confirmation quotes the URL scrubbed.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'…: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText("secret") {
		t.Fatalf("credentials visible in the delete dialog; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("https://example.invalid/x.git") {
		t.Fatalf("scrubbed URL not shown in the delete dialog; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Delete Remote: %v", err)
	}
	h.ui.Frame()

	// The edit input prefill is the raw URL, credentials included.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin again: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Edit 'origin'…"); err != nil {
		t.Fatalf("choose Edit 'origin'…: %v", err)
	}
	h.ui.Frame()
	if fld, ok := h.ui.AccessByRole(ui.RoleTextField, "URL"); !ok || fld.Value != credURL {
		v := ""
		if ok {
			v = fld.Value
		}
		t.Fatalf("URL input prefill = %q (found=%v), want the raw URL", v, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("cancel Edit Remote: %v", err)
	}
	h.ui.Frame()

	// A refusal quoting the URL scrubs the credentials, in the session error
	// and in what the UI shows.
	if err := h.ui.RightClick("origin"); err != nil {
		t.Fatalf("right-click origin a third time: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'origin'…"); err != nil {
		t.Fatalf("choose Delete 'origin'… again: %v", err)
	}
	h.ui.Frame()
	repo.Git("remote", "set-url", "origin", credURL+"-moved")
	clickDialogButton(t, h, "Confirm Delete Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("stale credentialed delete produced no error")
	} else {
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("credentials in the session error: %q", err)
		}
		if !strings.Contains(err.Error(), "https://example.invalid/x.git") {
			t.Fatalf("scrubbed URL missing from the refusal: %q", err)
		}
	}
	if h.ui.HasText("secret") {
		t.Fatalf("credentials visible in the UI after the refusal; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("https://example.invalid/x.git") {
		t.Fatalf("scrubbed URL not visible in the UI; texts=%q", h.ui.Texts())
	}
}

// The remote dialogs are scoped to the repository: switching repositories
// discards the drafts.
func TestRemoteManageDialogsResetOnRepoSwitch(t *testing.T) {
	repoA := setupSidebarRepo(t, "remote-switch-a")
	repoB := setupSidebarRepo(t, "remote-switch-b")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	if !remoteUI(h.app).addOpen {
		t.Fatal("add remote dialog did not open")
	}

	h.openRepo(repoB.StoreRepo())
	h.drain()

	st := remoteUI(h.app)
	if st.addOpen || st.editOpen || st.delOpen {
		t.Fatalf("remote dialogs survived a repository switch: %+v", st)
	}
}

// --- Loopback SSH ---

// On a loopback SSH repository the same entries run on the server: adding a
// remote with fetch wires it and downloads the server remote's branches,
// and deleting removes it again.
func TestRemoteManageAddAndDeleteLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// A bare repository on the same server holding main, not yet a remote.
	bare := srv.InitBareRepo(t, "upstream")
	srv.RunGit(t, repo.Path, "push", "-q", bare, "main")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	if err := h.ui.Click("New Remote"); err != nil {
		t.Fatalf("click New Remote: %v", err)
	}
	h.ui.Frame()
	h.ui.Type("upstream")
	typeField(t, h, "URL", bare, false)
	clickDialogButton(t, h, "Add Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH add error: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, repo.Path, "remote", "get-url", "upstream")); got != bare {
		t.Fatalf("server remote get-url upstream = %q, want %q", got, bare)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, "upstream"); !ok {
		t.Fatalf("upstream missing from the SSH sidebar after add; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("upstream/main"); !ok {
		t.Fatalf("upstream/main missing after SSH fetch; texts=%q", h.ui.Texts())
	}

	if err := h.ui.RightClick("upstream"); err != nil {
		t.Fatalf("right-click upstream on SSH: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Delete 'upstream'…"); err != nil {
		t.Fatalf("choose Delete 'upstream'… on SSH: %v", err)
	}
	h.ui.Frame()
	clickDialogButton(t, h, "Confirm Delete Remote")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected SSH delete error: %v", err)
	}
	out := srv.RunShell(t, "cd "+git.ShellQuote(repo.Path)+"; git remote; echo done")
	if strings.Contains(out, "upstream") {
		t.Fatalf("upstream survived the SSH delete: %q", out)
	}
	if _, ok := h.ui.Find("upstream/main"); ok {
		t.Fatal("upstream/main stayed visible in the SSH sidebar after delete")
	}
}
