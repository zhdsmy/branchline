package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptRunsInTopFolder(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(&LocalExecutor{Dir: filepath.Join(dir, "sub")})
	out, err := r.Script(`pwd; echo "it's $HOME" >/dev/null; printf '%s' 'a b'`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "a b") || !strings.Contains(out, filepath.Base(dir)) || strings.Contains(out, "sub") {
		t.Fatalf("script output %q", out)
	}
	if _, err := r.Script("exit 3"); err == nil {
		t.Fatal("a failing script should fail")
	}
	if _, err := r.Script("  "); err == nil {
		t.Fatal("an empty script should fail")
	}
	got, err := r.Output("rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(got) != "true" {
		t.Fatalf("output %q %v", got, err)
	}
}

func TestScriptOverSSHQuotesTheScript(t *testing.T) {
	ex := &SSHExecutor{Host: "h", Dir: "/srv/repo"}
	cmd := ex.RemoteCommand("-c", "alias.branchlinescript=!echo 'x y'", "branchlinescript")
	if !strings.Contains(cmd, `'alias.branchlinescript=!echo '\''x y'\'''`) {
		t.Fatalf("remote command %q", cmd)
	}
}
