package git

import "strings"

// leadingDash reports whether any of values starts with "-". Git reads an
// argument starting with "-" as its own command-line options wherever it
// appears before the end-of-options separator, so such a value — a branch,
// tag or remote name, a revision — must be put behind one. Values that do
// not start with "-" need no separator: the commands keep the exact
// argument list they have always had, and only a hostile or unusual name
// changes it.
func leadingDash(values ...string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, "-") {
			return true
		}
	}
	return false
}

// withDashdash inserts the "--" separator before the positional arguments
// of a command that takes no pathspec — push, fetch, branch, tag, remote,
// merge, rebase, merge-tree, stash apply/pop/drop — but only when one of
// them starts with "-". The separator ends option parsing for every
// argument after it, so a name like --receive-pack=./script stays the
// reference it is instead of choosing the program git-push runs. Options
// of the command itself must already be in args; they keep working in
// every case, and without a "-" value the argument list is unchanged.
// It is not for cherry-pick, revert or git stash show — those read an
// argument behind "--" as a pathspec or misuse it, withEndOfOptions is
// theirs — nor for pull, which strips the separator before forwarding the
// positional arguments to the git fetch it spawns; pull refuses such
// names instead.
func withDashdash(args []string, positional ...string) []string {
	if leadingDash(positional...) {
		args = append(args, "--")
	}
	return append(args, positional...)
}

// withEndOfOptions inserts "--end-of-options" before the revision arguments
// of a command whose revisions share the command line with a pathspec —
// checkout, reset, diff, log, show — and of cherry-pick, revert and
// git stash show, where "--" would read as a pathspec separator or be
// misused — but only when one of the revisions starts with "-". There
// "--" cannot protect a revision: it separates revisions from paths, so a
// revision before it is still read as options. Pseudo-revisions of the
// caller's own construction (--branches, --all) are options and must not
// be part of revs. Without a "-" value the argument list is unchanged.
func withEndOfOptions(args []string, revs ...string) []string {
	if leadingDash(revs...) {
		args = append(args, "--end-of-options")
	}
	return append(args, revs...)
}
