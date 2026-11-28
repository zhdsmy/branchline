// Remote file editing for repositories reached over SSH: download one file
// of the server's working tree into a private local copy, and write the
// edited copy back only while the server file is still what was downloaded.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RemoteFileMeta is the server's description of a file when it was read or
// written: its size, permission bits as octal digits, modification time as
// seconds, and SHA-256 of its content.
type RemoteFileMeta struct {
	Size   int64
	Mode   string // octal, for example "644"
	MTime  int64
	SHA256 string
}

// RemoteCopy is one file of an SSH repository's working tree, copied to this
// machine for editing. Path is the slash-separated path inside the
// repository, Local the copy on this machine, and Meta what the server file
// was when the copy was made.
type RemoteCopy struct {
	Path  string
	Local string
	Meta  RemoteFileMeta
}

var (
	// ErrNotSSHRepository is the repository not being reached over SSH, so
	// its working tree cannot be read or written through a server shell.
	ErrNotSSHRepository = errors.New("the repository is not on a server reached over SSH")
	// ErrRemoteChanged is the file on the server no longer being what was
	// downloaded: someone or something else wrote it. Nothing is written.
	ErrRemoteChanged = errors.New("the file on the server changed since it was downloaded")
	// ErrRemoteSymlink is a symbolic link in the path: the repository root,
	// a directory leading to the file, or the file itself. Nothing is read
	// or written through one, so a link cannot redirect the edit outside
	// the repository.
	ErrRemoteSymlink = errors.New("the path on the server holds a symbolic link")
	// ErrRemoteNotRegular is the file missing or not being a regular file.
	ErrRemoteNotRegular = errors.New("the file on the server is missing or not a regular file")
	// ErrRemoteTempFile is the server failing to create the temporary file
	// the write goes through.
	ErrRemoteTempFile = errors.New("cannot create a temporary file on the server")
	// ErrRemoteUploadCorrupt is the temporary file on the server not holding
	// what was sent: size or hash differs, so the write is dropped.
	ErrRemoteUploadCorrupt = errors.New("the upload reached the server corrupted")
	// ErrRemoteMetaAfterSave is the server failing to describe the file
	// after it was written. The write itself already happened.
	ErrRemoteMetaAfterSave = errors.New("the file was saved on the server, but reading its new metadata failed")
)

// remoteEditShell is what editing a remote file needs of an executor: run a
// shell command on the server, and the repository's path there.
type remoteEditShell interface {
	RunShellInput(ctx context.Context, command string, stdin []byte) ([]byte, error)
	DirPath() string
}

// remoteEditExit codes the server scripts report with. They are high enough
// not to collide with the shell's own (126, 127) or a signal's (128+n).
const (
	remoteExitSymlink       = 10
	remoteExitNotRegular    = 11
	remoteExitChanged       = 12
	remoteExitTempFile      = 13
	remoteExitUploadCorrupt = 14
	remoteExitMetaAfterSave = 15
)

const remoteEditMetaPrefix = "BLREMOTE1 "

// validRemoteEditPath rejects an empty or absolute path, one holding an
// empty, "." or ".." segment, and one holding NUL. Bytes are kept as they
// are: no whitespace trimming, no backslash rewriting.
func validRemoteEditPath(path string) error {
	if path == "" {
		return fmt.Errorf("the path is empty")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("the path %q is absolute; it must be relative to the repository", path)
	}
	if strings.IndexByte(path, 0) >= 0 {
		return fmt.Errorf("the path holds a NUL byte")
	}
	if pathEscapes(path) {
		return fmt.Errorf("the path %q is not a file inside the repository", path)
	}
	return nil
}

func remoteEditExecutor(ex Executor) (remoteEditShell, error) {
	if direct, ok := ex.(*SSHExecutor); ok {
		return direct, nil
	}
	if shell, ok := ex.(remoteEditShell); ok {
		return shell, nil
	}
	return nil, ErrNotSSHRepository
}

// remoteEditComponents is the path split into its slash-separated segments.
func remoteEditComponents(path string) []string {
	return strings.Split(path, "/")
}

// quoteRelComponent quotes one path segment for the server's shell.
func quoteRelComponent(c string) string {
	return ShellQuote(c)
}

// cleanRemoteRoot strips trailing slashes so `[ -L root ]` tests the root
// entry itself rather than dereferencing a symlinked directory.
func cleanRemoteRoot(root string) string {
	trimmed := strings.TrimRight(root, "/")
	if trimmed == "" && root != "" {
		return "/"
	}
	return trimmed
}

// writeRemoteSymlinkChecks writes POSIX shell checks that exit with
// remoteExitSymlink if any prefix component or the file itself is a symlink.
func writeRemoteSymlinkChecks(b *strings.Builder, comps []string) {
	prefix := "./"
	for _, c := range comps {
		prefix += quoteRelComponent(c)
		b.WriteString("[ -L ")
		b.WriteString(prefix)
		b.WriteString(" ] && exit ")
		b.WriteString(strconv.Itoa(remoteExitSymlink))
		b.WriteString(";")
		prefix += "/"
	}
}

// remoteEditHelpers is the shared part of the server scripts: refuse symbolic
// links on the root and on every path prefix, move into the repository, and
// define a helper printing "size mode mtime sha256" of one file. GNU tools
// (stat -c, sha256sum) are tried first, BSD ones (stat -f, shasum -a 256)
// second; the hash always comes from standard input so its output never has
// to be parsed apart from a file name.
func remoteEditHelpers(root string, comps []string) string {
	var b strings.Builder
	rq := RemotePath(cleanRemoteRoot(root))
	b.WriteString("[ -L ")
	b.WriteString(rq)
	b.WriteString(" ] && exit ")
	b.WriteString(strconv.Itoa(remoteExitSymlink))
	b.WriteString(";")
	b.WriteString("cd ")
	b.WriteString(rq)
	b.WriteString(" || exit 3;")
	writeRemoteSymlinkChecks(&b, comps)
	b.WriteString("__bl_stat(){ a=$(stat -c '%s %a %Y' \"$1\" 2>/dev/null) || a=$(stat -f '%z %Lp %m' \"$1\" 2>/dev/null) || return 1; printf '%s' \"$a\";};")
	b.WriteString("__bl_hash(){ h=$(sha256sum < \"$1\" 2>/dev/null) || h=$(shasum -a 256 < \"$1\" 2>/dev/null) || return 1; printf '%s' \"${h%% *}\";};")
	b.WriteString("__bl_meta(){ s=$(__bl_stat \"$1\") || return 1; hh=$(__bl_hash \"$1\") || return 1; printf '%s %s' \"$s\" \"$hh\";};")
	return b.String()
}

// remoteEditDownloadScript is one server command that checks the file is a
// readable regular file with no symbolic link in its path, prints one line
// of metadata, then the file's bytes.
func remoteEditDownloadScript(root, path string) string {
	comps := remoteEditComponents(path)
	var b strings.Builder
	b.WriteString(remoteEditHelpers(root, comps))
	rel := "./" + strings.Join(quoteComponents(comps), "/")
	b.WriteString("[ -f ")
	b.WriteString(rel)
	b.WriteString(" ] && [ -r ")
	b.WriteString(rel)
	b.WriteString(" ] || exit ")
	b.WriteString(strconv.Itoa(remoteExitNotRegular))
	b.WriteString(";")
	b.WriteString("m=$(__bl_meta ")
	b.WriteString(rel)
	b.WriteString(") || exit 3;printf '%s\\n' '")
	b.WriteString(remoteEditMetaPrefix)
	b.WriteString("'\"$m\";cat -- ")
	b.WriteString(rel)
	return b.String()
}

func quoteComponents(comps []string) []string {
	out := make([]string, len(comps))
	for i, c := range comps {
		out[i] = quoteRelComponent(c)
	}
	return out
}

// remoteEditSaveScript is one server command that re-checks the file is a
// regular file without symbolic links, compares its metadata with what was
// downloaded, writes the bytes of stdin into a temporary file in the same
// directory, verifies them, restores the permission bits, re-checks the
// metadata once more, renames the temporary file over the original, and
// prints the new metadata. The temporary file is removed on any failure,
// and a HUP, INT or TERM removes it and exits with 129, 130 or 143.
func remoteEditSaveScript(root, path string, meta RemoteFileMeta, size int64, sha string, mode string) string {
	comps := remoteEditComponents(path)
	var b strings.Builder
	b.WriteString(remoteEditHelpers(root, comps))
	rel := "./" + strings.Join(quoteComponents(comps), "/")
	relq := rel
	expect := fmt.Sprintf("%d %s %d %s", meta.Size, meta.Mode, meta.MTime, meta.SHA256)
	exit := func(code int) string {
		return "exit " + strconv.Itoa(code) + ";"
	}
	b.WriteString("[ -f ")
	b.WriteString(relq)
	b.WriteString(" ] || exit ")
	b.WriteString(strconv.Itoa(remoteExitNotRegular))
	b.WriteString(";")
	b.WriteString("cur=$(__bl_meta ")
	b.WriteString(relq)
	b.WriteString(") || cur=;")
	b.WriteString("[ \"$cur\" = ")
	b.WriteString(ShellQuote(expect))
	b.WriteString(" ] || ")
	b.WriteString(exit(remoteExitChanged))
	dirq := "./" + strings.Join(quoteComponents(comps[:len(comps)-1]), "/")
	b.WriteString("tmp=$(mktemp ")
	b.WriteString(strings.TrimSuffix(dirq, "/") + "/.branchline-XXXXXXXX")
	b.WriteString(" 2>/dev/null) || exit ")
	b.WriteString(strconv.Itoa(remoteExitTempFile))
	b.WriteString(";")
	b.WriteString("[ -n \"$tmp\" ] || ")
	b.WriteString(exit(remoteExitTempFile))
	b.WriteString("trap 'rm -f \"$tmp\"' EXIT;")
	b.WriteString("trap 'rm -f \"$tmp\";exit 129' HUP;")
	b.WriteString("trap 'rm -f \"$tmp\";exit 130' INT;")
	b.WriteString("trap 'rm -f \"$tmp\";exit 143' TERM;")
	b.WriteString("cat > \"$tmp\" || exit 3;")
	b.WriteString("tm=$(__bl_meta \"$tmp\") || tm=;")
	b.WriteString("set -f;set -- $tm;set +f;")
	b.WriteString("[ \"$1\" = ")
	b.WriteString(ShellQuote(strconv.FormatInt(size, 10)))
	b.WriteString(" ] && [ \"$4\" = ")
	b.WriteString(ShellQuote(sha))
	b.WriteString(" ] || ")
	b.WriteString(exit(remoteExitUploadCorrupt))
	b.WriteString("chmod ")
	b.WriteString(ShellQuote(mode))
	b.WriteString(" \"$tmp\" || exit 3;")
	writeRemoteSymlinkChecks(&b, comps)
	b.WriteString("cur=$(__bl_meta ")
	b.WriteString(relq)
	b.WriteString(") || cur=;")
	b.WriteString("[ \"$cur\" = ")
	b.WriteString(ShellQuote(expect))
	b.WriteString(" ] || ")
	b.WriteString(exit(remoteExitChanged))
	b.WriteString("mv -f \"$tmp\" ")
	b.WriteString(relq)
	b.WriteString(" || exit 3;")
	b.WriteString("trap - EXIT HUP INT TERM;")
	b.WriteString("nm=$(__bl_meta ")
	b.WriteString(relq)
	b.WriteString(") || exit ")
	b.WriteString(strconv.Itoa(remoteExitMetaAfterSave))
	b.WriteString(";")
	b.WriteString("printf '%s\\n' '")
	b.WriteString(remoteEditMetaPrefix)
	b.WriteString("'\"$nm\"")
	return b.String()
}

// parseRemoteMeta parses the metadata line of a server script: size, octal
// permission bits, modification seconds and lowercase hex SHA-256.
func parseRemoteMeta(line string) (RemoteFileMeta, error) {
	fields := strings.Split(line, " ")
	if len(fields) != 4 {
		return RemoteFileMeta{}, fmt.Errorf("the server returned malformed metadata %q", line)
	}
	size, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || size < 0 {
		return RemoteFileMeta{}, fmt.Errorf("the server returned a malformed size %q", fields[0])
	}
	if len(fields[1]) < 3 || len(fields[1]) > 4 {
		return RemoteFileMeta{}, fmt.Errorf("the server returned malformed permission bits %q", fields[1])
	}
	for _, r := range fields[1] {
		if r < '0' || r > '7' {
			return RemoteFileMeta{}, fmt.Errorf("the server returned malformed permission bits %q", fields[1])
		}
	}
	mtime, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return RemoteFileMeta{}, fmt.Errorf("the server returned a malformed modification time %q", fields[2])
	}
	if len(fields[3]) != sha256.Size*2 {
		return RemoteFileMeta{}, fmt.Errorf("the server returned a malformed hash %q", fields[3])
	}
	if _, err := hex.DecodeString(fields[3]); err != nil {
		return RemoteFileMeta{}, fmt.Errorf("the server returned a malformed hash %q", fields[3])
	}
	return RemoteFileMeta{Size: size, Mode: fields[1], MTime: mtime, SHA256: fields[3]}, nil
}

// splitRemoteMetaLine reads the first output line of a server script, which
// must be the metadata line.
func splitRemoteMetaLine(out []byte) (RemoteFileMeta, []byte, error) {
	if !bytes.HasPrefix(out, []byte(remoteEditMetaPrefix)) {
		return RemoteFileMeta{}, nil, fmt.Errorf("the server output does not start with metadata: %.80q", string(out))
	}
	rest := out[len(remoteEditMetaPrefix):]
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 {
		return RemoteFileMeta{}, nil, fmt.Errorf("the server output ends after the metadata line")
	}
	meta, err := parseRemoteMeta(string(rest[:nl]))
	if err != nil {
		return RemoteFileMeta{}, nil, err
	}
	return meta, rest[nl+1:], nil
}

func remoteEditError(err error) error {
	var ge *Error
	if !errors.As(err, &ge) {
		return err
	}
	switch ge.ExitCode {
	case remoteExitSymlink:
		return ErrRemoteSymlink
	case remoteExitNotRegular:
		return ErrRemoteNotRegular
	case remoteExitChanged:
		return ErrRemoteChanged
	case remoteExitTempFile:
		return ErrRemoteTempFile
	case remoteExitUploadCorrupt:
		return ErrRemoteUploadCorrupt
	case remoteExitMetaAfterSave:
		return ErrRemoteMetaAfterSave
	}
	return err
}

// DownloadForEdit copies one file of an SSH repository's working tree onto
// this machine for editing. The copy lands in a fresh private directory
// under cacheDir, and the returned RemoteCopy remembers what the server file
// was: SaveEdited writes back only while that is still true.
//
// The server reads the file after describing it, so a concurrent write can
// slip between the two; the bytes received are hashed again here and a
// mismatch is reported as the file having changed during the download.
//
// cacheDir, the fresh directory inside it and the copy are private to the
// user (0700, 0700, 0600) whatever permission bits the server file has: the
// copy is meant to be edited. On any failure the fresh directory is removed.
func (r *Repo) DownloadForEdit(ctx context.Context, path, cacheDir string) (RemoteCopy, error) {
	if err := validRemoteEditPath(path); err != nil {
		return RemoteCopy{}, err
	}
	ssh, err := remoteEditExecutor(r.Exec)
	if err != nil {
		return RemoteCopy{}, err
	}
	out, runErr := ssh.RunShellInput(ctx, remoteEditDownloadScript(ssh.DirPath(), path), nil)
	if runErr != nil {
		return RemoteCopy{}, remoteEditError(runErr)
	}
	meta, content, err := splitRemoteMetaLine(out)
	if err != nil {
		return RemoteCopy{}, err
	}
	sum := sha256.Sum256(content)
	if int64(len(content)) != meta.Size || hex.EncodeToString(sum[:]) != meta.SHA256 {
		return RemoteCopy{}, fmt.Errorf("the file changed while it was being downloaded; please try again: %w", ErrRemoteChanged)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return RemoteCopy{}, fmt.Errorf("create the cache directory: %w", err)
	}
	if err := os.Chmod(cacheDir, 0o700); err != nil {
		return RemoteCopy{}, fmt.Errorf("protect the cache directory: %w", err)
	}
	dir, err := os.MkdirTemp(cacheDir, "edit-")
	if err != nil {
		return RemoteCopy{}, fmt.Errorf("create the edit directory: %w", err)
	}
	comps := remoteEditComponents(path)
	local, err := filepath.Abs(filepath.Join(dir, filepath.FromSlash(comps[len(comps)-1])))
	if err != nil {
		_ = os.RemoveAll(dir)
		return RemoteCopy{}, fmt.Errorf("resolve the local copy path: %w", err)
	}
	if err := os.WriteFile(local, content, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return RemoteCopy{}, fmt.Errorf("write the local copy: %w", err)
	}
	return RemoteCopy{Path: path, Local: local, Meta: meta}, nil
}

// SaveEdited writes the edited local copy back to the server, but only while
// the file there is still exactly what c.Meta describes. The write goes
// through a temporary file in the same directory: the server checks the copy
// arrived whole (size and SHA-256), restores the original permission bits,
// checks the original file once more, and renames the temporary file over
// it. On any failure the original file keeps its content and permission
// bits, the temporary file is removed, and the local copy is kept.
//
// This is not a full compare-and-swap: a write on the server between the
// final check and the rename is overwritten without being seen. And the
// rename replaces the directory entry, so afterwards the file belongs to the
// SSH login's user, hard links to the old file keep the old content, and
// extended attributes and access control lists are not carried over.
//
// The returned metadata describes the file after the write; keeping it in
// c.Meta lets the next SaveEdited of the same copy go ahead.
func (r *Repo) SaveEdited(ctx context.Context, c RemoteCopy) (RemoteFileMeta, error) {
	if err := validRemoteEditPath(c.Path); err != nil {
		return RemoteFileMeta{}, err
	}
	ssh, err := remoteEditExecutor(r.Exec)
	if err != nil {
		return RemoteFileMeta{}, err
	}
	if len(c.Meta.Mode) < 3 || len(c.Meta.Mode) > 4 {
		return RemoteFileMeta{}, fmt.Errorf("the remembered permission bits %q are not octal digits", c.Meta.Mode)
	}
	for _, r2 := range c.Meta.Mode {
		if r2 < '0' || r2 > '7' {
			return RemoteFileMeta{}, fmt.Errorf("the remembered permission bits %q are not octal digits", c.Meta.Mode)
		}
	}
	content, err := os.ReadFile(c.Local)
	if err != nil {
		return RemoteFileMeta{}, fmt.Errorf("read the local copy: %w", err)
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	script := remoteEditSaveScript(ssh.DirPath(), c.Path, c.Meta, int64(len(content)), sha, c.Meta.Mode)
	out, runErr := ssh.RunShellInput(ctx, script, content)
	if runErr != nil {
		return RemoteFileMeta{}, remoteEditError(runErr)
	}
	meta, rest, err := splitRemoteMetaLine(out)
	if err != nil {
		return RemoteFileMeta{}, err
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return RemoteFileMeta{}, fmt.Errorf("the server printed unexpected output after the metadata")
	}
	return meta, nil
}
