package git

import (
	"path/filepath"
	"reflect"
	"testing"
)

// withSubmodule returns a repository with the submodule "vendor/my lib"
// and the submodule's folder.
func withSubmodule(t *testing.T) (dir, sub string) {
	t.Helper()
	lib := initRepo(t)
	writeFile(t, lib, "lib.txt", "lib\n")
	gitRun(t, lib, "add", ".")
	gitRun(t, lib, "commit", "-q", "-m", "lib")
	dir = initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "one")
	gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "--", lib, "vendor/my lib")
	gitRun(t, dir, "commit", "-q", "-m", "add lib")
	sub = filepath.Join(dir, "vendor", "my lib")
	gitRun(t, sub, "config", "user.name", "Ada")
	gitRun(t, sub, "config", "user.email", "ada@example.com")
	return dir, sub
}

func commitLocalOnly(t *testing.T, sub string) {
	t.Helper()
	writeFile(t, sub, "new.txt", "new\n")
	gitRun(t, sub, "add", ".")
	gitRun(t, sub, "commit", "-q", "-m", "local only")
}

// A submodule with a commit its remote lacks is reported until the
// commit is pushed.
func TestUnpushedSubmodules(t *testing.T) {
	plain := initRepo(t)
	if got, err := New(&LocalExecutor{Dir: plain}).UnpushedSubmodules(); err != nil || len(got) != 0 {
		t.Fatalf("no submodules: %v %v", got, err)
	}
	dir, sub := withSubmodule(t)
	r := New(&LocalExecutor{Dir: dir})
	if got, err := r.UnpushedSubmodules(); err != nil || len(got) != 0 {
		t.Fatalf("a fresh submodule: %v %v", got, err)
	}
	commitLocalOnly(t, sub)
	got, err := r.UnpushedSubmodules()
	must(t, err)
	if want := []string{"vendor/my lib"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unpushed %q, want %q", got, want)
	}
	gitRun(t, sub, "push", "-q", "origin", "HEAD:refs/heads/pushed")
	if got, err := r.UnpushedSubmodules(); err != nil || len(got) != 0 {
		t.Fatalf("after the push: %v %v", got, err)
	}
}

func TestUnpushedSubmodulesOverSSH(t *testing.T) {
	srv := startSSHD(t)
	dir, sub := withSubmodule(t)
	commitLocalOnly(t, sub)
	got, err := New(srv.executor(t, dir)).UnpushedSubmodules()
	must(t, err)
	if want := []string{"vendor/my lib"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("through ssh %q, want %q", got, want)
	}
}
