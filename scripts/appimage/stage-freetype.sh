#!/usr/bin/env bash
# Match the experimental COLRv1 ABI of jammy's prebuilt WebKit (nocx-2inid).
# Run AFTER linuxdeploy: its exclusion list deliberately leaves FreeType out.
set -euo pipefail
APPDIR="${1:?usage: stage-freetype.sh <appdir>}"
version='2.11.1+dfsg-1ubuntu0.4'
sha256='3728210ef44a7a51b3824d5c499f6fbd428584a42a65aee15794a4264c8c173a'
url="https://archive.ubuntu.com/ubuntu/pool/main/f/freetype/libfreetype6_${version}_amd64.deb"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FreeType staging failed: $*" >&2; exit 1; }
wget -q -O "$tmp/freetype.deb" "$url" || fail "download failed: $url"
echo "$sha256  $tmp/freetype.deb" | sha256sum -c - || fail "package SHA256 mismatch"
[ "$(dpkg-deb -f "$tmp/freetype.deb" Package)" = libfreetype6 ] \
  || fail "unexpected package name (expected libfreetype6)"
[ "$(dpkg-deb -f "$tmp/freetype.deb" Version)" = "$version" ] \
  || fail "unexpected package version (expected $version)"
[ "$(dpkg-deb -f "$tmp/freetype.deb" Architecture)" = amd64 ] \
  || fail "unexpected package architecture (expected amd64)"
dpkg-deb -x "$tmp/freetype.deb" "$tmp/package" || fail "package extraction failed"
[ -f "$tmp/package/usr/lib/x86_64-linux-gnu/libfreetype.so.6" ] \
  || fail "extracted package has no FreeType SONAME"
[ -s "$tmp/package/usr/share/doc/libfreetype6/copyright" ] \
  || fail "extracted package has no FreeType copyright notice"
mkdir -p "$APPDIR/usr/lib" "$APPDIR/usr/share/doc/libfreetype6"
# A regular file under the SONAME: no symlink back to a build-host path.
cp -L "$tmp/package/usr/lib/x86_64-linux-gnu/libfreetype.so.6" "$APPDIR/usr/lib/libfreetype.so.6"
cp -a "$tmp/package/usr/share/doc/libfreetype6/." "$APPDIR/usr/share/doc/libfreetype6/"
# Check the ABI generation, not just a filename. A .13+ runtime writes larger
# structs through the same symbols; _GLIBCXX_ASSERTIONS is not the defect.
python3 - "$APPDIR/usr/lib/libfreetype.so.6" <<'PY' || fail "copied library ABI assertion failed"
import ctypes, pathlib, sys
path = pathlib.Path(sys.argv[1]).resolve()
ft = ctypes.CDLL(str(path))
handle = ctypes.c_void_p()
assert ft.FT_Init_FreeType(ctypes.byref(handle)) == 0
major, minor, patch = (ctypes.c_int() for _ in range(3))
ft.FT_Library_Version(handle, ctypes.byref(major), ctypes.byref(minor), ctypes.byref(patch))
ft.FT_Done_FreeType(handle)
version = (major.value, minor.value, patch.value)
assert version == (2, 11, 1), f"wrong FreeType COLRv1 ABI: {version}"
print(f"FreeType ABI checked: {path} version={version}")
PY
# Ship exact package provenance and its post-copy ELF digest. The release gate
# reads these back from the extracted artefact, not from this staging folder.
{
  echo "package=libfreetype6"
  echo "version=$version"
  echo "architecture=amd64"
  echo "url=$url"
  echo "deb_sha256=$sha256"
  echo "elf_sha256=$(sha256sum "$APPDIR/usr/lib/libfreetype.so.6" | cut -d ' ' -f 1)"
} | tee "$APPDIR/usr/share/doc/libfreetype6/nocx-provenance.txt"
