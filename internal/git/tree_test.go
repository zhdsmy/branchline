package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestTreeUnusualPaths(t *testing.T) {
	dir := testRepo(t)
	names := []string{"dir/space and é.txt", "tab\tname.txt", "new\nline.txt"}
	for _, name := range names {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "--", names[0], names[1], names[2]}, {"commit", "-qm", "unusual paths"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	r := New(&LocalExecutor{Dir: dir})
	entries, err := r.Tree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		i := slices.IndexFunc(entries, func(e TreeEntry) bool { return e.Path == name })
		if i < 0 {
			t.Fatalf("missing %q: %+v", name, entries)
		}
		blob, err := r.Blob(entries[i].Hash)
		if err != nil || string(blob) != name {
			t.Fatalf("blob %q: %q %v", name, blob, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"a name\"]\n\tpath = lib/a name\n\turl = ../example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	paths, err := r.Submodules()
	if err != nil || !slices.Equal(paths, []string{"lib/a name"}) {
		t.Fatalf("submodules: %q %v", paths, err)
	}
}
