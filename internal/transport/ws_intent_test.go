package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

type intentCall struct {
	sessionID string
	params    proto.IntentParams
}

type fakePaneIntentSource struct {
	mu     sync.Mutex
	calls  []intentCall
	result proto.IntentResult
}

func (f *fakePaneIntentSource) Intent(_ context.Context, sessionID string, p proto.IntentParams) (proto.IntentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, intentCall{sessionID: sessionID, params: p})
	return f.result, nil
}

func (f *fakePaneIntentSource) snapshot() []intentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]intentCall(nil), f.calls...)
}

func TestSessionIntentOverTheWireForwardsStructuredInput(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{State: "executed", BytesWritten: 1, FenceAfter: 7}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	schema := loadSchema(t, "session.intent.schema.json")

	raw := jsonrpcCallWithID(t, conn, "session.intent", map[string]any{
		"sessionId": sid, "accessEpoch": 4, "kind": "key", "payload": []byte("Enter"),
	}, 2)
	var response struct {
		Result sessionIntentResult `json:"result"`
		Error  *RPCError           `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("session.intent error: %+v", response.Error)
	}
	if response.Result.State != "executed" || response.Result.BytesWritten != 1 {
		t.Fatalf("session.intent result = %+v", response.Result)
	}
	validateJSON(t, schema, sessionIntentResultRaw(t, raw), "session.intent result (real socket)")

	calls := source.snapshot()
	if len(calls) != 1 {
		t.Fatalf("helper calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.sessionID != sid || call.params.AccessEpoch != 4 || !call.params.Interactive || call.params.Kind != "key" || string(call.params.Payload) != "Enter" {
		t.Fatalf("forwarded intent = %+v, want pane input with observed epoch and physical key", call)
	}
	if call.params.Token != "" {
		t.Fatalf("interactive input unexpectedly minted a screen token: %q", call.params.Token)
	}
}

func TestSessionIntentForwardsAccessRevocationRefusalOverTheWire(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{
		State: "refused", BytesWritten: 0,
		Refusal: &proto.IntentRefusal{Cause: "access_revoked"},
	}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	schema := loadSchema(t, "session.intent.schema.json")

	raw := jsonrpcCallWithID(t, conn, "session.intent", map[string]any{
		"sessionId": sid, "accessEpoch": 1, "kind": "text", "payload": []byte("stale"),
	}, 2)
	var response struct {
		Result sessionIntentResult `json:"result"`
		Error  *RPCError           `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != nil || response.Result.State != "refused" || response.Result.Refusal == nil || response.Result.Refusal.Cause != "access_revoked" || response.Result.BytesWritten != 0 {
		t.Fatalf("revoked intent response = %+v, error=%+v", response.Result, response.Error)
	}
	validateJSON(t, schema, sessionIntentResultRaw(t, raw), "session.intent access_revoked result (real socket)")
}

func TestSessionIntentRejectsAConnectionThatDoesNotOwnThePane(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{State: "executed"}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	owner := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, owner, 1)
	other := connectWS(t, ws)
	raw := jsonrpcCallWithID(t, other, "session.intent", map[string]any{
		"sessionId": sid, "accessEpoch": 1, "kind": "key", "payload": []byte("Enter"),
	}, 2)
	var response struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("non-owner response = %s, want -32602", raw)
	}
	if len(source.snapshot()) != 0 {
		t.Fatal("non-owner intent reached the helper")
	}
}

func TestSessionIntentDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.intent.schema.json")
	raw, err := json.Marshal(sessionIntentResult{State: "refused", BytesWritten: 0, FenceAfter: 3, Refusal: &proto.IntentRefusal{Cause: "access_revoked"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.intent DTO")
}

func sessionIntentResultRaw(t *testing.T, raw []byte) []byte {
	t.Helper()
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return response.Result
}
