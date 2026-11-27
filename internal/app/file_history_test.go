package app

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// fileRowLabel is the accessibility label of the row of one changed file in
// the commit detail of the History page.
func fileRowLabel(path string) string { return "File " + path }

// fileCommitRowLabel is the accessibility label of one commit row of the
// file inspector's File History mode.
func fileCommitRowLabel(hash, subject string) string {
	return "File Commit " + short7(hash) + " " + subject
}

// chooseFileMenuItem opens the context menu of the file row labeled rowLabel
// and chooses item, as a user would with the secondary button.
func chooseFileMenuItem(t *testing.T, h *testHarness, rowLabel, item string) {
	t.Helper()
	if err := h.ui.RightClick(rowLabel); err != nil {
		t.Fatalf("right-click %q: %v\ntexts=%q", rowLabel, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q in menu of %q: %v", item, rowLabel, err)
	}
}

// fastImportFileCommits appends n commits modifying file to the fixture in
// one fast-import stream, so pagination tests need no hundreds of shell-outs.
// Commit subjects are "<prefix> <i>" counting upward.
func fastImportFileCommits(t *testing.T, repo *gitRepoFixture, file, prefix string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		subject := fmt.Sprintf("%s %d", prefix, i)
		content := fmt.Sprintf("%s content %d\n", prefix, i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(subject), subject)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline %s\ndata %d\n%s\n", file, len(content), content)
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
}

// countExactText counts the rendered texts exactly equal to s.
func countExactText(tt *ui.Tester, s string) int {
	n := 0
	for _, text := range tt.Texts() {
		if text == s {
			n++
		}
	}
	return n
}

// assertBlameLinesMatchPorcelain compares the inspector's blame lines with
// the parsing of git blame --porcelain output line by line.
func assertBlameLinesMatchPorcelain(t *testing.T, got []git.BlameLine, porcelain string) {
	t.Helper()
	want := git.ParseBlame(porcelain)
	if len(got) != len(want) {
		t.Fatalf("blame lines = %d, git blame --porcelain lines = %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("blame line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Opening File History from the context menu of an unstaged file of the
// Local Changes page replaces the page's split with the inspector: it lists
// the commits that changed the file newest-first with the newest commit
// chosen, and the shared diff panel shows that commit's diff of the file.
func TestFileHistoryOpensFromChangesUnstagedFile(t *testing.T) {
	repo := newGitRepo(t, "fh-changes-open")
	first := repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "bump v")
	second := headOf(t, repo)
	repo.WriteFile("code.go", "package main\n\nvar v = 3\n")

	h := openChangesHarness(t, repo)

	headBefore := headOf(t, repo)
	statusBefore := repo.Git("status", "--porcelain")

	chooseFileMenuItem(t, h, "Unstaged code.go", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.path != "code.go" || st.originPage != pageChanges {
		t.Fatalf("inspector state open=%v path=%q origin=%v, want open file history of code.go from changes", st.open, st.path, st.originPage)
	}
	if len(st.histCommits) != 2 {
		t.Fatalf("file history commits = %+v, want 2", st.histCommits)
	}
	if st.histCommits[0].Hash != second || st.histCommits[1].Hash != first {
		t.Fatalf("file history order = %s then %s, want %s then %s",
			st.histCommits[0].Hash, st.histCommits[1].Hash, second, first)
	}

	texts := h.ui.Texts()
	idxSecond := slices.Index(texts, fileCommitRowLabel(second, "bump v"))
	idxFirst := slices.Index(texts, fileCommitRowLabel(first, "create code"))
	if idxSecond < 0 || idxFirst < 0 {
		t.Fatalf("missing file commit rows: second=%d first=%d; texts=%q", idxSecond, idxFirst, texts)
	}
	if idxSecond >= idxFirst {
		t.Fatalf("file commit rows out of order: second=%d first=%d", idxSecond, idxFirst)
	}
	// The newest commit is auto-selected, exposed in the accessibility tree.
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, fileCommitRowLabel(second, "bump v")); !ok || !row.Selected {
		t.Fatalf("auto-selected file commit row access = %+v (found=%v), want Selected=true", row, ok)
	}
	// The diff of the file in the selected commit renders its changed lines.
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "var v = 2") {
		t.Fatalf("file history diff insertions = %q, want var v = 2", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); !slices.Contains(del, "var v = 1") {
		t.Fatalf("file history diff deletions = %q, want var v = 1", del)
	}

	// Opening the inspector and browsing it is read-only: HEAD and the
	// working tree are exactly as before.
	if got := headOf(t, repo); got != headBefore {
		t.Fatalf("HEAD moved while browsing file history: %s -> %s", headBefore, got)
	}
	if got := repo.Git("status", "--porcelain"); got != statusBefore {
		t.Fatalf("working tree changed while browsing file history: %q -> %q", statusBefore, got)
	}
}

// Opening Blame from the context menu of a Local Changes file annotates the
// file at HEAD: every line matches git blame --porcelain, a run of lines of
// one commit shows its subject once, and choosing any line — including one
// with empty commit cells — fills the bottom bar with its commit.
func TestFileBlameOpensFromChangesAndFoldsRuns(t *testing.T) {
	repo := newGitRepo(t, "fb-changes-open")
	repo.CommitFile("code.txt", "alpha\nbeta\ngamma\n", "create code")
	repo.WriteFile("code.txt", "alpha CHANGED\nbeta\ngamma\n")
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "change alpha")
	// An unstaged modification makes the file row visible in Local Changes;
	// the blame still reads the committed HEAD content.
	repo.WriteFile("code.txt", "alpha CHANGED\nbeta\ngamma\nuncommitted\n")

	h := openChangesHarness(t, repo)

	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.mode != inspectorModeBlame {
		t.Fatalf("inspector mode = %d (open=%v), want blame", st.mode, st.open)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
	if len(st.blameLines) != 3 {
		t.Fatalf("blame lines = %d, want 3 (HEAD content, not the uncommitted line)", len(st.blameLines))
	}
	// Lines 2-3 form one run of "create code": it marks the run's first line
	// and leaves the second one's commit cells empty.
	if len(st.blameLines) >= 3 {
		if !st.blameLines[1].First || st.blameLines[2].First {
			t.Fatalf("fixture: run marks First=%v,%v, want true,false", st.blameLines[1].First, st.blameLines[2].First)
		}
	}

	// Each commit's subject shows exactly once per run of its lines: "create
	// code" covers lines 2-3 in one run, "change alpha" one line.
	for _, subject := range []string{"create code", "change alpha"} {
		if n := countExactText(h.ui, subject); n != 1 {
			t.Fatalf("subject %q rendered %d times, want exactly 1 for its run", subject, n)
		}
	}
	if !h.ui.HasText("alpha CHANGED") || !h.ui.HasText("gamma") {
		t.Fatalf("blame missing line text; texts=%q", h.ui.Texts())
	}

	// Choosing line 3 — the second line of the "create code" run, whose
	// commit cells are empty — fills the bottom bar with its commit.
	if err := h.ui.Click("Blame Line 3"); err != nil {
		t.Fatalf("click Blame Line 3: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, "Blame Line 3"); !ok || !row.Selected {
		t.Fatalf("blame line 3 row access = %+v (found=%v), want Selected=true", row, ok)
	}
	if st.blameSel != 2 {
		t.Fatalf("blame selection = %d, want 2", st.blameSel)
	}
	info := st.blameLines[st.blameSel]
	if info.Subject != "create code" {
		t.Fatalf("line 3 belongs to %q, want create code", info.Subject)
	}
	if info.First {
		t.Fatal("fixture: line 3 must be a continuation line with empty commit cells")
	}
	if !h.ui.HasText(short7(info.Hash)) {
		t.Fatalf("bottom bar missing short hash %q; texts=%q", short7(info.Hash), h.ui.Texts())
	}
	if !h.ui.HasText("Branchline Test") {
		t.Fatalf("bottom bar missing author; texts=%q", h.ui.Texts())
	}
	if countExactText(h.ui, "create code") < 2 {
		t.Fatalf("bottom bar missing subject of line 3's commit; texts=%q", h.ui.Texts())
	}
}

// Opening File History and Blame from the context menu of a changed-file row
// in the History page's commit detail pins the blame at the shown commit —
// not at HEAD — and a later commit not touching the file is absent from the
// file's history.
func TestFileHistoryAndBlameOpenFromHistoryCommitRow(t *testing.T) {
	repo := newGitRepo(t, "fh-history-open")
	repo.CommitFile("target.txt", "l1\nl2\n", "add target")
	second := repo.CommitFile("target.txt", "l1\nl2\nl3\n", "extend target")
	repo.CommitFile("other.txt", "o\n", "add other")
	// A commit touching target after the one being inspected, so blame at
	// HEAD visibly differs from blame at the inspected commit.
	last := repo.CommitFile("target.txt", "l1\nl2 changed\nl3\nl4\n", "rework target")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, second, "extend target")
	h.drain()
	if len(h.app.historyView.files) != 1 {
		t.Fatalf("files of extend target = %+v, want exactly one", h.app.historyView.files)
	}

	// File History of target.txt lists exactly the commits that changed it;
	// "add other" never touched it.
	chooseFileMenuItem(t, h, fileRowLabel("target.txt"), "File History…")
	h.drain()
	st := h.app.fileInspectorUI()
	if !st.open || st.originPage != pageHistory {
		t.Fatalf("inspector origin = %v (open=%v), want history page", st.originPage, st.open)
	}
	var subjects []string
	for _, cm := range st.histCommits {
		subjects = append(subjects, cm.Subject)
	}
	wantSubjects := []string{"rework target", "extend target", "add target"}
	if !slices.Equal(subjects, wantSubjects) {
		t.Fatalf("file history subjects = %q, want %q", subjects, wantSubjects)
	}
	if slices.Contains(subjects, "add other") {
		t.Fatal("unrelated commit listed in the file's history")
	}

	// Closing with the button returns to the commit detail the inspector
	// was opened from.
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("click Close File Inspector: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if st.open {
		t.Fatal("inspector still open after Close")
	}
	if !h.ui.HasText("File target.txt") && !h.ui.HasText("target.txt") {
		t.Fatalf("commit detail file list not visible after close; texts=%q", h.ui.Texts())
	}

	// Blame pins the revision at the shown commit: line 4 does not exist
	// there yet, and the badge carries the commit's short hash.
	chooseFileMenuItem(t, h, fileRowLabel("target.txt"), "Blame…")
	h.drain()
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", second, "--", "target.txt"))
	if len(st.blameLines) != 3 {
		t.Fatalf("blame lines at extend target = %d, want 3 (l4 arrived later)", len(st.blameLines))
	}
	if st.blameRev != second {
		t.Fatalf("blame rev = %q, want the shown commit %q", st.blameRev, second)
	}
	if !h.ui.HasText(short7(second)) {
		t.Fatalf("blame revision badge missing %q; texts=%q", short7(second), h.ui.Texts())
	}
	// Blame at HEAD would annotate l1 of "rework target"; at the pinned
	// commit every line is older.
	for _, l := range st.blameLines {
		if l.Hash == last {
			t.Fatalf("blame line %+v belongs to the later commit: not pinned at the shown commit", l)
		}
	}
}

// For a file the shown commit deleted, Blame falls back to the commit's
// first parent — the revision where the file still exists — while File
// History still walks HEAD. Esc closes the inspector like the Close button.
func TestFileBlameDeletedFileFallsBackToParent(t *testing.T) {
	repo := newGitRepo(t, "fb-deleted")
	lines := repo.CommitFile("file2.txt", "one\ntwo\nthree\n", "write lines")
	more := repo.CommitFile("file3.txt", "x\ny\n", "write more")
	repo.Git("rm", "-q", "file2.txt")
	repo.WriteFile("file3.txt", "x\ny\nz\n")
	repo.Git("add", "file3.txt")
	repo.Git("commit", "-m", "touch both")
	touch := headOf(t, repo)

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, touch, "touch both")
	h.drain()

	// File History of the deleted file still lists the commits that changed
	// it over its lifetime.
	chooseFileMenuItem(t, h, fileRowLabel("file2.txt"), "File History…")
	h.drain()
	st := h.app.fileInspectorUI()
	if len(st.histCommits) != 2 || st.histCommits[0].Hash != touch || st.histCommits[1].Hash != lines {
		t.Fatalf("deleted file history = %+v, want [touch both, write lines]", st.histCommits)
	}

	// Esc closes the inspector back to the commit detail.
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	if st.open {
		t.Fatal("inspector still open after Escape")
	}
	if h.app.historyView.sel != touch {
		t.Fatalf("history selection moved while inspecting: %q", h.app.historyView.sel)
	}

	// Blame annotates the file at the first parent — the "write more"
	// commit — where it still exists.
	chooseFileMenuItem(t, h, fileRowLabel("file2.txt"), "Blame…")
	h.drain()
	if st.blameRev != more {
		t.Fatalf("blame rev for deleted file = %q, want first parent %q", st.blameRev, more)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", more, "--", "file2.txt"))
	if len(st.blameLines) != 3 {
		t.Fatalf("blame lines at parent = %d, want 3", len(st.blameLines))
	}
	if !h.ui.HasText(short7(more)) {
		t.Fatalf("badge missing parent short hash %q; texts=%q", short7(more), h.ui.Texts())
	}
	if !h.ui.HasText("three") {
		t.Fatalf("blame missing line text of the pre-deletion content; texts=%q", h.ui.Texts())
	}
}

// The mode switch flips the same file between File History and Blame in
// place without closing the inspector, keeping data already loaded, and the
// diff panel's Split/Unified toggle works inside the inspector.
func TestFileHistoryModeSwitchInPlace(t *testing.T) {
	repo := newGitRepo(t, "fh-modeswitch")
	repo.CommitFile("code.txt", "alpha\nbeta\n", "create code")
	repo.WriteFile("code.txt", "alpha\nbeta\ngamma\n")
	repo.Git("add", "code.txt")
	repo.Git("commit", "-m", "add gamma")
	repo.WriteFile("code.txt", "alpha\nbeta\ngamma\ndelta\n")

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	loadedCommits := len(st.histCommits)
	if loadedCommits != 2 {
		t.Fatalf("setup: file history commits = %d, want 2", loadedCommits)
	}

	// History -> Blame: the blame read starts at once and the diff panel is
	// no longer on screen.
	if err := h.ui.Click("Blame"); err != nil {
		t.Fatalf("click Blame segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if st.mode != inspectorModeBlame {
		t.Fatalf("mode after Blame click = %d, want blame", st.mode)
	}
	if !st.open || st.path != "code.txt" {
		t.Fatalf("inspector closed or changed file on mode switch: open=%v path=%q", st.open, st.path)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "code.txt"))
	if h.ui.HasText("Loading file history…") {
		t.Fatal("file history view still rendered in blame mode")
	}

	// Blame -> History: the commits loaded before the switch are still
	// there, with no fresh read needed.
	if err := h.ui.Click("File History"); err != nil {
		t.Fatalf("click File History segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if st.mode != inspectorModeHistory {
		t.Fatalf("mode after File History click = %d, want history", st.mode)
	}
	if len(st.histCommits) != loadedCommits {
		t.Fatalf("file history commits = %d after switch back, want the %d already loaded", len(st.histCommits), loadedCommits)
	}
	if !h.ui.HasText("add gamma") {
		t.Fatalf("file history rows missing after switch back; texts=%q", h.ui.Texts())
	}

	// The shared diff panel inside the inspector honors the layout toggle.
	if st.diff.Layout() != 0 { // editor.DiffSplit is the zero value
		t.Fatalf("default inspector diff layout = %v, want split", st.diff.Layout())
	}
	if err := h.ui.Click("Unified"); err != nil {
		t.Fatalf("click Unified: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "gamma") {
		t.Fatalf("unified inspector diff missing insertion: %q", ins)
	}
}

// With more commits than one page, the inspector offers Load More File
// History; clicking it grows the walk by one page, and while the read is in
// flight the button is disabled.
func TestFileHistoryLoadMorePagination(t *testing.T) {
	repo := newGitRepo(t, "fh-loadmore")
	fastImportFileCommits(t, repo, "f.txt", "imp", 105)
	// fast-import updates refs without the index; reset it to HEAD so the
	// working-tree modification below reads as an unstaged change.
	repo.Git("reset")
	// An unstaged modification makes the file visible in Local Changes.
	repo.WriteFile("f.txt", "imp content 105\ndirty\n")

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged f.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if len(st.histCommits) != 100 {
		t.Fatalf("first page = %d commits, want 100", len(st.histCommits))
	}
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More File History")
	if !ok {
		t.Fatalf("Load More File History missing; texts=%q", h.ui.Texts())
	}
	if btn.Disabled {
		t.Fatal("Load More File History disabled while idle")
	}

	h.queue.PauseWorkers()
	if err := h.ui.Click("Load More File History"); err != nil {
		t.Fatalf("click Load More File History: %v", err)
	}
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Load More File History"); ok && !btn.Disabled {
		t.Fatal("Load More File History not disabled during the read")
	}
	worker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(worker)
	h.drain()

	if len(st.histCommits) != 105 {
		t.Fatalf("commits after Load More = %d, want all 105", len(st.histCommits))
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Load More File History"); ok {
		t.Fatal("Load More File History still offered after the tail of the walk")
	}
	// The list is virtualized: keyboard-end reveals the oldest commit.
	clickCommit := st.histCommits[0]
	if err := h.ui.Click(fileCommitRowLabel(clickCommit.Hash, clickCommit.Subject)); err != nil {
		t.Fatalf("click first file commit row: %v", err)
	}
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEnd)
	h.ui.Frame()
	if !hasExactText(h.ui, fileCommitRowLabel(st.histCommits[len(st.histCommits)-1].Hash, "imp 1")) {
		t.Fatalf("oldest commit missing after Load More; texts=%q", h.ui.Texts())
	}
}

// A file that only exists on another branch — visible in the History page
// because the commit walk includes every ref — has no commits in HEAD's
// ancestry, so its File History reads cleanly empty rather than failing.
func TestFileHistoryEmptyForFileOutsideHeadAncestry(t *testing.T) {
	repo := newGitRepo(t, "fh-otherbranch")
	repo.CommitFile("main.txt", "m\n", "main commit")
	repo.Git("checkout", "-q", "-b", "side")
	side := repo.CommitFile("side.txt", "s\n", "side file")
	repo.Checkout("main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	clickCommitRow(t, h, side, "side file")
	h.drain()

	chooseFileMenuItem(t, h, fileRowLabel("side.txt"), "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if st.histErr != nil {
		t.Fatalf("file history error for other-branch file: %v", st.histErr)
	}
	if st.histCommits == nil || len(st.histCommits) != 0 {
		t.Fatalf("file history commits = %+v, want empty for a file outside HEAD ancestry", st.histCommits)
	}
	if !h.ui.HasText("No commits changed this file") {
		t.Fatalf("missing empty file-history state; texts=%q", h.ui.Texts())
	}
}

// A file-history read that fails shows the error with the original message
// and a retry; retrying after the cause is gone loads the commits.
func TestFileHistoryErrorAndRetry(t *testing.T) {
	repo := newGitRepo(t, "fh-error")
	repo.CommitFile("code.txt", "one\n", "create code")
	repo.WriteFile("code.txt", "one\ntwo\n")

	h := openChangesHarness(t, repo)

	objects := repo.Path() + "/.git/objects"
	backup := repo.Path() + "/.git/objects-backup"
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("hide objects: %v", err)
	}
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if st.histErr == nil {
		t.Fatal("expected file history error with missing objects")
	}
	if !h.ui.HasText("Failed to load file history") {
		t.Fatalf("missing file-history failure message; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(st.histErr.Error()) {
		t.Fatalf("error detail not rendered; texts=%q", h.ui.Texts())
	}

	if err := os.Rename(backup, objects); err != nil {
		t.Fatalf("restore objects: %v", err)
	}
	if err := h.ui.Click("Retry File History"); err != nil {
		t.Fatalf("click Retry File History: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if st.histErr != nil {
		t.Fatalf("file history error after retry: %v", st.histErr)
	}
	if len(st.histCommits) != 1 || st.histCommits[0].Subject != "create code" {
		t.Fatalf("commits after retry = %+v, want [create code]", st.histCommits)
	}
}

// A blame read that fails shows its error with a working retry, and a file
// committed empty reads as having no lines instead of an error.
func TestFileBlameErrorRetryAndEmptyFile(t *testing.T) {
	repo := newGitRepo(t, "fb-error")
	repo.CommitFile("code.txt", "one\n", "create code")
	repo.CommitFile("empty.txt", "", "add empty file")
	repo.WriteFile("empty.txt", "dirty\n")
	repo.WriteFile("code.txt", "one\ntwo\n")

	h := openChangesHarness(t, repo)

	// empty.txt is empty at HEAD however dirty the working tree is.
	chooseFileMenuItem(t, h, "Unstaged empty.txt", "Blame…")
	h.drain()
	st := h.app.fileInspectorUI()
	if st.blameErr != nil {
		t.Fatalf("blame of empty file errored: %v", st.blameErr)
	}
	if st.blameLines == nil || len(st.blameLines) != 0 {
		t.Fatalf("blame lines of empty file = %+v, want empty", st.blameLines)
	}
	if !h.ui.HasText("This file has no lines") {
		t.Fatalf("missing empty-file blame state; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v", err)
	}
	h.ui.Frame()

	objects := repo.Path() + "/.git/objects"
	backup := repo.Path() + "/.git/objects-backup"
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("hide objects: %v", err)
	}
	chooseFileMenuItem(t, h, "Unstaged code.txt", "Blame…")
	h.drain()
	if st.blameErr == nil {
		t.Fatal("expected blame error with missing objects")
	}
	if !h.ui.HasText("Failed to load blame") {
		t.Fatalf("missing blame failure message; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(st.blameErr.Error()) {
		t.Fatalf("blame error detail not rendered; texts=%q", h.ui.Texts())
	}

	if err := os.Rename(backup, objects); err != nil {
		t.Fatalf("restore objects: %v", err)
	}
	if err := h.ui.Click("Retry Blame"); err != nil {
		t.Fatalf("click Retry Blame: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if st.blameErr != nil {
		t.Fatalf("blame error after retry: %v", st.blameErr)
	}
	if len(st.blameLines) != 1 {
		t.Fatalf("blame lines after retry = %+v, want 1 line", st.blameLines)
	}
}

// Files that never entered a commit — untracked, ignored or newly added
// ones — and every file of a repository without commits offer neither File
// History nor Blame: both menu entries are disabled.
func TestFileHistoryBlameDisabledForUncommittedFiles(t *testing.T) {
	repo := newGitRepo(t, "fh-disabled")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")
	repo.WriteFile("added.txt", "added\n")
	repo.Git("add", "added.txt")

	h := openChangesHarness(t, repo)

	assertDisabled := func(rowLabel string) {
		t.Helper()
		if err := h.ui.RightClick(rowLabel); err != nil {
			t.Fatalf("right-click %q: %v\ntexts=%q", rowLabel, err, h.ui.Texts())
		}
		menu := h.ui.Menu()
		if !slices.Contains(menu, "File History…") || !slices.Contains(menu, "Blame…") {
			t.Fatalf("menu of %q = %q, want both entries present though disabled", rowLabel, menu)
		}
		for _, item := range []string{"File History…", "Blame…"} {
			if err := h.ui.ChooseMenuItem(item); err == nil {
				t.Fatalf("%q of %q is not disabled", item, rowLabel)
			}
			st := h.app.fileInspectorUI()
			if st.open {
				t.Fatalf("inspector opened from a disabled %q entry of %q", item, rowLabel)
			}
		}
		h.ui.CloseMenu()
		h.ui.Frame()
	}

	assertDisabled("Unstaged untracked.txt")
	assertDisabled("Staged added.txt")

	// A tracked modified file keeps both entries enabled.
	if err := h.ui.RightClick("Unstaged tracked.txt"); err != nil {
		t.Fatalf("right-click Unstaged tracked.txt: %v", err)
	}
	if err := h.ui.ChooseMenuItem("File History…"); err != nil {
		t.Fatalf("File History… of a tracked file is disabled: %v", err)
	}
	h.drain()
	if st := h.app.fileInspectorUI(); !st.open {
		t.Fatal("inspector did not open for a tracked file")
	}
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v", err)
	}
	h.ui.Frame()

	// In a repository without any commit, every file is uncommitted.
	unborn := newGitRepo(t, "fh-disabled-unborn")
	unborn.WriteFile("loose.txt", "x\n")
	uh := openChangesHarness(t, unborn)
	if err := uh.ui.RightClick("Unstaged loose.txt"); err != nil {
		t.Fatalf("right-click Unstaged loose.txt: %v\ntexts=%q", err, uh.ui.Texts())
	}
	for _, item := range []string{"File History…", "Blame…"} {
		if err := uh.ui.ChooseMenuItem(item); err == nil {
			t.Fatalf("%q of an unborn-repository file is not disabled", item)
		}
	}
	uh.ui.CloseMenu()
}

// While their reads are in flight, File History and Blame each show a
// loading state instead of a blank page or stale content.
func TestFileHistoryBlameShowsLoadingStates(t *testing.T) {
	repo := newGitRepo(t, "fh-loading")
	repo.CommitFile("code.txt", "one\n", "create code")
	repo.WriteFile("code.txt", "one\ntwo\n")

	h := openChangesHarness(t, repo)

	h.queue.PauseWorkers()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.ui.Frame()
	if !h.ui.HasText("Loading file history…") {
		t.Fatalf("missing file-history loading state; texts=%q", h.ui.Texts())
	}
	logWorker := h.queue.WaitWorker(t)

	if err := h.ui.Click("Blame"); err != nil {
		t.Fatalf("click Blame segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if !h.ui.HasText("Loading blame…") {
		t.Fatalf("missing blame loading state; texts=%q", h.ui.Texts())
	}
	blameWorker := h.queue.WaitWorker(t)

	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(logWorker)
	h.queue.ReleaseWorker(blameWorker)
	h.drain()

	st := h.app.fileInspectorUI()
	if len(st.blameLines) != 1 {
		t.Fatalf("blame lines after loading = %+v, want 1 line", st.blameLines)
	}
	if err := h.ui.Click("File History"); err != nil {
		t.Fatalf("click File History segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if len(st.histCommits) != 1 || st.histCommits[0].Subject != "create code" {
		t.Fatalf("file history after loading = %+v, want [create code]", st.histCommits)
	}
}

// A staged rename reads its history and blame through the path the file had
// in HEAD, because the new path does not exist there yet.
func TestFileHistoryStagedRenameUsesOriginalPath(t *testing.T) {
	repo := newGitRepo(t, "fh-rename")
	before := repo.CommitFile("before.txt", "rename me\nsecond line\n", "add before")
	repo.Git("mv", "before.txt", "after.txt")

	h := openChangesHarness(t, repo)

	st := h.app.session.Snapshot().Status
	if len(st.Staged) != 1 || st.Staged[0].Status != 'R' {
		t.Fatalf("fixture: staged = %+v, want one rename", st.Staged)
	}

	chooseFileMenuItem(t, h, "Staged after.txt", "File History…")
	h.drain()
	stu := h.app.fileInspectorUI()
	if stu.path != "before.txt" {
		t.Fatalf("inspector path of a staged rename = %q, want before.txt", stu.path)
	}
	if len(stu.histCommits) != 1 || stu.histCommits[0].Hash != before {
		t.Fatalf("rename file history = %+v, want [add before]", stu.histCommits)
	}

	if err := h.ui.Click("Blame"); err != nil {
		t.Fatalf("click Blame segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	assertBlameLinesMatchPorcelain(t, stu.blameLines,
		repo.Git("blame", "--porcelain", "-w", "HEAD", "--", "before.txt"))
	if !h.ui.HasText("rename me") {
		t.Fatalf("rename blame missing line text; texts=%q", h.ui.Texts())
	}
}

// Rapidly switching the selected file-history commit discards the slower
// first diff read: the view ends on the commit chosen last even though its
// worker finished first.
func TestFileHistoryRapidCommitSwitchDiscardsStaleDiff(t *testing.T) {
	repo := newGitRepo(t, "fh-race-diff")
	older := repo.CommitFile("code.txt", "v1\n", "version one")
	newer := repo.CommitFile("code.txt", "v2\n", "version two")
	repo.WriteFile("code.txt", "v2\ndirty\n")

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if len(st.histCommits) != 2 {
		t.Fatalf("setup: commits = %+v, want 2", st.histCommits)
	}

	// The newest commit is auto-selected with its diff loaded. Switch to the
	// older one (gated), then back to the newer before the worker finishes.
	h.queue.PauseWorkers()
	if err := h.ui.Click(fileCommitRowLabel(older, "version one")); err != nil {
		t.Fatalf("click older commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	workerOlder := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if err := h.ui.Click(fileCommitRowLabel(newer, "version two")); err != nil {
		t.Fatalf("click newer commit row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.queue.RunNext(t) // the newer diff applies first
	h.ui.Frame()

	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "v2") {
		t.Fatalf("newer diff not applied first: insertions=%q", ins)
	}

	h.queue.ReleaseWorker(workerOlder)
	h.drain()

	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "v2") {
		t.Fatalf("stale diff overwrote the newer commit: insertions=%q", ins)
	}
	if st.diff.req.Commit.Hash != newer {
		t.Fatalf("active diff commit = %q, want %q", st.diff.req.Commit.Hash, newer)
	}
}

// Switching from File History to Blame while the commit read is in flight
// discards that read when it lands: no commit list, no auto-selection and no
// chained diff read belong in the blame view, and switching back starts the
// history read over.
func TestFileHistoryModeSwitchDiscardsStaleReads(t *testing.T) {
	repo := newGitRepo(t, "fh-race-mode")
	repo.CommitFile("code.txt", "one\n", "create code")
	repo.WriteFile("code.txt", "one\ntwo\n")

	h := openChangesHarness(t, repo)

	h.queue.PauseWorkers()
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.ui.Frame()
	logWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Switch to Blame before the log read lands; let the blame read finish
	// first, then release the stale log read.
	if err := h.ui.Click("Blame"); err != nil {
		t.Fatalf("click Blame segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	h.queue.RunNext(t) // blame applies
	h.ui.Frame()

	st := h.app.fileInspectorUI()
	if st.blameLines == nil {
		t.Fatal("setup: blame did not load first")
	}

	h.queue.ReleaseWorker(logWorker)
	h.drain()

	if st.histCommits != nil {
		t.Fatalf("stale log read applied in blame mode: %+v", st.histCommits)
	}
	if st.diff.req.Kind != diffNone {
		t.Fatalf("stale log read chained a diff load: %+v", st.diff.req)
	}

	// Switching back to File History restarts the read and lands cleanly.
	if err := h.ui.Click("File History"); err != nil {
		t.Fatalf("click File History segment: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if len(st.histCommits) != 1 || st.histCommits[0].Subject != "create code" {
		t.Fatalf("file history after switching back = %+v, want [create code]", st.histCommits)
	}
}

// Inspecting another file before the first file's read lands discards the
// first read: the inspector ends on the second file even though its read
// finished first.
func TestFileHistoryFileSwitchDiscardsStaleReads(t *testing.T) {
	repo := newGitRepo(t, "fh-race-file")
	repo.CommitFile("aaa.txt", "a1\n", "create aaa")
	repo.CommitFile("bbb.txt", "b1\n", "create bbb")
	repo.WriteFile("aaa.txt", "a1\na2\n")
	repo.WriteFile("bbb.txt", "b1\nb2\n")

	h := openChangesHarness(t, repo)

	h.queue.PauseWorkers()
	chooseFileMenuItem(t, h, "Unstaged aaa.txt", "File History…")
	h.ui.Frame()
	workerA := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// Open bbb.txt's history before aaa.txt's read lands; bbb's read is not
	// gated and finishes first.
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	chooseFileMenuItem(t, h, "Unstaged bbb.txt", "File History…")
	h.queue.RunNext(t) // bbb's log applies first
	h.ui.Frame()

	st := h.app.fileInspectorUI()
	if st.path != "bbb.txt" {
		t.Fatalf("setup: inspector path = %q, want bbb.txt", st.path)
	}
	if len(st.histCommits) != 1 || st.histCommits[0].Subject != "create bbb" {
		t.Fatalf("setup: commits = %+v, want [create bbb]", st.histCommits)
	}

	h.queue.ReleaseWorker(workerA)
	h.drain()

	if st.path != "bbb.txt" || len(st.histCommits) != 1 || st.histCommits[0].Subject != "create bbb" {
		t.Fatalf("stale aaa.txt read polluted the inspector: path=%q commits=%+v", st.path, st.histCommits)
	}
	for _, cm := range st.histCommits {
		if cm.Subject == "create aaa" {
			t.Fatal("aaa.txt commit listed in bbb.txt's inspector")
		}
	}
}

// Switching repositories with inspector reads in flight closes the inspector
// and drops the late results of the old repository: nothing of it survives
// in the new one.
func TestFileHistoryRepoSwitchDropsLateReadsAndCloses(t *testing.T) {
	repoA := newGitRepo(t, "fh-swap-a")
	repoA.CommitFile("only-a.txt", "a1\n", "commit in alpha")
	repoA.WriteFile("only-a.txt", "a1\na2\n")
	repoB := newGitRepo(t, "fh-swap-b")
	repoB.CommitFile("only-b.txt", "b1\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if h.app.route.page != pageChanges {
		t.Fatalf("initial page = %v, want pageChanges", h.app.route.page)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial snapshot load failed: %v", err)
	}

	h.queue.PauseWorkers()
	chooseFileMenuItem(t, h, "Unstaged only-a.txt", "Blame…")
	h.ui.Frame()
	blameWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("fh-swap-b"); err != nil {
		t.Fatalf("select fh-swap-b: %v", err)
	}
	h.ui.Frame()

	h.queue.ReleaseWorker(blameWorker)
	h.drain()

	st := h.app.fileInspectorUI()
	if st.open {
		t.Fatalf("inspector still open after the repository switch settled: path=%q", st.path)
	}
	if len(st.blameLines) != 0 {
		t.Fatalf("stale blame lines survived the repository switch: %+v", st.blameLines)
	}
	if h.ui.HasText("alpha") {
		t.Fatalf("old repository content rendered after switch; texts=%q", h.ui.Texts())
	}
	if h.app.session.Meta().Path != repoB.Path() || !h.app.session.Loaded() {
		t.Fatalf("session after switch = %q (loaded=%v), want fh-swap-b loaded", h.app.session.Meta().Path, h.app.session.Loaded())
	}
}

// The inspector renders only on the page it was opened from: navigating to
// the other page shows that page plain, and navigating back shows the
// inspector again, which Close then leaves for the origin page's content.
func TestFileHistoryRendersOnlyOnOriginPage(t *testing.T) {
	repo := newGitRepo(t, "fh-pages")
	repo.CommitFile("code.txt", "one\n", "create code")
	repo.WriteFile("code.txt", "one\ntwo\n")

	h := openChangesHarness(t, repo)
	chooseFileMenuItem(t, h, "Unstaged code.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.originPage != pageChanges {
		t.Fatalf("setup: open=%v origin=%v, want inspector from changes", st.open, st.originPage)
	}

	// History shows its own commit list, no inspector.
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("1 commits") && !h.ui.HasText("Commit History") {
		t.Fatalf("history page not visible while inspector open on changes; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Close File Inspector") {
		t.Fatal("inspector rendered on a page it was not opened from")
	}

	// Back on Changes the inspector is still there, and closing it returns
	// the file lists and the commit box.
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Close File Inspector") {
		t.Fatalf("inspector missing back on its origin page; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("Unstaged") {
		t.Fatalf("changes file lists not restored after close; texts=%q", h.ui.Texts())
	}
}

// --- SSH loopback ---

// openSSHRepoInApp opens a loopback SSH repository through the Repository
// Switcher, drains the snapshot load, and opens the inspector through the
// real context-menu entry of one of its files.
func openSSHRepoInApp(t *testing.T, h *testHarness, repo store.Repo) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(repo.Name); err != nil {
		t.Fatalf("select SSH repo %q: %v", repo.Name, err)
	}
	h.ui.Frame()
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("SSH snapshot not loaded")
	}
}

// sshHistoryFixture builds a small linear history of history.txt on the
// loopback server, with a dirty working tree so the file shows in Local
// Changes, plus the deletion of deleted.txt in its last commit.
func sshHistoryFixture(t *testing.T, srv *sshtest.Target, dir string) {
	t.Helper()
	q := git.ShellQuote
	srv.RunShell(t, "set -eu; cd "+q(dir)+`
printf 'one\ntwo\nthree\n' > history.txt
git add history.txt
git commit -q -m 'create history'
printf 'one\ntwo\nthree\nfour\n' > history.txt
git commit -q -am 'add line four'
printf 'one changed\ntwo\nthree\nfour\n' > history.txt
git commit -q -am 'change line one'
printf 'to be deleted\n' > deleted.txt
git add deleted.txt
git commit -q -m 'create deleted'
printf 'other one\n' > other.txt
git add other.txt
git commit -q -m 'create other'
git rm -q deleted.txt
git commit -q -m 'remove deleted'
printf 'one changed\ntwo\nthree\nfour\nuncommitted\n' > history.txt
`)
}

// Over a real loopback SSH connection, File History opened from Local
// Changes lists exactly the commits of git log for the file and switching
// the chosen commit loads that commit's diff of the file.
func TestFileHistoryLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	sshHistoryFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHRepoInApp(t, h, repo)

	// history.txt has uncommitted modifications, so it sits in Unstaged.
	chooseFileMenuItem(t, h, "Unstaged history.txt", "File History…")
	h.drain()

	st := h.app.fileInspectorUI()
	if !st.open || st.path != "history.txt" {
		t.Fatalf("inspector state open=%v path=%q over SSH", st.open, st.path)
	}

	// The commit list matches git log for the file exactly.
	wantOut := srv.RunGit(t, repo.Path, "log", "--format=%H%x00%s", "HEAD", "--", "history.txt")
	var wantHashes, wantSubjects []string
	for _, line := range strings.Split(wantOut, "\n") {
		hash, subject, _ := strings.Cut(line, "\x00")
		wantHashes = append(wantHashes, hash)
		wantSubjects = append(wantSubjects, subject)
	}
	if len(st.histCommits) != len(wantHashes) {
		t.Fatalf("SSH file history commits = %d, git log = %d", len(st.histCommits), len(wantHashes))
	}
	for i, cm := range st.histCommits {
		if cm.Hash != wantHashes[i] || cm.Subject != wantSubjects[i] {
			t.Fatalf("SSH file history commit %d = %s %q, want %s %q", i, cm.Hash, cm.Subject, wantHashes[i], wantSubjects[i])
		}
	}
	if len(wantSubjects) != 3 {
		t.Fatalf("fixture: git log subjects = %q, want exactly the 3 of history.txt", wantSubjects)
	}
	for _, subject := range wantSubjects {
		if !h.ui.HasText(subject) {
			t.Fatalf("SSH file history missing subject %q; texts=%q", subject, h.ui.Texts())
		}
	}

	// The newest commit is auto-selected and its diff of the file renders.
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "one changed") {
		t.Fatalf("SSH diff of newest commit missing insertion: %q", ins)
	}

	// Switching to "add line four" replaces the diff with that commit's:
	// four arrives, nothing is deleted.
	second := wantHashes[1]
	if err := h.ui.Click(fileCommitRowLabel(second, "add line four")); err != nil {
		t.Fatalf("click add line four row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !slices.Contains(ins, "four") {
		t.Fatalf("SSH diff of add line four missing insertion: %q", ins)
	}
	if del := diffAccessLines(h.ui, ui.RoleDeletion); len(del) != 0 {
		t.Fatalf("SSH diff of add line four has unexpected deletions: %q", del)
	}
	if row, ok := h.ui.AccessByRole(ui.RoleListItem, fileCommitRowLabel(second, "add line four")); !ok || !row.Selected {
		t.Fatalf("SSH selected file commit row access = %+v (found=%v), want Selected=true", row, ok)
	}
}

// Over a real loopback SSH connection, Blame annotates every line exactly as
// git blame --porcelain does, and for a file the shown commit deleted Blame
// falls back to the commit's first parent.
func TestFileBlameLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	sshHistoryFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	openSSHRepoInApp(t, h, repo)

	// Blame of the modified file at HEAD matches the porcelain output line
	// by line: numbers, hashes, subjects, texts and run-first marks.
	chooseFileMenuItem(t, h, "Unstaged history.txt", "Blame…")
	h.drain()

	st := h.app.fileInspectorUI()
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		srv.RunGit(t, repo.Path, "blame", "--porcelain", "-w", "HEAD", "--", "history.txt"))
	if len(st.blameLines) != 4 {
		t.Fatalf("SSH blame lines = %d, want 4 (HEAD content)", len(st.blameLines))
	}
	for _, line := range []string{"one changed", "two", "three", "four"} {
		if !h.ui.HasText(line) {
			t.Fatalf("SSH blame missing line text %q; texts=%q", line, h.ui.Texts())
		}
	}
	// Lines 2-3 form one run of "create history": before any line is chosen
	// and the bottom bar joins in, its subject shows exactly once, as does
	// the one-line run of "add line four".
	if n := countExactText(h.ui, "create history"); n != 1 {
		t.Fatalf("SSH blame subject of a two-line run rendered %d times, want 1", n)
	}
	if n := countExactText(h.ui, "add line four"); n != 1 {
		t.Fatalf("SSH blame subject of a one-line run rendered %d times, want 1", n)
	}

	// Choosing line 3 — the continuation line of the run, its commit cells
	// empty — fills the bottom bar with its commit over SSH too.
	if err := h.ui.Click("Blame Line 3"); err != nil {
		t.Fatalf("click Blame Line 3: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if st.blameSel < 0 || st.blameLines[st.blameSel].Subject != "create history" {
		t.Fatalf("line 3 selection = %+v, want create history", st.blameLines[st.blameSel])
	}
	if st.blameLines[st.blameSel].First {
		t.Fatal("fixture: SSH blame line 3 must be a continuation line")
	}

	// The last commit deleted deleted.txt: Blame from its changed-file row
	// falls back to the first parent and annotates the pre-deletion content.
	if err := h.ui.Click("Close File Inspector"); err != nil {
		t.Fatalf("close inspector: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	head := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	clickCommitRow(t, h, head, "remove deleted")
	h.drain()

	chooseFileMenuItem(t, h, fileRowLabel("deleted.txt"), "Blame…")
	h.drain()

	parent := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD^"))
	if st.blameRev != parent {
		t.Fatalf("SSH blame rev of deleted file = %q, want first parent %q", st.blameRev, parent)
	}
	assertBlameLinesMatchPorcelain(t, st.blameLines,
		srv.RunGit(t, repo.Path, "blame", "--porcelain", "-w", parent, "--", "deleted.txt"))
	if !h.ui.HasText("to be deleted") {
		t.Fatalf("SSH blame missing pre-deletion content; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText(short7(parent)) {
		t.Fatalf("SSH badge missing parent short hash; texts=%q", h.ui.Texts())
	}
}
