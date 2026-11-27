package git_test

// Domain-level tests: Log must tolerate an unborn HEAD (an empty repository
// or a not yet committed --orphan branch) by retrying without the strict HEAD
// revision, while every corrupt-repository, missing-path and
// explicit-revision failure still surfaces as an error.
//
// The file is package git_test because internal/sshtest imports
// github.com/zhdsmy/branchline/internal/git; a same-package test would create an import cycle.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// localGit runs the machine's git inside dir with isolated configuration.
func localGit(t *testing.T, dir string, args ...string) string {
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

// writeFileLocal writes content inside dir, creating parent directories.
func writeFileLocal(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// initEmptyLocal creates a truly empty repository (git init, no commits) on
// the initial branch main and returns its directory.
func initEmptyLocal(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	localGit(t, dir, "init", "-q", "-b", "main")
	localGit(t, dir, "config", "user.name", "Unborn Test")
	localGit(t, dir, "config", "user.email", "unborn@branchline.invalid")
	localGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// initCommittedLocal creates a repository on main with one commit ("first",
// containing a.txt) and returns its directory and the commit hash.
func initCommittedLocal(t *testing.T, name string) (string, string) {
	t.Helper()
	dir := initEmptyLocal(t, name)
	writeFileLocal(t, dir, "a.txt", "one\n")
	localGit(t, dir, "add", "a.txt")
	localGit(t, dir, "commit", "-q", "-m", "first")
	return dir, strings.TrimSpace(localGit(t, dir, "rev-parse", "HEAD"))
}

func localRepo(dir string) *git.Repo {
	return git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
}

// logHashes returns the hashes of r.Log with the default options.
func logHashes(t *testing.T, r *git.Repo) []string {
	t.Helper()
	commits, err := r.Log(git.LogOptions{})
	if err != nil {
		t.Fatalf("Log default options: %v", err)
	}
	var hashes []string
	for _, c := range commits {
		hashes = append(hashes, c.Hash)
	}
	return hashes
}

func containsHash(hashes []string, want string) bool {
	for _, h := range hashes {
		if h == want {
			return true
		}
	}
	return false
}

// TestLogUnbornEmptyRepo covers the empty-repository case on the local
// executor: Log returns an empty list and no error, while Status still
// reports the initial branch with an empty head, and CommandLog retains the
// original failed log args untouched alongside the retry without HEAD.
func TestLogUnbornEmptyRepo(t *testing.T) {
	dir := initEmptyLocal(t, "empty")
	cmdLog := git.NewCommandLog()
	r := localRepo(dir).WithCommandLog(cmdLog)

	commits, err := r.Log(git.LogOptions{})
	if err != nil {
		t.Fatalf("Log on empty repository: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("empty repository Log returned %d commits, want 0", len(commits))
	}

	records, _ := cmdLog.Snapshot()
	if len(records) != 4 {
		t.Fatalf("empty repository Log recorded %d commands, want 4 (log, symbolic-ref, show-ref, retry log): %+v", len(records), records)
	}
	if !slices.Contains(records[0].Args, "HEAD") || records[0].Args[len(records[0].Args)-1] != "--" {
		t.Fatalf("initial log CommandLog args mutated: %v", records[0].Args)
	}
	if len(records[1].Args) == 0 || records[1].Args[0] != "symbolic-ref" {
		t.Fatalf("second command = %v, want symbolic-ref", records[1].Args)
	}
	if len(records[2].Args) == 0 || records[2].Args[0] != "show-ref" {
		t.Fatalf("third command = %v, want show-ref", records[2].Args)
	}
	if slices.Contains(records[3].Args, "HEAD") || !slices.Contains(records[3].Args, "--branches") || records[3].Args[len(records[3].Args)-1] != "--" {
		t.Fatalf("retry log CommandLog args unexpected: %v", records[3].Args)
	}

	st, err := r.Status()
	if err != nil {
		t.Fatalf("Status on empty repository: %v", err)
	}
	if st.Head != "" {
		t.Fatalf("Status.Head = %q, want empty (unborn)", st.Head)
	}
	if st.Branch != "main" {
		t.Fatalf("Status.Branch = %q, want main", st.Branch)
	}

	// A query-driven search walks the same default revisions twice and must
	// also come back empty rather than erroring.
	got, err := r.Log(git.LogOptions{Query: "anything"})
	if err != nil {
		t.Fatalf("Log with query on empty repository: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("query Log on empty repository returned %d commits, want 0", len(got))
	}
}

// TestLogUnbornOrphanBranch covers a repository with existing commits checked
// out on an uncommitted --orphan branch: Log must still return the other
// branches' commits, and a detached orphan commit in a normal repository must
// keep appearing through the HEAD revision.
func TestLogUnbornOrphanBranch(t *testing.T) {
	dir, firstHash := initCommittedLocal(t, "orphan")

	// A second branch with its own commit makes the multi-branch case real.
	localGit(t, dir, "branch", "feature")
	writeFileLocal(t, dir, "b.txt", "two\n")
	localGit(t, dir, "add", "b.txt")
	localGit(t, dir, "commit", "-q", "-m", "second on main")
	mainTip := strings.TrimSpace(localGit(t, dir, "rev-parse", "main"))

	localGit(t, dir, "checkout", "-q", "--orphan", "orphan1")
	// git checkout --orphan keeps the index staged; clear it so the status
	// assertions reflect a plain unborn branch.
	localGit(t, dir, "rm", "-q", "-r", "--cached", ".")

	r := localRepo(dir)
	commits, err := r.Log(git.LogOptions{})
	if err != nil {
		t.Fatalf("Log on orphan branch: %v", err)
	}
	var hashes []string
	for _, c := range commits {
		hashes = append(hashes, c.Hash)
	}
	if !containsHash(hashes, firstHash) || !containsHash(hashes, mainTip) {
		t.Fatalf("orphan Log missing main commits %s/%s: %v", firstHash, mainTip, hashes)
	}

	st, err := r.Status()
	if err != nil {
		t.Fatalf("Status on orphan branch: %v", err)
	}
	if st.Head != "" {
		t.Fatalf("orphan Status.Head = %q, want empty", st.Head)
	}
	if st.Branch != "orphan1" {
		t.Fatalf("orphan Status.Branch = %q, want orphan1", st.Branch)
	}

	refs, err := r.Refs()
	if err != nil {
		t.Fatalf("Refs on orphan branch: %v", err)
	}
	foundMain := false
	for _, b := range refs.Branches {
		if b.Name == "main" {
			foundMain = true
			if b.Current {
				t.Fatalf("main must not be Current on orphan branch: %+v", b)
			}
		}
		if b.Name == "orphan1" {
			t.Fatalf("unborn orphan1 must not exist as a ref: %+v", b)
		}
	}
	if !foundMain {
		t.Fatalf("Refs missing main on orphan branch: %+v", refs.Branches)
	}
}

// TestLogUnbornDetachedOrphanCommit verifies the behavior the HEAD revision
// exists for: in a born repository, a commit no branch points at still shows
// in Log while HEAD is detached at it.
func TestLogUnbornDetachedOrphanCommit(t *testing.T) {
	dir, _ := initCommittedLocal(t, "detached")
	localGit(t, dir, "checkout", "-q", "--detach", "HEAD")
	writeFileLocal(t, dir, "orphan.txt", "orphan work\n")
	localGit(t, dir, "add", "orphan.txt")
	localGit(t, dir, "commit", "-q", "-m", "detached work")
	orphanHash := strings.TrimSpace(localGit(t, dir, "rev-parse", "HEAD"))

	r := localRepo(dir)
	hashes := logHashes(t, r)
	if !containsHash(hashes, orphanHash) {
		t.Fatalf("detached HEAD commit %s missing from Log: %v", orphanHash, hashes)
	}

	// Once HEAD moves back to main the orphan commit drops out of the default
	// walk again: the fallback must not have widened the walk permanently.
	localGit(t, dir, "checkout", "-q", "main")
	hashes = logHashes(t, r)
	if containsHash(hashes, orphanHash) {
		t.Fatalf("orphan commit %s still listed after checkout main: %v", orphanHash, hashes)
	}
}

// TestLogUnbornErrorsNotSwallowed covers every failure path that must keep
// surfacing as an error instead of being misread as an unborn HEAD.
func TestLogUnbornErrorsNotSwallowed(t *testing.T) {
	t.Run("CorruptHEADFile", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "corrupt-head")
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("garbage content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log with corrupt .git/HEAD succeeded, want error")
		}
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "corrupt-ref")
		if err := os.MkdirAll(filepath.Join(dir, ".git", "refs", "heads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("not-a-hash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log with corrupt refs/heads/main text succeeded, want error")
		}
	})

	t.Run("EmptyBranchRefFile", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "empty-ref")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log with empty refs/heads/main succeeded, want error")
		}
	})

	t.Run("MissingCommitObject", func(t *testing.T) {
		dir, head := initCommittedLocal(t, "missing-object")
		objFile := filepath.Join(dir, ".git", "objects", head[:2], head[2:])
		if err := os.Remove(objFile); err != nil {
			t.Fatalf("remove commit object %s: %v", objFile, err)
		}
		// refs/heads/main still points at the now-missing object: HEAD is born
		// but unreadable, so the unborn fallback must not engage and the error
		// must surface.
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log with missing commit object succeeded, want error")
		}
	})

	t.Run("DetachedHeadBogusOID", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "detached-bogus")
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("0123456789012345678901234567890123456789\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log with detached HEAD at bogus OID succeeded, want error")
		}
	})

	t.Run("OrphanWithCorruptOtherBranch", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "orphan-corrupt")
		localGit(t, dir, "checkout", "-q", "--orphan", "orphan1")
		// HEAD is genuinely unborn here, but refs/heads/main is corrupt: the
		// retried walk without HEAD must still fail rather than return an
		// empty or partial list.
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("garbage\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := localRepo(dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log on orphan branch with corrupt refs/heads/main succeeded, want error")
		}
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		if _, err := localRepo(missing).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log in a missing directory succeeded, want error")
		}
	})

	t.Run("NotAGitDirectory", func(t *testing.T) {
		plain := t.TempDir()
		if _, err := localRepo(plain).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log in a non-git directory succeeded, want error")
		}
	})

	t.Run("ExplicitBadRevs", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "bad-revs")
		_, err := localRepo(dir).Log(git.LogOptions{Revs: []string{"nonexistent"}})
		if err == nil {
			t.Fatal("Log with explicit bad Revs succeeded, want error")
		}
	})

	t.Run("ExplicitBadRevsOnEmptyRepo", func(t *testing.T) {
		dir := initEmptyLocal(t, "bad-revs-empty")
		// Even on an unborn HEAD repository, explicit revisions are caller
		// intent: the fallback must not rescue them.
		_, err := localRepo(dir).Log(git.LogOptions{Revs: []string{"nonexistent"}})
		if err == nil {
			t.Fatal("Log with explicit bad Revs on empty repository succeeded, want error")
		}
	})

	t.Run("ExplicitBranchesAndHEADOnEmptyRepo", func(t *testing.T) {
		dir := initEmptyLocal(t, "explicit-head-empty")
		cmdLog := git.NewCommandLog()
		// Explicit Revs containing --branches and HEAD must NOT trigger the
		// default-revs unborn fallback (which would strip HEAD and succeed).
		_, err := localRepo(dir).WithCommandLog(cmdLog).Log(git.LogOptions{Revs: []string{"--branches", "HEAD"}})
		if err == nil {
			t.Fatal("Log with explicit [--branches HEAD] on empty repository succeeded, want error")
		}
		records, _ := cmdLog.Snapshot()
		if len(records) != 1 {
			t.Fatalf("explicit Revs triggered extra commands (%d recorded), want 1: %+v", len(records), records)
		}
	})
}

// sshEmptyRepo creates a truly empty repository (no initial commit) on the
// loopback/configured server, mirroring sshtest.InitRepo's configuration
// minus the commit.
func sshEmptyRepo(t *testing.T, srv *sshtest.Target, name string) string {
	t.Helper()
	dir := srv.TempDir(t, name)
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + git.ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Test'
git config user.email 'test@branchline.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
git config gc.auto 0
git config maintenance.auto false
`
	srv.RunShell(t, script)
	return dir
}

// sshOrphanRepo creates a repository on the server whose main branch has two
// commits and which is checked out on the uncommitted orphan branch, with a
// clean index.
func sshOrphanRepo(t *testing.T, srv *sshtest.Target, name, orphan string) (dir, mainTip string) {
	t.Helper()
	dir = srv.InitRepo(t, name)
	srv.WriteFile(t, dir, "b.txt", []byte("two\n"))
	srv.RunGit(t, dir, "add", "b.txt")
	srv.RunGit(t, dir, "commit", "-q", "-m", "second on main")
	mainTip = srv.RunGit(t, dir, "rev-parse", "refs/heads/main")
	srv.RunGit(t, dir, "checkout", "-q", "--orphan", orphan)
	srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")
	return dir, mainTip
}

// TestLogUnbornEmptyAndOrphanLoopbackSSH runs the empty-repository and
// orphan-branch scenarios over the loopback sshd.
func TestLogUnbornEmptyAndOrphanLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)

	t.Run("EmptyRepo", func(t *testing.T) {
		dir := sshEmptyRepo(t, srv, "logunborn-empty")
		repo := srv.Repo(t, dir)

		commits, err := repo.Log(git.LogOptions{})
		if err != nil {
			t.Fatalf("SSH Log on empty repository: %v", err)
		}
		if len(commits) != 0 {
			t.Fatalf("SSH empty repository Log returned %d commits, want 0", len(commits))
		}
		got, err := repo.Log(git.LogOptions{Query: "anything"})
		if err != nil {
			t.Fatalf("SSH query Log on empty repository: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("SSH query Log on empty repository returned %d commits, want 0", len(got))
		}
		st, err := repo.Status()
		if err != nil {
			t.Fatalf("SSH Status on empty repository: %v", err)
		}
		if st.Head != "" || st.Branch != "main" {
			t.Fatalf("SSH empty Status = head %q branch %q, want empty/main", st.Head, st.Branch)
		}
	})

	t.Run("OrphanBranch", func(t *testing.T) {
		dir, mainTip := sshOrphanRepo(t, srv, "logunborn-orphan", "orphanssh")
		repo := srv.Repo(t, dir)
		initialHash := srv.RunGit(t, dir, "rev-list", "--max-parents=0", "main")

		commits, err := repo.Log(git.LogOptions{})
		if err != nil {
			t.Fatalf("SSH Log on orphan branch: %v", err)
		}
		var hashes []string
		for _, c := range commits {
			hashes = append(hashes, c.Hash)
		}
		if !containsHash(hashes, mainTip) || !containsHash(hashes, initialHash) {
			t.Fatalf("SSH orphan Log missing main commits %s/%s: %v", mainTip, initialHash, hashes)
		}
		st, err := repo.Status()
		if err != nil {
			t.Fatalf("SSH Status on orphan branch: %v", err)
		}
		if st.Head != "" || st.Branch != "orphanssh" {
			t.Fatalf("SSH orphan Status = head %q branch %q, want empty/orphanssh", st.Head, st.Branch)
		}
	})
}

// TestLogUnbornErrorsNotSwallowedLoopbackSSH runs the corrupt/missing failure
// paths over the loopback sshd.
func TestLogUnbornErrorsNotSwallowedLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)

	t.Run("CorruptHEADFile", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-badhead")
		srv.RunShell(t, "printf 'garbage content\\n' > "+git.ShellQuote(dir+"/.git/HEAD"))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log with corrupt .git/HEAD succeeded, want error")
		}
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-badref")
		srv.RunShell(t, "printf 'not-a-hash\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log with corrupt refs/heads/main succeeded, want error")
		}
	})

	t.Run("EmptyBranchRefFile", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-emptyref")
		srv.RunShell(t, ": > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log with empty refs/heads/main succeeded, want error")
		}
	})

	t.Run("MissingCommitObject", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-missingobj")
		head := srv.RunGit(t, dir, "rev-parse", "HEAD")
		objRel := ".git/objects/" + head[:2] + "/" + head[2:]
		srv.RunShell(t, "rm -f -- "+git.ShellQuote(dir+"/"+objRel))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log with missing commit object succeeded, want error")
		}
	})

	t.Run("DetachedHeadBogusOID", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-bogusdetached")
		srv.RunShell(t, "printf '0123456789012345678901234567890123456789\\n' > "+git.ShellQuote(dir+"/.git/HEAD"))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log with detached HEAD at bogus OID succeeded, want error")
		}
	})

	t.Run("OrphanWithCorruptOtherBranch", func(t *testing.T) {
		dir, _ := sshOrphanRepo(t, srv, "logunborn-orphancorrupt", "orphanx")
		srv.RunShell(t, "printf 'garbage\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log on orphan branch with corrupt refs/heads/main succeeded, want error")
		}
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		missing := srv.RootDir + "/logunborn-no-such-dir"
		if _, err := srv.Repo(t, missing).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log in a missing directory succeeded, want error")
		}
	})

	t.Run("NotAGitDirectory", func(t *testing.T) {
		plain := srv.TempDir(t, "logunborn-plain")
		if _, err := srv.Repo(t, plain).Log(git.LogOptions{}); err == nil {
			t.Fatal("SSH Log in a non-git directory succeeded, want error")
		}
	})

	t.Run("ExplicitBadRevs", func(t *testing.T) {
		dir := srv.InitRepo(t, "logunborn-badrevs")
		if _, err := srv.Repo(t, dir).Log(git.LogOptions{Revs: []string{"nonexistent"}}); err == nil {
			t.Fatal("SSH Log with explicit bad Revs succeeded, want error")
		}
	})

	t.Run("SSHFailure", func(t *testing.T) {
		// A host that does not exist exits ssh with 255; ExitCode != 128 means
		// the unborn probe never engages and the error must surface unchanged.
		ex := &git.SSHExecutor{
			Host:           "127.0.0.1",
			Port:           1, // nothing listens on port 1
			User:           srv.User,
			Identity:       srv.Identity,
			Dir:            "/tmp/nowhere",
			SSH:            srv.SSHProgram(),
			NoGlobalConfig: true,
		}
		if _, err := git.New(ex).Log(git.LogOptions{}); err == nil {
			t.Fatal("Log over a dead SSH connection succeeded, want error")
		} else if git.ExitCode(err) != 255 {
			t.Fatalf("dead SSH connection exit code = %d, want 255 (err=%v)", git.ExitCode(err), err)
		}
	})
}

// ---------------------------------------------------------------------------
// HeadState structured probe
// ---------------------------------------------------------------------------

// requireHeadState calls r.HeadState and asserts the exact triple.
func requireHeadState(t *testing.T, r *git.Repo, wantKind git.HEADKind, wantName string) {
	t.Helper()
	kind, name, err := r.HeadState()
	if err != nil {
		t.Fatalf("HeadState error = %v, want kind %v name %q", err, wantKind, wantName)
	}
	if kind != wantKind {
		t.Fatalf("HeadState kind = %v, want %v (name %q)", kind, wantKind, name)
	}
	if name != wantName {
		t.Fatalf("HeadState name = %q, want %q", name, wantName)
	}
}

// requireHeadStateErr asserts HeadState returns a non-nil error and never a
// misread unborn or detached classification.
func requireHeadStateErr(t *testing.T, r *git.Repo) {
	t.Helper()
	kind, name, err := r.HeadState()
	if err == nil {
		t.Fatalf("HeadState succeeded with (kind=%v, name=%q), want error", kind, name)
	}
	if kind != 0 || name != "" {
		t.Fatalf("HeadState error path returned (kind=%v, name=%q), want (0, \"\")", kind, name)
	}
}

// TestHeadStateEmptyRepo verifies the empty-repository classification: HEAD is
// a symbolic-ref to the unborn main, so HeadState is HEADUnborn/main, while a
// sibling branch with a slash in its name keeps the full suffix.
func TestHeadStateEmptyRepo(t *testing.T) {
	dir := initEmptyLocal(t, "hs-empty")
	requireHeadState(t, localRepo(dir), git.HEADUnborn, "main")

	// A slash-bearing unborn branch must keep everything after refs/heads/.
	localGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/feature/topic")
	requireHeadState(t, localRepo(dir), git.HEADUnborn, "feature/topic")
}

// TestHeadStateBornBranch verifies a repository with commits on main is
// HEADBranch/main, and after checking out a second born branch the name tracks
// the checkout.
func TestHeadStateBornBranch(t *testing.T) {
	dir, _ := initCommittedLocal(t, "hs-born")
	requireHeadState(t, localRepo(dir), git.HEADBranch, "main")

	localGit(t, dir, "checkout", "-q", "-b", "feature/x")
	requireHeadState(t, localRepo(dir), git.HEADBranch, "feature/x")
}

// TestHeadStateOrphanBranch verifies a repository with existing commits whose
// HEAD is an uncommitted --orphan branch is HEADUnborn for the orphan name.
func TestHeadStateOrphanBranch(t *testing.T) {
	dir, _ := initCommittedLocal(t, "hs-orphan")
	localGit(t, dir, "checkout", "-q", "--orphan", "orphan1")
	localGit(t, dir, "rm", "-q", "-r", "--cached", ".")
	requireHeadState(t, localRepo(dir), git.HEADUnborn, "orphan1")
}

// TestHeadStateDetached verifies a repository checked out at a commit is
// HEADDetached with an empty branch name.
func TestHeadStateDetached(t *testing.T) {
	dir, head := initCommittedLocal(t, "hs-detached")
	localGit(t, dir, "checkout", "-q", head)
	requireHeadState(t, localRepo(dir), git.HEADDetached, "")
}

// TestHeadStateCorruptNotMisread covers every corrupt or missing shape that
// must surface as an error instead of being misread as unborn or detached.
func TestHeadStateCorruptNotMisread(t *testing.T) {
	t.Run("CorruptHEADFile", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-corrupt-head")
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("garbage content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-corrupt-ref")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("not-a-hash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("EmptyBranchRefFile", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-empty-ref")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("ZeroOIDBranchRef", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-zero-ref")
		if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("0000000000000000000000000000000000000000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("MissingCommitObject", func(t *testing.T) {
		dir, head := initCommittedLocal(t, "hs-missing-obj")
		objFile := filepath.Join(dir, ".git", "objects", head[:2], head[2:])
		if err := os.Remove(objFile); err != nil {
			t.Fatalf("remove commit object %s: %v", objFile, err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("DetachedHeadBogusOID", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-bogus-detached")
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("0123456789012345678901234567890123456789\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("CorruptPackedRefs", func(t *testing.T) {
		dir, _ := initCommittedLocal(t, "hs-corrupt-packed-refs")
		localGit(t, dir, "pack-refs", "--all")
		if err := os.WriteFile(filepath.Join(dir, ".git", "packed-refs"), []byte("corrupt packed-refs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		requireHeadStateErr(t, localRepo(dir))
	})

	t.Run("NotAGitDirectory", func(t *testing.T) {
		requireHeadStateErr(t, localRepo(t.TempDir()))
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		requireHeadStateErr(t, localRepo(filepath.Join(t.TempDir(), "no-such-dir")))
	})
}

// TestHeadStateLoopbackSSH runs the classification matrix over the loopback
// sshd: empty and orphan repositories are unborn, a born branch and a detached
// checkout classify correctly, and corrupt shapes plus a dead connection are
// errors — never a misread unborn or detached state.
func TestHeadStateLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)

	t.Run("EmptyRepo", func(t *testing.T) {
		dir := sshEmptyRepo(t, srv, "hs-empty")
		requireHeadState(t, srv.Repo(t, dir), git.HEADUnborn, "main")
	})

	t.Run("OrphanBranch", func(t *testing.T) {
		dir, _ := sshOrphanRepo(t, srv, "hs-orphan", "orphanssh")
		requireHeadState(t, srv.Repo(t, dir), git.HEADUnborn, "orphanssh")
	})

	t.Run("BornBranch", func(t *testing.T) {
		dir := srv.InitRepo(t, "hs-born")
		requireHeadState(t, srv.Repo(t, dir), git.HEADBranch, "main")
	})

	t.Run("Detached", func(t *testing.T) {
		dir := srv.InitRepo(t, "hs-detached")
		head := srv.RunGit(t, dir, "rev-parse", "HEAD")
		srv.RunGit(t, dir, "checkout", "-q", head)
		requireHeadState(t, srv.Repo(t, dir), git.HEADDetached, "")
	})

	t.Run("CorruptBranchRefText", func(t *testing.T) {
		dir := srv.InitRepo(t, "hs-corrupt-ref")
		srv.RunShell(t, "printf 'not-a-hash\\n' > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		requireHeadStateErr(t, srv.Repo(t, dir))
	})

	t.Run("EmptyBranchRefFile", func(t *testing.T) {
		dir := srv.InitRepo(t, "hs-empty-ref")
		srv.RunShell(t, ": > "+git.ShellQuote(dir+"/.git/refs/heads/main"))
		requireHeadStateErr(t, srv.Repo(t, dir))
	})

	t.Run("DetachedHeadBogusOID", func(t *testing.T) {
		dir := srv.InitRepo(t, "hs-bogus-detached")
		srv.RunShell(t, "printf '0123456789012345678901234567890123456789\\n' > "+git.ShellQuote(dir+"/.git/HEAD"))
		requireHeadStateErr(t, srv.Repo(t, dir))
	})

	t.Run("MissingDirectory", func(t *testing.T) {
		requireHeadStateErr(t, srv.Repo(t, srv.RootDir+"/hs-no-such-dir"))
	})

	t.Run("SSHFailure", func(t *testing.T) {
		ex := &git.SSHExecutor{
			Host:           "127.0.0.1",
			Port:           1, // nothing listens on port 1
			User:           srv.User,
			Identity:       srv.Identity,
			Dir:            "/tmp/nowhere",
			SSH:            srv.SSHProgram(),
			NoGlobalConfig: true,
		}
		requireHeadStateErr(t, git.New(ex))
	})
}
