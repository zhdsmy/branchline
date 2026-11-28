package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Refs returns the branches, remote branches and tags. References are
// scanned in two passes: branches and remote branches without peeling, then
// tags peeling each annotated tag to the commit it names. for-each-ref
// prints references ordered by full name, so heads, remotes and tags read
// in the same order one scan would print them.
func (r *Repo) Refs() (Refs, error) {
	out, err := r.run(quick, "for-each-ref", "--format="+refFormatBranches, "refs/heads", "refs/remotes")
	if err != nil {
		return Refs{}, err
	}
	tags, err := r.run(quick, "for-each-ref", "--format="+refFormatTags, "refs/tags")
	if err != nil {
		return Refs{}, err
	}
	refs := ParseRefs(append(out, tags...))
	// Remotes without branches fetched yet show too.
	if out, err := r.run(quick, "remote"); err == nil {
		have := map[string]bool{}
		for _, n := range refs.Remotes {
			have[n] = true
		}
		for _, n := range strings.Fields(string(out)) {
			if !have[n] {
				refs.Remotes = append(refs.Remotes, n)
			}
		}
	}
	if name := r.originDefault(); name != "" {
		refs.Default = name
	}
	return refs, nil
}

// originDefault reads the branch refs/remotes/origin/HEAD points at.
func (r *Repo) originDefault() string {
	out, err := r.run(quick, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	name = strings.TrimPrefix(name, "origin/")
	if name == "" || name == "HEAD" {
		return ""
	}
	return name
}

// Checkout switches to a branch or, detached, to a commit.
// Local changes that would be overwritten stop it.
func (r *Repo) Checkout(rev string) error {
	return r.checkout(rev, false)
}

// CheckoutForce switches to rev and discards local changes that would
// be overwritten. It is git checkout -f.
func (r *Repo) CheckoutForce(rev string) error {
	return r.checkout(rev, true)
}

func (r *Repo) checkout(rev string, force bool) error {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return fmt.Errorf("checkout needs a revision")
	}
	args := []string{"checkout"}
	if force {
		args = append(args, "-f")
	}
	args = withEndOfOptions(args, rev)
	args = append(args, "--")
	_, err := r.run(quick, args...)
	return err
}

// CheckoutRemote makes a local branch tracking a remote branch, such as
// origin/feature, and switches to it; it switches to the local branch
// when it exists.
func (r *Repo) CheckoutRemote(remoteBranch string) error {
	_, local, ok := strings.Cut(remoteBranch, "/")
	if !ok {
		return fmt.Errorf("%s is not a remote branch", remoteBranch)
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "refs/heads/"+local); err == nil {
		return r.Checkout(local)
	}
	args := withEndOfOptions([]string{"checkout", "-b", local, "--track"}, remoteBranch)
	_, err := r.run(quick, append(args, "--")...)
	return err
}

// CreateBranch makes a branch at rev, HEAD when empty, and switches to it
// when checkout is set.
func (r *Repo) CreateBranch(name, rev string, checkout bool) error {
	return r.CreateBranchForce(name, rev, checkout, false)
}

// CreateBranchForce makes a branch at rev. When checkout and force are
// both set, switching to it discards local changes that would be overwritten.
func (r *Repo) CreateBranchForce(name, rev string, checkout, force bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a branch needs a name")
	}
	if rev == "" {
		rev = "HEAD"
	}
	if checkout {
		args := []string{"checkout"}
		if force {
			args = append(args, "-f")
		}
		args = append(args, "-b", name)
		args = withEndOfOptions(args, rev)
		args = append(args, "--")
		_, err := r.run(quick, args...)
		return err
	}
	_, err := r.run(quick, withDashdash([]string{"branch"}, name, rev)...)
	return err
}

// DeleteBranch deletes a local branch, forcing when force is set.
func (r *Repo) DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := r.run(quick, withDashdash([]string{"branch", flag}, name)...)
	return err
}

// RenameBranch renames a local branch.
func (r *Repo) RenameBranch(old, name string) error {
	_, err := r.run(quick, withDashdash([]string{"branch", "-m"}, old, name)...)
	return err
}

// TrackBranch makes local track remoteBranch, such as origin/feature.
// It does not switch branches or change commits.
func (r *Repo) TrackBranch(local, remoteBranch string) error {
	local, remoteBranch = strings.TrimSpace(local), strings.TrimSpace(remoteBranch)
	if local == "" || !strings.Contains(remoteBranch, "/") {
		return fmt.Errorf("tracking needs a local branch and a remote branch")
	}
	_, err := r.run(quick, withDashdash([]string{"branch", "--set-upstream-to", remoteBranch}, local)...)
	return err
}

// Reset moves the current branch to rev: mode is soft, mixed, hard or
// keep. Keep stops instead of losing local changes to files that differ.
func (r *Repo) Reset(rev, mode string) error {
	switch mode {
	case "soft", "mixed", "hard", "keep":
	default:
		return fmt.Errorf("unknown reset mode %q", mode)
	}
	_, err := r.run(quick, append(withEndOfOptions([]string{"reset", "--" + mode}, rev), "--")...)
	return err
}

// CreateTag tags rev, annotated when message is not empty.
func (r *Repo) CreateTag(name, rev, message string) error {
	if message == "" {
		_, err := r.run(quick, withDashdash([]string{"tag"}, name, rev)...)
		return err
	}
	_, err := r.runInput(quick, []byte(message), withDashdash([]string{"tag", "-a", "-F", "-"}, name, rev)...)
	return err
}

// DeleteTag deletes a local tag.
func (r *Repo) DeleteTag(name string) error {
	_, err := r.run(quick, withDashdash([]string{"tag", "-d"}, name)...)
	return err
}

// Branches returns local branch names.
func (r *Repo) CheckoutBranch(name string) error {
	return r.Checkout(name)
}

// CurrentBranch returns the branch checked out, or "" when detached.
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.run(quick, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "HEAD" {
		return "", nil
	}
	return name, nil
}

// DefaultBranch returns the branch HEAD starts from: the remote named
// by origin/HEAD when it exists, otherwise the local init.defaultBranch.
func (r *Repo) DefaultBranch() (string, error) {
	if name := r.originDefault(); name != "" {
		return name, nil
	}
	out, err := r.run(quick, "config", "--get", "init.defaultBranch")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// CheckoutRemoteAs makes local, a new branch tracking remoteBranch such
// as origin/feature, and switches to it. force discards local changes
// that would be overwritten.
func (r *Repo) CheckoutRemoteAs(remoteBranch, local string, force bool) error {
	remoteBranch, local = strings.TrimSpace(remoteBranch), strings.TrimSpace(local)
	if !strings.Contains(remoteBranch, "/") || local == "" {
		return fmt.Errorf("tracking needs a remote branch and a local name")
	}
	args := []string{"checkout"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, "-b", local, "--track")
	args = withEndOfOptions(args, remoteBranch)
	_, err := r.run(quick, append(args, "--")...)
	return err
}

// RemoveBranchOptions are the choices of the Delete Branch dialog.
type RemoveBranchOptions struct {
	Name string
	// Force deletes the branch even when it is not merged.
	Force bool
	// Upstream, such as origin/feature, is deleted from its remote too.
	Upstream string
	// Worktree is a linked checkout of the branch, removed first.
	Worktree string
}

// RemoveBranch deletes a local branch and, as asked, the checkout using
// it and its branch on the remote. The worktree goes first: git keeps a
// branch that a worktree has checked out.
func (r *Repo) RemoveBranch(o RemoveBranchOptions) error {
	name := strings.TrimSpace(o.Name)
	if name == "" {
		return fmt.Errorf("deleting a branch needs its name")
	}
	if o.Worktree != "" {
		if err := r.WorktreeRemove(o.Worktree, false); err != nil {
			return err
		}
	}
	if err := r.DeleteBranch(name, o.Force); err != nil {
		return err
	}
	if o.Upstream != "" {
		return r.DeleteRemoteBranch(o.Upstream)
	}
	return nil
}

// RenameRemoteBranch gives upstream, such as origin/old, the name newName
// on its remote: local is pushed under the new name and tracks it, then
// the old remote branch is deleted.
func (r *Repo) RenameRemoteBranch(local, upstream, newName string) error {
	remote, old, ok := strings.Cut(strings.TrimSpace(upstream), "/")
	local, newName = strings.TrimSpace(local), strings.TrimSpace(newName)
	if !ok || remote == "" || old == "" || newName == "" || local == "" {
		return fmt.Errorf("renaming a remote branch needs the local branch, its upstream and a name")
	}
	if _, err := r.run(slow, withDashdash([]string{"push", "--set-upstream"}, remote, "refs/heads/"+local+":refs/heads/"+newName)...); err != nil {
		return err
	}
	if old == newName {
		return nil
	}
	_, err := r.run(slow, withDashdash([]string{"push", "--delete"}, remote, "refs/heads/"+old)...)
	return err
}

// TagDetail is what Show Tag Details shows.
type TagDetail struct {
	Name string
	// Annotated is false for a lightweight tag, which has no tagger or message.
	Annotated   bool
	Target      string // the commit the tag names
	Tagger      string
	TaggerEmail string
	Date        time.Time
	Message     string
}

// TagInfo reads the tagger, date and message of a tag.
func (r *Repo) TagInfo(name string) (TagDetail, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TagDetail{}, fmt.Errorf("a tag name is required")
	}
	const format = "%(objecttype)%00%(objectname)%00%(*objectname)%00%(taggername)%00%(taggeremail)%00%(taggerdate:unix)%00%(contents)"
	out, err := r.run(quick, "for-each-ref", "--format="+format, "refs/tags/"+name)
	if err != nil {
		return TagDetail{}, err
	}
	if len(out) == 0 {
		return TagDetail{}, fmt.Errorf("there is no tag %s", name)
	}
	f := strings.SplitN(strings.TrimSuffix(string(out), "\n"), "\x00", 7)
	for len(f) < 7 {
		f = append(f, "")
	}
	d := TagDetail{Name: name, Annotated: f[0] == "tag", Target: f[1]}
	if !d.Annotated {
		return d, nil
	}
	d.Target = f[2]
	d.Tagger = f[3]
	d.TaggerEmail = strings.Trim(f[4], "<>")
	if sec, err := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64); err == nil {
		d.Date = time.Unix(sec, 0)
	}
	d.Message = strings.TrimRight(f[6], "\n")
	return d, nil
}

// StartBranch is Lean Branching's Start Branch: it fetches fetchRemote
// when set, then creates name at at, such as origin/main, without
// tracking it, and switches to it. force discards local changes that
// would be overwritten.
func (r *Repo) StartBranch(name, at, fetchRemote string, force bool) error {
	name, at = strings.TrimSpace(name), strings.TrimSpace(at)
	if name == "" || at == "" {
		return fmt.Errorf("a branch needs a name and a start")
	}
	if fetchRemote = strings.TrimSpace(fetchRemote); fetchRemote != "" {
		if err := r.FetchOpts(FetchOptions{Remote: fetchRemote}); err != nil {
			return err
		}
	}
	args := []string{"checkout"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, "--no-track", "-b", name)
	args = withEndOfOptions(args, at)
	_, err := r.run(quick, append(args, "--")...)
	return err
}
