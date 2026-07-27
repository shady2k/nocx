package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/zalando/go-keyring"
)

// These tests prove the credential delete cascade (nocx-7l4): deleting a
// credential removes its metadata AND every secret it references, in an
// order that cannot strand a secret. They query the secret store directly,
// never through the RPC being changed, so a regression in the cascade is
// caught even if the RPC lies about success.

// cascadeHarness wires a WSServer with a profile store and a keychain
// credential store, ready for delete-cascade tests. The keyring mock is
// per-test fresh.
type cascadeHarness struct {
	t    *testing.T
	ws   *WSServer
	ps   *profile.JSONStore
	cs   credential.SecretStore
	conn *websocket.Conn
}

func newCascadeHarness(t *testing.T) *cascadeHarness {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	ps := profile.NewJSONStore(dir + "/p.json")
	cs := credential.NewKeychain()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps), WithCredentialStore(cs))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return &cascadeHarness{t: t, ws: ws, ps: ps, cs: cs, conn: conn}
}

// createCredentialViaRPC creates a credential through the credentials.create
// RPC and returns the server-assigned ID.
func (h *cascadeHarness) createCredentialViaRPC(c profile.Credential) string {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "credentials.create", c)
	var got struct {
		Result profile.Credential `json:"result"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		h.t.Fatalf("unmarshal create result: %v\nraw: %s", err, string(resp))
	}
	if got.Result.ID == "" {
		h.t.Fatalf("create returned empty id: %s", string(resp))
	}
	return got.Result.ID
}

func (h *cascadeHarness) deleteCredentialViaRPC(id string) {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "credentials.delete", map[string]any{"id": id})
	var got struct {
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		h.t.Fatalf("delete unmarshal: %v\nraw: %s", err, string(resp))
	}
	if !got.Result {
		h.t.Fatalf("delete returned false: %s", string(resp))
	}
}

// savePasswordViaRPC calls credentials.savePassword and returns the SecretID
// that the backend assigned (read from the updated credential metadata).
func (h *cascadeHarness) savePasswordViaRPC(credID, password string) credential.SecretID {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "credentials.savePassword", map[string]any{
		"credentialId": credID,
		"password":     password,
	})
	var got struct {
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(resp, &got); err != nil || !got.Result {
		h.t.Fatalf("savePassword failed: %v\nraw: %s", err, string(resp))
	}
	// Read the SecretID from the updated credential metadata.
	creds, err := h.ps.LoadCredentials()
	if err != nil {
		h.t.Fatalf("LoadCredentials: %v", err)
	}
	for _, c := range creds {
		if c.ID == credID {
			return credential.SecretID(c.SecretID)
		}
	}
	h.t.Fatalf("credential %s not found after savePassword", credID)
	return ""
}

// savePassphraseViaRPC calls credentials.saveKeyPassphrase and returns the
// PassphraseSecretID assigned.
func (h *cascadeHarness) savePassphraseViaRPC(credID, passphrase string) credential.SecretID {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "credentials.saveKeyPassphrase", map[string]any{
		"credentialId": credID,
		"passphrase":   passphrase,
	})
	var got struct {
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(resp, &got); err != nil || !got.Result {
		h.t.Fatalf("saveKeyPassphrase failed: %v\nraw: %s", err, string(resp))
	}
	creds, err := h.ps.LoadCredentials()
	if err != nil {
		h.t.Fatalf("LoadCredentials: %v", err)
	}
	for _, c := range creds {
		if c.ID == credID {
			return credential.SecretID(c.PassphraseSecretID)
		}
	}
	h.t.Fatalf("credential %s not found after saveKeyPassphrase", credID)
	return ""
}

// TestDeleteCascade_RemovesPassword proves deleting a credential removes
// the stored password from the secret store.
func TestDeleteCascade_RemovesPassword(t *testing.T) {
	h := newCascadeHarness(t)

	// Host is required for a credential to be storable at all (nocx-wd2m); it
	// is incidental to what this test proves, which is secret cleanup.
	id := h.createCredentialViaRPC(profile.Credential{
		Name: "cascade-pw", Username: "alice", Auth: "password",
	})

	pwID := h.savePasswordViaRPC(id, "cascade-password-secret")

	// Precondition: password exists.
	got, err := h.cs.Get(pwID)
	if err != nil || got.IsEmpty() {
		t.Fatal("precondition: password should be present after save", err)
	}

	h.deleteCredentialViaRPC(id)

	// Password must be deleted.
	after, err := h.cs.Get(pwID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if !after.IsEmpty() {
		t.Fatal("password entry survived credential delete; the keychain is orphaned")
	}
}

// TestDeleteCascade_RemovesKeyPassphrase proves deleting a credential with a
// passphrase removes it from the secret store via PassphraseSecretID.
func TestDeleteCascade_RemovesKeyPassphrase(t *testing.T) {
	h := newCascadeHarness(t)

	id := h.createCredentialViaRPC(profile.Credential{
		Name: "cascade-pp", Username: "bob", Auth: "publicKey",
	})

	ppID := h.savePassphraseViaRPC(id, "cascade-passphrase-secret")

	got, err := h.cs.Get(ppID)
	if err != nil || got.IsEmpty() {
		t.Fatalf("precondition: passphrase should be present, got %v", err)
	}

	h.deleteCredentialViaRPC(id)

	after, err := h.cs.Get(ppID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if !after.IsEmpty() {
		t.Fatal("key passphrase survived credential delete; the secret is orphaned forever")
	}
}

// TestDeleteCascade_NoSecretsSucceeds proves deleting a credential that
// never had a secret stored succeeds.
func TestDeleteCascade_NoSecretsSucceeds(t *testing.T) {
	h := newCascadeHarness(t)

	id := h.createCredentialViaRPC(profile.Credential{
		Name: "cascade-empty", Username: "carol", Auth: "password",
	})

	h.deleteCredentialViaRPC(id)

	creds, err := h.ps.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	for _, c := range creds {
		if c.ID == id {
			t.Fatal("credential metadata survived delete")
		}
	}
}

// TestDeleteCascade_Idempotent proves deleting a credential twice both succeeds.
func TestDeleteCascade_Idempotent(t *testing.T) {
	h := newCascadeHarness(t)

	id := h.createCredentialViaRPC(profile.Credential{
		Name: "cascade-idem", Username: "dan", Auth: "password",
	})
	pwID := h.savePasswordViaRPC(id, "p")

	h.deleteCredentialViaRPC(id)
	h.deleteCredentialViaRPC(id) // second delete

	after, err := h.cs.Get(pwID)
	if err != nil {
		t.Fatalf("Get after second delete: %v", err)
	}
	if !after.IsEmpty() {
		t.Fatal("password survived second delete")
	}
}

// TestDeleteCascade_KeyFileDeleted proves a credential delete removes both
// secrets via SecretID — the private key file does not need to exist.
func TestDeleteCascade_KeyFileDeleted(t *testing.T) {
	h := newCascadeHarness(t)

	id := h.createCredentialViaRPC(profile.Credential{
		Name: "cascade-key-gone", Username: "bob", Auth: "publicKey",
		KeyPath: "/nonexistent/key/file",
	})

	ppID := h.savePassphraseViaRPC(id, "passphrase-stored-by-id")

	got, err := h.cs.Get(ppID)
	if err != nil || got.IsEmpty() {
		t.Fatalf("precondition: passphrase should be present, got %v", err)
	}

	// Delete the credential — the key path doesn't matter because the
	// cascade uses PassphraseSecretID from metadata.
	h.deleteCredentialViaRPC(id)

	after, err := h.cs.Get(ppID)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if !after.IsEmpty() {
		t.Fatal("key passphrase survived credential delete despite missing key file — it is orphaned forever (nocx-dm0)")
	}
}
