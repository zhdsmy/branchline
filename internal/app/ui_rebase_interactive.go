package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// irShort abbreviates a commit hash the way the commit list does, for the
// accessibility labels of the plan rows.
func irShort(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// irActionNames are the actions one row of the interactive rebase plan can
// take, in the order the choice list offers them.
var irActionNames = []string{"Pick", "Reword", "Edit", "Squash", "Fixup", "Drop"}

// irActionDisplay capitalizes the backend's lowercase action for the choice
// list, or returns it unchanged when it already is one of them.
func irActionDisplay(action string) string {
	for _, name := range irActionNames {
		if strings.EqualFold(name, action) {
			return name
		}
	}
	return action
}

// irActionBackend converts the displayed action back to the lowercase verb
// the rebase todo language uses.
func irActionBackend(action string) string {
	return strings.ToLower(action)
}

// interactiveRebaseRow is one commit of the plan as the form edits it. It is
// a heap object the choice control binds to directly, so changing an action
// or moving rows around can never write the plan of another commit: the row
// travels with its hash wherever the order puts it, and every mutation —
// action, move up, move down — resolves its target by the full hash in the
// current order, never by a slice index or a captured position.
type interactiveRebaseRow struct {
	hash    string
	subject string
	author  string
	action  string
}

// interactiveRebaseUIState is the per-app state of the interactive rebase:
// the form draft with the HEAD, branch and base it was opened against, the
// plan rows as the user edits them, the asynchronous plan read behind the
// form, and the repository generation they belong to.
type interactiveRebaseUIState struct {
	repoGen uint64

	open          bool
	branch        string
	onto          string
	ontoLabel     string
	expectedHead  string
	loading       bool
	readErr       error
	rows          []*interactiveRebaseRow
	submitClicked bool
}

// interactiveRebaseUI returns the app's interactive rebase state, creating
// it on first use and discarding every open form draft when the repository
// generation moved past it: a switch or close makes the captured base, HEAD,
// branch and plan meaningless, so nothing of the previous repository carries
// over. The plan read of the closing form is canceled with it, so its late
// result can never land in the next repository's form.
func interactiveRebaseUI(a *app) *interactiveRebaseUIState {
	st := featureState(a, func() *interactiveRebaseUIState { return &interactiveRebaseUIState{} })
	if gen := a.session.Generation(); st.repoGen != gen {
		st.repoGen = gen
		if st.open || st.loading {
			a.session.cancelRead("ir-plan")
		}
		st.clearInteractiveRebaseDraft()
		a.clearInteractiveRebaseOptions()
		a.clearInteractiveRebaseMessages()
	}
	return st
}

// clearInteractiveRebaseDraft closes the form and drops every value captured
// for it, so a reopened form starts from the repository as it is then.
func (st *interactiveRebaseUIState) clearInteractiveRebaseDraft() {
	st.open = false
	st.branch = ""
	st.onto = ""
	st.ontoLabel = ""
	st.expectedHead = ""
	st.loading = false
	st.readErr = nil
	st.rows = nil
	st.submitClicked = false
}

// commitInteractiveRebaseMenuItems adds the interactive rebase item to the
// context menu of one commit of the History list: rewriting the commits of
// the current branch that come after that commit. Right-clicking HEAD itself
// would leave an empty plan, so the item disables on the HEAD commit row
// besides the shared operation and repository conditions; a detached or
// unborn HEAD has no branch for the rebase to move.
func (a *app) commitInteractiveRebaseMenuItems(m *ui.Menu, cm git.Commit) {
	snap := a.session.Snapshot()
	label := "Interactively Rebase HEAD to Here…"
	if snap.Status.Branch != "" {
		label = "Interactively Rebase '" + snap.Status.Branch + "' to Here…"
	}
	disabled := a.session.Busy() || snap.Status.State != "" || snap.Status.Head == "" || snap.Status.Branch == "" || cm.Hash == snap.Status.Head
	if m.Item(label).Disabled(disabled).Chosen() {
		a.openInteractiveRebaseModal(cm)
	}
}

// openInteractiveRebaseModal opens the Interactive Rebase form for the
// commits after cm. Everything the form or its worker needs later is
// captured now, on the UI thread: the full hash of the base commit — onto is
// never a branch or reflog expression, so what the plan rewrites cannot move
// underneath the form without the guard noticing — and the HEAD hash and
// branch the rebase must still find when it runs. The plan read starts here
// so the rows are on their way before the form first renders.
func (a *app) openInteractiveRebaseModal(cm git.Commit) {
	st := interactiveRebaseUI(a)
	snap := a.session.Snapshot()
	st.open = true
	st.branch = snap.Status.Branch
	st.onto = cm.Hash
	st.ontoLabel = irShort(cm.Hash) + " " + cm.Subject
	st.expectedHead = snap.Status.Head
	st.readErr = nil
	st.rows = nil
	st.submitClicked = false
	a.clearInteractiveRebaseMessages()
	a.startInteractiveRebasePlanRead(st)
	a.startInteractiveRebaseOptionsRead(st.onto, st.branch, st.expectedHead)
}

// startInteractiveRebasePlanRead reads the plan of the open form in one
// background read. The base is captured before the read starts and the
// result lands only when the form still waits for the plan of exactly that
// base: a retried read, a reopened form or a switched repository invalidates
// the sequence in between, and the onto comparison rejects a stale callback
// of the same generation outright.
func (a *app) startInteractiveRebasePlanRead(st *interactiveRebaseUIState) {
	onto := st.onto
	st.loading = true
	st.readErr = nil
	sessionRead(a.session, "ir-plan", func(repo *git.Repo) ([]git.RebaseTodo, error) {
		return repo.RebaseRangePlan(onto)
	}, func(plan []git.RebaseTodo, err error) {
		if !st.open || st.onto != onto {
			return
		}
		st.loading = false
		defer a.kickInteractiveRebaseOptionsRead(onto)
		if err != nil {
			st.readErr = err
			return
		}
		st.readErr = nil
		rows := make([]*interactiveRebaseRow, len(plan))
		for i, step := range plan {
			rows[i] = &interactiveRebaseRow{
				hash:    step.Hash,
				subject: step.Subject,
				author:  step.Author,
				action:  irActionDisplay(step.Action),
			}
		}
		st.rows = rows
	})
}

// moveInteractiveRebaseRow moves the row of hash one step in direction dir —
// +1 toward the top of the form, which is later in the execution order, -1
// toward the bottom, which is earlier. The row is found by its full hash in
// the current order: a move and a second move of the same frame both act on
// where the commit actually is, never on a captured position.
func moveInteractiveRebaseRow(rows []*interactiveRebaseRow, hash string, dir int) {
	idx := -1
	for i, row := range rows {
		if row.hash == hash {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	to := idx + dir
	if to < 0 || to >= len(rows) {
		return
	}
	rows[idx], rows[to] = rows[to], rows[idx]
}

// interactiveRebasePlanProblem reports the one plan defect the form refuses
// to start with: a squash or fixup on the oldest kept commit, which has no
// previous commit to fold into. The backend refuses the same plan, but the
// form catches it before any write starts so the user fixes the draft
// instead of reading a backend error; the fold choice is a property of the
// whole plan — reordering moves the boundary — so it is a validation error
// with the start button disabled, not a disabled option.
func interactiveRebasePlanProblem(rows []*interactiveRebaseRow) string {
	kept := false
	for _, row := range rows {
		switch row.action {
		case "Squash", "Fixup":
			if !kept {
				return "The oldest kept commit cannot be " + irActionBackend(row.action) + ": it has no previous commit to fold into."
			}
		}
		if row.action != "Drop" {
			kept = true
		}
	}
	return ""
}

// interactiveRebaseModal is the Interactive Rebase form: it lists the
// commits the rebase will replay, newest on top as the History list shows
// them, with the action and the position of each commit the user controls.
// The submit button stays disabled until a plan is read, while a read is in
// flight, while it failed, while the draft folds into nothing, and while a
// write is running — none of those states can produce a rebase the user
// confirmed.
func (a *app) interactiveRebaseModal(c *ui.Context) {
	p := a.pal
	st := interactiveRebaseUI(a)
	if !st.open {
		return
	}

	ensureInteractiveRebaseMessages(a, st)
	msgLoading, msgFailed, msgValidation := interactiveRebaseMessagesBlocker(st.rows, interactiveRebaseMessages(a))

	problem := interactiveRebasePlanProblem(st.rows)
	drops := 0
	for _, row := range st.rows {
		if row.action == "Drop" {
			drops++
		}
	}
	rebaseDisabled := a.session.Busy() || st.loading || st.readErr != nil || len(st.rows) == 0 || problem != "" || msgLoading || msgFailed || msgValidation != ""

	ui.Modal(c.Key("ir-modal"), &st.open, func() {
		ui.Column(c.Key("ir-modal-body")).Width(640).Padding(18, 18, 14, 18).Gap(10).Children(func() {
			ui.Text(c, "Interactive Rebase").FontSize(17).FontWeight(650).TextColor(p.text)
			ui.Text(c, "Choose the action and position of each commit; the rebase applies them from bottom to top.").FontSize(12).TextColor(p.muted).MaxWidth(604)
			ui.Field(c, "Rebase:", func() {
				ui.Text(c, st.branch).Font("monospace").FontSize(12).TextColor(p.text)
			})
			ui.Field(c, "Onto:", func() {
				ui.Text(c, st.ontoLabel).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
			})
			switch {
			case st.loading:
				ui.Text(c, "Loading plan…").FontSize(12.5).TextColor(p.muted)
			case st.readErr != nil:
				ui.Text(c, st.readErr.Error()).FontSize(12).TextColor(p.danger).MaxWidth(604)
				if ui.Button(c.Key("ir-retry"), "Retry").Label("Retry").Clicked() {
					a.startInteractiveRebasePlanRead(st)
				}
			default:
				// The rows drawn this frame are a snapshot of the order as the
				// frame started: a move button's swap lands while its own row is
				// still building, and the rows the loop has not reached yet must
				// not shift underneath it — otherwise one commit renders twice in
				// one pass. The row objects are shared, so the action choice of
				// each still writes the commit it names.
				display := append([]*interactiveRebaseRow(nil), st.rows...)
				ui.Scroll(c.Key("ir-rows-scroll")).MaxHeight(320).Gap(2).FillWidth().Children(func() {
					for pos := len(display) - 1; pos >= 0; pos-- {
						a.interactiveRebaseRowView(c, p, st, display, pos)
					}
				})
			}
			a.interactiveRebaseMessagePanel(c, p, st)
			if drops > 0 {
				ui.Text(c, fmt.Sprintf("%d commit(s) will be dropped.", drops)).FontSize(12).TextColor(p.danger)
			}
			if problem != "" {
				ui.Text(c, problem).FontSize(12).TextColor(p.danger).MaxWidth(604)
			}
			if err := a.session.ActionErr(); st.submitClicked && err != nil {
				ui.Text(c, err.Error()).FontSize(12).TextColor(p.danger).MaxWidth(604)
			}
			a.interactiveRebaseOptionsSection(c, p)
			ui.Row(c.Key("ir-modal-actions")).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("ir-cancel"), "Cancel").Label("Cancel").Clicked() {
					a.closeInteractiveRebaseModal()
				}
				if ui.PrimaryButton(c.Key("ir-start"), "Rebase").Label("Rebase").Disabled(rebaseDisabled).Clicked() {
					a.submitInteractiveRebase()
				}
			})
		})
	})
	if !st.open {
		a.closeInteractiveRebaseModal()
	}
}

// interactiveRebaseRowView renders one plan row of the form: the short hash,
// subject and author of the commit, its move controls and its action choice.
// display is the frame's frozen projection of the execution order and pos
// counts backward through it — the form shows the newest commit on top — so
// the top row is already as late as it can go and disables moving up, the
// bottom row disables moving down.
func (a *app) interactiveRebaseRowView(c *ui.Context, p palette, st *interactiveRebaseUIState, display []*interactiveRebaseRow, pos int) {
	row := display[pos]
	short := irShort(row.hash)
	ui.Row(c.Key("ir-row-" + row.hash)).Height(30).FillWidth().Gap(8).AlignItems(ui.Center).PaddingX(4).Children(func() {
		ui.Text(c, short).Font("monospace").FontSize(11).TextColor(p.muted).Width(56).Shrink(0).SingleLine()
		ui.Text(c, row.subject).FontSize(12.5).TextColor(p.text).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
		ui.Text(c, row.author).FontSize(11.5).TextColor(p.muted).SingleLine().Ellipsis("…").Width(96).Shrink(0)

		a.interactiveRebaseRowMessageButton(c, p, st, display, pos)

		upDisabled := pos == len(display)-1
		a.interactiveRebaseMoveButton(c, p, st, row, "up", upDisabled)
		downDisabled := pos == 0
		a.interactiveRebaseMoveButton(c, p, st, row, "down", downDisabled)

		action := ui.Select(c.Key("ir-action-"+row.hash), &row.action, irActionNames).Label("Action " + short).Width(104).Shrink(0)
		if action.Changed() {
			if row.action == "Reword" {
				// Choosing Reword is the user's request to edit the message:
				// the editor opens on the row at once, and the read of the
				// original messages starts in the same frame instead of one
				// later.
				a.openInteractiveRebaseMessageEditor(st, row.hash)
			}
			ensureInteractiveRebaseMessages(a, st)
		}
	})
}

// interactiveRebaseMoveButton is one move control of a plan row. Screen up
// is later in the execution order (+1), screen down is earlier (-1): the
// list is the reverse projection of the order the rebase applies.
func (a *app) interactiveRebaseMoveButton(c *ui.Context, p palette, st *interactiveRebaseUIState, row *interactiveRebaseRow, direction string, disabled bool) {
	sf := "chevron.up"
	fallback := fallbackUpArr
	dir := 1
	if direction == "down" {
		sf = "chevron.down"
		fallback = fallbackDownArr
		dir = -1
	}
	label := "Move Up " + irShort(row.hash)
	if direction == "down" {
		label = "Move Down " + irShort(row.hash)
	}
	color := p.muted
	if disabled {
		color = p.faint
	}
	b := ui.ButtonBase(c.Key("ir-move-"+direction+"-"+row.hash)).
		Size(22, 22).
		Radius(5).
		Center().
		Shrink(0).
		Label(label).
		Disabled(disabled).
		FocusRing(false)
	if b.Hovered() && !disabled {
		b.Background(p.hover)
	}
	b.Children(func() {
		symbolIcon(c, sf, fallback, 10, color)
	})
	if b.Clicked() {
		moveInteractiveRebaseRow(st.rows, row.hash, dir)
	}
}

// closeInteractiveRebaseModal closes the form, cancels its plan read and
// clears the draft. Canceling, dismissing and finishing a rebase all end
// here so no captured base or plan survives the form.
func (a *app) closeInteractiveRebaseModal() {
	st := interactiveRebaseUI(a)
	if st.open || st.loading {
		a.session.cancelRead("ir-plan")
	}
	st.clearInteractiveRebaseDraft()
	a.clearInteractiveRebaseOptions()
	a.clearInteractiveRebaseMessages()
}

// submitInteractiveRebase starts the rebase the form describes through the
// session's write path. The form stays open with its draft: the background
// worker re-validates everything it assumed, and a refusal is the user's to
// fix, so only a started-and-finished rebase closes the form. The plan, the
// base, HEAD and the branch are captured here by value — the rows are copied
// outright, so the worker never reads mutable form state.
func (a *app) submitInteractiveRebase() {
	st := interactiveRebaseUI(a)
	if a.session.Busy() {
		return
	}
	if st.loading || st.readErr != nil || len(st.rows) == 0 {
		return
	}
	if interactiveRebasePlanProblem(st.rows) != "" {
		return
	}
	plan, err := a.buildInteractiveRebasePlan(st)
	if err != nil {
		return
	}
	st.submitClicked = true
	onto := st.onto
	expectedHead := st.expectedHead
	expectedBranch := st.branch
	opts := a.interactiveRebaseOptions()
	a.session.doThen("Rebasing", func(repo *git.Repo) error {
		return interactiveRebaseWithOptionsGuard(repo, expectedHead, expectedBranch, onto, plan, opts)
	}, func(err error) {
		if err == nil {
			a.closeInteractiveRebaseModal()
		}
	})
}

// interactiveRebaseWithGuard re-validates the repository against what the
// Interactive Rebase form captured before it rewrites the branch, keeping
// the signature the interactive rebase tests call directly. The guarded
// write itself lives in interactiveRebaseWithOptionsGuard; this is it with
// no options set.
func interactiveRebaseWithGuard(repo *git.Repo, expectedHead, expectedBranch, onto string, plan []git.RebaseTodo) error {
	return interactiveRebaseWithOptionsGuard(repo, expectedHead, expectedBranch, onto, plan, git.RebaseOptions{})
}

// sameRebaseCommitSet reports whether the captured plan and the freshly read
// one name exactly the same commits. With onto and HEAD verified unmoved the
// range can only change when the commits inside it were rewritten, so the
// comparison is a set, not an order: the captured order is the user's own
// editing and never a reason to refuse.
func sameRebaseCommitSet(plan []git.RebaseTodo, live []git.RebaseTodo) bool {
	if len(plan) != len(live) {
		return false
	}
	seen := make(map[string]int, len(plan))
	for _, step := range plan {
		seen[step.Hash]++
	}
	for _, step := range live {
		seen[step.Hash]--
		if seen[step.Hash] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
