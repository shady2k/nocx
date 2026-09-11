#!/usr/bin/env bash
# Record one Claude Code moment for internal/agentdriver's corpus, isolated.
# usage: record.sh <endpoint> <model> <script> <out.jsonl> [cols] [settings.json]
set -euo pipefail
endpoint=$1 model=$2 script=$3 out=$4 cols=${5:-120} settings=${6:-}
repo=$(git rev-parse --show-toplevel)
run=$(mktemp -d /var/tmp/nocx-detect-XXXXXX)
mkdir -p "$run/home" "$run/claude-config" "$run/work"
claude_path=$(command -v claude) || { echo "record.sh: claude not found on PATH" >&2; exit 1; }
claude_dir=$(dirname "$claude_path")
cat > "$run/run.env" <<EOF
HOME=$run/home
CLAUDE_CONFIG_DIR=$run/claude-config
PATH=$claude_dir:/run/current-system/sw/bin:/usr/bin:/bin
ANTHROPIC_BASE_URL=$endpoint
ANTHROPIC_AUTH_TOKEN=local-model-no-credential
ANTHROPIC_MODEL=$model
ANTHROPIC_SMALL_FAST_MODEL=$model
DISABLE_TELEMETRY=1
EOF
args=()
if [[ -n $settings ]]; then cp "$settings" "$run/settings.json"; args=(--settings "$run/settings.json"); fi
go build -o "$run/agent-capture" "$repo/cmd/agent-capture"
"$run/agent-capture" capture -env-file "$run/run.env" -dir "$run/work" -meta "$run/meta.json" \
  -out "$out" -script "$script" -cols "$cols" -rows 40 -timeout 240s -version-arg --version \
  -- claude "${args[@]+"${args[@]}"}"
echo "run directory: $run (meta.json holds what ran, the launcher's own env additions and the Claude version)"
