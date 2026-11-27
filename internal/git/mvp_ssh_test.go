package git_test

import (
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// TestMVPOperationsLoopbackSSH exercises the MVP Git operations (status, log,
// stage/unstage/commit, branch create/checkout/delete, fetch/pull/push) over a
// disposable loopback sshd via internal/sshtest.
func TestMVPOperationsLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	runMVPOperationsOverSSH(t, srv)
}

// TestConfiguredLinuxMVPOperations exercises the same MVP Git operations over
// the configured Linux SSH environment (sshlab E1/E2/E3) via internal/sshtest.
func TestConfiguredLinuxMVPOperations(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	runMVPOperationsOverSSH(t, srv)
}

func runMVPOperationsOverSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	t.Run("LogStatusStageCommit", func(t *testing.T) {
		workDir := srv.InitRepo(t, "mvp-work")
		repo := srv.Repo(t, workDir)

		top, err := repo.TopLevel()
		if err != nil {
			t.Fatalf("TopLevel: %v", err)
		}
		wantTop := srv.RunGit(t, workDir, "rev-parse", "--show-toplevel")
		if top != wantTop {
			t.Fatalf("TopLevel = %q, want %q", top, wantTop)
		}

		st, err := repo.Status()
		if err != nil {
			t.Fatalf("initial Status: %v", err)
		}
		initialHead := srv.RunGit(t, workDir, "rev-parse", "HEAD")
		if st.Branch != "main" || st.Head != initialHead || len(st.Staged) != 0 || len(st.Unstaged) != 0 {
			t.Fatalf("unexpected initial Status: %+v (want branch=main head=%s clean)", st, initialHead)
		}

		commits, err := repo.Log(git.LogOptions{Limit: 10})
		if err != nil {
			t.Fatalf("initial Log: %v", err)
		}
		if len(commits) != 1 || commits[0].Hash != initialHead || commits[0].Subject != "initial commit" {
			t.Fatalf("unexpected initial Log: %+v", commits)
		}

		srv.WriteFile(t, workDir, "a.txt", []byte("one\ntwo\n"))
		srv.WriteFile(t, workDir, "notes.txt", []byte("mvp notes\n"))

		st, err = repo.Status()
		if err != nil {
			t.Fatalf("Status after edits: %v", err)
		}
		if len(st.Staged) != 0 || len(st.Unstaged) != 2 {
			t.Fatalf("expected 0 staged and 2 unstaged after edits, got staged=%+v unstaged=%+v", st.Staged, st.Unstaged)
		}

		if err := repo.Stage("a.txt", "notes.txt"); err != nil {
			t.Fatalf("Stage: %v", err)
		}
		st, err = repo.Status()
		if err != nil {
			t.Fatalf("Status after Stage: %v", err)
		}
		if len(st.Staged) != 2 || len(st.Unstaged) != 0 {
			t.Fatalf("expected 2 staged and 0 unstaged, got staged=%+v unstaged=%+v", st.Staged, st.Unstaged)
		}

		if err := repo.Unstage("a.txt"); err != nil {
			t.Fatalf("Unstage: %v", err)
		}
		st, err = repo.Status()
		if err != nil {
			t.Fatalf("Status after Unstage: %v", err)
		}
		if len(st.Staged) != 1 || st.Staged[0].Path != "notes.txt" || len(st.Unstaged) != 1 || st.Unstaged[0].Path != "a.txt" {
			t.Fatalf("unexpected status after Unstage(a.txt): staged=%+v unstaged=%+v", st.Staged, st.Unstaged)
		}

		if err := repo.Stage("a.txt"); err != nil {
			t.Fatalf("re-Stage a.txt: %v", err)
		}
		if err := repo.Commit("mvp stage and commit", false); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		st, err = repo.Status()
		if err != nil {
			t.Fatalf("Status after Commit: %v", err)
		}
		newHead := srv.RunGit(t, workDir, "rev-parse", "HEAD")
		if newHead == initialHead || st.Head != newHead || len(st.Staged) != 0 || len(st.Unstaged) != 0 {
			t.Fatalf("unexpected Status after Commit: %+v (head=%s initial=%s)", st, newHead, initialHead)
		}

		commits, err = repo.Log(git.LogOptions{Limit: 10})
		if err != nil {
			t.Fatalf("Log after Commit: %v", err)
		}
		if len(commits) != 2 || commits[0].Hash != newHead || commits[0].Subject != "mvp stage and commit" || commits[1].Hash != initialHead {
			t.Fatalf("unexpected Log after Commit: %+v", commits)
		}
		if got := srv.RunGit(t, workDir, "log", "-1", "--format=%s"); got != "mvp stage and commit" {
			t.Fatalf("oracle commit subject = %q, want %q", got, "mvp stage and commit")
		}
	})

	t.Run("BranchCreateCheckoutDelete", func(t *testing.T) {
		workDir := srv.InitRepo(t, "mvp-branch")
		repo := srv.Repo(t, workDir)

		if err := repo.CreateBranch("feature/mvp", "HEAD", true); err != nil {
			t.Fatalf("CreateBranch(feature/mvp): %v", err)
		}
		st, err := repo.Status()
		if err != nil {
			t.Fatalf("Status on feature/mvp: %v", err)
		}
		if st.Branch != "feature/mvp" {
			t.Fatalf("branch = %q, want feature/mvp", st.Branch)
		}

		srv.WriteFile(t, workDir, "feature.txt", []byte("unmerged feature\n"))
		if err := repo.Stage("feature.txt"); err != nil {
			t.Fatalf("Stage feature.txt: %v", err)
		}
		if err := repo.Commit("feature commit", false); err != nil {
			t.Fatalf("Commit feature: %v", err)
		}
		featureHead := srv.RunGit(t, workDir, "rev-parse", "refs/heads/feature/mvp")

		if err := repo.Checkout("main"); err != nil {
			t.Fatalf("Checkout main: %v", err)
		}
		if cur, err := repo.CurrentBranch(); err != nil || cur != "main" {
			t.Fatalf("CurrentBranch = (%q, %v), want (main, nil)", cur, err)
		}

		// Safe delete (-d) must refuse an unmerged branch without deleting its ref.
		if err := repo.DeleteBranch("feature/mvp", false); err == nil {
			t.Fatal("DeleteBranch(feature/mvp, force=false) succeeded on unmerged branch, want error")
		}
		if got := srv.RunGit(t, workDir, "rev-parse", "refs/heads/feature/mvp"); got != featureHead {
			t.Fatalf("unmerged branch ref changed after failed DeleteBranch: got %q, want %q", got, featureHead)
		}

		// Force delete (-D) removes the unmerged branch.
		if err := repo.DeleteBranch("feature/mvp", true); err != nil {
			t.Fatalf("DeleteBranch(feature/mvp, force=true): %v", err)
		}
		if branches := srv.RunGit(t, workDir, "for-each-ref", "--format=%(refname:short)", "refs/heads"); strings.Contains(branches, "feature/mvp") {
			t.Fatalf("feature/mvp still present after force delete: %q", branches)
		}

		// A merged branch can be deleted safely without force.
		if err := repo.CreateBranch("merged-temp", "HEAD", false); err != nil {
			t.Fatalf("CreateBranch(merged-temp): %v", err)
		}
		refs, err := repo.Refs()
		if err != nil {
			t.Fatalf("Refs: %v", err)
		}
		foundTemp := false
		for _, b := range refs.Branches {
			if b.Name == "merged-temp" {
				foundTemp = true
			}
		}
		if !foundTemp {
			t.Fatalf("Refs().Branches missing merged-temp: %+v", refs.Branches)
		}
		if err := repo.DeleteBranch("merged-temp", false); err != nil {
			t.Fatalf("DeleteBranch(merged-temp, force=false): %v", err)
		}
		if branches := srv.RunGit(t, workDir, "for-each-ref", "--format=%(refname:short)", "refs/heads"); strings.Contains(branches, "merged-temp") {
			t.Fatalf("merged-temp still present after safe delete: %q", branches)
		}
	})

	t.Run("FetchPullPush", func(t *testing.T) {
		workDir := srv.InitRepo(t, "mvp-sync-work")
		bareDir := srv.InitBareRepo(t, "mvp-sync-origin")
		repo := srv.Repo(t, workDir)

		if err := repo.RemoteAdd("origin", bareDir, false); err != nil {
			t.Fatalf("RemoteAdd: %v", err)
		}
		if err := repo.Push("origin", "main", true, false, false); err != nil {
			t.Fatalf("initial Push: %v", err)
		}
		initialHead := srv.RunGit(t, workDir, "rev-parse", "HEAD")
		if bareHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); bareHead != initialHead {
			t.Fatalf("bare main after initial Push = %q, want %q", bareHead, initialHead)
		}

		// Advance the bare remote through a separate peer clone on the server.
		peerParent := srv.TempDir(t, "mvp-sync-peer")
		peerDir := peerParent + "/clone"
		peerScript := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
git clone -q ` + git.ShellQuote(bareDir) + ` ` + git.ShellQuote(peerDir) + `
cd ` + git.ShellQuote(peerDir) + `
git config user.name 'Peer User'
git config user.email 'peer@branchline.invalid'
git config commit.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
printf 'from peer\n' > from-peer.txt
git add from-peer.txt
git commit -q -m 'peer advance'
git push -q origin refs/heads/main:refs/heads/main
git rev-parse HEAD
`
		peerHead := strings.TrimSpace(srv.RunShell(t, peerScript))
		if peerHead == "" || peerHead == initialHead {
			t.Fatalf("unexpected peerHead %q (initial=%q)", peerHead, initialHead)
		}

		// Fetch updates origin/main without advancing HEAD.
		if err := repo.Fetch(); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if got := srv.RunGit(t, workDir, "rev-parse", "refs/remotes/origin/main"); got != peerHead {
			t.Fatalf("origin/main after Fetch = %q, want %q", got, peerHead)
		}
		if got := srv.RunGit(t, workDir, "rev-parse", "HEAD"); got != initialHead {
			t.Fatalf("HEAD moved during Fetch: got %q, want %q", got, initialHead)
		}

		// Pull fast-forwards HEAD and updates the working tree.
		if err := repo.Pull(false); err != nil {
			t.Fatalf("Pull: %v", err)
		}
		if got := srv.RunGit(t, workDir, "rev-parse", "HEAD"); got != peerHead {
			t.Fatalf("HEAD after Pull = %q, want %q", got, peerHead)
		}
		if got := string(srv.ReadFile(t, workDir, "from-peer.txt")); got != "from peer\n" {
			t.Fatalf("from-peer.txt after Pull = %q, want %q", got, "from peer\n")
		}
		commits, err := repo.Log(git.LogOptions{Limit: 1})
		if err != nil || len(commits) != 1 || commits[0].Subject != "peer advance" {
			t.Fatalf("Log after Pull = (%+v, %v), want subject 'peer advance'", commits, err)
		}

		// Commit a follow-up change via repo and push it back to the bare remote.
		srv.WriteFile(t, workDir, "after-pull.txt", []byte("pushed back\n"))
		if err := repo.Stage("after-pull.txt"); err != nil {
			t.Fatalf("Stage after-pull.txt: %v", err)
		}
		if err := repo.Commit("local after pull", false); err != nil {
			t.Fatalf("Commit after pull: %v", err)
		}
		pushedHead := srv.RunGit(t, workDir, "rev-parse", "HEAD")
		if err := repo.Push("origin", "main", false, false, false); err != nil {
			t.Fatalf("Push after pull: %v", err)
		}
		if bareHead := srv.RunGit(t, bareDir, "rev-parse", "refs/heads/main"); bareHead != pushedHead {
			t.Fatalf("bare main after Push = %q, want %q", bareHead, pushedHead)
		}
		if got := srv.RunGit(t, bareDir, "show", "refs/heads/main:after-pull.txt"); got != "pushed back" {
			t.Fatalf("bare after-pull.txt = %q, want %q", got, "pushed back")
		}
	})
}
