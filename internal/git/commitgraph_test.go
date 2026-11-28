package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// commitGraphGit runs one git command in dir with the configuration files
// out of the way, as the other tests of this package do.
func commitGraphGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitGraphRepo makes a repository of n linear commits through one
// fast-import stream, fast enough for the counts these tests need.
func commitGraphRepo(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	commitGraphGit(t, dir, "init", "-q", "-b", "main")
	commitGraphGit(t, dir, "config", "user.name", "Ada")
	commitGraphGit(t, dir, "config", "user.email", "ada@example.com")
	// The write must work out of the box whatever the machine's global
	// configuration says about commit-graphs; only the tests of the off
	// switches set them false.
	commitGraphGit(t, dir, "config", "core.commitGraph", "true")
	commitGraphGit(t, dir, "config", "gc.writeCommitGraph", "true")

	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Ada <ada@example.com> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Ada <ada@example.com> %d +0000\n", 1700000000+i)
		msg := fmt.Sprintf("commit %d", i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i > 1 {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline filler.txt\ndata %d\n%d\n\n", len(fmt.Sprintf("%d\n", i)), i)
	}
	cmd := exec.Command("git", "-C", dir, "fast-import", "--quiet")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	return dir
}

// commitGraphFile is where a plain commit-graph write lands in the main
// repository, for assertions about the file itself.
func commitGraphFile(dir string) string {
	return filepath.Join(dir, ".git", "objects", "info", "commit-graph")
}

func commitGraphExists(dir string) bool {
	_, err := os.Stat(commitGraphFile(dir))
	return err == nil
}

func localRepo(dir string) *Repo { return New(&LocalExecutor{Dir: dir}) }

// TestCommitGraphWritesAtThreshold checks that a repository whose reachable
// commits reach the threshold gets a commit-graph, the call reports the
// write, and the result passes git's own verification.
func TestCommitGraphWritesAtThreshold(t *testing.T) {
	dir := commitGraphRepo(t, 300)
	r := localRepo(dir)

	wrote, err := r.WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false for a history above the threshold")
	}
	if !commitGraphExists(dir) {
		t.Fatal("commit-graph file missing after a reported write")
	}
	if err := r.Run("commit-graph", "verify"); err != nil {
		t.Fatalf("git commit-graph verify: %v", err)
	}
}

// TestCommitGraphSkipsExisting checks that a repository which already has a
// commit-graph is left exactly as it is: the call reports no write and the
// file is not even touched.
func TestCommitGraphSkipsExisting(t *testing.T) {
	dir := commitGraphRepo(t, 300)
	r := localRepo(dir)
	if wrote, err := r.WriteCommitGraphIfLarge(200); err != nil || !wrote {
		t.Fatalf("first write: wrote=%v err=%v", wrote, err)
	}

	before, err := os.Stat(commitGraphFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	// The file's modification time proves the rewrite; make sure a rewrite
	// could not land inside the same clock tick as the first write.
	time.Sleep(20 * time.Millisecond)

	wrote, err := r.WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true although a commit-graph already exists")
	}
	after, err := os.Stat(commitGraphFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("commit-graph rewritten: mtime %v -> %v", before.ModTime(), after.ModTime())
	}
}

// TestCommitGraphSkipsSmallHistory checks that fewer reachable commits than
// the threshold write nothing.
func TestCommitGraphSkipsSmallHistory(t *testing.T) {
	dir := commitGraphRepo(t, 150)
	r := localRepo(dir)

	wrote, err := r.WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true for a history below the threshold")
	}
	if commitGraphExists(dir) {
		t.Fatal("commit-graph file exists although the history is too small")
	}
}

// TestCommitGraphSkipsShallow checks that a shallow clone writes nothing
// even when its reachable commits pass the threshold.
func TestCommitGraphSkipsShallow(t *testing.T) {
	full := commitGraphRepo(t, 300)
	dir := filepath.Join(t.TempDir(), "shallow")
	commitGraphGit(t, t.TempDir(), "clone", "-q", "--depth", "250", "file://"+full, dir)

	if out := commitGraphGit(t, dir, "rev-parse", "--is-shallow-repository"); out != "true" {
		t.Fatalf("fixture is not shallow: %q", out)
	}
	if count := commitGraphGit(t, dir, "rev-list", "--count", "--all"); count != "250" {
		t.Fatalf("fixture commit count = %q, want 250 (above the threshold of 200)", count)
	}

	wrote, err := localRepo(dir).WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true for a shallow clone")
	}
	if commitGraphExists(dir) {
		t.Fatal("commit-graph file exists in a shallow clone")
	}
}

// TestCommitGraphRespectsConfig checks that either switch the user has for
// commit-graphs — core.commitGraph or gc.writeCommitGraph — turned off
// writes nothing.
func TestCommitGraphRespectsConfig(t *testing.T) {
	for _, key := range []string{"core.commitGraph", "gc.writeCommitGraph"} {
		t.Run(key, func(t *testing.T) {
			dir := commitGraphRepo(t, 300)
			commitGraphGit(t, dir, "config", key, "false")

			wrote, err := localRepo(dir).WriteCommitGraphIfLarge(200)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if wrote {
				t.Fatalf("wrote = true although %s is false", key)
			}
			if commitGraphExists(dir) {
				t.Fatalf("commit-graph file exists although %s is false", key)
			}
		})
	}
}

// TestCommitGraphLinkedWorktree checks that calling from a linked worktree
// writes the commit-graph into the shared objects folder of the main
// repository, where every worktree reads it.
func TestCommitGraphLinkedWorktree(t *testing.T) {
	dir := commitGraphRepo(t, 300)
	worktree := filepath.Join(t.TempDir(), "linked")
	commitGraphGit(t, dir, "worktree", "add", "--detach", worktree, "main")

	wrote, err := localRepo(worktree).WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false when called from a linked worktree")
	}
	if !commitGraphExists(dir) {
		t.Fatal("commit-graph missing in the main repository's objects/info")
	}
}

// TestCommitGraphSkipsSplitChain checks that a repository which already has
// a split commit-graph chain — no single commit-graph file — is recognized
// as covered and left alone.
func TestCommitGraphSkipsSplitChain(t *testing.T) {
	dir := commitGraphRepo(t, 300)
	commitGraphGit(t, dir, "commit-graph", "write", "--reachable", "--split")
	chain := filepath.Join(dir, ".git", "objects", "info", "commit-graphs", "commit-graph-chain")
	if _, err := os.Stat(chain); err != nil {
		t.Fatalf("fixture has no split chain: %v", err)
	}
	if commitGraphExists(dir) {
		t.Fatal("fixture unexpectedly has a single-file commit-graph")
	}

	wrote, err := localRepo(dir).WriteCommitGraphIfLarge(200)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true although a split commit-graph chain exists")
	}
	if commitGraphExists(dir) {
		t.Fatal("a single-file commit-graph appeared next to the split chain")
	}
}
