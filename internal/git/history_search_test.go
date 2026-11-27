package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// longRepo makes a history longer than a page of the log: n commits, the
// first of which, far down, adds needle.txt by Grace.
func longRepo(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		cmd.Env = append(cmd.Env, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ada := []string{"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com"}
	grace := []string{"GIT_AUTHOR_NAME=Grace", "GIT_AUTHOR_EMAIL=grace@example.com", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com"}
	run(nil, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "needle.txt"), []byte("a haystack needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(nil, "add", "needle.txt")
	run(grace, "commit", "-q", "-m", "hide the needle")
	// The rest are made at once with fast-import, as n commits would be
	// slow one by one.
	var b strings.Builder
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\ncommitter Ada <ada@example.com> %d +0000\ndata <<EOF\nfiller %d\nEOF\n", i, 1700000000+i, i)
		if i == 1 {
			b.WriteString("from refs/heads/main^0\n")
		}
		fmt.Fprintf(&b, "M 644 inline filler.txt\ndata <<EOF\n%d\nEOF\n\n", i)
	}
	cmd := exec.Command("git", "-C", dir, "fast-import", "--quiet")
	cmd.Env = append(os.Environ(), append(ada, "GIT_CONFIG_GLOBAL=/dev/null")...)
	cmd.Stdin = strings.NewReader(b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, out)
	}
	run(nil, "reset", "-q", "--hard", "main")
	return dir
}

// TestSearchAllHistory finds a commit far past the log's first page by
// message, author, content, path and hash, here and over ssh.
func TestSearchAllHistory(t *testing.T) {
	dir := longRepo(t, 620)
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		page, err := r.Log(LogOptions{})
		if err != nil || len(page) != 500 {
			t.Fatalf("first page %d %v", len(page), err)
		}
		for _, c := range page {
			if c.Subject == "hide the needle" {
				t.Fatal("the needle is on the first page: the test proves nothing")
			}
		}
		one := func(what string, o SearchOptions) Commit {
			t.Helper()
			got, err := r.Search(o)
			if err != nil || len(got) != 1 || got[0].Subject != "hide the needle" {
				t.Fatalf("%s: %q %v", what, subjectList(got), err)
			}
			return got[0]
		}
		needle := one("message", SearchOptions{Field: SearchMessage, Query: "needle"})
		one("author", SearchOptions{Field: SearchAuthor, Query: "grace@"})
		one("content -G", SearchOptions{Field: SearchContent, Query: "haystack"})
		one("content -S", SearchOptions{Field: SearchContent, Query: "Haystack", Occurrences: true})
		one("path", SearchOptions{Field: SearchPath, Query: "needle"})
		one("hash", SearchOptions{Field: SearchHash, Query: needle.Hash[:10]})
		// A path keeps any search to the commits that changed it.
		one("author in path", SearchOptions{Field: SearchAuthor, Query: "example.com", Paths: []string{"needle.txt"}})
		if got, _ := r.Search(SearchOptions{Field: SearchMessage, Query: "needle", Paths: []string{"filler.txt"}}); len(got) != 0 {
			t.Fatalf("path filter: %q", subjectList(got))
		}
		// The history bar's query finds a hash past the first page too.
		if got, err := r.Log(LogOptions{Query: needle.Hash[:12]}); err != nil || len(got) != 1 || got[0].Hash != needle.Hash {
			t.Fatalf("log hash query: %q %v", subjectList(got), err)
		}

		// Pages of matches follow one another.
		first, err := r.Search(SearchOptions{Field: SearchMessage, Query: "filler", Limit: 300})
		if err != nil || len(first) != 300 {
			t.Fatalf("first page of matches %d %v", len(first), err)
		}
		next, err := r.Search(SearchOptions{Field: SearchMessage, Query: "filler", Limit: 500, Skip: 300})
		if err != nil || len(next) != 319 || next[0].Hash == first[0].Hash {
			t.Fatalf("next page of matches %d %v", len(next), err)
		}
	})
}

func TestBlobContent(t *testing.T) {
	dir := testRepo(t)
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		for _, tc := range []struct {
			side BlobSide
			want string
		}{
			{BlobSide{Rev: "HEAD~1", Path: "a.txt"}, "one\ntwo\n"},
			{BlobSide{Rev: "index", Path: "staged file.txt"}, "s\n"},
			{BlobSide{Rev: "worktree", Path: "a.txt"}, "one\ntwo\nthree\n"},
		} {
			got, err := r.BlobContent(tc.side)
			if err != nil || string(got) != tc.want {
				t.Errorf("%+v: %q %v", tc.side, got, err)
			}
		}
		if _, err := r.BlobContent(BlobSide{Path: "a.txt"}); err == nil {
			t.Error("a side the file is not on was read")
		}
	})
}
