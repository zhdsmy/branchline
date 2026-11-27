# Branchline sshlab (Docker Reproducible Linux SSH Testbed)

`scripts/sshlab/` provides a reproducible, isolated Linux SSH testbed built on Docker for exercising Branchline's SSH-based Git operations against disposable local containers through `internal/sshtest`.

## 1. Environments (`e1` / `e2` / `e3`)

sshlab manages disposable local containers only. The scripts hard-refuse any target that is not an `e1`/`e2`/`e3` container they created: container management, fault injection, and software installation never apply to external hosts.

| Env | Base Image | Git & Userland | Default Container (`BRANCHLINE_SSHLAB_PREFIX=bl-sshlab`) | Matrix Name (`BRANCHLINE_SSHLAB_PREFIX=bl-lab BRANCHLINE_SSHLAB_MATRIX_NAMES=1`) | Purpose |
| --- | --- | --- | --- | --- | --- |
| `e1` | `ubuntu:24.04` | Git 2.43 + GNU coreutils + OpenSSH 9.6 | `bl-sshlab-e1` | `bl-lab-ubuntu24` | Primary Linux target for dynamically discovered `TestConfiguredLinux*` tests |
| `e2` | `ubuntu:20.04` | Git 2.25 + GNU coreutils + OpenSSH 8.2 | `bl-sshlab-e2` | `bl-lab-ubuntu20` | Minimum Git version compatibility (Git 2.25) |
| `e3` | `alpine:latest` | BusyBox `sh`/`stat`/`sha256sum` + Git + OpenSSH | `bl-sshlab-e3` | `bl-lab-alpine` | Non-GNU userland (`BusyBox`); verifies POSIX shell & non-GNU tool paths |

> **Concurrency limit**: At most **2** containers with the active prefix (`bl-sshlab-*` by default) may run simultaneously (`BRANCHLINE_SSHLAB_MAX_CONTAINERS=2`). `up.sh` enforces this limit and never touches containers outside its prefix. The scripts never invoke `docker system`, `docker context`, `docker volume`, or any `prune` command; unrelated containers and images on the host are never touched.

### Hardened `sshd` Configuration

In every sshlab container, `sshd` runs with a hardened configuration written at image build time and refreshed whenever the container is started or reused (validated with `sshd -t` before the daemon starts):

- `MaxStartups 64:30:128` — raised unauthenticated-connection budget, because the test suite opens 100+ short-lived connections per environment (the OpenSSH default `10:30:100` can throttle them).
- `MaxSessions 64` — raised per-connection channel budget, because a single repository snapshot load opens 8 concurrent channels on one multiplexed connection (the default `10` leaves almost no headroom).
- `LogLevel VERBOSE` — server-side accept/auth/disconnect detail.
- `PerSourcePenalties no` — written **only** when the installed OpenSSH supports it (`>= 9.8`, probed with `sshd -T -o PerSourcePenalties=no`); older sshd builds (Ubuntu 20.04/24.04) reject the directive, so it is never written there.
- `sshd` is always started with `-E /tmp/sshd.log`, so all server-side events persist inside the container at `/tmp/sshd.log` (the containers have no syslog daemon; without `-E` every server error is lost).

## 2. Quick Start & Standard Entrypoints

All scripts are POSIX `/bin/sh` (`set -eu`), idempotent, and exit non-zero on any failure. Package discovery defaults to `./internal/...` (override with `BRANCHLINE_SSHLAB_PACKAGES` if needed) so it covers `internal/git`, `internal/app`, and any future internal packages.

```sh
# Syntax check all scripts & run parser/gate/hardening self-tests (offline, no container required)
sh -n scripts/sshlab/*.sh
sh scripts/sshlab/test-check-results.sh
sh scripts/sshlab/test-hardening.sh

# 1. Start (or reset fixture in) E1 Ubuntu 24.04
sh scripts/sshlab/up.sh e1

# 2. Verify environment, dynamically discover all TestConfiguredLinux* tests across ./internal/...,
#    and run them with BRANCHLINE_REQUIRE_LINUX=1, -p 1 -race -count=1 (0 SKIP, 0 FAIL required)
sh scripts/sshlab/test.sh e1

# 3. Export environment into current shell for ad-hoc runs (works with internal/sshtest.ConfiguredLinux)
eval "$(sh scripts/sshlab/env.sh e1)"
BRANCHLINE_REQUIRE_LINUX=1 go test -race -count=1 -run '^TestConfiguredLinux' ./internal/git

# 4. Tear down container & runtime keys/config (preserves JSON test logs & manifests)
sh scripts/sshlab/down.sh e1
```

Selecting `e2` (Ubuntu 20.04) or `e3` (Alpine):

```sh
sh scripts/sshlab/up.sh e2
sh scripts/sshlab/test.sh e2
sh scripts/sshlab/down.sh e2

sh scripts/sshlab/up.sh e3
sh scripts/sshlab/test.sh e3
sh scripts/sshlab/down.sh e3
```

> **Docker Daemon Prerequisite**: A working Docker daemon must already be running on the host; the scripts never start it. Every entrypoint runs a bounded preflight (`docker version`, 10 seconds) and exits with an error when the daemon is unreachable, asking the user to start Docker first. Connections use `127.0.0.1` loopback only, so no macOS Local Network permission or VM network is involved.

## 3. State Directory, SSH Isolation & `internal/sshtest` Integration

- **State Directory**: Defaults to `${TMPDIR:-/tmp}/branchline-sshlab/<env>` (override root directory via `BRANCHLINE_SSHLAB_STATE`).
- **Published sshd Port**: Each test container is launched with `-p 127.0.0.1::22`, publishing its sshd on a Docker-assigned ephemeral host port bound to loopback only. `docker port <container> 22/tcp` supplies the port, validated as a numeric `127.0.0.1` binding. Because Docker may assign a different host port when a container is recreated, `fault.sh <env> start` and `up.sh` re-read the port and refresh `ssh_config`, `known_hosts`, and `meta.env` on every start or reuse.
- **Disposable Keypair**: `up.sh` generates a per-environment Ed25519 keypair (`id_ed25519`, `id_ed25519.pub`) inside `${STATE_DIR}` and injects the public key into `/root/.ssh/authorized_keys` inside the container via `docker exec`.
- **Verified `known_hosts` (No `ssh-keyscan`, No `StrictHostKeyChecking=no`)**:
  1. `up.sh` inspects the container via `docker inspect`, verifies `.Name` and `.State.Status == "running"`, and reads the published `127.0.0.1` host port of TCP/22.
  2. `up.sh` reads `/etc/ssh/ssh_host_ed25519_key.pub` directly from inside the container via `docker exec`, validates the key type (`ssh-ed25519`), and writes `${STATE_DIR}/known_hosts` with a single line binding `${HOST_ALIAS}` to that exact host key.
  3. `${STATE_DIR}/ssh_config` pins `HostName 127.0.0.1`, `Port <published-port>`, `HostKeyAlias ${HOST_ALIAS}`, and `CheckHostIP no` (the published host port is not stable across rebuilds, so host-key verification is pinned to the alias rather than the socket address). It enforces `StrictHostKeyChecking yes`, `UserKnownHostsFile ${STATE_DIR}/known_hosts`, `GlobalKnownHostsFile /dev/null`, `IdentitiesOnly yes`, and `IdentityAgent none`.
- **Target-Scoped `ssh` Wrapper**:
  Because `git.SSHExecutor` invokes `ssh` from `PATH` with `BRANCHLINE_LINUX_SSH_HOST`, `up.sh` generates `${STATE_DIR}/bin/ssh` and `env.sh` prepends `${STATE_DIR}/bin` to `PATH`. The wrapper **only** applies `-F ${STATE_DIR}/ssh_config` when the target argument matches `${HOST_ALIAS}` (optionally `root@${HOST_ALIAS}`); all other SSH connections — notably `127.0.0.1` and `localhost` loopback sshd tests via `internal/sshtest.StartLoopback`, which must reach the real host and not this container's published port — pass through to `/usr/bin/ssh "$@"` untouched.
- **Zero User Side Effects**: `~/.ssh/config`, `~/.ssh/known_hosts`, and the user's `ssh-agent` are never read or modified. `test.sh` and `regression.sh` hash `~/.ssh/config` and `~/.ssh/known_hosts` before and after execution to verify zero mutation.
- **Disposable Fixture Reset**:
  Every `up.sh <env>` and `test.sh <env>` recreates `/tmp/branchline-test-<env>` from scratch on branch `main` with `a.txt` (`one\n`, commit `first`). Tests using `internal/sshtest.ConfiguredLinux(t)` create isolated sub-repositories under `$BRANCHLINE_LINUX_SSH_REPO` and clean them up in `t.Cleanup`.

## 4. Full Internal Regression & Zero-SKIP Gate (`regression.sh`)

```sh
sh scripts/sshlab/regression.sh e1
```

`regression.sh e1` executes the full internal regression and dynamic zero-SKIP gate in a single foreground command:

1. **Clean Disposable Instance & Trap Teardown**: Tears down any prior container for the active prefix/env, starts a fresh disposable container, generates fresh SSH keys/`known_hosts`/`ssh_config`, and registers a unified `EXIT INT TERM HUP` trap that calls `down.sh <env>` on exit (removing container, keys, and SSH config while preserving `${STATE_DIR}/logs/` and the exact non-zero exit code).
2. **Environment & Published-Port Preflight**: Loads `env.sh <env>`, exports `BRANCHLINE_REQUIRE_LINUX=1`, verifies the published sshd port (`BRANCHLINE_SSHLAB_PORT`, a numeric loopback host port), verifies `PATH` resolves `ssh` to the target-scoped wrapper, resets `/tmp/branchline-test-<env>`, and probes remote Linux + Git version.
3. **Static Analysis**: Runs `go vet ./internal/...` (or `BRANCHLINE_SSHLAB_PACKAGES`).
4. **Dynamic `TestConfiguredLinux*` Discovery (`manifest`)**: Runs `go test -json -list '^TestConfiguredLinux' ./internal/...` and invokes `scripts/sshlab/check-results.py manifest` to dynamically discover all `TestConfiguredLinux*` top-level tests and packages. Fails if 0 tests are discovered.
5. **Single Uncached Run**: Resets the remote fixture and runs `go test -json -p 1 -count=1 -timeout=20m ./internal/...` **exactly once** with `BRANCHLINE_REQUIRE_LINUX=1`.
6. **Dynamic Zero-SKIP / Zero-FAIL Result Gate (`check-results.py verify`)**: Verifies that:
   - The `go test` command exited `0`.
   - Every discovered `TestConfiguredLinux*` top-level test and every one of its subtests emitted `run` + `pass` with **0 `skip`** and **0 `fail`**, and was not served from `(cached)`.
   - All required packages passed, no unlisted `TestConfiguredLinux*` tests appeared outside the manifest, the JSON stream is complete/untruncated, and no ordinary test or package failed.

### Evidence & Log Paths Preserved After Teardown

`down.sh` (and the `regression.sh` cleanup trap) removes the container and runtime SSH keys/config while preserving logs and gate artifacts under `${STATE_DIR}/logs/` (default: `${TMPDIR:-/tmp}/branchline-sshlab/<env>/logs/`):

- **Targeted `ConfiguredLinux` Verification (`test.sh e1`)**:
  Runs each dynamically discovered `TestConfiguredLinux*` test with `-p 1 -race -count=1 -timeout=10m` and `BRANCHLINE_REQUIRE_LINUX=1`.
  - `${STATE_DIR}/logs/configured-linux-e1-manifest.json`
  - `${STATE_DIR}/logs/configured-linux-e1.json`
  - `${STATE_DIR}/logs/configured-linux-e1-gate.json`
  - `${STATE_DIR}/logs/configured-linux-e1.log`
- **Full Internal Regression (`regression.sh e1`)**:
  - `${STATE_DIR}/logs/regression-e1-manifest.json`
  - `${STATE_DIR}/logs/regression-e1.json`
  - `${STATE_DIR}/logs/regression-e1-gate.json`
  - `${STATE_DIR}/logs/regression-e1.log`

### Failure Diagnostics (`*-diag.log`)

When a test fails, the scripts capture a diagnostic snapshot **before the container is torn down** so an intermittent SSH or container-network failure can be investigated post-mortem:

- `test.sh` appends to `${STATE_DIR}/logs/configured-linux-<env>-diag.log` immediately after each failing `TestConfiguredLinux*` test (context label `test=<name> rc=<code>`) and prints the path on stderr.
- `regression.sh` writes `${STATE_DIR}/logs/regression-<env>-diag.log` when `go test` or the zero-SKIP gate exits non-zero (context label `regression go_rc=<n> gate_rc=<n>`), before the `EXIT` trap runs `down.sh`, and records the path in the summary log and stderr.

Each snapshot contains UTC/host timestamps and the context label, the `docker inspect` output, an in-container capture via `docker exec` (`ps` process table, `ss -tnp`/`netstat -tn` sockets, `ip addr`, `ip route`, the last 200 lines of `/tmp/sshd.log`), the host-side ControlMaster socket state for the environment's host alias, and a full `ssh -vvv -o BatchMode=yes -o ConnectTimeout=5 -o ControlMaster=no` probe handshake/timeout output.

Diagnostics are strictly observational:

- every capture runs under a bounded timeout (a hung or dead container cannot block cleanup);
- collection is fully fault-tolerant and never changes, masks, or retries the original non-zero exit code;
- fully green runs never produce a `*-diag.log`.

### ControlMaster Socket Cleanup

The app multiplexes SSH connections through `/tmp/branchline-<16hex>` sockets hashed from `(user, host, port, identity)` — notably **without** the published port — with `ControlPersist=600`. If a run is interrupted, a stale socket would pin later connections to a destroyed container's published port. `down.sh` (after teardown) and `up.sh` (before a fresh container claims the alias) remove exactly the stale sockets of the environment's own host alias (first asking ssh to exit the master with a 3-second bound, then deleting the socket file); sockets of any other host or unrelated `/tmp/branchline-*` files are never touched.

### Offline Verification of an Existing JSON Log & Manifest

```sh
sh scripts/sshlab/regression.sh --verify-only \
  --manifest /tmp/branchline-sshlab/e1/logs/regression-e1-manifest.json \
  --json /tmp/branchline-sshlab/e1/logs/regression-e1.json \
  --exit-code 0

python3 scripts/sshlab/check-results.py verify \
  --manifest /tmp/branchline-sshlab/e1/logs/configured-linux-e1-manifest.json \
  --json /tmp/branchline-sshlab/e1/logs/configured-linux-e1.json \
  --exit-code 0
```

### Rebuilding Images & Prefix Overrides

`up.sh` caches prepared local images (`bl-sshlab-e1:latest`, `bl-sshlab-e2:latest`, `bl-sshlab-e3:latest`) via `docker commit`. Reference OCI `Containerfile.e1`, `Containerfile.e2`, and `Containerfile.e3` are kept in `scripts/sshlab/`.

```sh
# Rebuild cached image from upstream distro image
sh scripts/sshlab/up.sh e1 --rebuild-image

# Remove both container and cached image
sh scripts/sshlab/down.sh e1 --purge-image

# Override container prefix (must start with bl-)
BRANCHLINE_SSHLAB_PREFIX=bl-local sh scripts/sshlab/up.sh e1
BRANCHLINE_SSHLAB_PREFIX=bl-local sh scripts/sshlab/test.sh e1
BRANCHLINE_SSHLAB_PREFIX=bl-local sh scripts/sshlab/down.sh e1
```

## 5. Fault Injection Entrypoints & Kernel Capability Boundaries

`scripts/sshlab/fault.sh <e1|e2|e3> <action>` provides scoped fault injection strictly limited to `sshlab` containers (external hosts are hard-blocked):

```sh
sh scripts/sshlab/fault.sh e1 status
sh scripts/sshlab/fault.sh e1 kill-sshd        # Kills sshd inside running container (TCP RST / Connection refused)
sh scripts/sshlab/fault.sh e1 start-sshd       # Restarts sshd inside running container
sh scripts/sshlab/fault.sh e1 restart-sshd     # Kills and restarts sshd
sh scripts/sshlab/fault.sh e1 iptables-drop    # Drops incoming TCP/22 packets (simulates network blackhole / timeout)
sh scripts/sshlab/fault.sh e1 iptables-reject  # Rejects incoming TCP/22 packets with TCP RST
sh scripts/sshlab/fault.sh e1 iptables-clear   # Flushes iptables INPUT rules
sh scripts/sshlab/fault.sh e1 stop             # Stops the container VM
sh scripts/sshlab/fault.sh e1 start            # Starts the stopped container VM and ensures sshd is running
```

### Kernel Support Notes (Docker)

| Mechanism | Supported? | Details |
| --- | --- | --- |
| `docker stop` / `start` | **Yes** | Full stop/start via `fault.sh <env> stop` / `start`; `start` re-reads the published port and refreshes `ssh_config`, `known_hosts`, and `meta.env`. |
| `kill-sshd` / `start-sshd` | **Yes** | Container PID 1 runs a lightweight supervisor loop so killing `sshd` does not terminate the container. |
| `iptables` (`DROP` / `REJECT`) | **Yes** | Containers are launched with `--cap-add NET_ADMIN --cap-add NET_RAW`; both `iptables-nft` and `iptables-legacy` work inside E1/E2/E3. |
| `tc netem` (latency / jitter) | **Yes** | With the `NET_ADMIN` capability, `tc qdisc ... netem` works inside the Docker guest kernel; `fault.sh <env> tc-delay <ms>` applies the delay and `tc-clear` removes it. |
