package session_test

// The pane's PROCESS environment is the second route to its tool endpoint, and
// the one nothing owned (nocx-e7khb).
//
// tool_endpoint_test.go asserts the route that IS owned: the launch script
// renders NOCX_TOOL_SOCKET from the request, so each coordinator's pane names
// its own endpoint. What that leaves open is the environment underneath the
// script. A helper daemon inherits the environment of whichever coordinator
// started it (D12: one daemon per generation, several coordinators on it), and
// internal/pty hands a pane os.Environ() plus the launch's own additions — so a
// daemon started from a process that carries NOCX_TOOL_SOCKET gave every pane
// that value, and the launch script only ever OVERWROTE it.
//
// Overwriting is not owning. A launch that renders no script at all (the plain
// tier: an explicit shell argv, or a login shell that is neither bash nor zsh)
// renders no assignment either, and the inherited value stands unopposed — the
// pane names a coordinator that never asked for it. The same holds for an
// enhanced pane whose script does not fully apply, which is how this was first
// seen: on macOS CI, once, the first coordinator's pane printed
// `/run/nocx/daemon-start-tool.sock` — the decoy tool_endpoint_test.go sets as
// the ambient value precisely so a daemon-scoped regression is loud.
//
// So the assertion here is the negative one that file cannot make: with the
// ambient variable set, a pane of the PLAIN tier names NOTHING. The plain tier
// is the deterministic seam — it has no script to race, so what the pane prints
// is exactly what it inherited.

import (
	"os/exec"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/shellintegration"
)

func TestAPaneNeverInheritsTheDaemonsOwnToolEndpoint(t *testing.T) {
	const ambient = "/run/nocx/daemon-start-tool.sock"
	t.Setenv(shellintegration.ToolSocketEnvVar, ambient)

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is not installed and this test drives a real pane: %v", err)
	}

	// An explicit argv, which is what makes this the PLAIN tier: Spawn's
	// enhanced arm requires len(shellArgs) == 0, so nothing here renders a
	// launch script and the pane's environment is the whole of what it was
	// told. --norc --noprofile keeps the runner's own rc files out of it.
	spawner := session.NewLocalSpawner(
		logtest.Slog(t),
		session.Shell{Path: bash, Args: []string{"--norc", "--noprofile", "-i"}},
		"",
	)
	proc, err := spawner.Spawn(session.SpawnRequest{
		SessionID: "sess-ambient-tool-endpoint",
		Cwd:       "/",
		Cols:      80,
		Rows:      24,
		// A real endpoint on the request, so a pane that named the ambient
		// value did so having been told a different one.
		AgentToolEndpoint: "/tmp/nocx-this-coordinator.sock",
	})
	if err != nil {
		t.Fatalf("spawn a plain pane: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })

	printed := `printf '` + toolEndpointMarker + `%s]\n' "$NOCX_TOOL_SOCKET"`
	if _, err := proc.Write([]byte(printed + "\n")); err != nil {
		t.Fatalf("ask the pane to print its tool endpoint: %v", err)
	}

	read := make(chan string, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 8*1024)
		for {
			n, readErr := proc.Read(buf)
			if n > 0 {
				seen = append(seen, buf[:n]...)
				if value, ok := printedToolEndpoint(seen); ok {
					read <- value
					return
				}
			}
			if readErr != nil {
				read <- ""
				return
			}
		}
	}()

	select {
	case value := <-read:
		if value == ambient {
			t.Fatalf("the pane named the daemon's own tool endpoint %q, which no coordinator asked it to", value)
		}
		if value != "" {
			t.Fatalf("a pane whose launch rendered no tool endpoint named %q", value)
		}
	case <-time.After(toolEndpointWait):
		t.Fatal("the pane never printed its tool endpoint")
	}
}
