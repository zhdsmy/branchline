package app

import (
	"errors"
	"maps"
	"path/filepath"
	"strings"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/repository"
	"github.com/zhdsmy/branchline/internal/store"
)

const (
	defaultLocalLogLimit = 500
	defaultSSHLogLimit   = 1000
	defaultLoadMoreStep  = 500
)

// repoSession coordinates repository reads and mutations for the active
// repository without blocking the UI thread.
//
// All fields on repoSession are owned by the UI thread. Background workers
// receive only immutable parameters captured on the UI thread (the *git.Repo
// instance, repository.Options, and caller-captured inputs) and publish their
// results back through post (Window.Update in production, or a controlled UI
// queue in tests).
type repoSession struct {
	post     func(func())
	spawn    func(func())
	openRepo func(store.Repo) *git.Repo

	meta    store.Repo
	hasRepo bool
	repo    *git.Repo

	// repoGen identifies the currently open repository session. Every Open or
	// Close advances repoGen so completions from an earlier repository are
	// discarded even if they finish after the new repository loads.
	repoGen uint64
	// loadGen identifies the active snapshot read within the current repoGen.
	// Reload and write start both advance loadGen so an older read cannot
	// overwrite newer state or clear a newer request's loading flag.
	loadGen uint64
	// readSeq tracks per-scope generation numbers for auxiliary reads
	// (such as commit details or file diffs) created via sessionRead.
	readSeq map[string]uint64

	logLimit    int
	showIgnored bool
	// ignoreSpace is the diff panel's global ignore-whitespace choice. The UI
	// thread captures it into diff requests before background workers run, so
	// workers never read this field.
	ignoreSpace bool
	// saveIgnoreSpace persists the ignore-whitespace choice. It is wired once
	// when the app is created; tests without a store leave it nil.
	saveIgnoreSpace func(bool) error

	// diffChangesOnly, diffContextLines, diffContextSaved and diffWrap are
	// the diff panel's global toolbar choices: only the changed lines rather
	// than the entire file, the unchanged lines around a change, and whether
	// long lines wrap. As with ignoreSpace the UI thread captures them into
	// diff requests and states before background workers run, so workers
	// never read these fields.
	diffChangesOnly  bool
	diffContextLines int
	// diffContextSaved marks the context line count explicitly chosen, since
	// 0 is a choice; without it the count reads as the default of 3.
	diffContextSaved bool
	diffWrap         bool
	// The save callbacks persist one diff toolbar choice each. They are wired
	// once when the app is created; tests without a store leave them nil.
	saveDiffChangesOnly  func(bool) error
	saveDiffContextLines func(int) error
	saveDiffWrap         func(bool) error

	// settingsFn reads the app's persisted settings, wired when the app is
	// created. Tests building a session alone leave it nil, and Settings then
	// returns the zero value, which keeps every view option at its default.
	settingsFn func() store.Settings
	// dateOrder sorts the history by committer date rather than topology, as
	// git log --date-order does; SetDateOrder reloads when the choice changes.
	dateOrder bool

	snapshot repository.Snapshot
	loaded   bool
	loading  bool
	loadErr  error

	// Git capability state of the current repository generation. The probe
	// runs once per repository session inside the first successful snapshot
	// load; capsLoaded marks a verified answer, capsErr a detection failure
	// that a user can retry explicitly, and capsRetrying guards the explicit
	// background retry against re-entry. A capsErr is never a verified old
	// version, and neither state survives Open or Close.
	caps         git.GitCapabilities
	capsLoaded   bool
	capsErr      error
	capsRetrying bool

	busy      bool
	busyLabel string
	actionErr error
}

func newRepoSession(post, spawn func(func()), openRepo func(store.Repo) *git.Repo) *repoSession {
	if post == nil {
		post = func(fn func()) {}
	}
	if spawn == nil {
		spawn = func(fn func()) { go fn() }
	}
	if openRepo == nil {
		openRepo = defaultOpenGitRepo
	}
	return &repoSession{
		post:     post,
		spawn:    spawn,
		openRepo: openRepo,
		readSeq:  map[string]uint64{},
	}
}

// defaultOpenGitRepo builds a *git.Repo for either a local checkout or an SSH
// server repository using the existing store and git executor constructors.
func defaultOpenGitRepo(meta store.Repo) *git.Repo {
	return git.New(meta.Executor())
}

// resolveRepoTarget parses a startup target from BRANCHLINE_OPEN, accepting
// either an SSH address (user@host:/path or ssh://...) or a local directory.
func resolveRepoTarget(raw string) (store.Repo, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return store.Repo{}, errors.New("empty repository path")
	}
	if store.IsAddress(raw) {
		r, err := store.ParseAddress(raw)
		if err != nil {
			return store.Repo{}, err
		}
		if r.Name == "" {
			r.Name = store.DefaultName(r.Path)
		}
		return r, nil
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return store.Repo{}, err
	}
	abs = filepath.Clean(abs)
	return store.Repo{
		Kind: store.Local,
		Path: abs,
		Name: store.DefaultName(abs),
	}, nil
}

// Open switches the session to meta, invalidates any in-flight reads or writes
// from the previous repository, and begins loading a fresh snapshot.
func (s *repoSession) Open(meta store.Repo) {
	s.repoGen++
	s.loadGen++
	clear(s.readSeq)

	s.meta = meta
	s.hasRepo = true
	s.repo = s.openRepo(meta)

	s.snapshot = repository.Snapshot{}
	s.loaded = false
	s.loading = false
	s.loadErr = nil
	s.busy = false
	s.busyLabel = ""
	s.actionErr = nil

	s.caps = git.GitCapabilities{}
	s.capsLoaded = false
	s.capsErr = nil
	s.capsRetrying = false

	if meta.Kind == store.SSH {
		s.logLimit = defaultSSHLogLimit
	} else {
		s.logLimit = defaultLocalLogLimit
	}
	s.Reload()
}

// Close detaches the active repository and invalidates any in-flight work.
func (s *repoSession) Close() {
	s.repoGen++
	s.loadGen++
	clear(s.readSeq)

	s.meta = store.Repo{}
	s.hasRepo = false
	s.repo = nil
	s.snapshot = repository.Snapshot{}
	s.loaded = false
	s.loading = false
	s.loadErr = nil
	s.busy = false
	s.busyLabel = ""
	s.actionErr = nil

	s.caps = git.GitCapabilities{}
	s.capsLoaded = false
	s.capsErr = nil
	s.capsRetrying = false
}

// Reload starts an asynchronous repository.Load using options captured on the
// UI thread. While a mutating write is in progress (s.busy), Reload is a no-op
// because doThen unconditionally schedules a fresh Reload upon completion.
// Stale completions are ignored without clearing newer state.
func (s *repoSession) Reload() {
	if !s.hasRepo || s.repo == nil || s.busy {
		return
	}
	repo := s.repo
	repoGen := s.repoGen
	s.loadGen++
	loadGen := s.loadGen
	opts := s.loadOptions()

	s.loading = true
	s.loadErr = nil

	s.spawn(func() {
		snap, err := repository.Load(repo, opts)
		s.post(func() {
			if s.repoGen != repoGen || s.loadGen != loadGen {
				return
			}
			s.loading = false
			if err != nil {
				s.loadErr = err
				return
			}
			s.snapshot = snap
			s.loaded = true
			s.loadErr = nil
			if !opts.SkipCapabilities {
				// The first successful load of this repository carries the
				// one probe result; every later refresh skips it. A probing
				// load without a probe (a reader that cannot answer) leaves
				// the state untouched, so a plain session reload never
				// erases a verified answer either.
				if snap.HasCapabilities {
					s.caps = snap.Capabilities
					s.capsLoaded = true
					s.capsErr = nil
				} else if snap.CapabilitiesErr != nil {
					s.caps = git.GitCapabilities{}
					s.capsLoaded = false
					s.capsErr = snap.CapabilitiesErr
				}
			}
		})
	})
}

// LoadMore grows the history log limit and loads the next page of commits.
// With a settled snapshot in place — no write busy, no read in flight — only
// the new page is fetched and appended, instead of reloading the whole
// snapshot. While a write operation is busy or a read is already running,
// the updated logLimit is recorded and the read is deferred to a full
// reload, exactly as before.
func (s *repoSession) LoadMore(step int) {
	if !s.hasRepo || s.repo == nil {
		return
	}
	if step <= 0 {
		step = defaultLoadMoreStep
	}
	if s.busy || s.loading || !s.loaded || len(s.snapshot.Commits) == 0 {
		s.logLimit += step
		s.Reload()
		return
	}
	s.logLimit += step
	s.loadMorePage(step)
}

// loadMorePage fetches only the next page of the history off the UI thread
// and appends it to the loaded commits, keeping the rest of the snapshot
// untouched. Appending is only sound while the loaded commits still read as
// they did when they were captured, so two checks verify that. The reference
// tips are re-read and compared with the captured ones: a branch rewritten
// anywhere inside the loaded prefix — an amend or rebase of commits the
// pages already hold — moves its tip without touching the seam, and the
// comparison is the only check that sees it. The fetch also starts one
// commit before the loaded tail, and the overlap verifies the walk itself:
// a detached HEAD moving without a ref, or any shift of the seam region,
// breaks it. A failed check or a failed read falls back to a full reload.
// A reload, write or repository switch in the meantime invalidates the
// completion through the generation pair, and it is discarded.
func (s *repoSession) loadMorePage(step int) {
	repo := s.repo
	repoGen := s.repoGen
	s.loadGen++
	loadGen := s.loadGen
	// Everything the worker needs is captured on the UI thread. The base
	// slice is never mutated in place anywhere, so the worker can read it
	// while the UI thread goes on; the merged commits are a fresh slice.
	base := s.snapshot.Commits
	n := len(base)
	lastHash := base[n-1].Hash
	logOpts := s.loadOptions().Log
	logOpts.Limit = step + 1
	logOpts.Skip = n - 1
	refs := s.snapshot.Refs
	wantTips := refTips(refs)

	s.loading = true
	s.loadErr = nil

	s.spawn(func() {
		page, logErr := repo.Log(logOpts)
		// Refs are re-read after the walk: a tip moving between the two
		// reads is caught, and a tip moving after them lands in the window
		// no snapshot read could close either.
		freshRefs, refsErr := repo.Refs()
		tipsIntact := refsErr == nil && maps.Equal(refTips(freshRefs), wantTips)
		// The history is unchanged when the tips stand where the capture
		// left them and the walk still reaches the commit the capture ended
		// on; an empty or shifted walk cannot be joined either.
		continuous := logErr == nil && len(page) > 0 && page[0].Hash == lastHash && tipsIntact
		var merged []git.Commit
		var graph []git.GraphRow
		more := false
		if continuous {
			git.FixRefKinds(page, freshRefs)
			merged = make([]git.Commit, 0, n+len(page)-1)
			merged = append(merged, base...)
			merged = append(merged, page[1:]...)
			graph = git.Graph(merged)
			// A full page means the walk was cut off by the limit, so older
			// commits remain; a short page reached the end of the history.
			more = len(page) == step+1
		}
		s.post(func() {
			if s.repoGen != repoGen || s.loadGen != loadGen {
				return
			}
			if !continuous {
				// The history moved underneath the pagination, or the page
				// could not be read at all: only a complete reload yields a
				// consistent snapshot. The log limit was already grown.
				s.loading = false
				s.Reload()
				return
			}
			s.loading = false
			s.loadErr = nil
			s.snapshot.Commits = merged
			s.snapshot.Graph = graph
			s.snapshot.More = more
		})
	})
}

// refTips maps every branch, remote branch and tag of refs to the commit it
// points at. The tips are what a mutation of the history always moves —
// even one rewriting commits already loaded — while the rest of the refs
// record (upstream tracking, checkout state) does not affect the walk.
func refTips(refs git.Refs) map[string]string {
	tips := make(map[string]string, len(refs.Branches)+len(refs.Remote)+len(refs.Tags))
	for _, b := range refs.Branches {
		tips[b.FullName] = b.Hash
	}
	for _, b := range refs.Remote {
		tips[b.FullName] = b.Hash
	}
	for _, t := range refs.Tags {
		tips["refs/tags/"+t.Name] = t.Hash
	}
	return tips
}

// do executes a mutating Git operation off the UI thread. It sets the busy
// state, rejects concurrent re-entry while busy, reloads the snapshot after
// completion, and retains any write error separately from snapshot load errors.
//
// Callers must capture all mutable UI inputs on the UI thread before calling
// do; fn must use only its *git.Repo argument and captured values.
func (s *repoSession) do(label string, fn func(*git.Repo) error) bool {
	return s.doThen(label, fn, nil)
}

// doThen behaves like do and additionally invokes after on the UI thread once
// the write completes (only if the session is still on the same repository).
// Reload is scheduled before after runs; after must not assume the reload has
// already finished.
func (s *repoSession) doThen(label string, fn func(*git.Repo) error, after func(error)) bool {
	if !s.hasRepo || s.repo == nil || fn == nil || s.busy {
		return false
	}
	repo := s.repo
	repoGen := s.repoGen
	// Advance loadGen so an in-flight read started before this write cannot
	// publish pre-mutation state while or after the write runs.
	s.loadGen++
	s.loading = false
	s.busy = true
	s.busyLabel = label
	s.actionErr = nil

	s.spawn(func() {
		err := fn(repo)
		s.post(func() {
			if s.repoGen != repoGen {
				return
			}
			s.busy = false
			s.busyLabel = ""
			s.actionErr = err
			s.Reload()
			if after != nil {
				after(err)
			}
		})
	})
	return true
}

// sessionRead runs a scoped asynchronous read (for example commit detail or
// file diff) off the UI thread and invokes done on the UI thread only if both
// the repository generation and the scope's request sequence still match.
func sessionRead[T any](s *repoSession, scope string, work func(*git.Repo) (T, error), done func(T, error)) bool {
	if s == nil || !s.hasRepo || s.repo == nil || work == nil || done == nil {
		return false
	}
	repo := s.repo
	repoGen := s.repoGen
	seq := s.readSeq[scope] + 1
	s.readSeq[scope] = seq

	s.spawn(func() {
		res, err := work(repo)
		s.post(func() {
			if s.repoGen != repoGen || s.readSeq[scope] != seq {
				return
			}
			done(res, err)
		})
	})
	return true
}

// cancelRead invalidates any in-flight sessionRead for scope so late results
// are discarded when a caller clears its selection.
func (s *repoSession) cancelRead(scope string) {
	if s == nil {
		return
	}
	s.readSeq[scope]++
}

func (s *repoSession) loadOptions() repository.Options {
	limit := s.logLimit
	if limit <= 0 {
		limit = defaultLocalLogLimit
	}
	return repository.Options{
		ShowIgnored: s.showIgnored,
		Log: git.LogOptions{
			Limit:     limit,
			DateOrder: s.dateOrder,
		},
		// Once this repository generation has a verified answer or a
		// detection failure, ordinary reloads, pagination, and post-write
		// refreshes never run git --version again; only an explicit retry
		// re-probes after a failure.
		SkipCapabilities: s.capsLoaded || s.capsErr != nil,
	}
}

// Settings returns the app's persisted settings, or the zero value in tests
// that build a session alone: every view option then reads as its default.
func (s *repoSession) Settings() store.Settings {
	if s.settingsFn == nil {
		return store.Settings{}
	}
	return s.settingsFn()
}

// DateOrder reports whether the history sorts by committer date rather than
// topology.
func (s *repoSession) DateOrder() bool { return s.dateOrder }

// SetDateOrder records the history ordering and reloads the snapshot when it
// changes, so the log is walked again with the new order.
func (s *repoSession) SetDateOrder(on bool) {
	if s.dateOrder == on {
		return
	}
	s.dateOrder = on
	s.Reload()
}

// RebuildExecutor replaces the executor of the active repository with a fresh
// one, so changes to how git itself is run — its program, its PATH, its
// traces — take effect without reopening the repository. The metadata and the
// log limit are kept. While no write is in progress the snapshot reloads at
// once; a write in progress is not interrupted, and its automatic reload
// already runs on the new executor.
func (s *repoSession) RebuildExecutor() {
	if !s.hasRepo {
		return
	}
	s.repo = s.openRepo(s.meta)
	if !s.busy {
		s.Reload()
	}
}

// IgnoreSpace reports whether file diffs ignore whitespace changes.
func (s *repoSession) IgnoreSpace() bool { return s.ignoreSpace }

// SetIgnoreSpace records the ignore-whitespace choice. The choice only takes
// effect after it is persisted: when the save callback fails, the in-memory
// value is left unchanged and the error is returned, so a view can keep its
// previous rendering instead of reloading.
func (s *repoSession) SetIgnoreSpace(v bool) error {
	if s.saveIgnoreSpace != nil {
		if err := s.saveIgnoreSpace(v); err != nil {
			return err
		}
	}
	s.ignoreSpace = v
	return nil
}

// saveIgnoreSpacePref writes the ignore-whitespace choice to the store,
// leaving every other setting untouched. A failure is surfaced as a toast:
// the toggle is a global preference rather than diff content, so the panel's
// own error area — reserved for diff load failures — is not the place for it.
func saveIgnoreSpacePref(a *app, v bool) error {
	if a.store == nil {
		return nil
	}
	settings := a.store.Settings()
	settings.IgnoreSpace = v
	if err := a.store.SetSettings(settings); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// DiffChangesOnly reports whether file diffs show only the changed lines and
// their context rather than the entire file.
func (s *repoSession) DiffChangesOnly() bool { return s.diffChangesOnly }

// DiffEntireFile reports whether file diffs show the entire file; it is the
// inverse of DiffChangesOnly, phrased for the toolbar's on/off button.
func (s *repoSession) DiffEntireFile() bool { return !s.diffChangesOnly }

// SetDiffChangesOnly records the changes-only choice. As with the other diff
// toolbar choices it takes effect only once persisted: a failed save leaves
// the in-memory value unchanged and returns the error, so a view keeps its
// previous rendering instead of reloading.
func (s *repoSession) SetDiffChangesOnly(v bool) error {
	if s.saveDiffChangesOnly != nil {
		if err := s.saveDiffChangesOnly(v); err != nil {
			return err
		}
	}
	s.diffChangesOnly = v
	return nil
}

// DiffContextLines is the chosen number of unchanged lines around a change,
// as git diff -U: 3 until one is chosen, then the choice, 0 included.
func (s *repoSession) DiffContextLines() int {
	if !s.diffContextSaved {
		return defaultDiffContextLines
	}
	return s.diffContextLines
}

// SetDiffContextLines records the context line count; negative counts read
// as none, as git diff -U0. The choice takes effect only once persisted, as
// SetDiffChangesOnly's does.
func (s *repoSession) SetDiffContextLines(v int) error {
	if v < 0 {
		v = 0
	}
	if s.saveDiffContextLines != nil {
		if err := s.saveDiffContextLines(v); err != nil {
			return err
		}
	}
	s.diffContextLines = v
	s.diffContextSaved = true
	return nil
}

// DiffWrap reports whether long diff lines wrap rather than scroll sideways.
func (s *repoSession) DiffWrap() bool { return s.diffWrap }

// SetDiffWrap records the line-wrapping choice. It takes effect only once
// persisted, as SetDiffChangesOnly's does.
func (s *repoSession) SetDiffWrap(v bool) error {
	if s.saveDiffWrap != nil {
		if err := s.saveDiffWrap(v); err != nil {
			return err
		}
	}
	s.diffWrap = v
	return nil
}

// saveDiffChangesOnlyPref writes the changes-only choice to the store,
// leaving every other setting untouched; a failure is surfaced as a toast,
// as the ignore-whitespace choice's is.
func saveDiffChangesOnlyPref(a *app, v bool) error {
	if a.store == nil {
		return nil
	}
	settings := a.store.Settings()
	settings.DiffChangesOnly = v
	if err := a.store.SetSettings(settings); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// saveDiffContextLinesPref writes the context line count to the store with
// its explicit-choice marker, leaving every other setting untouched; a
// failure is surfaced as a toast, as the ignore-whitespace choice's is.
func saveDiffContextLinesPref(a *app, v int) error {
	if a.store == nil {
		return nil
	}
	settings := a.store.Settings()
	settings.DiffContextLines = v
	settings.DiffContextSaved = true
	if err := a.store.SetSettings(settings); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

// saveDiffWrapPref writes the line-wrapping choice to the store, leaving
// every other setting untouched; a failure is surfaced as a toast, as the
// ignore-whitespace choice's is.
func saveDiffWrapPref(a *app, v bool) error {
	if a.store == nil {
		return nil
	}
	settings := a.store.Settings()
	settings.DiffWrap = v
	if err := a.store.SetSettings(settings); err != nil {
		a.toast = "Cannot save settings: " + err.Error()
		return err
	}
	return nil
}

func (s *repoSession) HasRepo() bool                 { return s.hasRepo }
func (s *repoSession) Meta() store.Repo              { return s.meta }
func (s *repoSession) Repo() *git.Repo               { return s.repo }
func (s *repoSession) Snapshot() repository.Snapshot { return s.snapshot }
func (s *repoSession) Loaded() bool                  { return s.loaded }
func (s *repoSession) Loading() bool                 { return s.loading }
func (s *repoSession) LoadErr() error                { return s.loadErr }
func (s *repoSession) Busy() bool                    { return s.busy }
func (s *repoSession) BusyLabel() string             { return s.busyLabel }
func (s *repoSession) ActionErr() error              { return s.actionErr }
func (s *repoSession) ClearActionErr()               { s.actionErr = nil }
func (s *repoSession) Generation() uint64            { return s.repoGen }

// RetryGitCapabilities re-probes the current repository's Git version in the
// background after a detection failure. It returns false when there is no
// repository, no recorded detection failure, or a retry already in flight.
// The probe runs through sessionRead, so a completion arriving after the
// repository switched or closed is discarded with the other stale reads and
// never touches the new repository's capability state.
func (s *repoSession) RetryGitCapabilities() bool {
	if s == nil || !s.hasRepo || s.repo == nil || s.capsLoaded || s.capsErr == nil || s.capsRetrying {
		return false
	}
	s.capsRetrying = true
	if !sessionRead(s, "git-capabilities", func(repo *git.Repo) (git.GitCapabilities, error) {
		return repo.Capabilities()
	}, func(caps git.GitCapabilities, err error) {
		s.capsRetrying = false
		if err != nil {
			s.caps = git.GitCapabilities{}
			s.capsLoaded = false
			s.capsErr = err
			return
		}
		s.caps = caps
		s.capsLoaded = true
		s.capsErr = nil
	}) {
		s.capsRetrying = false
		return false
	}
	return true
}

// GitCapabilities returns the verified capability answer of the current
// repository generation. The bool is false while the first probe is still in
// flight and after a detection failure: an unverified state must never read
// as an answer.
func (s *repoSession) GitCapabilities() (git.GitCapabilities, bool) {
	if s.capsLoaded && s.capsErr == nil {
		return s.caps, true
	}
	return git.GitCapabilities{}, false
}

// GitCapabilitiesErr returns the detection failure of the current repository
// generation, or nil when the probe succeeded or has not finished yet.
func (s *repoSession) GitCapabilitiesErr() error { return s.capsErr }

// GitCapabilitiesRetrying reports whether an explicit probe retry is in flight.
func (s *repoSession) GitCapabilitiesRetrying() bool { return s.capsRetrying }

// SupportsRebaseUpdateRefs reports whether the repository's verified Git
// supports rebase --update-refs. Unverified or failed detection is never
// support: the answer is false until the probe succeeds.
func (s *repoSession) SupportsRebaseUpdateRefs() bool {
	caps, ok := s.GitCapabilities()
	return ok && caps.RebaseUpdateRefs
}

// SupportsStashStaged reports whether the repository's verified Git supports
// stash --staged. Unverified or failed detection is never support.
func (s *repoSession) SupportsStashStaged() bool {
	caps, ok := s.GitCapabilities()
	return ok && caps.StashStaged
}
