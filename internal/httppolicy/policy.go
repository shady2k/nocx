// Package httppolicy owns the http:// address rule and the credential
// boundary, for every HTTP client in nocx. It was extracted from
// internal/assistant/httpguard.go so that the API-testing executor could
// obey the same rule rather than re-derive it (design §7.3): two answers to
// one question agree everywhere you look and disagree somewhere you did not.
//
// The comment below is the extracted guard's, carried across word for word.
//
// The http:// address rule (design §4.5 decision 3, bead notes): http:// is
// permitted ONLY for loopback and private addresses, and it is enforced ON
// EVERY CONNECTION, not in the form. The four reasons it cannot be a form
// check are the whole of why this file reads the way it does — a future
// reader who does not know them will "simplify" this into a form validator:
//
//  1. a hostname can resolve public while it is validated and private when
//     it is dialled — so the guard resolves, validates and dials ONE answer;
//  2. a redirect can walk from https public to http private — and the other
//     way, so every redirect hop is re-checked as if it were a new endpoint;
//  3. localhost is not the only spelling of loopback — IPv6 loopback,
//     link-local, IPv4-mapped and cloud metadata addresses are all reachable
//     by name, so the rule is about the RESOLVED ADDRESS, never the string;
//  4. proxy environment variables can reroute a request the user believes is
//     local — so http:// always dials DIRECT, and the address checked is the
//     address connected.
//
// And the credential is never forwarded across an origin change: a redirect
// to a different scheme, host or port loses the Authorization header.
//
// https is unrestricted: the rule exists because a frame can carry a
// password prompt or a token from a pager, and sending it in clear text to a
// remote host is a validation failure. Over https the credential is
// encrypted in transit, so remote https endpoints are the product's normal
// case. https keeps the environment proxy (corporate MITM still works);
// only the restricted http scheme dials direct.
package httppolicy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/shady2k/nocx/internal/log"
)

// DefaultMaxRedirects bounds a redirect chain. Every hop is re-checked, so
// the bound is about work, not about safety.
const DefaultMaxRedirects = 10

// Params builds a Transport. Component is the prefix every error carries —
// "assistant", "apisend" — so a refusal names the caller that hit the rule
// and not this package, which the user has never heard of.
type Params struct {
	Component       string
	Route           Route
	Log             log.Logger // may be nil
	MaxRedirects    int        // 0 → DefaultMaxRedirects
	TLSClientConfig *tls.Config
}

// Transport applies the rule to every connection an http.Client makes.
// Inner and Proxy are exported because a caller legitimately configures the
// transport it owns (TLS trust, timeouts) and because the proxy decision is
// worth exercising directly in a test.
type Transport struct {
	Inner *http.Transport
	Proxy ProxyFunc

	component    string
	route        Route
	log          log.Logger
	maxRedirects int
}

// NewTransport builds the guarded transport alone, for a caller that needs
// to hold the client itself (a cookie jar, a timeout).
func NewTransport(p Params) *Transport {
	if p.Route == nil {
		p.Route = Local()
	}
	if p.MaxRedirects <= 0 {
		p.MaxRedirects = DefaultMaxRedirects
	}
	t := &Transport{
		component:    p.Component,
		route:        p.Route,
		log:          p.Log,
		maxRedirects: p.MaxRedirects,
	}
	t.Inner = &http.Transport{
		// http always dials DIRECT (reason 4): a proxy would reroute a
		// request the user believes is local. https keeps the route's
		// proxy.
		Proxy: func(req *http.Request) (*url.URL, error) {
			if req.URL.Scheme == "http" {
				return nil, nil
			}
			return t.route.ProxyForHTTPS(req)
		},
		// The dial uses the resolution the policy validated, so the address
		// connected is the address checked (reason 1). The request context
		// carries the validated spec from RoundTrip.
		DialContext:     t.dial,
		TLSClientConfig: p.TLSClientConfig,
	}
	t.Proxy = t.Inner.Proxy
	return t
}

// dialSpec is the validated resolution for one request, threaded from
// RoundTrip (where the scheme is known) to the dial (where it is not).
type dialSpec struct {
	scheme string
	host   string
	port   string
	ips    []net.IP
}

type dialSpecKey struct{}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	spec, err := t.check(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	if spec.scheme == "http" {
		ctx := context.WithValue(req.Context(), dialSpecKey{}, spec)
		return t.Inner.RoundTrip(req.WithContext(ctx))
	}
	return t.Inner.RoundTrip(req)
}

// check applies the address rule to one URL. It returns the dial spec that
// pins the connection to the validated resolution.
func (t *Transport) check(ctx context.Context, u *url.URL) (dialSpec, error) {
	switch u.Scheme {
	case "https":
		return dialSpec{scheme: "https"}, nil
	case "http":
		// fall through to the address check
	default:
		return dialSpec{}, fmt.Errorf("%s: unsupported URL scheme %q", t.component, u.Scheme)
	}

	host := u.Hostname()
	ips, err := t.route.LookupIP(ctx, host)
	if err != nil {
		return dialSpec{}, fmt.Errorf("%s: resolving %q: %w", t.component, host, err)
	}
	if err := CheckDestination(t.component, u.Scheme, host, ips); err != nil {
		return dialSpec{}, err
	}
	return dialSpec{
		scheme: "http",
		host:   host,
		port:   portFor(u),
		ips:    ips,
	}, nil
}

// CheckDestination applies the rule to one resolved destination: https is
// unrestricted; http requires every resolved address to be loopback or
// private. A mixed answer (some public, some private) is refused — the
// hostname can be dialled either way, so it is not permitted either way.
func CheckDestination(component, scheme, host string, ips []net.IP) error {
	if scheme != "http" {
		return nil
	}
	if len(ips) == 0 {
		return fmt.Errorf("%s: http:// to %q refused: no address resolved", component, host)
	}
	for _, ip := range ips {
		if !isPrivateLoopback(ip) {
			return fmt.Errorf("%s: http:// to %q refused: %s is not a loopback or private address (http:// is permitted only for loopback and private addresses; remote endpoints are https only)", component, host, ip)
		}
	}
	return nil
}

// isPrivateLoopback answers whether ip is a loopback or private address in
// every spelling the rule cares about: 127/8 and ::1 (loopback), RFC1918,
// link-local (169.254/16 and fe80::/10 — cloud metadata lives here), ULA
// (fc00::/7), and IPv4-mapped IPv6 (::ffff:x), where the embedded IPv4 must
// itself be private or loopback.
func isPrivateLoopback(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// To4 unwraps ::ffff:x.y.z.w, so the mapped forms are decided by
		// their embedded IPv4.
		return v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast()
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
}

// portFor returns the effective port for the URL's scheme.
func portFor(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// dial dials the validated resolution: for an http request, exactly the IPs
// RoundTrip validated; otherwise the address as given. Both go through the
// route, which is the point of the extraction — the assistant's route dials
// with net.Dialer, a connection route dials on the far side.
func (t *Transport) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if spec, ok := ctx.Value(dialSpecKey{}).(dialSpec); ok && spec.scheme == "http" {
		var lastErr error
		for _, ip := range spec.ips {
			conn, err := t.route.DialContext(ctx, network, net.JoinHostPort(ip.String(), spec.port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("no addresses to dial")
		}
		if t.log != nil {
			t.log.Warn(t.component+" dial failed", "host", spec.host, "error", lastErr)
		}
		return nil, lastErr
	}
	return t.route.DialContext(ctx, network, addr)
}

// CheckRedirect enforces the credential rule: a hop to a different origin
// (scheme, host or port) never carries the credential. It also bounds the
// chain length. The address rule itself is enforced per hop in RoundTrip —
// this is only the credential boundary.
//
// The endpoint's custom headers follow the SAME rule (bead nocx-lyyk): a
// header value can BE a credential — Azure's api-key header is the key — so
// a token in a custom header must not survive a redirect the Authorization
// header would not. The names of the custom headers ride the request
// context (WithCustomHeaderNames, set by the callers that build the
// requests), and exactly those names are dropped on a crossing — never a
// guess from arbitrary request headers, which would strip headers the user
// deliberately set on an endpoint that is supposed to keep them.
// A SECRET IN THE URL CANNOT BE STRIPPED, SO THE HOP IS REFUSED. The rule
// above works by DELETION: a header is a thing beside the request, so it can
// be dropped and the request still means what it meant. A value in a path or
// a query is not beside the request — it IS the target, and a redirect that
// carried it would hand a credential to an origin the person never named,
// while a redirect that removed it would ask for a resource nobody meant.
// Neither is a thing to do quietly, so the chain stops and says so.
//
// It is here rather than beside the one caller that can produce it, because
// this function IS the credential boundary — a second rule about credentials
// crossing origins would be two owners of one property, agreeing until the
// day one of them was edited. Callers that never mark a request are
// unaffected: the flag rides the same context channel the custom header
// names do, and nothing sets it by default.
func (t *Transport) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= t.maxRedirects {
		return fmt.Errorf("%s: stopped after %d redirects", t.component, t.maxRedirects)
	}
	if len(via) > 0 && Origin(req.URL) != Origin(via[0].URL) {
		if secretInURL(req.Context()) {
			// The ORIGIN and never the URL: the URL is the thing carrying
			// the credential, and this message reaches a log the person did
			// not choose.
			return fmt.Errorf("%s: refusing to follow a redirect to %s: this request carries a "+
				"secret in its address, and a value in a path or a query cannot be dropped on the "+
				"way the Authorization header can", t.component, Origin(req.URL))
		}
		req.Header.Del("Authorization")
		req.Header.Del("Www-Authenticate")
		req.Header.Del("Cookie")
		req.Header.Del("Cookie2")
		for _, name := range customHeaderNames(req.Context()) {
			req.Header.Del(name)
		}
	}
	return nil
}

// customHeaderNamesKey carries the canonical names of the endpoint's custom
// headers on the request context. The redirect rule needs them (CheckRedirect
// above); the context is the one channel that survives from the initial
// request into every redirect hop (net/http clones the initial request's
// context), so the names are available exactly when they are needed.
type customHeaderNamesKey struct{}

// secretInURLKey marks a request whose ADDRESS carries a credential. It is a
// bool and never the value: the redirect rule needs to know that there is
// one, and nothing in this package needs to know what it is.
type secretInURLKey struct{}

// WithSecretInURL marks ctx as a request whose path or query carries a
// secret. Set by the caller that placed it — internal/apisend, which is the
// only one that substitutes a vault-held value into an address and therefore
// the only one that can know.
//
// The context is the channel that survives into every redirect hop (net/http
// clones the initial request's context), which is why the custom header
// names travel the same way.
func WithSecretInURL(ctx context.Context) context.Context {
	return context.WithValue(ctx, secretInURLKey{}, true)
}

// secretInURL reads the mark back.
func secretInURL(ctx context.Context) bool {
	marked, _ := ctx.Value(secretInURLKey{}).(bool)
	return marked
}

// WithCustomHeaderNames tags ctx with the canonical names of the custom
// headers the request carries. Set by the request builders (the assistant's
// engine.go for the completion, connection.go for the connection check; the
// sender for a collection request's headers) so the policy never has to
// guess which headers are the endpoint's.
func WithCustomHeaderNames(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, customHeaderNamesKey{}, names)
}

// customHeaderNames reads the tagged names back, or nil when none.
func customHeaderNames(ctx context.Context) []string {
	names, _ := ctx.Value(customHeaderNamesKey{}).([]string)
	return names
}

// Origin is the RFC 6454 origin triple, compared strictly: a subdomain or a
// scheme change is a different origin, and the credential does not follow.
func Origin(u *url.URL) string {
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
