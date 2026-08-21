package httppolicy

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"golang.org/x/net/http/httpproxy"
)

// A Route is the second half of the extraction, and the half that is NOT
// shared: the policy is one rule set, but who answers a name and who opens
// the socket is route-specific. The assistant resolves with the machine's
// resolver and dials with a net.Dialer; a request routed through an SSH
// connection is dialled on the far side, where the name is resolved by the
// remote server (design §7.1) — so the concrete resolve-and-dial in the
// original guard could not be reused, only the rule could.
//
// The contract a Route implementation signs, and reason 1 depends on it:
// LookupIP returns the addresses the dial WILL ACTUALLY REACH. A route that
// cannot answer that question truthfully must return an error rather than a
// guess, because the policy validates exactly what LookupIP returned and
// then dials exactly that.
type Route interface {
	Resolver
	Dialer
	// ProxyForHTTPS reports the proxy for an https request, or nil for a
	// direct dial. Only https ever asks: http:// always dials direct
	// (reason 4), so the policy never consults this for it.
	ProxyForHTTPS(req *http.Request) (*url.URL, error)
}

// Resolver answers a hostname with the addresses that will be dialled.
type Resolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

// Dialer opens one connection. It is http.Transport.DialContext's signature
// deliberately: an adapter over an SSH pool lease satisfies it, and so does
// net.Dialer, which is the whole of AD-8's "variance in the interface, no
// flag inside the implementation".
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// ResolverFunc adapts a function to Resolver.
type ResolverFunc func(ctx context.Context, host string) ([]net.IP, error)

func (f ResolverFunc) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return f(ctx, host)
}

// DialerFunc adapts a function to Dialer.
type DialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f DialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// ProxyFunc decides the proxy for one request.
type ProxyFunc func(req *http.Request) (*url.URL, error)

// NewRoute composes a route from its three parts. A nil proxy means the
// route never proxies — which is what a tunnelled route wants: this
// machine's proxy configuration describes this machine's network, not the
// far side's.
func NewRoute(r Resolver, d Dialer, proxy ProxyFunc) Route {
	return route{Resolver: r, Dialer: d, proxy: proxy}
}

type route struct {
	Resolver
	Dialer
	proxy ProxyFunc
}

func (r route) ProxyForHTTPS(req *http.Request) (*url.URL, error) {
	if r.proxy == nil {
		return nil, nil
	}
	return r.proxy(req)
}

// Local is the route of an ordinary machine: the system resolver, a plain
// net.Dialer and the environment's proxy configuration. It is the behaviour
// the assistant had before the extraction, unchanged.
func Local() Route {
	return NewRoute(SystemResolver(), &net.Dialer{}, EnvironmentProxy)
}

// SystemResolver resolves with net.DefaultResolver.
func SystemResolver() Resolver {
	return ResolverFunc(func(ctx context.Context, host string) ([]net.IP, error) {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		ips := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
		return ips, nil
	})
}

// EnvironmentProxy reads the environment fresh per request rather than
// through http.ProxyFromEnvironment, which caches the parsed config ONCE per
// process — a cache that makes the guard's behavior depend on which request
// ran first (and on other tests' env). The env is static in production; a
// fresh read costs nothing.
func EnvironmentProxy(req *http.Request) (*url.URL, error) {
	cfg := httpproxy.FromEnvironment()
	return cfg.ProxyFunc()(req.URL)
}
