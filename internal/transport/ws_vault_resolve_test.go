package transport

// vault.resolveLine — the reference seam ({{secret:NAME}} in a command
// line). These tests drive the real handler through the real socket against
// a real vault: a password minted the way the Secrets page mints one, then
// resolved by the name the inventory reports. The wire is a party to the
// contract — the resolved value reaches the caller and nowhere else.

import (
	"encoding/json"
	"strings"
	"testing"
)

type resolveLineResult struct {
	Line string `json:"line"`
	Refs []struct {
		Name     string `json:"name"`
		Resolved bool   `json:"resolved"`
	} `json:"refs"`
}

func (h *inventoryHarness) callResolveLine(line string) resolveLineResult {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "vault.resolveLine", map[string]any{"line": line})
	var result struct {
		Result resolveLineResult `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		h.t.Fatalf("vault.resolveLine unmarshal: %v\nraw: %s", err, string(resp))
	}
	return result.Result
}

func (h *inventoryHarness) callResolveLineError() (code int, message string, reason string) {
	h.t.Helper()
	resp := jsonrpcCall(h.t, h.conn, "vault.resolveLine", map[string]any{"line": "{{secret:x}}"})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
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
	return errResult.Error.Code, errResult.Error.Message, errResult.Error.Data.Reason
}

// A name the inventory reports resolves to the stored value: the line comes
// back substituted, and the ref says the name resolved. The value appears
// in the line (that is the whole point — it is going to the PTY) and
// nowhere else in the response.
func TestVaultResolveLine_ResolvesByName(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	// A key-shaped literal is the point of the test: resolveLine must put the
	// real value in the line. It is a fixture, not a credential.
	secretValue := "sk-proj-abcdef1234567890" //nolint:gosec // G101: test fixture
	h.mintPassword(secretValue, "prod-api-key")

	got := h.callResolveLine(`curl -H "Authorization: Bearer {{secret:prod-api-key}}" https://api.example.com`)
	if want := `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api.example.com`; got.Line != want {
		t.Errorf("line = %q, want %q", got.Line, want)
	}
	if len(got.Refs) != 1 || got.Refs[0].Name != "prod-api-key" || !got.Refs[0].Resolved {
		t.Errorf("refs = %+v, want [{prod-api-key true}]", got.Refs)
	}
}

// A name the vault does not hold is reported, never silently left as
// literal text: the ref says resolved=false and the line keeps the literal
// reference so the caller can surface it.
func TestVaultResolveLine_UnresolvedNameReported(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	line := "echo {{secret:no-such-secret}}"
	got := h.callResolveLine(line)
	if got.Line != line {
		t.Errorf("line = %q, want the literal reference kept (%q)", got.Line, line)
	}
	if len(got.Refs) != 1 || got.Refs[0].Name != "no-such-secret" || got.Refs[0].Resolved {
		t.Errorf("refs = %+v, want [{no-such-secret false}]", got.Refs)
	}
}

// A line with one resolvable and one unresolvable reference resolves the
// former and reports the latter; both facts come back in one response.
func TestVaultResolveLine_MixedRefs(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	h.mintPassword("hunter2-strong-value", "db-password")

	got := h.callResolveLine(`run --password {{secret:db-password}} --other {{secret:ghost}}`)
	if !strings.Contains(got.Line, "hunter2-strong-value") || strings.Contains(got.Line, "{{secret:db-password}}") {
		t.Errorf("line = %q, want the db-password reference substituted", got.Line)
	}
	if !strings.Contains(got.Line, "{{secret:ghost}}") {
		t.Errorf("line = %q, want the ghost reference left literal", got.Line)
	}
	if len(got.Refs) != 2 {
		t.Fatalf("refs = %+v, want two", got.Refs)
	}
	if !got.Refs[0].Resolved || got.Refs[1].Resolved {
		t.Errorf("refs = %+v, want [true false]", got.Refs)
	}
}

// A line with no references is identity: the vault is not consulted, the
// refs list is [] never null.
func TestVaultResolveLine_NoRefs(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()

	got := h.callResolveLine("ls -la /srv")
	if got.Line != "ls -la /srv" {
		t.Errorf("line = %q, want identity", got.Line)
	}
	if got.Refs == nil || len(got.Refs) != 0 {
		t.Errorf("refs = %+v, want non-nil empty", got.Refs)
	}
}

func TestVaultResolveLine_SealedVault(t *testing.T) {
	h := newInventoryHarness(t)
	h.setupAndUnseal()
	h.v.Seal() // the vault is now sealed, not merely uninitialized

	// The sealed vault answers with the canonical sealed error — code
	// -32001, reason vault-sealed — the shape the renderer's dispatcher
	// turns into the unlock prompt (ADR-0032). Nothing blocks on an ask:
	// the answer is immediate, and no prompt was raised.
	code, message, reason := h.callResolveLineError()
	if code != vaultSealedCode {
		t.Errorf("code = %d, want %d (vault-sealed)", code, vaultSealedCode)
	}
	if reason != "vault-sealed" {
		t.Errorf("reason = %q, want vault-sealed", reason)
	}
	if !strings.Contains(message, "sealed") {
		t.Errorf("message = %q, want it to say the vault is sealed", message)
	}
	assertNoPendingAsk(t, h.ws)
}

// An uninitialized vault is its own actionable error, not a generic one.
func TestVaultResolveLine_UninitializedVault(t *testing.T) {
	h := newInventoryHarness(t)
	code, _, reason := h.callResolveLineError()
	if code != -32000 || reason != "vault-uninitialized" {
		t.Errorf("code/reason = %d/%q, want -32000/vault-uninitialized", code, reason)
	}
}

// Without the profile/group stores the method degrades to -32601, the same
// honest answer vault.inventory gives in that state.
func TestVaultResolveLine_NotWired(t *testing.T) {
	ws, stop := newVaultWSServer(t, newFakeVaultLifecycle())
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "vault.resolveLine", map[string]any{"line": "x {{secret:y}}"}, 1)
	if resp.Error == nil {
		t.Fatal("expected error, got success")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601", resp.Error.Code)
	}
}
