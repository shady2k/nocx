#!/usr/bin/env bash
#
# Reproduces every number in README.md from a clean checkout: the four
# libghostty-vt archives, the four probe links, the expected darwin failure,
# the linkage table and the two Linux runs.
#
#   cd .internal/spikes/buildmatrix && ./run.sh
#
# The toolchain is not installed: Zig 0.16.0 comes from nixpkgs, Go from PATH.
# The fetch, the ghostty checkout and everything built are gitignored; only the
# README and this script's small sources are meant to be kept.
set -euo pipefail
cd "$(dirname "$0")"

GHOSTTY_REPO=https://github.com/ghostty-org/ghostty.git
GHOSTTY_COMMIT=e2e53f861482e080bf45054ba49ef471f9849937
GO="go"

# Resolve Zig once: the Go links need it as $CC, and nix shell costs a second
# per invocation. 0.16.0 is ghostty's own build.zig.zon minimum_zig_version.
ZIG=$(nix shell nixpkgs#zig -c sh -c 'command -v zig')
[ -n "$ZIG" ] || { echo "FATAL: no zig" >&2; exit 1; }
echo "zig: $("$ZIG" version) ($ZIG)"
mkdir -p bin dist logs
info() { printf '\n=== %s\n' "$*"; }

# 1. Vendored source at the exact commit, verified rather than assumed: a run
#    that cannot pin the commit fails here instead of measuring a different
#    ghostty than the report describes.
info "ghostty source at $GHOSTTY_COMMIT"
mkdir -p .vendor
if [ ! -d .vendor/ghostty/.git ]; then
  git init -q .vendor/ghostty
  git -C .vendor/ghostty remote add origin "$GHOSTTY_REPO"
fi
git -C .vendor/ghostty fetch --depth 1 origin "$GHOSTTY_COMMIT"
git -C .vendor/ghostty checkout -q --detach FETCH_HEAD
got=$(git -C .vendor/ghostty rev-parse HEAD)
[ "$got" = "$GHOSTTY_COMMIT" ] || { echo "FATAL: checked out $got" >&2; exit 1; }
git -C .vendor/ghostty log -1 --format='%H %ci %s'

# 2. One archive per helper target. -Dtarget is the only per-target input; this
#    is the whole of the cross-build, and it needed nothing but Zig.
info "six archives (four helper targets; the Linux pair twice)"
for spec in x86_64-linux:linux-amd64-gnu aarch64-linux:linux-arm64-gnu \
            x86_64-linux-musl:linux-amd64 aarch64-linux-musl:linux-arm64 \
            x86_64-macos:darwin-amd64 aarch64-macos:darwin-arm64; do
  triple=${spec%%:*}; name=${spec##*:}
  start=$(date +%s)
  (cd .vendor/ghostty && "$ZIG" build -Demit-lib-vt=true -Dtarget="$triple" \
    -Doptimize=ReleaseFast) >"logs/archive-$name.log" 2>&1
  secs=$(( $(date +%s) - start ))
  mkdir -p "dist/$name"
  cp .vendor/ghostty/zig-out/lib/libghostty-vt.a "dist/$name/"
  printf '%-14s %-20s %8d bytes  %2d s\n' "$name" "$triple" \
    "$(stat -c%s "dist/$name/libghostty-vt.a")" "$secs"
done

# 3. The Go link. The default build of the darwin targets MUST fail, and the
#    failure must be Go's own -lresolv: that assertion is the point of the
#    exercise, so it is a test here rather than a note.
info "links (default: no stub libs)"
for spec in linux/amd64:x86_64-linux-musl:linux-amd64 \
            linux/arm64:aarch64-linux-musl:linux-arm64; do
  osarch=${spec%%:*}; rest=${spec#*:}; triple=${rest%%:*}; name=${rest##*:}
  cc="$ZIG cc -target $triple"
  if CGO_ENABLED=1 GOOS=${osarch%/*} GOARCH=${osarch#*/} CC="$cc" \
      $GO build -trimpath -ldflags="-s -w" -o "bin/m-$name" ./cmd/probe 2>"logs/m-$name.log"; then
    printf '%-14s LINK OK   %d bytes\n' "$name" "$(stat -c%s "bin/m-$name")"
  else
    printf '%-14s LINK FAILED\n' "$name"; tail -3 "logs/m-$name.log"; exit 1
  fi
done
# The brief's literal Linux targets: the same program against the glibc archive,
# which links and runs but is dynamically linked. This is the row that decides
# whether -musl is a detail or the whole point.
info "links (brief's literal Linux triple, -tags gnu)"
for spec in amd64:x86_64-linux-gnu:linux-amd64-gnu arm64:aarch64-linux-gnu:linux-arm64-gnu; do
  arch=${spec%%:*}; rest=${spec#*:}; triple=${rest%%:*}; name=${rest##*:}
  cc="$ZIG cc -target $triple"
  if CGO_ENABLED=1 GOOS=linux GOARCH=$arch CC="$cc" \
      $GO build -tags gnu -trimpath -ldflags="-s -w" -o "bin/g-$name" ./cmd/probe 2>"logs/g-$name.log"; then
    printf '%-20s LINK OK   %d bytes\n' "$name" "$(stat -c%s "bin/g-$name")"
  else
    printf '%-20s LINK FAILED\n' "$name"; tail -3 "logs/g-$name.log"; exit 1
  fi
done

info "links (darwin without stub libs must fail)"
for spec in darwin/amd64:x86_64-macos:darwin-amd64 darwin/arm64:aarch64-macos:darwin-arm64; do
  osarch=${spec%%:*}; rest=${spec#*:}; triple=${rest%%:*}; name=${rest##*:}
  cc="$ZIG cc -target $triple"
  if CGO_ENABLED=1 GOOS=${osarch%/*} GOARCH=${osarch#*/} CC="$cc" \
      $GO build -trimpath -ldflags="-s -w" -o "bin/m-$name" ./cmd/probe 2>"logs/m-$name.log"; then
    printf '%-14s UNEXPECTEDLY LINKED\n' "$name"; exit 1
  fi
  grep -q "unable to find dynamic system library 'resolv'" "logs/m-$name.log" || {
    echo "FATAL: $name failed for another reason:" >&2; tail -3 "logs/m-$name.log" >&2; exit 1; }
  printf '%-14s LINK FAILED on -lresolv (expected)\n' "$name"
done

# 4. The same two links with the stub libs, which is what a Linux host can do
#    about that flag without a macOS SDK.
info "links (darwin with -tags stublibs)"
for spec in darwin/amd64:x86_64-macos:darwin-amd64 darwin/arm64:aarch64-macos:darwin-arm64; do
  osarch=${spec%%:*}; rest=${spec#*:}; triple=${rest%%:*}; name=${rest##*:}
  cc="$ZIG cc -target $triple"
  if CGO_ENABLED=1 GOOS=${osarch%/*} GOARCH=${osarch#*/} CC="$cc" \
      $GO build -trimpath -ldflags="-s -w" -tags stublibs -o "bin/s-$name" ./cmd/probe 2>"logs/s-$name.log"; then
    printf '%-14s LINK OK   %d bytes\n' "$name" "$(stat -c%s "bin/s-$name")"
  else
    printf '%-14s LINK FAILED\n' "$name"; tail -5 "logs/s-$name.log"; exit 1
  fi
done

# 5. What each artifact asks the loader for. `file` is not installed on this
#    machine, so this reads the real program headers and load commands.
info "linkage"
$GO run ./cmd/binaryinfo bin/m-linux-amd64 bin/m-linux-arm64 \
  bin/g-linux-amd64-gnu bin/g-linux-arm64-gnu \
  bin/s-darwin-amd64 bin/s-darwin-arm64
for b in bin/m-linux-amd64 bin/m-linux-arm64 bin/g-linux-amd64-gnu bin/g-linux-arm64-gnu; do
  printf '%-22s readelf INTERP=%s NEEDED=%s\n' "$(basename $b)" \
    "$(readelf -l $b | grep -c INTERP)" "$(readelf -d $b | grep -c NEEDED)"
done

# 6. The two artifacts that can be executed here. A macOS host is the only
#    place the darwin ones can run, which is README §5.
info "runs"
./bin/m-linux-amd64
nix shell nixpkgs#qemu -c qemu-aarch64 ./bin/m-linux-arm64
# The glibc amd64 artifact is dynamically linked, so it needs its interpreter;
# this host happens to supply /lib64/ld-linux-x86-64.so.2 via nix-ld, and qemu
# with a glibc of our own is the route that does not depend on that.
./bin/g-linux-amd64-gnu
glibc=$(nix eval --raw nixpkgs#glibc.outPath)
nix shell nixpkgs#qemu -c qemu-x86_64 -L "$glibc" ./bin/g-linux-amd64-gnu
# Same problem one architecture over: the glibc arm64 artifact needs an aarch64
# loader, which this host has only through pkgsCross.
cross_glibc=$(nix eval --raw nixpkgs#pkgsCross.aarch64-multiplatform.glibc.outPath)
nix shell nixpkgs#qemu -c qemu-aarch64 -L "$cross_glibc" ./bin/g-linux-arm64-gnu
