package git

import (
	"errors"
	"fmt"
	"strings"
)

// Commit commits the index with message, or amends HEAD with it.
func (r *Repo) Commit(message string, amend bool) error {
	return r.CommitWith(message, amend, CommitOptions{})
}

// CommitOptions are the author and trailer Settings applies to a commit.
type CommitOptions struct {
	Name, Email string
	SignedOff   bool
	GPGKey      string
}

func (r *Repo) CommitWith(message string, amend bool, o CommitOptions) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("the commit message is empty")
	}
	signoff := false
	if o.SignedOff && !strings.Contains(message, "Signed-off-by:") {
		if strings.TrimSpace(o.Name) != "" {
			who := strings.TrimSpace(o.Name)
			if o.Email != "" {
				who += " <" + strings.TrimSpace(o.Email) + ">"
			}
			message = strings.TrimRight(message, "\n") + "\n\nSigned-off-by: " + who + "\n"
		} else {
			// Without an explicit author override the committer identity is
			// only settled by git itself (repository or global configuration),
			// so a hand-written trailer cannot name it: the native flag
			// appends Signed-off-by for the committer on every platform.
			signoff = true
		}
	}
	args := []string{"commit", "-F", "-", "--cleanup=strip"}
	if amend {
		args = append(args, "--amend")
	}
	if signoff {
		args = append(args, "--signoff")
	}
	if o.Name != "" {
		args = append(args, "--author="+o.Name+" <"+o.Email+">")
	}
	if o.GPGKey != "" {
		args = append(args, "-S", "--gpg-sign="+o.GPGKey)
	}
	_, err := r.runInput(quick, []byte(message), args...)
	if err != nil {
		return err
	}
	if err := r.restoreSyncStash(nil); err != nil {
		return fmt.Errorf("commit created, but saved sync changes could not be restored: %w", err)
	}
	return nil
}

// LastMessage returns HEAD's message, for amending.
func (r *Repo) LastMessage() (string, error) {
	return r.Message("HEAD")
}

// Message returns the full message of one commit.
func (r *Repo) Message(rev string) (string, error) {
	out, err := r.run(quick, "log", "-1", "--format=%B", rev)
	return strings.TrimRight(string(out), "\n"), err
}
