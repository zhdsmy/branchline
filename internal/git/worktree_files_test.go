package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// wfRepo is a scratch repository builder for the worktree-files tests: a
// temporary directory, a git invocation that cannot reach the user's
// configuration, and a file writer.
type wfRepo struct {
	t   *testing.T
	dir string
}

func newWFRepo(t *testing.T) *wfRepo {
	t.Helper()
	r := &wfRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.name", "Branchline Test")
	r.git("config", "user.email", "test@branchline.invalid")
	return r
}

func (r *wfRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *wfRepo) write(rel, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *wfRepo) commitFile(rel, content, message string) string {
	r.t.Helper()
	r.write(rel, content)
	r.git("add", "--", rel)
	r.git("commit", "-q", "-m", message)
	return r.git("rev-parse", "HEAD")
}

func (r *wfRepo) commit(message string) string {
	r.t.Helper()
	r.git("commit", "-q", "--allow-empty", "-m", message)
	return r.git("rev-parse", "HEAD")
}

func (r *wfRepo) open() *git.Repo {
	return git.New(&git.LocalExecutor{Dir: r.dir, NoGlobalConfig: true})
}

// wfStatuses renders a file-change list as a path-to-status map, with a
// rename's origin in parentheses: "renamed-after.txt" → "R(renamed-before.txt)".
func wfStatuses(files []git.FileChange) map[string]string {
	m := make(map[string]string, len(files))
	for _, f := range files {
		st := string(f.Status)
		if f.OrigPath != "" {
			st += "(" + f.OrigPath + ")"
		}
		m[f.Path] = st
	}
	return m
}

// Compared against an older commit, the list is the union of the staged,
// unstaged, deleted, renamed and untracked files the working tree carries:
// every one with its own status letter, a staged rename detected as such,
// and no ignored or unmodified file among them.
func TestWorktreeFilesAllChangeKinds(t *testing.T) {
	repo := newWFRepo(t)
	// The seed commits shared content, then an empty tip commit leaves HEAD
	// ahead of the compared commit, so every file of the list comes from the
	// working tree or the index alone.
	repo.commitFile("mod-staged.txt", "mod v1\n", "seed mod-staged")
	repo.commitFile("mod-unstaged.txt", "mod v1\n", "seed mod-unstaged")
	repo.commitFile("deleted.txt", "gone\n", "seed deleted")
	repo.commitFile("rename-before.txt", "alpha\nbeta\ngamma\ndelta\nepsilon\n", "seed rename")
	repo.commitFile("clean.txt", "clean\n", "seed clean")
	repo.commitFile(".gitignore", "ignored.txt\n", "seed gitignore")
	base := repo.git("rev-parse", "HEAD")
	repo.commit("empty tip")

	repo.write("mod-staged.txt", "mod v2\n")
	repo.git("add", "--", "mod-staged.txt")
	repo.write("mod-unstaged.txt", "mod v2\n")
	repo.write("added-staged.txt", "added\n")
	repo.git("add", "--", "added-staged.txt")
	repo.git("rm", "-q", "--", "deleted.txt")
	repo.git("mv", "rename-before.txt", "rename-after.txt")
	repo.write("untracked.txt", "new\n")
	repo.write("ignored.txt", "ignored\n")

	files, err := repo.open().WorktreeFiles(base)
	if err != nil {
		t.Fatalf("WorktreeFiles: %v", err)
	}
	got := wfStatuses(files)
	want := map[string]string{
		"mod-staged.txt":   "M",
		"mod-unstaged.txt": "M",
		"added-staged.txt": "A",
		"deleted.txt":      "D",
		"rename-after.txt": "R(rename-before.txt)",
		"untracked.txt":    "?",
	}
	if len(got) != len(want) {
		t.Fatalf("WorktreeFiles = %v, want exactly %v", got, want)
	}
	for path, st := range want {
		if got[path] != st {
			t.Fatalf("%s status = %q, want %q; all = %v", path, got[path], st, got)
		}
	}
	if _, ok := got["ignored.txt"]; ok {
		t.Fatalf("ignored file listed: %v", got)
	}
	if _, ok := got["clean.txt"]; ok {
		t.Fatalf("unmodified file listed: %v", got)
	}
	if _, ok := got[".gitignore"]; ok {
		t.Fatalf("unmodified .gitignore listed: %v", got)
	}
}

// Compared against HEAD with a clean working tree the list is empty.
func TestWorktreeFilesCleanTreeIsEmpty(t *testing.T) {
	repo := newWFRepo(t)
	head := repo.commitFile("only.txt", "only\n", "seed only")

	files, err := repo.open().WorktreeFiles(head)
	if err != nil {
		t.Fatalf("WorktreeFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("clean working tree listed files: %+v", files)
	}
}

// Paths with spaces, CJK characters and newlines parse correctly through
// the NUL-terminated output: each untracked file arrives whole, not split
// or quoted.
func TestWorktreeFilesSpecialFilenames(t *testing.T) {
	repo := newWFRepo(t)
	base := repo.commitFile("seed.txt", "seed\n", "seed")

	paths := []string{
		"with space.txt",
		"中文文件.txt",
		"with\nnewline.txt",
	}
	for _, p := range paths {
		repo.write(p, "content\n")
	}

	files, err := repo.open().WorktreeFiles(base)
	if err != nil {
		t.Fatalf("WorktreeFiles: %v", err)
	}
	got := wfStatuses(files)
	if len(got) != len(paths) {
		t.Fatalf("WorktreeFiles = %v, want exactly the %d untracked files", got, len(paths))
	}
	for _, p := range paths {
		if got[p] != "?" {
			t.Fatalf("path %q missing or status %q; all = %q", p, got[p], keys(got))
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// An empty revision is an error, as it is for the other worktree reads.
func TestWorktreeFilesEmptyRevision(t *testing.T) {
	repo := newWFRepo(t)
	repo.commitFile("only.txt", "only\n", "seed only")

	for _, rev := range []string{"", "  "} {
		if _, err := repo.open().WorktreeFiles(rev); err == nil {
			t.Fatalf("WorktreeFiles(%q) succeeded, want missing commit error", rev)
		}
	}
}

// Over a loopback SSH connection the same listing runs on the server:
// staged, deleted and untracked changes of the remote working tree arrive,
// and the ignored file does not.
func TestWorktreeFilesLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	workDir := srv.InitRepo(t, "wf-ssh")

	srv.WriteFile(t, workDir, "mod.txt", []byte("mod v1\n"))
	srv.WriteFile(t, workDir, "del.txt", []byte("gone\n"))
	srv.WriteFile(t, workDir, ".gitignore", []byte("ignored.txt\n"))
	srv.RunGit(t, workDir, "add", "--", "mod.txt", "del.txt", ".gitignore")
	srv.RunGit(t, workDir, "commit", "-m", "seed")
	base := srv.RunGit(t, workDir, "rev-parse", "HEAD")
	srv.RunGit(t, workDir, "commit", "--allow-empty", "-m", "empty tip")

	srv.WriteFile(t, workDir, "mod.txt", []byte("mod v2\n"))
	srv.RunGit(t, workDir, "rm", "-q", "--", "del.txt")
	srv.WriteFile(t, workDir, "untracked.txt", []byte("new\n"))
	srv.WriteFile(t, workDir, "ignored.txt", []byte("ignored\n"))

	files, err := srv.Repo(t, workDir).WorktreeFiles(base)
	if err != nil {
		t.Fatalf("SSH WorktreeFiles: %v", err)
	}
	got := wfStatuses(files)
	want := map[string]string{
		"mod.txt":       "M",
		"del.txt":       "D",
		"untracked.txt": "?",
	}
	if len(got) != len(want) {
		t.Fatalf("SSH WorktreeFiles = %v, want exactly %v", got, want)
	}
	for path, st := range want {
		if got[path] != st {
			t.Fatalf("SSH %s status = %q, want %q; all = %v", path, got[path], st, got)
		}
	}
}
