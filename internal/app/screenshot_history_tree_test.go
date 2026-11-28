package app

import (
	"strings"
	"testing"
	"time"
)

// setupScreenshotHistoryTree captures the History commit detail with its
// changed files as a tree: one commit touching a merged chain of
// single-child folders, a folder of two files and a root file, the chain
// folder collapsed and the diff of the selected file filling the right
// pane. Every commit carries a fixed committer date and the interactions —
// choosing the commit, switching the view, collapsing the folder — go
// through their real controls.
func setupScreenshotHistoryTree(t *testing.T) *testHarness {
	t.Helper()
	r := newDetGitRepo(t, "branchline")
	base := screenshotFixtureTime
	detCommitFile(t, r, base, "README.md", "# Branchline fixture\n\nA deterministic repository for UI screenshots.\n", "Initial project skeleton")

	// One commit changing a chain of folders, a wider folder and a root
	// file, so the tree shows the merge, the counts and a depth-0 file.
	files := map[string]string{
		"src/core/engine/main.go":   "package main\n\nfunc main() {\n\trun()\n}\n",
		"src/core/engine/render.go": "package main\n\nfunc run() {\n\tprintln(\"render\")\n}\n",
		"docs/guide/intro.md":       "# Guide\n\nGetting started.\n",
		"docs/guide/usage.md":       "# Usage\n\nUse it well.\n",
		"notes.txt":                 "the plan\n",
	}
	for path, content := range files {
		r.WriteFile(path, content)
	}
	r.Git("add", "--", "src/core/engine/main.go", "src/core/engine/render.go", "docs/guide/intro.md", "docs/guide/usage.md", "notes.txt")
	r.gitAt(base.Add(time.Hour), "commit", "-q", "-m", "Build the engine and write the guide")
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))

	h := newSnapshotApp(t)
	h.openRepo(r.StoreRepo())
	h.drain()
	mustSnapshotClick(t, h, "History")
	h.drain()
	mustSnapshotClick(t, h, commitRowLabel(head, "Build the engine and write the guide"))
	h.drain()
	mustSnapshotClick(t, h, "Show as Tree")
	h.ui.Frame()
	mustSnapshotClick(t, h, "Folder docs/guide, 2 files")
	h.ui.Frame()
	mustSnapshotClick(t, h, fileRowLabel("src/core/engine/render.go"))
	h.drain()

	for _, want := range []string{"Folder src/core/engine, 2 files", "Folder docs/guide, 2 files"} {
		if _, ok := historyFolderShown(h, want); !ok {
			t.Fatalf("history-tree state missing %q; texts=%q", want, h.ui.Texts())
		}
	}
	if historyFileRowShown(h, "docs/guide/intro.md") {
		t.Fatalf("history-tree state did not collapse the docs folder; texts=%q", h.ui.Texts())
	}
	if got := h.app.historyView.diff.req.File.Path; got != "src/core/engine/render.go" {
		t.Fatalf("history-tree state diff path = %q, want src/core/engine/render.go", got)
	}
	return h
}
