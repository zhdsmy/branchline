#!/usr/bin/env python3
"""Run the Go test suite sharded across parallel processes.

Uses only the Python 3 standard library. Run from the repository root:

    python3 scripts/test-shards.py [--race] [--shards N] [--timeout 30m]
        [--split PKG ...] [--keep-logs] [PKG ...]

The package patterns default to ./... and are expanded with `go list`.
Every package's top-level test names are collected with `go test -list '.*'`.
Packages named by --split (default ./internal/app) are divided into --shards
processes (default 4) by round-robin over the sorted test names, each shard
running `go test -run '^(name1|name2|...)$'`; every other package runs as one
process. All processes start at the same time, write `go test -json` output
to per-process log files in a temporary directory, and run with the default
SIGHUP disposition (this script is often invoked from environments where
SIGHUP is ignored, and tests must not inherit that).

After all processes finish, every listed test must have reported a result
(pass, fail or skip); a test with no result — the process crashed, timed out
or was killed — is a failure and is listed by name. The summary prints one
line per process, every failed test name, every missing test name, the
skipped tests, and a final PASS/FAIL line. Exit code 0 means everything
passed. On failure (or interruption) the log directory is kept and its path
printed; on success it is removed unless --keep-logs is given.

On Ctrl-C or SIGTERM only the child processes this script started are
terminated, each in its own process group; no process is matched by name.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

# Python resets signals it ignores itself for child processes, but a SIGHUP
# ignored by a parent of this script is inherited as ignored. This wrapper
# restores the default disposition and execs `go`, so the test binaries see
# the default SIGHUP no matter how the script itself was started. The exec
# keeps the PID and process group of the wrapper, so terminating the child's
# process group still reaches the whole tree.
EXEC_WRAPPER = ("import os,signal,sys;"
                "signal.signal(signal.SIGHUP, signal.SIG_DFL);"
                "os.execvpe(sys.argv[1], sys.argv[1:], os.environ)")


class ShardsError(Exception):
    """A setup problem that must fail the run before tests start."""


class Interrupted(Exception):
    """Ctrl-C or SIGTERM: child process groups have already been terminated."""


def expand_packages(patterns, required):
    """Expand `go list` patterns into sorted unique package import paths.

    A pattern that matches nothing makes `go list` exit non-zero. For the
    main package set that is fatal; for --split patterns it only means there
    is nothing to split, so a warning is printed and the empty set returned.
    """
    try:
        result = subprocess.run(
            [sys.executable, "-c", EXEC_WRAPPER, "go", "list"] + list(patterns),
            capture_output=True, text=True, timeout=300)
    except (OSError, subprocess.TimeoutExpired) as exc:
        if required:
            raise ShardsError(f"failed to run `go list`: {exc}")
        return set()
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        if required:
            raise ShardsError(f"`go list {' '.join(patterns)}` failed: {detail}")
        print(f"test-shards: warning: `go list {' '.join(patterns)}` matched nothing "
              f"({detail}); nothing will be split", file=sys.stderr)
        return set()
    packages = []
    for line in result.stdout.splitlines():
        line = line.strip()
        if line and line not in packages:
            packages.append(line)
    if required and not packages:
        raise ShardsError(f"`go list {' '.join(patterns)}` matched no packages")
    return set(packages)


def go_test_args(race, extra):
    args = ["test", "-count=1"]
    if race:
        args.append("-race")
    return args + extra


def list_tests(packages, race):
    """List the top-level test names of every package, in parallel.

    Returns {package: (sorted names, error or None)}. Only lines starting
    with "Test" are test names; `go test -list` may also print examples,
    fuzz targets and the trailing `ok` line.
    """
    def one(package):
        args = go_test_args(race, ["-list", ".*", package])
        try:
            result = subprocess.run(
                [sys.executable, "-c", EXEC_WRAPPER, "go"] + args,
                capture_output=True, text=True, timeout=600)
        except (OSError, subprocess.TimeoutExpired) as exc:
            return package, [], f"failed to run `go test -list`: {exc}"
        if result.returncode != 0:
            detail = (result.stderr.strip() or result.stdout.strip())[:500]
            return package, [], f"`go test -list` exited {result.returncode}: {detail}"
        names = sorted(line.strip() for line in result.stdout.splitlines()
                       if line.startswith("Test"))
        return package, names, None

    workers = min(len(packages), 8) or 1
    with ThreadPoolExecutor(max_workers=workers) as pool:
        results = list(pool.map(one, sorted(packages)))
    return {package: (names, error) for package, names, error in results}


def assign_shards(names, count):
    """Round-robin the sorted names over `count` shards.

    Returns a list of at most `count` non-empty shard name lists; shard i
    gets every i-th name of the sorted order, so every listed test is in
    exactly one shard.
    """
    shards = []
    for index in range(min(count, len(names))):
        shard = names[index::count]
        if shard:
            shards.append(shard)
    return shards


def run_pattern(names):
    """A `go test -run` pattern matching exactly the given test names."""
    return "^(" + "|".join(re.escape(name) for name in names) + ")$"


def sanitize(package):
    return re.sub(r"[^A-Za-z0-9._-]+", "_", package)


class Job:
    """One test process: a whole package or one shard of a split package."""

    def __init__(self, package, names, shard_index, shard_count, log_path):
        self.package = package
        self.names = list(names)      # tests this process must report
        self.shard_index = shard_index  # None for whole-package runs
        self.shard_count = shard_count
        self.log_path = log_path
        self.process = None
        self.returncode = None
        self.elapsed = 0.0

    def label(self):
        if self.shard_index is None:
            return self.package
        return f"{self.package} shard {self.shard_index}/{self.shard_count}"

    def argv(self, race, timeout):
        args = go_test_args(race, ["-json", "-timeout", timeout])
        if self.shard_index is not None:
            args += ["-run", run_pattern(self.names)]
        return args + [self.package]

    def start(self, race, timeout):
        self.log_file = open(self.log_path, "w")
        self.started = time.monotonic()
        # start_new_session: every child is its own process group leader, so
        # os.killpg(pid) terminates exactly this child and its descendants.
        self.process = subprocess.Popen(
            [sys.executable, "-c", EXEC_WRAPPER, "go"] + self.argv(race, timeout),
            stdout=self.log_file, stderr=subprocess.STDOUT,
            start_new_session=True)

    def wait(self):
        self.returncode = self.process.wait()
        self.elapsed = time.monotonic() - self.started
        self.log_file.close()

    def terminate_group(self):
        if self.process is not None and self.process.poll() is None:
            try:
                os.killpg(self.process.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass


def parse_events(text):
    """Parse `go test -json` output into test results.

    Returns (passed, failed, skipped, package_failed): sets of test names
    with a final result event — including subtests, which carry a "/" in
    their name — and whether the package itself failed. Non-JSON lines
    (crash trailers, build errors) carry no events and are ignored here;
    they stay in the log file.
    """
    passed, failed, skipped = set(), set(), set()
    package_failed = False
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            event = json.loads(line)
        except ValueError:
            continue
        action = event.get("Action")
        test = event.get("Test")
        if test:
            if action == "pass":
                passed.add(test)
            elif action == "fail":
                failed.add(test)
            elif action == "skip":
                skipped.add(test)
        elif action == "fail":
            package_failed = True
    return passed, failed, skipped, package_failed


def build_jobs(packages, split_packages, listed, shard_count, log_dir):
    """Plan the test processes: shards for split packages, whole otherwise."""
    jobs = []
    for package in sorted(packages):
        names, error = listed[package]
        if package in split_packages and not error and names:
            shards = assign_shards(names, shard_count)
            for index, shard in enumerate(shards, 1):
                log_path = log_dir / f"{sanitize(package)}.shard{index}of{len(shards)}.log"
                jobs.append(Job(package, shard, index, len(shards), log_path))
        else:
            log_path = log_dir / f"{sanitize(package)}.log"
            jobs.append(Job(package, names, None, 0, log_path))
    return jobs


def run_jobs(jobs, race, timeout):
    """Start all jobs at once and wait for them.

    On SIGTERM or Ctrl-C, terminate the process groups of the children this
    script started — never processes matched by name — and raise Interrupted.
    """
    interrupted = []

    def handler(signum, frame):
        interrupted.append(signum)
        for job in jobs:
            job.terminate_group()

    old_term = signal.signal(signal.SIGTERM, handler)
    old_int = signal.signal(signal.SIGINT, handler)
    try:
        for job in jobs:
            if interrupted:
                break
            job.start(race, timeout)
        for job in jobs:
            if job.process is not None:
                job.wait()
    finally:
        signal.signal(signal.SIGTERM, old_term)
        signal.signal(signal.SIGINT, old_int)
    if interrupted:
        raise Interrupted()


def main():
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--race", action="store_true",
                        help="pass -race to every go test invocation")
    parser.add_argument("--shards", type=int, default=4, metavar="N",
                        help="number of shards per split package (default 4)")
    parser.add_argument("--timeout", default="30m", metavar="DURATION",
                        help="go test -timeout value (default 30m)")
    parser.add_argument("--split", nargs="+", default=None, metavar="PKG",
                        help="packages to shard by test name "
                             "(default ./internal/app); put positional PKG "
                             "arguments before this flag")
    parser.add_argument("--keep-logs", action="store_true",
                        help="keep the per-process log directory even on success")
    parser.add_argument("packages", nargs="*", metavar="PKG",
                        help="package patterns to test (default ./...)")
    arguments = parser.parse_args()

    if arguments.shards < 1:
        print("test-shards: --shards must be at least 1", file=sys.stderr)
        return 1

    patterns = arguments.packages or ["./..."]
    split_patterns = arguments.split if arguments.split is not None else ["./internal/app"]

    try:
        packages = expand_packages(patterns, required=True)
        split_packages = expand_packages(split_patterns, required=False) & packages
    except ShardsError as error:
        print(f"test-shards: {error}", file=sys.stderr)
        return 1

    print(f"test-shards: listing tests of {len(packages)} packages"
          + (" (race)" if arguments.race else ""))
    listed = list_tests(packages, arguments.race)
    list_errors = {pkg: error for pkg, (names, error) in listed.items() if error}
    for package in sorted(list_errors):
        print(f"test-shards: warning: {package}: cannot list tests "
              f"({list_errors[package]}); running the package whole, "
              "individual results cannot be verified", file=sys.stderr)

    log_dir = Path(tempfile.mkdtemp(prefix="branchline-test-shards-"))
    jobs = build_jobs(packages, split_packages, listed, arguments.shards, log_dir)
    total = sum(len(job.names) for job in jobs)
    shards_note = ""
    if split_packages:
        shards_note = (f"; splitting {', '.join(sorted(split_packages))} "
                       f"into {arguments.shards} shards")
    print(f"test-shards: running {len(jobs)} processes, {total} tests{shards_note}")

    started = time.monotonic()
    try:
        run_jobs(jobs, arguments.race, arguments.timeout)
    except Interrupted:
        print(f"test-shards: interrupted; child process groups terminated; "
              f"logs kept: {log_dir}")
        return 1
    wall = time.monotonic() - started

    # Collect results: every test assigned to a process must have reported
    # pass, fail or skip; anything else missing means its process crashed,
    # timed out or was killed before reporting it.
    missing = []
    failed_names = []
    skipped_names = []
    process_failures = 0
    for job in sorted(jobs, key=lambda j: (j.package, j.shard_index or 0)):
        print(f"{job.label()}: {len(job.names)} tests, "
              f"exit {job.returncode}, {job.elapsed:.1f} s")
        if job.returncode != 0:
            process_failures += 1
        try:
            text = job.log_path.read_text(errors="replace")
        except OSError:
            text = ""
        passed, failed, skipped, package_failed = parse_events(text)
        reported = passed | failed | skipped
        for name in job.names:
            if name not in reported:
                missing.append(f"{job.package}.{name}")
        failed_names.extend(f"{job.package}.{name}" for name in sorted(failed))
        skipped_names.extend(f"{job.package}.{name}" for name in sorted(skipped))

    if failed_names:
        print("--- FAIL tests:")
        for name in failed_names:
            print(f"  {name}")
    if missing:
        print("--- missing results (listed but never reported):")
        for name in sorted(missing):
            print(f"  {name}")
    if skipped_names:
        print(f"--- SKIP ({len(skipped_names)}):")
        for name in skipped_names:
            print(f"  {name}")

    ok = not failed_names and not missing and process_failures == 0
    if ok:
        print(f"PASS {total} tests in {wall:.1f} s")
        if arguments.keep_logs:
            print(f"logs kept: {log_dir}")
        else:
            shutil.rmtree(log_dir, ignore_errors=True)
        return 0

    parts = []
    if failed_names:
        parts.append(f"{len(failed_names)} failed")
    if missing:
        parts.append(f"{len(missing)} missing")
    if process_failures:
        parts.append(f"{process_failures} process failures")
    detail = f" ({', '.join(parts)})" if parts else ""
    print(f"FAIL of {total} tests in {wall:.1f} s{detail}")
    print(f"logs kept: {log_dir}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
