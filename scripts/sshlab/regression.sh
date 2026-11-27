#!/bin/sh
# Full SSH regression & dynamic TestConfiguredLinux* zero-SKIP gate for Branchline v2.
#
# Performs:
#   1. Clean disposable sshlab container setup (with unified trap teardown of container/keys/config)
#   2. Environment export (including BRANCHLINE_REQUIRE_LINUX=1) + published-port SSH preflight
#   3. `go vet ./internal/...` (or BRANCHLINE_SSHLAB_PACKAGES)
#   4. Dynamic `TestConfiguredLinux*` discovery (`go test -json -list '^TestConfiguredLinux' ./internal/...`) -> manifest
#   5. Exactly one full uncached run (`go test -json -p 1 -count=1 -timeout=20m ./internal/...`)
#   6. Dynamic zero-SKIP / zero-FAIL result gate (`scripts/sshlab/check-results.py verify`)
#
# Preserves JSON logs, manifest, gate report, and human-readable summary in ${SSHLAB_LOG_DIR}.
#
# Usage:
#   sh scripts/sshlab/regression.sh [e1|e2|e3] [--rebuild-image]
#   sh scripts/sshlab/regression.sh --verify-only --manifest <manifest.json> --json <results.json> [--exit-code <rc>] [--repo-root <dir>]
set -eu

case "${BRANCHLINE_SSHLAB_PREFIX:-}" in
	BL-*)
		BRANCHLINE_SSHLAB_PREFIX="bl-${BRANCHLINE_SSHLAB_PREFIX#BL-}"
		export BRANCHLINE_SSHLAB_PREFIX
		;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/../.." && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

SSHLAB_UP_SCRIPT=${BRANCHLINE_SSHLAB_UP_SCRIPT:-${SCRIPT_DIR}/up.sh}
SSHLAB_ENV_SCRIPT=${BRANCHLINE_SSHLAB_ENV_SCRIPT:-${SCRIPT_DIR}/env.sh}
SSHLAB_DOWN_SCRIPT=${BRANCHLINE_SSHLAB_DOWN_SCRIPT:-${SCRIPT_DIR}/down.sh}

ENV_ARG="e1"
REBUILD_IMAGE=0
VERIFY_ONLY=0
VERIFY_MANIFEST=""
VERIFY_JSON=""
VERIFY_EXIT_CODE=0
VERIFY_REPO_ROOT=""

while [ $# -gt 0 ]; do
	case "$1" in
		--rebuild-image)
			REBUILD_IMAGE=1
			shift
			;;
		--verify-only)
			VERIFY_ONLY=1
			shift
			;;
		--manifest)
			[ $# -ge 2 ] || sshlab_die "--manifest requires a file path"
			VERIFY_MANIFEST=$2
			shift 2
			;;
		--json)
			[ $# -ge 2 ] || sshlab_die "--json requires a file path"
			VERIFY_JSON=$2
			shift 2
			;;
		--exit-code)
			[ $# -ge 2 ] || sshlab_die "--exit-code requires a numeric value"
			VERIFY_EXIT_CODE=$2
			shift 2
			;;
		--repo-root)
			[ $# -ge 2 ] || sshlab_die "--repo-root requires a directory path"
			VERIFY_REPO_ROOT=$2
			shift 2
			;;
		-h|--help)
			cat >&2 <<EOF
usage:
  $0 [e1|e2|e3] [--rebuild-image]
  $0 --verify-only --manifest <manifest.json> --json <results.json> [--exit-code <rc>] [--repo-root <dir>]
EOF
			exit 0
			;;
		*)
			ENV_ARG=$1
			shift
			;;
	esac
done

if [ "$VERIFY_ONLY" = "1" ]; then
	sshlab_require_cmd python3
	[ -n "$VERIFY_MANIFEST" ] || sshlab_die "--verify-only requires --manifest <manifest.json>"
	[ -n "$VERIFY_JSON" ] || sshlab_die "--verify-only requires --json <results.json>"
	set -- python3 "${SCRIPT_DIR}/check-results.py" verify \
		--manifest "$VERIFY_MANIFEST" \
		--json "$VERIFY_JSON" \
		--exit-code "$VERIFY_EXIT_CODE"
	if [ -n "$VERIFY_REPO_ROOT" ]; then
		set -- "$@" --repo-root "$VERIFY_REPO_ROOT"
	fi
	exec "$@"
fi

sshlab_require_cmd docker go git jq python3 ssh ssh-keygen awk sed grep tr wc shasum date pgrep
git -C "$REPO_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1 || sshlab_die "not inside a Git worktree: $REPO_ROOT"

sshlab_resolve_env "$ENV_ARG"

# Snapshot user ~/.ssh state before starting to guarantee zero side effects.
_user_ssh_dir="${HOME}/.ssh"
_before_cfg_hash="missing"
_before_kh_hash="missing"
if [ -f "${_user_ssh_dir}/config" ]; then
	_before_cfg_hash=$(shasum -a 256 "${_user_ssh_dir}/config" | awk '{print $1}')
fi
if [ -f "${_user_ssh_dir}/known_hosts" ]; then
	_before_kh_hash=$(shasum -a 256 "${_user_ssh_dir}/known_hosts" | awk '{print $1}')
fi

mkdir -p "$SSHLAB_LOG_DIR"
MANIFEST_FILE="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-manifest.json"
LIST_JSON="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-list.json"
LIST_ERR="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-list.stderr"
VET_LOG="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-vet.log"
JSON_LOG="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}.json"
ERR_LOG="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}.stderr"
GATE_JSON="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-gate.json"
TEXT_LOG="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}.log"
DIAG_LOG="${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-diag.log"
rm -f "$MANIFEST_FILE" "$LIST_JSON" "$LIST_ERR" "$VET_LOG" "$JSON_LOG" "$ERR_LOG" "$GATE_JSON" "$TEXT_LOG" "$DIAG_LOG"

RUN_ID="regression-${SSHLAB_ENV}-$(date -u '+%Y%m%dT%H%M%SZ')-$$"
START_TIME=$(date '+%Y-%m-%dT%H:%M:%S%z')
HEAD_COMMIT=$(git -C "$REPO_ROOT" rev-parse HEAD)
TREE_HASH=$(git -C "$REPO_ROOT" rev-parse 'HEAD^{tree}')
BRANCH_NAME=$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)
STATUS_PORCELAIN=$(git -C "$REPO_ROOT" status --porcelain=v1)
if [ -n "$STATUS_PORCELAIN" ]; then
	DIRTY_STATE="dirty"
else
	DIRTY_STATE="clean"
fi
GO_VERSION=$(go version)
LOCAL_GIT_VERSION=$(git --version)

log_both() {
	printf '%s\n' "$*" >>"$TEXT_LOG"
	sshlab_log "$*"
}

_instance_active=0
_signal_rc=0
_active_child_pid=""

collect_process_tree() {
	[ -n "${1:-}" ] || return 0
	for _child_pid in $(pgrep -P "$1" 2>/dev/null || true); do
		(collect_process_tree "$_child_pid")
	done
	printf '%s\n' "$1"
}

any_pid_alive() {
	for _chk_pid in "$@"; do
		[ -n "$_chk_pid" ] || continue
		if kill -0 "$_chk_pid" 2>/dev/null; then
			return 0
		fi
	done
	return 1
}

terminate_active_child() {
	if [ -n "${_active_child_pid:-}" ]; then
		_tracked_pids=$(collect_process_tree "$_active_child_pid" | tr '\n' ' ')
		if [ -n "$_tracked_pids" ]; then
			# shellcheck disable=SC2086
			if any_pid_alive $_tracked_pids; then
				# shellcheck disable=SC2086
				kill -TERM $_tracked_pids 2>/dev/null || true
				_wait_ticks=0
				# shellcheck disable=SC2086
				while any_pid_alive $_tracked_pids && [ "$_wait_ticks" -lt 10 ]; do
					sleep 0.05
					for _tp in $_tracked_pids; do
						_extra=$(collect_process_tree "$_tp" | tr '\n' ' ')
						for _ep in $_extra; do
							case " ${_tracked_pids} " in
								*" ${_ep} "*) ;;
								*) _tracked_pids="${_tracked_pids} ${_ep}" ;;
							esac
						done
					done
					_wait_ticks=$((_wait_ticks + 1))
				done
				# shellcheck disable=SC2086
				if any_pid_alive $_tracked_pids; then
					# shellcheck disable=SC2086
					kill -KILL $_tracked_pids 2>/dev/null || true
					_kill_ticks=0
					# shellcheck disable=SC2086
					while any_pid_alive $_tracked_pids && [ "$_kill_ticks" -lt 10 ]; do
						sleep 0.02
						_kill_ticks=$((_kill_ticks + 1))
					done
				fi
			fi
		fi
		wait "$_active_child_pid" 2>/dev/null || true
		_active_child_pid=""
	fi
}

cleanup_regression() {
	_exit_rc=$?
	trap - EXIT INT TERM HUP
	if [ "${_signal_rc:-0}" -ne 0 ]; then
		_exit_rc=$_signal_rc
	fi
	terminate_active_child
	if [ "${_instance_active:-0}" = "1" ]; then
		sshlab_log "cleaning up disposable ${SSHLAB_ENV} instance (${SSHLAB_CONTAINER_NAME})..."
		if ! sh "${SSHLAB_DOWN_SCRIPT:-${SCRIPT_DIR}/down.sh}" "$SSHLAB_ENV"; then
			sshlab_log "ERROR: failed to tear down ${SSHLAB_ENV} during cleanup"
			if [ "$_exit_rc" -eq 0 ]; then
				_exit_rc=1
			fi
		fi
	fi
	if [ -n "${TEXT_LOG:-}" ] && [ -f "$TEXT_LOG" ]; then
		printf 'end_time=%s final_exit_code=%d\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$_exit_rc" >>"$TEXT_LOG"
	fi
	exit "$_exit_rc"
}

on_regression_signal() {
	_signal_rc=$1
	exit "$_signal_rc"
}

trap cleanup_regression EXIT
trap 'on_regression_signal 129' HUP
trap 'on_regression_signal 130' INT
trap 'on_regression_signal 143' TERM

run_in_repo_to_files() {
	_out_file=$1
	_err_file=$2
	shift 2
	(
		cd "$REPO_ROOT"
		exec "$@"
	) >"$_out_file" 2>"$_err_file" &
	_active_child_pid=$!
	if wait "$_active_child_pid"; then
		_cmd_rc=0
	else
		_cmd_rc=$?
	fi
	_active_child_pid=""
	return "$_cmd_rc"
}

cat >"$TEXT_LOG" <<EOF
=== Branchline Daily SSH Regression & Dynamic Zero-SKIP Gate ===
run_id=${RUN_ID}
start_time=${START_TIME}
repo_root=${REPO_ROOT}
branch=${BRANCH_NAME}
head_commit=${HEAD_COMMIT}
tree_hash=${TREE_HASH}
worktree_state=${DIRTY_STATE}
go_version=${GO_VERSION}
local_git_version=${LOCAL_GIT_VERSION}
sshlab_env=${SSHLAB_ENV}
sshlab_prefix=${SSHLAB_PREFIX}
container_name=${SSHLAB_CONTAINER_NAME}
state_dir=${SSHLAB_STATE_DIR}
EOF
if [ -n "$STATUS_PORCELAIN" ]; then
	printf -- '--- git status --porcelain ---\n%s\n' "$STATUS_PORCELAIN" >>"$TEXT_LOG"
fi

# 1. Clean disposable container creation (tear down any prior instance with same name first).
_instance_active=1
sh "$SSHLAB_DOWN_SCRIPT" "$SSHLAB_ENV"
if [ "$REBUILD_IMAGE" = "1" ]; then
	sh "$SSHLAB_UP_SCRIPT" "$SSHLAB_ENV" --rebuild-image
else
	sh "$SSHLAB_UP_SCRIPT" "$SSHLAB_ENV"
fi

# 2. Export environment and run strict non-loopback SSH preflight.
_exports=$(sh "$SSHLAB_ENV_SCRIPT" "$SSHLAB_ENV")
eval "$_exports"
export BRANCHLINE_REQUIRE_LINUX=1

[ -n "${BRANCHLINE_LINUX_SSH_HOST:-}" ] || sshlab_die "BRANCHLINE_LINUX_SSH_HOST is empty"
[ -n "${BRANCHLINE_LINUX_SSH_REPO:-}" ] || sshlab_die "BRANCHLINE_LINUX_SSH_REPO is empty"
[ -n "${BRANCHLINE_SSHLAB_PORT:-}" ] || sshlab_die "BRANCHLINE_SSHLAB_PORT is empty"
sshlab_verify_published_port "$BRANCHLINE_SSHLAB_PORT"
sshlab_container_is_running "$SSHLAB_CONTAINER_NAME" || sshlab_die "container ${SSHLAB_CONTAINER_NAME} is not running"

_resolved_ssh=$(command -v ssh || true)
[ "$_resolved_ssh" = "$SSHLAB_SSH_WRAPPER" ] || sshlab_die "PATH ssh resolved to '$_resolved_ssh', expected wrapper '$SSHLAB_SSH_WRAPPER'"

sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$BRANCHLINE_LINUX_SSH_REPO"

_preflight=$(ssh "$BRANCHLINE_LINUX_SSH_HOST" "printf '%s\n' \"\$(uname -s)\" \"\$(git --version)\" \"\$(git -C $(sshlab_shell_quote "$BRANCHLINE_LINUX_SSH_REPO") rev-parse --abbrev-ref HEAD)\" \"\$(cat $(sshlab_shell_quote "${BRANCHLINE_LINUX_SSH_REPO}/a.txt"))\"")
_pf_os=$(printf '%s\n' "$_preflight" | awk 'NR==1 {print}')
_pf_git=$(printf '%s\n' "$_preflight" | awk 'NR==2 {print}')
_pf_branch=$(printf '%s\n' "$_preflight" | awk 'NR==3 {print}')
_pf_content=$(printf '%s\n' "$_preflight" | awk 'NR==4 {print}')

[ "$_pf_os" = "Linux" ] || sshlab_die "remote OS is '$_pf_os', expected 'Linux'"
[ "$_pf_branch" = "main" ] || sshlab_die "remote fixture branch is '$_pf_branch', expected 'main'"
[ "$_pf_content" = "one" ] || sshlab_die "remote fixture a.txt is '$_pf_content', expected 'one'"

cat >>"$TEXT_LOG" <<EOF
ssh_host=${BRANCHLINE_LINUX_SSH_HOST}
published_port=${BRANCHLINE_SSHLAB_PORT}
ssh_repo=${BRANCHLINE_LINUX_SSH_REPO}
remote_os=${_pf_os}
remote_git_version=${_pf_git}
EOF

# 3. Run `go vet` over target packages (default: ./internal/...)
log_both "step 1/4: running 'go vet ${SSHLAB_TEST_PACKAGES}'..."
# shellcheck disable=SC2086
if run_in_repo_to_files "$VET_LOG" "${VET_LOG}.err" go vet $SSHLAB_TEST_PACKAGES; then
	_vet_rc=0
else
	_vet_rc=$?
fi
if [ -s "${VET_LOG}.err" ]; then
	cat "${VET_LOG}.err" >>"$VET_LOG"
fi
rm -f "${VET_LOG}.err"
printf -- '--- go vet %s (exit=%d) ---\n' "$SSHLAB_TEST_PACKAGES" "$_vet_rc" >>"$TEXT_LOG"
if [ -s "$VET_LOG" ]; then
	cat "$VET_LOG" >>"$TEXT_LOG"
	cat "$VET_LOG" >&2
fi
if [ "$_vet_rc" -ne 0 ]; then
	log_both "FAIL: 'go vet ${SSHLAB_TEST_PACKAGES}' exited with code $_vet_rc (see $TEXT_LOG)"
	exit "$_vet_rc"
fi

# 4. Dynamic discovery of all TestConfiguredLinux* tests across target packages
log_both "step 2/4: discovering TestConfiguredLinux* tests across ${SSHLAB_TEST_PACKAGES}"
# shellcheck disable=SC2086
if run_in_repo_to_files "$LIST_JSON" "$LIST_ERR" go test -json -list '^TestConfiguredLinux' $SSHLAB_TEST_PACKAGES; then
	_list_rc=0
else
	_list_rc=$?
fi
if [ -s "$LIST_ERR" ]; then
	cat "$LIST_ERR" >>"$TEXT_LOG"
	cat "$LIST_ERR" >&2
fi

if python3 "${SCRIPT_DIR}/check-results.py" manifest \
	--list-json "$LIST_JSON" \
	--list-exit-code "$_list_rc" \
	--output "$MANIFEST_FILE" \
	--run-id "$RUN_ID" \
	--repo-root "$REPO_ROOT" \
	--ssh-env "$SSHLAB_ENV" \
	--ssh-host "$BRANCHLINE_LINUX_SSH_HOST" \
	--ssh-repo "$BRANCHLINE_LINUX_SSH_REPO" \
	--remote-git-version "$_pf_git" >>"$TEXT_LOG" 2>&1; then
	_manifest_rc=0
else
	_manifest_rc=$?
fi
if [ "$_manifest_rc" -ne 0 ]; then
	cat "$TEXT_LOG" >&2
	if [ "$_list_rc" -ne 0 ]; then
		exit "$_list_rc"
	fi
	exit "$_manifest_rc"
fi

# 5. Reset remote fixture before the single uncached (-count=1) full regression run.
sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$BRANCHLINE_LINUX_SSH_REPO"

log_both "step 3/4: running single full regression 'go test -json -p 1 -count=1 -timeout=20m ${SSHLAB_TEST_PACKAGES}' on ${SSHLAB_ENV} (127.0.0.1:${BRANCHLINE_SSHLAB_PORT}, ${_pf_git})..."
# shellcheck disable=SC2086
if run_in_repo_to_files "$JSON_LOG" "$ERR_LOG" go test -json -p 1 -count=1 -timeout=20m $SSHLAB_TEST_PACKAGES; then
	_go_rc=0
else
	_go_rc=$?
fi

printf -- '--- go test -json -p 1 -count=1 -timeout=20m %s (exit=%d) ---\n' "$SSHLAB_TEST_PACKAGES" "$_go_rc" >>"$TEXT_LOG"
if [ -s "$JSON_LOG" ]; then
	jq -rj 'select(.Output != null) | .Output' "$JSON_LOG" >>"$TEXT_LOG" 2>/dev/null || true
fi
if [ -s "$ERR_LOG" ]; then
	printf -- '--- go test stderr ---\n' >>"$TEXT_LOG"
	cat "$ERR_LOG" >>"$TEXT_LOG"
	cat "$ERR_LOG" >&2
fi

# 6. Dynamic zero-SKIP / zero-FAIL result gate.
log_both "step 4/4: running ConfiguredLinux zero-SKIP result gate..."
if python3 "${SCRIPT_DIR}/check-results.py" verify \
	--manifest "$MANIFEST_FILE" \
	--json "$JSON_LOG" \
	--exit-code "$_go_rc" \
	--run-id "$RUN_ID" \
	--repo-root "$REPO_ROOT" \
	--ssh-env "$SSHLAB_ENV" \
	--ssh-host "$BRANCHLINE_LINUX_SSH_HOST" \
	--ssh-repo "$BRANCHLINE_LINUX_SSH_REPO" \
	--report-json "$GATE_JSON" >"${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-gate.msg" 2>&1; then
	_gate_rc=0
else
	_gate_rc=$?
fi

cat "${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-gate.msg" >>"$TEXT_LOG"
cat "${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-gate.msg" >&2
rm -f "${SSHLAB_LOG_DIR}/regression-${SSHLAB_ENV}-gate.msg"

if [ "$_go_rc" -ne 0 ] || [ "$_gate_rc" -ne 0 ]; then
	# Capture a bounded diagnostic snapshot BEFORE the EXIT trap tears the
	# container down. Fault-tolerant and never masking the non-zero exit.
	log_both "collecting failure diagnostics before container teardown..."
	sshlab_collect_diagnostics "$DIAG_LOG" "regression go_rc=${_go_rc} gate_rc=${_gate_rc}"
	log_both "failure diagnostics: ${DIAG_LOG}"
else
	# Reset remote fixture before trap tears down the container (success path only;
	# on failure the teardown follows immediately and the reset would only obscure state).
	sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$BRANCHLINE_LINUX_SSH_REPO" || true
fi

# Verify user ~/.ssh files were untouched.
_after_cfg_hash="missing"
_after_kh_hash="missing"
if [ -f "${_user_ssh_dir}/config" ]; then
	_after_cfg_hash=$(shasum -a 256 "${_user_ssh_dir}/config" | awk '{print $1}')
fi
if [ -f "${_user_ssh_dir}/known_hosts" ]; then
	_after_kh_hash=$(shasum -a 256 "${_user_ssh_dir}/known_hosts" | awk '{print $1}')
fi
[ "$_before_cfg_hash" = "$_after_cfg_hash" ] || sshlab_die "~/.ssh/config was modified during regression run!"
[ "$_before_kh_hash" = "$_after_kh_hash" ] || sshlab_die "~/.ssh/known_hosts was modified during regression run!"

log_both "Manifest: ${MANIFEST_FILE}"
log_both "JSON log: ${JSON_LOG}"
log_both "Gate report: ${GATE_JSON}"
log_both "Summary log: ${TEXT_LOG}"

if [ "$_go_rc" -ne 0 ]; then
	log_both "FAIL: go test exited with code $_go_rc"
	exit "$_go_rc"
fi
if [ "$_gate_rc" -ne 0 ]; then
	log_both "FAIL: result gate exited with code $_gate_rc"
	exit "$_gate_rc"
fi

log_both "PASS: daily full regression and ConfiguredLinux zero-SKIP gate succeeded on ${SSHLAB_ENV}"
