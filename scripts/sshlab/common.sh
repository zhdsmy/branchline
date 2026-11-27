#!/bin/sh
# Shared helpers and configuration for Branchline sshlab scripts.
# Sourced by up.sh, down.sh, env.sh, test.sh, and fault.sh.
set -eu

sshlab_log() {
	printf 'sshlab: %s\n' "$*" >&2
}

sshlab_die() {
	printf 'sshlab: ERROR: %s\n' "$*" >&2
	exit 1
}

sshlab_shell_quote() {
	printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

sshlab_require_cmd() {
	for _cmd in "$@"; do
		command -v "$_cmd" >/dev/null 2>&1 || sshlab_die "required command not found: $_cmd"
	done
}

sshlab_host_arch() {
	_arch=$(uname -m)
	case "$_arch" in
		arm64|aarch64) printf 'arm64' ;;
		x86_64|amd64) printf 'amd64' ;;
		*) printf '%s' "$_arch" ;;
	esac
}

sshlab_resolve_env() {
	_raw_env=${1:-e1}
	case "$_raw_env" in
		e1|E1)
			SSHLAB_ENV="e1"
			SSHLAB_BASE_IMAGE="ubuntu:24.04"
			SSHLAB_DISTRO="debian"
			SSHLAB_EXPECT_GNU_STAT="1"
			SSHLAB_DESC="Ubuntu 24.04 (Git 2.43, GNU coreutils)"
			SSHLAB_MATRIX_NAME="bl-lab-ubuntu24"
			;;
		e2|E2)
			SSHLAB_ENV="e2"
			SSHLAB_BASE_IMAGE="ubuntu:20.04"
			SSHLAB_DISTRO="debian"
			SSHLAB_EXPECT_GNU_STAT="1"
			SSHLAB_DESC="Ubuntu 20.04 (Git 2.25 min-compat, GNU coreutils)"
			SSHLAB_MATRIX_NAME="bl-lab-ubuntu20"
			;;
		e3|E3)
			SSHLAB_ENV="e3"
			SSHLAB_BASE_IMAGE="alpine:latest"
			SSHLAB_DISTRO="alpine"
			SSHLAB_EXPECT_GNU_STAT="0"
			SSHLAB_DESC="Alpine Linux (BusyBox sh/coreutils)"
			SSHLAB_MATRIX_NAME="bl-lab-alpine"
			;;
		e4|E4)
			sshlab_die "refusing operation on '$_raw_env': sshlab only manages disposable local containers (e1, e2, e3), never external hosts"
			;;
		*)
			sshlab_die "unknown sshlab environment '$_raw_env' (expected: e1, e2, or e3)"
			;;
	esac

	SSHLAB_TEST_PACKAGES=${BRANCHLINE_SSHLAB_PACKAGES:-./internal/...}
	SSHLAB_PREFIX=${BRANCHLINE_SSHLAB_PREFIX:-bl-sshlab}
	case "$SSHLAB_PREFIX" in
		bl-*) ;;
		*) sshlab_die "BRANCHLINE_SSHLAB_PREFIX must start with 'bl-' (got '$SSHLAB_PREFIX')" ;;
	esac
	_prefix_tail=${SSHLAB_PREFIX#bl-}
	case "$_prefix_tail" in
		''|*[!A-Za-z0-9-]*|-*|*-|*--*)
			sshlab_die "BRANCHLINE_SSHLAB_PREFIX must be 'bl-' followed by alphanumeric/hyphen characters (got '$SSHLAB_PREFIX')"
			;;
	esac

	if [ "${BRANCHLINE_SSHLAB_MATRIX_NAMES:-0}" = "1" ] && [ "$SSHLAB_PREFIX" = "bl-lab" ]; then
		SSHLAB_CONTAINER_NAME="$SSHLAB_MATRIX_NAME"
	else
		SSHLAB_CONTAINER_NAME="${SSHLAB_PREFIX}-${SSHLAB_ENV}"
	fi

	SSHLAB_IMAGE_TAG="bl-sshlab-${SSHLAB_ENV}:latest"
	SSHLAB_HOST_ALIAS="$SSHLAB_CONTAINER_NAME"
	SSHLAB_REMOTE_USER="root"
	SSHLAB_REMOTE_REPO="/tmp/branchline-test-${SSHLAB_ENV}"
	_tmp_base=${TMPDIR:-/tmp}
	_tmp_base=${_tmp_base%/}
	SSHLAB_STATE_ROOT=${BRANCHLINE_SSHLAB_STATE:-${_tmp_base}/branchline-sshlab}
	SSHLAB_STATE_ROOT=${SSHLAB_STATE_ROOT%/}
	_nl='
'
	_cr=$(printf '\r')
	case "$SSHLAB_STATE_ROOT" in
		*\"*|*:*|*"$_nl"*|*"$_cr"*)
			sshlab_die "BRANCHLINE_SSHLAB_STATE cannot contain double-quotes, colons, or newline characters"
			;;
	esac
	SSHLAB_STATE_DIR="${SSHLAB_STATE_ROOT}/${SSHLAB_ENV}"
	SSHLAB_KEY_FILE="${SSHLAB_STATE_DIR}/id_ed25519"
	SSHLAB_PUBKEY_FILE="${SSHLAB_STATE_DIR}/id_ed25519.pub"
	SSHLAB_KNOWN_HOSTS="${SSHLAB_STATE_DIR}/known_hosts"
	SSHLAB_SSH_CONFIG="${SSHLAB_STATE_DIR}/ssh_config"
	SSHLAB_BIN_DIR="${SSHLAB_STATE_DIR}/bin"
	SSHLAB_SSH_WRAPPER="${SSHLAB_BIN_DIR}/ssh"
	SSHLAB_META_FILE="${SSHLAB_STATE_DIR}/meta.env"
	SSHLAB_LOG_DIR="${SSHLAB_STATE_DIR}/logs"

	sshlab_require_docker_daemon
}

# Bounded preflight: the Docker daemon must already be running (the scripts
# never start it). Fails fast with a readable error otherwise.
sshlab_require_docker_daemon() {
	if ! sshlab_run_bounded 10 docker version --format '{{.Server.Version}}' >/dev/null 2>&1; then
		sshlab_die "Docker daemon is not reachable (docker version timed out or failed); start Docker first and retry"
	fi
}

sshlab_container_exists() {
	_cid=$1
	docker ps -a --format '{{.Names}}' 2>/dev/null | grep -Fxq "$_cid"
}

sshlab_container_is_running() {
	_cid=$1
	docker ps --format '{{.Names}}' 2>/dev/null | grep -Fxq "$_cid"
}

sshlab_container_port() {
	_cid=$1
	_port_out=$(docker port "$_cid" 22/tcp 2>/dev/null) || return 0
	printf '%s\n' "$_port_out" | awk 'NR==1' | sed -n 's/^127\.0\.0\.1:\([0-9][0-9]*\)$/\1/p'
}

sshlab_verify_published_port() {
	_port=$1
	case "$_port" in
		''|*[!0-9]*)
			sshlab_die "invalid published sshd port '$_port' (expected a numeric 127.0.0.1 host port published by Docker)"
			;;
	esac
	if [ "$_port" -le 0 ] || [ "$_port" -gt 65535 ]; then
		sshlab_die "published sshd port '$_port' out of range 1-65535"
	fi
}

# Run a command with a hard timeout (seconds as $1) using a small python3
# wrapper so a hung container or network probe can never block cleanup.
# On timeout the child process group is killed and "TIMED OUT after <n>s" is
# printed; the wrapper always exits non-zero in that case.
sshlab_run_bounded() {
	_timeout=$1
	shift
	python3 -c '
import os, signal, subprocess, sys
timeout = float(sys.argv[1])
proc = subprocess.Popen(sys.argv[2:], start_new_session=True)
try:
    rc = proc.wait(timeout=timeout)
except subprocess.TimeoutExpired:
    # Kill the whole process group, not just the direct child, so a detached
    # grandchild (e.g. a shell stub sleeping forever) cannot keep inherited
    # stdout/stderr open and stall whoever reads them.
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass
    proc.wait()
    print("TIMED OUT after %ss" % sys.argv[1], flush=True)
    sys.exit(124)
sys.exit(rc)
' "$_timeout" "$@"
}

# Compute the exact ControlMaster socket path the app uses for a given
# user/host/port/identity tuple: SHA-256 of
# "<user>\x00<host>\x00<port>\x00<identity>", first 16 lowercase hex chars,
# as "/tmp/branchline-<16hex>". Must stay byte-identical with
# internal/git/exec.go controlPath().
sshlab_control_path() {
	python3 -c '
import hashlib, sys
payload = ("%s\x00%s\x00%d\x00%s" % (sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4])).encode("utf-8")
print("/tmp/branchline-" + hashlib.sha256(payload).hexdigest()[:16])
' "$1" "$2" "$3" "$4"
}

# Remove stale ControlMaster sockets for the current host alias only.
# The app hashes (User, Host, Port, Identity) without the container IP, so a
# rebuilt container reuses the same socket path; a leftover socket would pin
# new connections to a destroyed IP. Covers the exact tuples the tests use
# (user in {"", remote user} x identity in {"", key file}, port 0) and never
# touches any other /tmp/branchline-* socket.
sshlab_cleanup_control_sockets() {
	[ -n "${SSHLAB_HOST_ALIAS:-}" ] || return 0
	_seen_paths=""
	for _cu in "" "${SSHLAB_REMOTE_USER:-root}"; do
		for _ci in "" "${SSHLAB_KEY_FILE:-}"; do
			_cp=$(sshlab_control_path "$_cu" "$SSHLAB_HOST_ALIAS" 0 "$_ci") || continue
			# Guard the exact expected shape before ever removing anything.
			case "$_cp" in
				/tmp/branchline-????????????????) ;;
				*) continue ;;
			esac
			case "$_seen_paths" in
				*"$_cp "*) continue ;;
			esac
			_seen_paths="${_seen_paths}${_cp} "
			[ -e "$_cp" ] || continue
			sshlab_log "removing stale ControlMaster socket $_cp for ${SSHLAB_HOST_ALIAS}"
			sshlab_run_bounded 3 /usr/bin/ssh -F /dev/null -O exit -o ControlPath="$_cp" "$SSHLAB_HOST_ALIAS" >/dev/null 2>&1 || true
			if [ -e "$_cp" ]; then
				rm -f "$_cp" 2>/dev/null || true
			fi
			if [ -e "$_cp" ]; then
				sshlab_log "WARNING: could not remove stale ControlMaster socket $_cp"
			fi
		done
	done
}

# One-shot bounded readiness probe: no retries, no multiplex reuse. Confirms
# the remote is Linux and the sshd log file is being written. Returns non-zero
# with a readable error otherwise; callers decide whether to abort.
sshlab_probe_ready() {
	_reason=$1
	_probe_out=""
	_probe_rc=0
	_probe_out=$("$SSHLAB_SSH_WRAPPER" "$SSHLAB_HOST_ALIAS" "printf '%s ' \"\$(uname -s)\"; if [ -r /tmp/sshd.log ]; then printf 'log-ok\n'; else printf 'log-missing\n'; fi" 2>/dev/null) || _probe_rc=$?
	_probe_os=$(printf '%s\n' "$_probe_out" | awk 'NR==1 {print $1}')
	_probe_log=$(printf '%s\n' "$_probe_out" | awk 'NR==1 {print $2}')
	if [ "$_probe_rc" -ne 0 ]; then
		sshlab_log "probe failed (${_reason}): ssh to ${SSHLAB_HOST_ALIAS} exited ${_probe_rc}"
		return 1
	fi
	if [ "$_probe_os" != "Linux" ]; then
		sshlab_log "probe failed (${_reason}): remote uname is '${_probe_os}', expected 'Linux'"
		return 1
	fi
	if [ "$_probe_log" != "log-ok" ]; then
		sshlab_log "probe failed (${_reason}): /tmp/sshd.log is missing or unreadable inside ${SSHLAB_HOST_ALIAS}"
		return 1
	fi
	return 0
}

# Collect a bounded diagnostic snapshot into <diag_file> after a test failure,
# before the container is torn down. Every capture is individually wrapped in
# sshlab_run_bounded and fault-tolerant so a hung or dead container cannot
# block cleanup; the function always returns 0 and never masks the original
# non-zero exit code. Prints the diag file path on stderr.
sshlab_collect_diagnostics() {
	_diag_file=$1
	_diag_label=$2
	mkdir -p "$(dirname "$_diag_file")" 2>/dev/null || true
	_diag_expected_port=${BRANCHLINE_SSHLAB_PORT:-unknown}
	{
		printf '=== sshlab failure diagnostics ===\n'
		printf 'collected_at_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf unknown)"
		printf 'context=%s\n' "$_diag_label"
		printf 'env=%s container=%s expected_port=%s host_alias=%s\n' "${SSHLAB_ENV:-unknown}" "${SSHLAB_CONTAINER_NAME:-unknown}" "$_diag_expected_port" "${SSHLAB_HOST_ALIAS:-unknown}"
		printf 'host_time=%s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z' 2>/dev/null || printf unknown)"

		printf -- '--- docker inspect %s ---\n' "${SSHLAB_CONTAINER_NAME:-unknown}"
		sshlab_run_bounded 10 docker inspect "${SSHLAB_CONTAINER_NAME:-unknown}" 2>&1 || true

		printf -- '--- docker exec: ps / sockets / network / sshd.log ---\n'
		sshlab_run_bounded 10 docker exec "${SSHLAB_CONTAINER_NAME:-unknown}" /bin/sh -c '
printf -- "--- ps ---\n"
ps aux 2>&1 || ps 2>&1 || true
printf -- "--- ss/netstat ---\n"
ss -tnp 2>&1 || netstat -tn 2>&1 || true
printf -- "--- ip addr ---\n"
ip addr 2>&1 || true
printf -- "--- ip route ---\n"
ip route 2>&1 || true
printf -- "--- tail -n 200 /tmp/sshd.log ---\n"
tail -n 200 /tmp/sshd.log 2>&1 || printf "(no /tmp/sshd.log)\n"
' 2>&1 || true

		printf -- '--- host ControlPath state for %s ---\n' "${SSHLAB_HOST_ALIAS:-unknown}"
		for _du in "" "${SSHLAB_REMOTE_USER:-root}"; do
			for _di in "" "${SSHLAB_KEY_FILE:-}"; do
				_dcp=$(sshlab_control_path "$_du" "${SSHLAB_HOST_ALIAS:-unknown}" 0 "$_di" 2>/dev/null) || _dcp=""
				[ -n "$_dcp" ] || continue
				if [ -S "$_dcp" ]; then
					printf '%s present(socket)\n' "$_dcp"
				elif [ -e "$_dcp" ]; then
					printf '%s present(non-socket file)\n' "$_dcp"
				else
					printf '%s absent\n' "$_dcp"
				fi
			done
		done

		printf -- '--- ssh -vvv probe %s ---\n' "${SSHLAB_HOST_ALIAS:-unknown}"
		sshlab_run_bounded 15 /usr/bin/ssh -vvv \
			-F "${SSHLAB_SSH_CONFIG:-/dev/null}" \
			-o BatchMode=yes -o ConnectTimeout=5 \
			-o ControlMaster=no -o ControlPath=none \
			"${SSHLAB_HOST_ALIAS:-unknown}" true 2>&1 || true

		printf '=== end of diagnostics ===\n'
	} >>"$_diag_file" 2>/dev/null || true
	printf 'sshlab: diagnostics written to %s\n' "$_diag_file" >&2
	return 0
}

sshlab_enforce_max_containers() {
	_target=$1
	_max=${BRANCHLINE_SSHLAB_MAX_CONTAINERS:-2}
	_running_other=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E "^${SSHLAB_PREFIX}-" | grep -Fxv "$_target" | wc -l | tr -d ' ')
	if [ "$_running_other" -ge "$_max" ]; then
		_list=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E "^${SSHLAB_PREFIX}-" | tr '\n' ' ')
		sshlab_die "already running $_running_other '${SSHLAB_PREFIX}-*' containers (${_list}); max allowed is $_max. Run 'sh scripts/sshlab/down.sh <env>' first"
	fi
}

sshlab_reset_remote_repo() {
	_cid=$1
	_repo=$2
	case "$_repo" in
		/tmp/branchline-test-*) ;;
		*) sshlab_die "refusing to reset remote repository outside /tmp/branchline-test- prefix: $_repo" ;;
	esac
	docker exec "$_cid" /bin/sh -c "
set -eu
repo=$(sshlab_shell_quote "$_repo")
rm -rf -- \"\$repo\"
mkdir -p -- \"\$repo\"
chmod 700 \"\$repo\"
cd \"\$repo\"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
git init -q
git symbolic-ref HEAD refs/heads/main
git config user.name 'Branchline SSHLab'
git config user.email 'sshlab@example.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
git config core.hooksPath /dev/null
git config core.autocrlf false
printf 'one\n' > a.txt
chmod 644 a.txt
git add a.txt
git commit -q -m first
"
}

sshlab_clean_path_with_bin() {
	_new_bin=$1
	_old_path=${PATH:-/usr/bin:/bin}
	_cleaned=""
	_ifs_saved=$IFS
	IFS=":"
	for _dir in $_old_path; do
		[ -n "$_dir" ] || continue
		case "$_dir" in
			"${SSHLAB_STATE_ROOT}"/*/bin|"$_new_bin")
				continue
				;;
		esac
		if [ -z "$_cleaned" ]; then
			_cleaned="$_dir"
		else
			_cleaned="${_cleaned}:${_dir}"
		fi
	done
	IFS=$_ifs_saved
	for _brew in /opt/homebrew/bin /usr/local/bin; do
		case ":${_cleaned}:" in
			*":${_brew}:"*) ;;
			*) _cleaned="${_brew}:${_cleaned}" ;;
		esac
	done
	printf '%s:%s' "$_new_bin" "$_cleaned"
}

sshlab_sync_ssh_state() {
	mkdir -p "$SSHLAB_STATE_DIR" "$SSHLAB_BIN_DIR" "$SSHLAB_LOG_DIR"
	chmod 700 "$SSHLAB_STATE_DIR"

	if [ ! -f "$SSHLAB_KEY_FILE" ] || [ ! -f "$SSHLAB_PUBKEY_FILE" ]; then
		rm -f "$SSHLAB_KEY_FILE" "$SSHLAB_PUBKEY_FILE"
		ssh-keygen -q -t ed25519 -N "" -C "branchline-sshlab-${SSHLAB_ENV}" -f "$SSHLAB_KEY_FILE"
		chmod 600 "$SSHLAB_KEY_FILE"
		chmod 644 "$SSHLAB_PUBKEY_FILE"
	fi

	CONTAINER_PORT=""
	_attempt=0
	while [ "$_attempt" -lt 30 ]; do
		if sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
			CONTAINER_PORT=$(sshlab_container_port "$SSHLAB_CONTAINER_NAME")
			if [ -n "$CONTAINER_PORT" ]; then
				if docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c 'mkdir -p /run/sshd && test -s /etc/ssh/ssh_host_ed25519_key.pub && (grep -qx "sshd" /proc/[0-9]*/comm 2>/dev/null || /usr/sbin/sshd -E /tmp/sshd.log)' >/dev/null 2>&1; then
					break
				fi
			fi
		fi
		_attempt=$((_attempt + 1))
		sleep 0.2
	done

	sshlab_verify_published_port "$CONTAINER_PORT"

	_inspected_name=$(docker inspect "$SSHLAB_CONTAINER_NAME" | jq -r '.[0].Name // empty | ltrimstr("/")')
	_inspected_state=$(docker inspect "$SSHLAB_CONTAINER_NAME" | jq -r '.[0].State.Status // empty')
	[ "$_inspected_name" = "$SSHLAB_CONTAINER_NAME" ] || sshlab_die "docker inspect name mismatch: got '$_inspected_name', want '$SSHLAB_CONTAINER_NAME'"
	[ "$_inspected_state" = "running" ] || sshlab_die "container '$SSHLAB_CONTAINER_NAME' is not running (state='$_inspected_state')"

	_pubkey_content=$(cat "$SSHLAB_PUBKEY_FILE")
	[ -n "$_pubkey_content" ] || sshlab_die "empty public key in $SSHLAB_PUBKEY_FILE"
	docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c "
set -eu
mkdir -p /root/.ssh
chmod 700 /root/.ssh
printf '%s\n' $(sshlab_shell_quote "$_pubkey_content") > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
chown -R 0:0 /root/.ssh
"

	_raw_host_pub=$(docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c 'cat /etc/ssh/ssh_host_ed25519_key.pub')
	_host_key_type=$(printf '%s\n' "$_raw_host_pub" | awk 'NR==1 {print $1}')
	_host_key_data=$(printf '%s\n' "$_raw_host_pub" | awk 'NR==1 {print $2}')
	case "$_host_key_type" in
		ssh-ed25519) ;;
		*) sshlab_die "unexpected container sshd host key type '$_host_key_type'" ;;
	esac
	[ -n "$_host_key_data" ] || sshlab_die "empty container sshd host public key data"

	# The published host port is per-container and changes across rebuilds, so
	# host-key verification is pinned to the stable host alias via HostKeyAlias
	# instead of the 127.0.0.1:<port> socket address.
	printf '%s %s %s\n' \
		"$SSHLAB_HOST_ALIAS" "$_host_key_type" "$_host_key_data" \
		> "$SSHLAB_KNOWN_HOSTS"
	chmod 600 "$SSHLAB_KNOWN_HOSTS"

	cat > "$SSHLAB_SSH_CONFIG" <<EOF
Host ${SSHLAB_HOST_ALIAS}
    HostName 127.0.0.1
    User ${SSHLAB_REMOTE_USER}
    Port ${CONTAINER_PORT}
    HostKeyAlias ${SSHLAB_HOST_ALIAS}
    CheckHostIP no
    IdentityFile "${SSHLAB_KEY_FILE}"
    IdentitiesOnly yes
    IdentityAgent none
    UserKnownHostsFile "${SSHLAB_KNOWN_HOSTS}"
    GlobalKnownHostsFile /dev/null
    StrictHostKeyChecking yes
    PasswordAuthentication no
    KbdInteractiveAuthentication no
    BatchMode yes
    ConnectTimeout 15
    ServerAliveInterval 30
    LogLevel ERROR
EOF
	chmod 600 "$SSHLAB_SSH_CONFIG"

	# The wrapper must NOT intercept 127.0.0.1 or localhost: those are loopback
	# targets of other tests (e.g. internal/sshtest.StartLoopback) and must
	# reach the real host, not this container's published port.
	cat > "$SSHLAB_SSH_WRAPPER" <<EOF
#!/bin/sh
# Target-scoped SSH wrapper for Branchline sshlab (${SSHLAB_ENV}).
# Intercepts only ${SSHLAB_HOST_ALIAS} (optionally as ${SSHLAB_REMOTE_USER}@${SSHLAB_HOST_ALIAS});
# every other host, including 127.0.0.1 and localhost, passes through to /usr/bin/ssh untouched.
for _arg in "\$@"; do
	case "\$_arg" in
		${SSHLAB_HOST_ALIAS}|${SSHLAB_REMOTE_USER}@${SSHLAB_HOST_ALIAS})
			exec /usr/bin/ssh -F $(sshlab_shell_quote "$SSHLAB_SSH_CONFIG") "\$@"
			;;
	esac
done
exec /usr/bin/ssh "\$@"
EOF
	chmod 755 "$SSHLAB_SSH_WRAPPER"

	_ssh_ready=0
	_try=0
	while [ "$_try" -lt 25 ]; do
		if _os=$("$SSHLAB_SSH_WRAPPER" "$SSHLAB_HOST_ALIAS" "uname -s" 2>/dev/null); then
			if [ "$_os" = "Linux" ]; then
				_ssh_ready=1
				break
			fi
		fi
		_try=$((_try + 1))
		sleep 0.2
	done
	[ "$_ssh_ready" = "1" ] || sshlab_die "failed to verify SSH connection to ${SSHLAB_HOST_ALIAS} (127.0.0.1:${CONTAINER_PORT})"

	_git_ver=$("$SSHLAB_SSH_WRAPPER" "$SSHLAB_HOST_ALIAS" "git --version" | awk '{print $3}')

	cat > "$SSHLAB_META_FILE" <<EOF
SSHLAB_ENV=$(sshlab_shell_quote "$SSHLAB_ENV")
SSHLAB_CONTAINER_NAME=$(sshlab_shell_quote "$SSHLAB_CONTAINER_NAME")
SSHLAB_HOST_ALIAS=$(sshlab_shell_quote "$SSHLAB_HOST_ALIAS")
SSHLAB_SSH_PORT=$(sshlab_shell_quote "$CONTAINER_PORT")
SSHLAB_REMOTE_REPO=$(sshlab_shell_quote "$SSHLAB_REMOTE_REPO")
SSHLAB_SSH_CONFIG=$(sshlab_shell_quote "$SSHLAB_SSH_CONFIG")
SSHLAB_KNOWN_HOSTS=$(sshlab_shell_quote "$SSHLAB_KNOWN_HOSTS")
SSHLAB_KEY_FILE=$(sshlab_shell_quote "$SSHLAB_KEY_FILE")
SSHLAB_BIN_DIR=$(sshlab_shell_quote "$SSHLAB_BIN_DIR")
SSHLAB_GIT_VERSION=$(sshlab_shell_quote "$_git_ver")
EOF
	chmod 600 "$SSHLAB_META_FILE"
}
