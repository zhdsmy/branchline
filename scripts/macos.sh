#!/bin/sh
# Use a persistent certificate for local native builds; never fall back to ad hoc.
set -eu

usage() {
    echo 'Usage: [BRANCHLINE_SIGN_IDENTITY=<40-char SHA-1>] sh scripts/macos.sh check|build|verify|dev' >&2
    exit 2
}

[ "$#" -eq 1 ] || usage
case "$1" in check|build|verify|dev) mode=$1 ;; *) usage ;; esac
[ "$(uname -s)" = Darwin ] || { echo 'This entry point requires macOS.' >&2; exit 1; }

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)

case "$mode" in
    check|build|verify)
        exec sh "$SCRIPT_DIR/package-macos.sh" "$mode"
        ;;
    dev)
        check_out=$(sh "$SCRIPT_DIR/package-macos.sh" check)
        identity=$(printf '%s\n' "$check_out" | awk '/^Code-signing identity available: / { print $4; exit }')
        case "$identity" in
            ''|*[!0-9a-fA-F]*)
                echo 'Failed to resolve signing identity for dev mode.' >&2
                exit 1 ;;
        esac
        [ "${#identity}" -eq 40 ] || { echo 'Failed to resolve signing identity for dev mode.' >&2; exit 1; }
        cd "$SCRIPT_DIR/.."
        exec go tool mygo dev -sign "$identity"
        ;;
esac
