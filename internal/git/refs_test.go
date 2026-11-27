package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// remoteRepo makes a repository with one commit on main, pushed to a
// bare origin it tracks.
func remoteRepo(t *testing.T) (dir, bare string, r *Repo) {
	t.Helper()
	dir = initRepo(t)
	bare = filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main")
	return dir, bare, New(&LocalExecutor{Dir: dir})
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func hasRef(dir, ref string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

func TestPushBranchesAndCustomDestination(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	gitRun(t, dir, "branch", "one")
	gitRun(t, dir, "branch", "two")
	if err := r.PushBranches("origin", []PushSpec{{Local: "one"}, {Local: "two", Remote: "renamed"}}, true, false); err != nil {
		t.Fatal(err)
	}
	if !hasRef(bare, "refs/heads/one") || !hasRef(bare, "refs/heads/renamed") || hasRef(bare, "refs/heads/two") {
		t.Fatalf("remote branches: %s", gitOut(t, bare, "branch"))
	}
	if up := gitOut(t, dir, "rev-parse", "--abbrev-ref", "two@{upstream}"); up != "origin/renamed" {
		t.Fatalf("two tracks %q", up)
	}
	if err := r.PushBranches("origin", nil, false, false); err == nil {
		t.Fatal("pushing nothing should fail")
	}

	if err := r.PushTo("origin", "main", "refs/heads/review/main", false, false, false); err != nil {
		t.Fatal(err)
	}
	if !hasRef(bare, "refs/heads/review/main") {
		t.Fatalf("custom destination missing: %s", gitOut(t, bare, "branch"))
	}
}

func TestRemoveBranchWithRemoteAndWorktree(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	gitRun(t, dir, "push", "-q", "-u", "origin", "main:topic")
	gitRun(t, dir, "branch", "topic", "--track", "origin/topic")
	wt := filepath.Join(t.TempDir(), "topic")
	gitRun(t, dir, "worktree", "add", "-q", wt, "topic")

	// Without the worktree, git keeps the branch it has checked out.
	if err := r.RemoveBranch(RemoveBranchOptions{Name: "topic", Force: true}); err == nil {
		t.Fatal("deleted a branch a worktree has checked out")
	}
	if err := r.RemoveBranch(RemoveBranchOptions{Name: "topic", Force: true, Upstream: "origin/topic", Worktree: wt}); err != nil {
		t.Fatal(err)
	}
	if hasRef(dir, "refs/heads/topic") || hasRef(bare, "refs/heads/topic") {
		t.Fatal("topic is still here or on origin")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
}

func TestRenameRemoteBranch(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	gitRun(t, dir, "checkout", "-q", "-b", "old")
	gitRun(t, dir, "push", "-q", "-u", "origin", "old")
	if err := r.RenameBranch("old", "new"); err != nil {
		t.Fatal(err)
	}
	if err := r.RenameRemoteBranch("new", "origin/old", "new"); err != nil {
		t.Fatal(err)
	}
	if hasRef(bare, "refs/heads/old") || !hasRef(bare, "refs/heads/new") {
		t.Fatalf("remote branches: %s", gitOut(t, bare, "branch"))
	}
	if up := gitOut(t, dir, "rev-parse", "--abbrev-ref", "new@{upstream}"); up != "origin/new" {
		t.Fatalf("new tracks %q", up)
	}
}

func TestCheckoutRemoteAsAndWorktree(t *testing.T) {
	dir, _, r := remoteRepo(t)
	gitRun(t, dir, "push", "-q", "origin", "main:feature")
	gitRun(t, dir, "push", "-q", "origin", "main:wt")
	gitRun(t, dir, "fetch", "-q", "origin")

	if err := r.CheckoutRemoteAs("origin/feature", "mine", false); err != nil {
		t.Fatal(err)
	}
	if cur := gitOut(t, dir, "branch", "--show-current"); cur != "mine" {
		t.Fatalf("on %q", cur)
	}
	if up := gitOut(t, dir, "rev-parse", "--abbrev-ref", "mine@{upstream}"); up != "origin/feature" {
		t.Fatalf("mine tracks %q", up)
	}

	path := filepath.Join(t.TempDir(), "wt")
	if err := r.WorktreeAddRemote(path, "origin/wt", "wt"); err != nil {
		t.Fatal(err)
	}
	if hasRef(dir, "refs/heads/origin/wt") {
		t.Fatal("made a local branch named origin/wt")
	}
	if cur := gitOut(t, path, "branch", "--show-current"); cur != "wt" {
		t.Fatalf("worktree on %q", cur)
	}
	if up := gitOut(t, dir, "rev-parse", "--abbrev-ref", "wt@{upstream}"); up != "origin/wt" {
		t.Fatalf("wt tracks %q", up)
	}
}

func TestTagsOnRemotes(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	other := filepath.Join(t.TempDir(), "other.git")
	gitRun(t, dir, "init", "-q", "--bare", other)
	gitRun(t, dir, "remote", "add", "other", other)
	gitRun(t, dir, "tag", "-a", "v1", "-m", "Release one\n\nNotes.")
	gitRun(t, dir, "tag", "light")

	if err := r.PushRefs("origin", []string{"refs/tags/v1", "refs/tags/light"}); err != nil {
		t.Fatal(err)
	}
	if !hasRef(bare, "refs/tags/v1") || !hasRef(bare, "refs/tags/light") {
		t.Fatal("tags not pushed")
	}

	d, err := r.TagInfo("v1")
	if err != nil || !d.Annotated || d.Tagger != "Ada" || d.TaggerEmail != "ada@example.com" || d.Date.IsZero() || d.Message != "Release one\n\nNotes." {
		t.Fatalf("v1 %+v %v", d, err)
	}
	if head := gitOut(t, dir, "rev-parse", "HEAD"); d.Target != head {
		t.Fatalf("v1 names %s, HEAD is %s", d.Target, head)
	}
	d, err = r.TagInfo("light")
	if err != nil || d.Annotated || d.Target == "" || d.Tagger != "" {
		t.Fatalf("light %+v %v", d, err)
	}
	if _, err := r.TagInfo("missing"); err == nil {
		t.Fatal("a missing tag has details")
	}

	// other never had v1: it is skipped, origin loses it.
	if err := r.DeleteTag("v1"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteTagFromRemotes([]string{"origin", "other"}, "v1"); err != nil {
		t.Fatal(err)
	}
	if hasRef(bare, "refs/tags/v1") || !hasRef(bare, "refs/tags/light") {
		t.Fatalf("origin tags: %s", gitOut(t, bare, "tag"))
	}
}

func TestEditAndTestRemote(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	moved := filepath.Join(t.TempDir(), "moved.git")
	if err := os.Rename(bare, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := r.TestRemote(bare); err == nil {
		t.Fatal("the old URL answered")
	}
	n, err := r.TestRemote(moved)
	if err != nil || n != 1 {
		t.Fatalf("test %d %v", n, err)
	}
	if err := r.EditRemote("origin", "upstream", moved); err != nil {
		t.Fatal(err)
	}
	if url := gitOut(t, dir, "remote", "get-url", "upstream"); url != moved {
		t.Fatalf("url %q", url)
	}
	if remotes := gitOut(t, dir, "remote"); remotes != "upstream" {
		t.Fatalf("remotes %q", remotes)
	}
}

func TestResetKeep(t *testing.T) {
	dir, _, r := remoteRepo(t)
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "bee")
	writeFile(t, dir, "a.txt", "one\nlocal\n")
	// a.txt is the same in both commits, so keep moves and keeps the change.
	if err := r.Reset("HEAD~1", "keep"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(body) != "one\nlocal\n" || hasRef(dir, "HEAD~1") {
		t.Fatalf("after keep: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("b.txt stayed")
	}
	// b.txt differs between the commits: keep stops rather than lose it.
	gitRun(t, dir, "reset", "-q", "--hard", "ORIG_HEAD")
	writeFile(t, dir, "b.txt", "changed\n")
	if err := r.Reset("HEAD~1", "keep"); err == nil {
		t.Fatal("keep dropped a local change")
	}
	if err := r.Reset("HEAD", "merge"); err == nil {
		t.Fatal("an unknown mode ran")
	}
}

func TestStashApplyWith(t *testing.T) {
	dir, _, r := remoteRepo(t)
	writeFile(t, dir, "a.txt", "one\nstaged\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "stash", "push", "-q", "-m", "wip")

	if err := r.StashApplyWith("stash@{0}", StashApplyOptions{Index: false}); err != nil {
		t.Fatal(err)
	}
	if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("without index, staged %q", staged)
	}
	if n := gitOut(t, dir, "stash", "list"); n == "" {
		t.Fatal("apply dropped the stash")
	}
	gitRun(t, dir, "checkout", "-q", "--", "a.txt")

	if err := r.StashApplyWith("stash@{0}", StashApplyOptions{Pop: true, Index: true}); err != nil {
		t.Fatal(err)
	}
	if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); staged != "a.txt" {
		t.Fatalf("with index, staged %q", staged)
	}
	if n := gitOut(t, dir, "stash", "list"); n != "" {
		t.Fatalf("pop kept %q", n)
	}
}

func TestStashApplyWithMultiSelectDropEntries(t *testing.T) {
	dir, _, r := remoteRepo(t)
	writeFile(t, dir, "keep.txt", "base keep\n")
	gitRun(t, dir, "add", "keep.txt")
	gitRun(t, dir, "commit", "-q", "-m", "add keep")

	for _, s := range []struct {
		body string
		msg  string
	}{
		{body: "one\ns1\n", msg: "s1 oldest"},
		{body: "one\ns2\n", msg: "s2 lower"},
		{body: "one\ns3\n", msg: "s3 upper"},
		{body: "one\ns4\n", msg: "s4 newest"},
	} {
		writeFile(t, dir, "a.txt", s.body)
		if err := r.StashPushWith(StashOptions{Message: s.msg}); err != nil {
			t.Fatalf("StashPushWith(%q): %v", s.msg, err)
		}
	}

	writeFile(t, dir, "a.txt", "working dirty a\n")
	writeFile(t, dir, "keep.txt", "staged keep\n")
	gitRun(t, dir, "add", "keep.txt")
	writeFile(t, dir, "untracked.txt", "untracked keep\n")
	beforeStatus := gitOut(t, dir, "status", "--porcelain")
	beforeDiff := gitOut(t, dir, "diff")
	beforeCached := gitOut(t, dir, "diff", "--cached")

	before, err := r.Stashes()
	if err != nil || len(before) != 4 {
		t.Fatalf("Stashes() = %+v, %v", before, err)
	}
	s4, s3, s2, s1 := before[0], before[1], before[2], before[3]

	// Missing entry in a multi-select batch must abort before dropping any target.
	missing := StashSelection{Hash: s1.Hash, Message: s1.Message, Occurrence: 1}
	if err := r.StashDropEntries([]StashSelection{selectionFrom(before, s4), missing}); err == nil || !strings.Contains(err.Error(), "no longer in the stash") {
		t.Fatalf("expected missing stash error, got %v", err)
	}
	if afterReject, err := r.Stashes(); err != nil || len(afterReject) != 4 {
		t.Fatalf("stashes after rejected batch = %+v, %v", afterReject, err)
	}

	// Non-adjacent multi-select deletion drops s4 (stash@{0}) and s2 (stash@{2}),
	// preserving s3 and s1 in order and leaving worktree and index untouched.
	log := NewCommandLog()
	if err := r.WithCommandLog(log).StashDropEntries([]StashSelection{
		selectionFrom(before, s4),
		selectionFrom(before, s2),
	}); err != nil {
		t.Fatalf("StashDropEntries: %v", err)
	}
	after, err := r.Stashes()
	if err != nil || len(after) != 2 || after[0].Hash != s3.Hash || after[1].Hash != s1.Hash {
		t.Fatalf("remaining stashes = %+v (%v), want [%s, %s]", after, err, s3.Hash, s1.Hash)
	}
	if gotRef := gitOut(t, dir, "rev-parse", "refs/stash"); gotRef != s3.Hash {
		t.Fatalf("refs/stash = %q, want %q", gotRef, s3.Hash)
	}
	if got := gitOut(t, dir, "status", "--porcelain"); got != beforeStatus {
		t.Fatalf("status = %q, want %q", got, beforeStatus)
	}
	if got := gitOut(t, dir, "diff"); got != beforeDiff || gitOut(t, dir, "diff", "--cached") != beforeCached {
		t.Fatalf("diff/cached changed after multi-select drop")
	}
	for name, want := range map[string]string{
		"a.txt":         "working dirty a\n",
		"keep.txt":      "staged keep\n",
		"untracked.txt": "untracked keep\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q (%v), want %q", name, got, readErr, want)
		}
	}
	recs, _ := log.Snapshot()
	if len(recs) != 3 || recs[1].Command != "$ git stash drop 'stash@{2}'" || recs[2].Command != "$ git stash drop 'stash@{0}'" {
		t.Fatalf("expected oldest-first drop commands, got %#v", recs)
	}
}

func TestStashApplyWithConflictPreservesStashAndRecoversWorktree(t *testing.T) {
	dir, _, r := remoteRepo(t)
	writeFile(t, dir, "keep.txt", "keep base\n")
	writeFile(t, dir, "staged.txt", "staged base\n")
	gitRun(t, dir, "add", "keep.txt", "staged.txt")
	gitRun(t, dir, "commit", "-q", "-m", "add keep and staged")

	// Create a stash that modifies a.txt.
	writeFile(t, dir, "a.txt", "one from stash\n")
	if err := r.StashPushWith(StashOptions{Message: "conflict stash"}); err != nil {
		t.Fatal(err)
	}
	stashes, err := r.Stashes()
	if err != nil || len(stashes) != 1 {
		t.Fatalf("Stashes() = %+v, %v", stashes, err)
	}
	stashCommit := stashes[0].Hash

	// Diverge HEAD on a.txt so applying or popping the stash produces an unmerged conflict.
	writeFile(t, dir, "a.txt", "one from head\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "head divergent")
	headCommit := gitOut(t, dir, "rev-parse", "HEAD")

	// Prepare unrelated dirty worktree, staged index, and untracked files.
	writeFile(t, dir, "keep.txt", "keep dirty worktree\n")
	writeFile(t, dir, "staged.txt", "staged local index\n")
	gitRun(t, dir, "add", "staged.txt")
	writeFile(t, dir, "untracked.txt", "untracked local\n")

	// Pop with Index=true falls back when index cannot be restored and stops on conflict,
	// retaining the stash entry in refs/stash without dropping it.
	err = r.StashApplyEntryWith(selectionFrom(stashes, stashes[0]), StashApplyOptions{Pop: true, Index: true})
	if err == nil {
		t.Fatal("expected conflict error from StashApplyEntryWith(Pop: true)")
	}
	st, err := r.Status()
	if err != nil || !st.Conflicted() {
		t.Fatalf("expected conflicted status, got %+v (%v)", st, err)
	}
	if gotRef := gitOut(t, dir, "rev-parse", "refs/stash"); gotRef != stashCommit {
		t.Fatalf("refs/stash = %q, want %q", gotRef, stashCommit)
	}
	sides, err := r.ConflictSides("a.txt")
	if err != nil || !sides.HaveOurs || !sides.HaveTheirs || sides.Ours != "one from head\n" || sides.Theirs != "one from stash\n" {
		t.Fatalf("ConflictSides(a.txt) = %+v, %v", sides, err)
	}

	// Recover the conflicted path back to Ours (HEAD) and unstage it so the original worktree/index is restored.
	if err := r.TakeConflictSide("a.txt", "ours"); err != nil {
		t.Fatalf("TakeConflictSide(ours): %v", err)
	}
	if err := r.Unstage("a.txt"); err != nil {
		t.Fatalf("Unstage(a.txt): %v", err)
	}
	stAfter, err := r.Status()
	if err != nil || stAfter.Conflicted() || stAfter.Head != headCommit {
		t.Fatalf("status after conflict recovery = %+v, %v", stAfter, err)
	}
	if gotRef := gitOut(t, dir, "rev-parse", "refs/stash"); gotRef != stashCommit {
		t.Fatalf("refs/stash after recovery = %q, want %q", gotRef, stashCommit)
	}
	if gotCached := gitOut(t, dir, "diff", "--cached", "--name-only"); gotCached != "staged.txt" {
		t.Fatalf("staged files after recovery = %q, want staged.txt", gotCached)
	}
	if gotUnstaged := gitOut(t, dir, "diff", "--name-only"); gotUnstaged != "keep.txt" {
		t.Fatalf("unstaged files after recovery = %q, want keep.txt", gotUnstaged)
	}
	for name, want := range map[string]string{
		"a.txt":         "one from head\n",
		"keep.txt":      "keep dirty worktree\n",
		"staged.txt":    "staged local index\n",
		"untracked.txt": "untracked local\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q (%v), want %q", name, got, readErr, want)
		}
	}
}

func TestStashApplyWithConflictAbortRestoresOriginalWorktree(t *testing.T) {
	dir := syncConflictRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	// Seed an existing user stash that must survive the entire conflict + Abort cycle.
	writeFile(t, dir, "local.txt", "user stash body\n")
	if err := r.StashPushWith(StashOptions{Message: "existing user stash"}); err != nil {
		t.Fatal(err)
	}
	userStashHash := gitOut(t, dir, "rev-parse", "refs/stash")

	// Prepare local unstaged, staged, and untracked changes before starting a stashed operation that conflicts.
	writeFile(t, dir, "local.txt", "original unstaged work\n")
	writeFile(t, dir, "index.txt", "original staged index\n")
	gitRun(t, dir, "add", "index.txt")
	writeFile(t, dir, "untracked.txt", "original untracked work\n")

	beforeHead := gitOut(t, dir, "rev-parse", "HEAD")

	// CheckoutAndSync with Stash=true saves the worktree/index into syncStashRef and stops on merge conflict.
	if err := r.CheckoutAndSync("topic", "main", SyncMerge, true, false); err == nil {
		t.Fatal("expected merge conflict during CheckoutAndSync")
	}
	st, err := r.Status()
	if err != nil || st.State != "merge" || !st.Conflicted() {
		t.Fatalf("expected stopped merge conflict, got %+v (%v)", st, err)
	}
	syncHash := gitOut(t, dir, "rev-parse", syncStashRef)
	if syncHash == "" || syncHash == userStashHash {
		t.Fatalf("unexpected syncStashRef %q (userStash=%q)", syncHash, userStashHash)
	}

	// Abort stops the conflicted merge, restores the saved worktree and index via StashApplyWith(Index: true),
	// drops only the temporary sync stash entry, and preserves the user's pre-existing stash ref.
	log := NewCommandLog()
	if err := r.WithCommandLog(log).Abort("merge"); err != nil {
		t.Fatalf("Abort(merge): %v", err)
	}
	stAfter, err := r.Status()
	if err != nil || stAfter.State != "" || stAfter.Conflicted() || stAfter.Head != beforeHead {
		t.Fatalf("status after Abort = %+v (%v), want clean state at %s", stAfter, err, beforeHead)
	}
	if marker, err := r.syncStash(); err != nil || marker != "" {
		t.Fatalf("syncStash marker after Abort = %q (%v), want empty", marker, err)
	}
	stashesAfter, err := r.Stashes()
	if err != nil || len(stashesAfter) != 1 || stashesAfter[0].Hash != userStashHash {
		t.Fatalf("stashes after Abort = %+v (%v), want only [%s]", stashesAfter, err, userStashHash)
	}
	if gotRef := gitOut(t, dir, "rev-parse", "refs/stash"); gotRef != userStashHash {
		t.Fatalf("refs/stash after Abort = %q, want %q", gotRef, userStashHash)
	}
	if gotCached := gitOut(t, dir, "diff", "--cached", "--name-only"); gotCached != "index.txt" {
		t.Fatalf("staged index after Abort = %q, want index.txt", gotCached)
	}
	if gotUnstaged := gitOut(t, dir, "diff", "--name-only"); gotUnstaged != "local.txt" {
		t.Fatalf("unstaged diff after Abort = %q, want local.txt", gotUnstaged)
	}
	for name, want := range map[string]string{
		"a.txt":         "topic\n",
		"local.txt":     "original unstaged work\n",
		"index.txt":     "original staged index\n",
		"untracked.txt": "original untracked work\n",
	} {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q (%v), want %q", name, got, readErr, want)
		}
	}
}

func TestLeanBranching(t *testing.T) {
	dir, bare, r := remoteRepo(t)
	// Someone else pushes to main; Start Branch fetches it first.
	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, dir, "clone", "-q", bare, clone)
	writeFile(t, clone, "c.txt", "sea\n")
	gitRun(t, clone, "add", "c.txt")
	gitRun(t, clone, "-c", "user.name=Bo", "-c", "user.email=bo@example.com", "commit", "-q", "-m", "theirs")
	gitRun(t, clone, "push", "-q", "origin", "main")

	if err := r.StartBranch("topic", "origin/main", "origin", false); err != nil {
		t.Fatal(err)
	}
	if cur := gitOut(t, dir, "branch", "--show-current"); cur != "topic" {
		t.Fatalf("on %q", cur)
	}
	if hasRef(dir, "topic@{upstream}") {
		t.Fatal("topic tracks origin/main")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal("topic did not start from the fetched main")
	}
	writeFile(t, dir, "d.txt", "dee\n")
	gitRun(t, dir, "add", "d.txt")
	gitRun(t, dir, "commit", "-q", "-m", "topic work")

	if err := r.FinishBranch("main", "main", true); err == nil {
		t.Fatal("finished main into itself")
	}
	if err := r.FinishBranch("topic", "main", true); err != nil {
		t.Fatal(err)
	}
	if cur := gitOut(t, dir, "branch", "--show-current"); cur != "main" {
		t.Fatalf("on %q", cur)
	}
	if parents := strings.Fields(gitOut(t, dir, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("no merge commit: %v", parents)
	}
}

func TestPushAndPullSayWhenUpToDate(t *testing.T) {
	dir, _, r := remoteRepo(t)
	if note, err := r.PushNote("origin", "main", "", true, false, false); err != nil || note != UpToDatePush && note != "" {
		t.Fatalf("first push: %q %v", note, err)
	}
	note, err := r.PushNote("origin", "main", "", false, false, false)
	if err != nil || note != UpToDatePush {
		t.Fatalf("push with nothing new: %q %v", note, err)
	}
	writeFile(t, dir, "new.txt", "x\n")
	gitRun(t, dir, "add", "new.txt")
	gitRun(t, dir, "commit", "-q", "-m", "new")
	if note, err := r.PushNote("origin", "main", "", false, false, false); err != nil || note != "" {
		t.Fatalf("push with a commit: %q %v", note, err)
	}
	note, err = r.PullNote(PullOptions{Remote: "origin", Branch: "main"})
	if err != nil || note != UpToDatePull {
		t.Fatalf("pull with nothing new: %q %v", note, err)
	}
}
