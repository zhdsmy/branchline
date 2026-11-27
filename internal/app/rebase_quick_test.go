package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// --- fixtures and helpers ---

// quickLinearFixture builds a linear main history: the initial commit of the
// fixture setup plus one commit per subject, each adding a file named after
// it, so replaying any subset applies without conflicts. It returns the
// hashes of the subject commits, oldest first.
func quickLinearFixture(t *testing.T, repo *gitRepoFixture, subjects ...string) []string {
	t.Helper()
	hashes := make([]string, 0, len(subjects))
	for i, subject := range subjects {
		name := "q" + string(rune('1'+i)) + ".txt"
		hashes = append(hashes, repo.CommitFile(name, subject+"\n", subject))
	}
	return hashes
}

// quickCommitMultiLine commits one file with a subject and a multi-line body,
// returning the new commit hash.
func quickCommitMultiLine(t *testing.T, repo *gitRepoFixture, rel, content, subject, body string) string {
	t.Helper()
	repo.WriteFile(rel, content)
	repo.Git("add", "--", rel)
	repo.Git("commit", "-m", subject, "-m", body)
	return strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
}

// quickHashBySubject resolves the hash of the newest commit of main carrying
// subject, as an independent git command reads it.
func quickHashBySubject(t *testing.T, dir, subject string) string {
	t.Helper()
	out := strings.TrimSpace(gitOutTrim(t, dir, "log", "--format=%H%x1f%s", "main"))
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) == 2 && parts[1] == subject {
			return parts[0]
		}
	}
	t.Fatalf("no commit with subject %q on main; log=%q", subject, out)
	return ""
}

// openQuickRebaseItem right-clicks the commit row of hash and chooses the
// item of the Quick Rebase Actions submenu through the real menu path.
func openQuickRebaseItem(t *testing.T, h *testHarness, hash, subject, item string) {
	t.Helper()
	row := commitRowLabel(hash, subject)
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row %q: %v\ntexts=%q", row, err, h.ui.Texts())
	}
	if err := h.ui.ChooseMenuItem("Quick Rebase Actions", item); err != nil {
		t.Fatalf("choose Quick Rebase Actions > %q: %v; menu=%q", item, err, h.ui.Menu())
	}
	h.ui.Frame()
}

// drainQuickRead waits for the dialog's message read to land and renders it.
func drainQuickRead(t *testing.T, h *testHarness) {
	t.Helper()
	h.drain()
	st := quickRebaseUI(h.app)
	if st.loading {
		t.Fatal("quick rebase message read still in flight after drain")
	}
}

// assertQuickDialogClosed verifies no quick rebase dialog draft survives.
func assertQuickDialogClosed(t *testing.T, h *testHarness) {
	t.Helper()
	if st := quickRebaseUI(h.app); st.open {
		t.Fatalf("quick rebase dialog still open: %+v", st)
	}
}

// assertQuickRepoUnchanged proves a refused or canceled rewrite changed
// nothing: HEAD, the main branch, the commit count and the working tree
// exactly as before.
func assertQuickRepoUnchanged(t *testing.T, repo *gitRepoFixture, head, count string) {
	t.Helper()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != head {
		t.Fatalf("main moved: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != count {
		t.Fatalf("commit count = %q, want %q", got, count)
	}
	if got := gitOutTrim(t, repo.Path(), "status", "--porcelain"); got != "" {
		t.Fatalf("working tree touched: %q", got)
	}
}

// assertQuickRefsUnmoved proves a refusal moved no ref and rewrote no
// commit — without touching the working tree question, which tests with
// deliberately dirty trees answer byte by byte on their own.
func assertQuickRefsUnmoved(t *testing.T, repo *gitRepoFixture, head, count string) {
	t.Helper()
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != head {
		t.Fatalf("main moved: %q, want %q", got, head)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != count {
		t.Fatalf("commit count = %q, want %q", got, count)
	}
}

// assertQuickRefusal proves the write was refused and the dialog kept its
// draft with the error showing.
func assertQuickRefusal(t *testing.T, h *testHarness, fragment string) {
	t.Helper()
	err := h.app.session.ActionErr()
	if err == nil {
		t.Fatal("expected an action error, got nil")
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("action error %q missing %q", err.Error(), fragment)
	}
	if st := quickRebaseUI(h.app); !st.open {
		t.Fatal("dialog closed on the refusal")
	}
	if !hasExactText(h.ui, err.Error()) {
		t.Fatalf("dialog does not display the refusal %q; texts=%q", err.Error(), h.ui.Texts())
	}
}

// cancelQuickDialog cancels the open dialog through its real Cancel button.
func cancelQuickDialog(t *testing.T, h *testHarness) {
	t.Helper()
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
	assertQuickDialogClosed(t, h)
}

// --- menu items ---

// The quick rebase items disable while an operation is in progress, while a
// write is busy and in a detached HEAD: none of those states can host a
// rewrite the user could confirm, and on an ordinary row every item of a
// commit with exactly one parent is offered.
func TestRebaseQuickMenuSharedDisabledStates(t *testing.T) {
	repo := newGitRepo(t, "qr-menu-shared")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	row := commitRowLabel(hashes[1], "q two")

	// During an operation — here a cherry-pick stopped on a conflict — the
	// items disable.
	repo.Git("checkout", "-qb", "other", hashes[0])
	repo.WriteFile("conflict.txt", "one\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "conflict one")
	side := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")
	repo.WriteFile("conflict.txt", "two\n")
	repo.Git("add", "--", "conflict.txt")
	repo.Git("commit", "-m", "conflict two")
	if out, err := tryGitCommand(repo.Path(), "cherry-pick", side); err == nil {
		t.Fatalf("conflicting cherry-pick unexpectedly succeeded: %s", out)
	}
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.State; got != "cherry-pick" {
		t.Fatalf("fixture: state = %q, want cherry-pick", got)
	}
	conflictRow := commitRowLabel(gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"), "conflict two")
	if err := h.ui.RightClick(conflictRow); err != nil {
		t.Fatalf("right-click during operation: %v", err)
	}
	for _, item := range []string{"Reword Message…", "Edit…", "Squash into Parent…", "Fixup into Parent…", "Drop…"} {
		if err := h.ui.ChooseMenuItem("Quick Rebase Actions", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q during an operation = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()
	repo.Git("cherry-pick", "--abort")
	repo.Git("reset", "-q", "--hard", hashes[1])
	h.app.session.Reload()
	h.drain()

	// While a write is busy the items disable too.
	repo.WriteFile("pending.txt", "pending\n")
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("click Changes: %v", err)
	}
	h.ui.Frame()
	h.queue.PauseWorkers()
	if err := h.ui.Click("Stage pending.txt"); err != nil {
		t.Fatalf("click Stage pending.txt: %v", err)
	}
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the stage worker is held")
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click while busy: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Quick Rebase Actions", "Drop…"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("choosing Drop while busy = %v, want a disabled error", err)
	}
	h.ui.CloseMenu()
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	// A detached HEAD has no branch for the rebase to move.
	repo.Git("checkout", "-q", "--detach", "HEAD")
	h.app.session.Reload()
	h.drain()
	if got := h.app.session.Snapshot().Status.Branch; got != "" {
		t.Fatalf("fixture: branch = %q, want detached", got)
	}
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click while detached: %v", err)
	}
	for _, item := range []string{"Reword Message…", "Drop…"} {
		if err := h.ui.ChooseMenuItem("Quick Rebase Actions", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q while detached = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()
	repo.Checkout("main")
	h.app.session.Reload()
	h.drain()

	// On an ordinary commit row every item is offered.
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click ordinary row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Quick Rebase Actions", "Reword Message…"); err != nil {
		t.Fatalf("choosing Reword Message on an ordinary row: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	cancelQuickDialog(t, h)
}

// The items of a merge commit all disable — rewriting it would flatten the
// merge — the fold items disable on a root commit, and Drop disables on the
// branch's only commit while Reword and Edit stay offered there.
func TestRebaseQuickMenuShapeDisabledStates(t *testing.T) {
	repo := newGitRepo(t, "qr-menu-shape")
	repo.CommitFile("base.txt", "base\n", "base commit")
	base := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	quickLinearFixture(t, repo, "q one", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// The root commit of a longer history cannot fold into a parent, but
	// Drop is offered: it is not the branch's only commit.
	rootRow := commitRowLabel(base, "base commit")
	if err := h.ui.RightClick(rootRow); err != nil {
		t.Fatalf("right-click root row: %v", err)
	}
	for _, item := range []string{"Squash into Parent…", "Fixup into Parent…"} {
		if err := h.ui.ChooseMenuItem("Quick Rebase Actions", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q on the root commit = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()

	// A merge commit disables every item.
	repo.Git("checkout", "-qb", "side", base)
	repo.WriteFile("side.txt", "side\n")
	repo.Git("add", "--", "side.txt")
	repo.Git("commit", "-m", "side commit")
	repo.Checkout("main")
	repo.Git("merge", "--no-ff", "-m", "merge side", "side")
	mergeHash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	h.app.session.Reload()
	h.drain()
	mergeRow := commitRowLabel(mergeHash, "merge side")
	if err := h.ui.RightClick(mergeRow); err != nil {
		t.Fatalf("right-click merge row: %v", err)
	}
	for _, item := range []string{"Reword Message…", "Edit…", "Squash into Parent…", "Fixup into Parent…", "Drop…"} {
		if err := h.ui.ChooseMenuItem("Quick Rebase Actions", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q on a merge commit = %v, want a disabled error", item, err)
		}
	}
	h.ui.CloseMenu()

	// The branch's only commit cannot be dropped — that would leave the
	// branch empty — but Reword and Edit stay offered.
	solo := newGitRepo(t, "qr-menu-solo")
	solo.CommitFile("only.txt", "only\n", "only commit")
	only := strings.TrimSpace(solo.Git("rev-parse", "HEAD"))
	h2 := newTestApp(t)
	openHistoryPage(t, h2, solo.StoreRepo())
	onlyRow := commitRowLabel(only, "only commit")
	if err := h2.ui.RightClick(onlyRow); err != nil {
		t.Fatalf("right-click only commit row: %v", err)
	}
	for _, item := range []string{"Squash into Parent…", "Fixup into Parent…", "Drop…"} {
		if err := h2.ui.ChooseMenuItem("Quick Rebase Actions", item); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("choosing %q on the only commit = %v, want a disabled error", item, err)
		}
	}
	if err := h2.ui.ChooseMenuItem("Quick Rebase Actions", "Reword Message…"); err != nil {
		t.Fatalf("choosing Reword Message on the only commit: %v; menu=%q", err, h2.ui.Menu())
	}
	h2.ui.Frame()
	cancelQuickDialog(t, h2)
}

// --- reword ---

// Rewording HEAD replaces its complete multi-line message through the real
// menu path: the editor pre-fills the full %B — never just the one-line
// subject — and the rewritten commit carries exactly the edited multi-line
// text while the files and the commit count stay unchanged.
func TestRebaseQuickRewordHeadKeepsMultiLineBody(t *testing.T) {
	repo := newGitRepo(t, "qr-reword-head")
	repo.CommitFile("base.txt", "base\n", "base commit")
	quickLinearFixture(t, repo, "q one")
	body := "first body line\nsecond body line"
	tip := quickCommitMultiLine(t, repo, "q2.txt", "q two\n", "q two subject", body)
	count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q two subject", "Reword Message…")
	if !hasExactText(h.ui, "Reword Commit") {
		t.Fatalf("reword dialog did not open; texts=%q", h.ui.Texts())
	}
	// The first frame still shows the loading line: the read's completion is
	// a queued callback the test has not drained yet.
	if !hasExactText(h.ui, "Loading commit message…") {
		t.Fatalf("loading line missing at the first frame; texts=%q", h.ui.Texts())
	}
	drainQuickRead(t, h)

	// The pre-fill is the complete multi-line message.
	original := "q two subject\n\n" + body
	if got := quickRebaseUI(h.app).message; got != original {
		t.Fatalf("pre-filled message = %q, want %q", got, original)
	}
	assertDialogLayout(t, h, "Cancel", "Reword")

	edited := "rewritten subject\n\nrewritten body line one\nrewritten body line two"
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Command("selectAll")
	h.ui.Type(edited)
	h.ui.Frame()
	if got := quickRebaseUI(h.app).message; got != edited {
		t.Fatalf("edited message = %q, want %q", got, edited)
	}
	if err := h.ui.Click("Reword"); err != nil {
		t.Fatalf("click Reword: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reword failed: %v", err)
	}
	assertQuickDialogClosed(t, h)

	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != edited {
		t.Fatalf("HEAD message after reword = %q, want %q", msg, edited)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != count {
		t.Fatalf("commit count after reword = %q, want %q", got, count)
	}
	assertFileBytes(t, repo, "q2.txt", "q two\n")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got == tip {
		t.Fatal("main did not move after the reword")
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after reword = %q, want empty", got)
	}
}

// Rewording a middle commit rewrites it in place with the multi-line body
// preserved in the parts the user did not edit, and rewording the root
// commit works through --root: the commits after each target keep their
// subjects in order and the working tree bytes stay exactly as before.
func TestRebaseQuickRewordMiddleAndRoot(t *testing.T) {
	repo := newGitRepo(t, "qr-reword-mid")
	rootBody := "root body line"
	root := quickCommitMultiLine(t, repo, "base.txt", "base\n", "base subject", rootBody)
	mid := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "middle body line")
	repo.CommitFile("q2.txt", "q two\n", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: the middle commit. Editing only the subject line keeps the
	// body because the pre-fill carried it.
	openQuickRebaseItem(t, h, mid, "q one subject", "Reword Message…")
	drainQuickRead(t, h)
	if got, want := quickRebaseUI(h.app).message, "q one subject\n\nmiddle body line"; got != want {
		t.Fatalf("middle pre-fill = %q, want %q", got, want)
	}
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type("q one renamed\n\nmiddle body line")
	h.ui.Frame()
	if err := h.ui.Click("Reword"); err != nil {
		t.Fatalf("click Reword: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("middle reword failed: %v", err)
	}
	midNew := quickHashBySubject(t, repo.Path(), "q one renamed")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", midNew)); msg != "q one renamed\n\nmiddle body line" {
		t.Fatalf("middle message after reword = %q", msg)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q two", "q one renamed", "base subject"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after middle reword = %v, want %v", subjects, want)
	}

	// Round 2: the root commit rewrites through --root.
	openQuickRebaseItem(t, h, root, "base subject", "Reword Message…")
	drainQuickRead(t, h)
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type("base renamed\n\nroot body line")
	h.ui.Frame()
	if err := h.ui.Click("Reword"); err != nil {
		t.Fatalf("click Reword: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("root reword failed: %v", err)
	}
	subjects = irLogSubjects(t, repo.Path(), "main")
	want = []string{"q two", "q one renamed", "base renamed"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after root reword = %v, want %v", subjects, want)
	}
	rootNew := quickHashBySubject(t, repo.Path(), "base renamed")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", rootNew)); msg != "base renamed\n\nroot body line" {
		t.Fatalf("root message after reword = %q", msg)
	}
	if out := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); out != "3" {
		t.Fatalf("commit count after both rewords = %q, want 3", out)
	}
	assertFileBytes(t, repo, "base.txt", "base\n")
	assertFileBytes(t, repo, "q1.txt", "q one\n")
	assertQuickDialogClosed(t, h)
}

// --- squash and fixup ---

// Squashing a commit into its parent opens the editor pre-filled with the
// parent's and the commit's full messages joined oldest first, and the fold
// lands with the edited multi-line message: one commit fewer, both files in
// it, later commits replayed in order.
func TestRebaseQuickSquashIntoParent(t *testing.T) {
	repo := newGitRepo(t, "qr-squash")
	repo.CommitFile("base.txt", "base\n", "base commit")
	parent := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "parent body one\nparent body two")
	target := quickCommitMultiLine(t, repo, "q2.txt", "q two\n", "q two subject", "child body")
	repo.CommitFile("q3.txt", "q three\n", "q three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, target, "q two subject", "Squash into Parent…")
	if !hasExactText(h.ui, "Squash into Parent") {
		t.Fatalf("squash dialog did not open; texts=%q", h.ui.Texts())
	}
	// The parent shows with its short hash.
	if !hasExactText(h.ui, irShort(parent)+" q one subject") {
		t.Fatalf("parent context line missing; texts=%q", h.ui.Texts())
	}
	drainQuickRead(t, h)

	prefill := "q one subject\n\nparent body one\nparent body two\n\nq two subject\n\nchild body"
	if got := quickRebaseUI(h.app).message; got != prefill {
		t.Fatalf("squash pre-fill = %q, want %q", got, prefill)
	}
	assertDialogLayout(t, h, "Cancel", "Squash")

	edited := "squashed subject\n\nsquashed body one\nsquashed body two"
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type(edited)
	h.ui.Frame()
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash into parent failed: %v", err)
	}
	assertQuickDialogClosed(t, h)

	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q three", "squashed subject", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after squash = %v, want %v", subjects, want)
	}
	folded := quickHashBySubject(t, repo.Path(), "squashed subject")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", folded)); msg != edited {
		t.Fatalf("folded commit message = %q, want %q", msg, edited)
	}
	// Both files of the pair sit in the folded commit's tree, and the
	// replayed q three keeps its content.
	if content := strings.TrimSpace(repo.Git("show", folded+":q1.txt")); content != "q one" {
		t.Fatalf("q1.txt at the folded commit = %q", content)
	}
	if content := strings.TrimSpace(repo.Git("show", folded+":q2.txt")); content != "q two" {
		t.Fatalf("q2.txt at the folded commit = %q", content)
	}
	assertFileBytes(t, repo, "q3.txt", "q three\n")
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("commit count after squash = %q, want 3", got)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after squash = %q, want empty", got)
	}
}

// Folding into the root commit works through --root: the squashed pair
// becomes the new root carrying the edited message.
func TestRebaseQuickSquashIntoRootParent(t *testing.T) {
	repo := newGitRepo(t, "qr-squash-root")
	root := repo.CommitFile("base.txt", "base\n", "base commit")
	target := repo.CommitFile("q1.txt", "q one\n", "q one")
	repo.CommitFile("q2.txt", "q two\n", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, target, "q one", "Squash into Parent…")
	drainQuickRead(t, h)
	if got, want := quickRebaseUI(h.app).message, "base commit\n\nq one"; got != want {
		t.Fatalf("root squash pre-fill = %q, want %q", got, want)
	}
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("squash into the root parent failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q two", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after the root squash = %v, want %v", subjects, want)
	}
	folded := quickHashBySubject(t, repo.Path(), "base commit")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", folded)); msg != "base commit\n\nq one" {
		t.Fatalf("folded root message = %q", msg)
	}
	if parents := gitOutTrim(t, repo.Path(), "rev-list", "--parents", "-n", "1", folded); len(strings.Fields(parents)) != 1 {
		t.Fatalf("the folded commit is not the root: %q", parents)
	}
	if content := strings.TrimSpace(repo.Git("show", folded+":q1.txt")); content != "q one" {
		t.Fatalf("q1.txt at the folded root = %q", content)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "2" {
		t.Fatalf("commit count after the root squash = %q, want 2", got)
	}
	assertQuickDialogClosed(t, h)
	_ = root
}

// Fixing up a commit into its parent folds it away silently: the parent
// keeps its complete multi-line message byte for byte, the files of both
// commits sit in the folded one, and later commits are replayed.
func TestRebaseQuickFixupKeepsParentMessage(t *testing.T) {
	repo := newGitRepo(t, "qr-fixup")
	repo.CommitFile("base.txt", "base\n", "base commit")
	parentBody := "parent body one\nparent body two"
	quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", parentBody)
	target := repo.CommitFile("q2.txt", "q two\n", "q two subject")
	repo.CommitFile("q3.txt", "q three\n", "q three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, target, "q two subject", "Fixup into Parent…")
	if !hasExactText(h.ui, "Fixup "+irShort(target)+" into its parent?") {
		t.Fatalf("fixup confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Fixup")
	if err := h.ui.Click("Fixup"); err != nil {
		t.Fatalf("click Fixup: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("fixup into parent failed: %v", err)
	}
	assertQuickDialogClosed(t, h)

	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q three", "q one subject", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after fixup = %v, want %v", subjects, want)
	}
	folded := quickHashBySubject(t, repo.Path(), "q one subject")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", folded)); msg != "q one subject\n\n"+parentBody {
		t.Fatalf("folded commit message = %q, want the parent's original multi-line message", msg)
	}
	if content := strings.TrimSpace(repo.Git("show", folded+":q2.txt")); content != "q two" {
		t.Fatalf("q2.txt at the folded commit = %q", content)
	}
	assertFileBytes(t, repo, "q3.txt", "q three\n")
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("commit count after fixup = %q, want 3", got)
	}
}

// --- edit ---

// Editing a commit stops the rebase on it: the shared banner appears with
// Continue available, the commit is the stopped HEAD, and continuing through
// the banner's real button finishes the replay in order.
func TestRebaseQuickEditStopAndContinue(t *testing.T) {
	repo := newGitRepo(t, "qr-edit")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two", "q three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, hashes[1], "q two", "Edit…")
	if !hasExactText(h.ui, "Edit "+irShort(hashes[1])+"?") {
		t.Fatalf("edit confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Edit")
	if err := h.ui.Click("Edit"); err != nil {
		t.Fatalf("click Edit: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("rebase to the edit stop failed: %v", err)
	}
	assertQuickDialogClosed(t, h)

	st := h.app.session.Snapshot().Status
	if st.State != "rebase" {
		t.Fatalf("state at the edit stop = %q, want rebase", st.State)
	}
	if st.Stopped != "edit" {
		t.Fatalf("stop reason = %q, want edit", st.Stopped)
	}
	if !hasExactText(h.ui, "Rebase in progress. Continue, skip, or abort the rebase.") {
		t.Fatalf("edit stop banner copy missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("Continue Operation at the edit stop = %+v found=%v, want enabled", btn, ok)
	}
	if subject := strings.TrimSpace(repo.Git("log", "-1", "--format=%s", "HEAD")); subject != "q two" {
		t.Fatalf("HEAD subject at the edit stop = %q, want q two", subject)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:q3.txt"); err == nil {
		t.Fatalf("q3.txt applied before the edit stop ended: %s", out)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("continue after the edit stop failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q three", "q two", "q one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after continuing the edit stop = %v, want %v", subjects, want)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after continue = %q, want empty", got)
	}
}

// --- drop ---

// Dropping a middle commit removes it and replays the commits after it; then
// dropping the new HEAD — the replayed tip — works the same way: the files
// of both dropped commits are gone and the count shrinks each time.
func TestRebaseQuickDropCommits(t *testing.T) {
	repo := newGitRepo(t, "qr-drop")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two", "q three")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: drop the middle commit q two.
	openQuickRebaseItem(t, h, hashes[1], "q two", "Drop…")
	if !hasExactText(h.ui, "Drop "+irShort(hashes[1])+"?") {
		t.Fatalf("drop confirmation did not open; texts=%q", h.ui.Texts())
	}
	assertDialogLayout(t, h, "Cancel", "Drop")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop of the middle commit failed: %v", err)
	}
	subjects := irLogSubjects(t, repo.Path(), "main")
	want := []string{"q three", "q one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after dropping q two = %v, want %v", subjects, want)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:q2.txt"); err == nil {
		t.Fatalf("q2.txt of the dropped commit survived: %s", out)
	}
	assertQuickDialogClosed(t, h)

	// Round 2: drop the replayed HEAD q three. Its hash changed with the
	// rewrite, so the row is resolved by subject.
	tip := quickHashBySubject(t, repo.Path(), "q three")
	h.app.session.Reload()
	h.drain()
	openQuickRebaseItem(t, h, tip, "q three", "Drop…")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop on HEAD: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("drop of HEAD failed: %v", err)
	}
	subjects = irLogSubjects(t, repo.Path(), "main")
	want = []string{"q one", "base commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("log after dropping HEAD = %v, want %v", subjects, want)
	}
	if out, err := tryGitCommand(repo.Path(), "cat-file", "-e", "HEAD:q3.txt"); err == nil {
		t.Fatalf("q3.txt of the dropped HEAD survived: %s", out)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "2" {
		t.Fatalf("commit count after both drops = %q, want 2", got)
	}
	assertFileBytes(t, repo, "q1.txt", "q one\n")
	assertQuickDialogClosed(t, h)
}

// --- guarded execution ---

// quickDropDialog opens the Drop confirmation for the commit of hash.
func quickDropDialog(t *testing.T, h *testHarness, hash, subject string) {
	t.Helper()
	openQuickRebaseItem(t, h, hash, subject, "Drop…")
	if !hasExactText(h.ui, "Drop "+irShort(hash)+"?") {
		t.Fatalf("drop confirmation did not open; texts=%q", h.ui.Texts())
	}
}

// An external commit after the dialog opened moves HEAD: the guard refuses
// the rebase, the dialog keeps its draft with the error showing, and every
// ref and byte of the repository is exactly as the external commit left it.
func TestRebaseQuickGuardHeadMoved(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-head")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	quickDropDialog(t, h, hashes[0], "q one")

	repo.WriteFile("other.txt", "other\n")
	repo.Git("add", "--", "other.txt")
	repo.Git("commit", "-m", "external commit")
	moved := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != moved {
		t.Fatalf("HEAD moved by the refused rebase: %q, want the external commit %q", got, moved)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != "4" {
		t.Fatalf("commit count after the refusal = %q, want 4", got)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	cancelQuickDialog(t, h)
}

// Switching to another branch without moving HEAD still refuses the rebase:
// HEAD and the branch must both be exactly what the dialog captured.
func TestRebaseQuickGuardBranchSwitched(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-branch")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	quickDropDialog(t, h, hashes[0], "q one")

	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.Git("checkout", "-q", "-b", "other")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD"); got != head {
		t.Fatal("fixture: checkout -b moved HEAD")
	}

	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "HEAD or the current branch changed")
	if got := gitOutTrim(t, repo.Path(), "symbolic-ref", "--short", "HEAD"); got != "other" {
		t.Fatalf("the refused rebase switched back: %q, want other", got)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "other"); got != head {
		t.Fatalf("other moved by the refused rebase: %q, want %q", got, head)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	cancelQuickDialog(t, h)
}

// Uncommitted tracked changes — unstaged or staged — refuse the rebase
// before Git runs: these actions offer no autostash choice, so the user
// commits or stashes first, and every byte of the working tree and the index
// is exactly as before.
func TestRebaseQuickGuardTrackedChanges(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-dirty")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: an unstaged tracked modification.
	repo.WriteFile("base.txt", "base\ndirty\n")
	quickDropDialog(t, h, hashes[0], "q one")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "cannot rebase with uncommitted local changes")
	assertQuickRefsUnmoved(t, repo, head, "3")
	assertFileBytes(t, repo, "base.txt", "base\ndirty\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); out != "" {
		t.Fatalf("index touched by the refused rebase: %q", out)
	}
	cancelQuickDialog(t, h)

	// Round 2: a staged change refuses the same way.
	repo.Git("checkout", "--", "base.txt")
	repo.WriteFile("base.txt", "base\nstaged\n")
	repo.Git("add", "--", "base.txt")
	quickDropDialog(t, h, hashes[0], "q one")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "cannot rebase with uncommitted local changes")
	assertQuickRefsUnmoved(t, repo, head, "3")
	assertFileBytes(t, repo, "base.txt", "base\nstaged\n")
	if out := gitOutTrim(t, repo.Path(), "diff", "--cached", "--name-only"); !strings.Contains(out, "base.txt") {
		t.Fatalf("staged change lost by the refused rebase: %q", out)
	}
	cancelQuickDialog(t, h)
}

// When the guard cannot even read the repository state, the rebase never
// runs: HEAD, the branch, the index and every byte of the working tree are
// exactly as before.
func TestRebaseQuickGuardStatusFailure(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-status")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	quickDropDialog(t, h, hashes[0], "q one")

	installFailFirstExecutor(t, h, isStatusCommand, errors.New("injected status failure"))
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "reading the repository state failed")
	assertQuickRepoUnchanged(t, repo, head, "3")
	cancelQuickDialog(t, h)
}

// Right-clicking a commit of another forked branch — visible in History
// because the log covers every branch — refuses the rewrite: the live plan
// of the current branch never contains it, and no byte of either branch
// moves.
func TestRebaseQuickGuardForkedBranchCommit(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-fork")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	mainHead := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")
	repo.Git("checkout", "-qb", "side", hashes[0])
	repo.WriteFile("side.txt", "side\n")
	repo.Git("add", "--", "side.txt")
	repo.Git("commit", "-m", "side only commit")
	sideHash := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	repo.Checkout("main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	if got := h.app.session.Snapshot().Status.Branch; got != "main" {
		t.Fatalf("fixture: branch = %q, want main", got)
	}
	quickDropDialog(t, h, sideHash, "side only commit")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "no longer on the current branch")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != mainHead {
		t.Fatalf("main moved by the refused rebase: %q, want %q", got, mainHead)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "side"); got != sideHash {
		t.Fatalf("side moved by the refused rebase: %q, want %q", got, sideHash)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	cancelQuickDialog(t, h)
}

// A merge commit inside the range the rewrite would replay refuses the
// rebase: interactive rebase flattens merges, so the guard's live plan read
// rejects the range before any write, and the history stays exactly as it
// was.
func TestRebaseQuickGuardMergeInRange(t *testing.T) {
	repo := newGitRepo(t, "qr-guard-merge")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one")
	mainBefore := gitOutTrim(t, repo.Path(), "rev-parse", "main")
	// The merge sits after the target q one, inside the replay range.
	repo.Git("checkout", "-qb", "side", hashes[0])
	repo.WriteFile("side.txt", "side\n")
	repo.Git("add", "--", "side.txt")
	repo.Git("commit", "-m", "side commit")
	repo.Checkout("main")
	repo.Git("merge", "--no-ff", "-m", "merge side", "side")
	repo.WriteFile("q2.txt", "q two\n")
	repo.Git("add", "--", "q2.txt")
	repo.Git("commit", "-m", "q two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	quickDropDialog(t, h, hashes[0], "q one")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	assertQuickRefusal(t, h, "merge commit")
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != gitOutTrim(t, repo.Path(), "rev-parse", "HEAD") {
		t.Fatal("main and HEAD diverged in the fixture")
	}
	if out := strings.TrimSpace(repo.Git("log", "--format=%s", "main")); !strings.Contains(out, "merge side") {
		t.Fatalf("history changed by the refused rebase: %q", out)
	}
	if out := strings.TrimSpace(repo.Git("status", "--porcelain")); out != "" {
		t.Fatalf("working tree touched by the refused rebase: %q", out)
	}
	_ = mainBefore
	cancelQuickDialog(t, h)
}

// --- message read failure, cancel and repository switch ---

// A failed message read shows the error and a Retry button and keeps the
// confirmation disabled — a rewrite must never start from a message the user
// did not see — and retrying after the failure loads the message for real.
func TestRebaseQuickMessageReadFailureAndRetry(t *testing.T) {
	repo := newGitRepo(t, "qr-retry")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "the body")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	isMessageLog := func(args []string) bool {
		return len(args) >= 2 && args[0] == "log" && args[1] == "-1"
	}
	installFailFirstExecutor(t, h, isMessageLog, errors.New("injected message read failure"))

	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)
	if !hasExactText(h.ui, "Retry") {
		t.Fatalf("Retry missing after the failed read; texts=%q", h.ui.Texts())
	}
	if !hasExactText(h.ui, "injected message read failure") {
		t.Fatalf("read error text missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Reword"); !ok || !btn.Disabled {
		t.Fatalf("Reword after the failed read = %+v found=%v, want disabled", btn, ok)
	}

	// The injected failure fired exactly once: the retry reads for real.
	if err := h.ui.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	h.drain()
	if got, want := quickRebaseUI(h.app).message, "q one subject\n\nthe body"; got != want {
		t.Fatalf("message after Retry = %q, want %q", got, want)
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Reword"); !ok || btn.Disabled {
		t.Fatalf("Reword after Retry = %+v found=%v, want enabled", btn, ok)
	}
	cancelQuickDialog(t, h)
}

// A blank message disables the confirmation and carries the hint: Git would
// refuse an empty message the same way, so the dialog catches it before any
// write starts.
func TestRebaseQuickBlankMessageDisabled(t *testing.T) {
	repo := newGitRepo(t, "qr-blank")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "the body")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)

	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type("   ")
	h.ui.Frame()
	if !hasExactText(h.ui, "The commit message cannot be empty.") {
		t.Fatalf("blank message hint missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Reword"); !ok || !btn.Disabled {
		t.Fatalf("Reword with a blank message = %+v found=%v, want disabled", btn, ok)
	}
	cancelQuickDialog(t, h)
	assertQuickRepoUnchanged(t, repo, head, "2")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "q one subject\n\nthe body" {
		t.Fatalf("message changed by the canceled dialog: %q", msg)
	}
}

// Canceling the editor — by its button or by dismissing the dialog — and
// canceling a confirmation touch nothing: the commit, the message, HEAD and
// every byte of the working tree stay exactly as they were, and the draft is
// gone.
func TestRebaseQuickCancelLeavesRepoUntouched(t *testing.T) {
	repo := newGitRepo(t, "qr-cancel")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "the body")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())

	// Round 1: edit the message, then cancel by button.
	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Type("\n\na note never committed")
	h.ui.Frame()
	cancelQuickDialog(t, h)
	assertQuickRepoUnchanged(t, repo, head, "2")

	// Round 2: reopen and dismiss with Escape.
	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)
	h.ui.Key(0, ui.KeyEscape)
	h.ui.Frame()
	assertQuickDialogClosed(t, h)
	assertQuickRepoUnchanged(t, repo, head, "2")

	// Round 3: cancel the Drop confirmation.
	quickDropDialog(t, h, tip, "q one subject")
	cancelQuickDialog(t, h)
	assertQuickRepoUnchanged(t, repo, head, "2")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "q one subject\n\nthe body" {
		t.Fatalf("message changed by the canceled dialogs: %q", msg)
	}
}

// Switching the repository closes the dialog and discards its message read:
// the read's late result — held back until after the switch — never lands in
// the draft, and reopening the dialog in the other repository reads its
// message, never a leftover of the first one.
func TestRebaseQuickLateMessageReadDiscardedOnRepoSwitch(t *testing.T) {
	repoA := newGitRepo(t, "qr-switch-a")
	repoA.CommitFile("base.txt", "base\n", "base commit")
	tipA := quickCommitMultiLine(t, repoA, "q1.txt", "q one\n", "alpha subject", "alpha body")
	repoB := newGitRepo(t, "qr-switch-b")
	repoB.CommitFile("b.txt", "b\n", "commit in beta")
	tipB := quickCommitMultiLine(t, repoB, "b2.txt", "b2\n", "beta subject", "beta body")

	h := newTestApp(t, repoB.StoreRepo())
	openHistoryPage(t, h, repoA.StoreRepo())

	// Open the dialog and hold its message read worker.
	h.queue.PauseWorkers()
	row := commitRowLabel(tipA, "alpha subject")
	if err := h.ui.RightClick(row); err != nil {
		t.Fatalf("right-click commit row: %v", err)
	}
	if err := h.ui.ChooseMenuItem("Quick Rebase Actions", "Reword Message…"); err != nil {
		t.Fatalf("choose Reword Message: %v; menu=%q", err, h.ui.Menu())
	}
	h.ui.Frame()
	readWorker := h.queue.WaitWorker(t)

	// Switch the repository while the read is in flight: resume first so the
	// switch's own load workers are never gated, then release the held
	// message read — its result can only land after the generation moved
	// past it.
	h.queue.ResumeWorkers()
	var savedID string
	for _, r := range h.store.Repos() {
		if r.Name == repoB.StoreRepo().Name {
			savedID = r.ID
		}
	}
	saved, ok := h.store.Get(savedID)
	if !ok {
		t.Fatal("seeded repository B missing from the store")
	}
	h.app.openRepo(saved)
	h.ui.Frame()
	h.queue.ReleaseWorker(readWorker)
	h.drain()

	st := quickRebaseUI(h.app)
	if st.open {
		t.Fatal("dialog still open after the repository switch")
	}
	if st.message != "" || st.target.Hash != "" {
		t.Fatalf("the late read landed in the draft: message=%q target=%q", st.message, st.target.Hash)
	}

	// Reopening the dialog in the other repository reads its message.
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("click History after switch: %v", err)
	}
	h.ui.Frame()
	openQuickRebaseItem(t, h, tipB, "beta subject", "Reword Message…")
	drainQuickRead(t, h)
	if got, want := quickRebaseUI(h.app).message, "beta subject\n\nbeta body"; got != want {
		t.Fatalf("message after reopening = %q, want %q", got, want)
	}
	cancelQuickDialog(t, h)
}

// --- SSH loopback ---

// quickRemoteCommitMultiLine commits one file on the server with a subject
// and a multi-line body, returning the new commit hash.
func quickRemoteCommitMultiLine(t *testing.T, srv *sshtest.Target, dir, rel, content, subject, body string) string {
	t.Helper()
	srv.WriteFile(t, dir, rel, []byte(content))
	srv.RunGit(t, dir, "add", "--", rel)
	srv.RunGit(t, dir, "commit", "-q", "-m", subject, "-m", body)
	return strings.TrimSpace(srv.RunGit(t, dir, "rev-parse", "HEAD"))
}

// quickRemoteHashBySubject resolves the hash of the newest commit of main
// carrying subject on the server.
func quickRemoteHashBySubject(t *testing.T, srv *sshtest.Target, dir, subject string) string {
	t.Helper()
	out := strings.TrimSpace(srv.RunGit(t, dir, "log", "--format=%H%x1f%s", "main"))
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) == 2 && parts[1] == subject {
			return parts[0]
		}
	}
	t.Fatalf("no remote commit with subject %q on main; log=%q", subject, out)
	return ""
}

// quickRemoteSubjects returns the log subjects of main on the server, newest
// first.
func quickRemoteSubjects(t *testing.T, srv *sshtest.Target, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(srv.RunGit(t, dir, "log", "--format=%s", "main"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Over a real SSH connection the multi-line reword of a non-HEAD commit and
// the squash into its parent with a custom message run exactly as locally:
// every assertion reads the server's repository through a second SSH
// channel.
func TestRebaseQuickRewordSquashLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	quickRemoteCommitMultiLine(t, srv, repo.Path, "q1.txt", "q one\n", "q one subject", "parent body")
	target := quickRemoteCommitMultiLine(t, srv, repo.Path, "q2.txt", "q two\n", "q two subject", "child body one\nchild body two")
	remoteCommitFile(t, srv, repo.Path, "q3.txt", "q three\n", "q three")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Part 1: reword the middle commit q two, keeping its multi-line body
	// through an edit of the subject alone.
	openQuickRebaseItem(t, h, target, "q two subject", "Reword Message…")
	drainQuickRead(t, h)
	if got, want := quickRebaseUI(h.app).message, "q two subject\n\nchild body one\nchild body two"; got != want {
		t.Fatalf("SSH pre-fill = %q, want %q", got, want)
	}
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type("q two renamed\n\nchild body one\nchild body two")
	h.ui.Frame()
	if err := h.ui.Click("Reword"); err != nil {
		t.Fatalf("click Reword: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH reword failed: %v", err)
	}
	renamed := quickRemoteHashBySubject(t, srv, repo.Path, "q two renamed")
	if msg := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%B", renamed)); msg != "q two renamed\n\nchild body one\nchild body two" {
		t.Fatalf("remote message after reword = %q", msg)
	}
	assertQuickDialogClosed(t, h)

	// Part 2: squash the renamed commit into its parent with a custom
	// multi-line message.
	h.app.session.Reload()
	h.drain()
	openQuickRebaseItem(t, h, renamed, "q two renamed", "Squash into Parent…")
	drainQuickRead(t, h)
	if err := h.ui.Click("Commit message"); err != nil {
		t.Fatalf("click Commit message: %v", err)
	}
	h.ui.Command("selectAll")
	h.ui.Type("ssh squashed\n\nssh squash body")
	h.ui.Frame()
	if err := h.ui.Click("Squash"); err != nil {
		t.Fatalf("click Squash: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH squash failed: %v", err)
	}
	subjects := quickRemoteSubjects(t, srv, repo.Path)
	want := []string{"q three", "ssh squashed", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after squash = %v, want %v", subjects, want)
	}
	folded := quickRemoteHashBySubject(t, srv, repo.Path, "ssh squashed")
	if msg := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%B", folded)); msg != "ssh squashed\n\nssh squash body" {
		t.Fatalf("remote folded message = %q", msg)
	}
	if content := strings.TrimSpace(srv.RunGit(t, repo.Path, "show", folded+":q2.txt")); content != "q two" {
		t.Fatalf("remote q2.txt at the folded commit = %q", content)
	}
	if got := srv.RunGit(t, repo.Path, "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("remote commit count after squash = %q, want 3", got)
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "status", "--porcelain")); out != "" {
		t.Fatalf("remote working tree dirty after the rewrites: %q", out)
	}
	assertQuickDialogClosed(t, h)
}

// Over SSH the fixup keeps the parent's multi-line message, the drop removes
// the commit from the branch, and the edit stop continues through the
// banner, each verified on the server through a second SSH channel.
func TestRebaseQuickFixupDropEditLoopbackSSH(t *testing.T) {
	repo, srv := loopbackSSHRepo(t)
	quickRemoteCommitMultiLine(t, srv, repo.Path, "q1.txt", "q one\n", "q one subject", "parent body one\nparent body two")
	target := quickRemoteCommitMultiLine(t, srv, repo.Path, "q2.txt", "q two\n", "q two subject", "child body")
	remoteCommitFile(t, srv, repo.Path, "q3.txt", "q three\n", "q three")

	h := newTestApp(t, repo)
	openSSHHistoryPage(t, h, repo)

	// Part 1: fixup q two into q one — the parent's full message survives.
	openQuickRebaseItem(t, h, target, "q two subject", "Fixup into Parent…")
	if err := h.ui.Click("Fixup"); err != nil {
		t.Fatalf("click Fixup: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH fixup failed: %v", err)
	}
	folded := quickRemoteHashBySubject(t, srv, repo.Path, "q one subject")
	if msg := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%B", folded)); msg != "q one subject\n\nparent body one\nparent body two" {
		t.Fatalf("remote fixup message = %q, want the parent's original", msg)
	}
	if content := strings.TrimSpace(srv.RunGit(t, repo.Path, "show", folded+":q2.txt")); content != "q two" {
		t.Fatalf("remote q2.txt at the fixup commit = %q", content)
	}
	assertQuickDialogClosed(t, h)

	// Part 2: drop the replayed q three — resolved by subject because the
	// rewrite changed its hash.
	h.app.session.Reload()
	h.drain()
	tip := quickRemoteHashBySubject(t, srv, repo.Path, "q three")
	openQuickRebaseItem(t, h, tip, "q three", "Drop…")
	if err := h.ui.Click("Drop"); err != nil {
		t.Fatalf("click Drop: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH drop failed: %v", err)
	}
	subjects := quickRemoteSubjects(t, srv, repo.Path)
	want := []string{"q one subject", "initial commit"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after drop = %v, want %v", subjects, want)
	}
	if out := srv.RunShell(t, "git -C "+git.ShellQuote(repo.Path)+" cat-file -e HEAD:q3.txt 2>/dev/null && echo present || echo absent"); strings.TrimSpace(out) != "absent" {
		t.Fatalf("q3.txt of the dropped commit survived on the server: %q", out)
	}
	assertQuickDialogClosed(t, h)

	// Part 3: edit the folded commit — the rebase stops with the banner's
	// Continue available, and continuing finishes the replay.
	h.app.session.Reload()
	h.drain()
	foldedNow := quickRemoteHashBySubject(t, srv, repo.Path, "q one subject")
	openQuickRebaseItem(t, h, foldedNow, "q one subject", "Edit…")
	if err := h.ui.Click("Edit"); err != nil {
		t.Fatalf("click Edit: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH rebase to the edit stop failed: %v", err)
	}
	st := h.app.session.Snapshot().Status
	if st.State != "rebase" || st.Stopped != "edit" {
		t.Fatalf("SSH edit stop state = %+v, want rebase stopped at edit", st)
	}
	if !hasExactText(h.ui, "Rebase in progress. Continue, skip, or abort the rebase.") {
		t.Fatalf("SSH edit stop banner copy missing; texts=%q", h.ui.Texts())
	}
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Continue Operation"); !ok || btn.Disabled {
		t.Fatalf("SSH Continue at the edit stop = %+v found=%v, want enabled", btn, ok)
	}
	if subject := strings.TrimSpace(srv.RunGit(t, repo.Path, "log", "-1", "--format=%s", "HEAD")); subject != "q one subject" {
		t.Fatalf("remote HEAD subject at the edit stop = %q, want q one subject", subject)
	}

	if err := h.ui.Click("Continue Operation"); err != nil {
		t.Fatalf("click Continue Operation: %v", err)
	}
	h.drain()
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("SSH continue after the edit stop failed: %v", err)
	}
	if got := h.app.session.Snapshot().Status.State; got != "" {
		t.Fatalf("state after SSH continue = %q, want empty", got)
	}
	subjects = quickRemoteSubjects(t, srv, repo.Path)
	if !slices.Equal(subjects, want) {
		t.Fatalf("remote log after the edit continue = %v, want %v", subjects, want)
	}
	if out := strings.TrimSpace(srv.RunGit(t, repo.Path, "status", "--porcelain")); out != "" {
		t.Fatalf("remote working tree dirty after continue: %q", out)
	}
	assertQuickDialogClosed(t, h)
}
