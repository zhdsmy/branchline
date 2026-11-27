package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// treeActionsRepo builds a repository whose working tree exercises the
// folder actions of the tree: a folder with nested files, a sibling folder
// whose name extends it as a string without sharing its path, a merged
// single-child chain, files at the root and a staged folder of its own.
func treeActionsRepo(t *testing.T, name string) *gitRepoFixture {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("app/one.txt", "one\n", "app one")
	repo.CommitFile("app/nested/two.txt", "two\n", "app two")
	repo.CommitFile("application/sibling.txt", "sibling\n", "sibling of app")
	repo.CommitFile("chain/a/b/c/x.txt", "x\n", "chain file")
	repo.CommitFile("other/outside.txt", "outside\n", "outside")
	repo.WriteFile("app/one.txt", "one\nedited\n")
	repo.WriteFile("app/nested/two.txt", "two\nedited\n")
	repo.WriteFile("application/sibling.txt", "sibling\nedited\n")
	repo.WriteFile("chain/a/b/c/x.txt", "x\nedited\n")
	repo.WriteFile("other/outside.txt", "outside\nedited\n")
	repo.WriteFile("staged-app/put.txt", "put\n")
	repo.Git("add", "--", "staged-app/put.txt")
	return repo
}

// openTreeActionsHarness opens repo in tree mode, the view the folder rows
// and their buttons live in.
func openTreeActionsHarness(t *testing.T, repo *gitRepoFixture) *testHarness {
	t.Helper()
	h := openChangesHarness(t, repo)
	mustClick(t, h, "Show as Tree")
	h.ui.Frame()
	return h
}

// porcelainLines returns the status lines of the repository one per change,
// so assertions compare exactly what the index and the working tree hold.
func porcelainLines(repo *gitRepoFixture, args ...string) string {
	full := append([]string{"status", "--porcelain"}, args...)
	return repo.Git(full...)
}

func TestChangesTreeActionsStageAndUnstageFolderScope(t *testing.T) {
	// Stage Folder moves exactly the files under the folder at every depth
	// into the index — the nested file included — while the sibling folder
	// application, which merely shares the string prefix, and every other
	// folder stay unstaged. Unstage Folder of the staged section moves the
	// same files back.
	repo := treeActionsRepo(t, "treactions-scope")
	h := openTreeActionsHarness(t, repo)

	// Every folder row of the unstaged section carries Discard and Stage,
	// the rows of the staged section Unstage.
	for _, label := range []string{"Discard Folder app", "Stage Folder app", "Discard Folder application", "Discard Folder other"} {
		if _, ok := h.ui.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("button %q missing from the unstaged folder rows; texts=%q", label, h.ui.Texts())
		}
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstage Folder staged-app"); !ok {
		t.Fatalf("the staged folder row carries no Unstage; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstage Folder app"); ok {
		t.Fatal("an unstaged folder row offers Unstage")
	}

	mustClick(t, h, "Stage Folder app")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Stage Folder error: %v", err)
	}
	staged := porcelainLines(repo)
	for _, want := range []string{"M  app/one.txt", "M  app/nested/two.txt"} {
		if !strings.Contains(staged, want) {
			t.Fatalf("%q missing after Stage Folder app; status=%q", want, staged)
		}
	}
	for _, want := range []string{" M application/sibling.txt", " M chain/a/b/c/x.txt", " M other/outside.txt"} {
		if !strings.Contains(staged, want) {
			t.Fatalf("%q missing or moved after Stage Folder app; status=%q", want, staged)
		}
	}

	// The staged copies reappear under a staged folder row, whose Unstage
	// moves exactly them back out of the index.
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Unstage Folder app"); !ok {
		t.Fatalf("the staged copies of app did not reappear under a folder row with Unstage; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Unstage Folder app")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Unstage Folder error: %v", err)
	}
	back := porcelainLines(repo)
	for _, want := range []string{" M app/one.txt", " M app/nested/two.txt"} {
		if !strings.Contains(back, want) {
			t.Fatalf("%q missing after Unstage Folder app; status=%q", want, back)
		}
	}
	if strings.Contains(back, "M  app/") {
		t.Fatalf("a file under app stayed staged after Unstage Folder; status=%q", back)
	}
	if !strings.Contains(back, "A  staged-app/put.txt") {
		t.Fatalf("staged-app/put.txt lost its index entry although it is outside app; status=%q", back)
	}
}

func TestChangesTreeActionsMergedChainFolder(t *testing.T) {
	// The folder row of a merged single-child chain carries the joined path
	// chain/a/b/c, and its Stage acts on the files under exactly that path.
	repo := treeActionsRepo(t, "treactions-chain")
	h := openTreeActionsHarness(t, repo)

	if _, ok := treeFolderShown(h, "Unstaged Folder chain/a/b/c, 1 file"); !ok {
		t.Fatalf("merged chain folder row missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Stage Folder chain/a/b/c")
	h.drain()
	staged := porcelainLines(repo)
	if !strings.Contains(staged, "M  chain/a/b/c/x.txt") {
		t.Fatalf("chain file not staged by the merged folder row; status=%q", staged)
	}
	if !strings.Contains(staged, " M app/one.txt") {
		t.Fatalf("app/one.txt moved although it is outside the chain; status=%q", staged)
	}
}

func TestChangesTreeActionsFilterLimitsScope(t *testing.T) {
	// Under a filter the folder actions act on the matching files alone:
	// staging the folder leaves the filtered-out file in the working tree.
	repo := treeActionsRepo(t, "treactions-filter")
	repo.WriteFile("app/keep-me.txt", "keep\n")
	repo.WriteFile("app/skip-me.txt", "skip\n")
	h := openTreeActionsHarness(t, repo)

	typeChangesFilter(t, h, "keep")
	mustClick(t, h, "Stage Folder app")
	h.drain()
	staged := porcelainLines(repo)
	if !strings.Contains(staged, "A  app/keep-me.txt") {
		t.Fatalf("the matching file is not staged; status=%q", staged)
	}
	if !strings.Contains(staged, "?? app/skip-me.txt") {
		t.Fatalf("the filtered-out file was staged although it is not shown; status=%q", staged)
	}
	if strings.Contains(staged, "A  app/skip-me.txt") {
		t.Fatalf("the filtered-out file reached the index; status=%q", staged)
	}
	for _, want := range []string{" M app/one.txt", " M app/nested/two.txt"} {
		if !strings.Contains(staged, want) {
			t.Fatalf("%q moved under the filter although it is not shown; status=%q", want, staged)
		}
	}
}

func TestChangesTreeActionsStageFolderSkipsConflicts(t *testing.T) {
	// A conflicted file inside the folder never enters the index through
	// Stage Folder: the conflicted section is not part of the folder's
	// files, and staging one would silently mark the conflict resolved.
	repo := newGitRepo(t, "treactions-conflict")
	repo.CommitFile("box/left.txt", "base\n", "base")
	repo.Git("checkout", "-q", "-b", "side")
	repo.CommitFile("box/left.txt", "side\n", "side")
	repo.Git("checkout", "-q", "main")
	repo.CommitFile("box/left.txt", "main\n", "main")
	if out, err := tryGitCommand(repo.Path(), "merge", "--no-edit", "side"); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", out)
	}
	repo.WriteFile("box/plain.txt", "plain\n")

	h := openTreeActionsHarness(t, repo)
	if !changeRowShown(h, "Conflicted box/left.txt") {
		t.Fatalf("conflicted row missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Stage Folder box")
	h.drain()
	staged := porcelainLines(repo)
	if !strings.Contains(staged, "A  box/plain.txt") {
		t.Fatalf("the ordinary file of the folder is not staged; status=%q", staged)
	}
	unmerged := repo.Git("ls-files", "-u", "--", "box/left.txt")
	if strings.TrimSpace(unmerged) == "" {
		t.Fatalf("box/left.txt lost its unmerged entries — Stage Folder resolved the conflict; status=%q", staged)
	}
	if !strings.Contains(staged, "UU box/left.txt") {
		t.Fatalf("box/left.txt no longer reads as a conflict; status=%q", staged)
	}
}

func TestChangesTreeActionsUnstageFolderRenamesRestoreOriginalPath(t *testing.T) {
	// A staged rename inside the folder unstages with its source path, as
	// the file row and the section's bulk Unstage do: afterwards the old
	// path reads as a working-tree deletion and the new path as untracked,
	// while a staged file outside the folder keeps its index entry.
	repo := newGitRepo(t, "treactions-rename")
	repo.CommitFile("app/old.txt", "old\n", "to rename")
	repo.Git("mv", "app/old.txt", "app/new.txt")
	repo.WriteFile("keep.txt", "keep\n")
	repo.Git("add", "--", "keep.txt")

	h := openTreeActionsHarness(t, repo)
	if !changeRowShown(h, "Staged app/new.txt") {
		t.Fatalf("staged rename row missing; texts=%q", h.ui.Texts())
	}
	mustClick(t, h, "Unstage Folder app")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Unstage Folder error: %v", err)
	}
	staged := porcelainLines(repo)
	if !strings.Contains(staged, " D app/old.txt") {
		t.Fatalf("the rename source did not return to the working tree as a deletion; status=%q", staged)
	}
	if !strings.Contains(staged, "?? app/new.txt") {
		t.Fatalf("the renamed file does not read as untracked after the unstage; status=%q", staged)
	}
	if strings.Contains(staged, "R  ") || strings.Contains(staged, "D  app/old.txt") {
		t.Fatalf("the rename is still staged after Unstage Folder; status=%q", staged)
	}
	if !strings.Contains(staged, "A  keep.txt") {
		t.Fatalf("keep.txt lost its index entry although it is outside the folder; status=%q", staged)
	}
}

func TestChangesTreeActionsDiscardFolderConfirmAndCancel(t *testing.T) {
	// Discard Folder opens the shared confirmation naming exactly the files
	// under the folder: Cancel and a bare Enter leave every byte untouched,
	// Confirm Discard reverts the tracked files and deletes the untracked
	// ones of the folder alone.
	repo := treeActionsRepo(t, "treactions-discard")
	repo.WriteFile("app/fresh.txt", "fresh\n")
	repo.WriteFile("other/fresh.txt", "fresh\n")
	h := openTreeActionsHarness(t, repo)

	mustClick(t, h, "Discard Folder app")
	h.ui.Frame()
	if !h.ui.HasText("Discard changes?") {
		t.Fatalf("discard confirmation did not open; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Unstaged changes in 2 files will be lost, and 1 untracked file will be permanently deleted.") {
		t.Fatalf("folder discard copy wrong; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm Discard"); !ok {
		t.Fatalf("destructive confirm button missing; texts=%q", h.ui.Texts())
	}

	before := porcelainLines(repo)
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if after := porcelainLines(repo); after != before {
		t.Fatalf("a bare Enter discarded the folder: before %q after %q", before, after)
	}
	if h.ui.HasText("Discard changes?") {
		mustClick(t, h, "Cancel")
		h.ui.Frame()
		if h.ui.HasText("Discard changes?") {
			t.Fatalf("Cancel did not close the confirmation; texts=%q", h.ui.Texts())
		}
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "app/fresh.txt")); err != nil || string(content) != "fresh\n" {
		t.Fatalf("Cancel altered app/fresh.txt: content=%q err=%v", content, err)
	}

	mustClick(t, h, "Discard Folder app")
	h.ui.Frame()
	mustClick(t, h, "Confirm Discard")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Discard Folder error: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "app/one.txt")); err != nil || string(content) != "one\n" {
		t.Fatalf("app/one.txt not reverted after Discard Folder: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "app/nested/two.txt")); err != nil || string(content) != "two\n" {
		t.Fatalf("nested app file not reverted after Discard Folder: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Path(), "app/fresh.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked app/fresh.txt survived Discard Folder: err=%v", err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "other/outside.txt")); err != nil || string(content) != "outside\nedited\n" {
		t.Fatalf("other/outside.txt was reverted although it is outside the folder: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "other/fresh.txt")); err != nil || string(content) != "fresh\n" {
		t.Fatalf("other/fresh.txt was deleted although it is outside the folder: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(repo.Path(), "application/sibling.txt")); err != nil || string(content) != "sibling\nedited\n" {
		t.Fatalf("the sibling folder application was discarded although it is not under app: content=%q err=%v", content, err)
	}
}

func TestChangesTreeActionsContextMenuAndCopyPath(t *testing.T) {
	// Right-clicking a folder row offers the section's folder actions and
	// Copy Path: the unstaged menu stages and discards, Copy Path puts the
	// folder's repository-relative path on the clipboard without a trailing
	// slash, and the staged menu offers Unstage instead.
	repo := treeActionsRepo(t, "treactions-menu")
	h := openTreeActionsHarness(t, repo)

	unstagedRow := "Unstaged Folder app, 2 files"
	if err := h.ui.RightClick(unstagedRow); err != nil {
		t.Fatalf("right-click folder row: %v\ntexts=%q", err, h.ui.Texts())
	}
	menu := h.ui.Menu()
	want := []string{"Stage Folder", "Discard Folder…", "-", "Copy Path"}
	if len(menu) != len(want) {
		t.Fatalf("folder menu = %q, want exactly %q", menu, want)
	}
	for i, w := range want {
		if menu[i] != w {
			t.Fatalf("folder menu = %q, want %q", menu, want)
		}
	}

	h.ui.SetClipboard("")
	if err := h.ui.ChooseMenuItem("Copy Path"); err != nil {
		t.Fatalf("choose Copy Path: %v; menu=%q", err, h.ui.Menu())
	}
	if got := h.ui.Clipboard(); got != "app" {
		t.Fatalf("clipboard = %q, want exactly %q", got, "app")
	}

	if err := h.ui.RightClick(unstagedRow); err != nil {
		t.Fatalf("right-click folder row again: %v\ntexts=%q", err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Stage Folder"); err != nil {
		t.Fatalf("choose Stage Folder: %v; menu=%q", err, h.ui.Menu())
	}
	h.drain()
	staged := porcelainLines(repo)
	for _, want := range []string{"M  app/one.txt", "M  app/nested/two.txt"} {
		if !strings.Contains(staged, want) {
			t.Fatalf("%q missing after the menu's Stage Folder; status=%q", want, staged)
		}
	}
	if !strings.Contains(staged, " M other/outside.txt") {
		t.Fatalf("other/outside.txt moved through the folder menu; status=%q", staged)
	}

	stagedRow := "Staged Folder app, 2 files"
	if err := h.ui.RightClick(stagedRow); err != nil {
		t.Fatalf("right-click staged folder row: %v\ntexts=%q", err, h.ui.Texts())
	}
	stagedMenu := h.ui.Menu()
	wantStaged := []string{"Unstage Folder", "-", "Copy Path"}
	if len(stagedMenu) != len(wantStaged) {
		t.Fatalf("staged folder menu = %q, want exactly %q", stagedMenu, wantStaged)
	}
	if err := h.ui.ChooseMenuItem("Unstage Folder"); err != nil {
		t.Fatalf("choose Unstage Folder: %v; menu=%q", err, stagedMenu)
	}
	h.drain()
	back := porcelainLines(repo)
	for _, want := range []string{" M app/one.txt", " M app/nested/two.txt"} {
		if !strings.Contains(back, want) {
			t.Fatalf("%q missing after the menu's Unstage Folder; status=%q", want, back)
		}
	}
}

func TestChangesTreeActionsLongFolderStagesAndUnstagesInBatches(t *testing.T) {
	// A folder of 2000 files whose paths measure past the batching limit of
	// one command stages in one click and unstages in one more, git status
	// counting exactly 2000 entries each way.
	repo := newGitRepo(t, "treactions-batch")
	repo.CommitFile("base.txt", "base\n", "initial")
	pad := strings.Repeat("p", 40)
	for i := 0; i < 2000; i++ {
		repo.WriteFile(fmt.Sprintf("bulk/%s-%04d.txt", pad, i), fmt.Sprintf("content %d\n", i))
	}

	h := openTreeActionsHarness(t, repo)
	mustClick(t, h, "Stage Folder bulk")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batched Stage error: %v", err)
	}
	countLines := func(out string) int {
		out = strings.TrimSpace(out)
		if out == "" {
			return 0
		}
		return len(strings.Split(out, "\n"))
	}
	if got := countLines(repo.Git("ls-files", "--", "bulk/")); got != 2000 {
		t.Fatalf("indexed files under bulk = %d after Stage Folder, want 2000", got)
	}

	mustClick(t, h, "Unstage Folder bulk")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected batched Unstage error: %v", err)
	}
	if got := countLines(repo.Git("ls-files", "--", "bulk/")); got != 0 {
		t.Fatalf("indexed files under bulk = %d after Unstage Folder, want 0", got)
	}
	if got := countLines(repo.Git("ls-files", "--others", "--exclude-standard", "--", "bulk/")); got != 2000 {
		t.Fatalf("untracked files under bulk = %d after Unstage Folder, want 2000", got)
	}
}

func TestChangesTreeActionsButtonsKeepCollapseState(t *testing.T) {
	// Clicking a folder's action button never toggles the row: a collapsed
	// folder keeps its collapse entry and its files hidden through Discard
	// and Stage clicks, and an expanded folder never gains one.
	repo := treeActionsRepo(t, "treactions-collapse")
	h := openTreeActionsHarness(t, repo)

	chain := "Unstaged Folder chain/a/b/c, 1 file"
	mustClick(t, h, chain)
	h.ui.Frame()
	if info, ok := treeFolderShown(h, chain); !ok || info.Expanded {
		t.Fatalf("setup: chain did not collapse; texts=%q", h.ui.Texts())
	}

	// Discard only asks, so nothing but the confirmation may follow the
	// click: the collapse entry survives it, and once the confirmation is
	// cancelled the row still reads collapsed with its file hidden.
	mustClick(t, h, "Discard Folder chain/a/b/c")
	h.ui.Frame()
	if !h.app.changesView.collapsedUnstaged["chain/a/b/c"] {
		t.Fatal("the Discard button click expanded the collapsed chain")
	}
	mustClick(t, h, "Cancel")
	h.ui.Frame()
	if info, ok := treeFolderShown(h, chain); !ok || info.Expanded {
		t.Fatalf("the chain row shows expanded after the Discard click; texts=%q", h.ui.Texts())
	}
	if changeRowShown(h, "Unstaged chain/a/b/c/x.txt") {
		t.Fatal("the chain file became visible when the Discard button was clicked")
	}

	mustClick(t, h, "Stage Folder chain/a/b/c")
	h.drain()
	if !h.app.changesView.collapsedUnstaged["chain/a/b/c"] {
		t.Fatal("the Stage button click dropped the chain's collapse entry")
	}

	// The expanded folder other never gains a collapse entry from the
	// clicks on its own buttons.
	if h.app.changesView.collapsedUnstaged["other"] {
		t.Fatal("setup: other collapsed although it was never clicked")
	}
	mustClick(t, h, "Stage Folder other")
	h.drain()
	if h.app.changesView.collapsedUnstaged["other"] {
		t.Fatal("the Stage button click collapsed the expanded other folder")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("unexpected Stage Folder error: %v", err)
	}
}
