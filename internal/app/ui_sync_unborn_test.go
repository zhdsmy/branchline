package app

// End-to-end UI tests: clicking Push on an unborn branch (an empty
// repository or a not yet committed --orphan branch) must say the branch has
// no commits yet, never the detached-HEAD message, and must never run git
// push. Covers the local executor and the loopback sshd, the UI pre-flight
// path and the worker live-probe path, the encrypted-key snapshot-not-loaded
// path, the born-branch hot path (zero extra HeadState probe), and corrupt
// repository shapes that must surface a real error instead of either
// validation message.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

const (
	wantUnbornMsg   = "Current branch has no commits yet: make a commit before pushing."
	wantDetachedMsg = "HEAD is detached: check out a branch to push it."
	wantNoRemoteMsg = "The repository has no remote to push to."
)

// newLoggedApp creates a test app with sync cleanup and a CommandLog bound to
// every repository opened through the product path (including sync workers,
// which retain the CommandLog through WithExecutor).
func newLoggedApp(t *testing.T, initial ...store.Repo) (*testHarness, *git.CommandLog) {
	t.Helper()
	cmdLog := git.NewCommandLog()
	h := newTestApp(t, initial...)
	closeSync(t, h)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		return defaultOpenGitRepo(meta).WithCommandLog(cmdLog)
	}
	return h, cmdLog
}

// requireNoGitPush asserts no git push ran through the repository the app opened.
func requireNoGitPush(t *testing.T, l *git.CommandLog) {
	t.Helper()
	records, _ := l.Snapshot()
	for _, r := range records {
		if len(r.Args) > 0 && r.Args[0] == "push" {
			t.Fatalf("git push ran unexpectedly; records=%+v", records)
		}
	}
}

// requirePushValidationError asserts the toolbar shows wantMsg (and never
// forbidMsg), the session is not busy, and no git push ran.
func requirePushValidationError(t *testing.T, h *testHarness, l *git.CommandLog, wantMsg, forbidMsg string) {
	t.Helper()
	h.ui.Frame()
	if !h.ui.HasText(wantMsg) {
		t.Fatalf("missing expected push error %q; texts=%q", wantMsg, h.ui.Texts())
	}
	if forbidMsg != "" && h.ui.HasText(forbidMsg) {
		t.Fatalf("forbidden message %q wrongly shown; texts=%q", forbidMsg, h.ui.Texts())
	}
	if h.app.session.Busy() {
		t.Fatal("session busy for a push that should not have started")
	}
	requireNoGitPush(t, l)
}

// dismissToolbarError clicks Dismiss Error and asserts the strip is gone.
func dismissToolbarError(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Dismiss Error"); err != nil {
		t.Fatalf("click Dismiss Error: %v", err)
	}
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); ok {
		t.Fatal("Repository Error still visible after dismiss")
	}
}

// runEmptyRepoNoRemoteFlow verifies the unborn-branch Push validation and
// recovery flow on an already-opened empty repository with an untracked
// hello.txt and no remote.
func runEmptyRepoNoRemoteFlow(t *testing.T, h *testHarness, cmdLog *git.CommandLog) {
	t.Helper()
	emptySnapshotLoaded(t, h)

	clickPush(t, h)
	requirePushValidationError(t, h, cmdLog, wantUnbornMsg, wantDetachedMsg)
	dismissToolbarError(t, h)

	// The first commit through real Local Changes controls makes the repository
	// born; Push then reports the missing remote instead of either HEAD message.
	mustClick(t, h, "Stage hello.txt")
	h.drain()
	typeCommitMessage(t, h, "first commit")
	mustClick(t, h, "Commit")
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("commit through UI failed: %v", err)
	}

	clickPush(t, h)
	requirePushValidationError(t, h, cmdLog, wantNoRemoteMsg, wantUnbornMsg)
	if h.ui.HasText(wantDetachedMsg) {
		t.Fatalf("detached message wrongly shown after first commit; texts=%q", h.ui.Texts())
	}
	dismissToolbarError(t, h)
}

// assertCorruptPushFlow verifies Push on a repository whose initial load failed
// due to a corrupt ref surfaces the real git error, neither validation message,
// no git push, and dismisses cleanly.
func assertCorruptPushFlow(t *testing.T, h *testHarness, cmdLog *git.CommandLog) {
	t.Helper()
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("corrupt repository loaded without error, want load failure")
	}

	clickPush(t, h)
	h.drain()
	h.ui.Frame()

	if h.ui.HasText(wantUnbornMsg) {
		t.Fatalf("corrupt repository misread as unborn branch; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText(wantDetachedMsg) {
		t.Fatalf("corrupt repository misread as detached HEAD; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.AccessByRole(ui.RoleStatus, "Repository Error"); !ok {
		t.Fatalf("no Repository Error shown for corrupt repository; texts=%q", h.ui.Texts())
	}
	requireNoGitPush(t, cmdLog)
	dismissToolbarError(t, h)
}

type barePushCase struct {
	name      string
	wantMsg   string
	forbidMsg string
	drain     bool // true when Push passes pre-flight and relies on worker HeadState probe
}

// runBarePushCase executes the shared Push assertion sequence against an opened
// repository with a bare origin, proving the expected message appears, no git
// push runs, the bare remote is unchanged byte-for-byte, and Dismiss clears it.
func runBarePushCase(t *testing.T, h *testHarness, cmdLog *git.CommandLog, tc barePushCase, bareRefs func() string, mutateBeforePush func()) {
	t.Helper()
	emptySnapshotLoaded(t, h)
	before := bareRefs()
	if mutateBeforePush != nil {
		mutateBeforePush()
	}

	clickPush(t, h)
	if tc.drain {
		h.drain()
	}
	requirePushValidationError(t, h, cmdLog, tc.wantMsg, tc.forbidMsg)
	if got := bareRefs(); got != before {
		t.Fatalf("bare remote refs changed by a push that must not run:\nbefore=%q\nafter=%q", before, got)
	}
	dismissToolbarError(t, h)
}

// TestSyncPushUnbornEmptyRepoNoRemote covers the unborn-branch Push
// validation locally: empty repository with no remote reports the unborn
// message (before no-remote), runs no git push, dismisses cleanly, and after
// the first commit reports the accurate no-remote message.
func TestSyncPushUnbornEmptyRepoNoRemote(t *testing.T) {
	repo := newGitRepo(t, "push-unborn-noremote")
	repo.WriteFile("hello.txt", "hello\n")

	h, cmdLog := newLoggedApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())
	runEmptyRepoNoRemoteFlow(t, h, cmdLog)
}

// TestSyncPushUnbornBareRemote covers every bare-remote unborn and detached
// regression case locally through a table: empty repo pre-flight, orphan branch
// pre-flight, stale-snapshot orphan worker probe, and stale-snapshot detached
// worker probe.
func TestSyncPushUnbornBareRemote(t *testing.T) {
	cases := []struct {
		barePushCase
		setup func(t *testing.T) (repoDir string, bareDir string, mutate func())
	}{
		{
			barePushCase: barePushCase{
				name:      "EmptyRepo",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
			},
			setup: func(t *testing.T) (string, string, func()) {
				repo := newGitRepo(t, "push-unborn-remote")
				repo.WriteFile("hello.txt", "hello\n")
				return repo.Path(), repo.AddBareRemote("origin"), nil
			},
		},
		{
			barePushCase: barePushCase{
				name:      "OrphanSnapshotLoaded",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
			},
			setup: func(t *testing.T) (string, string, func()) {
				repo := newGitRepo(t, "push-unborn-orphan-snap")
				repo.CommitFile("a.txt", "one\n", "first on main")
				bareDir := repo.AddBareRemote("origin")
				repo.Git("checkout", "-q", "--orphan", "orphanui")
				repo.Git("rm", "-q", "-r", "--cached", ".")
				return repo.Path(), bareDir, nil
			},
		},
		{
			barePushCase: barePushCase{
				name:      "OrphanWorkerProbe",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
				drain:     true,
			},
			setup: func(t *testing.T) (string, string, func()) {
				repo := newGitRepo(t, "push-unborn-orphan-worker")
				repo.CommitFile("a.txt", "one\n", "first on main")
				bareDir := repo.AddBareRemote("origin")
				return repo.Path(), bareDir, func() {
					repo.Git("checkout", "-q", "--orphan", "orphanworker")
					repo.Git("rm", "-q", "-r", "--cached", ".")
				}
			},
		},
		{
			barePushCase: barePushCase{
				name:      "DetachedWorkerProbe",
				wantMsg:   wantDetachedMsg,
				forbidMsg: wantUnbornMsg,
				drain:     true,
			},
			setup: func(t *testing.T) (string, string, func()) {
				repo := newGitRepo(t, "push-detached-worker")
				head := repo.CommitFile("a.txt", "one\n", "first on main")
				bareDir := repo.AddBareRemote("origin")
				return repo.Path(), bareDir, func() {
					repo.Git("checkout", "-q", head)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, bareDir, mutate := tc.setup(t)
			h, cmdLog := newLoggedApp(t)
			addLocalRepoThroughSwitcher(t, h, repoDir)
			runBarePushCase(t, h, cmdLog, tc.barePushCase, func() string {
				return runGitCommand(t, bareDir, "for-each-ref")
			}, mutate)
		})
	}
}

// TestSyncPushBornBranchSkipsHeadStateProbe verifies the born-branch Push hot
// path adds no HeadState git commands (symbolic-ref -q HEAD, show-ref, or
// rev-parse --verify HEAD^{commit}).
func TestSyncPushBornBranchSkipsHeadStateProbe(t *testing.T) {
	repo := newGitRepo(t, "push-born-hotpath")
	repo.CommitFile("a.txt", "one\n", "first on main")
	bareDir := repo.AddBareRemote("origin")
	head := repo.CommitFile("b.txt", "two\n", "second on main")

	h, cmdLog := newLoggedApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())
	emptySnapshotLoaded(t, h)

	beforeRecords, _ := cmdLog.Snapshot()
	startIdx := len(beforeRecords)

	clickPush(t, h)
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("born-branch Push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}

	afterRecords, _ := cmdLog.Snapshot()
	sawPush := false
	for _, r := range afterRecords[startIdx:] {
		if len(r.Args) == 0 {
			continue
		}
		if r.Args[0] == "push" {
			sawPush = true
			continue
		}
		if slices.Equal(r.Args, []string{"symbolic-ref", "-q", "HEAD"}) ||
			r.Args[0] == "show-ref" ||
			slices.Contains(r.Args, "HEAD^{commit}") {
			t.Fatalf("born-branch Push ran unexpected HeadState probe %v; all=%+v", r.Args, afterRecords[startIdx:])
		}
	}
	if !sawPush {
		t.Fatalf("expected git push on born-branch hot path; records=%+v", afterRecords[startIdx:])
	}
}

// TestSyncPushCorruptBranchRefNotMisread covers a repository whose
// refs/heads/main holds invalid text ("not-a-hash"): Push surfaces the real git
// error rather than either validation message, runs no git push, and dismisses.
func TestSyncPushCorruptBranchRefNotMisread(t *testing.T) {
	repo := newGitRepo(t, "push-corrupt-ref")
	repo.CommitFile("a.txt", "one\n", "first")
	if err := os.WriteFile(filepath.Join(repo.Path(), ".git", "refs", "heads", "main"), []byte("not-a-hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h, cmdLog := newLoggedApp(t)
	addLocalRepoThroughSwitcher(t, h, repo.Path())
	assertCorruptPushFlow(t, h, cmdLog)
}

// ---------------------------------------------------------------------------
// Loopback SSH variants (no t.Parallel: startLoopbackForApp calls t.Setenv)
// ---------------------------------------------------------------------------

// TestSyncPushUnbornEmptyRepoLoopbackSSH runs the empty-repository no-remote
// defect and post-first-commit recovery flow over the loopback sshd.
func TestSyncPushUnbornEmptyRepoLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := initSSHEmptyRepo(t, srv, "push-unborn-ssh")
	repo := sshStoreRepo(t, srv, dir)

	h, cmdLog := newLoggedApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)
	runEmptyRepoNoRemoteFlow(t, h, cmdLog)
}

// TestSyncPushUnbornBareRemoteLoopbackSSH runs the bare-remote unborn and
// detached matrix over one loopback sshd: empty repo pre-flight, orphan branch
// pre-flight, stale-snapshot orphan worker probe, and stale-snapshot detached
// worker probe.
func TestSyncPushUnbornBareRemoteLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)

	cases := []struct {
		barePushCase
		setup func(t *testing.T) (repo store.Repo, bareDir string, mutate func())
	}{
		{
			barePushCase: barePushCase{
				name:      "EmptyRepo",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
			},
			setup: func(t *testing.T) (store.Repo, string, func()) {
				dir := initSSHEmptyRepo(t, srv, "push-unborn-remote-ssh")
				bareDir := srv.InitBareRepo(t, "origin-empty")
				srv.RunGit(t, dir, "remote", "add", "origin", bareDir)
				return sshStoreRepo(t, srv, dir), bareDir, nil
			},
		},
		{
			barePushCase: barePushCase{
				name:      "OrphanSnapshotLoaded",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
			},
			setup: func(t *testing.T) (store.Repo, string, func()) {
				dir := srv.InitRepo(t, "push-unborn-orphan-snap-ssh")
				bareDir := srv.InitBareRepo(t, "origin-orphan-snap")
				srv.RunGit(t, dir, "remote", "add", "origin", bareDir)
				srv.RunGit(t, dir, "push", "-u", "origin", "main")
				srv.RunGit(t, dir, "checkout", "-q", "--orphan", "orphansshui")
				srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")
				return sshStoreRepo(t, srv, dir), bareDir, nil
			},
		},
		{
			barePushCase: barePushCase{
				name:      "OrphanWorkerProbe",
				wantMsg:   wantUnbornMsg,
				forbidMsg: wantDetachedMsg,
				drain:     true,
			},
			setup: func(t *testing.T) (store.Repo, string, func()) {
				dir := srv.InitRepo(t, "push-unborn-orphan-worker-ssh")
				bareDir := srv.InitBareRepo(t, "origin-orphan-worker")
				srv.RunGit(t, dir, "remote", "add", "origin", bareDir)
				srv.RunGit(t, dir, "push", "-u", "origin", "main")
				return sshStoreRepo(t, srv, dir), bareDir, func() {
					srv.RunGit(t, dir, "checkout", "-q", "--orphan", "orphanworker")
					srv.RunGit(t, dir, "rm", "-q", "-r", "--cached", ".")
				}
			},
		},
		{
			barePushCase: barePushCase{
				name:      "DetachedWorkerProbe",
				wantMsg:   wantDetachedMsg,
				forbidMsg: wantUnbornMsg,
				drain:     true,
			},
			setup: func(t *testing.T) (store.Repo, string, func()) {
				dir := srv.InitRepo(t, "push-detached-worker-ssh")
				bareDir := srv.InitBareRepo(t, "origin-detached-worker")
				srv.RunGit(t, dir, "remote", "add", "origin", bareDir)
				srv.RunGit(t, dir, "push", "-u", "origin", "main")
				return sshStoreRepo(t, srv, dir), bareDir, func() {
					head := srv.RunGit(t, dir, "rev-parse", "HEAD")
					srv.RunGit(t, dir, "checkout", "-q", head)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, bareDir, mutate := tc.setup(t)
			h, cmdLog := newLoggedApp(t)
			openSSHRepoThroughSwitcher(t, h, repo)
			runBarePushCase(t, h, cmdLog, tc.barePushCase, func() string {
				return srv.RunGit(t, bareDir, "for-each-ref")
			}, mutate)
		})
	}
}

// TestSyncPushUnbornEncryptedKeyLoopbackSSH covers the snapshot-not-loaded
// path: an encrypted SSH key fails the initial promptless load, so Push defers
// validation to the worker. After unlocking through the real askpass modal,
// the worker probes refs and HeadState live and reports the unborn message via
// actionErr without running git push, the post-action Reload succeeds over the
// established ControlMaster, and Dismiss Error clears the toolbar error.
func TestSyncPushUnbornEncryptedKeyLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := initSSHEmptyRepo(t, srv, "push-unborn-enc-ssh")
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, dir, "remote", "add", "origin", bareDir)
	repo := sshStoreRepo(t, srv, dir)
	repo.Identity = srv.GenerateKey(t, "askpass-unborn-enc", "s3cret")
	before := srv.RunGit(t, bareDir, "for-each-ref")

	h, cmdLog := newLoggedApp(t, repo)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("expected initial load to fail with the encrypted key locked")
	}

	clickPush(t, h)
	h.ui.Frame()

	waitAskPass(t, h)
	h.ui.Type("s3cret")
	h.ui.Frame()
	if err := h.ui.Click("OK"); err != nil {
		t.Fatalf("click OK in passphrase dialog: %v", err)
	}
	h.ui.Frame()
	h.drain()

	requirePushValidationError(t, h, cmdLog, wantUnbornMsg, wantDetachedMsg)
	if got := srv.RunGit(t, bareDir, "for-each-ref"); got != before {
		t.Fatalf("bare remote changed by a push that must not run:\nbefore=%q\nafter=%q", before, got)
	}
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("post-push reload failed over established ControlMaster: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("snapshot did not load after askpass-unlocked push validation")
	}
	dismissToolbarError(t, h)
}

// TestSyncPushCorruptBranchRefLoopbackSSH runs the corrupt-refs/heads/main
// shape over the loopback sshd: the real git error surfaces, neither
// validation message shows, no git push crosses the wire, and Dismiss clears it.
func TestSyncPushCorruptBranchRefLoopbackSSH(t *testing.T) {
	srv := startLoopbackForApp(t)
	dir := srv.InitRepo(t, "push-corrupt-ssh")
	srv.RunShell(t, "printf 'not-a-hash\\n' > "+shellQ(dir+"/.git/refs/heads/main"))
	repo := sshStoreRepo(t, srv, dir)

	h, cmdLog := newLoggedApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)
	assertCorruptPushFlow(t, h, cmdLog)
}
