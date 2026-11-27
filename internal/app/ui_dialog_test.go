package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// dialogHarness drives a view that is a single dialogFrame and latches what
// its frames report: a dismissal or a click holds for the one frame that
// took it, after which the dialog is closed or the buttons repaint, so the
// pulses are kept rather than the last frame's.
type dialogHarness struct {
	ui        *ui.Tester
	open      bool
	primary   dialogAction
	dismissed bool
	cancelled bool
	confirmed bool
}

func newDialogHarness(t *testing.T, primary dialogAction) *dialogHarness {
	t.Helper()
	h := &dialogHarness{open: true, primary: primary}
	p := paletteForTheme(nil)
	h.ui = ui.NewTester(func(c *ui.Context) {
		dismissed, cancelled, confirmed := dialogFrame(c, p, "test-modal", &h.open, "Test Dialog", 460, func() {
			ui.Text(c, "body text")
		},
			dialogAction{key: "test-cancel", text: "Cancel", label: "Cancel"},
			h.primary)
		h.dismissed = h.dismissed || dismissed
		h.cancelled = h.cancelled || cancelled
		h.confirmed = h.confirmed || confirmed
	}, 1200, 800)
	h.ui.Frame()
	return h
}

// testConfirmButton is the harness's primary button in its plain form.
func testConfirmButton() dialogAction {
	return dialogAction{key: "test-confirm", text: "Confirm", label: "Confirm the Test"}
}

// The panel is a dialog and carries the title as its accessible name, so a
// screen reader announces it when the dialog opens.
func TestDialogFramePanelRoleAndName(t *testing.T) {
	h := newDialogHarness(t, testConfirmButton())
	panel, ok := h.ui.AccessByRole(ui.RoleDialog, "Test Dialog")
	if !ok {
		t.Fatalf("no dialog panel named %q in the accessibility tree; texts=%q", "Test Dialog", h.ui.Texts())
	}
	if panel.Name != "Test Dialog" {
		t.Fatalf("panel name is %q, want the title %q", panel.Name, "Test Dialog")
	}
}

// Focus opens on the cancel button, the first focusable control of the
// footer, unless the primary button asks for it.
func TestDialogFrameFocus(t *testing.T) {
	h := newDialogHarness(t, testConfirmButton())
	cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel")
	if !ok {
		t.Fatalf("cancel button missing; texts=%q", h.ui.Texts())
	}
	if !cancel.Focused {
		t.Error("focus is not on the cancel button of a dialog without autofocus")
	}

	primary := testConfirmButton()
	primary.autoFocus = true
	h2 := newDialogHarness(t, primary)
	confirm, ok := h2.ui.AccessByRole(ui.RoleButton, "Confirm the Test")
	if !ok {
		t.Fatalf("primary button missing; texts=%q", h2.ui.Texts())
	}
	if !confirm.Focused {
		t.Error("focus is not on the primary button that asked for autofocus")
	}
}

// Escape dismisses the dialog: the frame reports the dismissal and the open
// flag is down.
func TestDialogFrameEscapeDismisses(t *testing.T) {
	h := newDialogHarness(t, testConfirmButton())
	h.ui.KeyDown(ui.KeyEscape)
	if !h.dismissed {
		t.Error("no frame reported the dismissal Escape caused")
	}
	if h.open {
		t.Error("the open flag is still up after Escape")
	}
	h.ui.KeyUp(ui.KeyEscape)
}

// The footer clicks report through the frame, and a disabled primary button
// does not.
func TestDialogFrameClicks(t *testing.T) {
	h := newDialogHarness(t, testConfirmButton())
	if err := h.ui.Click("Cancel"); err != nil {
		t.Fatalf("click cancel: %v", err)
	}
	if !h.cancelled {
		t.Error("clicking cancel was not reported")
	}
	if h.confirmed {
		t.Error("clicking cancel reported the primary button too")
	}

	h2 := newDialogHarness(t, testConfirmButton())
	if err := h2.ui.Click("Confirm"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if !h2.confirmed {
		t.Error("clicking the primary button was not reported")
	}

	primary := testConfirmButton()
	primary.disabled = true
	h3 := newDialogHarness(t, primary)
	if err := h3.ui.Click("Confirm"); err != nil {
		t.Fatalf("click disabled confirm: %v", err)
	}
	if h3.confirmed {
		t.Error("clicking the disabled primary button was reported as a confirmation")
	}
}

// A closed dialog builds nothing: no panel, no buttons, no body.
func TestDialogFrameClosedBuildsNothing(t *testing.T) {
	h := newDialogHarness(t, testConfirmButton())
	h.open = false
	h.ui.Frame()
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, ""); ok {
		t.Error("a dialog panel exists while the open flag is down")
	}
	if h.ui.HasText("body text") {
		t.Error("the body is showing while the open flag is down")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); ok {
		t.Error("the cancel button exists while the open flag is down")
	}
	if h.dismissed || h.cancelled || h.confirmed {
		t.Error("a closed frame reported a dismissal or a click")
	}
}
