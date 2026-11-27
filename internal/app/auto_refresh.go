package app

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/zhdsmy/branchline/internal/repository"
	"github.com/zhdsmy/branchline/internal/store"
)

// autoRefreshWatcher is the file watch one local repository uses: the subset
// of mygo.FileWatcher the refresh loop needs, behind an interface so tests
// can substitute a fake. WatchFiles cannot run before mygo.App.Run, which
// tests never start.
type autoRefreshWatcher interface {
	Next(ctx context.Context) ([]mygo.FileEvent, error)
	Close() error
}

// autoRefreshWatchFactory starts a watch on a working tree. The production
// implementation is mygoWatchFactory; tests substitute a fake whose events
// they send by hand.
type autoRefreshWatchFactory func(ctx context.Context, path string) (autoRefreshWatcher, error)

// mygoWatchFactory is the production watch: recursive and hints-only — a
// hint only says something under a directory changed, which is all a status
// refresh needs — with the Git metadata directory left out at every depth.
// Changes inside .git, such as a commit made in a terminal, reach the app
// through the interval and activation checks instead.
func mygoWatchFactory(ctx context.Context, path string) (autoRefreshWatcher, error) {
	return mygo.WatchFiles(ctx, path, mygo.FileWatchOptions{
		Recursive: true,
		HintsOnly: true,
		Debounce:  100 * time.Millisecond,
		MaxDelay:  500 * time.Millisecond,
		Ignore:    watchIgnoreGitDir,
	})
}

// watchIgnoreGitDir keeps any directory named .git out of the watch, at the
// top or inside a nested tree; a plain .gitignore file or a .github directory
// is still watched.
func watchIgnoreGitDir(rel string, isDir bool) bool {
	return isDir && (rel == ".git" || strings.HasSuffix(rel, "/.git"))
}

// autoRefreshState is the automatic repository refresh state of one app,
// mounted through featureState. All fields are owned by the UI thread;
// background work reaches it exclusively through callbacks it hands to
// runOnMain or post. The clock, the watch factory, the run-on-main handoff
// and the foreground flag live here so tests can substitute them: production
// wires the mygo watch factory, mygo.RunOnMain and a tick goroutine in Run,
// tests drive the pieces with their own channels and a synchronous handoff.
type autoRefreshState struct {
	clock        func() time.Time
	watchFactory autoRefreshWatchFactory
	runOnMain    func(func())
	foreground   bool

	checking    bool
	lastSuccess time.Time
	failedUntil time.Time
	dirty       bool
	repoGen     uint64

	watch     autoRefreshWatcher
	watchPath string
	watchStop context.CancelFunc
	watchGen  uint64
}

func autoRefresh(a *app) *autoRefreshState {
	return featureState(a, func() *autoRefreshState {
		return &autoRefreshState{
			clock:      time.Now,
			foreground: true,
		}
	})
}

// refreshIntervalSeconds is the interval a tick counts down in seconds: a
// stored zero reads as 60 through the store, and a negative value stays as
// saved, which makes every foreground tick due.
func refreshIntervalSeconds(s store.Settings) int {
	if s.StatusUpdateSeconds == 0 {
		return 60
	}
	return s.StatusUpdateSeconds
}

// autoRefreshTick is one beat of the automatic refresh, run on the UI thread
// once a second in production. It keeps the local working-tree watch in step
// with the open repository and the setting, then starts at most one status
// check: a hint refreshes as soon as it can, an interval due only counts
// while automatic refresh is on and the application is in front. Hints
// arriving in the background or while refresh is off keep until the next
// activation.
func autoRefreshTick(a *app) {
	st := autoRefresh(a)
	syncAutoRefreshWatch(a, st)
	if !autoRefreshCanCheck(a, st) {
		return
	}
	prefs := a.store.Settings()
	if prefs.DisableStatusUpdate || !st.foreground {
		return
	}
	now := st.clock()
	if !st.failedUntil.IsZero() && now.Before(st.failedUntil) {
		return
	}
	if st.dirty {
		startAutoRefreshCheck(a, st, true)
		return
	}
	interval := time.Duration(refreshIntervalSeconds(prefs)) * time.Second
	if now.Sub(st.lastSuccess) > interval {
		startAutoRefreshCheck(a, st, false)
	}
}

// autoRefreshForeground records whether the application is the active one
// and, on becoming active, checks at once even while automatic refresh is
// off — the manual fallback every activation is. A check already in flight
// makes the activation a no-op.
func autoRefreshForeground(a *app, active bool) {
	st := autoRefresh(a)
	st.foreground = active
	if active && autoRefreshCanCheck(a, st) {
		startAutoRefreshCheck(a, st, true)
	}
}

// autoRefreshCanCheck reports the conditions every check needs: an open
// repository whose snapshot loaded, with no read or write of the session in
// flight — a fetch, pull or push counts through the busy flag — and no
// status check already running.
func autoRefreshCanCheck(a *app, st *autoRefreshState) bool {
	if st.checking || !a.session.HasRepo() || !a.session.Loaded() {
		return false
	}
	if a.session.Loading() || a.session.Busy() {
		return false
	}
	return !syncInFlight(a)
}

// syncInFlight reports whether a sync operation of the app runs. Sync
// operations run inside session.do, so the busy flag always covers them;
// the explicit count is kept for what a future entry point outside the
// session would mean.
func syncInFlight(a *app) bool {
	st := syncStateFor(a)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.inflight > 0
}

// startAutoRefreshCheck begins one passive status poll on a worker. The
// repository copy never asks for credentials: the poll uses connections,
// keys or the agent as they are, and fails into the one-minute backoff
// rather than surfacing a credential dialog behind the user's back. The
// hint a local file change raised is consumed here and restored if the
// result never applies.
func startAutoRefreshCheck(a *app, st *autoRefreshState, fromHint bool) {
	st.checking = true
	st.dirty = false
	st.repoGen = a.session.Generation()
	repo := a.session.Repo().WithoutPrompts()
	showIgnored := a.session.loadOptions().ShowIgnored
	repoGen := st.repoGen
	loadGen := a.session.loadGen
	isLocal := a.session.Meta().Kind == store.Local

	a.spawn(func() {
		polled, err := repository.Poll(repo, showIgnored)
		a.post(func() {
			finishAutoRefreshCheck(a, st, polled, err, checkResult{
				repoGen:  repoGen,
				loadGen:  loadGen,
				isLocal:  isLocal,
				fromHint: fromHint,
			})
		})
	})
}

// checkResult is what one in-flight check remembers of the moment it
// started: which repository generation and load generation it belongs to,
// and whether a file hint or an activation triggered it, which decides the
// diff refresh after the comparison.
type checkResult struct {
	repoGen  uint64
	loadGen  uint64
	isLocal  bool
	fromHint bool
}

// finishAutoRefreshCheck applies one check's outcome on the UI thread. A
// result of a repository switched away from, or one a write started during,
// is dropped without touching the snapshot; a dropped local hint is kept
// for the next eligible check. A failure is silent — it never shows and
// never replaces an operation error — and backs interval and hint checks
// off for a minute, while an activation still checks. A success stamps its
// completion time, reloads when the state differs, and otherwise leaves the
// snapshot alone so no log read runs.
func finishAutoRefreshCheck(a *app, st *autoRefreshState, polled repository.State, err error, check checkResult) {
	st.checking = false
	if a.session.Generation() != check.repoGen || a.session.loadGen != check.loadGen {
		if check.isLocal && st.repoGen == check.repoGen {
			st.dirty = true
		}
		return
	}
	now := st.clock()
	if err != nil {
		st.failedUntil = now.Add(time.Minute)
		return
	}
	st.failedUntil = time.Time{}
	st.lastSuccess = now
	if !samePollState(a.session.Snapshot(), polled) {
		a.session.Reload()
		return
	}
	// The state reads as unchanged, but a modified file may have changed
	// content without changing its status letter: a hint or an activation
	// re-reads the diff on display so it does not stay stale. The interval
	// alone cannot tell content churn apart, so it never re-reads.
	if check.fromHint {
		a.changesView.diff.RefreshIfChanged(a.session, changesDiffScope)
	}
}

// samePollState compares a passive poll against the snapshot of the current
// repository generation. The snapshot's bisect marker is the one field the
// full load derives from a probe the poll does not run, so it is stripped
// before comparing; everything else the poll reads exactly as the load did.
func samePollState(snap repository.Snapshot, polled repository.State) bool {
	st := snap.Status
	if st.State == "bisect" {
		st.State = polled.Status.State
	}
	return reflect.DeepEqual(st, polled.Status) &&
		reflect.DeepEqual(snap.Refs, polled.Refs) &&
		reflect.DeepEqual(snap.Stashes, polled.Stashes)
}

// syncAutoRefreshWatch keeps the working-tree watch in step with the open
// repository and the automatic refresh setting: exactly the open local
// repository is watched, only while refresh is on. It runs on the UI thread,
// so a settings change that calls it takes effect at once.
func syncAutoRefreshWatch(a *app, st *autoRefreshState) {
	want := ""
	if a.session.HasRepo() && a.session.Meta().Kind == store.Local && !a.store.Settings().DisableStatusUpdate {
		want = a.session.Meta().Path
	}
	if want == st.watchPath {
		return
	}
	stopAutoRefreshWatch(st)
	// Even a failed creation keeps the path: a watch that cannot start is
	// not retried every second, as the interval and activation checks cover
	// the repository either way.
	st.watchPath = want
	if want == "" || st.watchFactory == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher, err := st.watchFactory(ctx, want)
	if err != nil {
		cancel()
		return
	}
	st.watch = watcher
	st.watchStop = cancel
	runAutoRefreshWatch(a, st, ctx, watcher)
}

// stopAutoRefreshWatch ends the active watch, if any: canceling its context
// unblocks the watch loop's Next, whose exit cleanup finds the generation
// already advanced and does nothing more.
func stopAutoRefreshWatch(st *autoRefreshState) {
	st.watchGen++
	if st.watchStop != nil {
		st.watchStop()
	}
	st.watch = nil
	st.watchStop = nil
	st.watchPath = ""
}

// runAutoRefreshWatch reads hints of one watch on its own goroutine — it is
// the only piece of the refresh that blocks — and hands every batch to the
// main thread through the state's run-on-main handoff, which only sets the
// dirty flag and never redraws. It never touches app state itself. The watch
// ending, in error, overflow or because its directory went away, is silent:
// the interval and activation checks cover the repository regardless.
func runAutoRefreshWatch(a *app, st *autoRefreshState, ctx context.Context, watcher autoRefreshWatcher) {
	st.watchGen++
	gen := st.watchGen
	// The watch loop blocks inside Next for as long as the watch lives, so
	// it must not run as a spawned worker: the app's worker accounting is
	// for bounded operations a drain can wait for, and the production spawn
	// is a plain goroutine anyway.
	go func() {
		for {
			events, err := watcher.Next(ctx)
			if err != nil {
				// Close before the handoff: the handoff waits for the main
				// thread to run it, and nothing here may outlive the watch's
				// resources because the handoff never ran.
				_ = watcher.Close()
				st.runOnMain(func() {
					if st.watchGen == gen {
						stopAutoRefreshWatch(st)
					}
				})
				return
			}
			if len(events) == 0 {
				continue
			}
			st.runOnMain(func() {
				if st.watchGen == gen && autoRefresh(a) == st {
					st.dirty = true
				}
			})
		}
	}()
}

// closeAutoRefresh ends the refresh's watch and forgets its factory, as the
// application quits. The state itself stays mounted on the app; only the
// operating system resources are released.
func closeAutoRefresh(a *app) {
	st := autoRefresh(a)
	stopAutoRefreshWatch(st)
	st.watchFactory = nil
}

// autoRefreshTickLoop is the refresh's tick goroutine body: one beat of the
// ticker hands one tick to the main thread through the state's run-on-main
// handoff, which waits for it to run and draws nothing — a tick that starts
// no check must never cost a frame, in the background least of all. The
// handoff is synchronous, so the loop runs at most one tick at a time, and
// a handoff dropped after the application quits simply lets the loop go on.
func autoRefreshTickLoop(a *app, st *autoRefreshState, ticks <-chan time.Time, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-ticks:
			st.runOnMain(func() { autoRefreshTick(a) })
		}
	}
}

// startAutoRefresh wires the production refresh into a run of the
// application: the real file watch factory and run-on-main handoff, the
// once-a-second tick, the activation and resignation callbacks, and the
// teardown. Tests build their apps through newApp instead, where none of
// this starts — no real watch, no tick goroutine — and drive the pieces
// with their own channels.
func startAutoRefresh(a *app) {
	st := autoRefresh(a)
	st.watchFactory = mygoWatchFactory
	st.runOnMain = mygo.RunOnMain
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		autoRefreshTickLoop(a, st, ticker.C, done)
	}()
	mygo.App.OnDidBecomeActive(func() { autoRefreshForeground(a, true) })
	mygo.App.OnDidResignActive(func() { autoRefreshForeground(a, false) })
	mygo.App.OnQuit(func() {
		close(done)
		closeAutoRefresh(a)
	})
}
