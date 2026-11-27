package app

import (
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui/editor"
	"github.com/zhdsmy/branchline/internal/git"
)

// RefreshIfChanged re-reads the diff of the panel's current request in the
// background and applies it only when its content differs from what the
// panel shows. An identical patch leaves the panel exactly as it is: the
// same *editor.DiffState object, its selection and its scroll position
// survive untouched, so a refresh without visible changes never flickers.
// The automatic refresh uses it after a file hint or an activation, where a
// file's content may have changed without its status letter changing, which
// the status-driven reloads cannot see.
//
// Nothing happens while a normal load is in flight or no diff is loaded.
func (d *diffPanel) RefreshIfChanged(s *repoSession, scope string) bool {
	if s == nil || !s.HasRepo() || !d.req.valid() || d.loading {
		return false
	}
	req := d.req
	req.IgnoreSpace = s.IgnoreSpace()
	req.EntireFile = s.DiffEntireFile()
	req.ContextLines = s.DiffContextLines()
	key := req.key()
	if key != d.loadedKey {
		return false
	}
	captured := req
	captured.Commit.Parents = slices.Clone(req.Commit.Parents)
	limit := d.limit()
	gen := s.Generation()

	return sessionRead(s, scope, func(repo *git.Repo) (diffLoadResult, error) {
		return readDiffPatch(repo, captured, limit)
	}, func(res diffLoadResult, err error) {
		if d.repoGen != gen || d.loadedKey != key || d.loading {
			return
		}
		// A failed re-read keeps the content on display: replacing a good
		// diff with the error of a passive refresh would destroy what the
		// user is reading, and the next hint or activation retries anyway.
		if err != nil || diffResultUnchanged(d, res) {
			return
		}
		d.applyRefreshResult(s, res)
	})
}

// diffResultUnchanged reports whether res renders exactly what the panel
// already shows: every placeholder kind by its flags and byte count, and a
// textual diff by its parsed content. Equal raw bytes alone never decide,
// since two different patches can share a length.
func diffResultUnchanged(d *diffPanel, res diffLoadResult) bool {
	switch {
	case d.err != nil:
		return false
	case d.loading:
		return false
	case d.tooLarge:
		return res.tooLarge && res.rawBytes == d.rawBytes
	case d.binary:
		return res.binary && res.rawBytes == d.rawBytes
	case d.empty:
		return !res.binary && !res.tooLarge && res.rawBytes == d.rawBytes &&
			reflect.DeepEqual(res.patch, d.patch)
	case d.state != nil:
		return res.hasPatch && !res.binary && !res.tooLarge &&
			reflect.DeepEqual(res.patch, d.patch)
	default:
		return false
	}
}

// applyRefreshResult replaces the panel content with a fresh read, as a
// normal load's completion does. It runs only for content known to differ,
// so the swap is always a real change.
func (d *diffPanel) applyRefreshResult(s *repoSession, res diffLoadResult) {
	d.err = nil
	d.binary = false
	d.tooLarge = false
	d.empty = false
	d.rawBytes = res.rawBytes
	d.patch = editor.FilePatch{}
	d.state = nil
	d.added = 0
	d.deleted = 0
	switch {
	case res.tooLarge:
		d.tooLarge = true
	case res.binary:
		d.binary = true
	case !res.hasPatch || len(res.patch.Hunks) == 0:
		d.patch = res.patch
		d.empty = true
	default:
		st := &editor.DiffState{
			Layout:         d.layout,
			KeepScrollbars: true,
		}
		st.SetPatch(res.patch)
		applyDiffDisplayPrefs(st, s.Settings(), s.DiffWrap())
		d.patch = res.patch
		d.state = st
		d.added, d.deleted = st.Stats()
	}
}
