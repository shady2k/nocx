package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVaultAPIToken_CreateInventoryRenameReplaceDelete(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	createResp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name":  "service token",
		"kind":  "api-token",
		"value": "token-one",
	})
	if isErrorResponse(t, createResp) {
		t.Fatalf("vault.createSecret: %s", string(createResp))
	}

	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("inventory entries = %d, want 1", len(inv.Entries))
	}
	entry := inv.Entries[0]
	if entry.Kind != "api-token" {
		t.Fatalf("inventory kind = %q, want %q", entry.Kind, "api-token")
	}

	renameResp := jsonrpcCall(t, h.conn, "vault.renameSecret", map[string]any{
		"id":   entry.ID,
		"name": "renamed service token",
	})
	if isErrorResponse(t, renameResp) {
		t.Fatalf("vault.renameSecret: %s", string(renameResp))
	}

	replaceResp := jsonrpcCall(t, h.conn, "vault.replaceSecret", map[string]any{
		"id":    entry.ID,
		"value": "token-two",
	})
	if isErrorResponse(t, replaceResp) {
		t.Fatalf("vault.replaceSecret: %s", string(replaceResp))
	}

	inv = h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("inventory entries after replace = %d, want 1", len(inv.Entries))
	}
	if got := inv.Entries[0]; got.ID != entry.ID || got.Name != "renamed service token" || got.Kind != "api-token" {
		t.Fatalf("inventory after rename/replace = %+v, want same row with renamed api-token", got)
	}

	deleteResp := jsonrpcCall(t, h.conn, "vault.deleteSecret", map[string]any{"id": entry.ID})
	if isErrorResponse(t, deleteResp) {
		t.Fatalf("vault.deleteSecret: %s", string(deleteResp))
	}
	if inv = h.callInventory(); len(inv.Entries) != 0 {
		t.Fatalf("inventory entries after delete = %d, want 0", len(inv.Entries))
	}
}

func TestVaultCreateSecret_RejectsRowLookingNameBeforeWriting(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	resp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name":  "secrow:foo",
		"kind":  "api-token",
		"value": "token",
	})
	assertRowLookingNameError(t, resp, "vault.createSecret")
	if inv := h.callInventory(); len(inv.Entries) != 0 {
		t.Fatalf("inventory entries after refused create = %d, want 0", len(inv.Entries))
	}
}

func TestVaultRenameSecret_RejectsRowLookingNameBeforeWriting(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	createResp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name":  "old name",
		"kind":  "api-token",
		"value": "token",
	})
	if isErrorResponse(t, createResp) {
		t.Fatalf("vault.createSecret: %s", string(createResp))
	}
	inv := h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("precondition inventory entries = %d, want 1", len(inv.Entries))
	}
	row := inv.Entries[0].ID

	resp := jsonrpcCall(t, h.conn, "vault.renameSecret", map[string]any{
		"id":   row,
		"name": "secrow:foo",
	})
	assertRowLookingNameError(t, resp, "vault.renameSecret")

	inv = h.callInventory()
	if len(inv.Entries) != 1 {
		t.Fatalf("inventory entries after refused rename = %d, want 1", len(inv.Entries))
	}
	if inv.Entries[0].Name != "old name" {
		t.Fatalf("name after refused rename = %q, want %q", inv.Entries[0].Name, "old name")
	}
}

func TestVaultNamesContainingRowPrefixLaterAreAccepted(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	createResp := jsonrpcCall(t, h.conn, "vault.createSecret", map[string]any{
		"name":  "my secrow:thing",
		"kind":  "api-token",
		"value": "token",
	})
	if isErrorResponse(t, createResp) {
		t.Fatalf("vault.createSecret: %s", string(createResp))
	}
	inv := h.callInventory()
	if len(inv.Entries) != 1 || inv.Entries[0].Name != "my secrow:thing" {
		t.Fatalf("inventory after containing-prefix create = %+v, want one accepted name", inv.Entries)
	}

	resp := jsonrpcCall(t, h.conn, "vault.renameSecret", map[string]any{
		"id":   inv.Entries[0].ID,
		"name": "renamed secrow:thing",
	})
	if isErrorResponse(t, resp) {
		t.Fatalf("vault.renameSecret containing prefix: %s", string(resp))
	}
}

func assertRowLookingNameError(t *testing.T, raw json.RawMessage, method string) {
	t.Helper()
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s response: %v\nraw: %s", method, err, string(raw))
	}
	if envelope.Error == nil {
		t.Fatalf("%s accepted a display name that looks like a row handle", method)
	}
	if want := "secret name must not begin with secrow:"; !strings.Contains(envelope.Error.Message, want) {
		t.Fatalf("%s error = %q, want it to contain %q", method, envelope.Error.Message, want)
	}
}
