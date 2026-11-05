package shellintegration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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
	// run(), not a bare write followed by a read (nocx-atpw5.1.33): the two
	// render waits this test used to make — waitForPromptAfterComplete and
	// waitForRenderedThroughPrompt — examine the whole event log, so an earlier
	// command satisfies them and they return without waiting for this one. That
	// was invisible here only because the pane has run exactly one command.
	s.run(`mkdir -p "$TMPDIR/nocx-agent-launch.stale"; printf '999999999\n' > "$TMPDIR/nocx-agent-launch.stale/lease"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude --model test user-prompt`)
	enrol := waitForEvent(t, k, "agent_enrol")
	withdraw := waitForEvent(t, k, "agent_withdraw")
	if withdraw.Seq <= enrol.Seq {
		t.Fatalf("the agent's interval closed out of order: enrol=%d withdraw=%d", enrol.Seq, withdraw.Seq)
	}
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
	for _, forbidden := range []string{"NOCX_AGENT_HELPER_PATH", "NOCX_LIFECYCLE", `"env"`} {
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

func TestBashAgentStagingFailureUsesDistinctWording(t *testing.T) {
	refusal := runBashAgentEnrollmentRefusal(t, func(k *nestedKernel) {
		k.refuseEnrolment = true
		k.enrolReason = "enrolment was refused"
	})
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "claude", agentFallbackBody)
	if _, err := s.ptmx.Write([]byte("unset NOCX_TOOL_SOCKET; export NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the staging-failure fallback", func() bool {
		return strings.Contains(s.output(), "AGENT-RAN")
	})
	staging := s.output()
	if !strings.Contains(staging, "nocx: tool surface unavailable — nocx tool socket path is not configured") {
		t.Fatalf("staging failure was not identified as a tool-surface failure: %q", staging)
	}
	if strings.Contains(staging, "nocx: not orchestrated") || strings.Contains(refusal, "tool surface unavailable") {
		t.Fatalf("enrolment refusal and staging failure wording overlap: refusal=%q staging=%q", refusal, staging)
	}
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
	if _, err := s.ptmx.Write([]byte("mkdir -p \"$TMPDIR/nocx-agent-launch.stale\"; printf '%s\\n' \"$$\" > \"$TMPDIR/nocx-agent-launch.stale/lease\"; ps -o lstart= -p \"$$\" | tr -s ' ' >> \"$TMPDIR/nocx-agent-launch.stale/lease\"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the live launch lease", func() bool {
		out := s.output()
		return strings.Contains(out, "AGENT-RAN") && strings.Contains(out, "STALE_PRESENT")
	})
	// The interval's other end before this test ends, for the reason the
	// worker-literal test states in full: the wrapper withdraws when the agent
	// exits, and a test that returns first closes the kernel connection under
	// that answer (nocx-atpw5.1.34).
	waitForEvent(t, k, "agent_withdraw")
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
	// The agent has printed, but its wrapper has not returned: the withdrawal
	// that closes the interval is still a frame in flight, and this test must
	// not tear the kernel connection down under its answer (nocx-atpw5.1.34,
	// the same shape as the worker-literal test's own interval end).
	waitForEvent(t, k, "agent_withdraw")
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
	// THE ENVIRONMENT THE FRAME HANDS THE PANE, BY NAME AND NOT BY A LIST OF
	// STRINGS THAT MUST NOT APPEAR. A forbidden-string list cannot fail for a
	// variable nobody has thought of yet, and it goes on passing for one that
	// has been renamed — so what is asserted is the EXACT SET the frame
	// exports, which is the whole of what reaches the pane and its children.
	//
	// The five addressing variables are the frame's own (the loader and the
	// integration scripts read them) and the two paths are the agent's. The
	// DROP'S variable (NOCX_AGENT_REPORT) used to be checked here as a
	// forbidden string and it is gone with the declaration (ADR-0070): a worker
	// reports through workers.report, so no rendezvous file is named to the
	// agent at all. The second assertion below is what says so, rather than a
	// name nothing can any longer produce.
	wantExported := []string{
		"NOCX_CAP_FD", "NOCX_LIFECYCLE_LANE", "NOCX_LIFECYCLE_DOMAIN",
		"NOCX_LIFECYCLE_EPOCH", "NOCX_LIFECYCLE_PORT", "NOCX_AGENT_HELPER_PATH",
		"NOCX_TOOL_SOCKET", "NOCX_BOOTSTRAP",
	}
	exported := exportedNames(got)
	if !reflect.DeepEqual(exported, wantExported) {
		t.Fatalf("stage-1 exports %v, want exactly %v", exported, wantExported)
	}
	// NO NAME FOR A REPORT. A worker says what it has to say by calling an MCP
	// tool, so a frame that named a file for the agent to write a verdict into
	// would be the drop coming back — under whatever spelling.
	for _, name := range exported {
		if strings.Contains(name, "REPORT") {
			t.Fatalf("stage-1 exports %q: the agent's declaration must be a tool call, not a rendezvous file", name)
		}
	}
	// And no bearer material, which is the claim the frame's own doc makes.
	// Checked against the VALUE and not only the name: a frame that exported
	// the capability under some other name would still be a frame carrying it.
	for _, forbidden := range []string{"NOCX_LIFECYCLE_CAPABILITY", opts.Capability} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("stage-1 carried forbidden authority %q", forbidden)
		}
	}
}

// exportedNames reads every `export NAME` line out of a rendered shell frame,
// in order. It is how a test asserts WHICH variables a frame hands a child
// without the test carrying a list that drifts from the frame.
func exportedNames(frame string) []string {
	var out []string
	for _, line := range strings.Split(frame, "\n") {
		// One line may name several variables (`export NOCX_CAP_FD
		// NOCX_LIFECYCLE_LANE ...`), which is what the loader writes, so the
		// line is split into words rather than read as one name.
		if names, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "export "); ok {
			out = append(out, strings.Fields(names)...)
		}
	}
	return out
}

func TestBashAndZshAgentRestoreUserTrapByteForByte(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start nestedParentStarter
	}{
		{name: "bash", start: startNestedBashParent},
		{name: "zsh", start: startNestedZshParent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newNestedKernel(t)
			s := tc.start(t, k, "claude", fakeAgentBody)
			// ONE LINE, run through the harness's own wait (nocx-atpw5.1.34).
			// The marker this test waits for is IN the line it types, so the
			// terminal's echo of that line satisfies a wait on the pane's text
			// before the shell has run a single command. Measured on this
			// machine, five runs of five: the wait was met with 0 launch
			// resolutions, 0 enrolments, 0 withdrawals and 0 completions
			// accepted. The subtest then returned with the wrapper's launch
			// round trip still in flight, the fixture's cleanup closed the
			// kernel connection under it, and the answer was encoded onto the
			// closed socket — CI's "encode agent_launch_resolved: write unix
			// @->@: use of closed network connection".
			//
			// run() never reads the pane's text to decide: it waits for a NEW
			// complete, then for that complete's render fence and the prompt
			// after it. Everything this line printed is in the buffer, and the
			// shell is back, before the assertion below looks at anything.
			s.run(`trap 'echo USER_INT_TRAP' INT; trap | grep ' INT$' > "$TMPDIR/trap.before"; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude; trap | grep ' INT$' > "$TMPDIR/trap.after"; cmp "$TMPDIR/trap.before" "$TMPDIR/trap.after" && echo TRAP_RESTORED`)
			out := strings.ReplaceAll(s.output(), "\r", "")
			if !strings.Contains(out, "TRAP_RESTORED") {
				t.Fatalf("the user's trap was not restored byte for byte: output=%q", out)
			}
			// The interval's other end, for the reason the worker-literal test
			// states in full: the wrapper withdraws when the agent exits, and a
			// test that ends first tears the socket down under that answer.
			waitForEvent(t, k, "agent_withdraw")
		})
	}
}

const interruptingAgentBody = `#!/bin/sh
for arg in "$@"; do
	case "$arg" in
		*/mcp.json) echo MCP_PATH="$arg" ;;
	esac
done
echo AGENT-STARTED
while :; do sleep 1; done
`

func TestBashAgentInterruptDoesNotExitInteractiveShell(t *testing.T) {
	k := newNestedKernel(t)
	s := startNestedBashParent(t, k, "claude", interruptingAgentBody)
	if _, err := s.ptmx.Write([]byte("set +m; export NOCX_TOOL_SOCKET=/tmp/nocx-tool.sock NOCX_AGENT_HELPER_PATH=/opt/nocx-helper; claude\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitUntil(t, "the interruptible agent", func() bool {
		return strings.Contains(s.output(), "AGENT-STARTED")
	})
	var mcpPath string
	waitUntil(t, "the staged launch path", func() bool {
		out := strings.ReplaceAll(s.output(), "\r", "")
		const marker = "MCP_PATH="
		if idx := strings.Index(out, marker); idx >= 0 {
			rest := out[idx+len(marker):]
			if end := strings.IndexByte(rest, '\n'); end >= 0 {
				rest = rest[:end]
			}
			mcpPath = strings.TrimSpace(rest)
		}
		return mcpPath != ""
	})
	if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("interrupt foreground process group: %v", err)
	}
	if _, err := s.ptmx.Write([]byte("echo SHELL_ALIVE\n")); err != nil {
		t.Fatalf("type shell liveness check: %v", err)
	}
	waitUntil(t, "the interactive shell after interrupt", func() bool {
		return strings.Contains(s.output(), "SHELL_ALIVE")
	})
	waitUntil(t, "the interrupted launch directory cleanup", func() bool {
		_, err := os.Stat(filepath.Dir(mcpPath))
		return os.IsNotExist(err)
	})
}
