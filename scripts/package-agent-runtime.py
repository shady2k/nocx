#!/usr/bin/env python3
"""Build a relocatable runtime from the checked-in lock; never run on end-user startup."""

from __future__ import annotations

import argparse
import csv
import email.parser
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PROJECT = ROOT / "agent-runtime"
PLATFORMS = {
    "linux-amd64": "x86_64-manylinux_2_35",
    "darwin-amd64": "x86_64-apple-darwin",
    "darwin-arm64": "aarch64-apple-darwin",
}
MACHO = {b"\xfe\xed\xfa\xce", b"\xce\xfa\xed\xfe", b"\xfe\xed\xfa\xcf", b"\xcf\xfa\xed\xfe", b"\xca\xfe\xba\xbe", b"\xbe\xba\xfe\xca", b"\xca\xfe\xba\xbf", b"\xbf\xba\xfe\xca"}


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def download(cache: Path, url: str, expected: str) -> Path:
    path = cache / expected
    if path.exists():
        if digest(path) != expected:
            raise ValueError(f"cached download has wrong hash: {path}")
        return path
    cache.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as tmp:
        temporary = Path(tmp.name)
        try:
            with urllib.request.urlopen(url, timeout=60) as response:
                shutil.copyfileobj(response, tmp)
            tmp.flush()
            if digest(temporary) != expected:
                raise ValueError(f"download has wrong hash: {url}")
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)
    return path


def uv_binary(cache: Path, source: dict) -> Path:
    machine = {"x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine()]
    system = {"Linux": "linux", "Darwin": "darwin"}[platform.system()]
    suffix = {("linux", "amd64"): "manylinux2014_x86_64.whl", ("darwin", "amd64"): "macosx_10_12_x86_64.whl", ("darwin", "arm64"): "macosx_11_0_arm64.whl"}[(system, machine)]
    artifact = next(a for a in source["build_tool"]["artifacts"] if a["filename"].endswith(suffix))
    wheel = download(cache, artifact["url"], artifact["sha256"])
    binary = cache / (artifact["sha256"] + "-uv")
    with zipfile.ZipFile(wheel) as package:
        names = [n for n in package.namelist() if Path(n).name == "uv" and not n.endswith("/")]
        if len(names) != 1:
            raise ValueError("pinned uv wheel has no unique executable")
        data = package.read(names[0])
        if not binary.exists() or binary.read_bytes() != data:
            # Another builder may already be executing the cached inode.
            with tempfile.NamedTemporaryFile(dir=cache, delete=False) as tmp:
                temporary = Path(tmp.name)
                try:
                    tmp.write(data)
                    tmp.flush()
                    os.fchmod(tmp.fileno(), 0o700)
                    temporary.replace(binary)
                finally:
                    temporary.unlink(missing_ok=True)
    return binary


def run(*args: str | Path, cwd: Path = PROJECT, capture: bool = False) -> str:
    # Configuration and credentials in the builder's shell cannot change the lock's index.
    environment = {key: value for key, value in os.environ.items() if not key.startswith(("UV_", "PIP_", "PYTHON"))}
    environment["UV_PYTHON_DOWNLOADS"] = "never"
    result = subprocess.run([str(a) for a in args], cwd=cwd, env=environment, check=True, text=True, capture_output=capture)
    return result.stdout if capture else ""


def check_glibc(tree: Path) -> None:
    for path in tree.rglob("*"):
        if path.is_symlink() or not path.is_file():
            continue
        with path.open("rb") as stream:
            if stream.read(4) != b"\x7fELF":
                continue
        output = run("readelf", "--version-info", path, capture=True)
        versions = [(int(a), int(b)) for a, b in re.findall(r"GLIBC_(\d+)\.(\d+)", output)]
        if versions and max(versions) > (2, 35):
            raise ValueError(f"{path} exceeds the Linux glibc 2.35 floor: {max(versions)}")


def remove_builder_state(site: Path) -> None:
    # Only nocx's controlled entrypoint is shipped. Wheel-generated CLI scripts
    # embed the builder's interpreter, and uv receipts embed its paths/timestamps.
    removed = set()
    scripts = site / "bin"
    if scripts.exists():
        removed.update(p.relative_to(site).as_posix() for p in scripts.rglob("*") if p.is_file())
        shutil.rmtree(scripts)
    for info in site.glob("*.dist-info"):
        for name in ("direct_url.json", "uv_cache.json"):
            path = info / name
            if path.exists():
                removed.add(path.relative_to(site).as_posix())
                path.unlink()
        record = info / "RECORD"
        with record.open(newline="") as stream:
            rows = [row for row in csv.reader(stream) if row[0] not in removed]
        with record.open("w", newline="") as stream:
            csv.writer(stream, lineterminator="\n").writerows(rows)


def notices(tree: Path, source: dict) -> list[dict]:
    site = tree / "python/lib/python3.13/site-packages"
    destination = tree / "licenses"
    destination.mkdir()
    packages = []
    for info in sorted(site.glob("*.dist-info")):
        metadata = email.parser.Parser().parsestr((info / "METADATA").read_text())
        name, version = metadata["Name"], metadata["Version"]
        if not name or not version:
            raise ValueError(f"package has no identity: {info}")
        packages.append({"name": name, "version": version})
        package_notices = destination / info.name
        package_notices.mkdir()
        shutil.copy2(info / "METADATA", package_notices / "METADATA")
        for path in info.rglob("*"):
            if path.is_file() and ("licenses" in path.relative_to(info).parts or path.name.lower().startswith(("license", "copying", "notice"))):
                target = package_notices / path.relative_to(info)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(path, target)
        # Keep declared license files outside dist-info too, when a wheel uses that layout.
        for relative in metadata.get_all("License-File", []):
            candidate = info / relative
            if candidate.is_file() and candidate.resolve().is_relative_to(site.resolve()):
                shutil.copy2(candidate, package_notices / Path(relative).name)
    expected = source["lock"]["package_count_including_project"] - 1
    if len(packages) != expected:
        raise ValueError(f"locked runtime has {len(packages)} distributions, expected {expected}")
    (destination / "runtime-provenance.json").write_text(json.dumps(source, indent=2) + "\n")
    shutil.copy2(ROOT / "third_party/pydantic-ai/LICENSE", destination / "pydantic-ai-LICENSE")
    return packages


def manifest_for(tree: Path, target: str, source: dict, packages: list[dict]) -> dict:
    files = []
    for path in sorted(tree.rglob("*")):
        relative = path.relative_to(tree).as_posix()
        if relative == "manifest.json":
            continue
        if path.is_symlink():
            if not path.resolve(strict=True).is_relative_to(tree.resolve()):
                raise ValueError(f"runtime symlink escapes its tree: {relative}")
            files.append({"path": relative, "link": os.readlink(path)})
        elif path.is_file():
            files.append({"path": relative, "sha256": digest(path), "size": path.stat().st_size, "executable": bool(path.stat().st_mode & 0o111)})
        elif not path.is_dir():
            raise ValueError(f"unexpected runtime file type: {relative}")
    return {
        "formatVersion": 1,
        "metadata": {
            "bridgeVersion": 1,
            "target": target.replace("-", "/", 1),
            "interpreter": "python/bin/python3.13",
            "entrypoint": "app/entrypoint.py",
            "pythonVersion": source["python"]["version"],
            "lockSha256": source["lock"]["sha256"],
            "packages": packages,
            "provenance": source,
        },
        "files": files,
    }


def seal(tree: Path, target: str, source: dict, packages: list[dict]) -> str:
    manifest = manifest_for(tree, target, source, packages)
    path = tree / "manifest.json"
    path.write_text(json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n")
    return digest(path)


def verify(tree: Path, target: str) -> str:
    path = tree / "manifest.json"
    recorded = json.loads(path.read_text())
    metadata = recorded["metadata"]
    actual = manifest_for(tree, target, metadata["provenance"], metadata["packages"])
    if actual != recorded:
        raise ValueError("packaged runtime files or platform no longer match their manifest")
    return digest(path)


def sign_and_seal(tree: Path, target: str, identity: str) -> str:
    if platform.system() != "Darwin" or not target.startswith("darwin-"):
        raise ValueError("runtime signing requires a Darwin host and target")
    manifest = json.loads((tree / "manifest.json").read_text())
    for path in sorted(tree.rglob("*"), key=lambda p: len(p.parts), reverse=True):
        if path.is_symlink() or not path.is_file():
            continue
        with path.open("rb") as stream:
            native = stream.read(4) in MACHO
        if native:
            run("codesign", "--force", "--sign", identity, path)
            run("codesign", "--verify", "--strict", path)
    return seal(tree, target, manifest["metadata"]["provenance"], manifest["metadata"]["packages"])


def build(target: str, output: Path, replace: bool) -> str:
    source = json.loads((PROJECT / "runtime-manifest.json").read_text())
    if digest(PROJECT / "uv.lock") != source["lock"]["sha256"]:
        raise ValueError("uv.lock no longer matches the recorded runtime provenance")
    patch_wheel = ROOT / "third_party/pydantic-ai/dist/pydantic_ai_slim-2.52.0+nocx.1-py3-none-any.whl"
    if digest(patch_wheel) != source["patched_sdk"]["wheel_sha256"]:
        raise ValueError("patched SDK wheel does not match recorded provenance")
    if output.is_symlink():
        raise ValueError(f"refusing to replace an output symlink: {output}")
    if output.exists():
        if not replace:
            raise ValueError(f"output exists; explicitly use --replace for a previous runtime build: {output}")
        previous = json.loads((output / "manifest.json").read_text())
        if previous["formatVersion"] != 1 or previous["metadata"]["target"] != target.replace("-", "/", 1):
            raise ValueError("refusing to replace a directory not owned by this runtime target")
    cache = ROOT / "build/agent-runtime-cache"
    uv = uv_binary(cache, source)
    archive = next(a for a in source["python"]["artifacts"] if a["platform"] == target.replace("-", "/", 1))
    python_archive = download(cache, archive["url"], archive["sha256"])
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".agent-runtime-", dir=output.parent) as temporary:
        tree = Path(temporary) / "bundle"
        tree.mkdir()
        with tarfile.open(python_archive) as package:
            package.extractall(tree, filter="data")
        # The standalone distribution ships pip outside our locked graph.
        # Its bootstrap environment must not become an application dependency.
        site = tree / "python/lib/python3.13/site-packages"
        shutil.rmtree(site)
        site.mkdir()
        for script in (tree / "python/bin").glob("pip*"):
            script.unlink()
        requirements = Path(temporary) / "requirements.txt"
        run(uv, "export", "--frozen", "--no-dev", "--no-emit-project", "--no-header", "--format", "requirements-txt", "--output-file", requirements, capture=True)
        run(uv, "pip", "install", "--no-config", "--no-deps", "--require-hashes", "--only-binary", ":all:", "--link-mode", "copy", "--python-version", source["python"]["version"], "--python-platform", PLATFORMS[target], "--target", tree / "python/lib/python3.13/site-packages", "--requirements", requirements)
        remove_builder_state(site)
        app = tree / "app"
        app.mkdir()
        shutil.copytree(PROJECT / "nocx_agent_runtime", app / "nocx_agent_runtime", ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
        shutil.copy2(PROJECT / "entrypoint.py", app / "entrypoint.py")
        packages = notices(tree, source)
        if target == "linux-amd64":
            check_glibc(tree)
        identity = seal(tree, target, source, packages)
        if output.exists():
            shutil.rmtree(output)
        tree.replace(output)
    return identity


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True, choices=PLATFORMS)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--replace", action="store_true")
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--sign", metavar="IDENTITY", help="sign an existing Darwin runtime inside-out, then reseal its manifest")
    action.add_argument("--verify", action="store_true", help="verify a complete packaged tree without changing it")
    action.add_argument("--check-lock", action="store_true", help="verify a native bundle and refuse project/lock drift using its interpreter")
    args = parser.parse_args()
    resources = "Resources/agent-runtime" if args.target.startswith("darwin-") else "lib/nocx/agent-runtime"
    output = Path(os.path.abspath(args.output or ROOT / "build" / resources / args.target))
    if args.sign:
        identity = sign_and_seal(output, args.target, args.sign)
    elif args.verify:
        identity = verify(output, args.target)
    elif args.check_lock:
        identity = verify(output, args.target)
        source = json.loads((PROJECT / "runtime-manifest.json").read_text())
        uv = uv_binary(ROOT / "build/agent-runtime-cache", source)
        run(uv, "lock", "--locked", "--python", output / "python/bin/python3.13")
    else:
        identity = build(args.target, output, args.replace)
    print(json.dumps({"target": args.target, "path": str(output), "manifestDigest": identity}))


if __name__ == "__main__":
    main()
