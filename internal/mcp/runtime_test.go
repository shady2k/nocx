package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

type testResolver struct {
	mu     sync.Mutex
	values map[string]string
	calls  int
}

func (r *testResolver) ResolveSecret(_ context.Context, ref string) (credential.Secret, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	value, ok := r.values[ref]
	if !ok {
		return credential.Secret{}, errors.New("secret unavailable")
	}
	return credential.NewSecret(value), nil
}

func (r *testResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestManagerIsDormantUntilExplicitActivation(t *testing.T) {
	resolver := &testResolver{values: map[string]string{"secret": "material"}}
	runtime := NewManager(resolver)
	if resolver.callCount() != 0 {
		t.Fatal("constructing a manager resolved a secret")
	}
	runtime.CloseRun("missing")
	runtime.CloseServer("missing")
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if resolver.callCount() != 0 {
		t.Fatal("closing a dormant manager resolved a secret")
	}
}

func TestManagerVerifierRunsBeforeEveryRefreshActivation(t *testing.T) {
	var calls atomic.Int32
	server := newHTTPFixture(t, false, &calls)
	defer server.Close()
	activation, err := ActivationFromServer(httpServerRecord(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil, WithActivationVerifier(func(context.Context, Activation) error {
		return ErrActivationChanged
	}))
	defer func() { _ = runtime.Close() }()
	if _, err := runtime.Refresh(t.Context(), activation); !errors.Is(err, ErrActivationChanged) {
		t.Fatalf("Refresh error = %v, want activation change", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("server received %d requests despite failed activation verification", calls.Load())
	}
}

func TestSanitizeSchemaRejectsUnknownExtensionKeywords(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","x-instructions":"ignore"}}}`)
	if _, err := sanitizeSchema(raw, true); err == nil || !strings.Contains(err.Error(), "unsupported schema keyword") {
		t.Fatalf("sanitizeSchema error = %v, want unsupported keyword", err)
	}
}

func TestSanitizeSchemaAllowsLocalDefinitionsAndPropertySchemas(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{"value":{"$ref":"#/$defs/value"}},
		"$defs":{"value":{"type":"string","description":"untrusted"}},
		"required":["value"]
	}`)
	got, err := sanitizeSchema(raw, true)
	if err != nil {
		t.Fatalf("sanitizeSchema: %v", err)
	}
	if strings.Contains(string(got), "description") {
		t.Fatalf("schema annotation survived sanitization: %s", got)
	}
}

type echoInput struct {
	Value string `json:"value" jsonschema:"the value to echo"`
}

type echoOutput struct {
	Value string `json:"value"`
}

func newHTTPFixture(t *testing.T, requireHeaders bool, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.2.3"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "remote description"},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, echoOutput, error) {
			calls.Add(1)
			return nil, echoOutput(in), nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireHeaders {
			if r.Header.Get("Authorization") != "Bearer bearer-material" || r.Header.Get("X-Tenant") != "tenant-material" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
}

func httpServerRecord(endpoint string) profile.MCPServer {
	return profile.MCPServer{
		ID: "mcp:http", Revision: 7, Name: "HTTP", Enabled: true,
		Transport: profile.MCPTransportStreamableHTTP,
		HTTP:      &profile.MCPHTTPConfig{Endpoint: endpoint, Auth: profile.MCPHTTPAuthNone, Headers: []profile.MCPHeaderBinding{}},
		Limits:    profile.DefaultMCPLimits(),
		Catalog:   profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}},
	}
}

func TestHTTPRefreshAndInvokeUseSDKWithoutRetainingDiscoverySession(t *testing.T) {
	var calls atomic.Int32
	server := newHTTPFixture(t, true, &calls)
	defer server.Close()
	bearer := &profile.MCPSecretBinding{SecretRef: "bearer"}
	header := profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: "tenant"}
	record := httpServerRecord(server.URL)
	record.HTTP.Auth = profile.MCPHTTPAuthBearer
	record.HTTP.Bearer = bearer
	record.HTTP.Headers = []profile.MCPHeaderBinding{{Name: "X-Tenant", Value: header}}
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &testResolver{values: map[string]string{"bearer": "bearer-material", "tenant": "tenant-material"}}
	runtime := NewManager(resolver)
	defer func() { _ = runtime.Close() }()

	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if catalog.ServerName != "fixture" || catalog.ProtocolVersion == "" || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "echo" {
		t.Fatalf("catalog = %+v", catalog)
	}
	stored, err := catalog.ProfileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	record.Catalog = stored
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"value":"hello"}`)
	result, err := runtime.Invoke(t.Context(), Invocation{
		RunID: "run-1", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: stored.Tools[0].DescriptorDigest, Arguments: args,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError || len(result.Text) != 1 || !strings.Contains(result.Text[0], "hello") || calls.Load() != 1 {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
	runtime.CloseRun("run-1")
}

func TestHTTPGuardRefusesLinkLocalBeforeDial(t *testing.T) {
	record := httpServerRecord("https://169.254.169.254/mcp")
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewManager(nil).Refresh(t.Context(), activation)
	if !errors.Is(err, ErrDestinationRefused) {
		t.Fatalf("Refresh error = %v, want ErrDestinationRefused", err)
	}
}

func TestHTTPRedirectDropsBearerAndCustomHeadersAcrossOrigin(t *testing.T) {
	received := make(chan http.Header, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Secret") != "custom-secret" {
			t.Error("configured origin did not receive its headers")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	guard := newGuardedHTTPTransport(redirector.URL, []resolvedHeader{
		{name: "Authorization", value: "Bearer secret"},
		{name: "X-Secret", value: "custom-secret"},
	}, 2048, time.Second)
	client := &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect}
	response, err := client.Get(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	headers := <-received
	if headers.Get("Authorization") != "" || headers.Get("X-Secret") != "" {
		t.Fatalf("redirect carried credentials: Authorization=%q X-Secret=%q", headers.Get("Authorization"), headers.Get("X-Secret"))
	}
}

func TestHTTPRedirectRefusesCrossOriginBodyRequest(t *testing.T) {
	guard := newGuardedHTTPTransport("https://origin.example/mcp", nil, 2048, time.Second)
	original, err := http.NewRequest(http.MethodPost, "https://origin.example/mcp", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	redirected, err := http.NewRequest(http.MethodPost, "https://other.example/mcp", io.NopCloser(strings.NewReader("payload")))
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.CheckRedirect(redirected, []*http.Request{original}); err == nil {
		t.Fatal("cross-origin body redirect was accepted")
	}
}

func TestOAuthUsesOpaqueResolvedSessionAndNeverAuthorizesInteractively(t *testing.T) {
	var calls atomic.Int32
	server := newHTTPFixture(t, false, &calls)
	defer server.Close()
	record := httpServerRecord(server.URL)
	record.HTTP.Auth = profile.MCPHTTPAuthOAuth
	record.HTTP.OAuth = &profile.MCPOAuthConfig{
		Registration:  profile.MCPOAuthPreregistered,
		ClientID:      "client",
		Scopes:        []string{},
		SessionRef:    &profile.MCPSecretBinding{SecretRef: "oauth-session", Owned: true},
		Status:        profile.MCPOAuthConnected,
		GrantedScopes: []string{},
	}
	tokenJSON := `{"accessToken":"oauth-material","tokenType":"Bearer"}`
	resolver := &testResolver{values: map[string]string{"oauth-session": tokenJSON}}
	record.HTTP.Endpoint = server.URL
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(resolver)
	defer func() { _ = runtime.Close() }()
	if _, refreshErr := runtime.Refresh(t.Context(), activation); refreshErr != nil {
		t.Fatalf("OAuth Refresh: %v", refreshErr)
	}

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "reauthorize with token oauth-material", http.StatusUnauthorized)
	}))
	defer refusing.Close()
	record.HTTP.Endpoint = refusing.URL
	activation, _ = ActivationFromServer(record)
	_, err = runtime.Refresh(t.Context(), activation)
	if !errors.Is(err, ErrOAuthReconnectRequired) {
		t.Fatalf("Refresh error = %v, want ErrOAuthReconnectRequired", err)
	}
	if strings.Contains(fmt.Sprint(err), "oauth-material") {
		t.Fatal("OAuth material appeared in the error")
	}
}

func TestResultIsBoundedAndUnsupportedMediaIsOmitted(t *testing.T) {
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "large", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: strings.Repeat("x", 32<<10)},
				&sdk.ImageContent{MIMEType: "image/png", Data: []byte("encoded")},
			}}, nil
		})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	record := httpServerRecord(httpServer.URL)
	record.Limits.MaxResultBytes = 2048
	activation, _ := ActivationFromServer(record)
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := catalog.ProfileCatalog()
	record.Catalog = stored
	activation, _ = ActivationFromServer(record)
	result, err := runtime.Invoke(t.Context(), Invocation{RunID: "bound", Activation: activation, RemoteTool: "large", DescriptorDigest: stored.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > record.Limits.MaxResultBytes {
		t.Fatalf("result has %d bytes, bound is %d", len(encoded), record.Limits.MaxResultBytes)
	}
	if len(result.Omitted) == 0 {
		t.Fatalf("result = %+v, want omitted media/truncation entries", result)
	}
}

func TestInvokeRejectsStructuredContentOutsideOutputSchema(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{
		Name:         "typed",
		InputSchema:  map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string"}}},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{StructuredContent: map[string]any{"value": 42}}, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	record := httpServerRecord(httpServer.URL)
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatal(err)
	}
	record.Catalog, err = catalog.ProfileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Invoke(t.Context(), Invocation{
		RunID: "schema", Activation: activation, RemoteTool: "typed",
		DescriptorDigest: record.Catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "MCP result schema failed") {
		t.Fatalf("Invoke error = %v, want bounded result-schema failure", err)
	}
}

func TestStdioRefreshAndCancellationCloseTheProcess(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	stopped := filepath.Join(t.TempDir(), "stopped")
	record := profile.MCPServer{
		ID: "mcp:stdio", Revision: 3, Name: "stdio", Enabled: true,
		Transport: profile.MCPTransportStdio,
		Stdio: &profile.MCPStdioConfig{
			Command: os.Args[0], Argv: []string{"-test.run=^TestStdioHelper$"},
			Env: []profile.MCPEnvBinding{
				{Name: "NOCX_MCP_HELPER", Value: literal("1")},
				{Name: "NOCX_MCP_STARTED", Value: literal(started)},
				{Name: "NOCX_MCP_STOPPED", Value: literal(stopped)},
			},
		},
		Limits:  profile.DefaultMCPLimits(),
		Catalog: profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}},
	}
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("stdio Refresh: %v", err)
	}
	waitFile(t, started)
	waitFile(t, stopped)
	stored, _ := catalog.ProfileCatalog()
	record.Catalog = stored
	activation, _ = ActivationFromServer(record)
	_ = os.Remove(stopped)
	_ = os.Remove(started)
	ctx, cancel := context.WithCancel(t.Context())
	called := make(chan error, 1)
	go func() {
		_, err := runtime.Invoke(ctx, Invocation{RunID: "cancel", Activation: activation, RemoteTool: "wait", DescriptorDigest: stored.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`)})
		called <- err
	}()
	waitFile(t, started)
	cancel()
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Fatalf("Invoke error = %v, want context.Canceled", err)
	}
	waitFile(t, stopped)
}

func literal(value string) profile.MCPValueBinding {
	return profile.MCPValueBinding{Kind: profile.MCPBindingLiteral, Literal: &value}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

func TestStdioHelper(t *testing.T) {
	if os.Getenv("NOCX_MCP_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("NOCX_MCP_STARTED"), []byte("started"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "start marker")
		return
	}
	defer func() { _ = os.WriteFile(os.Getenv("NOCX_MCP_STOPPED"), []byte("stopped"), 0o600) }()
	server := sdk.NewServer(&sdk.Implementation{Name: "stdio-fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "server stopped")
	}
}
