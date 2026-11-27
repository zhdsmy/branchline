package git

import (
	"fmt"
	"strings"
)

// StashSnapshot saves the local changes as a new stash via git stash create
// and git stash store without modifying the working directory. When
// o.StageNew is set, untracked files are staged into the index first; if
// creating or storing the snapshot fails, those newly staged paths are
// unstaged while pre-existing staged changes are preserved.
func (r *Repo) StashSnapshot(o StashOptions) error {
	if o.Staged {
		return fmt.Errorf("a snapshot takes the working directory, not only the index")
	}
	if o.Untracked && !o.StageNew {
		return fmt.Errorf("a snapshot includes untracked files only by staging them with StageNew")
	}
	var staged []string
	if o.StageNew {
		var err error
		staged, err = r.stageNewFiles()
		if err != nil {
			return err
		}
	}
	rollback := func(cause error) error {
		if len(staged) == 0 {
			return cause
		}
		if err := r.Unstage(staged...); err != nil {
			return fmt.Errorf("%w (rollback unstage failed: %w)", cause, err)
		}
		return cause
	}
	createArgs := []string{"stash", "create"}
	message := strings.TrimSpace(o.Message)
	if message != "" {
		createArgs = append(createArgs, message)
	}
	out, err := r.run(quick, createArgs...)
	if err != nil {
		return rollback(err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return rollback(fmt.Errorf("no local changes to save"))
	}
	storeArgs := []string{"stash", "store"}
	if message != "" {
		storeArgs = append(storeArgs, "--message", message)
	}
	storeArgs = append(storeArgs, commit)
	if _, err := r.run(quick, storeArgs...); err != nil {
		return rollback(err)
	}
	return nil
}
