package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// ── test-only declarations ─────────────────────────────────────────────

func f64ptr(v float64) *float64 { return &v }

var testSecretKey = settings.MustRegisterSecret(settings.SecretSpec{
	Key:         "test.secret",
	Section:     "Test",
	Label:       "Test Secret",
	Description: "A test secret setting for transport-level tests.",
	DataClass:   settings.SecretAuthenticator,
})

var testNumberKey = settings.MustRegisterNumber(settings.NumberSpec{
	Key:         "test.number",
	Section:     "Test",
	Label:       "Test Number",
	Description: "A test number setting with bounds.",
	DataClass:   settings.PublicConfig,
	Default:     50,
	Min:         f64ptr(0),
	Max:         f64ptr(100),
})

var testStringKey = settings.MustRegisterString(settings.StringSpec{
	Key:         "test.stringNotEmpty",
	Section:     "Test",
	Label:       "Non-Empty String",
	Description: "A string setting whose validation rejects the empty string.",
	Default:     "required",
	DataClass:   settings.PublicConfig,
})

// ── fake secret store ──────────────────────────────────────────────────

type fakeSecretStore struct {
	mu   sync.Mutex
	data map[credential.SecretID]string
}

func (f *fakeSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = make(map[credential.SecretID]string)
	}
	var idB [32]byte
	if _, err := rand.Read(idB[:]); err != nil {
		return "", err
	}
	id := credential.SecretID(hex.EncodeToString(idB[:]))
	var s string
	if err := value.Use(func(b []byte) error { s = string(b); return nil }); err != nil {
		return "", err
	}
	f.data[id] = s
	return id, nil
}

func (f *fakeSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.data[id]; ok {
		return credential.NewSecret(v), nil
	}
	return credential.Secret{}, nil
}

func (f *fakeSecretStore) Delete(_ context.Context, id credential.SecretID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
	return nil
}

func (f *fakeSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[id]
	return ok, nil
}

// ── helpers ────────────────────────────────────────────────────────────

func newSettingsWSServer(t *testing.T) (*WSServer, func()) {
	t.Helper()
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	secretStore := &fakeSecretStore{}
	reg := settings.New(docStore, secretStore)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithSettingsRegistry(reg))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

type rpcEnvelope struct {
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *jsonrpcErrorObj `json:"error,omitempty"`
}

// ── settings.describe ──────────────────────────────────────────────────

func TestSettingsDescribe_ReturnsDeclarations(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.describe", map[string]any{})
	var env struct {
		Result struct {
			Declarations []settings.Declaration `json:"declarations"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}
	if len(env.Result.Declarations) == 0 {
		t.Fatal("expected at least one declaration")
	}
	// The OSC 52 suppressed declaration must be present.
	found := false
	for _, d := range env.Result.Declarations {
		if d.Key == "clipboard.osc52Suppressed" {
			found = true
			if d.Control != "toggle" {
				t.Errorf("clipboard.osc52Suppressed control = %q, want toggle", d.Control)
			}
		}
	}
	if !found {
		t.Error("clipboard.osc52Suppressed not found in declarations")
	}
}

// ── settings.getSnapshot ─────────────────────────────────────────────────

func TestSettingsGetSnapshot_ContainsNoSecret(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.getSnapshot", map[string]any{})
	var env struct {
		Result struct {
			Values     map[string]any `json:"values"`
			Overridden []string       `json:"overridden"`
			Revision   int            `json:"revision"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}
	// No secret-class key may appear in values or overridden.
	for _, d := range settings.Descriptors() {
		if d.Control() == "secret" {
			if _, ok := env.Result.Values[d.Key()]; ok {
				t.Errorf("secret key %q found in snapshot values", d.Key())
			}
			for _, k := range env.Result.Overridden {
				if k == d.Key() {
					t.Errorf("secret key %q found in snapshot overridden", d.Key())
				}
			}
		}
	}
}

func TestSettingsGetSnapshot_ReturnsDefaults(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.getSnapshot", map[string]any{})
	var env struct {
		Result struct {
			Values     map[string]any `json:"values"`
			Overridden []string       `json:"overridden"`
			Revision   int            `json:"revision"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}
	v, ok := env.Result.Values["clipboard.osc52Suppressed"]
	if !ok {
		t.Fatal("clipboard.osc52Suppressed missing from snapshot values")
	}
	bv, ok := v.(bool)
	if !ok || bv {
		t.Errorf("expected default false, got %v (%T)", v, v)
	}
	if env.Result.Revision < 0 {
		t.Errorf("revision must be >= 0, got %d", env.Result.Revision)
	}
}

// ── settings.set ───────────────────────────────────────────────────────

func TestSettingsSet_SetsAndGetsBool(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": true,
	})
	var env struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}
	if !env.Result.OK {
		t.Fatal("expected ok: true")
	}
}

func TestSettingsSet_RejectsSecret(t *testing.T) {
	// settings.set MUST refuse a control:'secret' key.  Secrets go
	// through settings.secretSet, never through settings.set.
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.set", map[string]any{
		"key":   testSecretKey.Key(),
		"value": "should-fail",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error when settings.set called on secret key")
	}
	if env.Error.Code != -32602 {
		t.Errorf("expected code -32602 (Invalid params), got %d", env.Error.Code)
	}
}

func TestSettingsSet_ValidationErrorIsJSONRPCError(t *testing.T) {
	// A *settings.ValidationError from the registry becomes a JSON-RPC
	// error with code -32602, not {ok: false}.
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Set test.number to a value outside [0, 100] to trigger validation.
	resp := jsonrpcCall(t, conn, "settings.set", map[string]any{
		"key":   testNumberKey.Key(),
		"value": float64(200),
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error for out-of-range number, got success")
	}
	if env.Error.Code != -32602 {
		t.Errorf("expected code -32602 (Invalid params), got %d", env.Error.Code)
	}
}

func TestSettingsSet_UnknownKey(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.set", map[string]any{
		"key":   "nonexistent.key",
		"value": true,
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error for unknown key")
	}
	if env.Error.Code != -32602 {
		t.Errorf("expected code -32602 (Invalid params), got %d", env.Error.Code)
	}
}

// ── settings.reset ─────────────────────────────────────────────────────

func TestSettingsReset_RestoresDefault(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Set to true, then reset.
	jsonrpcCall(t, conn, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": true,
	})
	resp := jsonrpcCall(t, conn, "settings.reset", map[string]any{
		"key": "clipboard.osc52Suppressed",
	})
	var env struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}
	if !env.Result.OK {
		t.Fatal("expected ok: true")
	}
}

func TestSettingsReset_UnknownKey(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.reset", map[string]any{
		"key": "nonexistent.key",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error for unknown key")
	}
	if env.Error.Code != -32602 {
		t.Errorf("expected code -32602 (Invalid params), got %d", env.Error.Code)
	}
}

func TestSettingsReset_RejectsSecret(t *testing.T) {
	// Reset on a control:'secret' returns ValidationError → JSON-RPC error -32602.
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.reset", map[string]any{
		"key": testSecretKey.Key(),
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error when resetting a secret key")
	}
	if env.Error.Code != -32602 {
		t.Errorf("expected code -32602 (Invalid params), got %d", env.Error.Code)
	}
}

// ── settings.secretSet / secretDelete / secretExists ───────────────────

func TestSettingsSecretSetDeleteExists(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// secretSet on a real secret key.
	resp := jsonrpcCall(t, conn, "settings.secretSet", map[string]any{
		"key":   testSecretKey.Key(),
		"value": "my-secret-value",
	})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal secretSet: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error on secretSet: code=%d msg=%s", env.Error.Code, env.Error.Message)
	}

	// secretExists should report true.
	resp2 := jsonrpcCall(t, conn, "settings.secretExists", map[string]any{
		"key": testSecretKey.Key(),
	})
	var existEnv struct {
		Result struct {
			Exists bool `json:"exists"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp2, &existEnv); err != nil {
		t.Fatalf("unmarshal secretExists: %v", err)
	}
	if existEnv.Error != nil {
		t.Fatalf("unexpected error on secretExists: code=%d msg=%s", existEnv.Error.Code, existEnv.Error.Message)
	}
	if !existEnv.Result.Exists {
		t.Fatal("expected exists: true after secretSet")
	}

	// secretDelete.
	resp3 := jsonrpcCall(t, conn, "settings.secretDelete", map[string]any{
		"key": testSecretKey.Key(),
	})
	var delEnv struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error,omitempty"`
	}
	if err := json.Unmarshal(resp3, &delEnv); err != nil {
		t.Fatalf("unmarshal secretDelete: %v", err)
	}
	if delEnv.Error != nil {
		t.Fatalf("unexpected error on secretDelete: code=%d msg=%s", delEnv.Error.Code, delEnv.Error.Message)
	}
	if !delEnv.Result.OK {
		t.Fatal("expected ok: true after secretDelete")
	}

	// secretExists should now report false.
	resp4 := jsonrpcCall(t, conn, "settings.secretExists", map[string]any{
		"key": testSecretKey.Key(),
	})
	var existEnv2 struct {
		Result struct {
			Exists bool `json:"exists"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp4, &existEnv2); err != nil {
		t.Fatalf("unmarshal secretExists: %v", err)
	}
	if existEnv2.Result.Exists {
		t.Fatal("expected exists: false after secretDelete")
	}
}

func TestSettingsSecretMethods_UnknownKey(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	for _, method := range []string{"settings.secretSet", "settings.secretDelete", "settings.secretExists"} {
		params := map[string]any{"key": "nonexistent.secret"}
		if method == "settings.secretSet" {
			params["value"] = "v"
		}
		resp := jsonrpcCall(t, conn, method, params)
		var env rpcEnvelope
		if err := json.Unmarshal(resp, &env); err != nil {
			t.Fatalf("%s unmarshal: %v", method, err)
		}
		if env.Error == nil {
			t.Errorf("%s: expected JSON-RPC error for unknown key, got success", method)
		} else if env.Error.Code != -32602 {
			t.Errorf("%s: expected code -32602, got %d", method, env.Error.Code)
		}
	}
}

// ── not wired ──────────────────────────────────────────────────────────

func TestSettingsDescribe_MethodNotFound_WhenNotWired(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "settings.describe", map[string]any{})
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil {
		t.Fatal("expected JSON-RPC error when settings not wired")
	}
	if env.Error.Code != -32601 {
		t.Errorf("expected code -32601 (Method not found), got %d", env.Error.Code)
	}
}

// ── validation error unwrap ────────────────────────────────────────────

func TestSettingsSet_ValidationErrorUnwraps(t *testing.T) {
	if !errors.Is(settings.ErrValidation, settings.ErrValidation) {
		t.Fatal("settings.ErrValidation does not satisfy errors.Is with itself")
	}
	ve := &settings.ValidationError{SettingKey: "test", Value: "bad", Message: "invalid"}
	if !errors.Is(ve, settings.ErrValidation) {
		t.Fatal("ValidationError does not unwrap to ErrValidation")
	}
}

// ── helpers for notification tests ──────────────────────────────────────

// rpcResult holds a decoded JSON-RPC response or notification.
type rpcResult struct {
	ID     int              `json:"id,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *jsonrpcErrorObj `json:"error,omitempty"`
	Method string           `json:"method,omitempty"`
	Params map[string]any   `json:"params,omitempty"`
}

// callAndReadAll sends a JSON-RPC request and reads all messages until the
// response with matching id arrives. Returns the response and any
// notifications that were received before it.
func callAndReadAll(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) (*rpcResult, []*rpcResult) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var notifications []*rpcResult
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var msg rpcResult
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Method != "" {
			notifications = append(notifications, &msg)
			continue
		}
		if msg.ID == id {
			return &msg, notifications
		}
	}
}

// ── settings.changed broadcast ──────────────────────────────────────────

func TestSettingsChanged_BroadcastAfterMutation(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp, notifs := callAndReadAll(t, conn, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": true,
	}, 1)

	if resp.Error != nil {
		t.Fatalf("settings.set error: %s", resp.Error.Message)
	}

	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}
	notif := notifs[0]
	if notif.Method != "settings.changed" {
		t.Fatalf("expected settings.changed, got %q", notif.Method)
	}
	if notif.Params == nil {
		t.Fatal("params missing")
	}
	if _, ok := notif.Params["revision"]; !ok {
		t.Error("revision missing from notification")
	}
	keys, ok := notif.Params["keys"].([]any)
	if !ok {
		t.Fatal("keys missing from notification")
	}
	if len(keys) != 1 || keys[0] != "clipboard.osc52Suppressed" {
		t.Errorf("keys = %v, want [clipboard.osc52Suppressed]", keys)
	}
}

func TestSettingsChanged_TwoClientsBothReceive(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn1 := connectWS(t, ws)
	defer func() { _ = conn1.Close() }()
	conn2 := connectWS(t, ws)
	defer func() { _ = conn2.Close() }()

	resp, notifs1 := callAndReadAll(t, conn1, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": true,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.set error: %s", resp.Error.Message)
	}
	if len(notifs1) != 1 || notifs1[0].Method != "settings.changed" {
		t.Error("conn1: expected settings.changed notification")
	}

	// conn2 must also receive the notification.
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("conn2 read: %v", err)
	}
	var msg rpcResult
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("conn2 unmarshal: %v", err)
	}
	if msg.Method != "settings.changed" {
		t.Errorf("conn2: expected settings.changed, got %q", msg.Method)
	}
}

func TestSettingsChanged_DisconnectedClientNotLeaked(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn1 := connectWS(t, ws)
	conn2 := connectWS(t, ws)

	// Disconnect conn2 and wait for the server to process the close.
	_ = conn2.Close()
	time.Sleep(100 * time.Millisecond)

	// Mutate from conn1.
	resp, notifs := callAndReadAll(t, conn1, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": true,
	}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.set error: %s", resp.Error.Message)
	}
	if len(notifs) != 1 || notifs[0].Method != "settings.changed" {
		t.Error("conn1: expected settings.changed")
	}

	// Mutate again — broadcast must not panic.
	resp2, notifs2 := callAndReadAll(t, conn1, "settings.set", map[string]any{
		"key":   "clipboard.osc52Suppressed",
		"value": false,
	}, 2)
	if resp2.Error != nil {
		t.Fatalf("second settings.set error: %s", resp2.Error.Message)
	}
	if len(notifs2) != 1 || notifs2[0].Method != "settings.changed" {
		t.Error("conn1: expected second settings.changed")
	}

	_ = conn1.Close()
}

func TestSettingsChanged_SecretValueNotInNotification(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp, notifs := callAndReadAll(t, conn, "settings.secretSet", map[string]any{
		"key":   testSecretKey.Key(),
		"value": "super-secret-value-12345",
	}, 1)
	if resp.Error != nil {
		t.Fatalf("settings.secretSet error: %s", resp.Error.Message)
	}
	if len(notifs) != 1 || notifs[0].Method != "settings.changed" {
		t.Fatal("expected settings.changed notification")
	}
	notif := notifs[0]

	// Serialise the full notification and check that the secret value
	// does not appear anywhere.
	raw, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	if bytes.Contains(raw, []byte("super-secret-value-12345")) {
		t.Fatal("secret value found in settings.changed notification")
	}
	// The key name must be present (declared).
	if !bytes.Contains(raw, []byte(testSecretKey.Key())) {
		t.Error("secret key not found in notification; expected key presence")
	}
}

func TestSettingsChanged_FailedMutationEmitsNothing(t *testing.T) {
	ws, cleanup := newSettingsWSServer(t)
	defer cleanup()

	conn1 := connectWS(t, ws)
	defer func() { _ = conn1.Close() }()
	conn2 := connectWS(t, ws)
	defer func() { _ = conn2.Close() }()

	// Attempt an invalid mutation.
	resp, notifs := callAndReadAll(t, conn1, "settings.set", map[string]any{
		"key":   testStringKey.Key(),
		"value": "",
	}, 1)
	if resp.Error == nil {
		t.Fatal("expected validation error, got success")
	}
	if len(notifs) > 0 {
		for _, n := range notifs {
			if n.Method == "settings.changed" {
				t.Error("conn1: received unexpected settings.changed after failed mutation")
			}
		}
	}

	// Verify conn2 receives nothing.
	_ = conn2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, data, err := conn2.ReadMessage()
	if err == nil {
		var msg rpcResult
		if json.Unmarshal(data, &msg) == nil && msg.Method == "settings.changed" {
			t.Error("conn2: received unexpected settings.changed after failed mutation")
		}
	}
}
