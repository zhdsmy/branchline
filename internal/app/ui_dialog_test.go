package app

import (
	"image"
	"image/color"
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

// lockedDialogHarness drives the same single-dialogFrame view as
// dialogHarness, with the cancel button's disabled condition bound to its
// lock: while a dialog's write keeps cancel disabled, the frame must refuse
// Escape and clicks outside with it.
type lockedDialogHarness struct {
	ui        *ui.Tester
	open      bool
	locked    bool
	dismissed bool
	cancelled bool
}

func newLockedDialogHarness(t *testing.T) *lockedDialogHarness {
	t.Helper()
	h := &lockedDialogHarness{open: true, locked: true}
	p := paletteForTheme(nil)
	h.ui = ui.NewTester(func(c *ui.Context) {
		dismissed, cancelled, _ := dialogFrame(c, p, "test-modal", &h.open, "Test Dialog", 460, func() {
			ui.Text(c, "body text")
		},
			dialogAction{key: "test-cancel", text: "Cancel", label: "Cancel", disabled: h.locked},
			testConfirmButton())
		h.dismissed = h.dismissed || dismissed
		h.cancelled = h.cancelled || cancelled
	}, 1200, 800)
	h.ui.Frame()
	return h
}

// While the cancel button is disabled — the dialog's write is running —
// Escape does not dismiss the dialog: the frame reports no dismissal, the
// open flag stays up and the panel with its name keeps showing. Releasing
// the lock lets Escape dismiss as always.
func TestDialogFrameLockedIgnoresEscape(t *testing.T) {
	h := newLockedDialogHarness(t)

	h.ui.Key(0, ui.KeyEscape)
	if h.dismissed {
		t.Error("a frame reported the dismissal Escape caused while locked")
	}
	if !h.open {
		t.Error("the open flag is down after Escape while locked")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Test Dialog"); !ok {
		t.Errorf("the dialog panel is gone after Escape while locked; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("body text") {
		t.Errorf("the body is gone after Escape while locked; texts=%q", h.ui.Texts())
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok {
		t.Error("the cancel button is gone after Escape while locked")
	} else if !cancel.Disabled {
		t.Error("the cancel button of the locked harness is enabled")
	}

	h.locked = false
	h.ui.Frame()
	h.ui.Key(0, ui.KeyEscape)
	if !h.dismissed {
		t.Error("no frame reported the dismissal Escape caused once unlocked")
	}
	if h.open {
		t.Error("the open flag is still up after Escape once unlocked")
	}
}

// While locked, a click outside the panel does not dismiss the dialog
// either; unlocked, it does.
func TestDialogFrameLockedIgnoresClickOutside(t *testing.T) {
	h := newLockedDialogHarness(t)

	h.ui.ClickAt(10, 10)
	if h.dismissed {
		t.Error("a frame reported the dismissal the outside click caused while locked")
	}
	if !h.open {
		t.Error("the open flag is down after an outside click while locked")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Test Dialog"); !ok {
		t.Errorf("the dialog panel is gone after an outside click while locked; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("body text") {
		t.Errorf("the body is gone after an outside click while locked; texts=%q", h.ui.Texts())
	}

	h.locked = false
	h.ui.Frame()
	h.ui.ClickAt(10, 10)
	if !h.dismissed {
		t.Error("no frame reported the dismissal the outside click caused once unlocked")
	}
	if h.open {
		t.Error("the open flag is still up after an outside click once unlocked")
	}
}

// enterDialogHarness drives a single dialogFrame with a checkbox in its
// body, so tests can move the keyboard focus onto a control that takes
// neither Enter nor a click away from the footer. confirmCount counts the
// frames that reported a confirmation rather than latching one, because a
// double-delivery regression only shows in a count.
type enterDialogHarness struct {
	ui           *ui.Tester
	open         bool
	checked      bool
	primary      dialogAction
	confirmCount int
	cancelled    bool
	dismissed    bool
}

func newEnterDialogHarness(t *testing.T, primary dialogAction) *enterDialogHarness {
	t.Helper()
	h := &enterDialogHarness{open: true, primary: primary}
	p := paletteForTheme(nil)
	h.ui = ui.NewTester(func(c *ui.Context) {
		dismissed, cancelled, confirmed := dialogFrame(c, p, "test-modal", &h.open, "Test Dialog", 460, func() {
			ui.Text(c, "body text")
			ui.Checkbox(c.Key("test-checkbox"), &h.checked, "Test Option").Label("Test Option")
		},
			dialogAction{key: "test-cancel", text: "Cancel", label: "Cancel"},
			h.primary)
		h.dismissed = h.dismissed || dismissed
		h.cancelled = h.cancelled || cancelled
		if confirmed {
			h.confirmCount++
		}
	}, 1200, 800)
	h.ui.Frame()
	return h
}

// focusTestCheckbox clicks the checkbox and verifies it took the focus, so
// an Enter test is really measuring the key with the focus off the footer.
func focusTestCheckbox(t *testing.T, h *enterDialogHarness) {
	t.Helper()
	if err := h.ui.Click("Test Option"); err != nil {
		t.Fatalf("click checkbox: %v", err)
	}
	if box, ok := h.ui.AccessByRole(ui.RoleCheckBox, "Test Option"); !ok || !box.Focused {
		t.Fatalf("checkbox not focused after its click: %+v found=%v", box, ok)
	}
}

// Enter confirms a non-destructive dialog: with the focus on the autofocused
// primary button the button itself takes the key, and with the focus on a
// checkbox — which takes Space only — the frame's window-level shortcut
// confirms without touching the checkbox.
func TestDialogFrameEnterConfirms(t *testing.T) {
	primary := testConfirmButton()
	primary.autoFocus = true
	h := newEnterDialogHarness(t, primary)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm the Test"); !ok || !btn.Focused {
		t.Fatalf("primary button not focused at open: %+v found=%v", btn, ok)
	}
	h.ui.Key(0, ui.KeyEnter)
	if h.confirmCount == 0 {
		t.Error("Enter with the focus on the primary button did not confirm")
	}

	h2 := newEnterDialogHarness(t, testConfirmButton())
	focusTestCheckbox(t, h2)
	h2.ui.Key(0, ui.KeyEnter)
	if h2.confirmCount == 0 {
		t.Error("Enter with the focus on a checkbox did not confirm")
	}
	if h2.confirmCount > 1 {
		t.Errorf("one Enter press confirmed %d times, want one", h2.confirmCount)
	}
	if !h2.checked {
		t.Error("Enter toggled the focused checkbox in passing")
	}
	if h2.cancelled || h2.dismissed {
		t.Error("Enter canceled or dismissed the non-destructive dialog")
	}
}

// While the primary button is disabled Enter does nothing at all: no
// confirmation, no toggle of the focused checkbox, the dialog stays open.
func TestDialogFrameEnterDisabledDoesNothing(t *testing.T) {
	primary := testConfirmButton()
	primary.disabled = true
	h := newEnterDialogHarness(t, primary)
	focusTestCheckbox(t, h)
	h.ui.Key(0, ui.KeyEnter)
	if h.confirmCount != 0 {
		t.Error("Enter confirmed while the primary button is disabled")
	}
	if !h.checked {
		t.Error("Enter toggled the checkbox while the primary button is disabled")
	}
	if h.cancelled || h.dismissed || !h.open {
		t.Error("Enter canceled or dismissed the dialog while the primary button is disabled")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Test Dialog"); !ok {
		t.Error("the dialog panel is gone after Enter with the primary disabled")
	}
}

// A destructive dialog asks the window for no Enter shortcut: with the focus
// cleared off every button the key goes nowhere, and the dialog stays open
// and unconfirmed.
func TestDialogFrameDestructiveEnterDoesNothing(t *testing.T) {
	primary := testConfirmButton()
	primary.destructive = true
	h := newEnterDialogHarness(t, primary)

	// Clicking the body text moves the focus off cancel — which opens with
	// it — onto nothing, so the only consumer Enter could find would be the
	// window-level shortcut a destructive frame must not register.
	if err := h.ui.Click("body text"); err != nil {
		t.Fatalf("click body text: %v", err)
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); ok && cancel.Focused {
		t.Fatal("focus is still on cancel after clicking the body")
	}
	h.ui.Key(0, ui.KeyEnter)
	if h.confirmCount != 0 {
		t.Error("Enter confirmed the destructive dialog")
	}
	if h.cancelled || h.dismissed || !h.open {
		t.Error("Enter canceled or dismissed the destructive dialog")
	}
	if _, ok := h.ui.AccessByRole(ui.RoleDialog, "Test Dialog"); !ok {
		t.Error("the dialog panel is gone after Enter on the destructive dialog")
	}
}

// autoFocus on a destructive primary is ignored: the focus opens on the
// body's first focusable control — cancel, in a dialog of plain text — where
// Enter cancels, the safe key of a dialog whose primary discards work.
func TestDialogFrameDestructiveNoAutoFocus(t *testing.T) {
	primary := testConfirmButton()
	primary.autoFocus = true
	primary.destructive = true
	h := newDialogHarness(t, primary)
	if btn, ok := h.ui.AccessByRole(ui.RoleButton, "Confirm the Test"); !ok {
		t.Fatalf("primary button missing; texts=%q", h.ui.Texts())
	} else if btn.Focused {
		t.Error("the destructive primary button took the focus despite autoFocus being ignored")
	}
	if cancel, ok := h.ui.AccessByRole(ui.RoleButton, "Cancel"); !ok {
		t.Fatalf("cancel button missing; texts=%q", h.ui.Texts())
	} else if !cancel.Focused {
		t.Error("focus is not on cancel at the destructive dialog's open")
	}

	h.ui.Key(0, ui.KeyEnter)
	if h.confirmed {
		t.Error("Enter on the focused cancel of a destructive dialog confirmed")
	}
	if !h.cancelled {
		t.Error("Enter on the focused cancel was not the cancel click it must be")
	}
}

// rgbaOf converts a ui.Color to the color.RGBA the pixel helpers compare.
func rgbaOf(c ui.Color) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

// buttonFillSample reads the dominant color of the middle band of the button
// named label: insets leave out the border, the rounded corners and most of
// the glyph pixels. The tester renders at scale 1, so DIPs are pixels.
func buttonFillSample(t *testing.T, tt *ui.Tester, label string) color.RGBA {
	t.Helper()
	btn, ok := tt.AccessByRole(ui.RoleButton, label)
	if !ok {
		t.Fatalf("button %q missing; texts=%q", label, tt.Texts())
	}
	b := btn.Bounds
	px := image.Rect(int(b.X+b.W*0.25), int(b.Y+6), int(b.X+b.W*0.75), int(b.Y+b.H-6))
	return dominantColor(tt.Image(), px)
}

// fillWithin reports whether the sampled fill is within tol of want per
// channel, the tolerance covering the rasterizer's rounding of the flat fill.
func fillWithin(fill color.RGBA, want ui.Color, tol uint8) bool {
	return maxChannelDiff(fill, rgbaOf(want)) <= tol
}

// The destructive primary paints in the theme's Danger color at rest, over
// the pointer and pressed, in the light and the dark appearance alike — the
// theme copy of WithAccent(Danger) derives its hover and pressed shades —
// while the ordinary primary keeps the accent.
func TestDialogFrameDestructivePrimaryPaintsDanger(t *testing.T) {
	for _, dark := range []bool{false, true} {
		primary := testConfirmButton()
		primary.destructive = true
		var themeDanger, themeAccent ui.Color
		p := paletteForTheme(nil)
		open := true
		tt := ui.NewTester(func(c *ui.Context) {
			th := c.Theme()
			themeDanger, themeAccent = th.Danger, th.Accent
			dialogFrame(c, p, "test-modal", &open, "Test Dialog", 460, func() {
				ui.Text(c, "body text")
			},
				dialogAction{key: "test-cancel", text: "Cancel", label: "Cancel"},
				primary)
		}, 1200, 800)
		tt.SetDark(dark)
		tt.Frame()

		btn, ok := tt.AccessByRole(ui.RoleButton, "Confirm the Test")
		if !ok {
			t.Fatalf("dark=%v: primary button missing; texts=%q", dark, tt.Texts())
		}
		cx := btn.Bounds.X + btn.Bounds.W/2
		cy := btn.Bounds.Y + btn.Bounds.H/2

		const tol = 12
		rest := buttonFillSample(t, tt, "Confirm the Test")
		if !fillWithin(rest, themeDanger, tol) {
			t.Errorf("dark=%v: resting fill = %v, want within %d of the theme danger %v", dark, rest, tol, themeDanger)
		}
		if fillWithin(rest, themeAccent, tol) {
			t.Errorf("dark=%v: resting fill %v is the plain accent %v, not the danger color", dark, rest, themeAccent)
		}

		tt.Move(cx, cy)
		tt.Frame()
		hover := buttonFillSample(t, tt, "Confirm the Test")
		if maxChannelDiff(hover, rest) <= tol {
			t.Errorf("dark=%v: hover fill %v does not move off the resting fill %v", dark, hover, rest)
		}
		if fillWithin(hover, themeAccent, tol) {
			t.Errorf("dark=%v: hover fill %v is the plain accent %v, not a danger shade", dark, hover, themeAccent)
		}

		tt.Press(cx, cy)
		tt.Frame()
		pressed := buttonFillSample(t, tt, "Confirm the Test")
		tt.Release(cx, cy)
		tt.Frame()
		if maxChannelDiff(pressed, rest) <= tol {
			t.Errorf("dark=%v: pressed fill %v does not move off the resting fill %v", dark, pressed, rest)
		}
		if maxChannelDiff(pressed, hover) <= tol {
			t.Errorf("dark=%v: pressed fill %v matches the hover fill %v, so neither shade comes from WithAccent", dark, pressed, hover)
		}
		if fillWithin(pressed, themeAccent, tol) {
			t.Errorf("dark=%v: pressed fill %v is the plain accent %v, not a danger shade", dark, pressed, themeAccent)
		}

		// The release lands a click on the destructive primary: it confirms,
		// which no assertion here minds — the harness is spent either way.
		tt.Move(10, 10)
		tt.Frame()
	}

	// An ordinary primary keeps the accent, the control half of the contrast.
	h := newEnterDialogHarness(t, testConfirmButton())
	plain := buttonFillSample(t, h.ui, "Confirm the Test")
	var accent ui.Color
	tt2 := ui.NewTester(func(c *ui.Context) {
		accent = c.Theme().Accent
	}, 100, 60)
	tt2.Frame()
	if !fillWithin(plain, accent, 12) {
		t.Errorf("non-destructive fill %v is not within 12 of the accent %v; the harness window may be inactive", plain, accent)
	}
}
