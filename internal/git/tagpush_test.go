package git

import (
	"strings"
	"testing"
)

func TestCreateTagAndPushToEveryRemote(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	first, second := t.TempDir(), t.TempDir()
	gitRun(t, first, "init", "-q", "--bare")
	gitRun(t, second, "init", "-q", "--bare")
	gitRun(t, dir, "remote", "add", "one", first)
	gitRun(t, dir, "remote", "add", "two", second)
	r := New(&LocalExecutor{Dir: dir})

	if err := r.CreateTagAndPush("v1", "HEAD", "release one", []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	for _, bare := range []string{first, second} {
		if out := gitOut(t, bare, "tag", "-l"); !strings.Contains(out, "v1") {
			t.Fatalf("%s has tags %q", bare, out)
		}
	}
	if kind := strings.TrimSpace(gitOut(t, dir, "cat-file", "-t", "v1")); kind != "tag" {
		t.Fatalf("v1 is a %s, want an annotated tag", kind)
	}

	err := r.CreateTagAndPush("v2", "HEAD", "", []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "pushing it to missing failed") {
		t.Fatalf("error %v", err)
	}
	if out := gitOut(t, dir, "tag", "-l", "v2"); !strings.Contains(out, "v2") {
		t.Fatal("the tag was lost when the push failed")
	}
}
