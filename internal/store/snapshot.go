package store

import (
	"bytes"
	"encoding/json"
	"slices"
)

// Copies crossing the store boundary must own their collections. In particular,
// editing a settings dialog must not mutate live preferences before saving.
func cloneSettings(v Settings) Settings {
	v.SourceFolders = slices.Clone(v.SourceFolders)
	v.SSHKeys = slices.Clone(v.SSHKeys)
	v.SSHKeyFiles = slices.Clone(v.SSHKeyFiles)
	v.MergeTools = slices.Clone(v.MergeTools)
	v.Commands = slices.Clone(v.Commands)
	for i := range v.Commands {
		v.Commands[i] = CloneCommand(v.Commands[i])
	}
	return v
}

func cloneRepoSettings(v RepoSettings) RepoSettings {
	v.NoAutoFetch = slices.Clone(v.NoAutoFetch)
	v.SidebarClosed = slices.Clone(v.SidebarClosed)
	v.SidebarSections.Branches = cloneBool(v.SidebarSections.Branches)
	v.SidebarSections.Remotes = cloneBool(v.SidebarSections.Remotes)
	v.SidebarSections.Tags = cloneBool(v.SidebarSections.Tags)
	v.SidebarSections.Stashes = cloneBool(v.SidebarSections.Stashes)
	v.SidebarSections.Worktrees = cloneBool(v.SidebarSections.Worktrees)
	v.SidebarSections.Submodules = cloneBool(v.SidebarSections.Submodules)
	v.IssueRules = slices.Clone(v.IssueRules)
	v.RemoteAccounts = slices.Clone(v.RemoteAccounts)
	v.Layout.HistoryColumns = cloneColumnWidths(v.Layout.HistoryColumns)
	v.Layout.FileHistoryColumns = cloneColumnWidths(v.Layout.FileHistoryColumns)
	return v
}

func cloneColumnWidths(v map[string]float32) map[string]float32 {
	if v == nil {
		return nil
	}
	out := make(map[string]float32, len(v))
	for key, width := range v {
		out[key] = width
	}
	return out
}

func cloneRepo(v Repo) Repo {
	v.Local = cloneRepoSettings(v.Local)
	return v
}

// CloneRepo returns a configuration the caller can retain while a worker runs.
func CloneRepo(v Repo) Repo { return cloneRepo(v) }

func cloneBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

// CloneCommand returns a command whose dialog can change without changing source.
func CloneCommand(cmd CustomCommand) CustomCommand {
	cmd.UI = cloneCommandUI(cmd.UI)
	return cmd
}

// CloneCommands returns commands whose dialogs can change independently.
func CloneCommands(cmds []CustomCommand) []CustomCommand {
	out := make([]CustomCommand, len(cmds))
	for i, cmd := range cmds {
		out[i] = CloneCommand(cmd)
	}
	return out
}

func cloneCommandUI(source *CommandUI) *CommandUI {
	if source == nil {
		return nil
	}
	ui := *source
	ui.Controls = slices.Clone(source.Controls)
	for i := range ui.Controls {
		ui.Controls[i].Options = slices.Clone(ui.Controls[i].Options)
	}
	ui.Buttons = slices.Clone(source.Buttons)
	return &ui
}

// SameCommand reports whether two command definitions and their dialogs match.
// Shared is runtime provenance, not part of the saved definition.
func SameCommand(a, b CustomCommand) bool {
	a.Shared, b.Shared = false, false
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

// SameCommands reports whether two command lists have the same definitions in order.
func SameCommands(a, b []CustomCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !SameCommand(a[i], b[i]) {
			return false
		}
	}
	return true
}
