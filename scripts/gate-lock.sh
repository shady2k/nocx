#!/bin/sh
# One heavy containerized run at a time, machine-wide. Sourced by
# .githooks/containerized-tests.sh, scripts/ci-linux.sh, scripts/ci-frontend.sh
# and e2e/run-in-container.sh.
#
# WHY IT IS SHARED RATHER THAN PER-CALLER: the first version of this lock lived
# in the pre-push hook alone, and within the hour a `make ci-full` walked
# straight past it and put four jobs on the machine while five agents were
# working. A lock that only one of several entry points takes is not a lock. The
# heavy paths are the ones that start containers, so every one of them takes it.
#
# The lock is a directory because mkdir is atomic on every POSIX filesystem and
# macOS ships no flock(1). It lives in /tmp, not in the worktree: each worktree
# checks out its own copy of these scripts, so a lock inside one would be
# invisible to the others, which is the entire case being defended against.
# Keyed by uid so two accounts on one machine do not block each other.
#
# It never blocks forever. A holder that died leaves a stale directory,
# reclaimed by checking whether its pid is still alive; a holder that is merely
# slow eventually exhausts the wait, and then we warn and run anyway. A gate
# that can hang a push is a gate people disable with --no-verify, which costs
# more than the contention it avoided.
GATE_LOCK_DIR="/tmp/nocx-hook-gate-$(id -u).lock"
GATE_LOCK_WAIT=${NOCX_GATE_LOCK_WAIT:-1800}
GATE_LOCK_HELD=0

gate_lock_acquire() {
    _waited=0
    _announced=0
    while :; do
        if mkdir "$GATE_LOCK_DIR" 2>/dev/null; then
            printf '%s\n' "$$" > "$GATE_LOCK_DIR/pid" 2>/dev/null || true
            GATE_LOCK_HELD=1
            return 0
        fi

        # Stale? The holder is gone if its pid no longer exists. An unreadable
        # or empty pid file is treated as stale too: the only way to get one is
        # a holder that died between the mkdir and the write above.
        _holder=$(cat "$GATE_LOCK_DIR/pid" 2>/dev/null || echo "")
        if [ -z "$_holder" ] || ! kill -0 "$_holder" 2>/dev/null; then
            rm -rf "$GATE_LOCK_DIR" 2>/dev/null || true
            continue
        fi

        if [ "$_announced" = 0 ]; then
            printf 'WAIT: another containerized run holds the gate (pid %s).\n' "$_holder" >&2
            printf '      Waiting rather than piling containers onto one Docker VM.\n' >&2
            _announced=1
        fi

        if [ "$_waited" -ge "$GATE_LOCK_WAIT" ]; then
            printf 'WARN: gate lock still held after %ss — running anyway.\n' "$GATE_LOCK_WAIT" >&2
            return 0
        fi
        sleep 2
        _waited=$((_waited + 2))
    done
}

gate_lock_release() {
    [ "$GATE_LOCK_HELD" = 1 ] || return 0
    GATE_LOCK_HELD=0
    rm -rf "$GATE_LOCK_DIR" 2>/dev/null || true
}
