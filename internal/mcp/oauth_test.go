package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"

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

func (s *oauthTestSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := credential.SecretID("sec:test:" + string(rune('0'+s.next)))
	s.values[id] = value
	return id, nil
}

func (s *oauthTestSecretStore) Delete(_ context.Context, id credential.SecretID) error {
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
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "oauth-fixture", Version: "1"}, nil)
	mcpServer.AddTool(
		&sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "oauth-runtime-ok"}}}, nil
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
				"resource":              serverURL + "/mcp",
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
	manager := NewOAuthManager(store, nil)
	activation := Activation{
		ServerID:       "mcp:test",
		ServerRevision: 1,
		Enabled:        true,
		Transport:      TransportStreamableHTTP,
		HTTP:           &HTTPConfig{Endpoint: server.URL + "/mcp", Auth: HTTPAuthOAuth, OAuthRegistration: "dynamic", OAuthScopes: []string{"read"}},
		Limits:         Limits{StartupTimeout: time.Second, CallTimeout: time.Second, MaxResultBytes: 4096},
	}
	var presented []string
	presenter := urlPresenterFunc(func(_ context.Context, target string) error {
		presented = append(presented, target)
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
	if calls.Load() != 1 || len(result.Text) != 1 || result.Text[0] != "oauth-runtime-ok" {
		t.Fatalf("runtime result = %+v, tools/call count = %d", result, calls.Load())
	}
	if len(presented) != 1 {
		t.Fatalf("runtime execution presented OAuth URLs: %q", presented)
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
	if err := manager.Forget(context.Background(), activation.ServerID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if len(store.deleted) != 1 || len(store.values) != 0 {
		t.Fatalf("after Forget deleted=%v values=%d, want one delete and no values", store.deleted, len(store.values))
	}
}

func TestOAuthManagerDiscardDoesNotDeleteNewerSession(t *testing.T) {
	store := newOAuthTestSecretStore()
	manager := NewOAuthManager(store, nil)
	oldID := credential.SecretID("sec:test:old")
	newID := credential.SecretID("sec:test:new")
	store.values[oldID] = credential.NewSecretBytes([]byte("old"))
	store.values[newID] = credential.NewSecretBytes([]byte("new"))
	manager.sessions["mcp:test"] = newID

	if err := manager.DiscardOAuthSession(context.Background(), "mcp:test", string(oldID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.values[newID]; !ok {
		t.Fatal("discarding a stale session deleted the newer session")
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted = %v, want no deletion for stale session", store.deleted)
	}
	if err := manager.DiscardOAuthSession(context.Background(), "mcp:test", string(newID)); err != nil {
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
