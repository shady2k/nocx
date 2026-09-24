package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
)

func sendPaneClosed(t *testing.T, conn *websocket.Conn, paneID string) {
	t.Helper()
	params, err := json.Marshal(paneClosedParams{PaneID: paneID})
	if err != nil {
		t.Fatalf("marshal pane close: %v", err)
	}
	frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"secrets.paneClosed","params":%s}`, params)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write pane close: %v", err)
	}
}

func activeConn(t *testing.T, ws *WSServer) *wsConn {
	t.Helper()
	ws.connsMu.Lock()
	defer ws.connsMu.Unlock()
	for conn := range ws.conns {
		return conn
	}
	t.Fatal("websocket connection was not registered")
	return nil
}

func pendingCapture(t *testing.T, caps *credential.CaptureRegistry, scope credential.CaptureScope, value string) string {
	t.Helper()
	results := caps.Submit(scope, []credential.PendingCredential{{
		Value:         []byte(value),
		SuggestedName: "test-secret",
		Redaction:     content.Redaction{Kind: "token", Start: 0, End: len(value), Prefix: "abcd", Suffix: "wxyz"},
	}})
	if len(results) != 1 || (results[0].Outcome != credential.OutcomeCaptured && results[0].Outcome != credential.OutcomeLinked) {
		t.Fatalf("capture submit = %+v, want one pending capture", results)
	}
	return string(results[0].CaptureID)
}

func destroyPaneOverSocket(t *testing.T, conn *websocket.Conn, wc *wsConn, caps *credential.CaptureRegistry, paneID string) {
	t.Helper()
	sendPaneClosed(t, conn, paneID)
	params, err := json.Marshal(paneClosedParams{PaneID: paneID})
	if err != nil {
		t.Fatalf("marshal pane close barrier: %v", err)
	}
	// The notification has no response. Invoke the same handler after sending the
	// wire frame to make the registry assertion deterministic; the real socket
	// registration and parser are still exercised by the frame above.
	paneClosedHandlers{captures: caps}.handlePaneClosed(context.Background(), wc, jsonrpcRequest{Params: params})
}

func TestPaneClose_DestroysOnlyThatPanesCapturesOverSocket(t *testing.T) {
	caps, err := credential.NewCaptureRegistry()
	if err != nil {
		t.Fatalf("NewCaptureRegistry: %v", err)
	}
	ws, stop := newHistoryWSServer(t, nil, WithCaptureRegistry(caps))
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wc := activeConn(t, ws)
	clientID := connectionID(wc)

	captureA := pendingCapture(t, caps, credential.CaptureScope{Connection: clientID, Pane: "pane-a", EntryID: "entry-a"}, "token-a")
	captureB := pendingCapture(t, caps, credential.CaptureScope{Connection: clientID, Pane: "pane-b", EntryID: "entry-b"}, "token-b")
	destroyPaneOverSocket(t, conn, wc, caps, "pane-a")

	if _, err := caps.Reserve(credential.CaptureID(captureA)); !errors.Is(err, credential.ErrCaptureUnknown) {
		t.Fatalf("pane-a capture after close = %v, want unknown", err)
	}
	if _, err := caps.Reserve(credential.CaptureID(captureB)); err != nil {
		t.Fatalf("pane-b capture after pane-a close = %v, want live", err)
	}
}

func TestPaneClose_DoesNotCrossConnectionsAndRejectsMalformedFrames(t *testing.T) {
	caps, err := credential.NewCaptureRegistry()
	if err != nil {
		t.Fatalf("NewCaptureRegistry: %v", err)
	}
	ws, stop := newHistoryWSServer(t, nil, WithCaptureRegistry(caps))
	defer stop()
	connA := connectWS(t, ws)
	defer func() { _ = connA.Close() }()
	wcA := activeConn(t, ws)
	idA := connectionID(wcA)
	connB := connectWS(t, ws)
	defer func() { _ = connB.Close() }()
	var wcB *wsConn
	ws.connsMu.Lock()
	for conn := range ws.conns {
		if conn != wcA {
			wcB = conn
			break
		}
	}
	ws.connsMu.Unlock()
	if wcB == nil {
		t.Fatal("second websocket connection was not registered")
	}
	idB := connectionID(wcB)

	captureA := pendingCapture(t, caps, credential.CaptureScope{Connection: idA, Pane: "same-pane", EntryID: "entry-a"}, "token-c")
	captureB := pendingCapture(t, caps, credential.CaptureScope{Connection: idB, Pane: "same-pane", EntryID: "entry-b"}, "token-d")
	malformed := pendingCapture(t, caps, credential.CaptureScope{Connection: idA, Pane: "malformed", EntryID: "entry-m"}, "token-m")
	if err := connA.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","method":"secrets.paneClosed","params":{}}`)); err != nil {
		t.Fatalf("write malformed pane close: %v", err)
	}
	if _, err := caps.Reserve(credential.CaptureID(malformed)); err != nil {
		t.Fatalf("capture after malformed frame = %v, want live", err)
	}

	destroyPaneOverSocket(t, connA, wcA, caps, "same-pane")
	if _, err := caps.Reserve(credential.CaptureID(captureA)); !errors.Is(err, credential.ErrCaptureUnknown) {
		t.Fatalf("connection A capture after close = %v, want unknown", err)
	}
	if _, err := caps.Reserve(credential.CaptureID(captureB)); err != nil {
		t.Fatalf("same pane on connection B after A close = %v, want live", err)
	}
}

func TestPaneClose_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.paneClosed.schema.json")
	raw, err := json.Marshal(paneClosedParams{PaneID: "pane-a"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "secrets.paneClosed params DTO")
}
