package git

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// SecretStore keeps the answers the user asked to remember, under an
// account name made from the prompt.
type SecretStore interface {
	Find(account string) (secret string, ok bool)
	Save(account, secret string) error
	Delete(account string) error
}

// KeychainService is the service of the items Branchline saves.
const KeychainService = "Branchline"

// Keychain keeps secrets as generic passwords of the login keychain, with
// macOS's security program.
type Keychain struct {
	// Program is the security program, /usr/bin/security when empty.
	Program string
	// Service names the items, KeychainService when empty.
	Service string
}

func (k *Keychain) program() string {
	if k.Program != "" {
		return k.Program
	}
	return "/usr/bin/security"
}

func (k *Keychain) service() string {
	if k.Service != "" {
		return k.Service
	}
	return KeychainService
}

func (k *Keychain) run(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, k.program(), args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	if err == nil && strings.Contains(errOut.String(), "returned -") {
		err = errors.New(strings.TrimSpace(errOut.String()))
	}
	if err != nil && errOut.Len() > 0 {
		err = errors.New("keychain: " + strings.TrimSpace(errOut.String()))
	}
	return out.String(), err
}

// Find returns the secret saved for account.
func (k *Keychain) Find(account string) (string, bool) {
	out, err := k.run("", "find-generic-password", "-a", account, "-s", k.service(), "-w")
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(out, "\n"), true
}

// Save saves secret for account, replacing what was saved. The secret
// goes to security on its standard input, so that it is in no command
// line another process could read.
func (k *Keychain) Save(account, secret string) error {
	if strings.ContainsAny(secret+account, "\n\r") {
		return errors.New("keychain: a secret cannot hold a new line")
	}
	line := "add-generic-password -U -a " + securityQuote(account) + " -s " + securityQuote(k.service()) +
		" -l " + securityQuote(k.service()+": "+account) + " -w " + securityQuote(secret) + "\n"
	_, err := k.run(line, "-i")
	return err
}

// Delete removes what was saved for account.
func (k *Keychain) Delete(account string) error {
	_, err := k.run("", "delete-generic-password", "-a", account, "-s", k.service())
	return err
}

// securityQuote quotes an argument of a command of security -i.
func securityQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

var (
	gitURLPrompt  = regexp.MustCompile(`^(Username|Password) for '([^']+)'`)
	sshPassPrompt = regexp.MustCompile(`^([^\s@']+@[^\s']+)'s password`)
	keyPassPrompt = regexp.MustCompile(`passphrase\s*for\s*(?:key\s*)?['"]([^'"]+)['"]`)
)

// SecretAccount names what a prompt asks for, as the keychain keeps it:
// the URL with its user for a password of git, "username" and the URL
// for a user name, ssh://user@host for the password of a login, and the
// key file for a passphrase. It is empty for what is not remembered.
func SecretAccount(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if m := gitURLPrompt.FindStringSubmatch(prompt); m != nil {
		if m[1] == "Username" {
			return "username " + m[2]
		}
		return m[2]
	}
	if m := sshPassPrompt.FindStringSubmatch(prompt); m != nil {
		return "ssh://" + m[1]
	}
	if m := keyPassPrompt.FindStringSubmatch(prompt); m != nil {
		return "key:" + m[1]
	}
	return ""
}

// promptURL is the URL a prompt of git names, without its user.
func promptURL(prompt string) (*url.URL, bool) {
	m := gitURLPrompt.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return nil, false
	}
	u, err := url.Parse(m[2])
	if err != nil || u.Host == "" {
		return nil, false
	}
	u.User = nil
	return u, true
}

// authFailed reports an error that says the credentials were refused, so
// that a saved one is not tried again.
func authFailed(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"authentication failed", "permission denied", "invalid username or password", "access denied", "incorrect passphrase", "bad passphrase", "401", "403"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
