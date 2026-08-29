#!/usr/bin/env bash
# Run the CI `frontend` job locally — on the runner's Node, package set and
# core count (nocx-cn86's shape, applied to the last job that had no local
# equivalent).
#
#   scripts/ci-frontend.sh              # the whole job
#   scripts/ci-frontend.sh --frontend   # frontend/ only
#   scripts/ci-frontend.sh --root       # the repo-root gates only
#   NOCX_CI_CPUS=0 scripts/ci-frontend.sh   # uncapped, while iterating
#
# WHY THIS EXISTS. `make ci` ran the frontend gates on the DEVELOPER'S node
# against the developer's install, and ci.yml's own header claimed the Makefile
# "mirrors this same set of checks so green is identical locally and in CI".
# It was not, in three ways that had each already cost a red run:
#
#   node                 host's (v22 here)      setup-node@v7 with '24'
#   frontend/ gates      yes                    yes
#   repo-root gates      NO LOCAL RUNNER        typecheck, lint, format:check
#   spec coverage        NO LOCAL RUNNER        e2e/check-coverage.mjs
#
# The root row is the expensive one. `eslint .` and `prettier --check .` at the
# repo root ran ONLY in the pre-commit hook, where they had been dying on EACCES
# against the e2e container's root-owned output — a crashing gate reports
# nothing, so nothing was reported and 19 lint errors and 15 unformatted files
# accumulated behind it (nocx-z9s9.8). CI grew steps for them; nothing local
# did.
#
# The other two jobs already have their runners: scripts/ci-linux.sh is
# `backend-linux`, e2e/run-in-container.sh is `e2e`. `backend` runs on
# macos-latest and cannot be containerized — see the Makefile's `ci` target for
# what that leaves uncovered.
set -euo pipefail

# Pinned to setup-node's `node-version: '24'`. The runner resolves the newest
# 24.x; naming the major here and letting the tag float keeps the two in step
# without a version this file has to chase.
IMAGE="node:24-bookworm"
REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

RUN_FRONTEND=1
RUN_ROOT=1
case "${1:-}" in
    --frontend) RUN_ROOT=0 ;;
    --root)     RUN_FRONTEND=0 ;;
    "")         ;;
    *)          printf 'usage: %s [--frontend|--root]\n' "$0" >&2; exit 2 ;;
esac

if ! command -v docker >/dev/null 2>&1 || ! docker version >/dev/null 2>&1; then
    printf 'ci-frontend: Docker/OrbStack is required.\n' >&2
    exit 1
fi

# The runner's capacity, for the same reason scripts/ci-linux.sh and
# e2e/run-in-container.sh both default to it: ubuntu-latest on a public repo is
# 4 vCPU, and a vitest suite with timers in it is a different suite on a
# developer's core count. NOCX_CI_CPUS=0 opts out.
CPUS="${NOCX_CI_CPUS:-4}"

# One heavy containerized run at a time on this machine.
. "$(dirname "$0")/gate-lock.sh"
trap gate_lock_release EXIT INT TERM
gate_lock_acquire

cpu_flag=()
[ "$CPUS" != "0" ] && cpu_flag=(--cpus "$CPUS")

# BOTH node_modules trees in named volumes, never on the bind mount. The host's
# are macOS/arm64 artefacts and the container's are Linux: sharing them leaves
# the host holding @rollup/rollup-linux-*-gnu where it wants
# @rollup/rollup-darwin-arm64, and `npm test` on the Mac breaks after every
# container run. e2e/run-in-container.sh bought this rule the hard way; these
# are its own volumes so the two runners cannot fight over one install.
#
# AND KEYED BY WORKTREE (nocx-x6z3). The paragraph above separated this runner
# from e2e's, which was one axis short: the names were still global, so two
# WORKTREES running this script at once shared one install. A node_modules tree
# is not a cache — it is an install of one lockfile, and two branches need not
# agree on that lockfile — so the second `npm ci` reinstalls under the first
# one's running vitest and the symptom is ERR_MODULE_NOT_FOUND on a module that
# existed a second earlier. The Go and npm-download caches in the other runners
# are content-addressed and stay shared on purpose; only install trees are
# keyed. cksum over the absolute path: POSIX, stable, and short enough to keep
# `docker volume ls` readable.
WORKTREE_KEY="$(printf '%s' "$REPO" | cksum | cut -d' ' -f1)"
FE_ROOT_VOL="nocx-ci-fe-root-${WORKTREE_KEY}"
FE_FRONTEND_VOL="nocx-ci-fe-frontend-${WORKTREE_KEY}"
docker volume create "$FE_ROOT_VOL" >/dev/null
docker volume create "$FE_FRONTEND_VOL" >/dev/null

# The container runs as root on a bind mount, so anything it writes lands
# root-owned in the developer's checkout — and that is what broke `eslint .`
# and `prettier --check .` on the host (nocx-z9s9.8). Nothing here should
# write, but the handback costs nothing and the failure it prevents is the very
# gate this script exists to run.
inner='
set -euo pipefail
git config --global --add safe.directory /work

handback() {
  status=$?
  if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    chown -R "$HOST_UID:$HOST_GID" /work/frontend/dist 2>/dev/null || true
  fi
  return $status
}
trap handback EXIT

if [ "$RUN_FRONTEND" = 1 ]; then
  echo "=== frontend: npm ci ==="
  (cd /work/frontend && npm ci --silent)
  echo "=== frontend: format:check ==="; (cd /work/frontend && npm run format:check)
  echo "=== frontend: lint ===";         (cd /work/frontend && npm run lint)
  echo "=== frontend: typecheck ===";    (cd /work/frontend && npm run typecheck)
  echo "=== frontend: test ===";         (cd /work/frontend && npm test)
  echo "=== frontend: build ===";        (cd /work/frontend && npm run build)
fi

if [ "$RUN_ROOT" = 1 ]; then
  echo "=== root: npm ci ==="
  (cd /work && npm ci --silent)
  echo "=== root: typecheck (the e2e suite) ==="; (cd /work && npm run typecheck)
  echo "=== root: lint ===";                      (cd /work && npm run lint)
  echo "=== root: format:check ===";              (cd /work && npm run format:check)
  echo "=== root: every spec file is collected ==="
  (cd /work && node e2e/check-coverage.mjs)
fi
'

# A git worktree keeps no .git DIRECTORY — it keeps a .git FILE pointing at
# `<main-repo>/.git/worktrees/<name>`, which is outside the bind mount. The
# container then answers "fatal: not a git repository" at the first git command
# the job runs, and the whole frontend job dies before a single check does.
#
# So mount the common git dir at its own absolute path, which is where the .git
# file's `gitdir:` line says to look. Nothing is written to it: read-only says
# so. Empty for an ordinary checkout, whose .git is a directory already inside
# the mount. Same arrangement, and the same reasoning, as
# e2e/run-in-container.sh — this script was the one runner that never learned
# it, so `make ci-full` could not complete from a worktree at all (nocx-7wkc).
git_flag=()
if [ -f "$REPO/.git" ]; then
    git_common="$(git -C "$REPO" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
    [ -n "$git_common" ] && git_flag=(-v "$git_common:$git_common:ro")
fi

printf '=== frontend job on %s — %s cpus ===\n' "$IMAGE" "$CPUS"
exec docker run --rm -i \
    ${cpu_flag[@]+"${cpu_flag[@]}"} \
    ${git_flag[@]+"${git_flag[@]}"} \
    -v "$REPO:/work" \
    -v "$FE_ROOT_VOL":/work/node_modules \
    -v "$FE_FRONTEND_VOL":/work/frontend/node_modules \
    -e RUN_FRONTEND="$RUN_FRONTEND" \
    -e RUN_ROOT="$RUN_ROOT" \
    -e HOST_UID="$(id -u)" \
    -e HOST_GID="$(id -g)" \
    -w /work \
    "$IMAGE" \
    bash -c "$inner"
