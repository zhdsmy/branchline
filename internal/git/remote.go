package git

import (
	"fmt"
	"strings"
)

// Fetch fetches from every remote, pruning branches gone from them.
func (r *Repo) Fetch() error { return r.FetchOpts(FetchOptions{All: true, Prune: true, Tags: true}) }

// FetchOptions are the choices of Fetch… .
type FetchOptions struct {
	Remote string
	All    bool
	Prune  bool
	Tags   bool
}

func (r *Repo) FetchOpts(o FetchOptions) error {
	args := []string{"fetch"}
	if o.All || o.Remote == "" {
		args = append(args, "--all")
	}
	if o.Prune {
		args = append(args, "--prune")
	}
	if o.Tags {
		args = append(args, "--tags")
	}
	// withDashdash puts the remote name behind "--" when it starts with
	// "-": it comes from the repository's own configuration and must never
	// become command-line options. Every other case keeps the arguments
	// they have always had.
	if o.Remote != "" && !o.All {
		args = withDashdash(args, o.Remote)
	}
	_, err := r.run(slow, args...)
	return err
}

// Pull pulls the current branch's upstream, rebasing when rebase is set.
func (r *Repo) Pull(rebase bool) error { return r.PullOpts(PullOptions{Rebase: rebase}) }

type PullOptions struct {
	Remote string
	Branch string
	Rebase bool
	Tags   bool
	// Stash saves local changes before the pull and puts them back after
	// a clean pull, or after continuing/aborting a conflict. While paused,
	// the exact stash is kept across application restarts.
	Stash bool
}

func (r *Repo) PullOpts(o PullOptions) error {
	_, err := r.PullNote(o)
	return err
}

// UpToDatePull and UpToDatePush are the notes shown after a pull or a push
// that had nothing to bring or to send.
const (
	UpToDatePull = "Already up to date."
	UpToDatePush = "Nothing to push; remote is already up to date."
)

// PullNote is PullOpts that also says UpToDatePull when git found nothing
// to pull.
func (r *Repo) PullNote(o PullOptions) (string, error) {
	if o.Stash {
		if err := r.saveSyncStash("Branchline pull"); err != nil {
			return "", err
		}
	}
	note, err := r.pull(o)
	return note, r.restoreSyncStash(err)
}

func (r *Repo) pull(o PullOptions) (string, error) {
	args := []string{"pull", "--no-edit"}
	if o.Rebase {
		args = append(args, "--rebase")
	} else {
		args = append(args, "--no-rebase")
	}
	if o.Tags {
		args = append(args, "--tags")
	}
	if remote := strings.TrimSpace(o.Remote); remote != "" {
		positional := []string{remote}
		if branch := strings.TrimSpace(o.Branch); branch != "" {
			positional = append(positional, branch)
		}
		args = withDashdash(args, positional...)
	}
	out, err := r.run(slow, args...)
	if err == nil && strings.Contains(string(out), "Already up to date") {
		return UpToDatePull, nil
	}
	return "", err
}

// Push pushes branch to remote, setting it as the upstream when it has
// none, and forcing with lease when force is set.
func (r *Repo) Push(remote, branch string, setUpstream, force, tags bool) error {
	return r.PushTo(remote, branch, "", setUpstream, force, tags)
}

// PushTo is Push with the remote branch named dest. An empty dest is the
// branch's own name.
func (r *Repo) PushTo(remote, branch, dest string, setUpstream, force, tags bool) error {
	_, err := r.PushNote(remote, branch, dest, setUpstream, force, tags)
	return err
}

// PushNote is PushTo that also says UpToDatePush when there was nothing
// to send.
func (r *Repo) PushNote(remote, branch, dest string, setUpstream, force, tags bool) (string, error) {
	if dest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(dest), "refs/heads/")); dest == "" {
		dest = branch
	}
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	if force {
		args = append(args, "--force-with-lease")
	}
	if tags {
		args = append(args, "--tags")
	}
	var positional []string
	if remote != "" {
		positional = append(positional, remote)
		if branch != "" {
			positional = append(positional, "refs/heads/"+branch+":refs/heads/"+dest)
		}
	}
	// --porcelain puts a line on standard output for each reference, led by
	// "=" when the remote had it already, which git otherwise only says
	// on standard error. When the remote name starts with "-", "--" must
	// precede it and --porcelain moves in front of the separator to stay
	// an option; every other case keeps the arguments they have always had.
	if leadingDash(positional...) {
		args = append(append(args, "--porcelain", "--"), positional...)
	} else {
		args = append(args, positional...)
		args = append(args, "--porcelain")
	}
	out, err := r.run(slow, args...)
	if err == nil && allUpToDate(string(out)) {
		return UpToDatePush, nil
	}
	return "", err
}

// allUpToDate reads git push --porcelain: true when it listed references
// and every one was already on the remote.
func allUpToDate(out string) bool {
	seen := false
	for _, line := range strings.Split(out, "\n") {
		if line == "" || line == "Done" || strings.HasPrefix(line, "To ") {
			continue
		}
		if !strings.HasPrefix(line, "=\t") {
			return false
		}
		seen = true
	}
	return seen
}

// PushRefs pushes the named references to remote. A tag uses its tag ref;
// anything else is pushed as written.
func (r *Repo) PushRefs(remote string, refs []string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" || len(refs) == 0 {
		return fmt.Errorf("pushing references needs a remote and a reference")
	}
	positional := []string{remote}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if !strings.Contains(ref, "/") {
			ref = "refs/tags/" + ref
		}
		positional = append(positional, ref)
	}
	if len(positional) == 1 {
		return fmt.Errorf("pushing references needs a reference")
	}
	args := withDashdash([]string{"push"}, positional...)
	_, err := r.run(slow, args...)
	return err
}

// RemoteURL returns the fetch URL of a remote.
func (r *Repo) RemoteURL(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("a remote name is required")
	}
	out, err := r.run(quick, withDashdash([]string{"remote", "get-url"}, name)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoteAdd adds a remote. fetch downloads its branches when set.
func (r *Repo) RemoteAdd(name, url string, fetch bool) error {
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	if name == "" || url == "" {
		return fmt.Errorf("a remote needs a name and a URL")
	}
	if _, err := r.run(quick, withDashdash([]string{"remote", "add"}, name, url)...); err != nil {
		return err
	}
	if !fetch {
		return nil
	}
	_, err := r.run(slow, withDashdash([]string{"fetch"}, name)...)
	return err
}

// RemoteSetURL changes the fetch and push URL of a remote.
func (r *Repo) RemoteSetURL(name, url string) error {
	name, url = strings.TrimSpace(name), strings.TrimSpace(url)
	if name == "" || url == "" {
		return fmt.Errorf("a remote needs a name and a URL")
	}
	_, err := r.run(quick, withDashdash([]string{"remote", "set-url"}, name, url)...)
	return err
}

// RemoteRename renames a remote and its remote-tracking branches.
func (r *Repo) RemoteRename(old, name string) error {
	old, name = strings.TrimSpace(old), strings.TrimSpace(name)
	if old == "" || name == "" {
		return fmt.Errorf("renaming a remote needs both names")
	}
	_, err := r.run(quick, withDashdash([]string{"remote", "rename"}, old, name)...)
	return err
}

// RemoteRemove deletes a remote and its remote-tracking branches.
// Branches on the server stay.
func (r *Repo) RemoteRemove(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a remote name is required")
	}
	_, err := r.run(quick, withDashdash([]string{"remote", "remove"}, name)...)
	return err
}

// DeleteRemoteTag deletes one tag from a remote. The local tag stays.
func (r *Repo) DeleteRemoteTag(remote, name string) error {
	remote, name = strings.TrimSpace(remote), strings.TrimSpace(name)
	if remote == "" || name == "" {
		return fmt.Errorf("deleting a remote tag needs a remote and a tag")
	}
	_, err := r.run(slow, withDashdash([]string{"push", "--delete"}, remote, "refs/tags/"+name)...)
	return err
}

// DeleteRemoteBranch deletes a branch, such as origin/feature, from its
// remote.
func (r *Repo) DeleteRemoteBranch(remoteBranch string) error {
	remote, branch, ok := strings.Cut(remoteBranch, "/")
	if !ok {
		return fmt.Errorf("%s is not a remote branch", remoteBranch)
	}
	_, err := r.run(slow, withDashdash([]string{"push", "--delete"}, remote, branch)...)
	return err
}

// PushRef pushes one reference, such as a tag, to remote.
func (r *Repo) PushRef(remote, ref string) error {
	_, err := r.run(slow, withDashdash([]string{"push"}, remote, ref)...)
	return err
}

// Init creates a repository at the executor's directory.
func (r *Repo) Init(branch string) error {
	if branch == "" {
		branch = "main"
	}
	if err := validateInitBranch(branch); err != nil {
		return err
	}
	caps, err := r.Capabilities()
	if err != nil {
		return err
	}
	if caps.InitInitialBranch {
		_, err := r.run(quick, "init", "-b", branch)
		return err
	}
	return r.initCompat225(branch)
}

// PushSpec is a local branch and the remote branch it is pushed to.
type PushSpec struct {
	Local  string
	Remote string // the branch name on the remote, Local when empty
}

// PushBranches pushes several local branches to remote in one git push.
// setUpstream makes each track the branch it was pushed to.
func (r *Repo) PushBranches(remote string, specs []PushSpec, setUpstream, force bool) error {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return fmt.Errorf("pushing needs a remote")
	}
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	if force {
		args = append(args, "--force-with-lease")
	}
	positional := []string{remote}
	for _, s := range specs {
		local := strings.TrimSpace(s.Local)
		if local == "" {
			continue
		}
		dest := strings.TrimSpace(s.Remote)
		if dest == "" {
			dest = local
		}
		positional = append(positional, "refs/heads/"+local+":refs/heads/"+dest)
	}
	if len(positional) == 1 {
		return fmt.Errorf("choose a branch to push")
	}
	args = withDashdash(args, positional...)
	_, err := r.run(slow, args...)
	return err
}

// DeleteTagFromRemotes deletes a tag from each remote that has it. A
// remote without the tag is skipped.
func (r *Repo) DeleteTagFromRemotes(remotes []string, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("deleting a remote tag needs a tag")
	}
	var failed []string
	for _, remote := range remotes {
		remote = strings.TrimSpace(remote)
		if remote == "" {
			continue
		}
		if err := r.DeleteRemoteTag(remote, name); err != nil {
			if strings.Contains(err.Error(), "remote ref does not exist") {
				continue
			}
			failed = append(failed, remote+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "\n"))
	}
	return nil
}

// EditRemote sets the URL of the remote old and renames it to name.
func (r *Repo) EditRemote(old, name, url string) error {
	old, name, url = strings.TrimSpace(old), strings.TrimSpace(name), strings.TrimSpace(url)
	if old == "" || name == "" || url == "" {
		return fmt.Errorf("a remote needs a name and a URL")
	}
	if current, err := r.RemoteURL(old); err != nil || current != url {
		if err := r.RemoteSetURL(old, url); err != nil {
			return err
		}
	}
	if old == name {
		return nil
	}
	return r.RemoteRename(old, name)
}

// TestRemote checks that url answers as a git repository, listing its
// branches without fetching. In an SSH repository the server connects.
func (r *Repo) TestRemote(url string) (int, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return 0, fmt.Errorf("enter the repository URL")
	}
	out, err := r.run(quick, "ls-remote", "--heads", "--", url)
	if err != nil {
		return 0, err
	}
	return len(strings.Fields(string(out))) / 2, nil
}
