#!/usr/bin/env bash
# Connect the active worktree to this machine's tracker ref, then install the
# project policy into the recipe's published mechanism island.
#
# TWO THINGS ARE DECIDED HERE THAT THE MECHANISM CANNOT DECIDE FOR A CLONE
# (nocx-3n1ly):
#
# - Whether the tracker ref ARRIVED. tracker-sync's connect creates the store
#   from refs/tracker/state, and when its own 20 s fetch misses it writes an
#   empty store commit and carries on (`ensureStore` in tracker-sync.mjs, a
#   set-owned file this project must not edit). The clone then has a tracker
#   with no issues in it presented as the real one. The ref is therefore fetched
#   here first, into a probe ref so a machine's unpublished store commits are
#   never touched, and a ref the remote advertises but this clone cannot receive
#   stops connect before a store exists.
# - Whether a REFUSED publish is a failure. Many clones cannot write to the
#   remote at all - a VM with read-only access, a contributor with no push
#   rights. That clone is still a working clone, so the hooks are linked and the
#   tracker is checked BEFORE the publish, and a refusal leaves them in place
#   with a line that says so. Any other publish failure is still a failure.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"
gate=.githooks/backlog-gate
mechanism="$gate/tracker-sync.mjs"
recipe="$gate/trackers/br.mjs"
policy="$gate/tracker-policy.yaml"
ref=refs/tracker/state
remote=origin
for path in "$mechanism" "$recipe" "$policy" "$gate/argv.mjs" "$gate/regular-file.mjs" "$gate/tracker-sync.version"; do
    [ -r "$path" ] || { printf 'tracker connect: %s is missing; run setup-shady2k-skills\n' "$path" >&2; exit 2; }
done
command -v node >/dev/null 2>&1 || { printf 'tracker connect: node is missing\n' >&2; exit 2; }
command -v git >/dev/null 2>&1 || { printf 'tracker connect: git is missing\n' >&2; exit 2; }
command -v br >/dev/null 2>&1 || { printf 'tracker connect: br is missing\n' >&2; exit 2; }

# The number of items the state file holds at a revision; 0 when it holds none,
# including when the file is absent. `.beads/issues.jsonl` is this project's own
# export, the same path scripts/connect-clone.sh requires. `|| true` is what
# keeps `set -o pipefail` from turning a missing file into a failed script.
issues_at() {
    git cat-file -p "$1:.beads/issues.jsonl" 2>/dev/null | wc -l | tr -d ' ' || true
}

# The number of items this checkout's tracker holds. `br` is what a person
# reads, so the count is asked of it, never of the state file.
tracker_items() {
    br count --status all --json | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const x=JSON.parse(s);if(typeof x.count!=="number")process.exit(2);process.stdout.write(String(x.count))})'
}

# ---- 1. the tracker ref, before anything is written ------------------------
# A probe ref, never refs/tracker/state itself: this machine's store may hold
# commits the remote has not got yet, and a forced fetch into the ref it is
# checked out on would drop them.
probe=refs/tracker-connect/probe
remote_issues=0
if said=$(git ls-remote --exit-code "$remote" "$ref" 2>&1); then
    if ! GIT_TERMINAL_PROMPT=0 git fetch --no-tags "$remote" "+$ref:$probe" >/dev/null 2>&1; then
        printf 'tracker connect: the remote carries %s but it did not arrive; no store was created.\n' "$ref" >&2
        printf 'tracker connect: fix the fetch (network, credentials, a proxy) and run make connect again\n' >&2
        exit 1
    fi
    probe_oid=$(git rev-parse -q --verify "$probe^{commit}") ||
        { printf 'tracker connect: %s did not arrive as a commit\n' "$ref" >&2; exit 1; }
    remote_issues=$(issues_at "$probe")
    # A clone has no ref until it is fetched; a machine that is already
    # connected keeps the one its store is checked out on.
    if ! git rev-parse -q --verify "$ref" >/dev/null; then
        git update-ref "$ref" "$probe_oid"
        printf 'tracker connect: fetched %s from %s (%s issues)\n' "$ref" "$remote" "$remote_issues"
    fi
    git update-ref -d "$probe"
else
    status=$?
    if [ "$status" -ne 2 ]; then
        printf 'tracker connect: the remote could not be asked for %s: %s\n' "$ref" "${said:-no answer}" >&2
        exit 1
    fi
    printf 'tracker connect: the remote has no %s yet; this clone starts an empty store\n' "$ref"
fi

# ---- 2. the machine's mechanism, and the store -----------------------------
# tracker-sync refuses to embed a machine-specific state path into a hooks
# folder that will be committed. This project's hook folder is tracked, so use
# the per-machine external hooks folder and keep the project hook entrypoints
# as links there.
common=$(git rev-parse --git-common-dir)
case "$common" in /*) ;; *) common="$repo/$common" ;; esac
main_checkout=$(cd "$(dirname "$common")" && pwd -P)
machine_key=$(basename "$main_checkout")
state_base=${XDG_STATE_HOME:-"$HOME/.local/state"}
# A hooks folder is shared by every worktree of a repository when its path is
# keyed only by the machine. Symlinking one checkout's project hooks there makes
# another worktree execute the first checkout's hooks, and removing that
# checkout leaves dangling links Git silently skips. Key the folder by the
# checkout path as well, using git's own hash so no extra tool is needed.
checkout_key=$(printf '%s' "$repo" | git hash-object --stdin)
hooks="$state_base/$machine_key/hooks/$checkout_key"
mkdir -p "$hooks"
git config extensions.worktreeConfig true
git config --worktree core.hooksPath "$hooks"

# The watcher and every checkout's record hook run the machine's own copy of the
# mechanism, never a checkout's: a worktree that is removed must not take the
# sync or the recordings with it. Install that copy from this checkout's
# byte-for-byte copies of the plugin (docs/agents/backlog.md, Tracker
# synchronization), then connect through it.
machine_mech_dir="$state_base/$machine_key/mechanism"
mkdir -p "$machine_mech_dir/trackers"
for f in tracker-sync.mjs argv.mjs regular-file.mjs tracker-sync.version; do
    install -m 0644 "$gate/$f" "$machine_mech_dir/$f"
done
install -m 0644 "$recipe" "$machine_mech_dir/trackers/br.mjs"
rm -f "$machine_mech_dir/br-tracker.mjs"
mechanism="$machine_mech_dir/tracker-sync.mjs"
recipe="$machine_mech_dir/trackers/br.mjs"

machine=${NOCX_TRACKER_MACHINE:-$(hostname -s 2>/dev/null || hostname)}
connect_said=$(node "$mechanism" connect --checkout "$repo" --machine "$machine" --prefix nocx --recipe "$recipe" 2>&1) && connect_status=0 || connect_status=$?
printf '%s\n' "$connect_said"

status=$(node "$mechanism" status --json --repo "$repo" --recipe "$recipe") ||
    { printf 'tracker connect: the tracker store could not be read after connect\n' >&2; exit 1; }
store=$(printf '%s' "$status" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const x=JSON.parse(s);if(!x.store)process.exit(2);process.stdout.write(x.store)})') ||
    { printf 'tracker connect: connect left no store to attach this checkout to\n' >&2; exit 1; }

if [ "$connect_status" -ne 0 ]; then
    # THE MECHANISM STOPS AT ITS FIRST PUSH, and a clone whose remote refuses
    # this person's writes cannot satisfy that push. `pushOid` in tracker-sync.mjs
    # (set-owned: never edited here) treats a refusal it does not recognise as
    # fatal, so connect gives up after it has already built and imported the
    # store - before it points the checkout at it. Everything from here is the
    # project's: the pointer, the hooks, and a tracker that answers.
    ( cd "$store" && node "$recipe" pointer-install --checkout "$repo" ) ||
        { printf 'tracker connect: this checkout could not be put on the store at %s\n' "$store" >&2; exit 1; }
    printf 'tracker connect: connect stopped at its push (exit %s); the checkout was put on the store here\n' "$connect_status"
fi

mkdir -p "$store/.tracker-sync" "$store/.beads"
install -m 0644 "$policy" "$store/.tracker-sync/policy.yaml"
install -m 0644 "$policy" "$store/.beads/policy.yaml"

# ---- 3. hooks and a readable tracker, before anything is published ---------
# tracker-sync owns the post-* hooks; the project still owns these local gates.
for hook in pre-commit commit-msg pre-merge-commit pre-push; do
    if [ -e "$hooks/$hook" ] && [ ! -L "$hooks/$hook" ]; then
        printf 'tracker connect: refusing to replace existing hook %s\n' "$hooks/$hook" >&2
        exit 1
    fi
    ln -sfn "$repo/.githooks/$hook" "$hooks/$hook"
done

# The mechanism installs its own post-commit/post-merge/post-checkout hooks in
# the same folder, and it does so after the push it could not make. Without
# them this clone records no revision, and the commit check refuses a push for
# the missing recording (commit-links.mjs reads the recording at the tip). The
# hooks are the mechanism's to write; the line is what a person needs to know.
if [ ! -e "$hooks/post-commit" ]; then
    printf 'tracker connect: the mechanism could not install its record hooks (they follow the same push), so this clone records no revision and a push from it is refused for a missing recording until a connect that can push runs\n' >&2
fi

br where --json >/dev/null ||
    { printf 'tracker connect: br cannot reach a tracker from this checkout\n' >&2; exit 1; }
br sync --status

# A store an earlier connect created while its fetch was missing holds no
# items, and the mechanism's import cannot tell: the state it imports is the
# empty one the store's worktree carries. The ref is what says otherwise, and
# `br` is what a person reads - so the question is asked of `br`.
if [ "${remote_issues:-0}" -gt 0 ]; then
    tracker_count=$(tracker_items) ||
        { printf 'tracker connect: br could not count this checkout'"'"'s issues\n' >&2; exit 1; }
    if [ "$tracker_count" -eq 0 ]; then
        # THE MECHANISM IMPORTS ONLY A STORE THAT ALREADY EXISTED. One it has
        # just created from the ref carries the state files, but its `init`
        # leaves the database empty; the checkout's own import is
        # connect-clone.sh's, and that runs after this script. So a zero is
        # first read as "not imported yet": import what the store carries and
        # ask again. On the store this guard exists for, the state file is
        # empty too, so nothing arrives and the refusal below still fires.
        br sync --import-only >/dev/null 2>&1 || true
        tracker_count=$(tracker_items) ||
            { printf 'tracker connect: br could not count this checkout'"'"'s issues\n' >&2; exit 1; }
    fi
    if [ "$tracker_count" -eq 0 ]; then
        printf 'tracker connect: the tracker in this checkout holds no issues although %s carries %s.\n' "$ref" "$remote_issues" >&2
        printf 'tracker connect: an earlier connect ran while its fetch was missing, and the store it built is empty. Repair it with:\n' >&2
        printf '  git -C %s worktree remove --force %s\n  git update-ref -d %s\n  make connect\n' "$repo" "$store" "$ref" >&2
        exit 1
    fi
fi

# ---- 4. publication, and what a refusal means here -------------------------
publish_said=$(node "$mechanism" publish --repo "$repo" --recipe "$recipe" 2>&1) && publish_status=0 || publish_status=$?
if [ "$publish_status" -ne 0 ]; then
    printf '%s\n' "$publish_said" >&2
    # A remote that will not take this person's writes is a read-only clone,
    # not a broken one. The wording is the transports' own - git's and the
    # hosted remote's - and anything else stays a failure: a wrong guess here
    # would hide a real one.
    case "$publish_said" in
        *"No anonymous write access"*|*"Authentication failed"*|*"Permission denied"*|*denied*|*"protected ref"*|*Forbidden*|*403*)
            printf 'tracker connect: hooks and the tracker are connected, but this clone cannot publish tracker state: the remote refused the push. Tracker changes stay on this machine.\n' >&2
            exit 0
            ;;
    esac
    printf 'tracker connect: the tracker store could not be published\n' >&2
    exit 1
fi
printf 'tracker connect: policy installed from %s and published with the tracker store\n' "$policy"
