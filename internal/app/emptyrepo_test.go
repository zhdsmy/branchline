package app

// End-to-end UI tests: opening a truly empty repository or one
// checked out on an uncommitted --orphan branch through the real product
// entry points (Repository Switcher, Add Local Repository, Add SSH
// Repository) must load a complete snapshot instead of failing with
// "bad revision 'HEAD'", and the first commit must be creatable through the
// real Local Changes controls. Covers the local executor and the loopback
// sshd, plus the corrupt/missing failure paths that must keep surfacing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// emptySnapshotLoaded asserts the freshly opened repository loaded without an
// error and returns the snapshot for further assertions.
func emptySnapshotLoaded(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("session snapshot not marked loaded")
	}
	if h.ui.HasText("Failed to load repository status") || h.ui.HasText("bad revision") {
		t.Fatalf("snapshot error text visible; texts=%q", h.ui.Texts())
	}
}

// addLocalRepoThroughSwitcher drives the real Add Local Repository flow: open
// the switcher, click the entry with the picker stubbed to dir, and drain.
func addLocalRepoThroughSwitcher(t *testing.T, h *testHarness, dir string) {
	t.Helper()
	stubPickLocalFolder(t, func() (string, error) { return dir, nil })
	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
}

// TestEmptyRepoFirstCommit walks the MVP main path on a truly empty local
// repository: add it through the real product entry, verify the unborn
// snapshot, stage a file, commit it through the real controls, and check the
// born repository in Local Changes and History.
func TestEmptyRepoFirstCommit(t *testing.T) {
	repo := newGitRepo(t, "empty-local")
	repo.WriteFile("hello.txt", "hello\n")

	h := newTestApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())

	emptySnapshotLoaded(t, h)
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" {
		t.Fatalf("Status.Head = %q, want empty (unborn)", snap.Status.Head)
	}
	if snap.Status.Branch != "main" {
		t.Fatalf("Status.Branch = %q, want main", snap.Status.Branch)
	}
	if len(snap.Commits) != 0 {
		t.Fatalf("len(Commits) = %d, want 0", len(snap.Commits))
	}
	if !h.ui.HasText("No branches") {
		t.Fatalf("sidebar missing %q on empty repository; texts=%q", "No branches", h.ui.Texts())
	}
	if !h.ui.HasText("Unstaged hello.txt") && !hasAccessRow(h, "Unstaged hello.txt") {
		t.Fatalf("hello.txt missing from Local Changes; texts=%q", h.ui.Texts())
	}

	// Stage through the real row button.
	mustClick(t, h, "Stage hello.txt")
	h.drain()
	if !hasAccessRow(h, "Staged hello.txt") {
		t.Fatalf("hello.txt did not move to staged list; texts=%q", h.ui.Texts())
	}

	// Commit through the real message field and Commit button.
	typeCommitMessage(t, h, "first commit")
	mustClick(t, h, "Commit")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("commit action failed: %v", err)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("post-commit snapshot load failed: %v", err)
	}
	if !h.ui.HasText("Working tree is clean") {
		t.Fatalf("Local Changes not clean after first commit; texts=%q", h.ui.Texts())
	}

	wantHead := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	snap = h.app.session.Snapshot()
	if snap.Status.Head != wantHead {
		t.Fatalf("post-commit Status.Head = %q, want %q", snap.Status.Head, wantHead)
	}
	if len(snap.Commits) != 1 || snap.Commits[0].Subject != "first commit" {
		t.Fatalf("post-commit commits = %+v, want one 'first commit'", snap.Commits)
	}
	if snap.Commits[0].Hash != wantHead {
		t.Fatalf("Commits[0].Hash = %q, want HEAD %q", snap.Commits[0].Hash, wantHead)
	}
	if !hasAccessRow(h, "main") && !h.ui.HasText("main") {
		t.Fatalf("sidebar missing branch main after first commit; texts=%q", h.ui.Texts())
	}

	// Switch to History through the real navigation row.
	mustClick(t, h, "History")
	h.ui.Frame()
	h.drain()
	if h.app.route.page != pageHistory {
		t.Fatalf("page = %v, want pageHistory", h.app.route.page)
	}
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") || h.ui.HasText("No commits yet") {
		t.Fatalf("History page shows an unexpected error or empty state; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("first commit") {
		t.Fatalf("History list missing commit subject %q; texts=%q", "first commit", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(wantHead, "first commit")); !ok {
		t.Fatalf("History list missing commit row %q; texts=%q", commitRowLabel(wantHead, "first commit"), h.ui.Texts())
	}
	snap = h.app.session.Snapshot()
	if len(snap.Commits) != 1 || snap.Commits[0].Subject != "first commit" {
		t.Fatalf("history snapshot commits = %+v, want one 'first commit'", snap.Commits)
	}
}

// hasAccessRow reports whether an accessible element with the given name
// exists in any role.
func hasAccessRow(h *testHarness, name string) bool {
	for _, role := range []ui.Role{ui.RoleButton, ui.RoleListItem, ui.RoleRow, ui.RoleText} {
		if _, ok := h.ui.AccessByRole(role, name); ok {
			return true
		}
	}
	return false
}

// TestEmptyRepoOrphanBranch opens a repository checked out on an uncommitted
// --orphan branch whose main already has commits.
func TestEmptyRepoOrphanBranch(t *testing.T) {
	repo := newGitRepo(t, "orphan-local")
	mainTip := repo.CommitFile("a.txt", "one\n", "first on main")
	repo.Git("checkout", "-q", "--orphan", "orphanui")
	repo.Git("rm", "-q", "-r", "--cached", ".")
	repo.WriteFile("orphan-work.txt", "orphan\n")

	h := newTestApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())

	emptySnapshotLoaded(t, h)
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" {
		t.Fatalf("orphan Status.Head = %q, want empty", snap.Status.Head)
	}
	if snap.Status.Branch != "orphanui" {
		t.Fatalf("orphan Status.Branch = %q, want orphanui", snap.Status.Branch)
	}
	var hashes []string
	for _, c := range snap.Commits {
		hashes = append(hashes, c.Hash)
	}
	if !containsString(hashes, mainTip) {
		t.Fatalf("orphan snapshot missing main commit %s: %v", mainTip, hashes)
	}
	if !h.ui.HasText("main") {
		t.Fatalf("sidebar missing existing branch main on orphan; texts=%q", h.ui.Texts())
	}
	for _, b := range snap.Refs.Branches {
		if b.Current {
			t.Fatalf("branch %q marked Current on orphan branch", b.Name)
		}
	}
	if !hasAccessRow(h, "Unstaged orphan-work.txt") {
		t.Fatalf("orphan working tree file missing from Local Changes; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") || h.ui.HasText("No commits yet") {
		t.Fatalf("orphan History page shows unexpected error or empty state; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("first on main") {
		t.Fatalf("orphan History list missing main commit subject; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(mainTip, "first on main")); !ok {
		t.Fatalf("orphan History list missing main commit row %q; texts=%q", commitRowLabel(mainTip, "first on main"), h.ui.Texts())
	}
}

// TestEmptyRepoBrokenRepoShowsError verifies a corrupt repository surfaces a
// load failure in the UI instead of being misread as an empty repository.
func TestEmptyRepoBrokenRepoShowsError(t *testing.T) {
	repo := newGitRepo(t, "broken-local")
	repo.CommitFile("a.txt", "one\n", "first")
	if err := os.WriteFile(filepath.Join(repo.Path(), ".git", "refs", "heads", "main"), []byte("not-a-hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newTestApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())

	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("corrupt repository loaded without error, want load failure")
	}
	if !h.ui.HasText("Failed to load repository status") {
		t.Fatalf("Local Changes missing load failure notice; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("No branches") && len(h.app.session.Snapshot().Refs.Branches) == 0 && h.app.session.LoadErr() == nil {
		t.Fatal("corrupt repository was misread as an empty repository")
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if !h.ui.HasText("Failed to load repository snapshot") {
		t.Fatalf("History missing load failure notice; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("No commits yet") {
		t.Fatal("broken repository was rendered as empty History state")
	}
}

// TestEmptyRepoMissingPathShowsError verifies opening a repository whose
// directory does not exist surfaces the readable missing-repository notice
// with its removal entry, instead of git's raw error.
func TestEmptyRepoMissingPathShowsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-repo")
	h := newTestApp(t)
	// The real product entry cannot produce a missing path through the picker
	// (rev-parse --show-toplevel fails first), so open through the same
	// session entry point the switcher itself calls after validation.
	h.openRepo(store.Repo{Kind: store.Local, Path: missing, Name: "missing"})
	h.drain()

	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("missing-path repository loaded without error, want load failure")
	}
	if !h.ui.HasText("Repository Not Found") {
		t.Fatalf("Local Changes missing the Repository Not Found notice; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Remove from List"); !ok {
		t.Fatalf("Remove from List button missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("fatal: cannot change to") {
		t.Fatalf("raw git error visible; texts=%q", h.ui.Texts())
	}
}

// initSSHEmptyRepo creates a truly empty repository on the loopback server
// with an untracked hello.txt, mirroring sshtest.InitRepo's configuration
// minus the initial commit.
func initSSHEmptyRepo(t *testing.T, srv *sshtest.Target, name string) string {
	t.Helper()
	dir := srv.TempDir(t, name)
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + git.ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Test'
git config user.email 'test@branchline.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
git config gc.auto 0
git config maintenance.auto false
printf 'hello\n' > hello.txt
`
	srv.RunShell(t, script)
	return dir
}

// startLoopbackForApp starts the loopback sshd and prepends its isolated ssh
// wrapper directory to PATH. Like loopbackSSHRepo it must be called before
// newTestApp so cleanup drains app workers before stopping sshd, and the test
// must not use t.Parallel.
func startLoopbackForApp(t *testing.T) *sshtest.Target {
	t.Helper()
	srv := sshtest.StartLoopback(t)
	if binDir := srv.BinDir(); binDir != "" {
		if pathEnv := os.Getenv("PATH"); pathEnv != "" {
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+pathEnv)
		} else {
			t.Setenv("PATH", binDir)
		}
	}
	return srv
}

// sshStoreRepo builds the store.Repo for dir on srv.
func sshStoreRepo(t *testing.T, srv *sshtest.Target, dir string) store.Repo {
	t.Helper()
	repo, err := store.ParseAddress(srv.Address(dir))
	if err != nil {
		t.Fatalf("parse SSH address %q: %v", srv.Address(dir), err)
	}
	repo.Name = store.DefaultName(repo.Path)
	repo.Identity = srv.Identity
	return repo
}

// openSSHRepoThroughSwitcher seeds the store with repo and opens it through
// the real Repository Switcher popup.
func openSSHRepoThroughSwitcher(t *testing.T, h *testHarness, repo store.Repo) {
	t.Helper()
	if _, err := h.store.Add(repo); err != nil {
		t.Fatalf("seed store with SSH repo: %v", err)
	}
	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("click %q in switcher: %v\ntexts=%q", repo.Name, err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
}

// TestEmptyRepoFirstCommitLoopbackSSH walks the same first-commit flow over
// the loopback sshd: the repository is created empty on the server, added to
// the store, opened through the Repository Switcher, and committed through
// the real Local Changes controls.
func TestEmptyRepoFirstCommitLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := initSSHEmptyRepo(t, srv, "empty-ssh")
	repo := sshStoreRepo(t, srv, dir)

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	emptySnapshotLoaded(t, h)
	if h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("session kind = %q, want SSH", h.app.session.Meta().Kind)
	}
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" || snap.Status.Branch != "main" {
		t.Fatalf("SSH empty status = head %q branch %q, want empty/main", snap.Status.Head, snap.Status.Branch)
	}
	if len(snap.Commits) != 0 {
		t.Fatalf("SSH empty len(Commits) = %d, want 0", len(snap.Commits))
	}
	if !h.ui.HasText("No branches") {
		t.Fatalf("SSH sidebar missing No branches; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Stage hello.txt")
	h.drain()
	if !hasAccessRow(h, "Staged hello.txt") {
		t.Fatalf("SSH hello.txt did not move to staged list; texts=%q", h.ui.Texts())
	}
	typeCommitMessage(t, h, "first commit")
	mustClick(t, h, "Commit")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH commit action failed: %v", err)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH post-commit load failed: %v", err)
	}
	wantHead := srv.RunGit(t, dir, "rev-parse", "HEAD")
	if got := srv.RunGit(t, dir, "log", "-1", "--format=%s"); got != "first commit" {
		t.Fatalf("remote commit subject = %q, want first commit", got)
	}
	snap = h.app.session.Snapshot()
	if snap.Status.Head != wantHead {
		t.Fatalf("SSH post-commit Status.Head = %q, want %q", snap.Status.Head, wantHead)
	}
	if len(snap.Commits) != 1 || snap.Commits[0].Subject != "first commit" {
		t.Fatalf("SSH post-commit commits = %+v, want one 'first commit'", snap.Commits)
	}
	if !h.ui.HasText("Working tree is clean") {
		t.Fatalf("SSH Local Changes not clean after commit; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	h.drain()
	if h.app.route.page != pageHistory {
		t.Fatalf("SSH page = %v, want pageHistory", h.app.route.page)
	}
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") || h.ui.HasText("No commits yet") {
		t.Fatalf("SSH History page shows an unexpected error or empty state; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("first commit") {
		t.Fatalf("SSH History list missing commit subject %q; texts=%q", "first commit", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(wantHead, "first commit")); !ok {
		t.Fatalf("SSH History list missing commit row %q; texts=%q", commitRowLabel(wantHead, "first commit"), h.ui.Texts())
	}
}

// TestEmptyRepoOrphanBranchLoopbackSSH opens an SSH repository checked out on
// an uncommitted orphan branch whose main already has commits.
func TestEmptyRepoOrphanBranchLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := srv.InitRepo(t, "orphan-ssh")
	mainTip := srv.RunGit(t, dir, "rev-parse", "refs/heads/main")
	mainSubject := srv.RunGit(t, dir, "log", "-1", "--format=%s", "refs/heads/main")
	srv.RunGit(t, dir, "checkout", "-q", "--orphan", "orphansshui")
	srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")
	srv.WriteFile(t, dir, "orphan-work.txt", []byte("orphan\n"))
	repo := sshStoreRepo(t, srv, dir)

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	emptySnapshotLoaded(t, h)
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" || snap.Status.Branch != "orphansshui" {
		t.Fatalf("SSH orphan status = head %q branch %q, want empty/orphansshui", snap.Status.Head, snap.Status.Branch)
	}
	var hashes []string
	for _, c := range snap.Commits {
		hashes = append(hashes, c.Hash)
	}
	if !containsString(hashes, mainTip) {
		t.Fatalf("SSH orphan snapshot missing main commit %s: %v", mainTip, hashes)
	}
	if !h.ui.HasText("main") {
		t.Fatalf("SSH sidebar missing branch main on orphan; texts=%q", h.ui.Texts())
	}
	for _, b := range snap.Refs.Branches {
		if b.Current {
			t.Fatalf("SSH branch %q marked Current on orphan branch", b.Name)
		}
	}
	if !hasAccessRow(h, "Unstaged orphan-work.txt") {
		t.Fatalf("SSH orphan working tree file missing from Local Changes; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") || h.ui.HasText("No commits yet") {
		t.Fatalf("SSH orphan History page shows unexpected error or empty state; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(mainSubject) {
		t.Fatalf("SSH orphan History list missing main commit subject %q; texts=%q", mainSubject, h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(mainTip, mainSubject)); !ok {
		t.Fatalf("SSH orphan History list missing main commit row %q; texts=%q", commitRowLabel(mainTip, mainSubject), h.ui.Texts())
	}
}

// TestEmptyRepoBrokenRepoShowsErrorLoopbackSSH verifies a corrupt SSH
// repository surfaces a load failure in the UI.
func TestEmptyRepoBrokenRepoShowsErrorLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := srv.InitRepo(t, "broken-ssh")
	srv.RunShell(t, "printf 'not-a-hash\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
	repo := sshStoreRepo(t, srv, dir)

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("corrupt SSH repository loaded without error, want load failure")
	}
	if !h.ui.HasText("Failed to load repository status") {
		t.Fatalf("SSH Local Changes missing load failure notice; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if !h.ui.HasText("Failed to load repository snapshot") {
		t.Fatalf("SSH History missing load failure notice; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("No commits yet") {
		t.Fatal("broken SSH repository was rendered as empty History state")
	}
}
