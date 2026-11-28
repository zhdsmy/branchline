package git

import (
	"strconv"
	"strings"
)

// WriteCommitGraphIfLarge writes a commit-graph covering every reachable
// commit when the repository's history is large enough to be slow without
// one, and reports whether it wrote. The file makes walking the history —
// the first page of the log included — an order of magnitude faster on
// repositories of tens of thousands of commits, where sorting by topology
// otherwise parses every commit object. Nothing is written, and (false, nil)
// is returned, when any of these holds:
//
//   - a commit-graph already exists, as a single file or a split chain. An
//     old graph still accelerates the commits it covers, so it is not
//     refreshed either.
//   - the clone is shallow: its boundary breaks the reachability the graph
//     assumes, and a shallow history is small by definition.
//   - the user turned commit-graphs off, in core.commitGraph or in
//     gc.writeCommitGraph.
//   - fewer than minCommits commits are reachable.
//
// The write asks for changed-path Bloom filters in version 2, so walks
// with a path — file history, and blame where it can use them — skip the
// commits that never touched the path instead of parsing and diffing them
// all. The filters make writing once cost a few seconds of CPU on a large
// history instead of a fraction of one, but every later read of the graph
// keeps them: the user's own git carries them through rewrites, garbage
// collection and split-chain appends. Version 2 is fixed because the hash
// of version 1 depends on the signedness of the platform's char, so a
// graph written by an x86_64 git — including one under Rosetta — and one
// written on Apple silicon disagree for paths with bytes of 0x80 and up,
// and the mismatched one drops commits from path walks.
//
// The user's own settings come first: when commitGraph.changedPaths,
// commitGraph.changedPathsVersion or commitGraph.readChangedPaths is set,
// in any scope or on the command line, the write carries neither flag and
// git decides by that configuration. Older versions of git get what they
// understand: one that does not know commitGraph.changedPathsVersion
// writes the version-1 filters it knows, and one too old for
// --changed-paths fails the write as an unknown option.
//
// A git command failing returns the error; the caller decides what it
// means. Nothing else is written and no configuration is changed.
func (r *Repo) WriteCommitGraphIfLarge(minCommits int) (bool, error) {
	has, err := r.hasCommitGraph()
	if err != nil {
		return false, err
	}
	if has {
		return false, nil
	}
	shallow, err := r.isShallowRepository()
	if err != nil {
		return false, err
	}
	if shallow {
		return false, nil
	}
	for _, key := range []string{"core.commitGraph", "gc.writeCommitGraph"} {
		off, err := r.configBoolFalse(key)
		if err != nil {
			return false, err
		}
		if off {
			return false, nil
		}
	}
	count, err := r.reachableCommitCount()
	if err != nil {
		return false, err
	}
	if count < minCommits {
		return false, nil
	}
	args := []string{"commit-graph", "write", "--reachable"}
	configured, err := r.changedPathsConfigured()
	if err != nil {
		return false, err
	}
	if !configured {
		args = append([]string{"-c", "commitGraph.changedPathsVersion=2"}, append(args, "--changed-paths")...)
	}
	if _, err := r.run(slow, args...); err != nil {
		return false, err
	}
	return true, nil
}

// changedPathsConfigured reports whether any of the three changed-path
// settings — commitGraph.changedPaths, commitGraph.changedPathsVersion or
// commitGraph.readChangedPaths — has a value in any scope git reads. git
// lowercases the section and key of every configuration entry it prints,
// so one lowercase pattern matches them however they were written, and
// values passed as -c on this command line count as much as files do.
// git config --get-regexp exits 1 for no match, which reads as unset; any
// other failure is an error.
func (r *Repo) changedPathsConfigured() (bool, error) {
	out, err := r.run(quick, "config", "--get-regexp", `^commitgraph\.(changedpaths|changedpathsversion|readchangedpaths)$`)
	if err != nil {
		if ExitCode(err) == 1 && strings.TrimSpace(string(out)) == "" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// hasCommitGraph reports whether objects/info/commit-graph or the chain of a
// split commit-graph exists. The paths go through git rev-parse --git-path so
// a linked worktree resolves them in the shared objects folder of its main
// repository rather than in its own git file.
func (r *Repo) hasCommitGraph() (bool, error) {
	out, err := r.Script(
		`graph=$(git rev-parse --git-path objects/info/commit-graph) || exit 1; ` +
			`chain=$(git rev-parse --git-path objects/info/commit-graphs/commit-graph-chain) || exit 1; ` +
			`if [ -f "$graph" ] || [ -f "$chain" ]; then echo yes; else echo no; fi`)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

// isShallowRepository reports whether the repository is a shallow clone.
func (r *Repo) isShallowRepository() (bool, error) {
	out, err := r.run(quick, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// configBoolFalse reports whether key is set to a boolean false. An unset
// key is not false: both commit-graph settings default to on. git config
// exits 1 for an unset key and prints nothing, which reads as unset; any
// other failure is an error.
func (r *Repo) configBoolFalse(key string) (bool, error) {
	out, err := r.run(quick, "config", "--bool", key)
	if err != nil {
		if ExitCode(err) == 1 && strings.TrimSpace(string(out)) == "" {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) == "false", nil
}

// reachableCommitCount counts the commits reachable from every ref, the same
// set commit-graph write --reachable covers.
func (r *Repo) reachableCommitCount() (int, error) {
	out, err := r.run(quick, "rev-list", "--count", "--all")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
