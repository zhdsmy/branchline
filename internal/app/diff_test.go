package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

type diffPanelHarness struct {
	t       *testing.T
	session *repoSession
	panel   *diffPanel
	scope   string
	queue   *uiQueue
	ui      *ui.Tester
}

func newDiffPanelHarness(t *testing.T, meta store.Repo, scope string) *diffPanelHarness {
	t.Helper()
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)

	s := newRepoSession(q.post, q.spawn, defaultOpenGitRepo)
	panel := &diffPanel{}
	var darkKnown, lastDark bool
	themeGen := 0

	view := func(c *ui.Context) {
		dark := c.Theme().Dark
		if !darkKnown {
			darkKnown = true
			lastDark = dark
		} else if lastDark != dark {
			lastDark = dark
			themeGen++
		}
		p := applyPalette(c, themeGen)
		ui.Column(c).Fill().Background(p.bg).Children(func() {
			panel.View(c, s, scope, p)
		})
	}

	h := &diffPanelHarness{
		t:       t,
		session: s,
		panel:   panel,
		scope:   scope,
		queue:   q,
		ui:      ui.NewTester(view, 1000, 640),
	}
	s.Open(meta)
	h.drain()
	return h
}

func (h *diffPanelHarness) drain() {
	h.t.Helper()
	h.queue.Drain(h.t)
	h.ui.Frame()
}

func diffAccessLines(tt *ui.Tester, role ui.Role) []string {
	var lines []string
	for _, a := range tt.AccessAll() {
		if a.Role == role {
			lines = append(lines, a.Name)
		}
	}
	return lines
}

// diffPanel loads and renders diffs from a real Git repository for all three
// request kinds: a commit file against its parent (and root commit), unstaged
// working-tree changes (tracked and untracked), and staged index changes.
func TestDiffPanelLoadsCommitUnstagedAndStagedFromRealRepo(t *testing.T) {
	repo := newGitRepo(t, "diff-repo")
	rootHash := repo.CommitFile("root.go", "package main\n\nfunc root() string { return \"init\" }\n", "root commit")
	secondHash := repo.CommitFile("root.go", "package main\n\nfunc root() string { return \"committed\" }\n", "update root")

	// Staged modification in index.
	repo.WriteFile("staged.go", "package main\n\nconst stagedValue = 42\n")
	repo.Git("add", "staged.go")

	// Unstaged modification to tracked file and a new untracked file.
	repo.WriteFile("root.go", "package main\n\nfunc root() string { return \"unstaged-edit\" }\n")
	repo.WriteFile("untracked.txt", "brand new line\n")

	h := newDiffPanelHarness(t, repo.StoreRepo(), "test-diff")

	// 1. Commit file against parent.
	commitWithParent := git.Commit{Hash: secondHash, Parents: []string{rootHash}}
	if !h.panel.Load(h.session, h.scope, commitFileDiffRequest(commitWithParent, git.FileChange{Path: "root.go", Status: 'M'})) {
		t.Fatal("expected commit diff load to start")
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, `func root() string { return "committed" }`) {
		t.Fatalf("commit diff insertions = %q, missing committed line", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, `func root() string { return "init" }`) {
		t.Fatalf("commit diff deletions = %q, missing parent line", del)
	}

	// 1b. Root commit file (no parent -> compared against empty tree).
	rootCommit := git.Commit{Hash: rootHash}
	if !h.panel.Load(h.session, h.scope, commitFileDiffRequest(rootCommit, git.FileChange{Path: "root.go", Status: 'A'})) {
		t.Fatal("expected root commit diff load to start")
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, `func root() string { return "init" }`) {
		t.Fatalf("root commit diff insertions = %q, missing initial line", ins)
	}

	// 2. Unstaged tracked modification.
	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "root.go", Status: 'M'}, "rev-1")) {
		t.Fatal("expected unstaged tracked diff load to start")
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, `func root() string { return "unstaged-edit" }`) {
		t.Fatalf("unstaged diff insertions = %q, missing working-tree edit", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, `func root() string { return "committed" }`) {
		t.Fatalf("unstaged diff deletions = %q, missing index line", del)
	}

	// 2b. Unstaged untracked file.
	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "untracked.txt", Status: '?'}, "rev-1")) {
		t.Fatal("expected untracked diff load to start")
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "brand new line") {
		t.Fatalf("untracked diff insertions = %q, missing untracked line", ins)
	}

	// 3. Staged index file.
	if !h.panel.Load(h.session, h.scope, stagedDiffRequest(git.FileChange{Path: "staged.go", Status: 'A', Staged: true}, "rev-1")) {
		t.Fatal("expected staged diff load to start")
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "const stagedValue = 42") {
		t.Fatalf("staged diff insertions = %q, missing staged line", ins)
	}
	if !h.ui.HasText("Staged") {
		t.Fatalf("expected Staged badge in diff header; texts=%q", h.ui.Texts())
	}

	// 4. Deleted file diff matches via OldName when NewName is empty.
	repo.CommitFile("obsolete.txt", "line-to-remove\n", "add obsolete")
	if err := os.Remove(filepath.Join(repo.Path(), "obsolete.txt")); err != nil {
		t.Fatalf("remove obsolete.txt: %v", err)
	}
	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "obsolete.txt", Status: 'D'}, "rev-2")) {
		t.Fatal("expected deleted file diff load to start")
	}
	h.drain()
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "line-to-remove") {
		t.Fatalf("deleted file diff deletions = %q, missing removed line", del)
	}
	if h.panel.patch.OldName != "obsolete.txt" || h.panel.patch.NewName != "" {
		t.Fatalf("deleted file patch names = (old=%q, new=%q), want (obsolete.txt, \"\")", h.panel.patch.OldName, h.panel.patch.NewName)
	}
}

// Rapidly changing file selection discards late results from an earlier
// selection (deterministically ordered with uiQueue), and clearing selection
// also prevents an in-flight diff from reappearing.
func TestDiffPanelRapidSelectionDiscardsStaleResult(t *testing.T) {
	repo := newGitRepo(t, "race-diff-repo")
	repo.CommitFile("slow.txt", "slow-old\n", "add slow")
	repo.CommitFile("fast.txt", "fast-old\n", "add fast")
	repo.WriteFile("slow.txt", "slow-new-stale\n")
	repo.WriteFile("fast.txt", "fast-new-current\n")

	h := newDiffPanelHarness(t, repo.StoreRepo(), "changes-diff")

	// Hold the first request's worker so the second request completes strictly earlier.
	h.queue.PauseWorkers()
	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "slow.txt", Status: 'M'}, "1")) {
		t.Fatal("expected first diff request to start")
	}
	slowWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "fast.txt", Status: 'M'}, "1")) {
		t.Fatal("expected second diff request to start")
	}
	h.queue.RunNext(t)
	h.ui.Frame()

	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "fast-new-current") {
		t.Fatalf("fast diff not displayed before slow completed: insertions=%q", ins)
	}

	// Unblock the superseded slow worker and drain its late callback.
	h.queue.ReleaseWorker(slowWorker)
	h.drain()

	if h.panel.req.File.Path != "fast.txt" {
		t.Fatalf("active diff path = %q, want fast.txt", h.panel.req.File.Path)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "fast-new-current") || slices.Contains(ins, "slow-new-stale") {
		t.Fatalf("stale diff leaked after rapid switch: insertions=%q", ins)
	}

	// Clearing selection while a new diff is in flight also discards its completion.
	h.queue.PauseWorkers()
	if !h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "slow.txt", Status: 'M'}, "2")) {
		t.Fatal("expected third diff request to start")
	}
	clearedWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	h.panel.Clear(h.session, h.scope)
	h.queue.ReleaseWorker(clearedWorker)
	h.drain()

	if !h.ui.HasText("No File Selected") {
		t.Fatalf("cleared panel was overwritten by late diff; texts=%q", h.ui.Texts())
	}
}

// diffPanel defaults to Split layout and toggles between Split and Unified via
// the accessible header buttons while preserving loaded diff content.
func TestDiffPanelSwitchesSplitAndUnifiedLayout(t *testing.T) {
	repo := newGitRepo(t, "layout-diff-repo")
	repo.CommitFile("app.go", "package main\n\nvar version = 1\n", "initial")
	repo.WriteFile("app.go", "package main\n\nvar version = 2\n")

	h := newDiffPanelHarness(t, repo.StoreRepo(), "history-diff")
	if h.panel.Layout() != editor.DiffSplit {
		t.Fatalf("default layout = %v, want DiffSplit", h.panel.Layout())
	}

	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "app.go", Status: 'M'}, "1"))
	h.drain()

	// Default Split shows the divider splitter between Old and New panes.
	if splitBtn, ok := h.ui.AccessByRole(ui.RoleButton, "Split"); !ok || !splitBtn.Checked {
		t.Fatalf("Split toggle state = %+v (found=%v), want Checked=true", splitBtn, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleSplitter, "Divider"); !ok {
		t.Fatal("expected Split diff view to expose a Divider splitter")
	}

	// Clicking Unified switches layout and removes the side-by-side splitter.
	if err := h.ui.Click("Unified"); err != nil {
		t.Fatalf("click Unified button: %v", err)
	}
	h.ui.Frame()
	if h.panel.Layout() != editor.DiffUnified {
		t.Fatalf("layout after clicking Unified = %v, want DiffUnified", h.panel.Layout())
	}
	if uniBtn, ok := h.ui.AccessByRole(ui.RoleButton, "Unified"); !ok || !uniBtn.Checked {
		t.Fatalf("Unified toggle state = %+v (found=%v), want Checked=true", uniBtn, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleSplitter, "Divider"); ok {
		t.Fatal("Unified diff view unexpectedly still shows a Split Divider")
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var version = 2") {
		t.Fatalf("Unified diff missing insertion line: %q", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "var version = 1") {
		t.Fatalf("Unified diff missing deletion line: %q", del)
	}

	// Clicking Split restores side-by-side layout.
	if err := h.ui.Click("Split"); err != nil {
		t.Fatalf("click Split button: %v", err)
	}
	h.ui.Frame()
	if h.panel.Layout() != editor.DiffSplit {
		t.Fatalf("layout after clicking Split = %v, want DiffSplit", h.panel.Layout())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleSplitter, "Divider"); !ok {
		t.Fatal("expected Split Divider to reappear after switching back to Split")
	}
}

// diffPanel shows explicit placeholders for binary files, oversized diffs,
// and Git read errors, and clears stale diffs when switching repositories.
func TestDiffPanelPlaceholdersBinaryTooLargeAndError(t *testing.T) {
	repo := newGitRepo(t, "placeholder-diff-repo")
	// Real binary file with NUL bytes.
	binPath := filepath.Join(repo.Path(), "logo.bin")
	if err := os.WriteFile(binPath, []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write binary v1: %v", err)
	}
	repo.Git("add", "logo.bin")
	repo.Git("commit", "-m", "add binary")
	if err := os.WriteFile(binPath, []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xee}, 0o644); err != nil {
		t.Fatalf("write binary v2: %v", err)
	}

	// Oversized file exceeding defaultMaxDiffBytes (2 MiB).
	repo.CommitFile("huge.txt", "small\n", "add huge placeholder")
	repo.WriteFile("huge.txt", strings.Repeat("0123456789abcdef\n", 140000))

	h := newDiffPanelHarness(t, repo.StoreRepo(), "changes-diff")

	// 1. Binary placeholder.
	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "logo.bin", Status: 'M'}, "1"))
	h.drain()
	if !h.ui.HasText("Binary file") {
		t.Fatalf("expected Binary file placeholder; texts=%q", h.ui.Texts())
	}

	// 2. Oversized diff placeholder.
	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "huge.txt", Status: 'M'}, "1"))
	h.drain()
	if !h.ui.HasText("Diff too large to display") {
		t.Fatalf("expected oversized diff placeholder; texts=%q", h.ui.Texts())
	}

	// 3. Git read error placeholder with Retry action.
	badCommit := git.Commit{Hash: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
	h.panel.Load(h.session, h.scope, commitFileDiffRequest(badCommit, git.FileChange{Path: "missing.go", Status: 'M'}))
	h.drain()
	if !h.ui.HasText("Failed to load diff") {
		t.Fatalf("expected error placeholder; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Retry Diff"); !ok {
		t.Fatal("expected Retry Diff button on diff load error")
	}

	// Clicking Retry Diff initiates a fresh background read for the same request.
	h.queue.PauseWorkers()
	if err := h.ui.Click("Retry Diff"); err != nil {
		t.Fatalf("click Retry Diff button: %v", err)
	}
	h.ui.Frame()
	retryWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	if !h.panel.loading {
		t.Fatal("expected diff panel to enter loading state after clicking Retry Diff")
	}
	if !h.ui.HasText("Loading diff…") {
		t.Fatalf("expected loading placeholder after clicking Retry Diff; texts=%q", h.ui.Texts())
	}
	h.queue.ReleaseWorker(retryWorker)
	h.drain()
	if !h.ui.HasText("Failed to load diff") {
		t.Fatalf("expected error placeholder after retry failed; texts=%q", h.ui.Texts())
	}

	// 4. Switching repository clears stale panel state on the next frame.
	other := newGitRepo(t, "other-diff-repo")
	other.CommitFile("x.txt", "x\n", "init other")
	h.session.Open(other.StoreRepo())
	h.drain()
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("expected diff panel to reset after switching repo; texts=%q", h.ui.Texts())
	}
}
