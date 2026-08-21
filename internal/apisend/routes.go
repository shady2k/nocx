package apisend

// The route TABLE: what turns an environment's route (design §6.5) into the
// route this sender dials through (§7.1).
//
// dialer.go declares the seam — `Routes` answers "which route is this?" for
// the RouteID on a Key — and says the table belongs to the environment
// store rather than to the sender. This file is that table, and it holds
// exactly two answers because an environment declares exactly two routes:
//
//	direct      → this machine, net.Dialer, the environment's proxy
//	connection  → a lease on the pooled SSH connection for a profile
//
// Everything else is REFUSED BY NAME. That refusal, not the two answers, is
// the reason this file exists: a request the user routed through a
// connection must never quietly go out of this machine's own interface,
// because the whole point of putting the route on the environment is that a
// production request cannot accidentally go out around its bastion (§6.5's
// third consequence). So there is no fallback anywhere below — not for an
// unknown id, not for an unwired pool, and not for a connection that has
// died.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/ssh"
)

// connectionRoutePrefix spells a connection route's id. The id is derived
// from the environment's route and from nothing else, so two environments
// naming one profile are ONE route — which is what lets their sends share a
// client instance and therefore a pooled connection (AD-7) — and two
// profiles are always two, so one connection's jar never reaches the other.
const connectionRoutePrefix = "connection:"

// ErrNoConnection — a "connection" route whose profile could not be leased.
// It is its own sentinel because it is its own sentence for the user: "that
// connection is not available" is not "the server refused" and not "you
// cancelled", and a surface that cannot tell them apart offers the wrong
// remedy.
//
// It is a REFUSAL, never a local dial. A send that fell back would put a
// production request on this machine's own interface, around the bastion the
// environment named.
var ErrNoConnection = errors.New(component + ": this environment routes through a connection that is not available")

// ErrNameResolvedRemotely — an http:// URL naming a HOST through a
// connection route.
//
// httppolicy.Route's contract is that LookupIP returns the addresses the
// dial WILL ACTUALLY REACH, and that a route which cannot answer truthfully
// must return an error rather than a guess. A tunnelled route cannot: the
// far side opens the direct-tcpip channel and the far side resolves the name
// (§7.1). Guessing with this machine's resolver would be worse than useless
// — the http:// address rule would be checked against an address in OUR
// network and the connection then made to a different one in THEIRS, so
// `http://api.internal` could pass the check here and reach a public host
// over there.
//
// An http:// URL naming an ADDRESS is a different question and is allowed:
// the far side resolves nothing, so this end can say exactly what will be
// reached. https:// is never asked at all — the policy checks addresses only
// for http:// — so the ordinary case of §6.5's own example, an https
// endpoint behind a bastion, is untouched by this.
var ErrNameResolvedRemotely = errors.New(component + ": a request routed through a connection cannot resolve a host name on this side; the far side resolves it")

// RouteIDFor turns an environment's route into the id that names it on a
// Key. It is the ONE place that mapping is written: a second spelling of it
// would be two derivations of one fact, agreeing until the day they did not.
//
// A route that does not say how to get there is refused here as well as in
// apicoll's own file validation, and deliberately so — the file check
// governs what is on disk, and this governs what a caller hands over, which
// includes a value built in memory by a surface that never read a file.
func RouteIDFor(r apicoll.Route) (string, error) {
	switch r.Kind {
	case apicoll.RouteDirect:
		if r.ProfileID != "" {
			return "", fmt.Errorf("%s: route is %q but names connection %q; a direct route goes out from this machine and names none",
				component, apicoll.RouteDirect, r.ProfileID)
		}
		// The empty id is the sender's own direct route (dialer.go), so a
		// direct environment and no environment at all are the same send.
		return "", nil
	case apicoll.RouteConnection:
		if r.ProfileID == "" {
			return "", fmt.Errorf("%s: route is %q but names no connection; a request routed through a connection must say which one",
				component, apicoll.RouteConnection)
		}
		return connectionRoutePrefix + r.ProfileID, nil
	case "":
		return "", fmt.Errorf("%s: the environment declares no route; it is refused rather than treated as %q, "+
			"because a request the user routed through a connection must never quietly go out of this machine",
			component, apicoll.RouteDirect)
	default:
		return "", fmt.Errorf("%s: unknown route kind %q", component, r.Kind)
	}
}

// ConnectionLeaser takes a lease on the pooled SSH connection a profile
// names. It is this package's own narrow contract rather than
// tunnel.Connector, because the two differ in exactly the part that matters
// here: a forward names a HOST and carries the connect options it resolved,
// while an environment names a PROFILE and knows nothing about hosts,
// credentials or jump routes. Resolving that is the composition root's, and
// this is the seam it hands over.
//
// The lease is a REFERENCE to a pooled connection, never an owned one
// (AD-7): the implementation shares whenever the resolved pool key matches
// and establishes one otherwise, so a send may authenticate anew rather than
// ride a connection visible in a tab. A route names a destination, not a
// window (§7.1).
type ConnectionLeaser interface {
	LeaseForProfile(ctx context.Context, profileID string) (ssh.TunnelConn, error)
}

// NewRoutes builds the table over the pool. A nil leaser is allowed and is
// honest: the direct route still works and every connection route refuses by
// name, which is the shape the app has whenever the SSH side is not wired.
//
// There is no dial-timeout option here on purpose. The bound on one remote
// channel open belongs to the adapter that cannot be interrupted by a
// context (ssh_dialer.go's defaultSSHDialTimeout), and a second way to spell
// it would be a second answer to one question.
func NewRoutes(leaser ConnectionLeaser) Routes {
	return (&routeTable{leaser: leaser, routes: map[string]Route{}}).route
}

// routeTable caches one route per id. The cache is what makes the lease
// per-ROUTE rather than per-send: the sender already caches a client
// instance per Key and asks for a route only on a miss, but the table is the
// owner of that promise rather than a beneficiary of the sender's caching.
type routeTable struct {
	leaser ConnectionLeaser

	mu     sync.Mutex
	routes map[string]Route
}

func (t *routeTable) route(_ context.Context, routeID string) (Route, error) {
	if routeID == "" {
		return httppolicy.Local(), nil
	}
	profileID, ok := strings.CutPrefix(routeID, connectionRoutePrefix)
	if !ok || profileID == "" {
		return nil, fmt.Errorf("%s: no route named %q — this sender knows the direct route and connection routes, and refuses anything else rather than sending from this machine",
			component, routeID)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if r, cached := t.routes[routeID]; cached {
		return r, nil
	}
	c := &connectionRoute{leaser: t.leaser, profileID: profileID}
	// Composed through httppolicy's own constructor rather than by
	// declaring a fourth Route implementation: one owner for what a Route
	// is. The proxy is nil deliberately — this machine's proxy
	// configuration describes this machine's network, not the far side's.
	r := httppolicy.NewRoute(httppolicy.ResolverFunc(c.lookupIP), httppolicy.DialerFunc(c.dial), nil)
	t.routes[routeID] = r
	return r, nil
}

// connectionRoute is one profile's route: it holds the lease and re-takes it
// when the connection it names has gone.
//
// The interval, both ends named, because a moment is not an invariant: a
// lease is held from the first dial that needed one until the connection it
// references shuts down — Done closes, and ssh.tunnelConn's own watcher has
// already released the pool reference by then — and from that moment the
// next dial takes a NEW lease rather than dialling a dead channel. Without
// the second end, one lost connection would leave this route refusing every
// send for the rest of the process.
type connectionRoute struct {
	leaser    ConnectionLeaser
	profileID string

	// mu guards the lease and IS held across the acquisition, which is a
	// remote connect. That is deliberate and its scope is one route: two
	// concurrent sends through one environment must not race into two SSH
	// connections, which is the thing AD-7 is about. Sends on any other
	// route, and everything else in the app, are untouched by it.
	mu    sync.Mutex
	lease ssh.TunnelConn
}

// lookupIP answers only what this end can answer truthfully. See
// ErrNameResolvedRemotely.
//
// LOOPBACK NAMES ARE THE ONE EXCEPTION, and it is the case this whole
// feature exists for: `http://localhost:9443` through a connection means the
// service on the FAR SIDE's loopback, which is how anybody reaches an
// internal admin port, a dev server or a database console on a box they have
// SSH to. Refusing it made the headline feature useless for the request
// people most want to make with it.
//
// It is truthful, which is the property ErrNameResolvedRemotely protects.
// `localhost` is the one name whose answer does not depend on WHO resolves it
// — RFC 6761 §6.3 requires it and `*.localhost` to resolve to loopback
// everywhere — so this end can say what will be reached without knowing the
// far side's resolver. The dial then goes to that address ON THAT HOST: the
// direct-tcpip channel names 127.0.0.1 and the far side's own stack answers
// it (§7.1).
//
// And the http:// rule is satisfied for the reason it exists rather than by
// an exemption from it. That rule refuses plaintext across a network
// (httppolicy: "http:// is permitted only for loopback and private
// addresses"). Here the plaintext rides the SSH channel to the far side and
// terminates on that host's own loopback, so it crosses no network in the
// clear at either end — a stronger guarantee than the local case the rule
// was written for, not a weaker one.
//
// Every OTHER name is still refused. `http://api.internal` through a bastion
// means the bastion talking plaintext to api.internal across their LAN,
// which is the thing the rule is about, and this end cannot resolve it
// truthfully anyway.
func (c *connectionRoute) lookupIP(_ context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if isLoopbackName(host) {
		// IPv4 first: the dialer tries these in order, and a far side with
		// no IPv6 is the ordinary case.
		return []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, nil
	}
	return nil, fmt.Errorf("%w (host %q, connection %q)", ErrNameResolvedRemotely, host, c.profileID)
}

// isLoopbackName reports whether host is a name that resolves to loopback
// wherever it is resolved: `localhost` and anything under `.localhost`, which
// RFC 6761 §6.3 reserves for exactly that. A trailing dot is the same name
// fully qualified. Nothing else qualifies — `127.0.0.1.nip.io` resolves to
// loopback today and is a name somebody else controls tomorrow.
func isLoopbackName(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	return name == "localhost" || strings.HasSuffix(name, ".localhost")
}

func (c *connectionRoute) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	lease, err := c.currentLease(ctx)
	if err != nil {
		return nil, err
	}
	// Zero means the adapter's own bound, which is the one owner of how long
	// a remote channel open may take (ssh_dialer.go).
	return NewSSHDialer(lease, 0).DialContext(ctx, network, addr)
}

// currentLease returns a lease on a live connection, taking one if the route
// holds none or holds one whose connection has gone.
func (c *connectionRoute) currentLease(ctx context.Context) (ssh.TunnelConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease != nil && !gone(c.lease) {
		return c.lease, nil
	}
	if c.lease != nil {
		// The watcher has already released this one on loss; Close is
		// idempotent (ssh.tunnelConn.releaseOnce) and saying so here is
		// what keeps the reference count right if it ever is not.
		_ = c.lease.Close()
		c.lease = nil
	}
	if c.leaser == nil {
		return nil, fmt.Errorf("%w: connection %q: no SSH pool is wired into this sender", ErrNoConnection, c.profileID)
	}
	lease, err := c.leaser.LeaseForProfile(ctx, c.profileID)
	if err != nil {
		return nil, fmt.Errorf("%w: connection %q: %w", ErrNoConnection, c.profileID, err)
	}
	if lease == nil {
		return nil, fmt.Errorf("%w: connection %q: the pool answered with no lease", ErrNoConnection, c.profileID)
	}
	c.lease = lease
	return lease, nil
}

// gone reports whether the connection a lease references has shut down. It
// never blocks: Done is closed on loss and on server close, and a lease that
// is still live simply falls through.
func gone(lease ssh.TunnelConn) bool {
	select {
	case <-lease.Done():
		return true
	default:
		return false
	}
}
