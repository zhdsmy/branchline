package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type reviewFailStashDrop struct{ Executor }

func (e reviewFailStashDrop) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "stash" && args[1] == "drop" {
		return nil, &Error{Args: args, ExitCode: 128, Stderr: "fixture stash drop rejected"}
	}
	return e.Executor.Run(ctx, stdin, args...)
}

func TestReviewStashRenameFailedDropKeepsBothEntries(t *testing.T) {
	for _, entry := range []bool{false, true} {
		name := "current ref"
		if entry {
			name = "selected entry"
		}
		t.Run(name, func(t *testing.T) {
			dir, r, before := reviewRenameStashes(t, true)
			r.Exec = reviewFailStashDrop{Executor: r.Exec}
			var err error
			if entry {
				err = r.StashRenameEntry(selectionFrom(before, before[1]), "renamed selected", 1)
			} else {
				err = r.StashRename(before[1].Name, "renamed selected")
			}
			if err == nil || !strings.Contains(err.Error(), "renamed stash") || !strings.Contains(err.Error(), "original entry was not removed") || !strings.Contains(err.Error(), "fixture stash drop rejected") {
				t.Fatalf("partial drop failure not reported: %v", err)
			}
			after, readErr := r.Stashes()
			if readErr != nil || len(after) != 4 || after[0].Hash == before[1].Hash || after[0].Message != "renamed selected" {
				t.Fatalf("replacement was not kept first: before=%+v after=%+v err=%v", before, after, readErr)
			}
			for i := range before {
				if after[i+1].Hash != before[i].Hash || after[i+1].Message != before[i].Message {
					t.Fatalf("failed drop changed original entry %d: before=%+v after=%+v", i, before[i], after[i+1])
				}
			}
			if got := gitOut(t, dir, "cat-file", "-t", after[0].Hash); got != "commit" {
				t.Fatalf("replacement is not reachable as a commit: %s", got)
			}
			if got := gitOut(t, dir, "status", "--porcelain"); got != "" {
				t.Fatalf("failed drop changed checkout: %s", got)
			}
		})
	}
}

func TestReviewStashRenameDuplicateGroupChangeKeepsOriginal(t *testing.T) {
	dir, r, before := reviewRenameStashes(t, false)
	selected := selectionFrom(before, before[1])
	gitRun(t, dir, "update-ref", "-m", before[1].Message, "refs/stash", before[1].Hash, before[0].Hash)
	if err := r.StashRenameEntry(selected, "renamed selected", 1); err == nil || !strings.Contains(err.Error(), "changed while the stash operation was waiting") {
		t.Fatalf("changed duplicate group was accepted: %v", err)
	}
	after, err := r.Stashes()
	if err != nil || len(after) != 4 || after[0].Hash != before[1].Hash || after[1].Hash != before[0].Hash || after[2].Hash != before[1].Hash || after[3].Hash != before[2].Hash {
		t.Fatalf("rejected rename changed entries: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestReviewStashRenameByRefPreservesDuplicateOccurrence(t *testing.T) {
	dir, r, before := reviewRenameStashes(t, false)
	selected := before[1]
	gitRun(t, dir, "update-ref", "-m", selected.Message, "refs/stash", selected.Hash, before[0].Hash)
	current, err := r.Stashes()
	if err != nil || len(current) != 4 || current[0].Hash != selected.Hash || current[0].Message != selected.Message || current[2].Name != "stash@{2}" {
		t.Fatalf("duplicate fixture: %+v %v", current, err)
	}
	ex := &reviewFailStashDrop{Executor: r.Exec}
	r.Exec = ex
	if err := r.StashRename(current[2].Name, "renamed selected"); err == nil || !strings.Contains(err.Error(), "original entry was not removed") {
		t.Fatalf("drop failure not reported: %v", err)
	}
	after, err := r.Stashes()
	if err != nil || len(after) != 5 || after[0].Message != "renamed selected" || after[0].Hash == selected.Hash {
		t.Fatalf("replacement missing: %+v %v", after, err)
	}
	if after[1].Hash != current[0].Hash || after[1].Message != current[0].Message || after[2].Hash != current[1].Hash || after[3].Hash != selected.Hash || after[3].Message != selected.Message || after[4].Hash != current[3].Hash {
		t.Fatalf("failed ref rename removed another duplicate: before=%+v after=%+v", current, after)
	}
	r.Exec = ex.Executor
	if err := r.StashRename(after[3].Name, "renamed selected"); err != nil {
		t.Fatal(err)
	}
	after, err = r.Stashes()
	if err != nil || len(after) != 4 || after[0].Message != "renamed selected" || after[0].Hash == selected.Hash || after[1].Hash != selected.Hash || after[1].Message != selected.Message || after[2].Hash != before[0].Hash || after[3].Hash != before[2].Hash {
		t.Fatalf("ref rename removed the newest duplicate: %+v %v", after, err)
	}
}

func TestReviewStashRenameOverSSHRewritesSelectedEntry(t *testing.T) {
	srv := startSSHD(t)
	dir := reviewSSHStashRepo(t)
	r := New(srv.executor(t, dir))
	before, err := r.Stashes()
	if err != nil || len(before) != 3 {
		t.Fatalf("ssh stash fixture: %+v %v", before, err)
	}
	selected := before[1]
	identity := gitOut(t, dir, "show", "-s", "--format=%T %P", selected.Hash)
	gitRun(t, dir, "config", "user.name", "SSH Rename Identity")
	gitRun(t, dir, "config", "user.email", "ssh-rename@example.invalid")
	started := time.Now().Add(-2 * time.Second).Unix()
	if err := r.StashRenameEntry(selectionFrom(before, selected), "renamed over ssh", 1); err != nil {
		t.Fatal(err)
	}
	after, err := r.Stashes()
	if err != nil || len(after) != 3 || after[0].Hash == selected.Hash || after[0].Message != "renamed over ssh" {
		t.Fatalf("ssh rename result: before=%+v after=%+v err=%v", selected, after, err)
	}
	if after[1].Hash != before[0].Hash || after[2].Hash != before[2].Hash {
		t.Fatalf("ssh rename changed unrelated entries: %+v", after)
	}
	if got := gitOut(t, dir, "show", "-s", "--format=%T %P", after[0].Hash); got != identity {
		t.Fatalf("ssh rename changed tree/parents: %s -> %s", identity, got)
	}
	got := gitOut(t, dir, "show", "-s", "--format=%an <%ae>|%cn <%ce>|%at|%ct|%B", after[0].Hash)
	parts := strings.Split(got, "|")
	if len(parts) != 5 || parts[0] != "SSH Rename Identity <ssh-rename@example.invalid>" || parts[1] != parts[0] || parts[4] != "renamed over ssh" {
		t.Fatalf("ssh rewritten identity/message: %q", got)
	}
	author, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	committer, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	old := int64(978307200)
	if author == old || committer == old || author < started || committer < started || author > time.Now().Add(2*time.Second).Unix() || committer > time.Now().Add(2*time.Second).Unix() {
		t.Fatalf("ssh dates are not the remote current time: %q", got)
	}
}

func reviewSSHStashRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_DATE", "2001-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2001-01-01T00:00:00Z")
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitRun(t, dir, "config", "user.name", "Fixture Identity")
	gitRun(t, dir, "config", "user.email", "fixture@example.invalid")
	writeFile(t, dir, "a.txt", "base\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-qm", "base")
	for _, name := range []string{"first", "selected", "last"} {
		writeFile(t, dir, "a.txt", name+"\n")
		writeFile(t, dir, "extra.txt", name+" extra\n")
		gitRun(t, dir, "stash", "push", "-q", "--include-untracked", "-m", name)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	return dir
}
