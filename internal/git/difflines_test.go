package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDiffContextAndUntrackedLines(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	var body strings.Builder
	for i := 1; i <= 20; i++ {
		body.WriteString("line\n")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\nfour\nfive\nsix\n"), 0o644))
	must(r.Stage("a.txt"))
	must(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\nFOUR\nfive\nsix\n"), 0o644))
	f := FileChange{Path: "a.txt", Status: 'M'}
	zero, err := r.FileDiffOpts(f, DiffOptions{Context: -1})
	must(err)
	if !strings.Contains(zero, "@@ -4 +4 @@") || strings.Contains(zero, "\n three") {
		t.Fatalf("-U0 diff\n%s", zero)
	}
	one, err := r.FileDiffOpts(f, DiffOptions{Context: 1})
	must(err)
	if !strings.Contains(one, "\n three") || strings.Contains(one, "\n two") {
		t.Fatalf("-U1 diff\n%s", one)
	}

	must(os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body.String()), 0o644))
	n, binary, err := r.UntrackedLines("big.txt")
	if err != nil || n != 20 || binary {
		t.Fatalf("lines %d binary %v err %v", n, binary, err)
	}
	must(os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 0}, 0o644))
	if _, binary, err := r.UntrackedLines("blob.bin"); err != nil || !binary {
		t.Fatalf("binary %v err %v", binary, err)
	}
	patch, err := r.FileDiffOpts(FileChange{Path: "big.txt", Status: '?'}, DiffOptions{Context: 1})
	if err != nil || !strings.Contains(patch, "+++ b/big.txt") {
		t.Fatalf("untracked patch %q %v", patch, err)
	}
}

func TestApplyLines(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	// a.txt is one two three in the working tree, one two in the index.
	stage := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -2,1 +2,2 @@\n two\n+three\n"
	if err := r.ApplyLines(stage, true, false); err != nil {
		t.Fatal(err)
	}
	staged, err := r.StagedDiff("a.txt")
	if err != nil || !strings.Contains(staged, "+three") {
		t.Fatalf("staged %q %v", staged, err)
	}
	// Unstaging is the same patch backwards against the index.
	if err := r.ApplyLines(stage, true, true); err != nil {
		t.Fatal(err)
	}
	if staged, _ := r.StagedDiff("a.txt"); strings.Contains(staged, "three") {
		t.Fatalf("still staged %q", staged)
	}
	// Discarding applies it backwards to the working tree.
	if err := r.ApplyLines(stage, false, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "one\ntwo\n" {
		t.Fatalf("worktree %q", got)
	}
	if err := r.ApplyLines(" ", true, false); err == nil {
		t.Fatal("an empty patch applied")
	}
}
