package git

import (
	"os"
	"os/exec"
	"path/filepath"
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
