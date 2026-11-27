package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// interactiveRebaseOptionsUIState is the per-app state of the interactive
// rebase form's options section: the captured branch and HEAD the backup ref
// preview is drawn from, the two checkbox drafts, the asynchronous probe of
// the executor's capabilities and the dependent branches, and the repository
// generation they belong to. It lives outside the shared app struct, keyed
// by the app instance, exactly as the form's own plan state does, so the two
// states never share a field and never lock in each other's order.
type interactiveRebaseOptionsUIState struct {
	repoGen uint64

	onto         string
	branch       string
	expectedHead string
	backup       bool
	updateRefs   bool

	inspecting bool
	probed     bool
	probedOK   bool
	caps       git.GitCapabilities
	capsErr    error
	dependents []string
}

var interactiveRebaseOptionsUIStates = struct {
	sync.Mutex
	m map[*app]*interactiveRebaseOptionsUIState
}{m: map[*app]*interactiveRebaseOptionsUIState{}}

// interactiveRebaseOptionsUI returns the app's interactive rebase options
// state, creating it on first use and discarding every captured or probed
// value when the repository generation moved past it: a switch or close makes
// the captured base, HEAD and branch meaningless, so nothing of the previous
// repository carries over. The probe of the closing form is canceled with it,
// so its late result can never land in the next repository's form.
func interactiveRebaseOptionsUI(a *app) *interactiveRebaseOptionsUIState {
	interactiveRebaseOptionsUIStates.Lock()
	defer interactiveRebaseOptionsUIStates.Unlock()
	st := interactiveRebaseOptionsUIStates.m[a]
	if st == nil {
		st = &interactiveRebaseOptionsUIState{}
		interactiveRebaseOptionsUIStates.m[a] = st
	}
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		if st.inspecting {
			a.session.cancelRead("ir-options")
		}
		st.clearInteractiveRebaseOptionsDraft()
	}
	return st
}

// clearInteractiveRebaseOptionsDraft drops every value captured or probed for
// the form, so a reopened form starts from the repository as it is then.
func (st *interactiveRebaseOptionsUIState) clearInteractiveRebaseOptionsDraft() {
	st.onto = ""
	st.branch = ""
	st.expectedHead = ""
	st.backup = false
	st.updateRefs = false
	st.inspecting = false
	st.probed = false
	st.probedOK = false
	st.caps = git.GitCapabilities{}
	st.capsErr = nil
	st.dependents = nil
}

// clearInteractiveRebaseOptions cancels the probe of the closing form and
// clears the options draft. Canceling, dismissing, finishing a rebase and
// switching the repository all end here so no captured base or probe survives
// the form.
func (a *app) clearInteractiveRebaseOptions() {
	st := interactiveRebaseOptionsUI(a)
	if st.inspecting {
		a.session.cancelRead("ir-options")
	}
	st.clearInteractiveRebaseOptionsDraft()
}

// startInteractiveRebaseOptionsRead arms the asynchronous probe of the open
// form's options. The probe itself is not spawned here: it starts from the
// plan read's completion callback once the plan read settles. Running both
// reads concurrently would spawn a second background worker at open time and
// race on shared test executors, while triggering the probe directly from plan
// completion runs it strictly afterwards with no busy-reposting on the UI
// queue. The base, HEAD and branch are captured now so the preview works
// immediately and the probe never reads mutable form state.
func (a *app) startInteractiveRebaseOptionsRead(onto, branch, expectedHead string) {
	st := interactiveRebaseOptionsUI(a)
	if st.inspecting {
		a.session.cancelRead("ir-options")
	}
	st.clearInteractiveRebaseOptionsDraft()
	st.onto = onto
	st.branch = branch
	st.expectedHead = expectedHead
}

// kickInteractiveRebaseOptionsRead starts the probe once the plan read of the
// form it belongs to has settled. A kick that finds the form closed, switched,
// superseded or still loading the plan dies silently; after a successful probe
// it does not re-run on Retry, while a previous probe failure retries with the
// plan.
func (a *app) kickInteractiveRebaseOptionsRead(onto string) {
	st := interactiveRebaseOptionsUI(a)
	if st.onto != onto || st.inspecting {
		return
	}
	if st.probed && st.probedOK {
		return
	}
	if interactiveRebaseUI(a).loading {
		return
	}
	a.probeInteractiveRebaseOptions(st, onto)
}

// interactiveRebaseOptionsProbe is the pre-flight result of the form's
// options section: the capabilities of the Git that would run the rebase and
// the local branches a rebase with update-ref lines would move. Each piece is
// independent: a failure of one never discards the others.
type interactiveRebaseOptionsProbe struct {
	caps       git.GitCapabilities
	capsErr    error
	dependents []string
	depsErr    error
}

// probeInteractiveRebaseOptions reads the two option answers of the open form
// in one background read: the capabilities of the executor's own Git — never
// the local Git standing in for a server's — and the dependent branches of
// the base. Neither failure is decisive for the other, so each is recorded on
// its own: a failed capability probe keeps Update dependent branches disabled,
// while failed dependent branches simply hide the choice. The result lands
// only when the form still waits for the probe of exactly that base.
func (a *app) probeInteractiveRebaseOptions(st *interactiveRebaseOptionsUIState, onto string) {
	st.inspecting = true
	ok := sessionRead(a.session, "ir-options", func(repo *git.Repo) (interactiveRebaseOptionsProbe, error) {
		res := interactiveRebaseOptionsProbe{}
		caps, err := repo.Capabilities()
		res.caps = caps
		res.capsErr = err
		names, derr := repo.DependentBranches(onto)
		res.depsErr = derr
		if derr == nil {
			res.dependents = names
		}
		return res, nil
	}, func(res interactiveRebaseOptionsProbe, err error) {
		if st.onto != onto {
			return
		}
		st.inspecting = false
		st.probed = true
		st.probedOK = res.capsErr == nil && res.depsErr == nil
		st.caps = res.caps
		st.capsErr = res.capsErr
		st.dependents = res.dependents
	})
	if !ok {
		st.inspecting = false
	}
}

// irBackupRefPreview names the backup ref the rebase would write, exactly as
// the backend composes it: refs/backup/<branch>-<short head> with the slashes
// of the branch name turned to dashes, so the preview the user confirms is
// the ref they can look up afterwards.
func irBackupRefPreview(branch, head string) string {
	name := strings.TrimSpace(branch)
	name = strings.ReplaceAll(name, "/", "-")
	if name == "" {
		name = "HEAD"
	}
	return "refs/backup/" + name + "-" + irShort(head)
}

// interactiveRebaseOptions captures the checkbox drafts of the form by value,
// on the UI thread, for the guarded write. Update dependent branches counts
// only when dependent branches exist: the choice is hidden without them, so
// a stale checked draft can never smuggle the flag into a rebase the user
// was never shown.
func (a *app) interactiveRebaseOptions() git.RebaseOptions {
	st := interactiveRebaseOptionsUI(a)
	canUpdateRefs := !st.inspecting && st.capsErr == nil && st.caps.RebaseUpdateRefs
	return git.RebaseOptions{
		Backup:     st.backup,
		UpdateRefs: st.updateRefs && canUpdateRefs && len(st.dependents) > 0,
	}
}

// interactiveRebaseOptionsSection renders the options of the Interactive
// Rebase form above its buttons. Create backup branch previews the exact ref
// the rebase would write; Update dependent branches only appears when the
// range actually carries dependent branches, and stays disabled until the
// executor's Git is verified to know update-ref lines: a version that cannot
// be verified is never treated as a new one.
func (a *app) interactiveRebaseOptionsSection(c *ui.Context, p palette) {
	st := interactiveRebaseOptionsUI(a)
	canUpdateRefs := !st.inspecting && st.capsErr == nil && st.caps.RebaseUpdateRefs
	if !canUpdateRefs {
		st.updateRefs = false
	}

	ui.Checkbox(c.Key("ir-opt-backup"), &st.backup, "Create backup branch").Label("Create backup branch")
	if st.backup {
		ui.Text(c, irBackupRefPreview(st.branch, st.expectedHead)).Font("monospace").FontSize(11.5).TextColor(p.muted).MaxWidth(604)
	}
	if len(st.dependents) > 0 {
		ui.Checkbox(c.Key("ir-opt-update-refs"), &st.updateRefs, "Update dependent branches").Label("Update dependent branches").Disabled(!canUpdateRefs)
		if !canUpdateRefs && !st.inspecting {
			reason := "Git version could not be verified; requires Git 2.38 or newer."
			if st.capsErr == nil && st.caps.RawVersion != "" {
				reason = "Requires Git 2.38 or newer (found " + st.caps.RawVersion + ")."
			}
			ui.Text(c, reason).FontSize(12).TextColor(p.muted).MaxWidth(604)
		}
		if st.updateRefs {
			ui.Text(c, "These branches move with the rebase:").FontSize(11).TextColor(p.muted)
			for _, name := range st.dependents {
				ui.Text(c.Key("ir-opt-dep-"+name), name).Font("monospace").FontSize(12).TextColor(p.text)
			}
		}
	}
}

// interactiveRebaseWithOptionsGuard re-validates the repository against what
// the Interactive Rebase form captured before it rewrites the branch: the
// status must still be readable, no merge, rebase, cherry-pick or revert may
// be in progress, HEAD and the branch must still be exactly where the form
// saw them, the index and working tree must carry no uncommitted tracked
// changes — the backend always autostashes, but this form offers no such
// choice, so local work is the user's to commit or stash first — a freshly
// read plan must contain exactly the commits the form captured, and an
// update-refs write asks the executor's Git itself, live, whether it knows
// the capability. Only then does the rebase run. Any check failing returns
// the error without touching the working tree, the index, HEAD or any branch:
// the form's snapshot of the repository can be stale by the time the write
// starts, and replaying a plan the user never saw would rewrite commits they
// never confirmed against. The backup ref is part of that invariant: it is
// written by the rebase itself, so a refused write creates no ref, moves no
// branch and leaves no trace — not even the backup the user asked for,
// because a backup of a rebase that never happened is a lie about what
// happened.
func interactiveRebaseWithOptionsGuard(repo *git.Repo, expectedHead, expectedBranch, onto string, plan []git.RebaseTodo, opts git.RebaseOptions) error {
	if onto == "" || strings.HasPrefix(onto, "-") || strings.ContainsAny(onto, "\x00\r\n") {
		return fmt.Errorf("cannot rebase: invalid target revision")
	}
	if expectedHead == "" || expectedBranch == "" {
		return fmt.Errorf("cannot rebase: the repository is not on a branch")
	}
	st, err := repo.Status()
	if err != nil {
		return fmt.Errorf("cannot rebase: reading the repository state failed: %w", err)
	}
	if st.State != "" {
		return fmt.Errorf("cannot rebase: a %s is in progress; finish or abort it first", st.State)
	}
	if st.Head != expectedHead || st.Branch != expectedBranch {
		return fmt.Errorf("cannot rebase: HEAD or the current branch changed after the dialog opened; review the repository and try again")
	}
	if rebaseHasTrackedLocalChanges(st) {
		return fmt.Errorf("cannot rebase with uncommitted local changes; commit or stash them first")
	}
	live, err := repo.RebaseRangePlan(onto)
	if err != nil {
		return fmt.Errorf("cannot rebase: the plan can no longer be read: %w", err)
	}
	if !sameRebaseCommitSet(plan, live) {
		return fmt.Errorf("cannot rebase: the commits in the rebase range changed after the dialog opened; review the repository and try again")
	}
	if opts.UpdateRefs {
		caps, err := repo.Capabilities()
		if err != nil {
			return fmt.Errorf("cannot rebase with --update-refs: the Git version could not be verified: %w", err)
		}
		if !caps.RebaseUpdateRefs {
			return fmt.Errorf("cannot rebase with --update-refs: it requires Git 2.38 or newer")
		}
	}
	return repo.RebaseWithOptions(onto, plan, opts)
}
