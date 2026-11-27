package sshkey

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func keygenPath(t *testing.T) string {
	t.Helper()
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatalf("ssh-keygen is required for these tests: %v", err)
	}
	return keygen
}

// writePair creates a key pair through Generate and returns it.
func writePair(t *testing.T, dir, name, comment string) Key {
	t.Helper()
	key, err := Generate(context.Background(), keygenPath(t), dir, name, comment)
	if err != nil {
		t.Fatalf("Generate(%q): %v", name, err)
	}
	return key
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A generated key is a complete pair: type, comment, permissions 0600 on the
// private key, and the fingerprint ssh-keygen -lf prints for it.
func TestSSHKeyGenerateAndRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "keys")
	key, err := Generate(context.Background(), keygenPath(t), dir, "id_work", "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "id_work" {
		t.Errorf("Name %q, want id_work", key.Name)
	}
	if key.Path != filepath.Join(dir, "id_work") {
		t.Errorf("Path %q, want %q", key.Path, filepath.Join(dir, "id_work"))
	}
	if key.Type != "ssh-ed25519" {
		t.Errorf("Type %q, want ssh-ed25519", key.Type)
	}
	if key.Comment != "me@example.com" {
		t.Errorf("Comment %q, want me@example.com", key.Comment)
	}
	if key.Public != strings.TrimRight(fileContent(t, filepath.Join(dir, "id_work.pub")), "\r\n") {
		t.Errorf("Public does not match the first line of the .pub file")
	}
	st, err := os.Stat(key.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("private key permissions %o, want 600", st.Mode().Perm())
	}
	sd, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sd.Mode().Perm() != 0o700 {
		t.Errorf("folder permissions %o, want 700", sd.Mode().Perm())
	}

	out, err := exec.Command(keygenPath(t), "-lf", filepath.Join(dir, "id_work.pub")).Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Fatalf("unexpected ssh-keygen -lf output %q", out)
	}
	if key.Fingerprint != fields[1] {
		t.Errorf("Fingerprint %q, ssh-keygen -lf prints %q", key.Fingerprint, fields[1])
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") || strings.Contains(key.Fingerprint, "=") {
		t.Errorf("Fingerprint %q is not the unpadded SHA256 format", key.Fingerprint)
	}
}

// Generating over an existing key file fails with ErrExists and changes
// nothing, whichever half of the pair is in the way.
func TestSSHKeyGenerateExisting(t *testing.T) {
	dir := t.TempDir()
	key := writePair(t, dir, "id_work", "first")
	privBefore := fileContent(t, key.Path)
	pubBefore := fileContent(t, key.Path+".pub")

	_, err := Generate(context.Background(), keygenPath(t), dir, "id_work", "second")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Generate: %v, want ErrExists", err)
	}
	if fileContent(t, key.Path) != privBefore || fileContent(t, key.Path+".pub") != pubBefore {
		t.Error("the existing key files changed")
	}

	// A lone .pub of the same name also blocks the name and stays untouched.
	lone := filepath.Join(dir, "lone.pub")
	if err := os.WriteFile(lone, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Generate(context.Background(), keygenPath(t), dir, "lone", "c")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("Generate over a lone .pub: %v, want ErrExists", err)
	}
	if fileContent(t, lone) != "keep me" {
		t.Error("the pre-existing .pub file changed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "lone")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a private key appeared next to the lone .pub")
	}
}

// When ssh-keygen fails after writing a partial file, the error carries its
// stderr, the partial files of this run are removed, and files that existed
// before stay untouched.
func TestSSHKeyGenerateFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fakekeygen")
	body := `#!/bin/sh
out=
while [ $# -gt 0 ]; do
  if [ "$1" = "-f" ]; then shift; out="$1"; fi
  shift
done
printf '%s\n' '-----BEGIN OPENSSH PRIVATE KEY-----' > "$out"
echo 'fake keygen failure message' >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Generate(context.Background(), script, dir, "broken", "c")
	if err == nil {
		t.Fatal("Generate with a failing keygen succeeded")
	}
	if !strings.Contains(err.Error(), "fake keygen failure message") {
		t.Errorf("error %q does not carry the keygen stderr", err)
	}
	for _, p := range []string{filepath.Join(dir, "broken"), filepath.Join(dir, "broken.pub")} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("partial file %q was not removed", p)
		}
	}

	// A neighbor file that existed before the run is never cleanup.
	keep := filepath.Join(dir, "other")
	if err := os.WriteFile(keep, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Generate(context.Background(), script, dir, "again", "c")
	if err == nil {
		t.Fatal("second failing Generate succeeded")
	}
	if fileContent(t, keep) != "keep me" {
		t.Error("the pre-existing neighbor file changed")
	}
}

// If another writer creates the target file or its .pub between the initial
// existence check and keygen finishing, Generate never deletes or overwrites
// that file, whether keygen fails or succeeds.
func TestSSHKeyGenerateConcurrentTargetPreserved(t *testing.T) {
	realKeygen := keygenPath(t)
	dir := t.TempDir()

	// Case 1: another writer creates the target private key while keygen runs
	// and fails; cleanup removes only the temporary generation files.
	failRace := filepath.Join(t.TempDir(), "failrace")
	failTarget := filepath.Join(dir, "raced_fail")
	failScript := "#!/bin/sh\n" +
		"printf 'external priv' > " + shellSingleQuote(failTarget) + "\n" +
		"echo 'simulated keygen error' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(failRace, []byte(failScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), failRace, dir, "raced_fail", "c"); err == nil {
		t.Fatal("Generate with failing racing keygen succeeded")
	}
	if got := fileContent(t, failTarget); got != "external priv" {
		t.Fatalf("concurrently created file = %q, want 'external priv'", got)
	}

	// Case 2: another writer creates the target private key while keygen
	// succeeds; Generate fails with ErrExists and leaves both target files safe.
	privRace := filepath.Join(t.TempDir(), "privrace")
	privTarget := filepath.Join(dir, "raced_priv")
	privScript := "#!/bin/sh\n" +
		"printf 'keep priv' > " + shellSingleQuote(privTarget) + "\n" +
		"exec " + shellSingleQuote(realKeygen) + " \"$@\"\n"
	if err := os.WriteFile(privRace, []byte(privScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), privRace, dir, "raced_priv", "c"); !errors.Is(err, ErrExists) {
		t.Fatalf("Generate racing on private key: %v, want ErrExists", err)
	}
	if got := fileContent(t, privTarget); got != "keep priv" {
		t.Fatalf("concurrently created private key = %q, want 'keep priv'", got)
	}
	if _, err := os.Lstat(privTarget + ".pub"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public key appeared despite private key collision: %v", err)
	}

	// Case 3: another writer creates the target .pub while keygen succeeds;
	// Generate fails with ErrExists, keeps the .pub, and rolls back the private key.
	pubRace := filepath.Join(t.TempDir(), "pubrace")
	pubTarget := filepath.Join(dir, "raced_pub.pub")
	pubScript := "#!/bin/sh\n" +
		"printf 'keep pub' > " + shellSingleQuote(pubTarget) + "\n" +
		"exec " + shellSingleQuote(realKeygen) + " \"$@\"\n"
	if err := os.WriteFile(pubRace, []byte(pubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(context.Background(), pubRace, dir, "raced_pub", "c"); !errors.Is(err, ErrExists) {
		t.Fatalf("Generate racing on public key: %v, want ErrExists", err)
	}
	if got := fileContent(t, pubTarget); got != "keep pub" {
		t.Fatalf("concurrently created public key = %q, want 'keep pub'", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "raced_pub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private key was not rolled back after .pub collision: %v", err)
	}

	// No temporary generation directories remain in dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sshkey-gen-") {
			t.Errorf("leftover temporary directory %q in %s", e.Name(), dir)
		}
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// A canceled context stops generation before anything is written.
func TestSSHKeyGenerateCanceled(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "keys")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Generate(ctx, keygenPath(t), dir, "id_work", "c")
	if err == nil {
		t.Fatal("Generate with a canceled context succeeded")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Error("the folder or files were created despite the canceled context")
	}
}

// List returns the complete pairs of a folder in name order and skips
// folders, broken or orphan .pub files, and files that are not keys at all.
func TestSSHKeyList(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "zeta", "z@example.com")
	writePair(t, dir, "alpha", "a@example.com")

	// An orphan .pub, a broken .pub, a folder, and the usual non-key files.
	if err := os.WriteFile(filepath.Join(dir, "orphan.pub"), []byte("ssh-ed25519 AAAA orphan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(dir, "broken")
	if err := os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv+".pub", []byte("not base64 at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"known_hosts", "config", "id_plain"} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "inner.pub"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	keys, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("List returned %d keys, want 2: %+v", len(keys), keys)
	}
	if keys[0].Name != "alpha" || keys[1].Name != "zeta" {
		t.Errorf("keys %q, %q, want alpha, zeta in order", keys[0].Name, keys[1].Name)
	}
	if keys[0].Comment != "a@example.com" || keys[1].Comment != "z@example.com" {
		t.Errorf("comments %q, %q", keys[0].Comment, keys[1].Comment)
	}

	keys, err = List(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("List of a missing folder: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List of a missing folder returned %d keys", len(keys))
	}
}

// Open accepts the private key path or the .pub path with the same result,
// and rejects pairs whose files are missing or inconsistent.
func TestSSHKeyOpen(t *testing.T) {
	dir := t.TempDir()
	key := writePair(t, dir, "id_work", "me@example.com")

	byPub, err := Open(key.Path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	byPriv, err := Open(key.Path)
	if err != nil {
		t.Fatal(err)
	}
	if byPub != byPriv {
		t.Errorf("opening the .pub path differs from the private key path:\n%+v\n%+v", byPub, byPriv)
	}
	byRel, err := Open(filepath.Join(dir, "id_work"))
	if err != nil || byRel != byPriv {
		t.Errorf("reopening failed: %v", err)
	}

	// A private key whose first line is not a key header.
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("just some text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad+".pub", []byte(strings.SplitN(key.Public, "\n", 2)[0]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bad); err == nil {
		t.Error("Open accepted a private key without a key header")
	} else if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error %q does not name the file", err)
	}

	// A private key without its .pub file.
	lonely := filepath.Join(dir, "lonely")
	if err := os.WriteFile(lonely, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(lonely); err == nil {
		t.Error("Open accepted a private key without a .pub file")
	} else if !strings.Contains(err.Error(), "lonely.pub") {
		t.Errorf("error %q does not name the missing .pub", err)
	}

	// A .pub file without its private key.
	if err := os.WriteFile(filepath.Join(dir, "orphan.pub"), []byte(key.Public+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "orphan.pub")); err == nil {
		t.Error("Open accepted a .pub file without a private key")
	}

	// A .pub whose type does not match the type inside the key data.
	blob := append([]byte{0, 0, 0, 7}, []byte("ssh-rsa")...)
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " c\n"
	wrong := filepath.Join(dir, "wrongtype")
	if err := os.WriteFile(wrong, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrong+".pub", []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(wrong); err == nil {
		t.Error("Open accepted a key type that contradicts the key data")
	}
}

// Fingerprint is empty for lines that are not public keys and stable for
// real ones.
func TestSSHKeyFingerprint(t *testing.T) {
	if got := Fingerprint(""); got != "" {
		t.Errorf("Fingerprint of an empty line is %q", got)
	}
	if got := Fingerprint("hello world"); got != "" {
		t.Errorf("Fingerprint of plain text is %q", got)
	}
	if got := Fingerprint("ssh-ed25519 !!!invalid!!!"); got != "" {
		t.Errorf("Fingerprint of bad base64 is %q", got)
	}
	blob := append([]byte{0, 0, 0, 7}, []byte("ssh-rsa")...)
	line := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	if got := Fingerprint(line); got != "" {
		t.Errorf("Fingerprint of a mismatched type is %q", got)
	}

	dir := t.TempDir()
	key := writePair(t, dir, "id_work", "c")
	if got := Fingerprint(key.Public); got != key.Fingerprint {
		t.Errorf("Fingerprint of the public line is %q, want %q", got, key.Fingerprint)
	}
}

// CheckName rejects names that cannot be key files and accepts plain ones.
func TestSSHKeyCheckName(t *testing.T) {
	bad := map[string]string{
		"empty":               "",
		"leading whitespace":  " work",
		"trailing whitespace": "work ",
		"slash":               "a/b",
		"NUL":                 "a\x00b",
		"leading dot":         ".hidden",
		"leading dash":        "-work",
		"pub suffix":          "id.pub",
		"too long":            strings.Repeat("a", 256),
	}
	for why, name := range bad {
		if err := CheckName(name); err == nil {
			t.Errorf("CheckName accepted %s (%q)", why, name)
		}
	}
	if err := CheckName("id_work"); err != nil {
		t.Errorf("CheckName rejected id_work: %v", err)
	}
	if err := CheckName(strings.Repeat("a", 255)); err != nil {
		t.Errorf("CheckName rejected 255 bytes: %v", err)
	}
}

// Dir is the .ssh folder inside the home directory.
func TestSSHKeyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".ssh") {
		t.Errorf("Dir is %q, want %q", dir, filepath.Join(home, ".ssh"))
	}
}
