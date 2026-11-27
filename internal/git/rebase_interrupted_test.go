package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHPreparedRemoteCommandBuffersStderrAndPreservesExitCode(t *testing.T) {
	srv := startSSHD(t)
	dir, hashes := linearRepo(t, 4)

	open := func() *Repo {
		return New(srv.executor(t, dir))
	}

	r := open()
	plan, err := r.RebasePlan(hashes[0])
	must(t, err)
	if len(plan) != 3 {
		t.Fatalf("expected 3 steps in plan, got %d", len(plan))
	}
	plan[0].PreparedMessage = "Reworded c2 over SSH\n\nBody of c2"
	plan[1].Action = "edit"
	plan[2].PreparedMessage = "Reworded c4 over SSH\n\nBody of c4"

	must(t, r.RebaseWithOptions(hashes[0], plan, RebaseOptions{Backup: true}))
	st, err := r.Status()
	must(t, err)
	if st.State != "rebase" || st.Stopped != "edit" || st.Operation != hashes[2] {
		t.Fatalf("expected edit stop on c3 (%s), got %+v", hashes[2], st)
	}
	must(t, r.Continue("rebase"))
	st, err = r.Status()
	must(t, err)
	if st.State != "" {
		t.Fatalf("expected rebase finished, got %+v", st)
	}
	if msg, err := r.Message("HEAD~2"); err != nil || msg != plan[0].PreparedMessage {
		t.Fatalf("HEAD~2 message = %q (err=%v), want %q", msg, err, plan[0].PreparedMessage)
	}
	if msg, err := r.Message("HEAD"); err != nil || msg != plan[2].PreparedMessage {
		t.Fatalf("HEAD message = %q (err=%v), want %q", msg, err, plan[2].PreparedMessage)
	}

	hookRepo, hookHashes := linearRepo(t, 3)
	rHook := New(srv.executor(t, hookRepo))
	hookPlan, err := rHook.RebasePlan(hookHashes[0])
	must(t, err)
	must(t, os.WriteFile(filepath.Join(hookRepo, ".git", "hooks", "pre-rebase"), []byte("#!/bin/sh\necho 'ssh pre-rebase hook failure' >&2\nexit 1\n"), 0o755))
	err = rHook.RebaseWith(hookHashes[0], hookPlan)
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "ssh pre-rebase hook failure") {
		t.Fatalf("expected hook failure with stderr, got err=%v (code=%d)", err, ExitCode(err))
	}

	_, err = rHook.runPrepared(nil, "-c", "alias.fail17=!echo 'custom failure 17' >&2; exit 17", "fail17")
	if err == nil || ExitCode(err) != 17 || !strings.Contains(err.Error(), "custom failure 17") {
		t.Fatalf("expected exit 17 with custom stderr, got err=%v (code=%d)", err, ExitCode(err))
	}
}

func TestRepairInterruptedRebaseTodoAndStatusTarget(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			var srv *sshServer
			if transport == "ssh" {
				srv = startSSHD(t)
			}
			for _, mode := range []string{"continue", "abort"} {
				t.Run(mode, func(t *testing.T) {
					dir, hashes := linearRepo(t, 4)
					writeFile(t, dir, "f1.txt", "dirty unstaged bytes\n")
					open := func() *Repo {
						if srv != nil {
							return New(srv.executor(t, dir))
						}
						return New(&LocalExecutor{Dir: dir})
					}
					r := open()
					plan, err := r.RebasePlan(hashes[0])
					must(t, err)
					plan[0].PreparedMessage = "Prepared c2\n\nBody c2"
					plan[1].Action = "edit"
					plan[2].PreparedMessage = "Prepared c4\n\nBody c4"

					must(t, r.RebaseWithOptions(hashes[0], plan, RebaseOptions{Backup: true}))
					st, err := r.Status()
					must(t, err)
					if st.State != "rebase" || st.Stopped != "edit" || st.Operation != hashes[2] {
						t.Fatalf("initial stop: %+v", st)
					}

					rewrittenC2 := gitOut(t, dir, "rev-parse", "HEAD~1")
					gitRun(t, dir, "reset", "--hard", rewrittenC2)
					rmDir := filepath.Join(dir, ".git", "rebase-merge")
					for _, f := range []string{"REBASE_HEAD", "orig-head.lock"} {
						_ = os.Remove(filepath.Join(dir, ".git", f))
					}
					for _, f := range []string{"stopped-sha", "amend", "message", "patch", "author-script"} {
						_ = os.Remove(filepath.Join(rmDir, f))
					}

					beforeDone, err := os.ReadFile(filepath.Join(rmDir, "done"))
					must(t, err)
					beforeTodo, err := os.ReadFile(filepath.Join(rmDir, "git-rebase-todo"))
					must(t, err)
					beforeOrigHead, err := os.ReadFile(filepath.Join(rmDir, "orig-head"))
					must(t, err)

					stCorrupt, err := r.Status()
					must(t, err)
					if stCorrupt.State != "rebase" || stCorrupt.Stopped != "" || !strings.HasPrefix(stCorrupt.Operation, hashes[2]+":") || len(stCorrupt.Operation) <= len(hashes[2])+1 {
						t.Fatalf("corrupted rebase status = %+v, want state=rebase stop=\"\" operation=%s:<digest>", stCorrupt, hashes[2])
					}

					if mode == "abort" {
						must(t, r.Abort("rebase"))
						afterAbort, err := r.Status()
						must(t, err)
						if afterAbort.State != "" || afterAbort.Head != hashes[3] {
							t.Fatalf("after abort: %+v, want head=%s", afterAbort, hashes[3])
						}
						if got, err := os.ReadFile(filepath.Join(dir, "f1.txt")); err != nil || string(got) != "dirty unstaged bytes\n" {
							t.Fatalf("autostash dirty bytes after abort: %q (%v)", got, err)
						}
						return
					}

					if err := r.RepairInterruptedRebaseTodo(); err == nil {
						t.Fatal("expected RepairInterruptedRebaseTodo to refuse unknown rebase state without REBASE_HEAD")
					}
					afterDone, err := os.ReadFile(filepath.Join(rmDir, "done"))
					must(t, err)
					afterTodo, err := os.ReadFile(filepath.Join(rmDir, "git-rebase-todo"))
					must(t, err)
					afterOrigHead, err := os.ReadFile(filepath.Join(rmDir, "orig-head"))
					must(t, err)
					if string(beforeDone) != string(afterDone) || string(beforeTodo) != string(afterTodo) || string(beforeOrigHead) != string(afterOrigHead) {
						t.Fatal("RepairInterruptedRebaseTodo mutated rebase-merge files on refusal")
					}
					if headNow := gitOut(t, dir, "rev-parse", "HEAD"); headNow != rewrittenC2 {
						t.Fatalf("HEAD mutated on refusal: got %s want %s", headNow, rewrittenC2)
					}

					must(t, os.WriteFile(filepath.Join(rmDir, "git-rebase-todo"), append(beforeTodo, []byte("# comment or step change\n")...), 0o644))
					stChangedTodo, err := r.Status()
					must(t, err)
					if stChangedTodo.Operation == stCorrupt.Operation {
						t.Fatalf("expected Status.Operation to change when git-rebase-todo changes under same HEAD and last done hash, got identical %q", stChangedTodo.Operation)
					}
				})
			}
		})
	}
}

func TestRepairInterruptedRebaseDoesNotRequeueAlreadyAppliedCommit(t *testing.T) {
	dir, hashes := linearRepo(t, 4)
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan(hashes[0])
	must(t, err)
	plan[0].PreparedMessage = "Rewritten first"
	plan[1].Action = "edit"
	must(t, r.RebaseWithOptions(hashes[0], plan, RebaseOptions{Backup: true}))
	// Model interruption after the done commit has applied, before stop metadata persists.
	rmDir := filepath.Join(dir, ".git", "rebase-merge")
	must(t, os.Remove(filepath.Join(dir, ".git", "REBASE_HEAD")))
	for _, f := range []string{"stopped-sha", "amend", "message", "patch", "author-script"} {
		if err := os.Remove(filepath.Join(rmDir, f)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	beforeDone, err := os.ReadFile(filepath.Join(rmDir, "done"))
	must(t, err)
	beforeTodo, err := os.ReadFile(filepath.Join(rmDir, "git-rebase-todo"))
	must(t, err)
	_ = r.RepairInterruptedRebaseTodo()
	afterDone, err := os.ReadFile(filepath.Join(rmDir, "done"))
	must(t, err)
	afterTodo, err := os.ReadFile(filepath.Join(rmDir, "git-rebase-todo"))
	must(t, err)
	if string(beforeDone) != string(afterDone) || string(beforeTodo) != string(afterTodo) {
		t.Fatal("already-applied last done commit was blindly moved back into todo")
	}
}
