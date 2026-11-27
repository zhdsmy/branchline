package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CompareSides reads the two versions named by a difftool request. changed
// "index" is the index and base is its HEAD, or the empty tree when base is
// empty. An empty changed is the working tree. Any other pair is two
// revisions, with an empty base meaning the empty tree. A second path is the
// base path of a rename; a missing version is returned as an empty file.
func (r *Repo) CompareSides(base, changed string, paths ...string) (localData, remoteData []byte, localName, remoteName string, err error) {
	if len(paths) == 0 || paths[0] == "" {
		return nil, nil, "", "", fmt.Errorf("missing file to compare")
	}
	remoteName = paths[0]
	localName = remoteName
	if len(paths) > 1 && paths[1] != "" && paths[1] != remoteName {
		localName = paths[1]
	}
	switch {
	case changed == "index":
		if base == "" {
			base = "HEAD"
		}
		localData, err = r.sideContent(base, localName)
		if err != nil {
			return nil, nil, "", "", err
		}
		remoteData, err = r.sideContent("index", remoteName)
	case changed == "":
		if base == "" {
			base = "HEAD"
		}
		localData, err = r.sideContent(base, localName)
		if err != nil {
			return nil, nil, "", "", err
		}
		remoteData, err = r.sideContent("worktree", remoteName)
	default:
		if base == "" {
			base = emptyTree
		}
		localData, err = r.sideContent(base, localName)
		if err != nil {
			return nil, nil, "", "", err
		}
		remoteData, err = r.sideContent(changed, remoteName)
	}
	if err != nil {
		return nil, nil, "", "", err
	}
	return localData, remoteData, localName, remoteName, nil
}

// sideContent reads one revision of path. A missing path is an empty file,
// which is what an addition, deletion, or comparison with the empty tree
// shows. A connection, permission, or corrupt-reference failure is returned
// unchanged. The read uses git and therefore works over the repository's SSH
// connection without running a diff tool there.
func (r *Repo) sideContent(rev, path string) ([]byte, error) {
	if rev == "worktree" {
		return r.worktreeFile(path)
	}
	if rev == emptyTree {
		return []byte{}, nil
	}
	spec := ":" + path
	if rev != "index" {
		if err := validateRevisionArg(rev); err != nil {
			return nil, err
		}
		spec = rev + ":" + path
	}
	out, err := r.run(slow, "rev-parse", "-q", "--verify", spec)
	if err != nil {
		if missingObject(err) {
			if err := r.confirmRevision(rev); err != nil {
				return nil, err
			}
			return []byte{}, nil
		}
		return nil, err
	}
	hash := strings.TrimSpace(string(out))
	if !isHex(hash) {
		return nil, fmt.Errorf("git rev-parse returned %q", out)
	}
	return r.run(slow, "cat-file", "blob", hash)
}

// worktreeFile reads a working-tree path without writing an object. A local
// repository opens the file directly. SSH prints the file from the server
// without invoking a diff tool. Only a confirmed missing file is an empty side.
func (r *Repo) worktreeFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || pathEscapes(path) {
		return nil, fmt.Errorf("missing file to compare")
	}
	if rooted, ok := r.Exec.(interface{ Worktree() string }); ok {
		root := strings.TrimSpace(rooted.Worktree())
		if root != "" {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if os.IsNotExist(err) {
				return []byte{}, nil
			}
			return data, err
		}
	}
	if local, ok := r.Exec.(*LocalExecutor); ok && strings.TrimSpace(local.Dir) != "" {
		data, err := os.ReadFile(filepath.Join(local.Dir, filepath.FromSlash(path)))
		if os.IsNotExist(err) {
			return []byte{}, nil
		}
		return data, err
	}
	type remoteShell interface {
		RunShell(context.Context, string) ([]byte, error)
		DirPath() string
	}
	ssh, ok := r.Exec.(remoteShell)
	if direct, directOK := r.Exec.(*SSHExecutor); directOK {
		ssh, ok = direct, true
	}
	if !ok {
		return nil, fmt.Errorf("cannot read the working tree of %s", r.Exec.Describe())
	}
	ctx, cancel := context.WithTimeout(context.Background(), slow)
	defer cancel()
	out, err := ssh.RunShell(ctx, "cat -- "+remoteJoin(ssh.DirPath(), path))
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("read %s: timed out after %s", path, slow)
	}
	if err != nil {
		if missingRemoteFile(err) {
			return []byte{}, nil
		}
		return nil, err
	}
	return out, nil
}

// confirmRevision checks that rev itself can be resolved before a missing
// path is treated as an empty side. A missing index entry or path in a valid
// revision exits 1, but so can a bad revision. HEAD in an unborn repository
// is a valid empty base. Any other revision failure is returned.
func (r *Repo) confirmRevision(rev string) error {
	if rev == "index" {
		return r.confirmIndex()
	}
	if rev == "HEAD" {
		if err := r.confirmCommit("HEAD"); err == nil {
			return nil
		} else if !missingObject(err) {
			return err
		}
		return r.confirmUnbornHEAD()
	}
	return r.confirmCommit(rev)
}

// confirmIndex accepts a readable index, including one with no entry for the
// requested path and one in an unborn repository. A damaged index is an error.
// The check only lists the index, so it does not refresh or rewrite it.
func (r *Repo) confirmIndex() error {
	_, err := r.run(slow, "ls-files", "--stage", "--")
	return err
}

// confirmUnbornHEAD accepts the missing commit only after HEAD is symbolic and
// its target ref does not exist. A detached HEAD and a ref naming a missing or
// unsuitable object are not an unborn repository.
func (r *Repo) confirmUnbornHEAD() error {
	out, err := r.run(slow, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("git HEAD does not resolve to a commit")
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return fmt.Errorf("git symbolic-ref returned an empty HEAD")
	}
	_, err = r.run(slow, "show-ref", "--verify", "--quiet", ref)
	if missingObject(err) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("git HEAD does not resolve to a commit")
	}
	return err
}

// confirmCommit requires rev to resolve to a commit. A quiet missing result
// is still a failure: exit 1 also covers a ref whose object cannot be read.
func (r *Repo) confirmCommit(rev string) error {
	if err := validateRevisionArg(rev); err != nil {
		return err
	}
	out, err := r.run(slow, "rev-parse", "-q", "--verify", rev+"^{commit}")
	if err != nil {
		return err
	}
	if !isHex(strings.TrimSpace(string(out))) {
		return fmt.Errorf("git rev-parse returned %q", out)
	}
	return nil
}

func missingObject(err error) bool {
	return ExitCode(err) == 1
}

func missingRemoteFile(err error) bool {
	var ge *Error
	if !errors.As(err, &ge) || ge.ExitCode == 255 {
		return false
	}
	msg := strings.ToLower(ge.Stderr)
	return strings.Contains(msg, "no such file") || strings.Contains(msg, "not a directory")
}

func pathEscapes(path string) bool {
	rel := strings.TrimPrefix(filepath.ToSlash(path), "/")
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return rel == ""
}

func remoteJoin(root, path string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(path), "/")
	base := strings.TrimRight(root, "/")
	switch {
	case base == "~":
		base = "~"
	case strings.HasPrefix(base, "~/"):
		base = "~/" + ShellQuote(base[2:])
	default:
		base = ShellQuote(base)
	}
	return base + "/" + ShellQuote(rel)
}
