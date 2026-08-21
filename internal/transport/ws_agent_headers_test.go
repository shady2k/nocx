package transport

// The ask path resolves the endpoint's stored header references at stream
// time (bead nocx-lyyk, acceptance 5): a secret-valued header reaches the
// engine as its material, alongside the credential, and a literal rides
// as-is.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
)

func TestAgentAsk_EndpointHeadersResolveAtStreamTime(t *testing.T) {
	client := &scriptedAssistantClient{deltas: []string{"ok"}}
	h := newAskHarness(t, client)

	// One vault secret the header will reference.
	id, err := h.v.CreateNamed(t.Context(), credential.NewSecret("tenant-material"), vault.SecretMeta{
		Name: "azure tenant",
		Kind: vault.KindPassword,
	})
	if err != nil {
		t.Fatalf("CreateNamed: %v", err)
	}
	row := vault.RowFor(id)

	raw := jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name":    "Local",
		"baseUrl": "http://127.0.0.1:11434/v1",
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "qwen3"}},
		"headers": []map[string]any{
			{"name": "X-Tenant", "value": nil, "secret": row},
			{"name": "HTTP-Referer", "value": "nocx", "secret": nil},
		},
	})
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("endpoints.create unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("endpoints.create: code %d\nraw: %s", env.Error.Code, raw)
	}
	// The ask resolves through the ANSWERING ROLE (bead nocx-e6kn2): the
	// fixture assigns the role to the endpoint it just created.
	created, code := decodeEndpointResult(t, raw)
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	assign := jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": created.ID,
		"model":      "qwen3",
	})
	if isErrorResponse(t, assign) {
		t.Fatalf("roles.assign: %s", assign)
	}
	sid := openLocalSession(t, h.conn)
	frameID, errObj := captureFrameOverWire(t, h.conn, frozenWireFrame(sid, "frame-1"), 1)
	if errObj != nil {
		t.Fatalf("captureFrame: %+v", errObj)
	}
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "ask-headers",
		"sessionId": sid,
		"question":  "q",
		"cwd":       "/repo",
		"references": []any{
			map[string]any{"frameId": frameID, "region": map[string]any{"rowStart": 0, "rowEnd": 2}},
		},
	}, 2); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	stRaw := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stRaw, &st); err != nil {
		t.Fatalf("runState unmarshal: %v", err)
	}
	if st.State != "completed" {
		t.Fatalf("runState = %q, want completed", st.State)
	}

	if len(client.receivedParams.Headers) != 2 {
		t.Fatalf("engine received %+v, want the 2 resolved headers", client.receivedParams.Headers)
	}
	got := map[string]string{}
	for _, hd := range client.receivedParams.Headers {
		got[hd.Name] = hd.Value
	}
	if got["X-Tenant"] != "tenant-material" {
		t.Errorf("X-Tenant = %q, want the RESOLVED material of the stored reference", got["X-Tenant"])
	}
	if got["HTTP-Referer"] != "nocx" {
		t.Errorf("HTTP-Referer = %q, want the literal", got["HTTP-Referer"])
	}
}
