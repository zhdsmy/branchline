package app

// Display and commit-message preferences helpers: the shared normalization,
// persistence and validation the Sync toolbar, the Blame view and the commit
// message rules consume. The settings windows call the save helpers; the
// views read through the normalization helpers.

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/zhdsmy/branchline/internal/store"
)

// The monospace code font sizes the views accept. A stored value outside the
// range — one an older or hand-edited store carries — clamps to it; an
// explicit zero means the views keep their built-in default.
const (
	minDiffFontSize = 10
	maxDiffFontSize = 24
)

// normalizedDiffFontSize is the code font size the monospace views render
// with: 0 keeps the view's built-in default, and any finite positive stored
// value clamps into the accepted range.
func normalizedDiffFontSize(prefs store.Settings) float32 {
	sz := prefs.DiffFontSize
	if sz <= 0 || math.IsNaN(sz) || math.IsInf(sz, 0) {
		return 0
	}
	if sz < minDiffFontSize {
		return minDiffFontSize
	}
	if sz > maxDiffFontSize {
		return maxDiffFontSize
	}
	return float32(sz)
}

// saveFetchTags persists whether Fetch brings every tag of the remotes. The
// store rolls its own failed write back.
func (a *app) saveFetchTags(on bool) error {
	cur := a.store.Settings()
	cur.FetchTags = on
	return a.store.SetSettings(cur)
}

// saveDiffFontSize persists the monospace code font size. A zero size restores
// the views' built-in default; other values must be finite and inside the
// accepted range, and an out-of-range or invalid size leaves the store
// untouched. The store rolls its own failed write back.
func (a *app) saveDiffFontSize(size float64) error {
	if size == 0 {
		cur := a.store.Settings()
		cur.DiffFontSize = 0
		return a.store.SetSettings(cur)
	}
	if math.IsNaN(size) || math.IsInf(size, 0) || size < minDiffFontSize || size > maxDiffFontSize {
		return fmt.Errorf("font size must be between %d and %d", minDiffFontSize, maxDiffFontSize)
	}
	cur := a.store.Settings()
	return a.store.SetDiffFont(cur.DiffFont, size)
}

// commitMessageWarning reads the commit message rules against the draft:
// the subject line is measured in Unicode characters against the hard limit
// first — which reports as severe — and then the soft one; the description
// lines are measured against the page guide. A limit of 0 turns its own
// check off. An empty draft warns about nothing.
func commitMessageWarning(message string, prefs store.Settings) (text string, severe bool) {
	if strings.TrimSpace(message) == "" {
		return "", false
	}
	subject, body, _ := strings.Cut(message, "\n")
	subject = strings.TrimRight(subject, "\r")
	n := utf8.RuneCountInString(subject)
	switch {
	case prefs.SubjectHigh > 0 && n > prefs.SubjectHigh:
		return fmt.Sprintf("Subject is %d characters, past the limit of %d.", n, prefs.SubjectHigh), true
	case prefs.SubjectLow > 0 && n > prefs.SubjectLow:
		return fmt.Sprintf("Subject is %d characters, past the soft limit of %d.", n, prefs.SubjectLow), false
	}
	if !prefs.NoPageGuide && prefs.PageGuide > 0 {
		for _, line := range strings.Split(body, "\n") {
			if utf8.RuneCountInString(strings.TrimRight(line, "\r")) > prefs.PageGuide {
				return fmt.Sprintf("A description line is past the %d character guide.", prefs.PageGuide), false
			}
		}
	}
	return "", false
}

// saveCommitPrefs validates and persists the commit message rules: the soft
// and hard subject limits, the page guide position, whether the guide is
// off, and whether commits sign off. A saved zero stays zero across restarts
// — the saved flags keep it apart from a store that never chose — and the
// store rolls its own failed write back.
func (a *app) saveCommitPrefs(subjectLow, subjectHigh, pageGuide int, noPageGuide, signedOffBy bool) error {
	if subjectLow < 0 {
		return fmt.Errorf("the soft subject limit must not be negative")
	}
	if subjectHigh < 0 {
		return fmt.Errorf("the hard subject limit must not be negative")
	}
	if subjectLow > 0 && subjectHigh > 0 && subjectLow > subjectHigh {
		return fmt.Errorf("the soft subject limit must not pass the hard one")
	}
	if !noPageGuide && pageGuide <= 0 {
		return fmt.Errorf("the page guide needs a positive column while it is on")
	}
	cur := a.store.Settings()
	cur.SubjectLow, cur.SubjectLowSaved = subjectLow, true
	cur.SubjectHigh, cur.SubjectHighSaved = subjectHigh, true
	cur.PageGuide = pageGuide
	cur.NoPageGuide = noPageGuide
	cur.SignedOffBy = signedOffBy
	return a.store.SetSettings(cur)
}
