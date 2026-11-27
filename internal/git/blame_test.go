package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBlameLines(t *testing.T) {
	dir := testRepo(t)
	file := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "b.txt")
	gitOut(t, dir, "commit", "-q", "-m", "first draft")
	if err := os.WriteFile(file, []byte("alpha\nBETA\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "commit", "-q", "-am", "second thoughts")
	bothExecutors(t, dir, func(t *testing.T, r *Repo) {
		got, err := r.BlameLines("b.txt", "")
		if err != nil || len(got) != 4 {
			t.Fatalf("%+v %v", got, err)
		}
		want := []struct {
			text, subject string
			first         bool
		}{{"alpha", "first draft", true}, {"BETA", "second thoughts", true}, {"gamma", "first draft", true}, {"delta", "second thoughts", true}}
		for i, w := range want {
			g := got[i]
			if g.Number != i+1 || g.Text != w.text || g.Subject != w.subject || g.First != w.first || g.Author == "" || g.Date.IsZero() || len(g.Hash) != 40 {
				t.Errorf("line %d: %+v", i+1, g)
			}
		}
	})

	// A run of lines of one commit is shown once, at its first line.
	if err := os.WriteFile(file, []byte("alpha\nBETA\ngamma\ndelta\nepsilon\nzeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "commit", "-q", "-am", "more")
	r := New(&LocalExecutor{Dir: dir})
	got, err := r.BlameLines("b.txt", "")
	if err != nil || len(got) != 6 || !got[4].First || got[5].First || got[5].Subject != "more" || got[3].Subject != "second thoughts" {
		t.Fatalf("%+v %v", got, err)
	}
}
