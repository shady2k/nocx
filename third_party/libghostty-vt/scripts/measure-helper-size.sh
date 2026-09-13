#!/usr/bin/env bash
#
# Measures what `make helpers` produces per target, and prints the numbers the
# size ceiling in internal/helper/deploy/artifacts/source_test.go is derived
# from.
#
#   third_party/libghostty-vt/scripts/measure-helper-size.sh
#   third_party/libghostty-vt/scripts/measure-helper-size.sh --no-build
#
# WHY IT EXISTS. Linking libghostty-vt into the helper is not a small change:
# the spike measured +12.4 MB per Linux helper and +1.6 MB on darwin/arm64
# (.internal/spikes/buildmatrix/README.md §4), against the 4.2–4.5 MB helpers
# and the ceiling of that time. The ceiling must be re-derived from a real
# helper with the archive linked — not bumped mechanically, and not guessed
# from the spike's probe, whose -s flag interaction was left unexplained there.
#
# WHICH IS WHY IT SAYS WHICH BINARY IT MEASURED. The helper imported no
# emulator when this script was written, and it said so rather than letting a
# reader take its numbers for the size the budget is about. It does import one
# now (nocx-ygxjv.2): the measured 2026-09-13 figures are 17,044,136 (linux/
# amd64), 16,303,928 (linux/arm64), 5,614,557 (darwin/amd64) and 5,308,802
# (darwin/arm64) — the Linux pair carrying the whole of the archive, which is
# what the 20 MiB ceiling in source_test.go is derived from. The check below
# still reports whether the emulator is linked, because a run whose helper had
# stopped importing it would print smaller numbers that the budget no longer
# describes.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../../.." && pwd)"
artifacts="$repo_root/internal/helper/deploy/artifacts/bin"
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
# The ceiling the test below the numbers asserts, so the two cannot print
# different budgets: internal/helper/deploy/artifacts/source_test.go's
# maxHelperBytes, which is derived from these numbers.
ceiling_bytes=$((20 * 1024 * 1024))
build=1

while [ $# -gt 0 ]; do
  case "$1" in
    --no-build) build=0; shift ;;
    -h|--help)
      echo "usage: measure-helper-size.sh [--no-build]"
      exit 0
      ;;
    *) echo "measure-helper-size.sh: unknown argument $1" >&2; exit 2 ;;
  esac
done

cd "$repo_root"

if [ "$build" = 1 ]; then
  echo "=== make helpers"
  make helpers
fi

if go list -deps ./cmd/nocx-helper 2>/dev/null | grep -q 'internal/emulator/ghostty'; then
  linked=yes
else
  linked=no
fi

echo
echo "helper imports internal/emulator/ghostty: $linked"
if [ "$linked" = no ]; then
  echo "NOTE these sizes are the FLOOR: nothing in the helper links libghostty-vt,"
  echo "     so the budget in source_test.go no longer describes these numbers."
  echo "     Those numbers were derived from a run where the archive IS linked."
fi

echo
printf '%-16s %12s %12s %12s\n' target decompressed ceiling headroom
max=0
for t in $targets; do
  gz="$artifacts/nocx-helper-$(printf '%s' "${t%/*}-${t#*/}").gz"
  if [ ! -f "$gz" ]; then
    printf '%-16s %12s %12s %12s\n' "$t" missing - -
    continue
  fi
  size="$(gzip -dc "$gz" | wc -c)"
  [ "$size" -gt "$max" ] && max="$size"
  printf '%-16s %12s %12s %12s\n' "$t" "$size" "$ceiling_bytes" "$((ceiling_bytes - size))"
done

echo
echo "largest: $max bytes; ceiling: $ceiling_bytes bytes ($((ceiling_bytes / 1024 / 1024)) MiB)"
if [ "$max" -gt "$ceiling_bytes" ]; then
  echo "OVER THE CEILING — the budget in source_test.go needs a measured number, not this output alone."
else
  echo "within the ceiling"
fi
