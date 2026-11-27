package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/sfsymbols"
	"github.com/egoist/mygo/ui"
)

// palette defines the light and dark visual tokens used across the shell and
// feature pages. Both variants follow the system theme with a shared cobalt
// accent hue.
type palette struct {
	dark     bool
	gen      int
	bg       ui.Color
	sidebar  ui.Color
	surface  ui.Color
	text     ui.Color
	muted    ui.Color
	faint    ui.Color
	hairline ui.Color
	border   ui.Color
	hover    ui.Color
	selected ui.Color
	press    ui.Color
	accent   ui.Color
	onAccent ui.Color
	// listSelected is the filled background of the chosen row of the commit
	// list, aligned with the macOS selectedContentBackgroundColor blue; it is
	// stronger than the neutral selected wash the other rows use because the
	// list paints it through the theme accent.
	listSelected ui.Color
	// onAccentMuted is the secondary text color on a listSelected row:
	// authors, short hashes and dates, kept readable but below the pure-white
	// primary text.
	onAccentMuted ui.Color
	danger        ui.Color
	popoverBg     ui.Color
	popoverEdge   ui.Color
}

// colorAnimKey scopes AnimateColor transitions to the current theme polarity
// generation so switching between light and dark appearance snaps cleanly to
// the new palette instead of blending from the previous theme's alpha tint.
type colorAnimKey struct {
	name string
	gen  int
}

func paletteForTheme(t *ui.Theme) palette {
	if t != nil && t.Dark {
		return palette{
			dark:          true,
			bg:            ui.Hex("#151619"),
			sidebar:       ui.Hex("#1c1d22"),
			surface:       ui.Hex("#202229"),
			text:          ui.Hex("#f1f3f8"),
			muted:         ui.RGBA(224, 229, 240, 0.64),
			faint:         ui.RGBA(224, 229, 240, 0.36),
			hairline:      ui.RGBA(255, 255, 255, 0.10),
			border:        ui.Hex("#363842"),
			hover:         ui.RGBA(255, 255, 255, 0.09),
			selected:      ui.RGBA(255, 255, 255, 0.16),
			press:         ui.RGBA(255, 255, 255, 0.21),
			accent:        ui.Oklch(0.68, 0.15, 252),
			onAccent:      ui.Hex("#ffffff"),
			listSelected:  ui.Oklch(0.52, 0.16, 255),
			onAccentMuted: ui.RGBA(255, 255, 255, 0.88),
			danger:        ui.Hex("#f06b6f"),
			popoverBg:     ui.Hex("#24262d"),
			popoverEdge:   ui.RGBA(255, 255, 255, 0.14),
		}
	}
	return palette{
		bg:            ui.Hex("#fbfbfc"),
		sidebar:       ui.Hex("#f1f2f5"),
		surface:       ui.Hex("#ffffff"),
		text:          ui.Hex("#18191d"),
		muted:         ui.RGBA(52, 56, 66, 0.66),
		faint:         ui.RGBA(52, 56, 66, 0.36),
		hairline:      ui.RGBA(15, 18, 28, 0.08),
		border:        ui.Hex("#d8dae2"),
		hover:         ui.RGBA(15, 18, 28, 0.045),
		selected:      ui.RGBA(15, 18, 28, 0.09),
		press:         ui.RGBA(15, 18, 28, 0.12),
		accent:        ui.Oklch(0.56, 0.17, 256),
		onAccent:      ui.Hex("#ffffff"),
		listSelected:  ui.Oklch(0.53, 0.17, 256),
		onAccentMuted: ui.RGBA(255, 255, 255, 0.88),
		danger:        ui.Hex("#d13438"),
		popoverBg:     ui.Hex("#ffffff"),
		popoverEdge:   ui.RGBA(15, 18, 28, 0.12),
	}
}

func applyPalette(c *ui.Context, gen int) palette {
	t := *c.Theme()
	p := paletteForTheme(&t)
	p.gen = gen
	black, white := ui.Hex("#000000"), ui.Hex("#ffffff")
	t.Background = p.bg
	t.Surface = p.surface
	t.SurfaceHover = p.hover.Over(p.surface)
	t.SurfacePressed = p.press.Over(p.surface)
	t.Border = p.border
	t.Text = p.text
	t.TextMuted = p.muted.Over(p.bg)
	t.Danger = p.danger
	t.Accent, t.AccentText = p.accent, p.onAccent
	if p.dark {
		t.AccentHover = p.accent.MixOklab(white, 0.14)
		t.AccentPressed = p.accent.MixOklab(black, 0.14)
	} else {
		t.AccentHover = p.accent.MixOklab(black, 0.10)
		t.AccentPressed = p.accent.MixOklab(black, 0.20)
	}
	t.Selection = p.accent.Alpha(0.26)
	c.SetTheme(&t)
	return p
}

// Original minimal geometric fallback SVGs used when running on systems
// without Apple SF Symbols (or headless renderers lacking CoreGlyphs).
var (
	fallbackBack    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M10 3.5L5.5 8 10 12.5"/></svg>`)
	fallbackFwd     = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3.5L10.5 8 6 12.5"/></svg>`)
	fallbackDown    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6l4 4 4-4"/></svg>`)
	fallbackChev    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M5 6.5L8 3.5l3 3M5 9.5L8 12.5l3-3"/></svg>`)
	fallbackDiff    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2.5" y="2.5" width="11" height="11" rx="2"/><path d="M8 5.2v5.6M5.2 8h5.6"/></svg>`)
	fallbackHist    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="5.5"/><path d="M8 4.8V8l2.2 1.6"/></svg>`)
	fallbackBr      = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="4.5" cy="4" r="1.5"/><circle cx="11.5" cy="5.5" r="1.5"/><circle cx="4.5" cy="12" r="1.5"/><path d="M4.5 5.5v5M11.5 7c0 2-2 3-7 3"/></svg>`)
	fallbackRem     = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="5.5"/><path d="M2.8 8h10.4M8 2.5c1.7 1.6 1.7 9.4 0 11M8 2.5c-1.7 1.6-1.7 9.4 0 11"/></svg>`)
	fallbackTag     = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 8.2V3h5.2l4.8 4.8-5.2 5.2L3 8.2z"/><circle cx="5.8" cy="5.8" r="0.9" fill="currentColor" stroke="none"/></svg>`)
	fallbackRepo    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 4.2h4l1.3 1.4h5.7v6.6H2.5V4.2z"/></svg>`)
	fallbackSSH     = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2.5" y="3" width="11" height="4" rx="1"/><rect x="2.5" y="9" width="11" height="4" rx="1"/><circle cx="5" cy="5" r="0.7" fill="currentColor" stroke="none"/><circle cx="5" cy="11" r="0.7" fill="currentColor" stroke="none"/></svg>`)
	fallbackChk     = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8.5L6.5 11.5 12.5 4.5"/></svg>`)
	fallbackDownArr = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v9M4.8 8.8L8 12l3.2-3.2"/></svg>`)
	fallbackUpArr   = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V4M4.8 7.2L8 4l3.2 3.2"/></svg>`)
	fallbackSync    = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12.5 8a4.5 4.5 0 1 1-1.3-3.2M12.5 3.2v2.4h-2.4"/></svg>`)
)

func mustSVG(raw string) *ui.SVG {
	svg, err := ui.ParseSVG([]byte(raw))
	if err != nil {
		panic(err)
	}
	return svg
}

// symbolIconDrawOverride, when non-nil in tests, overrides how symbolIcon
// paints SF Symbols so screenshot regression tests can pin backing scale
// independently of the host main display. Always nil in production builds.
var symbolIconDrawOverride func(c *ui.Context, sfName string, fallback *ui.SVG, size float32, color ui.Color)

func symbolIcon(c *ui.Context, sfName string, fallback *ui.SVG, size float32, color ui.Color) {
	if symbolIconDrawOverride != nil {
		symbolIconDrawOverride(c, sfName, fallback, size, color)
		return
	}
	sfsymbols.Symbol(c, sfName).
		Size(size).
		Color(color).
		Weight(sfsymbols.Medium).
		Fallback(fallback)
}

func iconBtn(c *ui.Context, sfName string, fallback *ui.SVG, size float32, color ui.Color, label string, p palette) ui.Element {
	b := ui.ButtonBase(c).
		Size(size+12, size+12).
		Center().
		Radius(7).
		Label(label).
		Tooltip(label).
		FocusRing(false)
	bg := ui.Transparent
	if b.Pressed() {
		bg = p.press
	} else if b.Hovered() {
		bg = p.hover
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	b.Children(func() {
		symbolIcon(c, sfName, fallback, size, color)
	})
	return b
}

func toolbarActionBtn(c *ui.Context, sfName string, fallback *ui.SVG, label string, p palette, disabled bool) ui.Element {
	return toolbarActionBtnTip(c, sfName, fallback, label, label, p, disabled)
}

// toolbarActionBtnTip is toolbarActionBtn with a tooltip other than the
// label. Tooltip adds a tip on each call rather than replacing the last, so
// a button that needs its own tip sets it here instead of calling Tooltip
// again on the returned element.
func toolbarActionBtnTip(c *ui.Context, sfName string, fallback *ui.SVG, label, tooltip string, p palette, disabled bool) ui.Element {
	b := ui.ButtonBase(c).
		Height(28).
		PaddingX(9).
		Gap(6).
		Radius(7).
		AlignItems(ui.Center).
		Label(label).
		Tooltip(tooltip).
		Disabled(disabled).
		FocusRing(false)
	bg := ui.Transparent
	if !disabled {
		if b.Pressed() {
			bg = p.press
		} else if b.Hovered() {
			bg = p.hover
		}
	}
	fg := p.text
	if disabled {
		fg = p.faint
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	b.Children(func() {
		symbolIcon(c, sfName, fallback, 13.5, fg)
		ui.Text(c, label).FontSize(12.5).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b
}

func sidebarSectionHeader(c *ui.Context, title string, count int, collapsed *bool, p palette) ui.Element {
	b := ui.ButtonBase(c.Key("sec-" + title)).
		Height(26).
		PaddingX(8).
		Gap(5).
		Radius(6).
		AlignItems(ui.Center).
		Justify(ui.Start).
		Label(title).
		Expanded(!*collapsed).
		FocusRing(false)
	if b.Hovered() {
		b.Background(p.hover)
	}
	iconName, fb := "chevron.down", fallbackDown
	if *collapsed {
		iconName, fb = "chevron.right", fallbackFwd
	}
	b.Children(func() {
		symbolIcon(c, iconName, fb, 10, p.faint)
		ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(p.muted).SingleLine()
		ui.Spacer(c)
		if count > 0 {
			ui.Text(c, fmt.Sprint(count)).FontSize(11).TextColor(p.faint).SingleLine()
		}
	})
	if b.Clicked() {
		*collapsed = !*collapsed
	}
	return b
}

func placeholderCard(c *ui.Context, title, subtitle, detail string, p palette) {
	ui.Column(c).
		Grow(1).
		Center().
		Padding(24).
		Gap(8).
		Children(func() {
			ui.Text(c, title).FontSize(18).FontWeight(600).TextColor(p.text).TextAlign(ui.Center)
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(13).TextColor(p.muted).TextAlign(ui.Center).MaxWidth(480)
			}
			if detail != "" {
				ui.Text(c, detail).FontSize(12).TextColor(p.faint).TextAlign(ui.Center).MaxWidth(480)
			}
		})
}
