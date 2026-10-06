package transport

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/session"
)

type intentCall struct {
	sessionID string
	params    proto.IntentParams
}

type fakePaneIntentSource struct {
	mu     sync.Mutex
	calls  []intentCall
	result proto.IntentResult
	// epoch is what AccessEpoch answers, standing in for the helper's
	// session.snapshot (nocx-zg3k3.3.1).
	epoch uint64
}

func (f *fakePaneIntentSource) Intent(_ context.Context, sessionID string, p proto.IntentParams) (proto.IntentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, intentCall{sessionID: sessionID, params: p})
	return f.result, nil
}

func (f *fakePaneIntentSource) AccessEpoch(_ context.Context, _ string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.epoch, nil
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

// The wire's kind set is the runtime's whole intent vocabulary: a person keys,
// types, pastes, clicks and moves focus, and each reaches the helper as its own
// kind so the encoder can decide what the program asked for (nocx-zg3k3.3.1,
// design §6.1). A kind missing here is not a smaller set of features — it is a
// click the runtime is never asked to encode, refused cannot_encode.
func TestSessionIntentCarriesEveryKindTheRuntimeEncodes(t *testing.T) {
	kinds := []struct {
		kind    string
		payload string
	}{
		{"key", "Ctrl+Left"},
		{"text", "hello"},
		{"paste", "hello\nworld"},
		{"mouse", "press left 2 3"},
		{"focus", "in"},
	}
	source := &fakePaneIntentSource{result: proto.IntentResult{State: "executed", BytesWritten: 1, FenceAfter: 7}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	for i, k := range kinds {
		raw := jsonrpcCallWithID(t, conn, "session.intent", map[string]any{
			"sessionId": sid, "accessEpoch": 1, "kind": k.kind, "payload": []byte(k.payload),
		}, i+2)
		var response struct {
			Result sessionIntentResult `json:"result"`
			Error  *RPCError           `json:"error"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode %s response: %v", k.kind, err)
		}
		if response.Error != nil {
			t.Fatalf("%s intent refused at the wire: %+v", k.kind, response.Error)
		}
	}

	calls := source.snapshot()
	if len(calls) != len(kinds) {
		t.Fatalf("helper calls = %d, want %d", len(calls), len(kinds))
	}
	for i, k := range kinds {
		if calls[i].params.Kind != k.kind || string(calls[i].params.Payload) != k.payload {
			t.Fatalf("call %d = kind %q payload %q, want %q/%q",
				i, calls[i].params.Kind, calls[i].params.Payload, k.kind, k.payload)
		}
	}
}

// A kind outside that set is refused as a bad request rather than forwarded:
// sessionruntime maps an unrecognised spelling to IntentKindNone and refuses it
// cannot_encode, but a payload this protocol has no shape for is the caller's
// error and never reaches the session at all (An unrecognised claim must never
// look stronger than it is).
func TestSessionIntentRefusesAKindTheRuntimeDoesNotEncode(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{State: "executed"}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	for _, kind := range []string{"volume", "MOUSE", ""} {
		raw := jsonrpcCallWithID(t, conn, "session.intent", map[string]any{
			"sessionId": sid, "accessEpoch": 1, "kind": kind, "payload": []byte("x"),
		}, 2)
		var response struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode %q response: %v", kind, err)
		}
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("kind %q response = %s, want -32602", kind, raw)
		}
	}
	if len(source.snapshot()) != 0 {
		t.Fatal("a kind the runtime cannot encode reached the helper")
	}
}

// The answer names the epoch the pane's session is in (nocx-zg3k3.3.1): that
// is what turns a refusal into something a renderer can act on, because the
// intent it was refused can be presented again with the epoch the answer
// named. It rides the result rather than a notification so the renderer never
// needs a second source to stay current.
func TestSessionIntentResultNamesTheEpochInForce(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{
		State: "refused", BytesWritten: 0, AccessEpoch: 6,
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
	if response.Error != nil {
		t.Fatalf("session.intent error: %+v", response.Error)
	}
	if response.Result.AccessEpoch != 6 {
		t.Fatalf("refusal named epoch %d, want the 6 in force", response.Result.AccessEpoch)
	}
	validateJSON(t, schema, sessionIntentResultRaw(t, raw), "session.intent refusal (real socket)")
}

// A controller with no epoch yet sends none, and that is a request this layer
// admits: the decision belongs to the session, which refuses it with the epoch
// in force rather than this layer inventing one (nocx-zg3k3.3.1).
func TestSessionIntentWithoutAnEpochReachesTheSession(t *testing.T) {
	source := &fakePaneIntentSource{result: proto.IntentResult{
		State: "refused", BytesWritten: 0, AccessEpoch: 1,
		Refusal: &proto.IntentRefusal{Cause: "access_revoked"},
	}}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	raw := jsonrpcCallWithID(t, conn, "session.intent", map[string]any{
		"sessionId": sid, "kind": "text", "payload": []byte("first"),
	}, 2)
	var response struct {
		Result sessionIntentResult `json:"result"`
		Error  *RPCError           `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("an epochless intent was refused at the wire: %+v", response.Error)
	}
	calls := source.snapshot()
	if len(calls) != 1 || calls[0].params.AccessEpoch != 0 {
		t.Fatalf("forwarded calls = %+v, want one intent carrying no epoch", calls)
	}
	if response.Result.AccessEpoch != 1 {
		t.Fatalf("epochless intent answered with epoch %d, want the 1 in force", response.Result.AccessEpoch)
	}
}

// The DTO the wire actually sends conforms when it carries an epoch, which is
// the case the client reads.
func TestSessionIntentResultWithEpochConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.intent.schema.json")
	raw, err := json.Marshal(sessionIntentResult{State: "refused", BytesWritten: 0, FenceAfter: 3, AccessEpoch: 4, Refusal: &proto.IntentRefusal{Cause: "access_revoked"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.intent DTO with epoch")
}

// The open ack names the epoch this pane's controller presents with its intents
// (nocx-zg3k3.3.1). It is read from the helper that holds the pane, before the
// ack is built, because a renderer cannot type without it: session.intent
// refuses an intent presented under any other epoch and one presented under
// none. This is the one place the renderer can be told at the moment the
// session it will type into comes into being.
func TestOpenAckNamesThePanesAccessEpoch(t *testing.T) {
	source := &fakePaneIntentSource{epoch: 5}
	ws, _, _ := newPanesWS(t, WithPaneIntentSource(source))
	conn := connectWS(t, ws)

	raw := jsonrpcCallWithID(t, conn, "open", map[string]uint16{"cols": 80, "rows": 24}, 1)
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("open: unmarshal: %v\nraw: %s", err, raw)
	}
	if envelope.Error != nil {
		t.Fatalf("open: %+v", envelope.Error)
	}
	var got struct {
		SessionID   string `json:"sessionId"`
		AccessEpoch uint64 `json:"accessEpoch"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("open: decode result: %v", err)
	}
	if got.AccessEpoch != 5 {
		t.Fatalf("open ack named epoch %d, want the helper's 5", got.AccessEpoch)
	}
	validateJSON(t, loadSchema(t, "open.schema.json"), envelope.Result, "open ack with an access epoch (real socket)")
	awaitSubscriber(t, ws, session.ID(got.SessionID))
}

// A coordinator that cannot read the epoch states nothing rather than
// something: the field is absent, not defaulted, and the renderer's first
// intent then learns the epoch from the refusal that refuses it (nocx-zg3k3.3.1).
func TestOpenAckOmitsAnEpochNobodyCouldAnswer(t *testing.T) {
	ws, _, _ := newPanesWS(t)
	conn := connectWS(t, ws)

	raw := jsonrpcCallWithID(t, conn, "open", map[string]uint16{"cols": 80, "rows": 24}, 1)
	var envelope struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  *jsonrpcErrorObj           `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("open: unmarshal: %v\nraw: %s", err, raw)
	}
	if envelope.Error != nil {
		t.Fatalf("open: %+v", envelope.Error)
	}
	if _, present := envelope.Result["accessEpoch"]; present {
		t.Fatalf("open ack carried an access epoch with no intent source wired: %s", raw)
	}
}
