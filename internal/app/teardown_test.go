package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// This file measures what switching repositories, opening and closing the
// Settings window, and the quit teardown leave behind — goroutines grouped
// by the function on their stack, the process's open file descriptors, its
// child processes, and the askpass script directories — and guards the
// result: the counts after the thirtieth repetition must be those after the
// tenth. The tests count in this test process, which other tests of the
// package share, so only the counts of a measurement window are compared,
// never against zero.

// goroutineClasses are the categories a goroutine's stack falls into, in
// match order: the first marker a stack contains decides. Leaf-most first,
// so a session worker running git commands counts as a load worker, not a
// session worker, and a watch loop waiting inside the run-on-main handoff
// still counts as a watch loop.
var goroutineClasses = []struct {
	name    string
	markers []string
}{
	{"watch-loop", []string{"runAutoRefreshWatch"}},
	{"tick-loop", []string{"autoRefreshTickLoop"}},
	{"mygo-watch-run", []string{"mygo.(*FileWatcher).run"}},
	{"mygo-watch-fse", []string{"fseWatch"}},
	{"exec-child", []string{"os/exec.(*Cmd).watchCtx"}},
	{"repo-load", []string{"internal/repository.Load", "internal/repository.Poll"}},
	{"git-io", []string{"zhdsmy/branchline/internal/git."}},
	{"session-worker", []string{"repoSession"}},
	{"test-harness", []string{"zhdsmy/branchline/internal/app_test", "uiQueue", "testing.tRunner", "zhdsmy/branchline/internal/sshtest"}},
	{"app-other", []string{"zhdsmy/branchline/internal/app."}},
	{"mygo-other", []string{"egoist/mygo"}},
}

// resourceCounts is one measurement: goroutines grouped by class, the
// process's open file descriptors, its live child processes, and the
// askpass script directories in its private temporary directory.
type resourceCounts struct {
	groups   map[string]int
	total    int
	fds      int
	children int
	askpass  int
}

// countResources samples every resource once. The goroutine dump classifies
// each goroutine by the first matching class marker anywhere on its stack.
func countResources() resourceCounts {
	c := resourceCounts{groups: map[string]int{}}
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	dump := string(buf[:n])
	for _, chunk := range strings.Split(dump, "\n\n") {
		if !strings.HasPrefix(chunk, "goroutine ") {
			continue
		}
		c.total++
		name := "other"
		for _, class := range goroutineClasses {
			for _, marker := range class.markers {
				if strings.Contains(chunk, marker) {
					name = class.name
					break
				}
			}
			if name != "other" {
				break
			}
		}
		c.groups[name]++
	}
	if entries, err := os.ReadDir("/dev/fd"); err == nil {
		c.fds = len(entries)
	}
	c.children = len(childPIDs(os.Getpid()))
	if matches, err := filepath.Glob(filepath.Join(os.TempDir(), "branchline-askpass*")); err == nil {
		c.askpass = len(matches)
	}
	return c
}

// childPIDs lists the live processes whose parent is pid, as
// ps -o pid,ppid,command filtered down reports them. ps itself counts as a
// child while it runs, and is dropped by its name.
func childPIDs(pid int) []int {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid != pid {
			continue
		}
		if fields[2] == "ps" || strings.HasSuffix(fields[2], "/ps") {
			continue
		}
		if child, err := strconv.Atoi(fields[0]); err == nil {
			pids = append(pids, child)
		}
	}
	return pids
}

// equalCounts reports whether a and b agree on every leak-relevant count:
// each goroutine class, the descriptors, the children and the askpass
// directories. The unclassified total is noise of the shared test process —
// other tests running in it before or alongside leave goroutines behind —
// and is reported but never compared.
func equalCounts(a, b resourceCounts) bool {
	if a.fds != b.fds || a.children != b.children || a.askpass != b.askpass {
		return false
	}
	names := map[string]bool{}
	for name := range a.groups {
		names[name] = true
	}
	for name := range b.groups {
		names[name] = true
	}
	for name := range names {
		if a.groups[name] != b.groups[name] {
			return false
		}
	}
	return true
}

// formatCounts renders one measurement compactly for the measurement log.
func formatCounts(c resourceCounts) string {
	var names []string
	for name, n := range c.groups {
		if n > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, c.groups[name]))
	}
	return fmt.Sprintf("goroutines{%s} total=%d fds=%d children=%d askpass-dirs=%d", strings.Join(parts, " "), c.total, c.fds, c.children, c.askpass)
}

// tryServe services one pending main-thread handoff if one is waiting,
// returning whether it did. Unlike serveOne it never blocks: draining a
// quiescent app produces no handoffs at all.
func (m *mainHandoff) tryServe() bool {
	select {
	case call := <-m.in:
		call.fn()
		close(call.done)
		return true
	default:
		return false
	}
}

// pumpHandoffs services every main-thread handoff waiting right now, as the
// production main thread would service them between frames. Handoffs of a
// retiring watch loop still in flight are picked up by the bounded loops of
// measureStable.
func pumpHandoffs(m *mainHandoff) {
	if m == nil {
		return
	}
	for m.tryServe() {
	}
}

// measureStable waits for the app's background activity to settle and the
// resource counts to hold still, then returns the stable measurement. It is
// a bounded loop instead of a fixed sleep: each pass drains queued worker
// completions, services pending main-thread handoffs — a retiring watch
// loop exits only through them — and compares two samples taken 20 ms
// apart. Three equal consecutive samples count as stable.
func measureStable(t *testing.T, h *testHarness, rig *autoRefreshRig, what string) resourceCounts {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	equal := 0
	var prev resourceCounts
	for time.Now().Before(deadline) {
		h.drain()
		pumpHandoffs(rigOrNil(rig))
		cur := countResources()
		if equalCounts(cur, prev) {
			equal++
			if equal >= 2 {
				return cur
			}
		} else {
			equal = 0
		}
		prev = cur
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: resource counts never stabilized; last two samples:\n  %s\n  %s", what, formatCounts(prev), formatCounts(countResources()))
	return resourceCounts{}
}

func rigOrNil(rig *autoRefreshRig) *mainHandoff {
	if rig == nil {
		return nil
	}
	return rig.main
}

// logMeasurement writes one measurement line the report reads back.
func logMeasurement(t *testing.T, scenario, round string, c resourceCounts) {
	t.Helper()
	t.Logf("MEASURE %s %s: %s", scenario, round, formatCounts(c))
}

// assertFlatCounts fails when the counts after the later round exceed those
// after the earlier one in any compared category: that is a leak. Stability
// of the measurement itself is measureStable's preconditions, so equality
// is the correct expectation.
func assertFlatCounts(t *testing.T, scenario string, tenth, thirtieth resourceCounts) {
	t.Helper()
	if equalCounts(tenth, thirtieth) {
		return
	}
	names := map[string]bool{}
	for name := range tenth.groups {
		names[name] = true
	}
	for name := range thirtieth.groups {
		names[name] = true
	}
	var diffs []string
	var sorted []string
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		if tenth.groups[name] != thirtieth.groups[name] {
			diffs = append(diffs, fmt.Sprintf("goroutines[%s] %d → %d", name, tenth.groups[name], thirtieth.groups[name]))
		}
	}
	if tenth.fds != thirtieth.fds {
		diffs = append(diffs, fmt.Sprintf("fds %d → %d", tenth.fds, thirtieth.fds))
	}
	if tenth.children != thirtieth.children {
		diffs = append(diffs, fmt.Sprintf("children %d → %d", tenth.children, thirtieth.children))
	}
	if tenth.askpass != thirtieth.askpass {
		diffs = append(diffs, fmt.Sprintf("askpass-dirs %d → %d", tenth.askpass, thirtieth.askpass))
	}
	t.Fatalf("%s leaks: counts after the later round exceed the earlier:\n  %s\n  earlier: %s\n  later:   %s", scenario, strings.Join(diffs, "\n  "), formatCounts(tenth), formatCounts(thirtieth))
}

// clickSwitch selects the named repository in the Repository Switcher, as a
// user switching between repositories does.
func clickSwitch(t *testing.T, h *testHarness, name string) {
	t.Helper()
	if err := h.ui.Click("Repository Switcher"); err != nil {
		t.Fatalf("open Repository Switcher: %v", err)
	}
	h.ui.Frame()
	if err := h.ui.Click(name); err != nil {
		t.Fatalf("select repo %q in the switcher: %v", name, err)
	}
	h.ui.Frame()
}

// teardownRepos builds three real local repositories with one commit each,
// opens the first, and returns all three in switch order.
func teardownRepos(t *testing.T, h *testHarness) []*gitRepoFixture {
	t.Helper()
	repos := []*gitRepoFixture{
		newGitRepo(t, "td-a"),
		newGitRepo(t, "td-b"),
		newGitRepo(t, "td-c"),
	}
	for i, r := range repos {
		r.CommitFile(fmt.Sprintf("f%d.txt", i), fmt.Sprintf("content %d\n", i), "initial commit")
	}
	for _, r := range repos[1:] {
		if _, err := h.store.Add(r.StoreRepo()); err != nil {
			t.Fatalf("add repo %q: %v", r.Path(), err)
		}
	}
	h.openRepo(repos[0].StoreRepo())
	return repos
}

// Scenario a: thirty switches between three real local repositories through
// the Repository Switcher, each followed by one refresh tick. The counts of
// every resource after the thirtieth switch must be those after the tenth:
// the old repository's session worker completes, its read results are
// dropped, and nothing stays behind.
func TestTeardownSwitchLocalRepos(t *testing.T) {
	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	repos := teardownRepos(t, h)

	// The interval never passes in the fake clock, so the tick only keeps
	// the watch in step with the open repository and starts no checks.
	order := []string{repos[1].Path(), repos[2].Path(), repos[0].Path()}
	names := map[string]string{}
	for _, r := range repos {
		names[r.Path()] = r.StoreRepo().Name
	}
	var tenth, thirtieth resourceCounts
	for round := 1; round <= 30; round++ {
		target := order[(round-1)%len(order)]
		clickSwitch(t, h, names[target])
		autoRefreshTick(h.app)
		h.drain()
		pumpHandoffs(rig.main)
		if round == 10 {
			tenth = measureStable(t, h, rig, "switch-local round 10")
			logMeasurement(t, "a.switch-local", "round10", tenth)
		}
		if round == 30 {
			thirtieth = measureStable(t, h, rig, "switch-local round 30")
			logMeasurement(t, "a.switch-local", "round30", thirtieth)
		}
	}
	assertFlatCounts(t, "switch-local", tenth, thirtieth)
}

// Scenario b: the same switches with the production file watch factory —
// the real recursive mygo watch — so every round starts one watch and the
// next round retires it. A retired watch's goroutines are the watch loop of
// runAutoRefreshWatch, the FileWatcher's run goroutine, and the backend's
// FSEvents wait; all three must be gone after the measurement settles.
func TestTeardownSwitchRealWatch(t *testing.T) {
	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	st := rig.state
	st.watchFactory = mygoWatchFactory
	repos := teardownRepos(t, h)
	autoRefreshTick(h.app)
	h.drain()
	pumpHandoffs(rig.main)
	if st.watch == nil {
		// WatchFiles cannot run before the application loop starts; in a
		// headless test it either fails or panics. The card's fallback is
		// the fake watch, which still exercises the watch loop itself.
		t.Log("MEASURE b.switch-real-watch: mygoWatchFactory produced no watch in the headless test; falling back to the fake factory")
		st.watchFactory = rig.factory.make
	} else {
		t.Log("MEASURE b.switch-real-watch: production mygo watch factory in use")
	}

	order := []string{repos[1].Path(), repos[2].Path(), repos[0].Path()}
	names := map[string]string{}
	for _, r := range repos {
		names[r.Path()] = r.StoreRepo().Name
	}
	var tenth, thirtieth resourceCounts
	for round := 1; round <= 30; round++ {
		target := order[(round-1)%len(order)]
		clickSwitch(t, h, names[target])
		autoRefreshTick(h.app)
		h.drain()
		pumpHandoffs(rig.main)
		if round == 10 {
			tenth = measureStable(t, h, rig, "switch-real-watch round 10")
			logMeasurement(t, "b.switch-real-watch", "round10", tenth)
		}
		if round == 30 {
			thirtieth = measureStable(t, h, rig, "switch-real-watch round 30")
			logMeasurement(t, "b.switch-real-watch", "round30", thirtieth)
		}
	}
	assertFlatCounts(t, "switch-real-watch", tenth, thirtieth)
}

// gatingExecutor passes every git invocation to the real executor only once
// the test releases it, capturing each invocation's context so the test can
// ask whether a repository switch cancels the reads of the repository it
// leaves. entered fires on the first gated call, so the test knows the read
// is in flight without polling.
type gatingExecutor struct {
	real    git.Executor
	mu      sync.Mutex
	release chan struct{}
	entered chan struct{}
	fired   bool
	ctxs    []context.Context
}

func newGatingExecutor(real git.Executor) *gatingExecutor {
	return &gatingExecutor{real: real}
}

// arm makes the next invocations wait on a fresh release channel, delegating
// to real once released.
func (g *gatingExecutor) arm(real git.Executor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.real = real
	g.release = make(chan struct{})
	g.entered = make(chan struct{})
	g.fired = false
	g.ctxs = nil
}

func (g *gatingExecutor) unblock() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.release != nil {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	}
}

func (g *gatingExecutor) contexts() []context.Context {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]context.Context(nil), g.ctxs...)
}

func (g *gatingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	g.mu.Lock()
	release := g.release
	real := g.real
	g.ctxs = append(g.ctxs, ctx)
	if !g.fired && g.entered != nil {
		g.fired = true
		close(g.entered)
	}
	g.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return real.Run(ctx, stdin, args...)
}

func (g *gatingExecutor) Describe() string { return g.real.Describe() }

// Scenario c: a snapshot read of repository A held in flight when the user
// switches to repository B is not killed — its context is never canceled —
// it runs to completion and its result is dropped by the generation checks.
// Running to completion is proven two ways: the contexts the reads run
// under stay open across the switch, and a slowed-down git log observed
// through the child process table survives the switch and finishes. Thirty
// rounds repeat the in-flight switch; the resource counts after the tenth
// and the thirtieth are the comparison.
func TestTeardownInFlightReadSwitch(t *testing.T) {
	repoA := newGitRepo(t, "td-inflight-a")
	repoA.CommitFile("a.txt", "a\n", "commit a")
	repoB := newGitRepo(t, "td-inflight-b")
	repoB.CommitFile("b.txt", "b\n", "commit b")

	h := newTestApp(t)
	repos := map[string]*gitRepoFixture{}
	saved := map[string]string{}
	for _, r := range []*gitRepoFixture{repoA, repoB} {
		meta, err := h.store.Add(r.StoreRepo())
		if err != nil {
			t.Fatalf("add repo: %v", err)
		}
		saved[r.Path()] = meta.ID
		repos[meta.ID] = r
	}
	h.openRepo(repoA.StoreRepo())
	h.drain()

	gating := newGatingExecutor(nil)
	t.Cleanup(gating.unblock)

	var tenth, thirtieth resourceCounts
	for round := 1; round <= 30; round++ {
		cur := h.app.session.Meta().ID
		var other string
		if cur == saved[repoA.Path()] {
			other = saved[repoB.Path()]
		} else {
			other = saved[repoA.Path()]
		}
		// Arm the current repository's executor while no worker is in
		// flight — the drain of the previous round guarantees that — then
		// start a reload whose reads all hold at the gate. The object
		// mutated here is the one restored after the round, never the
		// repository switched to.
		curRepo := h.app.session.Repo()
		realExec := curRepo.Exec
		gating.arm(realExec)
		curRepo.Exec = gating
		h.app.session.Reload()
		select {
		case <-gating.entered:
		case <-time.After(queueTimeout):
			gating.unblock()
			t.Fatal("round started no read within the timeout")
		}

		// Switch away mid-read: through the switcher, as a user does. The
		// contexts of the in-flight reads must not be canceled — a canceled
		// context is what would kill the git child processes.
		clickSwitch(t, h, repos[other].StoreRepo().Name)
		canceled := false
		for _, ctx := range gating.contexts() {
			select {
			case <-ctx.Done():
				canceled = true
			default:
			}
		}
		if canceled {
			t.Logf("MEASURE c.in-flight round%d: read contexts WERE canceled at the switch — children are killed", round)
		} else if round == 1 {
			t.Logf("MEASURE c.in-flight round%d: read contexts stay open across the switch — the old repository's git child processes run to completion (bounded by their timeouts), they are not killed", round)
		}

		// Release the old reads; their completions must be dropped without
		// overwriting the new repository's state. The executor swap back
		// happens only once every worker is done: the fanned-out reads
		// re-read the field on every git invocation, and writing it while
		// a worker runs would race them.
		gating.unblock()
		h.drain()
		curRepo.Exec = realExec
		if got := h.app.session.Meta().ID; got != other {
			t.Fatalf("active repository = %q, want %q after the switch", got, other)
		}
		if err := h.app.session.LoadErr(); err != nil {
			t.Fatalf("new repository failed to load: %v", err)
		}
		wantHead := headOf(t, repos[other])
		if got := h.app.session.Snapshot().Status.Head; got != wantHead {
			t.Fatalf("snapshot HEAD = %q, want the new repository's %q — the dropped read overwrote state", got, wantHead)
		}
		if round == 10 {
			tenth = measureStable(t, h, nil, "in-flight after round 10")
			logMeasurement(t, "c.in-flight", "round10", tenth)
		}
		if round == 30 {
			thirtieth = measureStable(t, h, nil, "in-flight after round 30")
			logMeasurement(t, "c.in-flight", "round30", thirtieth)
		}
	}

	// Empirical half: a git shim that holds log until the test releases it
	// makes the read's child processes observable across a switch. They must
	// still be there right after switching — nothing kills them — and gone
	// once they run out. The shim waits on a file the test creates after the
	// assertion rather than on a fixed delay: switching can take longer than
	// any chosen delay on a loaded machine, and a child that finished on its
	// own before the count would look exactly like a killed one. The poll
	// loop still ends by itself half a minute in, so a failing test cannot
	// leave the shims behind.
	release := filepath.Join(t.TempDir(), "release-git-log")
	slowLog := filepath.Join(t.TempDir(), "git-slow-log")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	shim := "#!/bin/sh\n" +
		"hold=\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = log ]; then hold=yes; break; fi\n" +
		"done\n" +
		"if [ -n \"$hold\" ]; then\n" +
		"  _w=0\n" +
		"  while [ ! -e " + git.ShellQuote(release) + " ] && [ \"$_w\" -lt 30 ]; do\n" +
		"    sleep 1\n" +
		"    _w=$((_w + 1))\n" +
		"  done\n" +
		"fi\n" +
		"exec " + git.ShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(slowLog, []byte(shim), 0o755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Cleanup(func() {
		// A failure before the release must not leave the shim's polls
		// running for their half-minute bound.
		_ = os.WriteFile(release, []byte("release\n"), 0o644)
	})
	if ex, ok := h.app.session.Repo().Exec.(*git.LocalExecutor); ok {
		copy := *ex
		copy.Git = slowLog
		h.app.session.Repo().Exec = &copy
	} else {
		t.Fatalf("expected a local executor, got %T", h.app.session.Repo().Exec)
	}
	h.app.session.Reload()
	// The load fans out several children; wait until at least one is live.
	var sawChild bool
	deadline := time.Now().Add(queueTimeout)
	for time.Now().Before(deadline) && !sawChild {
		sawChild = len(childPIDs(os.Getpid())) > 0
		time.Sleep(5 * time.Millisecond)
	}
	if !sawChild {
		t.Fatal("no git child process of the slowed load ever appeared")
	}
	cur := h.app.session.Meta().ID
	other := saved[repoA.Path()]
	if cur == other {
		other = saved[repoB.Path()]
	}
	clickSwitch(t, h, repos[other].StoreRepo().Name)
	if n := len(childPIDs(os.Getpid())); n == 0 {
		t.Fatal("the old repository's git child processes are gone immediately after the switch — they WERE killed")
	} else {
		t.Logf("MEASURE c.in-flight children: %d git child process(es) of the old repository survive the switch", n)
	}
	// The shim proved its point; let the held reads finish so the drain and
	// the measurements below see a settled app rather than polls.
	if err := os.WriteFile(release, []byte("release\n"), 0o644); err != nil {
		t.Fatalf("release the shim: %v", err)
	}
	h.drain()
	final := measureStable(t, h, nil, "in-flight after the slowed load")
	logMeasurement(t, "c.in-flight", "final", final)
	assertFlatCounts(t, "c.in-flight", thirtieth, final)
	if !equalCounts(tenth, thirtieth) {
		t.Fatalf("in-flight switching leaks across rounds:\n  round 10: %s\n  round 30: %s", formatCounts(tenth), formatCounts(thirtieth))
	}
}

// Scenario d: ten switches between a loopback SSH repository and a local
// one. SSH reads are the slowest the app starts, so this scenario measures
// the heaviest child-process churn; the multiplexed ssh control master is
// one connection per server, daemonized out of the process's children by
// ssh itself, and expires ten minutes after its last use by design.
func TestTeardownSwitchSSHMixed(t *testing.T) {
	repo, _ := loopbackSSHRepo(t)
	local := newGitRepo(t, "td-mixed-local")
	local.CommitFile("l.txt", "l\n", "local commit")

	h := newTestApp(t)
	sshMeta, err := h.store.Add(repo)
	if err != nil {
		t.Fatalf("add ssh repo: %v", err)
	}
	localMeta, err := h.store.Add(local.StoreRepo())
	if err != nil {
		t.Fatalf("add local repo: %v", err)
	}
	h.openRepo(sshMeta)
	h.drain()
	if err := h.app.session.LoadErr(); err != nil {
		t.Fatalf("initial SSH load failed: %v", err)
	}

	// A round is one switch; the measurement after round 5 (of 10) plays
	// the part of round 10 of 30 in the 30-round scenarios.
	half := 5
	var first, last resourceCounts
	for round := 1; round <= 10; round++ {
		var target string
		if h.app.session.Meta().ID == sshMeta.ID {
			target = localMeta.Name
		} else {
			target = sshMeta.Name
		}
		clickSwitch(t, h, target)
		h.drain()
		if err := h.app.session.LoadErr(); err != nil {
			t.Fatalf("load failed in round %d: %v", round, err)
		}
		if round == half {
			first = measureStable(t, h, nil, "switch-ssh round 5")
			logMeasurement(t, "d.switch-ssh", "round5", first)
		}
		if round == 10 {
			last = measureStable(t, h, nil, "switch-ssh round 10")
			logMeasurement(t, "d.switch-ssh", "round10", last)
		}
	}
	assertFlatCounts(t, "switch-ssh", first, last)
}

// Scenario e: twenty open-close cycles of the Settings window through its
// real entry, ⌘,. Headless tests measure only what the window logic itself
// holds — goroutines, descriptors, children — since the native window never
// exists; the production window's teardown is AppKit's and outside what a
// test process can count.
func TestTeardownSettingsWindowChurn(t *testing.T) {
	h := newTestApp(t)
	rec := installSettingsHooks(h)

	var tenth, twentieth resourceCounts
	for round := 1; round <= 20; round++ {
		openSettingsThroughShortcut(t, h)
		if !h.app.settingsOpen {
			t.Fatalf("round %d: Settings did not open", round)
		}
		h.app.closeSettingsWindow()
		h.drain()
		if round == 10 {
			tenth = measureStable(t, h, nil, "settings round 10")
			logMeasurement(t, "e.settings-churn", "round10", tenth)
		}
		if round == 20 {
			twentieth = measureStable(t, h, nil, "settings round 20")
			logMeasurement(t, "e.settings-churn", "round20", twentieth)
		}
	}
	if rec.opened != 20 {
		t.Fatalf("the window hook ran %d times over 20 rounds, want exactly 20", rec.opened)
	}
	assertFlatCounts(t, "settings-churn", tenth, twentieth)
}

// openSettingsThroughShortcut presses ⌘, in the main window, the keystroke
// the production menu binds to Settings.
func openSettingsThroughShortcut(t *testing.T, h *testHarness) {
	t.Helper()
	h.ui.Key(ui.Cmd, ui.KeyComma)
	h.ui.Frame()
}

// Scenario f: the quit teardown — closing the tick loop's stop channel and
// closeAutoRefresh, the two steps of the production OnQuit callback — ends
// both the once-a-second tick goroutine and the working-tree watch loop.
func TestTeardownQuitReleasesWatchAndTick(t *testing.T) {
	repo := newGitRepo(t, "td-quit")
	repo.CommitFile("a.txt", "one\n", "initial commit")

	h := newTestApp(t)
	rig := installAutoRefresh(t, h)
	h.openRepo(repo.StoreRepo())
	h.drain()
	autoRefreshTick(h.app)
	h.drain()
	pumpHandoffs(rig.main)
	if rig.state.watch == nil {
		t.Fatal("fixture: no working-tree watch for the open local repository")
	}
	loop := startTestTickLoop(t, h.app, rig.state)
	for i := 0; i < 3; i++ {
		pumpTick(t, rig, loop.ticks)
	}
	before := measureStable(t, h, rig, "quit before teardown")
	logMeasurement(t, "f.quit", "before", before)
	if before.groups["watch-loop"] == 0 {
		t.Fatal("fixture: no watch loop running before the teardown")
	}
	if before.groups["tick-loop"] == 0 {
		t.Fatal("fixture: no tick loop running before the teardown")
	}

	// The production OnQuit ordering: stop the ticks, then close the watch.
	loop.Close()
	closeAutoRefresh(h.app)
	pumpHandoffs(rig.main)

	after := measureStable(t, h, rig, "quit after teardown")
	logMeasurement(t, "f.quit", "after", after)
	if got := after.groups["watch-loop"]; got != 0 {
		t.Fatalf("watch loops after the quit teardown = %d, want 0", got)
	}
	if got := after.groups["tick-loop"]; got != 0 {
		t.Fatalf("tick loops after the quit teardown = %d, want 0", got)
	}
	select {
	case <-loop.done:
	default:
		t.Fatal("the tick loop goroutine is still inside autoRefreshTickLoop after the teardown")
	}
	if after.fds > before.fds {
		t.Fatalf("file descriptors grew through the teardown: %d → %d", before.fds, after.fds)
	}
}
