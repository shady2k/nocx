package apisend

// Design §7.1: one executor, a supplied dialer. This file is the SSH half
// and, more than anything, the honest statement of ITS ONE LIMIT.
//
// `tunnelConn.Dial` (internal/ssh/ssh_tunnel.go:116) is `Dial(addr string)`
// — a net.Conn, which is what matters, but no context. So there are two
// separate claims here and each has its own test:
//
//   - CANCELLATION CANNOT INTERRUPT A BLOCKED REMOTE DIAL. Nothing in this
//     package can make it, and a test that claimed it would be lying.
//   - What IS guaranteed: the caller is released at the earlier of the
//     context and the dial timeout, and a connection that arrives after
//     that is CLOSED AND NEVER PRODUCES A RUN.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/ssh"
)

// ─── a fake pooled SSH connection ──────────────────────────────────────────

// fakePool stands in for the SSH pool. It counts the CONNECTIONS it opens
// and the LEASES taken on them, which are the two numbers AD-7 is about:
// a session references a pooled connection, never owns it, so N leases on
// one pool key are still one connection.
type fakePool struct {
	mu          sync.Mutex
	connections map[string]int // pool key → how many times a connection was established
	leases      int
}

func newFakePool() *fakePool { return &fakePool{connections: map[string]int{}} }

// acquire is acquirePooled's shape: share when the resolved pool key
// matches, otherwise establish one.
func (p *fakePool) acquire(poolKey string) *fakeLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connections[poolKey] == 0 {
		p.connections[poolKey] = 1
	}
	p.leases++
	return &fakeLease{pool: p, key: poolKey, done: make(chan struct{})}
}

func (p *fakePool) connectionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.connections {
		n += c
	}
	return n
}

func (p *fakePool) leaseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases
}

// fakeLease is an ssh.TunnelConn. Its Dial opens a REAL local TCP
// connection, so a whole send can run through it — the far side of the
// tunnel is the test's own server.
type fakeLease struct {
	pool *fakePool
	key  string

	mu       sync.Mutex
	closed   bool
	dials    int
	closes   int
	blockOn  chan struct{} // when non-nil, Dial waits for it before connecting
	lastConn *recordingConn

	done chan struct{}
}

func (l *fakeLease) Dial(addr string) (net.Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ssh.ErrTunnelConnClosed
	}
	l.dials++
	block := l.blockOn
	l.mu.Unlock()

	if block != nil {
		<-block
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	rc := &recordingConn{Conn: c}
	l.mu.Lock()
	l.lastConn = rc
	l.mu.Unlock()
	return rc, nil
}

func (l *fakeLease) Listen(string) (net.Listener, error) { return nil, errors.New("not used") }
func (l *fakeLease) Done() <-chan struct{}               { return l.done }
func (l *fakeLease) LostErr() error                      { return nil }

func (l *fakeLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.closes++
	return nil
}

func (l *fakeLease) dialCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dials
}

func (l *fakeLease) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closes
}

func (l *fakeLease) conn() *recordingConn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastConn
}

// recordingConn reports whether it was closed, which is the observable the
// late-arrival guarantee is stated against.
type recordingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *recordingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// sshRoute is the route a tunnelled environment builds: the supplied
// dialer, no proxy (this machine's proxy configuration describes this
// machine's network, not the far side's), and a resolver that REFUSES to
// answer — the remote side resolves the name (§7.1), so this end cannot
// truthfully say which addresses the dial will reach, and httppolicy's
// Route contract says such a route must error rather than guess.
func sshRoute(d Dialer) httppolicy.Route {
	return httppolicy.NewRoute(
		httppolicy.ResolverFunc(func(_ context.Context, host string) ([]net.IP, error) {
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			return nil, errors.New("a tunnelled route does not resolve names on this side")
		}),
		d, nil)
}

// ─── the happy path ────────────────────────────────────────────────────────

// TestSSHDialer_SendsThroughTheLease is the pair every refusal below needs
// (AGENTS.md testing rule 3): on an ordinary machine, with a live lease, the
// request goes out through the tunnel and the response comes back.
func TestSSHDialer_SendsThroughTheLease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through the tunnel")
	}))
	defer srv.Close()

	pool := newFakePool()
	lease := pool.acquire("user@bastion:22")
	route := sshRoute(NewSSHDialer(lease, time.Minute))

	ex, err := New(WithRoutes(func(context.Context, string) (Route, error) { return route, nil })).
		Send(context.Background(), apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: "prod"})
	got := answered(t, ex, err)
	if got.Text != "through the tunnel" {
		t.Errorf("Text = %q, want the response from the far side", got.Text)
	}
	if lease.dialCount() != 1 {
		t.Errorf("the lease was dialled %d times, want 1", lease.dialCount())
	}
}

// TestSSHDialer_OneConnectionAcrossManySends is AD-7 in the shape this
// package can hold it: a session REFERENCES a pooled connection and never
// owns one. Two sends on one route, plus a second route resolving to the
// same pool key, must still be ONE SSH connection — and the dialer must
// never close the lease it was handed, because it does not own it.
func TestSSHDialer_OneConnectionAcrossManySends(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	pool := newFakePool()
	const poolKey = "user@bastion:22"
	var leases []*fakeLease
	var mu sync.Mutex

	routes := func(_ context.Context, routeID string) (Route, error) {
		// Every environment that names this profile resolves to the same
		// pool key, which is what makes the sharing observable.
		l := pool.acquire(poolKey)
		mu.Lock()
		leases = append(leases, l)
		mu.Unlock()
		return sshRoute(NewSSHDialer(l, time.Minute)), nil
	}
	c := New(WithRoutes(routes))

	for _, key := range []Key{{RouteID: "prod"}, {RouteID: "prod"}, {RouteID: "staging"}} {
		if _, err := c.Send(context.Background(),
			apicoll.Request{Method: http.MethodGet, URL: srv.URL}, key); err != nil {
			t.Fatalf("Send(%v): %v", key, err)
		}
	}

	if got := pool.connectionCount(); got != 1 {
		t.Errorf("%d SSH connections opened across three sends, want 1 — a lease shares when the pool key matches (AD-7)", got)
	}
	// Two client instances (one per RouteID), so two leases; the third send
	// reuses the cached instance and asks for no route at all.
	if got := pool.leaseCount(); got != 2 {
		t.Errorf("%d leases taken, want 2 — one per client instance, and the repeat send reuses its instance", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, l := range leases {
		if l.closeCount() != 0 {
			t.Errorf("lease %d was closed %d times by the dialer; the dialer does not own the lease and must never release it", i, l.closeCount())
		}
	}
}

// ─── the limit, stated in both directions ──────────────────────────────────

// TestSSHDialer_CancellationReleasesTheCallerButCannotInterruptTheDial is
// the honest half of the limit. The context is DROPPED at the tunnel — the
// remote dial stays blocked, and the test proves it by observing that the
// dial is still in flight after DialContext has already returned.
func TestSSHDialer_CancellationReleasesTheCallerButCannotInterruptTheDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	pool := newFakePool()
	lease := pool.acquire("k")
	block := make(chan struct{})
	lease.blockOn = block

	d := NewSSHDialer(lease, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	returned := make(chan error, 1)
	go func() {
		_, err := d.DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
		returned <- err
	}()
	// Cancel only once the remote dial is genuinely in flight — otherwise
	// the pre-dial context check answers and the test proves nothing.
	waitFor(t, "the remote dial to be in flight", func() bool { return lease.dialCount() == 1 })
	cancel()

	err := <-returned
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext returned %v, want context.Canceled — the CALLER is released", err)
	}
	// And the claim we cannot make: the remote dial is STILL BLOCKED after
	// DialContext has returned. Nothing this package can do reaches it.
	if lease.conn() != nil {
		t.Fatal("a connection existed while the dial was still blocked; the fake did not block")
	}
	close(block)
}

// TestSSHDialer_AConnectionArrivingAfterCancellationIsClosed is the
// mitigation, and it is the guarantee that replaces the one we cannot make:
// a cancelled run can never acquire a connection, because the connection
// that arrives late is closed by the adapter and handed to nobody.
func TestSSHDialer_AConnectionArrivingAfterCancellationIsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the server was reached by a cancelled run")
	}))
	defer srv.Close()

	pool := newFakePool()
	lease := pool.acquire("k")
	block := make(chan struct{})
	lease.blockOn = block

	d := NewSSHDialer(lease, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		conn, err := d.DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err == nil {
			_ = conn.Close()
			t.Error("DialContext handed back a connection after cancellation")
		}
	}()
	// The dial must be in flight before the cancel, or the pre-dial check
	// answers and there is no late connection to close.
	waitFor(t, "the remote dial to be in flight", func() bool { return lease.dialCount() == 1 })
	cancel()
	<-returned

	// Now let the remote dial complete. The connection it produces belongs
	// to nobody, so the adapter closes it.
	close(block)
	waitFor(t, "the late connection to be closed", func() bool {
		c := lease.conn()
		return c != nil && c.closed.Load()
	})
}

// TestSSHDialer_BoundsABlockedDialByItsOwnTimeout: a context with no
// deadline is the ordinary case (a user pressing Send), so the bound cannot
// come from the context. It comes from dialTimeout, and it is the only
// thing standing between a dead bastion and a run that never ends.
func TestSSHDialer_BoundsABlockedDialByItsOwnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	pool := newFakePool()
	lease := pool.acquire("k")
	block := make(chan struct{})
	defer close(block)
	lease.blockOn = block

	d := NewSSHDialer(lease, time.Millisecond)
	// context.Background(): no deadline of its own, so only the dial
	// timeout can end this.
	_, err := d.DialContext(context.Background(), "tcp", strings.TrimPrefix(srv.URL, "http://"))
	if !errors.Is(err, ErrSSHDialTimeout) {
		t.Fatalf("DialContext returned %v, want ErrSSHDialTimeout", err)
	}
}

// TestSSHDialer_AConnectionArrivingAfterTheTimeoutIsClosed is the other
// direction of the same mitigation: a timed-out dial must not leave a live
// channel to the far side either.
func TestSSHDialer_AConnectionArrivingAfterTheTimeoutIsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the server was reached by a timed-out dial")
	}))
	defer srv.Close()

	pool := newFakePool()
	lease := pool.acquire("k")
	block := make(chan struct{})
	lease.blockOn = block

	d := NewSSHDialer(lease, time.Millisecond)
	if _, err := d.DialContext(context.Background(), "tcp", strings.TrimPrefix(srv.URL, "http://")); err == nil {
		t.Fatal("DialContext succeeded, want the timeout")
	}
	close(block)
	waitFor(t, "the late connection to be closed", func() bool {
		c := lease.conn()
		return c != nil && c.closed.Load()
	})
}

// TestSSHDialer_AnAlreadyCancelledContextNeverDials: the cheapest half of
// the guarantee, and the one that matters when a user cancels a queue of
// runs — nothing is asked of the far side at all.
func TestSSHDialer_AnAlreadyCancelledContextNeverDials(t *testing.T) {
	pool := newFakePool()
	lease := pool.acquire("k")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewSSHDialer(lease, time.Minute).DialContext(ctx, "tcp", "10.0.0.1:80")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext returned %v, want context.Canceled", err)
	}
	if lease.dialCount() != 0 {
		t.Errorf("the lease was dialled %d times for an already-cancelled run, want 0", lease.dialCount())
	}
}

// ─── the refusals ──────────────────────────────────────────────────────────

// TestSSHDialer_ASpentLeaseRefusesRatherThanDiallingLocally is the test the
// whole of §6.5 exists for. A silent fallback to a local dialer would send
// a production request AROUND its bastion — and the assertion is written as
// the ABSENCE of that path: the server on the other end of the address
// records that nothing ever arrived.
func TestSSHDialer_ASpentLeaseRefusesRatherThanDiallingLocally(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		_, _ = io.WriteString(w, "reached directly")
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	pool := newFakePool()
	lease := pool.acquire("k")
	_ = lease.Close() // the lease is spent: the tab closed, the tunnel stopped

	// The dialer on its own.
	conn, err := NewSSHDialer(lease, time.Minute).DialContext(context.Background(), "tcp", addr)
	if err == nil {
		_ = conn.Close()
		t.Fatal("DialContext succeeded on a spent lease")
	}
	if !errors.Is(err, ssh.ErrTunnelConnClosed) {
		t.Errorf("error = %v, want it to carry ssh.ErrTunnelConnClosed so a surface can say why", err)
	}

	// And the whole executor, which is where a fallback would actually be
	// reachable.
	route := sshRoute(NewSSHDialer(lease, time.Minute))
	ex, sendErr := New(WithRoutes(func(context.Context, string) (Route, error) { return route, nil })).
		Send(context.Background(), apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: "prod"})
	// A run at phase `dial`: the route existed and its dial refused, which
	// is the sentence a person needs — the connection is spent, and nothing
	// fell back to this machine's own interface.
	failedAt(t, ex, sendErr, PhaseDial)

	if n := reached.Load(); n != 0 {
		t.Fatalf("the server was reached %d times through a spent lease — the request went out around the bastion", n)
	}
}

// TestSSHDialer_ALostConnectionRefusesToo: the other spent state. A
// connection that DIED is not a lease the user released, and the two are
// different sentences for them — but neither dials locally.
func TestSSHDialer_ALostConnectionRefusesToo(t *testing.T) {
	pool := newFakePool()
	lease := pool.acquire("k")
	lease.mu.Lock()
	lease.closed = true
	lease.mu.Unlock()

	_, err := NewSSHDialer(lease, time.Minute).DialContext(context.Background(), "tcp", "10.0.0.1:80")
	if err == nil {
		t.Fatal("DialContext succeeded on a lost connection")
	}
	if !strings.Contains(err.Error(), component) {
		t.Errorf("error = %q, want it to name the sender rather than a package the user has never heard of", err)
	}
}

// TestSSHDialer_WithNoLeaseAtAll: nil is a wiring mistake, and the answer
// is a refusal rather than a panic in the dial path — and, again, never a
// local dial.
func TestSSHDialer_WithNoLeaseAtAll(t *testing.T) {
	_, err := NewSSHDialer(nil, time.Minute).DialContext(context.Background(), "tcp", "10.0.0.1:80")
	if !errors.Is(err, ErrNoSSHLease) {
		t.Fatalf("error = %v, want ErrNoSSHLease", err)
	}
}

// TestSSHDialer_RefusesANetworkTheTunnelCannotHonour: `tunnelConn.Dial`
// opens a direct-tcpip channel and the ADAPTER supplies the network
// parameter — so a network the tunnel cannot honour must be refused rather
// than silently widened. tcp4/tcp6 pin an address family the remote side
// decides, so answering them would be a promise this end cannot keep.
func TestSSHDialer_RefusesANetworkTheTunnelCannotHonour(t *testing.T) {
	pool := newFakePool()
	lease := pool.acquire("k")
	d := NewSSHDialer(lease, time.Minute)

	for _, network := range []string{"tcp4", "tcp6", "udp", "unix", ""} {
		if _, err := d.DialContext(context.Background(), network, "10.0.0.1:80"); err == nil {
			t.Errorf("DialContext(%q) succeeded, want it refused", network)
		}
	}
	if lease.dialCount() != 0 {
		t.Errorf("the lease was dialled %d times for a network it cannot honour, want 0", lease.dialCount())
	}
}

// TestSSHDialer_ReportsTheFarSideRefusal is §12.1 for the call this adapter
// makes: the remote target refuses the connection, and the error names the
// send rather than being swallowed.
func TestSSHDialer_ReportsTheFarSideRefusal(t *testing.T) {
	// A closed listener's address: the far side's connect fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	pool := newFakePool()
	lease := pool.acquire("k")
	if _, err := NewSSHDialer(lease, time.Minute).DialContext(context.Background(), "tcp", addr); err == nil {
		t.Fatal("DialContext succeeded against a closed listener")
	} else if !strings.Contains(err.Error(), addr) {
		t.Errorf("error = %q, want it to name the address that could not be reached", err)
	}
}

// TestNewSSHDialer_ZeroTimeoutStillBounds: a caller that passes no timeout
// gets the default rather than an unbounded wait. Stated as an interval
// with both ends: the dial is outstanding from the call until EITHER the
// far side answers OR the bound elapses — there is no third outcome in
// which it stays outstanding forever.
func TestNewSSHDialer_ZeroTimeoutStillBounds(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		got, ok := NewSSHDialer(nil, d).(*sshDialer)
		if !ok {
			t.Fatal("NewSSHDialer returned something other than *sshDialer")
		}
		if got.timeout != defaultSSHDialTimeout {
			t.Errorf("NewSSHDialer(_, %v).timeout = %v, want the default %v", d, got.timeout, defaultSSHDialTimeout)
		}
	}
}

// waitFor polls a condition. It waits on an OBSERVABLE STATE CHANGE rather
// than on a duration (AGENTS.md): a slow machine takes more passes, never a
// different answer. The deadline is a failure report, not a timing
// assertion.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
