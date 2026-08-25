package profile

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// EndpointSchema is the wire-schema vocabulary of an AI endpoint (design
// §4.5, decision 2). The field is on the record because a schema cannot be
// inferred from a base URL; the set is closed and grows by addition — the
// UI's select appears when the second value does, never before.
type EndpointSchema string

const (
	// EndpointSchemaOpenAICompatible is the ONE schema this pass knows: the
	// chat-completions protocol the eino openai adapter speaks (design
	// §4.5; "OpenAI-compatible" is not one protocol — risk 6 — but this
	// pass implements exactly one and says so).
	EndpointSchemaOpenAICompatible EndpointSchema = "openai-compatible"
)

// validEndpointSchema reports whether v is a value this build recognises.
// An unrecognised stored value is a validation error at write time — there
// is no resolution layer for endpoints to fall back on, so the record must
// never hold a schema nobody can speak.
func validEndpointSchema(v EndpointSchema) bool {
	return v == EndpointSchemaOpenAICompatible
}

// NeedsCredential is the ONE derivation of the endpoint authentication fact.
// NoKey is explicit because a URL cannot tell whether a local or remote
// provider expects authentication; false preserves the credential-required
// behavior of records written before this field existed.
func (e Endpoint) NeedsCredential() bool {
	return !e.NoKey
}

// EndpointModel is one model an endpoint offers: the model id the API
// understands, plus an optional alias the picker shows instead of the id.
// Alias is nil when the picker should show Name.
type EndpointModel struct {
	Name  string  `json:"name"`
	Alias *string `json:"alias,omitempty"`
}

// Endpoint is an AI model endpoint (design §4.5, ADR-0030): a display
// name, a base URL, a wire schema, ONE credential and one or more models.
//
// CredentialRef is the endpoint's OWN secret: the API key the user gave at
// create/update time, minted into the vault. It is a BACKEND-OWNED
// reference (sec:v1:...) — on the wire the transport replaces it with the
// renderer's row handle, exactly like profile secret bindings (ADR-0017
// §1), so a reference never crosses the boundary. Empty means no key is
// set: the endpoint's explicit NoKey fact or a deleted credential is
// represented separately so the ask path can distinguish them.
type Endpoint struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	BaseURL string         `json:"baseUrl"`
	Schema  EndpointSchema `json:"schema"`
	// NoKey is a person-declared fact, not a URL heuristic. Its false zero
	// value keeps old records credential-required.
	NoKey         bool            `json:"noKey"`
	CredentialRef string          `json:"credentialRef"`
	Models        []EndpointModel `json:"models"`
	// Headers are the custom HTTP headers the endpoint sends on every
	// request (bead nocx-lyyk). Zero or more; nil and [] are the same
	// empty list on the wire (the contract declares an array, never null).
	Headers []EndpointHeader `json:"headers,omitempty"`
}

// EndpointHeader is one custom HTTP header the endpoint sends on every
// request it makes — the streaming completion AND the connection check, so
// a Test that passes means the real calls will too (bead nocx-lyyk).
//
// A header value can BE a credential (Azure's api-key header is the key), so
// the value's SOURCE is a literal or a vault secret, chosen with the same
// control the endpoint's own key uses. Exactly one of Value (a literal) and
// ValueRef (a backend-owned secret reference, sec:v1:...) is set; on the
// wire the transport maps ValueRef to the renderer's row handle, so the
// material never crosses the boundary (ADR-0017 §1).
type EndpointHeader struct {
	Name string `json:"name"`
	// Value is the literal header value, or null when the value is a vault
	// secret. An empty literal is legal HTTP and stays a pointer to "".
	Value *string `json:"value,omitempty"`
	// ValueRef is the backend-owned reference of the vault secret the value
	// resolves to at request time. Empty when the value is a literal.
	ValueRef string `json:"valueRef,omitempty"`
}

// ValidateBaseURL is the ONE parse-level rule for an endpoint's base URL,
// used both when a record is stored and when a base URL is probed without
// being stored (endpoints.probe, nocx-q27y). Two callers, one rule: a
// second copy would agree everywhere anyone looked and disagree on the URL
// nobody tried.
//
// Shape, not policy. The loopback/private address rule, redirect
// re-checking and proxy handling live at dial time in
// internal/assistant/httpguard.go, where they can be enforced against the
// address actually connected — a rule checked here against a string would
// be theatre, because a hostname can resolve public at validation and
// private at dial. What is checked here is what a string must be to be an
// address at all, plus userinfo, which is rejected because credentials
// belong in the credential field and never in the address.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base URL must name a host")
	}
	if u.User != nil {
		return fmt.Errorf("base URL must not carry credentials; put the API key in the credential field")
	}
	return nil
}

// HasControlChars reports whether s contains any C0/C1 control character or
// DEL. Tabs and newlines are control characters too, and neither belongs in
// a credential, a model id, a URL or a header name/value (the endpoint
// headers rule shares this one predicate with the transport's wire bounds —
// one owner, so a control character cannot be refused in one place and
// dialled in another).
//
// A raw C1 byte (\x80-\x9f) is not valid UTF-8, so ranging over the string
// would decode it to U+FFFD and miss it; an invalid-UTF-8 payload is refused
// outright — the C1 control characters are exactly the class this checks.
func HasControlChars(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// refusedEndpointHeaderNames names the headers an endpoint may not set, and
// WHY, so the refusal message can say which header and what it would have
// broken. The set is deliberate and closed:
//
//   - Authorization would silently fight the credential resolution: the
//     endpoint's own key is sent as the Bearer, and a user-set Authorization
//     would be whichever of the two won by evaluation order — the defect
//     this whole bead exists to avoid.
//   - Host is set from the base URL by the HTTP stack; overriding it breaks
//     the request the endpoint record describes.
//   - Content-Length is derived from the request body; a lying value breaks
//     the request framing.
//   - Content-Type is the JSON body's own media type; overriding it breaks
//     the completion the endpoint record promises.
//   - The hop-by-hop set (RFC 7230 §6.1) — Connection, Keep-Alive,
//     Proxy-Authenticate, Proxy-Authorization, TE, Trailer,
//     Transfer-Encoding, Upgrade — is consumed by the first hop and must
//     not be declared by an application header.
//
// Keys are lowercase; the lookup lowercases the name, because HTTP field
// names are case-insensitive ("TE" and "te" are the same header).
var refusedEndpointHeaderNames = map[string]string{
	"authorization":       "it would silently fight the API key's credential resolution",
	"host":                "the HTTP stack sets it from the base URL; overriding it breaks the request",
	"content-length":      "the request body derives it; a lying value breaks the request",
	"content-type":        "the JSON body's own media type; overriding it breaks the request",
	"connection":          "hop-by-hop (RFC 7230 §6.1) — consumed by the first hop, not settable by an application",
	"keep-alive":          "hop-by-hop (RFC 7230 §6.1)",
	"proxy-authenticate":  "hop-by-hop (RFC 7230 §6.1)",
	"proxy-authorization": "hop-by-hop (RFC 7230 §6.1)",
	"te":                  "hop-by-hop (RFC 7230 §6.1)",
	"trailer":             "hop-by-hop (RFC 7230 §6.1)",
	"transfer-encoding":   "hop-by-hop (RFC 7230 §6.1)",
	"upgrade":             "hop-by-hop (RFC 7230 §6.1)",
}

// isToken reports whether s is a valid HTTP field-name token (RFC 7230
// §3.2.6): one or more tchar. A name that is not a token cannot be sent by
// Go's http stack as a header name — it would be rejected or mangled — so it
// is refused before it is stored.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// ValidateEndpointHeaderName checks one header name. The refusal names which
// header and why, so the renderer can repeat it to the person typing it.
func ValidateEndpointHeaderName(name string) error {
	if name == "" {
		return errors.New("header name is required")
	}
	if HasControlChars(name) {
		return fmt.Errorf("header name %q must not contain control characters", name)
	}
	if !isToken(name) {
		return fmt.Errorf("header name %q is not a valid HTTP field name", name)
	}
	name = strings.ToLower(name)
	if reason, ok := refusedEndpointHeaderNames[name]; ok {
		return fmt.Errorf("header %q is refused: %s", name, reason)
	}
	return nil
}

// ValidateEndpointHeaders checks the endpoint's custom header list before it
// is stored: every name is valid and not refused, no name is set twice
// (case-insensitively — "X-Title" and "x-title" are the same header), and a
// literal value carries no control characters. Exactly one source per
// header: a literal or a vault reference, never neither and never both.
func ValidateEndpointHeaders(headers []EndpointHeader) error {
	seen := make(map[string]struct{}, len(headers))
	for i, h := range headers {
		if err := ValidateEndpointHeaderName(h.Name); err != nil {
			return fmt.Errorf("headers[%d]: %w", i, err)
		}
		canonical := http.CanonicalHeaderKey(h.Name)
		if _, dup := seen[canonical]; dup {
			return fmt.Errorf("headers[%d]: header %q is set more than once", i, canonical)
		}
		seen[canonical] = struct{}{}
		if (h.Value == nil) == (h.ValueRef == "") {
			return fmt.Errorf("headers[%d]: header %q must have exactly one source — a literal value or a vault secret", i, canonical)
		}
		if h.Value != nil && HasControlChars(*h.Value) {
			return fmt.Errorf("headers[%d]: value of header %q must not contain control characters", i, canonical)
		}
	}
	return nil
}

// ValidateEndpoint checks an endpoint record before it is stored.
//
// Base-URL validation is parse-level only this pass: any absolute http(s)
// URL is accepted. The loopback/private address policy, redirect
// re-checking and proxy handling belong to nocx-edio, where the HTTP
// client that could enforce them lands (design §4.5) — a rule with no
// enforcement point would be theatre. What IS checked here is shape, not
// policy: a URL that is not an absolute http(s) URL cannot be a base URL,
// and userinfo in the URL is rejected because credentials belong in the
// credential field, never in the address.
func ValidateEndpoint(e Endpoint) error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("endpoint name is required")
	}
	if !validEndpointSchema(e.Schema) {
		return fmt.Errorf("unknown endpoint schema %q", e.Schema)
	}
	if err := ValidateBaseURL(e.BaseURL); err != nil {
		return err
	}
	if e.NoKey && e.CredentialRef != "" {
		// Refuse the contradiction rather than silently dropping a stored
		// credential: the explicit declaration is allowed to be corrected
		// without hiding a secret reference the person still owns.
		return errors.New("endpoint declaring noKey must not carry a credential reference")
	}
	if len(e.Models) == 0 {
		return fmt.Errorf("endpoint requires at least one model")
	}
	for _, m := range e.Models {
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("model name is required")
		}
	}
	if err := ValidateEndpointHeaders(e.Headers); err != nil {
		return err
	}
	return nil
}

// NewEndpointID mints a namespaced endpoint id: "endpoint:custom:slug:uuid".
//
// Ids are minted here rather than in the renderer for the same reason
// profile ids are: an id is identity, and a display layer that invents one
// has to know the uniqueness rule the store enforces.
func NewEndpointID(name string) string {
	return "endpoint:custom:" + slugify(name) + ":" + newUUID()
}

// EndpointDTO is the wire form of an endpoint (design §4.5.4): the stored
// record with CredentialRef mapped to the renderer's row handle (or null
// when no key is set). The reference itself never crosses the wire — the
// transport does the mapping (vault.RowFor), the way wireProfile does for
// profiles.
type EndpointDTO struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	BaseURL    string             `json:"baseUrl"`
	Schema     EndpointSchema     `json:"schema"`
	NoKey      bool               `json:"noKey"`
	Credential *string            `json:"credential"`
	Models     []EndpointModelDTO `json:"models"`
	// Headers is never null: an endpoint with no custom headers sends [] —
	// the contract declares an array (nocx-25k9.14's shape).
	Headers []EndpointHeaderDTO `json:"headers"`
}

// EndpointHeaderDTO is the wire form of one custom header. The secret
// reference becomes the renderer's row handle (Secret) — never the material,
// and never the reference itself. Exactly one of Value and Secret is
// non-null; the contract declares both nullable on purpose.
type EndpointHeaderDTO struct {
	Name   string  `json:"name"`
	Value  *string `json:"value"`
	Secret *string `json:"secret"`
}

// EndpointModelDTO is the wire form of one model. Alias is required on the
// wire and null when absent — the contract declares nullable fields
// explicitly (["string","null"]), never by omission.
type EndpointModelDTO struct {
	Name  string  `json:"name"`
	Alias *string `json:"alias"`
}
