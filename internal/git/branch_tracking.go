package git

import (
	"fmt"
	"strings"
)

// BranchUpstream reads the upstream a local branch is configured to track,
// as "<remote>/<branch>" such as origin/feature, or just the branch when it
// is another local branch. It is empty when the branch tracks nothing. The
// name comes from branch.<name>.remote and branch.<name>.merge, so it still
// answers for an upstream whose remote-tracking ref is gone: the setting is
// there until it is unset.
func (r *Repo) BranchUpstream(local string) (string, error) {
	local = strings.TrimSpace(local)
	if local == "" {
		return "", fmt.Errorf("a branch is needed")
	}
	get := func(key string) (string, error) {
		out, err := r.run(quick, "config", "--get", key)
		if ExitCode(err) == 1 {
			return "", nil
		}
		return strings.TrimSpace(string(out)), err
	}
	remote, err := get("branch." + local + ".remote")
	if err != nil {
		return "", err
	}
	merge, err := get("branch." + local + ".merge")
	if err != nil {
		return "", err
	}
	branch := strings.TrimPrefix(merge, "refs/heads/")
	if remote == "" || branch == "" {
		return "", nil
	}
	if remote == "." {
		return branch, nil
	}
	return remote + "/" + branch, nil
}

// FastForwardBranch moves the local branch to target without checking it
// out, as git update-ref with the branch's own hash as the old value: the
// ref moves only when it still is exactly old, so a branch advanced by
// someone else between the read and the write is never overwritten. The move
// is refused when target is not a descendant of old — that is no fast-forward
// — and when a checkout in any worktree has the branch, whose working tree
// the move would corrupt.
func (r *Repo) FastForwardBranch(local, old, target string) error {
	local = strings.TrimSpace(local)
	if local == "" {
		return fmt.Errorf("fast-forwarding a branch needs its name")
	}
	if !isHex(old) || !isHex(target) {
		return fmt.Errorf("fast-forwarding %s needs the commits it moves from and to", local)
	}
	if _, err := r.run(quick, "merge-base", "--is-ancestor", old, target); err != nil {
		if ExitCode(err) == 1 {
			return fmt.Errorf("cannot fast-forward %s: %s is not ahead of it", local, short(target))
		}
		return fmt.Errorf("cannot fast-forward %s: checking the commits failed: %w", local, err)
	}
	worktrees, err := r.Worktrees()
	if err != nil {
		return fmt.Errorf("cannot fast-forward %s: checking its checkouts failed: %w", local, err)
	}
	for _, w := range worktrees {
		if w.Branch == local {
			return fmt.Errorf("cannot fast-forward %s: it is checked out in %s", local, w.Path)
		}
	}
	_, err = r.run(quick, "update-ref", "refs/heads/"+local, target, old)
	return err
}
