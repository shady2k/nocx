package assistant

// The worktree ask and answer on workers.spawn's own seam (nocx-xn63t.1.2).
//
// The executor's whole job for the new param is CARRYING and COPYING: the
// ask travels to the record as asked (branch required, base optional), and
// the answer is read back from the record's Worktree — the fact the record
// accepted at MarkLive — never re-derived here. The refusals that matter
// (no repository, a branch held elsewhere, an occupied path) are the git
// seam's own, surfaced through the register error; what this file adds is
// the one shape check a caller that bypassed the schema could break, and
// the wire proof that the result satisfies the contract both with the
// checkout present and absent.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

// Criterion: the ask travels to the record AS ASKED — the branch and the
// base the caller named, resolved by nobody on the way.
func TestWorkerSpawnCarriesTheWorktreeAskToTheRecord(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it","worktree":{"branch":"feat/worker-worktree","base":"4f2a1c9"}}`),
		workerSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(rec.registered) != 1 {
		t.Fatalf("record saw %d registrations, want 1", len(rec.registered))
	}
	ask := rec.registered[0].Worktree
	if ask == nil {
		t.Fatal("the record was handed no worktree ask")
	}
	if ask.Branch != "feat/worker-worktree" || ask.Base != "4f2a1c9" {
		t.Fatalf("ask = %+v, want it unchanged", *ask)
	}
	// And the result of a record that answers no checkout carries none —
	// the ask alone does not invent facts.
	if strings.Contains(out, "worktree") {
		t.Fatalf("the result names a worktree the record never accepted: %s", out)
	}
}

// Criterion: a spawn without the ask is today's wire — nothing named
// worktree reaches the record or the result.
func TestWorkerSpawnWithoutAWorktreeAskIsTodaysWire(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.registered[0].Worktree != nil {
		t.Fatalf("the record was handed an ask nobody made: %+v", rec.registered[0].Worktree)
	}
	if strings.Contains(out, "worktree") {
		t.Fatalf("the result grew a worktree nobody asked for: %s", out)
	}
}

// Criterion: the result's checkout is the RECORD's, copied once: path,
// branch, and the resolved base — present only when the record accepted
// one.
func TestWorkerSpawnResultCarriesTheRecordedWorktree(t *testing.T) {
	rec := &fakeWorkerRecord{
		registerFn: func(workers.RegisterRequest) (workers.Participant, error) {
			return workers.Participant{
				ID: "p-1", State: workers.StateLive,
				Worktree: workers.Worktree{Path: "/wt/nocx-feat", Branch: "feat/one", Base: "4f2a1c9"},
			}, nil
		},
	}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it","worktree":{"branch":"feat/one"}}`),
		workerSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var got workerSpawnResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if got.Worktree == nil {
		t.Fatalf("the result carries no checkout: %s", out)
	}
	if got.Worktree.Path != "/wt/nocx-feat" || got.Worktree.Branch != "feat/one" || got.Worktree.Base != "4f2a1c9" {
		t.Fatalf("worktree = %+v, want the record's own", *got.Worktree)
	}
}

// Criterion: the one shape check the executor owns — an ask with no branch
// is refused before the record is reached, because the schema cannot help a
// caller that bypassed it.
func TestWorkerSpawnRefusesAWorktreeWithNoBranch(t *testing.T) {
	rec := &fakeWorkerRecord{}
	_, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it","worktree":{}}`),
		workerSeams(rec))
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("err = %v, want the unnamed-branch refusal", err)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("a refused ask reached the record: %+v", rec.registered)
	}
}

// ── the contract (AGENTS.md rule 5) ──────────────────────────────────────

func workerSpawnResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/workers.spawn.schema.json")
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if unmarshalErr := json.Unmarshal(raw, &doc); unmarshalErr != nil {
		t.Fatalf("parse the contract: %v", unmarshalErr)
	}
	compiler := jsonschema.NewCompiler()
	resource, err := jsonschema.UnmarshalJSON(strings.NewReader(string(doc.Defs["result"])))
	if err != nil {
		t.Fatalf("read $defs/result: %v", err)
	}
	if addErr := compiler.AddResource("workers.spawn.result.json", resource); addErr != nil {
		t.Fatalf("add the result schema: %v", addErr)
	}
	schema, err := compiler.Compile("workers.spawn.result.json")
	if err != nil {
		t.Fatalf("compile the result schema: %v", err)
	}
	return schema
}

// Criterion: the executor's result, WITH a checkout, marshals to something
// the contract's own result shape accepts — and the checkout really rides
// it, so the check is about a shape that happened rather than an absence.
func TestWorkerSpawnWorktreeDTOConformsToContract(t *testing.T) {
	rec := &fakeWorkerRecord{
		registerFn: func(workers.RegisterRequest) (workers.Participant, error) {
			return workers.Participant{
				ID: "p-1", State: workers.StateLive,
				Worktree: workers.Worktree{Path: "/wt/nocx-feat", Branch: "feat/one", Base: "4f2a1c9"},
			}, nil
		},
	}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it","worktree":{"branch":"feat/one"}}`),
		workerSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := workerSpawnResultSchema(t).Validate(value); validateErr != nil {
		t.Fatalf("workers.spawn result does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}

// Criterion: the OPTIONAL half stays optional — a plain spawn's result
// satisfies the same contract with no worktree key at all.
func TestWorkerSpawnPlainDTOConformsToContract(t *testing.T) {
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(&fakeWorkerRecord{}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := workerSpawnResultSchema(t).Validate(value); validateErr != nil {
		t.Fatalf("workers.spawn result does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}

// Criterion, the third check: the REAL result off the REAL dispatcher — the
// schema validation, the narrow, the executor map — satisfies the contract
// when a coordinator asks for a worktree and gets one. This is the check a
// payload the test built for itself cannot make.
func TestWorkerSpawn_OverTheWireConformsToContract(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	rec := &fakeWorkerRecord{
		registerFn: func(workers.RegisterRequest) (workers.Participant, error) {
			return workers.Participant{
				ID: "p-1", State: workers.StateLive,
				Worktree: workers.Worktree{Path: "/wt/nocx-feat", Branch: "feat/one", Base: "4f2a1c9"},
			}, nil
		},
	}
	dispatcher, err := NewToolDispatcher(reg, rec, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	invocation := ToolInvocation{
		Context: context.Background(),
		Method:  "workers.spawn",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "sess-coordinator",
		},
		Grant: workerGrantForTest(),
		RawParams: json.RawMessage(
			`{"command":"claude","task":"read it","worktree":{"branch":"feat/one"}}`),
	}
	out, dispatchErr := dispatcher.Dispatch(invocation)
	if dispatchErr != nil {
		t.Fatalf("dispatch workers.spawn: %v", dispatchErr)
	}
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := workerSpawnResultSchema(t).Validate(value); validateErr != nil {
		t.Fatalf("workers.spawn result off the real dispatcher does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}

// Criterion: on the wire, the schema itself refuses an ask with no branch
// and an ask with an unknown key — both before any executor or record runs,
// which is what makes the executor's own check above a backstop and not the
// boundary.
func TestTheSpawnSchemaRefusesAnUnnamedBranchAndUnknownKeys(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	rec := &fakeWorkerRecord{}
	dispatcher, err := NewToolDispatcher(reg, rec, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	for name, params := range map[string]string{
		"no branch":    `{"command":"claude","task":"t","worktree":{}}`,
		"unknown key":  `{"command":"claude","task":"t","worktree":{"branch":"b","path":"/etc"}}`,
		"branch empty": `{"command":"claude","task":"t","worktree":{"branch":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, dispatchErr := dispatcher.Dispatch(ToolInvocation{
				Context: context.Background(),
				Method:  "workers.spawn",
				RunContext: agenttools.RunContext{
					RunID: "run-1", Session: "sess-coordinator",
				},
				Grant:     workerGrantForTest(),
				RawParams: json.RawMessage(params),
			})
			if !strings.Contains(dispatchErr.Error(), "invalid") && !strings.Contains(dispatchErr.Error(), "Invalid") {
				t.Fatalf("dispatch(%s) err = %v, want invalid params", name, dispatchErr)
			}
			if len(rec.registered) != 0 {
				t.Fatalf("invalid params reached the record: %+v", rec.registered)
			}
		})
	}
}
