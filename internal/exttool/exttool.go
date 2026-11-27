// Package exttool runs the external diff and merge tools chosen in Settings.
// The versions to compare or merge are written into a private snapshot
// folder on this Mac first, so the same code path works for local
// repositories and for repositories reached over SSH, where the tool cannot
// read the files itself. The tool runs as one shell command on this Mac and
// the file paths reach it only through the LOCAL, REMOTE, BASE and MERGED
// environment variables, so a file name from the repository is never
// interpreted by the shell.
package exttool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhdsmy/branchline/internal/git"
)

// Custom is the name of the user-configured tool in Settings. The other
// names are the presets Names returns, in the order of the pop-up menu.
const Custom = "Custom"

// Names returns the tool names in the order the Settings pop-ups list them.
func Names() []string {
	names := make([]string, 0, len(presets)+1)
	for _, p := range presets {
		names = append(names, p.name)
	}
	return append(names, Custom)
}

// Tool is one resolved external tool: its name in Settings and the shell
// command that runs it. Command is handed to /bin/sh -c; the file paths
// reach it only through the environment, never inside the command text.
type Tool struct {
	Name    string
	Command string
}

// Finder looks for a tool's program: candidates are absolute paths tried in
// order, program is what to look up in PATH when no candidate works. It
// returns the program's path, or "" when the program cannot be found.
type Finder func(candidates []string, program string) string

// FindProgram is the default Finder: the first candidate that is an
// executable regular file, or the program found in PATH. Applications
// opened from the Finder see a short PATH without the Homebrew directories,
// so the candidates come first.
func FindProgram(candidates []string, program string) string {
	for _, candidate := range candidates {
		if executable(candidate) {
			return candidate
		}
	}
	if path, err := exec.LookPath(program); err == nil {
		return path
	}
	return ""
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// preset is one tool in the table: where its program is looked for and how
// its diff and merge commands are built. Each command is the shape of the
// matching script in git's own mergetool helpers, including the parts that
// keep the process alive until the user closes the window: FileMerge's
// "| cat" and Visual Studio Code's "--wait".
type preset struct {
	name       string
	candidates []string
	program    string
	diff       func(p string) string
	merge      func(p string) string
}

var presets = []preset{
	{
		name:       "FileMerge",
		candidates: []string{"/usr/bin/opendiff"},
		program:    "opendiff",
		diff:       func(p string) string { return p + ` "$LOCAL" "$REMOTE" | cat` },
		merge:      func(p string) string { return p + ` "$LOCAL" "$REMOTE" -ancestor "$BASE" -merge "$MERGED" | cat` },
	},
	{
		name: "Kaleidoscope",
		candidates: []string{
			"/usr/local/bin/ksdiff",
			"/opt/homebrew/bin/ksdiff",
			"/Applications/Kaleidoscope.app/Contents/MacOS/ksdiff",
		},
		program: "ksdiff",
		diff: func(p string) string {
			return p + ` --partial-changeset --relative-path "$MERGED" -- "$LOCAL" "$REMOTE"`
		},
		merge: func(p string) string {
			return p + ` --merge --output "$MERGED" --base "$BASE" -- "$LOCAL" --snapshot "$REMOTE" --snapshot`
		},
	},
	{
		name: "Beyond Compare",
		candidates: []string{
			"/usr/local/bin/bcomp",
			"/opt/homebrew/bin/bcomp",
			"/Applications/Beyond Compare.app/Contents/MacOS/bcomp",
		},
		program: "bcomp",
		diff:    func(p string) string { return p + ` "$LOCAL" "$REMOTE"` },
		merge:   func(p string) string { return p + ` "$LOCAL" "$REMOTE" "$BASE" -mergeoutput="$MERGED"` },
	},
	{
		name: "P4Merge",
		candidates: []string{
			"/Applications/p4merge.app/Contents/MacOS/p4merge",
			"/opt/homebrew/bin/p4merge",
			"/usr/local/bin/p4merge",
		},
		program: "p4merge",
		diff:    func(p string) string { return p + ` "$LOCAL" "$REMOTE"` },
		merge:   func(p string) string { return p + ` "$BASE" "$REMOTE" "$LOCAL" "$MERGED"` },
	},
	{
		name: "Visual Studio Code",
		candidates: []string{
			"/usr/local/bin/code",
			"/opt/homebrew/bin/code",
			"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code",
		},
		program: "code",
		diff:    func(p string) string { return p + ` --wait --diff "$LOCAL" "$REMOTE"` },
		merge:   func(p string) string { return p + ` --wait --merge "$REMOTE" "$LOCAL" "$BASE" "$MERGED"` },
	},
}

// ResolveDiff builds the diff command of the tool named in Settings.
// customPath and customArgs are the program and arguments of the Custom
// tool; they are only read when name is Custom. A nil find uses FindProgram.
func ResolveDiff(name, customPath, customArgs string, find Finder) (Tool, error) {
	return resolve(name, customPath, customArgs, find, false)
}

// ResolveMerge builds the merge command of the tool named in Settings, like
// ResolveDiff.
func ResolveMerge(name, customPath, customArgs string, find Finder) (Tool, error) {
	return resolve(name, customPath, customArgs, find, true)
}

func resolve(name, customPath, customArgs string, find Finder, merge bool) (Tool, error) {
	if find == nil {
		find = FindProgram
	}
	if name == Custom {
		return resolveCustom(customPath, customArgs, merge)
	}
	for _, p := range presets {
		if p.name != name {
			continue
		}
		program := find(p.candidates, p.program)
		if program == "" {
			return Tool{}, fmt.Errorf("%s not found: checked %s, then %s in PATH", name, strings.Join(p.candidates, ", "), p.program)
		}
		quoted := git.ShellQuote(program)
		if merge {
			return Tool{Name: name, Command: p.merge(quoted)}, nil
		}
		return Tool{Name: name, Command: p.diff(quoted)}, nil
	}
	return Tool{}, fmt.Errorf("unknown tool %q", name)
}

func resolveCustom(customPath, customArgs string, merge bool) (Tool, error) {
	if !filepath.IsAbs(customPath) {
		return Tool{}, fmt.Errorf("custom tool %q is not an absolute path", customPath)
	}
	info, err := os.Stat(customPath)
	switch {
	case err != nil:
		return Tool{}, fmt.Errorf("custom tool %q: %w", customPath, err)
	case !info.Mode().IsRegular():
		return Tool{}, fmt.Errorf("custom tool %q is not a regular file", customPath)
	case info.Mode().Perm()&0o111 == 0:
		return Tool{}, fmt.Errorf("custom tool %q is not executable", customPath)
	}
	args := strings.TrimSpace(customArgs)
	if args == "" {
		if merge {
			args = `"$BASE" "$LOCAL" "$REMOTE" "$MERGED"`
		} else {
			args = `"$LOCAL" "$REMOTE"`
		}
	}
	return Tool{Name: Custom, Command: git.ShellQuote(customPath) + " " + args}, nil
}
