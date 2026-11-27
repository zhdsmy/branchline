package git_test

// Domain-level tests: CommitWith signs a commit off through git's native
// --signoff when the author is not overridden, so the trailer names the
// committer identity git itself resolves. The file is package git_test
// because internal/sshtest imports github.com/zhdsmy/branchline/internal/git;
// a same-package test would create an import cycle.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// countSignoffTrailers counts the Signed-off-by trailer lines of a commit
// message.
func countSignoffTrailers(message string) int {
	n := 0
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Signed-off-by:") {
			n++
		}
	}
	return n
}

// initSignoffLocalRepo creates a repository whose committer identity lives
// only in its own configuration — never in the machine's global one — and
// returns its directory.
func initSignoffLocalRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	localGit(t, dir, "init", "-q", "-b", "main")
	localGit(t, dir, "config", "user.name", "Signoff Tester")
	localGit(t, dir, "config", "user.email", "signoff@branchline.invalid")
	localGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// commitSignoffFile stages content into the repository and returns nothing;
// the oracle runs git inside the repository's own directory.
type signoffFixture struct {
	repo   *git.Repo
	dir    string
	oracle func(args ...string) string
	write  func(relPath, content string)
}

// checkCommitSignoff runs the four signoff scenarios against the fixture's
// repository: a plain signed-off commit picks up the committer identity git
// resolves, an amend of a message already carrying the trailer never
// duplicates it, an explicit author override keeps the hand-written trailer,
// and the switch being off adds nothing.
func checkCommitSignoff(t *testing.T, f signoffFixture) {
	t.Helper()

	t.Run("PlainCommitSignsOffCommitter", func(t *testing.T) {
		f.write("plain.txt", "one\n")
		f.oracle("add", "plain.txt")
		if err := f.repo.CommitWith("plain signed work", false, git.CommitOptions{SignedOff: true}); err != nil {
			t.Fatalf("signed commit: %v", err)
		}
		msg, err := f.repo.Message("HEAD")
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if n := countSignoffTrailers(msg); n != 1 {
			t.Fatalf("message has %d Signed-off-by lines, want 1:\n%s", n, msg)
		}
		committer := strings.TrimSpace(f.oracle("log", "-1", "--format=%cn <%ce>"))
		want := "Signed-off-by: " + committer
		if !strings.Contains(msg, want) {
			t.Fatalf("trailer %q missing for committer %q; message:\n%s", want, committer, msg)
		}
		if !strings.HasPrefix(msg, "plain signed work") {
			t.Fatalf("subject lost: message:\n%s", msg)
		}
	})

	t.Run("AmendDoesNotDuplicateTrailer", func(t *testing.T) {
		f.write("amendb.txt", "two\n")
		f.oracle("add", "amendb.txt")
		if err := f.repo.CommitWith("signed original", false, git.CommitOptions{SignedOff: true}); err != nil {
			t.Fatalf("initial signed commit: %v", err)
		}
		msg, err := f.repo.LastMessage()
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if n := countSignoffTrailers(msg); n != 1 {
			t.Fatalf("fixture: original has %d Signed-off-by lines, want 1:\n%s", n, msg)
		}
		f.write("amendc.txt", "three\n")
		f.oracle("add", "amendc.txt")
		// Amending with the original message — exactly what an amend flow
		// passes back — must not grow a second trailer.
		if err := f.repo.CommitWith(msg, true, git.CommitOptions{SignedOff: true}); err != nil {
			t.Fatalf("amend signed commit: %v", err)
		}
		got, err := f.repo.Message("HEAD")
		if err != nil {
			t.Fatalf("read amended message: %v", err)
		}
		if n := countSignoffTrailers(got); n != 1 {
			t.Fatalf("amended message has %d Signed-off-by lines, want 1:\n%s", n, got)
		}
		if !strings.HasPrefix(got, "signed original") {
			t.Fatalf("subject lost in amend: message:\n%s", got)
		}
	})

	t.Run("ExplicitAuthorKeepsWrittenTrailer", func(t *testing.T) {
		f.write("authored.txt", "four\n")
		f.oracle("add", "authored.txt")
		o := git.CommitOptions{SignedOff: true, Name: "Explicit Author", Email: "author@branchline.invalid"}
		if err := f.repo.CommitWith("authored work", false, o); err != nil {
			t.Fatalf("signed commit with explicit author: %v", err)
		}
		msg, err := f.repo.Message("HEAD")
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		want := "Signed-off-by: Explicit Author <author@branchline.invalid>"
		if n := countSignoffTrailers(msg); n != 1 || !strings.Contains(msg, want) {
			t.Fatalf("message =\n%s\nwant exactly one %q", msg, want)
		}
		if author := strings.TrimSpace(f.oracle("log", "-1", "--format=%an <%ae>")); author != "Explicit Author <author@branchline.invalid>" {
			t.Fatalf("author = %q, want the explicit override", author)
		}
	})

	t.Run("SwitchOffAddsNothing", func(t *testing.T) {
		f.write("unsigned.txt", "five\n")
		f.oracle("add", "unsigned.txt")
		if err := f.repo.CommitWith("unsigned work", false, git.CommitOptions{SignedOff: false}); err != nil {
			t.Fatalf("unsigned commit: %v", err)
		}
		msg, err := f.repo.Message("HEAD")
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if strings.Contains(msg, "Signed-off-by:") {
			t.Fatalf("unsigned commit gained a trailer:\n%s", msg)
		}
	})
}

// TestCommitSignoff runs the four scenarios against a repository on this
// machine.
func TestCommitSignoff(t *testing.T) {
	dir := initSignoffLocalRepo(t, "signoff-local")
	f := signoffFixture{
		repo: localRepo(dir),
		dir:  dir,
		oracle: func(args ...string) string {
			return strings.TrimSpace(localGit(t, dir, args...))
		},
		write: func(relPath, content string) {
			writeFileLocal(t, dir, relPath, content)
		},
	}
	checkCommitSignoff(t, f)
}

// TestCommitSignoffLoopbackSSH runs the same four scenarios through the real
// SSH executor against the disposable loopback sshd: the signoff trailer is
// written by the server's git from the server's repository configuration.
func TestCommitSignoffLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	dir := srv.InitRepo(t, "signoff")
	f := signoffFixture{
		repo: srv.Repo(t, dir),
		dir:  dir,
		oracle: func(args ...string) string {
			return strings.TrimSpace(srv.RunGit(t, dir, args...))
		},
		write: func(relPath, content string) {
			srv.WriteFile(t, dir, relPath, []byte(content))
		},
	}
	checkCommitSignoff(t, f)
}
