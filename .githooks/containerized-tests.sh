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

GO_IMAGE="golang:1.26-bookworm"
NODE_IMAGE="node:22-bookworm-slim"

require_docker() {
    if ! command -v docker >/dev/null 2>&1 || ! docker version >/dev/null 2>&1; then
        printf 'FAIL: Docker/OrbStack is required to run tests in a container.\n' >&2
        printf '      Start OrbStack (or Docker Desktop) and retry. To skip the\n' >&2
        printf '      hook for one commit only: git commit --no-verify\n' >&2
        return 1
    fi
}

# `go test -race ./...` with the module mounted read-only. CGO/race needs a C
# compiler, which the full bookworm image carries.
go_test_containerized() {
    require_docker || return 1
    docker run --rm \
        -v "$PWD:/src:ro" \
        -v nocx-hook-gomod:/go/pkg/mod \
        -v nocx-hook-gobuild:/root/.cache/go-build \
        -w /src \
        "$GO_IMAGE" \
        go test -race -count=1 ./...
}

# vitest against a Linux workspace assembled in a persistent volume from the
# read-only source, minus node_modules. `npm ci` reinstalls a Linux node_modules
# each run; the npm cache volume keeps that fast.
vitest_containerized() {
    require_docker || return 1
    docker run --rm \
        -v "$PWD/frontend:/src:ro" \
        -v nocx-hook-fe:/work \
        -v nocx-hook-npm:/root/.npm \
        -w /work \
        "$NODE_IMAGE" \
        sh -euc '
            find /work -mindepth 1 -maxdepth 1 ! -name node_modules -exec rm -rf {} +
            (cd /src && find . -mindepth 1 -maxdepth 1 ! -name node_modules -exec cp -a {} /work/ \;)
            npm ci --prefer-offline --no-audit --no-fund
            npm test
        '
}
