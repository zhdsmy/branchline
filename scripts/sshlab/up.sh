#!/bin/sh
# Bring up (or reset) a Branchline sshlab container environment (e1, e2, or e3).
# Usage: sh scripts/sshlab/up.sh [e1|e2|e3] [--rebuild-image]
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

sshlab_require_cmd docker ssh-keygen ssh jq awk sed grep tr wc

ENV_ARG="e1"
REBUILD_IMAGE=${BRANCHLINE_SSHLAB_REBUILD_IMAGE:-0}

for _arg in "$@"; do
	case "$_arg" in
		--rebuild-image)
			REBUILD_IMAGE=1
			;;
		-h|--help)
			printf 'usage: %s [e1|e2|e3] [--rebuild-image]\n' "$0" >&2
			exit 0
			;;
		*)
			ENV_ARG="$_arg"
			;;
	esac
done

sshlab_resolve_env "$ENV_ARG"
sshlab_enforce_max_containers "$SSHLAB_CONTAINER_NAME"

_active_build_cid=""
cleanup_build_container() {
	if [ -n "${_active_build_cid:-}" ]; then
		docker rm -f "$_active_build_cid" >/dev/null 2>&1 || true
	fi
}
trap cleanup_build_container EXIT INT TERM HUP

ensure_image() {
	if [ "$REBUILD_IMAGE" != "1" ] && docker image inspect "$SSHLAB_IMAGE_TAG" >/dev/null 2>&1; then
		return 0
	fi

	# If rebuilding the image while the target container is running, remove the target
	# container first so the build container never becomes a 3rd running container,
	# and verify that total running containers allow starting the build container.
	if sshlab_container_exists "$SSHLAB_CONTAINER_NAME"; then
		sshlab_log "removing existing ${SSHLAB_CONTAINER_NAME} before image build..."
		docker rm -f "$SSHLAB_CONTAINER_NAME" >/dev/null
	fi

	_build_cid="${SSHLAB_CONTAINER_NAME}-build"
	if sshlab_container_exists "$_build_cid"; then
		docker rm -f "$_build_cid" >/dev/null
	fi
	sshlab_enforce_max_containers "$_build_cid"

	_arch=$(sshlab_host_arch)
	if ! docker image inspect "$SSHLAB_BASE_IMAGE" >/dev/null 2>&1; then
		sshlab_log "pulling base image ${SSHLAB_BASE_IMAGE} (linux/${_arch})..."
		docker pull --platform "linux/${_arch}" "$SSHLAB_BASE_IMAGE"
	fi

	sshlab_log "building local image ${SSHLAB_IMAGE_TAG} from ${SSHLAB_BASE_IMAGE}..."
	_active_build_cid="$_build_cid"
	docker run -d --name "$_build_cid" "$SSHLAB_BASE_IMAGE" /bin/sh -c 'sleep 600' >/dev/null

	if [ "$SSHLAB_DISTRO" = "debian" ]; then
		docker exec "$_build_cid" /bin/sh -c '
set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates git iproute2 iptables openssh-server >/dev/null
rm -rf /var/lib/apt/lists/*
mkdir -p /run/sshd /root/.ssh
chmod 700 /root/.ssh
rm -f /etc/ssh/ssh_host_*
ssh-keygen -q -t ed25519 -N "" -f /etc/ssh/ssh_host_ed25519_key
printf "%s\n" \
	"Port 22" \
	"ListenAddress 0.0.0.0" \
	"HostKey /etc/ssh/ssh_host_ed25519_key" \
	"PermitRootLogin prohibit-password" \
	"PubkeyAuthentication yes" \
	"AuthorizedKeysFile .ssh/authorized_keys" \
	"PasswordAuthentication no" \
	"KbdInteractiveAuthentication no" \
	"UsePAM no" \
	"StrictModes yes" \
	"Subsystem sftp internal-sftp" \
	"MaxStartups 64:30:128" \
	"MaxSessions 64" \
	"LogLevel VERBOSE" \
	> /etc/ssh/sshd_config
if /usr/sbin/sshd -T -o PerSourcePenalties=no >/dev/null 2>&1; then
	printf "%s\n" "PerSourcePenalties no" >> /etc/ssh/sshd_config
fi
/usr/sbin/sshd -t
rm -f /etc/ssh/ssh_host_*
'
	else
		docker exec "$_build_cid" /bin/sh -c '
set -eu
apk add --no-cache -q ca-certificates git iproute2 iptables openssh
sed -i "s/^root:!:/root:*:/" /etc/shadow
mkdir -p /run/sshd /root/.ssh
chmod 700 /root/.ssh
rm -f /etc/ssh/ssh_host_*
ssh-keygen -q -t ed25519 -N "" -f /etc/ssh/ssh_host_ed25519_key
printf "%s\n" \
	"Port 22" \
	"ListenAddress 0.0.0.0" \
	"HostKey /etc/ssh/ssh_host_ed25519_key" \
	"PermitRootLogin prohibit-password" \
	"PubkeyAuthentication yes" \
	"AuthorizedKeysFile .ssh/authorized_keys" \
	"PasswordAuthentication no" \
	"KbdInteractiveAuthentication no" \
	"StrictModes yes" \
	"Subsystem sftp internal-sftp" \
	"MaxStartups 64:30:128" \
	"MaxSessions 64" \
	"LogLevel VERBOSE" \
	> /etc/ssh/sshd_config
if /usr/sbin/sshd -T -o PerSourcePenalties=no >/dev/null 2>&1; then
	printf "%s\n" "PerSourcePenalties no" >> /etc/ssh/sshd_config
fi
/usr/sbin/sshd -t
rm -f /etc/ssh/ssh_host_*
'
	fi

	docker stop "$_build_cid" >/dev/null
	if docker image inspect "$SSHLAB_IMAGE_TAG" >/dev/null 2>&1; then
		docker image rm "$SSHLAB_IMAGE_TAG" >/dev/null
	fi
	docker commit "$_build_cid" "$SSHLAB_IMAGE_TAG" >/dev/null
	docker rm -f "$_build_cid" >/dev/null
	_active_build_cid=""
	sshlab_log "built image ${SSHLAB_IMAGE_TAG}"
}

ensure_image

# Refresh the hardened sshd_config inside a container: raised connection and
# session concurrency, verbose server logging, and PerSourcePenalties disabled
# only when the installed sshd supports it (OpenSSH >= 9.8). Followed by a
# strict config preflight so a broken sshd never starts silently.
SSHD_CONFIG_REFRESH='mkdir -p /run/sshd
if [ ! -s /etc/ssh/ssh_host_ed25519_key ]; then
	rm -f /etc/ssh/ssh_host_*
	ssh-keygen -q -t ed25519 -N "" -f /etc/ssh/ssh_host_ed25519_key
fi
printf "%s\n" \
	"Port 22" \
	"ListenAddress 0.0.0.0" \
	"HostKey /etc/ssh/ssh_host_ed25519_key" \
	"PermitRootLogin prohibit-password" \
	"PubkeyAuthentication yes" \
	"AuthorizedKeysFile .ssh/authorized_keys" \
	"PasswordAuthentication no" \
	"KbdInteractiveAuthentication no" \
	"StrictModes yes" \
	"Subsystem sftp internal-sftp" \
	"MaxStartups 64:30:128" \
	"MaxSessions 64" \
	"LogLevel VERBOSE" \
	> /etc/ssh/sshd_config
if /usr/sbin/sshd -T -o UsePAM=no >/dev/null 2>&1; then
	printf "%s\n" "UsePAM no" >> /etc/ssh/sshd_config
fi
if /usr/sbin/sshd -T -o PerSourcePenalties=no >/dev/null 2>&1; then
	printf "%s\n" "PerSourcePenalties no" >> /etc/ssh/sshd_config
fi
/usr/sbin/sshd -t'

if sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
	sshlab_log "reusing running container ${SSHLAB_CONTAINER_NAME}"
	docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c "
set -eu
mkdir -p /root/.ssh
chmod 700 /root/.ssh
$SSHD_CONFIG_REFRESH
if command -v iptables >/dev/null 2>&1; then
	iptables -F >/dev/null 2>&1 || true
fi
# Restart sshd so it picks up the refreshed config and the persistent log:
# tolerate kill failures from races, wait for the old daemon to be reaped so
# the new one can bind port 22, then always start a fresh daemon.
_killed_sshd=0
for _c in /proc/[0-9]*/comm; do
	[ -r \"\$_c\" ] || continue
	if [ \"\$(cat \"\$_c\" 2>/dev/null || true)\" = \"sshd\" ]; then
		_p=\${_c#/proc/}
		kill -KILL \"\${_p%/comm}\" 2>/dev/null && _killed_sshd=1 || true
	fi
done
if [ \"\$_killed_sshd\" = \"1\" ]; then
	_w=0
	while grep -qx \"sshd\" /proc/[0-9]*/comm 2>/dev/null && [ \"\$_w\" -lt 30 ]; do
		sleep 0.1
		_w=\$(( _w + 1 ))
	done
fi
/usr/sbin/sshd -E /tmp/sshd.log
"
else
	if sshlab_container_exists "$SSHLAB_CONTAINER_NAME"; then
		docker rm -f "$SSHLAB_CONTAINER_NAME" >/dev/null
	fi
	# Remove ControlMaster sockets left over from a previous container under
	# this host alias before the new container takes over the alias.
	sshlab_cleanup_control_sockets
	sshlab_log "starting container ${SSHLAB_CONTAINER_NAME} (${SSHLAB_DESC})..."
	docker run -d \
		--name "$SSHLAB_CONTAINER_NAME" \
		-u 0:0 \
		-w / \
		--entrypoint /bin/sh \
		--cap-add NET_ADMIN \
		--cap-add NET_RAW \
		-p 127.0.0.1::22 \
		"$SSHLAB_IMAGE_TAG" \
		-c "set -eu; mkdir -p /root/.ssh; chmod 700 /root/.ssh; $SSHD_CONFIG_REFRESH; /usr/sbin/sshd -E /tmp/sshd.log; trap \"exit 0\" TERM INT; while :; do sleep 1; done" >/dev/null
fi

sshlab_sync_ssh_state

# One-shot readiness probe: remote must be Linux and sshd must be writing its
# persistent log. No retries; a failure here means the environment is broken.
sshlab_probe_ready "up.sh readiness" || sshlab_die "sshlab readiness probe failed for ${SSHLAB_HOST_ALIAS}"

# Prepare clean disposable fixture repository inside the container.
sshlab_reset_remote_repo "$SSHLAB_CONTAINER_NAME" "$SSHLAB_REMOTE_REPO"

# Verify end-to-end SSH connectivity and fixture state via the isolated wrapper.
_probe=$("$SSHLAB_SSH_WRAPPER" "$SSHLAB_HOST_ALIAS" "uname -s && git -C $(sshlab_shell_quote "$SSHLAB_REMOTE_REPO") rev-parse --show-toplevel && git -C $(sshlab_shell_quote "$SSHLAB_REMOTE_REPO") rev-parse --abbrev-ref HEAD && cat $(sshlab_shell_quote "${SSHLAB_REMOTE_REPO}/a.txt")")
_expected=$(printf 'Linux\n%s\nmain\none' "$SSHLAB_REMOTE_REPO")
[ "$_probe" = "$_expected" ] || sshlab_die "failed to verify fixture repository on ${SSHLAB_HOST_ALIAS} (127.0.0.1:${CONTAINER_PORT})"

sshlab_log "${SSHLAB_ENV} ready: container=${SSHLAB_CONTAINER_NAME} port=${CONTAINER_PORT} git=${_git_ver} repo=${SSHLAB_REMOTE_REPO}"
