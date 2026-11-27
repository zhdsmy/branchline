package store

import (
	"path/filepath"
	"slices"
)

// Welcomed reports whether the welcome window has been through.
func (s *Store) Welcomed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Welcomed
}

// SetWelcomed remembers that the welcome window has been through.
func (s *Store) SetWelcomed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.Welcomed
	s.data.Welcomed = true
	if err := s.save(); err != nil {
		s.data.Welcomed = previous
		return err
	}
	return nil
}

// RemoveDeleted forgets a repository, as Remove does, and remembers its
// path so a rescan of the source folders does not bring it back.
func (s *Store) RemoveDeleted(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(id, true)
}

// Deleted returns the local paths removed from the list.
func (s *Store) Deleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.data.Deleted...)
}

// ResetDeleted forgets the paths removed, so a rescan finds them again.
func (s *Store) ResetDeleted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]string(nil), s.data.Deleted...)
	s.data.Deleted = nil
	if err := s.save(); err != nil {
		s.data.Deleted = previous
		return err
	}
	return nil
}

// AddMany adds the local repositories at paths not in the list yet, and
// returns how many it added. They are not marked opened.
func (s *Store) AddMany(paths []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previousRepos := append([]Repo(nil), s.data.Repos...)
	previousDeleted := append([]string(nil), s.data.Deleted...)
	added := 0
	for _, p := range paths {
		r := Repo{Kind: Local, Path: filepath.Clean(p), Name: DefaultName(p)}
		known := false
		for _, o := range s.data.Repos {
			if o.Same(r) {
				known = true
				break
			}
		}
		if known {
			continue
		}
		r.ID = newID()
		s.data.Repos = append(s.data.Repos, r)
		s.data.Deleted = slices.DeleteFunc(s.data.Deleted, func(d string) bool { return d == r.Path })
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := s.save(); err != nil {
		s.data.Repos = previousRepos
		s.data.Deleted = previousDeleted
		return 0, err
	}
	return added, nil
}
