package app

import (
	"strings"
	"testing"
)

// tickRemoteDelete ticks the Delete Tags dialog's remote box and renders a
// frame so the confirm button reflects it.
func tickRemoteDelete(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Also delete from remotes"); err != nil {
		t.Fatalf("tick remote deletion: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
}

// resetRecording drops the invocations a recordingRunExecutor collected so
// far, so the count of one batch operation measures that operation alone.
func resetRecording(e *recordingRunExecutor) {
	e.mu.Lock()
	e.cmds = nil
	e.mu.Unlock()
}

// countMatching reports how many recorded invocations match the predicate.
func countMatching(e *recordingRunExecutor, match func(args []string) bool) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, args := range e.cmds {
		if match(args) {
			n++
		}
	}
	return n
}

// anyArgContains reports whether any argument holds sub as a substring.
func anyArgContains(args []string, sub string) bool {
	for _, a := range args {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

// The batch re-verification reads every checked tag in one for-each-ref —
// the same fields the snapshot scans with, so capture and comparison agree
// on every git version — never one rev-parse per tag and never a fully
// peeling ^{} read. The push and the delete each pass through it once.
func TestTagBatchLeaseVerifyRunsOneCommand(t *testing.T) {
	repo := newGitRepo(t, "tbl-verify-count")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.CreateTag("rel-1")
	repo.CreateTag("rel-2")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()
	rec := installRecordingExecutor(t, h)

	isTagScan := func(args []string) bool {
		return len(args) > 0 && args[0] == "for-each-ref" && hasArg(args, "refs/tags/rel-1")
	}
	isPeelRead := func(args []string) bool {
		return len(args) > 0 && args[0] == "rev-parse" && anyArgContains(args, "^{}")
	}

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to 'origin'…")
	resetRecording(rec)
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch push failed: %v", err)
	}
	if n := countMatching(rec, isTagScan); n != 1 {
		t.Fatalf("%d for-each-ref invocations naming rel-1 during the push, want exactly the one re-verification", n)
	}
	if n := countMatching(rec, isPeelRead); n != 0 {
		t.Fatalf("%d rev-parse ^{} invocations during the push, want none: the comparison reads like the snapshot", n)
	}

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	resetRecording(rec)
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch delete failed: %v", err)
	}
	if n := countMatching(rec, isTagScan); n != 1 {
		t.Fatalf("%d for-each-ref invocations naming rel-1 during the delete, want exactly the one re-verification", n)
	}
	if n := countMatching(rec, isPeelRead); n != 0 {
		t.Fatalf("%d rev-parse ^{} invocations during the delete, want none: the comparison reads like the snapshot", n)
	}
	if !bareHasTag(t, bareDir, "rel-1") {
		t.Fatal("fixture broken: the pushed tags never reached the server")
	}
}

// A tag somebody moved on the server since the last fetch refuses the whole
// batch delete with the remote box ticked: nothing is deleted, not locally
// and not on the server — not even the tag that still matches — and the
// error in the interface names the moved tag and the remote.
func TestTagBatchLeaseDeleteRefusesServerMovedTag(t *testing.T) {
	repo := newGitRepo(t, "tbl-moved")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CreateTag("rel-1")
	repo.CreateTag("rel-2")
	bareDir := repo.AddBareRemote("origin")
	repo.Git("push", "-q", "origin", "refs/tags/rel-1", "refs/tags/rel-2")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	tickRemoteDelete(t, h)

	// From another clone, recreate rel-1 on the server onto newer work,
	// behind the local repository's back: the local tag is exactly what the
	// dialog captured, but the server's tag no longer is.
	peer := t.TempDir() + "/peer"
	runGitCommand(t, "", "clone", "-q", bareDir, peer)
	writeTestFile(t, peer, "peer.txt", "peer work\n")
	runGitCommand(t, peer, "add", "peer.txt")
	runGitCommand(t, peer, "-c", "user.name=Peer", "-c", "user.email=peer@test.local", "commit", "-qm", "peer retagged work")
	peerHead := strings.TrimSpace(runGitCommand(t, peer, "rev-parse", "HEAD"))
	runGitCommand(t, peer, "tag", "-f", "rel-1", "HEAD")
	runGitCommand(t, peer, "push", "-qf", "origin", "refs/tags/rel-1")
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/tags/rel-1")); got != peerHead {
		t.Fatalf("fixture broken: server rel-1 = %q, want the peer's %q", got, peerHead)
	}

	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting a server-moved tag produced no error")
	}
	if !strings.Contains(err.Error(), "rel-1") || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("ActionErr = %q, want it to name rel-1 and origin", err)
	}
	if strings.Contains(err.Error(), "rel-2") {
		t.Fatalf("ActionErr = %q names rel-2, which still matches", err)
	}
	if got := strings.TrimSpace(runGitCommand(t, bareDir, "rev-parse", "refs/tags/rel-1")); got != peerHead {
		t.Fatalf("server rel-1 = %q, want the peer's %q untouched", got, peerHead)
	}
	if !bareHasTag(t, bareDir, "rel-2") {
		t.Fatal("server rel-2 was deleted even though the batch was refused")
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !tagRefExists(t, repo, tag) {
			t.Fatalf("local %s was deleted even though the batch was refused", tag)
		}
	}

	// The reload after the failure leaves the sidebar consistent with the
	// repository: both tags are still there.
	h.drain()
	for _, tag := range []string{"rel-1", "rel-2"} {
		if _, ok := h.ui.Find(tag); !ok {
			t.Fatalf("%s missing from the sidebar after the refused delete; texts=%q", tag, h.ui.Texts())
		}
	}
}

// Tags of a blob, of a tree, of another tag and an annotated tag of a
// commit delete through the batch dialog like any lightweight commit tag:
// the re-verification reads the same for-each-ref fields the snapshot
// captured, so it matches the peeled object whatever the object type.
func TestTagBatchLeaseDeleteNonCommitTags(t *testing.T) {
	repo := newGitRepo(t, "tbl-noncommit-del")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	blob := strings.TrimSpace(repo.Git("hash-object", "-w", "a.txt"))
	repo.Git("tag", "blobtag", blob)
	tree := strings.TrimSpace(repo.Git("rev-parse", "HEAD^{tree}"))
	repo.Git("tag", "treetag", tree)
	repo.Git("tag", "-a", "-m", "inner", "inner", "HEAD")
	repo.Git("tag", "outer", "inner")
	repo.Git("tag", "-a", "-m", "annotated", "anntag", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "blobtag", "treetag", "outer", "anntag")
	chooseBatchItem(t, h, "blobtag", "Delete 4 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch delete of blob, tree and nested tags failed: %v", err)
	}
	for _, tag := range []string{"blobtag", "treetag", "outer", "anntag"} {
		if tagRefExists(t, repo, tag) {
			t.Fatalf("%s survived the confirmed batch delete", tag)
		}
	}
	if !tagRefExists(t, repo, "inner") {
		t.Fatal("inner is gone even though it was never selected")
	}
}

// Tags of a blob, of a tree, of another tag and an annotated tag of a
// commit push through the batch dialog: git itself carries such tags by
// their full references, and the re-verification matches the peeled object
// the snapshot captured instead of refusing everything but commit peels.
func TestTagBatchLeasePushNonCommitTags(t *testing.T) {
	repo := newGitRepo(t, "tbl-noncommit-push")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	blob := strings.TrimSpace(repo.Git("hash-object", "-w", "a.txt"))
	repo.Git("tag", "blobtag", blob)
	tree := strings.TrimSpace(repo.Git("rev-parse", "HEAD^{tree}"))
	repo.Git("tag", "treetag", tree)
	repo.Git("tag", "-a", "-m", "inner", "inner", "HEAD")
	repo.Git("tag", "outer", "inner")
	repo.Git("tag", "-a", "-m", "annotated", "anntag", "HEAD")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "blobtag", "treetag", "outer", "anntag")
	chooseBatchItem(t, h, "blobtag", "Push 4 Tags to 'origin'…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch push of blob, tree and nested tags failed: %v", err)
	}
	for _, tag := range []string{"blobtag", "treetag", "outer", "anntag"} {
		if !bareHasTag(t, bareDir, tag) {
			t.Fatalf("%s did not reach the server through the confirmed batch push", tag)
		}
	}
	if bareHasTag(t, bareDir, "inner") {
		t.Fatal("inner reached the server even though it was never selected")
	}
}

// A tag moved with git tag -f after the Delete Tags dialog opened refuses
// the whole batch delete: nothing is deleted, not even the tags that never
// moved, and the error says the tag changed after the dialog opened.
func TestTagBatchLeaseDeleteRefusesMovedTag(t *testing.T) {
	repo := newGitRepo(t, "tbl-del-guard")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	repo.CreateTag("rel-1")
	repo.CreateTag("rel-2")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open; texts=%q", h.ui.Texts())
	}

	repo.CommitFile("move.txt", "moved\n", "retarget commit")
	repo.Git("tag", "-f", "rel-1", "HEAD")

	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting over a retargeted tag produced no error")
	}
	if !strings.Contains(err.Error(), "cannot delete tags: rel-1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !tagRefExists(t, repo, tag) {
			t.Fatalf("%s was deleted even though the batch was refused", tag)
		}
	}
}

// A tag moved with git tag -f after the Push Tags dialog opened refuses the
// whole batch push: nothing is pushed, not even the tags that never moved,
// and the error says the tag changed after the dialog opened.
func TestTagBatchLeasePushRefusesMovedTag(t *testing.T) {
	repo := newGitRepo(t, "tbl-push-guard")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	bareDir := repo.AddBareRemote("origin")
	repo.CreateTag("rel-1")
	repo.CreateTag("rel-2")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Push 2 Tags to 'origin'…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open; texts=%q", h.ui.Texts())
	}

	repo.CommitFile("move.txt", "moved\n", "retarget commit")
	repo.Git("tag", "-f", "rel-1", "HEAD")

	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("pushing over a retargeted tag produced no error")
	}
	if !strings.Contains(err.Error(), "cannot push tags: rel-1 changed after the dialog opened") {
		t.Fatalf("ActionErr = %q, want the changed-after-the-dialog-opened refusal", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if bareHasTag(t, bareDir, tag) {
			t.Fatalf("%s reached the server even though the batch was refused", tag)
		}
	}
}

// On a loopback SSH repository the batch delete with the remote box ticked
// verifies the server's copies before deleting: a tag moved on the server
// refuses the whole batch with the error naming it, and once the server
// matches again the same batch deletes everywhere.
func TestTagBatchLeaseLoopbackSSHDelete(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	bare := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bare)
	for _, tag := range []string{"rel-1", "rel-2"} {
		srv.RunGit(t, repo.Path, "tag", tag)
	}
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "refs/tags/rel-1", "refs/tags/rel-2")

	sshBareHasTag := func(tag string) bool {
		return strings.TrimSpace(srv.RunGit(t, bare, "tag", "--list", tag)) != ""
	}
	serverTagOID := func(tag string) string {
		return strings.TrimSpace(srv.RunGit(t, bare, "rev-parse", "refs/tags/"+tag))
	}

	// Move rel-1 on the server behind the repository's back: new committed
	// work the local repository never fetches, retagged there.
	srv.WriteFile(t, repo.Path, "moved.txt", []byte("server move\n"))
	srv.RunGit(t, repo.Path, "add", "moved.txt")
	srv.RunGit(t, repo.Path, "commit", "-qm", "server move")
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "main")
	srv.RunGit(t, bare, "tag", "-f", "rel-1", "main")
	movedOID := serverTagOID("rel-1")
	// The move stays invisible to the local repository: its main is reset
	// back and it never fetches the retagging.
	srv.RunGit(t, repo.Path, "reset", "-q", "--hard", "HEAD~1")

	h := newTestApp(t)
	h.openRepo(repo)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}

	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open on SSH; texts=%q", h.ui.Texts())
	}
	tickRemoteDelete(t, h)
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("deleting a server-moved tag over SSH produced no error")
	}
	if !strings.Contains(err.Error(), "rel-1") || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("ActionErr = %q, want it to name rel-1 and origin", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if !sshBareHasTag(tag) {
			t.Fatalf("server %s was deleted even though the batch was refused", tag)
		}
		if srv.RunGit(t, repo.Path, "tag", "--list", tag) == "" {
			t.Fatalf("local %s was deleted even though the batch was refused", tag)
		}
	}
	if got := serverTagOID("rel-1"); got != movedOID {
		t.Fatalf("server rel-1 = %q, want the moved %q untouched", got, movedOID)
	}

	// The server matches the local tags again — someone moved rel-1 back —
	// and the very same batch now deletes everywhere.
	srv.RunGit(t, bare, "tag", "-f", "rel-1", "rel-2")
	selectTagsForBatch(t, h, "rel-1", "rel-2")
	chooseBatchItem(t, h, "rel-1", "Delete 2 Tags…")
	tickRemoteDelete(t, h)
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH batch delete of matching tags failed: %v", err)
	}
	for _, tag := range []string{"rel-1", "rel-2"} {
		if sshBareHasTag(tag) {
			t.Fatalf("%s survived on the server even though it matched the local tag", tag)
		}
		if srv.RunGit(t, repo.Path, "tag", "--list", tag) != "" {
			t.Fatalf("local %s survived the confirmed batch delete", tag)
		}
	}
}

// On a configured Linux SSH repository (sshlab, down to Git 2.25 on e2) a
// tag of a tag batches like any other: the re-verification reads the same
// for-each-ref fields the snapshot scans with, so the two agree however far
// the server's git peels a star reference — the comparison never refuses
// the batch for the nesting alone. Pushing and then deleting the nested
// tag through the batch dialogs both succeed, locally and on the server's
// own remote.
func TestConfiguredLinuxTagBatchLeaseTagOfTag(t *testing.T) {
	repo, srv := configuredLinuxRepo(t)
	srv.RunGit(t, repo.Path, "tag", "-a", "-m", "inner release", "inner", "HEAD")
	srv.RunGit(t, repo.Path, "tag", "outer", "inner")
	srv.RunGit(t, repo.Path, "tag", "rel-keep")
	bareDir := srv.InitBareRepo(t, "origin")
	srv.RunGit(t, repo.Path, "remote", "add", "origin", bareDir)
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "main")

	linuxHasTag := func(dir, tag string) bool {
		return strings.TrimSpace(srv.RunGit(t, dir, "tag", "--list", tag)) != ""
	}

	h := newTestApp(t)
	openSSHRepoThroughSwitcher(t, h, repo)
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("SSH snapshot load failed: %v", err)
	}
	for _, tag := range []string{"inner", "outer", "rel-keep"} {
		if _, ok := h.ui.Find(tag); !ok {
			t.Fatalf("%s missing from the SSH sidebar; texts=%q", tag, h.ui.Texts())
		}
	}

	// The batch push of the nested tag must pass the re-verification: outer
	// reaches the server's remote, the never-selected tags do not.
	selectTagsForBatch(t, h, "outer")
	chooseBatchItem(t, h, "outer", "Push 1 Tags to 'origin'…")
	if !hasExactText(h.ui, "Push Tags") {
		t.Fatalf("Push Tags dialog did not open on Linux SSH; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Confirm Push Tags"); err != nil {
		t.Fatalf("click Confirm Push Tags: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch push of a tag of a tag on Linux SSH failed: %v", err)
	}
	if !linuxHasTag(bareDir, "outer") {
		t.Fatal("outer did not reach the server's remote through the confirmed batch push")
	}
	if linuxHasTag(bareDir, "inner") || linuxHasTag(bareDir, "rel-keep") {
		t.Fatal("a tag that was never selected reached the server's remote")
	}

	// The batch delete with the remote box ticked must pass it too: outer
	// leaves the repository and the server's remote, the others stay.
	srv.RunGit(t, repo.Path, "push", "-q", "origin", "refs/tags/outer:refs/tags/outer")
	selectTagsForBatch(t, h, "outer", "rel-keep")
	chooseBatchItem(t, h, "outer", "Delete 2 Tags…")
	if !hasExactText(h.ui, "Delete Tags") {
		t.Fatalf("Delete Tags dialog did not open on Linux SSH; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("rel-keep"); err != nil {
		t.Fatalf("uncheck rel-keep row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	tickRemoteDelete(t, h)
	if err := h.ui.Click("Confirm Delete Tags"); err != nil {
		t.Fatalf("click Confirm Delete Tags: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("batch delete of a tag of a tag on Linux SSH failed: %v", err)
	}
	if linuxHasTag(repo.Path, "outer") {
		t.Fatal("outer survived the confirmed batch delete")
	}
	if linuxHasTag(bareDir, "outer") {
		t.Fatal("outer survived on the server's remote even though the remote box was ticked")
	}
	for _, tag := range []string{"inner", "rel-keep"} {
		if !linuxHasTag(repo.Path, tag) {
			t.Fatalf("%s is gone even though it was never part of the batch", tag)
		}
	}
}
