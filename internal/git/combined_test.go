package git

import "testing"

func TestCombineMergesStagedAndUnstagedByPath(t *testing.T) {
	staged := []FileChange{{Path: "b.txt", Status: 'M', Staged: true}, {Path: "c.txt", Status: 'A', Staged: true}}
	unstaged := []FileChange{{Path: "a.txt", Status: '?'}, {Path: "c.txt", Status: 'M'}}
	got := Combine(staged, unstaged)
	if len(got) != 3 {
		t.Fatalf("%d rows: %+v", len(got), got)
	}
	want := []struct {
		path           string
		checked, mixed bool
		status         byte
		index          int
	}{
		{"a.txt", false, false, '?', 0},
		{"b.txt", true, false, 'M', 0},
		{"c.txt", false, true, 'M', 1},
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.path || g.Checked() != w.checked || g.Mixed() != w.mixed || g.Change.Status != w.status || g.Index != w.index {
			t.Errorf("row %d: %+v, want %+v", i, g, w)
		}
	}
}
