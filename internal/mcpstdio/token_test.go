package mcpstdio

// THE BRIDGE PRESENTS THE PANE'S BEARER (nocx-50w7p.16, AC3).
//
// The endpoint reads its preamble before it decides anything — the helper's pane
// record and then this bridge's bearer — so the bearer has to be on the wire
// before the first request, on the connection that IS this session's admission
// interval (ADR-0058). A bridge that presented it a call too late would have its
// first request answered with a refusal for a bearer that was still in flight.
//
// These tests drive the link rather than the MCP protocol: what is asserted is
// the bytes that precede a request, and a test through the protocol would be
// asserting the same bytes one layer further from the code that writes them.
//
// WHAT THIS FILE DOES NOT COVER, said rather than implied: a RECONNECT (a second
// connection after the first drops) and the bridge's own logs and its children's
// environments. The first has no missing code to find — attach presents the
// bearer on every dial it makes — but nothing here proves it, and the second is
// asserted where the value enters the process rather than where it could leave.

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// bearerDialer hands the bridge one end of an in-memory connection and READS
// the other end from the moment the dial returns.
//
// The reader is not a convenience. net.Pipe is synchronous: the bridge writes the
// bearer inside attach and nothing returns until somebody reads it, so a test
// that read afterwards would deadlock — measured, which is why this dialer reads
// from the start and the test asks it what arrived.
type bearerDialer struct {
	mu       sync.Mutex
	received []*bytes.Buffer
	endpoint []net.Conn
	dialed   chan struct{}
	// arrived is signalled on the first byte, so an absence can be asserted as
	// an event that did not happen rather than as a duration that elapsed.
	arrived chan struct{}
}

func newBearerDialer() *bearerDialer {
	return &bearerDialer{dialed: make(chan struct{}, 4), arrived: make(chan struct{}, 1)}
}

func (d *bearerDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	bridge, endpoint := net.Pipe()
	// ONE BUFFER PER CONNECTION, appended in dial order: a reconnect's bytes are
	// not the first connection's continued, and a test that pooled them could
	// not tell a bridge that presents the bearer again from one that does not.
	buf := &bytes.Buffer{}
	d.mu.Lock()
	d.received = append(d.received, buf)
	d.endpoint = append(d.endpoint, endpoint)
	d.mu.Unlock()
	d.dialed <- struct{}{}
	go func() {
		raw := make([]byte, 256)
		for {
			n, err := endpoint.Read(raw)
			if n > 0 {
				d.mu.Lock()
				buf.Write(raw[:n])
				d.mu.Unlock()
				select {
				case d.arrived <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return bridge, nil
}

// endpointAt is the i-th connection's far end, so a test can DROP one for real
// rather than telling the link it is gone.
func (d *bearerDialer) endpointAt(i int) net.Conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.endpoint) {
		return nil
	}
	return d.endpoint[i]
}

// awaitBytesAt waits until at least n bytes have arrived on connection i.
func (d *bearerDialer) awaitBytesAt(t *testing.T, i, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.writtenAt(i); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("connection %d carried %d bytes, want at least %d", i, len(d.writtenAt(i)), n)
	return ""
}

// connections is how many times the bridge has dialed.
func (d *bearerDialer) connections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.received)
}

// writtenAt is the i-th connection's bytes, empty when it does not exist yet.
func (d *bearerDialer) writtenAt(i int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.received) {
		return ""
	}
	return d.received[i].String()
}

// written is what the bridge has put on the connection so far.
func (d *bearerDialer) written() string { return d.writtenAt(0) }

// awaitBytes waits until at least n bytes have arrived on the first connection.
func (d *bearerDialer) awaitBytes(t *testing.T, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.written(); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the bridge wrote %d bytes, want at least %d", len(d.written()), n)
	return ""
}

const testBearer = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestTheBridgePresentsTheBearerBeforeAnyRequest(t *testing.T) {
	dialer := newBearerDialer()
	link := newEndpointLink("sock", dialer, testBearer, discardLogger)
	if _, _, err := link.attach(context.Background()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	got := dialer.awaitBytes(t, len(testBearer)+1)
	if !strings.HasPrefix(got, testBearer+"\n") {
		t.Fatalf("the connection began with %q, want the bearer and nothing before it", got)
	}
	// AND IT IS FIRST: no attach may put a byte in front of it, so once the
	// bearer has arrived the connection is quiet until the caller writes.
	if got != testBearer+"\n" {
		t.Fatalf("the bridge wrote %q after the bearer, before any request", got[len(testBearer)+1:])
	}
}

func TestTheBridgeWritesNoBearerWhenItHasNone(t *testing.T) {
	dialer := newBearerDialer()
	link := newEndpointLink("sock", dialer, "", discardLogger)
	if _, _, err := link.attach(context.Background()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// A local agent's connection is NOT on the helper's lane and the endpoint
	// reads no preamble on it: a bearer written here would be bytes the far side
	// never asked for, in front of a request. The assertion is the ABSENCE of a
	// write — nothing has to arrive for it to be true, so the bound is on an
	// event that must not happen rather than on a state that must be reached.
	select {
	case <-dialer.arrived:
		t.Fatalf("a bridge with no bearer wrote %q to the endpoint", dialer.written())
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTheRequestFollowsTheBearerOnTheSameConnection — the ORDER, on one
// connection, byte for byte: the bearer and then the caller's request. The
// endpoint reads its preamble before anything else, so a request that overtook
// the bearer would be refused for a value that arrived a moment later.
func TestTheRequestFollowsTheBearerOnTheSameConnection(t *testing.T) {
	dialer := newBearerDialer()
	link := newEndpointLink("sock", dialer, testBearer, discardLogger)
	conn, _, err := link.attach(context.Background())
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if got := dialer.awaitBytes(t, len(testBearer)+1); got != testBearer+"\n" {
		t.Fatalf("the connection began with %q, want the bearer alone", got)
	}
	// THE CALLER'S REQUEST, through the same send path the server uses, so what
	// is asserted is the order the bridge actually produces and not a frame this
	// test wrote itself.
	if _, err := conn.send(context.Background(), 1, "tools/list", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	got := dialer.awaitBytes(t, len(testBearer)+1+1)
	if !strings.HasPrefix(got, testBearer+"\n") {
		t.Fatalf("the connection began with %q, want the bearer and nothing before it", got)
	}
	after := got[len(testBearer)+1:]
	if !strings.Contains(after, "tools/list") {
		t.Fatalf("the bearer was followed by %q, want the caller's request", after)
	}
	if strings.Index(got, "tools/list") < len(testBearer)+1 {
		t.Fatalf("the request overtook the bearer: %q", got)
	}
}

// TestAReconnectPresentsTheBearerAgain — AC3's remaining half. A bearer
// presented once and never again is a tool that stops working after the first
// blip: the connection IS the admission interval (ADR-0058), so every connection
// the bridge has to establish is a connection the endpoint has to admit.
func TestAReconnectPresentsTheBearerAgain(t *testing.T) {
	dialer := newBearerDialer()
	link := newEndpointLink("sock", dialer, testBearer, discardLogger)
	ctx := context.Background()

	if _, _, err := link.attach(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if got := dialer.awaitBytes(t, len(testBearer)+1); !strings.HasPrefix(got, testBearer+"\n") {
		t.Fatalf("the first connection began with %q, want the bearer", got)
	}

	// THE CONNECTION DROPS, as one does mid-session.
	if err := dialer.endpointAt(0).Close(); err != nil {
		t.Fatalf("drop the connection: %v", err)
	}

	// The bridge dials again. The loop is the wait: attach reuses its connection
	// until the dead one is observed as gone, and the dialer says when a second
	// dial happened — an observable, not a duration.
	deadline := time.Now().Add(5 * time.Second)
	for dialer.connections() < 2 {
		if _, _, err := link.attach(ctx); err != nil {
			t.Fatalf("re-attach: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the bridge never dialled again after its connection dropped")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// AND THE NEW CONNECTION CARRIES THE BEARER: its own bytes, not the first
	// connection's continued.
	if got := dialer.awaitBytesAt(t, 1, len(testBearer)+1); !strings.HasPrefix(got, testBearer+"\n") {
		t.Fatalf("the reconnected connection began with %q, want the bearer again", got)
	}
}
