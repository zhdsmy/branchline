package git

import (
	"fmt"
	"strings"
)

// UnsetUpstream removes the tracking reference of a local branch. The
// branch keeps its commits.
func (r *Repo) UnsetUpstream(local string) error {
	local = strings.TrimSpace(local)
	if local == "" {
		return fmt.Errorf("a local branch is needed")
	}
	_, err := r.run(quick, withDashdash([]string{"branch", "--unset-upstream"}, local)...)
	return err
}

// StaleBranches returns the local branches whose upstream is gone and
// that are not checked out.
func StaleBranches(branches []Branch) []string {
	var out []string
	for _, b := range branches {
		if b.Gone && !b.Current && b.FullName != "" && !strings.Contains(b.FullName, "refs/remotes/") {
			out = append(out, b.Name)
		}
	}
	return out
}

// FastForwardToUpstreams moves every local branch that is behind its upstream
// and has nothing of its own to the upstream. A branch that is checked out moves
// by merge --ff-only; the others move without a checkout. It returns the
// branches moved, and the branches skipped because they diverged, have no
// upstream or are gone.
func (r *Repo) FastForwardToUpstreams(branches []Branch) (moved []string, err error) {
	for _, b := range branches {
		if b.Upstream == "" || b.Gone || b.Behind == 0 || b.Ahead > 0 {
			continue
		}
		if b.Current {
			if _, err := r.run(quick, "merge", "--ff-only", "@{upstream}"); err != nil {
				return moved, err
			}
		} else {
			if _, err := r.run(quick, withDashdash([]string{"fetch"}, ".", b.Upstream+":refs/heads/"+b.Name)...); err != nil {
				return moved, err
			}
		}
		moved = append(moved, b.Name)
	}
	return moved, nil
}

// BranchDiff is what head has that base does not: the diff from their
// merge base to head, as git diff base...head. It is what the AI code
// review of a branch reads.
func (r *Repo) BranchDiff(base, head string) (string, error) {
	base, head = strings.TrimSpace(base), strings.TrimSpace(head)
	if base == "" || head == "" {
		return "", fmt.Errorf("a branch to compare is needed")
	}
	out, err := r.run(quick, append(withEndOfOptions([]string{"diff", "--no-ext-diff"}, base+"..."+head), "--")...)
	return string(out), err
}

// StashPatch is the patch of a stash: what applying it changes, as git
// stash show -p.
func (r *Repo) StashPatch(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("a stash is needed")
	}
	out, err := r.run(quick, withEndOfOptions([]string{"stash", "show", "-p", "--no-ext-diff", "--no-textconv", "--include-untracked"}, name)...)
	if err != nil {
		// Git before 2.32 does not know --include-untracked here.
		out, err = r.run(quick, withEndOfOptions([]string{"stash", "show", "-p", "--no-ext-diff", "--no-textconv"}, name)...)
	}
	return string(out), err
}
