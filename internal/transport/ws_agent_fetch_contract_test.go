package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/apifetch"
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

type fetchResultProvider struct {
	mu       sync.Mutex
	bodies   []string
	result   chan struct{}
	once     sync.Once
	toolCall string
}

func (p *fetchResultProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(body))
	n := len(p.bodies)
	p.mu.Unlock()
	if n == 1 {
		streamToolCallChunk(w, "fetch.url", `{"url":"https://example.test/page"}`)
		return
	}
	tool := lifecycleBlockReadToolResult(string(body))
	p.mu.Lock()
	p.toolCall = tool
	p.mu.Unlock()
	p.once.Do(func() { close(p.result) })
	streamAnswerChunk(w, "The page says hello from the fetched result.")
}

func (p *fetchResultProvider) toolResult() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.toolCall
}

// TestFetchURLResult_OverTheWireConformsToContract drives the real agent.ask
// method over the real WebSocket. The provider only answers after it receives
// the executed tool result, and that exact result is validated against the
// result schema. A DTO-only check could pass while executeFetchURL sent a
// different shape or omitted a field.
func TestFetchURLResult_OverTheWireConformsToContract(t *testing.T) {
	schema := loadFetchResultSchema(t)
	provider := &fetchResultProvider{result: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer server.Close()

	fetcher := transportFetchTextFetcher{result: apifetch.TextDocument{
		URL: "https://example.test/page", ContentType: "text/html", Text: "hello from the page",
	}}
	h := newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(fetcher))
	h.createEndpointAt(server.URL)
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
	select {
	case <-provider.result:
	case <-time.After(15 * time.Second):
		t.Fatal("provider did not receive the executed fetch.url result")
	}
	toolResult := provider.toolResult()
	if toolResult == "" {
		t.Fatal("provider received no fetch.url tool result")
	}
	const openTag = "<tool-output>\n"
	const closeTag = "\n</tool-output>"
	start := strings.Index(toolResult, openTag)
	end := strings.LastIndex(toolResult, closeTag)
	if start < 0 || end <= start+len(openTag) {
		t.Fatalf("tool result = %q, want a tool-output JSON envelope", toolResult)
	}
	rawResult := strings.TrimSpace(toolResult[start+len(openTag) : end])
	validateJSON(t, schema, []byte(rawResult), "fetch.url result (real socket path)")
	if !strings.Contains(rawResult, `"text":"hello from the page"`) {
		t.Fatalf("fetch.url result = %s, want the fetched page text", rawResult)
	}

	delta := readNotification(t, h.conn, "agent.runDelta", 15*time.Second)
	if !strings.Contains(string(delta), "hello from the fetched result") {
		t.Fatalf("answer delta = %s, want the provider's answer", delta)
	}
	readNotification(t, h.conn, "agent.runState", 15*time.Second)
}
