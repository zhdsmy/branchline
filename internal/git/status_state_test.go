package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// stateGitStop runs a command expected to stop midway: any exit code is
// fine as long as the repository ends in the state the caller then reads.
func stateGitStop(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	_ = cmd.Run()
}

// stateRepo opens dir locally and over loopback SSH.
func stateRepos(t *testing.T, srv *sshServer, dir string) map[string]*Repo {
	t.Helper()
	return map[string]*Repo{
		"local": New(&LocalExecutor{Dir: dir, NoGlobalConfig: true}),
		"ssh":   New(srv.executor(t, dir)),
	}
}

func assertStatusState(t *testing.T, srv *sshServer, dir, state, stopped string, conflicted bool) {
	t.Helper()
	for where, r := range stateRepos(t, srv, dir) {
		st, err := r.Status()
		if err != nil {
			t.Fatalf("%s: Status: %v", where, err)
		}
		if st.State != state {
			t.Errorf("%s: State = %q, want %q", where, st.State, state)
		}
		if st.Stopped != stopped {
			t.Errorf("%s: Stopped = %q, want %q", where, st.Stopped, stopped)
		}
		if st.Conflicted() != conflicted {
			t.Errorf("%s: Conflicted = %v, want %v", where, st.Conflicted(), conflicted)
		}
	}
}

// stateMergeBase builds main and feature changing the same file in
// different ways, so merging feature into main conflicts.
func stateMergeBase(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "theirs\n")
	gitRun(t, dir, "commit", "-q", "-am", "feature")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "ours\n")
	gitRun(t, dir, "commit", "-q", "-am", "main")
	return dir
}

// The state read prunes every path, so the states it recognizes must come
// from the lines git prints regardless of the file list. Each operation
// below is driven into its stopped state and read back locally and over
// loopback SSH. The conflicted flag reads from the porcelain pass, which
// is unaffected by the pruning, so a stopped merge or rebase still knows
// its conflicts.
func TestStatusStateInProgressOperations(t *testing.T) {
	srv := startSSHD(t)

	t.Run("merge with conflicts", func(t *testing.T) {
		dir := stateMergeBase(t)
		stateGitStop(t, dir, "merge", "feature")
		assertStatusState(t, srv, dir, "merge", "", true)
	})

	t.Run("merge conflicts resolved not committed", func(t *testing.T) {
		dir := stateMergeBase(t)
		stateGitStop(t, dir, "merge", "feature")
		writeFile(t, dir, "a.txt", "resolved\n")
		gitRun(t, dir, "add", "a.txt")
		assertStatusState(t, srv, dir, "merge", "", false)
	})

	t.Run("rebase conflict", func(t *testing.T) {
		dir := stateMergeBase(t)
		gitRun(t, dir, "checkout", "-q", "feature")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		plan, err := r.RebasePlan("main")
		must(t, err)
		if err := r.RebaseWith("main", plan); err != nil {
			t.Fatalf("rebase did not stop: %v", err)
		}
		// The conflicted flag decides the conflict stop reason through
		// rebaseStop; it comes from the porcelain pass, so the pruned
		// state read must not lose it.
		assertStatusState(t, srv, dir, "rebase", "conflict", true)
	})

	t.Run("interactive rebase stopped on edit", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "base")
		writeFile(t, dir, "a.txt", "one\ntwo\n")
		gitRun(t, dir, "commit", "-q", "-am", "second")
		writeFile(t, dir, "c.txt", "see\n")
		gitRun(t, dir, "add", "c.txt")
		gitRun(t, dir, "commit", "-q", "-m", "third")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		plan, err := r.RebasePlan("HEAD~1")
		must(t, err)
		if len(plan) != 1 {
			t.Fatalf("plan %+v, want one step", plan)
		}
		plan[0].Action = "edit"
		must(t, r.RebaseWith("HEAD~1", plan))
		assertStatusState(t, srv, dir, "rebase", "edit", false)
	})

	t.Run("cherry-pick conflict", func(t *testing.T) {
		dir := stateMergeBase(t)
		stateGitStop(t, dir, "cherry-pick", "feature")
		assertStatusState(t, srv, dir, "cherry-pick", "", true)
	})

	t.Run("revert conflict", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "one")
		writeFile(t, dir, "a.txt", "two\n")
		gitRun(t, dir, "commit", "-q", "-am", "two")
		writeFile(t, dir, "a.txt", "three\n")
		gitRun(t, dir, "commit", "-q", "-am", "three")
		// Reverting "two" means rewriting the line back to one, but the
		// commit after it already holds three, so the revert conflicts.
		stateGitStop(t, dir, "revert", "--no-edit", "HEAD~1")
		assertStatusState(t, srv, dir, "revert", "", true)
	})

	t.Run("am stopped", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "one")
		writeFile(t, dir, "a.txt", "one\ntwo\n")
		gitRun(t, dir, "commit", "-q", "-am", "two")
		gitRun(t, dir, "format-patch", "-q", "-1", "HEAD")
		gitRun(t, dir, "reset", "-q", "--hard", "HEAD~1")
		writeFile(t, dir, "a.txt", "uno\ndos\n")
		gitRun(t, dir, "commit", "-q", "-am", "diverged")
		mail, err := filepath.Glob(filepath.Join(dir, "0001-*.patch"))
		must(t, err)
		if len(mail) != 1 {
			t.Fatalf("patch files %v, want one", mail)
		}
		stateGitStop(t, dir, "am", mail[0])
		assertStatusState(t, srv, dir, "am", "", false)
	})

	t.Run("bisect in progress", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "one")
		for i := 0; i < 4; i++ {
			writeFile(t, dir, "a.txt", "one\n"+string(rune('a'+i))+"\n")
			gitRun(t, dir, "commit", "-q", "-am", "more")
		}
		gitRun(t, dir, "bisect", "start")
		gitRun(t, dir, "bisect", "bad")
		gitRun(t, dir, "bisect", "good", "HEAD~4")
		assertStatusState(t, srv, dir, "bisect", "", false)
	})

	t.Run("no operation", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "one\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "one")
		assertStatusState(t, srv, dir, "", "", false)
	})

	// The state read passes literal ".git" under --literal-pathspecs so no
	// tracked file can ever match it, even one whose name spells pathspec
	// magic or contains an operation keyword. One repository carries a
	// file named ":(exclude)*" through a merge conflict and another carries
	// modified ":(exclude)*/bisect.txt" and ".gitignore" with no operation
	// in progress: neither may confuse the state read or vanish from the
	// porcelain pass.
	t.Run("file named like the pathspec magic", func(t *testing.T) {
		dir := stateMergeBase(t)
		writeFile(t, dir, ":(exclude)*", "odd name\n")
		// The name itself is pathspec magic to git, so it takes a ./ prefix
		// to mean the file.
		gitRun(t, dir, "add", "./:(exclude)*")
		gitRun(t, dir, "commit", "-q", "-m", "odd name")
		// Modified but not staged, so the porcelain pass lists it and shows
		// the state pass cannot swallow it either way.
		writeFile(t, dir, ":(exclude)*", "odd name\nmore\n")
		stateGitStop(t, dir, "merge", "feature")
		for where, r := range stateRepos(t, srv, dir) {
			st, err := r.Status()
			must(t, err)
			if st.State != "merge" || !st.Conflicted() {
				t.Errorf("%s: state %q conflicted %v, want merge with conflicts", where, st.State, st.Conflicted())
			}
			found := false
			for _, f := range st.Unstaged {
				found = found || f.Path == ":(exclude)*"
			}
			for _, f := range st.Staged {
				found = found || f.Path == ":(exclude)*"
			}
			if !found {
				t.Errorf("%s: the oddly named file vanished from the porcelain pass: %+v", where, st)
			}
		}
	})

	t.Run("keyword path and dotgit prefix files never match state pathspec", func(t *testing.T) {
		dir := initRepo(t)
		must(t, os.MkdirAll(filepath.Join(dir, ":(exclude)*"), 0o755))
		writeFile(t, dir, ":(exclude)*/bisect.txt", "one\n")
		writeFile(t, dir, ".gitignore", "*.tmp\n")
		writeFile(t, dir, "merging.txt", "one\n")
		gitRun(t, dir, "add", "./:(exclude)*/bisect.txt", ".gitignore", "merging.txt")
		gitRun(t, dir, "commit", "-q", "-m", "init")
		writeFile(t, dir, ":(exclude)*/bisect.txt", "two\n")
		writeFile(t, dir, ".gitignore", "*.tmp\n*.log\n")
		writeFile(t, dir, "merging.txt", "two\n")
		assertStatusState(t, srv, dir, "", "", false)
	})
}
