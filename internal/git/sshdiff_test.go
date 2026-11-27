package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type recordingExecutor struct {
	args  [][]string
	blobs map[string][]byte
	Dir   string
}

func (e *recordingExecutor) Describe() string { return "ssh test" }
func (e *recordingExecutor) Worktree() string { return e.Dir }

func (e *recordingExecutor) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	e.args = append(e.args, append([]string(nil), args...))
	for i, arg := range args {
		switch {
		case arg == "cat-file" && i+2 < len(args) && args[i+1] == "blob":
			return e.blobs[args[i+2]], nil
		case arg == "rev-parse" && len(args) >= 4 && args[len(args)-2] == "--verify":
			spec := args[len(args)-1]
			if strings.HasPrefix(spec, "missing:") {
				return nil, &Error{Args: args, ExitCode: 1, Stderr: "missing"}
			}
			if strings.HasPrefix(spec, "broken") {
				return nil, &Error{Args: args, ExitCode: 128, Stderr: "fatal: bad object"}
			}
			return []byte(hashFor(spec) + "\n"), nil
		case arg == "ls-files":
			return nil, nil
		}
	}
	return nil, nil
}

func hashFor(spec string) string {
	sum := 0
	for _, r := range spec {
		sum = sum*131 + int(r)
	}
	out := make([]byte, 40)
	for i := range out {
		sum = sum*1103515245 + 12345
		out[i] = "0123456789abcdef"[(sum>>16)&15]
	}
	return string(out)
}

func TestCompareSidesReadsContentWithoutDifftool(t *testing.T) {
	ex := &recordingExecutor{blobs: map[string][]byte{
		hashFor("HEAD:old.txt"): []byte("base"),
		hashFor("HEAD:a.txt"):   []byte("base"),
		hashFor(":new.txt"):     []byte("staged"),
		hashFor("aaa:new.txt"):  []byte("left"),
		hashFor("bbb:new.txt"):  []byte("right"),
	}}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("working"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex.Dir = dir
	repo := New(ex)

	local, remote, localName, remoteName, err := repo.CompareSides("", "index", "new.txt", "old.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(local) != "base" || string(remote) != "staged" || localName != "old.txt" || remoteName != "new.txt" {
		t.Fatalf("index got %q %q %s %s", local, remote, localName, remoteName)
	}

	local, remote, _, _, err = repo.CompareSides("", "", "a.txt")
	if err != nil || string(local) != "base" || string(remote) != "working" {
		t.Fatalf("worktree %q %q %v", local, remote, err)
	}

	local, remote, _, _, err = repo.CompareSides("aaa", "bbb", "new.txt")
	if err != nil || string(local) != "left" || string(remote) != "right" {
		t.Fatalf("revisions %q %q %v", local, remote, err)
	}

	local, _, _, _, err = repo.CompareSides("missing", "deadbeef", "old.txt")
	if err != nil || len(local) != 0 {
		t.Fatalf("missing base %q %v", local, err)
	}

	for _, call := range ex.args {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "difftool") || strings.Contains(joined, "opendiff") || strings.Contains(joined, "/Applications") {
			t.Fatalf("repository executor received a diff tool command: %q", call)
		}
	}
}

func TestCompareSidesKeepsReadFailures(t *testing.T) {
	ex := &recordingExecutor{blobs: map[string][]byte{}}
	repo := New(ex)
	if _, _, _, _, err := repo.CompareSides("broken", "HEAD", "a.txt"); err == nil || !strings.Contains(err.Error(), "bad object") {
		t.Fatalf("broken ref became empty: %v", err)
	}
}

func TestRemoteJoinCoversHomeRelativeAndSpaces(t *testing.T) {
	cases := []struct{ root, path, want string }{
		{"~", "dir/file.txt", "~/" + ShellQuote("dir/file.txt")},
		{"~/repo", "my file.txt", "~/repo/" + ShellQuote("my file.txt")},
		{"/srv/app", "dir/my file.txt", ShellQuote("/srv/app") + "/" + ShellQuote("dir/my file.txt")},
		{"relative repo", "dir/file.txt", ShellQuote("relative repo") + "/" + ShellQuote("dir/file.txt")},
	}
	for _, tc := range cases {
		if got := remoteJoin(tc.root, tc.path); got != tc.want {
			t.Fatalf("remoteJoin(%q,%q)=%q want %q", tc.root, tc.path, got, tc.want)
		}
	}
}

type shellExecutor struct {
	command string
	out     []byte
	err     error
}

func (e *shellExecutor) Describe() string { return "ssh shell" }
func (e *shellExecutor) DirPath() string  { return "/srv/app" }
func (e *shellExecutor) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, &Error{Args: []string{"git"}, ExitCode: 1, Stderr: "unexpected git"}
}
func (e *shellExecutor) RunShell(_ context.Context, command string) ([]byte, error) {
	e.command = command
	return e.out, e.err
}

func TestWorktreeFileDistinguishesMissingFromConnectionFailure(t *testing.T) {
	missing := &shellExecutor{err: &Error{Args: []string{"ssh"}, ExitCode: 1, Stderr: "cat: no such file or directory"}}
	repo := &Repo{Exec: missing}
	data, err := repo.worktreeFile("dir/my file.txt")
	if err != nil || len(data) != 0 {
		t.Fatalf("missing file %q %v", data, err)
	}
	if !strings.Contains(missing.command, "my file.txt") {
		t.Fatalf("command %q", missing.command)
	}
	failed := &shellExecutor{err: &Error{Args: []string{"ssh"}, ExitCode: 255, Stderr: "Permission denied"}}
	if _, err := (&Repo{Exec: failed}).worktreeFile("a.txt"); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("connection error hidden: %v", err)
	}
}

func TestCompareSidesUsesRealGitReferences(t *testing.T) {
	root := t.TempDir()
	born := filepath.Join(root, "born")
	if err := os.Mkdir(born, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, born, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(born, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, born, "add", "a.txt")
	runGit(t, born, "-c", "user.email=a@b.c", "-c", "user.name=tester", "commit", "-q", "-m", "c")
	blob := strings.TrimSpace(gitOutput(t, born, "rev-parse", "HEAD:a.txt"))

	detached := copyGit(t, born, filepath.Join(root, "detached"))
	runGit(t, detached, "checkout", "-q", "--detach")
	unborn := filepath.Join(root, "unborn")
	if err := os.Mkdir(unborn, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, unborn, "init", "-q", "-b", "main")
	blobHead := copyGit(t, born, filepath.Join(root, "blob"))
	if err := os.WriteFile(filepath.Join(blobHead, ".git", "refs", "heads", "main"), []byte(blob+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingObject := copyGit(t, born, filepath.Join(root, "missing-object"))
	if err := os.WriteFile(filepath.Join(missingObject, ".git", "refs", "heads", "main"), []byte("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	detachedBad := copyGit(t, born, filepath.Join(root, "detached-bad"))
	if err := os.WriteFile(filepath.Join(detachedBad, ".git", "HEAD"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	corruptIndex := copyGit(t, born, filepath.Join(root, "corrupt-index"))
	if err := os.WriteFile(filepath.Join(corruptIndex, ".git", "index"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		dir, base, changed, path string
		empty                    bool
	}{
		{detached, "HEAD", "HEAD", "missing.txt", true},
		{unborn, "HEAD", "HEAD", "missing.txt", true},
		{unborn, "", "index", "missing.txt", true},
		{born, "", "index", "missing.txt", true},
	} {
		local, _, _, _, err := New(&LocalExecutor{Dir: tc.dir}).CompareSides(tc.base, tc.changed, tc.path)
		if err != nil || !tc.empty || len(local) != 0 {
			t.Fatalf("%s %s %s: %q %v", tc.dir, tc.base, tc.changed, local, err)
		}
	}
	for _, dir := range []string{blobHead, missingObject, detachedBad} {
		if _, _, _, _, err := New(&LocalExecutor{Dir: dir}).CompareSides("HEAD", "HEAD", "a.txt"); err == nil {
			t.Fatalf("%s was treated as an empty file", dir)
		}
	}
	if _, _, _, _, err := New(&LocalExecutor{Dir: corruptIndex}).CompareSides("", "index", "a.txt"); err == nil {
		t.Fatal("corrupt index was treated as an empty file")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func copyGit(t *testing.T, src, dst string) string {
	t.Helper()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}
