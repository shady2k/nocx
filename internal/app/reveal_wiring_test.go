package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestFilesRevealerWiredThroughProductionWiring proves the revealer is
// reached on a normal machine, not only that it errors when absent
// (AGENTS.md: "For every 'returns an error when…' there is a paired
// 'and on a normal machine it succeeds'"). Before the wiring the
// revealer was nil in the shipped app, files.reveal answered -32601
// "files.reveal not available", and the menu item raised a danger toast.
//
// The OS command is NOT actually invoked: a fake open (macOS) or
// xdg-open (Linux) is placed on PATH and records its argv. This keeps
// the test deterministic and side-effect-free on every CI runner — a
// real `open -R` would pop a Finder window on macOS (the nocx-o4hg
// class of disruption). The assertion is that files.reveal SUCCEEDS
// and the revealer reached the fake command with the containing
// directory — the -32601 "not available" refusal would fail here.
func TestFilesRevealerWiredThroughProductionWiring(t *testing.T) {
	storagetest.IsolateWithHome(t)

	// A fake revealer command on PATH: records argv, exits 0. The
	// backend (in-process) inherits this PATH.
	fakeDir := t.TempDir()
	marker := filepath.Join(fakeDir, "called.args")
	var cmdName string
	switch runtime.GOOS {
	case "darwin":
		cmdName = "open"
	case "windows":
		cmdName = "explorer.exe"
	default:
		cmdName = "xdg-open"
	}
	script := "#!/bin/sh\necho \"$@\" > \"" + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(fakeDir, cmdName), []byte(script), 0o755); err != nil { //nolint:gosec // a fixture executable
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	a, appErr := newTestApp(t)
	if appErr != nil {
		t.Fatalf("New(): %v", appErr)
	}
	if serr := a.Start(context.Background()); serr != nil {
		t.Fatalf("Start: %v", serr)
	}
	defer a.Shutdown(context.Background())

	wsURL := "ws://127.0.0.1:" + strconv.Itoa(a.WSPort()) + "/session"
	conn, _, dialErr := (&websocket.Dialer{
		Subprotocols: []string{"nocx.token." + a.WSToken()},
	}).Dial(wsURL, nil)
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer func() { _ = conn.Close() }()

	// Open a local session and capture the session id from the result.
	sid := openSessionApp(t, conn, 1)

	// Open a files binding rooted at a temp directory.
	dir := t.TempDir()
	bid := openBindingApp(t, conn, sid, dir, 2)

	// The file to reveal — inside the binding's root.
	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Call files.reveal over the wire and assert SUCCESS: the revealer
	// is wired, the handler reached Reveal, and Reveal invoked the fake
	// OS command. The -32601 "not available" refusal of the nil branch
	// would fail here with an error envelope.
	resp := jsonrpcCallRaw(t, conn, "files.reveal", map[string]any{
		"bindingId": bid,
		"path":      filePath,
	}, 3)
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.reveal answered %d %q; the revealer was not reached",
			envelope.Error.Code, envelope.Error.Message)
	}

	got, err := os.ReadFile(marker) //nolint:gosec // a test-only path under t.TempDir()
	if err != nil {
		t.Fatalf("the OS reveal command was not invoked (marker not written): %v", err)
	}
	gotStr := strings.TrimSpace(string(got))
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(gotStr, "-R") || !strings.Contains(gotStr, filePath) {
			t.Fatalf("open called with %q, want -R and the file path", gotStr)
		}
	case "windows":
		if !strings.Contains(gotStr, "/select,"+filePath) {
			t.Fatalf("explorer.exe called with %q, want /select,<path>", gotStr)
		}
	default:
		if gotStr != dir {
			t.Fatalf("xdg-open called with %q, want %q (the containing directory)", gotStr, dir)
		}
	}
}

// openSessionApp opens a local session over the real WebSocket and
// returns the session id from the result.
func openSessionApp(t *testing.T, conn *websocket.Conn, id int) string {
	t.Helper()
	resp := jsonrpcCallRaw(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24,
	}, id)
	var env struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode open result: %v", err)
	}
	if env.Result.SessionID == "" {
		t.Fatal("open returned empty sessionId")
	}
	return env.Result.SessionID
}

// openBindingApp opens a files binding for the session rooted at dir.
func openBindingApp(t *testing.T, conn *websocket.Conn, sid, dir string, id int) string {
	t.Helper()
	resp := jsonrpcCallRaw(t, conn, "files.open", map[string]any{
		"sessionId": sid,
		"rootPath":  dir,
	}, id)
	var env struct {
		Result struct {
			BindingID string `json:"bindingId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode files.open result: %v", err)
	}
	if env.Result.BindingID == "" {
		t.Fatal("files.open returned empty bindingId")
	}
	return env.Result.BindingID
}

// jsonrpcCallRaw sends a JSON-RPC request and returns the raw response
// (skipping notifications). Unlike jsonrpcCall in app_test.go, it
// returns the full raw bytes so the caller can decode the result.
func jsonrpcCallRaw(t *testing.T, conn *websocket.Conn, method string, params any, id int) []byte {
	t.Helper()
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read %s response: %v", method, rerr)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(raw, &check)
		if check.ID == nil {
			continue // a notification
		}
		return raw
	}
}
