#!/usr/bin/env python3
"""Unit checks for the proof's coordinator identity policy, not app acceptance."""

import importlib.util
from pathlib import Path
import sys
import tempfile
from unittest import mock
import unittest


DIRECTORY = Path(__file__).resolve().parent
sys.path.insert(0, str(DIRECTORY))
SPEC = importlib.util.spec_from_file_location("update_proof_linux", DIRECTORY / "prove-linux.py")
proof = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(proof)


class CoordinatorTests(unittest.TestCase):
    def test_version_increase_requires_candidate_coordinator(self):
        initial = [{"executable": "/old/nocx-server", "sha256": "old-server"}]
        self.assertEqual(proof.permitted_coordinators(
            {"version": "0.8.0"}, {"version": "0.8.1"}, initial,
            {"coordinator": "new-server"}), {"new-server"})

    def test_restart_observation_refuses_old_coordinator_and_explains_why(self):
        for equal_version in (False, True):
            with self.subTest(equal_version=equal_version), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                backup, journal = root / "backup", root / "journal"
                backup.touch()
                window = {"pid": 2, "executable": "/new/nocx", "sha256": "new-window",
                          "appimage": str(root / "nocx.AppImage")}
                old_server = {"pid": 3, "executable": "/old/nocx-server", "sha256": "old-server"}
                reading = proof.RestartObservation(
                    {"version": "0.8.0"}, {"version": "0.8.0" if equal_version else "0.8.1"},
                    [old_server], {"coordinator": "new-server", "window": "new-window"},
                    root / "nocx.AppImage", {1}, root, journal, backup)
                with mock.patch.object(proof, "profile_processes", return_value=[window, old_server]):
                    self.assertEqual(bool(reading.observe()), equal_version)
                diagnostic = reading.describe()
                self.assertIn("old-server", diagnostic)
                if not equal_version:
                    self.assertIn("version change requires candidate-only coordinator", diagnostic)
                    self.assertIn("expected coordinator SHA-256=['new-server']", diagnostic)
                    with self.assertRaisesRegex(proof.ProofError, "candidate-only coordinator"):
                        proof.wait_for("restart", reading.observe, timeout=0,
                                       describe=reading.describe)
                new_server = dict(old_server, sha256="new-server")
                with mock.patch.object(proof, "profile_processes", return_value=[window, new_server]):
                    self.assertEqual(reading.observe(), [window, new_server])
                    journal.touch()
                    self.assertIsNone(reading.observe())
                    self.assertIn("journal exists=True", reading.describe())

    def test_equal_version_permits_existing_coordinator(self):
        initial = [{"executable": "/old/nocx-server", "sha256": "old-server"},
                   {"executable": "/old/nocx", "sha256": "old-window"}]
        self.assertEqual(proof.permitted_coordinators(
            {"version": "0.8.0"}, {"version": "0.8.0"}, initial,
            {"coordinator": "new-server"}), {"new-server", "old-server"})


if __name__ == "__main__":
    unittest.main()
