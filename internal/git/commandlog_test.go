package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type delegatingExecutor struct {
	Executor
}

func (d *delegatingExecutor) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return d.Executor.Run(ctx, stdin, args...)
}

func TestCommandLogCapturesSuccessStderrFailureAndDeduplicatesWrappers(t *testing.T) {
	remote := t.TempDir()
	gitRun(t, remote, "init", "--bare", "-q", "-b", "main")

	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "remote", "add", "origin", remote)

	base := New(&delegatingExecutor{Executor: &LocalExecutor{Dir: dir}})
	log := NewCommandLog()
	logged := base.WithCommandLog(log)

	if base.CommandLog() != nil {
		t.Fatal("WithCommandLog mutated base repository")
	}
	if logged.WithoutPrompts().CommandLog() != log {
		t.Fatal("WithoutPrompts lost bound CommandLog")
	}

	// Unlogged base repo must not write into log.
	if _, err := base.Status(); err != nil {
		t.Fatal(err)
	}
	if recs, dropped := log.Snapshot(); len(recs) != 0 || dropped != 0 {
		t.Fatalf("unlogged repo recorded %+v dropped %d", recs, dropped)
	}

	// PushNote writes porcelain summary to stdout on exit 0.
	if _, err := logged.PushNote("origin", "main", "main", true, false, false); err != nil {
		t.Fatal(err)
	}
	// Create a new branch in remote so Fetch writes to stderr on exit 0.
	gitRun(t, remote, "branch", "from-remote", "main")
	if err := logged.FetchOpts(FetchOptions{Remote: "origin", Prune: true}); err != nil {
		t.Fatal(err)
	}
	// Failing command preserves original *Error and ExitCode while recording into log.
	err := logged.Checkout("no-such-revision")
	if err == nil {
		t.Fatal("expected checkout failure")
	}
	var ge *Error
	if !errors.As(err, &ge) || ExitCode(err) == 0 {
		t.Fatalf("expected *Error with non-zero exit code, got %T (%v)", err, err)
	}

	recs, dropped := log.Snapshot()
	if dropped != 0 || len(recs) != 3 {
		t.Fatalf("expected 3 records (no duplicate from wrapper), got %d (dropped %d): %+v", len(recs), dropped, recs)
	}
	if recs[0].Command != "$ git push --set-upstream origin refs/heads/main:refs/heads/main --porcelain" || recs[0].ExitCode != 0 || !strings.Contains(recs[0].Stdout, "refs/heads/main") {
		t.Fatalf("push record: %+v", recs[0])
	}
	if recs[1].Command != "$ git fetch --prune origin" || recs[1].ExitCode != 0 || !strings.Contains(recs[1].Stderr, "from-remote") {
		t.Fatalf("fetch stderr-on-success record: %+v", recs[1])
	}
	if recs[2].Command != "$ git checkout no-such-revision --" || recs[2].ExitCode == 0 || !strings.Contains(recs[2].Stderr, "no-such-revision") {
		t.Fatalf("checkout failure record: %+v", recs[2])
	}

	// Snapshot must deep-copy Args so caller mutation cannot alias internal state.
	recs[0].Args[0] = "mutated"
	recs[0].Command = "mutated"
	again, _ := log.Snapshot()
	if again[0].Args[0] != "push" || again[0].Command == "mutated" {
		t.Fatalf("Snapshot aliased internal slice: %+v", again[0])
	}
}

func TestCommandLogPreservesRealExecutionArgsAndCapturesClone(t *testing.T) {
	src := initRepo(t)
	parent := t.TempDir()
	log := NewCommandLog()
	repo := New(&LocalExecutor{Dir: parent}).WithCommandLog(log)

	if err := repo.CloneInto(context.Background(), CloneOptions{URL: src, Name: "cloned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "cloned", ".git")); err != nil {
		t.Fatalf("clone did not succeed: %v", err)
	}
	recs, _ := log.Snapshot()
	if len(recs) != 1 || !strings.HasPrefix(recs[0].Command, "$ git clone -- ") {
		t.Fatalf("clone records: %+v", recs)
	}

	// Verify real execution slice passed by caller is not mutated by redaction.
	rawArgs := []string{"-c", "http.extraheader=Authorization: Bearer secret-tok", "ls-remote", "https://ada:ghp_secret@example.com/org/repo.git?access_token=qtok"}
	before := append([]string(nil), rawArgs...)
	log.Record(rawArgs, nil, "", errors.New("failed"))
	for i := range rawArgs {
		if rawArgs[i] != before[i] {
			t.Fatalf("Record mutated input args[%d]: got %q want %q", i, rawArgs[i], before[i])
		}
	}
}

func TestCommandLogRedactsCredentialsQueryTokensHeadersAndTraces(t *testing.T) {
	log := NewCommandLog()
	args := []string{
		"-c", "credential.helper=!/tmp/helper --token=sec123",
		"-c", "http.https://example.com/.extraheader=AUTHORIZATION: basic dXNlcjpwYXNz",
		"fetch",
		"https://ada:ghp_topsecret123@example.com/org/repo.git?access_token=qsecret999&foo=bar&x-amz-signature=sig777",
		"ssh://deploy:sshpass456@example.com/srv/repo.git",
		"git@example.com:org/clean.git",
		"deploy:scppass789@example.com:org/repo.git",
	}
	stdout := []byte("url=https://oauth2:glpat_secret@example.com/a/b.git?private_token=ptok_111\npassword=my_super_secret\n")
	stderr := strings.Join([]string{
		"12:00:00.000000 http.c:700              => Send header: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.secret",
		"> Authorization: Basic dXNlcjpzZWNyZXQ=",
		"> PRIVATE-TOKEN: glpat-header-secret",
		"http.extraheader=Authorization: token ghp_header_tok",
		"\x1eBRANCHLINE-ASKPASS L /tmp/branchline-ap.123456 Password for 'https://ada@example.com': ",
		"fatal: Authentication failed for 'https://ada:ghp_topsecret123@example.com/org/repo.git?token=urltok888'",
	}, "\n")

	log.Record(args, stdout, stderr, &Error{Args: args, ExitCode: 128, Stderr: stderr, Stdout: stdout})
	recs, _ := log.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("records %d", len(recs))
	}
	r := recs[0]
	combined := r.Command + "\n" + strings.Join(r.Args, "\n") + "\n" + r.Stdout + "\n" + r.Stderr + "\n" +
		BoundActivityLabel("Cloning https://ada:ghp_topsecret123@example.com/org/repo.git?access_token=qsecret999") + "\n" +
		BoundActivityError(stderr)

	forbidden := []string{
		"sec123",
		"dXNlcjpwYXNz",
		"ghp_topsecret123",
		"qsecret999",
		"sig777",
		"sshpass456",
		"scppass789",
		"glpat_secret",
		"ptok_111",
		"my_super_secret",
		"eyJhbGciOiJIUzI1NiJ9.secret",
		"dXNlcjpzZWNyZXQ=",
		"glpat-header-secret",
		"ghp_header_tok",
		"BRANCHLINE-ASKPASS",
		"urltok888",
	}
	for _, secret := range forbidden {
		if strings.Contains(combined, secret) {
			t.Fatalf("leaked secret %q in:\n%s", secret, combined)
		}
	}
	if !strings.Contains(r.Command, "https://example.com/org/repo.git?access_token=[REDACTED]&foo=bar&x-amz-signature=[REDACTED]") {
		t.Fatalf("unexpected redacted URL in command: %s", r.Command)
	}
	if !strings.Contains(r.Command, "ssh://deploy@example.com/srv/repo.git") || !strings.Contains(r.Command, "deploy@example.com:org/repo.git") {
		t.Fatalf("unexpected SSH/SCP redaction in command: %s", r.Command)
	}
}

func TestCommandLogEnforcesStrictByteAndCommandBounds(t *testing.T) {
	log := NewCommandLog()
	hugeArg := strings.Repeat("中", 3000) // 9000 UTF-8 bytes (> 4 KiB MaxCommandLineBytes)
	hugeOut := strings.Repeat("出", 8000) // 24000 UTF-8 bytes (> 16 KiB MaxCommandStreamBytes)
	hugeErr := strings.Repeat("错", 8000) // 24000 UTF-8 bytes (> 16 KiB MaxCommandStreamBytes)

	for i := 0; i < MaxLoggedCommands+10; i++ {
		log.Record([]string{"commit", "-m", hugeArg}, []byte(hugeOut), hugeErr, nil)
	}

	recs, dropped := log.Snapshot()
	if len(recs) == 0 || len(recs) > MaxLoggedCommands {
		t.Fatalf("len(recs) = %d, want 1..%d", len(recs), MaxLoggedCommands)
	}
	if dropped == 0 {
		t.Fatal("expected dropped > 0 when exceeding budget")
	}

	total := 0
	for i, r := range recs {
		if !utf8.ValidString(r.Command) || !utf8.ValidString(r.Stdout) || !utf8.ValidString(r.Stderr) {
			t.Fatalf("record %d has invalid UTF-8 after truncation", i)
		}
		if len(r.Command) > MaxCommandLineBytes {
			t.Fatalf("record %d Command len %d > %d", i, len(r.Command), MaxCommandLineBytes)
		}
		argBytes := 0
		for _, a := range r.Args {
			if !utf8.ValidString(a) {
				t.Fatalf("record %d arg invalid UTF-8", i)
			}
			argBytes += len(a)
		}
		if argBytes > MaxCommandLineBytes {
			t.Fatalf("record %d Args bytes %d > %d", i, argBytes, MaxCommandLineBytes)
		}
		if len(r.Stdout) > MaxCommandStreamBytes {
			t.Fatalf("record %d Stdout len %d > %d", i, len(r.Stdout), MaxCommandStreamBytes)
		}
		if len(r.Stderr) > MaxCommandStreamBytes {
			t.Fatalf("record %d Stderr len %d > %d", i, len(r.Stderr), MaxCommandStreamBytes)
		}
		total += r.ByteLen()
	}
	if total > MaxActivityOutputBytes || log.TotalBytes() != total {
		t.Fatalf("total bytes %d (log.TotalBytes=%d) exceeds MaxActivityOutputBytes %d", total, log.TotalBytes(), MaxActivityOutputBytes)
	}
	if !strings.HasSuffix(recs[0].Command, TruncatedArgSuffix) || !strings.HasSuffix(recs[0].Stdout, TruncatedOutputSuffix) || !strings.HasSuffix(recs[0].Stderr, TruncatedOutputSuffix) {
		t.Fatalf("record 0 missing truncation suffix: cmd=%d out=%d err=%d", len(recs[0].Command), len(recs[0].Stdout), len(recs[0].Stderr))
	}

	// Verify MaxLoggedCommands cap with small records.
	smallLog := NewCommandLog()
	for i := 0; i < MaxLoggedCommands+5; i++ {
		smallLog.Record([]string{"status"}, []byte("ok"), "", nil)
	}
	smallRecs, smallDropped := smallLog.Snapshot()
	if len(smallRecs) != MaxLoggedCommands || smallDropped != 5 {
		t.Fatalf("smallLog len=%d dropped=%d, want %d and 5", len(smallRecs), smallDropped, MaxLoggedCommands)
	}

	// Verify BoundActivityLabel and BoundActivityError limits.
	lbl := BoundActivityLabel(strings.Repeat("标", 600))
	if len(lbl) > MaxActivityLabelBytes || !utf8.ValidString(lbl) || !strings.HasSuffix(lbl, TruncatedArgSuffix) {
		t.Fatalf("BoundActivityLabel len=%d valid=%v", len(lbl), utf8.ValidString(lbl))
	}
	errText := BoundActivityError(strings.Repeat("障", 8000))
	if len(errText) > MaxActivityErrorBytes || !utf8.ValidString(errText) || !strings.HasSuffix(errText, TruncatedOutputSuffix) {
		t.Fatalf("BoundActivityError len=%d valid=%v", len(errText), utf8.ValidString(errText))
	}
}

func TestCommandLogConcurrentRepoIsolation(t *testing.T) {
	dir := initRepo(t)
	shared := New(&LocalExecutor{Dir: dir})
	logA := NewCommandLog()
	logB := NewCommandLog()
	repoA := shared.WithCommandLog(logA)
	repoB := shared.WithCommandLog(logB)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			_, _ = shared.Status()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			_, _ = repoA.TopLevel()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			_, _ = repoB.Version()
		}
	}()
	wg.Wait()

	recsA, _ := logA.Snapshot()
	recsB, _ := logB.Snapshot()
	if len(recsA) != 3 || len(recsB) != 4 {
		t.Fatalf("recsA=%d recsB=%d", len(recsA), len(recsB))
	}
	for _, r := range recsA {
		if r.Command != "$ git rev-parse --show-toplevel" {
			t.Fatalf("logA received unexpected command: %+v", r)
		}
	}
	for _, r := range recsB {
		if r.Command != "$ git --version" {
			t.Fatalf("logB received unexpected command: %+v", r)
		}
	}
}

func TestCommandLogQuotedAndPercentEncodedSecrets(t *testing.T) {
	cases := []string{
		`Cookie: "session=primaryreviewsecret"`,
		`Set-Cookie: "session=primaryreviewsecret; HttpOnly"`,
		`https://example.invalid/repo?%74oken=primaryreviewsecret`,
		`https://example.invalid/repo?access%5Ftoken=primaryreviewsecret`,
		`https://example.invalid/repo?%2574oken=primaryreviewsecret`,
		`https://example.invalid/repo%3Ftoken%3Dprimaryreviewsecret`,
		`http.extraheader="X-Custom-Authentication: primaryreviewsecret"`,
		`http.extraheader: "X-Custom-Authentication: primaryreviewsecret"`,
		`Authorization: "Bearer primaryreviewsecret"`,
		`Proxy-Authorization: 'Basic primaryreviewsecret'`,
	}
	for _, input := range cases {
		got := RedactText(input)
		if strings.Contains(got, "primaryreviewsecret") {
			t.Errorf("secret survives redaction: input=%q got=%q", input, got)
		}
	}
}

func TestCommandLogBoundsManyEmptyArguments(t *testing.T) {
	log := NewCommandLog()
	emptyArgs := make([]string, 10000)
	emptyArgs[0] = "status"
	log.Record(emptyArgs, nil, "", nil)

	recs, _ := log.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if len(recs[0].Args) > MaxCommandArgs+1 || cap(recs[0].Args) > MaxCommandArgs+1 {
		t.Fatalf("Args len=%d cap=%d exceeds MaxCommandArgs+1=%d", len(recs[0].Args), cap(recs[0].Args), MaxCommandArgs+1)
	}
	if recs[0].ByteLen() > MaxActivityOutputBytes || log.TotalBytes() != recs[0].ByteLen() {
		t.Fatalf("ByteLen=%d TotalBytes=%d", recs[0].ByteLen(), log.TotalBytes())
	}
}

func TestCommandLogTruncatedUserinfoAndUnclosedAuthority(t *testing.T) {
	cases := []string{
		"https://user:primaryreviewsecret" + strings.Repeat("a", 25000) + "@example.invalid/repo",
		"https://primaryreviewsecret" + strings.Repeat("a", 25000) + "@example.invalid/repo",
		"ssh://user:primaryreviewsecret" + strings.Repeat("a", 25000) + "@example.invalid/repo",
		"deploy:primaryreviewsecret" + strings.Repeat("a", 25000) + "@example.invalid:org/repo.git",
	}
	for _, raw := range cases {
		checks := map[string]string{
			"label": BoundActivityLabel(raw),
			"error": BoundActivityError(raw),
		}
		if strings.Contains(raw, "://") {
			checks["redact"] = RedactText(raw[:1024])
		}
		for name, got := range checks {
			if strings.Contains(got, "primaryreviewsecret") {
				t.Fatalf("%s leaks credential after prefix truncation for input prefix %.48q: got %q", name, raw, got)
			}
		}
		log := NewCommandLog()
		log.Record([]string{"clone", raw}, []byte(raw), raw, nil)
		records, _ := log.Snapshot()
		if len(records) != 1 {
			t.Fatalf("expected 1 record, got %d", len(records))
		}
		r := records[0]
		for name, got := range map[string]string{
			"command": r.Command,
			"args":    strings.Join(r.Args, " "),
			"stdout":  r.Stdout,
			"stderr":  r.Stderr,
		} {
			if strings.Contains(got, "primaryreviewsecret") {
				t.Fatalf("%s leaks credential after prefix truncation: got %q", name, got)
			}
		}
		if cap(r.Args) != len(r.Args) {
			t.Fatalf("expected tight Args slice len=%d cap=%d", len(r.Args), cap(r.Args))
		}
		if r.ByteLen() < 80+len(r.Command)+len(r.Stdout)+len(r.Stderr)+cap(r.Args)*16 {
			t.Fatalf("ByteLen=%d under-accounts for 80-byte CommandRecord header or Args cap=%d", r.ByteLen(), cap(r.Args))
		}
	}
}

func TestCommandLogURLScopedConfigKeysAndTightRecordSlice(t *testing.T) {
	cases := []string{
		"http.https://user:primaryreviewsecret@example.invalid/.extraheader=Authorization: Bearer anothertestsecret",
		"credential.https://user:primaryreviewsecret@example.invalid.helper=store",
		"http.https://user:primaryreviewsecret" + strings.Repeat("a", 25000) + "@example.invalid/.extraheader=Authorization: Bearer anothertestsecret",
		"url.https://user:primaryreviewsecret@example.invalid/.insteadOf=https://example.invalid/",
	}
	for _, input := range cases {
		for _, args := range [][]string{
			{"-c", input, "fetch"},
			{"-c" + input, "fetch"},
		} {
			l := NewCommandLog()
			l.Record(args, nil, "", nil)
			records, _ := l.Snapshot()
			if len(records) != 1 {
				t.Fatalf("got %d records", len(records))
			}
			for _, secret := range []string{"primaryreviewsecret", "anothertestsecret"} {
				if strings.Contains(records[0].Command, secret) || strings.Contains(strings.Join(records[0].Args, " "), secret) {
					t.Fatalf("URL-specific config key credential %q survives redaction for %v: Command=%q Args=%q",
						secret, args[0], records[0].Command, records[0].Args)
				}
			}
		}
	}

	// Verify l.records slice has zero uncounted spare capacity across multiple appends.
	l := NewCommandLog()
	for i := 0; i < 5; i++ {
		l.Record([]string{"status", "--short"}, []byte("M file.go\n"), "", nil)
		if cap(l.records) != len(l.records) {
			t.Fatalf("step %d: expected tight records slice len=%d cap=%d", i, len(l.records), cap(l.records))
		}
	}
}
