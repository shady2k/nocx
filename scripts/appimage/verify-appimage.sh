#!/usr/bin/env bash
# The AppImage release gate: does the packaged file work on a distribution that
# is not the one it was built on?
#
#   scripts/appimage/verify-appimage.sh dist/nocx-0.2.1-linux-amd64.AppImage
#
# It runs the artefact on Arch, under Xvfb, on a host with no GTK and no
# WebKitGTK, and asserts a user-visible outcome: a terminal session opens. Then
# it paints a verified COLRv1 gradient using the shipped engine and the actual
# terminal/title fallback, records loader maps, and runs four mutations that
# must each go red, because a gate nobody has watched fail is not a gate. `--version` cannot replace this: it never creates
# a webview, which is why v0.2.0 shipped an AppImage that could not start on
# Arch, Fedora or NixOS while every check in the release workflow was green
# (nocx-azxe.7).
#
# Needs docker and SYS_PTRACE (the gate traces execve to prove the WebKit helper
# processes came from inside the bundle rather than from the host).
set -euo pipefail

APPIMAGE="${1:?usage: verify-appimage.sh <path-to-appimage> [mutation ...]}"
[ -f "$APPIMAGE" ] || { echo "no such file: $APPIMAGE" >&2; exit 1; }
APPIMAGE="$(cd "$(dirname "$APPIMAGE")" && pwd)/$(basename "$APPIMAGE")"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The happy path first, then the mutations. Order matters: proving the
# unmodified artefact passes in this very container is what makes a later red
# result evidence about the mutation rather than about the container.
# Optional unbundled-baseline runs the original released image directly. It
# requires no bundled FT and the same exact assertion as no-freetype. It is
# intentionally not a default: repaired artifacts must contain FreeType.
MUTATIONS=("none" "no-freetype" "no-network-process" "no-web-process" "unpatched")
[ $# -gt 1 ] && MUTATIONS=("${@:2}")

# Keep screenshots and full logs outside the disposable container. CI receives
# the evidence in its workspace; a caller can choose another output directory.
EVIDENCE="${NOCX_APPIMAGE_VERIFY_EVIDENCE:-$(dirname "$APPIMAGE")/appimage-verification}"
mkdir -p "$EVIDENCE"
EVIDENCE="$(cd "$EVIDENCE" && pwd)"
IMAGE="${NOCX_APPIMAGE_VERIFY_IMAGE:-archlinux:latest}"
status=0
for mutation in "${MUTATIONS[@]}"; do
  echo
  echo "############ $mutation ############"
  if ! docker run --rm --platform=linux/amd64 --cap-add=SYS_PTRACE \
      -v "$APPIMAGE:/artefact.AppImage:ro" \
      -v "$HERE:/verification:ro" \
      -v "$EVIDENCE:/evidence" \
      "$IMAGE" bash /verification/verify-in-container.sh /artefact.AppImage "$mutation"; then
    echo "### $mutation: FAILED"
    status=1
  fi
done

echo
if [ $status -eq 0 ]; then
  echo "AppImage gate: all checks passed"
else
  echo "AppImage gate: FAILED" >&2
fi
exit $status
