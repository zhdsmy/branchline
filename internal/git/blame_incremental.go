package git

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"time"
)

// BlameHunk is one entry of git blame --incremental: the commit a run of
// lines of the final file is attributed to. Line is the run's first line
// in the final file, counting from 1, and Lines its length. Author, Date
// and Subject are the commit's; Boundary marks a commit at the edge of the
// blamed history, the root commit among them. The field names follow
// BlameLine, which the hunks spread onto when they reach the view.
type BlameHunk struct {
	Hash     string
	Line     int
	Lines    int
	Author   string
	Date     time.Time
	Subject  string
	Boundary bool
}

// BlameIncremental annotates path at rev, HEAD when rev is empty, calling
// fn with each run of lines as git blame --incremental reports it. The
// runs arrive while blame is still computing, most recent commits first
// and out of order, where BlameLines answers only after the whole blame;
// fn runs on the goroutine that called BlameIncremental. Canceling the
// context of the Repo's WithContext copy stops git: fn is not called again
// and the error reports the cancellation, as errors.Is(err,
// context.Canceled) shows.
func (r *Repo) BlameIncremental(path, rev string, fn func(BlameHunk)) error {
	if rev == "" {
		rev = "HEAD"
	}
	args := []string{"blame", "--incremental", "-w", rev, "--", path}
	pr, pw := io.Pipe()
	runErr := make(chan error, 1)
	go func() {
		err := r.runStreaming(r.baseCtx(), slow, nil, pw, args...)
		pw.CloseWithError(err)
		runErr <- err
	}()
	// Canceling the Repo's context ends the parsing even while a write is
	// still in flight: closing the reader fails the write, the copy of the
	// output stops, and the command the cancellation already killed returns.
	// The watch ends with the call, whatever happens to the context.
	base := r.baseCtx()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-base.Done():
			pr.CloseWithError(base.Err())
		case <-watchDone:
		}
	}()
	p := &blameIncrementalParser{fn: fn, commits: map[string]*blameCommitMeta{}}
	buf := make([]byte, 32*1024)
	clean := false
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			p.write(buf[:n])
		}
		if err != nil {
			// The leftover bytes of a complete stream end with a newline.
			clean = err == io.EOF
			break
		}
	}
	if clean {
		p.flush()
	}
	return <-runErr
}

// blameCommitMeta is what one commit's first blame entry carries: entries
// of the same commit after it name only the hash, and the parser answers
// them from here.
type blameCommitMeta struct {
	author   string
	when     time.Time
	subject  string
	boundary bool
}

// blameIncrementalParser parses the output of git blame --incremental as
// it is written, in pieces that can end at any byte. An entry opens with
// "<hash> <original line> <final line> <number of lines>", carries the
// commit's details when the commit appears for the first time, and always
// ends with its filename line; lines of the file itself are not part of
// the output.
type blameIncrementalParser struct {
	fn      func(BlameHunk)
	line    []byte
	commits map[string]*blameCommitMeta
	cur     *blameCommitMeta
	hunk    BlameHunk
	inEntry bool
}

func (p *blameIncrementalParser) write(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			p.line = append(p.line, b...)
			return
		}
		p.line = append(p.line, b[:i]...)
		p.lineEnd(string(p.line))
		p.line = p.line[:0]
		b = b[i+1:]
	}
}

// flush answers the last line of a stream that ends without one.
func (p *blameIncrementalParser) flush() {
	if len(p.line) > 0 {
		p.lineEnd(string(p.line))
		p.line = nil
	}
}

func (p *blameIncrementalParser) lineEnd(l string) {
	if p.inEntry {
		switch {
		case strings.HasPrefix(l, "author "):
			p.cur.author = l[len("author "):]
		case strings.HasPrefix(l, "author-time "):
			if n, err := strconv.ParseInt(l[len("author-time "):], 10, 64); err == nil {
				p.cur.when = time.Unix(n, 0)
			}
		case strings.HasPrefix(l, "summary "):
			p.cur.subject = l[len("summary "):]
		case l == "boundary":
			p.cur.boundary = true
		case strings.HasPrefix(l, "filename "):
			h := p.hunk
			h.Author, h.Date, h.Subject = p.cur.author, p.cur.when, p.cur.subject
			h.Boundary = h.Boundary || p.cur.boundary
			p.inEntry = false
			p.fn(h)
		}
		// The tags an entry can grow are ignored, as the format's own
		// parsing advice says, so a newer git's entries still read.
		return
	}
	f := strings.Fields(l)
	if len(f) < 4 || len(f[0]) < 40 {
		return
	}
	hash := f[0]
	boundary := false
	// Boundary commits once carried a caret ahead of their hash; git no
	// longer prints it there, but stripping it costs nothing if one does.
	if strings.HasPrefix(hash, "^") {
		hash = hash[1:]
		boundary = true
	}
	if !isHex(hash) {
		return
	}
	finalLine, err1 := strconv.Atoi(f[2])
	numLines, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil || numLines < 1 {
		return
	}
	meta := p.commits[hash]
	if meta == nil {
		meta = &blameCommitMeta{}
		p.commits[hash] = meta
	}
	p.cur = meta
	p.hunk = BlameHunk{Hash: hash, Line: finalLine, Lines: numLines, Boundary: boundary}
	p.inEntry = true
}
