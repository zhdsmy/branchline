package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

// This file builds the patches behind the diff viewer's hunk and line
// actions: staging part of an unstaged file, or unstaging part of a staged
// one. The patch the viewer shows is git's own diff, but it is not what git
// apply can take when only some of its changed lines are wanted. As git-gui
// does, the patch built here keeps every context line so that git verifies
// the surrounding content, keeps the changed lines chosen, and rewrites the
// rest so the index gains exactly the lines chosen.

// patchLinesSelected marks the changed lines of f's hunks that a selection
// of the diff viewer's lines covers, hunk by hunk and line by line. The
// viewer's lines are those of the texts its patch made: the lines of every
// hunk joined end to end, numbered from 0 per side, with the deleted lines
// on the old side and the rest on both. A selection lives on one side, so
// only lines of its kind can be chosen: deletions on the old side,
// additions on the new. Context lines inside the range never count as a
// chosen change.
func patchLinesSelected(f editor.FilePatch, r editor.DiffLineRange) [][]bool {
	if r.End < r.Start {
		r.Start, r.End = r.End, r.Start
	}
	inside := func(line int) bool { return line >= r.Start && line <= r.End }
	chosen := make([][]bool, len(f.Hunks))
	oldLine, newLine := 0, 0
	for i, h := range f.Hunks {
		chosen[i] = make([]bool, len(h.Lines))
		for j, l := range h.Lines {
			if l == "" || l[0] == '\\' {
				continue
			}
			switch l[0] {
			case '-':
				chosen[i][j] = r.Side == editor.DiffOld && inside(oldLine)
				oldLine++
			case '+':
				chosen[i][j] = r.Side == editor.DiffNew && inside(newLine)
				newLine++
			default:
				oldLine++
				newLine++
			}
		}
	}
	return chosen
}

// patchRunLines marks the changed lines of the run of - and + lines that
// the change hunk covers. The viewer's changes are the runs of changed
// lines of the patch's hunks, so one @@ hunk split by context lines makes
// several of them, and hunk is how the viewer tells them apart: the old
// lines it replaces, from 0 and exclusive of the end, and the new ones
// they become, counted over the same joined texts the selection numbers.
func patchRunLines(f editor.FilePatch, hunk editor.DiffHunk) [][]bool {
	chosen := make([][]bool, len(f.Hunks))
	oldLine, newLine := 0, 0
	inRun := false
	matched := false
	for i, h := range f.Hunks {
		chosen[i] = make([]bool, len(h.Lines))
		for j, l := range h.Lines {
			if l == "" || l[0] == '\\' {
				continue
			}
			switch l[0] {
			case '-', '+':
				if !inRun {
					inRun = true
					matched = oldLine == hunk.OldStart && newLine == hunk.NewStart
				}
				if matched {
					chosen[i][j] = true
				}
			default:
				inRun = false
				matched = false
			}
			if l[0] != '+' {
				oldLine++
			}
			if l[0] != '-' {
				newLine++
			}
		}
		// Runs cannot span @@ hunks: the lines of one hunk are contiguous.
		inRun = false
		matched = false
	}
	return chosen
}

// buildLinesPatch is the patch of f's changed lines that chosen marks, in
// the form git apply takes. Forward (reverse false) it stages the lines:
// applied to the index, whose content is the diff's old side. In reverse it
// unstages them: git apply --reverse takes additions out of the index and
// puts deletions back, the index then being the diff's new side.
//
// The lines of a hunk are rewritten one by one: a context line stays, a
// chosen change line keeps its sign, and a change line not chosen turns
// into context when the line it stands for must remain — a deletion while
// staging, an addition while unstaging — and is left out otherwise.
//
// Rewriting is not enough on its own: the order the lines end in decides
// where every line lands, because a line rewritten to context is copied
// rather than skipped. Staging, the context lines a run of unchosen
// deletions becomes wait — as git-gui holds them back — until a real
// context line, a chosen deletion, a dropped line or the hunk's end, so a
// chosen addition moves ahead of the lines it answers to and a replacement
// can stage as the replacement was. Unstaging wants the diff's pair order
// back: a patch applied backwards consumes its additions and copies its
// context in the order they come, inserting its deletions where the walk
// stands, so every restoration takes the place of the addition it was
// aligned with and comes before it. Without that, unstaging the second of
// two neighbouring replacements puts the lines back transposed.
//
// A "\ No newline at end of file" line belongs to the line before it: it
// stays while that line stays, whatever form the line took, and goes when
// the line goes. A @@ hunk with no chosen line is dropped whole, and every
// surviving hunk header is recomputed: the side git reads to place the hunk
// keeps its start, the other is shifted by the net lines the hunks before
// it add or remove.
//
// patchPiece is one line of a patch being built, with the form it takes
// and the form the diff gave it: the line itself, the "\ No newline at end
// of file" lines belonging to it, whether it is a line of the old or the
// new side or both, and whether the diff offered it as a change or as
// context. A conversion is a change line rewritten as context: of the old
// side when a deletion stays (staging), of the new side when an addition
// stays (unstaging). A dropped piece is one the patch leaves out, kept in
// the list for pairing its place with the lines around it.
type patchPiece struct {
	kind    byte   // the line's first character in the patch built: ' ', '-' or '+'
	orig    byte   // its first character in the diff: ' ', '-' or '+'
	line    string // the whole line with the built prefix
	tail    []string
	dropped bool
}

// patchPieces rewrites the lines of one hunk for the changed lines chosen,
// in the diff's order, and counts the lines of either side the rewritten
// hunk has. Reverse selects the rewrites of unstaging, forward those of
// staging. Lines left out stay in the list as dropped pieces, so the
// ordering can read the pairs the diff aligned; the emitter skips them.
// The order the pieces take is the caller's: the two directions want
// different ones.
func patchPieces(h editor.PatchHunk, hunkChosen []bool, reverse bool) (pieces []patchPiece, anyChosen bool, oldN, newN int) {
	prevOut := false
	for j, l := range h.Lines {
		if l == "" {
			continue
		}
		if l[0] == '\\' {
			// Goes with the line before it, which a conversion counts as
			// kept.
			if prevOut && len(pieces) > 0 {
				pieces[len(pieces)-1].tail = append(pieces[len(pieces)-1].tail, l)
			}
			continue
		}
		pick := j < len(hunkChosen) && hunkChosen[j]
		kind := l[0]
		emit := func(p patchPiece) {
			pieces = append(pieces, p)
			prevOut = !p.dropped
			if p.dropped {
				return
			}
			if p.kind != '+' {
				oldN++
			}
			if p.kind != '-' {
				newN++
			}
		}
		switch {
		case kind != '-' && kind != '+':
			emit(patchPiece{kind: ' ', orig: ' ', line: l})
		case pick && kind == '-':
			emit(patchPiece{kind: '-', orig: '-', line: l})
			anyChosen = true
		case pick && kind == '+':
			emit(patchPiece{kind: '+', orig: '+', line: l})
			anyChosen = true
		case kind == '-' && !reverse:
			// A deletion not staged stays in the index: it becomes a line
			// of both sides.
			emit(patchPiece{kind: ' ', orig: '-', line: " " + l[1:]})
		case kind == '+' && reverse:
			// An addition not unstaged stays in the index.
			emit(patchPiece{kind: ' ', orig: '+', line: " " + l[1:]})
		case kind == '+':
			// An addition not staged is left out.
			emit(patchPiece{kind: '+', orig: '+', line: l, dropped: true})
		default:
			// A deletion not unstaged is left out.
			emit(patchPiece{kind: '-', orig: '-', line: l, dropped: true})
		}
	}
	return pieces, anyChosen, oldN, newN
}

// orderStagePieces is the order git-gui gives the pieces of a staging
// patch: the conversions of deletions wait until a real context line, a
// chosen deletion, a dropped line or the end, so a chosen addition moves
// ahead of the lines it answers to and a replacement stages as the
// replacement was. Dropped lines write nothing, yet the conversions waiting
// flush before where they were.
func orderStagePieces(pieces []patchPiece) []patchPiece {
	var out, pending []patchPiece
	flush := func() {
		out = append(out, pending...)
		pending = nil
	}
	for _, p := range pieces {
		switch {
		case p.dropped:
			flush()
		case p.kind == ' ' && p.orig == '-':
			pending = append(pending, p)
		case p.kind == '+':
			out = append(out, p)
		default:
			flush()
			out = append(out, p)
		}
	}
	flush()
	return out
}

// orderUnstagePieces is the order a patch applied backwards needs. Written
// in the diff's order, the deletions restored would land ahead of the
// additions that stay, transposing neighbouring replacements: a patch
// applied backwards consumes its additions and copies its context in the
// order they come, and inserts its deletions where the walk stands. Within
// each run of changed lines the pieces pair off as the diff aligned them —
// the first deletion with the first addition, the second with the second —
// and every pair takes the addition's place: the restoration first, as a
// forward diff writes deletions before additions, then the addition or the
// context it became. What a pair holds counts whether or not the patch
// keeps it, so a line out of the patch still puts its partner where the
// diff had it. True context lines bound the runs and keep their places.
func orderUnstagePieces(pieces []patchPiece) []patchPiece {
	var out []patchPiece
	var minus, plus []patchPiece
	flush := func() {
		n := len(minus)
		if len(plus) > n {
			n = len(plus)
		}
		for k := 0; k < n; k++ {
			if k < len(minus) && !minus[k].dropped {
				out = append(out, minus[k])
			}
			if k < len(plus) && !plus[k].dropped {
				out = append(out, plus[k])
			}
		}
		minus, plus = nil, nil
	}
	for _, p := range pieces {
		switch p.orig {
		case ' ':
			flush()
			out = append(out, p)
		case '-':
			minus = append(minus, p)
		default:
			// Additions unstage takes out or leaves: the additions it takes
			// out, the conversions of the ones staying, and the places of
			// the ones out of the patch, all of the new side.
			plus = append(plus, p)
		}
	}
	flush()
	return out
}

func buildLinesPatch(f editor.FilePatch, chosen [][]bool, reverse bool) (string, error) {
	type outHunk struct {
		pieces             []patchPiece
		oldStart, newStart int
		oldN, newN         int
		section            string
	}
	var hunks []outHunk
	delta := 0
	for i, h := range f.Hunks {
		var hunkChosen []bool
		if i < len(chosen) {
			hunkChosen = chosen[i]
		}
		pieces, anyChosen, oldN, newN := patchPieces(h, hunkChosen, reverse)
		if !anyChosen {
			continue
		}
		if reverse {
			pieces = orderUnstagePieces(pieces)
		} else {
			pieces = orderStagePieces(pieces)
		}
		oh := outHunk{pieces: pieces, oldN: oldN, newN: newN, section: h.Section}
		if !reverse {
			// The old side is the index the patch applies to, unchanged by
			// the hunks before this one in the original diff's numbering.
			pos := h.OldStart
			if h.OldLines == 0 {
				pos++
			}
			oh.oldStart = h.OldStart
			oh.newStart = patchStartOf(pos+delta, newN)
		} else {
			pos := h.NewStart
			if h.NewLines == 0 {
				pos++
			}
			oh.newStart = h.NewStart
			oh.oldStart = patchStartOf(pos-delta, oldN)
		}
		delta += newN - oldN
		hunks = append(hunks, oh)
	}
	if len(hunks) == 0 {
		return "", errors.New("the lines selected have no changes")
	}

	name := f.NewName
	if name == "" {
		name = f.OldName
	}
	quoted := quotePatchPath(name)
	var b strings.Builder
	b.WriteString("diff --git a/" + quoted + " b/" + quoted + "\n")
	b.WriteString("--- a/" + quoted + "\n")
	b.WriteString("+++ b/" + quoted + "\n")
	for _, oh := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@", oh.oldStart, oh.oldN, oh.newStart, oh.newN)
		if oh.section != "" {
			b.WriteString(" " + oh.section)
		}
		b.WriteByte('\n')
		for _, p := range oh.pieces {
			if p.dropped {
				continue
			}
			b.WriteString(p.line)
			b.WriteByte('\n')
			for _, l := range p.tail {
				b.WriteString(l)
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

// patchStartOf is the start a hunk header gives a side whose lines begin
// at pos in the other side's numbering, n of them: the line before when
// the side holds no lines, as unified diff numbers empty sides.
func patchStartOf(pos, n int) int {
	if n == 0 {
		return pos - 1
	}
	return pos
}

// quotePatchPath writes a path as git does in a patch when the path needs
// it: plain unless it holds a control character, a quote or a backslash,
// else as a C string with octal escapes. Spaces and non-ASCII text stay as
// they are, as the diffs this applies to are read with core.quotepath off.
func quotePatchPath(path string) string {
	plain := true
	for i := 0; i < len(path); i++ {
		if c := path[i]; c < 0x20 || c == 0x7f || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
