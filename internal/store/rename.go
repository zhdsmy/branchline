package store

import (
	"fmt"
	"strings"
)

// Rename changes the display name without changing the repository's path,
// settings, workspace or last-opened date.
func (s *Store) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("the repository name cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Repos {
		if s.data.Repos[i].ID == id {
			old := s.data.Repos[i].Name
			s.data.Repos[i].Name = name
			if err := s.save(); err != nil {
				s.data.Repos[i].Name = old
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("unknown repository %s", id)
}
