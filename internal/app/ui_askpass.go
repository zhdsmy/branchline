package app

import (
	"regexp"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// passDialog is the askpass modal queue for git and ssh questions. Commands
// running on background workers call wait, which blocks until the user answers
// or cancels in the dialog. Answers live in memory only and are handed back
// through the askpass named pipe; they are never written to disk or a keychain.
//
// Concurrency: background workers only enqueue requests (mu) and park on
// req.reply. The dialog fields current/open/value/seq are owned by the UI
// thread, except for the handoff inside cancelAll and answer, which deliver
// through the buffered reply channel exactly once per request.
type passDialog struct {
	mu     sync.Mutex
	queue  []*passRequest
	closed bool

	current *passRequest // UI thread; guarded by mu only inside cancelAll
	open    bool         // UI thread
	value   string       // UI thread
	seq     int          // UI thread
}

type passRequest struct {
	prompt string
	reply  chan passReply
}

type passReply struct {
	answer string
	ok     bool
}

// passKind is what a prompt asks for.
type passKind int

const (
	askPassword passKind = iota
	askPassphrase
	askUsername
	askYesNo
)

var keyPrompt = regexp.MustCompile(`Enter\s+passphrase\s*for\s*(?:key\s*)?['"]([^'"]+)['"]`)

func kindOfPrompt(prompt string) passKind {
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "passphrase"):
		return askPassphrase
	case strings.HasPrefix(lower, "username"):
		return askUsername
	case strings.Contains(lower, "(yes/no"):
		return askYesNo
	}
	return askPassword
}

// prompter returns what git.AskPass calls, from any goroutine. It blocks the
// calling worker until the user answers in the dialog on the UI thread.
func (d *passDialog) prompter(a *app) git.Prompter {
	return func(prompt string) (string, bool) {
		return d.wait(a, prompt)
	}
}

// wait enqueues the question, wakes the UI thread, and blocks until the dialog
// answers. cancelAll answers ok=false so no worker ever stays parked. A dialog
// already closed by close answers immediately: a worker arriving after
// teardown must not park on a question nobody will ever render.
func (d *passDialog) wait(a *app, prompt string) (string, bool) {
	req := &passRequest{prompt: strings.TrimSpace(prompt), reply: make(chan passReply, 1)}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return "", false
	}
	d.queue = append(d.queue, req)
	d.mu.Unlock()
	a.post(func() {})
	r := <-req.reply
	return r.answer, r.ok
}

// next shows the first question waiting, on the UI thread.
func (d *passDialog) next() {
	if d.current != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || len(d.queue) == 0 {
		return
	}
	d.current, d.queue = d.queue[0], d.queue[1:]
	d.open, d.value = true, ""
	d.seq++
}

// answer resolves the pending question and resets the dialog draft. It runs
// on the UI thread; the waiting worker wakes with the reply.
func (d *passDialog) answer(value string, ok bool) {
	if d.current == nil {
		return
	}
	d.current.reply <- passReply{answer: value, ok: ok}
	d.current, d.open, d.value = nil, false, ""
}

// cancelAllLocked drops the pending and every queued question with a cancel
// answer, with d.mu held. It runs only on the UI thread or in teardown: like
// cancelAll and close, it touches the UI-owned current/open/value fields and
// must never race the rendering in view.
func (d *passDialog) cancelAllLocked() {
	queued := d.queue
	d.queue = nil
	current := d.current
	d.current = nil
	d.open, d.value = false, ""
	if current != nil {
		current.reply <- passReply{ok: false}
	}
	for _, req := range queued {
		req.reply <- passReply{ok: false}
	}
}

// cancelAll drops the pending and every queued question with a cancel answer,
// on repository switches and app teardown, so no worker is left parked on a
// dialog that will never render again. Only the UI thread or teardown calls
// it: it hands answers into reply channels a view-pass answer could be using.
func (d *passDialog) cancelAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelAllLocked()
}

// close is the final cancelAll: it marks the dialog closed, clears the queue,
// and cancels the pending question in a single critical section. Keeping the
// three steps inside one lock hold is deliberate simplicity: the closed check
// inside wait already turns late questions down, and doing the rest under the
// same hold means the invariant never depends on how the two halves interleave.
// App teardown calls it before draining workers so a late question cannot
// park its worker past the drain. Only the UI thread or teardown may call it,
// never a background worker.
func (d *passDialog) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.cancelAllLocked()
}

// view renders the modal for the pending question. It is built at the end of
// the app view so ui.Modal mounts into the window-level overlay. Any dismissal
// path — Escape, clicking the backdrop, or the Cancel button — must answer
// ok=false immediately so the waiting worker never hangs.
func (d *passDialog) view(c *ui.Context) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	d.next()
	if d.current == nil {
		return
	}
	modal := ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(440).Gap(12).Children(func() {
			d.content(c)
		})
	})
	if modal.Dismissed() || !d.open {
		d.answer("", false)
	}
}

func (d *passDialog) content(c *ui.Context) {
	t := c.Theme()
	prompt := d.current.prompt
	kind := kindOfPrompt(prompt)
	title, label, note, okLabel := "The remote host requires password", "Password:", prompt, "OK"
	switch kind {
	case askPassphrase:
		title, label = "Passphrase for SSH key", "Passphrase:"
		if m := keyPrompt.FindStringSubmatch(prompt); m != nil {
			note = "Enter passphrase for SSH key '" + m[1] + "'"
		}
	case askUsername:
		title, label = "The remote host requires user name", "User name:"
	case askYesNo:
		title, label, okLabel = "Connect to this host?", "Answer:", "Connect"
	}
	ui.Text(c, title).FontSize(16).Bold()
	ui.Text(c, note).FontSize(12).TextColor(t.TextMuted).Selectable()
	submitted := false
	if kind != askYesNo {
		// c.Key(d.seq) isolates the draft editing state and undo history of
		// each question from the previous one.
		input := ui.TextInput(c.Key(d.seq), &d.value).FillWidth().AutoFocus().Label(strings.TrimSuffix(label, ":"))
		if kind != askUsername {
			input.Password()
		}
		submitted = input.Submitted()
		ui.Text(c, "The answer goes to git or ssh for this command only. Branchline does not save it.").FontSize(11).TextColor(t.TextMuted)
	}
	ui.Row(c).FillWidth().Gap(8).Margin(8, 0, 0, 0).Children(func() {
		ui.Spacer(c)
		if ui.Button(c, "Cancel").Clicked() {
			d.answer("", false)
			return
		}
		if ui.PrimaryButton(c, okLabel).Clicked() || submitted {
			if kind == askYesNo {
				d.answer("yes", true)
			} else {
				d.answer(d.value, true)
			}
		}
	})
}

// repoWithAskPass returns a shallow copy of r whose executor asks through ask,
// like (*git.Repo).WithoutPrompts but in reverse. Snapshot reads keep using the
// original promptless repository; only sync operations use the copy.
func repoWithAskPass(r *git.Repo, ask *git.AskPass) *git.Repo {
	if r == nil || ask == nil {
		return r
	}
	switch ex := r.Exec.(type) {
	case *git.SSHExecutor:
		cp := *ex
		cp.AskPass = ask
		return r.WithExecutor(&cp)
	case *git.LocalExecutor:
		cp := *ex
		cp.AskPass = ask
		return r.WithExecutor(&cp)
	default:
		return r
	}
}
