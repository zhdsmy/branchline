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
// rest of the tree stays.
func (r *Repo) ResetFiles(rev string, paths ...string) error {
	if strings.TrimSpace(rev) == "" || len(paths) == 0 {
		return fmt.Errorf("missing revision or paths")
	}
	args := append([]string{"--literal-pathspecs", "ls-tree", "-r", "--name-only", "-z", rev, "--"}, paths...)
	out, err := r.run(quick, args...)
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
	if len(keep) > 0 {
		if _, err := r.run(quick, append([]string{"--literal-pathspecs", "checkout", rev, "--"}, keep...)...); err != nil {
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
	args := append([]string{"--literal-pathspecs", "show", "--format=email", "--first-parent", hash, "--"}, paths...)
	out, err := r.run(quick, args...)
	return string(out), err
}
