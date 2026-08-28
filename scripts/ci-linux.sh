#!/bin/sh
# Run the CI `backend-linux` job locally — both matrix variants, on the runner's
# image, package set and core count (nocx-cn86).
#
#   scripts/ci-linux.sh                 # both variants
#   scripts/ci-linux.sh --keyring       # with a Secret Service only
#   scripts/ci-linux.sh --no-keyring    # without one only
#   scripts/ci-linux.sh -- ./internal/ssh/...   # narrow the package set
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
CPUS="${NOCX_CI_CPUS:-0}"

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
    CPU_LABEL="$CPUS cpus (capped by NOCX_CI_CPUS — a debugging aid, not the gate)"
fi

run_variant() {
    _label="$1"
    _cmd="$2"
    printf '\n=== backend-linux (%s) — %s, -count=1, %s ===\n' "$_label" "$CPU_LABEL" "$PKGS"
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
    run_variant "no Secret Service" 'go test -race -tags gtk3 -count=1 $PKGS' || RC=1
fi

if [ "$RUN_KEYRING" = 1 ]; then
    # Byte-for-byte the job's own sequence: a session bus, a daemon started
    # with a login password, an explicit unlock, then the suite.
    run_variant "with Secret Service" '
        dbus-run-session -- bash -c "
            set -euo pipefail
            eval \"\$(echo -n nocx-ci | gnome-keyring-daemon --daemonize --login)\"
            echo -n nocx-ci | gnome-keyring-daemon --unlock
            go test -race -tags gtk3 -count=1 $PKGS
        "' || RC=1
fi

if [ "$RC" = 0 ]; then
    printf '\n=== backend-linux: both variants green (macOS is NOT covered — see the header) ===\n'
else
    printf '\n=== backend-linux: FAILED ===\n' >&2
fi
exit "$RC"
