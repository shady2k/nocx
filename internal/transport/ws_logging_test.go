package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
)

// syncBuffer wraps bytes.Buffer with a mutex so it is safe to use
// from the server's read goroutine under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// assertSafeFrameLog enforces the transport-wide rule on captured log output:
// none of the frame's bytes survive, no record carries the raw payload, and the
// diagnostic we deliberately keep — message plus size — is still emitted. That
// last check is what stops this test from passing if logging disappears
// entirely; absence-only assertions rot silently.
func assertSafeFrameLog(t *testing.T, logged, secret, wantMsg string) {
	t.Helper()

	// Check a fragment as well: a truncated write could leave a prefix of the
	// secret behind even when the whole sentinel is absent.
	for _, needle := range []string{secret, secret[:8]} {
		if strings.Contains(logged, needle) {
			t.Errorf("secret fragment %q leaked into the log:\n%s", needle, logged)
		}
	}

	found := false
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("bad log line %q: %v", line, err)
			continue
		}
		if _, ok := record["data"]; ok {
			t.Errorf("log record carries the raw frame: %v", record)
		}
		if record["msg"] == wantMsg {
			found = true
			if _, ok := record["len"]; !ok {
				t.Errorf("record %q dropped the size diagnostic: %v", wantMsg, record)
			}
		}
	}
	if !found {
		t.Errorf("expected a %q record, got:\n%s", wantMsg, logged)
	}
}

func TestControlFrameLogging(t *testing.T) {
	var buf syncBuffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := log.NewSlogAdapter(slog.New(h))

	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	password := "hunter2_secret_789!"

	t.Run("non_json_frame", func(t *testing.T) {
		buf.Reset()
		// A JSON array is not a JSON object — hits the -32600 branch at ws.go:419.
		frame := `["` + password + `", 42]`
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Read the error response back. handleControlFrame logs before writing the
		// response, so this read orders our assertion after the log write.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read response: %v", err)
		}

		assertSafeFrameLog(t, buf.String(), password, "jsonrpc invalid request")
	})

	t.Run("truncated_json_object", func(t *testing.T) {
		buf.Reset()
		// Starts with '{' (passes isJSONObject) but is not valid JSON —
		// hits the -32700 branch at ws.go:427.
		frame := `{"jsonrpc":"2.0","id":1,"method":"secrets.savePassword","params":{"password":"` + password
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatalf("read response: %v", err)
		}

		assertSafeFrameLog(t, buf.String(), password, "jsonrpc parse error")
	})
}
