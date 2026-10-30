#!/usr/bin/env python3
"""Bounded shell-level staging contract tests; no network or desktop process.

Run: python3 scripts/appimage/test-stage-freetype.py
The real staging script runs throughout. PATH doubles isolate wget, dpkg-deb
and (except the checksum-negative case) the archive checksum boundary. The ABI
check is never doubled: it loads tiny compiled FreeType API fixtures through
its real ctypes code. cc, bash, Python and coreutils are required.
"""
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("stage-freetype.sh").resolve()
PIN = "3728210ef44a7a51b3824d5c499f6fbd428584a42a65aee15794a4264c8c173a"


class StageFreeTypeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixtures = tempfile.TemporaryDirectory()
        cls.fixture_root = Path(cls.fixtures.name)
        for minor in (11, 13):
            source = cls.fixture_root / f"ft-{minor}.c"
            source.write_text(
                "int FT_Init_FreeType(void **h) { *h = (void *)1; return 0; }\n"
                f"void FT_Library_Version(void *h, int *a, int *b, int *c) {{ *a=2; *b={minor}; *c=1; }}\n"
                "int FT_Done_FreeType(void *h) { return 0; }\n"
            )
            subprocess.run(
                ["cc", "-shared", "-fPIC", str(source), "-o", str(cls.fixture_root / f"ft-{minor}.so")],
                check=True, capture_output=True, timeout=20,
            )
        cls.real_sha = shutil.which("sha256sum")
        assert cls.real_sha

    @classmethod
    def tearDownClass(cls):
        cls.fixtures.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        self.app = self.work / "AppDir"
        self.env = {**os.environ, "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                    "FT_TEST_MODE": "ok", "FT_TEST_FIXTURES": str(self.fixture_root)}
        self.stub("wget", '#!/bin/sh\n[ "$FT_TEST_MODE" != download ] || exit 8\nprintf archive-fixture > "$3"\n')
        # Deeper tests must cross a hash-verified archive boundary without
        # downloading the pinned package. Never replace the ABI assertion.
        self.stub("sha256sum", '#!/bin/sh\nif [ "${1-}" = -c ]; then\n read -r expected file\n [ "$expected" = 3728210ef44a7a51b3824d5c499f6fbd428584a42a65aee15794a4264c8c173a ] && [ -s "$file" ]\nelse\n exec REAL_SHA "$@"\nfi\n'.replace("REAL_SHA", shlex.quote(self.real_sha)))
        self.stub("dpkg-deb", "#!" + sys.executable + "\n" + DPkg_STUB)

    def stub(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def run_stage(self, mode="ok"):
        return subprocess.run(["bash", str(SCRIPT), str(self.app)],
                              env={**self.env, "FT_TEST_MODE": mode},
                              capture_output=True, text=True, timeout=15)

    def assert_failure(self, mode, diagnostic):
        result = self.run_stage(mode)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(diagnostic, result.stdout + result.stderr)
        self.assertFalse((self.app / "usr/share/doc/libfreetype6/nocx-provenance.txt").exists())

    def test_download_failure_is_named(self):
        self.assert_failure("download", "download failed:")

    def test_wrong_archive_hash_uses_real_checksum_and_fails(self):
        (self.bin / "sha256sum").unlink()
        self.assert_failure("ok", "package SHA256 mismatch")

    def test_wrong_package_name_fails(self):
        self.assert_failure("name", "unexpected package name")

    def test_wrong_package_version_fails(self):
        self.assert_failure("version", "unexpected package version")

    def test_wrong_package_architecture_fails(self):
        self.assert_failure("architecture", "unexpected package architecture")

    def test_unpack_failure_is_named(self):
        self.assert_failure("extract", "package extraction failed")

    def test_unpacked_package_without_freetype_fails(self):
        self.assert_failure("missing-library", "extracted package has no FreeType SONAME")

    def test_unpacked_package_without_notice_fails(self):
        self.assert_failure("missing-notice", "extracted package has no FreeType copyright notice")

    def test_real_abi_assertion_rejects_new_layout_runtime(self):
        self.assert_failure("abi", "copied library ABI assertion failed")

    def test_unpacked_non_library_fails_real_loader_assertion(self):
        self.assert_failure("invalid-library", "copied library ABI assertion failed")

    def test_expected_runtime_succeeds_and_stages_notice_and_provenance(self):
        result = self.run_stage()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        library = self.app / "usr/lib/libfreetype.so.6"
        self.assertTrue(library.is_file())
        self.assertFalse(library.is_symlink())
        docs = self.app / "usr/share/doc/libfreetype6"
        self.assertEqual((docs / "copyright").read_text(), "fixture notice\n")
        self.assertIn("version=2.11.1+dfsg-1ubuntu0.4", (docs / "nocx-provenance.txt").read_text())
        self.assertIn("FreeType ABI checked:", result.stdout)


DPkg_STUB = r'''import os
from pathlib import Path
import shutil
import sys

mode = os.environ["FT_TEST_MODE"]
if sys.argv[1] == "-f":
    field = sys.argv[3]
    expected = {"Package": "libfreetype6", "Version": "2.11.1+dfsg-1ubuntu0.4", "Architecture": "amd64"}
    bad = {"Package": "name", "Version": "version", "Architecture": "architecture"}
    print("wrong" if mode == bad[field] else expected[field])
else:
    if mode == "extract":
        sys.exit(2)
    root = Path(sys.argv[3])
    lib = root / "usr/lib/x86_64-linux-gnu/libfreetype.so.6"
    lib.parent.mkdir(parents=True)
    if mode != "missing-library":
        minor = 13 if mode == "abi" else 11
        shutil.copyfile(Path(os.environ["FT_TEST_FIXTURES"]) / f"ft-{minor}.so", lib)
        if mode == "invalid-library":
            lib.write_bytes(b"not an ELF library")
    docs = root / "usr/share/doc/libfreetype6"
    docs.mkdir(parents=True)
    if mode != "missing-notice":
        (docs / "copyright").write_text("fixture notice\n")
'''


if __name__ == "__main__":
    unittest.main()
