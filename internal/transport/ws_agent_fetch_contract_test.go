package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

type transportFetchTextFetcher struct {
	result apifetch.TextDocument
}

func (f transportFetchTextFetcher) FetchText(context.Context, apifetch.TextRequest) (apifetch.TextDocument, error) {
	return f.result, nil
}

// loadFetchResultSchema reads the result half of the unified tool contract.
// Tool declarations keep params and results in one document so the registry
// can reject either half when its declaration is incomplete; this helper
// validates the exact result half without inventing a second schema.
func loadFetchResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contractDir, "tools", "fetch.url.schema.json"))
	if err != nil {
		t.Fatalf("read fetch.url contract: %v", err)
	}
	var envelope struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if decodeErr := json.Unmarshal(raw, &envelope); decodeErr != nil {
		t.Fatalf("decode fetch.url contract: %v", decodeErr)
	}
	result, ok := envelope.Defs["result"]
	if !ok {
		t.Fatal("fetch.url contract has no $defs.result")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("decode fetch.url result schema: %v", err)
	}
	const name = "https://nocx.local/contracts/tools/fetch.url.result.schema.json"
	compiler := jsonschema.NewCompiler()
	if addErr := compiler.AddResource(name, doc); addErr != nil {
		t.Fatalf("add fetch.url result schema: %v", addErr)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		t.Fatalf("compile fetch.url result schema: %v", err)
	}
	return schema
}

// TestFetchURLResult_OverTheWireConformsToContract drives the real agent.ask
// method over the real WebSocket. The fake agent only answers after it receives
// the executed tool result, and that exact result is validated against the
// result schema. A DTO-only check could pass while executeFetchURL sent a
// different shape or omitted a field.
func TestFetchURLResult_OverTheWireConformsToContract(t *testing.T) {
	schema := loadFetchResultSchema(t)
	fetcher := transportFetchTextFetcher{result: apifetch.TextDocument{
		URL: "https://example.test/page", ContentType: "text/html", Text: "hello from the page",
	}}
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "fetch.url", Arguments: map[string]any{"url": "https://example.test/page"}},
		{Kind: protocol.StepAnswer, Text: "The page says hello from the fetched result."},
		{Kind: protocol.StepEnd},
	}},
		WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(fetcher))
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "fetch-contract", "sessionId": sid,
		"question": "fetch the page and tell me what it says", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	call := readNotification(t, h.conn, "agent.runToolCall", 15*time.Second)
	var announced struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(call, &announced); err != nil {
		t.Fatalf("decode fetch.url call: %v", err)
	}
	if announced.Tool != "fetch.url" {
		t.Fatalf("announced tool = %q, want fetch.url", announced.Tool)
	}
	delta := readNotification(t, h.conn, "agent.runDelta", 15*time.Second)
	if !strings.Contains(string(delta), "hello from the fetched result") {
		t.Fatalf("answer delta = %s, want the agent's answer", delta)
	}
	readNotification(t, h.conn, "agent.runState", 15*time.Second)
	toolResult := h.toolResult()
	if toolResult == "" {
		t.Fatal("agent received no fetch.url tool result")
	}
	rawResult := toolResult
	validateJSON(t, schema, []byte(rawResult), "fetch.url result (real socket path)")
	if !strings.Contains(rawResult, `"text":"hello from the page"`) {
		t.Fatalf("fetch.url result = %s, want the fetched page text", rawResult)
	}
}
