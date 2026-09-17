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

// startLocalBashPane boots a real bash pane through the exact function a
// local pane is started with, with the tool socket (if any) and the fake
// agent binary already in place — nobody exports anything by hand.
func startLocalBashPane(t *testing.T, k *nestedKernel, agentToolSocketPath, binName, fakeBody string) *channelShell {
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
	cmd.ExtraFiles = append(append([]*os.File{}, launch.ExtraFiles...), shellFile)
	cmd.Env = append(
		cleanEnv("HOME="+home, "TMPDIR="+t.TempDir(), "TERM=xterm", "HISTFILE=/dev/null",
			"PATH="+binDir+":"+os.Getenv("PATH")),
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

	// The bootstrap, if the tier writes one; bash's in-memory tier writes
	// none (the script is sourced through --rcfile /dev/fd/3), but the same
	// sequence spawn_local.go itself runs is followed here rather than
	// special-cased per shell.
	if len(launch.Bootstrap) > 0 {
		if _, werr := ptmx.Write(launch.Bootstrap); werr != nil {
			t.Fatalf("write the launch bootstrap: %v", werr)
		}
	}
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
	s := startLocalBashPane(t, k, sock, "claude", inspectingAgentBody)

	if _, err := s.ptmx.Write([]byte("claude --model test user-prompt\n")); err != nil {
		t.Fatalf("type claude: %v", err)
	}
	waitForEvent(t, k, "agent_enrol")
	waitForEvent(t, k, "agent_withdraw")
	waitUntil(t, "the agent output", func() bool { return strings.Contains(s.output(), "AGENT-RAN") })

	out := strings.ReplaceAll(s.output(), "\r", "")
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

// TestBashLocalPaneLaunchWithNoToolSocketKeepsTheRefusalSoft is criterion 2:
// a pane started for a backend running no tool endpoint (the composition
// root never called SetLocalToolSocketPath, or called it with "" —
// startToolEndpoint's own nil, nil) carries no NOCX_TOOL_SOCKET, and the
// shell's existing refusal text is exactly what a user sees — never a
// pane that fails to start, and never one silently pointed at nothing.
func TestBashLocalPaneLaunchWithNoToolSocketKeepsTheRefusalSoft(t *testing.T) {
	k := newNestedKernel(t)
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
