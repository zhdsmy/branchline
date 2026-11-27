package git

import (
	"fmt"
	"strings"
)

// BranchMerged reports whether the local branch name is fully merged into
// HEAD: every commit refs/heads/<name> reaches is an ancestor of HEAD.
//
// The answer comes from git merge-base --is-ancestor, whose exit code 1 is
// ambiguous between "not an ancestor" and "a ref is missing", so the refs
// are verified first: a missing branch or an unborn HEAD is an error, never
// a silent "not merged". Any other failure of the check is an error too, so
// callers deleting branches can refuse to delete when the answer is unknown.
func (r *Repo) BranchMerged(name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("a branch needs a name")
	}
	// show-ref, not rev-parse: rev-parse resolves revision suffixes too,
	// so refs/heads/main~1 would pass verification as a revspec even though
	// no branch of that name exists. show-ref matches exact refs only.
	if _, err := r.run(quick, "show-ref", "--verify", "--quiet", "refs/heads/"+name); err != nil {
		return false, fmt.Errorf("verify branch %s: %w", name, err)
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return false, fmt.Errorf("verify HEAD: %w", err)
	}
	_, err := r.run(quick, "merge-base", "--is-ancestor", "refs/heads/"+name, "HEAD")
	if err == nil {
		return true, nil
	}
	if ExitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is merged: %w", name, err)
}

// SafeDeleteBranch deletes the local branch name only after proving on the
// same repository that the deletion is allowed:
//
//   - name must not be the branch currently checked out;
//   - merged == true must hold for a non-forcing delete, and merged may only
//     be trusted when checkErr == nil.
//
// It returns an error without touching the repository whenever the
// preconditions fail, when checkErr reports the merged state is unknown, or
// when an unmerged branch is deleted without force. The delete itself is
// git branch -d or, forced, -D, with the name behind "--" so a branch named
// like an option is still just a branch name.
func (r *Repo) SafeDeleteBranch(name string, force bool, merged bool, checkErr error) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("deleting a branch needs its name")
	}
	current, err := r.CurrentBranch()
	if err != nil {
		return fmt.Errorf("confirm the current branch before deleting %s: %w", name, err)
	}
	if name == current {
		return fmt.Errorf("%s is checked out: a branch in use cannot be deleted", name)
	}
	if checkErr != nil {
		return fmt.Errorf("refusing to delete %s: whether it is merged is unknown: %w", name, checkErr)
	}
	if force && !merged {
		return r.deleteBranchGuarded(name, true)
	}
	if !merged {
		return fmt.Errorf("refusing to delete %s without force: the branch is not fully merged", name)
	}
	return r.deleteBranchGuarded(name, false)
}

// deleteBranchGuarded is git branch -d or -D with the branch name behind a
// "--" separator. DeleteBranch cannot be used here: it passes the name as a
// bare argument, so a branch whose name starts with "-" — creatable through
// the ref store, as any ref is — would be read as command-line options
// (git branch -d -v is --verbose, not the deletion of a branch named -v).
func (r *Repo) deleteBranchGuarded(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := r.run(quick, "branch", flag, "--", name)
	return err
}
