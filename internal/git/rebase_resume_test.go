package git

import (
	"strings"
	"testing"
)

func TestRebasePreparedMessagesResume(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			var server *sshServer
			if transport == "ssh" {
				server = startSSHD(t)
			}
			for _, stop := range []string{"edit", "conflict"} {
				t.Run(stop, func(t *testing.T) {
					dir, hashes := linearRepo(t, 3)
					t.Setenv("GIT_SEQUENCE_EDITOR", "false")
					open := func() *Repo {
						if server != nil {
							return New(server.executor(t, dir))
						}
						return New(&LocalExecutor{Dir: dir})
					}
					r := open()
					onto := hashes[0]
					if stop == "conflict" {
						gitRun(t, dir, "checkout", "-q", "-b", "other", hashes[0])
						writeFile(t, dir, "f2.txt", "conflicting upstream\n")
						gitRun(t, dir, "add", ".")
						gitRun(t, dir, "commit", "-qm", "upstream")
						onto = gitOut(t, dir, "rev-parse", "HEAD")
						gitRun(t, dir, "checkout", "-q", "main")
					}
					plan, err := r.RebasePlan(onto)
					if err != nil || len(plan) != 2 {
						t.Fatalf("plan: %+v %v", plan, err)
					}
					message := "renamed 'commit' $HOME $(false)\n\nbody with shell characters; and a newline"
					if stop == "edit" {
						plan[0].Action = "edit"
					}
					plan[1].Action, plan[1].Subject = "reword", message
					if err := r.RebaseWith(onto, plan); err != nil {
						t.Fatal(err)
					}
					st, err := r.Status()
					if err != nil || st.State != "rebase" || st.Stopped != stop {
						t.Fatalf("stop: %+v %v", st, err)
					}
					// The earlier commit must not acquire the later commit's message.
					if got := gitOut(t, dir, "log", "-1", "--format=%s"); strings.HasPrefix(got, "renamed") {
						t.Fatalf("wrong commit amended before %s: %s", stop, got)
					}
					if stop == "conflict" {
						if err := r.TakeConflictSide("f2.txt", "theirs"); err != nil {
							t.Fatal(err)
						}
					}
					// A new Repo models closing and reopening the app at the stop.
					r = open()
					if err := r.Continue("rebase"); err != nil {
						t.Fatal(err)
					}
					if got, err := r.Message("HEAD"); err != nil || got != message {
						t.Fatalf("message: %q %v", got, err)
					}
					if got := gitOut(t, dir, "log", "-1", "--format=%s", "HEAD~"); got != "c2" {
						t.Fatalf("previous commit changed: %q", got)
					}
					if st, err := r.Status(); err != nil || st.State != "" {
						t.Fatalf("unfinished: %+v %v", st, err)
					}
				})
			}
		})
	}
}

func TestRebasePreparedMessagesAndSquash(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		name := "combined"
		if supplied {
			name = "supplied"
		}
		t.Run(name, func(t *testing.T) {
			dir, hashes := linearRepo(t, 4)
			t.Setenv("GIT_SEQUENCE_EDITOR", "false")
			r := New(&LocalExecutor{Dir: dir})
			plan, err := r.RebasePlan(hashes[0])
			if err != nil {
				t.Fatal(err)
			}
			plan[0].Action, plan[0].Subject = "reword", "first message\n\nfirst body"
			plan[1].Action, plan[1].Subject = "squash", ""
			expected := "first message\n\nfirst body\n\nc3"
			if supplied {
				plan[1].Subject = "squashed message\n\ncombined body"
				expected = plan[1].Subject
			}
			plan[2].Action, plan[2].Subject = "reword", "last message\n\nlast body"
			if err := r.RebaseWith(hashes[0], plan); err != nil {
				t.Fatal(err)
			}
			if got, err := r.Message("HEAD~"); err != nil || got != expected {
				t.Fatalf("squash: %q, want %q, %v", got, expected, err)
			}
			if got, err := r.Message("HEAD"); err != nil || got != plan[2].Subject {
				t.Fatalf("last reword: %q %v", got, err)
			}
			if got := gitOut(t, dir, "rev-list", "--count", "HEAD"); got != "3" {
				t.Fatalf("commit count: %s", got)
			}
		})
	}
}
