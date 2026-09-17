#!/usr/bin/env bash
#
# Proves that every archive in third_party/libghostty-vt/MANIFEST.json LINKS
# for its own target, and that the Linux ones link STATICALLY.
#
#   third_party/libghostty-vt/scripts/verify-link.sh
#   third_party/libghostty-vt/scripts/verify-link.sh --base build/libghostty-vt/dist
#
# WHY A PROBE AND NOT `make helpers`. In this worktree nothing in the product
# imports the emulator yet — the CGo adapter is internal/emulator/ghostty
# (nocx-ygxjv.2) and the helper will import it later. So `make helpers` cannot
# yet link libghostty-vt at all, and a brief that claimed otherwise would be
# measuring a build with no CGo in it. What CAN be proved today, and is, is
# that the bytes the fetch publishes are linkable: linkprobe/ genuinely calls
# into the archive (create, read the geometry back, resize, write, free), and
# this script asserts, per target:
#
#   * it links with the target's own C compiler (Zig, -musl on Linux);
#   * on Linux the musl build has NO PT_INTERP and NO DT_NEEDED — the static
#     property a helper on an unknown host needs (Makefile's helpers comment);
#   * the archive is really IN the artifact — a ghostty symbol is defined in
#     it — because a static archive contributes nothing to a link that
#     references nothing in it;
#   * the two runnable Linux builds run and report 80x24 then 120 columns
#     after a resize.
#
# The glibc pair is built too, and reported as dynamic: that is the measured
# difference between the two readings of "the Linux target" (the archives are
# the same size and different bytes), and the native build in CI and on a
# developer's machine is the one that links it.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pkg_dir="$(cd "$script_dir/.." && pwd)"
repo_root="$(cd "$pkg_dir/../.." && pwd)"
manifest="$pkg_dir/MANIFEST.json"
probe_dir="$pkg_dir/linkprobe"
root="$repo_root/build/libghostty-vt"
bin="$root/probe"
base=""
zig_bin="${ZIG:-zig}"

usage() {
  cat <<'FLAGS'
Proves every pinned archive links (and links statically on Linux).

Flags:
  --base LOCATION   where the archives come from: a release URL or a local
                    directory (default: the manifest's own release URL)
  --zig BIN         the Zig binary (default $ZIG, then PATH)
  --probe-dir DIR   where probe binaries are written (default build/libghostty-vt/probe)
FLAGS
}

while [ $# -gt 0 ]; do
  case "$1" in
    --base) base="$2"; shift 2 ;;
    --zig) zig_bin="$2"; shift 2 ;;
    --probe-dir) bin="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "verify-link.sh: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
done

vtfetch() { (cd "$repo_root" && go run ./cmd/vtfetch "$@"); }
step() { printf '\n=== %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }

# 1. The bytes. Unsigned or tampered archives never reach the compiler: this is
#    the same fetch a build runs, so a probe that links is a probe of the
#    published files and not of whatever a machine had lying around.
step "toolchain"
zig_path="$(vtfetch zig --bin "$zig_bin" --manifest "$manifest")" || fail "no pinned Zig"
echo "zig: $("$zig_path" version) ($zig_path)"

step "fetch and verify"
fetch_args=(fetch --manifest "$manifest" --root "$root")
if [ -n "$base" ]; then fetch_args+=(--base "$base"); fi
vtfetch "${fetch_args[@]}"

mkdir -p "$bin"

# 2. Every target in the manifest, one link each.
step "links"
plan="$(vtfetch plan --manifest "$manifest")"
declare -a linux_static_probes=() linux_dynamic_probes=() other_probes=()
while IFS=$'\t' read -r kind name goos goarch libc _zig_target _archive _headers; do
  [ "$kind" = "target" ] || continue
  cc="$(vtfetch cc --target "$goos/$goarch" --libc "$libc" --zig "$zig_path" --manifest "$manifest")" || fail "no compiler for $name"
  tags=()
  [ "$libc" = "glibc" ] && tags=(-tags gnu)
  out="$bin/probe-$name"
  # -ldflags=-w ONLY ON DARWIN, and for a reason that has nothing to do with
  # the probe: without it Go runs dsymutil to write DWARF for a Mach-O it just
  # produced, and dsymutil is an Xcode tool this Linux host does not have — so a
  # link that WORKED would look like a failure. On Linux the flag must be LEFT
  # OFF: measured here, `-ldflags=-w` under Zig's external linking produces an
  # ELF with no symbol table at all (3,926,024 bytes) while omitting it — or
  # using `-w -s`, which is what the helper itself builds with — keeps one
  # (13,882,904 / 15,616,952 bytes). It is the same flag interaction the spike
  # recorded as an unexplained size difference; the symbol table is what it
  # turns out to move, and step 3 reads a ghostty symbol out of it.
  ldflags=()
  [ "$goos" = "darwin" ] && ldflags=(-ldflags=-w)
  (cd "$probe_dir" && CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" CC="$cc" \
    LINKPROBE_TARGET="$name" go build "${tags[@]+"${tags[@]}"}" "${ldflags[@]+"${ldflags[@]}"}" -o "$out" .) \
    || fail "$name: does not link with $cc"
  printf 'LINK OK  %-18s %-6s %s\n' "$name" "$libc" "$(basename "$cc")"

  case "$name" in
    linux-*) if [ "$libc" = "musl" ]; then linux_static_probes+=("$out"); else linux_dynamic_probes+=("$out"); fi ;;
    *) other_probes+=("$out") ;;
  esac
done <<< "$plan"

# 3. What the linker produced. The static assertion is on the artifact, not on
#    a flag: the archive's libc ABI decides it, and only the musl triple keeps
#    it (the glibc builds below are the measured counter-example).
step "linkage"
if [ "${#linux_static_probes[@]}" -gt 0 ]; then
  vtfetch inspect --require-static --symbol ghostty_terminal_new \
    "${linux_static_probes[@]}" || fail "a Linux helper archive is not statically linked, or its symbol is absent"
fi
if [ "${#linux_dynamic_probes[@]}" -gt 0 ]; then
  vtfetch inspect --symbol ghostty_terminal_new "${linux_dynamic_probes[@]}" \
    || fail "a Linux glibc archive did not link (its symbol is absent)"
fi
if [ "${#other_probes[@]}" -gt 0 ]; then
  vtfetch inspect --symbol ghostty_terminal_new "${other_probes[@]}" \
    || fail "a macOS archive did not link (its symbol is absent)"
fi

# 4. The two Linux builds that can run here, run. amd64 natively; arm64 under
#    qemu when it is installed, and reported as skipped when it is not — a skip
#    that says so is worth more than a script that quietly does less.
step "runs"
LINKPROBE_TARGET=linux-amd64 "$bin/probe-linux-amd64"
if command -v qemu-aarch64 >/dev/null 2>&1; then
  LINKPROBE_TARGET=linux-arm64 qemu-aarch64 "$bin/probe-linux-arm64"
else
  echo "SKIP probe-linux-arm64: qemu-aarch64 is not installed"
fi
# The glibc amd64 probe is dynamically linked, so it needs its interpreter;
# that is the host's own on a glibc machine, and this line is what makes the
# dynamic-linkage row above something other than a claim about a file header.
LINKPROBE_TARGET=linux-amd64-gnu "$bin/probe-linux-amd64-gnu"

step "verify-link: every pinned archive links; the musl pair is static"
