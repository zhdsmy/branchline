package app

import (
	"strings"
	"testing"

	"github.com/zhdsmy/branchline/internal/git"
)

func TestZZProbeEntire(t *testing.T) {
	repo := newGitRepo(t, "zz-entire")
	writeWhitespaceFixture(repo, "code.go", "var x = 1", "var y = 1")
	repo.Git("add", "code.go")
	repo.Git("commit", "-m", "base")
	writeWhitespaceFixture(repo, "code.go", "var x  =  1", "var y = 2")

	h := newDiffPanelHarness(t, repo.StoreRepo(), "zz-scope")
	h.panel.Load(h.session, h.scope, unstagedDiffRequest(git.FileChange{Path: "code.go", Status: 'M'}, "1"))
	h.drain()

	t.Logf("entire=%v ctx=%d key-end=%q", h.panel.req.EntireFile, h.panel.req.ContextLines, h.panel.loadedKey[len(h.panel.loadedKey)-20:])
	names := map[string]int{}
	for _, a := range h.ui.AccessAll() {
		if strings.HasPrefix(a.Name, "var ") || strings.HasPrefix(a.Name, "filler") || strings.HasPrefix(a.Name, "header") {
			names[a.Name]++
			t.Logf("role=%v name=%q y=%.0f h=%.0f", a.Role, a.Name, a.Bounds.Y, a.Bounds.H)
		}
	}
	t.Logf("distinct fixture lines visible: %d", len(names))
	t.Logf("state nil? %v hunks=%d", h.panel.state == nil, len(h.panel.patch.Hunks))
	for _, hk := range h.panel.patch.Hunks {
		t.Logf("hunk old=%d+%d new=%d+%d lines=%d", hk.OldStart, hk.OldLines, hk.NewStart, hk.NewLines, len(hk.Lines))
	}
	_ = strings.Repeat
}
