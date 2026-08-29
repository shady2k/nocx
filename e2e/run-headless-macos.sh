#!/usr/bin/env bash
# Run the e2e suite natively on macOS, and prove the run did not touch the
# developer's real home.
#
# WHY THIS EXISTS ALONGSIDE THE CONTAINER. The container is Linux, and two of
# the three CI jobs run on macOS VMs — GitHub does not containerise those, and
# macOS cannot be containerised at all. Specs that drive a real shell see a
# different product there: the completion specs run bash, and macOS ships bash
# 3.2 while a Linux image ships 5.x. Those failures are invisible in the
# container by construction. This script is how they become visible.
#
# WHAT IT NO LONGER DOES, and why. It used to build cmd/devharness, start it
# behind a home boundary, start vite, and hand playwright a port and a token.
# Every one of those belongs to the stand now (e2e/stand.ts, started from
# playwright.config.ts's globalSetup), and doing them here started a SECOND
# backend beside the one the suite was actually driving. What is left is the
# one thing this script uniquely does: watch the real home across the run.
#
# WHAT THE SUITE TOUCHES, precisely, because it runs on a real person's
# machine — all of it applied by e2e/home-isolation.ts, which raises rather
# than warns if a caller tries to opt out:
#
#   $HOME            a disposable directory under .e2e/, fresh every run. This
#                    is what moves ~/.config/nocx-dev, ~/.nocx and the shell rc
#                    files out of reach; the backend resolves all of them from
#                    $HOME.
#   ~/.ssh/config    not read — it is under the disposable $HOME.
#   XDG_*            stripped, not overridden: XDG_CONFIG_HOME outranks $HOME.
#   ZDOTDIR/BASH_ENV/ENV
#                    stripped, so a shell a PTY spawns cannot read back out.
#   $TMPDIR          specs create their own mkdtemp fixtures here and remove
#                    them; nothing redirects it.
#   the repo         read-only in practice: the suite writes only test-results/
#                    and .e2e/, both git-ignored.
#
#   the keychain     NOT TOUCHED, and no longer by a variable anybody has to
#                    remember. The backend is cmd/nocx-server built WITHOUT
#                    `-tags nocx_login_session`, so it makes no claim to a
#                    login session, takes the file provider and never calls the
#                    OS keystore at all (design D10). $HOME does not move a
#                    keychain to safety — it moves it to NOTHING, because macOS
#                    looks for the login keychain under ~/Library/Keychains and
#                    a disposable home has none, so a WRITE raises "Keychain
#                    not found" on the developer's own screen (nocx-o4hg). The
#                    one spec that does want the OS store asks for the build
#                    that declares a login session, by name.
#
# If a keychain dialog appears anyway, stop the run and report it: something is
# reaching the keystore that the build stance does not cover.
#
#   e2e/run-headless-macos.sh                          # whole suite
#   e2e/run-headless-macos.sh e2e/completion.spec.ts   # one spec
#   PW_PROJECTS=chromium e2e/run-headless-macos.sh     # one browser
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

real_home="$HOME"
work="$(mktemp -d "${TMPDIR:-/tmp}/nocx-e2e-macos.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM

echo "=== boundary ==="
echo "    real home : $real_home  (must stay untouched)"

# Recorded, not guessed. The paths a leak would land in, with their mtimes, so
# the check at the end compares rather than asks the operator to remember.
watched=("$real_home/.nocx" "$real_home/.bashrc" "$real_home/.zshrc" \
  "$real_home/.ssh/config" "$real_home/Library/Application Support/nocx-dev")
snapshot() {
  for w in "${watched[@]}"; do
    if [ -e "$w" ]; then printf '%s\t%s\n' "$w" "$(stat -f '%m' "$w")"; else printf '%s\tabsent\n' "$w"; fi
  done
}
snapshot > "$work/home-before.tsv"

echo "=== playwright (the stand is its own — see e2e/stand.ts) ==="
set +e
npx playwright test "$@"
status=$?
set -e

# The boundary, checked rather than asserted.
snapshot > "$work/home-after.tsv"
if ! diff -q "$work/home-before.tsv" "$work/home-after.tsv" >/dev/null; then
  echo "" >&2
  echo "BOUNDARY LEAK — the real home changed during this run:" >&2
  diff "$work/home-before.tsv" "$work/home-after.tsv" >&2 || true
  echo "That is a bug worth filing, and worth stopping for." >&2
  exit 1
fi
echo "=== boundary intact ==="
exit $status
