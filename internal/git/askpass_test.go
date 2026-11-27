package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// isolateCredentials keeps git away from the user's credential helpers
// and temporary folder.
func isolateCredentials(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return tmp
}

// recorder answers like a user: a name for a user name, a password for
// anything else, and keeps what it was asked.
type recorder struct {
	mu     sync.Mutex
	asked  []string
	cancel bool
}

func (r *recorder) prompt(p string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, p)
	if r.cancel {
		return "", false
	}
	if strings.HasPrefix(p, "Username") {
		return "ada", true
	}
	return "s3cret pass", true
}

func credentialFill(t *testing.T, ex Executor) (string, error) {
	t.Helper()
	out, err := ex.Run(context.Background(), []byte("protocol=https\nhost=example.com\n\n"), "credential", "fill")
	return string(out), err
}

// noSecretOnDisk checks that the answer is in no file left behind.
func noSecretOnDisk(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasPrefix(info.Name(), "branchline-ap.") {
				t.Errorf("pipe %s left behind", path)
			}
			if info.Mode().IsRegular() {
				if data, _ := os.ReadFile(path); strings.Contains(string(data), "s3cret") {
					t.Errorf("the password is written in %s", path)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAskPassLocal(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()
	ex := &LocalExecutor{Dir: dir, AskPass: ask}
	out, err := credentialFill(t, ex)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "username=ada\n") || !strings.Contains(out, "password=s3cret pass\n") {
		t.Fatalf("credential fill printed %q", out)
	}
	if len(r.asked) != 2 || !strings.Contains(r.asked[0], "Username for 'https://example.com'") || !strings.Contains(r.asked[1], "Password for 'https://ada@example.com'") {
		t.Fatalf("asked %q", r.asked)
	}
	noSecretOnDisk(t, tmp, dir)

	// Cancelling stops git, and what it printed stays readable.
	r.cancel = true
	if _, err := credentialFill(t, ex); err == nil {
		t.Fatal("credential fill went on after the user cancelled")
	} else if strings.Contains(err.Error(), askMarker) {
		t.Fatalf("the question leaked into the error: %v", err)
	}
}

// askingSSH is an ssh that asks for the passphrase of its key with
// SSH_ASKPASS, as ssh does without a terminal, then runs the command here.
const askingSSH = `#!/bin/sh
batch=
while [ $# -gt 1 ]; do
  case "$1" in
    -o) [ "$2" = BatchMode=no ] && batch=no; shift 2 ;;
    -p|-i|-l) shift 2 ;;
    *) break ;;
  esac
done
host=$1; shift
[ "$batch" = no ] || { echo "Permission denied (publickey)" >&2; exit 255; }
if [ ! -e "$LOGIN_DONE" ]; then
  [ "$SSH_ASKPASS_REQUIRE" = force ] || exit 255
  pass=$("$SSH_ASKPASS" "Enter passphrase for key '/k/id_ed25519': ") || { echo "Permission denied" >&2; exit 255; }
  [ "$pass" = "s3cret pass" ] || { echo "Permission denied" >&2; exit 255; }
  : > "$LOGIN_DONE"
fi
exec sh -c "$1"
`

func TestAskPassSSH(t *testing.T) {
	tmp := isolateCredentials(t)
	dir := testRepo(t)
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(askingSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOGIN_DONE", filepath.Join(t.TempDir(), "login"))

	// Without AskPass, the login cannot ask.
	plain := &SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true}
	if _, err := New(plain).TopLevel(); err == nil {
		t.Fatal("logged in without a passphrase")
	}

	r := &recorder{}
	ask := NewAskPass(r.prompt)
	defer ask.Close()
	ex := &SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true, AskPass: ask}
	if _, err := New(ex).TopLevel(); err != nil {
		t.Fatal(err)
	}
	if len(r.asked) != 1 || !strings.Contains(r.asked[0], "Enter passphrase for key '/k/id_ed25519'") {
		t.Fatalf("login asked %q", r.asked)
	}

	// git on the server asks through the script made there, and the
	// answer comes back over another channel.
	r.asked = nil
	out, err := credentialFill(t, ex)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "username=ada\n") || !strings.Contains(out, "password=s3cret pass\n") {
		t.Fatalf("credential fill on the server printed %q", out)
	}
	if len(r.asked) != 2 {
		t.Fatalf("server asked %q", r.asked)
	}
	if cmd := ex.RemoteCommand("status"); strings.Contains(cmd, "ASKPASS") {
		t.Errorf("a local command gets the askpass script: %s", cmd)
	}
	if cmd := ex.RemoteCommand("fetch", "origin"); !strings.Contains(cmd, "GIT_ASKPASS=") || !strings.Contains(cmd, `rm -f "$__bl_ap"`) {
		t.Errorf("fetch has no askpass: %s", cmd)
	}
	noSecretOnDisk(t, tmp, dir)
}

func TestAskWriterKeepsOutput(t *testing.T) {
	var got []string
	var mu sync.Mutex
	w := &askWriter{answer: func(pipe, prompt string, remote bool) {
		mu.Lock()
		got = append(got, pipe+"|"+prompt)
		mu.Unlock()
	}}
	if _, err := w.Write([]byte("remote: counting\npartial " + askMarker + "L /tmp/branchline-ap.abc Password: \n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(askMarker + "L /etc/passwd x\n" + "fatal: done")); err != nil {
		t.Fatal(err)
	}
	w.pending.Wait()
	if text := w.text(); text != "remote: counting\npartial fatal: done" {
		t.Errorf("stderr %q", text)
	}
	if len(got) != 1 || got[0] != "/tmp/branchline-ap.abc|Password: " {
		t.Errorf("questions %q", got)
	}
}
