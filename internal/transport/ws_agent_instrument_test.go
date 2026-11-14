package transport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/log"
)

type instrumentLogRecord struct {
	message string
	args    []any
}

type instrumentLogger struct {
	record   instrumentLogRecord
	warnings []instrumentLogRecord
}

func (l *instrumentLogger) Debug(string, ...any) {}
func (l *instrumentLogger) Info(message string, args ...any) {
	l.record = instrumentLogRecord{message: message, args: args}
}

func (l *instrumentLogger) Warn(message string, args ...any) {
	for i := 1; i < len(args); i += 2 {
		if err, ok := args[i].(error); ok {
			args[i] = err.Error()
		}
	}
	l.warnings = append(l.warnings, instrumentLogRecord{message, args})
}
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
	root := t.TempDir()
	target := filepath.Join(root, "input.txt")
	if err := os.WriteFile(target, []byte("read me"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "before"}, {Kind: protocol.StepTool, Name: "files.read", Arguments: map[string]any{"path": target}}, {Kind: protocol.StepEnd}}}
	h := newGapHandlers(svc, newScriptedAgent(t, script).harness, assistant.NewApprovalStore())
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := content.Open(t.Context(), content.Config{Path: filepath.Join(t.TempDir(), "content.db"), Key: make([]byte, 32), Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8}, Logger: log.NewSlogAdapter(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.attemptLedger = db.Ledger()
	h.client = client
	h.knownMaterial = adapterKnownMaterial(adapterVault(t))
	rc := gapRunContext()
	rc.sessionID = "instrument-session"
	grant := content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}.AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	rc.grant = &grant
	h.grantFor = func(string) *content.Grant { return &grant }

	h.log = logger

	r := &gapResponder{}
	h.runAskStream(context.Background(), rc, r)

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
		t.Fatalf("tool counts = requested %v executed %v, want 1/0 after seal failure (terminal=%+v warnings=%+v)",
			values["toolCallsRequested"], values["toolCallsExecuted"], r.runState, logger.warnings)
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
