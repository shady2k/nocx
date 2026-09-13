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
	received bytes.Buffer
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
	d.dialed <- struct{}{}
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := endpoint.Read(buf)
			if n > 0 {
				d.mu.Lock()
				d.received.Write(buf[:n])
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

// written is what the bridge has put on the connection so far.
func (d *bearerDialer) written() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.received.String()
}

// awaitBearer waits until at least n bytes have arrived, without a duration.
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
	link := newEndpointLink("sock", dialer, testBearer)
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
	link := newEndpointLink("sock", dialer, "")
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
