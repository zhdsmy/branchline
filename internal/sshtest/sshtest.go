// Package sshtest provides isolated SSH test targets for Branchline tests
// without depending on the UI framework.
//
// Tests can target either a disposable loopback sshd on 127.0.0.1
// (StartLoopback) or a configured Linux SSH host such as an sshlab container
// (ConfiguredLinux). Both return *Target with identical helpers for creating
// disposable remote repositories, running server-side Git commands, and
// building git.SSHExecutor / git.Repo / SSH addresses.
package sshtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

const (
	// EnvRequireSSH forces StartLoopback to fail instead of skipping when a
	// local loopback sshd cannot be started.
	EnvRequireSSH = "BRANCHLINE_REQUIRE_SSH"
	// EnvRequireLinux forces ConfiguredLinux to fail instead of skipping when
	// BRANCHLINE_LINUX_SSH_HOST / BRANCHLINE_LINUX_SSH_REPO are not configured.
	EnvRequireLinux = "BRANCHLINE_REQUIRE_LINUX"
	// EnvLinuxHost names the SSH host or config alias of the Linux test server.
	EnvLinuxHost = "BRANCHLINE_LINUX_SSH_HOST"
	// EnvLinuxRepo is the disposable remote base directory (must start with
	// /tmp/branchline-test-) on the Linux test server.
	EnvLinuxRepo = "BRANCHLINE_LINUX_SSH_REPO"

	// KindLoopback identifies a Target backed by a local loopback sshd.
	KindLoopback = "loopback"
	// KindConfiguredLinux identifies a Target backed by a configured Linux SSH host.
	KindConfiguredLinux = "configured-linux"
)

// Target is an isolated SSH server environment for tests.
// All resources started or created through Target are cleaned up via t.Cleanup.
type Target struct {
	// Kind is KindLoopback or KindConfiguredLinux.
	Kind string
	// Host is the SSH host or config alias (for example "127.0.0.1" or "bl-sshlab-e1").
	Host string
	// Port is the SSH port (non-zero for loopback, 0 when supplied by ssh config).
	Port int
	// User is the SSH login user (empty when supplied by ssh config).
	User string
	// Identity is the disposable private key file for loopback targets (empty when
	// supplied by ssh config).
	Identity string
	// SSH is the isolated ssh wrapper program path for loopback targets (empty for
	// configured-linux targets, which resolve ssh from PATH).
	SSH string
	// KnownHosts is the isolated known_hosts file path, when known.
	KnownHosts string
	// RootDir is the base directory on the server under which disposable test
	// repositories are created.
	RootDir string
	// PID is the local sshd process ID for loopback targets (0 for configured-linux).
	PID int

	mu              sync.Mutex
	sshBin          string
	sshConfig       string
	baseDir         string
	authorizedKeys  string
	controlPathsLog string
	controlPaths    map[string]struct{}
	cmd             *exec.Cmd
	exited          chan struct{}
	closed          bool
	closeOnce       sync.Once
	closeErr        error
}

// Executor returns a git.SSHExecutor configured to run git in remoteDir on s.
// Its ControlMaster socket is tracked and closed during s's cleanup.
func (s *Target) Executor(t testing.TB, remoteDir string) *git.SSHExecutor {
	t.Helper()
	ex := &git.SSHExecutor{
		Host:           s.Host,
		Port:           s.Port,
		User:           s.User,
		Identity:       s.Identity,
		Dir:            remoteDir,
		SSH:            s.SSH,
		NoGlobalConfig: true,
	}
	s.trackControlPath(ex.ControlPath())
	// Also track the identity-less ControlPath in case callers open the same
	// server via Address(remoteDir) without setting Identity explicitly.
	noID := *ex
	noID.Identity = ""
	s.trackControlPath(noID.ControlPath())
	return ex
}

// Repo returns a *git.Repo connected to remoteDir on s.
func (s *Target) Repo(t testing.TB, remoteDir string) *git.Repo {
	t.Helper()
	return git.New(s.Executor(t, remoteDir))
}

// Address returns an SSH repository address for remoteDir in the format
// accepted by store.ParseAddress: ssh://user@host:port/path when Port > 0, or
// [user@]host:path when Port == 0.
func (s *Target) Address(remoteDir string) string {
	host := s.Host
	if s.User != "" {
		host = s.User + "@" + host
	}
	if s.Port > 0 {
		p := remoteDir
		if strings.HasPrefix(p, "~") {
			p = "/" + p
		} else if p != "" && !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return fmt.Sprintf("ssh://%s:%d%s", host, s.Port, p)
	}
	return host + ":" + remoteDir
}

// SSHProgram returns the isolated ssh wrapper program path for loopback
// targets, or "" for configured-linux targets (which resolve ssh from PATH).
func (s *Target) SSHProgram() string {
	return s.SSH
}

// BinDir returns the directory containing the isolated ssh wrapper for loopback
// targets, or "" when SSHProgram is empty.
func (s *Target) BinDir() string {
	if s.SSH == "" {
		return ""
	}
	return filepath.Dir(s.SSH)
}

// ControlPaths returns all sorted, deduplicated ControlMaster socket paths
// tracked or observed for s.
func (s *Target) ControlPaths() []string {
	seen := make(map[string]struct{})
	s.mu.Lock()
	for cp := range s.controlPaths {
		if cp != "" {
			seen[cp] = struct{}{}
		}
	}
	logPath := s.controlPathsLog
	s.mu.Unlock()

	if logPath != "" {
		if data, err := os.ReadFile(logPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				cp := strings.TrimSpace(line)
				if cp != "" && !strings.EqualFold(cp, "none") {
					seen[cp] = struct{}{}
				}
			}
		}
	}

	paths := make([]string, 0, len(seen))
	for cp := range seen {
		paths = append(paths, cp)
	}
	sort.Strings(paths)
	return paths
}

// Close closes tracked ControlMaster connections and stops the loopback sshd
// process group (if any). It is idempotent and automatically registered with
// t.Cleanup when Target is created.
func (s *Target) Close() {
	_ = s.close()
}

func (s *Target) close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		var errs []string
		controlPaths := s.ControlPaths()
		dest := s.Host
		if s.User != "" {
			dest = s.User + "@" + s.Host
		}
		sshBin := s.sshBin
		if sshBin == "" {
			sshBin = findSSHProgram()
		}

		// 1. Gracefully exit every tracked ControlMaster connection while sshd is still alive.
		for _, cp := range controlPaths {
			if _, err := os.Stat(cp); errors.Is(err, os.ErrNotExist) {
				continue
			}
			closeControlSocket(sshBin, cp, dest)
			if !waitPathGone(cp, 2*time.Second) {
				errs = append(errs, fmt.Sprintf("control socket %s still existed after ssh -O exit", cp))
			}
		}

		// 2. Terminate loopback sshd and its entire process group.
		if s.cmd != nil && s.cmd.Process != nil {
			pid := s.PID
			if pid <= 0 {
				pid = s.cmd.Process.Pid
			}
			terminateProcessGroup(pid, s.cmd.Process, s.exited)
			if err := waitProcessGroupGone(pid, 3*time.Second); err != nil {
				errs = append(errs, fmt.Sprintf("loopback sshd process group %d: %v", pid, err))
			}
		}

		// 3. Re-check control sockets after sshd exit and clean up any dead socket file.
		var remaining []string
		for _, cp := range s.ControlPaths() {
			info, err := os.Stat(cp)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err == nil && info.Mode()&os.ModeSocket != 0 {
				// Re-attempt control exit in case another socket appeared before sshd died.
				closeControlSocket(sshBin, cp, dest)
				if waitPathGone(cp, time.Second) {
					continue
				}
				_ = os.Remove(cp)
				if _, statErr := os.Stat(cp); errors.Is(statErr, os.ErrNotExist) {
					continue
				}
			}
			remaining = append(remaining, cp)
		}
		if len(remaining) > 0 {
			errs = append(errs, fmt.Sprintf("leftover control sockets: %s", strings.Join(remaining, ", ")))
		}

		if len(errs) > 0 {
			s.closeErr = errors.New(strings.Join(errs, "; "))
		}
	})
	return s.closeErr
}

// RunShell executes a POSIX /bin/sh script on the server over SSH without
// multiplexing and fails the test on error, returning stdout as a string.
func (s *Target) RunShell(t testing.TB, script string) string {
	t.Helper()
	return string(s.RunShellInput(t, script, nil))
}

// RunShellInput executes a POSIX /bin/sh script on the server over SSH with
// optional stdin bytes and fails the test on error, returning raw stdout bytes.
func (s *Target) RunShellInput(t testing.TB, script string, stdin []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := s.probeExecutor().RunShellInput(ctx, "/bin/sh -c "+git.ShellQuote(script), stdin)
	if err != nil {
		t.Fatalf("sshtest RunShell failed: %v\nscript:\n%s\nstdout:\n%s", err, script, out)
	}
	return out
}

// RunGit executes git -C remoteDir <args...> directly on the server with
// isolated Git configuration and fails the test on error. Trailing CR/LF
// newlines are trimmed while leading whitespace (such as git status porcelain
// columns) is preserved.
func (s *Target) RunGit(t testing.TB, remoteDir string, args ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C git -C ")
	b.WriteString(git.ShellQuote(remoteDir))
	b.WriteString(" -c core.quotepath=false")
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(git.ShellQuote(a))
	}
	return strings.TrimRight(s.RunShell(t, b.String()), "\r\n")
}

// TempDir creates a disposable directory directly under s.RootDir on the server
// and registers t.Cleanup to remove it.
func (s *Target) TempDir(t testing.TB, prefix string) string {
	t.Helper()
	cleanPrefix, err := sanitizePrefix(prefix)
	if err != nil {
		t.Fatalf("sshtest TempDir(%q): %v", prefix, err)
	}
	tmpl := path.Join(s.RootDir, cleanPrefix+".XXXXXX")
	dir := strings.TrimSpace(s.RunShell(t, "mktemp -d "+git.ShellQuote(tmpl)))
	if path.Dir(dir) != s.RootDir || !strings.HasPrefix(path.Base(dir), cleanPrefix+".") {
		t.Fatalf("sshtest TempDir: unexpected remote path %q (root %q)", dir, s.RootDir)
	}
	t.Cleanup(func() {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			if s.Kind == KindLoopback {
				_ = os.RemoveAll(dir)
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := s.probeExecutor().RunShell(ctx, "rm -rf -- "+git.ShellQuote(dir)); err != nil {
			t.Errorf("sshtest cleanup remote dir %s: %v", dir, err)
		}
	})
	return dir
}

// InitRepo creates a disposable Git repository on the server on branch main
// with an initial commit ("initial commit", containing a.txt = "one\n") and
// isolated repo-local Git settings compatible with Git 2.25+.
func (s *Target) InitRepo(t testing.TB, name string) string {
	t.Helper()
	dir := s.TempDir(t, name)
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + git.ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Test'
git config user.email 'test@branchline.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
git config gc.auto 0
git config maintenance.auto false
printf 'one\n' > a.txt
chmod 644 a.txt
git add a.txt
git commit -q -m 'initial commit'
`
	s.RunShell(t, script)
	return dir
}

// InitBareRepo creates a disposable bare Git repository on the server with
// HEAD pointing to refs/heads/main, compatible with Git 2.25+.
func (s *Target) InitBareRepo(t testing.TB, name string) string {
	t.Helper()
	if strings.TrimSpace(name) == "" {
		name = "origin"
	}
	dir := s.TempDir(t, name+"-bare")
	script := `set -eu
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
cd ` + git.ShellQuote(dir) + `
git init -q --bare
git symbolic-ref HEAD refs/heads/main
git config core.hooksPath /dev/null
git config gc.auto 0
git config maintenance.auto false
`
	s.RunShell(t, script)
	return dir
}

// WriteFile writes content to relPath inside remoteDir on the server, creating
// parent directories as needed.
func (s *Target) WriteFile(t testing.TB, remoteDir, relPath string, content []byte) {
	t.Helper()
	full, err := joinRemoteRel(remoteDir, relPath)
	if err != nil {
		t.Fatalf("sshtest WriteFile(%q, %q): %v", remoteDir, relPath, err)
	}
	parent := path.Dir(full)
	script := "set -eu\nmkdir -p -- " + git.ShellQuote(parent) + "\ncat > " + git.ShellQuote(full) + "\n"
	s.RunShellInput(t, script, content)
}

// ReadFile reads the exact bytes of relPath inside remoteDir on the server.
func (s *Target) ReadFile(t testing.TB, remoteDir, relPath string) []byte {
	t.Helper()
	full, err := joinRemoteRel(remoteDir, relPath)
	if err != nil {
		t.Fatalf("sshtest ReadFile(%q, %q): %v", remoteDir, relPath, err)
	}
	return s.RunShellInput(t, "cat -- "+git.ShellQuote(full), nil)
}

func (s *Target) probeExecutor() *git.SSHExecutor {
	return &git.SSHExecutor{
		Host:           s.Host,
		Port:           s.Port,
		User:           s.User,
		Identity:       s.Identity,
		Dir:            s.RootDir,
		SSH:            s.SSH,
		NoMultiplex:    true,
		NoGlobalConfig: true,
	}
}

func (s *Target) trackControlPath(cp string) {
	if cp == "" || strings.EqualFold(cp, "none") {
		return
	}
	s.mu.Lock()
	if s.controlPaths == nil {
		s.controlPaths = make(map[string]struct{})
	}
	s.controlPaths[cp] = struct{}{}
	s.mu.Unlock()
}

func isEnvTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func sanitizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "repo", nil
	}
	if strings.ContainsAny(prefix, "/\\\n\r\x00 \t#?%") || prefix == "." || prefix == ".." {
		return "", fmt.Errorf("invalid prefix %q", prefix)
	}
	return prefix, nil
}

func joinRemoteRel(remoteDir, relPath string) (string, error) {
	if strings.TrimSpace(remoteDir) == "" {
		return "", errors.New("empty remoteDir")
	}
	if relPath == "" || strings.HasPrefix(relPath, "/") || strings.ContainsAny(relPath, "\n\r\x00") {
		return "", fmt.Errorf("invalid relative path %q", relPath)
	}
	cleaned := path.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("relative path %q escapes remote directory", relPath)
	}
	return path.Join(remoteDir, cleaned), nil
}

func findSSHProgram() string {
	if p, err := exec.LookPath("ssh"); err == nil {
		return p
	}
	for _, cand := range []string{"/usr/bin/ssh", "/opt/homebrew/bin/ssh", "/usr/local/bin/ssh"} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "ssh"
}

func closeControlSocket(sshBin, controlPath, destination string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshBin, "-F", os.DevNull, "-O", "exit", "-o", "ControlPath="+controlPath, destination)
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
}

func waitPathGone(p string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func terminateProcessGroup(pid int, proc *os.Process, exited <-chan struct{}) {
	// Signal the whole process group first so forked session sshd children also exit.
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	if proc != nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	if exited != nil {
		select {
		case <-exited:
			return
		case <-time.After(2 * time.Second):
		}
	}
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if proc != nil {
		_ = proc.Kill()
	}
	if exited != nil {
		<-exited
	}
}

func waitProcessGroupGone(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		procErr := syscall.Kill(pid, 0)
		pgErr := syscall.Kill(-pid, 0)
		if errors.Is(procErr, syscall.ESRCH) && errors.Is(pgErr, syscall.ESRCH) {
			return nil
		}
		if time.Now().After(deadline) {
			// Escalate once more before giving up.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return fmt.Errorf("still alive after %s (pidErr=%v pgErr=%v)", timeout, procErr, pgErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
