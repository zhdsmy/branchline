package repository

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

type readerFixture struct {
	mu         sync.Mutex
	calls      []string
	errors     map[string]error
	statusDone bool
	premature  bool
	options    git.LogOptions
}

func (f *readerFixture) read(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "status" {
		f.statusDone = true
	} else if !f.statusDone {
		f.premature = true
	}
	f.calls = append(f.calls, name)
	return f.errors[name]
}
func (f *readerFixture) StatusOpts(bool) (git.Status, error) {
	return git.Status{Branch: "main", Head: "aa"}, f.read("status")
}
func (f *readerFixture) Refs() (git.Refs, error) { return git.Refs{}, f.read("refs") }
func (f *readerFixture) RemoteURLs() (map[string]string, error) {
	return map[string]string{"origin": "verified"}, f.read("urls")
}
func (f *readerFixture) Log(o git.LogOptions) ([]git.Commit, error) {
	f.options = o
	if len(o.Revs) > 0 {
		o.Revs[0] = "changed by reader"
	}
	return []git.Commit{{Hash: "aa", Subject: "first"}}, f.read("history")
}
func (f *readerFixture) Stashes() ([]git.Stash, error)         { return nil, f.read("stashes") }
func (f *readerFixture) Submodules() ([]string, error)         { return nil, f.read("submodules") }
func (f *readerFixture) Worktrees() ([]git.Worktree, error)    { return nil, f.read("worktrees") }
func (f *readerFixture) IndexFlags() ([]git.IndexFlags, error) { return nil, f.read("index") }
func (f *readerFixture) BisectProgress() (git.BisectProgress, bool, error) {
	return git.BisectProgress{}, false, f.read("bisect")
}

func TestLoadWarmsConnectionAndOwnsOptions(t *testing.T) {
	f := &readerFixture{}
	refs := []string{"main"}
	s, err := Load(f, Options{Log: git.LogOptions{Limit: 1, Revs: refs}})
	if err != nil {
		t.Fatal(err)
	}
	if f.premature || f.calls[0] != "status" || len(f.calls) != 9 {
		t.Fatalf("bad startup sequence: %v", f.calls)
	}
	if refs[0] != "main" {
		t.Fatal("reader mutated caller's options")
	}
	if !s.More || len(s.Commits) != 1 || len(s.Graph) != 1 || s.Status.Branch != "main" || s.RemoteURLs["origin"] != "verified" {
		t.Fatalf("incomplete snapshot: %+v", s)
	}
}

// capFixture is a Reader that also implements git.GitCapabilityReader with a
// scripted probe outcome.
type capFixture struct {
	readerFixture
	caps    git.GitCapabilities
	capsErr error
}

func (f *capFixture) Capabilities() (git.GitCapabilities, error) {
	if err := f.read("capabilities"); err != nil {
		return git.GitCapabilities{}, err
	}
	return f.caps, f.capsErr
}

func verifiedCaps() git.GitCapabilities {
	return git.GitCapabilities{
		Version:           git.GitVersion{Major: 2, Minor: 43, Patch: 0},
		RawVersion:        "git version 2.43.0",
		MeetsMinimum:      true,
		RebaseUpdateRefs:  true,
		StashStaged:       true,
		InitInitialBranch: true,
	}
}

func countCalls(f *readerFixture, name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

// The optional capability probe rides the same fan-out the other reads use:
// it runs once, never before StatusOpts, and its verified answer lands in the
// snapshot. A Reader without GitCapabilityReader is never probed, which the
// fixed nine-call sequence of TestLoadWarmsConnectionAndOwnsOptions proves.
func TestLoadCapabilitiesProbeRidesFanOut(t *testing.T) {
	f := &capFixture{caps: verifiedCaps()}
	s, err := Load(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if countCalls(&f.readerFixture, "capabilities") != 1 {
		t.Fatalf("capabilities probe calls = %v, want exactly one", f.calls)
	}
	if f.premature || f.calls[0] != "status" {
		t.Fatalf("probe ran before status established the connection: %v", f.calls)
	}
	if !s.HasCapabilities || s.Capabilities != verifiedCaps() || s.CapabilitiesErr != nil {
		t.Fatalf("snapshot capabilities = %+v has=%v err=%v, want verified probe", s.Capabilities, s.HasCapabilities, s.CapabilitiesErr)
	}
}

// SkipCapabilities leaves the probe out of the load entirely, for sessions
// that already cache a verified answer.
func TestLoadCapabilitiesSkipped(t *testing.T) {
	f := &capFixture{caps: verifiedCaps()}
	s, err := Load(f, Options{SkipCapabilities: true})
	if err != nil {
		t.Fatal(err)
	}
	if countCalls(&f.readerFixture, "capabilities") != 0 {
		t.Fatalf("probe ran despite SkipCapabilities: %v", f.calls)
	}
	if s.HasCapabilities || s.CapabilitiesErr != nil {
		t.Fatalf("skipped probe produced capability state: %+v err=%v", s.Capabilities, s.CapabilitiesErr)
	}
}

// A probe error never fails the load and is never presented as a verified
// answer: the snapshot stays complete, HasCapabilities is false, and the
// error is preserved for the caller.
func TestLoadCapabilitiesErrorNeverFailsLoad(t *testing.T) {
	probeErr := errors.New("git --version: exec failed")
	f := &capFixture{capsErr: probeErr}
	s, err := Load(f, Options{})
	if err != nil {
		t.Fatalf("probe failure broke the load: %v", err)
	}
	if len(s.Commits) != 1 || s.Status.Branch != "main" {
		t.Fatalf("snapshot incomplete after probe failure: %+v", s)
	}
	if s.HasCapabilities {
		t.Fatal("failed probe marked HasCapabilities = true")
	}
	if !errors.Is(s.CapabilitiesErr, probeErr) {
		t.Fatalf("CapabilitiesErr = %v, want %v", s.CapabilitiesErr, probeErr)
	}
	if s.Capabilities != (git.GitCapabilities{}) {
		t.Fatalf("failed probe produced capabilities: %+v", s.Capabilities)
	}
}

func TestLoadDoesNotReturnPartialRequiredResults(t *testing.T) {
	failure := errors.New("connection lost")
	for _, step := range []string{"status", "refs", "history", "stashes", "submodules", "worktrees", "index", "bisect"} {
		t.Run(step, func(t *testing.T) {
			f := &readerFixture{errors: map[string]error{step: failure}}
			s, err := Load(f, Options{})
			if !errors.Is(err, failure) || !reflect.DeepEqual(s, Snapshot{}) {
				t.Fatalf("failure became partial success: %+v %v", s, err)
			}
			if step == "status" && !reflect.DeepEqual(f.calls, []string{"status"}) {
				t.Fatalf("continued after authentication failed: %v", f.calls)
			}
		})
	}
}

func TestOptionalURLsAreDistinctFromAnEmptyCache(t *testing.T) {
	f := &readerFixture{errors: map[string]error{"urls": errors.New("missing remote URL")}}
	s, err := Load(f, Options{})
	if err != nil || len(s.Commits) != 1 || s.RemoteURLs != nil || len(s.Warnings) != 1 {
		t.Fatalf("optional failure broke history: %+v %v", s, err)
	}
	p, err := Poll(f, false)
	if err != nil || p.RemoteURLs != nil || len(p.Warnings) != 1 || p.Status.Head != "aa" {
		t.Fatalf("optional failure broke poll: %+v %v", p, err)
	}
}

func TestPollStopsOnFirstRequiredFailure(t *testing.T) {
	failure := errors.New("authentication refused")
	for _, step := range []string{"status", "refs", "stashes"} {
		t.Run(step, func(t *testing.T) {
			f := &readerFixture{errors: map[string]error{step: failure}}
			s, err := Poll(f, false)
			if !errors.Is(err, failure) || !reflect.DeepEqual(s, State{}) {
				t.Fatalf("failure became partial poll: %+v %v", s, err)
			}
			want := map[string]int{"status": 1, "refs": 2, "stashes": 3}[step]
			if len(f.calls) != want {
				t.Fatalf("continued after failure: %v", f.calls)
			}
		})
	}
}
