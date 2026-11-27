package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cloneTimeout bounds a clone, which can be long; the Clone sheet can
// cancel it sooner.
const cloneTimeout = 2 * time.Hour

// CloneOptions are the choices of the Clone sheet.
type CloneOptions struct {
	URL string
	// Name is the directory git creates in the executor's directory.
	Name string
	// Recurse clones the submodules too, as the app does when Settings
	// update submodules on checkout.
	Recurse bool
}

// CloneInto clones URL into Name, under the executor's directory: a
// folder of this Mac, or one of the server for an SSH executor, which
// then holds the repository. Canceling ctx stops git, and a clone that
// did not finish leaves no directory behind.
func (r *Repo) CloneInto(ctx context.Context, o CloneOptions) error {
	src, name := strings.TrimSpace(o.URL), strings.TrimSpace(o.Name)
	if src == "" {
		return errors.New("enter the repository URL")
	}
	if err := CheckCloneName(name); err != nil {
		return err
	}
	existed := pathExists(r.Exec, name)
	args := []string{"clone"}
	if o.Recurse {
		args = append(args, "--recurse-submodules")
	}
	args = append(args, "--", src, name)
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	_, err := recordInvocation(ctx, r.cmdLog, args, func(runCtx context.Context) ([]byte, error) {
		out, runErr := r.Exec.Run(runCtx, nil, args...)
		if ctx.Err() != nil {
			if !existed {
				_ = removeTree(r.Exec, name)
			}
			if ctx.Err() == context.DeadlineExceeded {
				return out, fmt.Errorf("git clone: timed out after %s", cloneTimeout)
			}
			return out, context.Canceled
		}
		return out, runErr
	})
	return err
}

// CheckCloneName reports a directory name git clone should not be given.
func CheckCloneName(name string) error {
	switch {
	case name == "":
		return errors.New("enter the name of the repository's folder")
	case name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00"):
		return fmt.Errorf("%q cannot name a folder", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("the folder name cannot start with -")
	}
	return nil
}

// EnsureDir creates the executor's directory and the folders above it,
// on this Mac or on the server.
func EnsureDir(ex Executor) error {
	switch e := ex.(type) {
	case *LocalExecutor:
		return os.MkdirAll(e.Dir, 0o755)
	case *SSHExecutor:
		ctx, cancel := context.WithTimeout(context.Background(), quick)
		defer cancel()
		_, err := e.RunShell(ctx, "mkdir -p "+RemotePath(e.Dir))
		return err
	}
	return nil
}

// pathExists reports whether name is in the executor's directory. An
// executor it cannot ask says yes, so that nothing is removed.
func pathExists(ex Executor, name string) bool {
	switch e := ex.(type) {
	case *LocalExecutor:
		_, err := os.Lstat(filepath.Join(e.Dir, name))
		return err == nil || !errors.Is(err, os.ErrNotExist)
	case *SSHExecutor:
		ctx, cancel := context.WithTimeout(context.Background(), quick)
		defer cancel()
		out, err := e.RunShell(ctx, "cd "+RemotePath(e.Dir)+" && if [ -e "+ShellQuote(name)+" ]; then echo yes; else echo no; fi")
		return err != nil || strings.TrimSpace(string(out)) != "no"
	}
	return true
}

// removeTree deletes what a canceled clone left of name.
func removeTree(ex Executor, name string) error {
	if CheckCloneName(name) != nil {
		return nil
	}
	switch e := ex.(type) {
	case *LocalExecutor:
		return os.RemoveAll(filepath.Join(e.Dir, name))
	case *SSHExecutor:
		ctx, cancel := context.WithTimeout(context.Background(), quick)
		defer cancel()
		_, err := e.RunShell(ctx, "cd "+RemotePath(e.Dir)+" && rm -rf -- "+ShellQuote(name))
		return err
	}
	return nil
}

// RepoNameFromURL is the folder git clone makes for a URL: its last part
// without .git.
func RepoNameFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexAny(s, "?#"); i >= 0 && strings.Contains(s, "://") {
		s = s[:i]
	}
	for {
		t := strings.TrimSuffix(strings.TrimSuffix(s, "/"), "/.git")
		if t == s {
			break
		}
		s = t
	}
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/:\\"); i >= 0 {
		s = s[i+1:]
	}
	if CheckCloneName(s) != nil {
		return ""
	}
	return s
}

// URLProtocol is "https" or "ssh" for a URL of a hosting service, or ""
// for another, such as a path.
func URLProtocol(raw string) string {
	s := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return "https"
	case strings.HasPrefix(s, "ssh://"):
		return "ssh"
	}
	if host, p, ok := strings.Cut(s, ":"); ok && host != "" && p != "" && !strings.ContainsAny(host, "/\\") && !strings.Contains(s, "://") && len(host) > 1 {
		return "ssh"
	}
	return ""
}

// ConvertURL writes a hosting URL with the other protocol — the https or
// ssh form of the same repository: https://host/owner/repo.git and
// git@host:owner/repo.git. ok is false for a URL it cannot read.
func ConvertURL(raw, protocol string) (string, bool) {
	s := strings.TrimSpace(raw)
	from := URLProtocol(s)
	if from == "" || protocol != "https" && protocol != "ssh" {
		return s, false
	}
	if from == protocol {
		return s, true
	}
	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return s, false
		}
		host, p = u.Hostname(), u.Path
	default:
		h, rest, _ := strings.Cut(s, ":")
		if _, after, ok := strings.Cut(h, "@"); ok {
			h = after
		}
		host, p = h, rest
	}
	p = strings.Trim(p, "/")
	if host == "" || p == "" {
		return s, false
	}
	if !strings.HasSuffix(p, ".git") {
		p += ".git"
	}
	if protocol == "https" {
		return "https://" + host + "/" + p, true
	}
	return "git@" + host + ":" + p, true
}

// The answers of Test Connection.
const (
	ConnectionSucceeded = "Connected to remote"
	PermissionDenied    = "SSH authentication failed (public key rejected)"
	RepositoryNotFound  = "Remote repository not found"
)

// ConnectionMessage is what Test Connection says for the result of
// TestRemote: one of the three answers above, or git's own words for
// another failure. Those words are the first fatal: or error: line of
// what git printed, or its first non-empty line when there is neither:
// a failure often starts with progress such as "Cloning into 'x'...",
// while the reason follows on a fatal: line. The message can hold what
// a credential helper or the remote wrote, so the caller redacts it —
// for example with RedactText — before showing or logging it.
func ConnectionMessage(err error) string {
	if err == nil {
		return ConnectionSucceeded
	}
	msg := err.Error()
	var ge *Error
	if errors.As(err, &ge) && strings.TrimSpace(ge.Stderr) != "" {
		msg = ge.Stderr
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "permission denied (publickey"):
		return PermissionDenied
	case strings.Contains(lower, "repository not found"),
		strings.Contains(lower, "does not appear to be a git repository"),
		strings.Contains(lower, "not found") && strings.Contains(lower, "repository"),
		strings.Contains(lower, "returned error: 404"):
		return RepositoryNotFound
	}
	lines := strings.Split(msg, "\n")
	for _, prefix := range []string{"fatal:", "error:"} {
		for _, line := range lines {
			if text, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				if text = strings.TrimSpace(text); text != "" {
					return text
				}
			}
		}
	}
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return strings.TrimSpace(msg)
}
