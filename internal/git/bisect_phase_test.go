package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBisectProgressSeparatesWaitingSearchingAndCompletion(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "base\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	for i := 0; i < 6; i++ {
		writeFile(t, dir, "a.txt", strings.Repeat("x", i+1)+"\n")
		gitRun(t, dir, "commit", "-qam", "revision")
	}
	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.Bisect("start"); err != nil {
		t.Fatal(err)
	}
	progress, ok, err := r.BisectProgress()
	if err != nil || !ok || progress.Phase != BisectWaiting || progress.Marked {
		t.Fatalf("waiting %+v ok %v err %v", progress, ok, err)
	}
	if _, err := r.Bisect("bad"); err != nil {
		t.Fatal(err)
	}
	progress, ok, err = r.BisectProgress()
	if err != nil || !ok || progress.Phase != BisectWaiting || progress.Marked {
		t.Fatalf("one boundary %+v ok %v err %v", progress, ok, err)
	}
	gitRun(t, dir, "checkout", "-q", "HEAD~5")
	if _, err := r.Bisect("good"); err != nil {
		t.Fatal(err)
	}
	progress, ok, err = r.BisectProgress()
	if err != nil || !ok || progress.Phase != BisectSearching || !progress.Marked || progress.Head == "" {
		t.Fatalf("searching %+v ok %v err %v", progress, ok, err)
	}

	bad := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "refs/bisect/bad", bad)
	if err := os.WriteFile(filepath.Join(dir, ".git", "BISECT_LOG"), []byte("# first bad commit: ["+bad+"] revision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	progress, ok, err = r.BisectProgress()
	if err != nil || !ok || progress.Phase != BisectCompleted || progress.Marked || !strings.Contains(progress.Result, "first bad commit") {
		t.Fatalf("completed %+v ok %v err %v", progress, ok, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "BISECT_LOG"), []byte("There is no single first bad commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	progress, ok, err = r.BisectProgress()
	if err != nil || !ok || progress.Phase != BisectAmbiguous || progress.Marked || !strings.Contains(progress.Result, "no single first bad commit") {
		t.Fatalf("ambiguous %+v ok %v err %v", progress, ok, err)
	}
}
