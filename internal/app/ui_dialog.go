package app

import (
	"github.com/egoist/mygo/ui"
)

// dialogAction is one footer button: its element key, visible text and
// accessible label.
type dialogAction struct {
	key, text, label    string
	disabled, autoFocus bool
}

// dialogFrame shows a dialog in the app's single look while *open is true:
// the title, then body, then the footer with cancel on the left and the
// primary button on the right. The panel is named after the title. It
// reports whether this frame dismissed the dialog (Escape or a click
// outside), and whether cancel or the primary button was clicked.
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
			ui.Row(c.Key(key + "-actions")).Gap(8).Justify(ui.End).Children(func() {
				cancelEl := ui.Button(c.Key(cancel.key), cancel.text).Label(cancel.label).Disabled(cancel.disabled)
				if cancelEl.Clicked() {
					cancelled = true
				}
				primaryEl := ui.PrimaryButton(c.Key(primary.key), primary.text).Label(primary.label).Disabled(primary.disabled)
				if primary.autoFocus {
					primaryEl = primaryEl.AutoFocus()
				}
				if primaryEl.Clicked() {
					confirmed = true
				}
			})
		})
	}).Label(title).Dismissed()
	return dismissed, cancelled, confirmed
}
