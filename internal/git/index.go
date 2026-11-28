package git

import (
	"fmt"
	pathpkg "path"
	"strings"
)

// Stage adds paths to the index, deletions too.
func (r *Repo) Stage(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	return r.runPathBatches([]string{"add", "-A", "--"}, paths)
}

// StageAll adds every change to the index.
func (r *Repo) StageAll() error {
	_, err := r.run(quick, "add", "-A")
	return err
}

// Unstage takes paths out of the index, keeping their changes.
func (r *Repo) Unstage(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		// Before the first commit there is no HEAD to restore from.
		return r.runPathBatches([]string{"rm", "--cached", "-r", "-q", "--"}, paths)
	}
	return r.runPathBatches([]string{"reset", "-q", "HEAD", "--"}, paths)
}

// UnstageAll empties the index of changes.
func (r *Repo) UnstageAll() error {
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		_, err := r.run(quick, "rm", "--cached", "-r", "-q", ".")
		return err
	}
	_, err := r.run(quick, "reset", "-q")
	return err
}

// Discard throws away the working tree's changes to files: untracked
// files are deleted.
func (r *Repo) Discard(files ...FileChange) error {
	var tracked, untracked, ignored []string
	for _, f := range files {
		switch f.Status {
		case '?':
			untracked = append(untracked, f.Path)
		case '!':
			ignored = append(ignored, f.Path)
		default:
			tracked = append(tracked, f.Path)
		}
	}
	if len(tracked) > 0 {
		if err := r.runPathBatches([]string{"--literal-pathspecs", "checkout", "--"}, tracked); err != nil {
			return err
		}
	}
	if len(untracked) > 0 {
		if err := r.runPathBatches([]string{"--literal-pathspecs", "clean", "-f", "-q", "--"}, untracked); err != nil {
			return err
		}
	}
	if len(ignored) > 0 {
		// -x is required: plain clean leaves paths excluded by .gitignore.
		if err := r.runPathBatches([]string{"--literal-pathspecs", "clean", "-f", "-x", "-q", "--"}, ignored); err != nil {
			return err
		}
	}
	return nil
}

// StagePatch stages the selected hunk of a unified diff.
// ApplyLines applies a patch made of some lines of a file's diff: to the
// index when cached, to the working tree otherwise, and backwards when
// reverse. --unidiff-zero lets hunks without context lines apply.
func (r *Repo) ApplyLines(patch string, cached, reverse bool) error {
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("the patch is empty")
	}
	args := []string{"apply", "--unidiff-zero", "--whitespace=nowarn"}
	if cached {
		args = append(args, "--cached")
	}
	if reverse {
		args = append(args, "--reverse")
	}
	_, err := r.runInput(quick, []byte(patch), append(args, "-")...)
	return err
}

func (r *Repo) StagePatch(patch string) error {
	_, err := r.runInput(quick, []byte(patch), "apply", "--cached", "--unidiff-zero", "-")
	return err
}

// PathHash resolves a git revision of a path, as HEAD:logo.png or :logo.png
// for the index. It works for a local repository and one reached over SSH.
func (r *Repo) PathHash(spec string) (string, error) {
	out, err := r.run(quick, "rev-parse", "-q", "--verify", spec)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(out))
	if !isHex(hash) {
		return "", fmt.Errorf("git rev-parse returned %q", hash)
	}
	return hash, nil
}

// ReadWorktree reads a file of the working tree. It works for a local
// repository and one reached over SSH, because git itself reads the file.
func (r *Repo) ReadWorktree(path string) (string, error) {
	// hash-object reads the working tree, including conflict markers.
	// Writing the blob lets cat-file return it on a later SSH command.
	out, err := r.run(quick, "hash-object", "-w", "--", path)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(out))
	if !isHex(hash) {
		return "", fmt.Errorf("git hash-object returned %q", hash)
	}
	out, err = r.run(quick, "cat-file", "blob", hash)
	return string(out), err
}

// CheckIgnore returns the untracked paths matched by patterns. Patterns are
// one per line. A pattern matches a path or any path inside a directory.
// Already tracked files stay out, and the working tree is not changed.
func (r *Repo) CheckIgnore(patterns string) ([]string, error) {
	var rules []string
	for _, line := range strings.Split(patterns, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		rules = append(rules, strings.TrimPrefix(line, "/"))
	}
	if len(rules) == 0 {
		return nil, nil
	}
	st, err := r.Status()
	if err != nil {
		return nil, err
	}
	var matched []string
	for _, f := range st.Unstaged {
		if f.Status != '?' {
			continue
		}
		for _, rule := range rules {
			if ignoreMatches(rule, f.Path) {
				matched = append(matched, f.Path)
				break
			}
		}
	}
	return matched, nil
}

func ignoreMatches(rule, path string) bool {
	dir := strings.HasSuffix(rule, "/")
	rule = strings.Trim(rule, "/")
	if rule == "" {
		return false
	}
	if ok, err := pathpkg.Match(rule, path); err == nil && ok {
		return true
	}
	if ok, err := pathpkg.Match(rule, pathpkg.Base(path)); err == nil && ok && !strings.Contains(rule, "/") {
		return true
	}
	prefix := rule + "/"
	return strings.HasPrefix(path, prefix) || (dir && path == rule)
}

// AppendWorktree adds a line to a file in the working tree, creating it when it is absent.
func (r *Repo) AppendWorktree(path, line string) error {
	text, err := r.ReadWorktree(path)
	if err != nil {
		text = ""
	}
	line = strings.TrimRight(line, "\r\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += line + "\n"
	return r.WriteWorktree(path, text)
}

// WriteWorktree replaces a working-tree file and stages it. The content
// goes through git hash-object, so the same path works over SSH.
func (r *Repo) WriteWorktree(path, content string) error {
	out, err := r.runInput(quick, []byte(content), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	hash := strings.TrimSpace(string(out))
	if !isHex(hash) {
		return fmt.Errorf("git hash-object returned %q", hash)
	}
	if _, err := r.run(quick, "update-index", "--add", "--cacheinfo", "100644,"+hash+","+path); err != nil {
		return err
	}
	_, err = r.run(quick, "checkout-index", "-f", "--", path)
	return err
}

// RestoreFile puts one file back to the revision named. The rest of the tree stays.
func (r *Repo) RestoreFile(rev, path string) error {
	if strings.TrimSpace(rev) == "" || strings.TrimSpace(path) == "" {
		return fmt.Errorf("missing revision or path")
	}
	_, err := r.run(quick, append(withEndOfOptions([]string{"checkout"}, rev), "--", path)...)
	return err
}

// SaveSnapshot commits the index on a snapshot branch, then returns to the
// branch that was current. Changes that are not staged stay in the working tree.
func (r *Repo) SaveSnapshot(branch, message string) error {
	branch = strings.TrimSpace(branch)
	message = strings.TrimSpace(message)
	if branch == "" || message == "" {
		return fmt.Errorf("a snapshot needs a branch and a message")
	}
	if err := r.requireStashStaged(); err != nil {
		return err
	}
	st, err := r.Status()
	if err != nil {
		return err
	}
	if st.Branch == "" {
		return fmt.Errorf("HEAD is detached")
	}
	if st.Branch == branch {
		return fmt.Errorf("the snapshot branch is the current branch")
	}
	if len(st.Staged) == 0 {
		return fmt.Errorf("nothing is staged")
	}
	cur := st.Branch
	if _, err := r.run(quick, "stash", "push", "--staged", "-m", "Branchline snapshot"); err != nil {
		return err
	}
	created := false
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		if _, err := r.run(quick, withDashdash([]string{"branch"}, branch, cur)...); err != nil {
			_, _ = r.run(quick, "stash", "pop")
			return err
		}
		created = true
	}
	if _, err := r.run(quick, append(withEndOfOptions([]string{"checkout"}, branch), "--")...); err != nil {
		if created {
			_, _ = r.run(quick, withDashdash([]string{"branch", "-D"}, branch)...)
		}
		_, _ = r.run(quick, "stash", "pop")
		return err
	}
	if _, err := r.run(quick, "stash", "pop"); err != nil {
		_, _ = r.run(quick, append(withEndOfOptions([]string{"checkout"}, cur), "--")...)
		return err
	}
	if _, err := r.run(quick, "commit", "-m", message); err != nil {
		_, _ = r.run(quick, "reset", "-q")
		_, _ = r.run(quick, append(withEndOfOptions([]string{"checkout"}, cur), "--")...)
		return err
	}
	_, err = r.run(quick, append(withEndOfOptions([]string{"checkout"}, cur), "--")...)
	return err
}

// RestoreWorktree replaces one working-tree file with the revision named.
// The index is read back afterward, so a staged change to the file stays staged.
func (r *Repo) RestoreWorktree(rev, path string) error {
	if strings.TrimSpace(rev) == "" || strings.TrimSpace(path) == "" {
		return fmt.Errorf("missing revision or path")
	}
	staged, err := r.run(quick, "rev-parse", "-q", "--verify", ":"+path)
	haveStaged := err == nil && isHex(strings.TrimSpace(string(staged)))
	out, err := r.run(quick, withEndOfOptions([]string{"show"}, rev+":"+path)...)
	if err != nil {
		return err
	}
	hashed, err := r.runInput(quick, out, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	hash := strings.TrimSpace(string(hashed))
	if !isHex(hash) {
		return fmt.Errorf("git hash-object returned %q", hash)
	}
	if _, err := r.run(quick, "update-index", "--add", "--cacheinfo", "100644,"+hash+","+path); err != nil {
		return err
	}
	if _, err := r.run(quick, "checkout-index", "-f", "--", path); err != nil {
		return err
	}
	if haveStaged {
		_, err = r.run(quick, "update-index", "--cacheinfo", "100644,"+strings.TrimSpace(string(staged))+","+path)
		return err
	}
	_, err = r.run(quick, "reset", "-q", "HEAD", "--", path)
	return err
}

// IndexFlags lists tracked paths whose assume-unchanged or skip-worktree
// bit is set. A path can have both.
func (r *Repo) IndexFlags() ([]IndexFlags, error) {
	out, err := r.run(quick, "ls-files", "-v", "-z")
	if err != nil {
		return nil, err
	}
	return ParseIndexFlags(out), nil
}

// SetIndexFlag sets or clears one index bit on each path. The same git
// update-index command runs for a local repository and one reached over SSH.
func (r *Repo) SetIndexFlag(flag IndexFlag, on bool, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	var bit string
	switch flag {
	case AssumeUnchanged:
		bit = "--assume-unchanged"
		if !on {
			bit = "--no-assume-unchanged"
		}
	case SkipWorktree:
		bit = "--skip-worktree"
		if !on {
			bit = "--no-skip-worktree"
		}
	default:
		return fmt.Errorf("unknown index flag")
	}
	return r.runPathBatches([]string{"update-index", bit, "--"}, paths)
}
