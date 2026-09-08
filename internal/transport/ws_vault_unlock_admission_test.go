package transport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// THE UNLOCK A PERSON IS SHOWN MUST BE ANSWERABLE (nocx-o3606).
//
// An operation that needs secret material raises the vault's own prompt and
// waits. The renderer answers that prompt by calling vault.unseal and only
// then vault.unlockResolved — the resolution is sent from UnlockDialog's
// onUnsealed, so an unseal that does not succeed means the waiter is never
// released.
//
// vault.unseal runs under the vault gate (capacity 1, a one-second wait). So
// if the operation that raised the prompt is still holding that gate, the
// unseal it needs is refused "Control plane busy" and the dialog cannot be
// satisfied at all: the person is shown a door with no handle, and the only
// way out is to cancel the thing they asked for.
//
// This is the whole reason ADR-0032's amendment says no capability admission
// may block on unlock. It is asserted here, over the wire, on the path that
// holds a domain gate — agent.ask holds none, so an ask test cannot see it.
func TestSecretOperation_TheUnlockItRaisesCanBeAnswered(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	pem, _ := testEncryptedKeyPEM(t, "pass123")
	keyRow, _, _ := h.mintKeyMaterial(pem, "ops key")

	// The vault seals while the person is still typing into the key
	// passphrase prompt — the ordinary way in, since auto-seal is a
	// feature and the prompt is a dialog somebody types into.
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 77, "method": "secrets.saveKeyPassphrase",
		"params": map[string]any{
			"keyRow": keyRow, "passphrase": "pass123", "name": "ops key passphrase",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if werr := h.conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write secrets.saveKeyPassphrase: %v", werr)
	}

	frame := readUnlockRequestFrame(t, h.conn)

	// The renderer's unlock dialog, in the order it actually runs.
	unseal := jsonrpcCallWithID(t, h.conn, "vault.unseal", map[string]any{
		"means": "passphrase", "secret": "test",
	}, 78)
	if isErrorResponse(t, unseal) {
		t.Fatalf("the unseal that answers the raised unlock was refused: %s", unseal)
	}
	answerUnlock(t, h.conn, frame.RequestID, "unsealed")

	saved, err := awaitFrame(h.conn, time.Now().Add(wantWithin), isResponseTo(77))
	if err != nil {
		t.Fatalf("read secrets.saveKeyPassphrase response: %v", err)
	}
	if isErrorResponse(t, saved) {
		t.Fatalf("the operation did not continue after the unlock: %s", saved)
	}
	var out struct {
		Result struct {
			Row string `json:"row"`
		} `json:"result"`
	}
	if uerr := json.Unmarshal(saved, &out); uerr != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", uerr, saved)
	}
	if out.Result.Row == "" {
		t.Fatalf("the passphrase was not stored: %s", saved)
	}
}

// And the other half of the same journey: the person decides not to unlock.
//
// The operation must come back as the cancellation it was — the reason the
// renderer's dispatcher turns into VaultOperationCancelledError, so a caller
// abandons quietly — and not as "load key material: vault is sealed", which
// would send the dispatcher round to raise the dialog the person just shut.
func TestSecretOperation_ADismissedUnlockIsTheCancellation(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	pem, _ := testEncryptedKeyPEM(t, "pass123")
	keyRow, _, _ := h.mintKeyMaterial(pem, "ops key")

	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 81, "method": "secrets.saveKeyPassphrase",
		"params": map[string]any{
			"keyRow": keyRow, "passphrase": "pass123", "name": "ops key passphrase",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if werr := h.conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write secrets.saveKeyPassphrase: %v", werr)
	}

	frame := readUnlockRequestFrame(t, h.conn)
	answerUnlock(t, h.conn, frame.RequestID, "cancelled")

	raw, err := awaitFrame(h.conn, time.Now().Add(wantWithin), isResponseTo(81))
	if err != nil {
		t.Fatalf("read secrets.saveKeyPassphrase response: %v", err)
	}
	var env struct {
		Error *struct {
			Code int             `json:"code"`
			Data *vaultErrorData `json:"data"`
		} `json:"error"`
	}
	if uerr := json.Unmarshal(raw, &env); uerr != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", uerr, raw)
	}
	if env.Error == nil {
		t.Fatalf("a dismissed unlock answered as a success: %s", raw)
	}
	if env.Error.Data == nil || env.Error.Data.Reason != "vault-operation-cancelled" {
		t.Fatalf("reason = %+v, want vault-operation-cancelled: %s", env.Error.Data, raw)
	}
	if env.Error.Code == vaultSealedCode {
		t.Fatalf("a dismissed unlock answered as a sealed vault, which sends the "+
			"dispatcher back to raise the dialog just shut: %s", raw)
	}
}

// THE SAME INVARIANT, ON THE PATH THAT RESOLVES A MODEL (nocx-9fzkk).
//
// skills.audit resolves its endpoint's credential through the CONFIG
// operation, and that operation holds the vault gate as well as the config
// one (buildConfigOp takes both). So a resolution done inside its callback
// raises the unlock while holding the very gate vault.unseal needs, and the
// person is shown the "Unlock the vault to audit a skill" dialog whose
// Unlock button answers "Control plane busy".
//
// It is the identical defect the two tests above pin for the secret
// operation; it reappeared here because the rule lives in a comment on
// credentialResolver rather than in anything a handler must satisfy.
func TestSkillsAudit_TheUnlockItRaisesCanBeAnswered(t *testing.T) {
	client := &auditingClient{report: "It reads the station and curls example.test."}
	h := newAuditHarness(t, client)
	h.createEndpoint()

	// The vault seals while the card is open — auto-seal on idle is the
	// ordinary way a person arrives at this dialog.
	h.v.Seal()
	h.v.SetUnlockRequester(unlockRequesterFunc(h.ws.RequestUnlock))

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 91, "method": "skills.audit",
		"params": map[string]any{"name": "weather"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if werr := h.conn.WriteMessage(websocket.TextMessage, req); werr != nil {
		t.Fatalf("write skills.audit: %v", werr)
	}

	frame := readUnlockRequestFrame(t, h.conn)
	if frame.Reason == "" {
		t.Fatal("the unlock names no reason")
	}

	unseal := jsonrpcCallWithID(t, h.conn, "vault.unseal", map[string]any{
		"means": "passphrase", "secret": "test",
	}, 92)
	if isErrorResponse(t, unseal) {
		t.Fatalf("the unseal that answers the raised unlock was refused: %s", unseal)
	}
	answerUnlock(t, h.conn, frame.RequestID, "unsealed")

	raw, err := awaitFrame(h.conn, time.Now().Add(wantWithin), isResponseTo(91))
	if err != nil {
		t.Fatalf("read skills.audit response: %v", err)
	}
	if isErrorResponse(t, raw) {
		t.Fatalf("the audit did not continue after the unlock: %s", raw)
	}
	var out struct {
		Result struct {
			Verdict string `json:"verdict"`
		} `json:"result"`
	}
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", uerr, raw)
	}
	if out.Result.Verdict == "" {
		t.Fatalf("the audit answered without a verdict: %s", raw)
	}
}
