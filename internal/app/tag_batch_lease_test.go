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

// Tags of a blob, of a tree and of another tag delete through the batch
// dialog like any commit tag: the re-verification compares the object the
// tag peels to, which such tags have exactly as commit tags do.
func TestTagBatchLeaseDeleteNonCommitTags(t *testing.T) {
	repo := newGitRepo(t, "tbl-noncommit-del")
	repo.CommitFile("a.txt", "one\n", "initial commit")
	blob := strings.TrimSpace(repo.Git("hash-object", "-w", "a.txt"))
	repo.Git("tag", "blobtag", blob)
	tree := strings.TrimSpace(repo.Git("rev-parse", "HEAD^{tree}"))
	repo.Git("tag", "treetag", tree)
	repo.Git("tag", "-a", "-m", "inner", "inner", "HEAD")
	repo.Git("tag", "outer", "inner")

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "blobtag", "treetag", "outer")
	chooseBatchItem(t, h, "blobtag", "Delete 3 Tags…")
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
	for _, tag := range []string{"blobtag", "treetag", "outer"} {
		if tagRefExists(t, repo, tag) {
			t.Fatalf("%s survived the confirmed batch delete", tag)
		}
	}
	if !tagRefExists(t, repo, "inner") {
		t.Fatal("inner is gone even though it was never selected")
	}
}

// Tags of a blob, of a tree and of another tag push through the batch
// dialog: git itself carries such tags by their full references, and the
// re-verification no longer refuses them for never peeling to a commit.
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

	h := newTestApp(t)
	h.openRepo(repo.StoreRepo())
	h.drain()

	selectTagsForBatch(t, h, "blobtag", "treetag", "outer")
	chooseBatchItem(t, h, "blobtag", "Push 3 Tags to 'origin'…")
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
	for _, tag := range []string{"blobtag", "treetag", "outer"} {
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
