package assistant

// The quiet bound's question and its continuation, at the tool layer
// (nocx-6dzxq). The lease itself lives in the transport; what is proven here
// is what the MODEL is handed and what it can say back — the ask for a
// shorter bound, the clamp being stated rather than silent, the two
// decisions, and the sentence the still-running answer produces.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// waitingRunner is recordingRunner plus the continuation seam and a record
// of the quiet bound each call asked for.
type waitingRunner struct {
	unscriptedBlocks

	mu        sync.Mutex
	askedQuit []time.Duration
	waits     []waitedRun
	body      json.RawMessage
	runErr    error
	waitBody  json.RawMessage
	waitErr   error
}

type waitedRun struct {
	runID    string
	decision RunDecision
	quiet    time.Duration
}

func (r *waitingRunner) RequestScreen(context.Context, string, *FrameRegion) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (r *waitingRunner) RequestRun(ctx context.Context, _ string, _ string) (json.RawMessage, error) {
	r.mu.Lock()
	r.askedQuit = append(r.askedQuit, RunQuietBoundFromContext(ctx))
	r.mu.Unlock()
	if r.runErr != nil {
		return nil, r.runErr
	}
	return r.body, nil
}

func (r *waitingRunner) RequestRunWait(ctx context.Context, runID string, decision RunDecision) (json.RawMessage, error) {
	r.mu.Lock()
	r.waits = append(r.waits, waitedRun{runID: runID, decision: decision, quiet: RunQuietBoundFromContext(ctx)})
	r.mu.Unlock()
	if r.waitErr != nil {
		return nil, r.waitErr
	}
	return r.waitBody, nil
}

func (r *waitingRunner) asked() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.askedQuit...)
}

func (r *waitingRunner) waited() []waitedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]waitedRun(nil), r.waits...)
}

func sessionRunner(t *testing.T) *agenttools.Runner {
	t.Helper()
	return agenttools.NewRunner([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
}

func sessionWatcher(t *testing.T) *agenttools.RunWatcher {
	t.Helper()
	return agenttools.NewRunWatcher([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
}

// A call that asks for its own quiet bound gets it carried to the lease. The
// clamp is not this layer's job and is deliberately not performed here —
// there is exactly one clamp and it is in the transport.
func TestExecuteRun_CarriesTheCallsOwnQuietBoundToTheLease(t *testing.T) {
	req := &waitingRunner{body: runResolvedBody("entry-1", new(0), "success", 1, 0, 1, "hi")}
	if _, err := executeRun(toolTestContext(), sessionRunner(t), req,
		json.RawMessage(`{"command":"ls","quietSeconds":45}`), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := req.asked(); len(got) != 1 || got[0] != 45*time.Second {
		t.Fatalf("quiet bound carried = %v, want one call asking for 45s", got)
	}
}

// THE UNSTATED PATH IS UNCHANGED: a call that asks for nothing carries
// nothing, and the person's setting applies alone.
func TestExecuteRun_WithNoAskCarriesNoBoundAtAll(t *testing.T) {
	req := &waitingRunner{body: runResolvedBody("entry-1", new(0), "success", 1, 0, 1, "hi")}
	if _, err := executeRun(toolTestContext(), sessionRunner(t), req,
		json.RawMessage(`{"command":"ls"}`), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := req.asked(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("quiet bound carried = %v, want one call asking for nothing", got)
	}
}

// The bounds a run was actually held to reach the model on the result, and a
// clamp says both that it happened and what it was cut from.
func TestExecuteRun_ResultStatesTheBoundsAndTheClamp(t *testing.T) {
	body := map[string]any{
		"entryId": "entry-1", "exitCode": 0, "status": "success", "stopped": false,
		"total": 1, "start": 0, "end": 1, "text": "hi",
		"leaseBounds": map[string]any{
			"quietSeconds": 600, "wallClockSeconds": 600, "clamped": true, "askedSeconds": 2700,
		},
	}
	raw, _ := json.Marshal(body)
	req := &waitingRunner{body: raw}
	out, err := executeRun(toolTestContext(), sessionRunner(t), req,
		json.RawMessage(`{"command":"ls","quietSeconds":2700}`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var res runResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.Bounds == nil {
		t.Fatal("the result carries no bounds: the model cannot see what it is held to")
	}
	if !res.Bounds.Clamped {
		t.Fatal("the clamp was silent in the result")
	}
	if res.Bounds.QuietSeconds != 600 || res.Bounds.AskedSeconds != 2700 {
		t.Fatalf("bounds = %+v, want the person's 600s and the asked 2700s", *res.Bounds)
	}
}

// A call asking BELOW the person's bound gets what it asked for, and the
// result says so without claiming a clamp. The paired half of the test above
// (AGENTS.md rule 3).
func TestExecuteRun_AnAskBelowTheCeilingIsReportedUnclamped(t *testing.T) {
	body := map[string]any{
		"entryId": "entry-1", "exitCode": 0, "status": "success", "stopped": false,
		"total": 1, "start": 0, "end": 1, "text": "hi",
		"leaseBounds": map[string]any{"quietSeconds": 45, "wallClockSeconds": 600},
	}
	raw, _ := json.Marshal(body)
	req := &waitingRunner{body: raw}
	out, err := executeRun(toolTestContext(), sessionRunner(t), req,
		json.RawMessage(`{"command":"ls","quietSeconds":45}`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var res runResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.Bounds == nil || res.Bounds.Clamped || res.Bounds.QuietSeconds != 45 {
		t.Fatalf("bounds = %+v, want the asked 45s honoured and no clamp", res.Bounds)
	}
}

// The continuation names the run it is answering about and carries the
// decision the model chose.
func TestExecuteRunWait_KeepWaitingReattachesToTheNamedRun(t *testing.T) {
	req := &waitingRunner{waitBody: runResolvedBody("entry-1", new(0), "success", 1, 0, 1, "done")}
	out, err := executeRunWait(toolTestContext(), sessionWatcher(t), req,
		json.RawMessage(`{"runId":"abc123","decision":"continue","quietSeconds":30}`))
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	got := req.waited()
	if len(got) != 1 || got[0].runID != "abc123" || got[0].decision != RunKeepWaiting || got[0].quiet != 30*time.Second {
		t.Fatalf("continuation asked %+v, want one continue on abc123 asking for 30s", got)
	}
	var res runResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.Text != "done" || res.Status != "success" {
		t.Fatalf("result = %+v, want the command's own completion", res)
	}
}

func TestExecuteRunWait_StopIsAnExplicitDecisionAndItsFailureIsTheOutcome(t *testing.T) {
	req := &waitingRunner{waitErr: &RunLeaseError{Reason: content.TermAgentDeclined, Err: context.Canceled}}
	_, err := executeRunWait(toolTestContext(), sessionWatcher(t), req,
		json.RawMessage(`{"runId":"abc123","decision":"stop"}`))
	var leaseErr *RunLeaseError
	if !errors.As(err, &leaseErr) || leaseErr.Reason != content.TermAgentDeclined {
		t.Fatalf("stop returned %v, want the agent-declined lease outcome", err)
	}
	got := req.waited()
	if len(got) != 1 || got[0].decision != RunStop {
		t.Fatalf("continuation asked %+v, want one stop", got)
	}
}

func TestExecuteRunWait_RefusesAnAnswerThatNamesNoRun(t *testing.T) {
	req := &waitingRunner{}
	if _, err := executeRunWait(toolTestContext(), sessionWatcher(t), req,
		json.RawMessage(`{"runId":"","decision":"continue"}`)); err == nil {
		t.Fatal("a continuation naming no run was accepted")
	}
	if len(req.waited()) != 0 {
		t.Fatal("the renderer was asked about a run the call never named")
	}
}

// A requester with no parked run to continue says so rather than pretending.
func TestExecuteRunWait_ARequesterWithNoContinuationSaysSo(t *testing.T) {
	plain := &recordingRunner{}
	_, err := executeRunWait(toolTestContext(), sessionWatcher(t), plain,
		json.RawMessage(`{"runId":"abc","decision":"continue"}`))
	if err == nil || !strings.Contains(err.Error(), "no waiting command") {
		t.Fatalf("err = %v, want an honest refusal from a requester that arms no lease", err)
	}
}

// The sentence names the bound, its value and the continuation — and it says
// how much of the person's ceiling is left, which is what makes "keep
// waiting" a decision rather than a reflex. Asserted on the string.
func TestRunStillRunningSentence_StatesTheClampAndTheRenewalCount(t *testing.T) {
	got := RunStillRunningSentence("session.run", &RunStillRunningError{
		RunID:       "deadbeef",
		Quiet:       10 * time.Minute,
		Remaining:   90 * time.Second,
		Renewals:    2,
		ClampedFrom: 45 * time.Minute,
	})
	for _, want := range []string{
		"deadbeef",
		"10 minutes",
		"1 minute 30 seconds",
		"already chosen to keep waiting 2 times",
		"You asked for a quiet bound of 45 minutes",
		"the person's limit is 10 minutes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the sentence does not say %q:\n%s", want, got)
		}
	}
	// And it never proposes waking anybody: the ladder is nocx→model, and
	// who the model consults afterwards is the model's business.
	if strings.Contains(strings.ToLower(got), "ask the person") {
		t.Fatalf("the sentence instructs the model to wake somebody:\n%s", got)
	}
}

// ── the contract (AGENTS.md rule 5) ───────────────────────────────────────

// THE REAL RESULT, OFF THE REAL EXECUTOR, against the schema the model was
// shown — not a payload the test built. `session.run` grew a `bounds` object
// and `session.wait` is a new contract document; both are held to the same
// check the kernel applies at its one seam (effectKernel.checkResult), so a
// declaration that lies about its return shape fails here.
func TestToolResults_ConformToTheirContracts(t *testing.T) {
	reg, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	validate := func(name, out string) {
		t.Helper()
		decl, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the registry", name)
		}
		sch, schemaErr := compileResultSchema(decl)
		if schemaErr != nil {
			t.Fatalf("%s result schema: %v", name, schemaErr)
		}
		var doc any
		dec := json.NewDecoder(strings.NewReader(out))
		dec.UseNumber()
		if decodeErr := dec.Decode(&doc); decodeErr != nil {
			t.Fatalf("%s result does not parse: %v", name, decodeErr)
		}
		if validateErr := sch.Validate(doc); validateErr != nil {
			t.Fatalf("%s result does not conform to its contract: %v\n%s", name, validateErr, out)
		}
	}

	body := map[string]any{
		"entryId": "entry-1", "exitCode": 0, "status": "success", "stopped": false,
		"total": 1, "start": 0, "end": 1, "text": "hi",
		"leaseBounds": map[string]any{
			"quietSeconds": 600, "wallClockSeconds": 600, "clamped": true, "askedSeconds": 2700,
		},
	}
	raw, _ := json.Marshal(body)

	runOut, err := executeRun(toolTestContext(), sessionRunner(t), &waitingRunner{body: raw},
		json.RawMessage(`{"command":"ls","quietSeconds":2700}`), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	validate("session.run", runOut)

	waitOut, err := executeRunWait(toolTestContext(), sessionWatcher(t), &waitingRunner{waitBody: raw},
		json.RawMessage(`{"runId":"abc123","decision":"continue"}`))
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	validate("session.wait", waitOut)
}

// The params the model may send are the ones the schema declares — and the
// schema is the file the model was SHOWN, so a call asking for a quiet bound
// is accepted and a call inventing a field is not.
func TestSessionWaitParams_AreExactlyWhatTheContractDeclares(t *testing.T) {
	reg, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	decl, ok := reg.Lookup("session.wait")
	if !ok {
		t.Fatal("session.wait is not in the registry")
	}
	sch, err := compileToolSchema(decl)
	if err != nil {
		t.Fatalf("params schema: %v", err)
	}
	accept := func(args string) error {
		var doc any
		dec := json.NewDecoder(strings.NewReader(args))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return err
		}
		return sch.Validate(doc)
	}
	if err := accept(`{"runId":"abc","decision":"continue","quietSeconds":30}`); err != nil {
		t.Fatalf("a well-formed continuation was refused: %v", err)
	}
	if err := accept(`{"runId":"abc","decision":"maybe"}`); err == nil {
		t.Fatal("a third decision was accepted; there are exactly two answers")
	}
	if err := accept(`{"decision":"continue"}`); err == nil {
		t.Fatal("a continuation naming no run was accepted")
	}
	if err := accept(`{"runId":"abc","decision":"continue","command":"rm -rf /"}`); err == nil {
		t.Fatal("session.wait accepted a command; it carries none, which is why it is a separate tool")
	}
}

// A COMMAND CAN FINISH BETWEEN THE QUESTION AND THE ANSWER, which is a race
// the model cannot win and did nothing wrong by losing. The continuation
// surfaces a typed answer the kernel can recognise, so the turn carries on
// instead of dying over it — the same shape a refusal and a lease bound
// already have.
func TestExecuteRunWait_ALateContinuationIsATypedAnswer(t *testing.T) {
	req := &waitingRunner{waitErr: fmt.Errorf("run: %w", ErrRunNotWaiting)}
	_, err := executeRunWait(toolTestContext(), sessionWatcher(t), req,
		json.RawMessage(`{"runId":"gone","decision":"continue"}`))
	if !errors.Is(err, ErrRunNotWaiting) {
		t.Fatalf("err = %v, want it to carry ErrRunNotWaiting so the kernel can answer rather than fail the run", err)
	}
}

// And what the model is handed says the call did nothing, so it cannot read
// the outcome as a command that returned empty.
func TestRunNotWaitingResult_SaysNothingHappenedAndNotToRepeatIt(t *testing.T) {
	got := runNotWaitingResult("session.wait")
	for _, want := range []string{
		"NOT WAITING",
		"session.wait",
		"no longer waiting",
		"Nothing was changed by this call",
		"Do not answer about it again",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the sentence does not say %q:\n%s", want, got)
		}
	}
}
