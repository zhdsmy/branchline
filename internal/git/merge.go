package git

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MergeStyle is how Merge joins rev into the current branch.
type MergeStyle int

const (
	// MergeFastForward fast-forwards when Git can, and merges otherwise.
	MergeFastForward MergeStyle = iota
	// MergeNoFastForward always records a merge commit.
	MergeNoFastForward
	// MergeSquash stages the changes of rev without committing them.
	MergeSquash
	// MergeNoCommit merges and leaves the result uncommitted.
	MergeNoCommit
)

// Merge merges rev into the current branch. noFF keeps the older call:
// true always records a merge commit, false fast-forwards when Git can.
func (r *Repo) Merge(rev string, noFF bool) error {
	style := MergeFastForward
	if noFF {
		style = MergeNoFastForward
	}
	return r.MergeWith(rev, style)
}

// MergeWith merges rev with the choice of the merge dialog.
// Squash and no-commit leave the changes staged for a later commit.
func (r *Repo) MergeWith(rev string, style MergeStyle) error {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return fmt.Errorf("merge needs a revision")
	}
	args := []string{"merge"}
	switch style {
	case MergeFastForward:
		args = append(args, "--no-edit")
	case MergeNoFastForward:
		args = append(args, "--no-edit", "--no-ff")
	case MergeSquash:
		args = append(args, "--squash")
	case MergeNoCommit:
		// git merge --no-commit leaves a real merge uncommitted. A
		// fast-forward still moves the branch, which is what that flag does.
		args = append(args, "--no-commit")
	default:
		return fmt.Errorf("unknown merge type")
	}
	_, err := r.run(quick, withDashdash(args, rev)...)
	return err
}

// Rebase rebases the current branch onto rev.
func (r *Repo) Rebase(rev string) error {
	_, err := r.run(quick, withDashdash([]string{"rebase"}, rev)...)
	return err
}

// Abort stops the merge, rebase, cherry-pick or revert in progress.
func (r *Repo) Abort(state string) error {
	if state == "" {
		return errors.New("nothing is in progress")
	}
	_, err := r.run(quick, state, "--abort")
	return r.restoreSyncStash(err)
}

// Continue goes on with the rebase, cherry-pick or revert in progress, or
// concludes the merge.
func (r *Repo) Continue(state string) error {
	if state == "merge" {
		_, err := r.run(quick, "commit", "--no-edit")
		return r.restoreSyncStash(err)
	}
	// Override GIT_EDITOR too: it takes precedence over core.editor.
	_, err := r.runPrepared(nil, state, "--continue")
	return r.restoreSyncStash(err)
}

// ReplayOptions chooses how one commit is cherry-picked or reverted.
type ReplayOptions struct {
	// NoCommit applies the change to the index and working tree without
	// creating a commit.
	NoCommit bool
	// Mainline is the parent to skip for a merge, starting at 1.
	// Zero uses Git's default and rejects a merge.
	Mainline int
	// RecordOrigin appends the source commit to a cherry-pick message,
	// which is git cherry-pick -x. Revert ignores it.
	RecordOrigin bool
}

func replayArgs(command, hash string, o ReplayOptions) ([]string, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return nil, fmt.Errorf("%s needs a commit", command)
	}
	if o.Mainline < 0 {
		return nil, fmt.Errorf("the parent to skip starts at 1")
	}
	args := []string{command}
	if command == "revert" {
		args = append(args, "--no-edit")
	}
	if o.NoCommit {
		args = append(args, "--no-commit")
	}
	if command == "cherry-pick" && o.RecordOrigin {
		args = append(args, "-x")
	}
	if o.Mainline > 0 {
		args = append(args, "-m", strconv.Itoa(o.Mainline))
	}
	if leadingDash(hash) {
		// "--" cannot protect the commit here: git cherry-pick -- -dash
		// prints the usage instead of reading -dash as the commit, while
		// --end-of-options does read it as one.
		args = append(args, "--end-of-options")
	}
	return append(args, hash), nil
}

// CherryPick applies a commit onto the current branch.
func (r *Repo) CherryPick(hash string) error {
	return r.CherryPickWith(hash, ReplayOptions{})
}

// CherryPickWith applies a commit with the choices of the cherry-pick dialog.
func (r *Repo) CherryPickWith(hash string, o ReplayOptions) error {
	args, err := replayArgs("cherry-pick", hash, o)
	if err != nil {
		return err
	}
	_, err = r.run(quick, args...)
	return err
}

// Revert commits the reverse of a commit.
func (r *Repo) Revert(hash string) error {
	return r.RevertWith(hash, ReplayOptions{})
}

// RevertWith reverses a commit with the choices of the revert dialog.
func (r *Repo) RevertWith(hash string, o ReplayOptions) error {
	args, err := replayArgs("revert", hash, o)
	if err != nil {
		return err
	}
	_, err = r.run(quick, args...)
	return err
}

// Skip drops the commit an operation stopped on and goes on.
// state is rebase, cherry-pick or revert.
func (r *Repo) Skip(state string) error {
	switch state {
	case "rebase", "cherry-pick", "revert", "am":
	default:
		return fmt.Errorf("%s has nothing to skip", state)
	}
	_, err := r.runPrepared(nil, state, "--skip")
	return r.restoreSyncStash(err)
}

// CommitsBetween counts the commits tip has that base does not.
func (r *Repo) CommitsBetween(base, tip string) (int, error) {
	out, err := r.run(quick, withEndOfOptions([]string{"rev-list", "--count"}, base+".."+tip)...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// FinishBranch is Lean Branching's Finish Branch: it checks out into and
// merges branch into it, always recording a merge commit when noFF is set.
func (r *Repo) FinishBranch(branch, into string, noFF bool) error {
	branch, into = strings.TrimSpace(branch), strings.TrimSpace(into)
	if branch == "" || into == "" || branch == into {
		return fmt.Errorf("finishing needs a branch other than %s", into)
	}
	if err := r.Checkout(into); err != nil {
		return err
	}
	style := MergeFastForward
	if noFF {
		style = MergeNoFastForward
	}
	return r.MergeWith(branch, style)
}
