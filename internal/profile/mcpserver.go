package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	MaxMCPServers              = 64
	MaxMCPToolsPerServer       = 256
	MaxMCPCatalogBytes         = 2 << 20
	MaxMCPSchemaBytes          = 32 << 10
	MaxMCPDescriptionBytes     = 2 << 10
	MaxMCPBindingRows          = 128
	MaxMCPBindingLiteralBytes  = 8 << 10
	MaxMCPCommandBytes         = 4 << 10
	MaxMCPNameBytes            = 128
	MaxMCPResultBytes          = 256 << 10
	MaxMCPStartupTimeoutMillis = 120_000
	MaxMCPCallTimeoutMillis    = 300_000
	MaxMCPIdleTimeoutMillis    = 120_000
)

// MCPTransportKind is the closed set of transports nocx can activate.
type MCPTransportKind string

const (
	MCPTransportStdio          MCPTransportKind = "stdio"
	MCPTransportStreamableHTTP MCPTransportKind = "streamable-http"
)

// MCPHTTPAuthKind is the closed set of HTTP credential mechanisms.
type MCPHTTPAuthKind string

const (
	MCPHTTPAuthNone   MCPHTTPAuthKind = "none"
	MCPHTTPAuthBearer MCPHTTPAuthKind = "bearer"
	MCPHTTPAuthOAuth  MCPHTTPAuthKind = "oauth"
)

type MCPBindingKind string

const (
	MCPBindingLiteral MCPBindingKind = "literal"
	MCPBindingSecret  MCPBindingKind = "secret"
)

// MCPValueBinding stores either a bounded, non-secret literal or an opaque
// vault reference. Secret material is never a field of this type. Owned says
// whether deleting the server may delete the referenced material; selected
// existing Vault rows are shared and therefore have Owned=false.
type MCPValueBinding struct {
	Kind      MCPBindingKind `json:"kind"`
	Literal   *string        `json:"literal,omitempty"`
	SecretRef string         `json:"secretRef,omitempty"`
	Owned     bool           `json:"owned,omitempty"`
}

// MCPSecretBinding is used where a literal is never legal (bearer tokens,
// OAuth client secrets, and the system-owned OAuth token session).
type MCPSecretBinding struct {
	SecretRef string `json:"secretRef"`
	Owned     bool   `json:"owned,omitempty"`
}

type MCPEnvBinding struct {
	Name  string          `json:"name"`
	Value MCPValueBinding `json:"value"`
}

type MCPHeaderBinding struct {
	Name  string          `json:"name"`
	Value MCPValueBinding `json:"value"`
}

type MCPStdioConfig struct {
	Command string          `json:"command"`
	Argv    []string        `json:"argv"`
	Cwd     string          `json:"cwd,omitempty"`
	Env     []MCPEnvBinding `json:"env"`
}

type MCPOAuthRegistrationKind string

const (
	MCPOAuthDynamic       MCPOAuthRegistrationKind = "dynamic"
	MCPOAuthPreregistered MCPOAuthRegistrationKind = "preregistered"
)

type MCPOAuthStatusKind string

const (
	MCPOAuthMissing   MCPOAuthStatusKind = "missing"
	MCPOAuthConnected MCPOAuthStatusKind = "connected"
	MCPOAuthExpired   MCPOAuthStatusKind = "expired"
)

// MCPOAuthConfig contains public registration/status metadata and opaque
// references only. SessionRef points at the single system-owned secret that
// contains tokens (and a dynamic client secret, if any).
type MCPOAuthConfig struct {
	Registration       MCPOAuthRegistrationKind `json:"registration"`
	ClientID           string                   `json:"clientId,omitempty"`
	ClientSecret       *MCPSecretBinding        `json:"clientSecret,omitempty"`
	Scopes             []string                 `json:"scopes"`
	SessionRef         *MCPSecretBinding        `json:"sessionRef,omitempty"`
	Status             MCPOAuthStatusKind       `json:"status"`
	Issuer             string                   `json:"issuer,omitempty"`
	GrantedScopes      []string                 `json:"grantedScopes"`
	AccessTokenExpires *time.Time               `json:"accessTokenExpires,omitempty"`
}

type MCPHTTPConfig struct {
	Endpoint string             `json:"endpoint"`
	Auth     MCPHTTPAuthKind    `json:"auth"`
	Headers  []MCPHeaderBinding `json:"headers"`
	Bearer   *MCPSecretBinding  `json:"bearer,omitempty"`
	OAuth    *MCPOAuthConfig    `json:"oauth,omitempty"`
}

// MCPLimits are persisted as milliseconds/bytes so the JSON aggregate does
// not depend on Go duration's nanosecond representation.
type MCPLimits struct {
	StartupTimeoutMS int `json:"startupTimeoutMs"`
	CallTimeoutMS    int `json:"callTimeoutMs"`
	IdleTimeoutMS    int `json:"idleTimeoutMs"`
	MaxResultBytes   int `json:"maxResultBytes"`
}

func (l MCPLimits) StartupTimeout() time.Duration {
	return time.Duration(l.StartupTimeoutMS) * time.Millisecond
}

func (l MCPLimits) CallTimeout() time.Duration {
	return time.Duration(l.CallTimeoutMS) * time.Millisecond
}

func (l MCPLimits) IdleTimeout() time.Duration {
	return time.Duration(l.IdleTimeoutMS) * time.Millisecond
}

type MCPCatalogState string

const (
	MCPCatalogMissing MCPCatalogState = "missing"
	MCPCatalogFresh   MCPCatalogState = "fresh"
	MCPCatalogStale   MCPCatalogState = "stale"
)

type MCPToolStatus string

const (
	MCPToolUnchanged MCPToolStatus = "unchanged"
	MCPToolNew       MCPToolStatus = "new"
	MCPToolChanged   MCPToolStatus = "changed"
)

// MCPTool holds bounded, untrusted discovery metadata. Description is never
// reused as nocx authority or a model tool description. Input/OutputSchema
// are sanitized and canonicalized at the descriptor boundary before hashing
// and persistence; schema annotations never enter the declaration channel.
type MCPTool struct {
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	InputSchema      json.RawMessage `json:"inputSchema"`
	OutputSchema     json.RawMessage `json:"outputSchema,omitempty"`
	DescriptorDigest string          `json:"descriptorDigest"`
	Enabled          bool            `json:"enabled"`
	Status           MCPToolStatus   `json:"status"`
}

type MCPCatalog struct {
	State           MCPCatalogState `json:"state"`
	ServerName      string          `json:"serverName,omitempty"`
	ServerVersion   string          `json:"serverVersion,omitempty"`
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	RefreshedAt     *time.Time      `json:"refreshedAt,omitempty"`
	Digest          string          `json:"digest,omitempty"`
	Tools           []MCPTool       `json:"tools"`
}

type MCPServer struct {
	ID        string           `json:"id"`
	Revision  uint64           `json:"revision"`
	Name      string           `json:"name"`
	Enabled   bool             `json:"enabled"`
	Transport MCPTransportKind `json:"transport"`
	Stdio     *MCPStdioConfig  `json:"stdio,omitempty"`
	HTTP      *MCPHTTPConfig   `json:"http,omitempty"`
	Limits    MCPLimits        `json:"limits"`
	Catalog   MCPCatalog       `json:"catalog"`
}

var (
	ErrMCPServerNotFound = errors.New("MCP server not found")
	ErrMCPServerConflict = errors.New("MCP server revision conflict")
	ErrMCPToolNotFound   = errors.New("MCP tool not found")
)

// MCPDeleteResult carries only backend cleanup work. OwnedSecretRefs must not
// be put on the wire; shared references are deliberately omitted.
type MCPDeleteResult struct {
	OwnedSecretRefs []string
}

// MCPServerRepository is the narrow persistence capability consumed by the
// control plane. Every mutating method after Create is a revision CAS.
type MCPServerRepository interface {
	ListMCPServers() ([]MCPServer, error)
	GetMCPServer(id string) (MCPServer, error)
	CreateMCPServer(server MCPServer) (MCPServer, error)
	UpdateMCPServer(server MCPServer) (MCPServer, error)
	DeleteMCPServer(id string, revision uint64) (MCPDeleteResult, error)
	SetMCPToolsEnabled(id string, revision uint64, names []string) (MCPServer, error)
	RefreshMCPServerCatalog(id string, revision uint64, catalog MCPCatalog) (MCPServer, error)
}

func NewMCPServerID(name string) string {
	slug := slugify(name, 64)
	return "mcp:" + slug + ":" + newUUID()
}

func (s MCPServer) CanonicalDestination() (string, error) {
	switch s.Transport {
	case MCPTransportStdio:
		if s.ID == "" || HasControlChars(s.ID) {
			return "", errors.New("stdio MCP destination requires a valid server id")
		}
		return "mcp+stdio:" + s.ID, nil
	case MCPTransportStreamableHTTP:
		if s.HTTP == nil {
			return "", errors.New("HTTP MCP destination requires HTTP config")
		}
		return CanonicalMCPHTTPDestination(s.HTTP.Endpoint)
	default:
		return "", fmt.Errorf("unknown MCP transport %q", s.Transport)
	}
}

// CanonicalMCPHTTPDestination validates the static URL shape and normalizes
// scheme, host, default port, and an empty path. Dial-time DNS/rebinding and
// redirect checks remain the runtime transport's responsibility.
func CanonicalMCPHTTPDestination(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > MaxMCPCommandBytes || HasControlChars(raw) {
		return "", errors.New("MCP HTTP endpoint is required and must be bounded text")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid MCP HTTP endpoint: %w", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("MCP HTTP endpoint scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("MCP HTTP endpoint must have a host and no userinfo or fragment")
	}
	hostname := strings.ToLower(u.Hostname())
	if u.Scheme == "http" && !localHTTPHost(hostname) {
		return "", errors.New("plaintext MCP HTTP is allowed only for loopback or private IP endpoints")
	}
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	u.Host = hostname
	if port != "" {
		u.Host = net.JoinHostPort(strings.Trim(hostname, "[]"), port)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func localHTTPHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ValidateMCPServer(server MCPServer) error {
	s := cloneMCPServer(server)
	normalizeMCPServer(&s)
	if s.ID != "" && (len(s.ID) > 256 || HasControlChars(s.ID)) {
		return errors.New("MCP server id is invalid")
	}
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > MaxMCPNameBytes || HasControlChars(s.Name) {
		return fmt.Errorf("MCP server name is required and must be at most %d bytes", MaxMCPNameBytes)
	}
	if err := validateMCPLimits(s.Limits); err != nil {
		return err
	}
	switch s.Transport {
	case MCPTransportStdio:
		if s.Stdio == nil || s.HTTP != nil {
			return errors.New("stdio MCP server must have exactly a stdio config")
		}
		if err := validateMCPStdio(*s.Stdio); err != nil {
			return err
		}
	case MCPTransportStreamableHTTP:
		if s.HTTP == nil || s.Stdio != nil {
			return errors.New("streamable-http MCP server must have exactly an HTTP config")
		}
		if err := validateMCPHTTP(*s.HTTP); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown MCP transport %q", s.Transport)
	}
	return validateMCPCatalog(s.Catalog, false)
}

func validateMCPLimits(l MCPLimits) error {
	if l.StartupTimeoutMS < 100 || l.StartupTimeoutMS > MaxMCPStartupTimeoutMillis {
		return fmt.Errorf("MCP startup timeout must be between 100 and %d ms", MaxMCPStartupTimeoutMillis)
	}
	if l.CallTimeoutMS < 100 || l.CallTimeoutMS > MaxMCPCallTimeoutMillis {
		return fmt.Errorf("MCP call timeout must be between 100 and %d ms", MaxMCPCallTimeoutMillis)
	}
	if l.IdleTimeoutMS < 0 || l.IdleTimeoutMS > MaxMCPIdleTimeoutMillis {
		return fmt.Errorf("MCP idle timeout must be between 0 and %d ms", MaxMCPIdleTimeoutMillis)
	}
	if l.MaxResultBytes < 1024 || l.MaxResultBytes > MaxMCPResultBytes {
		return fmt.Errorf("MCP result bound must be between 1024 and %d bytes", MaxMCPResultBytes)
	}
	return nil
}

func validateMCPStdio(c MCPStdioConfig) error {
	if strings.TrimSpace(c.Command) == "" || len(c.Command) > MaxMCPCommandBytes || strings.IndexByte(c.Command, 0) >= 0 || !utf8.ValidString(c.Command) {
		return errors.New("MCP stdio command is required and must be bounded UTF-8 without NUL")
	}
	if c.Cwd != "" && (!filepath.IsAbs(c.Cwd) || len(c.Cwd) > MaxMCPCommandBytes || strings.IndexByte(c.Cwd, 0) >= 0) {
		return errors.New("MCP stdio cwd must be an absolute bounded path")
	}
	if len(c.Argv) > MaxMCPBindingRows || len(c.Env) > MaxMCPBindingRows {
		return fmt.Errorf("MCP stdio argv and env are limited to %d rows", MaxMCPBindingRows)
	}
	for i, arg := range c.Argv {
		if len(arg) > MaxMCPCommandBytes || strings.IndexByte(arg, 0) >= 0 || !utf8.ValidString(arg) {
			return fmt.Errorf("argv[%d] is not bounded UTF-8 without NUL", i)
		}
	}
	seen := make(map[string]struct{}, len(c.Env))
	for i, row := range c.Env {
		if !envNameRE.MatchString(row.Name) {
			return fmt.Errorf("env[%d] has invalid name %q", i, row.Name)
		}
		if _, duplicate := seen[row.Name]; duplicate {
			return fmt.Errorf("env variable %q is set more than once", row.Name)
		}
		seen[row.Name] = struct{}{}
		if err := validateMCPValueBinding(row.Value, false); err != nil {
			return fmt.Errorf("env[%d]: %w", i, err)
		}
	}
	return nil
}

var refusedMCPHeaders = map[string]struct{}{
	"authorization": {}, "cookie": {}, "host": {}, "connection": {},
	"keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
	"accept": {}, "content-type": {}, "mcp-protocol-version": {},
	"mcp-session-id": {}, "mcp-method": {}, "mcp-name": {}, "last-event-id": {},
}

func validateMCPHTTP(c MCPHTTPConfig) error {
	if _, err := CanonicalMCPHTTPDestination(c.Endpoint); err != nil {
		return err
	}
	if len(c.Headers) > MaxMCPBindingRows {
		return fmt.Errorf("MCP HTTP headers are limited to %d rows", MaxMCPBindingRows)
	}
	seen := make(map[string]struct{}, len(c.Headers))
	for i, row := range c.Headers {
		if !isToken(row.Name) || HasControlChars(row.Name) {
			return fmt.Errorf("headers[%d] has invalid name %q", i, row.Name)
		}
		name := strings.ToLower(row.Name)
		if _, refused := refusedMCPHeaders[name]; refused {
			return fmt.Errorf("headers[%d] %q is reserved", i, row.Name)
		}
		canonical := http.CanonicalHeaderKey(row.Name)
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("header %q is set more than once", canonical)
		}
		seen[canonical] = struct{}{}
		if err := validateMCPValueBinding(row.Value, true); err != nil {
			return fmt.Errorf("headers[%d]: %w", i, err)
		}
	}
	switch c.Auth {
	case MCPHTTPAuthNone:
		if c.Bearer != nil || c.OAuth != nil {
			return errors.New("MCP HTTP auth none must not carry bearer or OAuth config")
		}
	case MCPHTTPAuthBearer:
		if c.Bearer == nil || c.OAuth != nil {
			return errors.New("MCP HTTP bearer auth requires exactly a bearer binding")
		}
		if err := validateMCPSecretBinding(*c.Bearer); err != nil {
			return fmt.Errorf("bearer: %w", err)
		}
	case MCPHTTPAuthOAuth:
		if c.OAuth == nil || c.Bearer != nil {
			return errors.New("MCP HTTP OAuth auth requires exactly an OAuth config")
		}
		if err := validateMCPOAuth(*c.OAuth); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown MCP HTTP auth %q", c.Auth)
	}
	return nil
}

func validateMCPValueBinding(v MCPValueBinding, header bool) error {
	switch v.Kind {
	case MCPBindingLiteral:
		if v.Literal == nil || v.SecretRef != "" || v.Owned {
			return errors.New("literal binding must have only a literal value")
		}
		if len(*v.Literal) > MaxMCPBindingLiteralBytes || !utf8.ValidString(*v.Literal) || strings.IndexByte(*v.Literal, 0) >= 0 {
			return errors.New("literal binding is not bounded UTF-8 without NUL")
		}
		if header && HasControlChars(*v.Literal) {
			return errors.New("header literal must not contain control characters")
		}
	case MCPBindingSecret:
		if v.Literal != nil {
			return errors.New("secret binding must not carry a literal")
		}
		if err := validateMCPSecretBinding(MCPSecretBinding{SecretRef: v.SecretRef, Owned: v.Owned}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown MCP binding kind %q", v.Kind)
	}
	return nil
}

func validateMCPSecretBinding(v MCPSecretBinding) error {
	if v.SecretRef == "" || len(v.SecretRef) > 512 || HasControlChars(v.SecretRef) {
		return errors.New("secret binding requires a bounded opaque reference")
	}
	return nil
}

func validateMCPOAuth(o MCPOAuthConfig) error {
	if len(o.Scopes) > 64 || len(o.GrantedScopes) > 64 {
		return errors.New("OAuth scopes are limited to 64 entries")
	}
	for _, scopes := range [][]string{o.Scopes, o.GrantedScopes} {
		seen := map[string]struct{}{}
		for _, scope := range scopes {
			if scope == "" || len(scope) > 256 || HasControlChars(scope) {
				return errors.New("OAuth scope must be bounded text")
			}
			if _, duplicate := seen[scope]; duplicate {
				return fmt.Errorf("OAuth scope %q appears more than once", scope)
			}
			seen[scope] = struct{}{}
		}
	}
	switch o.Registration {
	case MCPOAuthDynamic:
		if o.ClientID != "" || o.ClientSecret != nil {
			return errors.New("dynamic OAuth registration must not carry preregistered client credentials")
		}
	case MCPOAuthPreregistered:
		if o.ClientID == "" || len(o.ClientID) > 512 || HasControlChars(o.ClientID) {
			return errors.New("preregistered OAuth requires a bounded client id")
		}
		if o.ClientSecret != nil {
			if err := validateMCPSecretBinding(*o.ClientSecret); err != nil {
				return fmt.Errorf("OAuth client secret: %w", err)
			}
		}
	default:
		return fmt.Errorf("unknown OAuth registration %q", o.Registration)
	}
	if o.SessionRef != nil {
		if err := validateMCPSecretBinding(*o.SessionRef); err != nil {
			return fmt.Errorf("OAuth session: %w", err)
		}
		if !o.SessionRef.Owned {
			return errors.New("OAuth session secret must be system-owned")
		}
	}
	switch o.Status {
	case MCPOAuthMissing:
		if o.SessionRef != nil || o.Issuer != "" || len(o.GrantedScopes) != 0 || o.AccessTokenExpires != nil {
			return errors.New("missing OAuth status must not carry session metadata")
		}
	case MCPOAuthConnected, MCPOAuthExpired:
		if o.SessionRef == nil {
			return errors.New("connected or expired OAuth status requires a session reference")
		}
	default:
		return fmt.Errorf("unknown OAuth status %q", o.Status)
	}
	return nil
}

var schemaAnnotations = map[string]struct{}{
	"description": {}, "title": {}, "examples": {}, "default": {},
	"deprecated": {}, "readOnly": {}, "writeOnly": {}, "$comment": {},
}

// CanonicalMCPToolDescriptor is the single schema/digest boundary for discovery,
// persistence and live descriptor verification. It never mutates the input.
// Remote annotations are stripped only at schema positions, not from literal data.
func CanonicalMCPToolDescriptor(tool MCPTool) (MCPTool, error) {
	if strings.TrimSpace(tool.Name) == "" || len(tool.Name) > 512 || HasControlChars(tool.Name) || !utf8.ValidString(tool.Name) {
		return MCPTool{}, errors.New("invalid MCP tool name")
	}
	if len(tool.Description) > MaxMCPDescriptionBytes || !utf8.ValidString(tool.Description) {
		return MCPTool{}, errors.New("MCP tool description exceeds its bound")
	}
	var err error
	tool.InputSchema, err = canonicalMCPSchema(tool.InputSchema, true)
	if err != nil {
		return MCPTool{}, fmt.Errorf("tool %q input schema: %w", tool.Name, err)
	}
	tool.OutputSchema, err = canonicalMCPSchema(tool.OutputSchema, false)
	if err != nil {
		return MCPTool{}, fmt.Errorf("tool %q output schema: %w", tool.Name, err)
	}
	tool.DescriptorDigest = descriptorDigest(tool)
	return tool, nil
}

var mcpSchemaKeywords = map[string]struct{}{
	"$schema": {}, "$ref": {}, "$defs": {}, "definitions": {},
	"type": {}, "enum": {}, "const": {},
	"oneOf": {}, "anyOf": {}, "allOf": {}, "not": {},
	"if": {}, "then": {}, "else": {},
	"properties": {}, "patternProperties": {}, "additionalProperties": {},
	"additionalItems": {}, "unevaluatedProperties": {}, "propertyNames": {},
	"required": {}, "dependentRequired": {}, "dependentSchemas": {}, "dependencies": {},
	"items": {}, "prefixItems": {}, "contains": {}, "minContains": {}, "maxContains": {},
	"unevaluatedItems": {}, "minProperties": {}, "maxProperties": {},
	"minItems": {}, "maxItems": {}, "uniqueItems": {},
	"minLength": {}, "maxLength": {}, "pattern": {}, "format": {},
	"contentEncoding": {}, "contentMediaType": {},
	"multipleOf": {}, "maximum": {}, "exclusiveMaximum": {},
	"minimum": {}, "exclusiveMinimum": {},
}

func validateMCPSchema(raw json.RawMessage, required bool) error {
	_, err := canonicalMCPSchema(raw, required)
	return err
}

func canonicalMCPSchema(raw json.RawMessage, required bool) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if required {
			return nil, errors.New("input schema is required")
		}
		return nil, nil
	}
	if len(raw) > MaxMCPSchemaBytes {
		return nil, fmt.Errorf("schema exceeds %d bytes", MaxMCPSchemaBytes)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid schema JSON: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("invalid schema JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("MCP schema root must be an object")
	}
	if err := validateSchemaNode(value, 0); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	// No schema URL (including a custom metaschema) may cause filesystem or
	// network I/O. Only the resource below and compiler built-ins are available.
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	const resource = "https://nocx.local/mcp/schema.json"
	if err := compiler.AddResource(resource, value); err != nil {
		return nil, fmt.Errorf("compile MCP schema: %w", err)
	}
	if _, err := compiler.Compile(resource); err != nil {
		return nil, fmt.Errorf("compile MCP schema: %w", err)
	}
	return canonical, nil
}

func validateSchemaNode(value any, depth int) error {
	// References may target only nodes visited as schemas, never const/enum
	// data. Otherwise a local pointer could bypass the keyword policy.
	nodes := make(map[string]struct{})
	var refs []string
	escape := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
	}
	var walk func(any, string, int) error
	walk = func(value any, path string, depth int) error {
		if depth > 32 {
			return errors.New("MCP schema nesting exceeds 32 levels")
		}
		nodes[path] = struct{}{}
		if _, ok := value.(bool); ok {
			return nil
		}
		node, ok := value.(map[string]any)
		if !ok {
			return errors.New("MCP schema node must be an object or boolean")
		}
		for key, child := range node {
			if _, annotation := schemaAnnotations[key]; annotation {
				delete(node, key)
				continue
			}
			if _, supported := mcpSchemaKeywords[key]; !supported {
				return fmt.Errorf("unsupported MCP schema keyword %q", key)
			}
			childPath := path + "/" + escape(key)
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") {
					return errors.New("MCP schema references must be local JSON pointers")
				}
				pointer, err := url.PathUnescape(ref[1:])
				if err != nil {
					return errors.New("invalid MCP schema JSON pointer")
				}
				refs = append(refs, pointer)
			}
			switch key {
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies":
				schemas, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("MCP schema keyword %q must be an object", key)
				}
				for name, schema := range schemas {
					if _, propertyNames := schema.([]any); key == "dependencies" && propertyNames {
						continue
					}
					if err := walk(schema, childPath+"/"+escape(name), depth+1); err != nil {
						return err
					}
				}
			case "oneOf", "anyOf", "allOf", "prefixItems", "items":
				if schemas, ok := child.([]any); ok {
					for i, schema := range schemas {
						if err := walk(schema, fmt.Sprintf("%s/%d", childPath, i), depth+1); err != nil {
							return err
						}
					}
				} else if key == "items" {
					if err := walk(child, childPath, depth+1); err != nil {
						return err
					}
				} else {
					return fmt.Errorf("MCP schema keyword %q must be an array", key)
				}
			case "additionalProperties", "additionalItems", "unevaluatedProperties", "propertyNames",
				"contains", "unevaluatedItems", "not", "if", "then", "else":
				if err := walk(child, childPath, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value, "", depth); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, ok := nodes[ref]; !ok {
			return errors.New("MCP schema reference must target a validated schema node")
		}
	}
	return nil
}

func validateMCPCatalog(c MCPCatalog, requireFresh bool) error {
	if c.Tools == nil {
		return errors.New("MCP catalog tools must be a non-nil array")
	}
	if len(c.Tools) > MaxMCPToolsPerServer {
		return fmt.Errorf("MCP catalog is limited to %d tools", MaxMCPToolsPerServer)
	}
	switch c.State {
	case MCPCatalogMissing:
		if len(c.Tools) != 0 || c.Digest != "" {
			return errors.New("missing MCP catalog must not contain tools or a digest")
		}
	case MCPCatalogFresh:
		if c.Digest == "" || c.RefreshedAt == nil {
			return errors.New("fresh MCP catalog requires digest and refresh time")
		}
	case MCPCatalogStale:
		// Stale discovery metadata remains visible but is not executable.
	default:
		if requireFresh {
			return fmt.Errorf("invalid discovered MCP catalog state %q", c.State)
		}
		return fmt.Errorf("unknown MCP catalog state %q", c.State)
	}
	seen := make(map[string]struct{}, len(c.Tools))
	for i := range c.Tools {
		t := c.Tools[i]
		if strings.TrimSpace(t.Name) == "" || len(t.Name) > 512 || HasControlChars(t.Name) {
			return fmt.Errorf("tools[%d] has invalid name", i)
		}
		if _, duplicate := seen[t.Name]; duplicate {
			return fmt.Errorf("tool %q appears more than once", t.Name)
		}
		seen[t.Name] = struct{}{}
		if len(t.Description) > MaxMCPDescriptionBytes || !utf8.ValidString(t.Description) {
			return fmt.Errorf("tool %q description exceeds its bound", t.Name)
		}
		if err := validateMCPSchema(t.InputSchema, true); err != nil {
			return fmt.Errorf("tool %q input schema: %w", t.Name, err)
		}
		if err := validateMCPSchema(t.OutputSchema, false); err != nil {
			return fmt.Errorf("tool %q output schema: %w", t.Name, err)
		}
		if t.DescriptorDigest != "" && !isHexDigest(t.DescriptorDigest) {
			return fmt.Errorf("tool %q has invalid descriptor digest", t.Name)
		}
		switch t.Status {
		case MCPToolUnchanged, MCPToolNew, MCPToolChanged:
		default:
			if c.State != MCPCatalogMissing {
				return fmt.Errorf("tool %q has invalid status %q", t.Name, t.Status)
			}
		}
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode MCP catalog: %w", err)
	}
	if len(encoded) > MaxMCPCatalogBytes {
		return fmt.Errorf("MCP catalog exceeds %d bytes", MaxMCPCatalogBytes)
	}
	return nil
}

func isHexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func descriptorDigest(t MCPTool) string {
	h := sha256.New()
	_, _ = h.Write([]byte(t.Name))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(t.InputSchema)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(t.OutputSchema)
	return hex.EncodeToString(h.Sum(nil))
}

func catalogDigest(tools []MCPTool) string {
	ordered := append([]MCPTool(nil), tools...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	h := sha256.New()
	for _, tool := range ordered {
		_, _ = h.Write([]byte(tool.DescriptorDigest))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeMCPServer(s *MCPServer) {
	if s.Catalog.State == "" {
		s.Catalog.State = MCPCatalogMissing
	}
	if s.Catalog.Tools == nil {
		s.Catalog.Tools = []MCPTool{}
	}
	if s.Stdio != nil {
		if s.Stdio.Argv == nil {
			s.Stdio.Argv = []string{}
		}
		if s.Stdio.Env == nil {
			s.Stdio.Env = []MCPEnvBinding{}
		}
	}
	if s.HTTP != nil {
		if s.HTTP.Headers == nil {
			s.HTTP.Headers = []MCPHeaderBinding{}
		}
		if s.HTTP.OAuth != nil {
			if s.HTTP.OAuth.Scopes == nil {
				s.HTTP.OAuth.Scopes = []string{}
			}
			if s.HTTP.OAuth.GrantedScopes == nil {
				s.HTTP.OAuth.GrantedScopes = []string{}
			}
		}
	}
}

func validateMCPStoredServers(servers []MCPServer) error {
	if len(servers) > MaxMCPServers {
		return fmt.Errorf("MCP server limit %d exceeded", MaxMCPServers)
	}
	seen := make(map[string]struct{}, len(servers))
	for i := range servers {
		server := servers[i]
		if server.ID == "" {
			return fmt.Errorf("MCP server %d has no id", i)
		}
		if _, duplicate := seen[server.ID]; duplicate {
			return fmt.Errorf("duplicate MCP server id %q", server.ID)
		}
		seen[server.ID] = struct{}{}
		if err := ValidateMCPServer(server); err != nil {
			return fmt.Errorf("MCP server %q: %w", server.ID, err)
		}
	}
	return nil
}

func validateMCPStoreData(d *storeData) error {
	return validateMCPStoredServers(d.MCPServers)
}

func (s *JSONStore) ListMCPServers() ([]MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]MCPServer(nil), d.MCPServers...)
	if out == nil {
		out = []MCPServer{}
	}
	return out, nil
}

func (s *JSONStore) GetMCPServer(id string) (MCPServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPServer{}, err
	}
	for _, server := range d.MCPServers {
		if server.ID == id {
			return server, nil
		}
	}
	return MCPServer{}, fmt.Errorf("%s: %w", id, ErrMCPServerNotFound)
}

func (s *JSONStore) CreateMCPServer(server MCPServer) (MCPServer, error) {
	server = cloneMCPServer(server)
	server.ID = NewMCPServerID(server.Name)
	server.Revision = 1
	server.Catalog = MCPCatalog{State: MCPCatalogMissing, Tools: []MCPTool{}}
	normalizeMCPServer(&server)
	if err := ValidateMCPServer(server); err != nil {
		return MCPServer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPServer{}, err
	}
	if len(d.MCPServers) >= MaxMCPServers {
		return MCPServer{}, fmt.Errorf("MCP server limit %d reached", MaxMCPServers)
	}
	for _, existing := range d.MCPServers {
		if existing.ID == server.ID {
			return MCPServer{}, errors.New("generated duplicate MCP server id")
		}
	}
	d.MCPServers = append(d.MCPServers, server)
	if err := s.writeLocked(d); err != nil {
		return MCPServer{}, err
	}
	return server, nil
}

func (s *JSONStore) UpdateMCPServer(server MCPServer) (MCPServer, error) {
	server = cloneMCPServer(server)
	normalizeMCPServer(&server)
	if server.ID == "" || server.Revision == 0 {
		return MCPServer{}, errors.New("MCP update requires id and revision")
	}
	if err := ValidateMCPServer(server); err != nil {
		return MCPServer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPServer{}, err
	}
	for i := range d.MCPServers {
		current := d.MCPServers[i]
		if current.ID != server.ID {
			continue
		}
		if current.Revision != server.Revision {
			return MCPServer{}, fmt.Errorf("%s: have revision %d, got %d: %w", server.ID, current.Revision, server.Revision, ErrMCPServerConflict)
		}
		configChanged := !sameMCPRuntimeConfig(current, server)
		server.Revision++
		server.Catalog = current.Catalog
		if configChanged {
			server.Catalog.State = MCPCatalogStale
		}
		d.MCPServers[i] = server
		if err := s.writeLocked(d); err != nil {
			return MCPServer{}, err
		}
		return server, nil
	}
	return MCPServer{}, fmt.Errorf("%s: %w", server.ID, ErrMCPServerNotFound)
}

func sameMCPRuntimeConfig(a, b MCPServer) bool {
	return a.Transport == b.Transport && reflect.DeepEqual(a.Stdio, b.Stdio) &&
		reflect.DeepEqual(a.HTTP, b.HTTP) && reflect.DeepEqual(a.Limits, b.Limits)
}

func (s *JSONStore) DeleteMCPServer(id string, revision uint64) (MCPDeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPDeleteResult{}, err
	}
	for i := range d.MCPServers {
		current := d.MCPServers[i]
		if current.ID != id {
			continue
		}
		if current.Revision != revision {
			return MCPDeleteResult{}, fmt.Errorf("%s: have revision %d, got %d: %w", id, current.Revision, revision, ErrMCPServerConflict)
		}
		refs := MCPSecretReferences(current, true)
		d.MCPServers = append(d.MCPServers[:i], d.MCPServers[i+1:]...)
		safe := unreferencedSecretRefs(d, refs)
		if err := s.writeLocked(d); err != nil {
			return MCPDeleteResult{}, err
		}
		return MCPDeleteResult{OwnedSecretRefs: safe}, nil
	}
	return MCPDeleteResult{}, fmt.Errorf("%s: %w", id, ErrMCPServerNotFound)
}

func (s *JSONStore) SetMCPToolsEnabled(id string, revision uint64, names []string) (MCPServer, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return MCPServer{}, errors.New("enabled MCP tool name is required")
		}
		if _, duplicate := wanted[name]; duplicate {
			return MCPServer{}, fmt.Errorf("enabled MCP tool %q appears more than once", name)
		}
		wanted[name] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPServer{}, err
	}
	for i := range d.MCPServers {
		current := &d.MCPServers[i]
		if current.ID != id {
			continue
		}
		if current.Revision != revision {
			return MCPServer{}, fmt.Errorf("%s: have revision %d, got %d: %w", id, current.Revision, revision, ErrMCPServerConflict)
		}
		if current.Catalog.State != MCPCatalogFresh {
			return MCPServer{}, errors.New("MCP tools can be enabled only from a fresh catalog")
		}
		for _, tool := range current.Catalog.Tools {
			delete(wanted, tool.Name)
		}
		if len(wanted) != 0 {
			for name := range wanted {
				return MCPServer{}, fmt.Errorf("%s: %w", name, ErrMCPToolNotFound)
			}
		}
		enabled := make(map[string]struct{}, len(names))
		for _, name := range names {
			enabled[name] = struct{}{}
		}
		for j := range current.Catalog.Tools {
			_, current.Catalog.Tools[j].Enabled = enabled[current.Catalog.Tools[j].Name]
		}
		current.Revision++
		if err := s.writeLocked(d); err != nil {
			return MCPServer{}, err
		}
		return *current, nil
	}
	return MCPServer{}, fmt.Errorf("%s: %w", id, ErrMCPServerNotFound)
}

func (s *JSONStore) RefreshMCPServerCatalog(id string, revision uint64, discovered MCPCatalog) (MCPServer, error) {
	if len(discovered.Tools) > MaxMCPToolsPerServer {
		return MCPServer{}, fmt.Errorf("MCP catalog is limited to %d tools", MaxMCPToolsPerServer)
	}
	discovered = cloneMCPCatalog(discovered)
	for i := range discovered.Tools {
		tool, err := CanonicalMCPToolDescriptor(discovered.Tools[i])
		if err != nil {
			return MCPServer{}, err
		}
		tool.Enabled = false
		tool.Status = MCPToolNew
		discovered.Tools[i] = tool
	}
	now := time.Now().UTC()
	discovered.State = MCPCatalogFresh
	discovered.RefreshedAt = &now
	discovered.Digest = catalogDigest(discovered.Tools)
	if err := validateMCPCatalog(discovered, true); err != nil {
		return MCPServer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return MCPServer{}, err
	}
	for i := range d.MCPServers {
		current := &d.MCPServers[i]
		if current.ID != id {
			continue
		}
		if current.Revision != revision {
			return MCPServer{}, fmt.Errorf("%s: have revision %d, got %d: %w", id, current.Revision, revision, ErrMCPServerConflict)
		}
		oldByName := make(map[string]MCPTool, len(current.Catalog.Tools))
		for _, old := range current.Catalog.Tools {
			oldByName[old.Name] = old
		}
		for j := range discovered.Tools {
			tool := &discovered.Tools[j]
			if old, ok := oldByName[tool.Name]; ok {
				if old.DescriptorDigest == tool.DescriptorDigest {
					tool.Enabled = old.Enabled
					tool.Status = MCPToolUnchanged
				} else {
					tool.Status = MCPToolChanged
				}
			}
		}
		current.Catalog = discovered
		current.Revision++
		if err := s.writeLocked(d); err != nil {
			return MCPServer{}, err
		}
		return *current, nil
	}
	return MCPServer{}, fmt.Errorf("%s: %w", id, ErrMCPServerNotFound)
}

func visitMCPSecretRefs(server *MCPServer, visit func(ref string, owned bool)) {
	visitValue := func(v *MCPValueBinding) {
		if v != nil && v.Kind == MCPBindingSecret && v.SecretRef != "" {
			visit(v.SecretRef, v.Owned)
		}
	}
	visitSecret := func(v *MCPSecretBinding) {
		if v != nil && v.SecretRef != "" {
			visit(v.SecretRef, v.Owned)
		}
	}
	if server.Stdio != nil {
		for i := range server.Stdio.Env {
			visitValue(&server.Stdio.Env[i].Value)
		}
	}
	if server.HTTP != nil {
		for i := range server.HTTP.Headers {
			visitValue(&server.HTTP.Headers[i].Value)
		}
		visitSecret(server.HTTP.Bearer)
		if server.HTTP.OAuth != nil {
			visitSecret(server.HTTP.OAuth.ClientSecret)
			visitSecret(server.HTTP.OAuth.SessionRef)
		}
	}
}

// MCPServerDTO is the minimal sanitized editable record. Opaque references
// are represented only by presence/ownership facts; neither SecretID nor
// secret material can be obtained by marshaling this DTO.
type MCPServerDTO struct {
	ID        string           `json:"id"`
	Revision  uint64           `json:"revision"`
	Name      string           `json:"name"`
	Enabled   bool             `json:"enabled"`
	Transport MCPTransportKind `json:"transport"`
	Stdio     *MCPStdioDTO     `json:"stdio"`
	HTTP      *MCPHTTPDTO      `json:"http"`
	Limits    MCPLimits        `json:"limits"`
	Catalog   MCPCatalog       `json:"catalog"`
}

type MCPValueBindingDTO struct {
	Kind      MCPBindingKind `json:"kind"`
	Literal   *string        `json:"literal"`
	SecretSet bool           `json:"secretSet"`
	Owned     bool           `json:"owned"`
}

type MCPStdioDTO struct {
	Command string             `json:"command"`
	Argv    []string           `json:"argv"`
	Cwd     string             `json:"cwd"`
	Env     []MCPEnvBindingDTO `json:"env"`
}

type MCPEnvBindingDTO struct {
	Name  string             `json:"name"`
	Value MCPValueBindingDTO `json:"value"`
}

type MCPHeaderBindingDTO struct {
	Name  string             `json:"name"`
	Value MCPValueBindingDTO `json:"value"`
}

type MCPSecretBindingDTO struct {
	SecretSet bool `json:"secretSet"`
	Owned     bool `json:"owned"`
}

type MCPOAuthDTO struct {
	Registration       MCPOAuthRegistrationKind `json:"registration"`
	ClientID           string                   `json:"clientId"`
	ClientSecret       MCPSecretBindingDTO      `json:"clientSecret"`
	Scopes             []string                 `json:"scopes"`
	SessionSet         bool                     `json:"sessionSet"`
	Status             MCPOAuthStatusKind       `json:"status"`
	Issuer             string                   `json:"issuer"`
	GrantedScopes      []string                 `json:"grantedScopes"`
	AccessTokenExpires *time.Time               `json:"accessTokenExpires"`
}

type MCPHTTPDTO struct {
	Endpoint string                `json:"endpoint"`
	Auth     MCPHTTPAuthKind       `json:"auth"`
	Headers  []MCPHeaderBindingDTO `json:"headers"`
	Bearer   MCPSecretBindingDTO   `json:"bearer"`
	OAuth    *MCPOAuthDTO          `json:"oauth"`
}

func (s MCPServer) SanitizedDTO() MCPServerDTO {
	s = cloneMCPServer(s)
	dto := MCPServerDTO{ID: s.ID, Revision: s.Revision, Name: s.Name, Enabled: s.Enabled, Transport: s.Transport, Limits: s.Limits, Catalog: s.Catalog}
	if dto.Catalog.Tools == nil {
		dto.Catalog.Tools = []MCPTool{}
	}
	if s.Stdio != nil {
		dto.Stdio = &MCPStdioDTO{Command: s.Stdio.Command, Argv: append([]string{}, s.Stdio.Argv...), Cwd: s.Stdio.Cwd, Env: make([]MCPEnvBindingDTO, len(s.Stdio.Env))}
		for i, row := range s.Stdio.Env {
			dto.Stdio.Env[i] = MCPEnvBindingDTO{Name: row.Name, Value: sanitizeMCPValue(row.Value)}
		}
	}
	if s.HTTP != nil {
		dto.HTTP = &MCPHTTPDTO{Endpoint: s.HTTP.Endpoint, Auth: s.HTTP.Auth, Headers: make([]MCPHeaderBindingDTO, len(s.HTTP.Headers)), Bearer: sanitizeMCPSecret(s.HTTP.Bearer)}
		for i, row := range s.HTTP.Headers {
			dto.HTTP.Headers[i] = MCPHeaderBindingDTO{Name: row.Name, Value: sanitizeMCPValue(row.Value)}
		}
		if o := s.HTTP.OAuth; o != nil {
			dto.HTTP.OAuth = &MCPOAuthDTO{Registration: o.Registration, ClientID: o.ClientID, ClientSecret: sanitizeMCPSecret(o.ClientSecret), Scopes: append([]string{}, o.Scopes...), SessionSet: o.SessionRef != nil, Status: o.Status, Issuer: o.Issuer, GrantedScopes: append([]string{}, o.GrantedScopes...), AccessTokenExpires: o.AccessTokenExpires}
		}
	}
	return dto
}

func sanitizeMCPValue(v MCPValueBinding) MCPValueBindingDTO {
	return MCPValueBindingDTO{Kind: v.Kind, Literal: v.Literal, SecretSet: v.Kind == MCPBindingSecret && v.SecretRef != "", Owned: v.Kind == MCPBindingSecret && v.Owned}
}

func sanitizeMCPSecret(v *MCPSecretBinding) MCPSecretBindingDTO {
	return MCPSecretBindingDTO{SecretSet: v != nil && v.SecretRef != "", Owned: v != nil && v.Owned}
}

func strictUnmarshal(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(dec)
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("more than one JSON value")
		}
		return err
	}
	return nil
}

func (v *MCPValueBinding) UnmarshalJSON(b []byte) error {
	type plain MCPValueBinding
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPSecretBinding) UnmarshalJSON(b []byte) error {
	type plain MCPSecretBinding
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPEnvBinding) UnmarshalJSON(b []byte) error {
	type plain MCPEnvBinding
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPHeaderBinding) UnmarshalJSON(b []byte) error {
	type plain MCPHeaderBinding
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPStdioConfig) UnmarshalJSON(b []byte) error {
	type plain MCPStdioConfig
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPOAuthConfig) UnmarshalJSON(b []byte) error {
	type plain MCPOAuthConfig
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPHTTPConfig) UnmarshalJSON(b []byte) error {
	type plain MCPHTTPConfig
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPLimits) UnmarshalJSON(b []byte) error {
	type plain MCPLimits
	return strictUnmarshal(b, (*plain)(v))
}

func (v *MCPTool) UnmarshalJSON(b []byte) error {
	type plain MCPTool
	if err := strictUnmarshal(b, (*plain)(v)); err != nil {
		return err
	}
	// The document store indents RawMessages. Storage formatting must not
	// turn an otherwise bounded canonical schema into an unloadable record.
	for _, schema := range []*json.RawMessage{&v.InputSchema, &v.OutputSchema} {
		if len(*schema) > MaxMCPSchemaBytes {
			var compact bytes.Buffer
			if err := json.Compact(&compact, *schema); err != nil {
				return err
			}
			*schema = compact.Bytes()
		}
	}
	canonical, err := CanonicalMCPToolDescriptor(*v)
	if err != nil {
		return err
	}
	*v = canonical
	return nil
}

func (v *MCPCatalog) UnmarshalJSON(b []byte) error {
	type plain MCPCatalog
	if err := strictUnmarshal(b, (*plain)(v)); err != nil {
		return err
	}
	if v.Digest != "" {
		v.Digest = catalogDigest(v.Tools)
	}
	return nil
}

func (v *MCPServer) UnmarshalJSON(b []byte) error {
	type plain MCPServer
	return strictUnmarshal(b, (*plain)(v))
}
