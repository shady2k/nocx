package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/vault"
)

// ── fake vault lifecycle ──────────────────────────────────────────────

type fakeVaultLifecycle struct {
	state                 vault.State
	snap                  vault.Snapshot
	setupErr              error
	setupResult           vault.SetupResult
	unsealErr             error
	sealCalled            bool
	changePassphraseErr   error
	regenerateErr         error
	regenerateCode        string
	setDefaultProviderErr error
	setAutoSealErr        error
	setAutoSealCalled     bool
	activityCalled        bool
	inventoryErr          error
	inventoryResult       []vault.InventoryEntry
	createNamedErr        error
	createNamedID         credential.SecretID
	resolvedName          string
	usedName              string
	createNamedCalled     int
	renameSecretErr       error
	renameSecretName      string
	resolveRowID          credential.SecretID
	resolveRowFound       bool
	resolveRowErr         error
	getSecret             credential.Secret
	getErr                error
}

func (f *fakeVaultLifecycle) State() vault.State { return f.state }

func (f *fakeVaultLifecycle) Snapshot(_ context.Context) vault.Snapshot { return f.snap }

func (f *fakeVaultLifecycle) Setup(_ context.Context, _ vault.SetupRequest) (vault.SetupResult, error) {
	if f.setupErr != nil {
		return vault.SetupResult{}, f.setupErr
	}
	return f.setupResult, nil
}

func (f *fakeVaultLifecycle) Unseal(_ context.Context, _ vault.UnsealRequest) error {
	return f.unsealErr
}

func (f *fakeVaultLifecycle) Seal() { f.sealCalled = true }

func (f *fakeVaultLifecycle) ChangePassphrase(_ context.Context, _ vault.ChangePassphraseRequest) error {
	return f.changePassphraseErr
}

func (f *fakeVaultLifecycle) RegenerateRecovery(_ context.Context, _ vault.RegenerateRequest) (string, error) {
	if f.regenerateErr != nil {
		return "", f.regenerateErr
	}
	return f.regenerateCode, nil
}

func (f *fakeVaultLifecycle) SetDefaultProvider(_ context.Context, _ vault.ProviderID) error {
	return f.setDefaultProviderErr
}

func (f *fakeVaultLifecycle) SetAutoSeal(_ context.Context, _ int) error {
	f.setAutoSealCalled = true
	return f.setAutoSealErr
}

func (f *fakeVaultLifecycle) Activity() {
	f.activityCalled = true
}

func (f *fakeVaultLifecycle) BuildInventory(_ context.Context, _ []vault.CredentialInventory) ([]vault.InventoryEntry, error) {
	if f.inventoryErr != nil {
		return nil, f.inventoryErr
	}
	return f.inventoryResult, nil
}

func (f *fakeVaultLifecycle) CreateNamed(_ context.Context, _ credential.Secret, _ vault.SecretMeta) (credential.SecretID, error) {
	if f.createNamedErr != nil {
		return "", f.createNamedErr
	}
	return f.createNamedID, nil
}

func (f *fakeVaultLifecycle) CreateNamedResolved(_ context.Context, _ credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error) {
	f.createNamedCalled++
	if f.createNamedErr != nil {
		return "", "", f.createNamedErr
	}
	f.usedName = meta.Name
	return f.createNamedID, f.resolvedName, nil
}

func (f *fakeVaultLifecycle) RenameSecret(_ context.Context, row string, name string, _ []vault.CredentialInventory) error {
	f.renameSecretName = name
	return f.renameSecretErr
}

func (f *fakeVaultLifecycle) ReplaceSecret(_ context.Context, row string, _ credential.Secret, _ []vault.CredentialInventory) error {
	f.renameSecretName = row
	return f.renameSecretErr
}

func (f *fakeVaultLifecycle) ResolveRow(row string, _ []vault.CredentialInventory) (credential.SecretID, bool) {
	if f.resolveRowErr != nil {
		return "", false
	}
	return f.resolveRowID, f.resolveRowFound
}

func (f *fakeVaultLifecycle) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	if f.getErr != nil {
		return credential.Secret{}, f.getErr
	}
	return f.getSecret, nil
}

func newFakeVaultLifecycle() *fakeVaultLifecycle {
	return &fakeVaultLifecycle{
		state: vault.StateUnsealed,
		snap: vault.Snapshot{
			State:        vault.StateUnsealed,
			HasOSKey:     true,
			OSKeyCapable: true,
			Providers: []vault.ProviderSnapshot{
				{ID: "system", Writable: true, Ready: true},
				{ID: "file", Writable: true, Ready: true},
			},
		},
		setupResult: vault.SetupResult{RecoveryCode: "abc123"},
	}
}

// ── helpers ───────────────────────────────────────────────────────────

func newVaultWSServer(t *testing.T, vl VaultLifecycle) (*WSServer, func()) {
	t.Helper()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithVaultLifecycle(vl))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// vaultRPCResult holds a decoded JSON-RPC response.
type vaultRPCResult struct {
	ID     int             `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *vaultRPCError  `json:"error,omitempty"`
}

type vaultRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// vaultCall sends a JSON-RPC request and reads the matching response.
func vaultCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) *vaultRPCResult {
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
	if werr := conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write request: %v", werr)
	}
	// Through the inbox: this loop used to drop every frame that was not
	// its own response, which cost TestWSServer_OpenPasswordAsk_DoesNotBlockTheReadLoop
	// the open response it read for afterwards (nocx-2h08).
	data, err := awaitFrame(conn, time.Now().Add(wantWithin), isResponseTo(id))
	if err != nil {
		t.Fatalf("read response to %s (id %d): %v", method, id, err)
	}
	var msg vaultRPCResult
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("%s response: decode: %v", method, err)
	}
	return &msg
}

// ── vault.status ──────────────────────────────────────────────────────

func TestVaultRPC_Status_EmptyParams(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected result, got nil")
	}
	var status struct {
		State          string `json:"state"`
		OSKeyAvailable bool   `json:"osKeyAvailable"`
		OSKeyCapable   bool   `json:"osKeyCapable"`
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if status.State != "unsealed" {
		t.Errorf("state = %q, want %q", status.State, "unsealed")
	}
	if !status.OSKeyAvailable {
		t.Error("osKeyAvailable = false, want true")
	}
	if !status.OSKeyCapable {
		t.Error("osKeyCapable = false, want true")
	}
}

func TestVaultRPC_Status_NoLocators(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	raw := string(resp.Result)
	for _, forbidden := range []string{"entryName", "entry_name", "locator", "storageLocation", "secretId", "secret_id"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(forbidden)) {
			t.Errorf("response contains forbidden field %q", forbidden)
		}
	}
}

func TestVaultRPC_Status_ProviderReason(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.snap = vault.Snapshot{
		State:        vault.StateUnsealed,
		HasOSKey:     true,
		OSKeyCapable: true,
		Providers: []vault.ProviderSnapshot{
			{ID: "system", Writable: true, Ready: true},
			{ID: "file", Writable: true, Ready: false, Reason: vault.ReasonLocked},
		},
	}
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var status struct {
		Providers []struct {
			ID       string `json:"id"`
			Writable bool   `json:"writable"`
			Ready    bool   `json:"ready"`
			Reason   string `json:"reason"`
		}
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(status.Providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(status.Providers))
	}
	if status.Providers[1].ID != "file" {
		t.Fatalf("second provider id = %q, want %q", status.Providers[1].ID, "file")
	}
	if status.Providers[1].Ready {
		t.Error("file provider ready = true, want false")
	}
	if status.Providers[1].Reason != "locked" {
		t.Errorf("file provider reason = %q, want %q", status.Providers[1].Reason, "locked")
	}
}

// ── vault.setup ───────────────────────────────────────────────────────

func TestVaultRPC_Setup_EmptyParams(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.setup", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.RecoveryCode != "abc123" {
		t.Errorf("recoveryCode = %q, want %q", result.RecoveryCode, "abc123")
	}
}

func TestVaultRPC_Setup_Failure(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.setupErr = vault.ErrVaultUninitialized
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.setup", map[string]any{"passphrase": "hunter2"}, 1)

	if resp.Error == nil {
		t.Fatal("expected error, got nil")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("error code = %d, want %d", resp.Error.Code, -32000)
	}
	if !strings.Contains(resp.Error.Message, "not initialized") {
		t.Errorf("error message = %q, want substring %q", resp.Error.Message, "not initialized")
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("unmarshal error data: %v", err)
	}
	reason, _ := data["reason"].(string)
	if reason != "vault-uninitialized" {
		t.Errorf("reason = %q, want %q", reason, "vault-uninitialized")
	}
}

func TestVaultRPC_Setup_Silent(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.setupResult = vault.SetupResult{} // silent: no recovery code
	fake.setupErr = nil
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.setup", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := result["recoveryCode"]; ok {
		t.Error("recoveryCode should be absent for silent setup")
	}
}

// ── vault.unseal ──────────────────────────────────────────────────────

func TestVaultRPC_Unseal_EmptyParams(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.state = vault.StateSealed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.unseal", map[string]any{}, 1)

	if resp.Error == nil {
		t.Fatal("expected error for empty params")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "means is required") {
		t.Errorf("error message = %q, want substring 'means is required'", resp.Error.Message)
	}
}

func TestVaultRPC_Unseal_SecretIdRejected(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.state = vault.StateSealed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.unseal", map[string]any{
		"means":    "passphrase",
		"secret":   "hunter2",
		"secretId": "sec-v1-abc123def456",
	}, 1)

	if resp.Error == nil {
		t.Fatal("expected error for secretId field")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "backend-owned") {
		t.Errorf("error message = %q, want substring 'backend-owned'", resp.Error.Message)
	}
}

func TestVaultRPC_Unseal_Failure(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.state = vault.StateSealed
	fake.unsealErr = vault.ErrUnsealFailed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.unseal", map[string]any{
		"means":  "passphrase",
		"secret": "wrong",
	}, 1)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32003 {
		t.Errorf("error code = %d, want -32003", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "unseal failed") {
		t.Errorf("error message = %q, want substring 'unseal failed'", resp.Error.Message)
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil {
		t.Fatalf("unmarshal error data: %v", err)
	}
	reason, _ := data["reason"].(string)
	if reason != "unseal-failed" {
		t.Errorf("reason = %q, want %q", reason, "unseal-failed")
	}
}

func TestVaultRPC_Unseal_OSMeans(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.state = vault.StateSealed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.unseal", map[string]any{"means": "os"}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

// ── vault.seal ────────────────────────────────────────────────────────

func TestVaultRPC_Seal_EmptyParams(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.state = vault.StateUnsealed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.seal", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !fake.sealCalled {
		t.Error("Seal was not called")
	}
}

// ── vault.changePassphrase ─────────────────────────────────────────────

func TestVaultRPC_ChangePassphrase_Success(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{
		"oldPassphrase": "sekret",
		"newPassphrase": "newsekret",
	}
	resp := vaultCall(t, conn, "vault.changePassphrase", params, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestVaultRPC_ChangePassphrase_ErrorPropagation(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.changePassphraseErr = vault.ErrUnsealFailed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{
		"oldPassphrase": "wrong",
		"newPassphrase": "newsekret",
	}
	resp := vaultCall(t, conn, "vault.changePassphrase", params, 1)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32003 {
		t.Errorf("error code = %d, want -32003", resp.Error.Code)
	}
}

// ── vault.regenerateRecovery ──────────────────────────────────────────

func TestVaultRPC_RegenerateRecovery_Success(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.regenerateCode = "new-recovery-42"
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{"passphrase": "sekret"}
	resp := vaultCall(t, conn, "vault.regenerateRecovery", params, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var result struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.RecoveryCode != "new-recovery-42" {
		t.Errorf("recoveryCode = %q, want %q", result.RecoveryCode, "new-recovery-42")
	}
}

func TestVaultRPC_RegenerateRecovery_ErrorPropagation(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.regenerateErr = vault.ErrUnsealFailed
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{"passphrase": "wrong"}
	resp := vaultCall(t, conn, "vault.regenerateRecovery", params, 1)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32003 {
		t.Errorf("error code = %d, want -32003", resp.Error.Code)
	}
}

// ── vault.setDefaultProvider ───────────────────────────────────────────

func TestVaultRPC_SetDefaultProvider_Success(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{"provider": "file"}
	resp := vaultCall(t, conn, "vault.setDefaultProvider", params, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestVaultRPC_SetDefaultProvider_ErrorPropagation(t *testing.T) {
	fake := newFakeVaultLifecycle()
	pe := &vault.ProviderError{Provider: "system", Reason: vault.ReasonDenied, Err: vault.ErrProviderUnavailable}
	fake.setDefaultProviderErr = pe
	ws, stop := newVaultWSServer(t, fake)
	defer stop()

	conn := connectWS(t, ws)
	params := map[string]any{"provider": "system"}
	resp := vaultCall(t, conn, "vault.setDefaultProvider", params, 1)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32002 {
		t.Errorf("error code = %d, want -32002", resp.Error.Code)
	}
}

// ── vault.setAutoSeal ────────────────────────────────────────────────────

func TestVaultRPC_SetAutoSeal_Success(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, cleanup := newVaultWSServer(t, fake)
	defer cleanup()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.setAutoSeal", map[string]any{"minutes": 30}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	if !fake.setAutoSealCalled {
		t.Fatal("SetAutoSeal was not called on lifecycle")
	}
}

func TestVaultRPC_SetAutoSeal_InvalidParams(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, cleanup := newVaultWSServer(t, fake)
	defer cleanup()

	conn := connectWS(t, ws)
	// Missing "minutes" field.
	resp := vaultCall(t, conn, "vault.setAutoSeal", map[string]any{}, 1)

	if resp.Error == nil {
		t.Fatal("expected error for missing minutes")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
	if fake.setAutoSealCalled {
		t.Fatal("SetAutoSeal should not be called with invalid params")
	}
}

func TestVaultRPC_SetAutoSeal_ErrorPropagation(t *testing.T) {
	fake := newFakeVaultLifecycle()
	fake.setAutoSealErr = vault.ErrVaultUninitialized
	ws, cleanup := newVaultWSServer(t, fake)
	defer cleanup()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.setAutoSeal", map[string]any{"minutes": 5}, 1)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("error code = %d, want -32000 (ErrVaultUninitialized)", resp.Error.Code)
	}
}

// ── vault.activity ──────────────────────────────────────────────────────

func TestVaultRPC_Activity(t *testing.T) {
	fake := newFakeVaultLifecycle()
	ws, cleanup := newVaultWSServer(t, fake)
	defer cleanup()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.activity", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	if !fake.activityCalled {
		t.Fatal("Activity was not called on lifecycle")
	}
}

// ── vault.status: new fields ────────────────────────────────────────────

func TestVaultRPC_Status_AutoSealFields(t *testing.T) {
	snap := vault.Snapshot{
		State:           vault.StateUnsealed,
		HasOSKey:        false,
		OSKeyCapable:    true,
		HasPassphrase:   true,
		AutoSealMinutes: 15,
		Providers: []vault.ProviderSnapshot{
			{ID: vault.ProviderSystem, Writable: true, Ready: true},
		},
	}
	fake := newFakeVaultLifecycle()
	fake.snap = snap
	ws, cleanup := newVaultWSServer(t, fake)
	defer cleanup()

	conn := connectWS(t, ws)
	resp := vaultCall(t, conn, "vault.status", map[string]any{}, 1)

	if resp.Error != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// Decode the result to check new fields.
	var status struct {
		State           string `json:"state"`
		OSKeyAvailable  bool   `json:"osKeyAvailable"`
		OSKeyCapable    bool   `json:"osKeyCapable"`
		HasPassphrase   bool   `json:"hasPassphrase"`
		AutoSealMinutes int    `json:"autoSealMinutes"`
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.AutoSealMinutes != 15 {
		t.Errorf("autoSealMinutes = %d, want 15", status.AutoSealMinutes)
	}
	if !status.HasPassphrase {
		t.Error("hasPassphrase = false, want true")
	}
	if !status.OSKeyCapable {
		t.Error("osKeyCapable = false, want true")
	}
}

// ── not wired ─────────────────────────────────────────────────────────

func TestVaultRPC_MethodNotFound_WhenNotWired(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	for _, method := range []string{
		"vault.status", "vault.setup", "vault.unseal", "vault.seal",
		"vault.changePassphrase", "vault.regenerateRecovery", "vault.setDefaultProvider",
	} {
		resp := vaultCall(t, conn, method, map[string]any{}, 1)
		if resp.Error == nil {
			t.Errorf("%s: expected error, got nil", method)
			continue
		}
		if resp.Error.Code != -32601 {
			t.Errorf("%s: error code = %d, want -32601", method, resp.Error.Code)
		}
		if !strings.Contains(resp.Error.Message, "not available") {
			t.Errorf("%s: error message = %q, want substring 'not available'", method, resp.Error.Message)
		}
	}
}
