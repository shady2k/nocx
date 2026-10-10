package app

// Placeholders expand at spawn (nocx-bag8j): the command a coordinator asks
// a worker to run may name the facts of the spawn it is part of, and the line
// the pane receives carries their values rather than the tokens.
//
// What is asserted is the line the PTY actually received — the same assertion
// the axis-gate test makes for the unexpanded line — because the queue only
// accepts, and a rewrite nobody delivered would otherwise pass unnoticed.

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

func TestAWorkerCommandLineCarriesTheSpawnFactsItsPlaceholdersName(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = transport.IntegrationOutcome{
		Registered: true, Status: transport.IntegrationIntegrated,
	}
	stand.tabs.cwds = map[string]string{"coord-pane": "/home/dev/repos/iaam"}
	coordinator := openCoordinatorPane(t, stand, "coord-pane")

	spawned, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant:        "p-placeholders",
		Group:              "worker-1",
		CoordinatorSession: string(coordinator),
		Command:            `agent --in {WORKSPACE_PATH} --for {WORKSPACE_ID} --id {UUID} --keep {NOT_A_PLACEHOLDER}`,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	pty := stand.ptys.last()
	if pty == nil {
		t.Fatal("no pty was opened")
	}
	// EnqueueWrite only accepts; the writeLoop drains onto the pty on its own
	// goroutine, so wait for the line rather than reading immediately.
	waittest.WaitFor(t, "the expanded command line to reach the pty", func() bool {
		got := pty.read()
		return strings.Contains(got, "--in /home/dev/repos/iaam") &&
			strings.Contains(got, "--for ws-test") &&
			strings.Contains(got, "--keep {NOT_A_PLACEHOLDER}") &&
			!strings.Contains(got, "{WORKSPACE_PATH}") &&
			!strings.Contains(got, "{WORKSPACE_ID}") &&
			uuidArgOf(got) != ""
	})
	line := pty.read()
	identified, ok := spawned.(workers.RestartIdentified)
	if !ok {
		t.Fatalf("spawned %T does not expose its restart identity", spawned)
	}
	resume := identified.RestartIdentity().Resume
	if resume != (workers.ResumeIdentity{Mode: workers.ResumeByID, ID: uuidArgOf(line)}) {
		t.Fatalf("restart identity = %+v, want the exact id passed to the PTY command %q", resume, uuidArgOf(line))
	}
}

// A spawn fact may itself contain spaces, and it must reach the process as
// ONE argument — the line re-quotes the expanded argv, and the pane's shell
// splits it back to one.
func TestAWorkspacePathWithASpaceStaysOneArgumentInTheTypedLine(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = transport.IntegrationOutcome{
		Registered: true, Status: transport.IntegrationIntegrated,
	}
	stand.tabs.cwds = map[string]string{"coord-pane": "/home/dev/repos/main workspace"}
	coordinator := openCoordinatorPane(t, stand, "coord-pane")

	if _, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant:        "p-spaced-workspace",
		Group:              "worker-1",
		CoordinatorSession: string(coordinator),
		Command:            `agent --workspace {WORKSPACE_PATH}`,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	pty := stand.ptys.last()
	if pty == nil {
		t.Fatal("no pty was opened")
	}
	waittest.WaitFor(t, "the expanded command line to reach the pty", func() bool {
		return pty.read() == `agent --workspace '/home/dev/repos/main workspace'`+"\n"
	})
}

// uuidArgOf answers the argument that follows --id when there is one, and ""
// when there is not: the assertion cares that A uuid was minted, not which.
func uuidArgOf(line string) string {
	i := strings.Index(line, "--id ")
	if i < 0 {
		return ""
	}
	rest := line[i+len("--id "):]
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}
