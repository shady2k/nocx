package assistant

// workers.close's changed result against its contract (nocx-xn63t.1.3).
//
// The close now answers what is left of the worker's own checkout. The
// checks here are AGENTS.md rule 5's halves for that answer: the DTO the
// executor marshals must satisfy the schema's own result shape — WITH the
// checkout present, and with it absent — and the real result off the real
// dispatcher must satisfy it too, because a payload the test built for
// itself proves nothing about what the wire actually carries.
//
// The unknown reading gets its own check: uncommitted and ahead must be
// ABSENT, not false and zero, because "could not read" and "clean" are the
// two answers this surface must not confuse.

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

func workerCloseResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/tools/workers.close.schema.json")
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
	if addErr := compiler.AddResource("workers.close.result.json", resource); addErr != nil {
		t.Fatalf("add the result schema: %v", addErr)
	}
	schema, err := compiler.Compile("workers.close.result.json")
	if err != nil {
		t.Fatalf("compile the result schema: %v", err)
	}
	return schema
}

// Criterion: a close whose worker HAD a checkout marshals to something the
// contract accepts — the reading riding only when it was read.
func TestWorkerCloseWorktreeDTOConformsToContract(t *testing.T) {
	t.Run("a read checkout", func(t *testing.T) {
		rec := &fakeWorkerRecord{closeResult: workers.CloseResult{Worktree: workers.Leftover{
			Path: "/wt/nocx-feat", Branch: "feat/one", State: workers.CheckoutRead,
			Uncommitted: true, Ahead: 2,
		}}}
		out, err := executeWorkerClose(context.Background(),
			testCoordinator("sess-coordinator"),
			json.RawMessage(`{"worker":"p-1"}`), workerSeams(rec))
		if err != nil {
			t.Fatalf("workers.close: %v", err)
		}
		var value any
		if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
			t.Fatalf("decode result: %v", unmarshalErr)
		}
		if validateErr := workerCloseResultSchema(t).Validate(value); validateErr != nil {
			t.Fatalf("workers.close result does not satisfy its contract: %v\npayload: %s", validateErr, out)
		}
	})

	t.Run("a checkout nobody could read", func(t *testing.T) {
		rec := &fakeWorkerRecord{closeResult: workers.CloseResult{Worktree: workers.Leftover{
			Path: "/wt/nocx-feat", Branch: "feat/one", State: workers.CheckoutUnknown,
		}}}
		out, err := executeWorkerClose(context.Background(),
			testCoordinator("sess-coordinator"),
			json.RawMessage(`{"worker":"p-1"}`), workerSeams(rec))
		if err != nil {
			t.Fatalf("workers.close: %v", err)
		}
		var decoded struct {
			Worktree map[string]any `json:"worktree"`
		}
		if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
			t.Fatalf("decode result: %v", unmarshalErr)
		}
		if decoded.Worktree["state"] != "unknown" {
			t.Fatalf("state = %v, want unknown", decoded.Worktree["state"])
		}
		// The values that were never read must not be on the wire at all:
		// their zero forms are answers, and nobody answered.
		for _, key := range []string{"uncommitted", "ahead"} {
			if _, present := decoded.Worktree[key]; present {
				t.Fatalf("an unread checkout answered %q — unknown must not look like false or zero", key)
			}
		}
		var value any
		if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
			t.Fatalf("decode result: %v", unmarshalErr)
		}
		if validateErr := workerCloseResultSchema(t).Validate(value); validateErr != nil {
			t.Fatalf("workers.close result does not satisfy its contract: %v\npayload: %s", validateErr, out)
		}
	})
}

// Criterion: closing a worker with no worktree answers no worktree field at
// all, and the result is otherwise as it was — id and ended, nothing else.
func TestWorkerCloseWithoutAWorktreeOmitsTheField(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerClose(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"worker":"p-1"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.close: %v", err)
	}
	var decoded map[string]any
	if unmarshalErr := json.Unmarshal([]byte(out), &decoded); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if _, present := decoded["worktree"]; present {
		t.Fatalf("a worker with no checkout answered %v — the field must be absent, not zero", decoded["worktree"])
	}
	if decoded["id"] != "p-1" || decoded["ended"] != true {
		t.Fatalf("result = %v, want the plain id/ended answer", decoded)
	}
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := workerCloseResultSchema(t).Validate(value); validateErr != nil {
		t.Fatalf("workers.close result does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}

// Criterion, the third check: the REAL result off the REAL dispatcher —
// schema validation, the narrow, the executor map — satisfies the contract
// when a coordinator closes a worker that had a checkout.
func TestWorkerClose_OverTheWireConformsToContract(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	rec := &fakeWorkerRecord{closeResult: workers.CloseResult{Worktree: workers.Leftover{
		Path: "/wt/nocx-feat", Branch: "feat/one", State: workers.CheckoutRead,
		Uncommitted: false, Ahead: 3,
	}}}
	dispatcher, err := NewToolDispatcher(reg, rec, content.EnvironmentIDFor(content.EnvLocal, ""))
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	invocation := ToolInvocation{
		Context: context.Background(),
		Method:  "workers.close",
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "sess-coordinator",
		},
		Grant:     workerGrantForTest(),
		RawParams: json.RawMessage(`{"worker":"p-1"}`),
	}
	out, dispatchErr := dispatcher.Dispatch(invocation)
	if dispatchErr != nil {
		t.Fatalf("dispatch workers.close: %v", dispatchErr)
	}
	var value any
	if unmarshalErr := json.Unmarshal([]byte(out), &value); unmarshalErr != nil {
		t.Fatalf("decode result: %v", unmarshalErr)
	}
	if validateErr := workerCloseResultSchema(t).Validate(value); validateErr != nil {
		t.Fatalf("workers.close result off the real dispatcher does not satisfy its contract: %v\npayload: %s", validateErr, out)
	}
}
