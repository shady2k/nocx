#!/usr/bin/env python3
"""Real installed macOS nightly update proof. No Wails calls are replaced.

Requires a disposable macOS login with Accessibility permission for osascript,
real published nightly manifests, and common.py from nocx-1ebrs.35.6.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
from urllib.error import HTTPError
from urllib.request import urlopen

BASE = "https://github.com/shady2k/nocx/releases/download/nightly/"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def parse_version(text):
    match = re.fullmatch(r"nocx ([0-9]+\.[0-9]+\.[0-9]+) \(commit ([0-9a-f]+), built [^)]+\)\s*", text)
    require(match is not None, "packaged binary has no release build metadata: " + text)
    return match.group(1), match.group(2)


def assert_transition(old_version, new_version, old_digest, new_digest, old_commit, new_commit):
    require(old_version == new_version, "this proof requires two nightlies at the same semver")
    require(old_digest != new_digest, "nightlies must have different artifact bytes")
    require(old_commit != new_commit, "nightlies must have different linked commits")


def assert_diagnostics(text, version, commit):
    facts = dict(line.split(": ", 1) for line in text.splitlines() if ": " in line)
    require(facts.get("Version") == version, "About Version does not report the installed candidate")
    require(facts.get("Commit") == commit, "About Commit is not the candidate coordinator")


def same_publication(body, signature, expected_body, expected_signature):
    return body == expected_body and signature.strip() == expected_signature.strip()


class Evidence:
    def __init__(self, path):
        self.path = path
        self.data = {"platform": "darwin", "result": "running", "commands": [], "facts": {}}

    def save(self):
        self.path.write_text(json.dumps(self.data, indent=2) + "\n")

    def fact(self, key, value):
        self.data["facts"][key] = value
        self.save()

    def run(self, argv, input=None, check=True):
        argv = [str(value) for value in argv]
        try:
            result = subprocess.run(argv, input=input, text=True, capture_output=True, timeout=120)
        except subprocess.TimeoutExpired:
            self.data["commands"].append({"argv": argv, "exit": None, "error": "timeout"})
            self.save()
            raise
        self.data["commands"].append({"argv": argv, "exit": result.returncode})
        self.save()
        if check:
            require(result.returncode == 0, f"{argv[0]} exited {result.returncode}: {result.stderr}")
        return result


def wait_for(description, predicate, timeout):
    deadline = time.monotonic() + timeout
    while True:
        answer = predicate()
        if answer:
            return answer
        require(time.monotonic() < deadline, "timed out waiting for " + description)
        time.sleep(0.5)  # State polling, never a fixed-duration success condition.


def applescript_literal(text):
    return json.dumps(text)


class Desktop:
    """System Events accessibility actions against the actual WKWebView."""
    def __init__(self, evidence, timeout):
        self.evidence, self.timeout = evidence, timeout

    def script(self, body, check=True):
        script = 'tell application "System Events"\n' + body + '\nend tell'
        return self.evidence.run(["/usr/bin/osascript", "-e", script], check=check)

    def pid(self):
        result = self.script('return unix id of first application process whose name is "nocx"', check=False)
        return result.stdout.strip() if result.returncode == 0 else None

    def element(self, role, label, action="return value of e", optional=False):
        # Match semantic role/name, never coordinates or JS injection. Errors
        # reading a single container attribute do not suppress a missing target.
        body = f'''set p to first application process whose name is "nocx"
set frontmost of p to true
repeat with e in (entire contents of window 1 of p)
    set matches to false
    try
        set matches to (role of e is {applescript_literal(role)} and name of e is {applescript_literal(label)})
    end try
    if matches then
        {action}
        return "acted"
    end if
end repeat
error "No accessible {role}: {label}"'''
        result = self.script(body, check=False)
        if optional and result.returncode != 0:
            return None
        require(result.returncode == 0, result.stderr)
        return result.stdout.strip()

    def button(self, label):
        wait_for(label, lambda: self.element("AXButton", label, 'perform action "AXPress" of e', True), self.timeout)

    def channel(self):
        return self.element("AXPopUpButton", "Update channel")

    def nightly(self):
        self.element("AXPopUpButton", "Update channel", 'perform action "AXPress" of e')
        self.script('keystroke "n"\nkey code 36')
        wait_for("Nightly channel selected", lambda: self.channel() == "Nightly", self.timeout)

    def about(self):
        self.button("About")

    def diagnostics(self):
        # Clear the pasteboard first so an old successful copy can never pass.
        self.evidence.run(["/usr/bin/pbcopy"], input="proof: awaiting live diagnostics")
        self.button("Copy diagnostics")
        return wait_for("live About diagnostics", lambda: self.clipboard(), self.timeout)

    def clipboard(self):
        value = self.evidence.run(["/usr/bin/pbpaste"]).stdout
        return value if value.startswith("nocx\nVersion: ") else None


def live_publication():
    # These are the production URLs compiled into the app. No endpoint override.
    with urlopen(BASE + "manifest.json", timeout=30) as response:
        body = response.read(1024 * 1024 + 1)
    with urlopen(BASE + "manifest.json.sig", timeout=30) as response:
        signature = response.read(4096)
    return body, signature


def require_absent(path):
    # exists() follows symlinks and misses dangling entries. Every pre-existing
    # entry is somebody else's state, including a broken redirect.
    require(not os.path.lexists(path), "existing path: use a disposable macOS login: " + str(path))


def assert_previous_running_publication(body, signature, previous_body, previous_signature):
    require(same_publication(body, signature, previous_body, previous_signature),
            "candidate published before old app was ready on nightly; restart the proof with an unpublished candidate")


def choose_install(home):
    # Match ResolveInstallTarget's order, and refuse rather than overwrite.
    candidates = [Path("/Applications"), home / "Applications"]
    for directory in candidates:
        for name in ["nocx.app", ".nocx-swap.app", ".nocx-backup.app"]:
            require_absent(directory / name)
    for directory in candidates:
        try:
            directory.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryFile(dir=directory):
                pass
            return directory / "nocx.app"
        except OSError:
            continue
    raise RuntimeError("no writable Applications directory")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["previous-manifest", "previous-signature", "manifest", "signature", "verifier", "work-dir"]:
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--consent-disposable-login", action="store_true", required=True)
    parser.add_argument("--publication-timeout", type=int, default=3600)
    parser.add_argument("--ui-timeout", type=int, default=120)
    parser.add_argument("--preset", choices=["unavailable", "nightly"], default="unavailable",
                        help="unavailable records .66.1.7 gap; nightly asserts the installed artifact preset")
    args = parser.parse_args()
    require(sys.platform == "darwin", "macOS install/relaunch cannot run in the Linux pod")
    require(os.getuid() != 0, "run in a disposable graphical login, not as root")
    require_absent(args.work_dir)
    args.work_dir.mkdir(parents=True)
    evidence = Evidence(args.work_dir / "macos-evidence.json")
    try:
        # Shared owner .35.6; no second signature, download or manifest parser.
        import common
        home = Path.home()
        profile = home / "Library/Application Support/nocx"
        require_absent(profile)
        require_absent(home / "Library/Caches/nocx")
        require(evidence.run(["/usr/bin/pgrep", "-x", "nocx"], check=False).returncode == 1,
                "nocx is already running")
        require(evidence.run(["/usr/bin/pgrep", "-x", "nocx-server"], check=False).returncode == 1,
                "a coordinator is already running")
        install = choose_install(home)
        def prepare(label, manifest, signature):
            path = args.work_dir / (label + ".json")
            evidence.run([sys.executable, Path(__file__).with_name("common.py"), "prepare",
                          "--manifest", manifest, "--signature", signature, "--verifier", args.verifier,
                          "--platform", "darwin", "--arch", "universal", "--download",
                          args.work_dir / (label + ".zip"), "--evidence", path])
            prepared = json.loads(path.read_text())
            dest = args.work_dir / label
            evidence.run(["/usr/bin/ditto", "-x", "-k", "--noqtn", args.work_dir / (label + ".zip"), dest])
            app = dest / "nocx.app"
            evidence.run(["/usr/bin/codesign", "--verify", "--deep", "--strict", app])
            archs = evidence.run(["/usr/bin/lipo", "-archs", app / "Contents/MacOS/nocx"]).stdout
            require("arm64" in archs.split() and "x86_64" in archs.split(), "not a universal app")
            build = parse_version(evidence.run([app / "Contents/MacOS/nocx", "--version"]).stdout)
            require(build[0] == prepared["manifest"]["version"], "binary disagrees with signed manifest")
            return prepared, build

        previous, old = prepare("previous", args.previous_manifest, args.previous_signature)
        require(same_publication(*live_publication(), args.previous_manifest.read_bytes(), args.previous_signature.read_bytes()),
                "moving channel must still serve previous nightly before launch; publication order is part of proof")
        evidence.run(["/usr/bin/ditto", args.work_dir / "previous/nocx.app", install])
        evidence.fact("install", str(install))
        evidence.run(["/usr/bin/open", "-n", install])
        desktop = Desktop(evidence, args.ui_timeout)
        old_pid = wait_for("installed app window", desktop.pid, args.ui_timeout)
        desktop.button("Settings")
        desktop.button("Updates")
        initial_channel = desktop.channel()
        evidence.fact("initialChannel", initial_channel)
        if args.preset == "nightly":
            require(initial_channel == "Nightly", "nightly install did not preset its channel")
            evidence.fact("stableDefault", "not tested: requires separate fresh stable-cask pass")
        else:
            require(initial_channel == "Stable", "plain artifact did not start on stable by default")
            evidence.fact("nightlyPreset", "SKIPPED: nocx-1ebrs.66.1.7 is not available; explicit UI opt-in")
            desktop.nightly()
        desktop.about()
        assert_diagnostics(desktop.diagnostics(), *old)
        assert_previous_running_publication(*live_publication(), args.previous_manifest.read_bytes(), args.previous_signature.read_bytes())
        evidence.fact("previousRunningBeforePublication", True)
        # Candidate files may arrive only after N starts. The real producer
        # owns publication; this script never dispatches or fabricates one.
        print("READY_FOR_PUBLICATION: installed N is running nightly; publish N+1 via main workflow", flush=True)
        wait_for("candidate manifest files from real producer",
                 lambda: args.manifest.is_file() and args.signature.is_file(), args.publication_timeout)
        candidate, new = prepare("candidate", args.manifest, args.signature)
        common.nightly_pair(previous["manifest"], candidate["manifest"], "darwin", "universal")
        assert_transition(old[0], new[0], previous["artifact"]["sha256"], candidate["artifact"]["sha256"], old[1], new[1])
        evidence.fact("builds", {"previous": old, "candidate": new})
        expected = (args.manifest.read_bytes(), args.signature.read_bytes())
        def published():
            try:
                return same_publication(*live_publication(), *expected)
            except HTTPError as error:
                # Producer replaces the alias assets non-atomically (nightly.md).
                # Only a missing asset is pending publication, not auth/TLS failure.
                if error.code == 404:
                    return False
                raise
        wait_for("real producer publication of candidate", published, args.publication_timeout)
        evidence.fact("publicationAfterStartup", True)
        desktop.button("Check for updates")
        desktop.button("Install now")
        journal = profile / ".nocx-update-journal.json"
        wait_for("Restart now", lambda: desktop.element("AXButton", "Restart now", "return true", optional=True), args.ui_timeout)
        record = common.read_pending(journal, install, {**candidate["artifact"], "version": new[0]})
        require(record["fromVersion"] == old[0], "journal did not start from previous build")
        evidence.fact("pending", record)
        expected_binary = common.sha256(args.work_dir / "candidate/nocx.app/Contents/MacOS/nocx")
        require(common.sha256(install / "Contents/MacOS/nocx") == expected_binary, "installed binary is not candidate bytes")
        require((install.parent / ".nocx-swap.app").exists(), "no rollback peer after exchange")
        desktop.button("Restart now")
        new_pid = wait_for("new app process", lambda: desktop.pid() if desktop.pid() != old_pid else None, args.ui_timeout)
        running = evidence.run(["/bin/ps", "-p", new_pid, "-o", "comm="]).stdout.strip()
        require(Path(running).resolve() == (install / "Contents/MacOS/nocx").resolve(),
                "restarted window is not the installed app")
        evidence.fact("windowProcesses", {"previousPID": old_pid, "candidatePID": new_pid, "executable": running})
        desktop.button("Settings")
        desktop.button("Updates")
        require(desktop.channel() == "Nightly", "nightly opt-in did not persist across restart")
        desktop.about()
        diagnostics = desktop.diagnostics()
        assert_diagnostics(diagnostics, *new)
        evidence.fact("afterRelaunchDiagnostics", diagnostics)
        wait_for("ReportHealthy finalisation", lambda: not journal.exists(), args.ui_timeout)
        backup = install.parent / ".nocx-backup.app"
        require(backup.exists(), "ReportHealthy did not retain old app backup")
        require(common.sha256(backup / "Contents/MacOS/nocx") ==
                common.sha256(args.work_dir / "previous/nocx.app/Contents/MacOS/nocx"),
                "retained backup is not previous nightly bytes")
        require(not (install.parent / ".nocx-swap.app").exists(), "swap still exists after health confirmation")
        require(common.sha256(install / "Contents/MacOS/nocx") == expected_binary, "update rolled back")
        require(same_publication(*live_publication(), *expected), "channel moved during proof")
        evidence.fact("artifactInitialChannelMode", args.preset)
        evidence.fact("caskAndPromotionAcceptance", "NOT TESTED: attach separate real cask/promotion evidence")
        evidence.data["result"] = "updated; partial task evidence, casks and promotion remain separate"
        evidence.save()
    except Exception as error:
        evidence.data.update(result="failed", error=str(error))
        evidence.save()
        raise


if __name__ == "__main__":
    main()
