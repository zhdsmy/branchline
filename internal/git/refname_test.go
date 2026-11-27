package git

import (
	"os/exec"
	"testing"
)

// RefNameProblem and git check-ref-format must agree on what is a name.
func TestRefNameProblemMatchesGit(t *testing.T) {
	names := map[string]string{
		"feature/ok": "", "a": "", "v1.0": "", "feat-1_2": "",
		"@":        "Branch or tag name cannot be '@'.",
		"/lead":    "Must not start with '/'.",
		".dot":     "Must not start with '.'.",
		"a..b":     "Must not contain '..'.",
		"a//b":     "Must not contain consecutive '/'.",
		"a/.b":     "Path segment must not start with '.'.",
		"a/b.lock": "Path segment must not end with '.lock'.",
		"b.lock":   "Must not end with '.lock'.",
		"end.":     "Must not end with '.'.",
		"a@{b":     "Must not contain '@{'.",
		"a\\b":     "Must not contain '\\'.",
		"a*b":      "Must not contain '?', '*', or '['.",
		"a:b":      "Must not contain '~', '^', ':', or control characters.",
		"a~b":      "Must not contain '~', '^', ':', or control characters.",
	}
	for name, want := range names {
		if got := RefNameProblem(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
		valid := exec.Command("git", "check-ref-format", "--branch", name).Run() == nil
		if name == "@" {
			valid = false
		}
		if valid != (want == "") {
			t.Errorf("%q: git says valid=%v", name, valid)
		}
	}
}
