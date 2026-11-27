package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMergeFileWithCustomTool(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	gitcmd := func(allowFail bool, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil && !allowFail {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitcmd(false, "init", "-q", "-b", "main")
	write("base\n")
	gitcmd(false, "add", "a.txt")
	gitcmd(false, "commit", "-q", "-m", "base")
	gitcmd(false, "branch", "other")
	write("ours\n")
	gitcmd(false, "commit", "-q", "-am", "ours")
	gitcmd(false, "checkout", "-q", "other")
	write("theirs\n")
	gitcmd(false, "commit", "-q", "-am", "theirs")
	gitcmd(false, "checkout", "-q", "main")
	gitcmd(true, "merge", "other")

	r := New(&LocalExecutor{Dir: dir})
	if err := r.MergeFileWith("", "", "a.txt"); err == nil {
		t.Fatal("an empty tool should fail")
	}
	// The tool is given the four paths in the environment: merge by taking
	// the other side and noting the base was seen.
	if err := r.MergeFileWith("takeremote", `test -f "$BASE" && cp "$REMOTE" "$MERGED"`, "a.txt"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "theirs\n" {
		t.Fatalf("merged %q %v", got, err)
	}
	st, err := r.Status()
	if err != nil || st.Conflicted() {
		t.Fatalf("still conflicted %+v %v", st, err)
	}
}
