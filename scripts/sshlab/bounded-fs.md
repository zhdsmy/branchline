# Bounded ENOSPC tmpfs fixture protocol

This fixture provides a bounded 16 MiB tmpfs in a private Linux mount namespace
for exercising ENOSPC behavior. It is a namespace/ENOSPC fixture only, not a
UI test or a full end-to-end environment.

## Scope and setup

`bounded-fs.sh serve` runs **inside an owned sshlab Linux instance only**.
The caller first validates the actual running container identity, e1/e2/e3
metadata, published sshd port, task ownership and private SSH
config/known_hosts. It must not run on an external host, a developer machine,
or a shared instance. The script does not create containers, change
capabilities, install packages, or touch host mounts. The maintained verifier
checks an existing instance using `--state`.

```sh
BRANCHLINE_SSHLAB_PREFIX=bl-enospc BRANCHLINE_SSHLAB_STATE=/tmp/branchline-sshlab-enospc sh scripts/sshlab/up.sh e1
python3 scripts/sshlab/test-bounded-fs.py --state /tmp/branchline-sshlab-enospc/e1
BRANCHLINE_SSHLAB_PREFIX=bl-enospc BRANCHLINE_SSHLAB_STATE=/tmp/branchline-sshlab-enospc sh scripts/sshlab/down.sh e1
```

These are targeted fixture commands, not `regression.sh` or a second full test
run. Use a fresh owned prefix/state for another run. The Python verifier requires
Python 3.9+ locally. The guest requires unshare, mount/umount, nsenter, timeout,
readlink and the ordinary shell utilities; missing or denied capabilities FAIL.
Do not infer compatibility of a given environment from its presence in the
standard metadata schema.

**Docker limitation**: under Docker's default seccomp profile, `unshare` is
denied inside the sshlab containers (which carry `NET_ADMIN`/`NET_RAW` but not
`CAP_SYS_ADMIN`), so `unshare --user` fails with `Operation not permitted` and
this fixture FAILs. The capability set is deliberately not widened for it; the
fixture is currently unused by any Go test and remains a known limitation on
Docker.

## Lease contract

1. Copy this exact shell script into a unique temporary file under the owned
   `/tmp/branchline-test-<env>` path. Start `sh <script> serve` through actual strict
   SSH, with an owned process handle and stdin pipe. Keep stdin open.
2. The parent creates an empty private `/tmp/branchline-test-bounded-fs.XXXXXXXX`
   control directory. The child enters `unshare --user --map-root-user --mount`,
   proves its mount namespace differs, makes mount propagation private, and
   mounts a **16 MiB tmpfs, 4096 inodes, nodev/nosuid** at `<root>/fs`. No filler
   is written unless this setup succeeds. No SYS_ADMIN/privileged/host mount or
   root-volume fill is used.
3. After mounting, one stdout line is published:
   `branchline-bounded-fs-v1 <pid> <mount-namespace-number> <root> 16777216`.
   Nothing before the line is a ready lease. Retain the actual running process,
   validate the PID's namespace before each use, and never rely on a stale
   manifest alone. Each lease has an outer 300-second timeout, TERM then KILL
   after five seconds if needed. This bound is infrastructure, not a product
   Git timeout claim.
4. Independent commands enter the same namespace with
   `nsenter --target <pid> --user --mount -- /bin/sh -c <quoted-command>`.
   Verify `mountpoint`, tmpfs type and `df` capacity before creating a repo or
   filler. The mount is invisible to the parent's namespace; a path that looks
   empty outside it is not a valid product target.
5. Product SSH commands need the same namespace. A task-owned wrapper may
   prepend verified `nsenter ... sh -c` to the exact remote command and then
   exec real `/usr/bin/ssh` with the private strict config. It must forward all
   bytes, arguments and actual exit results, never supply fake Git output.
   Ordinary independent assertions use a separate SSH command. Keep the lease
   process and all workers live/owned until their actual completion.
6. Prepare the repo and assertions before filling. A maximum 17 MiB write
   attempt on the 16 MiB mount produces real ENOSPC; record the write's actual
   stderr/exit and zero available blocks. Store logs outside the bounded mount:
   a diagnostic file inside a full filesystem may itself have no space.
   EACCES, EFBIG, missing-path errors or a fake stderr are not ENOSPC.
7. Remove only the owned filler to release space. Recheck actual product retry
   and independent data invariants. Once all commands have completed/Waited,
   write `release\n` to the lease or close stdin. Both paths unmount, remove
   empty control directories and exit 0. Unknown lease text exits 64 with
   cleanup; transport loss or expiry may exit nonzero. Confirm the process and
   directory are absent. Active processes/files can prevent unmount: preserve
   that failure and reap only the owned workers before cleanup, never silently
   erase the directory or mount over another filesystem.

The shell API offers no arbitrary create path, cleanup/reset destination,
capacity override, or filler command. Its internal `_serve` root accepts only
the generated alphanumeric suffix. The caller owns its small uploaded script
and local config/wrapper, and removes them by exact path. As a final fallback,
tear down only the matching owned auxiliary container, preserving failed logs.
The ordinary gate's env/config, standard repo and instance remain untouched.
