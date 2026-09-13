package shellintegration

// THE STAGED MCP CONFIG CARRIES THE PANE'S TOOL TOKEN (nocx-50w7p.16, AC2).
//
// The token reaches the agent as the `nocx` server entry's `env` and nowhere
// else: not in `args` (a far host's /proc/<pid>/cmdline is world-readable, which
// is why it goes in the environment of the child the agent starts rather than in
// argv), not in the shell's exported environment (a non-exported variable is not
// inherited, and the launch clears it before the agent is exec'd), and not in
// the pane's output.
//
// The token is not typed into an interactive shell here and that is deliberate:
// typing it would put it in the transcript this test is asserting it out of, and
// the product never does that either — it arrives through the descriptor
// (capability_source.go) and the staging reads it from the variable. The test
// therefore hands the shell a script over its OWN standard input from a private
// 0600 file, which is the capabilityLiteral transport design D4 allows, and
// reads the staged file back from Go rather than printing it.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type stagedServer struct {
	MCPServers map[string]struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	} `json:"mcpServers"`
}

const (
	stageHelper = "/opt/nocx-helper"
	stageSocket = "/tmp/nocx-tool.sock"
	stageToken  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// runIntegrationStage runs the SHIPPED integration script in a real shell, sets
// the token the way the descriptor would (a non-exported variable), stages the
// launch directory, and returns: everything the shell printed, the launch
// directory, and the bytes of the mcp.json it wrote.
//
// The script is sourced from a file and the token from a second file, so nothing
// secret is in the shell's argv or in this process's own.
func runIntegrationStage(t *testing.T, shell, token string, extra string) (out, dir string, config []byte) {
	t.Helper()
	shellPath := requireIntegrationShell(t, shell)
	scriptPath, err := filepath.Abs(filepath.Join("scripts", "nocx."+shell))
	if err != nil {
		t.Fatalf("resolve the integration script: %v", err)
	}
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		t.Fatalf("the shipped %s script is not where the test expects it: %v", shell, statErr)
	}
	home := t.TempDir()
	rc := filepath.Join(home, "script")
	// #nosec G306 — test fixture, private to this test.
	body := "source " + scriptPath + "\n"
	if token != "" {
		body += "__nocx_agent_token=" + shellQuoteForTest(token) + "\n"
	}
	body += extra
	if writeErr := os.WriteFile(rc, []byte(body), 0o600); writeErr != nil {
		t.Fatalf("write the shell script: %v", writeErr)
	}

	cmd := exec.Command(shellPath, "-c", "source "+rc) // #nosec G204 — a fixture path this test wrote.
	cmd.Env = []string{
		"NOCX_SHELL_INTEGRATION=1",
		"HOME=" + home,
		"TERM=xterm",
		"TMPDIR=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	combined, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("%s failed to stage the launch directory: %v\n%s", shell, runErr, combined)
	}
	out = string(combined)

	// WHERE THE LAUNCH DIRECTORY IS, told by the shell rather than guessed: the
	// staging prints it under a private 0700 parent this test named.
	if !strings.Contains(out, "STAGE_OK\n") {
		t.Fatalf("the shell did not stage a launch directory; output:\n%s", out)
	}
	rest := out[strings.Index(out, "STAGE_OK\n")+len("STAGE_OK\n"):]
	dir = strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	config, readErr := os.ReadFile(filepath.Join(dir, "mcp.json")) // #nosec G304 — the path the test's own shell reported.
	if readErr != nil {
		t.Fatalf("read the staged mcp.json: %v", readErr)
	}
	return out, dir, config
}

func shellQuoteForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// stageThenReport prints the launch directory so the test can read the file
// itself, and reports whether the bearer is still in the shell's own variable
// space and whether any child could have inherited it.
const stageThenReport = `if __nocx_agent_stage ` + stageHelper + ` ` + stageSocket + `; then
    echo STAGE_OK
    echo "$__nocx_agent_launch_dir"
    echo "MODES $(stat -c %a "$__nocx_agent_launch_dir") $(stat -c %a "$__nocx_agent_launch_dir/mcp.json")"
    echo "EXPORTED [$(printenv NOCX_AGENT_TOKEN || echo absent)]"
else
    echo "STAGE_FAILED reason=$__nocx_agent_stage_reason"
fi
`

func TestTheStagedMCPConfigCarriesTheTokenInEnvAndNowhereElse(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			out, dir, config := runIntegrationStage(t, shell, stageToken, stageThenReport)

			var staged stagedServer
			if err := json.Unmarshal(config, &staged); err != nil {
				t.Fatalf("the staged config is not JSON: %v\n%s", err, config)
			}
			server, ok := staged.MCPServers["nocx"]
			if !ok || len(staged.MCPServers) != 1 {
				t.Fatalf("servers = %#v, want exactly one named nocx", staged.MCPServers)
			}
			// IN `env`, and in `args` NOWHERE.
			if got := server.Env["NOCX_AGENT_TOKEN"]; got != stageToken {
				t.Errorf("the staged config's env carries %q, want the token", got)
			}
			wantArgs := []string{"mcp", "--socket", stageSocket}
			if len(server.Args) != len(wantArgs) {
				t.Fatalf("args = %#v, want %#v", server.Args, wantArgs)
			}
			for i := range wantArgs {
				if server.Args[i] != wantArgs[i] {
					t.Fatalf("args = %#v, want %#v", server.Args, wantArgs)
				}
			}
			for _, a := range server.Args {
				if strings.Contains(a, stageToken) {
					t.Fatalf("the token is in argv, which a far host publishes: %#v", server.Args)
				}
			}

			// The volume is private and the file is private.
			if !strings.Contains(out, "MODES 700 600") {
				t.Errorf("launch directory / config modes are not 700 / 600: %s", out)
			}
			if info, err := os.Stat(dir); err != nil {
				t.Errorf("stat the launch directory: %v", err)
			} else if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("launch directory mode = %o, want 700", got)
			}

			// AND NO CHILD COULD HAVE INHERITED IT. The variable is
			// non-exported by construction and the launch clears it before the
			// agent is exec'd; this is the same question asked from the
			// outside, of the shell's own exported environment.
			if !strings.Contains(out, "EXPORTED [absent]") {
				t.Errorf("the token is in the shell's exported environment: %s", out)
			}

			// THE TOKEN IS NOT IN THE PANE'S OUTPUT. Everything the shell said
			// before the staging is here, and the staging writes the bearer to
			// a file rather than to a terminal.
			if strings.Contains(strings.SplitN(out, "STAGE_OK", 2)[0], stageToken) {
				t.Errorf("the token reached the terminal before the staging:\n%s", out)
			}
		})
	}
}

// TestTheStagedMCPConfigWithoutATokenIsUnchanged — the pairing, and the reason
// the env field is conditional: a pane with no tool surface stages the file it
// staged before this existed, byte for byte, so nothing about a session without
// a bearer changed.
func TestTheStagedMCPConfigWithoutATokenIsUnchanged(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			_, _, config := runIntegrationStage(t, shell, "", stageThenReport)

			var staged stagedServer
			if err := json.Unmarshal(config, &staged); err != nil {
				t.Fatalf("the staged config is not JSON: %v\n%s", err, config)
			}
			server := staged.MCPServers["nocx"]
			if len(server.Env) != 0 {
				t.Fatalf("a pane with no token staged an env block: %#v", server.Env)
			}
			if strings.Contains(string(config), `"env"`) {
				t.Fatalf("a pane with no token staged an env key: %s", config)
			}
			want := `{"mcpServers":{"nocx":{"type":"stdio","command":"` + stageHelper +
				`","args":["mcp","--socket","` + stageSocket + `"]}}}` + "\n"
			if string(config) != want {
				t.Fatalf("the no-token config is not byte-identical to the pre-token shape:\n got %s\nwant %s", config, want)
			}
		})
	}
}
