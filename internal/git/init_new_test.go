package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolveTestDir is dir with its symlinks settled, the spelling
// rev-parse --show-toplevel answers with on macOS where /var is one.
func resolveTestDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// initBranchIn is what git symbolic-ref HEAD says inside dir.
func initBranchIn(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git symbolic-ref HEAD in %s: %v\n%s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The parent folder is created with the folders above it, and the new
// repository's unborn HEAD is the asked initial branch.
func TestInitNewCreatesMissingParentAndRepository(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "deep", "nested")
	ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}

	top, err := InitNew(ex, "fresh", "main")
	if err != nil {
		t.Fatalf("InitNew: %v", err)
	}
	want := filepath.Join(resolveTestDir(t, parent), "fresh")
	if top != want {
		t.Fatalf("top level = %q, want %q", top, want)
	}
	if got := initBranchIn(t, top); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}
	if _, err := os.Stat(filepath.Join(top, ".git")); err != nil {
		t.Fatalf("no .git in the new repository: %v", err)
	}
}

// A branch other than main is the unborn HEAD the same way.
func TestInitNewBranchTrunk(t *testing.T) {
	parent := t.TempDir()
	ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}

	top, err := InitNew(ex, "trunky", "trunk")
	if err != nil {
		t.Fatalf("InitNew: %v", err)
	}
	if got := initBranchIn(t, top); got != "refs/heads/trunk" {
		t.Fatalf("HEAD = %q, want refs/heads/trunk", got)
	}
}

// An empty branch means main, as Repo.Init reads it.
func TestInitNewEmptyBranchMeansMain(t *testing.T) {
	parent := t.TempDir()
	ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}

	top, err := InitNew(ex, "defaulted", "")
	if err != nil {
		t.Fatalf("InitNew: %v", err)
	}
	if got := initBranchIn(t, top); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}
}

// A file or a folder already holding the name is never initialized into and
// never touched: the error names the collision and the content survives.
func TestInitNewExistingNameRefusedUntouched(t *testing.T) {
	for _, kind := range []string{"folder", "file"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "taken")
			sentinelPath := target
			if kind == "folder" {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				sentinelPath = filepath.Join(target, "sentinel.txt")
			}
			if err := os.WriteFile(sentinelPath, []byte("keep me\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}
			_, err := InitNew(ex, "taken", "main")
			if err == nil {
				t.Fatal("InitNew initialized into the existing name")
			}
			if !strings.Contains(err.Error(), `a file or folder named "taken" already exists`) {
				t.Fatalf("error = %q, want the collision named", err)
			}
			data, readErr := os.ReadFile(sentinelPath)
			if readErr != nil {
				t.Fatalf("content gone: %v", readErr)
			}
			sentinel := string(data)
			if sentinel != "keep me\n" {
				t.Fatalf("content after refused init = %q, want intact", sentinel)
			}
			if kind == "folder" {
				if _, statErr := os.Stat(filepath.Join(target, ".git")); statErr == nil {
					t.Fatal("the init wrote into the existing folder")
				}
			}
		})
	}
}

// A name git cannot be asked to create fails before anything is made: the
// parent folder does not even appear.
func TestInitNewIllegalNameBuildsNothing(t *testing.T) {
	for _, name := range []string{"", "a/b", ".."} {
		t.Run("name="+name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}
			if _, err := InitNew(ex, name, "main"); err == nil {
				t.Fatalf("InitNew accepted the name %q", name)
			}
			if _, err := os.Stat(parent); err == nil {
				t.Fatalf("the parent folder was created for the illegal name %q", name)
			}
		})
	}
}

// A branch git must refuse fails before anything is made, so the refusal
// leaves no folder behind.
func TestInitNewIllegalBranchBuildsNothing(t *testing.T) {
	for _, branch := range []string{"-x", "a..b", "a b"} {
		t.Run("branch="+branch, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			ex := &LocalExecutor{Dir: parent, NoGlobalConfig: true}
			if _, err := InitNew(ex, "fresh", branch); err == nil {
				t.Fatalf("InitNew accepted the branch %q", branch)
			}
			if _, err := os.Stat(parent); err == nil {
				t.Fatalf("the parent folder was created for the illegal branch %q", branch)
			}
		})
	}
}

// Over a real loopback sshd the repository is created on the server, the
// answered root is the absolute path the server reports, and a folder name
// with a space and a single quote survives the shell of both sides.
func TestInitNewOverSSHServer(t *testing.T) {
	srv := startSSHD(t)
	parent := filepath.Join(t.TempDir(), "server parent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "it's mine"
	ex := srv.executor(t, parent)
	ex.NoGlobalConfig = true

	top, err := InitNew(ex, name, "main")
	if err != nil {
		t.Fatalf("InitNew over ssh: %v", err)
	}
	if strings.HasPrefix(top, "~") || !strings.HasPrefix(top, "/") {
		t.Fatalf("answered root %q is not absolute", top)
	}
	want := filepath.Join(resolveTestDir(t, parent), name)
	if top != want {
		t.Fatalf("top level = %q, want %q", top, want)
	}
	if got := initBranchIn(t, top); got != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", got)
	}

	// The collision is read on the server too: a second init of the same
	// name is refused and the first repository is untouched.
	if _, err := InitNew(ex, name, "main"); err == nil {
		t.Fatal("a second InitNew of the same name succeeded over ssh")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init error = %q, want the collision", err)
	}
	if got := initBranchIn(t, top); got != "refs/heads/main" {
		t.Fatalf("HEAD after refused second init = %q, want refs/heads/main", got)
	}
}
