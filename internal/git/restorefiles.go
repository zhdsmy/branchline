package git

import (
	"fmt"
	"strings"
)

// EmptyTree is the hash of the tree with nothing in it: the state "before"
// the first commit of a history.
func (r *Repo) EmptyTree() (string, error) {
	out, err := r.runInput(quick, nil, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ResetFiles puts several files, in the working tree and in the index, back
// to the revision named. A file the revision does not have is removed. The
// rest of the tree stays. A path obstructed by untracked or ignored content
// refuses the whole batch untouched rather than destroying it.
func (r *Repo) ResetFiles(rev string, paths ...string) error {
	if strings.TrimSpace(rev) == "" || len(paths) == 0 {
		return fmt.Errorf("missing revision or paths")
	}
	args := append([]string{"--literal-pathspecs"}, withEndOfOptions([]string{"ls-tree", "-r", "--name-only", "-z"}, rev)...)
	args = append(args, "--")
	out, err := r.run(quick, append(args, paths...)...)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			have[p] = true
		}
	}
	var keep, drop []string
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if have[p] {
			keep = append(keep, p)
		} else {
			drop = append(drop, p)
		}
	}
	// Restoring or removing a path whose place a directory of untracked or
	// ignored files took — or whose parent directory an untracked or ignored
	// file blocks — would make git destroy that content without a word, so
	// one obstructed path refuses the whole reset before anything is
	// written.
	if err := r.checkWorktreeObstructions(paths); err != nil {
		return err
	}
	if len(keep) > 0 {
		co := append([]string{"--literal-pathspecs"}, withEndOfOptions([]string{"checkout"}, rev)...)
		if _, err := r.run(quick, append(append(co, "--"), keep...)...); err != nil {
			return err
		}
	}
	if len(drop) > 0 {
		if _, err := r.run(quick, append([]string{"--literal-pathspecs", "rm", "-r", "-f", "-q", "--ignore-unmatch", "--"}, drop...)...); err != nil {
			return err
		}
	}
	return nil
}

// FormatPatchFiles returns the mailbox patch of the commit named, kept to
// the paths given.
func (r *Repo) FormatPatchFiles(hash string, paths ...string) (string, error) {
	if strings.TrimSpace(hash) == "" || len(paths) == 0 {
		return "", fmt.Errorf("missing commit or paths")
	}
	args := append([]string{"--literal-pathspecs"}, withEndOfOptions([]string{"show", "--format=email", "--first-parent"}, hash)...)
	args = append(args, "--")
	out, err := r.run(quick, append(args, paths...)...)
	return string(out), err
}
