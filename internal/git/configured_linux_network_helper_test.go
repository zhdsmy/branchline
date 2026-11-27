package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type resolvedSSHTarget struct {
	hostAlias      string
	rootRepo       string
	sshlabEnv      string
	containerName  string
	sshConfigPath  string
	knownHostsPath string
	identityFile   string
	targetIP       string
	targetPort     int
	targetAddr     string
	remoteUser     string
	hostKeyAlias   string
	gitVersionRaw  string
	gitCaps        GitCapabilities
	directEx       *SSHExecutor
}

func fileSHA256OrMissing(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func resolveConfiguredLinuxSSHTarget(t *testing.T) *resolvedSSHTarget {
	t.Helper()
	host := strings.TrimSpace(os.Getenv("BRANCHLINE_LINUX_SSH_HOST"))
	root := strings.TrimSpace(os.Getenv("BRANCHLINE_LINUX_SSH_REPO"))
	if host == "" && root == "" {
		t.Skip("BRANCHLINE_LINUX_SSH_HOST and BRANCHLINE_LINUX_SSH_REPO are both required")
	}
	if host == "" || root == "" {
		t.Fatalf("both BRANCHLINE_LINUX_SSH_HOST (%q) and BRANCHLINE_LINUX_SSH_REPO (%q) must be set when configured", host, root)
	}
	if !strings.HasPrefix(root, "/tmp/branchline-test-") || path.Clean(root) != root || root == "/tmp/branchline-test-" || strings.ContainsAny(root, "\n\r") {
		t.Fatalf("refusing repository outside disposable /tmp/branchline-test-* prefix: %q", root)
	}

	sshlabEnv := strings.ToLower(strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_ENV")))
	containerName := strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_CONTAINER"))
	envPort := strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_PORT"))
	sshConfigPath := strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_SSH_CONFIG"))
	knownHostsEnv := strings.TrimSpace(os.Getenv("BRANCHLINE_SSHLAB_KNOWN_HOSTS"))

	for _, val := range []string{host, sshlabEnv, containerName, sshConfigPath} {
		if strings.ToLower(val) == "e4" {
			t.Fatalf("refusing fault test on external target (%q)", val)
		}
	}
	switch sshlabEnv {
	case "e1", "e2", "e3":
	default:
		t.Fatalf("BRANCHLINE_SSHLAB_ENV=%q must be e1, e2, or e3 for sshlab network fault testing", sshlabEnv)
	}
	if !strings.HasPrefix(host, "bl-") {
		t.Fatalf("BRANCHLINE_LINUX_SSH_HOST=%q must be an isolated bl-* sshlab alias", host)
	}
	if containerName != "" && host != containerName {
		t.Fatalf("BRANCHLINE_LINUX_SSH_HOST=%q does not match BRANCHLINE_SSHLAB_CONTAINER=%q", host, containerName)
	}
	if sshConfigPath == "" {
		t.Fatal("BRANCHLINE_SSHLAB_SSH_CONFIG is required for isolated sshlab target resolution")
	}
	if _, err := os.Stat(sshConfigPath); err != nil {
		t.Fatalf("stat BRANCHLINE_SSHLAB_SSH_CONFIG (%s): %v", sshConfigPath, err)
	}

	homeDir, err := os.UserHomeDir()
	must(t, err)
	userSSHDir := filepath.Join(homeDir, ".ssh")
	if strings.HasPrefix(filepath.Clean(sshConfigPath), filepath.Clean(userSSHDir)+string(filepath.Separator)) {
		t.Fatalf("isolated ssh_config %q must not be inside user ~/.ssh (%q)", sshConfigPath, userSSHDir)
	}

	beforeUserCfg, err := fileSHA256OrMissing(filepath.Join(userSSHDir, "config"))
	must(t, err)
	beforeUserKH, err := fileSHA256OrMissing(filepath.Join(userSSHDir, "known_hosts"))
	must(t, err)
	t.Cleanup(func() {
		afterUserCfg, cfgErr := fileSHA256OrMissing(filepath.Join(userSSHDir, "config"))
		afterUserKH, khErr := fileSHA256OrMissing(filepath.Join(userSSHDir, "known_hosts"))
		if cfgErr != nil || afterUserCfg != beforeUserCfg {
			t.Errorf("~/.ssh/config changed during test: before=%s after=%s err=%v", beforeUserCfg, afterUserCfg, cfgErr)
		}
		if khErr != nil || afterUserKH != beforeUserKH {
			t.Errorf("~/.ssh/known_hosts changed during test: before=%s after=%s err=%v", beforeUserKH, afterUserKH, khErr)
		}
	})

	if envPort == "" {
		t.Fatal("BRANCHLINE_SSHLAB_PORT is required for isolated sshlab target resolution")
	}
	wantPort, err := strconv.Atoi(envPort)
	if err != nil || wantPort <= 0 || wantPort > 65535 {
		t.Fatalf("invalid BRANCHLINE_SSHLAB_PORT %q: %v", envPort, err)
	}

	cfgFileHostName, cfgFilePort := parseSSHConfigFileTarget(t, sshConfigPath, host)
	resolved := runSSHResolveConfig(t, sshConfigPath, host)

	// Docker publishes the container sshd on a per-container loopback host port,
	// so the resolved target must be exactly 127.0.0.1 on that published port.
	targetIP := resolved["hostname"]
	if targetIP != "127.0.0.1" {
		t.Fatalf("resolved hostname %q from ssh -G is not 127.0.0.1", targetIP)
	}
	if cfgFileHostName != "" && cfgFileHostName != targetIP {
		t.Fatalf("ssh_config HostName %q does not match ssh -G hostname %q", cfgFileHostName, targetIP)
	}

	targetPort, err := strconv.Atoi(resolved["port"])
	if err != nil || targetPort <= 0 || targetPort > 65535 {
		t.Fatalf("invalid resolved port %q: %v", resolved["port"], err)
	}
	if targetPort != wantPort {
		t.Fatalf("resolved port %d does not match BRANCHLINE_SSHLAB_PORT %d", targetPort, wantPort)
	}
	if cfgFilePort > 0 && cfgFilePort != targetPort {
		t.Fatalf("ssh_config Port %d does not match ssh -G port %d", cfgFilePort, targetPort)
	}

	strictVal := strings.ToLower(resolved["stricthostkeychecking"])
	if strictVal != "yes" && strictVal != "true" {
		t.Fatalf("resolved StrictHostKeyChecking=%q, want yes/true", resolved["stricthostkeychecking"])
	}

	knownHostsPath := resolved["userknownhostsfile"]
	if knownHostsPath == "" || knownHostsPath == "/dev/null" {
		t.Fatalf("invalid resolved UserKnownHostsFile %q", knownHostsPath)
	}
	if knownHostsEnv != "" && filepath.Clean(knownHostsPath) != filepath.Clean(knownHostsEnv) {
		t.Fatalf("resolved UserKnownHostsFile %q != BRANCHLINE_SSHLAB_KNOWN_HOSTS %q", knownHostsPath, knownHostsEnv)
	}
	if _, err := os.Stat(knownHostsPath); err != nil {
		t.Fatalf("stat UserKnownHostsFile (%s): %v", knownHostsPath, err)
	}

	identityFile := resolved["identityfile"]
	if identityFile == "" {
		t.Fatal("missing resolved IdentityFile from ssh -G")
	}
	if _, err := os.Stat(identityFile); err != nil {
		t.Fatalf("stat IdentityFile (%s): %v", identityFile, err)
	}

	// ssh -G lowercases hostkeyalias (and hostname) in its output and performs
	// host-key matching case-insensitively, so compare case-insensitively too.
	hostKeyAlias := resolved["hostkeyalias"]
	if !strings.EqualFold(hostKeyAlias, host) {
		t.Fatalf("resolved HostKeyAlias %q does not match host alias %q", hostKeyAlias, host)
	}

	directEx := &SSHExecutor{Host: host, Dir: root, NoMultiplex: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	unameOut, err := directEx.RunShell(ctx, "uname -s")
	if err != nil {
		t.Fatalf("direct SSH uname -s failed: %v", err)
	}
	if got := strings.TrimSpace(string(unameOut)); got != "Linux" {
		t.Fatalf("remote uname -s = %q, want Linux", got)
	}

	rootRepo := New(directEx)
	caps, err := rootRepo.Capabilities()
	must(t, err)
	indepVerBytes, err := directEx.RunShell(ctx, "git --version")
	must(t, err)
	indepVer := strings.TrimSpace(string(indepVerBytes))
	indepParsed, err := ParseGitVersion(indepVer)
	must(t, err)
	if caps.RawVersion != indepVer || caps.Version != indepParsed {
		t.Fatalf("Capabilities() = (%q, %+v), independent git --version = (%q, %+v)", caps.RawVersion, caps.Version, indepVer, indepParsed)
	}
	if !caps.MeetsMinimum {
		t.Fatalf("remote Git %+v (%q) does not meet minimum supported Git 2.25", caps.Version, caps.RawVersion)
	}
	if expectRaw := strings.TrimSpace(os.Getenv("BRANCHLINE_LINUX_SSH_EXPECT_GIT")); expectRaw != "" {
		expectVer, err := ParseGitVersion(expectRaw)
		if err != nil {
			t.Fatalf("invalid BRANCHLINE_LINUX_SSH_EXPECT_GIT=%q: %v", expectRaw, err)
		}
		if caps.Version.Major != expectVer.Major || caps.Version.Minor != expectVer.Minor {
			t.Fatalf("expected remote Git %d.%d.x via BRANCHLINE_LINUX_SSH_EXPECT_GIT=%q, got %+v", expectVer.Major, expectVer.Minor, expectRaw, caps.Version)
		}
	}

	// Snapshot the standard root fixture to guarantee we never delete or mutate it.
	rootSnapCmd := "cd " + ShellQuote(root) + " && git rev-parse HEAD && git symbolic-ref HEAD && git status --porcelain=v1 && cat a.txt"
	beforeRootSnapBytes, err := directEx.RunShell(ctx, rootSnapCmd)
	if err != nil {
		t.Fatalf("snapshot root fixture %s: %v", root, err)
	}
	beforeRootSnap := string(beforeRootSnapBytes)
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		afterRootSnapBytes, err := directEx.RunShell(cleanCtx, rootSnapCmd)
		if err != nil {
			t.Errorf("verify root fixture %s in cleanup: %v", root, err)
			return
		}
		if string(afterRootSnapBytes) != beforeRootSnap {
			t.Errorf("standard root fixture %s was mutated:\nbefore:\n%s\nafter:\n%s", root, beforeRootSnap, string(afterRootSnapBytes))
		}
	})

	target := &resolvedSSHTarget{
		hostAlias:      host,
		rootRepo:       root,
		sshlabEnv:      sshlabEnv,
		containerName:  containerName,
		sshConfigPath:  sshConfigPath,
		knownHostsPath: knownHostsPath,
		identityFile:   identityFile,
		targetIP:       targetIP,
		targetPort:     targetPort,
		targetAddr:     net.JoinHostPort(targetIP, strconv.Itoa(targetPort)),
		remoteUser:     resolved["user"],
		hostKeyAlias:   hostKeyAlias,
		gitVersionRaw:  indepVer,
		gitCaps:        caps,
		directEx:       directEx,
	}
	t.Logf("verified sshlab target: env=%s host=%s target=%s git=%q (caps=%+v) config=%s known_hosts=%s",
		target.sshlabEnv, target.hostAlias, target.targetAddr, target.gitVersionRaw, target.gitCaps.Version, target.sshConfigPath, target.knownHostsPath)
	return target
}

func parseSSHConfigFileTarget(t *testing.T, configPath, wantHost string) (hostName string, port int) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	must(t, err)
	matchedHost := false
	inMatchingBlock := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(fields[0])
		if key == "host" {
			inMatchingBlock = false
			for _, pat := range fields[1:] {
				if pat == wantHost {
					inMatchingBlock = true
					matchedHost = true
					break
				}
			}
			continue
		}
		if !inMatchingBlock {
			continue
		}
		switch key {
		case "hostname":
			hostName = strings.Trim(fields[1], "\"")
		case "port":
			p, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("invalid Port %q in %s: %v", fields[1], configPath, err)
			}
			port = p
		case "stricthostkeychecking":
			if strings.EqualFold(fields[1], "no") {
				t.Fatalf("ssh_config %s has StrictHostKeyChecking no", configPath)
			}
		}
	}
	if !matchedHost {
		t.Fatalf("ssh_config %s does not contain a Host entry for %q", configPath, wantHost)
	}
	return hostName, port
}

func runSSHResolveConfig(t *testing.T, configPath, host string, extraOpts ...string) map[string]string {
	t.Helper()
	args := []string{"-F", configPath}
	args = append(args, extraOpts...)
	args = append(args, "-G", host)
	cmd := exec.Command("/usr/bin/ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("/usr/bin/ssh %v failed: %v\n%s", args, err, out)
	}
	res := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if _, exists := res[k]; !exists {
			res[k] = v
		}
	}
	return res
}

type proxyConnPair struct {
	id         int
	clientConn net.Conn
	serverConn net.Conn
	done       chan struct{}
	closeOnce  sync.Once
}

func (c *proxyConnPair) closeNow(abortive bool) {
	c.closeOnce.Do(func() {
		close(c.done)
		if abortive {
			if tc, ok := c.clientConn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			if tc, ok := c.serverConn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
		}
		_ = c.clientConn.Close()
		_ = c.serverConn.Close()
	})
}

type sshTCPProxy struct {
	targetAddr string
	listener   net.Listener
	listenAddr string
	listenPort int

	mu            sync.Mutex
	cond          *sync.Cond
	closed        bool
	rejectNew     bool
	paused        bool
	nextConnID    int
	totalConns    int
	rejectedConns int
	bytesC2S      int64
	bytesS2C      int64
	active        map[int]*proxyConnPair
	wg            sync.WaitGroup
}

func startSSHTCPProxy(t *testing.T, targetAddr string) *sshTCPProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback TCP proxy: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	if !tcpAddr.IP.IsLoopback() {
		_ = ln.Close()
		t.Fatalf("proxy listener %v is not loopback", tcpAddr)
	}
	p := &sshTCPProxy{
		targetAddr: targetAddr,
		listener:   ln,
		listenAddr: ln.Addr().String(),
		listenPort: tcpAddr.Port,
		active:     make(map[int]*proxyConnPair),
	}
	p.cond = sync.NewCond(&p.mu)
	p.wg.Add(1)
	go p.acceptLoop()
	t.Cleanup(p.Close)
	return p
}

func (p *sshTCPProxy) acceptLoop() {
	defer p.wg.Done()
	for {
		clientConn, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed || p.rejectNew {
			p.rejectedConns++
			p.mu.Unlock()
			if tc, ok := clientConn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			_ = clientConn.Close()
			continue
		}
		p.nextConnID++
		connID := p.nextConnID
		p.totalConns++
		p.mu.Unlock()

		serverConn, err := net.DialTimeout("tcp", p.targetAddr, 10*time.Second)
		if err != nil {
			_ = clientConn.Close()
			continue
		}

		pair := &proxyConnPair{
			id:         connID,
			clientConn: clientConn,
			serverConn: serverConn,
			done:       make(chan struct{}),
		}
		p.mu.Lock()
		if p.closed || p.rejectNew {
			p.mu.Unlock()
			pair.closeNow(true)
			continue
		}
		p.active[connID] = pair
		p.cond.Broadcast()
		p.mu.Unlock()

		p.wg.Add(2)
		go func() {
			defer p.wg.Done()
			p.pipe(pair, clientConn, serverConn, true)
			p.removePair(pair, false)
		}()
		go func() {
			defer p.wg.Done()
			p.pipe(pair, serverConn, clientConn, false)
			p.removePair(pair, false)
		}()
	}
}

func (p *sshTCPProxy) pipe(pair *proxyConnPair, src, dst net.Conn, clientToServer bool) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if !p.waitIfPaused(pair) {
				return
			}
			written, werr := dst.Write(buf[:n])
			if written > 0 {
				p.mu.Lock()
				if clientToServer {
					p.bytesC2S += int64(written)
				} else {
					p.bytesS2C += int64(written)
				}
				p.mu.Unlock()
			}
			if werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

func (p *sshTCPProxy) waitIfPaused(pair *proxyConnPair) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.paused && !p.closed {
		select {
		case <-pair.done:
			return false
		default:
		}
		p.cond.Wait()
	}
	select {
	case <-pair.done:
		return false
	default:
	}
	return !p.closed
}

func (p *sshTCPProxy) removePair(pair *proxyConnPair, abortive bool) {
	pair.closeNow(abortive)
	p.mu.Lock()
	delete(p.active, pair.id)
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *sshTCPProxy) DisconnectActive() int {
	p.mu.Lock()
	p.rejectNew = true
	p.paused = false
	pairs := make([]*proxyConnPair, 0, len(p.active))
	for _, pair := range p.active {
		pairs = append(pairs, pair)
	}
	p.cond.Broadcast()
	p.mu.Unlock()

	for _, pair := range pairs {
		p.removePair(pair, true)
	}
	return len(pairs)
}

func (p *sshTCPProxy) PauseForwarding() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
	p.cond.Broadcast()
	return len(p.active)
}

func (p *sshTCPProxy) ResumeForwarding() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
	p.cond.Broadcast()
}

func (p *sshTCPProxy) Restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rejectNew = false
	p.paused = false
	p.cond.Broadcast()
}

func (p *sshTCPProxy) Stats() (total, active, rejected int, c2s, s2c int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalConns, len(p.active), p.rejectedConns, p.bytesC2S, p.bytesS2C
}

func (p *sshTCPProxy) WaitForZeroActive(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		n := len(p.active)
		p.mu.Unlock()
		if n == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy still has %d active connection(s) after %s", n, timeout)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (p *sshTCPProxy) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.rejectNew = true
	p.paused = false
	pairs := make([]*proxyConnPair, 0, len(p.active))
	for _, pair := range p.active {
		pairs = append(pairs, pair)
	}
	p.cond.Broadcast()
	p.mu.Unlock()

	_ = p.listener.Close()
	for _, pair := range pairs {
		p.removePair(pair, true)
	}
	p.wg.Wait()
}

type proxySSHClient struct {
	wrapperPath    string
	invocationsLog string
	pidsLog        string
}

func newProxySSHClient(t *testing.T, target *resolvedSSHTarget, proxy *sshTCPProxy) *proxySSHClient {
	t.Helper()
	dir := t.TempDir()
	wrapperPath := filepath.Join(dir, "ssh-proxy-wrapper")
	invocationsLog := filepath.Join(dir, "ssh-invocations.log")
	pidsLog := filepath.Join(dir, "ssh-pids.log")

	opts := []string{
		"-o", "HostName=127.0.0.1",
		"-o", "Port=" + strconv.Itoa(proxy.listenPort),
		"-o", "HostKeyAlias=" + target.hostKeyAlias,
		"-o", "UserKnownHostsFile=" + target.knownHostsPath,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "IdentityFile=" + target.identityFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ControlPersist=no",
	}
	var quotedOpts []string
	for _, o := range opts {
		quotedOpts = append(quotedOpts, ShellQuote(o))
	}

	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"printf '%s\\n' \"$$\" >> " + ShellQuote(pidsLog) + "\n" +
		"printf '%s\\n' \"$*\" >> " + ShellQuote(invocationsLog) + "\n" +
		"exec /usr/bin/ssh -F " + ShellQuote(target.sshConfigPath) + " " + strings.Join(quotedOpts, " ") + " \"$@\"\n"
	must(t, os.WriteFile(wrapperPath, []byte(script), 0o755))

	if strings.Contains(strings.ToLower(script), "stricthostkeychecking=no") || strings.Contains(strings.ToLower(script), "stricthostkeychecking no") {
		t.Fatal("proxy ssh wrapper must never set StrictHostKeyChecking=no")
	}

	resolved := runSSHResolveConfig(t, target.sshConfigPath, target.hostAlias, opts...)
	if resolved["hostname"] != "127.0.0.1" || resolved["port"] != strconv.Itoa(proxy.listenPort) {
		t.Fatalf("proxy wrapper resolved target = %s:%s, want 127.0.0.1:%d", resolved["hostname"], resolved["port"], proxy.listenPort)
	}
	strictVal := strings.ToLower(resolved["stricthostkeychecking"])
	if strictVal != "yes" && strictVal != "true" {
		t.Fatalf("proxy wrapper StrictHostKeyChecking = %q, want yes/true", resolved["stricthostkeychecking"])
	}
	cmVal := strings.ToLower(resolved["controlmaster"])
	if cmVal != "no" && cmVal != "false" {
		t.Fatalf("proxy wrapper ControlMaster = %q, want no/false", resolved["controlmaster"])
	}
	if cpVal := strings.ToLower(resolved["controlpath"]); cpVal != "" && cpVal != "none" {
		t.Fatalf("proxy wrapper ControlPath = %q, want empty/none", resolved["controlpath"])
	}

	return &proxySSHClient{
		wrapperPath:    wrapperPath,
		invocationsLog: invocationsLog,
		pidsLog:        pidsLog,
	}
}

func (c *proxySSHClient) Executor(host, dir string) *SSHExecutor {
	return &SSHExecutor{
		Host:        host,
		Dir:         dir,
		SSH:         c.wrapperPath,
		NoMultiplex: true,
	}
}

func (c *proxySSHClient) Invocations(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(c.invocationsLog)
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func (c *proxySSHClient) VerifyAllLocalSSHPIDsExited(t *testing.T, timeout time.Duration) []int {
	t.Helper()
	data, err := os.ReadFile(c.pidsLog)
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("invalid local ssh PID %q: %v", line, err)
		}
		pids = append(pids, pid)
	}
	deadline := time.Now().Add(timeout)
	for _, pid := range pids {
		for {
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("local ssh process %d is still alive after %s (err=%v)", pid, timeout, err)
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	return pids
}

const (
	wantStagedSentinelBytes    = "staged sentinel bytes v1\n"
	wantTrackedStagedBytes     = "tracked staged v2\n"
	wantTrackedWorktreeBytes   = "tracked worktree unstaged v3\n"
	wantSharedBaseBytes        = "shared base v1\n"
	wantSharedWorktreeBytes    = "shared worktree unstaged v2\n"
	wantUntrackedSentinelBytes = "untracked sentinel bytes 0123456789\n"
	wantPeerKeepBytes          = "peer branch sentinel data\n"
)

type workRepoSnapshot struct {
	HeadSymref        string
	HeadOID           string
	Refs              string
	RefObjectsCheck   string
	IndexStages       string
	PorcelainStatus   string
	DiffCached        string
	DiffWorktree      string
	StagedIndex       string
	StagedWorktree    string
	TrackedIndex      string
	TrackedWorktree   string
	SharedIndex       string
	SharedWorktree    string
	UntrackedWorktree string
	FsckOut           string
	LockAndTempFiles  string
}

type bareRepoSnapshot struct {
	Refs             string
	RefObjectsCheck  string
	MainOID          string
	PeerKeepOID      string
	PeerKeepBlob     string
	FsckOut          string
	LockAndTempFiles string
}

type refTransitionRule struct {
	OldValue      string
	AllowedValues []string
}

type uploadPackStageEvidence struct {
	ReadyMarker string
	GitPID      int
	WrapperPID  int
	GitComm     string
	StageBytes  int
	StageHex    string
}

type preReceiveStageEvidence struct {
	ReadyMarker    string
	HookPID        int
	ReceivePackPID int
	ReceiveComm    string
	StdinRefs      string
	RefWhilePaused string
}

type networkRecoveryCaseFixture struct {
	t                 *testing.T
	target            *resolvedSSHTarget
	caseDir           string
	bareDir           string
	peerDir           string
	workDir           string
	barrierDir        string
	uploadBarrierPath string
	preReceivePath    string
	baseCommit        string
	peerKeepCommit    string

	baselineWorkRefs    string
	baselineWorkHeadOID string
	baselineBareRefs    string
	baselineBareMainOID string

	mu          sync.Mutex
	trackedPIDs []int
}

func (f *networkRecoveryCaseFixture) sshOut(t *testing.T, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := f.target.directEx.RunShell(ctx, "/bin/sh -c "+ShellQuote("set -eu\n"+script))
	if err != nil {
		t.Fatalf("remote script failed: %v\nscript:\n%s", err, script)
	}
	return string(out)
}

func (f *networkRecoveryCaseFixture) trackPIDs(pids ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pid := range pids {
		if pid > 0 {
			f.trackedPIDs = append(f.trackedPIDs, pid)
		}
	}
}

func newNetworkRecoveryCaseFixture(t *testing.T, target *resolvedSSHTarget, name string) *networkRecoveryCaseFixture {
	t.Helper()
	f := &networkRecoveryCaseFixture{
		t:      t,
		target: target,
	}
	caseDir := strings.TrimSpace(f.sshOut(t, "mktemp -d "+ShellQuote(target.rootRepo+"/t506-"+name+".XXXXXX")))
	if path.Dir(caseDir) != target.rootRepo || !strings.HasPrefix(path.Base(caseDir), "t506-"+name+".") {
		t.Fatalf("unexpected remote caseDir %q under %q", caseDir, target.rootRepo)
	}
	f.caseDir = caseDir
	f.bareDir = caseDir + "/origin.git"
	f.peerDir = caseDir + "/peer"
	f.workDir = caseDir + "/work"
	f.barrierDir = caseDir + "/barrier"
	f.uploadBarrierPath = f.barrierDir + "/upload-pack-barrier.sh"
	f.preReceivePath = f.bareDir + "/hooks/pre-receive"

	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		f.mu.Lock()
		pids := append([]int(nil), f.trackedPIDs...)
		f.mu.Unlock()
		var pidList []string
		for _, pid := range pids {
			pidList = append(pidList, strconv.Itoa(pid))
		}
		cleanupScript := "set -eu\n" +
			"if [ -d " + ShellQuote(f.barrierDir) + " ]; then\n" +
			"  rm -f -- " + ShellQuote(f.barrierDir+"/arm") + "\n" +
			"  printf 'release\\n' > " + ShellQuote(f.barrierDir+"/release") + " || true\n" +
			"fi\n"
		if len(pidList) > 0 {
			cleanupScript += "for pid in " + strings.Join(pidList, " ") + "; do\n" +
				"  i=0\n" +
				"  while kill -0 \"$pid\" 2>/dev/null; do\n" +
				"    i=$((i + 1))\n" +
				"    if [ \"$i\" -ge 50 ]; then kill -9 \"$pid\" 2>/dev/null || true; break; fi\n" +
				"    sleep 0.02\n" +
				"  done\n" +
				"done\n"
		}
		cleanupScript += "rm -rf -- " + ShellQuote(f.caseDir) + "\n"
		if _, err := target.directEx.RunShell(cleanCtx, "/bin/sh -c "+ShellQuote(cleanupScript)); err != nil {
			t.Errorf("cleanup remote fixture %s: %v", f.caseDir, err)
		}
	})

	setupScript := `
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
mkdir -p ` + ShellQuote(f.bareDir) + ` ` + ShellQuote(f.peerDir) + ` ` + ShellQuote(f.workDir) + ` ` + ShellQuote(f.barrierDir) + `

git init -q --bare ` + ShellQuote(f.bareDir) + `
git -C ` + ShellQuote(f.bareDir) + ` symbolic-ref HEAD refs/heads/main
git -C ` + ShellQuote(f.bareDir) + ` config --unset-all core.hooksPath 2>/dev/null || true

cd ` + ShellQuote(f.peerDir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Peer'
git config user.email 'peer@example.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
git remote add origin ` + ShellQuote(f.bareDir) + `
printf 'base tracked v1\n' > tracked.txt
printf 'shared base v1\n' > shared.txt
git add tracked.txt shared.txt
git commit -q -m 'base commit'
base_oid=$(git rev-parse HEAD)
git tag v0.1-keep "$base_oid"
git push -q origin refs/heads/main:refs/heads/main refs/tags/v0.1-keep:refs/tags/v0.1-keep

git checkout -qb peer-keep
printf 'peer branch sentinel data\n' > peer-only.txt
git add peer-only.txt
git commit -q -m 'peer keep commit'
peer_oid=$(git rev-parse HEAD)
git push -q origin refs/heads/peer-keep:refs/heads/peer-keep
git checkout -q main

cd ` + ShellQuote(f.workDir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline Work'
git config user.email 'work@example.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
git remote add origin ` + ShellQuote(f.bareDir) + `
# Git 2.54 defaults to creating origin/HEAD on the first fetch. This fixture
# declares exact refs, so pin that optional behavior before any fetch runs.
git config --local remote.origin.followRemoteHEAD never
[ "$(git config --local --get remote.origin.followRemoteHEAD)" = never ]
git fetch -q --tags origin
git reset -q --hard refs/remotes/origin/main
git branch -q --set-upstream-to=origin/main main
git branch -q local-keep "$base_oid"

printf '%s\n%s\n' "$base_oid" "$peer_oid"
`
	lines := strings.Split(strings.TrimSpace(f.sshOut(t, setupScript)), "\n")
	if len(lines) != 2 || !isHex(lines[0]) || !isHex(lines[1]) {
		t.Fatalf("unexpected fixture OIDs %q", lines)
	}
	f.baseCommit = lines[0]
	f.peerKeepCommit = lines[1]

	f.installBarrierScripts(t)
	return f
}

func (f *networkRecoveryCaseFixture) installBarrierScripts(t *testing.T) {
	t.Helper()
	bDir := ShellQuote(f.barrierDir)
	uploadScript := "#!/bin/sh\n" +
		"set -eu\n" +
		"bdir=" + bDir + "\n" +
		"if [ ! -f \"$bdir/arm\" ]; then\n" +
		"  exec git-upload-pack \"$@\"\n" +
		"fi\n" +
		"rm -f -- \"$bdir/arm\"\n" +
		"printf '%s\\n' \"$$\" > \"$bdir/wrapper.pid\"\n" +
		"fifo=\"$bdir/up.fifo.$$\"\n" +
		"rm -f -- \"$fifo\"\n" +
		"mkfifo \"$fifo\"\n" +
		"exec 4<&0\n" +
		"git-upload-pack \"$@\" <&4 > \"$fifo\" &\n" +
		"up_pid=$!\n" +
		"printf '%s\\n' \"$up_pid\" > \"$bdir/git.pid\"\n" +
		"exec 3< \"$fifo\"\n" +
		"rm -f -- \"$fifo\"\n" +
		"dd bs=1 count=64 <&3 > \"$bdir/stage.bytes\" 2>/dev/null\n" +
		"cat \"$bdir/stage.bytes\"\n" +
		"wc -c < \"$bdir/stage.bytes\" | tr -d ' \\t\\r\\n' > \"$bdir/stage.count\"\n" +
		"od -An -tx1 -N 16 \"$bdir/stage.bytes\" | tr -d ' \\t\\r\\n' > \"$bdir/stage.hex\"\n" +
		"printf 'upload-pack-stream-paused pid=%s wrapper=%s\\n' \"$up_pid\" \"$$\" > \"$bdir/ready\"\n" +
		"while [ ! -f \"$bdir/release\" ]; do\n" +
		"  sleep 0.02\n" +
		"done\n" +
		"exec 3<&-\n" +
		"exec 4<&-\n" +
		"kill \"$up_pid\" 2>/dev/null || true\n" +
		"wait \"$up_pid\" 2>/dev/null || true\n" +
		"exit 1\n"

	preReceiveScript := "#!/bin/sh\n" +
		"set -eu\n" +
		"bdir=" + bDir + "\n" +
		"if [ ! -f \"$bdir/arm\" ]; then\n" +
		"  exit 0\n" +
		"fi\n" +
		"rm -f -- \"$bdir/arm\"\n" +
		"cat > \"$bdir/stdin.refs\"\n" +
		"printf '%s\\n' \"$$\" > \"$bdir/hook.pid\"\n" +
		"printf '%s\\n' \"$PPID\" > \"$bdir/receive-pack.pid\"\n" +
		"printf 'pre-receive-paused hook_pid=%s rp_pid=%s\\n' \"$$\" \"$PPID\" > \"$bdir/ready\"\n" +
		"while [ ! -f \"$bdir/release\" ]; do\n" +
		"  sleep 0.02\n" +
		"done\n" +
		"if [ -f \"$bdir/fail-on-release\" ]; then\n" +
		"  rm -f -- \"$bdir/fail-on-release\"\n" +
		"  echo 'aborted at pre-receive barrier after disconnect' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"exit 0\n"

	installCmd := "mkdir -p " + ShellQuote(f.bareDir+"/hooks") + "\n" +
		"printf '%s' " + ShellQuote(uploadScript) + " > " + ShellQuote(f.uploadBarrierPath) + "\n" +
		"chmod 755 " + ShellQuote(f.uploadBarrierPath) + "\n" +
		"printf '%s' " + ShellQuote(preReceiveScript) + " > " + ShellQuote(f.preReceivePath) + "\n" +
		"chmod 755 " + ShellQuote(f.preReceivePath) + "\n"
	f.sshOut(t, installCmd)
}

func (f *networkRecoveryCaseFixture) populateWorkSentinels(t *testing.T) {
	t.Helper()
	script := "cd " + ShellQuote(f.workDir) + "\n" +
		"printf " + ShellQuote(wantStagedSentinelBytes) + " > staged-sentinel.txt\n" +
		"printf " + ShellQuote(wantTrackedStagedBytes) + " > tracked.txt\n" +
		"git add staged-sentinel.txt tracked.txt\n" +
		"printf " + ShellQuote(wantTrackedWorktreeBytes) + " > tracked.txt\n" +
		"printf " + ShellQuote(wantSharedWorktreeBytes) + " > shared.txt\n" +
		"printf " + ShellQuote(wantUntrackedSentinelBytes) + " > untracked-sentinel.txt\n"
	f.sshOut(t, script)
}

func (f *networkRecoveryCaseFixture) armUploadPackBarrier(t *testing.T) {
	t.Helper()
	script := "rm -f -- " + ShellQuote(f.barrierDir) + "/ready " +
		ShellQuote(f.barrierDir) + "/release " +
		ShellQuote(f.barrierDir) + "/git.pid " +
		ShellQuote(f.barrierDir) + "/wrapper.pid " +
		ShellQuote(f.barrierDir) + "/stage.bytes " +
		ShellQuote(f.barrierDir) + "/stage.count " +
		ShellQuote(f.barrierDir) + "/stage.hex\n" +
		"git -C " + ShellQuote(f.workDir) + " config remote.origin.uploadpack " + ShellQuote(f.uploadBarrierPath) + "\n" +
		"printf 'arm\\n' > " + ShellQuote(f.barrierDir+"/arm") + "\n"
	f.sshOut(t, script)
}

func (f *networkRecoveryCaseFixture) armPreReceiveBarrier(t *testing.T, failOnRelease bool) {
	t.Helper()
	script := "rm -f -- " + ShellQuote(f.barrierDir) + "/ready " +
		ShellQuote(f.barrierDir) + "/release " +
		ShellQuote(f.barrierDir) + "/hook.pid " +
		ShellQuote(f.barrierDir) + "/receive-pack.pid " +
		ShellQuote(f.barrierDir) + "/stdin.refs " +
		ShellQuote(f.barrierDir) + "/fail-on-release\n" +
		"printf 'arm\\n' > " + ShellQuote(f.barrierDir+"/arm") + "\n"
	if failOnRelease {
		script += "printf '1\\n' > " + ShellQuote(f.barrierDir+"/fail-on-release") + "\n"
	}
	f.sshOut(t, script)
}

func (f *networkRecoveryCaseFixture) waitUploadPackBarrier(t *testing.T, timeout time.Duration) uploadPackStageEvidence {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pollScript := "bdir=" + ShellQuote(f.barrierDir) + "\n" +
		"while [ ! -s \"$bdir/ready\" ]; do sleep 0.02; done\n" +
		"up_pid=$(cat \"$bdir/git.pid\")\n" +
		"wrap_pid=$(cat \"$bdir/wrapper.pid\")\n" +
		"kill -0 \"$up_pid\"\n" +
		"kill -0 \"$wrap_pid\"\n" +
		"comm=$(cat \"/proc/$up_pid/comm\" 2>/dev/null || ps -p \"$up_pid\" -o comm= 2>/dev/null || printf 'git-upload-pack')\n" +
		"printf '%s\\n%s\\n%s\\n%s\\n%s\\n%s\\n' " +
		"\"$(cat \"$bdir/ready\")\" \"$up_pid\" \"$wrap_pid\" \"$comm\" \"$(cat \"$bdir/stage.count\")\" \"$(cat \"$bdir/stage.hex\")\"\n"

	waitDur := time.Until(deadline)
	if waitDur <= 0 {
		waitDur = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitDur)
	defer cancel()
	outBytes, err := f.target.directEx.RunShell(ctx, "/bin/sh -c "+ShellQuote("set -eu\n"+pollScript))
	if err != nil {
		t.Fatalf("timed out or failed waiting for upload-pack barrier: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	if len(lines) != 6 {
		t.Fatalf("unexpected upload-pack barrier output lines (%d): %q", len(lines), string(outBytes))
	}
	upPID, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	must(t, err)
	wrapPID, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	must(t, err)
	stageBytes, err := strconv.Atoi(strings.TrimSpace(lines[4]))
	must(t, err)
	if upPID <= 0 || wrapPID <= 0 || stageBytes != 64 || lines[5] == "" {
		t.Fatalf("invalid upload-pack stage evidence: %+v", lines)
	}
	f.trackPIDs(upPID, wrapPID)
	return uploadPackStageEvidence{
		ReadyMarker: lines[0],
		GitPID:      upPID,
		WrapperPID:  wrapPID,
		GitComm:     strings.TrimSpace(lines[3]),
		StageBytes:  stageBytes,
		StageHex:    strings.TrimSpace(lines[5]),
	}
}

func (f *networkRecoveryCaseFixture) waitPreReceiveBarrier(t *testing.T, targetRef string, timeout time.Duration) preReceiveStageEvidence {
	t.Helper()
	deadline := time.Now().Add(timeout)
	pollScript := "bdir=" + ShellQuote(f.barrierDir) + "\n" +
		"while [ ! -s \"$bdir/ready\" ]; do sleep 0.02; done\n" +
		"hook_pid=$(cat \"$bdir/hook.pid\")\n" +
		"rp_pid=$(cat \"$bdir/receive-pack.pid\")\n" +
		"kill -0 \"$hook_pid\"\n" +
		"kill -0 \"$rp_pid\"\n" +
		"comm=$(cat \"/proc/$rp_pid/comm\" 2>/dev/null || ps -p \"$rp_pid\" -o comm= 2>/dev/null || printf 'git-receive-pack')\n" +
		"ref_now=$(git -C " + ShellQuote(f.bareDir) + " rev-parse --verify -q " + ShellQuote(targetRef) + " || printf 'missing')\n" +
		"printf '%s\\n%s\\n%s\\n%s\\n%s\\n%s\\n' " +
		"\"$(cat \"$bdir/ready\")\" \"$hook_pid\" \"$rp_pid\" \"$comm\" \"$(tr '\\n' ' ' < \"$bdir/stdin.refs\")\" \"$ref_now\"\n"

	waitDur := time.Until(deadline)
	if waitDur <= 0 {
		waitDur = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitDur)
	defer cancel()
	outBytes, err := f.target.directEx.RunShell(ctx, "/bin/sh -c "+ShellQuote("set -eu\n"+pollScript))
	if err != nil {
		t.Fatalf("timed out or failed waiting for pre-receive barrier: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	if len(lines) != 6 {
		t.Fatalf("unexpected pre-receive barrier output lines (%d): %q", len(lines), string(outBytes))
	}
	hookPID, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	must(t, err)
	rpPID, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	must(t, err)
	if hookPID <= 0 || rpPID <= 0 || strings.TrimSpace(lines[4]) == "" {
		t.Fatalf("invalid pre-receive stage evidence: %+v", lines)
	}
	f.trackPIDs(hookPID, rpPID)
	return preReceiveStageEvidence{
		ReadyMarker:    lines[0],
		HookPID:        hookPID,
		ReceivePackPID: rpPID,
		ReceiveComm:    strings.TrimSpace(lines[3]),
		StdinRefs:      strings.TrimSpace(lines[4]),
		RefWhilePaused: strings.TrimSpace(lines[5]),
	}
}

func (f *networkRecoveryCaseFixture) releaseBarrierAndWaitPIDs(t *testing.T, pids ...int) {
	t.Helper()
	var pidStrs []string
	for _, pid := range pids {
		if pid > 0 {
			pidStrs = append(pidStrs, strconv.Itoa(pid))
		}
	}
	script := "printf 'release\\n' > " + ShellQuote(f.barrierDir+"/release") + "\n"
	if len(pidStrs) > 0 {
		script += "for pid in " + strings.Join(pidStrs, " ") + "; do\n" +
			"  i=0\n" +
			"  while kill -0 \"$pid\" 2>/dev/null; do\n" +
			"    i=$((i + 1))\n" +
			"    if [ \"$i\" -ge 250 ]; then\n" +
			"      echo \"remote pid $pid still running after 5s\" >&2\n" +
			"      exit 1\n" +
			"    fi\n" +
			"    sleep 0.02\n" +
			"  done\n" +
			"done\n"
	}
	f.sshOut(t, script)
}

const snapSep = "\n===BRANCHLINE_SNAP_SEP===\n"

func (f *networkRecoveryCaseFixture) captureWorkSnapshot(t *testing.T) workRepoSnapshot {
	t.Helper()
	w := ShellQuote(f.workDir)
	sep := "printf '\\n===BRANCHLINE_SNAP_SEP===\\n'\n"
	script := "git -C " + w + " symbolic-ref HEAD\n" + sep +
		"git -C " + w + " rev-parse HEAD\n" + sep +
		"git -C " + w + " for-each-ref --format='%(refname) %(objectname)'\n" + sep +
		"git -C " + w + " for-each-ref --format='%(objectname)' | git -C " + w + " cat-file --batch-check\n" + sep +
		"git -C " + w + " ls-files --stage\n" + sep +
		"git -C " + w + " status --porcelain=v1\n" + sep +
		"git -C " + w + " diff --cached\n" + sep +
		"git -C " + w + " diff\n" + sep +
		"git -C " + w + " show :staged-sentinel.txt\n" + sep +
		"cat -- " + ShellQuote(f.workDir+"/staged-sentinel.txt") + "\n" + sep +
		"git -C " + w + " show :tracked.txt\n" + sep +
		"cat -- " + ShellQuote(f.workDir+"/tracked.txt") + "\n" + sep +
		"git -C " + w + " show :shared.txt\n" + sep +
		"cat -- " + ShellQuote(f.workDir+"/shared.txt") + "\n" + sep +
		"cat -- " + ShellQuote(f.workDir+"/untracked-sentinel.txt") + "\n" + sep +
		"git -C " + w + " fsck --full 2>&1\n" + sep +
		"find " + ShellQuote(f.workDir+"/.git") + " \\( -name '*.lock' -o -name 'tmp_*' \\) -print\n"
	parts := strings.Split(f.sshOut(t, script), snapSep)
	if len(parts) != 17 {
		t.Fatalf("captureWorkSnapshot parts = %d, want 17", len(parts))
	}
	snap := workRepoSnapshot{
		HeadSymref:        strings.TrimSpace(parts[0]),
		HeadOID:           strings.TrimSpace(parts[1]),
		Refs:              strings.TrimSpace(parts[2]),
		RefObjectsCheck:   strings.TrimSpace(parts[3]),
		IndexStages:       strings.TrimSpace(parts[4]),
		PorcelainStatus:   parts[5],
		DiffCached:        parts[6],
		DiffWorktree:      parts[7],
		StagedIndex:       parts[8],
		StagedWorktree:    parts[9],
		TrackedIndex:      parts[10],
		TrackedWorktree:   parts[11],
		SharedIndex:       parts[12],
		SharedWorktree:    parts[13],
		UntrackedWorktree: parts[14],
		FsckOut:           strings.TrimSpace(parts[15]),
		LockAndTempFiles:  strings.TrimSpace(parts[16]),
	}
	if f.baselineWorkRefs == "" {
		f.baselineWorkRefs = snap.Refs
		f.baselineWorkHeadOID = snap.HeadOID
	}
	return snap
}

func (f *networkRecoveryCaseFixture) assertWorkSentinelsAndCleanLocks(t *testing.T, phase string, got, want workRepoSnapshot) {
	t.Helper()
	if strings.HasSuffix(phase, "/before") {
		f.baselineWorkRefs = got.Refs
		f.baselineWorkHeadOID = got.HeadOID
	}
	if got.HeadSymref != want.HeadSymref || got.HeadOID != want.HeadOID {
		t.Fatalf("%s: work HEAD = (%q, %q), want (%q, %q)", phase, got.HeadSymref, got.HeadOID, want.HeadSymref, want.HeadOID)
	}
	if got.IndexStages != want.IndexStages {
		t.Fatalf("%s: work index stages changed:\ngot:\n%s\nwant:\n%s", phase, got.IndexStages, want.IndexStages)
	}
	if got.PorcelainStatus != want.PorcelainStatus {
		t.Fatalf("%s: work porcelain status changed:\ngot:\n%s\nwant:\n%s", phase, got.PorcelainStatus, want.PorcelainStatus)
	}
	if got.DiffCached != want.DiffCached || got.DiffWorktree != want.DiffWorktree {
		t.Fatalf("%s: work diffs changed:\ncached got=%q want=%q\nworktree got=%q want=%q", phase, got.DiffCached, want.DiffCached, got.DiffWorktree, want.DiffWorktree)
	}
	if got.StagedIndex != wantStagedSentinelBytes || got.StagedWorktree != wantStagedSentinelBytes {
		t.Fatalf("%s: staged sentinel corrupted: index=%q worktree=%q", phase, got.StagedIndex, got.StagedWorktree)
	}
	if got.TrackedIndex != wantTrackedStagedBytes || got.TrackedWorktree != wantTrackedWorktreeBytes {
		t.Fatalf("%s: tracked sentinel corrupted: index=%q worktree=%q", phase, got.TrackedIndex, got.TrackedWorktree)
	}
	if got.SharedIndex != wantSharedBaseBytes || got.SharedWorktree != wantSharedWorktreeBytes {
		t.Fatalf("%s: shared sentinel corrupted: index=%q worktree=%q", phase, got.SharedIndex, got.SharedWorktree)
	}
	if got.UntrackedWorktree != wantUntrackedSentinelBytes {
		t.Fatalf("%s: untracked sentinel corrupted: got=%q want=%q", phase, got.UntrackedWorktree, wantUntrackedSentinelBytes)
	}
	if got.LockAndTempFiles != "" {
		t.Fatalf("%s: leftover lock/temp files in work/.git: %q", phase, got.LockAndTempFiles)
	}
	if err := verifyRefObjectsBatchCheck("work", phase, got.Refs, got.RefObjectsCheck); err != nil {
		t.Fatalf("%v", err)
	}
	if err := f.checkWorkRefsForPhase(phase, want.Refs, got.Refs); err != nil {
		t.Fatalf("%v", err)
	}
}

func (f *networkRecoveryCaseFixture) captureBareSnapshot(t *testing.T) bareRepoSnapshot {
	t.Helper()
	b := ShellQuote(f.bareDir)
	sep := "printf '\\n===BRANCHLINE_SNAP_SEP===\\n'\n"
	script := "git -C " + b + " for-each-ref --format='%(refname) %(objectname)'\n" + sep +
		"git -C " + b + " for-each-ref --format='%(objectname)' | git -C " + b + " cat-file --batch-check\n" + sep +
		"git -C " + b + " rev-parse refs/heads/main\n" + sep +
		"git -C " + b + " rev-parse refs/heads/peer-keep\n" + sep +
		"git -C " + b + " show refs/heads/peer-keep:peer-only.txt\n" + sep +
		"git -C " + b + " fsck --full 2>&1\n" + sep +
		"find " + b + " \\( -name '*.lock' -o -name 'tmp_*' -o -name 'tmp_objdir*' \\) -print\n"
	parts := strings.Split(f.sshOut(t, script), snapSep)
	if len(parts) != 7 {
		t.Fatalf("captureBareSnapshot parts = %d, want 7", len(parts))
	}
	snap := bareRepoSnapshot{
		Refs:             strings.TrimSpace(parts[0]),
		RefObjectsCheck:  strings.TrimSpace(parts[1]),
		MainOID:          strings.TrimSpace(parts[2]),
		PeerKeepOID:      strings.TrimSpace(parts[3]),
		PeerKeepBlob:     parts[4],
		FsckOut:          strings.TrimSpace(parts[5]),
		LockAndTempFiles: strings.TrimSpace(parts[6]),
	}
	if f.baselineBareRefs == "" {
		f.baselineBareRefs = snap.Refs
		f.baselineBareMainOID = snap.MainOID
	}
	return snap
}

func (f *networkRecoveryCaseFixture) assertBarePeerAndCleanLocks(t *testing.T, phase string, got bareRepoSnapshot) {
	t.Helper()
	if strings.HasSuffix(phase, "/before") {
		f.baselineBareRefs = got.Refs
		f.baselineBareMainOID = got.MainOID
	}
	if got.PeerKeepOID != f.peerKeepCommit || got.PeerKeepBlob != wantPeerKeepBytes {
		t.Fatalf("%s: bare peer-keep corrupted: oid=%q (want %q) blob=%q", phase, got.PeerKeepOID, f.peerKeepCommit, got.PeerKeepBlob)
	}
	if got.LockAndTempFiles != "" {
		t.Fatalf("%s: leftover lock/temp/quarantine files in bare: %q", phase, got.LockAndTempFiles)
	}
	if err := verifyRefObjectsBatchCheck("bare", phase, got.Refs, got.RefObjectsCheck); err != nil {
		t.Fatalf("%v", err)
	}
	if err := f.checkBareRefsForPhase(phase, f.baselineBareRefs, got.Refs); err != nil {
		t.Fatalf("%v", err)
	}
}

func parseExactRefMap(raw string) (map[string]string, error) {
	out := make(map[string]string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return out, nil
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed for-each-ref line %q", line)
		}
		refName, oid := fields[0], fields[1]
		if !strings.HasPrefix(refName, "refs/") || !isHex(oid) {
			return nil, fmt.Errorf("invalid ref entry (%q, %q) in line %q", refName, oid, line)
		}
		if prev, dup := out[refName]; dup {
			return nil, fmt.Errorf("duplicate ref %q (%q vs %q)", refName, prev, oid)
		}
		out[refName] = oid
	}
	return out, nil
}

func formatExactRefMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+" "+m[k])
	}
	return strings.Join(lines, "\n")
}

func cloneRefMap(m map[string]string) map[string]string {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

func verifyRefObjectsBatchCheck(repoLabel, phase, refsRaw, batchCheckRaw string) error {
	refMap, err := parseExactRefMap(refsRaw)
	if err != nil {
		return fmt.Errorf("%s: %s parse refs for batch-check: %w", phase, repoLabel, err)
	}
	validOIDs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(batchCheckRaw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("%s: %s malformed cat-file --batch-check line %q", phase, repoLabel, line)
		}
		if fields[1] == "missing" {
			return fmt.Errorf("%s: %s object %q is missing in cat-file --batch-check (%q)", phase, repoLabel, fields[0], line)
		}
		validOIDs[fields[0]] = fields[1]
	}
	for ref, oid := range refMap {
		objType, ok := validOIDs[oid]
		if !ok || (objType != "commit" && objType != "tag") {
			return fmt.Errorf("%s: %s ref %s -> %s has invalid or missing object type %q in batch-check", phase, repoLabel, ref, oid, objType)
		}
	}
	return nil
}

func verifyExactRefMapEqual(repoLabel, phase string, gotMap, wantMap map[string]string) error {
	for ref, gotOID := range gotMap {
		wantOID, ok := wantMap[ref]
		if !ok {
			return fmt.Errorf("%s: %s baseline refs has unexpected extra ref %q = %q", phase, repoLabel, ref, gotOID)
		}
		if gotOID != wantOID {
			return fmt.Errorf("%s: %s baseline ref %q = %q, want %q", phase, repoLabel, ref, gotOID, wantOID)
		}
	}
	for ref, wantOID := range wantMap {
		if _, ok := gotMap[ref]; !ok {
			return fmt.Errorf("%s: %s baseline refs missing required ref %q (want %q)", phase, repoLabel, ref, wantOID)
		}
	}
	return nil
}

func verifyExactRefMapTransition(repoLabel, phase, beforeRaw, gotRaw string, declared map[string]refTransitionRule, requireConsistentState bool) error {
	beforeMap, err := parseExactRefMap(beforeRaw)
	if err != nil {
		return fmt.Errorf("%s: %s parse before refs: %w", phase, repoLabel, err)
	}
	gotMap, err := parseExactRefMap(gotRaw)
	if err != nil {
		return fmt.Errorf("%s: %s parse got refs: %w", phase, repoLabel, err)
	}

	declaredNames := make([]string, 0, len(declared))
	for ref := range declared {
		declaredNames = append(declaredNames, ref)
	}
	sort.Strings(declaredNames)

	changedDeclared := 0
	unchangedDeclared := 0
	for _, ref := range declaredNames {
		rule := declared[ref]
		beforeVal := beforeMap[ref]
		if beforeVal != rule.OldValue {
			return fmt.Errorf("%s: %s refs mismatch: declared target ref %q had before=%q, expected old=%q",
				phase, repoLabel, ref, beforeVal, rule.OldValue)
		}
		gotVal := gotMap[ref]
		allowed := false
		for _, candidate := range rule.AllowedValues {
			if gotVal == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%s: %s refs mismatch: target ref %q = %q not in allowed values %q (old=%q)",
				phase, repoLabel, ref, gotVal, rule.AllowedValues, rule.OldValue)
		}
		if gotVal == rule.OldValue {
			unchangedDeclared++
		} else {
			changedDeclared++
		}
	}

	if requireConsistentState && len(declared) > 1 && changedDeclared > 0 && unchangedDeclared > 0 {
		return fmt.Errorf("%s: %s refs mismatch: partial target ref transition (changed=%d unchanged=%d) in got=%v",
			phase, repoLabel, changedDeclared, unchangedDeclared, gotMap)
	}

	gotNames := make([]string, 0, len(gotMap))
	for ref := range gotMap {
		gotNames = append(gotNames, ref)
	}
	sort.Strings(gotNames)
	for _, ref := range gotNames {
		if _, isDeclared := declared[ref]; isDeclared {
			continue
		}
		gotVal := gotMap[ref]
		beforeVal, existed := beforeMap[ref]
		if !existed {
			return fmt.Errorf("%s: %s refs mismatch: unexpected extra ref %q = %q (not in baseline refs and not a declared target ref)",
				phase, repoLabel, ref, gotVal)
		}
		if gotVal != beforeVal {
			return fmt.Errorf("%s: %s refs mismatch: non-target ref %q mutated from %q to %q",
				phase, repoLabel, ref, beforeVal, gotVal)
		}
	}

	beforeNames := make([]string, 0, len(beforeMap))
	for ref := range beforeMap {
		beforeNames = append(beforeNames, ref)
	}
	sort.Strings(beforeNames)
	for _, ref := range beforeNames {
		if _, isDeclared := declared[ref]; isDeclared {
			continue
		}
		beforeVal := beforeMap[ref]
		gotVal, existed := gotMap[ref]
		if !existed {
			return fmt.Errorf("%s: %s refs mismatch: non-target ref %q (was %q) was unexpectedly deleted",
				phase, repoLabel, ref, beforeVal)
		}
		if gotVal != beforeVal {
			return fmt.Errorf("%s: %s refs mismatch: non-target ref %q mutated from %q to %q",
				phase, repoLabel, ref, beforeVal, gotVal)
		}
	}

	return nil
}

func (f *networkRecoveryCaseFixture) checkWorkRefsForPhase(phase, beforeRaw, gotRaw string) error {
	beforeMap, err := parseExactRefMap(beforeRaw)
	if err != nil {
		return fmt.Errorf("%s: work parse before refs: %w", phase, err)
	}
	switch {
	case strings.HasPrefix(phase, "FetchDisconnectRetry/"), strings.HasPrefix(phase, "InFlightDeadlineRecovery/"):
		wantInitial := map[string]string{
			"refs/heads/local-keep":         f.baseCommit,
			"refs/heads/main":               f.baseCommit,
			"refs/remotes/origin/main":      f.baseCommit,
			"refs/remotes/origin/peer-keep": f.peerKeepCommit,
			"refs/tags/v0.1-keep":           f.baseCommit,
		}
		if err := verifyExactRefMapEqual("work", phase, beforeMap, wantInitial); err != nil {
			return err
		}
	case strings.HasPrefix(phase, "PushDisconnectRetry/"):
		if !isHex(f.baselineWorkHeadOID) || f.baselineWorkHeadOID == f.baseCommit {
			return fmt.Errorf("%s: invalid work baseline HEAD OID %q (base=%q)", phase, f.baselineWorkHeadOID, f.baseCommit)
		}
		wantInitial := map[string]string{
			"refs/heads/local-keep":         f.baseCommit,
			"refs/heads/main":               f.baselineWorkHeadOID,
			"refs/remotes/origin/main":      f.baseCommit,
			"refs/remotes/origin/peer-keep": f.peerKeepCommit,
			"refs/tags/v0.1-keep":           f.baseCommit,
		}
		if err := verifyExactRefMapEqual("work", phase, beforeMap, wantInitial); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown phase %q for work ref map verification", phase)
	}

	switch phase {
	case "FetchDisconnectRetry/before", "PushDisconnectRetry/before", "InFlightDeadlineRecovery/before":
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, nil, true)
	case "FetchDisconnectRetry/afterFault":
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare target OID %q", phase, f.baselineBareMainOID)
		}
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baseCommit, f.baselineBareMainOID}},
			"refs/tags/v1.0-fetch":     {OldValue: "", AllowedValues: []string{"", f.baselineBareMainOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "FetchDisconnectRetry/afterRetry":
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare target OID %q", phase, f.baselineBareMainOID)
		}
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baselineBareMainOID}},
			"refs/tags/v1.0-fetch":     {OldValue: "", AllowedValues: []string{f.baselineBareMainOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "PushDisconnectRetry/afterPushNoteFault":
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baseCommit, f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "PushDisconnectRetry/afterPushNoteRetry":
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "PushDisconnectRetry/afterPushToFault":
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main":           {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
			"refs/remotes/origin/published/main": {OldValue: "", AllowedValues: []string{"", f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, false)
	case "PushDisconnectRetry/afterAllRetry":
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main":           {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
			"refs/remotes/origin/published/main": {OldValue: "", AllowedValues: []string{f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "InFlightDeadlineRecovery/afterTimeout":
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare target OID %q", phase, f.baselineBareMainOID)
		}
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baseCommit, f.baselineBareMainOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	case "InFlightDeadlineRecovery/afterRetry":
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare target OID %q", phase, f.baselineBareMainOID)
		}
		declared := map[string]refTransitionRule{
			"refs/remotes/origin/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baselineBareMainOID}},
		}
		return verifyExactRefMapTransition("work", phase, beforeRaw, gotRaw, declared, true)
	default:
		return fmt.Errorf("unknown phase %q for work ref map verification", phase)
	}
}

func (f *networkRecoveryCaseFixture) checkBareRefsForPhase(phase, beforeRaw, gotRaw string) error {
	beforeMap, err := parseExactRefMap(beforeRaw)
	if err != nil {
		return fmt.Errorf("%s: bare parse before refs: %w", phase, err)
	}
	switch {
	case strings.HasPrefix(phase, "FetchDisconnectRetry/"):
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare baseline main OID %q (base=%q)", phase, f.baselineBareMainOID, f.baseCommit)
		}
		wantInitial := map[string]string{
			"refs/heads/main":      f.baselineBareMainOID,
			"refs/heads/peer-keep": f.peerKeepCommit,
			"refs/tags/v0.1-keep":  f.baseCommit,
			"refs/tags/v1.0-fetch": f.baselineBareMainOID,
		}
		if err := verifyExactRefMapEqual("bare", phase, beforeMap, wantInitial); err != nil {
			return err
		}
	case strings.HasPrefix(phase, "InFlightDeadlineRecovery/"):
		if !isHex(f.baselineBareMainOID) || f.baselineBareMainOID == f.baseCommit {
			return fmt.Errorf("%s: invalid bare baseline main OID %q (base=%q)", phase, f.baselineBareMainOID, f.baseCommit)
		}
		wantInitial := map[string]string{
			"refs/heads/main":      f.baselineBareMainOID,
			"refs/heads/peer-keep": f.peerKeepCommit,
			"refs/tags/v0.1-keep":  f.baseCommit,
		}
		if err := verifyExactRefMapEqual("bare", phase, beforeMap, wantInitial); err != nil {
			return err
		}
	case strings.HasPrefix(phase, "PushDisconnectRetry/"):
		wantInitial := map[string]string{
			"refs/heads/main":      f.baseCommit,
			"refs/heads/peer-keep": f.peerKeepCommit,
			"refs/tags/v0.1-keep":  f.baseCommit,
		}
		if err := verifyExactRefMapEqual("bare", phase, beforeMap, wantInitial); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown phase %q for bare ref map verification", phase)
	}

	switch phase {
	case "FetchDisconnectRetry/before", "FetchDisconnectRetry/afterFault", "FetchDisconnectRetry/afterRetry",
		"InFlightDeadlineRecovery/before", "InFlightDeadlineRecovery/afterTimeout", "InFlightDeadlineRecovery/afterRetry",
		"PushDisconnectRetry/before":
		return verifyExactRefMapTransition("bare", phase, beforeRaw, gotRaw, nil, true)
	case "PushDisconnectRetry/afterPushNoteFault":
		declared := map[string]refTransitionRule{
			"refs/heads/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baseCommit, f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("bare", phase, beforeRaw, gotRaw, declared, true)
	case "PushDisconnectRetry/afterPushNoteRetry":
		declared := map[string]refTransitionRule{
			"refs/heads/main": {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("bare", phase, beforeRaw, gotRaw, declared, true)
	case "PushDisconnectRetry/afterPushToFault":
		declared := map[string]refTransitionRule{
			"refs/heads/main":           {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
			"refs/heads/published/main": {OldValue: "", AllowedValues: []string{"", f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("bare", phase, beforeRaw, gotRaw, declared, false)
	case "PushDisconnectRetry/afterAllRetry":
		declared := map[string]refTransitionRule{
			"refs/heads/main":           {OldValue: f.baseCommit, AllowedValues: []string{f.baselineWorkHeadOID}},
			"refs/heads/published/main": {OldValue: "", AllowedValues: []string{f.baselineWorkHeadOID}},
		}
		return verifyExactRefMapTransition("bare", phase, beforeRaw, gotRaw, declared, true)
	default:
		return fmt.Errorf("unknown phase %q for bare ref map verification", phase)
	}
}

func (f *networkRecoveryCaseFixture) verifyFetchRefOracleCounterexamples(t *testing.T, beforeWork workRepoSnapshot, beforeBare bareRepoSnapshot, wantRemoteOID string) {
	t.Helper()
	w := ShellQuote(f.workDir)
	b := ShellQuote(f.bareDir)

	// 1. Live remote git update-ref counterexamples on work and bare via direct SSH + captureWorkSnapshot/captureBareSnapshot.
	f.sshOut(t, "git -C "+w+" update-ref refs/heads/unexpected-review "+ShellQuote(f.baseCommit))
	extraBranchSnap := f.captureWorkSnapshot(t)
	f.sshOut(t, "git -C "+w+" update-ref -d refs/heads/unexpected-review")
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", beforeWork.Refs, extraBranchSnap.Refs); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-review"`) {
		t.Fatalf("oracle failed to reject live extra branch refs/heads/unexpected-review in afterFault: %v", err)
	}
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, extraBranchSnap.Refs); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-review"`) {
		t.Fatalf("oracle failed to reject live extra branch refs/heads/unexpected-review in afterRetry: %v", err)
	}

	f.sshOut(t, "git -C "+w+" update-ref refs/tags/unexpected-tag "+ShellQuote(f.baseCommit))
	extraTagSnap := f.captureWorkSnapshot(t)
	f.sshOut(t, "git -C "+w+" update-ref -d refs/tags/unexpected-tag")
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, extraTagSnap.Refs); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/tags/unexpected-tag"`) {
		t.Fatalf("oracle failed to reject live extra tag refs/tags/unexpected-tag: %v", err)
	}

	f.sshOut(t, "git -C "+b+" update-ref refs/heads/unexpected-bare "+ShellQuote(f.baseCommit))
	extraBareBranchSnap := f.captureBareSnapshot(t)
	f.sshOut(t, "git -C "+b+" update-ref -d refs/heads/unexpected-bare")
	if err := f.checkBareRefsForPhase("FetchDisconnectRetry/afterFault", beforeBare.Refs, extraBareBranchSnap.Refs); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-bare"`) {
		t.Fatalf("oracle failed to reject live bare extra branch refs/heads/unexpected-bare: %v", err)
	}

	// Verify restored work and bare after live counterexample injection match post-Retry clean state.
	restoredWork := f.captureWorkSnapshot(t)
	restoredBare := f.captureBareSnapshot(t)
	f.assertWorkSentinelsAndCleanLocks(t, "FetchDisconnectRetry/afterRetry", restoredWork, beforeWork)
	f.assertBarePeerAndCleanLocks(t, "FetchDisconnectRetry/afterRetry", restoredBare)

	// 2. Exhaustive ref-map transition counterexamples (legal old/new states vs extra branch/tag, deleted non-target, mutated non-target, partial/corrupted target).
	baseWorkMap, err := parseExactRefMap(beforeWork.Refs)
	must(t, err)
	baseBareMap, err := parseExactRefMap(beforeBare.Refs)
	must(t, err)

	completeNewWorkMap := cloneRefMap(baseWorkMap)
	completeNewWorkMap["refs/remotes/origin/main"] = wantRemoteOID
	completeNewWorkMap["refs/tags/v1.0-fetch"] = wantRemoteOID

	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", beforeWork.Refs, formatExactRefMap(baseWorkMap)); err != nil {
		t.Fatalf("oracle rejected legal complete old work refs in afterFault: %v", err)
	}
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", beforeWork.Refs, formatExactRefMap(completeNewWorkMap)); err != nil {
		t.Fatalf("oracle rejected legal complete new work refs in afterFault: %v", err)
	}
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, formatExactRefMap(completeNewWorkMap)); err != nil {
		t.Fatalf("oracle rejected legal complete new work refs in afterRetry: %v", err)
	}
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, formatExactRefMap(baseWorkMap)); err == nil {
		t.Fatal("oracle accepted stale old work refs in afterRetry, want error")
	}

	partialWorkMap := cloneRefMap(baseWorkMap)
	partialWorkMap["refs/remotes/origin/main"] = wantRemoteOID
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", beforeWork.Refs, formatExactRefMap(partialWorkMap)); err == nil || !strings.Contains(err.Error(), "partial target ref transition") {
		t.Fatalf("oracle failed to reject partial target ref transition in afterFault: %v", err)
	}

	corruptedTargetWorkMap := cloneRefMap(completeNewWorkMap)
	corruptedTargetWorkMap["refs/remotes/origin/main"] = f.peerKeepCommit
	if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", beforeWork.Refs, formatExactRefMap(corruptedTargetWorkMap)); err == nil || !strings.Contains(err.Error(), "not in allowed values") {
		t.Fatalf("oracle failed to reject arbitrary target ref OID in work: %v", err)
	}

	for _, nonTargetRef := range []string{"refs/heads/main", "refs/heads/local-keep", "refs/remotes/origin/peer-keep", "refs/tags/v0.1-keep"} {
		deletedMap := cloneRefMap(completeNewWorkMap)
		delete(deletedMap, nonTargetRef)
		if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, formatExactRefMap(deletedMap)); err == nil || !strings.Contains(err.Error(), "unexpectedly deleted") {
			t.Fatalf("oracle failed to reject deletion of work non-target ref %s: %v", nonTargetRef, err)
		}
		mutatedMap := cloneRefMap(completeNewWorkMap)
		if mutatedMap[nonTargetRef] == wantRemoteOID {
			mutatedMap[nonTargetRef] = f.baseCommit
		} else {
			mutatedMap[nonTargetRef] = wantRemoteOID
		}
		if err := f.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", beforeWork.Refs, formatExactRefMap(mutatedMap)); err == nil || !strings.Contains(err.Error(), "non-target ref") {
			t.Fatalf("oracle failed to reject mutation of work non-target ref %s: %v", nonTargetRef, err)
		}
	}

	for _, extraBareRef := range []string{"refs/heads/unexpected-bare-branch", "refs/tags/unexpected-bare-tag"} {
		extraMap := cloneRefMap(baseBareMap)
		extraMap[extraBareRef] = f.baseCommit
		if err := f.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", beforeBare.Refs, formatExactRefMap(extraMap)); err == nil || !strings.Contains(err.Error(), "unexpected extra ref") {
			t.Fatalf("oracle failed to reject bare extra ref %s: %v", extraBareRef, err)
		}
	}
	for _, nonTargetBareRef := range []string{"refs/heads/main", "refs/heads/peer-keep", "refs/tags/v0.1-keep", "refs/tags/v1.0-fetch"} {
		deletedMap := cloneRefMap(baseBareMap)
		delete(deletedMap, nonTargetBareRef)
		if err := f.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", beforeBare.Refs, formatExactRefMap(deletedMap)); err == nil || !strings.Contains(err.Error(), "unexpectedly deleted") {
			t.Fatalf("oracle failed to reject deletion of bare ref %s: %v", nonTargetBareRef, err)
		}
		mutatedMap := cloneRefMap(baseBareMap)
		if mutatedMap[nonTargetBareRef] == f.baseCommit {
			mutatedMap[nonTargetBareRef] = f.peerKeepCommit
		} else {
			mutatedMap[nonTargetBareRef] = f.baseCommit
		}
		if err := f.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", beforeBare.Refs, formatExactRefMap(mutatedMap)); err == nil || !strings.Contains(err.Error(), "non-target ref") {
			t.Fatalf("oracle failed to reject mutation of bare ref %s: %v", nonTargetBareRef, err)
		}
	}
}

func (f *networkRecoveryCaseFixture) verifyPushRefOracleCounterexamples(t *testing.T, beforeWork workRepoSnapshot, beforeBare bareRepoSnapshot, wantPushOID string) {
	t.Helper()
	baseWorkMap, err := parseExactRefMap(beforeWork.Refs)
	must(t, err)
	baseBareMap, err := parseExactRefMap(beforeBare.Refs)
	must(t, err)

	// Legal old and new states for PushNote fault/retry on work and bare.
	noteNewWorkMap := cloneRefMap(baseWorkMap)
	noteNewWorkMap["refs/remotes/origin/main"] = wantPushOID
	noteNewBareMap := cloneRefMap(baseBareMap)
	noteNewBareMap["refs/heads/main"] = wantPushOID

	if err := f.checkWorkRefsForPhase("PushDisconnectRetry/afterPushNoteFault", beforeWork.Refs, formatExactRefMap(baseWorkMap)); err != nil {
		t.Fatalf("oracle rejected legal old work refs in afterPushNoteFault: %v", err)
	}
	if err := f.checkWorkRefsForPhase("PushDisconnectRetry/afterPushNoteFault", beforeWork.Refs, formatExactRefMap(noteNewWorkMap)); err != nil {
		t.Fatalf("oracle rejected legal new work refs in afterPushNoteFault: %v", err)
	}
	if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterPushNoteFault", beforeBare.Refs, formatExactRefMap(baseBareMap)); err != nil {
		t.Fatalf("oracle rejected legal old bare refs in afterPushNoteFault: %v", err)
	}
	if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterPushNoteFault", beforeBare.Refs, formatExactRefMap(noteNewBareMap)); err != nil {
		t.Fatalf("oracle rejected legal new bare refs in afterPushNoteFault: %v", err)
	}
	if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterPushNoteRetry", beforeBare.Refs, formatExactRefMap(baseBareMap)); err == nil {
		t.Fatal("oracle accepted stale old bare refs in afterPushNoteRetry, want error")
	}

	// Extra branch/tag and non-target deletion/mutation on bare and work during Push.
	allNewBareMap := cloneRefMap(noteNewBareMap)
	allNewBareMap["refs/heads/published/main"] = wantPushOID
	for _, extraRef := range []string{"refs/heads/unexpected-push-branch", "refs/tags/unexpected-push-tag"} {
		m := cloneRefMap(allNewBareMap)
		m[extraRef] = wantPushOID
		if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterAllRetry", beforeBare.Refs, formatExactRefMap(m)); err == nil || !strings.Contains(err.Error(), "unexpected extra ref") {
			t.Fatalf("oracle failed to reject bare extra ref %s in Push: %v", extraRef, err)
		}
	}
	for _, nonTargetBare := range []string{"refs/heads/peer-keep", "refs/tags/v0.1-keep"} {
		delMap := cloneRefMap(allNewBareMap)
		delete(delMap, nonTargetBare)
		if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterAllRetry", beforeBare.Refs, formatExactRefMap(delMap)); err == nil || !strings.Contains(err.Error(), "unexpectedly deleted") {
			t.Fatalf("oracle failed to reject bare deletion of %s in Push: %v", nonTargetBare, err)
		}
		mutMap := cloneRefMap(allNewBareMap)
		mutMap[nonTargetBare] = wantPushOID
		if err := f.checkBareRefsForPhase("PushDisconnectRetry/afterAllRetry", beforeBare.Refs, formatExactRefMap(mutMap)); err == nil || !strings.Contains(err.Error(), "non-target ref") {
			t.Fatalf("oracle failed to reject bare mutation of %s in Push: %v", nonTargetBare, err)
		}
	}
}
