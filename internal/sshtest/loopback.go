package sshtest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

// loopbackDeps allows unit tests in package sshtest to test skip/fail policy
// without mutating process-wide PATH or system binaries.
type loopbackDeps struct {
	findSSHD      func() (string, error)
	findSSH       func() (string, error)
	findSSHKeygen func() (string, error)
	findSH        func() (string, error)
	listen        func(network, address string) (net.Listener, error)
}

func defaultLoopbackDeps() loopbackDeps {
	return loopbackDeps{
		findSSHD: defaultFindSSHD,
		findSSH: func() (string, error) {
			return lookUpTool("ssh", "/usr/bin/ssh", "/opt/homebrew/bin/ssh", "/usr/local/bin/ssh")
		},
		findSSHKeygen: func() (string, error) {
			return lookUpTool("ssh-keygen", "/usr/bin/ssh-keygen", "/opt/homebrew/bin/ssh-keygen")
		},
		findSH: func() (string, error) { return lookUpTool("sh", "/bin/sh") },
		listen: net.Listen,
	}
}

func defaultFindSSHD() (string, error) {
	return lookUpTool("sshd", "/usr/sbin/sshd", "/sbin/sshd")
}

func lookUpTool(name string, candidates ...string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH or standard paths", name)
}

// StartLoopback starts a disposable sshd listening on a free port of 127.0.0.1
// with fresh Ed25519 host and user keys and fully isolated SSH configuration
// (never reading or modifying ~/.ssh or ssh-agent).
//
// If sshd cannot be started on this machine, StartLoopback calls t.Skip with
// the reason by default, or t.Fatal when BRANCHLINE_REQUIRE_SSH=1 is set.
func StartLoopback(t testing.TB) *Target {
	t.Helper()
	return startLoopback(t, defaultLoopbackDeps())
}

func startLoopback(t testing.TB, deps loopbackDeps) *Target {
	t.Helper()

	requireSSH := isEnvTrue(EnvRequireSSH)
	skipOrFail := func(format string, args ...any) {
		t.Helper()
		if requireSSH {
			t.Fatalf(EnvRequireSSH+"=1: "+format, args...)
		}
		t.Skipf(format, args...)
	}

	sshdBin, err := deps.findSSHD()
	if err != nil {
		skipOrFail("cannot start loopback sshd: %v", err)
		return nil
	}
	sshBin, err := deps.findSSH()
	if err != nil {
		skipOrFail("cannot start loopback sshd: %v", err)
		return nil
	}
	keygenBin, err := deps.findSSHKeygen()
	if err != nil {
		skipOrFail("cannot start loopback sshd: %v", err)
		return nil
	}
	shBin, err := deps.findSH()
	if err != nil {
		skipOrFail("cannot start loopback sshd: %v", err)
		return nil
	}

	curUser, err := user.Current()
	if err != nil {
		skipOrFail("cannot resolve current user for loopback sshd: %v", err)
		return nil
	}

	baseDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(baseDir); err == nil {
		baseDir = resolved
	}
	if err := os.Chmod(baseDir, 0o700); err != nil {
		t.Fatalf("chmod loopback base dir: %v", err)
	}
	rootDir := filepath.Join(baseDir, "repos")
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatalf("mkdir loopback repos dir: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir loopback bin dir: %v", err)
	}

	hostKey := filepath.Join(baseDir, "host_ed25519")
	userKey := filepath.Join(baseDir, "user_ed25519")
	for _, k := range []string{hostKey, userKey} {
		if out, err := exec.Command(keygenBin, "-q", "-t", "ed25519", "-N", "", "-f", k).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen %s: %v\n%s", filepath.Base(k), err, out)
		}
	}
	userPub, err := os.ReadFile(userKey + ".pub")
	if err != nil {
		t.Fatalf("read user public key: %v", err)
	}
	authKeys := filepath.Join(baseDir, "authorized_keys")
	if err := os.WriteFile(authKeys, userPub, 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	hostPub, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatalf("read host public key: %v", err)
	}
	hostPubFields := strings.Fields(string(hostPub))
	if len(hostPubFields) < 2 {
		t.Fatalf("unexpected host public key format: %q", string(hostPub))
	}
	hostKeyLine := hostPubFields[0] + " " + hostPubFields[1]

	controlPathsLog := filepath.Join(baseDir, "control_paths.log")
	if err := os.WriteFile(controlPathsLog, nil, 0o600); err != nil {
		t.Fatalf("init control_paths.log: %v", err)
	}

	// Retry up to 3 times in case another process grabs the ephemeral port
	// between net.Listen().Close() and sshd binding it.
	const maxAttempts = 3
	var lastErr string
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		ln, err := deps.listen("tcp", "127.0.0.1:0")
		if err != nil {
			skipOrFail("cannot bind loopback port for sshd: %v", err)
			return nil
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()

		knownHosts := filepath.Join(baseDir, "known_hosts")
		knownContent := fmt.Sprintf("[127.0.0.1]:%d %s\n127.0.0.1 %s\n", port, hostKeyLine, hostKeyLine)
		if err := os.WriteFile(knownHosts, []byte(knownContent), 0o600); err != nil {
			t.Fatalf("write known_hosts: %v", err)
		}

		sshConfig := filepath.Join(baseDir, "ssh_config")
		clientCfg := fmt.Sprintf(strings.Join([]string{
			"Host *",
			"    UserKnownHostsFile %q",
			"    GlobalKnownHostsFile /dev/null",
			"    StrictHostKeyChecking yes",
			"    IdentityAgent none",
			"    IdentitiesOnly yes",
			"    LogLevel ERROR",
			"",
		}, "\n"), knownHosts)
		if err := os.WriteFile(sshConfig, []byte(clientCfg), 0o600); err != nil {
			t.Fatalf("write ssh_config: %v", err)
		}

		wrapper := filepath.Join(binDir, "ssh")
		wrapperScript := buildLoopbackSSHWrapper(shBin, sshBin, sshConfig, userKey, controlPathsLog)
		if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o755); err != nil {
			t.Fatalf("write ssh wrapper: %v", err)
		}

		pidFile := filepath.Join(baseDir, "sshd.pid")
		sshdConfig := filepath.Join(baseDir, "sshd_config")
		serverLines := []string{
			fmt.Sprintf("Port %d", port),
			"ListenAddress 127.0.0.1",
			fmt.Sprintf("HostKey %q", hostKey),
			fmt.Sprintf("AuthorizedKeysFile %q", authKeys),
			fmt.Sprintf("PidFile %q", pidFile),
			"StrictModes no",
			"UsePAM no",
			"PubkeyAuthentication yes",
			"PasswordAuthentication no",
			"KbdInteractiveAuthentication no",
			"MaxStartups 64:30:128",
			"LogLevel ERROR",
		}
		if sshdSupportsPerSourcePenalties(sshdBin, hostKey) {
			serverLines = append(serverLines, "PerSourcePenalties no")
		}
		serverLines = append(serverLines, "")
		serverCfg := strings.Join(serverLines, "\n")
		if err := os.WriteFile(sshdConfig, []byte(serverCfg), 0o600); err != nil {
			t.Fatalf("write sshd_config: %v", err)
		}

		var stderrBuf syncBuffer
		cmd := exec.Command(sshdBin, "-D", "-e", "-f", sshdConfig)
		cmd.Stdout = &stderrBuf
		cmd.Stderr = &stderrBuf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			skipOrFail("failed to start loopback sshd: %v", err)
			return nil
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()

		ready := false
		exitedEarly := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-exited:
				lastErr = fmt.Sprintf("sshd exited early on port %d: %s", port, strings.TrimSpace(stderrBuf.String()))
				exitedEarly = true
			default:
			}
			if exitedEarly {
				break
			}
			conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if dialErr == nil {
				_ = conn.Close()
				ready = true
				break
			}
			time.Sleep(25 * time.Millisecond)
		}

		if exitedEarly {
			if !strings.Contains(strings.ToLower(lastErr), "address already in use") {
				break
			}
			continue
		}

		if !ready {
			terminateProcessGroup(cmd.Process.Pid, cmd.Process, exited)
			lastErr = fmt.Sprintf("sshd did not accept TCP connections on 127.0.0.1:%d within 5s: %s", port, strings.TrimSpace(stderrBuf.String()))
			continue
		}

		target := &Target{
			Kind:            KindLoopback,
			Host:            "127.0.0.1",
			Port:            port,
			User:            curUser.Username,
			Identity:        userKey,
			SSH:             wrapper,
			KnownHosts:      knownHosts,
			RootDir:         rootDir,
			PID:             cmd.Process.Pid,
			sshBin:          sshBin,
			sshConfig:       sshConfig,
			baseDir:         baseDir,
			authorizedKeys:  authKeys,
			controlPathsLog: controlPathsLog,
			controlPaths:    make(map[string]struct{}),
			cmd:             cmd,
			exited:          exited,
		}

		// Track default ControlPaths immediately so even if callers only use
		// Address()+SSHProgram() without calling Executor(), cleanup sees them.
		target.trackControlPath((&git.SSHExecutor{Host: target.Host, Port: target.Port, User: target.User, Identity: target.Identity, Dir: target.RootDir}).ControlPath())
		target.trackControlPath((&git.SSHExecutor{Host: target.Host, Port: target.Port, User: target.User, Dir: target.RootDir}).ControlPath())

		// Verify an actual authenticated SSH handshake succeeds.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := target.probeExecutor().RunShell(ctx, "printf ready")
		cancel()
		if err != nil || string(out) != "ready" {
			_ = target.close()
			lastErr = fmt.Sprintf("loopback sshd handshake failed on 127.0.0.1:%d: err=%v out=%q sshd_stderr=%q",
				port, err, string(out), strings.TrimSpace(stderrBuf.String()))
			continue
		}

		t.Cleanup(func() {
			if err := target.close(); err != nil {
				t.Errorf("sshtest loopback cleanup: %v", err)
			}
		})
		return target
	}

	skipOrFail("unable to start loopback sshd after %d attempts: %s", attempts, lastErr)
	return nil
}

// AddAuthorizedKey appends an additional SSH public key line to a loopback
// target's authorized_keys file.
func (s *Target) AddAuthorizedKey(t testing.TB, pubKey []byte) {
	t.Helper()
	if s.Kind != KindLoopback || s.authorizedKeys == "" {
		t.Fatal("sshtest AddAuthorizedKey is only supported on loopback targets")
	}
	line := strings.TrimSpace(string(pubKey))
	if line == "" {
		t.Fatal("sshtest AddAuthorizedKey: empty public key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.authorizedKeys, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open authorized_keys: %v", err)
	}
	_, writeErr := f.WriteString(line + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatalf("append authorized_keys: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close authorized_keys: %v", closeErr)
	}
}

// GenerateKey creates a new Ed25519 key pair inside the loopback target's
// temporary directory (optionally protected with passphrase), authorizes its
// public key on the loopback sshd, and returns the private key path.
func (s *Target) GenerateKey(t testing.TB, name, passphrase string) string {
	t.Helper()
	if s.Kind != KindLoopback || s.baseDir == "" {
		t.Fatal("sshtest GenerateKey is only supported on loopback targets")
	}
	cleanName, err := sanitizePrefix(name)
	if err != nil {
		t.Fatalf("sshtest GenerateKey(%q): %v", name, err)
	}
	keygenBin, err := lookUpTool("ssh-keygen", "/usr/bin/ssh-keygen", "/opt/homebrew/bin/ssh-keygen")
	if err != nil {
		t.Fatalf("ssh-keygen not found: %v", err)
	}
	keyPath := filepath.Join(s.baseDir, "key-"+cleanName)
	if out, err := exec.Command(keygenBin, "-q", "-t", "ed25519", "-N", passphrase, "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen %s: %v\n%s", cleanName, err, out)
	}
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read generated public key: %v", err)
	}
	s.AddAuthorizedKey(t, pub)
	s.trackControlPath((&git.SSHExecutor{
		Host:     s.Host,
		Port:     s.Port,
		User:     s.User,
		Identity: keyPath,
		Dir:      s.RootDir,
	}).ControlPath())
	return keyPath
}

var perSourcePenaltiesSupport = struct {
	mu sync.Mutex
	ok map[string]bool
}{ok: make(map[string]bool)}

// sshdSupportsPerSourcePenalties reports whether sshdBin recognizes the
// OpenSSH 9.8+ PerSourcePenalties directive. Loopback tests intentionally
// trigger repeated failed authentications from 127.0.0.1 (locked keys, wrong
// passphrases, cancelled askpass), which would otherwise trip OpenSSH's default
// 15s penalty threshold and reset subsequent test or cleanup connections.
func sshdSupportsPerSourcePenalties(sshdBin, hostKey string) bool {
	perSourcePenaltiesSupport.mu.Lock()
	if supported, cached := perSourcePenaltiesSupport.ok[sshdBin]; cached {
		perSourcePenaltiesSupport.mu.Unlock()
		return supported
	}
	perSourcePenaltiesSupport.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshdBin, "-t", "-f", os.DevNull, "-h", hostKey, "-o", "PerSourcePenalties=no")
	supported := cmd.Run() == nil

	perSourcePenaltiesSupport.mu.Lock()
	perSourcePenaltiesSupport.ok[sshdBin] = supported
	perSourcePenaltiesSupport.mu.Unlock()
	return supported
}

func buildLoopbackSSHWrapper(shBin, sshBin, sshConfig, defaultIdentity, controlPathsLog string) string {
	return "#!" + shBin + "\n" +
		"_has_id=0\n" +
		"_prev=\"\"\n" +
		"for _arg in \"$@\"; do\n" +
		"  case \"$_prev\" in\n" +
		"    -i) _has_id=1 ;;\n" +
		"    -o)\n" +
		"      case \"$_arg\" in\n" +
		"        ControlPath=*|controlpath=*)\n" +
		"          _cp=${_arg#*=}\n" +
		"          if [ -n \"$_cp\" ] && [ \"$_cp\" != \"none\" ]; then\n" +
		"            printf '%s\\n' \"$_cp\" >> " + git.ShellQuote(controlPathsLog) + "\n" +
		"          fi\n" +
		"          ;;\n" +
		"        IdentityFile=*|identityfile=*)\n" +
		"          _has_id=1\n" +
		"          ;;\n" +
		"      esac\n" +
		"      ;;\n" +
		"  esac\n" +
		"  case \"$_arg\" in\n" +
		"    -i|-i*) _has_id=1 ;;\n" +
		"    -oControlPath=*|-ocontrolpath=*)\n" +
		"      _cp=${_arg#*=}\n" +
		"      if [ -n \"$_cp\" ] && [ \"$_cp\" != \"none\" ]; then\n" +
		"        printf '%s\\n' \"$_cp\" >> " + git.ShellQuote(controlPathsLog) + "\n" +
		"      fi\n" +
		"      ;;\n" +
		"    -oIdentityFile=*|-oidentityfile=*)\n" +
		"      _has_id=1\n" +
		"      ;;\n" +
		"  esac\n" +
		"  _prev=$_arg\n" +
		"done\n" +
		"if [ \"$_has_id\" = \"1\" ]; then\n" +
		"  exec " + git.ShellQuote(sshBin) + " -F " + git.ShellQuote(sshConfig) + " \"$@\"\n" +
		"fi\n" +
		"exec " + git.ShellQuote(sshBin) + " -F " + git.ShellQuote(sshConfig) + " -i " + git.ShellQuote(defaultIdentity) + " -o IdentitiesOnly=yes \"$@\"\n"
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
