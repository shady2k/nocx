package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
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

func TestAgentAsk_NoKeyEndpointAnswersWithoutAuthorization(t *testing.T) {
	auth := make(chan string, 1)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		streamOKChunks(w)
	}))
	defer model.Close()

	client, err := assistant.NewClient(nil)
	if err != nil {
		t.Fatalf("assistant.NewClient: %v", err)
	}
	h := newAskHarness(t, client)
	e, code := decodeEndpointResult(t, jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name":    "Local",
		"baseUrl": model.URL + "/v1",
		"schema":  "openai-compatible",
		"noKey":   true,
		"models":  []map[string]any{{"name": "local-model"}},
	}))
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": e.ID,
		"model":      "local-model",
	})) {
		t.Fatal("roles.assign refused the no-key endpoint")
	}
	sid := openLocalSession(t, h.conn)
	res, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":           "ask-nokey",
		"sessionId":       sid,
		"question":        "answer locally",
		"cwd":             "/repo",
		"attachedContent": []any{},
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	if res.State != "prepared" {
		t.Fatalf("agent.ask state = %q, want prepared", res.State)
	}
	for range 2 {
		readNotification(t, h.conn, "agent.runDelta", 5*time.Second)
	}
	readNotification(t, h.conn, "agent.runState", 5*time.Second)
	select {
	case got := <-auth:
		if got != "" {
			t.Fatalf("Authorization = %q, want no Authorization header", got)
		}
	default:
		t.Fatal("the model server did not receive the ask")
	}
}
