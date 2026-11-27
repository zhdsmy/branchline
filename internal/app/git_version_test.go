package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// versionOverrideExecutor delegates every Git command to the real executor of
// the repository and intercepts only the bare "--version" probe, so tests can
// present an old, unparseable, or failing Git while the repository itself
// keeps working on the machine's real Git. It also counts probe invocations
// and can hold a probe at a gate, for deterministic cross-repository races.
type versionOverrideExecutor struct {
	git.Executor

	mu           sync.Mutex
	armed        bool
	out          string
	err          error
	probes       int
	probeGate    chan struct{}
	probeStarted chan struct{}
}

func newVersionOverrideExecutor() *versionOverrideExecutor {
	return &versionOverrideExecutor{probeStarted: make(chan struct{}, 16)}
}

func (v *versionOverrideExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if len(args) == 1 && args[0] == "--version" {
		v.mu.Lock()
		v.probes++
		armed, out, err := v.armed, v.out, v.err
		gate := v.probeGate
		started := v.probeStarted
		base := v.Executor
		v.mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		if gate != nil {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if armed {
			if err != nil {
				return nil, err
			}
			return []byte(out), nil
		}
		return base.Run(ctx, stdin, args...)
	}
	v.mu.Lock()
	base := v.Executor
	v.mu.Unlock()
	return base.Run(ctx, stdin, args...)
}

func (v *versionOverrideExecutor) probeCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.probes
}

// armVersion makes the probe answer with out, or fail with err when err is
// non-nil. Disarm with disarmVersion to return to the real Git.
func (v *versionOverrideExecutor) armVersion(out string, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.armed = true
	v.out = out
	v.err = err
}

func (v *versionOverrideExecutor) disarmVersion() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.armed = false
	v.out = ""
	v.err = nil
}

// holdProbes makes every later probe wait on a fresh gate until releaseProbes
// closes it; probes already started are unaffected.
func (v *versionOverrideExecutor) holdProbes() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.probeGate = make(chan struct{})
}

func (v *versionOverrideExecutor) releaseProbes() {
	v.mu.Lock()
	gate := v.probeGate
	v.probeGate = nil
	v.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

// awaitProbeStarted waits until the executor observed a probe invocation.
func (v *versionOverrideExecutor) awaitProbeStarted(t *testing.T) {
	t.Helper()
	select {
	case <-v.probeStarted:
	case <-time.After(queueTimeout):
		t.Fatal("timed out waiting for the version probe to start")
	}
}

// installVersionOverrides makes the app build repositories whose executor is
// the scripted wrapper for the working tree paths in overrides, and the
// ordinary executor everywhere else.
func installVersionOverrides(h *testHarness, overrides map[string]*versionOverrideExecutor) {
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		base := meta.Executor()
		if w, ok := overrides[meta.Path]; ok {
			w.mu.Lock()
			w.Executor = base
			w.mu.Unlock()
			return git.New(w)
		}
		return git.New(base)
	}
}

// requireHostGitVersion fails the test when the machine's own Git is older
// than major.minor: these tests exercise the real local Git and need it to
// meet the support floor so a fully supported host renders no banner.
func requireHostGitVersion(t *testing.T, major, minor int) git.GitVersion {
	t.Helper()
	out, err := tryGitCommand("", "--version")
	if err != nil {
		t.Fatalf("host git --version failed: %v\n%s", err, out)
	}
	v, err := git.ParseGitVersion(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("host git version %q did not parse: %v", strings.TrimSpace(out), err)
	}
	if !v.AtLeast(git.GitVersion{Major: major, Minor: minor}) {
		t.Fatalf("host git is %d.%d.%d, these tests require %d.%d or later", v.Major, v.Minor, v.Patch, major, minor)
	}
	return v
}

// openRepoViaSwitcher opens the named repository through the Repository
// Switcher popup, the same path a user takes, and drains the first load.
func openRepoViaSwitcher(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(name); err != nil {
		t.Fatalf("select repository %q: %v", name, err)
	}
	h.ui.Frame()
	h.drain()
}

// Opening a local repository probes its Git version exactly once: the answer
// is verified into the session, later reloads and page switches never run
// git --version again, and a fully supported Git renders no banner.
func TestGitVersionProbedOncePerRepository(t *testing.T) {
	host := requireHostGitVersion(t, 2, 25)
	repo := newGitRepo(t, "gv-probe-once")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	over := newVersionOverrideExecutor()
	h := newTestApp(t, repo.StoreRepo())
	installVersionOverrides(h, map[string]*versionOverrideExecutor{repo.Path(): over})

	openRepoViaSwitcher(t, h, "gv-probe-once")

	if got := over.probeCount(); got != 1 {
		t.Fatalf("probes after first load = %d, want exactly 1", got)
	}
	caps, ok := h.app.session.GitCapabilities()
	if !ok {
		t.Fatal("GitCapabilities() unverified after successful first load")
	}
	if !caps.MeetsMinimum {
		t.Fatalf("host git %+v reported below the support floor", caps.Version)
	}
	if caps.Version != host {
		t.Fatalf("session version = %+v, want the host git %+v", caps.Version, host)
	}
	if got := h.app.session.GitCapabilitiesErr(); got != nil {
		t.Fatalf("unexpected capabilities error: %v", got)
	}
	if h.app.session.SupportsRebaseUpdateRefs() != host.AtLeast(git.GitVersion{Major: 2, Minor: 38}) {
		t.Fatalf("SupportsRebaseUpdateRefs disagrees with host git %+v", host)
	}
	if h.app.session.SupportsStashStaged() != host.AtLeast(git.GitVersion{Major: 2, Minor: 35}) {
		t.Fatalf("SupportsStashStaged disagrees with host git %+v", host)
	}
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner rendered for a fully supported git; texts=%q", h.ui.Texts())
	}

	// Reloads, pagination, and page switches refresh the snapshot without
	// ever probing the version again.
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("switch to History: %v", err)
	}
	h.ui.Frame()
	h.app.session.Reload()
	h.drain()
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("switch to Changes: %v", err)
	}
	h.ui.Frame()
	h.drain()

	if got := over.probeCount(); got != 1 {
		t.Fatalf("probes after reloads and page switches = %d, want exactly 1", got)
	}
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner rendered for a fully supported git after reloads; texts=%q", h.ui.Texts())
	}
}

// A repository whose Git is older than 2.25 still opens, browses commits, and
// switches pages, while a non-blocking banner names the old version. Dismiss
// hides the banner without changing the verified low-version capabilities,
// and switching away and back re-evaluates the banner for the new generation.
func TestGitVersionLowVersionBanner(t *testing.T) {
	requireHostGitVersion(t, 2, 25)
	repoA := newGitRepo(t, "gv-old")
	repoA.CommitFile("a.txt", "alpha\n", "alpha commit")
	repoB := newGitRepo(t, "gv-plain")
	repoB.CommitFile("b.txt", "beta\n", "beta commit")

	over := newVersionOverrideExecutor()
	over.armVersion("git version 2.24.1", nil)
	h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
	installVersionOverrides(h, map[string]*versionOverrideExecutor{repoA.Path(): over})

	openRepoViaSwitcher(t, h, "gv-old")

	if !h.app.session.Loaded() {
		t.Fatal("repository with an old git failed to open")
	}
	if !h.ui.HasText("Git 2.24.1 is older than 2.25") {
		t.Fatalf("low-version banner text missing on Changes; texts=%q", h.ui.Texts())
	}
	if _, ok := h.ui.Find("Git Version Warning"); !ok {
		t.Fatal("banner status container missing on Changes")
	}
	if _, ok := h.ui.Find("Dismiss Git Version Warning"); !ok {
		t.Fatal("dismiss button missing on Changes")
	}
	caps, ok := h.app.session.GitCapabilities()
	if !ok || caps.MeetsMinimum {
		t.Fatalf("capabilities = %+v ok=%v, want verified below the floor", caps, ok)
	}
	if h.app.session.SupportsRebaseUpdateRefs() || h.app.session.SupportsStashStaged() {
		t.Fatal("high-version features reported supported on git 2.24.1")
	}

	// The banner never blocks the repository: History opens and real commits stay readable.
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("switch to History: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("alpha commit") {
		t.Fatalf("real commit missing on History; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("Git 2.24.1 is older than 2.25") {
		t.Fatalf("low-version banner text missing on History; texts=%q", h.ui.Texts())
	}

	// Dismiss hides the banner on both pages, but the verified capabilities stay low-version.
	if err := h.ui.Click("Dismiss Git Version Warning"); err != nil {
		t.Fatalf("click Dismiss: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner still visible after Dismiss; texts=%q", h.ui.Texts())
	}
	if err := h.ui.Click("Changes"); err != nil {
		t.Fatalf("switch to Changes: %v", err)
	}
	h.ui.Frame()
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner still visible on Changes after Dismiss; texts=%q", h.ui.Texts())
	}
	caps, ok = h.app.session.GitCapabilities()
	if !ok || caps.MeetsMinimum {
		t.Fatalf("dismiss changed the verified capabilities: %+v ok=%v", caps, ok)
	}

	// Switching to another repository re-evaluates: the plain repository has
	// no banner, and returning to the old one shows the banner again because
	// the dismissal belonged to the previous repository generation.
	openRepoViaSwitcher(t, h, "gv-plain")
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner leaked onto the fully supported repository; texts=%q", h.ui.Texts())
	}
	openRepoViaSwitcher(t, h, "gv-old")
	if !h.ui.HasText("Git 2.24.1 is older than 2.25") {
		t.Fatalf("dismissal wrongly survived the repository switch; texts=%q", h.ui.Texts())
	}
}

// The boundary versions classify exactly: 2.25.0 meets the support floor and
// renders no banner but predates rebase --update-refs and stash --staged,
// while 2.38.0 renders no banner and supports every flagged feature.
func TestGitVersionBoundaryVersions(t *testing.T) {
	requireHostGitVersion(t, 2, 25)
	cases := []struct {
		name         string
		raw          string
		wantUpdateRF bool
		wantStash    bool
	}{
		{"gv-v225", "git version 2.25.0", false, false},
		{"gv-v238", "git version 2.38.0", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newGitRepo(t, tc.name)
			repo.CommitFile("a.txt", "one\n", "initial commit")

			over := newVersionOverrideExecutor()
			over.armVersion(tc.raw, nil)
			h := newTestApp(t, repo.StoreRepo())
			installVersionOverrides(h, map[string]*versionOverrideExecutor{repo.Path(): over})

			openRepoViaSwitcher(t, h, tc.name)

			if h.ui.HasText("Git Version Warning") {
				t.Fatalf("banner rendered for %q; texts=%q", tc.raw, h.ui.Texts())
			}
			caps, ok := h.app.session.GitCapabilities()
			if !ok || !caps.MeetsMinimum {
				t.Fatalf("%q should verify at or above the floor: %+v ok=%v", tc.raw, caps, ok)
			}
			if got := h.app.session.SupportsRebaseUpdateRefs(); got != tc.wantUpdateRF {
				t.Fatalf("SupportsRebaseUpdateRefs = %v for %q, want %v", got, tc.raw, tc.wantUpdateRF)
			}
			if got := h.app.session.SupportsStashStaged(); got != tc.wantStash {
				t.Fatalf("SupportsStashStaged = %v for %q, want %v", got, tc.raw, tc.wantStash)
			}
		})
	}
}

// A failed version probe never blocks the repository and is never misreported
// as a low version: the error banner offers Retry, the retry re-probes in the
// background with a disabled Checking state, and a repaired Git clears the
// banner with verified capabilities.
func TestGitVersionProbeFailureAndRetry(t *testing.T) {
	host := requireHostGitVersion(t, 2, 25)
	repo := newGitRepo(t, "gv-retry")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	over := newVersionOverrideExecutor()
	over.armVersion("", errors.New("git --version: broken installation"))
	h := newTestApp(t, repo.StoreRepo())
	installVersionOverrides(h, map[string]*versionOverrideExecutor{repo.Path(): over})

	openRepoViaSwitcher(t, h, "gv-retry")

	// The repository opened despite the failed probe.
	if !h.app.session.Loaded() {
		t.Fatal("repository failed to open after a probe failure")
	}
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("switch to History: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("initial commit") {
		t.Fatalf("real commit missing after probe failure; texts=%q", h.ui.Texts())
	}

	// The banner reports a detection failure, never a low version.
	if !h.ui.HasText("Git version check failed") || !h.ui.HasText("broken installation") {
		t.Fatalf("probe failure banner text missing; texts=%q", h.ui.Texts())
	}
	if h.ui.HasText("older than 2.25") {
		t.Fatalf("probe failure misreported as a low version; texts=%q", h.ui.Texts())
	}
	if _, ok := h.app.session.GitCapabilities(); ok {
		t.Fatal("failed probe produced verified capabilities")
	}
	if h.app.session.GitCapabilitiesErr() == nil {
		t.Fatal("failed probe left no capabilities error")
	}
	if h.app.session.SupportsRebaseUpdateRefs() || h.app.session.SupportsStashStaged() {
		t.Fatal("unverified detection reported feature support")
	}
	if _, ok := h.ui.Find("Retry Git Version Check"); !ok {
		t.Fatal("retry button missing on the failure banner")
	}

	// Once the executor is repaired, Retry re-probes: while it runs the
	// button is disabled and reads Checking, and the verified answer then
	// clears the banner by itself.
	over.disarmVersion()
	probesBefore := over.probeCount()
	if err := h.ui.Click("Retry Git Version Check"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	if !h.app.session.GitCapabilitiesRetrying() {
		t.Fatal("session not retrying after clicking Retry")
	}
	h.ui.Frame()
	if !h.ui.HasText("Checking…") {
		t.Fatalf("retry button did not switch to Checking while retrying; texts=%q", h.ui.Texts())
	}
	// Re-entry while retrying is refused.
	if h.app.session.RetryGitCapabilities() {
		t.Fatal("retry re-entry accepted while a retry was in flight")
	}
	h.drain()

	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("banner survived a successful retry; texts=%q", h.ui.Texts())
	}
	caps, ok := h.app.session.GitCapabilities()
	if !ok || !caps.MeetsMinimum || caps.Version != host {
		t.Fatalf("capabilities after retry = %+v ok=%v, want host git %+v", caps, ok, host)
	}
	if h.app.session.GitCapabilitiesErr() != nil {
		t.Fatalf("capabilities error survived the successful retry: %v", h.app.session.GitCapabilitiesErr())
	}
	if got := over.probeCount(); got != probesBefore+1 {
		t.Fatalf("probes = %d, want exactly one retry probe after %d", got, probesBefore)
	}
}

// A retry completion that arrives after the user switched repositories is
// discarded with its stale generation: it never overwrites the new
// repository's verified capabilities and never resurrects a banner.
func TestGitVersionRetryDiscardedAfterRepoSwitch(t *testing.T) {
	requireHostGitVersion(t, 2, 25)
	repoA := newGitRepo(t, "gv-race-a")
	repoA.CommitFile("a.txt", "alpha\n", "alpha commit")
	repoB := newGitRepo(t, "gv-race-b")
	repoB.CommitFile("b.txt", "beta\n", "beta commit")

	overA := newVersionOverrideExecutor()
	overA.armVersion("", errors.New("git --version: broken installation"))
	overB := newVersionOverrideExecutor()
	overB.armVersion("git version 2.38.0", nil)
	h := newTestApp(t, repoA.StoreRepo(), repoB.StoreRepo())
	installVersionOverrides(h, map[string]*versionOverrideExecutor{
		repoA.Path(): overA,
		repoB.Path(): overB,
	})

	openRepoViaSwitcher(t, h, "gv-race-a")
	if !h.ui.HasText("Git version check failed") {
		t.Fatalf("expected the initial failure banner; texts=%q", h.ui.Texts())
	}

	// Freeze new workers, then start a retry whose probe is held at the gate.
	h.queue.PauseWorkers()
	overA.holdProbes()
	// The retry resolves to a verified low version: if this stale result is
	// ever applied, the new repository shows the low-version banner.
	overA.armVersion("git version 2.24.1", nil)
	if err := h.ui.Click("Retry Git Version Check"); err != nil {
		t.Fatalf("click Retry: %v", err)
	}
	retryWorker := h.queue.WaitWorker(t)

	// Switch repositories while the retry is frozen.
	openRepoViaSwitcherPaused(t, h, "gv-race-b")
	loadWorker := h.queue.WaitWorker(t)
	h.queue.ResumeWorkers()

	h.queue.ReleaseWorker(retryWorker)
	overA.awaitProbeStarted(t)
	h.queue.ReleaseWorker(loadWorker)
	overA.releaseProbes()
	h.drain()

	caps, ok := h.app.session.GitCapabilities()
	if !ok {
		t.Fatal("new repository capabilities unverified after its load completed")
	}
	if got := fmt.Sprintf("%d.%d.%d", caps.Version.Major, caps.Version.Minor, caps.Version.Patch); got != "2.38.0" {
		t.Fatalf("new repository version = %q, want 2.38.0 — the stale retry result leaked", got)
	}
	if h.app.session.GitCapabilitiesErr() != nil {
		t.Fatalf("stale retry error leaked into the new repository: %v", h.app.session.GitCapabilitiesErr())
	}
	if h.ui.HasText("Git Version Warning") {
		t.Fatalf("stale retry result raised a banner on the new repository; texts=%q", h.ui.Texts())
	}
	if !h.ui.HasText("beta commit") {
		if err := h.ui.Click("History"); err != nil {
			t.Fatalf("switch to History: %v", err)
		}
		h.ui.Frame()
		if !h.ui.HasText("beta commit") {
			t.Fatalf("new repository commits unreadable; texts=%q", h.ui.Texts())
		}
	}
}

// openRepoViaSwitcherPaused switches repositories through the popup while
// workers are paused: the popup clicks are UI events, but the load behind
// them stays gated until the test releases it. It does not drain.
func openRepoViaSwitcherPaused(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(name); err != nil {
		t.Fatalf("select repository %q: %v", name, err)
	}
	h.ui.Frame()
}

// Over a real loopback SSH connection the version probe reads the server's
// Git, never the local one: a remote git shimmed to answer 2.24.1 raises the
// low-version banner even though the machine's own Git is fully supported,
// and the remote repository stays open and browsable.
func TestGitVersionDetectionLoopbackSSH(t *testing.T) {
	host := requireHostGitVersion(t, 2, 25)
	repo, srv := loopbackSSHRepo(t)

	// Resolve the real git on the server before shadowing it.
	realGit := strings.TrimSpace(srv.RunShell(t, `for p in /opt/homebrew/bin/git /usr/local/bin/git /usr/bin/git; do if [ -x "$p" ]; then printf %s "$p"; exit 0; fi; done`))
	if realGit == "" {
		t.Fatal("no real git found on the loopback server")
	}

	// The shim answers only --version and delegates everything else to the
	// real git, so the remote repository keeps working end to end.
	remoteBin := srv.TempDir(t, "gv-fakebin")
	shim := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--version\" ]; then\n" +
		"    echo 'git version 2.24.1'\n" +
		"    exit 0\n" +
		"  fi\n" +
		"done\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	srv.WriteFile(t, remoteBin, "git", []byte(shim))
	srv.RunShell(t, "chmod +x "+git.ShellQuote(filepath.Join(remoteBin, "git")))

	// The SSH client wrapper prepends the shim directory to the remote PATH,
	// so only what the server runs is affected; the local git is untouched.
	wrapper := filepath.Join(t.TempDir(), "ssh-remote-version")
	wrapperScript := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"set -- \"$@\"\n" +
		"args=\"\"\n" +
		"while [ $# -gt 1 ]; do\n" +
		"  args=\"$args '$1'\"\n" +
		"  shift\n" +
		"done\n" +
		"eval \"set -- $args\"\n" +
		"exec " + git.ShellQuote(srv.SSHProgram()) + " \"$@\" \"PATH=" + remoteBin + ":\\$PATH; $last\"\n"
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o755); err != nil {
		t.Fatalf("write ssh wrapper: %v", err)
	}

	h := newTestApp(t, repo)
	h.app.openRepoFn = func(meta store.Repo) *git.Repo {
		return git.New(&git.SSHExecutor{
			Host:     srv.Host,
			Port:     srv.Port,
			User:     srv.User,
			Identity: srv.Identity,
			Dir:      meta.Path,
			SSH:      wrapper,
		})
	}

	openRepoViaSwitcher(t, h, repo.Name)

	if !h.app.session.Loaded() {
		t.Fatal("loopback SSH repository failed to open behind the version shim")
	}
	caps, ok := h.app.session.GitCapabilities()
	if !ok {
		t.Fatal("remote capabilities unverified after the first load")
	}
	if got := fmt.Sprintf("%d.%d.%d", caps.Version.Major, caps.Version.Minor, caps.Version.Patch); got != "2.24.1" {
		t.Fatalf("detected remote version = %q, want the shimmed 2.24.1", got)
	}
	// The host machine's own Git meets the floor: had the probe read the
	// local git instead of the remote one, no banner could exist.
	if !host.AtLeast(git.GitVersion{Major: 2, Minor: 25}) {
		t.Fatal("host git unexpectedly below the floor")
	}
	if !h.ui.HasText("Git 2.24.1 is older than 2.25") {
		t.Fatalf("remote low-version banner missing; texts=%q", h.ui.Texts())
	}
	if h.app.session.SupportsRebaseUpdateRefs() || h.app.session.SupportsStashStaged() {
		t.Fatal("shimmed 2.24.1 remote reported high-version feature support")
	}

	// The repository stays usable through the shimmed remote git.
	if err := h.ui.Click("History"); err != nil {
		t.Fatalf("switch to History: %v", err)
	}
	h.ui.Frame()
	if !h.ui.HasText("initial commit") {
		t.Fatalf("remote commit missing on History; texts=%q", h.ui.Texts())
	}
	wantHead := srv.RunGit(t, repo.Path, "rev-parse", "HEAD")
	if got := h.app.session.Snapshot().Status.Head; got != wantHead {
		t.Fatalf("remote HEAD = %q, want %q", got, wantHead)
	}
}
