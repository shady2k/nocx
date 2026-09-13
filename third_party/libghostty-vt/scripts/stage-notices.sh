#!/usr/bin/env bash
# Stage the pin's THIRD_PARTY_LICENSES where a binary that links the archives
# can carry it:
#
#   third_party/libghostty-vt/scripts/stage-notices.sh <destination-file>
#
# Three consumers, one rule. `make vt-archives` stages it into the helper's
# embed directory, so `nocx-helper --licenses` prints it from the binary that
# lands on a remote host; the release workflow stages it into the macOS bundle
# (Contents/Resources); scripts/appimage/package-appimage.sh stages it into the
# AppImage (usr/share/doc/nocx). The rule is that the bytes are the PIN's,
# verified against MANIFEST.json's sha256 — never a hand-copied file — which is
# why the asset name and the hash come from cmd/vtfetch, the one reader of the
# manifest, rather than from a second spelling here.
#
# The DESTINATION is the caller's and not the pin's: it is a path in our
# layout, so the manifest stays the only place a pin is stated.
#
# Run it from anywhere; it resolves the repository root from its own path.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $(basename "$0") <destination-file>" >&2
  exit 2
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"

# The DESTINATION is resolved before the cd, so a relative one means what the
# caller's working directory says; the ROOT is resolved after it, so a relative
# VT_ROOT means what the repository root says — which is the only reading that
# works, because `make vt-archives` passes the Makefile's own relative VT_ROOT
# and make runs here. Swapping the two makes one of them resolve against the
# wrong directory and the fetch becomes unfindable from anywhere but the root,
# which is exactly what "run it from anywhere" is for.
dest="$1"
case "$dest" in
  /*) ;;
  *) dest="$PWD/$dest" ;;
esac

cd "$repo"
root="${VT_ROOT:-build/libghostty-vt}"

meta="$(go run ./cmd/vtfetch meta)"
asset="$(printf '%s\n' "$meta" | sed -n 's/^licenses_asset=//p')"
pinned="$(printf '%s\n' "$meta" | sed -n 's/^licenses_sha256=//p')"
if [ -z "$asset" ] || [ -z "$pinned" ]; then
  echo "no licenses asset in third_party/libghostty-vt/MANIFEST.json" >&2
  exit 1
fi

source_file="$root/$asset"
if [ ! -f "$source_file" ]; then
  echo "no $source_file — run: make vt-archives" >&2
  exit 1
fi

# shasum is on macOS, which has no sha256sum, and on Linux; sha256sum is the
# fallback for a Linux host without perl. This repository's workflows carry the
# same preference for the same reason.
if command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "$source_file" | awk '{print $1}')"
else
  got="$(sha256sum "$source_file" | awk '{print $1}')"
fi
if [ "$got" != "$pinned" ]; then
  echo "$source_file is not the pinned document: manifest says sha256 $pinned, the file is $got" >&2
  exit 1
fi

mkdir -p "$(dirname "$dest")"
cp "$source_file" "$dest"
bytes="$(wc -c < "$dest" | tr -d ' ')"
echo "third-party notices: $dest (sha256 $pinned, $bytes bytes)"
