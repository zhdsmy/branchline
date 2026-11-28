package git

import (
	"strings"
	"testing"
)

// TestPushNoteRejectsEmptyTarget checks that PushNote refuses to run any git
// command when the remote or the branch is empty or all whitespace: a push
// without both names is a bare git push, whose choice of references follows
// the user's push.default rather than the caller's. The CommandLog proves no
// git invocation ran — a rejected target must fail before reaching the
// command line.
func TestPushNoteRejectsEmptyTarget(t *testing.T) {
	dir, _ := endOfOptionsRepo(t)

	cases := []struct {
		name   string
		remote string
		branch string
	}{
		{"empty remote", "", "main"},
		{"empty branch", "srv", ""},
		{"empty remote and branch", "", ""},
		{"whitespace remote", " \t ", "main"},
		{"whitespace branch", "srv", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := NewCommandLog()
			r := New(&LocalExecutor{Dir: dir}).WithCommandLog(log)
			note, err := r.PushNote(tc.remote, tc.branch, "", false, false, false)
			if err == nil {
				t.Fatalf("PushNote(%q, %q) succeeded, want an error before git runs", tc.remote, tc.branch)
			}
			if note != "" {
				t.Fatalf("PushNote(%q, %q) note = %q, want none for a push that never ran", tc.remote, tc.branch, note)
			}
			recs, dropped := log.Snapshot()
			if len(recs) != 0 || dropped != 0 {
				t.Fatalf("PushNote(%q, %q) ran %d git command(s) despite the invalid target: %+v", tc.remote, tc.branch, len(recs)+dropped, recs)
			}
		})
	}
}

// TestPushNoteExplicitRefSpec checks that a valid PushNote call pushes exactly
// the one reference it names — the source branch onto the destination branch,
// nothing else — so the remote ends with the pushed commit and no other refs.
func TestPushNoteExplicitRefSpec(t *testing.T) {
	dir, bare := endOfOptionsRepo(t)
	log := NewCommandLog()
	r := New(&LocalExecutor{Dir: dir}).WithCommandLog(log)

	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "refs/heads/main"))
	if _, err := r.PushNote("srv", "main", "", true, false, false); err != nil {
		t.Fatalf("PushNote: %v", err)
	}
	if got := gitOut(t, bare, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("bare refs/heads/main = %q, want %q", got, head)
	}
	if got := strings.TrimSpace(gitOut(t, bare, "for-each-ref")); strings.Count(got, "\n") != 0 || !strings.Contains(got, "refs/heads/main") {
		t.Fatalf("bare carries refs beyond the pushed one: %q", got)
	}

	// The recorded command names the remote and the explicit refspec, never
	// degenerating into a bare push.
	recs, _ := log.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("recorded %d git commands, want exactly the one push: %+v", len(recs), recs)
	}
	want := "refs/heads/main:refs/heads/main"
	if !strings.Contains(recs[0].Command, want) {
		t.Fatalf("push command %q lacks the explicit refspec %q", recs[0].Command, want)
	}
}
