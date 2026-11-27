#!/bin/sh
# Output POSIX shell export statements for a running Branchline sshlab environment.
# Only writes safe `export VAR='...'` lines to stdout; diagnostics go to stderr.
# Usage:
#   sh scripts/sshlab/env.sh [e1|e2|e3] > /tmp/sshlab.env && . /tmp/sshlab.env
#   eval "$(sh scripts/sshlab/env.sh [e1|e2|e3])"
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
# shellcheck source=scripts/sshlab/common.sh
. "${SCRIPT_DIR}/common.sh"

sshlab_require_cmd docker jq

ENV_ARG=${1:-e1}
case "$ENV_ARG" in
	-h|--help)
		printf 'usage: %s [e1|e2|e3]\n' "$0" >&2
		exit 0
		;;
esac

sshlab_resolve_env "$ENV_ARG"

[ -f "$SSHLAB_META_FILE" ] || sshlab_die "environment '${SSHLAB_ENV}' is not initialized (missing ${SSHLAB_META_FILE}); run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"
[ -x "$SSHLAB_SSH_WRAPPER" ] || sshlab_die "missing SSH wrapper ${SSHLAB_SSH_WRAPPER}; run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"
[ -f "$SSHLAB_SSH_CONFIG" ] || sshlab_die "missing SSH config ${SSHLAB_SSH_CONFIG}; run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"
[ -f "$SSHLAB_KNOWN_HOSTS" ] || sshlab_die "missing known_hosts ${SSHLAB_KNOWN_HOSTS}; run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"
[ -f "$SSHLAB_KEY_FILE" ] || sshlab_die "missing private key ${SSHLAB_KEY_FILE}; run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"

if ! sshlab_container_is_running "$SSHLAB_CONTAINER_NAME"; then
	sshlab_die "container '${SSHLAB_CONTAINER_NAME}' is not running; run 'sh scripts/sshlab/up.sh ${SSHLAB_ENV}' first"
fi

CONTAINER_PORT=$(sshlab_container_port "$SSHLAB_CONTAINER_NAME")
sshlab_verify_published_port "$CONTAINER_PORT"

NEW_PATH=$(sshlab_clean_path_with_bin "$SSHLAB_BIN_DIR")

printf 'export BRANCHLINE_LINUX_SSH_HOST=%s\n' "$(sshlab_shell_quote "$SSHLAB_HOST_ALIAS")"
printf 'export BRANCHLINE_LINUX_SSH_REPO=%s\n' "$(sshlab_shell_quote "$SSHLAB_REMOTE_REPO")"
printf 'export BRANCHLINE_SSHLAB_ENV=%s\n' "$(sshlab_shell_quote "$SSHLAB_ENV")"
printf 'export BRANCHLINE_SSHLAB_CONTAINER=%s\n' "$(sshlab_shell_quote "$SSHLAB_CONTAINER_NAME")"
printf 'export BRANCHLINE_SSHLAB_PORT=%s\n' "$(sshlab_shell_quote "$CONTAINER_PORT")"
printf 'export BRANCHLINE_SSHLAB_SSH_CONFIG=%s\n' "$(sshlab_shell_quote "$SSHLAB_SSH_CONFIG")"
printf 'export BRANCHLINE_SSHLAB_KNOWN_HOSTS=%s\n' "$(sshlab_shell_quote "$SSHLAB_KNOWN_HOSTS")"
printf 'export PATH=%s\n' "$(sshlab_shell_quote "$NEW_PATH")"
