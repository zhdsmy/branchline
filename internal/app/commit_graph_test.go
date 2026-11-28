package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// lowerCommitGraphThreshold drops the commit-count threshold for one test so
// fixtures of a few hundred commits exercise the background write, and
// restores it when the test ends.
func lowerCommitGraphThreshold(t *testing.T, n int) {
	t.Helper()
	old := commitGraphMinCommits
	commitGraphMinCommits = n
	t.Cleanup(func() { commitGraphMinCommits = old })
}

// commitGraphFastImport adds n linear commits on main to the repository in
// dir through one fast-import stream, chaining the first of them on the
// existing tip of main when there is one.
func commitGraphFastImport(t *testing.T, dir string, n int) {
	t.Helper()
	tip, err := tryGitCommand(dir, "rev-parse", "--verify", "--quiet", "refs/heads/main")
	hasTip := err == nil && strings.TrimSpace(tip) != ""

	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\n", i)
		fmt.Fprintf(&b, "author Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		fmt.Fprintf(&b, "committer Branchline Test <test@branchline.local> %d +0000\n", 1700000000+i)
		msg := fmt.Sprintf("imported commit %d", i)
		fmt.Fprintf(&b, "data %d\n%s\n", len(msg), msg)
		if i == 1 {
			if hasTip {
				b.WriteString("from refs/heads/main^0\n")
			}
		} else {
			fmt.Fprintf(&b, "from :%d\n", i-1)
		}
		fmt.Fprintf(&b, "M 100644 inline filler.txt\ndata %d\n%d\n\n", len(fmt.Sprintf("%d\n", i)), i)
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v\n%s", err, out)
	}
}

// commitGraphFixture builds a repository with an initial commit and n
// imported commits on top, worktree and index reset to the tip. The
// commit-graph switches are set explicitly so the outcome does not depend
// on the machine's global Git configuration.
func commitGraphFixture(t *testing.T, name string, n int) *gitRepoFixture {
	t.Helper()
	fixture := newGitRepo(t, name)
	fixture.Git("config", "core.commitGraph", "true")
	fixture.Git("config", "gc.writeCommitGraph", "true")
	fixture.CommitFile("seed.txt", "seed\n", "initial commit")
	commitGraphFastImport(t, fixture.Path(), n)
	fixture.Git("reset", "-q", "--hard", "main")
	return fixture
}

func appCommitGraphFile(dir string) string {
	return filepath.Join(dir, ".git", "objects", "info", "commit-graph")
}

func appCommitGraphExists(dir string) bool {
	_, err := os.Stat(appCommitGraphFile(dir))
	return err == nil
}

// commandRecorder is the shared record of every git invocation reaching the
// executors of one test, however many repositories the test opens.
type commandRecorder struct {
	mu   sync.Mutex
	args [][]string
}

// recordingExecutor wraps one executor and reports every invocation to the
// shared record. Workers fan out concurrently, so the record is locked.
type recordingExecutor struct {
	real   git.Executor
	shared *commandRecorder
}

func (e *recordingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.shared.mu.Lock()
	e.shared.args = append(e.shared.args, append([]string(nil), args...))
	e.shared.mu.Unlock()
	return e.real.Run(ctx, stdin, args...)
}

func (e *recordingExecutor) Describe() string { return e.real.Describe() }

// countInvocations counts the recorded git invocations match accepts.
func (r *commandRecorder) countInvocations(match func(args []string) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, args := range r.args {
		if match(args) {
			n++
		}
	}
	return n
}

func isRevListCountAll(args []string) bool {
	return len(args) == 3 && args[0] == "rev-list" && args[1] == "--count" && args[2] == "--all"
}

func isCommitGraphWrite(args []string) bool {
	return len(args) >= 2 && args[0] == "commit-graph" && args[1] == "write"
}

// isCommitGraphProbe recognizes the shell script that checks for an existing
// commit-graph: it is the only script mentioning the commit-graph paths.
func isCommitGraphProbe(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "alias.branchlinescript=") && strings.Contains(a, "objects/info/commit-graph") {
			return true
		}
	}
	return false
}

// recordingOpenRepoFn builds repositories the way the app does and wraps
// every executor in the shared recorder, so a test counts what git ran
// across opens, closes and background workers.
func recordingOpenRepoFn(h *testHarness, rec *commandRecorder) func(store.Repo) *git.Repo {
	return func(meta store.Repo) *git.Repo {
		prefs := h.app.store.Settings()
		repo := git.New(meta.ExecutorFor(prefs))
		repo.Exec = &recordingExecutor{real: repo.Exec, shared: rec}
		return repo
	}
}

// TestSessionCommitGraphWritesAfterOpen checks that opening a local
// repository whose history is longer than one page and past the commit-count
// threshold writes a commit-graph in the background: once the workers drain,
// the file is in objects/info.
func TestSessionCommitGraphWritesAfterOpen(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	fixture := commitGraphFixture(t, "cg-large", 600)

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	h.openRepo(fixture.StoreRepo())
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	if !appCommitGraphExists(fixture.Path()) {
		t.Fatalf("no commit-graph written for a 601-commit repository; texts=%q", h.ui.Texts())
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times, want 1", got)
	}
}

// TestSessionCommitGraphWritesOncePerRun checks that closing and reopening
// the same repository, and reloading it several times, still probes and
// writes only once per run of the app: exactly one commit-graph probe and
// one write reach git in total.
func TestSessionCommitGraphWritesOncePerRun(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	fixture := commitGraphFixture(t, "cg-once", 600)

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	saved := h.openRepo(fixture.StoreRepo())
	h.drain()

	if !appCommitGraphExists(fixture.Path()) {
		t.Fatal("first open did not write the commit-graph")
	}

	h.app.session.Close()
	h.app.openRepo(saved)
	h.drain()
	h.app.session.Reload()
	h.drain()
	h.app.session.Reload()
	h.drain()

	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times across close, reopen and reloads, want 1", got)
	}
	if got := rec.countInvocations(isCommitGraphProbe); got != 1 {
		t.Fatalf("commit-graph existence probed %d times across close, reopen and reloads, want 1", got)
	}
}

// TestSessionCommitGraphSkipsShortHistory checks that a history within one
// page never triggers counting or writing — even one long enough to pass the
// commit-count threshold — so neither rev-list --count nor commit-graph
// reaches git.
func TestSessionCommitGraphSkipsShortHistory(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	// 301 commits pass the lowered threshold of 200 but fit the first page
	// of 500, so the length gate must stop the write before counting starts.
	fixture := commitGraphFixture(t, "cg-short", 300)

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	h.openRepo(fixture.StoreRepo())
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	if h.app.session.Snapshot().More {
		t.Fatal("fixture history unexpectedly spans more than one page")
	}
	if got := rec.countInvocations(isRevListCountAll); got != 0 {
		t.Fatalf("rev-list --count --all ran %d times for a one-page history, want 0", got)
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 0 {
		t.Fatalf("commit-graph write ran %d times for a one-page history, want 0", got)
	}
	if appCommitGraphExists(fixture.Path()) {
		t.Fatal("commit-graph file exists for a one-page history")
	}
}

// TestSessionCommitGraphSkipsSSH checks that an SSH repository is never
// counted or written, however long its history: no rev-list --count and no
// commit-graph reach the server.
func TestSessionCommitGraphSkipsSSH(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	repo, srv := loopbackSSHRepo(t)
	// The loopback server is this machine: import a long history directly
	// into the remote repository. SSH repositories read a thousand commits
	// per page, so 1101 pass both one page and the threshold.
	commitGraphFastImport(t, repo.Path, 1100)
	// Explicitly on, whatever the machine's global configuration says: the
	// SSH check must be what stops the write, not an ambient off switch.
	srv.RunGit(t, repo.Path, "config", "core.commitGraph", "true")
	srv.RunGit(t, repo.Path, "config", "gc.writeCommitGraph", "true")
	if count := srv.RunGit(t, repo.Path, "rev-list", "--count", "--all"); count != "1101" {
		t.Fatalf("remote fixture commit count = %q, want 1101", count)
	}

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	h.openRepo(repo)
	h.drain()

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("snapshot load failed: %v", err)
	}
	if !h.app.session.Snapshot().More {
		t.Fatal("fixture history unexpectedly fits one page: the test proves nothing")
	}
	if got := rec.countInvocations(isRevListCountAll); got != 0 {
		t.Fatalf("rev-list --count --all ran %d times on an SSH repository, want 0", got)
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 0 {
		t.Fatalf("commit-graph write ran %d times on an SSH repository, want 0", got)
	}
}

// TestSessionCommitGraphWriteFailureIsSilent checks that a failing
// commit-graph write — here objects/info is not writable — surfaces nowhere:
// no action or load error, and the repository keeps working.
func TestSessionCommitGraphWriteFailureIsSilent(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	fixture := commitGraphFixture(t, "cg-readonly", 600)

	info := filepath.Join(fixture.Path(), ".git", "objects", "info")
	if err := os.Chmod(info, 0o555); err != nil {
		t.Fatalf("chmod objects/info: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(info, 0o755); err != nil {
			t.Errorf("restoring objects/info permissions: %v", err)
		}
	})

	h := newTestApp(t)
	h.openRepo(fixture.StoreRepo())
	h.drain()
	// Restore before any failure exit too, so the temporary directory can be
	// cleaned up no matter how the assertions below go.
	if err := os.Chmod(info, 0o755); err != nil {
		t.Fatalf("restoring objects/info permissions: %v", err)
	}

	if appCommitGraphExists(fixture.Path()) {
		t.Fatal("commit-graph exists although objects/info was read-only: the test proves nothing")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("action error after a failed background write: %v", err)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after a failed background write: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("snapshot not loaded after a failed background write")
	}
	if got := len(h.app.session.Snapshot().Commits); got == 0 {
		t.Fatal("repository is unusable after a failed background write: no commits in the snapshot")
	}
}

// TestSessionCommitGraphLockContentionIsSilentAndNotRetried checks that when
// another git operation already holds commit-graph.lock (for example a fetch
// writing the commit-graph), the background write gives up quietly without
// disturbing the lock or surfacing an error, and later reloads do not retry it.
func TestSessionCommitGraphLockContentionIsSilentAndNotRetried(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	fixture := commitGraphFixture(t, "cg-locked", 600)

	lockFile := filepath.Join(fixture.Path(), ".git", "objects", "info", "commit-graph.lock")
	if err := os.WriteFile(lockFile, []byte("held\n"), 0o644); err != nil {
		t.Fatalf("create commit-graph.lock: %v", err)
	}

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	h.openRepo(fixture.StoreRepo())
	h.drain()

	if appCommitGraphExists(fixture.Path()) {
		t.Fatal("commit-graph exists despite commit-graph.lock being held")
	}
	if got, err := os.ReadFile(lockFile); err != nil || string(got) != "held\n" {
		t.Fatalf("commit-graph.lock = %q err %v, want untouched lock file", got, err)
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("action error after lock contention: %v", err)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after lock contention: %v", err)
	}
	if h.app.session.Busy() {
		t.Fatal("session stayed busy after lock contention")
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times on first open, want 1", got)
	}

	// Releasing the lock and reloading must not retry the background write:
	// each repository path gets at most one attempt per run.
	if err := os.Remove(lockFile); err != nil {
		t.Fatalf("remove commit-graph.lock: %v", err)
	}
	h.app.session.Reload()
	h.drain()
	h.app.session.Reload()
	h.drain()
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times after reloads, want 1 (no retry)", got)
	}
}

// TestSessionCommitGraphAutoRefreshNoLoop checks that writing
// .git/objects/info/commit-graph cannot trigger a write-refresh-write loop:
// the production watch ignores .git, a poll right after the commit-graph write
// sees no status or ref change and does not reload, and even a subsequent
// working-tree edit that reloads the snapshot never probes or writes the
// commit-graph a second time.
func TestSessionCommitGraphAutoRefreshNoLoop(t *testing.T) {
	lowerCommitGraphThreshold(t, 200)
	fixture := commitGraphFixture(t, "cg-watch-noloop", 600)

	if !watchIgnoreGitDir(".git", true) {
		t.Fatal("production watch does not ignore .git")
	}

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	rig := installAutoRefresh(t, h)
	openAutoRefreshRepo(t, h, rig, fixture.StoreRepo())

	if !appCommitGraphExists(fixture.Path()) {
		t.Fatal("commit-graph was not written on initial open")
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times on initial open, want 1", got)
	}

	// Even if a watch hint arrives right after the commit-graph file lands,
	// the poll compares status, refs and stashes — none changed — so no
	// snapshot reload runs.
	loadGenBefore := h.app.session.loadGen
	rig.factory.watch(t, 0).hint()
	awaitHint(t, rig)
	tickAutoRefresh(t, h)
	if h.app.session.loadGen != loadGenBefore {
		t.Fatalf("loadGen advanced %d -> %d after commit-graph write with no working-tree change", loadGenBefore, h.app.session.loadGen)
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times after unchanged poll, want 1", got)
	}
	if got := rec.countInvocations(isCommitGraphProbe); got != 1 {
		t.Fatalf("commit-graph probe ran %d times after unchanged poll, want 1", got)
	}

	// A real working-tree edit does reload the snapshot, and the once-per-run
	// guard still prevents a second probe or write.
	fixture.WriteFile("watch-probe.txt", "hello\n")
	rig.factory.watch(t, 0).hint()
	awaitHint(t, rig)
	tickAutoRefresh(t, h)
	if h.app.session.loadGen == loadGenBefore {
		t.Fatal("working-tree edit did not trigger a snapshot reload")
	}
	if got := rec.countInvocations(isCommitGraphWrite); got != 1 {
		t.Fatalf("commit-graph write ran %d times after working-tree refresh, want 1", got)
	}
	if got := rec.countInvocations(isCommitGraphProbe); got != 1 {
		t.Fatalf("commit-graph probe ran %d times after working-tree refresh, want 1", got)
	}
}
