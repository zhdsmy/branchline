package app

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeMergeToolOpts steers the fake merge tool of one test: the gate file it
// waits for before finishing ("" for none), whether it writes a merge result
// into MERGED and which file holds it, and the exit code it ends with.
type fakeMergeToolOpts struct {
	gate        string
	writeResult bool
	resultFile  string
	exitCode    int
}

// writeFakeMergeTool writes a shell script acting as the user's merge tool:
// it never opens anything graphical. It copies the contents and the paths of
// the BASE, LOCAL, REMOTE and MERGED files it receives into the record
// directory first — the MERGED copy is the pre-merge working tree content —
// then waits for the gate file when one is set, optionally writes the result
// file over MERGED, and exits with the configured code.
func writeFakeMergeTool(t *testing.T, dir, recordDir string, opts fakeMergeToolOpts) string {
	t.Helper()
	script := filepath.Join(dir, "fake-merge-tool.sh")
	content := "#!/bin/sh\n"
	for _, role := range []string{"BASE", "LOCAL", "REMOTE", "MERGED"} {
		lower := strings.ToLower(role)
		content += "cp \"$" + role + "\" " + shellQ(filepath.Join(recordDir, "record."+lower)) + "\n"
		content += "printf '%s\\n' \"$" + role + "\" > " + shellQ(filepath.Join(recordDir, "record."+lower+"path")) + "\n"
	}
	if opts.gate != "" {
		content += "while [ ! -f " + shellQ(opts.gate) + " ]; do sleep 0.05; done\n"
	}
	if opts.writeResult {
		content += "cp " + shellQ(opts.resultFile) + " \"$MERGED\"\n"
	}
	content += "exit " + strconv.Itoa(opts.exitCode) + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake merge tool: %v", err)
	}
	return script
}

// setupMergeTool installs the fake merge tool of one test: the Custom choice
// of the Merge Tool settings points at it, and the snapshot parent and the
// program lookup of the external tool state are replaced so no real graphical
// tool can ever start and no snapshot can leave the temporary folder. It
// returns the record directory the tool writes into and the snapshot parent.
func setupMergeTool(t *testing.T, h *testHarness, opts fakeMergeToolOpts) (recordDir, snapParent string) {
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
	script := writeFakeMergeTool(t, dir, recordDir, opts)
	cur := h.store.Settings()
	cur.MergeTool = "Custom"
	cur.CustomMerge.Path = script
	cur.CustomMerge.Args = `"$BASE" "$LOCAL" "$REMOTE" "$MERGED"`
	if err := h.store.SetSettings(cur); err != nil {
		t.Fatalf("save fake tool settings: %v", err)
	}
	st := externalDiffUI(h.app)
	st.snapshotParent = snapParent
	st.find = func(candidates []string, program string) string { return "" }
	return recordDir, snapParent
}

// mergeToolResultFile writes the merge resolution the fake tool copies into
// MERGED when writeResult is set.
func mergeToolResultFile(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "merge-result.txt")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("write merge result file: %v", err)
	}
	return file
}

// writeMergeToolResultOpts are the options of a fake tool that writes content
// into MERGED and exits 0, with no gate.
func writeResultOpts(t *testing.T, content string) fakeMergeToolOpts {
	t.Helper()
	return fakeMergeToolOpts{writeResult: true, resultFile: mergeToolResultFile(t, content)}
}

// waitForToolRecord polls until the fake tool wrote its first record file —
// the point where the tool is running and, for a gated tool, waiting on the
// gate — bounded to the same ten seconds the UI queue allows: the worker
// pipeline is detached, so there is no completion callback to wait on.
func waitForToolRecord(t *testing.T, recordDir string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if _, err := os.Stat(filepath.Join(recordDir, "record.local")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the merge tool to start; record dir %s", recordDir)
}

// openMergeToolFor opens the merge tool of the conflicted file through its
// context menu, as a user would with the secondary button. It does not drain:
// tests with a gated tool interleave other actions before the tool exits.
func openMergeToolFor(t *testing.T, h *testHarness, path string) {
	t.Helper()
	row := "Conflicted " + path
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click conflict row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Open in Merge Tool"); err != nil {
		t.Fatalf("choose Open in Merge Tool of %q: %v\nmenu=%q", path, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// assertStillConflicted proves path is still unmerged with exactly the bytes
// the merge left in the working tree: no resolution of the merge tool reached
// the repository.
func assertStillConflicted(t *testing.T, repo *gitRepoFixture, path, wantContent string) {
	t.Helper()
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", path); out == "" {
		t.Fatalf("%s lost its unmerged entries", path)
	}
	assertFileBytes(t, repo, path, wantContent)
}

// assertNoOrigFile proves the resolution wrote no merge-tool backup file into
// the working tree.
func assertNoOrigFile(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(repo.Path(), "*.orig"))
	if err != nil {
		t.Fatalf("glob .orig files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf(".orig backup files in the working tree: %v", matches)
	}
}

// Choosing Open in Merge Tool on a both-modified conflict hands BASE, LOCAL,
// REMOTE and the working tree file to the tool as a snapshot on this Mac, and
// the resolution the tool writes lands as the staged resolution of the
// conflict: the file holds exactly the tool's bytes, no unmerged entries and
// no .orig backups remain, and the snapshot folder is deleted.
func TestMergeToolResolvesConflict(t *testing.T) {
	repo := newGitRepo(t, "mt-resolve")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	recordDir, snapParent := setupMergeTool(t, h, writeResultOpts(t, "resolved-by-tool\n"))
	h.drain()

	// The merge tool entry heads the conflict row's menu, ahead of a
	// separator and the quick resolutions.
	if err := h.ui.RightClick("Conflicted f.txt"); err != nil {
		t.Fatalf("right-click conflict row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if len(menu) < 2 || menu[0] != "Open in Merge Tool" || menu[1] != "-" {
		t.Fatalf("conflict menu head = %q, want Open in Merge Tool and a separator first", menu)
	}
	if err := h.ui.ChooseMenuItem("Open in Merge Tool"); err != nil {
		t.Fatalf("choose Open in Merge Tool: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge tool resolution failed: %v", err)
	}
	if got := readToolRecord(t, recordDir, "record.base"); got != "base\n" {
		t.Fatalf("tool BASE = %q, want the merge base", got)
	}
	if got := readToolRecord(t, recordDir, "record.local"); got != "main\n" {
		t.Fatalf("tool LOCAL = %q, want ours", got)
	}
	if got := readToolRecord(t, recordDir, "record.remote"); got != "side\n" {
		t.Fatalf("tool REMOTE = %q, want theirs", got)
	}
	if merged := readToolRecord(t, recordDir, "record.merged"); !strings.Contains(merged, "<<<<<<<") {
		t.Fatalf("tool MERGED = %q, want the working tree file with its conflict markers", merged)
	}
	if path := readToolRecord(t, recordDir, "record.mergedpath"); !strings.HasSuffix(strings.TrimSpace(path), filepath.Join("merged", "f.txt")) {
		t.Fatalf("MERGED snapshot path = %q, want the merged role folder ending in f.txt", path)
	}

	assertFileBytes(t, repo, "f.txt", "resolved-by-tool\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the merge tool resolution: %q", out)
	}
	assertNoOrigFile(t, repo)
	if got := extSnapshotDirs(t, snapParent); len(got) != 0 {
		t.Fatalf("snapshot folders after the tool exited = %v, want none", got)
	}
	if h.ui.HasText("Conflicted (") {
		t.Fatalf("Conflicted section still rendered after the resolution; texts=%q", h.ui.Texts())
	}
}

// A tool that exits 0 without touching MERGED changed nothing: the conflict
// stays exactly as it was, nothing is written, and the user is told the file
// was not changed in the tool.
func TestMergeToolUnchangedKeepsConflict(t *testing.T) {
	repo := newGitRepo(t, "mt-unchanged")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	setupMergeTool(t, h, fakeMergeToolOpts{})
	h.drain()

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	openMergeToolFor(t, h, "f.txt")
	h.drain()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	joined := strings.Join(h.ui.Texts(), "\n")
	if !strings.Contains(joined, "was not changed in") || !strings.Contains(joined, "still unresolved") {
		t.Fatalf("unchanged notice missing; texts=%q", joined)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("an unchanged merge produced an action error: %v", err)
	}
}

// A tool exiting nonzero failed: the conflict keeps every byte, nothing is
// written or staged, and the error names the tool with its exit status.
func TestMergeToolExitFailureKeepsConflict(t *testing.T) {
	repo := newGitRepo(t, "mt-exitfail")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	setupMergeTool(t, h, fakeMergeToolOpts{writeResult: true, resultFile: mergeToolResultFile(t, "resolved-by-tool\n"), exitCode: 1})
	h.drain()

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	openMergeToolFor(t, h, "f.txt")
	h.drain()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	joined := strings.Join(h.ui.Texts(), "\n")
	if !strings.Contains(joined, "Merge tool failed") {
		t.Fatalf("merge tool failure toast missing; texts=%q", joined)
	}
	if !strings.Contains(joined, "Custom") {
		t.Fatalf("the failure does not name the tool; texts=%q", joined)
	}
	if !strings.Contains(joined, "status 1") {
		t.Fatalf("the failure does not carry the exit status; texts=%q", joined)
	}
}

// A result that still carries conflict markers is refused by the safe write:
// the conflict is not resolved, the markered bytes never reach the working
// tree, and the refusal shows in the page.
func TestMergeToolMarkersRemainRefused(t *testing.T) {
	repo := newGitRepo(t, "mt-markers")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	marked := "resolved in part\n<<<<<<< leftover\n"
	setupMergeTool(t, h, writeResultOpts(t, marked))
	h.drain()

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	openMergeToolFor(t, h, "f.txt")
	h.drain()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("action error = %v, want the conflict markers refusal", err)
	}
	if !h.ui.HasText("conflict markers") {
		t.Fatalf("marker refusal not visible in the page; texts=%q", h.ui.Texts())
	}
}

// A both-added conflict has no merge base: the tool receives an empty BASE
// file, and its resolution lands exactly as for a both-modified conflict.
func TestMergeToolBothAddedHasEmptyBase(t *testing.T) {
	repo := newGitRepo(t, "mt-bothadded")
	repo.CommitFile("base.txt", "base\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("aa.txt", "side-aa\n", "side adds")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("aa.txt", "main-aa\n", "main adds")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}

	h := openConflictedMerge(t, repo)
	recordDir, _ := setupMergeTool(t, h, writeResultOpts(t, "resolved-aa\n"))
	h.drain()

	openMergeToolFor(t, h, "aa.txt")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge tool resolution of the both-added conflict failed: %v", err)
	}
	base := readToolRecord(t, recordDir, "record.base")
	if base != "" {
		t.Fatalf("tool BASE = %q, want empty for a conflict without a merge base", base)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "record.base")); err != nil {
		t.Fatalf("the BASE snapshot file was never handed to the tool: %v", err)
	}
	assertFileBytes(t, repo, "aa.txt", "resolved-aa\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the both-added resolution: %q", out)
	}
}

// A modify/delete conflict has no LOCAL or REMOTE side to merge, so the entry
// is disabled and the tool never runs.
func TestMergeToolModifyDeleteDisabled(t *testing.T) {
	repo := newGitRepo(t, "mt-modifydelete")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	recordDir, _ := setupMergeTool(t, h, writeResultOpts(t, "resolved\n"))
	h.drain()

	for _, path := range []string{"du.txt", "ud.txt"} {
		if err := h.ui.RightClick("Conflicted " + path); err != nil {
			t.Fatalf("right-click conflict row %q: %v\ntexts=%q", path, err, h.ui.Texts())
		}
		if err := h.ui.ChooseMenuItem("Open in Merge Tool"); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing Open in Merge Tool of %q = %v, want a disabled error", path, err)
		}
		h.ui.CloseMenu()
		h.ui.Frame()
	}
	if got := readToolRecord(t, recordDir, "record.local"); got != "" {
		t.Fatalf("the tool ran for a modify/delete conflict: %q", got)
	}
}

// When the conflict is resolved outside the app while the tool is still open,
// the tool's result never overwrites that resolution: the write back refuses
// because the path is no longer unmerged, and the externally staged content
// survives byte for byte.
func TestMergeToolExternalResolutionNotOverwritten(t *testing.T) {
	repo := newGitRepo(t, "mt-external")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	gate := filepath.Join(t.TempDir(), "tool-gate")
	recordDir, _ := setupMergeTool(t, h, fakeMergeToolOpts{
		gate:        gate,
		writeResult: true,
		resultFile:  mergeToolResultFile(t, "resolved-by-tool\n"),
	})
	h.drain()

	openMergeToolFor(t, h, "f.txt")
	waitForToolRecord(t, recordDir)

	// The user resolves the conflict in the terminal while the tool is open:
	// the unmerged entries leave the index.
	repo.Git("add", "--", "f.txt")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", "f.txt"); out != "" {
		t.Fatalf("fixture: external git add left unmerged entries: %q", out)
	}
	external, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read externally resolved f.txt: %v", err)
	}

	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the tool gate: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err == nil || !strings.Contains(err.Error(), "no longer unmerged") {
		t.Fatalf("action error = %v, want the no-longer-unmerged refusal", err)
	}
	assertFileBytes(t, repo, "f.txt", string(external))
	if staged := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(staged, "f.txt") {
		t.Fatalf("the external resolution was overwritten: %q", staged)
	}
}

// When the tool exits while another write is in progress, its result is kept
// instead of written: the conflict is untouched until the write finishes, and
// the row's menu then offers Save Merge Result, which writes the resolution
// back through the same safe write.
func TestMergeToolBusyWriteKeepsResult(t *testing.T) {
	repo := newGitRepo(t, "mt-busy")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	gate := filepath.Join(t.TempDir(), "tool-gate")
	recordDir, _ := setupMergeTool(t, h, fakeMergeToolOpts{
		gate:        gate,
		writeResult: true,
		resultFile:  mergeToolResultFile(t, "resolved-by-tool\n"),
	})
	h.drain()

	openMergeToolFor(t, h, "f.txt")
	waitForToolRecord(t, recordDir)

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	// Start a write that stays busy: a staged file held back by the paused
	// worker queue. The reload that surfaces pending.txt must complete before
	// the pause, or the drain would wait on the still-open merge tool.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.queue.RunNext(t)
	h.ui.Frame()
	h.queue.PauseWorkers()
	mustClick(t, h, "Stage pending.txt")
	write := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ResumeWorkers()
		h.queue.ReleaseWorker(write)
		h.drain()
		t.Fatal("session not busy while the stage worker is held")
	}

	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the tool gate: %v", err)
	}
	// The merge worker posts twice — the outcome and the running-mark cleanup —
	// and nothing else can drain while the write worker is held.
	h.queue.RunNext(t)
	h.queue.RunNext(t)
	h.ui.Frame()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	st := mergeToolUI(h.app)
	key := mergeToolKey(mergeToolRepoKey(h.app), "f.txt")
	if _, ok := st.results[key]; !ok {
		t.Fatal("the merge result was dropped instead of kept")
	}
	if err := h.ui.RightClick("Conflicted f.txt"); err != nil {
		t.Fatalf("right-click conflict row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	for _, item := range []string{"Save Merge Result", "Discard Merge Result"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("menu %q missing %q for the kept result", menu, item)
		}
	}
	if slices.Contains(menu, "Open in Merge Tool") {
		t.Fatalf("menu %q still offers Open in Merge Tool next to the kept result", menu)
	}
	h.ui.CloseMenu()

	// Finish the write, then save the kept result through the menu.
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(write)
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session still busy after releasing the write worker")
	}
	chooseConflictMenuItem(t, h, "f.txt", "Save Merge Result")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("saving the kept merge result failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "resolved-by-tool\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", "f.txt"); out != "" {
		t.Fatalf("unmerged entries survived saving the kept result: %q", out)
	}
	if _, ok := mergeToolUI(h.app).results[key]; ok {
		t.Fatal("the merge result survived its successful save")
	}
}

// When the tool exits while the user looks at another repository, its result
// is kept for the repository it belongs to: nothing is written anywhere,
// switching back offers Save Merge Result on the still-conflicted row, and
// choosing it resolves the conflict.
func TestMergeToolRepoSwitchSaveResult(t *testing.T) {
	repo := newGitRepo(t, "mt-switch-a")
	uuMergeFixture(t, repo)
	other := newGitRepo(t, "mt-switch-b")
	other.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t)
	savedA := h.openRepo(repo.StoreRepo())
	h.drain()
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: snapshot does not report the conflict: %+v", st)
	}
	gate := filepath.Join(t.TempDir(), "tool-gate")
	recordDir, _ := setupMergeTool(t, h, fakeMergeToolOpts{
		gate:        gate,
		writeResult: true,
		resultFile:  mergeToolResultFile(t, "resolved-by-tool\n"),
	})
	h.drain()

	openMergeToolFor(t, h, "f.txt")
	waitForToolRecord(t, recordDir)

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	// Switch away while the tool is open; its exit then lands in the other
	// repository's session and the result is kept instead of written.
	savedB, err := h.store.Add(other.StoreRepo())
	if err != nil {
		t.Fatalf("add second repository: %v", err)
	}
	h.app.openRepo(savedB)
	h.ui.Frame()

	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the tool gate: %v", err)
	}
	h.drain()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	if out := gitOutTrim(t, other.Path(), "status", "--porcelain"); out != "" {
		t.Fatalf("the other repository was written: %q", out)
	}
	st := mergeToolUI(h.app)
	if len(st.results) != 1 {
		t.Fatalf("kept results = %d, want exactly one", len(st.results))
	}
	joined := strings.Join(h.ui.Texts(), "\n")
	if !strings.Contains(joined, "was kept") {
		t.Fatalf("kept-result notice missing; texts=%q", joined)
	}

	// Back in the conflict's repository the row offers the result for saving.
	h.app.openRepo(savedA)
	h.drain()
	chooseConflictMenuItem(t, h, "f.txt", "Save Merge Result")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("saving the kept merge result after the switch failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "resolved-by-tool\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u", "--", "f.txt"); out != "" {
		t.Fatalf("unmerged entries survived saving the kept result: %q", out)
	}
}

// Discarding a kept merge result touches nothing: the conflict stays exactly
// as it was, and the row offers Open in Merge Tool again.
func TestMergeToolDiscardResult(t *testing.T) {
	repo := newGitRepo(t, "mt-discard-a")
	uuMergeFixture(t, repo)
	other := newGitRepo(t, "mt-discard-b")
	other.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t)
	savedA := h.openRepo(repo.StoreRepo())
	h.drain()
	gate := filepath.Join(t.TempDir(), "tool-gate")
	recordDir, _ := setupMergeTool(t, h, fakeMergeToolOpts{
		gate:        gate,
		writeResult: true,
		resultFile:  mergeToolResultFile(t, "resolved-by-tool\n"),
	})
	h.drain()

	openMergeToolFor(t, h, "f.txt")
	waitForToolRecord(t, recordDir)

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read conflicted f.txt: %v", err)
	}

	savedB, err := h.store.Add(other.StoreRepo())
	if err != nil {
		t.Fatalf("add second repository: %v", err)
	}
	h.app.openRepo(savedB)
	h.ui.Frame()
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatalf("release the tool gate: %v", err)
	}
	h.drain()

	h.app.openRepo(savedA)
	h.drain()
	chooseConflictMenuItem(t, h, "f.txt", "Discard Merge Result")
	h.ui.Frame()

	assertStillConflicted(t, repo, "f.txt", string(conflictBytes))
	st := mergeToolUI(h.app)
	if len(st.results) != 0 {
		t.Fatalf("kept results after discard = %d, want none", len(st.results))
	}
	if err := h.ui.RightClick("Conflicted f.txt"); err != nil {
		t.Fatalf("right-click conflict row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	if !slices.Contains(menu, "Open in Merge Tool") {
		t.Fatalf("menu %q missing Open in Merge Tool after the discard", menu)
	}
	if slices.Contains(menu, "Save Merge Result") || slices.Contains(menu, "Discard Merge Result") {
		t.Fatalf("menu %q still offers the discarded result", menu)
	}
	h.ui.CloseMenu()
	h.ui.Frame()
}

// ⇧F10 on a focused conflict row opens the same context menu a right-click
// opens, and the merge tool entry is keyboard-reachable through it.
func TestMergeToolKeyboardShiftF10(t *testing.T) {
	repo := newGitRepo(t, "mt-keyboard")
	uuMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	setupMergeTool(t, h, writeResultOpts(t, "resolved-by-tool\n"))
	h.drain()

	mustClick(t, h, "Conflicted f.txt")
	h.ui.Frame()
	h.ui.Key(ui.Shift, ui.KeyF10)
	h.ui.Frame()
	if err := h.ui.ChooseMenuItem("Open in Merge Tool"); err != nil {
		t.Fatalf("choose Open in Merge Tool after ⇧F10: %v\nmenu=%q", err, h.ui.Menu())
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("merge tool resolution after ⇧F10 failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "resolved-by-tool\n")
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the ⇧F10 resolution: %q", out)
	}
}

// An SSH repository merges in the same tool on this Mac: the versions are
// read from the server into the snapshot, and the resolution is written back
// onto the server, staged as the resolution there.
func TestMergeToolLoopbackSSHResolves(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteUUConflictFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	h.openRepo(repo)
	h.drain()
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: SSH snapshot does not report the conflict: %+v", st)
	}
	recordDir, snapParent := setupMergeTool(t, h, writeResultOpts(t, "resolved-by-tool\n"))
	h.drain()

	chooseConflictMenuItem(t, h, "a.txt", "Open in Merge Tool")
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH merge tool resolution failed: %v", err)
	}
	if got := readToolRecord(t, recordDir, "record.local"); got != "main\n" {
		t.Fatalf("tool LOCAL = %q, want ours read from the server", got)
	}
	if got := readToolRecord(t, recordDir, "record.remote"); got != "side\n" {
		t.Fatalf("tool REMOTE = %q, want theirs read from the server", got)
	}
	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != "resolved-by-tool\n" {
		t.Fatalf("SSH a.txt after the merge tool resolution = %q, want the tool's result", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived the resolution: %q", out)
	}
	if got := extSnapshotDirs(t, snapParent); len(got) != 0 {
		t.Fatalf("snapshot folders after the tool exited = %v, want none", got)
	}
}

// Over SSH a nonzero exit of the tool writes nothing either: the server's
// conflicted file keeps every byte and stays unmerged.
func TestMergeToolLoopbackSSHExitFailure(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteUUConflictFixture(t, srv, repo.Path)

	h := newTestApp(t, repo)
	h.openRepo(repo)
	h.drain()
	setupMergeTool(t, h, fakeMergeToolOpts{
		writeResult: true,
		resultFile:  mergeToolResultFile(t, "resolved-by-tool\n"),
		exitCode:    1,
	})
	h.drain()

	before := srv.ReadFile(t, repo.Path, "a.txt")
	chooseConflictMenuItem(t, h, "a.txt", "Open in Merge Tool")
	h.drain()

	if got := srv.ReadFile(t, repo.Path, "a.txt"); string(got) != string(before) {
		t.Fatalf("SSH a.txt overwritten despite the failed tool: before %q after %q", before, got)
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) == "" {
		t.Fatal("SSH conflict resolved despite the failed tool")
	}
	joined := strings.Join(h.ui.Texts(), "\n")
	if !strings.Contains(joined, "Merge tool failed") {
		t.Fatalf("SSH merge tool failure toast missing; texts=%q", joined)
	}
}
