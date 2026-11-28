package app

import (
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// Load More pagination of a linear history on a configured Linux SSH
// container (sshlab): paging page by page must produce exactly the commits
// and graph of reading the same number of commits in one load.
func TestConfiguredLinuxHistoryPagingLinear(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	base := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	srv.RunShellInput(t,
		"cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet",
		[]byte(historyPagingLinearStream(1300, base)))
	srv.RunGit(t, repo.Path, "reset", "--hard", "HEAD")

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if got := len(h.app.session.Snapshot().Commits); got != defaultSSHLogLimit {
		t.Fatalf("SSH first page = %d commits, want %d", got, defaultSSHLogLimit)
	}

	h.app.session.LoadMore(0)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after paging: %v", err)
	}
	loaded := len(h.app.session.Snapshot().Commits)
	if loaded != 1301 {
		t.Fatalf("loaded commits = %d, want all 1301", loaded)
	}
	assertMatchesFullLoad(t, h, 1301, false)
}

// The same on a history with a merge every 50 commits on a configured Linux
// SSH container (sshlab), so pages join across branching lanes over SSH.
func TestConfiguredLinuxHistoryPagingMerges(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	base := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	srv.RunShellInput(t,
		"cd "+git.ShellQuote(repo.Path)+" && GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git fast-import --quiet",
		[]byte(historyPagingMergeStream(1500, 50, base)))
	srv.RunGit(t, repo.Path, "reset", "--hard", "HEAD")

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)

	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	if got := len(h.app.session.Snapshot().Commits); got != defaultSSHLogLimit {
		t.Fatalf("SSH first page = %d commits, want %d", got, defaultSSHLogLimit)
	}

	h.app.session.LoadMore(0)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("load error after paging: %v", err)
	}
	loaded := len(h.app.session.Snapshot().Commits)
	if loaded != 1500 {
		t.Fatalf("loaded commits = %d, want 1500", loaded)
	}
	assertMatchesFullLoad(t, h, 1500, false)
}
