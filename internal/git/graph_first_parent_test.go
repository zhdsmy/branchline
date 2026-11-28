package git

import (
	"reflect"
	"testing"
)

// TestFirstParentGraphLinearInputMatchesGraph checks that with no merge
// commit in the input the first-parent truncation is a no-op: laying out a
// purely linear history with FirstParentGraph — the mode the filter is off
// in — produces exactly the rows Graph produces for the same commits.
func TestFirstParentGraphLinearInputMatchesGraph(t *testing.T) {
	commits := []Commit{
		{Hash: "e", Parents: []string{"d"}},
		{Hash: "d", Parents: []string{"c"}},
		{Hash: "c", Parents: []string{"b"}},
		{Hash: "b", Parents: []string{"a"}},
		{Hash: "a"},
	}
	got := FirstParentGraph(commits)
	want := Graph(commits)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FirstParentGraph of a merge-free input differs from Graph:\ngot  %+v\nwant %+v", got, want)
	}
}

// TestFirstParentGraphMergesSingleLane checks that a linear first-parent
// chain whose walk includes merge commits uses only lane 0 in every row:
// the merged-in second parents are not in the walk, so no line may reach
// sideways toward them, and the layout narrows to the one lane the chain
// occupies. The input's Parents slices stay complete — Cherry-pick, Revert
// and the commit diff read them.
func TestFirstParentGraphMergesSingleLane(t *testing.T) {
	commits := []Commit{
		{Hash: "f", Parents: []string{"e"}},
		{Hash: "e", Parents: []string{"d", "s2"}},
		{Hash: "d", Parents: []string{"c"}},
		{Hash: "c", Parents: []string{"b", "s1"}},
		{Hash: "b", Parents: []string{"a"}},
		{Hash: "a"},
	}
	rows := FirstParentGraph(commits)
	if len(rows) != len(commits) {
		t.Fatalf("rows = %d, want %d", len(rows), len(commits))
	}
	for i, row := range rows {
		if row.Lane != 0 {
			t.Errorf("row %d lane = %d, want 0", i, row.Lane)
		}
		if row.Width != 1 {
			t.Errorf("row %d width = %d, want 1 (segments %+v)", i, row.Width, row.Segments)
		}
		for _, s := range row.Segments {
			if s.From != 0 || s.To != 0 {
				t.Errorf("row %d segment %+v reaches outside lane 0", i, s)
			}
		}
	}
	// The merge rows keep exactly one bottom segment — toward the first
	// parent — where Graph draws a second one per merged-in parent.
	for _, i := range []int{1, 3} {
		bottoms := 0
		for _, s := range rows[i].Segments {
			if s.Kind == SegmentBottom {
				bottoms++
			}
		}
		if bottoms != 1 {
			t.Errorf("merge row %d bottom segments = %d, want 1", i, bottoms)
		}
	}
	// None of the input commits lost parents: the truncation lives only in
	// the copies the layout reads.
	wantParents := [][]string{{"e"}, {"d", "s2"}, {"c"}, {"b", "s1"}, {"a"}, nil}
	for i, c := range commits {
		if len(c.Parents) != len(wantParents[i]) {
			t.Fatalf("commit %d parents mutated: %+v", i, c.Parents)
		}
		for j, p := range c.Parents {
			if p != wantParents[i][j] {
				t.Fatalf("commit %d parent %d = %q, want %q", i, j, p, wantParents[i][j])
			}
		}
	}
	// Graph itself, reading the same complete parents, still fans the merges
	// out into side lanes: the narrowing is FirstParentGraph's alone.
	full := Graph(commits)
	wide := false
	for _, row := range full {
		if row.Width > 1 {
			wide = true
		}
	}
	if !wide {
		t.Fatal("Graph of the same commits uses one lane, want the merge side lanes it always draws")
	}
}

// TestFirstParentGraphRealRepoWalk checks the real walk the filter drives:
// git log --first-parent of a repository whose first-parent chain interleaves
// merge commits lays out with every row inside lane 0 — no dangling second-
// parent lines — while plain Graph of the same first-parent walk leaves the
// side lanes the second parents open hanging past the walk's end.
func TestFirstParentGraphRealRepoWalk(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})
	commits, err := r.Log(LogOptions{FirstParent: true, Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatalf("first-parent walk: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("first-parent walk returned no commits")
	}
	mergeSeen := false
	for _, c := range commits {
		if len(c.Parents) > 1 {
			mergeSeen = true
		}
	}
	if !mergeSeen {
		t.Fatal("fixture walk has no merge commit: the test proves nothing")
	}

	rows := FirstParentGraph(commits)
	for i, row := range rows {
		if row.Width != 1 || row.Lane != 0 {
			t.Errorf("row %d lane %d width %d, want lane 0 width 1", i, row.Lane, row.Width)
		}
		for _, s := range row.Segments {
			if s.From != 0 || s.To != 0 {
				t.Errorf("row %d segment %+v outside lane 0: a second-parent line survived", i, s)
			}
		}
	}

	// Plain Graph of the identical walk — no first-parent lane layout —
	// keeps a lane the merged-in parents open waiting for commits the walk
	// never lists: rows wider than one lane appear, which is what the lines
	// hanging without an end look like in the list.
	hanging := false
	for _, row := range Graph(commits) {
		if row.Width > 1 {
			hanging = true
		}
	}
	if !hanging {
		t.Fatal("Graph of the first-parent walk stays one lane wide, want the hanging side lanes")
	}
}

// TestFirstParentGraphUnbornHeadLogEmpty checks the read behind the filtered
// empty state: a HEAD-only walk on an unborn HEAD — a fresh orphan branch in
// a repository whose other branches have commits — returns no commits and no
// error instead of failing the load, while a caller-chosen revision that
// cannot resolve still fails as before.
func TestFirstParentGraphUnbornHeadLogEmpty(t *testing.T) {
	dir := testRepo(t)
	r := New(&LocalExecutor{Dir: dir})

	commits, err := r.Log(LogOptions{Revs: []string{"HEAD"}, FirstParent: true})
	if err != nil {
		t.Fatalf("HEAD walk on the born repository: %v", err)
	}
	if len(commits) == 0 {
		t.Fatal("born repository: HEAD walk returned no commits")
	}

	runGit(t, dir, "checkout", "-q", "--orphan", "fresh")
	commits, err = r.Log(LogOptions{Revs: []string{"HEAD"}, FirstParent: true})
	if err != nil {
		t.Fatalf("unborn HEAD walk failed instead of answering empty: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("unborn HEAD walk returned %d commits, want 0", len(commits))
	}
	commits, err = r.Log(LogOptions{Revs: []string{"HEAD"}})
	if err != nil {
		t.Fatalf("unborn HEAD walk without first-parent failed: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("unborn HEAD walk without first-parent returned %d commits, want 0", len(commits))
	}

	// A revision that is not exactly HEAD keeps failing: the empty answer is
	// only the unborn HEAD's, never a corrupt or mistyped revision's.
	if _, err := r.Log(LogOptions{Revs: []string{"main", "HEAD"}}); err == nil {
		t.Fatal("walk of main plus the unborn HEAD succeeded, want the strict failure")
	}
	if _, err := r.Log(LogOptions{Revs: []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}}); err == nil {
		t.Fatal("walk of a missing revision succeeded, want the failure")
	}
}
