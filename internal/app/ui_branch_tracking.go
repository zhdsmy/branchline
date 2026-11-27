package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// upstreamRefAndHash finds the full ref and the commit the upstream of a
// local branch names, exactly as the reference list shows it: the
// remote-tracking branch of the same short name first, then a local branch,
// and the default refspec layout as a fallback. The full ref, not the short
// name, is what the fast-forward worker re-reads: rev-parse answers a bare
// origin/feature differently once a local branch of that very name exists,
// which the snapshot's short name never means.
func upstreamRefAndHash(refs git.Refs, upstream string) (ref, hash string) {
	for _, rb := range refs.Remote {
		if rb.Name == upstream {
			return rb.FullName, rb.Hash
		}
	}
	for _, b := range refs.Branches {
		if b.Name == upstream {
			return b.FullName, b.Hash
		}
	}
	if strings.Contains(upstream, "/") {
		return "refs/remotes/" + upstream, ""
	}
	return "refs/heads/" + upstream, ""
}

// branchTrackingMenuItems adds the Fast-Forward item and the Tracking
// submenu to the context menu of a local branch of the sidebar, behind a
// separator. Fast-Forward only exists for a branch whose upstream is still
// there, and disables when the branch is not behind it, has diverged, or any
// write or operation is running: what is not behind cannot fast-forward, and
// a diverged branch never can. Choosing either writes nothing on the spot:
// the click only captures what the user saw, and the background worker
// re-reads the repository before it changes anything.
func (a *app) branchTrackingMenuItems(m *ui.Menu, br git.Branch) {
	snap := a.session.Snapshot()
	busy := a.session.Busy()

	m.Separator()
	if br.Upstream != "" && !br.Gone {
		upstreamRef, upstreamHash := upstreamRefAndHash(snap.Refs, br.Upstream)
		label := "Fast-Forward to '" + br.Upstream + "'"
		disabled := busy || snap.Status.State != "" || br.Behind == 0 || br.Ahead > 0
		if m.Item(label).Disabled(disabled).Chosen() {
			a.fastForwardBranch(br, upstreamRef, upstreamHash)
		}
	}
	m.Submenu("Tracking", func(m *ui.Menu) {
		if m.Item("Remove tracking reference").Disabled(busy || br.Upstream == "").Chosen() {
			a.unsetUpstream(br.Name, br.Upstream)
		}
		if len(snap.Refs.Remote) > 0 {
			m.Separator()
		}
		for _, rb := range snap.Refs.Remote {
			if m.Item(rb.Name).Checked(br.Upstream == rb.Name).Disabled(busy).Chosen() {
				if rb.Name != br.Upstream {
					a.trackBranch(br.Name, rb.Name, br.Upstream)
				}
			}
		}
	})
}

// fastForwardBranch fast-forwards a branch to its upstream through the
// session's write path. Everything the write needs to be safe is captured
// here, on the UI thread, at the values the user saw: the branch's name,
// commit and checkout state, and the upstream's name, ref and commit.
func (a *app) fastForwardBranch(br git.Branch, upstreamRef, upstreamHash string) {
	name, hash, current := br.Name, br.Hash, br.Current
	upstream := br.Upstream
	a.session.do("Fast-forwarding "+name, func(r *git.Repo) error {
		return fastForwardWithGuard(r, name, hash, current, upstream, upstreamRef, upstreamHash)
	})
}

// fastForwardWithGuard re-reads the repository before moving anything: the
// branch must still point at the commit the user saw, still be checked out
// or not as it was, and its upstream must still resolve to the commit the
// user saw. Anything else means the click's answer is stale and nothing is
// moved. Only then does the fast-forward run: the current branch through a
// fast-forward-only merge, which Git refuses when local changes stand in the
// way, any other branch through an atomic compare-and-update of its ref,
// which refuses again when a checkout in another worktree has it.
func fastForwardWithGuard(r *git.Repo, branch, branchHash string, wasCurrent bool, upstream, upstreamRef, upstreamHash string) error {
	if branchHash == "" || upstreamHash == "" || upstreamRef == "" {
		return fmt.Errorf("cannot fast-forward %s: what it and %s point at is unknown; reload the repository and try again", branch, upstream)
	}
	st, err := r.Status()
	if err != nil {
		return fmt.Errorf("cannot fast-forward %s: reading the repository state failed: %w", branch, err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot fast-forward %s: a %s is in progress; finish or abort it first", branch, st.State)
	}
	gotBranch, err := r.PathHash("refs/heads/" + branch + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot fast-forward %s: the branch no longer resolves to a commit: %w", branch, err)
	}
	if gotBranch != branchHash {
		return fmt.Errorf("cannot fast-forward %s: the branch moved after the menu item was chosen; review the repository and try again", branch)
	}
	gotUpstreamName, err := r.BranchUpstream(branch)
	if err != nil {
		return fmt.Errorf("cannot fast-forward %s: reading its tracking reference failed: %w", branch, err)
	}
	if gotUpstreamName != upstream {
		return fmt.Errorf("cannot fast-forward %s: its tracking reference changed after the menu item was chosen; review the repository and try again", branch)
	}
	gotUpstream, err := r.PathHash(upstreamRef + "^{commit}")
	if err != nil {
		return fmt.Errorf("cannot fast-forward %s: %s no longer resolves to a commit: %w", branch, upstream, err)
	}
	if gotUpstream != upstreamHash {
		return fmt.Errorf("cannot fast-forward %s: %s moved after the menu item was chosen; review the repository and try again", branch, upstream)
	}
	if (st.Branch == branch) != wasCurrent {
		return fmt.Errorf("cannot fast-forward %s: the checked out branch changed after the menu item was chosen; review the repository and try again", branch)
	}
	if wasCurrent {
		return r.FastForward(upstreamRef)
	}
	return r.FastForwardBranch(branch, branchHash, upstreamHash)
}

// trackBranch makes a local branch follow a remote branch through the
// session's write path, and unsetUpstream drops its tracking reference. Both
// capture the upstream the user saw, and both re-read it before writing: a
// tracking setting that changed since the click belongs to someone else's
// decision, never to this one.
func (a *app) trackBranch(branch, upstream, expectedUpstream string) {
	a.session.do("Tracking "+upstream, func(r *git.Repo) error {
		return trackBranchWithGuard(r, branch, upstream, expectedUpstream)
	})
}

func (a *app) unsetUpstream(branch, expectedUpstream string) {
	a.session.do("Removing tracking reference", func(r *git.Repo) error {
		return unsetUpstreamWithGuard(r, branch, expectedUpstream)
	})
}

func trackBranchWithGuard(r *git.Repo, branch, upstream, expectedUpstream string) error {
	current, err := r.BranchUpstream(branch)
	if err != nil {
		return fmt.Errorf("cannot change the upstream of %s: reading its tracking reference failed: %w", branch, err)
	}
	if current != expectedUpstream {
		return fmt.Errorf("cannot change the upstream of %s: its tracking reference changed after the menu item was chosen; review the repository and try again", branch)
	}
	return r.TrackBranch(branch, upstream)
}

func unsetUpstreamWithGuard(r *git.Repo, branch, expectedUpstream string) error {
	current, err := r.BranchUpstream(branch)
	if err != nil {
		return fmt.Errorf("cannot remove the upstream of %s: reading its tracking reference failed: %w", branch, err)
	}
	if current != expectedUpstream {
		return fmt.Errorf("cannot remove the upstream of %s: its tracking reference changed after the menu item was chosen; review the repository and try again", branch)
	}
	if current == "" {
		return nil
	}
	return r.UnsetUpstream(branch)
}
