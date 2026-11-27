package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// twoHunkMergeFixture builds a both-modified merge conflict in f.txt with two
// separate conflict blocks: ours edits both mid lines to main-mid*, theirs to
// side-mid*, so each block can take a different resolution.
func twoHunkMergeFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CommitFile("f.txt", "top\nbase-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nbase-mid2\ntail\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", "top\nside-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nside-mid2\ntail\n", "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", "top\nmain-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nmain-mid2\ntail\n", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
}

// twoHunkDiff3MergeFixture builds the same two-block conflict with diff3
// conflict markers: every block carries its base section after |||||||.
func twoHunkDiff3MergeFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.Git("config", "merge.conflictStyle", "diff3")
	twoHunkMergeFixture(t, repo)
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || !strings.Contains(string(content), "|||||||") {
		t.Fatalf("fixture: diff3 merge left no base markers: %q err=%v", content, err)
	}
}

// emptyOursMergeFixture builds a both-modified merge conflict where our side
// is a present zero-byte file and their side carries content.
func emptyOursMergeFixture(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CommitFile("f.txt", "x\n", "base commit")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("f.txt", "y\n", "side edits")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("f.txt", "", "main empties")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("fixture: merge produced no unmerged entries")
	}
}

// plumbSubmoduleConflict writes gitlink stage entries for sub/ by index
// plumbing: a natural submodule conflict needs a nested repository, but the
// stage shape is identical, and the inspector only reads the stage modes.
func plumbSubmoduleConflict(t *testing.T, repo *gitRepoFixture) {
	t.Helper()
	repo.CommitFile("keep.txt", "base\n", "base commit")
	// Three distinct commit objects so the stages differ the way a real
	// submodule conflict's do.
	repo.Git("commit", "-q", "--allow-empty", "-m", "second")
	repo.Git("commit", "-q", "--allow-empty", "-m", "third")
	oids := []string{
		strings.TrimSpace(repo.Git("rev-parse", "HEAD~2")),
		strings.TrimSpace(repo.Git("rev-parse", "HEAD~1")),
		strings.TrimSpace(repo.Git("rev-parse", "HEAD")),
	}
	entry := fmt.Sprintf("160000 %s 1\tsub\n160000 %s 2\tsub\n160000 %s 3\tsub\n", oids[0], oids[1], oids[2])
	if out, err := tryGitCommandStdin(repo.Path(), entry, "update-index", "--index-info"); err != nil {
		t.Fatalf("fixture: plumb submodule entries: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo.Path(), "sub"), 0o755); err != nil {
		t.Fatalf("fixture: mkdir sub: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); !strings.Contains(out, "160000") {
		t.Fatalf("fixture: gitlink stages missing after plumbing: %q", out)
	}
}

// selectConflictRow clicks a conflict row and drains the inspection read it
// starts, failing when the inspection never lands.
func selectConflictRow(t *testing.T, h *testHarness, path string) {
	t.Helper()
	mustClick(t, h, "Conflicted "+path)
	h.drain()
	st := conflictEditor(h.app)
	if !st.loaded || st.path != path {
		t.Fatalf("inspection of %s did not load: %+v loadErr=%v", path, st, st.loadErr)
	}
	if st.loadErr != nil {
		t.Fatalf("inspection of %s failed: %v", path, st.loadErr)
	}
}

// openMergeHunks enters the merge editor of the already selected conflict.
func openMergeHunks(t *testing.T, h *testHarness) {
	t.Helper()
	mustClick(t, h, "Open Hunk Resolver")
	h.ui.Frame()
	if st := conflictEditor(h.app); st.mode != 1 {
		t.Fatal("Open Hunk Resolver did not enter the merge view")
	}
}

// editorState returns the conflict editor state for direct assertions that no
// accessible pixel can express, such as the exact resolution draft.
func editorState(t *testing.T, h *testHarness) *conflictEditorState {
	t.Helper()
	return conflictEditor(h.app)
}

func saveButton(t *testing.T, h *testHarness) ui.AccessInfo {
	t.Helper()
	btn, ok := h.ui.AccessByRole(ui.RoleButton, "Save and Mark Resolved")
	if !ok {
		t.Fatalf("Save and Mark Resolved missing; texts=%q", h.ui.Texts())
	}
	return btn
}

// countingExecutor counts every Git invocation, proving a frame started no
// repository read at all.
type countingExecutor struct {
	real  git.Executor
	reads int
}

func (e *countingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.reads++
	return e.real.Run(ctx, stdin, args...)
}

func (e *countingExecutor) Describe() string { return e.real.Describe() }

// The overview of a two-block conflict shows both full versions side by side,
// the conflict progress, and the quick resolutions, while the merge mode
// stays behind its entry.
func TestConflictEditorOverviewSidesAndProgress(t *testing.T) {
	repo := newGitRepo(t, "ceditor-overview")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")

	if !h.ui.HasText("Both Modified") {
		t.Fatalf("conflict type badge missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("2 conflicts") {
		t.Fatalf("progress badge missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main-mid1") || !h.ui.HasText("main-mid2") {
		t.Fatalf("ours preview missing its lines; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("side-mid1") || !h.ui.HasText("side-mid2") {
		t.Fatalf("theirs preview missing its lines; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("This side deleted the file.") {
		t.Fatalf("a both-modified conflict must not claim a deletion; texts=%q", h.ui.Texts())
	}
	for _, label := range []string{"Use Ours", "Use Theirs", "Mark Resolved"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("pane quick action %q missing; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Open Hunk Resolver"); !ok {
		t.Fatalf("hunk resolver entry missing; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || mh.Disabled {
		t.Fatalf("Merge Hunks toggle = %+v found=%v, want enabled", mh, ok)
	}
	st := editorState(t, h)
	if st.mode != 0 {
		t.Fatalf("editor starts in mode %d, want overview", st.mode)
	}
	if got := len(st.inspection.Hunks); got != 2 {
		t.Fatalf("inspection parsed %d hunks, want 2", got)
	}
	if !strings.Contains(st.draft, "<<<<<<<") {
		t.Fatalf("initial draft is not the raw conflicted file: %q", st.draft)
	}
}

// Per-hunk checkboxes compose the resolution block by block: the first block
// keeps our lines, the second keeps both sides in order, and toggling a side
// off restores the block's markers.
func TestConflictEditorHunkChoicesComposeAndSave(t *testing.T) {
	repo := newGitRepo(t, "ceditor-choices")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	if !h.ui.HasText("Conflict 1") || !h.ui.HasText("Conflict 2") {
		t.Fatalf("hunk titles missing; texts=%q", h.ui.Texts())
	}
	for _, label := range []string{"Ours 1", "Theirs 1", "Ours 2", "Theirs 2"} {
		if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, label); !ok {
			t.Fatalf("hunk checkbox %q missing; texts=%q", label, h.ui.Texts())
		}
	}

	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	st := editorState(t, h)
	if !strings.Contains(st.draft, "main-mid1") || strings.Contains(st.draft, "side-mid1") {
		t.Fatalf("first hunk did not take ours: %q", st.draft)
	}
	if !strings.Contains(st.draft, "<<<<<<<") {
		t.Fatalf("untouched second hunk lost its markers: %q", st.draft)
	}
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled while the second block still has markers")
	}

	mustClick(t, h, "Ours 2")
	h.ui.Frame()
	mustClick(t, h, "Theirs 2")
	h.ui.Frame()
	st = editorState(t, h)
	ours2 := strings.Index(st.draft, "main-mid2")
	theirs2 := strings.Index(st.draft, "side-mid2")
	if ours2 < 0 || theirs2 < 0 || ours2 > theirs2 {
		t.Fatalf("double choice did not merge ours before theirs: %q", st.draft)
	}
	if git.HasConflictMarkers(st.draft) {
		t.Fatalf("draft still carries markers after both blocks resolved: %q", st.draft)
	}
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save disabled although the draft is clean")
	}
	if !h.ui.HasText("All conflicts resolved") {
		t.Fatalf("all-resolved progress missing; texts=%q", h.ui.Texts())
	}

	// Unchecking both sides of block 2 restores that block with its markers:
	// a block neither side is chosen for keeps its original conflict text.
	mustClick(t, h, "Ours 2")
	h.ui.Frame()
	mustClick(t, h, "Theirs 2")
	h.ui.Frame()
	st = editorState(t, h)
	if !git.HasConflictMarkers(st.draft) {
		t.Fatalf("restored block did not bring its markers back: %q", st.draft)
	}
	if !strings.Contains(st.draft, "main-mid2") || !strings.Contains(st.draft, "side-mid2") {
		t.Fatalf("restored block lost its original lines: %q", st.draft)
	}
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled again with restored markers")
	}

	// Resolve for real and save: the composed bytes land in the working tree
	// and in stage 0.
	mustClick(t, h, "Ours 2")
	h.ui.Frame()
	mustClick(t, h, "Theirs 2")
	h.ui.Frame()
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("save and mark resolved failed: %v", err)
	}
	want := "top\nmain-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nmain-mid2\nside-mid2\ntail\n"
	assertFileBytes(t, repo, "f.txt", want)
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the save: %q", out)
	}
	if staged := repo.Git("ls-files", "-s", "--", "f.txt"); !strings.Contains(staged, " 0\t") {
		t.Fatalf("f.txt not staged at stage 0: %q", staged)
	}
	if h.ui.HasText("Conflicted (") {
		t.Fatalf("Conflicted section still rendered after the save; texts=%q", h.ui.Texts())
	}
}

// The bulk checkboxes flip every block's side at once and compose the result
// the same way the per-hunk ones do.
func TestConflictEditorAllOursAllTheirsToggles(t *testing.T) {
	repo := newGitRepo(t, "ceditor-all")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	mustClick(t, h, "All Ours")
	h.ui.Frame()
	st := editorState(t, h)
	for i, ch := range st.choices {
		if !ch.Ours {
			t.Fatalf("All Ours left block %d without ours: %+v", i, ch)
		}
	}
	if !strings.Contains(st.draft, "main-mid1") || !strings.Contains(st.draft, "main-mid2") {
		t.Fatalf("All Ours draft missing our lines: %q", st.draft)
	}
	if strings.Contains(st.draft, "side-mid1") {
		t.Fatalf("All Ours draft kept their first block: %q", st.draft)
	}

	mustClick(t, h, "All Theirs")
	h.ui.Frame()
	st = editorState(t, h)
	if !strings.Contains(st.draft, "side-mid1") || !strings.Contains(st.draft, "side-mid2") {
		t.Fatalf("All Theirs draft missing their lines: %q", st.draft)
	}
	if git.HasConflictMarkers(st.draft) {
		t.Fatalf("draft still marked after both bulk choices: %q", st.draft)
	}

	// Unchecking All Ours drops our side of every block; theirs stays.
	mustClick(t, h, "All Ours")
	h.ui.Frame()
	st = editorState(t, h)
	for i, ch := range st.choices {
		if ch.Ours {
			t.Fatalf("unchecking All Ours left block %d with ours: %+v", i, ch)
		}
		if !ch.Theirs {
			t.Fatalf("unchecking All Ours lost theirs of block %d: %+v", i, ch)
		}
	}
	if strings.Contains(st.draft, "main-mid1") || !strings.Contains(st.draft, "side-mid2") {
		t.Fatalf("draft after unchecking All Ours wrong: %q", st.draft)
	}
}

// The conflict navigation walks the blocks in a circle and scrolls the target
// into view on exactly that frame, while the column layout switch rearranges
// the merge view.
func TestConflictEditorNavigationAndColumnLayout(t *testing.T) {
	repo := newGitRepo(t, "ceditor-nav")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	st := editorState(t, h)
	if st.activeHunk != 0 {
		t.Fatalf("initial focus = %d, want 0", st.activeHunk)
	}
	mustClick(t, h, "Next Conflict")
	h.ui.Frame()
	if st.activeHunk != 1 {
		t.Fatalf("focus after next = %d, want 1", st.activeHunk)
	}
	if st.scrollHunk {
		t.Fatal("scroll request survived past its frame")
	}
	mustClick(t, h, "Next Conflict")
	h.ui.Frame()
	if st.activeHunk != 0 {
		t.Fatalf("focus did not wrap to 0: %d", st.activeHunk)
	}
	mustClick(t, h, "Previous Conflict")
	h.ui.Frame()
	if st.activeHunk != 1 {
		t.Fatalf("focus after previous wrap = %d, want 1", st.activeHunk)
	}

	if _, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Column layout"); !ok {
		t.Fatalf("Column layout checkbox missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Column layout")
	h.ui.Frame()
	if !st.columns {
		t.Fatal("Column layout did not enable the column view")
	}
	if !h.ui.HasText("Conflict 1") {
		t.Fatalf("hunk list missing in column layout; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Column layout")
	h.ui.Frame()
	if st.columns {
		t.Fatal("Column layout did not switch back")
	}

	// A single-block conflict disables both navigation buttons.
	repo2 := newGitRepo(t, "ceditor-nav-single")
	uuMergeFixture(t, repo2)
	h2 := openConflictedMerge(t, repo2)
	selectConflictRow(t, h2, "f.txt")
	openMergeHunks(t, h2)
	for _, label := range []string{"Previous Conflict", "Next Conflict"} {
		if btn, ok := h2.ui.AccessByRole(ui.RoleButton, label); !ok || !btn.Disabled {
			t.Fatalf("single-hunk %s = %+v found=%v, want disabled", label, btn, ok)
		}
	}
}

// A diff3 conflict parses its base sections away from the sides: composing
// per-hunk choices drops the ||||||| base text entirely.
func TestConflictEditorDiff3Hunks(t *testing.T) {
	repo := newGitRepo(t, "ceditor-diff3")
	twoHunkDiff3MergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	st := editorState(t, h)
	if got := len(st.inspection.Hunks); got != 2 {
		t.Fatalf("diff3 inspection parsed %d hunks, want 2", got)
	}
	for i, hk := range st.inspection.Hunks {
		if hk.BaseStart < 0 {
			t.Fatalf("diff3 hunk %d has no base section: %+v", i, hk)
		}
	}

	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Theirs 2")
	h.ui.Frame()
	st = editorState(t, h)
	if strings.Contains(st.draft, "base-mid1") || strings.Contains(st.draft, "base-mid2") {
		t.Fatalf("diff3 compose leaked base lines into the result: %q", st.draft)
	}
	if strings.Contains(st.draft, "|||||||") {
		t.Fatalf("diff3 compose leaked base markers into the result: %q", st.draft)
	}
	if git.HasConflictMarkers(st.draft) {
		t.Fatalf("diff3 draft still marked: %q", st.draft)
	}
	if !strings.Contains(st.draft, "main-mid1") || !strings.Contains(st.draft, "side-mid2") {
		t.Fatalf("diff3 draft missing the chosen lines: %q", st.draft)
	}

	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("diff3 save failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", "top\nmain-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nside-mid2\ntail\n")
}

// The result text area is freely editable: typing the resolution by hand
// enables the save, and emptying it to zero bytes is a legal resolution that
// lands in the working tree and the index.
func TestConflictEditorManualEditAndZeroByteSave(t *testing.T) {
	repo := newGitRepo(t, "ceditor-manual")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	// A manual fusion replaces the whole draft: no checkbox involved.
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type("top\nmanual fusion\ntail\n")
	h.ui.Frame()
	st := editorState(t, h)
	if st.draft != "top\nmanual fusion\ntail\n" {
		t.Fatalf("manual edit draft = %q", st.draft)
	}
	if !st.draftDirty {
		t.Fatal("manual edit did not mark the draft dirty")
	}
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save disabled after a clean manual edit")
	}

	// Typing markers back in disables the save again, with the note visible.
	h.ui.Command("selectAll")
	h.ui.Type("half resolved\n>>>>>>> leftover\n")
	h.ui.Frame()
	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled with manually typed markers")
	}
	if !h.ui.HasText("The result still contains conflict markers.") {
		t.Fatalf("marker hint missing; texts=%q", h.ui.Texts())
	}

	// Emptying the result entirely is a legal zero-byte resolution.
	h.ui.Command("selectAll")
	h.ui.Key(0, ui.KeyBackspace)
	h.ui.Frame()
	st = editorState(t, h)
	if st.draft != "" || !st.draftDirty {
		t.Fatalf("emptied draft = %q dirty=%v", st.draft, st.draftDirty)
	}
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save disabled for the zero-byte resolution")
	}
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("zero-byte save failed: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || len(content) != 0 {
		t.Fatalf("zero-byte resolution changed the working tree file: %q err=%v", content, err)
	}
	if staged := repo.Git("ls-files", "-s", "--", "f.txt"); !strings.Contains(staged, "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391") {
		t.Fatalf("staged content of the zero-byte resolution is not the empty blob: %q", staged)
	}
}

// While the draft carries conflict markers the save stays disabled, and the
// domain write refuses marked content outright, so no order of clicks can
// stage a half-resolved file.
func TestConflictEditorSaveMarkerGuard(t *testing.T) {
	repo := newGitRepo(t, "ceditor-marker-guard")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	if btn := saveButton(t, h); !btn.Disabled {
		t.Fatal("save enabled for the untouched raw conflict")
	}
	// Clicking the disabled button is a no-op: nothing is written or staged.
	if err := h.ui.Click("Save and Mark Resolved"); err != nil {
		t.Fatalf("clicking the disabled save errored: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("the disabled save started a write: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out == "" {
		t.Fatal("the disabled save staged the conflict")
	}

	// The backend refuses the same content even when called directly.
	domainRepo := git.New(repo.StoreRepo().Executor())
	identity, ok := h.app.session.Snapshot().Status.ConflictIdentity("f.txt")
	if !ok {
		t.Fatal("conflict identity missing")
	}
	raw, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("read raw conflict: %v", err)
	}
	if err := domainRepo.SafeWriteResolvedConflict("f.txt", string(raw), identity); !errors.Is(err, git.ErrConflictMarkersRemain) {
		t.Fatalf("backend write error = %v, want ErrConflictMarkersRemain", err)
	}
	assertNotStaged(t, repo, "f.txt")
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(raw) {
		t.Fatalf("the refused write altered the file: %q err=%v", content, err)
	}

	// Once the markers are gone the save fires and stages the file.
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Ours 2")
	h.ui.Frame()
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save still disabled after resolving both blocks")
	}
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("markerless save failed: %v", err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived: %q", out)
	}
}

// When the save's status or stage read fails, nothing is overwritten or
// staged: the working tree bytes and the unmerged entries survive untouched,
// the error shows, and the user's draft is kept for another attempt.
func TestConflictEditorSaveReadFailureProtections(t *testing.T) {
	repo := newGitRepo(t, "ceditor-save-readfail")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Ours 2")
	h.ui.Frame()

	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("fixture: read conflicted f.txt: %v", err)
	}
	stagesBefore := gitOutTrim(t, repo.Path(), "ls-files", "-u")

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error after the injected status failure")
	}
	assertRepoErrorShown(t, h, "")
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(conflictBytes) {
		t.Fatalf("save overwrote the file despite the failed status read: %q err=%v", content, err)
	}
	if stages := gitOutTrim(t, repo.Path(), "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("unmerged stages altered despite the failed status read: before %q after %q", stagesBefore, stages)
	}
	assertNotStaged(t, repo, "f.txt")

	// The draft survives the failure: fixing nothing but the executor lets the
	// retry save exactly what the user composed.
	st := editorState(t, h)
	if st.draft == "" || git.HasConflictMarkers(st.draft) {
		t.Fatalf("draft lost after the failed save: %q", st.draft)
	}
	want := st.draft
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("save retry failed: %v", err)
	}
	assertFileBytes(t, repo, "f.txt", want)
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); out != "" {
		t.Fatalf("unmerged entries survived the retry: %q", out)
	}
}

// When the conflict changes underneath the open editor — resolved externally,
// or its stages replaced — the save refuses to overwrite what the user is now
// looking at, and the error is visible.
func TestConflictEditorSaveStaleIdentityProtections(t *testing.T) {
	repo := newGitRepo(t, "ceditor-save-stale")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Ours 2")
	h.ui.Frame()

	// Externally resolved: the conflict the editor was opened against no
	// longer exists, so the save must neither rewrite the external resolution
	// nor stage over it.
	repo.Git("checkout", "--theirs", "--", "f.txt")
	repo.Git("add", "--", "f.txt")
	repo.WriteFile("f.txt", "external edit\n")
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error for the externally resolved conflict")
	}
	if !h.ui.HasText("no longer unmerged") {
		t.Fatalf("external resolution error not visible; texts=%q", h.ui.Texts())
	}
	assertFileBytes(t, repo, "f.txt", "external edit\n")
	if staged := repo.Git("ls-files", "-s", "--", "f.txt"); !strings.Contains(staged, " 0\t") {
		t.Fatalf("external resolution lost its stage-0 entry: %q", staged)
	}

	// Stages replaced: the conflict is back but its objects moved, so the
	// captured identity refuses the write and the tampered stages survive.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	h.drain()
	selectConflictRow(t, h, "f.txt")
	// The second conflict arrives over the same selection: a manual edit of
	// the result is independent of whatever the editor kept, so the save it
	// feeds is always a clean resolution.
	if st := editorState(t, h); st.mode != 1 {
		openMergeHunks(t, h)
	}
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type("top\nmanual merge\ntail\n")
	h.ui.Frame()
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("save disabled for the manual merge resolution")
	}

	stages := repo.Git("ls-files", "-u")
	tamperedOut, err := tryGitCommandStdin(repo.Path(), "tampered\n", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("fixture: write tampered blob: %v\n%s", err, tamperedOut)
	}
	tampered := strings.TrimSpace(tamperedOut)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(stages), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[2] == "3" {
			line = fields[0] + " " + tampered + " " + fields[2] + "\t" + fields[3]
		}
		lines = append(lines, line)
	}
	repo.Git("update-index", "-q", "--force-remove", "f.txt")
	if out, err := tryGitCommandStdin(repo.Path(), strings.Join(lines, "\n")+"\n", "update-index", "--index-info"); err != nil {
		t.Fatalf("fixture: rewrite stages: %v\n%s", err, out)
	}
	conflictBytes, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt"))
	if err != nil {
		t.Fatalf("fixture: read conflicted f.txt: %v", err)
	}
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("no action error for the replaced conflict stages")
	}
	if !h.ui.HasText("conflict state changed") {
		t.Fatalf("state-changed error not visible; texts=%q", h.ui.Texts())
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "f.txt")); err != nil || string(content) != string(conflictBytes) {
		t.Fatalf("save overwrote the file despite the replaced stages: %q err=%v", content, err)
	}
	if out := gitOutTrim(t, repo.Path(), "ls-files", "-u"); !strings.Contains(out, tampered) {
		t.Fatalf("replaced stages lost despite the refusal: %q", out)
	}
}

// Saving the resolution of an executable conflict keeps the 100755 mode in
// the index and on disk instead of degrading the file to a plain blob.
func TestConflictEditorExecBitPreserved(t *testing.T) {
	repo := newGitRepo(t, "ceditor-execbit")
	full := filepath.Join(repo.Path(), "run.sh")
	writeExec := func(content string) {
		repo.t.Helper()
		if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
			t.Fatalf("write run.sh: %v", err)
		}
		if err := os.Chmod(full, 0o755); err != nil {
			t.Fatalf("chmod run.sh: %v", err)
		}
		repo.Git("add", "--", "run.sh")
	}
	writeExec("#!/bin/sh\necho base\n")
	repo.Git("commit", "-m", "base")
	repo.Git("checkout", "-q", "-b", "side")
	writeExec("#!/bin/sh\necho side\n")
	repo.Git("commit", "-m", "side edits")
	repo.Git("checkout", "-q", "main")
	writeExec("#!/bin/sh\necho main\n")
	repo.Git("commit", "-m", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "run.sh")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("exec save failed: %v", err)
	}
	staged := repo.Git("ls-files", "-s", "--", "run.sh")
	if !strings.HasPrefix(staged, "100755") {
		t.Fatalf("staged mode = %q, want 100755", staged)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("working tree mode lost the exec bit: %v", info.Mode())
	}
	assertFileBytes(t, repo, "run.sh", "#!/bin/sh\necho main\n")
}

// Delete/modify and delete/delete previews name the deleted side exactly, the
// merge mode stays unavailable for them, and a present zero-byte side is never
// misread as a deletion.
func TestConflictEditorDeleteModifyPreviewsAndEmptyFile(t *testing.T) {
	repo := newGitRepo(t, "ceditor-delmod")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)

	// du.txt is deleted by us, modified by them: the deletion note shows while
	// their content fills the other column.
	selectConflictRow(t, h, "du.txt")
	if !h.ui.HasText("This side deleted the file.") {
		t.Fatalf("deletion note missing for du.txt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("side-du") {
		t.Fatalf("theirs content missing for du.txt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Merge Hunks is unavailable because one side deleted this file.") {
		t.Fatalf("delete-side merge hint missing; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("Merge Hunks for du.txt = %+v found=%v, want disabled", mh, ok)
	}

	// ud.txt is modified by us, deleted by them: ours shows the modified
	// content against the deletion note.
	selectConflictRow(t, h, "ud.txt")
	if !h.ui.HasText("This side deleted the file.") {
		t.Fatalf("deletion note missing for ud.txt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main-ud") {
		t.Fatalf("ours content missing for ud.txt; texts=%q", h.ui.Texts())
	}

	// A delete/delete conflict has no side to show at all.
	repo2 := newGitRepo(t, "ceditor-dd")
	deleteDeleteFixture(t, repo2)
	h2 := openConflictedMerge(t, repo2)
	selectConflictRow(t, h2, "dd.txt")
	deletedNotes := 0
	for _, tx := range h2.ui.Texts() {
		if tx == "This side deleted the file." {
			deletedNotes++
		}
	}
	if deletedNotes != 2 {
		t.Fatalf("delete-delete deletion notes = %d, want 2; texts=%q", deletedNotes, h2.ui.Texts())
	}
	if mh, ok := h2.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("Merge Hunks for dd.txt = %+v found=%v, want disabled", mh, ok)
	}

	// A present zero-byte side is a real empty file, never a deletion.
	repo3 := newGitRepo(t, "ceditor-emptyours")
	emptyOursMergeFixture(t, repo3)
	h3 := openConflictedMerge(t, repo3)
	selectConflictRow(t, h3, "f.txt")
	if !h3.ui.HasText("Empty file (0 bytes).") {
		t.Fatalf("empty-file note missing; texts=%q", h3.ui.Texts())
	}
	if h3.ui.HasText("This side deleted the file.") {
		t.Fatalf("zero-byte file misread as a deletion; texts=%q", h3.ui.Texts())
	}
	st := editorState(t, h3)
	if !st.inspection.HaveOurs {
		t.Fatal("empty ours wrongly reported as absent")
	}
	if st.inspection.OursText != "" {
		t.Fatalf("empty ours text = %q, want zero bytes", st.inspection.OursText)
	}
}

// A binary conflict explains why no line merge is possible and directs the
// user to the whole-side actions, which keep working.
func TestConflictEditorBinaryPlaceholder(t *testing.T) {
	repo := newGitRepo(t, "ceditor-binary")
	full := filepath.Join(repo.Path(), "bin.dat")
	writeBin := func(content string) {
		repo.t.Helper()
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write binary: %v", err)
		}
		repo.Git("add", "--", "bin.dat")
	}
	writeBin("a\x00base\n")
	repo.Git("commit", "-m", "base")
	repo.Git("checkout", "-q", "-b", "side")
	writeBin("a\x00side\n")
	repo.Git("commit", "-m", "side edits")
	repo.Git("checkout", "-q", "main")
	writeBin("a\x00main\n")
	repo.Git("commit", "-m", "main edits")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: merge unexpectedly succeeded: %s", out)
	}

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "bin.dat")
	if !h.ui.HasText("Binary file conflict") {
		t.Fatalf("binary placeholder missing; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("Merge Hunks for binary = %+v found=%v, want disabled", mh, ok)
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Open Hunk Resolver"); ok {
		t.Fatal("hunk resolver entry rendered for a binary conflict")
	}
	if !editorState(t, h).inspection.Binary {
		t.Fatal("inspection did not flag the binary conflict")
	}

	// The quick resolution takes the whole binary side byte for byte.
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("binary use ours failed: %v", err)
	}
	if content, err := os.ReadFile(full); err != nil || string(content) != "a\x00main\n" {
		t.Fatalf("binary resolution bytes = %q err=%v", content, err)
	}
}

// A conflict beyond the inline inspection limit shows the too-large
// placeholder instead of hauling megabytes into the view.
func TestConflictEditorTooLargePlaceholder(t *testing.T) {
	repo := newGitRepo(t, "ceditor-toolarge")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	conflictEditor(h.app).maxBytes = 32
	selectConflictRow(t, h, "f.txt")
	if !h.ui.HasText("Conflict file too large to display inline") {
		t.Fatalf("too-large placeholder missing; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("Merge Hunks for too-large = %+v found=%v, want disabled", mh, ok)
	}
	if !editorState(t, h).inspection.TooLarge {
		t.Fatal("inspection did not flag the oversized conflict")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Use Ours"); !ok {
		t.Fatalf("quick actions missing on the too-large card; texts=%q", h.ui.Texts())
	}
}

// A submodule conflict explains that the resolution belongs inside the
// submodule and never offers the line merge.
func TestConflictEditorSubmodulePlaceholder(t *testing.T) {
	repo := newGitRepo(t, "ceditor-submodule")
	plumbSubmoduleConflict(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "sub")
	if !h.ui.HasText("Submodule conflict") {
		t.Fatalf("submodule placeholder missing; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("Merge Hunks for submodule = %+v found=%v, want disabled", mh, ok)
	}
	if !editorState(t, h).inspection.IsSubmodule {
		t.Fatal("inspection did not flag the submodule conflict")
	}
}

// A failed inspection shows the error with a manual retry and never reloads
// on its own: the failure stays cached across frames until the retry click
// clears it, and the retry then loads the conflict.
func TestConflictEditorLoadFailureAndRetry(t *testing.T) {
	repo := newGitRepo(t, "ceditor-loadfail")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected inspect failure"))
	mustClick(t, h, "Conflicted f.txt")
	h.drain()

	if !h.ui.HasText("Failed to load conflict") {
		t.Fatalf("load failure card missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("injected inspect failure") {
		t.Fatalf("load failure error text missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Retry Conflict Load"); !ok {
		t.Fatalf("retry button missing; texts=%q", h.ui.Texts())
	}

	// Frames come and go: the failure stays cached, nothing retries silently.
	for i := 0; i < 4; i++ {
		h.ui.Frame()
	}
	if !h.ui.HasText("Failed to load conflict") {
		t.Fatalf("failure auto-retried across frames; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("main-mid1") {
		t.Fatalf("silent retry loaded the conflict; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Retry Conflict Load")
	h.drain()
	if h.ui.HasText("Failed to load conflict") {
		t.Fatalf("retry did not clear the failure; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("main-mid1") {
		t.Fatalf("retry did not load the conflict; texts=%q", h.ui.Texts())
	}
	if st := editorState(t, h); st.loadErr != nil || !st.loaded {
		t.Fatalf("state after retry: loaded=%v err=%v", st.loaded, st.loadErr)
	}
}

// Redrawing the same selection reuses the cached inspection instead of
// re-reading the repository for every frame.
func TestConflictEditorSameSelectionReadCached(t *testing.T) {
	repo := newGitRepo(t, "ceditor-cache")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")

	counting := &countingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = counting
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = counting.real
	})

	for i := 0; i < 6; i++ {
		h.ui.Frame()
	}
	if counting.reads != 0 {
		t.Fatalf("redrawing the same selection started %d git reads", counting.reads)
	}
	if !h.ui.HasText("main-mid1") {
		t.Fatalf("cached inspection no longer rendered; texts=%q", h.ui.Texts())
	}
}

// Leaving a conflicted file for another and coming back drops the draft, and
// the superseded file's late read never publishes into the editor again.
func TestConflictEditorRaceABASwitchDiscard(t *testing.T) {
	repo := newGitRepo(t, "ceditor-race")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "uu.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	if st := editorState(t, h); !st.draftDirty {
		t.Fatal("fixture: draft did not turn dirty")
	}

	// Switch to aa.txt with the workers paused: selecting a conflicted row
	// starts only its conflict inspection (no combined diff read). Switching
	// back to uu.txt supersedes the aa.txt inspection before it can publish.
	h.queue.PauseWorkers()
	mustClick(t, h, "Conflicted aa.txt")
	h.ui.Frame()
	wInspAA := h.queue.WaitWorker(t)
	if st := editorState(t, h); st.path != "aa.txt" || st.draft != "" {
		t.Fatalf("switching to aa.txt kept uu.txt state: %+v", st)
	}
	mustClick(t, h, "Conflicted uu.txt")
	h.ui.Frame()
	wInspUU := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	// The aa.txt completion arrives first and must be dropped: nothing of it
	// may land in the editor now bound to uu.txt.
	h.queue.ReleaseWorker(wInspAA)
	h.queue.RunNext(t)
	h.ui.Frame()
	st := editorState(t, h)
	if strings.Contains(st.inspection.WorktreeText, "aa") || strings.Contains(st.inspection.OursText, "aa") {
		t.Fatalf("superseded aa.txt read published into uu.txt editor: %+v", st.inspection)
	}

	// Releasing the uu.txt inspection publishes it fresh without resurrecting
	// the draft dropped on departure.
	h.queue.ReleaseWorker(wInspUU)
	h.drain()
	st = editorState(t, h)
	if st.path != "uu.txt" || !st.loaded || st.loadErr != nil {
		t.Fatalf("uu.txt did not load after returning: %+v", st)
	}
	if len(st.inspection.Hunks) == 0 {
		t.Fatal("uu.txt inspection carries no hunks")
	}
	if st.draftDirty {
		t.Fatalf("the dropped uu.txt draft resurrected: %q", st.draft)
	}
	if !strings.Contains(st.draft, "<<<<<<<") {
		t.Fatalf("reseeded draft is not the raw conflict: %q", st.draft)
	}
}

// A status refresh of the same conflict keeps the user's draft — including a
// deliberately emptied zero-byte draft — while the inspection renews.
func TestConflictEditorStatusRefreshKeepsDraft(t *testing.T) {
	repo := newGitRepo(t, "ceditor-refresh")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)

	// A dirty draft survives an unrelated status refresh: the untracked file
	// appearing bumps the status revision without touching the conflict.
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type("top\nuser edit\ntail\n")
	h.ui.Frame()
	before := editorState(t, h).loadedRev
	repo.WriteFile("other.txt", "other\n")
	h.app.session.Reload()
	h.drain()
	// The first drain settles the snapshot; the refresh inspection starts on
	// its trailing frame, so the second drain waits for it to publish.
	h.drain()
	st := editorState(t, h)
	if st.draft != "top\nuser edit\ntail\n" || !st.draftDirty {
		t.Fatalf("status refresh lost the draft: %q dirty=%v", st.draft, st.draftDirty)
	}
	if st.loadedRev <= before {
		t.Fatalf("status refresh did not renew the inspection: before %d after %d", before, st.loadedRev)
	}

	// So does the deliberately emptied zero-byte draft.
	h.ui.Command("selectAll")
	h.ui.Key(0, ui.KeyBackspace)
	h.ui.Frame()
	repo.WriteFile("another.txt", "another\n")
	h.app.session.Reload()
	h.drain()
	h.drain()
	st = editorState(t, h)
	if st.draft != "" || !st.draftDirty {
		t.Fatalf("status refresh reseeded over the emptied draft: %q dirty=%v", st.draft, st.draftDirty)
	}
	if len(st.inspection.Hunks) != 2 {
		t.Fatalf("inspection lost its hunks across refreshes: %+v", st.inspection.Hunks)
	}
}

// An inspection read made stale by a status revision moving underneath it
// never publishes: the completion is dropped and the fresh read for the new
// revision takes its place.
func TestConflictEditorStaleRevisionReadDropped(t *testing.T) {
	repo := newGitRepo(t, "ceditor-stalerev")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	loadedAtStart := editorState(t, h).loadedRev

	// An unrelated file appearing bumps the status revision. The snapshot
	// callback runs alone; only then the workers pause, so the refresh
	// inspection the next frame starts is captured before it can run.
	revBeforeWrite := h.app.changesView.statusRev
	repo.WriteFile("other.txt", "other\n")
	h.app.session.Reload()
	h.queue.RunNext(t)
	h.queue.PauseWorkers()
	h.ui.Frame()
	if h.app.changesView.statusRev == revBeforeWrite {
		t.Fatal("fixture: first status change did not bump the revision")
	}
	wInsp := h.queue.WaitWorker(t)
	if st := editorState(t, h); !st.loading {
		t.Fatalf("refresh inspection did not start: %+v", st)
	}
	revBeforeStale := h.app.changesView.statusRev
	h.queue.ResumeWorkers()

	// Another unrelated change moves the revision again before the parked
	// read runs: its completion, when it finally arrives, is already stale.
	// The snapshot callback runs alone so the drain cannot wait on the parked
	// inspection.
	repo.WriteFile("another.txt", "another\n")
	h.app.session.Reload()
	h.queue.RunNext(t)
	h.ui.Frame()
	if h.app.changesView.statusRev <= revBeforeStale {
		t.Fatal("fixture: second status change did not bump the revision")
	}
	liveRev := h.app.changesView.statusRev

	h.queue.ReleaseWorker(wInsp)
	h.queue.RunNext(t)
	st := editorState(t, h)
	if st.loadedRev == revBeforeStale {
		t.Fatalf("stale revision %d published its inspection", revBeforeStale)
	}

	// The fresh read for the live revision publishes instead.
	h.ui.Frame()
	h.drain()
	st = editorState(t, h)
	if !st.loaded || st.loadErr != nil {
		t.Fatalf("fresh inspection did not land: %+v err=%v", st, st.loadErr)
	}
	if st.loadedRev != liveRev {
		t.Fatalf("inspection revision = %d, want the live %d", st.loadedRev, liveRev)
	}
	if st.loadedRev == loadedAtStart {
		t.Fatal("inspection never moved past the first load")
	}
}

// Switching the repository clears the editor state and the draft, so nothing
// of one repository's conflict can bleed into another's.
func TestConflictEditorRepoSwitchClearsState(t *testing.T) {
	repoA := newGitRepo(t, "ceditor-switch-a")
	twoHunkMergeFixture(t, repoA)
	repoB := newGitRepo(t, "ceditor-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")

	h := newTestApp(t, repoB.StoreRepo())
	h.openRepo(repoA.StoreRepo())
	h.drain()
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	if st := editorState(t, h); !st.draftDirty {
		t.Fatal("fixture: draft did not turn dirty")
	}

	saved, ok := h.store.Get(h.store.Repos()[0].ID)
	if !ok {
		t.Fatal("seeded repository missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.drain()

	st := editorState(t, h)
	if st.path != "" || st.draft != "" || st.draftDirty || st.loaded {
		t.Fatalf("editor state survived the repository switch: %+v", st)
	}
	if h.ui.HasText("Conflict 1") {
		t.Fatalf("merge view still rendered after the switch; texts=%q", h.ui.Texts())
	}
}

// --- SSH loopback ---

// remoteTwoHunkConflictFixture builds the two-block both-modified conflict on
// the server: ours edits both mid lines to main-mid*, theirs to side-mid*.
func remoteTwoHunkConflictFixture(t *testing.T, srv *sshtest.Target, dir string) {
	t.Helper()
	remoteCommitFile(t, srv, dir, "m.txt", "top\nbase-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nbase-mid2\ntail\n", "base of m")
	srv.RunGit(t, dir, "checkout", "-q", "-b", "side")
	remoteCommitFile(t, srv, dir, "m.txt", "top\nside-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nside-mid2\ntail\n", "side edits")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	remoteCommitFile(t, srv, dir, "m.txt", "top\nmain-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nmain-mid2\ntail\n", "main edits")
	remoteMergeShell(t, srv, dir, "side")
}

// openSSHConflict opens the loopback SSH repository in the harness and
// verifies the snapshot sees the conflict.
func openSSHConflict(t *testing.T, repo store.Repo) *testHarness {
	t.Helper()
	h := newTestApp(t, repo)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}
	if st := h.app.session.Snapshot().Status; !st.Conflicted() {
		t.Fatalf("fixture: SSH snapshot does not report the conflict: %+v", st)
	}
	return h
}

// remoteTwoHunkPickConflictFixture builds a two-block conflict on the server
// from a cherry-pick: a picked commit edits both mid lines of m.txt to
// side-mid*, main edits the same lines to main-mid*, so picking stops on a
// two-block conflict with the operation banner in progress.
func remoteTwoHunkPickConflictFixture(t *testing.T, srv *sshtest.Target, dir string) {
	t.Helper()
	remoteCommitFile(t, srv, dir, "m.txt", "top\nbase-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nbase-mid2\ntail\n", "base of m")
	srv.RunGit(t, dir, "checkout", "-q", "-b", "side")
	pick := remoteCommitFile(t, srv, dir, "m.txt", "top\nside-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nside-mid2\ntail\n", "side edits")
	srv.RunGit(t, dir, "checkout", "-q", "main")
	remoteCommitFile(t, srv, dir, "m.txt", "top\nmain-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nmain-mid2\ntail\n", "main edits")
	cmd := "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C " + git.ShellQuote(dir) + " cherry-pick " + pick + " >/dev/null 2>&1 || true"
	srv.RunShell(t, cmd)
	if out := srv.RunGit(t, dir, "ls-files", "-u"); strings.TrimSpace(out) == "" {
		t.Fatal("fixture: remote cherry-pick produced no unmerged entries")
	}
}

// Over SSH the hunk editor composes a mixed resolution, the manual edit and
// the save survive the wire byte for byte, and Continue finishes the
// cherry-pick as its commit.
func TestConflictEditorHunkMergeSaveContinueLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteTwoHunkPickConflictFixture(t, srv, repo.Path)

	h := openSSHConflict(t, repo)
	selectConflictRow(t, h, "m.txt")
	if !h.ui.HasText("main-mid1") || !h.ui.HasText("side-mid1") {
		t.Fatalf("SSH overview missing side previews; texts=%q", h.ui.Texts())
	}
	openMergeHunks(t, h)

	// First block ours, second block both, then a manual fusion of the result.
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Ours 2")
	h.ui.Frame()
	mustClick(t, h, "Theirs 2")
	h.ui.Frame()
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type("top\nssh manual fusion\ntail\n")
	h.ui.Frame()
	if btn := saveButton(t, h); btn.Disabled {
		t.Fatal("SSH save disabled for the manual fusion")
	}
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH save and mark resolved failed: %v", err)
	}
	if got := srv.ReadFile(t, repo.Path, "m.txt"); string(got) != "top\nssh manual fusion\ntail\n" {
		t.Fatalf("SSH m.txt after save = %q, want the manual fusion", string(got))
	}
	if out := srv.RunGit(t, repo.Path, "ls-files", "-u"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH unmerged entries survived the save: %q", out)
	}
	if staged := srv.RunGit(t, repo.Path, "ls-files", "-s", "--", "m.txt"); !strings.Contains(staged, " 0\t") {
		t.Fatalf("SSH m.txt not staged at stage 0: %q", staged)
	}

	// The last conflict resolved, Continue finishes the cherry-pick.
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("SSH Continue Operation = %+v found=%v, want enabled", btn, ok)
	}
	mustClick(t, h, "Continue Operation")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue failed: %v", err)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "side edits" {
		t.Fatalf("SSH continued commit subject = %q, want side edits", subject)
	}
	if tree := srv.RunGit(t, repo.Path, "show", "HEAD:m.txt"); tree != "top\nssh manual fusion\ntail" {
		t.Fatalf("SSH committed tree m.txt = %q, want the manual fusion", tree)
	}
	bannerGone(t, h)
}

// Over SSH the delete/modify previews and the empty-file distinction hold
// exactly as locally.
func TestConflictEditorDeleteModifyAndEmptyLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	// d.txt deleted by us and modified by them; e.txt modified by us and
	// deleted by them.
	remoteCommitFile(t, srv, repo.Path, "d.txt", "d-base\n", "add d")
	remoteCommitFile(t, srv, repo.Path, "e.txt", "e-base\n", "add e")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "side")
	remoteCommitFile(t, srv, repo.Path, "d.txt", "d-side\n", "side modifies d")
	srv.RunGit(t, repo.Path, "rm", "-q", "e.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "side deletes e")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.RunGit(t, repo.Path, "rm", "-q", "d.txt")
	remoteCommitFile(t, srv, repo.Path, "e.txt", "e-main\n", "main deletes d and modifies e")
	remoteMergeShell(t, srv, repo.Path, "side")

	h := openSSHConflict(t, repo)

	selectConflictRow(t, h, "d.txt")
	if !h.ui.HasText("This side deleted the file.") {
		t.Fatalf("SSH deletion note missing for d.txt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("d-side") {
		t.Fatalf("SSH theirs content missing for d.txt; texts=%q", h.ui.Texts())
	}
	if mh, ok := h.ui.AccessByRole(ui.RoleButton, "Merge Hunks"); !ok || !mh.Disabled {
		t.Fatalf("SSH Merge Hunks for d.txt = %+v found=%v, want disabled", mh, ok)
	}

	selectConflictRow(t, h, "e.txt")
	if !h.ui.HasText("This side deleted the file.") {
		t.Fatalf("SSH deletion note missing for e.txt; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("e-main") {
		t.Fatalf("SSH ours content missing for e.txt; texts=%q", h.ui.Texts())
	}

	// The zero-byte side over SSH is a real empty file, not a deletion.
	srv.RunGit(t, repo.Path, "merge", "--abort")
	remoteCommitFile(t, srv, repo.Path, "z.txt", "z-base\n", "add z")
	srv.RunGit(t, repo.Path, "checkout", "-q", "-b", "zside")
	remoteCommitFile(t, srv, repo.Path, "z.txt", "z-side\n", "side edits z")
	srv.RunGit(t, repo.Path, "checkout", "-q", "main")
	srv.WriteFile(t, repo.Path, "z.txt", []byte{})
	srv.RunGit(t, repo.Path, "add", "--", "z.txt")
	srv.RunGit(t, repo.Path, "commit", "-q", "-m", "main empties z")
	remoteMergeShell(t, srv, repo.Path, "zside")
	h.app.session.Reload()
	h.drain()

	selectConflictRow(t, h, "z.txt")
	if !h.ui.HasText("Empty file (0 bytes).") {
		t.Fatalf("SSH empty-file note missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("This side deleted the file.") {
		t.Fatalf("SSH zero-byte file misread as a deletion; texts=%q", h.ui.Texts())
	}
}

// Over SSH the save protections hold byte for byte on the remote working
// tree: a failed read and a conflict resolved externally both refuse without
// touching the file or the index.
func TestConflictEditorSaveProtectionsLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	remoteTwoHunkConflictFixture(t, srv, repo.Path)

	h := openSSHConflict(t, repo)
	selectConflictRow(t, h, "m.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Ours 1")
	h.ui.Frame()
	mustClick(t, h, "Ours 2")
	h.ui.Frame()

	conflictBytes := srv.ReadFile(t, repo.Path, "m.txt")
	stagesBefore := srv.RunGit(t, repo.Path, "ls-files", "-u")

	// A failed status read refuses before anything is written remotely.
	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("SSH: no action error after the injected status failure")
	}
	if got := srv.ReadFile(t, repo.Path, "m.txt"); string(got) != string(conflictBytes) {
		t.Fatalf("SSH file overwritten despite the failed status read: before %q after %q", conflictBytes, got)
	}
	if stages := srv.RunGit(t, repo.Path, "ls-files", "-u"); stages != stagesBefore {
		t.Fatalf("SSH unmerged stages altered despite the failed status read: before %q after %q", stagesBefore, stages)
	}
	st := editorState(t, h)
	if st.draft == "" || git.HasConflictMarkers(st.draft) {
		t.Fatalf("SSH draft lost after the failed save: %q", st.draft)
	}

	// Externally resolved on the server: the save refuses and the external
	// resolution survives byte for byte.
	srv.RunGit(t, repo.Path, "checkout", "--theirs", "--", "m.txt")
	srv.RunGit(t, repo.Path, "add", "--", "m.txt")
	mustClick(t, h, "Save and Mark Resolved")
	h.drain()
	if err := h.app.session.ActionErr(); err == nil {
		t.Fatal("SSH: no action error for the externally resolved conflict")
	}
	if !h.ui.HasText("no longer unmerged") {
		t.Fatalf("SSH external resolution error not visible; texts=%q", h.ui.Texts())
	}
	if got := srv.ReadFile(t, repo.Path, "m.txt"); string(got) != "top\nside-mid1\ng1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\nside-mid2\ntail\n" {
		t.Fatalf("SSH external resolution overwritten: %q", string(got))
	}
	if staged := srv.RunGit(t, repo.Path, "diff", "--cached", "--name-only"); !strings.Contains(staged, "m.txt") {
		t.Fatalf("SSH external resolution disturbed by the refused write: %q", staged)
	}
}

// assertControlsInsideWindow verifies that every named accessible control is
// present with non-empty bounds inside [0, winW], and that no two sibling
// controls overlap.
func assertControlsInsideWindow(t *testing.T, h *testHarness, winW int, contextLabel string, controls []struct {
	role  ui.Role
	label string
}) {
	t.Helper()
	type placed struct {
		label string
		rect  ui.Rect
	}
	placedList := make([]placed, 0, len(controls))
	limit := float32(winW) + 0.5
	for _, ctrl := range controls {
		info, ok := h.ui.AccessByRole(ctrl.role, ctrl.label)
		if !ok {
			t.Fatalf("%s (%dpx): control %q missing; texts=%q", contextLabel, winW, ctrl.label, h.ui.Texts())
		}
		b := info.Bounds
		if b.W <= 0 || b.H <= 0 {
			t.Fatalf("%s (%dpx): control %q has empty bounds %+v", contextLabel, winW, ctrl.label, b)
		}
		if b.X < -0.5 || b.X+b.W > limit {
			t.Fatalf("%s (%dpx): control %q bounds %+v overflow window right %.0f", contextLabel, winW, ctrl.label, b, float32(winW))
		}
		placedList = append(placedList, placed{label: ctrl.label, rect: b})
	}
	for i := 0; i < len(placedList); i++ {
		for j := i + 1; j < len(placedList); j++ {
			a, b := placedList[i].rect, placedList[j].rect
			if a.X+a.W > b.X+0.5 && b.X+b.W > a.X+0.5 && a.Y+a.H > b.Y+0.5 && b.Y+b.H > a.Y+0.5 {
				t.Fatalf("%s (%dpx): controls %q (%+v) and %q (%+v) overlap", contextLabel, winW, placedList[i].label, a, placedList[j].label, b)
			}
		}
	}
}

// Header mode toggles, quick actions (including long delete/modify labels),
// and Merge Hunks toolbar controls (including Save & Mark Resolved once all
// conflicts are resolved) must stay inside the window across default, test,
// minimum, and narrow window widths.
func TestConflictEditorToolbarAndHeaderLayoutFitsWindows(t *testing.T) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"default1180", 1180, 780},
		{"harness1200", 1200, 800},
		{"min880", 880, 580},
		{"narrow820", 820, 700},
	}

	uuRepo := newGitRepo(t, "ceditor-layout-uu")
	twoHunkMergeFixture(t, uuRepo)
	hUU := openConflictedMerge(t, uuRepo)
	selectConflictRow(t, hUU, "f.txt")

	uuOverviewControls := []struct {
		role  ui.Role
		label string
	}{
		{ui.RoleButton, "Overview"},
		{ui.RoleButton, "Merge Hunks"},
		{ui.RoleButton, "Use Ours"},
		{ui.RoleButton, "Use Theirs"},
		{ui.RoleButton, "Mark Resolved"},
		{ui.RoleButton, "Open Hunk Resolver"},
	}
	for _, sz := range sizes {
		hUU.ui.SetSize(sz.w, sz.h)
		hUU.ui.Frame()
		assertControlsInsideWindow(t, hUU, sz.w, "UU overview "+sz.name, uuOverviewControls)
	}

	hUU.ui.SetSize(1200, 800)
	hUU.ui.Frame()
	openMergeHunks(t, hUU)
	mergeControls := []struct {
		role  ui.Role
		label string
	}{
		{ui.RoleButton, "Overview"},
		{ui.RoleButton, "Merge Hunks"},
		{ui.RoleButton, "Use Ours"},
		{ui.RoleButton, "Use Theirs"},
		{ui.RoleButton, "Mark Resolved"},
		{ui.RoleButton, "Previous Conflict"},
		{ui.RoleButton, "Next Conflict"},
		{ui.RoleButton, "Save and Mark Resolved"},
		{ui.RoleCheckBox, "All Ours"},
		{ui.RoleCheckBox, "All Theirs"},
		{ui.RoleCheckBox, "Column layout"},
	}
	for _, sz := range sizes {
		hUU.ui.SetSize(sz.w, sz.h)
		hUU.ui.Frame()
		assertControlsInsideWindow(t, hUU, sz.w, "UU merge unresolved "+sz.name, mergeControls)
	}

	// Resolving all hunks expands the progress label to "All conflicts resolved";
	// Save and Mark Resolved must still sit strictly inside the window.
	hUU.ui.SetSize(1200, 800)
	hUU.ui.Frame()
	mustClick(t, hUU, "All Ours")
	hUU.ui.Frame()
	for _, sz := range sizes {
		hUU.ui.SetSize(sz.w, sz.h)
		hUU.ui.Frame()
		assertControlsInsideWindow(t, hUU, sz.w, "UU merge resolved "+sz.name, mergeControls)
	}

	// Delete/modify conflicts have the longest quick-action labels.
	duRepo := newGitRepo(t, "ceditor-layout-du")
	multiTypeMergeFixture(t, duRepo)
	hDU := openConflictedMerge(t, duRepo)
	selectConflictRow(t, hDU, "du.txt")
	duControls := []struct {
		role  ui.Role
		label string
	}{
		{ui.RoleButton, "Overview"},
		{ui.RoleButton, "Merge Hunks"},
		{ui.RoleButton, "Use Ours (Delete File)"},
		{ui.RoleButton, "Use Theirs (Keep File)"},
		{ui.RoleButton, "Mark Resolved"},
	}
	for _, sz := range sizes {
		hDU.ui.SetSize(sz.w, sz.h)
		hDU.ui.Frame()
		assertControlsInsideWindow(t, hDU, sz.w, "DU overview "+sz.name, duControls)
	}
}

// The header progress badge never claims "All conflicts resolved" on
// delete/modify or delete/delete conflicts, uses proper singular/plural
// wording, and recounts actual conflict markers after manual edits.
func TestConflictEditorProgressBadgeAndManualMarkerRecount(t *testing.T) {
	// Delete/modify and delete/delete conflicts show only their conflict type,
	// never "All conflicts resolved".
	repoMT := newGitRepo(t, "ceditor-badge-delmod")
	multiTypeMergeFixture(t, repoMT)
	hMT := openConflictedMerge(t, repoMT)
	selectConflictRow(t, hMT, "du.txt")
	if !hMT.ui.HasText("Deleted by Us") {
		t.Fatalf("du.txt missing conflict type badge; texts=%q", hMT.ui.Texts())
	}
	if hMT.ui.HasText("All conflicts resolved") {
		t.Fatalf("unresolved du.txt wrongly displayed All conflicts resolved; texts=%q", hMT.ui.Texts())
	}
	selectConflictRow(t, hMT, "ud.txt")
	if !hMT.ui.HasText("Deleted by Them") {
		t.Fatalf("ud.txt missing conflict type badge; texts=%q", hMT.ui.Texts())
	}
	if hMT.ui.HasText("All conflicts resolved") {
		t.Fatalf("unresolved ud.txt wrongly displayed All conflicts resolved; texts=%q", hMT.ui.Texts())
	}

	repoDD := newGitRepo(t, "ceditor-badge-dd")
	deleteDeleteFixture(t, repoDD)
	hDD := openConflictedMerge(t, repoDD)
	selectConflictRow(t, hDD, "dd.txt")
	if !hDD.ui.HasText("Both Deleted") {
		t.Fatalf("dd.txt missing conflict type badge; texts=%q", hDD.ui.Texts())
	}
	if hDD.ui.HasText("All conflicts resolved") {
		t.Fatalf("unresolved dd.txt wrongly displayed All conflicts resolved; texts=%q", hDD.ui.Texts())
	}

	// Single-hunk conflict uses singular "1 conflict" and "1 conflict block".
	repoOne := newGitRepo(t, "ceditor-badge-one")
	uuMergeFixture(t, repoOne)
	hOne := openConflictedMerge(t, repoOne)
	selectConflictRow(t, hOne, "f.txt")
	if !hOne.ui.HasText("1 conflict") || hOne.ui.HasText("1 conflicts") {
		t.Fatalf("single-hunk header badge pluralization wrong; texts=%q", hOne.ui.Texts())
	}
	if !hOne.ui.HasText("1 conflict block") || hOne.ui.HasText("1 conflict blocks") {
		t.Fatalf("single-hunk overview block count pluralization wrong; texts=%q", hOne.ui.Texts())
	}

	// Two-hunk conflict updates count via checkboxes and manual edits alike.
	repoTwo := newGitRepo(t, "ceditor-badge-two")
	twoHunkMergeFixture(t, repoTwo)
	hTwo := openConflictedMerge(t, repoTwo)
	selectConflictRow(t, hTwo, "f.txt")
	if !hTwo.ui.HasText("2 conflicts") || !hTwo.ui.HasText("2 conflict blocks") {
		t.Fatalf("two-hunk initial counts missing; texts=%q", hTwo.ui.Texts())
	}
	openMergeHunks(t, hTwo)

	mustClick(t, hTwo, "Ours 1")
	hTwo.ui.Frame()
	if !hTwo.ui.HasText("1 conflict") || hTwo.ui.HasText("1 conflicts") {
		t.Fatalf("after resolving one of two hunks want '1 conflict'; texts=%q", hTwo.ui.Texts())
	}

	mustClick(t, hTwo, "Ours 2")
	hTwo.ui.Frame()
	if !hTwo.ui.HasText("All conflicts resolved") {
		t.Fatalf("after resolving both hunks want 'All conflicts resolved'; texts=%q", hTwo.ui.Texts())
	}

	// Manually typing a complete conflict block back after all checkboxes were
	// checked recounts from the actual markers (never "0 conflicts").
	mustClick(t, hTwo, "Conflict result")
	hTwo.ui.Command("selectAll")
	hTwo.ui.Type("<<<<<<< HEAD\na\n=======\nb\n>>>>>>> side\n")
	hTwo.ui.Frame()
	if hTwo.ui.HasText("All conflicts resolved") || hTwo.ui.HasText("0 conflicts") {
		t.Fatalf("manual conflict block wrongly reported resolved or 0 conflicts; texts=%q", hTwo.ui.Texts())
	}
	if !hTwo.ui.HasText("1 conflict") {
		t.Fatalf("manual conflict block did not recount to '1 conflict'; texts=%q", hTwo.ui.Texts())
	}

	// Typing two complete conflict blocks recounts to "2 conflicts".
	hTwo.ui.Command("selectAll")
	hTwo.ui.Type("<<<<<<< HEAD\na\n=======\nb\n>>>>>>> side\nmid\n<<<<<<< HEAD\nc\n=======\nd\n>>>>>>> side\n")
	hTwo.ui.Frame()
	if !hTwo.ui.HasText("2 conflicts") {
		t.Fatalf("two manual conflict blocks did not recount to '2 conflicts'; texts=%q", hTwo.ui.Texts())
	}

	// Typing a truncated marker line still counts as unresolved, never "0 conflicts".
	hTwo.ui.Command("selectAll")
	hTwo.ui.Type("half resolved\n>>>>>>> leftover\n")
	hTwo.ui.Frame()
	if hTwo.ui.HasText("0 conflicts") || hTwo.ui.HasText("All conflicts resolved") {
		t.Fatalf("truncated marker reported 0 conflicts or resolved; texts=%q", hTwo.ui.Texts())
	}
	if !hTwo.ui.HasText("1 conflict") {
		t.Fatalf("truncated marker did not report '1 conflict'; texts=%q", hTwo.ui.Texts())
	}

	// Manually removing all markers without using checkboxes reports resolved.
	hTwo.ui.Command("selectAll")
	hTwo.ui.Type("clean manual merge\n")
	hTwo.ui.Frame()
	if !hTwo.ui.HasText("All conflicts resolved") {
		t.Fatalf("clean manual merge did not report 'All conflicts resolved'; texts=%q", hTwo.ui.Texts())
	}
}

// diffTrackingExecutor wraps a git.Executor and counts any "diff" subcommand
// invocations behind a mutex, proving conflict row selection skips combined
// diff loading.
type diffTrackingExecutor struct {
	real git.Executor

	mu        sync.Mutex
	diffCalls int
}

func (e *diffTrackingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "diff" {
		e.mu.Lock()
		e.diffCalls++
		e.mu.Unlock()
	}
	return e.real.Run(ctx, stdin, args...)
}

func (e *diffTrackingExecutor) Describe() string { return e.real.Describe() }

func (e *diffTrackingExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.diffCalls
}

// Clicking a conflicted file row selects it directly for the conflict editor
// without starting a spurious git diff read that the next frame would cancel.
func TestConflictEditorSelectConflictSkipsCombinedDiff(t *testing.T) {
	repo := newGitRepo(t, "ceditor-nodiff")
	multiTypeMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	tracker := &diffTrackingExecutor{real: h.app.session.Repo().Exec}
	h.app.session.Repo().Exec = tracker
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		h.app.session.Repo().Exec = tracker.real
	})

	selectConflictRow(t, h, "uu.txt")
	selectConflictRow(t, h, "aa.txt")
	if got := tracker.count(); got != 0 {
		t.Fatalf("selecting conflicted rows started %d git diff invocations, want 0", got)
	}
}

// While conflicted files exist and no non-conflicted unstaged or staged files
// remain, the Unstaged (0) section says "No unstaged changes" instead of
// claiming "Working tree is clean".
func TestConflictEditorUnstagedEmptyMessageWhileConflicted(t *testing.T) {
	repo := newGitRepo(t, "ceditor-unstaged-empty")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	if !h.ui.HasText("Unstaged (0)") {
		t.Fatalf("Unstaged (0) header missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("Working tree is clean") {
		t.Fatalf("conflicted repository claimed 'Working tree is clean'; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("No unstaged changes") {
		t.Fatalf("conflicted repository missing 'No unstaged changes'; texts=%q", h.ui.Texts())
	}
}

// Resolving a conflict through a header quick action clears the open editor's
// draft so a later conflict with the same identity does not resurrect stale
// text.
func TestConflictEditorQuickResolveClearsDraft(t *testing.T) {
	repo := newGitRepo(t, "ceditor-quick-reset")
	twoHunkMergeFixture(t, repo)

	h := openConflictedMerge(t, repo)
	selectConflictRow(t, h, "f.txt")
	openMergeHunks(t, h)
	mustClick(t, h, "Conflict result")
	h.ui.Command("selectAll")
	h.ui.Type("stale custom draft\n")
	h.ui.Frame()
	if st := editorState(t, h); !st.draftDirty {
		t.Fatal("fixture: draft did not turn dirty")
	}

	// Resolve with the quick action instead of Save & Mark Resolved.
	mustClick(t, h, "Use Ours")
	h.ui.Frame()
	confirmResolveOpen(t, h)
	clickConfirmResolve(t, h)
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick Use Ours failed: %v", err)
	}

	// Recreate the exact same conflict in the same repository and re-select it:
	// the previous draft must not resurface.
	head := headOf(t, repo)
	repo.Git("reset", "--hard", "-q", head)
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("fixture: second merge unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	selectConflictRow(t, h, "f.txt")
	st := editorState(t, h)
	if st.draftDirty || st.draft == "stale custom draft\n" {
		t.Fatalf("quick resolve left stale draft behind: dirty=%v draft=%q", st.draftDirty, st.draft)
	}
	if !strings.Contains(st.draft, "<<<<<<<") {
		t.Fatalf("fresh conflict did not reseed raw conflict text: %q", st.draft)
	}
}
