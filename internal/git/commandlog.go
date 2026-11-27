package git

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// MaxLoggedCommands bounds the number of Git invocations stored per Activity entry.
	MaxLoggedCommands = 64
	// MaxCommandArgs bounds the number of arguments formatted or retained per command.
	MaxCommandArgs = 64
	// MaxCommandLineBytes bounds the formatted "$ git ..." command string and
	// the retained Args slice bytes per logged command, including any truncation marker.
	MaxCommandLineBytes = 4 * 1024
	// MaxCommandStreamBytes bounds each command's retained Stdout and Stderr string,
	// including any truncation marker.
	MaxCommandStreamBytes = 16 * 1024
	// MaxActivityOutputBytes bounds the total retained bytes across all CommandRecords
	// (including Command, Args, Stdout, Stderr, truncation markers, and slice/record overhead)
	// in one CommandLog.
	MaxActivityOutputBytes = 128 * 1024
	// MaxActivityLabelBytes bounds an Activity entry's display label.
	MaxActivityLabelBytes = 1024
	// MaxActivityErrorBytes bounds an Activity entry's display error string.
	MaxActivityErrorBytes = 16 * 1024

	perArgOverheadBytes    = 16
	perRecordOverheadBytes = 80

	TruncatedOutputSuffix = "\n...[truncated]"
	TruncatedArgSuffix    = "...[truncated]"
)

// CommandRecord is one Git command invocation captured for an Activity entry.
type CommandRecord struct {
	Command  string
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

// ByteLen returns the total retained bytes (strings plus structural overhead) in r.
func (r CommandRecord) ByteLen() int {
	argSlots := max(len(r.Args), cap(r.Args))
	n := perRecordOverheadBytes + len(r.Command) + len(r.Stdout) + len(r.Stderr) + argSlots*perArgOverheadBytes
	for _, a := range r.Args {
		n += len(a)
	}
	return n
}

// CommandLog collects redacted, size-bounded Git command records for one Activity task.
type CommandLog struct {
	mu         sync.Mutex
	records    []CommandRecord
	totalBytes int
	dropped    int
}

// NewCommandLog returns an empty CommandLog for one Activity task.
func NewCommandLog() *CommandLog {
	return &CommandLog{}
}

type commandLogKey struct{}
type execCaptureKey struct{}

type execCapture struct {
	stderr string
}

// WithCommandLog attaches l to ctx so Executor.Run calls using ctx record into l.
func WithCommandLog(ctx context.Context, l *CommandLog) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, commandLogKey{}, l)
}

// CommandLogFromContext returns the CommandLog attached to ctx, if any.
func CommandLogFromContext(ctx context.Context) *CommandLog {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(commandLogKey{}).(*CommandLog)
	return l
}

func setExecStderr(ctx context.Context, stderr string) {
	if ctx == nil || stderr == "" {
		return
	}
	if cap, ok := ctx.Value(execCaptureKey{}).(*execCapture); ok && cap != nil {
		cap.stderr = stderr
	}
}

func recordInvocation(ctx context.Context, fallback *CommandLog, args []string, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(execCaptureKey{}) != nil {
		return fn(ctx)
	}
	log := CommandLogFromContext(ctx)
	if log == nil {
		log = fallback
	}
	if log == nil {
		return fn(ctx)
	}
	cap := &execCapture{}
	runCtx := context.WithValue(WithCommandLog(ctx, log), execCaptureKey{}, cap)
	out, err := fn(runCtx)
	log.Record(args, out, cap.stderr, err)
	return out, err
}

// Record stores one completed Git invocation after redacting secrets and enforcing
// per-field, per-command, and per-activity byte and record limits.
// It never mutates the caller's args or stdout slices.
func (l *CommandLog) Record(args []string, stdout []byte, stderr string, err error) {
	if l == nil || len(args) == 0 {
		return
	}
	code := 0
	if err != nil {
		code = -1
		var ge *Error
		if errors.As(err, &ge) {
			code = ge.ExitCode
			if strings.TrimSpace(ge.Stderr) != "" {
				stderr = ge.Stderr
			}
			if len(stdout) == 0 && len(ge.Stdout) > 0 {
				stdout = ge.Stdout
			}
		} else if strings.TrimSpace(stderr) == "" {
			stderr = err.Error()
		}
	}

	l.mu.Lock()
	if len(l.records) >= MaxLoggedCommands || l.totalBytes+perRecordOverheadBytes >= MaxActivityOutputBytes {
		l.dropped++
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()

	redactedArgs := RedactArgs(args)
	cmdLine := "$ " + FormatCommand(redactedArgs)
	outStr, outPreTrunc := redactBounded(strings.TrimRight(string(stdout), "\r\n"), MaxCommandStreamBytes)
	errStr, errPreTrunc := redactBounded(strings.TrimRight(stderr, "\r\n"), MaxCommandStreamBytes)

	l.mu.Lock()
	defer l.mu.Unlock()

	rem := MaxActivityOutputBytes - l.totalBytes - perRecordOverheadBytes
	if len(l.records) >= MaxLoggedCommands || rem <= 0 {
		l.dropped++
		return
	}

	cmdLimit := min(MaxCommandLineBytes, rem)
	boundedCmd := truncateBytesUTF8(cmdLine, cmdLimit, TruncatedArgSuffix)
	rem -= len(boundedCmd)
	if boundedCmd == "" {
		l.dropped++
		return
	}

	argsLimit := min(MaxCommandLineBytes, rem)
	boundedArgs, argsCost := boundArgsSlice(redactedArgs, argsLimit)
	rem -= argsCost

	outLimit := min(MaxCommandStreamBytes, rem)
	boundedOut := truncateOrForceSuffix(outStr, outLimit, outPreTrunc, TruncatedOutputSuffix)
	rem -= len(boundedOut)

	errLimit := min(MaxCommandStreamBytes, rem)
	boundedErr := truncateOrForceSuffix(errStr, errLimit, errPreTrunc, TruncatedOutputSuffix)
	rem -= len(boundedErr)

	rec := CommandRecord{
		Command:  boundedCmd,
		Args:     boundedArgs,
		Stdout:   boundedOut,
		Stderr:   boundedErr,
		ExitCode: code,
	}
	tightRecords := make([]CommandRecord, len(l.records)+1)
	copy(tightRecords, l.records)
	tightRecords[len(l.records)] = rec
	l.records = tightRecords
	l.totalBytes += rec.ByteLen()
}

// Snapshot returns a deep copy of the recorded commands and the count of omitted commands.
func (l *CommandLog) Snapshot() (records []CommandRecord, dropped int) {
	if l == nil {
		return nil, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.records) == 0 {
		return nil, l.dropped
	}
	out := make([]CommandRecord, len(l.records))
	for i, r := range l.records {
		var copiedArgs []string
		if len(r.Args) > 0 {
			copiedArgs = make([]string, len(r.Args))
			copy(copiedArgs, r.Args)
		}
		out[i] = CommandRecord{
			Command:  r.Command,
			Args:     copiedArgs,
			Stdout:   r.Stdout,
			Stderr:   r.Stderr,
			ExitCode: r.ExitCode,
		}
	}
	return out, l.dropped
}

// TotalBytes returns the total retained bytes across all records in l,
// including any spare backing-array capacity on the records slice.
func (l *CommandLog) TotalBytes() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	spareRecords := max(cap(l.records)-len(l.records), 0) * perRecordOverheadBytes
	return l.totalBytes + spareRecords
}

func boundArgsSlice(args []string, maxBytes int) ([]string, int) {
	if len(args) == 0 || maxBytes < perArgOverheadBytes {
		return nil, 0
	}
	limitCount := min(len(args), MaxCommandArgs)
	out := make([]string, 0, limitCount)
	used := 0
	for i := 0; i < limitCount; i++ {
		a := args[i]
		rem := maxBytes - used - perArgOverheadBytes
		if rem < 0 {
			break
		}
		if a == "" {
			out = append(out, "")
			used += perArgOverheadBytes
			continue
		}
		if rem == 0 {
			break
		}
		b := truncateBytesUTF8(a, rem, TruncatedArgSuffix)
		if b == "" {
			break
		}
		out = append(out, b)
		used += perArgOverheadBytes + len(b)
		if len(b) < len(a) {
			break
		}
	}
	if len(out) == 0 {
		return nil, 0
	}
	if cap(out) != len(out) {
		tight := make([]string, len(out))
		copy(tight, out)
		out = tight
	}
	return out, used
}

func redactBounded(s string, maxBytes int) (string, bool) {
	if s == "" || maxBytes <= 0 {
		return "", s != ""
	}
	window := maxBytes + 2048
	preTrunc := false
	if len(s) > window {
		for window > maxBytes && !utf8.ValidString(s[:window]) {
			window--
		}
		s = s[:window]
		preTrunc = true
	}
	return redactText(s, preTrunc), preTrunc
}

func truncateOrForceSuffix(s string, maxBytes int, forceTrunc bool, suffix string) string {
	if !forceTrunc || len(s) > maxBytes {
		return truncateBytesUTF8(s, maxBytes, suffix)
	}
	if maxBytes <= 0 {
		return ""
	}
	if len(s)+len(suffix) <= maxBytes {
		return strings.Clone(s) + suffix
	}
	return truncateBytesUTF8(s+suffix, maxBytes, suffix)
}

// BoundActivityLabel redacts and bounds an Activity entry label to MaxActivityLabelBytes.
func BoundActivityLabel(label string) string {
	r, pre := redactBounded(label, MaxActivityLabelBytes)
	return truncateOrForceSuffix(r, MaxActivityLabelBytes, pre, TruncatedArgSuffix)
}

// BoundActivityError redacts and bounds an Activity entry error to MaxActivityErrorBytes.
func BoundActivityError(msg string) string {
	r, pre := redactBounded(msg, MaxActivityErrorBytes)
	return truncateOrForceSuffix(r, MaxActivityErrorBytes, pre, TruncatedOutputSuffix)
}

func truncateBytesUTF8(s string, maxBytes int, suffix string) string {
	if s == "" || maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return strings.Clone(s)
	}
	if maxBytes <= len(suffix) {
		n := maxBytes
		for n > 0 && !utf8.ValidString(suffix[:n]) {
			n--
		}
		return strings.Clone(suffix[:n])
	}
	budget := maxBytes - len(suffix)
	for budget > 0 && !utf8.ValidString(s[:budget]) {
		budget--
	}
	return strings.Clone(s[:budget]) + suffix
}

// FormatCommand formats git arguments as "git <quoted args...>".
func FormatCommand(args []string) string {
	if len(args) == 0 {
		return "git"
	}
	limit := min(len(args), MaxCommandArgs)
	parts := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		a := args[i]
		if a != "" && !strings.ContainsAny(a, " \t\r\n'\"\\$`*?[]{}()<>|&;#~!") {
			parts = append(parts, a)
		} else {
			parts = append(parts, ShellQuote(a))
		}
	}
	if len(args) > limit {
		parts = append(parts, TruncatedArgSuffix)
	}
	return "git " + strings.Join(parts, " ")
}

// RedactArgs returns a new slice with credentials, headers, and URL secrets redacted,
// bounded to at most MaxCommandArgs elements. It never modifies args in place.
func RedactArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	truncatedCount := len(args) > MaxCommandArgs
	limit := min(len(args), MaxCommandArgs)
	capLen := limit
	if truncatedCount {
		capLen++
	}
	out := make([]string, limit, capLen)
	copy(out, args[:limit])
	for i := 0; i < limit; i++ {
		if out[i] == "-c" && i+1 < limit {
			out[i+1] = redactConfigArg(out[i+1])
			i++
			continue
		}
		if strings.HasPrefix(out[i], "-c") && len(out[i]) > 2 {
			out[i] = truncateBytesUTF8("-c"+redactConfigArg(out[i][2:]), MaxCommandLineBytes, TruncatedArgSuffix)
			continue
		}
		r, pre := redactBounded(out[i], MaxCommandLineBytes)
		out[i] = truncateOrForceSuffix(r, MaxCommandLineBytes, pre, TruncatedArgSuffix)
	}
	if truncatedCount {
		out = append(out, TruncatedArgSuffix)
	}
	return out
}

func redactConfigArg(kv string) string {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		r, pre := redactBounded(kv, MaxCommandLineBytes)
		return truncateOrForceSuffix(r, MaxCommandLineBytes, pre, TruncatedArgSuffix)
	}
	lower := strings.ToLower(strings.TrimSpace(k))
	sensitiveKey := isSensitiveConfigKey(lower)
	rk, kPre := redactBounded(k, MaxCommandLineBytes)
	boundedK := truncateOrForceSuffix(rk, MaxCommandLineBytes, kPre, TruncatedArgSuffix)
	if sensitiveKey {
		return truncateBytesUTF8(boundedK+"=[REDACTED]", MaxCommandLineBytes, TruncatedArgSuffix)
	}
	if kPre || len(boundedK) >= MaxCommandLineBytes {
		return truncateBytesUTF8(boundedK, MaxCommandLineBytes, TruncatedArgSuffix)
	}
	budget := max(MaxCommandLineBytes-len(boundedK)-1, 64)
	rv, vPre := redactBounded(v, budget)
	boundedV := truncateOrForceSuffix(rv, budget, vPre, TruncatedArgSuffix)
	return truncateBytesUTF8(boundedK+"="+boundedV, MaxCommandLineBytes, TruncatedArgSuffix)
}

func isSensitiveConfigKey(lower string) bool {
	return strings.HasPrefix(lower, "credential.") ||
		lower == "http.extraheader" ||
		strings.HasSuffix(lower, ".extraheader") ||
		strings.HasSuffix(lower, ".password") ||
		strings.HasSuffix(lower, ".token") ||
		strings.HasSuffix(lower, ".secret") ||
		strings.HasSuffix(lower, ".apitoken") ||
		strings.HasSuffix(lower, ".apikey")
}

// RedactText scrubs URL userinfo, sensitive URL query parameters (including
// percent-encoded keys/values), HTTP auth/token/cookie headers, extraheader
// config values, GIT_TRACE/GIT_TRACE_CURL secret lines, and internal askpass markers.
func RedactText(s string) string {
	return redactText(s, false)
}

func redactText(s string, truncatedAtEnd bool) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	keep := make([]string, 0, len(lines))
	for i, line := range lines {
		lineTrunc := truncatedAtEnd && i == len(lines)-1
		if idx := strings.Index(line, askMarker); idx >= 0 {
			line = line[:idx]
			lineTrunc = false
			if strings.TrimSpace(line) == "" {
				continue
			}
		}
		line = redactLine(line, lineTrunc)
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

func redactLine(line string, truncatedAtEnd bool) string {
	line = redactURLsInText(line, truncatedAtEnd)
	line = redactSCPUserinfoInText(line, truncatedAtEnd)
	line = redactHeaderLine(line)
	line = redactBearerAndBasicTokens(line)
	line = redactSensitiveKeyValues(line)
	return line
}

var urlSchemes = []string{"https://", "http://", "ssh://", "git://", "ftps://", "ftp://"}

func redactURLsInText(s string, truncatedAtEnd bool) string {
	lower := strings.ToLower(s)
	hasScheme := false
	for _, sch := range urlSchemes {
		if strings.Contains(lower, sch) {
			hasScheme = true
			break
		}
	}
	if !hasScheme {
		return redactStandaloneQuery(s)
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		matchPos := -1
		for _, sch := range urlSchemes {
			if p := indexFold(s[i:], sch); p >= 0 {
				abs := i + p
				if matchPos < 0 || abs < matchPos {
					matchPos = abs
				}
			}
		}
		if matchPos < 0 {
			b.WriteString(redactStandaloneQuery(s[i:]))
			break
		}
		b.WriteString(redactStandaloneQuery(s[i:matchPos]))
		end := matchPos
		for end < len(s) && !isURLDelimiter(s[end]) {
			end++
		}
		rawURL := s[matchPos:end]
		urlTrunc := truncatedAtEnd && end == len(s)
		if urlTrunc {
			b.WriteString(redactSingleURL(rawURL, true))
		} else {
			trimmed, tail := trimURLTrailingPunct(rawURL)
			b.WriteString(redactSingleURL(trimmed, false))
			b.WriteString(tail)
		}
		i = end
	}
	return b.String()
}

func isURLDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\'', '"', '<', '>', '(', ')', '[', ']', '{', '}':
		return true
	}
	return false
}

func trimURLTrailingPunct(u string) (trimmed, tail string) {
	hasPath := false
	if sep := strings.Index(u, "://"); sep >= 0 {
		hasPath = strings.ContainsAny(u[sep+3:], "/?#")
	}
	end := len(u)
	for end > 0 {
		switch u[end-1] {
		case '.', ',', ';':
			end--
		case ':':
			if !hasPath {
				return u[:end], u[end:]
			}
			end--
		default:
			return u[:end], u[end:]
		}
	}
	return u, ""
}

func redactSingleURL(raw string, truncatedAtEnd bool) string {
	schemeSep := strings.Index(raw, "://")
	if schemeSep < 0 {
		return raw
	}
	scheme := raw[:schemeSep]
	rest := raw[schemeSep+3:]

	authEnd := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == '?' || rest[i] == '#' {
			authEnd = i
			break
		}
	}
	authority := rest[:authEnd]
	tail := rest[authEnd:]

	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userinfo := authority[:at]
		host := authority[at+1:]
		if strings.EqualFold(scheme, "ssh") {
			if colon := strings.IndexByte(userinfo, ':'); colon >= 0 {
				authority = userinfo[:colon] + "@" + host
			}
		} else {
			authority = host
		}
	} else if (truncatedAtEnd && authEnd == len(rest)) || !isValidURLHostPort(authority) {
		authority = "[REDACTED]"
	}

	tail = redactURLQuery(tail)
	return scheme + "://" + authority + tail
}

func isValidURLHostPort(authority string) bool {
	if authority == "" {
		return true
	}
	if strings.HasPrefix(authority, "[") {
		closeIdx := strings.IndexByte(authority, ']')
		if closeIdx <= 1 {
			return false
		}
		rem := authority[closeIdx+1:]
		if rem == "" {
			return true
		}
		return strings.HasPrefix(rem, ":") && isValidNumericPort(rem[1:])
	}
	host := authority
	if colon := strings.IndexByte(authority, ':'); colon >= 0 {
		if colon == 0 || !isValidNumericPort(authority[colon+1:]) {
			return false
		}
		host = authority[:colon]
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	labelLen := 0
	for i := 0; i < len(host); i++ {
		if host[i] == '.' {
			if labelLen == 0 || labelLen > 63 {
				return false
			}
			labelLen = 0
			continue
		}
		labelLen++
		if labelLen > 63 {
			return false
		}
	}
	return labelLen > 0 && labelLen <= 63
}

func isValidNumericPort(port string) bool {
	if len(port) == 0 || len(port) > 5 {
		return false
	}
	n := 0
	for i := 0; i < len(port); i++ {
		c := port[i]
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

func redactURLQuery(tail string) string {
	qIdx := strings.IndexByte(tail, '?')
	if qIdx < 0 {
		// Check for percent-encoded '?' such as %3F or %3f.
		if p := indexFold(tail, "%3f"); p >= 0 {
			return tail[:p+3] + redactQueryPairs(tail[p+3:])
		}
		return tail
	}
	prefix := tail[:qIdx+1]
	rest := tail[qIdx+1:]
	frag := ""
	if fIdx := strings.IndexByte(rest, '#'); fIdx >= 0 {
		frag = rest[fIdx:]
		rest = rest[:fIdx]
	}
	return prefix + redactQueryPairs(rest) + frag
}

func redactStandaloneQuery(s string) string {
	qIdx := strings.IndexByte(s, '?')
	if qIdx < 0 {
		return s
	}
	end := qIdx + 1
	for end < len(s) && !isURLDelimiter(s[end]) {
		end++
	}
	return s[:qIdx+1] + redactQueryPairs(s[qIdx+1:end]) + s[end:]
}

func unescapeRepeated(s string) string {
	cur := s
	for i := 0; i < 3; i++ {
		next, err := url.QueryUnescape(cur)
		if err != nil || next == cur {
			break
		}
		cur = next
	}
	return cur
}

func redactQueryPairs(query string) string {
	if query == "" {
		return ""
	}
	parts := strings.Split(query, "&")
	for i, p := range parts {
		k, _, ok := strings.Cut(p, "=")
		if ok {
			decodedK := unescapeRepeated(k)
			if isSensitiveQueryKey(decodedK) {
				parts[i] = k + "=[REDACTED]"
				continue
			}
		}
		decodedPair := unescapeRepeated(p)
		if dk, _, dok := strings.Cut(decodedPair, "="); dok && isSensitiveQueryKey(dk) {
			parts[i] = dk + "=[REDACTED]"
		}
	}
	return strings.Join(parts, "&")
}

func isSensitiveQueryKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "token", "access_token", "refresh_token", "private_token", "oauth_token",
		"id_token", "auth_token", "api_key", "apikey", "key", "secret",
		"client_secret", "password", "passwd", "pass", "sig", "signature",
		"x-amz-signature", "x-amz-security-token", "x-goog-signature",
		"credential", "credentials":
		return true
	}
	return strings.HasSuffix(k, "_token") ||
		strings.HasSuffix(k, "-token") ||
		strings.HasSuffix(k, "_secret") ||
		strings.HasSuffix(k, "-secret") ||
		strings.HasSuffix(k, "_key") ||
		strings.HasSuffix(k, "-key") ||
		strings.HasSuffix(k, "password")
}

func redactSCPUserinfoInText(s string, truncatedAtEnd bool) string {
	if !strings.Contains(s, ":") && (!strings.Contains(s, "@") || !truncatedAtEnd) {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		for i < len(s) && isURLDelimiter(s[i]) {
			b.WriteByte(s[i])
			i++
		}
		start := i
		for i < len(s) && !isURLDelimiter(s[i]) {
			i++
		}
		tok := s[start:i]
		b.WriteString(redactSCPToken(tok, truncatedAtEnd && i == len(s)))
	}
	return b.String()
}

func redactSCPToken(tok string, truncatedAtEnd bool) string {
	if strings.Contains(tok, "://") {
		return tok
	}
	at := strings.IndexByte(tok, '@')
	if at <= 0 {
		if truncatedAtEnd {
			if colon := strings.IndexByte(tok, ':'); colon > 0 && !strings.ContainsAny(tok[:colon], "/\\=") {
				return tok[:colon] + ":[REDACTED]"
			}
		}
		return tok
	}
	userPass := tok[:at]
	hostPath := tok[at+1:]
	colon := strings.IndexByte(userPass, ':')
	if colon <= 0 || strings.ContainsAny(userPass, "/\\=") {
		return tok
	}
	if !truncatedAtEnd && (!strings.Contains(hostPath, ":") || strings.HasPrefix(hostPath, ":")) {
		return tok
	}
	return userPass[:colon] + "@" + hostPath
}

var sensitiveHeaders = []string{
	"proxy-authorization:",
	"authorization:",
	"x-gitlab-token:",
	"private-token:",
	"x-api-key:",
	"set-cookie:",
	"cookie:",
}

func redactHeaderLine(line string) string {
	if !strings.Contains(line, ":") && !strings.Contains(line, "=") {
		return line
	}
	if idx := indexFold(line, "extraheader="); idx >= 0 {
		return line[:idx+len("extraheader=")] + "[REDACTED]"
	}
	if idx := indexFold(line, "extraheader:"); idx >= 0 {
		return line[:idx+len("extraheader:")] + " [REDACTED]"
	}
	for _, h := range sensitiveHeaders {
		if idx := indexFold(line, h); idx >= 0 {
			if idx == 0 || !isIdentChar(line[idx-1]) {
				return line[:idx+len(h)] + " [REDACTED]"
			}
		}
	}
	return line
}

func redactBearerAndBasicTokens(line string) string {
	if !strings.Contains(line, " ") {
		return line
	}
	for _, prefix := range []string{"Bearer ", "bearer ", "Basic ", "basic "} {
		searchFrom := 0
		for searchFrom < len(line) {
			idx := strings.Index(line[searchFrom:], prefix)
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			if abs > 0 && isIdentChar(line[abs-1]) {
				searchFrom = abs + len(prefix)
				continue
			}
			valStart := abs + len(prefix)
			for valStart < len(line) && line[valStart] == ' ' {
				valStart++
			}
			valEnd := valStart
			for valEnd < len(line) && !isURLDelimiter(line[valEnd]) && line[valEnd] != ',' && line[valEnd] != ';' {
				valEnd++
			}
			if valEnd > valStart {
				line = line[:valStart] + "[REDACTED]" + line[valEnd:]
				searchFrom = valStart + len("[REDACTED]")
			} else {
				searchFrom = valStart
			}
		}
	}
	return line
}

var sensitiveKVKeys = []string{
	"access_token",
	"refresh_token",
	"private_token",
	"oauth_token",
	"auth_token",
	"id_token",
	"client_secret",
	"api_key",
	"apikey",
	"password",
	"passwd",
	"secret",
	"token",
}

func redactSensitiveKeyValues(line string) string {
	if !strings.Contains(line, "=") && !strings.Contains(line, ":") {
		return line
	}
	for _, key := range sensitiveKVKeys {
		for _, sep := range []string{"=", ": "} {
			pat := key + sep
			searchFrom := 0
			for searchFrom < len(line) {
				idx := indexFold(line[searchFrom:], pat)
				if idx < 0 {
					break
				}
				abs := searchFrom + idx
				if abs > 0 && isIdentChar(line[abs-1]) {
					searchFrom = abs + len(pat)
					continue
				}
				valStart := abs + len(pat)
				if valStart < len(line) && (line[valStart] == '"' || line[valStart] == '\'') {
					q := line[valStart]
					valStart++
					valEnd := valStart
					for valEnd < len(line) && line[valEnd] != q {
						valEnd++
					}
					line = line[:valStart] + "[REDACTED]" + line[valEnd:]
					searchFrom = valStart + len("[REDACTED]")
					continue
				}
				valEnd := valStart
				for valEnd < len(line) && !isURLDelimiter(line[valEnd]) && line[valEnd] != '&' && line[valEnd] != ',' && line[valEnd] != ';' {
					valEnd++
				}
				if valEnd > valStart {
					line = line[:valStart] + "[REDACTED]" + line[valEnd:]
					searchFrom = valStart + len("[REDACTED]")
				} else {
					searchFrom = valStart
				}
			}
		}
	}
	return line
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '.'
}

func indexFold(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}
