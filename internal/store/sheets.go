package store

import "maps"

// The check boxes of the git sheets that keep their state from one use
// to the next, keyed by sheet and option.
const (
	FetchAllRemotes             = "fetch.all_remotes"
	FetchAllTags                = "fetch.all_tags"
	FetchPrune                  = "fetch.prune"
	PullStashAndReapply         = "pull.autostash"
	PullRebase                  = "pull.rebase"
	PushAllTags                 = "push.all_tags"
	CreateBranchCheckout        = "create_branch.checkout"
	RebaseAutostash             = "rebase.autostash"
	RebaseUpdateRefs            = "rebase.update_refs"
	StashStageNewFiles          = "stash.include_untracked"
	CherryPickAppendSha         = "cherry_pick.append_origin"
	CreateTagPush               = "create_tag.push"
	CheckoutSyncAutostash       = "checkout_sync.autostash"
	InteractiveRebaseUpdateRefs = "interactive_rebase.update_refs"
	FlowFinishFeatureDelete     = "flow.finish_feature.delete_branch"
	FlowFinishFeatureRebase     = "flow.finish_feature.rebase"
	FlowFinishFeatureNoFF       = "flow.finish_feature.no_ff"
)

// Sheet returns the remembered state of a sheet's check box, or def
// when it was never set.
func (s *Store) Sheet(key string, def bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.data.Sheets[key]; ok {
		return v
	}
	return def
}

// SetSheet remembers the state of a sheet's check box.
func (s *Store) SetSheet(key string, on bool) error {
	return s.SetSheets(map[string]bool{key: on})
}

// SetSheets saves a submitted sheet's choices together in one write.
func (s *Store) SetSheets(values map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for key, on := range values {
		if v, ok := s.data.Sheets[key]; !ok || v != on {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	previous := s.data.Sheets
	s.data.Sheets = maps.Clone(previous)
	if s.data.Sheets == nil {
		s.data.Sheets = map[string]bool{}
	}
	for key, on := range values {
		s.data.Sheets[key] = on
	}
	if err := s.save(); err != nil {
		s.data.Sheets = previous
		return err
	}
	return nil
}
