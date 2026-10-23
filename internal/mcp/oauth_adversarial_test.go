package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

func TestExplicitOAuthValidatesPKCEStateIssuerAndToken(t *testing.T) {
	// #nosec G101 -- dedicated local fixture marker used to detect credential disclosure.
	const headerSecret = "mcp-header-only-sentinel"
	for _, mode := range []string{"success-dynamic", "success-preregistered", "success-custom-challenge", "success-post-only", "header-scope", "header-token-scope", "header-token-issuer", "header-authorization-url", "pkce", "state", "issuer", "metadata-issuer", "resource", "token", "oversize-metadata", "presenter-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var serverURL, challenge, callbackURL string
			var presentations, exchanges, probeGETs, probePOSTs atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/mcp":
					if strings.HasPrefix(mode, "header-") && r.Header.Get("X-API-Key") != headerSecret {
						t.Error("challenge did not receive its configured credential")
						http.Error(w, "missing header", 400)
						return
					}
					if r.Method == http.MethodGet {
						probeGETs.Add(1)
						if mode == "success-post-only" {
							w.Header().Set("Allow", "POST")
							w.WriteHeader(http.StatusMethodNotAllowed)
							return
						}
					} else if r.Method == http.MethodPost {
						probePOSTs.Add(1)
						var probe struct {
							JSONRPC string          `json:"jsonrpc"`
							ID      json.RawMessage `json:"id"`
							Method  string          `json:"method"`
							Params  struct {
								Meta map[string]any `json:"_meta"`
							} `json:"params"`
						}
						if json.NewDecoder(r.Body).Decode(&probe) != nil || probe.JSONRPC != "2.0" || len(probe.ID) == 0 || probe.Method != "server/discover" || probe.Params.Meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" {
							t.Error("OAuth POST probe was not valid stateless discovery")
							http.Error(w, "invalid probe", 400)
							return
						}
					} else {
						t.Error("unexpected MCP activation during Connect")
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					metadata := serverURL + "/.well-known/oauth-protected-resource/mcp"
					if mode == "success-custom-challenge" {
						metadata = serverURL + "/custom-resource"
					}
					scope := "server-scope"
					if mode == "header-scope" {
						scope = headerSecret
					}
					w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadata+`", scope="`+scope+`"`)
					w.WriteHeader(http.StatusUnauthorized)
				case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/custom-resource":
					if mode == "success-custom-challenge" && r.URL.Path != "/custom-resource" {
						http.NotFound(w, r)
						return
					}
					resource := serverURL + "/mcp"
					if mode == "resource" {
						resource = serverURL + "/other"
					}
					issuer := serverURL
					if mode == "success-custom-challenge" {
						issuer += "/issuer"
					}
					writeOAuthJSON(t, w, map[string]any{"resource": resource, "authorization_servers": []string{issuer}})
				case "/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/issuer":
					if mode == "success-custom-challenge" && r.URL.Path != "/.well-known/oauth-authorization-server/issuer" {
						http.NotFound(w, r)
						return
					}
					issuer := serverURL
					if mode == "metadata-issuer" {
						issuer += "/other"
					}
					if mode == "success-custom-challenge" {
						issuer += "/issuer"
					}
					methods := []string{"S256"}
					if mode == "pkce" {
						methods = []string{"plain"}
					}
					metadata := map[string]any{"issuer": issuer, "authorization_endpoint": serverURL + "/authorize", "token_endpoint": serverURL + "/token", "registration_endpoint": serverURL + "/register", "code_challenge_methods_supported": methods, "authorization_response_iss_parameter_supported": true}
					if mode == "oversize-metadata" {
						metadata["padding"] = strings.Repeat("x", 129<<10)
					}
					if mode == "header-authorization-url" {
						metadata["authorization_endpoint"] = serverURL + "/authorize?credential=" + url.QueryEscape(headerSecret)
					}
					writeOAuthJSON(t, w, metadata)
				case "/register":
					writeOAuthJSON(t, w, map[string]any{"client_id": "dynamic-client", "client_secret": "dynamic-client-secret"})
				case "/token":
					exchanges.Add(1)
					if err := r.ParseForm(); err != nil {
						http.Error(w, "invalid form", 400)
						return
					}
					digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || r.Form.Get("resource") != serverURL+"/mcp" {
						t.Error("token exchange did not bind PKCE/resource")
						http.Error(w, "invalid exchange", 400)
						return
					}
					if mode == "token" {
						http.Error(w, "provider echoed client-secret", 400)
						return
					}
					token := map[string]any{"access_token": "access-material", "refresh_token": "refresh-material", "token_type": "Bearer", "expires_in": 3600}
					if mode == "header-token-scope" {
						token["scope"] = headerSecret
					}
					if mode == "header-token-issuer" {
						token["issuer"] = headerSecret
					}
					writeOAuthJSON(t, w, token)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			serverURL = server.URL
			store := newOAuthTestSecretStore()
			lifecycle := NewManager(store)
			defer closeTestResource(t, lifecycle)
			manager := NewOAuthManager(store, store, lifecycle)
			record := httpServerRecord(server.URL + "/mcp")
			record.HTTP.Auth = profile.MCPHTTPAuthOAuth
			record.HTTP.OAuth = &profile.MCPOAuthConfig{Registration: profile.MCPOAuthDynamic, Status: profile.MCPOAuthMissing, Scopes: []string{"read"}}
			if mode == "success-preregistered" {
				id, err := store.CreateSystemSecret(t.Context(), credential.NewSecretBytes([]byte("preregistered-secret")))
				if err != nil {
					t.Fatal(err)
				}
				record.HTTP.OAuth.Registration = profile.MCPOAuthPreregistered
				record.HTTP.OAuth.ClientID = "preregistered-client"
				record.HTTP.OAuth.ClientSecret = &profile.MCPSecretBinding{SecretRef: string(id)}
			}
			if strings.HasPrefix(mode, "header-") {
				id, err := store.CreateSystemSecret(t.Context(), credential.NewSecret(headerSecret))
				if err != nil {
					t.Fatal(err)
				}
				record.HTTP.Headers = []profile.MCPHeaderBinding{{Name: "X-API-Key", Value: profile.MCPValueBinding{Kind: profile.MCPBindingSecret, SecretRef: string(id)}}}
			}
			beforeSecrets := len(store.values)
			a := activationFor(t, record)
			presenter := urlPresenterFunc(func(ctx context.Context, target string) error {
				presentations.Add(1)
				u, err := url.Parse(target)
				if err != nil {
					return err
				}
				if strings.Contains(target, headerSecret) || strings.Contains(u.Query().Encode(), headerSecret) {
					t.Error("challenge credential escaped through browser presentation")
				}
				if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("resource") != serverURL+"/mcp" {
					t.Error("authorization URL missing PKCE/resource binding")
				}
				if scope := " " + u.Query().Get("scope") + " "; !strings.Contains(scope, " read ") || !strings.Contains(scope, " server-scope ") {
					t.Error("configured scopes replaced the server challenge")
				}
				challenge = u.Query().Get("code_challenge")
				callbackURL = u.Query().Get("redirect_uri")
				if mode == "presenter-unavailable" {
					return errors.New("native URL presentation unavailable")
				}
				state, issuer := u.Query().Get("state"), serverURL
				if mode == "success-custom-challenge" {
					issuer += "/issuer"
				}
				if mode == "state" {
					state = "wrong-state"
				}
				if mode == "issuer" {
					issuer += "/wrong"
				}
				query := url.Values{"code": {"authorization-code"}, "state": {state}, "iss": {issuer}}
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL+"?"+query.Encode(), nil)
				resp, err := http.DefaultClient.Do(request)
				if err != nil {
					return err
				}
				defer closeTestResource(t, resp.Body)
				_, err = io.Copy(io.Discard, resp.Body)
				return err
			})
			status, err := manager.Authorize(t.Context(), a, presenter)
			success := strings.HasPrefix(mode, "success")
			if success {
				if err != nil || !status.Connected || status.SessionRef == "" || exchanges.Load() != 1 || presentations.Load() != 1 {
					t.Fatalf("status=%+v err=%v exchanges=%d presentations=%d", status, err, exchanges.Load(), presentations.Load())
				}
			} else {
				if err == nil || status.SessionRef != "" || len(store.values) != beforeSecrets {
					t.Fatalf("failed authorization persisted authority: %+v %v", status, err)
				}
				if strings.Contains(err.Error(), "client-secret") || strings.Contains(err.Error(), "authorization-code") || strings.Contains(err.Error(), headerSecret) {
					t.Fatal("OAuth error leaked material")
				}
			}
			if mode == "success-post-only" && (probeGETs.Load() != 1 || probePOSTs.Load() != 1) {
				t.Fatal("optional GET did not fall back to exactly one stateless POST probe")
			}
			if strings.HasPrefix(mode, "header-") {
				raw, _ := json.Marshal(status)
				if strings.Contains(string(raw), headerSecret) {
					t.Fatal("header credential escaped through OAuth status")
				}
				if strings.HasPrefix(mode, "header-token-") {
					if presentations.Load() != 1 || exchanges.Load() != 1 {
						t.Fatal("returned credential-bearing metadata was not exercised")
					}
				} else if presentations.Load() != 0 || exchanges.Load() != 0 {
					t.Fatal("credential-bearing challenge metadata reached authorization")
				}
			}
			if callbackURL != "" {
				u, _ := url.Parse(callbackURL)
				conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", u.Host)
				if err == nil {
					closeTestResource(t, conn)
					t.Fatal("callback listener survived authorization")
				}
			}
		})
	}
}

func TestOAuthClientSecretWaitIsCanceledBeforeMutationCommit(t *testing.T) {
	entered := make(chan struct{})
	resolver := resolverFunc(func(ctx context.Context, _ string) (credential.Secret, error) {
		close(entered)
		<-ctx.Done()
		return credential.Secret{}, ctx.Err()
	})
	store := newOAuthTestSecretStore()
	lifecycle := NewManager(resolver)
	defer closeTestResource(t, lifecycle)
	manager := NewOAuthManager(store, resolver, lifecycle)
	record := httpServerRecord("http://127.0.0.1:1/mcp")
	record.HTTP.Auth = profile.MCPHTTPAuthOAuth
	record.HTTP.OAuth = &profile.MCPOAuthConfig{Registration: profile.MCPOAuthPreregistered, ClientID: "client", ClientSecret: &profile.MCPSecretBinding{SecretRef: "waiting"}, Status: profile.MCPOAuthMissing}
	done := make(chan error, 1)
	go func() {
		_, err := manager.Authorize(t.Context(), activationFor(t, record), urlPresenterFunc(func(context.Context, string) error { return errors.New("must not present") }))
		done <- err
	}()
	await(t, entered)
	if err := lifecycle.RunServerMutations(t.Context(), []string{record.ID}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(store.values) != 0 {
		t.Fatal("canceled authorization minted a secret")
	}
}

func TestSilentOAuthRenewalSerializesSameIDAndKeepsAllRedactionMaterial(t *testing.T) {
	var renewals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		if r.Form.Get("grant_type") != "refresh_token" || !strings.HasSuffix(r.Form.Get("resource"), "/mcp") {
			t.Error("refresh omitted resource indicator")
		}
		renewals.Add(1)
		writeOAuthJSON(t, w, map[string]any{"access_token": "new-access-material", "refresh_token": "new-refresh-material", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	store := newOAuthTestSecretStore()
	raw, _ := json.Marshal(storedOAuthToken{AccessToken: "old-access-material", RefreshToken: "old-refresh-material", ClientSecret: "client-secret-material", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute), ClientID: "client", TokenURL: server.URL + "/token", Resource: server.URL + "/mcp"})
	ref, err := store.CreateSystemSecret(t.Context(), credential.NewSecretBytes(raw))
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := NewManager(store)
	defer closeTestResource(t, lifecycle)
	record := httpServerRecord(server.URL + "/mcp")
	record.HTTP.Auth = profile.MCPHTTPAuthOAuth
	record.HTTP.OAuth = &profile.MCPOAuthConfig{Registration: profile.MCPOAuthPreregistered, ClientID: "client", Status: profile.MCPOAuthConnected, SessionRef: &profile.MCPSecretBinding{SecretRef: string(ref), Owned: true}}
	coordinator := newOAuthRefreshCoordinator()
	var configs []httpSessionConfig
	for range 2 {
		config, err := buildHTTPTransport(t.Context(), t.Context(), activationFor(t, record), store, coordinator, lifecycle)
		if err != nil {
			t.Fatal(err)
		}
		defer config.cleanup()
		configs = append(configs, config)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, config := range configs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			source, err := config.transport.OAuthHandler.TokenSource(t.Context())
			if err == nil {
				_, err = source.Token()
			}
			errorsCh <- err
		}()
	}
	close(start)
	workers.Wait()
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	if renewals.Load() != 1 || store.replacements.Load() != 1 || len(store.values) != 1 {
		t.Fatalf("renewals=%d replacements=%d refs=%d", renewals.Load(), store.replacements.Load(), len(store.values))
	}
	if _, exists := store.values[ref]; !exists {
		t.Fatal("renewal changed persisted ID")
	}
	for _, config := range configs {
		values := config.sensitive.snapshot()
		for _, secret := range []string{"old-access-material", "old-refresh-material", "new-access-material", "new-refresh-material", "client-secret-material"} {
			if redactKnown(secret, values) == secret {
				t.Fatalf("session did not screen token history %q", secret)
			}
		}
	}
}

func TestExpiredUnrenewableOAuthFailsClosed(t *testing.T) {
	store := newOAuthTestSecretStore()
	raw, _ := json.Marshal(storedOAuthToken{AccessToken: "expired-material", TokenType: "Bearer", Expiry: time.Now().Add(-time.Minute)})
	ref, _ := store.CreateSystemSecret(t.Context(), credential.NewSecretBytes(raw))
	clear(raw)
	lifecycle := NewManager(store)
	defer closeTestResource(t, lifecycle)
	record := httpServerRecord("http://127.0.0.1:1/mcp")
	record.HTTP.Auth = profile.MCPHTTPAuthOAuth
	record.HTTP.OAuth = &profile.MCPOAuthConfig{Registration: profile.MCPOAuthPreregistered, ClientID: "client", Status: profile.MCPOAuthConnected, SessionRef: &profile.MCPSecretBinding{SecretRef: string(ref), Owned: true}}
	config, err := buildHTTPTransport(t.Context(), t.Context(), activationFor(t, record), store, newOAuthRefreshCoordinator(), lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	defer config.cleanup()
	source, err := config.transport.OAuthHandler.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Token(); !errors.Is(err, ErrOAuthReconnectRequired) {
		t.Fatal(err)
	}
	if store.replacements.Load() != 0 {
		t.Fatal("unrenewable token was persisted")
	}
}
