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

	// list is the place of the plan rows list, kept by item key across the
	// moves of the rows, and moves is the queue of row moves clicked while
	// the list built: a move applies only once every row of the pass is
	// built, so one pass never draws a commit twice. moves is drained within
	// the frame it was recorded in and never survives it.
	list  ui.ListState
	moves []interactiveRebaseMove

	// planVersion counts the in-place mutations of the plan rows — a chosen
	// action and every move that really swaps — so a mutation the backing
	// slice cannot show still invalidates the derived cache. A wholesale
	// replacement of rows needs no count: the cache key compares the backing
	// array itself.
	planVersion uint64

	// derived is the cache of everything the form computes from the plan and
	// the message state: fold chains, message targets, the validation, the
	// drop count, the blocker results and the messages still to read. An
	// unchanged plan and message state hit it, so a frame scans nothing.
	derived irDerivedCache
}

// interactiveRebaseMove is one move a move button recorded: the commit it
// moves, and the direction in the execution order.
type interactiveRebaseMove struct {
	hash string
	dir  int
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
	st.list = ui.ListState{}
	st.moves = nil
	st.derived = irDerivedCache{}
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
	st.list = ui.ListState{}
	st.moves = nil
	st.derived = irDerivedCache{}
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
	st.list = ui.ListState{}
	st.moves = nil
	st.derived = irDerivedCache{}
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
		st.list = ui.ListState{}
		st.moves = nil
		st.derived = irDerivedCache{}
	})
}

// moveInteractiveRebaseRow moves the row of hash one step in direction dir —
// +1 toward the top of the form, which is later in the execution order, -1
// toward the bottom, which is earlier — and reports whether rows really
// changed places: a move off either end of the plan swaps nothing, so it
// invalidates nothing either. The row is found by its full hash in the
// current order: a move and a second move of the same frame both act on where
// the commit actually is, never on a captured position.
func moveInteractiveRebaseRow(rows []*interactiveRebaseRow, hash string, dir int) bool {
	idx := -1
	for i, row := range rows {
		if row.hash == hash {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	to := idx + dir
	if to < 0 || to >= len(rows) {
		return false
	}
	rows[idx], rows[to] = rows[to], rows[idx]
	return true
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
// confirmed. A plan dropping commits makes the dialog destructive: dropping
// a commit is the user's own choice in the row's action list, but the Enter
// key must never finish it, because a user choosing actions row by row
// presses Enter there all the time.
func (a *app) interactiveRebaseModal(c *ui.Context) {
	p := a.pal
	st := interactiveRebaseUI(a)
	if !st.open {
		return
	}

	// Drain any deferred move queued by a late-built row during the previous
	// layout pass before deriving plan state for this frame.
	if !st.loading && st.readErr == nil && len(st.moves) > 0 {
		for _, m := range st.moves {
			if moveInteractiveRebaseRow(st.rows, m.hash, m.dir) {
				st.planVersion++
			}
		}
		st.moves = st.moves[:0]
	}

	ensureInteractiveRebaseMessages(a, st)
	// Everything the form derives from the plan and the message state comes
	// out of the one cache: an unchanged plan scans nothing frame after
	// frame, and a plan or message mutation recomputes the whole set once.
	derived := st.irDerived(interactiveRebaseMessages(a))
	msgLoading, msgFailed, msgValidation := derived.msgLoading, derived.msgFailed, derived.msgValidation
	problem := derived.problem
	drops := derived.drops
	rebaseDisabled := a.session.Busy() || st.loading || st.readErr != nil || len(st.rows) == 0 || problem != "" || msgLoading || msgFailed || msgValidation != ""

	// A plan that drops commits discards them outright: the confirmation is
	// the destructive one — red, deaf to Enter, its autofocus ignored — while
	// a plan keeping every commit only reshapes the branch the rebase replays
	// anyway, so its Rebase confirms on Enter like every non-destructive
	// dialog.
	primary := dialogAction{key: "ir-start", text: "Rebase", label: "Rebase", disabled: rebaseDisabled, autoFocus: true}
	if drops > 0 {
		primary = dialogAction{key: "ir-start", text: "Rebase", label: "Rebase", disabled: rebaseDisabled, destructive: true}
	}

	_, cancelled, confirmed := dialogFrame(c, p, "ir", &st.open, "Interactive Rebase", 640, func() {
		ui.Text(c, "Choose the action and position of each commit; the rebase applies them from bottom to top.").FontSize(12).TextColor(p.muted).MaxWidth(604)
		ui.Field(c, "Rebase:", func() {
			ui.Text(c, st.branch).Font("monospace").FontSize(12).TextColor(p.text)
		})
		ui.Field(c, "Onto:", func() {
			ui.Text(c, st.ontoLabel).FontSize(12).TextColor(p.text).SingleLine().Ellipsis("…")
		})
		switch {
		case st.loading:
			// A new plan read starts the rows list from the top again:
			// the place of the rows it replaces means nothing to it, as
			// the scroll container's reset offset did before.
			st.list = ui.ListState{}
			st.moves = nil
			ui.Text(c, "Loading plan…").FontSize(12.5).TextColor(p.muted)
		case st.readErr != nil:
			st.moves = nil
			ui.Text(c, st.readErr.Error()).FontSize(12).TextColor(p.danger).MaxWidth(604)
			if ui.Button(c.Key("ir-retry"), "Retry").Label("Retry").Clicked() {
				a.startInteractiveRebasePlanRead(st)
			}
		default:
			// The list draws only the rows in view; its order is the reverse
			// of the execution order, the newest commit on top. A move
			// button's click cannot swap rows while the rows build — the
			// rows not built yet would shift underneath the pass, drawing
			// one commit twice — so the button records the move and the
			// swaps apply once the whole list is built. The pass rebuilds
			// for the click it consumed, so the frame shows the outcome the
			// same as an immediate swap would. With fewer rows than the
			// list's height the list is exactly as tall as its content;
			// past MaxHeight it scrolls.
			st.list.KeepOffset = true
			st.list.Key = func(row int) any { return st.rows[len(st.rows)-1-row].hash }
			ui.List(c.Key("ir-rows-scroll"), &st.list, len(st.rows)).
				MaxHeight(320).Gap(2).FillWidth().
				Rows(func(r ui.ListRow) {
					a.interactiveRebaseRowView(r.Context, p, st, st.rows, len(st.rows)-1-r.Index)
				})
			if len(st.moves) > 0 {
				for _, m := range st.moves {
					// A real swap mutates the plan in place: the backing
					// array stays, so only the plan version can tell the
					// derived cache the rows changed.
					if moveInteractiveRebaseRow(st.rows, m.hash, m.dir) {
						st.planVersion++
					}
				}
				st.moves = st.moves[:0]
			}
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
	},
		dialogAction{key: "ir-cancel", text: "Cancel", label: "Cancel"},
		primary)
	if confirmed {
		a.submitInteractiveRebase()
	}
	// Canceling, dismissing and a rebase finishing behind the form all end in
	// the one close, once: the submission keeps the form open over its write,
	// so no two of these can need the close in the same pass.
	if cancelled || !st.open {
		a.closeInteractiveRebaseModal()
	}
}

// interactiveRebaseRowView renders one plan row of the form: the short hash,
// subject and author of the commit, its move controls and its action choice.
// display is the plan in the execution order and pos the row's position in
// it — the form shows the newest commit on top — so the top row is already
// as late as it can go and disables moving up, the bottom row disables
// moving down. The order display holds never changes while the rows build:
// move buttons record their clicks, and the swaps apply once the whole list
// is built.
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
			// The choice wrote the row in place through the binding: the plan's
			// backing array is unchanged, so the plan version carries the
			// mutation to the derived cache.
			st.planVersion++
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
		// The swap applies once the whole list is built: a row not yet built
		// must never shift underneath the pass and draw its commit twice.
		st.moves = append(st.moves, interactiveRebaseMove{hash: row.hash, dir: dir})
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
