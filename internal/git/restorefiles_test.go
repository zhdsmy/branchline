package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetFilesAndPatchFiles(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "a1\n")
	writeFile(t, dir, "b.txt", "b1\n")
	writeFile(t, dir, "gone.txt", "gone\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	first := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "a.txt", "a2\n")
	writeFile(t, dir, "b.txt", "b2\n")
	writeFile(t, dir, "new.txt", "new\n")
	gitRun(t, dir, "rm", "-q", "gone.txt")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "second")
	second := gitOut(t, dir, "rev-parse", "HEAD")
	r := New(&LocalExecutor{Dir: dir})

	patch, err := r.FormatPatchFiles(second, "a.txt", "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "new.txt") || strings.Contains(patch, "b.txt") || strings.Contains(patch, "gone.txt") {
		t.Fatalf("patch of two files:\n%s", patch)
	}

	// Before the second commit: a and b go back, new.txt is removed, gone.txt returns.
	if err := r.ResetFiles(second+"~", "a.txt", "new.txt", "gone.txt"); err != nil {
		t.Fatal(err)
	}
	got := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<none>"
		}
		return string(b)
	}
	if got("a.txt") != "a1\n" || got("new.txt") != "<none>" || got("gone.txt") != "gone\n" || got("b.txt") != "b2\n" {
		t.Fatalf("after reset: a=%q new=%q gone=%q b=%q", got("a.txt"), got("new.txt"), got("gone.txt"), got("b.txt"))
	}
	// And at the commit again.
	if err := r.ResetFiles(second, "a.txt", "new.txt", "gone.txt"); err != nil {
		t.Fatal(err)
	}
	if got("a.txt") != "a2\n" || got("new.txt") != "new\n" || got("gone.txt") != "<none>" {
		t.Fatalf("after second reset: a=%q new=%q gone=%q", got("a.txt"), got("new.txt"), got("gone.txt"))
	}
	if st := gitOut(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("tree is not clean: %q", st)
	}

	// Before the first commit nothing exists.
	empty, err := r.EmptyTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ResetFiles(empty, "a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	if got("a.txt") != "<none>" || got("b.txt") != "<none>" {
		t.Fatal("files stayed before the first commit")
	}
	_ = first
	if err := r.ResetFiles("", "a.txt"); err == nil {
		t.Fatal("no revision should fail")
	}
}
