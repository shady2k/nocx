"""Offline assertions for the macOS driver, not evidence of a real app update."""
import importlib.util
from pathlib import Path
import unittest
import tempfile

SPEC = importlib.util.spec_from_file_location("macos", Path(__file__).with_name("prove-macos.py"))
macos = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(macos)


class MacProofAssertions(unittest.TestCase):
    def test_same_semver_requires_new_bytes_and_a_new_commit(self):
        macos.assert_transition("0.8.0", "0.8.0", "aaa", "bbb", "111", "222")
        with self.assertRaisesRegex(RuntimeError, "bytes"):
            macos.assert_transition("0.8.0", "0.8.0", "aaa", "aaa", "111", "222")
        with self.assertRaisesRegex(RuntimeError, "commit"):
            macos.assert_transition("0.8.0", "0.8.0", "aaa", "bbb", "111", "111")

    def test_pair_diagnostics_cannot_accept_old_same_version_coordinator(self):
        with self.assertRaisesRegex(RuntimeError, "Commit"):
            macos.assert_diagnostics("Version: 0.8.0\nCommit: old\n", "0.8.0", "new")
        macos.assert_diagnostics("nocx\nVersion: 0.8.0\nCommit: new\n", "0.8.0", "new")

    def test_version_is_read_from_the_actual_packaged_binary(self):
        self.assertEqual(macos.parse_version("nocx 0.8.0 (commit abc123, built now)\n"), ("0.8.0", "abc123"))
        with self.assertRaisesRegex(RuntimeError, "metadata"):
            macos.parse_version("nocx dev")

    def test_no_publication_match_on_a_different_signed_manifest(self):
        self.assertFalse(macos.same_publication(b"previous", b"sig", b"next", b"sig"))
        self.assertFalse(macos.same_publication(b"next", b"other", b"next", b"sig"))
        self.assertTrue(macos.same_publication(b"next", b"sig\n", b"next", b"sig"))

    def test_publication_barrier_rejects_candidate_already_live(self):
        with self.assertRaisesRegex(RuntimeError, "before old app was ready"):
            macos.assert_previous_running_publication(b"candidate", b"sig2", b"previous", b"sig1")
        macos.assert_previous_running_publication(b"previous", b"sig1", b"previous", b"sig1")

    def test_existing_entries_including_dangling_symlinks_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            entry = Path(directory) / "nocx.app"
            macos.require_absent(entry)
            entry.symlink_to(Path(directory) / "missing")
            with self.assertRaisesRegex(RuntimeError, "existing path"):
                macos.require_absent(entry)
            entry.unlink()
            entry.mkdir()
            with self.assertRaises(RuntimeError):
                macos.require_absent(entry)

    def test_partial_diagnostics_never_match(self):
        with self.assertRaises(RuntimeError):
            macos.assert_diagnostics("Version: 0.8.0\n", "0.8.0", "new")

    def test_unparseable_or_development_build_metadata_is_refused(self):
        for text in ["nocx dev (commit none, built unknown)", "nocx 0.8.0 (commit unknown, built now)"]:
            with self.assertRaises(RuntimeError):
                macos.parse_version(text)


if __name__ == "__main__":
    unittest.main()
