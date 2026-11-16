package shellintegration

// nocx-2tesu's acceptance criterion 3: a test at the seam a user reaches,
// not at the seam agent_mcp_shell_test.go already covers. That file drives
// the SHELL's own behaviour given NOCX_TOOL_SOCKET and NOCX_AGENT_HELPER_PATH
// already exported — by a person typing `export ...` in the pane. It cannot
// catch this bug, because nothing in the product ever asked a person to do
// that; a local pane is started through LocalEnhancedLaunchInMemory
// (internal/helper/session/spawn_local.go), which is the function this file
// drives instead. The technique — a real bash on a real pty, a nestedKernel
// answering the lifecycle handshake AND the agent enrolment exchange, a fake
// `claude` on PATH — is agent_mcp_shell_test.go's own; only the launch is
// different.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
)

// localHandshakeBudgetEnv is the pane's lifecycle handshake budget, in the
// product's own spelling (internal/shellintegration/scripts/nocx.bash reads
// it, and channel_exec_test.go's TCP helper sets it the same way). It is
// deliberately below waitForHandshake's backstop: the pane then always
// answers — with its handshake, or by failing open — before the wait's own
// ceiling can fire.
const localHandshakeBudgetEnv = "NOCX_LIFECYCLE_TIMEOUT_MS=3000"

// startLocalBashPane boots a real bash pane through the exact function a
// local pane is started with, with the tool socket (if any) and the fake
// agent binary already in place — nobody exports anything by hand.
func startLocalBashPane(t *testing.T, k *nestedKernel, agentToolSocketPath, binName, fakeBody string) *channelShell {
	return startLocalBashPaneWithToken(t, k, agentToolSocketPath, binName, fakeBody, "")
}

func startLocalBashPaneWithToken(t *testing.T, k *nestedKernel, agentToolSocketPath, binName, fakeBody, token string) *channelShell {
	t.Helper()
	bash := requireShell(t, "bash")

	home := t.TempDir()
	binDir := t.TempDir()
	// #nosec G306 — a stand-in agent binary in the test's own temp dir,
	// which must be executable to be found and run through PATH.
	if err := os.WriteFile(filepath.Join(binDir, binName), []byte(fakeBody), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", binName, err)
	}

	opts := localTestOpts()
	opts.AgentToolSocketPath = agentToolSocketPath
	opts.AgentToolToken = token
	launch, err := LocalEnhancedLaunchInMemory(bash, ShellBash, opts)
	if err != nil {
		t.Fatalf("LocalEnhancedLaunchInMemory: %v", err)
	}

	kernelFile, shellFile := lifecycleSocketpair(t)
	k.shellFile = shellFile

	// #nosec G204 — launch.Command is the requireShell-resolved bash; a real
	// interactive shell is the only way to observe what a user gets.
	cmd := exec.Command(launch.Command, launch.Args...)
	// The script's reader first and the lifecycle channel second — fd 3 and
	// fd 4, the order LifecycleFD names and the order the helper's spawner
	// (internal/helper/session/spawn_local.go) appends them in.
	extraFiles := append([]*os.File{}, launch.ExtraFiles...)
	extraFiles = append(extraFiles, shellFile)
	if launch.AgentToolTokenFile != nil {
		extraFiles = append(extraFiles, launch.AgentToolTokenFile)
	}
	cmd.ExtraFiles = extraFiles
	// THE PANE'S OWN HANDSHAKE BUDGET IS STATED, NOT ASSUMED (nocx-a62cl.6.5).
	// A pane that cannot read its accept fails open — a conventional
	// terminal with its native prompt and no lifecycle frame ever again
	// (ADR-0004) — and it decides that on the budget below. The test's wait
	// has a backstop of its own, and when the two are the same ten seconds
	// the backstop is what fails, so the failure says "timed out" about a
	// pane whose own answer was "I gave up". Stating a budget well inside
	// the backstop makes the pane's decision, not the clock, the thing the
	// wait observes.
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null",
			"PATH="+binDir+":"+os.Getenv("PATH"), localHandshakeBudgetEnv),
		launch.Env...,
	)

	go k.serveFile(kernelFile)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		launch.Abort()
		t.Fatalf("pty start: %v", err)
	}
	s := &channelShell{t: t, cmd: cmd, ptmx: ptmx, kernel: k}
	go s.readPump()
	t.Cleanup(func() { _ = ptmx.Close(); _ = cmd.Process.Kill() })

	// The daemon's own sequence after the fork: release the parent's copies.
	// No tier types a bootstrap line any more (nocx-1ebrs.8) — bash's script
	// is sourced through --rcfile /dev/fd/3 during startup, and zsh's through
	// a transient ZDOTDIR — so there is nothing to write here.
	launch.Cleanup()

	s.waitForHandshake()
	return s
}

// TestBashLocalPaneLaunchCarriesToolSocketAndStagesAgent is criteria 1 and 3
// together: LaunchOptions.AgentToolSocketPath — what
// internal/helper/session.LocalSpawner now sets from the value
// cmd/nocx-server's composition root hands down (nocx-2tesu) — reaches a
// typed `claude` with nobody exporting anything, and staging succeeds.
func TestBashLocalPaneLaunchCarriesToolSocketAndStagesAgent(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "tool.sock")
	k := newNestedKernel(t)
	k.launchCommand = "claude"
	s := startLocalBashPane(t, k, sock, "claude", inspectingAgentBody)

	if _, err := s.ptmx.Write([]byte("claude --model test user-prompt\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitForEvent(t, k, "agent_withdraw")
	waitUntil(t, "the agent output", func() bool { return strings.Contains(s.output(), "AGENT-RAN") })
	s.waitForPromptAfterComplete()
	s.waitForRenderedThroughPrompt()

	out := strings.ReplaceAll(s.output(), "\r", "")
	if strings.Contains(out, "@EARLYTOKEN@") {
		t.Fatal("the local pane exposed an unresolved startup slot")
	}
	if strings.Contains(out, "tool surface unavailable") {
		t.Fatalf("a pane started with a configured tool socket refused the tool surface: %q", out)
	}
	if !strings.Contains(out, "ARG_3=--mcp-config") {
		t.Fatalf("the staged MCP argument was not appended: %q", out)
	}

	begin := strings.Index(out, "CONFIG_BEGIN\n")
	end := strings.Index(out, "\nCONFIG_END")
	if begin < 0 || end <= begin {
		t.Fatalf("MCP config was not printed by the launched agent: %q", out)
	}
	var config stagedMCPConfig
	configText := strings.TrimSpace(out[begin+len("CONFIG_BEGIN\n") : end])
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("decode staged MCP config: %v; output=%q", err, out)
	}
	server, ok := config.MCPServers["nocx"]
	if !ok || len(config.MCPServers) != 1 {
		t.Fatalf("MCP servers = %#v, want exactly one server named nocx", config.MCPServers)
	}
	wantArgs := []string{"mcp", "--socket", sock}
	if len(server.Args) != len(wantArgs) {
		t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
	}
	for i := range wantArgs {
		if server.Args[i] != wantArgs[i] {
			t.Fatalf("MCP args = %#v, want %#v", server.Args, wantArgs)
		}
	}
}

// TestBashLocalPaneLaunchAuthenticatesItsSpawnBoundToolBearer proves a local
// pane's launched agent receives the exact bearer in its private MCP config.
// The fake agent reports only whether the config matches; it never prints the
// bearer or the config contents into terminal output.
func TestBashLocalPaneLaunchAuthenticatesItsSpawnBoundToolBearer(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const body = `#!/bin/sh
for arg do
    if [ "$read_config" = 1 ]; then
        if grep -Fq '"NOCX_AGENT_TOKEN":"` + token + `"' "$arg"; then echo TOOL_AUTHENTICATED; else echo TOOL_AUTH_REFUSED; fi
        break
    fi
    [ "$arg" = "--mcp-config" ] && read_config=1
done
`
	k := newNestedKernel(t)
	k.launchCommand = "claude"
	s := startLocalBashPaneWithToken(t, k, filepath.Join(t.TempDir(), "tool.sock"), "claude", body, token)
	if _, err := s.ptmx.Write([]byte("claude --model test prompt\n")); err != nil {
		t.Fatalf("type agent command: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitForEvent(t, k, "agent_withdraw")
	waitUntil(t, "the fake agent's auth result", func() bool {
		return strings.Contains(s.output(), "TOOL_AUTHENTICATED") || strings.Contains(s.output(), "TOOL_AUTH_REFUSED")
	})
	out := strings.ReplaceAll(s.output(), "\r", "")
	if !strings.Contains(out, "TOOL_AUTHENTICATED") {
		t.Fatal("the local agent tool did not authenticate with its pane-bound bearer")
	}
	if strings.Contains(out, token) {
		t.Fatal("the local pane disclosed its bearer in terminal output")
	}
}

// TestBashLocalPaneLaunchWithNoToolSocketKeepsTheRefusalSoft is criterion 2:
// a pane started for a backend running no tool endpoint (the composition
// root never called SetLocalToolSocketPath, or called it with "" —
// startToolEndpoint's own nil, nil) carries no NOCX_TOOL_SOCKET, and the
// shell's existing refusal text is exactly what a user sees — never a
// pane that fails to start, and never one silently pointed at nothing.
func TestBashLocalPaneLaunchWithNoToolSocketKeepsTheRefusalSoft(t *testing.T) {
	k := newNestedKernel(t)
	k.launchCommand = "claude"
	s := startLocalBashPane(t, k, "", "claude", agentFallbackBody)

	if _, err := s.ptmx.Write([]byte("claude --model test user-prompt\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitUntil(t, "the agent output", func() bool { return strings.Contains(s.output(), "AGENT-RAN") })

	out := strings.ReplaceAll(s.output(), "\r", "")
	if !strings.Contains(out, "nocx: tool surface unavailable — nocx tool socket path is not configured") {
		t.Fatalf("an unconfigured pane did not show the shell's own refusal: %q", out)
	}
	if strings.Contains(out, "UNEXPECTED_MCP_PATH=") {
		t.Fatalf("an unconfigured pane staged MCP config anyway: %q", out)
	}
}
