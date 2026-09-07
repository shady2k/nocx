package shellintegration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inspectingAgentBody = `#!/bin/sh
echo AGENT-RAN
echo HOME_PATH=$HOME
if [ -e "$TMPDIR/nocx-agent-launch.stale" ]; then echo STALE_PRESENT; else echo STALE_SWEPT; fi
i=0
for arg in "$@"; do
	echo ARG_$i=$arg
	case "$arg" in
		*/mcp.json)
			echo MCP_PATH=$arg
			ls -ld "$(dirname "$arg")"
			ls -l "$(dirname "$arg")"
			echo CONFIG_BEGIN
			cat "$arg"
			echo CONFIG_END
			;;
	esac
	i=$((i + 1))
done
for path in "$HOME/.mcp.json" "$HOME/.claude/settings.json"; do
	if [ -e "$path" ]; then echo USER_FILE_WRITTEN=$path; fi
done
`

type stagedMCPConfig struct {
	MCPServers map[string]struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"mcpServers"`
}

func TestBashAgentStagesMCPConfigAndCleansLaunchDirectory(t *testing.T) {
	agentStagesMCPConfigAndCleansLaunchDirectory(t, startNestedBashParent)
}

func TestZshAgentStagesMCPConfigAndCleansLaunchDirectory(t *testing.T) {
	agentStagesMCPConfigAndCleansLaunchDirectory(t, startNestedZshParent)
}

func agentStagesMCPConfigAndCleansLaunchDirectory(t *testing.T, start nestedParentStarter) {
	t.Helper()
	k := newNestedKernel(t)
	s := start(t, k, "claude", inspectingAgentBody)
	_, err := s.ptmx.Write([]byte("mkdir -p \"$TMPDIR/nocx-agent-launch.stale\"; printf '999999999\\n' > \"$TMPDIR/nocx-agent-launch.stale/lease\"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude --model test user-prompt\n"))
	if err != nil {
		t.Fatalf("type staged claude command: %v", err)
	}

	waitForEvent(t, k, "agent_enrol")
	waitForEvent(t, k, "agent_withdraw")
	waitUntil(t, "the agent output", func() bool { return strings.Contains(s.output(), "AGENT-RAN") })
	out := strings.ReplaceAll(s.output(), "\r", "")
	if strings.Contains(out, "USER_FILE_WRITTEN=") {
		t.Fatalf("staging wrote a user-owned file: output=%q", out)
	}
	if !strings.Contains(out, "STALE_SWEPT") {
		t.Fatalf("the stale launch directory was not swept: output=%q", out)
	}
	if strings.Contains(out, "ARG_0=--strict-mcp-config") {
		t.Fatalf("strict MCP mode was passed: output=%q", out)
	}
	if !strings.Contains(out, "ARG_0=--model") ||
		!strings.Contains(out, "ARG_1=test") ||
		!strings.Contains(out, "ARG_2=user-prompt") {
		t.Fatalf("the user's flag and positional prompt were not preserved: output=%q", out)
	}
	if !strings.Contains(out, "ARG_3=--mcp-config") {
		t.Fatalf("the staged MCP argument was not appended after the user's arguments: output=%q", out)
	}
	if !strings.Contains(out, "drwx------") || !strings.Contains(out, "-rw-------") {
		t.Fatalf("launch directory or files were not private: output=%q", out)
	}

	begin := strings.Index(out, "CONFIG_BEGIN\n")
	end := strings.Index(out, "\nCONFIG_END")
	if begin < 0 || end <= begin {
		t.Fatalf("MCP config was not printed by the launched agent: output=%q", out)
	}
	var config stagedMCPConfig
	configText := strings.TrimSpace(out[begin+len("CONFIG_BEGIN\n") : end])
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("decode staged MCP config: %v; output=%q", err, out)
	}
	for _, forbidden := range []string{"NOCX_AGENT_REPORT", "NOCX_LIFECYCLE", `"env"`} {
		if strings.Contains(configText, forbidden) {
			t.Fatalf("staged MCP config contains forbidden authority/environment %q: %s", forbidden, configText)
		}
	}
	server, ok := config.MCPServers["nocx"]
	if !ok || len(config.MCPServers) != 1 {
		t.Fatalf("MCP servers = %#v, want exactly one server named nocx", config.MCPServers)
	}
	if server.Type != "stdio" || server.Command != "/opt/nocx-helper" {
		t.Fatalf("MCP server = %#v, want stdio /opt/nocx-helper", server)
	}
	wantArgs := []string{"mcp", "--socket", "/tmp/nocx-tool.sock"}
	if len(server.Args) != len(wantArgs) {
		t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
	}
	for i := range wantArgs {
		if server.Args[i] != wantArgs[i] {
			t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
		}
	}

	var mcpPath string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MCP_PATH=") {
			mcpPath = strings.TrimPrefix(line, "MCP_PATH=")
			break
		}
	}
	if mcpPath == "" {
		t.Fatal("launched agent did not report the staged config path")
	}
	waitUntil(t, "the launch directory cleanup", func() bool {
		_, err := os.Stat(filepath.Dir(mcpPath))
		return os.IsNotExist(err)
	})
}

const agentFallbackBody = `#!/bin/sh
echo AGENT-RAN
for arg in "$@"; do
	case "$arg" in
		*/mcp.json) echo UNEXPECTED_MCP_PATH="$arg" ;;
	esac
done
`

func runBashAgentEnrollmentRefusal(t *testing.T, configure func(*nestedKernel)) string {
	t.Helper()
	k := newNestedKernel(t)
	configure(k)
	s := startNestedBashParent(t, k, "claude", agentFallbackBody)
	if _, err := s.ptmx.Write([]byte("export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude --model test prompt\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the refused agent launch", func() bool {
		return strings.Contains(s.output(), "AGENT-RAN")
	})
	out := s.output()
	if strings.Contains(out, "UNEXPECTED_MCP_PATH=") {
		t.Fatalf("refused enrolment staged MCP config: %q", out)
	}
	return out
}

func TestBashAgentMalformedEnrollmentFallsBackWithoutStaging(t *testing.T) {
	out := runBashAgentEnrollmentRefusal(t, func(k *nestedKernel) {
		k.agentMalformed = true
	})
	if !strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("agent did not run after malformed enrolment answer: %q", out)
	}
}

func TestBashAgentTimedOutEnrollmentFallsBackWithoutStaging(t *testing.T) {
	out := runBashAgentEnrollmentRefusal(t, func(k *nestedKernel) {
		k.agentTimeout = true
	})
	if !strings.Contains(out, "AGENT-RAN") {
		t.Fatalf("agent did not run after timed-out enrolment: %q", out)
	}
}

func TestBashAgentKeepsLiveLaunchLease(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "claude", inspectingAgentBody)
	if _, err := s.ptmx.Write([]byte("mkdir -p \"$TMPDIR/nocx-agent-launch.stale\"; printf '%s\\n' \"$$\" > \"$TMPDIR/nocx-agent-launch.stale/lease\"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the live launch lease", func() bool {
		out := s.output()
		return strings.Contains(out, "AGENT-RAN") && strings.Contains(out, "STALE_PRESENT")
	})
}

const unchangedUserConfigAgentBody = `#!/bin/sh
echo AGENT-RAN
printf 'USER_MCP='
cat "$HOME/.mcp.json"
printf 'USER_SETTINGS='
cat "$HOME/.claude/settings.json"
printf 'REPO_FILE='
cat "$TMPDIR/repo/tracked"
`

func TestBashAgentLaunchLeavesUserConfigAndRepositoryUntouched(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "claude", unchangedUserConfigAgentBody)
	cmd := `mkdir -p "$HOME/.claude" "$TMPDIR/repo"; printf 'user-mcp-before\n' > "$HOME/.mcp.json"; printf 'settings-before\n' > "$HOME/.claude/settings.json"; printf 'repo-before\n' > "$TMPDIR/repo/tracked"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude
`
	if _, err := s.ptmx.Write([]byte(cmd)); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the unchanged user files", func() bool {
		out := s.output()
		return strings.Contains(out, "USER_MCP=user-mcp-before") &&
			strings.Contains(out, "USER_SETTINGS=settings-before") &&
			strings.Contains(out, "REPO_FILE=repo-before")
	})
}

func TestStage1CarriesOnlyNonSecretAgentPaths(t *testing.T) {
	opts := LaunchOptions{
		AgentHelperPath:     "/opt/nocx helper",
		AgentToolSocketPath: "/tmp/nocx-tool.sock",
		Capability:          strings.Repeat("ab", 32),
	}
	body, err := Stage1Frame(ShellBash, opts)
	if err != nil {
		t.Fatalf("Stage1Frame: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "NOCX_AGENT_HELPER_PATH='/opt/nocx helper'") {
		t.Fatalf("stage-1 omitted the bridge path: %s", got)
	}
	if !strings.Contains(got, "NOCX_TOOL_SOCKET='/tmp/nocx-tool.sock'") {
		t.Fatalf("stage-1 omitted the tool socket path: %s", got)
	}
	for _, forbidden := range []string{"NOCX_AGENT_REPORT", "NOCX_LIFECYCLE_CAPABILITY", opts.Capability} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("stage-1 carried forbidden authority %q", forbidden)
		}
	}
}
