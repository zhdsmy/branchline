#!/usr/bin/env python3
"""Tests for scripts/test-shards.py.

Uses only the Python 3 standard library. The end-to-end tests build a small
self-contained Go module in a temporary directory — passing tests, a skipped
test, a failing test, one that kills its process with os.Exit, and one that
checks SIGHUP is not ignored — and run scripts/test-shards.py on it. The
module has no external dependencies and never touches the network. TMPDIR
is redirected into the temporary directory so log directories never leak.
"""

import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT_DIR = Path(__file__).resolve().parent

spec = importlib.util.spec_from_file_location(
    "test_shards", SCRIPT_DIR / "test-shards.py")
shards = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shards)

SCRIPT = SCRIPT_DIR / "test-shards.py"

GO_MOD = """\
module example.com/shardtoy

go 1.21
"""

PKG_OK = """\
package pkgok

import (
	"os/signal"
	"syscall"
	"testing"
)

func TestAlpha(t *testing.T) {}

func TestBeta(t *testing.T) { t.Skip("beta skipped") }

func TestGamma(t *testing.T) {}

// Test binaries must see the default SIGHUP disposition even when the
// script's own parent ignores it; several tests in the real suite depend
// on a HUP actually terminating a process.
func TestHupDefault(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Fatal("SIGHUP is ignored")
	}
}
"""

PKG_FAIL = """\
package pkgfail

import "testing"

func TestBad(t *testing.T) { t.Fatal("bad failure") }

func TestGood(t *testing.T) {}
"""

# TestSurvivor is in a file sorting before the crashing one, so it always
# runs and reports before os.Exit kills the test binary.
PKG_CRASH_SURVIVOR = """\
package pkgcrash

import "testing"

func TestSurvivor(t *testing.T) {}
"""

PKG_CRASH_CRASH = """\
package pkgcrash

import (
	"os"
	"testing"
)

func TestCrash(t *testing.T) { os.Exit(1) }
"""

# TestShardAA starts with TestShardA: a -run pattern that is not anchored
# and exact would run TestShardAA in TestShardA's shard too, and the
# union-exactly-once check would catch it.
PKG_SHARD = """\
package pkgshard

import "testing"

func TestShardA(t *testing.T)  {}
func TestShardAA(t *testing.T) {}
func TestShardB(t *testing.T)  {}
func TestShardC(t *testing.T)  {}
func TestShardD(t *testing.T)  {}
func TestShardE(t *testing.T)  {}
"""

PKG_FILES = {
    "pkgok/ok_test.go": PKG_OK,
    "pkgfail/fail_test.go": PKG_FAIL,
    "pkgcrash/a_survivor_test.go": PKG_CRASH_SURVIVOR,
    "pkgcrash/z_crash_test.go": PKG_CRASH_CRASH,
    "pkgshard/shard_test.go": PKG_SHARD,
}


def write_file(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


class ToyModuleCase(unittest.TestCase):
    """A temporary Go module with the fixture packages above."""

    def setUp(self):
        self.tempdir = tempfile.mkdtemp(prefix="test-shards-test-")
        self.addCleanup(shutil.rmtree, self.tempdir, True)
        self.module = Path(self.tempdir) / "toy"
        self.module.mkdir()
        write_file(self.module / "go.mod", GO_MOD)
        for relative, content in PKG_FILES.items():
            write_file(self.module / relative, content)
        # Log directories go inside the test tempdir so nothing leaks even
        # when a run keeps them.
        self.log_tmp = Path(self.tempdir) / "tmp"
        self.log_tmp.mkdir()

    def run_script(self, *args, ignore_sighup=False):
        """Run scripts/test-shards.py on the toy module.

        With ignore_sighup, the script is started through a wrapper that
        ignores SIGHUP first, simulating a parent process (or harness) that
        leaves SIGHUP ignored; the test binaries must still see the default
        disposition.
        """
        env = dict(os.environ)
        env["TMPDIR"] = str(self.log_tmp)
        command = [sys.executable, str(SCRIPT)] + list(args)
        if ignore_sighup:
            wrapper = ("import os,signal,sys;"
                       "signal.signal(signal.SIGHUP, signal.SIG_IGN);"
                       "os.execvpe(sys.argv[1], sys.argv[1:], os.environ)")
            command = [sys.executable, "-c", wrapper] + command
        return subprocess.run(command, cwd=self.module, env=env,
                              capture_output=True, text=True, timeout=600)

    def kept_log_dirs(self):
        return sorted(self.log_tmp.glob("branchline-test-shards-*"))


class HelpersTest(unittest.TestCase):
    def test_assign_shards_disjoint_complete_sorted(self):
        names = sorted(f"TestN{i}" for i in range(10))
        result = shards.assign_shards(names, 3)
        self.assertEqual(len(result), 3)
        union = sorted(name for shard in result for name in shard)
        self.assertEqual(union, names, "shards must partition the names")
        for shard in result:
            self.assertEqual(shard, sorted(shard), "shards keep sorted order")
            self.assertTrue(shard, "no empty shards")

    def test_assign_shards_fewer_names_than_shards(self):
        result = shards.assign_shards(["TestA", "TestB"], 4)
        self.assertEqual(result, [["TestA"], ["TestB"]])

    def test_assign_shards_round_robin(self):
        result = shards.assign_shards(["TestA", "TestB", "TestC", "TestD"], 2)
        self.assertEqual(result, [["TestA", "TestC"], ["TestB", "TestD"]])

    def test_run_pattern_anchored_and_exact(self):
        pattern = shards.run_pattern(["TestA", "TestBB"])
        compiled = re.compile(pattern)
        self.assertTrue(compiled.fullmatch("TestA"))
        self.assertTrue(compiled.fullmatch("TestBB"))
        self.assertIsNone(compiled.fullmatch("TestAB"),
                          "the alternation must not recombine names")
        self.assertIsNone(compiled.fullmatch("TestAA"),
                          "TestA must not match TestAA as a prefix")
        # ^(...)$ anchors each alternative, so no substring matches either.
        self.assertIsNone(compiled.search("xTestA"))

    def test_run_pattern_escapes_regex_specials(self):
        # Go test names cannot contain regex metacharacters, but the
        # escaping must be correct regardless: a name like "TestA.B" must
        # match only itself, never "TestAXB".
        pattern = shards.run_pattern(["TestA.B", "TestC+"])
        compiled = re.compile(pattern)
        self.assertTrue(compiled.fullmatch("TestA.B"))
        self.assertIsNone(compiled.fullmatch("TestAXB"))
        self.assertTrue(compiled.fullmatch("TestC+"))
        self.assertIsNone(compiled.fullmatch("TestC"))

    def test_parse_events(self):
        events = [
            {"Action": "pass", "Test": "TestAlpha"},
            {"Action": "output", "Test": "TestBeta", "Output": "--- SKIP: x\n"},
            {"Action": "skip", "Test": "TestBeta"},
            {"Action": "fail", "Test": "TestBad"},
            {"Action": "pass", "Test": "TestN/sub"},
            {"Action": "fail"},  # package-level fail: no Test key
        ]
        text = "\n".join(json.dumps(event) for event in events)
        text += "\nFAIL\n"  # non-JSON crash trailer lines are ignored
        passed, failed, skipped, package_failed = shards.parse_events(text)
        self.assertEqual(passed, {"TestAlpha", "TestN/sub"})
        self.assertEqual(failed, {"TestBad"})
        self.assertEqual(skipped, {"TestBeta"})
        self.assertTrue(package_failed)


class EndToEndTest(ToyModuleCase):
    def test_all_pass_exit_zero_and_count(self):
        result = self.run_script("./pkgok", "--split", "./pkgok", "--shards", "2")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        match = re.search(r"^PASS (\d+) tests in ", result.stdout, re.MULTILINE)
        self.assertIsNotNone(match, result.stdout)
        self.assertEqual(int(match.group(1)), 4,
                         "pkgok has 4 top-level tests")
        self.assertIn("--- SKIP (1):", result.stdout)
        self.assertIn("example.com/shardtoy/pkgok.TestBeta", result.stdout)
        self.assertEqual(self.kept_log_dirs(), [],
                         "a successful run must remove its log directory")

    def test_failure_exit_one_and_name_reported(self):
        result = self.run_script("./pkgfail")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("--- FAIL tests:", result.stdout)
        self.assertIn("example.com/shardtoy/pkgfail.TestBad", result.stdout)
        self.assertNotIn("pkgfail.TestGood\n", result.stdout,
                         "the passing test is neither failed nor missing")
        self.assertTrue(self.kept_log_dirs(),
                        "a failed run must keep its log directory")

    def test_crash_missing_results_listed(self):
        result = self.run_script("./pkgcrash")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("--- missing results", result.stdout)
        self.assertIn("example.com/shardtoy/pkgcrash.TestCrash", result.stdout,
                      "the crashing test never reports a result")
        missing_section = result.stdout.split("--- missing results")[1]
        self.assertNotIn("TestSurvivor", missing_section,
                         "TestSurvivor runs before the crash and reports pass")

    def test_shard_union_exactly_the_listed_tests(self):
        result = self.run_script("./pkgshard", "--split", "./pkgshard",
                                 "--shards", "3", "--keep-logs")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        match = re.search(r"^logs kept: (.+)$", result.stdout, re.MULTILINE)
        self.assertIsNotNone(match, result.stdout)
        log_dir = Path(match.group(1).strip())

        names = ["TestShardA", "TestShardAA", "TestShardB",
                 "TestShardC", "TestShardD", "TestShardE"]
        # Six tests over three shards: two tests per shard, three logs.
        logs = sorted(log_dir.glob("*.log"))
        self.assertEqual(len(logs), 3, result.stdout)
        occurrences = {name: 0 for name in names}
        for log in logs:
            text = log.read_text()
            self.assertIn("shard", log.name)
            passed, failed, skipped, _ = shards.parse_events(text)
            self.assertEqual(failed | skipped, set(), text)
            top_level = {name for name in passed if "/" not in name}
            self.assertEqual(len(top_level), 2,
                             f"{log.name} should hold one shard of two tests")
            for name in top_level:
                self.assertIn(name, occurrences,
                              f"unlisted test {name} ran in {log.name}")
                occurrences[name] += 1
        for name in names:
            self.assertEqual(occurrences[name], 1,
                             f"{name} ran {occurrences[name]} times; "
                             "shards must partition the listed tests exactly, "
                             "and TestShardA must not also match TestShardAA")

    def test_sighup_default_in_children(self):
        # Start the script with SIGHUP ignored, like a harness that ignores
        # it; the test checking signal.Ignored(SIGHUP) must still pass.
        result = self.run_script("./pkgok", ignore_sighup=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("PASS 4 tests", result.stdout)


if __name__ == "__main__":
    unittest.main()
