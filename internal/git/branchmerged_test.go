package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BranchMerged reports true for a branch whose commits HEAD contains, and
// false for a branch with commits HEAD does not contain.
func TestBranchMergedMergedAndUnmerged(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	// merged sits at the same commit as main: an ancestor of HEAD.
	gitRun(t, dir, "branch", "merged")

	// unmerged gains a commit main does not have.
	gitRun(t, dir, "checkout", "-q", "-b", "unmerged")
	writeFile(t, dir, "b.txt", "two\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "unmerged work")
	gitRun(t, dir, "checkout", "-q", "main")

	r := New(&LocalExecutor{Dir: dir})

	merged, err := r.BranchMerged("merged")
	if err != nil {
		t.Fatalf("BranchMerged(merged) error: %v", err)
	}
	if !merged {
		t.Fatal("BranchMerged(merged) = false, want true")
	}

	unmerged, err := r.BranchMerged("unmerged")
	if err != nil {
		t.Fatalf("BranchMerged(unmerged) error: %v", err)
	}
	if unmerged {
		t.Fatal("BranchMerged(unmerged) = true, want false")
	}
}

// BranchMerged reports true after the branch was merged into HEAD, not only
// when it points at a commit HEAD already had.
func TestBranchMergedAfterMerge(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "b.txt", "two\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "feature work")
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "merge", "-q", "--no-ff", "feature", "-m", "merge feature")

	r := New(&LocalExecutor{Dir: dir})
	merged, err := r.BranchMerged("feature")
	if err != nil {
		t.Fatalf("BranchMerged(feature) error: %v", err)
	}
	if !merged {
		t.Fatal("BranchMerged(feature) = false after merge, want true")
	}
}

// BranchMerged returns an error for a branch that does not exist rather
// than reporting it merged or not merged.
func TestBranchMergedMissingBranch(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.BranchMerged("no-such-branch"); err == nil {
		t.Fatal("BranchMerged(no-such-branch) = nil error, want error")
	}
	if _, err := r.BranchMerged("  "); err == nil {
		t.Fatal("BranchMerged(blank) = nil error, want error")
	}
}

// BranchMerged returns an error when the repository has no HEAD commit yet.
func TestBranchMergedUnbornHEAD(t *testing.T) {
	dir := initRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.BranchMerged("main"); err == nil {
		t.Fatal("BranchMerged on unborn HEAD = nil error, want error")
	}
}

// SafeDeleteBranch deletes a merged branch with git branch -d and refuses,
// without deleting, when the merged check failed, when the branch is
// unmerged without force, or when the branch is checked out.
func TestBranchMergedSafeDeleteProtections(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "branch", "merged")
	gitRun(t, dir, "checkout", "-q", "-b", "unmerged")
	writeFile(t, dir, "b.txt", "two\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "unmerged work")
	gitRun(t, dir, "checkout", "-q", "main")

	r := New(&LocalExecutor{Dir: dir})
	hasBranch := func(name string) bool {
		out := gitOut(t, dir, "branch", "--list", name)
		return strings.TrimSpace(out) != ""
	}

	// A merged branch is deleted.
	if err := r.SafeDeleteBranch("merged", false, true, nil); err != nil {
		t.Fatalf("SafeDeleteBranch(merged) error: %v", err)
	}
	if hasBranch("merged") {
		t.Fatal("merged branch survived a safe delete")
	}

	// An unknown merged state never deletes, even with force.
	gitRun(t, dir, "branch", "unknown")
	checkErr := errString("simulated merge-base failure")
	if err := r.SafeDeleteBranch("unknown", true, false, checkErr); err == nil {
		t.Fatal("SafeDeleteBranch with failed check = nil error, want error")
	}
	if !hasBranch("unknown") {
		t.Fatal("unknown branch was deleted even though the merged check failed")
	}

	// An unmerged branch without force is refused and survives.
	if err := r.SafeDeleteBranch("unmerged", false, false, nil); err == nil {
		t.Fatal("SafeDeleteBranch(unmerged, no force) = nil error, want error")
	}
	if !hasBranch("unmerged") {
		t.Fatal("unmerged branch was deleted without force")
	}

	// The checked-out branch is refused and survives even with force.
	if err := r.SafeDeleteBranch("main", true, true, nil); err == nil {
		t.Fatal("SafeDeleteBranch(current) = nil error, want error")
	}
	if !hasBranch("main") {
		t.Fatal("current branch was deleted")
	}

	// Forcing an unmerged branch with a successful check deletes it.
	if err := r.SafeDeleteBranch("unmerged", true, false, nil); err != nil {
		t.Fatalf("SafeDeleteBranch(unmerged, force) error: %v", err)
	}
	if hasBranch("unmerged") {
		t.Fatal("unmerged branch survived a confirmed force delete")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// writeDashBranchRef creates the branch refs/heads/-dash by writing the ref
// file directly: git branch refuses option-like names, but the ref store
// holds any name, and a repository can arrive with one already inside.
func writeDashBranchRef(t *testing.T, dir, name string) {
	t.Helper()
	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", name), []byte(head+"\n"), 0o644); err != nil {
		t.Fatalf("write ref file for %s: %v", name, err)
	}
}

// A branch whose name starts with "-" is checked and deleted as a branch,
// never read as command-line options: BranchMerged finds it through
// show-ref, and SafeDeleteBranch passes the name behind "--" so git branch
// does not mistake it for flags like -v or -l.
func TestBranchMergedDashNamedBranch(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	writeDashBranchRef(t, dir, "-dash")

	r := New(&LocalExecutor{Dir: dir})
	merged, err := r.BranchMerged("-dash")
	if err != nil {
		t.Fatalf("BranchMerged(-dash) error: %v", err)
	}
	if !merged {
		t.Fatal("BranchMerged(-dash) = false, want true: it points at HEAD")
	}

	if err := r.SafeDeleteBranch("-dash", false, true, nil); err != nil {
		t.Fatalf("SafeDeleteBranch(-dash) error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "-dash")); !os.IsNotExist(err) {
		t.Fatal("-dash ref file survived a safe delete: the name was treated as options")
	}
}

// A dash-named branch with unmerged commits reports unmerged, and a forcing
// delete still removes it instead of tripping over its name.
func TestBranchMergedDashNamedUnmergedForceDelete(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "realwork")
	writeFile(t, dir, "b.txt", "two\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "unmerged work")
	// Rename realwork's ref to the dash name by moving the ref file.
	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "-work"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatalf("write ref file for -work: %v", err)
	}
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "branch", "-D", "realwork")

	r := New(&LocalExecutor{Dir: dir})
	merged, err := r.BranchMerged("-work")
	if err != nil {
		t.Fatalf("BranchMerged(-work) error: %v", err)
	}
	if merged {
		t.Fatal("BranchMerged(-work) = true, want false: it has commits HEAD lacks")
	}

	// Non-forcing delete is refused; the ref survives.
	if err := r.SafeDeleteBranch("-work", false, false, nil); err == nil {
		t.Fatal("SafeDeleteBranch(-work, no force) = nil error, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "-work")); err != nil {
		t.Fatal("-work ref file is gone after a refused delete")
	}

	// Forcing deletes it.
	if err := r.SafeDeleteBranch("-work", true, false, nil); err != nil {
		t.Fatalf("SafeDeleteBranch(-work, force) error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "heads", "-work")); !os.IsNotExist(err) {
		t.Fatal("-work ref file survived a confirmed force delete")
	}
}

// mergeScriptExecutor is a fake Executor that answers show-ref and rev-parse
// like a healthy repository but fails merge-base --is-ancestor with the
// scripted error, to prove BranchMerged never swallows an unknown answer as
// "not merged".
type mergeScriptExecutor struct {
	mergeErr error
	calls    [][]string
}

func (e *mergeScriptExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.calls = append(e.calls, append([]string(nil), args...))
	switch args[0] {
	case "show-ref", "rev-parse":
		return []byte("ok\n"), nil
	case "merge-base":
		return nil, e.mergeErr
	}
	return nil, fmt.Errorf("unexpected git %s", args[0])
}

func (e *mergeScriptExecutor) Describe() string { return "merge-script fake" }

// When merge-base --is-ancestor fails with exit code 128, 255, or with an
// error that is not a git exit status at all, BranchMerged returns that
// failure as an error — never (false, nil), which callers would read as a
// license to treat the branch as safely unmerged-checked.
func TestBranchMergedMergeBaseFailureIsError(t *testing.T) {
	cases := []struct {
		name     string
		mergeErr error
	}{
		{"exit128", &Error{Args: []string{"merge-base"}, ExitCode: 128, Stderr: "fatal: Not a valid object name"}},
		{"exit255", &Error{Args: []string{"merge-base"}, ExitCode: 255, Stderr: "ssh: connection lost"}},
		{"nonGitError", errString("executor transport broke")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &mergeScriptExecutor{mergeErr: tc.mergeErr}
			r := New(ex)
			merged, err := r.BranchMerged("feature")
			if err == nil {
				t.Fatalf("BranchMerged = (%v, nil), want non-nil error for %s", merged, tc.name)
			}
			if merged {
				t.Fatalf("BranchMerged = (true, %v), want merged=false on failure", err)
			}
		})
	}
}

// BranchMerged does not accept revision suffixes as branch names:
// refs/heads/main~1 is a revspec rev-parse would resolve, not a branch, so
// the exact-ref verification must fail it.
func TestBranchMergedRevspecIsNotABranch(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "a.txt", "two\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "second")

	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.BranchMerged("main~1"); err == nil {
		t.Fatal("BranchMerged(main~1) = nil error, want error: a revspec is not a branch")
	}
}
