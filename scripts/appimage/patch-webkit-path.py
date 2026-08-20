#!/usr/bin/env python3
"""Redirect the bundled WebKitGTK's helper-process lookup into the AppDir.

WebKitGTK does not render in one process. libwebkit2gtk spawns WebKitWebProcess
and WebKitNetworkProcess, and it finds them at a path compiled into the library
when the distribution built it -- on ubuntu-22.04 that is
/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1. The path is an ordinary data string,
not loader metadata, so patchelf cannot move it and no RPATH reaches it.

Nothing else can move it either, and this was measured rather than assumed:

  * linuxdeploy-plugin-gtk contains no occurrence of "webkit". It bundles the
    library because ldd lists it; the helpers are separate executables that
    nothing links, so ldd never names them and nothing copies them.
  * WEBKIT_EXEC_PATH is absent from the shipped library -- upstream gates it
    behind ENABLE(DEVELOPER_MODE), which distribution builds do not set. Only
    WEBKIT_INJECTED_BUNDLE_PATH and WEBKIT_DISABLE_SANDBOX_* are present, and
    neither redirects the exec lookup.
  * Copying the helpers into the AppDir at the mirrored absolute path -- what
    wails v3's own internal/commands/appimage.go does -- changes nothing: the
    lookup is absolute, so the AppDir copy is never consulted. Verified by
    reproducing the user-reported crash with the copies in place.

So the string itself is the only lever, and this script pulls it: the absolute
path becomes an equal-length RELATIVE one, and the AppRun hook chdir's to the
AppDir so it resolves inside the bundle (nocx-azxe.7).

    /usr/lib/x86_64-linux-gnu/webkit2gtk-4.1   ->   usr//lib/x86_64-linux-gnu/webkit2gtk-4.1

Equal length is the whole trick. The doubled slash is padding, and a path with
one is the same path. Keeping the byte count identical means no ELF offset
moves, and -- less obviously -- the library's OTHER baked string, which is this
one plus "/injected-bundle/", keeps its suffix instead of being truncated by the
NUL padding a shorter replacement would need. An upstream report tried
`sed s|/usr|./usr|g`, which LENGTHENS the string, shifts every byte after it and
corrupts the file; that is why theirs "did nothing".

Every assertion here exists so the build fails loudly rather than shipping an
AppImage that starts on the build machine and dies on the user's.
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path


def relative_twin(absolute: str) -> str:
    """Return an equal-length relative form of an absolute directory path.

    Dropping the leading slash costs one byte, so one interior slash is doubled
    to pay it back. The result addresses the same place relative to the AppDir
    and occupies exactly as many bytes as what it replaces.
    """
    if not absolute.startswith("/"):
        raise ValueError(f"expected an absolute path, got {absolute!r}")
    relative = absolute[1:]
    cut = relative.index("/")
    twin = relative[:cut] + "/" + relative[cut:]
    if len(twin) != len(absolute):
        raise ValueError(f"length not preserved: {absolute!r} -> {twin!r}")
    return twin


def find_occurrences(blob: bytes, needle: bytes) -> list[int]:
    at, found = 0, []
    while True:
        at = blob.find(needle, at)
        if at < 0:
            return found
        found.append(at)
        at += 1


def patch(library: Path, old: str, new: str, expected: int) -> int:
    # Resolve first. linuxdeploy leaves symlink chains (libfoo.so.0 ->
    # libfoo.so.0.1.2), and patching "every matching name" would rewrite one
    # inode several times and count its occurrences several times over.
    real = library.resolve()
    if not real.is_file():
        raise SystemExit(f"not a regular file after resolving symlinks: {real}")

    old_b, new_b = old.encode(), new.encode()
    if len(old_b) != len(new_b):
        raise SystemExit(
            f"replacement must be byte-identical in length: "
            f"{len(old_b)} vs {len(new_b)}"
        )

    before = real.read_bytes()

    already = find_occurrences(before, new_b)
    if already:
        raise SystemExit(
            f"{real}: the replacement path is already present at {already} — "
            "refusing to patch a file that has been patched before"
        )

    sites = find_occurrences(before, old_b)
    if len(sites) != expected:
        raise SystemExit(
            f"{real}: expected exactly {expected} occurrence(s) of {old!r}, "
            f"found {len(sites)} at {sites}. The bundled WebKitGTK is not the "
            "build this patch was written against — re-measure before shipping."
        )

    after = before.replace(old_b, new_b)

    # The transformation must be exactly "those byte ranges and nothing else".
    # Rebuilding the expected image independently is cheap and catches a
    # replace() that matched somewhere unintended.
    rebuilt = bytearray(before)
    for at in sites:
        rebuilt[at : at + len(new_b)] = new_b
    if bytes(rebuilt) != after:
        raise SystemExit(f"{real}: patch touched bytes outside the {expected} known sites")
    if len(after) != len(before):
        raise SystemExit(f"{real}: file size changed ({len(before)} -> {len(after)})")
    if not after.startswith(b"\x7fELF"):
        raise SystemExit(f"{real}: ELF magic missing after patch")

    mode = real.stat().st_mode
    real.write_bytes(after)
    os.chmod(real, mode)

    verify = real.read_bytes()
    if find_occurrences(verify, old_b):
        raise SystemExit(f"{real}: the host path survived the patch")
    if len(find_occurrences(verify, new_b)) != expected:
        raise SystemExit(f"{real}: expected {expected} patched site(s) on re-read")

    print(f"patched {real}")
    print(f"  {old}  ->  {new}")
    print(f"  {expected} site(s) at {sites}, {len(after)} bytes, size unchanged")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--library", required=True, type=Path,
                    help="the bundled libwebkit2gtk inside the AppDir")
    ap.add_argument("--libexec", required=True,
                    help="absolute helper directory baked into that library")
    ap.add_argument("--occurrences", type=int, default=2,
                    help="how many times the baked path must appear (pinned on purpose)")
    args = ap.parse_args()
    return patch(args.library, args.libexec, relative_twin(args.libexec), args.occurrences)


if __name__ == "__main__":
    sys.exit(main())
