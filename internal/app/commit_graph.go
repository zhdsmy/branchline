package app

import (
	"github.com/zhdsmy/branchline/internal/store"
)

// commitGraphMinCommits is how many reachable commits a local repository
// needs before a commit-graph is written for it. Below it the one-time cost
// of counting and writing buys nothing the log's first page can feel. It is
// a variable so tests can lower it and build small fixtures; production
// never changes it.
var commitGraphMinCommits = 10000

// considerCommitGraph starts the one background commit-graph write a local
// repository gets per run of the app. It runs on the UI thread when a
// snapshot load succeeded; more is the snapshot's word for a history longer
// than one page, the cheapest signal that counting commits could be worth
// it. SSH repositories are never touched: their log reads walk the same
// objects, but writing files on a server the user only reads from is not
// this feature's call to make.
//
// The write itself — counting the commits first — runs off the UI thread.
// Its result and any error go nowhere: a failure is a missed optimization,
// not something the user can or should act on, so neither busy nor the
// snapshot nor any error surface changes for it, and nothing reloads.
func (s *repoSession) considerCommitGraph(more bool) {
	if !more || s.repo == nil || s.meta.Kind != store.Local {
		return
	}
	if s.commitGraphTried[s.meta.Path] {
		return
	}
	s.commitGraphTried[s.meta.Path] = true
	// The repository instance is captured now, on the UI thread: it carries
	// the git binary and PATH the settings chose, and stays valid even if
	// the session moves on to another repository before the worker finishes.
	repo := s.repo
	s.spawn(func() {
		_, _ = repo.WriteCommitGraphIfLarge(commitGraphMinCommits)
	})
}
