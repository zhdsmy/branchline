package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// whitespaceContent builds file content where filler lines keep the
// whitespace-only line and the real change in separate diff hunks.
func whitespaceContent(wsLine, realLine string) string {
	var b strings.Builder
	b.WriteString("header\n")
	for i := 1; i <= 8; i++ {
		b.WriteString("filler a\n")
	}
	b.WriteString(wsLine + "\n")
	for i := 1; i <= 8; i++ {
		b.WriteString("filler b\n")
	}
	b.WriteString(realLine + "\n")
	return b.String()
}

func writeWhitespaceFixture(f *gitRepoFixture, file, wsLine, realLine string) {
	f.WriteFile(file, whitespaceContent(wsLine, realLine))
}

func assertWhitespaceToggleEffect(t *testing.T, h *testHarness) {
	t.Helper()
	// These tests check the whitespace toggle, not the file coverage: the
	// entire-file default spreads the fixture's two changes beyond the
	// viewport, so the fixture pins coverage to the changes and their
	// context through the header's own Show Entire File toggle before the
	// whitespace assertions run.
	if err := h.ui.Click("Show Entire File"); err != nil {
		t.Fatalf("turn off Show Entire File for the whitespace fixture: %v", err)
	}
	h.drain()

	ins := diffAccessLines(h.ui, ui.RoleInsertion)
	del := diffAccessLines(h.ui, ui.RoleDeletion)
	if !slices.Contains(ins, "var x  =  1") {
		t.Fatalf("diff before toggle missing whitespace-only insertion: insertions=%q", ins)
	}
	if !slices.Contains(del, "var x = 1") {
		t.Fatalf("diff before toggle missing whitespace-only deletion: deletions=%q", del)
	}
	if !slices.Contains(ins, "var y = 2") {
		t.Fatalf("diff before toggle missing real insertion: insertions=%q", ins)
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok {
		t.Fatalf("Ignore Whitespace toggle missing; texts=%q", h.ui.Texts())
	}
	if btn.Checked {
		t.Fatalf("Ignore Whitespace toggle starts checked: %+v", btn)
	}

	mustClick(t, h, "Ignore Whitespace")
	h.drain()

	ins = diffAccessLines(h.ui, ui.RoleInsertion)
	del = diffAccessLines(h.ui, ui.RoleDeletion)
	if slices.Contains(ins, "var x  =  1") || slices.Contains(del, "var x = 1") {
		t.Fatalf("whitespace-only lines survive ignore-whitespace: insertions=%q deletions=%q", ins, del)
	}
	if !slices.Contains(ins, "var y = 2") {
		t.Fatalf("real change lost with ignore-whitespace: insertions=%q", ins)
	}
	if !slices.Contains(del, "var y = 1") {
		t.Fatalf("real change lost with ignore-whitespace: deletions=%q", del)
	}
	btn, ok = h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok || !btn.Checked {
		t.Fatalf("Ignore Whitespace toggle after click = %+v (found=%v), want checked", btn, ok)
	}
}

// The ignore-whitespace toggle reloads a commit file diff of the History page
// with whitespace changes dropped and real changes kept.
func TestDiffIgnoreWhitespaceCommitFile(t *testing.T) {
	repo := newGitRepo(t, "iw-commit")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "ws and real change")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "ws and real change")
	h.drain()

	assertWhitespaceToggleEffect(t, h)
}

// The ignore-whitespace toggle reloads an unstaged working-tree diff with
// whitespace changes dropped and real changes kept.
func TestDiffIgnoreWhitespaceUnstaged(t *testing.T) {
	repo := newGitRepo(t, "iw-unstaged")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged code.go")
	h.drain()

	assertWhitespaceToggleEffect(t, h)
}

// The ignore-whitespace toggle reloads a staged index diff with whitespace
// changes dropped and real changes kept.
func TestDiffIgnoreWhitespaceStaged(t *testing.T) {
	repo := newGitRepo(t, "iw-staged")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")
	repo.Git("add", "code.go")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Staged code.go")
	h.drain()

	assertWhitespaceToggleEffect(t, h)
}

// A file whose diff consists only of whitespace changes shows the panel's
// empty-diff state once whitespace is ignored.
func TestDiffIgnoreWhitespaceOnlyDiffShowsEmpty(t *testing.T) {
	repo := newGitRepo(t, "iw-empty")
	repo.CommitFile("notes.txt", "first line\n", "base")
	h := newDiffPanelHarness(t, repo.StoreRepo(), "iw-empty-diff")

	repo.WriteFile("notes.txt", "first  line\n")
	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "notes.txt", Status: 'M'}, "rev-1"))
	h.drain()
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "first line") {
		t.Fatalf("diff before toggle missing whitespace-only deletion: deletions=%q", del)
	}

	if err := h.ui.Click("Ignore Whitespace"); err != nil {
		t.Fatalf("click Ignore Whitespace: %v", err)
	}
	h.drain()
	if !h.ui.HasText("No diff to display") {
		t.Fatalf("whitespace-only diff did not show empty state with ignore-whitespace; texts=%q", h.ui.Texts())
	}
}

// The toggle is global: switching it on in History keeps it on in Local
// Changes, and newApp restores the saved choice from the store.
func TestDiffIgnoreWhitespacePersistsAcrossPagesAndRestores(t *testing.T) {
	repo := newGitRepo(t, "iw-persist")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "ws and real change")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	if h.app.session.IgnoreSpace() {
		t.Fatal("fresh app session starts with ignore-whitespace on")
	}
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "ws and real change")
	h.drain()

	mustClick(t, h, "Ignore Whitespace")
	h.drain()
	if !h.app.session.IgnoreSpace() {
		t.Fatal("session ignore-whitespace off after clicking the toggle")
	}
	if !h.store.Settings().IgnoreSpace {
		t.Fatal("store did not persist ignore-whitespace after clicking the toggle")
	}

	// The Local Changes page shares the same global choice.
	mustClick(t, h, "Changes")
	h.ui.Frame()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok {
		t.Fatalf("Ignore Whitespace toggle missing on Local Changes; texts=%q", h.ui.Texts())
	}
	if !btn.Checked {
		t.Fatalf("Ignore Whitespace toggle on Local Changes = %+v, want checked", btn)
	}
}

// A new app instance restores the persisted ignore-whitespace choice.
func TestDiffIgnoreWhitespaceRestoredFromStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "repositories.json")
	s1, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	settings := s1.Settings()
	settings.IgnoreSpace = true
	if err := s1.SetSettings(settings); err != nil {
		t.Fatalf("seed ignore-whitespace setting: %v", err)
	}

	// Reopening the store proves the choice survived on disk; the app built
	// from it starts with the toggle on.
	s2, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	a := newApp(s2)
	if !a.session.IgnoreSpace() {
		t.Fatal("new app did not restore ignore-whitespace from the store")
	}
}

// When the settings save fails, the toggle stays off, the diff is not
// reloaded, and the failure is shown as a toast.
func TestDiffIgnoreWhitespaceSaveFailure(t *testing.T) {
	repo := newGitRepo(t, "iw-savefail")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")

	// Build the harness by hand so the store's directory can be made
	// read-only after the initial snapshot: SetSettings then fails on write.
	storeDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chmod(storeDir, 0o755); err != nil {
			t.Errorf("restore store dir permissions: %v", err)
		}
	})
	s, err := store.Open(filepath.Join(storeDir, "repositories.json"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{
		t:     t,
		app:   a,
		store: s,
		ui:    ui.NewTester(a.view, 1200, 800),
		queue: q,
	}
	h.ui.Frame()
	h.openRepo(repo.StoreRepo())
	h.drain()

	mustClick(t, h, "Unstaged code.go")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var x  =  1") {
		t.Fatalf("diff before failed save missing whitespace-only insertion: insertions=%q", ins)
	}

	if err := os.Chmod(storeDir, 0o555); err != nil {
		t.Fatalf("make store dir read-only: %v", err)
	}
	mustClick(t, h, "Ignore Whitespace")

	if h.app.session.IgnoreSpace() {
		t.Fatal("session ignore-whitespace turned on although saving failed")
	}
	if h.app.changesView.diff.loading {
		t.Fatal("diff reload started although saving the toggle failed")
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok {
		t.Fatalf("Ignore Whitespace toggle missing after failed save; texts=%q", h.ui.Texts())
	}
	if btn.Checked {
		t.Fatalf("Ignore Whitespace toggle checked after failed save: %+v", btn)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var x  =  1") {
		t.Fatalf("whitespace-only insertion missing after failed save: insertions=%q", ins)
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("save failure toast missing; texts=%q", h.ui.Texts())
	}
}

// The ignore-whitespace toggle takes effect on a repository hosted behind a
// real loopback SSH server.
func TestDiffIgnoreWhitespaceLoopbackSSH(t *testing.T) {
	sshRepo, srv := loopbackSSHRepo(t)
	dir := sshRepo.Path

	writeRemote := func(wsLine, realLine string) {
		t.Helper()
		srv.WriteFile(t, dir, "code.go", []byte(whitespaceContent(wsLine, realLine)))
	}
	writeRemote("var x = 1", "var y = 1")
	srv.RunGit(t, dir, "add", "code.go")
	srv.RunGit(t, dir, "commit", "-m", "base")
	writeRemote("var x  =  1", "var y = 2")
	srv.RunGit(t, dir, "add", "code.go")
	srv.RunGit(t, dir, "commit", "-m", "ws and real change")
	head := strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))

	h := newTestApp(t, sshRepo)
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(sshRepo.Name); err != nil {
		t.Fatalf("select SSH repo %q in Repository Switcher: %v", sshRepo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, head, "ws and real change")
	h.drain()

	assertWhitespaceToggleEffect(t, h)
}

// At the 880-point minimum window width the ignore-whitespace toggle stays
// inside the diff header: it does not overlap the Split/Unified control and
// the header controls end inside the window.
func TestDiffIgnoreWhitespaceHeaderFitsAtMinWidth(t *testing.T) {
	repo := newGitRepo(t, "iw-layout")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "base")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged code.go")
	h.drain()

	h.ui.SetSize(880, 580)
	h.ui.Frame()

	iw, ok := h.ui.AccessByRole(ui.RoleButton, "Ignore Whitespace")
	if !ok {
		t.Fatalf("Ignore Whitespace toggle missing at minimum width; texts=%q", h.ui.Texts())
	}
	split, ok := h.ui.AccessByRole(ui.RoleButton, "Split")
	if !ok {
		t.Fatalf("Split toggle missing at minimum width; texts=%q", h.ui.Texts())
	}
	unified, ok := h.ui.AccessByRole(ui.RoleButton, "Unified")
	if !ok {
		t.Fatalf("Unified toggle missing at minimum width; texts=%q", h.ui.Texts())
	}
	if iwRight := iw.Bounds.X + iw.Bounds.W; iwRight > split.Bounds.X {
		t.Fatalf("Ignore Whitespace right edge %.2f overlaps Split at %.2f: the toggle overflows into the layout control", iwRight, split.Bounds.X)
	}
	for name, b := range map[string]ui.Rect{"Ignore Whitespace": iw.Bounds, "Unified": unified.Bounds} {
		if right := b.X + b.W; right > 880+0.5 {
			t.Fatalf("%s right edge %.2f beyond the 880-point window: the toggle overflows the header", name, right)
		}
	}
}
