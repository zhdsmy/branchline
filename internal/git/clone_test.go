package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneIntoLocalFolder(t *testing.T) {
	src := testRepo(t)
	parent := filepath.Join(t.TempDir(), "src", "work")
	ex := &LocalExecutor{Dir: parent}
	must(t, EnsureDir(ex))
	r := New(ex)
	must(t, r.CloneInto(context.Background(), CloneOptions{URL: src, Name: "copy", Recurse: true}))
	copy := New(&LocalExecutor{Dir: filepath.Join(parent, "copy")})
	cs, err := copy.Log(LogOptions{Limit: 5})
	must(t, err)
	if len(cs) == 0 {
		t.Fatal("the clone has no commits")
	}
	url, err := copy.RemoteURL("origin")
	must(t, err)
	if url != src {
		t.Fatalf("origin %q, want %q", url, src)
	}

	// A second clone into the same folder fails with git's reason, and
	// leaves the first one alone.
	if err := r.CloneInto(context.Background(), CloneOptions{URL: src, Name: "copy"}); err == nil {
		t.Fatal("cloned over an existing repository")
	}
	if _, err := os.Stat(filepath.Join(parent, "copy", ".git")); err != nil {
		t.Fatalf("the first clone is gone: %v", err)
	}
	for _, bad := range []string{"", "..", "a/b", "-x"} {
		if err := r.CloneInto(context.Background(), CloneOptions{URL: src, Name: bad}); err == nil {
			t.Fatalf("cloned into %q", bad)
		}
	}
}

// A canceled clone stops git and removes the folder it had started.
func TestCloneCanceledLeavesNothing(t *testing.T) {
	parent := t.TempDir()
	fake := filepath.Join(t.TempDir(), "git")
	// The last argument is the folder: make it, then hang as a slow
	// network would.
	script := "#!/bin/sh\nfor a; do last=$a; done\nmkdir -p \"$last/.git\"\nexec sleep 30\n"
	must(t, os.WriteFile(fake, []byte(script), 0o755))
	r := New(&LocalExecutor{Dir: parent, Git: fake})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(filepath.Join(parent, "slow")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	err := r.CloneInto(ctx, CloneOptions{URL: "https://example.com/slow.git", Name: "slow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "slow")); !os.IsNotExist(err) {
		t.Fatalf("the canceled clone left its folder: %v", err)
	}

	// A folder that was there before is never removed.
	must(t, os.MkdirAll(filepath.Join(parent, "kept"), 0o755))
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_ = r.CloneInto(ctx, CloneOptions{URL: "https://example.com/kept.git", Name: "kept"})
	if _, err := os.Stat(filepath.Join(parent, "kept")); err != nil {
		t.Fatalf("removed a folder the clone did not make: %v", err)
	}
}

// TestCloneOnSSHServer clones into a folder of the server, which then
// opens as an SSH repository.
func TestCloneOnSSHServer(t *testing.T) {
	srv := startSSHD(t)
	src := testRepo(t)
	parent := filepath.Join(t.TempDir(), "srv", "repos")
	ex := srv.executor(t, parent)
	must(t, EnsureDir(ex))
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("the server's folder was not made: %v", err)
	}
	r := New(ex)
	if n, err := r.TestRemote(src); err != nil || n == 0 {
		t.Fatalf("test connection from the server: %d %v", n, err)
	}
	if msg := ConnectionMessage(func() error { _, err := r.TestRemote(filepath.Join(parent, "missing")); return err }()); msg != RepositoryNotFound {
		t.Fatalf("missing repository says %q", msg)
	}
	must(t, r.CloneInto(context.Background(), CloneOptions{URL: src, Name: "app"}))
	opened := srv.executor(t, filepath.Join(parent, "app"))
	cs, err := New(opened).Log(LogOptions{Limit: 1})
	must(t, err)
	if len(cs) != 1 {
		t.Fatalf("log of the server's clone %+v", cs)
	}
	if !pathExists(ex, "app") || pathExists(ex, "nothing here") {
		t.Fatal("pathExists on the server")
	}
	must(t, removeTree(ex, "app"))
	if _, err := os.Stat(filepath.Join(parent, "app")); !os.IsNotExist(err) {
		t.Fatalf("removeTree on the server: %v", err)
	}
}

func TestRepoNameFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/ada/branchline.git":  "branchline",
		"https://github.com/ada/branchline/":     "branchline",
		"git@github.com:ada/branchline.git":      "branchline",
		"ssh://git@host:2222/srv/app.git":        "app",
		"host:app":                               "app",
		"/home/ada/src/tool/.git":                "tool",
		"https://example.com/a/b.git?ref=x#frag": "b",
		"  ":                                     "",
		"https://example.com/":                   "example.com",
	} {
		if got := RepoNameFromURL(in); got != want {
			t.Errorf("RepoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConvertURL(t *testing.T) {
	cases := []struct{ in, protocol, want string }{
		{"https://github.com/ada/branchline.git", "ssh", "git@github.com:ada/branchline.git"},
		{"https://github.com/ada/branchline", "ssh", "git@github.com:ada/branchline.git"},
		{"git@gitlab.com:group/sub/app.git", "https", "https://gitlab.com/group/sub/app.git"},
		{"ssh://git@host:2222/srv/app.git", "https", "https://host/srv/app.git"},
		{"git@github.com:ada/branchline.git", "ssh", "git@github.com:ada/branchline.git"},
	}
	for _, c := range cases {
		if got, ok := ConvertURL(c.in, c.protocol); !ok || got != c.want {
			t.Errorf("ConvertURL(%q, %s) = %q %v, want %q", c.in, c.protocol, got, ok, c.want)
		}
	}
	if _, ok := ConvertURL("/home/ada/repo", "ssh"); ok {
		t.Error("converted a path")
	}
	if URLProtocol("/srv/a:b") != "" || URLProtocol("C:/repo") != "" || URLProtocol("git@h:x") != "ssh" {
		t.Error("URLProtocol")
	}
}

func TestConnectionMessage(t *testing.T) {
	if ConnectionMessage(nil) != ConnectionSucceeded {
		t.Fatal("success")
	}
	r := New(&LocalExecutor{Dir: t.TempDir()})
	_, err := r.TestRemote(filepath.Join(t.TempDir(), "none"))
	if got := ConnectionMessage(err); got != RepositoryNotFound {
		t.Fatalf("a missing repository says %q", got)
	}
	denied := &Error{Args: []string{"ls-remote"}, ExitCode: 128, Stderr: "git@github.com: Permission denied (publickey).\r\nfatal: Could not read from remote repository."}
	if got := ConnectionMessage(denied); got != PermissionDenied {
		t.Fatalf("denied says %q", got)
	}
	other := &Error{Args: []string{"ls-remote"}, ExitCode: 128, Stderr: "fatal: unable to access 'https://x/': Could not resolve host: x\n"}
	if got := ConnectionMessage(other); got != "unable to access 'https://x/': Could not resolve host: x" {
		t.Fatalf("other says %q", got)
	}
}
