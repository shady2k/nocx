package assistant

// workers.answer's executor (nocx-f545a.4): it passes the named option through
// untouched, reports the gate's outcome as a result — a refusal included — and
// passes the record's own refusals through so the endpoint's ownership and
// delegation sentences apply.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
