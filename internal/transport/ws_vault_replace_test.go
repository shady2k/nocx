package transport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// vault.replaceSecret — the renderer's "replace the value" flow, driven the
// way a user reaches it: create a secret on the Secrets page, address the row
// by the handle the inventory carries, replace its value, and observe that
// the material changed while the reference and the record did not.
func TestVaultReplaceSecret_OverTheWire(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	resp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "prod password", "kind": "password", "value": "old value",
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("vault.createSecret: %s", string(resp))
	}

	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(inv.Entries))
	}
	row := inv.Entries[0].ID
	id, ok := h.v.ResolveRowForTest(row)
	if !ok {
		t.Fatalf("row %q did not resolve", row)
	}

	replaceResp := jsonrpcCall(t, h.conn, "vault.replaceSecret", map[string]any{
		"id": row, "value": "new value",
	})
	if isErrorResponse(t, replaceResp) {
		t.Fatalf("vault.replaceSecret: %s", string(replaceResp))
	}

	// The material behind the SAME reference changed.
	got, err := h.v.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	var val string
	_ = got.Use(func(b []byte) error { val = string(b); return nil })
	if val != "new value" {
		t.Errorf("value = %q, want %q", val, "new value")
	}

	// The row, name and kind are untouched — the reference did not change.
	inv2 := h.callInventory()
	if len(inv2.Entries) != 1 {
		t.Fatalf("expected 1 entry after replace, got %d", len(inv2.Entries))
	}
	e := inv2.Entries[0]
	if e.ID != row {
		t.Errorf("row handle changed: %q → %q", row, e.ID)
	}
	if e.Name != "prod password" {
		t.Errorf("name = %q, want %q", e.Name, "prod password")
	}
	if e.Kind != "password" {
		t.Errorf("kind = %q, want %q", e.Kind, "password")
	}
}

// A private key created on the Secrets page from a path must store the file's
// CONTENTS — never the path string, which is the defect dcf566b fixed on the
// connection editor and must not be reintroduced here.
func TestVaultCreateSecret_PathStoresFileContents(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	keyContents := "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-real-key\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(keyContents), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	resp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "deploy key", "kind": "private-key", "path": keyPath,
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("vault.createSecret with path: %s", string(resp))
	}

	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(inv.Entries))
	}
	id, ok := h.v.ResolveRowForTest(inv.Entries[0].ID)
	if !ok {
		t.Fatalf("row %q did not resolve", inv.Entries[0].ID)
	}

	got, err := h.v.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var stored string
	_ = got.Use(func(b []byte) error { stored = string(b); return nil })
	if stored != keyContents {
		t.Errorf("stored material = %q, want the file contents (not the path %q)", stored, keyPath)
	}
}

// An unreadable path must fail the create and store nothing — silence there
// would leave the user believing a key was loaded.
func TestVaultCreateSecret_UnreadablePath(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	resp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name": "deploy key", "kind": "private-key", "path": "/nonexistent/nocx/key",
	})
	if !isErrorResponse(t, resp) {
		t.Fatalf("expected an error for an unreadable path, got %s", string(resp))
	}

	inv := h.callInventory()
	if len(inv.Entries) != 0 {
		t.Fatalf("expected no entry after a failed path create, got %d", len(inv.Entries))
	}
}

// The renderer may not name a secret (nocx-jb20.1): replace takes the row
// handle, and a SecretID sent in its place must be refused.
func TestVaultReplaceSecret_RejectsSecretID(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	resp := jsonrpcCall(t, h.conn, "vault.replaceSecret", map[string]any{
		"id": "sec:v1:file:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "value": "x",
	})
	if !isErrorResponse(t, resp) {
		t.Fatalf("expected an error for a SecretID addressed replace, got %s", string(resp))
	}
}

func TestVaultReplaceSecret_RequiresRow(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	resp := jsonrpcCall(t, h.conn, "vault.replaceSecret", map[string]any{"value": "x"})
	if !isErrorResponse(t, resp) {
		t.Fatalf("expected an error for a missing row, got %s", string(resp))
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func isErrorResponse(t *testing.T, resp json.RawMessage) bool {
	t.Helper()
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	return env.Error != nil
}
