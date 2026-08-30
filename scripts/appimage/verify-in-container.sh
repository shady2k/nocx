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

# ---- finding the session's shell ---------------------------------------
# THE SHELL IS NO LONGER A CHILD OF THE APP. Since the coordinator cutover
# (nocx-gpyxp) the PTY belongs to cmd/nocx-server, which the window spawns
# DETACHED — setsid, and on Linux from a versioned copy under the profile's
# data directory rather than from the bundle, because the AppImage's FUSE
# mount dies with the window and the daemon exists to outlive it. So the
# shell hangs off the coordinator and `pgrep -P "$app_pid"` finds nothing:
# an empty result that used to read as "could not observe" (nocx-k2q5l).
#
# What replaces the parent test is ANCESTRY, never a name. "Any bash on the
# machine" is not an option: this script is itself bash in this container and
# the app runs as its descendant, so a name-only match would have the gate
# assert its OWN working directory — the mistake recorded just below, arriving
# by a second door. Walking UP cannot make it, because this script's shell is
# an ancestor of the app and never a descendant of it.

# PPID out of /proc/<pid>/stat. Field 2 is comm, parenthesised and free to
# contain spaces, so cut after the LAST ')': what is left is "<state> <ppid> …".
ppid_of() {
  _stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 1
  [ -n "$_stat" ] || return 1
  _rest="${_stat##*) }"
  # shellcheck disable=SC2086 # deliberate split of the stat tail into fields
  set -- $_rest
  [ $# -ge 2 ] || return 1
  echo "$2"
}

# True when pid $1's PPID chain reaches any of the remaining arguments. The hop
# cap is a cheap guard against a chain that does not terminate; nothing in this
# container is anywhere near sixty-four deep.
descends_from() {
  _p="$1"; shift
  _hops=0
  while [ -n "$_p" ] && [ "$_p" != 1 ] && [ "$_p" != 0 ] && [ "$_hops" -lt 64 ]; do
    for _want in "$@"; do
      [ "$_p" = "$_want" ] && return 0
    done
    _p="$(ppid_of "$_p")" || return 1
    _hops=$((_hops + 1))
  done
  return 1
}

# Every shell whose ancestry reaches one of the given pids, one "<pid> <cwd>"
# per line. Empty output means none — which the assertion below turns into a
# failure, never a pass.
session_shells() {
  for _pid in $(ls /proc 2>/dev/null); do
    case "$_pid" in ''|*[!0-9]*) continue ;; esac
    case "$(cat "/proc/$_pid/comm" 2>/dev/null)" in
      bash|sh|zsh|dash) ;;
      *) continue ;;
    esac
    descends_from "$_pid" "$@" || continue
    _cwd="$(readlink "/proc/$_pid/cwd" 2>/dev/null)"
    [ -n "$_cwd" ] || continue
    echo "$_pid $_cwd"
  done
}

shell_cwds=""
for _ in $(seq 1 60); do
  if grep -qa "session opened" "$LOG" 2>/dev/null; then
    # Capture while it is alive: the PTY's shell is a descendant of the app or
    # of the coordinator the app spawned, and its working directory is the
    # invariant the AppRun chdir must not disturb.
    #
    # The app process is AppRun.wrapped, not usr/bin/nocx — linuxdeploy's AppRun
    # execs the wrapper symlink, so that is the cmdline. Matching the wrong name
    # left the pid empty, `pgrep -P 0` then answered with PID 1, and the check
    # sampled THIS SCRIPT's working directory and called it a leak.
    app_pid="$(pgrep -f "$APPDIR/AppRun\.wrapped" | head -1)"
    [ -n "$app_pid" ] || app_pid="$(pgrep -f "$APPDIR/usr/bin/nocx" | head -1)"
    [ -n "$app_pid" ] || fail "the app announced a session but no process matches it"
    # The coordinator is usually a child of the app, but it is setsid and an
    # already-running one would have been adopted by init, so name it as a
    # second root rather than assuming the edge. comm is truncated to 15 bytes
    # and the installed copy is nocx-server-<version>-<sha256>, hence a prefix.
    coord_pids=""
    for pid in $(ls /proc 2>/dev/null); do
      case "$pid" in ''|*[!0-9]*) continue ;; esac
      case "$(cat "/proc/$pid/comm" 2>/dev/null)" in
        nocx-server*) coord_pids="$coord_pids $pid" ;;
      esac
    done
    # The shell appears a moment after the session is announced. Wait for the
    # observation itself rather than for a fixed sleep; if it never arrives the
    # list stays empty and the assertion below says so.
    for _try in $(seq 1 15); do
      # shellcheck disable=SC2086 # coord_pids is a deliberate list of pids
      shell_cwds="$(session_shells "$app_pid" $coord_pids)"
      [ -n "$shell_cwds" ] && break
      sleep 1
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
[ -n "$shell_cwds" ] || fail "could not observe the session's shell to check its cwd"
# Every shell under the app or the coordinator, not just the first: the chdir
# leaks into all of them or none, and naming the pid makes a failure readable.
while read -r shell_pid shell_cwd; do
  [ -n "$shell_pid" ] || continue
  [ "$shell_cwd" = "$HOME" ] \
    || fail "the session's shell (pid $shell_pid) started in '$shell_cwd', not \$HOME ('$HOME') — the AppRun chdir leaked"
done <<<"$shell_cwds"
note "the session's shell started in \$HOME, not the AppDir ($(echo "$shell_cwds" | wc -l) observed)"

echo "PASS: $(basename "$APPIMAGE") works on a host with no GTK and no WebKit"
