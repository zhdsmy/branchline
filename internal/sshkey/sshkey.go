// Package sshkey lists, reads and generates SSH key pairs in a folder.
// It is the base for choosing and creating the keys offered to SSH remotes.
package sshkey

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Key is one SSH key pair: a private key file and its public half.
type Key struct {
	Name        string // file name of the private key
	Path        string // absolute path of the private key
	Public      string // first line of the .pub file
	Type        string // key type, for example ssh-ed25519
	Comment     string // comment after the base64 part of the public line
	Fingerprint string // SHA256 fingerprint as ssh-keygen -lf prints it
}

// ErrExists says a key file of the same name already exists and nothing was
// overwritten.
var ErrExists = errors.New("a key file with this name already exists")

// Dir is the folder SSH keys are usually kept in. It is not checked to
// exist.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh"), nil
}

// Open reads the key pair at path, which can be the private key or its .pub
// file. Both files must be present. Of the private key only the first line
// is read: the rest may be encrypted and is never needed here.
func Open(path string) (Key, error) {
	priv, pub := keyPaths(path)

	privLine, err := firstLine(priv)
	if errors.Is(err, fs.ErrNotExist) {
		return Key{}, fmt.Errorf("there is no private key at %q", priv)
	}
	if err != nil {
		return Key{}, fmt.Errorf("cannot read the private key %q: %v", priv, err)
	}
	if !looksPrivate(privLine) {
		return Key{}, fmt.Errorf("the first line of %q is not a private key header", priv)
	}

	pubLine, err := firstLine(pub)
	if errors.Is(err, fs.ErrNotExist) {
		return Key{}, fmt.Errorf("the public key %q is missing", pub)
	}
	if err != nil {
		return Key{}, fmt.Errorf("cannot read the public key %q: %v", pub, err)
	}
	typ, blob, comment, err := parsePublicLine(pubLine)
	if err != nil {
		return Key{}, fmt.Errorf("the public key %q is not usable: %v", pub, err)
	}

	abs, err := filepath.Abs(priv)
	if err != nil {
		return Key{}, err
	}
	return Key{
		Name:        filepath.Base(priv),
		Path:        abs,
		Public:      pubLine,
		Type:        typ,
		Comment:     comment,
		Fingerprint: fingerprintBlob(blob),
	}, nil
}

// List reads every usable key pair in dir, sorted by file name. A .pub file
// counts only when the private key of the same name is present and the pair
// passes Open; subfolders and everything else are skipped. A folder that
// does not exist is empty, not an error.
func List(dir string) ([]Key, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the folder %q: %v", dir, err)
	}
	var keys []Key
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		k, err := Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b Key) int { return cmp.Compare(a.Name, b.Name) })
	return keys, nil
}

// Fingerprint is the SHA256 fingerprint of a public key line in the format
// ssh-keygen -lf prints, or empty when the line is not a public key.
func Fingerprint(line string) string {
	_, blob, _, err := parsePublicLine(line)
	if err != nil {
		return ""
	}
	return fingerprintBlob(blob)
}

// CheckName tells why name cannot be the file name of a new key.
func CheckName(name string) error {
	switch {
	case name == "":
		return errors.New("the key needs a file name")
	case strings.TrimSpace(name) != name:
		return errors.New("the file name cannot start or end with whitespace")
	case strings.ContainsRune(name, '/'):
		return errors.New("the file name cannot contain /")
	case strings.ContainsRune(name, 0):
		return errors.New("the file name cannot contain NUL")
	case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-"):
		return errors.New("the file name cannot start with . or -")
	case strings.HasSuffix(name, ".pub"):
		return errors.New("the file name of the private key cannot end in .pub")
	case len(name) > 255:
		return errors.New("the file name cannot be longer than 255 bytes")
	}
	return nil
}

// Generate runs keygen (the path of an ssh-keygen program) to create an
// ed25519 key pair named name in dir, with comment in the public line. The
// key has no passphrase, like the new keys of common Git clients; one can be
// added later with ssh-keygen -p. Existing files are never overwritten, and
// a failed run removes only the files it created itself.
func Generate(ctx context.Context, keygen, dir, name, comment string) (Key, error) {
	if err := CheckName(name); err != nil {
		return Key{}, err
	}
	priv := filepath.Join(dir, name)
	pub := priv + ".pub"
	for _, p := range []string{priv, pub} {
		if _, err := os.Lstat(p); err == nil {
			return Key{}, fmt.Errorf("%w: %q", ErrExists, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Key{}, fmt.Errorf("cannot check %q: %v", p, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Key{}, err
	}
	absPriv, err := filepath.Abs(priv)
	if err != nil {
		return Key{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Key{}, fmt.Errorf("cannot create the folder %q: %v", dir, err)
	}

	// Generate into a private temporary directory inside dir (same mount),
	// then publish each file with os.Link so a concurrent writer creating
	// the target name can neither be overwritten nor deleted on cleanup.
	// Standard input is the null device: no prompt of ssh-keygen can block,
	// and a canceled context kills it.
	tmpDir, err := os.MkdirTemp(dir, ".sshkey-gen-*")
	if err != nil {
		return Key{}, fmt.Errorf("cannot create a temporary folder in %q: %v", dir, err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	tmpPriv := filepath.Join(tmpDir, name)
	tmpPub := tmpPriv + ".pub"
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, keygen, "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", tmpPriv)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Key{}, fmt.Errorf("ssh-keygen did not create the key (%v): %s", err, msg)
		}
		return Key{}, fmt.Errorf("ssh-keygen did not create the key: %v", err)
	}

	st, err := os.Lstat(tmpPriv)
	if err != nil {
		return Key{}, fmt.Errorf("ssh-keygen did not create the private key %q", priv)
	}
	if !st.Mode().IsRegular() {
		return Key{}, fmt.Errorf("the new private key %q is not a regular file", priv)
	}
	if st.Mode().Perm() != 0o600 {
		return Key{}, fmt.Errorf("the new private key %q has permissions %o instead of 600", priv, st.Mode().Perm())
	}
	key, err := Open(tmpPriv)
	if err != nil {
		return Key{}, err
	}

	if err := os.Link(tmpPriv, priv); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Key{}, fmt.Errorf("%w: %q", ErrExists, priv)
		}
		return Key{}, fmt.Errorf("cannot install the private key %q: %v", priv, err)
	}
	if err := os.Link(tmpPub, pub); err != nil {
		_ = os.Remove(priv)
		if errors.Is(err, fs.ErrExist) {
			return Key{}, fmt.Errorf("%w: %q", ErrExists, pub)
		}
		return Key{}, fmt.Errorf("cannot install the public key %q: %v", pub, err)
	}
	key.Path = absPriv
	return key, nil
}

// keyPaths derives the private and public file paths from either one.
func keyPaths(path string) (priv, pub string) {
	if strings.HasSuffix(path, ".pub") {
		return strings.TrimSuffix(path, ".pub"), path
	}
	return path, path + ".pub"
}

// looksPrivate reports whether the first line of a file is a private key
// header, PEM or OpenSSH.
func looksPrivate(line string) bool {
	return strings.HasPrefix(line, "-----BEGIN ") && strings.Contains(line, "PRIVATE KEY")
}

// firstLine reads only the first line of a file, without its line ending.
func firstLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// cutField splits the first whitespace-separated field off s.
func cutField(s string) (field, rest string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimLeft(s[i:], " \t")
	}
	return s, ""
}

// parsePublicLine decodes a public key line of the form
// "<type> <base64> [comment]". The type inside the decoded blob must match
// the leading type, as ssh-keygen insists.
func parsePublicLine(line string) (typ string, blob []byte, comment string, err error) {
	typ, rest := cutField(strings.TrimRight(line, "\r\n"))
	if typ == "" {
		return "", nil, "", errors.New("the line is empty")
	}
	enc, rest := cutField(rest)
	if enc == "" {
		return "", nil, "", errors.New("the key data is missing")
	}
	blob, err = base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", nil, "", errors.New("the key data is not valid base64")
	}
	if inner := blobType(blob); inner != typ {
		return "", nil, "", fmt.Errorf("the key type %q does not match the type %q inside the key data", typ, inner)
	}
	return typ, blob, rest, nil
}

// blobType reads the type string an SSH public key blob starts with: a four
// byte length and that many bytes.
func blobType(blob []byte) string {
	if len(blob) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint32(blob))
	if len(blob) < 4+n {
		return ""
	}
	return string(blob[4 : 4+n])
}

// fingerprintBlob is the SHA256 fingerprint of a decoded public key blob:
// "SHA256:" and the standard base64 of the digest without padding, the
// format of ssh-keygen -lf.
func fingerprintBlob(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(sum[:])
}
