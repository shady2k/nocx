#!/usr/bin/env bash
#
# Generates THIRD_PARTY_LICENSES for the pin: the licenses of every component
# the pinned archives statically link, so a binary that ships those archives can
# ship their licenses with it (nocx-ygxjv.14).
#
#   third_party/libghostty-vt/scripts/licenses.sh --source DIR --dist DIR [--out FILE]
#
# WHY IT IS GENERATED AND NOT WRITTEN. "What is inside this archive" is a
# question only the archive answers, and a document maintained by hand answers
# it from memory: a pin bump that adds a dependency would keep a license file
# that looks complete. So the component list comes from `ar`-level evidence in
# the built archives (object members and the `zig-pkg/<directory>` paths their
# objects embed), the text comes from the source at the pin, and
# internal/vtpin's coverage test makes the pinned archives judge the result:
# a component whose member or directory no entry covers FAILS that test.
#
# TWO COMPONENTS SHIP NO LICENCE FILE IN THE PINNED INPUTS at all — simdutf,
# which ghostty vendors as an amalgamation, and Zig's compiler_rt, whose tool
# installation carries no LICENSE — so their texts are committed under
# third_party/libghostty-vt/licenses and every ORIGIN line in the document says
# which upstream file the copy came from and what its sha256 is. That is stated
# rather than hidden because a license text with no stated origin is worse than
# none: it looks checkable and is not.
#
# The generator REFUSES to write a document it cannot complete — a missing
# license file, an archive member no rule attributes, a dependency it cannot
# resolve — so it cannot produce a file with a hole in it. The component rules
# themselves are in internal/vtpin/components.go, each with the measurement that
# established it.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pkg_dir="$(cd "$script_dir/.." && pwd)"          # third_party/libghostty-vt
repo_root="$(cd "$pkg_dir/../.." && pwd)"        # the module root
manifest="$pkg_dir/MANIFEST.json"

source_dir=""
dist_dir="$repo_root/build/libghostty-vt/dist"
out=""

usage() {
  cat <<'FLAGS'
Generates THIRD_PARTY_LICENSES from the pin and the archives built from it.

Flags:
  --source DIR   the source checkout at the pin (the build root Zig built in,
                 with its zig-pkg/ directory populated)
  --dist DIR     the directory holding the archives to inspect
                 (default build/libghostty-vt/dist)
  --out FILE     the document to write
                 (default: --dist/<the manifest's licenses asset name>)
  --manifest P   the pin document (default third_party/libghostty-vt/MANIFEST.json)
FLAGS
}

while [ $# -gt 0 ]; do
  case "$1" in
    --source) source_dir="$2"; shift 2 ;;
    --dist) dist_dir="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --manifest) manifest="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "licenses.sh: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$source_dir" ] || { echo "licenses.sh: --source is required" >&2; exit 2; }
[ -d "$source_dir" ] || { echo "licenses.sh: $source_dir is not a directory" >&2; exit 2; }
[ -d "$dist_dir" ] || { echo "licenses.sh: $dist_dir is not a directory" >&2; exit 2; }

if [ -z "$out" ]; then
  asset="$(cd "$repo_root" && go run ./cmd/vtfetch meta --manifest "$manifest" | sed -n 's/^licenses_asset=//p')"
  [ -n "$asset" ] || { echo "licenses.sh: the manifest names no licenses asset" >&2; exit 1; }
  out="$dist_dir/$asset"
fi

# vtfetch is the only reader of the manifest, so the script passes paths and
# nothing else: a second parser here would be a second answer to what the pin
# says.
cd "$repo_root"
go run ./cmd/vtfetch licenses \
  --manifest "$manifest" \
  --source "$(cd "$source_dir" && pwd)" \
  --dist "$(cd "$dist_dir" && pwd)" \
  --out "$out"

echo "licenses.sh: wrote $out"
