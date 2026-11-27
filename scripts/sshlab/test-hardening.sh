#!/bin/sh
# Offline self-test suite for the sshlab hardening helpers:
#   1. sshlab_control_path is byte-identical with the Go SSHExecutor.ControlPath()
#   2. sshlab_cleanup_control_sockets removes only the current host alias's sockets
#   3. up.sh and the reference Containerfiles carry the hardened sshd config
#   4. test.sh/regression.sh failure paths write bounded *-diag.log snapshots
#      before teardown and never mask non-zero exit codes
# Uses fake docker/go/ssh stand-ins in an isolated temp dir; never starts a
# real container and never touches real /tmp/branchline-* sockets (all cleanup
# tests use a synthetic host alias no real connection can share).
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd -P)

command -v python3 >/dev/null 2>&1 || {
	printf 'test-hardening: ERROR: python3 not found\n' >&2
	exit 1
}
command -v go >/dev/null 2>&1 || {
	printf 'test-hardening: ERROR: go not found\n' >&2
	exit 1
}

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/bl-hardening-test.XXXXXX")
GO_PROBE_DIR="${REPO_ROOT}/sshlabhardeningtmp"
cleanup() {
	rm -rf "$WORK_DIR"
	rm -rf "$GO_PROBE_DIR"
}
trap cleanup EXIT INT TERM HUP
rm -rf "$GO_PROBE_DIR"

PASS_COUNT=0

ok() {
	PASS_COUNT=$((PASS_COUNT + 1))
	printf 'ok %d - %s\n' "$PASS_COUNT" "$1"
}

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	exit 1
}

assert_file_contains() {
	_desc=$1
	_file=$2
	_needle=$3
	grep -qF -e "$_needle" "$_file" || fail "$_desc (missing '$_needle' in $_file)"
	ok "$_desc"
}

# ---------------------------------------------------------------------------
# 1. sshlab_control_path byte-identical with Go SSHExecutor.ControlPath().
#    Builds a tiny throwaway main package inside the module tree (required for
#    internal/ import visibility), runs it for several user/host/port/identity
#    tuples, and compares each output byte-for-byte with the shell helper.
# ---------------------------------------------------------------------------

mkdir -p "$GO_PROBE_DIR"
cat >"${GO_PROBE_DIR}/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/zhdsmy/branchline/internal/git"
)

func main() {
	port, err := strconv.Atoi(os.Args[3])
	if err != nil {
		panic(err)
	}
	fmt.Println((&git.SSHExecutor{User: os.Args[1], Host: os.Args[2], Port: port, Identity: os.Args[4]}).ControlPath())
}
EOF

compare_control_path() {
	_user=$1
	_host=$2
	_port=$3
	_identity=$4
	_go_path=$(cd "$REPO_ROOT" && go run ./sshlabhardeningtmp "$_user" "$_host" "$_port" "$_identity")
	_sh_path=$(
		. "${SCRIPT_DIR}/common.sh"
		sshlab_control_path "$_user" "$_host" "$_port" "$_identity"
	)
	if [ "$_go_path" != "$_sh_path" ]; then
		fail "sshlab_control_path drifted from Go ControlPath for user='$_user' host='$_host': go='$_go_path' sh='$_sh_path'"
	fi
}

compare_control_path "" "bl-sshlab-e1" 0 ""
compare_control_path "root" "bl-sshlab-e1" 0 ""
compare_control_path "" "bl-sshlab-e1" 0 "/tmp/bl-hardening/id_ed25519"
compare_control_path "root" "bl-sshlab-e1" 0 "/tmp/bl-hardening/id_ed25519"
compare_control_path "root" "bl-sshlab-e2" 0 ""
compare_control_path "" "bl-sshlab-e3" 22 "/tmp/bl-hardening/other_key"
ok "sshlab_control_path matches Go SSHExecutor.ControlPath byte-for-byte across 6 tuples"

# Source-anchor drift guards: if exec.go ever changes its hash recipe, these
# exact lines disappear and the byte comparison above is re-validated by hand.
assert_file_contains "exec.go still hashes user/host/port/identity with NUL separators" \
	"${REPO_ROOT}/internal/git/exec.go" 'fmt.Sprintf("%s\x00%s\x00%d\x00%s", s.User, s.Host, s.Port, s.Identity+strings.Join(s.SSHKeys'
assert_file_contains "exec.go still emits /tmp/branchline-<16 lowercase hex>" \
	"${REPO_ROOT}/internal/git/exec.go" '"/tmp/branchline-" + hex.EncodeToString(sum[:])[:16]'

# ---------------------------------------------------------------------------
# 2. sshlab_cleanup_control_sockets scope: removes exactly the sockets of the
#    given host alias (all four user x identity combos), leaves sockets of any
#    other alias and unrelated /tmp/branchline-* files untouched. Uses the
#    synthetic alias bl-hardening-nohost so no real ssh socket can collide.
# ---------------------------------------------------------------------------

TEST_ALIAS="bl-hardening-nohost"
TEST_KEY="/tmp/bl-hardening/id_ed25519"

_sockets_setup() {
	# Compute the four combo paths for the test alias inside a subshell.
	(
		. "${SCRIPT_DIR}/common.sh"
		sshlab_control_path "" "$TEST_ALIAS" 0 ""
		sshlab_control_path "root" "$TEST_ALIAS" 0 ""
		sshlab_control_path "" "$TEST_ALIAS" 0 "$TEST_KEY"
		sshlab_control_path "root" "$TEST_ALIAS" 0 "$TEST_KEY"
		sshlab_control_path "root" "bl-hardening-otherhost" 0 ""
	)
}

_path_list=$(_sockets_setup)
_p1=$(printf '%s\n' "$_path_list" | sed -n '1p')
_p2=$(printf '%s\n' "$_path_list" | sed -n '2p')
_p3=$(printf '%s\n' "$_path_list" | sed -n '3p')
_p4=$(printf '%s\n' "$_path_list" | sed -n '4p')
_p_other=$(printf '%s\n' "$_path_list" | sed -n '5p')
_unrelated="/tmp/branchline-0123456789abcdef"

# Pre-clean in case a previous interrupted run left these exact files behind.
rm -f "$_p1" "$_p2" "$_p3" "$_p4" "$_p_other" "$_unrelated"

touch "$_p1" "$_p2" "$_p3" "$_p4" "$_p_other" "$_unrelated"

(
	. "${SCRIPT_DIR}/common.sh"
	SSHLAB_HOST_ALIAS="$TEST_ALIAS"
	SSHLAB_REMOTE_USER="root"
	SSHLAB_KEY_FILE="$TEST_KEY"
	sshlab_cleanup_control_sockets
)

for _gone in "$_p1" "$_p2" "$_p3" "$_p4"; do
	[ ! -e "$_gone" ] || fail "cleanup did not remove stale socket $_gone for its own alias"
done
ok "cleanup removes all four stale socket combos of the current host alias"

[ -e "$_p_other" ] || fail "cleanup removed socket $_p_other belonging to a different host alias"
ok "cleanup leaves sockets of other host aliases untouched"

[ -e "$_unrelated" ] || fail "cleanup removed unrelated file $_unrelated"
ok "cleanup leaves unrelated /tmp/branchline-* files untouched"

rm -f "$_p1" "$_p2" "$_p3" "$_p4" "$_p_other" "$_unrelated"

# Empty-state cleanup must be a quiet no-op even when no sockets exist.
(
	. "${SCRIPT_DIR}/common.sh"
	SSHLAB_HOST_ALIAS="$TEST_ALIAS"
	SSHLAB_REMOTE_USER="root"
	SSHLAB_KEY_FILE="$TEST_KEY"
	sshlab_cleanup_control_sockets
) 2>"${WORK_DIR}/cleanup-empty.err" || fail "cleanup with no stale sockets exited non-zero"
ok "cleanup with no stale sockets is a no-op and exits zero"

# ---------------------------------------------------------------------------
# 3. Static hardening assertions on up.sh and the reference Containerfiles.
# ---------------------------------------------------------------------------

for _f in "${SCRIPT_DIR}/up.sh" "${SCRIPT_DIR}/Containerfile.e1" "${SCRIPT_DIR}/Containerfile.e2" "${SCRIPT_DIR}/Containerfile.e3"; do
	assert_file_contains "$(basename "$_f") sets MaxStartups 64:30:128" "$_f" "MaxStartups 64:30:128"
	assert_file_contains "$(basename "$_f") sets MaxSessions 64" "$_f" "MaxSessions 64"
	assert_file_contains "$(basename "$_f") keeps sshd log at /tmp/sshd.log" "$_f" "/tmp/sshd.log"
	assert_file_contains "$(basename "$_f") probes PerSourcePenalties support before writing it" "$_f" "sshd -T -o PerSourcePenalties=no"
done
assert_file_contains "up.sh sets LogLevel VERBOSE" "${SCRIPT_DIR}/up.sh" "LogLevel VERBOSE"
assert_file_contains "up.sh starts sshd with -E /tmp/sshd.log" "${SCRIPT_DIR}/up.sh" "sshd -E /tmp/sshd.log"
assert_file_contains "up.sh validates config with sshd -t" "${SCRIPT_DIR}/up.sh" "sshd -t"
assert_file_contains "up.sh cleans stale ControlMaster sockets ahead of container launch" "${SCRIPT_DIR}/up.sh" "sshlab_cleanup_control_sockets"
assert_file_contains "down.sh cleans stale ControlMaster sockets after teardown" "${SCRIPT_DIR}/down.sh" "sshlab_cleanup_control_sockets"

# ---------------------------------------------------------------------------
# 4. Failure-path stand-in matrix: fake docker/go/ssh so no real container
#    is started. Verifies *-diag.log is written before down.sh teardown, that
#    a hung docker exec cannot block cleanup, and that exit codes stay non-zero.
# ---------------------------------------------------------------------------

STANDIN_DIR="${WORK_DIR}/standin"
STANDIN_BIN="${STANDIN_DIR}/bin"
STANDIN_STATE="${STANDIN_DIR}/state"
mkdir -p "$STANDIN_BIN" "$STANDIN_STATE/e1/bin" "$STANDIN_STATE/e1/logs"

# Target-scoped ssh wrapper stand-in: answers the readiness probe shape and
# the preflight shape; everything else falls through to canned output.
cat >"${STANDIN_STATE}/e1/bin/ssh" <<'EOF'
#!/bin/sh
case "$*" in
	*log-ok*)
		printf 'Linux log-ok\n'
		exit 0
		;;
esac
printf 'Linux\ngit version 2.43.0\nmain\none\n'
EOF
chmod 755 "${STANDIN_STATE}/e1/bin/ssh"

cat >"${STANDIN_DIR}/up.sh" <<'EOF'
#!/bin/sh
printf 'up:%s\n' "$*" >>"${STANDIN_LOG}"
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

# down.sh stand-in records whether the diag log already exists at teardown
# time, proving the snapshot lands before the container is destroyed.
cat >"${STANDIN_DIR}/down.sh" <<'EOF'
#!/bin/sh
if [ -s "${STANDIN_STATE}/e1/logs/regression-e1-diag.log" ] || [ -s "${STANDIN_STATE}/e1/logs/configured-linux-e1-diag.log" ]; then
	printf 'down:%s:diag-present\n' "$*" >>"${STANDIN_LOG}"
else
	printf 'down:%s:diag-missing\n' "$*" >>"${STANDIN_LOG}"
fi
exit 0
EOF
chmod 755 "${STANDIN_DIR}/up.sh" "${STANDIN_DIR}/env.sh" "${STANDIN_DIR}/down.sh"

# docker stand-in. STUB_CONTAINER_MODE=normal|exec-timeout|exec-fail applies
# ONLY to the diagnostic exec (the one collecting ps/network/sshd.log, matched
# via its tail -n 200 probe) so the unbounded pre-test fixture resets keep
# behaving like a healthy container, exactly as in a real intermittent outage.
cat >"${STANDIN_BIN}/docker" <<'EOF'
#!/bin/sh
case "$1" in
	version)
		printf '27.0.0\n'
		;;
	ps)
		printf 'bl-standin-e1\n'
		;;
	port)
		printf '127.0.0.1:32768\n'
		;;
	inspect)
		printf '[{"Name":"/bl-standin-e1","State":{"Status":"running"}}]\n'
		;;
	exec)
		case "$*" in
			*"tail -n 200 /tmp/sshd.log"*)
				case "${STUB_CONTAINER_MODE:-normal}" in
					exec-timeout)
						sleep 3600
						;;
					exec-fail)
						printf 'stub docker exec failure\n' >&2
						exit 3
						;;
					*)
						printf 'STUB-PS-SSHD-RUNNING\n'
						printf 'STUB-SSHD-LOG-TAIL\n'
						printf 'STUB-IP-ADDR\n'
						;;
				esac
				;;
			*)
				exit 0
				;;
		esac
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
		exit 0
		;;
	test)
		case "$*" in
			*"-list"*)
				NOW_TS=$(date '+%Y-%m-%dT%H:%M:%S.500000%z' | sed 's/\([0-9][0-9]\)$/:\1/')
				printf '{"Time":"%s","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}\n' "$NOW_TS"
				printf '{"Time":"%s","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"TestConfiguredLinuxSSHWorkflow\\n"}\n' "$NOW_TS"
				printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.01}\n' "$NOW_TS"
				exit 0
				;;
		esac
		# +2s skew keeps every test event strictly newer than the manifest
		# created_at so the stale-log gate never fires on same-second timing.
		NOW_TS=$(date -v+2S '+%Y-%m-%dT%H:%M:%S.900000%z' | sed 's/\([0-9][0-9]\)$/:\1/')
		printf '{"Time":"%s","Action":"start","Package":"github.com/zhdsmy/branchline/internal/app"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"run","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Test":"TestConfiguredLinuxSSHWorkflow","Elapsed":0.01}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"output","Package":"github.com/zhdsmy/branchline/internal/app","Output":"ok  \\tgithub.com/zhdsmy/branchline/internal/app\\t0.010s\\n"}\n' "$NOW_TS"
		printf '{"Time":"%s","Action":"pass","Package":"github.com/zhdsmy/branchline/internal/app","Elapsed":0.01}\n' "$NOW_TS"
		exit "${STUB_GO_TEST_EXIT:-0}"
		;;
esac
EOF
chmod 755 "${STANDIN_BIN}/go"

run_standin_regression() {
	STANDIN_LOG="${STANDIN_DIR}/events.log"
	rm -f "$STANDIN_LOG"
	rm -f "${STANDIN_STATE}/e1/logs/regression-e1-diag.log"
	STANDIN_LOG="$STANDIN_LOG" \
	STANDIN_STATE="$STANDIN_STATE" \
	BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
	BRANCHLINE_SSHLAB_STATE="$STANDIN_STATE" \
	BRANCHLINE_SSHLAB_UP_SCRIPT="${STANDIN_DIR}/up.sh" \
	BRANCHLINE_SSHLAB_ENV_SCRIPT="${STANDIN_DIR}/env.sh" \
	BRANCHLINE_SSHLAB_DOWN_SCRIPT="${STANDIN_DIR}/down.sh" \
	STUB_CONTAINER_MODE="${STUB_CONTAINER_MODE:-normal}" \
	STUB_GO_TEST_EXIT="${STUB_GO_TEST_EXIT:-0}" \
	PATH="${STANDIN_BIN}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
	sh "${SCRIPT_DIR}/regression.sh" e1
}

run_standin_test() {
	STANDIN_LOG="${STANDIN_DIR}/events-test.log"
	rm -f "$STANDIN_LOG"
	rm -f "${STANDIN_STATE}/e1/logs/configured-linux-e1-diag.log"
	STANDIN_LOG="$STANDIN_LOG" \
	STANDIN_STATE="$STANDIN_STATE" \
	BRANCHLINE_SSHLAB_PREFIX="bl-standin" \
	BRANCHLINE_SSHLAB_STATE="$STANDIN_STATE" \
	BRANCHLINE_SSHLAB_ENV_SCRIPT="${STANDIN_DIR}/env.sh" \
	STUB_CONTAINER_MODE="${STUB_CONTAINER_MODE:-normal}" \
	STUB_GO_TEST_EXIT="${STUB_GO_TEST_EXIT:-0}" \
	PATH="${STANDIN_BIN}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" \
	sh "${SCRIPT_DIR}/test.sh" e1
}

DIAG_REGRESSION="${STANDIN_STATE}/e1/logs/regression-e1-diag.log"
DIAG_TEST="${STANDIN_STATE}/e1/logs/configured-linux-e1-diag.log"

# 4a. regression.sh success path: exit 0, and no diag log is produced.
set +e
STUB_GO_TEST_EXIT=0 STUB_CONTAINER_MODE=normal run_standin_regression \
	>"${WORK_DIR}/reg-pass.out" 2>"${WORK_DIR}/reg-pass.err"
_reg_pass_rc=$?
set -e
if [ "$_reg_pass_rc" -ne 0 ]; then
	cat "${WORK_DIR}/reg-pass.out" >&2 || true
	cat "${WORK_DIR}/reg-pass.err" >&2 || true
	fail "stand-in regression success path exited non-zero ($_reg_pass_rc)"
fi
ok "stand-in regression success path exits zero"
[ ! -e "$DIAG_REGRESSION" ] || fail "success path must not produce a diag log"
ok "success path produces no diag log"

# 4b. regression.sh go test failure (exit 7): exit code preserved at 7, diag
# written before down.sh teardown, all sections present.
set +e
STUB_GO_TEST_EXIT=7 STUB_CONTAINER_MODE=normal run_standin_regression \
	>"${WORK_DIR}/reg-fail.out" 2>"${WORK_DIR}/reg-fail.err"
_reg_rc=$?
set -e
[ "$_reg_rc" -eq 7 ] || fail "regression failure exit code masked or altered (got $_reg_rc, want 7)"
ok "regression failure preserves the exact non-zero exit code"

[ -s "$DIAG_REGRESSION" ] || fail "regression failure did not write a non-empty diag log"
ok "regression failure writes a non-empty diag log"

assert_file_contains "diag log records the failure context label" "$DIAG_REGRESSION" "regression go_rc=7 gate_rc="
assert_file_contains "diag log has docker inspect section" "$DIAG_REGRESSION" "--- docker inspect"
assert_file_contains "diag log has docker exec section" "$DIAG_REGRESSION" "--- docker exec:"
assert_file_contains "diag log captured ps output" "$DIAG_REGRESSION" "STUB-PS-SSHD-RUNNING"
assert_file_contains "diag log captured sshd log tail" "$DIAG_REGRESSION" "STUB-SSHD-LOG-TAIL"
assert_file_contains "diag log has ControlPath state section" "$DIAG_REGRESSION" "--- host ControlPath state"
assert_file_contains "diag log has ssh -vvv probe section" "$DIAG_REGRESSION" "--- ssh -vvv probe"

_last_down=$(grep '^down:' "${STANDIN_DIR}/events.log" | tail -n 1)
[ "$_last_down" = "down:e1:diag-present" ] || fail "diag log was not present before teardown down.sh (last down line: '$_last_down')"
ok "diag log exists before the teardown down.sh runs"

_first_down=$(grep '^down:' "${STANDIN_DIR}/events.log" | head -n 1)
[ "$_first_down" = "down:e1:diag-missing" ] || fail "unexpected diag presence at initial down.sh (first down line: '$_first_down')"
ok "initial down.sh correctly runs before any diag exists"

[ "$(grep -c '^down:' "${STANDIN_DIR}/events.log")" -eq 2 ] || fail "expected exactly 2 down.sh invocations in regression stand-in"
ok "teardown down.sh still runs after diagnostics collection"

grep -qF "failure diagnostics: $DIAG_REGRESSION" "${STANDIN_STATE}/e1/logs/regression-e1.log" \
	|| fail "regression summary log does not record the diag file path"
ok "regression summary log records the diag file path"

# 4c. regression.sh failure with a hung docker exec: diagnostics must stay
# bounded (TIMED OUT recorded), teardown must still happen, exit stays 7.
set +e
STUB_GO_TEST_EXIT=7 STUB_CONTAINER_MODE=exec-timeout run_standin_regression \
	>"${WORK_DIR}/reg-timeout.out" 2>"${WORK_DIR}/reg-timeout.err"
_reg_to_rc=$?
set -e
[ "$_reg_to_rc" -eq 7 ] || fail "exec-timeout regression exit code masked or altered (got $_reg_to_rc, want 7)"
ok "hung docker exec does not change the non-zero exit code"

[ -s "$DIAG_REGRESSION" ] || fail "exec-timeout regression did not write a diag log"
grep -qF "TIMED OUT after 10" "$DIAG_REGRESSION" \
	|| fail "diag log does not record the bounded timeout of docker exec"
ok "hung docker exec is killed by the bounded timeout and recorded in diag"

_last_down_to=$(grep '^down:' "${STANDIN_DIR}/events.log" | tail -n 1)
[ "$_last_down_to" = "down:e1:diag-present" ] || fail "teardown did not run after bounded diagnostics on hung exec"
ok "teardown down.sh completes even when docker exec hangs"

# 4d. test.sh per-test failure: non-zero exit preserved (never zero), diag
# written with all sections.
set +e
STUB_GO_TEST_EXIT=7 STUB_CONTAINER_MODE=normal run_standin_test \
	>"${WORK_DIR}/test-fail.out" 2>"${WORK_DIR}/test-fail.err"
_test_rc=$?
set -e
[ "$_test_rc" -ne 0 ] || fail "test.sh failure was masked with exit code 0"
ok "test.sh per-test failure exits non-zero"

[ -s "$DIAG_TEST" ] || fail "test.sh failure did not write a non-empty diag log"
ok "test.sh failure writes a non-empty diag log"

assert_file_contains "test diag log records the failing test context" "$DIAG_TEST" "test=TestConfiguredLinuxSSHWorkflow rc=7"
assert_file_contains "test diag log has docker inspect section" "$DIAG_TEST" "--- docker inspect"
assert_file_contains "test diag log captured sshd log tail" "$DIAG_TEST" "STUB-SSHD-LOG-TAIL"
assert_file_contains "test diag log has ssh -vvv probe section" "$DIAG_TEST" "--- ssh -vvv probe"

# 4e. test.sh failure with docker exec returning an error: diagnostics
# tolerate it, exit stays non-zero.
set +e
STUB_GO_TEST_EXIT=7 STUB_CONTAINER_MODE=exec-fail run_standin_test \
	>"${WORK_DIR}/test-execfail.out" 2>"${WORK_DIR}/test-execfail.err"
_test_ef_rc=$?
set -e
[ "$_test_ef_rc" -ne 0 ] || fail "test.sh exec-fail failure was masked with exit code 0"
[ -s "$DIAG_TEST" ] || fail "test.sh exec-fail run did not write a diag log"
grep -qF "stub docker exec failure" "$DIAG_TEST" \
	|| fail "diag log does not capture failing docker exec output"
ok "failing docker exec is captured fault-tolerantly and never masks the failure"

# 4f. test.sh success path produces no diag log.
STUB_GO_TEST_EXIT=0 STUB_CONTAINER_MODE=normal run_standin_test \
	>"${WORK_DIR}/test-pass.out" 2>"${WORK_DIR}/test-pass.err" \
	|| fail "stand-in test.sh success path exited non-zero"
[ ! -e "$DIAG_TEST" ] || fail "test.sh success path must not produce a diag log"
ok "test.sh success path exits zero and produces no diag log"

# ---------------------------------------------------------------------------
# 5. SSH wrapper scoping: the wrapper generated by sshlab_sync_ssh_state must
#    intercept ONLY the host alias (optionally root@<alias>); 127.0.0.1,
#    localhost and any <user>@127.0.0.1 must pass through to /usr/bin/ssh
#    untouched. Offline: the exact generator code is extracted from common.sh
#    and instantiated in a subshell against a synthetic ssh_config. Each probe
#    invocation is then traced with `sh -x`: the exec line of the generated
#    wrapper script itself records the exact command the wrapper finally
#    executes (`+ exec /usr/bin/ssh ...`), so the assertions read directly
#    whether the interception fired — no network, no container, and immune to
#    -F/Host-match config semantics. ssh exits 255 for unresolved hostnames,
#    which is fine because only the trace matters.
# ---------------------------------------------------------------------------

WRAP_STATE="${WORK_DIR}/wrapstate"
WRAP_BIN="${WRAP_STATE}/bin"
WRAP_CONFIG="${WRAP_STATE}/ssh_config"
WRAP_KNOWN_HOSTS="${WRAP_STATE}/known_hosts"
mkdir -p "$WRAP_BIN"
printf 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeHardeningTestKeyMaterialOnly0000000 bl-hardening-wraptest\n' >"$WRAP_KNOWN_HOSTS"
cat >"$WRAP_CONFIG" <<EOF
Host bl-hardening-wraptest
    HostName 127.0.0.1
    User root
    Port 32768
    HostKeyAlias bl-hardening-wraptest
    CheckHostIP no
    UserKnownHostsFile "${WRAP_KNOWN_HOSTS}"
    GlobalKnownHostsFile /dev/null
    StrictHostKeyChecking yes
EOF

# Extract the exact wrapper-generation code from common.sh (the cat redirect
# line, the heredoc body, its EOF terminator, and the chmod line) and run it
# the way sshlab_sync_ssh_state does. A drift in the generator breaks this test.
sed -n '/cat > "\$SSHLAB_SSH_WRAPPER" <<EOF$/,/chmod 755 "\$SSHLAB_SSH_WRAPPER"$/p' "${SCRIPT_DIR}/common.sh" >"${WORK_DIR}/wrapper-fragment.sh"
[ -s "${WORK_DIR}/wrapper-fragment.sh" ] || fail "could not extract wrapper generation heredoc from common.sh"
grep -qF 'exec /usr/bin/ssh -F' "${WORK_DIR}/wrapper-fragment.sh" \
	|| fail "extracted wrapper fragment does not contain the interception exec line"

WRAP_OUT=$(
	export SSHLAB_ENV=e1
	export SSHLAB_HOST_ALIAS=bl-hardening-wraptest
	export SSHLAB_REMOTE_USER=root
	export SSHLAB_SSH_CONFIG="$WRAP_CONFIG"
	export SSHLAB_SSH_WRAPPER="${WRAP_BIN}/ssh"
	mkdir -p "$WRAP_BIN"
	. "${SCRIPT_DIR}/common.sh"
	. "${WORK_DIR}/wrapper-fragment.sh"
	printf '%s\n' generated
)
[ "$WRAP_OUT" = "generated" ] || fail "wrapper fragment instantiation failed: $WRAP_OUT"
[ -x "${WRAP_BIN}/ssh" ] || fail "instantiated ssh wrapper is missing or not executable"
ok "ssh wrapper generator heredoc extracted from common.sh and instantiated offline"

# Probe helper: traces one wrapper invocation with sh -x and prints only the
# generated wrapper's own exec line — the exact final command the wrapper
# hands to the kernel. Passing -G with an isolated HOME keeps /usr/bin/ssh
# strictly offline (no TCP connection to 127.0.0.1:22, no host-key prompt on
# hosts with Remote Login enabled, and no ~/.ssh access). Only the FIRST exec
# line (the wrapper script's own exec) is authoritative.
wrap_exec_line() {
	HOME="$WORK_DIR" sh -x "${WRAP_BIN}/ssh" -G "$@" </dev/null 2>&1 >/dev/null | sed -n 's/^+ exec \(\/usr\/bin\/ssh .*\)$/\1/p' | head -n 1
}

assert_wrap_passthrough() {
	_desc=$1
	shift
	_line=$(wrap_exec_line "$@")
	[ -n "$_line" ] || fail "$_desc: wrapper produced no exec line in trace"
	case "$_line" in
		/usr/bin/ssh\ -F\ *|/usr/bin/ssh\ *-F\ "$WRAP_CONFIG"*)
			fail "$_desc: intercepted instead of passed through (final exec is '$_line')" ;;
	esac
	ok "$_desc"
}

assert_wrap_intercepted() {
	_desc=$1
	shift
	_line=$(wrap_exec_line "$@")
	[ -n "$_line" ] || fail "$_desc: wrapper produced no exec line in trace"
	case "$_line" in
		/usr/bin/ssh\ -F\ "$WRAP_CONFIG"\ *)
			;;
		*)
			fail "$_desc: not intercepted with -F ssh_config (final exec is '$_line')" ;;
	esac
	ok "$_desc"
}

assert_wrap_passthrough "ssh wrapper passes 127.0.0.1 through untouched" 127.0.0.1 echo hi
assert_wrap_passthrough "ssh wrapper passes localhost through untouched" localhost echo hi
assert_wrap_passthrough "ssh wrapper passes user@127.0.0.1 through untouched" user@127.0.0.1 echo hi
assert_wrap_passthrough "ssh wrapper passes root@127.0.0.1 through untouched" root@127.0.0.1 echo hi
assert_wrap_passthrough "ssh wrapper passes root@localhost through untouched" root@localhost echo hi
assert_wrap_intercepted "ssh wrapper intercepts the bare host alias" bl-hardening-wraptest echo hi
assert_wrap_intercepted "ssh wrapper intercepts root@<host-alias>" root@bl-hardening-wraptest echo hi

# An invocation with ssh's own options: the arguments must reach the real ssh
# verbatim (-p 2222 and the target unchanged) while the loopback target must
# still pass through without the -F interception.
_line=$(wrap_exec_line -p 2222 127.0.0.1 echo hi)
[ -n "$_line" ] || fail "ssh wrapper produced no exec line for '-p 2222 127.0.0.1'"
case "$_line" in
	/usr/bin/ssh\ -F\ *)
		fail "ssh wrapper intercepts '-p 2222 127.0.0.1' (final exec is '$_line')" ;;
esac
case "$_line" in
	*"-p 2222 127.0.0.1"*)
		;;
	*)
		fail "ssh wrapper does not forward '-p 2222 127.0.0.1' verbatim (final exec is '$_line')" ;;
esac
ok "ssh wrapper forwards -p 2222 verbatim and still passes 127.0.0.1 through"

printf 'test-hardening: PASS (%d/%d assertions passed)\n' "$PASS_COUNT" "$PASS_COUNT"
