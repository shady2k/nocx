package assistant

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

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpproxy"

	"github.com/shady2k/nocx/internal/log"
)

type guardedTransport struct {
	inner   *http.Transport
	proxy   func(*http.Request) (*url.URL, error)
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	log     log.Logger
}

// newGuardedHTTPClient builds the http.Client every model call goes through.
// logger may be nil (tests).
func newGuardedHTTPClient(logger log.Logger) *http.Client {
	return newGuardedHTTPClientWithResolver(logger, nil)
}

func newGuardedHTTPClientWithResolver(logger log.Logger, resolve func(ctx context.Context, host string) ([]net.IP, error)) *http.Client {
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, nil
		}
	}

	tr := &guardedTransport{
		resolve: resolve,
		log:     logger,
	}
	tr.inner = &http.Transport{
		// http always dials DIRECT (reason 4): a proxy would reroute a
		// request the user believes is local. https keeps the environment
		// proxy.
		Proxy: func(req *http.Request) (*url.URL, error) {
			if req.URL.Scheme == "http" {
				return nil, nil
			}
			// Read the environment fresh per request rather than through
			// http.ProxyFromEnvironment, which caches the parsed config ONCE
			// per process — a cache that makes the guard's behavior depend
			// on which request ran first (and on other tests' env). The
			// env is static in production; a fresh read costs nothing.
			cfg := httpproxy.FromEnvironment()
			return cfg.ProxyFunc()(req.URL)
		},
		// The dial uses the resolution the guard validated, so the address
		// connected is the address checked (reason 1). The request context
		// carries the validated spec from RoundTrip.
		DialContext: tr.dial,
	}
	tr.proxy = tr.inner.Proxy

	return &http.Client{
		Transport:     tr,
		CheckRedirect: tr.checkRedirect,
	}
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

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	spec, err := t.check(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	if spec.scheme == "http" {
		ctx := context.WithValue(req.Context(), dialSpecKey{}, spec)
		return t.inner.RoundTrip(req.WithContext(ctx))
	}
	return t.inner.RoundTrip(req)
}

// check applies the address rule to one URL. It returns the dial spec that
// pins the connection to the validated resolution.
func (t *guardedTransport) check(ctx context.Context, u *url.URL) (dialSpec, error) {
	switch u.Scheme {
	case "https":
		return dialSpec{scheme: "https"}, nil
	case "http":
		// fall through to the address check
	default:
		return dialSpec{}, fmt.Errorf("assistant: unsupported URL scheme %q", u.Scheme)
	}

	host := u.Hostname()
	ips, err := t.resolve(ctx, host)
	if err != nil {
		return dialSpec{}, fmt.Errorf("assistant: resolving %q: %w", host, err)
	}
	if err := checkDestination(u.Scheme, host, ips); err != nil {
		return dialSpec{}, err
	}
	return dialSpec{
		scheme: "http",
		host:   host,
		port:   portFor(u),
		ips:    ips,
	}, nil
}

// checkDestination applies the rule to one resolved destination: https is
// unrestricted; http requires every resolved address to be loopback or
// private. A mixed answer (some public, some private) is refused — the
// hostname can be dialled either way, so it is not permitted either way.
func checkDestination(scheme, host string, ips []net.IP) error {
	if scheme != "http" {
		return nil
	}
	if len(ips) == 0 {
		return fmt.Errorf("assistant: http:// to %q refused: no address resolved", host)
	}
	for _, ip := range ips {
		if !isPrivateLoopback(ip) {
			return fmt.Errorf("assistant: http:// to %q refused: %s is not a loopback or private address (http:// is permitted only for loopback and private addresses; remote endpoints are https only)", host, ip)
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
// RoundTrip validated; otherwise the ordinary net dial.
func (t *guardedTransport) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if spec, ok := ctx.Value(dialSpecKey{}).(dialSpec); ok && spec.scheme == "http" {
		var lastErr error
		for _, ip := range spec.ips {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), spec.port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("no addresses to dial")
		}
		if t.log != nil {
			t.log.Warn("assistant dial failed", "host", spec.host, "error", lastErr)
		}
		return nil, lastErr
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// checkRedirect enforces the credential rule: a hop to a different origin
// (scheme, host or port) never carries the credential. It also bounds the
// chain length. The address rule itself is enforced per hop in RoundTrip —
// this is only the credential boundary.
//
// The endpoint's custom headers follow the SAME rule (bead nocx-lyyk): a
// header value can BE a credential — Azure's api-key header is the key — so
// a token in a custom header must not survive a redirect the Authorization
// header would not. The names of the custom headers ride the request
// context (withCustomHeaderNames, set by the callers that build the
// requests), and exactly those names are dropped on a crossing — never a
// guess from arbitrary request headers, which would strip headers the user
// deliberately set on an endpoint that is supposed to keep them.
func (t *guardedTransport) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("assistant: stopped after 10 redirects")
	}
	if len(via) > 0 && origin(req.URL) != origin(via[0].URL) {
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
// headers on the request context. The redirect rule needs them (checkRedirect
// above); the context is the one channel that survives from the initial
// request into every redirect hop (net/http clones the initial request's
// context), so the names are available exactly when they are needed.
type customHeaderNamesKey struct{}

// withCustomHeaderNames tags ctx with the canonical names of the custom
// headers the request carries. Set by the request builders (engine.go for
// the completion, connection.go for the connection check) so the guard never
// has to guess which headers are the endpoint's.
func withCustomHeaderNames(ctx context.Context, names []string) context.Context {
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

// origin is the RFC 6454 origin triple, compared strictly: a subdomain or a
// scheme change is a different origin, and the credential does not follow.
func origin(u *url.URL) string {
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
