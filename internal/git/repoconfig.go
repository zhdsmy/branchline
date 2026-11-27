package git

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoGPG says gpg is not installed where the repository's git runs.
var ErrNoGPG = errors.New("GPG Tools are not installed")

// GPGKey is a secret key gpg can sign with.
type GPGKey struct {
	ID          string // the long key ID
	Fingerprint string
	User        string // the first user ID, as Name <email>
	Unusable    bool   // expired, revoked or disabled
}

// Label is how a key is listed: its user and ID.
func (k GPGKey) Label() string {
	if k.User == "" {
		return k.ID
	}
	return k.User + " (" + k.ID + ")"
}

// GPGKeys lists the secret keys of the gpg git signs with: gpg.program,
// or gpg. It runs where git runs, so a repository on a server lists the
// server's keys.
func (r *Repo) GPGKeys() ([]GPGKey, error) {
	out, err := r.Script(`p=$(git config gpg.program || echo gpg); command -v "$p" >/dev/null 2>&1 || exit 127; "$p" --batch --list-secret-keys --with-colons`)
	if err != nil {
		if ExitCode(err) == 127 || strings.Contains(err.Error(), "not found") {
			return nil, ErrNoGPG
		}
		return nil, err
	}
	return ParseGPGKeys(out), nil
}

// ParseGPGKeys reads gpg --list-secret-keys --with-colons.
func ParseGPGKeys(out string) []GPGKey {
	var keys []GPGKey
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), ":")
		if len(f) < 10 {
			continue
		}
		switch f[0] {
		case "sec":
			k := GPGKey{ID: f[4]}
			k.Unusable = strings.ContainsAny(f[1], "erd") || len(f) > 11 && strings.Contains(f[11], "D")
			keys = append(keys, k)
		case "fpr":
			if n := len(keys); n > 0 && keys[n-1].Fingerprint == "" {
				keys[n-1].Fingerprint = f[9]
			}
		case "uid":
			if n := len(keys); n > 0 && keys[n-1].User == "" {
				keys[n-1].User = strings.ReplaceAll(f[9], `\x3a`, ":")
			}
		}
	}
	return keys
}

// LocalUser returns the user.name and user.email of the repository's own
// configuration, empty when only the global one sets them.
func (r *Repo) LocalUser() (name, email string, err error) {
	get := func(key string) (string, error) {
		out, err := r.run(quick, "config", "--local", "--get", key)
		if ExitCode(err) == 1 {
			return "", nil
		}
		return strings.TrimSpace(string(out)), err
	}
	if name, err = get("user.name"); err != nil {
		return
	}
	email, err = get("user.email")
	return
}

// GlobalUser returns the user.name and user.email git uses outside the
// repository's own configuration.
func (r *Repo) GlobalUser() (name, email string) {
	get := func(key string) string {
		out, _ := r.run(quick, "config", "--global", "--get", key)
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

// SetLocalUser writes user.name and user.email in the repository's own
// configuration. An empty value is removed, so the global one shows again.
func (r *Repo) SetLocalUser(name, email string) error {
	for _, kv := range [][2]string{{"user.name", name}, {"user.email", email}} {
		var err error
		if v := strings.TrimSpace(kv[1]); v != "" {
			_, err = r.run(quick, "config", "--local", kv[0], v)
		} else {
			_, err = r.run(quick, "config", "--local", "--unset", kv[0])
			if ExitCode(err) == 5 {
				err = nil
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// CommitTemplate returns the text of git's commit.template, empty when
// none is set.
func (r *Repo) CommitTemplate() (string, error) {
	out, err := r.run(quick, "config", "--path", "--get", "commit.template")
	if ExitCode(err) == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", nil
	}
	return r.Script("cat -- " + ShellQuote(path))
}

// ReadFile returns a file of the working tree, relative to its top, or
// "" when it does not exist.
func (r *Repo) ReadFile(path string) (string, error) {
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return "", fmt.Errorf("%s is outside the repository", path)
	}
	return r.Script("if [ -f " + ShellQuote(path) + " ]; then cat -- " + ShellQuote(path) + "; fi")
}

// TrackerRule is a bug tracker rule of an .issuetracker file: text
// matching Regex links to URL, where $1, $2… are the groups.
type TrackerRule struct {
	Name  string
	Regex string
	URL   string
}

// trackerFile is where rules are kept: .issuetracker at the top of the
// working tree is shared through the repository, .git/issuetracker is
// this clone's.
func (r *Repo) trackerFile(shared bool) (string, error) {
	if shared {
		return ".issuetracker", nil
	}
	out, err := r.run(quick, "rev-parse", "--git-path", "issuetracker")
	return strings.TrimSpace(string(out)), err
}

// TrackerRules reads the rules of one .issuetracker file.
func (r *Repo) TrackerRules(shared bool) ([]TrackerRule, error) {
	file, err := r.trackerFile(shared)
	if err != nil {
		return nil, err
	}
	out, err := r.run(quick, "config", "-f", file, "-z", "--get-regexp", `^issuetracker\.`)
	if ExitCode(err) == 1 || err != nil && strings.Contains(err.Error(), "No such file") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseTrackerRules(string(out)), nil
}

func parseTrackerRules(out string) []TrackerRule {
	byName := map[string]*TrackerRule{}
	var order []string
	for _, entry := range strings.Split(out, "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		if !strings.HasPrefix(key, "issuetracker.") {
			continue
		}
		key = strings.TrimPrefix(key, "issuetracker.")
		dot := strings.LastIndex(key, ".")
		if dot < 0 {
			continue
		}
		name, field := key[:dot], key[dot+1:]
		rule := byName[name]
		if rule == nil {
			rule = &TrackerRule{Name: name}
			byName[name] = rule
			order = append(order, name)
		}
		switch field {
		case "regex":
			rule.Regex = value
		case "url":
			rule.URL = value
		}
	}
	rules := make([]TrackerRule, 0, len(order))
	for _, name := range order {
		rules = append(rules, *byName[name])
	}
	return rules
}

// SetTrackerRules replaces the rules of one .issuetracker file. No rules
// leave an empty file.
func (r *Repo) SetTrackerRules(shared bool, rules []TrackerRule) error {
	file, err := r.trackerFile(shared)
	if err != nil {
		return err
	}
	old, err := r.TrackerRules(shared)
	if err != nil {
		return err
	}
	for _, rule := range old {
		if _, err := r.run(quick, "config", "-f", file, "--remove-section", "issuetracker."+rule.Name); err != nil && ExitCode(err) != 128 {
			return err
		}
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" || seen[name] || strings.ContainsAny(name, "\n\"\\") {
			return fmt.Errorf("bug tracker rule names must be unique and on one line: %q", rule.Name)
		}
		seen[name] = true
		for _, kv := range [][2]string{{"regex", rule.Regex}, {"url", rule.URL}} {
			if _, err := r.run(quick, "config", "-f", file, "issuetracker."+name+"."+kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// lfsPathspec turns a .gitattributes pattern into a pathspec: one without
// a slash matches a name in any folder, as Git LFS reads it.
func lfsPathspec(pattern string) string {
	if strings.Contains(strings.TrimSuffix(pattern, "/"), "/") {
		return ":(glob)" + strings.TrimPrefix(pattern, "/")
	}
	return ":(glob)**/" + pattern
}

// LFSPatterns splits the patterns typed one per line, without blanks.
func LFSPatterns(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// LFSMatches lists the files of the working tree, tracked or not, that
// the patterns would put in Git LFS.
func (r *Repo) LFSMatches(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}
	for _, p := range patterns {
		args = append(args, lfsPathspec(p))
	}
	out, err := r.run(quick, args...)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files, nil
}

// LFSTrackAll records every pattern in .gitattributes with one git lfs
// track, and stages that file.
func (r *Repo) LFSTrackAll(patterns []string) error {
	if len(patterns) == 0 {
		return fmt.Errorf("a Git LFS pattern is empty")
	}
	if _, err := r.run(quick, append([]string{"lfs", "track", "--"}, patterns...)...); err != nil {
		return err
	}
	return r.Stage(".gitattributes")
}

// ReadGitFile returns a file of the repository's git folder, such as
// info/exclude, or "" when it does not exist.
func (r *Repo) ReadGitFile(name string) (string, error) {
	text, _, err := r.ReadGitFileExists(name)
	return text, err
}

// ReadGitFileExists reads a file of the repository's git folder. The boolean
// is false when that file does not exist. An existing empty file returns an
// empty string and true, which is distinct from a missing file or a read error.
func (r *Repo) ReadGitFileExists(name string) (string, bool, error) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return "", false, fmt.Errorf("%s is outside the git folder", name)
	}
	out, err := r.Script(`f=$(git rev-parse --git-path ` + ShellQuote(name) + `) && if [ -f "$f" ]; then echo yes; cat -- "$f"; else echo no; fi`)
	if err != nil {
		return "", false, err
	}
	marker, text, found := strings.Cut(out, "\n")
	if !found {
		marker = out
	}
	switch marker {
	case "yes":
		return text, true, nil
	case "no":
		return "", false, nil
	default:
		return "", false, fmt.Errorf("cannot tell whether %s exists", name)
	}
}

// PushCustomRefspecKey is where the push dialog remembers the custom
// remote branch last pushed to, in the repository's own git config.
const PushCustomRefspecKey = "branchline.pushLastCustomRefspec"

// LocalConfig returns a value of the repository's own configuration,
// empty when it is not set.
func (r *Repo) LocalConfig(key string) (string, error) {
	out, err := r.run(quick, "config", "--local", "--get", key)
	if ExitCode(err) == 1 {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

// SetLocalConfig writes a value in the repository's own configuration.
// An empty value removes the key.
func (r *Repo) SetLocalConfig(key, value string) error {
	if value = strings.TrimSpace(value); value == "" {
		_, err := r.run(quick, "config", "--local", "--unset-all", key)
		if ExitCode(err) == 5 {
			return nil
		}
		return err
	}
	_, err := r.run(quick, "config", "--local", key, value)
	return err
}

// WriteFile replaces a file of the working tree, creating its folders.
// The text travels base64 encoded, so any bytes and any quoting reach the
// file as they are, on a local and on a remote repository alike.
func (r *Repo) WriteFile(path, text string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("%s is outside the repository", path)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	dir := ""
	if i := strings.LastIndex(path, "/"); i > 0 {
		dir = "mkdir -p -- " + ShellQuote(path[:i]) + " && "
	}
	_, err := r.Script(dir + "printf %s " + ShellQuote(enc) + " | (base64 -d 2>/dev/null || base64 -D) > " + ShellQuote(path))
	return err
}
