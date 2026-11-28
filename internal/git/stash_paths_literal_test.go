package git_test

// Domain-level tests: StashPaths treats its paths literally, so a file
// whose name holds glob characters or pathspec magic is stashed by exactly
// that name and never sweeps up the files a pattern would match.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initStashLiteralRepo creates a repository on the initial branch main with
// its own committer identity and returns its directory.
func initStashLiteralRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	localGit(t, dir, "init", "-q", "-b", "main")
	localGit(t, dir, "config", "user.name", "Stash Tester")
	localGit(t, dir, "config", "user.email", "stash@branchline.invalid")
	return dir
}

// readStashFile reads one file of the repository at dir.
func readStashFile(t *testing.T, dir, relPath string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return string(body)
}

// checkStashLiteralPath runs the one scenario shared by the glob and the
// pathspec-magic names: both the tricky file and its glob sibling are
// modified, stashing the tricky one must take it alone, restore its
// committed content, and leave the sibling's change in the working tree.
func checkStashLiteralPath(t *testing.T, dir, tricky, sibling string) {
	t.Helper()
	writeFileLocal(t, dir, tricky, "one\n")
	writeFileLocal(t, dir, sibling, "one\n")
	localGit(t, dir, "--literal-pathspecs", "add", "--", tricky, sibling)
	localGit(t, dir, "commit", "-q", "-m", "base")
	writeFileLocal(t, dir, tricky, "changed\n")
	writeFileLocal(t, dir, sibling, "changed\n")

	r := localRepo(dir)
	if err := r.StashPaths("literal", []string{tricky}); err != nil {
		t.Fatalf("StashPaths(%q): %v", tricky, err)
	}

	if body := readStashFile(t, dir, sibling); body != "changed\n" {
		t.Fatalf("%s after the stash = %q, want its change still in the working tree", sibling, body)
	}
	if body := readStashFile(t, dir, tricky); body != "one\n" {
		t.Fatalf("%s after the stash = %q, want the committed content restored", tricky, body)
	}
	shown := localGit(t, dir, "stash", "show", "--name-status", "stash@{0}")
	var stashed []string
	for _, line := range strings.Split(shown, "\n") {
		if _, name, ok := strings.Cut(line, "\t"); ok {
			stashed = append(stashed, name)
		}
	}
	if len(stashed) != 1 || stashed[0] != tricky {
		t.Fatalf("stash entry holds %v, want exactly [%s]", stashed, tricky)
	}
	if status := localGit(t, dir, "--literal-pathspecs", "status", "--porcelain", "--", tricky); status != "" {
		t.Fatalf("status of %q after the stash = %q, want clean", tricky, status)
	}
}

// Stashing a path whose name is a glob pattern takes exactly that file:
// ab.txt, which the pattern would match as a wildcard, keeps its change.
func TestStashPathsLiteralGlob(t *testing.T) {
	dir := initStashLiteralRepo(t, "stash-literal-glob")
	checkStashLiteralPath(t, dir, "a*.txt", "ab.txt")
}

// Stashing a path whose name opens with pathspec magic takes exactly that
// file: the magic is not read as a directive, and the plain x.txt a magic
// reading would resolve to keeps its change.
func TestStashPathsLiteralMagic(t *testing.T) {
	dir := initStashLiteralRepo(t, "stash-literal-magic")
	checkStashLiteralPath(t, dir, ":(top)x.txt", "x.txt")
}
