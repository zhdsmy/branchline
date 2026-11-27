package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// writeUnstagedFiles drops n untracked files file-0000.txt .. file-<n-1>.txt
// into the repository's working tree.
func writeUnstagedFiles(t *testing.T, repo *gitRepoFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		repo.WriteFile(fmt.Sprintf("file-%04d.txt", i), fmt.Sprintf("content %d\n", i))
	}
}

// scrollChangesListToEnd moves the virtual file list of Local Changes to its
// end and renders enough frames for the last rows to be built.
func scrollChangesListToEnd(h *testHarness) {
	h.t.Helper()
	h.app.changesView.list.ScrollToEnd()
	h.ui.Frame()
	h.ui.Frame()
}

func TestChangesListScrollsToLastFileAndStagesIt(t *testing.T) {
	// With 2000 unstaged files the list builds only the rows in view. Scrolling
	// to the end must reveal the last file, clicking it loads its diff, and its
	// Stage button moves it into the index for real.
	repo := newGitRepo(t, "changeslist-scroll")
	repo.CommitFile("base.txt", "base\n", "initial")
	writeUnstagedFiles(t, repo, 2000)

	h := openChangesHarness(t, repo)

	if _, ok := h.ui.Find("file-1999.txt"); ok {
		t.Fatal("the last file row must not be built before scrolling to it")
	}
	scrollChangesListToEnd(h)
	if _, ok := h.ui.Find("file-1999.txt"); !ok {
		t.Fatalf("the last file row is missing after scrolling to the end; texts tail=%q", tailTexts(h.ui.Texts(), 20))
	}

	mustClick(t, h, "Unstaged file-1999.txt")
	h.drain()
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "file-1999.txt" {
		t.Fatalf("clicking the last row did not select it: %+v", h.app.changesView)
	}
	if d := &h.app.changesView.diff; d.state == nil {
		t.Fatalf("diff of file-1999.txt did not load: err=%v", d.err)
	}

	mustClick(t, h, "Stage file-1999.txt")
	h.drain()

	out := repo.Git("ls-files", "--", "file-1999.txt")
	if strings.TrimSpace(out) == "" {
		t.Fatalf("file-1999.txt is not in the index after clicking its Stage button; ls-files=%q", out)
	}
	porcelain := repo.Git("status", "--porcelain", "--", "file-1999.txt")
	if !strings.HasPrefix(porcelain, "A ") {
		t.Fatalf("git status for file-1999.txt = %q, want a staged addition", porcelain)
	}
	h.app.session.Reload()
	h.drain()
	cache := h.app.changesView.listCache.lookup(h.app.session.Snapshot().Status)
	if _, ok := cache.lookupStaged("file-1999.txt"); !ok {
		t.Fatal("file-1999.txt missing from the staged rows after reload")
	}
	if _, ok := cache.lookupUnstaged("file-1999.txt"); ok {
		t.Fatal("file-1999.txt is still among the unstaged rows after staging it")
	}
}

func tailTexts(texts []string, n int) []string {
	if len(texts) > n {
		return texts[len(texts)-n:]
	}
	return texts
}

// mergeConflictingFiles leaves the repository mid-merge with n unmerged files
// c-0000.txt .. c-<n-1>.txt, each conflicted in both-modified form.
func mergeConflictingFiles(t *testing.T, repo *gitRepoFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		repo.WriteFile(fmt.Sprintf("c-%04d.txt", i), "base\n")
	}
	repo.Git("add", "-A")
	repo.Git("commit", "-q", "-m", "base")
	repo.Git("checkout", "-q", "-b", "side")
	for i := 0; i < n; i++ {
		repo.WriteFile(fmt.Sprintf("c-%04d.txt", i), "side\n")
	}
	repo.Git("add", "-A")
	repo.Git("commit", "-q", "-m", "side")
	repo.Git("checkout", "-q", "main")
	for i := 0; i < n; i++ {
		repo.WriteFile(fmt.Sprintf("c-%04d.txt", i), "main\n")
	}
	repo.Git("add", "-A")
	repo.Git("commit", "-q", "-m", "main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", out)
	}
	unmerged := repo.Git("ls-files", "-u")
	if strings.TrimSpace(unmerged) == "" {
		t.Fatal("merge left no unmerged index entries")
	}
}

func TestChangesListConflictSectionsOrderCountsAndRowButtons(t *testing.T) {
	// A merge conflict puts the Conflicted section above Unstaged and Staged
	// with the right counts, and a conflicted row offers only its resolution
	// entries, never Stage or Discard.
	repo := newGitRepo(t, "changeslist-conflict")
	mergeConflictingFiles(t, repo, 2)
	repo.WriteFile("plain.txt", "plain\n")
	repo.WriteFile("staged.txt", "staged\n")
	repo.Git("add", "--", "staged.txt")

	h := openChangesHarness(t, repo)

	texts := h.ui.Texts()
	at := func(s string) int {
		for i, text := range texts {
			if text == s {
				return i
			}
		}
		return -1
	}
	conflictAt, unstagedAt, stagedAt := at("Conflicted (2)"), at("Unstaged (1)"), at("Staged (1)")
	if conflictAt < 0 || unstagedAt < 0 || stagedAt < 0 {
		t.Fatalf("section counts missing: conflicted=%d unstaged=%d staged=%d texts=%q", conflictAt, unstagedAt, stagedAt, texts)
	}
	if !(conflictAt < unstagedAt && unstagedAt < stagedAt) {
		t.Fatalf("section order wrong: conflicted=%d unstaged=%d staged=%d texts=%q", conflictAt, unstagedAt, stagedAt, texts)
	}

	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage c-0000.txt"); ok {
		t.Fatal("a conflicted row must not offer a Stage button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Discard c-0000.txt"); ok {
		t.Fatal("a conflicted row must not offer a Discard button")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Mark Resolved c-0000.txt"); !ok {
		t.Fatalf("conflicted row missing its Mark Resolved button; texts=%q", texts)
	}

	// Clicking a conflicted row selects it and shows the conflict pane instead
	// of a diff, as before the list virtualization.
	mustClick(t, h, "Conflicted c-0001.txt")
	h.drain()
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "c-0001.txt" || h.app.changesView.selectedStaged {
		t.Fatalf("clicking the conflict row did not select it: %+v", h.app.changesView)
	}
	if f, ok := h.app.selectedUnstagedConflict(); !ok || f.Path != "c-0001.txt" {
		t.Fatalf("selectedUnstagedConflict = %+v, %v; want c-0001.txt", f, ok)
	}
}

func TestChangesListStatusRevTracksContentChangesOnly(t *testing.T) {
	// Reloading an unchanged snapshot must not advance the status revision;
	// a real status change advances it exactly once.
	repo := newGitRepo(t, "changeslist-rev")
	repo.CommitFile("tracked.txt", "base\n", "initial")

	h := openChangesHarness(t, repo)
	rev := h.app.changesView.statusRev

	h.app.session.Reload()
	h.drain()
	h.app.session.Reload()
	h.drain()
	if got := h.app.changesView.statusRev; got != rev {
		t.Fatalf("statusRev advanced on unchanged reloads: %d -> %d", rev, got)
	}

	repo.WriteFile("new.txt", "new\n")
	h.app.session.Reload()
	h.drain()
	if got := h.app.changesView.statusRev; got != rev+1 {
		t.Fatalf("statusRev after one content change = %d, want %d", got, rev+1)
	}

	h.app.session.Reload()
	h.drain()
	if got := h.app.changesView.statusRev; got != rev+1 {
		t.Fatalf("statusRev advanced again on an unchanged reload: %d -> %d", rev+1, got)
	}
}

func TestChangesListExternalStageClearsSelectionAndDiff(t *testing.T) {
	// When the selected file leaves the unstaged list behind the app's back —
	// staged through the Git CLI — the next reload drops the selection and
	// empties the diff panel.
	repo := newGitRepo(t, "changeslist-external")
	repo.CommitFile("tracked.txt", "base\n", "initial")
	repo.WriteFile("tracked.txt", "base\nmodified\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged tracked.txt")
	h.drain()
	if d := &h.app.changesView.diff; d.state == nil {
		t.Fatalf("diff of tracked.txt did not load: err=%v", d.err)
	}

	repo.Git("add", "--", "tracked.txt")
	h.app.session.Reload()
	h.drain()

	if h.app.changesView.selected || h.app.changesView.selectedPath != "" {
		t.Fatalf("selection survived the external stage: %+v", h.app.changesView)
	}
	if d := &h.app.changesView.diff; d.state != nil || d.loadedKey != "" {
		t.Fatalf("diff panel still shows the staged file: state=%v key=%q", d.state != nil, d.loadedKey)
	}
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("diff panel placeholder missing after the selection cleared; texts=%q", h.ui.Texts())
	}
}

func TestChangesListCacheDoesNotLeakAcrossRepositories(t *testing.T) {
	// Two repositories with the same number of changed files but different
	// paths must never share the derived row cache: opening the second one
	// shows its own files.
	repoA := newGitRepo(t, "changeslist-cache-a")
	repoA.CommitFile("base.txt", "base\n", "initial")
	for i := 0; i < 4; i++ {
		repoA.WriteFile(fmt.Sprintf("a-%04d.txt", i), "a\n")
	}
	repoB := newGitRepo(t, "changeslist-cache-b")
	repoB.CommitFile("base.txt", "base\n", "initial")
	for i := 0; i < 4; i++ {
		repoB.WriteFile(fmt.Sprintf("b-%04d.txt", i), "b\n")
	}

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}
	if !h.ui.HasText("a-0000.txt") {
		t.Fatalf("repo A files missing before the switch; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "changeslist-cache-b")
	h.drain()

	if !h.ui.HasText("b-0000.txt") {
		t.Fatalf("repo B files missing after the switch; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("a-0000.txt") {
		t.Fatalf("repo A files leaked into repo B's list; texts=%q", h.ui.Texts())
	}
	cache := h.app.changesView.listCache.lookup(h.app.session.Snapshot().Status)
	if _, ok := cache.lookupUnstaged("b-0000.txt"); !ok {
		t.Fatal("lookup of a repo B file misses after the switch")
	}
	if _, ok := cache.lookupUnstaged("a-0000.txt"); ok {
		t.Fatal("lookup of a repo A file still hits after the switch")
	}
}
