#!/bin/sh
# Provision the audited native Fish for shell-integration acceptance, not the
# user's account shell. Both CI and local container runners call this owner.
#
# Usage:
#   scripts/install-fish-ci.sh <private destination directory>
#   scripts/install-fish-ci.sh --identity   prints key= and path= for
#                                           $GITHUB_OUTPUT, and touches no
#                                           network
#
# THE ARCHIVE IS FETCHED ONCE PER PIN, NOT ONCE PER JOB (nocx-ouru5.1.12). This
# used to download
# https://github.com/fish-shell/fish-shell/releases/download/$VERSION/$ASSET on
# every run, and the Linux jobs failed at "Install the pinned native Fish"
# whenever that release host answered 504. The archive now lives in a cache
# directory whose KEY and PATH are this script's own, printed by --identity and
# read by the workflow's actions/cache steps, so the restore and the install
# cannot disagree about either — and a warm run makes no request at all.
#
# THE PIN IS STILL THE GUARD, on both paths. The archive is checked against the
# pinned sha256 BEFORE anything is unpacked — a restored archive exactly as a
# downloaded one — so a cache entry can never substitute bytes. An entry that
# fails the check is deleted and fetched again rather than used, and only a
# verified archive is ever written into the directory the cache step saves.
set -eu

VERSION=4.9.3

# ONE OWNER OF (platform, asset, sha256). The platform tag is the asset's own,
# as the download URL spells it: a new version or a new asset is a new cache
# entry by construction, never a stale one served under the same name.
pin() {
    case "$(uname -s):$(uname -m)" in
        Linux:x86_64)
            PLATFORM=linux-x86_64
            ASSET="fish-$VERSION-linux-x86_64.tar.xz"
            SHA=8f643d10ad1abb0ef7072764c01ceb5cf61b2d373fda400cf60f0bad69b7e095 ;;
        Linux:aarch64|Linux:arm64)
            PLATFORM=linux-aarch64
            ASSET="fish-$VERSION-linux-aarch64.tar.xz"
            SHA=57349f0111728c64b703a84e611aa97ff4d1fa8466385d64e67bd5bc3ffa49fc ;;
        Darwin:*)
            # The macOS release is one universal .app, so the tag names the OS
            # and not an architecture that does not exist for it.
            PLATFORM=macos
            ASSET="fish-$VERSION.app.zip"
            SHA=3619fa92d540ed0dff4816594429290550d21b92226e0523457e5f0242c5e9a8 ;;
        *) printf 'install-fish-ci: unsupported platform\n' >&2; exit 1 ;;
    esac
}

# THE CACHE ENTRY'S IDENTITY, and the only copy of it: the workflow restores
# what --identity prints and this script reads the same two functions, so one
# edit moves both ends (scripts/workflow-wiring.test.mjs holds the agreement).
cache_key() { printf 'nocx-fish-%s-%s\n' "$VERSION" "$PLATFORM"; }
cache_dir() { printf '%s/nocx-fish-archive\n' "${RUNNER_TEMP:-${TMPDIR:-/tmp}}"; }

pin
case "${1:-}" in
    --identity)
        printf 'key=%s\n' "$(cache_key)"
        printf 'path=%s\n' "$(cache_dir)"
        exit 0 ;;
    "")
        printf 'usage: install-fish-ci.sh <destination directory> | --identity\n' >&2
        exit 2 ;;
esac
DEST="$1"
CACHE_DIR="$(cache_dir)"
ARCHIVE="$CACHE_DIR/$ASSET"

# The pinned bytes, or a non-zero exit. The check prints its verdict either way,
# so a log says whether a hit was verified as well as a fetch.
verify() {
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s  %s\n' "$SHA" "$1" | sha256sum -c -
    else
        printf '%s  %s\n' "$SHA" "$1" | shasum -a 256 -c -
    fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

if [ -f "$ARCHIVE" ] && verify "$ARCHIVE"; then
    printf 'install-fish-ci: %s restored from the cache at %s\n' "$ASSET" "$CACHE_DIR"
else
    # COLD, or the entry is not the pinned bytes. Either way the next thing that
    # touches the network is this download and nothing else, and the entry is
    # removed so a broken one can never be unpacked from.
    rm -f "$ARCHIVE"
    curl -fsSL "https://github.com/fish-shell/fish-shell/releases/download/$VERSION/$ASSET" -o "$TMP/archive"
    verify "$TMP/archive"
    mkdir -p "$CACHE_DIR"
    # MOVED IN ONLY AFTER THE PIN CHECKED IT: the directory the cache step saves
    # can hold nothing but the pinned archive.
    mv "$TMP/archive" "$ARCHIVE"
    printf 'install-fish-ci: %s fetched and cached at %s\n' "$ASSET" "$CACHE_DIR"
fi

mkdir -p "$DEST/bin"
case "$ASSET" in
    *.tar.xz) tar -xJf "$ARCHIVE" -C "$DEST/bin" fish ;;
    *.zip)
        unzip -oq "$ARCHIVE" -d "$DEST"
        ln -sf "../fish-$VERSION.app/Contents/Resources/base/usr/local/bin/fish" "$DEST/bin/fish" ;;
esac
[ "$("$DEST/bin/fish" --version)" = "fish, version $VERSION" ]
"$DEST/bin/fish" --version
if [ -n "${GITHUB_PATH:-}" ]; then
    printf '%s/bin\n' "$DEST" >> "$GITHUB_PATH"
    printf 'NOCX_REQUIRE_NATIVE_FISH=1\n' >> "$GITHUB_ENV"
fi
