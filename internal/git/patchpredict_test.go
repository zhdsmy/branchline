package git

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPredictPatchAndCommitHeader(t *testing.T) {
	dir := testRepo(t)
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	gitIn("stash", "-u", "-q")
	mail := gitIn("format-patch", "--stdout", "-1", "HEAD~1")
	bare := gitIn("diff", "HEAD~2", "HEAD~1")
	gitIn("reset", "-q", "--hard", "HEAD~2")

	if !PatchHasCommitHeader([]byte(mail)) {
		t.Fatalf("a format-patch mail has no commit header:\n%s", mail)
	}
	if PatchHasCommitHeader([]byte(bare)) {
		t.Fatalf("a plain diff has a commit header:\n%s", bare)
	}
	if !PatchHasCommitHeader([]byte("Subject: a change\n\n---\n" + bare)) {
		t.Fatal("a Subject line is a commit header")
	}

	r := New(&LocalExecutor{Dir: dir})
	if p := r.PredictPatch([]byte(mail), ApplyOptions{Stage: true}); !p.Clean || p.Conflicts {
		t.Fatalf("a patch that applies: %+v", p)
	}
	if st, _ := r.Status(); len(st.Unstaged)+len(st.Staged) != 0 {
		t.Fatalf("predicting changed the tree: %+v", st)
	}

	// The same change on a file that moved on conflicts.
	if err := os.WriteFile(dir+"/a.txt", []byte("uno\ndos\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := r.PredictPatch([]byte(bare), ApplyOptions{})
	if p.Clean || !p.Conflicts || !strings.Contains(p.Message, "a.txt") {
		t.Fatalf("a patch that conflicts: %+v", p)
	}

	// Text that is no patch is not a conflict.
	p = r.PredictPatch([]byte("hello\n"), ApplyOptions{})
	if p.Clean || p.Conflicts || p.Message == "" {
		t.Fatalf("not a patch: %+v", p)
	}

	// git am stopped on a patch that does not apply is the "am" state.
	gitIn("checkout", "-q", "--", "a.txt")
	gitIn("commit", "-q", "--allow-empty", "-m", "x")
	broken := strings.Replace(mail, " one\n", " uno\n", 1)
	if _, err := r.ApplyPatchWith([]byte(broken), ApplyOptions{Commit: true}); err == nil {
		t.Fatal("a broken patch applied")
	}
	st, err := r.Status()
	if err != nil || st.State != "am" {
		t.Fatalf("state %q: %v", st.State, err)
	}
	if err := r.Abort("am"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(); st.State != "" {
		t.Fatalf("still %q after abort", st.State)
	}
}
