package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// splitBranchUpstream splits a short upstream name such as "origin/feature/x"
// into its remote and remote branch name against the repository's configured
// remotes, preferring the longest matching remote prefix so names containing
// slashes resolve unambiguously.
func splitBranchUpstream(upstream string, remotes []string) (remote, remoteBranch string, ok bool) {
	if upstream == "" {
		return "", "", false
	}
	best := ""
	for _, r := range remotes {
		if r == "" || len(r) <= len(best) {
			continue
		}
		if strings.HasPrefix(upstream, r+"/") && len(upstream) > len(r)+1 {
			best = r
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, upstream[len(best)+1:], true
}

// branchSyncMenuItems adds Pull, Checkout and… and Push to entries to a
// local branch's context menu in the sidebar:
//   - Pull '<upstream>' appears only on the current branch when it has a live
//     upstream and disables while a write or Git operation is in progress;
//   - Checkout and… appears on every non-current branch with a live upstream:
//     a submenu of Fast-Forward…, Merge…, Rebase… and Reset…, each opening the
//     dialog that switches to the branch and brings it level with its
//     upstream, disabled while a write or Git operation is in progress;
//   - Push to appears whenever the repository has at least one remote: a single
//     item naming the upstream when tracked, a single item naming the lone
//     remote when untracked with one remote, or a Push to submenu listing each
//     remote when untracked with several remotes.
func (a *app) branchSyncMenuItems(m *ui.Menu, br git.Branch) {
	snap := a.session.Snapshot()
	busy := a.session.Busy()

	remote, remoteBranch, hasUpstream := "", "", false
	if br.Upstream != "" && !br.Gone {
		remote, remoteBranch, hasUpstream = splitBranchUpstream(br.Upstream, snap.Refs.Remotes)
	}

	showPull := br.Current && hasUpstream
	showCheckoutAnd := !br.Current && hasUpstream
	showPush := len(snap.Refs.Remotes) > 0
	if !showPull && !showCheckoutAnd && !showPush {
		return
	}

	m.Separator()
	if showPull {
		pullDisabled := busy || snap.Status.State != ""
		if m.Item("Pull '" + br.Upstream + "'").Disabled(pullDisabled).Chosen() {
			a.branchSyncPull(br.Name, br.Hash, br.Upstream)
		}
	}
	if showCheckoutAnd {
		checkoutDisabled := busy || snap.Status.State != ""
		m.Submenu("Checkout and…", func(sub *ui.Menu) {
			for _, ch := range checkoutSyncChoices {
				action := ch.action
				if sub.Item(ch.title + "…").Disabled(checkoutDisabled).Chosen() {
					a.openCheckoutSync(br, action)
				}
			}
		})
	}
	if !showPush {
		return
	}
	switch {
	case hasUpstream:
		if m.Item("Push to '" + br.Upstream + "'…").Disabled(busy).Chosen() {
			a.branchSyncPush(br.Name, br.Hash, remote, remoteBranch, false, br.Upstream)
		}
	case len(snap.Refs.Remotes) == 1:
		targetRemote := snap.Refs.Remotes[0]
		if m.Item("Push to '" + targetRemote + "'…").Disabled(busy).Chosen() {
			a.branchSyncPush(br.Name, br.Hash, targetRemote, br.Name, true, "")
		}
	default:
		m.Submenu("Push to", func(sub *ui.Menu) {
			for _, r := range snap.Refs.Remotes {
				targetRemote := r
				if sub.Item(targetRemote + "…").Disabled(busy).Chosen() {
					a.branchSyncPush(br.Name, br.Hash, targetRemote, br.Name, true, "")
				}
			}
		})
	}
}

// branchSyncPull runs a guarded pull of the checked-out branch's upstream
// through the shared sync worker and askpass channel.
func (a *app) branchSyncPull(expectedBranch, expectedHead, expectedUpstream string) {
	if a.session.Busy() {
		return
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	a.session.ClearActionErr()
	st.syncWorkerStarted()
	if !a.session.doThen("Pulling", func(*git.Repo) error {
		defer st.syncWorkerDone()
		note, err := branchPullWithGuard(repo, expectedBranch, expectedHead, expectedUpstream)
		return noted(note, err)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
		if note := syncNote(err); note != "" {
			a.session.ClearActionErr()
			a.toast = note
		}
	}) {
		st.syncWorkerDone()
	}
}

// branchSyncPush runs a guarded push of branch (current or non-current) to
// remote/destBranch through the shared sync worker and askpass channel.
func (a *app) branchSyncPush(branch, expectedHash, remote, destBranch string, setUpstream bool, expectedUpstream string) {
	if a.session.Busy() {
		return
	}
	st := syncStateFor(a)
	repo, gen := a.syncRepo()
	if repo == nil {
		return
	}
	st.syncErr = ""
	a.session.ClearActionErr()
	st.syncWorkerStarted()
	if !a.session.doThen("Pushing", func(*git.Repo) error {
		defer st.syncWorkerDone()
		note, err := branchPushWithGuard(repo, branch, expectedHash, remote, destBranch, setUpstream, expectedUpstream)
		return noted(note, err)
	}, func(err error) {
		if dropIfStale(a, gen) {
			return
		}
		if note := syncNote(err); note != "" {
			a.session.ClearActionErr()
			a.toast = note
		}
	}) {
		st.syncWorkerDone()
	}
}

// branchPullWithGuard re-validates the repository state and live branch refs
// against what the menu captured before pulling: status and refs must both be
// readable, no Git operation may be in progress, HEAD must still sit on the
// same commit of the same checked-out branch, and its upstream must still be
// live and unchanged.
func branchPullWithGuard(repo *git.Repo, expectedBranch, expectedHead, expectedUpstream string) (string, error) {
	if expectedBranch == "" || expectedHead == "" || expectedUpstream == "" {
		return "", fmt.Errorf("cannot pull: branch target is incomplete")
	}
	st, err := repo.Status()
	if err != nil {
		return "", fmt.Errorf("cannot pull %s: reading the repository state failed: %w", expectedBranch, err)
	}
	if st.State != "" {
		return "", fmt.Errorf("cannot pull %s: a %s is in progress; finish or abort it first", expectedBranch, st.State)
	}
	if st.Branch != expectedBranch || st.Head != expectedHead {
		return "", fmt.Errorf("cannot pull %s: HEAD or the current branch changed after the menu opened; review the repository and try again", expectedBranch)
	}
	refs, err := repo.Refs()
	if err != nil {
		return "", fmt.Errorf("cannot pull %s: reading repository references failed: %w", expectedBranch, err)
	}
	cur := findLocalBranch(refs, expectedBranch)
	if cur == nil || !cur.Current {
		return "", fmt.Errorf("cannot pull %s: the branch is no longer checked out", expectedBranch)
	}
	if cur.Hash != expectedHead {
		return "", fmt.Errorf("cannot pull %s: HEAD or the current branch changed after the menu opened; review the repository and try again", expectedBranch)
	}
	if cur.Upstream != expectedUpstream || cur.Gone {
		return "", fmt.Errorf("cannot pull %s: upstream changed after the menu opened; review the repository and try again", expectedBranch)
	}
	return repo.PullNote(git.PullOptions{Rebase: false})
}

// branchPushWithGuard re-validates live references before pushing: refs must
// be readable, the target remote must still exist, the local branch must still
// point to the commit captured when the menu item was chosen, and any tracked
// upstream captured for the push must still match and remain live. Pushes are
// never forced.
func branchPushWithGuard(repo *git.Repo, branch, expectedHash, remote, destBranch string, setUpstream bool, expectedUpstream string) (string, error) {
	if branch == "" || expectedHash == "" || remote == "" || destBranch == "" {
		return "", fmt.Errorf("cannot push: branch target is incomplete")
	}
	refs, err := repo.Refs()
	if err != nil {
		return "", fmt.Errorf("cannot push %s: reading repository references failed: %w", branch, err)
	}
	if !slices.Contains(refs.Remotes, remote) {
		return "", fmt.Errorf("cannot push %s: remote %q no longer exists", branch, remote)
	}
	local := findLocalBranch(refs, branch)
	if local == nil {
		return "", fmt.Errorf("cannot push %s: the branch no longer exists", branch)
	}
	if local.Hash != expectedHash {
		return "", fmt.Errorf("cannot push %s: branch moved after the menu opened; review the repository and try again", branch)
	}
	if expectedUpstream != "" && (local.Upstream != expectedUpstream || local.Gone) {
		return "", fmt.Errorf("cannot push %s: upstream changed after the menu opened; review the repository and try again", branch)
	}
	return repo.PushNote(remote, branch, destBranch, setUpstream, false, false)
}

func findLocalBranch(refs git.Refs, name string) *git.Branch {
	for i := range refs.Branches {
		if refs.Branches[i].Name == name {
			return &refs.Branches[i]
		}
	}
	return nil
}
