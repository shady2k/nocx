//go:build nocx_local_ssh

package ssh

// The pool as a seam for a caller that is NOT this package's dial path: the
// helper that dials on this machine's behalf.
//
// # Why this exists, given FSConn/TunnelConn/DiscoveryConn already exist
//
// Those leases are the COORDINATOR's: each builds a ConnectConfig out of
// ConnectOptions, resolves it through ~/.ssh/config, enforces credential
// authorization against the resolved endpoint, and hands back a purpose-shaped
// capability. A helper can do none of that — it reads no config, holds no
// credential and must not decide what a credential is authorized for — and the
// caller that CAN do it is the coordinator, which resolves the destination and
// then asks the helper to dial exactly that.
//
// So what the helper needs from this package is the part that is genuinely
// about holding connections: AD-4's ref-counted pool, keyed by
// host+port+user+identity, with the dial performed once and every channel
// multiplexing over it. That is what AcquirePooled answers, and it is
// deliberately the smallest thing that can: it does not resolve, does not
// authorize and does not classify — it takes a resolved address and a
// caller-built *gossh.ClientConfig and gives back a borrowed connection.
//
// # What it is not
//
// It is not a second way for the COORDINATOR to dial. Nothing in this
// repository's coordinator path calls it: the consumers reach FSConn,
// TunnelConn and their siblings, and each of those is being re-pointed at the
// helper's proxied channels rather than at this (epic nocx-50w7p, plan §3).
// This is the pool the helper's `ssh` service acquires from, which is why the
// key derivation below mirrors poolKeyFor and NOT a new one.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// PooledSpec is one connection to acquire: a destination the CALLER has
// already resolved, the route it is reached through, the identity component of
// the pool key, and the client configuration to dial with.
//
// Host, Port and User are resolved values — an address and an account, never
// an alias — for the reason proto.ProbeParams states: alias resolution and
// ~/.ssh/config merging stay in the coordinator, which is the party that reads
// the config.
type PooledSpec struct {
	// Host is the resolved dial target.
	Host string
	// Port is the resolved port (1-65535).
	Port int
	// User is the resolved account.
	User string
	// Identity is the pool key's credential component: what tells two
	// principals apart on one host+port+user. It is the caller's value and
	// never a derivation of the address — a pool keyed without it would let
	// one credential's transport carry another's traffic, which is the
	// authorization hole AD-4's key exists to close.
	Identity string
	// Config is the client configuration to dial with, complete: its Auth and
	// its HostKeyCallback are the caller's, because the caller is the party
	// that can answer the handshake's two questions (the helper asks the
	// coordinator over the reverse channel and builds this from the answers).
	Config *gossh.ClientConfig
	// Route is the hosts this destination is reached THROUGH, in dial order:
	// Route[0] is dialed from this machine, each next hop through the one
	// before it, and this spec's own destination through the last. Empty for a
	// direct connection.
	//
	// Each hop carries its own resolved address, its own identity and its own
	// client configuration, because a route is a chain of independent
	// handshakes: the hop's credential and the hop's host key are the
	// coordinator's answers, one hop at a time, exactly as they are for the
	// destination.
	Route []PooledHop
	// KeepaliveInterval and KeepaliveCountMax arm the same prober the
	// coordinator's own connections carry, so a pooled connection that has
	// gone quiet is noticed on this side of the wire too. Zero disables it.
	KeepaliveInterval time.Duration
	KeepaliveCountMax int
	// Liveness receives what the prober armed by KeepaliveInterval learns,
	// exactly as ConnectConfig.Liveness does for the coordinator's own
	// non-pooled dials (nocx-y6fh7 item 6: sshsvc's shell channel is the
	// first pooled caller to actually want one — every other caller of
	// AcquirePooled still passes nil, unchanged). Read only on a cache MISS,
	// along with the interval and the count: a caller that joins an
	// already-dialed pooled connection joins whatever prober (or none) the
	// first caller armed, because there is one connection and therefore one
	// prober, not one per reference.
	Liveness LivenessObserver
}

// PooledHop is one intermediate host a connection is dialed through. It is the
// same four facts the destination is — address, account, pool identity, client
// configuration — because a hop authenticates and is verified exactly as the
// destination is; only its POOLING differs, and that is this package's decision
// rather than the caller's.
type PooledHop struct {
	Host     string
	Port     int
	User     string
	Identity string
	Config   *gossh.ClientConfig
}

// routeHop is one endpoint this package is about to dial: a PooledHop and the
// destination share this shape, and it exists so the dialing code has one
// parameter type rather than two that agree by convention.
type routeHop struct {
	host     string
	port     int
	user     string
	identity string
	config   *gossh.ClientConfig
}

func (h PooledHop) routeHop() routeHop {
	return routeHop{host: h.Host, port: h.Port, user: h.User, identity: h.Identity, config: h.Config}
}

// addr is the dial address of one endpoint.
func (h routeHop) addr() string { return net.JoinHostPort(h.host, strconv.Itoa(h.port)) }

// routeKeyOf renders an ordered route into the pool key's route component.
//
// It is built out of poolKey.jumpRouteKey — the same rendering the
// coordinator's own pool keys use for their bastion — joined the same way the
// coordinator joins a multi-hop chain, so the two pools identify a route by the
// same string and a connection cannot be shared between two different routes.
// An empty route renders empty, which is how a direct connection is spelled.
func routeKeyOf(route []PooledHop) string {
	if len(route) == 0 {
		return ""
	}
	parts := make([]string, 0, len(route))
	for _, hop := range route {
		parts = append(parts, poolKey{
			host: hop.Host, port: hop.Port, user: hop.User, identity: hop.Identity,
		}.jumpRouteKey())
	}
	return strings.Join(parts, ">")
}

// PooledConn is a borrowed reference to a pooled connection: the caller may
// open channels on Client() for as long as it holds this, and MUST call Close
// when it is done — the connection itself closes when the last reference is
// released (AD-4).
//
// Client() hands out the RAW *gossh.Client on purpose. This seam exists for a
// caller whose whole job is to open channels of kinds this package has no
// lease for (a subsystem stream today, a forward later); wrapping each kind in
// its own interface here would put the helper's channel vocabulary inside the
// coordinator's ssh package, which is the direction AD-8 forbids.
type PooledConn struct {
	client      *gossh.Client
	fingerprint string
	release     func()
	once        sync.Once

	// taint and taintReason bridge to the owning pool's own bookkeeping
	// (ConnPool.Taint / poolHandle.closeReason, spec §5.7, nocx-6q1uh.3).
	// Both are nil for a connection dialed outside the pool (DialAuth):
	// there is no pool entry to taint, and Taint is then a documented no-op
	// rather than a caller error — a probe never carries a writer this
	// mechanism exists for.
	taint       func()
	taintReason func() string
}

// Taint marks this borrowed connection so the pool hands it to no new
// caller again (spec §5.7's first detach): existing siblings — including
// this reference — run to their own end, and it closes when the last of
// them releases, or at once past the helper's detached-writer cap. A no-op
// for a connection this package dialed outside the pool.
func (p *PooledConn) Taint() {
	if p == nil || p.taint == nil {
		return
	}
	p.taint()
}

// TaintReason is the name this connection was closed under by the pool's
// cap (ReasonDetachedWriterCap), or "" otherwise — read by a sibling channel
// to report why ITS OWN session ended (spec §5.7: "the sibling sessions on
// that connection report that reason on exit").
func (p *PooledConn) TaintReason() string {
	if p == nil || p.taintReason == nil {
		return ""
	}
	return p.taintReason()
}

// Fingerprint is the SHA256 fingerprint of the TARGET host's public key as
// presented and verified when this connection was dialed.
//
// It is carried on the reference rather than looked up, because the party that
// reads it is one process away and cannot enumerate the pool: the helper's
// `lease` op answers it, and the coordinator's install path keys CONSENT by it
// (ADR-0023 — the host key is the machine, where a route name is only a proxy
// for one). A connection whose key was never established reports the empty
// string, and the helper refuses an empty one rather than handing out a lease
// whose identity nobody can state.
func (p *PooledConn) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.fingerprint
}

// Client is the underlying connection.
func (p *PooledConn) Client() *gossh.Client { return p.client }

// Close releases this reference. The connection stays open for every other
// reference; it closes when the last one goes. Idempotent.
func (p *PooledConn) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		if p.release != nil {
			p.release()
		}
	})
	return nil
}

// AcquirePooled returns a reference to the pooled connection for a resolved
// spec, dialing it if no live connection with the same key exists.
//
// The dial runs through the same dialer the coordinator's own path uses, so the
// context-aware handshake watchdog, the classification of an authentication
// refusal and the known_hosts-free configuration all behave identically — one
// implementation, two callers, which is the whole point of the seam rather than
// a second dialer beside the first.
//
// A ROUTED spec dials its hops the way the coordinator's own jump path does:
// each hop is itself acquired from this same pool (AD-4), so two destinations
// behind one bastion share that bastion's authenticated connection, and the
// bastion's reference is released when the last connection through it is. The
// destination's key is what the pool entry carries — a hop's own fingerprint is
// not what anybody keys consent by (ADR-0023 keys it by the machine a person
// is connecting TO).
func (rc *RealClient) AcquirePooled(ctx context.Context, spec PooledSpec) (*PooledConn, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}

	key := poolKey{
		host:      spec.Host,
		port:      spec.Port,
		user:      spec.User,
		identity:  spec.Identity,
		jumpRoute: routeKeyOf(spec.Route),
	}

	dial := func(poolKey) (sshClientConn, error) {
		// The TARGET host's fingerprint is captured here, at the one place a
		// helper's dial passes through, because the callback that sees the key
		// is the caller's own (the helper's asks the coordinator what to make
		// of it) and the pool entry is what must carry the answer: a LATER
		// acquisition is a cache hit and runs no handshake at all, so a
		// fingerprint remembered per-acquire would be empty for every lease
		// but the first — and the install path keys consent by it (ADR-0023).
		//
		// The HOPS are deliberately not captured: their keys are verified by
		// the coordinator through the same ask, but the identity a lease
		// reports is the destination's, which is the machine the consent
		// decision is about.
		var fingerprint string
		cfg := *spec.Config
		inner := cfg.HostKeyCallback
		cfg.HostKeyCallback = func(hostname string, remote net.Addr, key gossh.PublicKey) error {
			fingerprint = gossh.FingerprintSHA256(key)
			if inner == nil {
				return errors.New("ssh: pooled connection: no host-key callback")
			}
			return inner(hostname, remote, key)
		}
		conn, err := rc.dialRoute(ctx, spec.Route, routeHop{
			host: spec.Host, port: spec.Port, user: spec.User, identity: spec.Identity, config: &cfg,
		})
		if err != nil {
			return nil, err
		}
		conn.fingerprint = fingerprint
		stopKA, _ := startKeepalive(conn, spec.KeepaliveInterval, spec.KeepaliveCountMax, spec.Liveness)
		conn.setKeepaliveStop(stopKA)
		return conn, nil
	}

	handle, err := rc.dial.pool.AcquireDial(ctx, key, dial)
	if err != nil {
		return nil, err
	}
	return rc.borrowPooled(handle)
}

// DialAuth dials a resolved spec ONCE and hands back the connection, without
// touching the pool: the caller owns the whole chain — the hops and the
// destination alike — and closing it closes all of them.
//
// It is what a PROBE rides. A probe answers "does this credential work here",
// it half-applies nothing, and the connection it left behind would be one whose
// lifetime nothing owns; that is why the coordinator's own probe path bypasses
// its pool too, and why this is a method of its own rather than a mode of
// AcquirePooled. A routed probe dials each hop for itself and closes it, which
// is the honest reading of "leaves nothing behind" — including the bastion.
//
// The caller must Close what it is given, as with every PooledConn.
func (rc *RealClient) DialAuth(ctx context.Context, spec PooledSpec) (*PooledConn, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	var fingerprint string
	cfg := *spec.Config
	inner := cfg.HostKeyCallback
	cfg.HostKeyCallback = func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		fingerprint = gossh.FingerprintSHA256(key)
		if inner == nil {
			return errors.New("ssh: probe: no host-key callback")
		}
		return inner(hostname, remote, key)
	}
	client, release, err := rc.dialRouteOnce(ctx, spec.Route, routeHop{
		host: spec.Host, port: spec.Port, user: spec.User, identity: spec.Identity, config: &cfg,
	})
	if err != nil {
		return nil, err
	}
	return &PooledConn{
		client:      client,
		fingerprint: fingerprint,
		release:     release,
	}, nil
}

// validate refuses a spec that cannot be dialed, before anything is dialed. The
// destination and every hop are checked the same way: a hop that is missing an
// address is a route the caller built wrongly, and discovering that at the
// second handshake would mean a connection to nowhere whose failure names the
// wrong thing.
func (s PooledSpec) validate() error {
	if err := validateEndpoint(s.Host, s.Port, s.User, s.Config); err != nil {
		return err
	}
	for i, hop := range s.Route {
		if err := validateEndpoint(hop.Host, hop.Port, hop.User, hop.Config); err != nil {
			return fmt.Errorf("route hop %d: %w", i, err)
		}
	}
	return nil
}

func validateEndpoint(host string, port int, user string, cfg *gossh.ClientConfig) error {
	switch {
	case host == "":
		return fmt.Errorf("ssh: pooled connection: no host")
	case port <= 0 || port > 65535:
		return fmt.Errorf("ssh: pooled connection: port %d", port)
	case user == "":
		return fmt.Errorf("ssh: pooled connection: no user")
	case cfg == nil:
		return fmt.Errorf("ssh: pooled connection: no client configuration")
	}
	return nil
}

// dialRoute answers a pooled reference to an endpoint reached through route,
// acquiring each hop from this same pool.
//
// The recursion is the structure rather than an accident: the last hop is
// acquired with its own pool key and its dial, in turn, acquires the hop before
// it, so the chain is pooled at every level and a second destination through
// the same bastion finds it already there. The reference the destination holds
// releases the hop's, which releases the one before it — the same ownership
// chain the coordinator's own jump dial keeps, one caller out.
func (rc *RealClient) dialRoute(ctx context.Context, route []PooledHop, last routeHop) (*pooledSSHConn, error) {
	var upstream *PooledConn
	if len(route) > 0 {
		var err error
		upstream, err = rc.acquireHop(ctx, route)
		if err != nil {
			return nil, err
		}
	}
	var upClient *gossh.Client
	if upstream != nil {
		upClient = upstream.Client()
	}
	client, err := rc.dialEndpoint(ctx, upClient, last)
	if err != nil {
		if upstream != nil {
			_ = upstream.Close()
		}
		return nil, err
	}
	var release func()
	if upstream != nil {
		release = func() { _ = upstream.Close() }
	}
	return &pooledSSHConn{client: client, release: release}, nil
}

// acquireHop answers a pooled reference to the LAST hop of route.
func (rc *RealClient) acquireHop(ctx context.Context, route []PooledHop) (*PooledConn, error) {
	hop := route[len(route)-1]
	hopEndpoint := hop.routeHop()
	key := poolKey{
		host:      hop.Host,
		port:      hop.Port,
		user:      hop.User,
		identity:  hop.Identity,
		jumpRoute: routeKeyOf(route[:len(route)-1]),
	}
	dial := func(poolKey) (sshClientConn, error) {
		return rc.dialRoute(ctx, route[:len(route)-1], hopEndpoint)
	}
	handle, err := rc.dial.pool.AcquireDial(ctx, key, dial)
	if err != nil {
		return nil, err
	}
	return rc.borrowPooled(handle)
}

// dialRouteOnce dials an endpoint through route WITHOUT pooling anything, and
// answers a client whose release closes the destination and every hop under it.
func (rc *RealClient) dialRouteOnce(ctx context.Context, route []PooledHop, last routeHop) (*gossh.Client, func(), error) {
	if len(route) == 0 {
		client, err := rc.dialEndpoint(ctx, nil, last)
		if err != nil {
			return nil, nil, err
		}
		return client, func() { _ = client.Close() }, nil
	}
	upClient, closeUpstream, err := rc.dialRouteOnce(ctx, route[:len(route)-1], route[len(route)-1].routeHop())
	if err != nil {
		return nil, nil, err
	}
	client, err := rc.dialEndpoint(ctx, upClient, last)
	if err != nil {
		closeUpstream()
		return nil, nil, err
	}
	return client, func() {
		_ = client.Close()
		closeUpstream()
	}, nil
}

// dialEndpoint performs the TCP dial and the handshake for ONE endpoint: to the
// address directly when there is no upstream, and through the network of the
// connection before it when there is.
func (rc *RealClient) dialEndpoint(ctx context.Context, upstream *gossh.Client, ep routeHop) (*gossh.Client, error) {
	d := &dialer{client: rc}
	if upstream == nil {
		return d.dialDirect(ctx, ep.addr(), ep.config, ep.host, ep.user)
	}
	conn, err := upstream.DialContext(ctx, "tcp", ep.addr())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dial %s through the host before it: %w", ep.addr(), err)
	}
	client, err := d.handshakeOver(ctx, conn, ep.addr(), ep.config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ssh client conn through a hop: %w", err)
	}
	return client, nil
}

// borrowPooled wraps a pool handle as the borrowed reference callers hold. A
// pool entry that is not this package's own wrapper cannot be handed out as a
// *gossh.Client, and guessing at it would be a type assertion standing in for a
// guarantee.
func (rc *RealClient) borrowPooled(handle *poolHandle) (*PooledConn, error) {
	client, ok := handle.conn.(*pooledSSHConn)
	if !ok {
		rc.dial.pool.Release(handle)
		return nil, fmt.Errorf("ssh: pooled connection: unexpected connection type %T", handle.conn)
	}
	gclient, ok := client.client.(*gossh.Client)
	if !ok {
		rc.dial.pool.Release(handle)
		return nil, fmt.Errorf("ssh: pooled connection: unexpected client type %T", client.client)
	}
	return &PooledConn{
		client:      gclient,
		fingerprint: client.HostKeyFingerprint(),
		release:     func() { rc.dial.pool.Release(handle) },
		taint:       func() { rc.dial.pool.Taint(handle) },
		taintReason: handle.closeReason,
	}, nil
}
