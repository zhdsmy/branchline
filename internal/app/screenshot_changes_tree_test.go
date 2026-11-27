package app

import (
	"testing"
	"time"
)

// setupScreenshotChangesTree captures the Local Changes file list in its
// tree view: a merged chain of single-child folders, a collapsed folder and
// a selected file whose diff fills the right pane. Every commit carries a
// fixed committer date and the tree interactions — switching the view,
// collapsing the folder, selecting the row — go through their real controls.
func setupScreenshotChangesTree(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "branchline")
	base := screenshotFixtureTime
	detCommitFile(t, r, base, "src/core/engine/main.go", "package main\n\nfunc main() {\n\trun()\n}\n", "add engine entry point")
	detCommitFile(t, r, base.Add(time.Hour), "src/core/engine/render.go", "package main\n\nfunc run() {\n\tprintln(\"render\")\n}\n", "add renderer")
	detCommitFile(t, r, base.Add(2*time.Hour), "docs/guide/intro.md", "# Guide\n\nGetting started.\n", "write the guide")
	detCommitFile(t, r, base.Add(3*time.Hour), "notes/todo.txt", "first draft\n", "note the plan")
	detCommitFile(t, r, base.Add(4*time.Hour), "notes/plan.txt", "the plan\n", "write the plan")
	r.WriteFile("src/core/engine/main.go", "package main\n\nfunc main() {\n\trun()\n\tprintln(\"staged start\")\n}\n")
	r.Git("add", "--", "src/core/engine/main.go")
	r.WriteFile("src/core/engine/render.go", "package main\n\nfunc run() {\n\tprintln(\"render\")\n\tprintln(\"unstaged frame\")\n}\n")
	r.WriteFile("docs/guide/intro.md", "# Guide\n\nGetting started, revised.\n")
	r.WriteFile("notes/todo.txt", "first draft\nsecond draft\n")
	r.WriteFile("notes/scratch.txt", "scratch\n")

	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "Show as Tree")
	h.ui.Frame()
	mustSnapshotClick(t, h, "Unstaged Folder notes, 2 files")
	h.ui.Frame()
	mustSnapshotClick(t, h, "Unstaged src/core/engine/render.go")
	h.drain()
	for _, want := range []string{"Unstaged Folder src/core/engine, 1 file", "Staged Folder src/core/engine, 1 file"} {
		if _, ok := treeFolderShown(h, want); !ok {
			t.Fatalf("changes-tree state missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	if changeRowShown(h, "Unstaged notes/todo.txt") {
		t.Fatalf("changes-tree state did not collapse the notes folder; texts=%q", h.ui.Texts())
	}
	if !h.app.changesView.selected || h.app.changesView.selectedPath != "src/core/engine/render.go" {
		t.Fatalf("changes-tree state missing the selected file: %+v", h.app.changesView)
	}
	return h
}
