#!/bin/sh
# Tear down a Branchline sshlab container environment and clean up its runtime keys/config.
# Preserves ${STATE_DIR}/logs so test JSON logs remain readable after down.sh.
# Usage: sh scripts/sshlab/down.sh [e1|e2|e3|all] [--purge-image]
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

sshlab_require_cmd docker python3

ENV_ARG="e1"
PURGE_IMAGE=0

for _arg in "$@"; do
	case "$_arg" in
		--purge-image)
			PURGE_IMAGE=1
			;;
		-h|--help)
			printf 'usage: %s [e1|e2|e3|all] [--purge-image]\n' "$0" >&2
			exit 0
			;;
		*)
			ENV_ARG="$_arg"
			;;
	esac
done

down_one() {
	sshlab_resolve_env "$1"

	if sshlab_container_exists "$SSHLAB_CONTAINER_NAME"; then
		sshlab_log "removing container ${SSHLAB_CONTAINER_NAME}..."
		docker rm -f "$SSHLAB_CONTAINER_NAME" >/dev/null
		if sshlab_container_exists "$SSHLAB_CONTAINER_NAME"; then
			sshlab_die "failed to remove container ${SSHLAB_CONTAINER_NAME}"
		fi
	fi

	_build_cid="${SSHLAB_CONTAINER_NAME}-build"
	if sshlab_container_exists "$_build_cid"; then
		sshlab_log "removing build container ${_build_cid}..."
		docker rm -f "$_build_cid" >/dev/null
		if sshlab_container_exists "$_build_cid"; then
			sshlab_die "failed to remove build container ${_build_cid}"
		fi
	fi

	if [ "$PURGE_IMAGE" = "1" ] && docker image inspect "$SSHLAB_IMAGE_TAG" >/dev/null 2>&1; then
		sshlab_log "removing image ${SSHLAB_IMAGE_TAG}..."
		docker image rm "$SSHLAB_IMAGE_TAG" >/dev/null
		if docker image inspect "$SSHLAB_IMAGE_TAG" >/dev/null 2>&1; then
			sshlab_die "failed to remove image ${SSHLAB_IMAGE_TAG}"
		fi
	fi

	rm -f \
		"$SSHLAB_KEY_FILE" \
		"$SSHLAB_PUBKEY_FILE" \
		"$SSHLAB_KNOWN_HOSTS" \
		"$SSHLAB_SSH_CONFIG" \
		"$SSHLAB_SSH_WRAPPER" \
		"$SSHLAB_META_FILE"
	rmdir "$SSHLAB_BIN_DIR" 2>/dev/null || true

	# Remove stale ControlMaster sockets for this host alias so a later
	# rebuild can never multiplex onto a destroyed container's IP. Scoped to
	# the exact sockets of this alias; other /tmp/branchline-* are untouched.
	sshlab_cleanup_control_sockets

	sshlab_log "${SSHLAB_ENV} down (${SSHLAB_CONTAINER_NAME} removed; logs preserved in ${SSHLAB_LOG_DIR})"
}

case "$ENV_ARG" in
	all|ALL)
		for _e in e1 e2 e3; do
			down_one "$_e"
		done
		;;
	*)
		down_one "$ENV_ARG"
		;;
esac
