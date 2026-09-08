package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	internalLog "github.com/shady2k/nocx/internal/log"
)

// EVERY PROVIDER EXCHANGE LEAVES TWO LINES (nocx-8byie).
//
// Diagnosing a reading that never finished meant running `ss -tnp` against the
// backend to find the open socket, because the log had nothing between the
// vault's "secret retrieved" and silence. wireTap is the one place that sees
// every model call and it wrote nothing through the logger.
//
// Two lines rather than one, and that is the point: a call that never answers
// is a START WITH NO END, which is a shape somebody can read. One line on
// completion would say nothing at all about the failure that matters most.
func logLines(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func lineWithMsg(t *testing.T, raw []byte, msg string) map[string]any {
	t.Helper()
	for _, record := range logLines(t, raw) {
		if record["msg"] == msg {
			return record
		}
	}
	t.Fatalf("no %q line in %s", msg, raw)
	return nil
}

func traceLogger(buf *bytes.Buffer) internalLog.Logger {
	return internalLog.NewSlogAdapter(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func TestWireTap_LogsTheCallItMakesAndTheAnswerItGets(t *testing.T) {
	var logs bytes.Buffer
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	tap := newWireTapWith(inner, "", nil, traceLogger(&logs))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://provider.test/v1/chat/completions?api-key=SUPERSECRET", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tap.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	started := lineWithMsg(t, logs.Bytes(), "provider call started")
	if started["method"] != http.MethodPost {
		t.Fatalf("method = %v", started["method"])
	}
	if started["url"] != "https://provider.test/v1/chat/completions" {
		t.Fatalf("url = %v, want scheme, host and path and nothing else", started["url"])
	}
	if started["requestBytes"] != float64(len(`{"model":"m"}`)) {
		t.Fatalf("requestBytes = %v", started["requestBytes"])
	}

	answered := lineWithMsg(t, logs.Bytes(), "provider call answered")
	if answered["status"] != float64(http.StatusOK) {
		t.Fatalf("status = %v", answered["status"])
	}
	if _, ok := answered["elapsed"]; !ok {
		t.Fatal("the answer line does not say how long it took")
	}

	// A QUERY MAY CARRY A KEY, so it never reaches the log. Asserted against
	// the whole buffer rather than one field, because a second line added
	// later must not be the one that leaks it.
	if strings.Contains(logs.String(), "SUPERSECRET") {
		t.Fatalf("the log carries the query string: %s", logs.String())
	}
}

// A call that FAILS says so, with the time it spent failing — the fact that
// tells a refusal (instant) from a bad address (a dial) from a provider that
// accepted and then said nothing (the whole budget).
func TestWireTap_LogsACallThatDidNotAnswer(t *testing.T) {
	var logs bytes.Buffer
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(req.Body)
		return nil, errors.New("context deadline exceeded")
	})
	tap := newWireTapWith(inner, "", nil, traceLogger(&logs))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://provider.test/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, rtErr := tap.RoundTrip(req); rtErr == nil {
		t.Fatal("RoundTrip swallowed the transport error")
	}

	lineWithMsg(t, logs.Bytes(), "provider call started")
	failed := lineWithMsg(t, logs.Bytes(), "provider call failed")
	if failed["url"] != "https://provider.test/v1/chat/completions" {
		t.Fatalf("url = %v", failed["url"])
	}
	if _, ok := failed["elapsed"]; !ok {
		t.Fatal("the failure line does not say how long it took")
	}
	if !strings.Contains(logs.String(), "deadline") {
		t.Fatalf("the failure line does not carry the reason: %s", logs.String())
	}
}
