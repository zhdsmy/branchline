package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testRepo makes a repository with two branches merged, a tag, a file
// changed, one staged and one untracked.
func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Ada")
	git("config", "user.email", "ada@example.com")
	write("a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "first")
	git("checkout", "-q", "-b", "feature/x")
	write("b.txt", "bee\n")
	git("add", "b.txt")
	git("commit", "-q", "-m", "feature")
	git("checkout", "-q", "main")
	write("a.txt", "one\ntwo\n")
	git("commit", "-q", "-am", "second")
	git("merge", "-q", "--no-ff", "-m", "merge feature", "feature/x")
	git("tag", "-a", "v1", "-m", "release")
	write("a.txt", "one\ntwo\nthree\n")
	write("staged file.txt", "s\n")
	git("add", "staged file.txt")
	write("new é.txt", "n\n")
	return dir
}

func checkRepo(t *testing.T, r *Repo) {
	t.Helper()
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Head == "" {
		t.Errorf("branch %q head %q", st.Branch, st.Head)
	}
	entries, err := r.Tree(st.Head)
	if err != nil || len(entries) != 2 || entries[0].Path != "a.txt" || entries[1].Path != "b.txt" {
		t.Fatalf("tree: %+v %v", entries, err)
	}
	// The preview comes from the commit, even with different working-tree content.
	data, err := r.Blob(entries[0].Hash)
	if err != nil || string(data) != "one\ntwo\n" {
		t.Fatalf("blob: %q %v", data, err)
	}
	modules, err := r.Submodules()
	if err != nil || len(modules) != 0 {
		t.Fatalf("submodules without .gitmodules: %v %v", modules, err)
	}
	if len(st.Staged) != 1 || st.Staged[0].Path != "staged file.txt" || st.Staged[0].Status != 'A' {
		t.Errorf("staged: %+v", st.Staged)
	}
	var paths []string
	for _, f := range st.Unstaged {
		paths = append(paths, string(f.Status)+" "+f.Path)
	}
	if strings.Join(paths, ",") != "M a.txt,? new é.txt" {
		t.Errorf("unstaged: %v", paths)
	}

	commits, err := r.Log(LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, c := range commits {
		subjects = append(subjects, c.Subject)
	}
	if len(commits) != 4 || commits[0].Subject != "merge feature" || len(commits[0].Parents) != 2 {
		t.Fatalf("log: %v", subjects)
	}
	refs, err := r.Refs()
	if err != nil {
		t.Fatal(err)
	}
	FixRefKinds(commits, refs)
	var decorated []string
	for _, c := range commits {
		for _, ref := range c.Refs {
			decorated = append(decorated, ref.Name)
			if ref.Name == "feature/x" && ref.Kind != RefBranch {
				t.Errorf("feature/x is kind %v", ref.Kind)
			}
		}
	}
	if got := strings.Join(decorated, ","); got != "main,v1,feature/x" {
		t.Errorf("decorations %s", got)
	}
	if len(refs.Branches) != 2 || len(refs.Tags) != 1 || refs.Tags[0].Hash != commits[0].Hash {
		t.Errorf("refs %+v", refs)
	}

	g := Graph(commits)
	if g[0].Lane != 0 || len(g[0].Segments) != 2 {
		t.Errorf("merge row %+v", g[0])
	}
	last := g[len(g)-1]
	if last.Lane != 0 {
		t.Errorf("root row %+v", last)
	}

	d, err := r.Detail(commits[0].Hash)
	if err != nil || d.Message != "merge feature" || len(d.Parents) != 2 {
		t.Errorf("detail %+v %v", d, err)
	}
	patch, err := r.CommitDiff(commits[0])
	if err != nil || !strings.Contains(patch, "+bee") {
		t.Errorf("merge diff %q %v", patch, err)
	}
	for _, f := range append(st.Staged, st.Unstaged...) {
		p, err := r.FileDiff(f)
		if err != nil || !strings.Contains(p, "@@") {
			t.Errorf("diff of %s: %q %v", f.Path, p, err)
		}
	}

	// Stage, commit with a message of several lines, stash and pop.
	if err := r.Stage("a.txt", "new é.txt"); err != nil {
		t.Fatal(err)
	}
	if err := r.Unstage("staged file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("third\n\nwith 'quotes' and $(dollars)", false); err != nil {
		t.Fatal(err)
	}
	if msg, _ := r.LastMessage(); msg != "third\n\nwith 'quotes' and $(dollars)" {
		t.Errorf("message %q", msg)
	}
	if err := r.StashPush("wip"); err != nil {
		t.Fatal(err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "wip") {
		t.Fatalf("stashes %+v %v", stashes, err)
	}
	if err := r.StashApply(stashes[0].Name, true); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch("topic", "HEAD~1", false); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteBranch("topic", true); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Status()
	if len(st.Unstaged) != 1 || st.Unstaged[0].Path != "staged file.txt" || st.State != "" {
		t.Errorf("after pop: %+v", st)
	}
	if err := r.Discard(st.Unstaged...); err != nil {
		t.Fatal(err)
	}
	if st, _ = r.Status(); len(st.Unstaged)+len(st.Staged) != 0 {
		t.Errorf("after discard: %+v", st)
	}
}

func TestLocal(t *testing.T) {
	checkRepo(t, New(&LocalExecutor{Dir: testRepo(t)}))
}

// fakeSSH is an ssh that runs the remote command here, with sh, as a
// server would.
const fakeSSH = `#!/bin/sh
while [ $# -gt 1 ]; do
  case "$1" in
    -o|-p|-i|-l) shift 2 ;;
    *) break ;;
  esac
done
host=$1; shift
[ "$host" = "devbox" ] || { echo "bad host $host" >&2; exit 255; }
exec sh -c "$1"
`

func TestSSH(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory with a space and a quote, as the server's shell must
	// read it whole.
	parent := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "repo")
	if err := os.Rename(testRepo(t), dir); err != nil {
		t.Fatal(err)
	}
	ex := &SSHExecutor{Host: "devbox", User: "ada", Port: 2222, Dir: dir, SSH: bin, NoMultiplex: true}
	r := New(ex)
	top, err := r.TopLevel()
	if err != nil {
		t.Fatal(err)
	}
	if real, _ := filepath.EvalSymlinks(dir); top != real && top != dir {
		t.Errorf("top level %q, want %q", top, dir)
	}
	checkRepo(t, r)

	bad := New(&SSHExecutor{Host: "elsewhere", Dir: dir, SSH: bin})
	if _, err := bad.TopLevel(); err == nil || !strings.Contains(err.Error(), "cannot connect to elsewhere") {
		t.Errorf("unreachable host: %v", err)
	}
}

func TestSSHArgs(t *testing.T) {
	ex := &SSHExecutor{Host: "example.com", User: "git", Port: 2200, Identity: "/k/id", Dir: "~/src/my app"}
	remote := ex.RemoteCommand("log", "--format=%H %s", "it's")
	want := `cd ~/'src/my app' && GIT_TERMINAL_PROMPT=0 GIT_OPTIONAL_LOCKS=0 LC_ALL=C git --literal-pathspecs -c core.quotepath=false -c color.ui=false -c log.showSignature=false -c core.pager=cat -c core.fsmonitor=false -c protocol.ext.allow=never log '--format=%H %s' 'it'\''s'`
	if remote != want {
		t.Errorf("remote command\n got %s\nwant %s", remote, want)
	}
	ex.Trace = AllTraces()
	traced := ex.RemoteCommand("status")
	for _, name := range []string{"GIT_TRACE=1", "GIT_TRACE_CURL=1", "GIT_SSH_COMMAND='ssh -vvv'", "GIT_TRACE_PACKFILE=1", "GIT_TRACE_PERFORMANCE=1"} {
		if !strings.Contains(traced, name) {
			t.Errorf("verbose remote lacks %s: %s", name, traced)
		}
	}
	if strings.Contains(remote, "GIT_TRACE=") {
		t.Errorf("a quiet command was traced: %s", remote)
	}
	args := strings.Join(ex.SSHArgs("X"), " ")
	for _, s := range []string{"BatchMode=yes", "-p 2200", "-i /k/id", "-l git", "example.com X", "ControlPath=/tmp/branchline-"} {
		if !strings.Contains(args, s) {
			t.Errorf("ssh args %q lack %q", args, s)
		}
	}
	for _, bad := range []SSHExecutor{{Host: "-oProxyCommand=x", Dir: "/r"}, {Host: "a b", Dir: "/r"}, {Host: "h"}} {
		if bad.Validate() == nil {
			t.Errorf("%+v is valid", bad)
		}
	}
	if _, err := (&SSHExecutor{Host: "-oProxyCommand=touch /tmp/pwned", Dir: "/"}).Run(context.Background(), nil, "status"); err == nil {
		t.Error("a host starting with - ran")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"": "''", "main": "main", "a b": "'a b'", "it's": `'it'\''s'`,
		"$(rm -rf /)": "'$(rm -rf /)'", "`x`": "'`x`'", "refs/heads/a:refs/heads/a": "refs/heads/a:refs/heads/a",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("sh read %s as %q (%v)", ShellQuote(in), out, err)
		}
	}
	if got := RemotePath("~/a b"); got != "~/'a b'" {
		t.Errorf("RemotePath = %s", got)
	}
}

func TestGraphBranches(t *testing.T) {
	// d merges c into b; b and c both come from a.
	commits := []Commit{
		{Hash: "d", Parents: []string{"b", "c"}},
		{Hash: "c", Parents: []string{"a"}},
		{Hash: "b", Parents: []string{"a"}},
		{Hash: "a"},
	}
	g := Graph(commits)
	lanes := []int{g[0].Lane, g[1].Lane, g[2].Lane, g[3].Lane}
	if lanes[0] != 0 || lanes[1] != 1 || lanes[2] != 0 || lanes[3] != 0 {
		t.Errorf("lanes %v", lanes)
	}
	// c's lane goes on past b, then joins a.
	var past, join bool
	for _, s := range g[2].Segments {
		past = past || s.Kind == SegmentFull && s.From == 1
	}
	for _, s := range g[3].Segments {
		join = join || s.Kind == SegmentTop && s.From == 1 && s.To == 0
	}
	if !past || !join {
		t.Errorf("segments %+v / %+v", g[2].Segments, g[3].Segments)
	}
}

func TestParseNameStatus(t *testing.T) {
	out := []byte("M\x00a.go\x00R100\x00old.go\x00new.go\x00A\x00dir/b.txt\x00")
	got := ParseNameStatus(out)
	want := []FileChange{
		{Status: 'M', Path: "a.go"},
		{Status: 'R', Path: "new.go", OrigPath: "old.go"},
		{Status: 'A', Path: "dir/b.txt"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRangeCompare(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	commits, err := r.Log(LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Commit{}
	for _, c := range commits {
		by[c.Subject] = c
	}
	root, second, feature, merge := by["first"], by["second"], by["feature"], by["merge feature"]

	files, err := r.RangeFiles(root.Hash, second.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "a.txt" || files[0].Status != 'M' {
		t.Fatalf("adjacent: %+v", files)
	}
	patch, err := r.RangeDiff(root.Hash, second.Hash)
	if err != nil || !strings.Contains(patch, "+two") {
		t.Fatalf("adjacent patch %q %v", patch, err)
	}

	files, err = r.RangeFiles(root.Hash, feature.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "b.txt" || files[0].Status != 'A' {
		t.Fatalf("other branch: %+v", files)
	}

	files, err = r.RangeFiles(root.Hash, merge.Hash)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]byte{}
	for _, f := range files {
		got[f.Path] = f.Status
	}
	if got["a.txt"] != 'M' || got["b.txt"] != 'A' {
		t.Fatalf("across merge: %+v", files)
	}
	patch, err = r.RangeFileDiff(root.Hash, merge.Hash, FileChange{Path: "b.txt", Status: 'A'})
	if err != nil || !strings.Contains(patch, "+bee") {
		t.Fatalf("file patch %q %v", patch, err)
	}

	rootFiles, err := r.RangeFiles("", root.Hash)
	if err != nil || len(rootFiles) != 1 || rootFiles[0].Path != "a.txt" || rootFiles[0].Status != 'A' {
		t.Fatalf("empty base: %+v %v", rootFiles, err)
	}
}

func TestLogQueryAndPaging(t *testing.T) {
	r := New(&LocalExecutor{Dir: testRepo(t)})
	all, err := r.Log(LogOptions{Limit: 10})
	if err != nil || len(all) < 4 {
		t.Fatal(err, len(all))
	}
	page, err := r.Log(LogOptions{Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("page %+v %v", subjects(page), err)
	}
	next, err := r.Log(LogOptions{Limit: 2, Skip: 2})
	if err != nil || len(next) != 2 || next[0].Hash == page[0].Hash {
		t.Fatalf("next %+v", subjects(next))
	}
	found, err := r.Log(LogOptions{Query: "bee", Limit: 10})
	if err != nil || len(found) != 0 && found[0].Subject == "" {
		t.Fatal(err)
	}
	// The feature commit's message does not contain bee, but its patch does not either in the subject.
	// Search the author, which every commit has, and a subject word.
	byAuthor, err := r.Log(LogOptions{Query: "Ada", Limit: 10})
	if err != nil || len(byAuthor) != len(all) {
		t.Fatalf("author %v want %d: %+v", err, len(all), subjects(byAuthor))
	}
	byMsg, err := r.Log(LogOptions{Query: "feature", Limit: 10})
	if err != nil || len(byMsg) == 0 {
		t.Fatalf("message %v %+v", err, subjects(byMsg))
	}
	first, err := r.Log(LogOptions{FirstParent: true, Limit: 10, Revs: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range first {
		if c.Subject == "feature" {
			t.Fatalf("first parent included the side branch: %+v", subjects(first))
		}
	}
}

func subjects(cs []Commit) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Subject
	}
	return out
}

func TestIgnoreSpace(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q", "-b", "main")
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))
	git("add", "a.txt")
	git("commit", "-q", "-m", "one")
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one \n"), 0o644))
	git("commit", "-q", "-am", "space")
	r := New(&LocalExecutor{Dir: dir})
	cs, err := r.Log(LogOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := r.RangeDiff(cs[1].Hash, cs[0].Hash)
	if err != nil || !strings.Contains(plain, "+") {
		t.Fatalf("plain %q %v", plain, err)
	}
	ignored, err := r.RangeDiffOpts(cs[1].Hash, cs[0].Hash, DiffOptions{IgnoreSpace: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ignored, "+one") {
		t.Fatalf("whitespace change remained: %q", ignored)
	}
}

func TestRebasePlan(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	write("a.txt", "one\n")
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	write("a.txt", "one\ntwo\n")
	gitcmd("commit", "-q", "-am", "second")
	write("b.txt", "bee\n")
	gitcmd("add", "b.txt")
	gitcmd("commit", "-q", "-m", "third")

	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan("HEAD~2")
	if err != nil || len(plan) != 2 || plan[0].Subject != "second" || plan[1].Subject != "third" {
		t.Fatalf("plan %+v err %v", plan, err)
	}
	plan[1].Action = "drop"
	if err := r.RebaseWith("HEAD~2", plan); err != nil {
		t.Fatal(err)
	}
	log, err := r.Log(LogOptions{Limit: 10, Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Subject != "second" || log[1].Subject != "base" {
		t.Fatalf("after drop %+v", subjects(log))
	}

	plan, err = r.RebasePlan("HEAD~1")
	if err != nil || len(plan) != 1 {
		t.Fatal(err, plan)
	}
	plan[0].Action, plan[0].Subject = "reword", "renamed"
	if err := r.RebaseWith("HEAD~1", plan); err != nil {
		t.Fatal(err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || log[0].Subject != "renamed" {
		t.Fatalf("reword %+v %v", subjects(log), err)
	}
}

func TestRebaseStopAndAbort(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	write("a.txt", "one\n")
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	write("a.txt", "one\ntwo\n")
	gitcmd("commit", "-q", "-am", "second")
	write("c.txt", "ours\n")
	gitcmd("add", "c.txt")
	gitcmd("commit", "-q", "-m", "third")

	r := New(&LocalExecutor{Dir: dir})
	plan, err := r.RebasePlan("HEAD~1")
	if err != nil || len(plan) != 1 {
		t.Fatal(err, plan)
	}
	plan[0].Action = "edit"
	if err := r.RebaseWith("HEAD~1", plan); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.State != "rebase" || st.Stopped != "edit" {
		t.Fatalf("edit stop %+v %v", st, err)
	}
	if err := r.Abort(st.State); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.State != "" || st.Branch != "main" {
		t.Fatalf("after abort %+v %v", st, err)
	}

	gitcmd("checkout", "-q", "-b", "feature", "HEAD~1")
	write("c.txt", "theirs\n")
	gitcmd("add", "c.txt")
	gitcmd("commit", "-q", "-m", "feature")
	plan, err = r.RebasePlan("main")
	if err != nil || len(plan) != 1 || plan[0].Subject != "feature" {
		t.Fatal(err, plan)
	}
	if err := r.RebaseWith("main", plan); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.State != "rebase" || st.Stopped != "conflict" || !st.Conflicted() {
		t.Fatalf("conflict %+v %v", st, err)
	}
	if err := r.TakeConflictSide("c.txt", "theirs"); err != nil {
		t.Fatal(err)
	}
	if err := r.Continue("rebase"); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.State != "" || st.Branch != "feature" {
		t.Fatalf("continued %+v %v", st, err)
	}
	log, err := r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || log[0].Subject != "feature" {
		t.Fatalf("log %+v %v", subjects(log), err)
	}
}

func TestConflictResolve(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	write("a.txt", "base\n")
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	gitcmd("branch", "other")
	write("a.txt", "ours\n")
	gitcmd("commit", "-q", "-am", "ours")
	gitcmd("checkout", "-q", "other")
	write("a.txt", "theirs\n")
	gitcmd("commit", "-q", "-am", "theirs")
	gitcmd("checkout", "-q", "main")
	cmd := exec.Command("git", "-C", dir, "merge", "other")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	_, _ = cmd.CombinedOutput()

	r := New(&LocalExecutor{Dir: dir})
	st, err := r.Status()
	if err != nil || !st.Conflicted() {
		t.Fatalf("status %+v err %v", st, err)
	}
	sides, err := r.ConflictSides("a.txt")
	if err != nil || sides.Ours != "ours\n" || sides.Theirs != "theirs\n" {
		t.Fatalf("sides %+v %v", sides, err)
	}
	body, err := r.ReadWorktree("a.txt")
	if err != nil || !strings.Contains(body, "<<<<<<<") {
		t.Fatalf("markers %q %v", body, err)
	}
	if err := r.WriteWorktree("a.txt", "resolved\n"); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil || st.Conflicted() {
		t.Fatalf("still conflicted %+v %v", st, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "resolved\n" {
		t.Fatalf("written %q %v", got, err)
	}

	gitcmd("reset", "--hard", "HEAD")
	cmd = exec.Command("git", "-C", dir, "merge", "other")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
	_, _ = cmd.CombinedOutput()
	if err := r.TakeConflictSide("a.txt", "theirs"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "theirs\n" {
		t.Fatalf("theirs %q %v", got, err)
	}
	st, err = r.Status()
	if err != nil || st.Conflicted() || len(st.Staged) != 1 {
		t.Fatalf("after take %+v %v", st, err)
	}
}

func TestFileStats(t *testing.T) {
	raw := []byte("\x1eabc\x1fAda\x1fada@example.com\x1f1700000000\x1fmove files\n1\t0\told.go => dir/new.go\n0\t0\tdir/sub/{old.go => new.go}\n-\t-\tlogo.png\n")
	commits, stats := ParseFileStats(raw)
	if len(commits) != 1 || commits[0].Hash != "abc" || commits[0].Subject != "move files" || commits[0].Date.Unix() != 1700000000 {
		t.Fatalf("commit %+v", commits)
	}
	want := []FileStat{
		{Path: "dir/new.go", Added: 1},
		{Path: "dir/sub/new.go"},
		{Path: "logo.png"},
	}
	if len(stats) != 1 || len(stats[0]) != len(want) {
		t.Fatalf("stats %+v", stats)
	}
	for i := range want {
		if stats[0][i] != want[i] {
			t.Errorf("file %d: got %+v, want %+v", i, stats[0][i], want[i])
		}
	}

	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	got, files, err := r.CommitFileStats()
	if err != nil || len(got) < 4 || len(files) != len(got) {
		t.Fatal(err, len(got), len(files))
	}
	seen := map[string]bool{}
	for i, c := range got {
		if c.Subject == "add the bee" {
			if len(files[i]) != 1 || files[i][0].Path != "b.txt" || files[i][0].Added == 0 {
				t.Fatalf("bee files %+v", files[i])
			}
		}
		for _, f := range files[i] {
			seen[f.Path] = true
		}
	}
	if !seen["a.txt"] || !seen["b.txt"] {
		t.Fatalf("paths %v", seen)
	}
}

func TestIgnoredStatusAndBisect(t *testing.T) {
	dir := testRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("skip.me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.me"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Ignored) != 0 {
		t.Fatalf("ignored without asking: %+v", st.Ignored)
	}
	st, err = r.StatusOpts(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Ignored) != 1 || st.Ignored[0].Path != "skip.me" || st.Ignored[0].Status != '!' {
		t.Fatalf("ignored %+v", st.Ignored)
	}
	if err := r.Discard(st.Ignored...); err != nil {
		t.Fatal(err)
	}
	st, err = r.StatusOpts(true)
	if err != nil || len(st.Ignored) != 0 {
		t.Fatalf("after delete %+v %v", st.Ignored, err)
	}

	progress, ok, err := r.BisectProgress()
	if err != nil || ok {
		t.Fatalf("idle bisect %+v %v %v", progress, ok, err)
	}
	if err := r.Commit("keep the working tree clean", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bisect("start", "main", "HEAD~2"); err != nil {
		t.Fatal(err)
	}
	progress, ok, err = r.BisectProgress()
	if err != nil || !ok || progress.Head == "" || progress.Started != "main" || progress.Left < 1 {
		t.Fatalf("bisect %+v %v %v", progress, ok, err)
	}
	if _, err := r.Bisect("reset"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = r.BisectProgress(); err != nil || ok {
		t.Fatalf("after reset ok %v err %v", ok, err)
	}

	bench, err := r.Benchmark()
	if err != nil || len(bench.Samples) != 3 || bench.Samples[0].Name != "Reading changed files" || bench.Samples[0].MS < 0 || bench.Score < 0 || bench.Score > 100 {
		t.Fatalf("benchmark %+v %v", bench, err)
	}
}

// resolveTestGoBuildCache picks the Go build cache for helper binaries the
// tests compile. Reusing the process or toolchain cache keeps the builds as
// fast as any other go build; only a missing or disabled cache falls back to
// a fresh temporary directory.
func resolveTestGoBuildCache(envVal, goEnvVal string, fallback func() string) string {
	if v := strings.TrimSpace(envVal); v != "" && v != "off" {
		return v
	}
	if v := strings.TrimSpace(goEnvVal); v != "" && v != "off" {
		return v
	}
	return fallback()
}

// buildTestLFSHelper compiles program into a git-lfs stand-in inside binDir.
// Compilation reuses the existing Go build cache and never reaches the
// network.
func buildTestLFSHelper(t *testing.T, binDir, program string) {
	t.Helper()
	helper := filepath.Join(binDir, "git-lfs")
	source := filepath.Join(binDir, "lfshelper.go")
	if err := os.WriteFile(source, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := resolveTestGoBuildCache(os.Getenv("GOCACHE"), goEnvGOCACHE(t), t.TempDir)
	build := exec.Command("go", "build", "-o", helper, source)
	build.Env = append(os.Environ(), "GOCACHE="+cache, "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
}

func goEnvGOCACHE(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestResolveTestGoBuildCache(t *testing.T) {
	fallbackDir := t.TempDir()
	calls := 0
	fallback := func() string {
		calls++
		return fallbackDir
	}

	if got := resolveTestGoBuildCache("/env/cache", "/goenv/cache", fallback); got != "/env/cache" {
		t.Fatalf("env cache: got %q, want /env/cache", got)
	}
	if got := resolveTestGoBuildCache("  /env/cache  ", "", fallback); got != "/env/cache" {
		t.Fatalf("padded env cache: got %q, want /env/cache", got)
	}
	if calls != 0 {
		t.Fatalf("fallback called %d times with a valid env cache, want 0", calls)
	}

	if got := resolveTestGoBuildCache("", "/goenv/cache", fallback); got != "/goenv/cache" {
		t.Fatalf("empty env cache: got %q, want /goenv/cache", got)
	}
	if got := resolveTestGoBuildCache("off", "/goenv/cache", fallback); got != "/goenv/cache" {
		t.Fatalf("off env cache: got %q, want /goenv/cache", got)
	}
	if calls != 0 {
		t.Fatalf("fallback called %d times with a valid go env cache, want 0", calls)
	}

	for i, pair := range [][2]string{{"", ""}, {"off", "off"}, {"", "off"}, {"off", ""}, {"  ", "  "}} {
		before := calls
		if got := resolveTestGoBuildCache(pair[0], pair[1], fallback); got != fallbackDir {
			t.Fatalf("case %d (%q, %q): got %q, want fallback %q", i, pair[0], pair[1], got, fallbackDir)
		}
		if calls != before+1 {
			t.Fatalf("case %d (%q, %q): fallback called %d times, want exactly 1", i, pair[0], pair[1], calls-before)
		}
	}
}

func TestLFSTrack(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	program := `package main
import ("os"; "strings")
func main() {
  if err := os.WriteFile(os.Getenv("LFS_ARGS"), []byte(strings.Join(os.Args[1:], "\x00")), 0o644); err != nil { os.Exit(1) }
  f, err := os.OpenFile(os.Getenv("LFS_ATTR"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
  if err != nil { os.Exit(1) }
  defer f.Close()
  if _, err = f.WriteString("*.bin filter=lfs diff=lfs merge=lfs -text\n"); err != nil { os.Exit(1) }
}
`
	buildTestLFSHelper(t, bin, program)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LFS_ARGS", filepath.Join(dir, "lfs-args"))
	t.Setenv("LFS_ATTR", filepath.Join(dir, ".gitattributes"))
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.LFSTrack(""); err == nil {
		t.Fatal("an empty pattern was tracked")
	}
	if err := r.LFSTrack("*.bin"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "lfs-args"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 3 || got[0] != "track" || got[1] != "--" || got[2] != "*.bin" {
		t.Fatalf("args %#v", got)
	}
	attr, err := os.ReadFile(filepath.Join(dir, ".gitattributes"))
	if err != nil || !strings.Contains(string(attr), "*.bin filter=lfs") {
		t.Fatalf("attributes %q %v", attr, err)
	}
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	var staged bool
	for _, f := range st.Staged {
		if f.Path == ".gitattributes" {
			staged = true
		}
	}
	if !staged {
		t.Fatalf("attributes were not staged: %+v", st)
	}
}

func TestLFSFetch(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	program := `package main
import ("os"; "strings")
func main() {
  if err := os.WriteFile(os.Getenv("LFS_ARGS"), []byte(strings.Join(os.Args[1:], "\x00")), 0o644); err != nil { os.Exit(1) }
}
`
	buildTestLFSHelper(t, bin, program)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	argsFile := filepath.Join(dir, "lfs-args")
	t.Setenv("LFS_ARGS", argsFile)
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.LFSFetch("origin"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 3 || got[0] != "fetch" || got[1] != "--all" || got[2] != "origin" {
		t.Fatalf("remote args %#v", got)
	}
	if err := os.Remove(argsFile); err != nil {
		t.Fatal(err)
	}
	if err := r.LFSFetch("  "); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 2 || got[0] != "fetch" || got[1] != "--all" {
		t.Fatalf("default args %#v", got)
	}
}

func TestLFSPull(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	program := `package main
import ("os"; "strings")
func main() {
  if err := os.WriteFile(os.Getenv("LFS_ARGS"), []byte(strings.Join(os.Args[1:], "\x00")), 0o644); err != nil { os.Exit(1) }
}
`
	buildTestLFSHelper(t, bin, program)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	argsFile := filepath.Join(dir, "lfs-args")
	t.Setenv("LFS_ARGS", argsFile)
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.LFSPull("upstream"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 2 || got[0] != "pull" || got[1] != "upstream" {
		t.Fatalf("remote args %#v", got)
	}
	if err := os.Remove(argsFile); err != nil {
		t.Fatal(err)
	}
	if err := r.LFSPull(""); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 1 || got[0] != "pull" {
		t.Fatalf("default args %#v", got)
	}
}

func TestLFSLock(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	program := `package main
import ("os"; "strings")
func main() {
  if err := os.WriteFile(os.Getenv("LFS_ARGS"), []byte(strings.Join(os.Args[1:], "\x00")), 0o644); err != nil { os.Exit(1) }
}
`
	buildTestLFSHelper(t, bin, program)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	argsFile := filepath.Join(dir, "lfs-args")
	t.Setenv("LFS_ARGS", argsFile)
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.LFSLock("", false, false); err == nil {
		t.Fatal("an empty path was locked")
	}
	if err := r.LFSLock("assets/a.bin", false, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 3 || got[0] != "lock" || got[1] != "--" || got[2] != "assets/a.bin" {
		t.Fatalf("lock args %#v", got)
	}
	if err := os.Remove(argsFile); err != nil {
		t.Fatal(err)
	}
	if err := r.LFSLock("assets/a.bin", true, true); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(string(raw), "\x00"); len(got) != 4 || got[0] != "unlock" || got[1] != "--force" || got[2] != "--" || got[3] != "assets/a.bin" {
		t.Fatalf("unlock args %#v", got)
	}
}

func TestLFSAndBisectParsers(t *testing.T) {
	if got := ParseLFSFiles([]byte("4d7a214614 * dir/real image.png\n0123456789 - gone.bin\nnoise here\n")); len(got) != 2 || got[0].Path != "dir/real image.png" || got[0].ID != "4d7a214614" || !got[0].Present || got[1].Present || got[1].Path != "gone.bin" {
		t.Fatalf("real git lfs output %+v", got)
	}
	files := ParseLFSFiles([]byte("* abcdef image.png\n- 123456 missing.bin\nnoise\n"))
	if len(files) != 2 || !files[0].Present || files[0].Path != "image.png" || files[1].Present || files[1].ID != "123456" {
		t.Fatalf("files %+v", files)
	}
	files = applyLFSLocks(files, []byte(`[{"path":"image.png","owner":{"name":"Ada"}},{"path":"other.dat","owner":{"name":"Bea"}}]`))
	if files[0].Owner != "Ada" || len(files) != 3 || files[2].Path != "other.dat" || files[2].Owner != "Bea" {
		t.Fatalf("locks %+v", files)
	}
	progress := ParseBisectVars([]byte("bisect_rev='abc'\nbisect_nr=2\nbisect_good=3\nbisect_bad=1\nbisect_steps=1\n"))
	if progress.Left != 2 || progress.Good != 3 || progress.Bad != 1 || progress.Steps != 1 {
		t.Fatalf("progress %+v", progress)
	}
}

func TestFlowDeinit(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if err := r.FlowInit(FlowConfig{Main: "main", Develop: "develop", Feature: "feature/", Tag: "v"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.FlowConfig()
	if err != nil || got.Develop != "develop" || got.Tag != "v" {
		t.Fatalf("config %+v %v", got, err)
	}
	if err := r.FlowDeinit(); err != nil {
		t.Fatal(err)
	}
	got, err = r.FlowConfig()
	if err != nil || got.Develop != "" || got.Feature != "" || got.Tag != "" {
		t.Fatalf("after deinit %+v %v", got, err)
	}
}

func TestWorktrees(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")

	r := New(&LocalExecutor{Dir: dir})
	list, err := r.Worktrees()
	if err != nil || len(list) != 1 || !list[0].Current || list[0].Branch != "main" {
		t.Fatalf("main checkout %+v %v", list, err)
	}
	other := t.TempDir()
	if err := r.WorktreeAdd(other, "feature"); err != nil {
		t.Fatal(err)
	}
	list, err = r.Worktrees()
	if err != nil || len(list) != 2 {
		t.Fatalf("added %+v %v", list, err)
	}
	var found Worktree
	for _, w := range list {
		if w.Branch == "feature" {
			found = w
		}
	}
	if found.Path == "" || found.Current || found.Head == "" {
		t.Fatalf("feature checkout %+v", list)
	}
	linkedList, err := New(&LocalExecutor{Dir: other}).Worktrees()
	if err != nil || len(linkedList) != 2 || linkedList[0].Current || !linkedList[1].Current || linkedList[1].Branch != "feature" {
		t.Fatalf("linked current worktree %+v %v", linkedList, err)
	}
	if cmd := (&SSHExecutor{Host: "h", Dir: "~/repo"}).RemoteCommand("worktree", "add", "-b", "feature", "--", "~/repo-feature", "HEAD"); !strings.Contains(cmd, " -- ~/repo-feature HEAD") {
		t.Fatalf("remote worktree command %q", cmd)
	}
	// Local symlink counterexample and controlled TopLevel failure/unmatched tests for SSH:
	// Create a local symlink on this machine pointing to list[0].Path (main worktree).
	// SSHExecutor must never use local EvalSymlinks on that symlink to falsely mark list[0] as Current.
	localSameLink := filepath.Join(t.TempDir(), "same-name-link")
	if err := os.Symlink(list[0].Path, localSameLink); err != nil {
		t.Fatal(err)
	}
	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh")
	porcelainOut := "worktree " + list[0].Path + "\nHEAD 1111111111111111111111111111111111111111\nbranch refs/heads/main\n\nworktree " + list[1].Path + "\nHEAD 2222222222222222222222222222222222222222\nbranch refs/heads/feature\n\n"
	writeFakeSSH := func(topOut, topExit string) {
		script := "#!/bin/sh\nlast=\"\"\nfor a in \"$@\"; do last=\"$a\"; done\ncase \"$last\" in\n  *--porcelain*) printf %s " + ShellQuote(porcelainOut) + " ;;\n  *--show-toplevel*) printf '%s\n' " + ShellQuote(topOut) + "; exit " + topExit + " ;;\n  *) exit 1 ;;\nesac\n"
		if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 1) Controlled TopLevel failure: must return error, never default list[0].Current = true.
	writeFakeSSH("", "128")
	if got, err := New(&SSHExecutor{Host: "h", Dir: localSameLink, SSH: fakeSSH, NoMultiplex: true}).Worktrees(); err == nil {
		t.Fatalf("TopLevel failure returned worktrees %+v without error", got)
	}
	// 2) Controlled TopLevel unmatched (returns localSameLink, which on this Mac is a symlink to list[0].Path):
	// SSH must not run local EvalSymlinks and must return an unmatched error instead of marking list[0].Current.
	writeFakeSSH(localSameLink, "0")
	if got, err := New(&SSHExecutor{Host: "h", Dir: localSameLink, SSH: fakeSSH, NoMultiplex: true}).Worktrees(); err == nil {
		t.Fatalf("unmatched remote TopLevel with local symlink matched %+v", got)
	}
	// 3) Remote TopLevel resolves to list[1].Path (linked checkout) while localSameLink points to list[0].Path locally:
	// SSH must mark list[1].Current = true and list[0].Current = false.
	writeFakeSSH(list[1].Path, "0")
	sshLinked, err := New(&SSHExecutor{Host: "h", Dir: localSameLink, SSH: fakeSSH, NoMultiplex: true}).Worktrees()
	if err != nil || len(sshLinked) != 2 || sshLinked[0].Current || !sshLinked[1].Current {
		t.Fatalf("SSH linked worktree with local symlink counterexample: %+v (%v)", sshLinked, err)
	}

	if err := r.WorktreeRemove(list[0].Path, false); err == nil {
		t.Fatal("removed the checkout the commands run in")
	}
	if err := r.WorktreeRemove(found.Path, false); err != nil {
		t.Fatal(err)
	}
	list, err = r.Worktrees()
	if err != nil || len(list) != 1 || list[0].Branch != "main" {
		t.Fatalf("after remove %+v %v", list, err)
	}
}

func TestStashStageNewFiles(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("skip/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt", ".gitignore")
	gitcmd("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "skip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip", "secret.txt"), []byte("no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	if err := r.StashPushWith(StashOptions{Message: "with new", StageNew: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\n" {
		t.Fatalf("tracked file %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skip", "secret.txt")); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || len(st.Staged)+len(st.Unstaged) != 0 {
		t.Fatalf("index after stash %+v %v", st, err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 1 {
		t.Fatalf("stashes %+v %v", stashes, err)
	}
	if err := r.StashApply(stashes[0].Name, true); err != nil {
		t.Fatal(err)
	}
	st, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	var stagedNew, unstagedTracked bool
	for _, f := range st.Staged {
		if f.Path == "new.txt" && f.Status == 'A' {
			stagedNew = true
		}
	}
	for _, f := range st.Unstaged {
		if f.Path == "a.txt" && f.Status == 'M' {
			unstagedTracked = true
		}
		if f.Path == "new.txt" {
			t.Fatalf("new file came back untracked: %+v", st)
		}
	}
	if !stagedNew || !unstagedTracked {
		t.Fatalf("restored %+v", st)
	}
}

func TestStashOptionsAndRename(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	if err := os.WriteFile(filepath.Join(dir, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	if err := r.StashPushWith(StashOptions{Message: "index only", Staged: true, Untracked: true}); err == nil {
		t.Fatal("staged and untracked were both accepted")
	}
	if err := r.StashPushWith(StashOptions{Message: "index only", Staged: true}); err != nil {
		t.Fatal(err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "index only") {
		t.Fatalf("stash %+v %v", stashes, err)
	}
	if err := r.StashRename(stashes[0].Name, "kept index"); err != nil {
		t.Fatal(err)
	}
	stashes, err = r.Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "kept index") {
		t.Fatalf("renamed %+v %v", stashes, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\n" {
		t.Fatalf("working tree %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "later.txt")); err != nil {
		t.Fatal(err)
	}
	if err := r.StashRename("", "x"); err == nil {
		t.Fatal("empty stash rename was accepted")
	}
}

func TestSubmoduleAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	lib := t.TempDir()
	gitcmd := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd(lib, "init", "-q", "-b", "main")
	gitcmd(lib, "config", "user.name", "Ada")
	gitcmd(lib, "config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(lib, "lib.txt"), []byte("lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd(lib, "add", "lib.txt")
	gitcmd(lib, "commit", "-q", "-m", "lib")
	gitcmd(dir, "init", "-q", "-b", "main")
	gitcmd(dir, "config", "user.name", "Ada")
	gitcmd(dir, "config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd(dir, "add", "a.txt")
	gitcmd(dir, "commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.SubmoduleAdd(lib, "vendor/lib"); err != nil {
		t.Fatal(err)
	}
	paths, err := r.Submodules()
	if err != nil || len(paths) != 1 || paths[0] != "vendor/lib" {
		t.Fatalf("added %+v %v", paths, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "vendor", "lib", "lib.txt"))
	if err != nil || string(body) != "lib\n" {
		t.Fatalf("cloned %q %v", body, err)
	}
	if err := r.SubmoduleDeinit("vendor/lib", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "lib", "lib.txt")); !os.IsNotExist(err) {
		t.Fatalf("submodule files stayed: %v", err)
	}
	if err := r.SubmoduleAdd("", "x"); err == nil {
		t.Fatal("empty submodule was accepted")
	}
}

func TestSubmoduleSync(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"lib\"]\n\tpath = lib\n\turl = ../example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", ".gitmodules")
	gitcmd("update-index", "--add", "--cacheinfo", "160000,4b825dc642cb6eb9a060e54bf8d69288fbee4904,lib")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.SubmoduleInit("lib"); err != nil {
		t.Fatal(err)
	}
	got, err := r.run(quick, "config", "--get", "submodule.lib.url")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(got)), "/example") {
		t.Fatalf("url %q %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"lib\"]\n\tpath = lib\n\turl = ../changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SubmoduleSync("lib"); err != nil {
		t.Fatal(err)
	}
	got, err = r.run(quick, "config", "--get", "submodule.lib.url")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(got)), "/changed") {
		t.Fatalf("synced %q %v", got, err)
	}
}

func TestRewordMessage(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("commit", "-q", "-am", "second")
	r := New(&LocalExecutor{Dir: dir})
	log, err := r.Log(LogOptions{Limit: 2, Revs: []string{"HEAD"}})
	if err != nil || log[1].Subject != "base" {
		t.Fatal(err, subjects(log))
	}
	msg := "renamed base\n\nThe body stays on its own line."
	plan, err := r.RebasePlan("--root")
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		if plan[i].Hash == log[1].Hash {
			plan[i].Action, plan[i].Subject = "reword", msg
		}
	}
	if err := r.RebaseWith("--root", plan); err != nil {
		t.Fatal(err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	var base Commit
	for _, c := range log {
		if c.Subject == "renamed base" {
			base = c
		}
	}
	if base.Hash == "" {
		t.Fatalf("subjects %+v", subjects(log))
	}
	got, err := r.Message(base.Hash)
	if err != nil || got != msg {
		t.Fatalf("message %q %v", got, err)
	}
	st, err := r.Status()
	if err != nil || st.State != "" {
		t.Fatalf("state %+v %v", st, err)
	}
}

func TestFixupAndDrop(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	write("one\n")
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	write("one\ntwo\n")
	gitcmd("commit", "-q", "-am", "second")
	write("one\ntwo\nthree\n")
	gitcmd("commit", "-q", "-am", "third")
	r := New(&LocalExecutor{Dir: dir})
	log, err := r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || len(log) != 3 {
		t.Fatal(err, subjects(log))
	}
	plan, err := r.RebasePlan("--root")
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		if plan[i].Hash == log[1].Hash {
			plan[i].Action = "fixup"
		}
	}
	if err := r.RebaseWith("--root", plan); err != nil {
		t.Fatal(err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || len(log) != 2 || log[0].Subject != "third" || log[1].Subject != "base" {
		t.Fatalf("after fixup %+v %v", subjects(log), err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\ntwo\nthree\n" {
		t.Fatalf("content %q %v", body, err)
	}
	plan, err = r.RebasePlan(log[0].Hash + "~")
	if err != nil {
		t.Fatal(err)
	}
	plan[0].Action = "drop"
	if err := r.RebaseWith(log[0].Hash+"~", plan); err != nil {
		t.Fatal(err)
	}
	log, err = r.Log(LogOptions{Limit: 5, Revs: []string{"HEAD"}})
	if err != nil || len(log) != 1 || log[0].Subject != "base" {
		t.Fatalf("after drop %+v %v", subjects(log), err)
	}
}

func TestPathModes(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	brew, ok := resolvePath(PathProcessBrew)
	if !ok || !strings.Contains(brew, "/opt/homebrew/bin") || !strings.HasSuffix(brew, "/usr/bin") {
		t.Fatalf("brew path %q ok=%v", brew, ok)
	}
	once := withBrew(withBrew("/usr/bin"))
	if strings.Count(once, "/opt/homebrew/bin") != 1 || strings.Count(once, "/usr/local/bin") != 1 {
		t.Fatalf("brew added twice: %s", once)
	}
	if _, ok := resolvePath(PathProcess); ok {
		t.Fatal("parent mode rewrote PATH")
	}
	env := commandEnv(PathProcess, AllTraces())
	if envValue(env, "PATH") != "/usr/bin" {
		t.Fatalf("parent PATH %q", envValue(env, "PATH"))
	}
	for _, name := range []string{"GIT_TRACE", "GIT_TRACE_CURL", "GIT_TRACE_PACKFILE", "GIT_TRACE_PERFORMANCE"} {
		if envValue(env, name) != "1" {
			t.Fatalf("%s=%q", name, envValue(env, name))
		}
	}
	if envValue(env, "GIT_SSH_COMMAND") != "ssh -vvv" {
		t.Fatalf("ssh command %q", envValue(env, "GIT_SSH_COMMAND"))
	}
	quiet := commandEnv(PathProcess, Trace{})
	if envValue(quiet, "GIT_TRACE") != "" {
		t.Fatal("quiet command was traced")
	}
	if got := withBrew(""); !strings.Contains(got, "/opt/homebrew/bin") || !strings.Contains(got, "/usr/local/bin") {
		t.Fatal(got)
	}
}

func TestDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "trunk")
	gitcmd("config", "user.email", "ada@example.com")
	gitcmd("config", "user.name", "Ada")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "first")
	gitcmd("remote", "add", "origin", dir)
	gitcmd("update-ref", "refs/remotes/origin/trunk", "HEAD")
	gitcmd("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	r := New(&LocalExecutor{Dir: dir})
	name, err := r.DefaultBranch()
	if err != nil || name != "trunk" {
		t.Fatalf("default %q %v", name, err)
	}
	refs, err := r.Refs()
	if err != nil || refs.Default != "trunk" {
		t.Fatalf("refs default %+v %v", refs.Default, err)
	}
}

func TestWorktreeDiffAndRestore(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	patch, err := r.WorktreeDiff(st.Head)
	if err != nil || !strings.Contains(patch, "a.txt") || !strings.Contains(patch, "+three") {
		t.Fatalf("worktree diff %q %v", patch, err)
	}
	if _, err := r.WorktreeDiff(" "); err == nil {
		t.Fatal("an empty revision compared")
	}
	if err := r.RestoreFile(st.Head, "a.txt"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(after) != "one\ntwo\n" {
		t.Fatalf("restored %q %v", after, err)
	}
	st, err = r.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range append(st.Staged, st.Unstaged...) {
		if f.Path == "a.txt" {
			t.Fatalf("a.txt is still changed: %+v", st)
		}
	}
	if err := r.RestoreFile("", "a.txt"); err == nil {
		t.Fatal("missing revision restored a file")
	}
}

func TestWorktreeAddExistingBranch(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	gitcmd("branch", "feature")
	r := New(&LocalExecutor{Dir: dir})
	other := filepath.Join(t.TempDir(), "feature-checkout")
	if err := r.WorktreeAdd(other, "feature"); err != nil {
		t.Fatal(err)
	}
	list, err := r.Worktrees()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range list {
		if w.Branch == "feature" && w.Path != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("existing branch was not checked out: %+v", list)
	}
}

func TestWorktreeMove(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	r := New(&LocalExecutor{Dir: dir})
	other := filepath.Join(t.TempDir(), "feature")
	if err := r.WorktreeAdd(other, "feature"); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(other), "feature-moved")
	if err := r.WorktreeMove(other, moved); err != nil {
		t.Fatal(err)
	}
	list, err := r.Worktrees()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range list {
		if w.Branch == "feature" && samePath(w.Path, moved) {
			found = true
		}
	}
	if !found {
		t.Fatalf("moved worktree missing: %+v", list)
	}
	if err := r.WorktreeMove("", moved); err == nil {
		t.Fatal("an empty path moved a worktree")
	}
}

func samePath(a, b string) bool {
	x, err := filepath.EvalSymlinks(a)
	if err != nil {
		x = a
	}
	y, err := filepath.EvalSymlinks(b)
	if err != nil {
		y = b
	}
	return filepath.Clean(x) == filepath.Clean(y)
}

func TestFormatPatchAndSnapshot(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	commits, err := r.Log(LogOptions{Limit: 1})
	if err != nil || len(commits) != 1 {
		t.Fatal(err)
	}
	patch, err := r.FormatPatch(commits[0].Hash)
	if err != nil || !strings.Contains(patch, "Subject: [PATCH] merge feature") || !strings.Contains(patch, "diff --git") {
		t.Fatalf("patch %q %v", patch, err)
	}
	if _, err := r.FormatPatch(" "); err == nil {
		t.Fatal("empty hash formatted")
	}
	if err := os.WriteFile(filepath.Join(dir, "snap.txt"), []byte("saved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Stage("snap.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveSnapshot("wip", "keep the work"); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.Branch != "main" || len(st.Staged) != 0 {
		t.Fatalf("after snapshot %+v %v", st, err)
	}
	var kept bool
	for _, f := range st.Unstaged {
		if f.Path == "later.txt" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("unstaged file was taken into the snapshot: %+v", st.Unstaged)
	}
	out, err := r.run(quick, "log", "-1", "--format=%s", "wip")
	if err != nil || strings.TrimSpace(string(out)) != "keep the work" {
		t.Fatalf("snapshot commit %q %v", out, err)
	}
	body, err := r.run(quick, "show", "wip:snap.txt")
	if err != nil || string(body) != "saved\n" {
		t.Fatalf("snapshot file %q %v", body, err)
	}
	if err := r.SaveSnapshot("", "x"); err == nil {
		t.Fatal("empty snapshot branch was accepted")
	}
}

func TestDiscardHunkAndRestoreWorktree(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Stage("keep.txt"); err != nil {
		t.Fatal(err)
	}
	commits, err := r.Log(LogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var root string
	for _, c := range commits {
		if c.Subject == "first" {
			root = c.Hash
		}
	}
	if root == "" {
		t.Fatal("root commit missing")
	}
	if err := r.RestoreWorktree(root, "a.txt"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\n" {
		t.Fatalf("restored worktree %q %v", body, err)
	}
	after, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	var staged, unstaged bool
	for _, f := range after.Staged {
		if f.Path == "keep.txt" {
			staged = true
		}
		if f.Path == "a.txt" {
			t.Fatalf("a.txt was staged by the restore: %+v", after.Staged)
		}
	}
	for _, f := range after.Unstaged {
		if f.Path == "a.txt" && f.Status == 'M' {
			unstaged = true
		}
	}
	if !staged || !unstaged {
		t.Fatalf("staged keep %v unstaged a %v status %+v", staged, unstaged, after)
	}

	patch := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+restored\n"
	if err := r.ApplyPatchText(patch); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(filepath.Join(dir, "a.txt"))
	want := "restored\n"
	if err != nil || string(body) != want {
		t.Fatalf("hunk restored %q %v", body, err)
	}
	if err := r.ApplyPatchText(" "); err == nil {
		t.Fatal("an empty patch was applied")
	}
}

func TestFormatRange(t *testing.T) {
	r := New(&LocalExecutor{Dir: testRepo(t)})
	commits, err := r.Log(LogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, c := range commits {
		by[c.Subject] = c.Hash
	}
	patch, err := r.FormatRange(by["first"], by["second"])
	if err != nil || !strings.Contains(patch, "Subject: [PATCH] second") || !strings.Contains(patch, "+two") {
		t.Fatalf("range %q %v", patch, err)
	}
	if strings.Contains(patch, "Subject: [PATCH] first") {
		t.Fatal("the base commit was included")
	}
	if _, err := r.FormatRange("", by["second"]); err == nil {
		t.Fatal("an empty base was formatted")
	}
}

func TestTrackBranchAndPushRefs(t *testing.T) {
	dir := t.TempDir()
	origin := t.TempDir()
	gitcmd := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd(origin, "init", "-q", "-b", "main")
	gitcmd(origin, "config", "user.name", "Ada")
	gitcmd(origin, "config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd(origin, "add", "a.txt")
	gitcmd(origin, "commit", "-q", "-m", "base")
	gitcmd(dir, "clone", "-q", origin, ".")
	gitcmd(dir, "config", "user.name", "Ada")
	gitcmd(dir, "config", "user.email", "ada@example.com")
	gitcmd(dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd(dir, "commit", "-q", "-am", "feature")
	r := New(&LocalExecutor{Dir: dir})
	if err := r.TrackBranch("feature", "origin/main"); err != nil {
		t.Fatal(err)
	}
	upstream, err := r.run(quick, "rev-parse", "--abbrev-ref", "feature@{upstream}")
	if err != nil || strings.TrimSpace(string(upstream)) != "origin/main" {
		t.Fatalf("upstream %q %v", upstream, err)
	}
	if err := r.CreateTag("v1", "HEAD", "one"); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateTag("v2", "HEAD", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.PushRefs("origin", []string{"v1", "v2"}); err != nil {
		t.Fatal(err)
	}
	out, err := r.run(quick, "ls-remote", "--tags", "origin")
	if err != nil || !strings.Contains(string(out), "refs/tags/v1") || !strings.Contains(string(out), "refs/tags/v2") {
		t.Fatalf("remote tags %q %v", out, err)
	}
	if err := r.TrackBranch("", "origin/main"); err == nil {
		t.Fatal("an empty local branch was tracked")
	}
	if err := r.PushRefs("origin", nil); err == nil {
		t.Fatal("an empty reference list was pushed")
	}
}

func TestRemoteAddEditAndRemove(t *testing.T) {
	dir := t.TempDir()
	origin := t.TempDir()
	other := t.TempDir()
	gitcmd := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	setup := func(cwd string) {
		t.Helper()
		gitcmd(cwd, "init", "-q", "-b", "main")
		gitcmd(cwd, "config", "user.name", "Ada")
		gitcmd(cwd, "config", "user.email", "ada@example.com")
		if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitcmd(cwd, "add", "a.txt")
		gitcmd(cwd, "commit", "-q", "-m", "base")
	}
	setup(dir)
	setup(origin)
	setup(other)
	r := New(&LocalExecutor{Dir: dir})
	if err := r.RemoteAdd("upstream", origin, true); err != nil {
		t.Fatal(err)
	}
	url, err := r.RemoteURL("upstream")
	if err != nil || url != origin {
		t.Fatalf("url %q %v", url, err)
	}
	refs, err := r.Refs()
	if err != nil {
		t.Fatal(err)
	}
	var tracked bool
	for _, b := range refs.Remote {
		if b.Name == "upstream/main" && b.Remote == "upstream" {
			tracked = true
		}
	}
	if !tracked {
		t.Fatalf("fetched remote missing: %+v", refs.Remote)
	}
	if err := r.RemoteSetURL("upstream", other); err != nil {
		t.Fatal(err)
	}
	url, err = r.RemoteURL("upstream")
	if err != nil || url != other {
		t.Fatalf("edited url %q %v", url, err)
	}
	if err := r.RemoteRename("upstream", "mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RemoteURL("mirror"); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoteRemove("mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RemoteURL("mirror"); err == nil {
		t.Fatal("removed remote still answered")
	}
	if err := r.RemoteAdd("", origin, false); err == nil {
		t.Fatal("an empty remote name was accepted")
	}
}

func TestCherryPickAndRevertOptions(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	gitcmd("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "b.txt")
	gitcmd("commit", "-q", "-m", "feature")
	gitcmd("checkout", "-q", "main")
	r := New(&LocalExecutor{Dir: dir})
	commits, err := r.Log(LogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var feature string
	for _, c := range commits {
		if c.Subject == "feature" {
			feature = c.Hash
		}
	}
	if feature == "" {
		t.Fatal("feature commit missing")
	}
	gitcmd("checkout", "-q", "-b", "pick")
	if err := r.CherryPickWith(feature, ReplayOptions{NoCommit: true}); err != nil {
		t.Fatal(err)
	}
	st, err := r.Status()
	if err != nil || st.Branch != "pick" {
		t.Fatal(err)
	}
	var staged bool
	for _, f := range st.Staged {
		if f.Path == "b.txt" && f.Status == 'A' {
			staged = true
		}
	}
	if !staged {
		t.Fatalf("cherry-pick was committed or unstaged: %+v", st.Staged)
	}
	if err := r.Commit("picked", false); err != nil {
		t.Fatal(err)
	}
	gitcmd("checkout", "-q", "pick")
	if err := r.RevertWith(feature, ReplayOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("reverted file stayed: %v", err)
	}
	if err := r.CherryPickWith("", ReplayOptions{}); err == nil {
		t.Fatal("an empty commit was cherry-picked")
	}
	if err := r.RevertWith(feature, ReplayOptions{Mainline: -1}); err == nil {
		t.Fatal("a negative mainline was accepted")
	}
}

func TestStashPaths(t *testing.T) {
	dir := t.TempDir()
	gitcmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitcmd("init", "-q", "-b", "main")
	gitcmd("config", "user.name", "Ada")
	gitcmd("config", "user.email", "ada@example.com")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitcmd("add", "a.txt")
	gitcmd("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	if err := r.StashPaths("only a", []string{"a.txt", "new.txt"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(body) != "one\n" {
		t.Fatalf("stashed file %q %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file stayed: %v", err)
	}
	body, err = os.ReadFile(filepath.Join(dir, "keep.txt"))
	if err != nil || string(body) != "keep\n" {
		t.Fatalf("other file changed: %q %v", body, err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "only a") {
		t.Fatalf("stash %+v %v", stashes, err)
	}
	if err := r.StashPaths("none", nil); err == nil {
		t.Fatal("an empty path list was stashed")
	}
}

func TestCheckIgnore(t *testing.T) {
	dir := testRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build", "out.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.tmp"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: dir})
	matched, err := r.CheckIgnore("# comment\n!keep.tmp\n*.tmp\nbuild/\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build/out.log", "notes.tmp"}
	if strings.Join(matched, ",") != strings.Join(want, ",") {
		t.Fatalf("matched %v", matched)
	}
	none, err := r.CheckIgnore("# only a comment\n")
	if err != nil || none != nil {
		t.Fatalf("comments matched %v %v", none, err)
	}
	miss, err := r.CheckIgnore("missing.dat\n")
	if err != nil || miss != nil {
		t.Fatalf("unmatched %v %v", miss, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err == nil {
		t.Fatalf("preview wrote .gitignore: %q", body)
	}
}

func TestIndexFlags(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags, err := r.IndexFlags()
	if err != nil || len(flags) != 0 {
		t.Fatalf("flags before marking: %+v %v", flags, err)
	}
	checkIndexFlag(t, r, AssumeUnchanged, "a.txt")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bee\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkIndexFlag(t, r, SkipWorktree, "b.txt")

	odd := "odd name.txt"
	if err := os.WriteFile(filepath.Join(dir, odd), []byte("odd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Stage(odd); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("add odd", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, odd), []byte("odd\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.SetIndexFlag(AssumeUnchanged, true, odd); err != nil {
		t.Fatal(err)
	}
	if err := r.SetIndexFlag(SkipWorktree, true, odd); err != nil {
		t.Fatal(err)
	}
	flags, err = r.IndexFlags()
	if err != nil {
		t.Fatal(err)
	}
	var found *IndexFlags
	for i := range flags {
		if flags[i].Path == odd {
			found = &flags[i]
		}
	}
	if found == nil || !found.AssumeUnchanged || !found.SkipWorktree {
		t.Fatalf("both bits %+v", flags)
	}
	if err := r.SetIndexFlag(AssumeUnchanged, false, odd); err != nil {
		t.Fatal(err)
	}
	flags, err = r.IndexFlags()
	if err != nil {
		t.Fatal(err)
	}
	found = nil
	for i := range flags {
		if flags[i].Path == odd {
			found = &flags[i]
		}
	}
	if found == nil || found.AssumeUnchanged || !found.SkipWorktree {
		t.Fatalf("skip remains %+v", flags)
	}

	parsed := ParseIndexFlags([]byte("H plain.txt\x00h assumed.txt\x00S skipped.txt\x00s both.txt\x00H weird\nname.txt\x00h quoted 'name'.txt\x00"))
	want := map[string]IndexFlags{
		"assumed.txt":       {Path: "assumed.txt", AssumeUnchanged: true},
		"skipped.txt":       {Path: "skipped.txt", SkipWorktree: true},
		"both.txt":          {Path: "both.txt", AssumeUnchanged: true, SkipWorktree: true},
		"quoted 'name'.txt": {Path: "quoted 'name'.txt", AssumeUnchanged: true},
	}
	if len(parsed) != len(want) {
		t.Fatalf("parsed %+v", parsed)
	}
	for _, f := range parsed {
		if want[f.Path] != f {
			t.Fatalf("parsed %+v", parsed)
		}
	}
	if err := r.SetIndexFlag(IndexFlag(9), true, "a.txt"); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

func checkIndexFlag(t *testing.T, r *Repo, flag IndexFlag, path string) {
	t.Helper()
	before, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !statusHas(before, path) {
		t.Fatalf("%s was not changed before the flag: %+v", path, before)
	}
	if err := r.SetIndexFlag(flag, true, path); err != nil {
		t.Fatal(err)
	}
	hidden, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if statusHas(hidden, path) {
		t.Fatalf("%s stayed visible: %+v", path, hidden)
	}
	flags, err := r.IndexFlags()
	if err != nil {
		t.Fatal(err)
	}
	if !indexFlagSet(flags, flag, path) {
		t.Fatalf("flag list %+v lacks %s", flags, path)
	}
	if err := r.SetIndexFlag(flag, false, path); err != nil {
		t.Fatal(err)
	}
	after, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !statusHas(after, path) {
		t.Fatalf("%s did not return: %+v", path, after)
	}
	flags, err = r.IndexFlags()
	if err != nil {
		t.Fatal(err)
	}
	if indexFlagSet(flags, flag, path) {
		t.Fatalf("flag stayed set: %+v", flags)
	}
}

func statusHas(st Status, path string) bool {
	for _, f := range append(append([]FileChange{}, st.Staged...), st.Unstaged...) {
		if f.Path == path {
			return true
		}
	}
	return false
}

func indexFlagSet(flags []IndexFlags, flag IndexFlag, path string) bool {
	for _, f := range flags {
		if f.Path != path {
			continue
		}
		if flag == AssumeUnchanged {
			return f.AssumeUnchanged
		}
		return f.SkipWorktree
	}
	return false
}

func TestBenchmarkScore(t *testing.T) {
	fast := []BenchmarkSample{{MS: 20}, {MS: 20}, {MS: 20}}
	if got := BenchmarkScore(fast, 20); got != 100 {
		t.Fatalf("as fast as the latency scores %d", got)
	}
	if got := BenchmarkScore([]BenchmarkSample{{MS: 320}}, 20); got != 50 {
		t.Fatalf("300 ms over the latency scores %d", got)
	}
	slow := BenchmarkScore([]BenchmarkSample{{MS: 5000}}, 20)
	if slow >= 10 || slow < 0 {
		t.Fatalf("a slow repository scores %d", slow)
	}
	// The latency is not held against the repository.
	if BenchmarkScore([]BenchmarkSample{{MS: 520}}, 500) < 90 {
		t.Fatal("the latency counted against the repository")
	}
	if BenchmarkScore(nil, 0) != 0 {
		t.Fatal("no samples score")
	}
}

// The five Git trace options are separate: each sets only its own
// variable, locally and in the command sent to a server.
func TestTraceOptionsAreIndependent(t *testing.T) {
	vars := map[string]Trace{
		"GIT_TRACE=1":              {Git: true},
		"GIT_TRACE_CURL=1":         {Curl: true},
		"GIT_SSH_COMMAND=ssh -vvv": {SSH: true},
		"GIT_TRACE_PACKFILE=1":     {Pack: true},
		"GIT_TRACE_PERFORMANCE=1":  {Perf: true},
	}
	// The command sent to a server quotes each value for the server's
	// shell, so the remote form of a variable with a space differs from
	// the local process environment, which never adds quotes.
	remoteForm := func(name string) string {
		n, v, _ := strings.Cut(name, "=")
		return n + "=" + ShellQuote(v)
	}
	for name, trace := range vars {
		env := strings.Join(commandEnv(PathProcess, trace), "\n")
		remote := (&SSHExecutor{Host: "h", Dir: "/r", Trace: trace}).RemoteCommand("status")
		for other := range vars {
			has, hasRemote := strings.Contains(env, other), strings.Contains(remote, remoteForm(other))
			if (other == name) != has || (other == name) != hasRemote {
				t.Errorf("trace %s: %s local %v remote %v", name, other, has, hasRemote)
			}
		}
	}
}
