package git

import (
	"fmt"
	"sort"
	"strings"
)

// Stashes returns the stash, newest first.
func (r *Repo) Stashes() ([]Stash, error) {
	out, err := r.run(quick, "stash", "list", "--format="+stashFormat)
	if err != nil {
		return nil, err
	}
	return ParseStashes(out), nil
}

// StashOptions chooses which changes a stash keeps.
type StashOptions struct {
	Message string
	// Staged keeps only the index. Untracked files stay in the working tree.
	Staged bool
	// Untracked includes files git does not track. Ignored files stay out.
	Untracked bool
	// StageNew stages untracked files before stashing, so the stash keeps
	// them as added files. Already tracked changes are stashed too.
	StageNew bool
}

// StashPush stashes the working tree. By default it also takes untracked files.
func (r *Repo) StashPush(message string) error {
	stashed, err := r.StashSave(message)
	if err != nil {
		return err
	}
	if !stashed {
		return fmt.Errorf("no local changes to save")
	}
	return nil
}

// StashSave stashes tracked and untracked changes. stashed is false when
// the working tree had nothing to save; that is not an error.
func (r *Repo) StashSave(message string) (bool, error) {
	args := []string{"stash", "push", "--include-untracked"}
	if message = strings.TrimSpace(message); message != "" {
		args = append(args, "-m", message)
	}
	out, err := r.run(quick, args...)
	if err != nil {
		return false, err
	}
	if strings.Contains(string(out), "No local changes to save") {
		return false, nil
	}
	return true, nil
}

// StashPushWith stashes the changes described by o. Staged and untracked
// cannot both be set: a staged stash is the index alone.
// StashPaths stashes the named paths and leaves every other change
// in the working tree. Untracked files among paths are included.
func (r *Repo) StashPaths(message string, paths []string) error {
	var chosen []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" {
			chosen = append(chosen, path)
		}
	}
	if len(chosen) == 0 {
		return fmt.Errorf("a partial stash needs a file")
	}
	args := []string{"stash", "push", "--include-untracked"}
	if message = strings.TrimSpace(message); message != "" {
		args = append(args, "-m", message)
	}
	args = append(args, "--")
	args = append(args, chosen...)
	_, err := r.run(quick, args...)
	return err
}

func (r *Repo) StashPushWith(o StashOptions) error {
	if o.Staged && (o.Untracked || o.StageNew) {
		return fmt.Errorf("a staged stash cannot also take untracked files")
	}
	if o.Staged {
		if err := r.requireStashStaged(); err != nil {
			return err
		}
	}
	var staged []string
	if o.StageNew {
		var err error
		staged, err = r.stageNewFiles()
		if err != nil {
			return err
		}
	}
	args := []string{"stash", "push"}
	switch {
	case o.Staged:
		args = append(args, "--staged")
	case o.Untracked || o.StageNew:
		args = append(args, "--include-untracked")
	}
	if message := strings.TrimSpace(o.Message); message != "" {
		args = append(args, "-m", message)
	}
	if _, err := r.run(quick, args...); err != nil {
		if len(staged) > 0 {
			_ = r.Unstage(staged...)
		}
		return err
	}
	return nil
}

// stageNewFiles adds every untracked file and returns the paths it added.
// Ignored files stay out. A failure leaves the index unchanged.
func (r *Repo) stageNewFiles() ([]string, error) {
	st, err := r.Status()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range st.Unstaged {
		if f.Status == '?' {
			paths = append(paths, f.Path)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if err := r.Stage(paths...); err != nil {
		return nil, err
	}
	return paths, nil
}

// StashApply applies a stash entry, and drops it when pop is set.
func (r *Repo) StashApply(name string, pop bool) error {
	return r.StashApplyWith(name, StashApplyOptions{Pop: pop, Index: true})
}

// StashSelection identifies one reflog entry. Occurrence counts entries
// with the same commit and message from the newest entry; it is not Stash.Index.
type StashSelection struct {
	Hash       string
	Message    string
	Occurrence int
}

// StashApplyEntry applies one selected reflog entry.
func (r *Repo) StashApplyEntry(selected StashSelection, pop bool) error {
	return r.StashApplyEntryWith(selected, StashApplyOptions{Pop: pop, Index: true})
}

// StashApplyEntryWith applies one selected reflog entry as o asks.
func (r *Repo) StashApplyEntryWith(selected StashSelection, o StashApplyOptions) error {
	current, err := r.stashRef(selected)
	if err != nil {
		return err
	}
	return r.StashApplyWith(current.Name, o)
}

// StashApplyOptions are the choices of the Apply Stash dialog.
type StashApplyOptions struct {
	// Pop deletes the stash after applying it. Git keeps it when the
	// changes conflict.
	Pop bool
	// Index restores what was staged as staged. When the index cannot be
	// restored, the changes are applied unstaged instead.
	Index bool
}

// StashApplyWith applies a stash entry as o asks.
func (r *Repo) StashApplyWith(name string, o StashApplyOptions) error {
	verb := "apply"
	if o.Pop {
		verb = "pop"
	}
	if o.Index {
		_, err := r.run(quick, "stash", verb, "--index", name)
		if err == nil || !strings.Contains(err.Error(), "Try without --index") {
			return err
		}
	}
	_, err := r.run(quick, "stash", verb, name)
	return err
}

// StashDrop deletes a stash entry.
func (r *Repo) StashDrop(name string) error {
	_, err := r.run(quick, "stash", "drop", name)
	return err
}

// StashDropEntry deletes one selected reflog entry at its current position.
func (r *Repo) StashDropEntry(selected StashSelection) error {
	return r.StashDropEntries([]StashSelection{selected})
}

// StashDropEntries resolves every selection before deleting any entry. It
// drops the current reflog positions from the oldest, largest index to the newest, so earlier deletions do not move later names.
func (r *Repo) StashDropEntries(selected []StashSelection) error {
	stashes, err := r.Stashes()
	if err != nil {
		return err
	}
	resolved, err := resolveStashSelections(stashes, selected)
	if err != nil {
		return err
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Index > resolved[j].Index })
	for _, stash := range resolved {
		if err := r.StashDrop(stash.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) stashRef(selected StashSelection) (Stash, error) {
	stashes, err := r.Stashes()
	if err != nil {
		return Stash{}, err
	}
	return resolveStashSelection(stashes, selected)
}

func resolveStashSelections(stashes []Stash, selected []StashSelection) ([]Stash, error) {
	var resolved []Stash
	seen := map[string]bool{}
	for _, selection := range selected {
		stash, err := resolveStashSelection(stashes, selection)
		if err != nil {
			return nil, err
		}
		if seen[stash.Name] {
			continue
		}
		seen[stash.Name] = true
		resolved = append(resolved, stash)
	}
	return resolved, nil
}

func resolveStashSelection(stashes []Stash, selected StashSelection) (Stash, error) {
	hash := strings.TrimSpace(selected.Hash)
	message := strings.TrimSpace(selected.Message)
	if hash == "" || message == "" || selected.Occurrence < 0 {
		return Stash{}, fmt.Errorf("a stash needs its commit, message, and occurrence")
	}
	seen := 0
	for _, stash := range stashes {
		if stash.Hash != hash || strings.TrimSpace(stash.Message) != message {
			continue
		}
		if seen == selected.Occurrence {
			return stash, nil
		}
		seen++
	}
	return Stash{}, fmt.Errorf("%s %q is no longer in the stash", hash, message)
}

// StashRename changes the message of the named reflog entry. The
// replacement commit is stored before the original entry is removed.
func (r *Repo) StashRename(name, message string) error {
	name = strings.TrimSpace(name)
	message = strings.TrimSpace(message)
	if name == "" || message == "" {
		return fmt.Errorf("a stash rename needs the stash and a message")
	}
	stashes, err := r.Stashes()
	if err != nil {
		return err
	}
	for _, stash := range stashes {
		if stash.Name == name {
			selected := StashSelection{Hash: stash.Hash, Message: stash.Message, Occurrence: stashSelectionOccurrence(stashes, stash)}
			return r.rewriteStash(stash, message, &selected)
		}
	}
	return fmt.Errorf("%s is not a stash", name)
}

// StashRenameEntry changes the message of one selected reflog entry. It
// rejects the rename when another entry with the same commit and message has
// been added or removed since the selection was made.
func (r *Repo) StashRenameEntry(selected StashSelection, message string, groupCount int) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("a stash rename needs a message")
	}
	stashes, err := r.Stashes()
	if err != nil {
		return err
	}
	if stashSelectionCount(stashes, selected) != groupCount {
		return fmt.Errorf("%s %q changed while the stash operation was waiting", strings.TrimSpace(selected.Hash), strings.TrimSpace(selected.Message))
	}
	current, err := resolveStashSelection(stashes, selected)
	if err != nil {
		return err
	}
	return r.rewriteStash(current, message, &selected)
}

// rewriteStash creates the replacement with the current configured identity,
// stores it first, and then removes the originally selected reflog entry.
// A failed store leaves the original list unchanged. A failed removal leaves
// both commits reachable; it does not choose another entry or edit reflog
// files directly, and it does not exclude another Git writer.
func (r *Repo) rewriteStash(current Stash, message string, selected *StashSelection) error {
	replacement, err := r.rewriteStashCommit(current.Hash, message)
	if err != nil {
		return err
	}
	if _, err := r.run(quick, "stash", "store", "-m", message, replacement); err != nil {
		return err
	}
	stashes, err := r.Stashes()
	if err != nil {
		return fmt.Errorf("renamed stash %s was stored, but the original entry could not be resolved: %w", shortStashHash(replacement), err)
	}
	original, err := resolveRewrittenStash(stashes, current, selected)
	if err != nil {
		return fmt.Errorf("renamed stash %s was stored, but the original entry was not removed: %w", shortStashHash(replacement), err)
	}
	if err := r.StashDrop(original.Name); err != nil {
		return fmt.Errorf("renamed stash %s was stored, but the original entry was not removed: %w", shortStashHash(replacement), err)
	}
	return nil
}

func (r *Repo) rewriteStashCommit(hash, message string) (string, error) {
	info, err := r.run(quick, "rev-list", "-1", "--format=%T%n%P", hash)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(info), "\n"), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "commit ") || !isHex(lines[1]) {
		return "", fmt.Errorf("%s is not a stash commit", shortStashHash(hash))
	}
	args := []string{"commit-tree", lines[1]}
	for _, parent := range strings.Fields(lines[2]) {
		if !isHex(parent) {
			return "", fmt.Errorf("%s has an unreadable stash parent", shortStashHash(hash))
		}
		args = append(args, "-p", parent)
	}
	if len(args) < 4 {
		return "", fmt.Errorf("%s is not a stash commit", shortStashHash(hash))
	}
	args = append(args, "-m", message)
	out, err := r.run(quick, args...)
	if err != nil {
		return "", err
	}
	replacement := strings.TrimSpace(string(out))
	if !isHex(replacement) {
		return "", fmt.Errorf("git did not return the renamed stash commit")
	}
	return replacement, nil
}

func resolveRewrittenStash(stashes []Stash, current Stash, selected *StashSelection) (Stash, error) {
	if selected != nil {
		resolved, err := resolveStashSelection(stashes, *selected)
		if err != nil {
			return Stash{}, err
		}
		if resolved.Hash != current.Hash {
			return Stash{}, fmt.Errorf("%s %q changed while the stash was being renamed", strings.TrimSpace(selected.Hash), strings.TrimSpace(selected.Message))
		}
		return resolved, nil
	}
	return Stash{}, fmt.Errorf("%s %q is no longer in the stash", shortStashHash(current.Hash), current.Message)
}

func stashSelectionOccurrence(stashes []Stash, selected Stash) int {
	seen := 0
	for _, stash := range stashes {
		if stash.Hash == selected.Hash && strings.TrimSpace(stash.Message) == strings.TrimSpace(selected.Message) {
			if stash.Name == selected.Name {
				return seen
			}
			seen++
		}
	}
	return -1
}

func shortStashHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func stashSelectionCount(stashes []Stash, selected StashSelection) int {
	count := 0
	hash, message := strings.TrimSpace(selected.Hash), strings.TrimSpace(selected.Message)
	for _, stash := range stashes {
		if stash.Hash == hash && strings.TrimSpace(stash.Message) == message {
			count++
		}
	}
	return count
}
