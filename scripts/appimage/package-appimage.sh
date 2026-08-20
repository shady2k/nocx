#!/usr/bin/env bash
# Build the Linux AppImage from an already-compiled binary.
#
# Lives here rather than inline in .github/workflows/release.yml so it can be
# run on a developer machine against a real build — packaging that only exists
# inside a workflow is packaging nobody can test before tagging, and this step
# is where nocx-azxe.7 shipped from.
#
#   VERSION=0.2.1 scripts/appimage/package-appimage.sh
#
# Expects to run from the repository root on a Debian-family host with
# libwebkit2gtk-4.1 and its helper processes installed (the release job uses
# ubuntu-22.04, which sets the glibc floor). Writes dist/nocx-$VERSION-linux-amd64.AppImage.
#
# The result is NOT verified here: scripts/appimage/verify-appimage.sh is the
# gate, and it deliberately runs on a different distribution.
set -euo pipefail

: "${VERSION:?set VERSION, e.g. VERSION=0.2.1}"
BIN="${BIN:-build/bin/nocx}"
test -x "$BIN" || { echo "no binary at $BIN — build it first"; exit 1; }
base="nocx-${VERSION}-linux-amd64"
mkdir -p dist

# ── AppDir skeleton ──────────────────────────────────────────
mkdir -p AppDir/usr/bin
cp "$BIN" AppDir/usr/bin/nocx

# .desktop file — required for linuxdeploy to recognise the app.
# `Icon` is not decoration: appimagetool refuses to package without
# one ("Icon entry not found in desktop file"), and the value must
# name an icon actually installed in the AppDir. It is `nocx`, and
# the resize below writes nocx.png so the basename answers it
# (nocx-zvd7).
cat > AppDir/nocx.desktop << 'DESKTOP'
[Desktop Entry]
Name=nocx
Exec=nocx
Icon=nocx
Type=Application
Categories=Utility;TerminalEmulator;
DESKTOP

# No AppRun is written here. linuxdeploy generates its own — a wrapper
# that sources the plugin hooks and execs AppRun.wrapped — and
# overwrites whatever is in the way, so a hand-written one was dead
# weight that read as if it were the entry point. The hook that runs
# inside the generated AppRun is installed further down.

# ── WebKitGTK's helper processes ─────────────────────────────
# These four files are why v0.2.0's AppImage could not start on any
# distribution but Debian's (nocx-azxe.7). WebKitGTK renders in
# several processes; the library spawns WebKitWebProcess and
# WebKitNetworkProcess and finds them at a path baked in when Ubuntu
# compiled it. They are separate executables that nothing links, so
# ldd never names them and the GTK plugin — which has no occurrence
# of "webkit" in it — never copies them. The library was bundled and
# its helpers were not, so it went looking outside the AppImage and
# found them only where Debian puts them.
#
# Copy them first, so the linuxdeploy run below deploys THEIR
# dependency closure too. Without that the failure merely moves from
# "the helper is absent" to "the helper's own library is".
webkit_libexec=/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1
test -x "$webkit_libexec/WebKitNetworkProcess" \
  || { echo "webkit2gtk-4.1 helpers not found at $webkit_libexec"; exit 1; }
mkdir -p "AppDir$webkit_libexec"
cp -a "$webkit_libexec/." "AppDir$webkit_libexec/"
# The injected bundle is shipped for runtime completeness — WebKit may
# select it on configurations we do not exercise — but it is NOT
# load-bearing for us and has no red test: measured on the stand, the
# app reaches a working terminal without it, because our renderer
# talks over our own websocket (AD-1) rather than a web extension.
ls "AppDir$webkit_libexec/injected-bundle/"

# ── linuxdeploy + GTK plugin, both pinned ────────────────────
# The GTK plugin bundles libgtk-3, libwebkit2gtk-4.1 and their
# transitive dependencies into the AppDir, so the AppImage is
# self-contained (the whole point vs a bare tarball — ADR-0007).
#
# Pinned by tag and commit, with checksums. These tools decide the
# AppDir layout, the AppRun, the hook mechanism and whether anything
# is stripped — and the patch step below asserts an exact byte
# pattern in a deployed library. On `continuous` and `master` that
# contract could change with no commit in this repository, and the
# first symptom would be a red release or, worse, a green one.
fetch() { # url sha256 dest
  wget -q -O "$3" "$1"
  echo "$2  $3" | sha256sum -c - || { echo "checksum mismatch for $1"; exit 1; }
  chmod +x "$3"
}
fetch https://github.com/linuxdeploy/linuxdeploy/releases/download/1-alpha-20251107-1/linuxdeploy-x86_64.AppImage \
  c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d linuxdeploy.AppImage
fetch https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/7a3fbc31a9e5075073ff8790f26effbac5f84453/linuxdeploy-plugin-gtk.sh \
  b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171 linuxdeploy-plugin-gtk.sh
fetch https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage \
  ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0 appimagetool.AppImage
# appimagetool fetches the type-2 runtime at packaging time unless it
# is handed one, which would leave an unpinned binary — the first
# bytes every user executes — inside our artefact. Pinned and passed
# in explicitly with --runtime-file.
fetch https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64 \
  2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d runtime-x86_64

# Nothing may rewrite an ELF file after the patch step below, so
# stripping is off for the whole run rather than detected and
# disabled conditionally. linuxdeploy carries a strip too old for the
# .relr.dyn sections modern toolchains emit (Arch, Fedora 39+, Ubuntu
# 24.04+), which is a second reason and the one upstream documents;
# ours is that a stripped library is a rewritten library. The cost is
# a larger file, and a larger file that runs beats a smaller one that
# does not.
export NO_STRIP=1

# linuxdeploy deploys icons into hicolor, so it accepts only that
# spec's resolutions and refuses anything else outright: "Icon
# build/appicon.png has invalid x resolution: 1024 … Valid
# resolutions are 8x8 … 512x512". The master is 1024x1024 because
# that is what macOS wants.
#
# Derived here rather than committed as a second file: build/appicon.png
# stays the one icon in the repository, and a resize cannot drift from
# its master the way a checked-in copy silently would. The basename is
# what answers the desktop file's Icon=nocx — linuxdeploy resolves the
# NAME, not the path — so it is nocx.png and needs no --icon-filename.
icondir="$(mktemp -d)"
convert build/appicon.png -resize 512x512 "$icondir/nocx.png"

# (LDAI_OUTPUT is gone with linuxdeploy's appimage plugin — the
# output name is now an argument to appimagetool below.)
# --desktop-file and --icon-file are what INSTALL the two files
# linuxdeploy then links into the AppDir root; dropping the .desktop
# into AppDir/ by hand is not the same thing, and linuxdeploy said so
# ("Could not find desktop file in AppDir") right before appimagetool
# refused for want of an icon. --icon-filename renames appicon.png to
# `nocx` so it answers the desktop file's Icon= key — appimagetool
# resolves that name, not the path.
#
# build/appicon.png is in git precisely so a clean CI checkout has it
# (nocx-azxe.4); until now nothing on the Linux path used it, so even
# a successful AppImage would have carried no icon.
# Deploy only — no `--output appimage`. Packaging is a separate
# appimagetool call below, because the library is byte-patched after
# this point and a second linuxdeploy invocation is not a
# packaging-only step: it may rescan, redeploy, strip or replace the
# very inode that was patched. `--deploy-deps-only` on the helper
# directory is what pulls in the helpers' own dependencies.
# --appimage-extract-and-run so packaging does not need FUSE. The GitHub runner
# happens to have it; a container does not, and packaging you cannot run in a
# container is packaging you cannot test before tagging.
./linuxdeploy.AppImage --appimage-extract-and-run --appdir AppDir \
  --desktop-file AppDir/nocx.desktop \
  --icon-file "$icondir/nocx.png" \
  --deploy-deps-only="AppDir$webkit_libexec" \
  --plugin gtk

# ── Point the bundled WebKitGTK back inside the AppDir ───────
# The AppRun sources its hooks BY NAME, so a file dropped into
# apprun-hooks/ is not picked up on its own; the source line is
# inserted here, immediately before the exec, and both halves are
# asserted. `zz-` keeps it last so a future plugin hook cannot undo
# the chdir.
#
# The chdir is what makes the relative path in the patched library
# resolve inside the bundle. It cannot leak into a user's shell:
# internal/pty/pty_local.go resolveCwd() falls back to $HOME when no
# cwd is given, precisely because a GUI app's working directory is a
# useless place to start a shell — and the release gate asserts it.
python3 scripts/appimage/install-cwd-hook.py --appdir AppDir
cat AppDir/AppRun

python3 scripts/appimage/patch-webkit-path.py \
  --library AppDir/usr/lib/libwebkit2gtk-4.1.so.0 \
  --libexec "$webkit_libexec" \
  --occurrences 2

# ── Package ──────────────────────────────────────────────────
ARCH=x86_64 ./appimagetool.AppImage --appimage-extract-and-run \
  --runtime-file runtime-x86_64 \
  AppDir "dist/${base}.AppImage"

# ── Cheap checks; the real gate is the next step ─────────────
test -f "dist/${base}.AppImage"
test -x "dist/${base}.AppImage"

# A version check can only ever prove the binary runs: it never
# creates a webview, which is how an AppImage that could not start
# on Arch, Fedora or NixOS passed every check this job had (nocx-azxe.7).
echo "=== version check ==="
"./dist/${base}.AppImage" --appimage-extract-and-run --version 2>&1 | tee /dev/stderr | grep -qw "$VERSION"

sha256sum dist/*
