package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sshServer is an sshd run as the current user on a free port of
// 127.0.0.1, which lets in one key made for the test.
type sshServer struct {
	dir      string
	port     int
	identity string
	// ssh runs the Mac's ssh without the user's configuration and known
	// hosts, which know nothing of this server.
	ssh string
}

func startSSHD(t *testing.T) *sshServer {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("no sshd on this machine")
	}
	dir := t.TempDir()
	for _, k := range []string{"host", "user"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-C", "branchline-test", "-N", "", "-f", filepath.Join(dir, k)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, err := os.ReadFile(filepath.Join(dir, "user.pub"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen here: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\n"+
		"StrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nLogLevel ERROR\n",
		port, filepath.Join(dir, "host"), filepath.Join(dir, "authorized_keys"), filepath.Join(dir, "pid"))
	must(t, os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(cfg), 0o600))

	var log strings.Builder
	cmd := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Skipf("sshd: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	wrapper := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nexec /usr/bin/ssh -F /dev/null -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \"$@\"\n"
	must(t, os.WriteFile(wrapper, []byte(script), 0o755))

	for i := 0; ; i++ {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			c.Close()
			break
		}
		if i == 50 {
			t.Skipf("sshd did not start: %s", log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &sshServer{dir: dir, port: port, identity: filepath.Join(dir, "user"), ssh: wrapper}
}

// executor opens dir on the server as the current user, sharing one
// connection between commands as the app does.
func (s *sshServer) executor(t *testing.T, dir string) *SSHExecutor {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ex := &SSHExecutor{Host: "127.0.0.1", Port: s.port, User: u.Username, Identity: s.identity, Dir: dir, SSH: s.ssh}
	t.Cleanup(func() {
		if s.ssh == "" {
			return
		}
		closeSSHControl(s.ssh, ex.controlPath(), "127.0.0.1")
	})
	return ex
}

// TestRealSSHServer works on a repository through a real sshd: what the
// server's shell gets must come out the same as on this Mac, whatever
// the names and messages hold.
func TestRealSSHServer(t *testing.T) {
	srv := startSSHD(t)
	dir := testRepo(t)
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r := New(srv.executor(t, dir))
	local := New(&LocalExecutor{Dir: dir})

	top, err := r.TopLevel()
	must(t, err)
	if want, _ := filepath.EvalSymlinks(dir); top != want && top != dir {
		t.Fatalf("top level %q, want %q", top, want)
	}

	// A file whose name the shell would expand, in the working tree.
	name := `naïve $(whoami) 'q' "d".txt`
	must(t, os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644))
	st, err := r.Status()
	must(t, err)
	found := false
	for _, f := range st.Unstaged {
		found = found || f.Path == name
	}
	if !found {
		t.Fatalf("unstaged %+v", st.Unstaged)
	}
	must(t, r.Stage(name))
	message := "Say \"hi\" & $(whoami) `id` 'q' $HOME\n\nBody; rm -rf nothing"
	must(t, r.Commit(message, false))
	subject, err := local.Output("log", "-1", "--format=%B")
	must(t, err)
	if strings.TrimSpace(subject) != message {
		t.Fatalf("message through ssh %q", subject)
	}
	cs, err := r.Log(LogOptions{Limit: 1})
	must(t, err)
	if len(cs) != 1 || cs[0].Subject != `Say "hi" & $(whoami) `+"`id`"+` 'q' $HOME` {
		t.Fatalf("log %+v", cs)
	}

	// Branch, push to a remote of the server, and stash.
	must(t, r.CreateBranch("feature/ssh-$x", "HEAD", true))
	st, err = r.Status()
	must(t, err)
	if st.Branch != "feature/ssh-$x" {
		t.Fatalf("branch %q", st.Branch)
	}
	_, err = r.Output("remote", "add", "origin", bare)
	must(t, err)
	must(t, r.Push("origin", "feature/ssh-$x", true, false, false))
	if out, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/feature/ssh-$x").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != cs[0].Hash {
		t.Fatalf("pushed %q %v, want %s", out, err, cs[0].Hash)
	}
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644))
	must(t, r.StashPush("wip $HOME `x`"))
	stashes, err := r.Stashes()
	must(t, err)
	if len(stashes) != 1 || !strings.Contains(stashes[0].Message, "wip $HOME `x`") {
		t.Fatalf("stashes %+v", stashes)
	}

	// A script runs in the repository with the server's shell.
	out, err := r.Script(`printf '%s' "$(git rev-parse --abbrev-ref HEAD)"`)
	must(t, err)
	if out != "feature/ssh-$x" {
		t.Fatalf("script printed %q", out)
	}

	// What the server shows matches what this Mac shows.
	remoteLog, err := r.Log(LogOptions{Limit: 20})
	must(t, err)
	localLog, err := local.Log(LogOptions{Limit: 20})
	must(t, err)
	if fmt.Sprint(remoteLog) != fmt.Sprint(localLog) {
		t.Fatalf("logs differ:\n%v\n%v", remoteLog, localLog)
	}
}

func startJumpSSHD(t *testing.T, targetPort int, identity string, logPath string) *sshServer {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("no sshd on this machine")
	}
	dir := t.TempDir()
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-C", "branchline-test", "-N", "", "-f", filepath.Join(dir, "host")).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen here: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cfg := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\n"+
		"AllowTcpForwarding yes\nDisableForwarding no\nPermitOpen 127.0.0.1:%d\n"+
		"AuthorizedKeysFile %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nLogLevel VERBOSE\n",
		port, filepath.Join(dir, "host"), filepath.Join(dir, "pid"), targetPort, filepath.Join(dir, "authorized_keys"))
	must(t, os.WriteFile(filepath.Join(dir, "sshd_config"), []byte(cfg), 0o600))
	pub, err := os.ReadFile(identity + ".pub")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600))

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	must(t, err)
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command("/usr/sbin/sshd", "-D", "-e", "-f", filepath.Join(dir, "sshd_config"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Skipf("sshd: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	for i := 0; ; i++ {
		select {
		case <-exited:
			data, _ := os.ReadFile(logPath)
			t.Fatalf("jump sshd exited before accepting connections: %s", data)
		default:
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
		if i == 50 {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("jump sshd did not accept connections: %s", data)
		}
	}
	return &sshServer{dir: dir, port: port, identity: identity}
}

// TestSSHConfigJumpHost reads and writes through both a standard
// ProxyJump and an explicit ProxyCommand. The executor leaves Port at 0 so
// each alias supplies its own port.
func TestSSHConfigJumpHost(t *testing.T) {
	target := startSSHD(t)
	u, err := user.Current()
	must(t, err)
	cfgDir := t.TempDir()

	t.Run("ProxyJump", func(t *testing.T) {
		jumpLog := filepath.Join(t.TempDir(), "jump-sshd.log")
		jump := startJumpSSHD(t, target.port, target.identity, jumpLog)
		known := filepath.Join(cfgDir, "proxyjump-known")
		must(t, os.WriteFile(known, knownHostLines(t, filepath.Join(target.dir, "host.pub"), target.port, filepath.Join(jump.dir, "host.pub"), jump.port), 0o600))
		configPath := filepath.Join(cfgDir, "proxyjump-client.config")
		repoAlias, jumpAlias := uniqueSSHAliases(t, "proxyjump", configPath)
		client, _ := sshConfigClient(t, cfgDir, "proxyjump-client", fmt.Sprintf("Host %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n\n"+
			"Host %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  ProxyJump %s\n  UserKnownHostsFile %s\n",
			jumpAlias, jump.port, u.Username, target.identity, known, repoAlias, target.port, u.Username, target.identity, jumpAlias, known))
		checkJumpRepo(t, target, client, repoAlias, "")
		assertJumpLog(t, jumpLog)
		assertProxyJumpConfig(t, configPath, repoAlias, target.port)
	})
	t.Run("ProxyCommand", func(t *testing.T) {
		jumpLog := filepath.Join(t.TempDir(), "jump-sshd.log")
		jump := startJumpSSHD(t, target.port, target.identity, jumpLog)
		known := filepath.Join(cfgDir, "proxycommand-known")
		must(t, os.WriteFile(known, knownHostLines(t, filepath.Join(target.dir, "host.pub"), target.port, filepath.Join(jump.dir, "host.pub"), jump.port), 0o600))
		configPath := filepath.Join(cfgDir, "proxycommand.config")
		repoAlias, jumpAlias := uniqueSSHAliases(t, "proxycommand", configPath)
		client := filepath.Join(cfgDir, "proxycommand-ssh")
		trace := filepath.Join(cfgDir, "proxycommand-trace")
		config := fmt.Sprintf("Host %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n\n"+
			"Host %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  ProxyCommand exec /bin/sh -c \"%s -F %s -W [%%h]:%%p %s\"\n  UserKnownHostsFile %s\n",
			jumpAlias, jump.port, u.Username, target.identity, known, repoAlias, target.port, u.Username, target.identity, client, configPath, jumpAlias, known)
		must(t, os.WriteFile(configPath, []byte(config), 0o600))
		script := "#!/bin/sh\nprintf '%s\n' \"$*\" >> " + ShellQuote(trace) + "\nexec /usr/bin/ssh -F " + ShellQuote(configPath) + " -o LogLevel=ERROR \"$@\"\n"
		must(t, os.WriteFile(client, []byte(script), 0o755))
		checkJumpRepo(t, target, client, repoAlias, trace)
		assertJumpLog(t, jumpLog)
	})
}

func knownHostLines(t *testing.T, targetPub string, targetPort int, jumpPub string, jumpPort int) []byte {
	t.Helper()
	return []byte(knownHostLine(t, targetPub, targetPort) + "\n" + knownHostLine(t, jumpPub, jumpPort) + "\n")
}

func knownHostLine(t *testing.T, publicKey string, port int) string {
	t.Helper()
	text, err := os.ReadFile(publicKey)
	must(t, err)
	fields := strings.Fields(string(text))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		t.Fatalf("public key %s: %q", publicKey, text)
	}
	return fmt.Sprintf("[127.0.0.1]:%d %s %s", port, fields[0], fields[1])
}

func sshConfigClient(t *testing.T, dir, traceName, config string) (string, string) {
	t.Helper()
	client := filepath.Join(dir, traceName+"-ssh")
	trace := filepath.Join(dir, traceName)
	must(t, os.WriteFile(filepath.Join(dir, traceName+".config"), []byte(config), 0o600))
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" >> " + ShellQuote(trace) + "\nexec /usr/bin/ssh -F " + ShellQuote(filepath.Join(dir, traceName+".config")) + " -o LogLevel=ERROR \"$@\"\n"
	must(t, os.WriteFile(client, []byte(script), 0o755))
	return client, trace
}

func uniqueSSHAliases(t *testing.T, kind, configPath string) (string, string) {
	t.Helper()
	sum := sha256.Sum256([]byte(configPath))
	id := hex.EncodeToString(sum[:8])
	return "repo-" + kind + "-" + id, "jump-" + kind + "-" + id
}

func closeSSHControl(program, controlPath, destination string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "-O", "exit", "-o", "ControlPath="+controlPath, destination)
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
}

func assertJumpLog(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	text := string(data)
	if strings.Count(text, "Accepted publickey") < 2 {
		t.Fatalf("jump log lacks two authenticated sessions: %s", text)
	}
}

func assertProxyJumpConfig(t *testing.T, configPath, alias string, targetPort int) {
	t.Helper()
	cmd := exec.Command("/usr/bin/ssh", "-G", "-F", configPath, alias)
	out, err := cmd.Output()
	must(t, err)
	if !strings.Contains(string(out), "\nport "+strconv.Itoa(targetPort)+"\n") || !strings.Contains(string(out), "\nproxyjump jump-") {
		t.Fatalf("resolved ProxyJump config: %s", out)
	}
}

func checkJumpRepo(t *testing.T, target *sshServer, client, alias, trace string) {
	t.Helper()
	dir := t.TempDir()
	init := exec.Command("git", "init", "-q", "-b", "main", dir)
	init.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))

	ex := target.executor(t, dir)
	ex.Host = alias
	ex.Port = 0
	ex.User = ""
	ex.Identity = ""
	ex.SSHKeys = []string{target.identity}
	ex.SSH = client
	t.Cleanup(func() { closeSSHControl(client, ex.ControlPath(), alias) })
	repo := New(ex)

	name := "jump note.txt"
	must(t, os.WriteFile(filepath.Join(dir, name), []byte("through jump\n"), 0o644))
	st, err := repo.Status()
	must(t, err)
	seen := false
	for _, f := range st.Unstaged {
		seen = seen || f.Path == name
	}
	if !seen {
		t.Fatalf("status through jump: %+v", st.Unstaged)
	}
	must(t, repo.Stage(name))
	must(t, repo.Commit("through jump", false))
	cs, err := repo.Log(LogOptions{Limit: 1, Revs: []string{"HEAD"}})
	must(t, err)
	if len(cs) != 1 || cs[0].Subject != "through jump" {
		t.Fatalf("commit through jump: %+v", cs)
	}

	control := func(action string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, client, "-O", action, "-o", "ControlPath="+ex.ControlPath(), alias)
		cmd.WaitDelay = time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("control %s: %v %s", action, err, out)
		}
	}
	control("check")
	control("exit")

	must(t, os.WriteFile(filepath.Join(dir, name), []byte("reconnected\n"), 0o644))
	st, err = repo.Status()
	must(t, err)
	modified := false
	for _, f := range st.Unstaged {
		modified = modified || f.Path == name
	}
	if !modified {
		t.Fatalf("status after reconnect: %+v", st.Unstaged)
	}
	must(t, repo.Stage(name))
	must(t, repo.Commit("after reconnect", false))
	cs, err = repo.Log(LogOptions{Limit: 1, Revs: []string{"HEAD"}})
	must(t, err)
	if len(cs) != 1 || cs[0].Subject != "after reconnect" {
		t.Fatalf("commit after reconnect: %+v", cs)
	}
	if trace == "" {
		return
	}
	traced, err := os.ReadFile(trace)
	must(t, err)
	want := fmt.Sprintf("-W [127.0.0.1]:%d", target.port)
	if strings.Count(string(traced), want) < 2 || strings.Count(string(traced), alias) < 2 {
		t.Fatalf("proxy forwards %q, want at least two %q for %s", traced, want, alias)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestRealSSHServerAsksPassphrase logs in with a key that has a
// passphrase: the real ssh asks for it through AskPass, once for the
// connection the commands then share.
func TestRealSSHServerAsksPassphrase(t *testing.T) {
	isolateCredentials(t)
	srv := startSSHD(t)
	key := filepath.Join(srv.dir, "locked")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-C", "branchline-test", "-N", "s3cret pass", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	must(t, err)
	f, err := os.OpenFile(filepath.Join(srv.dir, "authorized_keys"), os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.Write(pub)
	must(t, err)
	must(t, f.Close())

	dir := testRepo(t)
	ex := srv.executor(t, dir)
	ex.Identity = key
	r := &recorder{}
	ex.AskPass = NewAskPass(r.prompt)
	defer ex.AskPass.Close()
	repo := New(ex)
	for i := 0; i < 3; i++ {
		if _, err := repo.Status(); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.asked) != 1 || !strings.Contains(r.asked[0], "passphrase") {
		t.Fatalf("asked %q", r.asked)
	}

	// A wrong answer is a failed login, not a hang.
	ex2 := srv.executor(t, dir)
	ex2.Identity, ex2.NoMultiplex = key, true
	wrong := NewAskPass(func(string) (string, bool) { return "wrong", true })
	defer wrong.Close()
	ex2.AskPass = wrong
	done := make(chan error, 1)
	go func() { _, err := New(ex2).Status(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("logged in with a wrong passphrase")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a wrong passphrase hung")
	}
}
