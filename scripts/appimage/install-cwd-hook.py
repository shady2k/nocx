#!/usr/bin/env python3
"""Make the AppImage start in its own AppDir, so the patched WebKit path resolves.

scripts/appimage/patch-webkit-path.py turns the helper-process path baked into
the bundled WebKitGTK from absolute into relative. Relative to what is decided
here: the AppRun chdir's into the AppDir before exec'ing the app, so
"usr//lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitNetworkProcess" names the copy
inside the bundle no matter where the user launched the file from (nocx-azxe.7).

Two things make this less obvious than dropping a file into apprun-hooks/:

  * linuxdeploy's generated AppRun sources its hooks BY NAME, one `source` line
    per installed hook. A file that no line names is never read, so the line is
    inserted here — immediately before the exec, with the shape of the AppRun
    asserted rather than assumed.
  * The hook is named zz- so it sources last. Ordering should not matter (the
    GTK plugin's hook only exports variables), but a future plugin hook that
    changed directory would otherwise silently undo this one.

The chdir is invisible to the user: internal/pty/pty_local.go resolveCwd()
already falls back to $HOME when a session names no directory, because a GUI
app's working directory is a useless place to start a shell. The release gate
asserts that as an observable property rather than trusting this paragraph.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

HOOK_NAME = "zz-nocx-webkit-cwd.sh"

HOOK_BODY = """\
# nocx-azxe.7 — the bundled WebKitGTK looks for its helper processes at a path
# relative to this directory; see scripts/appimage/patch-webkit-path.py.
cd "${APPDIR:-$(readlink -f "$(dirname "$0")")}" || exit 1
"""


def install(appdir: pathlib.Path) -> int:
    apprun = appdir / "AppRun"
    hooks = appdir / "apprun-hooks"
    if not apprun.is_file():
        raise SystemExit(f"{apprun} does not exist — run linuxdeploy first")
    if not hooks.is_dir():
        raise SystemExit(f"{hooks} does not exist — the AppRun hook mechanism is gone")

    hook = hooks / HOOK_NAME
    hook.write_text(HOOK_BODY)
    hook.chmod(0o755)

    lines = apprun.read_text().splitlines(keepends=True)

    source_line = f'source "$this_dir"/apprun-hooks/{HOOK_NAME}\n'
    if source_line in lines:
        print(f"{apprun}: already sources {HOOK_NAME}")
        return 0

    # The AppRun is generated, so its shape is a contract with linuxdeploy
    # rather than with us. Assert both halves of it: exactly one exec to insert
    # before, and the GTK plugin's hook still sourced — if either changed, the
    # pinned tool changed under us and this insertion is no longer understood.
    execs = [i for i, line in enumerate(lines) if line.startswith("exec ")]
    if len(execs) != 1:
        raise SystemExit(
            f"{apprun}: expected exactly one `exec` line, found {len(execs)} — "
            "linuxdeploy's AppRun is not the shape this insertion was written for"
        )
    if not any("linuxdeploy-plugin-gtk.sh" in line for line in lines):
        raise SystemExit(f"{apprun}: the GTK plugin hook is no longer sourced")
    if not any('this_dir=' in line for line in lines):
        raise SystemExit(f"{apprun}: no $this_dir to hang the hook path on")

    lines.insert(execs[0], source_line)
    apprun.write_text("".join(lines))

    # No other hook may change directory; this one has to be the only one.
    offenders = [
        other.name
        for other in sorted(hooks.glob("*.sh"))
        if other.name != HOOK_NAME
        and any(
            line.strip().startswith(("cd ", "chdir "))
            for line in other.read_text(errors="replace").splitlines()
        )
    ]
    if offenders:
        raise SystemExit(f"another AppRun hook changes directory: {offenders}")

    print(f"installed {hook.relative_to(appdir)} and sourced it from AppRun")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--appdir", required=True, type=pathlib.Path)
    return install(ap.parse_args().appdir)


if __name__ == "__main__":
    sys.exit(main())
