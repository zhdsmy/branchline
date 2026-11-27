#!/usr/bin/env python3
"""Local protocol regressions; no SSH/container or performance run."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest


SCRIPT = Path(__file__).with_name("perf-fixture.py")


class FixtureProtocolTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "create", "--commits", "1000"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=60)
        cls.created = json.loads(result.stdout)
        cls.root = Path(cls.created["root"]).resolve()
        if (cls.root.parent != Path("/tmp").resolve()
                or not cls.root.name.startswith("branchline-ssh-perf-")):
            raise ValueError("create returned a nonfixture directory")
        cls.repo = cls.root / "repo"
        cls.addClassCleanup(shutil.rmtree, cls.root)

    def check_fixture(self):
        return subprocess.run(
            [sys.executable, str(SCRIPT), "check", str(self.root)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)

    def assert_pass(self):
        result = self.check_fixture()
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(json.loads(result.stdout)["check"], "PASS")

    def file_bytes(self):
        return {str(path.relative_to(self.root)): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in self.root.rglob("*") if path.is_file()}

    def git(self, *args):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        return subprocess.run(
            ["git", "--no-optional-locks", "-C", str(self.repo), *args],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env,
            check=True, timeout=60).stdout

    def test_fresh_fixture_check_is_read_only(self):
        before = self.file_bytes()
        self.assert_pass()
        self.assertEqual(self.file_bytes(), before)

    def test_extra_refs_are_rejected_then_recover(self):
        self.assert_pass()
        for ref in ("refs/heads/unexpected", "refs/tags/unexpected",
                    "refs/remotes/origin/unexpected"):
            with self.subTest(ref=ref):
                self.git("update-ref", ref, self.created["head"])
                try:
                    before = self.file_bytes()
                    rejected = self.check_fixture()
                    self.assertEqual(rejected.returncode, 1, rejected.stdout.decode())
                    self.assertIn(b"expected exactly refs/heads/main", rejected.stderr)
                    self.assertEqual(self.file_bytes(), before)
                finally:
                    self.git("update-ref", "-d", ref)
                self.assert_pass()


if __name__ == "__main__":
    unittest.main()
