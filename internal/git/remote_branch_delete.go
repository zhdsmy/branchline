package git

import (
	"fmt"
	"strings"
)

// DeleteRemoteBranchLease deletes branch from remote, such as feature from
// origin, but only while the server's branch still points at expect, the
// commit hash the caller last verified. It is
//
//	git push --force-with-lease=refs/heads/<branch>:<expect> <remote> --delete refs/heads/<branch>
//
// so a branch someone else moved since is never deleted: git rejects the
// push with "stale info" and the server keeps it. expect must be the full
// commit hash the server's branch is believed to be at — an empty or
// malformed expectation is refused before anything is sent, because a lease
// whose value git cannot parse protects nothing.
func (r *Repo) DeleteRemoteBranchLease(remote, branch, expect string) error {
	remote, branch, expect = strings.TrimSpace(remote), strings.TrimSpace(branch), strings.TrimSpace(expect)
	if remote == "" || branch == "" {
		return fmt.Errorf("deleting a remote branch needs a remote and a branch")
	}
	if strings.ContainsAny(remote, " \t\r\n\x00") || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("deleting a remote branch: the remote name %q is not usable", remote)
	}
	if strings.ContainsAny(branch, " \t\r\n\x00") || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("deleting a remote branch: the branch name %q is not usable", branch)
	}
	if problem := RefNameProblem(branch); problem != "" {
		return fmt.Errorf("deleting a remote branch: %q cannot be a branch: %s", branch, problem)
	}
	if (len(expect) != 40 && len(expect) != 64) || !isHex(expect) {
		return fmt.Errorf("deleting a remote branch needs the commit it is expected to be at")
	}
	ref := "refs/heads/" + branch
	_, err := r.run(slow, "push", "--force-with-lease="+ref+":"+expect, remote, "--delete", ref)
	return err
}
