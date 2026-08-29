package transport

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// The discovery handshake hands a client the address to connect to, not a
// port it has to pair with a host of its own choosing (design §4). These
// two assert the accessor answers that question, and answers "" rather than
// a plausible-looking address before there is a listener to describe.

func TestAddrReportsTheBoundLoopbackAddress(t *testing.T) {
	sess := newRegWithStub(log.NewSlogAdapter(nil))
	ws := NewWSServer(log.NewSlogAdapter(nil), sess)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	host, port, err := net.SplitHostPort(ws.Addr())
	if err != nil {
		t.Fatalf("Addr() = %q, not a host:port pair: %v", ws.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Errorf("Addr() host = %q, want loopback", host)
	}
	if port != strconv.Itoa(ws.Port()) {
		t.Errorf("Addr() port = %q, want the bound port %d", port, ws.Port())
	}
	// And it names something a client can actually reach.
	conn, err := net.Dial("tcp", ws.Addr())
	if err != nil {
		t.Fatalf("dial %s: %v", ws.Addr(), err)
	}
	_ = conn.Close()
}

func TestAddrIsEmptyBeforeStart(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	if got := ws.Addr(); got != "" {
		t.Errorf("Addr() before Start = %q, want empty", got)
	}
}
