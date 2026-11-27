package store

import (
	"strings"
	"testing"
)

// TestAddressRoundTrip: the path of an ssh:// address is written as it is
// on the server, not as a URL escapes it — a % is a percent, and # or ? are
// part of the name rather than the start of a fragment or query. Reading
// what Address writes must give the same repository back for any path.
func TestAddressRoundTrip(t *testing.T) {
	// Paths that only make sense read verbatim: escapes, delimiters, spaces
	// inside the path, non-ASCII names and home-relative forms.
	paths := []string{
		"/srv/per%cent",
		"/srv/per%2fcent",
		"/srv/hash#frag",
		"/srv/quest?ion",
		"/srv/has space",
		"/srv/中文目录",
		"/srv/a%b#c?d e",
		"~/project",
		"~",
		"/",
		"/srv/plain",
	}
	for _, p := range paths {
		for _, r := range []Repo{
			{Kind: SSH, User: "u", Host: "h", Port: 2222, Path: p},
			{Kind: SSH, User: "", Host: "h", Port: 0, Path: p},
			{Kind: SSH, User: "u", Host: "::1", Port: 2222, Path: p},
			{Kind: SSH, User: "", Host: "2001:db8::1", Port: 0, Path: p},
		} {
			back, err := ParseAddress(r.Address())
			if err != nil {
				t.Errorf("ParseAddress(%q) of %+v: %v", r.Address(), r, err)
				continue
			}
			if back.Path != r.Path {
				t.Errorf("path round trip of %+v: read back %q from %q", r, back.Path, r.Address())
			}
			if back.Host != r.Host || back.User != r.User || back.Port != r.Port {
				t.Errorf("host round trip of %+v: read back %+v from %q", r, back, r.Address())
			}
		}
	}

	// With a port the address is exactly what was typed.
	for _, p := range []string{"/srv/per%cent", "/srv/hash#frag", "/srv/quest?ion", "/srv/has space", "/srv/中文目录", "/srv/plain"} {
		in := "ssh://u@h:2222" + p
		r, err := ParseAddress(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if r.Path != p {
			t.Errorf("%q read as path %q, want verbatim %q", in, r.Path, p)
		}
		if got := r.Address(); got != in {
			t.Errorf("%q written back as %q", in, got)
		}
	}

	// /~ is the home under the ssh:// form.
	if r, err := ParseAddress("ssh://u@h:2222/~/project"); err != nil || r.Path != "~/project" {
		t.Errorf("/~ read as %q, %v; want ~/project", r.Path, err)
	}
	if r, err := ParseAddress("ssh://u@h:2222/~"); err != nil || r.Path != "~" {
		t.Errorf("/~ read as %q, %v; want ~", r.Path, err)
	}
	if got := (Repo{Kind: SSH, User: "u", Host: "h", Port: 2222, Path: "~/project"}).Address(); got != "ssh://u@h:2222/~/project" {
		t.Errorf("~/project written as %q, want the /~ form", got)
	}

	// An IPv6 host carries brackets in the ssh:// form.
	r, err := ParseAddress("ssh://u@[::1]:2222/srv/x")
	if err != nil || r.Host != "::1" || r.Port != 2222 || r.Path != "/srv/x" {
		t.Fatalf("bracketed IPv6 read as %+v, %v", r, err)
	}
	if got := r.Address(); got != "ssh://u@[::1]:2222/srv/x" {
		t.Errorf("IPv6 written back as %q", got)
	}
	r, err = ParseAddress("ssh://u@[2001:db8::1]/srv/x")
	if err != nil || r.Host != "2001:db8::1" || r.Port != 0 || r.Path != "/srv/x" {
		t.Fatalf("portless IPv6 read as %+v, %v", r, err)
	}
	if got := r.Address(); got != "ssh://u@[2001:db8::1]/srv/x" {
		t.Errorf("portless IPv6 written back as %q, want the ssh:// form: scp style cannot hold colons", got)
	}

	// Malformed authorities are still errors.
	for _, bad := range []string{"ssh://box:port/x", "ssh://u@[::1x/srv", "ssh://u@[::1]x/srv"} {
		if got, err := ParseAddress(bad); err == nil {
			t.Errorf("%q: want an error, got %+v", bad, got)
		}
	}

	// The scp form is untouched: its path never carried escapes or
	// delimiters, and it reads back unchanged.
	r, err = ParseAddress("u@h:/srv/per%cent#x?y")
	if err != nil || r.Path != "/srv/per%cent#x?y" {
		t.Errorf("scp path read as %q, %v; want verbatim", r.Path, err)
	}
	if err == nil {
		if got := r.Address(); got != "u@h:/srv/per%cent#x?y" {
			t.Errorf("scp path written back as %q", got)
		}
	}

	// Whitespace around the typed address is a paste artifact and still
	// goes; the path inside keeps exactly what the server has, so a name
	// ending in a space cannot be typed at all — Address never writes one
	// that reads back different, it writes the space and pasting trims it.
	in := "ssh://u@h:2222/srv/has space"
	if r, err := ParseAddress("  " + in + "  "); err != nil || r.Path != "/srv/has space" {
		t.Errorf("padded address read as %q, %v; want the space inside kept", r.Path, err)
	} else if strings.TrimSpace(in) != in {
		t.Errorf("test error: %q should have no outer whitespace", in)
	}
}
