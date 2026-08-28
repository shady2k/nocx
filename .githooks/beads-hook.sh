#!/bin/sh
# Hand a git hook to beads. Sourced by .githooks/pre-commit and pre-push.
#
# Beads ships its own thin shims in .beads/hooks/, but git honours exactly one
# hook directory and `make hooks` points core.hooksPath at .githooks/ — so those
# shims never run, and the tracker only syncs when somebody remembers to do it
# by hand (nocx-lte). Calling them from here keeps a single hook root.
#
# The obvious call here would be `bd hooks run pre-push`, the CLI's own shim.
# Measured on bd 1.0.5 it is a silent no-op: it exits 0 and refs/dolt/data on the
# remote does not move. `bd dolt push` moves it. So this calls the command that
# demonstrably works — worth re-testing on the next bd upgrade, since the shim is
# the interface the tool documents.

# Failure policy, deliberately unlike the stock beads shim — that one ends in
# `exit $_bd_exit` and so blocks git on any error at all:
#
#   - bd missing, or exit 3 (no database in this clone): skip. Someone
#     contributing a patch without using beads must still commit and push.
#   - bd present and the sync genuinely failed: stop, and say what to run. This
#     is the case worth interrupting for — everything looks fine locally while
#     the remote silently rots, which is how a colleague ends up re-fixing a
#     bug that was closed days ago.
push_beads_state() {
    command -v bd >/dev/null 2>&1 || return 0

    BD_GIT_HOOK=1
    export BD_GIT_HOOK
    timeout_secs=${BEADS_HOOK_TIMEOUT:-300}

    # Never call bd bare here: these hooks run under `set -eu`, and a nonzero
    # exit would kill the script before the policy below could look at it.
    # -k: `bd dolt pull` and `bd dolt push` IGNORE SIGTERM (measured 2026-08-28,
    # nocx-v48vl: a wedged pull survived `kill` and needed `kill -9`). Plain
    # `timeout` sends TERM, reports 124 and returns, leaving the process alive
    # and still holding the embedded store's exclusive .dolt/noms/LOCK. Every
    # worktree on the machine shares that store, so one abandoned run freezes
    # `bd ready`, `bd create` and `bd export` everywhere until somebody finds
    # it by hand. That is exactly what happened: four sessions queued behind one
    # orphan, the longest waiting 85 minutes. --kill-after sends KILL so the
    # timeout actually times out.
    if command -v timeout >/dev/null 2>&1; then
        timeout -k 5 "$timeout_secs" bd dolt push && bd_exit=0 || bd_exit=$?
    elif command -v gtimeout >/dev/null 2>&1; then
        gtimeout -k 5 "$timeout_secs" bd dolt push && bd_exit=0 || bd_exit=$?
    else
        bd dolt push && bd_exit=0 || bd_exit=$?
    fi

    case $bd_exit in
        0)
            return 0
            ;;
        3)
            # No beads database here. Not this repo's business to insist.
            return 0
            ;;
        124 | 142)
            # 124 from timeout, 142 when a shell reports SIGALRM instead.
            printf "\nWARN: beads sync timed out after %ss — continuing without it.\n" \
                "$timeout_secs" >&2
            return 0
            ;;
    esac

    printf "\nFAIL: bd dolt push exited %s.\n" "$bd_exit" >&2
    printf "      Issue state did NOT sync. A fresh clone would see a stale backlog,\n" >&2
    printf "      because bd bootstrap prefers the Dolt remote over the tracked JSONL.\n" >&2
    printf "      Fix it (often: bd dolt pull, resolve, bd dolt push) and retry.\n" >&2
    printf "      To push code anyway, knowing the tracker lags: git push --no-verify\n" >&2
    return "$bd_exit"
}

# Refresh the local issue database from the Dolt remote when git brings in
# somebody else's work.
#
# Failure policy is deliberately the OPPOSITE of push_beads_state. A failed push
# means your state never left the machine while everything looks fine locally —
# worth stopping for. A failed pull means you keep the backlog you already had,
# and blocking somebody's merge over that is a worse bug than the staleness. So
# every branch here returns 0; the hook only ever warns.
#
# Timeout is shorter than the push side (60s vs 300s) because this one sits in
# front of an interactive command. A pull that needs longer than a minute is not
# worth making a person watch — they will get the data on the next pull, and the
# claim protocol in AGENTS.md pulls again at the moment it actually matters.
#
# Calls bd dolt pull directly, for the same reason the push side does. Re-tested
# on bd 1.1.0 (2026-07-25, nocx-wj4), both shims still decline to move data:
# 'bd hooks run post-merge' prints "skipping JSONL import because sync.remote is
# configured" and performs no Dolt pull at all, and 'bd hooks run pre-push' is
# still the silent no-op nocx-lte measured on 1.0.5 — refs/dolt/data does not
# move. Worth re-testing again on the next bd upgrade.
pull_beads_state() {
    command -v bd >/dev/null 2>&1 || return 0

    BD_GIT_HOOK=1
    export BD_GIT_HOOK
    timeout_secs=${BEADS_PULL_TIMEOUT:-60}

    # Never call bd bare: these hooks run under `set -eu` and a nonzero exit
    # would kill the script before the policy below could look at it.
    # -k: `bd dolt pull` and `bd dolt push` IGNORE SIGTERM (measured 2026-08-28,
    # nocx-v48vl: a wedged pull survived `kill` and needed `kill -9`). Plain
    # `timeout` sends TERM, reports 124 and returns, leaving the process alive
    # and still holding the embedded store's exclusive .dolt/noms/LOCK. Every
    # worktree on the machine shares that store, so one abandoned run freezes
    # `bd ready`, `bd create` and `bd export` everywhere until somebody finds
    # it by hand. That is exactly what happened: four sessions queued behind one
    # orphan, the longest waiting 85 minutes. --kill-after sends KILL so the
    # timeout actually times out.
    if command -v timeout >/dev/null 2>&1; then
        timeout -k 5 "$timeout_secs" bd dolt pull >/dev/null 2>&1 && bd_exit=0 || bd_exit=$?
    elif command -v gtimeout >/dev/null 2>&1; then
        gtimeout -k 5 "$timeout_secs" bd dolt pull >/dev/null 2>&1 && bd_exit=0 || bd_exit=$?
    else
        bd dolt pull >/dev/null 2>&1 && bd_exit=0 || bd_exit=$?
    fi

    case $bd_exit in
        0)
            return 0
            ;;
        3)
            # No beads database in this clone. Not this repo's business to insist.
            return 0
            ;;
        124 | 142)
            # 124 from timeout, 142 when a shell reports SIGALRM instead.
            printf "\nWARN: beads pull timed out after %ss — backlog may be stale.\n" \
                "$timeout_secs" >&2
            return 0
            ;;
    esac

    printf "\nWARN: bd dolt pull exited %s — your backlog may be behind the team's.\n" \
        "$bd_exit" >&2
    printf "      Run 'bd dolt pull' when convenient. This merge is unaffected.\n" >&2
    return 0
}

# Write .beads/issues.jsonl and stage it, so the commit carries the issue state
# it describes.
#
# This calls `bd export` rather than `bd hooks run pre-commit` on purpose. The
# export is a plain data dump behind a documented flag, so calling it directly
# is exact and synchronous. export.auto in .beads/config.yaml writes the same
# file on its own, but it is throttled to once a minute and, measured here, the
# write does not reliably land before the command returns — fine for keeping the
# file warm between commits, not something a hook can depend on. Pushing, by
# contrast, is protocol-level work left to the CLI's own shim.
export_beads_snapshot() {
    command -v bd >/dev/null 2>&1 || return 0

    BD_GIT_HOOK=1
    export BD_GIT_HOOK

    bd export -o .beads/issues.jsonl >/dev/null 2>&1 && bd_exit=0 || bd_exit=$?

    if [ "$bd_exit" -eq 3 ]; then
        return 0 # no database in this clone
    fi
    if [ "$bd_exit" -ne 0 ]; then
        printf "\nFAIL: bd export exited %s — the commit would carry a stale backlog.\n" \
            "$bd_exit" >&2
        return "$bd_exit"
    fi

    git add .beads/issues.jsonl
}
