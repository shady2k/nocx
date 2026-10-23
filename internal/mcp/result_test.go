package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSchemaValidationNeverLoadsExternalReferences(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	schema, _ := json.Marshal(map[string]any{"$ref": server.URL + "/schema"})
	if err := validateToolArguments(json.RawMessage(`{}`), schema); err == nil {
		t.Fatal("external schema reference accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("schema compiler fetched an external reference")
	}
	if err := validateToolArguments(json.RawMessage(`{} {}`), json.RawMessage(`{"type":"object"}`)); err == nil {
		t.Fatal("trailing argument JSON accepted")
	}
}

func TestRawOutputSchemaFailureRemainsTerminal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "typed", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "typed", "inputSchema": map[string]any{"type": "object"}, "outputSchema": map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "wrong type"}}, "structuredContent": map[string]any{"value": 123}}
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	a := discoverFor(t, manager, activationFor(t, httpServerRecord(server.URL)))
	if _, err := invokeEcho(t.Context(), manager, a, "schema"); err == nil || IsRecoverableError(err) {
		t.Fatalf("schema failure lost terminal meaning: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("schema scenario did not reach actual remote call")
	}
	_, err := manager.Invoke(t.Context(), Invocation{RunID: "large", Activation: a, RemoteTool: a.Tools[0].Name, DescriptorDigest: a.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{"v":"` + strings.Repeat("x", maxArgumentsBytes) + `"}`)})
	if !errors.Is(err, ErrSchema) || calls.Load() != 1 {
		t.Fatal("oversized arguments activated server", err)
	}
}

func TestStatefulDiscoveryDeletesRemoteSession(t *testing.T) {
	var deletions atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "stateful", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletions.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer fixture.Close()
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	if _, err := manager.Refresh(t.Context(), activationFor(t, httpServerRecord(fixture.URL))); err != nil {
		t.Fatal(err)
	}
	if deletions.Load() != 1 {
		t.Fatalf("discovery teardown DELETE count=%d", deletions.Load())
	}
}
