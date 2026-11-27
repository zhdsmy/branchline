#!/bin/sh
# Scoped fault injection entrypoints for Branchline sshlab containers (e1, e2, e3 ONLY).
# Strictly refuses external hosts and any non-sshlab target.
#
# Usage:
#   sh scripts/sshlab/fault.sh <e1|e2|e3> <action> [args...]
#
# Actions:
#   status                Show container and sshd/iptables status
#   stop                  Stop the container (simulates host poweroff / network down)
#   start                 Start a stopped container and ensure sshd is running
#   kill-sshd             Kill all sshd processes inside the container (port 22 connection refused)
#   start-sshd            Start sshd inside the running container
#   restart-sshd          Restart sshd inside the running container
#   iptables-drop         Drop incoming TCP port 22 traffic via iptables inside the container (blackhole/timeout)
#   iptables-reject       Reject incoming TCP port 22 traffic with tcp-reset via iptables
#   iptables-clear        Flush iptables INPUT rules inside the container
#   tc-delay <ms>         Apply tc netem delay inside the container (requires the NET_ADMIN capability)
#   tc-clear              Clear tc root qdisc if present
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

sshlab_require_cmd docker ssh-keygen ssh jq awk sed grep tr wc

if [ $# -lt 2 ]; then
	cat >&2 <<EOF
usage: $0 <e1|e2|e3> <action> [args...]
actions: status | stop | start | kill-sshd | start-sshd | restart-sshd | iptables-drop | iptables-reject | iptables-clear | tc-delay <ms> | tc-clear
note: fault injection is strictly limited to disposable e1/e2/e3 sshlab containers.
EOF
	exit 64
fi

ENV_ARG=$1
ACTION=$2
shift 2

sshlab_resolve_env "$ENV_ARG"

case "$SSHLAB_CONTAINER_NAME" in
	"${SSHLAB_PREFIX}-"*|bl-lab-*) ;;
	*) sshlab_die "refusing fault injection on non-sshlab container '$SSHLAB_CONTAINER_NAME'" ;;
esac

require_running() {
	sshlab_container_is_running "$SSHLAB_CONTAINER_NAME" || sshlab_die "container '${SSHLAB_CONTAINER_NAME}' is not running"
}

case "$ACTION" in
	status)
		if ! sshlab_container_exists "$SSHLAB_CONTAINER_NAME"; then
			printf 'container=%s state=missing\n' "$SSHLAB_CONTAINER_NAME"
			exit 0
		fi
		if ! sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
			printf 'container=%s state=stopped\n' "$SSHLAB_CONTAINER_NAME"
			exit 0
		fi
		_port=$(sshlab_container_port "$SSHLAB_CONTAINER_NAME")
		_sshd=$(docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c 'if grep -qx "sshd" /proc/[0-9]*/comm 2>/dev/null; then printf running; else printf stopped; fi')
		printf 'container=%s state=running port=%s sshd=%s\n' "$SSHLAB_CONTAINER_NAME" "$_port" "$_sshd"
		;;
	stop)
		sshlab_container_exists "$SSHLAB_CONTAINER_NAME" || sshlab_die "container '${SSHLAB_CONTAINER_NAME}' does not exist"
		if sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
			docker stop "$SSHLAB_CONTAINER_NAME" >/dev/null
		fi
		sshlab_log "stopped container ${SSHLAB_CONTAINER_NAME}"
		;;
	start)
		sshlab_container_exists "$SSHLAB_CONTAINER_NAME" || sshlab_die "container '${SSHLAB_CONTAINER_NAME}' does not exist; run up.sh first"
		if ! sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
			sshlab_enforce_max_containers "$SSHLAB_CONTAINER_NAME"
			docker start "$SSHLAB_CONTAINER_NAME" >/dev/null
		fi
		sshlab_sync_ssh_state
		sshlab_log "started container ${SSHLAB_CONTAINER_NAME} (port=${CONTAINER_PORT}) and refreshed ssh_config/known_hosts/meta.env"
		;;
	kill-sshd)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c '
for _c in /proc/[0-9]*/comm; do
	[ -r "$_c" ] || continue
	if [ "$(cat "$_c" 2>/dev/null || true)" = "sshd" ]; then
		_p=${_c#/proc/}
		_p=${_p%/comm}
		kill -KILL "$_p" 2>/dev/null || true
	fi
done
'
		sshlab_log "killed sshd inside ${SSHLAB_CONTAINER_NAME} (container remains running)"
		;;
	start-sshd)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c '
set -eu
mkdir -p /run/sshd
if ! grep -qx "sshd" /proc/[0-9]*/comm 2>/dev/null; then
	/usr/sbin/sshd
fi
'
		sshlab_log "started sshd inside ${SSHLAB_CONTAINER_NAME}"
		;;
	restart-sshd)
		require_running
		sh "$0" "$SSHLAB_ENV" kill-sshd
		sh "$0" "$SSHLAB_ENV" start-sshd
		;;
	iptables-drop)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c '
set -eu
iptables -F INPUT
iptables -I INPUT 1 -p tcp --dport 22 -j DROP
'
		sshlab_log "injected iptables DROP on TCP port 22 inside ${SSHLAB_CONTAINER_NAME}"
		;;
	iptables-reject)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c '
set -eu
iptables -F INPUT
iptables -I INPUT 1 -p tcp --dport 22 -j REJECT --reject-with tcp-reset
'
		sshlab_log "injected iptables REJECT (tcp-reset) on TCP port 22 inside ${SSHLAB_CONTAINER_NAME}"
		;;
	iptables-clear)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c 'iptables -F INPUT'
		sshlab_log "cleared iptables INPUT rules inside ${SSHLAB_CONTAINER_NAME}"
		;;
	tc-delay)
		require_running
		_ms=${1:-100}
		case "$_ms" in
			''|*[!0-9]*) sshlab_die "tc-delay requires a numeric millisecond value (got '$_ms')" ;;
		esac
		if ! _tc_err=$(docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c "tc qdisc replace dev eth0 root netem delay ${_ms}ms" 2>&1); then
			sshlab_die "tc netem failed inside ${SSHLAB_CONTAINER_NAME} (${_tc_err}); use 'iptables-drop', 'kill-sshd', or 'stop' instead (see scripts/sshlab/README.md)"
		fi
		sshlab_log "applied ${_ms}ms tc netem delay on eth0 in ${SSHLAB_CONTAINER_NAME}"
		;;
	tc-clear)
		require_running
		docker exec "$SSHLAB_CONTAINER_NAME" /bin/sh -c 'tc qdisc del dev eth0 root 2>/dev/null || true'
		sshlab_log "cleared tc qdisc on eth0 in ${SSHLAB_CONTAINER_NAME}"
		;;
	*)
		sshlab_die "unknown fault action '$ACTION'"
		;;
esac
