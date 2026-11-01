#!/bin/sh
# Tests for scripts/diagnose/macos-lan-after-update.sh.
#
# Linux-runnable by construction: every macOS-only tool is a stub in $TMP/bin,
# and the "tool missing" case runs the same script with a PATH that holds none
# of them. What is tested is the script's argument handling, its output shape,
# and that a missing tool degrades to a sentence instead of aborting the
# evidence.
set -u

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
SCRIPT=$ROOT/scripts/diagnose/macos-lan-after-update.sh
TMP=$(mktemp -d "${TMPDIR:-/tmp}/nocx-lan-diag-test.XXXXXXXX")
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
BIN=$TMP/bin
mkdir -p "$BIN"
OUT=$TMP/out
ERR=$TMP/err

passed=0
failed=0
check() {
    if [ "$2" = "$3" ]; then
        printf 'OK:   %s\n' "$1"
        passed=$((passed + 1))
    else
        printf 'FAIL: %s (expected %s, got %s)\n' "$1" "$3" "$2"
        failed=$((failed + 1))
    fi
}
check_present() {
    if grep -q -- "$2" "$3"; then
        printf 'OK:   %s\n' "$1"
        passed=$((passed + 1))
    else
        printf 'FAIL: %s (%s absent)\n' "$1" "$2"
        failed=$((failed + 1))
    fi
}
check_absent() {
    if grep -q -- "$2" "$3"; then
        printf 'FAIL: %s (%s present)\n' "$1" "$2"
        failed=$((failed + 1))
    else
        printf 'OK:   %s\n' "$1"
        passed=$((passed + 1))
    fi
}

# Paths the stubs report. The helper and the app exist so the code-signing
# reading is exercised; the responsible pid below is absent from the table so
# the exited-responsible case is exercised too.
APP=$TMP/apps/nocx.app
APPEXE=$APP/Contents/MacOS/nocx
HELPER=$TMP/home/.nocx/helper/deadbeef/nocx-helper
mkdir -p "$APP/Contents/MacOS" "$TMP/home/.nocx/helper/deadbeef"
: > "$APPEXE"
: > "$HELPER"

# The process table the stubs answer from. $$ is this test script, which is the
# direct parent of every `sh "$SCRIPT"` invocation below (invocations redirect
# to files, never to command substitution, so the parent pid stays known).
TEST_PID=$$
cat > "$TMP/ps-table" <<EOF
1200 1 $HELPER
$TEST_PID 1200 /bin/zsh
1500 1 $APPEXE
EOF
cat > "$TMP/ps-table-running" <<EOF
1200 1 $HELPER
$TEST_PID 1200 /bin/zsh
1500 1 $APPEXE
4242 1 $APPEXE
EOF

day=2026-10-30T12:00:00Z
cat > "$TMP/log" <<EOF
$day host nehelper[301]: LNP policy denied pid $TEST_PID (responsible 4242)
$day host tccd[302]: client $TEST_PID responsible 4242 denied
$day host nehelper[301]: unrelated pid 999999 allowed
EOF

cat > "$BIN/uname" <<'UNAME'
#!/bin/sh
case ${1:-} in
  -s) printf '%s\n' "${STUB_UNAME:-Darwin}" ;;
  -r) printf '24.0.0\n' ;;
  -m) printf 'arm64\n' ;;
  *) printf '%s\n' "${STUB_UNAME:-Darwin}" ;;
esac
UNAME

cat > "$BIN/ps" <<'PS'
#!/bin/sh
table=${PS_TABLE:?}
mode=
pid=
format=
while [ $# -gt 0 ]; do
  case $1 in
    -axo) mode=all; format=$2; shift 2 ;;
    -p) pid=$2; shift 2 ;;
    -o) format=$2; shift 2 ;;
    *) shift ;;
  esac
done
if [ "$mode" = all ]; then
  case $format in
    comm=) awk '{print $3}' "$table" ;;
    *) awk '{print $1" "$2" "$3}' "$table" ;;
  esac
  exit 0
fi
line=$(awk -v p="$pid" '$1==p' "$table")
[ -n "$line" ] || exit 1
case $format in
  ppid=) printf '%s\n' "$line" | awk '{print $2}' ;;
  comm=) printf '%s\n' "$line" | awk '{print $3}' ;;
  *) printf '%s\n' "$line" ;;
esac
PS

cat > "$BIN/lsof" <<'LSOF'
#!/bin/sh
pid=
while [ $# -gt 0 ]; do
  case $1 in
    -p) pid=$2; shift 2 ;;
    *) shift ;;
  esac
done
line=$(awk -v p="$pid" '$1==p' "$PS_TABLE")
[ -n "$line" ] || exit 1
printf 'p%s\nn%s\n' "$pid" "$(printf '%s\n' "$line" | awk '{print $3}')"
LSOF

cat > "$BIN/codesign" <<'CODESIGN'
#!/bin/sh
last=
for last do :; done
case $last in
  *nocx.app*)
    printf 'Identifier=nocx\nTeamIdentifier=TEAM123\nCDHash=abc123\n'
    ;;
  *nocx-helper*)
    printf 'Identifier=nocx-helper\nTeamIdentifier=TEAM123\nCDHash=0ddf00d\n'
    ;;
  *zsh*)
    printf 'Identifier=com.apple.zsh\nTeamIdentifier=APPLE\nCDHash=def456\n'
    ;;
  *)
    printf 'Identifier=unknown\nTeamIdentifier=\nCDHash=fedcba\n'
    ;;
esac
CODESIGN

cat > "$BIN/launchctl" <<'LAUNCHCTL'
#!/bin/sh
if [ "${STUB_LAUNCHCTL_FAIL:-}" = 1 ]; then
  printf 'Could not find service\n' >&2
  exit 1
fi
printf 'program = %s\n' "${STUB_HELPER:-/Users/tester/.nocx/helper/deadbeef/nocx-helper}"
printf 'responsible pid = 4242\n'
printf 'pid = 1300\n'
LAUNCHCTL

cat > "$BIN/log" <<'LOG'
#!/bin/sh
cat "$STUB_LOG"
LOG

cat > "$BIN/defaults" <<'DEFAULTS'
#!/bin/sh
printf '0.8.0\n'
DEFAULTS

# The probe stub answers by target: the LAN cases fail with the reported errno,
# everything else connects. That keeps the test off any network.
cat > "$BIN/perl" <<'PERL'
#!/bin/sh
case "$*" in
  *192.0.2.10*|*2001:db8::10*) printf 'failed: No route to host (errno 65)\n' ;;
  *) printf 'connected\n' ;;
esac
PERL

cat > "$BIN/nc" <<'NC'
#!/bin/sh
printf 'nc: not used when perl answers\n' >&2
exit 1
NC

chmod +x "$BIN"/*

export PS_TABLE="$TMP/ps-table"
export STUB_LOG="$TMP/log"
export PATH="$BIN:$PATH"

run() {
    : > "$OUT"
    : > "$ERR"
    sh "$SCRIPT" "$@" > "$OUT" 2> "$ERR"
    rc=$?
}

printf '=== argument handling ===\n'
run
check "no arguments exits 2" "$rc" "2"
check "usage names LAN_HOST" "$(grep -c 'Usage:.*LAN_HOST' "$ERR" || true)" "1"

run -x
check "an unknown option exits 2" "$rc" "2"

run ""
check "an empty LAN target exits 2" "$rc" "2"
check "an empty LAN target is named" "$(grep -c 'invalid LAN target' "$ERR" || true)" "1"

run 192.0.2.10:notaport
check "a non-numeric port exits 2" "$rc" "2"

run 192.0.2.10:443 1.1.1.1:70000
check "an out-of-range port exits 2" "$rc" "2"

printf '=== a host that is not macOS ===\n'
STUB_UNAME=Linux run 192.0.2.10
check "a non-Darwin host exits 1" "$rc" "1"
check_present "a non-Darwin host is told why" 'run it on macOS' "$OUT"

printf '=== evidence, the old-tab failure ===\n'
run 192.0.2.10:445
check "evidence exits 0" "$rc" "0"
check "the invoking shell pid is printed" \
    "$(grep -c "invoking shell: pid $TEST_PID" "$OUT" || true)" "1"
check_present "the shell executable is printed" '^shell executable: /bin/zsh' "$OUT"
check_present "the helper generation is in the chain" "$HELPER" "$OUT"
check_present "the chain reaches launchd" 'pid 1: launchd' "$OUT"
check "the responsible pid is printed" "$(grep -c 'responsible pid: 4242' "$OUT" || true)" "1"
check "an exited responsible process is marked" \
    "$(grep -c 'responsible pid 4242 is NOT running' "$OUT" || true)" "1"
check_present "the helper's on-disk identity is printed" 'CDHash=0ddf00d' "$OUT"
check_present "the Team ID is printed" 'TeamIdentifier=TEAM123' "$OUT"
check_present "the replaced app path is shown with its on-disk version" \
    'CFBundleShortVersionString=0.8.0' "$OUT"
check "launchd is not searched in the log" \
    "$(grep -c 'pids searched:.* 1 ' "$OUT" || true)" "0"
check "the LAN target and port are printed" \
    "$(grep -c 'TCP connect (lan): 192.0.2.10:445' "$OUT" || true)" "1"
check "the LAN errno is printed" \
    "$(grep -c 'failed: No route to host (errno 65)' "$OUT" || true)" "1"
check "the public target is printed" \
    "$(grep -c 'TCP connect (public): 1.1.1.1:443' "$OUT" || true)" "1"
check "the public probe is printed" "$(grep -c '^  connected$' "$OUT" || true)" "1"
check_present "the log keeps the shell's line" "tccd.*client $TEST_PID" "$OUT"
check_present "the log keeps the responsible pid's line" 'tccd.*responsible 4242' "$OUT"
check_absent "the log drops an unrelated pid" "999999" "$OUT"

printf '=== a running responsible process ===\n'
PS_TABLE="$TMP/ps-table-running" run 192.0.2.10:445
check "a running responsible process is reported" \
    "$(grep -c 'responsible pid 4242 is running' "$OUT" || true)" "1"
check_present "its app executable is named" "$APPEXE" "$OUT"
check_present "its bundle version is read" 'CFBundleShortVersionString=0.8.0' "$OUT"

printf '=== default port and IPv6 form ===\n'
run 192.0.2.10
check "a bare host uses port 443" \
    "$(grep -c 'TCP connect (lan): 192.0.2.10:443' "$OUT" || true)" "1"

run '[2001:db8::10]:22'
check "a bracketed IPv6 host keeps its port" \
    "$(grep -c 'TCP connect (lan): \[2001:db8::10\]:22' "$OUT" || true)" "1"
check "the IPv6 probe keeps the reported errno" \
    "$(grep -c 'failed: No route to host (errno 65)' "$OUT" || true)" "1"

printf '=== launchctl failure and missing tools ===\n'
STUB_LAUNCHCTL_FAIL=1 run 192.0.2.10:445
check_present "a launchctl failure is printed" 'launchctl procinfo: failed' "$OUT"
check "a launchctl failure still exits 0" "$rc" "0"

BARE=$TMP/bare
mkdir -p "$BARE"
for tool in awk sed grep tr sort head tail date; do
    ln -s "$(command -v "$tool")" "$BARE/$tool"
done
cp "$BIN/uname" "$BARE/uname"
cp "$BIN/ps" "$BARE/ps"
PATH="$BARE" PS_TABLE="$TMP/ps-table" STUB_LOG="$TMP/log" \
    /bin/sh "$SCRIPT" 192.0.2.10:445 > "$OUT" 2> "$ERR"
rc=$?
check "missing tools still exit 0" "$rc" "0"
check_present "a missing codesign is said" 'codesign: unavailable' "$OUT"
check_present "a missing launchctl is said" 'launchctl: unavailable' "$OUT"
check_present "a missing log is said" 'log: unavailable' "$OUT"
check_present "a missing perl and nc are said" 'neither perl nor nc found' "$OUT"

printf '\n%d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
