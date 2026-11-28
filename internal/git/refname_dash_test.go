package git

import (
	"os/exec"
	"testing"
)

// A branch or tag name starting with "-" is one git itself refuses:
// git check-ref-format --branch rejects it, because on a command line the
// name would read as options. RefNameProblem must reject it too, so the
// dialogs answer as the user types instead of letting git fail later.
func TestRefNameProblemDash(t *testing.T) {
	for _, name := range []string{"-x", "--help", "--receive-pack=./evil.sh"} {
		if got := RefNameProblem(name); got == "" {
			t.Errorf("RefNameProblem(%q) is empty, want a rejection", name)
		}
		if exec.Command("git", "check-ref-format", "--branch", name).Run() == nil {
			t.Errorf("git check-ref-format --branch accepts %q: the premise of the rejection is gone", name)
		}
	}
	for _, name := range []string{"a-x", "x-", "a/-x", ""} {
		if got := RefNameProblem(name); got != "" {
			t.Errorf("RefNameProblem(%q) = %q, want empty", name, got)
		}
	}
}
