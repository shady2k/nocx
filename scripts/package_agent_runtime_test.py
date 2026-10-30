#!/usr/bin/env python3
"""A build must not mutate an executing cache inode or replace an aliased output."""
import hashlib
import importlib.util
import json
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

SCRIPT = Path(__file__).with_name("package-agent-runtime.py")
SPEC = importlib.util.spec_from_file_location("package_agent_runtime", SCRIPT)
PACKAGER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACKAGER)


class RuntimePackagingTests(unittest.TestCase):
    def test_republishing_cached_tool_preserves_an_executing_inode(self):
        suffixes = {
            ("Linux", "x86_64"): "manylinux2014_x86_64.whl",
            ("Darwin", "x86_64"): "macosx_10_12_x86_64.whl",
            ("Darwin", "arm64"): "macosx_11_0_arm64.whl",
        }
        suffix = suffixes.get((platform.system(), platform.machine()))
        if suffix is None:
            self.skipTest("the runtime builder does not support this host target")
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary)
            wheel = cache / "tool.whl"
            replacement = Path(shutil.which("echo")).read_bytes()
            with zipfile.ZipFile(wheel, "w") as archive:
                archive.writestr("uv.data/scripts/uv", replacement)
            identity = hashlib.sha256(wheel.read_bytes()).hexdigest()
            shutil.copyfile(wheel, cache / identity)
            binary = cache / (identity + "-uv")
            shutil.copyfile(shutil.which("cat"), binary)
            binary.chmod(0o700)
            source = {"build_tool": {"artifacts": [{
                "filename": "uv-test-" + suffix,
                "url": wheel.as_uri(),
                "sha256": identity,
            }]}}
            running = subprocess.Popen(
                [str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            try:
                published = PACKAGER.uv_binary(cache, source)
                result = subprocess.run(
                    [str(published), "new executable"], capture_output=True,
                    check=True, timeout=5,
                )
                self.assertEqual(result.stdout, b"new executable\n")
                output, errors = running.communicate(b"original executable\n", timeout=5)
                self.assertEqual(output, b"original executable\n")
                self.assertEqual(errors, b"")
                self.assertEqual(running.returncode, 0)
            finally:
                if running.poll() is None:
                    running.kill()
                running.communicate(timeout=5)

    def test_cli_refuses_existing_and_dangling_output_symlinks_before_building(self):
        for existing in (True, False):
            with self.subTest(existing=existing), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                destination = root / "destination"
                if existing:
                    destination.mkdir()
                    (destination / "manifest.json").write_text(json.dumps({
                        "formatVersion": 1, "metadata": {"target": "linux/amd64"},
                    }))
                    (destination / "retained").write_text("do not replace through an alias")
                alias = root / "alias"
                alias.symlink_to(destination, target_is_directory=True)
                arguments = [str(SCRIPT), "--target", "linux-amd64", "--output", str(alias), "--replace"]
                with patch.object(sys, "argv", arguments), patch.object(
                    PACKAGER, "uv_binary", side_effect=AssertionError("an output alias must not start a build"),
                ):
                    with self.assertRaisesRegex(ValueError, "symlink"):
                        PACKAGER.main()
                self.assertTrue(alias.is_symlink())
                if existing:
                    self.assertEqual((destination / "retained").read_text(), "do not replace through an alias")
                else:
                    self.assertFalse(destination.exists())


if __name__ == "__main__":
    unittest.main()
