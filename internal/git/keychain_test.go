package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSecurity is macOS's security for tests: it keeps each item in a
// file of $FAKE_KEYCHAIN and logs its command lines, never touching the
// real keychain.
const fakeSecurity = `#!/bin/sh
db=$FAKE_KEYCHAIN
handle() {
  cmd=$1; shift; acct=; svc=; pw=
  while [ $# -gt 0 ]; do
    case $1 in
      -a) acct=$2; shift 2 ;;
      -s) svc=$2; shift 2 ;;
      -l) shift 2 ;;
      -w) if [ $# -gt 1 ]; then pw=$2; shift 2; else shift; fi ;;
      *) shift ;;
    esac
  done
  f="$db/$(printf '%s/%s' "$svc" "$acct" | shasum | cut -c1-16)"
  case $cmd in
    add-generic-password) printf '%s' "$pw" > "$f"; printf '%s/%s\n' "$svc" "$acct" >> "$db/items.log" ;;
    find-generic-password) [ -f "$f" ] || { echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2; return 44; }; cat "$f"; echo ;;
    delete-generic-password) [ -f "$f" ] || return 44; rm -f "$f" ;;
    *) return 1 ;;
  esac
}
if [ "$1" = -i ]; then
  echo "-i" >> "$db/argv.log"
  while IFS= read -r line; do
    line=$(printf '%s' "$line" | sed 's/[$` + "`" + `]/\\&/g')
    eval "set -- $line"
    handle "$@" || echo "security: $1 returned -25300" >&2
  done
  exit 0
fi
echo "$*" >> "$db/argv.log"
handle "$@"
`

func fakeKeychain(t *testing.T) (*Keychain, string) {
	t.Helper()
	db := t.TempDir()
	bin := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(bin, []byte(fakeSecurity), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KEYCHAIN", db)
	return &Keychain{Program: bin}, db
}

func TestKeychainSaveFindDelete(t *testing.T) {
	k, db := fakeKeychain(t)
	if _, ok := k.Find("https://ada@example.com"); ok {
		t.Fatal("found an item never saved")
	}
	secret := `pa ss"w\ord$x`
	if err := k.Save("https://ada@example.com", secret); err != nil {
		t.Fatal(err)
	}
	if got, ok := k.Find("https://ada@example.com"); !ok || got != secret {
		t.Fatalf("found %q %v", got, ok)
	}
	log, _ := os.ReadFile(filepath.Join(db, "argv.log"))
	if strings.Contains(string(log), "pa ss") {
		t.Fatalf("the secret was on a command line: %s", log)
	}
	items, _ := os.ReadFile(filepath.Join(db, "items.log"))
	if strings.TrimSpace(string(items)) != "Branchline/https://ada@example.com" {
		t.Fatalf("saved under %q", items)
	}
	if err := k.Delete("https://ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := k.Find("https://ada@example.com"); ok {
		t.Fatal("delete left the item")
	}
	if err := k.Save("a", "two\nlines"); err == nil {
		t.Fatal("saved a secret with a new line")
	}
}

func TestSecretAccount(t *testing.T) {
	for prompt, want := range map[string]string{
		"Password for 'https://ada@example.com': ":                "https://ada@example.com",
		"Username for 'https://example.com': ":                    "username https://example.com",
		"ada@devbox's password: ":                                 "ssh://ada@devbox",
		"Enter passphrase for key '/home/ada/.ssh/id_ed25519': ":  "key:/home/ada/.ssh/id_ed25519",
		"Are you sure you want to continue connecting (yes/no)? ": "",
	} {
		if got := SecretAccount(prompt); got != want {
			t.Errorf("%q: %q, want %q", prompt, got, want)
		}
	}
}

// rememberer answers like a user who ticks Remember in Keychain.
type rememberer struct {
	mu    sync.Mutex
	asked []Question
}

func (r *rememberer) ask(q Question) Reply {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, q)
	if strings.HasPrefix(q.Prompt, "Username") {
		return Reply{Answer: "ada", OK: true, Remember: true}
	}
	return Reply{Answer: "s3cret pass", OK: true, Remember: true}
}

func TestAskPassRemembersInKeychain(t *testing.T) {
	isolateCredentials(t)
	dir := testRepo(t)
	k, _ := fakeKeychain(t)
	r := &rememberer{}
	ask := &AskPass{Ask: r.ask, Secrets: k}
	defer ask.Close()
	ex := &LocalExecutor{Dir: dir, AskPass: ask}
	if out, err := credentialFill(t, ex); err != nil || !strings.Contains(out, "password=s3cret pass") {
		t.Fatalf("fill %q %v", out, err)
	}
	if len(r.asked) != 2 || !r.asked[0].CanRemember || !r.asked[1].CanRemember {
		t.Fatalf("asked %+v", r.asked)
	}
	if got, ok := k.Find("https://ada@example.com"); !ok || got != "s3cret pass" {
		t.Fatalf("keychain has %q %v", got, ok)
	}

	// The next time, the keychain answers and nobody is asked.
	r.asked = nil
	if out, err := credentialFill(t, ex); err != nil || !strings.Contains(out, "username=ada") || !strings.Contains(out, "password=s3cret pass") {
		t.Fatalf("second fill %q %v", out, err)
	}
	if len(r.asked) != 0 {
		t.Fatalf("asked again %+v", r.asked)
	}

	// A refused login forgets what the keychain answered.
	sess := ask.session(nil)
	if v, ok := sess.ask("Password for 'https://ada@example.com': "); !ok || v != "s3cret pass" {
		t.Fatalf("session answered %q", v)
	}
	sess.finish(errors.New("fatal: Authentication failed for 'https://example.com/'"))
	if _, ok := k.Find("https://ada@example.com"); ok {
		t.Fatal("a refused password stayed in the keychain")
	}

	// Without a keychain the dialog offers nothing to remember.
	r.asked = nil
	plain := &AskPass{Ask: r.ask}
	defer plain.Close()
	if _, err := credentialFill(t, &LocalExecutor{Dir: dir, AskPass: plain}); err != nil {
		t.Fatal(err)
	}
	if len(r.asked) == 0 || r.asked[0].CanRemember {
		t.Fatalf("asked %+v", r.asked)
	}
}

func TestAskedTwiceForgetsTheSavedAnswer(t *testing.T) {
	k, _ := fakeKeychain(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(k.Save("ssh://ada@devbox", "old"))
	r := &recorder{}
	ask := &AskPass{Prompt: r.prompt, Secrets: k}
	sess := ask.session(nil)
	if v, _ := sess.ask("ada@devbox's password: "); v != "old" {
		t.Fatalf("first answer %q", v)
	}
	// ssh asks again: the saved password was wrong, so the user is asked.
	if v, _ := sess.ask("ada@devbox's password: "); v != "s3cret pass" || len(r.asked) != 1 {
		t.Fatalf("second answer %q asked %q", v, r.asked)
	}
	sess.finish(nil)
	if _, ok := k.Find("ssh://ada@devbox"); ok {
		t.Fatal("the refused password stayed")
	}
}

func boundFill(t *testing.T, ex Executor, path string) (string, error) {
	t.Helper()
	out, err := ex.Run(context.Background(), []byte("protocol=https\nhost=example.com\npath="+path+"\n\n"), "credential", "fill")
	return string(out), err
}

func TestBoundCredentialsLocal(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()
	creds := []Credential{{URL: "https://old@example.com/owner/repo.git", Username: "x-access-token", Password: "s3cret-token"}}
	ex := &LocalExecutor{Dir: dir, AskPass: ask, Credentials: creds}
	out, err := boundFill(t, ex, "owner/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "username=x-access-token\n") || !strings.Contains(out, "password=s3cret-token\n") || len(r.asked) != 0 {
		t.Fatalf("fill %q asked %q", out, r.asked)
	}
	for _, a := range credentialConfig(creds) {
		if strings.Contains(a, "s3cret") || strings.Contains(a, "old@") {
			t.Fatalf("config holds %q", a)
		}
	}
	// Another repository of the host is not given the token.
	if out, err := boundFill(t, ex, "other/repo.git"); err != nil || strings.Contains(out, "s3cret-token") || len(r.asked) == 0 {
		t.Fatalf("other repository %q asked %q err %v", out, r.asked, err)
	}
	noSecretOnDisk(t, tmp, dir)
}

func TestBoundCredentialsSSH(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(askingSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	login := filepath.Join(t.TempDir(), "login")
	if err := os.WriteFile(login, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOGIN_DONE", login)
	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()
	ex := &SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true, AskPass: ask,
		Credentials: []Credential{{URL: "https://example.com/owner/repo", Username: "oauth2", Password: "s3cret-token"}}}
	out, err := boundFill(t, ex, "owner/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "username=oauth2\n") || !strings.Contains(out, "password=s3cret-token\n") || len(r.asked) != 0 {
		t.Fatalf("fill on the server %q asked %q", out, r.asked)
	}
	if cmd := ex.RemoteCommand("fetch", "origin"); strings.Contains(cmd, "s3cret") || !strings.Contains(cmd, "credential.https://example.com/owner/repo.helper=") {
		t.Fatalf("remote command %s", cmd)
	}
	if cmd := ex.RemoteCommand("status"); strings.Contains(cmd, "credential.") {
		t.Fatalf("status gets the credentials: %s", cmd)
	}
	noSecretOnDisk(t, tmp, dir)
}

func TestNoGlobalConfig(t *testing.T) {
	dir := testRepo(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[branchline]\n\tprobe = global\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	read := func(ex Executor) string {
		out, _ := ex.Run(context.Background(), nil, "config", "--get", "branchline.probe")
		return strings.TrimSpace(string(out))
	}
	if got := read(&LocalExecutor{Dir: dir}); got != "global" {
		t.Fatalf("with the global file %q", got)
	}
	if got := read(&LocalExecutor{Dir: dir, NoGlobalConfig: true}); got != "" {
		t.Fatalf("without the global file %q", got)
	}
	ssh := &SSHExecutor{Host: "devbox", Dir: dir, NoGlobalConfig: true}
	if cmd := ssh.RemoteCommand("status"); !strings.Contains(cmd, "GIT_CONFIG_GLOBAL=/dev/null git ") {
		t.Fatalf("remote command %s", cmd)
	}
}

func TestWriteFileKeepsTheText(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	text := "[{\"script\": \"echo 'it''s' $HOME \\\\ `x`\"}]\n\né"
	if err := r.WriteFile(".branchline/custom-commands.json", text); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".branchline", "custom-commands.json"))
	if err != nil || string(got) != text {
		t.Fatalf("written %q %v", got, err)
	}
	if err := r.WriteFile("../escape", "x"); err == nil {
		t.Fatal("wrote outside the repository")
	}
}
