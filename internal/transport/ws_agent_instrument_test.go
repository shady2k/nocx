package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/log"
)

type instrumentLogRecord struct {
	message string
	args    []any
}

type instrumentLogger struct {
	record instrumentLogRecord
}

func (l *instrumentLogger) Debug(string, ...any) {}
func (l *instrumentLogger) Info(message string, args ...any) {
	l.record = instrumentLogRecord{message: message, args: args}
}
func (l *instrumentLogger) Warn(string, ...any)                      {}
func (l *instrumentLogger) Error(string, ...any)                     {}
func (l *instrumentLogger) With(...any) log.Logger                   { return l }
func (l *instrumentLogger) WithContext(_ context.Context) log.Logger { return l }

func TestLogAgentStreamEnded_CarriesTheRunsOwnCounts(t *testing.T) {
	logger := &instrumentLogger{}
	logAgentStreamEnded(logger, 41, time.Now(), 3, 12, 1, 0, nil)
	if logger.record.message != "agent ask: the stream ended" {
		t.Fatalf("message = %q", logger.record.message)
	}
	want := map[string]any{
		"run":                int64(41),
		"reasoning":          3,
		"answer":             12,
		"toolCallsRequested": 1,
		"toolCallsExecuted":  0,
	}
	got := make(map[string]any)
	for i := 0; i < len(logger.record.args); i += 2 {
		key, ok := logger.record.args[i].(string)
		if !ok {
			t.Fatalf("argument %d key = %#v, want string", i, logger.record.args[i])
		}
		got[key] = logger.record.args[i+1]
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("field %q = %#v, want %#v; all fields %#v", key, got[key], value, got)
		}
	}
}

func TestAgentAsk_SealFailureDoesNotCountToolAsExecuted(t *testing.T) {
	svc := &fakeAgentService{sealErr: errors.New("seal failed")}
	logger := &instrumentLogger{}
	h := newGapHandlers(svc, &toolCallScript{events: []assistant.AskEvent{
		answerEvent("before"),
		callEvent("call-1", "files.read", "action-1"),
	}}, assistant.NewApprovalStore())
	h.log = logger

	h.runAskStream(context.Background(), gapRunContext(), &gapResponder{})

	if logger.record.message != "agent ask: the stream ended" {
		t.Fatalf("message = %q, want stream-ended record", logger.record.message)
	}
	values := make(map[string]any)
	for i := 0; i < len(logger.record.args); i += 2 {
		key, ok := logger.record.args[i].(string)
		if !ok {
			t.Fatalf("argument %d key = %#v, want string", i, logger.record.args[i])
		}
		values[key] = logger.record.args[i+1]
	}
	if values["toolCallsRequested"] != 1 || values["toolCallsExecuted"] != 0 {
		t.Fatalf("tool counts = requested %v executed %v, want 1/0 after seal failure",
			values["toolCallsRequested"], values["toolCallsExecuted"])
	}
}

// TestAuthorisedRun_LogsOneSegmentForTheAnsweredCall replaces the suspended
// and resumed pair this check used to assert (nocx-a62cl.3.1). The pair was the
// OLD shape's fingerprint: the question ended the drive ("suspended", executed
// 0) and the answer started a second one ("resumed", executed 1). With the
// approval service the call blocks on the answer and runs in the SAME drive, so
// there is one segment — and the property worth keeping is the one the pair was
// there to prove: the call was requested and executed exactly once, and the log
// says so without a suspension it did not have.
func TestAuthorisedRun_LogsOneSegmentForTheAnsweredCall(t *testing.T) {
	h, _, _, _ := driveOneAuthorisedRun(t)
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if record["msg"] == "agent ask: the stream ended" {
			records = append(records, record)
		}
	}
	if len(records) != 1 {
		t.Fatalf("stream-ended records = %d, want one — the answer reaches the call that asked, so the drive never ends at the question: %s", len(records), h.logs.String())
	}
	only := records[0]
	if only["toolCallsRequested"] != float64(1) || only["toolCallsExecuted"] != float64(1) {
		t.Fatalf("segment = %v, want requested=1 executed=1 in one drive", only)
	}
	if _, ok := only["suspensionTool"]; ok {
		t.Fatalf("segment = %v, want no suspension — nothing suspends a run any more", only)
	}
	if only["outcome"] != "completed" {
		t.Fatalf("outcome = %v, want completed", only["outcome"])
	}
}
