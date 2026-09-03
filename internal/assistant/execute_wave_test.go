package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/wave"
)

const testWaveEnv = "env-local"

// fakeWaveRecord records what it was asked and answers what it is told to.
type fakeWaveRecord struct {
	registered []wave.RegisterRequest
	held       []wave.Participant
	registerFn func(wave.RegisterRequest) (wave.Participant, error)
	heldErr    error
	heldFor    []string
}

func (f *fakeWaveRecord) Register(_ context.Context, req wave.RegisterRequest) (wave.Participant, error) {
	f.registered = append(f.registered, req)
	if f.registerFn != nil {
		return f.registerFn(req)
	}
	return wave.Participant{ID: "p-1", State: wave.StateLive, Task: req.Task}, nil
}

func (f *fakeWaveRecord) HeldBy(_ context.Context, coordinatorSession string) ([]wave.Participant, error) {
	f.heldFor = append(f.heldFor, coordinatorSession)
	return f.held, f.heldErr
}

func waveSeams(rec WaveRecord) toolSeams {
	return toolSeams{waves: rec, waveEnvironment: testWaveEnv, runID: "run-1"}
}

// A coordinator holding the environment its grant named.
func testCoordinator(session string, environments ...string) *agenttools.WaveCoordinator {
	scopes := make([]content.GrantScope, 0, len(environments))
	for _, e := range environments {
		scopes = append(scopes, content.GrantScope{Kind: content.ResourceEnvironment, ID: e})
	}
	return agenttools.NewWaveCoordinator(session, scopes)
}

// D3, at the tool: the answer is about the run's OWN session, and the model
// has no way to ask about another — there is no parameter to put one in.
func TestWaveHoldingsAnswersTheRunsOwnSession(t *testing.T) {
	rec := &fakeWaveRecord{held: []wave.Participant{
		{ID: "p-1", State: wave.StateLive, Task: "read AGENTS.md"},
		{
			ID: "p-2", State: wave.StateCompleted, Task: "build it",
			Declared: &wave.Declaration{OK: true, Summary: "built"},
		},
	}}
	out, err := executeWaveHoldings(context.Background(),
		testCoordinator("sess-coordinator", testWaveEnv), json.RawMessage(`{}`), waveSeams(rec))
	if err != nil {
		t.Fatalf("holdings: %v", err)
	}
	if len(rec.heldFor) != 1 || rec.heldFor[0] != "sess-coordinator" {
		t.Fatalf("record was asked about %v, want the run's own session", rec.heldFor)
	}
	var got waveHoldingsResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("participants = %+v, want both", got.Participants)
	}
	if got.Participants[0].Task != "read AGENTS.md" || got.Participants[0].State != "live" {
		t.Fatalf("first participant = %+v", got.Participants[0])
	}
	// The summary rides only when the worker actually said something.
	if got.Participants[0].Summary != "" {
		t.Fatalf("a worker that said nothing was given a summary: %+v", got.Participants[0])
	}
	if got.Participants[1].Summary != "built" {
		t.Fatalf("second participant = %+v", got.Participants[1])
	}
}

// A9, asserted against the wire and not against intent: the holdings schema
// has NO participant parameter, the way session.run's has no session
// parameter. What the model cannot spell it cannot ask for.
func TestTheWaveToolsNameNoParticipantOnTheWire(t *testing.T) {
	for _, name := range []string{"wave.holdings.schema.json", "wave.spawn.schema.json"} {
		t.Run(name, func(t *testing.T) {
			//nolint:gosec // name is one of the two literals in the loop above
			raw, err := os.ReadFile("../../contracts/tools/" + name)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			var schema struct {
				AdditionalProperties bool                       `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("schema: %v", err)
			}
			if schema.AdditionalProperties {
				t.Fatalf("%s admits additional properties, so it bounds nothing", name)
			}
			for prop := range schema.Properties {
				if strings.Contains(strings.ToLower(prop), "participant") ||
					strings.Contains(strings.ToLower(prop), "session") ||
					strings.Contains(strings.ToLower(prop), "worker") {
					t.Fatalf("%s takes %q; the holder's own resources live inside the object", name, prop)
				}
			}
		})
	}
}

// A spawn outside the run's fence is REFUSED, and the refusal names both what
// was asked for and what was available — a message that says only "no" leaves
// the model guessing at a boundary it cannot see.
func TestWaveSpawnRefusesAnEnvironmentOutsideTheFence(t *testing.T) {
	rec := &fakeWaveRecord{}
	// A coordinator whose grant named a DIFFERENT environment.
	_, err := executeWaveSpawn(context.Background(),
		testCoordinator("sess-coordinator", "env-somewhere-else"),
		json.RawMessage(`{"command":"claude","task":"read it"}`), waveSeams(rec))
	if err == nil {
		t.Fatalf("a spawn outside the fence was accepted")
	}
	if !strings.Contains(err.Error(), testWaveEnv) || !strings.Contains(err.Error(), "env-somewhere-else") {
		t.Fatalf("err = %v, want it to name what was asked for and what was available", err)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("a refused spawn reached the record: %+v", rec.registered)
	}
}

// A10 in its strongest form: a coordinator minted from a grant with NO
// environment at all can spawn nowhere. This is what "a wave call carries no
// authority the session does not already have" means in code.
func TestACoordinatorFromAFenceWithNoEnvironmentCanSpawnNowhere(t *testing.T) {
	rec := &fakeWaveRecord{}
	_, err := executeWaveSpawn(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"command":"claude","task":"read it"}`), waveSeams(rec))
	if err == nil {
		t.Fatalf("a coordinator with an empty fence spawned a worker")
	}
	if len(rec.registered) != 0 {
		t.Fatalf("a refused spawn reached the record: %+v", rec.registered)
	}
}

// The ordinary case: the command and the task travel through untouched, the
// coordinator session is the run's own, and the result says live.
func TestWaveSpawnStartsOneWorkerAndReturnsItLive(t *testing.T) {
	rec := &fakeWaveRecord{}
	out, err := executeWaveSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWaveEnv),
		json.RawMessage(`{"command":"claude --resume","task":"read AGENTS.md and report"}`), waveSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(rec.registered) != 1 {
		t.Fatalf("record saw %d registrations, want 1", len(rec.registered))
	}
	req := rec.registered[0]
	if req.Command != "claude --resume" || req.Task != "read AGENTS.md and report" {
		t.Fatalf("registration = %+v", req)
	}
	if req.CoordinatorSession != "sess-coordinator" || req.Role != wave.RoleWorker {
		t.Fatalf("registration = %+v, want the run's own session and a worker role", req)
	}
	if req.Environment != testWaveEnv {
		t.Fatalf("registration environment = %q, want the fenced one", req.Environment)
	}
	// Provenance travels; it decides nothing.
	if req.CreatedByRunID != "run-1" {
		t.Fatalf("registration lost its run provenance: %+v", req)
	}
	var got waveSpawnResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if got.ID != "p-1" || got.State != "live" {
		t.Fatalf("result = %+v, want the worker live", got)
	}
}

// A registration that failed is an ERROR and never a half-result: there is no
// id for a coordinator to address something that did not start.
func TestAFailedSpawnIsAnErrorAndNotAResult(t *testing.T) {
	rec := &fakeWaveRecord{registerFn: func(wave.RegisterRequest) (wave.Participant, error) {
		return wave.Participant{ID: "p-1", State: wave.StateInterrupted}, wave.ErrEnrolmentNeverArrived
	}}
	out, err := executeWaveSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWaveEnv),
		json.RawMessage(`{"command":"claude","task":"read it"}`), waveSeams(rec))
	if err == nil {
		t.Fatalf("a spawn that never enrolled returned a result: %s", out)
	}
	if !errors.Is(err, wave.ErrEnrolmentNeverArrived) {
		t.Fatalf("err = %v, want the cause to survive", err)
	}
	if out != "" {
		t.Fatalf("a failed spawn returned %q", out)
	}
}

// A backend with no record refuses and says so, rather than accepting a spawn
// into nothing. Both tools, because a coordinator that could ASK but not spawn
// would be told an empty holdings and conclude it had started nothing.
func TestTheWaveToolsRefuseWhenThereIsNoRecord(t *testing.T) {
	c := testCoordinator("sess-coordinator", testWaveEnv)
	seams := toolSeams{waveEnvironment: testWaveEnv}
	if _, err := executeWaveHoldings(context.Background(), c, json.RawMessage(`{}`), seams); err == nil {
		t.Fatalf("holdings answered with no record wired")
	}
	if _, err := executeWaveSpawn(context.Background(), c,
		json.RawMessage(`{"command":"claude","task":"t"}`), seams); err == nil {
		t.Fatalf("spawn accepted with no record wired")
	}
}

// A run with no session cannot be answered about, and an empty holdings would
// be indistinguishable from a coordinator that holds nothing.
func TestWaveToolsRefuseARunWithNoSession(t *testing.T) {
	rec := &fakeWaveRecord{}
	if _, err := executeWaveHoldings(context.Background(),
		testCoordinator("", testWaveEnv), json.RawMessage(`{}`), waveSeams(rec)); err == nil {
		t.Fatalf("holdings answered for a run with no session")
	}
}

// Both tools refuse a capability that is not a coordinator's. The type switch
// is what proves the two authorities are distinct; this is the assertion that
// the executors honour it rather than casting hopefully.
func TestWaveToolsRefuseAnotherCapability(t *testing.T) {
	notACoordinator := agenttools.NewSessionReader(nil, nil, nil)
	if _, err := executeWaveHoldings(context.Background(), notACoordinator,
		json.RawMessage(`{}`), waveSeams(&fakeWaveRecord{})); err == nil {
		t.Fatalf("holdings ran on a session reader")
	}
	if _, err := executeWaveSpawn(context.Background(), notACoordinator,
		json.RawMessage(`{"command":"claude","task":"t"}`), waveSeams(&fakeWaveRecord{})); err == nil {
		t.Fatalf("spawn ran on a session reader")
	}
}
