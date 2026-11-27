package git

import (
	"errors"
	"strings"
)

// Output runs a git command and returns what it printed.
func (r *Repo) Output(args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("git needs a command")
	}
	out, err := r.run(slow, args...)
	return string(out), err
}

// Script runs a shell script in the top folder of the repository. It goes
// through git as an alias, so a repository on a server runs it there, over
// the same connection as every other command.
func (r *Repo) Script(script string) (string, error) {
	if strings.TrimSpace(script) == "" {
		return "", errors.New("the script is empty")
	}
	out, err := r.run(slow, "-c", "alias.branchlinescript=!"+script, "branchlinescript")
	return string(out), err
}
