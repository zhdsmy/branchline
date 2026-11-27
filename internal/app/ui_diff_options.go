package app

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// defaultDiffContextLines is how many unchanged lines a diff shows around a
// change until the user chooses a count: git's own default.
const defaultDiffContextLines = 3

// entireFileContext is the context count entire-file diffs pass to git, as
// -U: large enough that one hunk covers any file the size cap lets through.
const entireFileContext = 1 << 20

// diffContextTiers are the context line counts the stepper buttons walk
// through: none, few, the default, and progressively wider readings of a
// change in its surroundings.
var diffContextTiers = []int{0, 1, 3, 5, 10, 25}

// diffGitContext maps a request's entire-file and context line choices to the
// context count of git.DiffOptions: a count covering the whole file, the
// chosen number of lines, or none for a zero choice, as git diff -U0.
func diffGitContext(req diffRequest) int {
	if req.EntireFile {
		return entireFileContext
	}
	if req.ContextLines == 0 {
		return -1
	}
	return req.ContextLines
}

// diffContextStep returns the tier next below or above cur. The bool is
// false at the ladder's ends, where the matching button disables. A count
// between tiers steps onto the nearest tier in the direction taken, so a
// store written by hand still lands back on the ladder.
func diffContextStep(cur int, up bool) (int, bool) {
	if up {
		for _, tier := range diffContextTiers {
			if tier > cur {
				return tier, true
			}
		}
		return cur, false
	}
	for i := len(diffContextTiers) - 1; i >= 0; i-- {
		if diffContextTiers[i] < cur {
			return diffContextTiers[i], true
		}
	}
	return cur, false
}

// The toolbar's icon buttons are small: the diff header's room at the
// minimum window width is nearly saturated once the other controls render,
// so each button is a symbol with its tooltip naming the action in full.
const (
	diffToolbarIconBtnWidth = 22
	diffToolbarIconBtnSize  = 12
	diffToolbarGroupGap     = 4
)

// diffToolbarGroupWidth is the width the four toolbar buttons take with the
// gaps between them, for the header's fit measurement.
func diffToolbarGroupWidth() float32 {
	return 4*diffToolbarIconBtnWidth + 3*diffToolbarGroupGap
}

// Original minimal geometric fallback SVGs for the diff toolbar, used where
// Apple SF Symbols are unavailable.
var (
	fallbackDiffEntire = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="2" width="9" height="12" rx="1.5"/><path d="M5.7 5h4.6M5.7 8h4.6M5.7 11h4.6"/></svg>`)
	fallbackDiffLess   = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8h9"/></svg>`)
	fallbackDiffMore   = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3.5v9M3.5 8h9"/></svg>`)
	fallbackDiffWrap   = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5h10M3 8h6.5a2.25 2.25 0 0 1 0 4.5H7.4"/><path d="M9.2 10.6L7.4 12.5l1.8 1.9"/></svg>`)
)

// diffToolBtn renders one of the diff toolbar's small icon buttons: a symbol
// with a fallback drawing, its accessible label and tooltip naming the action
// in full, and the selected wash while the toggle it represents is on.
func (d *diffPanel) diffToolBtn(c *ui.Context, key, label, tooltip, sfName string, fallback *ui.SVG, active, disabled bool, p palette) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Size(diffToolbarIconBtnWidth, 22).
		Center().
		Radius(5).
		Label(label).
		AccessSelected(active).
		Disabled(disabled).
		FocusRing(false)
	b.Tooltip(tooltip)
	bg := ui.Transparent
	switch {
	case active:
		bg = p.selected
	case !disabled && b.Hovered():
		bg = p.hover
	}
	b.Background(b.AnimateColor(colorAnimKey{"bg", p.gen}, bg, ui.Animation{Duration: 110 * time.Millisecond}))
	fg := p.muted
	switch {
	case disabled:
		fg = p.faint
	case active:
		fg = p.text
	}
	b.Children(func() {
		symbolIcon(c, sfName, fallback, diffToolbarIconBtnSize, fg)
	})
	return b
}

// diffOptionsGroup renders the diff toolbar's four choices together: the
// entire file, the context line count in both directions, and line wrapping.
// They are global for every diff panel, as the ignore-whitespace toggle is.
func (d *diffPanel) diffOptionsGroup(c *ui.Context, s *repoSession, scope string, p palette) {
	entire := s != nil && s.DiffEntireFile()
	wrap := s != nil && s.DiffWrap()
	cur := defaultDiffContextLines
	if s != nil {
		cur = s.DiffContextLines()
	}
	_, canLess := diffContextStep(cur, false)
	_, canMore := diffContextStep(cur, true)

	ui.Row(c.Key("diff-options")).
		Gap(diffToolbarGroupGap).
		AlignItems(ui.Center).
		Shrink(0).
		Children(func() {
			entireBtn := d.diffToolBtn(c, "diff-entire-file", "Show Entire File", "Show the entire file in diffs", "rectangle.expand.vertical", fallbackDiffEntire, entire, false, p)
			if entireBtn.Clicked() {
				d.toggleEntireFile(s, scope)
			}
			lessBtn := d.diffToolBtn(c, "diff-context-less", "Decrease Context Lines", "Show fewer unchanged lines around changes", "minus", fallbackDiffLess, false, entire || !canLess, p)
			if lessBtn.Clicked() {
				d.stepContextLines(s, scope, false)
			}
			moreBtn := d.diffToolBtn(c, "diff-context-more", "Increase Context Lines", "Show more unchanged lines around changes", "plus", fallbackDiffMore, false, entire || !canMore, p)
			if moreBtn.Clicked() {
				d.stepContextLines(s, scope, true)
			}
			wrapBtn := d.diffToolBtn(c, "diff-wrap-lines", "Wrap Lines", "Wrap long diff lines", "return", fallbackDiffWrap, wrap, false, p)
			if wrapBtn.Clicked() {
				d.toggleWrapLines(s)
			}
		})
}

// toggleEntireFile flips the session's entire-file choice and reloads the
// active diff with it. When the choice fails to persist, SetDiffChangesOnly
// leaves it unchanged and nothing is reloaded, as with the ignore-whitespace
// toggle.
func (d *diffPanel) toggleEntireFile(s *repoSession, scope string) {
	if s == nil {
		return
	}
	if err := s.SetDiffChangesOnly(s.DiffEntireFile()); err != nil {
		return
	}
	d.Load(s, scope, d.req)
}

// stepContextLines moves the session's context line count one tier in the
// given direction and reloads the active diff with it. At the ladder's ends,
// or when the choice fails to persist, nothing is reloaded.
func (d *diffPanel) stepContextLines(s *repoSession, scope string, up bool) {
	if s == nil {
		return
	}
	next, ok := diffContextStep(s.DiffContextLines(), up)
	if !ok {
		return
	}
	if err := s.SetDiffContextLines(next); err != nil {
		return
	}
	d.Load(s, scope, d.req)
}

// toggleWrapLines flips the session's line-wrapping choice. Wrapping is a
// display choice applied as the diff renders, so the diff is not reloaded:
// the open diff wraps or unwraps on the next frame. When the choice fails to
// persist it is left unchanged, as with the ignore-whitespace toggle.
func (d *diffPanel) toggleWrapLines(s *repoSession) {
	if s == nil {
		return
	}
	if err := s.SetDiffWrap(!s.DiffWrap()); err != nil {
		return
	}
}
