#!/usr/bin/env python3
"""Unit checks for the macOS proof's step naming and layout, not app acceptance.

The step table is the driver's whole diagnostic value (nocx-vh2hr.1.6): when an
update fails, what a person needs is WHICH step refused. These checks pin the
table to the updater's own vocabulary, so a reworded log line breaks a test
instead of quietly turning a named failure back into "the update failed".
"""

import importlib.util
from pathlib import Path
import shutil
import sys
import tempfile
import time
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
        # One owner: the strings the driver classifies live in internal/update
        # and in the packaged diagnostic that first says "starting".
        # A log line reworded there without this table is a diagnosis that
        # silently stops naming the step.
        sources = [path.read_text() for path in (REPOSITORY / "internal/update").glob("*.go")
                   if not path.name.endswith("_test.go")]
        sources.append((REPOSITORY / "release_diagnostics.go").read_text())
        source = "".join(sources)
        for fragment, step in proof.STEPS:
            with self.subTest(step=step):
                self.assertIn(fragment, source,
                              f"{fragment!r} is not in internal/update's messages any more")

    def test_the_steps_are_in_transaction_order(self):
        # The table's order is the tie-break when one line carries two
        # fragments; a step listed before the step that must precede it would
        # make the earlier one win.
        names = [name for _, name in proof.STEPS]
        self.assertEqual(names[0], "start the updater")
        self.assertLess(names.index("start the updater"), names.index("download the release manifest"))
        self.assertLess(names.index("download the release manifest"), names.index("download"))
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


class ArgumentTests(unittest.TestCase):
    """The driver has to START before it can prove anything.

    Nightly 41398991857 died in main() before its first step:
    `AttributeError: 'int' object has no attribute 'resolve'`. The
    normalisation resolved EVERY argument, and `--ui-timeout` is an int.
    """

    # The exact command line nightly.yml's prove-macos step runs, with paths
    # relative to the runner's workspace as they are there.
    NIGHTLY = [
        "--previous-manifest", "proof-inputs/previous/manifest.json",
        "--previous-signature", "proof-inputs/previous/manifest.json.sig",
        "--candidate-manifest", "proof-inputs/candidate/manifest.json",
        "--candidate-signature", "proof-inputs/candidate/manifest.json.sig",
        "--verifier", "/tmp/manifest-sign",
        "--work", "/tmp/nocx-macos-proof",
        "--evidence", "proof-results/macos.json",
    ]

    def test_the_nightly_command_line_is_normalised_and_starts(self):
        args = proof.parse_arguments(self.NIGHTLY)
        self.assertIsInstance(args.ui_timeout, int)
        self.assertEqual(args.ui_timeout, 300, "the default the workflow relies on")
        self.assertEqual(str(args.work), "/tmp/nocx-macos-proof")
        self.assertEqual(str(args.evidence), str(Path("proof-results/macos.json").resolve()))
        self.assertEqual(str(args.previous_manifest),
                         str(Path("proof-inputs/previous/manifest.json").resolve()))
        self.assertIsInstance(args.previous_signature, Path)

    def test_an_explicit_ui_timeout_is_kept_as_an_integer(self):
        args = proof.parse_arguments(self.NIGHTLY + ["--ui-timeout", "45"])
        self.assertEqual(args.ui_timeout, 45)
        self.assertIsInstance(args.work, Path)


class LayoutTests(unittest.TestCase):
    def test_the_layout_is_the_products_own(self):
        layout = proof.install_layout("/Users/runner/proof/home")
        self.assertEqual(str(layout["install"]), "/Users/runner/proof/home/Applications/nocx.app")
        self.assertEqual(str(layout["journal"]),
                         "/Users/runner/proof/home/Library/Application Support/nocx/"
                         ".nocx-update-journal.json")
        # internal/app opens <DataDir>/nocx.log; on darwin DataDir is the
        # profile under Application Support. ~/Library/Logs/nocx is not a path
        # this product writes, and copying it collected nothing.
        self.assertEqual(str(layout["log"]),
                         "/Users/runner/proof/home/Library/Application Support/nocx/nocx.log")
        self.assertEqual(str(layout["windowLog"]),
                         "/Users/runner/proof/home/Library/Application Support/nocx/"
                         "nocx-window.log")
        self.assertEqual(proof.bundle_binary(layout["install"]).name, "nocx")

    def test_the_bundle_identifier_is_the_one_the_release_build_stamps(self):
        # The driver reads Info.plist instead of trusting a path; the identifier
        # is asserted against the one release-build.yml stamps.
        packaging = (REPOSITORY / ".github/workflows/release-build.yml").read_text()
        self.assertTrue("dev.nocx" in packaging,
                        "release-build.yml no longer stamps dev.nocx; the driver still expects it")


class DiagnosticCapabilityTests(unittest.TestCase):
    """The installed build must know the driver before it is asked to run it.

    The previous nightly used by the failed run (commit 92aba45) predates
    --apply-release-update. Handed the flag it did not refuse it: it treated it
    as an unknown argument and started the window, which on the headless runner
    never exits. The proof waited thirty minutes and could only say "the updater
    logged nothing" (nightly 41407342443). The driver's usage line is embedded
    in the binary, so the capability is decidable without starting anything.
    """

    def test_a_build_that_carries_the_usage_line_has_the_driver(self):
        self.assertTrue(proof.apply_diagnostic_present(
            "usage: --apply-release-update [--channel stable|nightly]"))

    def test_a_build_packaged_before_the_driver_does_not(self):
        self.assertFalse(proof.apply_diagnostic_present(
            "usage: nocx [--version]\n--verify-release-manifest\n--build-info\n"))

    def test_the_refusal_is_named_as_a_start_failure(self):
        self.assertEqual(
            proof.failing_step("", "the installed build has no "
                               "--apply-release-update diagnostic: /x was "
                               "packaged before the driver existed, so asking "
                               "it to update would start the app instead"),
            "start (the installed build has no update diagnostic)")

    def test_a_build_without_the_driver_is_refused_before_it_starts(self):
        class Result:
            returncode = 0
            stdout = "no update diagnostic in these strings"
            stderr = ""

        original = proof.command
        proof.command = lambda argv, **kwargs: Result()
        self.addCleanup(setattr, proof, "command", original)
        with self.assertRaises(proof.ProofError) as caught:
            proof.require_apply_diagnostic("/proof/home/Applications/nocx.app/Contents/MacOS/nocx")
        self.assertIn("has no --apply-release-update diagnostic", str(caught.exception))


class ApplicatorTests(unittest.TestCase):
    """The diagnostic's silence and its output both have deadlines.

    subprocess.run's timeout discards the captured output, so the failed run's
    evidence named no step even though the answering line was already in the
    pipe. A diagnostic that never reaches the updater has said nothing at all,
    and that is a different failure from one that reached a step and stalled.
    """

    def bundle(self, script):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root, ignore_errors=True)
        app = root / "Applications" / "nocx.app"
        binary = app / "Contents" / "MacOS" / "nocx"
        binary.parent.mkdir(parents=True)
        binary.write_text("#!/bin/sh\n" + script)
        binary.chmod(0o755)
        home = root / "home"
        home.mkdir()
        return app, home

    def test_a_silent_updater_is_killed_at_the_startup_deadline(self):
        app, home = self.bundle("sleep 30\n")
        started = time.monotonic()
        with self.assertRaises(proof.ProofError) as caught:
            proof.run_applicator(app, home, "nightly", timeout=30, start_timeout=0.2)
        self.assertIn("logged nothing", str(caught.exception))
        self.assertLess(time.monotonic() - started, 10,
                        "a silent run was waited out instead of killed")

    def test_a_stalled_update_keeps_the_lines_it_printed(self):
        app, home = self.bundle(
            'echo ' + "'" + 'level=INFO msg="update apply: starting" channel=nightly' + "'" + '\n'
            "sleep 30\n")
        with self.assertRaises(proof.ProofError) as caught:
            proof.run_applicator(app, home, "nightly", timeout=1, start_timeout=0.5)
        text = str(caught.exception)
        self.assertIn("start the updater", text)
        self.assertIn("update apply: starting", text)

    def test_a_complete_run_returns_its_result_line(self):
        app, home = self.bundle(
            'echo ' + "'" + 'level=INFO msg="update apply: journal written, starting download"' + "'" + '\n'
            'echo ' + "'" + '{"step":"applied","channel":"nightly","toVersion":"0.8.1"}' + "'" + '\n')
        payload, reached, output = proof.run_applicator(app, home, "nightly", timeout=30)
        self.assertEqual(payload["step"], "applied")
        self.assertEqual(reached, "journal")
        self.assertIn("journal written", output)

    def test_a_failing_run_names_its_step_and_keeps_its_output(self):
        app, home = self.bundle(
            'echo ' + "'" + 'level=INFO msg="update apply: extract"' + "'" + '\n'
            'echo "codesign refused the staged bundle" >&2\n'
            "exit 1\n")
        with self.assertRaises(proof.ProofError) as caught:
            proof.run_applicator(app, home, "nightly", timeout=30)
        self.assertIn("extraction (ditto)", str(caught.exception))


class ExchangeTests(unittest.TestCase):
    """What a complete exchange leaves, and what it must not (nocx-vh2hr.1.22).

    Nightly 42362329510 failed this step with "the rollback peer is still there
    after a complete exchange", one line after the updater logged "exchange
    complete". The peer WAS the exchange: RENAME_SWAP swaps the two names, so
    .nocx-swap.app holds the previous nightly and is the rollback copy §7.5
    needs. The retained backup is the next step's, created only after the
    restarted build certifies its pair.
    """

    def tree(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root, ignore_errors=True)
        applications = root / "Applications"
        layout = {"install": applications / "nocx.app"}

        def bundle(app, contents):
            binary = proof.bundle_binary(app)
            binary.parent.mkdir(parents=True)
            binary.write_text(contents)
            return proof.sha256(binary)

        candidate_sha = bundle(layout["install"], "the candidate's bytes")
        previous_sha = bundle(applications / ".nocx-swap.app", "the previous nightly")
        return applications, layout, candidate_sha, previous_sha

    def test_a_complete_exchange_leaves_the_previous_nightly_as_the_rollback_peer(self):
        applications, layout, candidate_sha, previous_sha = self.tree()
        swap, backup = proof.exchange_step(layout, candidate_sha, previous_sha)
        self.assertEqual(swap, applications / ".nocx-swap.app")
        self.assertEqual(backup, applications / ".nocx-backup.app")
        self.assertFalse(backup.exists(), "finalisation has not run yet")

    def test_a_missing_peer_is_a_missing_rollback_copy_not_leftover_debris(self):
        applications, layout, candidate_sha, previous_sha = self.tree()
        shutil.rmtree(applications / ".nocx-swap.app")
        with self.assertRaises(proof.ProofError) as caught:
            proof.exchange_step(layout, candidate_sha, previous_sha)
        self.assertIn("rollback peer", str(caught.exception))

    def test_a_peer_that_is_not_the_previous_nightly_is_refused(self):
        applications, layout, candidate_sha, previous_sha = self.tree()
        proof.bundle_binary(applications / ".nocx-swap.app").write_text("something else")
        with self.assertRaisesRegex(proof.ProofError, "previous nightly"):
            proof.exchange_step(layout, candidate_sha, previous_sha)

    def test_a_backup_before_the_restart_is_refused(self):
        applications, layout, candidate_sha, previous_sha = self.tree()
        backup = proof.bundle_binary(applications / ".nocx-backup.app")
        backup.parent.mkdir(parents=True)
        backup.write_text("the previous nightly")
        with self.assertRaises(proof.ProofError) as caught:
            proof.exchange_step(layout, candidate_sha, previous_sha)
        self.assertIn("before the restart", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
