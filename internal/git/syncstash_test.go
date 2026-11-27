package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syncConflictRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	for _, name := range []string{"a.txt", "local.txt", "index.txt"} {
		writeFile(t, dir, name, "base\n")
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "base")
	gitRun(t, dir, "checkout", "-qb", "topic")
	writeFile(t, dir, "a.txt", "topic\n")
	gitRun(t, dir, "commit", "-qam", "topic")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "main\n")
	gitRun(t, dir, "commit", "-qam", "main")
	gitRun(t, dir, "checkout", "-q", "topic")
	return dir
}

func TestSyncStashSurvivesStops(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			var server *sshServer
			if transport == "ssh" {
				server = startSSHD(t)
			}
			for _, operation := range []string{"pull-merge", "pull-rebase", "checkout-merge", "checkout-rebase"} {
				t.Run(operation, func(t *testing.T) {
					actions := []string{"continue", "abort"}
					if strings.HasSuffix(operation, "merge") {
						actions = append(actions, "commit")
					}
					if strings.HasSuffix(operation, "rebase") {
						actions = append(actions, "skip")
					}
					for _, action := range actions {
						t.Run(action, func(t *testing.T) {
							dir := syncConflictRepo(t)
							t.Setenv("GIT_EDITOR", "false")
							writeFile(t, dir, "local.txt", "older stash\n")
							gitRun(t, dir, "stash", "push", "-qm", "older user stash")
							older := gitOut(t, dir, "rev-parse", "refs/stash")
							if strings.HasPrefix(operation, "checkout") {
								gitRun(t, dir, "checkout", "-qb", "parking")
							}
							writeFile(t, dir, "local.txt", "unsaved local\n")
							writeFile(t, dir, "index.txt", "staged local\n")
							gitRun(t, dir, "add", "index.txt")
							writeFile(t, dir, "untracked.txt", "untracked local\n")
							open := func() *Repo {
								if server != nil {
									return New(server.executor(t, dir))
								}
								return New(&LocalExecutor{Dir: dir})
							}
							r := open()
							var err error
							if strings.HasPrefix(operation, "pull") {
								err = r.PullOpts(PullOptions{Remote: ".", Branch: "main", Rebase: strings.HasSuffix(operation, "rebase"), Stash: true})
							} else {
								sync := SyncMerge
								if strings.HasSuffix(operation, "rebase") {
									sync = SyncRebase
								}
								err = r.CheckoutAndSync("topic", "main", sync, true, false)
							}
							if err == nil {
								t.Fatal("expected conflict")
							}
							st, err := r.Status()
							if err != nil || !st.Conflicted() || st.State == "" {
								t.Fatalf("not stopped: %+v %v", st, err)
							}
							saved := gitOut(t, dir, "rev-parse", syncStashRef)
							// Change the head of the stash list while the operation is paused.
							gitRun(t, dir, "stash", "store", "-m", "newer user stash", older)
							r = open() // No application memory is retained.
							switch action {
							case "abort":
								err = r.Abort(st.State)
							case "skip":
								err = r.Skip(st.State)
							default:
								if err = r.TakeConflictSide("a.txt", "theirs"); err != nil {
									t.Fatal(err)
								}
								if action == "commit" {
									err = r.Commit("Resolve sync merge", false)
								} else {
									err = r.Continue(st.State)
								}
							}
							if err != nil {
								t.Fatal(err)
							}
							for file, want := range map[string]string{"local.txt": "unsaved local\n", "index.txt": "staged local\n", "untracked.txt": "untracked local\n"} {
								got, err := os.ReadFile(filepath.Join(dir, file))
								if err != nil || string(got) != want {
									t.Fatalf("%s: %q %v", file, got, err)
								}
							}
							if got := gitOut(t, dir, "diff", "--cached", "--name-only"); got != "index.txt" {
								t.Fatalf("index not restored: %q", got)
							}
							if got := gitOut(t, dir, "stash", "list", "--format=%H"); got != older+"\n"+older {
								t.Fatalf("wrong stash removed (%s): %s", saved, got)
							}
							if hash, err := r.syncStash(); err != nil || hash != "" {
								t.Fatalf("marker left %s %v", hash, err)
							}
							if st, err := r.Status(); err != nil || st.State != "" {
								t.Fatalf("unfinished: %+v %v", st, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestSyncStashRecoveryAfterListCleared(t *testing.T) {
	dir := syncConflictRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	writeFile(t, dir, "local.txt", "my work\n")
	if err := r.CheckoutAndSync("topic", "main", SyncMerge, true, false); err == nil {
		t.Fatal("expected conflict")
	}
	gitRun(t, dir, "stash", "clear")
	// The private worktree ref still protects the saved object from GC.
	gitRun(t, dir, "gc", "--prune=now")
	if err := New(&LocalExecutor{Dir: dir}).Abort("merge"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "local.txt"))
	if err != nil || string(got) != "my work\n" {
		t.Fatalf("lost changes: %q %v", got, err)
	}
}

func TestSyncStashRestoreConflictKeepsRecoveryCopy(t *testing.T) {
	dir := syncConflictRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	writeFile(t, dir, "local.txt", "saved local work\n")
	if err := r.CheckoutAndSync("topic", "main", SyncMerge, true, false); err == nil {
		t.Fatal("expected conflict")
	}
	// A file created while resolving conflicts prevents an untracked restore.
	// Use a tracked conflicting edit to exercise failed stash application.
	writeFile(t, dir, "local.txt", "changed while resolving\n")
	if err := r.Abort("merge"); err == nil {
		t.Fatal("expected restore refusal")
	}
	entries, err := r.Stashes()
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery copy missing: %+v %v", entries, err)
	}
	if got := gitOut(t, dir, "show", entries[0].Hash+":local.txt"); got != "saved local work" {
		t.Fatal(got)
	}
	if hash, err := r.syncStash(); err != nil || hash != "" {
		t.Fatalf("would apply twice: %s %v", hash, err)
	}
}

func TestSyncStashIsPrivateToLinkedWorktree(t *testing.T) {
	dir := syncConflictRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, dir, "worktree", "add", "-q", "-b", "linked", linked, "main")
	r := New(&LocalExecutor{Dir: dir})
	writeFile(t, dir, "local.txt", "saved here\n")
	if err := r.CheckoutAndSync("topic", "main", SyncMerge, true, false); err == nil {
		t.Fatal("expected conflict")
	}
	other := New(&LocalExecutor{Dir: linked})
	if hash, err := other.syncStash(); err != nil || hash != "" {
		t.Fatalf("marker leaked: %s %v", hash, err)
	}
	if err := other.restoreSyncStash(nil); err != nil {
		t.Fatal(err)
	}
	if hash, err := r.syncStash(); err != nil || hash == "" {
		t.Fatalf("original marker lost: %s %v", hash, err)
	}
	if err := r.Abort("merge"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncStashRestoredWhenOperationFailsBeforeStarting(t *testing.T) {
	for _, operation := range []string{"pull", "checkout"} {
		t.Run(operation, func(t *testing.T) {
			dir := syncConflictRepo(t)
			r := New(&LocalExecutor{Dir: dir})
			writeFile(t, dir, "local.txt", "unsaved local\n")
			writeFile(t, dir, "untracked.txt", "untracked local\n")
			var err error
			if operation == "pull" {
				err = r.PullOpts(PullOptions{Remote: "missing-remote", Stash: true})
			} else {
				err = r.CheckoutAndSync("missing-branch", "", SyncCheckout, true, false)
			}
			if err == nil {
				t.Fatal("expected failure")
			}
			for file, want := range map[string]string{"local.txt": "unsaved local\n", "untracked.txt": "untracked local\n"} {
				got, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q %v", file, got, err)
				}
			}
			if entries, err := r.Stashes(); err != nil || len(entries) != 0 {
				t.Fatalf("stash left: %+v %v", entries, err)
			}
			if hash, err := r.syncStash(); err != nil || hash != "" {
				t.Fatalf("marker left: %s %v", hash, err)
			}
		})
	}
}
