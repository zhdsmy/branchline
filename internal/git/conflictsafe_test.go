package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

func conflictRepo(dir string) *git.Repo {
	return git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
}

// conflictGitRunInput runs git with stdin, for index plumbing.
func conflictGitRunInput(t *testing.T, dir, stdin string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "--literal-pathspecs", "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// conflictMustIdentity reads the identity of path or fails.
func conflictMustIdentity(t *testing.T, r *git.Repo, path string) git.ConflictIdentity {
	t.Helper()
	st, err := r.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	id, ok := st.ConflictIdentity(path)
	if !ok {
		t.Fatalf("%s is not unmerged: %+v", path, st.Conflicts)
	}
	return id
}

// conflictStateSnapshot captures the bytes a destructive primitive could
// damage: the working tree file and the index stages of one path.
type conflictStateSnapshot struct {
	fileBytes []byte
	fileGone  bool
	index     string
}

func conflictSnapshot(t *testing.T, dir, path string) conflictStateSnapshot {
	t.Helper()
	var snap conflictStateSnapshot
	data, err := os.ReadFile(filepath.Join(dir, path))
	switch {
	case err == nil:
		snap.fileBytes = data
	case os.IsNotExist(err):
		snap.fileGone = true
	default:
		t.Fatalf("read %s: %v", path, err)
	}
	snap.index = conflictGitOut(t, dir, "ls-files", "--stage", "--", path)
	return snap
}

func conflictAssertSnapshot(t *testing.T, dir, path string, want conflictStateSnapshot) {
	t.Helper()
	got := conflictSnapshot(t, dir, path)
	if want.fileGone {
		if !got.fileGone {
			t.Errorf("%s reappeared in the working tree", path)
		}
	} else if got.fileGone || string(got.fileBytes) != string(want.fileBytes) {
		t.Errorf("%s working tree changed: got %q, want %q", path, got.fileBytes, want.fileBytes)
	}
	if got.index != want.index {
		t.Errorf("%s index changed:\n got %q\nwant %q", path, got.index, want.index)
	}
}

// conflictFailExecutor fails the git commands fail selects, for proving
// a failed read blocks every write.
type conflictFailExecutor struct {
	inner git.Executor
	fail  func(args []string) bool
}

func (f *conflictFailExecutor) Describe() string { return f.inner.Describe() }

func (f *conflictFailExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if f.fail != nil && f.fail(args) {
		return nil, errors.New("injected read failure")
	}
	return f.inner.Run(ctx, stdin, args...)
}

func conflictFailRepo(dir string, fail func(args []string) bool) *git.Repo {
	return git.New(&conflictFailExecutor{inner: &git.LocalExecutor{Dir: dir, NoGlobalConfig: true}, fail: fail})
}

func argsHave(args []string, want ...string) bool {
	if len(args) < len(want) {
		return false
	}
	for i := range want {
		if args[i] != want[i] {
			return false
		}
	}
	return true
}

// setupUUConflict builds a both-modified conflict on path, or uu.txt.
func setupUUConflict(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		path = "uu.txt"
	}
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, path, "base\nline2\nline3\n")
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictWriteFile(t, dir, path, "side\nline2\nline3\n")
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictWriteFile(t, dir, path, "main\nline2\nline3\n")
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")
	return dir
}

// setupDUConflict builds a conflict where our side deleted path and
// their side modified it.
func setupDUConflict(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		path = "du.txt"
	}
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, path, "base du\n")
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictWriteFile(t, dir, path, "side modified\n")
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictGitRun(t, dir, "rm", "-q", path)
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")
	return dir
}

// setupUDConflict builds a conflict where their side deleted path and
// our side modified it.
func setupUDConflict(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		path = "ud.txt"
	}
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, path, "base ud\n")
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictGitRun(t, dir, "rm", "-q", path)
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictWriteFile(t, dir, path, "main modified\n")
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")
	return dir
}

// setupSubmoduleConflict stages a gitlink conflict directly in the index:
// a real merge state on a submodule, with mode 160000 stages.
func setupSubmoduleConflict(t *testing.T) string {
	t.Helper()
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, "s.txt", "sup\n")
	conflictCommitAll(t, dir, "base")
	base := strings.Repeat("1", 40)
	ours := strings.Repeat("2", 40)
	theirs := strings.Repeat("3", 40)
	input := "160000 " + base + " 1\tlibsub\n" +
		"160000 " + ours + " 2\tlibsub\n" +
		"160000 " + theirs + " 3\tlibsub\n"
	conflictGitRunInput(t, dir, input, "update-index", "--index-info")
	return dir
}

func conflictFileBytes(t *testing.T, dir, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func conflictUnmergedEntries(t *testing.T, dir, path string) string {
	t.Helper()
	return conflictGitOut(t, dir, "ls-files", "--unmerged", "--", path)
}

// TestConflictSafeInspectTypes reads every conflict kind of one merge and
// checks stages, sides, worktree presence and conflict blocks.
func TestConflictSafeInspectTypes(t *testing.T) {
	dir := setupAllConflictTypes(t)
	r := conflictRepo(dir)

	uu, err := r.InspectConflict("uu.txt", 0)
	if err != nil {
		t.Fatalf("uu inspect: %v", err)
	}
	if !uu.HaveBase || !uu.HaveOurs || !uu.HaveTheirs || !uu.WorktreeExists {
		t.Errorf("uu flags %+v", uu)
	}
	if uu.OursText != "main uu\nline2\nline3\n" || uu.TheirsText != "side uu\nline2\nline3\n" || uu.BaseText != "base uu\nline2\nline3\n" {
		t.Errorf("uu sides ours=%q theirs=%q base=%q", uu.OursText, uu.TheirsText, uu.BaseText)
	}
	if len(uu.Hunks) != 1 || !uu.HasMarkers {
		t.Errorf("uu hunks %+v markers=%v", uu.Hunks, uu.HasMarkers)
	}
	if uu.Binary || uu.TooLarge || uu.IsSubmodule {
		t.Errorf("uu flags binary=%v large=%v submodule=%v", uu.Binary, uu.TooLarge, uu.IsSubmodule)
	}

	aa, err := r.InspectConflict("aa.txt", 0)
	if err != nil {
		t.Fatalf("aa inspect: %v", err)
	}
	if aa.HaveBase || aa.BaseText != "" || !aa.HaveOurs || !aa.HaveTheirs {
		t.Errorf("aa flags %+v", aa)
	}
	if aa.OursText != "aa main\n" || aa.TheirsText != "aa side\n" {
		t.Errorf("aa sides ours=%q theirs=%q", aa.OursText, aa.TheirsText)
	}

	du, err := r.InspectConflict("du.txt", 0)
	if err != nil {
		t.Fatalf("du inspect: %v", err)
	}
	if du.HaveOurs || !du.HaveTheirs || !du.WorktreeExists || du.OursText != "" {
		t.Errorf("du flags %+v", du)
	}
	if du.TheirsText != "side du modified\n" || len(du.Hunks) != 0 || du.HasMarkers {
		t.Errorf("du theirs=%q hunks=%+v markers=%v", du.TheirsText, du.Hunks, du.HasMarkers)
	}

	ud, err := r.InspectConflict("ud.txt", 0)
	if err != nil {
		t.Fatalf("ud inspect: %v", err)
	}
	if !ud.HaveOurs || ud.HaveTheirs || ud.TheirsText != "" {
		t.Errorf("ud flags %+v", ud)
	}
	if ud.OursText != "main ud modified\n" {
		t.Errorf("ud ours=%q", ud.OursText)
	}

	// Both-deleted is a valid inspection, not an error.
	dd, err := r.InspectConflict("dd.txt", 0)
	if err != nil {
		t.Fatalf("dd inspect: %v", err)
	}
	if dd.HaveOurs || dd.HaveTheirs || !dd.HaveBase || dd.WorktreeExists {
		t.Errorf("dd flags %+v", dd)
	}
	if dd.BaseText != "base dd\n" || dd.WorktreeText != "" {
		t.Errorf("dd base=%q worktree=%q", dd.BaseText, dd.WorktreeText)
	}

	au, err := r.InspectConflict("renamed_main.txt", 0)
	if err != nil {
		t.Fatalf("au inspect: %v", err)
	}
	if !au.HaveOurs || au.HaveTheirs || au.OursText != "base dd\n" {
		t.Errorf("au %+v", au)
	}

	ua, err := r.InspectConflict("renamed_side.txt", 0)
	if err != nil {
		t.Fatalf("ua inspect: %v", err)
	}
	if ua.HaveOurs || !ua.HaveTheirs || ua.TheirsText != "base dd\n" {
		t.Errorf("ua %+v", ua)
	}

	if _, err := r.InspectConflict("clean-missing.txt", 0); !errors.Is(err, git.ErrConflictNotUnmerged) {
		t.Errorf("clean path inspect err %v, want ErrConflictNotUnmerged", err)
	}
}

// TestConflictSafeInspectBinaryAndLarge checks binary contents stay
// undecoded and oversized contents are flagged instead of read.
func TestConflictSafeInspectBinaryAndLarge(t *testing.T) {
	dir := conflictInitRepo(t)
	conflictGitRun(t, dir, "config", "core.autocrlf", "false")
	writeBin := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeBin("bin.dat", []byte{'b', 'a', 's', 'e', 0, 1, 2, '\n'})
	conflictWriteFile(t, dir, "big.txt", strings.Repeat("base line\n", 40))
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	writeBin("bin.dat", []byte{'s', 'i', 'd', 'e', 0, 9, 9, '\n'})
	conflictWriteFile(t, dir, "big.txt", strings.Repeat("side line\n", 40))
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	writeBin("bin.dat", []byte{'m', 'a', 'i', 'n', 0, 5, 5, '\n'})
	conflictWriteFile(t, dir, "big.txt", strings.Repeat("main line\n", 40))
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")

	r := conflictRepo(dir)
	bin, err := r.InspectConflict("bin.dat", 0)
	if err != nil {
		t.Fatalf("binary inspect: %v", err)
	}
	if !bin.Binary {
		t.Errorf("binary not detected: %+v", bin)
	}
	if bin.OursText != "" || bin.TheirsText != "" || bin.WorktreeText != "" {
		t.Errorf("binary decoded as text: ours=%q theirs=%q worktree=%q", bin.OursText, bin.TheirsText, bin.WorktreeText)
	}

	big, err := r.InspectConflict("big.txt", 100)
	if err != nil {
		t.Fatalf("large inspect: %v", err)
	}
	if !big.TooLarge {
		t.Errorf("oversize not detected: %+v", big)
	}
	if big.OursText != "" || big.WorktreeText != "" {
		t.Errorf("oversize content read out: ours=%q worktree=%q", big.OursText, big.WorktreeText)
	}
	bigOK, err := r.InspectConflict("big.txt", 0)
	if err != nil || bigOK.TooLarge || bigOK.OursText == "" {
		t.Errorf("default limit should fit: tooLarge=%v ours=%q err=%v", bigOK.TooLarge, bigOK.OursText, err)
	}
}

// TestConflictSafeInspectZeroByteVsDeleted proves an existing zero-byte
// blob is a present side, never the same as a deleted one.
func TestConflictSafeInspectZeroByteVsDeleted(t *testing.T) {
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, "z.txt", "base z\n")
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictWriteFile(t, dir, "z.txt", "side content\n")
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictWriteFile(t, dir, "z.txt", "")
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")

	r := conflictRepo(dir)
	insp, err := r.InspectConflict("z.txt", 0)
	if err != nil {
		t.Fatalf("zero-byte inspect: %v", err)
	}
	if !insp.HaveOurs || insp.OursText != "" {
		t.Errorf("a zero-byte ours is a present, empty side: %+v", insp)
	}
	if !insp.HaveTheirs || insp.TheirsText != "side content\n" {
		t.Errorf("theirs %+v", insp)
	}

	// A deleted side reports false where the zero-byte side reports true.
	duDir := setupDUConflict(t, "")
	du, err := conflictRepo(duDir).InspectConflict("du.txt", 0)
	if err != nil {
		t.Fatalf("du inspect: %v", err)
	}
	if du.HaveOurs {
		t.Errorf("a deleted ours must not read as present: %+v", du)
	}
}

// TestConflictSafeInspectSubmodule leaves gitlink conflicts untouched:
// their objects live in the submodule, so nothing is read or decoded.
func TestConflictSafeInspectSubmodule(t *testing.T) {
	dir := setupSubmoduleConflict(t)
	r := conflictRepo(dir)
	insp, err := r.InspectConflict("libsub", 0)
	if err != nil {
		t.Fatalf("submodule inspect: %v", err)
	}
	if !insp.IsSubmodule {
		t.Errorf("gitlink conflict not detected: %+v", insp)
	}
	if insp.OursText != "" || insp.TheirsText != "" || insp.BaseText != "" || insp.WorktreeText != "" {
		t.Errorf("submodule contents read: %+v", insp)
	}
	if !insp.HaveOurs || !insp.HaveTheirs || !insp.HaveBase {
		t.Errorf("submodule stages %+v", insp)
	}

	id := conflictMustIdentity(t, r, "libsub")
	if err := r.SafeTakeConflictSide("libsub", "ours", id); err == nil {
		t.Error("took a side of a submodule conflict")
	}
	if err := r.SafeWriteResolvedConflict("libsub", "x\n", id); err == nil {
		t.Error("wrote a submodule conflict")
	}
}

// TestConflictSafeTakeSide resolves conflicts by choosing one side,
// including sides that are deletions.
func TestConflictSafeTakeSide(t *testing.T) {
	t.Run("UUours", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "ours", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictFileBytes(t, dir, "uu.txt"); got != "main\nline2\nline3\n" {
			t.Errorf("worktree %q", got)
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
	t.Run("UUtheirs", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictFileBytes(t, dir, "uu.txt"); got != "side\nline2\nline3\n" {
			t.Errorf("worktree %q", got)
		}
	})
	t.Run("DUoursDeletes", func(t *testing.T) {
		dir := setupDUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "du.txt")
		if err := r.SafeTakeConflictSide("du.txt", "ours", id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "du.txt")); !os.IsNotExist(err) {
			t.Errorf("du.txt stayed: %v", err)
		}
		if got := conflictUnmergedEntries(t, dir, "du.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
	t.Run("DUtheirsKeeps", func(t *testing.T) {
		dir := setupDUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "du.txt")
		if err := r.SafeTakeConflictSide("du.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictFileBytes(t, dir, "du.txt"); got != "side modified\n" {
			t.Errorf("worktree %q", got)
		}
	})
	t.Run("UDtheirsDeletes", func(t *testing.T) {
		dir := setupUDConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "ud.txt")
		if err := r.SafeTakeConflictSide("ud.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "ud.txt")); !os.IsNotExist(err) {
			t.Errorf("ud.txt stayed: %v", err)
		}
	})
	t.Run("AAours", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "aa.txt")
		if err := r.SafeTakeConflictSide("aa.txt", "ours", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictFileBytes(t, dir, "aa.txt"); got != "aa main\n" {
			t.Errorf("worktree %q", got)
		}
	})
	t.Run("DDrefused", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "dd.txt")
		snap := conflictSnapshot(t, dir, "renamed_main.txt")
		err := r.SafeTakeConflictSide("dd.txt", "ours", id)
		if err == nil {
			t.Error("took a side of a both-deleted conflict")
		}
		if got := conflictUnmergedEntries(t, dir, "dd.txt"); got == "" {
			t.Error("dd.txt lost its stages")
		}
		conflictAssertSnapshot(t, dir, "renamed_main.txt", snap)
	})
	t.Run("AUtheirsDeletes", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "renamed_main.txt")
		if err := r.SafeTakeConflictSide("renamed_main.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "renamed_main.txt")); !os.IsNotExist(err) {
			t.Errorf("renamed_main.txt stayed: %v", err)
		}
		if got := conflictUnmergedEntries(t, dir, "renamed_main.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
	t.Run("BadSide", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		snap := conflictSnapshot(t, dir, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "sideways", id); err == nil {
			t.Error("an unknown side resolved")
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
}

// TestConflictSafeDeleteFile stages deletions only for conflicts where a
// side is missing, never for both-modified or both-added files.
func TestConflictSafeDeleteFile(t *testing.T) {
	t.Run("DD", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "dd.txt")
		if err := r.SafeDeleteConflictFile("dd.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "dd.txt"); got != "" {
			t.Errorf("dd.txt still unmerged %q", got)
		}
		// The rename targets and other conflicts are untouched.
		if got := conflictUnmergedEntries(t, dir, "renamed_main.txt"); got == "" {
			t.Error("renamed_main.txt lost its stages")
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got == "" {
			t.Error("uu.txt lost its stages")
		}
		if got := conflictFileBytes(t, dir, "uu.txt"); !strings.Contains(got, "<<<<<<<") {
			t.Errorf("uu.txt worktree changed %q", got)
		}
	})
	t.Run("DU", func(t *testing.T) {
		dir := setupDUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "du.txt")
		if err := r.SafeDeleteConflictFile("du.txt", id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "du.txt")); !os.IsNotExist(err) {
			t.Errorf("du.txt stayed: %v", err)
		}
	})
	t.Run("UD", func(t *testing.T) {
		dir := setupUDConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "ud.txt")
		if err := r.SafeDeleteConflictFile("ud.txt", id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "ud.txt")); !os.IsNotExist(err) {
			t.Errorf("ud.txt stayed: %v", err)
		}
	})
	t.Run("UUrefused", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		snap := conflictSnapshot(t, dir, "uu.txt")
		if err := r.SafeDeleteConflictFile("uu.txt", id); err == nil {
			t.Error("deleted a both-modified conflict")
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
	t.Run("AArefused", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "aa.txt")
		snap := conflictSnapshot(t, dir, "aa.txt")
		if err := r.SafeDeleteConflictFile("aa.txt", id); err == nil {
			t.Error("deleted a both-added conflict")
		}
		conflictAssertSnapshot(t, dir, "aa.txt", snap)
	})
}

// TestConflictSafeMarkersRefusedOrStaged stages the working tree file, refusing any
// file that still carries conflict markers.
func TestConflictSafeMarkersRefusedOrStaged(t *testing.T) {
	t.Run("MarkersRefused", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		snap := conflictSnapshot(t, dir, "uu.txt")
		err := r.SafeMarkConflictResolved("uu.txt", id)
		if !errors.Is(err, git.ErrConflictMarkersRemain) {
			t.Errorf("err %v, want ErrConflictMarkersRemain", err)
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
	t.Run("HalfMarkersRefused", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		// The user edited but left the closing marker behind.
		conflictWriteFile(t, dir, "uu.txt", "main\nline2\nline3\n>>>>>>> side\n")
		snap := conflictSnapshot(t, dir, "uu.txt")
		err := r.SafeMarkConflictResolved("uu.txt", id)
		if !errors.Is(err, git.ErrConflictMarkersRemain) {
			t.Errorf("err %v, want ErrConflictMarkersRemain", err)
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
	t.Run("CleanFileStages", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		conflictWriteFile(t, dir, "uu.txt", "resolved by hand\n")
		if err := r.SafeMarkConflictResolved("uu.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
		if staged := conflictGitOut(t, dir, "show", ":uu.txt"); staged != "resolved by hand\n" {
			t.Errorf("staged %q", staged)
		}
	})
	t.Run("ZeroByteStages", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		// Emptying the file is a valid manual resolution.
		conflictWriteFile(t, dir, "uu.txt", "")
		if err := r.SafeMarkConflictResolved("uu.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
	t.Run("BinaryStagesWithoutScan", func(t *testing.T) {
		dir := conflictInitRepo(t)
		writeBin := func(data []byte) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "b.dat"), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		writeBin([]byte{'b', 0, '\n'})
		conflictCommitAll(t, dir, "base")
		conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
		writeBin([]byte{'s', 0, '\n'})
		conflictCommitAll(t, dir, "side")
		conflictGitRun(t, dir, "checkout", "-q", "main")
		writeBin([]byte{'m', 0, '\n'})
		conflictCommitAll(t, dir, "main")
		conflictMerge(t, dir, "side")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "b.dat")
		// Binary content never counts as leftover markers, even when the
		// bytes happen to hold marker-looking text after a NUL.
		writeBin(append([]byte{'x', 0, '\n'}, []byte(">>>>>>> side\n")...))
		if err := r.SafeMarkConflictResolved("b.dat", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "b.dat"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
	t.Run("DDstagesDeletion", func(t *testing.T) {
		dir := setupAllConflictTypes(t)
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "dd.txt")
		if err := r.SafeMarkConflictResolved("dd.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "dd.txt"); got != "" {
			t.Errorf("dd.txt still unmerged %q", got)
		}
	})
	t.Run("UserDeletedStagesDeletion", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		if err := os.Remove(filepath.Join(dir, "uu.txt")); err != nil {
			t.Fatal(err)
		}
		if err := r.SafeMarkConflictResolved("uu.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})
}

// TestConflictSafeWriteResolved writes resolutions through the object
// store, preserving modes and refusing leftover markers.
func TestConflictSafeWriteResolved(t *testing.T) {
	t.Run("BasicWrite", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		if err := r.SafeWriteResolvedConflict("uu.txt", "written resolution\n", id); err != nil {
			t.Fatal(err)
		}
		if got := conflictFileBytes(t, dir, "uu.txt"); got != "written resolution\n" {
			t.Errorf("worktree %q", got)
		}
		if got := conflictUnmergedEntries(t, dir, "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
		if staged := conflictGitOut(t, dir, "show", ":uu.txt"); staged != "written resolution\n" {
			t.Errorf("staged %q", staged)
		}
	})
	t.Run("MarkerContentRefused", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		snap := conflictSnapshot(t, dir, "uu.txt")
		err := r.SafeWriteResolvedConflict("uu.txt", "line\n=======\n", id)
		if !errors.Is(err, git.ErrConflictMarkersRemain) {
			t.Errorf("err %v, want ErrConflictMarkersRemain", err)
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
	t.Run("ZeroByteWrite", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		if err := r.SafeWriteResolvedConflict("uu.txt", "", id); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "uu.txt"))
		if err != nil || len(data) != 0 {
			t.Errorf("zero-byte write: %q %v", data, err)
		}
	})
	t.Run("ExecutableModePreserved", func(t *testing.T) {
		dir := conflictInitRepo(t)
		full := filepath.Join(dir, "x.sh")
		if err := os.WriteFile(full, []byte("#!/bin/sh\necho base\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		conflictCommitAll(t, dir, "base")
		conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
		conflictWriteFile(t, dir, "x.sh", "#!/bin/sh\necho side\n")
		if err := os.Chmod(full, 0o755); err != nil {
			t.Fatal(err)
		}
		conflictCommitAll(t, dir, "side")
		conflictGitRun(t, dir, "checkout", "-q", "main")
		conflictWriteFile(t, dir, "x.sh", "#!/bin/sh\necho main\n")
		if err := os.Chmod(full, 0o755); err != nil {
			t.Fatal(err)
		}
		conflictCommitAll(t, dir, "main")
		conflictMerge(t, dir, "side")

		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "x.sh")
		if err := r.SafeWriteResolvedConflict("x.sh", "#!/bin/sh\necho resolved\n", id); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("executable bit lost: mode %o", info.Mode().Perm())
		}
		stageLine := strings.TrimSpace(conflictGitOut(t, dir, "ls-files", "--stage", "--", "x.sh"))
		if !strings.HasPrefix(stageLine, "100755") {
			t.Errorf("index mode %q", stageLine)
		}
	})
}

// TestConflictSafeSpecialPaths resolves conflicts on paths with glob
// characters, leading dashes, spaces, unicode and commas.
func TestConflictSafeSpecialPaths(t *testing.T) {
	targets := []string{"conflict[ab].txt", "--option-like.txt", "dir with space/冲突 αβγ.txt", "comma,name.txt"}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			dir := setupUUConflict(t, target)
			r := conflictRepo(dir)
			// A bystander that glob expansion would hit for the [ab] name.
			conflictWriteFile(t, dir, "bystander-note.txt", "bystander clean\n")
			id := conflictMustIdentity(t, r, target)
			if err := r.SafeWriteResolvedConflict(target, "resolved special\n", id); err != nil {
				t.Fatal(err)
			}
			if got := conflictFileBytes(t, dir, target); got != "resolved special\n" {
				t.Errorf("worktree %q", got)
			}
			if got := conflictFileBytes(t, dir, "bystander-note.txt"); got != "bystander clean\n" {
				t.Errorf("bystander changed %q", got)
			}
			if got := conflictUnmergedEntries(t, dir, target); got != "" {
				t.Errorf("still unmerged %q", got)
			}
		})
	}
}

// TestConflictSafeLiteralPathspecIsolation proves a conflicted path that
// starts with pathspec magic is treated literally: the neighbor file the
// glob would match must survive resolution untouched.
func TestConflictSafeLiteralPathspecIsolation(t *testing.T) {
	target := ":(glob)conflict[ab].txt"
	neighbor := "conflicta.txt"
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, target, "base target\n")
	conflictWriteFile(t, dir, neighbor, "base neighbor\n")
	conflictCommitAll(t, dir, "base")
	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictWriteFile(t, dir, target, "side target\n")
	conflictWriteFile(t, dir, neighbor, "side neighbor\n")
	conflictCommitAll(t, dir, "side")
	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictWriteFile(t, dir, target, "main target\n")
	conflictWriteFile(t, dir, neighbor, "main neighbor\n")
	conflictCommitAll(t, dir, "main")
	conflictMerge(t, dir, "side")

	r := conflictRepo(dir)
	id := conflictMustIdentity(t, r, target)
	neighborBefore := conflictSnapshot(t, dir, neighbor)
	if err := r.SafeTakeConflictSide(target, "theirs", id); err != nil {
		t.Fatal(err)
	}
	if got := conflictFileBytes(t, dir, target); got != "side target\n" {
		t.Errorf("target %q", got)
	}
	// The glob conflicta* must never have expanded: the neighbor keeps
	// its own conflict, its markers and its stages.
	conflictAssertSnapshot(t, dir, neighbor, neighborBefore)
	if got := conflictUnmergedEntries(t, dir, neighbor); got == "" {
		t.Error("the neighbor conflict was resolved by glob expansion")
	}
	if got := conflictFileBytes(t, dir, neighbor); !strings.Contains(got, "<<<<<<<") {
		t.Errorf("neighbor worktree overwritten: %q", got)
	}
}

// TestConflictSafeReadFailureProtects proves a failing status, ls-files or
// hash-object read makes the write primitives fail closed, with the
// working tree and the index byte-identical afterwards. Each fail point is
// tested against the primitives whose reads actually depend on it: status
// and ls-files guard everything, while hash-object carries the content
// reads of the scan-and-write primitives.
func TestConflictSafeReadFailureProtects(t *testing.T) {
	type primitive struct {
		name string
		run  func(r *git.Repo, path string, id git.ConflictIdentity) error
	}
	primitives := []primitive{
		{"take", func(r *git.Repo, path string, id git.ConflictIdentity) error {
			return r.SafeTakeConflictSide(path, "ours", id)
		}},
		{"delete", func(r *git.Repo, path string, id git.ConflictIdentity) error {
			return r.SafeDeleteConflictFile(path, id)
		}},
		{"mark", func(r *git.Repo, path string, id git.ConflictIdentity) error {
			return r.SafeMarkConflictResolved(path, id)
		}},
		{"write", func(r *git.Repo, path string, id git.ConflictIdentity) error {
			return r.SafeWriteResolvedConflict(path, "overwrite\n", id)
		}},
	}
	statusFail := func(args []string) bool { return argsHave(args, "status", "--porcelain=v2") }
	lsFilesFail := func(args []string) bool {
		return argsHave(args, "ls-files") || argsHave(args, "--literal-pathspecs", "ls-files")
	}
	hashFail := func(args []string) bool { return argsHave(args, "hash-object") }
	failPoints := []struct {
		name string
		fail func(args []string) bool
		// target chooses the conflict setup: delete needs a conflict it may
		// legally resolve, or its refusal proves nothing about the guard.
		prims []primitive
	}{
		{"status", statusFail, primitives},
		{"ls-files", lsFilesFail, primitives},
		{"hash-object", hashFail, primitives[2:]},
	}
	for _, fp := range failPoints {
		for _, prim := range fp.prims {
			t.Run(fp.name+"/"+prim.name, func(t *testing.T) {
				path := "uu.txt"
				dir := setupUUConflict(t, "")
				if prim.name == "delete" {
					path = "du.txt"
					dir = setupDUConflict(t, "")
				}
				real := conflictRepo(dir)
				id := conflictMustIdentity(t, real, path)
				snap := conflictSnapshot(t, dir, path)
				r := conflictFailRepo(dir, fp.fail)
				if err := prim.run(r, path, id); err == nil {
					t.Errorf("%s succeeded with a failing %s read", prim.name, fp.name)
				}
				conflictAssertSnapshot(t, dir, path, snap)
			})
		}
	}
}

// TestConflictSafeNotUnmergedAfterExternalResolution proves a conflict resolved
// elsewhere is never overwritten: once the path is merged, every write
// primitive refuses to touch it.
func TestConflictSafeNotUnmergedAfterExternalResolution(t *testing.T) {
	dir := setupUUConflict(t, "")
	r := conflictRepo(dir)
	id := conflictMustIdentity(t, r, "uu.txt")
	// Someone resolves the conflict outside the application.
	conflictGitRun(t, dir, "add", "--", "uu.txt")
	if st, err := r.Status(); err != nil || st.Conflicted() {
		t.Fatalf("still conflicted %+v %v", st, err)
	}
	resolved := conflictFileBytes(t, dir, "uu.txt")

	for name, act := range map[string]func() error{
		"take":  func() error { return r.SafeTakeConflictSide("uu.txt", "theirs", id) },
		"mark":  func() error { return r.SafeMarkConflictResolved("uu.txt", id) },
		"write": func() error { return r.SafeWriteResolvedConflict("uu.txt", "clobber\n", id) },
	} {
		err := act()
		if !errors.Is(err, git.ErrConflictNotUnmerged) {
			t.Errorf("%s err %v, want ErrConflictNotUnmerged", name, err)
		}
		if got := conflictFileBytes(t, dir, "uu.txt"); got != resolved {
			t.Errorf("%s overwrote the resolved file: %q", name, got)
		}
	}
}

// TestConflictSafeChangedIdentityProtects replays a stale identity after
// the conflict moved on: changed stage objects, a moved HEAD or a changed
// operation all fail closed without touching bytes.
func TestConflictSafeChangedIdentityProtects(t *testing.T) {
	t.Run("StageOIDChanged", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		// Another process replaced the ours stage with new content.
		base := strings.TrimSpace(conflictGitOut(t, dir, "rev-parse", ":1:uu.txt"))
		theirs := strings.TrimSpace(conflictGitOut(t, dir, "rev-parse", ":3:uu.txt"))
		newOurs := strings.TrimSpace(conflictGitOutInput(t, dir, "tampered ours\n", "hash-object", "-w", "--stdin"))
		input := "100644 " + base + " 1\tuu.txt\n" +
			"100644 " + newOurs + " 2\tuu.txt\n" +
			"100644 " + theirs + " 3\tuu.txt\n"
		conflictGitRunInput(t, dir, input, "update-index", "--index-info")

		snap := conflictSnapshot(t, dir, "uu.txt")
		for name, act := range map[string]func() error{
			"take":  func() error { return r.SafeTakeConflictSide("uu.txt", "ours", id) },
			"mark":  func() error { return r.SafeMarkConflictResolved("uu.txt", id) },
			"write": func() error { return r.SafeWriteResolvedConflict("uu.txt", "clobber\n", id) },
		} {
			if err := act(); !errors.Is(err, git.ErrConflictStateChanged) {
				t.Errorf("%s err %v, want ErrConflictStateChanged", name, err)
			}
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})

	t.Run("HeadMoved", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		base := strings.TrimSpace(conflictGitOut(t, dir, "rev-parse", "HEAD~1"))
		conflictGitRun(t, dir, "update-ref", "HEAD", base)
		snap := conflictSnapshot(t, dir, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "ours", id); !errors.Is(err, git.ErrConflictStateChanged) {
			t.Errorf("err %v, want ErrConflictStateChanged", err)
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})

	t.Run("OperationChanged", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "uu.txt")
		// MERGE_HEAD moving means a different merge is in progress.
		head := strings.TrimSpace(conflictGitOut(t, dir, "rev-parse", "HEAD"))
		if err := os.WriteFile(filepath.Join(dir, ".git", "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		snap := conflictSnapshot(t, dir, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "ours", id); !errors.Is(err, git.ErrConflictStateChanged) {
			t.Errorf("err %v, want ErrConflictStateChanged", err)
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})

	t.Run("DeleteGuardedToo", func(t *testing.T) {
		dir := setupDUConflict(t, "")
		r := conflictRepo(dir)
		id := conflictMustIdentity(t, r, "du.txt")
		base := strings.TrimSpace(conflictGitOut(t, dir, "rev-parse", "HEAD~1"))
		conflictGitRun(t, dir, "update-ref", "HEAD", base)
		snap := conflictSnapshot(t, dir, "du.txt")
		if err := r.SafeDeleteConflictFile("du.txt", id); !errors.Is(err, git.ErrConflictStateChanged) {
			t.Errorf("err %v, want ErrConflictStateChanged", err)
		}
		conflictAssertSnapshot(t, dir, "du.txt", snap)
	})

	t.Run("WrongIdentityInput", func(t *testing.T) {
		dir := setupUUConflict(t, "")
		r := conflictRepo(dir)
		snap := conflictSnapshot(t, dir, "uu.txt")
		var zero git.ConflictIdentity
		if err := r.SafeTakeConflictSide("uu.txt", "ours", zero); err == nil {
			t.Error("an empty identity resolved a conflict")
		}
		conflictAssertSnapshot(t, dir, "uu.txt", snap)
	})
}

// conflictGitOutInput runs git with stdin and returns standard output.
func conflictGitOutInput(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "--literal-pathspecs", "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// sshMergeConflict builds a both-modified conflict on the server: path
// has baseBody in the base commit, then side and main edit its first line.
func sshMergeConflict(t *testing.T, srv *sshtest.Target, dir, path, baseBody, sideBody, mainBody string) {
	t.Helper()
	q := git.ShellQuote
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + q(dir) + `
printf '%s' ` + q(baseBody) + ` > ` + q(path) + `
git add -A
git commit -q -m base
git checkout -q -b side
printf '%s' ` + q(sideBody) + ` > ` + q(path) + `
git add -A
git commit -q -m side
git checkout -q main
printf '%s' ` + q(mainBody) + ` > ` + q(path) + `
git add -A
git commit -q -m main
if git merge --no-edit side > merge.out 2>&1; then
  echo "merge unexpectedly succeeded" >&2
  exit 1
fi
grep -q CONFLICT merge.out
`
	srv.RunShell(t, script)
}

// sshSnapshot reads the working tree bytes and index stages of path on the
// server, as a single comparable string.
func sshSnapshot(t *testing.T, srv *sshtest.Target, dir, path string) string {
	t.Helper()
	q := git.ShellQuote
	script := `cd ` + q(dir) + `
if [ -e ` + q(path) + ` ]; then echo "exists"; else echo "absent"; fi
od -c ` + q(path) + ` 2>/dev/null || true
echo "---index---"
git ls-files --stage -- ` + q(path)
	return strings.TrimRight(srv.RunShell(t, script), "\r\n")
}

// TestConflictSafeLoopbackSSH exercises the conflict primitives against a
// real sshd on loopback: the same reads, writes and data-safety guards as
// on this machine.
func TestConflictSafeLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)

	t.Run("InspectUU", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-inspect-uu")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r := srv.Repo(t, dir)
		insp, err := r.InspectConflict("uu.txt", 0)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if !insp.HaveBase || !insp.HaveOurs || !insp.HaveTheirs || !insp.WorktreeExists {
			t.Errorf("flags %+v", insp)
		}
		if insp.OursText != "main\nline2\n" || insp.TheirsText != "side\nline2\n" || insp.BaseText != "base\nline2\n" {
			t.Errorf("sides ours=%q theirs=%q base=%q", insp.OursText, insp.TheirsText, insp.BaseText)
		}
		if len(insp.Hunks) != 1 || !insp.HasMarkers {
			t.Errorf("hunks %+v markers=%v", insp.Hunks, insp.HasMarkers)
		}
		if insp.Identity.State != "merge" || insp.Identity.Head == "" || insp.Identity.Operation == "" {
			t.Errorf("identity %+v", insp.Identity)
		}
	})

	t.Run("TakeSideUU", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-take-uu")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st.Conflicts)
		}
		if err := r.SafeTakeConflictSide("uu.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		if got := string(srv.ReadFile(t, dir, "uu.txt")); got != "side\nline2\n" {
			t.Errorf("worktree %q", got)
		}
		if got := srv.RunGit(t, dir, "ls-files", "--unmerged", "--", "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})

	t.Run("TakeSideDUoursDeletes", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-take-du")
		q := git.ShellQuote
		script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + q(dir) + `
printf 'base du\n' > du.txt
git add -A
git commit -q -m base
git checkout -q -b side
printf 'side modified\n' > du.txt
git add -A
git commit -q -m side
git checkout -q main
git rm -q du.txt
git commit -q -m main
if git merge --no-edit side > merge.out 2>&1; then
  echo "merge unexpectedly succeeded" >&2
  exit 1
fi
grep -q CONFLICT merge.out
`
		srv.RunShell(t, script)
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("du.txt")
		if !ok {
			t.Fatalf("du.txt not unmerged: %+v", st.Conflicts)
		}
		if err := r.SafeTakeConflictSide("du.txt", "ours", id); err != nil {
			t.Fatal(err)
		}
		srv.RunShell(t, "! test -e "+q(dir)+"/du.txt")
		if got := srv.RunGit(t, dir, "ls-files", "--unmerged", "--", "du.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})

	t.Run("TakeSideUDtheirsDeletes", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-take-ud")
		q := git.ShellQuote
		script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + q(dir) + `
printf 'base ud\n' > ud.txt
git add -A
git commit -q -m base
git checkout -q -b side
git rm -q ud.txt
git commit -q -m side
git checkout -q main
printf 'main modified\n' > ud.txt
git add -A
git commit -q -m main
if git merge --no-edit side > merge.out 2>&1; then
  echo "merge unexpectedly succeeded" >&2
  exit 1
fi
grep -q CONFLICT merge.out
`
		srv.RunShell(t, script)
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("ud.txt")
		if !ok {
			t.Fatalf("ud.txt not unmerged: %+v", st.Conflicts)
		}
		if err := r.SafeTakeConflictSide("ud.txt", "theirs", id); err != nil {
			t.Fatal(err)
		}
		srv.RunShell(t, "! test -e "+q(dir)+"/ud.txt")
	})

	t.Run("DeleteDD", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-del-dd")
		q := git.ShellQuote
		script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + q(dir) + `
printf 'base dd\n' > dd.txt
git add -A
git commit -q -m base
git checkout -q -b side
git mv dd.txt renamed_side.txt
git commit -q -m side
git checkout -q main
git mv dd.txt renamed_main.txt
git commit -q -m main
if git merge --no-edit side > merge.out 2>&1; then
  echo "merge unexpectedly succeeded" >&2
  exit 1
fi
grep -q CONFLICT merge.out
`
		srv.RunShell(t, script)
		r := srv.Repo(t, dir)
		insp, err := r.InspectConflict("dd.txt", 0)
		if err != nil {
			t.Fatalf("dd inspect over ssh: %v", err)
		}
		if insp.WorktreeExists || insp.HaveOurs || insp.HaveTheirs || !insp.HaveBase {
			t.Errorf("dd flags %+v", insp)
		}
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("dd.txt")
		if !ok {
			t.Fatalf("dd.txt not unmerged: %+v", st.Conflicts)
		}
		if err := r.SafeDeleteConflictFile("dd.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := srv.RunGit(t, dir, "ls-files", "--unmerged", "--", "dd.txt"); got != "" {
			t.Errorf("dd.txt still unmerged %q", got)
		}
	})

	t.Run("MarkResolvedGuardThenSuccess", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-mark")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st.Conflicts)
		}
		before := sshSnapshot(t, srv, dir, "uu.txt")
		if err := r.SafeMarkConflictResolved("uu.txt", id); !errors.Is(err, git.ErrConflictMarkersRemain) {
			t.Errorf("err %v, want ErrConflictMarkersRemain", err)
		}
		if got := sshSnapshot(t, srv, dir, "uu.txt"); got != before {
			t.Errorf("rejected mark changed state:\n got %q\nwant %q", got, before)
		}
		srv.WriteFile(t, dir, "uu.txt", []byte("resolved by hand\n"))
		if err := r.SafeMarkConflictResolved("uu.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := srv.RunGit(t, dir, "ls-files", "--unmerged", "--", "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
		if got := srv.RunGit(t, dir, "show", ":uu.txt"); got != "resolved by hand" {
			t.Errorf("staged %q", got)
		}
	})

	t.Run("MarkResolvedZeroByte", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-mark-zero")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st.Conflicts)
		}
		srv.WriteFile(t, dir, "uu.txt", nil)
		if err := r.SafeMarkConflictResolved("uu.txt", id); err != nil {
			t.Fatal(err)
		}
		if got := srv.RunGit(t, dir, "ls-files", "--unmerged", "--", "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})

	t.Run("WriteResolvedExecutableAndEmpty", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-write-exec")
		q := git.ShellQuote
		script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + q(dir) + `
printf '#!/bin/sh\necho base\n' > x.sh
chmod +x x.sh
git add -A
git commit -q -m base
git checkout -q -b side
printf '#!/bin/sh\necho side\n' > x.sh
chmod +x x.sh
git add -A
git commit -q -m side
git checkout -q main
printf '#!/bin/sh\necho main\n' > x.sh
chmod +x x.sh
git add -A
git commit -q -m main
if git merge --no-edit side > merge.out 2>&1; then
  echo "merge unexpectedly succeeded" >&2
  exit 1
fi
grep -q CONFLICT merge.out
`
		srv.RunShell(t, script)
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("x.sh")
		if !ok {
			t.Fatalf("x.sh not unmerged: %+v", st.Conflicts)
		}
		if err := r.SafeWriteResolvedConflict("x.sh", "#!/bin/sh\necho resolved\n", id); err != nil {
			t.Fatal(err)
		}
		srv.RunShell(t, "test -x "+q(dir)+"/x.sh")
		stageLine := srv.RunGit(t, dir, "ls-files", "--stage", "--", "x.sh")
		if !strings.HasPrefix(stageLine, "100755") {
			t.Errorf("index mode %q", stageLine)
		}
		if got := string(srv.ReadFile(t, dir, "x.sh")); got != "#!/bin/sh\necho resolved\n" {
			t.Errorf("worktree %q", got)
		}

		// A mismatched identity writes nothing.
		if err := r.SafeWriteResolvedConflict("x.sh", "", git.ConflictIdentity{}); err == nil {
			t.Fatal("an empty identity wrote a resolution")
		}

		// An empty resolution writes a zero-byte file, on a fresh conflict.
		dir2 := srv.InitRepo(t, "cs-write-empty")
		sshMergeConflict(t, srv, dir2, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r2 := srv.Repo(t, dir2)
		st2, err := r2.Status()
		if err != nil {
			t.Fatal(err)
		}
		emptyID, ok := st2.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st2.Conflicts)
		}
		if err := r2.SafeWriteResolvedConflict("uu.txt", "", emptyID); err != nil {
			t.Fatal(err)
		}
		if got := string(srv.ReadFile(t, dir2, "uu.txt")); got != "" {
			t.Errorf("zero-byte write %q", got)
		}
		if got := srv.RunGit(t, dir2, "ls-files", "--unmerged", "--", "uu.txt"); got != "" {
			t.Errorf("still unmerged %q", got)
		}
	})

	t.Run("StaleIdentityAndReadFailure", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-guards")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		r := srv.Repo(t, dir)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st.Conflicts)
		}

		// A stale identity from before an external resolution is refused,
		// and the resolved file is never overwritten.
		srv.RunGit(t, dir, "add", "--", "uu.txt")
		before := sshSnapshot(t, srv, dir, "uu.txt")
		if err := r.SafeTakeConflictSide("uu.txt", "ours", id); !errors.Is(err, git.ErrConflictNotUnmerged) {
			t.Errorf("take err %v, want ErrConflictNotUnmerged", err)
		}
		if err := r.SafeWriteResolvedConflict("uu.txt", "clobber\n", id); !errors.Is(err, git.ErrConflictNotUnmerged) {
			t.Errorf("write err %v, want ErrConflictNotUnmerged", err)
		}
		if got := sshSnapshot(t, srv, dir, "uu.txt"); got != before {
			t.Errorf("resolved file changed:\n got %q\nwant %q", got, before)
		}
	})

	t.Run("ReadFailureProtectsSSH", func(t *testing.T) {
		dir := srv.InitRepo(t, "cs-readfail")
		sshMergeConflict(t, srv, dir, "uu.txt", "base\nline2\n", "side\nline2\n", "main\nline2\n")
		ex := srv.Executor(t, dir)
		r := git.New(ex)
		st, err := r.Status()
		if err != nil {
			t.Fatal(err)
		}
		id, ok := st.ConflictIdentity("uu.txt")
		if !ok {
			t.Fatalf("uu.txt not unmerged: %+v", st.Conflicts)
		}
		before := sshSnapshot(t, srv, dir, "uu.txt")
		failing := git.New(&conflictFailExecutor{
			inner: ex,
			fail:  func(args []string) bool { return argsHave(args, "status", "--porcelain=v2") },
		})
		if err := failing.SafeTakeConflictSide("uu.txt", "ours", id); err == nil {
			t.Error("take succeeded with a failing status read")
		}
		if err := failing.SafeMarkConflictResolved("uu.txt", id); err == nil {
			t.Error("mark succeeded with a failing status read")
		}
		if err := failing.SafeWriteResolvedConflict("uu.txt", "clobber\n", id); err == nil {
			t.Error("write succeeded with a failing status read")
		}
		if got := sshSnapshot(t, srv, dir, "uu.txt"); got != before {
			t.Errorf("read failure changed state:\n got %q\nwant %q", got, before)
		}
	})
}
