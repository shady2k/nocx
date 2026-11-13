//go:build nocx_local_ssh

package ssh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
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
	client sshClientConn
	// barrier is the connection manager's answer to "has the far side
	// answered one request", for the release barrier (PooledSpec.Barrier).
	barrier   func(ctx context.Context) error
	release   func() // releases the jump handle, nil for direct conns
	closeOnce sync.Once

	// mu guards nothing that outlives construction any more; kept so a field
	// added here has one place to be read under.
	mu sync.Mutex
	// ended is set by the dial's own connection marker the instant the library
	// closes the socket — inside x/crypto's teardown, before Wait returns. It
	// is what makes "this transport has ended" answerable synchronously, so
	// admission cannot hand a dead connection out in the window before any
	// watcher goroutine runs (nocx-oalo1.4.6, review finding 5).
	ended *atomic.Bool
	// tainted records that the pool has retired this connection from new
	// admission (Taint). The supervisor honours it: a connection the pool will
	// not hand out is not one it hands out either.
	tainted atomic.Bool
	// dead records that this connection has been closed, so the pool can
	// refuse to hand it to anyone else. See isDead.
	dead atomic.Bool

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

func (c *pooledSSHConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// Marked BEFORE anything is torn down, so the interval "this
		// connection may still be handed to a caller" closes at the first
		// instant it stops being true rather than at the last. A closed
		// transport that still reads as live for the length of its own
		// teardown is exactly the window a new tab falls into.
		c.dead.Store(true)
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

// sessionReleaseBudget bounds the round trip awaitSessionRelease spends. It is
// a BACKSTOP for a peer that answers nothing, never a wait: the barrier's
// verdict is the far side's own reply, and the budget only decides how long
// silence may park a session open that would otherwise park inside
// gossh.Client.NewSession, which takes neither a context nor a deadline.
const sessionReleaseBudget = 10 * time.Second

// newSession opens a session channel on this connection, re-asking ONCE when
// the far side refuses the request.
//
// THE DEFECT THIS EXISTS FOR (nocx-xn63t.4.10). A far side bounds the sessions
// it grants one connection at a time — OpenSSH's MaxSessions, and a subsystem
// (sftp) counts as one — and its release of a session the client has just
// CLOSED is not complete when the next request arrives. The second request is
// then refused, and what a caller is told is `connect failed (open failed)`;
// on the live-sshd fixture at MaxSessions 1 with the previous session closed
// immediately before, that was 59 refusals in 60 rounds, and 3 grants in 10 at
// a zero delay against 10 of 10 once 1 ms had passed. The shell-integration
// bundle publish asks ONE pooled connection for two sessions by design (a probe
// for the account's home, then an sftp channel — AD-4, one authentication for
// one machine), so at MaxSessions 1 the sftp open was the one refused and the
// session never integrated.
//
// WHY A RETRY AND NOT A WAIT BEFORE EVERY SESSION. A far side's DEFAULT is ten
// sessions on one connection (OpenSSH's own `maxsessions 10`, read back with
// `sshd -T` in CI's image and on the host), and nocx holds one or two at a time
// — a pane's shell, a lane, a probe, an sftp channel. So the refusal is the
// exceptional path, and a barrier paid before every session after the first
// would tax every pane on a WAN host forever to serve a host that allows one.
// Paying nothing while nothing is refused is the whole reason this shape was
// chosen over the pre-emptive one, which measured 30 of 30 green here and is
// recorded in this bead's history.
//
// WHY THE RETRY IS NOT A RETRY ON TIMING (AGENTS.md forbids one). It is gated
// on an observable refusal, and the wait between the two attempts is an ANSWER
// from the far side (awaitSessionRelease) rather than a duration: the server
// cannot read the second request before it has finished the pass that wrote the
// reply, and the session the client just closed was released by then. A bare
// retry with nothing in between was measured at 59 of 59 here as well, because
// the refusal's own reply happens to order the release too — that is a property
// of this server's loop order, and not one to depend on when an answer costs
// the same and states the ordering outright.
//
// A REFUSAL THAT IS NOT THE RELEASE RACE IS REPORTED UNCHANGED, after exactly
// one re-ask: a host that will never grant the session (policy, a restricted
// shell) or one whose session is genuinely held by somebody else answers the
// same way as the release race (measured: both are `connect failed (open
// failed)`, reason code `ConnectionFailed`, so the client cannot tell them
// apart), and the caller sees that second refusal — the same class it would
// have seen from a single attempt.
func (c *pooledSSHConn) newSession() (*gossh.Session, error) {
	gclient, ok := c.client.(*gossh.Client)
	if !ok {
		// A pool whose dial factory was overridden with a fake (tests): the
		// fake has no session channels to open, and saying so is better than
		// reporting a nil-pointer panic from the type assertion above.
		return nil, fmt.Errorf("ssh: pooled connection: %T carries no session channels", c.client)
	}
	sess, err := gclient.NewSession()
	if err == nil {
		return sess, nil
	}
	if !sessionRefused(err) {
		return nil, err
	}
	if rerr := c.awaitSessionRelease(gclient); rerr != nil {
		return nil, rerr
	}
	return gclient.NewSession()
}

// sessionRefused reports whether the far side refused the session channel
// itself, which is the only class worth re-asking: the channel never opened, so
// nothing about the connection has changed and the next attempt is the same
// request made again.
//
// EVERY REASON counts, not one of them, and that is measured rather than
// cautious: OpenSSH answers the release race and a session genuinely held by
// somebody else identically (reason `connect failed`, message "open failed"),
// while its own source names further reasons for the same decision — so a host
// that spells a MaxSessions refusal `resource shortage` or `administratively
// prohibited` must still get the one re-ask. Anything else — a subsystem the
// host will not start, a transport that died — is not this class and is
// reported as it is.
func sessionRefused(err error) bool {
	var openErr *gossh.OpenChannelError
	return errors.As(err, &openErr)
}

// awaitSessionRelease waits for the far side to have answered one request on
// this connection, which is what makes its release of the session a caller just
// closed an ordered fact rather than a race — the wait between the two attempts
// newSession makes (see it for the measurement and why this rather than a
// delay).
//
// THE CONNECTION'S ONE PROBER ANSWERS IT (design §5.3; review finding 1). This
// call used to send its own reply-wanting keepalive, which made the barrier a
// second prober: it queued behind the supervisor's parked probe on the mux's
// send lock, accumulated one parked request per refusal, and closed the whole
// connection after its own ten seconds — without the supervisor's look grace or
// its silence budget. Now it asks the connection manager, which reuses the
// probe already parked (it is the evidence) or starts one, and answers with the
// far side's own ordering. A barrier that gets no answer fails THIS channel's
// open and leaves the connection alone: whether silence ends a connection is
// the one closing policy's decision, never this call's.
func (c *pooledSSHConn) awaitSessionRelease(gclient *gossh.Client) error {
	_ = gclient
	if c.barrier == nil {
		// A connection dialled outside the helper's manager (a test fake, a
		// direct dial): there is no prober to ask, and the one thing this call
		// must not do is invent one.
		return errNoBarrier
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionReleaseBudget)
	defer cancel()
	err := c.barrier(ctx)
	if err == nil {
		return nil
	}
	return fmt.Errorf("ssh: the far side did not answer before the session was re-asked: %w", err)
}

// errNoBarrier is what a connection with no prober to ask answers the release
// barrier with: it fails the channel's open rather than sending a second
// keepalive, which is the behaviour the one-prober rule requires.
var errNoBarrier = errors.New("ssh: this connection has no prober to ask")

// Tainted reports whether the pool has retired this connection from new
// admission (a detached writer's cap, a credential rotation).
func (c *pooledSSHConn) Tainted() bool { return c.tainted.Load() }

// isDead reports whether this connection has been closed. It is a MARK, never
// a probe: asking the far end would put a network round trip under the pool's
// mutex, which is the shape of defect the keepalive prober was just cured of
// (ssh_keepalive.go).
func (c *pooledSSHConn) isDead() bool {
	if c.dead.Load() {
		return true
	}
	return c.ended != nil && c.ended.Load()
}

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
	jumpRoute string // the rendered route (see routeComponent), "" for direct
}

// routeComponent renders ONE hop into the route component of a connection key:
// its own identity, the same sentence a key's String uses with the route left
// out. It is the one spelling — a route's component is these joined by ">"
// (ssh_pooled.go's routeKeyOf), and a hop's own key uses it too — so the
// rendering is never rewritten wherever a hop is named.
//
// The values are the hop's own identity fields, so a target reached through two
// bastions (or the same bastion with two jump credentials) gets two keys. A hop
// is itself pooled under its own key (ssh_pooled.go's acquireHop), so this
// string is the deduplication that keeps a target-through-a-route entry
// separate from the same target dialled directly or through a different route.
func routeComponent(host string, port int, user, identity string) string {
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s@%s:%d/%s", user, host, port, identity)
}

// refCount tracks how many tabs (channels) reference a pooled connection.
// n is guarded by the pool mutex. detachCount and closeReason are the
// detached-writer bookkeeping a helper's SSH sessions need (spec §5.7,
// nocx-6q1uh.3): detachCount (also guarded by the pool mutex) is how many
// detached writers THIS connection currently holds, given back to the
// pool's own live gauge when the connection actually closes, whichever way
// that happens; closeReason is set once, by CloseTainted, before the close
// it names, so every handle sharing this connection — a sibling channel —
// can read back why its own session ended.
type refCount struct {
	n           int
	detachCount int
	closeReason atomic.Value // string, unset until CloseTainted
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

// closeReason is the name CloseTainted gave this handle's connection when it
// closed it (spec §5.7's detached-writer cap), or "" for a connection that
// has not been (or was not) closed that way. Every handle sharing the
// connection reads the same value, because they share the *refCount it is
// stored on.
func (h *poolHandle) closeReason() string {
	if h == nil || h.ref == nil {
		return ""
	}
	v, _ := h.ref.closeReason.Load().(string)
	return v
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

	// detachedWriters is how many detached writers this pool currently
	// holds, summed across every connection (spec §5.7's maxDetachedWriters,
	// "counted per helper process" — one ConnPool serves the whole helper
	// for its life, which is what makes this pool the process-wide gauge).
	// Guarded by mu.
	detachedWriters int
}

// maxDetachedWriters bounds how many writers this pool's connections
// currently hold detached (spec §5.7): a fail-closed cap on one helper
// process's worst case, since golang.org/x/crypto/ssh gives no per-channel
// interrupt and a write stuck behind a zero-sized window can only ever be
// abandoned, never cancelled. Each one still holds a goroutine and a
// connection open until its peer lets it go.
const maxDetachedWriters = 8

// ReasonDetachedWriterCap is why CloseTainted closes a connection when the
// cap above is already spent — the one name both the pool's own bookkeeping
// and a sibling channel reading it back (poolHandle.closeReason,
// sshsvc.ShellChannel via *PooledConn) use, so there is exactly one spelling
// of this cause (AGENTS.md, "Look for the existing answer").
const ReasonDetachedWriterCap = "detached_writer_cap"

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
// the dial logic (ssh_dial.go) and acquire cleans up the dialInProgress slot
// before waking waiters.
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
			// Every detached writer this connection ever held ends with it
			// (mux teardown unblocks every remaining channel), so the cap's
			// live gauge gives those slots back here rather than never.
			p.detachedWriters -= h.ref.detachCount
			h.ref.detachCount = 0
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

// Taint marks h's connection so Acquire never hands it to a new caller again
// (spec §5.7's first detach): removing it from the pool map is enough — an
// existing handle's Release still finds it by h.ref/h.conn regardless, so
// siblings already holding a reference are unaffected and the connection
// closes exactly when the last of them releases, as any entry at ref zero
// does. h's own reference is untouched; the caller still owns it and must
// eventually Release it like any other.
//
// Taint also records one more detached writer against this pool
// (maxDetachedWriters, "per helper process"). Once the pool already holds
// the cap, Taint escalates instead: it closes h's connection AT ONCE
// (CloseTainted, ReasonDetachedWriterCap) rather than waiting for the last
// release — the cap is fail-closed rather than a leak.
func (p *ConnPool) Taint(h *poolHandle) {
	if h == nil || h.pool != p {
		return
	}
	if conn, ok := h.conn.(*pooledSSHConn); ok {
		conn.tainted.Store(true)
	}
	p.mu.Lock()
	h.ref.detachCount++
	p.detachedWriters++
	exceeded := p.detachedWriters > maxDetachedWriters
	if !exceeded {
		if entry, ok := p.pool[h.key]; ok && entry.ref == h.ref {
			delete(p.pool, h.key)
		}
	}
	p.mu.Unlock()
	if exceeded {
		p.CloseTainted(h, ReasonDetachedWriterCap)
	}
}

// CloseTainted closes h's connection NOW, ending every sibling channel on it
// rather than waiting for the last release. reason is recorded on the
// shared refCount BEFORE the close, so any handle sharing this connection —
// a sibling — reads it back through poolHandle.closeReason once its own
// Wait/Read notices the connection is gone.
func (p *ConnPool) CloseTainted(h *poolHandle, reason string) {
	if h == nil || h.pool != p {
		return
	}
	h.ref.closeReason.Store(reason)
	p.mu.Lock()
	if entry, ok := p.pool[h.key]; ok && entry.ref == h.ref {
		delete(p.pool, h.key)
	}
	p.detachedWriters -= h.ref.detachCount
	h.ref.detachCount = 0
	p.mu.Unlock()
	p.log.Warn("closing a tainted ssh connection", "host", h.key.host, "user", h.key.user, "port", h.key.port, "reason", reason)
	_ = h.conn.Close()
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

// Close closes every pooled connection regardless of refcount. It is the
// client's own shutdown, called by the helper daemon that owns it (sshdial's
// client.Close); ordinary tab closure releases a single handle via the
// channel's closeCb and never reaches here.
func (rc *RealClient) Close() error {
	rc.dial.pool.CloseAll()
	return nil
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
