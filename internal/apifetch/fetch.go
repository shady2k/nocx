// Package apifetch acquires an import document by URL, over the route the
// person chose.
//
// It is a package of its own for two reasons. internal/apiimport states that
// it does not reach the network (write.go), and it must go on being true —
// the converter is fed a reader and has no business knowing where the bytes
// came from. And the transport handler is not a place to keep an HTTP
// client: a fetch has a route, a ceiling, a timeout and a refusal
// vocabulary, which is a seam rather than four lines in a switch.
//
// WHAT IT REUSES, AND WHY IT IS NOT A SECOND SENDER. The route table is
// apisend's own (apisend.Routes): `direct` dials from this machine and
// `connection` leases the pooled SSH connection for a profile, refusing by
// name when it cannot — a fetch must never quietly go out around the bastion
// the person named, for the same reason a send must not (apisend/routes.go).
// The transport is httppolicy's, so the http:// address rule and the
// credential-across-origins rule apply here exactly as they do to every
// other request this product makes.
//
// WHAT IT DOES NOT REUSE: apisend.Client.Send. Its ceiling is 2 MiB with no
// option, deliberately (client.go), and an import document is bounded at
// apiimport.MaxDocumentBytes. Widening the sender for the import would put a
// knob on every send to serve one caller.
//
// IT NEVER VERIFIES LESS THAN NORMAL. route.InsecureTLS is ignored: the ask
// has no such control, and a fetch that could turn verification off would be
// a second place that decides it, beside the environment that owns it.
package apifetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apiimport"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
)

const component = "apifetch"

// fetchTimeout bounds the whole exchange — dial, TLS, headers and body.
// A document that has not arrived in a minute is not arriving; the ask is
// modal and a person is watching it.
const fetchTimeout = time.Minute

var (
	// ErrScheme — the URL names something this cannot GET.
	ErrScheme = errors.New(component + ": an import URL must be http:// or https://")
	// ErrTooLarge — the body is over apiimport.MaxDocumentBytes.
	ErrTooLarge = errors.New(component + ": the document at that address is too large to import")
	// ErrNotADocument — the first byte says this is not JSON.
	//
	// It is refused HERE rather than passed on, and that is the point of
	// it: apiimport.ImportInto tells its entrances apart by first byte and
	// hands anything that is not JSON to the CURL parser, so a login page
	// fetched instead of an export would come back as a curl parse error in
	// a dialog that never offered curl.
	ErrNotADocument = errors.New(component + ": what is at that address is not a Postman export")
)

// Fetcher acquires the document at a URL. One method, because "get me those
// bytes" is the whole of what the import needs from the network.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, route apicoll.Route) ([]byte, error)
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
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not a URL: %w", component, rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w (this one is %q)", ErrScheme, u.Scheme)
	}

	routeID, err := apisend.RouteIDFor(route)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}
	r, err := c.routes(ctx, routeID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}

	tr := c.transport(r)
	client := &http.Client{Transport: tr, CheckRedirect: tr.CheckRedirect, Timeout: fetchTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", component, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: fetching %s: %w", component, u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: fetching %s: the server answered %d %s",
			component, u.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, apiimport.MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", component, u.Redacted(), err)
	}
	if len(body) > apiimport.MaxDocumentBytes {
		return nil, fmt.Errorf("%w (the limit is %d bytes)", ErrTooLarge, apiimport.MaxDocumentBytes)
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
