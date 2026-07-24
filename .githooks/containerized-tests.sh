#!/bin/sh
# Container-backed test runners for the pre-commit hook. Sourced by
# .githooks/pre-commit.
#
# Policy: linters and type-checks (gofumpt, golangci-lint, prettier, eslint,
# tsc) run on the host, but *tests* — `go test` and `vitest` — run inside an
# OrbStack/Docker container so they never execute against the host filesystem.
# Source is mounted read-only; build/module/npm caches live in named volumes so
# repeat runs stay warm. The host's frontend/node_modules is macOS-specific and
# must never leak into the Linux container, so the frontend workspace is
# assembled from the source without it.
#
# Tests run as the HOST user, not root. A root container makes the test
# environment semantically wrong: permission-sensitive tests (e.g. a read-only
# directory that must fail a write preflight) silently pass because root
# bypasses the mode bits — the failure surfaces only inside the container and
# nowhere a developer or CI (which runs non-root) would see it.
#
# Privilege drop happens INSIDE a single container, not by pre-chowning volumes
# in a separate `docker run` and then mounting them again with `--user`: that
# cross-container pattern races on a fresh named volume (the chown from the
# first container is not reliably visible to the second, so /work can still be
# root-owned at cp time). Instead each runner starts as root, chowns the cache
# mounts, then `exec setpriv` to the host uid/gid for the actual command — one
# container, no ordering race. HOME and caches live outside /root (a non-root
# process cannot traverse /root). Cache volumes are keyed by uid/gid so a macOS
# dev (often 501:20) and a Linux box (1000:100) never inherit each other's (or
# root's) cache ownership.

GO_IMAGE="golang:1.26-bookworm"
NODE_IMAGE="node:22-bookworm-slim"

HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

# uid/gid-keyed cache volumes (see header). Old un-keyed volumes from the
# previous root-based runner are simply left orphaned.
GOMOD_VOL="nocx-hook-gomod-${HOST_UID}-${HOST_GID}"
GOBUILD_VOL="nocx-hook-gobuild-${HOST_UID}-${HOST_GID}"
FE_VOL="nocx-hook-fe-${HOST_UID}-${HOST_GID}"
NPM_VOL="nocx-hook-npm-${HOST_UID}-${HOST_GID}"

require_docker() {
    if ! command -v docker >/dev/null 2>&1 || ! docker version >/dev/null 2>&1; then
        printf 'FAIL: Docker/OrbStack is required to run tests in a container.\n' >&2
        printf '      Start OrbStack (or Docker Desktop) and retry. To skip the\n' >&2
        printf '      hook for one commit only: git commit --no-verify\n' >&2
        return 1
    fi
}

# `go test -race ./...` with the module mounted read-only. CGO/race needs a C
# compiler, which the full bookworm image carries. The container starts as root
# to chown the cache mounts, then drops to the host user for the test run.
go_test_containerized() {
    require_docker || return 1
    docker run --rm \
        -v "$PWD:/src:ro" \
        -v "$GOMOD_VOL:/cache/gomod" \
        -v "$GOBUILD_VOL:/cache/gobuild" \
        -e RUN_UID="$HOST_UID" -e RUN_GID="$HOST_GID" \
        -e HOME=/tmp \
        -e GOCACHE=/cache/gobuild \
        -e GOMODCACHE=/cache/gomod \
        -w /src \
        "$GO_IMAGE" \
        sh -euc '
            chown "$RUN_UID:$RUN_GID" /cache/gomod /cache/gobuild
            exec setpriv --reuid="$RUN_UID" --regid="$RUN_GID" --clear-groups \
                go test -race -count=1 ./...
        '
}

# vitest against a Linux workspace assembled in a persistent volume from the
# read-only source, minus node_modules. `npm ci` reinstalls a Linux node_modules
# each run; the npm cache volume keeps that fast. The assembly + install + test
# steps (INNER) run as the host user under setpriv; cp uses `-t DIR ... {} +`
# to avoid a `\;`-in-a-shell-string quoting hazard.
vitest_containerized() {
    require_docker || return 1
    _inner='
        find /work -mindepth 1 -maxdepth 1 ! -name node_modules -exec rm -rf {} +
        (cd /src && find . -mindepth 1 -maxdepth 1 ! -name node_modules -exec cp -a -t /work/ {} +)
        cd /work
        npm ci --prefer-offline --no-audit --no-fund
        npm test
    '
    docker run --rm \
        -v "$PWD/frontend:/src:ro" \
        -v "$FE_VOL:/work" \
        -v "$NPM_VOL:/npm" \
        -e RUN_UID="$HOST_UID" -e RUN_GID="$HOST_GID" \
        -e HOME=/tmp \
        -e npm_config_cache=/npm \
        -e INNER="$_inner" \
        -w /work \
        "$NODE_IMAGE" \
        sh -euc '
            chown "$RUN_UID:$RUN_GID" /work /npm
            exec setpriv --reuid="$RUN_UID" --regid="$RUN_GID" --clear-groups sh -euc "$INNER"
        '
}
