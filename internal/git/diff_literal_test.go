//go:build !windows

package git

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSingleFileDiffLiteralPaths(t *testing.T) {
	testSingleFileDiffLiteralPaths(t, nil)
}

func TestSingleFileDiffLiteralPathsRealSSH(t *testing.T) {
	testSingleFileDiffLiteralPaths(t, startSSHD(t))
}

func testSingleFileDiffLiteralPaths(t *testing.T, server *sshServer) {
	t.Helper()
	dir := initRepo(t)
	names := []string{
		"wild*.txt", "wild-other.txt", "mark?.txt", "markA.txt",
		"bracket[1].txt", "bracket1.txt", ":(glob)magic*.txt", "magic-other.txt",
		"-leading.txt", "空 格\ttab\nline ' $().txt", "old*.txt", "old-other.txt", "newA.txt",
	}
	fileIndex := make(map[string]int)
	for i, name := range names {
		fileIndex[name] = i
		var body strings.Builder
		for line := 0; line < 20; line++ {
			fmt.Fprintf(&body, "file %d original line %d\n", i, line)
		}
		writeFile(t, dir, name, body.String())
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "literal root")
	root := gitOut(t, dir, "rev-parse", "HEAD")
	for i, name := range names {
		if name == "old*.txt" {
			continue
		}
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		must(t, err)
		must(t, os.WriteFile(p, append(data, []byte(fmt.Sprintf("tip file %d\n", i))...), 0o644))
	}
	must(t, os.Rename(filepath.Join(dir, "old*.txt"), filepath.Join(dir, "new?.txt")))
	fileIndex["new?.txt"] = fileIndex["old*.txt"]
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "literal tip with rename")
	tip := gitOut(t, dir, "rev-parse", "HEAD")
	// Leave both index and worktree changes: reading historical patches must
	// not substitute either current version or modify these user changes.
	writeFile(t, dir, "wild*.txt", "staged current content\n")
	gitRun(t, dir, "--literal-pathspecs", "add", "--", "wild*.txt")
	for i, name := range names {
		if name != "old*.txt" {
			writeFile(t, dir, name, fmt.Sprintf("worktree file %d\n", i))
		}
	}
	writeFile(t, dir, "new?.txt", "worktree renamed content\n")
	writeFile(t, dir, ":(glob)untracked*.txt", "untracked content\n")
	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	if server != nil {
		r = New(server.executor(t, dir))
	}
	before := literalDiffSnapshot(t, dir)
	for _, bounds := range []struct{ name, from, to string }{
		{"root", "", root}, {"range", root, tip},
	} {
		t.Run(bounds.name, func(t *testing.T) {
			files, err := r.RangeFiles(bounds.from, bounds.to)
			must(t, err)
			if len(files) != len(names) {
				t.Fatalf("files = %d, want %d", len(files), len(names))
			}
			for _, file := range files {
				t.Run(file.Path, func(t *testing.T) {
					for _, opts := range []DiffOptions{{}, {IgnoreSpace: true, Context: -1}} {
						patch, err := r.RangeFileDiffOpts(bounds.from, bounds.to, file, opts)
						must(t, err)
						if count := strings.Count(patch, "diff --git "); count != 1 {
							t.Errorf("single selected file returned %d patches for %+v: %q", count, file, patch)
						}
						marker := fmt.Sprintf("+file %d original line 0\n", fileIndex[file.Path])
						if bounds.from != "" {
							marker = fmt.Sprintf("+tip file %d\n", fileIndex[file.Path])
						}
						if file.Status != 'R' && !strings.Contains(patch, marker) {
							t.Errorf("selected content marker %q missing: %q", marker, patch)
						}
						if strings.Contains(patch, "worktree ") || strings.Contains(patch, "staged current") {
							t.Errorf("historical diff read current data: %q", patch)
						}
						if file.Status == 'R' && (file.OrigPath != "old*.txt" || !strings.Contains(patch, "rename from old*.txt\n") || !strings.Contains(patch, "rename to new?.txt\n")) {
							t.Errorf("rename lost literal old/new paths: %+v, %q", file, patch)
						}
					}
				})
			}
		})
	}
	t.Run("worktree", func(t *testing.T) {
		for _, name := range append(append([]string(nil), names[:10]...), "new?.txt", ":(glob)untracked*.txt") {
			t.Run(name, func(t *testing.T) {
				file := FileChange{Path: name, Status: 'M'}
				if strings.Contains(name, "untracked") {
					file.Status = '?'
				}
				patch, err := r.WorktreeFileDiffOpts(root, file, DiffOptions{Context: -1})
				must(t, err)
				if count := strings.Count(patch, "diff --git "); count != 1 {
					t.Errorf("worktree selected file returned %d patches: %q", count, patch)
				}
				if marker := "+" + before.files[name]; !strings.Contains(patch, marker) {
					t.Errorf("selected worktree data %q missing: %q", marker, patch)
				}
			})
		}
	})
	after := literalDiffSnapshot(t, dir)
	if before.head != after.head || before.status != after.status || !bytes.Equal(before.index, after.index) || !reflect.DeepEqual(before.files, after.files) {
		t.Fatal("single-file diff changed HEAD, index, status or worktree data")
	}
}

type singleFileDiffSnapshot struct {
	head, status string
	index        []byte
	files        map[string]string
}

func literalDiffSnapshot(t *testing.T, dir string) singleFileDiffSnapshot {
	t.Helper()
	s := singleFileDiffSnapshot{
		head: gitOut(t, dir, "rev-parse", "HEAD"), status: gitOutput(t, dir, "status", "--porcelain=v1", "-z"),
		files: make(map[string]string),
	}
	var err error
	s.index, err = os.ReadFile(filepath.Join(dir, ".git", "index"))
	must(t, err)
	entries, err := os.ReadDir(dir)
	must(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		must(t, err)
		s.files[entry.Name()] = string(data)
	}
	return s
}

func TestDiscardLiteralPathspecs(t *testing.T) {
	t.Run("TrackedWildcardPreservesNeighbor", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "wild*.txt", "wild committed\n")
		writeFile(t, dir, "wild-neighbor.txt", "neighbor committed\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-q", "-m", "root")

		writeFile(t, dir, "wild*.txt", "wild dirty\n")
		writeFile(t, dir, "wild-neighbor.txt", "neighbor dirty\n")

		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		must(t, r.Discard(FileChange{Path: "wild*.txt", Status: 'M'}))

		gotWild, err := os.ReadFile(filepath.Join(dir, "wild*.txt"))
		must(t, err)
		if string(gotWild) != "wild committed\n" {
			t.Fatalf("wild*.txt = %q, want %q", gotWild, "wild committed\n")
		}
		gotNeighbor, err := os.ReadFile(filepath.Join(dir, "wild-neighbor.txt"))
		must(t, err)
		if string(gotNeighbor) != "neighbor dirty\n" {
			t.Fatalf("wild-neighbor.txt = %q, want %q", gotNeighbor, "neighbor dirty\n")
		}
		st, err := r.Status()
		must(t, err)
		if len(st.Unstaged) != 1 || st.Unstaged[0].Path != "wild-neighbor.txt" || st.Unstaged[0].Status != 'M' {
			t.Fatalf("unstaged = %+v, want only wild-neighbor.txt M", st.Unstaged)
		}
	})

	t.Run("UntrackedAndIgnoredWildcardPreservesNeighbor", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, ".gitignore", "*.log\n")
		gitRun(t, dir, "add", ".gitignore")
		gitRun(t, dir, "commit", "-q", "-m", "ignore logs")

		writeFile(t, dir, "untracked*.txt", "drop untracked\n")
		writeFile(t, dir, "untracked-neighbor.txt", "keep untracked neighbor\n")
		writeFile(t, dir, "ignored*.log", "drop ignored\n")
		writeFile(t, dir, "ignored-neighbor.log", "keep ignored neighbor\n")

		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		must(t, r.Discard(
			FileChange{Path: "untracked*.txt", Status: '?'},
			FileChange{Path: "ignored*.log", Status: '!'},
		))

		if _, err := os.Stat(filepath.Join(dir, "untracked*.txt")); !os.IsNotExist(err) {
			t.Fatalf("untracked*.txt still exists: err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "ignored*.log")); !os.IsNotExist(err) {
			t.Fatalf("ignored*.log still exists: err=%v", err)
		}
		gotUntrackedNeighbor, err := os.ReadFile(filepath.Join(dir, "untracked-neighbor.txt"))
		must(t, err)
		if string(gotUntrackedNeighbor) != "keep untracked neighbor\n" {
			t.Fatalf("untracked-neighbor.txt = %q", gotUntrackedNeighbor)
		}
		gotIgnoredNeighbor, err := os.ReadFile(filepath.Join(dir, "ignored-neighbor.log"))
		must(t, err)
		if string(gotIgnoredNeighbor) != "keep ignored neighbor\n" {
			t.Fatalf("ignored-neighbor.log = %q", gotIgnoredNeighbor)
		}
	})

	t.Run("RealSSHLoopback", func(t *testing.T) {
		server := startSSHD(t)
		dir := initRepo(t)
		writeFile(t, dir, "wild*.txt", "ssh wild committed\n")
		writeFile(t, dir, "wild-neighbor.txt", "ssh neighbor committed\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-q", "-m", "ssh root")

		writeFile(t, dir, "wild*.txt", "ssh wild dirty\n")
		writeFile(t, dir, "wild-neighbor.txt", "ssh neighbor dirty\n")
		writeFile(t, dir, "clean*.txt", "ssh drop untracked\n")
		writeFile(t, dir, "clean-neighbor.txt", "ssh keep untracked\n")

		r := New(server.executor(t, dir))
		must(t, r.Discard(
			FileChange{Path: "wild*.txt", Status: 'M'},
			FileChange{Path: "clean*.txt", Status: '?'},
		))

		gotWild, err := os.ReadFile(filepath.Join(dir, "wild*.txt"))
		must(t, err)
		if string(gotWild) != "ssh wild committed\n" {
			t.Fatalf("wild*.txt = %q, want %q", gotWild, "ssh wild committed\n")
		}
		gotNeighbor, err := os.ReadFile(filepath.Join(dir, "wild-neighbor.txt"))
		must(t, err)
		if string(gotNeighbor) != "ssh neighbor dirty\n" {
			t.Fatalf("wild-neighbor.txt = %q, want %q", gotNeighbor, "ssh neighbor dirty\n")
		}
		if _, err := os.Stat(filepath.Join(dir, "clean*.txt")); !os.IsNotExist(err) {
			t.Fatalf("clean*.txt still exists: err=%v", err)
		}
		gotCleanNeighbor, err := os.ReadFile(filepath.Join(dir, "clean-neighbor.txt"))
		must(t, err)
		if string(gotCleanNeighbor) != "ssh keep untracked\n" {
			t.Fatalf("clean-neighbor.txt = %q, want %q", gotCleanNeighbor, "ssh keep untracked\n")
		}
	})

	t.Run("MissingTargetReturnsErrorAndPreservesNeighbor", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "missing-neighbor.txt", "neighbor committed\n")
		gitRun(t, dir, "add", "-A")
		gitRun(t, dir, "commit", "-q", "-m", "root")

		writeFile(t, dir, "missing-neighbor.txt", "neighbor dirty\n")

		r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
		if err := r.Discard(FileChange{Path: "missing*.txt", Status: 'M'}); err == nil {
			t.Fatal("Discard of non-existent literal path missing*.txt succeeded; want error")
		}
		gotNeighbor, err := os.ReadFile(filepath.Join(dir, "missing-neighbor.txt"))
		must(t, err)
		if string(gotNeighbor) != "neighbor dirty\n" {
			t.Fatalf("missing-neighbor.txt was overwritten: got %q, want %q", gotNeighbor, "neighbor dirty\n")
		}
	})
}
