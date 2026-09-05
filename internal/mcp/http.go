package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/httppolicy"
	"golang.org/x/oauth2"
)

const maxHTTPRedirects = 5

type resolvedHeader struct {
	name  string
	value string
}

type httpSessionConfig struct {
	transport    *sdk.StreamableClientTransport
	cleanup      func()
	sensitive    []string
	sensitiveRef *[]string
}

type oauthRefreshCoordinator struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newOAuthRefreshCoordinator() *oauthRefreshCoordinator {
	return &oauthRefreshCoordinator{locks: make(map[string]*sync.Mutex)}
}

func (c *oauthRefreshCoordinator) lock(ref string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lock := c.locks[ref]; lock != nil {
		return lock
	}
	lock := new(sync.Mutex)
	c.locks[ref] = lock
	return lock
}

func buildHTTPTransport(ctx context.Context, activation Activation, resolver SecretResolver, coordinators ...*oauthRefreshCoordinator) (httpSessionConfig, error) {
	cfg := activation.HTTP
	if cfg == nil {
		return httpSessionConfig{}, fmt.Errorf("%w: HTTP configuration is missing", ErrInvalidActivation)
	}
	headers, sensitive, err := resolveHeaders(ctx, cfg.Headers, resolver)
	if err != nil {
		return httpSessionConfig{}, err
	}
	switch cfg.Auth {
	case HTTPAuthNone:
		if cfg.BearerRef != "" || cfg.OAuthSessionRef != "" {
			return httpSessionConfig{}, fmt.Errorf("%w: unexpected HTTP credential", ErrInvalidActivation)
		}
	case HTTPAuthBearer:
		secret, err := resolveSecret(ctx, resolver, cfg.BearerRef)
		if err != nil {
			return httpSessionConfig{}, err
		}
		var token string
		if err := secret.Use(func(value []byte) error {
			token = string(value)
			return nil
		}); err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
			return httpSessionConfig{}, ErrSecretUnavailable
		}
		headers = append(headers, resolvedHeader{name: "Authorization", value: "Bearer " + token})
		sensitive = append(sensitive, token)
	case HTTPAuthOAuth:
		secret, err := resolveSecret(ctx, resolver, cfg.OAuthSessionRef)
		if err != nil {
			return httpSessionConfig{}, err
		}
		token, tokenConfig, values, stored, err := decodeOAuthToken(secret)
		if err != nil {
			return httpSessionConfig{}, err
		}
		sensitive = append(sensitive, values...)
		clientSecret := stored.ClientSecret
		if clientSecret != "" {
			sensitive = append(sensitive, clientSecret)
		}
		if cfg.OAuthClientSecretRef != "" {
			clientSecretSecret, err := resolveSecret(ctx, resolver, cfg.OAuthClientSecretRef)
			if err != nil {
				return httpSessionConfig{}, err
			}
			if err := clientSecretSecret.Use(func(value []byte) error {
				clientSecret = string(value)
				return nil
			}); err != nil || clientSecret == "" || strings.ContainsAny(clientSecret, "\r\n") {
				return httpSessionConfig{}, ErrSecretUnavailable
			}
			sensitive = append(sensitive, clientSecret)
		}
		if tokenConfig != nil {
			tokenConfig.ClientSecret = clientSecret
		}
		guard := newGuardedHTTPTransport(cfg.Endpoint, headers, activation.Limits.MaxResultBytes, max(activation.Limits.StartupTimeout, activation.Limits.CallTimeout))
		client := &http.Client{Transport: guard}
		client.CheckRedirect = guard.CheckRedirect
		source := oauth2.StaticTokenSource(token)
		if tokenConfig != nil && token.RefreshToken != "" {
			refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, client)
			source = tokenConfig.TokenSource(refreshCtx, token)
		}
		coordinator := newOAuthRefreshCoordinator()
		if len(coordinators) > 0 && coordinators[0] != nil {
			coordinator = coordinators[0]
		}
		if replacer, ok := resolver.(OAuthTokenReplacer); ok {
			source = &persistentOAuthTokenSource{
				source:       source,
				replacer:     replacer,
				resolver:     resolver,
				ctx:          ctx,
				sessionRef:   cfg.OAuthSessionRef,
				template:     stored,
				last:         cloneOAuthToken(token),
				config:       tokenConfig,
				client:       client,
				clientSecret: clientSecret,
				coordinator:  coordinator,
				sensitive:    &sensitive,
			}
		}
		return httpSessionConfig{
			transport: &sdk.StreamableClientTransport{
				Endpoint:     cfg.Endpoint,
				HTTPClient:   client,
				MaxRetries:   -1,
				OAuthHandler: &nonInteractiveOAuth{source: source},
			},
			cleanup:      guard.inner.CloseIdleConnections,
			sensitive:    sensitive,
			sensitiveRef: &sensitive,
		}, nil
	default:
		return httpSessionConfig{}, fmt.Errorf("%w: unknown HTTP authentication", ErrInvalidActivation)
	}
	guard := newGuardedHTTPTransport(cfg.Endpoint, headers, activation.Limits.MaxResultBytes, max(activation.Limits.StartupTimeout, activation.Limits.CallTimeout))
	client := &http.Client{Transport: guard}
	client.CheckRedirect = guard.CheckRedirect
	return httpSessionConfig{
		transport: &sdk.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: client,
			MaxRetries: -1,
		},
		cleanup:      guard.inner.CloseIdleConnections,
		sensitive:    sensitive,
		sensitiveRef: &sensitive,
	}, nil
}

func resolveHeaders(ctx context.Context, bindings []Binding, resolver SecretResolver) ([]resolvedHeader, []string, error) {
	headers := make([]resolvedHeader, 0, len(bindings))
	sensitive := make([]string, 0, len(bindings))
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
			if err := secret.Use(func(material []byte) error {
				value = string(material)
				return nil
			}); err != nil {
				return nil, nil, ErrSecretUnavailable
			}
		default:
			return nil, nil, fmt.Errorf("%w: invalid header binding", ErrInvalidActivation)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, nil, fmt.Errorf("%w: invalid header value", ErrInvalidActivation)
		}
		headers = append(headers, resolvedHeader{name: http.CanonicalHeaderKey(binding.Name), value: value})
		sensitive = append(sensitive, value)
	}
	return headers, sensitive, nil
}

func resolveSecret(ctx context.Context, resolver SecretResolver, ref string) (credential.Secret, error) {
	if resolver == nil || ref == "" {
		return credential.Secret{}, ErrSecretUnavailable
	}
	secret, err := resolver.ResolveSecret(ctx, ref)
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
	Scopes       []string  `json:"scopes,omitempty"`
}

func decodeOAuthToken(secret credential.Secret) (*oauth2.Token, *oauth2.Config, []string, storedOAuthToken, error) {
	var raw []byte
	if err := secret.Use(func(value []byte) error {
		raw = append(raw, value...)
		return nil
	}); err != nil {
		return nil, nil, nil, storedOAuthToken{}, ErrSecretUnavailable
	}
	defer clear(raw)
	var stored storedOAuthToken
	if err := json.Unmarshal(raw, &stored); err != nil || stored.AccessToken == "" {
		return nil, nil, nil, storedOAuthToken{}, ErrSecretUnavailable
	}
	if stored.TokenType == "" {
		stored.TokenType = "Bearer"
	}
	values := []string{stored.AccessToken}
	if stored.RefreshToken != "" {
		values = append(values, stored.RefreshToken)
	}
	var config *oauth2.Config
	if stored.TokenURL != "" && stored.ClientID != "" {
		config = &oauth2.Config{
			ClientID:     stored.ClientID,
			ClientSecret: stored.ClientSecret,
			Endpoint:     oauth2.Endpoint{TokenURL: stored.TokenURL},
			Scopes:       append([]string(nil), stored.Scopes...),
		}
	}
	return &oauth2.Token{
		AccessToken:  stored.AccessToken,
		TokenType:    stored.TokenType,
		RefreshToken: stored.RefreshToken,
		Expiry:       stored.Expiry,
	}, config, values, stored, nil
}

type nonInteractiveOAuth struct {
	source oauth2.TokenSource
}

func (h *nonInteractiveOAuth) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h.source, nil
}

func (*nonInteractiveOAuth) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return ErrOAuthReconnectRequired
}

type persistentOAuthTokenSource struct {
	mu           sync.Mutex
	source       oauth2.TokenSource
	replacer     OAuthTokenReplacer
	resolver     SecretResolver
	ctx          context.Context
	sessionRef   string
	template     storedOAuthToken
	last         *oauth2.Token
	config       *oauth2.Config
	client       *http.Client
	clientSecret string
	coordinator  *oauthRefreshCoordinator
	sensitive    *[]string
}

func (s *persistentOAuthTokenSource) Token() (*oauth2.Token, error) {
	lock := s.coordinator.lock(s.sessionRef)
	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLatest(); err != nil {
		return nil, err
	}
	if s.last != nil && s.last.Valid() {
		return cloneOAuthToken(s.last), nil
	}
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if s.last != nil &&
		s.last.AccessToken == token.AccessToken &&
		s.last.TokenType == token.TokenType &&
		s.last.RefreshToken == token.RefreshToken &&
		s.last.Expiry.Equal(token.Expiry) {
		return token, nil
	}
	next := s.template
	next.AccessToken = token.AccessToken
	next.TokenType = token.TokenType
	next.RefreshToken = token.RefreshToken
	next.Expiry = token.Expiry
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, errors.New("MCP OAuth token could not be encoded")
	}
	err = s.replacer.ReplaceOAuthToken(s.ctx, s.sessionRef, credential.NewSecretBytes(raw))
	clear(raw)
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	s.template = next
	s.last = cloneOAuthToken(token)
	s.addSensitive(token.AccessToken)
	s.addSensitive(token.RefreshToken)
	return token, nil
}

func (s *persistentOAuthTokenSource) refreshLatest() error {
	if s.resolver == nil || s.sessionRef == "" {
		return nil
	}
	secret, err := s.resolver.ResolveSecret(s.ctx, s.sessionRef)
	if err != nil {
		return ErrSecretUnavailable
	}
	token, config, values, stored, err := decodeOAuthToken(secret)
	if err != nil {
		return err
	}
	if config != nil {
		config.ClientSecret = s.clientSecret
	}
	if s.last != nil &&
		s.last.AccessToken == token.AccessToken &&
		s.last.RefreshToken == token.RefreshToken &&
		s.last.Expiry.Equal(token.Expiry) {
		return nil
	}
	s.template = stored
	s.last = cloneOAuthToken(token)
	s.config = config
	if config != nil && token.RefreshToken != "" {
		refreshCtx := context.WithValue(s.ctx, oauth2.HTTPClient, s.client)
		s.source = config.TokenSource(refreshCtx, token)
	} else {
		s.source = oauth2.StaticTokenSource(token)
	}
	for _, value := range values {
		s.addSensitive(value)
	}
	s.addSensitive(s.clientSecret)
	return nil
}

func (s *persistentOAuthTokenSource) addSensitive(value string) {
	if value == "" || s.sensitive == nil {
		return
	}
	for _, existing := range *s.sensitive {
		if existing == value {
			return
		}
	}
	*s.sensitive = append(*s.sensitive, value)
}

func cloneOAuthToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	copyToken := *token
	return &copyToken
}

var _ auth.OAuthHandler = (*nonInteractiveOAuth)(nil)

type dialTarget struct {
	host string
	port string
	ips  []net.IP
}

type dialTargetKey struct{}

type guardedHTTPTransport struct {
	inner          *http.Transport
	endpointOrigin string
	headers        []resolvedHeader
	headerNames    []string
	maxBody        int64
	resolver       *net.Resolver
	dialer         *net.Dialer
}

func newGuardedHTTPTransport(endpoint string, headers []resolvedHeader, resultBound int, responseTimeout time.Duration) *guardedHTTPTransport {
	parsed, _ := url.Parse(endpoint)
	t := &guardedHTTPTransport{
		endpointOrigin: origin(parsed),
		headers:        append([]resolvedHeader(nil), headers...),
		maxBody:        int64(max(resultBound*4, 3<<20)),
		resolver:       net.DefaultResolver,
		dialer:         &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
	}
	for _, header := range headers {
		t.headerNames = append(t.headerNames, header.name)
	}
	t.inner = &http.Transport{
		Proxy:                 nil,
		DialContext:           t.dialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: responseTimeout,
	}
	return t
}

func (t *guardedHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := t.resolve(request.Context(), request.URL)
	if err != nil {
		return nil, err
	}
	clone := request.Clone(context.WithValue(request.Context(), dialTargetKey{}, target))
	clone.Header = request.Header.Clone()
	if origin(request.URL) == t.endpointOrigin {
		for _, header := range t.headers {
			clone.Header.Set(header.name, header.value)
		}
		clone = clone.WithContext(httppolicy.WithCustomHeaderNames(clone.Context(), t.headerNames))
	}
	response, err := t.inner.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > t.maxBody {
		_ = response.Body.Close()
		return nil, ErrResponseTooLarge
	}
	response.Body = &boundedResponseBody{reader: response.Body, remaining: t.maxBody}
	return response, nil
}

func (t *guardedHTTPTransport) resolve(ctx context.Context, target *url.URL) (dialTarget, error) {
	if target == nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return dialTarget{}, ErrDestinationRefused
	}
	host := target.Hostname()
	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		addresses, err := t.resolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return dialTarget{}, fmt.Errorf("%w: destination cannot be resolved", ErrDestinationRefused)
		}
		ips = make([]net.IP, 0, len(addresses))
		for _, address := range addresses {
			ips = append(ips, address.IP)
		}
	}
	for _, ip := range ips {
		if dangerousIP(ip) {
			return dialTarget{}, fmt.Errorf("%w: prohibited address class", ErrDestinationRefused)
		}
		if target.Scheme == "http" && !(ip.IsLoopback() || ip.IsPrivate()) {
			return dialTarget{}, fmt.Errorf("%w: plaintext HTTP requires loopback or private addressing", ErrDestinationRefused)
		}
	}
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return dialTarget{host: host, port: port, ips: ips}, nil
}

func dangerousIP(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func (t *guardedHTTPTransport) dialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	target, ok := ctx.Value(dialTargetKey{}).(dialTarget)
	if !ok || len(target.ips) == 0 {
		return nil, ErrDestinationRefused
	}
	var lastErr error
	for _, ip := range target.ips {
		connection, err := t.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), target.port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrDestinationRefused
	}
	return nil, lastErr
}

func (t *guardedHTTPTransport) CheckRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxHTTPRedirects {
		return errors.New("MCP HTTP redirect limit reached")
	}
	if len(via) > 0 && origin(request.URL) != origin(via[0].URL) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead || request.Body != nil {
			return errors.New("MCP HTTP cross-origin redirect refused")
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		request.Header.Del("Cookie2")
		for _, name := range t.headerNames {
			request.Header.Del(name)
		}
	}
	return nil
}

func origin(target *url.URL) string {
	if target == nil {
		return ""
	}
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else if target.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(target.Scheme) + "://" + strings.ToLower(target.Hostname()) + ":" + port
}

type boundedResponseBody struct {
	reader    io.ReadCloser
	remaining int64
	mu        sync.Mutex
	over      bool
}

func (b *boundedResponseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over {
		return 0, ErrResponseTooLarge
	}
	if b.remaining < 0 {
		b.over = true
		return 0, ErrResponseTooLarge
	}
	limit := int64(len(p))
	if limit > b.remaining+1 {
		limit = b.remaining + 1
	}
	n, err := b.reader.Read(p[:limit])
	b.remaining -= int64(n)
	if b.remaining < 0 {
		b.over = true
		if n > 0 {
			n--
		}
		return n, ErrResponseTooLarge
	}
	return n, err
}

func (b *boundedResponseBody) Close() error { return b.reader.Close() }

var _ http.RoundTripper = (*guardedHTTPTransport)(nil)
