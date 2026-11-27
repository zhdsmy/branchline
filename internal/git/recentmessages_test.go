package git

import (
	"reflect"
	"testing"
)

func TestRecentMessagesAreNewestFirstWithoutRepeats(t *testing.T) {
	dir := initRepo(t)
	for _, m := range []string{"first", "fix typo", "second", "fix typo"} {
		gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", m)
	}
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "third", "-m", "with a description")
	r := New(&LocalExecutor{Dir: dir})

	got, err := r.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"third\n\nwith a description", "fix typo", "second", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, _ := r.RecentMessages(2); len(got) != 2 {
		t.Fatalf("limit: %q", got)
	}
}

func TestRecentMessagesOfAnEmptyRepository(t *testing.T) {
	r := New(&LocalExecutor{Dir: initRepo(t)})
	if got, _ := r.RecentMessages(10); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
