#!/usr/bin/env python3
"""Shared artifact evidence for real installed-app update proofs.

This module never constructs an updater. Signatures use the release tool's
compiled keyring; the installed application independently verifies its check.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from datetime import datetime
from urllib.parse import urlparse
from urllib.request import urlopen


class ProofError(RuntimeError):
    """An acceptance assertion failed; never a skipped proof."""


def require(condition, message):
    if not condition:
        raise ProofError(message)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command(argv, **kwargs):
    """Record the command's own exit code, including expected refusals."""
    result = subprocess.run([str(arg) for arg in argv], capture_output=True,
                            text=True, timeout=120, **kwargs)
    print(json.dumps({"command": [str(arg) for arg in argv],
                      "exit": result.returncode}), file=sys.stderr)
    return result


def verify_manifest(manifest, signature, verifier):
    result = command([Path(verifier).resolve(), "-verify", "-in", manifest,
                      "-sig", signature])
    require(result.returncode == 0,
            "compiled release keyring refused manifest: " + result.stderr)
    # Never parse unauthenticated JSON, including in this evidence collector.
    try:
        value = json.loads(Path(manifest).read_bytes())
    except (OSError, ValueError) as error:
        raise ProofError(f"cannot read verified manifest {manifest}: {error}") from error
    require(isinstance(value, dict), f"verified manifest {manifest} must be an object")
    return value


def artifact(manifest, platform, arch):
    matches = [item for item in manifest["artifacts"]
               if item["os"] == platform and item["arch"] == arch
               and item["format"] == ("AppImage" if platform == "linux" else "zip")]
    if not matches and platform == "darwin" and arch != "universal":
        return artifact(manifest, platform, "universal")
    require(len(matches) == 1, "manifest must name exactly one matching artifact")
    item = matches[0]
    require(re.fullmatch(r"[0-9a-f]{64}", item["sha256"]) is not None,
            "artifact sha256 is not lowercase SHA-256")
    require(type(item["size"]) is int and item["size"] > 0,
            "artifact size must be a positive integer")
    require(urlparse(item["url"]).scheme == "https", "artifact URL must use HTTPS")
    return item


def nightly_tag(manifest, item):
    url = urlparse(item["url"])
    match = re.fullmatch(r"/shady2k/nocx/releases/download/"
                         r"(nightly-[0-9]{8}-[0-9]+)/[^/]+", url.path)
    require(url.netloc == "github.com" and not url.query and not url.fragment
            and match is not None, "artifact must use an immutable dated nightly URL")
    tag = match.group(1)
    require(manifest["notesUrl"] == "https://github.com/shady2k/nocx/releases/tag/" + tag,
            "manifest notes and artifact must name the same dated nightly")
    return tag


def stamped_build_time(manifest, side):
    """Match internal/update/manifest.go's complete nightly build identity."""
    build = manifest.get("build")
    require(isinstance(build, dict), f"{side} nightly requires a stamped build object")
    commit, built_at = build.get("commit"), build.get("builtAt")
    require(isinstance(commit, str) and re.fullmatch(r"[0-9a-f]{40}", commit) is not None,
            f"{side} nightly stamped build.commit must be a full lowercase commit")
    require(isinstance(built_at, str)
            and re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z",
                             built_at) is not None,
            f"{side} nightly stamped build.builtAt must use YYYY-MM-DDTHH:MM:SSZ")
    try:
        return datetime.fromisoformat(build["builtAt"].replace("Z", "+00:00"))
    except ValueError as error:
        raise ProofError(f"{side} nightly stamped build.builtAt is invalid: {error}") from error


def plain_version(manifest, side, kind):
    """The manifest's version as a number triple; stable and nightly share it."""
    version = manifest.get("version")
    require(isinstance(version, str)
            and re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version) is not None,
            f"{side} {kind} version must stamp a plain next-stable semver")
    return tuple(map(int, version.split(".")))


def require_release_instants(previous, candidate):
    """The candidate must have been published after the previous release."""
    for manifest in [previous, candidate]:
        require(re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}"
                             r"(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})", manifest["released"])
                is not None, "release timestamp must use RFC3339")
    old_time = datetime.fromisoformat(previous["released"].replace("Z", "+00:00"))
    new_time = datetime.fromisoformat(candidate["released"].replace("Z", "+00:00"))
    require(old_time.utcoffset() is not None and new_time.utcoffset() is not None
            and new_time > old_time, "candidate must have a later release timestamp")


def nightly_pair(previous, candidate, platform, arch):
    versions = []
    for side, manifest in (("previous", previous), ("candidate", candidate)):
        require(isinstance(manifest, dict), f"{side} manifest must be an object")
        versions.append(plain_version(manifest, side, "nightly"))
    require(versions[1] >= versions[0], "candidate version must not be lower than previous")
    if versions[1] == versions[0]:
        # Check uses IsNewer OR newerNightly (internal/update/transaction.go).
        # The equal-version branch at internal/update/manifest.go:137 requires
        # different complete commits and a strictly later BUILD time, not just
        # a later publication. A version increase needs no build tie-breaker.
        old_build_time = stamped_build_time(previous, "previous")
        new_build_time = stamped_build_time(candidate, "candidate")
        require(previous["build"]["commit"] != candidate["build"]["commit"]
                and new_build_time > old_build_time,
                "equal-version candidate must have a newer stamped build")
    before, after = artifact(previous, platform, arch), artifact(candidate, platform, arch)
    require(nightly_tag(previous, before) != nightly_tag(candidate, after),
            "proof requires two different published nightlies")
    require_release_instants(previous, candidate)
    require(before["sha256"] != after["sha256"], "the two releases must have different bytes")
    return before, after


def stable_tag(manifest, item):
    """The stable tag a promoted manifest names, or a refusal.

    Promotion copies the tried nightly's bytes and rewrites only its URLs
    (ADR-0097): the artifact moves from the nightly tag to v<version> and the
    notes move with it. So the tag is what proves this manifest is the stable
    release and not the nightly it was copied from, and the manifest's own
    version is the only tag it may name.
    """
    tag = "v" + manifest["version"]
    url = urlparse(item["url"])
    require(url.netloc == "github.com" and not url.query and not url.fragment
            and re.fullmatch(r"/shady2k/nocx/releases/download/v[0-9]+\.[0-9]+\.[0-9]+/[^/]+",
                             url.path) is not None,
            "stable artifact must use its immutable v<version> release URL")
    require(url.path.startswith("/shady2k/nocx/releases/download/" + tag + "/"),
            "stable artifact URL does not name the manifest's own version " + tag)
    require(manifest["notesUrl"] == "https://github.com/shady2k/nocx/releases/tag/" + tag,
            "stable manifest notes and artifact must name the same stable tag")
    return tag


def stable_pair(previous, candidate, platform, arch):
    """A stable release updating to the next promoted stable (nocx-86e2f).

    Stable has no equal-version rule: internal/update/transaction.go offers a
    candidate only when IsNewer, and newerNightly returns false for this
    channel, so the candidate must be strictly newer. No stamped build is
    required — the promotion carries the nightly's build object through, and
    the version is what decides.
    """
    versions = []
    for side, manifest in (("previous", previous), ("candidate", candidate)):
        require(isinstance(manifest, dict), f"{side} manifest must be an object")
        versions.append(plain_version(manifest, side, "stable"))
    require(versions[1] > versions[0],
            "candidate version must be strictly newer than the installed stable version")
    before, after = artifact(previous, platform, arch), artifact(candidate, platform, arch)
    require(stable_tag(previous, before) != stable_tag(candidate, after),
            "proof requires two different published stable releases")
    require_release_instants(previous, candidate)
    require(before["sha256"] != after["sha256"], "the two releases must have different bytes")
    return before, after


def channel_pair(channel, previous, candidate, platform, arch):
    """The pair a proof of `channel` may install, one answer per channel."""
    pairs = {"nightly": nightly_pair, "stable": stable_pair}
    require(channel in pairs, "unknown update channel: " + str(channel))
    return pairs[channel](previous, candidate, platform, arch)


def download_artifact(item, destination):
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Keep an existing installation intact on a short download or wrong digest.
    fd, temporary = tempfile.mkstemp(prefix=".proof-download-", dir=destination.parent)
    try:
        digest, size = hashlib.sha256(), 0
        with os.fdopen(fd, "wb") as output, urlopen(item["url"], timeout=120) as source:
            while chunk := source.read(1024 * 1024):
                size += len(chunk)
                require(size <= item["size"], "artifact exceeds signed size")
                output.write(chunk)
                digest.update(chunk)
            output.flush()
            os.fsync(output.fileno())
        require(size == item["size"], "artifact size differs from signed manifest")
        require(digest.hexdigest() == item["sha256"],
                "artifact digest differs from signed manifest")
        os.replace(temporary, destination)
    finally:
        Path(temporary).unlink(missing_ok=True)


def read_pending(journal, install, expected):
    record = json.loads(Path(journal).read_text())
    require(Path(record["installPath"]).resolve() == Path(install).resolve(),
            "journal targets running mount instead of installed artifact")
    require(record["artifactSHA256"] == expected["sha256"], "journal has wrong digest")
    require(record["toVersion"] == expected["version"], "journal has wrong version")
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["prepare"])
    for name in ["manifest", "signature", "verifier", "download", "evidence"]:
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--platform", required=True, choices=["linux", "darwin"])
    parser.add_argument("--arch", required=True, choices=["amd64", "arm64", "universal"])
    args = parser.parse_args()
    manifest = verify_manifest(args.manifest, args.signature, args.verifier)
    item = artifact(manifest, args.platform, args.arch)
    nightly_tag(manifest, item)
    download_artifact(item, args.download)
    evidence = {"manifest": manifest, "artifact": item,
                "manifestSHA256": sha256(args.manifest),
                "artifactPath": str(args.download.resolve())}
    args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence))


if __name__ == "__main__":
    main()
