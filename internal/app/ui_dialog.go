package app

import (
	"github.com/egoist/mygo/ui"
)

// dialogAction is one footer button: its element key, visible text and
// accessible label. destructive marks a primary button that discards work —
// a reset, drop or abort — and is meaningless on the cancel button.
type dialogAction struct {
	key, text, label    string
	disabled, autoFocus bool
	destructive         bool
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
func dialogFrame(c *ui.Context, p palette, key string, open *bool, title string, width float32, body func(), cancel, primary dialogAction) (dismissed, cancelled, confirmed bool) {
	if !*open {
		return false, false, false
	}
	dismissed = ui.Modal(c.Key(key), open, func() {
		ui.Column(c.Key(key+"-body")).Width(width).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(650).TextColor(p.text)
			if body != nil {
				body()
			}
			if !primary.destructive {
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
				if cancelEl.Clicked() {
					cancelled = true
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
