package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// typeChangesFilter focuses the filter box of Local Changes and types text
// into it, replacing whatever was typed before, as a user does.
func typeChangesFilter(t *testing.T, h *testHarness, text string) {
	t.Helper()
	mustClick(t, h, "Filter Changes")
	h.ui.Command("selectAll")
	h.ui.Type(text)
	h.ui.Frame()
	if got := h.app.changesView.filter; got != text {
		t.Fatalf("typed filter = %q, want %q", got, text)
	}
}

// changeRowShown reports whether the accessible row of one file of one
// section is built in the list. Ordinary rows are buttons; conflicted
// rows carry the list-item role.
func changeRowShown(h *testHarness, label string) bool {
	if _, ok := h.ui.AccessByRole(ui.RoleButton, label); ok {
		return true
	}
	_, ok := h.ui.AccessByRole(ui.RoleListItem, label)
	return ok
}

func TestChangesFilterMatchesCaseOrigPathAndSpacing(t *testing.T) {
	// The filter matches a case-insensitive substring of the path of every
	// section, and of the source path of a rename; surrounding space does
	// not count.
	repo := newGitRepo(t, "changesfilter-match")
	repo.CommitFile("oldname.txt", "base\n", "initial")
	repo.Git("mv", "oldname.txt", "newplace.txt")
	repo.WriteFile("alpha-notes.txt", "unstaged\n")
	repo.WriteFile("beta.txt", "unstaged\n")

	h := openChangesHarness(t, repo)

	if !h.ui.HasText("Unstaged (2)") || !h.ui.HasText("Staged (1)") {
		t.Fatalf("unfiltered section counts missing; texts=%q", h.ui.Texts())
	}
	field, ok := h.ui.AccessByRole(ui.RoleTextField, "Filter Changes")
	if !ok {
		t.Fatalf("filter input missing; texts=%q", h.ui.Texts())
	}
	if field.Placeholder != "Filter" {
		t.Fatalf("filter placeholder = %q, want %q", field.Placeholder, "Filter")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Clear Filter"); ok {
		t.Fatal("the Clear Filter button must not show while the box is empty")
	}

	// Uppercase with surrounding space matches lowercase paths: the
	// comparison folds case and trims the ends.
	typeChangesFilter(t, h, " ALPHA ")
	for _, text := range []string{"Unstaged 1 of 2", "Staged 0 of 1"} {
		if !h.ui.HasText(text) {
			t.Fatalf("filtered count %q missing; texts=%q", text, h.ui.Texts())
		}
	}
	if !changeRowShown(h, "Unstaged alpha-notes.txt") {
		t.Fatalf("alpha-notes.txt row missing under the filter; texts=%q", h.ui.Texts())
	}
	if changeRowShown(h, "Unstaged beta.txt") {
		t.Fatal("beta.txt must be hidden by the filter")
	}
	if changeRowShown(h, "Staged newplace.txt") {
		t.Fatal("newplace.txt must be hidden by a filter matching nothing of it")
	}
	if !h.ui.HasText("No staged changes") {
		t.Fatalf("the emptied staged section keeps its empty hint; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Clear Filter"); !ok {
		t.Fatalf("Clear Filter missing while the box holds text; texts=%q", h.ui.Texts())
	}

	// A rename answers to the path it was renamed from, not only to where
	// it is now.
	typeChangesFilter(t, h, "oldname")
	if !h.ui.HasText("Staged 1 of 1") {
		t.Fatalf("rename matched by its original path does not show; texts=%q", h.ui.Texts())
	}
	if !changeRowShown(h, "Staged newplace.txt") {
		t.Fatalf("newplace.txt row missing when filtering its original path; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Unstaged 0 of 2") || !h.ui.HasText("No unstaged changes") {
		t.Fatalf("the emptied unstaged section keeps its hint; texts=%q", h.ui.Texts())
	}

	// Nothing matching anywhere leaves both sections with title and hint.
	typeChangesFilter(t, h, "zzz-no-such-file")
	if !h.ui.HasText("Unstaged 0 of 2") || !h.ui.HasText("Staged 0 of 1") {
		t.Fatalf("fully filtered counts missing; texts=%q", h.ui.Texts())
	}
	if changeRowShown(h, "Unstaged alpha-notes.txt") || changeRowShown(h, "Staged newplace.txt") {
		t.Fatal("a file row survived a filter matching nothing")
	}

	// Clearing restores the unfiltered list, its counts and its buttons.
	mustClick(t, h, "Clear Filter")
	h.ui.Frame()
	if got := h.app.changesView.filter; got != "" {
		t.Fatalf("filter after Clear = %q, want empty", got)
	}
	for _, text := range []string{"Unstaged (2)", "Staged (1)"} {
		if !h.ui.HasText(text) {
			t.Fatalf("unfiltered count %q missing after clearing; texts=%q", text, h.ui.Texts())
		}
	}
	for _, label := range []string{"Unstaged alpha-notes.txt", "Unstaged beta.txt", "Staged newplace.txt"} {
		if !changeRowShown(h, label) {
			t.Fatalf("row %q missing after clearing the filter; texts=%q", label, h.ui.Texts())
		}
	}
	for _, label := range []string{"Stage All", "Unstage All", "Discard All"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("bulk button %q missing after clearing; texts=%q", label, h.ui.Texts())
		}
	}
	for _, label := range []string{"Stage Shown", "Unstage Shown", "Discard Shown"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); ok {
			t.Fatalf("filtered bulk button %q survived clearing the filter", label)
		}
	}
}

func TestChangesFilterConflictedSectionFilters(t *testing.T) {
	// The Conflicted section filters like the other two: rows not matching
	// leave the list, and the count reads shown of total.
	repo := newGitRepo(t, "changesfilter-conflict")
	mergeConflictingFiles(t, repo, 2)
	repo.WriteFile("keep.txt", "unstaged\n")

	h := openChangesHarness(t, repo)
	if !h.ui.HasText("Conflicted (2)") {
		t.Fatalf("unfiltered conflict count missing; texts=%q", h.ui.Texts())
	}

	typeChangesFilter(t, h, "c-0000")
	if !h.ui.HasText("Conflicted 1 of 2") {
		t.Fatalf("filtered conflict count missing; texts=%q", h.ui.Texts())
	}
	if !changeRowShown(h, "Conflicted c-0000.txt") {
		t.Fatalf("c-0000.txt conflict row missing under the filter; texts=%q", h.ui.Texts())
	}
	if changeRowShown(h, "Conflicted c-0001.txt") {
		t.Fatal("c-0001.txt must be hidden by the filter")
	}

	mustClick(t, h, "Clear Filter")
	h.ui.Frame()
	if !h.ui.HasText("Conflicted (2)") {
		t.Fatalf("unfiltered conflict count missing after clearing; texts=%q", h.ui.Texts())
	}
}

func TestChangesFilterStageAndUnstageShown(t *testing.T) {
	// Under a filter the bulk buttons act on the shown files alone: Stage
	// Shown moves the matching unstaged files into the index and leaves the
	// hidden ones out of it, Unstage Shown the reverse.
	repo := newGitRepo(t, "changesfilter-stage")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("match-a.txt", "a\n")
	repo.WriteFile("match-b.txt", "b\n")
	repo.WriteFile("hidden.txt", "hidden\n")
	repo.WriteFile("staged-other.txt", "staged\n")
	repo.Git("add", "--", "staged-other.txt")

	h := openChangesHarness(t, repo)
	typeChangesFilter(t, h, "match")

	if !h.ui.HasText("Unstaged 2 of 3") || !h.ui.HasText("Staged 0 of 1") {
		t.Fatalf("filtered counts missing; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage All"); ok {
		t.Fatal("Stage All must be renamed while the filter is active")
	}

	mustClick(t, h, "Stage Shown")
	h.drain()

	for _, path := range []string{"match-a.txt", "match-b.txt"} {
		if out := repo.Git("ls-files", "--", path); strings.TrimSpace(out) == "" {
			t.Fatalf("%s is not in the index after Stage Shown; ls-files=%q", path, out)
		}
	}
	if out := repo.Git("ls-files", "--", "hidden.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("hidden.txt was staged although the filter hides it; ls-files=%q", out)
	}
	if porcelain := repo.Git("status", "--porcelain", "--", "hidden.txt"); !strings.HasPrefix(porcelain, "??") {
		t.Fatalf("hidden.txt porcelain = %q, want an untracked file", porcelain)
	}
	if !h.ui.HasText("Staged 2 of 3") {
		t.Fatalf("staged count after Stage Shown missing; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Unstaged 0 of 1") {
		t.Fatalf("unstaged count after Stage Shown missing; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Unstage Shown")
	h.drain()

	for _, path := range []string{"match-a.txt", "match-b.txt"} {
		if out := repo.Git("ls-files", "--", path); strings.TrimSpace(out) != "" {
			t.Fatalf("%s is still in the index after Unstage Shown; ls-files=%q", path, out)
		}
	}
	if out := repo.Git("ls-files", "--", "staged-other.txt"); strings.TrimSpace(out) == "" {
		t.Fatalf("staged-other.txt lost its index entry although the filter hides it; ls-files=%q", out)
	}
	if !h.ui.HasText("Unstaged 2 of 3") {
		t.Fatalf("unstaged count after Unstage Shown missing; texts=%q", h.ui.Texts())
	}
}

func TestChangesFilterDiscardShown(t *testing.T) {
	// Discard Shown confirms exactly the files the filter keeps in view:
	// the confirmation speaks of those files, and confirming deletes them
	// while the hidden ones stay on disk.
	repo := newGitRepo(t, "changesfilter-discard")
	repo.CommitFile("base.txt", "base\n", "initial")
	repo.WriteFile("match-x.txt", "x\n")
	repo.WriteFile("match-y.txt", "y\n")
	repo.WriteFile("keep.txt", "keep\n")

	h := openChangesHarness(t, repo)
	typeChangesFilter(t, h, "match")

	mustClick(t, h, "Discard Shown")
	h.ui.Frame()
	if !h.ui.HasText("Discard changes?") {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("2 untracked files will be permanently deleted.") {
		t.Fatalf("discard confirmation does not count the shown files; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Confirm Discard")
	h.drain()

	for _, path := range []string{"match-x.txt", "match-y.txt"} {
		if _, err := os.Stat(filepath.Join(repo.Path(), path)); !os.IsNotExist(err) {
			t.Fatalf("%s survived Discard Shown: err=%v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "keep.txt")); err != nil {
		t.Fatalf("keep.txt was discarded although the filter hides it: %v", err)
	}
}

func TestChangesFilterStageShownSkipsConflicted(t *testing.T) {
	// With a merge in progress, Stage Shown moves the ordinary files the
	// filter shows and never touches the unmerged ones.
	repo := newGitRepo(t, "changesfilter-conflictstage")
	mergeConflictingFiles(t, repo, 1)
	repo.WriteFile("c-ordinary.txt", "ordinary\n")

	h := openChangesHarness(t, repo)
	typeChangesFilter(t, h, "c-")

	if !h.ui.HasText("Conflicted 1 of 1") || !h.ui.HasText("Unstaged 1 of 1") {
		t.Fatalf("filtered counts missing; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Stage Shown")
	h.drain()

	if out := repo.Git("ls-files", "--", "c-ordinary.txt"); strings.TrimSpace(out) == "" {
		t.Fatalf("c-ordinary.txt is not in the index after Stage Shown; ls-files=%q", out)
	}
	unmerged := repo.Git("ls-files", "-u")
	if !strings.Contains(unmerged, "c-0000.txt") {
		t.Fatalf("the conflicted file lost its unmerged index entries; ls-files -u=%q", unmerged)
	}
	if porcelain := repo.Git("status", "--porcelain", "--", "c-0000.txt"); !strings.HasPrefix(porcelain, "UU") {
		t.Fatalf("c-0000.txt porcelain = %q, want an unmerged file", porcelain)
	}
}

func TestChangesFilterClearsHiddenSelection(t *testing.T) {
	// A file the filter hides cannot stay selected: the selection clears
	// and the diff panel returns to its unselected placeholder.
	repo := newGitRepo(t, "changesfilter-selection")
	repo.CommitFile("tracked.txt", "base\n", "initial")
	repo.WriteFile("tracked.txt", "base\nmodified\n")
	repo.WriteFile("other.txt", "other\n")

	h := openChangesHarness(t, repo)
	mustClick(t, h, "Unstaged tracked.txt")
	h.drain()
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "tracked.txt" {
		t.Fatalf("clicking the row did not select it: %+v", h.app.changesView)
	}
	if d := &h.app.changesView.diff; d.state == nil {
		t.Fatalf("diff of tracked.txt did not load: err=%v", d.err)
	}

	typeChangesFilter(t, h, "other")
	h.ui.Frame()

	if h.app.changesView.selected || h.app.changesView.selectedPath != "" {
		t.Fatalf("selection survived the filter hiding its file: %+v", h.app.changesView)
	}
	if !h.ui.HasText("No File Selected") {
		t.Fatalf("diff panel placeholder missing after the selection cleared; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Clear Filter")
	h.ui.Frame()
	if !changeRowShown(h, "Unstaged tracked.txt") {
		t.Fatalf("tracked.txt row missing after clearing; texts=%q", h.ui.Texts())
	}
	if h.app.changesView.selected {
		t.Fatal("clearing the filter must not bring back the dropped selection")
	}
}

func TestChangesFilterSurvivesReloadAndClearsOnRepoSwitch(t *testing.T) {
	// The filter text belongs to the repository: reloading, staging and
	// unstaging keep it, while switching to another repository starts with
	// an empty box again.
	repoA := newGitRepo(t, "changesfilter-stay-a")
	repoA.CommitFile("base.txt", "base\n", "initial")
	repoA.WriteFile("alpha-1.txt", "a\n")
	repoA.WriteFile("beta-1.txt", "b\n")
	repoB := newGitRepo(t, "changesfilter-switch-b")
	repoB.CommitFile("base.txt", "base\n", "initial")
	for i := 0; i < 3; i++ {
		repoB.WriteFile(fmt.Sprintf("gamma-%d.txt", i), "g\n")
	}

	h := newTestApp(t)
	h.openRepo(repoA.StoreRepo())
	h.drain()
	if _, err := h.store.Add(repoB.StoreRepo()); err != nil {
		t.Fatalf("add repo B: %v", err)
	}

	typeChangesFilter(t, h, "alpha")
	if !h.ui.HasText("Unstaged 1 of 2") {
		t.Fatalf("filtered count missing before reload; texts=%q", h.ui.Texts())
	}

	h.app.session.Reload()
	h.drain()
	if got := h.app.changesView.filter; got != "alpha" {
		t.Fatalf("filter after a reload = %q, want %q", got, "alpha")
	}
	if !h.ui.HasText("Unstaged 1 of 2") {
		t.Fatalf("the filter stopped applying after a reload; texts=%q", h.ui.Texts())
	}

	repoA.WriteFile("alpha-2.txt", "a2\n")
	h.app.session.Reload()
	h.drain()
	if !h.ui.HasText("Unstaged 2 of 3") {
		t.Fatalf("the filter did not keep up with a changed snapshot; texts=%q", h.ui.Texts())
	}

	mustClick(t, h, "Repository Switcher")
	h.ui.Frame()
	mustClick(t, h, "changesfilter-switch-b")
	h.drain()

	if got := h.app.changesView.filter; got != "" {
		t.Fatalf("filter after switching repositories = %q, want empty", got)
	}
	if !h.ui.HasText("Unstaged (3)") {
		t.Fatalf("unfiltered count missing in the other repository; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Stage Shown"); ok {
		t.Fatal("filtered bulk buttons survived the repository switch")
	}
}
