package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestLiteralPathspecsStageAndUnstage(t *testing.T) {
	targets := []string{"stage*.txt", "stage?.txt", "stage[ab].txt", ":(top)"}
	dir := initRepo(t)
	for _, name := range append(append([]string{}, targets...), "stage-neighbor.txt") {
		writeFile(t, dir, name, "committed "+name+"\n")
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "literal stage")
	for _, name := range targets {
		writeFile(t, dir, name, "changed "+name+"\n")
	}
	writeFile(t, dir, "stage-neighbor.txt", "neighbor unchanged\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	must(t, r.Stage(targets...))
	got := strings.Fields(gitOut(t, dir, "diff", "--cached", "--name-only"))
	want := append([]string(nil), targets...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staged paths = %q, want %q", got, want)
	}
	must(t, r.Unstage(targets...))
	if got := strings.TrimSpace(gitOut(t, dir, "diff", "--cached", "--name-only")); got != "" {
		t.Fatalf("index after unstage = %q", got)
	}
	for _, name := range targets {
		body, err := os.ReadFile(filepath.Join(dir, name))
		must(t, err)
		if string(body) != "changed "+name+"\n" {
			t.Errorf("%s = %q", name, body)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "stage-neighbor.txt"))
	must(t, err)
	if string(body) != "neighbor unchanged\n" {
		t.Fatalf("neighbor = %q", body)
	}
	writeFile(t, dir, "stash-new*.txt", "stash bytes\n")
	saved, err := r.StashSave("--")
	must(t, err)
	if !saved {
		t.Fatal("stash with -- message did not save")
	}
	if _, err := os.Stat(filepath.Join(dir, "stash-new*.txt")); !os.IsNotExist(err) {
		t.Fatalf("stash-new*.txt remained after stash: %v", err)
	}
	must(t, r.StashApply("stash@{0}", false))
	must(t, r.StashDrop("stash@{0}"))

	server := startSSHD(t)
	sshDir := initRepo(t)
	writeFile(t, sshDir, "ssh*.txt", "ssh committed\n")
	writeFile(t, sshDir, "ssh-neighbor.txt", "ssh neighbor\n")
	gitRun(t, sshDir, "add", "-A")
	gitRun(t, sshDir, "commit", "-q", "-m", "ssh literal")
	writeFile(t, sshDir, "ssh*.txt", "ssh dirty\n")
	writeFile(t, sshDir, "ssh-neighbor.txt", "ssh neighbor dirty\n")
	sshRepo := New(server.executor(t, sshDir))
	must(t, sshRepo.Stage("ssh*.txt"))
	must(t, sshRepo.Unstage("ssh*.txt"))
	body, err = os.ReadFile(filepath.Join(sshDir, "ssh-neighbor.txt"))
	must(t, err)
	if string(body) != "ssh neighbor dirty\n" {
		t.Fatalf("SSH neighbor changed: %q", body)
	}
}

func TestLiteralPathspecsResetFiles(t *testing.T) {
	dir := initRepo(t)
	targets := []string{"reset*.txt", "reset?.txt", "reset[ab].txt", ":(top)"}
	for _, name := range append(append([]string{}, targets...), "reset-neighbor.txt") {
		writeFile(t, dir, name, "first "+name+"\n")
	}
	if err := os.Chmod(filepath.Join(dir, targets[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "reset first")
	root := gitOut(t, dir, "rev-parse", "HEAD")
	for _, name := range targets {
		writeFile(t, dir, name, "second "+name+"\n")
	}
	writeFile(t, dir, "reset-new*.txt", "new in second\n")
	writeFile(t, dir, "reset-neighbor.txt", "neighbor dirty\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "reset second")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	must(t, r.ResetFiles(root, append(targets, "reset-new*.txt")...))
	for _, name := range targets {
		body, err := os.ReadFile(filepath.Join(dir, name))
		must(t, err)
		if string(body) != "first "+name+"\n" {
			t.Errorf("%s = %q", name, body)
		}
	}
	if mode := fileMode(t, filepath.Join(dir, targets[0])); mode != 0o755 {
		t.Errorf("reset*.txt mode = %o, want 755", mode)
	}
	body, err := os.ReadFile(filepath.Join(dir, "reset-neighbor.txt"))
	must(t, err)
	if string(body) != "neighbor dirty\n" {
		t.Fatalf("neighbor overwritten: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "reset-new*.txt")); !os.IsNotExist(err) {
		t.Fatalf("reset-new*.txt still exists: %v", err)
	}
	if err := r.ResetFiles("not-a-revision", targets[0]); err == nil {
		t.Fatal("invalid revision succeeded")
	}
	body, err = os.ReadFile(filepath.Join(dir, "reset-neighbor.txt"))
	must(t, err)
	if string(body) != "neighbor dirty\n" {
		t.Fatalf("neighbor changed after failed reset: %q", body)
	}
}

func TestLiteralPathspecsConflictSide(t *testing.T) {
	dir := initRepo(t)
	target := "conflict[ab].txt"
	neighbor := "conflicta.txt"
	writeFile(t, dir, target, "base target\n")
	writeFile(t, dir, neighbor, "base neighbor\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "conflict base")
	gitRun(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, dir, target, "theirs target\n")
	gitRun(t, dir, "commit", "-q", "-am", "theirs")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, target, "ours target\n")
	gitRun(t, dir, "commit", "-q", "-am", "ours")
	cmd := exec.Command("git", "-C", dir, "merge", "side")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("merge output = %s, err = %v", out, err)
	}
	writeFile(t, dir, neighbor, "neighbor dirty\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	must(t, r.TakeConflictSide(target, "theirs"))
	body, err := os.ReadFile(filepath.Join(dir, target))
	must(t, err)
	if string(body) != "theirs target\n" {
		t.Fatalf("target = %q", body)
	}
	body, err = os.ReadFile(filepath.Join(dir, neighbor))
	must(t, err)
	if string(body) != "neighbor dirty\n" {
		t.Fatalf("neighbor changed: %q", body)
	}
	if err := r.TakeConflictSide("missing*(?)", "theirs"); err == nil {
		t.Fatal("missing conflict side succeeded")
	}
	body, err = os.ReadFile(filepath.Join(dir, neighbor))
	must(t, err)
	if string(body) != "neighbor dirty\n" {
		t.Fatalf("neighbor changed after failed conflict read: %q", body)
	}
}

func TestLiteralPathspecsDiscard(t *testing.T) {
	dir := initRepo(t)
	targets := []string{"discard*.txt", "discard?.txt", "discard[ab].txt", ":(top)"}
	for _, name := range targets {
		writeFile(t, dir, name, "committed "+name+"\n")
	}
	writeFile(t, dir, "discard-neighbor.txt", "neighbor committed\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "discard")
	writeFile(t, dir, ".gitignore", "ignored*.log\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "ignore")
	for _, name := range targets {
		writeFile(t, dir, name, "dirty "+name+"\n")
	}
	writeFile(t, dir, "discard-neighbor.txt", "neighbor dirty\n")
	writeFile(t, dir, "untracked?.txt", "drop untracked\n")
	writeFile(t, dir, "untracked-neighbor.txt", "keep untracked\n")
	writeFile(t, dir, "ignored*.log", "drop ignored\n")
	writeFile(t, dir, "ignored-neighbor.log", "keep ignored\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	selected := make([]FileChange, 0, len(targets)+2)
	for _, name := range targets {
		selected = append(selected, FileChange{Path: name, Status: 'M'})
	}
	selected = append(selected,
		FileChange{Path: "untracked?.txt", Status: '?'},
		FileChange{Path: "ignored*.log", Status: '!'})
	neighborIndex := gitOut(t, dir, "ls-files", "--stage", "--", "discard-neighbor.txt")
	neighborMode := fileMode(t, filepath.Join(dir, "discard-neighbor.txt"))
	must(t, r.Discard(selected...))
	for _, name := range targets {
		body, err := os.ReadFile(filepath.Join(dir, name))
		must(t, err)
		if string(body) != "committed "+name+"\n" {
			t.Errorf("%s = %q", name, body)
		}
	}
	for _, name := range []string{"untracked?.txt", "ignored*.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", name, err)
		}
	}
	for _, tc := range []struct{ name, want string }{{"discard-neighbor.txt", "neighbor dirty\n"}, {"untracked-neighbor.txt", "keep untracked\n"}, {"ignored-neighbor.log", "keep ignored\n"}} {
		body, err := os.ReadFile(filepath.Join(dir, tc.name))
		must(t, err)
		if string(body) != tc.want {
			t.Errorf("%s = %q", tc.name, body)
		}
	}
	if err := r.Discard(FileChange{Path: "missing*.txt", Status: 'M'}); err == nil {
		t.Fatal("missing discard succeeded")
	}
	body, err := os.ReadFile(filepath.Join(dir, "discard-neighbor.txt"))
	must(t, err)
	if string(body) != "neighbor dirty\n" {
		t.Fatalf("neighbor changed after failed discard: %q", body)
	}
	if got := gitOut(t, dir, "ls-files", "--stage", "--", "discard-neighbor.txt"); got != neighborIndex {
		t.Fatalf("neighbor index changed: got %q want %q", got, neighborIndex)
	}
	if got := fileMode(t, filepath.Join(dir, "discard-neighbor.txt")); got != neighborMode {
		t.Fatalf("neighbor mode changed: got %o want %o", got, neighborMode)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	must(t, err)
	return info.Mode().Perm()
}
