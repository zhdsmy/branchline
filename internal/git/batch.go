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

// DeleteTags deletes several tags here and, for each of remotes, from
// that remote: every tag that remote still has goes out with the exact
// lease ls-remote just answered, so a tag someone else moved on the
// server keeps its new value. A remote without some of the tags skips
// them.
func (r *Repo) DeleteTags(names, remotes []string) error {
	var tags []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			tags = append(tags, name)
		}
	}
	if len(tags) == 0 {
		return fmt.Errorf("deleting tags needs their names")
	}
	if _, err := r.run(quick, withDashdash([]string{"tag", "-d"}, tags...)...); err != nil {
		return err
	}
	var failed []string
	for _, remote := range remotes {
		if remote = strings.TrimSpace(remote); remote == "" {
			continue
		}
		for _, name := range tags {
			oid, exists, err := r.RemoteTagOID(remote, name)
			if err != nil {
				failed = append(failed, remote+": "+err.Error())
				continue
			}
			if !exists {
				continue
			}
			if err := r.SafeDeleteRemoteTag(remote, name, oid, nil); err != nil {
				failed = append(failed, remote+": "+err.Error())
			}
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
