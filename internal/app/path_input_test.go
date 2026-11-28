package app

import (
	"strings"
	"testing"
)

// The edge-whitespace check reports a leading or trailing blank of any
// whitespace kind with the field's name in the message, and leaves values
// whose spaces sit inside the path — including home-relative and non-ASCII
// paths — alone.
func TestPathInputEdges(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"leading space", " /tmp/repos", true},
		{"trailing space", "/tmp/repos ", true},
		{"trailing tab", "/tmp/repos\t", true},
		{"trailing no-break space", "/tmp/repos\u00a0", true},
		{"leading and trailing", " /tmp/repos ", true},
		{"space inside", "/tmp/repo with space", false},
		{"home relative with space inside", "~/repo with space", false},
		{"non-ASCII path", "~/代码/仓库", false},
		{"plain path", "/tmp/repos", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := edgeSpaceError(tc.input, "Parent Folder")
			if tc.want {
				want := "Parent Folder cannot begin or end with a space."
				if got != want {
					t.Fatalf("edgeSpaceError(%q) = %q, want %q", tc.input, got, want)
				}
			} else if got != "" {
				t.Fatalf("edgeSpaceError(%q) = %q, want no error", tc.input, got)
			}
		})
	}

	// The message carries the field it is shown for.
	if got := edgeSpaceError("/srv/repos ", "Server Folder"); !strings.Contains(got, "Server Folder") {
		t.Fatalf("edgeSpaceError for the Server Folder = %q, want the field named", got)
	}
}
