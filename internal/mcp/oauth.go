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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/shady2k/nocx/internal/credential"
	"golang.org/x/oauth2"
)

const oauthAuthorizationTimeout = 5 * time.Minute

// OAuthManager owns explicit Settings-only OAuth authorization. It never runs
// from Runtime.Invoke and keeps the resulting token material inside one owned
// SecretStore row.
type OAuthManager struct {
	store    credential.SecretStore
	resolver SecretResolver
	mu       sync.Mutex
	sessions map[string]credential.SecretID
}

func NewOAuthManager(store credential.SecretStore, resolver SecretResolver) *OAuthManager {
	return &OAuthManager{store: store, resolver: resolver, sessions: make(map[string]credential.SecretID)}
}

func (m *OAuthManager) Authorize(ctx context.Context, activation Activation, presenter URLPresenter) (status OAuthStatus, err error) {
	defer func() {
		if err != nil {
			err = safeOAuthError(err)
		}
	}()
	if presenter == nil || m == nil || m.store == nil {
		return OAuthStatus{}, errors.New("MCP OAuth authorization is unavailable")
	}
	if validationErr := activation.validate(false); validationErr != nil || activation.HTTP == nil || activation.HTTP.Auth != HTTPAuthOAuth {
		return OAuthStatus{}, ErrInvalidActivation
	}
	cfg := activation.HTTP
	secret, err := m.resolveClientSecret(ctx, cfg.OAuthClientSecretRef)
	if err != nil {
		return OAuthStatus{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return OAuthStatus{}, fmt.Errorf("MCP OAuth callback: %w", err)
	}
	defer func() { _ = listener.Close() }()

	callbackCtx, cancel := context.WithTimeout(ctx, oauthAuthorizationTimeout)
	defer cancel()
	callback := make(chan *auth.AuthorizationResult, 1)
	callbackErr := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if oauthError := q.Get("error"); oauthError != "" {
				if len(oauthError) > 256 || strings.ContainsAny(oauthError, "\r\n") {
					oauthError = "authorization_failed"
				}
				select {
				case callbackErr <- fmt.Errorf("OAuth authorization failed: %s", oauthError):
				default:
				}
				_, _ = io.WriteString(w, "Authorization failed. You can close this window.")
				return
			}
			code := q.Get("code")
			state := q.Get("state")
			if !validOAuthCallbackValue(code, 4096) || !validOAuthCallbackValue(state, 4096) {
				select {
				case callbackErr <- errors.New("OAuth callback is missing or invalid code/state"):
				default:
				}
				_, _ = io.WriteString(w, "Authorization response is incomplete. You can close this window.")
				return
			}
			select {
			case callback <- &auth.AuthorizationResult{Code: code, State: state, Iss: q.Get("iss")}:
			default:
			}
			_, _ = io.WriteString(w, "Authorization complete. You can close this window.")
		}),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		_ = server.Shutdown(shutdownCtx)
		shutdownCancel()
		select {
		case <-serveErr:
		default:
		}
	}()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return OAuthStatus{}, errors.New("MCP OAuth callback listener has an invalid address")
	}
	redirectURL := "http://127.0.0.1:" + strconv.Itoa(tcpAddr.Port) + "/callback"
	guard := newGuardedHTTPTransport(cfg.Endpoint, nil, activation.Limits.MaxResultBytes, activation.Limits.StartupTimeout)
	capture := &oauthCaptureTransport{base: guard}
	client := &http.Client{Transport: capture, CheckRedirect: guard.CheckRedirect}
	registration := &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
		RedirectURIs:    []string{redirectURL},
		ClientName:      "nocx",
		GrantTypes:      []string{"authorization_code", "refresh_token"},
		ResponseTypes:   []string{"code"},
		Scope:           strings.Join(cfg.OAuthScopes, " "),
		ApplicationType: "native",
	}}
	var prereg *oauthex.ClientCredentials
	if cfg.OAuthRegistration == "preregistered" {
		prereg = &oauthex.ClientCredentials{ClientID: cfg.OAuthClientID}
		if secret != "" {
			prereg.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: secret}
		}
	}
	if cfg.OAuthRegistration != "preregistered" && cfg.OAuthRegistration != "dynamic" {
		return OAuthStatus{}, ErrInvalidActivation
	}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         redirectURL,
		PreregisteredClient: prereg,
		DynamicClientRegistrationConfig: func() *auth.DynamicClientRegistrationConfig {
			if prereg != nil {
				return nil
			}
			return registration
		}(),
		RequestRefreshToken: true,
		Client:              client,
		AuthorizationCodeFetcher: func(fetchCtx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if presentErr := presenter.PresentURL(fetchCtx, args.URL); presentErr != nil {
				return nil, presentErr
			}
			select {
			case result := <-callback:
				return result, nil
			case callbackErrValue := <-callbackErr:
				return nil, callbackErrValue
			case serveErrValue := <-serveErr:
				return nil, fmt.Errorf("MCP OAuth callback server: %w", serveErrValue)
			case <-fetchCtx.Done():
				return nil, fetchCtx.Err()
			}
		},
	})
	if err != nil {
		return OAuthStatus{}, err
	}
	req, err := http.NewRequestWithContext(callbackCtx, http.MethodGet, cfg.Endpoint, nil)
	if err != nil {
		return OAuthStatus{}, err
	}
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: http.NoBody, Request: req}
	if len(cfg.OAuthScopes) > 0 {
		resp.Header.Set("WWW-Authenticate", fmt.Sprintf(`Bearer scope=%q`, strings.Join(cfg.OAuthScopes, " ")))
	}
	if authorizeErr := handler.Authorize(callbackCtx, req, resp); authorizeErr != nil {
		return OAuthStatus{}, authorizeErr
	}
	source, err := handler.TokenSource(context.Background())
	if err != nil || source == nil {
		return OAuthStatus{}, errors.New("MCP OAuth did not return a token source")
	}
	token, err := source.Token()
	if err != nil || token == nil || token.AccessToken == "" {
		return OAuthStatus{}, errors.New("MCP OAuth did not return an access token")
	}
	grantedScopes := oauthGrantedScopes(token, cfg.OAuthScopes)
	clientID, clientSecret, tokenURL, issuer := capture.snapshot()
	stored := storedOAuthToken{AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, Expiry: token.Expiry, ClientID: cfg.OAuthClientID, TokenURL: tokenURL, Scopes: grantedScopes}
	if clientID != "" {
		stored.ClientID = clientID
	}
	if clientSecret != "" {
		stored.ClientSecret = clientSecret
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return OAuthStatus{}, err
	}
	id, err := m.store.Create(callbackCtx, credential.NewSecretBytes(encoded))
	clear(encoded)
	if err != nil {
		return OAuthStatus{}, err
	}
	m.mu.Lock()
	m.sessions[activation.ServerID] = id
	m.mu.Unlock()
	expires := token.Expiry
	var expiresAt *time.Time
	if !expires.IsZero() {
		expiresAt = &expires
	}
	return OAuthStatus{Connected: true, Issuer: issuer, Scopes: grantedScopes, ExpiresAt: expiresAt, SessionRef: string(id)}, nil
}

func validOAuthCallbackValue(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && !strings.ContainsAny(value, "\r\n")
}

func oauthGrantedScopes(token *oauth2.Token, configured []string) []string {
	if token != nil {
		if raw, ok := token.Extra("scope").(string); ok && strings.TrimSpace(raw) != "" {
			scopes := strings.Fields(raw)
			if len(scopes) > 0 {
				return scopes
			}
		}
	}
	return append([]string(nil), configured...)
}

func safeOAuthError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalidActivation),
		errors.Is(err, ErrSecretUnavailable),
		errors.Is(err, ErrOAuthReconnectRequired):
		return err
	default:
		return ErrOAuthAuthorizationFailed
	}
}

func (m *OAuthManager) Forget(ctx context.Context, serverID string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	id := m.sessions[serverID]
	delete(m.sessions, serverID)
	m.mu.Unlock()
	if id == "" || m.store == nil {
		return nil
	}
	return m.store.Delete(ctx, id)
}

func (m *OAuthManager) DiscardOAuthSession(ctx context.Context, serverID, sessionRef string) error {
	if m == nil || sessionRef == "" {
		return nil
	}
	m.mu.Lock()
	if string(m.sessions[serverID]) != sessionRef {
		m.mu.Unlock()
		return nil
	}
	delete(m.sessions, serverID)
	m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	return m.store.Delete(ctx, credential.SecretID(sessionRef))
}

func (m *OAuthManager) resolveClientSecret(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if m.resolver == nil {
		return "", ErrSecretUnavailable
	}
	secret, err := m.resolver.ResolveSecret(ctx, ref)
	if err != nil {
		return "", err
	}
	var value string
	if err := secret.Use(func(raw []byte) error { value = string(raw); return nil }); err != nil {
		return "", ErrSecretUnavailable
	}
	return value, nil
}

type oauthCaptureTransport struct {
	base         http.RoundTripper
	mu           sync.Mutex
	clientID     string
	clientSecret string
	tokenURL     string
	issuer       string
}

func (t *oauthCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	registration, tokenRequest := classifyOAuthRequest(req)
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil {
		return resp, nil
	}
	var raw struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Issuer       string `json:"issuer"`
	}
	_ = json.Unmarshal(body, &raw)
	t.mu.Lock()
	if registration {
		if raw.ClientID != "" {
			t.clientID = raw.ClientID
		}
		if raw.ClientSecret != "" {
			t.clientSecret = raw.ClientSecret
		}
	}
	if tokenRequest {
		t.tokenURL = req.URL.String()
	}
	if raw.Issuer != "" {
		t.issuer = raw.Issuer
	}
	t.mu.Unlock()
	return resp, nil
}

func classifyOAuthRequest(req *http.Request) (registration, token bool) {
	if req == nil || req.Method != http.MethodPost || req.Body == nil {
		return false, false
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 64<<10))
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), req.Body))
	if err != nil {
		return false, false
	}
	registration = bytes.Contains(body, []byte(`"redirect_uris"`))
	token = bytes.Contains(body, []byte("grant_type=authorization_code")) ||
		bytes.Contains(body, []byte("grant_type=refresh_token"))
	return registration, token
}

func (t *oauthCaptureTransport) snapshot() (clientID, clientSecret, tokenURL, issuer string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clientID, t.clientSecret, t.tokenURL, t.issuer
}

var (
	_ OAuthService      = (*OAuthManager)(nil)
	_ http.RoundTripper = (*oauthCaptureTransport)(nil)
)
