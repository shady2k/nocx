package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/httppolicy"
	"golang.org/x/oauth2"
)

const maxHTTPRedirects = 5

type (
	resolvedHeader    struct{ name, value string }
	httpSessionConfig struct {
		transport  *sdk.StreamableClientTransport
		cleanup    func()
		beginClose func()
		sensitive  *sensitiveValues
	}
)

type oauthRefreshCoordinator struct {
	mu    sync.Mutex
	locks map[string]*oauthRefreshLock
}
type oauthRefreshLock struct {
	gate  chan struct{}
	users int
}

func newOAuthRefreshCoordinator() *oauthRefreshCoordinator {
	return &oauthRefreshCoordinator{locks: make(map[string]*oauthRefreshLock)}
}

func (c *oauthRefreshCoordinator) lock(ctx context.Context, ref string) (func(), error) {
	c.mu.Lock()
	lock := c.locks[ref]
	if lock == nil {
		lock = &oauthRefreshLock{gate: make(chan struct{}, 1)}
		c.locks[ref] = lock
	}
	lock.users++
	c.mu.Unlock()
	release := func() {
		c.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(c.locks, ref)
		}
		c.mu.Unlock()
	}
	select {
	case lock.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.gate
			release()
			return nil, err
		}
		return func() { <-lock.gate; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

func buildHTTPTransport(ctx, lifetime context.Context, activation Activation, resolver SecretResolver, coordinator *oauthRefreshCoordinator, operations OperationRunner) (httpSessionConfig, error) {
	cfg := activation.HTTP
	if cfg == nil {
		return httpSessionConfig{}, ErrInvalidActivation
	}
	headers, values, err := resolveHeaders(ctx, cfg.Headers, resolver)
	if err != nil {
		return httpSessionConfig{}, err
	}
	sensitive := &sensitiveValues{}
	sensitive.add(values...)
	var oauthHandler auth.OAuthHandler
	var oauthGuard *guardedHTTPTransport
	switch cfg.Auth {
	case HTTPAuthNone:
	case HTTPAuthBearer:
		secret, err := resolveSecret(ctx, resolver, cfg.BearerRef)
		if err != nil {
			return httpSessionConfig{}, err
		}
		var token string
		if secret.Use(func(value []byte) error { token = string(value); return nil }) != nil || !validCredentialValue(token) {
			return httpSessionConfig{}, ErrSecretUnavailable
		}
		headers = append(headers, resolvedHeader{name: "Authorization", value: "Bearer " + token})
		sensitive.add(token)
	case HTTPAuthOAuth:
		replacer, ok := resolver.(OAuthTokenReplacer)
		if !ok {
			return httpSessionConfig{}, ErrSecretUnavailable
		}
		secret, err := resolveSecret(ctx, resolver, cfg.OAuthSessionRef)
		if err != nil {
			return httpSessionConfig{}, err
		}
		initialToken, _, values, _, err := decodeOAuthToken(secret)
		if err != nil {
			return httpSessionConfig{}, err
		}
		sensitive.add(values...)
		clientSecret := ""
		if cfg.OAuthClientSecretRef != "" {
			secret, err := resolveSecret(ctx, resolver, cfg.OAuthClientSecretRef)
			if err != nil {
				return httpSessionConfig{}, err
			}
			if secret.Use(func(value []byte) error { clientSecret = string(value); return nil }) != nil || !validCredentialValue(clientSecret) {
				return httpSessionConfig{}, ErrSecretUnavailable
			}
			sensitive.add(clientSecret)
		}
		oauthGuard = newGuardedHTTPTransport(cfg.Endpoint, nil, activation.Limits.MaxResultBytes, activation.Limits.CallTimeout)
		oauthGuard.lifetime = lifetime
		oauthGuard.useOAuthPolicy()
		client := &http.Client{Transport: oauthGuard, CheckRedirect: oauthGuard.CheckRedirect}
		oauthHandler = &nonInteractiveOAuth{source: &persistentOAuthTokenSource{
			resolver: resolver, replacer: replacer, sessionRef: cfg.OAuthSessionRef,
			client: client, clientSecret: clientSecret, coordinator: coordinator,
			sensitive: sensitive, lifetime: lifetime, timeout: activation.Limits.CallTimeout,
			operations: operations, serverID: activation.ServerID,
			last: initialToken,
		}}
	default:
		return httpSessionConfig{}, ErrInvalidActivation
	}
	guard := newGuardedHTTPTransport(cfg.Endpoint, headers, activation.Limits.MaxResultBytes, max(activation.Limits.StartupTimeout, activation.Limits.CallTimeout))
	guard.lifetime = lifetime
	guard.oauthChallenge = cfg.Auth == HTTPAuthOAuth
	guard.notificationStreams = true
	return httpSessionConfig{
		transport: &sdk.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect},
			MaxRetries: -1, OAuthHandler: oauthHandler,
		},
		cleanup: func() {
			guard.close()
			if oauthGuard != nil {
				oauthGuard.close()
			}
		},
		beginClose: func() {
			guard.closing.Store(true)
			if h, ok := oauthHandler.(*nonInteractiveOAuth); ok {
				h.source.closing.Store(true)
			}
		},
		sensitive: sensitive,
	}, nil
}

func validCredentialValue(value string) bool {
	return value != "" && len(value) <= 64<<10 && !strings.ContainsAny(value, "\r\n\x00")
}

func resolveHeaders(ctx context.Context, bindings []Binding, resolver SecretResolver) ([]resolvedHeader, []string, error) {
	headers := make([]resolvedHeader, 0, len(bindings))
	values := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		var value string
		switch {
		case binding.Literal != nil && binding.SecretRef == "":
			value = *binding.Literal
		case binding.Literal == nil && binding.SecretRef != "":
			secret, err := resolveSecret(ctx, resolver, binding.SecretRef)
			if err != nil {
				return nil, nil, err
			}
			if secret.Use(func(raw []byte) error { value = string(raw); return nil }) != nil {
				return nil, nil, ErrSecretUnavailable
			}
		default:
			return nil, nil, ErrInvalidActivation
		}
		if len(value) > 64<<10 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, nil, ErrInvalidActivation
		}
		headers = append(headers, resolvedHeader{name: http.CanonicalHeaderKey(binding.Name), value: value})
		if binding.SecretRef != "" {
			values = append(values, value)
		}
	}
	return headers, values, nil
}

func resolveSecret(ctx context.Context, resolver SecretResolver, ref string) (credential.Secret, error) {
	if err := ctx.Err(); err != nil {
		return credential.Secret{}, err
	}
	if resolver == nil || ref == "" {
		return credential.Secret{}, ErrSecretUnavailable
	}
	secret, err := resolver.ResolveSecret(ctx, ref)
	if ctx.Err() != nil {
		return credential.Secret{}, ctx.Err()
	}
	if err != nil || secret.IsEmpty() {
		return credential.Secret{}, ErrSecretUnavailable
	}
	return secret, nil
}

type storedOAuthToken struct {
	AccessToken  string    `json:"accessToken"`
	TokenType    string    `json:"tokenType"`
	RefreshToken string    `json:"refreshToken"`
	Expiry       time.Time `json:"expiry"`
	ClientID     string    `json:"clientId,omitempty"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	TokenURL     string    `json:"tokenUrl,omitempty"`
	Resource     string    `json:"resource,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
}

func decodeOAuthToken(secret credential.Secret) (*oauth2.Token, *oauth2.Config, []string, storedOAuthToken, error) {
	var stored storedOAuthToken
	err := secret.Use(func(raw []byte) error {
		if len(raw) > 256<<10 {
			return ErrSecretUnavailable
		}
		return json.Unmarshal(raw, &stored)
	})
	if err != nil || !validCredentialValue(stored.AccessToken) || (stored.RefreshToken != "" && !validCredentialValue(stored.RefreshToken)) || (stored.ClientSecret != "" && !validCredentialValue(stored.ClientSecret)) {
		return nil, nil, nil, storedOAuthToken{}, ErrSecretUnavailable
	}
	if stored.TokenType == "" {
		stored.TokenType = "Bearer"
	}
	if !strings.EqualFold(stored.TokenType, "Bearer") {
		return nil, nil, nil, storedOAuthToken{}, ErrOAuthReconnectRequired
	}
	values := []string{stored.AccessToken, stored.RefreshToken, stored.ClientSecret}
	var config *oauth2.Config
	if stored.TokenURL != "" && stored.ClientID != "" {
		config = &oauth2.Config{ClientID: stored.ClientID, ClientSecret: stored.ClientSecret, Endpoint: oauth2.Endpoint{TokenURL: stored.TokenURL}, Scopes: stored.Scopes}
	}
	return &oauth2.Token{AccessToken: stored.AccessToken, TokenType: stored.TokenType, RefreshToken: stored.RefreshToken, Expiry: stored.Expiry}, config, values, stored, nil
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

type nonInteractiveOAuth struct{ source *persistentOAuthTokenSource }

func (h *nonInteractiveOAuth) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return tokenSourceFunc(func() (*oauth2.Token, error) { return h.source.token(ctx) }), nil
}

func (*nonInteractiveOAuth) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return ErrOAuthReconnectRequired
}

type persistentOAuthTokenSource struct {
	resolver     SecretResolver
	replacer     OAuthTokenReplacer
	sessionRef   string
	client       *http.Client
	clientSecret string
	coordinator  *oauthRefreshCoordinator
	sensitive    *sensitiveValues
	lifetime     context.Context
	timeout      time.Duration
	operations   OperationRunner
	serverID     string
	closing      atomic.Bool
	tokenMu      sync.Mutex
	last         *oauth2.Token
}

func (s *persistentOAuthTokenSource) token(ctx context.Context) (*oauth2.Token, error) {
	if s.closing.Load() {
		// Teardown may use only already-resolved material, never renewal/unlock.
		s.tokenMu.Lock()
		defer s.tokenMu.Unlock()
		if s.last == nil {
			return nil, ErrOAuthReconnectRequired
		}
		return s.last, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	stop := context.AfterFunc(s.lifetime, cancel)
	defer func() { stop(); cancel() }()
	if err := s.lifetime.Err(); err != nil {
		return nil, err
	}
	if s.operations == nil {
		return nil, ErrSecretUnavailable
	}
	var token *oauth2.Token
	err := s.operations.RunServerOperation(ctx, s.serverID, func(ctx context.Context) error {
		var err error
		token, err = s.tokenRegistered(ctx)
		return err
	})
	return token, err
}

func (s *persistentOAuthTokenSource) tokenRegistered(ctx context.Context) (*oauth2.Token, error) {
	unlock, err := s.coordinator.lock(ctx, s.sessionRef)
	if err != nil {
		return nil, err
	}
	defer unlock()
	secret, err := resolveSecret(ctx, s.resolver, s.sessionRef)
	if err != nil {
		return nil, err
	}
	token, config, values, stored, err := decodeOAuthToken(secret)
	if err != nil {
		return nil, err
	}
	s.sensitive.add(values...)
	s.tokenMu.Lock()
	s.last = token
	s.tokenMu.Unlock()
	if token.Valid() {
		return token, nil
	}
	if config == nil || token.RefreshToken == "" || stored.Resource == "" {
		return nil, ErrOAuthReconnectRequired
	}
	if s.clientSecret != "" {
		config.ClientSecret = s.clientSecret
	}
	s.sensitive.add(config.ClientSecret)
	refreshClient := *s.client
	refreshClient.Transport = &oauthResourceTransport{base: s.client.Transport, resource: stored.Resource}
	clientCtx := context.WithValue(ctx, oauth2.HTTPClient, &refreshClient)
	// OAuth's TokenSource serializes one exchange, while the shared coordinator
	// serializes every session using this persisted ID and reloads before reuse.
	next, err := config.TokenSource(clientCtx, token).Token()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrOAuthReconnectRequired
	}
	if next == nil || !next.Valid() || !validCredentialValue(next.AccessToken) {
		return nil, ErrOAuthReconnectRequired
	}
	s.sensitive.add(next.AccessToken, next.RefreshToken)
	stored.AccessToken, stored.TokenType, stored.Expiry = next.AccessToken, next.TokenType, next.Expiry
	if next.RefreshToken != "" {
		stored.RefreshToken = next.RefreshToken
	}
	stored.ClientSecret = config.ClientSecret
	raw, err := json.Marshal(stored) // #nosec G117 -- system-only credential boundary, never metadata
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	err = s.replacer.ReplaceOAuthToken(ctx, s.sessionRef, credential.NewSecretBytes(raw))
	clear(raw)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSecretUnavailable
	}
	s.tokenMu.Lock()
	s.last = next
	s.tokenMu.Unlock()
	return next, nil
}

type (
	redirectedKey        struct{}
	guardedHTTPTransport struct {
		policy              *httppolicy.Transport
		endpoint            string
		endpointOrigin      string
		headers             []resolvedHeader
		maxBody             int64
		responseTimeout     time.Duration
		lifetime            context.Context
		closing             atomic.Bool
		oauthChallenge      bool
		notificationStreams bool
	}
)

func newGuardedHTTPTransport(endpoint string, headers []resolvedHeader, resultBound int, responseTimeout time.Duration) *guardedHTTPTransport {
	parsed, _ := url.Parse(endpoint)
	t := &guardedHTTPTransport{
		endpoint: endpoint, endpointOrigin: origin(parsed), headers: append([]resolvedHeader(nil), headers...),
		maxBody: int64(max(resultBound*4, 3<<20)), responseTimeout: responseTimeout, lifetime: context.Background(),
	}
	t.policy = httppolicy.NewMCPTransport(httppolicy.Params{Component: "MCP", MaxRedirects: maxHTTPRedirects}, httppolicy.MCPPolicy{Endpoint: endpoint})
	t.configure()
	return t
}

func (t *guardedHTTPTransport) configure() {
	t.policy.Inner.ForceAttemptHTTP2 = true
	t.policy.Inner.MaxIdleConns = 8
	t.policy.Inner.MaxIdleConnsPerHost = 2
	t.policy.Inner.IdleConnTimeout = 30 * time.Second
	t.policy.Inner.TLSHandshakeTimeout = 15 * time.Second
	t.policy.Inner.ResponseHeaderTimeout = t.responseTimeout
	t.policy.Inner.MaxResponseHeaderBytes = 32 << 10
}

func (t *guardedHTTPTransport) useOAuthPolicy() {
	t.close()
	t.policy = httppolicy.NewMCPTransport(httppolicy.Params{Component: "MCP OAuth", MaxRedirects: maxHTTPRedirects}, httppolicy.MCPPolicy{OAuth: true, Endpoint: t.endpoint})
	t.configure()
}
func (t *guardedHTTPTransport) close() { t.policy.Inner.CloseIdleConnections() }
func (t *guardedHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	timeout := t.responseTimeout
	teardown := request.Method == http.MethodDelete && t.closing.Load()
	if teardown {
		ctx = context.WithoutCancel(ctx)
		timeout = time.Second
	}
	var cancel context.CancelFunc
	var setupTimer *time.Timer
	notification := t.notificationStreams && request.Method == http.MethodGet &&
		strings.Contains(request.Header.Get("Accept"), "text/event-stream") &&
		(request.Body == nil || request.Body == http.NoBody)
	if notification {
		// Standalone notifications belong to the session, not the first call's
		// response duration. Bound DNS, dialing and headers only until the
		// stream is established, then retain the session cancellation owner.
		var cancelCause context.CancelCauseFunc
		ctx, cancelCause = context.WithCancelCause(ctx)
		cancel = func() { cancelCause(context.Canceled) }
		setupTimer = time.AfterFunc(timeout, func() { cancelCause(context.DeadlineExceeded) })
	} else {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	stop := func() bool { return true }
	if !teardown {
		stop = context.AfterFunc(t.lifetime, cancel)
		if t.lifetime.Err() != nil {
			cancel()
		}
	}
	cleanup := func() { stop(); cancel() }
	clone := request.Clone(ctx)
	redirected, _ := request.Context().Value(redirectedKey{}).(bool)
	if !redirected && request.Response == nil && origin(request.URL) == t.endpointOrigin {
		for _, header := range t.headers {
			clone.Header.Set(header.name, header.value)
		}
	}
	response, err := t.policy.RoundTrip(clone)
	if setupTimer != nil {
		setupTimer.Stop()
	}
	if err != nil {
		cause := context.Cause(ctx)
		cleanup()
		if errors.Is(cause, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, err
	}
	if notification && !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		bodyTimer := time.AfterFunc(timeout, cancel)
		priorCleanup := cleanup
		cleanup = func() { bodyTimer.Stop(); priorCleanup() }
	}
	if !t.oauthChallenge && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		_ = response.Body.Close()
		cleanup()
		return nil, ErrSecretUnavailable
	}
	if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		_ = response.Body.Close()
		cleanup()
		return nil, ErrServerUnavailable
	}
	if response.ContentLength > t.maxBody {
		_ = response.Body.Close()
		cleanup()
		return nil, ErrResponseTooLarge
	}
	response.Body = &boundedResponseBody{reader: response.Body, remaining: t.maxBody, cleanup: cleanup}
	return response, nil
}

func (t *guardedHTTPTransport) validateOAuthURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return ErrDestinationRefused
	}
	if err := t.policy.ValidateURL(ctx, u); err != nil {
		return preferContext(ctx, ErrDestinationRefused)
	}
	return nil
}

func (t *guardedHTTPTransport) CheckRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxHTTPRedirects {
		return ErrDestinationRefused
	}
	if len(via) == 0 {
		return nil
	}
	original := via[0]
	hasBody := func(r *http.Request) bool { return r.Body != nil && r.Body != http.NoBody || r.ContentLength > 0 }
	if original.Method != http.MethodGet && original.Method != http.MethodHead || hasBody(original) ||
		request.Method != http.MethodGet && request.Method != http.MethodHead || hasBody(request) {
		return ErrDestinationRefused
	}
	// Reset on EVERY hop. net/http may copy initial headers again after an
	// origin-return chain; the marker also prevents RoundTrip reinjection.
	accept := request.Header.Get("Accept")
	request.Header = make(http.Header)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	*request = *request.WithContext(context.WithValue(request.Context(), redirectedKey{}, true))
	return nil
}

func origin(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else if u.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

type boundedResponseBody struct {
	reader    io.ReadCloser
	remaining int64
	mu        sync.Mutex
	over      bool
	cleanup   func()
	once      sync.Once
}

func (b *boundedResponseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over {
		return 0, ErrResponseTooLarge
	}
	limit := min(int64(len(p)), b.remaining+1)
	n, err := b.reader.Read(p[:limit])
	b.remaining -= int64(n)
	if b.remaining < 0 {
		b.over = true
		return max(n-1, 0), ErrResponseTooLarge
	}
	return n, err
}

func (b *boundedResponseBody) Close() error {
	err := b.reader.Close()
	b.once.Do(func() {
		if b.cleanup != nil {
			b.cleanup()
		}
	})
	return err
}

var (
	_ auth.OAuthHandler = (*nonInteractiveOAuth)(nil)
	_ http.RoundTripper = (*guardedHTTPTransport)(nil)
)

// oauth2 does not carry resource indicators into refresh requests itself.
type oauthResourceTransport struct {
	base     http.RoundTripper
	resource string
}

func (t *oauthResourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.Body == nil || !strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return t.base.RoundTrip(request)
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, (128<<10)+1))
	_ = request.Body.Close()
	if err != nil || len(raw) > 128<<10 {
		return nil, ErrOAuthReconnectRequired
	}
	values, err := url.ParseQuery(string(raw))
	clear(raw)
	if err != nil {
		return nil, ErrOAuthReconnectRequired
	}
	if values.Get("grant_type") == "refresh_token" {
		values.Set("resource", t.resource)
	}
	body := values.Encode()
	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(strings.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = nil
	return t.base.RoundTrip(clone)
}
