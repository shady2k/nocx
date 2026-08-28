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

# A RELATIVE CPU WEIGHT, DELIBERATELY NOT A CEILING. This hook fires on every
# commit while the developer goes on using the machine, and uncapped it took
# three of six cores: sampled on the owner's box (6 cores, 11.7 GiB) across one
# hook run, the container at 290/302/283/108/4 % CPU, desktop unusable.
# (Memory was never the constraint in that measurement — the host never dropped
# below 4.5 GiB free, peak container RSS 1.53 GiB — so there is no --memory
# here either.)
#
# NOT --cpus. A hard cap is the thing scripts/ci-linux.sh and
# e2e/run-in-container.sh removed on purpose: it "does not produce the runner,
# it produces a third machine", and it kept timing-dependent tests alive by
# reproducing the conditions they depended on. --cpu-shares has neither
# property. It is a weight in the scheduler, consulted only when two cgroups
# both want the same core: with nothing else runnable the container still gets
# every core, so an idle-machine commit has exactly today's timing profile, and
# no test can pass or fail because of this line unless something else was
# competing — in which case it was already nondeterministic.
#
# The value, against Docker's documented default weight of 1024: 512 is half of
# it. What that buys under contention depends on the cgroup version, and both
# directions are toward the desktop. On cgroup v2 (what Docker/OrbStack run
# today) runc converts shares to cpu.weight as 1+(shares-2)*9999/262142, so 512
# becomes weight 20 against the weight 100 an unconstrained cgroup carries —
# 20/120, about a sixth of contested time, i.e. ~1 of 6 cores for the hook and
# ~5 for the desktop, where it used to be 3 and 3. On cgroup v1 the ratio is
# read literally, 512/(512+1024) = a third. Neither number applies when the
# machine is idle, which is the common case and is unchanged.
CPU_SHARES=512

# The Go test runner uses a derived image (GO_TEST_IMAGE) built from
# .githooks/images/go-tests/Dockerfile — the stock golang image carries bash
# and dash but NOT zsh, and the shellintegration launcher tests fail, not
# skip, when the shell they must prove is absent (nocx-gd84). Why a derived
# image rather than an apt-get in the run command: measured 2026-08-04, an
# `apt-get update && apt-get install zsh` in a fresh container costs ~28s and
# needs Debian-mirror network on every commit, which would break the warm-run-
# offline property (the image and caches are what make a repeat commit run
# with no network); the build confines the apt cost to first use. The hook
# builds the image before every run — the BuildKit layer cache makes a warm
# build ~0.3s — so this Dockerfile is the source of truth: a package added
# there takes effect on the next commit with no manual rebuild.
GO_TEST_IMAGE="nocx-hook-go:1.26-bookworm"
GO_TEST_IMAGE_DIR="$(dirname "$0")/images/go-tests"

HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

# uid/gid-keyed cache volumes (see header). Old un-keyed volumes from the
# previous root-based runner are simply left orphaned.
GOMOD_VOL="nocx-hook-gomod-${HOST_UID}-${HOST_GID}"
GOBUILD_VOL="nocx-hook-gobuild-${HOST_UID}-${HOST_GID}"
NPM_VOL="nocx-hook-npm-${HOST_UID}-${HOST_GID}"

# FE_VOL is ALSO keyed by worktree, and the other three deliberately are not
# (nocx-x6z3). The distinction is what the volume holds. GOMOD, GOBUILD and NPM
# are content-addressed caches: two worktrees sharing them is not a hazard, it
# is the entire reason they are named volumes, and keying them per worktree
# would make every new worktree's first commit cold.
#
# /work is not a cache. vitest_containerized ASSEMBLES a workspace in it —
# it wipes everything but node_modules, copies the source in, and runs
# `npm ci`, which itself removes node_modules and reinstalls. Two worktrees
# doing that at once destroy each other's dependency tree mid-install, and the
# symptom is a module that vanished from under a running vitest:
# ERR_MODULE_NOT_FOUND on tinypool or on vitest/dist/worker.js itself. Observed
# 2026-08-17 with feat-workspaces and feat-ai-assistant committing at the same
# moment, which is the second time it cost a session an hour; AGENTS.md already
# named it as a cost invisible to whoever pays it.
#
# cksum over the absolute path: POSIX, stable across runs, and short enough to
# keep the volume name readable in `docker volume ls`.
WORKTREE_KEY="$(printf '%s' "$PWD" | cksum | cut -d' ' -f1)"
FE_VOL="nocx-hook-fe-${HOST_UID}-${HOST_GID}-${WORKTREE_KEY}"

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
#
# No `-count=1`. Go's test cache is keyed on the test binary and everything it
# reads, so a package whose code changed re-runs and one that did not is reused;
# defeating it buys nothing. Measured on this repo: 30s with the flag, 1s warm
# without it, 18 packages cached. It was never a decision — neither d7f2ef5 (the
# commit that introduced the gate) nor 6a170a2 (which moved tests into
# containers) mentions the cache — so this comment exists to stop it returning
# by reflex. If a test ever genuinely needs to bypass the cache, that test is
# reading something Go cannot see, and the fix is in the test (nocx-ro3h).
go_test_containerized() {
    require_docker || return 1
    # The shell tests need zsh, which the stock golang image lacks; the image
    # and the reason for deriving it are documented at GO_TEST_IMAGE above.
    # Build before every run: the layer cache makes a warm build ~0.3s
    # (measured 2026-08-04) and keeps the Dockerfile the source of truth.
    docker build -q -t "$GO_TEST_IMAGE" "$GO_TEST_IMAGE_DIR" >/dev/null || return 1
    # --cpu-shares is a weight, not a cap; see CPU_SHARES above. Quoted, so it
    # is always exactly one word — nothing here can word-split it away.
    docker run --rm --cpu-shares="$CPU_SHARES" \
        -v "$PWD:/src:ro" \
        -v "$GOMOD_VOL:/cache/gomod" \
        -v "$GOBUILD_VOL:/cache/gobuild" \
        -e RUN_UID="$HOST_UID" -e RUN_GID="$HOST_GID" \
        -e HOME=/tmp \
        -e GOCACHE=/cache/gobuild \
        -e GOMODCACHE=/cache/gomod \
        -w /src \
        "$GO_TEST_IMAGE" \
        sh -euc '
            chown "$RUN_UID:$RUN_GID" /cache/gomod /cache/gobuild
            # The live-sshd suite (nocx-u7uh.17) spawns a real OpenSSH server
            # as the setpriv-dropped test user; a non-root sshd serves only a
            # user the passwd database knows, so the test uid needs a passwd
            # entry with a login shell. Idempotent and scoped to this run.
            groupadd --gid "$RUN_GID" nocx-sshtest 2>/dev/null || true
            useradd -M -u "$RUN_UID" -g "$RUN_GID" -s /bin/bash -d /tmp/nocx-sshd-home nocx-sshtest 2>/dev/null || true
            exec setpriv --reuid="$RUN_UID" --regid="$RUN_GID" --clear-groups \
                go test -race -tags gtk3 ./...
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
    # Weight, not a cap — see CPU_SHARES above.
    docker run --rm --cpu-shares="$CPU_SHARES" \
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
