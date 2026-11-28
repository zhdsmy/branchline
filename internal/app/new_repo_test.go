package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// resolvedPath is dir with its symlinks settled, the spelling
// rev-parse --show-toplevel answers with on macOS where /var is one.
func resolvedPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// initBranchInApp is what git symbolic-ref HEAD says inside dir, for the
// unborn-branch assertions of the creates.
func initBranchInApp(t *testing.T, dir string) string {
	t.Helper()
	return gitOutTrim(t, dir, "symbolic-ref", "HEAD")
}

// openNewRepoDialog opens the New Repository dialog through the repository
// switcher popup, as a user does.
func openNewRepoDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("New Repository…"); err != nil {
		t.Fatalf("click New Repository: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.ui.HasText("Initial Branch") {
		t.Fatalf("New Repository dialog did not open; texts=%q", h.ui.Texts())
	}
}

// clickCreateNewRepo clicks the dialog's Create button and drains the worker.
func clickCreateNewRepo(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Create"); err != nil {
		t.Fatalf("click Create: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
}

// The New Repository dialog of the switcher creates a repository in the
// chosen parent folder on this Mac and opens it: the dialog closes, the
// repository is the one entry of the list, and the session shows the empty
// repository — no commits, the unborn main branch — exactly as Add Local
// Repository shows one.
func TestNewRepositoryThisMacCreatesAndOpens(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)
	replaceField(t, h, "Parent Folder", parent)
	replaceField(t, h, "Name", "fresh")
	clickCreateNewRepo(t, h)

	st := h.app.newRepoUI()
	if st.open {
		t.Fatalf("dialog stayed open after a successful create; err %q", st.err)
	}
	target := filepath.Join(resolvedPath(t, parent), "fresh")
	if got := initBranchInApp(t, target); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}
	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after the create")
	}
	if meta := h.app.session.Meta(); meta.Path != target || meta.Kind != store.Local {
		t.Fatalf("session meta = %+v, want local repo at %q", meta, target)
	}
	emptySnapshotLoaded(t, h)
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" {
		t.Fatalf("Status.Head = %q, want empty (unborn)", snap.Status.Head)
	}
	if snap.Status.Branch != "main" {
		t.Fatalf("Status.Branch = %q, want main", snap.Status.Branch)
	}
	if !h.ui.HasText("No branches") {
		t.Fatalf("sidebar missing the empty-repository look; texts=%q", h.ui.Texts())
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Path != target || repos[0].Kind != store.Local {
		t.Fatalf("store repos = %+v, want one local entry at %q", repos, target)
	}
	if repos[0].Name != "fresh" {
		t.Fatalf("stored name = %q, want fresh", repos[0].Name)
	}
}

// A parent folder or a name whose edge is a space is refused in the dialog
// before anything starts: the error names the field, the dialog stays open
// and idle, and neither spelling of the target is created.
func TestNewRepositoryRejectsEdgeSpaces(t *testing.T) {
	t.Run("parent folder", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "code")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		h := newTestApp(t)
		closeSync(t, h)
		openNewRepoDialog(t, h)
		replaceField(t, h, "Parent Folder", parent+" ")
		replaceField(t, h, "Name", "fresh")
		clickCreateNewRepo(t, h)

		st := h.app.newRepoUI()
		if !st.open {
			t.Fatal("dialog closed on a parent folder ending in a space")
		}
		if st.busy {
			t.Fatal("dialog stayed busy after the validation error")
		}
		if got, want := st.err, "Parent Folder cannot begin or end with a space."; got != want {
			t.Fatalf("error = %q, want %q", got, want)
		}
		for _, dir := range []string{filepath.Join(parent, "fresh"), parent + " "} {
			if _, err := os.Stat(dir); err == nil {
				t.Fatalf("%q exists after the refused create", dir)
			}
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("store gained an entry: %+v", h.store.Repos())
		}
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
	})

	t.Run("name", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "code")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		h := newTestApp(t)
		closeSync(t, h)
		openNewRepoDialog(t, h)
		replaceField(t, h, "Parent Folder", parent)
		replaceField(t, h, "Name", "fresh ")
		clickCreateNewRepo(t, h)

		st := h.app.newRepoUI()
		if !st.open {
			t.Fatal("dialog closed on a name ending in a space")
		}
		if got, want := st.err, "Name cannot begin or end with a space."; got != want {
			t.Fatalf("error = %q, want %q", got, want)
		}
		for _, dir := range []string{filepath.Join(parent, "fresh"), filepath.Join(parent, "fresh ")} {
			if _, err := os.Stat(dir); err == nil {
				t.Fatalf("%q exists after the refused create", dir)
			}
		}
		if len(h.store.Repos()) != 0 {
			t.Fatalf("store gained an entry: %+v", h.store.Repos())
		}
		if h.app.session.HasRepo() {
			t.Fatal("a repository opened when the create was refused")
		}
		h.ui.Key(0, ui.KeyEscape)
		h.ui.Frame()
	})
}

// A folder already holding the name is never initialized into: the error
// stays in the dialog, which stays open, and nothing is saved or opened.
func TestNewRepositoryNameExistsStaysInDialog(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "code")
	target := filepath.Join(parent, "taken")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)
	replaceField(t, h, "Parent Folder", parent)
	replaceField(t, h, "Name", "taken")
	clickCreateNewRepo(t, h)

	st := h.app.newRepoUI()
	if !st.open {
		t.Fatal("dialog closed when the name existed")
	}
	if st.busy {
		t.Fatal("dialog stayed busy after the failure")
	}
	if !strings.Contains(st.err, `a file or folder named "taken" already exists`) {
		t.Fatalf("error = %q, want the collision named", st.err)
	}
	if !h.ui.HasText(st.err) {
		t.Fatalf("error not visible in the dialog; texts=%q", h.ui.Texts())
	}
	got, err := os.ReadFile(filepath.Join(target, "sentinel.txt"))
	if err != nil || string(got) != "keep me\n" {
		t.Fatalf("sentinel after refused create = %q err %v, want intact", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal("the init wrote into the existing folder")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	if h.app.session.HasRepo() {
		t.Fatal("a repository opened when the create was refused")
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// An initial branch emptied before Create is an error of the draft rather
// than a silent main; a branch other than main — trunk here — is the unborn
// HEAD of the repository the retry creates.
func TestNewRepositoryBranchEmptyThenTrunk(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)
	replaceField(t, h, "Parent Folder", parent)
	replaceField(t, h, "Name", "trunky")
	replaceField(t, h, "Initial Branch", "")
	clickCreateNewRepo(t, h)

	st := h.app.newRepoUI()
	if !st.open {
		t.Fatal("dialog closed on an empty initial branch")
	}
	if got, want := st.err, "Initial Branch cannot be empty."; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(parent, "trunky")); err == nil {
		t.Fatal("the empty-branch create made the folder")
	}

	replaceField(t, h, "Initial Branch", "trunk")
	clickCreateNewRepo(t, h)

	st = h.app.newRepoUI()
	if st.open {
		t.Fatalf("dialog stayed open after the trunk create; err %q", st.err)
	}
	target := filepath.Join(resolvedPath(t, parent), "trunky")
	if got := initBranchInApp(t, target); got != "refs/heads/trunk" {
		t.Fatalf("HEAD = %q, want refs/heads/trunk", got)
	}
	if got := h.app.session.Snapshot().Status.Branch; got != "trunk" {
		t.Fatalf("session branch = %q, want trunk", got)
	}
}

// Closing the dialog mid-create and reopening it discards the worker's
// completion: the repository the create made on disk is never opened, the
// list stays empty, and the reopened dialog keeps its fresh draft.
func TestNewRepositoryCloseMidCreateDiscardsResult(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "code")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)
	replaceField(t, h, "Parent Folder", parent)
	replaceField(t, h, "Name", "dropped")

	h.queue.PauseWorkers()
	if err := h.ui.Click("Create"); err != nil {
		t.Fatalf("click Create: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	worker := h.queue.WaitWorker(t)
	if !h.ui.HasText("Creating…") {
		t.Fatalf("busy indicator missing mid-create; texts=%q", h.ui.Texts())
	}

	// Close the dialog and reopen a fresh draft before the worker runs.
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if h.app.newRepoUI().open {
		t.Fatal("Escape left the dialog open")
	}
	openNewRepoDialog(t, h)
	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	h.drain()

	st := h.app.newRepoUI()
	if !st.open {
		t.Fatal("the stale completion closed the reopened dialog")
	}
	if h.app.session.HasRepo() {
		t.Fatal("the stale completion opened the repository")
	}
	if len(h.store.Repos()) != 0 {
		t.Fatalf("store gained an entry: %+v", h.store.Repos())
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}

// Creating onto a server runs git init there and opens the repository as an
// SSH repository: the stored path is the absolute working tree root the
// server reports rather than the symlinked spelling typed into the dialog,
// and the login's identity travels with the entry.
func TestNewRepositorySSHServerCreatesAndOpens(t *testing.T) {
	srv := startCloneSSHServer(t)
	parent := srv.TempDir(t, "newrepo-parent")

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)
	if err := h.ui.Click("SSH Server"); err != nil {
		t.Fatalf("click SSH Server segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	replaceField(t, h, "Server Folder", sshServerFolderAddress(t, srv, parent))
	replaceField(t, h, "Identity file (optional)", srv.Identity)
	replaceField(t, h, "Name", "srvfresh")
	clickCreateNewRepo(t, h)

	st := h.app.newRepoUI()
	if st.open {
		t.Fatalf("dialog stayed open after the server create; err %q", st.err)
	}
	target := filepath.Join(parent, "srvfresh")
	if got := srv.RunGit(t, target, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("server HEAD = %q, want refs/heads/main", got)
	}
	if !remoteFileExists(t, srv, filepath.Join(target, ".git")) {
		t.Fatalf("the server has no repository at %s", target)
	}

	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after the server create")
	}
	meta := h.app.session.Meta()
	if meta.Kind != store.SSH {
		t.Fatalf("session kind = %q, want ssh", meta.Kind)
	}
	if meta.Path != target {
		t.Fatalf("session path = %q, want the server's absolute root %q", meta.Path, target)
	}
	if strings.HasPrefix(meta.Path, "~") || !strings.HasPrefix(meta.Path, "/") {
		t.Fatalf("stored path %q is not absolute", meta.Path)
	}
	if meta.Identity != srv.Identity {
		t.Fatalf("stored identity = %q, want %q", meta.Identity, srv.Identity)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot of the server repository failed to load: %v", err)
	}
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" || snap.Status.Branch != "main" {
		t.Fatalf("session status = head %q branch %q, want unborn main", snap.Status.Head, snap.Status.Branch)
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Kind != store.SSH || repos[0].Path != target {
		t.Fatalf("store repos = %+v, want one SSH entry at %q", repos, target)
	}
	if repos[0].Host != srv.Host || repos[0].User != srv.User || repos[0].Port != srv.Port {
		t.Fatalf("stored login = %s@%s:%d, want %s@%s:%d", repos[0].User, repos[0].Host, repos[0].Port, srv.User, srv.Host, srv.Port)
	}
	if repos[0].Name != "srvfresh" {
		t.Fatalf("stored name = %q, want srvfresh", repos[0].Name)
	}
}

// Create Repository Here in the not-a-git notice initializes the chosen
// folder itself and opens it: the notice closes, the file the folder already
// held is untouched and shows as untracked in the fresh repository, and the
// stored path is the canonical root.
func TestNewRepositoryCreateHereInitsChosenFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plainfolder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("already here\n"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	stubPickLocalFolder(t, func() (string, error) { return dir, nil })

	h := newTestApp(t)
	closeSync(t, h)
	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	if err := h.ui.Click("Add Local Repository…"); err != nil {
		t.Fatalf("click Add Local Repository: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()
	if !h.ui.HasText("Not a Git Repository") {
		t.Fatalf("not-a-git notice missing; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Create Repository Here"); err != nil {
		t.Fatalf("click Create Repository Here: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.drain()

	if h.ui.HasText("Not a Git Repository") {
		t.Fatal("notice stayed open after the create")
	}
	want := resolvedPath(t, dir)
	if got := initBranchInApp(t, want); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}
	got, err := os.ReadFile(filepath.Join(want, "note.txt"))
	if err != nil || string(got) != "already here\n" {
		t.Fatalf("existing file after init = %q err %v, want intact", got, err)
	}
	if !h.app.session.HasRepo() {
		t.Fatal("no repository open after Create Repository Here")
	}
	if meta := h.app.session.Meta(); meta.Path != want || meta.Kind != store.Local {
		t.Fatalf("session meta = %+v, want local repo at %q", meta, want)
	}
	emptySnapshotLoaded(t, h)
	if snap := h.app.session.Snapshot(); snap.Status.Head != "" || snap.Status.Branch != "main" {
		t.Fatalf("session status = head %q branch %q, want unborn main", snap.Status.Head, snap.Status.Branch)
	}
	if !hasAccessRow(h, "Unstaged note.txt") && !h.ui.HasText("Unstaged note.txt") {
		t.Fatalf("the folder's file missing from Local Changes; texts=%q", h.ui.Texts())
	}
	repos := h.store.Repos()
	if len(repos) != 1 || repos[0].Path != want {
		t.Fatalf("store repos = %+v, want one entry at %q", repos, want)
	}
	if repos[0].Name != "plainfolder" {
		t.Fatalf("stored name = %q, want plainfolder", repos[0].Name)
	}
}

// The folder picker of the New Repository dialog's Choose button fills the
// parent field with the dialog's title, and canceling it leaves the field
// unchanged.
func TestNewRepositoryChooseFolderPicker(t *testing.T) {
	var picked func() (string, error)
	var gotTitle string
	prev := pickParentFolderFn
	pickParentFolderFn = func(_ *mygo.Window, title string) (string, error) {
		gotTitle = title
		return picked()
	}
	t.Cleanup(func() { pickParentFolderFn = prev })

	h := newTestApp(t)
	closeSync(t, h)
	openNewRepoDialog(t, h)

	chosen := filepath.Join(t.TempDir(), "chosenparent")
	picked = func() (string, error) { return chosen, nil }
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if gotTitle != "New Repository" {
		t.Fatalf("picker title = %q, want %q", gotTitle, "New Repository")
	}
	if got := h.app.newRepoUI().parent; got != chosen {
		t.Fatalf("parent after Choose = %q, want %q", got, chosen)
	}

	picked = func() (string, error) { return "", nil }
	if err := h.ui.Click("Choose…"); err != nil {
		t.Fatalf("click Choose again: %v", err)
	}
	h.ui.Frame()
	h.drain()
	if got := h.app.newRepoUI().parent; got != chosen {
		t.Fatalf("canceling the picker changed the parent to %q, want %q", got, chosen)
	}
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
}
