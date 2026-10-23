package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

type testResolver struct {
	mu     sync.Mutex
	values map[string]string
	calls  int
}

func (r *testResolver) ResolveSecret(ctx context.Context, ref string) (credential.Secret, error) {
	if err := ctx.Err(); err != nil {
		return credential.Secret{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	value, ok := r.values[ref]
	if !ok {
		return credential.Secret{}, ErrSecretUnavailable
	}
	return credential.NewSecretBytes([]byte(value)), nil
}
func (r *testResolver) callCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

type resolverFunc func(context.Context, string) (credential.Secret, error)

func (f resolverFunc) ResolveSecret(ctx context.Context, ref string) (credential.Secret, error) {
	return f(ctx, ref)
}

type (
	echoInput struct {
		Value string `json:"value"`
	}
	echoOutput struct {
		Value string `json:"value"`
	}
)

func newHTTPFixture(t *testing.T, requireHeaders bool, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.2.3"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "remote description"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, echoOutput, error) {
		calls.Add(1)
		return nil, echoOutput(in), nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireHeaders && (r.Header.Get("Authorization") != "Bearer bearer-material" || r.Header.Get("X-Tenant") != "tenant-material") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
}

func httpServerRecord(endpoint string) profile.MCPServer {
	return profile.MCPServer{
		ID: "mcp:http", Revision: 7, Name: "HTTP", Enabled: true,
		Transport: profile.MCPTransportStreamableHTTP,
		HTTP:      &profile.MCPHTTPConfig{Endpoint: endpoint, Auth: profile.MCPHTTPAuthNone, Headers: []profile.MCPHeaderBinding{}},
		Limits:    profile.MCPLimits{StartupTimeoutMS: 3000, CallTimeoutMS: 3000, IdleTimeoutMS: 30000, MaxResultBytes: 262144},
		Catalog:   profile.MCPCatalog{State: profile.MCPCatalogMissing, Tools: []profile.MCPTool{}},
	}
}

func activationFor(t *testing.T, record profile.MCPServer) Activation {
	t.Helper()
	a, err := ActivationFromServer(record)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func discoverFor(t *testing.T, manager *Manager, activation Activation) Activation {
	t.Helper()
	catalog, err := manager.Refresh(t.Context(), activation)
	if err != nil {
		t.Fatal(err)
	}
	activation.Tools, activation.CatalogDigest = catalog.Tools, catalog.Digest
	return activation
}

func invokeEcho(ctx context.Context, manager *Manager, activation Activation, run string) (Result, error) {
	return manager.Invoke(ctx, Invocation{
		RunID: run, Activation: activation, RemoteTool: activation.Tools[0].Name,
		DescriptorDigest: activation.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{"value":"hello"}`),
	})
}

func closeTestResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if closeErr := resource.Close(); closeErr != nil {
		t.Errorf("closing test resource: %v", closeErr)
	}
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("operation failed to reach its barrier")
		var zero T
		return zero
	}
}

func TestManagerDormantAndCanonicalReload(t *testing.T) {
	var calls atomic.Int32
	server := newHTTPFixture(t, true, &calls)
	defer server.Close()
	resolver := &testResolver{values: map[string]string{"bearer": "bearer-material", "tenant": "tenant-material"}}
	manager := NewManager(resolver)
	defer closeTestResource(t, manager)
	record := httpServerRecord(server.URL)
	record.HTTP.Auth = profile.MCPHTTPAuthBearer
	record.HTTP.Bearer = &profile.MCPSecretBinding{SecretRef: "bearer"}
	record.HTTP.Headers = []profile.MCPHeaderBinding{{Name: "X-Tenant", Value: profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: "tenant"}}}
	a := activationFor(t, record)
	if resolver.callCount() != 0 || calls.Load() != 0 {
		t.Fatal("construction activated credentials or server")
	}
	catalog, err := manager.Refresh(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	retained := len(manager.sessions)
	manager.mu.Unlock()
	if retained != 0 {
		t.Fatal("discovery retained a pooled session")
	}
	record.Catalog, err = catalog.ProfileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	beforeReload := activationFor(t, record)
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var loaded profile.MCPServer
	if decodeErr := json.Unmarshal(raw, &loaded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	a = activationFor(t, loaded)
	if !beforeReload.SameIdentity(a) {
		t.Fatal("formatting-only profile reload changed activation identity")
	}
	result, err := invokeEcho(t.Context(), manager, a, "run")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || calls.Load() != 1 || !strings.Contains(strings.Join(result.Text, ""), "hello") {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
	manager.CloseRun("run")
	manager.mu.Lock()
	retained = len(manager.sessions)
	manager.mu.Unlock()
	if retained != 0 {
		t.Fatal("run cleanup retained pooled session")
	}
}

func TestMutationCancelsCredentialResolutionBeforeDrain(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	manager := NewManager(resolverFunc(func(ctx context.Context, _ string) (credential.Secret, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return credential.Secret{}, ctx.Err()
	}))
	defer closeTestResource(t, manager)
	record := httpServerRecord("http://127.0.0.1:1")
	record.HTTP.Auth = profile.MCPHTTPAuthBearer
	record.HTTP.Bearer = &profile.MCPSecretBinding{SecretRef: "waiting"}
	a := activationFor(t, record)
	refreshDone := make(chan error, 1)
	go func() { _, err := manager.Refresh(t.Context(), a); refreshDone <- err }()
	await(t, entered)
	mutated := make(chan error, 1)
	var committed atomic.Bool
	go func() {
		mutated <- manager.RunServerMutations(t.Context(), []string{a.ServerID, a.ServerID}, func(context.Context) error { committed.Store(true); return nil })
	}()
	await(t, canceled)
	if committed.Load() {
		t.Fatal("mutation committed before canceled resolver drained")
	}
	if _, err := manager.Refresh(t.Context(), a); !errors.Is(err, ErrActivationChanged) {
		t.Fatalf("barrier admission: %v", err)
	}
	if err := manager.RunServerOperation(t.Context(), "other", func(context.Context) error { return nil }); err != nil {
		t.Fatal("unaffected server was blocked", err)
	}
	close(release)
	if err := await(t, refreshDone); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := await(t, mutated); err != nil || !committed.Load() {
		t.Fatalf("mutation: %v", err)
	}
}

func TestCanceledClosedAndUndrainedMutationsNeverCommit(t *testing.T) {
	manager := NewManager(nil)
	called := false
	callback := func(context.Context) error { called = true; return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := manager.RunServerMutations(ctx, nil, callback); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled callback=%v err=%v", called, err)
	}
	work, finish, err := manager.begin(t.Context(), sessionKey{serverID: "waiting"})
	if err != nil {
		t.Fatal(err)
	}
	mutationCtx, cancelMutation := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- manager.RunServerMutations(mutationCtx, []string{"waiting"}, callback) }()
	await(t, work.Done())
	cancelMutation()
	if err := await(t, done); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("undrained callback=%v err=%v", called, err)
	}
	finish()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.RunServerMutations(t.Context(), nil, callback); !errors.Is(err, ErrClosed) || called {
		t.Fatalf("closed callback=%v err=%v", called, err)
	}
}

func TestEmptyMutationDoesNotCancelOperations(t *testing.T) {
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	ctx, finish, err := manager.begin(t.Context(), sessionKey{serverID: "alive"})
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err := manager.RunServerMutations(t.Context(), nil, func(context.Context) error {
		if ctx.Err() != nil {
			t.Fatal("empty mutation canceled unrelated operation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleIdleGenerationCannotCloseReusedSession(t *testing.T) {
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	key := sessionKey{runID: "run", serverID: "server"}
	lifetime, cancel := context.WithCancel(t.Context())
	s := &pooledSession{key: key, lifetime: lifetime, cancel: cancel, gate: make(chan struct{}, 1), idleGeneration: 1}
	manager.sessions[key] = s
	manager.mu.Lock()
	s.idleGeneration++
	s.active++
	manager.mu.Unlock()
	manager.expireIdle(s, 1)
	if lifetime.Err() != nil {
		t.Fatal("stale timer closed active reuse")
	}
	manager.mu.Lock()
	s.active--
	current := s.idleGeneration
	manager.mu.Unlock()
	manager.expireIdle(s, current)
	if lifetime.Err() == nil {
		t.Fatal("current idle generation did not close session")
	}
}

func TestCleanupRemainsVisibleWhileIdleCloseDrains(t *testing.T) {
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	entered, release := make(chan struct{}), make(chan struct{})
	key := sessionKey{runID: "run", serverID: "server"}
	lifetime, cancel := context.WithCancel(t.Context())
	s := &pooledSession{
		key: key, lifetime: lifetime, cancel: cancel, idleGeneration: 1,
		live: &liveSession{cancel: cancel, cleanup: func() { close(entered); <-release }},
	}
	manager.sessions[key] = s
	go manager.expireIdle(s, 1)
	await(t, entered)
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- manager.RunServerMutations(t.Context(), []string{key.serverID}, func(context.Context) error { return nil })
	}()
	// Closing cleanup is still an operation; test the ownership rather than a
	// small timeout that would claim an absence of a completion notification.
	manager.mu.Lock()
	count := len(manager.operations)
	manager.mu.Unlock()
	if count == 0 {
		t.Fatal("cleanup vanished before physical close")
	}
	close(release)
	if err := await(t, mutationDone); err != nil {
		t.Fatal(err)
	}
}

func TestActivationVerifierBeforeConnectionAndCall(t *testing.T) {
	var calls atomic.Int32
	server := newHTTPFixture(t, false, &calls)
	defer server.Close()
	var checks atomic.Int32
	manager := NewManager(nil, WithActivationVerifier(func(context.Context, Activation) error {
		if checks.Add(1) >= 4 {
			return ErrActivationChanged
		}
		return nil
	}))
	defer closeTestResource(t, manager)
	a := discoverFor(t, manager, activationFor(t, httpServerRecord(server.URL)))
	if _, err := invokeEcho(t.Context(), manager, a, "run"); !errors.Is(err, ErrActivationChanged) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("tools/call bypassed verifier")
	}
}

func TestLiveMismatchNeverCallsTool(t *testing.T) {
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	add := func(schema map[string]any) {
		server.AddTool(&sdk.Tool{Name: "echo", InputSchema: schema}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	}
	add(map[string]any{"type": "object"})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	manager := NewManager(nil)
	defer closeTestResource(t, manager)
	a := discoverFor(t, manager, activationFor(t, httpServerRecord(httpServer.URL)))
	add(map[string]any{"type": "object", "required": []string{"changed"}})
	if _, err := invokeEcho(t.Context(), manager, a, "stale"); !errors.Is(err, ErrCatalogStale) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("stale descriptor sent tools/call")
	}
}

func TestCloseAndRunCloseCancelSocketCall(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			entered, requestCanceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			server := sdk.NewServer(&sdk.Implementation{Name: "blocking", Version: "1"}, nil)
			server.AddTool(&sdk.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				close(entered)
				// SDK stateless sessions detach tool contexts from the physical
				// HTTP request. Fixture shutdown must not rely on SDK Close
				// canceling a handler it first waits for.
				select {
				case <-ctx.Done():
				case <-release:
				}
				return nil, context.Canceled
			})
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, "read failed", 400)
						return
					}
					_ = r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(body))
					var message struct {
						Method string `json:"method"`
					}
					if json.Unmarshal(body, &message) == nil && message.Method == "tools/call" {
						go func() {
							select {
							case <-r.Context().Done():
								close(requestCanceled)
							case <-release:
							}
						}()
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			defer releaseOnce.Do(func() { close(release) })
			manager := NewManager(nil)
			defer closeTestResource(t, manager)
			a := discoverFor(t, manager, activationFor(t, httpServerRecord(httpServer.URL)))
			invoked := make(chan error, 1)
			go func() { _, err := invokeEcho(t.Context(), manager, a, "blocked"); invoked <- err }()
			await(t, entered)
			if shutdown {
				_ = manager.Close()
			} else {
				manager.CloseRun("blocked")
			}
			if err := await(t, invoked); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			await(t, requestCanceled) // physical cancellation, not detached SDK state
		})
	}
}

func TestCatalogRejectsAdversarialDescriptors(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": "null", "invalid": `{`, "remote": `{"$ref":"https://127.0.0.1/schema"}`,
		"dynamic": `{"$dynamicRef":"#x"}`, "dynamicAnchor": `{"$dynamicAnchor":"x"}`,
		"unknown":  `{"type":"object","x-instructions":"do this"}`,
		"oversize": `{"type":"object","description":"` + strings.Repeat("x", profile.MaxMCPSchemaBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := makeCatalog("", "", "", []ToolDescriptor{{Name: "tool", InputSchema: json.RawMessage(raw)}})
			if err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
	tool := ToolDescriptor{Name: "same", InputSchema: json.RawMessage(`{"type":"object"}`)}
	if _, err := makeCatalog("", "", "", []ToolDescriptor{tool, tool}); err == nil {
		t.Fatal("duplicate accepted")
	}
	many := make([]ToolDescriptor, profile.MaxMCPToolsPerServer+1)
	if _, err := makeCatalog("", "", "", many); err == nil {
		t.Fatal("oversized catalog accepted")
	}
	good := ToolDescriptor{Name: "local", InputSchema: json.RawMessage(`{"type":"object","properties":{"v":{"$ref":"#/$defs/v"}},"$defs":{"v":{"type":"integer","minimum":9007199254740993}},"description":"not an instruction"}`)}
	catalog, err := makeCatalog("", "", "", []ToolDescriptor{good})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := profile.CanonicalMCPToolDescriptor(profile.MCPTool{Name: good.Name, InputSchema: good.InputSchema})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Tools[0].DescriptorDigest != canonical.DescriptorDigest || strings.Contains(string(catalog.Tools[0].InputSchema), "instruction") {
		t.Fatal("runtime diverged from canonical profile owner")
	}
}

func TestResultRedactsEveryStringChannelBeforeTruncation(t *testing.T) {
	secret := strings.Repeat("s", 300)
	for _, isError := range []bool{false, true} {
		response := &sdk.CallToolResult{IsError: isError, Content: []sdk.Content{
			&sdk.TextContent{Text: secret}, &sdk.ResourceLink{URI: "https://example/" + secret, Name: secret, Title: secret, MIMEType: secret},
			&sdk.ImageContent{Data: []byte{1, 2}, MIMEType: secret},
		}, StructuredContent: map[string]any{secret: []any{secret, json.Number("12345")}}}
		result := boundResult("server", "tool", response, 2048, []string{secret, "12345"})
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 2048 || strings.Contains(string(raw), strings.Repeat("s", 30)) || strings.Contains(string(raw), "12345") {
			t.Fatal("result leaked or exceeded bound", string(raw))
		}
		if len(result.Omitted) == 0 || result.IsError != isError {
			t.Fatal("omission or error semantics lost")
		}
	}
}

func TestProtocolAndCancellationClassificationIsExplicit(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, ErrProtocol, ErrSchema, ErrFrameTooLarge, ErrResponseTooLarge, errors.New("unknown")} {
		if IsRecoverableError(err) {
			t.Fatalf("terminal error classified recoverable: %v", err)
		}
	}
	for _, err := range []error{ErrCatalogStale, ErrOAuthReconnectRequired, ErrSecretUnavailable, ErrServerUnavailable, ErrServerFailure} {
		if !IsRecoverableError(err) {
			t.Fatalf("recoverable error classified terminal: %v", err)
		}
	}
	for _, code := range []int64{-32700, -32600, -32601, -32602, -32603, -32001} {
		err := safeOperationError("call", &jsonrpc.Error{Code: code, Message: "echo-secret"}, []string{"echo-secret"})
		if strings.Contains(err.Error(), "echo-secret") {
			t.Fatal("remote diagnostic escaped")
		}
		if IsRecoverableError(err) != (code == -32603 || code == -32001) {
			t.Fatalf("wrong wire-error class: %d %v", code, err)
		}
	}
}

func TestSensitiveValuesRetainRenewalHistoryConcurrently(t *testing.T) {
	values := &sensitiveValues{}
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			for n := range 30 {
				values.add(fmt.Sprintf("token-%d-%d", i, n))
				_ = values.snapshot()
			}
		}(i)
	}
	workers.Wait()
	if len(values.snapshot()) != 240 {
		t.Fatal("lost old or new renewal material")
	}
}

func TestActivationCloneOwnsSchemasAndBindings(t *testing.T) {
	literal := "original"
	original := Activation{
		Tools: []ToolDescriptor{{InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}},
		Stdio: &StdioConfig{Argv: []string{"original"}, Env: []Binding{{Name: "TEST", Literal: &literal}}},
		HTTP:  &HTTPConfig{Headers: []Binding{{Name: "X-Test", Literal: &literal}}, OAuthScopes: []string{"read"}},
	}
	copy := original.Clone()
	copy.Tools[0].InputSchema[0] = 'x'
	copy.Tools[0].OutputSchema[0] = 'x'
	copy.Stdio.Argv[0] = "changed"
	*copy.Stdio.Env[0].Literal = "changed"
	*copy.HTTP.Headers[0].Literal = "changed"
	copy.HTTP.OAuthScopes[0] = "write"
	if original.Tools[0].InputSchema[0] != '{' || original.Tools[0].OutputSchema[0] != '{' ||
		original.Stdio.Argv[0] != "original" || literal != "original" || original.HTTP.OAuthScopes[0] != "read" {
		t.Fatal("clone shared mutable authority with its source")
	}
}
