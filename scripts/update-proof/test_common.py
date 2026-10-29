#!/usr/bin/env python3
"""Unit checks for evidence collection, not proof of the shipped updater.

The temporary verifier checks process invocation and the refusal boundary only.
No generated key or substitute updater can establish release acceptance here.
"""

import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shlex
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location(
    "update_proof_common", Path(__file__).with_name("common.py"))
common = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(common)


class CommonTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)

    def item(self, payload=b"published AppImage bytes", *, platform="linux",
             arch="amd64", tag="nightly-20261025-12345"):
        suffix = "AppImage" if platform == "linux" else "zip"
        return {
            "os": platform,
            "arch": arch,
            "format": suffix,
            "url": ("https://github.com/shady2k/nocx/releases/download/"
                    f"{tag}/nocx-0.8.0-{platform}-{arch}.{suffix}"),
            "sha256": hashlib.sha256(payload).hexdigest(),
            "size": len(payload),
        }

    def manifest(self, payload=b"published AppImage bytes", *,
                 tag="nightly-20261025-12345", released="2026-10-25T04:00:00Z",
                 platform="linux", arch="amd64"):
        return {
            "version": "0.8.0",
            "released": released,
            "notesUrl": f"https://github.com/shady2k/nocx/releases/tag/{tag}",
            "artifacts": [self.item(payload, platform=platform, arch=arch, tag=tag)],
        }

    def pair(self):
        return (self.manifest(), self.manifest(
            b"next published AppImage bytes", tag="nightly-20261026-12346",
            released="2026-10-26T04:00:00Z"))

    def verifier(self, status=0):
        """A shell stub, deliberately not a signature-verification fixture."""
        executable = self.directory / "compiled verifier placeholder"
        arguments = self.directory / "verifier arguments"
        executable.write_text(
            "#!/bin/sh\n"
            f"printf '%s\\n' \"$@\" > {shlex.quote(str(arguments))}\n"
            "printf '%s\\n' 'verifier diagnostic' >&2\n"
            f"exit {status}\n")
        executable.chmod(0o700)
        return executable, arguments

    def test_require_refuses_with_the_assertion_message(self):
        common.require(True, "not a refusal")
        with self.assertRaisesRegex(common.ProofError, "signed bytes differ"):
            common.require(False, "signed bytes differ")

    def test_sha256_reads_the_actual_file_bytes(self):
        payload = bytes(range(256)) * 8193
        path = self.directory / "image.AppImage"
        path.write_bytes(payload)
        self.assertEqual(common.sha256(path), hashlib.sha256(payload).hexdigest())
        path.write_bytes(payload + b"changed")
        self.assertNotEqual(common.sha256(path), hashlib.sha256(payload).hexdigest())

    def test_verifier_receives_exact_manifest_and_signature_paths(self):
        manifest = self.directory / "manifest with spaces.json"
        signature = self.directory / "detached signature.sig"
        value = self.manifest()
        manifest.write_text(json.dumps(value))
        signature.write_text("not a real signature: invocation test only")
        verifier, arguments = self.verifier()
        self.assertEqual(common.verify_manifest(manifest, signature, verifier), value)
        self.assertEqual(arguments.read_text().splitlines(), [
            "-verify", "-in", str(manifest), "-sig", str(signature)])

    def test_verifier_refusal_precedes_parsing_untrusted_json(self):
        manifest = self.directory / "manifest.json"
        signature = self.directory / "manifest.json.sig"
        manifest.write_text("not JSON and not authenticated")
        signature.write_text("unsigned")
        verifier, arguments = self.verifier(status=17)
        with self.assertRaises(common.ProofError) as refused:
            common.verify_manifest(manifest, signature, verifier)
        self.assertIn("verifier diagnostic", str(refused.exception))
        self.assertTrue(arguments.exists(), "the external verifier must actually run")

    def test_verified_json_must_be_an_object(self):
        manifest = self.directory / "manifest.json"
        signature = self.directory / "manifest.json.sig"
        manifest.write_text("[]")
        signature.write_text("invocation stub")
        verifier, _ = self.verifier()
        with self.assertRaises(common.ProofError):
            common.verify_manifest(manifest, signature, verifier)

    def test_artifact_selects_the_exact_platform_architecture_and_format(self):
        wanted = self.item()
        manifest = {"artifacts": [
            self.item(platform="darwin", arch="universal"),
            self.item(arch="arm64"), dict(wanted, format="zip"), wanted]}
        self.assertEqual(common.artifact(manifest, "linux", "amd64"), wanted)

    def test_artifact_refuses_missing_or_ambiguous_matches(self):
        item = self.item()
        for artifacts in ([], [self.item(arch="arm64")], [item, copy.deepcopy(item)]):
            with self.subTest(artifacts=artifacts), self.assertRaises(common.ProofError):
                common.artifact({"artifacts": artifacts}, "linux", "amd64")

    def test_darwin_prefers_exact_architecture_then_accepts_universal(self):
        universal = self.item(platform="darwin", arch="universal")
        exact = self.item(platform="darwin", arch="arm64")
        self.assertEqual(common.artifact({"artifacts": [universal, exact]},
                                         "darwin", "arm64"), exact)
        self.assertEqual(common.artifact({"artifacts": [universal]},
                                         "darwin", "arm64"), universal)
        with self.assertRaises(common.ProofError):
            common.artifact({"artifacts": [universal, copy.deepcopy(universal)]},
                            "darwin", "arm64")

    def test_selected_artifact_requires_digest_size_and_https(self):
        invalid_fields = [
            {"sha256": "0" * 63}, {"sha256": "A" * 64},
            {"sha256": "g" * 64}, {"size": 0}, {"size": -1},
            {"size": True}, {"size": "25"},
            {"url": "http://github.com/shady2k/nocx/image.AppImage"},
        ]
        for fields in invalid_fields:
            with self.subTest(fields=fields), self.assertRaises(common.ProofError):
                common.artifact({"artifacts": [dict(self.item(), **fields)]},
                                "linux", "amd64")

    def test_download_replaces_only_after_size_and_digest_have_verified(self):
        payload = b"actual downloaded bytes"
        item = self.item(payload)
        destination = self.directory / "installed.AppImage"
        destination.write_bytes(b"previous installed bytes")
        replace = common.os.replace
        observations = []

        def observe_replace(source, target):
            source = Path(source)
            target = Path(target)
            observations.append((source.parent, target, source.read_bytes(),
                                 target.read_bytes()))
            return replace(source, target)

        with mock.patch.object(common, "urlopen", return_value=io.BytesIO(payload)), \
                mock.patch.object(common.os, "replace", side_effect=observe_replace):
            common.download_artifact(item, destination)
        self.assertEqual(observations, [(destination.parent, destination, payload,
                                        b"previous installed bytes")])
        self.assertEqual(destination.read_bytes(), payload)
        self.assertEqual(sorted(p.name for p in self.directory.iterdir()),
                         [destination.name])

    def test_short_long_or_corrupt_download_never_replaces_an_installation(self):
        payload = b"authenticated expected bytes"
        item = self.item(payload)
        for downloaded in (payload[:-1], payload + b"extra", b"X" * len(payload)):
            with self.subTest(downloaded=downloaded):
                destination = self.directory / "installed.AppImage"
                destination.write_bytes(b"old installation")
                with mock.patch.object(common, "urlopen", return_value=io.BytesIO(downloaded)), \
                        mock.patch.object(common.os, "replace") as replace, \
                        self.assertRaises(common.ProofError):
                    common.download_artifact(item, destination)
                replace.assert_not_called()
                self.assertEqual(destination.read_bytes(), b"old installation")
                self.assertEqual(sorted(p.name for p in self.directory.iterdir()),
                                 [destination.name])

    def test_transport_failure_keeps_installation_and_cleans_staging(self):
        destination = self.directory / "installed.AppImage"
        destination.write_bytes(b"old installation")
        with mock.patch.object(common, "urlopen", side_effect=OSError("network down")), \
                self.assertRaises(Exception):
            common.download_artifact(self.item(), destination)
        self.assertEqual(destination.read_bytes(), b"old installation")
        self.assertEqual(sorted(p.name for p in self.directory.iterdir()),
                         [destination.name])

    def test_validated_download_can_create_a_new_destination_directory(self):
        payload = b"new installed bytes"
        destination = self.directory / "new" / "nested" / "installed.AppImage"
        with mock.patch.object(common, "urlopen", return_value=io.BytesIO(payload)):
            common.download_artifact(self.item(payload), destination)
        self.assertEqual(destination.read_bytes(), payload)

    def test_pending_record_resolves_symlink_spelling_of_install_path(self):
        real_directory = self.directory / "real"
        real_directory.mkdir()
        alias = self.directory / "alias"
        alias.symlink_to(real_directory, target_is_directory=True)
        install = alias / "nocx.AppImage"
        install.write_bytes(b"installed")
        expected = {"version": "0.8.0", "sha256": "a" * 64}
        record = {"installPath": str(real_directory / install.name),
                  "artifactSHA256": expected["sha256"], "toVersion": expected["version"],
                  "schemaVersion": 1}
        journal = self.directory / "journal.json"
        journal.write_text(json.dumps(record))
        self.assertEqual(common.read_pending(journal, install, expected), record)

    def test_pending_record_refuses_wrong_mount_digest_or_version(self):
        install = self.directory / "installed.AppImage"
        expected = {"version": "0.8.0", "sha256": "a" * 64}
        record = {"installPath": str(install), "artifactSHA256": expected["sha256"],
                  "toVersion": expected["version"]}
        for changes in ({"installPath": str(self.directory / "mount" / "nocx")},
                        {"artifactSHA256": "b" * 64}, {"toVersion": "0.7.2"}):
            with self.subTest(changes=changes):
                journal = self.directory / "journal.json"
                journal.write_text(json.dumps(dict(record, **changes)))
                with self.assertRaises(common.ProofError):
                    common.read_pending(journal, install, expected)

    def test_pair_proves_two_same_version_different_byte_published_nightlies(self):
        previous, candidate = self.pair()
        self.assertEqual(common.nightly_pair(previous, candidate, "linux", "amd64"),
                         (previous["artifacts"][0], candidate["artifacts"][0]))

    def test_pair_supports_darwin_universal_payloads(self):
        previous = self.manifest(platform="darwin", arch="universal")
        candidate = self.manifest(b"next zip", tag="nightly-20261026-12346",
                                  released="2026-10-26T04:00:00Z",
                                  platform="darwin", arch="universal")
        self.assertEqual(common.nightly_pair(previous, candidate, "darwin", "arm64"),
                         (previous["artifacts"][0], candidate["artifacts"][0]))

    def test_pair_refuses_different_semvers_or_non_plain_semver(self):
        for version in ("0.9.0", "0.8.0-nightly.1", "0.8.0+build", "v0.8.0"):
            previous, candidate = self.pair()
            candidate["version"] = version
            with self.subTest(version=version), self.assertRaises(common.ProofError):
                common.nightly_pair(previous, candidate, "linux", "amd64")
        for version in ("0.8.0-nightly.1", "0.8.0+build", "v0.8.0", "01.8.0"):
            previous, candidate = self.pair()
            previous["version"] = candidate["version"] = version
            with self.subTest(both_versions=version), self.assertRaises(common.ProofError):
                common.nightly_pair(previous, candidate, "linux", "amd64")

    def test_pair_refuses_unchanged_bytes_or_the_same_published_tag(self):
        previous, candidate = self.pair()
        candidate["artifacts"][0]["sha256"] = previous["artifacts"][0]["sha256"]
        with self.assertRaises(common.ProofError):
            common.nightly_pair(previous, candidate, "linux", "amd64")
        previous, candidate = self.pair()
        candidate["artifacts"][0]["url"] = previous["artifacts"][0]["url"]
        candidate["notesUrl"] = previous["notesUrl"]
        with self.assertRaises(common.ProofError):
            common.nightly_pair(previous, candidate, "linux", "amd64")

    def test_pair_compares_release_instants_not_timestamp_text(self):
        previous, candidate = self.pair()
        previous["released"] = "2026-10-25T23:30:00-02:00"
        candidate["released"] = "2026-10-26T01:45:00Z"
        common.nightly_pair(previous, candidate, "linux", "amd64")
        candidate["released"] = "2026-10-26T01:30:00Z"
        with self.assertRaises(common.ProofError):
            common.nightly_pair(previous, candidate, "linux", "amd64")

    def test_pair_refuses_non_increasing_or_timezone_free_release_times(self):
        previous, _ = self.pair()
        for released in ("2026-10-24T04:00:00Z", previous["released"],
                         "2026-10-26T04:00:00"):
            _, candidate = self.pair()
            candidate["released"] = released
            with self.subTest(released=released), self.assertRaises(common.ProofError):
                common.nightly_pair(previous, candidate, "linux", "amd64")

    def test_pair_requires_rfc3339_not_relaxed_isoformat(self):
        for released in ("2026-10-26 04:00:00+00:00", "20261026T040000+00:00"):
            previous, candidate = self.pair()
            candidate["released"] = released
            with self.subTest(released=released), self.assertRaises(common.ProofError):
                common.nightly_pair(previous, candidate, "linux", "amd64")

    def test_pair_refuses_mutable_foreign_or_mismatched_nightly_urls(self):
        bad_urls = [
            "https://github.com/shady2k/nocx/releases/download/nightly/nocx.AppImage",
            "https://github.com/shady2k/nocx/releases/latest/download/nocx.AppImage",
            "https://example.com/shady2k/nocx/releases/download/nightly-20261026-12346/a",
            "https://github.com/another/nocx/releases/download/nightly-20261026-12346/a",
            "https://github.com/shady2k/nocx/releases/download/nightly-20261026-run/a",
            "https://github.com/shady2k/nocx/releases/download/nightly-20261026-12346/a?x=1",
            "https://github.com/shady2k/nocx/releases/download/nightly-20261026-12346/a#x",
        ]
        for url in bad_urls:
            for altered_side in (0, 1):
                pair = self.pair()
                pair[altered_side]["artifacts"][0]["url"] = url
                with self.subTest(url=url, side=altered_side), self.assertRaises(common.ProofError):
                    common.nightly_pair(*pair, "linux", "amd64")
        for altered_side in (0, 1):
            pair = self.pair()
            pair[altered_side]["notesUrl"] = (
                "https://github.com/shady2k/nocx/releases/tag/nightly-20261024-999")
            with self.subTest(notes_side=altered_side), self.assertRaises(common.ProofError):
                common.nightly_pair(*pair, "linux", "amd64")


if __name__ == "__main__":
    unittest.main()
