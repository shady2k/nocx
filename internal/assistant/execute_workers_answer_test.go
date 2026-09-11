package assistant

// workers.answer's executor (nocx-f545a.4): it passes the named option through
// untouched, reports the gate's outcome as a result — a refusal included — and
// passes the record's own refusals through so the endpoint's ownership and
// delegation sentences apply.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/workers"
)

func answerWith(t *testing.T, rec *fakeWorkerRecord, params string) (map[string]any, error) {
	t.Helper()
	out, err := executeWorkerAnswer(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(params), workerSeams(rec))
	if err != nil {
		return nil, err
	}
	var got map[string]any
	if uerr := json.Unmarshal([]byte(out), &got); uerr != nil {
		t.Fatalf("result: %v (%s)", uerr, out)
	}
	return got, nil
}

func TestWorkerAnswerPassesTheNamedOptionAndReportsTheOutcome(t *testing.T) {
	rec := &fakeWorkerRecord{answer: workers.PaneAnswer{Outcome: "submitted", State: "permission_choice"}}
	got, err := answerWith(t, rec, `{"worker":"p-7","option":"Yes, I trust this folder"}`)
	if err != nil {
		t.Fatalf("executeWorkerAnswer: %v", err)
	}
	if len(rec.answeredWith) != 1 || rec.answeredWith[0] != "Yes, I trust this folder" {
		t.Fatalf("answered with %q, want the option exactly as named", rec.answeredWith)
	}
	if got["worker"] != "p-7" || got["outcome"] != "submitted" || got["state"] != "permission_choice" {
		t.Fatalf("result = %v", got)
	}
}

// A refusal at the gate is a RESULT: the model is told nothing was written and
// why, rather than being handed an error for a pane nocx read correctly.
func TestWorkerAnswerReportsAGateRefusalAsAResult(t *testing.T) {
	rec := &fakeWorkerRecord{answer: workers.PaneAnswer{
		Outcome: "refused", State: "free_text", Reason: "that pane is waiting for input, and nocx answers only a menu",
	}}
	got, err := answerWith(t, rec, `{"worker":"p-7","option":"Yes"}`)
	if err != nil {
		t.Fatalf("a gate refusal became an error: %v", err)
	}
	if got["outcome"] != "refused" || got["reason"] == "" {
		t.Fatalf("result = %v, want refused with its reason", got)
	}
}

func TestWorkerAnswerPassesTheRecordsRefusalThrough(t *testing.T) {
	rec := &fakeWorkerRecord{answerErr: workers.ErrNotDelegated}
	if _, err := answerWith(t, rec, `{"worker":"p-7","option":"Yes"}`); !errors.Is(err, workers.ErrNotDelegated) {
		t.Fatalf("err = %v, want the record's ErrNotDelegated unchanged", err)
	}
}

func TestWorkerAnswerNeedsAWorkerAndAnOption(t *testing.T) {
	for _, params := range []string{`{"worker":"p-7"}`, `{"option":"Yes"}`} {
		rec := &fakeWorkerRecord{}
		if _, err := answerWith(t, rec, params); err == nil {
			t.Fatalf("%s was answered", params)
		}
		if len(rec.answeredWith) != 0 {
			t.Fatalf("%s still reached the record", params)
		}
	}
}

// A task outcome the record reports (nocx-f545a.7) reaches the wire in the
// wire's own vocabulary — never dropped, never renamed.
func TestWorkerAnswerPassesTheTaskObjectThrough(t *testing.T) {
	rec := &fakeWorkerRecord{answer: workers.PaneAnswer{
		Outcome: "submitted", State: "permission_choice",
		Task: &workers.TaskOutcome{Delivery: "typed"},
	}}
	got, err := answerWith(t, rec, `{"worker":"p-7","option":"Yes, I trust this folder"}`)
	if err != nil {
		t.Fatalf("executeWorkerAnswer: %v", err)
	}
	task, ok := got["task"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v, want a task object", got)
	}
	if task["delivery"] != "typed" {
		t.Fatalf("task = %v, want delivery typed", task)
	}
}

// A task the record reports as still WAITING carries its state and no
// reason, and an answer nothing was owed for carries no task field at all —
// the same distinction the record's own PaneAnswer.Task doc draws.
func TestWorkerAnswerPassesAWaitingTaskWithItsState(t *testing.T) {
	rec := &fakeWorkerRecord{answer: workers.PaneAnswer{
		Outcome: "submitted", State: "permission_choice",
		Task: &workers.TaskOutcome{Delivery: "waiting", State: "modal_choice"},
	}}
	got, err := answerWith(t, rec, `{"worker":"p-7","option":"No, exit"}`)
	if err != nil {
		t.Fatalf("executeWorkerAnswer: %v", err)
	}
	task, ok := got["task"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v, want a task object", got)
	}
	if task["delivery"] != "waiting" || task["state"] != "modal_choice" {
		t.Fatalf("task = %v, want delivery waiting with state modal_choice", task)
	}
}

func TestWorkerAnswerReportsNoTaskFieldWhenNoneWasOwed(t *testing.T) {
	rec := &fakeWorkerRecord{answer: workers.PaneAnswer{Outcome: "submitted", State: "permission_choice"}}
	got, err := answerWith(t, rec, `{"worker":"p-7","option":"Yes"}`)
	if err != nil {
		t.Fatalf("executeWorkerAnswer: %v", err)
	}
	if _, ok := got["task"]; ok {
		t.Fatalf("result = %v, want no task field for an answer that owed nothing", got)
	}
}

// TestWorkerAnswerDTOConformsToContract is the Go-struct half of AGENTS.md's
// testing rule 5 for workers.answer's task field: workerAnswerResult, filled
// with a task object, marshals to something
// contracts/tools/workers.answer.schema.json's own result shape accepts.
// internal/toolendpoint's contract_test.go covers the other half — the real
// result, off the real wire.
func TestWorkerAnswerDTOConformsToContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/tools/workers.answer.schema.json")
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
	if addErr := compiler.AddResource("workers.answer.result.json", resource); addErr != nil {
		t.Fatalf("add the result schema: %v", addErr)
	}
	schema, err := compiler.Compile("workers.answer.result.json")
	if err != nil {
		t.Fatalf("compile the result schema: %v", err)
	}
	encoded, err := json.Marshal(workerAnswerResult{
		Worker: "p-7", Outcome: "submitted", State: "permission_choice",
		Task: &workerAnswerTaskResult{Delivery: "typed"},
	})
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatalf("unmarshal DTO: %v", err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("DTO does not conform: %v", err)
	}
}
