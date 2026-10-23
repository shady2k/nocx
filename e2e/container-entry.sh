#!/usr/bin/env bash
# Inside the container: install dependencies, then hand over to the shared
# headless recipe.
#
# Only the install is container-specific — it targets the mounted node_modules
# volumes, which is why it cannot live in e2e/headless-run.sh, where CI's
# actions/setup-* has already done the equivalent. Everything after it (build
# the server, start the backend behind a home boundary, start vite, run
# playwright) is the same stack ci.yml's e2e-headless job needs, so it lives in
# one place and the two cannot drift.
set -euo pipefail

# The repo is bind-mounted from the host, so its files are owned by the host
# user while this container runs as root. Git calls that "dubious ownership"
# and refuses, and `go build` stamps VCS info by default — so the server
# build died on "error obtaining VCS status: exit status 128" before a single
# spec ran. Declaring the mount safe is the fix that leaves `go build` spelled
# the same here as in headless-run.sh, on CI and on a developer's machine;
# -buildvcs=false would have made this one path build something subtly
# different from everywhere else.
git config --global --add safe.directory /work

echo "=== npm ci (root + frontend) ==="
# NOT --silent: it hides the reason the install died, and the install is where
# this script fails when it fails. A registry timeout used to reach the reader
# as an exit code and two header lines (nocx-7jt3u). --prefer-offline is what
# makes the cache volume worth mounting; --no-audit and --no-fund drop two
# network round trips that decide nothing here.
npm ci --prefer-offline --no-audit --no-fund
(cd frontend && npm ci --prefer-offline --no-audit --no-fund)

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
# build/libghostty-vt joins them for exactly that reason and for the same
# owner — `make helpers` fetches the pinned archives there now
# (nocx-ygxjv.10). Its files are verified rather than rewritten when they are
# already the pinned bytes, so the common case needs no write; a NEW pin does,
# and that is the write that would fail as root-owned.
#
# In the EXIT trap, not after the run: the point is the FAILING run, whose
# artefacts are the ones somebody is about to read. `|| true` because a
# best-effort tidy must never be what a run reports — the test result is.
#
# Resolve the checkout owner in this container's user namespace: with rootless
# Docker the host developer is UID 0 here, not their host numeric UID.
handback() {
  local status=$?
  local owner
  if owner="$(stat -c "%u:%g" /work 2>/dev/null)"; then
    chown -R "$owner" \
      /work/.e2e /work/test-results /work/internal/helper/deploy/artifacts \
      /work/build/libghostty-vt 2>/dev/null || true
  fi
  return $status
}
trap handback EXIT

# THE PINNED WIDGET-WRAPPING ZSH PLUGINS (nocx-ruf5o), for the e2e variant
# that measures a zsh wrapped by them (e2e/first-command-completes.spec.ts).
# Provisioned here rather than baked into the image for the same reason the
# Go-test container provisions its own (.githooks/containerized-tests.sh): the
# pins have ONE owner (scripts/install-zsh-zle-plugins.sh), a pin change must
# not wait for an image rebuild, and the cost is a few seconds of shallow
# clone per run. The spec FAILS, never skips, without them — so this is
# provisioning, not decoration. Exported rather than written to $GITHUB_ENV:
# inside this container the Playwright process is a child of this shell.
bash ./scripts/install-zsh-zle-plugins.sh /tmp/nocx-zsh-plugins
export NOCX_ZSH_PLUGINS=/tmp/nocx-zsh-plugins

# A Secret Service for the one spec that needs one.
#
# e2e/vault.spec.ts case 3 is about a machine WITH a working OS keystore: that
# setting a vault up there asks for a passphrase anyway (ADR-0050 step 1).
# Without a keyring the case skips itself, and a permanently-skipped case is
# how an epic's happy path stops being watched without anybody noticing — the
# case's own comment says exactly that about itself.
#
# BEST EFFORT, deliberately: a keyring that will not start leaves the spec's
# own reachability guard to skip the case, precisely as before, and must never
# take the other specs down with it. Hence `|| true` inside, and no `set -e`
# reaching it.

# `npx playwright test` is the whole command — the same one a developer runs
# and the same one CI runs. The stand (backend + vite) is Playwright's, so
# nothing here starts or knows about it.
#
# Not `exec`: that would replace this shell and the trap above with it, and the
# handback would never run.
#
# Under dbus-run-session when one is available: the Secret Service is a SESSION
# BUS NAME, and this container has no session bus of its own. The keyring
# daemon therefore has to be started inside that session and the tests run in
# the same one, which is why the two are wrapped together rather than layered.
if command -v dbus-run-session >/dev/null 2>&1; then
  dbus-run-session -- bash -euo pipefail -c '
    if command -v gnome-keyring-daemon >/dev/null 2>&1; then
      printf "%s" nocx-ci | gnome-keyring-daemon --unlock --daemonize --components=secrets >/dev/null 2>&1 || true
    fi
    if [ "${NOCX_E2E_SHARD_FILES:-}" = "1" ]; then
      node e2e/shard-files.mjs --project "$PW_PROJECTS" --shard "$NOCX_E2E_SHARD"
    else
      npx playwright test "$@"
    fi
  ' bash "$@"
else
  if [ "${NOCX_E2E_SHARD_FILES:-}" = "1" ]; then
    node e2e/shard-files.mjs --project "$PW_PROJECTS" --shard "$NOCX_E2E_SHARD"
  else
    npx playwright test "$@"
  fi
fi
