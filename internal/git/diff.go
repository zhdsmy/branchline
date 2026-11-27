package git

import (
	"fmt"
	"strconv"
	"strings"
)

// Detail returns a commit with its message.
func (r *Repo) Detail(hash string) (CommitDetail, error) {
	out, err := r.run(quick, "show", "-s", "--decorate=short", "--format="+detailFormat, hash, "--")
	if err != nil {
		return CommitDetail{}, err
	}
	return ParseDetail(out), nil
}

// CommitDiff returns the patch of a commit against its first parent, or
// all of it for a root commit.
func (r *Repo) CommitDiff(c Commit) (string, error) {
	if len(c.Parents) == 0 {
		return r.RangeDiff("", c.Hash)
	}
	return r.RangeDiff(c.Parents[0], c.Hash)
}

// RangeDiff returns the patch from one commit to another. An empty from is a
// comparison against an empty tree, which is how a root commit is shown.
// WorktreeDiff returns the patch from rev to the working tree, including
// untracked files. Tracked changes use git diff; each untracked file is compared
// as a new file so both kinds appear in one patch.
func (r *Repo) WorktreeDiff(rev string) (string, error) {
	if strings.TrimSpace(rev) == "" {
		return "", fmt.Errorf("missing commit")
	}
	out, err := r.run(quick, "diff", rev, "--")
	if err != nil {
		return string(out), err
	}
	st, err := r.Status()
	if err != nil {
		return "", err
	}
	var extra strings.Builder
	extra.Write(out)
	for _, f := range append(st.Staged, st.Unstaged...) {
		if f.Status != '?' {
			continue
		}
		part, err := r.run(quick, "diff", "--no-index", "--no-ext-diff", "--", "/dev/null", f.Path)
		if err != nil && ExitCode(err) != 1 {
			return "", err
		}
		extra.Write(part)
	}
	return extra.String(), nil
}

// WorktreeFileDiffOpts returns one literal file's changes from rev to the working tree.
func (r *Repo) WorktreeFileDiffOpts(rev string, f FileChange, o DiffOptions) (string, error) {
	if strings.TrimSpace(rev) == "" {
		return "", fmt.Errorf("missing commit")
	}
	if f.Status == '?' || f.Status == '!' {
		args := []string{"diff", "--no-index", "--no-ext-diff"}
		if o.IgnoreSpace {
			args = append(args, "-w")
		}
		args = append(args, contextArgs(o)...)
		args = append(args, "--", "/dev/null", f.Path)
		out, err := r.run(quick, args...)
		if ExitCode(err) == 1 {
			err = nil
		}
		return string(out), err
	}
	args := append([]string{"--literal-pathspecs"}, diffArgs(o)...)
	args = append(args, rev, "--", f.Path)
	if f.OrigPath != "" && f.OrigPath != f.Path {
		args = append(args, f.OrigPath)
	}
	out, err := r.run(quick, args...)
	return string(out), err
}

func (r *Repo) RangeDiff(from, to string) (string, error) {
	return r.RangeDiffOpts(from, to, DiffOptions{})
}

// DiffOptions change how a patch is built.
type DiffOptions struct {
	// IgnoreSpace ignores changes of whitespace, as git diff -w.
	IgnoreSpace bool
	// Context is the number of unchanged lines around a change: 0 leaves
	// git's default of 3, and a negative number asks for none, as -U0.
	Context int
}

func (r *Repo) RangeDiffOpts(from, to string, o DiffOptions) (string, error) {
	if to == "" {
		return "", fmt.Errorf("missing commit")
	}
	base := from
	if base == "" {
		base = emptyTree
	}
	out, err := r.run(quick, append(diffArgs(o), base, to, "--")...)
	return string(out), err
}

func diffArgs(o DiffOptions) []string {
	args := []string{"diff", "-M", "--no-ext-diff"}
	if o.IgnoreSpace {
		args = append(args, "-w")
	}
	return append(args, contextArgs(o)...)
}

// FileStat is a file a commit changes, with the lines added and removed.
type FileStat struct {
	Path           string
	Added, Removed int
}

// CommitFileStats lists the files each commit changes, newest first, with
// the lines added and removed. Rename detection follows a file through a
// move, and a merge is counted against its first parent, as the log does.
func (r *Repo) CommitFileStats() ([]Commit, [][]FileStat, error) {
	out, err := r.run(slow, "log", "--date-order", "--no-renames",
		"--format=format:%x1e%H%x1f%an%x1f%ae%x1f%at%x1f%s", "--numstat", "HEAD")
	if err != nil {
		return nil, nil, err
	}
	commits, stats := ParseFileStats(out)
	return commits, stats, nil
}

// CommitFiles lists the files a commit changes against its first parent,
// without their patches: for commits whose patch is too large to load.
func (r *Repo) CommitFiles(c Commit) ([]FileChange, error) {
	if len(c.Parents) == 0 {
		return r.RangeFiles("", c.Hash)
	}
	return r.RangeFiles(c.Parents[0], c.Hash)
}

// RangeFiles lists files changed between two commits, without their patches.
// An empty from compares to against the empty tree.
func (r *Repo) RangeFiles(from, to string) ([]FileChange, error) {
	if to == "" {
		return nil, fmt.Errorf("missing commit")
	}
	var out []byte
	var err error
	if from == "" {
		out, err = r.run(quick, "diff", "-M", "--name-status", "-z", emptyTree, to, "--")
	} else {
		out, err = r.run(quick, "diff", "-M", "--name-status", "-z", from, to, "--")
	}
	if err != nil {
		return nil, err
	}
	return ParseNameStatus(out), nil
}

// CommitFileDiff returns the patch of one file of a commit.
func (r *Repo) CommitFileDiff(c Commit, f FileChange) (string, error) {
	return r.CommitFileDiffOpts(c, f, DiffOptions{})
}

// CommitFileDiffOpts is CommitFileDiff with options such as whether
// whitespace changes are ignored.
func (r *Repo) CommitFileDiffOpts(c Commit, f FileChange, o DiffOptions) (string, error) {
	if len(c.Parents) == 0 {
		return r.RangeFileDiffOpts("", c.Hash, f, o)
	}
	return r.RangeFileDiffOpts(c.Parents[0], c.Hash, f, o)
}

// RangeFileDiff returns the patch of one literal file between two commits.
func (r *Repo) RangeFileDiff(from, to string, f FileChange) (string, error) {
	return r.RangeFileDiffOpts(from, to, f, DiffOptions{})
}

func (r *Repo) RangeFileDiffOpts(from, to string, f FileChange, o DiffOptions) (string, error) {
	if to == "" {
		return "", fmt.Errorf("missing commit")
	}
	paths := []string{f.Path}
	if f.OrigPath != "" {
		paths = append(paths, f.OrigPath)
	}
	base := from
	if base == "" {
		base = emptyTree
	}
	args := append([]string{"--literal-pathspecs"}, diffArgs(o)...)
	args = append(args, base, to, "--")
	out, err := r.run(quick, append(args, paths...)...)
	return string(out), err
}

// ShowFile opens one file in an external diff tool. base and changed
// are revisions; an empty base is the empty tree, and an empty changed
// revision is the working tree. "index" compares staged content with base.
func (r *Repo) ShowFile(tool, base, changed, path string) error {
	return r.ShowFileWith(tool, "", base, changed, path)
}

// ShowFileWith is ShowFile for a tool defined by a command, which git runs
// through the shell with $LOCAL and $REMOTE set. An empty command names a
// tool git knows. A rename includes its old and new paths. Paths are literal,
// as a file selected in the app must not expand to a Git pathspec pattern.
func (r *Repo) ShowFileWith(tool, command, base, changed string, paths ...string) error {
	if strings.TrimSpace(tool) == "" {
		return fmt.Errorf("no external diff tool is configured")
	}
	if len(paths) == 0 {
		return fmt.Errorf("missing file to compare")
	}
	for _, path := range paths {
		if path == "" {
			return fmt.Errorf("missing file to compare")
		}
	}
	args := []string{"--literal-pathspecs", "difftool", "--no-prompt", "--tool=" + tool, "-M"}
	if command != "" {
		args = append([]string{"-c", "difftool." + tool + ".cmd=" + command}, args...)
	}
	switch {
	case changed == "index":
		args = append(args, "--cached")
		if base != "" {
			args = append(args, base)
		}
	case changed == "":
		if base != "" {
			args = append(args, base)
		}
	default:
		if base == "" {
			base = emptyTree
		}
		args = append(args, base, changed)
	}
	args = append(append(args, "--"), paths...)
	_, err := r.run(slow, args...)
	return err
}

// FileDiff returns the patch of a changed file: what is staged, or what
// the working tree changes from the index.
func (r *Repo) FileDiff(f FileChange) (string, error) {
	return r.FileDiffOpts(f, DiffOptions{})
}

// FileDiffOpts is FileDiff with options such as the number of context
// lines and whether whitespace changes are ignored. A repository on a
// server runs the same git diff.
func (r *Repo) FileDiffOpts(f FileChange, o DiffOptions) (string, error) {
	var args []string
	switch {
	case f.Status == '?', f.Status == '!':
		args = []string{"diff", "--no-index", "--no-ext-diff"}
	case f.Staged:
		args = []string{"diff", "--cached", "-M", "--no-ext-diff"}
	default:
		args = []string{"diff", "--no-ext-diff"}
	}
	if o.IgnoreSpace {
		args = append(args, "-w")
	}
	args = append(args, contextArgs(o)...)
	args = append(args, "--")
	if f.Status == '?' || f.Status == '!' {
		args = append(args, "/dev/null")
	}
	out, err := r.run(quick, append(args, f.Path)...)
	if (f.Status == '?' || f.Status == '!') && ExitCode(err) == 1 {
		err = nil // --no-index exits with 1 for files that differ
	}
	return string(out), err
}

func contextArgs(o DiffOptions) []string {
	switch {
	case o.Context > 0:
		return []string{"-U" + strconv.Itoa(o.Context)}
	case o.Context < 0:
		return []string{"-U0"}
	}
	return nil
}

// UntrackedLines counts the lines of an untracked file with git diff
// --numstat, which prints one line however large the file is, so that a
// large file is not sent over an SSH connection before it is asked for.
// binary is set for a file git shows no lines of.
func (r *Repo) UntrackedLines(path string) (lines int, binary bool, err error) {
	out, err := r.run(quick, "diff", "--no-index", "--no-ext-diff", "--numstat", "--", "/dev/null", path)
	if ExitCode(err) == 1 {
		err = nil
	}
	if err != nil {
		return 0, false, err
	}
	added, _, ok := strings.Cut(strings.TrimSpace(string(out)), "	")
	if !ok {
		return 0, false, nil
	}
	if added == "-" {
		return 0, true, nil
	}
	n, err := strconv.Atoi(added)
	if err != nil {
		return 0, false, fmt.Errorf("git diff --numstat returned %q", out)
	}
	return n, false, nil
}

// StagedDiff returns the patch of what is staged, the whole index when
// paths is empty.
func (r *Repo) StagedDiff(paths ...string) (string, error) {
	return r.StagedDiffOpts(paths, DiffOptions{})
}

// StagedDiffOpts is StagedDiff with options such as whether whitespace
// changes are ignored.
func (r *Repo) StagedDiffOpts(paths []string, o DiffOptions) (string, error) {
	args := []string{"diff", "--cached", "-M", "--no-ext-diff"}
	if o.IgnoreSpace {
		args = append(args, "-w")
	}
	args = append(args, contextArgs(o)...)
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := r.run(quick, args...)
	return string(out), err
}

// ApplyPatch applies a patch file without committing it.
func (r *Repo) ApplyPatch(path string) error {
	_, err := r.run(quick, "apply", "--index", "--", path)
	return err
}

// FormatPatch returns the mailbox patch of the commit named. A merge is
// shown against its first parent, which is the commit the history selected.
func (r *Repo) FormatPatch(hash string) (string, error) {
	if strings.TrimSpace(hash) == "" {
		return "", fmt.Errorf("missing commit")
	}
	out, err := r.run(quick, "show", "--format=email", "--first-parent", hash)
	return string(out), err
}

// ApplyPatchText applies a unified diff to the working tree and the index.
func (r *Repo) ApplyPatchText(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("the patch is empty")
	}
	_, err := r.runInput(quick, []byte(patch), "apply", "--unidiff-zero", "-")
	return err
}

// FormatRange returns one mailbox patch of the commits after base through tip.
// base is excluded. The patch can be applied on another clone.
func (r *Repo) FormatRange(base, tip string) (string, error) {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(tip) == "" {
		return "", fmt.Errorf("missing commit")
	}
	out, err := r.run(quick, "format-patch", "--stdout", base+".."+tip)
	return string(out), err
}
