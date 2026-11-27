package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscardSubmodule(t *testing.T) {
	srv := startSSHD(t)
	for _, ssh := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "ssh"}[ssh], func(t *testing.T) {
			dir, sub := withSubmodule(t)
			writeFile(t, sub, "lib.txt", "edited\n")
			writeFile(t, sub, "untracked.txt", "untracked\n")
			var ex Executor = &LocalExecutor{Dir: dir}
			if ssh {
				ex = srv.executor(t, dir)
			}
			r := New(ex)
			if err := r.DiscardSubmodule("not-a-submodule"); err == nil {
				t.Fatal("accepted an unregistered path")
			}
			must(t, r.DiscardSubmodule("vendor/my lib"))
			data, err := os.ReadFile(filepath.Join(sub, "lib.txt"))
			must(t, err)
			if string(data) != "lib\n" {
				t.Fatalf("lib %q", data)
			}
			if _, err := os.Stat(filepath.Join(sub, "untracked.txt")); !os.IsNotExist(err) {
				t.Fatalf("untracked remains: %v", err)
			}
		})
	}
}

func TestDiscardSubmoduleDoesNotResetParentWhenUninitialized(t *testing.T) {
	dir, _ := withSubmodule(t)
	gitRun(t, dir, "submodule", "deinit", "-f", "--", "vendor/my lib")
	writeFile(t, dir, "a.txt", "keep my changes\n")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.DiscardSubmodule("vendor/my lib"); err == nil {
		t.Fatal("accepted an uninitialized submodule")
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	must(t, err)
	if string(data) != "keep my changes\n" {
		t.Fatalf("parent changed %q", data)
	}
}

func TestDiscardSubmoduleRequiresAGitlink(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "committed\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "first")
	gitRun(t, dir, "config", "--file", ".gitmodules", "submodule.fake.path", ".")
	writeFile(t, dir, "a.txt", "keep edits\n")
	if err := New(&LocalExecutor{Dir: dir}).DiscardSubmodule("."); err == nil {
		t.Fatal("accepted parent directory from a fake .gitmodules entry")
	}
	data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	must(t, err)
	if string(data) != "keep edits\n" {
		t.Fatalf("parent changed: %q", data)
	}
}
