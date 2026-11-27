package exttool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeFakeTool stores an executable shell script in dir and returns its
// path. Tests only ever run these scripts, never a real graphical tool.
func writeFakeTool(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtToolNames(t *testing.T) {
	// The Settings pop-ups list the five presets in the table's order,
	// followed by Custom.
	want := []string{"FileMerge", "Kaleidoscope", "Beyond Compare", "P4Merge", "Visual Studio Code", "Custom"}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names()[%d] = %q, want %q (full list %v)", i, got[i], want[i], got)
		}
	}
	if Custom != "Custom" {
		t.Fatalf("Custom = %q, want %q", Custom, "Custom")
	}
}

func TestExtToolResolveCommands(t *testing.T) {
	// Each preset's diff and merge command is the table's shape, verbatim,
	// with the resolved program path quoted because it contains a space.
	program := filepath.Join(t.TempDir(), "spaced tool", "toolbin")
	q := "'" + program + "'"
	tests := []struct {
		name  string
		diff  string
		merge string
	}{
		{"FileMerge",
			q + ` "$LOCAL" "$REMOTE" | cat`,
			q + ` "$LOCAL" "$REMOTE" -ancestor "$BASE" -merge "$MERGED" | cat`},
		{"Kaleidoscope",
			q + ` --partial-changeset --relative-path "$MERGED" -- "$LOCAL" "$REMOTE"`,
			q + ` --merge --output "$MERGED" --base "$BASE" -- "$LOCAL" --snapshot "$REMOTE" --snapshot`},
		{"Beyond Compare",
			q + ` "$LOCAL" "$REMOTE"`,
			q + ` "$LOCAL" "$REMOTE" "$BASE" -mergeoutput="$MERGED"`},
		{"P4Merge",
			q + ` "$LOCAL" "$REMOTE"`,
			q + ` "$BASE" "$REMOTE" "$LOCAL" "$MERGED"`},
		{"Visual Studio Code",
			q + ` --wait --diff "$LOCAL" "$REMOTE"`,
			q + ` --wait --merge "$REMOTE" "$LOCAL" "$BASE" "$MERGED"`},
	}
	find := func(candidates []string, prog string) string { return program }
	for _, tt := range tests {
		t.Run(tt.name+"/diff", func(t *testing.T) {
			tool, err := ResolveDiff(tt.name, "", "", find)
			if err != nil {
				t.Fatal(err)
			}
			if tool.Name != tt.name {
				t.Errorf("Name = %q, want %q", tool.Name, tt.name)
			}
			if tool.Command != tt.diff {
				t.Errorf("Command =\n  %s\nwant\n  %s", tool.Command, tt.diff)
			}
		})
		t.Run(tt.name+"/merge", func(t *testing.T) {
			tool, err := ResolveMerge(tt.name, "", "", find)
			if err != nil {
				t.Fatal(err)
			}
			if tool.Command != tt.merge {
				t.Errorf("Command =\n  %s\nwant\n  %s", tool.Command, tt.merge)
			}
		})
	}
}

func TestExtToolResolveNotFound(t *testing.T) {
	// A missing program is an error naming the tool and every location
	// that was checked.
	find := func(candidates []string, prog string) string { return "" }
	_, err := ResolveDiff("FileMerge", "", "", find)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"FileMerge", "/usr/bin/opendiff", "opendiff"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not mention %q", err, part)
		}
	}
	_, err = ResolveMerge("Beyond Compare", "", "", find)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"Beyond Compare", "/usr/local/bin/bcomp", "/opt/homebrew/bin/bcomp", "/Applications/Beyond Compare.app/Contents/MacOS/bcomp"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not mention %q", err, part)
		}
	}
}

func TestExtToolResolveUnknown(t *testing.T) {
	// A name outside the table is an error, and the finder is never asked.
	find := func(candidates []string, prog string) string {
		t.Error("finder called for an unknown tool")
		return ""
	}
	for _, name := range []string{"", "filemerge", "KDiff3"} {
		if _, err := ResolveDiff(name, "", "", find); err == nil {
			t.Errorf("ResolveDiff(%q): expected an error", name)
		} else if !strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("ResolveDiff(%q): error %q does not say unknown tool", name, err)
		}
	}
}

func TestExtToolCustom(t *testing.T) {
	// The Custom tool runs the user's own program: a path with a space is
	// quoted, empty arguments fall back to the table's defaults, and given
	// arguments pass through untouched.
	dir := filepath.Join(t.TempDir(), "spaced dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	program := writeFakeTool(t, dir, "my tool.sh", "#!/bin/sh\nexit 0\n")
	q := "'" + program + "'"

	tool, err := ResolveDiff(Custom, program, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := q + ` "$LOCAL" "$REMOTE"`; tool.Command != want {
		t.Errorf("diff Command =\n  %s\nwant\n  %s", tool.Command, want)
	}
	tool, err = ResolveMerge(Custom, program, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := q + ` "$BASE" "$LOCAL" "$REMOTE" "$MERGED"`; tool.Command != want {
		t.Errorf("merge Command =\n  %s\nwant\n  %s", tool.Command, want)
	}
	args := `--mode "$LOCAL" "$REMOTE" --fast`
	tool, err = ResolveDiff(Custom, program, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := q + " " + args; tool.Command != want {
		t.Errorf("custom-args Command =\n  %s\nwant\n  %s", tool.Command, want)
	}
}

func TestExtToolCustomBadProgram(t *testing.T) {
	// A custom program that is relative, missing, or not an executable
	// regular file is an error instead of a shell surprise later.
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain-file")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		what string
		path string
	}{
		{"relative", filepath.Join("some", "relative", "tool")},
		{"missing", filepath.Join(dir, "no-such-tool")},
		{"not executable", plain},
		{"directory", dir},
	}
	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			if _, err := ResolveDiff(Custom, tt.path, "", nil); err == nil {
				t.Error("expected an error")
			}
			if _, err := ResolveMerge(Custom, tt.path, "", nil); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestExtToolFindProgram(t *testing.T) {
	// The first candidate that is an executable regular file wins; missing
	// files, directories and non-executable files are skipped, and with no
	// working candidate the program is looked up in PATH.
	dir := t.TempDir()
	good := writeFakeTool(t, dir, "good", "#!/bin/sh\nexit 0\n")
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")

	if got := FindProgram([]string{missing, good, plain}, "no-such-program-anywhere"); got != good {
		t.Errorf("FindProgram with missing first candidate = %q, want %q", got, good)
	}
	if got := FindProgram([]string{dir, plain, good}, "no-such-program-anywhere"); got != good {
		t.Errorf("FindProgram skipping directory and non-executable = %q, want %q", got, good)
	}
	if got := FindProgram([]string{missing, plain, dir}, "sh"); got == "" {
		t.Error("FindProgram falling back to PATH lookup of sh returned empty")
	}
	if got := FindProgram([]string{missing}, "branchline-exttool-no-such-program"); got != "" {
		t.Errorf("FindProgram of absent program = %q, want empty", got)
	}
}

func TestExtToolSnapshotWrite(t *testing.T) {
	// Snapshot folders are private (0700), read-only versions come out
	// 0400 and the writable merged version 0600, with the content intact.
	parent := t.TempDir()
	snap, err := NewSnapshot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Remove() }()
	info, err := os.Stat(snap.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(snap.Dir()), "branchline-exttool-") {
		t.Errorf("snapshot dir %q lacks the branchline-exttool- prefix", snap.Dir())
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("snapshot dir perm = %o, want 700", perm)
	}

	local, err := snap.Write("local", "a/b/c.txt", []byte("old\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(local) != "c.txt" {
		t.Errorf("local file name = %q, want c.txt", filepath.Base(local))
	}
	if filepath.Dir(local) != filepath.Join(snap.Dir(), "local") {
		t.Errorf("local file dir = %q, want inside %q", filepath.Dir(local), filepath.Join(snap.Dir(), "local"))
	}
	checkFile := func(path, content string, perm os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != perm {
			t.Errorf("%s perm = %o, want %o", path, info.Mode().Perm(), perm)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != content {
			t.Errorf("%s content = %q, want %q", path, data, content)
		}
	}
	checkFile(local, "old\n", 0o400)
	merged, err := snap.Write("merged", "a/b/c.txt", []byte("new\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	checkFile(merged, "new\n", 0o600)

	for _, role := range []string{"remote", "base"} {
		if _, err := snap.Write(role, "f.txt", nil, false); err != nil {
			t.Errorf("role %s: %v", role, err)
		}
	}
	if _, err := snap.Write("bogus", "f.txt", nil, false); err == nil {
		t.Error("unknown role: expected an error")
	}
	if !filepath.IsAbs(local) {
		t.Errorf("Write returned non-absolute path %q", local)
	}
}

func TestExtToolSnapshotFileNames(t *testing.T) {
	// Repo paths that name nothing useful fall back to "file", and path
	// segments never escape the role folder.
	snap, err := NewSnapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Remove() }()
	for i, repoPath := range []string{"", "..", ".", "a/../.."} {
		role := []string{"local", "remote", "base", "merged"}[i]
		file, err := snap.Write(role, repoPath, []byte("x"), false)
		if err != nil {
			t.Fatalf("repoPath %q: %v", repoPath, err)
		}
		if filepath.Base(file) != "file" {
			t.Errorf("repoPath %q: file name = %q, want file", repoPath, filepath.Base(file))
		}
	}
	evil := `-rf $(touch pwned) "a b".txt`
	file, err := snap.Write("local", evil, []byte("x"), false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != evil {
		t.Errorf("evil repoPath: file name = %q, want verbatim %q", filepath.Base(file), evil)
	}
	if filepath.Dir(file) != filepath.Join(snap.Dir(), "local") {
		t.Errorf("evil repoPath escaped the role folder: %q", file)
	}
}

func TestExtToolSnapshotRemove(t *testing.T) {
	// Remove deletes everything, including the read-only files.
	snap, err := NewSnapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"local", "remote", "base", "merged"} {
		if _, err := snap.Write(role, "f.txt", []byte("x"), role == "merged"); err != nil {
			t.Fatal(err)
		}
	}
	dir := snap.Dir()
	if err := snap.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("after Remove, stat err = %v, want not-exist", err)
	}
}

func TestExtToolCleanStale(t *testing.T) {
	// Startup cleanup deletes only own old snapshot folders: fresh ones,
	// foreign files and foreign folders are untouched.
	parent := t.TempDir()
	mk := func(name string, age time.Duration) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	old := mk("branchline-exttool-old", time.Hour)
	fresh := mk("branchline-exttool-fresh", time.Minute)
	foreignDir := mk("other-folder", 24*time.Hour)
	foreignFile := filepath.Join(parent, "branchline-exttool-notdir")
	if err := os.WriteFile(foreignFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := CleanStale(parent, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CleanStale removed %d folders, want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale snapshot folder survived")
	}
	for _, keep := range []string{fresh, foreignDir, foreignFile} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed or damaged: %v", keep, err)
		}
	}
	if n, err := CleanStale(filepath.Join(parent, "no-such-parent"), time.Minute); n != 0 || err != nil {
		t.Errorf("missing parent = %d, %v; want 0, nil", n, err)
	}
}

// recordScript is a fake tool that hex-encodes its arguments and the four
// snapshot variables into one record file, so tests compare them byte for
// byte. %q shell-quoting is safe here: the record path comes from the test,
// never from the repository.
func recordScript(record string) string {
	return `#!/bin/sh
r=` + fmt.Sprintf("%q", record) + `
hex() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n'; }
{
i=1
for a in "$@"; do echo "arg$i=$(hex "$a")"; i=$((i+1)); done
echo "argc=$#"
echo "local=$(hex "$LOCAL")"
echo "remote=$(hex "$REMOTE")"
echo "base=$(hex "$BASE")"
echo "merged=$(hex "$MERGED")"
} > "$r"
`
}

// readRecord parses the fake tool's hex record back into bytes.
func readRecord(t *testing.T, record string) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("record line %q has no key", line)
		}
		if key == "argc" {
			continue
		}
		raw, err := hex.DecodeString(value)
		if err != nil {
			t.Fatalf("record line %q: %v", line, err)
		}
		out[key] = raw
	}
	return out
}

func TestExtToolRunDiffInjectionSafety(t *testing.T) {
	// A repository file named like a shell command line is data, not code:
	// the tool receives the snapshot path verbatim as one argument and
	// nothing named pwned appears.
	dir := t.TempDir()
	t.Chdir(dir)
	record := filepath.Join(dir, "record.txt")
	toolPath := writeFakeTool(t, dir, "record tool", recordScript(record))

	snap, err := NewSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Remove() }()
	evil := `-rf $(touch pwned) "a b".txt`
	local, err := snap.Write("local", evil, []byte("old\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := snap.Write("remote", "plain.txt", []byte("new\n"), false)
	if err != nil {
		t.Fatal(err)
	}

	tool := Tool{Name: "fake", Command: fmt.Sprintf("%q \"$LOCAL\" \"$REMOTE\"", toolPath)}
	if err := RunDiff(context.Background(), tool, Vars{Local: local, Remote: remote}); err != nil {
		t.Fatal(err)
	}
	got := readRecord(t, record)
	if string(got["arg1"]) != local {
		t.Errorf("arg1 = %q, want verbatim %q", got["arg1"], local)
	}
	if string(got["arg2"]) != remote {
		t.Errorf("arg2 = %q, want verbatim %q", got["arg2"], remote)
	}
	if string(got["local"]) != local || string(got["remote"]) != remote {
		t.Errorf("env = %q / %q, want %q / %q", got["local"], got["remote"], local, remote)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
		t.Errorf("pwned exists in %s: the file name was interpreted as shell code", dir)
	}
}

func TestExtToolRunMergeVars(t *testing.T) {
	// All four snapshot paths reach the merge tool through the environment.
	dir := t.TempDir()
	record := filepath.Join(dir, "record.txt")
	toolPath := writeFakeTool(t, dir, "record tool", recordScript(record))
	vars := Vars{
		Local:  filepath.Join(dir, "l"),
		Remote: filepath.Join(dir, "r"),
		Base:   filepath.Join(dir, "b"),
		Merged: filepath.Join(dir, "m"),
	}
	tool := Tool{Name: "fake", Command: fmt.Sprintf("%q \"$BASE\" \"$LOCAL\" \"$REMOTE\" \"$MERGED\"", toolPath)}
	if err := RunMerge(context.Background(), tool, vars); err != nil {
		t.Fatal(err)
	}
	got := readRecord(t, record)
	for key, want := range map[string]string{
		"arg1": vars.Base, "arg2": vars.Local, "arg3": vars.Remote, "arg4": vars.Merged,
		"local": vars.Local, "remote": vars.Remote, "base": vars.Base, "merged": vars.Merged,
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

func TestExtToolRunExitCodes(t *testing.T) {
	// Diff accepts 0 and 1 and rejects anything else with the stderr tail;
	// merge accepts only 0 and reports failures as *ExitError.
	dir := t.TempDir()
	exitTool := func(code int, stderrText string) Tool {
		script := "#!/bin/sh\nprintf '%s' '" + stderrText + "' >&2\nexit " + strconv.Itoa(code) + "\n"
		path := writeFakeTool(t, dir, fmt.Sprintf("exit%d", code)+stderrText, script)
		return Tool{Name: "fake", Command: fmt.Sprintf("%q", path)}
	}
	vars := Vars{Local: "l", Remote: "r", Base: "b", Merged: "m"}
	ctx := context.Background()

	if err := RunDiff(ctx, exitTool(0, ""), vars); err != nil {
		t.Errorf("diff exit 0: %v", err)
	}
	if err := RunDiff(ctx, exitTool(1, ""), vars); err != nil {
		t.Errorf("diff exit 1: %v", err)
	}
	for _, code := range []int{2, 126, 127} {
		err := RunDiff(ctx, exitTool(code, "diff failed"), vars)
		if err == nil {
			t.Errorf("diff exit %d: expected an error", code)
		} else if !strings.Contains(err.Error(), "diff failed") {
			t.Errorf("diff exit %d: error %q lacks the stderr text", code, err)
		}
	}

	if err := RunMerge(ctx, exitTool(0, ""), vars); err != nil {
		t.Errorf("merge exit 0: %v", err)
	}
	err := RunMerge(ctx, exitTool(1, "merge failed"), vars)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("merge exit 1: error %v is not an *ExitError", err)
	}
	if exitErr.ExitCode != 1 {
		t.Errorf("merge exit 1: ExitCode = %d, want 1", exitErr.ExitCode)
	}
	if !strings.Contains(exitErr.Stderr, "merge failed") {
		t.Errorf("merge exit 1: Stderr = %q, want the stderr text", exitErr.Stderr)
	}
	err = RunMerge(ctx, exitTool(127, "no such"), vars)
	if !errors.As(err, &exitErr) || exitErr.ExitCode != 127 {
		t.Errorf("merge exit 127: %v, want *ExitError with code 127", err)
	}
}

func TestExtToolRunOutputLimit(t *testing.T) {
	// Only the last 64 KiB of stderr is kept, however much the tool prints.
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf 'start%070d\\n' 0 >&2\n" +
		"yes abcdefgh | head -c 70000 >&2\n" +
		"printf 'the-end\\n' >&2\n" +
		"exit 2\n"
	path := writeFakeTool(t, dir, "loud", script)
	err := RunDiff(context.Background(), Tool{Name: "fake", Command: fmt.Sprintf("%q", path)}, Vars{})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v is not an *ExitError", err)
	}
	if len(exitErr.Stderr) != outputLimit {
		t.Errorf("stderr length = %d, want %d", len(exitErr.Stderr), outputLimit)
	}
	if !strings.HasSuffix(exitErr.Stderr, "the-end\n") {
		t.Errorf("stderr does not end with the final output: ...%q", exitErr.Stderr[len(exitErr.Stderr)-40:])
	}
	if strings.Contains(exitErr.Stderr, "start") {
		t.Error("stderr still contains the beginning, which should have been dropped")
	}
}

func TestExtToolRunContextCancel(t *testing.T) {
	// Cancelling the context ends a hanging tool quickly, process group and
	// all: the recorded PID is gone afterwards.
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho $$ > " + fmt.Sprintf("%q", pidfile) + "\nexec sleep 600\n"
	toolPath := writeFakeTool(t, dir, "sleeper", script)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunDiff(ctx, Tool{Name: "fake", Command: fmt.Sprintf("%q", toolPath)}, Vars{})
	}()
	deadline := time.Now().Add(10 * time.Second)
	var pid int
	for {
		data, err := os.ReadFile(pidfile)
		if err == nil && len(strings.TrimSpace(string(data))) > 0 {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tool never wrote its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled RunDiff returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled RunDiff did not return within 10 seconds")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tool process %d still exists after cancellation", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
