package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The tests of this file drive the keyboard focus and the Cmd+Enter
// confirmation of the quick rebase message dialogs through the real UI: the
// History context menu opens them, key presses and typed text go through the
// window's input path, and git itself is the measure of what ran. The rules
// they pin down: the message editor takes the focus the frame it appears —
// after the initial read and after a retried one — typed text lands in it
// without a click, plain Enter stays its line break, and Cmd+Enter confirms
// exactly like the primary button while that button is enabled and does
// nothing at all while it is not.

// Rewording a commit that is not HEAD: once the read lands the editor holds
// the focus, so typing needs no click first and the characters land in the
// draft, and Enter inserts a line break into the text without submitting or
// closing anything.
func TestQuickRebaseFocusRewordTextAreaFocusedOnLoad(t *testing.T) {
	repo := newGitRepo(t, "qrfocus-reword")
	repo.CommitFile("base.txt", "base\n", "base commit")
	hashes := quickLinearFixture(t, repo, "q one", "q two")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	// hashes[0] is the older commit: a non-HEAD reword.
	openQuickRebaseItem(t, h, hashes[0], "q one", "Reword Message…")
	drainQuickRead(t, h)

	if !h.ui.Focused("Commit message") {
		t.Fatalf("message editor not focused after the read; texts=%q", h.ui.Texts())
	}
	box, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel")
	if ok && box.Focused {
		t.Fatal("the focus stayed on cancel — typing would go nowhere")
	}

	// No click: the focus the dialog gave the editor is what carries the
	// typed text into the draft.
	h.ui.Type("xy")
	h.ui.Frame()
	if got := quickRebaseUI(h.app).message; !strings.Contains(got, "xy") {
		t.Fatalf("typed text missing from the draft: %q", got)
	}

	before := quickRebaseUI(h.app).message
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	after := quickRebaseUI(h.app).message
	if strings.Count(after, "\n") != strings.Count(before, "\n")+1 {
		t.Errorf("message after Enter = %q, want %q with one more line break", after, before)
	}
	if st := quickRebaseUI(h.app); !st.open {
		t.Error("Enter in the editor closed the dialog")
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got != head {
		t.Fatalf("main moved after typing and Enter: %q, want %q", got, head)
	}
	cancelQuickDialog(t, h)
}

// Cmd+Enter with the focus in the editor confirms exactly like the Reword
// button: one write runs, the commit carries the edited multi-line message,
// and the dialog closes behind it.
func TestQuickRebaseFocusCmdEnterRewordsOnce(t *testing.T) {
	repo := newGitRepo(t, "qrfocus-cmdenter")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "the body")
	count := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)

	edited := "reworded subject\n\nreworded body"
	h.ui.Command("selectAll")
	h.ui.Type(edited)
	h.ui.Frame()
	if got := quickRebaseUI(h.app).message; got != edited {
		t.Fatalf("edited message = %q, want %q", got, edited)
	}
	if box, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message"); !ok || !box.Focused {
		t.Fatalf("editor not focused before Cmd+Enter: %+v found=%v — the test would measure the window path, not the in-editor one", box, ok)
	}

	// One write, no more: a second consumer of the key would meet the
	// session's busy state and go nowhere, so the single spawned worker is
	// the exact measure.
	h.queue.PauseWorkers()
	h.ui.Key(ui.Cmd, ui.KeyEnter)
	writeWorker := h.queue.WaitWorker(t)
	h.ui.Frame()
	if !h.app.session.Busy() {
		t.Fatal("session not busy while the write is held")
	}
	h.queue.ResumeWorkers()
	h.queue.ReleaseWorker(writeWorker)
	h.drain()

	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("reword by Cmd+Enter failed: %v", err)
	}
	assertQuickDialogClosed(t, h)
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != edited {
		t.Fatalf("HEAD message after the reword = %q, want %q", msg, edited)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-list", "--count", "main"); got != count {
		t.Fatalf("commit count after the reword = %q, want %q", got, count)
	}
	if got := gitOutTrim(t, repo.Path(), "rev-parse", "main"); got == tip {
		t.Fatal("main did not move after the reword")
	}
}

// With the message cleared the primary button is disabled — Git would refuse
// an empty message — and Cmd+Enter does nothing at all: the dialog stays
// open and no write touches the repository.
func TestQuickRebaseFocusCmdEnterBlankDoesNothing(t *testing.T) {
	repo := newGitRepo(t, "qrfocus-blank")
	repo.CommitFile("base.txt", "base\n", "base commit")
	tip := quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "the body")
	head := gitOutTrim(t, repo.Path(), "rev-parse", "HEAD")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, tip, "q one subject", "Reword Message…")
	drainQuickRead(t, h)

	h.ui.Command("selectAll")
	h.ui.Type("   ")
	h.ui.Frame()
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Reword"); !ok || !btn.Disabled {
		t.Fatalf("Reword with a blank message = %+v found=%v, want disabled", btn, ok)
	}

	h.ui.Key(ui.Cmd, ui.KeyEnter)
	h.drain()

	if st := quickRebaseUI(h.app); !st.open {
		t.Error("Cmd+Enter with a blank message closed the dialog")
	}
	if err := h.app.session.ActionErr(); err != nil {
		t.Fatalf("Cmd+Enter with a blank message ran a write that failed: %v", err)
	}
	assertQuickRepoUnchanged(t, repo, head, "2")
	if msg := strings.TrimSpace(repo.Git("log", "-1", "--format=%B", "main")); msg != "q one subject\n\nthe body" {
		t.Fatalf("message changed by the refused confirmation: %q", msg)
	}
	cancelQuickDialog(t, h)
}

// Squash into Parent reads two messages before its editor can show: the
// focus lands in the editor the frame it appears there too.
func TestQuickRebaseFocusSquashFocusedOnLoad(t *testing.T) {
	repo := newGitRepo(t, "qrfocus-squash")
	repo.CommitFile("base.txt", "base\n", "base commit")
	quickCommitMultiLine(t, repo, "q1.txt", "q one\n", "q one subject", "parent body")
	target := quickCommitMultiLine(t, repo, "q2.txt", "q two\n", "q two subject", "child body")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openQuickRebaseItem(t, h, target, "q two subject", "Squash into Parent…")
	if !hasExactText(h.ui, "Squash into Parent") {
		t.Fatalf("squash dialog did not open; texts=%q", h.ui.Texts())
	}
	drainQuickRead(t, h)

	if !h.ui.Focused("Commit message") {
		t.Fatalf("message editor not focused after the squash read; texts=%q", h.ui.Texts())
	}
	cancelQuickDialog(t, h)
}

// A failed read puts the focus on Retry, and clicking it keeps it there —
// but the editor the retried read reveals takes the focus the frame it
// appears, exactly as on the first open.
func TestQuickRebaseFocusRetryFocusesTextArea(t *testing.T) {
	repo := newGitRepo(t, "qrfocus-retry")
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
	if !hasExactText(h.ui, "injected message read failure") {
		t.Fatalf("read error text missing; texts=%q", h.ui.Texts())
	}

	if err := h.ui.Click("Retry"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	h.drain()
	if got, want := quickRebaseUI(h.app).message, "q one subject\n\nthe body"; got != want {
		t.Fatalf("message after Retry = %q, want %q", got, want)
	}
	if !h.ui.Focused("Commit message") {
		t.Fatalf("message editor not focused after the retried read; texts=%q", h.ui.Texts())
	}
	cancelQuickDialog(t, h)
}
