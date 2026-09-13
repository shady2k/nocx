#!/usr/bin/env bash
#
# Rebuilds EVERY archive in third_party/libghostty-vt/MANIFEST.json from the
# pin it names, and checks the result against the hashes it records.
#
#   third_party/libghostty-vt/scripts/recipe.sh
#   third_party/libghostty-vt/scripts/recipe.sh --update-manifest   # a new pin
#   third_party/libghostty-vt/scripts/recipe.sh --out /tmp/dist --cache /tmp/vt
#
# WHY THIS EXISTS, in one line: a verified file gives identical bytes by
# construction, while a from-source build on a pinned toolchain gives identical
# SOURCE and only hopes for identical bytes — so the published archives are the
# product, and this is how a reader checks that the product is what the pin
# says (owner's decision, 2026-09-13; bead nocx-ygxjv.10).
#
# TWO ENTRY POINTS, because the two jobs have opposite contracts and one make
# target with both would be a target that always "fails": the Makefile's
# `vt-recipe-audit` runs this script as it is (it reports a mismatch), and
# `vt-recipe-pin` runs it with --update-manifest (it records what was built).
#
# It writes to $OUT (default build/libghostty-vt/dist, gitignored) the exact
# file names a release carries, and then runs `vtfetch verify` against the
# manifest — the same hashes the fetch checks before a build links anything.
#
# A REBUILD DOES NOT REPRODUCE THE ARCHIVES, and the script says so rather than
# pretending otherwise. Measured 2026-09-13: two builds of one archive from the
# same build root, the same Zig and the same flags differ in 60 bytes, and the
# differing bytes are Zig's own `.zig-cache/o/<key>` directory names, which are
# embedded in the objects and are not a function of source + toolchain + flags.
# The header bundles DO reproduce byte for byte, and so does the licenses
# document, which is generated from the pin rather than built. So this is an
# audit: it tells you exactly which files are not the pinned bytes. The
# published FILE is the identity guarantee, which is the owner's reason for
# shipping archives instead of building everywhere.
#
# WHAT IT NEEDS: the pinned Zig on PATH (or ZIG=<path>), git, network once, and
# Go. Zig 0.16.0 comes from nixpkgs here; ghostty's own build.zig.zon declares
# minimum_zig_version, which is why the version is pinned rather than
# recommended. Ghostty's Zig DEPENDENCIES are fetched by Zig itself and are
# addressed by content hash in the pinned build.zig.zon, so the source at the
# pin plus this toolchain is the whole input set.
#
# WHAT IT DOES NOT DO: publish. Uploading a release is an outward-facing act
# and belongs to the coordinator; this script leaves the files and the command
# (README.md, "Publishing") one step apart.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pkg_dir="$(cd "$script_dir/.." && pwd)"          # third_party/libghostty-vt
repo_root="$(cd "$pkg_dir/../.." && pwd)"        # the module root
manifest="$pkg_dir/MANIFEST.json"

cache="${VT_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/nocx/libghostty-vt}"
# One cache directory owns everything this script downloads and builds —
# ghostty's own Zig dependencies included, which are ~440 MB and land in Zig's
# global cache by default. A measured run can then be deleted whole, and two
# runs can be compared from two paths.
export ZIG_GLOBAL_CACHE_DIR="$cache/zig"
out="$repo_root/build/libghostty-vt/dist"
zig_bin="${ZIG:-zig}"
update_manifest=0
only=""
local_source=""

usage() {
  cat <<'FLAGS'
Rebuilds every archive in MANIFEST.json from the pin, and verifies the result.

Flags:
  --out DIR              where the assets land (default build/libghostty-vt/dist)
  --cache DIR            source checkout and Zig cache (default ~/.cache/nocx/libghostty-vt)
  --zig BIN              the Zig binary (default $ZIG, then PATH); its version is checked
  --only a,b             build only these manifest targets
  --source DIR           build from this checkout instead of fetching the pin
                         (it must be at the manifest's commit; used for a pin whose
                         commit is not published yet)
  --update-manifest      write the built hashes into the manifest (a new pin)
FLAGS
}

while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    --cache) cache="$2"; shift 2 ;;
    --zig) zig_bin="$2"; shift 2 ;;
    --only) only="$2"; shift 2 ;;
    --source) local_source="$2"; shift 2 ;;
    --update-manifest) update_manifest=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "recipe.sh: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
done

vtfetch() { (cd "$repo_root" && go run ./cmd/vtfetch "$@"); }
info() { printf '\n=== %s\n' "$*"; }

# One spelling that works on macOS and on Linux: `shasum -a 256` exists on
# both, `sha256sum` only on Linux — the same reason release.yml hashes with
# shasum.
if command -v sha256sum >/dev/null 2>&1; then
  sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
else
  sha256_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

# 1. The toolchain, checked rather than assumed: a different Zig produces
#    different bytes in an archive whose sha256 is committed.
info "toolchain"
zig_path="$(vtfetch zig --bin "$zig_bin" --manifest "$manifest")"
echo "zig: $("$zig_path" version) ($zig_path)"

# 2. The source at the pin — the FORK, at the commit the manifest names, which
#    is upstream's commit plus nocx's patches (upstream.baseCommit and
#    upstream.patch say which and what). ZIG_GLOBAL_CACHE_DIR keeps the ~440 MB
#    of fetched Zig dependencies inside the cache directory this script owns, so
#    a measured run can be deleted whole.
mkdir -p "$cache/src"
src="$cache/src/ghostty"
meta="$(vtfetch meta --manifest "$manifest")"
commit="$(printf '%s\n' "$meta" | sed -n 's/^commit=//p')"
repository="$(printf '%s\n' "$meta" | sed -n 's/^repository=//p')"
base_commit="$(printf '%s\n' "$meta" | sed -n 's/^base_commit=//p')"
[ -n "$commit" ] || { echo "FATAL: no commit in $manifest" >&2; exit 1; }
[ -n "$repository" ] || { echo "FATAL: no repository in $manifest" >&2; exit 1; }

if [ -n "$local_source" ]; then
  # A pin whose commit is not published yet: the coordinator builds the
  # archives before pushing the fork branch, and the bytes are the same bytes
  # either way. The commit is checked rather than trusted, so a checkout of the
  # wrong tree is an error and not a quietly different pin.
  src="$(cd "$local_source" && pwd)"
  info "source: local checkout $src"
  got="$(git -C "$src" rev-parse HEAD 2>/dev/null || true)"
  [ "$got" = "$commit" ] || {
    echo "FATAL: $src is at ${got:-no commit}, and the manifest pins $commit" >&2
    exit 1
  }
  git -C "$src" log -1 --format='%H %ci %s'
else
  info "source at $commit"
  if [ ! -d "$src/.git" ]; then
    mkdir -p "$src"
    git -C "$src" init -q
  fi
  # SET, not add-if-missing: the repository moved from upstream to the fork, and
  # a checkout that kept the old origin would fetch the wrong history for a
  # commit that only exists in the fork.
  if git -C "$src" remote get-url origin >/dev/null 2>&1; then
    git -C "$src" remote set-url origin "$repository"
  else
    git -C "$src" remote add origin "$repository"
  fi
  git -C "$src" fetch --depth 1 origin "$commit"
  git -C "$src" checkout -q --detach FETCH_HEAD
  got="$(git -C "$src" rev-parse HEAD)"
  [ "$got" = "$commit" ] || { echo "FATAL: checked out $got, wanted $commit" >&2; exit 1; }
  git -C "$src" log -1 --format='%H %ci %s'
fi
echo "upstream base: ${base_commit:-unknown}"

mkdir -p "$out"

# 3. Every target in the manifest, one build each. -Dtarget is the only
#    per-target input; the flags come from the manifest so the manifest, not
#    this script, is what a reader checks a published archive against.
info "archives"
plan="$(vtfetch plan --manifest "$manifest")"
echo "$plan" | awk -F'\t' 'NR>1 && $1=="target" {printf "  %-18s %s\n", $2, $6}'

while IFS=$'\t' read -r kind name _goos _goarch _libc zig_target archive_asset headers_asset; do
  [ "$kind" = "target" ] || continue
  if [ -n "$only" ]; then
    case ",$only," in *",$name,"*) ;; *) continue ;; esac
  fi
  start=$(date +%s)
  (cd "$src" && "$zig_path" build -Demit-lib-vt=true -Demit-xcframework=false \
    -Doptimize=ReleaseFast -Dtarget="$zig_target") >"$cache/build-$name.log" 2>&1 || {
      echo "FATAL: zig build failed for $name; first error:" >&2
      grep -m1 'error:' "$cache/build-$name.log" >&2 || tail -5 "$cache/build-$name.log" >&2
      exit 1
    }
  cp "$src/zig-out/lib/libghostty-vt.a" "$out/$archive_asset"
  vtfetch pack-headers --dir "$src/zig-out/include/ghostty" --out "$out/$headers_asset"
  secs=$(( $(date +%s) - start ))
  printf '%-18s %-20s %10d B  %3d s  %s\n' "$name" "$zig_target" \
    "$(wc -c < "$out/$archive_asset")" "$secs" \
    "$(sha256_of "$out/$archive_asset" | cut -c1-16)"
done <<< "$plan"

# 4. The licenses of everything the archives just linked (nocx-ygxjv.14). It is
#    generated AFTER the build and BEFORE the pin, because it is derived from
#    the archives themselves: which components are inside them is a question
#    only the bytes answer, and a document written from a list of names would
#    be a document that agrees with itself.
#
#    A partial run (--only) does not regenerate it: the document covers every
#    target, so writing one from some of them would be a document about a pin
#    that does not exist. `verify` then reports the missing file, which is the
#    correct answer for a dist directory that is not a whole pin.
if [ -z "$only" ]; then
  info "licenses"
  vtfetch licenses --manifest "$manifest" --source "$src" --dist "$out" \
    --out "$out/$(printf '%s\n' "$meta" | sed -n 's/^licenses_asset=//p')"
else
  echo "licenses: skipped (--only $only builds part of the pin)"
fi

# 5. The judgement. `verify` reads the manifest and reports every file that is
#    not the pinned bytes; `--update-manifest` writes instead of judging, which
#    is what a pin bump needs and what a check must never do.
echo "build root: $cache"
echo "zig cache:  $ZIG_GLOBAL_CACHE_DIR"
echo "zig:        $zig_path"

if [ "$update_manifest" = 1 ]; then
  info "updating $manifest"
  vtfetch pin --manifest "$manifest" --dist "$out"
  echo "re-run without --update-manifest to verify, and run the repository's formatter:"
  echo "  npx prettier --write third_party/libghostty-vt/MANIFEST.json"
else
  info "verifying against the manifest"
  if vtfetch verify --manifest "$manifest" --dist "$out"; then
    echo "recipe: every archive, header bundle and the licenses document is the pinned bytes"
  else
    echo "recipe: the built bytes are NOT what the manifest pins (see above)." >&2
    echo "  this run: root $cache, zig-cache $ZIG_GLOBAL_CACHE_DIR, zig $zig_path" >&2
    echo "  A mismatch here is EXPECTED and is not a bad build." >&2
    echo "  Measured 2026-09-13: two builds of one archive from the SAME root and" >&2
    echo "  the SAME Zig differ in 60 bytes, all of them inside Zig's own" >&2
    echo "  .zig-cache/o/<key> directory names, which the objects embed and which" >&2
    echo "  source + toolchain + flags do not determine. The header bundles DO match" >&2
    echo "  byte for byte, and so does the licenses document, which is generated from" >&2
    echo "  the pin rather than built; the archives do not." >&2
    echo "  The published FILE is the identity guarantee (README.md," >&2
    echo "  \"Reproducibility, stated exactly\")." >&2
    echo "If this pin is new, re-run with --update-manifest and read the diff." >&2
    exit 1
  fi
fi
