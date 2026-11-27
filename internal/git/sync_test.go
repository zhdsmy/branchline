package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.name", "Ada")
	gitRun(t, dir, "config", "user.email", "ada@example.com")
	return dir
}

func TestMergeStyles(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "feature")
	gitRun(t, dir, "checkout", "-q", "main")
	r := New(&LocalExecutor{Dir: dir})

	if err := r.MergeWith("feature", MergeSquash); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || len(st.Staged) != 1 || st.Staged[0].Path != "b.txt" {
		t.Fatalf("squash staged %+v %v", st.Staged, err)
	}
	log, err := r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || log[0].Subject != "base" {
		t.Fatalf("squash committed %+v %v", subjects(log), err)
	}
	gitRun(t, dir, "reset", "-q", "--hard")

	if err := r.MergeWith("feature", MergeNoCommit); err != nil {
		t.Fatal(err)
	}
	// A fast-forward has no merge commit to leave uncommitted, so Git
	// moves the branch. The dialog's Don't Commit is still --no-commit.
	st, err = r.Status()
	if err != nil || st.State != "" || st.Head == "" {
		t.Fatalf("no-commit fast-forward %+v %v", st, err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || log[0].Subject != "feature" {
		t.Fatalf("fast-forward %+v %v", subjects(log), err)
	}
	gitRun(t, dir, "reset", "-q", "--hard", "HEAD~1")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "diverge")
	if err := r.MergeWith("feature", MergeNoCommit); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.State != "merge" || len(st.Staged) != 1 {
		t.Fatalf("no-commit %+v %v", st, err)
	}
	if err := r.Abort("merge"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "reset", "-q", "--hard", "HEAD~1")

	if err := r.MergeWith("feature", MergeNoFastForward); err != nil {
		t.Fatal(err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || len(log[0].Parents) != 2 {
		t.Fatalf("no-ff parents %+v %v", log, err)
	}
}

func TestStashAroundPullAndCheckout(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	origin := t.TempDir()
	gitRun(t, dir, "clone", "-q", "--bare", dir, origin)
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main")
	writeFile(t, dir, "a.txt", "one\nlocal\n")
	r := New(&LocalExecutor{Dir: dir})

	// A clean working tree is not an error, and nothing is stashed.
	gitRun(t, dir, "checkout", "--", "a.txt")
	stashed, err := r.StashSave("empty")
	if err != nil || stashed {
		t.Fatalf("empty stash %v %v", stashed, err)
	}

	writeFile(t, dir, "a.txt", "one\nlocal\n")
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", origin, other)
	gitRun(t, other, "config", "user.name", "Ada")
	gitRun(t, other, "config", "user.email", "ada@example.com")
	writeFile(t, other, "b.txt", "bee\n")
	gitRun(t, other, "add", "b.txt")
	gitRun(t, other, "commit", "-q", "-m", "remote")
	gitRun(t, other, "push", "-q", "origin", "main")

	if err := r.PullOpts(PullOptions{Remote: "origin", Branch: "main", Stash: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\nlocal\n" {
		t.Fatalf("reapplied %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 0 {
		t.Fatalf("stash left %+v %v", stashes, err)
	}

	// A pull that cannot merge keeps the stash.
	writeFile(t, dir, "c.txt", "ours\n")
	gitRun(t, dir, "add", "c.txt")
	gitRun(t, dir, "commit", "-q", "-m", "ours")
	writeFile(t, other, "c.txt", "theirs\n")
	gitRun(t, other, "add", "c.txt")
	gitRun(t, other, "commit", "-q", "-m", "theirs")
	gitRun(t, other, "push", "-q", "origin", "main")
	writeFile(t, dir, "a.txt", "one\nlocal again\n")
	if err := r.PullOpts(PullOptions{Remote: "origin", Branch: "main", Stash: true}); err == nil {
		t.Fatal("conflicting pull succeeded")
	}
	st, err := r.Status()
	if err != nil || st.State != "merge" || !st.Conflicted() {
		t.Fatalf("merge state %+v %v", st, err)
	}
	stashes, err = r.Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "Branchline pull") {
		t.Fatalf("kept stash %+v %v", stashes, err)
	}
	if err := r.Abort("merge"); err != nil {
		t.Fatal(err)
	}

	// Discarding local changes is checkout -f. Git overwrites tracked
	// files and leaves untracked files that do not collide.
	writeFile(t, dir, "a.txt", "dirty\n")
	writeFile(t, dir, "new.txt", "kept\n")
	if err := r.CheckoutForce("HEAD"); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || strings.Contains(string(body), "dirty") {
		t.Fatalf("force left %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal("checkout -f removed an untracked file")
	}

	t.Run("PullRebaseWithStashAndReapplyRestoresUnrelatedEdits", func(t *testing.T) {
		rbDir := initRepo(t)
		writeFile(t, rbDir, "base.txt", "base\n")
		gitRun(t, rbDir, "add", "base.txt")
		gitRun(t, rbDir, "commit", "-q", "-m", "C0 base")
		rbOrigin := t.TempDir()
		gitRun(t, rbDir, "clone", "-q", "--bare", rbDir, rbOrigin)
		gitRun(t, rbDir, "remote", "add", "origin", rbOrigin)
		gitRun(t, rbDir, "push", "-q", "-u", "origin", "main")

		rbPeer := t.TempDir()
		gitRun(t, rbPeer, "clone", "-q", rbOrigin, rbPeer)
		gitRun(t, rbPeer, "config", "user.name", "Ada")
		gitRun(t, rbPeer, "config", "user.email", "ada@example.com")
		writeFile(t, rbPeer, "remote.txt", "remote line\n")
		gitRun(t, rbPeer, "add", "remote.txt")
		gitRun(t, rbPeer, "commit", "-q", "-m", "R remote")
		gitRun(t, rbPeer, "push", "-q", "origin", "main")
		remoteHead := gitOut(t, rbPeer, "rev-parse", "HEAD")

		writeFile(t, rbDir, "local.txt", "local commit\n")
		gitRun(t, rbDir, "add", "local.txt")
		gitRun(t, rbDir, "commit", "-q", "-m", "L1 local")
		writeFile(t, rbDir, "base.txt", "older user stash\n")
		gitRun(t, rbDir, "stash", "push", "-q", "-m", "user stash keep")
		userStash := gitOut(t, rbDir, "rev-parse", "refs/stash")

		writeFile(t, rbDir, "staged.txt", "staged index\n")
		gitRun(t, rbDir, "add", "staged.txt")
		writeFile(t, rbDir, "staged.txt", "staged index\nunstaged tail\n")
		writeFile(t, rbDir, "base.txt", "base\nunstaged edit\n")
		writeFile(t, rbDir, "untracked.txt", "untracked keep\n")

		rbRepo := New(&LocalExecutor{Dir: rbDir})
		if err := rbRepo.PullOpts(PullOptions{Remote: "origin", Branch: "main", Rebase: true, Stash: true}); err != nil {
			t.Fatal(err)
		}
		if got := gitOut(t, rbDir, "rev-parse", "HEAD~1"); got != remoteHead {
			t.Fatalf("rebased parent = %q, want remote %q", got, remoteHead)
		}
		if got := gitOut(t, rbDir, "rev-parse", "refs/remotes/origin/main"); got != remoteHead {
			t.Fatalf("origin/main = %q, want %q", got, remoteHead)
		}
		if got := gitOut(t, rbDir, "log", "-1", "--format=%s", "HEAD"); got != "L1 local" {
			t.Fatalf("HEAD subject = %q, want L1 local", got)
		}
		if got := gitOut(t, rbDir, "diff", "--cached", "--name-only"); got != "staged.txt" {
			t.Fatalf("staged index = %q, want staged.txt", got)
		}
		if got := gitOut(t, rbDir, "show", ":staged.txt"); got != "staged index" {
			t.Fatalf("index staged.txt = %q", got)
		}
		for name, want := range map[string]string{
			"remote.txt":    "remote line\n",
			"local.txt":     "local commit\n",
			"staged.txt":    "staged index\nunstaged tail\n",
			"base.txt":      "base\nunstaged edit\n",
			"untracked.txt": "untracked keep\n",
		} {
			gotBytes, err := os.ReadFile(filepath.Join(rbDir, name))
			if err != nil || string(gotBytes) != want {
				t.Fatalf("%s = %q (%v), want %q", name, gotBytes, err, want)
			}
		}
		if got := gitOut(t, rbDir, "rev-parse", "refs/stash"); got != userStash {
			t.Fatalf("refs/stash = %q, want %q", got, userStash)
		}
		if stashes, err := rbRepo.Stashes(); err != nil || len(stashes) != 1 || stashes[0].Hash != userStash {
			t.Fatalf("stashes = %+v (%v)", stashes, err)
		}
		if hash, err := rbRepo.syncStash(); err != nil || hash != "" {
			t.Fatalf("syncStash marker = %q (%v)", hash, err)
		}
	})

	t.Run("PullRebaseStashConflictContinueAndAbortRestoresEdits", func(t *testing.T) {
		for _, mode := range []string{"continue", "abort"} {
			t.Run(mode, func(t *testing.T) {
				cbDir := initRepo(t)
				writeFile(t, cbDir, "shared.txt", "base\n")
				writeFile(t, cbDir, "base.txt", "base\n")
				gitRun(t, cbDir, "add", "shared.txt", "base.txt")
				gitRun(t, cbDir, "commit", "-q", "-m", "C0 base")
				cbOrigin := t.TempDir()
				gitRun(t, cbDir, "clone", "-q", "--bare", cbDir, cbOrigin)
				gitRun(t, cbDir, "remote", "add", "origin", cbOrigin)
				gitRun(t, cbDir, "push", "-q", "-u", "origin", "main")

				cbPeer := t.TempDir()
				gitRun(t, cbPeer, "clone", "-q", cbOrigin, cbPeer)
				gitRun(t, cbPeer, "config", "user.name", "Ada")
				gitRun(t, cbPeer, "config", "user.email", "ada@example.com")
				writeFile(t, cbPeer, "shared.txt", "remote shared\n")
				writeFile(t, cbPeer, "remote.txt", "remote added\n")
				gitRun(t, cbPeer, "add", "shared.txt", "remote.txt")
				gitRun(t, cbPeer, "commit", "-q", "-m", "R remote")
				gitRun(t, cbPeer, "push", "-q", "origin", "main")
				remoteHead := gitOut(t, cbPeer, "rev-parse", "HEAD")

				writeFile(t, cbDir, "shared.txt", "local shared\n")
				gitRun(t, cbDir, "commit", "-q", "-am", "L1 local conflict")
				localHead := gitOut(t, cbDir, "rev-parse", "HEAD")

				writeFile(t, cbDir, "base.txt", "older user stash\n")
				gitRun(t, cbDir, "stash", "push", "-q", "-m", "user stash keep")
				userStash := gitOut(t, cbDir, "rev-parse", "refs/stash")

				writeFile(t, cbDir, "staged.txt", "staged index\n")
				gitRun(t, cbDir, "add", "staged.txt")
				writeFile(t, cbDir, "staged.txt", "staged index\nunstaged tail\n")
				writeFile(t, cbDir, "base.txt", "base\nunstaged edit\n")
				writeFile(t, cbDir, "untracked.txt", "untracked keep\n")

				cbRepo := New(&LocalExecutor{Dir: cbDir})
				if err := cbRepo.PullOpts(PullOptions{Remote: "origin", Branch: "main", Rebase: true, Stash: true}); err == nil {
					t.Fatal("expected pull rebase conflict")
				}
				st, err := cbRepo.Status()
				if err != nil || st.State != "rebase" || !st.Conflicted() {
					t.Fatalf("status after conflict = %+v (%v)", st, err)
				}
				saved, err := cbRepo.syncStash()
				if err != nil || saved == "" {
					t.Fatalf("syncStash during conflict = %q (%v)", saved, err)
				}

				if mode == "abort" {
					if err := cbRepo.Abort("rebase"); err != nil {
						t.Fatal(err)
					}
					if got := gitOut(t, cbDir, "rev-parse", "HEAD"); got != localHead {
						t.Fatalf("HEAD after abort = %q, want %q", got, localHead)
					}
				} else {
					t.Setenv("GIT_EDITOR", "true")
					writeFile(t, cbDir, "shared.txt", "resolved shared\n")
					if err := cbRepo.Stage("shared.txt"); err != nil {
						t.Fatal(err)
					}
					if err := cbRepo.Continue("rebase"); err != nil {
						t.Fatal(err)
					}
					if got := gitOut(t, cbDir, "rev-parse", "HEAD~1"); got != remoteHead {
						t.Fatalf("rebased parent after continue = %q, want %q", got, remoteHead)
					}
					if got := gitOut(t, cbDir, "show", "HEAD:shared.txt"); got != "resolved shared" {
						t.Fatalf("HEAD:shared.txt = %q", got)
					}
				}

				if got := gitOut(t, cbDir, "diff", "--cached", "--name-only"); got != "staged.txt" {
					t.Fatalf("staged index after %s = %q, want staged.txt", mode, got)
				}
				if got := gitOut(t, cbDir, "show", ":staged.txt"); got != "staged index" {
					t.Fatalf("index staged.txt after %s = %q", mode, got)
				}
				wantFiles := map[string]string{
					"staged.txt":    "staged index\nunstaged tail\n",
					"base.txt":      "base\nunstaged edit\n",
					"untracked.txt": "untracked keep\n",
				}
				if mode == "abort" {
					wantFiles["shared.txt"] = "local shared\n"
				} else {
					wantFiles["shared.txt"] = "resolved shared\n"
					wantFiles["remote.txt"] = "remote added\n"
				}
				for name, want := range wantFiles {
					gotBytes, err := os.ReadFile(filepath.Join(cbDir, name))
					if err != nil || string(gotBytes) != want {
						t.Fatalf("%s after %s = %q (%v), want %q", name, mode, gotBytes, err, want)
					}
				}
				if got := gitOut(t, cbDir, "rev-parse", "refs/stash"); got != userStash {
					t.Fatalf("refs/stash after %s = %q, want %q", mode, got, userStash)
				}
				if stashes, err := cbRepo.Stashes(); err != nil || len(stashes) != 1 || stashes[0].Hash != userStash {
					t.Fatalf("stashes after %s = %+v (%v)", mode, stashes, err)
				}
				if hash, err := cbRepo.syncStash(); err != nil || hash != "" {
					t.Fatalf("syncStash marker after %s = %q (%v)", mode, hash, err)
				}
			})
		}
	})
}

func TestRebaseBackupAndUpdateRefs(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	gitRun(t, dir, "commit", "-q", "-am", "second")
	second, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "branch", "side")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "third")
	head, err := exec.Command("git", "-C", dir, "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan("HEAD~2")
	if err != nil || len(plan) != 2 {
		t.Fatal(err, plan)
	}
	plan[0].Action, plan[0].Subject = "reword", "renamed"
	if err := r.RebaseWithOptions("HEAD~2", plan, RebaseOptions{Backup: true, UpdateRefs: true}); err != nil {
		t.Fatal(err)
	}
	want := "refs/backup/main-" + strings.TrimSpace(string(head))
	if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", want).Output(); err != nil {
		t.Fatalf("backup %s: %v", want, err)
	}
	log, err := r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || log[1].Subject != "renamed" {
		t.Fatalf("reword %+v %v", subjects(log), err)
	}
	side, err := exec.Command("git", "-C", dir, "rev-parse", "side").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(side)) == strings.TrimSpace(string(second)) {
		t.Fatal("side stayed on the old commit")
	}
	if strings.TrimSpace(string(side)) != log[1].Hash {
		t.Fatalf("side %s, renamed %s", side, log[1].Hash)
	}
}

func TestCherryPickOrigin(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "feature")
	feature, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "checkout", "-q", "main")
	r := New(&LocalExecutor{Dir: dir})
	hash := strings.TrimSpace(string(feature))
	if err := r.CherryPickWith(hash, ReplayOptions{RecordOrigin: true}); err != nil {
		t.Fatal(err)
	}
	msg, err := r.Message("HEAD")
	if err != nil || !strings.Contains(msg, hash) {
		t.Fatalf("origin missing from %q %v", msg, err)
	}
}

func TestFlowFinish(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "branch", "develop")
	gitRun(t, dir, "checkout", "-q", "-b", "feature/pay")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "pay")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.FlowFinish(FlowFinishOptions{
		Kind: "feature", Branch: "feature/pay", Develop: "develop", NoFF: true, Delete: true,
	}); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.Branch != "develop" {
		t.Fatalf("on %q %v", st.Branch, err)
	}
	if _, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/heads/feature/pay").CombinedOutput(); err == nil {
		t.Fatal("feature branch stayed")
	}

	gitRun(t, dir, "checkout", "-q", "-b", "hotfix/1", "main")
	writeFile(t, dir, "c.txt", "fix\n")
	gitRun(t, dir, "add", "c.txt")
	gitRun(t, dir, "commit", "-q", "-m", "fix")
	if err := r.FlowFinish(FlowFinishOptions{
		Kind: "hotfix", Branch: "hotfix/1", Main: "main", Develop: "develop",
		Tag: "v1", Message: "hotfix 1", Delete: true, BackMerge: true,
	}); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.Branch != "develop" {
		t.Fatalf("back-merge left %q %v", st.Branch, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal("hotfix did not reach develop")
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "v1").CombinedOutput()
	if err != nil {
		t.Fatalf("tag: %v %s", err, out)
	}
}
