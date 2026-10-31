#!/usr/bin/env bash
# Container half of the AppImage release gate. ADR-8c9vy815 requires the
# matched FreeType loader proof, gradient paint and deletion control below.
# Do not run this on your machine:
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

SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

APPIMAGE="${1:?usage: verify-in-container.sh <appimage> <mutation>}"
MUTATION="${2:-none}"
WORK=/work
APPDIR="$WORK/squashfs-root"
LOG="$WORK/run.log"
TRACE="$WORK/exec.trace"

fail() { echo "ASSERTION FAILED: $*" >&2; exit 1; }
note() { echo "  · $*"; }

echo "=== host: $(sed -n 's/^PRETTY_NAME=//p' /etc/os-release) / mutation: $MUTATION ==="
# The release gate stays Arch. An optional Ubuntu image exercises FreeType
# 2.13.2 with the exact same extraction, fixtures, probe and assertions.
if command -v pacman >/dev/null; then
  pacman -Sy --needed --noconfirm --quiet \
    xorg-server-xvfb xorg-xauth strace python python-fonttools \
    fontconfig fribidi harfbuzz ttf-dejavu xdotool xorg-xdpyinfo imagemagick binutils >/dev/null 2>&1 \
    || fail "could not install Arch regression host dependencies"
  pacman -Q freetype2 fontconfig harfbuzz || fail "missing modern host text libraries"
  for absent in gtk3 webkit2gtk-4.1; do
    pacman -Q "$absent" >/dev/null 2>&1 && fail "$absent installed; cannot prove self-containment"
  done
elif command -v apt-get >/dev/null; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq && apt-get install -y -qq --no-install-recommends \
    xvfb xauth strace python3 python3-fonttools fontconfig libfribidi0 \
    libharfbuzz0b libharfbuzz-icu0 fonts-dejavu-core xdotool x11-utils \
    imagemagick binutils procps libgl1 libegl1 libgles2 libgbm1 libgl1-mesa-dri \
    libx11-6 libxcb1 libwayland-client0 libgpg-error0 ca-certificates >/dev/null 2>&1 \
    || fail "could not install Ubuntu regression host dependencies"
  dpkg-query -W libfreetype6 fontconfig libharfbuzz0b || fail "missing modern host text libraries"
  for absent in libgtk-3-0 libgtk-3-0t64 libwebkit2gtk-4.1-0; do
    [ "$(dpkg-query -W -f='${db:Status-Status}' "$absent" 2>/dev/null)" != installed ] \
      || fail "$absent installed; cannot prove self-containment"
  done
else
  fail "unsupported host package manager"
fi
python3 "$SCRIPTS/colrv1-evidence.py" host || fail "host must have FreeType >=2.13"
sha256sum "$APPIMAGE" || fail "could not hash AppImage"
note "User LD_LIBRARY_PATH/LD_PRELOAD overrides unsupported; runtime maps must bind image FreeType"
note "host has neither GTK3 nor WebKitGTK"


mkdir -p "$WORK" && cd "$WORK"
rm -rf "$APPDIR"
"$APPIMAGE" --appimage-extract >/dev/null || fail "could not extract $APPIMAGE"

HELPERS="$APPDIR/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1"
case "$MUTATION" in
  none) ;;
  unbundled-baseline)
    [ -z "$(find "$APPDIR" -name 'libfreetype.so*' -print -quit)" ] \
      || fail "unbundled baseline unexpectedly contains FreeType"
    ;;
  no-freetype)
    [ -f "$APPDIR/usr/lib/libfreetype.so.6" ] || fail "no bundled FreeType to mutate"
    rm -f "$APPDIR/usr/lib/"libfreetype.so* || fail "could not remove FreeType"
    ;;
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
export XDG_CACHE_HOME="$HOME/.cache"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_DATA_HOME="$HOME/.local/share"
# This is still the same foreign-host/Xvfb harness, not Playwright's WebKit.
export DISPLAY=:99
Xvfb "$DISPLAY" -screen 0 1280x900x24 >"$WORK/xvfb.log" 2>&1 &
xvfb_pid=$!
cleanup() {
  pkill -9 -f "$APPDIR/usr/bin/nocx" 2>/dev/null || true
  pkill -9 -f "$APPDIR/AppRun\.wrapped" 2>/dev/null || true
  kill "$xvfb_pid" 2>/dev/null || true
  if [ -d /evidence ]; then
    mkdir -p "/evidence/$MUTATION"
    find "$WORK" -maxdepth 1 -type f \( -name '*.log' -o -name '*.png' -o -name '*.trace' -o -name 'fixture.json' \) \
      -exec cp '{}' "/evidence/$MUTATION/" \;
  fi
}
trap cleanup EXIT
for _try in $(seq 1 30); do
  xdpyinfo -display "$DISPLAY" >/dev/null 2>&1 && break
  kill -0 "$xvfb_pid" 2>/dev/null || fail "Xvfb exited: $(cat "$WORK/xvfb.log")"
  sleep 1
done
xdpyinfo -display "$DISPLAY" >/dev/null 2>&1 || fail "Xvfb never became ready"

if [ "$MUTATION" = none ] || [ "$MUTATION" = no-freetype ] || [ "$MUTATION" = unbundled-baseline ]; then
  python3 "$SCRIPTS/colrv1-fixture.py" "$WORK" || fail "invalid gradient font fixture"
  export FONTCONFIG_FILE="$WORK/fonts.conf"
  fc-cache -f || fail "could not cache fallback fixture"
  codepoint="$(python3 -c 'import json; print(format(json.load(open("/work/fixture.json"))["codepoint"], "x"))')"
  for candidate in 26a0 1f308 1f600; do
    for family in sans-serif monospace; do
      match="$(fc-match -f '%{file}' "$family:charset=$candidate")"
      note "FALLBACK family=$family glyph=U+$candidate file=$match"
      [ "$match" = "$WORK/fixture-fonts/noto-colrv1.ttf" ] || fail "fixture did not win $family fallback"
    done
  done
  if [ "$MUTATION" = none ]; then
    python3 - "$APPDIR" <<'PY' || fail "bundled FreeType provenance mismatch"
import hashlib, pathlib, sys
app = pathlib.Path(sys.argv[1])
ft = app / "usr/lib/libfreetype.so.6"
assert ft.is_file() and not ft.is_symlink(), "bundle must contain regular FreeType SONAME"
provenance = app / "usr/share/doc/libfreetype6/nocx-provenance.txt"
p = dict(line.split("=", 1) for line in provenance.read_text().splitlines())
assert p["package"] == "libfreetype6" and p["version"] == "2.11.1+dfsg-1ubuntu0.4", p
assert p["architecture"] == "amd64", p
assert p["deb_sha256"] == "3728210ef44a7a51b3824d5c499f6fbd428584a42a65aee15794a4264c8c173a", p
assert p["elf_sha256"] == hashlib.sha256(ft.read_bytes()).hexdigest(), p
assert (provenance.parent / "copyright").stat().st_size > 0
print("BUNDLE_FREETYPE_PROVENANCE " + provenance.read_text(), flush=True)
PY
  fi
  # ldd -r is complementary to process maps: modern host fontconfig/HarfBuzz
  # may import FT symbols the older ABI-compatible library does not export.
  host_fontconfig="$(ldconfig -p | awk '/libfontconfig.so.1 / {print $NF; exit}')"
  host_harfbuzz="$(ldconfig -p | awk '/libharfbuzz.so.0 / {print $NF; exit}')"
  for elf in "$APPDIR/usr/bin/nocx" "$HELPERS/WebKitWebProcess" "$host_fontconfig" "$host_harfbuzz"; do
    note "LDD_RELOCATIONS $elf"
    LD_LIBRARY_PATH="$APPDIR/usr/lib" ldd -r "$elf" >"$WORK/ldd-$(basename "$elf").log" 2>&1
    ldd_rc=$?
    cat "$WORK/ldd-$(basename "$elf").log"
    [ "$ldd_rc" -eq 0 ] || fail "ldd -r exited $ldd_rc for $elf"
    grep -qE 'undefined symbol:|not found' "$WORK/ldd-$(basename "$elf").log" \
      && fail "unresolved modern-host relocation for $elf"
  done
  readelf -n "$APPDIR/usr/lib/libwebkit2gtk-4.1.so.0" | grep 'Build ID:' \
    || fail "cannot identify bundled WebKit build"
  fixtures=("$WORK/fixture.json")
  : > "$WORK/colrv1-probe.log"
  exact_red=0
  for fixture in "${fixtures[@]}"; do
    scenario="$(basename "$fixture" .json)"
    LD_LIBRARY_PATH="$APPDIR/usr/lib" timeout -k 5 40 python3 "$SCRIPTS/colrv1-probe.py" \
      "$APPDIR" "$fixture" "$WORK/colrv1-$scenario.png" "$MUTATION" \
      >"$WORK/colrv1-$scenario.log" 2>&1
    probe_rc=$?
    cat "$WORK/colrv1-$scenario.log" | tee -a "$WORK/colrv1-probe.log"
    note "COLRV1_PROBE_EXIT=$probe_rc scenario=$scenario"
    if [ "$MUTATION" = no-freetype ] || [ "$MUTATION" = unbundled-baseline ]; then
      # A surviving simple graph does not preclude another fixture triggering
      # the bug. But a generic failure is not accepted as the negative control.
      if [ "$probe_rc" -eq 0 ]; then
        note "baseline survived scenario=$scenario; not red evidence"
        continue
      fi
      python3 - "$WORK/colrv1-$scenario.log" <<'PY' || fail "mutation failed without exact ColorStop assertion"
import pathlib, re, sys
lines = pathlib.Path(sys.argv[1]).read_text(errors="replace").splitlines()
assert any("colrv1_configure_skpaint" in line and "ColorStop" in line
           and re.search(r"Assertion.*__n < this->size\(\).*failed", line)
           for line in lines), "missing exact colrv1_configure_skpaint/ColorStop bounds assertion"
assert any("LOADED_FREETYPE label=probe-WebKitWebProcess" in line for line in lines), "missing renderer loader evidence"
PY
      exact_red=1
      note "exact ColorStop assertion reproduced scenario=$scenario"
    else
      [ "$probe_rc" -eq 0 ] || fail "shipped-WebKit gradient scenario=$scenario exited $probe_rc"
      grep -q COLRV1_RESPONSE_ACK "$WORK/colrv1-$scenario.log" || fail "no renderer response after $scenario"
    fi
  done
  if [ "$MUTATION" = no-freetype ] || [ "$MUTATION" = unbundled-baseline ]; then
    [ "$exact_red" -eq 1 ] || fail "$MUTATION survived every verified gradient; exact assertion NOT reproduced"
    echo "PASS (negative): $MUTATION reproduces exact COLRv1 ColorStop bounds assertion"
    exit 0
  fi
fi

# Trace execve only. This is the structural half of the gate: "it started" can
# be true while the helpers came from the host, and on a host with no WebKit at
# all that distinction is invisible in the log but plain in the trace.
timeout -k 5 90 strace -f -qq -e trace=execve -o "$TRACE" \
  "$APPDIR/AppRun" >"$LOG" 2>&1 &
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
if [ "$MUTATION" = none ] && [ -n "${app_pid:-}" ]; then
  python3 "$SCRIPTS/colrv1-evidence.py" "$app_pid" "$APPDIR" none \
    || fail "actual AppImage did not bind bundled FreeType in main AND renderer"
  # A real interactive shell emits the chosen gradient glyph as OSC title and
  # terminal output. Then send a fresh command through the live window. The
  # response file can only be written after renderer key handling reaches PTY.
  # Search by process identity, not a title that the PTY deliberately changes.
  window="$(timeout 15 xdotool search --sync --onlyvisible --pid "$app_pid" 2>/dev/null | head -1)"
  [ -n "$window" ] || fail "could not find the actual AppImage window"
  xdotool windowfocus --sync "$window" || fail "cannot focus AppImage"
  # ASCII shell escapes avoid xdotool/keyboard-layout Unicode substitution.
  glyph="$(python3 -c 'import json; print(" ".join("".join("\\U%08x" % ord(c) for c in g["text"]) for g in json.load(open("/work/fixture.json"))["candidates"]))')"
  xdotool type --clearmodifiers --delay 0 -- "printf '\\033]0;COLRV1 $glyph\\007\\nCOLRV1 OUTPUT $glyph\\n'; printf emitted > /work/title-output-emitted" \
    || fail "cannot type title/output command into AppImage"
  xdotool key Return || fail "cannot send Return to AppImage"
  for _try in $(seq 1 20); do
    [ -f "$WORK/title-output-emitted" ] && break
    kill -0 "$app_pid" 2>/dev/null || break
    sleep 1
  done
  [ -f "$WORK/title-output-emitted" ] || fail "AppImage did not accept title/output fixture command"
  import -window "$window" "$WORK/colrv1-appimage-title-output.png" \
    || fail "actual AppImage title/output screenshot failed"
  # Subsequent response is separate: startup success is not renderer survival.
  xdotool type --clearmodifiers --delay 0 -- "printf %s \"\${LD_LIBRARY_PATH-}\" > /work/terminal-loader-path; printf responded > /work/renderer-response" \
    || fail "cannot type response command into AppImage"
  xdotool key Return || fail "cannot send Return to AppImage"
  for _try in $(seq 1 20); do
    [ -f "$WORK/renderer-response" ] && break
    kill -0 "$app_pid" 2>/dev/null || break
    sleep 1
  done
  [ "$(cat "$WORK/renderer-response" 2>/dev/null)" = responded ] \
    || fail "actual AppImage stopped responding after COLRv1 title/output"
  [ -f "$WORK/terminal-loader-path" ] || fail "terminal child loader environment was not observed"
  grep -qF "$APPDIR" "$WORK/terminal-loader-path" \
    && fail "AppImage library path leaked into terminal subprocess: $(cat "$WORK/terminal-loader-path")"
  note "TERMINAL_LOADER_PATH=$(cat "$WORK/terminal-loader-path") (no AppDir leak)"
  python3 "$SCRIPTS/colrv1-evidence.py" "$app_pid" "$APPDIR" none \
    || fail "actual AppImage lost renderer after title/output"
  import -window "$window" "$WORK/colrv1-appimage-after-response.png" \
    || fail "actual AppImage response screenshot failed"
  note "APPIMAGE_COLRV1_RESPONSE_ACK title/output glyph=U+$codepoint; screenshots in /evidence/$MUTATION"
fi

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
