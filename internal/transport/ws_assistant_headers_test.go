package transport

// The probe's draft custom headers (bead nocx-lyyk, acceptance 5): a header
// whose value is a vault reference resolves to material at probe time, a
// literal rides as-is, and an unresolvable row is a refused probe RESULT
// naming the header — never a no-header dial that would lie about the
// endpoint. All driven over the real socket with a stub engine.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

func (h *assistantHarness) mintSecret(t *testing.T, name string) (string, string) {
	t.Helper()
	id, err := h.v.CreateNamed(t.Context(), credential.NewSecret("material-"+name), vault.SecretMeta{
		Name: name,
		Kind: vault.KindPassword,
	})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	return string(id), vault.RowFor(id)
}

func TestEndpointsProbe_DraftHeaderRowResolvesToMaterial(t *testing.T) {
	var got assistant.ProbeParams
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			got = p
			return assistant.ProbeResult{OK: true, Kind: assistant.ProbeModel}, nil
		},
	})
	h.setupAndUnseal()
	_, row := h.mintSecret(t, "azure key")

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name":    "Azure",
		"baseUrl": "https://api.example.com/v1",
		"noKey":   true,
		"model":   "gpt-4o",
		"headers": []map[string]any{
			{"name": "api-key", "value": nil, "secret": row},
			{"name": "HTTP-Referer", "value": "nocx", "secret": nil},
		},
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("endpoints.probe: %s", raw)
	}
	if len(got.Headers) != 2 {
		t.Fatalf("engine received %+v, want 2 resolved headers", got.Headers)
	}
	if got.Headers[0].Name != "api-key" || got.Headers[0].Value != "material-azure key" {
		t.Errorf("headers[0] = %+v, want the RESOLVED material of the referenced row", got.Headers[0])
	}
	if got.Headers[1].Name != "HTTP-Referer" || got.Headers[1].Value != "nocx" {
		t.Errorf("headers[1] = %+v, want the literal", got.Headers[1])
	}
	// The wire never carried the material in the result (the result has no
	// headers at all, and nothing leaked).
	if strings.Contains(string(raw), "material-azure key") {
		t.Fatalf("the probe result leaked secret material: %s", raw)
	}
}

func TestEndpointsProbe_UnknownHeaderRowIsARefusedResultNamingIt(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			t.Fatal("the engine must not be called for an unresolvable header row")
			return assistant.ProbeResult{}, nil
		},
	})
	h.setupAndUnseal()

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name":    "Azure",
		"baseUrl": "https://api.example.com/v1",
		"noKey":   true,
		"model":   "gpt-4o",
		"headers": []map[string]any{
			{"name": "api-key", "value": nil, "secret": "secrow:deadbeefdeadbeefdeadbeefdeadbeef"},
		},
	})
	var env struct {
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("an unknown row is a probe RESULT, not an RPC error: %s", raw)
	}
	var result assistant.ProbeResult
	if err := json.Unmarshal(mustResult(t, raw), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.OK {
		t.Fatalf("probe = %+v, want !OK for an unresolvable header row", result)
	}
	if !strings.Contains(result.Error, "api-key") {
		t.Errorf("error = %q, want it to name the header", result.Error)
	}
	if strings.Contains(result.Error, "deadbeef") {
		t.Errorf("error = %q, want the row name, not the opaque handle", result.Error)
	}
	if last := h.probes.Last(); last == nil || last.OK {
		t.Fatalf("a refused header probe must be recorded, got %+v", last)
	}
}

// A secret-valued header whose secret is deleted while the unlock is in
// flight: the Operation stance unlocks, the read then finds the secret gone,
// and the probe must be a refused RESULT naming the header — never a generic
// -32603 toast (the review bug). The engine must not be called at all.
func TestEndpointsProbe_DeletedHeaderSecretAfterUnlockIsARefusedResult(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			t.Fatal("the engine must not be called for a deleted header secret")
			return assistant.ProbeResult{}, nil
		},
	})
	h.setupAndUnseal()
	id, row := h.mintSecret(t, "azure key")

	h.v.Seal()
	// The unlock succeeds AND deletes the header's secret before the read
	// returns: the exact "deleted in parallel with the unlock" shape.
	h.v.SetUnlockRequester(unlockRequesterFunc(func(ctx context.Context, reason string) error {
		if err := h.v.Unseal(ctx, vault.UnsealRequest{Passphrase: "test"}); err != nil {
			return err
		}
		return h.v.Delete(ctx, credential.SecretID(id))
	}))

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name":    "Azure",
		"baseUrl": "https://api.example.com/v1",
		"noKey":   true,
		"model":   "gpt-4o",
		"headers": []map[string]any{
			{"name": "api-key", "value": nil, "secret": row},
		},
	})
	if isErrorResponse(t, raw) {
		t.Fatalf("a deleted header secret after unlock is a probe RESULT, not an RPC error: %s", raw)
	}
	var result assistant.ProbeResult
	if err := json.Unmarshal(mustResult(t, raw), &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.OK {
		t.Fatalf("probe = %+v, want !OK for a deleted header secret", result)
	}
	if !strings.Contains(result.Error, "api-key") {
		t.Errorf("error = %q, want it to name the header", result.Error)
	}
	if last := h.probes.Last(); last == nil || last.OK {
		t.Fatalf("a refused header probe must be recorded, got %+v", last)
	}
}

func mustResult(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.Result
}

// TestEndpointsProbe_RefusesControlCharacterHeaderBeforeAnyRequest (bead
// nocx-lyyk, acceptance 2): a header name or value carrying CR/LF or a
// control character is refused by the probe validator BEFORE anything is
// dialled — the engine is never called.
func TestEndpointsProbe_RefusesControlCharacterHeaderBeforeAnyRequest(t *testing.T) {
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(ctx context.Context, p assistant.ProbeParams) (assistant.ProbeResult, error) {
			t.Fatal("the engine must not be called for a control-character header")
			return assistant.ProbeResult{}, nil
		},
	})
	h.setupAndUnseal()

	for name, value := range map[string]string{
		"crlf name":  "X-Bad\r\nName",
		"crlf value": "line\nbreak",
		"tab value":  "tab\tvalue",
	} {
		headers := []map[string]any{{"name": "X-Test", "value": value, "secret": nil}}
		if strings.Contains(name, "name") {
			headers = []map[string]any{{"name": value, "value": "v", "secret": nil}}
		}
		raw := probeAnswer(t, h, map[string]any{
			"name":    "Local",
			"baseUrl": "https://api.example.com/v1",
			"model":   "gpt-4o",
			"headers": headers,
		})
		if !strings.Contains(string(raw), "-32602") {
			t.Errorf("%s: probe = %s, want -32602 before any request", name, raw)
		}
	}
}

// probeAnswer sends one endpoints.probe and returns the first answer that is
// about the REQUEST. "Control plane busy" is not: the probe lane is
// deliberately non-blocking — a probe is a network call nobody queues, so a
// second one arriving while the first is in flight is refused rather than
// held — and the refusal says so, retryable with retryAfterMs 0.
//
// The catch is that the slot is freed by the TAIL of the previous probe's
// task, after that probe's response was already enqueued. So a caller that
// reads one answer and immediately sends the next can land in the window
// between the two, and on a loaded machine it does: this test sent three
// probes in a row and the second was told the plane was busy with a probe it
// had already been answered about (nocx-2h08 — the same response-precedes-
// release shape as profiles.tabbyExecute, here in a lane that refuses by
// design rather than waits).
//
// Retrying is what the renderer does with a retryable refusal, and it is the
// only correct wait here: the state being waited on is the lane answering
// about this request. wantWithin bounds a lane that never frees at all; it is
// not the mechanism, and a slower machine simply retries once more.
func probeAnswer(t *testing.T, h *assistantHarness, params map[string]any) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(wantWithin)
	for {
		raw := jsonrpcCall(t, h.conn, "endpoints.probe", params)
		if !strings.Contains(string(raw), `"reason":"control-saturated"`) {
			return raw
		}
		if time.Now().After(deadline) {
			t.Fatalf("the probe lane never freed: %s", raw)
		}
	}
}
