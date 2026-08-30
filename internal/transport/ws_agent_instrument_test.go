package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestLogAgentStreamEnded_DistinguishesRequestedExecutedAndResume(t *testing.T) {
	logger := &instrumentLogger{}
	logAgentStreamEnded(logger, 41, time.Now(), 3, 12, 1, 0, false, "session.run", "mutate-destructive", nil)
	if logger.record.message != "agent ask: the stream ended" {
		t.Fatalf("message = %q", logger.record.message)
	}
	want := map[string]any{
		"run":                int64(41),
		"reasoning":          3,
		"answer":             12,
		"toolCallsRequested": 1,
		"toolCallsExecuted":  0,
		"resume":             false,
		"suspensionTool":     "session.run",
		"suspensionEffect":   "mutate-destructive",
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

func TestAuthorisedRun_LogsSuspendedAndResumedSegments(t *testing.T) {
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
	if len(records) != 2 {
		t.Fatalf("stream-ended records = %d, want suspended and resumed: %s", len(records), h.logs.String())
	}
	first := records[0]
	if first["toolCallsRequested"] != float64(1) || first["toolCallsExecuted"] != float64(0) ||
		first["resume"] != false || first["suspensionTool"] != "session.run" ||
		first["suspensionEffect"] != "observe" {
		t.Fatalf("first segment = %v, want requested=1 executed=0 suspended session.run/observe", first)
	}
	second := records[1]
	if second["toolCallsRequested"] != float64(1) || second["toolCallsExecuted"] != float64(1) ||
		second["resume"] != true || second["suspensionTool"] != "" || second["suspensionEffect"] != "" {
		t.Fatalf("resumed segment = %v, want requested=1 executed=1 resume=true", second)
	}
}
