package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	internalLog "github.com/shady2k/nocx/internal/log"
)

func TestWireTap_LogsOneStructuralOfferPerRun(t *testing.T) {
	var logs bytes.Buffer
	logger := internalLog.NewSlogAdapter(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	grant := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionAsk},
		Disclose:          content.EffectRow{Decision: content.DecisionAsk},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionAsk},
		Delegate:          content.EffectRow{Decision: content.DecisionAsk},
	}.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})
	ctx := WithWireToolOfferState(context.Background(), "run-7", &grant, NewWireToolOfferState())
	body := `{"tools":[{"type":"function","function":{"name":"session.run"}},{"type":"function","function":{"name":"session.read"}}]}`
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if _, err := io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	tap := newWireTapWith(inner, "", nil, logger)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tap.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("log = %q: %v", logs.String(), err)
	}
	if record["msg"] != "agent ask: tools offered" {
		t.Fatalf("message = %v, want structural offer", record["msg"])
	}
	if record["run"] != "run-7" {
		t.Fatalf("run = %v, want run-7", record["run"])
	}
	if record["count"] != float64(2) {
		t.Fatalf("count = %v, want 2", record["count"])
	}
	tools, ok := record["tools"].([]any)
	if !ok || len(tools) != 2 || tools[0] != "session.run" || tools[1] != "session.read" {
		t.Fatalf("tools = %v, want [session.run session.read]", record["tools"])
	}
	effects, ok := record["effects"].([]any)
	if !ok || len(effects) != 7 || effects[0] != "observe" || effects[2] != "mutate-destructive" {
		t.Fatalf("effects = %v, want all seven permitted effects", record["effects"])
	}
	scopes, ok := record["scopes"].([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("scopes = %v, want one session scope", record["scopes"])
	}
	scope, ok := scopes[0].(map[string]any)
	if !ok || scope["kind"] != "session" || scope["id"] != "session-1" {
		t.Fatalf("scope = %v, want session/session-1", scopes[0])
	}
	if strings.Contains(logs.String(), "question") || strings.Contains(logs.String(), "arguments") || strings.Contains(logs.String(), "output") {
		t.Fatalf("structural offer log contains sensitive prose fields: %s", logs.String())
	}
}

func TestClientAsk_TagsToolOfferOnModelRequest(t *testing.T) {
	var logs bytes.Buffer
	logger := internalLog.NewSlogAdapter(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	_, srv := newFakeOpenAI(nil)
	defer srv.Close()
	policy := content.EffectPolicy{
		Observe:           content.EffectRow{Decision: content.DecisionAsk},
		MutateReversible:  content.EffectRow{Decision: content.DecisionAsk},
		MutateDestructive: content.EffectRow{Decision: content.DecisionAsk},
		PrivilegeChange:   content.EffectRow{Decision: content.DecisionAsk},
		Disclose:          content.EffectRow{Decision: content.DecisionAsk},
		CrossBoundary:     content.EffectRow{Decision: content.DecisionAsk},
		Delegate:          content.EffectRow{Decision: content.DecisionAsk},
	}
	grant := policy.AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})
	cl, err := newClientWithoutSkillRoots(logger, os.DirFS(realToolsFS), nil, content.Floor{})
	if err != nil {
		t.Fatal(err)
	}
	p := testAskParams(srv.URL)
	p.Grant = &grant
	p.KnownMaterial = &fakeKnownMaterial{}
	p.AttemptLedger = &fakeLedger{}
	p.RunID = "run-7"
	p.TurnEntryID = "entry-7"
	if err := cl.Ask(WithWireToolOfferState(context.Background(), p.RunID, p.Grant, NewWireToolOfferState()), p, func(AskEvent) error { return nil }); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("log = %q: %v", logs.String(), err)
	}
	if record["run"] != "run-7" || record["count"] != float64(3) {
		t.Fatalf("offer record = %v, want run-7 and three permitted tools", record)
	}
	tools, ok := record["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %v, want session.run", record["tools"])
	}
	foundRun := false
	for _, tool := range tools {
		if tool == "session.run" {
			foundRun = true
		}
	}
	if !foundRun {
		t.Fatalf("tools = %v, want default-grant session.run", tools)
	}
}

func TestWireTap_LogsOfferOncePerRun(t *testing.T) {
	var logs bytes.Buffer
	logger := internalLog.NewSlogAdapter(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	grant := (content.EffectPolicy{Observe: content.EffectRow{Decision: content.DecisionPermit}}).AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(req.Body)
		return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	tap := newWireTapWith(inner, "", nil, logger)
	state7 := NewWireToolOfferState()
	req, err := http.NewRequestWithContext(
		WithWireToolOfferState(context.Background(), "run-7", &grant, state7),
		http.MethodPost, "https://example.test/v1", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tap.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	body := `{"tools":[{"function":{"name":"session.run"}}]}`
	for _, runID := range []string{"run-7", "run-7", "run-8"} {
		req, err := http.NewRequestWithContext(
			WithWireToolOfferState(context.Background(), runID, &grant, func() *WireToolOfferState {
				if runID == "run-7" {
					return state7
				}
				return NewWireToolOfferState()
			}()),
			http.MethodPost, "https://example.test/v1", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tap.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("offer log lines = %d, want one for each run: %s", len(lines), logs.String())
	}
	for i, want := range []string{"run-7", "run-8"} {
		var record map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &record); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if record["run"] != want {
			t.Fatalf("line %d run = %v, want %s", i, record["run"], want)
		}
	}
}

func TestWireTap_LogsEmptyStructuralOffer(t *testing.T) {
	var logs bytes.Buffer
	logger := internalLog.NewSlogAdapter(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	grant := (content.EffectPolicy{}).AsGrant([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-1"}})
	inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_, _ = io.ReadAll(req.Body)
		return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	tap := newWireTapWith(inner, "", nil, logger)
	req, err := http.NewRequestWithContext(
		WithWireToolOfferState(context.Background(), "run-empty", &grant, NewWireToolOfferState()),
		http.MethodPost, "https://example.test/v1", strings.NewReader(`{"messages":[],"tools":null}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tap.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("log = %q: %v", logs.String(), err)
	}
	if record["msg"] != "agent ask: tools offered" || record["count"] != float64(0) {
		t.Fatalf("offer record = %v, want empty structural offer", record)
	}
	if tools, ok := record["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("tools = %v, want []", record["tools"])
	}
}
