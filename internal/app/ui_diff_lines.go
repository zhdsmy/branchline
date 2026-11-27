package app

import (
	"errors"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/zhdsmy/branchline/internal/git"
)

// diffLineActionsEligible reports whether the diff of req may act on its
// changes hunk by hunk and line by line. Every case is a working-tree file
// of Local Changes read without ignoring whitespace — an ignore-whitespace
// diff is not the patch git can apply — and one the partial patches here
// can rewrite: a modification in place on either page, an untracked file
// to stage, an addition to unstage, and a deletion on either page. A
// change of kind the apply cannot split stays whole-file only: a rename or
// copy carries the old path, a conflicted file is not a plain diff, and a
// symlink or gitlink is one indivisible line even though git renders it as
// a text change.
func diffLineActionsEligible(req diffRequest, patch editor.FilePatch) bool {
	if req.IgnoreSpace {
		return false
	}
	switch req.Kind {
	case diffUnstaged:
		switch req.File.Status {
		case 'M', '?', '!', 'D':
		default:
			return false
		}
	case diffStaged:
		switch req.File.Status {
		case 'M', 'A', 'D':
		default:
			return false
		}
	default:
		return false
	}
	if diffPatchIsModeChange(patch) || diffPatchIsLinkOrGitlink(patch) {
		return false
	}
	// The patch shape must agree with the file's status: a modification
	// keeps both names, an addition the new one, a deletion the old one.
	// Anything else — a rename or copy pair, a mismatch between the row
	// clicked and the diff read — is not the patch these actions mean.
	switch req.File.Status {
	case '?', '!', 'A':
		return patch.OldName == "" && patch.NewName == req.File.Path
	case 'D':
		return patch.NewName == "" && patch.OldName == req.File.Path
	default:
		return patch.OldName != "" && patch.OldName == patch.NewName
	}
}

// diffPatchIsModeChange reports whether the diff changes the file's mode:
// a change of permissions only, or of the file's type. The patches here
// rewrite content and git apply takes no mode line from them, so the mode
// change would either be left out or applied whole, neither of which the
// line actions mean.
func diffPatchIsModeChange(patch editor.FilePatch) bool {
	return patch.OldMode != "" && patch.NewMode != "" && patch.OldMode != patch.NewMode
}

// diffPatchIsLinkOrGitlink reports whether the diff is of a symlink or a
// gitlink, neither of which a partial patch may act on. The diff of one
// added or deleted names the mode; the diff of a symlink target change or
// a moved submodule does not, but both are one indivisible line and git
// renders that line in one shape: a single replacement run of the whole
// file. A text file of one newline-less line changing reads the same and
// is refused with them — acting on its only change whole is what the line
// actions would do anyway. A gitlink's line names the subproject commit.
func diffPatchIsLinkOrGitlink(patch editor.FilePatch) bool {
	for _, m := range []string{patch.OldMode, patch.NewMode} {
		if m == "120000" || m == "160000" {
			return true
		}
	}
	changed := 0
	for _, h := range patch.Hunks {
		for j, l := range h.Lines {
			if l == "" || l[0] == '\\' {
				continue
			}
			if l[0] != '-' && l[0] != '+' {
				continue
			}
			changed++
			if strings.HasPrefix(l[1:], "Subproject commit ") {
				return true
			}
			// Every line of a link target or a gitlink ends the file
			// without a newline; a replacement of ordinary lines does not.
			if j+1 >= len(h.Lines) || !strings.HasPrefix(h.Lines[j+1], "\\") {
				return false
			}
		}
	}
	// One replacement of one line with no line of context beside it is the
	// whole file in one line: a link, a gitlink, or the text file that
	// reads the same.
	if changed == 2 {
		total := 0
		for _, h := range patch.Hunks {
			for _, l := range h.Lines {
				if l != "" && l[0] != '\\' {
					total++
				}
			}
		}
		return total == 2
	}
	return false
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

// renderLineActions renders the action bar on one change of the diff: the
// buttons its file's page offers. A modified file unstaged stages or
// discards the change, a staged one unstages it; untracked files and
// deletions stage, additions and staged deletions unstage. Untracked
// files and deletions have no lines to discard: discarding them is the
// whole-file Discard of the file list, unchanged. While lines are selected
// and the selection overlaps the change, the buttons act on the selection
// instead. The render runs every frame, so it only compares ranges;
// mapping the viewer's lines back to the patch and building the patch text
// happens when a button is clicked.
func (d *diffPanel) renderLineActions(s *repoSession, scope string, p palette) func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
	return func(c *ui.Context, bar ui.Element, a *editor.DiffHunkActions) {
		stageVerb := "Stage"
		if d.req.Kind == diffStaged {
			stageVerb = "Unstage"
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
		t := c.Theme()
		bar.Row().Gap(t.Space(1))
		bar.Children(func() {
			d.lineActionButton(c, s, p, unit, stageVerb, func() {
				d.applyDiffLines(s, scope, a.Hunk, overlaps)
			})
			if d.req.Kind == diffUnstaged && d.req.File.Status == 'M' {
				d.lineActionButton(c, s, p, unit, "Discard", func() {
					d.askDiscardDiffLines(s, a.Hunk, overlaps)
				})
			}
		})
	}
}

// lineActionButton renders one button of the diff's action bar: label the
// verb and the unit it acts on, the accessibility label the same so the
// button reads as it shows.
func (d *diffPanel) lineActionButton(c *ui.Context, s *repoSession, p palette, unit, verb string, act func()) {
	label := verb + " " + unit
	t := c.Theme()
	btn := ui.ButtonBase(c).Key(unit+"-"+verb).
		Padding(0, t.Space(2)).
		Height(18).
		Radius(t.Radius-2).
		Background(t.Surface).
		Border(1, t.Border).
		Label(label)
	btn.Children(func() {
		ui.Text(c, label).FontSize(max(11, t.FontSize-2)).TextColor(p.text)
	})
	if btn.Disabled(s.Busy()).Clicked() {
		act()
	}
}

// diffDiscardState is the discard confirmation the diff panel has open:
// the copies made on the UI thread when the user asked — the request, the
// patch as shown and the patch applying the lines chosen — so the write
// the confirmation starts touches nothing the user did not see. open is
// the modal's own flag; fileID and repoGen are the file and repository the
// question is about, and the question is answered with cancel when either
// moves.
type diffDiscardState struct {
	open    bool
	title   string
	body    string
	key     string
	fileID  string
	repoGen uint64
	req     diffRequest
	patch   editor.FilePatch
	text    string
	limit   int
}

// askDiscardDiffLines opens the discard confirmation for the change or the
// selected lines of hk: it captures the request, the patch shown and the
// selection on the UI thread, maps the change back to the patch's lines,
// and builds the patch whose reverse application discards them. Nothing is
// written before the confirmation answers.
func (d *diffPanel) askDiscardDiffLines(s *repoSession, hk editor.DiffHunk, lines bool) {
	if s == nil || s.Busy() || d.discard != nil {
		return
	}
	patch := d.patch
	var chosen [][]bool
	if lines {
		sel, ok := d.state.SelectedLines()
		if !ok {
			return
		}
		chosen = patchLinesSelected(patch, sel)
	} else {
		chosen = patchRunLines(patch, hk)
	}
	// Discard undoes the changes the diff shows, so the patch is built as
	// unstaging builds it and applied to the working tree in reverse.
	text, err := buildLinesPatch(patch, chosen, true)
	if err != nil {
		return
	}
	req := d.req
	req.Commit.Parents = append([]string(nil), req.Commit.Parents...)
	st := &diffDiscardState{
		open:    true,
		key:     req.key(),
		fileID:  diffFileIdentity(req),
		repoGen: d.repoGen,
		req:     req,
		patch:   patch,
		text:    text,
		limit:   d.limit(),
	}
	if lines {
		st.title = "Do you want to discard the changes in the selected lines?"
		st.body = "You can't undo this action."
	} else {
		st.title = "Discard this hunk of " + req.File.Path + "?"
		st.body = "The other changes in the file stay."
	}
	d.discard = st
}

// diffFileIdentity is the file a diff request shows: what switching the
// selection changes, without the revision and display choices a plain
// reload of the same file carries.
func diffFileIdentity(req diffRequest) string {
	return strings.Join([]string{
		string([]byte{byte(req.Kind)}),
		req.File.Path,
		req.File.OrigPath,
		string([]byte{req.File.Status}),
	}, "\x00")
}

// cancelDiscardDiffLines answers the open discard confirmation with
// cancel: nothing was ever written, so there is nothing to undo.
func (d *diffPanel) cancelDiscardDiffLines() {
	d.discard = nil
}

// confirmDiscardDiffLines answers the open discard confirmation with
// Discard: the write worker re-reads the diff and applies the captured
// patch to the working tree in reverse, only while the diff still reads as
// it did when the question was asked. The index is never touched.
func (d *diffPanel) confirmDiscardDiffLines(s *repoSession, scope string) {
	st := d.discard
	if st == nil {
		return
	}
	d.discard = nil
	if s == nil || s.Busy() {
		return
	}
	key := st.key
	s.doThen("Discard", func(repo *git.Repo) error {
		return applyDiffLinesGuarded(repo, st.req, st.patch, st.limit, st.text, false, true)
	}, func(error) {
		// Whatever the outcome — applied, refused, or failed — the working
		// tree the diff compares against may have moved, so the panel reads
		// it again when it still shows the diff this write acted on.
		if d.req.key() == key {
			d.Reload(s, scope)
		}
	})
}

// discardDiffLinesModal renders the discard confirmation over the panel.
// The question survives a reload of the diff — the user reads it while the
// panel reads again, and the guarded write refuses a file that moved — but
// it is answered with cancel when the selection or the repository moves,
// since the question then no longer names what the user is looking at.
func (d *diffPanel) discardDiffLinesModal(c *ui.Context, s *repoSession, scope string, p palette) {
	st := d.discard
	if st == nil {
		return
	}
	if s == nil || s.Generation() != st.repoGen || diffFileIdentity(d.req) != st.fileID {
		d.cancelDiscardDiffLines()
		return
	}
	if ui.Modal(c.Key("discard-lines-modal"), &st.open, func() {
		ui.Column(c.Key("discard-lines-modal-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, st.title).FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, st.body).FontSize(13).TextColor(p.muted).MaxWidth(390)
			ui.Row(c.Key("discard-lines-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("discard-lines-cancel"), "Cancel").Label("Cancel").Clicked() {
					d.cancelDiscardDiffLines()
				}
				if ui.PrimaryButton(c.Key("discard-lines-confirm"), "Discard").Label("Confirm Discard").Disabled(s.Busy()).Clicked() {
					d.confirmDiscardDiffLines(s, scope)
				}
			})
		})
	}).Dismissed() {
		d.cancelDiscardDiffLines()
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
		return applyDiffLinesGuarded(repo, req, patch, limit, text, true, staged)
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

// applyDiffLinesGuarded applies text — forward to the index to stage,
// reversed to the index to unstage, reversed to the working tree to
// discard — after re-reading the diff req asks for and proving it is still
// the patch the user acted on. When it is not, nothing is touched; a stale
// read or a diff gone empty counts as changed.
func applyDiffLinesGuarded(repo *git.Repo, req diffRequest, want editor.FilePatch, limit int, text string, cached, reverse bool) error {
	fresh, err := readDiffPatch(repo, req, limit)
	if err != nil {
		return err
	}
	if !fresh.hasPatch || fresh.binary || fresh.tooLarge || !reflect.DeepEqual(fresh.patch, want) {
		return errDiffChanged
	}
	return repo.ApplyLines(text, cached, reverse)
}
