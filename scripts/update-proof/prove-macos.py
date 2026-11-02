#!/usr/bin/env python3
"""Run an installed macOS nightly through the real updater to the next nightly.

The macOS half of the installed-app proof (nocx-1ebrs.35.5), shaped like
prove-linux.py: the same manifests, the same signature authority, the same
evidence file and the same four assertions — verify, swap, relaunch, healthy.
What differs is macOS's alone: a .app bundle installed from the signed zip,
ditto + codesign + lipo as the extraction and seal checks, the bundle exchange
of internal/update/platform_darwin.go, and the pair check in
internal/update/pair.go that certifies the window against the coordinator that
answered it.

The update itself is the window's own updater, run through the packaged
diagnostic `--apply-release-update` (release_diagnostics.go) — not a
substitute for it and not a mocked call. A hosted macOS runner grants no
scripted Accessibility permission, so the one trigger a person uses (Install in
Settings) cannot be clicked by a driver; the diagnostic calls the same
Check/Apply the window's binding calls, from inside the installed bundle. The
RELAUNCH is the real one: the driver starts the installed app afterwards, its
renderer reports healthy, and finalisation certifies the pair.

When anything fails the driver names the step — download, manifest signature,
extraction, bundle seal, bundle swap, relaunch, pair check — and prints the
updater's own log lines for it. That is what nocx-vh2hr.1.6 asks the owner to
collect by hand.
"""

import argparse
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import signal
import subprocess
import sys
import threading
import time

from common import (ProofError, artifact, command, download_artifact, nightly_pair,
                    read_pending, require, sha256, verify_manifest)

# The updater's own vocabulary, in the order the transaction reaches it. Each
# entry is (fragment of an updater log line or error, step the driver reports).
# One owner of the step names: the failure message and the evidence both read
# this table, and its test pins the fragments to internal/update's messages.
STEPS = (
    ("update apply: starting", "start the updater"),
    ("update check: fetch manifest", "download the release manifest"),
    ("update check: manifest verification", "signature verification (manifest)"),
    ("update check: no artefact", "artifact selection"),
    ("update apply: preflight refused", "preflight (Applications not writable)"),
    ("update apply: journal written", "journal"),
    ("update apply: download artefact", "download"),
    ("update apply: extract", "extraction (ditto)"),
    ("update apply: verify extracted", "bundle seal (codesign/lipo)"),
    ("update apply: rename staged", "bundle swap (renameatx_np)"),
    ("update apply: exchange", "bundle swap (renameatx_np)"),
    ("report healthy: refused — a mixed pair answered", "pair check (mixed pair)"),
    ("report healthy: refused — the coordinator could not be identified", "pair check (probe)"),
    ("report healthy: refused — the reporting build is not the installed version", "pair check (version)"),
    ("report healthy: pair certified", "pair check"),
)

# The update driver is a capability of the INSTALLED build. A build packaged
# before the driver existed does not refuse the flag — it treats it as unknown
# and starts the window, which on a headless runner never exits. That is a
# driver-side refusal, so no updater line can name it.
APPLY_DIAGNOSTIC = "--apply-release-update"
DRIVER_REFUSALS = (
    ("has no " + APPLY_DIAGNOSTIC + " diagnostic",
     "start (the installed build has no update diagnostic)"),
)

# Which step a run of output got as far as: the LAST step fragment seen is the
# step in flight, and a failure is in that step.
def step_reached(output):
    step = "start"
    for line in output.splitlines():
        for fragment, name in STEPS:
            if fragment in line:
                step = name
    return step


def failing_step(output, error_text=""):
    """The step a failed run did not get past.

    The updater logs the step it is entering before doing it, so the last one
    logged is the one that failed; the error text is used only when the run
    logged nothing (a crash before the first line).
    """
    reached = step_reached(output)
    if reached != "start":
        return reached
    for fragment, name in DRIVER_REFUSALS:
        if fragment in error_text:
            return name
    for fragment, name in STEPS:
        if fragment in error_text:
            return name
    return "start (the updater logged nothing)"


VERSION_LINE = re.compile(r"^nocx ([0-9]+\.[0-9]+\.[0-9]+) \(commit ([0-9a-f]+), built [^)]*\)\s*$",
                          re.MULTILINE)


def parse_version(text):
    """The build a packaged binary reports: version and the commit it links."""
    match = VERSION_LINE.search(text)
    require(match is not None, "packaged binary has no release build metadata: " + text.strip())
    return {"version": match.group(1), "commit": match.group(2)}


def install_layout(home):
    """Where the product puts everything a macOS install owns.

    Read from the layout internal/update and internal/storage define, never
    guessed: the bundle in the person's Applications folder, the journal and
    the swap/backup peers beside it, the log the app writes for itself.
    """
    home = Path(home)
    return {
        "applications": home / "Applications",
        "install": home / "Applications" / "nocx.app",
        "journal": home / "Library/Application Support/nocx/.nocx-update-journal.json",
        # internal/app opens <DataDir>/nocx.log, and on darwin internal/storage
        # makes DataDir the profile under Application Support. There is no
        # ~/Library/Logs/nocx entry in this product; the old path here was a
        # guess, and the copy step kept finding nothing (nocx-vh2hr.1.11).
        "log": home / "Library/Application Support/nocx/nocx.log",
        # The WINDOW process's own file (nocx-vh2hr.1.12): Check/Apply/
        # ReportHealthy run in the window, not in nocx-server, so this is
        # where an update that refused after the swap says why. It is a
        # sibling of the backend's file under the profile, owned solely by
        # the window.
        "windowLog": home / "Library/Application Support/nocx/nocx-window.log",
        "profile": home / "Library/Application Support/nocx",
    }


def bundle_binary(app):
    return Path(app) / "Contents/MacOS/nocx"


def wait_for(description, probe, timeout=120, describe=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = probe()
        if value:
            return value
        threading.Event().wait(0.25)
    if describe:
        try:
            description += " (last reading: " + describe() + ")"
        except Exception as error:
            description += " (last reading failed: " + str(error) + ")"
    raise ProofError("not observed: " + description)


def verify_bundle(app):
    """What the product's own VerifyExtracted proves, proved on the download.

    codesign --verify --deep --strict is Platform.VerifyExtracted's first check
    and lipo -archs is its second; the driver runs them on the installed bundle
    so a damaged seal or a lost slice is named before the updater is asked to
    accept the same bytes.
    """
    sealed = command(["/usr/bin/codesign", "--verify", "--deep", "--strict", app])
    require(sealed.returncode == 0,
            "bundle seal: codesign refused " + str(app) + ": " + sealed.stderr.strip())
    archs = command(["/usr/bin/lipo", "-archs", bundle_binary(app)])
    require(archs.returncode == 0, "bundle seal: lipo could not read " + str(bundle_binary(app)))
    slices = archs.stdout.split()
    require("arm64" in slices and "x86_64" in slices,
            "bundle seal: not a universal binary, got " + archs.stdout.strip())


def app_info(app):
    """The bundle's own identity, read where a person's Finder reads it."""
    with (Path(app) / "Contents/Info.plist").open("rb") as source:
        plist = plistlib.load(source)
    require(plist.get("CFBundleIdentifier") == "dev.nocx",
            "Info.plist names " + str(plist.get("CFBundleIdentifier")) + ", not dev.nocx")
    return plist


def environment(home):
    """The disposable profile the run owns, and nothing of the runner's."""
    env = dict(os.environ,
               HOME=str(home),
               XDG_CONFIG_HOME=str(home / "Library/Application Support"),
               XDG_DATA_HOME=str(home / "Library/Application Support"),
               XDG_CACHE_HOME=str(home / "Library/Caches"))
    for name in ["NOCX_UPDATE_CHANNEL", "APPIMAGE", "PYTHONPATH"]:
        env.pop(name, None)
    return env


def apply_diagnostic_present(strings_output):
    """Whether a packaged build implements the update driver.

    The flag's usage text is a literal in the binary's read-only data
    (release_diagnostics.go), so `strings` answers the question without
    starting anything. A build packaged before the driver existed has neither
    the flag nor its usage line; it treats the argument as unknown and starts
    the window, which on a headless runner never exits (nightly 41407342443
    waited thirty minutes for exactly that).
    """
    return "usage: " + APPLY_DIAGNOSTIC in strings_output


def require_apply_diagnostic(binary):
    """Refuse, by name, a build that cannot be asked to update itself."""
    result = command(["/usr/bin/strings", "-a", str(binary)])
    require(result.returncode == 0,
            "cannot read " + str(binary) + ": " + result.stderr.strip())
    require(apply_diagnostic_present(result.stdout),
            "the installed build has no " + APPLY_DIAGNOSTIC + " diagnostic: "
            + str(binary) + " was packaged before the driver existed, so asking "
            "it to update would start the app instead")


def _text(value):
    if isinstance(value, bytes):
        return value.decode(errors="replace")
    return value or ""


def _kill(process):
    """Kill the process group, not only the direct child.

    The diagnostic starts a session of its own and may have spawned a
    coordinator; leaving that behind is how an orphan nocx-server survived the
    timed-out run (nightly 41407342443).
    """
    try:
        os.killpg(os.getpgid(process.pid), signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        try:
            process.kill()
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        pass


def speak(process, start_timeout, timeout):
    """The process's combined output, or a ProofError that keeps what it said.

    subprocess.run discards the captured output with its TimeoutExpired, which
    is why the failed run's evidence said "the updater logged nothing" while
    the pipe held the updater's lines. Silence now has a deadline of its own:
    the diagnostic logs its first step before it can block, so a process that
    has said nothing after start_timeout never reached the updater, and is
    killed and named rather than waited out for thirty minutes.
    """
    started = time.monotonic()
    try:
        output, _ = process.communicate(timeout=start_timeout)
        return output or ""
    except subprocess.TimeoutExpired as silence:
        partial = _text(silence.output)
        if not partial.strip():
            _kill(process)
            raise ProofError("start (the updater logged nothing in "
                             + str(start_timeout) + " s); killed it") from None
    remaining = max(1, timeout - (time.monotonic() - started))
    try:
        output, _ = process.communicate(timeout=remaining)
        return output or partial
    except subprocess.TimeoutExpired as late:
        partial = _text(late.output) or partial
        _kill(process)
        raise ProofError("the update did not finish within " + str(timeout)
                         + " s at " + step_reached(partial) + ": "
                         + tail(partial)) from None


def run_applicator(install, home, channel, timeout=1800, start_timeout=120):
    """Run the installed bundle's own update diagnostic, as the extension point.

    Returns the parsed result line, the step it reached, and the raw output —
    the output is what a failure prints, because the updater's lines are the
    only account of which step refused.
    """
    binary = bundle_binary(install)
    process = subprocess.Popen([str(binary), APPLY_DIAGNOSTIC, "--channel", channel],
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                               text=True, env=environment(home), cwd=home,
                               start_new_session=True)
    output = speak(process, start_timeout, timeout)
    step = step_reached(output)
    if process.returncode != 0:
        raise ProofError("the update failed at " + failing_step(output)
                         + ": " + tail(output))
    payload = None
    for line in reversed(output.splitlines()):
        try:
            payload = json.loads(line)
        except ValueError:
            continue
        if isinstance(payload, dict) and "step" in payload:
            break
    require(payload is not None and payload.get("step") in ("applied", "current"),
            "the update diagnostic printed no result: " + tail(output))
    return payload, step, output


def tail(text, lines=40):
    kept = [line for line in text.splitlines() if line.strip()][-lines:]
    return "\n".join(kept)


def launch(install, home, log):
    """Start the INSTALLED app the way a person starts it, with this profile."""
    env = environment(home)
    process = subprocess.Popen([str(bundle_binary(install))], env=env, cwd=home,
                               stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    return process


def owned_pids(home):
    marker = ("HOME=" + str(home)).encode()
    pids = set()
    for entry in Path("/proc").iterdir() if sys.platform == "linux" else []:
        try:
            if marker in (entry / "environ").read_bytes().split(b"\0"):
                pids.add(int(entry.name))
        except OSError:
            pass
    return pids


def named_pids(home):
    """PIDs whose command line carries this disposable home.

    macOS has no /proc, so ps is asked for the command line instead; the
    disposable home is the marker, never the process name, because a runner
    may be running another nocx of its own.
    """
    result = command(["/bin/ps", "-axo", "pid=,command="])
    pids = set()
    for line in result.stdout.splitlines():
        pid, _, argv = line.strip().partition(" ")
        if pid.isdigit() and str(home) in argv and "nocx" in argv:
            pids.add(int(pid))
    return pids


def stop(pids):
    for pid in pids:
        try:
            os.kill(pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline and any(alive(pid) for pid in pids):
        threading.Event().wait(0.2)
    for pid in pids:
        if alive(pid):
            try:
                os.kill(pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def run(args, evidence):
    require(sys.platform == "darwin", "this is the macOS proof")
    require(os.getuid() != 0, "run as the runner's own user, not as root")
    previous = verify_manifest(args.previous_manifest, args.previous_signature, args.verifier)
    candidate = verify_manifest(args.candidate_manifest, args.candidate_signature, args.verifier)
    before, after = nightly_pair(previous, candidate, "darwin", "universal")
    evidence.update(previous=previous, candidate=candidate,
                    manifestDigests={"previous": sha256(args.previous_manifest),
                                     "candidate": sha256(args.candidate_manifest)})
    root = args.work.resolve()
    require(not root.exists(), "work directory must be absent (never reuse a developer profile)")
    root.mkdir(parents=True)
    home = root / "home"
    layout = install_layout(home)
    layout["applications"].mkdir(parents=True)
    layout["profile"].mkdir(parents=True)
    evidence["home"] = str(home)

    # THE INSTALL: the signed zip, extracted the way the product extracts one.
    previous_zip = root / "previous.zip"
    candidate_zip = root / "candidate.zip"
    download_artifact(before, previous_zip)
    download_artifact(after, candidate_zip)
    staging = root / "staging"
    staging.mkdir()
    extracted = command(["/usr/bin/ditto", "-x", "-k", "--noqtn", previous_zip, staging])
    require(extracted.returncode == 0, "extraction (ditto): " + extracted.stderr.strip())
    verify_bundle(staging / "nocx.app")
    installed_from = root / "previous.app"
    shutil.move(str(staging / "nocx.app"), installed_from)
    previous_build = parse_version(command([bundle_binary(installed_from), "--version"]).stdout)
    require(previous_build["version"] == previous["version"],
            "the previous nightly reports " + previous_build["version"]
            + " but its signed manifest says " + previous["version"])
    shutil.copytree(installed_from, layout["install"], symlinks=True)
    evidence["previousBuild"] = previous_build
    evidence["installedFrom"] = str(installed_from)

    # The candidate's own bytes are what the swap must leave behind.
    candidate_dir = root / "candidate"
    candidate_dir.mkdir()
    extracted = command(["/usr/bin/ditto", "-x", "-k", "--noqtn", candidate_zip, candidate_dir])
    require(extracted.returncode == 0, "extraction (ditto): " + extracted.stderr.strip())
    verify_bundle(candidate_dir / "nocx.app")
    candidate_build = parse_version(command([bundle_binary(candidate_dir / "nocx.app"), "--version"]).stdout)
    require(candidate_build["version"] == candidate["version"],
            "the candidate nightly reports " + candidate_build["version"]
            + " but its signed manifest says " + candidate["version"])
    require(previous_build["commit"] != candidate_build["commit"],
            "two nightlies at one commit prove nothing about an update")
    evidence["candidateBuild"] = candidate_build
    candidate_sha = sha256(bundle_binary(candidate_dir / "nocx.app"))
    previous_sha = sha256(bundle_binary(installed_from))

    # THE UPDATE, from inside the installed bundle. The driver is a capability
    # of that build, so ask whether it has one before starting it: a build
    # packaged before the diagnostic existed would open its window instead and
    # never exit, which is the timeout this proof used to report as "the
    # updater logged nothing".
    require_apply_diagnostic(bundle_binary(layout["install"]))
    payload, reached, output = run_applicator(layout["install"], home, "nightly")
    evidence["applicator"] = {"step": payload["step"], "channel": payload["channel"],
                              "reached": reached, "stdout": output}
    require(payload["step"] == "applied",
            "the installed nightly reported itself current instead of moving to "
            + candidate["version"] + ": the pair proved nothing")
    require(payload.get("toVersion") == candidate["version"],
            "the diagnostic applied " + str(payload.get("toVersion"))
            + ", not the signed candidate " + candidate["version"])

    # THE SWAP, on disk.
    require(sha256(bundle_binary(layout["install"])) == candidate_sha,
            "bundle swap: the installed bundle is not the candidate's bytes")
    swap = layout["install"].parent / ".nocx-swap.app"
    backup = layout["install"].parent / ".nocx-backup.app"
    require(not swap.exists(), "bundle swap: the rollback peer is still there after a complete exchange")
    require(sha256(bundle_binary(backup)) == previous_sha,
            "bundle swap: the retained backup is not the previous nightly")
    pending = read_pending(layout["journal"], layout["install"],
                           {**after, "version": candidate["version"]})
    require(pending["fromVersion"] == previous["version"],
            "journal did not start from the installed nightly")
    evidence["pendingBeforeRelaunch"] = pending
    installed_version = parse_version(command([bundle_binary(layout["install"]), "--version"]).stdout)
    require(installed_version == candidate_build,
            "the swapped bundle reports " + json.dumps(installed_version))
    evidence["installedAfterSwap"] = installed_version

    # THE RELAUNCH AND THE PAIR CHECK: the real app starts, its renderer reports
    # healthy, and finalisation certifies the window against the coordinator
    # that answered it (internal/update/pair.go) by deleting the journal.
    log_path = root / "app.log"
    log = log_path.open("w")
    process = None
    try:
        process = launch(layout["install"], home, log)
        evidence["relaunchedPID"] = process.pid
        wait_for("the relaunched app to finalise the update", lambda: not layout["journal"].exists(),
                 args.ui_timeout, describe=lambda: tail(log_path.read_text(errors="replace"), 20))
        require(sha256(bundle_binary(layout["install"])) == candidate_sha,
                "pair check: the installed bundle changed after finalisation")
        require(backup.exists() and sha256(bundle_binary(backup)) == previous_sha,
                "pair check: finalisation did not retain the previous nightly")
        evidence["relaunchedVersion"] = parse_version(
            command([bundle_binary(layout["install"]), "--version"]).stdout)
        evidence["pairCertified"] = True
    finally:
        log.close()
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
        stop(named_pids(home))
        app_log = layout["log"]
        if app_log.exists():
            evidence["applicationLog"] = tail(app_log.read_text(errors="replace"), 200)
        # THE WINDOW'S OWN ACCOUNT (nocx-vh2hr.1.12). The relaunch below and
        # the pair check both run in the window process, which writes here,
        # not to the backend's nocx.log.
        window_log = layout["windowLog"]
        if window_log.exists():
            evidence["windowLog"] = tail(window_log.read_text(errors="replace"), 200)
        evidence["applicatorLog"] = tail(output, 200)
    evidence["installedAfterRelaunch"] = sha256(bundle_binary(layout["install"]))
    evidence["result"] = "passed"


def parse_arguments(argv=None):
    """The driver's own command line, with every PATH resolved.

    ONLY paths. The normalisation used to run over every value and call
    `.resolve()` on it, so `--ui-timeout` — an int — killed the proof before its
    first step:

        AttributeError: 'int' object has no attribute 'resolve'

    which is what nightly 41398991857 did on every run (nocx-vh2hr.1.10). A
    defaulted argument reaches the loop just like a passed one, which is why the
    workflow never had to mention it to trigger this.
    """
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["previous-manifest", "previous-signature", "candidate-manifest",
                 "candidate-signature", "verifier", "work", "evidence"]:
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--ui-timeout", type=int, default=300)
    args = parser.parse_args(argv)
    for key, value in vars(args).items():
        if isinstance(value, Path):
            setattr(args, key, value.resolve())
    return args


def main():
    args = parse_arguments()
    evidence = {"result": "failed", "proof": "nocx-1ebrs.35.5"}
    try:
        run(args, evidence)
    except Exception as error:
        evidence["error"] = str(error)
        evidence["failedStep"] = failing_step(evidence.get("applicator", {}).get("stdout", ""),
                                              str(error))
        print("FAILED STEP: " + evidence["failedStep"], file=sys.stderr)
        print(str(error), file=sys.stderr)
        if evidence.get("applicator", {}).get("stdout"):
            print("--- the updater's own lines ---", file=sys.stderr)
            print(tail(evidence["applicator"]["stdout"]), file=sys.stderr)
        raise
    finally:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
