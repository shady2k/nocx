#!/bin/sh
# Tests for gate_lock_acquire/gate_lock_release in .githooks/containerized-tests.sh.
#
# The lock exists so three worktrees pushing at once do not put six containers
# on an 8 GiB Docker VM. What has to hold is narrow and worth pinning: it must
# be exclusive, it must not strand a pusher behind a holder that died, and it
# must never block a push forever — the last one because a gate that can hang a
# push is a gate people disable, which costs more than the load it saved.
#
# Runs from any cwd. Uses no Docker: only the lock functions are exercised.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"
export HOST_UID HOST_GID

# shellcheck source=/dev/null
. "$REPO/.githooks/containerized-tests.sh"

GATE_LOCK_DIR="/tmp/nocx-gate-lock-test-$$.lock"
passed=0
failed=0
cleanup() { rm -rf "$GATE_LOCK_DIR"; }
trap cleanup EXIT INT TERM

check() {
    if [ "$2" = "$3" ]; then
        printf 'OK:   %s\n' "$1"
        passed=$((passed + 1))
    else
        printf 'FAIL: %s (expected %s, got %s)\n' "$1" "$3" "$2"
        failed=$((failed + 1))
    fi
}

printf '=== exclusion ===\n'
gate_lock_acquire
check "a free lock is taken" "$GATE_LOCK_HELD" "1"
check "the directory exists" "$([ -d "$GATE_LOCK_DIR" ] && echo yes)" "yes"
check "the holder pid is recorded" "$(cat "$GATE_LOCK_DIR/pid")" "$$"

gate_lock_release
check "release clears the flag" "$GATE_LOCK_HELD" "0"
check "release removes the directory" "$([ -d "$GATE_LOCK_DIR" ] || echo gone)" "gone"

gate_lock_release
check "releasing twice is harmless" "$?" "0"

printf '=== stale reclaim ===\n'
# A holder that died leaves the directory behind. Nobody is coming to clean it,
# so the next pusher must take it rather than wait out the full timeout.
mkdir "$GATE_LOCK_DIR"
echo 999999 > "$GATE_LOCK_DIR/pid"
GATE_LOCK_WAIT=4
gate_lock_acquire
check "a dead holder's lock is reclaimed" "$GATE_LOCK_HELD" "1"
check "the pid is replaced with ours" "$(cat "$GATE_LOCK_DIR/pid")" "$$"
gate_lock_release

# The only way to observe an empty pid file is a holder that died between the
# mkdir and the write, which is the same case.
mkdir "$GATE_LOCK_DIR"
: > "$GATE_LOCK_DIR/pid"
gate_lock_acquire
check "an empty pid file counts as stale" "$GATE_LOCK_HELD" "1"
gate_lock_release

printf '=== a live holder blocks, but not forever ===\n'
sleep 300 &
live=$!
mkdir "$GATE_LOCK_DIR"
echo "$live" > "$GATE_LOCK_DIR/pid"
GATE_LOCK_WAIT=4
start=$(date +%s)
gate_lock_acquire 2>/dev/null
elapsed=$(($(date +%s) - start))
check "a live holder makes us wait" "$([ "$elapsed" -ge 4 ] && echo yes)" "yes"
check "the wait is bounded, not infinite" "$([ "$elapsed" -lt 20 ] && echo yes)" "yes"
kill "$live" 2>/dev/null || true
wait "$live" 2>/dev/null || true
rm -rf "$GATE_LOCK_DIR"

printf '\npassed: %s  failed: %s\n' "$passed" "$failed"
[ "$failed" = 0 ]
