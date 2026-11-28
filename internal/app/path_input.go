package app

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// edgeSpaceError reports a path input whose first or last character is
// whitespace, which unicode.IsSpace reads to include the no-break space.
// Such a path is almost never what the user meant — a trailing space slips
// in from a pasted or typed folder name — so the dialogs refuse it with the
// field's name rather than trimming it away and silently using a different
// path. A value of whitespace alone is not this check's problem: the submit
// buttons stay disabled for it. The message says "a space" for every kind
// of whitespace: a blank character is what the user sees either way.
func edgeSpaceError(input, field string) string {
	if input == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(input)
	last, _ := utf8.DecodeLastRuneInString(input)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return fmt.Sprintf("%s cannot begin or end with a space.", field)
	}
	return ""
}
