package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeSSHExecutor stands in for the server: it runs the commands the editing
// code builds with the machine's /bin/sh, in a controlled HOME and PATH, so
// tests exercise the exact script text without any network. Shim directories
// prepended to PATH steer which tools the scripts find. Cancellation behaves
// like the real executor's: the shell's process group gets SIGTERM.
type fakeSSHExecutor struct {
	dir      string
	home     string
	basePath string

	mu       sync.Mutex
	pathDirs []string
	commands []string
}

func (f *fakeSSHExecutor) Describe() string { return "fake ssh " + f.dir }
func (f *fakeSSHExecutor) DirPath() string  { return f.dir }

func (f *fakeSSHExecutor) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, &Error{Args: []string{"git"}, ExitCode: 1, Stderr: "the fake shell does not run git"}
}

func (f *fakeSSHExecutor) prependPath(dirs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pathDirs = append(dirs, f.pathDirs...)
}

func (f *fakeSSHExecutor) commandCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commands)
}

func (f *fakeSSHExecutor) RunShellInput(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	pathDirs := append([]string{}, f.pathDirs...)
	f.mu.Unlock()

	path := strings.Join(append(pathDirs, f.basePath), string(os.PathListSeparator))
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = []string{"HOME=" + f.home, "PATH=" + path, "LC_ALL=C"}
	configureShellCancel(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		msg := stderr.String()
		if code == -1 && msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), &Error{Args: []string{"ssh"}, ExitCode: code, Stderr: msg, Stdout: stdout.Bytes()}
	}
	return stdout.Bytes(), nil
}

// newFakeRemote builds a fake server: home holds the login's home for tilde
// expansion and repoDir an absolute repository path. The base PATH is the
// bare BSD toolchain, with no GNU coreutils.
func newFakeRemote(t *testing.T) (ex *fakeSSHExecutor, repoDir string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repoDir = filepath.Join(base, "repo with space")
	for _, d := range []string{home, repoDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ex = &fakeSSHExecutor{dir: repoDir, home: home, basePath: "/usr/bin:/bin"}
	return ex, repoDir
}

func writeServerFile(t *testing.T, root, rel string, content []byte, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func readServerFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func metaOfFile(t *testing.T, full string) RemoteFileMeta {
	t.Helper()
	st, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return RemoteFileMeta{
		Size:   st.Size(),
		Mode:   fmt.Sprintf("%o", st.Mode().Perm()),
		MTime:  st.ModTime().Unix(),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

// writeShim installs one executable shim script and returns its directory.
func writeShim(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func remoteTmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".branchline-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func manualCopy(t *testing.T, path string, content []byte) RemoteCopy {
	t.Helper()
	local := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return RemoteCopy{
		Path:  path,
		Local: local,
		Meta:  RemoteFileMeta{Size: 1, Mode: "644", MTime: 1, SHA256: hex.EncodeToString(sum[:])},
	}
}

// gnuToolShims fakes the GNU side of the toolchain: stat translates the GNU
// invocation into the BSD one of this machine, sha256sum forwards to shasum.
// Every use is logged so tests can prove the GNU branches ran.
func gnuToolShims(t *testing.T) (dir, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "tools.log")
	statScript := "#!/bin/sh\n" +
		"echo stat >> " + ShellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"-c\" ]; then\n" +
		"  fmt=\"$2\"\n" +
		"  shift 2\n" +
		"  if [ \"$fmt\" = '%s %a %Y' ]; then\n" +
		"    exec /usr/bin/stat -f '%z %Lp %m' \"$@\"\n" +
		"  fi\n" +
		"  exit 1\n" +
		"fi\n" +
		"exec /usr/bin/stat \"$@\"\n"
	sumScript := "#!/bin/sh\n" +
		"echo sha256sum >> " + ShellQuote(logPath) + "\n" +
		"exec /usr/bin/shasum -a 256 \"$@\"\n"
	dir = t.TempDir()
	for name, script := range map[string]string{"stat": statScript, "sha256sum": sumScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, logPath
}

// TestRemoteEditRoundtrip downloads a file, edits the copy and writes it back
// twice: the server content changes, the permission bits survive, the
// returned metadata matches the file, and a second save on top of the first
// goes ahead. It runs once against the BSD toolchain and once against the
// GNU one, faked with shims ahead of PATH.
func TestRemoteEditRoundtrip(t *testing.T) {
	for _, gnu := range []bool{false, true} {
		name := "bsd"
		if gnu {
			name = "gnu"
		}
		t.Run(name, func(t *testing.T) {
			ex, repoDir := newFakeRemote(t)
			var logPath string
			if gnu {
				var shimDir string
				shimDir, logPath = gnuToolShims(t)
				ex.prependPath(shimDir)
			}
			writeServerFile(t, repoDir, "sub/dir/run.sh", []byte("line one\nline two\n"), 0o755)
			repo := New(ex)
			cache := filepath.Join(t.TempDir(), "cache")

			copy, err := repo.DownloadForEdit(context.Background(), "sub/dir/run.sh", cache)
			if err != nil {
				t.Fatalf("download: %v", err)
			}
			if copy.Path != "sub/dir/run.sh" {
				t.Errorf("path %q", copy.Path)
			}
			data, err := os.ReadFile(copy.Local)
			if err != nil {
				t.Fatalf("read copy: %v", err)
			}
			if string(data) != "line one\nline two\n" {
				t.Errorf("copy content %q", data)
			}
			if copy.Meta.Size != int64(len(data)) || copy.Meta.Mode != "755" {
				t.Errorf("meta %+v", copy.Meta)
			}
			sum := sha256.Sum256(data)
			if copy.Meta.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("meta hash %q", copy.Meta.SHA256)
			}
			st, err := os.Stat(copy.Local)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("copy mode %o", st.Mode().Perm())
			}
			if dirst, err := os.Stat(filepath.Dir(copy.Local)); err != nil || dirst.Mode().Perm() != 0o700 {
				t.Errorf("edit dir mode %v %v", err, dirst)
			}
			if cst, err := os.Stat(cache); err != nil || cst.Mode().Perm() != 0o700 {
				t.Errorf("cache dir mode %v %v", err, cst)
			}

			if err := os.WriteFile(copy.Local, []byte("edited\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			meta, err := repo.SaveEdited(context.Background(), copy)
			if err != nil {
				t.Fatalf("save: %v", err)
			}
			if got := readServerFile(t, repoDir, "sub/dir/run.sh"); string(got) != "edited\n" {
				t.Errorf("server content %q", got)
			}
			full := filepath.Join(repoDir, "sub/dir/run.sh")
			if st, err := os.Stat(full); err != nil || st.Mode().Perm() != 0o755 {
				t.Errorf("server mode %v %v", err, st)
			}
			if want := metaOfFile(t, full); want != meta {
				t.Errorf("returned meta %+v, file has %+v", meta, want)
			}
			if meta == copy.Meta {
				t.Errorf("metadata did not change after the save")
			}

			copy.Meta = meta
			if err := os.WriteFile(copy.Local, []byte("edited again\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			meta2, err := repo.SaveEdited(context.Background(), copy)
			if err != nil {
				t.Fatalf("second save: %v", err)
			}
			if got := readServerFile(t, repoDir, "sub/dir/run.sh"); string(got) != "edited again\n" {
				t.Errorf("server content after second save %q", got)
			}
			if want := metaOfFile(t, full); want != meta2 {
				t.Errorf("second returned meta %+v, file has %+v", meta2, want)
			}

			if gnu {
				log, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatalf("gnu shim log: %v", err)
				}
				if !strings.Contains(string(log), "stat") || !strings.Contains(string(log), "sha256sum") {
					t.Errorf("gnu shims did not run: %q", log)
				}
			}
		})
	}
}

// TestRemoteEditRepoPathForms covers a repository addressed as ~/repo under
// the login's home and one at an absolute path holding spaces.
func TestRemoteEditRepoPathForms(t *testing.T) {
	for _, tc := range []struct{ name, dir string }{
		{"tilde", "~/repo"},
		{"absolute with spaces", ""}, // replaced below
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, repoDir := newFakeRemote(t)
			dir := tc.dir
			root := repoDir
			if dir == "~/repo" {
				root = filepath.Join(ex.home, "repo")
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				dir = repoDir
			}
			ex.dir = dir
			writeServerFile(t, root, "a.txt", []byte("alpha\n"), 0o644)
			repo := New(ex)
			copy, err := repo.DownloadForEdit(context.Background(), "a.txt", t.TempDir())
			if err != nil {
				t.Fatalf("download via %q: %v", dir, err)
			}
			if err := os.WriteFile(copy.Local, []byte("beta\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.SaveEdited(context.Background(), copy); err != nil {
				t.Fatalf("save via %q: %v", dir, err)
			}
			if got := readServerFile(t, root, "a.txt"); string(got) != "beta\n" {
				t.Errorf("server content %q", got)
			}
		})
	}
}

// TestRemoteEditFilenames downloads and writes back files whose names hold
// spaces, quotes, a command substitution, a leading dash, and non-ASCII
// bytes. The substitution must stay a file name: no "pwned" file may appear.
func TestRemoteEditFilenames(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	names := []string{
		"with space.txt",
		"with'quote.txt",
		"$(touch pwned)",
		"-leading-dash.txt",
		"中文文件.txt",
		"sub dir/nested name.txt",
	}
	repo := New(ex)
	for _, name := range names {
		writeServerFile(t, repoDir, name, []byte("content of "+name+"\n"), 0o644)
		copy, err := repo.DownloadForEdit(context.Background(), name, t.TempDir())
		if err != nil {
			t.Fatalf("download %q: %v", name, err)
		}
		if filepath.Base(copy.Local) != filepath.Base(filepath.FromSlash(name)) {
			t.Errorf("copy name %q for %q", copy.Local, name)
		}
		if err := os.WriteFile(copy.Local, []byte("edited "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SaveEdited(context.Background(), copy); err != nil {
			t.Fatalf("save %q: %v", name, err)
		}
		if got := readServerFile(t, repoDir, name); string(got) != "edited "+name+"\n" {
			t.Errorf("server content of %q: %q", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(repoDir, "pwned")); !os.IsNotExist(err) {
		t.Errorf("command substitution created a file: %v", err)
	}
}

// TestRemoteEditChangedOnServer writes the file on the server after the
// download — content, only the modification time, or only the permission
// bits — and each write back must fail with ErrRemoteChanged, leaving the
// server's file and the local copy untouched.
func TestRemoteEditChangedOnServer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, repoDir string)
	}{
		{"content", func(t *testing.T, repoDir string) {
			writeServerFile(t, repoDir, "f.txt", []byte("someone else\n"), 0o644)
		}},
		{"mtime", func(t *testing.T, repoDir string) {
			out, err := exec.Command("/usr/bin/touch", "-t", "200101010000", filepath.Join(repoDir, "f.txt")).CombinedOutput()
			if err != nil {
				t.Fatalf("touch: %v %s", err, out)
			}
		}},
		{"mode", func(t *testing.T, repoDir string) {
			if err := os.Chmod(filepath.Join(repoDir, "f.txt"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, repoDir := newFakeRemote(t)
			writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
			repo := New(ex)
			copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
			if err != nil {
				t.Fatalf("download: %v", err)
			}
			tc.alter(t, repoDir)
			if err := os.WriteFile(copy.Local, []byte("my edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = repo.SaveEdited(context.Background(), copy)
			if !errors.Is(err, ErrRemoteChanged) {
				t.Fatalf("save after %s change: %v", tc.name, err)
			}
			st, statErr := os.Stat(filepath.Join(repoDir, "f.txt"))
			if statErr != nil {
				t.Fatal(statErr)
			}
			got := readServerFile(t, repoDir, "f.txt")
			switch tc.name {
			case "content":
				if string(got) != "someone else\n" {
					t.Errorf("server content overwritten: %q", got)
				}
			default:
				if string(got) != "original\n" {
					t.Errorf("server content overwritten: %q", got)
				}
			}
			if tc.name == "mode" && st.Mode().Perm() != 0o600 {
				t.Errorf("server mode changed: %o", st.Mode().Perm())
			}
			if _, err := os.Stat(copy.Local); err != nil {
				t.Errorf("local copy removed: %v", err)
			}
		})
	}
}

// TestRemoteEditLateChange writes the server file between the first metadata
// check of the save and the rename: a cat shim copies the upload, then edits
// the original. The second check must catch it and the write must not land.
func TestRemoteEditLateChange(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
	repo := New(ex)
	copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := os.WriteFile(copy.Local, []byte("my edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repoDir, "f.txt")
	catShim := "#!/bin/sh\n" +
		"/bin/cat \"$@\"\n" +
		"printf 'intruder\\n' > " + ShellQuote(target) + "\n"
	ex.prependPath(writeShim(t, "cat", catShim))
	_, err = repo.SaveEdited(context.Background(), copy)
	if !errors.Is(err, ErrRemoteChanged) {
		t.Fatalf("save with a late change: %v", err)
	}
	if got := readServerFile(t, repoDir, "f.txt"); string(got) != "intruder\n" {
		t.Errorf("the late change was overwritten: %q", got)
	}
	if left := remoteTmpFiles(t, repoDir); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// TestRemoteEditSymlinks refuses downloads and writes when the file itself,
// a directory on the way, or the repository root is a symbolic link.
func TestRemoteEditSymlinks(t *testing.T) {
	setup := func(t *testing.T) (ex *fakeSSHExecutor, repoDir string) {
		ex, repoDir = newFakeRemote(t)
		writeServerFile(t, repoDir, "real/inner.txt", []byte("inner\n"), 0o644)
		writeServerFile(t, repoDir, "plain.txt", []byte("plain\n"), 0o644)
		return ex, repoDir
	}

	t.Run("file", func(t *testing.T) {
		ex, repoDir := setup(t)
		if err := os.Symlink(filepath.Join(repoDir, "plain.txt"), filepath.Join(repoDir, "link.txt")); err != nil {
			t.Fatal(err)
		}
		repo := New(ex)
		if _, err := repo.DownloadForEdit(context.Background(), "link.txt", t.TempDir()); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("download through a file link: %v", err)
		}
		if _, err := repo.SaveEdited(context.Background(), manualCopy(t, "link.txt", []byte("x"))); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("save through a file link: %v", err)
		}
		if got := readServerFile(t, repoDir, "plain.txt"); string(got) != "plain\n" {
			t.Errorf("link target written: %q", got)
		}
	})

	t.Run("directory", func(t *testing.T) {
		ex, repoDir := setup(t)
		if err := os.Symlink(filepath.Join(repoDir, "real"), filepath.Join(repoDir, "linkdir")); err != nil {
			t.Fatal(err)
		}
		repo := New(ex)
		if _, err := repo.DownloadForEdit(context.Background(), "linkdir/inner.txt", t.TempDir()); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("download through a directory link: %v", err)
		}
		if _, err := repo.SaveEdited(context.Background(), manualCopy(t, "linkdir/inner.txt", []byte("x"))); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("save through a directory link: %v", err)
		}
		if got := readServerFile(t, repoDir, "real/inner.txt"); string(got) != "inner\n" {
			t.Errorf("file behind the link written: %q", got)
		}
		if left := remoteTmpFiles(t, filepath.Join(repoDir, "real")); len(left) != 0 {
			t.Errorf("temporary files left: %v", left)
		}
	})

	t.Run("root", func(t *testing.T) {
		ex, repoDir := setup(t)
		linkRoot := filepath.Join(filepath.Dir(repoDir), "rootlink")
		if err := os.Symlink(repoDir, linkRoot); err != nil {
			t.Fatal(err)
		}
		ex.dir = linkRoot
		repo := New(ex)
		if _, err := repo.DownloadForEdit(context.Background(), "plain.txt", t.TempDir()); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("download with a linked root: %v", err)
		}
		if _, err := repo.SaveEdited(context.Background(), manualCopy(t, "plain.txt", []byte("x"))); !errors.Is(err, ErrRemoteSymlink) {
			t.Errorf("save with a linked root: %v", err)
		}
		if got := readServerFile(t, repoDir, "plain.txt"); string(got) != "plain\n" {
			t.Errorf("file behind the linked root written: %q", got)
		}
	})
}

// TestRemoteEditPathValidation rejects empty, absolute, escaping and
// NUL-holding paths before any command reaches the server, and refuses
// executors that are not SSH shells.
func TestRemoteEditPathValidation(t *testing.T) {
	ex, _ := newFakeRemote(t)
	repo := New(ex)
	bad := []string{"", "/etc/passwd", "..", "../x", "a/../b", "a//b", "a/./b", ".", "a/", "nul\x00byte"}
	for _, path := range bad {
		if _, err := repo.DownloadForEdit(context.Background(), path, t.TempDir()); err == nil {
			t.Errorf("download %q: no error", path)
		}
		if _, err := repo.SaveEdited(context.Background(), manualCopy(t, path, []byte("x"))); err == nil {
			t.Errorf("save %q: no error", path)
		}
	}
	if n := ex.commandCount(); n != 0 {
		t.Errorf("%d commands reached the server for invalid paths", n)
	}

	local := New(&LocalExecutor{Dir: t.TempDir()})
	if _, err := local.DownloadForEdit(context.Background(), "a.txt", t.TempDir()); !errors.Is(err, ErrNotSSHRepository) {
		t.Errorf("local download: %v", err)
	}
	if _, err := local.SaveEdited(context.Background(), manualCopy(t, "a.txt", []byte("x"))); !errors.Is(err, ErrNotSSHRepository) {
		t.Errorf("local save: %v", err)
	}
}

// TestRemoteEditMissingAndNotRegular maps a missing file, a directory and an
// unreadable file to ErrRemoteNotRegular.
func TestRemoteEditMissingAndNotRegular(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "adir/inner.txt", []byte("x"), 0o644)
	writeServerFile(t, repoDir, "plain.txt", []byte("plain\n"), 0o644)
	repo := New(ex)
	if _, err := repo.DownloadForEdit(context.Background(), "missing.txt", t.TempDir()); !errors.Is(err, ErrRemoteNotRegular) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := repo.DownloadForEdit(context.Background(), "adir", t.TempDir()); !errors.Is(err, ErrRemoteNotRegular) {
		t.Errorf("directory: %v", err)
	}
	if _, err := repo.SaveEdited(context.Background(), manualCopy(t, "adir", []byte("x"))); !errors.Is(err, ErrRemoteNotRegular) {
		t.Errorf("save over a directory: %v", err)
	}
}

// TestRemoteEditTempFileFailure breaks mktemp with a shim: the save must
// report ErrRemoteTempFile, write nothing, and leave no temporary file.
func TestRemoteEditTempFileFailure(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
	repo := New(ex)
	copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := os.WriteFile(copy.Local, []byte("my edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex.prependPath(writeShim(t, "mktemp", "#!/bin/sh\nexit 1\n"))
	_, err = repo.SaveEdited(context.Background(), copy)
	if !errors.Is(err, ErrRemoteTempFile) {
		t.Fatalf("save with broken mktemp: %v", err)
	}
	if got := readServerFile(t, repoDir, "f.txt"); string(got) != "original\n" {
		t.Errorf("server content written: %q", got)
	}
	if left := remoteTmpFiles(t, repoDir); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// TestRemoteEditUploadCorrupt truncates the upload with a cat shim: the
// server must drop the incomplete temporary file and change nothing.
func TestRemoteEditUploadCorrupt(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
	repo := New(ex)
	copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := os.WriteFile(copy.Local, []byte("a much longer edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex.prependPath(writeShim(t, "cat", "#!/bin/sh\nexec /usr/bin/head -c 2\n"))
	_, err = repo.SaveEdited(context.Background(), copy)
	if !errors.Is(err, ErrRemoteUploadCorrupt) {
		t.Fatalf("save with truncated upload: %v", err)
	}
	if got := readServerFile(t, repoDir, "f.txt"); string(got) != "original\n" {
		t.Errorf("server content written: %q", got)
	}
	if left := remoteTmpFiles(t, repoDir); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// TestRemoteEditMetaAfterSave breaks reading the new metadata: an mv shim
// moves the temporary file into place and then deletes the target, so the
// write has happened but describing it fails. The save must report that the
// file was saved but its metadata could not be read.
func TestRemoteEditMetaAfterSave(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
	repo := New(ex)
	copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := os.WriteFile(copy.Local, []byte("my edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mvShim := "#!/bin/sh\n" +
		"for last in \"$@\"; do :; done\n" +
		"/bin/mv \"$@\" || exit $?\n" +
		"rm -f \"$last\"\n"
	ex.prependPath(writeShim(t, "mv", mvShim))
	_, err = repo.SaveEdited(context.Background(), copy)
	if !errors.Is(err, ErrRemoteMetaAfterSave) {
		t.Fatalf("save with unreadable new metadata: %v", err)
	}
	if !strings.Contains(err.Error(), "saved") {
		t.Errorf("error does not say the file was saved: %q", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoDir, "f.txt")); !os.IsNotExist(statErr) {
		t.Errorf("the shim should have deleted the saved file: %v", statErr)
	}
}

// TestRemoteEditCancelMidUpload uploads 16 MiB through a cat shim that stops
// reading after 4 bytes until a release file appears. Once the temporary
// file holds exactly those 4 bytes and the call is still blocked, the
// context is cancelled; after the error returns and the shim is released,
// the temporary file must be gone and the original unchanged.
func TestRemoteEditCancelMidUpload(t *testing.T) {
	ex, repoDir := newFakeRemote(t)
	writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
	repo := New(ex)
	copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	big := make([]byte, 16*1024*1024)
	for i := range big {
		big[i] = byte(i * 31)
	}
	if err := os.WriteFile(copy.Local, big, 0o600); err != nil {
		t.Fatal(err)
	}

	release := filepath.Join(t.TempDir(), "release")
	catShim := "#!/bin/sh\n" +
		"/usr/bin/head -c 4\n" +
		"n=0\n" +
		"while [ ! -f " + ShellQuote(release) + " ]; do\n" +
		"  n=$((n+1))\n" +
		"  [ \"$n\" -gt 1500 ] && exit 1\n" +
		"  /bin/sleep 0.2\n" +
		"done\n" +
		"exit 0\n"
	ex.prependPath(writeShim(t, "cat", catShim))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type saveResult struct {
		meta RemoteFileMeta
		err  error
	}
	done := make(chan saveResult, 1)
	go func() {
		meta, err := repo.SaveEdited(ctx, copy)
		done <- saveResult{meta, err}
	}()

	// Wait until the temporary file exists and holds exactly the 4 bytes the
	// shim let through.
	pollDeadline := time.Now().Add(10 * time.Second)
	var tmpPath string
	var tmpSize int64 = -1
	for time.Now().Before(pollDeadline) {
		left := remoteTmpFiles(t, repoDir)
		if len(left) == 1 {
			tmpPath = filepath.Join(repoDir, left[0])
			if st, err := os.Stat(tmpPath); err == nil {
				tmpSize = st.Size()
				if tmpSize == 4 {
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if tmpSize != 4 {
		t.Fatalf("temporary file %q size %d, want 4", tmpPath, tmpSize)
	}
	select {
	case r := <-done:
		t.Fatalf("save returned before cancellation: %v", r.err)
	default:
	}

	cancel()
	var res saveResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("save did not return after cancellation")
	}
	if res.err == nil {
		t.Fatalf("cancelled save succeeded with %+v", res.meta)
	}

	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if len(remoteTmpFiles(t, repoDir)) == 0 {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		t.Errorf("temporary file never removed")
	}
	if got := readServerFile(t, repoDir, "f.txt"); string(got) != "original\n" {
		t.Errorf("server content written: %q", got)
	}
}

// TestRemoteEditSaveSignals runs the exact command the save sends to the
// server — the same script text, wrapped for /bin/sh as in production —
// signals it with HUP, INT and TERM while it reads standard input, then
// closes the input: the script must exit 129, 130 or 143, remove the
// temporary file and leave the original alone.
func TestRemoteEditSaveSignals(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		want int
	}{
		{"hup", syscall.SIGHUP, 129},
		{"int", syscall.SIGINT, 130},
		{"term", syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, repoDir := newFakeRemote(t)
			writeServerFile(t, repoDir, "f.txt", []byte("original\n"), 0o644)
			repo := New(ex)
			copy, err := repo.DownloadForEdit(context.Background(), "f.txt", t.TempDir())
			if err != nil {
				t.Fatalf("download: %v", err)
			}
			content := []byte("replacement content\n")
			if err := os.WriteFile(copy.Local, content, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			script := remoteShellCommand(remoteEditSaveScript(ex.DirPath(), copy.Path, copy.Meta, int64(len(content)), hex.EncodeToString(sum[:]), copy.Meta.Mode))

			cmd := exec.Command("/bin/sh", "-c", script)
			cmd.Env = []string{"HOME=" + ex.home, "PATH=/usr/bin:/bin", "LC_ALL=C"}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			pid := cmd.Process.Pid

			// Feed half the content and wait until it has been copied into
			// the temporary file: the script is now blocked reading stdin.
			half := content[:len(content)/2]
			if _, err := stdin.Write(half); err != nil {
				t.Fatalf("feed stdin: %v", err)
			}
			copied := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				for _, name := range remoteTmpFiles(t, repoDir) {
					if st, err := os.Stat(filepath.Join(repoDir, name)); err == nil && st.Size() == int64(len(half)) {
						copied = true
					}
				}
				if copied {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !copied {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("temporary file never held the partial content; stderr %q", stderr.String())
			}

			if err := syscall.Kill(pid, tc.sig); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("signal %v: %v", tc.sig, err)
			}
			// The trap waits for the foreground cat; ending stdin lets cat
			// finish so the trap can run.
			if err := stdin.Close(); err != nil {
				t.Fatalf("close stdin: %v", err)
			}
			waitErr := cmd.Wait()
			code := -1
			var ee *exec.ExitError
			if errors.As(waitErr, &ee) {
				code = ee.ExitCode()
			}
			if code != tc.want {
				t.Errorf("exit code %d, want %d (err %v, stderr %q)", code, tc.want, waitErr, stderr.String())
			}
			if left := remoteTmpFiles(t, repoDir); len(left) != 0 {
				t.Errorf("temporary files left: %v", left)
			}
			if got := readServerFile(t, repoDir, "f.txt"); string(got) != "original\n" {
				t.Errorf("server content written: %q", got)
			}
		})
	}
}
