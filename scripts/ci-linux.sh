#!/bin/sh
# Run the CI `backend-linux` job locally — both matrix variants, on the runner's
# image, package set and core count (nocx-cn86).
#
#   scripts/ci-linux.sh                 # both variants
#   scripts/ci-linux.sh --keyring       # with a Secret Service only
#   scripts/ci-linux.sh --no-keyring    # without one only
#   scripts/ci-linux.sh -- ./internal/ssh/...   # narrow the package set
#
# TWO PASSES PER VARIANT, since nocx-xk1di: the untagged build — what `make
# helpers` ships to a host nobody here owns — and then the packages that exist
# only under nocx_local_ssh, which is this machine's own helper and the ssh
# client linked into it. The second pass is scoped rather than applied to the
# whole set because the tag also EXCLUDES files (cmd/nocx-helper's
# `!nocx_local_ssh` pair, the assertion that a shipped artifact registers no ssh
# service), so one tagged run would be a deletion of coverage, not an addition.
# `make ci-backend` and `make ci-linux` hand each job's half its own tagged list
# in NOCX_LOCAL_SSH_PKGS; with nothing set the list is asked of the Makefile,
# which is where it is derived from the build constraints.
#
# WHY THIS EXISTS. The pre-commit hook runs no tests at all (nocx-hzsiv): it is
# a static gate — format, lint, types, ratchets, contracts — so nothing local
# runs the Go suite the way the runner does unless you run it here. It used to
# run `go test -race ./...` in a Debian container, and that was worse than it
# looked: no Secret Service, every core the host has, and no -count=1, so an
# unchanged package came from the test cache. On 2026-08-10 a release attempt
# and its follow-up PR both came back red from a job that hook had just
# reported green. This runs the job.
#
# WHAT IT STILL DOES NOT COVER: macOS. `backend` runs on macos-latest, where
# /bin/bash is 3.2, there is no /proc, sun_path is 104 bytes and PTY semantics
# are Darwin's. The bash 3.2 parse check runs here (the image carries bash32),
# but nothing else about macOS does. A green run here is not a green `backend`.
#
# The e2e suite has its own container and its own caveats — e2e/run-in-container.sh.
set -eu

IMAGE="nocx-ci-linux:ubuntu-24.04"
IMAGE_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/../.githooks/images/ci-linux" && pwd)"
REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

# UNCAPPED by default, on the owner's decision of 2026-08-11: a test must
# never depend on timing, so the local runner stops pretending to be a slow
# machine and runs on every core the host has.
#
# It used to default to 4, GitHub's vCPU count for a public repository, after
# TestOneLaneSeveralDomainsNoCurrentDomain failed on the runner and nowhere
# else — two adapter goroutines raced and a developer machine with cores to
# spare let the intended one win every time (nocx-x8ol). The cap did make that
# failure reproducible, and that is exactly the objection to it: it preserved
# a timing-dependent test by reproducing the conditions it depended on,
# instead of the test being fixed to wait on an observable state change.
#
# The cap could not have delivered what it promised anyway. This image is
# --platform=linux/amd64 and a developer's Mac is arm64, so the container runs
# EMULATED: byte-for-byte the runner in software, nothing like it in timing,
# whatever the core count is set to. A capped emulated run is not the runner
# either — it is a third machine. nocx-2h08 is the live example: one starved
# resource in internal/transport reporting a 30-second timeout under a
# different test name in every environment.
#
# Set NOCX_CI_CPUS to a number to cap it again while bisecting a suspected
# concurrency defect. That is a debugging tool, not the gate.
# Capped by default. AGENTS.md argues at length that a cap "does not produce
# the runner, it produces a third machine", and that argument still holds for
# what the gate MEASURES. It says nothing about what the gate COSTS the person
# running it: uncapped, this job takes every core on a laptop that is also
# running five agents. NOCX_CI_CPUS=0 restores the uncapped run when the
# machine is yours alone or a timing bug is being bisected.
CPUS="${NOCX_CI_CPUS:-4}"

# One heavy containerized run at a time on this machine.
. "$(dirname "$0")/gate-lock.sh"
trap gate_lock_release EXIT INT TERM
gate_lock_acquire


RUN_KEYRING=1
RUN_NO_KEYRING=1
PKGS="./..."
while [ $# -gt 0 ]; do
    case "$1" in
        --keyring)    RUN_NO_KEYRING=0 ;;
        --no-keyring) RUN_KEYRING=0 ;;
        --)           shift; [ $# -gt 0 ] && PKGS="$*"; break ;;
        *)            printf 'usage: %s [--keyring|--no-keyring] [-- <packages>]\n' "$0" >&2; exit 2 ;;
    esac
    shift
done

# THE PACKAGES THAT EXIST ONLY UNDER nocx_local_ssh (nocx-xk1di), and the second
# `go test` pass this runner did not have. A package whose Go files all carry
# the tag does not appear in `go list ./...` AT ALL, so it was not merely
# untested here — it was outside both package sets this script is handed, and
# the local helper's ssh client went unrun by every gate in the repository.
#
# The list belongs to the Makefile, which derives it from the build constraints
# and fails when it drifts (`make ci-local-ssh-split`), and it is asked over
# `make -s` the way ci.yml's jobs ask for the two halves of the suite. `make
# ci-backend` and `make ci-linux` pass the half their job owns in
# NOCX_LOCAL_SSH_PKGS; a bare invocation gets the whole set, matching the
# PKGS="./..." default above.
if [ -n "${NOCX_LOCAL_SSH_PKGS:-}" ]; then
    LOCAL_SSH_PKGS="$NOCX_LOCAL_SSH_PKGS"
else
    LOCAL_SSH_PKGS="$(cd "$REPO" && make -s print-local-ssh-pkgs)"
fi

# An empty list must not read as "the tagged packages passed": it is the shape
# the defect had, and go test with no arguments would report the repository
# root's absence of Go files rather than the omission.
if [ -z "$LOCAL_SSH_PKGS" ]; then
    printf 'ci-linux: the nocx_local_ssh package list is empty — refusing to run a suite that\n' >&2
    printf '          would silently skip this machine'"'"'s helper (make -s print-local-ssh-pkgs).\n' >&2
    exit 2
fi

if ! command -v docker >/dev/null 2>&1 || ! docker version >/dev/null 2>&1; then
    printf 'ci-linux: Docker/OrbStack is required.\n' >&2
    exit 1
fi

printf '=== building %s (first build fetches Go and Ubuntu packages) ===\n' "$IMAGE"
docker build -t "$IMAGE" "$IMAGE_DIR"

HOST_UID="$(id -u)"
HOST_GID="$(id -g)"
GOMOD_VOL="nocx-ci-gomod-${HOST_UID}-${HOST_GID}"
GOBUILD_VOL="nocx-ci-gobuild-${HOST_UID}-${HOST_GID}"

# Non-root, like the runner: root bypasses mode bits, so a permission-sensitive
# test passes there and nowhere a developer or CI would see it. Privilege is
# dropped inside the one container after the cache mounts are chowned — the
# same single-container pattern this script documents below.
#
# -count=1, like the runner and unlike a developer's `go test`: a warm cache
# answers for a package that was never run.
# CPU_FLAG is empty for the uncapped default, so docker is given no --cpus at
# all rather than a zero it would reject. Unquoted on purpose: an empty
# variable must expand to no word, which is what sh's word splitting does.
if [ "$CPUS" = 0 ]; then
    CPU_FLAG=""
    CPU_LABEL="every core"
else
    CPU_FLAG="--cpus=$CPUS"
    CPU_LABEL="$CPUS cpus (NOCX_CI_CPUS; 0 to uncap)"
fi

run_variant() {
    _label="$1"
    _cmd="$2"
    printf '\n=== backend-linux (%s) — %s, -count=1, %s + %s (nocx_local_ssh) ===\n' \
        "$_label" "$CPU_LABEL" "$PKGS" "$LOCAL_SSH_PKGS"
    # shellcheck disable=SC2086 # CPU_FLAG must word-split away when empty.
    docker run --rm $CPU_FLAG \
        -v "$REPO:/src:ro" \
        -v "$GOMOD_VOL:/cache/gomod" \
        -v "$GOBUILD_VOL:/cache/gobuild" \
        -e RUN_UID="$HOST_UID" -e RUN_GID="$HOST_GID" \
        -e HOME=/tmp/nocx-ci-home \
        -e GOCACHE=/cache/gobuild \
        -e GOMODCACHE=/cache/gomod \
        -e PKGS="$PKGS" \
        -e LOCAL_SSH_PKGS="$LOCAL_SSH_PKGS" \
        -e INNER="$_cmd" \
        -w /src \
        "$IMAGE" \
        sh -euc '
            chown "$RUN_UID:$RUN_GID" /cache/gomod /cache/gobuild
            mkdir -p "$HOME" && chown "$RUN_UID:$RUN_GID" "$HOME"
            # The live-sshd suite spawns a real OpenSSH server as the dropped
            # test user, and a non-root sshd serves only a user the passwd
            # database knows — same fixture requirement as the hook image.
            groupadd --gid "$RUN_GID" nocx-sshtest 2>/dev/null || true
            useradd -M -u "$RUN_UID" -g "$RUN_GID" -s /bin/bash -d "$HOME" nocx-sshtest 2>/dev/null || true
            exec setpriv --reuid="$RUN_UID" --regid="$RUN_GID" --clear-groups \
                sh -euc "$INNER"
        '
}

RC=0

if [ "$RUN_NO_KEYRING" = 1 ]; then
    run_variant "no Secret Service" '
        go test -race -tags gtk3 -count=1 $PKGS
        go test -race -tags gtk3,nocx_local_ssh -count=1 $LOCAL_SSH_PKGS
    ' || RC=1
fi

if [ "$RUN_KEYRING" = 1 ]; then
    # Byte-for-byte the job's own sequence: a session bus, a daemon started
    # with a login password, an explicit unlock, then the suite — and then the
    # tagged pass, inside the same bus, because the tag selects files and not a
    # different fixture: whichever keyring variant this is, the local helper's
    # ssh client is exercised under it.
    run_variant "with Secret Service" '
        dbus-run-session -- bash -c "
            set -euo pipefail
            eval \"\$(echo -n nocx-ci | gnome-keyring-daemon --daemonize --login)\"
            echo -n nocx-ci | gnome-keyring-daemon --unlock
            go test -race -tags gtk3 -count=1 $PKGS
            go test -race -tags gtk3,nocx_local_ssh -count=1 $LOCAL_SSH_PKGS
        "' || RC=1
fi

if [ "$RC" = 0 ]; then
    printf '\n=== backend-linux: both variants green (macOS is NOT covered — see the header) ===\n'
else
    printf '\n=== backend-linux: FAILED ===\n' >&2
fi
exit "$RC"
