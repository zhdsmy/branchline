// Package repository coordinates repository reads without depending on the UI,
// persisted preferences, or a particular local/SSH transport.
package repository

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/zhdsmy/branchline/internal/git"
)

// Reader is the read side of a Git repository used to build a tab's data.
// Implementations must permit concurrent reads after StatusOpts succeeds.
type Reader interface {
	StatusOpts(bool) (git.Status, error)
	Refs() (git.Refs, error)
	RemoteURLs() (map[string]string, error)
	Log(git.LogOptions) ([]git.Commit, error)
	Stashes() ([]git.Stash, error)
	Submodules() ([]string, error)
	Worktrees() ([]git.Worktree, error)
	IndexFlags() ([]git.IndexFlags, error)
	BisectProgress() (git.BisectProgress, bool, error)
}

// Options is captured by the controller before starting a background read.
// The service never reads live tab fields while commands are in flight.
type Options struct {
	ShowIgnored bool
	Log         git.LogOptions
	// SkipCapabilities leaves the optional Git capability probe out of the
	// load. Controllers caching the probe result set it on every refresh
	// after the first successful snapshot so git --version runs once per
	// repository session instead of once per reload.
	SkipCapabilities bool
}

// State is the metadata shared by a complete load and a passive status poll.
// A nil RemoteURLs means the optional URL read failed; controllers must retain
// their previous verified URL cache. An empty, non-nil map clears that cache.
type State struct {
	Status     git.Status
	Refs       git.Refs
	Stashes    []git.Stash
	RemoteURLs map[string]string
	Warnings   []error
}

type Snapshot struct {
	State
	Commits    []git.Commit
	Graph      []git.GraphRow
	Submodules []string
	Worktrees  []git.Worktree
	IndexFlags []git.IndexFlags
	Bisect     git.BisectProgress
	Bisecting  bool
	More       bool
	// Capabilities is the verified capability probe of the repository's own
	// Git, valid only when HasCapabilities is true. A probe error never fails
	// the load: it lands in CapabilitiesErr with HasCapabilities false, and
	// must not be read as a verified old version or as full feature support.
	Capabilities    git.GitCapabilities
	HasCapabilities bool
	CapabilitiesErr error
}

// Load returns a complete snapshot or an error. Failed required reads never
// become a successful partial snapshot capable of erasing an existing tab.
// Status runs first to establish a shared SSH connection before fan-out.
func Load(r Reader, o Options) (Snapshot, error) {
	o.Log.Revs = slices.Clone(o.Log.Revs)
	if o.Log.Limit <= 0 {
		o.Log.Limit = 500
	}
	var s Snapshot
	var err error
	s.Status, err = r.StatusOpts(o.ShowIgnored)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read status: %w", err)
	}
	jobs := []struct {
		name string
		read func() error
	}{
		{"references", func() (e error) { s.Refs, e = r.Refs(); return }},
		{"history", func() (e error) { s.Commits, e = r.Log(o.Log); return }},
		{"stashes", func() (e error) { s.Stashes, e = r.Stashes(); return }},
		{"submodules", func() (e error) { s.Submodules, e = r.Submodules(); return }},
		{"worktrees", func() (e error) { s.Worktrees, e = r.Worktrees(); return }},
		{"index flags", func() (e error) { s.IndexFlags, e = r.IndexFlags(); return }},
		{"bisect", func() (e error) {
			s.Bisect, s.Bisecting, e = r.BisectProgress()
			if e == nil && s.Bisecting {
				s.Status.State = "bisect"
			}
			return
		}},
	}
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Go(func() {
			if err := job.read(); err != nil {
				errs[i] = fmt.Errorf("read %s: %w", job.name, err)
			}
		})
	}
	wg.Go(func() { s.RemoteURLs, s.Warnings = readURLs(r) })
	// The capability probe is optional and rides the same fan-out the other
	// reads use, so it adds neither a worker nor a round-trip of its own and
	// benefits from the connection StatusOpts already established. Its error
	// never joins errs: a repository that opens fine must stay open when
	// git --version cannot run or cannot be parsed, and callers decide what
	// a failed probe means instead of mistaking it for a version answer.
	if !o.SkipCapabilities {
		if cr, ok := r.(git.GitCapabilityReader); ok {
			wg.Go(func() {
				caps, err := cr.Capabilities()
				if err != nil {
					s.CapabilitiesErr = err
					return
				}
				s.Capabilities = caps
				s.HasCapabilities = true
			})
		}
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return Snapshot{}, err
	}
	git.FixRefKinds(s.Commits, s.Refs)
	s.Graph = git.Graph(s.Commits)
	s.More = len(s.Commits) >= o.Log.Limit
	return s, nil
}

// Poll reads only metadata needed to decide whether a tab needs a full reload.
// It stops after a required failure, avoiding repeated authentication attempts.
func Poll(r Reader, showIgnored bool) (State, error) {
	var s State
	var err error
	if s.Status, err = r.StatusOpts(showIgnored); err != nil {
		return State{}, err
	}
	if s.Refs, err = r.Refs(); err != nil {
		return State{}, err
	}
	if s.Stashes, err = r.Stashes(); err != nil {
		return State{}, err
	}
	s.RemoteURLs, s.Warnings = readURLs(r)
	return s, nil
}

func readURLs(r Reader) (map[string]string, []error) {
	urls, err := r.RemoteURLs()
	if err != nil {
		return nil, []error{fmt.Errorf("read remote URLs: %w", err)}
	}
	if urls == nil {
		urls = map[string]string{}
	}
	return urls, nil
}
