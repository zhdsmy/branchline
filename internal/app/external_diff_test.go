package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/store"
)

// writeFakeDiffTool writes a shell script acting as the user's external diff
// tool: it never opens anything graphical. Instead it copies the contents of
// the LOCAL and REMOTE files it receives and the MERGED text and the file
// names into the record directory, so a test can compare what the tool was
// handed with what git holds. When gate is not empty the script waits for
// that file to appear first, holding the tool open as a user reading the
// diff in a real tool would.
func writeFakeDiffTool(t *testing.T, dir, recordDir, gate string) string {
	t.Helper()
	script := filepath.Join(dir, "fake-diff-tool.sh")
	content := "#!/bin/sh\n"
	if gate != "" {
		content += "while [ ! -f " + shellQ(gate) + " ]; do sleep 0.05; done\n"
	}
	content += "cp \"$LOCAL\" " + shellQ(filepath.Join(recordDir, "record.local")) + "\n"
	content += "cp \"$REMOTE\" " + shellQ(filepath.Join(recordDir, "record.remote")) + "\n"
	content += "printf '%s\\n' \"$MERGED\" > " + shellQ(filepath.Join(recordDir, "record.merged")) + "\n"
	content += "printf '%s/%s\\n' \"$(basename \"$LOCAL\")\" \"$(basename \"$REMOTE\")\" > " + shellQ(filepath.Join(recordDir, "record.names")) + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake diff tool: %v", err)
	}
	return script
}

// setupExternalDiffTool installs the fake diff tool of one test: the Custom
// choice of the Diff settings points at it, and the snapshot parent and the
// program lookup of the state are replaced so no real graphical tool can
// ever start and no snapshot can leave the temporary folder. It returns the
// record directory the tool writes into and the snapshot parent. The gate
// argument is the file the tool waits for before exiting, "" for none.
func setupExternalDiffTool(t *testing.T, h *testHarness, gate string) (recordDir, snapParent string) {
	t.Helper()
	dir := t.TempDir()
	recordDir = filepath.Join(dir, "record")
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatalf("mkdir record dir: %v", err)
	}
	snapParent = filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(snapParent, 0o755); err != nil {
		t.Fatalf("mkdir snapshot parent: %v", err)
	}
	script := writeFakeDiffTool(t, dir, recordDir, gate)
	cur := h.store.Settings()
	cur.DiffTool = "Custom"
	cur.CustomDiff.Path = script
	cur.CustomDiff.Args = `"$LOCAL" "$REMOTE"`
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save fake tool settings: %v", err)
	}
	st := externalDiffUI(h.app)
	st.snapshotParent = snapParent
	st.find = func(candidates []string, program string) string { return "" }
	return recordDir, snapParent
}

// readToolRecord reads one file the fake diff tool wrote, or "" when the
// file does not exist yet.
func readToolRecord(t *testing.T, recordDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recordDir, name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read tool record %s: %v", name, err)
	}
	return string(data)
}

// extSnapshotDirs lists the snapshot folders inside parent, recognized by the
// name prefix the snapshot package gives them.
func extSnapshotDirs(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read snapshot parent: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "branchline-exttool-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// waitForSnapshotDir polls until a snapshot folder exists inside parent,
// bounded to the same ten seconds the UI queue allows: the worker that
// creates it is detached, so there is no completion callback to wait on.
func waitForSnapshotDir(t *testing.T, parent string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if len(extSnapshotDirs(t, parent)) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a snapshot folder in %s", parent)
}

// chooseToolMenuItem opens the external diff of the file row labeled
// rowLabel through its context menu, as a user would with the secondary
// button, and drains the run: the fake tool exits immediately, so the drain
// covers the whole pipeline.
func chooseToolMenuItem(t *testing.T, h *testHarness, rowLabel string) {
	t.Helper()
	chooseFileMenuItem(t, h, rowLabel, "Open in External Diff Tool")
	h.drain()
}

// assertToolSides compares what the fake tool recorded with the two sides
// the comparison must have read.
func assertToolSides(t *testing.T, recordDir, wantLocal, wantRemote string) {
	t.Helper()
	if got := readToolRecord(t, recordDir, "record.local"); got != wantLocal {
		t.Fatalf("tool LOCAL = %q, want %q", got, wantLocal)
	}
	if got := readToolRecord(t, recordDir, "record.remote"); got != wantRemote {
		t.Fatalf("tool REMOTE = %q, want %q", got, wantRemote)
	}
}

// An unstaged file opens in the external diff tool with the index version on
// the left and the working tree version on the right, the same sides the
// page's own diff shows.
func TestExternalDiffChangesUnstaged(t *testing.T) {
	repo := newGitRepo(t, "xd-unstaged")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")

	h := openChangesHarness(t, repo)
	recordDir, snapParent := setupExternalDiffTool(t, h, "")

	chooseToolMenuItem(t, h, "Unstaged code.go")

	assertToolSides(t, recordDir, "package main\n\nvar v = 1\n", "package main\n\nvar v = 2\n")
	if got := readToolRecord(t, recordDir, "record.merged"); got != "code.go\n" {
		t.Fatalf("tool MERGED = %q, want the repository-relative path", got)
	}
	if got := extSnapshotDirs(t, snapParent); len(got) != 0 {
		t.Fatalf("snapshot folders after the tool exited = %v, want none", got)
	}
}

// A staged file opens with the HEAD version on the left and the index
// version on the right: the fixture's working tree carries a third version
// that must not reach the tool.
func TestExternalDiffChangesStaged(t *testing.T) {
	repo := newGitRepo(t, "xd-staged")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")
	repo.Git("add", "code.go")
	repo.WriteFile("code.go", "package main\n\nvar v = 3\n")

	h := openChangesHarness(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")

	chooseToolMenuItem(t, h, "Staged code.go")

	assertToolSides(t, recordDir, "package main\n\nvar v = 1\n", "package main\n\nvar v = 2\n")
}

// An untracked file has no version in the index, so its left side is empty
// and its right side is the working tree file.
func TestExternalDiffChangesUntracked(t *testing.T) {
	repo := newGitRepo(t, "xd-untracked")
	repo.CommitFile("code.go", "package main\n", "create code")
	repo.WriteFile("new.txt", "brand new\n")

	h := openChangesHarness(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")

	chooseToolMenuItem(t, h, "Unstaged new.txt")

	assertToolSides(t, recordDir, "", "brand new\n")
}

// A staged rename compares HEAD's version under the old path with the
// index's version under the new one, and the snapshot files carry the names
// the user knows.
func TestExternalDiffChangesStagedRename(t *testing.T) {
	repo := newGitRepo(t, "xd-rename")
	repo.CommitFile("old.txt", "original line one\noriginal line two\noriginal line three\n", "create old")
	repo.Git("mv", "old.txt", "new.txt")
	repo.WriteFile("new.txt", "original line one\noriginal line two\noriginal line three\nrenamed and edited\n")
	repo.Git("add", "new.txt")

	if status := repo.Git("status", "--porcelain"); !strings.HasPrefix(status, "R") {
		t.Fatalf("fixture broken: no staged rename in %q", status)
	}

	h := openChangesHarness(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")

	chooseToolMenuItem(t, h, "Staged new.txt")

	assertToolSides(t, recordDir,
		"original line one\noriginal line two\noriginal line three\n",
		"original line one\noriginal line two\noriginal line three\nrenamed and edited\n")
	if got := readToolRecord(t, recordDir, "record.names"); got != "old.txt/new.txt\n" {
		t.Fatalf("tool file names = %q, want the old name on the left, the new on the right", got)
	}
}

// A conflicted file offers no external diff: the menu entry is disabled,
// because the versions of a conflict belong to the merge tool.
func TestExternalDiffConflictedFileDisabled(t *testing.T) {
	repo := newGitRepo(t, "xd-conflict")
	repo.CommitFile("conflict.txt", "base\n", "base")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("conflict.txt", "side\n", "side change")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("conflict.txt", "main\n", "main change")
	if _, err := tryGitCommand(repo.Path(), "merge", "side"); err == nil {
		t.Fatal("fixture broken: the merge of side did not conflict")
	}

	h := openChangesHarness(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")
	if len(h.app.session.Snapshot().Status.Conflicts) == 0 {
		t.Fatal("fixture broken: no conflicted files in the snapshot")
	}

	if err := h.ui.RightClick("Conflicted conflict.txt"); err != nil {
		t.Fatalf("right-click conflicted row: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Open in External Diff Tool"); err == nil {
		t.Fatal("the external diff entry of a conflicted file is not disabled")
	}
	h.ui.CloseMenu()
	h.ui.Frame()

	if got := readToolRecord(t, recordDir, "record.merged"); got != "" {
		t.Fatalf("the tool ran for a conflicted file: %q", got)
	}
}

// The file rows of the History detail open the external diff with the first
// parent's version on the left and the commit's version on the right, the
// sides the page's own diff of the commit shows.
func TestExternalDiffHistoryCommit(t *testing.T) {
	repo := newGitRepo(t, "xd-history")
	first := repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.CommitFile("code.go", "package main\n\nvar v = 2\n", "bump v")
	_ = first

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	recordDir, _ := setupExternalDiffTool(t, h, "")

	clickCommitRow(t, h, headOf(t, repo), "bump v")
	h.drain()
	chooseToolMenuItem(t, h, "File code.go")

	assertToolSides(t, recordDir, "package main\n\nvar v = 1\n", "package main\n\nvar v = 2\n")
}

// A merge commit compares against its first parent: the left side is the
// version main had before the merge, not the merged-in branch's.
func TestExternalDiffHistoryMergeCommit(t *testing.T) {
	repo := newGitRepo(t, "xd-merge")
	repo.CommitFile("alpha.txt", "base\n", "base")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("alpha.txt", "side\n", "side edit")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("alpha.txt", "main\n", "main edit")
	repo.Git("merge", "--no-ff", "--no-edit", "-Xtheirs", "side")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	recordDir, _ := setupExternalDiffTool(t, h, "")

	merge := headOf(t, repo)
	parents := strings.Fields(repo.Git("rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Fatalf("fixture broken: HEAD parents = %v, want a merge", parents)
	}
	subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", merge))
	clickCommitRow(t, h, merge, subject)
	h.drain()
	chooseToolMenuItem(t, h, "File alpha.txt")

	assertToolSides(t, recordDir, "main\n", "side\n")
}

// The root commit has no parent: its left side is empty and its right side
// is the committed version.
func TestExternalDiffHistoryRootCommit(t *testing.T) {
	repo := newGitRepo(t, "xd-root")
	root := repo.CommitFile("code.go", "package main\n", "create code")
	repo.CommitFile("other.txt", "other\n", "unrelated second commit")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	recordDir, _ := setupExternalDiffTool(t, h, "")

	clickCommitRow(t, h, root, "create code")
	h.drain()
	chooseToolMenuItem(t, h, "File code.go")

	assertToolSides(t, recordDir, "", "package main\n")
}

// The file rows of the two-commit comparison open the external diff in the
// comparison's direction: the older commit on the left, the newer on the
// right.
func TestExternalDiffCompareDirection(t *testing.T) {
	repo, fx := newCompareRepo(t, "xd-compare")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	recordDir, _ := setupExternalDiffTool(t, h, "")

	selectComparePair(t, h, fx.c2, "second grow", fx.c1, "first seed")
	chooseToolMenuItem(t, h, "File alpha.txt")

	assertToolSides(t, recordDir, "alpha v1\n", "alpha v1\nalpha v2\n")
}

// A repository switch does not touch a running tool: the snapshot survives
// the switch and is deleted only after the tool exits.
func TestExternalDiffSnapshotSurvivesRepoSwitch(t *testing.T) {
	repo := newGitRepo(t, "xd-switch-a")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")
	other := newGitRepo(t, "xd-switch-b")
	other.CommitFile("b.txt", "b\n", "other repo")

	h := openChangesHarness(t, repo)
	gate := filepath.Join(t.TempDir(), "tool-gate")
	recordDir, snapParent := setupExternalDiffTool(t, h, gate)

	chooseFileMenuItem(t, h, "Unstaged code.go", "Open in External Diff Tool")
	waitForSnapshotDir(t, snapParent)

	// Switching repositories while the tool is still open keeps both the
	// tool and its snapshot: the worker is detached from the session.
	h.openRepo(other.StoreRepo())
	h.ui.Frame()
	if got := extSnapshotDirs(t, snapParent); len(got) == 0 {
		t.Fatal("the snapshot was deleted while the tool was still open")
	}

	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the tool gate: %v", err)
	}
	h.drain()

	assertToolSides(t, recordDir, "package main\n\nvar v = 1\n", "package main\n\nvar v = 2\n")
	if got := extSnapshotDirs(t, snapParent); len(got) != 0 {
		t.Fatalf("snapshot folders after the tool exited = %v, want none", got)
	}
}

// A tool that cannot be found reports its own name in the error instead of
// running anything.
func TestExternalDiffToolNotFoundShowsError(t *testing.T) {
	repo := newGitRepo(t, "xd-notfound")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")

	h := openChangesHarness(t, repo)
	cur := h.store.Settings()
	cur.DiffTool = "Kaleidoscope"
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save Kaleidoscope choice: %v", err)
	}
	st := externalDiffUI(h.app)
	st.snapshotParent = t.TempDir()
	st.find = func(candidates []string, program string) string { return "" }

	chooseFileMenuItem(t, h, "Unstaged code.go", "Open in External Diff Tool")
	h.drain()

	joined := strings.Join(h.ui.Texts(), "\n")
	if !strings.Contains(joined, "Kaleidoscope") {
		t.Fatalf("the error does not name the tool; texts=%q", joined)
	}
	if !strings.Contains(joined, "not found") {
		t.Fatalf("the error does not say the tool was not found; texts=%q", joined)
	}
}

// The first use of the external diff state sweeps stale snapshot folders of
// previous runs: a folder older than a day is deleted, a fresh one stays.
func TestExternalDiffCleanStaleRunsOnFirstUse(t *testing.T) {
	repo := newGitRepo(t, "xd-cleanup")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")

	h := openChangesHarness(t, repo)
	snapParent := filepath.Join(t.TempDir(), "snapshots")
	stale := filepath.Join(snapParent, "branchline-exttool-stale")
	fresh := filepath.Join(snapParent, "branchline-exttool-fresh")
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("age the stale snapshot folder: %v", err)
	}
	// The first use is the first menu build; the parent must be injected
	// before it, so the sweep the construction schedules reads it.
	st := externalDiffUI(h.app)
	st.snapshotParent = snapParent

	if err := h.ui.RightClick("Unstaged code.go"); err != nil {
		t.Fatalf("right-click unstaged row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.CloseMenu()
	h.drain()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale snapshot folder survived the sweep: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the fresh snapshot folder was swept: %v", err)
	}
}

// ⇧F10 on a focused file row opens the same context menu a right-click
// opens, and the external diff entry is keyboard-reachable through it.
func TestExternalDiffKeyboardShiftF10(t *testing.T) {
	repo := newGitRepo(t, "xd-keyboard")
	repo.CommitFile("code.go", "package main\n\nvar v = 1\n", "create code")
	repo.WriteFile("code.go", "package main\n\nvar v = 2\n")

	h := openChangesHarness(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")

	mustClick(t, h, "Unstaged code.go")
	h.ui.Frame()
	h.ui.Key(ui.Shift, ui.KeyF10)
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Open in External Diff Tool"); err != nil {
		t.Fatalf("choose the external diff entry after ⇧F10: %v", err)
	}
	h.drain()

	assertToolSides(t, recordDir, "package main\n\nvar v = 1\n", "package main\n\nvar v = 2\n")
}

// An SSH repository compares in the same tool: the working tree side is read
// from the server and both snapshots land on this Mac.
func TestExternalDiffSSHUnstaged(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "a.txt", []byte("remote edited\n"))

	h := newTestApp(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")
	openRepoViaSwitcher(t, h, repo.Name)

	chooseToolMenuItem(t, h, "Unstaged a.txt")

	assertToolSides(t, recordDir, "one\n", "remote edited\n")
}

// The History of an SSH repository opens the same comparison: the parent
// commit on the left, the chosen commit on the right.
func TestExternalDiffSSHHistory(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	srv.WriteFile(t, repo.Path, "a.txt", []byte("one\ntwo\n"))
	srv.RunGit(t, repo.Path, "add", "a.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "second commit")

	h := newTestApp(t, repo)
	recordDir, _ := setupExternalDiffTool(t, h, "")
	openRepoViaSwitcher(t, h, repo.Name)

	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History nav row: %v", err)
	}
	h.ui.Frame()
	clickCommitRow(t, h, srv.RunGit(t, repo.Path, "rev-parse", "HEAD"), "second commit")
	h.drain()
	chooseToolMenuItem(t, h, "File a.txt")

	assertToolSides(t, recordDir, "one\n", "one\ntwo\n")
}

// chooseExternalTool picks an option of one external tool pop-up of the Diff
// page through its real popup. The trigger is clicked through its pop-up
// button role: the field carries the same name and its center leaves the
// trigger as soon as an error row grows the field, so a label click is not
// reliable here.
func chooseExternalTool(t *testing.T, tt *ui.Tester, label, option string) {
	t.Helper()
	trigger, ok := tt.AccessByRole(ui.RolePopUpButton, label)
	if !ok {
		t.Fatalf("no pop-up button labeled %q; texts=%q", label, tt.Texts())
	}
	tt.ClickAt(trigger.Bounds.X+trigger.Bounds.W/2, trigger.Bounds.Y+trigger.Bounds.H/2)
	tt.Frame()
	if err := tt.Click(option); err != nil {
		t.Fatalf("choose %q in %s pop-up: %v; texts=%q", option, label, err, tt.Texts())
	}
	tt.Frame()
}

// The two pop-ups of the Diff page persist their choices in the store, and
// the Custom choice reveals the program and arguments inputs of its tool,
// hiding them again for a preset.
func TestExternalDiffSettingsToolChoicesPersist(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")

	if tt.HasText("Choose Custom Diff Tool Path") {
		t.Fatalf("the custom diff inputs show before Custom is chosen; texts=%q", tt.Texts())
	}

	chooseExternalTool(t, tt, "External Diff Tool", "Custom")
	if got := h.store.Settings().DiffTool; got != "Custom" {
		t.Fatalf("persisted DiffTool = %q, want Custom", got)
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "External Diff Tool Path"); !ok {
		t.Fatalf("no custom diff path input after choosing Custom; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "External Diff Tool Arguments"); !ok {
		t.Fatalf("no custom diff arguments input after choosing Custom; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "Choose External Diff Tool Path"); !ok {
		t.Fatalf("no custom diff Choose button after choosing Custom; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Merge Tool Path"); ok {
		t.Fatalf("the merge tool's custom inputs show with only the diff tool on Custom; texts=%q", tt.Texts())
	}

	chooseExternalTool(t, tt, "External Diff Tool", "Kaleidoscope")
	if got := h.store.Settings().DiffTool; got != "Kaleidoscope" {
		t.Fatalf("persisted DiffTool = %q, want Kaleidoscope", got)
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "External Diff Tool Path"); ok {
		t.Fatalf("the custom diff inputs stayed visible after leaving Custom; texts=%q", tt.Texts())
	}

	chooseExternalTool(t, tt, "Merge Tool", "Custom")
	if got := h.store.Settings().MergeTool; got != "Custom" {
		t.Fatalf("persisted MergeTool = %q, want Custom", got)
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Merge Tool Path"); !ok {
		t.Fatalf("no custom merge path input after choosing Custom; texts=%q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Merge Tool Arguments"); !ok {
		t.Fatalf("no custom merge arguments input after choosing Custom; texts=%q", tt.Texts())
	}
	chooseExternalTool(t, tt, "Merge Tool", "P4Merge")
	if got := h.store.Settings().MergeTool; got != "P4Merge" {
		t.Fatalf("persisted MergeTool = %q, want P4Merge", got)
	}
	if _, ok := tt.AccessByRole(ui.RoleTextField, "Merge Tool Path"); ok {
		t.Fatalf("the custom merge inputs stayed visible after leaving Custom; texts=%q", tt.Texts())
	}

	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := reopened.Settings(); got.DiffTool != "Kaleidoscope" || got.MergeTool != "P4Merge" {
		t.Fatalf("reopened tool choices = %q/%q, want Kaleidoscope/P4Merge", got.DiffTool, got.MergeTool)
	}
}

// The custom program and arguments inputs save as they are edited, and a
// fresh read of the store holds both.
func TestExternalDiffSettingsCustomInputsSave(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	chooseExternalTool(t, tt, "External Diff Tool", "Custom")

	typeInSettingsField(t, tt, "External Diff Tool Path", "/tmp/fake-diff.sh")
	tt.Frame()
	typeInSettingsField(t, tt, "External Diff Tool Arguments", `"$LOCAL" "$REMOTE" --wait`)
	tt.Frame()

	cur := h.store.Settings()
	if cur.CustomDiff.Path != "/tmp/fake-diff.sh" {
		t.Fatalf("persisted CustomDiff.Path = %q, want the typed program", cur.CustomDiff.Path)
	}
	if cur.CustomDiff.Args != `"$LOCAL" "$REMOTE" --wait` {
		t.Fatalf("persisted CustomDiff.Args = %q, want the typed arguments", cur.CustomDiff.Args)
	}
	reopened, err := store.Open(h.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := reopened.Settings().CustomDiff.Path; got != "/tmp/fake-diff.sh" {
		t.Fatalf("reopened CustomDiff.Path = %q, want the typed program", got)
	}
}

// A failed save of a tool choice reverts the pop-up to the saved value and
// shows the raw error, as the page's other controls do.
func TestExternalDiffSettingsSaveFailureReverts(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")

	storeDir := filepath.Dir(h.store.Path())
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	probe := h.store.SetSettings(h.store.Settings())
	if probe == nil {
		t.Fatal("SetSettings succeeded into a read-only directory: the failure assertions below would be vacuous")
	}

	chooseExternalTool(t, tt, "External Diff Tool", "Custom")

	// The store normalizes a never-chosen empty DiffTool to the default, so
	// the failed write leaving the default is what proves it wrote nothing.
	if got := h.store.Settings().DiffTool; got != "FileMerge" {
		t.Fatalf("failed save changed DiffTool to %q", got)
	}
	if !tt.HasText(probe.Error()) {
		t.Fatalf("the page does not show the raw error %q after the failed save; texts=%q", probe.Error(), tt.Texts())
	}
	// The reverted pop-up no longer shows the failed choice: the custom
	// inputs hide with it.
	if _, ok := tt.AccessByRole(ui.RoleTextField, "External Diff Tool Path"); ok {
		t.Fatalf("the pop-up still reads Custom after its failed save; texts=%q", tt.Texts())
	}

	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatalf("restore store dir: %v", err)
	}
	chooseExternalTool(t, tt, "External Diff Tool", "Custom")
	if got := h.store.Settings().DiffTool; got != "Custom" {
		t.Fatalf("the retry after restoring writability did not persist: DiffTool = %q", got)
	}

	// A typed custom path is its own control: with the directory read-only
	// again its failed keystroke save reverts the input and shows the error
	// under it.
	seeded := "/usr/local/bin/seeded-diff"
	typeInSettingsField(t, tt, "External Diff Tool Path", seeded)
	tt.Frame()
	if got := h.store.Settings().CustomDiff.Path; got != seeded {
		t.Fatalf("setup: the seeded path did not persist: %q", got)
	}
	if err := os.Chmod(storeDir, 0o500); err != nil {
		t.Fatalf("chmod store dir again: %v", err)
	}
	typeInSettingsField(t, tt, "External Diff Tool Path", seeded+"-edited")
	tt.Frame()
	if got := h.store.Settings().CustomDiff.Path; got != seeded {
		t.Fatalf("failed keystroke save changed CustomDiff.Path to %q", got)
	}
	st := diffToolSettings(h.app)
	if st.customDiffPath != seeded {
		t.Fatalf("the reverted input holds %q, want the saved %q", st.customDiffPath, seeded)
	}
	if st.errs["customDiffPath"] == "" {
		t.Fatal("the path input's failed save left no error under it")
	}
}

// The Choose button of the custom diff row puts the picked program into the
// input and saves it, without a system dialog in tests.
func TestExternalDiffSettingsChooseButtonPicksPath(t *testing.T) {
	h := newTestApp(t)
	tt := openSettingsTester(t, h)
	clickSettingsTab(t, tt, "Diff")
	chooseExternalTool(t, tt, "External Diff Tool", "Custom")

	st := diffToolSettings(h.app)
	picked := filepath.Join(t.TempDir(), "picked-diff.sh")
	st.pickToolPathFn = func(parent *mygo.Window) (string, error) { return picked, nil }

	if err := tt.Click("Choose External Diff Tool Path"); err != nil {
		t.Fatalf("click Choose of the custom diff row: %v; texts=%q", err, tt.Texts())
	}
	drainSettings(t, h, tt)

	if got := h.store.Settings().CustomDiff.Path; got != picked {
		t.Fatalf("persisted CustomDiff.Path after the picker = %q, want %q", got, picked)
	}
}
