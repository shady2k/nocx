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
    printf "      because the Dolt remote is the only copy a fresh clone can read.\n" >&2
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

# Publish the issue export to the git remote as a standalone ref.
#
# This is the spare copy of the backlog, and it exists for exactly one failure:
# nocx-wj4 records that concurrent pushes to a git-protocol Dolt remote can
# strand history in refs/dolt/data. When that happens the healthy backlog is the
# local Dolt database, and this ref is what carries it off the machine.
#
# Which is why this runs in a hook and not in CI. A GitHub Action can only read
# the remote — so in the one moment this insurance exists for, it would faithfully
# back up the broken state. The developer's database is the good copy and a hook
# on their machine is the only thing that can reach it.
#
# A ref, not a branch: refs/beads/snapshot does not appear in the branch list, is
# not part of any pull request and is not cloned by default — exactly like the
# refs/dolt/data that already lives on the same remote. Recovery is three lines,
# and they are in README.md.
#
# Nothing here touches the working tree or the index. The blob goes straight into
# the object database and the commit is built with plumbing, so a push never
# rewrites a file under somebody's editor.
#
# Failure policy is the pull side's, not the push side's: every branch returns 0
# and at most warns. A push that fails because the SPARE copy could not be made
# is a push somebody learns to make with --no-verify, and then neither the spare
# nor the real sync happens.
#
# Three details are load-bearing, all three found in review before this shipped:
#
#   --no-verify on the inner push. Without it, `git push` inside pre-push runs
#   pre-push again — recursion with no floor, each level also firing bd dolt push
#   at the .dolt/noms/LOCK every worktree on this machine shares. Measured in a
#   throwaway repo: 5 levels deep and still going, versus 1 with the flag.
#
#   origin, not the remote git is pushing to (which git passes as $1). A
#   `git push fork feature` would put the snapshot in fork and leave the
#   canonical origin/refs/beads/snapshot silently stale, which is worse than
#   having none: README recovers from origin.
#
#   A timeout around the push as well as around the export. An SSH remote waiting
#   on interactive auth would otherwise hold a push open forever — the one thing
#   this function promises never to do.
publish_beads_snapshot() {
    command -v bd >/dev/null 2>&1 || return 0
    git remote get-url origin >/dev/null 2>&1 || return 0

    BD_GIT_HOOK=1
    export BD_GIT_HOOK

    # A variable rather than the if/elif/else the other two functions inline,
    # because this one needs the same timeout twice. -k for the reason recorded
    # above them: bd ignores SIGTERM and a plain timeout leaves it holding the
    # shared lock (nocx-v48vl).
    timeout_secs=${BEADS_SNAPSHOT_TIMEOUT:-60}
    if command -v timeout >/dev/null 2>&1; then
        _t="timeout -k 5 $timeout_secs"
    elif command -v gtimeout >/dev/null 2>&1; then
        _t="gtimeout -k 5 $timeout_secs"
    else
        _t=""
    fi

    _snap=$(mktemp) || return 0

    $_t bd export >"$_snap" 2>/dev/null && bd_exit=0 || bd_exit=$?

    if [ "$bd_exit" -eq 3 ]; then
        rm -f "$_snap" || :
        return 0 # no database in this clone
    fi
    if [ "$bd_exit" -ne 0 ]; then
        rm -f "$_snap" || :
        printf "\nWARN: bd export exited %s — no backlog snapshot published this push.\n" \
            "$bd_exit" >&2
        return 0
    fi

    _blob=$(git hash-object -w --stdin <"$_snap") || _blob=""
    rm -f "$_snap" || :
    if [ -z "$_blob" ]; then
        printf "\nWARN: could not write the backlog snapshot blob — none published.\n" >&2
        return 0
    fi

    _tree=$(printf '100644 blob %s\tissues.jsonl\n' "$_blob" | git mktree) || _tree=""
    if [ -z "$_tree" ]; then
        printf "\nWARN: could not build the backlog snapshot tree — none published.\n" >&2
        return 0
    fi

    _commit=$(git commit-tree "$_tree" -m "beads snapshot") || _commit=""
    if [ -z "$_commit" ]; then
        printf "\nWARN: could not build the backlog snapshot commit — none published.\n" >&2
        printf "      A clone with no committer identity cannot mint one; git config user.email.\n" >&2
        return 0
    fi

    # --force because each snapshot is a fresh root commit: consecutive ones share
    # no history, so every push after the first is a non-fast-forward.
    if ! $_t git push --no-verify --force --quiet origin "$_commit:refs/beads/snapshot" 2>/dev/null; then
        printf "\nWARN: could not publish the backlog snapshot to origin.\n" >&2
        printf "      The backlog itself is unaffected — this is only the spare copy.\n" >&2
        return 0
    fi

    return 0
}
