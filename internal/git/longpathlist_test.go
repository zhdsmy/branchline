package git_test

// Long-path-list tests: staging, unstaging and discarding many thousands of
// files at once must survive the command-line length limits of the two ways
// git runs — about 1 MiB of argv for a process started on this machine, and
// 128 KiB for the single argument the whole command is when a server's shell
// runs it over SSH. The path lists here measure about 1.2 MB, so the writes
// only finish in batches.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

const (
	longPathCount = 20000
	longPathPad   = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
)

// longPathNames are the paths of the bulk files, each about 56 bytes: the
// whole list measures about 1.2 MB on a command line.
func longPathNames() []string {
	names := make([]string, longPathCount)
	for i := range names {
		names[i] = fmt.Sprintf("bulk/file-%d-%s.txt", i+1, longPathPad)
	}
	return names
}

// longPathScript is a POSIX sh script writing printf-formatted content to
// every bulk file of dir, one loop per parallel chunk. It must run under a
// minimal sh as on BusyBox: no arrays, no brace expansion, no bash syntax.
func longPathScript(dir string, count int, format string) string {
	var b strings.Builder
	b.WriteString("set -e\ncd " + git.ShellQuote(dir) + "\nmkdir -p bulk\npids=\"\"\n")
	const chunks = 8
	per := (count + chunks - 1) / chunks
	for c := 0; c < chunks; c++ {
		start := c*per + 1
		end := start + per - 1
		if start > count {
			break
		}
		if end > count {
			end = count
		}
		fmt.Fprintf(&b, "(i=%d; while [ $i -le %d ]; do printf '%s' > \"bulk/file-$i-%s.txt\"; i=$((i+1)); done) &\npids=\"$pids $!\"\n", start, end, format, longPathPad)
	}
	b.WriteString("fail=0\nfor p in $pids; do\n\twait \"$p\" || fail=1\ndone\n[ \"$fail\" -eq 0 ]\n")
	return b.String()
}

func countLines(out string) int {
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// runLongPathListFlow stages, unstages and discards the bulk paths through
// repo, checking each step with the plain git CLI of the same repository:
// the operations under test never prove themselves. rewrite puts different
// content into every bulk file, runGit runs the machine's or the server's
// git in the working tree.
func runLongPathListFlow(t *testing.T, repo *git.Repo, names []string, rewrite func(*testing.T), runGit func(t *testing.T, args ...string) string) {
	t.Helper()

	t.Run("Stage", func(t *testing.T) {
		if err := repo.Stage(names...); err != nil {
			t.Fatalf("Stage of %d paths: %v", len(names), err)
		}
		if got := countLines(runGit(t, "ls-files", "--", "bulk")); got != len(names) {
			t.Fatalf("bulk files in the index = %d, want %d", got, len(names))
		}
	})

	t.Run("Unstage", func(t *testing.T) {
		if err := repo.Unstage(names...); err != nil {
			t.Fatalf("Unstage of %d paths: %v", len(names), err)
		}
		if got := countLines(runGit(t, "ls-files", "--", "bulk")); got != 0 {
			t.Fatalf("bulk files in the index = %d, want 0", got)
		}
	})

	t.Run("DiscardModified", func(t *testing.T) {
		// Commit the files and rewrite them with different content: the
		// discard must restore every one through the checkout batches.
		runGit(t, "add", "-A", "--", "bulk")
		runGit(t, "commit", "-q", "-m", "bulk")
		rewrite(t)
		if status := runGit(t, "status", "--porcelain"); strings.TrimSpace(status) == "" {
			t.Fatal("rewriting the bulk files changed nothing")
		}
		tracked := make([]git.FileChange, len(names))
		for i, n := range names {
			tracked[i] = git.FileChange{Path: n, Status: 'M'}
		}
		if err := repo.Discard(tracked...); err != nil {
			t.Fatalf("Discard of %d modified paths: %v", len(tracked), err)
		}
		if status := runGit(t, "status", "--porcelain"); status != "" {
			t.Fatalf("status after Discard = %q, want clean", status)
		}
	})

	t.Run("DiscardUntracked", func(t *testing.T) {
		// Reset back to the initial commit with --mixed so the index drops
		// bulk/ immediately while every file stays untracked on disk, matching
		// a Discard All of a fresh build output without paying git rm --cached's
		// slow per-entry removal across 20,000 paths.
		runGit(t, "reset", "-q", "--mixed", "HEAD~1")
		untracked := make([]git.FileChange, len(names))
		for i, n := range names {
			untracked[i] = git.FileChange{Path: n, Status: '?'}
		}
		if err := repo.Discard(untracked...); err != nil {
			t.Fatalf("Discard of %d untracked paths: %v", len(untracked), err)
		}
		if got := runGit(t, "ls-files", "--others", "--exclude-standard"); got != "" {
			t.Fatalf("untracked files after Discard = %q, want none", got)
		}
	})
}

func TestLongPathListLocal(t *testing.T) {
	dir, _ := initCommittedLocal(t, "longpath")
	names := longPathNames()
	writeLocalBulkFiles(t, dir, names, "x\n")
	repo := localRepo(dir)
	runLongPathListFlow(t, repo, names,
		func(t *testing.T) { writeLocalBulkFiles(t, dir, names, "y\n") },
		func(t *testing.T, args ...string) string { return localGit(t, dir, args...) },
	)
}

func writeLocalBulkFiles(t *testing.T, dir string, names []string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bulk"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// runLongPathListOverSSH is the long-path-list flow of a repository on a
// server, shared by the loopback test and the configured-Linux one.
func runLongPathListOverSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()
	workDir := srv.InitRepo(t, "longpath-work")
	repo := srv.Repo(t, workDir)
	names := longPathNames()

	srv.RunShell(t, longPathScript(workDir, len(names), "x\\n"))
	runLongPathListFlow(t, repo, names,
		func(t *testing.T) { srv.RunShell(t, longPathScript(workDir, len(names), "y\\n")) },
		func(t *testing.T, args ...string) string { return srv.RunGit(t, workDir, args...) },
	)
}

func TestLongPathListLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	runLongPathListOverSSH(t, srv)
}

func TestConfiguredLinuxLongPathList(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	runLongPathListOverSSH(t, srv)
}
