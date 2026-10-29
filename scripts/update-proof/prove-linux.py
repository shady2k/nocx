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
from transport import BASE, Relay


def wait_for(description, probe, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = probe()
        if value:
            return value
        threading.Event().wait(0.1)
    raise ProofError("not observed: " + description)


class Desktop:
    def __init__(self, relay, home):
        import pyatspi
        self.atspi = pyatspi
        self.relay = relay
        self.home = home

    def nodes(self):
        # Query the real GTK/WebKit accessibility tree on every observation:
        # old accessible objects become defunct after a window restart.
        def walk(node):
            try:
                yield node
                for index in range(node.childCount):
                    yield from walk(node.getChildAtIndex(index))
            except (RuntimeError, AttributeError):
                return
        desktop = self.atspi.Registry.getDesktop(0)
        pids = profile_pids(self.home)
        result = []
        for application in desktop:
            if application.get_process_id() in pids:
                result.extend(walk(application))
        return result

    def named(self, name, role=None):
        for node in self.nodes():
            try:
                if node.name == name and (role is None or node.getRoleName() == role):
                    if node.getState().contains(self.atspi.STATE_SHOWING):
                        return node
            except RuntimeError:
                continue
        return None

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
        node = wait_for("Update channel", lambda: self.named("Update channel", "combo box"))
        require(node.queryAction().doAction(0), "channel select refused opening")
        option = wait_for("Nightly option", lambda: self.named("Nightly"))
        require(option.queryAction().doAction(0), "Nightly option refused selection")
        self.click("About")

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
        wait_for("same-semver different-byte nightly offer (.66.1.8)",
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
        permitted_servers = {identities["coordinator"]} | {
            p["sha256"] for p in initial if "nocx-server" in p["executable"]}
        desktop.click("Restart now")
        def restarted_pair():
            processes = profile_processes(home)
            windows = [p for p in processes if p["sha256"] == identities["window"]
                       and p["pid"] not in old_windows and p["appimage"] == str(install)]
            servers = [p for p in processes if "nocx-server" in p["executable"]
                       and p["sha256"] in permitted_servers]
            old_still_running = any(item["pid"] in old_windows for item in processes)
            return processes if (windows and servers and not old_still_running
                                 and not journal.exists() and backup.exists()) else None
        evidence["restartedProcesses"] = wait_for("new window and serving coordinator, ReportHealthy finalization",
                                                   restarted_pair)
        evidence["coordinatorPolicy"] = (
            "ADR-0104 acceptance #3: same-Version/different-Commit nightlies may run "
            "the new window against the previous coordinator by design")
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
        desktop.click("About")
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
