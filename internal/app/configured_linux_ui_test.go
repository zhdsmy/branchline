package app

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// remoteBranchExists checks on the remote SSH server whether refs/heads/<name>
// exists without failing the test when the ref is absent.
func remoteBranchExists(t *testing.T, srv *sshtest.Target, repoDir, name string) bool {
	t.Helper()
	out := strings.TrimSpace(srv.RunShell(t,
		"GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(repoDir)+
			" show-ref --verify --quiet "+git.ShellQuote("refs/heads/"+name)+
			" && printf present || printf absent"))
	return out == "present"
}

// remotePathExists checks on the remote SSH server whether relPath inside
// repoDir exists on disk using POSIX sh compatible with GNU and BusyBox.
func remotePathExists(t *testing.T, srv *sshtest.Target, repoDir, relPath string) bool {
	t.Helper()
	full := path.Join(repoDir, relPath)
	out := strings.TrimSpace(srv.RunShell(t,
		"if [ -e "+git.ShellQuote(full)+" ]; then printf present; else printf absent; fi"))
	return out == "present"
}

// History on a configured Linux SSH container (sshlab):
// opens a repository with more than one SSH page (>1000 commits) through the
// real Repository Switcher and History navigation, verifies ref badges
// (HEAD branch, local branch, tag), selects a commit to verify its metadata,
// changed file list, and file diffs, then loads the rest with Load More and
// inspects an older commit loaded by pagination.
func TestConfiguredLinuxHistoryListDetailDiffAndLoadMore(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	rootHash := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")

	// Construct 1020 commits in one git fast-import stream so the repository
	// exceeds the SSH first-page limit (1000) deterministically and fast on
	// Ubuntu 24.04 (e1), Ubuntu 20.04 / Git 2.25 (e2), and Alpine / BusyBox (e3).
	var stream strings.Builder
	for i := 1; i <= 1020; i++ {
		subject := fmt.Sprintf("imported %d", i)
		content := fmt.Sprintf("imported content %d\n", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&stream, "author Branchline Test <test@branchline.invalid> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&stream, "committer Branchline Test <test@branchline.invalid> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&stream, "data %d\n%s\n", len(subject), subject)
		if i == 1 {
			fmt.Fprintf(&stream, "from %s\n", rootHash)
		} else {
			fmt.Fprintf(&stream, "from :%d\n", i-1)
		}
		fmt.Fprintf(&stream, "M 100644 inline history.txt\ndata %d\n%s\n", len(content), content)
	}
	srv.RunShellInput(t,
		"cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet",
		[]byte(stream.String()))
	srv.RunGit(t, repo.Path, "reset", "--hard", "HEAD")

	// Add a branch and a tag on HEAD before creating the top commit so both
	// HEAD -> main and secondary ref badges are visible on the first page.
	srv.RunGit(t, repo.Path, "branch", "side-badge")
	srv.RunGit(t, repo.Path, "tag", "v1.0")

	srv.WriteFile(t, repo.Path, "history.txt", []byte("replaced linux line\nfinal linux line\n"))
	srv.WriteFile(t, repo.Path, "notes.txt", []byte("linux release notes\n"))
	srv.RunGit(t, repo.Path, "add", "--", "history.txt", "notes.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "linux head commit\n\nverified detail body on container")

	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	wantParent := srv.RunGit(t, repo.Path, "rev-parse", "HEAD~1")
	wantFirstImported := srv.RunGit(t, repo.Path, "rev-parse", "HEAD~1020")
	if got := srv.RunGit(t, repo.Path, "rev-list", "--count", "HEAD"); got != "1022" {
		t.Fatalf("server commit count = %q, want 1022", got)
	}
	if got := srv.RunGit(t, repo.Path, "show", "-s", "--format=%s", wantFirstImported); got != "imported 1" {
		t.Fatalf("HEAD~1020 subject = %q, want %q", got, "imported 1")
	}
	serverHeadFiles := strings.Fields(srv.RunGit(t, repo.Path, "diff-tree", "--no-commit-id", "--name-only", "-r", wantHead))
	slices.Sort(serverHeadFiles)
	if !slices.Equal(serverHeadFiles, []string{"history.txt", "notes.txt"}) {
		t.Fatalf("server HEAD changed files = %q, want [history.txt notes.txt]", serverHeadFiles)
	}

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("SSH snapshot not marked loaded")
	}
	snap := h.app.session.Snapshot()
	if snap.Status.Head != wantHead {
		t.Fatalf("snapshot HEAD = %q, want %q", snap.Status.Head, wantHead)
	}
	if len(snap.Commits) != defaultSSHLogLimit {
		t.Fatalf("SSH first page commits = %d, want %d", len(snap.Commits), defaultSSHLogLimit)
	}
	if !snap.More {
		t.Fatal("SSH snapshot.More = false, want true with 1022 commits")
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if h.app.route.page != pageHistory {
		t.Fatalf("route page = %v, want pageHistory", h.app.route.page)
	}
	if !h.ui.HasText("1000 commits") {
		t.Fatalf("History header missing first-page count; texts=%q", h.ui.Texts())
	}
	texts := h.ui.Texts()
	idxHead := slices.Index(texts, commitRowLabel(wantHead, "linux head commit"))
	idxParent := slices.Index(texts, commitRowLabel(wantParent, "imported 1020"))
	if idxHead < 0 || idxParent < 0 || idxHead >= idxParent {
		t.Fatalf("History commit rows missing or out of order: head=%d parent=%d; texts=%q", idxHead, idxParent, texts)
	}
	for _, badge := range []string{"HEAD → main", "side-badge", "v1.0"} {
		if !h.ui.HasText(badge) {
			t.Fatalf("History missing ref badge %q; texts=%q", badge, texts)
		}
	}
	if hasExactText(h.ui, "imported 1") {
		t.Fatal("oldest imported commit unexpectedly present before Load More")
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More"); !ok || btn.Disabled {
		t.Fatalf("Load More button = %+v (found=%v), want present and enabled", btn, ok)
	}

	// Select the top commit and verify detail metadata, changed files, and both file diffs.
	clickCommitRow(t, h, wantHead, "linux head commit")
	h.drain()

	if h.app.historyView.detailErr != nil {
		t.Fatalf("commit detail load error: %v", h.app.historyView.detailErr)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(wantHead, "linux head commit")); !ok || !row.Selected {
		t.Fatalf("selected HEAD commit row = %+v (found=%v), want Selected=true", row, ok)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(wantParent, "imported 1020")); !ok || row.Selected {
		t.Fatalf("unselected parent commit row = %+v (found=%v), want Selected=false", row, ok)
	}
	for _, want := range []string{
		wantHead,
		short7(wantParent),
		"verified detail body on container",
		"Branchline Test <test@branchline.invalid>",
		"history.txt",
		"notes.txt",
	} {
		if !h.ui.HasText(want) {
			t.Fatalf("commit detail missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	var uiFiles []string
	for _, f := range h.app.historyView.files {
		uiFiles = append(uiFiles, f.Path)
	}
	slices.Sort(uiFiles)
	if !slices.Equal(uiFiles, serverHeadFiles) {
		t.Fatalf("UI changed files = %q, want server files %q", uiFiles, serverHeadFiles)
	}

	// Verify the auto-selected file diff and then switch to the other file.
	firstFile := h.app.historyView.files[0].Path
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+firstFile); !ok || !row.Selected {
		t.Fatalf("auto-selected file row %q = %+v (found=%v), want Selected=true", firstFile, row, ok)
	}
	checkFileDiff := func(name string) {
		t.Helper()
		ins := diffAccessLines(h.ui, ui.RoleInsertion)
		del := diffAccessLines(h.ui, ui.RoleDeletion)
		switch name {
		case "history.txt":
			if !slices.Contains(ins, "replaced linux line") || !slices.Contains(ins, "final linux line") {
				t.Fatalf("history.txt diff insertions = %q, want replaced/final linux line", ins)
			}
			if !slices.Contains(del, "imported content 1020") {
				t.Fatalf("history.txt diff deletions = %q, want imported content 1020", del)
			}
		case "notes.txt":
			if !slices.Contains(ins, "linux release notes") {
				t.Fatalf("notes.txt diff insertions = %q, want linux release notes", ins)
			}
		default:
			t.Fatalf("unexpected file %q", name)
		}
	}
	checkFileDiff(firstFile)
	secondFile := h.app.historyView.files[1].Path
	mustClick(t, h, "File "+secondFile)
	h.drain()
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+secondFile); !ok || !row.Selected {
		t.Fatalf("clicked file row %q = %+v (found=%v), want Selected=true", secondFile, row, ok)
	}
	checkFileDiff(secondFile)

	// Load More fetches the remaining commits from the Linux server.
	mustClick(t, h, "Load More")
	h.drain()

	snap = h.app.session.Snapshot()
	if len(snap.Commits) != 1022 {
		t.Fatalf("commits after Load More = %d, want 1022", len(snap.Commits))
	}
	if snap.More {
		t.Fatal("snapshot.More still true after loading all 1022 commits")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Load More"); ok {
		t.Fatal("Load More button remained visible after loading all commits")
	}
	if !h.ui.HasText("1022 commits") {
		t.Fatalf("History header missing total count 1022; texts=%q", h.ui.Texts())
	}

	// Focus the commit list and jump to the end to verify older commits loaded
	// by Load More, then inspect both the root commit and "imported 1".
	clickCommitRow(t, h, wantHead, "linux head commit")
	h.drain()
	h.ui.Key(0, ui.KeyEnd)
	h.ui.Frame()
	h.drain()

	if !hasExactText(h.ui, "imported 1") || !hasExactText(h.ui, "initial commit") {
		t.Fatalf("oldest commits missing after Load More + End; texts=%q", h.ui.Texts())
	}
	if h.app.historyView.sel != rootHash {
		t.Fatalf("selection after End = %q, want root %q", h.app.historyView.sel, rootHash)
	}
	if !h.ui.HasText(rootHash) {
		t.Fatalf("root commit detail missing full hash %q; texts=%q", rootHash, h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "one") {
		t.Fatalf("root commit diff insertions = %q, want one", ins)
	}

	clickCommitRow(t, h, wantFirstImported, "imported 1")
	h.drain()
	if h.app.historyView.detailErr != nil {
		t.Fatalf("imported 1 detail load error: %v", h.app.historyView.detailErr)
	}
	if !h.ui.HasText(wantFirstImported) {
		t.Fatalf("imported 1 detail missing hash %q; texts=%q", wantFirstImported, h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "imported content 1") {
		t.Fatalf("imported 1 diff insertions = %q, want imported content 1", ins)
	}
}

// Local Changes on a configured Linux SSH container (sshlab):
// creates tracked and untracked changes on the server, drives single and bulk
// Stage/Unstage through the UI, verifies Discard confirmation/cancel and real
// file restoration/deletion on the server, and verifies Commit and Amend
// update HEAD and commit messages on the server.
func TestConfiguredLinuxLocalChangesStageUnstageDiscardCommitAmend(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	initialHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")

	srv.WriteFile(t, repo.Path, "a.txt", []byte("two-on-linux\n"))
	srv.WriteFile(t, repo.Path, "draft.txt", []byte("discard-me\n"))
	srv.WriteFile(t, repo.Path, "keep.txt", []byte("keep-on-linux\n"))

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if h.app.route.page != pageChanges {
		t.Fatalf("initial route page = %v, want pageChanges", h.app.route.page)
	}
	if !h.ui.HasText("Unstaged (3)") || !h.ui.HasText("Staged (0)") {
		t.Fatalf("unexpected initial change counts; texts=%q", h.ui.Texts())
	}

	// Selecting an unstaged file renders its real diff from the Linux server.
	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "two-on-linux") {
		t.Fatalf("unstaged a.txt diff insertions = %q, want two-on-linux", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "one") {
		t.Fatalf("unstaged a.txt diff deletions = %q, want one", del)
	}

	// Single Stage and Unstage verified on the server.
	mustClick(t, h, "Stage a.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Stage a.txt error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); got != "a.txt" {
		t.Fatalf("server cached diff after Stage a.txt = %q, want a.txt", got)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--name-only"); got != "" {
		t.Fatalf("server unstaged diff after Stage a.txt = %q, want empty", got)
	}
	mustClick(t, h, "Staged a.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "two-on-linux") {
		t.Fatalf("staged a.txt diff insertions = %q, want two-on-linux", ins)
	}

	mustClick(t, h, "Unstage a.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Unstage a.txt error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("server cached diff after Unstage a.txt = %q, want empty", got)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--name-only"); got != "a.txt" {
		t.Fatalf("server unstaged diff after Unstage a.txt = %q, want a.txt", got)
	}

	// Bulk Stage All and Unstage All verified on the server.
	mustClick(t, h, "Stage All")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Stage All error: %v", err)
	}
	cachedAll := strings.Fields(srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"))
	slices.Sort(cachedAll)
	if !slices.Equal(cachedAll, []string{"a.txt", "draft.txt", "keep.txt"}) {
		t.Fatalf("server cached files after Stage All = %q, want [a.txt draft.txt keep.txt]", cachedAll)
	}

	mustClick(t, h, "Unstage All")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Unstage All error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("server cached files after Unstage All = %q, want empty", got)
	}

	// Discard requires confirmation: Cancel leaves server bytes untouched,
	// Confirm Discard restores tracked a.txt and deletes untracked draft.txt.
	mustClick(t, h, "Discard a.txt")
	h.ui.Frame()
	if !h.ui.HasText("Discard changes?") {
		t.Fatalf("discard confirmation modal did not appear; texts=%q", h.ui.Texts())
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "two-on-linux\n" {
		t.Fatalf("server a.txt changed before confirmation: %q", got)
	}
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if h.ui.HasText("Discard changes?") {
		t.Fatalf("discard confirmation modal remained open after Cancel; texts=%q", h.ui.Texts())
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "two-on-linux\n" {
		t.Fatalf("server a.txt changed after Cancel: %q", got)
	}

	mustClick(t, h, "Discard a.txt")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Confirm Discard a.txt error: %v", err)
	}
	if got := string(srv.ReadFile(t, repo.Path, "a.txt")); got != "one\n" {
		t.Fatalf("server a.txt after Confirm Discard = %q, want one\\n", got)
	}
	if got := srv.RunGit(t, repo.Path, "diff", "--name-only"); got != "" {
		t.Fatalf("server unstaged diff after discarding a.txt = %q, want empty", got)
	}
	if !remotePathExists(t, srv, repo.Path, "draft.txt") || !remotePathExists(t, srv, repo.Path, "keep.txt") {
		t.Fatal("discarding tracked a.txt unexpectedly deleted unrelated untracked files")
	}

	mustClick(t, h, "Discard draft.txt")
	h.ui.Frame()
	if !h.ui.HasText("1 untracked file will be permanently deleted.") {
		t.Fatalf("discard modal missing untracked deletion warning; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Confirm Discard draft.txt error: %v", err)
	}
	if remotePathExists(t, srv, repo.Path, "draft.txt") {
		t.Fatal("server untracked draft.txt still exists after Confirm Discard")
	}
	if got := srv.RunGit(t, repo.Path, "status", "--porcelain", "--", "draft.txt"); strings.TrimSpace(got) != "" {
		t.Fatalf("server git status for draft.txt after discard = %q, want empty", got)
	}
	if !remotePathExists(t, srv, repo.Path, "keep.txt") {
		t.Fatal("discarding draft.txt unexpectedly removed keep.txt")
	}

	// Commit staged file through the real commit box and Commit button.
	mustClick(t, h, "Stage keep.txt")
	h.drain()
	typeCommitMessage(t, h, "linux ui commit")
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Commit error: %v", err)
	}
	commitHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	if commitHead == initialHead {
		t.Fatal("server HEAD did not advance after UI Commit")
	}
	if got := srv.RunGit(t, repo.Path, "log", "-1", "--format=%s"); got != "linux ui commit" {
		t.Fatalf("server commit subject = %q, want linux ui commit", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("server commit count after Commit = %q, want 2", got)
	}
	if got := srv.RunGit(t, repo.Path, "status", "--porcelain"); strings.TrimSpace(got) != "" {
		t.Fatalf("server status after Commit = %q, want clean", got)
	}
	if !h.ui.HasText("Working tree is clean") {
		t.Fatalf("Local Changes not clean after Commit; texts=%q", h.ui.Texts())
	}

	// Amend through the real Amend checkbox + Cmd+Enter shortcut.
	srv.WriteFile(t, repo.Path, "keep.txt", []byte("keep-on-linux\namended-on-linux\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage keep.txt")
	h.drain()

	selectAmend(t, h)
	h.drain()
	if got := h.app.changesView.message; got != "linux ui commit" {
		t.Fatalf("Amend prefill message = %q, want linux ui commit", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "HEAD"); got != commitHead {
		t.Fatalf("ticking Amend moved server HEAD prematurely: %q != %q", got, commitHead)
	}

	typeCommitMessage(t, h, "linux ui commit amended\n\namended body on container")
	h.ui.Key(ui.Cmd, ui.KeyEnter)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Amend commit error: %v", err)
	}
	amendedHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	if amendedHead == commitHead {
		t.Fatal("server HEAD did not change after UI Amend")
	}
	if got := srv.RunGit(t, repo.Path, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("server commit count after Amend = %q, want 2 (amended in place)", got)
	}
	if got := srv.RunGit(t, repo.Path, "log", "-1", "--format=%s"); got != "linux ui commit amended" {
		t.Fatalf("server amended subject = %q, want linux ui commit amended", got)
	}
	if body := srv.RunGit(t, repo.Path, "log", "-1", "--format=%B"); !strings.Contains(body, "amended body on container") {
		t.Fatalf("server amended message missing body: %q", body)
	}
	if got := srv.RunGit(t, repo.Path, "show", "HEAD:keep.txt"); !strings.Contains(got, "amended-on-linux") {
		t.Fatalf("server HEAD:keep.txt after Amend = %q, want amended-on-linux", got)
	}
	if got := h.app.session.Snapshot().Status.Head; got != amendedHead {
		t.Fatalf("snapshot HEAD after Amend = %q, want %q", got, amendedHead)
	}
}

// Refs sidebar on a configured Linux SSH container (sshlab):
// creates a branch through the New Branch dialog, double-clicks branches to
// check them out while verifying server HEAD changes, confirms deletion of a
// merged branch, and verifies the unmerged warning + explicit force agreement
// deletes the unmerged ref on the server.
func TestConfiguredLinuxRefsSidebarCreateCheckoutAndDelete(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	mainHash := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")

	// Prepare merged branch, unmerged branch with a unique commit, tag, and bare remote.
	srv.RunGit(t, repo.Path, "branch", "merged-branch")
	srv.RunGit(t, repo.Path, "tag", "v1.0")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "unmerged-branch")
	srv.WriteFile(t, repo.Path, "unmerged.txt", []byte("unmerged on linux\n"))
	srv.RunGit(t, repo.Path, "add", "--", "unmerged.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "unmerged linux work")
	unmergedHash := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")

	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-q", "-u", "origin", "main")

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, want := range []string{"main", "merged-branch", "unmerged-branch", "origin", "origin/main", "v1.0"} {
		if _, ok := h.ui.Find(want); !ok {
			t.Fatalf("sidebar missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("initial current branch main = %+v (found=%v), want Selected=true", row, ok)
	}

	// Create a new branch without immediate checkout, then double-click checkout.
	mustClick(t, h, "New Branch")
	h.ui.Frame()
	h.ui.Type("created-branch")
	h.ui.Frame()
	mustClick(t, h, "Checkout after create")
	h.ui.Frame()
	mustClick(t, h, "Create Branch")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Create Branch error: %v", err)
	}
	if !remoteBranchExists(t, srv, repo.Path, "created-branch") {
		t.Fatal("created-branch ref missing on server after Create Branch")
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("server HEAD branch after create without checkout = %q, want main", got)
	}
	if _, ok := h.ui.Find("created-branch"); !ok {
		t.Fatalf("created-branch missing from sidebar; texts=%q", h.ui.Texts())
	}

	// Double-click checkout created-branch, then unmerged-branch (distinct commit
	// SHA), then back to main, checking server HEAD each time.
	doubleClickCheckout(t, h, "created-branch")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout created-branch error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "created-branch" {
		t.Fatalf("server HEAD branch after double-click created-branch = %q, want created-branch", got)
	}
	if row, ok := findAccessNode(t, h.ui, "created-branch"); !ok || !row.Selected {
		t.Fatalf("created-branch row after checkout = %+v (found=%v), want Selected=true", row, ok)
	}

	doubleClickCheckout(t, h, "unmerged-branch")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout unmerged-branch error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "unmerged-branch" {
		t.Fatalf("server HEAD branch after double-click unmerged-branch = %q, want unmerged-branch", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "HEAD"); got != unmergedHash {
		t.Fatalf("server HEAD SHA after checkout unmerged-branch = %q, want %q", got, unmergedHash)
	}

	doubleClickCheckout(t, h, "main")
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("checkout main error: %v", err)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("server HEAD branch after checkout main = %q, want main", got)
	}
	if got := srv.RunGit(t, repo.Path, "rev-parse", "HEAD"); got != mainHash {
		t.Fatalf("server HEAD SHA after checkout main = %q, want %q", got, mainHash)
	}

	// Delete merged branch: confirm dialog appears, Cancel preserves ref,
	// Confirm Delete removes ref on server.
	mustClick(t, h, "Delete branch merged-branch")
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("fully merged") {
		t.Fatalf("merged branch confirmation text missing; texts=%q", h.ui.Texts())
	}
	if !remoteBranchExists(t, srv, repo.Path, "merged-branch") {
		t.Fatal("merged-branch deleted before confirmation")
	}
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if !remoteBranchExists(t, srv, repo.Path, "merged-branch") {
		t.Fatal("merged-branch deleted after Cancel")
	}

	mustClick(t, h, "Delete branch merged-branch")
	h.ui.Frame()
	drainDeleteCheck(t, h)
	mustClick(t, h, "Confirm Delete")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Confirm Delete merged-branch error: %v", err)
	}
	if remoteBranchExists(t, srv, repo.Path, "merged-branch") {
		t.Fatal("merged-branch ref still exists on server after Confirm Delete")
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "branch", "--list", "merged-branch")); out != "" {
		t.Fatalf("server git branch --list merged-branch = %q, want empty", out)
	}
	if _, ok := h.ui.Find("merged-branch"); ok {
		t.Fatal("merged-branch remained visible in sidebar after delete")
	}

	// Delete unmerged branch: warning shown, Force Delete disabled until
	// explicit agreement, then ref deleted on server.
	mustClick(t, h, "Delete branch unmerged-branch")
	h.ui.Frame()
	drainDeleteCheck(t, h)
	if !h.ui.HasText("not merged") {
		t.Fatalf("unmerged branch warning missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); !ok || !btn.Disabled {
		t.Fatalf("Force Delete before agreement = %+v (found=%v), want disabled", btn, ok)
	}
	_ = h.ui.Click("Force Delete")
	h.drain()
	if !remoteBranchExists(t, srv, repo.Path, "unmerged-branch") {
		t.Fatal("unmerged-branch deleted while Force Delete was disabled")
	}
	mustClick(t, h, "I understand, force delete this branch")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Force Delete"); !ok || btn.Disabled {
		t.Fatalf("Force Delete after agreement = %+v (found=%v), want enabled", btn, ok)
	}
	mustClick(t, h, "Force Delete")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Force Delete unmerged-branch error: %v", err)
	}
	if remoteBranchExists(t, srv, repo.Path, "unmerged-branch") {
		t.Fatal("unmerged-branch ref still exists on server after Force Delete")
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "branch", "--list", "unmerged-branch")); out != "" {
		t.Fatalf("server git branch --list unmerged-branch = %q, want empty", out)
	}
	if _, ok := h.ui.Find("unmerged-branch"); ok {
		t.Fatal("unmerged-branch remained visible in sidebar after Force Delete")
	}
	if !remoteBranchExists(t, srv, repo.Path, "created-branch") || !remoteBranchExists(t, srv, repo.Path, "main") {
		t.Fatal("deleting unmerged-branch unexpectedly removed main or created-branch")
	}
}

// Empty repository on a configured Linux SSH container (sshlab):
// opens a freshly git-init repository with an unborn HEAD through the real
// Repository Switcher without load errors on Local Changes or History, stages
// the initial file, creates the first commit through the UI, and verifies the
// born HEAD and commit on the server.
func TestConfiguredLinuxEmptyRepoFirstCommit(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	dir := initSSHEmptyRepo(t, srv, "empty-linux")
	repo := sshStoreRepo(t, srv, dir)

	headState := strings.TrimSpace(srv.RunShell(t,
		"GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "+git.ShellQuote(dir)+
			" rev-parse --verify --quiet HEAD >/dev/null 2>&1 && printf born || printf unborn"))
	if headState != "unborn" {
		t.Fatalf("initial server HEAD state = %q, want unborn", headState)
	}

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	emptySnapshotLoaded(t, h)
	if h.app.session.Meta().Kind != store.SSH {
		t.Fatalf("session kind = %q, want SSH", h.app.session.Meta().Kind)
	}
	snap := h.app.session.Snapshot()
	if snap.Status.Head != "" || snap.Status.Branch != "main" {
		t.Fatalf("empty Linux status = head %q branch %q, want empty/main", snap.Status.Head, snap.Status.Branch)
	}
	if len(snap.Commits) != 0 {
		t.Fatalf("empty Linux len(Commits) = %d, want 0", len(snap.Commits))
	}
	if !h.ui.HasText("No branches") {
		t.Fatalf("sidebar missing No branches on empty Linux repo; texts=%q", h.ui.Texts())
	}
	if !hasAccessRow(h, "Unstaged hello.txt") {
		t.Fatalf("hello.txt missing from Local Changes on empty Linux repo; texts=%q", h.ui.Texts())
	}

	// History also renders cleanly with no error on the unborn repository.
	mustClick(t, h, "History")
	h.ui.Frame()
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") {
		t.Fatalf("History showed error on empty Linux repo; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("No commits yet") {
		t.Fatalf("History missing empty state on empty Linux repo; texts=%q", h.ui.Texts())
	}

	// Return to Local Changes, stage hello.txt, and create the first commit.
	mustClick(t, h, "Changes")
	h.ui.Frame()
	mustClick(t, h, "Stage hello.txt")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Stage hello.txt error: %v", err)
	}
	if got := strings.TrimSpace(srv.RunGit(t, dir, "status", "--porcelain")); got != "A  hello.txt" {
		t.Fatalf("server status after staging hello.txt = %q, want %q", got, "A  hello.txt")
	}

	typeCommitMessage(t, h, "first linux commit")
	mustClick(t, h, "Commit")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("first commit ActionErr: %v", err)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("post-commit LoadErr: %v", err)
	}
	wantHead := srv.RunGit(t, dir, "rev-parse", "HEAD")
	if len(wantHead) != 40 {
		t.Fatalf("server HEAD after first commit = %q, want 40-char SHA", wantHead)
	}
	if got := srv.RunGit(t, dir, "log", "-1", "--format=%s"); got != "first linux commit" {
		t.Fatalf("server first commit subject = %q, want first linux commit", got)
	}
	if got := srv.RunGit(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("server commit count = %q, want 1", got)
	}
	if got := srv.RunGit(t, dir, "show", "HEAD:hello.txt"); got != "hello" {
		t.Fatalf("server HEAD:hello.txt = %q, want hello", got)
	}
	if !h.ui.HasText("Working tree is clean") {
		t.Fatalf("Local Changes not clean after first commit; texts=%q", h.ui.Texts())
	}
	if row, ok := findAccessNode(t, h.ui, "main"); !ok || !row.Selected {
		t.Fatalf("sidebar main branch after first commit = %+v (found=%v), want Selected=true", row, ok)
	}

	mustClick(t, h, "History")
	h.ui.Frame()
	if h.ui.HasText("Failed to load repository snapshot") || h.ui.HasText("bad revision") || h.ui.HasText("No commits yet") {
		t.Fatalf("History unexpected state after first commit; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleListItem, commitRowLabel(wantHead, "first linux commit")); !ok {
		t.Fatalf("History missing first commit row %q; texts=%q", commitRowLabel(wantHead, "first linux commit"), h.ui.Texts())
	}
	if !h.ui.HasText("HEAD → main") {
		t.Fatalf("History missing HEAD → main badge after first commit; texts=%q", h.ui.Texts())
	}
}
