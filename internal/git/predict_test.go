package git

import "testing"

func TestPredictConflicts(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "checkout", "-q", "-b", "clash")
	writeFile(t, dir, "a.txt", "clash\n")
	gitRun(t, dir, "commit", "-qam", "clash")
	clash := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "main")
	gitRun(t, dir, "checkout", "-q", "-b", "fine")
	writeFile(t, dir, "b.txt", "bee\n")
	gitRun(t, dir, "add", "b.txt")
	gitRun(t, dir, "commit", "-qm", "fine")
	fine := gitOut(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "main")
	writeFile(t, dir, "a.txt", "main\n")
	gitRun(t, dir, "commit", "-qam", "main change")
	mainTip := gitOut(t, dir, "rev-parse", "HEAD")
	r := New(&LocalExecutor{Dir: dir})
	if _, err := r.MergeConflicts("fine", "main"); err == ErrPredictUnavailable {
		t.Skip("git is older than 2.38")
	}

	check := func(name string, got bool, err error, want bool) {
		t.Helper()
		if err != nil || got != want {
			t.Errorf("%s: conflicts %v, %v; want %v", name, got, err, want)
		}
	}
	got, err := r.MergeConflicts("clash", "main")
	check("merge clash", got, err, true)
	got, err = r.MergeConflicts("fine", "main")
	check("merge fine", got, err, false)
	got, err = r.CherryPickConflicts(clash, 0)
	check("pick clash", got, err, true)
	got, err = r.CherryPickConflicts(fine, 0)
	check("pick fine", got, err, false)
	// Reverting the commit that main's own change builds on conflicts.
	got, err = r.RevertConflicts(mainTip, 0)
	check("revert tip", got, err, false)
	gitRun(t, dir, "checkout", "-q", "fine")
	got, err = r.RebaseConflicts("clash")
	check("rebase fine onto clash", got, err, false)
	gitRun(t, dir, "checkout", "-q", "main")
	got, err = r.RebaseConflicts("clash")
	check("rebase main onto clash", got, err, true)
	// Nothing changed in the working tree or the branch.
	if gitOut(t, dir, "status", "--porcelain") != "" || gitOut(t, dir, "rev-parse", "HEAD") != mainTip {
		t.Error("prediction changed the repository")
	}
}
