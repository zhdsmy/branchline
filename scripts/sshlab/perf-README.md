# SSH performance tools: controlled RTT proxy & deterministic repository fixture

This document describes two independent, composable tools for measuring SSH-dependent behavior under controlled network conditions:

- `scripts/sshlab/perf-proxy.py` — a user-space TCP pass-through proxy that adds a controlled round-trip time (RTT) to connections toward a disposable sshlab container;
- `scripts/sshlab/perf-fixture.py` — a generator/checker for deterministic synthetic Git repositories of exactly 1,000 or 100,000 commits.

Neither tool requires the other; both are usable standalone offline of any benchmark harness. Prerequisite for container-side use: a working Docker daemon must already be running on the host; these tools never start it.

---

## 1. Fixture nature & boundary

- **Format**: `branchline.ssh-performance-fixture.v1` (`scripts/sshlab/perf-fixture.py`, protocol specification in `scripts/sshlab/perf-fixture.md`).
- **Synthetic linear history**: Both repository scales (`1,000` commits and `100,000` commits) are **deterministic synthetic linear histories** with `1,000` tracked files (`tracked/group00/file0000.txt` .. `tracked/group09/file0999.txt`) and `1,000` unstaged working-tree modifications (`working tree change\n` appended to every tracked file while `.git/index` matches `HEAD^{tree}`). They must **never** be described as `git/git` or a real-world open-source project.
- **Out-of-band preparation & verification**: All fixture creation (`perf-fixture.py emit` piped to remote `git fast-import`), working-tree modification setup, and independent Git verification (`commits`, `HEAD`, `root_commit`, `tree`, `refs == {"refs/heads/main": HEAD}`, `index_entries_sha256`, `status_sha256`, `git fsck --full --no-reflogs`) execute outside any timing windows inside an isolated remote directory (`/tmp/bl-perf-bench.XXXXXXXX`) on an owned sshlab container. The standard sshlab root fixture (`/tmp/branchline-test-e1`) is never modified.

---

## 2. Controlled TCP latency proxy (`scripts/sshlab/perf-proxy.py`)

### 2.1 Stream availability delay model

`scripts/sshlab/perf-proxy.py` implements a user-space TCP pass-through proxy on `127.0.0.1:<ephemeral_port>` forwarding to a verified non-loopback IPv4 target. It is kernel-independent of the runtime in use. Under Docker, sshlab sshd is reachable only through the published `127.0.0.1` host port, which the proxy rejects by design (`--target-host` must be a non-loopback literal IPv4 address), so the proxy cannot front a Docker sshlab container; for RTT simulation against sshlab containers, use in-container `tc netem` instead — with the `NET_ADMIN` capability the Docker guest kernel **does** support `tc qdisc ... netem` (verified on the sshlab environments; see `scripts/sshlab/README.md`). The proxy model itself:

1. **End-to-end SSH integrity**: The proxy forwards raw TCP byte streams without inspecting, modifying, or synthesizing SSH frames. OpenSSH client-to-server encryption, user public-key authentication (`id_ed25519`), and strict `ssh-ed25519` host-key verification (`StrictHostKeyChecking=yes` against the verified `known_hosts`) remain completely end-to-end.
2. **Symmetric one-way propagation schedule (`rtt_ms / 2` per direction)**:
   - Each TCP connection spawns two independent directional pipelines (`c2s`: client → server, and `s2c`: server → client), each with an `asyncio.Queue` decoupling its reader coroutine from its writer coroutine.
   - When the reader coroutine receives a chunk (up to 64 KiB) from the OS socket at monotonic timestamp `t_read`, it immediately stamps the chunk's target availability time `t_release = max(t_read + RTT/2, t_prev_release)` and enqueues `(t_release, data)` without sleeping.
   - The writer coroutine awaits `max(0, t_release - t_now)` and coalesces any subsequent queued chunks whose `t_release <= t_now` into the same socket drain.
   - Because reading never blocks on `sleep`, a continuous multi-chunk stream (such as `git log -n 2000` or `git status` across 1,000 dirty files) is shifted in availability by a single one-way propagation delay (`RTT/2`) per direction rather than accumulating serial per-chunk sleep delays.

### 2.2 Timing precision, bandwidth, and queueing boundaries

- **Clock source**: `perf-proxy.py` uses the macOS monotonic clock (`time.monotonic_ns()`).
- **Scheduler resolution**: User-space `asyncio` timers on macOS typically exhibit `0.5–3.5 ms` wake-up jitter per round trip.
- **Bandwidth & queueing limitations**: The proxy models a pure propagation delay (`one_way_ms = rtt_ms / 2`) over local loopback + virtual bridge sockets without artificial bandwidth throttling or random packet loss. Under small-to-medium payloads (`< 512 KiB` per Git command), throughput is bounded by local loopback/virtio socket buffers rather than a WAN bottleneck link.

### 2.3 Calibration boundary

Passing `--rtt-ms 100` on the command line is never proof of effective latency. Any measurement through the proxy must be calibrated independently: open a non-multiplexed SSH session (`ControlMaster=no`, `ControlPath=none`) through the proxy, complete one warmup line after authentication, and measure sequential application-layer round-trip echo probes (for example `while IFS= read -r line; do printf "%s\n" "$line"; done` with a series of input lines). Compare the median probed RTT against an unproxied session on the same container and treat the difference as the actual added latency. A measured median outside the intended band invalidates the measurement, never the reverse.

### 2.4 Standalone usage

```sh
# Terminal 1: forward loopback port 2222 to the container sshd with 100ms added RTT (Ctrl-C to stop).
# The target must be a non-loopback IPv4 address; for a non-loopback-reachable
# target (for example a VM or remote host running sshlab-style sshd) pass its
# literal address and sshd port.
python3 scripts/sshlab/perf-proxy.py \
  --target-host <non-loopback-ipv4> --target-port 22 \
  --listen-port 2222 --rtt-ms 100

# Terminal 2: use the proxied endpoint with strict host-key verification
ssh -p 2222 -F <sshlab-state>/e1/ssh_config -o HostKeyAlias=<container-name> root@127.0.0.1 uname -s
```

`--target-host` must be a literal non-loopback IPv4 address — host names and aliases are rejected. `--events-file <path>` records one JSON object per line for every TCP accept/connect/close event with timestamps, which independently confirms how many new TCP connections a client operation opened. `--ready-file <path>` is atomically written with the bound listen address once the proxy is accepting. Automated callers that hold a stdin pipe open can pass `--exit-on-stdin-eof` so the proxy exits automatically if the parent terminates (do not combine it with `</dev/null`, which closes stdin immediately).

---

## 3. Deterministic repository fixture (`scripts/sshlab/perf-fixture.py`)

The full data contract is specified in `scripts/sshlab/perf-fixture.md`. Standalone usage:

```sh
# Create a fresh 1,000-commit fixture under /tmp/branchline-ssh-perf-* (local only)
python3 scripts/sshlab/perf-fixture.py create --commits 1000

# Create a 100,000-commit fixture
python3 scripts/sshlab/perf-fixture.py create --commits 100000

# Read-only verification of an existing fixture
python3 scripts/sshlab/perf-fixture.py check /tmp/branchline-ssh-perf-<created-suffix>

# Emit the raw git fast-import stream (e.g. to import inside a container)
python3 scripts/sshlab/perf-fixture.py emit --commits 1000 > /tmp/perf-1000.fi

# Offline self-test of the generator/checker (no SSH, no container)
python3 scripts/sshlab/test-perf-fixture.py
```

For a remote fixture inside a disposable sshlab container, import the `emit` stream with `git fast-import` in a fresh task-owned repository under `/tmp`, select `main` as the checkout branch, then append `working tree change\n` to every tracked file without staging — exactly as the data contract requires. Fixture preparation and independent Git verification happen entirely outside any timing window.
