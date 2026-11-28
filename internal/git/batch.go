package git

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// RemoveBranches deletes several local branches: the worktrees using
// them first, then the branches in one git branch -D. Every step is
// tried; the errors are joined.
func (r *Repo) RemoveBranches(list []RemoveBranchOptions) error {
	var names []string
	var errs []error
	for _, o := range list {
		name := strings.TrimSpace(o.Name)
		if name == "" {
			continue
		}
		if o.Worktree != "" {
			if err := r.WorktreeRemove(o.Worktree, false); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		names = append(names, name)
	}
	if len(names) == 0 && len(errs) == 0 {
		return fmt.Errorf("deleting branches needs their names")
	}
	if len(names) > 0 {
		if err := r.runPathBatches([]string{"branch", "-D", "--"}, names); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deleteOnRemote deletes refs from one remote in a single push, every
// reference pinned by a force-with-lease on the object id the caller
// verified the remote reference to hold: a reference someone moved or
// recreated since the check fails its lease and keeps its new value, and
// the porcelain answer names every reference the push rejected. Nothing
// else stops the batch: the references whose leases hold delete in the
// same push, and the error lists the ones that did not. A push rejected
// only for references the remote lost in the meantime retries them one by
// one, where a missing reference is not an error — it is already gone.
func (r *Repo) deleteOnRemote(remote string, refs []string, leases map[string]string) error {
	if len(refs) == 0 {
		return nil
	}
	args := []string{"push", "--porcelain", "--delete"}
	for _, ref := range refs {
		args = append(args, "--force-with-lease="+ref+":"+leases[ref])
	}
	args = append(args, "--", remote)
	args = append(args, refs...)
	out, err := r.run(slow, args...)
	if err == nil {
		return nil
	}
	if rejected := rejectedTagDeletes(string(out), refs); len(rejected) > 0 {
		return fmt.Errorf("failed to delete %s on %s (the server tags changed or were already deleted; Fetch first and try again)",
			joinTagNames(rejected), remote)
	}
	if !onlyMissingRefs(err) {
		return err
	}
	if len(refs) == 1 {
		return nil
	}
	var errs []error
	for _, ref := range refs {
		one := []string{"push", "--porcelain", "--force-with-lease=" + ref + ":" + leases[ref], "--delete", "--", remote, ref}
		if _, err := r.run(slow, one...); err != nil && !onlyMissingRefs(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// rejectedTagDeletes reads the porcelain answer of a failed batch delete
// push for the references git rejected, returning the matching refs in the
// order refs lists them. A rejection line of a delete names the reference
// as (delete):<ref>: anything else — a transport failure, a missing ref —
// has no such line and the caller falls back to the push's own error.
func rejectedTagDeletes(out string, refs []string) []string {
	rejected := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "!\t(delete):") {
			continue
		}
		rest := strings.TrimPrefix(line, "!\t(delete):")
		ref, _, _ := strings.Cut(rest, "\t")
		if strings.HasPrefix(ref, "refs/tags/") {
			rejected[ref] = true
		}
	}
	var list []string
	for _, ref := range refs {
		if rejected[ref] {
			list = append(list, ref)
		}
	}
	return list
}

// joinTagNames formats tag references for an error message: the bare tag
// names, comma-separated.
func joinTagNames(refs []string) string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, strings.TrimPrefix(ref, "refs/tags/"))
	}
	return strings.Join(names, ", ")
}

// onlyMissingRefs reports whether every ref a push rejected was missing
// on the remote.
func onlyMissingRefs(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "remote ref does not exist") {
		return false
	}
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "error:") && !strings.Contains(line, "remote ref does not exist") && !strings.Contains(line, "failed to push some refs") {
			return false
		}
	}
	return true
}

// DeleteTags deletes several tags here and, for each of remotes, from
// that remote. Before anything is deleted, every remote answers one
// ls-remote for the object ids its copies of the tags point at: a remote
// copy pointing anywhere else than the local tag's own object — a tag
// someone moved, or an annotated tag recreated onto even the same commit —
// refuses the whole batch without deleting a single tag, here or on any
// server, and so does a remote that cannot be read at all. A tag the
// remote never had is skipped, exactly as before.
//
// Only after every remote checks out do the local tags go, in one git
// tag -d, and then each remote receives one push deleting its verified
// copies, every reference pinned by a force-with-lease on the object id
// the check read. A copy moved between the check and the push fails its
// lease: it keeps its new value, the error names it, and the tags already
// deleted locally stay deleted. Without remotes the deletion is the one
// git tag -d it has always been.
func (r *Repo) DeleteTags(names, remotes []string) error {
	var tags, refs []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			tags = append(tags, name)
			refs = append(refs, "refs/tags/"+name)
		}
	}
	if len(tags) == 0 {
		return fmt.Errorf("deleting tags needs their names")
	}
	var servers []string
	for _, remote := range remotes {
		if remote = strings.TrimSpace(remote); remote != "" {
			servers = append(servers, remote)
		}
	}

	// The verification compares the exact objects the references hold, one
	// level deep: an annotated tag is its tag object, a lightweight one the
	// object it names. That is the value ls-remote reports for the exact
	// reference, and the only value a force-with-lease can pin.
	leases := map[string]map[string]string{}
	if len(servers) > 0 {
		var problems []string
		local := map[string]string{}
		objects, err := r.TagObjectIDs(tags)
		if err != nil {
			problems = append(problems, fmt.Sprintf("cannot delete tags: reading the local tags failed; nothing was deleted: %s", err))
		} else {
			for _, name := range tags {
				object, ok := objects[name]
				if !ok || !validOID(object.Object) {
					problems = append(problems, fmt.Sprintf("cannot delete tags: %s does not exist locally; nothing was deleted", name))
					continue
				}
				local[name] = object.Object
			}
		}
		if len(problems) == 0 {
			for _, remote := range servers {
				oids, err := r.RemoteTagOIDs(remote, tags)
				if err != nil {
					problems = append(problems, fmt.Sprintf("cannot delete tags: cannot read the tags on %s; nothing was deleted, here or on any remote: %s", remote, err))
					continue
				}
				leases[remote] = map[string]string{}
				for _, name := range tags {
					oid, ok := oids[name]
					if !ok {
						continue // the remote never had this tag: it is skipped.
					}
					if oid != local[name] {
						problems = append(problems, fmt.Sprintf("cannot delete tags: %s on %s does not match the local tag; nothing was deleted, here or on any remote", name, remote))
						continue
					}
					leases[remote]["refs/tags/"+name] = oid
				}
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "\n"))
		}
	}

	if _, err := r.run(quick, withDashdash([]string{"tag", "-d"}, tags...)...); err != nil {
		return err
	}
	var failed []string
	for _, remote := range servers {
		var remoteRefs []string
		for _, ref := range refs {
			if leases[remote][ref] != "" {
				remoteRefs = append(remoteRefs, ref)
			}
		}
		if err := r.deleteOnRemote(remote, remoteRefs, leases[remote]); err != nil {
			failed = append(failed, remote+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "\n"))
	}
	return nil
}

// StashDropMany deletes several stash entries, such as stash@{2}. They
// go from the newest index down, so that the names of those left to
// drop stay right.
func (r *Repo) StashDropMany(names []string) error {
	type entry struct {
		name  string
		index int
	}
	var list []entry
	for _, name := range names {
		name = strings.TrimSpace(name)
		var n int
		if _, err := fmt.Sscanf(name, "stash@{%d}", &n); err != nil {
			return fmt.Errorf("%q is not a stash", name)
		}
		list = append(list, entry{name, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].index > list[j].index })
	for i, e := range list {
		if i > 0 && list[i-1].index == e.index {
			continue
		}
		if err := r.StashDrop(e.name); err != nil {
			return err
		}
	}
	return nil
}

// CherryPickMany applies commits onto the current branch in the order
// given, oldest first, in one git cherry-pick. A conflict stops it, as
// one commit's does, and leaves the rest to continue.
func (r *Repo) CherryPickMany(hashes []string, o ReplayOptions) error {
	return r.replayMany("cherry-pick", hashes, o)
}

// RevertMany commits the reverse of each commit, in the order given:
// newest first, so that each revert applies cleanly.
func (r *Repo) RevertMany(hashes []string, o ReplayOptions) error {
	return r.replayMany("revert", hashes, o)
}

func (r *Repo) replayMany(command string, hashes []string, o ReplayOptions) error {
	if len(hashes) == 0 {
		return fmt.Errorf("%s needs a commit", command)
	}
	if o.Mainline > 1 && len(hashes) > 1 {
		// git takes -m for every commit given: parent 1 of a commit that is
		// not a merge is its only one, a higher parent fails part way.
		for _, h := range hashes {
			n, err := r.parentCount(h)
			if err != nil {
				return err
			}
			if n < o.Mainline {
				return fmt.Errorf("%s has no parent %d: only parent 1 can be skipped when the commits are not all merges", short(h), o.Mainline)
			}
		}
	}
	args, err := replayArgs(command, hashes[0], o)
	if err != nil {
		return err
	}
	for _, h := range hashes[1:] {
		if h = strings.TrimSpace(h); !isHex(h) {
			return fmt.Errorf("%q is not a commit", h)
		}
		args = append(args, h)
	}
	_, err = r.run(quick, args...)
	return err
}

// parentCount is how many parents the commit h has.
func (r *Repo) parentCount(h string) (int, error) {
	out, err := r.run(quick, withEndOfOptions([]string{"rev-list", "--parents", "-n", "1"}, strings.TrimSpace(h))...)
	if err != nil {
		return 0, err
	}
	return len(strings.Fields(string(out))) - 1, nil
}

// SquashCommits folds commits that follow each other on the current
// branch into the oldest of them. With a message, the commit made gets
// it; without, it keeps the oldest's (a fixup). Later commits are
// replayed on top. The message is handed to git as a blob, so no editor
// opens, here or on a server.
func (r *Repo) SquashCommits(hashes []string, message string) error {
	if len(hashes) < 2 {
		return fmt.Errorf("squashing needs two commits or more")
	}
	chosen := map[string]bool{}
	for _, h := range hashes {
		full, err := r.run(quick, "rev-parse", "--verify", "--quiet", strings.TrimSpace(h)+"^{commit}")
		if err != nil {
			return fmt.Errorf("%s is not a commit", h)
		}
		chosen[strings.TrimSpace(string(full))] = true
	}
	// The oldest is found on the branch's first-parent line, where the
	// chosen commits must follow each other.
	out, err := r.run(quick, "rev-list", "--first-parent", "--reverse", "HEAD")
	if err != nil {
		return err
	}
	line := strings.Fields(string(out))
	at, _, ok := chosenRun(line, chosen)
	if !ok {
		return errNotInRow
	}
	onto := "--root"
	if at > 0 {
		onto = line[at] + "~"
	}
	// git rebase -i replays merges as their changes, flattening the
	// history: a merge chosen or replayed above is refused.
	merges, err := r.run(quick, "rev-list", "--merges", "-n", "1", strings.TrimPrefix(onto+"..HEAD", "--root.."))
	if err != nil {
		return err
	}
	if m := strings.TrimSpace(string(merges)); m != "" {
		return fmt.Errorf("%w (%s)", ErrSquashMerge, short(m))
	}
	plan, err := r.RebasePlan(onto)
	if err != nil {
		return err
	}
	order := make([]string, len(plan))
	for i, step := range plan {
		order[i] = step.Hash
	}
	first, last, ok := chosenRun(order, chosen)
	if !ok {
		return errNotInRow
	}
	if onto == "--root" {
		onto = ""
	}
	var blob string
	if message = strings.TrimSpace(message); message != "" {
		out, err := r.runInput(quick, []byte(message+"\n"), "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		blob = strings.TrimSpace(string(out))
	}
	var todo []string
	for i, step := range plan {
		action := "pick"
		if i > first && i <= last {
			action = "fixup"
		}
		todo = append(todo, action+" "+step.Hash+" "+oneLine(step.Subject))
		if i == last && blob != "" {
			todo = append(todo, "exec git cat-file blob "+blob+" | git commit --amend --allow-empty --no-verify -q -F -")
		}
	}
	return r.rebaseSequence(onto, strings.Join(todo, "\n")+"\n")
}

// ErrSquashMerge is returned when a merge commit is among the commits to
// squash or those replayed after them.
var ErrSquashMerge = errors.New("merge commits can't be squashed or replayed: choose commits after the last merge")

var errNotInRow = errors.New("the commits to squash must follow each other on the current branch")

// chosenRun returns where the chosen commits start and end in list, and
// whether they are all there, one after the other.
func chosenRun(list []string, chosen map[string]bool) (first, last int, ok bool) {
	first, last = -1, -1
	for i, h := range list {
		if chosen[h] {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last, first >= 0 && last-first+1 == len(chosen)
}
