#!/bin/sh
# Verify Branchline sshlab environment, dynamically discover all TestConfiguredLinux*
# packages/tests across ./internal/... (or BRANCHLINE_SSHLAB_PACKAGES), and run them
# with BRANCHLINE_REQUIRE_LINUX=1, -p 1 -race -count=1, and 0 SKIP / 0 FAIL.
# Preserves go test -json logs and manifest in ${SSHLAB_LOG_DIR} for inspection.
# Usage:
#   sh scripts/sshlab/test.sh [e1|e2|e3] [--print-env]
set -eu

case "${BRANCHLINE_SSHLAB_PREFIX:-}" in
	BL-*)
		BRANCHLINE_SSHLAB_PREFIX="bl-${BRANCHLINE_SSHLAB_PREFIX#BL-}"
		export BRANCHLINE_SSHLAB_PREFIX
		;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

sshlab_require_cmd docker go git jq python3 ssh awk grep tr wc shasum date

ENV_ARG="e1"
PRINT_ENV=0

for _arg in "$@"; do
	case "$_arg" in
		--print-env)
			PRINT_ENV=1
			;;
		-h|--help)
			printf 'usage: %s [e1|e2|e3] [--print-env]\n' "$0" >&2
			exit 0
			;;
		*)
			ENV_ARG="$_arg"
			;;
	esac
done

SSHLAB_ENV_SCRIPT=${BRANCHLINE_SSHLAB_ENV_SCRIPT:-${SCRIPT_DIR}/env.sh}

if [ "$PRINT_ENV" = "1" ]; then
	exec sh "$SSHLAB_ENV_SCRIPT" "$ENV_ARG"
fi

sshlab_resolve_env "$ENV_ARG"

# Load environment exports from env.sh.
_exports=$(sh "$SSHLAB_ENV_SCRIPT" "$SSHLAB_ENV")
eval "$_exports"
export BRANCHLINE_REQUIRE_LINUX=1

[ -n "${BRANCHLINE_LINUX_SSH_HOST:-}" ] || sshlab_die "BRANCHLINE_LINUX_SSH_HOST is empty"
[ -n "${BRANCHLINE_LINUX_SSH_REPO:-}" ] || sshlab_die "BRANCHLINE_LINUX_SSH_REPO is empty"
[ -n "${BRANCHLINE_SSHLAB_PORT:-}" ] || sshlab_die "BRANCHLINE_SSHLAB_PORT is empty"
sshlab_verify_published_port "$BRANCHLINE_SSHLAB_PORT"

_resolved_ssh=$(command -v ssh || true)
[ "$_resolved_ssh" = "$SSHLAB_SSH_WRAPPER" ] || sshlab_die "PATH ssh resolved to '$_resolved_ssh', expected wrapper '$SSHLAB_SSH_WRAPPER'"

# Snapshot user ~/.ssh state before running tests to ensure zero modification.
_user_ssh_dir="${HOME}/.ssh"
_before_cfg_hash="missing"
_before_kh_hash="missing"
if [ -f "${_user_ssh_dir}/config" ]; then
	_before_cfg_hash=$(shasum -a 256 "${_user_ssh_dir}/config" | awk '{print $1}')
fi
if [ -f "${_user_ssh_dir}/known_hosts" ]; then
	_before_kh_hash=$(shasum -a 256 "${_user_ssh_dir}/known_hosts" | awk '{print $1}')
fi

# Reset remote fixture repository before test execution so repeated runs are idempotent.
sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$BRANCHLINE_LINUX_SSH_REPO"

# Pre-flight SSH check via the PATH wrapper.
_preflight=$(ssh "$BRANCHLINE_LINUX_SSH_HOST" "printf '%s\n' \"\$(uname -s)\" \"\$(git --version)\" \"\$(git -C $(sshlab_shell_quote "$BRANCHLINE_LINUX_SSH_REPO") rev-parse --abbrev-ref HEAD)\" \"\$(cat $(sshlab_shell_quote "${BRANCHLINE_LINUX_SSH_REPO}/a.txt"))\"")
_pf_os=$(printf '%s\n' "$_preflight" | awk 'NR==1 {print}')
_pf_git=$(printf '%s\n' "$_preflight" | awk 'NR==2 {print}')
_pf_branch=$(printf '%s\n' "$_preflight" | awk 'NR==3 {print}')
_pf_content=$(printf '%s\n' "$_preflight" | awk 'NR==4 {print}')

[ "$_pf_os" = "Linux" ] || sshlab_die "remote OS is '$_pf_os', expected 'Linux'"
[ "$_pf_branch" = "main" ] || sshlab_die "remote fixture branch is '$_pf_branch', expected 'main'"
[ "$_pf_content" = "one" ] || sshlab_die "remote fixture a.txt is '$_pf_content', expected 'one'"

mkdir -p "$SSHLAB_LOG_DIR"
MANIFEST_FILE="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-manifest.json"
LIST_JSON="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-list.json"
LIST_ERR="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-list.stderr"
JSON_LOG="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}.json"
TEXT_LOG="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}.log"
ERR_LOG="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}.stderr"
GATE_JSON="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-gate.json"
DIAG_LOG="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-diag.log"
rm -f "$MANIFEST_FILE" "$LIST_JSON" "$LIST_ERR" "$JSON_LOG" "$TEXT_LOG" "$ERR_LOG" "$GATE_JSON" "$DIAG_LOG"

REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd -P)
RUN_ID="test-${SSHLAB_ENV}-$(date -u '+%Y%m%dT%H%M%SZ')-$$"

sshlab_log "discovering TestConfiguredLinux* tests across ${SSHLAB_TEST_PACKAGES}"
set +e
(
	cd "$REPO_ROOT"
	# shellcheck disable=SC2086
	go test -json -list '^TestConfiguredLinux' $SSHLAB_TEST_PACKAGES
) >"$LIST_JSON" 2>"$LIST_ERR"
_list_rc=$?
set -e
if [ -s "$LIST_ERR" ]; then
	cat "$LIST_ERR" >&2
fi

python3 "${SCRIPT_DIR}/check-results.py" manifest \
	--list-json "$LIST_JSON" \
	--list-exit-code "$_list_rc" \
	--output "$MANIFEST_FILE" \
	--run-id "$RUN_ID" \
	--repo-root "$REPO_ROOT" \
	--ssh-env "$SSHLAB_ENV" \
	--ssh-host "$BRANCHLINE_LINUX_SSH_HOST" \
	--ssh-repo "$BRANCHLINE_LINUX_SSH_REPO" \
	--remote-git-version "$_pf_git"

_pkgs=$(python3 "${SCRIPT_DIR}/check-results.py" packages --manifest "$MANIFEST_FILE" | tr '\n' ' ')
[ -n "$_pkgs" ] || sshlab_die "no packages returned from manifest $MANIFEST_FILE"

sshlab_log "running ConfiguredLinux tests on ${SSHLAB_ENV} (host=${BRANCHLINE_LINUX_SSH_HOST}, port=${BRANCHLINE_SSHLAB_PORT}, ${_pf_git}, BRANCHLINE_REQUIRE_LINUX=1, packages: ${_pkgs})..."

_go_rc=0
: >"$JSON_LOG"
: >"$ERR_LOG"
_run_id=0
_last_pkg=""
_last_json=""
_last_pkg_fail_json=""
_tests_tsv="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-tests.tsv"
jq -r '.tests[] | [.package, .test] | @tsv' "$MANIFEST_FILE" >"$_tests_tsv"
# Read the list on its own descriptor: a command in the body that reads
# standard input (the diagnostics ssh, a test binary) must not eat the
# remaining lines and silently skip the tests after a failure.
while IFS="$(printf '\t')" read -r _pkg _test <&3; do
	[ -n "$_pkg" ] || continue
	_run_id=$((_run_id + 1))
	_one_json="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-test-${_run_id}.json"
	_one_err="${SSHLAB_LOG_DIR}/configured-linux-${SSHLAB_ENV}-test-${_run_id}.stderr"
	set +e
	(
		cd "$REPO_ROOT"
		go test -json -p 1 -race -count=1 -v -timeout=10m -run "^${_test}$" "$_pkg"
	) >"$_one_json" 2>"$_one_err"
	_test_rc=$?
	set -e
	# Keep one package lifecycle while retaining every test event. This lets the
	# existing result gate validate the per-test runs without duplicate package starts.
	if [ "$_pkg" != "$_last_pkg" ]; then
		if [ -n "$_last_json" ]; then
			_done_json=${_last_pkg_fail_json:-$_last_json}
			jq -c 'select(.Test == null and (.Action == "pass" or .Action == "fail" or .Action == "skip"))' "$_done_json" >>"$JSON_LOG"
		fi
		jq -c 'select(.Test == null and .Action == "start")' "$_one_json" >>"$JSON_LOG"
		_last_pkg="$_pkg"
		_last_pkg_fail_json=""
	fi
	jq -c 'select(.Test != null or .Action == "build-fail")' "$_one_json" >>"$JSON_LOG"
	_last_json="$_one_json"
	if [ -s "$_one_err" ]; then
		cat "$_one_err" >>"$ERR_LOG"
	fi
	if [ "$_test_rc" -ne 0 ]; then
		_go_rc=$_test_rc
		_last_pkg_fail_json="$_one_json"
		# Capture a bounded diagnostic snapshot immediately on each failure,
		# while the container is still alive; never masks the failure itself.
		sshlab_collect_diagnostics "$DIAG_LOG" "test=${_test} rc=${_test_rc}"
	fi
done 3<"$_tests_tsv"

# The final per-test invocation (or failing run in the last package) supplies the package completion event.
if [ -n "$_last_json" ]; then
	_done_json=${_last_pkg_fail_json:-$_last_json}
	jq -c 'select(.Test == null and (.Action == "pass" or .Action == "fail" or .Action == "skip"))' "$_done_json" >>"$JSON_LOG"
fi

set -e

if [ -s "$JSON_LOG" ]; then
	jq -rj 'select(.Output != null) | .Output' "$JSON_LOG" >"$TEXT_LOG" 2>/dev/null || true
	cat "$TEXT_LOG"
fi
if [ -s "$ERR_LOG" ]; then
	cat "$ERR_LOG" >&2
fi

set +e
python3 "${SCRIPT_DIR}/check-results.py" verify \
	--manifest "$MANIFEST_FILE" \
	--json "$JSON_LOG" \
	--exit-code "$_go_rc" \
	--run-id "$RUN_ID" \
	--repo-root "$REPO_ROOT" \
	--ssh-env "$SSHLAB_ENV" \
	--ssh-host "$BRANCHLINE_LINUX_SSH_HOST" \
	--ssh-repo "$BRANCHLINE_LINUX_SSH_REPO" \
	--report-json "$GATE_JSON"
_gate_rc=$?
set -e

# Reset remote repository back to clean initial state after tests finish.
sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$BRANCHLINE_LINUX_SSH_REPO"

# Verify user ~/.ssh files were untouched.
_after_cfg_hash="missing"
_after_kh_hash="missing"
if [ -f "${_user_ssh_dir}/config" ]; then
	_after_cfg_hash=$(shasum -a 256 "${_user_ssh_dir}/config" | awk '{print $1}')
fi
if [ -f "${_user_ssh_dir}/known_hosts" ]; then
	_after_kh_hash=$(shasum -a 256 "${_user_ssh_dir}/known_hosts" | awk '{print $1}')
fi
[ "$_before_cfg_hash" = "$_after_cfg_hash" ] || sshlab_die "~/.ssh/config was modified during test run!"
[ "$_before_kh_hash" = "$_after_kh_hash" ] || sshlab_die "~/.ssh/known_hosts was modified during test run!"

if [ "$_go_rc" -ne 0 ]; then
	if [ -f "$DIAG_LOG" ]; then
		sshlab_log "failure diagnostics: $DIAG_LOG"
	fi
	sshlab_die "go test exited with code $_go_rc (see $JSON_LOG)"
fi
if [ "$_gate_rc" -ne 0 ]; then
	sshlab_die "ConfiguredLinux result gate failed with code $_gate_rc (see $JSON_LOG and $MANIFEST_FILE)"
fi

_test_count=$(jq -r '.tests | length' "$MANIFEST_FILE")
_pkg_count=$(jq -r '.packages | length' "$MANIFEST_FILE")
sshlab_log "PASS: ${_test_count}/${_test_count} TestConfiguredLinux* across ${_pkg_count} package(s) passed with 0 SKIP, 0 FAIL on ${SSHLAB_ENV} (127.0.0.1:${BRANCHLINE_SSHLAB_PORT})"
sshlab_log "Manifest: ${MANIFEST_FILE}"
sshlab_log "JSON log: ${JSON_LOG}"
sshlab_log "Gate report: ${GATE_JSON}"
sshlab_log "Text log: ${TEXT_LOG}"
sshlab_log "For daily full regression, run: sh scripts/sshlab/regression.sh ${SSHLAB_ENV}"
