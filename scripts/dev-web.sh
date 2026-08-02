#!/usr/bin/env bash
#
# The browser dev stand: the real backend plus the real frontend, without Wails,
# so the UI can be looked at in an ordinary browser over an SSH tunnel.
#
# Two processes, because the app is two halves:
#
#   devharness  — internal/app with the WS transport and a real PTY, no GUI
#   vite        — the frontend, with the two Wails bindings shimmed
#
# The bindings are the whole reason a plain `npm run dev` is not enough. The
# frontend asks Go for the WS port and the capability token; in a browser the
# call throws, the token falls back to "" and the socket is refused by the auth
# gate. This script reads both values off the backend's stdout and hands them
# to vite, so the pair is never copied by hand.
#
# Ports (override by env): NOCX_WS_PORT=9880, NOCX_WEB_PORT=5180.
#
# Both halves are deliberately off every other consumer's port, because a dev
# stand that quietly shares a socket with the test suite is worse than one that
# refuses to start:
#
#   5173       `npm run dev` AND the headless e2e vite (playwright BASE_URL)
#   9876       the WS port the headless e2e runbook pins for devharness
#   34115      the wails dev asset server
#   32768+     the ephemeral range wails dev's own backend (127.0.0.1:0) lands in
#
# 5180 and 9880 are in none of those, the last one by arithmetic rather than by
# luck: both sit below the ephemeral floor, so a `wails dev` running alongside
# cannot draw one of them at random.
set -euo pipefail

# Deliberately NOT 9876: that is the port the headless e2e path pins for its own
# devharness, so sharing it means a test run and a look-and-see session evict
# each other on the backend socket.
WS_PORT="${NOCX_WS_PORT:-9880}"
# Deliberately NOT 5173: the headless e2e path serves the frontend there
# (playwright.config.ts BASE_URL), so sharing the port means a test run and a
# look-and-see session evict each other, or worse, one silently attaches to the
# other's server and reports on a tree it never built.
WEB_PORT="${NOCX_WEB_PORT:-5180}"
# Loopback by design, not caution: the WS auth rejects any Host that is not
# loopback, so a page served on a LAN address gets a UI that cannot connect.
# Reach it by forwarding both ports to localhost.
WEB_HOST="${NOCX_WEB_HOST:-127.0.0.1}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if [ ! -d frontend/node_modules ]; then
	echo "frontend/node_modules not found — run 'make init' first" >&2
	exit 1
fi

# pty_local.go spawns the user's login shell from the backend's own $SHELL. A
# /bin/sh there yields a POSIX sh that never sources ~/.bashrc, so the shell
# integration and its OSC 133 blocks silently never engage — the terminal looks
# subtly wrong for a reason nothing reports.
case "$(basename "${SHELL:-sh}")" in
sh | dash)
	if bash_path="$(command -v bash)"; then
		export SHELL="$bash_path"
	fi
	;;
esac

# Refuse to start on a port somebody already holds, and say who holds it.
#
# This is not belt-and-braces around vite's strictPort: strictPort compares
# bind addresses, so a server on [::1]:5173 and one on 127.0.0.1:5173 both
# succeed and neither notices the other. Since `localhost` resolves to ::1
# first on this box, the browser then reaches whichever of the two it was not
# meant to — a page served without the Wails shim looks identical until the
# WebSocket is refused for an empty token. Measured, not theorised: that is
# exactly what a leftover e2e vite did on 2026-07-27.
check_port_free() {
	local port="$1" what="$2" holders
	holders="$(ss -ltnp "sport = :$port" 2>/dev/null | tail -n +2)"
	if [ -n "$holders" ]; then
		echo "port $port ($what) is already in use:" >&2
		echo "$holders" >&2
		echo "" >&2
		echo "stop it, or pick another: NOCX_WS_PORT=... NOCX_WEB_PORT=... make dev-web" >&2
		exit 1
	fi
}
check_port_free "$WS_PORT" "backend WS"
check_port_free "$WEB_PORT" "frontend"

work="$(mktemp -d)"
backend_pid=""
cleanup() {
	if [ -n "$backend_pid" ]; then
		kill "$backend_pid" 2>/dev/null || true
		wait "$backend_pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

# Built rather than `go run`: go run wraps the binary in a child process that
# survives a kill of the parent, and an orphaned backend holds the WS port
# against the next run.
echo "=== building devharness ==="
go build -o "$work/devharness" ./cmd/devharness

echo "=== backend on 127.0.0.1:$WS_PORT ==="
backend_dbus_address="${DBUS_SESSION_BUS_ADDRESS:-}"
session_bus="/run/user/$(id -u)/bus"
if [[ ( -z "$backend_dbus_address" || "$backend_dbus_address" == "disabled:" ) && -S "$session_bus" ]]; then
	backend_dbus_address="unix:path=$session_bus"
fi
DBUS_SESSION_BUS_ADDRESS="$backend_dbus_address" \
	NOCX_WS_ADDR="127.0.0.1:$WS_PORT" \
	"$work/devharness" >"$work/backend.log" 2>&1 &
backend_pid=$!

# The backend prints WSPORT then WSTOKEN once the listener is up; WSTOKEN last,
# so waiting on it means both are readable.
for _ in $(seq 1 100); do
	if grep -q '^WSTOKEN=' "$work/backend.log" 2>/dev/null; then
		break
	fi
	if ! kill -0 "$backend_pid" 2>/dev/null; then
		echo "backend exited before it was ready:" >&2
		cat "$work/backend.log" >&2
		exit 1
	fi
	sleep 0.1
done

token="$(sed -n 's/^WSTOKEN=//p' "$work/backend.log" | head -1)"
port="$(sed -n 's/^WSPORT=//p' "$work/backend.log" | head -1)"
if [ -z "$token" ] || [ -z "$port" ]; then
	echo "backend never reported WSPORT/WSTOKEN:" >&2
	cat "$work/backend.log" >&2
	exit 1
fi

cat <<EOF

Backend up. From your machine:

  ssh -L $WEB_PORT:127.0.0.1:$WEB_PORT -L $port:127.0.0.1:$port <this-host>

then open http://localhost:$WEB_PORT

The remote side of both forwards is spelled 127.0.0.1, not localhost: both
servers bind IPv4 loopback, and localhost resolves to ::1 first here — a
forward aimed at localhost would land on nothing (or on someone else's
server). Set NOCX_WEB_HOST to change what vite binds.

Both ports must reach your machine's localhost — the WS auth only accepts a
loopback origin. Ctrl-C stops the backend and vite together.

EOF

cd frontend
NOCX_WS_PORT="$port" NOCX_WS_TOKEN="$token" NOCX_WEB_PORT="$WEB_PORT" \
	npx vite --config vite.dev-view.config.ts --host "$WEB_HOST"
