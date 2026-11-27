package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRebaseAutostashSurvivesStops(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			var server *sshServer
			if transport == "ssh" {
				server = startSSHD(t)
			}
			for _, interactive := range []bool{false, true} {
				kind := "ordinary"
				if interactive {
					kind = "interactive"
				}
				t.Run(kind, func(t *testing.T) {
					for _, abort := range []bool{false, true} {
						action := "continue"
						if abort {
							action = "abort"
						}
						t.Run(action, func(t *testing.T) {
							dir := initRepo(t)
							t.Setenv("GIT_EDITOR", "false")
							writeFile(t, dir, "a.txt", "base\n")
							writeFile(t, dir, "local.txt", "base local\n")
							gitRun(t, dir, "add", ".")
							gitRun(t, dir, "commit", "-qm", "base")
							gitRun(t, dir, "checkout", "-qb", "feature")
							writeFile(t, dir, "a.txt", "feature\n")
							gitRun(t, dir, "commit", "-qam", "feature")
							gitRun(t, dir, "checkout", "-q", "main")
							writeFile(t, dir, "a.txt", "main\n")
							gitRun(t, dir, "commit", "-qam", "main")
							gitRun(t, dir, "checkout", "-q", "feature")
							writeFile(t, dir, "local.txt", "older saved changes\n")
							gitRun(t, dir, "stash", "push", "-qm", "existing user stash")
							existing := gitOut(t, dir, "stash", "list", "--format=%H")
							writeFile(t, dir, "local.txt", "unsaved local work\n")
							writeFile(t, dir, "untracked.txt", "untracked work\n")
							open := func() *Repo {
								if server != nil {
									return New(server.executor(t, dir))
								}
								return New(&LocalExecutor{Dir: dir})
							}
							r := open()
							var err error
							if interactive {
								plan, e := r.RebasePlan("main")
								if e != nil {
									t.Fatal(e)
								}
								err = r.RebaseWith("main", plan)
							} else {
								err = r.RebaseOnto("main", RebaseOntoOptions{Stash: true})
							}
							st, stateErr := r.Status()
							if stateErr != nil || st.State != "rebase" || !st.Conflicted() {
								t.Fatalf("expected conflict stop: %+v %v, start error %v", st, stateErr, err)
							}
							if got := gitOut(t, dir, "show", "HEAD:local.txt"); got != "base local" {
								t.Fatal(got)
							}
							r = open() // Reopen after a conflict, losing all application memory.
							if abort {
								err = r.Abort("rebase")
							} else {
								if e := r.TakeConflictSide("a.txt", "theirs"); e != nil {
									t.Fatal(e)
								}
								err = r.Continue("rebase")
							}
							if err != nil {
								t.Fatal(err)
							}
							for name, want := range map[string]string{"local.txt": "unsaved local work\n", "untracked.txt": "untracked work\n"} {
								got, err := os.ReadFile(filepath.Join(dir, name))
								if err != nil || string(got) != want {
									t.Fatalf("%s: %q %v", name, got, err)
								}
							}
							if got := gitOut(t, dir, "stash", "list", "--format=%H"); got != existing {
								t.Fatalf("existing stash changed: %s, want %s", got, existing)
							}
							if st, err := r.Status(); err != nil || st.State != "" || st.Branch != "feature" {
								t.Fatalf("unfinished: %+v %v", st, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestRebaseAutostashRestoreConflictKeepsChanges(t *testing.T) {
	dir, r := stackedRepo(t)
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "upstream replaces content\n")
	gitRun(t, dir, "commit", "-qam", "upstream edit")
	gitRun(t, dir, "checkout", "-q", "feature")
	writeFile(t, dir, "a.txt", "local unsaved edit\n")
	if err := r.RebaseOnto("main", RebaseOntoOptions{Stash: true}); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.State != "" || !st.Conflicted() {
		t.Fatalf("restore conflict not visible: %+v %v", st, err)
	}
	if got := gitOut(t, dir, "show", "stash@{0}:a.txt"); got != "local unsaved edit" {
		t.Fatalf("unsaved work was not retained: %s", got)
	}
}

func TestInteractiveRebaseReportsHookFailure(t *testing.T) {
	dir, hashes := linearRepo(t, 3)
	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan(hashes[0])
	if err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-rebase"), []byte("#!/bin/sh\necho 'rebase rejected by test hook' >&2\nexit 1\n"), 0o755))
	if err := r.RebaseWith(hashes[0], plan); err == nil {
		t.Fatal("a failed hook was reported as a successful rebase stop")
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != hashes[2] {
		t.Fatal("hook rejection changed HEAD")
	}
}
