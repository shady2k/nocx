#!/bin/sh
# Exercise every branch of the backlog-snapshot hook with stub bd and git on PATH.
#
# Contract under test: publish_beads_snapshot NEVER blocks a push. Exit 0 in
# every case — missing bd, no database, no origin, a failed export, a failed
# plumbing call, an unreachable remote, a hung bd, a hung push — and warn only
# when something genuinely failed. Same policy as the pull side and deliberately
# unlike push_beads_state; see .githooks/beads-hook.sh.
#
# Two of these are regression tests for defects found in review before the code
# existed, and they are the reason this file is worth its length:
#   - the inner `git push` must not re-enter pre-push (unbounded recursion)
#   - the snapshot must be published even when `bd dolt push` fails, which is
#     the only scenario the snapshot exists for
#
# Run: sh scripts/test-beads-snapshot-hook.sh
set -u

SRC=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
STUB=$(mktemp -d)
REAL_GIT=$(command -v git)
PASS=0
FAIL=0

pass() { echo "OK:   $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }
want() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want '$2' got '$3')"; fi; }

check() { # check <label> <expected-exit> <actual-exit> <expect-warn:yes|no> <output>
    _label=$1 _want=$2 _got=$3 _warn=$4 _out=$5
    _ok=true
    [ "$_got" = "$_want" ] || { _ok=false; echo "  exit: want $_want got $_got"; }
    case $_warn in
        yes) printf '%s' "$_out" | grep -q WARN || { _ok=false; echo "  expected a WARN, got none"; } ;;
        no)  printf '%s' "$_out" | grep -q WARN && { _ok=false; echo "  unexpected WARN: $_out"; } ;;
    esac
    if $_ok; then pass "$_label"; else fail "$_label"; fi
}

make_bd() { printf '#!/bin/sh\n%s\n' "$1" > "$STUB/bd"; chmod +x "$STUB/bd"; }

# A git that delegates everything to the real one except the named subcommand.
# This is how each plumbing call gets a test where it fails (AGENTS.md rule 3).
make_git_failing() { # make_git_failing <subcommand> <body>
    cat > "$STUB/git" <<EOF
#!/bin/sh
if [ "\$1" = "$1" ]; then
$2
fi
exec $REAL_GIT "\$@"
EOF
    chmod +x "$STUB/git"
}
no_git_stub() { rm -f "$STUB/git"; }

fresh_sandbox() { # sets REPO and REMOTE
    REPO=$(mktemp -d)
    REMOTE=$(mktemp -d)
    git init -q --bare "$REMOTE"
    git init -q "$REPO"
    git -C "$REPO" config user.email tester@example.invalid
    git -C "$REPO" config user.name tester
    git -C "$REPO" remote add origin "$REMOTE"
    git -C "$REPO" commit -q --allow-empty -m init
}

# Runs the function the way pre-push runs it: under `set -eu`, cwd in the repo.
# `set -eu` is part of the contract — a bare $(...) that fails would kill the
# hook before its error policy could look at it.
run_publish() {
    HOOK_OUT=$(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=2 \
        sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
    HOOK_EXIT=$?
}

remote_blob() { git -C "$REMOTE" cat-file -p refs/beads/snapshot:issues.jsonl; }
have_ref() { [ -n "$(git -C "$REMOTE" for-each-ref refs/beads/snapshot)" ]; }

echo "=== publish_beads_snapshot: the happy path ==="

# 1. the ref appears and carries EXACTLY what bd printed, trailing newlines included
fresh_sandbox; no_git_stub
make_bd 'printf "%s\n" "{\"id\":\"nocx-1\"}" "{\"id\":\"nocx-2\"}"'
run_publish
check "success is silent" 0 "$HOOK_EXIT" no "$HOOK_OUT"
if have_ref; then pass "ref published"; else fail "ref published"; fi
# cmp on files, not $(...) — command substitution strips trailing newlines, so a
# hook that lost or gained one would sail through a string comparison.
PATH="$STUB:$PATH" bd export > "$STUB/expected.jsonl" 2>/dev/null
remote_blob > "$STUB/actual.jsonl"
if cmp -s "$STUB/expected.jsonl" "$STUB/actual.jsonl"; then
    pass "blob is byte-for-byte bd export"
else
    fail "blob is byte-for-byte bd export"; cmp "$STUB/expected.jsonl" "$STUB/actual.jsonl"
fi

# 2. the working tree is untouched — this runs on every push, it may not stage anything
fresh_sandbox; no_git_stub
printf 'dirty\n' > "$REPO/untracked.txt"
BEFORE=$(git -C "$REPO" status --porcelain)
make_bd 'echo "{}"'
run_publish
want "working tree untouched" "$BEFORE" "$(git -C "$REPO" status --porcelain)"

# 3. republish overwrites — consecutive snapshots share no history
fresh_sandbox; no_git_stub
make_bd 'echo "{\"v\":1}"'
run_publish
make_bd 'echo "{\"v\":2}"'
run_publish
check "republish is silent" 0 "$HOOK_EXIT" no "$HOOK_OUT"
want "republish overwrites" '{"v":2}' "$(remote_blob)"

echo "=== publish_beads_snapshot: every external call fails once ==="

# 4. bd exits 3 — no beads database in this clone
fresh_sandbox; no_git_stub
make_bd 'exit 3'
run_publish
check "no database (exit 3) skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"

# 5. bd export genuinely fails
fresh_sandbox; no_git_stub
make_bd 'echo "database is locked" >&2; exit 1'
run_publish
check "failed export warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 6. git hash-object fails
fresh_sandbox
make_bd 'echo "{}"'
make_git_failing hash-object '    echo "stub: hash-object failed" >&2; exit 1'
run_publish
check "failed hash-object warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 7. git mktree fails
fresh_sandbox
make_git_failing mktree '    echo "stub: mktree failed" >&2; exit 1'
run_publish
check "failed mktree warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 8. git commit-tree fails for the reason it actually fails in the field: a clone
# with no committer identity. Pushing existing commits works there; minting one
# does not.
fresh_sandbox; no_git_stub
EMPTY_HOME=$(mktemp -d)
git -C "$REPO" config --unset user.email
git -C "$REPO" config --unset user.name
HOOK_OUT=$(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=2 \
    HOME="$EMPTY_HOME" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    GIT_AUTHOR_NAME= GIT_AUTHOR_EMAIL= GIT_COMMITTER_NAME= GIT_COMMITTER_EMAIL= \
    sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
HOOK_EXIT=$?
check "no committer identity warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"
rm -rf "$EMPTY_HOME"

# 9. the remote is unreachable
fresh_sandbox; no_git_stub
git -C "$REPO" remote set-url origin "$REMOTE-does-not-exist"
run_publish
check "unreachable remote warns and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 10. there is no origin at all — a clone that only has a fork remote
fresh_sandbox; no_git_stub
git -C "$REPO" remote remove origin
run_publish
check "no origin skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"

echo "=== publish_beads_snapshot: nothing hangs ==="

# 11. bd hangs
fresh_sandbox; no_git_stub
make_bd 'sleep 30'
run_publish
check "hung bd hits the timeout and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 12. git push hangs — an SSH remote waiting on interactive auth. The
# unreachable-path test above fails instantly and does not model this at all.
fresh_sandbox
make_bd 'echo "{}"'
make_git_failing push '    sleep 30'
run_publish
check "hung push hits the timeout and returns 0" 0 "$HOOK_EXIT" yes "$HOOK_OUT"

# 13. bd absent entirely — a contributor who does not use beads.
# Can't just trim PATH: on NixOS bd and coreutils share /run/current-system/sw/bin,
# so dropping bd's directory also drops dirname and the hook dies for the wrong
# reason. Build a PATH with the utilities the hook needs and no bd. A stub that
# is merely non-executable does NOT work — PATH search walks straight past it to
# the real bd, and the test would then read the shared Dolt database for real.
fresh_sandbox; no_git_stub
NOBD=$(mktemp -d)
for _u in dirname timeout sleep grep sh env git mktemp rm printf; do
    _p=$(command -v "$_u" 2>/dev/null) && ln -sf "$_p" "$NOBD/$_u"
done
[ -e "$NOBD/git" ] || fail "test setup — no git to link"
HOOK_OUT=$(cd "$REPO" && PATH="$NOBD" \
    sh -c "set -eu; . '$SRC/.githooks/beads-hook.sh'; publish_beads_snapshot" 2>&1)
HOOK_EXIT=$?
check "bd absent skips silently" 0 "$HOOK_EXIT" no "$HOOK_OUT"
rm -rf "$NOBD"

echo "=== pre-push as a whole: the two defects found in review ==="

install_real_hooks() { # copy the shipped hook into the sandbox and arm it
    mkdir -p "$REPO/hooks"
    cp "$SRC/.githooks/pre-push" "$SRC/.githooks/beads-hook.sh" "$REPO/hooks/"
    chmod +x "$REPO/hooks/pre-push"
    git -C "$REPO" config core.hooksPath hooks
}

# 14. REGRESSION: the inner push must not re-enter pre-push.
# Without --no-verify this recurses without a floor, and every level also runs
# bd dolt push against the .dolt/noms/LOCK every worktree on the machine shares.
fresh_sandbox; no_git_stub
install_real_hooks
FIRED="$REPO/fired.log"; : > "$FIRED"
make_bd "printf '%s\n' '{}'; [ \"\$1\" = dolt ] && exit 0; exit 0"
# count invocations by wrapping the hook
mv "$REPO/hooks/pre-push" "$REPO/hooks/pre-push.real"
printf '#!/bin/sh\necho fired >> "%s"\nexec "$(dirname "$0")/pre-push.real" "$@"\n' "$FIRED" \
    > "$REPO/hooks/pre-push"
chmod +x "$REPO/hooks/pre-push"
(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=5 \
    git push -q origin HEAD:refs/heads/main >/dev/null 2>&1) || :
want "pre-push fires exactly once" 1 "$(wc -l < "$FIRED" | tr -d ' ')"

# 15. REGRESSION: the snapshot is published even when bd dolt push fails.
# That failure is the entire reason the snapshot exists; publishing after it,
# under set -e, would mean never publishing in the one case that matters.
fresh_sandbox; no_git_stub
install_real_hooks
make_bd 'case "$1 $2" in "dolt push") echo "remote is stranded" >&2; exit 1 ;; esac
case "$1" in export) printf "%s\n" "{\"id\":\"survivor\"}" ;; esac
exit 0'
(cd "$REPO" && PATH="$STUB:$PATH" BEADS_SNAPSHOT_TIMEOUT=5 \
    git push -q origin HEAD:refs/heads/main >/dev/null 2>&1) && PUSH_EXIT=0 || PUSH_EXIT=$?
if [ "$PUSH_EXIT" -ne 0 ]; then
    pass "a failed bd dolt push still blocks the code push"
else
    fail "a failed bd dolt push still blocks the code push"
fi
if have_ref && [ "$(remote_blob)" = '{"id":"survivor"}' ]; then
    pass "the snapshot was published anyway"
else
    fail "the snapshot was published anyway"
fi

rm -rf "$STUB"
echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
