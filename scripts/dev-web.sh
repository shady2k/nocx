#!/usr/bin/env bash
#
# The browser dev stand: the real backend plus the real frontend, without Wails,
# so the UI can be looked at in an ordinary browser over an SSH tunnel.
#
# Two processes, because the app is two halves:
#
#   nocx-server — the coordinator: internal/app with the WS transport and a
#                 real PTY, no GUI. The binary the desktop app spawns, so this
#                 stand and production are one thing rather than two similar
#                 ones (design D11; cmd/devharness is gone).
#   vite        — the frontend, with the two Wails bindings shimmed
#
# The bindings are the whole reason a plain `npm run dev` is not enough. The
# frontend asks Go for the WS port and the capability token; in a browser the
# call throws, the token falls back to "" and the socket is refused by the auth
# gate. This script learns both from the coordinator's discovery socket and
# hands them to vite, so the pair is never copied by hand.
#
# THE BACKEND PORT IS NO LONGER YOURS TO CHOOSE. nocx-server binds loopback on
# a port the OS picks and takes no flags at all — a token must never reach
# argv, and a daemon that took its address from the environment could be told
# to bind off loopback (design §6). So the port changes on every restart and
# the SSH forward has to be re-made; the command to copy is printed below with
# the real number in it. NOCX_WEB_PORT=5180 still pins vite.
#
# Vite's port is deliberately off every other consumer's, because a dev stand
# that quietly shares a socket with the test suite is worse than one that
# refuses to start:
#
#   5173       `npm run dev` AND the e2e vite (playwright BASE_URL)
#   34115      the wails dev asset server
#
# ONE COORDINATOR PER PROFILE. This stand starts its own nocx-server in the
# development profile's runtime directory, and so does `wails dev` — a second
# one there exits 3 rather than serving beside the first. Run one at a time.
set -euo pipefail
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
		echo "stop it, or pick another: NOCX_WEB_PORT=... make dev-web" >&2
		exit 1
	fi
}
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
# `-tags nocx_login_session` because this stand IS in one: somebody is at the
# machine, there is a login keychain, and there is a person who can answer a
# dialog about it. That is exactly the declaration design D10 asks for, and
# without it the dev stand's vault would take the file provider and ask for a
# passphrase where the developer's OS key would have carried it.
echo "=== building nocx-server ==="
go build -tags nocx_login_session -o "$work/nocx-server" ./cmd/nocx-server

echo "=== backend on loopback, port chosen by the OS ==="
backend_dbus_address="${DBUS_SESSION_BUS_ADDRESS:-}"
session_bus="/run/user/$(id -u)/bus"
if [[ ( -z "$backend_dbus_address" || "$backend_dbus_address" == "disabled:" ) && -S "$session_bus" ]]; then
	backend_dbus_address="unix:path=$session_bus"
fi
# THE RAW EXCHANGE WITH THE MODEL, on by default HERE and nowhere else.
# This stand exists to be looked at, and three live failures in a row were
# each diagnosed by guessing because nothing recorded what was sent and what
# came back. The file holds the person's question and whatever their tools
# read, which is why it is off in the shipped app and why it lives in this
# run's throwaway directory rather than anywhere that outlives it. The API key
# is never written to it (internal/assistant/wiretap.go). Point NOCX_WIRE_LOG
# somewhere else to keep one, or set it empty to turn the tap off.
DBUS_SESSION_BUS_ADDRESS="$backend_dbus_address" \
	NOCX_WIRE_LOG="${NOCX_WIRE_LOG-$work/wire.log}" \
	"$work/nocx-server" >"$work/backend.log" 2>&1 &
backend_pid=$!

# The server names its discovery socket on its readiness line; the port and the
# token come from the socket and from nowhere else, because a token on stdout
# is what design §6 forbids. e2e/coordinator.mts is the one implementation of
# that exchange — Node 24 runs it straight, with no build step.
socket=""
for _ in $(seq 1 200); do
	socket="$(sed -n 's/.*[[:space:]]socket=\([^[:space:]]*\).*/\1/p' "$work/backend.log" | tail -1 | tr -d '"')"
	[ -n "$socket" ] && break
	if ! kill -0 "$backend_pid" 2>/dev/null; then
		echo "backend exited before it was ready:" >&2
		cat "$work/backend.log" >&2
		exit 1
	fi
	sleep 0.1
done
if [ -z "$socket" ]; then
	echo "backend never named its discovery socket:" >&2
	cat "$work/backend.log" >&2
	exit 1
fi

if ! hello="$(node "$repo_root/e2e/coordinator.mts" "$socket")"; then
	echo "the coordinator on $socket refused the handshake:" >&2
	cat "$work/backend.log" >&2
	exit 1
fi
token="$(printf '%s\n' "$hello" | sed -n 's/^WSTOKEN=//p' | head -1)"
port="$(printf '%s\n' "$hello" | sed -n 's/^WSPORT=//p' | head -1)"
if [ -z "$token" ] || [ -z "$port" ]; then
	echo "the coordinator's hello carried no port or no token:" >&2
	printf '%s\n' "$hello" >&2
	exit 1
fi

cat <<EOF

Backend up. From your machine:

  ssh -L $WEB_PORT:127.0.0.1:$WEB_PORT -L $port:127.0.0.1:$port <this-host>

then open http://localhost:$WEB_PORT

What was sent to the model and what it said back, verbatim:

  ${NOCX_WIRE_LOG-$work/wire.log}

The backend port changes every restart — the OS picks it — so re-copy the
line above each time. The remote side of both forwards is spelled 127.0.0.1,
not localhost: both
servers bind IPv4 loopback, and localhost resolves to ::1 first here — a
forward aimed at localhost would land on nothing (or on someone else's
server). Set NOCX_WEB_HOST to change what vite binds.

Both ports must reach your machine's localhost — the WS auth only accepts a
loopback origin. Ctrl-C stops the backend and vite together.

EOF

cd frontend
NOCX_WS_PORT="$port" NOCX_WS_TOKEN="$token" NOCX_WEB_PORT="$WEB_PORT" \
	npx vite --config vite.dev-view.config.ts --host "$WEB_HOST"
