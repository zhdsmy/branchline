package store

import (
	"errors"
	"sort"
	"strings"
)

// WorkspaceName trims a workspace name and the / around its folders.
func WorkspaceName(name string) string { return cleanWorkspace(name) }

// cleanWorkspace is shared by persistence and workspace editing.
func cleanWorkspace(name string) string {
	parts := strings.Split(name, "/")
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "/")
}

// Workspaces are the names of the workspaces: those made in Configure
// Workspaces and those some repository is in, sorted. A / in a name nests
// the workspace in a folder.
func (s *Store) Workspaces() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaces()
}

func (s *Store) workspaces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name = cleanWorkspace(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, w := range s.data.Workspaces {
		add(w)
	}
	for _, r := range s.data.Repos {
		add(r.Workspace)
	}
	sort.Strings(out)
	return out
}

// AddWorkspace makes an empty workspace.
func (s *Store) AddWorkspace(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = cleanWorkspace(name)
	if name == "" {
		return errors.New("the workspace needs a name")
	}
	for _, w := range s.workspaces() {
		if w == name {
			return errors.New("there is a workspace named " + name)
		}
	}
	previous := append([]string(nil), s.data.Workspaces...)
	s.data.Workspaces = append(s.data.Workspaces, name)
	if err := s.save(); err != nil {
		s.data.Workspaces = previous
		return err
	}
	return nil
}

// inside reports whether name is the workspace folder or in it.
func inside(name, folder string) bool {
	return name == folder || strings.HasPrefix(name, folder+"/")
}

// RenameWorkspace renames a workspace and the workspaces nested in it,
// with the repositories that are in them.
func (s *Store) RenameWorkspace(old, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, name = cleanWorkspace(old), cleanWorkspace(name)
	if name == "" {
		return errors.New("the workspace needs a name")
	}
	if old == name {
		return nil
	}
	move := func(w string) string {
		if c := cleanWorkspace(w); inside(c, old) {
			return name + strings.TrimPrefix(c, old)
		}
		return w
	}
	for _, w := range s.workspaces() {
		if w == name || inside(w, name) && !inside(w, old) {
			return errors.New("there is a workspace named " + name)
		}
	}
	previousWorkspaces := append([]string(nil), s.data.Workspaces...)
	previousRepos := make([]Repo, len(s.data.Repos))
	for i, r := range s.data.Repos {
		previousRepos[i] = cloneRepo(r)
	}
	var list []string
	for _, w := range s.workspaces() {
		list = append(list, move(w))
	}
	s.data.Workspaces = list
	for i := range s.data.Repos {
		s.data.Repos[i].Workspace = move(s.data.Repos[i].Workspace)
	}
	if err := s.save(); err != nil {
		s.data.Workspaces = previousWorkspaces
		s.data.Repos = previousRepos
		return err
	}
	return nil
}

// DeleteWorkspace removes a workspace, with those nested in it. Its
// repositories stay, ungrouped.
func (s *Store) DeleteWorkspace(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = cleanWorkspace(name)
	previousWorkspaces := append([]string(nil), s.data.Workspaces...)
	previousRepos := make([]Repo, len(s.data.Repos))
	for i, r := range s.data.Repos {
		previousRepos[i] = cloneRepo(r)
	}
	var list []string
	for _, w := range s.workspaces() {
		if !inside(w, name) {
			list = append(list, w)
		}
	}
	s.data.Workspaces = list
	for i := range s.data.Repos {
		if inside(cleanWorkspace(s.data.Repos[i].Workspace), name) {
			s.data.Repos[i].Workspace = ""
		}
	}
	if err := s.save(); err != nil {
		s.data.Workspaces = previousWorkspaces
		s.data.Repos = previousRepos
		return err
	}
	return nil
}
