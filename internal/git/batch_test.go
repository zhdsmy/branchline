package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linearRepo makes main with commits c1..cn, each adding a file, and
// returns their hashes, oldest first.
func linearRepo(t *testing.T, n int) (string, []string) {
	t.Helper()
	// An editor git opened would wait for a terminal: fail instead.
	t.Setenv("GIT_EDITOR", "false")
	dir := initRepo(t)
	var hashes []string
	for i := 1; i <= n; i++ {
		name := "f" + string(rune('0'+i)) + ".txt"
		writeFile(t, dir, name, name+"\n")
		gitRun(t, dir, "add", name)
		gitRun(t, dir, "commit", "-q", "-m", "c"+string(rune('0'+i)))
		hashes = append(hashes, gitOut(t, dir, "rev-parse", "HEAD"))
	}
	return dir, hashes
}

func TestRemoveBranchesAndTags(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	for _, b := range []string{"one", "two", "three"} {
		gitRun(t, dir, "branch", b)
	}
	gitRun(t, dir, "push", "-q", "-u", "origin", "one", "two")
	// two is gone from the remote already: deleting it there is no error.
	gitRun(t, bare, "branch", "-D", "two")
	err := r.RemoveBranches([]RemoveBranchOptions{
		{Name: "one", Force: true, Upstream: "origin/one"},
		{Name: "two", Force: true, Upstream: "origin/two"},
		{Name: "three", Force: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"one", "two", "three"} {
		if hasRef(dir, "refs/heads/"+b) || hasRef(bare, "refs/heads/"+b) {
			t.Fatalf("%s stayed: %s | %s", b, gitOut(t, dir, "branch"), gitOut(t, bare, "branch"))
		}
	}

	gitRun(t, dir, "branch", "r1")
	gitRun(t, dir, "branch", "r2")
	gitRun(t, dir, "push", "-q", "origin", "r1", "r2")
	if err := r.DeleteRemoteBranches([]string{"origin/r1", "origin/r2"}); err != nil {
		t.Fatal(err)
	}
	if hasRef(bare, "refs/heads/r1") || hasRef(bare, "refs/heads/r2") || !hasRef(dir, "refs/heads/r1") {
		t.Fatalf("remote branches: %s", gitOut(t, bare, "branch"))
	}
	if err := r.DeleteRemoteBranches([]string{"nope"}); err == nil {
		t.Fatal("a name without a remote was accepted")
	}

	for _, tag := range []string{"t1", "t2", "t3"} {
		gitRun(t, dir, "tag", tag)
	}
	// t3 is only here: the remote skips it.
	gitRun(t, dir, "push", "-q", "origin", "t1", "t2")
	if err := r.DeleteTags([]string{"t1", "t2", "t3"}, []string{"origin"}); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"t1", "t2", "t3"} {
		if hasRef(dir, "refs/tags/"+tag) || hasRef(bare, "refs/tags/"+tag) {
			t.Fatalf("tag %s stayed", tag)
		}
	}
}

func TestStashDropMany(t *testing.T) {
	dir, _ := linearRepo(t, 1)
	r := New(&LocalExecutor{Dir: dir})
	for _, msg := range []string{"s0", "s1", "s2", "s3"} {
		writeFile(t, dir, "f1.txt", msg+"\n")
		gitRun(t, dir, "stash", "push", "-q", "-m", msg)
	}
	// stash@{0} is s3, the newest. Drop s3 and s1 and s0.
	if err := r.StashDropMany([]string{"stash@{0}", "stash@{3}", "stash@{2}"}); err != nil {
		t.Fatal(err)
	}
	list, err := r.Stashes()
	if err != nil || len(list) != 1 || !strings.Contains(list[0].Message, "s2") {
		t.Fatalf("left %+v %v", list, err)
	}
	if err := r.StashDropMany([]string{"HEAD"}); err == nil {
		t.Fatal("a non-stash was accepted")
	}
}

func TestCherryPickAndRevertMany(t *testing.T) {
	dir, hashes := linearRepo(t, 1)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "checkout", "-q", "-b", "topic")
	var picks []string
	for _, name := range []string{"x", "y", "z"} {
		writeFile(t, dir, name+".txt", name+"\n")
		gitRun(t, dir, "add", name+".txt")
		gitRun(t, dir, "commit", "-q", "-m", "add "+name)
		picks = append(picks, gitOut(t, dir, "rev-parse", "HEAD"))
	}
	gitRun(t, dir, "checkout", "-q", "main")
	if err := r.CherryPickMany([]string{picks[0], picks[2]}, ReplayOptions{RecordOrigin: true}); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s", hashes[0]+"..HEAD"); got != "add z\nadd x" {
		t.Fatalf("picked %q", got)
	}
	if body := gitOut(t, dir, "log", "-1", "--format=%b"); !strings.Contains(body, picks[2]) {
		t.Fatalf("-x missing: %q", body)
	}
	head := gitOut(t, dir, "rev-list", "HEAD")
	last := strings.Fields(head)[:2] // newest first
	if err := r.RevertMany(last, ReplayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("x.txt after revert: %v", err)
	}
	if got := gitOut(t, dir, "log", "-2", "--format=%s"); !strings.HasPrefix(got, `Revert "add x"`) {
		t.Fatalf("reverts %q", got)
	}
	if err := r.CherryPickMany([]string{picks[0], picks[1]}, ReplayOptions{Mainline: 2}); err == nil || !strings.Contains(err.Error(), "no parent 2") {
		t.Fatalf("parent 2 of commits that are not merges: %v", err)
	}
}

// A merge among several commits is cherry-picked with -m 1, which git
// takes for every commit; no-commit leaves the changes staged.
func TestReplayManyMergeAndNoCommit(t *testing.T) {
	dir, hashes := linearRepo(t, 1)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, dir, "side.txt", "side\n")
	gitRun(t, dir, "add", "side.txt")
	gitRun(t, dir, "commit", "-q", "-m", "side")
	gitRun(t, dir, "checkout", "-q", "-b", "topic", hashes[0])
	writeFile(t, dir, "topic.txt", "topic\n")
	gitRun(t, dir, "add", "topic.txt")
	gitRun(t, dir, "commit", "-q", "-m", "topic")
	gitRun(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "after.txt", "after\n")
	gitRun(t, dir, "add", "after.txt")
	gitRun(t, dir, "commit", "-q", "-m", "after")
	after := gitOut(t, dir, "rev-parse", "HEAD")

	gitRun(t, dir, "checkout", "-q", "-b", "dest", hashes[0])
	if err := r.CherryPickMany([]string{merge, after}, ReplayOptions{}); err == nil {
		t.Fatal("a merge was picked without a parent to skip")
	}
	gitRun(t, dir, "cherry-pick", "--quit")
	gitRun(t, dir, "reset", "-q", "--hard")
	if err := r.CherryPickMany([]string{merge, after}, ReplayOptions{Mainline: 1, RecordOrigin: true}); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s", hashes[0]+"..HEAD"); got != "after\nmerge side" {
		t.Fatalf("picked %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "side.txt")); err != nil {
		t.Fatalf("side.txt missing after -m 1: %v", err)
	}

	gitRun(t, dir, "checkout", "-q", "-b", "staged", hashes[0])
	if err := r.CherryPickMany([]string{merge, after}, ReplayOptions{Mainline: 1, NoCommit: true}); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != hashes[0] {
		t.Fatalf("no-commit moved HEAD to %s", got)
	}
	if got := gitOut(t, dir, "diff", "--cached", "--name-only"); got != "after.txt\nside.txt" {
		t.Fatalf("staged %q", got)
	}
}

func TestSquashCommits(t *testing.T) {
	dir, hashes := linearRepo(t, 5)
	r := New(&LocalExecutor{Dir: dir})
	writeFile(t, dir, "f1.txt", "local change\n")

	// c2..c4 become one commit with a new message; c5 is replayed on it.
	msg := "squashed: it's \"quoted\"\n\nbody line $HOME `x`"
	if err := r.SquashCommits([]string{hashes[3], hashes[1], hashes[2]}, msg); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s"); got != "c5\nsquashed: it's \"quoted\"\nc1" {
		t.Fatalf("log %q", got)
	}
	if body := gitOut(t, dir, "log", "-1", "--format=%B", "HEAD~"); body != msg {
		t.Fatalf("message %q", body)
	}
	if files := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD~"); files != "f2.txt\nf3.txt\nf4.txt" {
		t.Fatalf("files %q", files)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "f1.txt")); string(body) != "local change\n" {
		t.Fatalf("local change lost: %q", body)
	}

	// A fixup of the root and the next keeps the root's message.
	root := gitOut(t, dir, "rev-list", "--max-parents=0", "HEAD")
	next := gitOut(t, dir, "rev-parse", "HEAD~")
	if err := r.SquashCommits([]string{root, next}, ""); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s"); got != "c5\nc1" {
		t.Fatalf("after fixup %q", got)
	}

	// Commits with one between them are refused.
	all := strings.Fields(gitOut(t, dir, "rev-list", "HEAD"))
	writeFile(t, dir, "g.txt", "g\n")
	gitRun(t, dir, "add", "g.txt")
	gitRun(t, dir, "commit", "-q", "-m", "g")
	if err := r.SquashCommits([]string{all[1], gitOut(t, dir, "rev-parse", "HEAD")}, "x"); err == nil {
		t.Fatal("a gap was accepted")
	}
}

func TestSquashOverSSH(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, hashes := linearRepo(t, 3)
	r := New(&SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true})
	if err := r.SquashCommits(hashes[1:], "two and three"); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "log", "--format=%s"); got != "two and three\nc1" {
		t.Fatalf("log %q", got)
	}
}

func TestLocalConfigAndSubmoduleURL(t *testing.T) {
	dir, _ := linearRepo(t, 1)
	r := New(&LocalExecutor{Dir: dir})
	if v, err := r.LocalConfig(PushCustomRefspecKey); err != nil || v != "" {
		t.Fatalf("unset %q %v", v, err)
	}
	if err := r.SetLocalConfig(PushCustomRefspecKey, "review/main"); err != nil {
		t.Fatal(err)
	}
	if v, err := r.LocalConfig(PushCustomRefspecKey); err != nil || v != "review/main" {
		t.Fatalf("set %q %v", v, err)
	}
	if got := gitOut(t, dir, "config", "--local", "--get", PushCustomRefspecKey); got != "review/main" {
		t.Fatalf("in .git/config %q", got)
	}
	if err := r.SetLocalConfig(PushCustomRefspecKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.SetLocalConfig(PushCustomRefspecKey, ""); err != nil {
		t.Fatalf("unsetting twice: %v", err)
	}

	// A submodule's new URL goes to the parent's .gitmodules and config.
	parent, lib := submoduleParent(t)
	sub := New(&LocalExecutor{Dir: filepath.Join(parent, "vendor", "lib")})
	if p, path, err := sub.Superproject(); err != nil || path != "vendor/lib" || p == "" {
		t.Fatalf("superproject %q %q %v", p, path, err)
	}
	if p, _, err := r.Superproject(); err != nil || p != "" {
		t.Fatalf("not a submodule: %q %v", p, err)
	}
	moved := lib + "-moved"
	if err := sub.EditRemote("origin", "origin", moved); err != nil {
		t.Fatal(err)
	}
	if err := sub.SyncSubmoduleURL(moved); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, parent, "config", "--file", ".gitmodules", "submodule.vendor/lib.url"); got != moved {
		t.Fatalf(".gitmodules url %q", got)
	}
	if got := gitOut(t, parent, "config", "submodule.vendor/lib.url"); got != moved {
		t.Fatalf("parent config url %q", got)
	}
	if err := r.SyncSubmoduleURL("x"); err == nil {
		t.Fatal("a repository that is no submodule synced")
	}
}

// submoduleParent makes a repository with lib checked out at vendor/lib.
func submoduleParent(t *testing.T) (parent, lib string) {
	t.Helper()
	lib = initRepo(t)
	writeFile(t, lib, "lib.txt", "lib\n")
	gitRun(t, lib, "add", "lib.txt")
	gitRun(t, lib, "commit", "-q", "-m", "lib")
	parent = initRepo(t)
	writeFile(t, parent, "a.txt", "a\n")
	gitRun(t, parent, "add", "a.txt")
	gitRun(t, parent, "commit", "-q", "-m", "base")
	gitRun(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "vendor/lib")
	return parent, lib
}

// Squashing commits with a merge among them, or above them, is refused:
// git rebase -i would flatten the merge.
func TestSquashRefusesMerge(t *testing.T) {
	dir, hashes := linearRepo(t, 2)
	r := New(&LocalExecutor{Dir: dir})
	gitRun(t, dir, "checkout", "-q", "-b", "side", hashes[0])
	writeFile(t, dir, "side.txt", "side\n")
	gitRun(t, dir, "add", "side.txt")
	gitRun(t, dir, "commit", "-q", "-m", "side")
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	before := gitOut(t, dir, "rev-parse", "HEAD")
	err := r.SquashCommits([]string{hashes[0], hashes[1]}, "x")
	if !errors.Is(err, ErrSquashMerge) {
		t.Fatalf("squash below a merge: %v", err)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD"); got != before {
		t.Fatal("HEAD moved")
	}
}
