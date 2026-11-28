package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
	"github.com/zhdsmy/branchline/internal/store"
)

// readProbeToken marks the git arguments of a read the test watches: any
// argument carrying it makes the probe executor block the command instead
// of running it, so the read stays in flight until its context is canceled
// or the test releases it.
const readProbeToken = "readcancelprobe-"

// readProbeCall is one blocked command of a readProbeExecutor.
type readProbeCall struct {
	args     []string
	ctx      context.Context
	release  chan struct{}
	dead     chan struct{}
	deadOnce sync.Once
}

// readProbeExecutor blocks commands whose arguments carry readProbeToken
// and delegates everything else, so a session can load real snapshots while
// chosen reads stay in flight under the test's control.
type readProbeExecutor struct {
	delegate git.Executor

	mu     sync.Mutex
	probes []*readProbeCall
}

func (e *readProbeExecutor) Describe() string { return e.delegate.Describe() }

func probeTokenOf(args []string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, readProbeToken) {
			return a, true
		}
	}
	return "", false
}

func (e *readProbeExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if _, ok := probeTokenOf(args); !ok {
		return e.delegate.Run(ctx, stdin, args...)
	}
	c := &readProbeCall{
		args:    append([]string(nil), args...),
		ctx:     ctx,
		release: make(chan struct{}),
		dead:    make(chan struct{}),
	}
	e.mu.Lock()
	e.probes = append(e.probes, c)
	e.mu.Unlock()
	defer c.deadOnce.Do(func() { close(c.dead) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return nil, nil
	}
}

// probeCall returns the n-th blocked invocation (0-based) whose arguments
// carry the exact probe argument probeArg, failing after five seconds.
func (e *readProbeExecutor) probeCall(t *testing.T, probeArg string, n int) *readProbeCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		seen := 0
		for _, c := range e.probes {
			for _, a := range c.args {
				if a == probeArg {
					if seen == n {
						e.mu.Unlock()
						return c
					}
					seen++
					break
				}
			}
		}
		e.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("probe invocation %d of %q never started", n, probeArg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitDead passes once the probe's Run has returned, failing after the bound.
func (c *readProbeCall) waitDead(t *testing.T, bound time.Duration) {
	t.Helper()
	select {
	case <-c.dead:
	case <-time.After(bound):
		t.Fatal("the probe command did not return within the bound")
	}
}

// waitProbeCtxDone passes once the probe command's context is done.
func waitProbeCtxDone(t *testing.T, c *readProbeCall, what string, bound time.Duration) {
	t.Helper()
	select {
	case <-c.ctx.Done():
	case <-time.After(bound):
		t.Fatalf("%s: the read's context was not canceled within %s", what, bound)
	}
}

// newReadCancelFixture builds a repository with one commit of a probe-named
// file and the session that opens it through a probe executor.
func newReadCancelFixture(t *testing.T, name string) (*gitRepoFixture, *readProbeExecutor) {
	t.Helper()
	repo := newGitRepo(t, name)
	repo.CommitFile("base.txt", "base\n", "initial commit")
	ex := &readProbeExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}
	return repo, ex
}

// newReadCancelSession wires a controlled UI queue into a session whose
// repositories are opened through factory.
func newReadCancelSession(t *testing.T, factory func(store.Repo) *git.Repo) (*repoSession, *uiQueue) {
	t.Helper()
	q := newUIQueue()
	t.Cleanup(q.releaseAllAndWait)
	s := newRepoSession(q.post, q.spawn, factory)
	return s, q
}

func localFactory(ex *readProbeExecutor) func(store.Repo) *git.Repo {
	return func(store.Repo) *git.Repo { return git.New(ex) }
}

// startProbeRead begins a sessionRead of scope whose work runs one blocking
// probe command named token; deliveries are counted into the int done points
// at.
func startProbeRead(t *testing.T, s *repoSession, scope, token string, delivered *int) {
	t.Helper()
	ok := sessionRead(s, scope, func(repo *git.Repo) (string, error) {
		err := repo.Run(readProbeToken + token)
		return token, err
	}, func(string, error) {
		*delivered++
	})
	if !ok {
		t.Fatalf("sessionRead of %q in scope %q did not start", token, scope)
	}
}

// A read in flight of a scope is canceled within a second when another read
// of the same scope starts, a read of another scope is unaffected, and only
// the superseding read's result reaches done.
func TestReadCancelSupersedeStopsGit(t *testing.T) {
	repo, ex := newReadCancelFixture(t, "rc-supersede")
	s, q := newReadCancelSession(t, localFactory(ex))
	s.Open(repo.StoreRepo())
	q.Drain(t)

	var doneA, doneB, doneC int
	startProbeRead(t, s, "scope-x", "xa", &doneA)
	callA := ex.probeCall(t, readProbeToken+"xa", 0)
	startProbeRead(t, s, "scope-y", "ya", &doneC)
	callC := ex.probeCall(t, readProbeToken+"ya", 0)

	startProbeRead(t, s, "scope-x", "xb", &doneB)
	callB := ex.probeCall(t, readProbeToken+"xb", 0)

	waitProbeCtxDone(t, callA, "superseded read of scope-x", time.Second)
	if callB.ctx.Err() != nil {
		t.Fatalf("the superseding read was canceled: %v", callB.ctx.Err())
	}
	if callC.ctx.Err() != nil {
		t.Fatalf("the read of scope-y was canceled: %v", callC.ctx.Err())
	}

	close(callB.release)
	close(callC.release)
	q.Drain(t)

	if doneA != 0 {
		t.Fatalf("the superseded read delivered %d results, want 0", doneA)
	}
	if doneB != 1 {
		t.Fatalf("the superseding read delivered %d results, want 1", doneB)
	}
	if doneC != 1 {
		t.Fatalf("the read of scope-y delivered %d results, want 1", doneC)
	}
	if len(s.readCancels) != 0 {
		t.Fatalf("readCancels holds %d entries after every read finished, want 0", len(s.readCancels))
	}
}

// cancelRead cancels the scope's in-flight read, Close cancels every
// in-flight read, and opening another repository does the same.
func TestReadCancelCancelCloseOpen(t *testing.T) {
	t.Run("cancelRead", func(t *testing.T) {
		repo, ex := newReadCancelFixture(t, "rc-cancelread")
		s, q := newReadCancelSession(t, localFactory(ex))
		s.Open(repo.StoreRepo())
		q.Drain(t)

		var done int
		startProbeRead(t, s, "scope-x", "xa", &done)
		call := ex.probeCall(t, readProbeToken+"xa", 0)

		s.cancelRead("scope-x")
		waitProbeCtxDone(t, call, "read canceled through cancelRead", time.Second)
		q.Drain(t)

		if done != 0 {
			t.Fatalf("the canceled read delivered %d results, want 0", done)
		}
		if len(s.readCancels) != 0 {
			t.Fatalf("readCancels holds %d entries after the canceled read finished, want 0", len(s.readCancels))
		}
	})

	t.Run("close", func(t *testing.T) {
		repo, ex := newReadCancelFixture(t, "rc-close")
		s, q := newReadCancelSession(t, localFactory(ex))
		s.Open(repo.StoreRepo())
		q.Drain(t)

		var doneX, doneY int
		startProbeRead(t, s, "scope-x", "xa", &doneX)
		callX := ex.probeCall(t, readProbeToken+"xa", 0)
		startProbeRead(t, s, "scope-y", "ya", &doneY)
		callY := ex.probeCall(t, readProbeToken+"ya", 0)

		s.Close()
		waitProbeCtxDone(t, callX, "read of scope-x after Close", time.Second)
		waitProbeCtxDone(t, callY, "read of scope-y after Close", time.Second)
		q.Drain(t)

		if doneX != 0 || doneY != 0 {
			t.Fatalf("canceled reads delivered (%d, %d) results, want (0, 0)", doneX, doneY)
		}
	})

	t.Run("open another repository", func(t *testing.T) {
		repoA, ex := newReadCancelFixture(t, "rc-open-a")
		repoB := newGitRepo(t, "rc-open-b")
		repoB.CommitFile("base.txt", "base\n", "initial commit")
		s, q := newReadCancelSession(t, localFactory(ex))
		s.Open(repoA.StoreRepo())
		q.Drain(t)

		var done int
		startProbeRead(t, s, "scope-x", "xa", &done)
		call := ex.probeCall(t, readProbeToken+"xa", 0)

		s.Open(repoB.StoreRepo())
		waitProbeCtxDone(t, call, "read after opening another repository", time.Second)
		q.Drain(t)

		if done != 0 {
			t.Fatalf("the read of the replaced repository delivered %d results, want 0", done)
		}
	})
}

// A read finishing after its successor started neither cancels the
// successor nor removes its entry from the table: canceling the scope
// afterwards still stops the successor. The same holds when a repository
// switch in between restarted the scope's sequence numbers, so the
// successor's sequence is identical to the finished read's.
func TestReadCancelLateFinishKeepsSuccessor(t *testing.T) {
	t.Run("same repository", func(t *testing.T) {
		repo, ex := newReadCancelFixture(t, "rc-late")
		s, q := newReadCancelSession(t, localFactory(ex))
		s.Open(repo.StoreRepo())
		q.Drain(t)

		var done int
		startProbeRead(t, s, "scope-x", "xa", &done)
		callA := ex.probeCall(t, readProbeToken+"xa", 0)
		startProbeRead(t, s, "scope-x", "xb", &done)
		callB := ex.probeCall(t, readProbeToken+"xb", 0)

		// The superseded read's worker is already dead; its completion
		// callback is the only one pending and runs now, after the
		// successor's entry is in the table.
		callA.waitDead(t, 5*time.Second)
		q.RunNext(t)

		if s.readCancels["scope-x"] == nil {
			t.Fatal("the late finish removed the successor's entry from readCancels")
		}
		if callB.ctx.Err() != nil {
			t.Fatalf("the late finish canceled the successor: %v", callB.ctx.Err())
		}

		s.cancelRead("scope-x")
		waitProbeCtxDone(t, callB, "successor canceled through cancelRead", time.Second)
	})

	t.Run("sequence restarts after a switch", func(t *testing.T) {
		repoA, ex := newReadCancelFixture(t, "rc-late-switch-a")
		repoB := newGitRepo(t, "rc-late-switch-b")
		repoB.CommitFile("base.txt", "base\n", "initial commit")
		s, q := newReadCancelSession(t, localFactory(ex))
		s.Open(repoA.StoreRepo())
		q.Drain(t)

		var done int
		startProbeRead(t, s, "scope-x", "xa", &done)
		callA := ex.probeCall(t, readProbeToken+"xa", 0)

		// Open cancels the read and restarts every scope's numbering, so
		// the successor started below carries the same sequence the finished
		// read had. Nothing is drained yet: its completion stays queued.
		s.Open(repoB.StoreRepo())
		startProbeRead(t, s, "scope-x", "xb", &done)
		callB := ex.probeCall(t, readProbeToken+"xb", 0)
		if s.readSeq["scope-x"] != 1 {
			t.Fatalf("the successor's sequence = %d, want 1 after the switch reset", s.readSeq["scope-x"])
		}

		// Two completions are pending in some order: the superseded read's
		// and the new repository's snapshot load. Running both guarantees
		// the late finish of the first read ran with the successor's entry
		// already in the table.
		callA.waitDead(t, 5*time.Second)
		q.RunNext(t)
		q.RunNext(t)

		if s.readCancels["scope-x"] == nil {
			t.Fatal("the late finish removed the successor's entry from readCancels")
		}
		if callB.ctx.Err() != nil {
			t.Fatalf("the late finish canceled the successor: %v", callB.ctx.Err())
		}

		s.cancelRead("scope-x")
		waitProbeCtxDone(t, callB, "successor canceled through cancelRead", time.Second)
	})
}

// A read releases its context and leaves the table of cancel functions when
// it ends, so fifty completed reads leave the table empty.
func TestReadCancelReleasesEntries(t *testing.T) {
	repo, ex := newReadCancelFixture(t, "rc-release")
	s, q := newReadCancelSession(t, localFactory(ex))
	s.Open(repo.StoreRepo())
	q.Drain(t)

	var done int
	for i := 0; i < 50; i++ {
		token := fmt.Sprintf("r%02d", i)
		startProbeRead(t, s, "scope-x", token, &done)
		call := ex.probeCall(t, readProbeToken+token, 0)
		close(call.release)
		q.Drain(t)
	}
	if done != 50 {
		t.Fatalf("delivered results = %d, want 50", done)
	}
	if len(s.readCancels) != 0 {
		t.Fatalf("readCancels holds %d entries after fifty finished reads, want 0", len(s.readCancels))
	}
}

// Closing the file inspector cancels its in-flight blame read, and a new
// File History read of the inspected file stops the one before it.
func TestReadCancelFileInspector(t *testing.T) {
	repo := newGitRepo(t, "rc-inspector")
	repo.CommitFile("readcancelprobe-blame.txt", "one\n", "initial commit")
	repo.CommitFile("readcancelprobe-hist.txt", "one\ntwo\n", "second commit")
	ex := &readProbeExecutor{delegate: &git.LocalExecutor{Dir: repo.Path()}}

	h := newTestApp(t)
	h.app.openRepoFn = func(store.Repo) *git.Repo { return git.New(ex) }
	h.openRepo(repo.StoreRepo())
	h.drain()

	// The blame read of the inspector blocks in flight; closing the
	// inspector cancels its context within a second.
	h.app.openFileInspector(pageChanges, inspectorModeBlame, "readcancelprobe-blame.txt", "HEAD")
	blameCall := ex.probeCall(t, readProbeToken+"blame.txt", 0)
	h.app.closeFileInspector()
	waitProbeCtxDone(t, blameCall, "blame read after closing the inspector", time.Second)

	// A File History read blocks in flight; the next read of the file —
	// as loading more of the walk starts one — stops it, while the new
	// read keeps running.
	h.app.openFileInspector(pageChanges, inspectorModeHistory, "readcancelprobe-hist.txt", "HEAD")
	firstLog := ex.probeCall(t, readProbeToken+"hist.txt", 0)
	h.app.startFileHistoryLogLoad()
	secondLog := ex.probeCall(t, readProbeToken+"hist.txt", 1)
	waitProbeCtxDone(t, firstLog, "superseded file history read", time.Second)
	if secondLog.ctx.Err() != nil {
		t.Fatalf("the new file history read was canceled: %v", secondLog.ctx.Err())
	}

	// Switching the inspected file cancels the read in flight as well.
	h.app.openFileInspector(pageChanges, inspectorModeHistory, "readcancelprobe-blame.txt", "HEAD")
	waitProbeCtxDone(t, secondLog, "file history read after switching files", time.Second)

	// The read the last open started is still blocked; closing the inspector
	// cancels it so no worker outlives the test.
	thirdLog := ex.probeCall(t, readProbeToken+"blame.txt", 1)
	h.app.closeFileInspector()
	waitProbeCtxDone(t, thirdLog, "file history read after closing the inspector again", time.Second)
}

// --- SSH helpers: process table scanning on this machine, which a loopback
// server shares with the test. ---

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

// serverCmdLine reports whether a ps line belongs to a command running on
// the server: it carries the probe token, and none of the ssh client
// options that only the local ssh command lines carry.
func serverCmdLine(line, token string) bool {
	return strings.Contains(line, token) && !strings.Contains(line, "ControlPath")
}

// findServerGroupLeader is the process group of the server-side session
// leader whose command line holds the token, or 0. The leader is the
// process whose id is its group, which keeps the local ssh clients out:
// they carry the token in the remote command but belong to the test's own
// group.
func findServerGroupLeader(t *testing.T, token string) int {
	t.Helper()
	for _, e := range psEntries(t) {
		if e[0] == e[1] && serverCmdLine(e[2], token) {
			pg, err := strconv.Atoi(e[1])
			if err == nil {
				return pg
			}
		}
	}
	return 0
}

// countServerProcesses counts processes of the server whose command line
// carries the token: members of the remote session's process group.
func countServerProcesses(t *testing.T, token string) int {
	t.Helper()
	n := 0
	for _, e := range psEntries(t) {
		if serverCmdLine(e[2], token) {
			n++
		}
	}
	return n
}

// countLocalClients counts the local ssh client processes still carrying
// the token in their remote command argument.
func countLocalClients(t *testing.T, token string) int {
	t.Helper()
	n := 0
	for _, e := range psEntries(t) {
		if strings.Contains(e[2], token) && strings.Contains(e[2], "ControlPath") {
			n++
		}
	}
	return n
}

func groupAlive(pg int) bool {
	return syscall.Kill(-pg, 0) == nil
}

// waitServerGroupLeader passes once the server-side leader's group is
// known, failing after five seconds.
func waitServerGroupLeader(t *testing.T, token string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if pg := findServerGroupLeader(t, token); pg > 0 {
			return pg
		}
		if time.Now().After(deadline) {
			t.Fatalf("no server-side session leader with %q in its command line", token)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// waitServerGroupGone passes once no process of the group is left.
func waitServerGroupGone(t *testing.T, pg int, deadline time.Time) {
	t.Helper()
	for {
		if !groupAlive(pg) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server process group %d still alive past the deadline", pg)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// writeServerFakeGit writes the fake git for the server of a loopback
// target: a probe invocation writes a started file and sleeps half a
// minute as a child, keeping the probe argument in the shell's own command
// line, while everything else delegates to the real git of the server.
func writeServerFakeGit(t *testing.T, fakeDir, startedDir string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		for _, cand := range []string{"/usr/bin/git", "/opt/homebrew/bin/git"} {
			if st, statErr := os.Stat(cand); statErr == nil && !st.IsDir() {
				realGit = cand
				break
			}
		}
	}
	if realGit == "" {
		t.Fatal("no real git for the server fake to delegate to")
	}
	if err := os.MkdirAll(startedDir, 0o755); err != nil {
		t.Fatalf("mkdir started dir: %v", err)
	}
	script := "#!/bin/sh\n" +
		"for _a in \"$@\"; do\n" +
		"  case \"$_a\" in\n" +
		"    " + readProbeToken + "*)\n" +
		"      _tok=${_a#" + readProbeToken + "}\n" +
		"      touch " + git.ShellQuote(startedDir) + "/\"$_tok\"\n" +
		"      sleep 30 &\n" +
		"      wait\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	if err := os.MkdirAll(fakeDir, 0o755); err != nil {
		t.Fatalf("mkdir fake git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write server fake git: %v", err)
	}
}

// writeSSHWrapper writes the local ssh program of the SSH cancellation
// tests: it prepends the fake git's directory to the PATH of every remote
// command — recognized by the process-group marker only git commands carry
// — and logs one line per invocation for counting the server's kills.
func writeSSHWrapper(t *testing.T, innerSSH, fakeDir, logFile string) string {
	t.Helper()
	injection := "PATH='" + fakeDir + "':\\$PATH; export PATH; "
	// The arguments are rebuilt by rotation: each pass consumes the front
	// and appends it back, possibly rewritten, so after exactly as many
	// passes as there are arguments the order is the original one. Only
	// the last argument — the remote command — is rewritten, and only when
	// it is a git invocation, recognized by its process-group marker.
	script := "#!/bin/sh\n" +
		"_n=$#\n" +
		"_i=0\n" +
		"while [ \"$_i\" -lt \"$_n\" ]; do\n" +
		"  _a=$1\n" +
		"  shift\n" +
		"  _i=$((_i + 1))\n" +
		"  if [ \"$_i\" = \"$_n\" ]; then\n" +
		"    case \"$_a\" in\n" +
		"      *branchline-pgid*) _a=\"" + injection + "$_a\" ;;\n" +
		"    esac\n" +
		"  fi\n" +
		"  set -- \"$@\" \"$_a\"\n" +
		"done\n" +
		"_line=INV\n" +
		"for _a in \"$@\"; do _line=\"$_line $_a\"; done\n" +
		"printf '%s\\n' \"$_line\" >> " + git.ShellQuote(logFile) + "\n" +
		"exec " + git.ShellQuote(innerSSH) + " \"$@\"\n"
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ssh")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh wrapper: %v", err)
	}
	return wrapper
}

// countKillLines counts the wrapper log lines of the server's process-group
// kill commands.
func countKillLines(t *testing.T, logFile string) int {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read ssh wrapper log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "kill -s TERM") {
			n++
		}
	}
	return n
}

// sshReadCancelTarget starts a loopback server with the fake git on its
// PATH and returns the session opened on it, plus the pieces the tests
// measure with.
type sshReadCancelTarget struct {
	srv        *sshtest.Target
	session    *repoSession
	queue      *uiQueue
	startedDir string
	logFile    string
}

func newSSHReadCancelTarget(t *testing.T, name string) *sshReadCancelTarget {
	t.Helper()
	srv := sshtest.StartLoopback(t)
	remoteDir := srv.InitRepo(t, name)

	fakeDir := filepath.Join(t.TempDir(), "fakebin")
	startedDir := filepath.Join(t.TempDir(), "started")
	writeServerFakeGit(t, fakeDir, startedDir)
	logFile := filepath.Join(t.TempDir(), "ssh-invocations.log")
	wrapper := writeSSHWrapper(t, srv.SSHProgram(), fakeDir, logFile)

	meta, err := store.ParseAddress(srv.Address(remoteDir))
	if err != nil {
		t.Fatalf("parse loopback address: %v", err)
	}
	meta.Identity = srv.Identity

	factory := func(store.Repo) *git.Repo {
		return git.New(&git.SSHExecutor{
			Host:           srv.Host,
			Port:           srv.Port,
			User:           srv.User,
			Identity:       srv.Identity,
			Dir:            remoteDir,
			SSH:            wrapper,
			NoGlobalConfig: true,
		})
	}
	s, q := newReadCancelSession(t, factory)
	s.Open(meta)
	q.Drain(t)
	if err := s.LoadErr(); err != nil {
		t.Fatalf("snapshot load over SSH failed: %v", err)
	}
	return &sshReadCancelTarget{srv: srv, session: s, queue: q, startedDir: startedDir, logFile: logFile}
}

// waitStarted passes once the server-side fake git wrote the started file
// of the token.
func (x *sshReadCancelTarget) waitStarted(t *testing.T, token string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(x.startedDir, token)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server never started the probe %q", token)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Over SSH a superseded read stops the git command on the server: starting
// another read of the same scope ends the process group of a command
// sleeping half a minute within ten seconds.
func TestReadCancelSSH(t *testing.T) {
	x := newSSHReadCancelTarget(t, "rc-ssh")
	s, q := x.session, x.queue

	tokenA := "ssha"
	var done int
	startProbeRead(t, s, "scope-x", tokenA, &done)
	x.waitStarted(t, tokenA)
	pg := waitServerGroupLeader(t, readProbeToken+tokenA)

	startProbeRead(t, s, "scope-x", "sshb", &done)
	waitServerGroupGone(t, pg, time.Now().Add(10*time.Second))

	// The superseding read is unaffected: canceling the scope stops it too,
	// proving the cancellation reaches the server and not just the discard.
	tokenB := "sshb"
	x.waitStarted(t, tokenB)
	pgB := waitServerGroupLeader(t, readProbeToken+tokenB)
	s.cancelRead("scope-x")
	waitServerGroupGone(t, pgB, time.Now().Add(10*time.Second))

	q.Drain(t)
	if done != 0 {
		t.Fatalf("the superseded reads delivered %d results, want 0", done)
	}
	t.Logf("process-group kills sent to the server: %d", countKillLines(t, x.logFile))
}

// Twenty reads of one scope started in quick succession leave no processes
// behind: locally every superseded command dies with its context, and over
// SSH the server ends one process group per canceled command.
func TestReadCancelRapidSupersede(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		repo := newGitRepo(t, "rc-rapid-local")
		repo.CommitFile("base.txt", "base\n", "initial commit")
		fake := writeLocalFakeGit(t)
		ex := &git.LocalExecutor{Dir: repo.Path(), Git: fake}
		s, q := newReadCancelSession(t, func(store.Repo) *git.Repo { return git.New(ex) })
		s.Open(repo.StoreRepo())
		q.Drain(t)

		var done int
		for i := 0; i < 20; i++ {
			token := fmt.Sprintf("rapidloc-%02d", i)
			startProbeRead(t, s, "scope-x", token, &done)
		}
		// The last read is still in flight; canceling the scope ends it.
		s.cancelRead("scope-x")
		q.Drain(t)

		deadline := time.Now().Add(5 * time.Second)
		for {
			n := countLocalProbeProcesses(t, "rapidloc-")
			if n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d local probe processes linger after the rapid supersede", n)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Logf("local rapid supersede: no probe processes left after cancellation")
	})

	t.Run("ssh", func(t *testing.T) {
		x := newSSHReadCancelTarget(t, "rc-rapid-ssh")
		s, q := x.session, x.queue

		var done int
		for i := 0; i < 20; i++ {
			token := fmt.Sprintf("rapidssh%02d", i)
			startProbeRead(t, s, "scope-x", token, &done)
			// Waiting for the server-side start keeps the supersede behind
			// the process group's naming, the ordering a user switching
			// files quickly produces.
			x.waitStarted(t, token)
		}
		// The first nineteen were superseded in flight; the last is still
		// running. Canceling the scope ends it too.
		s.cancelRead("scope-x")
		q.Drain(t)

		deadline := time.Now().Add(10 * time.Second)
		for {
			n := countServerProcesses(t, readProbeToken+"rapidssh")
			if n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d server probe processes linger after the rapid supersede", n)
			}
			time.Sleep(25 * time.Millisecond)
		}
		kills := countKillLines(t, x.logFile)
		clients := countLocalClients(t, readProbeToken+"rapidssh")
		t.Logf("ssh rapid supersede of 20 reads: server probe processes left = 0, "+
			"local ssh clients left = %d, process-group kills sent = %d", clients, kills)
		if kills < 19 {
			t.Fatalf("server received %d process-group kills, want at least 19 for the superseded reads", kills)
		}
	})
}

// countLocalProbeProcesses counts processes on this machine whose command
// line carries a probe token with the given suffix.
func countLocalProbeProcesses(t *testing.T, suffix string) int {
	t.Helper()
	n := 0
	for _, e := range psEntries(t) {
		if strings.Contains(e[2], readProbeToken+suffix) {
			n++
		}
	}
	return n
}

// writeLocalFakeGit writes the fake git of the local rapid-supersede test:
// probe invocations sleep half a minute as a child of the shell carrying
// the probe argument, everything else delegates to the real git.
func writeLocalFakeGit(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		realGit = "/usr/bin/git"
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for _a in \"$@\"; do\n" +
		"  case \"$_a\" in\n" +
		"    " + readProbeToken + "*)\n" +
		"      sleep 30 &\n" +
		"      wait\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write local fake git: %v", err)
	}
	return fake
}
