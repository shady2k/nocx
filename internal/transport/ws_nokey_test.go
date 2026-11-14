package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/providertest"
)

func TestEndpointsProbe_RequiredCredentialMissingIsRefused(t *testing.T) {
	called := false
	h := newAssistantHarness(t, &stubAssistantClient{
		probe: func(context.Context, assistant.ProbeParams) (assistant.ProbeResult, error) {
			called = true
			return assistant.ProbeResult{OK: true}, nil
		},
	})
	created := h.createEndpoint(t, map[string]any{
		"name":    "Remote",
		"baseUrl": "https://api.example.com/v1",
		"schema":  "openai-compatible",
		"models":  []map[string]any{{"name": "gpt-4o"}},
	})

	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"endpointId": created.ID,
		"name":       "Remote",
		"baseUrl":    "https://api.example.com/v1",
		"key":        "",
		"noKey":      false,
		"model":      "gpt-4o",
	})
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal probe response: %v", err)
	}
	var result assistant.ProbeResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("unmarshal probe result: %v", err)
	}
	if result.OK {
		t.Fatalf("probe result = %+v, want refused result", result)
	}
	if result.Error != "the endpoint's credential is unavailable" {
		t.Fatalf("probe refusal = %q, want unavailable credential", result.Error)
	}
	if called {
		t.Fatal("probe engine called without the required credential")
	}
}

// Endpoint probes still own provider HTTP; agent.ask now runs an installed agent.
func TestEndpointsProbe_NoKeyEndpointAnswersWithoutAuthorization(t *testing.T) {
	auth := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !providertest.ConsumeRequestBeforeResponse(w, r) {
			return
		}
		auth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	h := newAssistantHarness(t, &stubAssistantClient{probe: client.Probe})
	raw := jsonrpcCall(t, h.conn, "endpoints.probe", map[string]any{
		"name": "Local", "baseUrl": model.URL + "/v1", "schema": "openai-compatible", "noKey": true, "model": "local-model",
	})
	var env struct {
		Result assistant.ProbeResult `json:"result"`
		Error  *jsonrpcErrorObj      `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil || !env.Result.OK {
		t.Fatalf("no-key probe: %s", raw)
	}
	select {
	case got := <-auth:
		if got != "" {
			t.Fatalf("Authorization = %q, want no Authorization header", got)
		}
	default:
		t.Fatal("the model server did not receive the probe")
	}
}

func TestAgentAsk_AnswersWithoutEndpointCredential(t *testing.T) {
	h := newAskHarness(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepAnswer, Text: "local agent answer"}, {Kind: protocol.StepEnd},
	}})
	sid := openLocalSession(t, h.conn)
	if _, rpcErr := askOverWire(t, h.conn, map[string]any{"askId": "without-credential", "sessionId": sid, "question": "answer locally", "cwd": "/repo"}, 2); rpcErr != nil {
		t.Fatalf("agent.ask: %+v", rpcErr)
	}
	delta := readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	if !strings.Contains(string(delta), "local agent answer") {
		t.Fatalf("answer: %s", delta)
	}
	state := readNotification(t, h.conn, "agent.runState", 5*time.Second)
	if !strings.Contains(string(state), `"state":"completed"`) {
		t.Fatalf("state: %s", state)
	}
}
