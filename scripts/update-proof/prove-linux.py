#!/usr/bin/env python3
"""Run two published nightly AppImages through the real desktop update controls.

Run ONLY through nocx-sky-test --e2e, under dbus-run-session and xvfb-run.
Requires distro python3-pyatspi. No app build, fake bridge or fixture updater.
"""

import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from urllib.request import Request, urlopen

from common import (ProofError, artifact, command, download_artifact, nightly_pair,
                    read_pending, require, sha256, verify_manifest)
from controls import (ACTIVATE, FOCUS, REFUSE, Channel, Option, Rail, Reveal,
                      channel_settled)
from transport import BASE, Relay


def wait_for(description, probe, timeout=120, describe=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = probe()
        if value:
            return value
        threading.Event().wait(0.1)
    if describe:
        # A timeout that says what the window looked like, not only what was
        # wanted; a window that died must not replace the timeout with its own
        # read error.
        try:
            description += " (last reading: " + describe() + ")"
        except Exception as error:
            description += " (last reading failed: " + str(error) + ")"
    raise ProofError("not observed: " + description)


def descendants(node):
    """Every accessible at or below `node`; objects that went away are skipped."""
    try:
        yield node
        for index in range(node.childCount):
            yield from descendants(node.getChildAtIndex(index))
    except (RuntimeError, AttributeError):
        return


class Desktop:
    def __init__(self, relay, home):
        import pyatspi
        self.atspi = pyatspi
        self.relay = relay
        self.home = home

    def nodes(self):
        # Query the real GTK/WebKit accessibility tree on every observation:
        # old accessible objects become defunct after a window restart.
        desktop = self.atspi.Registry.getDesktop(0)
        pids = profile_pids(self.home)
        result = []
        for application in desktop:
            if application.get_process_id() in pids:
                result.extend(descendants(application))
        return result

    def named(self, name, role=None, showing=True):
        for node in self.nodes():
            try:
                if node.name == name and (role is None or node.getRoleName() == role):
                    if not showing or node.getState().contains(self.atspi.STATE_SHOWING):
                        return node
            except RuntimeError:
                continue
        return None

    def rail(self):
        """One reading of the settings rail: every row, and the ones on screen."""
        nav = self.named("Settings sections")
        if nav is None:
            return None
        rows, showing = [], []
        for node in descendants(nav):
            try:
                if node.getRoleName() != "push button":
                    continue
                rows.append(node.name or "")
                if node.getState().contains(self.atspi.STATE_SHOWING):
                    showing.append(node.name or "")
            except RuntimeError:
                continue
        return Rail(tuple(rows), tuple(showing))

    def rail_click(self, name):
        """Activate a settings-rail row, scrolling the rail to it first.

        The rail is taller than the window: at the default 1024x768 the last
        rows are in the tree, FOCUSABLE, and not SHOWING (the About row sits
        about 270px below the rail's bottom edge), so waiting for a shown
        control never returned. A person scrolls the rail; asking the engine to
        focus the row is how this does it, and WebKitGTK then scrolls the rail
        until the row is on screen (measured on 2.52). Only then is the row
        activated, and a row that focusing cannot reveal is reported with the
        reading that refused it.
        """
        reveal = Reveal(name)
        deadline = time.monotonic() + 120
        reading = None
        while time.monotonic() < deadline:
            reading = self.rail()
            if reading is not None:
                step = reveal.step(reading)
                if step == ACTIVATE:
                    row = self.named(name, "push button")
                    require(row is not None, "rail row announced but not addressable: " + name)
                    actions = row.queryAction()
                    require(actions.nActions > 0 and actions.doAction(0),
                            "rail row refused activation: " + name)
                    return
                if step == FOCUS:
                    row = self.named(name, "push button", showing=False)
                    if row is not None:
                        row.queryComponent().grabFocus()
                elif step == REFUSE:
                    raise ProofError(reveal.refusal(reading))
            threading.Event().wait(0.1)
        raise ProofError("not observed: settings row " + name
                         + ("; " + reading.describe() if reading else "; no settings rail"))

    def wait_for_channel(self, wanted):
        """Wait until the combo's own value is `wanted`.

        The press counts only once the value changed, and the value is the row
        the combo marks SELECTED — never a row that merely carries the name.
        """
        def settled():
            reading = self.channel()
            return reading if reading is not None and channel_settled(reading, wanted) else None
        wait_for("the channel to read " + wanted, settled,
                 describe=lambda: (self.channel() or Channel()).describe())

    def channel(self):
        """One reading of the update-channel combo, or None before it is announced."""
        combo = self.named("Update channel", "combo box")
        if combo is None:
            return None
        options = []
        expanded = combo.getState().contains(self.atspi.STATE_EXPANDED)
        for node in descendants(combo):
            try:
                if node.getRoleName() != "menu item":
                    continue
                state = node.getState()
                options.append(Option(node.name or "",
                                      state.contains(self.atspi.STATE_SELECTED),
                                      state.contains(self.atspi.STATE_SHOWING)))
            except RuntimeError:
                continue
        return Channel(tuple(options), expanded)

    def text(self):
        result = []
        for node in self.nodes():
            try:
                if node.getState().contains(self.atspi.STATE_SHOWING):
                    result.append(node.name or "")
                    try:
                        text = node.queryText()
                        result.append(text.getText(0, text.characterCount))
                    except NotImplementedError:
                        pass
            except RuntimeError:
                continue
        return "\n".join(result)

    def click(self, name):
        node = wait_for("visible control " + name, lambda: self.named(name, "push button"))
        actions = node.queryAction()
        require(actions.nActions > 0 and actions.doAction(0), "control refused activation: " + name)

    def nightly(self):
        self.click("Settings")
        self.click("Updates")
        combo = wait_for("Update channel", lambda: self.named("Update channel", "combo box"))
        require(combo.queryAction().nActions > 0 and combo.queryAction().doAction(0),
                "channel select did not accept its press")
        # Every option is a menu item in the tree whether or not it is the
        # value, so the row is addressed without requiring it to be on screen.
        option = wait_for("the Nightly option",
                          lambda: self.named("Nightly", "menu item", showing=False))
        require(option.queryAction().doAction(0), "Nightly option refused selection")
        self.wait_for_channel("nightly")
        # About is the last row of the rail and starts below the fold.
        self.rail_click("About")
        wait_for("the About page", lambda: self.named("Check for updates", "push button"))

    def check(self, refused=False):
        start = len(self.relay.requests)
        self.relay.arm_check()
        try:
            self.click("Check for updates")
            wait_for("manual check started", lambda: self.named("Checking…", "push button"))
            wait_for("signature response reached transport barrier",
                     self.relay.signature_reached.is_set)
        finally:
            self.relay.release_check()
        def fetched_signature():
            requests = self.relay.requests[start:]
            errors = [item["error"] for item in requests if "error" in item]
            require(not errors, "nightly producer/relay failed: " + "; ".join(errors))
            return all(any(item["path"].endswith(suffix) and item.get("ok")
                               and item["mode"] == self.relay.mode for item in requests)
                       for suffix in ["/manifest.json", "/manifest.json.sig"])
        wait_for("this check fetched a detached signature", fetched_signature)
        if refused:
            return wait_for("visible failed signature check",
                            lambda: "signature" in self.text().lower()
                            and "failed" in self.text().lower())
        return wait_for("completed check", lambda: self.named("Check for updates", "push button")
                        and self.named("Check for updates", "push button").getState().contains(self.atspi.STATE_ENABLED)
                        and ("You're up to date" in self.text()
                             or self.named("Install now", "push button")
                             or self.named("Install", "push button")))


def profile_pids(home):
    # WebKit may expose accessibility from WebKitWebProcess rather than the
    # window's PID. Restrict by our unique inherited HOME, not process name.
    marker = ("HOME=" + str(home)).encode()
    pids = set()
    for entry in Path("/proc").iterdir():
        if entry.name.isdecimal():
            try:
                if marker in (entry / "environ").read_bytes().split(b"\0"):
                    pids.add(int(entry.name))
            except OSError:
                pass
    return pids


EXECUTABLE_DIGESTS = {}


def executable_digest(path):
    stat = path.stat()
    identity = (stat.st_dev, stat.st_ino, stat.st_size, stat.st_mtime_ns)
    if identity not in EXECUTABLE_DIGESTS:
        EXECUTABLE_DIGESTS[identity] = sha256(path)
    return EXECUTABLE_DIGESTS[identity]


def profile_processes(home, digest=True):
    matches = []
    for entry in Path("/proc").iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            env = (entry / "environ").read_bytes().split(b"\0")
            if ("HOME=" + str(home)).encode() not in env:
                continue
            executable = str((entry / "exe").readlink())
            cmdline = (entry / "cmdline").read_bytes().replace(b"\0", b" ").decode(errors="replace")
            if "nocx" not in executable and "AppRun.wrapped" not in executable:
                continue
            matches.append({"pid": int(entry.name), "executable": executable,
                            "command": cmdline,
                            "sha256": executable_digest(entry / "exe") if digest else None,
                            "appimage": next((x.decode().removeprefix("APPIMAGE=") for x in env
                                              if x.startswith(b"APPIMAGE=")), "")})
        except (OSError, ProcessLookupError):
            continue
    return matches


def extract_identities(appimage, destination):
    destination.mkdir()
    result = command([appimage, "--appimage-extract"], cwd=destination)
    require(result.returncode == 0, "cannot inspect downloaded candidate AppImage")
    appdir = destination / "squashfs-root"
    return {"window": sha256(appdir / "usr/bin/nocx"),
            "coordinator": sha256(appdir / "usr/bin/nocx-server")}


def permitted_coordinators(previous, candidate, initial, identities):
    # ADR-0104 allows reuse only within one Version. A version change must
    # replace the serving coordinator, even when its protocol is compatible.
    permitted = {identities["coordinator"]}
    if previous["version"] == candidate["version"]:
        permitted.update(p["sha256"] for p in initial if "nocx-server" in p["executable"])
    return permitted


class RestartObservation:
    """One restart observation, shared by the wait predicate and its diagnostic."""

    def __init__(self, previous, candidate, initial, identities, install, old_windows,
                 home, journal, backup):
        self.permitted = permitted_coordinators(previous, candidate, initial, identities)
        self.policy = ("same-Version coordinator reuse permitted"
                       if previous["version"] == candidate["version"] else
                       "version change requires candidate-only coordinator")
        self.identities, self.install, self.old_windows = identities, install, old_windows
        self.home, self.journal, self.backup = home, journal, backup
        self.processes = []
        self.windows = []
        self.servers = []
        self.old_still_running = False

    def observe(self):
        self.processes = profile_processes(self.home)
        self.windows = [p for p in self.processes
                        if p["sha256"] == self.identities["window"]
                        and p["pid"] not in self.old_windows
                        and p["appimage"] == str(self.install)]
        self.servers = [p for p in self.processes if "nocx-server" in p["executable"]
                        and p["sha256"] in self.permitted]
        self.old_still_running = any(p["pid"] in self.old_windows for p in self.processes)
        return self.processes if (self.windows and self.servers and not self.old_still_running
                                  and not self.journal.exists() and self.backup.exists()) else None

    def describe(self):
        observed = [p["sha256"] for p in self.processes if "nocx-server" in p["executable"]]
        return (f"{self.policy}; expected coordinator SHA-256={sorted(self.permitted)}; "
                f"observed coordinator SHA-256={observed}; candidate windows="
                f"{[p['pid'] for p in self.windows]}; old window running={self.old_still_running}; "
                f"journal exists={self.journal.exists()}; backup exists={self.backup.exists()}")


def run(args, evidence):
    require(sys.platform == "linux", "this is the Linux proof")
    require(os.environ.get("DISPLAY") and os.environ.get("DBUS_SESSION_BUS_ADDRESS"),
            "run under dbus-run-session and xvfb-run on the e2e pod")
    previous = verify_manifest(args.previous_manifest, args.previous_signature, args.verifier)
    candidate = verify_manifest(args.candidate_manifest, args.candidate_signature, args.verifier)
    lower = verify_manifest(args.lower_manifest, args.lower_signature, args.verifier)
    before, after = nightly_pair(previous, candidate, "linux", "amd64")
    version = candidate["version"]
    require(tuple(map(int, lower["version"].split("."))) < tuple(map(int, version.split("."))),
            "lower-version refusal needs a signed lower-semver manifest")
    evidence.update(previous=previous, candidate=candidate, lower=lower,
                    manifestDigests={"previous": sha256(args.previous_manifest),
                                     "candidate": sha256(args.candidate_manifest)})
    root = args.work.resolve()
    require(not root.exists(), "work directory must be absent (never reuse a developer profile)")
    root.mkdir(parents=True)
    home = root / "home"
    home.mkdir()
    install = args.appimage.resolve()
    require(not install.exists(), "AppImage installation must not already exist")
    require(install.parent == root, "AppImage must be installed directly in the disposable work directory")
    download_artifact(before, install)
    install.chmod(0o755)
    pristine = root / "candidate.AppImage"
    download_artifact(after, pristine)
    pristine.chmod(0o755)
    previous_identities = extract_identities(install, root / "inspect-previous")
    identities = extract_identities(pristine, root / "inspect-candidate")
    require(previous_identities["window"] != identities["window"],
            "nightlies must contain different window executables, not only packaging differences")
    evidence["previousExecutables"] = previous_identities
    evidence["candidateExecutables"] = identities
    relay = Relay(root, (args.previous_manifest, args.previous_signature),
                  (args.candidate_manifest, args.candidate_signature),
                  (args.lower_manifest, args.lower_signature))
    try:
        relay.verify_live()
    except Exception:
        relay.close()
        raise
    # The launcher and all restarts inherit this isolated HOME and transport CA.
    # APPIMAGE_EXTRACT_AND_RUN keeps the actual AppImage runtime in use on pods
    # without /dev/fuse. Never execute an extracted binary as the install target.
    env = dict(os.environ, HOME=str(home), XDG_CONFIG_HOME=str(home / "config"),
               XDG_DATA_HOME=str(home / "data"), XDG_CACHE_HOME=str(home / "cache"),
               APPIMAGE=str(install), APPIMAGE_EXTRACT_AND_RUN="1",
               GTK_MODULES="gail:atk-bridge", NO_AT_BRIDGE="0",
               HTTPS_PROXY=relay.url, https_proxy=relay.url, SSL_CERT_FILE=str(relay.cert),
               WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS="1")
    # An inherited bypass list must not let a refusal check miss the relay.
    for name in ["NO_PROXY", "no_proxy", "APPDIR", "LD_LIBRARY_PATH", "GTK_PATH"]:
        env.pop(name, None)
    runtime = home / "run"
    runtime.mkdir(mode=0o700)
    env["XDG_RUNTIME_DIR"] = str(runtime)
    # Prevent a channel-change startup check from leaving an earlier candidate
    # offer visible while the lower-version refusal is being asserted.
    relay.mode = "lower"
    journal = home / "data/nocx/.nocx-update-journal.json"
    swap, backup = root / ".nocx-swap.app", root / ".nocx-backup.app"
    app_log = (root / "app.log").open("w")
    try:
        process = subprocess.Popen([str(install)], env=env, stdout=app_log, stderr=subprocess.STDOUT,
                                   start_new_session=True, cwd=home)
    except Exception:
        app_log.close()
        relay.close()
        raise
    evidence["initialLauncherPID"] = process.pid
    desktop = None
    try:
        desktop = Desktop(relay, home)
        desktop.nightly()
        initial = profile_processes(home)
        evidence["initialProcesses"] = initial
        require(any(p["appimage"] == str(install) and p["sha256"] == previous_identities["window"]
                    for p in initial), "the real AppImage runtime did not set the installed target")
        require(not (root / ".nocx-update.lock").exists(), "lock must not be beside install")
        wait_for("profile update lock", lambda: (home / "data/nocx/.nocx-update.lock").exists())
        # The signed lower version must not offer a downgrade. The only replaced
        # network bytes are the authenticated manifest and detached signature.
        relay.mode = "lower"
        desktop.check()
        require("You're up to date" in desktop.text(), "lower semver offered an update")
        require(not journal.exists() and sha256(install) == before["sha256"],
                "lower-semver check mutated the installation")
        evidence["lowerSemverRefused"] = True
        relay.mode = "unsigned"
        desktop.check(refused=True)
        require(not journal.exists() and sha256(install) == before["sha256"],
                "unsigned manifest mutated the installation")
        evidence["unsignedRefused"] = True
        relay.verify_live()
        relay.mode = "live"
        desktop.check()
        wait_for("newer published nightly offer",
                 lambda: desktop.named("Install now", "push button")
                 or desktop.named("Install", "push button"))
        desktop.click("Install now" if desktop.named("Install now", "push button") else "Install")
        wait_for("installed update restart control", lambda: desktop.named("Restart now", "push button"))
        pending = read_pending(journal, install, {**after, "version": version})
        evidence["pendingBeforeRestart"] = pending
        require(sha256(install) == after["sha256"], "$APPIMAGE was not replaced")
        require(sha256(swap) == before["sha256"], "atomic swap did not retain previous bytes")
        old_windows = {p["pid"] for p in initial if p["sha256"] == previous_identities["window"]
                       and p["appimage"] == str(install)}
        # ADR-0104 deliberately permits a protocol-compatible same-Version
        # coordinator to survive a different-Commit nightly. Require a REAL
        # serving executable, but do not invent a coordinator takeover policy.
        restarted = RestartObservation(previous, candidate, initial, identities, install,
                                       old_windows, home, journal, backup)
        desktop.click("Restart now")
        evidence["restartedProcesses"] = wait_for(
            "new window and serving coordinator, ReportHealthy finalization",
            restarted.observe, describe=restarted.describe)
        evidence["coordinatorPolicy"] = (
            "ADR-0104 acceptance #3: same-Version/different-Commit nightlies may run "
            "the new window against the previous coordinator by design"
            if previous["version"] == candidate["version"] else
            "ADR-0104 acceptance #1: a version change requires the candidate coordinator")
        # The decisive assertion happens AFTER the new window has run, not merely
        # after Apply or after an app-reported version string.
        require(sha256(install) == after["sha256"], "post-restart installed digest is not candidate")
        require(sha256(backup) == before["sha256"] and not swap.exists(),
                "health finalization did not retain the old artifact as backup")
        evidence["installedSHA256AfterRestart"] = sha256(install)
        metadata = command([install, "--version"], env=env, cwd=home)
        require(metadata.returncode == 0 and metadata.stdout.startswith("nocx " + version + " "),
                "restarted installation does not report the signed version")
        evidence["installedVersionAfterRestart"] = metadata.stdout.strip()
        evidence["reportHealthyFinalized"] = True
        desktop.click("Settings")
        desktop.rail_click("About")
        wait_for("the About page", lambda: desktop.named("Check for updates", "push button"))
        desktop.check()
        require("You're up to date" in desktop.text(), "already-current artifact offered itself again")
        evidence["alreadyCurrentRefused"] = True
        # Replay yesterday's signed nightly: requires producer's monotonic
        # identity (.66.1.8), not a harness-only timestamp comparison.
        relay.mode = "stale"
        desktop.check()
        require("You're up to date" in desktop.text(), "stale signed nightly was offered")
        require(sha256(install) == after["sha256"] and not journal.exists(),
                "stale manifest changed installed artifact")
        evidence["staleRefused"] = True
        evidence["result"] = "passed"
    finally:
        evidence["transportRequests"] = relay.requests
        try:
            evidence["accessibilitySnapshot"] = desktop.text() if desktop else "driver unavailable"
            reading = desktop.rail() if desktop else None
            evidence["settingsRail"] = reading.describe() if reading else "settings rail not open"
        except Exception as error:
            evidence["snapshotError"] = str(error)
        owned_pids = profile_pids(home)
        for pid in owned_pids:
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
        # Helpers/coordinator may be detached; every PID here carries the
        # unique disposable HOME, so no other test or developer process is hit.
        for pid in owned_pids & profile_pids(home):
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        app_log.close()
        relay.close()
        evidence["initialLauncherExit"] = process.returncode


def publication_status():
    request = Request("https://api.github.com/repos/shady2k/nocx/releases?per_page=100",
                      headers={"User-Agent": "nocx-update-proof"})
    with urlopen(request, timeout=60) as response:
        releases = json.load(response)
    tags = [release["tag_name"] for release in releases
            if release["prerelease"] and not release["draft"]
            and release["tag_name"].startswith("nightly-")]
    result = {"result": "ready" if len(tags) >= 2 else "blocked",
              "reason": "two nightlies published" if len(tags) >= 2 else
                        "no nightly published yet" if not tags else
                        "only one nightly published; two are required",
              "tags": tags}
    print(json.dumps(result))
    return 0 if len(tags) >= 2 else 2


def main():
    if sys.argv[1:] == ["--publication-status"]:
        sys.exit(publication_status())
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["previous-manifest", "previous-signature", "candidate-manifest", "candidate-signature",
                 "lower-manifest", "lower-signature", "verifier", "appimage", "work", "evidence"]:
        parser.add_argument("--" + name, required=True, type=Path)
    args = parser.parse_args()
    for key, value in vars(args).items():
        setattr(args, key, value.resolve())
    evidence = {"result": "failed", "proof": "nocx-1ebrs.35.6"}
    try:
        run(args, evidence)
    except Exception as error:
        evidence["error"] = str(error)
        raise
    finally:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
