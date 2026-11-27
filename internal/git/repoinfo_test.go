package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseObjectSize(t *testing.T) {
	out := "count: 3\nsize: 12\nin-pack: 10\npacks: 1\nsize-pack: 40\nprune-packable: 0\ngarbage: 0\nsize-garbage: 0\n"
	if got := ParseObjectSize(out); got != 52 {
		t.Fatalf("size %d, want 52", got)
	}
}

func TestSizeText(t *testing.T) {
	for kb, want := range map[int64]string{12: "12 KB", 2048: "2.0 MB", 3 * 1024 * 1024: "3.00 GB"} {
		if got := SizeText(kb); got != want {
			t.Errorf("%d: %q, want %q", kb, got, want)
		}
	}
}

func TestInfoOfARepository(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	in, err := r.Info()
	if err != nil {
		t.Fatal(err)
	}
	if in.Commits != 4 || in.Last.Hash == "" || in.First.Hash == "" || in.Last.Date.IsZero() || in.First.Subject == "" {
		t.Fatalf("info %+v", in)
	}
	if in.Commits == 1 && in.First.Hash != in.Last.Hash {
		t.Fatalf("one commit is both first and last: %+v", in)
	}
	if in.Commits > 1 && in.First.Hash == in.Last.Hash {
		t.Fatalf("first and last are the same commit of %d: %+v", in.Commits, in)
	}
	if in.SizeKB <= 0 {
		t.Fatalf("size %d", in.SizeKB)
	}
}

func TestInfoOfAnEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	r := New(&LocalExecutor{Dir: dir})
	if err := r.Init("main"); err != nil {
		t.Fatal(err)
	}
	in, err := r.Info()
	if err != nil || in.Commits != 0 || in.Last.Hash != "" {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestReadme(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if name, text, err := r.Readme(); err != nil || name != "" || text != "" {
		t.Fatalf("readme of a repository without one: %q %q %v", name, text, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Title\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, text, err := r.Readme()
	if err != nil || name != "README.md" || text != "# Title\n" {
		t.Fatalf("%q %q %v", name, text, err)
	}
}
