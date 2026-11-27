package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A submodule is measured against the commit the repository records: the
// commits it is ahead by and behind by, and its uncommitted files; one
// not checked out has no state of its own. Both executors answer the same.
func TestSubmoduleStateOf(t *testing.T) {
	dir := testRepo(t)
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_ALLOW_PROTOCOL=file")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	sub := t.TempDir()
	gitIn(sub, "init", "-q", "-b", "main")
	gitIn(sub, "commit", "-q", "--allow-empty", "-m", "sub one")
	gitIn(sub, "commit", "-q", "--allow-empty", "-m", "sub two")
	gitIn(dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "lib")
	gitIn(dir, "commit", "-q", "-m", "add lib")
	lib := filepath.Join(dir, "lib")

	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		s, err := r.SubmoduleStateOf("lib")
		if err != nil || !s.CheckedOut() || s.Behind != 0 || s.Ahead != 0 || s.Uncommitted != 0 || s.Recorded != s.Head {
			t.Fatalf("in step: %+v %v", s, err)
		}
	})

	// Two commits ahead of the one recorded, one file changed and one new.
	gitIn(lib, "commit", "-q", "--allow-empty", "-m", "sub three")
	gitIn(lib, "commit", "-q", "--allow-empty", "-m", "sub four")
	if err := os.WriteFile(filepath.Join(lib, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		s, err := r.SubmoduleStateOf("lib")
		if err != nil || s.Ahead != 2 || s.Behind != 0 || s.Uncommitted != 1 {
			t.Fatalf("ahead: %+v %v", s, err)
		}
	})

	// Back one commit before the recorded one: behind by one, ahead by none.
	gitIn(lib, "checkout", "-q", "--detach", "HEAD~3")
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		s, err := r.SubmoduleStateOf("lib")
		if err != nil || s.Behind != 1 || s.Ahead != 0 {
			t.Fatalf("behind: %+v %v", s, err)
		}
	})

	// A submodule not cloned is an empty directory of the repository above.
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		s, err := r.SubmoduleStateOf("lib")
		if err != nil || s.CheckedOut() || s.Recorded == "" {
			t.Fatalf("not checked out: %+v %v", s, err)
		}
	})
}
