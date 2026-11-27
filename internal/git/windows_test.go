package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bothExecutors runs fn with the repository at dir reached directly and
// through the fake ssh, as the two must give the same answers.
func bothExecutors(t *testing.T, dir string, fn func(t *testing.T, r *Repo)) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(bin, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("local", func(t *testing.T) { fn(t, New(&LocalExecutor{Dir: dir})) })
	t.Run("ssh", func(t *testing.T) {
		fn(t, New(&SSHExecutor{Host: "devbox", Dir: dir, SSH: bin, NoMultiplex: true}))
	})
}

func subjectList(cs []Commit) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.Subject)
	}
	return strings.Join(s, ",")
}

func TestSearch(t *testing.T) {
	dir := testRepo(t)
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		got, err := r.Search(SearchOptions{Field: SearchMessage, Query: "FEATURE"})
		if err != nil || subjectList(got) != "merge feature,feature" {
			t.Errorf("message: %q %v", subjectList(got), err)
		}
		got, _ = r.Search(SearchOptions{Field: SearchMessage, Query: "FEATURE", MatchCase: true})
		if len(got) != 0 {
			t.Errorf("case-sensitive message: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchMessage, Query: "f.ature"})
		if len(got) != 0 {
			t.Errorf("a dot is not a pattern without Regex: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchMessage, Query: "^f.ature", Regex: true})
		if subjectList(got) != "feature" {
			t.Errorf("regex message: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchAuthor, Query: "ada@example"})
		if len(got) != 4 {
			t.Errorf("author: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchContent, Query: "bee"})
		if subjectList(got) != "feature" {
			t.Errorf("content: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchContent, Query: "TWO"})
		if subjectList(got) != "second" {
			t.Errorf("content ignoring case: %q", subjectList(got))
		}
		got, _ = r.Search(SearchOptions{Field: SearchPath, Query: "B.TX"})
		if subjectList(got) != "feature" {
			t.Errorf("path: %q", subjectList(got))
		}
		all, _ := r.Log(LogOptions{})
		got, _ = r.Search(SearchOptions{Field: SearchHash, Query: all[2].Hash[:8]})
		if len(got) != 1 || got[0].Hash != all[2].Hash {
			t.Errorf("hash: %+v", got)
		}
		if _, err := r.Search(SearchOptions{Query: "  "}); err == nil {
			t.Error("an empty search ran")
		}
		// A query is one argument, never read by a shell.
		got, err = r.Search(SearchOptions{Field: SearchMessage, Query: "'; touch pwned; '"})
		if err != nil || len(got) != 0 {
			t.Errorf("quoted query: %v %v", got, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
			t.Fatal("the query ran in a shell")
		}
		authors, err := r.Authors()
		if err != nil || len(authors) != 1 || authors[0] != "Ada <ada@example.com>" {
			t.Errorf("authors %q %v", authors, err)
		}
		paths, err := r.TrackedPaths()
		if err != nil || strings.Join(paths, ",") != "a.txt,b.txt,staged file.txt" {
			t.Errorf("paths %q %v", paths, err)
		}
	})
}

func TestApplyPatchWith(t *testing.T) {
	dir := testRepo(t)
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	gitIn("stash", "-u", "-q")
	// A patch of the last commit, then that commit taken back.
	patch := gitIn("format-patch", "--stdout", "-1", "HEAD~1")
	gitIn("reset", "-q", "--hard", "HEAD~2")
	r := New(&LocalExecutor{Dir: dir})
	res, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Check: true})
	if err != nil || !res.Checked || !strings.Contains(res.Stat, "a.txt") {
		t.Fatalf("check: %+v %v", res, err)
	}
	if st, _ := r.Status(); len(st.Unstaged)+len(st.Staged) != 0 {
		t.Fatalf("a check changed the tree: %+v", st)
	}
	if _, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Stage: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(); len(st.Staged) != 1 || st.Staged[0].Path != "a.txt" {
		t.Fatalf("staged: %+v", st)
	}
	if _, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Check: true}); err == nil {
		t.Fatal("the patch applied twice")
	}
	if _, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Reverse: true, Stage: true}); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if st, _ := r.Status(); len(st.Staged)+len(st.Unstaged) != 0 {
		t.Fatalf("after reverse: %+v", st)
	}
	if _, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Commit: true, Reverse: true}); err == nil {
		t.Fatal("reverse with commit was accepted")
	}
	t.Setenv("GIT_COMMITTER_NAME", "Ada")
	t.Setenv("GIT_COMMITTER_EMAIL", "ada@example.com")
	if _, err := r.ApplyPatchWith([]byte(patch), ApplyOptions{Commit: true, ThreeWay: true}); err != nil {
		t.Fatalf("am: %v", err)
	}
	if msg, _ := r.LastMessage(); strings.TrimSpace(msg) != "second" {
		t.Fatalf("am made %q", msg)
	}
	// A patch that does not apply leaves git am stopped; abort puts it back.
	broken := strings.Replace(patch, " one\n", " uno\n", 1)
	if _, err := r.ApplyPatchWith([]byte(broken), ApplyOptions{Commit: true}); err == nil {
		t.Fatal("a broken patch applied")
	}
	if err := r.AmAbort(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyPatchWith(nil, ApplyOptions{}); err == nil {
		t.Fatal("an empty patch applied")
	}
}

func TestPatchFileName(t *testing.T) {
	for subject, want := range map[string]string{
		"Fix the bug: crash on start!": "0001-Fix-the-bug-crash-on-start.patch",
		"...":                          "0001-patch.patch",
		"v1.2 release":                 "0001-v1.2-release.patch",
	} {
		if got := PatchFileName(1, subject); got != want {
			t.Errorf("%q: %q, want %q", subject, got, want)
		}
	}
}

func TestBinaryBlobAndSubmodule(t *testing.T) {
	dir := testRepo(t)
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_ALLOW_PROTOCOL=file")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	blob := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}
	if err := os.WriteFile(filepath.Join(dir, "logo.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(dir, "add", "logo.bin")
	gitIn(dir, "commit", "-q", "-m", "logo")
	if err := os.WriteFile(filepath.Join(dir, "logo.bin"), append(blob, blob...), 0o644); err != nil {
		t.Fatal(err)
	}

	sub := t.TempDir()
	gitIn(sub, "init", "-q", "-b", "main")
	gitIn(sub, "commit", "-q", "--allow-empty", "-m", "sub one")
	gitIn(dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "lib")
	gitIn(dir, "commit", "-q", "-m", "add lib")
	gitIn(sub, "commit", "-q", "--allow-empty", "-m", "sub two")
	gitIn(filepath.Join(dir, "lib"), "pull", "-q", "origin", "main")
	gitIn(dir, "commit", "-q", "-m", "bump lib", "--", "lib")

	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		old, err := r.BinaryBlob(BlobSide{Rev: "HEAD", Path: "logo.bin"}, 4)
		if err != nil || !old.Exists || old.Size != 8 || string(old.Head) != "\x89PNG" {
			t.Fatalf("HEAD side: %+v %v", old, err)
		}
		now, err := r.BinaryBlob(BlobSide{Rev: "worktree", Path: "logo.bin"}, 64)
		if err != nil || now.Size != 16 || len(now.Head) != 16 {
			t.Fatalf("worktree side: %+v %v", now, err)
		}
		if none, err := r.BinaryBlob(BlobSide{}, 4); err != nil || none.Exists {
			t.Fatalf("no side: %+v %v", none, err)
		}
		if _, err := r.BinaryBlob(BlobSide{Rev: "HEAD", Path: "missing.bin"}, 4); err == nil {
			t.Fatal("a missing file had a blob")
		}
		ch, err := r.SubmoduleDiff("HEAD~1", "HEAD", "lib", false)
		if err != nil || ch.Old == "" || ch.New == "" || len(ch.Commits) != 1 || ch.Commits[0].Subject != "sub two" || !ch.Commits[0].Added {
			t.Fatalf("submodule change %+v %v", ch, err)
		}
	})
	if dump := HexDump([]byte("ABCDEFGHIJKLMNOPQ\x00")); !strings.HasPrefix(dump, "00000000  41 42 43 44 45 46 47 48  49 4a") || !strings.Contains(dump, "|ABCDEFGHIJKLMNOP|") || !strings.Contains(dump, "00000010  51 00") || !strings.HasSuffix(dump, "|Q.|") {
		t.Errorf("hex dump\n%s", dump)
	}
}

func TestParseSubmoduleLog(t *testing.T) {
	ch := ParseSubmoduleLog("lib", "Submodule lib 0000000...1234abc (new submodule)\n")
	if ch.Old != "0000000" || ch.New != "1234abc" || ch.Note != "new submodule" {
		t.Errorf("new: %+v", ch)
	}
	ch = ParseSubmoduleLog("lib", "Submodule lib 1111111..2222222:\n  > added\n  < removed\nSubmodule lib contains modified content\n")
	if ch.Old != "1111111" || ch.New != "2222222" || len(ch.Commits) != 2 || ch.Commits[1].Added || !ch.Dirty {
		t.Errorf("range: %+v", ch)
	}
}

func TestGlobalIdentity(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	r := New(&LocalExecutor{Dir: t.TempDir()})
	id, err := r.GlobalIdentity()
	if err != nil || id != (Identity{}) {
		t.Fatalf("empty config: %+v %v", id, err)
	}
	if err := r.SetGlobalIdentity(Identity{Name: "Grace Hopper", Email: "grace@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetGlobalIdentity(Identity{Name: " ", Email: ""}); err != nil {
		t.Fatal(err)
	}
	id, err = r.GlobalIdentity()
	if err != nil || id.Name != "Grace Hopper" || id.Email != "grace@example.com" {
		t.Fatalf("after set: %+v %v", id, err)
	}
}

func TestCommitFileStatsAt(t *testing.T) {
	dir := testRepo(t)
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		all, _, err := r.CommitFileStatsAt("")
		if err != nil || len(all) != 4 {
			t.Fatalf("HEAD: %d %v", len(all), err)
		}
		feature, files, err := r.CommitFileStatsAt("feature/x")
		if err != nil || subjectList(feature) != "feature,first" || len(files) != 2 || files[0][0].Path != "b.txt" {
			t.Fatalf("feature/x: %q %+v %v", subjectList(feature), files, err)
		}
		if _, _, err := r.CommitFileStatsAt("--all"); err == nil {
			t.Fatal("an option was taken as a reference")
		}
	})
}
