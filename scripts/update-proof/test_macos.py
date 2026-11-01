#!/usr/bin/env python3
"""Unit checks for the macOS proof's step naming and layout, not app acceptance.

The step table is the driver's whole diagnostic value (nocx-vh2hr.1.6): when an
update fails, what a person needs is WHICH step refused. These checks pin the
table to the updater's own vocabulary, so a reworded log line breaks a test
instead of quietly turning a named failure back into "the update failed".
"""

import importlib.util
from pathlib import Path
import sys
import unittest


DIRECTORY = Path(__file__).resolve().parent
REPOSITORY = DIRECTORY.parent.parent
sys.path.insert(0, str(DIRECTORY))
SPEC = importlib.util.spec_from_file_location("update_proof_macos", DIRECTORY / "prove-macos.py")
proof = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(proof)


class StepTests(unittest.TestCase):
    def test_the_step_reached_is_the_last_step_the_updater_logged(self):
        output = "\n".join([
            'level=INFO msg="update apply: journal written, starting download" from=0.8.0 to=0.8.1',
            'level=INFO msg="update apply: download artefact"',
            'level=INFO msg="update apply: extract"',
        ])
        self.assertEqual(proof.step_reached(output), "extraction (ditto)")

    def test_a_failed_run_names_the_step_it_did_not_get_past(self):
        output = "\n".join([
            'level=INFO msg="update apply: extract"',
            'level=ERROR msg="update apply: verify extracted: codesign verification of /x failed"',
        ])
        self.assertEqual(proof.failing_step(output, "update apply: verify extracted: boom"),
                         "bundle seal (codesign/lipo)")

    def test_a_run_that_logged_nothing_falls_back_to_the_error(self):
        self.assertEqual(proof.failing_step("", "update check: manifest verification: refused"),
                         "signature verification (manifest)")
        self.assertEqual(proof.failing_step("", "something else entirely"),
                         "start (the updater logged nothing)")

    def test_the_pair_check_is_named_from_the_apps_own_lines(self):
        output = 'level=WARN msg="report healthy: refused — a mixed pair answered" coordinator=0.8.0'
        self.assertEqual(proof.step_reached(output), "pair check (mixed pair)")
        certified = 'level=INFO msg="report healthy: pair certified" version=0.8.1'
        self.assertEqual(proof.step_reached(certified), "pair check")

    def test_every_step_fragment_is_the_updaters_own_vocabulary(self):
        # One owner: the strings the driver classifies live in internal/update.
        # A log line reworded there without this table is a diagnosis that
        # silently stops naming the step.
        source = "".join(path.read_text() for path in (REPOSITORY / "internal/update").glob("*.go")
                         if not path.name.endswith("_test.go"))
        for fragment, step in proof.STEPS:
            with self.subTest(step=step):
                self.assertIn(fragment, source,
                              f"{fragment!r} is not in internal/update's messages any more")

    def test_the_steps_are_in_transaction_order(self):
        # The table's order is the tie-break when one line carries two
        # fragments; a step listed before the step that must precede it would
        # make the earlier one win.
        names = [name for _, name in proof.STEPS]
        self.assertEqual(names[0], "download the release manifest")
        self.assertLess(names.index("download"), names.index("extraction (ditto)"))
        self.assertLess(names.index("extraction (ditto)"), names.index("bundle seal (codesign/lipo)"))
        self.assertLess(names.index("bundle seal (codesign/lipo)"),
                        names.index("bundle swap (renameatx_np)"))
        self.assertEqual(names[-1], "pair check")


class VersionTests(unittest.TestCase):
    def test_the_packaged_binarys_own_build_metadata(self):
        self.assertEqual(
            proof.parse_version("nocx 0.8.1 (commit 0123456789abcdef0123456789abcdef01234567, "
                                "built 2026-10-30T07:50:35Z)\n"),
            {"version": "0.8.1", "commit": "0123456789abcdef0123456789abcdef01234567"})

    def test_a_development_build_has_no_release_metadata(self):
        with self.assertRaises(proof.ProofError):
            proof.parse_version("nocx dev (commit none, built unknown)\n")

    def test_unrelated_output_is_refused_rather_than_guessed(self):
        with self.assertRaises(proof.ProofError):
            proof.parse_version("Usage: nocx [--version]\n")


class LayoutTests(unittest.TestCase):
    def test_the_layout_is_the_products_own(self):
        layout = proof.install_layout("/Users/runner/proof/home")
        self.assertEqual(str(layout["install"]), "/Users/runner/proof/home/Applications/nocx.app")
        self.assertEqual(str(layout["journal"]),
                         "/Users/runner/proof/home/Library/Application Support/nocx/"
                         ".nocx-update-journal.json")
        self.assertEqual(str(layout["log"]), "/Users/runner/proof/home/Library/Logs/nocx/nocx.log")
        self.assertEqual(proof.bundle_binary(layout["install"]).name, "nocx")

    def test_the_bundle_identifier_is_the_one_the_release_build_stamps(self):
        # The driver reads Info.plist instead of trusting a path; the identifier
        # is asserted against the one release-build.yml stamps.
        packaging = (REPOSITORY / ".github/workflows/release-build.yml").read_text()
        self.assertTrue("dev.nocx" in packaging,
                        "release-build.yml no longer stamps dev.nocx; the driver still expects it")


if __name__ == "__main__":
    unittest.main()
