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

	// Malformed authorities and out-of-range or signed ports are errors.
	for _, bad := range []string{
		"ssh://box:port/x",
		"ssh://u@[::1x/srv",
		"ssh://u@[::1]x/srv",
		"ssh://u@h:+22/x",
		"ssh://u@h:-1/x",
		"ssh://u@h:0/x",
		"ssh://u@h:65536/x",
		"ssh://u@h:99999999999999999999/x",
		"ssh://u@h:22a/x",
		"ssh://u@[::1]:+22/x",
		"ssh://u@[::1]:-1/x",
		"ssh://u@[::1]:0/x",
		"ssh://u@[::1]:65536/x",
	} {
		if got, err := ParseAddress(bad); err == nil {
			t.Errorf("%q: want an error, got %+v", bad, got)
		}
	}

	// Boundary decimal ports 1 and 65535 are valid on hostnames and IPv6.
	for _, tc := range []struct {
		in   string
		host string
		port int
	}{
		{"ssh://u@h:1/x", "h", 1},
		{"ssh://u@h:65535/x", "h", 65535},
		{"ssh://u@[::1]:1/x", "::1", 1},
		{"ssh://u@[::1]:65535/x", "::1", 65535},
	} {
		got, err := ParseAddress(tc.in)
		if err != nil || got.Host != tc.host || got.Port != tc.port || got.Path != "/x" {
			t.Errorf("%q: got %+v, %v; want host=%q port=%d path=/x", tc.in, got, err, tc.host, tc.port)
		}
	}

	// Authority splits user and host on the last @, matching url.Parse;
	// SSHExecutor.Validate still rejects @ in the username.
	gotAt, err := ParseAddress("ssh://a@b@host/x")
	if err == nil {
		t.Errorf("ssh://a@b@host/x: want validation error for @ in user, got %+v", gotAt)
	}
	if gotAt.User != "a@b" || gotAt.Host != "host" || gotAt.Path != "/x" {
		t.Errorf("ssh://a@b@host/x: parsed as user=%q host=%q path=%q, want user=%q host=%q path=%q",
			gotAt.User, gotAt.Host, gotAt.Path, "a@b", "host", "/x")
	}
	gotAt6, err := ParseAddress("ssh://a@b@[::1]:2222/x")
	if err == nil {
		t.Errorf("ssh://a@b@[::1]:2222/x: want validation error for @ in user, got %+v", gotAt6)
	}
	if gotAt6.User != "a@b" || gotAt6.Host != "::1" || gotAt6.Port != 2222 || gotAt6.Path != "/x" {
		t.Errorf("ssh://a@b@[::1]:2222/x: parsed as user=%q host=%q port=%d path=%q, want user=%q host=%q port=2222 path=/x",
			gotAt6.User, gotAt6.Host, gotAt6.Port, gotAt6.Path, "a@b", "::1")
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
