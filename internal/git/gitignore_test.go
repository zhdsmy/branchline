package git

import (
	"strings"
	"testing"
)

func TestGitignoreTemplates(t *testing.T) {
	list := GitignoreTemplates()
	if len(list) != 24 {
		t.Fatalf("%d templates", len(list))
	}
	found := map[string]string{}
	for _, tpl := range list {
		if strings.TrimSpace(tpl.Patterns) == "" {
			t.Errorf("%s is empty", tpl.Name)
		}
		found[tpl.Name] = tpl.Patterns
	}
	if !strings.Contains(found["Go"], "vendor/") || !strings.Contains(found["macOS"], ".DS_Store") {
		t.Fatalf("Go %q macOS %q", found["Go"], found["macOS"])
	}
	if strings.Contains(found["Go"], "###") || strings.Contains(found["Go"], ".DS_Store") {
		t.Fatalf("templates run into each other: %q", found["Go"])
	}
}

func TestAddToGitignoreSkipsLinesThatAreThere(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, ".gitignore", "*.log\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "ignore logs")
	r := New(&LocalExecutor{Dir: dir})

	n, err := r.AddToGitignore("*.log\n\n  build/  \n# Mac\n.DS_Store\n")
	if err != nil || n != 2 {
		t.Fatalf("added %d patterns, err %v", n, err)
	}
	got, _ := r.ReadWorktree(".gitignore")
	if got != "*.log\nbuild/\n# Mac\n.DS_Store\n" {
		t.Fatalf(".gitignore is %q", got)
	}
	if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(staged, ".gitignore") {
		t.Fatalf("not staged: %q", staged)
	}

	if n, err := r.AddToGitignore("build/\n.DS_Store"); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}
