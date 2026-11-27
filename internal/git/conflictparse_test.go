package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

// conflictGitRun runs git in dir and fails the test on error. Literal
// pathspecs mirror the application, so test files may use glob characters.
func conflictGitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "--literal-pathspecs", "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// conflictGitOut runs git in dir and returns standard output.
func conflictGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "--literal-pathspecs", "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// conflictMerge runs a merge that must conflict.
func conflictMerge(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "--literal-pathspecs", "-c", "core.quotepath=false", "merge", "--no-edit", branch)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("merge %s: expected conflicts, err=%v\n%s", branch, err, out)
	}
}

func conflictWriteFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func conflictInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	conflictGitRun(t, dir, "init", "-q", "-b", "main")
	conflictGitRun(t, dir, "config", "user.name", "Ada")
	conflictGitRun(t, dir, "config", "user.email", "ada@example.com")
	conflictGitRun(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func conflictCommitAll(t *testing.T, dir, message string) {
	t.Helper()
	conflictGitRun(t, dir, "add", "-A")
	conflictGitRun(t, dir, "commit", "-q", "-m", message)
}

// setupAllConflictTypes builds one merge that conflicts in every way at
// once: uu both modified, aa both added, du deleted by us, ud deleted by
// them, dd both deleted through a rename/rename, plus the au and ua paths
// the rename/rename pair creates.
func setupAllConflictTypes(t *testing.T) string {
	t.Helper()
	dir := conflictInitRepo(t)
	conflictWriteFile(t, dir, "uu.txt", "base uu\nline2\nline3\n")
	conflictWriteFile(t, dir, "du.txt", "base du\n")
	conflictWriteFile(t, dir, "ud.txt", "base ud\n")
	conflictWriteFile(t, dir, "dd.txt", "base dd\n")
	conflictCommitAll(t, dir, "base")

	conflictGitRun(t, dir, "checkout", "-q", "-b", "side")
	conflictWriteFile(t, dir, "uu.txt", "side uu\nline2\nline3\n")
	conflictWriteFile(t, dir, "du.txt", "side du modified\n")
	conflictGitRun(t, dir, "rm", "-q", "ud.txt")
	conflictGitRun(t, dir, "mv", "dd.txt", "renamed_side.txt")
	conflictWriteFile(t, dir, "aa.txt", "aa side\n")
	conflictCommitAll(t, dir, "side")

	conflictGitRun(t, dir, "checkout", "-q", "main")
	conflictWriteFile(t, dir, "uu.txt", "main uu\nline2\nline3\n")
	conflictGitRun(t, dir, "rm", "-q", "du.txt")
	conflictWriteFile(t, dir, "ud.txt", "main ud modified\n")
	conflictGitRun(t, dir, "mv", "dd.txt", "renamed_main.txt")
	conflictWriteFile(t, dir, "aa.txt", "aa main\n")
	conflictCommitAll(t, dir, "main")

	conflictMerge(t, dir, "side")
	return dir
}

// TestParseStatusConflictTypes checks the porcelain v2 unmerged records of
// a real merge: all seven conflict codes with their stage modes, objects
// and worktree flags.
func TestParseStatusConflictTypes(t *testing.T) {
	dir := setupAllConflictTypes(t)
	r := git.New(&git.LocalExecutor{Dir: dir, NoGlobalConfig: true})
	st, err := r.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Conflicts) != 7 {
		t.Fatalf("conflicts %+v", st.Conflicts)
	}
	type want struct {
		code                         string
		base, ours, theirs, worktree bool
	}
	wants := map[string]want{
		"uu.txt":           {"UU", true, true, true, true},
		"aa.txt":           {"AA", false, true, true, true},
		"du.txt":           {"DU", true, false, true, true},
		"ud.txt":           {"UD", true, true, false, true},
		"dd.txt":           {"DD", true, false, false, false},
		"renamed_main.txt": {"AU", false, true, false, true},
		"renamed_side.txt": {"UA", false, false, true, true},
	}
	seen := map[string]bool{}
	for _, c := range st.Conflicts {
		w, ok := wants[c.Path]
		if !ok {
			t.Errorf("unexpected conflict path %q", c.Path)
			continue
		}
		seen[c.Path] = true
		if c.Code != w.code {
			t.Errorf("%s code %q, want %q", c.Path, c.Code, w.code)
		}
		if c.HaveBase() != w.base || c.HaveOurs() != w.ours || c.HaveTheirs() != w.theirs {
			t.Errorf("%s have base/ours/theirs %v/%v/%v, want %v/%v/%v",
				c.Path, c.HaveBase(), c.HaveOurs(), c.HaveTheirs(), w.base, w.ours, w.theirs)
		}
		if c.WorktreeExists() != w.worktree {
			t.Errorf("%s worktree exists %v, want %v", c.Path, c.WorktreeExists(), w.worktree)
		}
		if c.IsSubmodule() {
			t.Errorf("%s is not a submodule", c.Path)
		}
		for _, mode := range []string{c.BaseMode, c.OursMode, c.TheirsMode, c.WorktreeMode} {
			if mode != "000000" && mode != "100644" {
				t.Errorf("%s unexpected mode %q", c.Path, mode)
			}
		}
		// A present stage names a real object; a missing one is all zeros.
		if c.HaveOurs() != (strings.Trim(c.OursOID, "0") != "") {
			t.Errorf("%s ours oid %q disagrees with HaveOurs", c.Path, c.OursOID)
		}
	}
	for path := range wants {
		if !seen[path] {
			t.Errorf("missing conflict %q", path)
		}
	}

	// The unstaged list carries the same conflict codes.
	codes := map[string]string{}
	for _, f := range st.Unstaged {
		if f.Status == 'U' {
			codes[f.Path] = f.Conflict
		} else if f.Conflict != "" {
			t.Errorf("%s is not unmerged but has conflict code %q", f.Path, f.Conflict)
		}
	}
	for path, w := range wants {
		if codes[path] != w.code {
			t.Errorf("unstaged %s conflict %q, want %q", path, codes[path], w.code)
		}
	}

	// Queries find a stage by path and miss clean ones.
	if _, ok := st.ConflictStage("uu.txt"); !ok {
		t.Error("ConflictStage missed uu.txt")
	}
	if _, ok := st.ConflictStage("missing.txt"); ok {
		t.Error("ConflictStage found a missing path")
	}
	if _, ok := st.ConflictIdentity("uu.txt"); !ok {
		t.Error("ConflictIdentity missed uu.txt")
	}
}

// TestParseStatusConflictRaw feeds raw porcelain bytes through the parser:
// zero object ids of any hash width mark a missing stage, and ordinary
// entries keep an empty conflict code.
func TestParseStatusConflictRaw(t *testing.T) {
	zero64 := strings.Repeat("0", 64)
	oid64 := "a" + zero64[1:]
	raw := "# branch.oid abcd1234\x00" +
		"# branch.head main\x00" +
		"u UU N... 100644 100644 000000 100644 " + oid64 + " " + oid64 + " " + zero64 + " sha256.txt\x00" +
		"1 M. N... 100644 100644 100644 " + oid64 + " " + oid64 + " plain.txt\x00" +
		"? new file.txt\x00"
	st := git.ParseStatus([]byte(raw))
	if len(st.Conflicts) != 1 {
		t.Fatalf("conflicts %+v", st.Conflicts)
	}
	c := st.Conflicts[0]
	if c.Code != "UU" || !c.HaveBase() || !c.HaveOurs() || c.HaveTheirs() || !c.WorktreeExists() {
		t.Errorf("sha256-width stage %+v", c)
	}
	if len(st.Unstaged) != 2 || st.Unstaged[0].Conflict != "UU" || st.Unstaged[1].Conflict != "" {
		t.Errorf("unstaged %+v", st.Unstaged)
	}
	if len(st.Staged) != 1 || st.Staged[0].Conflict != "" {
		t.Errorf("staged %+v", st.Staged)
	}

	// A hand-deleted worktree file has a zero worktree mode.
	rawDD := "u DD N... 100644 000000 000000 000000 9ad4c034936fa27737b43fcb587225578dfd921b 0000000000000000000000000000000000000000 0000000000000000000000000000000000000000 dd.txt\x00"
	st = git.ParseStatus([]byte(rawDD))
	if len(st.Conflicts) != 1 {
		t.Fatalf("dd conflicts %+v", st.Conflicts)
	}
	dd := st.Conflicts[0]
	if !dd.HaveBase() || dd.HaveOurs() || dd.HaveTheirs() || dd.WorktreeExists() {
		t.Errorf("dd stage %+v", dd)
	}
}

// TestParseStatusConflictTypeLabels covers the label of every conflict code.
func TestParseStatusConflictTypeLabels(t *testing.T) {
	want := map[string]string{
		"UU": "Both Modified",
		"AA": "Both Added",
		"DD": "Both Deleted",
		"DU": "Deleted by Us",
		"UD": "Deleted by Them",
		"AU": "Added by Us",
		"UA": "Added by Them",
		"XX": "Unmerged",
		"":   "Unmerged",
	}
	for code, label := range want {
		if got := git.ConflictTypeLabel(code); got != label {
			t.Errorf("ConflictTypeLabel(%q) = %q, want %q", code, got, label)
		}
	}
}

const conflictTwoBlocks = "keep\n<<<<<<< ours\nalpha ours\n=======\nalpha theirs\n>>>>>>> theirs\nmiddle\n<<<<<<< HEAD\nbeta ours\n=======\nbeta theirs\n>>>>>>> side\ntail\n"

// TestConflictParseHunksStandard reads positions, labels and sides of
// ordinary two-way marker blocks.
func TestConflictParseHunksStandard(t *testing.T) {
	hunks := git.ParseConflictHunks(conflictTwoBlocks)
	if len(hunks) != 2 {
		t.Fatalf("hunks %+v", hunks)
	}
	h := hunks[0]
	if h.Start != 1 || h.BaseStart != -1 || h.Mid != 3 || h.End != 6 {
		t.Errorf("positions %+v", h)
	}
	if h.OursLabel != "ours" || h.TheirsLabel != "theirs" || h.BaseLabel != "" {
		t.Errorf("labels %+v", h)
	}
	if h.Ours != "alpha ours\n" || h.Theirs != "alpha theirs\n" || h.Base != "" {
		t.Errorf("sides %+v", h)
	}
	second := hunks[1]
	if second.Start != 7 || second.End != 12 || second.OursLabel != "HEAD" || second.TheirsLabel != "side" {
		t.Errorf("second %+v", second)
	}
	if got := git.CountConflictHunks(conflictTwoBlocks); got != 2 {
		t.Errorf("count %d", got)
	}
	if got := git.CountConflictHunks("plain\ntext\n"); got != 0 {
		t.Errorf("plain count %d", got)
	}
}

// TestConflictParseHunksDiff3 keeps the diff3 base section out of both sides.
func TestConflictParseHunksDiff3(t *testing.T) {
	text := "<<<<<<< HEAD\nours line\n||||||| base label\nold line\n=======\ntheirs line\n>>>>>>> side\nafter\n"
	hunks := git.ParseConflictHunks(text)
	if len(hunks) != 1 {
		t.Fatalf("hunks %+v", hunks)
	}
	h := hunks[0]
	if h.BaseStart != 2 || h.Mid != 4 || h.End != 7 {
		t.Errorf("positions %+v", h)
	}
	if h.Ours != "ours line\n" || h.Base != "old line\n" || h.Theirs != "theirs line\n" {
		t.Errorf("sides ours=%q base=%q theirs=%q", h.Ours, h.Base, h.Theirs)
	}
	if h.BaseLabel != "base label" {
		t.Errorf("base label %q", h.BaseLabel)
	}
}

// TestConflictParseHunksEdgeCases covers empty sides, CRLF endings, missing
// trailing newlines and broken blocks that must not parse.
func TestConflictParseHunksEdgeCases(t *testing.T) {
	// Empty sides: a hunk may have no lines on either side.
	empty := "a\n<<<<<<< x\n=======\n>>>>>>> y\nb\n"
	hunks := git.ParseConflictHunks(empty)
	if len(hunks) != 1 || hunks[0].Ours != "" || hunks[0].Theirs != "" {
		t.Fatalf("empty sides %+v", hunks)
	}

	// CRLF endings stay in the sides byte for byte.
	crlf := "x\r\n<<<<<<< H\r\nours1\r\n=======\r\ntheirs1\r\n>>>>>>> S\r\ny\r\n"
	hunks = git.ParseConflictHunks(crlf)
	if len(hunks) != 1 || hunks[0].Ours != "ours1\r\n" || hunks[0].Theirs != "theirs1\r\n" {
		t.Fatalf("crlf %+v", hunks)
	}

	// A file without a trailing newline parses the same.
	noEnd := "<<<<<<< a\no\n=======\nt\n>>>>>>> b"
	hunks = git.ParseConflictHunks(noEnd)
	if len(hunks) != 1 || hunks[0].End != 5 {
		t.Fatalf("no trailing newline %+v", hunks)
	}

	// Broken blocks are not hunks: missing separator, missing close, or
	// a stray opening marker.
	for _, broken := range []string{
		"<<<<<<< a\no\n=======\nt\n",
		"<<<<<<< a\no\n",
		"<<<<<<< a\no\n>>>>>>> b\n",
		"=======\n>>>>>>> b\n",
	} {
		if n := git.CountConflictHunks(broken); n != 0 {
			t.Errorf("broken %q parsed as %d hunks", broken, n)
		}
	}

	// A nested opening marker breaks the outer block.
	nested := "<<<<<<< a\nx\n<<<<<<< b\ny\n=======\nz\n>>>>>>> c\n"
	if n := git.CountConflictHunks(nested); n != 1 {
		t.Errorf("nested parses %d hunks, want the inner one only", n)
	}
}

// TestConflictHunkMarkers detects full and half-eaten markers, in LF and
// CRLF files, without false positives on ordinary text.
func TestConflictHunkMarkers(t *testing.T) {
	for _, text := range []string{
		"<<<<<<< HEAD\n",
		"content\n>>>>>>> side\n",
		"a\n=======\nb\n",
		"a\n||||||| base\nb\n",
		"plain\n<<<<<<<\r\n",    // half marker in a CRLF file
		"plain\n>>>>>>> x\r\n",  // closing marker in a CRLF file
		"a\r\n=======\r\nb\r\n", // separator in a CRLF file
	} {
		if !git.HasConflictMarkers(text) {
			t.Errorf("markers missed in %q", text)
		}
	}
	for _, clean := range []string{
		"plain text\n",
		"markdown\n========\n",       // eight signs are a heading underline
		"short <<< and >>> inline\n", // markers must start the line
		"  <<<<<<< indented\n",       // an indented marker is content
		"a\n|||||||\n",               // a bare pipe run is not a base marker
		"",
	} {
		if git.HasConflictMarkers(clean) {
			t.Errorf("false positive in %q", clean)
		}
	}
}

// TestConflictHunkCompose resolves blocks by choice and preserves the
// bytes of everything else.
func TestConflictHunkCompose(t *testing.T) {
	// A partial choice leaves the other blocks marked.
	part := git.ComposeConflictHunks(conflictTwoBlocks, []git.ConflictChoice{{Ours: true}})
	wantPart := "keep\nalpha ours\nmiddle\n<<<<<<< HEAD\nbeta ours\n=======\nbeta theirs\n>>>>>>> side\ntail\n"
	if part != wantPart {
		t.Errorf("part\n%q\nwant\n%q", part, wantPart)
	}
	if git.CountConflictHunks(part) != 1 {
		t.Errorf("part still has %d hunks", git.CountConflictHunks(part))
	}

	// Both sides concatenate ours before theirs; empty choices keep markers.
	all := git.ComposeConflictHunks(conflictTwoBlocks, []git.ConflictChoice{{Ours: true, Theirs: true}, {Theirs: true}})
	wantAll := "keep\nalpha ours\nalpha theirs\nmiddle\nbeta theirs\ntail\n"
	if all != wantAll {
		t.Errorf("all\n%q\nwant\n%q", all, wantAll)
	}
	none := git.ComposeConflictHunks(conflictTwoBlocks, []git.ConflictChoice{{}, {}})
	if none != conflictTwoBlocks {
		t.Errorf("empty choices changed the text:\n%q", none)
	}

	// An empty chosen side removes the block without leaving a blank line.
	gone := git.ComposeConflictHunks("a\n<<<<<<< x\n=======\nb\n>>>>>>> y\n", []git.ConflictChoice{{Ours: true}})
	if gone != "a\n" {
		t.Errorf("gone %q", gone)
	}

	// CRLF files keep their carriage returns everywhere.
	crlf := "x\r\n<<<<<<< H\r\nours1\r\n=======\r\ntheirs1\r\n>>>>>>> S\r\ny\r\n"
	got := git.ComposeConflictHunks(crlf, []git.ConflictChoice{{Theirs: true}})
	if got != "x\r\ntheirs1\r\ny\r\n" {
		t.Errorf("crlf compose %q", got)
	}

	// No trailing newline stays absent; diff3 base content never appears.
	noEnd := git.ComposeConflictHunks("<<<<<<< a\no\n=======\nt\n>>>>>>> b", []git.ConflictChoice{{Ours: true}})
	if noEnd != "o\n" {
		t.Errorf("no end newline %q", noEnd)
	}
	diff3 := "<<<<<<< HEAD\nours line\n||||||| base\nold line\n=======\ntheirs line\n>>>>>>> side\n"
	got = git.ComposeConflictHunks(diff3, []git.ConflictChoice{{Ours: true, Theirs: true}})
	if got != "ours line\ntheirs line\n" {
		t.Errorf("diff3 both %q", got)
	}

	// A file without hunks comes back unchanged.
	plain := "no\nconflict\n"
	if got := git.ComposeConflictHunks(plain, []git.ConflictChoice{{Ours: true}}); got != plain {
		t.Errorf("plain compose %q", got)
	}
}

// TestConflictHunkApply replaces one block by index and rejects unknown
// sides and indexes.
func TestConflictHunkApply(t *testing.T) {
	got, ok := git.ApplyConflictHunk(conflictTwoBlocks, 1, "theirs")
	want := "keep\n<<<<<<< ours\nalpha ours\n=======\nalpha theirs\n>>>>>>> theirs\nmiddle\nbeta theirs\ntail\n"
	if !ok || got != want {
		t.Errorf("apply\n%q ok=%v", got, ok)
	}
	got, ok = git.ApplyConflictHunk(got, 0, "both")
	if !ok || got != "keep\nalpha ours\nalpha theirs\nmiddle\nbeta theirs\ntail\n" {
		t.Errorf("both\n%q ok=%v", got, ok)
	}
	if _, ok := git.ApplyConflictHunk(conflictTwoBlocks, 5, "ours"); ok {
		t.Error("an index past the end applied")
	}
	if _, ok := git.ApplyConflictHunk(conflictTwoBlocks, -1, "ours"); ok {
		t.Error("a negative index applied")
	}
	if _, ok := git.ApplyConflictHunk(conflictTwoBlocks, 0, "sideways"); ok {
		t.Error("an unknown side applied")
	}
	if _, ok := git.ApplyConflictHunk("plain\n", 0, "ours"); ok {
		t.Error("a file without hunks applied")
	}
}

// TestConflictParseBinary detects NUL bytes in the first 8192 bytes only.
func TestConflictParseBinary(t *testing.T) {
	if git.IsBinaryContent([]byte("plain text\n")) {
		t.Error("plain text is binary")
	}
	if git.IsBinaryContent(nil) {
		t.Error("empty content is binary")
	}
	if !git.IsBinaryContent([]byte{'a', 0, 'b'}) {
		t.Error("NUL byte missed")
	}
	big := make([]byte, 8192)
	for i := range big {
		big[i] = 'x'
	}
	if git.IsBinaryContent(big) {
		t.Error("8192 clean bytes are binary")
	}
	withNUL := append(append([]byte{}, big...), 0)
	if git.IsBinaryContent(withNUL) {
		t.Error("a NUL past byte 8192 counts as binary")
	}
	big[8191] = 0
	if !git.IsBinaryContent(big) {
		t.Error("a NUL at byte 8191 was missed")
	}
}
