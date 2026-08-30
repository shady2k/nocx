package ssh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
	agent "golang.org/x/crypto/ssh/agent"
)

// sshClientConn is the subset of *gossh.Client we need: Close. The real
// *gossh.Client satisfies this; tests inject a fake.
type sshClientConn interface {
	Close() error
}

// pooledSSHConn wraps a *gossh.Client (or test fake) with an optional
// release hook for the jump transport. A target dialed via a bastion holds
// a poolHandle on the bastion's own entry; when the last target through
// that bastion closes, this conn's Close releases the bastion handle,
// which in turn closes the bastion when its own refcount hits zero. Close
// is idempotent (sync.Once) so a target whose session errors and is then
// released by tab-close cannot double-release the bastion.
type pooledSSHConn struct {
	client    sshClientConn
	release   func() // releases the jump handle, nil for direct conns
	closeOnce sync.Once

	// mu guards stopKeepalive, which is written after construction (see
	// setKeepaliveStop) and read by Close from another goroutine.
	mu sync.Mutex
	// stopKeepalive cancels the keepalive goroutine when non-nil. Set by
	// the dial factory when KeepaliveInterval > 0; called from Close before
	// closing the transport so the ticker stops before the connection goes
	// away (proved in TestKeepaliveTickerStopsOnClose). Nil when keepalive is
	// disabled or this is a test fake.
	stopKeepalive func()
	// dead records that this connection has been closed, so the pool can
	// refuse to hand it to anyone else. See isDead.
	dead atomic.Bool

	// agentForwardOnce guards agent.ForwardToRemote so it is called exactly
	// once per pooled connection, even when multiple tabs share the client.
	agentForwardOnce sync.Once

	// fingerprint is the SHA256 fingerprint of the TARGET host's public
	// key, as presented and verified when this connection was dialed (the
	// consent design keys consent by it — ADR-0023's route identity was a
	// proxy for the machine, and the host key is the machine). For a jump
	// route this is the target's key, never the bastion's. Empty for test
	// fakes that bypass the dial callback.
	fingerprint string
}

// HostKeyFingerprint returns the target host's public-key fingerprint
// observed at dial time.
func (c *pooledSSHConn) HostKeyFingerprint() string { return c.fingerprint }

// initAgentForward registers the auth-agent@openssh.com channel handler on
// the underlying *gossh.Client exactly once per pooled connection. It is a
// no-op (nil error) when addr is empty or the handler was already registered
// by a previous tab sharing this connection. addr should be the local SSH
// agent socket path (os.Getenv("SSH_AUTH_SOCK")).
func (c *pooledSSHConn) initAgentForward(gclient *gossh.Client, addr string) error {
	if addr == "" {
		return nil
	}
	var setupErr error
	c.agentForwardOnce.Do(func() {
		setupErr = agent.ForwardToRemote(gclient, addr)
	})
	return setupErr
}

func (c *pooledSSHConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Marked BEFORE anything is torn down, so the interval "this
		// connection may still be handed to a caller" closes at the first
		// instant it stops being true rather than at the last. A closed
		// transport that still reads as live for the length of its own
		// teardown is exactly the window a new tab falls into.
		c.dead.Store(true)
		c.mu.Lock()
		stop := c.stopKeepalive
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
		err = c.client.Close()
		if c.release != nil {
			c.release()
		}
	})
	return err
}

// globalRequester is the part of a transport that can carry an SSH global
// request. *gossh.Client has it; a test fake standing in for a connection
// need not.
type globalRequester interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

// SendRequest forwards a global request to the underlying transport, which is
// what makes the POOLED connection — rather than the raw client inside it —
// the thing the keepalive prober both probes and closes. One object, one
// owner: the prober cannot reach past the wrapper to close a transport whose
// teardown the wrapper is responsible for (AD-4).
func (c *pooledSSHConn) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	r, ok := c.client.(globalRequester)
	if !ok {
		return false, nil, errNoGlobalRequests
	}
	return r.SendRequest(name, wantReply, payload)
}

// errNoGlobalRequests is what a connection whose transport cannot carry a
// global request answers a probe with. It reads as a failed probe, which for
// a transport that cannot be probed is the honest answer.
var errNoGlobalRequests = errors.New("ssh: transport carries no global requests")

// setKeepaliveStop arms the prober's cancel after the connection exists. The
// prober is started with this connection as the thing it closes when it gives
// up, so the two are constructed in that order and the field is written after
// the goroutine is already running — under the mutex, because Close reads it
// from whichever goroutine gets there first.
func (c *pooledSSHConn) setKeepaliveStop(stop func()) {
	c.mu.Lock()
	c.stopKeepalive = stop
	c.mu.Unlock()
}

// isDead reports whether this connection has been closed. It is a MARK, never
// a probe: asking the far end would put a network round trip under the pool's
// mutex, which is the shape of defect the keepalive prober was just cured of
// (ssh_keepalive.go).
func (c *pooledSSHConn) isDead() bool { return c.dead.Load() }

// deadConn is the part of a pooled connection the pool consults before
// handing it to a second caller. Only *pooledSSHConn implements it; a test
// fake that does not is treated as live, which is what it is.
type deadConn interface{ isDead() bool }

// connIsDead reports whether a pooled connection has already been closed.
func connIsDead(c sshClientConn) bool {
	d, ok := c.(deadConn)
	return ok && d.isDead()
}

// poolKey identifies a shared SSH connection. Two Connect calls whose keys
// compare equal are the SAME principal reaching the SAME endpoint through
// the SAME route, so they may share one authenticated connection. Every
// component is load-bearing — omitting any of them is an authorization
// bug, not a resource bug:
//
//   - host/port/user: the network endpoint and the account on it. Two
//     different users on one host are different principals; sharing a
//     connection means one user's session carries the other's traffic.
//   - identity: the credential principal. A stored credential is isolated
//     by its SecretID — which is reminted on every password change, so a
//     rotated credential cannot reuse a transport authenticated with the
//     old secret (that is the distinction the "stored-credential ID" name
//     misses). For inline auth (no stored credential) we key on the SHA256
//     fingerprint of the public key, not the file path, so replacing the
//     file contents at the same path changes the pool identity. When neither
//     applies (agent-only / prompt-password), identity is empty by EXPLICIT
//     DESIGN: the agent authenticates each channel independently (every
//     shell/session request goes through the agent socket), so sharing the
//     transport does not share the authentication — each tab still proves
//     its identity through the agent for its own channel. If the agent's
//     loaded keys change (e.g. ssh-add -D), new transports use the updated
//     key set but existing transports remain valid. There is no credential
//     principal to isolate; pooling by host+user+port is correct.
//   - jumpRoute: the resolved identity of the bastion the connection is
//     dialed through. The same target reached via two different bastions
//     is a different route; pooling them together would let one bastion's
//     transport carry the other's traffic. Empty for direct connections.
//
// Widen this key only by adding a component that distinguishes two
// principals — narrowing it (dropping a component) is the authorization
// hole this task exists to close.
type poolKey struct {
	host      string
	port      int
	user      string
	identity  string // SecretID, public-key SHA256 fingerprint, or empty for agent/prompt auth
	jumpRoute string // resolved jump identity (see jumpRouteKey), "" for direct
}

// jumpRouteKey renders the jump route into a string component of poolKey.
// It mirrors poolKey's own identity fields for the bastion hop, so a target
// reached through two bastions (or the same bastion with two jump
// credentials) gets two entries. The bastion is itself pooled under its own
// poolKey (see dialViaJumpHost), so this string is the deduplication that
// keeps a target-with-jump entry separate from the same target dialed
// directly or via a different bastion.
func (k poolKey) jumpRouteKey() string {
	if k.host == "" {
		return ""
	}
	return fmt.Sprintf("%s@%s:%d/%s", k.user, k.host, k.port, k.identity)
}

// refCount tracks how many tabs (channels) reference a pooled connection.
// Guarded by the pool mutex.
type refCount struct {
	n int
}

// poolHandle is a reference to a pooled connection, returned by Acquire.
// Release decrements the ref count; the connection closes on the last ref.
//
// Each handle carries its own releaseOnce. Release is idempotent PER HANDLE:
// calling it twice on the same handle decrements the shared refcount exactly
// once, so double-releasing one of two handles cannot drop the count to zero
// underneath a live channel held by the other handle. The guarantee holds
// under concurrency because sync.Once serialises the decrement-and-maybe-
// close critical section, and the refcount itself is mutated only under the
// pool mutex (acquired inside the once). A handle whose once has fired is
// spent; further Release calls are no-ops.
type poolHandle struct {
	key         poolKey
	conn        sshClientConn
	ref         *refCount
	pool        *ConnPool
	releaseOnce sync.Once
}

// ConnPool is a ref-counted ssh.Client connection pool (AD-4). Channels
// multiplex over one connection per poolKey; the connection closes when the
// last tab releases its reference. The pool wraps the dial logic: on a cache
// miss it calls dial(key) to establish a new sshClientConn. Production use
// sets dial to a function that performs the real gossh.Dial (direct or via
// a jump host, the latter acquiring the bastion from this same pool).
type ConnPool struct {
	log     log.Logger
	mu      sync.Mutex
	pool    map[poolKey]*poolEntry
	dialing map[poolKey]*dialInProgress
	// dial is the connection factory (injected for testing; production sets
	// it to a function that calls gossh.Dial).
	dial func(key poolKey) (sshClientConn, error)
}

// poolEntry holds a connection and its ref count.
type poolEntry struct {
	conn sshClientConn
	ref  *refCount
}

// dialInProgress tracks an in-flight dial for a key so concurrent Acquire
// calls wait for the first dialer instead of racing.
type dialInProgress struct {
	done chan struct{}
}

// NewConnPool creates an empty connection pool. The default dial factory is
// the placeholder (defaultDial, which returns an error); production callers
// pass a per-call factory to AcquireDial, and tests override p.dial directly.
func NewConnPool(logger log.Logger) *ConnPool {
	p := &ConnPool{
		log:     logger.With("module", "ssh-pool"),
		pool:    make(map[poolKey]*poolEntry),
		dialing: make(map[poolKey]*dialInProgress),
	}
	p.dial = p.defaultDial
	return p
}

// Acquire returns a handle to a pooled connection for the given key. On a
// cache miss, it dials a new connection using the pool's configured dial
// factory. Concurrent Acquire calls with the same key share a single
// connection: the first goroutine dials under a per-key slot, others wait
// and reuse. Each Acquire increments the ref count and returns a fresh
// handle with its own release guard.
//
// ctx applies only while WAITING for an in-flight dial started by another
// goroutine. The dial itself runs with its own context (captured by the
// dial factory closure). Cancelling ctx while waiting returns ctx.Err();
// the goroutine is cleanly removed from the waiter set without touching
// the pool state.
//
// Production callers use AcquireDial so the dial factory sees the caller's
// credential store / key material (which the key alone cannot recover);
// Acquire is retained for tests and the simple factory path.
func (p *ConnPool) Acquire(ctx context.Context, key poolKey) (*poolHandle, error) {
	return p.acquire(ctx, key, p.dial)
}

// AcquireDial is Acquire with a per-call dial factory. On a cache miss the
// supplied dial is used; on a hit it is ignored. The factory captures the
// caller's ConnectConfig (credential store, key paths, jump binding) so the
// dial resolves the same auth buckets the caller would — the key identifies
// the principal, the factory provides the credentials. Concurrent AcquireDial
// calls with the same key share one connection regardless of which factory
// dialed; the first dialer's factory wins and the key guarantees the result
// is the same principal.
func (p *ConnPool) AcquireDial(ctx context.Context, key poolKey, dial func(key poolKey) (sshClientConn, error)) (*poolHandle, error) {
	if dial == nil {
		dial = p.dial
	}
	return p.acquire(ctx, key, dial)
}

// acquire is the shared core. dial is the factory to use on a cache miss;
// it is ignored on a hit. The dial runs without holding the pool mutex.
//
// Waiters on an in-flight dial watch ctx.Done() alongside the done channel:
// if the context cancels before the dialer finishes, the waiter returns
// ctx.Err() without touching pool state. The first dialer's context is
// embedded in the dial closure — cancellation there propagates through
// the dial logic (dialDirect/dialViaJumpHost) and acquire cleans up the
// dialInProgress slot before waking waiters.
func (p *ConnPool) acquire(ctx context.Context, key poolKey, dial func(key poolKey) (sshClientConn, error)) (*poolHandle, error) {
	p.mu.Lock()
	if entry, ok := p.pool[key]; ok && !connIsDead(entry.conn) {
		entry.ref.n++
		p.mu.Unlock()
		return &poolHandle{key: key, conn: entry.conn, ref: entry.ref, pool: p}, nil
	} else if ok {
		// The entry is a corpse: its transport is closed and the sessions
		// still holding references have not finished noticing yet. Evict it
		// here rather than waiting for the last Release, and fall through to
		// a fresh dial.
		//
		// The holders are not disturbed. Release compares the ref it holds
		// against the entry it finds (see Release), so a later Release for
		// this generation deletes nothing — the replacement dialled below is
		// a different entry with a different ref and cannot be removed by
		// somebody unwinding the old one.
		delete(p.pool, key)
		p.log.Info("evicting a closed pooled connection", "host", key.host, "port", key.port, "user", key.user)
	}

	// No existing entry — check if another goroutine is already dialing this
	// key. If so, wait on its done channel (with ctx cancellation) and reuse.
	if dialing, exists := p.dialing[key]; exists {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-dialing.done:
		}
		return p.acquire(ctx, key, dial)
	}

	// We're the first — register a dial-in-progress and dial without holding
	// the pool lock (the dial may block for seconds).
	d := &dialInProgress{done: make(chan struct{})}
	p.dialing[key] = d
	p.mu.Unlock()

	conn, err := dial(key)

	p.mu.Lock()
	delete(p.dialing, key)
	if err != nil {
		p.mu.Unlock()
		close(d.done) // wake waiters so they can see the failure / retry
		// A typed domain error already names the user and the host — wrapping
		// it here printed them twice in one sentence, which is how a message
		// stops being read. Only an untyped failure needs the context added.
		var noAuth *ErrNoAuthMethod
		if errors.As(err, &noAuth) {
			return nil, err
		}
		return nil, fmt.Errorf("dial %s@%s:%d: %w", key.user, key.host, key.port, err)
	}
	// A concurrent Acquire that waited on our done channel cannot have
	// inserted an entry (it re-enters acquire only after we close d.done),
	// but CloseAll could have run between the unlock and here. Be safe: if
	// an entry now exists, discard our dial and reuse it.
	if entry, ok := p.pool[key]; ok && !connIsDead(entry.conn) {
		entry.ref.n++
		p.mu.Unlock()
		_ = conn.Close() // discard our dial; close outside mutex (may release a jump handle)
		close(d.done)
		return &poolHandle{key: key, conn: entry.conn, ref: entry.ref, pool: p}, nil
	}
	ref := &refCount{n: 1}
	p.pool[key] = &poolEntry{conn: conn, ref: ref}
	p.mu.Unlock()
	close(d.done) // wake waiters

	return &poolHandle{key: key, conn: conn, ref: ref, pool: p}, nil
}

// Release decrements the ref count for a handle. On the last ref, the
// connection is closed and removed from the pool. Release is idempotent per
// handle: a handle's first Release decrements the shared refcount exactly
// once (under the pool mutex, inside a sync.Once); subsequent Release calls
// on the same handle are no-ops. Releasing a nil handle or one that belongs
// to a different pool is a no-op.
func (p *ConnPool) Release(h *poolHandle) {
	if h == nil || h.ref == nil || h.pool == nil {
		return
	}
	if h.pool != p {
		return // handle belongs to a different pool
	}
	var toClose sshClientConn
	h.releaseOnce.Do(func() {
		p.mu.Lock()
		h.ref.n--
		if h.ref.n <= 0 {
			if entry, ok := p.pool[h.key]; ok && entry.ref == h.ref {
				delete(p.pool, h.key)
			}
			toClose = h.conn
			p.log.Debug("pool connection closed (last ref)",
				"host", h.key.host, "user", h.key.user, "port", h.key.port)
		}
		p.mu.Unlock()
	})
	// Close runs OUTSIDE the pool mutex: a pooledSSHConn's Close may release
	// a jump handle, which re-enters Release on this same pool. Closing under
	// p.mu would self-deadlock.
	if toClose != nil {
		_ = toClose.Close()
	}
}

// CloseAll closes all pooled connections regardless of ref count.
// Used during shutdown. Conns are closed OUTSIDE the pool mutex: a
// pooledSSHConn's Close may release a jump handle, which re-enters Release.
func (p *ConnPool) CloseAll() {
	p.mu.Lock()
	toClose := make([]sshClientConn, 0, len(p.pool))
	for key, entry := range p.pool {
		toClose = append(toClose, entry.conn)
		delete(p.pool, key)
	}
	p.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
}

// Drain closes all pooled connections matching the predicate, regardless
// of refcount. Existing sessions on those connections will fail. Entries
// not matching the predicate are unaffected. Returns the number of
// connections drained.
//
// This is used when a credential version is retired: the pool key has
// changed (so new Acquire calls get a new transport), but the old entries
// are still in the pool. Drain removes them, forcing new dials.
func (p *ConnPool) Drain(match func(key poolKey) bool) int {
	p.mu.Lock()
	toClose := make([]sshClientConn, 0)
	closed := 0
	for key, entry := range p.pool {
		if match(key) {
			toClose = append(toClose, entry.conn)
			delete(p.pool, key)
			closed++
			p.log.Debug("pool connection drained",
				"host", key.host, "user", key.user, "port", key.port, "identity", key.identity)
		}
	}
	p.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
	return closed
}

// Count returns the number of pooled connections (for testing/diagnostics).
func (p *ConnPool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pool)
}

// defaultDial is the placeholder dial function used when no production dial
// is wired. Production sets ConnPool.dial to a function that performs the
// real gossh.Dial (direct or via a jump host acquired from this pool).
func (p *ConnPool) defaultDial(key poolKey) (sshClientConn, error) {
	return nil, fmt.Errorf("pool dial not configured for %s@%s:%d", key.user, key.host, key.port)
}
