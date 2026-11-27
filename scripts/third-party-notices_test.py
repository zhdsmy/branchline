#!/usr/bin/env python3
"""Offline unit tests for scripts/third-party-notices.py.

Uses only the Python 3 standard library and never invokes `go`: the tests
build fake module trees in temporary directories and pass package info
(directories, file lists) into the scanning functions directly.
"""

import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest import mock

SCRIPT_DIR = Path(__file__).resolve().parent

spec = importlib.util.spec_from_file_location(
    "third_party_notices", SCRIPT_DIR / "third-party-notices.py")
notices = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notices)

APACHE_HEADER = """\
// Copyright 2020 The Ebiten Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shader
"""

MIT_TEXT = """\
MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software.
"""

APACHE_TEXT = """\
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/
"""


def write_file(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


class FakeModuleCase(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.mkdtemp(prefix="third-party-notices-test-")
        self.addCleanup(shutil.rmtree, self.tempdir, True)
        # Resolve once: on macOS /var symlinks to /private/var, and the
        # scanning functions compare resolved paths.
        self.root = (Path(self.tempdir) / "mod").resolve()
        self.root.mkdir()

    def package(self, import_path, files=None):
        directory = self.root / import_path
        directory.mkdir(parents=True, exist_ok=True)
        return {"dir": str(directory), "files": list(files or [])}


class NestedLicenseTest(FakeModuleCase):
    def test_parent_license_collected_unlinked_sibling_not(self):
        write_file(self.root / "internal/shader/LICENSE", APACHE_TEXT)
        write_file(self.root / "internal/shader/shader.go", APACHE_HEADER)
        write_file(self.root / "internal/other/NOTICE.md", "some notice\n")
        write_file(self.root / "internal/other/other.go", "package other\n")

        packages = {
            "example.com/mod/internal/shader":
                self.package("internal/shader", ["shader.go"]),
        }
        nested = notices.find_nested_license_files(
            self.root, [pkg["dir"] for pkg in packages.values()])
        self.assertEqual(
            nested, [("internal/shader/LICENSE",
                      self.root / "internal/shader/LICENSE")],
            "LICENSE of a linked package's parent chain must be collected")

    def test_unlinked_sibling_license_not_collected(self):
        write_file(self.root / "internal/shader/LICENSE", APACHE_TEXT)
        write_file(self.root / "internal/other/LICENSE", APACHE_TEXT)
        packages = {
            "example.com/mod/internal/shader":
                self.package("internal/shader"),
        }
        nested = notices.find_nested_license_files(
            self.root, [pkg["dir"] for pkg in packages.values()])
        relatives = [relative for relative, _ in nested]
        self.assertNotIn("internal/other/LICENSE", relatives,
                         "sibling directory not linked in must not be collected")

    def test_grandparent_license_collected_once(self):
        write_file(self.root / "internal/LICENSE", APACHE_TEXT)
        packages = {
            "example.com/mod/internal/a/b": self.package("internal/a/b"),
            "example.com/mod/internal/a/c": self.package("internal/a/c"),
        }
        nested = notices.find_nested_license_files(
            self.root, [pkg["dir"] for pkg in packages.values()])
        self.assertEqual([relative for relative, _ in nested],
                         ["internal/LICENSE"],
                         "shared ancestors must be deduplicated by relative path")

    def test_package_outside_module_root_fails(self):
        # A package directory outside its module root is an error, not a
        # walk up to the filesystem root.
        outside = Path(self.tempdir) / "elsewhere"
        outside.mkdir()
        with self.assertRaises(notices.NoticesError) as caught:
            notices.find_nested_license_files(self.root, [str(outside)])
        self.assertIn("not inside", str(caught.exception))

    def test_module_root_license_not_nested(self):
        write_file(self.root / "LICENSE", MIT_TEXT)
        packages = {"example.com/mod": self.package(".")}
        nested = notices.find_nested_license_files(
            self.root, [pkg["dir"] for pkg in packages.values()])
        self.assertEqual(nested, [],
                         "the module root license is the root logic's job")


class HeaderScanTest(unittest.TestCase):
    def test_apache_header_recognized(self):
        scan = notices.scan_file_header(APACHE_HEADER)
        self.assertEqual(scan["licenses"], ["Apache-2.0"])
        self.assertIn("Copyright 2020 The Ebiten Authors", scan["copyrights"])
        self.assertFalse(scan["unrecognized"])

    def test_spdx_header_recognized(self):
        text = "// SPDX-License-Identifier: BSD-2-Clause\n\npackage x\n"
        scan = notices.scan_file_header(text)
        self.assertEqual(scan["licenses"], ["BSD-2-Clause"])
        self.assertFalse(scan["unrecognized"])

    def test_license_words_after_package_ignored(self):
        text = ("package x\n\n// Licensed under the Apache License, Version 2.0\n"
                "const s = \"Licensed under the Apache License, Version 2.0\"\n")
        scan = notices.scan_file_header(text)
        self.assertEqual(scan["licenses"], [],
                         "license words after the package clause are not a header")
        self.assertFalse(scan["unrecognized"])

    def test_package_comment_before_clause_not_header_license(self):
        # A doc comment may mention Apache in prose; only the two exact
        # forms (SPDX tag, Apache boilerplate line) are a declaration.
        text = ("// Package x is under the Apache License 2.0 in LICENSE.\n"
                "package x\n")
        scan = notices.scan_file_header(text)
        self.assertEqual(scan["licenses"], [])
        self.assertFalse(scan["unrecognized"])

    def test_no_header_at_all(self):
        scan = notices.scan_file_header("package x\n\nfunc f() {}\n")
        self.assertEqual(scan["licenses"], [])
        self.assertEqual(scan["copyrights"], [])
        self.assertFalse(scan["unrecognized"])

    def test_unrecognized_license_declaration(self):
        text = ("// Copyright 2024 Someone\n"
                "// Licensed under the GNU Lesser General Public License\n\n"
                "package x\n")
        scan = notices.scan_file_header(text)
        self.assertTrue(scan["unrecognized"],
                        "a license declaration of an unrecognized kind must be flagged")

    def test_reference_to_license_file_not_a_declaration(self):
        text = ("// Use of this source code is governed by a BSD-style\n"
                "// license that can be found in the LICENSE file.\n\npackage x\n")
        scan = notices.scan_file_header(text)
        self.assertEqual(scan["licenses"], [])
        self.assertFalse(scan["unrecognized"],
                         "pointing at the module's LICENSE file is not a declaration")


class NonGoHeaderTest(unittest.TestCase):
    def test_block_comment_apache_header_in_c_file(self):
        # C and header files have no package clause; a leading /* */
        # license block must still be read as the header.
        text = ("/*\n"
                " * Copyright 2021 Example Authors\n"
                " *\n"
                " * Licensed under the Apache License, Version 2.0\n"
                " */\n\n"
                "#include <stdint.h>\n"
                "/* Licensed under the GNU General Public License */\n")
        scan = notices.scan_file_header(text)
        self.assertEqual(scan["licenses"], ["Apache-2.0"])
        self.assertIn("Copyright 2021 Example Authors", scan["copyrights"])
        self.assertFalse(scan["unrecognized"],
                         "comments after the first code line are not header")

    def test_spdx_in_block_comment_strips_terminator(self):
        # A one-line /* SPDX-License-Identifier: X */ yields the id X.
        scan = notices.scan_file_header(
            "/* SPDX-License-Identifier: Apache-2.0 */\n#define X 1\n")
        self.assertEqual(scan["licenses"], ["Apache-2.0"])


class ScanPackageFilesTest(FakeModuleCase):
    def test_unreadable_compiled_file_fails(self):
        # A listed compiled file that cannot be read fails instead of being
        # skipped, and the error names the package and the file.
        self.package("shader")
        with self.assertRaises(notices.NoticesError) as caught:
            notices.scan_package_files(
                str(self.root / "shader"), ["missing.go"],
                "module example.com/mod: package example.com/mod/shader")
        message = str(caught.exception)
        self.assertIn("example.com/mod/shader", message)
        self.assertIn("missing.go", message)

    def test_test_files_ignored(self):
        write_file(self.root / "shader/shader.go", APACHE_HEADER)
        write_file(self.root / "shader/shader_test.go", APACHE_HEADER)
        pkg = self.package("shader", ["shader.go", "shader_test.go"])
        results = notices.scan_package_files(pkg["dir"], pkg["files"])
        self.assertEqual([name for name, _ in results], ["shader.go"],
                         "_test.go files must be skipped even if listed")


class PortionsTest(FakeModuleCase):
    def setUp(self):
        super().setUp()
        self.licenses_dir = Path(self.tempdir) / "licenses"
        self.licenses_dir.mkdir()
        write_file(self.licenses_dir / "Apache-2.0.txt", APACHE_TEXT)

    def test_same_as_root_license_no_portions(self):
        write_file(self.root / "a/a.go", APACHE_HEADER)
        packages = {"example.com/mod/a": self.package("a", ["a.go"])}
        portions = notices.collect_portions(
            "example.com/mod", "Apache-2.0", packages, self.licenses_dir)
        self.assertEqual(portions, {},
                         "header license equal to the root license adds no Portions")

    def test_different_license_groups_portions(self):
        write_file(self.root / "a/a.go", APACHE_HEADER)
        write_file(self.root / "a/b.go", APACHE_HEADER)
        packages = {"example.com/mod/a": self.package("a", ["a.go", "b.go"])}
        portions = notices.collect_portions(
            "example.com/mod", "MIT", packages, self.licenses_dir)
        self.assertEqual(sorted(portions), ["Apache-2.0"])
        portion = portions["Apache-2.0"]
        self.assertEqual(portion["packages"], {"example.com/mod/a"})
        self.assertEqual(portion["copyrights"],
                         {"Copyright 2020 The Ebiten Authors"})
        self.assertEqual(sorted(portion["files"]),
                         ["example.com/mod/a/a.go", "example.com/mod/a/b.go"])

    def test_missing_license_text_fails(self):
        write_file(self.root / "a/a.go", APACHE_HEADER)
        empty_dir = Path(self.tempdir) / "empty-licenses"
        empty_dir.mkdir()
        packages = {"example.com/mod/a": self.package("a", ["a.go"])}
        with self.assertRaises(notices.NoticesError) as caught:
            notices.collect_portions(
                "example.com/mod", "MIT", packages, empty_dir)
        message = str(caught.exception)
        self.assertIn("example.com/mod", message)
        self.assertIn("example.com/mod/a", message)
        self.assertIn("a.go", message)

    def test_unrecognized_header_fails(self):
        write_file(self.root / "a/a.go",
                   "// Licensed under the GNU Affero General Public License\n\n"
                   "package a\n")
        packages = {"example.com/mod/a": self.package("a", ["a.go"])}
        with self.assertRaises(notices.NoticesError) as caught:
            notices.collect_portions(
                "example.com/mod", "MIT", packages, self.licenses_dir)
        message = str(caught.exception)
        self.assertIn("example.com/mod", message)
        self.assertIn("example.com/mod/a", message)
        self.assertIn("a.go", message)


class CollectModulesTest(unittest.TestCase):
    def test_non_go_compiled_files_are_listed(self):
        # Header, C and other non-Go compiled files from `go list -json`
        # are scanned along with Go files; test files are not listed.
        package = {
            "ImportPath": "example.com/mod/abi",
            "Dir": "/mod/abi",
            "Module": {"Path": "example.com/mod", "Version": "v1.0.0",
                       "Dir": "/mod"},
            "GoFiles": ["abi.go"],
            "CFiles": ["abi.c"],
            "HFiles": ["abi_arm64.h"],
            "SFiles": ["abi_arm64.s"],
            "TestGoFiles": ["abi_test.go"],
        }
        result = mock.Mock(returncode=0, stdout=json.dumps(package), stderr="")
        with mock.patch.object(notices.subprocess, "run", return_value=result):
            modules = notices.collect_modules("/repo")
        files = modules["example.com/mod"]["packages"]["example.com/mod/abi"]["files"]
        self.assertEqual(sorted(files),
                         ["abi.c", "abi.go", "abi_arm64.h", "abi_arm64.s"])


class ModuleEntryTest(FakeModuleCase):
    def test_entry_has_nested_license_and_portions(self):
        write_file(self.root / "LICENSE", MIT_TEXT)
        write_file(self.root / "internal/shader/LICENSE", APACHE_TEXT)
        write_file(self.root / "internal/shader/shader.go", APACHE_HEADER)
        licenses_dir = Path(self.tempdir) / "licenses"
        licenses_dir.mkdir()
        write_file(licenses_dir / "Apache-2.0.txt", APACHE_TEXT)

        info = {
            "version": "v0.0.1",
            "dir": str(self.root),
            "packages": {
                "example.com/mod/internal/shader":
                    self.package("internal/shader", ["shader.go"]),
            },
        }
        entry = notices.module_entry("example.com/mod", info, licenses_dir)
        self.assertIn("example.com/mod v0.0.1 (MIT)", entry)
        self.assertIn("internal/shader/LICENSE", entry)
        self.assertIn("Portions licensed under Apache-2.0", entry)
        self.assertIn("example.com/mod/internal/shader", entry)
        self.assertIn("Copyright 2020 The Ebiten Authors", entry)
        self.assertIn(APACHE_TEXT.rstrip("\n"), entry)

    def test_deterministic_output(self):
        write_file(self.root / "LICENSE", MIT_TEXT)
        write_file(self.root / "internal/shader/LICENSE", APACHE_TEXT)
        write_file(self.root / "internal/shader/shader.go", APACHE_HEADER)
        write_file(self.root / "internal/shaderir/x.go", APACHE_HEADER)
        licenses_dir = Path(self.tempdir) / "licenses"
        licenses_dir.mkdir()
        write_file(licenses_dir / "Apache-2.0.txt", APACHE_TEXT)
        info = {
            "version": "v0.0.1",
            "dir": str(self.root),
            "packages": {
                "example.com/mod/internal/shader":
                    self.package("internal/shader", ["shader.go"]),
                "example.com/mod/internal/shaderir":
                    self.package("internal/shaderir", ["x.go"]),
            },
        }
        first = notices.module_entry("example.com/mod", info, licenses_dir)
        second = notices.module_entry("example.com/mod", info, licenses_dir)
        self.assertEqual(first, second)


if __name__ == "__main__":
    unittest.main()
