#!/bin/sh
# Diagnose local-network access from a tab that survived an in-app update.
#
# macOS Local Network Privacy (LNP) decides LAN access from the CODE IDENTITY
# of a process's RESPONSIBLE process, not from the process itself. An update
# replaces nocx.app in place (internal/update/platform_darwin.go's RENAME_SWAP),
# so a shell in a pre-update tab can end up attributed to a code identity that
# no longer exists on disk while a new tab -- whose chain is rooted in the new
# app -- still reaches the LAN.
#
# Run this INSIDE the tab that lost LAN access, right after the update, then run
# the same command in a tab opened after the update and compare the two blocks.
# It prints the invoking shell, its parent chain up to launchd with each
# executable's path, existence and code signature, the RESPONSIBLE pid macOS
# holds for that shell and that process's identity, a TCP connect to the LAN
# host you pass and to a public host, and the last minute of the LNP logs
# filtered to those pids. It prints no environment, no credentials, and no
# hostname other than the ones you pass.
#
# Usage: macos-lan-after-update.sh LAN_HOST[:PORT] [PUBLIC_HOST[:PORT]]
#   LAN_HOST    a host on the same network the old tab fails to reach.
#               A bare host uses port 443; a refused connection still proves
#               the route and LNP allowed the connect.
#   PUBLIC_HOST defaults to 1.1.1.1:443.
#
# Exit status: 0 = evidence printed (a failed connect is evidence, not an
# error), 2 = usage error, 1 = not macOS or the invoking shell is unknown.
set -u

PROG=${0##*/}
PUBLIC_DEFAULT=1.1.1.1:443

usage() {
    printf 'Usage: %s LAN_HOST[:PORT] [PUBLIC_HOST[:PORT]]\n' "$PROG" >&2
    printf '  Run inside the tab that lost local-network access, then in a new tab.\n' >&2
    printf '  Bare hosts use port 443; PUBLIC_HOST defaults to %s.\n' "$PUBLIC_DEFAULT" >&2
}

have() { command -v "$1" >/dev/null 2>&1; }
say() { printf '%s\n' "$*"; }
section() { printf '\n--- %s ---\n' "$*"; }

# split_target sets split_host/split_port, or returns 1 on a bad target.
# Accepts host, host:port, and [ipv6]:port; brackets are how a colon-bearing
# host is told apart from the port.
split_target() {
    target=$1
    case $target in
        \[*\]:*)
            split_host=${target%%]*}
            split_host=${split_host#\[}
            split_port=${target##*]:}
            ;;
        \[*)
            return 1
            ;;
        *:*)
            split_host=${target%:*}
            split_port=${target##*:}
            ;;
        *)
            split_host=$target
            split_port=443
            ;;
    esac
    [ -n "$split_host" ] || return 1
    case $split_host in
        *[[:space:]]*) return 1 ;;
    esac
    case $split_port in
        '' | *[!0-9]*) return 1 ;;
    esac
    [ "$split_port" -ge 1 ] && [ "$split_port" -le 65535 ] || return 1
    return 0
}

# format_target keeps an IPv6 host readable in the output.
format_target() {
    case $1 in
        *:*) printf '[%s]:%s' "$1" "$2" ;;
        *) printf '%s:%s' "$1" "$2" ;;
    esac
}

# executable_of answers a pid's executable path. lsof names the text file from
# the kernel; ps comm is the fallback (macOS prints the full path there).
executable_of() {
    pid=$1
    exe=
    if have lsof; then
        exe=$(lsof -a -p "$pid" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1)
    fi
    if [ -z "$exe" ]; then
        exe=$(ps -p "$pid" -o comm= 2>/dev/null | tr -d '\n')
    fi
    printf '%s' "$exe"
}

# describe_executable prints what LNP would decide about this file: does it
# still exist, and which code identity is on disk at that path now.
describe_executable() {
    label=$1
    path=$2
    if [ -z "$path" ]; then
        say "$label: (executable path unavailable)"
        return
    fi
    if [ ! -e "$path" ]; then
        say "$label: $path (MISSING on disk)"
        return
    fi
    say "$label: $path (exists on disk)"
    if have codesign; then
        sig=$(codesign -dv --verbose=4 "$path" 2>&1)
        if printf '%s\n' "$sig" | grep -q '^CDHash='; then
            printf '%s\n' "$sig" |
                grep -E '^(Identifier|TeamIdentifier|CDHash|Authority|Format|Signature)=' |
                sed 's/^/  /'
        else
            say "  codesign: no identity reported (unsigned or unreadable): $(printf '%s' "$sig" | head -n 1)"
        fi
    else
        say "  codesign: unavailable"
    fi
    case $path in
        *.app/Contents/MacOS/*)
            bundle=${path%%.app/Contents/MacOS/*}.app
            version=
            if have defaults; then
                version=$(defaults read "$bundle/Contents/Info.plist" CFBundleShortVersionString 2>/dev/null)
            fi
            if [ -n "$version" ]; then
                say "  bundle: $bundle (on-disk CFBundleShortVersionString=$version)"
            else
                say "  bundle: $bundle"
            fi
            ;;
    esac
}

# probe answers the connect and, when perl is present, the numeric errno.
probe() {
    label=$1
    host=$2
    port=$3
    section "TCP connect ($label): $(format_target "$host" "$port")"
    if have perl; then
        # shellcheck disable=SC2016 # $! and $0+$! are perl's, not the shell's.
        perl -MIO::Socket::INET -e '
            use strict;
            use warnings;
            my ($host, $port, $timeout) = @ARGV;
            my $sock = IO::Socket::INET->new(
                PeerAddr => $host, PeerPort => $port, Proto => "tcp", Timeout => $timeout);
            if ($sock) {
                print "connected\n";
                close $sock;
            } else {
                printf "failed: %s (errno %d)\n", $!, 0 + $!;
            }
        ' "$host" "$port" 5 2>&1 | sed 's/^/  /'
    elif have nc; then
        out=$(nc -z -G 5 -w 5 "$host" "$port" 2>&1)
        rc=$?
        if [ "$rc" -eq 0 ]; then
            say "  connected"
        else
            say "  nc failed (exit $rc): $out"
        fi
    else
        say "  unavailable: neither perl nor nc found"
    fi
    case $label in
        lan)
            say '  read: connected or "Connection refused" = the route and LNP allowed it;'
            say '        "No route to host" (errno 65) = the reported failure.'
            ;;
    esac
}

if [ "$(uname -s 2>/dev/null)" != Darwin ]; then
    say "this script reads macOS process and code-signing state; run it on macOS."
    say "uname -s here: $(uname -s 2>/dev/null)"
    exit 1
fi

[ "$#" -ge 1 ] || { usage; exit 2; }
case $1 in
    -*) usage; exit 2 ;;
esac
[ "$#" -le 2 ] || { usage; exit 2; }
lan_target=$1
public_target=${2:-$PUBLIC_DEFAULT}
if ! split_target "$lan_target"; then
    say "invalid LAN target: $lan_target" >&2
    usage
    exit 2
fi
lan_host=$split_host
lan_port=$split_port
if ! split_target "$public_target"; then
    say "invalid public target: $public_target" >&2
    usage
    exit 2
fi
public_host=$split_host
public_port=$split_port

shell_pid=${PPID:-}
case $shell_pid in
    '' | *[!0-9]*)
        say "cannot identify the invoking shell (PPID=$shell_pid); run this from the tab's prompt, not from a daemon."
        exit 1
        ;;
esac

section "context"
say "when: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
say "kernel: $(uname -s) $(uname -r) $(uname -m)"
say "this script: pid $$; invoking shell: pid $shell_pid"
say "LAN target: $(format_target "$lan_host" "$lan_port"); public target: $(format_target "$public_host" "$public_port")"
say ""
say "Run this in the OLD tab and again in a NEW tab, and keep both blocks."

section "invoking shell"
describe_executable "shell executable" "$(executable_of "$shell_pid")"

section "parent chain (shell pid $shell_pid up to launchd)"
chain_pids="$shell_pid"
pid=$shell_pid
depth=0
while [ "$pid" -gt 1 ] && [ "$depth" -lt 32 ]; do
    if ! ps -p "$pid" >/dev/null 2>&1; then
        say "pid $pid: not running"
        break
    fi
    ppid=$(ps -p "$pid" -o ppid= 2>/dev/null | tr -d ' \n')
    exe=$(executable_of "$pid")
    say "pid $pid (ppid ${ppid:-?})"
    describe_executable "  executable" "$exe"
    case $ppid in
        '' | *[!0-9]*) break ;;
    esac
    [ "$ppid" = "$pid" ] && break
    case " $chain_pids " in
        *" $ppid "*) break ;;
    esac
    chain_pids="$chain_pids $ppid"
    pid=$ppid
    depth=$((depth + 1))
done
[ "$pid" -eq 1 ] && say "pid 1: launchd"

section "responsible process macOS holds for the shell"
resp_pid=
if have launchctl; then
    info=$(launchctl procinfo "$shell_pid" 2>&1)
    rc=$?
    if [ "$rc" -ne 0 ]; then
        say "launchctl procinfo: failed (exit $rc): $info"
    else
        say "launchctl procinfo, lines naming responsibility:"
        printf '%s\n' "$info" | grep -i 'responsib' | sed 's/^/  /'
        resp_pid=$(printf '%s\n' "$info" | awk '
            /[Rr]esponsible/ {
                if (match($0, /[0-9]+/)) { print substr($0, RSTART, RLENGTH); exit }
                want = 1
                next
            }
            want && /[Pp][Ii][Dd]/ && match($0, /[0-9]+/) {
                print substr($0, RSTART, RLENGTH)
                exit
            }
            want { if (++after > 3) want = 0 }
        ')
        say "responsible pid: ${resp_pid:-could not parse; read the lines above}"
    fi
else
    say "launchctl: unavailable"
fi
if [ -n "$resp_pid" ]; then
    if ps -p "$resp_pid" >/dev/null 2>&1; then
        say "responsible pid $resp_pid is running"
        describe_executable "  executable" "$(executable_of "$resp_pid")"
    else
        say "responsible pid $resp_pid is NOT running (exited before this reading)"
        say "  its executable path and code identity are only the on-disk ones shown in the chain."
    fi
fi

section "nocx processes now (app, coordinator, helpers)"
if have ps; then
    # shellcheck disable=SC2009 # paths, not names: which generation's binary is the point.
    ps -axo pid=,ppid=,comm= 2>/dev/null |
        grep -E 'nocx|AppRun' |
        sed 's/^ */  /'
    app_exe=$(ps -axo comm= 2>/dev/null |
        sed -n 's/^[[:space:]]*//; s/[[:space:]]*$//; /\/Contents\/MacOS\/nocx$/p' |
        head -n 1)
    if [ -n "$app_exe" ]; then
        say ""
        say "on-disk nocx.app now (the path an in-app update replaces in place):"
        describe_executable "  app executable" "$app_exe"
    fi
else
    say "  ps: unavailable"
fi

probe lan "$lan_host" "$lan_port"
probe public "$public_host" "$public_port"

section "log show --last 1m (networkextension, nehelper, tccd), filtered to these pids"
if have log; then
    pid_re=$(printf '%s\n' "$$ $chain_pids ${resp_pid:-}" |
        tr ' ' '\n' | grep -E '^[0-9]+$' | grep -vx '1' |
        sort -u | tr '\n' '|' | sed 's/|$//')
    if [ -n "$pid_re" ]; then
        predicate='subsystem == "com.apple.networkextension" OR process == "nehelper" OR process == "tccd"'
        log show --last 1m --info --debug --style syslog --predicate "$predicate" 2>&1 |
            grep -E "(^|[^0-9])($pid_re)([^0-9]|$)" |
            tail -n 200 |
            sed 's/^/  /'
        say "  (pids searched: $(printf '%s' "$pid_re" | tr '|' ' '))"
    else
        say "  no numeric pids to search"
    fi
else
    say "  log: unavailable"
fi

exit 0
