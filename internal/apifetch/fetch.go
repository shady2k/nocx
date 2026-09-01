// Package apifetch acquires documents by URL over the route the person chose.
//
// It is a package of its own for two reasons. internal/apiimport states that
// it does not reach the network (write.go), and it must go on being true —
// the converter is fed a reader and has no business knowing where the bytes
// came from. And the transport handler is not a place to keep an HTTP client:
// a fetch has a route, a ceiling, a timeout and a refusal vocabulary, which is
// a seam rather than four lines in a switch.
//
// The assistant's text fetch extends this package instead of creating a sibling:
// both operations need the same guarded transport, route table and exchange
// bounds, while their response interpretation is explicit (JSON import versus
// UTF-8 text). Presentation-specific shaping belongs to the assistant, which
// decides whether to return source markup, pretty-print JSON, or extract HTML.
// A sibling would make it possible for the assistant to acquire a second HTTP
// client and silently diverge from the address and credential boundary owned by
// httppolicy.
//
// WHAT IT REUSES, AND WHY IT IS NOT A SECOND SENDER. The route table is
// apisend's own (apisend.Routes): `direct` dials from this machine and
// `connection` leases the pooled SSH connection for a profile, refusing by
// name when it cannot — a fetch must never quietly go out around the bastion
// the person named, for the same reason a send must not (apisend/routes.go).
// The transport is httppolicy's, so the http:// address rule and the
// credential-across-origins rule apply here exactly as they do to every other
// request this product makes.
//
// WHAT IT DOES NOT REUSE: apisend.Client.Send. Its ceiling is 2 MiB with no
// option, deliberately (client.go), and an acquisition has a caller-owned
// ceiling and a refusal vocabulary. Widening the sender for either caller
// would put a fetch knob on every send.
//
// IT NEVER VERIFIES LESS THAN NORMAL. route.InsecureTLS is ignored: the ask
// has no such control, and a fetch that could turn verification off would be a
// second place that decides it, beside the environment that owns it.
package apifetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
	"golang.org/x/net/html/charset"
)

const component = "apifetch"

// fetchTimeout bounds the whole exchange — dial, TLS, headers and body.
// A document that has not arrived in a minute is not arriving; the ask is
// modal and a person is watching it.
const fetchTimeout = time.Minute

var (
	// ErrScheme — the URL names something this cannot GET.
	ErrScheme = errors.New(component + ": an import URL must be http:// or https://")
	// ErrTooLarge — the body is over the caller's ceiling.
	ErrTooLarge = errors.New(component + ": the document at that address is too large to import")
	// ErrNotADocument — the first byte says this is not JSON.
	//
	// It is refused HERE rather than passed on, and that is the point of
	// it: apiimport.ImportInto tells its entrances apart by first byte and
	// hands anything that is not JSON to the CURL parser, so a login page
	// fetched instead of an export would come back as a curl parse error in
	// a dialog that never offered curl.
	ErrNotADocument = errors.New(component + ": what is at that address is not a Postman export")
	// ErrNotText — the assistant only returns UTF-8 text, never binary bytes.
	ErrNotText = errors.New(component + ": the response is not UTF-8 text")
)

// Fetcher acquires the document at a URL. One method, because "get me those
// bytes" is the whole of what the import needs from the network.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error)
}

// TextRequest asks the guarded fetch seam for one complete decoded document.
//
// MaxBytes is the absolute acquisition ceiling, not the assistant's response
// window. The assistant keeps the complete document in its run-scoped
// snapshot and applies the smaller result bound there.
type TextRequest struct {
	URL      string
	MaxBytes int64
}

// TextDocument is the complete decoded document acquired by the fetch seam.
// Text is decoded source text, not a presentation guarantee, and may contain
// markup. Callers may transform it for their presentation. Lossy reports either
// replacement characters from charset decoding or such a lossy presentation
// transformation.
// URL is metadata from the HTTP request; callers that authorize a URL keep
// their original request identity rather than replacing it with a redirect
// target.
type TextDocument struct {
	URL         string
	ContentType string
	Text        string
	Lossy       bool
}

// TextFetcher is the assistant-facing extension of the guarded fetch seam.
type TextFetcher interface {
	FetchText(ctx context.Context, request TextRequest) (TextDocument, error)
}

// Client is the Fetcher over a route table.
type Client struct {
	routes apisend.Routes
	log    log.Logger
}

// New builds a fetcher over the route table. A nil logger is allowed.
func New(routes apisend.Routes, l log.Logger) *Client {
	return &Client{routes: routes, log: l}
}

// Fetch GETs rawURL over route and returns the whole body.
//
// COMPLETELY, before anything is written: ImportInto's arrival is atomic and
// a half-read body must not be able to produce a half-collection. The cost is
// one document in memory, bounded by the same ceiling the parse uses.
func (c *Client) Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error) {
	resp, u, err := c.get(ctx, rawURL, route)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp, apiimport.MaxDocumentBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", component, u.Redacted(), err)
	}

	// The first byte, and never Content-Type: a server that mislabels JSON
	// is common and a server that labels HTML as JSON is not rare either,
	// so the bytes are the only witness worth believing. It is also the
	// same rule the rest of the import already lives by.
	for i, b := range body {
		if unicode.IsSpace(rune(b)) {
			continue
		}
		if b == '{' || b == '[' {
			return body[i:], nil
		}
		break
	}
	return nil, fmt.Errorf("%w: %s", ErrNotADocument, u.Redacted())
}

// FetchText gets a direct-route HTTP response as a complete UTF-8 document.
// The assistant applies its per-result window after this seam returns; this
// method only enforces the absolute acquisition ceiling in request.MaxBytes.
func (c *Client) FetchText(ctx context.Context, request TextRequest) (TextDocument, error) {
	if request.MaxBytes <= 0 {
		return TextDocument{}, fmt.Errorf("%s: text ceiling must be positive", component)
	}
	resp, u, err := c.get(ctx, request.URL, apicoll.Route{Kind: apicoll.RouteDirect})
	if err != nil {
		return TextDocument{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	body, err := readBody(resp, request.MaxBytes)
	if err != nil {
		return TextDocument{}, fmt.Errorf("%s: reading %s: %w", component, u.Redacted(), err)
	}
	if charsetErr := validateDeclaredCharset(contentType); charsetErr != nil {
		return TextDocument{}, charsetErr
	}
	// The header remains metadata and a decoding hint; the body is the only
	// witness for whether this response is text or binary.
	text, lossy, err := decodeText(body, contentType)
	if err != nil {
		return TextDocument{}, fmt.Errorf("%s: decoding %s: %w", component, u.Redacted(), err)
	}
	if sampleLooksBinary(body, contentType) {
		return TextDocument{}, fmt.Errorf("%w: body at %s looks binary", ErrNotText, u.Redacted())
	}

	// request.MaxBytes is authoritative for decoded text too. Reject expanded
	// UTF-8 rather than retaining a document beyond the acquisition ceiling.
	if int64(len(text)) > request.MaxBytes {
		return TextDocument{}, fmt.Errorf("%w (decoded text exceeds the limit of %d bytes)", ErrTooLarge, request.MaxBytes)
	}
	return TextDocument{URL: request.URL, ContentType: contentType, Text: text, Lossy: lossy}, nil
}

func (c *Client) get(ctx context.Context, rawURL string, route apicoll.Route) (*http.Response, *url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %q is not a URL: %w", component, rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, fmt.Errorf("%w (this one is %q)", ErrScheme, u.Scheme)
	}

	routeID, err := apisend.RouteIDFor(route)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", component, err)
	}
	r, err := c.routes(ctx, routeID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", component, err)
	}

	tr := c.transport(r)
	client := &http.Client{Transport: tr, CheckRedirect: tr.CheckRedirect, Timeout: fetchTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", component, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: fetching %s: %w", component, u.Redacted(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("%s: fetching %s: the server answered %d %s",
			component, u.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resp, u, nil
}

func readBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w (the limit is %d bytes)", ErrTooLarge, maxBytes)
	}
	return body, nil
}

func sampleLooksBinary(body []byte, contentType string) bool {
	sampleSize := len(body)
	if sampleSize > 4096 {
		sampleSize = 4096
	}
	if bytes.IndexByte(body[:sampleSize], 0) >= 0 {
		return true
	}
	if sampleSize == 0 {
		return false
	}
	text, _, err := decodeText(body[:sampleSize], contentType)
	if err != nil {
		return true
	}
	replacements := strings.Count(text, "\ufffd")
	characters := utf8.RuneCountInString(text)
	return replacements >= 3 && characters > 0 && replacements*100 > characters
}

func validateDeclaredCharset(contentType string) error {
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if label, ok := params["charset"]; ok {
			if _, name := charset.Lookup(label); name == "" {
				return fmt.Errorf("%w: unsupported charset %q", ErrNotText, label)
			}
		}
	}
	return nil
}

var xmlEncodingDeclaration = regexp.MustCompile(`(?is)^\x{FEFF}?\s*<\?xml\b[^>]*\bencoding\s*=\s*["']([^"']+)["']`)

func decodeText(body []byte, contentType string) (text string, lossy bool, err error) {
	if len(body) == 0 {
		return "", false, nil
	}
	effectiveContentType, err := contentTypeForDecode(body, contentType)
	if err != nil {
		return "", false, err
	}
	// Header charset beats the XML declaration, which beats charset.NewReader's
	// content sniffing: transport metadata is closer to the bytes than XML's
	// document-level opinion.
	reader, err := charset.NewReader(bytes.NewReader(body), effectiveContentType)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrNotText, err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrNotText, err)
	}
	text = string(decoded)
	valid := utf8.ValidString(text)
	lossy = !valid || strings.Contains(text, "\ufffd")
	if !valid {
		text = replaceInvalidUTF8(text)
	}
	return text, lossy, nil
}

func contentTypeForDecode(body []byte, contentType string) (string, error) {
	_, params, parseErr := mime.ParseMediaType(contentType)
	if parseErr == nil {
		if _, headerCharset := params["charset"]; headerCharset {
			return contentType, nil
		}
	} else if contentType != "" {
		return contentType, nil
	}
	declarationCharset, ok := xmlDeclarationCharset(body)
	if !ok {
		return contentType, nil
	}
	if _, name := charset.Lookup(declarationCharset); name == "" {
		return "", fmt.Errorf("%w: unsupported charset %q", ErrNotText, declarationCharset)
	}
	return contentTypeWithCharset(contentType, declarationCharset), nil
}

func xmlDeclarationCharset(body []byte) (string, bool) {
	match := xmlEncodingDeclaration.FindSubmatch(body)
	if len(match) != 2 {
		return "", false
	}
	return strings.TrimSpace(string(match[1])), true
}

func contentTypeWithCharset(contentType, label string) string {
	if contentType == "" {
		return "text/plain; charset=" + label
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType + "; charset=" + label
	}
	params["charset"] = label
	return mime.FormatMediaType(mediaType, params)
}

func replaceInvalidUTF8(text string) string {
	var normalized strings.Builder
	normalized.Grow(len(text))
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		if r == utf8.RuneError && size == 1 {
			normalized.WriteRune(utf8.RuneError)
		} else {
			normalized.WriteString(text[offset : offset+size])
		}
		offset += size
	}
	return normalized.String()
}

// transport builds the guarded transport for one route.
//
// It is its own function so that the thing it does NOT set can be asserted
// by a test: there is no TLSClientConfig here, on any route, ever. A fetch
// is not an environment (apicoll/collection.go:126) — insecureTls belongs to
// the environment a person configures, and a one-off acquisition may not
// spend it.
func (c *Client) transport(r httppolicy.Route) *httppolicy.Transport {
	return httppolicy.NewTransport(httppolicy.Params{
		Component: component,
		Route:     r,
		Log:       c.log,
	})
}
