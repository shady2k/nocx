package app

// Where a worker's pane opens (nocx-ty5ks).
//
// A participant's session was opened with no cwd at all, so `resolveSessionCwd`
// fell to `os.UserHomeDir()` and every worker started in $HOME whatever its
// coordinator was doing. Two things went wrong at once and only the second was
// visible: the worker worked in the wrong directory, and an agent launched in a
// directory it has not been trusted in opens a startup dialog instead of an
// input box — measured 2026-09-10, where the pane held that dialog for the
// whole enrolment budget and the spawn was compensated away.
//
// The coordinator's own pane is the answer, and its cwd has one owner already:
// `content.Layout.SetPaneCwd`, written by the renderer from a VERIFIED OSC 7
// (AD-5). These tests assert the spawner reads that owner and carries what it
// says onto the open — never that it re-derives a cwd of its own.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workers"
)

// integratedAxis is the answer these tests are not about: the shell answered,
// the pane can be watched, and the spawn proceeds to the part being asserted.
func integratedAxis() transport.IntegrationOutcome {
	return transport.IntegrationOutcome{Registered: true, Status: transport.IntegrationIntegrated}
}

// openCoordinatorPane opens a real session standing in for the coordinator's
// own pane, so the spawner resolves session -> pane the way it does in
// production rather than being handed a pane id by the test.
func openCoordinatorPane(t *testing.T, stand *axisGateStand, paneID string) session.ID {
	t.Helper()
	sess, err := stand.reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, PaneID: paneID,
	})
	if err != nil {
		t.Fatalf("open the coordinator's own session: %v", err)
	}
	return sess.ID()
}

// Criterion: the participant's session is opened with the coordinator's
// directory, read from the pane the coordinator's session belongs to.
func TestAWorkerOpensInItsCoordinatorsDirectory(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = integratedAxis()
	coordinator := openCoordinatorPane(t, stand, "coord-pane")
	stand.tabs.cwds = map[string]string{"coord-pane": "/home/dev/repos/iaam"}

	if _, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant:        "p-cwd",
		Group:              "worker-1",
		CoordinatorSession: string(coordinator),
		Command:            "run-agent",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if got := stand.opener.lastSpec().Cwd; got != "/home/dev/repos/iaam" {
		t.Fatalf("the participant's session opened with cwd %q, want the coordinator's %q",
			got, "/home/dev/repos/iaam")
	}
	if asked := stand.tabs.cwdAsked(); len(asked) != 1 || asked[0] != "coord-pane" {
		t.Fatalf("pane cwd was asked for %v, want exactly the coordinator's own pane", asked)
	}
	// And the ROW says the same thing, so a restore reopens the participant's
	// tab where it was rather than in the home directory the open no longer
	// falls back to.
	if got := stand.tabs.createdPane().Cwd; got != "/home/dev/repos/iaam" {
		t.Fatalf("the participant's pane row records cwd %q, want the coordinator's %q",
			got, "/home/dev/repos/iaam")
	}
}

// Criterion: a coordinator whose pane has no verified cwd still spawns, and
// the open carries no cwd — the fallback stays the session registry's, which
// is $HOME, rather than a directory this package guessed.
func TestAWorkerWhoseCoordinatorHasNoRecordedDirectoryStillOpens(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = integratedAxis()
	coordinator := openCoordinatorPane(t, stand, "coord-pane")
	stand.tabs.cwdErr = errors.New("no such pane")

	if _, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant:        "p-nocwd",
		Group:              "worker-1",
		CoordinatorSession: string(coordinator),
		Command:            "run-agent",
	}); err != nil {
		t.Fatalf("Spawn refused a coordinator with no recorded cwd: %v", err)
	}
	if got := stand.opener.lastSpec().Cwd; got != "" {
		t.Fatalf("the participant's session opened with cwd %q, want none", got)
	}
}

// Criterion: a spawn with no coordinator session named — every caller before
// nocx-ty5ks — asks the layout nothing and opens exactly as it did.
func TestASpawnWithNoCoordinatorSessionAsksForNoDirectory(t *testing.T) {
	stand := newAxisGateStand(t, false)
	stand.awaiter.outcome = integratedAxis()
	stand.tabs.cwds = map[string]string{"coord-pane": "/home/dev/repos/iaam"}

	if _, err := stand.spawner.Spawn(context.Background(), workers.SpawnRequest{
		Participant: "p-anonymous", Group: "worker-1", Command: "run-agent",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := stand.opener.lastSpec().Cwd; got != "" {
		t.Fatalf("the participant's session opened with cwd %q, want none", got)
	}
	if asked := stand.tabs.cwdAsked(); len(asked) != 0 {
		t.Fatalf("pane cwd was asked for %v, want nothing asked at all", asked)
	}
}
