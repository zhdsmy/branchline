package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// guardRepo builds a fixture repository with the tracked files victim.txt
// and plain.txt committed.
func guardRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "victim.txt", "tracked content\n")
	writeFile(t, dir, "plain.txt", "plain base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "initial")
	return dir
}

// mergeMustConflict merges the branch side and fails the test when the
// merge does not stop with a conflict.
func mergeMustConflict(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "merge", "side")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("fixture: the merge did not conflict: %s", out)
	}
}

// obstructWithDirectory deletes the tracked file victim.txt and puts a
// directory holding the given files in its place. The pairs are relative
// path and content.
func obstructWithDirectory(t *testing.T, dir, victim string, files ...string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, victim)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, victim), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(files); i += 2 {
		writeFileDeep(t, dir, filepath.Join(victim, files[i]), files[i+1])
	}
}

func readFileOr(t *testing.T, path string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Discarding the deletion of a tracked file whose place a directory of
// untracked files took must refuse and leave the directory untouched.
func TestWorktreeGuardDiscardRefusesUntrackedDirectory(t *testing.T) {
	dir := guardRepo(t)
	obstructWithDirectory(t, dir, "victim.txt", "inner.txt", "untracked work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.Discard(FileChange{Path: "victim.txt", Status: 'D'})
	if err == nil {
		t.Fatal("discard reported success although victim.txt holds a directory of untracked files")
	}
	if !strings.Contains(err.Error(), "victim.txt") {
		t.Fatalf("the error does not name the obstructed path: %v", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "victim.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "plain.txt")); err != nil {
		t.Fatalf("unrelated file damaged: %v", err)
	}
}

// A directory holding only files the ignore rules exclude is exactly as
// much unprotected work as untracked files, so the discard refuses it too.
func TestWorktreeGuardDiscardRefusesIgnoredOnlyDirectory(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, dir, ".gitignore", "*.log\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-qm", "ignore rule")
	obstructWithDirectory(t, dir, "victim.txt", "only.log", "ignored work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.Discard(FileChange{Path: "victim.txt", Status: 'D'})
	if err == nil {
		t.Fatal("discard reported success although victim.txt holds a directory of ignored files")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "victim.txt", "only.log"))
	if !ok || got != "ignored work\n" {
		t.Fatalf("the ignored content was destroyed: content %q, read ok %v", got, ok)
	}
}

// One obstructed path refuses the whole batch: the ordinary modification in
// the same batch keeps its changes because nothing at all ran.
func TestWorktreeGuardDiscardBatchIsAllOrNothing(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, dir, "plain.txt", "plain base\nmodified\n")
	obstructWithDirectory(t, dir, "victim.txt", "inner.txt", "untracked work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.Discard(
		FileChange{Path: "victim.txt", Status: 'D'},
		FileChange{Path: "plain.txt", Status: 'M'},
	)
	if err == nil {
		t.Fatal("discard of a batch with an obstructed path reported success")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "plain.txt"))
	if !ok || got != "plain base\nmodified\n" {
		t.Fatalf("the ordinary modification was discarded although the batch refused: content %q, read ok %v", got, ok)
	}
	got, ok = readFileOr(t, filepath.Join(dir, "victim.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}

// An empty directory in the deleted file's place holds no work, so the
// discard restores the file without complaint.
func TestWorktreeGuardDiscardEmptyDirectoryRestores(t *testing.T) {
	dir := guardRepo(t)
	if err := os.Remove(filepath.Join(dir, "victim.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "victim.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	if err := r.Discard(FileChange{Path: "victim.txt", Status: 'D'}); err != nil {
		t.Fatalf("an empty directory must not block the restore: %v", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "victim.txt"))
	if !ok || got != "tracked content\n" {
		t.Fatalf("the file was not restored: content %q, read ok %v", got, ok)
	}
}

// An untracked file standing where a parent directory of the target must be
// is deleted by git's checkout — measured: `git checkout -- sub/dir/file.txt`
// removes an untracked file at sub and creates the directory in its place,
// exit 0 — so the guard refuses it exactly like an obstructing directory.
func TestWorktreeGuardDiscardRefusesBlockedParent(t *testing.T) {
	dir := initRepo(t)
	writeFileDeep(t, dir, "sub/dir/file.txt", "deep base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "initial")
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "sub", "blocking file\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.Discard(FileChange{Path: "sub/dir/file.txt", Status: 'D'})
	if err == nil {
		t.Fatal("discard reported success although an untracked file blocks a parent directory")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "sub"))
	if !ok || got != "blocking file\n" {
		t.Fatalf("the blocking untracked file was destroyed: content %q, read ok %v", got, ok)
	}
}

// ResetFiles restoring a file whose place a directory of untracked files
// took must refuse and leave the directory untouched.
func TestWorktreeGuardResetFilesRefusesUntrackedDirectory(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, dir, "victim.txt", "version two\n")
	gitRun(t, dir, "commit", "-qam", "second")
	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD~1"))
	obstructWithDirectory(t, dir, "victim.txt", "inner.txt", "untracked work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.ResetFiles(head, "victim.txt")
	if err == nil {
		t.Fatal("reset reported success although victim.txt holds a directory of untracked files")
	}
	if !strings.Contains(err.Error(), "victim.txt") {
		t.Fatalf("the error does not name the obstructed path: %v", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "victim.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}

// Writing a merge resolution over a directory of untracked files must
// refuse before the working tree is touched, and the resolution's object
// stays in the repository for the user to recover.
func TestWorktreeGuardWriteResolvedConflictRefusesUntrackedDirectory(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "conf.txt", "base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, dir, "conf.txt", "theirs\n")
	gitRun(t, dir, "commit", "-qam", "theirs")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "conf.txt", "ours\n")
	gitRun(t, dir, "commit", "-qam", "ours")
	mergeMustConflict(t, dir)

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := st.ConflictIdentity("conf.txt")
	if !ok {
		t.Fatal("fixture: conf.txt is not unmerged")
	}

	obstructWithDirectory(t, dir, "conf.txt", "inner.txt", "untracked work\n")
	err = r.SafeWriteResolvedConflict("conf.txt", "resolved\n", identity)
	if err == nil {
		t.Fatal("writing the resolution reported success although conf.txt holds a directory of untracked files")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "conf.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
	if unmerged := strings.TrimSpace(gitOut(t, dir, "ls-files", "-u", "--", "conf.txt")); unmerged == "" {
		t.Fatal("conf.txt lost its unmerged index entries after refusal")
	}
}

// Taking one side of a conflict whose file's place a directory of untracked
// files took must refuse and leave the directory untouched.
func TestWorktreeGuardTakeConflictSideRefusesUntrackedDirectory(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "conf.txt", "base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, dir, "conf.txt", "theirs\n")
	gitRun(t, dir, "commit", "-qam", "theirs")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "conf.txt", "ours\n")
	gitRun(t, dir, "commit", "-qam", "ours")
	mergeMustConflict(t, dir)

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := st.ConflictIdentity("conf.txt")
	if !ok {
		t.Fatal("fixture: conf.txt is not unmerged")
	}

	obstructWithDirectory(t, dir, "conf.txt", "inner.txt", "untracked work\n")
	err = r.SafeTakeConflictSide("conf.txt", "ours", identity)
	if err == nil {
		t.Fatal("taking a side reported success although conf.txt holds a directory of untracked files")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "conf.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
	if unmerged := strings.TrimSpace(gitOut(t, dir, "ls-files", "-u", "--", "conf.txt")); unmerged == "" {
		t.Fatal("conf.txt lost its unmerged index entries after refusal")
	}
}

// Discarding a submodule whose HEAD moved must check out the commit the
// parent's index records instead of reporting a silent success.
func TestWorktreeGuardDiscardRestoresMovedSubmodule(t *testing.T) {
	dir, sub := withSubmodule(t)
	indexed := strings.TrimSpace(gitOut(t, dir, "ls-files", "--format=%(objectname)", "--", "vendor/my lib"))
	// Add a commit inside the submodule the index does not record, moving HEAD.
	writeFile(t, sub, "lib.txt", "lib\nmore\n")
	gitRun(t, sub, "commit", "-qam", "moved ahead")
	moved := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD"))
	if moved == indexed {
		t.Fatal("fixture: HEAD did not move")
	}
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	if err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'}); err != nil {
		t.Fatalf("discarding the submodule failed: %v", err)
	}
	head := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD"))
	if head != indexed {
		t.Fatalf("the submodule HEAD is %s, want the index's %s", head, indexed)
	}
}

// A submodule with uncommitted changes reports an error instead of deleting
// them: the changes survive and the error sends the user into the submodule.
func TestWorktreeGuardDiscardSubmoduleKeepsUncommittedChanges(t *testing.T) {
	dir, sub := withSubmodule(t)
	writeFile(t, sub, "loose.txt", "uncommitted work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'})
	if err == nil {
		t.Fatal("discard reported success although the submodule has uncommitted changes")
	}
	got, ok := readFileOr(t, filepath.Join(sub, "loose.txt"))
	if !ok || got != "uncommitted work\n" {
		t.Fatalf("the uncommitted change was destroyed: content %q, read ok %v", got, ok)
	}
	if !strings.Contains(err.Error(), "vendor/my lib") {
		t.Fatalf("the error does not name the submodule: %v", err)
	}
}

// When the submodule does not have the commit the parent's index records,
// the discard must fail without contacting any network.
func TestWorktreeGuardDiscardSubmoduleMissingCommitErrors(t *testing.T) {
	dir, _ := withSubmodule(t)
	fake := "1111111111111111111111111111111111111111"
	gitRun(t, dir, "update-index", "--cacheinfo", "160000,"+fake+",vendor/my lib")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	if err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'}); err == nil {
		t.Fatal("discard reported success although the recorded commit is missing from the submodule")
	}
}

// Ordinary discards and file resets still do exactly what they did: a
// modified file is restored, an untracked file is deleted, a reset file
// takes the revision's content.
func TestWorktreeGuardOrdinaryDiscardAndResetStillWork(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, dir, "plain.txt", "plain base\nmodified\n")
	writeFile(t, dir, "untracked.txt", "new\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	if err := r.Discard(
		FileChange{Path: "plain.txt", Status: 'M'},
		FileChange{Path: "untracked.txt", Status: '?'},
	); err != nil {
		t.Fatalf("ordinary discard failed: %v", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "plain.txt"))
	if !ok || got != "plain base\n" {
		t.Fatalf("plain.txt not restored: content %q, read ok %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked.txt not deleted: %v", err)
	}

	writeFile(t, dir, "victim.txt", "version two\n")
	gitRun(t, dir, "commit", "-qam", "second")
	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD~1"))
	if err := r.ResetFiles(head, "victim.txt"); err != nil {
		t.Fatalf("ordinary reset failed: %v", err)
	}
	got, ok = readFileOr(t, filepath.Join(dir, "victim.txt"))
	if !ok || got != "tracked content\n" {
		t.Fatalf("victim.txt not reset: content %q, read ok %v", got, ok)
	}
}

// The obstruction check runs on the server for an SSH repository too:
// the same obstructed discard refuses and destroys nothing.
func TestWorktreeGuardDiscardRefusesUntrackedDirectorySSH(t *testing.T) {
	srv := startSSHD(t)
	dir := guardRepo(t)
	obstructWithDirectory(t, dir, "victim.txt", "inner.txt", "untracked work\n")
	r := New(srv.executor(t, dir))

	err := r.Discard(FileChange{Path: "victim.txt", Status: 'D'})
	if err == nil {
		t.Fatal("discard over SSH reported success although victim.txt holds a directory of untracked files")
	}
	got, ok := readFileOr(t, filepath.Join(dir, "victim.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}

// Discarding a moved submodule over SSH checks out the commit the parent's
// index records, the git commands running on the server.
func TestWorktreeGuardDiscardRestoresMovedSubmoduleSSH(t *testing.T) {
	srv := startSSHD(t)
	dir, sub := withSubmodule(t)
	indexed := strings.TrimSpace(gitOut(t, dir, "ls-files", "--format=%(objectname)", "--", "vendor/my lib"))
	writeFile(t, sub, "lib.txt", "lib\nmore\n")
	gitRun(t, sub, "commit", "-qam", "moved ahead")
	r := New(srv.executor(t, dir))

	if err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'}); err != nil {
		t.Fatalf("discarding the submodule over SSH failed: %v", err)
	}
	head := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD"))
	if head != indexed {
		t.Fatalf("the submodule HEAD is %s, want the index's %s", head, indexed)
	}
}

// othersScanCounter counts the entries and bytes the obstruction check
// reads back from ls-files --others, nothing else.
type othersScanCounter struct {
	inner   Executor
	entries int
	bytes   int
}

func (c *othersScanCounter) Describe() string { return c.inner.Describe() }

func (c *othersScanCounter) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	out, err := c.inner.Run(ctx, stdin, args...)
	scanning := false
	for i, a := range args {
		if a == "ls-files" && i+1 < len(args) && args[i+1] == "--others" {
			scanning = true
		}
	}
	if scanning {
		c.bytes += len(out)
		for _, entry := range strings.Split(string(out), "\x00") {
			if entry != "" {
				c.entries++
			}
		}
	}
	return out, err
}

// The check's reads must not grow with untracked or ignored files that
// cannot obstruct anything: a directory of two thousand ignored files next
// to the target's parent leaves the check a handful of entries, while the
// obstruction at the target's own place is still found. Without the
// collapsed scan of the parent directories every one of the two thousand
// files would be listed, at 2001 entries instead of a few.
func TestWorktreeGuardCheckIgnoresUnrelatedIgnoredFiles(t *testing.T) {
	dir := initRepo(t)
	writeFileDeep(t, dir, "web/src/a.txt", "base\n")
	writeFile(t, dir, ".gitignore", "node_modules\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "initial")
	for i := 0; i < 2000; i++ {
		writeFileDeep(t, dir, filepath.Join("web/node_modules", "pkg", "f"+strconv.Itoa(i)+".js"), "ignored\n")
	}
	// The obstruction itself: a directory of untracked work at the file's place.
	if err := os.Remove(filepath.Join(dir, "web/src/a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "web/src/a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "web/src/a.txt/inner.txt", "untracked work\n")

	counter := &othersScanCounter{inner: &LocalExecutor{Dir: dir, NoGlobalConfig: true}}
	r := New(counter)
	err := r.Discard(FileChange{Path: "web/src/a.txt", Status: 'D'})
	if err == nil {
		t.Fatal("discard reported success although web/src/a.txt holds a directory of untracked files")
	}
	if !strings.Contains(err.Error(), "web/src/a.txt") {
		t.Fatalf("the error does not name the obstructed path: %v", err)
	}
	if counter.entries > 8 {
		t.Fatalf("the obstruction check read %d entries (%d bytes) from ls-files --others, want at most a few: the scan is following unrelated ignored files", counter.entries, counter.bytes)
	}
	if counter.bytes > 2000 {
		t.Fatalf("the obstruction check read %d bytes, want a few hundred at most", counter.bytes)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "web/src/a.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("the untracked content was destroyed: content %q, read ok %v", got, ok)
	}
}

func TestWorktreeGuardDiscardBlockedParentIgnoredSymlinkAndEmptyDir(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		check func(t *testing.T, dir string)
	}{
		{
			name: "ignored file",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, ".gitignore", "sub\n")
				gitRun(t, dir, "add", ".gitignore")
				gitRun(t, dir, "commit", "-qm", "ignore sub")
				if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, dir, "sub", "ignored blocker\n")
			},
			check: func(t *testing.T, dir string) {
				got, ok := readFileOr(t, filepath.Join(dir, "sub"))
				if !ok || got != "ignored blocker\n" {
					t.Fatalf("ignored blocker destroyed: %q (%v)", got, ok)
				}
			},
		},
		{
			name: "symlink to file",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "target.txt", "keep\n")
				if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target.txt", filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				dest, err := os.Readlink(filepath.Join(dir, "sub"))
				if err != nil || dest != "target.txt" {
					t.Fatalf("symlink to file destroyed: dest=%q err=%v", dest, err)
				}
			},
		},
		{
			name: "symlink to directory",
			setup: func(t *testing.T, dir string) {
				writeFileDeep(t, dir, "realdir/keep.txt", "keep\n")
				if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("realdir", filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				dest, err := os.Readlink(filepath.Join(dir, "sub"))
				if err != nil || dest != "realdir" {
					t.Fatalf("symlink to directory destroyed: dest=%q err=%v", dest, err)
				}
			},
		},
		{
			name: "dangling symlink",
			setup: func(t *testing.T, dir string) {
				if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("nonexistent", filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string) {
				dest, err := os.Readlink(filepath.Join(dir, "sub"))
				if err != nil || dest != "nonexistent" {
					t.Fatalf("dangling symlink destroyed: dest=%q err=%v", dest, err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			writeFileDeep(t, dir, "sub/dir/file.txt", "deep base\n")
			gitRun(t, dir, "add", ".")
			gitRun(t, dir, "commit", "-qm", "initial")
			tc.setup(t, dir)

			r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
			err := r.Discard(FileChange{Path: "sub/dir/file.txt", Status: 'D'})
			if err == nil {
				t.Fatalf("discard succeeded despite %s blocking parent directory", tc.name)
			}
			if !strings.Contains(err.Error(), "sub") {
				t.Fatalf("error %v does not name blocking parent sub", err)
			}
			tc.check(t, dir)
		})
	}

	t.Run("nested empty directories allow restore", func(t *testing.T) {
		dir := guardRepo(t)
		if err := os.Remove(filepath.Join(dir, "victim.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "victim.txt", "empty1", "empty2"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		if err := r.Discard(FileChange{Path: "victim.txt", Status: 'D'}); err != nil {
			t.Fatalf("nested empty directories blocked restore: %v", err)
		}
		got, ok := readFileOr(t, filepath.Join(dir, "victim.txt"))
		if !ok || got != "tracked content\n" {
			t.Fatalf("victim.txt = %q (%v), want tracked content", got, ok)
		}
	})
}

func TestWorktreeGuardDiscardCleanPathsAndMixedBatch(t *testing.T) {
	t.Run("untracked directory path refused", func(t *testing.T) {
		dir := guardRepo(t)
		writeFileDeep(t, dir, "udir/secret.txt", "keep untracked\n")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		err := r.Discard(FileChange{Path: "udir", Status: '?'})
		if err == nil || !strings.Contains(err.Error(), "udir") {
			t.Fatalf("discard of obstructed untracked path = %v, want refusal naming udir", err)
		}
		got, ok := readFileOr(t, filepath.Join(dir, "udir", "secret.txt"))
		if !ok || got != "keep untracked\n" {
			t.Fatalf("udir/secret.txt destroyed: %q (%v)", got, ok)
		}
	})

	t.Run("ignored directory path refused", func(t *testing.T) {
		dir := guardRepo(t)
		writeFile(t, dir, ".gitignore", "idir/\n")
		gitRun(t, dir, "add", ".gitignore")
		gitRun(t, dir, "commit", "-qm", "ignore idir")
		writeFileDeep(t, dir, "idir/secret.log", "keep ignored\n")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		err := r.Discard(FileChange{Path: "idir", Status: '!'})
		if err == nil || !strings.Contains(err.Error(), "idir") {
			t.Fatalf("discard of obstructed ignored path = %v, want refusal naming idir", err)
		}
		got, ok := readFileOr(t, filepath.Join(dir, "idir", "secret.log"))
		if !ok || got != "keep ignored\n" {
			t.Fatalf("idir/secret.log destroyed: %q (%v)", got, ok)
		}
	})

	t.Run("mixed batch leaves untracked and ignored intact on refusal", func(t *testing.T) {
		dir := guardRepo(t)
		writeFile(t, dir, ".gitignore", "*.log\n")
		gitRun(t, dir, "add", ".gitignore")
		gitRun(t, dir, "commit", "-qm", "ignore logs")
		writeFile(t, dir, "plain.txt", "modified\n")
		writeFile(t, dir, "loose.txt", "untracked\n")
		writeFile(t, dir, "app.log", "ignored\n")
		obstructWithDirectory(t, dir, "victim.txt", "inner.txt", "untracked work\n")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		err := r.Discard(
			FileChange{Path: "victim.txt", Status: 'D'},
			FileChange{Path: "plain.txt", Status: 'M'},
			FileChange{Path: "loose.txt", Status: '?'},
			FileChange{Path: "app.log", Status: '!'},
		)
		if err == nil {
			t.Fatal("mixed batch with obstructed path reported success")
		}
		for path, want := range map[string]string{
			"plain.txt":            "modified\n",
			"loose.txt":            "untracked\n",
			"app.log":              "ignored\n",
			"victim.txt/inner.txt": "untracked work\n",
		} {
			got, ok := readFileOr(t, filepath.Join(dir, filepath.FromSlash(path)))
			if !ok || got != want {
				t.Fatalf("%s = %q (%v), want %q", path, got, ok, want)
			}
		}
	})
}

func TestWorktreeGuardResetFilesDropAndBatchAllOrNothing(t *testing.T) {
	dir := guardRepo(t)
	first := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	writeFile(t, dir, "plain.txt", "plain v2\n")
	writeFile(t, dir, "added.txt", "added in second\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "second")

	obstructWithDirectory(t, dir, "added.txt", "inner.txt", "untracked work\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	err := r.ResetFiles(first, "plain.txt", "added.txt")
	if err == nil || !strings.Contains(err.Error(), "added.txt") {
		t.Fatalf("ResetFiles with obstructed drop path = %v, want refusal naming added.txt", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "added.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("added.txt/inner.txt destroyed: %q (%v)", got, ok)
	}
	got, ok = readFileOr(t, filepath.Join(dir, "plain.txt"))
	if !ok || got != "plain v2\n" {
		t.Fatalf("plain.txt was modified despite batch refusal: %q (%v)", got, ok)
	}
}

func TestWorktreeGuardTakeConflictSideDeletionRefusesUntrackedDirectory(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "conf.txt", "base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, dir, "conf.txt", "theirs\n")
	gitRun(t, dir, "commit", "-qam", "theirs")
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "rm", "-q", "conf.txt")
	gitRun(t, dir, "commit", "-qm", "delete on main")
	mergeMustConflict(t, dir)

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := st.ConflictIdentity("conf.txt")
	if !ok {
		t.Fatal("fixture: conf.txt is not unmerged")
	}

	obstructWithDirectory(t, dir, "conf.txt", "inner.txt", "untracked work\n")
	err = r.SafeTakeConflictSide("conf.txt", "ours", identity)
	if err == nil || !strings.Contains(err.Error(), "conf.txt") {
		t.Fatalf("SafeTakeConflictSide(deleted side) = %v, want refusal naming conf.txt", err)
	}
	got, ok := readFileOr(t, filepath.Join(dir, "conf.txt", "inner.txt"))
	if !ok || got != "untracked work\n" {
		t.Fatalf("conf.txt/inner.txt destroyed: %q (%v)", got, ok)
	}
	if unmerged := strings.TrimSpace(gitOut(t, dir, "ls-files", "-u", "--", "conf.txt")); unmerged == "" {
		t.Fatal("conf.txt lost its unmerged index entries after refusal")
	}
}

func TestWorktreeGuardWorktreeOthersBatchingMatchesSingleBatch(t *testing.T) {
	dir := initRepo(t)
	paths := []string{"d1/f1.txt", "d2/f2.txt", "d3/sub/f3.txt", "d4/f4.txt"}
	for _, p := range paths {
		writeFileDeep(t, dir, p, "base\n")
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "initial")

	obstructWithDirectory(t, dir, "d4/f4.txt", "inner.txt", "keep\n")
	if err := os.RemoveAll(filepath.Join(dir, "d3")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "d3", "parent blocker\n")
	writeFileDeep(t, dir, "d2/ignored_dir/x.txt", "sibling\n")

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	ancs := []string{"d1", "d2", "d3/sub", "d3", "d4"}

	singleTargets, err := r.worktreeOthers(paths, false)
	if err != nil {
		t.Fatal(err)
	}
	singleAncs, err := r.worktreeOthers(ancs, true)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(singleTargets)
	sort.Strings(singleAncs)

	old := pathBatchLimit
	pathBatchLimit = 12
	t.Cleanup(func() { pathBatchLimit = old })

	batchedTargets, err := r.worktreeOthers(paths, false)
	if err != nil {
		t.Fatal(err)
	}
	batchedAncs, err := r.worktreeOthers(ancs, true)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(batchedTargets)
	sort.Strings(batchedAncs)

	if !slices.Equal(batchedTargets, singleTargets) {
		t.Fatalf("batched targets = %v, want %v", batchedTargets, singleTargets)
	}
	if !slices.Equal(batchedAncs, singleAncs) {
		t.Fatalf("batched ancestors = %v, want %v", batchedAncs, singleAncs)
	}

	err = r.checkWorktreeObstructions(paths)
	if err == nil || !strings.Contains(err.Error(), "d4/f4.txt") || !strings.Contains(err.Error(), "d3") {
		t.Fatalf("batched checkWorktreeObstructions = %v, want both d4/f4.txt and d3 named", err)
	}
}

func TestWorktreeGuardDiscardSubmoduleEdgeCases(t *testing.T) {
	t.Run("uninitialized submodule errors", func(t *testing.T) {
		dir, _ := withSubmodule(t)
		clone := filepath.Join(t.TempDir(), "clone")
		gitRun(t, dir, "clone", "-q", dir, clone)
		r := New(&LocalExecutor{Dir: clone, NoGlobalConfig: true})
		err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'})
		if err == nil || !strings.Contains(err.Error(), "not checked out") {
			t.Fatalf("discard of uninitialized submodule = %v, want not checked out error", err)
		}
	})

	t.Run("conflicting dirty tracked edit preserved", func(t *testing.T) {
		dir, sub := withSubmodule(t)
		writeFile(t, sub, "lib.txt", "lib\nmoved\n")
		gitRun(t, sub, "commit", "-qam", "moved")
		moved := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD"))
		writeFile(t, sub, "lib.txt", "lib\nconflicting dirty\n")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

		if err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'}); err == nil {
			t.Fatal("discard succeeded although submodule has conflicting dirty tracked edits")
		}
		got, ok := readFileOr(t, filepath.Join(sub, "lib.txt"))
		if !ok || got != "lib\nconflicting dirty\n" {
			t.Fatalf("dirty tracked edit destroyed: %q (%v)", got, ok)
		}
		if head := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD")); head != moved {
			t.Fatalf("submodule HEAD moved to %s on conflicting checkout, want %s", head, moved)
		}
	})

	t.Run("update mode rebase still checks out index commit", func(t *testing.T) {
		dir, sub := withSubmodule(t)
		indexed := strings.TrimSpace(gitOut(t, dir, "ls-files", "--format=%(objectname)", "--", "vendor/my lib"))
		writeFile(t, sub, "lib.txt", "lib\nmoved\n")
		gitRun(t, sub, "commit", "-qam", "moved")
		gitRun(t, dir, "config", "submodule.vendor/my lib.update", "rebase")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

		if err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'M'}); err != nil {
			t.Fatalf("discard with update=rebase failed: %v", err)
		}
		if head := strings.TrimSpace(gitOut(t, sub, "rev-parse", "HEAD")); head != indexed {
			t.Fatalf("submodule HEAD = %s, want indexed %s", head, indexed)
		}
	})

	t.Run("blocked parent directory of submodule refused", func(t *testing.T) {
		dir, _ := withSubmodule(t)
		if err := os.RemoveAll(filepath.Join(dir, "vendor")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "vendor", "blocking file\n")
		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

		err := r.Discard(FileChange{Path: "vendor/my lib", Status: 'D'})
		if err == nil || !strings.Contains(err.Error(), "vendor") {
			t.Fatalf("discard with blocked submodule parent = %v, want refusal naming vendor", err)
		}
		got, ok := readFileOr(t, filepath.Join(dir, "vendor"))
		if !ok || got != "blocking file\n" {
			t.Fatalf("blocking vendor file destroyed: %q (%v)", got, ok)
		}
	})
}

func TestWorktreeGuardSSHSpecialCharsAndBlockedParent(t *testing.T) {
	srv := startSSHD(t)
	dir := initRepo(t)
	specialTarget := "dir 'one' a/sub$x/sub[a]z/-weird 中文.txt"
	parentFile := "p 'two' b/sub$y/child.txt"
	writeFileDeep(t, dir, specialTarget, "base target\n")
	writeFileDeep(t, dir, parentFile, "base parent\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "special paths")

	obstructWithDirectory(t, dir, specialTarget, "inner.txt", "keep special\n")
	if err := os.RemoveAll(filepath.Join(dir, "p 'two' b")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "p 'two' b", "keep parent\n")

	r := New(srv.executor(t, dir))
	err := r.Discard(FileChange{Path: specialTarget, Status: 'D'})
	if err == nil || !strings.Contains(err.Error(), specialTarget) {
		t.Fatalf("SSH discard of special obstructed target = %v, want refusal naming %q", err, specialTarget)
	}
	got, ok := readFileOr(t, filepath.Join(dir, filepath.FromSlash(specialTarget), "inner.txt"))
	if !ok || got != "keep special\n" {
		t.Fatalf("special target untracked content destroyed: %q (%v)", got, ok)
	}

	err = r.Discard(FileChange{Path: parentFile, Status: 'D'})
	if err == nil || !strings.Contains(err.Error(), "p 'two' b") {
		t.Fatalf("SSH discard with special blocked parent = %v, want refusal naming parent", err)
	}
	got, ok = readFileOr(t, filepath.Join(dir, "p 'two' b"))
	if !ok || got != "keep parent\n" {
		t.Fatalf("special parent blocker destroyed: %q (%v)", got, ok)
	}
}
