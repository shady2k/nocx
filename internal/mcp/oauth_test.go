package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/file"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type oauthTestSecretStore struct {
	mu           sync.Mutex
	next         int
	values       map[credential.SecretID]credential.Secret
	deleted      []credential.SecretID
	replacements atomic.Int32
}

func newOAuthTestSecretStore() *oauthTestSecretStore {
	return &oauthTestSecretStore{values: make(map[credential.SecretID]credential.Secret)}
}

func (s *oauthTestSecretStore) CreateSystemSecret(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := credential.SecretID("sec:test:" + string(rune('0'+s.next)))
	s.values[id] = value
	return id, nil
}

func (s *oauthTestSecretStore) ScheduleDelete(_ context.Context, id credential.SecretID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, id)
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *oauthTestSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[id]
	return ok, nil
}

func (s *oauthTestSecretStore) ResolveSecret(_ context.Context, ref string) (credential.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[credential.SecretID(ref)]
	if !ok {
		return credential.Secret{}, errors.New("secret unavailable")
	}
	var copy credential.Secret
	if err := value.Use(func(plaintext []byte) error {
		copy = credential.NewSecretBytes(plaintext)
		return nil
	}); err != nil {
		return credential.Secret{}, err
	}
	return copy, nil
}

func (s *oauthTestSecretStore) ReplaceOAuthToken(_ context.Context, ref string, value credential.Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := credential.SecretID(ref)
	if _, ok := s.values[id]; !ok {
		return errors.New("secret unavailable")
	}
	s.values[id] = value
	s.replacements.Add(1)
	return nil
}

func (s *oauthTestSecretStore) expire(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := credential.SecretID(ref)
	value, ok := s.values[id]
	if !ok {
		return errors.New("secret unavailable")
	}
	var raw []byte
	if err := value.Use(func(plaintext []byte) error {
		raw = append(raw, plaintext...)
		return nil
	}); err != nil {
		return err
	}
	defer clear(raw)
	var stored storedOAuthToken
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	stored.Expiry = time.Now().Add(-time.Minute)
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	defer clear(encoded)
	s.values[id] = credential.NewSecretBytes(encoded)
	return nil
}

func TestOAuthManagerAuthorizePresentsURLAndStoresOpaqueSession(t *testing.T) {
	var calls atomic.Int32
	var refreshes atomic.Int32
	var rejectMCP atomic.Bool
	var errorResult atomic.Bool
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "oauth-fixture", Version: "1"}, nil)
	mcpServer.AddTool(
		&sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{IsError: errorResult.Load(), Content: []sdk.Content{&sdk.TextContent{Text: "oauth-runtime-ok access-renewed refresh dynamic-secret"}}}, nil
		},
	)
	mcpHandler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return mcpServer },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeOAuthJSON(t, w, map[string]any{
				"resource":              serverURL,
				"authorization_servers": []string{serverURL},
			})
		case "/.well-known/oauth-authorization-server":
			writeOAuthJSON(t, w, map[string]any{
				"issuer":                           serverURL,
				"authorization_endpoint":           serverURL + "/authorize",
				"token_endpoint":                   serverURL + "/token",
				"registration_endpoint":            serverURL + "/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			writeOAuthJSON(t, w, map[string]any{"client_id": "client", "client_secret": "dynamic-secret"})
		case "/authorize":
			redirect := r.URL.Query().Get("redirect_uri")
			query := url.Values{"code": {"code"}, "state": {r.URL.Query().Get("state")}}
			http.Redirect(w, r, redirect+"?"+query.Encode(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid token request", http.StatusBadRequest)
				return
			}
			if r.Form.Get("resource") != serverURL {
				t.Error("authorization or renewal changed the discovered root audience")
				http.Error(w, "wrong resource", http.StatusBadRequest)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes.Add(1)
				writeOAuthJSON(t, w, map[string]any{"access_token": "access-renewed", "token_type": "Bearer", "refresh_token": "refresh", "expires_in": 3600})
				return
			}
			writeOAuthJSON(t, w, map[string]any{"access_token": "access", "token_type": "Bearer", "refresh_token": "refresh", "expires_in": 3600})
		case "/mcp":
			if rejectMCP.Load() || r.Header.Get("Authorization") != "Bearer access-renewed" {
				http.Error(w, "reauthorization required", http.StatusUnauthorized)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	store := newOAuthTestSecretStore()
	lifecycle := NewManager(store)
	defer closeTestResource(t, lifecycle)
	manager := NewOAuthManager(store, nil, lifecycle)
	activation := Activation{
		ServerID:       "mcp:test",
		Name:           "OAuth fixture",
		ServerRevision: 1,
		Enabled:        true,
		Transport:      TransportStreamableHTTP,
		HTTP:           &HTTPConfig{Endpoint: server.URL + "/mcp", Auth: HTTPAuthOAuth, OAuthRegistration: "dynamic", OAuthScopes: []string{"read"}},
		Limits:         Limits{StartupTimeout: time.Second, CallTimeout: time.Second, MaxResultBytes: 4096},
	}
	var presented []string
	presenter := urlPresenterFunc(func(_ context.Context, target string) error {
		presented = append(presented, target)
		u, err := url.Parse(target)
		if err != nil || u.Query().Get("resource") != serverURL {
			t.Error("authorization did not use the discovered root resource")
		}
		// #nosec G107 -- target is the loopback or local OAuth fixture URL.
		response, err := http.Get(target)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	})

	status, err := manager.Authorize(context.Background(), activation, presenter)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if len(presented) != 1 || !strings.HasPrefix(presented[0], server.URL+"/authorize?") {
		t.Fatalf("presented URLs = %q, want one authorization endpoint", presented)
	}
	if !status.Connected || status.SessionRef == "" || status.Issuer != server.URL {
		t.Fatalf("status = %+v, want connected session and issuer", status)
	}
	if len(store.values) != 1 {
		t.Fatalf("stored sessions = %d, want one", len(store.values))
	}
	if expireErr := store.expire(status.SessionRef); expireErr != nil {
		t.Fatalf("expire stored OAuth session: %v", expireErr)
	}

	activation.HTTP.OAuthSessionRef = status.SessionRef
	runtime := NewManager(store)
	defer func() { _ = runtime.Close() }()
	catalog, err := runtime.Refresh(context.Background(), activation)
	if err != nil {
		t.Fatalf("runtime Refresh with stored OAuth session: %v", err)
	}
	if refreshes.Load() != 1 || store.replacements.Load() != 1 {
		t.Fatalf("silent renewal count = %d, persisted replacements = %d; want 1 and 1", refreshes.Load(), store.replacements.Load())
	}
	if len(presented) != 1 {
		t.Fatalf("silent renewal presented OAuth URLs: %q", presented)
	}
	if len(runtime.sessions) != 0 {
		t.Fatalf("discovery retained %d runtime sessions, want 0", len(runtime.sessions))
	}
	activation.Tools = catalog.Tools
	activation.CatalogDigest = catalog.Digest
	result, err := runtime.Invoke(context.Background(), Invocation{
		RunID: "oauth-run", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("runtime Invoke with stored OAuth session: %v", err)
	}
	if calls.Load() != 1 || len(result.Text) != 1 || result.Text[0] != "oauth-runtime-ok [REDACTED] [REDACTED] [REDACTED]" {
		t.Fatalf("runtime result = %+v, tools/call count = %d", result, calls.Load())
	}
	if len(presented) != 1 {
		t.Fatalf("runtime execution presented OAuth URLs: %q", presented)
	}

	errorResult.Store(true)
	result, err = runtime.Invoke(context.Background(), Invocation{
		RunID: "oauth-server-error", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`),
	})
	if err != nil || !result.IsError || len(result.Text) != 1 || result.Text[0] != "oauth-runtime-ok [REDACTED] [REDACTED] [REDACTED]" {
		t.Fatalf("OAuth server error result escaped token screening: %+v %v", result, err)
	}

	rejectMCP.Store(true)
	_, err = runtime.Invoke(context.Background(), Invocation{
		RunID: "oauth-reauth", Activation: activation, RemoteTool: "echo",
		DescriptorDigest: catalog.Tools[0].DescriptorDigest, Arguments: json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrOAuthReconnectRequired) {
		t.Fatalf("runtime Invoke after 401 = %v, want ErrOAuthReconnectRequired", err)
	}
	if len(presented) != 1 {
		t.Fatalf("reauth-required execution opened OAuth again: %q", presented)
	}
	// A new OAuth manager has no in-memory ownership state after restart.
	manager = NewOAuthManager(store, nil, lifecycle)
	if err := manager.Forget(context.Background(), status.SessionRef); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if len(store.deleted) != 1 || len(store.values) != 0 {
		t.Fatalf("after Forget deleted=%v values=%d, want one delete and no values", store.deleted, len(store.values))
	}
}

func TestOAuthManagerDiscardDoesNotDeleteNewerSession(t *testing.T) {
	store := newOAuthTestSecretStore()
	lifecycle := NewManager(store)
	defer closeTestResource(t, lifecycle)
	manager := NewOAuthManager(store, nil, lifecycle)
	oldID := credential.SecretID("sec:test:old")
	newID := credential.SecretID("sec:test:new")
	store.values[oldID] = credential.NewSecretBytes([]byte("old"))
	store.values[newID] = credential.NewSecretBytes([]byte("new"))

	if err := manager.DiscardOAuthSession(context.Background(), string(oldID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.values[newID]; !ok {
		t.Fatal("discarding a stale session deleted the newer session")
	}
	if len(store.deleted) != 1 || store.deleted[0] != oldID {
		t.Fatalf("deleted = %v, want only the losing authorization's orphan", store.deleted)
	}
	if err := manager.DiscardOAuthSession(context.Background(), string(newID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.values[newID]; ok {
		t.Fatal("current session was not discarded")
	}
}

type urlPresenterFunc func(context.Context, string) error

func (f urlPresenterFunc) PresentURL(ctx context.Context, target string) error { return f(ctx, target) }

func writeOAuthJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode OAuth JSON: %v", err)
	}
}

func TestSafeOAuthErrorNeverExposesProviderDetails(t *testing.T) {
	err := safeOAuthError(errors.New("oauth client_secret=super-secret response body"))
	if !errors.Is(err, ErrOAuthAuthorizationFailed) {
		t.Fatalf("safeOAuthError = %v, want sanitized failure", err)
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("sanitized OAuth error leaked provider details: %v", err)
	}
}

func TestOAuthAuthorizationServerRequiresPKCES256(t *testing.T) {
	for _, test := range []struct {
		name    string
		methods []string
		want    bool
	}{
		{name: "missing"},
		{name: "plain only", methods: []string{"plain"}},
		{name: "S256", methods: []string{"plain", "S256"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := supportsPKCES256(test.methods); got != test.want {
				t.Fatalf("supportsPKCES256(%v) = %v, want %v", test.methods, got, test.want)
			}
		})
	}
}

type oauthMetadataTransport func(*http.Request) (*http.Response, error)

func (f oauthMetadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOAuthCaptureRejectsAuthorizationMetadataWithoutS256(t *testing.T) {
	for _, test := range []struct {
		name    string
		methods []string
		wantErr bool
	}{
		{name: "missing", wantErr: true},
		{name: "plain only", methods: []string{"plain"}, wantErr: true},
		{name: "S256", methods: []string{"S256"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"authorization_endpoint":           "https://issuer.example/authorize",
				"code_challenge_methods_supported": test.methods,
			})
			if err != nil {
				t.Fatal(err)
			}
			capture := &oauthCaptureTransport{base: oauthMetadataTransport(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
			})}
			request, err := http.NewRequest(http.MethodGet, "https://issuer.example/.well-known/oauth-authorization-server", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = capture.RoundTrip(request)
			if (err != nil) != test.wantErr {
				t.Fatalf("RoundTrip error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestOAuthSilentRefreshUsesOAuthDestinationPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		tokenURL string
	}{
		{name: "private plaintext HTTP", tokenURL: "http://10.0.0.7/token"},
		{name: "private cross-origin HTTPS", tokenURL: "https://10.0.0.7/token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newOAuthTestSecretStore()
			raw, err := json.Marshal(storedOAuthToken{
				AccessToken:  "expired-access",
				TokenType:    "Bearer",
				RefreshToken: "refresh-material",
				Expiry:       time.Now().Add(-time.Minute),
				ClientID:     "test-client",
				TokenURL:     test.tokenURL,
				Resource:     "https://10.0.0.5/mcp",
			})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := store.CreateSystemSecret(t.Context(), credential.NewSecretBytes(raw))
			clear(raw)
			if err != nil {
				t.Fatal(err)
			}
			record := httpServerRecord("https://10.0.0.5/mcp")
			record.HTTP.Auth = profile.MCPHTTPAuthOAuth
			record.HTTP.OAuth = &profile.MCPOAuthConfig{
				Registration:  profile.MCPOAuthPreregistered,
				ClientID:      "test-client",
				Scopes:        []string{},
				SessionRef:    &profile.MCPSecretBinding{SecretRef: string(ref), Owned: true},
				Status:        profile.MCPOAuthConnected,
				GrantedScopes: []string{},
			}
			activation, err := ActivationFromServer(record)
			if err != nil {
				t.Fatal(err)
			}
			lifecycle := NewManager(store)
			defer closeTestResource(t, lifecycle)
			session, err := buildHTTPTransport(t.Context(), t.Context(), activation, store, newOAuthRefreshCoordinator(), lifecycle)
			if err != nil {
				t.Fatal(err)
			}
			defer session.cleanup()
			transport := session.transport
			source, err := transport.OAuthHandler.TokenSource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			handler, ok := transport.OAuthHandler.(*nonInteractiveOAuth)
			if !ok {
				t.Fatal("silent renewal did not use the noninteractive OAuth owner")
			}
			guard, ok := handler.source.client.Transport.(*guardedHTTPTransport)
			if !ok {
				t.Fatal("silent renewal did not use the guarded OAuth transport")
			}
			var dials atomic.Int32
			dialErr := errors.New("unexpected dial")
			guard.policy.Inner.DialContext = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, dialErr
			}

			_, err = source.Token()
			if !errors.Is(err, ErrOAuthReconnectRequired) {
				t.Fatalf("Token error = %v, want reconnect without unsafe dialing", err)
			}
			if got := dials.Load(); got != 0 {
				t.Fatalf("token destination dial attempts = %d, want 0", got)
			}
			if got := store.replacements.Load(); got != 0 {
				t.Fatalf("secret replacements = %d, want 0", got)
			}
		})
	}
}

func TestOAuthDiscardSchedulesSealedFreshOrphanAndRecoversAfterRestart(t *testing.T) {
	ctx := t.Context()
	docs := storage.NewDocumentStore(t.TempDir())
	open := func() *vault.Vault {
		registry, err := vault.NewRegistry(file.New(docs, "vault-blob.json"))
		if err != nil {
			t.Fatal(err)
		}
		v, err := vault.New(docs, registry, logtest.Slog(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(v.Close)
		return v
	}
	v := open()
	// #nosec G101 -- passphrase protects only this test's disposable encrypted Vault.
	const passphrase = "isolated-oauth-discard-test"
	if _, err := v.Setup(ctx, vault.SetupRequest{Passphrase: passphrase}); err != nil {
		t.Fatal(err)
	}
	orphan, err := v.CreateSystemSecret(ctx, credential.NewSecret("fresh-orphan-token"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := v.CreateSystemSecret(ctx, credential.NewSecret("newer-owned-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.AttachTarget(ctx, current, "mcp:newer-authorization"); err != nil {
		t.Fatal(err)
	}
	if err := v.CommitMetadata(ctx, current); err != nil {
		t.Fatal(err)
	}
	v.Seal()
	manager := NewOAuthManager(v, nil, nil)
	for range 2 {
		if err := manager.DiscardOAuthSession(ctx, string(orphan)); err != nil {
			t.Fatal(err)
		}
	}
	if v.State() != vault.StateSealed {
		t.Fatal("orphan discard unlocked the Vault")
	}
	var document vault.Document
	if found, err := docs.Read("vault.json", &document); err != nil || !found {
		t.Fatalf("read durable intent: %v %v", found, err)
	}
	scheduled := false
	for _, entry := range document.Journal {
		if entry.NewID == orphan && entry.Op == "delete" {
			scheduled = true
		}
		if entry.NewID == current && entry.Op == "delete" {
			t.Fatal("discard targeted another authorization")
		}
	}
	if !scheduled {
		t.Fatal("orphan discard did not persist deletion intent")
	}
	for _, record := range document.Secrets {
		if record.ID == orphan {
			t.Fatal("orphan metadata remained selectable")
		}
	}
	for range 2 {
		v.Close()
		v = open()
		if err := v.Unseal(ctx, vault.UnsealRequest{Passphrase: passphrase}); err != nil {
			t.Fatal(err)
		}
		if _, err := v.Get(ctx, orphan); !errors.Is(err, vault.ErrSecretNotFound) {
			t.Fatalf("orphan material survived recovery: %v", err)
		}
		value, err := v.Get(ctx, current)
		if err != nil {
			t.Fatal("discard removed a newer authorization", err)
		}
		if err := value.Use(func(raw []byte) error {
			if string(raw) != "newer-owned-token" {
				t.Error("surviving authorization changed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var recovered vault.Document
		if found, err := docs.Read("vault.json", &recovered); err != nil || !found {
			t.Fatalf("read recovered document: %v %v", found, err)
		}
		for _, entry := range recovered.Journal {
			if entry.NewID == orphan {
				t.Fatal("recovered orphan deletion was not finalized")
			}
		}
	}
}

// Reject the actual deletion-intent document before its durable write, while
// allowing creation, attachment, candidates, and readback to use the real store.
type oauthDeletionIntentFailureStore struct {
	storage.DocumentStore
	mu       sync.Mutex
	rejected credential.SecretID
}

func (s *oauthDeletionIntentFailureStore) rejectDeletion(id credential.SecretID) {
	s.mu.Lock()
	s.rejected = id
	s.mu.Unlock()
}

func (s *oauthDeletionIntentFailureStore) Write(name string, value any) error {
	s.mu.Lock()
	rejected := s.rejected
	s.mu.Unlock()
	if name == "vault.json" && rejected != "" {
		document, ok := value.(*vault.Document)
		if !ok {
			return errors.New("unexpected Vault document at deletion fault boundary")
		}
		for _, entry := range document.Journal {
			if entry.Op == "delete" && entry.NewID == rejected {
				return errors.New("injected OAuth deletion-intent persistence failure")
			}
		}
	}
	return s.DocumentStore.Write(name, value)
}

func TestOAuthRejectedFreshSecretRecoversAfterFailedDiscardWithoutDeletingLiveOrReferencedTokens(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	documents := &oauthDeletionIntentFailureStore{DocumentStore: storage.NewDocumentStore(root)}
	profilePath := filepath.Join(root, "profiles.json")
	repository := profile.NewJSONStore(profilePath)
	open := func() *vault.Vault {
		registry, registryErr := vault.NewRegistry(file.New(documents, "vault-blob.json"))
		if registryErr != nil {
			t.Fatal(registryErr)
		}
		opened, openErr := vault.New(documents, registry, logtest.Slog(t))
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(opened.Close)
		return opened
	}
	configGate := control.NewSemaphore(capability.GateConfig, 1)
	vaultGate := control.NewSemaphore(capability.GateVault, 1)
	lane := control.NewSemaphore("oauth-recovery-test", 1)
	reconcile := func(v *vault.Vault, repo *profile.JSONStore) {
		t.Helper()
		operation := capability.NewConfigOperation(configGate, vaultGate, lane,
			repo, repo, repo, repo, profile.NewProfileService(repo), nil, v, v,
			capability.MCPConfigDependencies{Repository: repo, Secrets: v})
		if reconcileErr := operation.Run(ctx, func(admitted context.Context, service capability.ConfigService) error {
			return service.ReconcileMCPSecrets(admitted)
		}); reconcileErr != nil {
			t.Fatal(reconcileErr)
		}
	}
	assertMaterial := func(v *vault.Vault, id credential.SecretID, want string) {
		t.Helper()
		material, getErr := v.Get(ctx, id)
		if getErr != nil {
			t.Fatalf("expected authorization material is unavailable: %v", getErr)
		}
		if useErr := material.Use(func(raw []byte) error {
			if string(raw) != want {
				t.Error("authorization material changed")
			}
			return nil
		}); useErr != nil {
			t.Fatal(useErr)
		}
	}
	v := open()
	// #nosec G101 -- passphrase protects only this test's disposable encrypted Vault.
	const passphrase = "isolated-oauth-abort-recovery"
	if _, setupErr := v.Setup(ctx, vault.SetupRequest{Passphrase: passphrase}); setupErr != nil {
		t.Fatal(setupErr)
	}
	type survivor struct{ material, serverID string }
	survivors := make(map[credential.SecretID]survivor)
	for _, phase := range []string{"attached", "committed"} {
		value := phase + "-authorization-material"
		id, mintErr := v.CreateSystemSecret(ctx, credential.NewSecret(value))
		if mintErr != nil {
			t.Fatal(mintErr)
		}
		record := httpServerRecord("http://127.0.0.1:1/mcp")
		record.Name = phase
		record.HTTP.Auth = profile.MCPHTTPAuthOAuth
		record.HTTP.OAuth = &profile.MCPOAuthConfig{
			Registration: profile.MCPOAuthDynamic, Status: profile.MCPOAuthConnected,
			SessionRef: &profile.MCPSecretBinding{SecretRef: string(id), Owned: true},
		}
		created, saveErr := repository.CreateMCPServer(record)
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		if attachErr := v.AttachTarget(ctx, id, created.ID); attachErr != nil {
			t.Fatal(attachErr)
		}
		if phase == "committed" {
			if commitErr := v.CommitMetadata(ctx, id); commitErr != nil {
				t.Fatal(commitErr)
			}
		}
		survivors[id] = survivor{material: value, serverID: created.ID}
	}

	// Keep a real registered authorization operation in flight after creation.
	// Unrelated aggregate reconciliation and Seal/Unseal must not infer that
	// this still-owned, not-yet-attached result is an abandoned authorization.
	lifecycle := NewManager(nil)
	defer closeTestResource(t, lifecycle)
	type mintOutcome struct {
		id  credential.SecretID
		err error
	}
	minted := make(chan mintOutcome, 1)
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- lifecycle.RunServerOperation(ctx, "mcp:authorizing", func(operationCtx context.Context) error {
			id, mintErr := v.CreateSystemSecret(operationCtx, credential.NewSecret("in-flight-authorization"))
			minted <- mintOutcome{id: id, err: mintErr}
			if mintErr != nil {
				return mintErr
			}
			select {
			case <-release:
				return nil
			case <-operationCtx.Done():
				return operationCtx.Err()
			}
		})
	}()
	live := await(t, minted)
	if live.err != nil {
		t.Fatal(live.err)
	}
	for range 2 {
		reconcile(v, repository)
		assertMaterial(v, live.id, "in-flight-authorization")
		v.Seal()
		if unsealErr := v.Unseal(ctx, vault.UnsealRequest{Passphrase: passphrase}); unsealErr != nil {
			t.Fatal(unsealErr)
		}
		reconcile(v, repository)
		assertMaterial(v, live.id, "in-flight-authorization")
	}
	close(release)
	if operationErr := await(t, operationDone); operationErr != nil {
		t.Fatal(operationErr)
	}
	manager := NewOAuthManager(v, nil, lifecycle)
	if discardErr := manager.DiscardOAuthSession(ctx, string(live.id)); discardErr != nil {
		t.Fatal(discardErr)
	}
	if _, getErr := v.Get(ctx, live.id); !errors.Is(getErr, vault.ErrSecretNotFound) {
		t.Fatalf("completed live fixture was not retired: %v", getErr)
	}

	orphan, mintErr := v.CreateSystemSecret(ctx, credential.NewSecret("aborted-authorization"))
	if mintErr != nil {
		t.Fatal(mintErr)
	}
	assertMaterial(v, orphan, "aborted-authorization")
	v.Seal()
	documents.rejectDeletion(orphan)
	if discardErr := manager.DiscardOAuthSession(ctx, string(orphan)); !errors.Is(discardErr, ErrSecretUnavailable) {
		t.Fatalf("discard did not report its precommit persistence failure: %v", discardErr)
	}
	documents.rejectDeletion("")
	if v.State() != vault.StateSealed {
		t.Fatal("failed discard unlocked the Vault")
	}

	// No retry of Discard and no synthetic candidate preparation: restart and
	// the real reference-aware aggregate owner must retain cleanup responsibility.
	for range 2 {
		v.Close()
		v = open()
		repository = profile.NewJSONStore(profilePath)
		reconcile(v, repository)
		if unsealErr := v.Unseal(ctx, vault.UnsealRequest{Passphrase: passphrase}); unsealErr != nil {
			t.Fatal(unsealErr)
		}
		if _, getErr := v.Get(ctx, orphan); !errors.Is(getErr, vault.ErrSecretNotFound) {
			t.Fatalf("rejected fresh authorization survived restart and aggregate reconciliation: %v", getErr)
		}
		for id, retained := range survivors {
			assertMaterial(v, id, retained.material)
			record, readErr := repository.GetMCPServer(retained.serverID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if record.HTTP == nil || record.HTTP.OAuth == nil || record.HTTP.OAuth.SessionRef == nil || record.HTTP.OAuth.SessionRef.SecretRef != string(id) {
				t.Fatal("recovery changed a surviving authorization binding")
			}
		}
	}
}
