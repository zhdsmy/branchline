package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// recordedPushes returns the argument lists of the git push invocations the
// recorder saw, in the order they ran. Leading -c key=value pairs, which git
// options ahead of the subcommand come in, are skipped when picking the
// subcommand, exactly like isCommitGraphWrite does.
func recordedPushes(rec *commandRecorder) [][]string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out [][]string
	for _, args := range rec.args {
		i := 0
		for i+1 < len(args) && args[i] == "-c" {
			i += 2
		}
		if i < len(args) && args[i] == "push" {
			out = append(out, append([]string(nil), args...))
		}
	}
	return out
}

// wantOnePush asserts the recorder saw exactly one git push, with exactly
// these arguments, and returns it as one line for failure messages.
func wantOnePush(t *testing.T, rec *commandRecorder, want []string) {
	t.Helper()
	pushes := recordedPushes(rec)
	if len(pushes) != 1 {
		t.Fatalf("recorded %d git push invocation(s), want exactly one: %v", len(pushes), pushes)
	}
	if !slices.Equal(pushes[0], want) {
		t.Fatalf("quick push ran %q, want %q", strings.Join(pushes[0], " "), strings.Join(want, " "))
	}
}

// TestQuickPushTargetPushDefaultMatching checks that Option+clicking the
// toolbar Push button on a branch with a live upstream pushes exactly that
// branch to exactly its upstream, even when the repository sets
// push.default=matching: a bare git push would update every branch on the
// remote that shares its name with a local one, publishing the unpushed work
// of the sibling branch side. The remote side must not move, and the recorded
// command line must name the upstream explicitly.
func TestQuickPushTargetPushDefaultMatching(t *testing.T) {
	repo := newGitRepo(t, "quickpush-matching")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("config", "push.default", "matching")

	// side exists on the remote and carries a commit that was never pushed:
	// matching would send it along, simple or upstream would not.
	repo.CreateBranch("side")
	repo.Git("push", "origin", "side:refs/heads/side")
	sideOnServer := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/side"))
	repo.Checkout("side")
	repo.CommitFile("side.txt", "unpublished work\n", "unpublished work")
	sideLocal := strings.TrimSpace(repo.Git("rev-parse", "refs/heads/side"))
	repo.Checkout("main")
	if sideLocal == sideOnServer {
		t.Fatal("fixture broken: side never gained unpublished work")
	}
	mainHead := repo.CommitFile("m.txt", "main work\n", "main work")

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.ui.Frame()
	if pushDialogUI(h.app).open {
		t.Fatal("Push dialog opened on Option+click")
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != mainHead {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, mainHead)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/side")); got != sideOnServer {
		t.Fatalf("the quick push moved the remote side branch to %q — work never chosen for pushing; it must stay at %q", got, sideOnServer)
	}

	wantOnePush(t, rec, []string{"push", "origin", "refs/heads/main:refs/heads/main", "--porcelain"})
}

// TestQuickPushTargetUpstreamDifferentName checks that a branch tracking a
// differently named upstream — local fix tracking origin/main — pushes to
// that upstream branch and nowhere else, even with push.default=current,
// which a bare git push would follow onto a same-named remote branch instead.
func TestQuickPushTargetUpstreamDifferentName(t *testing.T) {
	repo := newGitRepo(t, "quickpush-renamed")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("config", "push.default", "current")

	repo.Git("checkout", "-b", "fix")
	fixHead := repo.CommitFile("f.txt", "fix\n", "fix work")
	repo.Git("branch", "--set-upstream-to=origin/main", "fix")
	if up := strings.TrimSpace(repo.Git("rev-parse", "--abbrev-ref", "fix@{upstream}")); up != "origin/main" {
		t.Fatalf("fixture broken: fix tracks %q, want origin/main", up)
	}

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != fixHead {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, fixHead)
	}
	if bareHasRef(t, bareDir, "refs/heads/fix") {
		t.Fatal("the quick push created refs/heads/fix on the remote — the push.default=current target instead of the tracked upstream")
	}

	wantOnePush(t, rec, []string{"push", "origin", "refs/heads/fix:refs/heads/main", "--porcelain"})
}

// TestQuickPushTargetNoUpstreamPublishesSameName checks that a branch without
// an upstream is still published to the default remote under its own name
// with upstream tracking set — the Quick Push behavior the explicit target
// must not change.
func TestQuickPushTargetNoUpstreamPublishesSameName(t *testing.T) {
	repo := newGitRepo(t, "quickpush-no-upstream")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	// main was pushed with -u by AddBareRemote; drop the tracking so the
	// branch has no upstream while the remote already holds the same commit.
	repo.Git("branch", "--unset-upstream", "main")
	head := repo.CommitFile("n.txt", "new\n", "new commit")

	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = recordingOpenRepoFn(h, rec)
	openSyncRepo(t, h, repo.StoreRepo())

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick push failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.remote")); got != "origin" {
		t.Fatalf("branch.main.remote = %q, want origin", got)
	}
	if got := strings.TrimSpace(repo.Git("config", "branch.main.merge")); got != "refs/heads/main" {
		t.Fatalf("branch.main.merge = %q, want refs/heads/main", got)
	}

	wantOnePush(t, rec, []string{"push", "--set-upstream", "origin", "refs/heads/main:refs/heads/main", "--porcelain"})
}

// TestQuickPushTargetUnreadableSnapshot checks the path where the snapshot
// never loaded — every read of git status fails — so the pre-flight
// validation cannot trust it and the worker re-reads the refs live: the push
// must still land exactly on the upstream, with push.default=matching in
// place and unpushed sibling work on the remote that must not move.
func TestQuickPushTargetUnreadableSnapshot(t *testing.T) {
	repo := newGitRepo(t, "quickpush-torn-snapshot")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("config", "push.default", "matching")

	repo.CreateBranch("side")
	repo.Git("push", "origin", "side:refs/heads/side")
	sideOnServer := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/side"))
	repo.Checkout("side")
	repo.CommitFile("side.txt", "unpublished work\n", "unpublished work")
	repo.Checkout("main")
	mainHead := repo.CommitFile("m.txt", "main work\n", "main work")

	readErr := errors.New("status unreadable for the test")
	rec := &commandRecorder{}
	h := newTestApp(t)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		prefs := h.app.store.Settings()
		repo := git.New(meta.ExecutorFor(prefs))
		repo.Exec = &recordingExecutor{real: &failReadExecutor{real: repo.Exec, err: readErr}, shared: rec}
		return repo
	}
	openSyncRepo(t, h, repo.StoreRepo())

	// The failing status read is what forces the worker path: the snapshot
	// never loads, so no pre-flight validation against it is possible.
	if err := h.app.session.LoadErr(); err == nil {
		t.Fatal("fixture broken: the snapshot loaded despite the failing status read")
	}
	if h.app.session.Loaded() {
		t.Fatal("fixture broken: session marked loaded despite the failing status read")
	}

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("quick push through the live-probe path failed: %v", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/main")); got != mainHead {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, mainHead)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/heads/side")); got != sideOnServer {
		t.Fatalf("the quick push moved the remote side branch to %q — work never chosen for pushing; it must stay at %q", got, sideOnServer)
	}

	wantOnePush(t, rec, []string{"push", "origin", "refs/heads/main:refs/heads/main", "--porcelain"})
}

// TestQuickPushTargetPushDefaultMatchingLoopbackSSH checks the same explicit
// target over a loopback SSH repository: the quick push updates only the
// upstream branch on the server, and the sibling branch with unpushed work
// stays exactly where it was despite push.default=matching.
func TestQuickPushTargetPushDefaultMatchingLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	repoDir := repo.Path
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repoDir, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repoDir, "push", "-u", "origin", "main")
	srv.RunGit(t, repoDir, "config", "push.default", "matching")

	// side exists on the remote and gains a commit that is never pushed.
	srv.RunGit(t, repoDir, "branch", "side")
	srv.RunGit(t, repoDir, "push", "origin", "side:refs/heads/side")
	sideOnServer := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/side")
	srv.RunGit(t, repoDir, "checkout", "side")
	srv.WriteFile(t, repoDir, "side.txt", []byte("unpublished work\n"))
	srv.RunGit(t, repoDir, "add", "--", "side.txt")
	srv.RunGit(t, repoDir, "commit", "-m", "unpublished work")
	srv.RunGit(t, repoDir, "checkout", "main")
	srv.WriteFile(t, repoDir, "m.txt", []byte("main work\n"))
	srv.RunGit(t, repoDir, "add", "--", "m.txt")
	srv.RunGit(t, repoDir, "commit", "-m", "main work")
	mainHead := srv.RunGit(t, repoDir, "rev-parse", "HEAD")

	h := newTestApp(t, repo)
	closeSync(t, h)
	selectRepo(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if !h.app.session.Loaded() {
		t.Fatal("SSH snapshot did not load")
	}

	if err := h.ui.ClickWith(ui.Alt, "Push"); err != nil {
		t.Fatalf("Option+click Push: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH quick push failed: %v", err)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); got != mainHead {
		t.Fatalf("server bare refs/heads/main = %q, want %q", got, mainHead)
	}
	if got := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/side"); got != sideOnServer {
		t.Fatalf("the SSH quick push moved the remote side branch to %q — work never chosen for pushing; it must stay at %q", got, sideOnServer)
	}
}
