package git_test

// Domain-level tests: StashPaths treats its paths literally, so a file
// whose name holds glob characters or pathspec magic is stashed by exactly
// that name and never sweeps up the files a pattern would match.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
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

// Stashing a plain staged deletion or a staged rename clears the chosen paths
// cleanly without leaving them behind, and applying the stash back restores
// the contents with each deletion returning as unstaged rather than staged.
func TestStashPathsStagedDeletionAndRenameRoundTrip(t *testing.T) {
	t.Run("staged deletion", func(t *testing.T) {
		dir := initStashLiteralRepo(t, "stash-staged-del")
		writeFileLocal(t, dir, "keep.txt", "keep base\n")
		writeFileLocal(t, dir, "del.txt", "deleted body\n")
		localGit(t, dir, "add", "--", "keep.txt", "del.txt")
		localGit(t, dir, "commit", "-q", "-m", "base")

		writeFileLocal(t, dir, "keep.txt", "keep modified\n")
		localGit(t, dir, "rm", "-q", "--", "del.txt")

		r := localRepo(dir)
		if err := r.StashPaths("staged del", []string{"del.txt"}); err != nil {
			t.Fatalf("StashPaths(del.txt): %v", err)
		}
		if got := readStashFile(t, dir, "keep.txt"); got != "keep modified\n" {
			t.Fatalf("keep.txt after stash = %q, want unstaged change kept", got)
		}
		if got := readStashFile(t, dir, "del.txt"); got != "deleted body\n" {
			t.Fatalf("del.txt after stash = %q, want committed content restored", got)
		}
		if st := strings.TrimSpace(localGit(t, dir, "status", "--porcelain", "--", "del.txt")); st != "" {
			t.Fatalf("status of del.txt after stash = %q, want clean", st)
		}

		if err := r.StashApply("stash@{0}", false); err != nil {
			t.Fatalf("StashApply: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "del.txt")); !os.IsNotExist(err) {
			t.Fatalf("del.txt exists after StashApply (err=%v)", err)
		}
		// Unstaging the deletion before the partial push means applying back
		// restores it as an unstaged deletion (" D"), not staged ("D ").
		if st := strings.TrimRight(localGit(t, dir, "status", "--porcelain", "--", "del.txt"), "\r\n"); st != " D del.txt" {
			t.Fatalf("status of del.txt after StashApply = %q, want %q", st, " D del.txt")
		}
		if got := readStashFile(t, dir, "keep.txt"); got != "keep modified\n" {
			t.Fatalf("keep.txt after StashApply = %q, want unstaged change intact", got)
		}
	})

	t.Run("staged rename", func(t *testing.T) {
		dir := initStashLiteralRepo(t, "stash-staged-rename")
		writeFileLocal(t, dir, "old.txt", "moved body\n")
		writeFileLocal(t, dir, "other.txt", "other base\n")
		localGit(t, dir, "add", "--", "old.txt", "other.txt")
		localGit(t, dir, "commit", "-q", "-m", "base")

		writeFileLocal(t, dir, "other.txt", "other modified\n")
		localGit(t, dir, "mv", "old.txt", "renamed.txt")

		r := localRepo(dir)
		if err := r.StashPaths("staged rename", []string{"old.txt", "renamed.txt"}); err != nil {
			t.Fatalf("StashPaths(rename): %v", err)
		}
		if got := readStashFile(t, dir, "old.txt"); got != "moved body\n" {
			t.Fatalf("old.txt after stash = %q, want committed content restored", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "renamed.txt")); !os.IsNotExist(err) {
			t.Fatalf("renamed.txt exists after stash (err=%v)", err)
		}
		if st := strings.TrimSpace(localGit(t, dir, "status", "--porcelain", "--", "old.txt", "renamed.txt")); st != "" {
			t.Fatalf("status of rename paths after stash = %q, want clean", st)
		}

		if err := r.StashApply("stash@{0}", false); err != nil {
			t.Fatalf("StashApply: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
			t.Fatalf("old.txt exists after StashApply (err=%v)", err)
		}
		if got := readStashFile(t, dir, "renamed.txt"); got != "moved body\n" {
			t.Fatalf("renamed.txt after StashApply = %q, want exact renamed body", got)
		}
		if st := strings.TrimRight(localGit(t, dir, "status", "--porcelain", "--", "old.txt"), "\r\n"); st != " D old.txt" {
			t.Fatalf("status of old.txt after StashApply = %q, want %q", st, " D old.txt")
		}
		if st := strings.TrimRight(localGit(t, dir, "status", "--porcelain", "--", "renamed.txt"), "\r\n"); st != "A  renamed.txt" {
			t.Fatalf("status of renamed.txt after StashApply = %q, want %q", st, "A  renamed.txt")
		}
		if got := readStashFile(t, dir, "other.txt"); got != "other modified\n" {
			t.Fatalf("other.txt after StashApply = %q, want unstaged change intact", got)
		}
	})
}

type failStashPushExecutor struct {
	delegate git.Executor
	err      error
}

func (e *failStashPushExecutor) Describe() string { return e.delegate.Describe() }

func (e *failStashPushExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "stash") {
		return nil, e.err
	}
	return e.delegate.Run(ctx, stdin, args...)
}

// When git stash push fails after unstaging a staged deletion, StashPaths
// keeps the deletion unstaged without losing any content and returns an error
// that explains both the push failure and that staged deletions were unstaged.
func TestStashPathsUnstagedDeletionErrorExplainsState(t *testing.T) {
	dir := initStashLiteralRepo(t, "stash-del-push-fail")
	writeFileLocal(t, dir, "keep.txt", "keep base\n")
	writeFileLocal(t, dir, "del.txt", "del base\n")
	localGit(t, dir, "add", "--", "keep.txt", "del.txt")
	localGit(t, dir, "commit", "-q", "-m", "base")

	writeFileLocal(t, dir, "keep.txt", "keep modified\n")
	localGit(t, dir, "rm", "-q", "--", "del.txt")

	pushErr := errors.New("simulated stash push failure")
	r := git.New(&failStashPushExecutor{
		delegate: &git.LocalExecutor{Dir: dir},
		err:      pushErr,
	})
	err := r.StashPaths("will fail", []string{"del.txt"})
	if err == nil {
		t.Fatal("StashPaths succeeded despite failing stash push")
	}
	if !errors.Is(err, pushErr) {
		t.Fatalf("error %q does not wrap the underlying push error", err)
	}
	if !strings.Contains(err.Error(), "staged deletions were unstaged") {
		t.Fatalf("error %q does not explain that staged deletions were unstaged", err)
	}
	if st := strings.TrimRight(localGit(t, dir, "status", "--porcelain", "--", "del.txt"), "\r\n"); st != " D del.txt" {
		t.Fatalf("status of del.txt after failed push = %q, want unstaged deletion %q", st, " D del.txt")
	}
	if got := readStashFile(t, dir, "keep.txt"); got != "keep modified\n" {
		t.Fatalf("keep.txt after failed push = %q, want sibling change untouched", got)
	}
}

// On a configured Linux SSH container (including Git 2.25 on Ubuntu 20.04 and
// BusyBox on Alpine), StashPaths stashes both staged deletions and staged
// renames cleanly over SSH and StashApply restores their exact content with
// deletions returning unstaged.
func TestConfiguredLinuxStashPathsStagedDeletionAndRename(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)

	t.Run("staged deletion", func(t *testing.T) {
		dir := srv.InitRepo(t, "stash-del")
		srv.WriteFile(t, dir, "keep.txt", []byte("keep base\n"))
		srv.WriteFile(t, dir, "del.txt", []byte("deleted body\n"))
		srv.RunGit(t, dir, "add", "--", "keep.txt", "del.txt")
		srv.RunGit(t, dir, "commit", "-q", "-m", "seed")

		srv.WriteFile(t, dir, "keep.txt", []byte("keep edited\n"))
		srv.RunGit(t, dir, "rm", "-q", "--", "del.txt")

		r := srv.Repo(t, dir)
		if err := r.StashPaths("linux del", []string{"del.txt"}); err != nil {
			t.Fatalf("StashPaths(del.txt) over SSH: %v", err)
		}
		if got := string(srv.ReadFile(t, dir, "del.txt")); got != "deleted body\n" {
			t.Fatalf("server del.txt after stash = %q, want committed content restored", got)
		}
		if got := string(srv.ReadFile(t, dir, "keep.txt")); got != "keep edited\n" {
			t.Fatalf("server keep.txt after stash = %q, want sibling edit kept", got)
		}
		if st := strings.TrimSpace(srv.RunGit(t, dir, "status", "--porcelain", "--", "del.txt")); st != "" {
			t.Fatalf("server status of del.txt after stash = %q, want clean", st)
		}

		if err := r.StashApply("stash@{0}", false); err != nil {
			t.Fatalf("StashApply over SSH: %v", err)
		}
		if st := srv.RunGit(t, dir, "status", "--porcelain", "--", "del.txt"); st != " D del.txt" {
			t.Fatalf("server status of del.txt after apply = %q, want %q", st, " D del.txt")
		}
		if got := string(srv.ReadFile(t, dir, "keep.txt")); got != "keep edited\n" {
			t.Fatalf("server keep.txt after apply = %q, want sibling edit intact", got)
		}
	})

	t.Run("staged rename", func(t *testing.T) {
		dir := srv.InitRepo(t, "stash-ren")
		srv.WriteFile(t, dir, "old.txt", []byte("renamed body\n"))
		srv.WriteFile(t, dir, "other.txt", []byte("other base\n"))
		srv.RunGit(t, dir, "add", "--", "old.txt", "other.txt")
		srv.RunGit(t, dir, "commit", "-q", "-m", "seed")

		srv.WriteFile(t, dir, "other.txt", []byte("other edited\n"))
		srv.RunGit(t, dir, "mv", "old.txt", "renamed.txt")

		r := srv.Repo(t, dir)
		if err := r.StashPaths("linux rename", []string{"old.txt", "renamed.txt"}); err != nil {
			t.Fatalf("StashPaths(rename) over SSH: %v", err)
		}
		if got := string(srv.ReadFile(t, dir, "old.txt")); got != "renamed body\n" {
			t.Fatalf("server old.txt after stash = %q, want committed content restored", got)
		}
		if st := strings.TrimSpace(srv.RunGit(t, dir, "status", "--porcelain", "--", "old.txt", "renamed.txt")); st != "" {
			t.Fatalf("server status of rename paths after stash = %q, want clean", st)
		}

		if err := r.StashApply("stash@{0}", false); err != nil {
			t.Fatalf("StashApply over SSH: %v", err)
		}
		if st := srv.RunGit(t, dir, "status", "--porcelain", "--", "old.txt"); st != " D old.txt" {
			t.Fatalf("server status of old.txt after apply = %q, want %q", st, " D old.txt")
		}
		if st := srv.RunGit(t, dir, "status", "--porcelain", "--", "renamed.txt"); st != "A  renamed.txt" {
			t.Fatalf("server status of renamed.txt after apply = %q, want %q", st, "A  renamed.txt")
		}
		if got := string(srv.ReadFile(t, dir, "renamed.txt")); got != "renamed body\n" {
			t.Fatalf("server renamed.txt after apply = %q, want exact renamed body", got)
		}
		if got := string(srv.ReadFile(t, dir, "other.txt")); got != "other edited\n" {
			t.Fatalf("server other.txt after apply = %q, want sibling edit intact", got)
		}
	})
}
