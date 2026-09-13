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
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// PooledSpec is one connection to acquire: a destination the CALLER has
// already resolved, the identity component of the pool key, and the client
// configuration to dial with.
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
	// KeepaliveInterval and KeepaliveCountMax arm the same prober the
	// coordinator's own connections carry, so a pooled connection that has
	// gone quiet is noticed on this side of the wire too. Zero disables it.
	KeepaliveInterval time.Duration
	KeepaliveCountMax int
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
	client  *gossh.Client
	release func()
	once    sync.Once
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
// The dial runs through the same `dialDirect` the coordinator's own path uses,
// so the context-aware handshake watchdog, the classification of an
// authentication refusal and the known_hosts-free configuration all behave
// identically — one implementation, two callers, which is the whole point of
// the seam rather than a second dialer beside the first.
func (rc *RealClient) AcquirePooled(ctx context.Context, spec PooledSpec) (*PooledConn, error) {
	switch {
	case spec.Host == "":
		return nil, fmt.Errorf("ssh: pooled connection: no host")
	case spec.Port <= 0 || spec.Port > 65535:
		return nil, fmt.Errorf("ssh: pooled connection: port %d", spec.Port)
	case spec.User == "":
		return nil, fmt.Errorf("ssh: pooled connection: no user")
	case spec.Config == nil:
		return nil, fmt.Errorf("ssh: pooled connection: no client configuration")
	}

	key := poolKey{
		host:     spec.Host,
		port:     spec.Port,
		user:     spec.User,
		identity: spec.Identity,
	}
	addr := net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port))

	dial := func(poolKey) (sshClientConn, error) {
		gclient, err := (&dialer{client: rc}).dialDirect(ctx, addr, spec.Config, spec.Host, spec.User)
		if err != nil {
			return nil, err
		}
		pconn := &pooledSSHConn{client: gclient}
		stopKA, _ := startKeepalive(pconn, spec.KeepaliveInterval, spec.KeepaliveCountMax, nil)
		pconn.setKeepaliveStop(stopKA)
		return pconn, nil
	}

	handle, err := rc.pool.AcquireDial(ctx, key, dial)
	if err != nil {
		return nil, err
	}
	client, ok := handle.conn.(*pooledSSHConn)
	if !ok {
		// A pool entry that is not this package's own wrapper cannot be
		// handed out as a *gossh.Client, and guessing at it would be a
		// type assertion standing in for a guarantee.
		rc.pool.Release(handle)
		return nil, fmt.Errorf("ssh: pooled connection: unexpected connection type %T", handle.conn)
	}
	gclient, ok := client.client.(*gossh.Client)
	if !ok {
		rc.pool.Release(handle)
		return nil, fmt.Errorf("ssh: pooled connection: unexpected client type %T", client.client)
	}
	return &PooledConn{
		client:  gclient,
		release: func() { rc.pool.Release(handle) },
	}, nil
}
