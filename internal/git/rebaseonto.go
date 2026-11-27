package git

import (
	"fmt"
	"strings"
)

// RebaseOntoOptions are the choices of the Rebase sheet.
type RebaseOntoOptions struct {
	// Stash puts local changes aside before the rebase and back after it.
	// Git keeps the autostash through conflicts and restores it on completion or abort.
	Stash bool
	// UpdateRefs moves the other local branches whose tips are among the
	// commits being replayed (git rebase --update-refs).
	UpdateRefs bool
}

// RebaseOnto rebases the current branch onto rev as o asks.
func (r *Repo) RebaseOnto(rev string, o RebaseOntoOptions) error {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return fmt.Errorf("a rebase needs a revision")
	}
	if err := validateRevisionArg(rev); err != nil {
		return err
	}
	if o.UpdateRefs {
		if err := r.requireRebaseUpdateRefs(); err != nil {
			return err
		}
	}
	args := []string{"rebase", "--no-autostash"}
	if o.Stash {
		args[1] = "--autostash"
	}
	if o.UpdateRefs {
		args = append(args, "--update-refs")
	}
	_, err := r.run(slow, append(args, rev)...)
	return err
}

// DependentBranches lists the local branches, other than the current one,
// whose tips are among the commits a rebase onto rev would replay. Those
// are the branches Update dependent branches moves.
func (r *Repo) DependentBranches(rev string) ([]string, error) {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return nil, nil
	}
	if err := validateRevisionArg(rev); err != nil {
		return nil, err
	}
	out, err := r.run(quick, "rev-list", rev+"..HEAD")
	if err != nil {
		return nil, err
	}
	in := map[string]bool{}
	for _, h := range strings.Fields(string(out)) {
		in[h] = true
	}
	refs, err := r.Refs()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range refs.Branches {
		if !b.Current && in[b.Hash] {
			names = append(names, b.Name)
		}
	}
	return names, nil
}
