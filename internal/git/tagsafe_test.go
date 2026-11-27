package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tagTestRepo builds a repository with two commits on main, the annotated tag
// ann (message "release notes") and the lightweight tag lw, both on HEAD.
// It returns the directory, the repo and the two commit hashes, oldest first.
func tagTestRepo(t *testing.T) (string, *Repo, string, string) {
	t.Helper()
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	first := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	gitRun(t, dir, "commit", "-q", "-am", "second")
	second := gitOut(t, dir, "rev-parse", "HEAD")
	return dir, New(&LocalExecutor{Dir: dir}), first, second
}

func tagExists(t *testing.T, dir, name string) bool {
	t.Helper()
	return hasRef(dir, "refs/tags/"+name)
}

// ValidateTagName layers the command-line rules over the ref-format rules:
// dash-leading, whitespace-holding and slash-ending names are rejected before
// the ref-format problems apply, and valid names pass.
func TestTagSafeValidateTagName(t *testing.T) {
	valid := []string{"v1.0", "release-2024", "feature/x", "ann_1", "@-abc"}
	for _, name := range valid {
		if p := ValidateTagName(name); p != "" {
			t.Errorf("ValidateTagName(%q) = %q, want empty", name, p)
		}
	}
	invalid := []struct {
		name string
		want string
	}{
		{"-v", "cannot begin with '-'"},
		{"--delete", "cannot begin with '-'"},
		{"has space", "cannot contain whitespace"},
		{"has\ttab", "cannot contain whitespace"},
		{"has\nnewline", "cannot contain whitespace"},
		{"feature/", "cannot end with '/'"},
	}
	for _, tc := range invalid {
		if p := ValidateTagName(tc.name); !strings.Contains(p, tc.want) {
			t.Errorf("ValidateTagName(%q) = %q, want it to contain %q", tc.name, p, tc.want)
		}
	}
	// Ref-format problems pass through as the layer underneath: whatever
	// RefNameProblem reports for the same name is what ValidateTagName says.
	if got, want := ValidateTagName("a..b"), RefNameProblem("a..b"); got == "" || got != want {
		t.Errorf("ValidateTagName(%q) = %q, want the non-empty RefNameProblem answer %q", "a..b", got, want)
	}
	// An empty name is not a problem of the name: creating needs one at all.
	if p := ValidateTagName(""); p != "" {
		t.Errorf("ValidateTagName(empty) = %q, want empty", p)
	}
}

// SafeCreateTag creates lightweight and annotated tags on the exact commit
// the target revision resolves to, including non-HEAD revspecs.
func TestTagSafeCreateLightweightAndAnnotated(t *testing.T) {
	dir, r, first, second := tagTestRepo(t)

	// A lightweight tag with an empty message names the commit directly.
	if err := r.SafeCreateTag("lw", "HEAD", ""); err != nil {
		t.Fatalf("SafeCreateTag lightweight: %v", err)
	}
	if got := gitOut(t, dir, "cat-file", "-t", "refs/tags/lw"); got != "commit" {
		t.Fatalf("lw object type = %q, want commit (lightweight)", got)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/lw"); got != second {
		t.Fatalf("lw target = %q, want HEAD %q", got, second)
	}

	// A whitespace-only message is still a lightweight tag.
	if err := r.SafeCreateTag("lw-blank", "HEAD", "   \n"); err != nil {
		t.Fatalf("SafeCreateTag blank message: %v", err)
	}
	if got := gitOut(t, dir, "cat-file", "-t", "refs/tags/lw-blank"); got != "commit" {
		t.Fatalf("lw-blank object type = %q, want commit (lightweight)", got)
	}

	// A non-empty message makes an annotated tag object carrying it.
	if err := r.SafeCreateTag("ann", "HEAD", "release notes\n"); err != nil {
		t.Fatalf("SafeCreateTag annotated: %v", err)
	}
	if got := gitOut(t, dir, "cat-file", "-t", "refs/tags/ann"); got != "tag" {
		t.Fatalf("ann object type = %q, want tag (annotated)", got)
	}
	if got := gitOut(t, dir, "for-each-ref", "--format=%(contents)", "refs/tags/ann"); strings.TrimSpace(got) != "release notes" {
		t.Fatalf("ann message = %q, want %q", got, "release notes")
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/ann^{}"); got != second {
		t.Fatalf("ann commit = %q, want HEAD %q", got, second)
	}

	// A revspec names the commit it resolves to, not HEAD: both branch names
	// and suffix forms resolve through the same peel.
	if err := r.SafeCreateTag("on-first", "HEAD~1", ""); err != nil {
		t.Fatalf("SafeCreateTag revspec: %v", err)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/on-first"); got != first {
		t.Fatalf("on-first target = %q, want first commit %q", got, first)
	}
	if err := r.SafeCreateTag("on-main", "main", ""); err != nil {
		t.Fatalf("SafeCreateTag branch name: %v", err)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/on-main"); got != second {
		t.Fatalf("on-main target = %q, want HEAD %q", got, second)
	}
	// An annotated tag can itself be the target revision: the peel lands on
	// the commit underneath.
	if err := r.SafeCreateTag("on-ann", "ann", ""); err != nil {
		t.Fatalf("SafeCreateTag onto annotated target: %v", err)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/on-ann"); got != second {
		t.Fatalf("on-ann target = %q, want %q", got, second)
	}
}

// SafeCreateTag never overwrites: a tag that exists — lightweight or
// annotated — stays untouched and the create is an error.
func TestTagSafeCreateRefusesToOverwrite(t *testing.T) {
	dir, r, _, second := tagTestRepo(t)

	if err := r.SafeCreateTag("lw", "HEAD", ""); err != nil {
		t.Fatalf("create lw: %v", err)
	}
	if err := r.SafeCreateTag("ann", "HEAD", "annotated\n"); err != nil {
		t.Fatalf("create ann: %v", err)
	}

	// Retargeting HEAD gives the overwrite something to move the tags to.
	writeFile(t, dir, "b.txt", "b\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-q", "-m", "third")

	for _, name := range []string{"lw", "ann"} {
		err := r.SafeCreateTag(name, "HEAD", "")
		if err == nil {
			t.Fatalf("SafeCreateTag over existing %s = nil error, want error", name)
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("overwrite refusal for %s = %q, want it to say the tag exists", name, err)
		}
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/lw"); got != second {
		t.Fatalf("lw moved to %q, want unchanged %q", got, second)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/ann^{}"); got != second {
		t.Fatalf("ann commit moved to %q, want unchanged %q", got, second)
	}
	if got := gitOut(t, dir, "for-each-ref", "--format=%(contents)", "refs/tags/ann"); strings.TrimSpace(got) != "annotated" {
		t.Fatalf("ann message changed to %q, want unchanged", got)
	}
}

// SafeCreateTag rejects targets that are not commits — a missing revision, a
// revspec peeling to a tree, an unborn HEAD — and names failing validation,
// in every case without creating anything.
func TestTagSafeCreateRejectsBadTargetsAndNames(t *testing.T) {
	dir, r, first, _ := tagTestRepo(t)

	badTargets := []struct {
		name string
		rev  string
	}{
		{"missing revision", "no-such-rev"},
		{"tree revspec", "HEAD^{tree}"},
		{"dash-leading revision", "-master"},
		{"whitespace revision", "HEAD extra"},
		{"empty revision", ""},
	}
	for i, tc := range badTargets {
		name := fmt.Sprintf("bad-target-%d", i)
		if err := r.SafeCreateTag(name, tc.rev, ""); err == nil {
			t.Errorf("SafeCreateTag(%q, %q) = nil error, want error", name, tc.rev)
		}
		if tagExists(t, dir, name) {
			t.Errorf("%s: tag %s was created despite the bad target", tc.name, name)
		}
	}

	badNames := []string{"", "-v", "has space", "feature/", "a..b"}
	for i, name := range badNames {
		if err := r.SafeCreateTag(name, "HEAD", ""); err == nil {
			t.Errorf("SafeCreateTag(%q) = nil error, want error", name)
		}
		if i > 0 && tagExists(t, dir, name) {
			t.Errorf("tag %q was created despite failing validation", name)
		}
	}

	// An unborn HEAD is not a commit to tag.
	empty := initRepo(t)
	er := New(&LocalExecutor{Dir: empty})
	if err := er.SafeCreateTag("v1", "HEAD", ""); err == nil {
		t.Error("SafeCreateTag on unborn HEAD = nil error, want error")
	}
	if tagExists(t, empty, "v1") {
		t.Error("v1 was created on an unborn HEAD")
	}

	// A valid create on first proves the repository itself was usable all along.
	if err := r.SafeCreateTag("control", first, ""); err != nil {
		t.Fatalf("control create after rejections: %v", err)
	}
}

// tagScriptExecutor is a fake Executor scripted per command, recording every
// invocation so tests can prove which git commands ran — and which never did.
type tagScriptExecutor struct {
	answer func(args []string, stdin []byte) ([]byte, error)
	calls  [][]string
}

func (e *tagScriptExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	e.calls = append(e.calls, append([]string(nil), args...))
	return e.answer(args, stdin)
}

func (e *tagScriptExecutor) Describe() string { return "tag-script fake" }

func (e *tagScriptExecutor) ran(cmd string) bool {
	for _, call := range e.calls {
		if len(call) > 0 && call[0] == cmd {
			return true
		}
	}
	return false
}

const tagFakeOID = "0123456789abcdef0123456789abcdef01234567"

// When verifying whether the tag exists fails with exit 128, exit 255 or an
// error that is not a git exit status at all, SafeCreateTag returns the
// failure instead of treating it as "does not exist" — and never runs git tag.
func TestTagSafeCreateExistsCheckFailureIsError(t *testing.T) {
	cases := []struct {
		name    string
		showErr error
	}{
		{"exit128", &Error{Args: []string{"show-ref"}, ExitCode: 128, Stderr: "fatal: bad object"}},
		{"exit255", &Error{Args: []string{"show-ref"}, ExitCode: 255, Stderr: "ssh: connection lost"}},
		{"nonGitError", errString("executor transport broke")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &tagScriptExecutor{answer: func(args []string, stdin []byte) ([]byte, error) {
				switch args[0] {
				case "rev-parse":
					return []byte(tagFakeOID + "\n"), nil
				case "show-ref":
					return nil, tc.showErr
				}
				return nil, fmt.Errorf("unexpected git %s", args[0])
			}}
			r := New(ex)
			err := r.SafeCreateTag("v1", "HEAD", "")
			if err == nil {
				t.Fatalf("SafeCreateTag = nil error with %s, want error", tc.name)
			}
			if strings.Contains(err.Error(), "already exists") {
				t.Fatalf("failure misread as 'already exists': %v", err)
			}
			if ex.ran("tag") {
				t.Fatal("git tag ran even though the existence check failed")
			}
		})
	}
}

// When the existence check answers exit 1 the tag does not exist and the
// create proceeds; exit 0 means the tag is there and nothing is created.
func TestTagSafeCreateExistsCheckExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		showErr  error
		wantTag  bool
		wantFail string
	}{
		{"exit1 creates", &Error{Args: []string{"show-ref"}, ExitCode: 1, Stderr: ""}, true, ""},
		{"exit0 refuses", nil, false, "already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &tagScriptExecutor{answer: func(args []string, stdin []byte) ([]byte, error) {
				switch args[0] {
				case "rev-parse":
					return []byte(tagFakeOID + "\n"), nil
				case "show-ref":
					return nil, tc.showErr
				case "tag":
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected git %s", args[0])
			}}
			r := New(ex)
			err := r.SafeCreateTag("v1", "HEAD", "")
			if tc.wantFail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantFail) {
					t.Fatalf("SafeCreateTag error = %v, want it to contain %q", err, tc.wantFail)
				}
			} else if err != nil {
				t.Fatalf("SafeCreateTag error = %v, want nil", err)
			}
			if got := ex.ran("tag"); got != tc.wantTag {
				t.Fatalf("git tag ran = %v, want %v", got, tc.wantTag)
			}
			// The name and the verified commit id go behind "--", never as
			// options and never the revspec text itself.
			for _, call := range ex.calls {
				if len(call) > 0 && call[0] == "tag" {
					want := []string{"tag", "--", "v1", tagFakeOID}
					if strings.Join(call, " ") != strings.Join(want, " ") {
						t.Fatalf("git tag invocation = %q, want %q", strings.Join(call, " "), strings.Join(want, " "))
					}
				}
			}
		})
	}
}

// TagRefInfo reads lightweight and annotated tags: the exact ref object, the
// commit, the kind, and the annotated metadata.
func TestTagSafeRefInfo(t *testing.T) {
	dir, r, _, second := tagTestRepo(t)
	gitRun(t, dir, "tag", "lw", "HEAD")
	gitRun(t, dir, "tag", "-a", "ann", "-m", "annotated release", "HEAD")

	lw, err := r.TagRefInfo("lw")
	if err != nil {
		t.Fatalf("TagRefInfo(lw): %v", err)
	}
	if lw.Annotated {
		t.Error("lw Annotated = true, want false")
	}
	if lw.RefOID != second || lw.CommitOID != second {
		t.Errorf("lw RefOID %q CommitOID %q, want both HEAD %q", lw.RefOID, lw.CommitOID, second)
	}
	if lw.Name != "lw" {
		t.Errorf("lw Name = %q", lw.Name)
	}

	ann, err := r.TagRefInfo("ann")
	if err != nil {
		t.Fatalf("TagRefInfo(ann): %v", err)
	}
	if !ann.Annotated {
		t.Error("ann Annotated = false, want true")
	}
	// The annotated ref names the tag object, distinct from the commit.
	if ann.RefOID == ann.CommitOID {
		t.Errorf("ann RefOID %q equals CommitOID %q, want the tag object and the commit", ann.RefOID, ann.CommitOID)
	}
	if ann.CommitOID != second {
		t.Errorf("ann CommitOID = %q, want HEAD %q", ann.CommitOID, second)
	}
	if strings.TrimSpace(ann.Message) != "annotated release" {
		t.Errorf("ann Message = %q, want %q", ann.Message, "annotated release")
	}
	if ann.Tagger == "" {
		t.Error("ann Tagger empty, want the configured tagger")
	}
	if ann.RefOID != gitOut(t, dir, "rev-parse", "refs/tags/ann") {
		t.Errorf("ann RefOID = %q, want the exact ref object %q", ann.RefOID, gitOut(t, dir, "rev-parse", "refs/tags/ann"))
	}

	// A missing tag and an empty name are errors, never partial answers.
	if _, err := r.TagRefInfo("no-such-tag"); err == nil {
		t.Error("TagRefInfo(no-such-tag) = nil error, want error")
	}
	if _, err := r.TagRefInfo("  "); err == nil {
		t.Error("TagRefInfo(blank) = nil error, want error")
	}
}

// TagRefInfo fails on malformed or mismatched show-ref answers rather than
// trusting them: a revspec-shaped answer naming a different ref is not the
// tag asked about.
func TestTagSafeRefInfoRejectsMismatchedAnswers(t *testing.T) {
	cases := []struct {
		name    string
		showOut string
		showErr error
	}{
		{"wrong ref in answer", tagFakeOID + " refs/tags/other\n", nil},
		{"no object id", "nothex refs/tags/v1\n", nil},
		{"empty answer", "", nil},
		{"read failure", "", &Error{Args: []string{"show-ref"}, ExitCode: 128, Stderr: "fatal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &tagScriptExecutor{answer: func(args []string, stdin []byte) ([]byte, error) {
				if args[0] == "show-ref" {
					return []byte(tc.showOut), tc.showErr
				}
				return nil, fmt.Errorf("unexpected git %s", args[0])
			}}
			r := New(ex)
			if _, err := r.TagRefInfo("v1"); err == nil {
				t.Fatalf("TagRefInfo = nil error for %s, want error", tc.name)
			}
		})
	}
}

// SafeDeleteTag deletes a tag whose verified state matches, through git tag
// -d with the name behind "--".
func TestTagSafeDelete(t *testing.T) {
	dir, r, _, _ := tagTestRepo(t)
	gitRun(t, dir, "tag", "v1", "HEAD")

	info, err := r.TagRefInfo("v1")
	if err != nil {
		t.Fatalf("TagRefInfo(v1): %v", err)
	}
	if err := r.SafeDeleteTag("v1", info.RefOID, nil); err != nil {
		t.Fatalf("SafeDeleteTag(v1): %v", err)
	}
	if tagExists(t, dir, "v1") {
		t.Fatal("v1 survived a matching safe delete")
	}
}

// A tag whose name starts with "-" — the ref store holds any name, whatever
// git tag's own parser accepts — is deleted as a tag, never parsed as
// command-line options: without the "--" separator git tag -d -v is an option
// conflict error, not the deletion of a tag named -v.
func TestTagSafeDeleteDashNamedTags(t *testing.T) {
	for _, name := range []string{"-v", "-l"} {
		t.Run(name, func(t *testing.T) {
			dir, r, _, _ := tagTestRepo(t)
			gitRun(t, dir, "update-ref", "refs/tags/"+name, "HEAD")
			if !tagExists(t, dir, name) {
				t.Fatalf("fixture: dash tag %s was not created through update-ref", name)
			}
			info, err := r.TagRefInfo(name)
			if err != nil {
				t.Fatalf("TagRefInfo(%s): %v", name, err)
			}
			if err := r.SafeDeleteTag(name, info.RefOID, nil); err != nil {
				t.Fatalf("SafeDeleteTag(%s): %v", name, err)
			}
			if tagExists(t, dir, name) {
				t.Fatalf("dash tag %s survived a matching safe delete: the name was treated as options", name)
			}
		})
	}

	// The "--" separator is on the command line itself, verified through a
	// fake executor answering a matching show-ref.
	ex := &tagScriptExecutor{answer: func(args []string, stdin []byte) ([]byte, error) {
		switch args[0] {
		case "show-ref":
			return []byte(tagFakeOID + " refs/tags/-v\n"), nil
		case "tag":
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected git %s", args[0])
	}}
	if err := New(ex).SafeDeleteTag("-v", tagFakeOID, nil); err != nil {
		t.Fatalf("SafeDeleteTag scripted: %v", err)
	}
	want := "tag -d -- -v"
	found := false
	for _, call := range ex.calls {
		if len(call) > 0 && call[0] == "tag" {
			found = true
			if got := strings.Join(call, " "); got != want {
				t.Fatalf("delete invocation = %q, want %q", got, want)
			}
		}
	}
	if !found {
		t.Fatal("git tag -d never ran for the matching scripted delete")
	}
}

// SafeDeleteTag refuses, without deleting anything, when the tag moved since
// the check: retargeted to another commit, or deleted and recreated onto the
// same commit — an annotated recreate is a new tag object with a new id, and
// the confirmed tag is gone either way.
func TestTagSafeDeleteRefusesChangedTags(t *testing.T) {
	dir, r, first, second := tagTestRepo(t)
	gitRun(t, dir, "tag", "moved", "HEAD")

	info, err := r.TagRefInfo("moved")
	if err != nil {
		t.Fatalf("TagRefInfo(moved): %v", err)
	}
	// Retarget the tag onto the first commit after the check answered.
	gitRun(t, dir, "update-ref", "refs/tags/moved", first)
	if err := r.SafeDeleteTag("moved", info.RefOID, nil); err == nil {
		t.Fatal("SafeDeleteTag of a retargeted tag = nil error, want error")
	}
	if !tagExists(t, dir, "moved") {
		t.Fatal("retargeted tag was deleted despite the changed object id")
	}
	if got := gitOut(t, dir, "rev-parse", "refs/tags/moved"); got != first {
		t.Fatalf("moved target = %q, want the retargeted %q untouched", got, first)
	}

	// Annotated: delete and recreate onto the same commit. The commit does
	// not change but the tag object id does — the recreated tag is not the
	// one the user confirmed.
	gitRun(t, dir, "tag", "-a", "ann", "-m", "original", "HEAD")
	annInfo, err := r.TagRefInfo("ann")
	if err != nil {
		t.Fatalf("TagRefInfo(ann): %v", err)
	}
	gitRun(t, dir, "tag", "-d", "ann")
	gitRun(t, dir, "tag", "-a", "ann", "-m", "recreated", "HEAD")
	if got := gitOut(t, dir, "rev-parse", "refs/tags/ann^{}"); got != second {
		t.Fatalf("fixture: recreated ann commit = %q, want HEAD %q", got, second)
	}
	err = r.SafeDeleteTag("ann", annInfo.RefOID, nil)
	if err == nil {
		t.Fatal("SafeDeleteTag of a recreated tag = nil error, want error")
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Fatalf("recreate refusal = %q, want it to say the tag changed", err)
	}
	if !tagExists(t, dir, "ann") {
		t.Fatal("recreated tag was deleted even though it is not the confirmed one")
	}
	if got := gitOut(t, dir, "for-each-ref", "--format=%(contents)", "refs/tags/ann"); strings.TrimSpace(got) != "recreated" {
		t.Fatalf("ann message = %q, want the recreated tag untouched", got)
	}
}

// SafeDeleteTag refuses without running a single git command when the inputs
// carry no verified answer: a failed check, an empty name, an empty object
// id.
func TestTagSafeDeleteRefusalsRunNoGit(t *testing.T) {
	cases := []struct {
		name     string
		tag      string
		expected string
		checkErr error
	}{
		{"failed check", "v1", tagFakeOID, errString("simulated verification failure")},
		{"empty name", "", tagFakeOID, nil},
		{"empty object id", "v1", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &tagScriptExecutor{answer: func(args []string, stdin []byte) ([]byte, error) {
				return nil, fmt.Errorf("no git command may run for %s", tc.name)
			}}
			err := New(ex).SafeDeleteTag(tc.tag, tc.expected, tc.checkErr)
			if err == nil {
				t.Fatalf("SafeDeleteTag = nil error for %s, want error", tc.name)
			}
			if len(ex.calls) != 0 {
				t.Fatalf("%s: git ran despite the refusal: %v", tc.name, ex.calls)
			}
		})
	}
}

// When the delete-time re-read of the tag fails — the tag vanished, the
// object store broke, the connection dropped — the tag is never deleted:
// an answer the repository cannot give is never permission.
func TestTagSafeDeleteReadFailureProtects(t *testing.T) {
	dir, r, _, _ := tagTestRepo(t)
	gitRun(t, dir, "tag", "v1", "HEAD")

	info, err := r.TagRefInfo("v1")
	if err != nil {
		t.Fatalf("TagRefInfo(v1): %v", err)
	}

	// The tag vanishes between the check and the delete.
	gitRun(t, dir, "tag", "-d", "v1")
	if err := r.SafeDeleteTag("v1", info.RefOID, nil); err == nil {
		t.Fatal("SafeDeleteTag of a vanished tag = nil error, want error")
	}

	// The repository breaks between the check and the delete: the re-read
	// cannot answer, so nothing may be deleted. Deleting a loose ref is a
	// ref-file operation that needs no objects — exactly why the re-read must
	// gate the delete. The ref file surviving proves nothing was removed.
	gitRun(t, dir, "tag", "v2", "HEAD")
	info2, err := r.TagRefInfo("v2")
	if err != nil {
		t.Fatalf("TagRefInfo(v2): %v", err)
	}
	objects := filepath.Join(dir, ".git", "objects")
	backup := filepath.Join(dir, ".git", "objects-backup")
	if err := os.Rename(objects, backup); err != nil {
		t.Fatalf("move object store away: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(backup, objects) })
	err = r.SafeDeleteTag("v2", info2.RefOID, nil)
	if err == nil {
		t.Fatal("SafeDeleteTag with a broken repository = nil error, want error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git", "refs", "tags", "v2")); statErr != nil {
		t.Fatalf("v2 ref file is gone even though the re-read failed: %v", statErr)
	}
}

// The delete-time verification reads the exact ref, not a revspec: a name
// with revision suffixes never passes as the tag, because show-ref matches
// exact refs only.
func TestTagSafeDeleteRevspecIsNotATag(t *testing.T) {
	dir, r, _, _ := tagTestRepo(t)
	gitRun(t, dir, "tag", "v1", "HEAD")

	info, err := r.TagRefInfo("v1")
	if err != nil {
		t.Fatalf("TagRefInfo(v1): %v", err)
	}
	if err := r.SafeDeleteTag("v1~0", info.RefOID, nil); err == nil {
		t.Fatal("SafeDeleteTag(v1~0) = nil error, want error: a revspec is not a tag")
	}
	if !tagExists(t, dir, "v1") {
		t.Fatal("v1 was deleted through a revspec-shaped name")
	}
}
