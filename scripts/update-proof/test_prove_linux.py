#!/usr/bin/env python3
"""Unit checks for the proof's coordinator identity policy, not app acceptance."""

import contextlib
import importlib.util
import io
import json
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


class StableSelectionTests(unittest.TestCase):
    """Which stable releases the proof would prove, decided without the network."""

    def test_selects_by_semver_not_text_or_list_order(self):
        tags = ["v0.10.0", "v0.8.10", "v0.9.0"]
        self.assertEqual(proof.stable_releases(tags), ["v0.8.10", "v0.9.0", "v0.10.0"])

    def test_the_pair_is_the_newest_promoted_stable_and_the_one_below_it(self):
        code, result = proof.stable_status(
            ["v0.7.2", "v0.8.0", "v0.8.1"], promoted="v0.8.1")
        self.assertEqual(code, 0)
        self.assertEqual(result["result"], "ready")
        self.assertEqual((result["candidate"], result["previous"], result["lower"]),
                         ("v0.8.1", "v0.8.0", "v0.7.2"))

    def test_a_promoted_stable_that_is_not_the_newest_fails_closed(self):
        for promoted in ("v0.8.0", "v0.8.2"):
            with self.subTest(promoted=promoted):
                code, result = proof.stable_status(
                    ["v0.7.2", "v0.8.0", "v0.8.1"], promoted=promoted)
                self.assertEqual(code, 1)
                self.assertEqual(result["result"], "failed")
                self.assertIn("refusing to prove another run's artifact", result["reason"])

    def test_blocked_is_exit_two_and_a_waiting_state_never_a_pass(self):
        for tags in ([], ["v0.8.1"], ["v0.8.0", "v0.8.1"]):
            with self.subTest(tags=tags):
                code, result = proof.stable_status(tags, promoted="v0.8.1")
                self.assertEqual(code, 2)
                self.assertEqual(result["result"], "blocked")

    def test_an_unreadable_release_tag_fails_closed_instead_of_being_skipped(self):
        for tag in ("v0.8", "0.8.0", "v01.8.0", "v0.8.0-rc.1", "nightly-20261025-1"):
            with self.subTest(tag=tag), \
                    self.assertRaisesRegex(proof.ProofError, "vMAJOR.MINOR.PATCH"):
                proof.stable_releases(["v0.7.2", "v0.8.0", tag])
            # A broken release set is a failure, not the "wait for more
            # releases" state that exit 2 reports.
            code, result = proof.stable_status(["v0.7.2", "v0.8.0", tag])
            self.assertEqual((code, result["result"]), (1, "failed"))

    def test_promoted_requires_the_stable_channel(self):
        with mock.patch.object(sys, "argv",
                               ["prove-linux.py", "--publication-status",
                                "--promoted", "v0.8.1"]):
            with self.assertRaises(SystemExit) as refused:
                proof.main()
        self.assertEqual(refused.exception.code, 2)


class PublicationStatusTests(unittest.TestCase):
    """What the readiness command reports, without reading GitHub."""

    def releases(self):
        return [
            {"tag_name": "v0.8.1", "prerelease": False, "draft": False},
            {"tag_name": "nightly-20261025-1", "prerelease": True, "draft": False},
            {"tag_name": "v0.8.0", "prerelease": False, "draft": False},
            {"tag_name": "v0.7.2", "prerelease": False, "draft": False},
            {"tag_name": "v0.9.0", "prerelease": False, "draft": True},
        ]

    def status(self, channel, promoted=None, releases=None):
        payload = io.BytesIO(json.dumps(releases or self.releases()).encode())
        output = io.StringIO()
        with mock.patch.object(proof, "urlopen", return_value=payload), \
                contextlib.redirect_stdout(output):
            code = proof.publication_status(channel, promoted)
        return code, json.loads(output.getvalue())

    def test_stable_readiness_ignores_prereleases_and_drafts(self):
        code, result = self.status("stable", "v0.8.1")
        self.assertEqual(code, 0)
        self.assertEqual((result["candidate"], result["previous"], result["lower"]),
                         ("v0.8.1", "v0.8.0", "v0.7.2"))
        self.assertNotIn("nightly-20261025-1", result["tags"])
        self.assertNotIn("v0.9.0", result["tags"])

    def test_nightly_readiness_still_counts_only_dated_nightlies(self):
        code, result = self.status("nightly")
        self.assertEqual(code, 2)
        self.assertEqual(result["tags"], ["nightly-20261025-1"])
        two = self.releases() + [{"tag_name": "nightly-20261026-2",
                                  "prerelease": True, "draft": False}]
        code, result = self.status("nightly", releases=two)
        self.assertEqual(code, 0)
        self.assertEqual(result["result"], "ready")


class PromotedStableTests(unittest.TestCase):
    """A stable proof must be about the release this run promoted."""

    def args(self, channel, promoted):
        return mock.Mock(channel=channel, promoted=promoted)

    def test_stable_proof_refuses_a_candidate_that_is_not_the_promoted_tag(self):
        proof.require_promoted(self.args("stable", "v0.8.1"), {"version": "0.8.1"})
        for promoted in (None, "v0.8.0", "v0.9.0"):
            with self.subTest(promoted=promoted), \
                    self.assertRaises(proof.ProofError):
                proof.require_promoted(self.args("stable", promoted), {"version": "0.8.1"})

    def test_nightly_proof_has_no_promoted_stable_tag_to_match(self):
        proof.require_promoted(self.args("nightly", None), {"version": "0.8.1"})
        proof.require_promoted(self.args("nightly", "v9.9.9"), {"version": "0.8.1"})


class LowerProbeTests(unittest.TestCase):
    """The downgrade probe must actually be below the app that installs."""

    def manifest(self, version):
        return {"version": version}

    def test_nightly_keeps_the_latest_stable_probe_even_when_it_equals_previous(self):
        # The first nightly after a promotion: previous 0.8.0, this run 0.8.1,
        # and releases/latest is still 0.8.0. That is the window
        # nocx-vh2hr.1.3 exists for; it must keep passing.
        proof.require_lower_probe("nightly", self.manifest("0.8.0"),
                                  self.manifest("0.8.0"), self.manifest("0.8.1"))

    def test_stable_probe_must_be_below_the_installed_previous(self):
        proof.require_lower_probe("stable", self.manifest("0.7.2"),
                                  self.manifest("0.8.0"), self.manifest("0.8.1"))
        with self.assertRaisesRegex(proof.ProofError, "below the installed previous"):
            proof.require_lower_probe("stable", self.manifest("0.8.0"),
                                      self.manifest("0.8.0"), self.manifest("0.8.1"))

    def test_a_probe_at_or_above_the_candidate_is_refused_on_every_channel(self):
        for channel in ("nightly", "stable"):
            for lower in ("0.8.1", "0.9.0"):
                with self.subTest(channel=channel, lower=lower), \
                        self.assertRaisesRegex(proof.ProofError, "below the candidate"):
                    proof.require_lower_probe(channel, self.manifest(lower),
                                              self.manifest("0.8.0"), self.manifest("0.8.1"))


if __name__ == "__main__":
    unittest.main()
