package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebaseExplicitPreparedTailSurvivesStopsAndReopen(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "ssh"
		}
		t.Run(name, func(t *testing.T) {
			var server *sshServer
			if remote {
				server = startSSHD(t)
			}
			for _, stop := range []string{"none", "edit", "conflict"} {
				t.Run(stop, func(t *testing.T) {
					dir, hashes := linearRepo(t, 4)
					open := func() *Repo {
						if server != nil {
							return New(server.executor(t, dir))
						}
						return New(&LocalExecutor{Dir: dir})
					}
					onto := "--root"
					if stop == "conflict" {
						gitRun(t, dir, "checkout", "-qb", "upstream", hashes[0])
						writeFile(t, dir, "f2.txt", "upstream conflict\n")
						gitRun(t, dir, "add", ".")
						gitRun(t, dir, "commit", "-qm", "upstream")
						onto = gitOut(t, dir, "rev-parse", "HEAD")
						gitRun(t, dir, "checkout", "-q", "main")
					}
					r := open()
					plan, err := r.RebasePlan(onto)
					must(t, err)
					plan[0].Action = "pick"
					if stop == "edit" {
						plan[0].Action = "edit"
					}
					for i := 1; i < len(plan); i++ {
						plan[i].Action, plan[i].Subject = "fixup", ""
					}
					want := "Prepared full message ' $HOME $(false)\n\nbody line one\nbody line two"
					plan[len(plan)-1].PreparedMessage = want
					gitRun(t, dir, "branch", "dependent", hashes[len(hashes)-1])
					gitRun(t, dir, "branch", "dependent-parent", plan[0].Hash)
					must(t, r.RebaseWithOptions(onto, plan, RebaseOptions{Backup: true, UpdateRefs: true}))
					if stop != "none" {
						st, err := r.Status()
						must(t, err)
						if st.State != "rebase" || st.Stopped != stop {
							t.Fatalf("stop %+v", st)
						}
						if stop == "conflict" {
							must(t, r.TakeConflictSide("f2.txt", "theirs"))
						}
						r = open()
						must(t, r.Continue("rebase"))
					}
					if msg, err := r.Message("HEAD"); err != nil || msg != want {
						t.Fatalf("prepared message %q %v", msg, err)
					}
					if gitOut(t, dir, "rev-parse", "HEAD") != gitOut(t, dir, "rev-parse", "dependent") {
						t.Fatal("dependent ref precedes prepared amend")
					}
					if gitOut(t, dir, "rev-parse", "HEAD") != gitOut(t, dir, "rev-parse", "dependent-parent") {
						t.Fatal("parent dependent ref was not updated after chain message")
					}
					for i := 1; i <= 4; i++ {
						name := "f" + string(rune('0'+i)) + ".txt"
						if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != name+"\n" {
							t.Fatalf("file %s: %q %v", name, got, err)
						}
					}
					if gitOut(t, dir, "status", "--porcelain") != "" {
						t.Fatal("prepared tail left worktree/index changes")
					}
					for _, name := range []string{"rebase-merge", "rebase-apply"} {
						if _, err := os.Stat(filepath.Join(dir, ".git", name)); !os.IsNotExist(err) {
							t.Fatal("left rebase state", err)
						}
					}
				})
			}
		})
	}
}

func TestRebaseExplicitPreparedMessageSkipDoesNotAmendPreviousCommit(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	gitRun(t, dir, "checkout", "-qb", "upstream", hashes[0])
	writeFile(t, dir, "f2.txt", "upstream content\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "Upstream preserved\n\nUpstream body")
	onto := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "main")
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan(onto)
	must(t, err)
	plan[0].PreparedMessage = "Skipped commit must never become upstream message\n\nSkipped body"
	must(t, r.RebaseWith(onto, plan))
	st, err := r.Status()
	must(t, err)
	if st.State != "rebase" || st.Stopped != "conflict" {
		t.Fatalf("expected conflict %+v", st)
	}
	r = New(&LocalExecutor{Dir: dir})
	must(t, r.Skip("rebase"))
	if got := gitOut(t, dir, "log", "-1", "--format=%B", "HEAD~"); got != "Upstream preserved\n\nUpstream body" {
		t.Fatalf("skip amended previous commit: %q", got)
	}
	if gitOut(t, dir, "rev-parse", "HEAD~") != onto {
		t.Fatal("skip rewrote upstream identity")
	}
	if gitOut(t, dir, "log", "-1", "--format=%s") != "c3" || gitOut(t, dir, "status", "--porcelain") != "" {
		t.Fatal("skip did not finish remaining plan cleanly")
	}
}

func TestRebaseExplicitPreparedMessageAbortKeepsOriginalHistory(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	before, tree := hashes[2], gitOut(t, dir, "rev-parse", "HEAD^{tree}")
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan("--root")
	must(t, err)
	plan[0].Action = "edit"
	plan[1].PreparedMessage = "Later prepared message\n\nLater body"
	must(t, r.RebaseWith("--root", plan))
	r = New(&LocalExecutor{Dir: dir})
	must(t, r.Abort("rebase"))
	if gitOut(t, dir, "rev-parse", "HEAD") != before || gitOut(t, dir, "write-tree") != tree || gitOut(t, dir, "status", "--porcelain") != "" {
		t.Fatal("abort changed original history or index")
	}
	if got := gitOut(t, dir, "log", "--format=%s", "--reverse"); strings.Join(strings.Fields(got), " ") != "c1 c2 c3" {
		t.Fatal(got)
	}
}

func TestRebaseExplicitPreparedMessageAfterEmptyFixup(t *testing.T) {
	dir, _ := linearRepo(t, 3)
	gitRun(t, dir, "commit", "--allow-empty", "-qm", "empty fixup")
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan("--root")
	must(t, err)
	for i := 1; i < len(plan); i++ {
		plan[i].Action, plan[i].Subject = "fixup", ""
	}
	plan[len(plan)-1].PreparedMessage = "Full chain message after empty tail\n\nThe empty Fixup must not suppress this body"
	must(t, r.RebaseWith("--root", plan))
	if got, err := r.Message("HEAD"); err != nil || got != plan[len(plan)-1].PreparedMessage {
		t.Fatalf("empty tail: %q %v", got, err)
	}
	if gitOut(t, dir, "rev-list", "--count", "HEAD") != "1" || gitOut(t, dir, "status", "--porcelain") != "" {
		t.Fatal("empty Fixup did not fold cleanly")
	}
}
