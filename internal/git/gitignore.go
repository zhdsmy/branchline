package git

import (
	_ "embed"
	"strings"
)

// GitignoreTemplate is a named list of patterns for one language, tool or
// system, such as Go or macOS.
type GitignoreTemplate struct {
	Name     string
	Patterns string
}

//go:embed gitignore_templates.txt
var gitignoreTemplates string

// GitignoreTemplates are the built-in templates Add .gitignore Template
// offers, grouped languages first, then tools, platforms and editors.
func GitignoreTemplates() []GitignoreTemplate {
	var list []GitignoreTemplate
	for _, line := range strings.Split(gitignoreTemplates, "\n") {
		if name, ok := strings.CutPrefix(line, "### "); ok {
			list = append(list, GitignoreTemplate{Name: strings.TrimSpace(name)})
			continue
		}
		if len(list) > 0 {
			list[len(list)-1].Patterns += line + "\n"
		}
	}
	for i := range list {
		list[i].Patterns = strings.TrimSpace(list[i].Patterns) + "\n"
	}
	return list
}

// IgnorePatterns turns the text typed in the sheet, one pattern per line,
// into the lines to append: blank lines are dropped, comments stay.
func IgnorePatterns(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// AddToGitignore appends text to .gitignore, stages the file, and leaves
// out the lines that are already in it. It says how many lines it added.
// Files that git already tracks stay tracked; only the file list changes.
func (r *Repo) AddToGitignore(text string) (int, error) {
	current, _ := r.ReadWorktree(".gitignore")
	have := map[string]bool{}
	for _, l := range strings.Split(current, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, l := range IgnorePatterns(text) {
		if strings.HasPrefix(l, "#") || !have[l] {
			add = append(add, l)
			have[l] = true
		}
	}
	patterns := 0
	for _, l := range add {
		if !strings.HasPrefix(l, "#") {
			patterns++
		}
	}
	if patterns == 0 {
		return 0, nil
	}
	if err := r.AppendWorktree(".gitignore", strings.Join(add, "\n")); err != nil {
		return 0, err
	}
	return patterns, r.Stage(".gitignore")
}
