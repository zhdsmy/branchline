package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// filterFixture is a working tree whose repository defines a filter that
// marks the test's own temporary directory when it runs, so the tests prove
// a status read never executes it. The marker commands never touch anything
// outside the fixture's directory.
type filterFixture struct {
	dir      string
	repoDir  string
	cleanSh  string
	procSh   string
	cleanLog string
	procLog  string
}

func newFilterFixture(t *testing.T, name string) *filterFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &filterFixture{dir: dir, repoDir: filepath.Join(dir, "repo")}
	if err := os.MkdirAll(f.repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.cleanLog = filepath.Join(dir, "clean.log")
	f.procLog = filepath.Join(dir, "process.log")
	f.cleanSh = filepath.Join(dir, "filter-clean.sh")
	f.procSh = filepath.Join(dir, "filter-process.sh")
	writeScript(t, f.cleanSh, f.cleanLog)
	writeScript(t, f.procSh, f.procLog)
	return f
}

func writeScript(t *testing.T, path, log string) {
	t.Helper()
	script := "#!/bin/sh\necho ran >> " + git.ShellQuote(log) + "\nexec cat\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *filterFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.repoDir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// init commits a text file pointed at filter x, with the stat information of
// the commit freshly in the index.
func (f *filterFixture) init(t *testing.T) {
	t.Helper()
	f.git(t, "init", "-q", "-b", "main")
	f.git(t, "config", "user.name", "Branchline Test")
	f.git(t, "config", "user.email", "test@branchline.invalid")
	f.git(t, "config", "filter.x.clean", "sh "+f.cleanSh)
	if err := os.WriteFile(filepath.Join(f.repoDir, ".gitattributes"), []byte("*.txt filter=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repoDir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(t, "add", ".gitattributes", "f.txt")
	f.git(t, "commit", "-q", "-m", "initial")
	f.resetMarks(t)
}

// resetMarks removes the marker logs so a later presence check is fresh.
func (f *filterFixture) resetMarks(t *testing.T) {
	t.Helper()
	for _, log := range []string{f.cleanLog, f.procLog} {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func (f *filterFixture) marked(t *testing.T, log string) bool {
	t.Helper()
	_, err := os.Stat(log)
	return err == nil
}

// backdate gives f.txt a modification time far from the index entry without
// changing its content, as an unpacked archive would: status must hash the
// file, which is when git runs the clean or process filter.
func (f *filterFixture) backdate(t *testing.T) {
	t.Helper()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(f.repoDir, "f.txt"), old, old); err != nil {
		t.Fatal(err)
	}
}

func (f *filterFixture) rewrite(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repoDir, "f.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *filterFixture) repo() *git.Repo {
	return git.New(&git.LocalExecutor{Dir: f.repoDir})
}

// controlStatus runs plain git status outside the app's executor, with the
// filters live, proving the fixture itself triggers them.
func (f *filterFixture) controlStatus(t *testing.T) {
	t.Helper()
	f.git(t, "status", "--porcelain=v2", "-z")
}

func unstagedHas(st git.Status, path string) bool {
	for _, fc := range st.Unstaged {
		if fc.Path == path {
			return true
		}
	}
	return false
}

// A repository's own clean filter never runs when the app reads the status
// of a checkout whose stat information no longer matches the index, while
// the same read through plain git runs it.
func TestUntrustedFilterCleanNeverRunsOnStatusRead(t *testing.T) {
	f := newFilterFixture(t, "clean")
	f.init(t)
	f.backdate(t)

	if _, err := f.repo().StatusOpts(false); err != nil {
		t.Fatalf("status read: %v", err)
	}
	if f.marked(t, f.cleanLog) {
		t.Fatal("the repository's own clean filter ran on a passive status read")
	}

	// The control proves the experiment: plain git, run the way a terminal
	// would, does execute the filter for this checkout.
	f.controlStatus(t)
	if !f.marked(t, f.cleanLog) {
		t.Fatal("control: plain git status did not run the filter, the fixture is broken")
	}
}

// The blocked status read still answers correctly: a file whose content is
// unchanged after conversion stays unmodified, a genuinely changed file is
// listed as modified, exactly as unfiltered git reports both.
func TestUntrustedFilterBlockedStatusStillCorrect(t *testing.T) {
	f := newFilterFixture(t, "correct")
	f.init(t)
	f.backdate(t)

	st, err := f.repo().StatusOpts(false)
	if err != nil {
		t.Fatalf("status read: %v", err)
	}
	if unstagedHas(st, "f.txt") {
		t.Fatalf("unchanged content reported modified: %+v", st.Unstaged)
	}

	f.rewrite(t)
	st, err = f.repo().StatusOpts(false)
	if err != nil {
		t.Fatalf("status read: %v", err)
	}
	if !unstagedHas(st, "f.txt") {
		t.Fatalf("changed file missing from the status: %+v", st.Unstaged)
	}

	// Unfiltered git agrees on both answers.
	f.resetMarks(t)
	out := f.git(t, "status", "--porcelain", "--", "f.txt")
	if !strings.HasPrefix(out, " M f.txt") {
		t.Fatalf("plain git status = %q, want the file modified", out)
	}
}

// A long-running process filter, which git prefers over the clean filter,
// never runs either.
func TestUntrustedFilterProcessNeverRunsOnStatusRead(t *testing.T) {
	f := newFilterFixture(t, "process")
	f.init(t)
	f.git(t, "config", "filter.x.process", "sh "+f.procSh)
	f.backdate(t)

	if _, err := f.repo().StatusOpts(false); err != nil {
		t.Fatalf("status read: %v", err)
	}
	if f.marked(t, f.procLog) {
		t.Fatal("the repository's own process filter ran on a passive status read")
	}

	f.controlStatus(t)
	if !f.marked(t, f.procLog) {
		t.Fatal("control: plain git status did not run the process filter, the fixture is broken")
	}
}

// A filter the repository declares required is neutralized too: clearing
// its command without clearing required would fail the whole status read.
func TestUntrustedFilterRequiredNeverFailsStatusRead(t *testing.T) {
	f := newFilterFixture(t, "required")
	f.init(t)
	f.git(t, "config", "filter.x.required", "true")
	f.backdate(t)

	st, err := f.repo().StatusOpts(false)
	if err != nil {
		t.Fatalf("status read of a required-filter repository: %v", err)
	}
	if f.marked(t, f.cleanLog) {
		t.Fatal("the repository's own required filter ran on a passive status read")
	}
	if unstagedHas(st, "f.txt") {
		t.Fatalf("unchanged content reported modified: %+v", st.Unstaged)
	}

	f.controlStatus(t)
	if !f.marked(t, f.cleanLog) {
		t.Fatal("control: plain git status did not run the filter, the fixture is broken")
	}
}

// Filters hidden behind an include directive of .git/config are found and
// blocked, not only the ones written in the file itself.
func TestUntrustedFilterIncludedConfigNeverRuns(t *testing.T) {
	f := newFilterFixture(t, "include")
	inc := filepath.Join(f.dir, "filters.inc")
	content := "[filter \"x\"]\n\tclean = sh " + f.cleanSh + "\n"
	f.git(t, "init", "-q", "-b", "main")
	f.git(t, "config", "user.name", "Branchline Test")
	f.git(t, "config", "user.email", "test@branchline.invalid")
	if err := os.WriteFile(inc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(t, "config", "include.path", inc)
	if err := os.WriteFile(filepath.Join(f.repoDir, ".gitattributes"), []byte("*.txt filter=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repoDir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(t, "add", ".gitattributes", "f.txt")
	f.git(t, "commit", "-q", "-m", "initial")
	f.resetMarks(t)
	f.backdate(t)

	if _, err := f.repo().StatusOpts(false); err != nil {
		t.Fatalf("status read: %v", err)
	}
	if f.marked(t, f.cleanLog) {
		t.Fatal("a filter reached through include.path ran on a passive status read")
	}

	f.controlStatus(t)
	if !f.marked(t, f.cleanLog) {
		t.Fatal("control: plain git status did not run the included filter, the fixture is broken")
	}
}

// A filter whose clean command is identical in the user's global
// configuration is the user's own filter — git-lfs works this way — and a
// passive read keeps running it. The global configuration lives in a
// temporary HOME; the real one is never touched.
func TestUntrustedFilterGlobalSameValueStillRuns(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	f := newFilterFixture(t, "global-same")
	content := "[filter \"x\"]\n\tclean = sh " + f.cleanSh + "\n"
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.init(t)
	// The repository repeats the user's own definition verbatim, as a clone
	// of the user's own project would. The value is the inherited one.
	f.backdate(t)

	if _, err := f.repo().StatusOpts(false); err != nil {
		t.Fatalf("status read: %v", err)
	}
	if !f.marked(t, f.cleanLog) {
		t.Fatal("a filter identical to the user's global configuration was blocked; it must keep running")
	}
}

// The repository rewriting the user's global filter to a different command
// is the repository's own definition again: the rewritten command never
// runs on a passive read.
func TestUntrustedFilterGlobalRewrittenNeverRuns(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	f := newFilterFixture(t, "global-rewrite")
	honest := filepath.Join(f.dir, "honest-clean.sh")
	writeScript(t, honest, filepath.Join(f.dir, "honest.log"))
	content := "[filter \"x\"]\n\tclean = sh " + honest + "\n"
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.init(t)
	// init configured filter.x.clean to the fixture's own script, which
	// differs from the global honest one: a rewrite.
	f.backdate(t)

	if _, err := f.repo().StatusOpts(false); err != nil {
		t.Fatalf("status read: %v", err)
	}
	if f.marked(t, f.cleanLog) {
		t.Fatal("a filter the repository rewrote ran on a passive status read")
	}
	if f.marked(t, filepath.Join(f.dir, "honest.log")) {
		t.Fatal("the user's global filter ran although the repository rewrote it")
	}
}

// Staging a file is the user's own action and runs the repository's filters
// exactly as git on the command line does.
func TestUntrustedFilterUserStageStillRunsFilter(t *testing.T) {
	f := newFilterFixture(t, "stage")
	f.init(t)
	f.rewrite(t)

	if err := f.repo().Stage("f.txt"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !f.marked(t, f.cleanLog) {
		t.Fatal("the user's own stage did not run the filter; the app must behave like git here")
	}
}

// A status read over SSH is guarded the same way: the server's repository
// defines the filter, the comparison is against the server account's own
// configuration, and the program never runs for the passive read.
func TestUntrustedFilterNeverRunsOverSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	repoDir := srv.InitRepo(t, "untrusted-filter")
	scriptDir := srv.TempDir(t, "filter-scripts")

	srv.RunShell(t, "cat > "+git.ShellQuote(scriptDir+"/filter-clean.sh")+" <<'EOF'\n#!/bin/sh\necho ran >> "+scriptDir+"/clean.log\nexec cat\nEOF\nchmod +x "+git.ShellQuote(scriptDir+"/filter-clean.sh"))
	srv.RunGit(t, repoDir, "config", "filter.x.clean", "sh "+scriptDir+"/filter-clean.sh")
	srv.WriteFile(t, repoDir, ".gitattributes", []byte("*.txt filter=x\n"))
	srv.WriteFile(t, repoDir, "f.txt", []byte("hello\n"))
	srv.RunGit(t, repoDir, "add", ".gitattributes", "f.txt")
	srv.RunGit(t, repoDir, "commit", "-q", "-m", "filter setup")
	srv.RunShell(t, "rm -f "+git.ShellQuote(scriptDir+"/clean.log")+" && touch -t 202001010000 "+git.ShellQuote(repoDir+"/f.txt"))

	repo := srv.Repo(t, repoDir)
	if _, err := repo.StatusOpts(false); err != nil {
		t.Fatalf("SSH status read: %v", err)
	}
	if out := srv.RunShell(t, "test -f "+git.ShellQuote(scriptDir+"/clean.log")+" && echo yes || echo no"); strings.TrimSpace(out) != "no" {
		t.Fatal("the server's own clean filter ran on a passive status read over SSH")
	}

	// The control on the server, plain git without the guard, runs it.
	srv.RunGit(t, repoDir, "status", "--porcelain=v2", "-z")
	if out := srv.RunShell(t, "test -f "+git.ShellQuote(scriptDir+"/clean.log")+" && echo yes || echo no"); strings.TrimSpace(out) != "yes" {
		t.Fatal("control: plain git status on the server did not run the filter, the fixture is broken")
	}
}
