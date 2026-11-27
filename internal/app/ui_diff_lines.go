package app

import (
	"errors"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

// diffLineActionsEligible reports whether the diff of req may stage or
// unstage its changes line by line: an unstaged or staged working-tree file
// of Local Changes, modified in place rather than added, deleted, renamed,
// copied, retyped or conflicted, and read without ignoring whitespace. An
// ignore-whitespace diff is not the patch git can apply, and a change of
// kind the apply cannot split (a rename carries the old path, an addition
// or deletion the whole file) stays whole-file only.
func diffLineActionsEligible(req diffRequest, patch editor.FilePatch) bool {
	if req.Kind != diffUnstaged && req.Kind != diffStaged {
		return false
	}
	if req.IgnoreSpace || req.File.Status != 'M' {
		return false
	}
	if patch.OldName == "" || patch.NewName == "" || patch.OldName != patch.NewName {
		return false
	}
	return true
}

// syncLineActions wires the diff viewer's line actions for the frame, or
// unwires them: the action bar on the change under the pointer or cursor,
// and line selection by the line numbers. Both belong to Local Changes
// alone — the kind of the request already says, since every other page's
// diff is of a commit — and only while the file is one a partial patch can
// act on.
func (d *diffPanel) syncLineActions(s *repoSession, scope string, p palette) {
	st := d.state
	if st == nil {
		return
	}
	if s == nil || !diffLineActionsEligible(d.req, d.patch) {
		st.HunkActions = false
		st.RenderHunkActions = nil
		st.LineSelection = false
		st.OnLineSelection = nil
		return
	}
	st.HunkActions = true
	st.RenderHunkActions = d.renderLineActions(s, scope, p)
	st.LineSelection = true
	st.OnLineSelection = nil
}

// renderLineActions renders the action bar on one change of the diff: a
// button staging the change's lines, or unstaging them for a staged file.
// While lines are selected and the selection overlaps the change, the
// button acts on the selection instead. The render runs every frame, so it
// only compares ranges; mapping the viewer's lines back to the patch and
// building the patch text happens when the button is clicked.
func (d *diffPanel) renderLineActions(s *repoSession, scope string, p palette) func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
	return func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
		staged := d.req.Kind == diffStaged
		verb := "Stage"
		if staged {
			verb = "Unstage"
		}
		hk := a.Hunk
		overlaps := false
		if sel, ok := d.state.SelectedLines(); ok {
			lo, hi := hk.NewStart, hk.NewEnd
			if sel.Side == editor.DiffOld {
				lo, hi = hk.OldStart, hk.OldEnd
			}
			overlaps = lo < hi && sel.Start < hi && sel.End >= lo
		}
		unit := "Hunk"
		if overlaps {
			unit = "Lines"
		}
		label := verb + " " + unit
		t := c.Theme()
		lh := float32(18)
		bar.Row().Gap(t.Space(1))
		bar.Children(func() {
			btn := ui.ButtonBase(c).Key(unit).
				Padding(0, t.Space(2)).
				Height(lh).
				Radius(t.Radius-2).
				Background(t.Surface).
				Border(1, t.Border).
				Label(label)
			btn.Children(func() {
				ui.Text(c, label).FontSize(max(11, t.FontSize-2)).TextColor(p.text)
			})
			if btn.Disabled(s.Busy()).Clicked() {
				d.applyDiffLines(s, scope, a.Hunk, overlaps)
			}
		})
	}
}

// applyDiffLines runs the write behind a hunk or line action: it captures
// the request, the patch shown and the selection on the UI thread, maps the
// change back to the patch's lines, and builds the patch applying them.
// The write worker re-reads the same diff and refuses to touch the index
// when it no longer matches what the user saw.
func (d *diffPanel) applyDiffLines(s *repoSession, scope string, hk editor.DiffHunk, lines bool) {
	if s == nil || s.Busy() {
		return
	}
	staged := d.req.Kind == diffStaged
	req := d.req
	req.Commit.Parents = append([]string(nil), req.Commit.Parents...)
	patch := d.patch
	limit := d.limit()
	key := d.req.key()

	var chosen [][]bool
	if lines {
		if sel, ok := d.state.SelectedLines(); ok {
			chosen = patchLinesSelected(patch, sel)
		} else {
			return
		}
	} else {
		chosen = patchRunLines(patch, hk)
	}
	text, err := buildLinesPatch(patch, chosen, staged)
	if err != nil {
		// The bar only offers what the change or selection covers, so a
		// patch of nothing marks a selection that held context lines alone.
		// Nothing is applied and nothing has to reload.
		return
	}

	label := "Stage"
	if staged {
		label = "Unstage"
	}
	s.doThen(label, func(repo *git.Repo) error {
		return applyDiffLinesGuarded(repo, req, patch, limit, text, staged)
	}, func(error) {
		// Whatever the outcome — applied, refused, or failed — the index the
		// diff compares against may have moved, so the panel reads it again
		// when it still shows the diff this write acted on. The reload also
		// clears the selection, which a fresh diff state never carries.
		if d.req.key() == key {
			d.Reload(s, scope)
		}
	})
}

// errDiffChanged is the refusal the guarded write returns when the file
// moved between showing the diff and applying part of it. The message ends
// with a full stop, as a sentence facing the user does, so it is built out
// of staticcheck's constant folding.
var errDiffChanged = errors.New(strings.Join([]string{
	"The diff changed after it was shown.",
	"Review it and try again.",
}, " "))

// applyDiffLinesGuarded applies text to the index — forward to stage,
// reversed to unstage — after re-reading the diff req asks for and proving
// it is still the patch the user acted on. When it is not, the index is
// left untouched; a stale read or a diff gone empty counts as changed.
func applyDiffLinesGuarded(repo *git.Repo, req diffRequest, want editor.FilePatch, limit int, text string, reverse bool) error {
	fresh, err := readDiffPatch(repo, req, limit)
	if err != nil {
		return err
	}
	if !fresh.hasPatch || fresh.binary || fresh.tooLarge || !reflect.DeepEqual(fresh.patch, want) {
		return errDiffChanged
	}
	return repo.ApplyLines(text, true, reverse)
}
