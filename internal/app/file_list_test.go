package app

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fileListImportRepo builds two commits on main in one fast-import stream:
// a seed commit and one changing files files at once, named so their order
// in the changed-file list is fixed. It returns the fixture and the hashes
// of the seed and the big commit.
func fileListImportRepo(t *testing.T, name string, files int) (*gitRepoFixture, string, string) {
	t.Helper()
	repo := newGitRepo(t, name)
	var b strings.Builder
	for i := 1; i <= 2; i++ {
		subject := fmt.Sprintf("files-change %d", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(subject), subject)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		write := func(path, content string) {
			fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", path, len(content), content)
		}
		if i == 1 {
			write("seed.txt", "seed\n")
		} else {
			for j := 0; j < files; j++ {
				write(fmt.Sprintf("dir/f%05d.txt", j), fmt.Sprintf("file %d\n", j))
			}
		}
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = repo.Path()
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, out)
	}
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	base := strings.TrimSpace(repo.Git("rev-parse", "HEAD^"))
	return repo, base, head
}

// openBigCommitDetail opens the History detail of the big commit, whose
// changed-file list is then as long as files says.
func openBigCommitDetail(t *testing.T, name string, files int) (*testHarness, *gitRepoFixture, string, string) {
	t.Helper()
	repo, base, head := fileListImportRepo(t, name, files)
	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	h.drain()
	clickCommitRow(t, h, head, "files-change 2")
	h.drain()
	if got := len(h.app.historyView.files); got != files {
		t.Fatalf("fixture: files = %d, want %d", got, files)
	}
	return h, repo, base, head
}

// With a commit changing more files than the list shows, scrolling to the
// end and clicking the last file selects it and loads its diff, even though
// the row did not exist in the first frame.
func TestChangedFileListScrollToBottomClickLastFile(t *testing.T) {
	h, _, _, _ := openBigCommitDetail(t, "cfl-bottom", 2000)
	hv := &h.app.historyView

	last := len(hv.files) - 1
	hv.fileList.ScrollTo(last, ui.End)
	h.ui.Frame()
	h.ui.Frame()

	lastPath := hv.files[last].Path
	if err := h.ui.Click("File " + lastPath); err != nil {
		t.Fatalf("click File %s: %v\ntexts=%q", lastPath, err, h.ui.Texts())
	}
	h.drain()

	if hv.fileSel != last {
		t.Fatalf("fileSel = %d, want %d (the last file)", hv.fileSel, last)
	}
	if hv.diff.req.File.Path != lastPath {
		t.Fatalf("diff path = %q, want %q", hv.diff.req.File.Path, lastPath)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File "+lastPath); !ok || !row.Selected {
		t.Fatalf("last file row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); len(ins) == 0 {
		t.Fatal("diff panel shows no insertions for the last file")
	}
}

// The file rows of the History commit detail keep their context menu:
// File History opens the file inspector for the row's file.
func TestChangedFileListHistoryRowContextMenu(t *testing.T) {
	h, _, _, _ := openBigCommitDetail(t, "cfl-menu", 20)
	hv := &h.app.historyView
	path := hv.files[0].Path

	chooseFileMenuItem(t, h, fileRowLabel(path), "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.originPage != pageHistory || st.path != path {
		t.Fatalf("inspector open=%v origin=%v path=%q, want history file history of %q", st.open, st.originPage, st.path, path)
	}
}

// The file rows of the comparison offer only the external diff entry of a
// context menu — File History and Blame belong to one commit, not to a
// range — while the rows of the search detail carry none: right-clicking
// them opens nothing.
func TestChangedFileListCompareAndSearchRowsHaveNoMenu(t *testing.T) {
	h, _, base, head := openBigCommitDetail(t, "cfl-nomenu", 20)

	selectComparePair(t, h, head, "files-change 2", base, "files-change 1")
	cmp := &h.app.historyView.compare
	if len(cmp.files) == 0 {
		t.Fatal("fixture: compare file list is empty")
	}
	if err := h.ui.RightClick("File " + cmp.files[0].Path); err != nil {
		t.Fatalf("right-click compare file row: %v", err)
	}
	h.ui.Frame()
	if menu := h.ui.Menu(); !slices.Equal(menu, []string{"Open in External Diff Tool"}) {
		t.Fatalf("compare file row menu = %q, want only the external diff entry", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	openSearchView(t, h)
	submitSearchQuery(t, h, "files-change")
	st := searchState(h)
	if len(st.files) == 0 {
		t.Fatal("fixture: search file list is empty")
	}
	if err := h.ui.RightClick("File " + st.files[0].Path); err != nil {
		t.Fatalf("right-click search file row: %v", err)
	}
	h.ui.Frame()
	if menu := h.ui.Menu(); len(menu) != 0 {
		t.Fatalf("search file row opened a menu %q, want none", menu)
	}
}

// Choosing a file deep in the list and then another commit resets the
// selection to the first file and scrolls the list back to its top.
func TestChangedFileListSwitchCommitResetsScrollAndSelection(t *testing.T) {
	h, _, base, _ := openBigCommitDetail(t, "cfl-reset", 2000)
	hv := &h.app.historyView

	hv.fileList.ScrollTo(1500, ui.Center)
	h.ui.Frame()
	h.ui.Frame()

	path1500 := hv.files[1500].Path
	if err := h.ui.Click("File " + path1500); err != nil {
		t.Fatalf("click File %s: %v\ntexts=%q", path1500, err, h.ui.Texts())
	}
	h.drain()
	if hv.fileSel != 1500 {
		t.Fatalf("setup: fileSel = %d, want 1500", hv.fileSel)
	}
	if first, _ := hv.fileList.Visible(); first == 0 {
		t.Fatal("setup: list did not scroll away from its first row")
	}

	clickCommitRow(t, h, base, "files-change 1")
	h.drain()

	if len(hv.files) != 1 {
		t.Fatalf("files of the seed commit = %d, want 1", len(hv.files))
	}
	if hv.fileSel != 0 {
		t.Fatalf("fileSel after switching commits = %d, want 0", hv.fileSel)
	}
	if hv.diff.req.File.Path != hv.files[0].Path {
		t.Fatalf("diff path = %q, want the first file %q", hv.diff.req.File.Path, hv.files[0].Path)
	}
	if first, _ := hv.fileList.Visible(); first != 0 {
		t.Fatalf("list first visible row = %d, want 0 (scrolled back to the top)", first)
	}
}

// A renamed file shows both its old and its new path in the shared list,
// and the row keeps the new path as its accessible name.
func TestChangedFileListRenameShowsBothPaths(t *testing.T) {
	repo := newGitRepo(t, "cfl-rename")
	repo.CommitFile("before.txt", "rename me\n", "add before")
	repo.Git("mv", "before.txt", "after.txt")
	repo.Git("commit", "-m", "rename file")
	head := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, head, "rename file")
	h.drain()

	if !h.ui.HasText("before.txt → after.txt") {
		t.Fatalf("rename row missing both paths; texts=%q", h.ui.Texts())
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "File after.txt"); !ok || !row.Selected {
		t.Fatalf("rename row access = %+v (found=%v), want the new path selected", row, ok)
	}
}

// Rapid arrow-key navigation in the shared changed-file list settles on the
// final file and loads its diff without getting stuck on intermediate rows.
func TestChangedFileListArrowKeysSelectFinalFile(t *testing.T) {
	h, _, _, _ := openBigCommitDetail(t, "cfl-arrows", 20)
	hv := &h.app.historyView

	if err := h.ui.Click("File " + hv.files[0].Path); err != nil {
		t.Fatalf("click first file to focus list: %v", err)
	}
	for range 5 {
		h.ui.Key(0, ui.KeyDown)
	}
	h.drain()

	if hv.fileSel != 5 {
		t.Fatalf("fileSel after 5 Down presses = %d, want 5", hv.fileSel)
	}
	if got, want := hv.diff.req.File.Path, hv.files[5].Path; got != want {
		t.Fatalf("diff path = %q, want %q", got, want)
	}
}
