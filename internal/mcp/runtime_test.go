package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	"github.com/santhosh-tekuri/jsonschema/v6"
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

func TestSanitizeSchemaPreservesLiteralValuesAndLargeNumbers(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"mode":{
				"description":"annotation removed from the schema node",
				"const":{"description":"literal","mode":"safe","$ref":"literal"},
				"enum":[{"description":"literal","mode":"safe","$ref":"literal"}]
			},
			"count":{"const":9007199254740993}
		},
		"required":["mode","count"],
		"dependentRequired":{"$ref":["description"]}
	}`)
	got, err := sanitizeSchema(raw, true)
	if err != nil {
		t.Fatalf("sanitizeSchema: %v", err)
	}
	if strings.Contains(string(got), "annotation removed from the schema node") {
		t.Fatalf("schema annotation survived sanitization: %s", got)
	}
	if !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("large integer was changed by decoding: %s", got)
	}
	compiler := jsonschema.NewCompiler()
	const resource = "https://nocx.local/mcp/literal-test.json"
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if err = compiler.AddResource(resource, doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	literal := map[string]any{"description": "literal", "mode": "safe", "$ref": "literal"}
	if err := compiled.Validate(map[string]any{"mode": literal, "count": json.Number("9007199254740993")}); err != nil {
		t.Fatalf("schema rejected its exact literal value: %v", err)
	}
	if err := compiled.Validate(map[string]any{"mode": "unsafe", "count": json.Number("9007199254740993")}); err == nil {
		t.Fatal("schema accepted a value different from its object const")
	}
}

func TestInvokeValidatesArgumentsAgainstLiveToolSchema(t *testing.T) {
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{
		Name: "literal",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{"mode": map[string]any{
				"const": map[string]any{"description": "literal", "mode": "safe", "$ref": "literal"},
			}},
			"required": []string{"mode"},
		},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	endpoint := httptest.NewServer(handler)
	defer endpoint.Close()

	record := httpServerRecord(endpoint.URL)
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	record.Catalog, err = catalog.ProfileCatalog()
	if err != nil {
		t.Fatalf("ProfileCatalog: %v", err)
	}
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(arguments string) error {
		_, invokeErr := runtime.Invoke(t.Context(), Invocation{
			RunID: "schema", Activation: activation, RemoteTool: "literal",
			DescriptorDigest: catalog.Tools[0].DescriptorDigest,
			Arguments:        json.RawMessage(arguments),
		})
		return invokeErr
	}
	if err := invoke(`{"mode":{"description":"literal","mode":"safe","$ref":"literal"}}`); err != nil {
		t.Fatalf("Invoke with the schema's object-valued const: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("remote calls after valid arguments = %d, want 1", calls.Load())
	}
	if err := invoke(`{"mode":"unsafe"}`); err == nil {
		t.Fatal("Invoke accepted arguments that violate the live tool schema")
	}
	if calls.Load() != 1 {
		t.Fatalf("remote calls after invalid arguments = %d, want 1", calls.Load())
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
	// The profile store writes indented JSON and reads it back before the
	// assistant assembles a run. Descriptor identity is semantic JSON, not the
	// incidental whitespace chosen by persistence.
	persisted, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var reloaded profile.MCPServer
	err = json.Unmarshal(persisted, &reloaded)
	if err != nil {
		t.Fatal(err)
	}
	activation, err = ActivationFromServer(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"value":"hello"}`)
	result, err := runtime.Invoke(t.Context(), Invocation{
		RunID: "run-1", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: reloaded.Catalog.Tools[0].DescriptorDigest, Arguments: args,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.IsError || len(result.Text) != 1 || !strings.Contains(result.Text[0], "hello") || calls.Load() != 1 {
		t.Fatalf("result = %+v, calls = %d", result, calls.Load())
	}
	runtime.CloseRun("run-1")
}

func TestPrivateHTTPMCPDestinationRemainsAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	record := httpServerRecord("http://10.0.0.7/mcp")
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	sessionConfig, err := buildHTTPTransport(t.Context(), activation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sessionConfig.cleanup()
	guard, ok := sessionConfig.transport.HTTPClient.Transport.(*guardedHTTPTransport)
	if !ok {
		t.Fatalf("HTTP transport has type %T, want guarded transport", sessionConfig.transport.HTTPClient.Transport)
	}
	guard.inner.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, record.HTTP.Endpoint, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := sessionConfig.transport.HTTPClient.Do(request)
	if err != nil {
		t.Fatalf("configured private HTTP MCP destination: %v", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close HTTP response body: %v", err)
		}
	}()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP status = %d, want 204 from loopback fixture", response.StatusCode)
	}
}

func TestInvokeRejectsChangedLiveDescriptorWithoutCallingTool(t *testing.T) {
	var calls atomic.Int32
	original := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	sdk.AddTool(original, &sdk.Tool{Name: "echo"},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, echoOutput, error) {
			calls.Add(1)
			return nil, echoOutput(in), nil
		})
	changed := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "2"}, nil)
	changed.AddTool(
		&sdk.Tool{
			Name:        "echo",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}},
		},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{}, nil
		},
	)
	var serveChanged atomic.Bool
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		if serveChanged.Load() {
			return changed
		}
		return original
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(handler)
	defer server.Close()

	record := httpServerRecord(server.URL)
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh original catalog: %v", err)
	}
	record.Catalog, err = catalog.ProfileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}

	serveChanged.Store(true)
	_, err = runtime.Invoke(t.Context(), Invocation{
		RunID: "stale", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: record.Catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{"value":"old shape"}`),
	})
	if !errors.Is(err, ErrCatalogStale) {
		t.Fatalf("Invoke error = %v, want ErrCatalogStale", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("changed live descriptor reached tools/call %d times", calls.Load())
	}
	runtime.mu.Lock()
	sessionCount := len(runtime.sessions)
	runtime.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("sessions after stale descriptor = %d, want 0", sessionCount)
	}

	refreshed, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh changed catalog: %v", err)
	}
	if refreshed.Tools[0].DescriptorDigest == record.Catalog.Tools[0].DescriptorDigest {
		t.Fatal("Refresh did not replace the stale descriptor")
	}
}

func TestInvokeCollapsesServerErrorContainingSecretAndClosesSession(t *testing.T) {
	const secret = "sk-proj-server-error-secret-material" // #nosec G101 -- deterministic fake credential used to prove error redaction
	var calls atomic.Int32
	fixture := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	fixture.AddTool(
		&sdk.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return nil, fmt.Errorf("server rejected credential %s", secret)
		},
	)
	server := httptest.NewServer(sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return fixture },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	))
	defer server.Close()

	record := httpServerRecord(server.URL)
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
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
		RunID: "server-error", Activation: activation, RemoteTool: "fail",
		DescriptorDigest: record.Catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`),
	})
	if err == nil || err.Error() != "MCP tool call failed" {
		t.Fatalf("Invoke error = %v, want collapsed tool-call failure", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Invoke error leaked server secret: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("tools/call count = %d, want 1", calls.Load())
	}
	runtime.mu.Lock()
	sessionCount := len(runtime.sessions)
	runtime.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("sessions after server error = %d, want 0", sessionCount)
	}
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

func TestOAuthTransportRejectsPlaintextAndPrivateCrossOriginEndpoints(t *testing.T) {
	guard := newGuardedHTTPTransport("https://10.0.0.5/mcp", nil, 2048, time.Second)
	guard.oauthEndpoints = true
	for _, test := range []struct {
		name string
		url  string
		want bool
	}{
		{name: "configured private HTTPS origin", url: "https://10.0.0.5/token"},
		{name: "loopback HTTP exception", url: "http://127.0.0.1:4321/token"},
		{name: "public HTTPS provider", url: "https://8.8.8.8/token"},
		{name: "private HTTPS cross-origin", url: "https://10.0.0.6/token", want: true},
		{name: "private HTTP", url: "http://10.0.0.5/token", want: true},
		{name: "HTTP non-loopback", url: "http://8.8.8.8/token", want: true},
		{name: "URL userinfo", url: "https://user:password@8.8.8.8/token", want: true},
		{name: "URL fragment", url: "https://8.8.8.8/token#fragment", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := guard.validateOAuthURL(t.Context(), test.url)
			if (err != nil) != test.want {
				t.Fatalf("validateOAuthURL(%q) error = %v, want error %v", test.url, err, test.want)
			}
		})
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
	request, err := http.NewRequest(http.MethodGet, redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Mcp-Session-Id", "session-secret")
	request.Header.Set("Last-Event-ID", "event-secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	headers := <-received
	if headers.Get("Authorization") != "" || headers.Get("X-Secret") != "" || headers.Get("Mcp-Session-Id") != "" || headers.Get("Last-Event-ID") != "" {
		t.Fatalf(
			"redirect carried credentials: Authorization=%q X-Secret=%q Mcp-Session-Id=%q Last-Event-ID=%q",
			headers.Get("Authorization"),
			headers.Get("X-Secret"),
			headers.Get("Mcp-Session-Id"),
			headers.Get("Last-Event-ID"),
		)
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

func TestHTTPClientRejectsPOST302AcrossOriginButAllowsGET(t *testing.T) {
	var targetCalls, sameOriginCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	var source *httptest.Server
	source = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same-redirect" {
			http.Redirect(w, r, source.URL+"/same-target", http.StatusFound)
			return
		}
		if r.URL.Path == "/same-target" {
			sameOriginCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	guard := newGuardedHTTPTransport(source.URL, nil, 2048, time.Second)
	client := &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect}
	request, err := http.NewRequest(http.MethodPost, source.URL+"/cross-redirect", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, requestErr := client.Do(request); requestErr == nil {
		t.Fatal("cross-origin POST 302 was accepted after net/http rewrote it to GET")
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("cross-origin target received %d requests after POST 302, want 0", got)
	}

	request, err = http.NewRequest(http.MethodPost, source.URL+"/same-redirect", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, requestErr := client.Do(request); requestErr == nil {
		t.Fatal("same-origin POST 302 was accepted after net/http rewrote it to GET")
	}
	if got := sameOriginCalls.Load(); got != 0 {
		t.Fatalf("same-origin redirect target received %d requests after POST 302, want 0", got)
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("cross-origin target received %d requests after POST 302, want 0", got)
	}

	request, err = http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("safe cross-origin GET redirect: %v", err)
	}
	_ = response.Body.Close()
	if got := targetCalls.Load(); got != 1 {
		t.Fatalf("cross-origin target received %d requests after safe GET, want 1", got)
	}
}

func TestDiscoveryLifecycleCancelsConcurrentRefreshesBeforeMutation(t *testing.T) {
	lifecycle := newDiscoveryLifecycle()
	ctx1, finish1, err := lifecycle.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx2, finish2, err := lifecycle.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mutationDone := make(chan struct{})
	go func() {
		release := lifecycle.beginMutation()
		close(mutationDone)
		release()
	}()

	select {
	case <-ctx1.Done():
	case <-time.After(time.Second):
		t.Fatal("first discovery was not canceled by mutation")
	}
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("second discovery was not canceled by mutation")
	}
	select {
	case <-mutationDone:
		t.Fatal("mutation completed before active discoveries cleaned up")
	default:
	}
	if _, _, err := lifecycle.begin(t.Context()); !errors.Is(err, ErrActivationChanged) {
		t.Fatalf("discovery during mutation error = %v, want activation changed", err)
	}
	finish1()
	select {
	case <-mutationDone:
		t.Fatal("mutation completed while one discovery remained active")
	default:
	}
	finish2()
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("mutation did not proceed after discovery cleanup")
	}
}

func TestRefreshIsClosedBeforeServerMutationReturns(t *testing.T) {
	discoveryStarted := make(chan struct{}, 1)
	serverImpl := sdk.NewServer(&sdk.Implementation{Name: "blocked", Version: "1"}, nil)
	sdk.AddTool(serverImpl, &sdk.Tool{Name: "echo"},
		func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, echoOutput, error) {
			return nil, echoOutput(in), nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return serverImpl },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request read failed", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/list"`)) {
			discoveryStarted <- struct{}{}
			<-r.Context().Done()
			// A late response must not turn the canceled refresh into success.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	activation, err := ActivationFromServer(httpServerRecord(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := runtime.Refresh(t.Context(), activation)
		refreshDone <- refreshErr
	}()
	select {
	case <-discoveryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Refresh never started tools/list")
	}
	mutationCalled := false
	if err := runtime.RunServerMutation(activation.ServerID, func() error {
		mutationCalled = true
		return nil
	}); err != nil {
		t.Fatalf("RunServerMutation: %v", err)
	}
	if !mutationCalled {
		t.Fatal("mutation callback did not run")
	}
	select {
	case refreshErr := <-refreshDone:
		if refreshErr == nil {
			t.Fatal("canceled Refresh returned a late catalog")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Refresh did not finish after mutation canceled discovery")
	}
}

func TestPooledSessionClosePreservesRedactionDuringActiveCall(t *testing.T) {
	manager := NewManager(nil)
	key := sessionKey{runID: "run", serverID: "server"}
	live := &liveSession{sensitive: []string{"api-secret"}}
	pooled := &pooledSession{live: live}
	manager.mu.Lock()
	manager.sessions[key] = pooled
	manager.mu.Unlock()

	pooled.opMu.Lock()
	closed := make(chan struct{})
	go func() {
		manager.dropSession(key, pooled)
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		pooled.opMu.Unlock()
		t.Fatal("session close waited for the active call instead of cancelling it")
	}
	if got := live.sensitiveValues(); len(got) != 1 || got[0] != "api-secret" {
		t.Fatalf("active call redaction material = %v, want api-secret retained", got)
	}

	result := boundResult("server", "tool", &sdk.CallToolResult{Content: []sdk.Content{
		&sdk.TextContent{Text: "server echoed api-secret"},
	}}, 1024, live.sensitiveValues())
	if strings.Contains(strings.Join(result.Text, " "), "api-secret") {
		t.Fatalf("tool result leaked the active secret: %+v", result)
	}

	pooled.opMu.Unlock()
	pooled.clearSensitiveIfClosed(live)
	if live.sensitive[0] != "" {
		t.Fatal("closed session retained redaction material after the call drained")
	}
}

func TestRunServerMutationClosesSessionsBeforeCallback(t *testing.T) {
	runtime := NewManager(nil)
	defer func() { _ = runtime.Close() }()
	key := sessionKey{runID: "run", serverID: "server"}
	session := &pooledSession{}
	runtime.mu.Lock()
	runtime.sessions[key] = session
	runtime.mu.Unlock()

	if err := runtime.RunServerMutation("server", func() error {
		runtime.mu.Lock()
		_, present := runtime.sessions[key]
		runtime.mu.Unlock()
		session.stateMu.Lock()
		closed := session.closed
		session.stateMu.Unlock()
		if present || !closed {
			t.Errorf("mutation callback observed pooled session present=%v closed=%v", present, closed)
		}
		return errors.New("simulated mutation failure")
	}); err == nil {
		t.Fatal("RunServerMutation swallowed callback failure")
	}
}

func TestCloseServersAndCloseDrainAndBlockDiscoveries(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Manager)
		want error
	}{
		{name: "reset", run: (*Manager).CloseServers, want: ErrActivationChanged},
		{name: "shutdown", run: func(manager *Manager) { _ = manager.Close() }, want: ErrClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil)
			ctx, finish, err := manager.beginDiscovery(t.Context(), "server")
			if err != nil {
				t.Fatal(err)
			}
			closed := make(chan struct{})
			go func() {
				test.run(manager)
				close(closed)
			}()
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("global close did not cancel discovery")
			}
			if _, _, err := manager.beginDiscovery(t.Context(), "server"); !errors.Is(err, test.want) {
				t.Fatalf("Refresh during %s error = %v, want %v", test.name, err, test.want)
			}
			select {
			case <-closed:
				t.Fatal("global close completed before discovery cleanup")
			default:
			}
			finish()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("global close did not complete after discovery cleanup")
			}
			if test.name == "reset" {
				if _, finishRefresh, err := manager.beginDiscovery(t.Context(), "server"); err != nil {
					t.Fatalf("Refresh after completed reset: %v", err)
				} else {
					finishRefresh()
				}
			}
		})
	}
}

func TestCloseServersDrainsActiveInvokeAndRejectsNewInvoke(t *testing.T) {
	callStarted := make(chan struct{}, 1)
	releaseCall := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseCall) })
	serverImpl := sdk.NewServer(&sdk.Implementation{Name: "reset", Version: "1"}, nil)
	sdk.AddTool(serverImpl, &sdk.Tool{Name: "echo"},
		func(ctx context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, echoOutput, error) {
			callStarted <- struct{}{}
			select {
			case <-releaseCall:
				return nil, echoOutput(in), nil
			case <-ctx.Done():
				return nil, echoOutput{}, ctx.Err()
			}
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return serverImpl },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(handler)
	defer server.Close()

	record := httpServerRecord(server.URL)
	activation, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewManager(nil)
	defer func() {
		releaseOnce.Do(func() { close(releaseCall) })
		_ = runtime.Close()
	}()
	catalog, err := runtime.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	record.Catalog, err = catalog.ProfileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		RunID: "active", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: record.Catalog.Tools[0].DescriptorDigest,
		Arguments:        json.RawMessage(`{"value":"active"}`),
	}
	activeDone := make(chan error, 1)
	go func() {
		_, invokeErr := runtime.Invoke(t.Context(), invocation)
		activeDone <- invokeErr
	}()
	select {
	case <-callStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Invoke did not reach the fixture tool")
	}

	resetDone := make(chan struct{})
	go func() {
		runtime.CloseServers()
		close(resetDone)
	}()
	deadline := time.After(3 * time.Second)
	for {
		runtime.mu.Lock()
		resetting := runtime.resetting
		runtime.mu.Unlock()
		if resetting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("CloseServers did not enter reset state")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := runtime.Invoke(t.Context(), invocation); !errors.Is(err, ErrActivationChanged) {
		t.Fatalf("Invoke during reset error = %v, want activation changed", err)
	}
	select {
	case <-resetDone:
		t.Fatal("CloseServers completed while an Invoke held the server gate")
	default:
	}
	releaseOnce.Do(func() { close(releaseCall) })
	select {
	case err := <-activeDone:
		if err != nil {
			t.Fatalf("active Invoke: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active Invoke did not finish after release")
	}
	select {
	case <-resetDone:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseServers did not finish after active Invoke drained")
	}
}

func TestCloseServersAndCloseSerializeLifecycleDrains(t *testing.T) {
	manager := NewManager(nil)
	ctx1, finish1, err := manager.beginDiscovery(t.Context(), "server-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, finish2, err := manager.beginDiscovery(t.Context(), "server-z")
	if err != nil {
		t.Fatal(err)
	}
	gateZ := manager.serverGate("server-z")
	gateA := manager.serverGate("server-a")
	manager.mu.Lock()
	gates := manager.serverGateSnapshotLocked()
	manager.mu.Unlock()
	if len(gates) != 2 || gates[0] != gateA || gates[1] != gateZ {
		t.Fatal("manager-wide gate snapshot is not ordered by server ID")
	}

	resetDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		manager.CloseServers()
		close(resetDone)
	}()
	go func() {
		_ = manager.Close()
		close(shutdownDone)
	}()
	for _, ctx := range []context.Context{ctx1, ctx2} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("concurrent manager-wide drain did not cancel discovery")
		}
	}
	finish1()
	finish2()
	for _, done := range []<-chan struct{}{resetDone, shutdownDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent CloseServers and Close deadlocked")
		}
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
	var waitDigest, doneDigest string
	for _, tool := range stored.Tools {
		switch tool.Name {
		case "wait":
			waitDigest = tool.DescriptorDigest
		case "done":
			doneDigest = tool.DescriptorDigest
		}
	}
	if waitDigest == "" || doneDigest == "" {
		t.Fatalf("stdio catalog tools = %+v, want wait and done", stored.Tools)
	}
	_ = os.Remove(stopped)
	_ = os.Remove(started)
	ctx, cancel := context.WithCancel(t.Context())
	called := make(chan error, 1)
	go func() {
		_, invokeErr := runtime.Invoke(ctx, Invocation{RunID: "cancel", Activation: activation, RemoteTool: "wait", DescriptorDigest: waitDigest, Arguments: json.RawMessage(`{}`)})
		called <- invokeErr
	}()
	waitFile(t, started)
	cancel()
	if invokeErr := <-called; !errors.Is(invokeErr, context.Canceled) {
		t.Fatalf("Invoke error = %v, want context.Canceled", invokeErr)
	}
	waitFile(t, stopped)

	_ = os.Remove(stopped)
	_ = os.Remove(started)
	record.Limits.CallTimeoutMS = 500
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Invoke(t.Context(), Invocation{
		RunID: "timeout", Activation: activation, RemoteTool: "wait",
		DescriptorDigest: waitDigest, Arguments: json.RawMessage(`{}`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed Invoke error = %v, want context.DeadlineExceeded", err)
	}
	waitFile(t, started)
	waitFile(t, stopped)

	_ = os.Remove(stopped)
	_ = os.Remove(started)
	record.Limits = profile.DefaultMCPLimits()
	activation, err = ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Invoke(t.Context(), Invocation{
		RunID: "mutation", Activation: activation, RemoteTool: "done",
		DescriptorDigest: doneDigest, Arguments: json.RawMessage(`{}`),
	})
	if err != nil || len(result.Text) != 1 || result.Text[0] != "done" {
		t.Fatalf("pooled Invoke result = %+v, err = %v", result, err)
	}
	waitFile(t, started)
	if err := runtime.RunServerMutation(record.ID, func() error { return nil }); err != nil {
		t.Fatalf("RunServerMutation: %v", err)
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
	server.AddTool(&sdk.Tool{Name: "done", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done"}}}, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "server stopped")
	}
}
