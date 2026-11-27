package git

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// batchProbe records the arguments of every command a Repo runs through it
// and answers failure for the commands failOn names.
type batchProbe struct {
	args         [][]string
	failOn       map[string]bool
	failRevParse bool
}

func (p *batchProbe) Describe() string { return "batch probe" }

func (p *batchProbe) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	p.args = append(p.args, slices.Clone(args))
	if len(args) > 0 && p.failOn[args[0]] {
		return nil, &Error{Args: slices.Clone(args), ExitCode: 1, Stderr: "probe failure"}
	}
	if len(args) > 0 && args[0] == "rev-parse" && p.failRevParse {
		return nil, &Error{Args: slices.Clone(args), ExitCode: 128, Stderr: "fatal: ambiguous argument 'HEAD'"}
	}
	return nil, nil
}

func smallBatchLimit(t *testing.T, n int) {
	t.Helper()
	old := pathBatchLimit
	pathBatchLimit = n
	t.Cleanup(func() { pathBatchLimit = old })
}

func pathsAfterDash(t *testing.T, args []string) []string {
	t.Helper()
	dash := slices.Index(args, "--")
	if dash < 0 {
		t.Fatalf("command has no -- separator: %v", args)
	}
	return args[dash+1:]
}

func TestPathBatchExactLimit(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{}
	r := New(ex)
	// Each path measures its length plus one space: 6+4+2 = 12, exactly the
	// limit, so the three stay in one batch.
	if err := r.Stage("aaaaa", "bbb", "c"); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) != 1 {
		t.Fatalf("ran %d commands, want 1: %v", len(ex.args), ex.args)
	}
	want := []string{"add", "-A", "--", "aaaaa", "bbb", "c"}
	if !slices.Equal(ex.args[0], want) {
		t.Fatalf("command = %v, want %v", ex.args[0], want)
	}
}

func TestPathBatchOverLimitByOneByte(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{}
	r := New(ex)
	// The list measures 6+4+3 = 13, one byte over the limit. aaaaa and bbb
	// (10) fit in the first batch; cc would push it to 13, so it starts the
	// next one.
	if err := r.Stage("aaaaa", "bbb", "cc"); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) != 2 {
		t.Fatalf("ran %d commands, want 2: %v", len(ex.args), ex.args)
	}
	if got := pathsAfterDash(t, ex.args[0]); !slices.Equal(got, []string{"aaaaa", "bbb"}) {
		t.Fatalf("first batch = %v", got)
	}
	if got := pathsAfterDash(t, ex.args[1]); !slices.Equal(got, []string{"cc"}) {
		t.Fatalf("second batch = %v", got)
	}
}

func TestPathBatchSingleOversizedPath(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{}
	r := New(ex)
	big := strings.Repeat("x", 20)
	if err := r.Stage("aa", big, "bb"); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) != 3 {
		t.Fatalf("ran %d commands, want 3: %v", len(ex.args), ex.args)
	}
	var batches [][]string
	for _, args := range ex.args {
		batches = append(batches, pathsAfterDash(t, args))
	}
	want := [][]string{{"aa"}, {big}, {"bb"}}
	if !slices.EqualFunc(batches, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
}

func TestPathBatchEmptyListRunsNothing(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{}
	r := New(ex)
	if err := r.runPathBatches([]string{"add", "-A", "--"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) != 0 {
		t.Fatalf("ran %d commands for an empty list: %v", len(ex.args), ex.args)
	}
}

func TestPathBatchPrefixRepeatedAndOrderKept(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{}
	r := New(ex)
	paths := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"}
	if err := r.SetIndexFlag(AssumeUnchanged, true, paths...); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) < 2 {
		t.Fatalf("ran %d commands, want several batches: %v", len(ex.args), ex.args)
	}
	prefix := []string{"update-index", "--assume-unchanged", "--"}
	var all []string
	for _, args := range ex.args {
		if !slices.Equal(args[:len(prefix)], prefix) {
			t.Fatalf("command without the full prefix: %v", args)
		}
		all = append(all, args[len(prefix):]...)
	}
	if !slices.Equal(all, paths) {
		t.Fatalf("the order of the paths changed: %v", all)
	}
}

func TestPathBatchMeasuredQuoted(t *testing.T) {
	smallBatchLimit(t, 9)
	ex := &batchProbe{}
	r := New(ex)
	// Measured raw the list is (4+1)+(3+1) = 9, exactly the limit: it would
	// stay in one batch. Measured quoted, "a b" is 'a b' — five bytes — and
	// the list is (4+1)+(5+1) = 11, over the limit, so the two split.
	if err := r.Stage("aaaa", "a b"); err != nil {
		t.Fatal(err)
	}
	if len(ex.args) != 2 {
		t.Fatalf("ran %d commands, want 2 when measuring the quoted lengths: %v", len(ex.args), ex.args)
	}
	if got := pathsAfterDash(t, ex.args[0]); !slices.Equal(got, []string{"aaaa"}) {
		t.Fatalf("first batch = %v", got)
	}
	if got := pathsAfterDash(t, ex.args[1]); !slices.Equal(got, []string{"a b"}) {
		t.Fatalf("second batch = %v", got)
	}
}

func TestPathBatchShortListSingleCommand(t *testing.T) {
	// The real limit: a handful of paths is one command, identical to what
	// an unbatched call would have run.
	ex := &batchProbe{}
	r := New(ex)
	if err := r.Stage("one.txt", "two.txt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"add", "-A", "--", "one.txt", "two.txt"}
	if len(ex.args) != 1 || !slices.Equal(ex.args[0], want) {
		t.Fatalf("commands = %v, want one %v", ex.args, want)
	}
	if err := r.Unstage("one.txt", "two.txt"); err != nil {
		t.Fatal(err)
	}
	// After the HEAD probe the unstage runs as the one command an unbatched
	// call would have run.
	want = []string{"reset", "-q", "HEAD", "--", "one.txt", "two.txt"}
	if len(ex.args) != 3 || !slices.Equal(ex.args[2], want) {
		t.Fatalf("commands = %v, want unstage %v", ex.args, want)
	}
}

func TestPathBatchStopsAtFirstError(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{failOn: map[string]bool{"add": true}}
	r := New(ex)
	// Every batch fails, so exactly one command must run: the batches after
	// the failed one never start, and the error is the failed batch's own.
	err := r.Stage("1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12")
	if err == nil {
		t.Fatal("no error")
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Stderr != "probe failure" {
		t.Fatalf("error = %v, want the batch's own error", err)
	}
	if len(ex.args) != 1 {
		t.Fatalf("ran %d commands, want exactly the first batch: %v", len(ex.args), ex.args)
	}
	if got := pathsAfterDash(t, ex.args[0]); len(got) == 0 {
		t.Fatal("the failed command carried no paths")
	}
}

func TestPathBatchUnstageWithoutHead(t *testing.T) {
	smallBatchLimit(t, 12)
	ex := &batchProbe{failRevParse: true}
	r := New(ex)
	paths := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	if err := r.Unstage(paths...); err != nil {
		t.Fatal(err)
	}
	prefix := []string{"rm", "--cached", "-r", "-q", "--"}
	if len(ex.args) < 3 {
		t.Fatalf("ran %d commands, want the probe plus several batches: %v", len(ex.args), ex.args)
	}
	if ex.args[0][0] != "rev-parse" {
		t.Fatalf("first command = %v, want the HEAD probe", ex.args[0])
	}
	var all []string
	for _, args := range ex.args[1:] {
		if !slices.Equal(args[:len(prefix)], prefix) {
			t.Fatalf("command without the rm --cached prefix: %v", args)
		}
		all = append(all, args[len(prefix):]...)
	}
	if !slices.Equal(all, paths) {
		t.Fatalf("paths = %v", all)
	}
}

func TestPathBatchDiscardKinds(t *testing.T) {
	smallBatchLimit(t, 10)
	ex := &batchProbe{}
	r := New(ex)
	files := []FileChange{
		{Path: "trk1", Status: 'M'},
		{Path: "trk2", Status: 'D'},
		{Path: "trk3", Status: 'M'},
		{Path: "u1", Status: '?'},
		{Path: "u2", Status: '?'},
		{Path: "i1", Status: '!'},
		{Path: "i2", Status: '!'},
	}
	if err := r.Discard(files...); err != nil {
		t.Fatal(err)
	}
	// The tracked paths measure 5 each: two fill a batch (10), the third
	// spills. The untracked and ignored pairs each fit in one batch.
	prefixes := map[string]int{}
	for _, args := range ex.args {
		dash := slices.Index(args, "--")
		if dash < 0 {
			t.Fatalf("command without --: %v", args)
		}
		prefixes[strings.Join(args[:dash], " ")]++
	}
	for want, n := range map[string]int{
		"--literal-pathspecs checkout":       2,
		"--literal-pathspecs clean -f -q":    1,
		"--literal-pathspecs clean -f -x -q": 1,
	} {
		if prefixes[want] != n {
			t.Fatalf("prefix %q ran %d times, want %d; all: %v", want, prefixes[want], n, ex.args)
		}
	}
	var tracked []string
	for _, args := range ex.args {
		if strings.HasPrefix(strings.Join(args, " "), "--literal-pathspecs checkout ") {
			tracked = append(tracked, pathsAfterDash(t, args)...)
		}
	}
	if !slices.Equal(tracked, []string{"trk1", "trk2", "trk3"}) {
		t.Fatalf("tracked batches = %v", tracked)
	}
}

func TestPathBatchRemoveBranches(t *testing.T) {
	smallBatchLimit(t, 10)
	ex := &batchProbe{}
	r := New(ex)
	err := r.RemoveBranches([]RemoveBranchOptions{
		{Name: "b1", Force: true},
		{Name: "b2", Force: true},
		{Name: "b3", Force: true},
		{Name: "b4", Force: true},
		{Name: "b5", Force: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix := []string{"branch", "-D", "--"}
	if len(ex.args) < 2 {
		t.Fatalf("ran %d commands, want batches: %v", len(ex.args), ex.args)
	}
	var all []string
	for _, args := range ex.args {
		if !slices.Equal(args[:len(prefix)], prefix) {
			t.Fatalf("command without the branch -D prefix: %v", args)
		}
		all = append(all, args[len(prefix):]...)
	}
	if !slices.Equal(all, []string{"b1", "b2", "b3", "b4", "b5"}) {
		t.Fatalf("branch names = %v", all)
	}
}
