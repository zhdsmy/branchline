#!/usr/bin/env python3
"""Dynamic TestConfiguredLinux* manifest generator and zero-SKIP result gate for Branchline sshlab."""

import argparse
from collections import Counter
from datetime import datetime, timezone
import json
import os
import re
import subprocess
import sys
from typing import Any, Dict, List, Optional, Set, Tuple

TEST_NAME_RE = re.compile(r"^TestConfiguredLinux[A-Za-z0-9_]*$")
CACHED_OK_RE = re.compile(r"^ok\s+\S+\s+\(cached\)")
RFC3339_RE = re.compile(
    r"^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$"
)
VALID_ACTIONS = {
    "start",
    "run",
    "pause",
    "cont",
    "pass",
    "bench",
    "fail",
    "output",
    "skip",
    "build-output",
    "build-fail",
}


def log_err(msg: str) -> None:
    sys.stderr.write(f"sshlab-gate: ERROR: {msg}\n")


def log_info(msg: str) -> None:
    sys.stderr.write(f"sshlab-gate: {msg}\n")


def parse_rfc3339(ts: str) -> datetime:
    m = RFC3339_RE.match(ts.strip())
    if not m:
        raise ValueError(f"invalid RFC3339 timestamp: {ts!r}")
    date_part, time_part, frac_part, tz_part = m.groups()
    if frac_part is None:
        micros = "000000"
    else:
        micros = (frac_part + "000000")[:6]
    if tz_part == "Z":
        tz_part = "+00:00"
    return datetime.fromisoformat(f"{date_part}T{time_part}.{micros}{tz_part}")


def now_rfc3339_micros() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="microseconds")


def run_cmd_capture(args: List[str], cwd: Optional[str] = None) -> bytes:
    proc = subprocess.run(
        args,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=True,
    )
    return proc.stdout


def inspect_repo_state(repo_root: str) -> Dict[str, Any]:
    abs_root = os.path.realpath(repo_root)
    head_commit = (
        run_cmd_capture(["git", "rev-parse", "HEAD"], cwd=abs_root)
        .decode("utf-8", errors="replace")
        .strip()
    )
    tree_hash = (
        run_cmd_capture(["git", "rev-parse", "HEAD^{tree}"], cwd=abs_root)
        .decode("utf-8", errors="replace")
        .strip()
    )
    branch = (
        run_cmd_capture(["git", "rev-parse", "--abbrev-ref", "HEAD"], cwd=abs_root)
        .decode("utf-8", errors="replace")
        .strip()
    )
    status_bytes = run_cmd_capture(
        ["git", "status", "--porcelain=v1"], cwd=abs_root
    )
    status_text = status_bytes.decode("utf-8", errors="replace")
    dirty = len(status_text.strip()) > 0

    go_version = (
        run_cmd_capture(["go", "version"], cwd=abs_root)
        .decode("utf-8", errors="replace")
        .strip()
    )
    go_env = (
        run_cmd_capture(["go", "env", "GOOS", "GOARCH"], cwd=abs_root)
        .decode("utf-8", errors="replace")
        .splitlines()
    )
    goos = go_env[0].strip() if len(go_env) > 0 else ""
    goarch = go_env[1].strip() if len(go_env) > 1 else ""

    return {
        "repo_root": abs_root,
        "head_commit": head_commit,
        "tree_hash": tree_hash,
        "branch": branch,
        "dirty": dirty,
        "go_version": go_version,
        "goos": goos,
        "goarch": goarch,
    }


def parse_json_lines(path: str) -> Tuple[List[Dict[str, Any]], List[str]]:
    errors: List[str] = []
    events: List[Dict[str, Any]] = []
    if not os.path.isfile(path):
        return [], [f"JSON file does not exist: {path}"]
    try:
        with open(path, "rb") as f:
            raw = f.read()
    except OSError as exc:
        return [], [f"failed to read JSON file {path}: {exc}"]

    if not raw or not raw.strip():
        return [], [f"JSON file is empty: {path}"]

    if not raw.endswith(b"\n"):
        errors.append(
            f"truncated JSON file {path}: last line does not end with newline"
        )

    lines = raw.splitlines()
    for idx, raw_line in enumerate(lines, start=1):
        line_str = raw_line.decode("utf-8", errors="replace").strip()
        if not line_str:
            errors.append(f"empty line in JSON stream {path} at line {idx}")
            continue
        try:
            obj = json.loads(line_str)
        except Exception as exc:
            errors.append(f"malformed JSON in {path} at line {idx}: {exc}")
            continue
        if not isinstance(obj, dict):
            errors.append(
                f"expected JSON object in {path} at line {idx}, got {type(obj).__name__}"
            )
            continue
        action = obj.get("Action")
        if not isinstance(action, str) or action not in VALID_ACTIONS:
            errors.append(
                f"invalid or missing Action {action!r} in {path} at line {idx}"
            )
            continue
        events.append(obj)

    return events, errors


def cmd_manifest(args: argparse.Namespace) -> int:
    list_exit_code = args.list_exit_code
    list_json_path = args.list_json
    temp_list_json: Optional[str] = None

    if not list_json_path:
        repo_cwd = args.repo_root or os.getcwd()
        pkgs = args.packages or ["./internal/..."]
        proc = subprocess.run(
            ["go", "test", "-json", "-list", "^TestConfiguredLinux", *pkgs],
            cwd=repo_cwd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        list_exit_code = proc.returncode
        if proc.stderr:
            sys.stderr.write(proc.stderr.decode("utf-8", errors="replace"))
        temp_list_json = f"{args.output}.list.tmp"
        with open(temp_list_json, "wb") as f:
            f.write(proc.stdout)
        list_json_path = temp_list_json

    try:
        events, parse_errors = parse_json_lines(list_json_path)
    finally:
        if temp_list_json and os.path.exists(temp_list_json):
            try:
                os.remove(temp_list_json)
            except OSError:
                pass

    errors: List[str] = list(parse_errors)
    if list_exit_code != 0:
        errors.append(
            f"go test -list exited with non-zero status {list_exit_code}"
        )

    pkg_started: Set[str] = set()
    pkg_status: Dict[str, str] = {}
    discovered: List[Tuple[str, str]] = []
    discovered_set: Set[Tuple[str, str]] = set()

    for ev in events:
        action = ev.get("Action")
        pkg = ev.get("Package")
        test_name = ev.get("Test")

        if action == "build-fail":
            failed_build = ev.get("FailedBuild") or pkg or "<unknown>"
            errors.append(f"build failed during test discovery: {failed_build}")
            continue

        if not test_name:
            if not isinstance(pkg, str) or not pkg:
                continue
            if action == "start":
                if pkg in pkg_started:
                    errors.append(
                        f"duplicate package start in -list output: {pkg}"
                    )
                pkg_started.add(pkg)
            elif action in ("pass", "fail", "skip"):
                pkg_status[pkg] = action
                if action == "fail":
                    errors.append(f"package failed during -list discovery: {pkg}")
            elif action == "output":
                out_text = ev.get("Output")
                if not isinstance(out_text, str):
                    continue
                for raw_line in out_text.splitlines():
                    candidate = raw_line.strip()
                    if candidate.startswith("TestConfiguredLinux"):
                        if not TEST_NAME_RE.match(candidate):
                            errors.append(
                                f"invalid TestConfiguredLinux identifier {candidate!r} in package {pkg}"
                            )
                            continue
                        pair = (pkg, candidate)
                        if pair in discovered_set:
                            errors.append(
                                f"duplicate TestConfiguredLinux test {candidate} in package {pkg}"
                            )
                            continue
                        discovered_set.add(pair)
                        discovered.append(pair)

    if not pkg_started:
        errors.append("no packages started in go test -list output")

    for pkg in sorted(pkg_started):
        if pkg not in pkg_status:
            errors.append(
                f"truncated go test -list output: package {pkg} did not finish"
            )

    for pkg, tname in discovered:
        status = pkg_status.get(pkg)
        if status != "pass":
            errors.append(
                f"package {pkg} containing {tname} did not pass -list (status={status!r})"
            )

    if not discovered:
        errors.append(
            "discovered 0 TestConfiguredLinux* tests across target packages (at least 1 required)"
        )

    repo_meta: Dict[str, Any] = {}
    if args.repo_root:
        try:
            repo_meta = inspect_repo_state(args.repo_root)
        except Exception as exc:
            errors.append(f"failed to inspect repository state at {args.repo_root}: {exc}")

    if errors:
        for err in errors:
            log_err(err)
        return 1

    discovered.sort(key=lambda item: (item[0], item[1]))
    packages = sorted({pkg for pkg, _ in discovered})

    manifest: Dict[str, Any] = {
        "schema_version": 1,
        "run_id": args.run_id or "",
        "created_at": args.created_at or now_rfc3339_micros(),
        "repo_root": repo_meta.get("repo_root", ""),
        "head_commit": repo_meta.get("head_commit", ""),
        "tree_hash": repo_meta.get("tree_hash", ""),
        "branch": repo_meta.get("branch", ""),
        "dirty": repo_meta.get("dirty", False),
        "go_version": repo_meta.get("go_version", ""),
        "goos": repo_meta.get("goos", ""),
        "goarch": repo_meta.get("goarch", ""),
        "ssh_env": args.ssh_env or "",
        "ssh_host": args.ssh_host or "",
        "ssh_repo": args.ssh_repo or "",
        "remote_git_version": args.remote_git_version or "",
        "packages": packages,
        "tests": [
            {"package": pkg, "test": tname} for pkg, tname in discovered
        ],
    }

    out_dir = os.path.dirname(os.path.abspath(args.output))
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2, sort_keys=False)
        f.write("\n")

    log_info(
        f"manifest written to {args.output}: {len(discovered)} TestConfiguredLinux* test(s) across {len(packages)} package(s): {', '.join(packages)}"
    )
    return 0


def load_and_validate_manifest(
    manifest_path: str,
) -> Tuple[Optional[Dict[str, Any]], List[Tuple[str, str]], List[str]]:
    errors: List[str] = []
    if not os.path.isfile(manifest_path):
        return None, [], [f"manifest file does not exist: {manifest_path}"]
    try:
        with open(manifest_path, "r", encoding="utf-8") as f:
            raw = f.read()
    except OSError as exc:
        return None, [], [f"failed to read manifest {manifest_path}: {exc}"]

    if not raw.strip():
        return None, [], [f"manifest file is empty: {manifest_path}"]

    try:
        manifest = json.loads(raw)
    except Exception as exc:
        return None, [], [f"malformed manifest JSON in {manifest_path}: {exc}"]

    if not isinstance(manifest, dict):
        return None, [], [f"manifest root must be a JSON object in {manifest_path}"]

    if manifest.get("schema_version") != 1:
        errors.append(
            f"unsupported or missing manifest schema_version: {manifest.get('schema_version')!r}"
        )

    raw_pkgs = manifest.get("packages")
    raw_tests = manifest.get("tests")

    if not isinstance(raw_pkgs, list) or not raw_pkgs:
        errors.append("manifest 'packages' must be a non-empty list")
        raw_pkgs = []

    if not isinstance(raw_tests, list) or not raw_tests:
        errors.append("manifest 'tests' must be a non-empty list (0 tests discovered)")
        raw_tests = []

    manifest_tests: List[Tuple[str, str]] = []
    seen_tests: Set[Tuple[str, str]] = set()
    pkgs_from_tests: Set[str] = set()

    for idx, entry in enumerate(raw_tests):
        if not isinstance(entry, dict):
            errors.append(f"manifest tests[{idx}] is not an object")
            continue
        pkg = entry.get("package")
        tname = entry.get("test")
        if not isinstance(pkg, str) or not pkg.strip():
            errors.append(f"manifest tests[{idx}] has invalid package {pkg!r}")
            continue
        if (
            not isinstance(tname, str)
            or "/" in tname
            or not TEST_NAME_RE.match(tname)
        ):
            errors.append(
                f"manifest tests[{idx}] has invalid top-level TestConfiguredLinux name {tname!r}"
            )
            continue
        pair = (pkg, tname)
        if pair in seen_tests:
            errors.append(f"duplicate test in manifest: {pkg} {tname}")
            continue
        seen_tests.add(pair)
        manifest_tests.append(pair)
        pkgs_from_tests.add(pkg)

    pkg_list_clean = [p for p in raw_pkgs if isinstance(p, str) and p.strip()]
    if len(pkg_list_clean) != len(raw_pkgs) or len(set(pkg_list_clean)) != len(
        pkg_list_clean
    ):
        errors.append("manifest 'packages' contains invalid or duplicate package names")

    if set(pkg_list_clean) != pkgs_from_tests:
        errors.append(
            f"manifest 'packages' {sorted(set(pkg_list_clean))} does not match packages in 'tests' {sorted(pkgs_from_tests)}"
        )

    return manifest, manifest_tests, errors


def cmd_packages(args: argparse.Namespace) -> int:
    manifest, _, errors = load_and_validate_manifest(args.manifest)
    if errors or manifest is None:
        for err in errors:
            log_err(err)
        return 1
    for pkg in manifest["packages"]:
        sys.stdout.write(f"{pkg}\n")
    return 0


def cmd_verify(args: argparse.Namespace) -> int:
    manifest, manifest_tests, manifest_errors = load_and_validate_manifest(
        args.manifest
    )
    events, json_errors = parse_json_lines(args.json)

    errors: List[str] = []
    errors.extend(manifest_errors)
    errors.extend(json_errors)

    manifest_created_dt: Optional[datetime] = None
    if manifest is not None:
        created_at_str = manifest.get("created_at")
        if created_at_str:
            if not isinstance(created_at_str, str):
                errors.append(f"manifest created_at must be a string, got {created_at_str!r}")
            else:
                try:
                    manifest_created_dt = parse_rfc3339(created_at_str)
                except ValueError as exc:
                    errors.append(f"invalid manifest created_at: {exc}")

        if args.run_id is not None:
            m_run_id = manifest.get("run_id", "")
            if m_run_id != args.run_id:
                errors.append(
                    f"manifest run_id mismatch: manifest has {m_run_id!r}, expected {args.run_id!r}"
                )

        if args.ssh_env is not None:
            m_env = manifest.get("ssh_env", "")
            if m_env != args.ssh_env:
                errors.append(
                    f"manifest ssh_env mismatch: manifest has {m_env!r}, expected {args.ssh_env!r}"
                )

        if args.ssh_host is not None:
            m_host = manifest.get("ssh_host", "")
            if m_host != args.ssh_host:
                errors.append(
                    f"manifest ssh_host mismatch: manifest has {m_host!r}, expected {args.ssh_host!r}"
                )

        if args.ssh_repo is not None:
            m_repo = manifest.get("ssh_repo", "")
            if m_repo != args.ssh_repo:
                errors.append(
                    f"manifest ssh_repo mismatch: manifest has {m_repo!r}, expected {args.ssh_repo!r}"
                )

        if args.repo_root:
            try:
                live_repo = inspect_repo_state(args.repo_root)
                for field in (
                    "head_commit",
                    "tree_hash",
                    "dirty",
                    "go_version",
                    "goos",
                    "goarch",
                ):
                    m_val = manifest.get(field)
                    l_val = live_repo.get(field)
                    if m_val != l_val:
                        errors.append(
                            f"manifest {field} mismatch with current repository: manifest has {m_val!r}, live has {l_val!r}"
                        )
            except Exception as exc:
                errors.append(
                    f"failed to verify repository state at {args.repo_root}: {exc}"
                )

    manifest_test_set: Set[Tuple[str, str]] = set(manifest_tests)
    manifest_pkgs: List[str] = (
        list(manifest.get("packages", [])) if manifest is not None else []
    )

    pkg_started: Set[str] = set()
    pkg_status: Dict[str, str] = {}
    pkg_cached: Set[str] = set()
    pkg_no_tests_warning: Set[str] = set()

    test_runs: Counter = Counter()
    test_passes: Counter = Counter()
    test_skips: Counter = Counter()
    test_fails: Counter = Counter()
    test_seen: Set[Tuple[str, str]] = set()

    min_event_time: Optional[datetime] = None
    max_event_time: Optional[datetime] = None

    for idx, ev in enumerate(events, start=1):
        action = ev.get("Action")
        pkg = ev.get("Package")
        test_name = ev.get("Test")
        time_str = ev.get("Time")

        if time_str is not None:
            if not isinstance(time_str, str):
                errors.append(f"invalid non-string Time at event #{idx}")
            else:
                try:
                    dt = parse_rfc3339(time_str)
                    if min_event_time is None or dt < min_event_time:
                        min_event_time = dt
                    if max_event_time is None or dt > max_event_time:
                        max_event_time = dt
                except ValueError as exc:
                    errors.append(f"invalid event Time at event #{idx}: {exc}")

        if action == "build-fail":
            failed_build = ev.get("FailedBuild") or pkg or "<unknown>"
            errors.append(f"package build failed: {failed_build}")
            continue

        if not test_name:
            if action == "build-output":
                continue
            if not isinstance(pkg, str) or not pkg.strip():
                errors.append(f"missing Package in package-level event #{idx} ({action})")
                continue
            if action == "start":
                if pkg in pkg_started:
                    errors.append(
                        f"duplicate package start event for {pkg} (possible concatenated log)"
                    )
                pkg_started.add(pkg)
            elif action == "output":
                out_text = ev.get("Output")
                if isinstance(out_text, str):
                    for line in out_text.splitlines():
                        if CACHED_OK_RE.match(line.strip()):
                            pkg_cached.add(pkg)
                        if "testing: warning: no tests to run" in line:
                            pkg_no_tests_warning.add(pkg)
            elif action in ("pass", "fail", "skip"):
                if pkg not in pkg_started:
                    errors.append(
                        f"package {pkg} emitted {action!r} without a prior 'start' event"
                    )
                if pkg in pkg_status:
                    errors.append(f"duplicate package completion event for {pkg}")
                pkg_status[pkg] = action
                if action == "fail":
                    errors.append(f"package failed: {pkg}")
            continue

        if not isinstance(pkg, str) or not pkg.strip():
            errors.append(
                f"missing Package for test event #{idx} (Test={test_name!r}, Action={action!r})"
            )
            continue
        if not isinstance(test_name, str) or not test_name.strip():
            errors.append(f"invalid Test field in event #{idx}")
            continue

        key = (pkg, test_name)
        top_test = test_name.split("/", 1)[0]
        is_cfg_linux = top_test.startswith("TestConfiguredLinux")

        if is_cfg_linux and (pkg, top_test) not in manifest_test_set:
            errors.append(
                f"unlisted TestConfiguredLinux test in JSON log not present in manifest: {pkg} {top_test} (event test={test_name})"
            )

        if action == "run":
            test_runs[key] += 1
            test_seen.add(key)
        elif action == "pass":
            test_passes[key] += 1
            test_seen.add(key)
        elif action == "skip":
            test_skips[key] += 1
            test_seen.add(key)
            if is_cfg_linux:
                errors.append(
                    f"SKIP detected in ConfiguredLinux test/subtest (zero SKIP required): {pkg} {test_name}"
                )
        elif action == "fail":
            test_fails[key] += 1
            test_seen.add(key)
            errors.append(f"FAIL detected in test: {pkg} {test_name}")
        elif action in ("pause", "cont", "output", "bench"):
            test_seen.add(key)

    if events:
        if not pkg_started:
            errors.append("JSON log contains no package 'start' events")
        if not pkg_status:
            errors.append("JSON log contains no package completion events")

        for pkg in sorted(pkg_started):
            if pkg not in pkg_status:
                errors.append(
                    f"truncated JSON log: package {pkg} started but never finished"
                )

        for pkg, tname in sorted(test_seen):
            key = (pkg, tname)
            if test_runs[key] < 1:
                errors.append(
                    f"test {pkg} {tname} had events without a 'run' event"
                )
            if test_passes[key] + test_skips[key] + test_fails[key] < 1:
                errors.append(
                    f"truncated JSON log: test {pkg} {tname} started ('run') but never completed"
                )

        if manifest_created_dt is not None:
            if min_event_time is None:
                errors.append(
                    "JSON log events are missing 'Time' timestamps required for anti-replay validation"
                )
            elif min_event_time < manifest_created_dt:
                errors.append(
                    f"stale JSON log: earliest event Time ({min_event_time.isoformat()}) is older than manifest created_at ({manifest_created_dt.isoformat()})"
                )

    for pkg in manifest_pkgs:
        if pkg not in pkg_started or pkg not in pkg_status:
            errors.append(f"missing required package from JSON log: {pkg}")
        elif pkg_status[pkg] != "pass":
            errors.append(
                f"required package {pkg} did not pass (status={pkg_status[pkg]!r})"
            )
        if pkg in pkg_cached:
            errors.append(
                f"required ConfiguredLinux package {pkg} was served from test cache '(cached)' instead of live execution"
            )
        if pkg in pkg_no_tests_warning:
            errors.append(
                f"required ConfiguredLinux package {pkg} reported 'no tests to run'"
            )

    passed_top_tests: List[str] = []
    passed_subtests: List[str] = []

    for pkg, req_test in manifest_tests:
        key = (pkg, req_test)
        if test_runs[key] != 1:
            errors.append(
                f"required top-level test {pkg} {req_test} expected run=1, got run={test_runs[key]}"
            )
        if test_passes[key] != 1:
            errors.append(
                f"required top-level test {pkg} {req_test} expected pass=1, got pass={test_passes[key]}"
            )
        if test_skips[key] > 0:
            errors.append(
                f"required top-level test {pkg} {req_test} was skipped"
            )
        if test_fails[key] > 0:
            errors.append(
                f"required top-level test {pkg} {req_test} failed"
            )
        if (
            test_runs[key] == 1
            and test_passes[key] == 1
            and test_skips[key] == 0
            and test_fails[key] == 0
        ):
            passed_top_tests.append(f"{pkg}:{req_test}")

        sub_prefix = req_test + "/"
        for spkg, stest in sorted(test_seen):
            if spkg == pkg and stest.startswith(sub_prefix):
                skey = (spkg, stest)
                if (
                    test_runs[skey] < 1
                    or test_passes[skey] < 1
                    or test_skips[skey] > 0
                    or test_fails[skey] > 0
                    or test_runs[skey] != test_passes[skey]
                ):
                    errors.append(
                        f"ConfiguredLinux subtest did not cleanly run+pass: {spkg} {stest} (run={test_runs[skey]}, pass={test_passes[skey]}, skip={test_skips[skey]}, fail={test_fails[skey]})"
                    )
                else:
                    passed_subtests.append(f"{spkg}:{stest}")

    if args.exit_code != 0:
        errors.append(
            f"go test command exited with non-zero status {args.exit_code}"
        )

    ordinary_skipped = [
        f"{pkg}:{tname}"
        for (pkg, tname), count in sorted(test_skips.items())
        if count > 0 and not tname.split("/", 1)[0].startswith("TestConfiguredLinux")
    ]

    status = "PASS" if not errors else "FAIL"
    report: Dict[str, Any] = {
        "status": status,
        "manifest_path": os.path.abspath(args.manifest),
        "json_path": os.path.abspath(args.json),
        "command_exit_code": args.exit_code,
        "required_packages": manifest_pkgs,
        "required_tests": [
            {"package": pkg, "test": tname} for pkg, tname in manifest_tests
        ],
        "passed_configured_linux_tests": passed_top_tests,
        "passed_configured_linux_subtests": passed_subtests,
        "ordinary_skipped_tests": ordinary_skipped,
        "total_packages_completed": len(pkg_status),
        "errors": errors,
    }

    if args.report_json:
        rep_dir = os.path.dirname(os.path.abspath(args.report_json))
        if rep_dir:
            os.makedirs(rep_dir, exist_ok=True)
        with open(args.report_json, "w", encoding="utf-8") as f:
            json.dump(report, f, indent=2, sort_keys=False)
            f.write("\n")

    if errors:
        log_err(
            f"FAIL: {len(errors)} gate error(s) detected (manifest={args.manifest}, json={args.json}):"
        )
        for err in errors:
            log_err(f"  - {err}")
        return 1

    log_info(
        f"PASS: {len(passed_top_tests)}/{len(manifest_tests)} TestConfiguredLinux* top-level test(s) (and {len(passed_subtests)} subtest(s)) across {len(manifest_pkgs)} package(s) passed with 0 SKIP, 0 FAIL"
    )
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Dynamic TestConfiguredLinux* manifest generator and zero-SKIP result gate."
    )
    sub = parser.add_subparsers(dest="subcommand", required=True)

    p_manifest = sub.add_parser(
        "manifest",
        help="Generate a dynamic TestConfiguredLinux* manifest from go test -json -list output.",
    )
    p_manifest.add_argument(
        "--list-json",
        help="Path to output of `go test -json -list '^TestConfiguredLinux' ./internal/...` (runs command if omitted).",
    )
    p_manifest.add_argument(
        "--packages",
        nargs="+",
        default=["./internal/..."],
        help="Go package patterns to scan when --list-json is omitted (default: ./internal/...).",
    )
    p_manifest.add_argument(
        "--list-exit-code",
        type=int,
        default=0,
        help="Exit code of the `go test -json -list` command.",
    )
    p_manifest.add_argument(
        "--output",
        required=True,
        help="Output path for generated manifest JSON.",
    )
    p_manifest.add_argument("--run-id", help="Unique run identifier.")
    p_manifest.add_argument(
        "--created-at",
        help="Override RFC3339 creation timestamp (for testing).",
    )
    p_manifest.add_argument(
        "--repo-root",
        help="Repository root directory to record commit/tree/dirty metadata.",
    )
    p_manifest.add_argument("--ssh-env", help="sshlab environment name (e.g. e1).")
    p_manifest.add_argument("--ssh-host", help="BRANCHLINE_LINUX_SSH_HOST value.")
    p_manifest.add_argument("--ssh-repo", help="BRANCHLINE_LINUX_SSH_REPO value.")
    p_manifest.add_argument(
        "--remote-git-version", help="Remote container Git version string."
    )

    p_pkgs = sub.add_parser(
        "packages",
        help="Print discovered packages from a manifest file (one per line).",
    )
    p_pkgs.add_argument("--manifest", required=True, help="Path to manifest JSON.")

    p_verify = sub.add_parser(
        "verify",
        help="Verify go test -json results against a dynamic TestConfiguredLinux* manifest.",
    )
    p_verify.add_argument("--manifest", required=True, help="Path to manifest JSON.")
    p_verify.add_argument(
        "--json", required=True, help="Path to go test -json output file."
    )
    p_verify.add_argument(
        "--exit-code",
        type=int,
        default=0,
        help="Actual exit code of the `go test` command.",
    )
    p_verify.add_argument(
        "--run-id",
        help="Require manifest run_id to match this value.",
    )
    p_verify.add_argument(
        "--repo-root",
        help="Require manifest commit/tree/dirty state to match this repository root.",
    )
    p_verify.add_argument(
        "--ssh-env",
        help="Require manifest ssh_env to match this value.",
    )
    p_verify.add_argument(
        "--ssh-host",
        help="Require manifest ssh_host to match this value.",
    )
    p_verify.add_argument(
        "--ssh-repo",
        help="Require manifest ssh_repo to match this value.",
    )
    p_verify.add_argument(
        "--report-json",
        help="Optional path to write structured gate verification report JSON.",
    )

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    if args.subcommand == "manifest":
        return cmd_manifest(args)
    if args.subcommand == "packages":
        return cmd_packages(args)
    if args.subcommand == "verify":
        return cmd_verify(args)
    return 64


if __name__ == "__main__":
    sys.exit(main())
