#!/usr/bin/env python3
"""The publication gate must reject mutations of what the package actually carries."""
import json
import plistlib
import zipfile
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("verify-built-artifact.py")
ROOT = Path(__file__).resolve().parents[1]


class BuiltArtifactGate(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bundle = self.root / "nocx.app"
        self.bin = self.bundle / "Contents/MacOS"
        self.bin.mkdir(parents=True)
        self.info = dict(Version="0.8.0", Commit="a" * 40, Date="2026-10-27T10:00:00Z",
                         Go="go1.26.1", Wails="v3.0.0-beta.9", Platform="darwin/arm64", Development=False)
        self.plist = dict(CFBundleIdentifier="dev.nocx", CFBundleName="nocx", CFBundleExecutable="nocx",
                          CFBundleVersion="0.8.0", CFBundleShortVersionString="0.8.0",
                          CFBundleGetInfoString="A local-first, Warp-style terminal",
                          NSHumanReadableCopyright="Copyright © 2026 shady2k")
        self.manifest = dict(manifest={"version": "0.7.2", "artifacts": []},
                             sha256="b" * 64, signature="signed-response", source="https://github.com/shady2k/nocx/releases/latest/download")
        self.write_package()
        self.archive = self.root / 'nocx.zip'
        self.pack()

    def write_package(self):
        with (self.bundle / "Contents/Info.plist").open("wb") as out:
            plistlib.dump(self.plist, out)
        for name in ["nocx", "nocx-server"]:
            output = json.dumps(self.info)
            manifest = json.dumps(self.manifest)
            # Deliberately executable fixtures: the gate must run the package,
            # not read a neighbouring metadata file prepared by the build.
            exe = self.bin / name
            exe.write_text('#!' + sys.executable + '\nimport sys\n'
                           + 'print(' + repr(manifest) + ' if sys.argv[1] == "--verify-release-manifest" else ' + repr(output) + ')\n')
            exe.chmod(0o755)

    def pack(self):
        with zipfile.ZipFile(self.archive, "w") as archive:
            for path in self.bundle.rglob("*"):
                if path.is_file():
                    archive.write(path, path.relative_to(self.root))

    def run_gate(self, *extra):
        self.pack()
        return subprocess.run([sys.executable, str(SCRIPT), '--artifact', str(self.archive),
                               '--version', '0.8.0', '--commit', 'a' * 40,
                               '--report', str(self.root / 'report.json'), *extra],
                              text=True, capture_output=True, check=False)

    def test_accepts_package_and_records_hashes_not_just_booleans(self):
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads((self.root / 'report.json').read_text())
        self.assertEqual(report['executables']['nocx-server']['about']['Wails'], self.info['Wails'])
        self.assertEqual(len(report['archive']['sha256']), 64)
        self.assertEqual(report['updater']['manifest']['version'], '0.7.2')

    def test_malformed_plist_failure_names_the_bytes_and_parser_reason(self):
        for payload, reason in [(b"not a plist\n", "Invalid file"),
                                (b"<?xml version='1.0'?><plist><dict>", "no element found"),
                                (b'<!DOCTYPE plist><plist><dict>', "no element found"),
                                (b'bplist00broken', "Invalid file")]:
            with self.subTest(payload=payload):
                (self.bundle / "Contents/Info.plist").write_bytes(payload)
                (self.root / 'report.json').write_text('{"stale":true}')
                result = self.run_gate()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('nocx.app/Contents/Info.plist', result.stderr)
                self.assertIn(f'{len(payload)} bytes', result.stderr)
                self.assertIn(repr(payload[:64]), result.stderr)
                self.assertIn(reason, result.stderr)
                self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)
                self.assertFalse((self.root / 'report.json').exists())

    def test_missing_plist_names_the_unreadable_path(self):
        (self.bundle / "Contents/Info.plist").unlink()
        result = self.run_gate()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('nocx.app/Contents/Info.plist', result.stderr)
        self.assertIn('size/first bytes unavailable', result.stderr)
        self.assertIn('No such file', result.stderr)
        self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)

    def test_malformed_json_names_the_executable_output_and_reason(self):
        for option, payload in [('--build-info', 'not JSON'),
                                ('--verify-release-manifest', '{"broken":')]:
            with self.subTest(option=option):
                self.write_package()
                exe = self.bin / 'nocx'
                exe.write_text('#!' + sys.executable + '\nimport sys\n'
                               + 'print(' + repr(payload) + ' if sys.argv[1] == '
                               + repr(option) + ' else ' + repr(json.dumps(self.info)) + ')\n')
                result = self.run_gate()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('nocx.app/Contents/MacOS/nocx ' + option, result.stderr)
                self.assertIn(f'{len(payload) + 1} bytes', result.stderr)
                self.assertIn(repr((payload + '\n').encode()[:64]), result.stderr)
                self.assertIn('JSON', result.stderr)
                self.assertIn('Expecting', result.stderr)
                self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)

    def test_corrupt_archive_names_its_bytes_and_zip_reason(self):
        # Bypass pack: these are the malformed archive bytes actually read.
        self.archive.write_bytes(b'not a zip')
        result = subprocess.run([sys.executable, str(SCRIPT), '--artifact', str(self.archive),
                                 '--version', '0.8.0', '--commit', 'a' * 40,
                                 '--report', str(self.root / 'report.json')],
                                text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(str(self.archive), result.stderr)
        self.assertIn('9 bytes', result.stderr)
        self.assertIn("b'not a zip'", result.stderr)
        self.assertIn('not a zip file', result.stderr)

    def test_accepts_binary_plist_without_skipping_identity_checks(self):
        path = self.bundle / 'Contents/Info.plist'
        path.write_bytes(plistlib.dumps(self.plist, fmt=plistlib.FMT_BINARY))
        self.assertEqual(self.run_gate().returncode, 0)
        self.plist['CFBundleVersion'] = '0.7.0'
        path.write_bytes(plistlib.dumps(self.plist, fmt=plistlib.FMT_BINARY))
        result = self.run_gate()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('CFBundleVersion', result.stderr)
        self.assertIn('nocx.app/Contents/Info.plist', result.stderr)

    def test_accepts_declaration_free_xml_and_still_checks_identity(self):
        path = self.bundle / 'Contents/Info.plist'
        # Generated by the pinned CLI with release-build.yml's exact flags.
        # Keep the original bytes: plistlib.dump would hide the format bug.
        payload = (SCRIPT.parent / 'testdata/wails-beta.9-Info.plist').read_bytes()
        self.assertTrue(payload.startswith(b'<!DOCTYPE plist'))
        path.write_bytes(payload)
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.plist['CFBundleVersion'] = '0.7.0'
        path.write_bytes(plistlib.dumps(self.plist).split(b'\n', 1)[1])
        result = self.run_gate()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('CFBundleVersion', result.stderr)

    def test_executable_failure_names_the_command_and_output(self):
        exe = self.bin / 'nocx'
        exe.write_text('#!/bin/sh\necho "bad build info"; echo "cannot inspect" >&2; exit 7\n')
        result = self.run_gate()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(str(self.archive), result.stderr)
        self.assertIn('nocx.app/Contents/MacOS/nocx --build-info', result.stderr)
        self.assertIn('exit 7', result.stderr)
        self.assertIn('bad build info', result.stderr)
        self.assertIn('cannot inspect', result.stderr)

    def test_missing_declaration_names_the_source_and_bytes(self):
        import shutil
        checkout = self.root / 'checkout'
        (checkout / 'scripts').mkdir(parents=True)
        shutil.copy2(SCRIPT, checkout / 'scripts/verify-built-artifact.py')
        for name in ['go.mod', 'LICENSE', 'main.go', 'build/darwin/Info.plist']:
            target = checkout / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / name, target)
        for name in ['go.mod', 'LICENSE', 'main.go', 'build/darwin/Info.plist']:
            with self.subTest(name=name):
                path = checkout / name
                original = path.read_bytes()
                path.write_bytes(b'missing declaration')
                try:
                    result = subprocess.run([sys.executable, str(checkout / 'scripts/verify-built-artifact.py'),
                                             '--artifact', str(self.archive), '--version', '0.8.0',
                                             '--commit', 'a' * 40, '--report', str(self.root / 'report.json')],
                                            text=True, capture_output=True, check=False)
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertIn(str(path), result.stderr)
                    self.assertIn('19 bytes', result.stderr)
                    self.assertIn("b'missing declaration'", result.stderr)
                finally:
                    path.write_bytes(original)

    def test_executable_output_diagnostics_preserve_exact_bytes(self):
        for payload in [b'not JSON\r\n', b'\xffbad utf8\n']:
            with self.subTest(payload=payload):
                exe = self.bin / 'nocx'
                exe.write_text('#!' + sys.executable + '\nimport sys\n'
                               + 'sys.stdout.buffer.write(' + repr(payload) + ')\n')
                result = self.run_gate()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('nocx.app/Contents/MacOS/nocx --build-info', result.stderr)
                self.assertIn(f'{len(payload)} bytes', result.stderr)
                self.assertIn(repr(payload), result.stderr)
                self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)

    def test_non_string_manifest_hash_is_a_contextual_refusal(self):
        for value in [None, 42, []]:
            with self.subTest(value=value):
                self.manifest['sha256'] = value
                self.write_package()
                result = self.run_gate()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn('updater.signature', result.stderr)
                self.assertIn('nocx.app/Contents/MacOS/nocx --verify-release-manifest', result.stderr)
                self.assertIn('bytes', result.stderr)
                self.assertEqual(len(result.stderr.splitlines()), 1, result.stderr)

    def test_refuses_each_wrong_about_field(self):
        for field in ['Version', 'Commit', 'Date', 'Go', 'Wails', 'Platform']:
            with self.subTest(field=field):
                original = self.info[field]
                self.info[field] = 'unknown'
                self.write_package()
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(field, result.stderr)
                self.info[field] = original

    def test_refuses_wrong_and_template_bundle_identity(self):
        for field, value in [('CFBundleIdentifier', 'com.example.app'), ('CFBundleVersion', '0.7.0'),
                             ('NSHumanReadableCopyright', '© now, My Company')]:
            with self.subTest(field=field):
                original = self.plist[field]
                self.plist[field] = value
                self.write_package()
                result = self.run_gate()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(field, result.stderr)
                self.plist[field] = original

    def test_manifest_fetch_or_verification_failure_is_fatal(self):
        exe = self.bin / 'nocx'
        exe.write_text('#!/bin/sh\nif [ "$1" = --verify-release-manifest ]; then echo "manifest verification failed" >&2; exit 7; fi\nprintf \'%s\\n\' \'' + json.dumps(self.info) + "'\n")
        exe.chmod(0o755)
        result = self.run_gate()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('manifest verification failed', result.stderr)

    def test_checks_linux_appimage_extracted_launch_path(self):
        self.info['Platform'] = 'linux/amd64'
        self.write_package()
        # AppImage fixture exercises the same extract-and-AppRun branch. It is
        # not evidence of a real Linux AppImage; GitHub supplies that check.
        appdir = self.root / 'source-appdir'
        bindir = appdir / 'usr/bin'
        bindir.mkdir(parents=True)
        for name in ['nocx', 'nocx-server']:
            import shutil
            shutil.copy2(self.bin / name, bindir / name)
        (appdir / 'AppRun').write_text('#!/bin/sh\nexec "$(dirname "$0")/usr/bin/nocx" "$@"\n')
        (appdir / 'AppRun').chmod(0o755)
        appimage = self.root / 'nocx.AppImage'
        appimage.write_text('#!' + sys.executable + '\nimport shutil, sys\n'
                            + 'assert sys.argv[1] == "--appimage-extract"\n'
                            + 'shutil.copytree(' + repr(str(appdir)) + ', "squashfs-root")\n')
        appimage.chmod(0o755)
        result = subprocess.run([sys.executable, str(SCRIPT), '--artifact', str(appimage),
                                 '--version', '0.8.0', '--commit', 'a' * 40,
                                 '--report', str(self.root / 'report.json')],
                                text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.root / 'report.json').read_text())['identity'], {})

    def test_refuses_coordinator_only_missing_wails_stamp(self):
        self.info['Wails'] = 'unknown'
        self.write_package()
        # Restore only the desktop fixture to show that inspecting it alone
        # cannot establish what app.about's coordinator was built with.
        desktop_info = dict(self.info, Wails='v3.0.0-beta.9')
        exe = self.bin / 'nocx'
        exe.write_text('#!' + sys.executable + '\nprint(' + repr(json.dumps(desktop_info)) + ')\n')
        exe.chmod(0o755)
        result = self.run_gate()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('nocx-server', result.stderr)
        self.assertIn('Wails', result.stderr)

    def test_failed_gate_removes_previous_success_report(self):
        (self.root / 'report.json').write_text('{"stale":true}')
        self.info['Wails'] = 'unknown'
        self.write_package()
        self.assertNotEqual(self.run_gate().returncode, 0)
        self.assertFalse((self.root / 'report.json').exists())


if __name__ == '__main__':
    unittest.main()
