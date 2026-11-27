#!/bin/sh
# A leased 16 MiB tmpfs in its own Linux user/mount namespaces.
# Run on a disposable sshlab Linux instance, never on an external host or host filesystem.
# stdout: branchline-bounded-fs-v1 PID MOUNT_NS_NUMBER ROOT CAPACITY_BYTES
# Keep stdin open while using the lease; send "release" or close it to clean up.
set -eu

case "${1:-}" in
serve)
    [ "$#" -eq 1 ] || exit 64
    [ "$(uname -s)" = Linux ] || { echo 'bounded-fs requires Linux' >&2; exit 69; }
    for tool in unshare timeout mount umount readlink mktemp; do
        command -v "$tool" >/dev/null || { echo "bounded-fs missing $tool" >&2; exit 69; }
    done
    script=$(readlink -f "$0")
    root=$(mktemp -d /tmp/branchline-test-bounded-fs.XXXXXXXX)
    chmod 700 "$root"
    parent=$(readlink /proc/self/ns/mnt)
    # Only empty directories are removed here; this process never writes a
    # filler or removes repository data. tmpfs data disappears at unmount.
    trap 'rmdir "$root/fs" 2>/dev/null || true; rmdir "$root" 2>/dev/null || true' EXIT
    timeout --signal=TERM --kill-after=5s 300s \
        unshare --user --map-root-user --mount /bin/sh "$script" _serve "$root" "$parent"
    ;;
_serve)
    [ "$#" -eq 3 ] || exit 64
    root=$2
    parent=$3
    case "$root" in /tmp/branchline-test-bounded-fs.*) ;; *) exit 64 ;; esac
    suffix=${root#/tmp/branchline-test-bounded-fs.}
    case "$suffix" in ''|*[!A-Za-z0-9]*) exit 64 ;; esac
    [ ! -L "$root" ] && [ -d "$root" ] && [ "$(id -u)" -eq 0 ] || exit 64
    current=$(readlink /proc/self/ns/mnt)
    [ "$current" != "$parent" ] || { echo 'mount namespace did not change' >&2; exit 1; }
    mount --make-rprivate /
    mkdir "$root/fs"
    mounted=no
    cleanup() {
        if [ "$mounted" = yes ]; then
            if ! umount "$root/fs"; then
                echo 'bounded-fs unmount failed; directory retained' >&2
                return 1
            fi
        fi
        rmdir "$root/fs"
    }
    trap cleanup EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
    mount -t tmpfs -o size=16777216,nr_inodes=4096,nodev,nosuid tmpfs "$root/fs"
    mounted=yes
    number=${current#mnt:[}
    number=${number%]}
    case "$number" in ''|*[!0-9]*) exit 1 ;; esac
    printf 'branchline-bounded-fs-v1 %s %s %s 16777216\n' "$$" "$number" "$root"
    # This intentionally holds a real process handle. EOF means the caller
    # has gone away; the outer timeout also bounds a forgotten live lease.
    lease=
    IFS= read -r lease || :
    [ -z "$lease" ] || [ "$lease" = release ] || { echo 'invalid lease input' >&2; exit 64; }
    ;;
*)
    echo 'usage: bounded-fs.sh serve (on disposable sshlab Linux only)' >&2
    exit 64
    ;;
esac
