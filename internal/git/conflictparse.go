package git

import "strings"

// ConflictTypeLabel is the human name of a two-letter unmerged status:
// which side of the merge added, modified or deleted the file.
func ConflictTypeLabel(code string) string {
	switch code {
	case "UU":
		return "Both Modified"
	case "AA":
		return "Both Added"
	case "DD":
		return "Both Deleted"
	case "DU":
		return "Deleted by Us"
	case "UD":
		return "Deleted by Them"
	case "AU":
		return "Added by Us"
	case "UA":
		return "Added by Them"
	}
	return "Unmerged"
}

// ConflictHunk is one conflict marker block of a file, as git writes it
// with <<<<<<<, ======= and >>>>>>>, and a diff3 base after |||||||.
type ConflictHunk struct {
	// Start, Mid and End are 0-based line numbers of the opening marker,
	// the separator, and half-open: the line after the closing marker.
	// BaseStart is the line of the diff3 base marker, or -1 without one.
	Start, BaseStart, Mid, End int
	// OursLabel, BaseLabel and TheirsLabel are the labels of the marker
	// lines, such as HEAD or the merged branch.
	OursLabel, BaseLabel, TheirsLabel string
	// Ours, Base and Theirs are the exact lines of each side, line
	// endings included. Base is empty without a diff3 section.
	Ours, Base, Theirs string
}

// ConflictChoice says which sides of a hunk the result keeps. Neither
// leaves the markers in place; both keeps ours and then theirs.
type ConflictChoice struct{ Ours, Theirs bool }

// conflictLine is a line of a conflicted file: its text without the
// ending, and the ending itself, "" for a last line without one.
type conflictLine struct {
	text string
	term string
}

// splitConflictLines splits text into lines that keep their endings,
// so composing a file preserves its LF or CRLF style exactly.
func splitConflictLines(text string) []conflictLine {
	var lines []conflictLine
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, conflictLine{text: text})
			break
		}
		line, term := text[:i], "\n"
		if strings.HasSuffix(line, "\r") {
			line, term = strings.TrimSuffix(line, "\r"), "\r\n"
		}
		lines = append(lines, conflictLine{text: line, term: term})
		text = text[i+1:]
	}
	return lines
}

func joinConflictLines(lines []conflictLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.term)
	}
	return b.String()
}

func conflictLabel(line, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

// ParseConflictHunks finds the complete conflict marker blocks of text.
// A diff3 base section belongs to neither side. A block missing its
// separator or its closing marker is not a hunk.
func ParseConflictHunks(text string) []ConflictHunk {
	lines := splitConflictLines(text)
	var hunks []ConflictHunk
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i].text, "<<<<<<<") {
			continue
		}
		base, mid, end := -1, -1, -1
		for j := i + 1; j < len(lines); j++ {
			l := lines[j].text
			switch {
			case strings.HasPrefix(l, "<<<<<<<"):
				// A new opening marker before this block closed leaves
				// both blocks broken.
				j = len(lines)
			case strings.HasPrefix(l, "|||||||") && mid < 0 && base < 0:
				base = j
			case l == "=======" && mid < 0:
				mid = j
			case strings.HasPrefix(l, ">>>>>>>") && mid >= 0:
				end = j
			}
			if end >= 0 {
				break
			}
		}
		if mid < 0 || end < 0 {
			continue
		}
		oursEnd := mid
		baseLabel := ""
		if base >= 0 {
			oursEnd = base
			baseLabel = conflictLabel(lines[base].text, "|||||||")
		}
		h := ConflictHunk{
			Start:       i,
			BaseStart:   base,
			Mid:         mid,
			End:         end + 1,
			OursLabel:   conflictLabel(lines[i].text, "<<<<<<<"),
			BaseLabel:   baseLabel,
			TheirsLabel: conflictLabel(lines[end].text, ">>>>>>>"),
			Ours:        joinConflictLines(lines[i+1 : oursEnd]),
			Theirs:      joinConflictLines(lines[mid+1 : end]),
		}
		if base >= 0 {
			h.Base = joinConflictLines(lines[base+1 : mid])
		}
		hunks = append(hunks, h)
		i = end
	}
	return hunks
}

// CountConflictHunks is how many complete marker blocks text has.
func CountConflictHunks(text string) int { return len(ParseConflictHunks(text)) }

// HasConflictMarkers reports any conflict marker line in text, including
// the half of a block a manual edit left behind. A trailing carriage
// return is stripped per line, so CRLF files count too.
func HasConflictMarkers(text string) bool {
	for _, l := range splitConflictLines(text) {
		line := l.text
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, ">>>>>>>") ||
			line == "=======" || strings.HasPrefix(line, "||||||| ") {
			return true
		}
	}
	return false
}

// ComposeConflictHunks resolves the blocks of text by choices, one per
// block in order. Blocks past the end of choices, and blocks with no
// side chosen, keep their markers. Line endings of the original file
// are preserved byte for byte.
func ComposeConflictHunks(text string, choices []ConflictChoice) string {
	hunks := ParseConflictHunks(text)
	if len(hunks) == 0 {
		return text
	}
	lines := splitConflictLines(text)
	var out []conflictLine
	at := 0
	for i, h := range hunks {
		out = append(out, lines[at:h.Start]...)
		at = h.End
		var c ConflictChoice
		if i < len(choices) {
			c = choices[i]
		}
		if !c.Ours && !c.Theirs {
			out = append(out, lines[h.Start:h.End]...)
			continue
		}
		chosen := ""
		switch {
		case c.Ours && c.Theirs:
			chosen = h.Ours + h.Theirs
		case c.Ours:
			chosen = h.Ours
		default:
			chosen = h.Theirs
		}
		out = append(out, splitConflictLines(chosen)...)
	}
	out = append(out, lines[at:]...)
	return joinConflictLines(out)
}

// ApplyConflictHunk replaces hunk index of text by its side: "ours",
// "theirs" or "both". The bool is false for an unknown side or index.
func ApplyConflictHunk(text string, index int, side string) (string, bool) {
	var c ConflictChoice
	switch side {
	case "ours":
		c.Ours = true
	case "theirs":
		c.Theirs = true
	case "both":
		c.Ours, c.Theirs = true, true
	default:
		return text, false
	}
	hunks := ParseConflictHunks(text)
	if index < 0 || index >= len(hunks) {
		return text, false
	}
	choices := make([]ConflictChoice, len(hunks))
	choices[index] = c
	return ComposeConflictHunks(text, choices), true
}

// IsBinaryContent reports data as binary the way git does: a NUL byte
// in the first 8192 bytes.
func IsBinaryContent(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
