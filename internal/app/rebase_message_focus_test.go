package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Choosing Reword through the row's action choice moves the keyboard focus
// into the message editor it opens — once the original message lands and the
// editor enables — so typing lands in the text without a click, and Enter is
// the text area's own newline: it neither reopens the choice's popup nor
// changes the action, and the editor stays open.
func TestRebaseMessageFocusRewordChoiceFocusesTextArea(t *testing.T) {
	repo := newGitRepo(t, "irfocus-reword")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	// While the original messages are still on their way the editor is
	// disabled and the focus must not be in it: a disabled area cannot take
	// input, so the focus waits for the read.
	if area, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message"); !ok || !area.Disabled {
		t.Fatalf("editor while the read is in flight = %+v found=%v, want present and disabled; texts=%q", area, ok, h.ui.Texts())
	}
	if h.ui.Focused("Commit message") {
		t.Fatal("editor focused while its original message is still loading")
	}
	drainIRMessages(t, h)
	if !h.ui.Focused("Commit message") {
		t.Fatalf("editor not focused after choosing Reword; texts=%q", h.ui.Texts())
	}

	// Typing without clicking anywhere lands in the editor.
	h.ui.Type("typed addition")
	h.ui.Frame()
	ms := irMessagesState(t, h)
	if !strings.Contains(ms.editText, "typed addition") {
		t.Fatalf("typed text missing from the editor: %q", ms.editText)
	}

	// Enter inserts a newline into the text. Everything else stays exactly
	// as it was: the editor open, the action Reword, the choice's popup
	// closed.
	before := ms.editText
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if got, want := strings.Count(ms.editText, "\n"), strings.Count(before, "\n")+1; got != want {
		t.Fatalf("newlines in the editor after Enter = %d, want %d: %q", got, want, ms.editText)
	}
	if ms.editingHash != hashes[0] {
		t.Fatalf("editor target after Enter = %q, want still %q", ms.editingHash, hashes[0])
	}
	if row := irRowOf(t, h, hashes[0]); row.action != "Reword" {
		t.Fatalf("action after Enter = %q, want still Reword", row.action)
	}
	if sel, ok := h.ui.AccessByRole(ui.RolePopUpButton, "Action "+irShort(hashes[0])); !ok || sel.Expanded {
		t.Fatalf("action choice after Enter = %+v found=%v, want present and closed", sel, ok)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Reopening the editor through the row's Edit Message button moves the focus
// into the text as well: Enter is a newline rather than a second activation
// of the button, which would close the editor the click just opened.
func TestRebaseMessageFocusReopenButtonFocusesTextArea(t *testing.T) {
	repo := newGitRepo(t, "irfocus-reopen")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)

	// Toggle the editor closed and open again through the row's button.
	button := "Edit Message " + irShort(hashes[0])
	if err := h.ui.Click(button); err != nil {
		t.Fatalf("click %s to close: %v\ntexts=%q", button, err, h.ui.Texts())
	}
	h.ui.Frame()
	ms := irMessagesState(t, h)
	if ms.editingHash != "" {
		t.Fatalf("editor survived the toggle close: %q", ms.editingHash)
	}
	if err := h.ui.Click(button); err != nil {
		t.Fatalf("click %s to reopen: %v\ntexts=%q", button, err, h.ui.Texts())
	}
	h.ui.Frame()
	if ms.editingHash != hashes[0] {
		t.Fatalf("editor target after reopening = %q, want %q", ms.editingHash, hashes[0])
	}
	if !h.ui.Focused("Commit message") {
		t.Fatalf("editor not focused after reopening; texts=%q", h.ui.Texts())
	}
	if h.ui.Focused(button) {
		t.Fatal("focus stayed on the Edit Message button after reopening")
	}

	// Enter is the text area's newline, not another click of the button: the
	// editor must still be open after it.
	before := ms.editText
	h.ui.Key(0, ui.KeyEnter)
	h.ui.Frame()
	if got, want := strings.Count(ms.editText, "\n"), strings.Count(before, "\n")+1; got != want {
		t.Fatalf("newlines in the editor after Enter = %d, want %d: %q", got, want, ms.editText)
	}
	if ms.editingHash != hashes[0] {
		t.Fatal("editor closed by Enter — it activated the button instead of inserting a newline")
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// Clicking another row's Edit Message while the editor is open retargets it:
// the text becomes that row's message and the focus follows into the area.
func TestRebaseMessageFocusSwitchRowsFocusesTextArea(t *testing.T) {
	repo := newGitRepo(t, "irfocus-switch")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one", "ir two")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	// Both rows reworded, so both carry an Edit Message button; the editor
	// sits on ir one, opened by the second choice.
	chooseIRAction(t, h, hashes[1], "Pick", "Reword")
	drainIRMessages(t, h)
	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	ms := irMessagesState(t, h)
	if ms.editingHash != hashes[0] {
		t.Fatalf("editor target before the switch = %q, want %q", ms.editingHash, hashes[0])
	}
	if want := "ir one\n\nir one body"; ms.editText != want {
		t.Fatalf("editor text before the switch = %q, want %q", ms.editText, want)
	}

	if err := h.ui.Click("Edit Message " + irShort(hashes[1])); err != nil {
		t.Fatalf("click Edit Message of the other row: %v\ntexts=%q", err, h.ui.Texts())
	}
	h.ui.Frame()
	if ms.editingHash != hashes[1] {
		t.Fatalf("editor target after the switch = %q, want %q", ms.editingHash, hashes[1])
	}
	if want := "ir two\n\nir two body"; ms.editText != want {
		t.Fatalf("editor text after the switch = %q, want %q", ms.editText, want)
	}
	if !h.ui.Focused("Commit message") {
		t.Fatalf("editor not focused after switching rows; texts=%q", h.ui.Texts())
	}

	// Typing lands in the retargeted editor without a click.
	h.ui.Type("switched row words")
	h.ui.Frame()
	if !strings.Contains(ms.editText, "switched row words") {
		t.Fatalf("typed text missing after the switch: %q", ms.editText)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}

// A failed first read of the original messages leaves the disabled editor
// unfocused; once Retry Message reads them for real and the editor enables,
// the focus lands in it and typing goes straight in.
func TestRebaseMessageFocusRetryReadFailureFocusesTextArea(t *testing.T) {
	repo := newGitRepo(t, "irfocus-retry")
	repo.CommitFile("base.txt", "base\n", "base commit")
	onto := strings.TrimSpace(repo.Git("rev-parse", "HEAD"))
	hashes := irMessageFixture(t, repo, "ir one")

	h := newTestApp(t)
	openHistoryPage(t, h, repo.StoreRepo())
	openIRDialog(t, h, onto, "base commit")
	drainIRPlan(t, h)

	installFailFirstExecutor(t, h, isMessageLogCommand, errors.New("injected message read failure"))
	chooseIRAction(t, h, hashes[0], "Pick", "Reword")
	drainIRMessages(t, h)
	if !hasExactText(h.ui, "injected message read failure") {
		t.Fatalf("read failure text missing; texts=%q", h.ui.Texts())
	}
	if h.ui.Focused("Commit message") {
		t.Fatal("editor focused while its read failed and it is disabled")
	}

	// The injected failure fired exactly once: the retry reads for real, and
	// the frame the editor enables is the frame the focus lands in it.
	if err := h.ui.Click("Retry Message"); err != nil {
		t.Fatalf("click Retry Message: %v\ntexts=%q", err, h.ui.Texts())
	}
	drainIRMessages(t, h)
	if hasExactText(h.ui, "injected message read failure") {
		t.Fatal("failure text survived the successful retry")
	}
	if area, ok := h.ui.AccessByRole(ui.RoleTextField, "Commit message"); !ok || area.Disabled {
		t.Fatalf("editor after the successful retry = %+v found=%v, want present and enabled", area, ok)
	}
	if !h.ui.Focused("Commit message") {
		t.Fatalf("editor not focused after the successful retry; texts=%q", h.ui.Texts())
	}
	h.ui.Type("after retry words")
	h.ui.Frame()
	if got := irMessagesState(t, h).editText; !strings.Contains(got, "after retry words") {
		t.Fatalf("typed text missing after the retry: %q", got)
	}
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	h.ui.Frame()
}
