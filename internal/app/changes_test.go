package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

type failFirstRunExecutor struct {
	real git.Executor
	args func(args []string) bool
	err  error

	mu   sync.Mutex
	done bool
}

func (f *failFirstRunExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	match := false
	if f.args == nil {
		match = true
	} else {
		match = f.args(append([]string(nil), args...))
	}
	f.mu.Lock()
	fire := !f.done && match
	if fire {
		f.done = true
	}
	f.mu.Unlock()
	if fire {
		return nil, f.err
	}
	return f.real.Run(ctx, stdin, args...)
}

func (f *failFirstRunExecutor) Describe() string { return f.real.Describe() }

type commandFailExecutor struct {
	real git.Executor
	fail func(args []string) bool
	err  error
}

func (f *commandFailExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if f.fail != nil && f.fail(append([]string(nil), args...)) {
		return nil, f.err
	}
	return f.real.Run(ctx, stdin, args...)
}

func (f *commandFailExecutor) Describe() string { return f.real.Describe() }

func installCommandFailExecutor(t *testing.T, h *testHarness, fail func([]string) bool, err error) {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("cannot install failing executor without an open repository")
	}
	wrapped := &commandFailExecutor{real: repo.Exec, fail: fail, err: err}
	repo.Exec = wrapped
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		repo.Exec = wrapped.real
	})
}

func isStatusCommand(args []string) bool {
	return len(args) > 0 && args[0] == "status"
}

func isRevParseHEAD(args []string) bool {
	if len(args) < 3 || args[0] != "rev-parse" {
		return false
	}
	for _, arg := range args[1:] {
		if arg == "HEAD" {
			return true
		}
	}
	return false
}

func installFailFirstExecutor(t *testing.T, h *testHarness, match func([]string) bool, err error) {
	t.Helper()
	repo := h.app.session.Repo()
	if repo == nil {
		t.Fatal("cannot install failing executor without an open repository")
	}
	wrapped := &failFirstRunExecutor{real: repo.Exec, args: match, err: err}
	repo.Exec = wrapped
	t.Cleanup(func() {
		h.queue.releaseAllAndWait()
		repo.Exec = wrapped.real
	})
}

func openChangesHarness(t *testing.T, f *gitRepoFixture) *testHarness {
	t.Helper()
	h := newTestApp(t)
	h.openRepo(f.StoreRepo())
	h.drain()
	if h.app.route.page != pageChanges {
		t.Fatalf("initial page = %v, want pageChanges", h.app.route.page)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial snapshot load failed: %v", err)
	}
	return h
}

func changesAccessButton(t *testing.T, h *testHarness, name string) ui.AccessInfo {
	t.Helper()
	info, ok := h.ui.AccessByRole(ui.RoleButton, name)
	if !ok {
		t.Fatalf("expected button %q; texts=%q", name, h.ui.Texts())
	}
	return info
}

func mustClick(t *testing.T, h *testHarness, label string) {
	t.Helper()
	if err := h.ui.Click(label); err != nil {
		t.Fatalf("click %q: %v\ntexts=%q", label, err, h.ui.Texts())
	}
}

func typeCommitMessage(t *testing.T, h *testHarness, message string) {
	t.Helper()
	mustClick(t, h, "Commit message")
	h.ui.Command("selectAll")
	h.ui.Type(message)
	h.ui.Frame()
	if got := h.app.changesView.message; got != message {
		t.Fatalf("typed commit message = %q, want %q", got, message)
	}
}

func selectAmend(t *testing.T, h *testHarness) {
	t.Helper()
	mustClick(t, h, "Amend")
	h.ui.Frame()
	if !h.app.changesView.amend {
		t.Fatal("clicking Amend did not enable amend mode")
	}
}

func headOf(t *testing.T, f *gitRepoFixture) string {
	t.Helper()
	return strings.TrimSpace(f.Git("rev-parse", "HEAD"))
}

func TestChangesListsRenderStatusesRenameAndEmptyState(t *testing.T) {
	// A real status populates the Unstaged and Staged lists with counts, file
	// status letters, and rename paths; a clean tree shows explicit empty states.
	repo := newGitRepo(t, "changes-lists")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")

	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")
	repo.WriteFile("staged-new.txt", "staged\n")
	repo.Git("add", "--", "staged-new.txt")
	repo.Git("mv", "tracked.txt", "renamed.txt")

	h := openChangesHarness(t, repo)

	for _, text := range []string{"Unstaged (2)", "Staged (2)", "untracked.txt", "staged-new.txt", "tracked.txt → renamed.txt"} {
		if !h.ui.HasText(text) {
			t.Fatalf("expected %q in Local Changes page; texts=%q", text, h.ui.Texts())
		}
	}
	for _, label := range []string{"Unstaged renamed.txt", "Unstaged untracked.txt", "Staged renamed.txt", "Staged staged-new.txt"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("missing accessible row %q; texts=%q", label, h.ui.Texts())
		}
	}
	if err := execGitStatusSanity(repo); err != nil {
		t.Fatalf("git CLI sanity failed before rename assertion: %v", err)
	}

	// Returning the rename to its original path removes the rename display.
	// git mv updates the index itself, so move first and only then reset the
	// pre-existing staged new file.
	repo.Git("mv", "renamed.txt", "tracked.txt")
	repo.Git("reset", "-q")
	h.app.session.Reload()
	h.drain()
	if !h.ui.HasText("Unstaged tracked.txt") {
		t.Fatalf("tracked path row missing after undoing rename; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("tracked.txt → renamed.txt") {
		t.Fatalf("stale rename display survived undoing rename; texts=%q", h.ui.Texts())
	}

	// With no changes, both sections explain their empty state instead of
	// rendering blank lists.
	repo.Git("reset", "--hard", "HEAD")
	repo.Git("clean", "-fdq")
	h.app.session.Reload()
	h.drain()
	for _, text := range []string{"Unstaged (0)", "Staged (0)", "Working tree is clean", "No staged changes"} {
		if !h.ui.HasText(text) {
			t.Fatalf("expected clean empty-state text %q; texts=%q", text, h.ui.Texts())
		}
	}
}

func execGitStatusSanity(repo *gitRepoFixture) error {
	out := repo.Git("status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if len(out) == 0 {
		return fmt.Errorf("git status returned empty output")
	}
	return nil
}

func TestChangesStageUnstageSingleAndAllThroughRealUI(t *testing.T) {
	// Clicking per-file and list-header Stage/Unstage controls moves real files
	// between the two lists and updates the sidebar Changes badge after reload.
	repo := newGitRepo(t, "changes-stage")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")

	h := openChangesHarness(t, repo)

	mustClick(t, h, "Stage tracked.txt")
	h.drain()
	st := h.app.session.Snapshot().Status
	if _, ok := findChange(st.Staged, "tracked.txt"); !ok {
		t.Fatalf("tracked.txt was not staged: %+v", st)
	}
	if _, ok := findChange(st.Unstaged, "untracked.txt"); !ok {
		t.Fatalf("untracked.txt should remain unstaged: %+v", st)
	}
	if badge, ok := h.ui.AccessByRole(ui.RoleButton, "Changes"); !ok || !strings.Contains(badge.Name, "Changes") {
		t.Fatalf("sidebar Changes button missing after stage: %+v found=%v", badge, ok)
	}
	if !h.ui.HasText("2") {
		t.Fatalf("sidebar/local change count did not update after stage; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Unstage tracked.txt")
	h.drain()
	st = h.app.session.Snapshot().Status
	if _, ok := findChange(st.Unstaged, "tracked.txt"); !ok {
		t.Fatalf("tracked.txt was not moved back to unstaged: %+v", st)
	}

	mustClick(t, h, "Stage All")
	h.drain()
	st = h.app.session.Snapshot().Status
	if len(st.Staged) != 2 || len(st.Unstaged) != 0 {
		t.Fatalf("Stage All status = unstaged %d staged %d, want 0 and 2", len(st.Unstaged), len(st.Staged))
	}

	mustClick(t, h, "Unstage All")
	h.drain()
	st = h.app.session.Snapshot().Status
	if len(st.Unstaged) != 2 || len(st.Staged) != 0 {
		t.Fatalf("Unstage All status = unstaged %d staged %d, want 2 and 0", len(st.Unstaged), len(st.Staged))
	}
	if got := strings.TrimSpace(repo.Git("diff", "--name-only")); !strings.Contains(got, "tracked.txt") {
		t.Fatalf("git CLI did not preserve unstaged tracked modification after Unstage All: %q", got)
	}

	// Unstaging a single staged rename must reset both new and original paths so
	// the old path does not remain staged as a deletion.
	repo.Git("mv", "tracked.txt", "renamed.txt")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Unstage renamed.txt")
	h.drain()
	st = h.app.session.Snapshot().Status
	if len(st.Staged) != 0 {
		t.Fatalf("Unstage renamed.txt left staged entries behind: %+v", st.Staged)
	}
}

func TestChangesDiffSelectionLayoutRefreshAndStaleDiscard(t *testing.T) {
	// Clicking real file rows loads unstaged/staged diffs through the shared
	// panel, Split/Unified toggles work, a status mutation refreshes the same
	// file's diff, and a superseded diff worker cannot overwrite a newer file.
	repo := newGitRepo(t, "changes-diff")
	repo.CommitFile("slow.txt", "slow-old\n", "add slow")
	repo.CommitFile("fast.txt", "fast-old\n", "add fast")
	repo.WriteFile("slow.txt", "slow-new-v1\n")
	repo.WriteFile("fast.txt", "fast-new-v1\n")

	h := openChangesHarness(t, repo)

	mustClick(t, h, "Unstaged slow.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "slow-new-v1") {
		t.Fatalf("unstaged slow diff insertions = %q, want slow-new-v1", ins)
	}
	if split, ok := h.ui.AccessByRole(ui.RoleButton, "Split"); !ok || !split.Checked {
		t.Fatalf("Split toggle = %+v found=%v, want checked", split, ok)
	}
	mustClick(t, h, "Unified")
	h.ui.Frame()
	if h.app.changesView.diff.Layout() != editor.DiffUnified {
		t.Fatalf("layout after clicking Unified = %v, want DiffUnified", h.app.changesView.diff.Layout())
	}
	if uni, ok := h.ui.AccessByRole(ui.RoleButton, "Unified"); !ok || !uni.Checked {
		t.Fatalf("Unified toggle = %+v found=%v after click, want checked", uni, ok)
	}
	mustClick(t, h, "Split")
	h.ui.Frame()

	mustClick(t, h, "Stage slow.txt")
	h.drain()
	mustClick(t, h, "Staged slow.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "slow-new-v1") {
		t.Fatalf("staged slow diff insertions = %q, want slow-new-v1", ins)
	}

	// Hold the slow file worker, select fast through the UI, let fast complete,
	// then release slow. The stale result must be discarded. A new mutation of
	// slow.txt advances the status revision and schedules its next diff read.
	repo.WriteFile("slow.txt", "slow-stale\n")
	h.app.session.Reload()
	h.drain()
	h.ui.Frame()
	h.app.session.Reload()
	h.drain()
	h.queue.PauseWorkers()
	mustClick(t, h, "Unstaged slow.txt")
	slowWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()
	mustClick(t, h, "Unstaged fast.txt")
	h.queue.RunNext(t)
	h.ui.Frame()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "fast-new-v1") {
		t.Fatalf("fast diff not visible before slow completes: %q", ins)
	}
	h.queue.ReleaseWorker(slowWorker)
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "fast-new-v1") || containsString(ins, "slow-stale") {
		t.Fatalf("stale slow diff overwrote fast selection: %q", ins)
	}

	// Once discard removes the selected file, the next snapshot prunes the
	// selection and resets the shared diff panel.
	mustClick(t, h, "Discard fast.txt")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("selected discarded file did not reset diff panel; texts=%q", h.ui.Texts())
	}
}

func TestChangesCommitButtonAndShortcutClearDraft(t *testing.T) {
	// A non-empty message plus staged files enables Commit through both the real
	// button and ⌘↩; successful commits create real commits and clear the draft.
	repo := newGitRepo(t, "changes-commit")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.WriteFile("one.txt", "one\n")

	h := openChangesHarness(t, repo)
	if info := changesAccessButton(t, h, "Commit"); !info.Disabled {
		t.Fatalf("Commit should start disabled without a message/staged files: %+v", info)
	}

	mustClick(t, h, "Stage one.txt")
	h.drain()
	typeCommitMessage(t, h, "add one")
	mustClick(t, h, "Commit")
	h.drain()

	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "add one" {
		t.Fatalf("commit subject after button commit = %q, want add one", subject)
	}
	if got := h.app.changesView.message; got != "" {
		t.Fatalf("commit draft after successful button commit = %q, want empty", got)
	}

	repo.WriteFile("two.txt", "two\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage two.txt")
	h.drain()
	typeCommitMessage(t, h, "add two")
	h.ui.Key(ui.Cmd, ui.KeyEnter)
	h.drain()
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "add two" {
		t.Fatalf("commit subject after Cmd+Enter = %q, want add two", subject)
	}
	if got := h.app.changesView.message; got != "" {
		t.Fatalf("commit draft after successful shortcut commit = %q, want empty", got)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected action error after commits: %v", err)
	}
}

func TestChangesDiscardConfirmCancelAndSuccess(t *testing.T) {
	// Discard opens a modal that warns about losing changes and deleting
	// untracked files in correct English for each combination; Cancel leaves
	// every byte untouched, while Confirm Discard revalidates and discards only
	// the selected real files.
	repo := newGitRepo(t, "changes-discard")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Discard tracked.txt")
	h.ui.Frame()
	if !h.ui.HasText("Discard changes?") {
		t.Fatalf("discard modal did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Unstaged changes in 1 file will be permanently lost.") {
		t.Fatalf("tracked-only discard modal copy wrong; texts=%q", h.ui.Texts())
	}
	before := repo.Git("status", "--porcelain")
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if h.ui.HasText("Discard changes?") {
		t.Fatalf("Cancel did not close discard modal; texts=%q", h.ui.Texts())
	}
	if after := repo.Git("status", "--porcelain"); after != before {
		t.Fatalf("Cancel changed working tree: before %q after %q", before, after)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "untracked.txt")); err != nil || string(content) != "new\n" {
		t.Fatalf("Cancel altered untracked file: content=%q err=%v", content, err)
	}

	mustClick(t, h, "Discard tracked.txt")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "tracked.txt")); err != nil || string(content) != "base\n" {
		t.Fatalf("tracked file after discard = %q err=%v, want base content", content, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "untracked.txt")); err != nil {
		t.Fatalf("untracked file should survive tracked-only discard: %v", err)
	}

	mustClick(t, h, "Discard untracked.txt")
	h.ui.Frame()
	if !h.ui.HasText("1 untracked file will be permanently deleted.") {
		t.Fatalf("untracked-only discard modal copy wrong; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if _, err := os.Stat(filepath.Join(repo.Path(), "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file still exists after confirmed discard: err=%v", err)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected discard action error: %v", err)
	}

	// Discard All mixes tracked and untracked targets in one confirmation.
	// Cancel must leave every byte of both kinds untouched, Confirm restores
	// the tracked file and deletes exactly the untracked one.
	repo.WriteFile("tracked.txt", "base\nagain\n")
	repo.WriteFile("another.txt", "more\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Discard All")
	h.ui.Frame()
	if !h.ui.HasText("Unstaged changes in 1 file will be lost, and 1 untracked file will be permanently deleted.") {
		t.Fatalf("mixed discard modal copy wrong; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "tracked.txt")); err != nil || string(content) != "base\nagain\n" {
		t.Fatalf("Cancel altered tracked file: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "another.txt")); err != nil || string(content) != "more\n" {
		t.Fatalf("Cancel altered untracked file: content=%q err=%v", content, err)
	}

	mustClick(t, h, "Discard All")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "tracked.txt")); err != nil || string(content) != "base\n" {
		t.Fatalf("tracked file after Discard All = %q err=%v, want base content", content, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "another.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file still exists after Discard All: err=%v", err)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Discard All action error: %v", err)
	}
}

func TestDiscardConfirmMessage(t *testing.T) {
	// The discard confirmation copy counts untracked files apart from tracked
	// ones, never double-counting either kind, and uses correct English
	// singular and plural for every combination.
	cases := []struct {
		name    string
		targets []git.FileChange
		want    string
	}{
		{"empty", nil, ""},
		{"one tracked",
			[]git.FileChange{{Path: "a.txt", Status: 'M'}},
			"Unstaged changes in 1 file will be permanently lost."},
		{"two tracked",
			[]git.FileChange{{Path: "a.txt", Status: 'M'}, {Path: "b.txt", Status: 'D'}},
			"Unstaged changes in 2 files will be permanently lost."},
		{"one untracked",
			[]git.FileChange{{Path: "n.txt", Status: '?'}},
			"1 untracked file will be permanently deleted."},
		{"two untracked",
			[]git.FileChange{{Path: "n.txt", Status: '?'}, {Path: "m.txt", Status: '?'}},
			"2 untracked files will be permanently deleted."},
		{"one and one",
			[]git.FileChange{{Path: "a.txt", Status: 'M'}, {Path: "n.txt", Status: '?'}},
			"Unstaged changes in 1 file will be lost, and 1 untracked file will be permanently deleted."},
		{"two and three",
			[]git.FileChange{
				{Path: "a.txt", Status: 'M'}, {Path: "b.txt", Status: 'M'},
				{Path: "x.txt", Status: '?'}, {Path: "y.txt", Status: '?'}, {Path: "z.txt", Status: '?'},
			},
			"Unstaged changes in 2 files will be lost, and 3 untracked files will be permanently deleted."},
		{"ignored counts as untracked",
			[]git.FileChange{{Path: "build.log", Status: '!'}},
			"1 untracked file will be permanently deleted."},
		{"ignored mixes with tracked",
			[]git.FileChange{{Path: "a.txt", Status: 'R'}, {Path: "build.log", Status: '!'}},
			"Unstaged changes in 1 file will be lost, and 1 untracked file will be permanently deleted."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := discardConfirmMessage(tc.targets); got != tc.want {
				t.Fatalf("discardConfirmMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChangesDiscardValidationFailureDeletesNothingAndShowsError(t *testing.T) {
	// If the pre-discard status read fails, the guard returns an error before
	// repo.Discard can run: tracked content stays modified and the untracked
	// file is not deleted, with the failure visible in the commit/action area.
	repo := newGitRepo(t, "changes-discard-failure")
	repo.CommitFile("tracked.txt", "base\n", "initial commit")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("untracked.txt", "new\n")

	h := openChangesHarness(t, repo)
	injected := fmt.Errorf("injected status read failure")
	installCommandFailExecutor(t, h, isStatusCommand, injected)

	mustClick(t, h, "Discard All")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "discard validation failed") {
		t.Fatalf("discard validation error = %v, want discard validation failure", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(repo.Path(), "tracked.txt")); readErr != nil || string(content) != "base\nmodified\n" {
		t.Fatalf("tracked file was overwritten despite failed validation: content=%q err=%v", content, readErr)
	}
	if content, readErr := os.ReadFile(filepath.Join(repo.Path(), "untracked.txt")); readErr != nil || string(content) != "new\n" {
		t.Fatalf("untracked file was deleted despite failed validation: content=%q err=%v", content, readErr)
	}
	if !h.ui.HasText("discard validation failed") {
		t.Fatalf("validation failure not visible in Local Changes page; texts=%q", h.ui.Texts())
	}
}

func TestChangesDiscardStaleStatusFailureDeletesNothingAndShowsError(t *testing.T) {
	// A captured untracked discard target that is already tracked/clean at
	// validation time is stale: the operation errors and no file is removed
	// or overwritten.
	repo := newGitRepo(t, "changes-discard-stale")
	repo.CommitFile("base.txt", "base\n", "initial commit")
	repo.WriteFile("candidate.txt", "new\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Discard candidate.txt")
	h.ui.Frame()

	// Make the captured row stale before confirmation: candidate.txt is now
	// tracked and committed rather than an untracked working-tree file.
	repo.Git("add", "--", "candidate.txt")
	repo.Git("commit", "-m", "track candidate")
	mustClick(t, h, "Confirm Discard")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "no longer unstaged") {
		t.Fatalf("stale discard error = %v, want no longer unstaged", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(repo.Path(), "candidate.txt")); readErr != nil || string(content) != "new\n" {
		t.Fatalf("stale discard deleted/overwrote candidate: content=%q err=%v", content, readErr)
	}
	if !h.ui.HasText("no longer unstaged") {
		t.Fatalf("stale validation failure not visible in page; texts=%q", h.ui.Texts())
	}
}

func TestChangesAmendPrefillSuccessAndShortcut(t *testing.T) {
	// Ticking Amend reads HEAD's real message, prefills an empty draft without
	// committing, and ⌘↩ amends the existing commit in place once validated.
	repo := newGitRepo(t, "changes-amend")
	oldHead := repo.CommitFile("base.txt", "base\n", "original subject\n\noriginal body")

	h := openChangesHarness(t, repo)
	repo.WriteFile("base.txt", "base\namended content\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage base.txt")
	h.drain()

	selectAmend(t, h)
	h.drain()
	if got := h.app.changesView.message; got != "original subject\n\noriginal body" {
		t.Fatalf("amend prefill draft = %q, want original full message", got)
	}
	if headOf(t, repo) != oldHead {
		t.Fatal("ticking Amend committed or rewrote HEAD")
	}
	if err := h.app.changesView.amendErr; err != "" {
		t.Fatalf("unexpected amend prefill error: %s", err)
	}

	mustClick(t, h, "Commit message")
	h.ui.Type("\n\namended note")
	h.ui.Frame()
	h.ui.Key(ui.Cmd, ui.KeyEnter)
	h.drain()

	newHead := headOf(t, repo)
	if newHead == oldHead {
		t.Fatal("amend did not create a replacement HEAD commit")
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "original subject" {
		t.Fatalf("amended subject = %q, want original subject", subject)
	}
	if body := repo.Git("log", "-1", "--format=%B"); !strings.Contains(body, "amended note") {
		t.Fatalf("amended body missing typed note: %q", body)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "base.txt")); err != nil || string(content) != "base\namended content\n" {
		t.Fatalf("amend lost staged working-tree content: %q err=%v", content, err)
	}
	if got := h.app.changesView.message; got != "" {
		t.Fatalf("successful amend did not clear draft: %q", got)
	}
}

func TestChangesAmendPrefillReadFailureDoesNotOverwriteOrCommit(t *testing.T) {
	// When reading the last commit message fails, the page shows the error,
	// preserves the user's existing draft, and performs no automatic commit.
	repo := newGitRepo(t, "changes-amend-prefill-failure")
	oldHead := repo.CommitFile("base.txt", "base\n", "original subject")

	h := openChangesHarness(t, repo)
	injected := fmt.Errorf("injected last-message read failure")
	installFailFirstExecutor(t, h, func(args []string) bool {
		return len(args) > 0 && args[0] == "log"
	}, injected)

	typeCommitMessage(t, h, "user draft")
	selectAmend(t, h)
	h.drain()

	if got := h.app.changesView.message; got != "user draft" {
		t.Fatalf("failed amend prefill overwrote draft: %q", got)
	}
	if err := h.app.changesView.amendErr; !strings.Contains(err, "Failed to read last commit message") {
		t.Fatalf("amendErr = %q, want last-message read failure", err)
	}
	if !h.ui.HasText("Failed to read last commit message") {
		t.Fatalf("prefill failure not visible in page; texts=%q", h.ui.Texts())
	}
	if headOf(t, repo) != oldHead {
		t.Fatal("prefill read failure unexpectedly amended HEAD")
	}
	if count := strings.TrimSpace(repo.Git("rev-list", "--count", "HEAD")); count != "1" {
		t.Fatalf("prefill failure created commits: count %q", count)
	}
}

func TestChangesAmendHeadMovedFailureDoesNotOverwriteAndShowsError(t *testing.T) {
	// If HEAD moves after Amend captured expectedHead, the write worker aborts
	// before git commit --amend: the original commit hash, message, and working
	// tree stay intact and the UI reports the validation error.
	repo := newGitRepo(t, "changes-amend-head-moved")
	oldHead := repo.CommitFile("base.txt", "base\n", "original subject")

	h := openChangesHarness(t, repo)
	repo.WriteFile("base.txt", "base\nuser edit\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage base.txt")
	h.drain()
	selectAmend(t, h)
	h.drain()

	// Move HEAD after the checkbox captured expectedHead but before committing.
	repo.Git("commit", "--amend", "-m", "externally moved subject")
	movedHead := headOf(t, repo)
	if movedHead == oldHead {
		t.Fatal("test setup failed to move HEAD")
	}
	typeCommitMessage(t, h, "replacement subject")
	mustClick(t, h, "Commit")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "HEAD moved") {
		t.Fatalf("amend validation error = %v, want HEAD moved", err)
	}
	if got := headOf(t, repo); got != movedHead {
		t.Fatalf("HEAD after rejected amend = %s, want externally moved %s", got, movedHead)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "externally moved subject" {
		t.Fatalf("original commit message overwritten: %q", subject)
	}
	if content, readErr := os.ReadFile(filepath.Join(repo.Path(), "base.txt")); readErr != nil || string(content) != "base\nuser edit\n" {
		t.Fatalf("working tree overwritten by rejected amend: %q err=%v", content, readErr)
	}
	if !h.ui.HasText("HEAD moved") {
		t.Fatalf("HEAD-moved validation failure not visible in page; texts=%q", h.ui.Texts())
	}
}

func TestChangesAmendHeadReadFailureDoesNotOverwriteAndShowsError(t *testing.T) {
	// If validating HEAD itself fails, the guard returns an error before amend:
	// the existing commit and working-tree content are not overwritten.
	repo := newGitRepo(t, "changes-amend-read-failure")
	oldHead := repo.CommitFile("base.txt", "base\n", "original subject")

	h := openChangesHarness(t, repo)
	repo.WriteFile("base.txt", "base\nuser edit\n")
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage base.txt")
	h.drain()
	selectAmend(t, h)
	h.drain()

	injected := fmt.Errorf("injected HEAD validation failure")
	installFailFirstExecutor(t, h, isRevParseHEAD, injected)
	typeCommitMessage(t, h, "replacement subject")
	mustClick(t, h, "Commit")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "failed to read HEAD") {
		t.Fatalf("amend read error = %v, want failed to read HEAD", err)
	}
	if got := headOf(t, repo); got != oldHead {
		t.Fatalf("HEAD changed despite validation read failure: got %s want %s", got, oldHead)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s")); subject != "original subject" {
		t.Fatalf("commit overwritten despite validation read failure: %q", subject)
	}
	if content, readErr := os.ReadFile(filepath.Join(repo.Path(), "base.txt")); readErr != nil || string(content) != "base\nuser edit\n" {
		t.Fatalf("working tree overwritten despite validation read failure: %q err=%v", content, readErr)
	}
	if !h.ui.HasText("failed to read HEAD") {
		t.Fatalf("HEAD read failure not visible in page; texts=%q", h.ui.Texts())
	}
}

func TestChangesSwitchingRepositoriesClearsSelectionDraftAndDiff(t *testing.T) {
	// Switching repositories through the Repository Switcher resets the changes
	// selection, discard state, commit draft, amend state, and shared diff panel.
	repoA := newGitRepo(t, "changes-repo-a")
	repoA.CommitFile("a.txt", "a-old\n", "a initial")
	repoA.WriteFile("a.txt", "a-new\n")
	repoB := newGitRepo(t, "changes-repo-b")
	repoB.CommitFile("b.txt", "b\n", "b initial")

	h := newTestApp(t)
	savedA := h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}

	mustClick(t, h, "Unstaged a.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "a-new") {
		t.Fatalf("repo A diff missing before switch: %q", ins)
	}
	typeCommitMessage(t, h, "draft before switch")
	selectAmend(t, h)
	h.drain()

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "changes-repo-b")
	h.drain()

	if h.app.session.Meta().ID == savedA.ID {
		t.Fatal("repository switcher did not leave repo A")
	}
	if h.app.changesView.selected || h.app.changesView.selectedPath != "" {
		t.Fatalf("selection survived repo switch: %+v", h.app.changesView)
	}
	if got := h.app.changesView.message; got != "" {
		t.Fatalf("draft survived repo switch: %q", got)
	}
	if h.app.changesView.amend || h.app.changesView.amendHead != "" {
		t.Fatalf("amend state survived repo switch: amend=%v head=%q", h.app.changesView.amend, h.app.changesView.amendHead)
	}
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("diff panel did not reset after repo switch; texts=%q", h.ui.Texts())
	}
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); containsString(ins, "a-new") {
		t.Fatalf("repo A diff leaked after repo switch: %q", ins)
	}
}

func TestChangesWriteOperationsDisableControlsAndRejectReentry(t *testing.T) {
	// While a session write is busy, row and bulk write buttons plus the commit
	// button are disabled through their real accessibility state, and clicking a
	// row cannot replace the current diff selection.
	repo := newGitRepo(t, "changes-busy")
	repo.CommitFile("tracked.txt", "base\n", "initial")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("other.txt", "other\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged tracked.txt")
	h.drain()

	h.queue.PauseWorkers()
	mustClick(t, h, "Stage All")
	worker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		h.queue.ReleaseWorker(worker)
		h.drain()
		t.Fatal("session did not enter busy state for paused Stage All")
	}
	for _, label := range []string{"Stage All", "Unstage All", "Discard All", "Commit"} {
		if info, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok || !info.Disabled {
			t.Fatalf("busy control %q = %+v found=%v, want disabled", label, info, ok)
		}
	}
	mustClick(t, h, "Unstaged other.txt")
	h.ui.Frame()
	if h.app.changesView.selectedPath != "tracked.txt" {
		t.Fatalf("busy row click changed selection to %q", h.app.changesView.selectedPath)
	}
	h.queue.ReleaseWorker(worker)
	h.queue.ResumeWorkers()
	h.drain()
	if h.app.session.Busy() {
		t.Fatal("session still busy after releasing write worker")
	}
}

func TestChangesLoopbackSSHStageDiffCommitAmendAndDiscard(t *testing.T) {
	// Loopback SSH: the same real UI path stages/unstages files, renders diffs,
	// commits, amends, and discards in a repository reached through sshd.
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH initial load failed: %v", err)
	}

	srv.WriteFile(t, repo.Path, "ssh-tracked.txt", []byte("ssh-base\nssh-modified\n"))
	srv.WriteFile(t, repo.Path, "ssh-untracked.txt", []byte("ssh-new\n"))
	h.app.session.Reload()
	h.drain()

	mustClick(t, h, "Unstaged ssh-tracked.txt")
	h.drain()
	if ins := diffAccessLines(h.ui, ui.RoleInsertion); !containsString(ins, "ssh-modified") {
		t.Fatalf("SSH unstaged diff insertions = %q, want ssh-modified", ins)
	}

	mustClick(t, h, "Stage ssh-tracked.txt")
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Staged ssh-tracked.txt"); !ok {
		t.Fatalf("SSH file did not move to staged list; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Unstage ssh-tracked.txt")
	h.drain()
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstaged ssh-tracked.txt"); !ok {
		t.Fatalf("SSH file did not move back to unstaged list; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Stage All")
	h.drain()
	typeCommitMessage(t, h, "ssh commit")
	mustClick(t, h, "Commit")
	h.drain()
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s")); subject != "ssh commit" {
		t.Fatalf("SSH commit subject = %q, want ssh commit", subject)
	}

	oldHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	srv.WriteFile(t, repo.Path, "ssh-tracked.txt", []byte("ssh-base\nssh-modified\nssh-amend\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Stage ssh-tracked.txt")
	h.drain()
	selectAmend(t, h)
	h.drain()
	mustClick(t, h, "Commit message")
	h.ui.Type("\n\nssh amend note")
	h.ui.Key(ui.Cmd, ui.KeyEnter)
	h.drain()
	newHead := strings.TrimSpace(srv.RunGit(t, repo.Path, "rev-parse", "HEAD"))
	if newHead == oldHead {
		t.Fatal("SSH amend did not replace HEAD")
	}
	if body := srv.RunGit(t, repo.Path, "log", "-1", "--format=%B"); !strings.Contains(body, "ssh amend note") {
		t.Fatalf("SSH amended message missing note: %q", body)
	}

	srv.WriteFile(t, repo.Path, "ssh-untracked.txt", []byte("discard me\n"))
	h.app.session.Reload()
	h.drain()
	mustClick(t, h, "Discard ssh-untracked.txt")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if out := srv.RunGit(t, repo.Path, "status", "--porcelain", "--", "ssh-untracked.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("SSH discard left untracked file in status: %q", out)
	}
}

func TestChangesLoopbackSSHDiscardValidationFailureDeletesNothing(t *testing.T) {
	// Loopback SSH failure protection: when the validating status read fails,
	// the SSH discard returns an error and neither overwrites tracked content
	// nor deletes the untracked file on the remote working tree.
	repo, srv := loopbackSSHRepo(t)
	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()

	srv.WriteFile(t, repo.Path, "ssh-tracked.txt", []byte("base\nmodified\n"))
	srv.WriteFile(t, repo.Path, "ssh-untracked.txt", []byte("new\n"))
	h.app.session.Reload()
	h.drain()

	injected := fmt.Errorf("injected SSH status read failure")
	installCommandFailExecutor(t, h, isStatusCommand, injected)
	mustClick(t, h, "Discard All")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil || !strings.Contains(err.Error(), "discard validation failed") {
		t.Fatalf("SSH discard validation error = %v, want validation failure", err)
	}
	if got := string(srv.ReadFile(t, repo.Path, "ssh-tracked.txt")); got != "base\nmodified\n" {
		t.Fatalf("SSH tracked file overwritten despite validation failure: %q", got)
	}
	if got := string(srv.ReadFile(t, repo.Path, "ssh-untracked.txt")); got != "new\n" {
		t.Fatalf("SSH untracked file deleted despite validation failure: %q", got)
	}
	if !h.ui.HasText("discard validation failed") {
		t.Fatalf("SSH discard validation failure not visible in UI; texts=%q", h.ui.Texts())
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
