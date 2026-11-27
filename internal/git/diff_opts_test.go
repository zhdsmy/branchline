package git

import (
	"strings"
	"testing"
)

// diffOptsContent builds file content for whitespace tests: filler lines keep
// the whitespace-only line and the real change in separate hunks.
func diffOptsContent(wsLine, realLine string) string {
	var b strings.Builder
	b.WriteString("header\n")
	for i := 1; i <= 8; i++ {
		b.WriteString("filler a\n")
	}
	b.WriteString(wsLine + "\n")
	for i := 1; i <= 8; i++ {
		b.WriteString("filler b\n")
	}
	b.WriteString(realLine + "\n")
	return b.String()
}

func writeDiffOptsFixture(t *testing.T, dir, file, wsLine, realLine string) {
	t.Helper()
	writeFile(t, dir, file, diffOptsContent(wsLine, realLine))
}

// CommitFileDiffOpts with IgnoreSpace drops the whitespace-only hunk and
// keeps the real change; with zero options its output matches CommitFileDiff
// byte for byte.
func TestDiffOptsCommitFileDiffIgnoreSpace(t *testing.T) {
	dir := initRepo(t)
	writeDiffOptsFixture(t, dir, "code.go", "var x = 1", "var y = 1")
	gitRun(t, dir, "add", "code.go")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	base := gitOut(t, dir, "rev-parse", "HEAD")

	writeDiffOptsFixture(t, dir, "code.go", "var x  =  1", "var y = 2")
	gitRun(t, dir, "add", "code.go")
	gitRun(t, dir, "commit", "-q", "-m", "change")
	head := gitOut(t, dir, "rev-parse", "HEAD")

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
	c := Commit{Hash: head, Parents: []string{base}}
	f := FileChange{Path: "code.go", Status: 'M'}

	plain, err := r.CommitFileDiff(c, f)
	if err != nil {
		t.Fatalf("CommitFileDiff: %v", err)
	}
	zero, err := r.CommitFileDiffOpts(c, f, DiffOptions{})
	if err != nil {
		t.Fatalf("CommitFileDiffOpts zero: %v", err)
	}
	if zero != plain {
		t.Fatalf("zero options output differs from CommitFileDiff:\nzero:\n%s\nplain:\n%s", zero, plain)
	}

	ws, err := r.CommitFileDiffOpts(c, f, DiffOptions{IgnoreSpace: true})
	if err != nil {
		t.Fatalf("CommitFileDiffOpts ignore-space: %v", err)
	}
	for _, line := range []string{"+var x  =  1", "-var x = 1"} {
		if strings.Contains(ws, line) {
			t.Fatalf("ignore-space diff still contains whitespace-only line %q:\n%s", line, ws)
		}
	}
	for _, line := range []string{"+var y = 2", "-var y = 1"} {
		if !strings.Contains(ws, line) {
			t.Fatalf("ignore-space diff lost real change line %q:\n%s", line, ws)
		}
	}

	// A commit whose only change is whitespace produces no diff at all with
	// IgnoreSpace, so a file like it shows as unchanged.
	writeDiffOptsFixture(t, dir, "code.go", "var x   =   1", "var y = 2")
	gitRun(t, dir, "add", "code.go")
	gitRun(t, dir, "commit", "-q", "-m", "whitespace only")
	wsHead := gitOut(t, dir, "rev-parse", "HEAD")
	onlyWS, err := r.CommitFileDiffOpts(Commit{Hash: wsHead, Parents: []string{head}}, f, DiffOptions{IgnoreSpace: true})
	if err != nil {
		t.Fatalf("CommitFileDiffOpts whitespace-only commit: %v", err)
	}
	if strings.TrimSpace(onlyWS) != "" {
		t.Fatalf("whitespace-only commit diff with ignore-space = %q, want empty", onlyWS)
	}
}

// StagedDiffOpts with IgnoreSpace drops whitespace-only staged changes and
// keeps real ones; with zero options its output matches StagedDiff byte for
// byte.
func TestDiffOptsStagedDiffIgnoreSpace(t *testing.T) {
	dir := initRepo(t)
	writeDiffOptsFixture(t, dir, "code.go", "var x = 1", "var y = 1")
	gitRun(t, dir, "add", "code.go")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	writeDiffOptsFixture(t, dir, "code.go", "var x  =  1", "var y = 2")
	gitRun(t, dir, "add", "code.go")

	r := New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})

	plain, err := r.StagedDiff("code.go")
	if err != nil {
		t.Fatalf("StagedDiff: %v", err)
	}
	zero, err := r.StagedDiffOpts([]string{"code.go"}, DiffOptions{})
	if err != nil {
		t.Fatalf("StagedDiffOpts zero: %v", err)
	}
	if zero != plain {
		t.Fatalf("zero options output differs from StagedDiff:\nzero:\n%s\nplain:\n%s", zero, plain)
	}

	ws, err := r.StagedDiffOpts([]string{"code.go"}, DiffOptions{IgnoreSpace: true})
	if err != nil {
		t.Fatalf("StagedDiffOpts ignore-space: %v", err)
	}
	for _, line := range []string{"+var x  =  1", "-var x = 1"} {
		if strings.Contains(ws, line) {
			t.Fatalf("ignore-space diff still contains whitespace-only line %q:\n%s", line, ws)
		}
	}
	for _, line := range []string{"+var y = 2", "-var y = 1"} {
		if !strings.Contains(ws, line) {
			t.Fatalf("ignore-space diff lost real change line %q:\n%s", line, ws)
		}
	}

	// Committing the earlier staging leaves only a whitespace edit staged:
	// the diff against HEAD vanishes with IgnoreSpace while plain output
	// still shows it.
	gitRun(t, dir, "commit", "-q", "-m", "change")
	writeDiffOptsFixture(t, dir, "code.go", "var x   =   1", "var y = 2")
	gitRun(t, dir, "add", "code.go")
	onlyWS, err := r.StagedDiffOpts([]string{"code.go"}, DiffOptions{IgnoreSpace: true})
	if err != nil {
		t.Fatalf("StagedDiffOpts whitespace-only staging: %v", err)
	}
	if strings.TrimSpace(onlyWS) != "" {
		t.Fatalf("whitespace-only staged diff with ignore-space = %q, want empty", onlyWS)
	}
	plainWS, err := r.StagedDiff("code.go")
	if err != nil {
		t.Fatalf("StagedDiff whitespace-only staging: %v", err)
	}
	if !strings.Contains(plainWS, "+var x   =   1") {
		t.Fatalf("plain staged diff missing whitespace-only change:\n%s", plainWS)
	}
}
