#!/usr/bin/env bash
# Inside the container: install dependencies, then hand over to the shared
# headless recipe.
#
# Only the install is container-specific — it targets the mounted node_modules
# volumes, which is why it cannot live in e2e/headless-run.sh, where CI's
# actions/setup-* has already done the equivalent. Everything after it (build
# devharness, start the backend behind a home boundary, start vite, run
# playwright) is the same stack ci.yml's e2e-headless job needs, so it lives in
# one place and the two cannot drift.
set -euo pipefail

# The repo is bind-mounted from the host, so its files are owned by the host
# user while this container runs as root. Git calls that "dubious ownership"
# and refuses, and `go build` stamps VCS info by default — so the devharness
# build died on "error obtaining VCS status: exit status 128" before a single
# spec ran. Declaring the mount safe is the fix that leaves `go build` spelled
# the same here as in headless-run.sh, on CI and on a developer's machine;
# -buildvcs=false would have made this one path build something subtly
# different from everywhere else.
git config --global --add safe.directory /work

echo "=== npm ci (root + frontend) ==="
npm ci --silent
(cd frontend && npm ci --silent)

# Hand the run's output back to the host user before leaving.
#
# Everything below this runs as root on a bind mount, so .e2e/ and test-results/
# would otherwise stay root-owned in the developer's checkout — and `eslint .`
# and `prettier --check .` both die on EACCES while expanding the directory,
# which broke the local gate every time the local suite ran (nocx-z9s9.8).
#
# The helper artifacts are on the list for the same reason and are the one
# entry INSIDE the source tree: the stand builds them on its way up
# (e2e/stand.ts), so they land root-owned next to the .gitignore that hides
# them, where the next `make helpers` on the host cannot overwrite them.
#
# In the EXIT trap, not after the run: the point is the FAILING run, whose
# artefacts are the ones somebody is about to read. `|| true` because a
# best-effort tidy must never be what a run reports — the test result is.
handback() {
  local status=$?
  if [ -n "${NOCX_E2E_HOST_UID:-}" ] && [ -n "${NOCX_E2E_HOST_GID:-}" ]; then
    chown -R "$NOCX_E2E_HOST_UID:$NOCX_E2E_HOST_GID" \
      /work/.e2e /work/test-results /work/internal/helper/deploy/artifacts 2>/dev/null || true
  fi
  return $status
}
trap handback EXIT

# `npx playwright test` is the whole command — the same one a developer runs
# and the same one CI runs. The stand (backend + vite) is Playwright's, so
# nothing here starts or knows about it.
#
# Not `exec`: that would replace this shell and the trap above with it, and the
# handback would never run.
npx playwright test "$@"
