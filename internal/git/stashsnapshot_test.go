package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStashSnapshotKeepsTheWorkingDirectory(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	log := NewCommandLog()
	r := New(&LocalExecutor{Dir: dir}).WithCommandLog(log)

	if err := r.StashSnapshot(StashOptions{Message: "nothing"}); err == nil {
		t.Fatal("a snapshot of a clean working directory succeeded")
	}

	writeFile(t, dir, "a.txt", "two\n")
	writeFile(t, dir, "b.txt", "staged\n")
	gitRun(t, dir, "add", "b.txt")
	writeFile(t, dir, "added.txt", "staged v1\n")
	gitRun(t, dir, "add", "added.txt")
	writeFile(t, dir, "added.txt", "working v2\n")
	writeFile(t, dir, "new.txt", "fresh\n")

	past := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
	files := map[string]string{
		"a.txt":     "two\n",
		"b.txt":     "staged\n",
		"added.txt": "working v2\n",
		"new.txt":   "fresh\n",
	}
	beforeInfo := make(map[string]os.FileInfo, len(files))
	for name := range files {
		p := filepath.Join(dir, name)
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat before %s: %v", name, err)
		}
		beforeInfo[name] = info
	}

	log = NewCommandLog()
	r = New(&LocalExecutor{Dir: dir}).WithCommandLog(log)
	if err := r.StashSnapshot(StashOptions{Message: "before the risky part", StageNew: true}); err != nil {
		t.Fatal(err)
	}
	records, _ := log.Snapshot()

	for name, want := range files {
		p := filepath.Join(dir, name)
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("%s is %q (%v), want %q", name, got, err, want)
		}
		afterInfo, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat after %s: %v", name, err)
		}
		if !os.SameFile(beforeInfo[name], afterInfo) {
			t.Fatalf("%s was replaced on disk (SameFile=false)", name)
		}
		if !afterInfo.ModTime().Equal(beforeInfo[name].ModTime()) {
			t.Fatalf("%s mtime changed from %v to %v", name, beforeInfo[name].ModTime(), afterInfo.ModTime())
		}
	}

	stashes, err := New(&LocalExecutor{Dir: dir}).Stashes()
	if err != nil || len(stashes) != 1 || !strings.Contains(stashes[0].Message, "before the risky part") {
		t.Fatalf("stashes %+v %v", stashes, err)
	}
	if unstaged := strings.TrimSpace(gitOut(t, dir, "diff", "--name-only")); unstaged != "a.txt\nadded.txt" {
		t.Fatalf("unstaged changes %q, want a.txt and added.txt", unstaged)
	}
	if staged := strings.TrimSpace(gitOut(t, dir, "diff", "--cached", "--name-only")); staged != "added.txt\nb.txt\nnew.txt" {
		t.Fatalf("staged changes %q, want added.txt, b.txt, new.txt", staged)
	}
	for spec, want := range map[string]string{
		":added.txt":          "staged v1\n",
		":b.txt":              "staged\n",
		":new.txt":            "fresh\n",
		"stash@{0}:a.txt":     "two\n",
		"stash@{0}:b.txt":     "staged\n",
		"stash@{0}:added.txt": "working v2\n",
		"stash@{0}:new.txt":   "fresh\n",
		"stash@{0}^2:b.txt":   "staged\n",
		"stash@{0}^2:new.txt": "fresh\n",
	} {
		if got := gitOutput(t, dir, "show", spec); got != want {
			t.Fatalf("git show %s = %q, want %q", spec, got, want)
		}
	}

	if len(records) < 3 {
		t.Fatalf("expected status, add, create, store records, got %#v", records)
	}
	tail := records[len(records)-3:]
	if tail[0].Command != "$ git add -A -- new.txt" ||
		tail[1].Command != "$ git stash create 'before the risky part'" ||
		tail[2].Command != "$ git stash store --message 'before the risky part' "+stashes[0].Hash {
		t.Fatalf("unexpected snapshot command log tail: %#v", tail)
	}
	for _, rec := range records {
		if strings.Contains(rec.Command, "stash push") || strings.Contains(rec.Command, "stash apply") {
			t.Fatalf("snapshot used legacy push/apply command: %q", rec.Command)
		}
	}
}

func TestStashSnapshotEdgeCasesAndMessages(t *testing.T) {
	t.Run("unborn branch rolls back newly staged files and keeps existing staged", func(t *testing.T) {
		dir := t.TempDir()
		gitRun(t, dir, "init", "-q", "-b", "main")
		gitRun(t, dir, "config", "user.email", "test@example.com")
		gitRun(t, dir, "config", "user.name", "Test")
		writeFile(t, dir, "staged.txt", "already staged\n")
		gitRun(t, dir, "add", "staged.txt")
		writeFile(t, dir, "new.txt", "untracked on unborn\n")

		past := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
		for _, name := range []string{"staged.txt", "new.txt"} {
			must(t, os.Chtimes(filepath.Join(dir, name), past, past))
		}
		beforeStaged, err := os.Stat(filepath.Join(dir, "staged.txt"))
		must(t, err)
		beforeNew, err := os.Stat(filepath.Join(dir, "new.txt"))
		must(t, err)

		r := New(&LocalExecutor{Dir: dir})
		if err := r.StashSnapshot(StashOptions{Message: "unborn", StageNew: true}); err == nil {
			t.Fatal("expected error on unborn branch")
		}
		if status := gitOutput(t, dir, "status", "--porcelain"); status != "A  staged.txt\n?? new.txt\n" {
			t.Fatalf("unborn status after failed snapshot = %q", status)
		}
		afterStaged, err := os.Stat(filepath.Join(dir, "staged.txt"))
		must(t, err)
		afterNew, err := os.Stat(filepath.Join(dir, "new.txt"))
		must(t, err)
		if !os.SameFile(beforeStaged, afterStaged) || !afterStaged.ModTime().Equal(past) ||
			!os.SameFile(beforeNew, afterNew) || !afterNew.ModTime().Equal(past) {
			t.Fatal("unborn snapshot mutated file inode or mtime")
		}
	})

	t.Run("staged only and untracked only and option validation", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "base\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "base")

		r := New(&LocalExecutor{Dir: dir})
		if err := r.StashSnapshot(StashOptions{Staged: true}); err == nil {
			t.Fatal("expected Staged option to be rejected")
		}
		if err := r.StashSnapshot(StashOptions{Untracked: true, StageNew: false}); err == nil {
			t.Fatal("expected Untracked without StageNew to be rejected")
		}

		// Untracked-only with StageNew=false must fail with no local changes to save.
		writeFile(t, dir, "only-new.txt", "untracked\n")
		past := time.Date(2022, 5, 6, 7, 8, 9, 0, time.UTC)
		must(t, os.Chtimes(filepath.Join(dir, "only-new.txt"), past, past))
		beforeNew, err := os.Stat(filepath.Join(dir, "only-new.txt"))
		must(t, err)
		if err := r.StashSnapshot(StashOptions{Message: "untracked ignored", StageNew: false}); err == nil || !strings.Contains(err.Error(), "no local changes to save") {
			t.Fatalf("expected no local changes error, got %v", err)
		}
		afterNew, err := os.Stat(filepath.Join(dir, "only-new.txt"))
		must(t, err)
		if !os.SameFile(beforeNew, afterNew) || !afterNew.ModTime().Equal(past) {
			t.Fatal("failed untracked-only snapshot touched only-new.txt")
		}
		if status := gitOutput(t, dir, "status", "--porcelain"); status != "?? only-new.txt\n" {
			t.Fatalf("status = %q, want ?? only-new.txt", status)
		}

		// Untracked-only with StageNew=true stages only-new.txt and creates a snapshot.
		if err := r.StashSnapshot(StashOptions{Message: "untracked staged", StageNew: true}); err != nil {
			t.Fatalf("untracked StageNew=true: %v", err)
		}
		afterNew, err = os.Stat(filepath.Join(dir, "only-new.txt"))
		must(t, err)
		if !os.SameFile(beforeNew, afterNew) || !afterNew.ModTime().Equal(past) {
			t.Fatal("StageNew=true snapshot rewrote only-new.txt")
		}
		if got := gitOutput(t, dir, "show", "stash@{0}:only-new.txt"); got != "untracked\n" {
			t.Fatalf("stash@{0}:only-new.txt = %q", got)
		}

		// Staged-only snapshot (only-new.txt is now staged in the index, no unstaged or untracked files).
		log := NewCommandLog()
		if err := r.WithCommandLog(log).StashSnapshot(StashOptions{Message: "staged only", StageNew: false}); err != nil {
			t.Fatalf("staged-only snapshot: %v", err)
		}
		stashes, err := r.Stashes()
		if err != nil || len(stashes) != 2 || stashes[0].Message != "staged only" {
			t.Fatalf("stashes after staged-only = %+v, %v", stashes, err)
		}
		recs, _ := log.Snapshot()
		if len(recs) != 2 ||
			recs[0].Command != "$ git stash create 'staged only'" ||
			recs[1].Command != "$ git stash store --message 'staged only' "+stashes[0].Hash {
			t.Fatalf("expected exact 2 commands (create + store) when StageNew=false, got %#v", recs)
		}
	})

	t.Run("messages with empty whitespace spaces and newlines", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "base\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "base")
		writeFile(t, dir, "a.txt", "modified\n")

		r := New(&LocalExecutor{Dir: dir})
		for _, tc := range []struct {
			msg      string
			wantText string
		}{
			{msg: "", wantText: "git stash store"},
			{msg: "   \t\n  ", wantText: "git stash store"},
			{msg: "  padded with spaces  ", wantText: "padded with spaces"},
			{msg: "first line\nsecond line", wantText: "first line second line"},
		} {
			if err := r.StashSnapshot(StashOptions{Message: tc.msg}); err != nil {
				t.Fatalf("StashSnapshot(%q): %v", tc.msg, err)
			}
			stashes, err := r.Stashes()
			if err != nil || len(stashes) == 0 || !strings.Contains(stashes[0].Message, tc.wantText) {
				t.Fatalf("StashSnapshot(%q) -> top stash %+v, err %v, want %q", tc.msg, stashes, err, tc.wantText)
			}
		}
	})
}

type failSnapshotExecutor struct {
	Executor
	failSubcmd  string
	emptyCreate bool
	failUnstage bool
}

func (f *failSnapshotExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if f.emptyCreate && len(args) >= 2 && args[0] == "stash" && args[1] == "create" {
		return []byte("\n"), nil
	}
	if len(args) >= 2 && args[0] == "stash" && args[1] == f.failSubcmd {
		return nil, &Error{Args: args, ExitCode: 1, Stderr: "simulated " + f.failSubcmd + " failure"}
	}
	if f.failUnstage && len(args) >= 1 && (args[0] == "reset" || args[0] == "rm") {
		return nil, &Error{Args: args, ExitCode: 1, Stderr: "simulated unstage failure"}
	}
	return f.Executor.Run(ctx, stdin, args...)
}

func TestStashSnapshotRollbackPreservesPrimaryErrorAndExistingStaged(t *testing.T) {
	t.Run("store failure rolls back only newly staged files and preserves existing staged", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "base\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "base")

		writeFile(t, dir, "existing.txt", "kept staged\n")
		gitRun(t, dir, "add", "existing.txt")
		writeFile(t, dir, "new.txt", "should return to untracked\n")

		r := New(&failSnapshotExecutor{
			Executor:   &LocalExecutor{Dir: dir},
			failSubcmd: "store",
		})
		err := r.StashSnapshot(StashOptions{Message: "fail store", StageNew: true})
		if err == nil || !strings.Contains(err.Error(), "simulated store failure") {
			t.Fatalf("expected simulated store failure, got %v", err)
		}
		if status := gitOutput(t, dir, "status", "--porcelain"); status != "A  existing.txt\n?? new.txt\n" {
			t.Fatalf("status after rollback = %q, want A  existing.txt and ?? new.txt", status)
		}
		if stashes, err := New(&LocalExecutor{Dir: dir}).Stashes(); err != nil || len(stashes) != 0 {
			t.Fatalf("expected 0 stashes after failed store, got %+v (%v)", stashes, err)
		}
	})

	for _, subcmd := range []string{"create", "store"} {
		t.Run(subcmd+" failure plus unstage failure reports both and keeps existing staged", func(t *testing.T) {
			dir := initRepo(t)
			writeFile(t, dir, "a.txt", "base\n")
			gitRun(t, dir, "add", "a.txt")
			gitRun(t, dir, "commit", "-q", "-m", "base")

			writeFile(t, dir, "existing.txt", "kept staged\n")
			gitRun(t, dir, "add", "existing.txt")
			writeFile(t, dir, "new.txt", "untracked\n")

			r := New(&failSnapshotExecutor{
				Executor:    &LocalExecutor{Dir: dir},
				failSubcmd:  subcmd,
				failUnstage: true,
			})
			err := r.StashSnapshot(StashOptions{Message: "fail both", StageNew: true})
			if err == nil {
				t.Fatal("expected error when both snapshot and rollback fail")
			}
			if !strings.Contains(err.Error(), "simulated "+subcmd+" failure") || !strings.Contains(err.Error(), "simulated unstage failure") {
				t.Fatalf("expected error to contain both primary and unstage failures, got %q", err.Error())
			}
			var gitErr *Error
			if !errors.As(err, &gitErr) {
				t.Fatalf("expected errors.As(*Error) to succeed, got %T: %v", err, err)
			}
			if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(staged, "existing.txt") {
				t.Fatalf("existing.txt lost from index: %q", staged)
			}
		})
	}

	t.Run("empty create output plus unstage failure reports both and keeps existing staged", func(t *testing.T) {
		dir := initRepo(t)
		writeFile(t, dir, "a.txt", "base\n")
		gitRun(t, dir, "add", "a.txt")
		gitRun(t, dir, "commit", "-q", "-m", "base")

		writeFile(t, dir, "existing.txt", "kept staged\n")
		gitRun(t, dir, "add", "existing.txt")
		writeFile(t, dir, "new.txt", "untracked\n")

		r := New(&failSnapshotExecutor{
			Executor:    &LocalExecutor{Dir: dir},
			emptyCreate: true,
			failUnstage: true,
		})
		err := r.StashSnapshot(StashOptions{Message: "empty create", StageNew: true})
		if err == nil || !strings.Contains(err.Error(), "no local changes to save") || !strings.Contains(err.Error(), "simulated unstage failure") {
			t.Fatalf("expected both no local changes and unstage failure, got %v", err)
		}
		if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(staged, "existing.txt") {
			t.Fatalf("existing.txt lost from index: %q", staged)
		}
	})
}

func TestStashSnapshotSSHLoopbackAndCommandLog(t *testing.T) {
	srv := startSSHD(t)
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	writeFile(t, dir, "a.txt", "two\n")
	writeFile(t, dir, "b.txt", "staged\n")
	gitRun(t, dir, "add", "b.txt")
	writeFile(t, dir, "new.txt", "fresh over ssh\n")

	past := time.Date(2021, 7, 8, 9, 10, 11, 0, time.UTC)
	beforeInfo := map[string]os.FileInfo{}
	for _, name := range []string{"a.txt", "b.txt", "new.txt"} {
		p := filepath.Join(dir, name)
		must(t, os.Chtimes(p, past, past))
		info, err := os.Stat(p)
		must(t, err)
		beforeInfo[name] = info
	}

	log := NewCommandLog()
	sshRepo := New(srv.executor(t, dir))
	msg := "ssh snapshot\nsecond line"
	if err := sshRepo.WithCommandLog(log).StashSnapshot(StashOptions{Message: msg, StageNew: true}); err != nil {
		t.Fatalf("SSH StashSnapshot: %v", err)
	}
	records, _ := log.Snapshot()

	for _, name := range []string{"a.txt", "b.txt", "new.txt"} {
		afterInfo, err := os.Stat(filepath.Join(dir, name))
		must(t, err)
		if !os.SameFile(beforeInfo[name], afterInfo) || !afterInfo.ModTime().Equal(past) {
			t.Fatalf("SSH snapshot rewrote %s", name)
		}
	}

	stashes, err := sshRepo.Stashes()
	if err != nil || len(stashes) != 1 || stashes[0].Message != "ssh snapshot second line" {
		t.Fatalf("SSH stashes = %+v, %v", stashes, err)
	}

	if len(records) < 3 {
		t.Fatalf("expected SSH records for add, create, store, got %#v", records)
	}
	tail := records[len(records)-3:]
	if tail[0].Command != "$ git add -A -- new.txt" ||
		tail[1].Command != "$ git stash create 'ssh snapshot\nsecond line'" ||
		tail[2].Command != "$ git stash store --message 'ssh snapshot\nsecond line' "+stashes[0].Hash {
		t.Fatalf("unexpected SSH command log tail: %#v", tail)
	}
}

type cancelStashStepExecutor struct {
	Executor
	cancelSubcmd string
}

func (c *cancelStashStepExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "stash" && args[1] == c.cancelSubcmd {
		cCtx, cancel := context.WithCancel(ctx)
		cancel()
		return c.Executor.Run(cCtx, stdin, args...)
	}
	return c.Executor.Run(ctx, stdin, args...)
}

func TestStashSnapshotContextCancellationLeavesZeroSideEffects(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "base a\n")
	writeFile(t, dir, "b.txt", "base b\n")
	gitRun(t, dir, "add", "a.txt", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")

	writeFile(t, dir, "a.txt", "seed stash\n")
	gitRun(t, dir, "stash", "push", "-q", "-m", "existing stash")
	seedRef := gitOut(t, dir, "rev-parse", "refs/stash")

	writeFile(t, dir, "a.txt", "unstaged edit\n")
	writeFile(t, dir, "b.txt", "staged edit\n")
	gitRun(t, dir, "add", "b.txt")
	writeFile(t, dir, "new.txt", "untracked edit\n")

	beforeStatus := gitOutput(t, dir, "status", "--porcelain")
	beforeDiff := gitOutput(t, dir, "diff")
	beforeCached := gitOutput(t, dir, "diff", "--cached")
	beforeStashList := gitOut(t, dir, "stash", "list", "--format=%gd:%H:%gs")

	for _, step := range []string{"create", "store", "push", "apply", "pop"} {
		log := NewCommandLog()
		r := New(&cancelStashStepExecutor{
			Executor:     &LocalExecutor{Dir: dir},
			cancelSubcmd: step,
		}).WithCommandLog(log)

		var err error
		switch step {
		case "create", "store":
			err = r.StashSnapshot(StashOptions{Message: "canceled snapshot " + step, StageNew: true})
		case "push":
			err = r.StashPushWith(StashOptions{Message: "canceled push", StageNew: true})
		case "apply":
			err = r.StashApplyWith("stash@{0}", StashApplyOptions{Pop: false, Index: true})
		case "pop":
			err = r.StashApplyWith("stash@{0}", StashApplyOptions{Pop: true, Index: true})
		}
		if err == nil {
			t.Fatalf("step %s: expected error on canceled context", step)
		}
		if got := gitOut(t, dir, "rev-parse", "refs/stash"); got != seedRef {
			t.Fatalf("step %s: refs/stash = %q, want %q", step, got, seedRef)
		}
		if got := gitOut(t, dir, "stash", "list", "--format=%gd:%H:%gs"); got != beforeStashList {
			t.Fatalf("step %s: stash list = %q, want %q", step, got, beforeStashList)
		}
		if got := gitOutput(t, dir, "status", "--porcelain"); got != beforeStatus {
			t.Fatalf("step %s: status = %q, want %q", step, got, beforeStatus)
		}
		if got := gitOutput(t, dir, "diff"); got != beforeDiff {
			t.Fatalf("step %s: diff = %q, want %q", step, got, beforeDiff)
		}
		if got := gitOutput(t, dir, "diff", "--cached"); got != beforeCached {
			t.Fatalf("step %s: diff --cached = %q, want %q", step, got, beforeCached)
		}
		for name, want := range map[string]string{
			"a.txt":   "unstaged edit\n",
			"b.txt":   "staged edit\n",
			"new.txt": "untracked edit\n",
		} {
			got, readErr := os.ReadFile(filepath.Join(dir, name))
			if readErr != nil || string(got) != want {
				t.Fatalf("step %s: %s = %q (%v), want %q", step, name, got, readErr, want)
			}
		}
		if records, _ := log.Snapshot(); len(records) == 0 {
			t.Fatalf("step %s: expected command log records", step)
		}
	}
}
