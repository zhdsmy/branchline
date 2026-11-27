package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeGit225Simulator creates a wrapper script around the real local git
// executable that reports Git 2.25.1 and rejects CLI flags introduced after
// Git 2.25 (init -b/--initial-branch, rev-list --no-commit-header,
// rev-parse --end-of-options, stash --staged/-S, rebase --update-refs, and
// interactive rebase update-ref todo instructions).
func writeGit225Simulator(t *testing.T) (gitWrapper string, markerPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	must(t, err)
	binDir := t.TempDir()
	markerPath = filepath.Join(binDir, "invocations.log")
	gitWrapper = filepath.Join(binDir, "git")

	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + ShellQuote(markerPath) + `
subcmd=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-C" ] || [ "$prev" = "-c" ]; then
    prev=""
    continue
  fi
  case "$arg" in
    -C|-c)
      prev="$arg"
      continue
      ;;
    --version)
      echo "git version 2.25.1 (Ubuntu 2.25.1-1ubuntu3.13)"
      exit 0
      ;;
    -*)
      continue
      ;;
    *)
      if [ -z "$subcmd" ]; then
        subcmd="$arg"
      fi
      ;;
  esac
done

case "$subcmd" in
  init)
    for arg in "$@"; do
      case "$arg" in
        -b|-b*|--initial-branch|--initial-branch=*)
          echo "error: unknown switch 'b'" >&2
          exit 129
          ;;
      esac
    done
    ;;
  rev-list)
    for arg in "$@"; do
      if [ "$arg" = "--no-commit-header" ]; then
        echo "fatal: unrecognized argument: --no-commit-header" >&2
        exit 128
      fi
    done
    ;;
  rev-parse)
    for arg in "$@"; do
      if [ "$arg" = "--end-of-options" ]; then
        echo "fatal: Needed a single revision" >&2
        exit 128
      fi
    done
    ;;
  stash)
    for arg in "$@"; do
      case "$arg" in
        --staged|-S)
          echo "error: unknown option 'staged'" >&2
          exit 129
          ;;
      esac
    done
    ;;
  rebase)
    for arg in "$@"; do
      case "$arg" in
        --update-refs)
          echo "error: unknown option 'update-refs'" >&2
          exit 129
          ;;
      esac
    done
    ;;
esac

exec ` + ShellQuote(realGit) + ` "$@"
`
	must(t, os.WriteFile(gitWrapper, []byte(script), 0o755))
	return gitWrapper, markerPath
}

func wrapSSHWithGitDir(t *testing.T, srv *sshServer, gitWrapper string) string {
	t.Helper()
	binDir := filepath.Dir(gitWrapper)
	sshWrapper := filepath.Join(t.TempDir(), "ssh-git225")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"set -- \"$@\"\n" +
		"args=\"\"\n" +
		"while [ $# -gt 1 ]; do\n" +
		"  args=\"$args '$1'\"\n" +
		"  shift\n" +
		"done\n" +
		"eval \"set -- $args\"\n" +
		"exec " + ShellQuote(srv.ssh) + " \"$@\" \"PATH=" + binDir + ":\\$PATH; $last\"\n"
	must(t, os.WriteFile(sshWrapper, []byte(script), 0o755))
	return sshWrapper
}

func openGit225Repo(t *testing.T, srv *sshServer, sshWrapper, gitWrapper, dir string) *Repo {
	t.Helper()
	if srv != nil {
		ex := srv.executor(t, dir)
		ex.SSH = sshWrapper
		ex.NoMultiplex = true
		return New(ex)
	}
	return New(&LocalExecutor{Dir: dir, Git: gitWrapper, NoGlobalConfig: true})
}

func TestGit225CompatibilityInit(t *testing.T) {
	srv := startSSHD(t)
	gitWrapper, _ := writeGit225Simulator(t)
	sshWrapper := wrapSSHWithGitDir(t, srv, gitWrapper)

	for _, mode := range []string{"local-225", "ssh-225", "local-modern"} {
		t.Run(mode, func(t *testing.T) {
			open := func(dir string) *Repo {
				switch mode {
				case "local-225":
					return openGit225Repo(t, nil, "", gitWrapper, dir)
				case "ssh-225":
					return openGit225Repo(t, srv, sshWrapper, gitWrapper, dir)
				default:
					return New(&LocalExecutor{Dir: dir, NoGlobalConfig: true})
				}
			}

			// 1. Fresh empty directory with custom branch and default branch.
			emptyCustom := t.TempDir()
			rCustom := open(emptyCustom)
			must(t, rCustom.Init("feature/git-225"))
			if got := strings.TrimSpace(gitOutput(t, emptyCustom, "symbolic-ref", "HEAD")); got != "refs/heads/feature/git-225" {
				t.Fatalf("symbolic-ref HEAD = %q, want refs/heads/feature/git-225", got)
			}
			st, err := rCustom.Status()
			must(t, err)
			if st.Branch != "feature/git-225" || st.Head != "" {
				t.Fatalf("unborn status = %+v, want branch feature/git-225 and empty Head", st)
			}

			emptyDefault := t.TempDir()
			rDefault := open(emptyDefault)
			must(t, rDefault.Init(""))
			if got := strings.TrimSpace(gitOutput(t, emptyDefault, "symbolic-ref", "HEAD")); got != "refs/heads/main" {
				t.Fatalf("default symbolic-ref HEAD = %q, want refs/heads/main", got)
			}

			// 2. Existing born repository must not have its current branch rewritten by Init.
			born := testRepo(t)
			beforeHead := strings.TrimSpace(gitOutput(t, born, "rev-parse", "HEAD"))
			beforeSym := strings.TrimSpace(gitOutput(t, born, "symbolic-ref", "HEAD"))
			beforePorcelain := gitOutput(t, born, "status", "--porcelain=v1")
			must(t, open(born).Init("hijack-branch"))
			if got := strings.TrimSpace(gitOutput(t, born, "symbolic-ref", "HEAD")); got != beforeSym {
				t.Fatalf("existing born repo symbolic-ref changed from %q to %q", beforeSym, got)
			}
			if got := strings.TrimSpace(gitOutput(t, born, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("existing born repo HEAD changed from %q to %q", beforeHead, got)
			}
			if got := gitOutput(t, born, "status", "--porcelain=v1"); got != beforePorcelain {
				t.Fatalf("existing born repo status changed:\nbefore:\n%s\nafter:\n%s", beforePorcelain, got)
			}

			// 3. Existing unborn repository must not have its unborn branch rewritten by Init.
			if err := open(emptyCustom).Init("another-unborn"); err != nil {
				t.Fatalf("re-init unborn repo failed: %v", err)
			}
			if got := strings.TrimSpace(gitOutput(t, emptyCustom, "symbolic-ref", "HEAD")); got != "refs/heads/feature/git-225" {
				t.Fatalf("existing unborn repo branch rewritten to %q, want refs/heads/feature/git-225", got)
			}

			// 4. Existing linked worktree must not have its HEAD (or main worktree HEAD) rewritten by Init.
			linkedDir := filepath.Join(t.TempDir(), "linked-wt")
			runGit(t, born, "worktree", "add", "-q", "-b", "wt-branch", linkedDir, "HEAD")
			beforeLinkedSym := strings.TrimSpace(gitOutput(t, linkedDir, "symbolic-ref", "HEAD"))
			beforeLinkedHead := strings.TrimSpace(gitOutput(t, linkedDir, "rev-parse", "HEAD"))
			must(t, open(linkedDir).Init("hijack-linked"))
			if got := strings.TrimSpace(gitOutput(t, linkedDir, "symbolic-ref", "HEAD")); got != beforeLinkedSym {
				t.Fatalf("linked worktree symbolic-ref changed from %q to %q", beforeLinkedSym, got)
			}
			if got := strings.TrimSpace(gitOutput(t, linkedDir, "rev-parse", "HEAD")); got != beforeLinkedHead {
				t.Fatalf("linked worktree HEAD changed from %q to %q", beforeLinkedHead, got)
			}
			if got := strings.TrimSpace(gitOutput(t, born, "symbolic-ref", "HEAD")); got != beforeSym {
				t.Fatalf("main worktree symbolic-ref changed after linked Init from %q to %q", beforeSym, got)
			}

			// 5. Invalid branch names must be rejected without initializing .git.
			for _, bad := range []string{"-b", "--quiet", "HEAD", "bad..name", "has space", "/leading", "trailing/", ".hidden", "bad.lock", "sub/bad.lock/leaf", "@{0}", "a~1"} {
				badDir := t.TempDir()
				if err := open(badDir).Init(bad); err == nil {
					t.Fatalf("Init(%q) succeeded, want error", bad)
				}
				if _, statErr := os.Stat(filepath.Join(badDir, ".git")); !os.IsNotExist(statErr) {
					t.Fatalf("Init(%q) created .git on invalid branch name: %v", bad, statErr)
				}
			}
		})
	}
}

func TestGit225CompatibilityCompareSidesAndConfirmCommit(t *testing.T) {
	srv := startSSHD(t)
	gitWrapper, markerPath := writeGit225Simulator(t)
	sshWrapper := wrapSSHWithGitDir(t, srv, gitWrapper)

	dir := testRepo(t)
	commits, err := New(&LocalExecutor{Dir: dir}).Log(LogOptions{Limit: 10})
	must(t, err)
	bySubject := map[string]string{}
	for _, c := range commits {
		bySubject[c.Subject] = c.Hash
	}
	firstCommit := bySubject["first"]
	mergeCommit := bySubject["merge feature"]
	if firstCommit == "" || mergeCommit == "" {
		t.Fatalf("missing expected commits in %+v", commits)
	}

	unbornDir := t.TempDir()
	runGit(t, unbornDir, "init", "-q", "-b", "main")

	for _, remote := range []bool{false, true} {
		name := "local-225"
		if remote {
			name = "ssh-225"
		}
		t.Run(name, func(t *testing.T) {
			open := func(d string) *Repo {
				if remote {
					return openGit225Repo(t, srv, sshWrapper, gitWrapper, d)
				}
				return openGit225Repo(t, nil, "", gitWrapper, d)
			}

			r := open(dir)
			// b.txt was added in feature, so it is missing in firstCommit but present in mergeCommit.
			// This triggers missingObject(err) -> confirmRevision(firstCommit) -> confirmCommit(firstCommit).
			left, right, lName, rName, err := r.CompareSides(firstCommit, mergeCommit, "b.txt")
			must(t, err)
			if len(left) != 0 || string(right) != "bee\n" || lName != "b.txt" || rName != "b.txt" {
				t.Fatalf("CompareSides missing in firstCommit: left=%q right=%q names=(%q,%q)", left, right, lName, rName)
			}

			// Missing file against HEAD in born repo.
			left, right, _, _, err = r.CompareSides("HEAD", "HEAD", "nonexistent.txt")
			must(t, err)
			if len(left) != 0 || len(right) != 0 {
				t.Fatalf("CompareSides missing in HEAD: left=%q right=%q", left, right)
			}

			// Missing file against HEAD and index in unborn repo.
			rUnborn := open(unbornDir)
			left, right, _, _, err = rUnborn.CompareSides("", "index", "nonexistent.txt")
			must(t, err)
			if len(left) != 0 || len(right) != 0 {
				t.Fatalf("CompareSides unborn index: left=%q right=%q", left, right)
			}

			// Valid revision expressions containing spaces and colons (e.g. reflog date specs) must remain allowed.
			left, right, _, _, err = r.CompareSides("HEAD@{2099-01-01 00:00:00}", mergeCommit, "b.txt")
			must(t, err)
			if string(left) != "bee\n" || string(right) != "bee\n" {
				t.Fatalf("CompareSides with dated reflog spec: left=%q right=%q", left, right)
			}
			left, right, _, _, err = r.CompareSides("HEAD@{2099-01-01 00:00:00}", mergeCommit, "nonexistent.txt")
			must(t, err)
			if len(left) != 0 || len(right) != 0 {
				t.Fatalf("CompareSides missing path with dated reflog spec: left=%q right=%q", left, right)
			}

			// Non-existent revision must still fail.
			if _, _, _, _, err := r.CompareSides("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "HEAD", "b.txt"); err == nil {
				t.Fatal("CompareSides accepted missing commit revision as empty file")
			}

			// Option injection revisions starting with '-' or containing NUL/newline must be rejected before invoking git.
			beforeLog, err := os.ReadFile(markerPath)
			must(t, err)
			for _, badRev := range []string{"-q", "--end-of-options", "--all", "--show-toplevel", "--git-dir=/tmp", " -q", "HEAD\n--all", "HEAD\x00--all"} {
				if _, _, _, _, err := r.CompareSides(badRev, "HEAD", "b.txt"); err == nil {
					t.Fatalf("CompareSides(%q, HEAD) succeeded, want rejection", badRev)
				}
				if err := r.confirmCommit(badRev); err == nil {
					t.Fatalf("confirmCommit(%q) succeeded, want rejection", badRev)
				}
			}
			afterLog, err := os.ReadFile(markerPath)
			must(t, err)
			if string(afterLog) != string(beforeLog) {
				t.Fatalf("option injection revision invoked git:\n%s", string(afterLog[len(beforeLog):]))
			}
		})
	}
}

func TestGit225CompatibilityRebaseAndUpdateRefsGuard(t *testing.T) {
	srv := startSSHD(t)
	gitWrapper, _ := writeGit225Simulator(t)
	sshWrapper := wrapSSHWithGitDir(t, srv, gitWrapper)

	for _, remote := range []bool{false, true} {
		name := "local-225"
		if remote {
			name = "ssh-225"
		}
		t.Run(name, func(t *testing.T) {
			open := func(d string) *Repo {
				if remote {
					return openGit225Repo(t, srv, sshWrapper, gitWrapper, d)
				}
				return openGit225Repo(t, nil, "", gitWrapper, d)
			}

			// 1. RebasePlan & interactive rebase with PreparedMessage (G05) and Backup=true, UpdateRefs=false succeed on Git 2.25.
			dir, hashes := linearRepo(t, 4)
			r := open(dir)
			plan, err := r.RebasePlan("--root")
			must(t, err)
			if len(plan) != 4 || plan[0].Hash != hashes[0] || plan[3].Hash != hashes[3] {
				t.Fatalf("RebasePlan(--root) = %+v, want 4 commits in oldest-first order", plan)
			}
			for i, step := range plan {
				if step.Author == "" || step.Email == "" || step.Date.IsZero() {
					t.Fatalf("step[%d] missing metadata: %+v", i, step)
				}
			}
			for i := 1; i < len(plan); i++ {
				plan[i].Action, plan[i].Subject = "fixup", ""
			}
			wantMsg := "Prepared fold message on Git 2.25\n\nBody line 1\nBody line 2"
			plan[len(plan)-1].PreparedMessage = wantMsg
			must(t, r.RebaseWithOptions("--root", plan, RebaseOptions{Backup: true, UpdateRefs: false}))
			if gotMsg, err := r.Message("HEAD"); err != nil || gotMsg != wantMsg {
				t.Fatalf("Message(HEAD) = %q, %v; want %q", gotMsg, err, wantMsg)
			}
			if got := strings.TrimSpace(gitOutput(t, dir, "for-each-ref", "--format=%(refname)", "refs/backup")); !strings.HasPrefix(got, "refs/backup/main-") {
				t.Fatalf("backup ref missing: %q", got)
			}

			// 2. Ordinary RebaseOnto without UpdateRefs succeeds with autostash on Git 2.25.
			sDir, _ := stackedRepo(t)
			writeFile(t, sDir, "a.txt", "one\ndirty worktree\n")
			writeFile(t, sDir, "untracked.txt", "keep untracked\n")
			rStacked := open(sDir)
			must(t, rStacked.RebaseOnto("main", RebaseOntoOptions{Stash: true, UpdateRefs: false}))
			if exec.Command("git", "-C", sDir, "merge-base", "--is-ancestor", "main", "feature").Run() != nil {
				t.Fatal("feature was not rebased onto main")
			}
			if got, err := os.ReadFile(filepath.Join(sDir, "a.txt")); err != nil || string(got) != "one\ndirty worktree\n" {
				t.Fatalf("autostash not restored on 2.25: %q, %v", got, err)
			}

			// 3. RebaseOnto and RebaseWithOptions with UpdateRefs=true are rejected upfront on Git 2.25 with zero side effects.
			uDir, _ := stackedRepo(t)
			writeFile(t, uDir, "staged.txt", "staged content\n")
			gitRun(t, uDir, "add", "staged.txt")
			writeFile(t, uDir, "a.txt", "one\nunstaged edit\n")
			writeFile(t, uDir, "untracked.txt", "untracked content\n")

			beforeHead := gitOut(t, uDir, "rev-parse", "HEAD")
			beforeMid := gitOut(t, uDir, "rev-parse", "mid")
			beforeRefs := gitOutput(t, uDir, "for-each-ref", "--format=%(refname) %(objectname)")
			beforeStatus := gitOutput(t, uDir, "status", "--porcelain=v1")
			beforeDiff := gitOutput(t, uDir, "diff")
			beforeCached := gitOutput(t, uDir, "diff", "--cached")
			beforeStash := gitOutput(t, uDir, "stash", "list")
			beforeObjects := gitOutput(t, uDir, "cat-file", "--batch-check=%(objectname)", "--batch-all-objects")

			rUpdate := open(uDir)
			uPlan, err := rUpdate.RebasePlan("main")
			must(t, err)
			uPlan[0].PreparedMessage = "Must not write blob when UpdateRefs is rejected"

			err = rUpdate.RebaseOnto("main", RebaseOntoOptions{Stash: true, UpdateRefs: true})
			if err == nil || !strings.Contains(err.Error(), "2.38") {
				t.Fatalf("RebaseOnto(UpdateRefs: true) on 2.25 = %v, want error mentioning 2.38", err)
			}

			err = rUpdate.RebaseWithOptions("main", uPlan, RebaseOptions{Backup: true, UpdateRefs: true})
			if err == nil || !strings.Contains(err.Error(), "2.38") {
				t.Fatalf("RebaseWithOptions(Backup: true, UpdateRefs: true) on 2.25 = %v, want error mentioning 2.38", err)
			}

			if gitOut(t, uDir, "rev-parse", "HEAD") != beforeHead {
				t.Fatal("UpdateRefs rejection changed HEAD")
			}
			if gitOut(t, uDir, "rev-parse", "mid") != beforeMid {
				t.Fatal("UpdateRefs rejection changed dependent branch mid")
			}
			if got := gitOutput(t, uDir, "for-each-ref", "--format=%(refname) %(objectname)"); got != beforeRefs {
				t.Fatalf("UpdateRefs rejection mutated refs (e.g. backup ref):\nbefore:\n%s\nafter:\n%s", beforeRefs, got)
			}
			if got := gitOutput(t, uDir, "status", "--porcelain=v1"); got != beforeStatus {
				t.Fatalf("UpdateRefs rejection mutated status:\nbefore:\n%s\nafter:\n%s", beforeStatus, got)
			}
			if gitOutput(t, uDir, "diff") != beforeDiff || gitOutput(t, uDir, "diff", "--cached") != beforeCached {
				t.Fatal("UpdateRefs rejection mutated worktree or index diff")
			}
			if gitOutput(t, uDir, "stash", "list") != beforeStash {
				t.Fatal("UpdateRefs rejection mutated stash list")
			}
			if got := gitOutput(t, uDir, "cat-file", "--batch-check=%(objectname)", "--batch-all-objects"); got != beforeObjects {
				t.Fatalf("UpdateRefs rejection wrote git objects before failing:\nbefore:\n%s\nafter:\n%s", beforeObjects, got)
			}
			for _, stateDir := range []string{"rebase-merge", "rebase-apply"} {
				if _, statErr := os.Stat(filepath.Join(uDir, ".git", stateDir)); !os.IsNotExist(statErr) {
					t.Fatalf("UpdateRefs rejection left %s: %v", stateDir, statErr)
				}
			}
		})
	}
}

func TestGit225CompatibilityStashAndSaveSnapshotGuard(t *testing.T) {
	srv := startSSHD(t)
	gitWrapper, _ := writeGit225Simulator(t)
	sshWrapper := wrapSSHWithGitDir(t, srv, gitWrapper)

	for _, remote := range []bool{false, true} {
		name := "local-225"
		if remote {
			name = "ssh-225"
		}
		t.Run(name, func(t *testing.T) {
			dir := initRepo(t)
			writeFile(t, dir, "a.txt", "base a\n")
			writeFile(t, dir, "both.txt", "base both\n")
			gitRun(t, dir, "add", ".")
			gitRun(t, dir, "commit", "-qm", "base")

			writeFile(t, dir, "staged.txt", "staged only\n")
			writeFile(t, dir, "both.txt", "staged both v1\n")
			gitRun(t, dir, "add", "staged.txt", "both.txt")
			writeFile(t, dir, "both.txt", "worktree both v2\n")
			writeFile(t, dir, "a.txt", "unstaged a\n")
			writeFile(t, dir, "untracked.txt", "untracked file\n")

			beforeHead := gitOut(t, dir, "rev-parse", "HEAD")
			beforeRefs := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
			beforeStatus := gitOutput(t, dir, "status", "--porcelain=v1")
			beforeDiff := gitOutput(t, dir, "diff")
			beforeCached := gitOutput(t, dir, "diff", "--cached")
			beforeStash := gitOutput(t, dir, "stash", "list")

			var r *Repo
			if remote {
				r = openGit225Repo(t, srv, sshWrapper, gitWrapper, dir)
			} else {
				r = openGit225Repo(t, nil, "", gitWrapper, dir)
			}

			// 1. StashPushWith(Staged: true) must be rejected upfront on 2.25 with 0 side effects (never full-stashing).
			err := r.StashPushWith(StashOptions{Message: "staged on 225", Staged: true})
			if err == nil || !strings.Contains(err.Error(), "2.35") {
				t.Fatalf("StashPushWith(Staged: true) on 2.25 = %v, want error mentioning 2.35", err)
			}

			// 2. SaveSnapshot must be rejected upfront on 2.25 with 0 side effects.
			err = r.SaveSnapshot("snapshot-branch", "save snapshot on 225")
			if err == nil || !strings.Contains(err.Error(), "2.35") {
				t.Fatalf("SaveSnapshot on 2.25 = %v, want error mentioning 2.35", err)
			}

			if gitOut(t, dir, "rev-parse", "HEAD") != beforeHead {
				t.Fatal("staged stash / SaveSnapshot rejection changed HEAD")
			}
			if got := gitOutput(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"); got != beforeRefs {
				t.Fatalf("staged stash / SaveSnapshot rejection changed refs:\nbefore:\n%s\nafter:\n%s", beforeRefs, got)
			}
			if got := gitOutput(t, dir, "status", "--porcelain=v1"); got != beforeStatus {
				t.Fatalf("staged stash / SaveSnapshot rejection changed status:\nbefore:\n%s\nafter:\n%s", beforeStatus, got)
			}
			if gitOutput(t, dir, "diff") != beforeDiff || gitOutput(t, dir, "diff", "--cached") != beforeCached {
				t.Fatal("staged stash / SaveSnapshot rejection changed worktree or index diff")
			}
			if gitOutput(t, dir, "stash", "list") != beforeStash {
				t.Fatal("staged stash / SaveSnapshot rejection created a stash")
			}

			// 3. Normal stash operations (StashSnapshot, StashPushWith Untracked/StageNew, StashRename, StashApply, StashDrop) succeed on 2.25.
			must(t, r.StashSnapshot(StashOptions{Message: "snapshot create store", StageNew: false}))
			stashes, err := r.Stashes()
			must(t, err)
			if len(stashes) != 1 || stashes[0].Message != "snapshot create store" {
				t.Fatalf("Stashes after StashSnapshot = %+v", stashes)
			}
			must(t, r.StashDrop(stashes[0].Name))

			must(t, r.StashPushWith(StashOptions{Message: "regular stash 225", Untracked: true}))
			if got := gitOutput(t, dir, "status", "--porcelain=v1"); got != "" {
				t.Fatalf("status after regular stash = %q, want clean", got)
			}
			stashes, err = r.Stashes()
			must(t, err)
			if len(stashes) != 1 || !strings.Contains(stashes[0].Message, "regular stash 225") {
				t.Fatalf("Stashes after StashPushWith = %+v", stashes)
			}
			must(t, r.StashRename(stashes[0].Name, "renamed stash 225"))
			stashes, err = r.Stashes()
			must(t, err)
			if len(stashes) != 1 || !strings.Contains(stashes[0].Message, "renamed stash 225") {
				t.Fatalf("Stashes after StashRename = %+v", stashes)
			}
			must(t, r.StashApply(stashes[0].Name, true))
			st, err := r.Status()
			must(t, err)
			if len(st.Staged) == 0 || len(st.Unstaged) == 0 {
				t.Fatalf("Status after StashApply(pop=true) = %+v, expected restored changes", st)
			}
		})
	}
}

func TestGit225CompatibilityCapabilityFailureDoesNotGuessSupport(t *testing.T) {
	sentinel := &Error{
		Args:     []string{"--version"},
		ExitCode: 255,
		Stderr:   "cannot connect to dev@127.0.0.1: Connection refused",
	}
	ex := &stubVersionExecutor{err: sentinel}
	r := New(ex)

	checkErr := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s succeeded when capability detection failed", name)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("%s error = %v, want sentinel %v", name, err, sentinel)
		}
	}

	checkErr("Init", r.Init("main"))
	checkErr("RebaseOnto(UpdateRefs)", r.RebaseOnto("main", RebaseOntoOptions{UpdateRefs: true}))
	checkErr("RebaseWithOptions(UpdateRefs)", r.RebaseWithOptions("main", []RebaseTodo{{Action: "pick", Hash: "0123456789abcdef0123456789abcdef01234567", Subject: "c1"}}, RebaseOptions{Backup: true, UpdateRefs: true}))
	checkErr("StashPushWith(Staged)", r.StashPushWith(StashOptions{Staged: true}))
	checkErr("SaveSnapshot", r.SaveSnapshot("snap", "msg"))

	if ex.calls != 5 {
		t.Fatalf("executor calls = %d, want 5 (--version only, no write commands attempted)", ex.calls)
	}
	for i, args := range ex.args {
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("call[%d] = %v, want [--version]", i, args)
		}
	}
}

func TestConfiguredLinuxGitCompatibility(t *testing.T) {
	host, root := os.Getenv("BRANCHLINE_LINUX_SSH_HOST"), os.Getenv("BRANCHLINE_LINUX_SSH_REPO")
	if host == "" || root == "" {
		t.Skip("BRANCHLINE_LINUX_SSH_HOST and BRANCHLINE_LINUX_SSH_REPO are both required")
	}
	if !strings.HasPrefix(root, "/tmp/branchline-test-") || path.Clean(root) != root || strings.ContainsAny(root, "\n\r") {
		t.Fatal("refusing a directory outside the disposable remote test prefix")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	conn := &SSHExecutor{Host: host, Dir: root, NoMultiplex: true}
	sshOut := func(t *testing.T, command string) string {
		t.Helper()
		out, err := conn.RunShell(ctx, command)
		if err != nil {
			t.Fatalf("remote command %q failed: %v", command, err)
		}
		return string(out)
	}

	if got := strings.TrimSpace(sshOut(t, "uname -s")); got != "Linux" {
		t.Fatalf("remote uname -s = %q, want Linux", got)
	}

	rootRepo := New(&SSHExecutor{Host: host, Dir: root, NoMultiplex: true})
	caps, err := rootRepo.Capabilities()
	must(t, err)
	independentVer := strings.TrimSpace(sshOut(t, "git --version"))
	indepParsed, err := ParseGitVersion(independentVer)
	must(t, err)
	t.Logf("remote Git version: %s (parsed %+v, independent %q, caps %+v)", caps.RawVersion, caps.Version, independentVer, caps)

	if caps.RawVersion != independentVer || caps.Version != indepParsed {
		t.Fatalf("Capabilities() = (%q, %+v), independent ssh git --version = (%q, %+v)", caps.RawVersion, caps.Version, independentVer, indepParsed)
	}
	if !caps.MeetsMinimum {
		t.Fatalf("remote Git %+v (%q) does not meet minimum supported Git 2.25", caps.Version, caps.RawVersion)
	}
	if caps.InitInitialBranch != caps.Version.AtLeast(GitVersion{Major: 2, Minor: 28}) ||
		caps.StashStaged != caps.Version.AtLeast(GitVersion{Major: 2, Minor: 35}) ||
		caps.RebaseUpdateRefs != caps.Version.AtLeast(GitVersion{Major: 2, Minor: 38}) {
		t.Fatalf("inconsistent capability flags for version %+v: %+v", caps.Version, caps)
	}

	if expectRaw := strings.TrimSpace(os.Getenv("BRANCHLINE_LINUX_SSH_EXPECT_GIT")); expectRaw != "" {
		expectVer, err := ParseGitVersion(expectRaw)
		if err != nil {
			t.Fatalf("invalid BRANCHLINE_LINUX_SSH_EXPECT_GIT=%q: %v", expectRaw, err)
		}
		if caps.Version.Major != expectVer.Major || caps.Version.Minor != expectVer.Minor {
			t.Fatalf("expected remote Git %d.%d.x via BRANCHLINE_LINUX_SSH_EXPECT_GIT=%q, got %+v (independent %q)", expectVer.Major, expectVer.Minor, expectRaw, caps.Version, independentVer)
		}
		if strings.Count(expectRaw, ".") >= 2 && caps.Version.Patch != expectVer.Patch {
			t.Fatalf("expected remote Git %+v via BRANCHLINE_LINUX_SSH_EXPECT_GIT=%q, got %+v (independent %q)", expectVer, expectRaw, caps.Version, independentVer)
		}
		if expectVer.Major == 2 && expectVer.Minor == 25 && (caps.InitInitialBranch || caps.StashStaged || caps.RebaseUpdateRefs) {
			t.Fatalf("unexpected Git 2.25 capability flags: %+v", caps)
		}
	}

	mkRemoteCaseDir := func(t *testing.T, prefix string) string {
		t.Helper()
		dir := strings.TrimSpace(sshOut(t, "mktemp -d "+ShellQuote(root+"/"+prefix+".XXXXXX")))
		if path.Dir(dir) != root || !strings.HasPrefix(path.Base(dir), prefix+".") {
			t.Fatalf("unexpected remote case directory %q", dir)
		}
		t.Cleanup(func() {
			cleanCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if _, err := conn.RunShell(cleanCtx, "rm -rf -- "+ShellQuote(dir)); err != nil {
				t.Errorf("cleanup %s: %v", dir, err)
			}
		})
		return dir
	}

	t.Run("Init", func(t *testing.T) {
		// 1. Fresh empty directory with explicit branch name.
		dir := mkRemoteCaseDir(t, "init-custom")
		r := New(&SSHExecutor{Host: host, Dir: dir, NoMultiplex: true})
		must(t, r.Init("trunk"))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" symbolic-ref HEAD")); got != "refs/heads/trunk" {
			t.Fatalf("remote symbolic-ref HEAD = %q, want refs/heads/trunk", got)
		}
		st, err := r.Status()
		must(t, err)
		if st.Branch != "trunk" || st.Head != "" {
			t.Fatalf("remote Status() after Init = %+v, want branch trunk and empty Head", st)
		}

		// Re-running Init with a different branch name on an existing unborn repository must not rewrite HEAD.
		must(t, r.Init("other-branch"))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" symbolic-ref HEAD")); got != "refs/heads/trunk" {
			t.Fatalf("re-init overwrote unborn HEAD to %q, want refs/heads/trunk", got)
		}

		// Commit a file and verify re-running Init on a born repository also keeps HEAD on trunk.
		sshOut(t, "cd "+ShellQuote(dir)+" && git config user.name 'SSH Tester' && git config user.email ssh@example.invalid && printf 'hello\n' > a.txt")
		must(t, r.Stage("a.txt"))
		must(t, r.Commit("initial on trunk", false))
		bornHead := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" rev-parse HEAD"))
		must(t, r.Init("hijack"))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" symbolic-ref HEAD")); got != "refs/heads/trunk" {
			t.Fatalf("re-init overwrote born symbolic-ref HEAD to %q", got)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" rev-parse HEAD")); got != bornHead {
			t.Fatalf("re-init changed born HEAD from %q to %q", bornHead, got)
		}

		// Linked worktree: re-running Init must not rewrite linked worktree HEAD or main worktree HEAD.
		wtDir := mkRemoteCaseDir(t, "init-wt")
		sshOut(t, "rm -rf -- "+ShellQuote(wtDir)+" && git -C "+ShellQuote(dir)+" worktree add -q -b wt-ssh "+ShellQuote(wtDir)+" HEAD")
		rWT := New(&SSHExecutor{Host: host, Dir: wtDir, NoMultiplex: true})
		must(t, rWT.Init("hijack-wt"))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(wtDir)+" symbolic-ref HEAD")); got != "refs/heads/wt-ssh" {
			t.Fatalf("re-init overwrote linked worktree HEAD to %q, want refs/heads/wt-ssh", got)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(wtDir)+" rev-parse HEAD")); got != bornHead {
			t.Fatalf("re-init changed linked worktree HEAD commit from %q to %q", bornHead, got)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" symbolic-ref HEAD")); got != "refs/heads/trunk" {
			t.Fatalf("linked worktree Init changed main worktree HEAD to %q", got)
		}

		// Invalid branch name must fail without creating .git.
		badDir := mkRemoteCaseDir(t, "init-bad")
		rBad := New(&SSHExecutor{Host: host, Dir: badDir, NoMultiplex: true})
		if err := rBad.Init("bad..branch"); err == nil {
			t.Fatal("Init(bad..branch) succeeded on remote host")
		}
		sshOut(t, "test ! -e "+ShellQuote(badDir+"/.git"))
	})

	t.Run("CompareSides", func(t *testing.T) {
		dir := mkRemoteCaseDir(t, "compare")
		r := New(&SSHExecutor{Host: host, Dir: dir, NoMultiplex: true})
		must(t, r.Init("main"))

		// Unborn repository compare.
		left, right, _, _, err := r.CompareSides("", "index", "missing.txt")
		must(t, err)
		if len(left) != 0 || len(right) != 0 {
			t.Fatalf("unborn CompareSides = (%q, %q), want empty", left, right)
		}

		setup := `set -eu
cd ` + ShellQuote(dir) + `
git config user.name 'SSH Tester'
git config user.email ssh@example.invalid
printf 'one\n' > a.txt
git add a.txt
git commit -q -m c1
printf 'two\n' > a.txt
printf 'added in c2\n' > b.txt
git add a.txt b.txt
git commit -q -m c2
printf 'staged a\n' > a.txt
git add a.txt
printf 'worktree a\n' > a.txt
`
		sshOut(t, "/bin/sh -c "+ShellQuote(setup))
		c1 := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" rev-parse HEAD~1"))
		c2 := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" rev-parse HEAD"))

		// b.txt is missing in c1, present in c2 -> exercises confirmCommit(c1).
		left, right, _, _, err = r.CompareSides(c1, c2, "b.txt")
		must(t, err)
		if len(left) != 0 || string(right) != "added in c2\n" {
			t.Fatalf("CompareSides(c1, c2, b.txt) = (%q, %q)", left, right)
		}

		// Dated reflog revision expression with spaces and colons.
		left, right, _, _, err = r.CompareSides("HEAD@{2099-01-01 00:00:00}", c2, "nonexistent.txt")
		must(t, err)
		if len(left) != 0 || len(right) != 0 {
			t.Fatalf("CompareSides with dated reflog spec = (%q, %q)", left, right)
		}

		// HEAD vs index and HEAD vs worktree.
		left, right, _, _, err = r.CompareSides("", "index", "a.txt")
		must(t, err)
		if string(left) != "two\n" || string(right) != "staged a\n" {
			t.Fatalf("CompareSides HEAD vs index = (%q, %q)", left, right)
		}
		left, right, _, _, err = r.CompareSides("", "", "a.txt")
		must(t, err)
		if string(left) != "two\n" || string(right) != "worktree a\n" {
			t.Fatalf("CompareSides HEAD vs worktree = (%q, %q)", left, right)
		}

		// Option injection and missing revision must fail.
		if _, _, _, _, err := r.CompareSides("-q", c2, "b.txt"); err == nil {
			t.Fatal("CompareSides accepted -q revision")
		}
		if _, _, _, _, err := r.CompareSides("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", c2, "b.txt"); err == nil {
			t.Fatal("CompareSides accepted non-existent commit")
		}
	})

	t.Run("RebasePlanAndInteractiveRebase", func(t *testing.T) {
		dir := mkRemoteCaseDir(t, "rebase")
		setup := `set -eu
cd ` + ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'SSH Tester'
git config user.email ssh@example.invalid
printf 'f1\n' > f1.txt && git add f1.txt && git commit -q -m c1
printf 'f2\n' > f2.txt && git add f2.txt && git commit -q -m c2
printf 'f3\n' > f3.txt && git add f3.txt && git commit -q -m c3
git checkout -qb upstream HEAD~2
printf 'up\n' > up.txt && git add up.txt && git commit -q -m upstream
git checkout -q main
printf 'dirty\n' > f1.txt
printf 'untracked\n' > untracked.txt
`
		sshOut(t, "/bin/sh -c "+ShellQuote(setup))
		r := New(&SSHExecutor{Host: host, Dir: dir, NoMultiplex: true})

		// 1. Ordinary RebaseOnto with autostash.
		must(t, r.RebaseOnto("upstream", RebaseOntoOptions{Stash: true, UpdateRefs: false}))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" log --format=%s --reverse")); got != "c1\nupstream\nc2\nc3" {
			t.Fatalf("commits after RebaseOnto = %q", got)
		}
		if got := sshOut(t, "cat "+ShellQuote(dir+"/f1.txt")); got != "dirty\n" {
			t.Fatalf("dirty f1.txt after RebaseOnto = %q, want dirty", got)
		}
		if got := sshOut(t, "cat "+ShellQuote(dir+"/untracked.txt")); got != "untracked\n" {
			t.Fatalf("untracked.txt after RebaseOnto = %q", got)
		}

		// Clean dirty files before interactive rebase check.
		sshOut(t, "cd "+ShellQuote(dir)+" && git checkout -- f1.txt && rm -f untracked.txt")

		// 2. RebasePlan + interactive RebaseWithOptions (Backup=true, G05 PreparedMessage on fold chain).
		upstreamHash := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" rev-parse upstream"))
		plan, err := r.RebasePlan(upstreamHash)
		must(t, err)
		if len(plan) != 2 || plan[0].Subject != "c2" || plan[1].Subject != "c3" {
			t.Fatalf("RebasePlan(upstream) = %+v, want [c2, c3]", plan)
		}
		plan[1].Action = "fixup"
		plan[1].Subject = ""
		wantMsg := "Folded c2+c3 over SSH\n\nFull body preserved"
		plan[1].PreparedMessage = wantMsg
		must(t, r.RebaseWithOptions(upstreamHash, plan, RebaseOptions{Backup: true, UpdateRefs: false}))

		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" log -1 --format=%B HEAD")); got != wantMsg {
			t.Fatalf("HEAD message after interactive rebase = %q, want %q", got, wantMsg)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" for-each-ref --format='%(refname)' refs/backup")); !strings.HasPrefix(got, "refs/backup/main-") {
			t.Fatalf("backup ref = %q, want refs/backup/main-*", got)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dir)+" status --porcelain=v1")); got != "" {
			t.Fatalf("status after interactive rebase = %q, want clean", got)
		}
	})

	t.Run("UpdateRefsCapabilityBoundary", func(t *testing.T) {
		setupStack := func(dir string, dirty bool) {
			t.Helper()
			script := `set -eu
cd ` + ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'SSH Tester'
git config user.email ssh@example.invalid
printf 'base\n' > a.txt && git add a.txt && git commit -q -m base
git checkout -qb feature
printf 'f1\n' > f1.txt && git add f1.txt && git commit -q -m f1
git branch mid
printf 'f2\n' > f2.txt && git add f2.txt && git commit -q -m f2
git checkout -q main
printf 'main2\n' > m.txt && git add m.txt && git commit -q -m main2
git checkout -q feature
`
			if dirty {
				script += `printf 'unstaged\n' > a.txt
printf 'untracked\n' > untracked.txt
`
			}
			sshOut(t, "/bin/sh -c "+ShellQuote(script))
		}

		if !caps.RebaseUpdateRefs {
			dir := mkRemoteCaseDir(t, "updaterefs-reject")
			setupStack(dir, true)
			sshOut(t, "cd "+ShellQuote(dir)+" && printf 'staged\n' > staged.txt && git add staged.txt")
			r := New(&SSHExecutor{Host: host, Dir: dir, NoMultiplex: true})
			plan, err := r.RebasePlan("main")
			must(t, err)
			plan[0].PreparedMessage = "Should not write object"

			snapshotCmd := "cd " + ShellQuote(dir) + " && git rev-parse HEAD && git for-each-ref --format='%(refname) %(objectname)' && git status --porcelain=v1 && git diff && git diff --cached && git stash list && git cat-file --batch-check='%(objectname)' --batch-all-objects"
			before := sshOut(t, snapshotCmd)

			if err := r.RebaseOnto("main", RebaseOntoOptions{Stash: true, UpdateRefs: true}); err == nil || !strings.Contains(err.Error(), "2.38") {
				t.Fatalf("RebaseOnto(UpdateRefs: true) on Git %+v = %v, want error mentioning 2.38", caps.Version, err)
			}
			if err := r.RebaseWithOptions("main", plan, RebaseOptions{Backup: true, UpdateRefs: true}); err == nil || !strings.Contains(err.Error(), "2.38") {
				t.Fatalf("RebaseWithOptions(Backup: true, UpdateRefs: true) on Git %+v = %v, want error mentioning 2.38", caps.Version, err)
			}

			after := sshOut(t, snapshotCmd)
			if after != before {
				t.Fatalf("UpdateRefs rejection mutated remote state:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			sshOut(t, "test ! -e "+ShellQuote(dir+"/.git/rebase-merge")+" && test ! -e "+ShellQuote(dir+"/.git/rebase-apply"))
			return
		}

		// Modern Git (>= 2.38): verify RebaseOnto(UpdateRefs: true) and RebaseWithOptions(UpdateRefs: true) succeed on clean independent fixtures.
		dirOnto := mkRemoteCaseDir(t, "updaterefs-onto")
		setupStack(dirOnto, true)
		rOnto := New(&SSHExecutor{Host: host, Dir: dirOnto, NoMultiplex: true})
		oldMid := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirOnto)+" rev-parse refs/heads/mid"))
		must(t, rOnto.RebaseOnto("main", RebaseOntoOptions{Stash: true, UpdateRefs: true}))
		newMid := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirOnto)+" rev-parse refs/heads/mid"))
		newFeatureParent := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirOnto)+" rev-parse feature~1"))
		if newMid == oldMid || newMid != newFeatureParent {
			t.Fatalf("RebaseOnto(UpdateRefs: true) mid = %q (old %q), want feature~1 %q", newMid, oldMid, newFeatureParent)
		}
		sshOut(t, "git -C "+ShellQuote(dirOnto)+" merge-base --is-ancestor main refs/heads/mid")
		if got := sshOut(t, "cat "+ShellQuote(dirOnto+"/a.txt")); got != "unstaged\n" {
			t.Fatalf("dirty a.txt after RebaseOnto(UpdateRefs: true) = %q, want unstaged", got)
		}
		if got := sshOut(t, "cat "+ShellQuote(dirOnto+"/untracked.txt")); got != "untracked\n" {
			t.Fatalf("untracked.txt after RebaseOnto(UpdateRefs: true) = %q, want untracked", got)
		}

		dirInteractive := mkRemoteCaseDir(t, "updaterefs-interactive")
		setupStack(dirInteractive, false)
		rInteractive := New(&SSHExecutor{Host: host, Dir: dirInteractive, NoMultiplex: true})
		oldFeature := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirInteractive)+" rev-parse feature"))
		plan, err := rInteractive.RebasePlan("main")
		must(t, err)
		if len(plan) != 2 || plan[0].Subject != "f1" || plan[1].Subject != "f2" {
			t.Fatalf("RebasePlan(main) = %+v, want [f1, f2]", plan)
		}
		plan[0].Action = "reword"
		wantMidMsg := "f1 reworded with update-refs\n\nBody preserved"
		plan[0].PreparedMessage = wantMidMsg
		must(t, rInteractive.RebaseWithOptions("main", plan, RebaseOptions{Backup: true, UpdateRefs: true}))

		rebasedMid := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirInteractive)+" rev-parse refs/heads/mid"))
		rebasedParent := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirInteractive)+" rev-parse HEAD~1"))
		if rebasedMid != rebasedParent {
			t.Fatalf("RebaseWithOptions(UpdateRefs: true) mid = %q, want HEAD~1 %q", rebasedMid, rebasedParent)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirInteractive)+" log -1 --format=%B refs/heads/mid")); got != wantMidMsg {
			t.Fatalf("mid commit message = %q, want %q", got, wantMidMsg)
		}
		backupLine := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirInteractive)+" for-each-ref --format='%(refname) %(objectname)' refs/backup"))
		if !strings.HasPrefix(backupLine, "refs/backup/feature-") || !strings.HasSuffix(backupLine, " "+oldFeature) {
			t.Fatalf("backup ref = %q, want refs/backup/feature-* -> %s", backupLine, oldFeature)
		}
	})

	t.Run("StashAndSaveSnapshot", func(t *testing.T) {
		setupMixedTree := func(dir string) {
			t.Helper()
			setup := `set -eu
cd ` + ShellQuote(dir) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'SSH Tester'
git config user.email ssh@example.invalid
printf 'base a\n' > a.txt
printf 'base both\n' > both.txt
git add a.txt both.txt
git commit -q -m base
printf 'staged only\n' > staged.txt
printf 'both index v1\n' > both.txt
git add staged.txt both.txt
printf 'both worktree v2\n' > both.txt
printf 'unstaged a\n' > a.txt
printf 'untracked u\n' > untracked.txt
`
			sshOut(t, "/bin/sh -c "+ShellQuote(setup))
		}

		if !caps.StashStaged {
			dir := mkRemoteCaseDir(t, "stash-reject")
			setupMixedTree(dir)
			r := New(&SSHExecutor{Host: host, Dir: dir, NoMultiplex: true})

			snapshotCmd := "cd " + ShellQuote(dir) + " && git rev-parse HEAD && git for-each-ref --format='%(refname) %(objectname)' && git status --porcelain=v1 && git diff && git diff --cached && git stash list && cat untracked.txt"
			before := sshOut(t, snapshotCmd)

			if err := r.StashPushWith(StashOptions{Message: "staged on old git", Staged: true}); err == nil || !strings.Contains(err.Error(), "2.35") {
				t.Fatalf("StashPushWith(Staged: true) on Git %+v = %v, want error mentioning 2.35", caps.Version, err)
			}
			if err := r.SaveSnapshot("snap-branch", "snapshot on old git"); err == nil || !strings.Contains(err.Error(), "2.35") {
				t.Fatalf("SaveSnapshot on Git %+v = %v, want error mentioning 2.35", caps.Version, err)
			}

			after := sshOut(t, snapshotCmd)
			if after != before {
				t.Fatalf("staged stash / SaveSnapshot rejection mutated remote state:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		} else {
			// Modern Git (>= 2.35): verify StashPushWith(Staged: true) and SaveSnapshot preserve unstaged/untracked boundaries.
			dirStaged := mkRemoteCaseDir(t, "stash-staged-modern")
			setupStaged := `set -eu
cd ` + ShellQuote(dirStaged) + `
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'SSH Tester'
git config user.email ssh@example.invalid
printf 'base a\n' > a.txt
git add a.txt
git commit -q -m base
printf 'staged v2\n' > staged.txt
git add staged.txt
printf 'unstaged a\n' > a.txt
printf 'untracked u\n' > untracked.txt
`
			sshOut(t, "/bin/sh -c "+ShellQuote(setupStaged))
			rStaged := New(&SSHExecutor{Host: host, Dir: dirStaged, NoMultiplex: true})
			must(t, rStaged.StashPushWith(StashOptions{Message: "staged only modern", Staged: true}))
			if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirStaged)+" diff --cached --name-only")); got != "" {
				t.Fatalf("staged diff after StashPushWith(Staged: true) = %q, want empty", got)
			}
			sshOut(t, "test ! -e "+ShellQuote(dirStaged+"/staged.txt"))
			if got := sshOut(t, "cat "+ShellQuote(dirStaged+"/a.txt")); got != "unstaged a\n" {
				t.Fatalf("unstaged a.txt after StashPushWith(Staged: true) = %q, want unstaged a", got)
			}
			if got := sshOut(t, "cat "+ShellQuote(dirStaged+"/untracked.txt")); got != "untracked u\n" {
				t.Fatalf("untracked.txt after StashPushWith(Staged: true) = %q, want untracked u", got)
			}
			if got := sshOut(t, "git -C "+ShellQuote(dirStaged)+" show stash@{0}:staged.txt"); got != "staged v2\n" {
				t.Fatalf("stash@{0}:staged.txt = %q, want staged v2", got)
			}

			dirSnap := mkRemoteCaseDir(t, "stash-snapshot-modern")
			sshOut(t, "/bin/sh -c "+ShellQuote(strings.ReplaceAll(setupStaged, ShellQuote(dirStaged), ShellQuote(dirSnap))))
			rSnap := New(&SSHExecutor{Host: host, Dir: dirSnap, NoMultiplex: true})
			mainHeadBefore := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirSnap)+" rev-parse HEAD"))
			must(t, rSnap.SaveSnapshot("snap-branch", "snapshot commit on modern git"))
			if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirSnap)+" symbolic-ref HEAD")); got != "refs/heads/main" {
				t.Fatalf("HEAD branch after SaveSnapshot = %q, want refs/heads/main", got)
			}
			if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirSnap)+" rev-parse HEAD")); got != mainHeadBefore {
				t.Fatalf("main HEAD commit after SaveSnapshot = %q, want %q", got, mainHeadBefore)
			}
			if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirSnap)+" log -1 --format=%s refs/heads/snap-branch")); got != "snapshot commit on modern git" {
				t.Fatalf("snap-branch subject = %q, want snapshot commit on modern git", got)
			}
			if got := sshOut(t, "git -C "+ShellQuote(dirSnap)+" show refs/heads/snap-branch:staged.txt"); got != "staged v2\n" {
				t.Fatalf("snap-branch:staged.txt = %q, want staged v2", got)
			}
			if got := sshOut(t, "git -C "+ShellQuote(dirSnap)+" show refs/heads/snap-branch:a.txt"); got != "base a\n" {
				t.Fatalf("snap-branch:a.txt = %q, want base a (unstaged excluded)", got)
			}
			if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirSnap)+" diff --cached --name-only")); got != "" {
				t.Fatalf("staged diff after SaveSnapshot = %q, want empty", got)
			}
			if got := sshOut(t, "cat "+ShellQuote(dirSnap+"/a.txt")); got != "unstaged a\n" {
				t.Fatalf("unstaged a.txt after SaveSnapshot = %q, want unstaged a", got)
			}
			if got := sshOut(t, "cat "+ShellQuote(dirSnap+"/untracked.txt")); got != "untracked u\n" {
				t.Fatalf("untracked.txt after SaveSnapshot = %q, want untracked u", got)
			}
		}

		// Regular stash operations must succeed on both E1 (modern) and E2 (2.25).
		dirRegular := mkRemoteCaseDir(t, "stash-regular")
		setupMixedTree(dirRegular)
		rRegular := New(&SSHExecutor{Host: host, Dir: dirRegular, NoMultiplex: true})
		must(t, rRegular.StashSnapshot(StashOptions{Message: "ssh snapshot create store", StageNew: false}))
		stashes, err := rRegular.Stashes()
		must(t, err)
		if len(stashes) != 1 || stashes[0].Message != "ssh snapshot create store" {
			t.Fatalf("Stashes after StashSnapshot = %+v", stashes)
		}
		must(t, rRegular.StashDrop(stashes[0].Name))

		must(t, rRegular.StashPushWith(StashOptions{Message: "ssh full stash", Untracked: true}))
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirRegular)+" status --porcelain=v1")); got != "" {
			t.Fatalf("remote status after StashPushWith(Untracked: true) = %q, want empty", got)
		}
		stashes, err = rRegular.Stashes()
		must(t, err)
		if len(stashes) != 1 || !strings.Contains(stashes[0].Message, "ssh full stash") {
			t.Fatalf("Stashes = %+v", stashes)
		}
		must(t, rRegular.StashRename(stashes[0].Name, "ssh renamed stash"))
		stashes, err = rRegular.Stashes()
		must(t, err)
		if len(stashes) != 1 || !strings.Contains(stashes[0].Message, "ssh renamed stash") {
			t.Fatalf("Stashes after StashRename = %+v", stashes)
		}
		must(t, rRegular.StashApply(stashes[0].Name, true))
		if got := sshOut(t, "cat "+ShellQuote(dirRegular+"/untracked.txt")); got != "untracked u\n" {
			t.Fatalf("restored untracked.txt = %q", got)
		}
		if got := strings.TrimSpace(sshOut(t, "git -C "+ShellQuote(dirRegular)+" stash list")); got != "" {
			t.Fatalf("stash list after pop = %q, want empty", got)
		}
	})
}
