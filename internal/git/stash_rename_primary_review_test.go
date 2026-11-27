package git

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func reviewRenameStashes(t *testing.T, untracked bool) (string, *Repo, []Stash) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_DATE", "2001-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2001-01-01T00:00:00Z")
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "base\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	for _, name := range []string{"first", "selected", "last"} {
		writeFile(t, dir, "a.txt", name+"\n")
		args := []string{"stash", "push", "-q", "-m", name}
		if untracked {
			writeFile(t, dir, "extra.txt", name+" extra\n")
			args = append(args, "--include-untracked")
		}
		gitRun(t, dir, args...)
	}
	r := New(&LocalExecutor{Dir: dir})
	list, err := r.Stashes()
	if err != nil || len(list) != 3 {
		t.Fatalf("stash fixture: %+v %v", list, err)
	}
	return dir, r, list
}

func TestReviewStashRenameRewritesCommit(t *testing.T) {
	for _, untracked := range []bool{false, true} {
		name := "two parents"
		if untracked {
			name = "three parents"
		}
		t.Run(name, func(t *testing.T) {
			dir, r, before := reviewRenameStashes(t, untracked)
			selected := before[1]
			identity := gitOut(t, dir, "show", "-s", "--format=%T %P", selected.Hash)
			gitRun(t, dir, "config", "user.name", "Rename Identity")
			gitRun(t, dir, "config", "user.email", "rename@example.invalid")
			t.Setenv("GIT_AUTHOR_DATE", "2002-01-01T00:00:00Z")
			t.Setenv("GIT_COMMITTER_DATE", "2002-01-01T00:00:00Z")
			if err := r.StashRenameEntry(selectionFrom(before, selected), "renamed selected", 1); err != nil {
				t.Fatal(err)
			}
			after, err := r.Stashes()
			if err != nil || len(after) != 3 {
				t.Fatalf("rename list: %+v %v", after, err)
			}
			if after[0].Hash == selected.Hash || after[0].Message != "renamed selected" {
				t.Fatalf("rename did not rewrite the selected commit: before=%+v after=%+v", selected, after[0])
			}
			if after[1].Hash != before[0].Hash || after[2].Hash != before[2].Hash {
				t.Fatalf("rename changed unrelated stash entries: %+v", after)
			}
			if got := gitOut(t, dir, "show", "-s", "--format=%T %P", after[0].Hash); got != identity {
				t.Fatalf("rename changed tree/parents: %s -> %s", identity, got)
			}
			want := "Rename Identity <rename@example.invalid>|Rename Identity <rename@example.invalid>|1009843200|1009843200|renamed selected"
			if got := gitOut(t, dir, "show", "-s", "--format=%an <%ae>|%cn <%ce>|%at|%ct|%B", after[0].Hash); got != want {
				t.Fatalf("rewritten commit identity/message: %q", got)
			}
			if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
				t.Fatalf("rename changed working tree/index: %s", got)
			}
		})
	}
}

type reviewFailStashStore struct{ Executor }

func (e reviewFailStashStore) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "stash" && args[1] == "store" {
		return nil, &Error{Args: args, ExitCode: 128, Stderr: "fixture stash store rejected"}
	}
	return e.Executor.Run(ctx, stdin, args...)
}

func TestReviewStashRenameFailedStoreKeepsOriginalEntry(t *testing.T) {
	for _, entry := range []bool{false, true} {
		name := "current ref"
		if entry {
			name = "selected entry"
		}
		t.Run(name, func(t *testing.T) {
			dir, r, before := reviewRenameStashes(t, true)
			r.Exec = reviewFailStashStore{Executor: r.Exec}
			var err error
			if entry {
				err = r.StashRenameEntry(selectionFrom(before, before[1]), "renamed selected", 1)
			} else {
				err = r.StashRename(before[1].Name, "renamed selected")
			}
			if err == nil || !strings.Contains(err.Error(), "fixture stash store rejected") {
				t.Fatalf("store failure not reported: %v", err)
			}
			after, readErr := r.Stashes()
			if readErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("failed rename lost/changed stash entries: before=%+v after=%+v err=%v", before, after, readErr)
			}
			if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
				t.Fatalf("failed rename changed checkout: %s", got)
			}
		})
	}
}
