package app

import (
	"github.com/egoist/mygo/ui"
)

// framedDialogs counts the dialogs dialogFrame drew in the frame of the
// main window currently building. The main window's view resets it to zero
// before the frame builds and reads it once the frame is built, so it
// answers the question mygo's native side cannot answer for the menu bar:
// whether the main window shows a dialog at all. Only the UI thread ever
// builds a frame, so the count needs no lock, and only the dialogs built
// inside the main window's reset-and-read span count: the Settings window
// builds its view in its own window outside that span, so a dialog open
// there never reaches the menu gate of the main window. Dialogs still
// calling ui.Modal directly do not count either, until they move to the
// frame. The same package-level shape already carries cloneUIStates.
var framedDialogs int

// dialogAction is one footer button: its element key, visible text and
// accessible label. destructive marks a primary button that discards work —
// a reset, drop or abort — and is meaningless on the cancel button. onClick
// belongs to the extra buttons alone: it is what clicking one runs, and the
// frame reads it of no other button.
type dialogAction struct {
	key, text, label    string
	disabled, autoFocus bool
	destructive         bool
	onClick             func()
}

// dialogFrame shows a dialog in the app's single look while *open is true:
// the title, then body, then the footer with cancel on the left and the
// primary button on the right. The panel is named after the title. It
// reports whether this frame dismissed the dialog (Escape or a click
// outside), and whether cancel or the primary button was clicked.
//
// While cancel is disabled the dialog cannot leave at all: Escape and a
// click outside are refused with it, so a dialog locking its cancel button
// for a write in progress keeps every way out shut for as long as the write
// runs — dismissing it would strand the user with the operation's outcome
// nowhere visible. The modal still computes its dismissal inside, but the
// frame builds the panel again either way, so holding the open flag up
// costs no flicker.
//
// Enter is the primary button of a non-destructive dialog: pressing it
// anywhere the dialog's own controls do not take the key first — a focused
// button or link takes Enter itself, a text field inserts its line break,
// and a drop-down opens or chooses — confirms exactly like the click, so
// long as the primary button is enabled. With the button disabled Enter
// does nothing at all; the window-level claim stands either way, so the
// key never toggles a focused checkbox or radio in passing. Destructive
// dialogs ask for no such shortcut: their primary button is drawn in the
// theme's Danger accent, and Enter never arms it. The shortcut is also why
// a dialog without a text field must not open with the focus on cancel —
// Enter there would cancel before the window sees the key — so the callers
// autofocus the primary button of every non-destructive one, except where
// the body has a text field that must keep it. autoFocus on a destructive
// primary is silently ignored, for the same reason: a focused button takes
// Enter before every shortcut, and the focus ring of a destructive dialog
// belongs on cancel, the first focusable control of its footer.
//
// A primary action with no key is a dialog with one button, the cancel's:
// the footer draws it alone, the frame autofocuses it itself so Enter and
// Space press it — Enter is nobody else's in such a dialog, the frame
// registers no shortcut and confirms nothing — and a disabled one still
// locks Escape and the click outside.
//
// Extra actions sit between cancel and the primary button in the order they
// are given, drawn as plain buttons, each answering its click by calling
// its onClick. They are no shortcut of the dialog's: Enter never presses
// one, and destructive and autoFocus mean nothing on them.
func dialogFrame(c *ui.Context, p palette, key string, open *bool, title string, width float32, body func(), cancel, primary dialogAction, extra ...dialogAction) (dismissed, cancelled, confirmed bool) {
	if !*open {
		return false, false, false
	}
	framedDialogs++
	single := primary.key == ""
	dismissed = ui.Modal(c.Key(key), open, func() {
		ui.Column(c.Key(key+"-body")).Width(width).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			if body != nil {
				body()
			}
			if !primary.destructive && !single {
				// Register even while the primary button is disabled: the
				// registration alone keeps Enter out of a focused toggle,
				// and the consumption here, not the registration, decides
				// whether the press confirms.
				if c.Shortcut(0, ui.KeyEnter) && !primary.disabled {
					confirmed = true
				}
			}
			ui.Row(c.Key(key + "-actions")).Gap(8).Justify(ui.End).Children(func() {
				cancelEl := ui.Button(c.Key(cancel.key), cancel.text).Label(cancel.label).Disabled(cancel.disabled)
				if single {
					// The one button of the dialog is where its focus ring
					// belongs: with no primary button to hold it, cancel takes
					// it so Enter and Space press the button itself.
					cancelEl = cancelEl.AutoFocus()
				}
				if cancelEl.Clicked() {
					cancelled = true
				}
				for _, x := range extra {
					if ui.Button(c.Key(x.key), x.text).Label(x.label).Disabled(x.disabled).Clicked() && !x.disabled && x.onClick != nil {
						x.onClick()
					}
				}
				if single {
					return
				}
				buildPrimary := func() ui.Element {
					primaryEl := ui.PrimaryButton(c.Key(primary.key), primary.text).Label(primary.label).Disabled(primary.disabled)
					if primary.autoFocus && !primary.destructive {
						primaryEl = primaryEl.AutoFocus()
					}
					return primaryEl
				}
				var primaryEl ui.Element
				if primary.destructive {
					// The danger accent replaces the window accent for this
					// one button, Focus excepted: the ring marks the focused
					// control, not the operation, so it keeps the color every
					// other button's ring has. The wrapper carries no name and
					// no spacing of its own, so the footer lays out exactly as
					// before.
					theme := c.Theme()
					danger := theme.WithAccent(theme.Danger)
					danger.Focus = theme.Focus
					ui.Themed(c.Key(primary.key+"-danger"), danger).Children(func() {
						primaryEl = buildPrimary()
					})
				} else {
					primaryEl = buildPrimary()
				}
				if primaryEl.Clicked() {
					confirmed = true
				}
			})
		})
	}).Label(title).Dismissed()
	if dismissed && cancel.disabled {
		*open = true
		dismissed = false
	}
	return dismissed, cancelled, confirmed
}
