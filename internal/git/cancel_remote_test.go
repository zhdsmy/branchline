package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stuckSource is an HTTP server that answers nothing: like a repository
// host that hangs, it keeps the request open until the client goes away
// or the test lets go.
type stuckSource struct {
	server    *httptest.Server
	release   chan struct{}
	connected chan struct{}
	connOnce  sync.Once
	mu        sync.Mutex
	dropped   bool
}

func newStuckSource(t *testing.T) *stuckSource {
	t.Helper()
	s := &stuckSource{
		release:   make(chan struct{}),
		connected: make(chan struct{}),
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.connOnce.Do(func() { close(s.connected) })
		select {
		case <-r.Context().Done():
			s.mu.Lock()
			s.dropped = true
			s.mu.Unlock()
		case <-s.release:
		}
	}))
	// Letting go comes first: the server's close waits for the connections,
	// which only a released or dropped request lets end.
	t.Cleanup(s.server.Close)
	t.Cleanup(func() { close(s.release) })
	return s
}

// waitConnected passes once git has asked the source, failing after five
// seconds.
func (s *stuckSource) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-s.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("git never connected to the stuck source")
	}
}

// connectionDropped reports whether the server saw the client go away.
func (s *stuckSource) connectionDropped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// psEntries is the pid, process group and command line of every process.
func psEntries(t *testing.T) [][3]string {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "pid=,pgid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var entries [][3]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			entries = append(entries, [3]string{fields[0], fields[1], strings.Join(fields[2:], " ")})
		}
	}
	return entries
}

// findGroupLeader is the process group of the session leader whose command
// line holds part, or 0. A shell sshd starts for a session leads its own
// group, so the leader is the process whose id is its group: that keeps
// the ssh client on this Mac out, which carries the same words in its
// arguments but belongs to the test's group.
func findGroupLeader(t *testing.T, part string) int {
	t.Helper()
	for _, e := range psEntries(t) {
		if e[0] == e[1] && strings.Contains(e[2], part) {
			pg, err := strconv.Atoi(e[1])
			if err == nil {
				return pg
			}
		}
	}
	return 0
}

// groupAlive reports whether any process still belongs to the group,
// checking with signal 0: nothing is sent.
func groupAlive(pg int) bool {
	return syscall.Kill(-pg, 0) == nil
}

// waitGroupLeader passes once the leader's group is known, failing after
// five seconds.
func waitGroupLeader(t *testing.T, part string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pg := findGroupLeader(t, part); pg > 0 {
			return pg
		}
		if time.Now().After(deadline) {
			t.Fatalf("no session leader with %q in its command line", part)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitGroupGone passes once no process of the group is left, failing when
// the deadline from the cancellation passes.
func waitGroupGone(t *testing.T, pg int, deadline time.Time) {
	t.Helper()
	for {
		if !groupAlive(pg) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive past the deadline", pg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitConnDropped passes once the source saw git's connection close,
// failing when the deadline from the cancellation passes.
func waitConnDropped(t *testing.T, s *stuckSource, deadline time.Time) {
	t.Helper()
	for {
		if s.connectionDropped() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the stuck source still sees the connection open past the deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestCancelSSHCloneStopsServerGit cancels a clone the server runs against
// a source that never answers: the command returns at once, and the
// process group the server named is gone, git having removed the folder
// it had started and closed its connection to the source.
func TestCancelSSHCloneStopsServerGit(t *testing.T) {
	srv := startSSHD(t)
	src := newStuckSource(t)
	parent := filepath.Join(t.TempDir(), "srv", "parent")
	ex := srv.executor(t, parent)
	must(t, EnsureDir(ex))
	r := New(ex)
	token := fmt.Sprintf("cancel%d", time.Now().UnixNano())
	url := src.server.URL + "/" + token + ".git"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.CloneInto(ctx, CloneOptions{URL: url, Name: "stuckcl"})
	}()

	src.waitConnected(t)
	pg := waitGroupLeader(t, token)
	cancel()
	cancelAt := time.Now()
	deadline := cancelAt.Add(5 * time.Second)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloneInto did not return within five seconds of the cancellation")
	}
	elapsed := time.Since(cancelAt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}
	t.Logf("cancel elapsed: %s", elapsed)

	waitGroupGone(t, pg, deadline)
	waitConnDropped(t, src, deadline)
	if time.Now().After(deadline) {
		t.Fatal("assertions finished past the deadline")
	}
	if _, statErr := os.Stat(filepath.Join(parent, "stuckcl")); !os.IsNotExist(statErr) {
		t.Fatalf("the canceled clone left its folder on the server: %v", statErr)
	}
}

// TestCancelSSHBeforeConnect cancels while ssh is still connecting, and
// before ssh even starts: the command returns, and nothing but the error
// comes of it.
func TestCancelSSHBeforeConnect(t *testing.T) {
	srv := startSSHD(t)

	t.Run("PreCanceled", func(t *testing.T) {
		dir := testRepo(t)
		ex := srv.executor(t, dir)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		_, err := ex.Run(ctx, nil, "rev-parse", "--git-dir")
		if err == nil {
			t.Fatal("a canceled command ran")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("took %s", elapsed)
		}
	})

	t.Run("Blackhole", func(t *testing.T) {
		isolateCredentials(t)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		var mu sync.Mutex
		var held []net.Conn
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				held = append(held, c)
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			_ = ln.Close()
			mu.Lock()
			for _, c := range held {
				_ = c.Close()
			}
			mu.Unlock()
		})

		u, err := user.Current()
		must(t, err)
		ex := &SSHExecutor{
			Host:     "127.0.0.1",
			Port:     ln.Addr().(*net.TCPAddr).Port,
			User:     u.Username,
			Identity: srv.identity,
			Dir:      t.TempDir(),
			SSH:      srv.ssh,
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := ex.Run(ctx, nil, "rev-parse", "--git-dir")
			done <- err
		}()

		// ssh has connected to the blackhole and waits for a banner.
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			n := len(held)
			mu.Unlock()
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("ssh never connected to the blackhole")
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		cancelAt := time.Now()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the command succeeded against a blackhole")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the command did not return within five seconds of the cancellation")
		}
		t.Logf("cancel elapsed: %s", time.Since(cancelAt))
	})
}

// TestCancelLocalCloneStopsChildren cancels a clone on this Mac against a
// source that never answers: the command returns at once with its helper
// processes stopped, the connection closed and the folder gone.
func TestCancelLocalCloneStopsChildren(t *testing.T) {
	isolateCredentials(t)
	src := newStuckSource(t)
	parent := t.TempDir()
	r := New(&LocalExecutor{Dir: parent})
	token := fmt.Sprintf("cancel%d", time.Now().UnixNano())
	url := src.server.URL + "/" + token + ".git"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.CloneInto(ctx, CloneOptions{URL: url, Name: "stuckcl"})
	}()

	src.waitConnected(t)
	cancel()
	cancelAt := time.Now()
	deadline := cancelAt.Add(5 * time.Second)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloneInto did not return within five seconds of the cancellation")
	}
	elapsed := time.Since(cancelAt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want canceled", err)
	}
	t.Logf("cancel elapsed: %s", elapsed)

	waitConnDropped(t, src, deadline)
	if _, statErr := os.Stat(filepath.Join(parent, "stuckcl")); !os.IsNotExist(statErr) {
		t.Fatalf("the canceled clone left its folder: %v", statErr)
	}
}

// TestCancelMarkerNeverShown checks that the marker the server's shell
// writes is lifted out of every output the user or the app can see, while
// the rest of standard error comes through unchanged.
func TestCancelMarkerNeverShown(t *testing.T) {
	isolateCredentials(t)
	srv := startSSHD(t)
	dir := testRepo(t)
	ex := srv.executor(t, dir)

	// A command that succeeds.
	out, err := ex.Run(context.Background(), nil, "rev-parse", "--git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(pgidMarker)) || strings.Contains(string(out), "branchline-pgid") {
		t.Fatalf("stdout holds the marker: %q", out)
	}

	// A command that fails: the error text has git's words, not the marker.
	_, err = ex.Run(context.Background(), nil, "rev-parse", "--verify", "refs/heads/no-such-branch")
	if err == nil {
		t.Fatal("rev-parse of a missing ref succeeded")
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("err is %T, want *Error", err)
	}
	if strings.Contains(ge.Stderr, "branchline-pgid") || strings.ContainsRune(ge.Stderr, '\x1e') {
		t.Fatalf("error text holds the marker: %q", ge.Stderr)
	}
	if !strings.Contains(ge.Stderr, "fatal") {
		t.Fatalf("error text lost git's words: %q", ge.Stderr)
	}

	// A script whose stderr and exit come from the shell: unchanged but
	// for the marker, which is gone from both streams and the error text.
	r := New(ex)
	got, err := r.Script("printf 'keep me\\n'; printf 'shell words\\n' >&2; exit 3")
	if err == nil {
		t.Fatal("the script succeeded")
	}
	if !errors.As(err, &ge) {
		t.Fatalf("err is %T, want *Error", err)
	}
	if ge.Stderr != "shell words\n" {
		t.Fatalf("script stderr %q, want exactly the shell's words", ge.Stderr)
	}
	if got != "keep me\n" {
		t.Fatalf("script stdout %q", got)
	}

	// A command that can ask, through the askpass wiring, both for the
	// login and for git on the server: no marker anywhere, and the
	// answers still arrive.
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(askingSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOGIN_DONE", filepath.Join(t.TempDir(), "login"))
	rec := &recorder{}
	ask := NewAskPass(rec.prompt)
	defer ask.Close()
	aex := &SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true, AskPass: ask}
	fill, err := credentialFill(t, aex)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fill, "branchline-pgid") {
		t.Fatalf("credential fill printed the marker: %q", fill)
	}
	if !strings.Contains(fill, "password=s3cret pass") {
		t.Fatalf("credential fill lost the answer: %q", fill)
	}
	_, err = aex.Run(context.Background(), nil, "rev-parse", "--verify", "refs/heads/no-such-branch")
	if !errors.As(err, &ge) || strings.Contains(ge.Stderr, "branchline-pgid") || strings.ContainsRune(ge.Stderr, '\x1e') {
		t.Fatalf("askpass error text holds the marker: %v", err)
	}

	// setExecStderr, which a recorded invocation keeps, has no marker.
	log := NewCommandLog()
	logged := New(ex).WithCommandLog(log)
	if _, err := logged.Output("rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
	records, _ := log.Snapshot()
	if len(records) != 1 {
		t.Fatalf("records %+v", records)
	}
	if strings.Contains(records[0].Stderr, "branchline-pgid") || strings.ContainsRune(records[0].Stderr, '\x1e') {
		t.Fatalf("recorded stderr holds the marker: %q", records[0].Stderr)
	}

	t.Run("Writer", func(t *testing.T) {
		cases := []struct {
			name   string
			writes [][]byte
			text   string
			pg     int
		}{
			{
				name:   "plain stream",
				writes: [][]byte{[]byte("one\ntwo\nthree")},
				text:   "one\ntwo\nthree",
			},
			{
				name:   "marker first",
				writes: [][]byte{[]byte(pgidMarker + "8123\nremote: counting\n")},
				text:   "remote: counting\n",
				pg:     8123,
			},
			{
				name:   "noise before the marker",
				writes: [][]byte{[]byte("ssh warning\x1ex noise\n" + pgidMarker + "42\nafter\n")},
				text:   "ssh warning\x1ex noise\nafter\n",
				pg:     42,
			},
			{
				name:   "split byte by byte",
				text:   "after\n",
				pg:     77,
				writes: markerBytes(77, "after\n"),
			},
			{
				name:   "deviating start passes through",
				writes: [][]byte{[]byte("pre\x1ebranchline-noise\npost")},
				text:   "pre\x1ebranchline-noise\npost",
			},
			{
				name:   "digits without a newline pass through",
				writes: [][]byte{[]byte(pgidMarker + "123x tail")},
				text:   pgidMarker + "123x tail",
			},
			{
				name:   "a second marker is ordinary text",
				writes: [][]byte{[]byte(pgidMarker + "55\n" + pgidMarker + "66\n")},
				text:   pgidMarker + "66\n",
				pg:     55,
			},
			{
				name:   "a broken stream loses nothing",
				writes: [][]byte{[]byte("words\x1ebranchline-p")},
				text:   "words\x1ebranchline-p",
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				var buf bytes.Buffer
				w := newPgidWriter()
				w.chain(&buf)
				for _, p := range c.writes {
					n, err := w.Write(p)
					if err != nil || n != len(p) {
						t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(p))
					}
				}
				w.finish()
				if buf.String() != c.text {
					t.Errorf("text %q, want %q", buf.String(), c.text)
				}
				if w.pgid() != c.pg {
					t.Errorf("pgid %d, want %d", w.pgid(), c.pg)
				}
			})
		}
	})
}

// markerBytes is the marker line for pg and text, one byte per write.
func markerBytes(pg int, text string) [][]byte {
	full := pgidMarker + strconv.Itoa(pg) + "\n" + text
	var writes [][]byte
	for i := 0; i < len(full); i++ {
		writes = append(writes, []byte{full[i]})
	}
	return writes
}

// loginShells is every shell a login can be, each run with the marker
// prefix and the kill the way the server would. fish joins in when the
// machine has it.
func loginShells(t *testing.T) []string {
	t.Helper()
	shells := []string{"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh", "/bin/tcsh", "/bin/csh"}
	if fish, err := exec.LookPath("fish"); err == nil {
		shells = append(shells, fish)
	}
	return shells
}

// shellEnv is a HOME of the test and nothing the user's files can leak
// into: zsh reads ~/.zshenv and csh ~/.cshrc even for -c.
func shellEnv(t *testing.T) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LC_ALL=C"}, home
}

// dirEmpty fails the test when anything landed in dir: a csh reading of
// the prefix would leave a file named 2 behind.
func dirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s is not empty: %v", dir, entries)
	}
}

// TestCancelMarkerLoginShells runs the marker prefix through every shell a
// login can use: the process group it names is the shell itself, nothing
// reaches the command's output, and nothing is written anywhere.
func TestCancelMarkerLoginShells(t *testing.T) {
	for _, shell := range loginShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s on this machine", shell)
			}
			env, home := shellEnv(t)
			dir := t.TempDir()
			cmd := exec.Command(shell, "-c", remoteShellCommand(pgidPrefix+"true"))
			cmd.Dir = dir
			cmd.Env = env
			var out, downstream bytes.Buffer
			cmd.Stdout = &out
			pw := newPgidWriter()
			pw.chain(&downstream)
			cmd.Stderr = pw
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s: %v", shell, err)
			}
			pw.finish()
			if want := cmd.Process.Pid; pw.pgid() != want {
				t.Errorf("%s: group %d, want the shell's own %d", shell, pw.pgid(), want)
			}
			if downstream.Len() != 0 {
				t.Errorf("%s: downstream stderr %q, want empty", shell, downstream.Bytes())
			}
			if out.Len() != 0 {
				t.Errorf("%s: stdout %q, want empty", shell, out.Bytes())
			}
			dirEmpty(t, dir)
			dirEmpty(t, home)
		})
	}
}

// TestCancelKillLoginShells ends a process group through every shell a
// login can use, with the exact command canceling sends to the server.
func TestCancelKillLoginShells(t *testing.T) {
	for _, shell := range loginShells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s on this machine", shell)
			}
			env, home := shellEnv(t)
			dir := t.TempDir()

			bed := exec.Command("/bin/sleep", "30")
			bed.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := bed.Start(); err != nil {
				t.Fatal(err)
			}
			pg := bed.Process.Pid
			t.Cleanup(func() {
				_ = syscall.Kill(-pg, syscall.SIGKILL)
				_ = bed.Wait()
			})

			cmd := exec.Command(shell, "-c", remoteGroupKill(pg))
			cmd.Dir = dir
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: kill command: %v %s", shell, err, out)
			}

			deadline := time.Now().Add(5 * time.Second)
			for groupAlive(pg) {
				if time.Now().After(deadline) {
					t.Fatalf("%s: process group %d still alive after the kill", shell, pg)
				}
				time.Sleep(50 * time.Millisecond)
			}
			dirEmpty(t, dir)
			dirEmpty(t, home)
		})
	}
}

// TestCancelKillPosixShells ends a process group through the shells a
// server's /bin/sh can be: the kill script itself, without the wrapping,
// as dash on Debian and Ubuntu, bash, zsh, ksh or BusyBox ash would run
// it. The login shell test above covers the outer parsing; this one is
// about what the inner shell makes of the kill.
func TestCancelKillPosixShells(t *testing.T) {
	type shell struct {
		name string
		args []string
	}
	var shells []shell
	for _, path := range []string{"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh"} {
		if _, err := os.Stat(path); err == nil {
			shells = append(shells, shell{filepath.Base(path), []string{path, "-c"}})
		}
	}
	if busybox, err := exec.LookPath("busybox"); err == nil {
		shells = append(shells, shell{"busybox", []string{busybox, "sh", "-c"}})
	}
	for _, sh := range shells {
		t.Run(sh.name, func(t *testing.T) {
			env, home := shellEnv(t)
			dir := t.TempDir()

			bed := exec.Command("/bin/sleep", "30")
			bed.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := bed.Start(); err != nil {
				t.Fatal(err)
			}
			pg := bed.Process.Pid
			t.Cleanup(func() {
				_ = syscall.Kill(-pg, syscall.SIGKILL)
				_ = bed.Wait()
			})

			args := append(append([]string{}, sh.args...), remoteGroupKillScript(pg))
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = dir
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: kill script: %v %s", sh.name, err, out)
			}

			deadline := time.Now().Add(5 * time.Second)
			for groupAlive(pg) {
				if time.Now().After(deadline) {
					t.Fatalf("%s: process group %d still alive after the kill", sh.name, pg)
				}
				time.Sleep(50 * time.Millisecond)
			}
			dirEmpty(t, dir)
			dirEmpty(t, home)
		})
	}
}
