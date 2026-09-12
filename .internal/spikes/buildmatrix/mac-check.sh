#!/bin/bash
#
# The Mac half of nocx-cm1ac: everything README section 5 says a Linux machine
# cannot answer. Run it on an Apple-silicon Mac, from a fresh clone of the
# branch, and paste back the SUMMARY block at the end (the full log is kept too).
#
#   git clone --branch feat/agent-orchestration --depth 50 \
#       https://github.com/shady2k/nocx.git && cd nocx
#   ./.internal/spikes/buildmatrix/mac-check.sh
#
# Needs: Xcode Command Line Tools (xcode-select --install), Go 1.26, git, curl.
# Zig 0.16.0 is fetched by this script if the one on PATH is not exactly that,
# because ghostty rejects any other version at configure time.
#
# Written for the bash 3.2 macOS ships: no associative arrays, no mapfile, no
# ${var,,}. And deliberately NOT `set -e`: every check runs and reports on its
# own, so one failure never hides the answers after it.

set -u
cd "$(dirname "$0")" || exit 2
SPIKE="$PWD"
EMU="$SPIKE/../emulator"

GHOSTTY_REPO=https://github.com/ghostty-org/ghostty.git
GHOSTTY_COMMIT=e2e53f861482e080bf45054ba49ef471f9849937
ZIG_VERSION=0.16.0

LOG="$SPIKE/mac-check.log"
: >"$LOG"
mkdir -p bin dist logs .vendor

RESULTS=""
record() { # record <id> <PASS|FAIL|SKIP> <one line>
  RESULTS="${RESULTS}$(printf '%-4s %-5s %s' "$1" "$2" "$3")
"
  printf '    -> %s %s\n' "$2" "$3" | tee -a "$LOG"
}
say() { printf '\n=== %s\n' "$*" | tee -a "$LOG"; }
run() { # run <logname> <cmd...>   output to logs/<logname>.log, tail on failure
  "$@" >"logs/$1.log" 2>&1
}
bytes() { stat -f%z "$1" 2>/dev/null || echo "?"; }
# The first line that says what went wrong. `tail -1` of a compiler log is
# usually the ^~~~ under the error, which names nothing.
why() { grep -m1 -E 'error:|FATAL|fatal:' "$1" 2>/dev/null || tail -1 "$1" 2>/dev/null; }

# ---------------------------------------------------------------------------
say "0. environment"
{
  echo "macOS: $(sw_vers -productVersion 2>/dev/null) ($(sw_vers -buildVersion 2>/dev/null))"
  echo "arch:  $(uname -m)"
  echo "go:    $(go version 2>/dev/null || echo MISSING)"
  echo "clt:   $(xcode-select -p 2>/dev/null || echo MISSING)"
  echo "clang: $(clang --version 2>/dev/null | head -1 || echo MISSING)"
} | tee -a "$LOG"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This script is the MAC half. On Linux use ./run.sh." | tee -a "$LOG"; exit 2
fi
[ "$(uname -m)" = "arm64" ] || echo "NOTE: not arm64 — the arm64 checks will be skipped." | tee -a "$LOG"

# Zig, exactly 0.16.0.
ZIG="${ZIG:-$(command -v zig 2>/dev/null || true)}"
if [ -z "$ZIG" ] || [ "$("$ZIG" version 2>/dev/null)" != "$ZIG_VERSION" ]; then
  case "$(uname -m)" in
    arm64)  ZARCH=aarch64-macos ;;
    x86_64) ZARCH=x86_64-macos ;;
  esac
  echo "fetching Zig $ZIG_VERSION ($ZARCH)" | tee -a "$LOG"
  mkdir -p .vendor/zig
  if curl -fsSL --retry 2 "https://ziglang.org/download/$ZIG_VERSION/zig-$ZARCH-$ZIG_VERSION.tar.xz" \
       | tar xJ -C .vendor/zig --strip-components=1; then
    ZIG="$SPIKE/.vendor/zig/zig"
  else
    echo "FATAL: could not fetch Zig $ZIG_VERSION; install it and rerun with ZIG=/path/to/zig" | tee -a "$LOG"; exit 2
  fi
fi
echo "zig:   $("$ZIG" version) ($ZIG)" | tee -a "$LOG"

# ---------------------------------------------------------------------------
say "1. ghostty at the pinned commit"
if [ ! -d .vendor/ghostty/.git ]; then
  git init -q .vendor/ghostty
  git -C .vendor/ghostty remote add origin "$GHOSTTY_REPO"
fi
git -C .vendor/ghostty fetch -q --depth 1 origin "$GHOSTTY_COMMIT" >>"$LOG" 2>&1
git -C .vendor/ghostty checkout -q --detach FETCH_HEAD >>"$LOG" 2>&1
got=$(git -C .vendor/ghostty rev-parse HEAD 2>/dev/null)
if [ "$got" = "$GHOSTTY_COMMIT" ]; then
  record C1 PASS "ghostty checked out at $GHOSTTY_COMMIT"
else
  record C1 FAIL "checked out '$got', wanted $GHOSTTY_COMMIT — nothing below is comparable"
  printf '\nSUMMARY\n%s' "$RESULTS" | tee -a "$LOG"; exit 1
fi

# ---------------------------------------------------------------------------
say "2. archives: aarch64-macos and x86_64-macos"
for spec in aarch64-macos:darwin-arm64 x86_64-macos:darwin-amd64; do
  triple=${spec%%:*}; name=${spec##*:}
  start=$(date +%s)
  if (cd .vendor/ghostty && "$ZIG" build -Demit-lib-vt=true -Demit-xcframework=false -Dtarget="$triple" -Doptimize=ReleaseFast) \
       >"logs/archive-$name.log" 2>&1; then
    mkdir -p "dist/$name"
    cp .vendor/ghostty/zig-out/lib/libghostty-vt.a "dist/$name/"
    record "C2" PASS "$name archive built: $(bytes "dist/$name/libghostty-vt.a") bytes in $(( $(date +%s) - start ))s"
  else
    record "C2" FAIL "$name archive did not build — tail: $(why "logs/archive-$name.log")"
  fi
done

# ---------------------------------------------------------------------------
# THE CHECK THE WHOLE STUB ROUTE HANGS ON. A Linux host links darwin with two
# empty .tbd stubs; a .tbd is a promise made to the LINKER and only dyld on a
# real Mac can say whether it is kept. Built with zig cc exactly as Linux does.
if [ "$(uname -m)" = "arm64" ] && [ -f dist/darwin-arm64/libghostty-vt.a ]; then

  say "3. NATIVE link (Apple clang, real SDK, no stubs) — does the binding run on macOS arm64?"
  if CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
       -o bin/n-darwin-arm64 ./cmd/probe >logs/n-darwin-arm64.log 2>&1; then
    out=$(./bin/n-darwin-arm64 2>&1); rc=$?
    echo "$out" | tee -a "$LOG"
    if [ $rc -eq 0 ]; then record C3 PASS "native build links and runs: ok=true"
    else record C3 FAIL "native build runs but probe exit $rc: $out"; fi
  else
    record C3 FAIL "native link failed — tail: $(why logs/n-darwin-arm64.log)"
  fi

  say "4. STUB link (zig cc + .tbd stubs, as a Linux host builds it) — does dyld keep the stubs' promise?"
  if CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="$ZIG cc -target aarch64-macos" \
       go build -trimpath -ldflags="-s -w" -tags stublibs \
       -o bin/s-darwin-arm64 ./cmd/probe >logs/s-darwin-arm64.log 2>&1; then
    out=$(./bin/s-darwin-arm64 2>&1); rc=$?
    echo "$out" | tee -a "$LOG"
    if [ $rc -eq 0 ]; then record C4 PASS "STUB-LINKED binary runs under dyld: ok=true  <-- the key answer"
    else record C4 FAIL "STUB-LINKED binary exit $rc: $out  <-- the stub route does not hold"; fi
  else
    record C4 FAIL "stub link failed on the Mac — tail: $(why logs/s-darwin-arm64.log)"
  fi

  say "5. native vs stub: what each asks dyld for"
  if [ -f bin/n-darwin-arm64 ] && [ -f bin/s-darwin-arm64 ]; then
    otool -L bin/n-darwin-arm64 | tail -n +2 | sed 's/(compat.*//' | sort >logs/otool-native.txt
    otool -L bin/s-darwin-arm64 | tail -n +2 | sed 's/(compat.*//' | sort >logs/otool-stub.txt
    echo "--- native:"; cat logs/otool-native.txt; echo "--- stub:"; cat logs/otool-stub.txt
    { echo "--- native:"; cat logs/otool-native.txt; echo "--- stub:"; cat logs/otool-stub.txt; } >>"$LOG"
    if diff -q logs/otool-native.txt logs/otool-stub.txt >/dev/null; then
      record C5 PASS "native and stub builds load the SAME dylibs"
    else
      record C5 FAIL "native and stub builds load DIFFERENT dylibs: $(diff logs/otool-native.txt logs/otool-stub.txt | grep '^[<>]' | tr '\n' ' ')"
    fi
  else
    record C5 SKIP "needs both C3 and C4 binaries"
  fi

  say "6. ad-hoc codesign the stub binary, verify, run again"
  if [ -f bin/s-darwin-arm64 ]; then
    cp bin/s-darwin-arm64 bin/s-darwin-arm64-signed
    if codesign -s - -f bin/s-darwin-arm64-signed >>"$LOG" 2>&1 \
         && codesign --verify --verbose bin/s-darwin-arm64-signed >>"$LOG" 2>&1; then
      if ./bin/s-darwin-arm64-signed >/dev/null 2>&1; then
        record C6 PASS "ad-hoc signed stub binary verifies and runs"
      else
        record C6 FAIL "signed stub binary verifies but does not run"
      fi
    else
      record C6 FAIL "codesign or verify failed — see mac-check.log"
    fi
  else
    record C6 SKIP "no stub binary from C4"
  fi
else
  record C3 SKIP "not an arm64 Mac, or no arm64 archive"
  record C4 SKIP "not an arm64 Mac, or no arm64 archive"
  record C5 SKIP "not an arm64 Mac"
  record C6 SKIP "not an arm64 Mac"
fi

# ---------------------------------------------------------------------------
say "7. universal: amd64 stub slice + arm64 stub slice, lipo, run both"
if [ -f dist/darwin-amd64/libghostty-vt.a ] && [ -f bin/s-darwin-arm64 ]; then
  if CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="$ZIG cc -target x86_64-macos" \
       go build -trimpath -ldflags="-s -w" -tags stublibs \
       -o bin/s-darwin-amd64 ./cmd/probe >logs/s-darwin-amd64.log 2>&1 \
     && lipo -create bin/s-darwin-arm64 bin/s-darwin-amd64 -output bin/s-darwin-universal >>"$LOG" 2>&1; then
    lipo -info bin/s-darwin-universal | tee -a "$LOG"
    if ./bin/s-darwin-universal >/dev/null 2>&1; then a=ok; else a=FAILED; fi
    if arch -x86_64 /usr/bin/true >/dev/null 2>&1; then
      if arch -x86_64 ./bin/s-darwin-universal >/dev/null 2>&1; then x=ok; else x=FAILED; fi
    else
      x="skipped (Rosetta not installed: softwareupdate --install-rosetta)"
    fi
    if [ "$a" = ok ] && { [ "$x" = ok ] || [ "${x#skipped}" != "$x" ]; }; then
      record C7 PASS "universal binary: arm64 slice $a, x86_64 slice $x"
    else
      record C7 FAIL "universal binary: arm64 slice $a, x86_64 slice $x"
    fi
  else
    record C7 FAIL "amd64 stub link or lipo failed — see logs/s-darwin-amd64.log"
  fi
else
  record C7 SKIP "needs the amd64 archive (C2) and the arm64 stub binary (C4)"
fi

# ---------------------------------------------------------------------------
# ADR-0065's FIRST acceptance gate: the qualification probes on macOS arm64 —
# create, ingest, extract cells, a reply, key encoding driven from terminal
# state, resize, destroy. Same drivers the Linux run used; the host build of
# the archive is what their binding links (-lm -lpthread resolve to libSystem).
say "8. the nine qualification probes on this Mac, against the Linux baseline"
if [ -d "$EMU/cmd/ghosttyvt" ]; then
  mkdir -p "$EMU/.vendor"
  [ -e "$EMU/.vendor/ghostty" ] || ln -s "$SPIKE/.vendor/ghostty" "$EMU/.vendor/ghostty"
  if (cd .vendor/ghostty && "$ZIG" build -Demit-lib-vt=true -Demit-xcframework=false -Doptimize=ReleaseFast) >logs/archive-host.log 2>&1 \
     && (cd "$EMU" && CGO_ENABLED=1 go run ./cmd/ghosttyvt) >"$SPIKE/logs/ghostty-mac.jsonl" 2>"$SPIKE/logs/ghostty-mac.err"; then
    mac_n=$(grep -c . logs/ghostty-mac.jsonl)
    lin_n=$(grep -c . "$EMU/results/ghostty.jsonl" 2>/dev/null || echo 0)
    # Compare probe OUTCOMES, not host measurements. Probe 7 (bounded REP)
    # records wall time and Go allocator counters of the process that ran it,
    # which differ between two machines by design and say nothing about the
    # emulator's behaviour. Every other key is behaviour and must match.
    #
    # scrollback_rows under a scrollback CAP is host-dependent too, but it is not
    # noise, so it is not merely dropped. ghostty's max_lines is "a page-granular
    # heuristic: at least one standard page worth of rows is permitted and only
    # complete historical pages are removed" (src/terminal/PageList.zig at the
    # pin), and a page is sized from std.heap.page_size_min — 16 KiB on Apple
    # silicon, 4 KiB on x86_64 Linux. So the retained count past a cap differs
    # between the two by construction (first measured: 218 on Linux, 205 on a
    # Mac, cap 100). The exact diff excludes it; the assertion below keeps the
    # part that IS behaviour: no capped case retains fewer rows than its cap.
    strip() { grep -vE '"key":"(wall_ns|wall_ms|total_alloc_bytes|heap_alloc_before_bytes|heap_alloc_after_bytes|mallocs)"' "$1" \
      | grep -vE '"case":"[^"]*scrollback[0-9]+","key":"scrollback_rows"'; }
    below_cap=$(grep -E '"case":"[^"]*scrollback[0-9]+","key":"scrollback_rows"' logs/ghostty-mac.jsonl \
      | sed -E 's/.*scrollback([0-9]+)".*"value":([0-9]+).*/\1 \2/' \
      | awk '$2 < $1 { n++ } END { print n + 0 }')
    strip logs/ghostty-mac.jsonl >logs/ghostty-mac.cmp
    strip "$EMU/results/ghostty.jsonl" >logs/ghostty-linux.cmp
    if [ "$below_cap" -ne 0 ]; then
      record C8 FAIL "$below_cap capped scrollback case(s) retained FEWER rows than their cap — that is a behaviour defect, not page size"
    elif diff -q logs/ghostty-linux.cmp logs/ghostty-mac.cmp >/dev/null; then
      record C8 PASS "probes run on macOS arm64 and match the Linux baseline ($mac_n observations; capped scrollback checked as >= cap)"
    else
      nd=$(diff logs/ghostty-linux.cmp logs/ghostty-mac.cmp | grep -c '^[<>]')
      record C8 FAIL "probes run ($mac_n obs, Linux had $lin_n) but $nd lines differ — diff logs/ghostty-linux.cmp logs/ghostty-mac.cmp"
    fi
  else
    record C8 FAIL "probes did not build or run — tail: $(why logs/ghostty-mac.err)"
  fi
else
  record C8 SKIP "emulator spike not found at $EMU"
fi

# ---------------------------------------------------------------------------
# Optional, strict: the EXACT binary a Linux build host produced, copied over.
# C4 rebuilds the stub binary on this Mac with the same Zig and stubs; this
# runs the Linux-made one itself, byte for byte.
say "9. optional: the Linux-built stub binary itself"
if [ -n "${LINUX_STUB:-}" ] && [ -f "$LINUX_STUB" ]; then
  chmod +x "$LINUX_STUB"
  echo "sha256: $(shasum -a 256 "$LINUX_STUB" | cut -d' ' -f1)" | tee -a "$LOG"
  xattr -d com.apple.quarantine "$LINUX_STUB" 2>/dev/null || true
  out=$("$LINUX_STUB" 2>&1); rc=$?
  echo "$out" | tee -a "$LOG"
  if [ $rc -eq 0 ]; then record C9 PASS "the Linux-built stub binary runs on this Mac"
  else record C9 FAIL "the Linux-built stub binary exit $rc: $out"; fi
else
  record C9 SKIP "set LINUX_STUB=/path/to/s-darwin-arm64 to run the Linux-built binary"
fi

# ---------------------------------------------------------------------------
{
  printf '\n================ SUMMARY (paste this back) ================\n'
  echo "macOS $(sw_vers -productVersion 2>/dev/null) $(uname -m) · $(go version 2>/dev/null | cut -d' ' -f3) · zig $("$ZIG" version)"
  printf '%s' "$RESULTS"
  echo "full log: $LOG"
} | tee -a "$LOG"
