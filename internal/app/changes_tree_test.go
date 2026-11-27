package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// treeChanges is a changesTree fixture shorthand: an untracked file at path,
// or a rename when orig is set.
func treeChange(p, orig string) git.FileChange {
	f := git.FileChange{Path: p, Status: '?'}
	if orig != "" {
		f = git.FileChange{Path: p, OrigPath: orig, Status: 'R'}
	}
	return f
}

// treeRowsOf flattens rows into "depth:display" (folders) or
// "depth:file:display" lines, so every assertion compares the shape of the
// whole tree in one string.
func treeRowsOf(rows []changesTreeRow) string {
	var lines []string
	for _, r := range rows {
		if r.folder {
			lines = append(lines, fmt.Sprintf("%d:folder:%s:%s:%d", r.depth, r.fullPath, r.display, r.count))
		} else {
			lines = append(lines, fmt.Sprintf("%d:file:%s:%s", r.depth, r.file.Path, r.display))
		}
	}
	return strings.Join(lines, "\n")
}

func TestChangesTreeSortsFoldersBeforeFilesBytewise(t *testing.T) {
	// Inside every folder the subfolders come before the files, each ordered
	// by byte-wise name, and files at the root sit at depth 0.
	rows := buildChangesTree([]git.FileChange{
		treeChange("zeta.txt", ""),
		treeChange("beta/two.txt", ""),
		treeChange("alpha/one.txt", ""),
		treeChange("alpha/aaa.txt", ""),
		treeChange("Alpha/upper.txt", ""),
		treeChange("mid.txt", ""),
	}, nil, false)
	want := strings.Join([]string{
		"0:folder:Alpha:Alpha:1",
		"1:file:Alpha/upper.txt:upper.txt",
		"0:folder:alpha:alpha:2",
		"1:file:alpha/aaa.txt:aaa.txt",
		"1:file:alpha/one.txt:one.txt",
		"0:folder:beta:beta:1",
		"1:file:beta/two.txt:two.txt",
		"0:file:mid.txt:mid.txt",
		"0:file:zeta.txt:zeta.txt",
	}, "\n")
	if got := treeRowsOf(rows); got != want {
		t.Fatalf("tree rows =\n%s\nwant\n%s", got, want)
	}
}

func TestChangesTreeMergesSingleChildChains(t *testing.T) {
	// A folder chain where every folder holds nothing but the next single
	// folder merges into one row carrying the joined path a/b/c, ending where
	// files or a second child appear; a folder holding only one file is a
	// row of its own, not merged away.
	rows := buildChangesTree([]git.FileChange{
		treeChange("a/b/c/one.txt", ""),
		treeChange("a/b/c/two.txt", ""),
		treeChange("solo/only.txt", ""),
		treeChange("split/x/one.txt", ""),
		treeChange("split/y/two.txt", ""),
	}, nil, false)
	want := strings.Join([]string{
		"0:folder:a/b/c:a/b/c:2",
		"1:file:a/b/c/one.txt:one.txt",
		"1:file:a/b/c/two.txt:two.txt",
		"0:folder:solo:solo:1",
		"1:file:solo/only.txt:only.txt",
		"0:folder:split:split:2",
		"1:folder:split/x:x:1",
		"2:file:split/x/one.txt:one.txt",
		"1:folder:split/y:y:1",
		"2:file:split/y/two.txt:two.txt",
	}, "\n")
	if got := treeRowsOf(rows); got != want {
		t.Fatalf("tree rows =\n%s\nwant\n%s", got, want)
	}
}

func TestChangesTreeCountsFilesAtEveryDepth(t *testing.T) {
	// The count of a folder row covers its files at every level below, not
	// only the files it holds itself.
	rows := buildChangesTree([]git.FileChange{
		treeChange("top/one.txt", ""),
		treeChange("top/inner/two.txt", ""),
		treeChange("top/inner/deep/three.txt", ""),
	}, nil, false)
	var top, inner, deep *changesTreeRow
	for i := range rows {
		if rows[i].folder {
			switch rows[i].fullPath {
			case "top":
				top = &rows[i]
			case "top/inner":
				inner = &rows[i]
			case "top/inner/deep":
				deep = &rows[i]
			}
		}
	}
	if top == nil || inner == nil || deep == nil {
		t.Fatalf("folder rows missing: %s", treeRowsOf(rows))
	}
	if top.count != 3 || inner.count != 2 || deep.count != 1 {
		t.Fatalf("counts = top %d inner %d deep %d, want 3/2/1", top.count, inner.count, deep.count)
	}
}

func TestChangesTreeCollapsedFolderKeepsRowHidesChildren(t *testing.T) {
	// A collapsed folder row stays in the output keyed by its merged path,
	// while everything under it leaves; other folders are unaffected.
	rows := buildChangesTree([]git.FileChange{
		treeChange("a/b/c/one.txt", ""),
		treeChange("a/b/c/two.txt", ""),
		treeChange("other/x.txt", ""),
	}, map[string]bool{"a/b/c": true}, false)
	want := strings.Join([]string{
		"0:folder:a/b/c:a/b/c:2",
		"0:folder:other:other:1",
		"1:file:other/x.txt:x.txt",
	}, "\n")
	if got := treeRowsOf(rows); got != want {
		t.Fatalf("collapsed tree rows =\n%s\nwant\n%s", got, want)
	}
}

func TestChangesTreeExpandAllIgnoresCollapse(t *testing.T) {
	// The filtered view expands every folder: the collapse state changes
	// nothing while it applies.
	files := []git.FileChange{
		treeChange("a/b/one.txt", ""),
		treeChange("a/b/two.txt", ""),
	}
	collapsed := map[string]bool{"a/b": true}
	open := buildChangesTree(files, nil, true)
	shut := buildChangesTree(files, collapsed, true)
	if got, want := treeRowsOf(shut), treeRowsOf(open); got != want {
		t.Fatalf("expandAll rows differ by collapse state:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(treeRowsOf(open), "1:file:a/b/one.txt") {
		t.Fatalf("expanded rows missing the files: %s", treeRowsOf(open))
	}
}

func TestChangesTreeRenameFilesUnderNewPath(t *testing.T) {
	// A renamed file is filed under the path it has now, displaying its bare
	// new name with the full original path after the arrow.
	rows := buildChangesTree([]git.FileChange{
		treeChange("newdir/newname.txt", "olddir/oldname.txt"),
	}, nil, false)
	want := strings.Join([]string{
		"0:folder:newdir:newdir:1",
		"1:file:newdir/newname.txt:newname.txt ← olddir/oldname.txt",
	}, "\n")
	if got := treeRowsOf(rows); got != want {
		t.Fatalf("rename tree rows =\n%s\nwant\n%s", got, want)
	}
}

// treeTestRepo builds a repository whose working tree exercises every tree
// behavior: a mergeable chain, a folder with siblings at several levels, a
// staged section of its own, a rename and root files.
func treeTestRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.CommitFile("chain/deep/down/alpha.txt", "alpha\n", "chain file")
	repo.CommitFile("chain/deep/down/beta.txt", "beta\n", "second chain file")
	repo.CommitFile("pack/one.txt", "one\n", "pack one")
	repo.CommitFile("pack/two.txt", "two\n", "pack two")
	repo.CommitFile("oldname.txt", "renamed\n", "file to rename")
	repo.Git("mv", "oldname.txt", "renamed.txt")
	repo.WriteFile("chain/deep/down/alpha.txt", "alpha\nedited\n")
	repo.WriteFile("pack/one.txt", "one\nedited\n")
	repo.WriteFile("root-new.txt", "new\n")
	repo.WriteFile("staged-dir/placed.txt", "staged\n")
	repo.Git("add", "--", "staged-dir/placed.txt")
	return repo
}

// treeFolderShown reports whether the folder row of path in the given
// section is built, returning its access info for the expanded state.
func treeFolderShown(h *testHarness, label string) (ui.AccessInfo, bool) {
	return h.ui.AccessByRole(ui.RoleButton, label)
}

func TestChangesTreeToggleShowsFoldersAndBack(t *testing.T) {
	// Clicking Show as Tree replaces the flat rows with folder rows that
	// count their files, and Show as List restores exactly the flat rows.
	repo := treeTestRepo(t, "changestree-toggle")
	h := openChangesHarness(t, repo)

	for _, label := range []string{"Unstaged chain/deep/down/alpha.txt", "Unstaged pack/one.txt", "Unstaged root-new.txt", "Staged staged-dir/placed.txt"} {
		if !changeRowShown(h, label) {
			t.Fatalf("flat row %q missing before the toggle; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := treeFolderShown(h, "Unstaged Folder chain/deep/down, 2 files"); ok {
		t.Fatal("a folder row shows before tree mode is on")
	}

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if !h.app.changesView.treeMode {
		t.Fatal("clicking Show as Tree did not switch the view state")
	}
	for _, label := range []string{"Unstaged Folder chain/deep/down, 1 file", "Unstaged Folder pack, 1 file", "Staged Folder staged-dir, 1 file"} {
		if _, ok := treeFolderShown(h, label); !ok {
			t.Fatalf("folder row %q missing in tree mode; texts=%q", label, h.ui.Texts())
		}
	}
	for _, label := range []string{"Unstaged chain/deep/down/alpha.txt", "Staged staged-dir/placed.txt"} {
		if !changeRowShown(h, label) {
			t.Fatalf("file row %q missing in tree mode; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Show as List"); !ok {
		t.Fatalf("the toggle did not rename itself in tree mode; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Show as List")
	h.ui.Frame()
	if h.app.changesView.treeMode {
		t.Fatal("clicking Show as List did not switch back")
	}
	for _, label := range []string{"Unstaged chain/deep/down/alpha.txt", "Unstaged pack/one.txt", "Unstaged root-new.txt", "Staged staged-dir/placed.txt"} {
		if !changeRowShown(h, label) {
			t.Fatalf("flat row %q missing after switching back; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := treeFolderShown(h, "Unstaged Folder pack, 1 file"); ok {
		t.Fatal("a folder row survived switching back to the list")
	}
}

// openTestAppOnStore builds a fresh app harness over the store file at
// storePath, as a relaunched app reads the repositories back.
func openTestAppOnStore(t *testing.T, storePath string) *testHarness {
	t.Helper()
	s, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("reopen test store: %v", err)
	}
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	a := newApp(s)
	a.postFn = q.post
	a.spawnFn = q.spawn
	h := &testHarness{t: t, app: a, store: s, ui: ui.NewTester(a.view, 1200, 800), queue: q}
	h.ui.Frame()
	return h
}

func TestChangesTreeRememberedPerRepository(t *testing.T) {
	// The view choice is the repository's: a fresh app over the same store
	// opens the chosen repository as a tree, a repository never chosen stays
	// a list, and the store file carries the choice as fileListTree.
	repoA := treeTestRepo(t, "changestree-keep-a")
	repoB := newGitRepo(t, "changestree-keep-b")
	repoB.CommitFile("base.txt", "base\n", "initial")
	repoB.WriteFile("loose/nested.txt", "n\n")

	h := openChangesHarness(t, repoA)
	savedA := h.app.session.Meta()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	got, ok := h.store.Get(savedA.ID)
	if !ok {
		t.Fatal("repo A missing from the store")
	}
	if !got.Local.Diff.FileListTree {
		t.Fatal("the tree choice did not reach the repository settings")
	}
	raw, err := os.ReadFile(h.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fileListTree": true`) {
		t.Fatalf("store JSON missing fileListTree: %s", raw)
	}

	// A new app over the same store file reads the choice back: repo A opens
	// as a tree, repo B as the flat list.
	h2 := openTestAppOnStore(t, h.store.Path())
	h2.openRepo(got)
	h2.drain()
	if !h2.app.changesView.treeMode {
		t.Fatal("the fresh app did not open repo A as a tree")
	}
	if _, ok := treeFolderShown(h2, "Unstaged Folder pack, 1 file"); !ok {
		t.Fatalf("folder rows missing in the reopened app; texts=%q", h2.ui.Texts())
	}

	mustClick(t, h2, "Repository Switcher")
	h2.ui.Frame()
	mustClick(t, h2, "changestree-keep-b")
	h2.drain()
	if h2.app.changesView.treeMode {
		t.Fatal("repo B opened as a tree although it never chose the view")
	}
	if !changeRowShown(h2, "Unstaged loose/nested.txt") {
		t.Fatalf("flat row missing in repo B; texts=%q", h2.ui.Texts())
	}
}

func TestChangesTreeCollapseClickAndKeys(t *testing.T) {
	// A folder row toggles its collapse by click, space and enter, expands
	// on Right and collapses on Left with no effect repeated, the rows under
	// it come and go, and its Expanded state follows.
	repo := treeTestRepo(t, "changestree-collapse")
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	chain := "Unstaged Folder chain/deep/down, 1 file"
	file := "Unstaged chain/deep/down/alpha.txt"

	expanded := func(label string, want bool) {
		t.Helper()
		info, ok := treeFolderShown(h, label)
		if !ok {
			t.Fatalf("folder row %q missing; texts=%q", label, h.ui.Texts())
		}
		if info.Expanded != want {
			t.Fatalf("folder row %q Expanded = %v, want %v", label, info.Expanded, want)
		}
	}

	expanded(chain, true)
	if !changeRowShown(h, file) {
		t.Fatalf("chain file missing while expanded; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, chain)
	h.ui.Frame()
	expanded(chain, false)
	if changeRowShown(h, file) {
		t.Fatal("the chain file survived collapsing its folder by click")
	}

	// Focus is on the folder row the click pressed: Space opens it again,
	// Enter collapses, Right expands a collapsed row and Left collapses an
	// open one, each a no-op in the state it asks for.
	h.ui.Key(0, ui.KeySpace)
	h.ui.Frame()
	expanded(chain, true)
	if !changeRowShown(h, file) {
		t.Fatal("Space did not expand the collapsed folder row")
	}

	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	expanded(chain, false)
	if changeRowShown(h, file) {
		t.Fatal("Enter did not collapse the open folder row")
	}

	h.ui.Key(0, ui.KeyLeft)
	h.ui.Frame()
	if changeRowShown(h, file) == true {
		// Left on a collapsed row must do nothing: the file stays hidden.
		t.Fatal("Left collapsed an already collapsed row")
	}
	expanded(chain, false)

	h.ui.Key(0, ui.KeyRight)
	h.ui.Frame()
	expanded(chain, true)
	if !changeRowShown(h, file) {
		t.Fatal("Right did not expand the collapsed folder row")
	}
	h.ui.Key(0, ui.KeyRight)
	h.ui.Frame()
	if !changeRowShown(h, file) {
		t.Fatal("Right on an expanded row hid its file")
	}

	h.ui.Key(0, ui.KeyLeft)
	h.ui.Frame()
	expanded(chain, false)
	if changeRowShown(h, file) {
		t.Fatal("Left did not collapse the open folder row")
	}
}

func TestChangesTreeCollapsedSelectionKeepsDiff(t *testing.T) {
	// Collapsing the folder of the selected file hides the row but keeps the
	// selection and the diff the panel shows.
	repo := treeTestRepo(t, "changestree-sel")
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	mustClick(t, h, "Unstaged chain/deep/down/alpha.txt")
	h.drain()
	if d := &h.app.changesView.diff; d.state == nil {
		t.Fatalf("diff of alpha.txt did not load: err=%v", d.err)
	}

	mustClick(t, h, "Unstaged Folder chain/deep/down, 1 file")
	h.ui.Frame()
	if changeRowShown(h, "Unstaged chain/deep/down/alpha.txt") {
		t.Fatal("the selected row survived the collapse")
	}
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "chain/deep/down/alpha.txt" {
		t.Fatalf("the collapse dropped the selection: %+v", h.app.changesView)
	}
	if d := &h.app.changesView.diff; d.state == nil {
		t.Fatal("the collapse emptied the diff panel")
	}
}

func TestChangesTreeFilterExpandsAndRestores(t *testing.T) {
	// A filter keeps only the matching files and the folders above them, all
	// of them expanded with no arrow and no collapse by click; clearing the
	// filter restores the collapsed state the folders had.
	repo := treeTestRepo(t, "changestree-filter")
	repo.WriteFile("pack/needle.txt", "needle\n")
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	mustClick(t, h, "Unstaged Folder pack, 2 files")
	h.ui.Frame()
	if changeRowShown(h, "Unstaged pack/one.txt") {
		t.Fatal("setup: pack did not collapse")
	}

	typeChangesFilter(t, h, "needle")
	for _, label := range []string{"Unstaged pack/one.txt", "Unstaged chain/deep/down/alpha.txt"} {
		if changeRowShown(h, label) {
			t.Fatalf("%s survived the filter", label)
		}
	}
	if !changeRowShown(h, "Unstaged pack/needle.txt") {
		t.Fatalf("the matching file is hidden; texts=%q", h.ui.Texts())
	}
	info, ok := treeFolderShown(h, "Unstaged Folder pack, 1 file")
	if !ok {
		t.Fatalf("the folder above the match is missing; texts=%q", h.ui.Texts())
	}
	if !info.Expanded {
		t.Fatal("the filter left a folder collapsed")
	}
	if _, ok := treeFolderShown(h, "Unstaged Folder chain/deep/down, 1 file"); ok {
		t.Fatal("a folder without matches survived the filter")
	}

	// Clicking the folder row while the filter is active must not collapse
	// it: the match stays visible.
	mustClick(t, h, "Unstaged Folder pack, 1 file")
	h.ui.Frame()
	if !changeRowShown(h, "Unstaged pack/needle.txt") {
		t.Fatal("clicking the folder row under a filter collapsed it")
	}

	mustClick(t, h, "Clear Filter")
	h.ui.Frame()
	if changeRowShown(h, "Unstaged pack/one.txt") {
		t.Fatal("clearing the filter did not restore the collapsed pack")
	}
	info, ok = treeFolderShown(h, "Unstaged Folder pack, 2 files")
	if !ok {
		t.Fatalf("pack missing after clearing; texts=%q", h.ui.Texts())
	}
	if info.Expanded {
		t.Fatal("clearing the filter forgot the collapsed state")
	}
}

func TestChangesTreeFileRowButtonsWork(t *testing.T) {
	// The file rows of the tree keep their working buttons: Stage moves one
	// file into the index, Unstage back out, and Discard removes an untracked
	// file through its confirmation, git status confirming each.
	repo := treeTestRepo(t, "changestree-buttons")
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	mustClick(t, h, "Stage root-new.txt")
	h.drain()
	if out := repo.Git("ls-files", "--", "root-new.txt"); strings.TrimSpace(out) == "" {
		t.Fatalf("root-new.txt is not in the index after Stage; ls-files=%q", out)
	}
	if porcelain := repo.Git("status", "--porcelain", "--", "root-new.txt"); !strings.HasPrefix(porcelain, "A ") {
		t.Fatalf("root-new.txt porcelain = %q, want a staged addition", porcelain)
	}

	mustClick(t, h, "Unstage staged-dir/placed.txt")
	h.drain()
	if out := repo.Git("ls-files", "--", "staged-dir/placed.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("staged-dir/placed.txt is still in the index after Unstage; ls-files=%q", out)
	}

	repo.WriteFile("trash.txt", "trash\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Discard trash.txt")
	h.ui.Frame()
	if !h.ui.HasText("Discard changes?") {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if _, err := os.Stat(filepath.Join(repo.Path(), "trash.txt")); !os.IsNotExist(err) {
		t.Fatalf("trash.txt survived Discard: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "root-new.txt")); err != nil {
		t.Fatalf("root-new.txt was discarded although only trash.txt was asked: %v", err)
	}
}

func TestChangesTreeRepoSwitchClearsCollapse(t *testing.T) {
	// The collapse state belongs to the repository: switching away and back
	// shows every folder expanded again, while the tree choice stays.
	repoA := treeTestRepo(t, "changestree-switch-a")
	repoB := newGitRepo(t, "changestree-switch-b")
	repoB.CommitFile("base.txt", "base\n", "initial")
	repoB.WriteFile("loose/nested.txt", "n\n")

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	mustClick(t, h, "Unstaged Folder pack, 1 file")
	h.ui.Frame()
	if changeRowShown(h, "Unstaged pack/one.txt") {
		t.Fatal("setup: pack did not collapse")
	}

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "changestree-switch-b")
	h.drain()

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "changestree-switch-a")
	h.drain()

	if !h.app.changesView.treeMode {
		t.Fatal("the tree choice did not survive the repository round trip")
	}
	if !changeRowShown(h, "Unstaged pack/one.txt") {
		t.Fatalf("pack stayed collapsed across the repository switch; texts=%q", h.ui.Texts())
	}
	info, ok := treeFolderShown(h, "Unstaged Folder pack, 1 file")
	if !ok {
		t.Fatalf("pack missing after switching back; texts=%q", h.ui.Texts())
	}
	if !info.Expanded {
		t.Fatal("the collapsed state survived the repository switch")
	}
}

func TestChangesTreeConflictedSectionStaysFlat(t *testing.T) {
	// Tree mode groups the unstaged and staged sections alone: conflicted
	// files stay flat, never inside a folder row.
	repo := newGitRepo(t, "changestree-conflict")
	mergeConflictingFiles(t, repo, 2)
	repo.WriteFile("nested/dir/plain.txt", "plain\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	for _, label := range []string{"Conflicted c-0000.txt", "Conflicted c-0001.txt"} {
		if !changeRowShown(h, label) {
			t.Fatalf("conflict row %q missing in tree mode; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := treeFolderShown(h, "Unstaged Folder nested/dir, 1 file"); !ok {
		t.Fatalf("ordinary folder row missing in tree mode; texts=%q", h.ui.Texts())
	}
	if _, ok := treeFolderShown(h, "Conflicted Folder c-0000.txt, 1 file"); ok {
		t.Fatal("a conflicted file fell into a folder row")
	}
}

func TestChangesTreeSaveFailureStillToggles(t *testing.T) {
	// When the repository settings cannot be written the view still switches
	// for this opening, and the failure surfaces as the settings save toast.
	repo := treeTestRepo(t, "changestree-fail")
	h := openChangesHarness(t, repo)
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.store.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(h.store.Path() + ".tmp") })

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if !h.app.changesView.treeMode {
		t.Fatal("the view did not switch although saving failed")
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("the save failure did not surface as the settings toast; texts=%q", h.ui.Texts())
	}
	got, ok := h.store.Get(h.app.session.Meta().ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if got.Local.Diff.FileListTree {
		t.Fatal("the failed save reached the repository settings")
	}
}

func TestChangesTreeCollapseSurvivesRefresh(t *testing.T) {
	// Reloading the repository status preserves the folder collapse state
	// while the same repository stays open.
	repo := treeTestRepo(t, "changestree-refresh")
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	mustClick(t, h, "Unstaged Folder pack, 1 file")
	h.ui.Frame()
	if changeRowShown(h, "Unstaged pack/one.txt") {
		t.Fatal("setup: pack did not collapse")
	}

	repo.WriteFile("pack/extra.txt", "extra\n")
	h.app.session.Reload()
	h.drain()

	info, ok := treeFolderShown(h, "Unstaged Folder pack, 2 files")
	if !ok {
		t.Fatalf("pack missing after refresh; texts=%q", h.ui.Texts())
	}
	if info.Expanded {
		t.Fatal("refreshing the repository forgot the collapsed folder")
	}
	if changeRowShown(h, "Unstaged pack/one.txt") || changeRowShown(h, "Unstaged pack/extra.txt") {
		t.Fatal("children of the collapsed folder reappeared after refresh")
	}
}

func TestChangesTreeToggleKeepsSelectionAndFocus(t *testing.T) {
	// Switching between the flat list and the tree keeps the selected file
	// and its diff intact, and leaves keyboard focus on the view toggle.
	repo := treeTestRepo(t, "changestree-selfocus")
	h := openChangesHarness(t, repo)

	mustClick(t, h, "Unstaged chain/deep/down/alpha.txt")
	h.drain()
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "chain/deep/down/alpha.txt" {
		t.Fatalf("setup: selection = %+v", h.app.changesView)
	}
	if h.app.changesView.diff.state == nil {
		t.Fatal("setup: diff did not load")
	}

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if !h.app.changesView.treeMode {
		t.Fatal("view did not switch to tree")
	}
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "chain/deep/down/alpha.txt" || h.app.changesView.diff.state == nil {
		t.Fatalf("switching to tree lost selection or diff: %+v", h.app.changesView)
	}
	if focused, ok := h.ui.AccessByRole(ui.RoleButton, "Show as List"); !ok || !focused.Focused {
		t.Fatalf("keyboard focus left the view toggle after switching to tree: ok=%v info=%+v", ok, focused)
	}

	h.ui.Key(0, ui.KeySpace)
	h.ui.Frame()
	if h.app.changesView.treeMode {
		t.Fatal("pressing Space on the focused toggle did not return to the flat list")
	}
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "chain/deep/down/alpha.txt" || h.app.changesView.diff.state == nil {
		t.Fatalf("switching back to list lost selection or diff: %+v", h.app.changesView)
	}
	if focused, ok := h.ui.AccessByRole(ui.RoleButton, "Show as Tree"); !ok || !focused.Focused {
		t.Fatalf("keyboard focus left the view toggle after returning to list: ok=%v info=%+v", ok, focused)
	}
}
