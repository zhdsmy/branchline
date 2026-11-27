package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSubmoduleFolder(t *testing.T) {
	for _, c := range []struct{ url, folder, want string }{
		{"https://host/team/lib.git", "", "lib"},
		{"git@host:team/lib.git", "", "lib"},
		{"/srv/git/lib/", " ", "lib"},
		{"https://host/team/lib", "vendor/", "vendor/lib"},
		{"https://host/team/lib", "./deps/other", "deps/other"},
	} {
		if got := SubmoduleFolder(c.url, c.folder); got != c.want {
			t.Errorf("SubmoduleFolder(%q, %q) = %q, want %q", c.url, c.folder, got, c.want)
		}
	}
}

// A submodule that has a submodule of its own: only a recursive add
// fetches the inner one.
func TestSubmoduleAddAtFetchesNestedSubmodulesWhenAsked(t *testing.T) {
	inner := initRepo(t)
	writeFile(t, inner, "inner.txt", "inner\n")
	gitRun(t, inner, "add", ".")
	gitRun(t, inner, "commit", "-q", "-m", "inner")

	middle := initRepo(t)
	writeFile(t, middle, "middle.txt", "middle\n")
	gitRun(t, middle, "add", ".")
	gitRun(t, middle, "commit", "-q", "-m", "middle")
	gitRun(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", "--", inner, "deep")
	gitRun(t, middle, "commit", "-q", "-m", "add inner")

	for _, recursive := range []bool{false, true} {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", ".")
		gitRun(t, dir, "commit", "-q", "-m", "base")
		r := New(&LocalExecutor{Dir: dir})
		if err := r.SubmoduleAddAt(middle, "vendor/", recursive); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "vendor", filepath.Base(middle), "middle.txt")); err != nil {
			t.Fatalf("recursive=%v: the submodule was not cloned into vendor/: %v", recursive, err)
		}
		_, err := os.Stat(filepath.Join(dir, "vendor", filepath.Base(middle), "deep", "inner.txt"))
		if recursive && err != nil {
			t.Fatalf("nested submodule missing: %v", err)
		}
		if !recursive && err == nil {
			t.Fatal("nested submodule fetched without being asked")
		}
	}
	if err := New(&LocalExecutor{Dir: t.TempDir()}).SubmoduleAddAt(" ", "x", false); err == nil {
		t.Fatal("an empty URL was accepted")
	}
}
