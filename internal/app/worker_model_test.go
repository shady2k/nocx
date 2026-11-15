package app

// Worker tool calls enter the assistant authority chain and the shipped executors.
// Model selection and tool proposals are covered by the real pi/harness tests;
// this test keeps the worker registration, holdings and close assertions.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
	"github.com/shady2k/nocx/internal/workers"
)

// launcherStandIn supplies the enrolment a real launcher would send, for
// every worker session that appears while tool calls run.
//
// It stands in for exactly one thing and says so: the enrolment act itself is
// internal/shellintegration's, proved there on a real pty in both shells. A
// spawn blocks until an enrolment arrives, so without this the first
// tool call would sit out its whole deadline.
func launcherStandIn(t *testing.T, w *wakeStand, stop <-chan struct{}) *sync.WaitGroup {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		seen := map[session.ID]bool{w.coordinator: true}
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			for _, s := range w.reg.List() {
				if seen[s.ID()] {
					continue
				}
				seen[s.ID()] = true
				lane := lifecycle.LaneID("lane-" + string(s.ID()))
				w.lanes.register(lane, string(s.ID()))
				w.enrol.enrolled(s.ID(), string(lane))
			}
		}
	}()
	return &wg
}

// The authority pipeline spawns three workers, reads holdings, and closes them.
// The test never calls the registrar directly.
func TestAssistantToolsSpawnThreeWorkersReadTheirHoldingsAndCloseThem(t *testing.T) {
	ctx := context.Background()
	w := newWakeStand(t)

	stop := make(chan struct{})
	wg := launcherStandIn(t, w, stop)
	defer func() { close(stop); wg.Wait() }()

	client, _, err := assistant.NewClientAndRegistry(log.NewSlogAdapter(nil), content.Floor{})
	if err != nil {
		t.Fatalf("assistant client: %v", err)
	}
	env := content.EnvironmentIDFor(content.EnvLocal, "")
	grant := autonomousWorkerGrant(string(w.coordinator), env)

	// One worker's process ends between tool calls. It is not
	// the test making a call the model should have made: the coordinator does
	// not end its own workers here, the WORK does — which is the case a read
	// of the record is for.
	go func() {
		waittest.WaitFor(t, "the first worker to be live", func() bool {
			held, herr := w.workerStore.HeldBy(ctx, string(w.coordinator))
			return herr == nil && len(held) >= 1 && held[0].State == workers.StateLive
		})
		held, _ := w.workerStore.HeldBy(ctx, string(w.coordinator))
		_ = w.reg.Close(session.ID(held[0].Liveness.SessionID))
	}()

	bound, err := assistant.BindRunTools(t.Context(), client, assistant.AskParams{
		SessionID: string(w.coordinator),
		Grant:     &grant, FreshGrant: func() *content.Grant { return &grant },
		AttemptLedger: w.db.Ledger(),
		KnownMaterial: noKnownMaterial{},
		Workers:       workerRecordForTools{w.record}, WorkerEnvironment: env,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, args string) string {
		t.Helper()
		invocation, admitErr := bound.Admit(ctx)
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		invocation.Context, invocation.Method, invocation.RawParams = ctx, method, []byte(args)
		result, dispatchErr := bound.Dispatch(invocation)
		if dispatchErr != nil {
			t.Fatalf("%s: %v", method, dispatchErr)
		}
		return result
	}
	var results []string
	for _, task := range []string{"read AGENTS.md", "read the architecture", "read the vision"} {
		results = append(results, call("workers.spawn", fmt.Sprintf(`{"command":"claude","task":%q}`, task)))
	}
	waittest.WaitFor(t, "the first worker's process to exit", func() bool {
		held, heldErr := w.workerStore.HeldBy(ctx, string(w.coordinator))
		return heldErr == nil && len(held) == 3 && held[0].State == workers.StateExited
	})
	holdings := call("workers.holdings", `{}`)
	results = append(results, holdings)
	for _, id := range liveWorkerIDs(holdings) {
		results = append(results, call("workers.close", fmt.Sprintf(`{"worker":%q}`, id)))
	}

	// THREE WORKERS EXIST, and the test never registered one.
	held, err := w.workerStore.HeldBy(ctx, string(w.coordinator))
	if err != nil {
		t.Fatalf("held by: %v", err)
	}
	if len(held) != 3 {
		t.Fatalf("tool calls produced %d workers, want 3", len(held))
	}
	tasks := map[string]bool{}
	for _, p := range held {
		tasks[p.Task] = true
	}
	for _, want := range []string{"read AGENTS.md", "read the architecture", "read the vision"} {
		if !tasks[want] {
			t.Fatalf("no worker carries the task %q; got %v", want, tasks)
		}
	}

	// Results come from the real executor, not from structs built by this test.
	if len(results) != 6 {
		t.Fatalf("the caller received %d tool results, want 6 (three spawns, holdings, two closes)", len(results))
	}
	// Each spawn told it a worker id it can address later, and told it the
	// worker is LIVE — which is the enrolment having arrived, never that the
	// call returned.
	for i, r := range results[:3] {
		if !strings.Contains(r, `"state":"live"`) {
			t.Fatalf("spawn %d did not tell the caller the worker is live: %s", i+1, r)
		}
	}
	// Holdings answered with what the session holds, and it names both the
	// worker whose process ended while the model was between calls and the
	// two that are still running — and nothing about how the work went,
	// because nocx records no outcome (ADR-0070 decision 3).
	holdingsAnswer := results[3]
	if !strings.Contains(holdingsAnswer, "participants") {
		t.Fatalf("holdings answered without participants: %s", holdingsAnswer)
	}
	if !strings.Contains(holdingsAnswer, `"state":"exited"`) {
		t.Fatalf("holdings did not report the worker that ended while the model was between calls: %s", holdingsAnswer)
	}
	if !strings.Contains(holdingsAnswer, `"state":"live"`) {
		t.Fatalf("holdings reported nothing still running: %s", holdingsAnswer)
	}
	if got := strings.Count(holdingsAnswer, `"task"`); got != 3 {
		t.Fatalf("holdings reported %d workers, want all three: %s", got, holdingsAnswer)
	}

	// EVERY WORKER IS ENDED, and the two the model closed ended because it
	// closed them: it read their ids out of the answer it was given and
	// addressed them. The state the record keeps for one of them is the
	// coordinator's own act (`closed`), not merely the exit it caused.
	for _, r := range results[4:] {
		if !strings.Contains(r, `"ended":true`) {
			t.Fatalf("a close did not report the worker ended: %s", r)
		}
	}
	for _, p := range held {
		waittest.WaitFor(t, "every worker to reach a terminal state", func() bool {
			stored, serr := w.workerStore.Participant(ctx, p.ID)
			return serr == nil && stored.State.Terminal()
		})
	}
	if len(w.reg.List()) != 1 {
		t.Fatalf("%d sessions are still open; only the coordinator's should be", len(w.reg.List()))
	}
}

// liveWorkerIDs pulls the still-running worker ids from the holdings result.
func liveWorkerIDs(last string) []string {
	open := last[strings.Index(last, "{"):]
	var answer struct {
		Participants []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"participants"`
	}
	// The content wraps the JSON in a labelled block, so it is trimmed to
	// the object rather than decoded whole.
	if end := strings.LastIndex(open, "}"); end >= 0 {
		open = open[:end+1]
	}
	if err := json.Unmarshal([]byte(open), &answer); err != nil {
		return nil
	}
	var out []string
	for _, p := range answer.Participants {
		if p.State == string(workers.StateLive) {
			out = append(out, p.ID)
		}
	}
	return out
}

// noKnownMaterial is the egress comparison for a stand with no vault: it
// knows no secrets, so it matches none. It is not a bypass — the screen still
// runs on every result and still finds nothing, which is what a machine with
// an empty vault genuinely answers.
type noKnownMaterial struct{}

func (noKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, nil
}

// autonomousWorkerGrant mints the authority a coordinator run holds: every
// effect permitted, fenced to its own session and to the local environment.
// It is minted through EffectPolicy.AsGrant rather than written as a literal,
// so the deadline is stamped the way production stamps it.
func autonomousWorkerGrant(sessionID, env string) content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	policy := content.EffectPolicy{
		Observe: permit, MutateReversible: permit, MutateDestructive: permit,
		PrivilegeChange: permit, Disclose: permit, CrossBoundary: permit, Delegate: permit,
	}
	return policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: sessionID},
		{Kind: content.ResourceEnvironment, ID: env},
	})
}
