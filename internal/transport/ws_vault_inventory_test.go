package transport

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"
)

// Test for vault.inventory RPC using a real vault + profile store.

type inventoryHarness struct {
	t    *testing.T
	v    *vault.Vault
	ps   *profile.JSONStore
	ws   *WSServer
	conn *websocket.Conn
}

func newInventoryHarness(t *testing.T, extra ...WSServerOption) *inventoryHarness {
	t.Helper()
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)

	reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := vault.New(docStore, reg, logger)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	t.Cleanup(v.Close)

	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))

	opts := append([]WSServerOption{
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(v),
		// Wired as production wires it. It only bites once a test also
		// calls SetUnlockRequester: EnsureUnsealed with no prompt carrier
		// answers ErrVaultSealed, which is the sealed-error path the rest
		// of these tests assert.
		WithVaultUnsealer(v),
		WithVaultLifecycle(v),
	}, extra...)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	return &inventoryHarness{t: t, v: v, ps: ps, ws: ws, conn: conn}
}

func (h *inventoryHarness) setupAndUnseal() {
	h.t.Helper()
	_, err := h.v.Setup(h.t.Context(), vault.SetupRequest{Passphrase: "test"})
	if err != nil {
		h.t.Fatalf("Setup: %v", err)
	}
}

// mintPassword saves a password the way the connection editor does
// (ADR-0016: the secret owns its name; empty falls back to rendering).
// Returns the renderer-addressable row handle.
func (h *inventoryHarness) mintPassword(password, name string) string {
	h.t.Helper()
	return h.mint("secrets.savePassword", map[string]any{
		"password": password,
		"name":     name,
	})
}

// mintKeyMaterial stores a private key and returns its row handle.
func (h *inventoryHarness) mintKeyMaterial(keyText, name string) (row, fingerprint string, passphraseWanted bool) {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "secrets.saveKeyMaterial", map[string]any{
		"keyText": keyText,
		"name":    name,
	})
	var result struct {
		Result struct {
			Row              string `json:"row"`
			Fingerprint      string `json:"fingerprint"`
			PassphraseWanted bool   `json:"passphraseWanted"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("secrets.saveKeyMaterial unmarshal: %v\nraw: %s", err, string(resp))
	}
	return result.Result.Row, result.Result.Fingerprint, result.Result.PassphraseWanted
}

// mintKeyPassphrase stores a verified key passphrase and returns its row.
func (h *inventoryHarness) mintKeyPassphrase(keyRow, passphrase, name string) string {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "secrets.saveKeyPassphrase", map[string]any{
		"keyRow":     keyRow,
		"passphrase": passphrase,
		"name":       name,
	})
	var result struct {
		Result struct {
			Row string `json:"row"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("secrets.saveKeyPassphrase unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result.Row == "" {
		h.t.Fatalf("secrets.saveKeyPassphrase returned empty row: %s", string(resp))
	}
	return result.Result.Row
}

func (h *inventoryHarness) mint(method string, params map[string]any) string {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, method, params)
	var result struct {
		Result struct {
			Row string `json:"row"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("%s unmarshal: %v\nraw: %s", method, err, string(resp))
	}
	if result.Result.Row == "" {
		h.t.Fatalf("%s returned empty row: %s", method, string(resp))
	}
	return result.Result.Row
}

func (h *inventoryHarness) createProfile(p profile.SSHProfile) {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "profiles.create", p)
	var result struct {
		Result profile.SSHProfile `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("profiles.create unmarshal: %v\nraw: %s", err, string(resp))
	}
}

func TestVaultInventory_MixedBindings(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	passRow := h.mintPassword("hunter2", "")

	// An encrypted key and its verified passphrase, minted the way the
	// connection editor does.
	pem, _ := testEncryptedKeyPEM(t, "pass123")
	keyRow, _, _ := h.mintKeyMaterial(pem, "ops key")
	keyPassRow := h.mintKeyPassphrase(keyRow, "pass123", "ops key passphrase")

	// Three profiles binding the password secret by its row handle.
	user1 := "deploy"
	port22 := 22
	for i := range 3 {
		host := fmt.Sprintf("vm-dsm0%d", i+1)
		h.createProfile(profile.SSHProfile{
			Base: profile.Base{
				ID:   fmt.Sprintf("prof:pass:%d", i),
				Name: fmt.Sprintf("Production %d", i+1),
				Type: "ssh",
			},
			Options: profile.StoredSSHProfileOptions{
				Host:           host,
				Port:           &port22,
				PasswordSecret: passRow,
				User:           &user1,
			},
		})
	}

	// One profile binding the key and its passphrase.
	user2 := "opsuser"
	port2222 := 2222
	h.createProfile(profile.SSHProfile{
		Base: profile.Base{
			ID:   "prof:key:1",
			Name: "OPS Server",
			Type: "ssh",
		},
		Options: profile.StoredSSHProfileOptions{
			Host:                "ops.internal",
			Port:                &port2222,
			KeySecret:           keyRow,
			KeyPassphraseSecret: keyPassRow,
			User:                &user2,
		},
	})

	inv := h.callInventory()
	if len(inv.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(inv.Entries))
	}

	var passEntry, keyEntry, passphraseEntry *inventoryEntryDTO
	for i := range inv.Entries {
		e := &inv.Entries[i]
		switch e.Kind {
		case "password":
			passEntry = e
		case "private-key":
			keyEntry = e
		case "key-passphrase":
			passphraseEntry = e
		}
	}

	if passEntry == nil {
		t.Fatal("no password entry found")
	}
	if passEntry.Provider == "" {
		t.Error("password provider is empty")
	}
	if passEntry.UsedBy != 3 {
		t.Errorf("password usedBy = %d, want 3", passEntry.UsedBy)
	}
	if !passEntry.Reachable {
		t.Error("password reachable should be true")
	}

	if keyEntry == nil {
		t.Fatal("no private-key entry found")
	}
	if keyEntry.UsedBy != 1 {
		t.Errorf("key usedBy = %d, want 1", keyEntry.UsedBy)
	}

	if passphraseEntry == nil {
		t.Fatal("no key-passphrase entry found")
	}
	if !passphraseEntry.Reachable {
		t.Error("key passphrase reachable should be true")
	}
	if passphraseEntry.UsedBy != 1 {
		t.Errorf("key passphrase usedBy = %d, want 1", passphraseEntry.UsedBy)
	}
}

type inventoryResponse struct {
	Entries []inventoryEntryDTO `json:"entries"`
}

type inventoryEntryDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Provider  string `json:"provider"`
	OwnerID   string `json:"ownerId"`
	UsedBy    int    `json:"usedBy"`
	Reachable bool   `json:"reachable"`
}

func (h *inventoryHarness) callInventory() inventoryResponse {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "vault.inventory", map[string]any{})
	var result struct {
		Result inventoryResponse `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("vault.inventory unmarshal: %v\nraw: %s", err, string(resp))
	}
	return result.Result
}

func (h *inventoryHarness) callInventoryError() (int, string, string) {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "vault.inventory", map[string]any{})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		h.t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		h.t.Fatal("expected error, got success")
	}
	reason := ""
	if errResult.Error.Data != nil {
		reason = errResult.Error.Data.Reason
	}
	return errResult.Error.Code, errResult.Error.Message, reason
}

// (owner + count). This drives the real backend over the real socket — no
// fixture — so a projection gap on the wire fails here.
func TestVaultInventory_KeyAndPassphraseCarryKindAndUsage(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	// An encrypted key, saved the way the connection editor does: the key
	// first, then its passphrase, both under the same generated name.
	pem, _ := testEncryptedKeyPEM(t, "correct horse")
	keyRow, _, wantsPass := h.mintKeyMaterial(pem, "root@192.168.0.57")
	if !wantsPass {
		t.Fatal("precondition: encrypted key not flagged as wanting a passphrase")
	}
	keyPassRow := h.mintKeyPassphrase(keyRow, "correct horse", "root@192.168.0.57")

	// One connection binds the key and its passphrase by row handle.
	user := "root"
	port := 22
	h.createProfile(profile.SSHProfile{
		Base: profile.Base{
			ID:   "prof:encrypted-key:1",
			Name: "vm-dsm01",
			Type: "ssh",
		},
		Options: profile.StoredSSHProfileOptions{
			Host:                "192.168.0.57",
			Port:                &port,
			KeySecret:           keyRow,
			KeyPassphraseSecret: keyPassRow,
			User:                &user,
		},
	})

	inv := h.callInventory()
	var keyRowEntry, passRow *inventoryEntryDTO
	for i := range inv.Entries {
		e := &inv.Entries[i]
		switch e.Kind {
		case "private-key":
			keyRowEntry = e
		case "key-passphrase":
			passRow = e
		}
	}

	if keyRowEntry == nil {
		t.Fatal("no private-key entry found — the key material reference is not on the wire")
	}
	if keyRowEntry.Name != "root@192.168.0.57" {
		t.Errorf("key name = %q, want %q", keyRowEntry.Name, "root@192.168.0.57")
	}
	if keyRowEntry.OwnerID != "prof:encrypted-key:1" {
		t.Errorf("key ownerId = %q, want %q", keyRowEntry.OwnerID, "prof:encrypted-key:1")
	}
	if keyRowEntry.UsedBy != 1 {
		t.Errorf("key usedBy = %d, want 1 — a stored key the connection resolves to must report that connection", keyRowEntry.UsedBy)
	}

	if passRow == nil {
		t.Fatal("no key-passphrase entry found")
	}
	if passRow.Name != "root@192.168.0.57" {
		t.Errorf("passphrase name = %q, want %q", passRow.Name, "root@192.168.0.57")
	}
	if passRow.OwnerID != "prof:encrypted-key:1" {
		t.Errorf("passphrase ownerId = %q, want %q", passRow.OwnerID, "prof:encrypted-key:1")
	}
	if passRow.UsedBy != 1 {
		t.Errorf("passphrase usedBy = %d, want 1", passRow.UsedBy)
	}

	// The two rows are distinguishable ONLY by kind — the names and store
	// are identical. The kind is what the surface must render (nocx-mg9r).
	if keyRowEntry.Kind == passRow.Kind {
		t.Errorf("key and passphrase rows carry the same kind %q — nothing tells them apart", keyRowEntry.Kind)
	}
}

func TestVaultInventory_SealedVault(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	h.mintPassword("hunter2", "")

	h.v.Seal()

	// The sealed vault answers with the canonical sealed error — code
	// -32001, reason vault-sealed — the shape the renderer's dispatcher
	// turns into the unlock prompt (ADR-0032). The prompt itself is the
	// renderer's, so nothing here blocks on an ask: the answer is
	// immediate, and no prompt was raised.
	code, msg, reason := h.callInventoryError()
	if code != vaultSealedCode {
		t.Errorf("error code = %d, want %d (ErrVaultSealed)", code, vaultSealedCode)
	}
	if reason != "vault-sealed" {
		t.Errorf("reason = %q, want %q", reason, "vault-sealed")
	}
	if msg == "" {
		t.Error("expected non-empty error message for sealed vault")
	}
	assertNoPendingAsk(t, h.ws)
}

func TestVaultInventory_UninitializedVault(t *testing.T) {
	h := newInventoryHarness(t)

	code, msg, _ := h.callInventoryError()
	if code != -32000 {
		t.Errorf("error code = %d, want -32000 (ErrVaultUninitialized)", code)
	}
	if msg == "" {
		t.Error("expected non-empty error message")
	}
}

func TestVaultInventory_EmptyStore(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	inv := h.callInventory()
	if len(inv.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(inv.Entries))
	}
}

func TestVaultInventory_NotWired(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "vault.inventory", map[string]any{})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatal("expected error for unwired vault")
	}
	if errResult.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", errResult.Error.Code)
	}
}

// A secret created through the production save path survives a restart.
// The journal contract used to be test-only: production never calls
// AttachTarget/CommitMetadata, so a PhaseSecretWritten entry with an empty
// target was deleted by Reconcile at the next startup. The catalogue record
// (ADR-0016) is the durable proof: the entry is cleared, the secret kept,
// and the row still renders with its name.
func TestVaultInventory_SecretSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	docStore := storage.NewDocumentStore(dir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	newVault := func() (*vault.Vault, func()) {
		reg, err := vault.NewRegistry(file.New(docStore, "vault-blob.json"))
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		v, err := vault.New(docStore, reg, logger)
		if err != nil {
			t.Fatalf("vault.New: %v", err)
		}
		return v, v.Close
	}

	v, closeV := newVault()
	if _, err := v.Setup(t.Context(), vault.SetupRequest{Passphrase: "test"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	id, err := v.CreateNamed(t.Context(), credential.NewSecret("hunter2"),
		vault.SecretMeta{Name: "deploy@web.example.com", Kind: vault.KindPassword})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	closeV()

	// Restart: reconciliation runs, the record proves the secret exists.
	v2, closeV2 := newVault()
	defer closeV2()
	if unsealErr := v2.Unseal(t.Context(), vault.UnsealRequest{Passphrase: "test"}); unsealErr != nil {
		t.Fatalf("Unseal after restart: %v", unsealErr)
	}
	ok, err := v2.Exists(t.Context(), id)
	if err != nil {
		t.Fatalf("Exists after restart: %v", err)
	}
	if !ok {
		t.Fatal("secret was deleted by reconciliation at startup")
	}
	entries, err := v2.BuildInventory(t.Context(), nil)
	if err != nil {
		t.Fatalf("BuildInventory after restart: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "deploy@web.example.com" {
		t.Fatalf("inventory after restart = %+v, want the named ownerless row", entries)
	}
}
