package git

import (
	"net/url"
	"strings"
	"sync"
)

// Credential is what git answers for an HTTPS remote bound to a hosting
// account: the account's user name and token. It is handed to git through
// the askpass script when git asks, so it is never in a command line, an
// environment, the git configuration or a file; for a repository on a
// server it travels on the standard input of ssh.
type Credential struct {
	// URL is the remote, http or https, without a user.
	URL      string
	Username string
	Password string
}

// remoteURL is the credential's URL, cleaned, or nil when it is not one
// git asks a password for.
func (c Credential) remoteURL() *url.URL {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil
	}
	u.User = nil
	return u
}

// credentialConfig is the -c settings that make git ask for the bound
// remotes: no credential helper, which could answer with another login
// or save the token, the path in the question, so that each remote gets
// its own account, and the account's user name.
func credentialConfig(creds []Credential) []string {
	var out []string
	for _, c := range creds {
		u := c.remoteURL()
		if u == nil {
			continue
		}
		// git matches the configuration by path, where repo and repo.git
		// differ: both get it.
		other := *u
		other.Path = strings.TrimSuffix(u.Path, "/")
		if strings.HasSuffix(other.Path, ".git") {
			other.Path = strings.TrimSuffix(other.Path, ".git")
		} else if other.Path != "" {
			other.Path += ".git"
		}
		for _, v := range []*url.URL{u, &other} {
			key := "credential." + v.String()
			out = append(out, "-c", key+".helper=", "-c", key+".useHttpPath=true")
			if c.Username != "" {
				out = append(out, "-c", key+".username="+c.Username)
			}
		}
	}
	return out
}

// sameRemotePath compares the paths of two remote URLs, ignoring .git and a
// trailing slash.
func sameRemotePath(a, b string) bool {
	clean := func(p string) string {
		p = strings.Trim(p, "/")
		return strings.TrimSuffix(p, ".git")
	}
	return clean(a) == clean(b)
}

// credentialFor answers a question of git from the bound credentials:
// the remote with the same host and path. git says the path, as the
// configuration asks it to for these remotes; another repository of the
// same host is asked to the user.
func credentialFor(creds []Credential, prompt string) (string, bool) {
	asked, ok := promptURL(prompt)
	if !ok || strings.Trim(asked.Path, "/") == "" {
		return "", false
	}
	for _, c := range creds {
		u := c.remoteURL()
		if u == nil || u.Scheme != asked.Scheme || !strings.EqualFold(u.Host, asked.Host) || !sameRemotePath(u.Path, asked.Path) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(prompt), "Username") {
			return c.Username, c.Username != ""
		}
		return c.Password, true
	}
	return "", false
}

// askSession answers the questions of one command: from the bound
// credentials, then the keychain, then the user. What the user asked to
// remember is saved when the command did not fail to log in; a saved
// answer that was refused is removed.
type askSession struct {
	a     *AskPass
	creds []Credential

	mu       sync.Mutex
	tried    map[string]bool
	used     []string
	refused  []string
	remember map[string]string
}

func (a *AskPass) session(creds []Credential) *askSession {
	return &askSession{a: a, creds: creds, tried: map[string]bool{}, remember: map[string]string{}}
}

func (s *askSession) ask(prompt string) (string, bool) {
	if answer, ok := credentialFor(s.creds, prompt); ok {
		return answer, true
	}
	key := SecretAccount(prompt)
	secrets := s.a.secrets()
	if key != "" && secrets != nil {
		s.mu.Lock()
		first := !s.tried[key]
		s.tried[key] = true
		s.mu.Unlock()
		if !first {
			// Asked again in the same command: what was saved was refused.
			s.mu.Lock()
			for _, used := range s.used {
				if used == key {
					s.refused = append(s.refused, key)
				}
			}
			s.mu.Unlock()
		} else {
			if secret, ok := secrets.Find(key); ok {
				s.mu.Lock()
				s.used = append(s.used, key)
				s.mu.Unlock()
				return secret, true
			}
		}
	}
	reply := s.a.askUser(Question{Prompt: prompt, CanRemember: key != "" && secrets != nil})
	if reply.OK && reply.Remember && key != "" {
		s.mu.Lock()
		s.remember[key] = reply.Answer
		s.mu.Unlock()
	}
	return reply.Answer, reply.OK
}

// finish saves or forgets, once the command is over.
func (s *askSession) finish(err error) {
	secrets := s.a.secrets()
	if secrets == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if authFailed(err) {
		for _, key := range s.used {
			_ = secrets.Delete(key)
		}
		return
	}
	for _, key := range s.refused {
		if _, again := s.remember[key]; !again {
			_ = secrets.Delete(key)
		}
	}
	for key, secret := range s.remember {
		_ = secrets.Save(key, secret)
	}
}
