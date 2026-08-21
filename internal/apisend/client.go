package apisend

// One client instance per Key, immutable once built. A single shared mutable
// client cannot hold a per-environment cookie jar and a per-call dialer at
// the same time without leaking one environment's cookies — or one
// environment's route — into another environment's request. So the instance
// is the thing that varies, it is keyed by exactly what varies, and nothing
// reaches into it afterwards.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
)

// component prefixes every policy refusal this package raises, so a message
// names the sender rather than the policy package the user has never heard
// of.
const component = "apisend"

// Key is what a client instance is keyed by: the route it dials through and
// the scope its cookies belong to.
type Key struct {
	RouteID     string
	CookieScope string
}

// Client is the sender. It is safe for concurrent use.
type Client struct {
	routes    Routes
	limit     int64
	tlsConfig *tls.Config
	log       log.Logger

	// mu guards the instance table ONLY. It is held for a map lookup and a
	// map insert and is never held across a dial, a resolve or a request:
	// a global gate behind arbitrary remote latency would block every other
	// send, and everything else that shares it.
	mu        sync.Mutex
	instances map[Key]*http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithRoutes supplies the route table. Without it the sender knows only the
// direct route (the empty RouteID).
func WithRoutes(r Routes) Option {
	return func(c *Client) {
		if r != nil {
			c.routes = r
		}
	}
}

// WithMaxBytes lowers the ceiling on what this package puts on the control
// plane — the captured body and the raw text of both sides, which are the
// same question asked twice and so take the same answer (§12.3). It can
// only lower it: a value above the inherited 2 MiB, or at or below zero,
// means the ceiling.
func WithMaxBytes(n int64) Option { return func(c *Client) { c.limit = n } }

// WithTLSClientConfig supplies the TLS trust and parameters for every
// instance this client builds — an internal CA, or a test server's own
// certificate. Nil means Go's defaults, which is the product's normal case.
func WithTLSClientConfig(cfg *tls.Config) Option { return func(c *Client) { c.tlsConfig = cfg } }

// WithLogger supplies the logger the policy warns through. Nil is allowed.
func WithLogger(l log.Logger) Option { return func(c *Client) { c.log = l } }

// New builds a sender. With no options it sends from this machine, bounded
// at the inherited 2 MiB ceiling.
func New(opts ...Option) *Client {
	c := &Client{
		routes:    directRoutes,
		limit:     ceiling,
		instances: make(map[Key]*http.Client),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// instanceFor returns the cached client for k, building one if there is
// none. Two callers racing on a cold key both build; the first to store
// wins and the loser's client is discarded unused — it has opened nothing,
// so discarding it costs a transport struct and never a connection.
func (c *Client) instanceFor(ctx context.Context, k Key) (*http.Client, error) {
	c.mu.Lock()
	in, ok := c.instances[k]
	c.mu.Unlock()
	if ok {
		return in, nil
	}

	route, err := c.routes(ctx, k.RouteID)
	if err != nil {
		return nil, err
	}
	if route == nil {
		return nil, fmt.Errorf("%s: route %q resolved to nothing", component, k.RouteID)
	}
	// The jar is per instance, which is per CookieScope: that is the whole
	// reason the scope is in the key. jar.go carries the rest, including
	// why it does not survive a restart.
	jar, err := newJar(k.CookieScope)
	if err != nil {
		return nil, err
	}
	tr := httppolicy.NewTransport(httppolicy.Params{
		Component:       component,
		Route:           tracingRoute{Route: route},
		Log:             c.log,
		TLSClientConfig: c.tlsConfig,
	})
	built := &http.Client{Transport: tr, CheckRedirect: tr.CheckRedirect, Jar: jar}

	c.mu.Lock()
	defer c.mu.Unlock()
	if in, ok := c.instances[k]; ok {
		return in, nil
	}
	c.instances[k] = built
	return built, nil
}

// tracingRoute times the two phases httptrace cannot see for every route.
// The policy resolves the name itself for http:// and then dials an address
// literal, so net.Dialer never performs the lookup httptrace's DNS hooks
// report; and a route that dials on the far side is not net.Dialer at all,
// so its connect time exists nowhere else. Both are reported into the tracer
// carried on the request context, which is the one channel that reaches from
// the send into the dial.
type tracingRoute struct {
	Route
}

func (r tracingRoute) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	start := time.Now()
	ips, err := r.Route.LookupIP(ctx, host)
	traceFrom(ctx).setDNS(time.Since(start))
	return ips, err
}

func (r tracingRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	start := time.Now()
	conn, err := r.Route.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	traceFrom(ctx).setConnect(time.Since(start))
	return conn, nil
}
