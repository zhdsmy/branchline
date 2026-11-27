package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// interactiveRebaseFoldChain is one kept commit of the plan and the fold
// chain hanging off it: parent indexes the commit the chain folds into, tail
// the last row of the chain — a squash, a fixup, or the parent itself — and
// squash the indices of its squash members, the only rows whose messages
// join the fold message.
type interactiveRebaseFoldChain struct {
	parent int
	tail   int
	squash []int
}

// interactiveRebaseFoldChains groups the form rows oldest-first into fold
// chains, the way the backend's rebaseMessageOwner reads them: a drop row
// neither starts a chain nor breaks one — a squash after a drop still folds
// into the most recent kept commit — and a fixup folds the files of its row
// without joining its message. A squash or fixup on the oldest kept commit
// is an invalid plan the form already refuses; it orphans here.
func interactiveRebaseFoldChains(rows []*interactiveRebaseRow) []interactiveRebaseFoldChain {
	var chains []interactiveRebaseFoldChain
	for i, row := range rows {
		switch row.action {
		case "Drop":
			continue
		case "Squash", "Fixup":
			if len(chains) == 0 {
				continue
			}
			c := &chains[len(chains)-1]
			c.tail = i
			if row.action == "Squash" {
				c.squash = append(c.squash, i)
			}
		default:
			chains = append(chains, interactiveRebaseFoldChain{parent: i, tail: i})
		}
	}
	return chains
}

// irDerivedCache is the one-entry cache of everything the form derives from
// the plan rows and the message state: the fold chains, the message targets,
// the validation error and drop count of the plan, the blocker the start
// button and the message panel read, and the original messages still to read.
// A hit answers every one of them without scanning a single row.
type irDerivedCache struct {
	rows        []*interactiveRebaseRow
	planVersion uint64
	msgVersion  uint64
	valid       bool

	chains        []interactiveRebaseFoldChain
	targets       map[string]interactiveRebaseFoldChain
	problem       string
	drops         int
	msgLoading    bool
	msgFailed     bool
	msgValidation string
	needed        []string
}

// irDerived returns the derived results of the current plan rows and message
// state, computing them the first time this combination is asked for. The
// key is the plan's mutation count, the rows backing array itself and the
// message mutation count: an action or a move mutates rows in place and
// counts on the plan version to invalidate, a wholesale replacement swaps the
// backing array the comparison reads, and every message input mutation counts
// on the message version. Retaining the rows in the cache keeps their backing
// array alive, so a fresh plan read can never reuse its address. The zero
// cache keyed by a nil rows slice is a correct hit: a form without a plan
// derives nothing.
func (st *interactiveRebaseUIState) irDerived(ms *interactiveRebaseMessagesState) *irDerivedCache {
	c := &st.derived
	if c.valid && c.planVersion == st.planVersion && c.msgVersion == ms.version && sameBackingSlice(c.rows, st.rows) {
		return c
	}
	rows := st.rows
	c.rows = rows
	c.planVersion = st.planVersion
	c.msgVersion = ms.version
	c.chains = interactiveRebaseFoldChains(rows)
	c.targets = interactiveRebaseMessageTargets(rows, c.chains)
	c.problem = interactiveRebasePlanProblem(rows)
	c.drops = 0
	for _, row := range rows {
		if row.action == "Drop" {
			c.drops++
		}
	}
	c.msgLoading, c.msgFailed, c.msgValidation = interactiveRebaseMessagesBlocker(rows, c.targets, ms)
	c.needed = interactiveRebaseNeededMessages(rows, c.targets, ms)
	c.valid = true
	return c
}

// interactiveRebaseMessagesState is the per-app state of the interactive
// rebase message editing: the full original messages of the commits the plan
// rewrites or folds, read asynchronously off the UI thread, the user's custom
// drafts keyed by the full hash of the chain parent they belong to, and the
// message editor's open target. It lives in its own feature state outside the
// form state, so the two lifecycles never share a struct.
type interactiveRebaseMessagesState struct {
	repoGen uint64

	originals map[string]string
	failed    map[string]bool
	reading   bool
	readErr   error
	drafts    map[string]string

	// version counts every mutation of the three message inputs — originals,
	// failures and drafts — whether a map entry arrives, changes or is
	// forgotten. Replacing the state wholesale needs no count: its content is
	// the empty state the version zero already describes. Everything derived
	// from these inputs keys on the count.
	version uint64

	editingHash string
	editText    string
	editDirty   bool

	// editFocus is the one-shot request to move the keyboard focus into
	// the editor's text area, set as the editor opens and cleared as it
	// lands or the editor closes. AutoFocus cannot serve here: the area's
	// key never changes, so it does not appear when the editor retargets
	// another row, and while the original messages are still on their way
	// the area appears disabled, so the frame of its birth passes long
	// before it can take input. The request waits for the frame the area
	// is enabled instead.
	editFocus bool
}

// interactiveRebaseMessages returns the app's message state, resetting it
// wholesale when the repository generation moved past it: original messages
// key on commit hashes of the old repository and drafts on its plan rows, so
// nothing of either survives a switch. The in-flight read of the old
// repository is invalidated with it, and its late callback can never land in
// the next repository's cache.
func interactiveRebaseMessages(a *app) *interactiveRebaseMessagesState {
	ms := featureState(a, func() *interactiveRebaseMessagesState {
		return &interactiveRebaseMessagesState{
			originals: map[string]string{},
			failed:    map[string]bool{},
			drafts:    map[string]string{},
		}
	})
	if gen := a.session.Generation(); ms.repoGen != gen {
		if ms.reading {
			a.session.cancelRead("ir-msg")
		}
		ms.resetInteractiveRebaseMessages(gen)
	}
	return ms
}

// resetInteractiveRebaseMessages drops every cached original, failure, draft
// and editor target of the state.
func (ms *interactiveRebaseMessagesState) resetInteractiveRebaseMessages(gen uint64) {
	ms.repoGen = gen
	ms.originals = map[string]string{}
	ms.failed = map[string]bool{}
	ms.reading = false
	ms.readErr = nil
	ms.drafts = map[string]string{}
	ms.version++
	ms.editingHash = ""
	ms.editText = ""
	ms.editDirty = false
	ms.editFocus = false
}

// clearInteractiveRebaseMessages closes the message editor and discards the
// whole cache: the form it belonged to is gone, and a reopened form reads the
// repository as it is then. Canceling the in-flight read keeps its late
// result out of the cache of a form reopened before it landed.
func (a *app) clearInteractiveRebaseMessages() {
	ms := interactiveRebaseMessages(a)
	ms.resetInteractiveRebaseMessages(a.session.Generation())
	a.session.cancelRead("ir-msg")
}

// interactiveRebaseMessageTargets collects the chains whose execution needs
// a full commit message: every chain whose parent is Reword, and every chain
// carrying a squash member whose parent is not Edit — an Edit parent's
// message is the user's to write at the stop, so its chain neither reads nor
// compiles one. The result is keyed by the full hash of the chain parent.
func interactiveRebaseMessageTargets(rows []*interactiveRebaseRow, chains []interactiveRebaseFoldChain) map[string]interactiveRebaseFoldChain {
	targets := map[string]interactiveRebaseFoldChain{}
	for _, chain := range chains {
		parent := rows[chain.parent]
		if parent.action == "Reword" || (len(chain.squash) > 0 && parent.action != "Edit") {
			targets[parent.hash] = chain
		}
	}
	return targets
}

// interactiveRebaseMessageHashes lists the commits whose full original
// messages the chain's default message joins: the parent first, then every
// squash member oldest-to-newest. Fixup members contribute their files only,
// never their messages.
func interactiveRebaseMessageHashes(rows []*interactiveRebaseRow, chain interactiveRebaseFoldChain) []string {
	hashes := []string{rows[chain.parent].hash}
	for _, i := range chain.squash {
		hashes = append(hashes, rows[i].hash)
	}
	return hashes
}

// interactiveRebaseDefaultMessage composes the chain's default message from
// the original messages of its members: the parent's full message, then each
// squash member's, trimmed and joined by a blank line, exactly as git's own
// squash composition orders them. A missing or failed member reports through
// loading or failed, and the message comes back empty — the caller must not
// use it.
func interactiveRebaseDefaultMessage(rows []*interactiveRebaseRow, chain interactiveRebaseFoldChain, originals map[string]string, failed map[string]bool) (message string, loading, failure bool) {
	var parts []string
	for _, hash := range interactiveRebaseMessageHashes(rows, chain) {
		if failed[hash] {
			return "", false, true
		}
		text, ok := originals[hash]
		if !ok {
			return "", true, false
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.Join(parts, "\n\n"), false, false
}

// interactiveRebaseChainMessage resolves the message one target chain would
// execute with: the user's draft when one exists, else the default composed
// from the originals. Chains with a draft neither load nor fail: their text
// no longer reads the originals at all.
func interactiveRebaseChainMessage(rows []*interactiveRebaseRow, chain interactiveRebaseFoldChain, ms *interactiveRebaseMessagesState) (message string, loading, failure bool) {
	parentHash := rows[chain.parent].hash
	if draft, ok := ms.drafts[parentHash]; ok {
		return draft, false, false
	}
	return interactiveRebaseDefaultMessage(rows, chain, ms.originals, ms.failed)
}

// interactiveRebaseMessagesBlocker surveys every target chain of the current
// rows and reports why the rebase cannot start yet: originals still loading,
// originals whose read failed, or a resolved message that trims to nothing.
// The empty-message validation is the form's own error text, shown next to
// the plan the user edits.
func interactiveRebaseMessagesBlocker(rows []*interactiveRebaseRow, targets map[string]interactiveRebaseFoldChain, ms *interactiveRebaseMessagesState) (loading, failure bool, validation string) {
	for hash, chain := range targets {
		message, chainLoading, chainFailed := interactiveRebaseChainMessage(rows, chain, ms)
		loading = loading || chainLoading
		failure = failure || chainFailed
		if chainLoading || chainFailed {
			continue
		}
		if strings.TrimSpace(message) == "" {
			validation = "Commit " + irShort(hash) + " needs a commit message."
		}
	}
	return loading, failure, validation
}

// interactiveRebaseNeededMessages lists the original messages the target
// chains still need: every member hash of every chain without a custom draft,
// minus what the cache already holds. Failed hashes stay out — re-reading
// them on every render would spin on a persistent failure; the Retry Message
// button forgets the failures first, and its read is this same one, re-run.
func interactiveRebaseNeededMessages(rows []*interactiveRebaseRow, targets map[string]interactiveRebaseFoldChain, ms *interactiveRebaseMessagesState) []string {
	seen := map[string]bool{}
	var needed []string
	for hash, chain := range targets {
		if _, drafted := ms.drafts[hash]; drafted {
			continue
		}
		for _, member := range interactiveRebaseMessageHashes(rows, chain) {
			if seen[member] {
				continue
			}
			seen[member] = true
			if _, ok := ms.originals[member]; ok {
				continue
			}
			if ms.failed[member] {
				continue
			}
			needed = append(needed, member)
		}
	}
	return needed
}

// ensureInteractiveRebaseMessages starts the asynchronous read of the full
// original messages the current rows need and do not have yet. One read
// covers the whole missing set in a single batch; while it is in flight no
// second read starts — the rows that arrived in between are picked up by the
// next render once the batch lands. Reads never substitute the one-line
// subjects: a hash the read could not fetch is a failure the form surfaces,
// never a silent fallback.
func ensureInteractiveRebaseMessages(a *app, st *interactiveRebaseUIState) {
	ms := interactiveRebaseMessages(a)
	if ms.reading || !st.open || st.loading || st.readErr != nil {
		return
	}
	needed := st.irDerived(ms).needed
	if len(needed) == 0 {
		return
	}
	hashes := append([]string(nil), needed...)
	onto := st.onto
	ms.reading = true
	if len(ms.failed) == 0 {
		ms.readErr = nil
	}
	ok := sessionRead(a.session, "ir-msg", func(repo *git.Repo) (map[string]string, error) {
		results := make(map[string]string, len(hashes))
		var firstErr error
		for _, hash := range hashes {
			text, err := repo.Message(hash)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			results[hash] = text
		}
		return results, firstErr
	}, func(results map[string]string, err error) {
		ms.reading = false
		if !st.open || st.onto != onto {
			return
		}
		if ms.originals == nil {
			ms.originals = map[string]string{}
		}
		if ms.failed == nil {
			ms.failed = map[string]bool{}
		}
		for hash, text := range results {
			ms.originals[hash] = text
			delete(ms.failed, hash)
		}
		failed := false
		for _, hash := range hashes {
			if _, ok := results[hash]; !ok {
				ms.failed[hash] = true
				failed = true
			}
		}
		if failed {
			ms.readErr = err
		} else if len(ms.failed) == 0 {
			ms.readErr = nil
		}
		// The batch mutated the originals and the failures; everything derived
		// from the messages recomputes on the next frame.
		ms.version++
		// Pick up any rows that became target chains while this batch was in
		// flight without waiting for another render pass.
		ensureInteractiveRebaseMessages(a, st)
	})
	if !ok {
		ms.reading = false
	}
}

// retryInteractiveRebaseMessages clears the recorded failures and starts the
// read of everything still missing: the same ensure the renders run, but
// with the failures forgotten so their hashes join the batch again.
func (a *app) retryInteractiveRebaseMessages(st *interactiveRebaseUIState) {
	ms := interactiveRebaseMessages(a)
	ms.failed = map[string]bool{}
	ms.readErr = nil
	// Forgetting the failures admits their hashes into the needed set again:
	// a mutation of the message inputs the derived results key on.
	ms.version++
	ensureInteractiveRebaseMessages(a, st)
}

// interactiveRebaseRowMessageButton is the compact message-editing entry of
// one plan row: present on a Reword row and on a chain parent whose chain
// carries a squash member, unless the parent is Edit — those rows own a
// message the panel edits, and the rest do not. The label carries the short
// hash so several open editors never collide, and the button text gains a
// marker once the chain holds a custom draft.
func (a *app) interactiveRebaseRowMessageButton(c *ui.Context, p palette, st *interactiveRebaseUIState, display []*interactiveRebaseRow, pos int) {
	row := display[pos]
	ms := interactiveRebaseMessages(a)
	// A row owns an editable message exactly when its hash keys a target
	// chain: every Reword parent, and every squash-carrying chain parent but
	// an Edit one — the very rows the derived cache's targets name.
	if _, eligible := st.irDerived(ms).targets[row.hash]; !eligible {
		return
	}
	text := "Edit Message"
	if _, drafted := ms.drafts[row.hash]; drafted {
		text += " •"
	}
	if ui.Button(c.Key("ir-msg-btn-"+row.hash), text).Label("Edit Message "+irShort(row.hash)).FontSize(11).Padding(2, 6).Shrink(0).Clicked() {
		if ms.editingHash == row.hash {
			a.closeInteractiveRebaseMessageEditor()
		} else {
			a.openInteractiveRebaseMessageEditor(st, row.hash)
		}
	}
}

// openInteractiveRebaseMessageEditor points the message editor at the chain
// of hash and seeds its text: the custom draft when one exists, else the
// default message of the chain — empty while the originals are still on
// their way, in which case the editor waits disabled and the text arrives
// with the read. An editor seeded from the default is clean: it follows the
// composition as the chain's membership changes until the first keystroke
// makes it dirty.
func (a *app) openInteractiveRebaseMessageEditor(st *interactiveRebaseUIState, hash string) {
	ms := interactiveRebaseMessages(a)
	ms.editingHash = hash
	// Opening the editor is the user's request to write the message: the
	// focus goes to the text area, in the first frame it can take input.
	ms.editFocus = true
	if draft, ok := ms.drafts[hash]; ok {
		ms.editText = draft
		ms.editDirty = true
		return
	}
	prefill := ""
	chains := interactiveRebaseFoldChains(st.rows)
	for _, chain := range chains {
		if st.rows[chain.parent].hash == hash {
			message, loading, failure := interactiveRebaseChainMessage(st.rows, chain, ms)
			if !loading && !failure {
				prefill = message
			}
			break
		}
	}
	ms.editText = prefill
	ms.editDirty = false
}

// closeInteractiveRebaseMessageEditor collapses the editing panel. The text
// the user typed survives as the draft of its chain — the panel is a view of
// the draft, never its owner — and only Reset Message discards one.
func (a *app) closeInteractiveRebaseMessageEditor() {
	ms := interactiveRebaseMessages(a)
	ms.editingHash = ""
	ms.editText = ""
	ms.editDirty = false
	// A closed editor must never draw the focus back to an area that is
	// no longer there, so a pending focus request dies with it.
	ms.editFocus = false
}

// interactiveRebaseMessagePanel renders the message state of the plan below
// the commit list: the loading hint or read failure with its Retry Message
// button — either one blocks the rebase, and hiding the reason would leave
// the disabled start button unexplained — the empty-message validation, and
// the editor itself when it is open. The editor lives inside the same modal
// as the list, never in a second one.
func (a *app) interactiveRebaseMessagePanel(c *ui.Context, p palette, st *interactiveRebaseUIState) {
	ms := interactiveRebaseMessages(a)
	rows := st.rows
	derived := st.irDerived(ms)
	targets := derived.targets

	// An editor whose row lost the message it edited collapses by itself: the
	// action leaving Reword, the squash chain dissolving, or the parent
	// turning Edit all end the chain's ownership of an editable message.
	if ms.editingHash != "" {
		if _, ok := targets[ms.editingHash]; !ok {
			a.closeInteractiveRebaseMessageEditor()
		}
	}

	loading, failure, validation := derived.msgLoading, derived.msgFailed, derived.msgValidation
	switch {
	case failure:
		errText := "reading the commit messages failed"
		if ms.readErr != nil {
			errText = ms.readErr.Error()
		}
		ui.Text(c, errText).FontSize(12).TextColor(p.danger).MaxWidth(604)
		if ui.Button(c.Key("ir-msg-retry"), "Retry Message").Label("Retry Message").FontSize(11).Padding(2, 6).Clicked() {
			a.retryInteractiveRebaseMessages(st)
		}
	case loading:
		ui.Text(c, "Loading commit message…").FontSize(12).TextColor(p.muted)
	}
	if validation != "" {
		ui.Text(c, validation).FontSize(12).TextColor(p.danger).MaxWidth(604)
	}

	if ms.editingHash == "" {
		return
	}
	chain, ok := targets[ms.editingHash]
	if !ok {
		return
	}
	short := irShort(ms.editingHash)
	title := "Message for " + short
	if len(chain.squash) > 0 {
		title = "Squash message for " + short
	}

	// The editor mirrors the composition of the original messages, never the
	// draft: feeding the draft back as the default would make every custom
	// text indistinguishable from it.
	defaultText, chainLoading, chainFailed := interactiveRebaseDefaultMessage(rows, chain, ms.originals, ms.failed)
	if _, drafted := ms.drafts[ms.editingHash]; drafted {
		chainLoading, chainFailed = false, false
	}

	ui.Column(c.Key("ir-msg-panel")).FillWidth().Gap(6).Padding(8, 10, 10, 10).Background(p.surface).Radius(8).Children(func() {
		ui.Text(c, title).FontSize(12.5).FontWeight(600).TextColor(p.text)

		area := ui.TextArea(c.Key("ir-msg-text"), &ms.editText).
			Placeholder("Commit message").
			Label("Commit message").
			Height(110).
			FillWidth().
			Disabled(chainLoading || chainFailed)
		if ms.editFocus && !chainLoading && !chainFailed {
			// The focus request lands in the first frame the area can take
			// input: neither a disabled area nor a frame earlier than the
			// read's landing can hold it, so it is consumed only here.
			area.Focus()
			ms.editFocus = false
		}
		// The widget applies the input it took this pass lazily: reading
		// Changed flushes it into the bound value before the state below
		// judges it, so a keystroke can never be judged against text from
		// before it landed.
		changed := area.Changed()
		if !chainLoading && !chainFailed {
			// An untouched editor mirrors the default composition, recomputed
			// with the chain's membership. The mirror writes after this pass's
			// input already landed — a frame that carries a keystroke reports
			// it through changed and skips the mirror — and the editor picks
			// the new text up on the next build.
			if !ms.editDirty && !changed && ms.editText != defaultText {
				ms.editText = defaultText
			}
			if ms.editText != defaultText {
				// The text diverged from the default composition: it is the
				// user's own words, kept verbatim as the chain's draft, and no
				// recompute of the default touches it.
				if ms.drafts == nil {
					ms.drafts = map[string]string{}
				}
				if prev, ok := ms.drafts[ms.editingHash]; !ok || prev != ms.editText {
					ms.drafts[ms.editingHash] = ms.editText
					ms.version++
				}
				ms.editDirty = true
			} else {
				// Typed back to the default, byte for byte: the chain holds no
				// custom message anymore and follows the composition again.
				if _, ok := ms.drafts[ms.editingHash]; ok {
					delete(ms.drafts, ms.editingHash)
					ms.version++
				}
				ms.editDirty = false
			}
		}

		ui.Row(c.Key("ir-msg-actions")).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c.Key("ir-msg-reset"), "Reset Message").Label("Reset Message").FontSize(11).Padding(2, 6).Clicked() {
				if _, ok := ms.drafts[ms.editingHash]; ok {
					delete(ms.drafts, ms.editingHash)
					ms.version++
				}
				ms.editText = defaultText
				ms.editDirty = false
			}
			if ui.Button(c.Key("ir-msg-done"), "Done").Label("Done").FontSize(11).Padding(2, 6).Clicked() {
				a.closeInteractiveRebaseMessageEditor()
			}
		})
	})
}

// buildInteractiveRebasePlan compiles the form rows into the execution plan
// of the rebase. Two message rules protect the bodies of the rewritten
// commits: every squash row ships an empty subject, so the backend's
// single-line squash draft never replaces the folded messages, and every
// target chain carries its complete multi-line message on the chain's tail
// row as a prepared message — the user's draft when one exists, else the
// default composition of the original full messages. A Reword parent turns
// into a plain pick in the plan: the prepared message on its chain's tail
// rewrites the text after the commit lands, without git's editor. Chains
// whose parent is Edit ship no prepared message at all: whatever the user
// amends at the stop is the message, and the plan must not overwrite it. A
// stale draft of a row that no longer owns a chain never enters the plan.
func (a *app) buildInteractiveRebasePlan(st *interactiveRebaseUIState) ([]git.RebaseTodo, error) {
	ms := interactiveRebaseMessages(a)
	rows := st.rows
	plan := make([]git.RebaseTodo, len(rows))
	for i, row := range rows {
		plan[i] = git.RebaseTodo{
			Action:  irActionBackend(row.action),
			Hash:    row.hash,
			Subject: row.subject,
		}
		if row.action == "Squash" {
			plan[i].Subject = ""
		}
	}
	for _, chain := range interactiveRebaseFoldChains(rows) {
		parent := rows[chain.parent]
		if parent.action == "Edit" {
			continue
		}
		if parent.action != "Reword" && len(chain.squash) == 0 {
			continue
		}
		message, loading, failure := interactiveRebaseChainMessage(rows, chain, ms)
		if failure {
			return nil, fmt.Errorf("cannot rebase: reading the commit messages failed; use Retry Message")
		}
		if loading {
			return nil, fmt.Errorf("cannot rebase: the commit messages are still loading")
		}
		if strings.TrimSpace(message) == "" {
			return nil, fmt.Errorf("cannot rebase: commit %s needs a commit message", irShort(parent.hash))
		}
		plan[chain.tail].PreparedMessage = message
		if parent.action == "Reword" {
			plan[chain.parent].Action = "pick"
		}
	}
	return plan, nil
}
