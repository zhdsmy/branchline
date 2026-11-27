package git

import "time"

// Commit is a commit of the log.
type Commit struct {
	Hash        string
	Parents     []string
	Author      string
	AuthorEmail string
	Date        time.Time
	Subject     string
	// Refs are the names pointing at the commit, as git log decorates it.
	Refs []Ref
}

// Short returns the first 7 characters of the hash.
func (c Commit) Short() string { return short(c.Hash) }

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// RefKind is what a reference names.
type RefKind int

const (
	RefBranch RefKind = iota
	RefRemote
	RefTag
	RefHead
)

// Ref is a reference decorating a commit.
type Ref struct {
	Name string // short, such as main, origin/main or v1.0
	Kind RefKind
	// Current is the branch HEAD points to.
	Current bool
}

// CommitDetail is a commit with its message and committer.
type CommitDetail struct {
	Commit
	Committer      string
	CommitterEmail string
	CommitDate     time.Time
	Message        string
}

// Branch is a local or a remote-tracking branch.
type Branch struct {
	Name     string // short
	FullName string // refs/heads/… or refs/remotes/…
	Hash     string
	Remote   string // the remote of a remote-tracking branch
	Upstream string // a local branch's upstream, short
	Ahead    int
	Behind   int
	Gone     bool // the upstream is gone
	Current  bool
}

// Tag is a tag and the commit it names.
type Tag struct {
	Name string
	Hash string
}

// Refs are the repository's branches, remotes and tags.
type Refs struct {
	Branches []Branch
	Remote   []Branch
	Tags     []Tag
	Remotes  []string
	// Default is the branch origin/HEAD names, without the remote.
	Default string
}

// Stash is an entry of the stash.
type Stash struct {
	Index   int
	Name    string // stash@{0}
	Hash    string
	Date    time.Time
	Message string
}

// FileChange is a file of the index or the working tree that changed.
type FileChange struct {
	Path     string
	OrigPath string // the path before a rename or copy
	// Status is a letter: M modified, A added, D deleted, R renamed, C
	// copied, T type changed, U conflicted, ? untracked, ! ignored.
	Status byte
	Staged bool
	// Conflict is the two-letter unmerged status of a conflicted file:
	// UU, AA, DD, DU, UD, AU or UA. It is empty unless Status is U.
	Conflict string
}

// Status is the state of the working tree.
type Status struct {
	Branch   string // the current branch, "" when detached
	Head     string // the hash of HEAD, "" before the first commit
	Upstream string
	Ahead    int
	Behind   int
	Staged   []FileChange
	Unstaged []FileChange
	// Conflicts are the unmerged paths with their index stage metadata,
	// one per conflicted file.
	Conflicts []ConflictStage
	// State is an operation in progress: "merge", "rebase", "cherry-pick"
	// or "revert", "" for none.
	State string
	// Stopped says why a rebase is waiting: "conflict", "edit" or "message".
	Stopped string
	// Operation identifies the stopped operation already shown to the user.
	// A merge, cherry-pick, revert, or rebase uses its target commit. An AM session
	// uses its patch count and the hash of the patch it stopped on.
	Operation string
	// Ignored are untracked paths excluded by .gitignore, filled only when
	// the view asks to show them.
	Ignored []FileChange
}

// Conflicted reports a file with conflicts.
func (s Status) Conflicted() bool {
	for _, f := range s.Unstaged {
		if f.Status == 'U' {
			return true
		}
	}
	return false
}

// zeroFileMode is the mode git prints for a missing stage or file.
const zeroFileMode = "000000"

// ConflictStage is what the index records for one unmerged path: the
// two-letter conflict code, and the mode and object of the base, ours and
// theirs stages. A missing stage has a zero mode and a zero object id;
// the worktree has only a mode, zero when the file is gone.
type ConflictStage struct {
	Path string
	// Code is the unmerged status: UU, AA, DD, DU, UD, AU or UA.
	Code string
	// Submodule is the submodule state git reports, such as "N...".
	Submodule    string
	BaseMode     string
	OursMode     string
	TheirsMode   string
	WorktreeMode string
	BaseOID      string
	OursOID      string
	TheirsOID    string
}

// ConflictStage returns the unmerged stage metadata of path, if any.
func (s Status) ConflictStage(path string) (ConflictStage, bool) {
	for _, c := range s.Conflicts {
		if c.Path == path {
			return c, true
		}
	}
	return ConflictStage{}, false
}

// oidPresent reports an object id that names a real object, not the
// zero id git prints for a missing stage, in any hash width.
func oidPresent(oid string) bool {
	for i := 0; i < len(oid); i++ {
		if oid[i] != '0' {
			return true
		}
	}
	return false
}

// HaveBase reports a base (stage 1) entry for the conflict.
func (c ConflictStage) HaveBase() bool {
	return oidPresent(c.BaseOID) || c.BaseMode != zeroFileMode
}

// HaveOurs reports an ours (stage 2) entry for the conflict.
func (c ConflictStage) HaveOurs() bool {
	return oidPresent(c.OursOID) || c.OursMode != zeroFileMode
}

// HaveTheirs reports a theirs (stage 3) entry for the conflict.
func (c ConflictStage) HaveTheirs() bool {
	return oidPresent(c.TheirsOID) || c.TheirsMode != zeroFileMode
}

// WorktreeExists reports the conflicted file is present in the working tree.
func (c ConflictStage) WorktreeExists() bool {
	return c.WorktreeMode != zeroFileMode
}

// IsSubmodule reports the conflict is on a gitlink: any present stage or
// the worktree file has the submodule mode.
func (c ConflictStage) IsSubmodule() bool {
	for _, mode := range []string{c.BaseMode, c.OursMode, c.TheirsMode, c.WorktreeMode} {
		if mode == "160000" {
			return true
		}
	}
	return false
}

// IndexFlag is a bit git update-index stores on a tracked path.
// AssumeUnchanged hides working-tree edits from status. SkipWorktree
// tells checkout and merge to leave the working tree file alone.
type IndexFlag int

const (
	AssumeUnchanged IndexFlag = iota
	SkipWorktree
)

// IndexFlags are the assume-unchanged and skip-worktree bits of one path.
type IndexFlags struct {
	Path            string
	AssumeUnchanged bool
	SkipWorktree    bool
}

// Worktree is a checkout linked to the repository.
type Worktree struct {
	Path    string
	Head    string
	Branch  string // short branch name, empty when detached
	Current bool   // the checkout this window is using
}
