package git

import (
	"errors"
	"fmt"
	"strings"
)

// ErrGlobalIdentityChanged says the global git configuration no longer holds
// the identity the caller read, so writing would overwrite someone else's
// edit.
var ErrGlobalIdentityChanged = errors.New("the global Git identity changed since it was read")

// ErrGlobalIdentityInvalid says the identity to write holds characters git
// configuration values cannot hold.
var ErrGlobalIdentityInvalid = errors.New("the global Git identity holds control characters")

// SafeSetGlobalIdentity writes user.name and user.email into the global git
// configuration without ever overwriting data blindly:
//
//   - readErr is the error of the read the caller based next and expected on;
//     when it is not nil nothing is written.
//   - A name or email holding a control character git cannot store is
//     rejected before any write.
//   - The live identity is re-read just before writing; a failed read, or a
//     live identity different from expected, refuses the write and leaves the
//     configuration untouched.
//   - An unchanged identity writes nothing at all.
//   - When the second of two writes fails, the first is rolled back to its
//     live value so a failure never leaves half of the new identity behind.
//
// An empty value unsets the key; a key both empty in live and empty in next is
// left alone.
func (r *Repo) SafeSetGlobalIdentity(next, expected Identity, readErr error) error {
	if readErr != nil {
		return fmt.Errorf("not writing the global Git identity after its read failed: %w", readErr)
	}
	next = trimIdentity(next)
	expected = trimIdentity(expected)
	for _, value := range []string{next.Name, next.Email} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return ErrGlobalIdentityInvalid
		}
	}
	live, err := r.readGlobalIdentityStrict()
	if err != nil {
		return fmt.Errorf("not writing the global Git identity after its re-read failed: %w", err)
	}
	if live != expected {
		return fmt.Errorf("%w: read name %q email %q, now name %q email %q", ErrGlobalIdentityChanged, expected.Name, expected.Email, live.Name, live.Email)
	}
	if next == live {
		return nil
	}
	// user.name first: rolling back one written key is enough, as at most the
	// name write has happened when the email write fails.
	if next.Name != live.Name {
		if err := r.writeGlobalKey("user.name", next.Name); err != nil {
			return err
		}
	}
	if next.Email != live.Email {
		if err := r.writeGlobalKey("user.email", next.Email); err != nil {
			if next.Name != live.Name {
				if rbErr := r.writeGlobalKey("user.name", live.Name); rbErr != nil {
					return fmt.Errorf("%w (and rolling back user.name failed too: %v)", err, rbErr)
				}
			}
			return err
		}
	}
	return nil
}

func trimIdentity(id Identity) Identity {
	return Identity{Name: strings.TrimSpace(id.Name), Email: strings.TrimSpace(id.Email)}
}

// readGlobalIdentityStrict reads the global identity without mistaking a
// damaged or inaccessible configuration for an empty one. git answers a
// configuration file it cannot read — no permission, for instance — with the
// same exit code 1 as a key that was never set, plus a warning on standard
// error; GlobalIdentity's exit-code check alone would read that as an empty
// identity, and a follow-up write would replace the file the user cannot
// read. Here anything on standard error or any other failure means the state
// of the file is unknown and the read fails.
func (r *Repo) readGlobalIdentityStrict() (Identity, error) {
	var out Identity
	for _, kv := range []struct {
		key  string
		into *string
	}{{"user.name", &out.Name}, {"user.email", &out.Email}} {
		stdout, err := r.run(quick, "config", "--global", "--get", kv.key)
		if err == nil {
			*kv.into = strings.TrimSpace(string(stdout))
			continue
		}
		var ge *Error
		if errors.As(err, &ge) && ge.ExitCode == 1 {
			if strings.TrimSpace(ge.Stderr) != "" {
				return Identity{}, fmt.Errorf("reading the global Git configuration: %s", strings.TrimSpace(ge.Stderr))
			}
			continue // the key is not set
		}
		return Identity{}, err
	}
	return out, nil
}

// writeGlobalKey sets key to value in the global git configuration, or unsets
// it when value is empty. Unsetting a key already gone (git exits 5) is not
// an error: the wanted state is already there.
func (r *Repo) writeGlobalKey(key, value string) error {
	if value != "" {
		_, err := r.run(quick, "config", "--global", key, value)
		return err
	}
	if _, err := r.run(quick, "config", "--global", "--unset", key); err != nil && ExitCode(err) != 5 {
		return err
	}
	return nil
}
