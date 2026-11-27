package git

import (
	"errors"
	"fmt"
	"strings"
)

// ErrPredictUnavailable means the git that ran the command cannot predict
// conflicts: git merge-tree --write-tree needs git 2.38.
var ErrPredictUnavailable = errors.New("this git cannot predict conflicts")

// predict says whether merging the trees of ours and theirs, with base as
// the common ancestor when it is not empty, would conflict. It writes
// objects but changes no ref, the index or the working tree, so it is the
// same on a local repository and one on a server.
func (r *Repo) predict(base, ours, theirs string) (bool, error) {
	args := []string{"merge-tree", "--write-tree", "--no-messages"}
	if base != "" {
		args = append(args, "--merge-base="+base)
	}
	_, err := r.run(quick, append(args, ours, theirs)...)
	if err == nil {
		return false, nil
	}
	if ExitCode(err) == 1 {
		return true, nil
	}
	if strings.Contains(err.Error(), "unknown option") || strings.Contains(err.Error(), "usage:") {
		return false, ErrPredictUnavailable
	}
	return false, err
}

// MergeConflicts says whether merging rev into into would conflict, for
// the Merge dialog.
func (r *Repo) MergeConflicts(rev, into string) (bool, error) {
	rev, into = strings.TrimSpace(rev), strings.TrimSpace(into)
	if rev == "" || into == "" {
		return false, fmt.Errorf("a merge needs two revisions")
	}
	return r.predict("", into, rev)
}

// CherryPickConflicts says whether cherry-picking hash onto HEAD would
// conflict. parent is the parent to skip of a merge commit, 1 when 0.
func (r *Repo) CherryPickConflicts(hash string, parent int) (bool, error) {
	base, err := r.parentOf(hash, parent)
	if err != nil {
		return false, err
	}
	return r.predict(base, "HEAD", strings.TrimSpace(hash))
}

// RevertConflicts says whether reverting hash on HEAD would conflict.
func (r *Repo) RevertConflicts(hash string, parent int) (bool, error) {
	p, err := r.parentOf(hash, parent)
	if err != nil {
		return false, err
	}
	return r.predict(strings.TrimSpace(hash), "HEAD", p)
}

// RebaseConflicts says whether replaying the commits of HEAD onto onto
// would conflict, judged by merging the two tips.
func (r *Repo) RebaseConflicts(onto string) (bool, error) {
	onto = strings.TrimSpace(onto)
	if onto == "" {
		return false, fmt.Errorf("a rebase needs a revision")
	}
	return r.predict("", "HEAD", onto)
}

// parentOf names the n-th parent of a commit, the empty tree for a root.
func (r *Repo) parentOf(hash string, n int) (string, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return "", fmt.Errorf("a commit is needed")
	}
	if n < 1 {
		n = 1
	}
	out, err := r.run(quick, "rev-list", "--parents", "-n", "1", hash)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return emptyTree, nil
	}
	if n >= len(fields) {
		return "", fmt.Errorf("%s has no parent %d", hash, n)
	}
	return fields[n], nil
}
