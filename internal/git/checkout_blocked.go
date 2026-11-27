package git

import (
	"errors"
	"strconv"
	"strings"
)

// CheckoutBlocked lists the files a checkout refused to overwrite: the
// tracked files with local changes and the untracked files in its way, in
// the order git names them.
type CheckoutBlocked struct {
	Changed   []string
	Untracked []string
}

const (
	changedBlockedHeader = "error: Your local changes to the following files would be overwritten by checkout:"
	untrackedOverwritten = "error: The following untracked working tree files would be overwritten by checkout:"
	untrackedRemoved     = "error: The following untracked working tree files would be removed by checkout:"
)

// untrackedBlockedHeader reports whether line heads the untracked section
// of a refused checkout. Both wordings parse: git prints "overwritten" when
// the checkout would replace the file and "removed" when the file is only
// in the way of what the target tree needs at that path.
func untrackedBlockedHeader(line string) bool {
	return line == untrackedOverwritten || line == untrackedRemoved
}

// CheckoutBlockedBy reports whether err is git refusing a checkout because
// local changes or untracked files would be overwritten, and parses the
// files it names out of the command's standard error. The paths keep their
// exact bytes — spaces and non-ASCII included — except the C-style quoting
// git adds around paths it cannot print literally, which is removed here.
// Anything but a *git.Error carrying one of the two sections is not a
// blocked checkout.
func CheckoutBlockedBy(err error) (CheckoutBlocked, bool) {
	var ge *Error
	if !errors.As(err, &ge) {
		return CheckoutBlocked{}, false
	}
	var blocked CheckoutBlocked
	found := false
	var section *[]string
	for _, line := range strings.Split(ge.Stderr, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == changedBlockedHeader:
			section = &blocked.Changed
			found = true
		case untrackedBlockedHeader(line):
			section = &blocked.Untracked
			found = true
		case strings.HasPrefix(line, "\t") && section != nil:
			*section = append(*section, unquoteCheckoutPath(strings.TrimPrefix(line, "\t")))
		default:
			section = nil
		}
	}
	if !found {
		return CheckoutBlocked{}, false
	}
	return blocked, true
}

// unquoteCheckoutPath reverses the C-style quoting git puts around a path
// that holds bytes quote_path cannot print literally — control characters,
// a double quote or a backslash — so the dialog names the file as it is on
// disk. A line that is not valid quoting is a path that never needed any
// and is returned as it came.
func unquoteCheckoutPath(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		return unquoted
	}
	return s
}

// WithSavedChanges stashes the working tree — untracked files included —,
// runs fn, and restores the saved changes afterwards exactly like the sync
// operations do. The restore always runs, also when fn failed: the checkout
// fn performs either happened or not, and in both cases the user's changes
// belong back in the working tree. When the reapply conflicts, the changes
// stay in the stash list and the returned error says so.
func (r *Repo) WithSavedChanges(message string, fn func() error) error {
	if err := r.saveSyncStash(message); err != nil {
		return err
	}
	fnErr := fn()
	return r.restoreSyncStash(fnErr)
}
