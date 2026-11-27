package git

import (
	"errors"
	"fmt"
	"strings"
)

// refs/worktree is private to each checkout, including linked worktrees.
// A ref keeps the exact saved object reachable across application restarts,
// even if the user changes or clears the stash list while resolving a conflict.
const syncStashRef = "refs/worktree/branchline-sync-stash"

func (r *Repo) syncStash() (string, error) {
	out, err := r.run(quick, "rev-parse", "--verify", "--quiet", syncStashRef)
	if ExitCode(err) == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

func (r *Repo) saveSyncStash(message string) error {
	if err := r.restoreSyncStash(nil); err != nil {
		return err
	}
	if hash, err := r.syncStash(); err != nil {
		return err
	} else if hash != "" {
		return fmt.Errorf("finish or abort the previous operation before saving more changes (stash %s)", hash)
	}
	st, err := r.Status()
	if err != nil {
		return err
	}
	if st.State != "" || st.Conflicted() {
		return fmt.Errorf("finish or abort the current operation before saving changes")
	}
	saved, err := r.StashSave(message)
	if err != nil || !saved {
		return err
	}
	out, err := r.run(quick, "rev-parse", "--verify", "refs/stash")
	if err != nil {
		return fmt.Errorf("changes remain in the stash list: %w", err)
	}
	hash := strings.TrimSpace(string(out))
	if _, err := r.run(quick, "update-ref", syncStashRef, hash, ""); err != nil {
		return fmt.Errorf("changes remain in stash %s; could not record automatic restoration: %w", hash, err)
	}
	return nil
}

// restoreSyncStash runs after the original operation and after Continue,
// Abort or Skip. A paused operation retains the marker. Remove it before
// applying so a failed application or a later Continue cannot apply twice.
// On any failure the saved object stays in the user's stash list.
func (r *Repo) restoreSyncStash(opErr error) error {
	hash, err := r.syncStash()
	if err != nil {
		return errors.Join(opErr, err)
	}
	if hash == "" {
		return opErr
	}
	st, err := r.Status()
	if err != nil {
		return errors.Join(opErr, err)
	}
	if st.State != "" {
		return opErr
	}
	if st.Conflicted() {
		return errors.Join(opErr, fmt.Errorf("resolve the current conflicts before restoring saved changes %s", hash))
	}
	// Ensure a visible recovery copy exists if the user cleared the stash list.
	entries, err := r.Stashes()
	if err != nil {
		return errors.Join(opErr, err)
	}
	found := false
	for _, entry := range entries {
		found = found || entry.Hash == hash
	}
	if !found {
		if _, err := r.run(quick, "stash", "store", "-m", "Branchline saved sync changes", hash); err != nil {
			return errors.Join(opErr, err)
		}
	}
	if _, err := r.run(quick, "update-ref", "-d", syncStashRef, hash); err != nil {
		return errors.Join(opErr, err)
	}
	if err := r.StashApplyWith(hash, StashApplyOptions{Index: true}); err != nil {
		return errors.Join(opErr, fmt.Errorf("saved changes %s could not be fully restored; they remain in the stash list: %w", hash, err))
	}
	// A hash is accepted by apply but not by drop. Resolve its current selector
	// after applying, rather than assuming it is still stash@{0}.
	entries, err = r.Stashes()
	if err != nil {
		return errors.Join(opErr, err)
	}
	for _, entry := range entries {
		if entry.Hash == hash {
			if err := r.StashDrop(entry.Name); err != nil {
				return errors.Join(opErr, fmt.Errorf("changes restored, but stash %s could not be removed: %w", hash, err))
			}
			break
		}
	}
	return opErr
}
