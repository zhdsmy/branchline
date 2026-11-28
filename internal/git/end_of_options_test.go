package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A reference or remote name starting with "-" must never become
// command-line options of the git commands the name is handed to. The
// tests build such names through the ref store — which accepts names git
// branch and git tag refuse to create — and exercise the operations the
// names reach. Marker scripts only touch files inside the test's own
// temporary directory, and every remote is a bare repository on disk.

// markerScript writes a script that touches path when it runs.
func markerScript(t *testing.T, path, scriptPath string) {
	t.Helper()
	body := "#!/bin/sh\ntouch " + ShellQuote(path) + "\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func endOfOptionsRepo(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	bare = filepath.Join(t.TempDir(), "srv.git")
	gitRun(t, dir, "init", "-q", "--bare", bare)
	gitRun(t, dir, "remote", "add", "srv", bare)
	return dir, bare
}

// TestEndOfOptionsDeleteTagsBatch covers the batch deletion of tags:
// git tag -d must read a name starting with "-" as the tag, not as flags.
func TestEndOfOptionsDeleteTagsBatch(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	head := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "refs/tags/--evil", head)

	r := New(&LocalExecutor{Dir: dir})
	if err := r.DeleteTags([]string{"--evil"}, nil); err != nil {
		t.Fatalf("DeleteTags: %v", err)
	}
	if out := gitOut(t, dir, "tag", "--list"); strings.Contains(out, "--evil") {
		t.Fatalf("tag survived the deletion: %q", out)
	}
}

// TestEndOfOptionsPushDashRemote pushes a reference to a remote whose
// name starts with "-": the name must stay the remote, not become flags.
func TestEndOfOptionsPushDashRemote(t *testing.T) {
	dir, bare := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	if err := r.RemoteAdd("-rx", bare, false); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	if out := gitOut(t, dir, "remote"); !strings.Contains(out, "-rx") {
		t.Fatalf("remote -rx was not added: %q", out)
	}
	if err := r.PushRef("-rx", "refs/heads/main:refs/heads/pushed"); err != nil {
		t.Fatalf("PushRef: %v", err)
	}
	if out := gitOut(t, dir, "ls-remote", "--heads", bare); !strings.Contains(out, "pushed") {
		t.Fatalf("push did not reach the remote: %q", out)
	}
}

// TestEndOfOptionsFetchUpstream fast-forwards a branch whose upstream
// starts with "-". The fetch refspec upstream:refs/heads/name must stay a
// refspec; without the separator it becomes the value of the --upload-pack
// option, the program git then tries to run for the fetch.
func TestEndOfOptionsFetchUpstream(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	writeFile(t, dir, "a.txt", "two\n")
	gitRun(t, dir, "commit", "-q", "-am", "ahead")
	ahead := gitOut(t, dir, "rev-parse", "HEAD")
	base := gitOut(t, dir, "rev-parse", "HEAD~1")

	upstream := "--upload-pack=evil"
	gitRun(t, dir, "update-ref", "refs/heads/"+upstream, ahead)
	gitRun(t, dir, "update-ref", "refs/heads/mine", base)

	r := New(&LocalExecutor{Dir: dir})
	moved, err := r.FastForwardToUpstreams([]Branch{{Name: "mine", Upstream: upstream, Behind: 1}})
	if err != nil {
		t.Fatalf("FastForwardToUpstreams: %v", err)
	}
	if len(moved) != 1 || moved[0] != "mine" {
		t.Fatalf("moved = %v, want [mine]", moved)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/heads/mine"); got != ahead {
		t.Fatalf("refs/heads/mine = %q, want %q", got, ahead)
	}
}

// TestEndOfOptionsRemoteRenameRemove renames and removes a remote whose
// name starts with "-". Both must treat the name as the remote: without
// the separator git parses it as switches and the operation fails.
func TestEndOfOptionsRemoteRenameRemove(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	if err := r.RemoteAdd("-rx", bare_NoSuchRemote, false); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	if err := r.RemoteRename("-rx", "-ry"); err != nil {
		t.Fatalf("RemoteRename: %v", err)
	}
	if out := gitOut(t, dir, "remote"); !strings.Contains(out, "-ry") {
		t.Fatalf("remote -ry missing after rename: %q", out)
	}
	if err := r.RemoteRemove("-ry"); err != nil {
		t.Fatalf("RemoteRemove: %v", err)
	}
	if out := gitOut(t, dir, "remote"); strings.Contains(out, "-r") {
		t.Fatalf("remote survived the removal: %q", out)
	}
}

// bare_NoSuchRemote is a path no repository lives at; rename and remove
// never contact it.
const bare_NoSuchRemote = "nonexistent-remote-path"

// TestEndOfOptionsResetHelp checks that a revision looking like an option
// never prints the command's usage: git reset --hard --help shows it, the
// protected form treats --help as the revision and fails instead.
func TestEndOfOptionsResetHelp(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	err := r.Reset("--help", "hard")
	if err == nil {
		t.Fatal("Reset(--help) succeeded, want a revision failure")
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("Reset error is %T, want *Error", err)
	}
	if strings.Contains(ge.Stderr, "usage: git reset") || strings.Contains(string(ge.Stdout), "usage: git reset") {
		t.Fatalf("--help was read as the option:\n%s\n%s", ge.Stderr, ge.Stdout)
	}
	if !strings.Contains(ge.Stderr, "--help") {
		t.Fatalf("the failure does not name the revision: %q", ge.Stderr)
	}
}

// TestEndOfOptionsCheckoutHelp does the same for checkout.
func TestEndOfOptionsCheckoutHelp(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	err := r.Checkout("--help")
	if err == nil {
		t.Fatal("Checkout(--help) succeeded, want a revision failure")
	}
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("Checkout error is %T, want *Error", err)
	}
	if strings.Contains(ge.Stderr, "usage: git checkout") || strings.Contains(string(ge.Stdout), "GIT-CHECKOUT") {
		t.Fatalf("--help was read as the option:\n%s\n%s", ge.Stderr, ge.Stdout)
	}
	if head := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Fatalf("HEAD moved to %q", head)
	}
}

// TestEndOfOptionsMergeHelp does the same for merge --ff-only.
func TestEndOfOptionsMergeHelp(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	err := r.FastForward("--help")
	if err == nil {
		t.Fatal("FastForward(--help) succeeded, want a revision failure")
	}
	if strings.Contains(err.Error(), "usage: git merge") {
		t.Fatalf("--help was read as the option: %v", err)
	}
	if !strings.Contains(err.Error(), "--help") {
		t.Fatalf("the failure does not name the revision: %v", err)
	}
}

// TestEndOfOptionsRebaseHelp does the same for rebase.
func TestEndOfOptionsRebaseHelp(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	r := New(&LocalExecutor{Dir: dir})
	err := r.Rebase("--help")
	if err == nil {
		t.Fatal("Rebase(--help) succeeded, want a revision failure")
	}
	if strings.Contains(err.Error(), "usage: git rebase") || strings.Contains(err.Error(), "GIT-REBASE") {
		t.Fatalf("--help was read as the option: %v", err)
	}
	if !strings.Contains(err.Error(), "--help") {
		t.Fatalf("the failure does not name the revision: %v", err)
	}
}

// TestEndOfOptionsCheckoutDashRev checks checkout of "-" keeps meaning the
// previous branch: the separator must not take away a valid revision that
// starts with "-".
func TestEndOfOptionsCheckoutDashRev(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	gitRun(t, dir, "checkout", "-q", "-b", "other")
	gitRun(t, dir, "checkout", "-q", "main")

	r := New(&LocalExecutor{Dir: dir})
	if err := r.Checkout("-"); err != nil {
		t.Fatalf("Checkout(-): %v", err)
	}
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "other" {
		t.Fatalf("HEAD is %q, want other", got)
	}
}

// TestEndOfOptionsBranchOps covers renaming and deleting a branch whose
// name starts with "-", which the ref store accepts even though git
// branch refuses to create it itself.
func TestEndOfOptionsBranchOps(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	head := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "update-ref", "refs/heads/-x", head)

	r := New(&LocalExecutor{Dir: dir})
	// git branch itself refuses to create a misnamed branch, so the rename
	// target is a valid name: the check is that the source starting with
	// "-" is read as the branch, not as switches.
	if err := r.RenameBranch("-x", "renamed"); err != nil {
		t.Fatalf("RenameBranch: %v", err)
	}
	if !hasRef(dir, "refs/heads/renamed") || hasRef(dir, "refs/heads/-x") {
		t.Fatal("the branch was not renamed as a name")
	}
	gitRun(t, dir, "update-ref", "refs/heads/-z", head)
	if err := r.DeleteBranch("-z", true); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if hasRef(dir, "refs/heads/-z") {
		t.Fatal("the branch survived the deletion")
	}
}

// TestEndOfOptionsExternalDiff checks WorktreeDiff and BranchDiff never
// run the repository's configured external diff command, while the same
// git diff without the protection does.
func TestEndOfOptionsExternalDiff(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	writeFile(t, dir, "a.txt", "two\n")

	marker := filepath.Join(t.TempDir(), "external-ran")
	script := filepath.Join(t.TempDir(), "external-diff.sh")
	markerScript(t, marker, script)
	gitRun(t, dir, "config", "diff.external", script)

	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.WorktreeDiff("HEAD"); err != nil {
		t.Fatalf("WorktreeDiff: %v", err)
	}
	if _, err := r.BranchDiff("HEAD", "HEAD"); err != nil {
		t.Fatalf("BranchDiff: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the external diff command ran: %s exists", marker)
	}

	// The same diff without --no-ext-diff runs the configured command:
	// the comparison proves the protection is what keeps it out.
	_, err := r.run(quick, "diff", "HEAD", "--")
	if err != nil {
		t.Fatalf("unprotected git diff: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("control: the unprotected diff did not run the external command: %v", statErr)
	}
}

// TestEndOfOptionsPullRejectsDashNames covers pull, which forwards its
// positional arguments to the git fetch it spawns after stripping any
// separator: no marker can protect them there, so a remote or branch
// starting with "-" is refused before git runs at all.
func TestEndOfOptionsPullRejectsDashNames(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)
	gitRun(t, dir, "push", "-q", "srv", "main")
	gitRun(t, dir, "branch", "--set-upstream-to", "srv/main", "main")

	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "upload-pack.sh")
	markerScript(t, marker, script)
	evil := "--upload-pack=" + script

	log := NewCommandLog()
	r := New(&LocalExecutor{Dir: dir}).WithCommandLog(log)

	for _, o := range []PullOptions{
		{Remote: evil},
		{Remote: "srv", Branch: evil},
	} {
		if err := r.PullOpts(o); err == nil {
			t.Fatalf("PullOpts(%+v) succeeded, want a refusal", o)
		} else if !strings.Contains(err.Error(), "is not usable") {
			t.Fatalf("PullOpts(%+v) error = %v, want the unusable-name refusal", o, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the upload-pack script ran: %s exists", marker)
	}
	recs, _ := log.Snapshot()
	for _, rec := range recs {
		if len(rec.Args) > 0 && rec.Args[0] == "pull" {
			t.Fatalf("git pull ran despite the refusal: %+v", rec)
		}
	}
}

// TestEndOfOptionsLFSRejectsDashRemote covers the Git LFS commands, which
// pass the remote to a program of their own that a separator here cannot
// guard: a remote starting with "-" is refused before git-lfs runs. The
// executor only records what it is asked to run — git-lfs does not have
// to be installed for the check — so a recorded invocation means the
// refusal did not happen.
func TestEndOfOptionsLFSRejectsDashRemote(t *testing.T) {
	ex := &argsRecorder{}
	r := New(ex)

	if err := r.LFSFetch("-rx"); err == nil {
		t.Fatal("LFSFetch(-rx) succeeded, want a refusal")
	} else if !strings.Contains(err.Error(), "is not usable") {
		t.Fatalf("LFSFetch error = %v, want the unusable-name refusal", err)
	}
	if ex.args != nil {
		t.Fatalf("git lfs fetch ran despite the refusal: %q", ex.args)
	}
	if err := r.LFSPull("-rx"); err == nil {
		t.Fatal("LFSPull(-rx) succeeded, want a refusal")
	} else if !strings.Contains(err.Error(), "is not usable") {
		t.Fatalf("LFSPull error = %v, want the unusable-name refusal", err)
	}
	if ex.args != nil {
		t.Fatalf("git lfs pull ran despite the refusal: %q", ex.args)
	}

	// Ordinary remotes are not refused and keep the exact argument list.
	if err := r.LFSFetch("srv"); err != nil {
		t.Fatalf("LFSFetch(srv): %v", err)
	}
	if want := []string{"lfs", "fetch", "--all", "srv"}; !slices.Equal(ex.args, want) {
		t.Fatalf("lfs fetch args %q, want %q", ex.args, want)
	}
	if err := r.LFSPull("srv"); err != nil {
		t.Fatalf("LFSPull(srv): %v", err)
	}
	if want := []string{"lfs", "pull", "srv"}; !slices.Equal(ex.args, want) {
		t.Fatalf("lfs pull args %q, want %q", ex.args, want)
	}
}

// argsRecorder is an Executor that only records the arguments it gets.
type argsRecorder struct{ args []string }

func (e *argsRecorder) Describe() string { return "test repository" }
func (e *argsRecorder) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	e.args = append([]string(nil), args...)
	return nil, nil
}

// TestEndOfOptionsNormalArgsUnchanged pins the conditional insertion:
// with ordinary names the commands keep the exact argument list they had
// before the hardening, so nothing a user does every day changes.
func TestEndOfOptionsNormalArgsUnchanged(t *testing.T) {
	ex := &argsRecorder{}
	r := New(ex)

	_ = r.Checkout("main")
	if want := []string{"checkout", "main", "--"}; !slices.Equal(ex.args, want) {
		t.Fatalf("checkout args %q, want %q", ex.args, want)
	}

	_ = r.DeleteBranch("feature", true)
	if want := []string{"branch", "-D", "feature"}; !slices.Equal(ex.args, want) {
		t.Fatalf("branch args %q, want %q", ex.args, want)
	}

	_ = r.Rebase("main")
	if want := []string{"rebase", "main"}; !slices.Equal(ex.args, want) {
		t.Fatalf("rebase args %q, want %q", ex.args, want)
	}

	_ = r.RemoteRemove("origin")
	if want := []string{"remote", "remove", "origin"}; !slices.Equal(ex.args, want) {
		t.Fatalf("remote args %q, want %q", ex.args, want)
	}
}

// TestEndOfOptionsInsertedArgs pins where the separators land when a name
// does start with "-": after the command's own options, before the first
// positional argument.
func TestEndOfOptionsInsertedArgs(t *testing.T) {
	ex := &argsRecorder{}
	r := New(ex)

	_ = r.Checkout("-x")
	if want := []string{"checkout", "--end-of-options", "-x", "--"}; !slices.Equal(ex.args, want) {
		t.Fatalf("checkout args %q, want %q", ex.args, want)
	}

	_ = r.DeleteBranch("-x", true)
	if want := []string{"branch", "-D", "--", "-x"}; !slices.Equal(ex.args, want) {
		t.Fatalf("branch args %q, want %q", ex.args, want)
	}

	_ = r.Rebase("-x")
	if want := []string{"rebase", "--", "-x"}; !slices.Equal(ex.args, want) {
		t.Fatalf("rebase args %q, want %q", ex.args, want)
	}

	_ = r.RemoteRemove("-x")
	if want := []string{"remote", "remove", "--", "-x"}; !slices.Equal(ex.args, want) {
		t.Fatalf("remote args %q, want %q", ex.args, want)
	}

	_ = r.Reset("-x", "hard")
	if want := []string{"reset", "--hard", "--end-of-options", "-x", "--"}; !slices.Equal(ex.args, want) {
		t.Fatalf("reset args %q, want %q", ex.args, want)
	}

	// cherry-pick and revert read a commit behind "--" as a pathspec and
	// print the usage, so they get --end-of-options instead.
	_ = r.CherryPick("-x")
	if want := []string{"cherry-pick", "--end-of-options", "-x"}; !slices.Equal(ex.args, want) {
		t.Fatalf("cherry-pick args %q, want %q", ex.args, want)
	}

	// git stash show reads a name behind "--" as a pathspec, so the stash
	// entry goes behind --end-of-options after the diff flags.
	_, _ = r.StashPatch("-x")
	want := []string{"stash", "show", "-p", "--no-ext-diff", "--no-textconv", "--include-untracked", "--end-of-options", "-x"}
	if !slices.Equal(ex.args, want) {
		t.Fatalf("stash show args %q, want %q", ex.args, want)
	}
}
