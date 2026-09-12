#!/usr/bin/env bash
#
# Records the geometry corpus: seven real programs on a real PTY, bytes only,
# through the product's own cmd/agent-capture. README section "§7.1" of
# REPORT.md describes what each capture is for; this script is how somebody
# records it again.
#
#   cd .internal/spikes/emulator && ./corpus/record.sh
#
# Three things about it are load-bearing and are the reason it is a script
# rather than seven lines in the report:
#
#   - Nothing is typed that makes a program exit. htop, vim and less restore
#     the normal screen (?1049l) on quit, which would record a blank screen
#     instead of the alternate one the corpus is here to measure. The capture's
#     script ends instead, and agent-capture kills the program with the
#     alternate screen still up.
#   - LESS is unset for the two less runs. The shell that starts this script
#     has LESS=FRX in its environment, and -X makes less skip smcup/rmcup, so
#     an inherited LESS turns the paging capture into a plain print.
#   - The Claude run is isolated: a fresh HOME and CLAUDE_CONFIG_DIR under
#     /var/tmp, an endpoint that answers nothing, and -env-file, which makes
#     agent-capture refuse to start at all if Claude Code would read
#     configuration outside that directory. Nothing of the owner's Claude
#     account is touched. The model picker warning about an unknown model
#     ("local") is part of what the capture shows.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
tmp=$(mktemp -d)
# Scratch this script owns: the go build directory, and later the Claude run tree
# under /var/tmp. One trap so that a failure anywhere still removes both — and
# `rm -rf ""` is a silent no-op for whichever has not been created yet.
run=""
sink=""
cleanup() {
  if [ -n "$sink" ]; then kill "$sink" 2>/dev/null || true; fi
  rm -rf "$tmp" "$run"
}
trap cleanup EXIT

go build -C "$repo" -o "$tmp/agent-capture" ./cmd/agent-capture

cap() {
  local name=$1
  shift
  echo "=== $name"
  "$tmp/agent-capture" capture -out "$here/$name.jsonl" -dir "$repo" \
    -cols 120 -rows 40 "$@"
}

# A delay-only script: the program draws and the capture ends. See the header.
idle=$here/scripts/idle.script

cap htop -script "$idle" -timeout 20s -- htop
# vim runs with -n, which is the difference between a recording that can be repeated
# and one that cannot: without it vim writes a swap file beside the file it opens,
# this capture ends by killing vim so the swap can survive, and the next recording of
# this row then finds it and draws vim's "swap file already exists" dialog instead of
# the file. The swap would also be a file outside this spike's directory. -n removes
# the swap, not any of the screen this row exists to measure.
cap vim -script "$idle" -timeout 20s -- vim -n internal/transport/ws.go
cap less -script "$idle" -timeout 20s -- env -u LESS less internal/transport/ws.go
cap wide -script "$idle" -timeout 20s -- env -u LESS less -R +26 e2e/ime.spec.ts
cap emoji -script "$idle" -timeout 20s -- bash "$here/scripts/emoji.sh"
cap bash -script "$here/scripts/bash.script" -timeout 30s -- bash -i

# The Claude first run. Isolated HOME and CLAUDE_CONFIG_DIR, and an endpoint
# that accepts and never answers, so the turn stays in flight and the spinner
# is what the last frame holds.
#
# COLORTERM=truecolor is set deliberately, because the env file replaces the
# environment wholesale and this row advertises the truecolor SGR path. The
# recorder pins TERM to xterm-256color, whose terminfo advertises no
# direct-colour capability: without this variable in the file the first
# recording of the row carried 256-colour SGR and not one 24-bit sequence, and
# with it the run emits 24-bit SGR and no 256-colour one. The counts belong to
# a recording, not to this comment — they are in REPORT.md's byte-shape table,
# measured from whichever corpus is committed.
run=$(mktemp -d /var/tmp/nocx-geom-XXXXXX)
mkdir -p "$run/home" "$run/claude-config" "$run/work"
cat > "$run/run.env" <<EOF
HOME=$run/home
CLAUDE_CONFIG_DIR=$run/claude-config
PATH=/run/current-system/sw/bin:/usr/bin:/bin
ANTHROPIC_BASE_URL=http://127.0.0.1:18999
ANTHROPIC_AUTH_TOKEN=local-model-no-credential
ANTHROPIC_MODEL=local
ANTHROPIC_SMALL_FAST_MODEL=local
DISABLE_TELEMETRY=1
COLORTERM=truecolor
EOF

python3 - "$run/stall.log" <<'EOF' &
import socket, sys
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 18999))
s.listen(64)
conns = []
sys.stderr.write("stalling endpoint up\n")
sys.stderr.flush()
while True:
    conns.append(s.accept())
EOF
sink=$!
sleep 0.5

echo "=== wizard"
"$tmp/agent-capture" capture -env-file "$run/run.env" -dir "$run/work" \
  -meta "$here/wizard.meta.json" -version-arg --version \
  -out "$here/wizard.jsonl" -script "$here/scripts/wizard.script" \
  -cols 120 -rows 40 -timeout 60s -- claude

kill "$sink" 2>/dev/null || true
sink=""

# Validate what was recorded: the offset chain is what a replay depends on, and
# the two rows the brief names are checked for the alternate screen they exist
# to exercise.
for f in "$here"/*.jsonl; do
  echo "--- $(basename "$f")"
  "$tmp/agent-capture" replay -at 0 "$f" > /dev/null
  printf 'chunks=%s 1049h=%s\n' \
    "$(grep -c '"atMs"' "$f")" \
    "$(grep -c '1049h' "$f")"
done
