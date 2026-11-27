package git

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRebaseRangeMerge is returned when a merge commit is inside the range an
// interactive rebase would replay: git rebase -i replays a merge as its
// changes, flattening the history, and this application does not offer that.
// The error carries the short hash of the first merge commit of the range.
var ErrRebaseRangeMerge = errors.New("the range contains a merge commit, which interactive rebase would flatten")

// RebaseRangePlan reads the interactive rebase plan of onto..HEAD: the
// commits from onto to HEAD, oldest first, each starting as a pick. Unlike
// the bare RebasePlan it refuses the ranges an interactive rebase of them
// could not honestly replay: onto must be an ancestor of HEAD — replaying
// the range rewrites the branch the user is standing on, never a side line —
// and no merge commit may be inside the range. The onto argument "" and
// "--root" name the whole history, exactly as RebasePlan reads it.
func (r *Repo) RebaseRangePlan(onto string) ([]RebaseTodo, error) {
	if err := validateRebaseOntoArg(onto); err != nil {
		return nil, err
	}
	root := onto == "" || onto == "--root"
	if !root {
		// rev-parse before merge-base: exit code 1 of --is-ancestor is
		// ambiguous between "not an ancestor" and "a ref is missing", so the
		// base commit is verified first and a plain 1 means not an ancestor.
		if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", onto+"^{commit}"); err != nil {
			return nil, fmt.Errorf("interactive rebase base %s is not a commit", onto)
		}
		if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
			return nil, fmt.Errorf("interactive rebase needs a HEAD commit: %w", err)
		}
		_, err := r.run(quick, "merge-base", "--is-ancestor", onto, "HEAD")
		if err == nil {
			// onto is an ancestor of HEAD: the range replays the branch itself.
		} else if ExitCode(err) == 1 {
			return nil, fmt.Errorf("interactive rebase base %s is not an ancestor of HEAD", onto)
		} else {
			return nil, fmt.Errorf("checking the interactive rebase base failed: %w", err)
		}
	}
	// git rebase -i replays merges as their changes, flattening the history:
	// a merge inside the range is refused, as the batch squash refuses it.
	rev := onto + "..HEAD"
	if root {
		rev = "HEAD"
	}
	merges, err := r.run(quick, "rev-list", "--merges", "-n", "1", rev)
	if err != nil {
		return nil, err
	}
	if m := strings.TrimSpace(string(merges)); m != "" {
		return nil, fmt.Errorf("%w (%s)", ErrRebaseRangeMerge, short(m))
	}
	return r.RebasePlan(onto)
}
