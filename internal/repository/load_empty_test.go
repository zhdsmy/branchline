package repository

// Snapshot-loading tests: repository.Load must assemble a complete
// snapshot on an unborn HEAD (an empty repository or a not yet committed
// --orphan branch), and a corrupt repository must keep failing the whole load
// with a zero-value Snapshot. The tests drive the real *git.Repo
// implementation, locally and over the loopback sshd.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

func loadTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Unborn", "GIT_AUTHOR_EMAIL=unborn@branchline.invalid",
		"GIT_COMMITTER_NAME=Unborn", "GIT_COMMITTER_EMAIL=unborn@branchline.invalid",
		"LC_ALL=C",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\r\n")
}

func writeLoadTestFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// initEmptyLoadRepo creates a truly empty repository (git init, no commits)
// on branch main with one untracked and one staged file.
func initEmptyLoadRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	loadTestGit(t, dir, "init", "-q", "-b", "main")
	loadTestGit(t, dir, "config", "user.name", "Unborn Test")
	loadTestGit(t, dir, "config", "user.email", "unborn@branchline.invalid")
	loadTestGit(t, dir, "config", "commit.gpgsign", "false")
	writeLoadTestFile(t, dir, "untracked.txt", "new\n")
	writeLoadTestFile(t, dir, "staged.txt", "staged\n")
	loadTestGit(t, dir, "add", "--", "staged.txt")
	return dir
}

func localLoadRepo(dir string) *git.Repo {
	return git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
}

// assertEmptySnapshot verifies the unborn empty-repository snapshot contract:
// no head hash, the branch name, no commits, no graph rows, and no branches.
func assertEmptySnapshot(t *testing.T, s Snapshot, branch string) {
	t.Helper()
	if s.Status.Head != "" {
		t.Errorf("Status.Head = %q, want empty (unborn)", s.Status.Head)
	}
	if s.Status.Branch != branch {
		t.Errorf("Status.Branch = %q, want %q", s.Status.Branch, branch)
	}
	if len(s.Commits) != 0 {
		t.Errorf("len(Commits) = %d, want 0", len(s.Commits))
	}
	if len(s.Graph) != 0 {
		t.Errorf("len(Graph) = %d, want 0", len(s.Graph))
	}
	if s.More {
		t.Errorf("More = true, want false")
	}
	if len(s.Refs.Branches) != 0 {
		t.Errorf("len(Refs.Branches) = %d, want 0 (%+v)", len(s.Refs.Branches), s.Refs.Branches)
	}
}

// assertUnstagedStaged verifies the working tree files survived the load.
func assertUnstagedStaged(t *testing.T, s Snapshot, unstaged, staged string) {
	t.Helper()
	foundUnstaged, foundStaged := false, false
	for _, f := range s.Status.Unstaged {
		if f.Path == unstaged {
			foundUnstaged = true
		}
	}
	for _, f := range s.Status.Staged {
		if f.Path == staged {
			foundStaged = true
		}
	}
	if !foundUnstaged {
		t.Errorf("unstaged file %q missing from snapshot status: %+v", unstaged, s.Status.Unstaged)
	}
	if !foundStaged {
		t.Errorf("staged file %q missing from snapshot status: %+v", staged, s.Status.Staged)
	}
}

func assertCurrentWorktree(t *testing.T, s Snapshot, branch string) {
	t.Helper()
	for _, w := range s.Worktrees {
		if w.Current {
			if w.Branch != branch {
				t.Errorf("current worktree branch = %q, want %q", w.Branch, branch)
			}
			return
		}
	}
	t.Errorf("snapshot has no current worktree: %+v", s.Worktrees)
}

// TestLoadEmptyRepo covers repository.Load on a truly empty local repository.
func TestLoadEmptyRepo(t *testing.T) {
	dir := initEmptyLoadRepo(t, "load-empty")
	s, err := Load(localLoadRepo(dir), Options{})
	if err != nil {
		t.Fatalf("Load on empty repository: %v", err)
	}
	assertEmptySnapshot(t, s, "main")
	assertUnstagedStaged(t, s, "untracked.txt", "staged.txt")
	assertCurrentWorktree(t, s, "main")
}

// TestLoadEmptyRepoLoopbackSSH is the same load over the loopback sshd.
func TestLoadEmptyRepoLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	dir := srv.TempDir(t, "load-empty")
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + git.ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Test'
git config user.email 'test@branchline.invalid'
git config commit.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
printf 'new\n' > untracked.txt
printf 'staged\n' > staged.txt
git add -- staged.txt
`
	srv.RunShell(t, script)

	s, err := Load(srv.Repo(t, dir), Options{})
	if err != nil {
		t.Fatalf("SSH Load on empty repository: %v", err)
	}
	assertEmptySnapshot(t, s, "main")
	assertUnstagedStaged(t, s, "untracked.txt", "staged.txt")
	assertCurrentWorktree(t, s, "main")
}

// prepareOrphan turns dir (which already has commits on main) into the
// uncommitted orphan branch state with a clean index.
func prepareOrphan(t *testing.T, dir, orphan string) {
	t.Helper()
	loadTestGit(t, dir, "checkout", "-q", "--orphan", orphan)
	loadTestGit(t, dir, "rm", "-q", "-r", "--cached", ".")
}

// assertOrphanSnapshot verifies the orphan-branch snapshot contract: other
// branches' commits survive and no ref is Current.
func assertOrphanSnapshot(t *testing.T, s Snapshot, orphan string, wantHashes ...string) {
	t.Helper()
	if s.Status.Head != "" {
		t.Errorf("orphan Status.Head = %q, want empty", s.Status.Head)
	}
	if s.Status.Branch != orphan {
		t.Errorf("orphan Status.Branch = %q, want %q", s.Status.Branch, orphan)
	}
	var hashes []string
	for _, c := range s.Commits {
		hashes = append(hashes, c.Hash)
	}
	for _, want := range wantHashes {
		found := false
		for _, h := range hashes {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("orphan snapshot missing commit %s: %v", want, hashes)
		}
	}
	if len(s.Graph) != len(s.Commits) {
		t.Errorf("len(Graph) = %d, want len(Commits) = %d", len(s.Graph), len(s.Commits))
	}
	foundMain := false
	for _, b := range s.Refs.Branches {
		if b.Name == "main" {
			foundMain = true
		}
		if b.Current {
			t.Errorf("branch %q marked Current on orphan branch", b.Name)
		}
		if b.Name == orphan {
			t.Errorf("unborn orphan branch %q must not exist as a ref", orphan)
		}
	}
	if !foundMain {
		t.Errorf("Refs.Branches missing main: %+v", s.Refs.Branches)
	}
}

// TestLoadUnbornOrphanBranch covers repository.Load on a repository checked
// out on an uncommitted --orphan branch.
func TestLoadUnbornOrphanBranch(t *testing.T) {
	dir := initEmptyLoadRepo(t, "load-orphan")
	loadTestGit(t, dir, "add", "--", "untracked.txt")
	loadTestGit(t, dir, "commit", "-q", "-m", "first")
	firstHash := strings.TrimSpace(loadTestGit(t, dir, "rev-parse", "HEAD"))
	writeLoadTestFile(t, dir, "b.txt", "two\n")
	loadTestGit(t, dir, "add", "b.txt")
	loadTestGit(t, dir, "commit", "-q", "-m", "second on main")
	mainTip := strings.TrimSpace(loadTestGit(t, dir, "rev-parse", "main"))
	prepareOrphan(t, dir, "orphan1")

	s, err := Load(localLoadRepo(dir), Options{})
	if err != nil {
		t.Fatalf("Load on orphan branch: %v", err)
	}
	assertOrphanSnapshot(t, s, "orphan1", firstHash, mainTip)
	assertCurrentWorktree(t, s, "orphan1")
}

// TestLoadUnbornOrphanBranchLoopbackSSH is the orphan load over the loopback
// sshd.
func TestLoadUnbornOrphanBranchLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	dir := srv.InitRepo(t, "load-orphan")
	srv.WriteFile(t, dir, "b.txt", []byte("two\n"))
	srv.RunGit(t, dir, "add", "b.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "second on main")
	mainTip := srv.RunGit(t, dir, "rev-parse", "refs/heads/main")
	initialHash := srv.RunGit(t, dir, "rev-list", "--max-parents=0", "main")
	srv.RunGit(t, dir, "checkout", "-q", "--orphan", "orphanssh")
	srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")

	s, err := Load(srv.Repo(t, dir), Options{})
	if err != nil {
		t.Fatalf("SSH Load on orphan branch: %v", err)
	}
	assertOrphanSnapshot(t, s, "orphanssh", initialHash, mainTip)
	assertCurrentWorktree(t, s, "orphanssh")
}

// TestLoadEmptyRepoErrorsNotSwallowed verifies the corrupt and missing input
// paths keep failing the whole load with a zero-value Snapshot.
func TestLoadEmptyRepoErrorsNotSwallowed(t *testing.T) {
	assertLoadFails := func(t *testing.T, r *git.Repo) {
		t.Helper()
		s, err := Load(r, Options{})
		if err == nil {
			t.Fatal("Load succeeded, want error")
		}
		if !reflect.DeepEqual(s, Snapshot{}) {
			t.Fatalf("failed Load returned a partial snapshot: %+v", s)
		}
	}

	t.Run("CorruptHEADFile", func(t *testing.T) {
		dir := initEmptyLoadRepo(t, "load-badhead")
		loadTestGit(t, dir, "add", "--", "untracked.txt")
		loadTestGit(t, dir, "commit", "-q", "-m", "first")
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("garbage content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertLoadFails(t, localLoadRepo(dir))
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir := initEmptyLoadRepo(t, "load-badref")
		loadTestGit(t, dir, "add", "--", "untracked.txt")
		loadTestGit(t, dir, "commit", "-q", "-m", "first")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("not-a-hash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertLoadFails(t, localLoadRepo(dir))
	})

	t.Run("OrphanWithCorruptOtherBranch", func(t *testing.T) {
		dir := initEmptyLoadRepo(t, "load-orphan-badref")
		loadTestGit(t, dir, "add", "--", "untracked.txt")
		loadTestGit(t, dir, "commit", "-q", "-m", "first")
		prepareOrphan(t, dir, "orphanbad")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("garbage\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		assertLoadFails(t, localLoadRepo(dir))
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		assertLoadFails(t, localLoadRepo(missing))
	})

	t.Run("NotAGitDirectory", func(t *testing.T) {
		assertLoadFails(t, localLoadRepo(t.TempDir()))
	})
}

// TestLoadEmptyRepoErrorsNotSwallowedLoopbackSSH runs the failure-path loads
// over the loopback sshd.
func TestLoadEmptyRepoErrorsNotSwallowedLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	assertLoadFails := func(t *testing.T, r *git.Repo) {
		t.Helper()
		s, err := Load(r, Options{})
		if err == nil {
			t.Fatal("SSH Load succeeded, want error")
		}
		if !reflect.DeepEqual(s, Snapshot{}) {
			t.Fatalf("failed SSH Load returned a partial snapshot: %+v", s)
		}
	}

	t.Run("CorruptHEADFile", func(t *testing.T) {
		dir := srv.InitRepo(t, "load-badhead")
		srv.RunShell(t, "printf 'garbage content\\n' > "+git.ShellQuote(dir+"/.git/HEAD"))
		assertLoadFails(t, srv.Repo(t, dir))
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir := srv.InitRepo(t, "load-badref")
		srv.RunShell(t, "printf 'not-a-hash\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		assertLoadFails(t, srv.Repo(t, dir))
	})

	t.Run("OrphanWithCorruptOtherBranch", func(t *testing.T) {
		dir := srv.InitRepo(t, "load-orphan-badref")
		srv.RunGit(t, dir, "checkout", "-q", "--orphan", "orphanbad")
		srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")
		srv.RunShell(t, "printf 'garbage\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		assertLoadFails(t, srv.Repo(t, dir))
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		assertLoadFails(t, srv.Repo(t, srv.RootDir+"/load-no-such-dir"))
	})

	t.Run("NotAGitDirectory", func(t *testing.T) {
		assertLoadFails(t, srv.Repo(t, srv.TempDir(t, "load-plain")))
	})
}
