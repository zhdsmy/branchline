package git_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/sshtest"
)

// TestRemoteEditLoopbackSSH runs download, write-back, conflicts, symbolic
// links and permission handling against a real sshd on loopback.
func TestRemoteEditLoopbackSSH(t *testing.T) {
	srv := sshtest.StartLoopback(t)
	runRemoteEditOverSSH(t, srv)

	// UnwritableDirectory runs only on loopback: the configured Linux
	// environments are root logins, and root writes past directory permission
	// bits (CAP_DAC_OVERRIDE), so the write failure cannot be exercised there.
	t.Run("UnwritableDirectory", func(t *testing.T) {
		runRemoteEditUnwritableDirectory(t, srv)
	})
}

// TestConfiguredLinuxRemoteEdit runs the same remote-editing checks against
// the configured Linux SSH environment (sshlab E1/E2/E3) via internal/sshtest.
func TestConfiguredLinuxRemoteEdit(t *testing.T) {
	srv := sshtest.ConfiguredLinux(t)
	runRemoteEditOverSSH(t, srv)
}

// remoteEditServerStat reads "size mode mtime" of one server file the way the
// server scripts do: GNU stat first, BSD stat second.
func remoteEditServerStat(t *testing.T, srv *sshtest.Target, full string) string {
	t.Helper()
	q := git.ShellQuote(full)
	return srv.RunShell(t, "stat -c '%s %a %Y' "+q+" 2>/dev/null || stat -f '%z %Lp %m' "+q)
}

// remoteEditServerLeftovers lists leftover ".branchline-*" temporary files in
// one server directory.
func remoteEditServerLeftovers(t *testing.T, srv *sshtest.Target, dir string) []string {
	t.Helper()
	out := srv.RunShell(t, "cd "+git.ShellQuote(dir)+" || exit 3; set -- .branchline-*; if [ -e \"$1\" ]; then printf '%s\\n' \"$@\"; fi")
	return strings.Fields(out)
}

// requireRemoteEditStatMatchesServer checks that the metadata returned by a
// save matches the server's stat of the file.
func requireRemoteEditStatMatchesServer(t *testing.T, srv *sshtest.Target, full string, meta git.RemoteFileMeta) {
	t.Helper()
	out := remoteEditServerStat(t, srv, full)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 3 {
		t.Fatalf("server stat output %q", out)
	}
	got := fields[0] + " " + fields[1] + " " + fields[2]
	want := strconv.FormatInt(meta.Size, 10) + " " + meta.Mode + " " + strconv.FormatInt(meta.MTime, 10)
	if got != want {
		t.Errorf("returned meta %+v, server stat %q", meta, out)
	}
}

// makeRemoteEditCopy builds a RemoteCopy of content whose remembered metadata
// matches nothing on any server: server-side checks (symbolic links, changed
// files) must fire before the metadata comparison ever passes.
func makeRemoteEditCopy(t *testing.T, path string, content []byte) git.RemoteCopy {
	t.Helper()
	local := filepath.Join(t.TempDir(), "copy")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return git.RemoteCopy{
		Path:  path,
		Local: local,
		Meta:  git.RemoteFileMeta{Size: int64(len(content)), Mode: "644", MTime: 1, SHA256: hex.EncodeToString(sum[:])},
	}
}

func runRemoteEditOverSSH(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	// RoundTrip downloads one file, writes an edited copy back, and checks the
	// server holds the new bytes, the permission bits survived, and the
	// returned metadata is what the server's stat reports.
	t.Run("RoundTrip", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-loop")
		srv.WriteFile(t, dir, "src/edit me.txt", []byte("before\n"))
		srv.RunShell(t, "chmod 755 "+git.ShellQuote(filepath.Join(dir, "src/edit me.txt")))

		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")
		copy, err := r.DownloadForEdit(context.Background(), "src/edit me.txt", cache)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		data, err := os.ReadFile(copy.Local)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "before\n" {
			t.Errorf("copy content %q", data)
		}
		if copy.Meta.Mode != "755" {
			t.Errorf("remote mode %q", copy.Meta.Mode)
		}
		if copy.Meta.Size != int64(len(data)) {
			t.Errorf("remote size %d, copy has %d bytes", copy.Meta.Size, len(data))
		}
		sum := sha256.Sum256(data)
		if copy.Meta.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("remote hash %q, copy hashes to %q", copy.Meta.SHA256, hex.EncodeToString(sum[:]))
		}

		if err := os.WriteFile(copy.Local, []byte("after\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		meta, err := r.SaveEdited(context.Background(), copy)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if got := srv.ReadFile(t, dir, "src/edit me.txt"); string(got) != "after\n" {
			t.Errorf("server content %q", got)
		}
		requireRemoteEditStatMatchesServer(t, srv, filepath.Join(dir, "src/edit me.txt"), meta)
	})

	// ConflictOnServer writes the file on the server after the download; the
	// next save must be refused with ErrRemoteChanged and the server's bytes
	// must stay untouched.
	t.Run("ConflictOnServer", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-conflict")
		srv.WriteFile(t, dir, "src/edit me.txt", []byte("before\n"))

		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")
		copy, err := r.DownloadForEdit(context.Background(), "src/edit me.txt", cache)
		if err != nil {
			t.Fatalf("download: %v", err)
		}

		// A write on the server after the download must block the next save.
		srv.WriteFile(t, dir, "src/edit me.txt", []byte("someone else\n"))
		if err := os.WriteFile(copy.Local, []byte("conflicting\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SaveEdited(context.Background(), copy); !errors.Is(err, git.ErrRemoteChanged) {
			t.Fatalf("conflicting save: %v", err)
		}
		if got := srv.ReadFile(t, dir, "src/edit me.txt"); string(got) != "someone else\n" {
			t.Errorf("conflict overwritten: %q", got)
		}
	})

	// QuotedPath exercises a path carrying single quotes in both directory and
	// file name; it must survive the login shell's quoting around `/bin/sh -c`.
	t.Run("QuotedPath", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-quoted")
		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")

		quotedPath := "dir's/it's quoted.txt"
		srv.WriteFile(t, dir, quotedPath, []byte("quoted before\n"))
		qCopy, err := r.DownloadForEdit(context.Background(), quotedPath, cache)
		if err != nil {
			t.Fatalf("download %q: %v", quotedPath, err)
		}
		if err := os.WriteFile(qCopy.Local, []byte("quoted after\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SaveEdited(context.Background(), qCopy); err != nil {
			t.Fatalf("save %q: %v", quotedPath, err)
		}
		if got := srv.ReadFile(t, dir, quotedPath); string(got) != "quoted after\n" {
			t.Errorf("quoted server content %q", got)
		}
	})

	// SymlinkedPath refuses downloads and writes through symbolic links: the
	// file itself, or a directory on the way. The data behind the links must
	// never be read or written.
	t.Run("SymlinkedPath", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-links")
		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")

		srv.WriteFile(t, dir, "realdir/inner.txt", []byte("inner\n"))
		srv.WriteFile(t, dir, "plain.txt", []byte("plain\n"))

		// The file itself being a link must refuse both directions.
		srv.RunShell(t, "ln -s "+git.ShellQuote(filepath.Join(dir, "plain.txt"))+" "+git.ShellQuote(filepath.Join(dir, "link.txt")))
		if _, err := r.DownloadForEdit(context.Background(), "link.txt", cache); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("download through a file link: %v", err)
		}
		if _, err := r.SaveEdited(context.Background(), makeRemoteEditCopy(t, "link.txt", []byte("written x\n"))); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("save through a file link: %v", err)
		}
		if got := srv.ReadFile(t, dir, "plain.txt"); string(got) != "plain\n" {
			t.Errorf("link target written: %q", got)
		}

		// A directory in the path being a link must refuse both directions too.
		srv.RunShell(t, "ln -s "+git.ShellQuote(filepath.Join(dir, "realdir"))+" "+git.ShellQuote(filepath.Join(dir, "linkdir")))
		if _, err := r.DownloadForEdit(context.Background(), "linkdir/inner.txt", cache); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("download through a directory link: %v", err)
		}
		if _, err := r.SaveEdited(context.Background(), makeRemoteEditCopy(t, "linkdir/inner.txt", []byte("written y\n"))); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("save through a directory link: %v", err)
		}
		if got := srv.ReadFile(t, dir, "realdir/inner.txt"); string(got) != "inner\n" {
			t.Errorf("file behind the linked directory written: %q", got)
		}
		if left := remoteEditServerLeftovers(t, srv, filepath.Join(dir, "realdir")); len(left) != 0 {
			t.Errorf("temporary files left: %v", left)
		}
	})

	// TrailingSlashRoot uses a repository addressed with a trailing slash on
	// its root: downloads and writes must work as usual, and the symbolic-link
	// refusals must apply exactly as they do without the slash — including
	// when the root entry itself is a link, which a naive `-L` test would
	// miss because the trailing slash dereferences it.
	t.Run("TrailingSlashRoot", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-tslash")
		srv.WriteFile(t, dir, "f.txt", []byte("slash before\n"))
		srv.WriteFile(t, dir, "plain.txt", []byte("plain\n"))
		srv.WriteFile(t, dir, "realdir/inner.txt", []byte("inner\n"))

		r := srv.Repo(t, dir+"/")
		cache := filepath.Join(t.TempDir(), "cache")
		copy, err := r.DownloadForEdit(context.Background(), "f.txt", cache)
		if err != nil {
			t.Fatalf("download with trailing-slash root: %v", err)
		}
		if err := os.WriteFile(copy.Local, []byte("slash after\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SaveEdited(context.Background(), copy); err != nil {
			t.Fatalf("save with trailing-slash root: %v", err)
		}
		if got := srv.ReadFile(t, dir, "f.txt"); string(got) != "slash after\n" {
			t.Errorf("server content %q", got)
		}

		// A linked directory below a trailing-slash root must still refuse.
		srv.RunShell(t, "ln -s "+git.ShellQuote(filepath.Join(dir, "realdir"))+" "+git.ShellQuote(filepath.Join(dir, "linkdir")))
		if _, err := r.DownloadForEdit(context.Background(), "linkdir/inner.txt", cache); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("download through a linked directory under a trailing-slash root: %v", err)
		}
		if _, err := r.SaveEdited(context.Background(), makeRemoteEditCopy(t, "linkdir/inner.txt", []byte("written y\n"))); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("save through a linked directory under a trailing-slash root: %v", err)
		}
		if got := srv.ReadFile(t, dir, "realdir/inner.txt"); string(got) != "inner\n" {
			t.Errorf("file behind the linked directory written: %q", got)
		}

		// A symbolic link as the repository root itself must refuse even when
		// addressed with a trailing slash: without trimming the slash first,
		// `[ -L root/ ]` resolves through the link and reports nothing.
		linkParent := srv.TempDir(t, "re-rootlink-parent")
		linkRoot := linkParent + "/rootlink"
		srv.RunShell(t, "ln -s "+git.ShellQuote(dir)+" "+git.ShellQuote(linkRoot))
		lr := srv.Repo(t, linkRoot+"/")
		if _, err := lr.DownloadForEdit(context.Background(), "f.txt", cache); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("download with trailing-slash linked root: %v", err)
		}

		// For the write-back direction the root must become a link only after
		// the download, or the write never gets past the first link check.
		// Moving the real directory aside and putting a link in its place
		// exercises exactly the check the trailing slash would defeat.
		srv.WriteFile(t, dir, "w.txt", []byte("w before\n"))
		wCopy, err := r.DownloadForEdit(context.Background(), "w.txt", cache)
		if err != nil {
			t.Fatalf("download before the root swap: %v", err)
		}
		if err := os.WriteFile(wCopy.Local, []byte("w after\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		aside := linkParent + "/aside"
		srv.RunShell(t, "mv "+git.ShellQuote(dir)+" "+git.ShellQuote(aside)+" && ln -s "+git.ShellQuote(aside)+" "+git.ShellQuote(dir))
		if _, err := r.SaveEdited(context.Background(), wCopy); !errors.Is(err, git.ErrRemoteSymlink) {
			t.Errorf("save after the root became a link: %v", err)
		}
		srv.RunShell(t, "rm "+git.ShellQuote(dir)+" && mv "+git.ShellQuote(aside)+" "+git.ShellQuote(dir))
		if got := srv.ReadFile(t, dir, "w.txt"); string(got) != "w before\n" {
			t.Errorf("file behind the linked root written: %q", got)
		}
	})

	// LargeFile round-trips a 4 MiB file: the downloaded bytes and the bytes
	// on the server after the write-back must hash to what was sent.
	t.Run("LargeFile", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-large")
		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")

		original := make([]byte, 4*1024*1024)
		state := uint32(0x12345678)
		for i := range original {
			state = state*1664525 + 1013904223
			original[i] = byte(state >> 24)
		}
		srv.WriteFile(t, dir, "big.bin", original)

		copy, err := r.DownloadForEdit(context.Background(), "big.bin", cache)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		data, err := os.ReadFile(copy.Local)
		if err != nil {
			t.Fatal(err)
		}
		sumOriginal := sha256.Sum256(original)
		sumCopy := sha256.Sum256(data)
		if !bytes.Equal(data, original) {
			t.Errorf("downloaded content differs from the server file")
		}
		if copy.Meta.SHA256 != hex.EncodeToString(sumOriginal[:]) || copy.Meta.SHA256 != hex.EncodeToString(sumCopy[:]) {
			t.Errorf("remote hash %q, server content hashes to %q, copy hashes to %q", copy.Meta.SHA256, hex.EncodeToString(sumOriginal[:]), hex.EncodeToString(sumCopy[:]))
		}

		edited := make([]byte, len(original))
		state = 0xabcdef01
		for i := range edited {
			state = state*1664525 + 1013904223
			edited[i] = byte(state >> 24)
		}
		if err := os.WriteFile(copy.Local, edited, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SaveEdited(context.Background(), copy); err != nil {
			t.Fatalf("save: %v", err)
		}
		serverBytes := srv.ReadFile(t, dir, "big.bin")
		sumEdited := sha256.Sum256(edited)
		sumServer := sha256.Sum256(serverBytes)
		if hex.EncodeToString(sumServer[:]) != hex.EncodeToString(sumEdited[:]) {
			t.Errorf("server content hashes to %q, the edit hashes to %q", hex.EncodeToString(sumServer[:]), hex.EncodeToString(sumEdited[:]))
		}
	})

	// PermissionModes writes back files of mode 644 and 755: the permission
	// bits must survive the write, and the returned metadata must be what the
	// server's stat reports.
	t.Run("PermissionModes", func(t *testing.T) {
		dir := srv.InitRepo(t, "re-modes")
		r := srv.Repo(t, dir)
		cache := filepath.Join(t.TempDir(), "cache")

		for _, mode := range []string{"644", "755"} {
			rel := "m" + mode + ".txt"
			full := filepath.Join(dir, rel)
			srv.WriteFile(t, dir, rel, []byte("mode "+mode+" before\n"))
			srv.RunShell(t, "chmod "+mode+" "+git.ShellQuote(full))

			copy, err := r.DownloadForEdit(context.Background(), rel, cache)
			if err != nil {
				t.Fatalf("download %q: %v", rel, err)
			}
			if copy.Meta.Mode != mode {
				t.Errorf("remote mode of %q %q, want %q", rel, copy.Meta.Mode, mode)
			}
			if err := os.WriteFile(copy.Local, []byte("mode "+mode+" after\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			meta, err := r.SaveEdited(context.Background(), copy)
			if err != nil {
				t.Fatalf("save %q: %v", rel, err)
			}
			if meta.Mode != mode {
				t.Errorf("mode of %q after save %q, want %q", rel, meta.Mode, mode)
			}
			if got := srv.ReadFile(t, dir, rel); string(got) != "mode "+mode+" after\n" {
				t.Errorf("server content of %q %q", rel, got)
			}
			requireRemoteEditStatMatchesServer(t, srv, full, meta)
		}
	})
}

// runRemoteEditUnwritableDirectory removes the write permission of the file's
// directory before a save: the save must fail, the original content and
// permission bits must survive, and no temporary file may remain. It is kept
// out of runRemoteEditOverSSH because the configured Linux environments log
// in as root, and root writes past directory permission bits
// (CAP_DAC_OVERRIDE), so the write failure cannot be exercised there; the
// probe stays in place so the test also stays honest on any loopback login
// with such powers.
func runRemoteEditUnwritableDirectory(t *testing.T, srv *sshtest.Target) {
	t.Helper()

	dir := srv.InitRepo(t, "re-unwritable")
	r := srv.Repo(t, dir)
	cache := filepath.Join(t.TempDir(), "cache")

	srv.WriteFile(t, dir, "sub/f.txt", []byte("original\n"))
	subdir := filepath.Join(dir, "sub")
	srv.RunShell(t, "chmod 644 "+git.ShellQuote(filepath.Join(dir, "sub/f.txt")))

	copy, err := r.DownloadForEdit(context.Background(), "sub/f.txt", cache)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if err := os.WriteFile(copy.Local, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv.RunShell(t, "chmod 555 "+git.ShellQuote(subdir))
	// The restore is registered after InitRepo's cleanup of dir, so it
	// runs first even when the subtest fails partway through.
	t.Cleanup(func() {
		srv.RunShell(t, "chmod 755 "+git.ShellQuote(subdir))
	})

	probe := strings.TrimSpace(srv.RunShell(t, "if touch "+git.ShellQuote(filepath.Join(subdir, ".write-probe"))+" 2>/dev/null; then rm -f "+git.ShellQuote(filepath.Join(subdir, ".write-probe"))+"; echo writable; else echo readonly; fi"))
	if probe == "writable" {
		t.Skipf("the server login can write into a chmod 555 directory (root bypasses permission bits); the unwritable-directory behavior cannot be exercised here")
	}
	if probe != "readonly" {
		t.Fatalf("unexpected write probe result %q", probe)
	}

	_, err = r.SaveEdited(context.Background(), copy)
	if !errors.Is(err, git.ErrRemoteTempFile) {
		t.Errorf("save into an unwritable directory: %v", err)
	}
	if got := srv.ReadFile(t, dir, "sub/f.txt"); string(got) != "original\n" {
		t.Errorf("server content written: %q", got)
	}
	out := remoteEditServerStat(t, srv, filepath.Join(dir, "sub/f.txt"))
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 3 {
		t.Fatalf("server stat output %q", out)
	}
	if fields[1] != "644" {
		t.Errorf("server mode after failed save %q, want 644", fields[1])
	}
	if left := remoteEditServerLeftovers(t, srv, subdir); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
