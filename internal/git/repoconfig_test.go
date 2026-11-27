package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const fakeGPG = `#!/bin/sh
cat <<'KEYS'
sec:u:255:22:AB12CD34EF567890:1700000000:::u:::scESC:::+:::ed25519:::0:
fpr:::::::::0123456789ABCDEF0123AB12CD34EF567890:
grp:::::::::FFFF:
uid:u::::1700000000::HASH::Ada Lovelace <ada@example.com>::::::::::0:
ssb:u:255:18:1111222233334444:1700000000::::::e:::+:::cv25519::
sec:e:255:22:9999888877776666:1500000000:1600000000::u:::sc:::+:::ed25519:::0:
uid:e::::1500000000::HASH2::Old Key \x3a expired::::::::::0:
KEYS
`

func TestGPGKeys(t *testing.T) {
	isolateCredentials(t)
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	gitIn(t, dir, "config", "gpg.program", "/nonexistent/gpg")
	if _, err := r.GPGKeys(); !errors.Is(err, ErrNoGPG) {
		t.Fatalf("missing gpg: %v", err)
	}

	gpg := filepath.Join(t.TempDir(), "gpg")
	if err := os.WriteFile(gpg, []byte(fakeGPG), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "config", "gpg.program", gpg)
	keys, err := r.GPGKeys()
	if err != nil {
		t.Fatal(err)
	}
	want := []GPGKey{
		{ID: "AB12CD34EF567890", Fingerprint: "0123456789ABCDEF0123AB12CD34EF567890", User: "Ada Lovelace <ada@example.com>"},
		{ID: "9999888877776666", User: "Old Key : expired", Unusable: true},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %+v", keys)
	}
	if keys[0].Label() != "Ada Lovelace <ada@example.com> (AB12CD34EF567890)" {
		t.Error(keys[0].Label())
	}
}

func TestLocalUserAndTemplate(t *testing.T) {
	isolateCredentials(t)
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if err := r.SetLocalUser("Grace", "grace@example.com"); err != nil {
		t.Fatal(err)
	}
	if name, email, err := r.LocalUser(); err != nil || name != "Grace" || email != "grace@example.com" {
		t.Fatalf("local user %q %q %v", name, email, err)
	}
	if err := r.SetLocalUser("", ""); err != nil {
		t.Fatal(err)
	}
	if name, email, err := r.LocalUser(); err != nil || name != "" || email != "" {
		t.Fatalf("after unset %q %q %v", name, email, err)
	}
	// Unsetting what is not set is fine.
	if err := r.SetLocalUser("", ""); err != nil {
		t.Fatal(err)
	}

	if text, err := r.CommitTemplate(); err != nil || text != "" {
		t.Fatalf("no template %q %v", text, err)
	}
	template := filepath.Join(t.TempDir(), "it's a template.txt")
	if err := os.WriteFile(template, []byte("Subject\n\nWhy:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "config", "commit.template", template)
	if text, err := r.CommitTemplate(); err != nil || text != "Subject\n\nWhy:\n" {
		t.Fatalf("template %q %v", text, err)
	}
	if text, err := r.ReadFile(".branchline/none.json"); err != nil || text != "" {
		t.Fatalf("missing file %q %v", text, err)
	}
	if _, err := r.ReadFile("../x"); err == nil {
		t.Fatal("read outside the repository")
	}
}

func TestTrackerRules(t *testing.T) {
	isolateCredentials(t)
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	if rules, err := r.TrackerRules(true); err != nil || len(rules) != 0 {
		t.Fatalf("no file %v %v", rules, err)
	}
	shared := []TrackerRule{{Name: "Jira Rule", Regex: `([A-Z]+-\d+)`, URL: "https://jira.example.com/browse/$1"}, {Name: "GitHub", Regex: `#(\d+)`, URL: "https://github.com/o/r/issues/$1"}}
	if err := r.SetTrackerRules(true, shared); err != nil {
		t.Fatal(err)
	}
	if got, err := r.TrackerRules(true); err != nil || !reflect.DeepEqual(got, shared) {
		t.Fatalf("shared %+v %v", got, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".issuetracker"))
	if !strings.Contains(string(data), `[issuetracker "Jira Rule"]`) {
		t.Fatalf(".issuetracker %s", data)
	}
	local := []TrackerRule{{Name: "Mine", Regex: `FW-(\d+)`, URL: "https://t/$1"}}
	if err := r.SetTrackerRules(false, local); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "issuetracker")); err != nil {
		t.Fatal(err)
	}
	// Replacing drops a removed rule.
	if err := r.SetTrackerRules(true, shared[1:]); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.TrackerRules(true); !reflect.DeepEqual(got, shared[1:]) {
		t.Fatalf("after removing %+v", got)
	}
	if got, _ := r.TrackerRules(false); !reflect.DeepEqual(got, local) {
		t.Fatalf("local %+v", got)
	}
	if err := r.SetTrackerRules(true, []TrackerRule{{Name: "a"}, {Name: "a"}}); err == nil {
		t.Fatal("two rules of one name")
	}
}

const fakeLFS = `#!/bin/sh
printf '%s\n' "$@" > "$LFS_ARGS"
shift 2
for p in "$@"; do printf '%s filter=lfs diff=lfs merge=lfs -text\n' "$p" >> .gitattributes; done
`

func TestLFSTrackPreview(t *testing.T) {
	isolateCredentials(t)
	dir := testRepo(t)
	for _, name := range []string{"art/logo.psd", "art/deep/icon.psd", "video/clip.mov", "notes.txt"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := New(&LocalExecutor{Dir: dir})
	patterns := LFSPatterns("*.psd\n\n  video/*.mov \n# a comment\n")
	if !reflect.DeepEqual(patterns, []string{"*.psd", "video/*.mov"}) {
		t.Fatalf("patterns %q", patterns)
	}
	files, err := r.LFSMatches(patterns)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"art/deep/icon.psd", "art/logo.psd", "video/clip.mov"}) {
		t.Fatalf("matches %q", files)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git-lfs"), []byte(fakeLFS), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LFS_ARGS", filepath.Join(t.TempDir(), "args"))
	if err := r.LFSTrackAll(patterns); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(os.Getenv("LFS_ARGS"))
	if string(args) != "track\n--\n*.psd\nvideo/*.mov\n" {
		t.Fatalf("lfs args %q", args)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(staged+"\n", ".gitattributes\n") {
		t.Fatalf("staged %q", staged)
	}
}
