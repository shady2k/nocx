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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/shady2k/nocx/internal/credential"
	"golang.org/x/oauth2"
)

const oauthAuthorizationTimeout = 5 * time.Minute

// SystemSecretStore exposes backend-only OAuth creation and durable owned deletion.
// The guarded capability owns journal attachment and metadata finalization.
type SystemSecretStore interface {
	CreateSystemSecret(context.Context, credential.Secret) (credential.SecretID, error)
	ScheduleDelete(context.Context, credential.SecretID) error
}

// OAuthManager owns explicit authorization; persisted refs, not a local map,
// identify sessions for forget and losing-CAS orphan cleanup.
type OAuthManager struct {
	store      SystemSecretStore
	resolver   SecretResolver
	operations OperationRunner
}

func NewOAuthManager(store SystemSecretStore, resolver SecretResolver, operations OperationRunner) *OAuthManager {
	return &OAuthManager{store: store, resolver: resolver, operations: operations}
}

func (m *OAuthManager) Authorize(ctx context.Context, activation Activation, presenter URLPresenter) (status OAuthStatus, err error) {
	if presenter == nil || m == nil || m.store == nil || m.operations == nil {
		return OAuthStatus{}, ErrOAuthAuthorizationFailed
	}
	activation = activation.Clone()
	if validationErr := activation.validate(false); validationErr != nil || activation.HTTP == nil || activation.HTTP.Auth != HTTPAuthOAuth {
		return OAuthStatus{}, ErrInvalidActivation
	}
	ctx, cancel := context.WithTimeout(ctx, oauthAuthorizationTimeout)
	defer cancel()
	err = m.operations.RunServerOperation(ctx, activation.ServerID, func(ctx context.Context) error {
		if verificationErr := m.operations.VerifyActivation(ctx, activation); verificationErr != nil {
			return verificationErr
		}
		var inner error
		status, inner = m.authorize(ctx, activation, presenter)
		return inner
	})
	return status, safeOAuthError(err)
}

func (m *OAuthManager) authorize(ctx context.Context, activation Activation, presenter URLPresenter) (OAuthStatus, error) {
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

	callbackCtx := ctx
	callback := make(chan *auth.AuthorizationResult, 1)
	callbackErr := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return callbackCtx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/callback" || len(r.URL.RawQuery) > 16<<10 || r.Host != listener.Addr().String() {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			for _, values := range q {
				if len(values) != 1 {
					select {
					case callbackErr <- ErrOAuthAuthorizationFailed:
					default:
					}
					http.Error(w, "Invalid authorization response.", http.StatusBadRequest)
					return
				}
			}
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
			if !validOAuthCallbackValue(code, 4096) || !validOAuthCallbackValue(state, 4096) || (q.Get("iss") != "" && !validOAuthCallbackValue(q.Get("iss"), 4096)) {
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
			_, _ = io.WriteString(w, "Authorization response received. Return to Settings to check the result.")
		}),
	}
	serveErr := make(chan error, 1)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); serveErr <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return OAuthStatus{}, errors.New("MCP OAuth callback listener has an invalid address")
	}
	redirectURL := "http://127.0.0.1:" + strconv.Itoa(tcpAddr.Port) + "/callback"
	guard := newGuardedHTTPTransport(cfg.Endpoint, nil, activation.Limits.MaxResultBytes, activation.Limits.StartupTimeout)
	guard.lifetime = callbackCtx
	guard.useOAuthPolicy()
	defer guard.close()
	sensitive := &sensitiveValues{}
	sensitive.add(secret)
	capture := &oauthCaptureTransport{base: guard, sensitive: sensitive}
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
		// Do not inherit the SDK's background refresh source: explicit Connect
		// consumes only the token already exchanged under callbackCtx.
		NewTokenSource: func(_ context.Context, _ *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			if token == nil || !token.Valid() {
				return nil, ErrOAuthAuthorizationFailed
			}
			return oauth2.StaticTokenSource(token), nil
		},
		AuthorizationCodeFetcher: func(fetchCtx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if !capture.pkceReady() {
				return nil, ErrOAuthAuthorizationFailed
			}
			_, _, _, issuer, _ := capture.snapshot()
			material := sensitive.snapshot()
			if oauthContainsMaterial(args.URL, material) || oauthContainsMaterial(issuer, material) {
				return nil, ErrOAuthAuthorizationFailed
			}
			if validationErr := guard.validateOAuthURL(fetchCtx, args.URL); validationErr != nil {
				return nil, validationErr
			}
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
	req, resp, headerMaterial, err := m.authorizationChallenge(callbackCtx, activation)
	if err != nil {
		return OAuthStatus{}, err
	}
	sensitive.add(headerMaterial...)
	materialBeforeAuthorization := sensitive.snapshot()
	for _, challenge := range resp.Header.Values("WWW-Authenticate") {
		if oauthContainsMaterial(challenge, materialBeforeAuthorization) {
			return OAuthStatus{}, ErrOAuthAuthorizationFailed
		}
	}
	capture.metadataResources = oauthMetadataResources(cfg.Endpoint, resp.Header)
	if authorizeErr := handler.Authorize(callbackCtx, req, resp); authorizeErr != nil {
		return OAuthStatus{}, authorizeErr
	}
	source, err := handler.TokenSource(callbackCtx)
	if err != nil || source == nil {
		return OAuthStatus{}, errors.New("MCP OAuth did not return a token source")
	}
	token, err := source.Token()
	if err != nil || token == nil || !token.Valid() || !validCredentialValue(token.AccessToken) || (token.RefreshToken != "" && !validCredentialValue(token.RefreshToken)) || (token.TokenType != "" && !strings.EqualFold(token.TokenType, "Bearer")) {
		return OAuthStatus{}, errors.New("MCP OAuth did not return an access token")
	}
	grantedScopes := oauthGrantedScopes(token, cfg.OAuthScopes)
	clientID, clientSecret, tokenURL, issuer, resource := capture.snapshot()
	if resource == "" || len(resource) > 4096 || len(issuer) > 2048 || len(grantedScopes) > 64 || len(clientID) > 512 || !validCredentialValue(clientID) && cfg.OAuthClientID == "" || clientSecret != "" && !validCredentialValue(clientSecret) {
		return OAuthStatus{}, ErrOAuthAuthorizationFailed
	}
	sensitive.add(token.AccessToken, token.RefreshToken, clientSecret)
	material := sensitive.snapshot()
	if oauthContainsMaterial(issuer, material) {
		return OAuthStatus{}, ErrOAuthAuthorizationFailed
	}
	seenScopes := make(map[string]bool, len(grantedScopes))
	for _, scope := range grantedScopes {
		if scope == "" || len(scope) > 256 || strings.ContainsAny(scope, "\r\n\x00") || seenScopes[scope] || oauthContainsMaterial(scope, material) {
			return OAuthStatus{}, ErrOAuthAuthorizationFailed
		}
		seenScopes[scope] = true
	}
	stored := storedOAuthToken{AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, Expiry: token.Expiry, ClientID: cfg.OAuthClientID, ClientSecret: secret, TokenURL: tokenURL, Resource: resource, Scopes: grantedScopes}
	if clientID != "" {
		stored.ClientID = clientID
	}
	if clientSecret != "" {
		stored.ClientSecret = clientSecret
	}
	encoded, err := json.Marshal(stored) // #nosec G117 -- the bytes are immediately wrapped in credential.Secret and written only to SecretStore
	if err != nil {
		return OAuthStatus{}, err
	}
	id, err := m.store.CreateSystemSecret(callbackCtx, credential.NewSecretBytes(encoded))
	clear(encoded)
	if err != nil {
		return OAuthStatus{}, err
	}
	if err := callbackCtx.Err(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(callbackCtx), 5*time.Second)
		defer cancel()
		_ = m.store.ScheduleDelete(cleanupCtx, id)
		return OAuthStatus{}, err
	}
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
	case errors.Is(err, ErrInvalidActivation):
		return ErrInvalidActivation
	case errors.Is(err, ErrSecretUnavailable):
		return ErrSecretUnavailable
	case errors.Is(err, ErrOAuthReconnectRequired):
		return ErrOAuthReconnectRequired
	case errors.Is(err, ErrClosed):
		return ErrClosed
	case errors.Is(err, ErrActivationChanged):
		return ErrActivationChanged
	default:
		return ErrOAuthAuthorizationFailed
	}
}

func (m *OAuthManager) Forget(ctx context.Context, sessionRef string) error {
	if sessionRef == "" {
		return nil
	}
	if m == nil || m.store == nil {
		return ErrSecretUnavailable
	}
	if err := m.store.ScheduleDelete(ctx, credential.SecretID(sessionRef)); err != nil {
		return preferContext(ctx, ErrSecretUnavailable)
	}
	return nil
}

func (m *OAuthManager) DiscardOAuthSession(ctx context.Context, sessionRef string) error {
	return m.Forget(ctx, sessionRef)
}

func (m *OAuthManager) resolveClientSecret(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if m.resolver == nil {
		return "", ErrSecretUnavailable
	}
	secret, err := resolveSecret(ctx, m.resolver, ref)
	if err != nil {
		return "", err
	}
	var value string
	if err := secret.Use(func(raw []byte) error { value = string(raw); return nil }); err != nil || !validCredentialValue(value) {
		return "", ErrSecretUnavailable
	}
	return value, nil
}

type oauthCaptureTransport struct {
	base              http.RoundTripper
	mu                sync.Mutex
	clientID          string
	clientSecret      string
	tokenURL          string
	issuer            string
	pkce              bool
	resource          string
	metadataResources map[string]string
	failed            bool
	sensitive         *sensitiveValues
}

func (t *oauthCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	failed := t.failed
	t.mu.Unlock()
	if failed || oauthContainsMaterial(req.URL.String(), t.sensitive.snapshot()) {
		return nil, ErrOAuthAuthorizationFailed
	}
	registration, tokenRequest := classifyOAuthRequest(req)
	var resource string
	if tokenRequest {
		body, err := io.ReadAll(io.LimitReader(req.Body, (128<<10)+1))
		_ = req.Body.Close()
		if err != nil || len(body) > 128<<10 {
			return nil, ErrOAuthAuthorizationFailed
		}
		values, err := url.ParseQuery(string(body))
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			return nil, ErrOAuthAuthorizationFailed
		}
		if values.Get("grant_type") == "authorization_code" {
			resource = values.Get("resource")
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (128<<10)+1))
	_ = resp.Body.Close()
	if readErr != nil || len(body) > 128<<10 {
		return nil, ErrOAuthAuthorizationFailed
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if expected, known := t.metadataResources[req.URL.String()]; known && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var metadata oauthex.ProtectedResourceMetadata
		if json.Unmarshal(body, &metadata) != nil || metadata.Resource != expected || len(metadata.AuthorizationServers) == 0 {
			// The SDK permits fallback after a metadata validation error. An
			// authoritative but invalid document must instead fail this Connect,
			// not silently become a legacy authorization at a different resource.
			t.mu.Lock()
			t.failed = true
			t.mu.Unlock()
			return nil, ErrOAuthAuthorizationFailed
		}
	}
	var raw struct {
		ClientID              string   `json:"client_id"`
		ClientSecret          string   `json:"client_secret"`
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		Resource              string   `json:"resource"`
		AuthorizationServers  []string `json:"authorization_servers"`
		Scopes                []string `json:"scopes_supported"`
		PKCEMethods           []string `json:"code_challenge_methods_supported"`
	}
	_ = json.Unmarshal(body, &raw)
	material := t.sensitive.snapshot()
	for _, values := range [][]string{{raw.ClientID, raw.Issuer, raw.AuthorizationEndpoint, raw.TokenEndpoint, raw.RegistrationEndpoint, raw.Resource}, raw.AuthorizationServers, raw.Scopes} {
		for _, value := range values {
			if oauthContainsMaterial(value, material) {
				t.mu.Lock()
				t.failed = true
				t.mu.Unlock()
				return nil, ErrOAuthAuthorizationFailed
			}
		}
	}
	if raw.AuthorizationEndpoint != "" && !supportsPKCES256(raw.PKCEMethods) {
		return nil, errors.New("MCP OAuth authorization server must support PKCE S256")
	}
	t.mu.Lock()
	if raw.AuthorizationEndpoint != "" {
		t.pkce = true
	}
	if registration {
		if raw.ClientID != "" {
			t.clientID = raw.ClientID
		}
		if raw.ClientSecret != "" {
			t.clientSecret = raw.ClientSecret
			if t.sensitive != nil {
				t.sensitive.add(raw.ClientSecret)
			}
		}
	}
	if tokenRequest && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		t.tokenURL = req.URL.String()
		t.resource = resource
	}
	if raw.Issuer != "" {
		t.issuer = raw.Issuer
	}
	t.mu.Unlock()
	return resp, nil
}

func (t *oauthCaptureTransport) pkceReady() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pkce
}

func classifyOAuthRequest(req *http.Request) (registration, token bool) {
	if req == nil || req.Method != http.MethodPost || req.Body == nil {
		return false, false
	}
	contentType := req.Header.Get("Content-Type")
	registration = strings.HasPrefix(contentType, "application/json")
	token = strings.HasPrefix(contentType, "application/x-www-form-urlencoded")
	return registration, token
}

func supportsPKCES256(methods []string) bool {
	for _, method := range methods {
		if method == "S256" {
			return true
		}
	}
	return false
}

func (t *oauthCaptureTransport) snapshot() (clientID, clientSecret, tokenURL, issuer, resource string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clientID, t.clientSecret, t.tokenURL, t.issuer, t.resource
}

var (
	_ OAuthService      = (*OAuthManager)(nil)
	_ http.RoundTripper = (*oauthCaptureTransport)(nil)
)

func (m *OAuthManager) authorizationChallenge(ctx context.Context, activation Activation) (*http.Request, *http.Response, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, activation.Limits.StartupTimeout)
	defer cancel()
	headers, sensitive, err := resolveHeaders(ctx, activation.HTTP.Headers, m.resolver)
	if err != nil {
		return nil, nil, nil, err
	}
	// The configured MCP endpoint has MCP routing rules; discovered OAuth
	// endpoints retain their stricter, separately owned OAuth policy.
	guard := newGuardedHTTPTransport(activation.HTTP.Endpoint, headers, activation.Limits.MaxResultBytes, activation.Limits.StartupTimeout)
	guard.lifetime = ctx
	guard.oauthChallenge = true
	defer guard.close()
	client := &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, activation.HTTP.Endpoint, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	_ = resp.Body.Close()
	resp.Body = http.NoBody
	if resp.StatusCode == http.StatusMethodNotAllowed {
		// Standalone GET is optional. Probe with the SDK's stateless discovery
		// request: this initializes no session and never lists or calls tools.
		const version = "2026-07-28"
		payload, marshalErr := json.Marshal(struct {
			JSONRPC string             `json:"jsonrpc"`
			ID      int                `json:"id"`
			Method  string             `json:"method"`
			Params  sdk.DiscoverParams `json:"params"`
		}{"2.0", 1, "server/discover", sdk.DiscoverParams{Meta: sdk.Meta{
			sdk.MetaKeyProtocolVersion:    version,
			sdk.MetaKeyClientInfo:         &sdk.Implementation{Name: "nocx-oauth", Version: "1"},
			sdk.MetaKeyClientCapabilities: map[string]any{},
		}}})
		if marshalErr != nil {
			return nil, nil, nil, marshalErr
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, activation.HTTP.Endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, nil, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", version)
		resp, err = client.Do(req)
		if err != nil {
			return nil, nil, nil, err
		}
		_ = resp.Body.Close()
		resp.Body = http.NoBody
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil, nil, nil, ErrOAuthAuthorizationFailed
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil, nil, nil, ErrOAuthAuthorizationFailed
	}
	scopes := append([]string(nil), activation.HTTP.OAuthScopes...)
	for _, challenge := range challenges {
		// Reject decoded parameters before resource_metadata can become a
		// request URL, or a scope can become a browser-visible authorization URL.
		for _, value := range challenge.Params {
			if oauthContainsMaterial(value, sensitive) {
				return nil, nil, nil, ErrOAuthAuthorizationFailed
			}
		}
		if challenge.Scheme != "bearer" {
			continue
		}
		for _, scope := range strings.Fields(challenge.Params["scope"]) {
			found := false
			for _, existing := range scopes {
				if existing == scope {
					found = true
					break
				}
			}
			if !found {
				scopes = append(scopes, scope)
			}
		}
	}
	if len(scopes) > 64 {
		return nil, nil, nil, ErrOAuthAuthorizationFailed
	}
	if len(scopes) > 0 {
		// Keep every real challenge (including resource_metadata and error).
		// The SDK's first scoped Bearer challenge supplies the configured union.
		resp.Header["Www-Authenticate"] = append([]string{fmt.Sprintf(`Bearer scope=%q`, strings.Join(scopes, " "))}, resp.Header.Values("WWW-Authenticate")...)
	}
	return req, resp, sensitive, nil
}

func oauthContainsMaterial(value string, sensitive []string) bool {
	if redactKnown(value, sensitive) != value {
		return true
	}
	// URL-bearing metadata and the final browser URL can percent-encode a
	// credential. Screen its decoded value as well as its wire spelling.
	if decoded, err := url.QueryUnescape(value); err == nil && redactKnown(decoded, sensitive) != decoded {
		return true
	}
	if decoded, err := url.PathUnescape(value); err == nil && redactKnown(decoded, sensitive) != decoded {
		return true
	}
	return false
}

func oauthMetadataResources(endpoint string, headers http.Header) map[string]string {
	resource, _ := url.Parse(endpoint) // activation validation already owns syntax
	pathMetadata := *resource
	pathMetadata.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(resource.Path, "/")
	rootMetadata := *resource
	rootMetadata.Path = "/.well-known/oauth-protected-resource"
	root := *resource
	root.Path = ""
	expected := map[string]string{pathMetadata.String(): endpoint, rootMetadata.String(): root.String()}
	challenges, _ := oauthex.ParseWWWAuthenticate(headers.Values("WWW-Authenticate"))
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" && challenge.Params["resource_metadata"] != "" {
			expected[challenge.Params["resource_metadata"]] = endpoint
			break
		}
	}
	return expected
}
