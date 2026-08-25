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
	// InsecureTLS sends this exchange WITHOUT verifying the server's
	// certificate — the environment's own choice (§6.5), for a development
	// host with a self-signed certificate, which is the case every API
	// client in the world has a switch for.
	//
	// It is in the KEY and not a field on the Client, and that is the whole
	// safety of it: instances are cached by this key, so a transport that
	// verifies and one that does not are two different transports and no
	// connection is ever reused across them. Without it, one environment
	// turning verification off would hand its pooled connection to the next
	// send from an environment that had not.
	InsecureTLS bool
}

// Client is the sender. It is safe for concurrent use.
//
// limit and tlsConfig have NO Option. Both had one and nothing in the
// product ever set either: the 2 MiB ceiling is the only ceiling any surface
// offers, and Go's default trust is the only trust. An option nothing calls
// is a second way to spell a constant, so the options went and the fields
// stayed — the ceiling is still read on every send, and the config still
// reaches every transport this builds. The tests set them directly (see
// client_test.go), which is what makes a 30-byte truncation and a test
// server's own certificate reachable without offering the product a knob it
// has no surface for.
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
		TLSClientConfig: c.tlsFor(k),
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

// tlsFor is the TLS configuration this key's transport gets: the client's
// own, and a copy of it with verification off when the environment asked for
// that.
//
// A COPY, never a mutation: c.tlsConfig is shared by every other instance,
// and turning verification off in place would turn it off for all of them —
// the exact defect the key exists to prevent, moved one field deeper.
func (c *Client) tlsFor(k Key) *tls.Config {
	if !k.InsecureTLS {
		return c.tlsConfig
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // InsecureSkipVerify is set below, deliberately and per environment
	if c.tlsConfig != nil {
		cfg = c.tlsConfig.Clone()
	}
	cfg.InsecureSkipVerify = true //nolint:gosec // the environment declares it; every run that used it says so
	return cfg
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

// It also reports WHICH STEP FAILED, which is the other thing only this
// wrapper knows. The policy resolves an http:// name here and then dials an
// address literal, so a resolve failure is not a *net.DNSError by the time
// the caller sees it, and a route that dials on the far side produces
// whatever that side's error type happens to be. Classifying the phase from
// the error alone would therefore be guesswork exactly where the route is
// least ordinary; the step that failed is a fact, and this records it.
func (r tracingRoute) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	start := time.Now()
	ips, err := r.Route.LookupIP(ctx, host)
	tr := traceFrom(ctx)
	tr.setDNS(time.Since(start))
	if err != nil {
		tr.failedResolve()
	}
	return ips, err
}

func (r tracingRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	start := time.Now()
	conn, err := r.Route.DialContext(ctx, network, addr)
	if err != nil {
		traceFrom(ctx).failedDial()
		return nil, err
	}
	tr := traceFrom(ctx)
	tr.setConnect(time.Since(start))
	// THE ADDRESS IS RECORDED AT THE DIAL, not only at httptrace's GotConn.
	// GotConn fires when the connection is ready for a REQUEST, which on an
	// https:// exchange is after the handshake — so a run that reached a
	// server and was refused by its certificate would report having reached
	// nothing, which is the opposite of what a person needs to read off it.
	if conn != nil && conn.RemoteAddr() != nil {
		tr.setRemote(conn.RemoteAddr().String())
	}
	return conn, nil
}
