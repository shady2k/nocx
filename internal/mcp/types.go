// Package mcp owns on-demand MCP client sessions. It deliberately accepts
// immutable activation snapshots rather than a profile repository or vault.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

const (
	maxArgumentsBytes = 64 << 10
	maxFrameBytes     = 4 << 20
	maxSessionBytes   = 64 << 20
	maxResultEntries  = 64
)

var (
	ErrClosed                   = errors.New("MCP runtime is closed")
	ErrInvalidActivation        = errors.New("invalid MCP activation")
	ErrActivationChanged        = errors.New("MCP activation changed")
	ErrCatalogStale             = errors.New("MCP catalog is stale; refresh tools")
	ErrDestinationRefused       = errors.New("MCP destination refused")
	ErrSecretUnavailable        = errors.New("MCP credential is unavailable")
	ErrOAuthReconnectRequired   = errors.New("MCP OAuth connection requires reconnecting in Settings")
	ErrOAuthAuthorizationFailed = errors.New("MCP OAuth authorization failed")
	ErrResponseTooLarge         = errors.New("MCP HTTP response exceeds its bound")
	ErrFrameTooLarge            = errors.New("MCP stdio frame exceeds its bound")
)

// SecretResolver is the only runtime-facing credential boundary. It returns
// the opaque credential.Secret capability; callers never receive vault rows or
// plaintext through this interface.
type SecretResolver interface {
	ResolveSecret(context.Context, string) (credential.Secret, error)
}

// Runtime is the only execution surface exposed to the assistant and control
// plane. Construction and close methods never activate a server.
type Runtime interface {
	Refresh(context.Context, Activation) (Catalog, error)
	Invoke(context.Context, Invocation) (Result, error)
	CloseRun(runID string)
	CloseServer(serverID string)
	Close() error
}

// OAuthTokenReplacer persists renewal under the existing system-only secret ID.
// OAuth activations fail closed if this boundary is unavailable.
type OAuthTokenReplacer interface {
	ReplaceOAuthToken(context.Context, string, credential.Secret) error
}

// OAuthService is an explicit management boundary. Runtime calls never invoke
// it, and in particular a 401/403 from an assistant call never opens a browser.
type OAuthService interface {
	Authorize(context.Context, Activation, URLPresenter) (OAuthStatus, error)
	Forget(context.Context, string) error
}

// OAuthSessionDiscarder deletes only the fresh session captured by a losing CAS.
// It is independent of process-local state and remains correct after restart.
type OAuthSessionDiscarder interface {
	DiscardOAuthSession(context.Context, string) error
}

type URLPresenter interface {
	PresentURL(context.Context, string) error
}

type OAuthStatus struct {
	Connected  bool
	Issuer     string
	Scopes     []string
	ExpiresAt  *time.Time
	SessionRef string
}
type TransportKind string

const (
	TransportStdio          TransportKind = "stdio"
	TransportStreamableHTTP TransportKind = "streamable-http"
)

type Binding struct {
	Name      string
	Literal   *string
	SecretRef string
}

type StdioConfig struct {
	Command string
	Argv    []string
	Cwd     string
	Env     []Binding
}

type HTTPAuthKind string

const (
	HTTPAuthNone   HTTPAuthKind = "none"
	HTTPAuthBearer HTTPAuthKind = "bearer"
	HTTPAuthOAuth  HTTPAuthKind = "oauth"
)

type HTTPConfig struct {
	Endpoint             string
	Headers              []Binding
	Auth                 HTTPAuthKind
	BearerRef            string
	OAuthSessionRef      string
	OAuthRegistration    profile.MCPOAuthRegistrationKind
	OAuthClientID        string
	OAuthClientSecretRef string
	OAuthScopes          []string
}

type Limits struct {
	StartupTimeout time.Duration
	CallTimeout    time.Duration
	IdleTimeout    time.Duration
	MaxResultBytes int
}

type ToolDescriptor struct {
	Name             string
	Description      string
	InputSchema      json.RawMessage
	OutputSchema     json.RawMessage
	DescriptorDigest string
}

// Activation is an immutable server snapshot. Callers should obtain it with
// ActivationFromServer; Manager validates it again before every activation.
type Activation struct {
	ServerID       string
	ServerRevision uint64
	Name           string
	Enabled        bool
	Transport      TransportKind
	Stdio          *StdioConfig
	HTTP           *HTTPConfig
	Limits         Limits
	CatalogDigest  string
	Tools          []ToolDescriptor
}

// Clone makes an independent snapshot, including schemas and literal bindings.
func (a Activation) Clone() Activation {
	out := a
	out.Tools = make([]ToolDescriptor, len(a.Tools))
	for i, tool := range a.Tools {
		out.Tools[i] = tool
		out.Tools[i].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		out.Tools[i].OutputSchema = append(json.RawMessage(nil), tool.OutputSchema...)
	}
	if a.Stdio != nil {
		copyConfig := *a.Stdio
		copyConfig.Argv = append([]string(nil), a.Stdio.Argv...)
		copyConfig.Env = cloneBindings(a.Stdio.Env)
		out.Stdio = &copyConfig
	}
	if a.HTTP != nil {
		copyConfig := *a.HTTP
		copyConfig.Headers = cloneBindings(a.HTTP.Headers)
		copyConfig.OAuthScopes = append([]string(nil), a.HTTP.OAuthScopes...)
		out.HTTP = &copyConfig
	}
	return out
}

func cloneBindings(bindings []Binding) []Binding {
	out := make([]Binding, len(bindings))
	for i, binding := range bindings {
		out[i] = binding
		if binding.Literal != nil {
			out[i].Literal = new(*binding.Literal)
		}
	}
	return out
}

// Invocation names exactly one remote tool in one assistant run.
type Invocation struct {
	RunID            string
	Activation       Activation
	RemoteTool       string
	DescriptorDigest string
	Arguments        json.RawMessage
}

type Catalog struct {
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
	RefreshedAt     time.Time
	Tools           []ToolDescriptor
	Digest          string
}

type Resource struct {
	URI      string `json:"uri"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

type Omitted struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Reason   string `json:"reason"`
}

type Result struct {
	ServerID          string          `json:"serverId"`
	Tool              string          `json:"tool"`
	IsError           bool            `json:"isError"`
	Text              []string        `json:"text"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	Resources         []Resource      `json:"resources"`
	Omitted           []Omitted       `json:"omitted"`
}

func ActivationFromServer(server profile.MCPServer) (Activation, error) {
	if err := profile.ValidateMCPServer(server); err != nil {
		return Activation{}, fmt.Errorf("%w: server configuration", ErrInvalidActivation)
	}
	a := Activation{
		ServerID:       server.ID,
		ServerRevision: server.Revision,
		Name:           server.Name,
		Enabled:        server.Enabled,
		Transport:      TransportKind(server.Transport),
		Limits: Limits{
			StartupTimeout: server.Limits.StartupTimeout(),
			CallTimeout:    server.Limits.CallTimeout(),
			IdleTimeout:    server.Limits.IdleTimeout(),
			MaxResultBytes: server.Limits.MaxResultBytes,
		},
		CatalogDigest: server.Catalog.Digest,
		Tools:         make([]ToolDescriptor, len(server.Catalog.Tools)),
	}
	for i, tool := range server.Catalog.Tools {
		canonical, err := profile.CanonicalMCPToolDescriptor(tool)
		if err != nil {
			return Activation{}, ErrSchema
		}
		a.Tools[i] = ToolDescriptor{
			Name:             tool.Name,
			Description:      tool.Description,
			InputSchema:      canonical.InputSchema,
			OutputSchema:     canonical.OutputSchema,
			DescriptorDigest: canonical.DescriptorDigest,
		}
	}
	if server.Stdio != nil {
		a.Stdio = &StdioConfig{Command: server.Stdio.Command, Argv: append([]string(nil), server.Stdio.Argv...), Cwd: server.Stdio.Cwd}
		a.Stdio.Env = make([]Binding, len(server.Stdio.Env))
		for i, row := range server.Stdio.Env {
			a.Stdio.Env[i] = bindingFromProfile(row.Name, row.Value)
		}
	}
	if server.HTTP != nil {
		endpoint, err := profile.CanonicalMCPHTTPDestination(server.HTTP.Endpoint)
		if err != nil {
			return Activation{}, fmt.Errorf("%w: HTTP endpoint", ErrInvalidActivation)
		}
		a.HTTP = &HTTPConfig{Endpoint: endpoint, Auth: HTTPAuthKind(server.HTTP.Auth), Headers: make([]Binding, len(server.HTTP.Headers))}
		if server.HTTP.OAuth != nil {
			a.HTTP.OAuthRegistration = server.HTTP.OAuth.Registration
			a.HTTP.OAuthClientID = server.HTTP.OAuth.ClientID
			a.HTTP.OAuthScopes = append([]string(nil), server.HTTP.OAuth.Scopes...)
			if server.HTTP.OAuth.ClientSecret != nil {
				a.HTTP.OAuthClientSecretRef = server.HTTP.OAuth.ClientSecret.SecretRef
			}
		}
		for i, row := range server.HTTP.Headers {
			a.HTTP.Headers[i] = bindingFromProfile(http.CanonicalHeaderKey(row.Name), row.Value)
		}
		if server.HTTP.Bearer != nil {
			a.HTTP.BearerRef = server.HTTP.Bearer.SecretRef
		}
		if server.HTTP.OAuth != nil && server.HTTP.OAuth.SessionRef != nil {
			a.HTTP.OAuthSessionRef = server.HTTP.OAuth.SessionRef.SecretRef
		}
	}
	return a, nil
}

func bindingFromProfile(name string, value profile.MCPValueBinding) Binding {
	b := Binding{Name: name, SecretRef: value.SecretRef}
	if value.Literal != nil {
		literal := *value.Literal
		b.Literal = &literal
	}
	return b
}

func (a Activation) validate(forInvoke bool) error {
	if a.ServerID == "" || a.ServerRevision == 0 {
		return fmt.Errorf("%w: server identity is incomplete", ErrInvalidActivation)
	}
	if forInvoke && !a.Enabled {
		return fmt.Errorf("%w: server is disabled", ErrInvalidActivation)
	}
	if a.Limits.StartupTimeout < 100*time.Millisecond || a.Limits.StartupTimeout > 120*time.Second || a.Limits.CallTimeout < 100*time.Millisecond || a.Limits.CallTimeout > 300*time.Second || a.Limits.IdleTimeout < 0 || a.Limits.IdleTimeout > 120*time.Second || a.Limits.MaxResultBytes < 1024 || a.Limits.MaxResultBytes > profile.MaxMCPResultBytes {
		return fmt.Errorf("%w: invalid lifecycle limits", ErrInvalidActivation)
	}
	switch a.Transport {
	case TransportStdio:
		if a.Stdio == nil || a.HTTP != nil || a.Stdio.Command == "" {
			return fmt.Errorf("%w: invalid stdio transport", ErrInvalidActivation)
		}
	case TransportStreamableHTTP:
		if a.HTTP == nil || a.Stdio != nil {
			return fmt.Errorf("%w: invalid HTTP transport", ErrInvalidActivation)
		}
		canonical, err := profile.CanonicalMCPHTTPDestination(a.HTTP.Endpoint)
		if err != nil || canonical != a.HTTP.Endpoint {
			return fmt.Errorf("%w: invalid HTTP endpoint", ErrInvalidActivation)
		}
	default:
		return fmt.Errorf("%w: unknown transport", ErrInvalidActivation)
	}
	if err := profile.ValidateMCPServer(a.profileConfig()); err != nil {
		return ErrInvalidActivation
	}
	if forInvoke && (a.CatalogDigest == "" || len(a.Tools) == 0) {
		return ErrCatalogStale
	}
	return nil
}

func (a Activation) identity() string {
	encoded, _ := json.Marshal(a)
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:])
}

// SameIdentity reports whether two immutable activations describe the exact
// same server revision, transport, credentials, catalog and tool descriptors.
func (a Activation) SameIdentity(other Activation) bool {
	return a.identity() == other.identity()
}

func (c Catalog) ProfileCatalog() (profile.MCPCatalog, error) {
	tools := make([]profile.MCPTool, len(c.Tools))
	for i, tool := range c.Tools {
		tools[i] = profile.MCPTool{
			Name:             tool.Name,
			Description:      tool.Description,
			InputSchema:      append(json.RawMessage(nil), tool.InputSchema...),
			OutputSchema:     append(json.RawMessage(nil), tool.OutputSchema...),
			DescriptorDigest: tool.DescriptorDigest,
			Enabled:          false,
			Status:           profile.MCPToolNew,
		}
	}
	if c.RefreshedAt.IsZero() || c.Digest == "" {
		return profile.MCPCatalog{}, errors.New("MCP catalog is incomplete")
	}
	return profile.MCPCatalog{
		State:           profile.MCPCatalogFresh,
		ServerName:      c.ServerName,
		ServerVersion:   c.ServerVersion,
		ProtocolVersion: c.ProtocolVersion,
		RefreshedAt:     new(c.RefreshedAt),
		Digest:          c.Digest,
		Tools:           tools,
	}, nil
}

func makeCatalog(serverName, serverVersion, protocolVersion string, tools []ToolDescriptor) (Catalog, error) {
	if len(tools) > profile.MaxMCPToolsPerServer {
		return Catalog{}, fmt.Errorf("MCP server exposes more than %d tools", profile.MaxMCPToolsPerServer)
	}
	seen := make(map[string]struct{}, len(tools))
	for i := range tools {
		if strings.TrimSpace(tools[i].Name) == "" || len(tools[i].Name) > 512 || !utf8.ValidString(tools[i].Name) || profile.HasControlChars(tools[i].Name) {
			return Catalog{}, errors.New("MCP server exposed an invalid tool name")
		}
		if _, ok := seen[tools[i].Name]; ok {
			return Catalog{}, errors.New("MCP server exposed duplicate tool names")
		}
		seen[tools[i].Name] = struct{}{}
		canonical, err := profile.CanonicalMCPToolDescriptor(profile.MCPTool{
			Name: tools[i].Name, Description: tools[i].Description,
			InputSchema: tools[i].InputSchema, OutputSchema: tools[i].OutputSchema,
		})
		if err != nil {
			return Catalog{}, ErrSchema
		}
		tools[i] = ToolDescriptor{
			Name: canonical.Name, Description: canonical.Description,
			InputSchema: canonical.InputSchema, OutputSchema: canonical.OutputSchema,
			DescriptorDigest: canonical.DescriptorDigest,
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	h := sha256.New()
	for _, tool := range tools {
		_, _ = h.Write([]byte(tool.DescriptorDigest))
		_, _ = h.Write([]byte{0})
	}
	catalog := Catalog{
		ServerName:      clipUTF8(serverName, profile.MaxMCPNameBytes),
		ServerVersion:   clipUTF8(serverVersion, profile.MaxMCPNameBytes),
		ProtocolVersion: clipUTF8(protocolVersion, 64),
		RefreshedAt:     time.Now().UTC(),
		Tools:           tools,
		Digest:          hex.EncodeToString(h.Sum(nil)),
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) > profile.MaxMCPCatalogBytes {
		return Catalog{}, fmt.Errorf("MCP catalog exceeds %d bytes", profile.MaxMCPCatalogBytes)
	}
	return catalog, nil
}

func clipUTF8(value string, max int) string {
	if len(value) <= max && utf8.ValidString(value) {
		return value
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// profileConfig delegates configuration validation to its existing owner.
// Catalog validation happens separately, before invocation and discovery commit.
func (a Activation) profileConfig() profile.MCPServer {
	record := profile.MCPServer{
		ID: a.ServerID, Revision: a.ServerRevision, Name: a.Name, Enabled: a.Enabled,
		Transport: profile.MCPTransportKind(a.Transport),
		Limits: profile.MCPLimits{
			StartupTimeoutMS: int(a.Limits.StartupTimeout / time.Millisecond),
			CallTimeoutMS:    int(a.Limits.CallTimeout / time.Millisecond),
			IdleTimeoutMS:    int(a.Limits.IdleTimeout / time.Millisecond),
			MaxResultBytes:   a.Limits.MaxResultBytes,
		},
	}
	value := func(b Binding) profile.MCPValueBinding {
		kind := profile.MCPBindingSecret
		if b.Literal != nil {
			kind = profile.MCPBindingLiteral
		}
		return profile.MCPValueBinding{Kind: kind, Literal: b.Literal, SecretRef: b.SecretRef}
	}
	secret := func(ref string) *profile.MCPSecretBinding {
		if ref == "" {
			return nil
		}
		return &profile.MCPSecretBinding{SecretRef: ref}
	}
	if a.Stdio != nil {
		record.Stdio = &profile.MCPStdioConfig{Command: a.Stdio.Command, Argv: a.Stdio.Argv, Cwd: a.Stdio.Cwd}
		for _, b := range a.Stdio.Env {
			record.Stdio.Env = append(record.Stdio.Env, profile.MCPEnvBinding{Name: b.Name, Value: value(b)})
		}
	}
	if a.HTTP != nil {
		h := a.HTTP
		record.HTTP = &profile.MCPHTTPConfig{Endpoint: h.Endpoint, Auth: profile.MCPHTTPAuthKind(h.Auth), Bearer: secret(h.BearerRef)}
		for _, b := range h.Headers {
			record.HTTP.Headers = append(record.HTTP.Headers, profile.MCPHeaderBinding{Name: b.Name, Value: value(b)})
		}
		if h.Auth == HTTPAuthOAuth || h.OAuthRegistration != "" || h.OAuthSessionRef != "" || h.OAuthClientID != "" || h.OAuthClientSecretRef != "" || len(h.OAuthScopes) != 0 {
			record.HTTP.OAuth = &profile.MCPOAuthConfig{
				Registration: h.OAuthRegistration, ClientID: h.OAuthClientID,
				ClientSecret: secret(h.OAuthClientSecretRef), SessionRef: secret(h.OAuthSessionRef),
				Scopes: h.OAuthScopes, Status: profile.MCPOAuthMissing,
			}
			if record.HTTP.OAuth.SessionRef != nil {
				record.HTTP.OAuth.SessionRef.Owned = true
				record.HTTP.OAuth.Status = profile.MCPOAuthConnected
			}
		}
	}
	return record
}
