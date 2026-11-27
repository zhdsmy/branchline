package git

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestConnectionMessagePicksReasonLine picks git's reason for a failure
// over the progress and bookkeeping lines that come before it.
func TestConnectionMessagePicksReasonLine(t *testing.T) {
	gitErr := func(stderr string) error {
		return &Error{Args: []string{"ls-remote"}, ExitCode: 128, Stderr: stderr}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "progress before fatal",
			err:  gitErr("Cloning into 'x'...\nfatal: could not read Username for 'https://example.com': terminal prompts disabled\n"),
			want: "could not read Username for 'https://example.com': terminal prompts disabled",
		},
		{
			name: "remote lines before fatal",
			err:  gitErr("remote: Enumerating objects: 5, done.\nremote: Counting objects: 100% (5/5), done.\nfatal: pack has bad object at offset 12\n"),
			want: "pack has bad object at offset 12",
		},
		{
			name: "only an error line",
			err:  gitErr("error: RPC failed; curl 56 OpenSSL SSL_read: connection reset\n"),
			want: "RPC failed; curl 56 OpenSSL SSL_read: connection reset",
		},
		{
			name: "error line before the fatal line",
			err:  gitErr("error: 5000 bytes of body are still expected\nfatal: the remote end hung up unexpectedly\n"),
			want: "the remote end hung up unexpectedly",
		},
		{
			name: "one plain line",
			err:  gitErr("ssh: connect to host example.com port 22: Operation timed out\n"),
			want: "ssh: connect to host example.com port 22: Operation timed out",
		},
		{
			name: "carriage returns of a Windows style line ending",
			err:  gitErr("Cloning into 'x'...\r\nfatal: unable to connect to example.com\r\n"),
			want: "unable to connect to example.com",
		},
		{
			name: "carriage return on a plain line",
			err:  gitErr("some progress\r\n"),
			want: "some progress",
		},
		{
			name: "whitespace before fatal",
			err:  gitErr("  fatal: cannot lock ref, another clone is running\n"),
			want: "cannot lock ref, another clone is running",
		},
		{
			name: "an error that is not git's uses its own words",
			err:  errors.New("context canceled"),
			want: "context canceled",
		},
		{
			name: "empty stderr falls back to the error text",
			err:  &Error{Args: []string{"clone"}, ExitCode: 128},
			want: "git clone: exit status 128",
		},
		{
			name: "whitespace-only stderr falls back to the error text",
			err:  gitErr("  \r\n\t\n"),
			want: "git ls-remote: exit status 128",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ConnectionMessage(c.err); got != c.want {
				t.Errorf("ConnectionMessage() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestConnectionMessageCloneFailureLoopback clones a server that answers
// every request with 401: the message is the authentication failure, not
// the "Cloning into" progress line, and no credential helper of this Mac
// runs or keeps the answer.
func TestConnectionMessageCloneFailureLoopback(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "")

	// No credential helper can be left: the empty entry above clears the
	// list, so --get-all prints nothing but one empty line.
	check := exec.Command("git", "config", "--get-all", "credential.helper")
	check.Dir = t.TempDir()
	if out, err := check.CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("credential helpers are configured: %q (err %v)", out, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	parent := t.TempDir()
	r := New(&LocalExecutor{Dir: parent})
	err := r.CloneInto(context.Background(), CloneOptions{URL: server.URL + "/repo.git", Name: "repo"})
	if err == nil {
		t.Fatal("the clone of a server that answers 401 succeeded")
	}
	var ge *Error
	if errors.As(err, &ge) {
		t.Logf("clone stderr verbatim:\n%s", ge.Stderr)
	}
	msg := ConnectionMessage(err)
	t.Logf("ConnectionMessage: %q", msg)
	if strings.HasPrefix(msg, "Cloning into") {
		t.Fatalf("the message is the progress line: %q", msg)
	}
	if !strings.Contains(msg, "could not read Username") && !strings.Contains(msg, "Authentication failed") {
		t.Fatalf("the message is not the authentication failure: %q", msg)
	}

	// Nothing of the failed login was kept in the keychain of this Mac.
	if out, err := exec.Command("security", "find-internet-password", "-s", "127.0.0.1").CombinedOutput(); err == nil {
		t.Fatalf("the keychain keeps a password for 127.0.0.1: %q", out)
	}
}
