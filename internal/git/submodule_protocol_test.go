package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fileTransportClosed keeps these tests measuring git's default refusal
// of the file transport. The process environment must not open it —
// GIT_CONFIG_COUNT pairs and GIT_CONFIG_PARAMETERS are how such an
// override would arrive — and empty global and system files keep this
// machine's configuration from deciding in git's place.
func fileTransportClosed(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		key, value, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") && strings.EqualFold(value, "protocol.file.allow") {
			n := strings.TrimPrefix(key, "GIT_CONFIG_KEY_")
			if strings.EqualFold(os.Getenv("GIT_CONFIG_VALUE_"+n), "always") {
				t.Fatalf("the environment opens the file transport: %s", key)
			}
		}
		if key == "GIT_CONFIG_PARAMETERS" && strings.Contains(strings.ToLower(value), "protocol.file.allow=always") {
			t.Fatalf("the environment opens the file transport: %s", key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// nestedSubmoduleFixture builds a repository whose only submodule is
// another local one, and returns it with the inner repository's path.
func nestedSubmoduleFixture(t *testing.T) (middle, inner string) {
	t.Helper()
	inner = initRepo(t)
	writeFile(t, inner, "inner.txt", "inner\n")
	gitRun(t, inner, "add", ".")
	gitRun(t, inner, "commit", "-q", "-m", "inner")

	middle = initRepo(t)
	writeFile(t, middle, "middle.txt", "middle\n")
	gitRun(t, middle, "add", ".")
	gitRun(t, middle, "commit", "-q", "-m", "middle")
	// The override belongs to this fixture alone: the inner URL is a
	// local path of the test, which git refuses by default.
	gitRun(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", "--", inner, "deep")
	gitRun(t, middle, "commit", "-q", "-m", "add inner")
	return middle, inner
}

func submoduleTarget(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// A recursive submodule add must not open the file transport for the
// nested URLs: they come from the added repository's .gitmodules, which
// its author wrote. The outer submodule is added, the recursive update
// reports git's refusal, and the inner repository is never cloned.
func TestSubmoduleProtocolNestedFileURLRefused(t *testing.T) {
	fileTransportClosed(t)
	middle, _ := nestedSubmoduleFixture(t)
	dir := submoduleTarget(t)
	r := New(&LocalExecutor{Dir: dir})
	err := r.SubmoduleAddAt(middle, "vendor/", true)
	if err == nil {
		t.Fatal("a nested submodule at a local path was cloned")
	}
	t.Logf("recursive update error verbatim: %v", err)
	if !strings.Contains(err.Error(), "transport 'file' not allowed") {
		t.Fatalf("the error is not git's file transport refusal: %v", err)
	}
	base := filepath.Join(dir, "vendor", filepath.Base(middle))
	if _, err := os.Stat(filepath.Join(base, "middle.txt")); err != nil {
		t.Fatalf("the outer submodule was not added: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "deep", "inner.txt")); !os.IsNotExist(err) {
		t.Fatalf("the inner repository was cloned: %v", err)
	}
}

// The same nested layout updated with the override the refusal test
// lacks: git itself fetches the inner repository, proving the refusal
// test measures this gate and not a broken fixture.
func TestSubmoduleProtocolOverrideControlClonesNested(t *testing.T) {
	fileTransportClosed(t)
	middle, _ := nestedSubmoduleFixture(t)
	dir := submoduleTarget(t)
	r := New(&LocalExecutor{Dir: dir})
	if err := r.SubmoduleAddAt(middle, "vendor/", false); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join("vendor", filepath.Base(middle))
	cmd := exec.Command("git", "-C", dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "--", folder)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the override control failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, folder, "deep", "inner.txt")); err != nil {
		t.Fatalf("the inner repository was not cloned with the override: %v", err)
	}
}

// The user-typed URL keeps its override: adding a local path directly,
// with or without recursion, works as before.
func TestSubmoduleProtocolUserTypedURLStillClones(t *testing.T) {
	fileTransportClosed(t)
	middle, inner := nestedSubmoduleFixture(t)

	plain := submoduleTarget(t)
	r := New(&LocalExecutor{Dir: plain})
	if err := r.SubmoduleAddAt(middle, "x/", false); err != nil {
		t.Fatalf("a non-recursive add of the user's own URL failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plain, "x", filepath.Base(middle), "middle.txt")); err != nil {
		t.Fatalf("the outer submodule was not cloned: %v", err)
	}

	direct := submoduleTarget(t)
	r = New(&LocalExecutor{Dir: direct})
	if err := r.SubmoduleAdd(inner, "vendor/inner"); err != nil {
		t.Fatalf("adding the user's own local path failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(direct, "vendor", "inner", "inner.txt")); err != nil {
		t.Fatalf("the submodule was not cloned: %v", err)
	}
}
