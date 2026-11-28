package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/exttool"
	"github.com/zhdsmy/branchline/internal/git"
)

// maxMergeToolBytes caps how large a conflicted file can be for the external
// merge tool: the whole content crosses the snapshot, the tool and the write
// back, so the merge of a very large file belongs in the terminal instead.
const maxMergeToolBytes = 16 << 20

// mergeToolResult is a conflict resolution the merge tool produced that is
// still owed its write back: either the write could not start the moment the
// tool exited — another write in progress, or the user had switched
// repositories — or it started but never consumed the result, because only a
// successful write may. It survives in full: the content, and the exact
// conflict identity the inspection was read against, so the write back
// re-verifies the conflict the same way a direct write does. The identity is
// the one the Save write needs, never refreshed: a conflict that moved on
// reports the change instead of being overwritten.
type mergeToolResult struct {
	repoKey  string
	repoGen  uint64
	path     string
	content  string
	identity git.ConflictIdentity
}

// mergeToolUIState is the per-app run state of the external merge tool
// integration. All fields are owned by the UI thread: the worker pipeline of
// one open tool receives only captured values and posts its completion back,
// like the external diff's. running guards the one-at-a-time rule per file
// with the key of the repository the tool was opened from; results holds the
// resolutions waiting for their write back, by repository key and path.
type mergeToolUIState struct {
	running map[string]bool
	results map[string]mergeToolResult
}

// mergeToolUI returns the app's merge tool state, creating it on first use.
// The state survives repository switches: a merge finished while the user was
// looking at another repository keeps its result here until it is saved or
// discarded back in the repository it belongs to.
func mergeToolUI(a *app) *mergeToolUIState {
	return featureState(a, func() *mergeToolUIState {
		return &mergeToolUIState{
			running: map[string]bool{},
			results: map[string]mergeToolResult{},
		}
	})
}

// mergeToolKey is the map key of one conflicted file of one repository. The
// repository part is the store ID, or the kind and path of a repository that
// was never persisted: two checkouts of one path in different lists are the
// same conflict for the write back either way.
func mergeToolKey(repoKey, path string) string {
	return repoKey + "\x00" + path
}

// mergeToolRepoKey identifies the session's open repository for the merge
// tool state, by its store ID or, for a repository without one, its kind and
// path.
func mergeToolRepoKey(a *app) string {
	meta := a.session.Meta()
	if meta.ID != "" {
		return meta.ID
	}
	return string(meta.Kind) + "|" + meta.Path
}

// conflictMergeToolMenuItems is the external merge tool part of a conflicted
// row's context menu. A conflict whose resolution the tool produced but the
// write back missed — a write in progress, a repository switch — offers the
// result for saving or discarding instead of a fresh merge, so the resolution
// the user already made in the tool is never silently dropped or overwritten.
func (a *app) conflictMergeToolMenuItems(m *ui.Menu, f git.FileChange, stage git.ConflictStage, haveStage bool) {
	st := mergeToolUI(a)
	repoKey := mergeToolRepoKey(a)
	key := mergeToolKey(repoKey, f.Path)
	if _, ok := st.results[key]; ok {
		if m.Item("Save Merge Result").ID("merge-tool-save").Disabled(a.session.Busy()).Chosen() {
			a.saveMergeToolResult(key)
		}
		if m.Item("Discard Merge Result").ID("merge-tool-discard").Chosen() {
			a.discardMergeToolResult(key)
		}
	} else {
		// Modify/delete conflicts have no LOCAL or REMOTE to merge, and the
		// worktree file, a submodule or a symbolic link cannot be edited in a
		// text merge tool. A second open of the same file is refused: its
		// snapshot would be another merge of the same conflict the first tool
		// already edits.
		unavailable := a.session.Busy() || !haveStage ||
			!stage.HaveOurs() || !stage.HaveTheirs() || !stage.WorktreeExists() ||
			stage.IsSubmodule() || stage.BaseMode == "120000" || stage.OursMode == "120000" ||
			stage.TheirsMode == "120000" || stage.WorktreeMode == "120000" ||
			st.running[key]
		if m.Item("Open in Merge Tool").ID("merge-tool-open").Disabled(unavailable).Chosen() {
			a.openMergeTool(f)
		}
	}
	m.Separator()
}

// openMergeTool resolves the chosen merge tool and starts the pipeline of one
// conflicted file: the inspection read, the snapshot, the tool run, and the
// write back, all in one background worker detached from the session. The
// tool can stay open for minutes, so the worker neither takes the write busy
// state nor follows the repository generation; the write back happens through
// the ordinary doThen write of the session once the tool has exited.
func (a *app) openMergeTool(f git.FileChange) {
	if a.session.Busy() {
		return
	}
	repo := a.session.Repo()
	if repo == nil {
		return
	}
	st := mergeToolUI(a)
	repoKey := mergeToolRepoKey(a)
	key := mergeToolKey(repoKey, f.Path)
	if st.running[key] {
		return
	}
	st.running[key] = true
	repoGen := a.session.Generation()
	path := f.Path
	diffState := externalDiffUI(a)
	find := diffState.find
	parent := diffState.snapshotParent
	settings := a.store.Settings()
	toolName := settings.MergeTool
	if toolName == "" {
		toolName = defaultDiffTool
	}
	a.spawn(func() {
		a.runMergeTool(repo, repoKey, repoGen, path, toolName, settings.CustomMerge.Path, settings.CustomMerge.Args, find, parent)
	})
}

// runMergeTool is the worker half of the merge tool integration: it reads the
// conflict, writes the four versions into a snapshot on this Mac, runs the
// tool to its exit, and hands the outcome to the UI thread. The snapshot is
// deleted as soon as the tool exits, whatever the outcome, and the running
// mark of the file is always lifted with the completion, so a later click can
// merge the file again.
func (a *app) runMergeTool(repo *git.Repo, repoKey string, repoGen uint64, path, toolName, customPath, customArgs string, find exttool.Finder, parent string) {
	key := mergeToolKey(repoKey, path)
	defer a.post(func() {
		delete(mergeToolUI(a).running, key)
	})
	fail := func(err error) {
		text := err.Error()
		a.post(func() {
			a.toast = "Merge tool failed: " + git.RedactText(text)
		})
	}
	tool, err := exttool.ResolveMerge(toolName, customPath, customArgs, find)
	if err != nil {
		fail(err)
		return
	}
	ins, err := repo.InspectConflict(path, maxMergeToolBytes)
	if err != nil {
		fail(fmt.Errorf("%s: reading the conflict: %w", tool.Name, err))
		return
	}
	switch {
	case ins.Binary:
		fail(fmt.Errorf("%s: %s is a binary file: resolve it in the repository", tool.Name, path))
		return
	case ins.TooLarge:
		fail(fmt.Errorf("%s: %s is larger than %d MiB: resolve it in the repository", tool.Name, path, maxMergeToolBytes>>20))
		return
	}
	if parent == "" {
		parent = os.TempDir()
	}
	snap, err := exttool.NewSnapshot(parent)
	if err != nil {
		fail(fmt.Errorf("%s: creating the snapshot: %w", tool.Name, err))
		return
	}
	defer func() { _ = snap.Remove() }()
	baseFile, err := snap.Write("base", path, []byte(ins.BaseText), false)
	if err != nil {
		fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
		return
	}
	localFile, err := snap.Write("local", path, []byte(ins.OursText), false)
	if err != nil {
		fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
		return
	}
	remoteFile, err := snap.Write("remote", path, []byte(ins.TheirsText), false)
	if err != nil {
		fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
		return
	}
	mergedFile, err := snap.Write("merged", path, []byte(ins.WorktreeText), true)
	if err != nil {
		fail(fmt.Errorf("%s: writing the snapshot: %w", tool.Name, err))
		return
	}
	if err := exttool.RunMerge(context.Background(), tool, exttool.Vars{
		Base:   baseFile,
		Local:  localFile,
		Remote: remoteFile,
		Merged: mergedFile,
	}); err != nil {
		fail(mergeToolError(tool.Name, err))
		return
	}
	data, err := os.ReadFile(mergedFile)
	if err != nil {
		fail(fmt.Errorf("%s: reading the merge result: %w", tool.Name, err))
		return
	}
	result := mergeToolResult{
		repoKey:  repoKey,
		repoGen:  repoGen,
		path:     path,
		content:  string(data),
		identity: ins.Identity,
	}
	a.post(func() {
		a.finishMergeTool(result, tool.Name, ins.WorktreeText)
	})
}

// mergeToolError composes the failure of the tool run itself: the tool's name
// first, then its exit status and the end of its standard error.
func mergeToolError(toolName string, err error) error {
	var exitErr *exttool.ExitError
	if errors.As(err, &exitErr) {
		if stderr := strings.TrimSpace(exitErr.Stderr); stderr != "" {
			return fmt.Errorf("%s exited with status %d: %s", toolName, exitErr.ExitCode, stderr)
		}
		return fmt.Errorf("%s exited with status %d", toolName, exitErr.ExitCode)
	}
	return err
}

// finishMergeTool decides what becomes of the resolution the tool left in the
// merged snapshot file, on the UI thread. An unchanged file was never merged
// in the tool and nothing happens. A changed resolution is kept first, before
// anything tries to write it: the write is asynchronous and its completion is
// dropped entirely when the repository generation moved on, so keeping only
// after the fact would lose the result to a refused write or a repository
// switch. A direct write goes ahead when the session is still on the same
// repository, at the same generation the tool opened in — switching away and
// back reopens the same repository at a new generation — and no write is in
// progress; it consumes the result only when it succeeds. Anything else stays
// kept whole for the row's Save Merge Result, and the user is told once.
func (a *app) finishMergeTool(result mergeToolResult, toolName, original string) {
	if result.content == original {
		a.toast = fmt.Sprintf("%s was not changed in %s; the conflict is still unresolved.", result.path, toolName)
		return
	}
	mergeToolUI(a).results[mergeToolKey(result.repoKey, result.path)] = result
	// The direct write needs the session still open on the repository the
	// tool merged for — the active key of no repository never equals a real
	// one — at the generation the tool opened in, and no write in progress
	// to queue behind.
	direct := result.repoKey == mergeToolRepoKey(a) &&
		result.repoGen == a.session.Generation() && !a.session.Busy()
	if direct && a.writeMergeToolResult(result) {
		// The write is in flight: its success deletes the kept result, its
		// refusal shows as the session's action error with the result kept.
		return
	}
	a.toast = fmt.Sprintf("The merge result of %s was kept; save it from the conflicted file's context menu.", result.path)
}

// writeMergeToolResult writes a merge resolution back through the safe
// conflict write, staged as the resolution. The write itself re-reads the
// repository in the worker and refuses the content when the conflict changed
// since the inspection, when the file is no longer unmerged, or when conflict
// markers remain; the refusal is the session's action error, displayed in the
// page exactly as the conflict editor's own save errors are. It reports
// whether the write started: a false answer — the session busy or without a
// repository — leaves the result to the caller for keeping. The completion
// runs only while the session is still at the write's generation; when a
// repository switch drops it, the kept result — stored before the write
// started — survives untouched, and only a successful completion may consume
// it.
func (a *app) writeMergeToolResult(result mergeToolResult) bool {
	path := result.path
	repoGen := result.repoGen
	content := result.content
	identity := result.identity
	repoKey := result.repoKey
	return a.session.doThen("Resolve "+path, func(repo *git.Repo) error {
		return repo.SafeWriteResolvedConflict(path, content, identity)
	}, func(err error) {
		if err != nil {
			// A refused write keeps the result: it was stored before the
			// write started, and the refusal alone never consumes it.
			return
		}
		// A successful write consumes the result, wherever it came from: the
		// conflict is resolved with exactly this content.
		delete(mergeToolUI(a).results, mergeToolKey(repoKey, path))
		if st := conflictEditor(a); st.repoGen == repoGen && st.path == path {
			a.resetConflictEditor()
		}
	})
}

// saveMergeToolResult writes back the merge resolution the row's menu kept:
// the same safe write the direct completion uses, against the identity the
// inspection captured when the tool opened. A conflict that changed since
// refuses with its ordinary error, and the result stays kept for a fresh
// decision — discarding it is the menu's other entry.
func (a *app) saveMergeToolResult(key string) {
	res, ok := mergeToolUI(a).results[key]
	if !ok {
		return
	}
	a.writeMergeToolResult(res)
}

// discardMergeToolResult drops the kept resolution of the file untouched: the
// conflict stays exactly as it is, and the row offers Open in Merge Tool
// again. Nothing reaches the repository.
func (a *app) discardMergeToolResult(key string) {
	delete(mergeToolUI(a).results, key)
}
