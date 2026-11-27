package git

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (r *Repo) WorktreeAdd(path, branch string) error {
	return r.WorktreeAddAt(path, branch, "HEAD")
}

// WorktreeCreateAt creates a new branch starting at start and checks it out
// in a new worktree at path. Git rejects the command when branch already exists.
func (r *Repo) WorktreeCreateAt(path, branch, start string) error {
	path, branch = r.worktreePath(path), strings.TrimSpace(branch)
	if path == "" || branch == "" {
		return fmt.Errorf("a worktree needs a folder and a branch name")
	}
	if strings.TrimSpace(start) == "" {
		start = "HEAD"
	}
	_, err := r.run(quick, "worktree", "add", "-b", branch, "--", path, start)
	return err
}

// WorktreeAddAt checks out branch at path. A branch that does not exist
// yet starts at start, a branch, tag or commit; one that exists is checked
// out as it is.
func (r *Repo) WorktreeAddAt(path, branch, start string) error {
	path = r.worktreePath(path)
	if strings.TrimSpace(start) == "" {
		start = "HEAD"
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = r.run(quick, "worktree", "add", "--", path, branch)
		return err
	}
	_, err := r.run(quick, "worktree", "add", "-b", branch, "--", path, start)
	return err
}

// WorktreeAddRemote checks out remoteBranch, such as origin/feature, at
// path as local, a new branch tracking it. An existing local branch is
// checked out as it is.
func (r *Repo) WorktreeAddRemote(path, remoteBranch, local string) error {
	path, remoteBranch, local = r.worktreePath(path), strings.TrimSpace(remoteBranch), strings.TrimSpace(local)
	if path == "" || local == "" || !strings.Contains(remoteBranch, "/") {
		return fmt.Errorf("a worktree needs a folder, a remote branch and a local name")
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "refs/heads/"+local); err == nil {
		_, err = r.run(quick, "worktree", "add", "--", path, local)
		return err
	}
	_, err := r.run(quick, "worktree", "add", "--track", "-b", local, "--", path, remoteBranch)
	return err
}

// ResolveWorktreePath resolves path to the canonical working tree root on the
// repository's target machine, expanding remote ~ and symlinks on the server.
func (r *Repo) ResolveWorktreePath(path string) (string, error) {
	path = r.worktreePath(path)
	switch ex := r.Exec.(type) {
	case *LocalExecutor:
		copy := *ex
		copy.Dir = path
		return r.WithExecutor(&copy).TopLevel()
	case *SSHExecutor:
		copy := *ex
		copy.Dir = path
		return r.WithExecutor(&copy).TopLevel()
	default:
		return path, nil
	}
}

// Worktrees lists the checkouts linked to the repository. Current marks the
// checkout the commands run in.
func (r *Repo) Worktrees() ([]Worktree, error) {
	out, err := r.run(quick, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	list := ParseWorktrees(out)
	if len(list) > 1 {
		if err := r.selectCurrentWorktree(list); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (r *Repo) selectCurrentWorktree(list []Worktree) error {
	for i := range list {
		list[i].Current = false
	}
	_, isLocal := r.Exec.(*LocalExecutor)
	switch ex := r.Exec.(type) {
	case *LocalExecutor:
		dir := expandHome(strings.TrimSpace(ex.Dir))
		if markMatchingWorktree(list, dir, true) {
			return nil
		}
	case *SSHExecutor:
		dir := strings.TrimSpace(ex.Dir)
		if markMatchingWorktree(list, dir, false) {
			return nil
		}
	}
	top, err := r.TopLevel()
	if err != nil {
		return fmt.Errorf("worktree current checkout: %w", err)
	}
	if !markMatchingWorktree(list, top, isLocal) {
		return fmt.Errorf("worktree: current checkout %s is not in worktree list", top)
	}
	return nil
}

func markMatchingWorktree(list []Worktree, target string, evalSymlinks bool) bool {
	if target == "" {
		return false
	}
	for i := range list {
		if list[i].Path == target || (evalSymlinks && sameCanonicalPath(list[i].Path, target)) {
			for j := range list {
				list[j].Current = j == i
			}
			return true
		}
	}
	return false
}

func sameCanonicalPath(a, b string) bool {
	if a == b {
		return true
	}
	aa, errA := filepath.EvalSymlinks(a)
	bb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && aa == bb
}

func (r *Repo) worktreePath(path string) string {
	path = strings.TrimSpace(path)
	if _, ssh := r.Exec.(*SSHExecutor); ssh {
		return path
	}
	return expandHome(path)
}

// WorktreeMove renames the directory of a linked checkout. Git updates
// the worktree list. The checkout this command runs in cannot move.
func (r *Repo) WorktreeMove(oldPath, newPath string) error {
	oldPath, newPath = r.worktreePath(oldPath), r.worktreePath(newPath)
	if oldPath == "" || newPath == "" {
		return fmt.Errorf("a worktree move needs both paths")
	}
	_, err := r.run(quick, "worktree", "move", "--", oldPath, newPath)
	return err
}

// WorktreeRemove deletes the checkout at path. Git refuses when it has
// changes that are not committed, unless force is set.
func (r *Repo) WorktreeRemove(path string, force bool) error {
	path = r.worktreePath(path)
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := r.run(quick, append(args, "--", path)...)
	return err
}
