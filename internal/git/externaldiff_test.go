package git

import (
	"context"
	"reflect"
	"testing"
)

type diffCommandRecorder struct{ args []string }

func (e *diffCommandRecorder) Describe() string { return "test repository" }
func (e *diffCommandRecorder) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	e.args = append([]string(nil), args...)
	return nil, nil
}

func TestExternalDiffUsesIndexForStagedFile(t *testing.T) {
	ex := &diffCommandRecorder{}
	r := New(ex)
	if err := r.ShowFileWith("custom", "", "", "index", "a b.txt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--literal-pathspecs", "difftool", "--no-prompt", "--tool=custom", "-M", "--cached", "--", "a b.txt"}
	if !reflect.DeepEqual(ex.args, want) {
		t.Fatalf("args %q", ex.args)
	}
	if err := r.ShowFileWith("custom", "", "HEAD", "", "a b.txt"); err != nil {
		t.Fatal(err)
	}
	want = []string{"--literal-pathspecs", "difftool", "--no-prompt", "--tool=custom", "-M", "HEAD", "--", "a b.txt"}
	if !reflect.DeepEqual(ex.args, want) {
		t.Fatalf("working tree args %q", ex.args)
	}
}
