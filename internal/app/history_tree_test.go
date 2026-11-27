package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// historyTreeCommit writes the files of one commit into the fixture and
// commits them all at once, returning the commit hash.
func historyTreeCommit(repo *gitRepoFixture, subject string, files map[string]string) string {
	repo.t.Helper()
	paths := make([]string, 0, len(files))
	for p, content := range files {
		repo.WriteFile(p, content)
		paths = append(paths, p)
	}
	args := append([]string{"add", "--"}, paths...)
	repo.Git(args...)
	repo.Git("commit", "-m", subject)
	return strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
}

// historyTreeFixture builds a repository whose commits exercise the tree:
// a mergeable chain of single-child folders, a folder holding two files, a
// root file, and a second commit sharing the chain folder so the collapse
// state can prove it survives choosing another commit. It returns the
// hashes of the two tree commits.
func historyTreeFixture(t *testing.T, name string) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "base commit")
	one := historyTreeCommit(repo, "tree commit one", map[string]string{
		"chain/deep/down/alpha.txt": "alpha one\n",
		"chain/deep/down/beta.txt":  "beta one\n",
		"pack/one.txt":              "pack one\n",
		"pack/two.txt":              "pack two\n",
		"root.txt":                  "root one\n",
	})
	two := historyTreeCommit(repo, "tree commit two", map[string]string{
		"chain/deep/down/alpha.txt": "alpha two\n",
		"pack/three.txt":            "pack three\n",
	})
	return repo, one, two
}

// openHistoryTreeCommit opens the fixture in a fresh harness, navigates to
// the History page and chooses the commit, its detail drained.
func openHistoryTreeCommit(t *testing.T, repo *gitRepoFixture, hash, subject string) *testHarness {
	t.Helper()
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, hash, subject)
	h.drain()
	return h
}

// historyFileRowShown reports whether the row of the file shows in the
// changed-file list, in either view.
func historyFileRowShown(h *testHarness, path string) bool {
	_, ok := h.ui.AccessByRole(ui.RoleListItem, fileRowLabel(path))
	return ok
}

// historyFolderShown returns the access info of the folder row's button, or
// whether it exists at all.
func historyFolderShown(h *testHarness, label string) (ui.AccessInfo, bool) {
	return h.ui.AccessByRole(ui.RoleButton, label)
}

func TestHistoryTreeToggleShowsFoldersAndBack(t *testing.T) {
	// Show as Tree replaces the flat rows with folder rows that count their
	// files, the root file staying a row of the root, and Show as List
	// restores exactly the flat rows.
	repo, one, _ := historyTreeFixture(t, "histtree-toggle")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")

	for _, path := range []string{"chain/deep/down/alpha.txt", "pack/one.txt", "root.txt"} {
		if !historyFileRowShown(h, path) {
			t.Fatalf("flat row %q missing before the toggle; texts=%q", path, h.ui.Texts())
		}
	}
	if _, ok := historyFolderShown(h, "Folder chain/deep/down, 2 files"); ok {
		t.Fatal("a folder row shows before tree mode is on")
	}

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if !h.app.historyView.treeMode {
		t.Fatal("clicking Show as Tree did not switch the view state")
	}
	for _, label := range []string{"Folder chain/deep/down, 2 files", "Folder pack, 2 files"} {
		if _, ok := historyFolderShown(h, label); !ok {
			t.Fatalf("folder row %q missing in tree mode; texts=%q", label, h.ui.Texts())
		}
	}
	for _, path := range []string{"chain/deep/down/alpha.txt", "pack/one.txt", "root.txt"} {
		if !historyFileRowShown(h, path) {
			t.Fatalf("file row %q missing in tree mode; texts=%q", path, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Show as List"); !ok {
		t.Fatalf("the toggle did not rename itself in tree mode; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Show as List")
	h.ui.Frame()
	if h.app.historyView.treeMode {
		t.Fatal("clicking Show as List did not switch back")
	}
	for _, path := range []string{"chain/deep/down/alpha.txt", "pack/one.txt", "root.txt"} {
		if !historyFileRowShown(h, path) {
			t.Fatalf("flat row %q missing after switching back; texts=%q", path, h.ui.Texts())
		}
	}
	if _, ok := historyFolderShown(h, "Folder pack, 2 files"); ok {
		t.Fatal("a folder row survived switching back to the list")
	}
}

func TestHistoryTreeMergedChainAndSortOrder(t *testing.T) {
	// The chain of single-child folders merges into one row carrying the
	// joined path, folder rows sort byte-wise before the files of their
	// folder, and a file count covers every level below.
	repo, one, _ := historyTreeFixture(t, "histtree-merge")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	cache := &h.app.historyView.treeRows
	var order []string
	counts := map[string]int{}
	for _, r := range cache.rows {
		if r.folder {
			order = append(order, "folder:"+r.fullPath)
			counts[r.fullPath] = r.count
		} else {
			order = append(order, "file:"+r.file.Path)
		}
	}
	want := []string{
		"folder:chain/deep/down",
		"file:chain/deep/down/alpha.txt",
		"file:chain/deep/down/beta.txt",
		"folder:pack",
		"file:pack/one.txt",
		"file:pack/two.txt",
		"file:root.txt",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tree rows =\n%s\nwant\n%s", strings.Join(order, "\n"), strings.Join(want, "\n"))
	}
	if counts["chain/deep/down"] != 2 {
		t.Fatalf("chain folder count = %d, want 2", counts["chain/deep/down"])
	}
	if _, ok := historyFolderShown(h, "Folder chain, 2 files"); ok {
		t.Fatal("the chain did not merge into its joined path")
	}
	for _, r := range cache.rows {
		if r.folder && r.depth != 0 {
			t.Fatalf("folder %q depth = %d, want 0: the merge leaves no nested chain rows", r.fullPath, r.depth)
		}
	}
}

func TestHistoryTreeRememberedPerRepository(t *testing.T) {
	// The view choice is the repository's own: a fresh app over the same
	// store opens the chosen repository's changed files as a tree, the store
	// file carries historyFileListTree, and the Local Changes view choice is
	// untouched either way.
	repo, one, _ := historyTreeFixture(t, "histtree-keep")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	saved := h.app.session.Meta()

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	got, ok := h.store.Get(saved.ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if !got.Local.Diff.HistoryFileListTree {
		t.Fatal("the tree choice did not reach the repository settings")
	}
	if got.Local.Diff.FileListTree {
		t.Fatal("the history toggle wrote the Local Changes view choice")
	}
	raw, err := os.ReadFile(h.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"historyFileListTree": true`) {
		t.Fatalf("store JSON missing historyFileListTree: %s", raw)
	}

	// A new app over the same store file reads the choice back on opening
	// the repository.
	h2 := openTestAppOnStore(t, h.store.Path())
	h2.openRepo(got)
	h2.drain()
	mustClick(t, h2, "History")
	h2.ui.Frame()
	clickCommitRow(t, h2, one, "tree commit one")
	h2.drain()
	if !h2.app.historyView.treeMode {
		t.Fatal("the fresh app did not open the commit's files as a tree")
	}
	if _, ok := historyFolderShown(h2, "Folder pack, 2 files"); !ok {
		t.Fatalf("folder rows missing in the reopened app; texts=%q", h2.ui.Texts())
	}
	if _, ok := h2.ui.AccessByRole(ui.RoleButton, "Show as List"); !ok {
		t.Fatalf("the reopened toggle does not read Show as List; texts=%q", h2.ui.Texts())
	}

	// The Local Changes page of the same repository still opens flat.
	mustClick(t, h2, "Changes")
	h2.drain()
	if h2.app.changesView.treeMode {
		t.Fatal("the history view choice leaked into Local Changes")
	}
}

func TestHistoryTreeSaveFailureStillToggles(t *testing.T) {
	// When the repository settings cannot be written the view still switches
	// for this opening, and the failure surfaces as the settings save toast.
	repo, one, _ := historyTreeFixture(t, "histtree-fail")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	if err := os.Remove(h.store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.store.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(h.store.Path() + ".tmp") })

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if !h.app.historyView.treeMode {
		t.Fatal("the view did not switch although saving failed")
	}
	if !h.ui.HasText("Cannot save settings") {
		t.Fatalf("the save failure did not surface as the settings toast; texts=%q", h.ui.Texts())
	}
	got, ok := h.store.Get(h.app.session.Meta().ID)
	if !ok {
		t.Fatal("repository missing from the store")
	}
	if got.Local.Diff.HistoryFileListTree {
		t.Fatal("the failed save reached the repository settings")
	}
}

func TestHistoryTreeCollapseKeysAndCommitSwitch(t *testing.T) {
	// A folder row toggles its collapse by click, expands on Right and
	// collapses on Left with no effect repeated, and the collapsed state
	// survives choosing another commit of the same repository because the
	// paths repeat there.
	repo, one, two := historyTreeFixture(t, "histtree-collapse")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	chain := "Folder chain/deep/down, 2 files"
	alpha := "chain/deep/down/alpha.txt"

	expanded := func(label string, want bool) {
		t.Helper()
		info, ok := historyFolderShown(h, label)
		if !ok {
			t.Fatalf("folder row %q missing; texts=%q", label, h.ui.Texts())
		}
		if info.Expanded != want {
			t.Fatalf("folder row %q Expanded = %v, want %v", label, info.Expanded, want)
		}
	}

	expanded(chain, true)
	if !historyFileRowShown(h, alpha) {
		t.Fatalf("chain file missing while expanded; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, chain)
	h.ui.Frame()
	expanded(chain, false)
	if historyFileRowShown(h, alpha) {
		t.Fatal("the chain file survived collapsing its folder by click")
	}

	// The click rested the selection on the folder row: Right opens it
	// again and is a no-op repeated; Left collapses and is a no-op repeated.
	h.ui.Key(0, ui.KeyRight)
	h.ui.Frame()
	expanded(chain, true)
	if !historyFileRowShown(h, alpha) {
		t.Fatal("Right did not expand the collapsed folder row")
	}
	h.ui.Key(0, ui.KeyRight)
	h.ui.Frame()
	if !historyFileRowShown(h, alpha) {
		t.Fatal("Right on an expanded row hid its file")
	}
	h.ui.Key(0, ui.KeyLeft)
	h.ui.Frame()
	expanded(chain, false)
	if historyFileRowShown(h, alpha) {
		t.Fatal("Left did not collapse the open folder row")
	}
	h.ui.Key(0, ui.KeyLeft)
	h.ui.Frame()
	expanded(chain, false)

	// The second commit changes the same chain folder, which holds one file
	// there: choosing it keeps the collapse keyed by the merged path, and
	// choosing back keeps it still.
	clickCommitRow(t, h, two, "tree commit two")
	h.drain()
	chainTwo := "Folder chain/deep/down, 1 file"
	expanded(chainTwo, false)
	if historyFileRowShown(h, alpha) {
		t.Fatal("the collapse did not survive choosing another commit")
	}
	clickCommitRow(t, h, one, "tree commit one")
	h.drain()
	expanded(chain, false)
	if historyFileRowShown(h, alpha) {
		t.Fatal("the collapse did not survive the commit round trip")
	}
}

func TestHistoryTreeRepoSwitchClearsCollapse(t *testing.T) {
	// The collapse state belongs to the repository: switching away and back
	// shows every folder expanded again, while the tree choice stays.
	repoA, oneA, _ := historyTreeFixture(t, "histtree-switch-a")
	repoB := newGitRepo(t, "histtree-switch-b")
	repoB.CommitFile("base.txt", "base\n", "initial")
	repoB.CommitFile("loose/nested.txt", "n\n", "nested file")

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}
	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, oneA, "tree commit one")
	h.drain()

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	mustClick(t, h, "Folder pack, 2 files")
	h.ui.Frame()
	if historyFileRowShown(h, "pack/one.txt") {
		t.Fatal("setup: pack did not collapse")
	}

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "histtree-switch-b")
	h.drain()

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "histtree-switch-a")
	h.drain()
	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, oneA, "tree commit one")
	h.drain()

	if !h.app.historyView.treeMode {
		t.Fatal("the tree choice did not survive the repository round trip")
	}
	if !historyFileRowShown(h, "pack/one.txt") {
		t.Fatalf("pack stayed collapsed across the repository switch; texts=%q", h.ui.Texts())
	}
	info, ok := historyFolderShown(h, "Folder pack, 2 files")
	if !ok {
		t.Fatalf("pack missing after switching back; texts=%q", h.ui.Texts())
	}
	if !info.Expanded {
		t.Fatal("the collapsed state survived the repository switch")
	}
}

func TestHistoryTreeFileClickLoadsDiffAndMenu(t *testing.T) {
	// The file rows of the tree behave as the flat rows do: clicking one
	// loads the diff of the commit at that file, and the context menu offers
	// File History, Blame and the reset entries, File History opening the
	// inspector for the row's file.
	repo, one, _ := historyTreeFixture(t, "histtree-menu")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	mustClick(t, h, fileRowLabel("pack/two.txt"))
	h.drain()
	if got := h.app.historyView.diff.req.File.Path; got != "pack/two.txt" {
		t.Fatalf("diff path = %q, want pack/two.txt", got)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); len(ins) == 0 {
		t.Fatal("the tree file row loaded no diff insertions")
	}

	if err := h.ui.RightClick(fileRowLabel("pack/two.txt")); err != nil {
		t.Fatalf("right-click tree file row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	for _, item := range []string{"File History…", "Blame…"} {
		found := false
		for _, got := range menu {
			if got == item {
				found = true
			}
		}
		if !found {
			t.Fatalf("tree row menu %q missing %q", menu, item)
		}
	}
	hasReset := false
	for _, got := range menu {
		if strings.Contains(got, "Reset") {
			hasReset = true
		}
	}
	if !hasReset {
		t.Fatalf("tree row menu %q missing the reset entries", menu)
	}
	if err := h.ui.ChooseMenuItem("File History…"); err != nil {
		t.Fatalf("choose File History: %v", err)
	}
	h.drain()
	st := h.app.fileInspectorUI()
	if !st.open || st.originPage != pageHistory || st.path != "pack/two.txt" {
		t.Fatalf("inspector open=%v origin=%v path=%q, want history file history of pack/two.txt", st.open, st.originPage, st.path)
	}

	// A folder row carries no menu: right-clicking it opens nothing.
	h.app.closeFileInspector()
	h.ui.Frame()
	if err := h.ui.RightClick("Folder pack, 2 files"); err != nil {
		t.Fatalf("right-click folder row: %v", err)
	}
	h.ui.Frame()
	if menu := h.ui.Menu(); len(menu) != 0 {
		t.Fatalf("folder row opened a menu %q, want none", menu)
	}
}

func TestHistoryTreeToggleKeepsSelectionAndDiff(t *testing.T) {
	// Switching the view keeps the chosen file chosen with its diff loaded:
	// the same file is selected in the tree, and switching back selects it
	// in the flat list again.
	repo, one, _ := historyTreeFixture(t, "histtree-keep-sel")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")

	mustClick(t, h, fileRowLabel("pack/one.txt"))
	h.drain()
	if got := h.app.historyView.diff.req.File.Path; got != "pack/one.txt" {
		t.Fatalf("setup: diff path = %q, want pack/one.txt", got)
	}
	flatSel := h.app.historyView.fileSel

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	h.ui.Frame()
	if h.app.historyView.fileSel != flatSel {
		t.Fatalf("fileSel = %d after switching to the tree, want %d", h.app.historyView.fileSel, flatSel)
	}
	if got := h.app.historyView.diff.req.File.Path; got != "pack/one.txt" {
		t.Fatalf("diff path = %q after switching to the tree, want pack/one.txt", got)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); len(ins) == 0 {
		t.Fatal("the diff panel lost its content switching to the tree")
	}
	row, ok := h.ui.AccessByRole(ui.RoleListItem, fileRowLabel("pack/one.txt"))
	if !ok || !row.Selected {
		t.Fatalf("tree row access = %+v (found=%v), want the same file selected", row, ok)
	}

	mustClick(t, h, "Show as List")
	h.ui.Frame()
	h.ui.Frame()
	row, ok = h.ui.AccessByRole(ui.RoleListItem, fileRowLabel("pack/one.txt"))
	if !ok || !row.Selected {
		t.Fatalf("flat row access = %+v (found=%v), want the same file selected", row, ok)
	}
	if got := h.app.historyView.diff.req.File.Path; got != "pack/one.txt" {
		t.Fatalf("diff path = %q after switching back, want pack/one.txt", got)
	}
}

func TestHistoryTreeCollapsedSelectionRestsOnFolder(t *testing.T) {
	// Collapsing the folder of the selected file hides the file's row but
	// keeps the diff, and the selection rests on the folder row.
	repo, one, _ := historyTreeFixture(t, "histtree-fold-sel")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	mustClick(t, h, fileRowLabel("chain/deep/down/alpha.txt"))
	h.drain()
	if got := h.app.historyView.diff.req.File.Path; got != "chain/deep/down/alpha.txt" {
		t.Fatalf("setup: diff path = %q, want chain/deep/down/alpha.txt", got)
	}

	mustClick(t, h, "Folder chain/deep/down, 2 files")
	h.ui.Frame()
	h.ui.Frame()
	if historyFileRowShown(h, "chain/deep/down/alpha.txt") {
		t.Fatal("the selected row survived the collapse")
	}
	if got := h.app.historyView.diff.req.File.Path; got != "chain/deep/down/alpha.txt" {
		t.Fatalf("the collapse cleared the diff: path = %q", got)
	}
	if h.app.historyView.treeFocus != "chain/deep/down" {
		t.Fatalf("treeFocus = %q, want chain/deep/down", h.app.historyView.treeFocus)
	}
	row, ok := h.ui.AccessByRole(ui.RoleListItem, "chain/deep/down, 2 files")
	if !ok || !row.Selected {
		t.Fatalf("folder row access = %+v (found=%v), want the folder selected", row, ok)
	}
}

func TestHistoryTreeArrowKeysMoveBetweenRows(t *testing.T) {
	// Up and Down move the selection across folder and file rows: landing
	// on a file row loads its diff, landing on a folder row moves only the
	// selection and keeps the diff of the file chosen before.
	repo, one, _ := historyTreeFixture(t, "histtree-arrows")
	h := openHistoryTreeCommit(t, repo, one, "tree commit one")
	hv := &h.app.historyView
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	// The first row is the chain folder: the detail load selected the first
	// file, alpha, so one Up reaches the folder above it.
	mustClick(t, h, fileRowLabel("chain/deep/down/alpha.txt"))
	h.drain()
	h.ui.Key(0, ui.KeyUp)
	h.ui.Frame()
	if hv.treeFocus != "chain/deep/down" {
		t.Fatalf("treeFocus = %q after Up, want chain/deep/down", hv.treeFocus)
	}
	if got := hv.diff.req.File.Path; got != "chain/deep/down/alpha.txt" {
		t.Fatalf("landing on the folder row reloaded the diff: path = %q", got)
	}

	h.ui.Key(0, ui.KeyDown)
	h.drain()
	if hv.treeFocus != "" {
		t.Fatalf("treeFocus = %q after Down back to the file, want empty", hv.treeFocus)
	}
	if got := hv.diff.req.File.Path; got != "chain/deep/down/alpha.txt" {
		t.Fatalf("diff path = %q after Down, want chain/deep/down/alpha.txt", got)
	}

	// Down again lands on beta, the next file row, and loads its diff.
	h.ui.Key(0, ui.KeyDown)
	h.drain()
	if got := hv.diff.req.File.Path; got != "chain/deep/down/beta.txt" {
		t.Fatalf("diff path = %q after the second Down, want chain/deep/down/beta.txt", got)
	}
}

func TestHistoryTreeRenameShowsNewPath(t *testing.T) {
	// A renamed file is filed under the path it has now, displaying its bare
	// new name with the original path after the arrow; nothing files it
	// under the folder it left.
	repo := newGitRepo(t, "histtree-rename")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.CommitFile("olddir/oldname.txt", "renamed\n", "file to rename")
	// git mv needs the target folder to exist on disk.
	if err := os.MkdirAll(filepath.Join(repo.Path(), "newdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("mv", "olddir/oldname.txt", "newdir/newname.txt")
	repo.Git("commit", "-m", "rename into folder")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "rename into folder")
	h.drain()
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()

	if _, ok := historyFolderShown(h, "Folder newdir, 1 file"); !ok {
		t.Fatalf("newdir folder row missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("newname.txt ← olddir/oldname.txt") {
		t.Fatalf("rename row missing the original path; texts=%q", h.ui.Texts())
	}
	if _, ok := historyFolderShown(h, "Folder olddir, 1 file"); ok {
		t.Fatal("the rename left a row in the folder it came from")
	}
	if !historyFileRowShown(h, "newdir/newname.txt") {
		t.Fatalf("the rename file row is missing under its new path; texts=%q", h.ui.Texts())
	}
}

func TestHistoryTreeLoopbackSSHFileDiff(t *testing.T) {
	// Loopback SSH: the tree of a commit in a repository reached through
	// sshd groups the files in folders, and clicking a file row loads its
	// diff over the same connection.
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "tree/inner/alpha.txt", []byte("ssh alpha\n"))
	srv.WriteFile(t, repo.Path, "tree/inner/beta.txt", []byte("ssh beta\n"))
	srv.RunGit(t, repo.Path, "add", "--", "tree/inner/alpha.txt", "tree/inner/beta.txt")
	srv.RunGit(t, repo.Path, "-c", "user.name=Branchline Test", "-c", "user.email=test@branchline.local", "commit", "-m", "ssh tree files")
	head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}
	mustClick(t, h, "History")
	h.ui.Frame()
	clickCommitRow(t, h, head, "ssh tree files")
	h.drain()

	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	if _, ok := historyFolderShown(h, "Folder tree/inner, 2 files"); !ok {
		t.Fatalf("SSH folder row missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, fileRowLabel("tree/inner/alpha.txt"))
	h.drain()
	if got := h.app.historyView.diff.req.File.Path; got != "tree/inner/alpha.txt" {
		t.Fatalf("SSH diff path = %q, want tree/inner/alpha.txt", got)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "ssh alpha") {
		t.Fatalf("SSH tree diff insertions = %q, want ssh alpha", ins)
	}
}
