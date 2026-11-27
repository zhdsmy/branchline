#!/usr/bin/env python3
"""Emit deterministic Git fast-import data or create/check a local SSH perf fixture.

create always chooses a new /tmp directory. check is read-only. There is no
arbitrary create destination, reset, cleanup, SSH, or container command.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


FORMAT = "branchline.ssh-performance-fixture.v1"
COUNTS = (1000, 100000)
FILES = 1000
MANIFEST = "manifest.json"


def file_name(index):
    return f"tracked/group{index // 100:02d}/file{index:04d}.txt"


def file_content(index, version):
    return f"file {index:04d} version {version:06d}\n".encode()


def emit(output, count):
    def data(payload):
        output.write(f"data {len(payload)}\n".encode())
        output.write(payload)
        output.write(b"\n")

    output.write(b"feature done\n")
    for number in range(1, count + 1):
        output.write(f"commit refs/heads/main\nmark :{number}\n".encode())
        output.write(("committer Branchline SSH Performance <perf@example.invalid> "
                      f"{1700000000 + number} +0000\n").encode())
        data(f"SSH performance commit {number:06d}\n\nDeterministic fixture.\n".encode())
        if number > 1:
            output.write(f"from :{number - 1}\n".encode())
        indexes = range(FILES) if number == 1 else ((number - 2) % FILES,)
        for index in indexes:
            output.write(f"M 100644 inline {file_name(index)}\n".encode())
            data(file_content(index, number))
        output.write(b"\n")
    output.write(b"done\n")


def git_env():
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update({"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_TERMINAL_PROMPT": "0", "GIT_DEFAULT_HASH": "sha1",
                "GIT_NO_REPLACE_OBJECTS": "1", "LC_ALL": "C"})
    return env


def git(repo, *args, stdin=None):
    result = subprocess.run(
        ["git", "--no-optional-locks", "-c", "core.fsmonitor=false",
         "-c", "core.hooksPath=" + os.devnull, "-c", "core.autocrlf=false",
         "-c", "gc.auto=0", "-C", str(repo), *args],
        stdin=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        env=git_env(), timeout=180,
    )
    if result.returncode:
        raise ValueError(f"git {args[0]} exited {result.returncode}: "
                         + result.stderr.decode(errors="replace").strip())
    return result.stdout


def snapshot(repo, count):
    if repo.is_symlink() or not repo.is_dir():
        raise ValueError("fixture repository is missing or a symlink")
    top = Path(git(repo, "rev-parse", "--show-toplevel").decode().strip()).resolve()
    if top != repo.resolve():
        raise ValueError("Git top-level does not match the fixture repository")
    if git(repo, "symbolic-ref", "HEAD").strip() != b"refs/heads/main":
        raise ValueError("fixture HEAD is not on main")
    if int(git(repo, "rev-list", "--count", "HEAD")) != count:
        raise ValueError("unexpected commit count")
    if git(repo, "rev-list", "--merges", "HEAD").strip():
        raise ValueError("fixture history is not linear")
    head = git(repo, "rev-parse", "HEAD").decode().strip()
    refs = git(repo, "for-each-ref", "--format=%(refname) %(objectname)")
    if refs != f"refs/heads/main {head}\n".encode():
        raise ValueError("expected exactly refs/heads/main pointing to HEAD")
    roots = git(repo, "rev-list", "--max-parents=0", "HEAD").decode().splitlines()
    if len(head) != 40 or len(roots) != 1:
        raise ValueError("expected SHA-1 history with one root")
    names = [n for n in git(repo, "ls-files", "-z").split(b"\0") if n]
    expected = {file_name(i).encode() for i in range(FILES)}
    if len(names) != FILES or set(names) != expected:
        raise ValueError("unexpected tracked file names or count")
    git(repo, "diff-index", "--cached", "--quiet", "HEAD", "--")
    status = git(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
    entries = [e for e in status.split(b"\0") if e]
    if (len(entries) != FILES or any(e[:3] != b" M " for e in entries)
            or {e[3:] for e in entries} != expected):
        raise ValueError("expected exactly 1000 unstaged tracked modifications")
    digest = hashlib.sha256()
    for name in sorted(names):
        path = repo / name.decode()
        if path.is_symlink() or not path.is_file():
            raise ValueError("tracked worktree entry is missing or a symlink")
        body = path.read_bytes()
        digest.update(len(name).to_bytes(8, "big"))
        digest.update(name)
        digest.update(len(body).to_bytes(8, "big"))
        digest.update(body)
    git(repo, "fsck", "--full", "--no-reflogs")
    index = git(repo, "ls-files", "--stage", "-z")
    return {"commits": count, "tracked_files": len(names), "dirty_files": len(entries),
            "head": head, "root_commit": roots[0], "refs": {"refs/heads/main": head},
            "tree": git(repo, "rev-parse", "HEAD^{tree}").decode().strip(),
            "index_matches_head": True,
            "index_entries_sha256": hashlib.sha256(index).hexdigest(),
            "status_sha256": hashlib.sha256(status).hexdigest(),
            "worktree_sha256": digest.hexdigest()}


def create(count):
    root = Path(tempfile.mkdtemp(prefix="branchline-ssh-perf-", dir="/tmp")).resolve()
    repo = root / "repo"
    repo.mkdir()
    try:
        git(repo, "init", "-q")
        git(repo, "symbolic-ref", "HEAD", "refs/heads/main")
        with tempfile.TemporaryFile() as stream:
            emit(stream, count)
            stream.seek(0)
            digest = hashlib.sha256()
            while chunk := stream.read(1024 * 1024):
                digest.update(chunk)
            stream.seek(0)
            git(repo, "fast-import", "--quiet", stdin=stream)
        git(repo, "reset", "--hard", "refs/heads/main")
        for index in range(FILES):
            with (repo / file_name(index)).open("ab") as output:
                output.write(b"working tree change\n")
        manifest = {"format": FORMAT, "kind": "synthetic linear history",
                    "git_version": git(repo, "--version").decode().strip(),
                    "commits": count, "stream_sha256": digest.hexdigest(),
                    "snapshot": snapshot(repo, count)}
        (root / MANIFEST).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    except Exception:
        print(f"incomplete fixture retained at {root}", file=sys.stderr)
        raise
    return {"root": str(root), "manifest": str(root / MANIFEST), **manifest["snapshot"]}


def check(raw_root):
    root = Path(raw_root).resolve()
    if root.parent != Path("/tmp").resolve() or not root.name.startswith("branchline-ssh-perf-"):
        raise ValueError("expected a generated /tmp/branchline-ssh-perf-* root")
    manifest = json.loads((root / MANIFEST).read_text())
    if manifest.get("format") != FORMAT or manifest.get("commits") not in COUNTS:
        raise ValueError("unsupported fixture manifest")
    current = snapshot(root / "repo", manifest["commits"])
    if current != manifest.get("snapshot"):
        raise ValueError("fixture differs from its recorded snapshot")
    return {"root": str(root), "check": "PASS", **current}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("emit", "create"):
        command = commands.add_parser(name)
        command.add_argument("--commits", type=int, choices=COUNTS, required=True)
    command = commands.add_parser("check")
    command.add_argument("root")
    args = parser.parse_args()
    try:
        if args.command == "emit":
            emit(sys.stdout.buffer, args.commits)
        else:
            result = create(args.commits) if args.command == "create" else check(args.root)
            print(json.dumps(result, sort_keys=True))
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        print(f"perf-fixture: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
