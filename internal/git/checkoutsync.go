package git

import (
	"fmt"
	"strings"
)

// SyncAction is what Checkout and... does with a branch once it is
// checked out.
type SyncAction string

const (
	SyncCheckout    SyncAction = "checkout"
	SyncFastForward SyncAction = "fast-forward"
	SyncMerge       SyncAction = "merge"
	SyncRebase      SyncAction = "rebase"
	SyncReset       SyncAction = "reset"
)

// FastForward moves the current branch to rev when that is a fast-forward,
// and fails when the branches have diverged.
func (r *Repo) FastForward(rev string) error {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return fmt.Errorf("fast-forward needs a revision")
	}
	_, err := r.run(quick, withDashdash([]string{"merge", "--ff-only"}, rev)...)
	return err
}

// CheckoutAndSync switches to branch and brings it level with upstream, such
// as origin/branch: by fast-forward, merge, rebase or hard reset. stash puts
// local changes aside first and back after, unless the operation stopped on
// a conflict, which keeps the stash until Continue or Abort. force discards local changes in the
// checkout.
func (r *Repo) CheckoutAndSync(branch, upstream string, action SyncAction, stash, force bool) error {
	branch, upstream = strings.TrimSpace(branch), strings.TrimSpace(upstream)
	if branch == "" {
		return fmt.Errorf("a branch is needed")
	}
	if action != SyncCheckout && upstream == "" {
		return fmt.Errorf("%s needs the branch it follows", action)
	}
	if stash {
		if err := r.saveSyncStash("Branchline checkout"); err != nil {
			return err
		}
	}
	err := r.checkout(branch, force)
	if err == nil {
		switch action {
		case SyncFastForward:
			err = r.FastForward(upstream)
		case SyncMerge:
			err = r.MergeWith(upstream, MergeFastForward)
		case SyncRebase:
			err = r.Rebase(upstream)
		case SyncReset:
			err = r.Reset(upstream, "hard")
		}
	}
	return r.restoreSyncStash(err)
}
