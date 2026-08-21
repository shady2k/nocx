#!/usr/bin/env bash
# Container half of the AppImage release gate. Do not run this on your machine:
# it mutates the AppImage it is given and installs packages. Use
# scripts/appimage/verify-appimage.sh, which starts the container for you.
#
# The host is Arch on purpose. Arch keeps WebKitGTK's helper processes in
# /usr/lib/webkit2gtk-4.1, not the Debian multiarch path our ubuntu-built
# library has baked in, so an AppImage that silently borrowed the host's helpers
# cannot borrow them here. That is exactly the failure a user reported and no
# amount of ldd or deadcode analysis can see, because the path is data, not
# loader metadata (nocx-azxe.7).
set -uo pipefail

APPIMAGE="${1:?usage: verify-in-container.sh <appimage> <mutation>}"
MUTATION="${2:-none}"
WORK=/work
APPDIR="$WORK/squashfs-root"
LOG="$WORK/run.log"
TRACE="$WORK/exec.trace"

fail() { echo "ASSERTION FAILED: $*" >&2; exit 1; }
note() { echo "  · $*"; }

echo "=== host: $(sed -n 's/^PRETTY_NAME=//p' /etc/os-release) / mutation: $MUTATION ==="
pacman -Sy --needed --noconfirm --quiet \
  xorg-server-xvfb xorg-xauth strace python fontconfig fribidi harfbuzz >/dev/null 2>&1

# fontconfig, fribidi and harfbuzz are NOT ours to bundle: linuxdeploy excludes
# them deliberately so text rendering uses the host's fonts. Installing them
# here is what makes this container an ordinary desktop rather than an empty
# one -- but note what is still absent, which is the entire point:
for absent in gtk3 webkit2gtk-4.1; do
  pacman -Q "$absent" >/dev/null 2>&1 && fail "$absent is installed; this host cannot prove self-containment"
done
note "host has neither gtk3 nor webkit2gtk-4.1"

mkdir -p "$WORK" && cd "$WORK"
rm -rf "$APPDIR"
"$APPIMAGE" --appimage-extract >/dev/null || fail "could not extract $APPIMAGE"

HELPERS="$APPDIR/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1"
case "$MUTATION" in
  none) ;;
  no-network-process) rm -f "$HELPERS/WebKitNetworkProcess" || fail "nothing to remove" ;;
  no-web-process)     rm -f "$HELPERS/WebKitWebProcess"     || fail "nothing to remove" ;;
  unpatched)
    # Put the host path back, which is what the artefact looked like before the
    # fix. This mutation proves the patch is what does the work, not the copies.
    python3 - "$APPDIR" <<'PY' || fail "could not revert the patch"
import pathlib, sys
lib = pathlib.Path(sys.argv[1], "usr/lib/libwebkit2gtk-4.1.so.0").resolve()
new = b"usr//lib/x86_64-linux-gnu/webkit2gtk-4.1"
old = b"/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1"
d = lib.read_bytes()
assert d.count(new) == 2, d.count(new)
lib.write_bytes(d.replace(new, old))
PY
    ;;
  *) fail "unknown mutation: $MUTATION" ;;
esac
[ "$MUTATION" = none ] || note "mutation applied: $MUTATION"

export HOME="$WORK/home"
rm -rf "$HOME"; mkdir -p "$HOME"

# Trace execve only. This is the structural half of the gate: "it started" can
# be true while the helpers came from the host, and on a host with no WebKit at
# all that distinction is invisible in the log but plain in the trace.
timeout -k 5 90 strace -f -qq -e trace=execve -o "$TRACE" \
  xvfb-run -a "$APPDIR/AppRun" >"$LOG" 2>&1 &
runner=$!

shell_cwd=""
for _ in $(seq 1 60); do
  if grep -qa "session opened" "$LOG" 2>/dev/null; then
    # Capture while it is alive: the PTY's shell is a child of the app, and its
    # working directory is the invariant the AppRun chdir must not disturb.
    #
    # The process is AppRun.wrapped, not usr/bin/nocx — linuxdeploy's AppRun
    # execs the wrapper symlink, so that is the cmdline. Matching the wrong name
    # left the pid empty, `pgrep -P 0` then answered with PID 1, and the check
    # sampled THIS SCRIPT's working directory and called it a leak.
    sleep 2   # the shell is spawned just after the session is announced
    app_pid="$(pgrep -f "$APPDIR/AppRun\.wrapped" | head -1)"
    [ -n "$app_pid" ] || app_pid="$(pgrep -f "$APPDIR/usr/bin/nocx" | head -1)"
    [ -n "$app_pid" ] || fail "the app announced a session but no process matches it"
    for child in $(pgrep -P "$app_pid" 2>/dev/null); do
      case "$(cat "/proc/$child/comm" 2>/dev/null)" in
        bash|sh|zsh|dash) shell_cwd="$(readlink "/proc/$child/cwd" 2>/dev/null)" ;;
      esac
    done
    break
  fi
  grep -qaE "Unable to spawn|SIGTRAP|error while loading" "$LOG" 2>/dev/null && break
  kill -0 $runner 2>/dev/null || break
  sleep 1
done
# Tear the whole tree down. Killing strace does not stop what strace traces, and
# a bare `wait` then never returns: measured, a run that had already passed sat
# for eleven minutes with a live webview and a live shell.
pkill -9 -f "$APPDIR/usr/bin/nocx" 2>/dev/null
pkill -9 -f Xvfb 2>/dev/null
kill -9 $runner 2>/dev/null
wait $runner 2>/dev/null || true

if [ "$MUTATION" != none ]; then
  # A mutation must fail, and fail for the stated reason. "It went red" is not
  # evidence when any unrelated crash satisfies it.
  grep -qa "session opened" "$LOG" && fail "mutation '$MUTATION' still reached a working terminal"
  grep -qa "Unable to spawn a new child process" "$LOG" \
    || fail "mutation '$MUTATION' failed, but not with the helper-spawn error: $(tail -2 "$LOG")"
  echo "PASS (negative): '$MUTATION' goes red with $(grep -ao 'Failed to spawn child process [^)]*)' "$LOG" | head -1)"
  exit 0
fi

# ---- the happy path a user can see -------------------------------------
# When this fails the cause is usually the helper spawn, and the tail of the log
# is a Go register dump from the SIGTRAP — which says nothing. Report the spawn
# error when there is one, and fall back to the tail only when there is not.
if ! grep -qa "session opened" "$LOG"; then
  why="$(grep -ao 'Failed to spawn child process [^)]*)' "$LOG" | head -1)"
  [ -n "$why" ] || why="tail: $(tail -3 "$LOG" | tr '\n' ' ')"
  fail "no PTY session opened — $why"
fi
note "webview loaded, renderer connected, session opened"

# ---- the helpers ran, and ran from INSIDE the bundle -------------------
# The patched library names them by a RELATIVE path, and the AppRun hook chdir'd
# into the AppDir, so that is the form strace records — `usr//lib/…`, not
# `/work/squashfs-root/usr/lib/…`. Both forms are accepted; an ABSOLUTE path is
# the bug itself, because the only absolute one the library can produce is the
# build host's.
helper_execs() {
  grep -ao 'execve("[^"]*WebKit[A-Za-z]*Process"' "$TRACE" \
    | sed 's/execve("//; s/"$//' | sort -u
}
for helper in WebKitNetworkProcess WebKitWebProcess; do
  helper_execs | grep -qE "^(usr//lib/|$APPDIR/).*/$helper\$" \
    || fail "$helper was never executed from inside the AppDir (saw: $(helper_execs | tr '\n' ' '))"
done
note "both helpers executed from inside the AppDir"

escaped="$(helper_execs | grep '^/' | grep -v "^$APPDIR/" || true)"
[ -z "$escaped" ] || fail "a WebKit helper was reached by absolute path outside the AppDir: $escaped"
note "no WebKit helper was reached outside the AppDir"

# ---- the AppRun chdir stayed invisible to the user ---------------------
[ -n "$shell_cwd" ] || fail "could not observe the session's shell to check its cwd"
[ "$shell_cwd" = "$HOME" ] \
  || fail "the session's shell started in '$shell_cwd', not \$HOME ('$HOME') — the AppRun chdir leaked"
note "the session's shell started in \$HOME, not the AppDir"

echo "PASS: $(basename "$APPIMAGE") works on a host with no GTK and no WebKit"
