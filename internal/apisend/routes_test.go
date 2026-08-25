package apisend

// The route table: the thing that turns an ENVIRONMENT's route into the
// route this sender dials through (design §6.5, §7.1).
//
// Two claims are asserted here over and over, because they are the whole
// reason the route lives on the environment rather than on a request:
//
//   - a "connection" route sends through the SSH lease, and
//   - when it cannot, THE SEND FAILS. It never falls back to the local
//     dialer, because that is a production request going out around its
//     bastion.
//
// The local server in these tests is the tripwire for the second one: if a
// refusal ever dialled locally, the server would see the request.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/ssh"
)

// countingServer is a local HTTP server that records how many requests
// reached it. Zero is the assertion every refusal below makes.
type countingServer struct {
	*httptest.Server
	hits atomic.Int64
}

func newCountingServer(t *testing.T, body string) *countingServer {
	t.Helper()
	cs := &countingServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cs.hits.Add(1)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// fakeLeaser stands in for the pool: it answers a profile id with a lease,
// or refuses. It counts the calls, which is how "one connection across many
// sends" is observed from this side.
type fakeLeaser struct {
	pool *fakePool
	// poolKeyFor maps a profile id to the pool key it resolves to, which is
	// what decides sharing (AD-7). Absent means the profile id is the key.
	poolKeyFor map[string]string
	err        error

	calls atomic.Int64

	mu     sync.Mutex
	leases []*fakeLease
}

func newFakeLeaser() *fakeLeaser {
	return &fakeLeaser{pool: newFakePool(), poolKeyFor: map[string]string{}}
}

func (l *fakeLeaser) LeaseForProfile(_ context.Context, profileID string) (ssh.TunnelConn, error) {
	l.calls.Add(1)
	if l.err != nil {
		return nil, l.err
	}
	key := profileID
	if k, ok := l.poolKeyFor[profileID]; ok {
		key = k
	}
	lease := l.pool.acquire(key)
	l.mu.Lock()
	l.leases = append(l.leases, lease)
	l.mu.Unlock()
	return lease, nil
}

// leaseAt returns the nth lease this leaser handed out.
func (l *fakeLeaser) leaseAt(t *testing.T, n int) *fakeLease {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if n >= len(l.leases) {
		t.Fatalf("lease %d was never taken; %d leases exist", n, len(l.leases))
	}
	return l.leases[n]
}

// eachLease walks every lease handed out, under the lock.
func (l *fakeLeaser) eachLease(fn func(i int, lease *fakeLease)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, lease := range l.leases {
		fn(i, lease)
	}
}

// ─── the id an environment's route is named by ─────────────────────────────

func TestRouteIDFor_TheTwoRoutesAnEnvironmentCanDeclare(t *testing.T) {
	direct, err := RouteIDFor(apicoll.Route{Kind: apicoll.RouteDirect})
	if err != nil {
		t.Fatalf("RouteIDFor(direct): %v", err)
	}
	if direct != "" {
		t.Errorf("the direct route id = %q, want the empty id — the sender's own default route", direct)
	}

	conn, err := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	if err != nil {
		t.Fatalf("RouteIDFor(connection): %v", err)
	}
	if conn == "" || conn == direct {
		t.Fatalf("the connection route id = %q, want an id distinct from the direct one", conn)
	}
	// Two environments naming ONE profile are one route, so their sends
	// share a client instance and therefore a pooled connection.
	again, err := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	if err != nil {
		t.Fatalf("RouteIDFor(connection) again: %v", err)
	}
	if again != conn {
		t.Errorf("the same profile produced two route ids, %q and %q", conn, again)
	}
	if other, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p2:1"}); other == conn {
		t.Errorf("two different profiles produced one route id %q; one connection's cookies would reach the other", conn)
	}
}

// A route that does not say how to get there is refused rather than quietly
// treated as direct — §6.5's third consequence, at the seam where the id is
// minted rather than only where the file is read.
func TestRouteIDFor_RefusesARouteThatDoesNotSayHowToGetThere(t *testing.T) {
	for name, r := range map[string]apicoll.Route{
		"no kind at all":                  {},
		"a kind this build does not know": {Kind: "socks"},
		"a connection naming none":        {Kind: apicoll.RouteConnection},
		"a direct route naming one":       {Kind: apicoll.RouteDirect, ProfileID: "ssh:p1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			if id, err := RouteIDFor(r); err == nil {
				t.Fatalf("RouteIDFor(%+v) = %q, want a refusal — a silent downgrade to direct is the send the route exists to prevent", r, id)
			}
		})
	}
}

// ─── the happy paths, one per route kind ───────────────────────────────────

func TestRoutes_TheDirectRouteSendsFromThisMachine(t *testing.T) {
	srv := newCountingServer(t, "from this machine")

	c := New(WithRoutes(NewRoutes(newFakeLeaser())))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: ""})
	got := answered(t, ex, err)
	if got.Text != "from this machine" {
		t.Errorf("Text = %q, want the response from the local server", got.Text)
	}
}

func TestRoutes_AConnectionRouteSendsThroughTheLease(t *testing.T) {
	srv := newCountingServer(t, "through the bastion")
	leaser := newFakeLeaser()

	id, err := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	if err != nil {
		t.Fatalf("RouteIDFor: %v", err)
	}
	c := New(WithRoutes(NewRoutes(leaser)))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: id})
	got := answered(t, ex, err)
	if got.Text != "through the bastion" {
		t.Errorf("Text = %q, want the response from the far side", got.Text)
	}
	if leaser.calls.Load() != 1 {
		t.Errorf("the pool was asked for %d leases, want 1", leaser.calls.Load())
	}
	if got := leaser.leaseAt(t, 0).dialCount(); got != 1 {
		t.Errorf("the lease was dialled %d times, want 1 — the request did not go through the tunnel", got)
	}
}

// ─── the refusal that is the point of the whole route ──────────────────────

// A profile with no live connection FAILS THE SEND. The local server is the
// tripwire: a fallback to the local dialer would reach it, and that is a
// production request going out around its bastion (§6.5).
func TestRoutes_AProfileWithNoLeaseFailsTheSendRatherThanDiallingLocally(t *testing.T) {
	srv := newCountingServer(t, "should never be reached")
	leaser := newFakeLeaser()
	leaser.err = errors.New("connect ssh:p1:1: no route to host")

	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	c := New(WithRoutes(NewRoutes(leaser)))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: id})
	// A RUN, at phase `connection`. The refusal is the same refusal; what
	// changed is that a person now has it on a row beside the request they
	// sent, rather than as a sentence with no request attached to it.
	fail := failedAt(t, ex, err, PhaseConnection)
	if !strings.Contains(fail.Reason, ErrNoConnection.Error()) {
		t.Errorf("reason = %q, want ErrNoConnection named in it — the surface needs a name to offer the right remedy", fail.Reason)
	}
	if !strings.Contains(fail.Reason, "ssh:p1:1") {
		t.Errorf("reason = %q, want it to name the connection that could not be leased", fail.Reason)
	}
	if got := srv.hits.Load(); got != 0 {
		t.Fatalf("the local server saw %d requests, want 0 — the refusal fell back to the local dialer", got)
	}
}

// An id this table does not know is refused by NAME, for the same reason.
func TestRoutes_AnUnknownRouteIDIsRefusedRatherThanDialledLocally(t *testing.T) {
	srv := newCountingServer(t, "should never be reached")

	c := New(WithRoutes(NewRoutes(newFakeLeaser())))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: "socks:whatever"})
	failedAt(t, ex, err, PhaseConnection)
	if got := srv.hits.Load(); got != 0 {
		t.Fatalf("the local server saw %d requests, want 0", got)
	}
}

// The lease is taken with no leaser wired at all: a route table built over
// nothing must refuse a connection route and still serve the direct one.
func TestRoutes_WithNoLeaserAConnectionRouteRefusesAndDirectStillWorks(t *testing.T) {
	srv := newCountingServer(t, "direct still works")
	c := New(WithRoutes(NewRoutes(nil)))

	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	refused, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: id})
	if fail := failedAt(t, refused, err, PhaseConnection); !strings.Contains(fail.Reason, ErrNoConnection.Error()) {
		t.Errorf("reason = %q, want ErrNoConnection", fail.Reason)
	}
	if got := srv.hits.Load(); got != 0 {
		t.Fatalf("the local server saw %d requests, want 0", got)
	}

	if _, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: ""}); err != nil {
		t.Fatalf("the direct route must still send: %v", err)
	}
	if got := srv.hits.Load(); got != 1 {
		t.Errorf("the local server saw %d requests, want 1", got)
	}
}

// ─── AD-7: a route REFERENCES a pooled connection ──────────────────────────

// Two sends on one environment, and a second environment resolving to the
// same pool key, are still ONE SSH connection — and the route never closes
// the lease it holds, because it does not own it.
func TestRoutes_OneConnectionAcrossManySends(t *testing.T) {
	srv := newCountingServer(t, "ok")
	leaser := newFakeLeaser()
	// Two profiles whose resolved pool key is the same host: the pool
	// shares, which is the behaviour a route must not defeat.
	leaser.poolKeyFor = map[string]string{"ssh:p1:1": "user@bastion:22", "ssh:p2:1": "user@bastion:22"}

	prod, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})
	staging, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p2:1"})

	c := New(WithRoutes(NewRoutes(leaser)))
	for _, id := range []string{prod, prod, staging} {
		if _, err := c.Send(context.Background(),
			apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: id}); err != nil {
			t.Fatalf("Send(%q): %v", id, err)
		}
	}

	if got := leaser.pool.connectionCount(); got != 1 {
		t.Errorf("%d SSH connections opened across three sends, want 1 — a lease shares when the pool key matches (AD-7)", got)
	}
	// One lease per route, not one per send: the second send on `prod`
	// reuses the route it already has.
	if got := leaser.calls.Load(); got != 2 {
		t.Errorf("the pool was asked for %d leases, want 2 — one per route, and the repeat send reuses its route", got)
	}
	leaser.eachLease(func(i int, l *fakeLease) {
		if l.closeCount() != 0 {
			t.Errorf("lease %d was closed %d times by the route; a route references a pooled connection and never owns it", i, l.closeCount())
		}
	})
}

// The interval, both ends. A lease is held from the first dial that needed
// one until the connection it references shuts down; from that moment the
// next dial takes a NEW one. Asserted at the route rather than through a
// whole send on purpose: an http.Client keeps its connection alive, so a
// send-level assertion would be waiting on the transport to notice a dead
// socket — a duration, which AGENTS.md forbids — instead of on the thing
// being claimed.
func TestRoutes_ALiveConnectionIsReusedAndALostOneIsReplaced(t *testing.T) {
	srv := newCountingServer(t, "ok")
	addr := strings.TrimPrefix(srv.URL, "http://")
	leaser := newFakeLeaser()
	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})

	route, err := NewRoutes(leaser)(context.Background(), id)
	if err != nil {
		t.Fatalf("resolve the route: %v", err)
	}

	first, err := route.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer func() { _ = first.Close() }()

	// While the connection is live the SAME lease serves every dial: a new
	// lease per dial would be a new pool reference per request.
	second, err := route.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("second dial on a live connection: %v", err)
	}
	defer func() { _ = second.Close() }()
	if got := leaser.calls.Load(); got != 1 {
		t.Fatalf("the pool was asked for %d leases across two dials on a live connection, want 1", got)
	}

	// The connection dies: ssh.tunnelConn closes Done and its own watcher
	// releases the pool reference.
	close(leaser.leaseAt(t, 0).done)

	third, err := route.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial after the connection was lost: %v — the route kept a dead lease", err)
	}
	defer func() { _ = third.Close() }()
	if got := leaser.calls.Load(); got != 2 {
		t.Fatalf("the pool was asked for %d leases, want 2 — a lost connection must be replaced, not reused", got)
	}
	if leaser.leaseAt(t, 1).dialCount() == 0 {
		t.Error("the replacement lease was never dialled")
	}
}

// ─── who resolves the name (httppolicy's Route contract) ───────────────────

// The paired case, and the one the whole feature is for: `localhost` through
// a connection is the service on the FAR SIDE's loopback — an admin port, a
// dev server, a database console on a box somebody has SSH to. It is allowed
// because it is TRUTHFUL rather than by exemption: RFC 6761 §6.3 makes
// `localhost` resolve to loopback wherever it is resolved, so this end can
// say what will be reached, and the direct-tcpip channel then names 127.0.0.1
// for the far side's own stack to answer.
func TestRoutes_HTTPToLocalhostThroughAConnectionReachesTheFarSidesLoopback(t *testing.T) {
	leaser := newFakeLeaser()
	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})

	c := New(WithRoutes(NewRoutes(leaser)))
	_, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: "http://localhost:9443/health"}, Key{RouteID: id})

	// The send still fails — the fake leaser's connection dials nothing —
	// but it must fail at the DIAL and never at the address rule: what is
	// asserted here is that the name was resolved and permitted.
	if errors.Is(err, ErrNameResolvedRemotely) {
		t.Fatalf("localhost was refused as an unresolvable name: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "not a loopback or private address") {
		t.Fatalf("localhost was refused by the http:// address rule: %v", err)
	}
	if leaser.pool.connectionCount() == 0 {
		t.Error("no connection was opened: the send never reached the far side at all")
	}
}

// A tunnelled route cannot answer "which addresses will this dial reach" for
// a NAME: the far side resolves it (§7.1). The contract says such a route
// must return an error rather than a guess, and the guess would be a real
// hole — this machine's resolver answering for a name in the far side's
// network, checked against the http:// address rule, and then dialled
// somewhere else entirely.
func TestRoutes_HTTPToANameThroughAConnectionIsRefusedRatherThanGuessed(t *testing.T) {
	leaser := newFakeLeaser()
	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})

	c := New(WithRoutes(NewRoutes(leaser)))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: "http://api.internal/health"}, Key{RouteID: id})
	// Phase `connection` and not `resolve`: nothing on this side tried to
	// resolve anything. The route is what cannot answer the question, and
	// the remedy — an address, or https — is about the route.
	if fail := failedAt(t, ex, err, PhaseConnection); !strings.Contains(fail.Reason, ErrNameResolvedRemotely.Error()) {
		t.Fatalf("reason = %q, want ErrNameResolvedRemotely", fail.Reason)
	}
	if leaser.pool.connectionCount() != 0 {
		t.Error("a connection was opened for a request that could never be checked")
	}
}

// And its pair, without which the refusal above would be satisfied by a
// route that refused everything: an http:// URL naming an ADDRESS is
// checkable from this end — the far side resolves nothing — so it goes.
func TestRoutes_HTTPToAnAddressThroughAConnectionIsSentAndChecked(t *testing.T) {
	srv := newCountingServer(t, "checked and sent")
	leaser := newFakeLeaser()
	id, _ := RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:p1:1"})

	// httptest listens on 127.0.0.1, so its URL is an address literal and
	// the address rule permits http:// to it.
	c := New(WithRoutes(NewRoutes(leaser)))
	ex, err := c.Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: srv.URL}, Key{RouteID: id})
	got := answered(t, ex, err)
	if got.Text != "checked and sent" {
		t.Errorf("Text = %q, want the far side's answer", got.Text)
	}
}
