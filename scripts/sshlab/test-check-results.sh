#!/bin/sh
# Self-test suite for scripts/sshlab/check-results.py.
# Covers PASS, SKIP, missing test, missing package, subtest SKIP/FAIL, only-package-pass,
# malformed/truncated/empty JSON, non-zero exit code, empty/mismatched manifest, and stale log.
# Runs purely on synthetic fixtures without executing `go test ./...`.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
CHECKER="${SCRIPT_DIR}/check-results.py"

command -v python3 >/dev/null 2>&1 || {
	printf 'test-check-results: ERROR: python3 not found\n' >&2
	exit 1
}

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/bl-check-results-test.XXXXXX")
cleanup() {
	rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM HUP

PYTHONPYCACHEPREFIX="${WORK_DIR}/pycache"
export PYTHONPYCACHEPREFIX
python3 -m py_compile "$CHECKER"

PASS_COUNT=0

assert_ok() {
	_desc=$1
	shift
	if "$@" >"${WORK_DIR}/last.out" 2>"${WORK_DIR}/last.err"; then
		PASS_COUNT=$((PASS_COUNT + 1))
		printf 'ok %d - %s\n' "$PASS_COUNT" "$_desc"
	else
		_rc=$?
		printf 'FAIL: expected 0 exit for [%s], got %d\n' "$_desc" "$_rc" >&2
		cat "${WORK_DIR}/last.out" "${WORK_DIR}/last.err" >&2
		exit 1
	fi
}

assert_fail_contains() {
	_desc=$1
	_needle=$2
	shift 2
	set +e
	"$@" >"${WORK_DIR}/last.out" 2>"${WORK_DIR}/last.err"
	_rc=$?
	set -e
	if [ "$_rc" -eq 0 ]; then
		printf 'FAIL: expected non-zero exit for [%s], got 0\n' "$_desc" >&2
		cat "${WORK_DIR}/last.out" "${WORK_DIR}/last.err" >&2
		exit 1
	fi
	if ! grep -Fq "$_needle" "${WORK_DIR}/last.err" && ! grep -Fq "$_needle" "${WORK_DIR}/last.out"; then
		printf 'FAIL: expected output for [%s] to contain %s\n' "$_desc" "$_needle" >&2
		cat "${WORK_DIR}/last.out" "${WORK_DIR}/last.err" >&2
		exit 1
	fi
	PASS_COUNT=$((PASS_COUNT + 1))
	printf 'ok %d - %s\n' "$PASS_COUNT" "$_desc"
}

# 1. Generate valid dynamic manifest across multiple packages (root, internal/git, internal/app, internal/repository, plus one no-test-files package).
LIST_VALID="${WORK_DIR}/list-valid.json"
cat >"$LIST_VALID" <<'EOF'
{"Time":"2026-11-06T12:00:00.000000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline"}
{"Time":"2026-11-06T12:00:00.010000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline","Output":"TestConfiguredLinuxRootWorkflow\n"}
{"Time":"2026-11-06T12:00:00.020000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline","Output":"ok  \tgithub.com/zhdsmy/branchline\t0.020s\n"}
{"Time":"2026-11-06T12:00:00.020000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Elapsed":0.02}
{"Time":"2026-11-06T12:00:00.030000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/git"}
{"Time":"2026-11-06T12:00:00.040000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/git","Output":"TestConfiguredLinuxGitOps\n"}
{"Time":"2026-11-06T12:00:00.050000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/git","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/git\t0.020s\n"}
{"Time":"2026-11-06T12:00:00.050000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/git","Elapsed":0.02}
{"Time":"2026-11-06T12:00:00.060000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/placeholder"}
{"Time":"2026-11-06T12:00:00.065000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/placeholder","Output":"?   \tgithub.com/zhdsmy/branchline/internal/placeholder\t[no test files]\n"}
{"Time":"2026-11-06T12:00:00.065000+08:00","Action":"skip","Package":"github.com/zhdsmy/branchline/internal/placeholder","Elapsed":0}
{"Time":"2026-11-06T12:00:00.070000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}
{"Time":"2026-11-06T12:00:00.080000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"TestConfiguredLinuxSSHWorkflow\n"}
{"Time":"2026-11-06T12:00:00.085000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"TestConfiguredLinuxSSHCancellationAndSignals\n"}
{"Time":"2026-11-06T12:00:00.090000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/app\t0.020s\n"}
{"Time":"2026-11-06T12:00:00.090000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.02}
{"Time":"2026-11-06T12:00:00.100000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/repository"}
{"Time":"2026-11-06T12:00:00.110000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/repository","Output":"TestConfiguredLinuxSSHMergeWorkflow\n"}
{"Time":"2026-11-06T12:00:00.120000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/repository","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/repository\t0.020s\n"}
{"Time":"2026-11-06T12:00:00.120000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Elapsed":0.02}
EOF

MANIFEST_VALID="${WORK_DIR}/manifest-valid.json"
assert_ok "manifest generation across 4 packages and 5 TestConfiguredLinux* tests" \
	python3 "$CHECKER" manifest \
	--list-json "$LIST_VALID" \
	--list-exit-code 0 \
	--output "$MANIFEST_VALID" \
	--run-id "run-001" \
	--created-at "2026-11-06T12:00:01.000000+08:00" \
	--ssh-env "e1" \
	--ssh-host "bl-sshlab-e1" \
	--ssh-repo "/tmp/branchline-test-e1"

PKGS_OUT=$(python3 "$CHECKER" packages --manifest "$MANIFEST_VALID" | tr '\n' ' ')
[ "$PKGS_OUT" = "github.com/zhdsmy/branchline github.com/zhdsmy/branchline/internal/app github.com/zhdsmy/branchline/internal/git github.com/zhdsmy/branchline/internal/repository " ] || {
	printf 'FAIL: unexpected packages output: %s\n' "$PKGS_OUT" >&2
	exit 1
}

# 2. PASS case: all 5 top-level TestConfiguredLinux* and subtests run+pass; ordinary test SKIP is allowed.
RUN_PASS="${WORK_DIR}/run-pass.json"
cat >"$RUN_PASS" <<'EOF'
{"Time":"2026-11-06T12:00:02.000000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline"}
{"Time":"2026-11-06T12:00:02.010000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline","Test":"TestConfiguredLinuxRootWorkflow"}
{"Time":"2026-11-06T12:00:02.050000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Test":"TestConfiguredLinuxRootWorkflow","Elapsed":0.04}
{"Time":"2026-11-06T12:00:02.060000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline","Output":"ok  \tgithub.com/zhdsmy/branchline\t0.060s\n"}
{"Time":"2026-11-06T12:00:02.060000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Elapsed":0.06}
{"Time":"2026-11-06T12:00:02.100000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/git"}
{"Time":"2026-11-06T12:00:02.110000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/git","Test":"TestOrdinaryLocalOptionalTool"}
{"Time":"2026-11-06T12:00:02.115000+08:00","Action":"skip","Package":"github.com/zhdsmy/branchline/internal/git","Test":"TestOrdinaryLocalOptionalTool","Elapsed":0.005}
{"Time":"2026-11-06T12:00:02.120000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/git","Test":"TestConfiguredLinuxGitOps"}
{"Time":"2026-11-06T12:00:02.150000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/git","Test":"TestConfiguredLinuxGitOps","Elapsed":0.03}
{"Time":"2026-11-06T12:00:02.160000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/git","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/git\t0.060s\n"}
{"Time":"2026-11-06T12:00:02.160000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/git","Elapsed":0.06}
{"Time":"2026-11-06T12:00:02.170000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/placeholder"}
{"Time":"2026-11-06T12:00:02.175000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/placeholder","Output":"?   \tgithub.com/zhdsmy/branchline/internal/placeholder\t[no test files]\n"}
{"Time":"2026-11-06T12:00:02.175000+08:00","Action":"skip","Package":"github.com/zhdsmy/branchline/internal/placeholder","Elapsed":0}
{"Time":"2026-11-06T12:00:02.200000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}
{"Time":"2026-11-06T12:00:02.210000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow"}
{"Time":"2026-11-06T12:00:02.250000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow","Elapsed":0.04}
{"Time":"2026-11-06T12:00:02.260000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals"}
{"Time":"2026-11-06T12:00:02.270000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals/upload_exits_after_ready"}
{"Time":"2026-11-06T12:00:02.280000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals/upload_exits_after_ready","Elapsed":0.01}
{"Time":"2026-11-06T12:00:02.285000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals/HUP"}
{"Time":"2026-11-06T12:00:02.290000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals/HUP","Elapsed":0.005}
{"Time":"2026-11-06T12:00:02.300000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals","Elapsed":0.04}
{"Time":"2026-11-06T12:00:02.310000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/app\t0.110s\n"}
{"Time":"2026-11-06T12:00:02.310000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.11}
{"Time":"2026-11-06T12:00:02.400000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/repository"}
{"Time":"2026-11-06T12:00:02.410000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow"}
{"Time":"2026-11-06T12:00:02.420000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline/internal/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow/save"}
{"Time":"2026-11-06T12:00:02.440000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow/save","Elapsed":0.02}
{"Time":"2026-11-06T12:00:02.450000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow","Elapsed":0.04}
{"Time":"2026-11-06T12:00:02.460000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline/internal/repository","Output":"ok  \tgithub.com/zhdsmy/branchline/internal/repository\t0.060s\n"}
{"Time":"2026-11-06T12:00:02.460000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Elapsed":0.06}
EOF

assert_ok "verify PASS with 5 top-level tests, subtests, and allowed ordinary test SKIP" \
	python3 "$CHECKER" verify \
	--manifest "$MANIFEST_VALID" \
	--json "$RUN_PASS" \
	--exit-code 0 \
	--run-id "run-001" \
	--ssh-env "e1" \
	--ssh-host "bl-sshlab-e1" \
	--ssh-repo "/tmp/branchline-test-e1" \
	--report-json "${WORK_DIR}/gate-pass.json"

# 3. Top-level TestConfiguredLinux* SKIP -> must FAIL.
RUN_TOP_SKIP="${WORK_DIR}/run-top-skip.json"
sed 's/"Action":"pass","Package":"github.com\/zhdsmy\/branchline\/internal\/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow"/"Action":"skip","Package":"github.com\/zhdsmy\/branchline\/internal\/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow"/' "$RUN_PASS" >"$RUN_TOP_SKIP"
assert_fail_contains "reject top-level TestConfiguredLinux SKIP" \
	"SKIP detected in ConfiguredLinux test/subtest" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_TOP_SKIP" --exit-code 0

# 4. Subtest of TestConfiguredLinux* SKIP -> must FAIL even if parent test passes.
RUN_SUB_SKIP="${WORK_DIR}/run-sub-skip.json"
sed 's/"Action":"pass","Package":"github.com\/zhdsmy\/branchline\/internal\/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals\/HUP"/"Action":"skip","Package":"github.com\/zhdsmy\/branchline\/internal\/app","Test":"TestConfiguredLinuxSSHCancellationAndSignals\/HUP"/' "$RUN_PASS" >"$RUN_SUB_SKIP"
assert_fail_contains "reject TestConfiguredLinux subtest SKIP" \
	"TestConfiguredLinuxSSHCancellationAndSignals/HUP" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_SUB_SKIP" --exit-code 0

# 5. Subtest of TestConfiguredLinux* FAIL -> must FAIL.
RUN_SUB_FAIL="${WORK_DIR}/run-sub-fail.json"
sed 's/"Action":"pass","Package":"github.com\/zhdsmy\/branchline\/internal\/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow\/save"/"Action":"fail","Package":"github.com\/zhdsmy\/branchline\/internal\/repository","Test":"TestConfiguredLinuxSSHMergeWorkflow\/save"/' "$RUN_PASS" >"$RUN_SUB_FAIL"
assert_fail_contains "reject TestConfiguredLinux subtest FAIL" \
	"FAIL detected in test: github.com/zhdsmy/branchline/internal/repository TestConfiguredLinuxSSHMergeWorkflow/save" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_SUB_FAIL" --exit-code 0

# 6. Missing required top-level test (缺测试) -> must FAIL.
RUN_MISSING_TEST="${WORK_DIR}/run-missing-test.json"
grep -v 'TestConfiguredLinuxGitOps' "$RUN_PASS" >"$RUN_MISSING_TEST"
assert_fail_contains "reject missing required top-level test" \
	"required top-level test github.com/zhdsmy/branchline/internal/git TestConfiguredLinuxGitOps expected run=1, got run=0" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_MISSING_TEST" --exit-code 0

# 7. Missing required package (缺包) -> must FAIL.
RUN_MISSING_PKG="${WORK_DIR}/run-missing-pkg.json"
grep -v '"Package":"github.com/zhdsmy/branchline/internal/repository"' "$RUN_PASS" >"$RUN_MISSING_PKG"
assert_fail_contains "reject missing required package" \
	"missing required package from JSON log: github.com/zhdsmy/branchline/internal/repository" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_MISSING_PKG" --exit-code 0

# 8. Only package pass without running tests (只有package pass) -> must FAIL.
RUN_ONLY_PKG_PASS="${WORK_DIR}/run-only-pkg-pass.json"
cat >"$RUN_ONLY_PKG_PASS" <<'EOF'
{"Time":"2026-11-06T12:00:02.000000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline"}
{"Time":"2026-11-06T12:00:02.010000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Elapsed":0.01}
{"Time":"2026-11-06T12:00:02.020000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/git"}
{"Time":"2026-11-06T12:00:02.030000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/git","Elapsed":0.01}
{"Time":"2026-11-06T12:00:02.040000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}
{"Time":"2026-11-06T12:00:02.050000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.01}
{"Time":"2026-11-06T12:00:02.060000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline/internal/repository"}
{"Time":"2026-11-06T12:00:02.070000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Elapsed":0.01}
EOF
assert_fail_contains "reject package-only pass with no test run/pass events" \
	"expected run=1, got run=0" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_ONLY_PKG_PASS" --exit-code 0

# 9. Test pass without run event -> must FAIL.
RUN_PASS_NO_RUN="${WORK_DIR}/run-pass-no-run.json"
grep -v '"Action":"run","Package":"github.com/zhdsmy/branchline","Test":"TestConfiguredLinuxRootWorkflow"' "$RUN_PASS" >"$RUN_PASS_NO_RUN"
assert_fail_contains "reject test pass without prior run event" \
	"test github.com/zhdsmy/branchline TestConfiguredLinuxRootWorkflow had events without a 'run' event" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_PASS_NO_RUN" --exit-code 0

# 10. Cached package result `(cached)` on required package -> must FAIL.
RUN_CACHED="${WORK_DIR}/run-cached.json"
sed 's/ok  \\tgithub.com\/zhdsmy\/branchline\/internal\/app\\t0.110s/ok  \\tgithub.com\/zhdsmy\/branchline\/internal\/app\\t(cached)/' "$RUN_PASS" >"$RUN_CACHED"
assert_fail_contains "reject cached test result for ConfiguredLinux package" \
	"served from test cache '(cached)'" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_CACHED" --exit-code 0

# 11. Ordinary (non-ConfiguredLinux) test FAIL -> must FAIL.
RUN_ORDINARY_FAIL="${WORK_DIR}/run-ordinary-fail.json"
sed 's/"Action":"skip","Package":"github.com\/zhdsmy\/branchline\/internal\/git","Test":"TestOrdinaryLocalOptionalTool"/"Action":"fail","Package":"github.com\/zhdsmy\/branchline\/internal\/git","Test":"TestOrdinaryLocalOptionalTool"/' "$RUN_PASS" >"$RUN_ORDINARY_FAIL"
assert_fail_contains "reject ordinary test FAIL" \
	"FAIL detected in test: github.com/zhdsmy/branchline/internal/git TestOrdinaryLocalOptionalTool" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_ORDINARY_FAIL" --exit-code 0

# 12. Empty JSON file -> must FAIL.
RUN_EMPTY="${WORK_DIR}/run-empty.json"
: >"$RUN_EMPTY"
assert_fail_contains "reject empty JSON log" \
	"JSON file is empty" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_EMPTY" --exit-code 0

# 13. Malformed JSON line -> must FAIL.
RUN_MALFORMED="${WORK_DIR}/run-malformed.json"
cp "$RUN_PASS" "$RUN_MALFORMED"
printf '{"Time":"2026-11-06T12:00:03Z","Action":broken\n' >>"$RUN_MALFORMED"
assert_fail_contains "reject malformed JSON line" \
	"malformed JSON" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_MALFORMED" --exit-code 0

# 14. Truncated JSON file (unfinished package / missing final package pass) -> must FAIL.
RUN_TRUNCATED="${WORK_DIR}/run-truncated.json"
grep -v '"Action":"pass","Package":"github.com/zhdsmy/branchline/internal/repository","Elapsed"' "$RUN_PASS" >"$RUN_TRUNCATED"
assert_fail_contains "reject truncated JSON log with unfinished package" \
	"truncated JSON log: package github.com/zhdsmy/branchline/internal/repository started but never finished" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_TRUNCATED" --exit-code 0

# 15. Truncated JSON line (missing trailing newline) -> must FAIL.
RUN_NO_NL="${WORK_DIR}/run-no-nl.json"
cp "$RUN_PASS" "$RUN_NO_NL"
printf '{"Time":"2026-11-06T12:00:03.000000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline","Output":"incomplete"}' >>"$RUN_NO_NL"
assert_fail_contains "reject truncated JSON file missing trailing newline" \
	"last line does not end with newline" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_NO_NL" --exit-code 0

# 16. Non-zero command exit code with otherwise clean JSON -> must FAIL.
assert_fail_contains "reject non-zero go test exit code" \
	"go test command exited with non-zero status 2" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_PASS" --exit-code 2

# 17. Discovery with 0 TestConfiguredLinux* tests -> manifest generation must FAIL.
LIST_ZERO="${WORK_DIR}/list-zero.json"
cat >"$LIST_ZERO" <<'EOF'
{"Time":"2026-11-06T12:00:00.000000+08:00","Action":"start","Package":"github.com/zhdsmy/branchline"}
{"Time":"2026-11-06T12:00:00.020000+08:00","Action":"output","Package":"github.com/zhdsmy/branchline","Output":"ok  \tgithub.com/zhdsmy/branchline\t0.020s\n"}
{"Time":"2026-11-06T12:00:00.020000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Elapsed":0.02}
EOF
assert_fail_contains "reject manifest discovery when 0 TestConfiguredLinux* tests found" \
	"discovered 0 TestConfiguredLinux* tests" \
	python3 "$CHECKER" manifest --list-json "$LIST_ZERO" --list-exit-code 0 --output "${WORK_DIR}/manifest-zero.json"

# 18. Empty manifest / 0 tests in verify -> must FAIL.
MANIFEST_EMPTY_TESTS="${WORK_DIR}/manifest-empty-tests.json"
cat >"$MANIFEST_EMPTY_TESTS" <<'EOF'
{
  "schema_version": 1,
  "packages": [],
  "tests": []
}
EOF
assert_fail_contains "reject manifest with empty tests list" \
	"manifest 'tests' must be a non-empty list" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_EMPTY_TESTS" --json "$RUN_PASS" --exit-code 0

# 19. Manifest mismatch: unlisted TestConfiguredLinux* in JSON log -> must FAIL.
RUN_UNLISTED="${WORK_DIR}/run-unlisted.json"
cat >"$RUN_UNLISTED" <<'EOF'
{"Time":"2026-11-06T12:00:02.500000+08:00","Action":"run","Package":"github.com/zhdsmy/branchline","Test":"TestConfiguredLinuxBrandNewUnlisted"}
{"Time":"2026-11-06T12:00:02.510000+08:00","Action":"pass","Package":"github.com/zhdsmy/branchline","Test":"TestConfiguredLinuxBrandNewUnlisted","Elapsed":0.01}
EOF
cat "$RUN_PASS" "$RUN_UNLISTED" >"${WORK_DIR}/run-with-unlisted.json"
assert_fail_contains "reject unlisted TestConfiguredLinux* test not in manifest" \
	"unlisted TestConfiguredLinux test in JSON log not present in manifest: github.com/zhdsmy/branchline TestConfiguredLinuxBrandNewUnlisted" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "${WORK_DIR}/run-with-unlisted.json" --exit-code 0

# 20. Manifest mismatch: run_id or ssh_env mismatch -> must FAIL.
assert_fail_contains "reject manifest run_id mismatch" \
	"manifest run_id mismatch" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_PASS" --exit-code 0 --run-id "wrong-run-id"

# 21. Stale JSON log replay (event timestamps older than manifest created_at) -> must FAIL.
RUN_STALE="${WORK_DIR}/run-stale.json"
sed 's/2026-11-06T12:00:02/2026-11-05T12:00:02/g' "$RUN_PASS" >"$RUN_STALE"
assert_fail_contains "reject stale JSON log older than manifest created_at" \
	"stale JSON log: earliest event Time" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_STALE" --exit-code 0

# 22. Manifest tree/commit mismatch with current repository -> must FAIL.
REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd -P)
assert_fail_contains "reject manifest head_commit/tree_hash mismatch with live repository" \
	"manifest head_commit mismatch with current repository" \
	python3 "$CHECKER" verify --manifest "$MANIFEST_VALID" --json "$RUN_PASS" --exit-code 0 --repo-root "$REPO_ROOT"

# 23. regression.sh --verify-only entrypoint (PASS and FAIL without starting containers or running ./...)
assert_ok "regression.sh --verify-only PASS on valid manifest and JSON" \
	sh "${SCRIPT_DIR}/regression.sh" --verify-only --manifest "$MANIFEST_VALID" --json "$RUN_PASS" --exit-code 0

assert_fail_contains "regression.sh --verify-only FAIL on skipped TestConfiguredLinux test" \
	"SKIP detected in ConfiguredLinux test/subtest" \
	sh "${SCRIPT_DIR}/regression.sh" --verify-only --manifest "$MANIFEST_VALID" --json "$RUN_TOP_SKIP" --exit-code 0

assert_exit_code() {
	_desc=$1
	_want_rc=$2
	shift 2
	set +e
	"$@" >"${WORK_DIR}/last.out" 2>"${WORK_DIR}/last.err"
	_got_rc=$?
	set -e
	if [ "$_got_rc" -ne "$_want_rc" ]; then
		printf 'FAIL: expected exit %d for [%s], got %d\n' "$_want_rc" "$_desc" "$_got_rc" >&2
		cat "${WORK_DIR}/last.out" "${WORK_DIR}/last.err" >&2
		exit 1
	fi
	PASS_COUNT=$((PASS_COUNT + 1))
	printf 'ok %d - %s (exit=%d)\n' "$PASS_COUNT" "$_desc" "$_got_rc"
}

# 25-27. Extracted cleanup/trap block from regression.sh: `true; kill -TERM/-INT/-HUP $$` must exit 143/130/129.
EXTRACTED_TRAP="${WORK_DIR}/extracted-trap.sh"
sed -n '/^_instance_active=0$/,/^trap .* TERM$/p' "${SCRIPT_DIR}/regression.sh" >"$EXTRACTED_TRAP"

for _sig_pair in "TERM:143" "INT:130" "HUP:129"; do
	_sig_name=${_sig_pair%%:*}
	_sig_code=${_sig_pair#*:}
	assert_exit_code "extracted regression.sh trap preserves non-zero exit on SIG${_sig_name} after true" "$_sig_code" \
		python3 -c '
import signal, subprocess, sys
sig_name, extracted = sys.argv[1], sys.argv[2]
script = f"""
sshlab_log() {{ :; }}
SSHLAB_ENV=e1
SSHLAB_CONTAINER_NAME=bl-test-e1
SSHLAB_DOWN_SCRIPT=/usr/bin/true
. \x27{extracted}\x27
true
kill -{sig_name} $$
"""
proc = subprocess.run(
    ["sh", "-c", script],
    preexec_fn=lambda: (
        signal.signal(signal.SIGINT, signal.SIG_DFL),
        signal.signal(signal.SIGTERM, signal.SIG_DFL),
        signal.signal(signal.SIGHUP, signal.SIG_DFL),
    ),
)
sys.exit(proc.returncode)
' "$_sig_name" "$EXTRACTED_TRAP"
done

# 28-33. Controlled end-to-end stand-in verification of regression.sh (normal exit, command failure, INT/TERM/HUP with active child reaping, and down failure).
STANDIN_DIR="${WORK_DIR}/standin"
STANDIN_BIN="${STANDIN_DIR}/bin"
STANDIN_STATE="${STANDIN_DIR}/state"
mkdir -p "$STANDIN_BIN" "$STANDIN_STATE/e1/bin" "$STANDIN_STATE/e1/logs"

cat >"${STANDIN_STATE}/e1/bin/ssh" <<'EOF'
#!/bin/sh
printf 'Linux\ngit version 2.43.0\nmain\none\n'
EOF
chmod 755 "${STANDIN_STATE}/e1/bin/ssh"

cat >"${STANDIN_DIR}/up.sh" <<'EOF'
#!/bin/sh
printf 'up:%s\n' "$*" >>"${STANDIN_LOG}"
: >"${STANDIN_UP_CALLED}"
EOF
cat >"${STANDIN_DIR}/env.sh" <<EOF
#!/bin/sh
printf "export BRANCHLINE_LINUX_SSH_HOST='bl-standin-e1'\n"
printf "export BRANCHLINE_LINUX_SSH_REPO='/tmp/branchline-test-e1'\n"
printf "export BRANCHLINE_SSHLAB_ENV='e1'\n"
printf "export BRANCHLINE_SSHLAB_CONTAINER='bl-standin-e1'\n"
printf "export BRANCHLINE_SSHLAB_PORT='32768'\n"
printf "export PATH='%s:%s:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin'\n" "${STANDIN_STATE}/e1/bin" "${STANDIN_BIN}"
EOF
cat >"${STANDIN_DIR}/down.sh" <<'EOF'
#!/bin/sh
printf 'down:%s\n' "$*" >>"${STANDIN_LOG}"
if [ "${STANDIN_DOWN_FAIL:-0}" = "1" ] && [ -f "${STANDIN_UP_CALLED}" ]; then
	exit 9
fi
exit 0
EOF
chmod 755 "${STANDIN_DIR}/up.sh" "${STANDIN_DIR}/env.sh" "${STANDIN_DIR}/down.sh"

cat >"${STANDIN_BIN}/docker" <<'EOF'
#!/bin/sh
case "$1" in
	version)
		printf '27.0.0\n'
		;;
	ps)
		printf 'bl-standin-e1\n'
		;;
	exec)
		exit 0
		;;
	*)
		exit 0
		;;
esac
EOF
chmod 755 "${STANDIN_BIN}/docker"

cat >"${STANDIN_BIN}/go" <<'EOF'
#!/bin/sh
case "$1" in
	version)
		exec /opt/homebrew/bin/go version 2>/dev/null || exec /usr/local/bin/go version
		;;
	env)
		shift
		exec /opt/homebrew/bin/go env "$@" 2>/dev/null || exec /usr/local/bin/go env "$@"
		;;
	tool|vet)
		printf 'go_vet_args:%s\n' "$*" >>"${STANDIN_LOG}"
		exit 0
		;;
	test)
		printf 'go_test_args:%s\n' "$*" >>"${STANDIN_LOG}"
		printf 'require_linux:%s\n' "${BRANCHLINE_REQUIRE_LINUX:-}" >>"${STANDIN_LOG}"
		case "$*" in
			*"-list"*)
				NOW_TS=$(date '+%Y-%m-%dT%H:%M:%S.500000%z' | sed 's/\([0-9][0-9]\)$/:\1/')
				printf '{"Time":"%s","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}\n' "$NOW_TS"
				printf '{"Time":"%s","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"TestConfiguredLinuxSSHWorkflow\\n"}\n' "$NOW_TS"
				printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.01}\n' "$NOW_TS"
				exit 0
				;;
		esac
		if [ -n "${STANDIN_SIGNAL_MODE:-}" ]; then
			if [ "$STANDIN_SIGNAL_MODE" = "both_ignore_term" ]; then
				trap '' TERM
			else
				trap 'exit 143' TERM
			fi
			sh -c 'trap "" TERM; while :; do sleep 1; done' &
			_grandchild=$!
			printf '%s\n' "$$" >"${STANDIN_CHILD_PID_FILE}"
			printf '%s\n' "$_grandchild" >"${STANDIN_GRANDCHILD_PID_FILE}"
			if [ "$STANDIN_SIGNAL_MODE" = "both_ignore_term" ]; then
				while :; do sleep 1; done
			else
				wait "$_grandchild"
			fi
		fi
		sleep 1
		NOW_TS=$(date '+%Y-%m-%dT%H:%M:%S.900000%z' | sed 's/\([0-9][0-9]\)$/:\1/')
		printf '{"Time":"%s","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow","Elapsed":0.01}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"ok  \\tgithub.com/zhdsmy/branchline/internal/app\\t0.010s\\n"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.01}\n' "$NOW_TS"
		exit "${STANDIN_GO_TEST_EXIT:-0}"
		;;
esac
EOF
chmod 755 "${STANDIN_BIN}/go"

run_standin_regression() {
	STANDIN_LOG="${STANDIN_DIR}/events.log"
	STANDIN_UP_CALLED="${STANDIN_DIR}/up.called"
	rm -f "$STANDIN_LOG" "$STANDIN_UP_CALLED"
	STANDIN_LOG="$STANDIN_LOG" \
	STANDIN_UP_CALLED="$STANDIN_UP_CALLED" \
	BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
	BRANCHLINE_SSHLAB_STATE="$STANDIN_STATE" \
	BRANCHLINE_SSHLAB_UP_SCRIPT="${STANDIN_DIR}/up.sh" \
	BRANCHLINE_SSHLAB_ENV_SCRIPT="${STANDIN_DIR}/env.sh" \
	BRANCHLINE_SSHLAB_DOWN_SCRIPT="${STANDIN_DIR}/down.sh" \
	PATH="${STANDIN_BIN}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
	sh "${SCRIPT_DIR}/regression.sh" e1
}

# 28. Stand-in normal PASS: exit 0, runs `-count=1 ./internal/...` with BRANCHLINE_REQUIRE_LINUX=1, runs down.sh in cleanup.
assert_exit_code "stand-in regression.sh normal PASS exits 0 and cleans up" 0 run_standin_regression
grep -Fq "go_vet_args:vet ./internal/..." "${STANDIN_DIR}/events.log" || {
	printf 'FAIL: expected go vet ./internal/... in regression.sh\n' >&2
	cat "${STANDIN_DIR}/events.log" >&2
	exit 1
}
grep -Fq "go_test_args:test -json -list ^TestConfiguredLinux ./internal/..." "${STANDIN_DIR}/events.log" || {
	printf 'FAIL: expected ./internal/... discovery in regression.sh\n' >&2
	cat "${STANDIN_DIR}/events.log" >&2
	exit 1
}
grep -Fq "go_test_args:test -json -p 1 -count=1 -timeout=20m ./internal/..." "${STANDIN_DIR}/events.log" || {
	printf 'FAIL: expected -count=1 ./internal/... in go test args\n' >&2
	cat "${STANDIN_DIR}/events.log" >&2
	exit 1
}
grep -Fq "require_linux:1" "${STANDIN_DIR}/events.log" || {
	printf 'FAIL: expected BRANCHLINE_REQUIRE_LINUX=1 during regression.sh\n' >&2
	cat "${STANDIN_DIR}/events.log" >&2
	exit 1
}
[ "$(grep -c '^down:e1$' "${STANDIN_DIR}/events.log")" -eq 2 ] || {
	printf 'FAIL: expected down.sh to be called before up and on trap exit\n' >&2
	cat "${STANDIN_DIR}/events.log" >&2
	exit 1
}

# 28b. Stand-in test.sh e1: discovers ./internal/... and runs with BRANCHLINE_REQUIRE_LINUX=1 and zero-SKIP gate.
assert_exit_code "stand-in test.sh e1 discovers ./internal/... with BRANCHLINE_REQUIRE_LINUX=1 and passes gate" 0 \
	sh -c '
		STANDIN_LOG="'"${STANDIN_DIR}"'/events-test.log"
		rm -f "$STANDIN_LOG"
		STANDIN_LOG="$STANDIN_LOG" \
		BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
		BRANCHLINE_SSHLAB_STATE="'"${STANDIN_STATE}"'" \
		BRANCHLINE_SSHLAB_ENV_SCRIPT="'"${STANDIN_DIR}"'/env.sh" \
		PATH="'"${STANDIN_BIN}"':/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
		sh "'"${SCRIPT_DIR}"'/test.sh" e1
	'
grep -Fq "go_test_args:test -json -list ^TestConfiguredLinux ./internal/..." "${STANDIN_DIR}/events-test.log" || {
	printf 'FAIL: expected ./internal/... discovery in test.sh\n' >&2
	cat "${STANDIN_DIR}/events-test.log" >&2
	exit 1
}
grep -Fq "go_test_args:test -json -p 1 -race -count=1 -v -timeout=10m -run ^TestConfiguredLinuxSSHWorkflow$ github.com/zhdsmy/branchline/internal/app" "${STANDIN_DIR}/events-test.log" || {
	printf 'FAIL: expected targeted TestConfiguredLinux* run in test.sh\n' >&2
	cat "${STANDIN_DIR}/events-test.log" >&2
	exit 1
}
grep -Fq "require_linux:1" "${STANDIN_DIR}/events-test.log" || {
	printf 'FAIL: expected BRANCHLINE_REQUIRE_LINUX=1 during test.sh\n' >&2
	cat "${STANDIN_DIR}/events-test.log" >&2
	exit 1
}

# 29. Stand-in command failure: go test exits 7 -> regression.sh records _go_rc=7, runs gate, runs down.sh in cleanup, and exits 7.
assert_exit_code "stand-in regression.sh preserves command failure exit code 7, runs gate, and cleans up" 7 \
	env STANDIN_GO_TEST_EXIT=7 sh -c '
		STANDIN_LOG="'"${STANDIN_DIR}"'/events.log"
		STANDIN_UP_CALLED="'"${STANDIN_DIR}"'/up.called"
		rm -f "$STANDIN_LOG" "$STANDIN_UP_CALLED"
		STANDIN_LOG="$STANDIN_LOG" \
		STANDIN_UP_CALLED="$STANDIN_UP_CALLED" \
		BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
		BRANCHLINE_SSHLAB_STATE="'"${STANDIN_STATE}"'" \
		BRANCHLINE_SSHLAB_UP_SCRIPT="'"${STANDIN_DIR}"'/up.sh" \
		BRANCHLINE_SSHLAB_ENV_SCRIPT="'"${STANDIN_DIR}"'/env.sh" \
		BRANCHLINE_SSHLAB_DOWN_SCRIPT="'"${STANDIN_DIR}"'/down.sh" \
		PATH="'"${STANDIN_BIN}"':/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
		sh "'"${SCRIPT_DIR}"'/regression.sh" e1
	'
[ "$(grep -c '^down:e1$' "${STANDIN_DIR}/events.log")" -eq 2 ] || {
	printf 'FAIL: expected down.sh cleanup on command failure\n' >&2
	exit 1
}
[ -f "${STANDIN_STATE}/e1/logs/regression-e1-gate.json" ] || {
	printf 'FAIL: expected gate report JSON to be generated when go test exits 7\n' >&2
	exit 1
}
[ "$(jq -r '.command_exit_code' "${STANDIN_STATE}/e1/logs/regression-e1-gate.json")" = "7" ] || {
	printf 'FAIL: expected gate report command_exit_code=7\n' >&2
	cat "${STANDIN_STATE}/e1/logs/regression-e1-gate.json" >&2
	exit 1
}
grep -Fq "go test -json -p 1 -count=1 -timeout=20m ./internal/... (exit=7)" "${STANDIN_STATE}/e1/logs/regression-e1.log" || {
	printf 'FAIL: expected exit=7 recorded in regression-e1.log\n' >&2
	cat "${STANDIN_STATE}/e1/logs/regression-e1.log" >&2
	exit 1
}

# 30. Stand-in down.sh failure during cleanup -> regression.sh exits non-zero (1).
assert_exit_code "stand-in regression.sh exits 1 when down.sh fails during cleanup" 1 \
	env STANDIN_DOWN_FAIL=1 sh -c '
		STANDIN_LOG="'"${STANDIN_DIR}"'/events.log"
		STANDIN_UP_CALLED="'"${STANDIN_DIR}"'/up.called"
		rm -f "$STANDIN_LOG" "$STANDIN_UP_CALLED"
		STANDIN_LOG="$STANDIN_LOG" \
		STANDIN_UP_CALLED="$STANDIN_UP_CALLED" \
		BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
		BRANCHLINE_SSHLAB_STATE="'"${STANDIN_STATE}"'" \
		BRANCHLINE_SSHLAB_UP_SCRIPT="'"${STANDIN_DIR}"'/up.sh" \
		BRANCHLINE_SSHLAB_ENV_SCRIPT="'"${STANDIN_DIR}"'/env.sh" \
		BRANCHLINE_SSHLAB_DOWN_SCRIPT="'"${STANDIN_DIR}"'/down.sh" \
		PATH="'"${STANDIN_BIN}"':/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
		sh "'"${SCRIPT_DIR}"'/regression.sh" e1
	'

# 31-34. Stand-in signal tests (TERM -> 143 with parent exiting & grandchild ignoring TERM; TERM -> 143 with both ignoring TERM; INT -> 130; HUP -> 129).
# Uses Python subprocess with SIGINT restored to SIG_DFL and a strict timeout so non-interactive shell background SIG_IGN cannot mask INT or hang.
for _sig_case in \
	"TERM:143:parent_exits_grandchild_ignores" \
	"TERM:143:both_ignore_term" \
	"INT:130:parent_exits_grandchild_ignores" \
	"HUP:129:parent_exits_grandchild_ignores"
do
	_sig_name=$(printf '%s' "$_sig_case" | awk -F: '{print $1}')
	_sig_code=$(printf '%s' "$_sig_case" | awk -F: '{print $2}')
	_sig_mode=$(printf '%s' "$_sig_case" | awk -F: '{print $3}')
	STANDIN_LOG="${STANDIN_DIR}/events.log"
	STANDIN_UP_CALLED="${STANDIN_DIR}/up.called"
	STANDIN_CHILD_PID_FILE="${STANDIN_DIR}/child.pid"
	STANDIN_GRANDCHILD_PID_FILE="${STANDIN_DIR}/grandchild.pid"
	rm -f "$STANDIN_LOG" "$STANDIN_UP_CALLED" "$STANDIN_CHILD_PID_FILE" "$STANDIN_GRANDCHILD_PID_FILE"

	_reg_rc=$(
		STANDIN_SIGNAL_MODE="$_sig_mode" \
		STANDIN_LOG="$STANDIN_LOG" \
		STANDIN_UP_CALLED="$STANDIN_UP_CALLED" \
		STANDIN_CHILD_PID_FILE="$STANDIN_CHILD_PID_FILE" \
		STANDIN_GRANDCHILD_PID_FILE="$STANDIN_GRANDCHILD_PID_FILE" \
		BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
		BRANCHLINE_SSHLAB_STATE="$STANDIN_STATE" \
		BRANCHLINE_SSHLAB_UP_SCRIPT="${STANDIN_DIR}/up.sh" \
		BRANCHLINE_SSHLAB_ENV_SCRIPT="${STANDIN_DIR}/env.sh" \
		BRANCHLINE_SSHLAB_DOWN_SCRIPT="${STANDIN_DIR}/down.sh" \
		PATH="${STANDIN_BIN}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
		python3 -c '
import os, signal, subprocess, sys, time

sig_name = sys.argv[1]
reg_script = sys.argv[2]
grandchild_pid_file = sys.argv[3]
out_path = sys.argv[4]
err_path = sys.argv[5]
sig_num = getattr(signal, "SIG" + sig_name)

with open(out_path, "wb") as out_f, open(err_path, "wb") as err_f:
    proc = subprocess.Popen(
        ["sh", reg_script, "e1"],
        stdout=out_f,
        stderr=err_f,
        preexec_fn=lambda: (
            signal.signal(signal.SIGINT, signal.SIG_DFL),
            signal.signal(signal.SIGTERM, signal.SIG_DFL),
            signal.signal(signal.SIGHUP, signal.SIG_DFL),
        ),
    )
    deadline = time.time() + 5.0
    while time.time() < deadline:
        if os.path.isfile(grandchild_pid_file) and os.path.getsize(grandchild_pid_file) > 0:
            break
        time.sleep(0.05)
    else:
        proc.kill()
        sys.exit("TIMEOUT_WAITING_FOR_CHILD")

    time.sleep(0.1)
    proc.send_signal(sig_num)
    try:
        rc = proc.wait(timeout=8.0)
    except subprocess.TimeoutExpired:
        proc.kill()
        sys.exit("TIMEOUT_WAITING_FOR_REGRESSION_EXIT")
    print(rc)
' "$_sig_name" "${SCRIPT_DIR}/regression.sh" "$STANDIN_GRANDCHILD_PID_FILE" "${WORK_DIR}/sig.out" "${WORK_DIR}/sig.err"
	)

	if [ "$_reg_rc" != "$_sig_code" ]; then
		printf 'FAIL: expected regression.sh exit %s on SIG%s (%s), got %s\n' "$_sig_code" "$_sig_name" "$_sig_mode" "$_reg_rc" >&2
		cat "${WORK_DIR}/sig.out" "${WORK_DIR}/sig.err" >&2
		exit 1
	fi
	_child_pid=$(cat "$STANDIN_CHILD_PID_FILE")
	_grandchild_pid=$(cat "$STANDIN_GRANDCHILD_PID_FILE")
	if kill -0 "$_child_pid" 2>/dev/null || kill -0 "$_grandchild_pid" 2>/dev/null; then
		kill -KILL "$_child_pid" "$_grandchild_pid" 2>/dev/null || true
		printf 'FAIL: active child (%s) or grandchild (%s) survived after SIG%s (%s) cleanup\n' "$_child_pid" "$_grandchild_pid" "$_sig_name" "$_sig_mode" >&2
		exit 1
	fi
	if [ "$(grep -c '^down:e1$' "$STANDIN_LOG")" -ne 2 ]; then
		printf 'FAIL: expected down.sh cleanup on SIG%s (%s)\n' "$_sig_name" "$_sig_mode" >&2
		cat "$STANDIN_LOG" >&2
		exit 1
	fi
	PASS_COUNT=$((PASS_COUNT + 1))
	printf 'ok %d - stand-in regression.sh SIG%s (%s) exits %s, reaps child+reparented grandchild, and runs down.sh\n' "$PASS_COUNT" "$_sig_name" "$_sig_mode" "$_reg_rc"
done

printf 'test-check-results: PASS (%d/%d assertions passed)\n' "$PASS_COUNT" "$PASS_COUNT"
