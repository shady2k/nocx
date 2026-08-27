package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
	gossh "golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// TunnelConn — the owned connection lease for a forward (spec §7.3)
// ---------------------------------------------------------------------------

func TestTunnelConn_ImplementsInterface(t *testing.T) {
	var _ TunnelConn = (*tunnelConn)(nil)
}

// tunnelTestClient builds a RealClient pointed at the test server, cleaned up
// with the test.
func tunnelTestClient(t *testing.T, srv *testSSHServer) *RealClient {
	t.Helper()
	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(log.NewSlogAdapter(nil), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func tunnelConnectOpts(srv *testSSHServer) []ConnectOption {
	return []ConnectOption{
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
	}
}

// startEchoTarget listens on a loopback port and echoes every accepted
// connection back, standing in for the "remote destination" of a forward.
func startEchoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo target listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// deadPort returns an address on which nothing listens (the listener is
// closed before returning).
func deadPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dead port listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestTunnelConn_PaneTeardownDoesNotKillForward is the lifetime invariant the
// whole ownership model exists for (spec §7.3): a forward holds its OWN
// pooled reference. Closing the tab that created it must not kill it — the
// shared connection stays up for the forward's reference, and the forward
// keeps dialing. Only the forward's own Close releases it.
func TestTunnelConn_PaneTeardownDoesNotKillForward(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	// The "tab": an ordinary Connect. It holds one pooled reference.
	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// The forward: its own lease on the SAME pooled connection (one dial,
	// two references — the pool entry count stays 1).
	lease, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn: %v", err)
	}
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("pool count after tab + tunnel = %d, want 1 (one shared connection)", got)
	}

	// Tab teardown: the tab releases its reference; the forward's keeps the
	// connection alive.
	_ = tab.Close()
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("pool count after tab close = %d, want 1 (forward still holds a ref)", got)
	}

	// The forward still works: a direct-tcpip round trip through the shared
	// connection.
	target := startEchoTarget(t)
	remote, err := lease.Dial(target)
	if err != nil {
		t.Fatalf("Dial after tab close: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.Write([]byte("ping")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(remote, buf); err != nil {
		t.Fatalf("read through tunnel: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("round trip = %q, want %q", buf, "ping")
	}

	// The forward's own Close releases the last reference; the connection
	// closes and leaves the pool.
	_ = lease.Close()
	if got := client.pool.Count(); got != 0 {
		t.Fatalf("pool count after lease close = %d, want 0", got)
	}
}

// TestTunnelConn_RefusedTargetDoesNotKillConnection proves one failed
// direct-tcpip open — the remote target refusing — leaves the connection
// fully usable. This is the transport half of spec §7.1 trap 4: a failing
// stream must not kill the forward.
func TestTunnelConn_RefusedTargetDoesNotKillConnection(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	lease, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn: %v", err)
	}
	defer func() { _ = lease.Close() }()

	// First stream: the target refuses. The server accepts the channel and
	// then fails to reach the target, so the refusal surfaces on the stream
	// (read fails) — either way, the connection must survive.
	dead, err := lease.Dial(deadPort(t))
	if err == nil {
		buf := make([]byte, 1)
		if _, rerr := dead.Read(buf); rerr == nil {
			_ = dead.Close()
			t.Fatal("stream to refused target: expected a failure, got a live stream")
		}
		_ = dead.Close()
	}

	// Second stream on the SAME connection succeeds — the refusal did not
	// touch the transport.
	target := startEchoTarget(t)
	remote, err := lease.Dial(target)
	if err != nil {
		t.Fatalf("Dial after refused stream: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if _, err := remote.Write([]byte("ping")); err != nil {
		t.Fatalf("write after refused stream: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(remote, buf); err != nil {
		t.Fatalf("read after refused stream: %v", err)
	}
}

// TestTunnelConn_ConnectionLossClosesDoneAndReclaimsPool proves the loss
// path: transport death closes the lease's Done and releases the lease's own
// reference. The tab's session watcher releases its reference too, so the
// dead entry is reclaimed from the pool, and a fresh lease on the same key
// dials a NEW connection instead of reusing the corpse.
func TestTunnelConn_ConnectionLossClosesDoneAndReclaimsPool(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	// A tab and a forward share one connection.
	tab, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	lease, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn: %v", err)
	}
	if got := client.pool.Count(); got != 1 {
		t.Fatalf("pool count = %d, want 1 (one shared connection)", got)
	}

	// Kill the server side: the client transport fails, as on real loss.
	srv.killConns()

	select {
	case <-lease.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("lease Done did not close after connection loss")
	}
	if lostErr := lease.LostErr(); lostErr == nil {
		t.Fatal("LostErr = nil after connection loss, want the transport error")
	}

	// Both references release on loss: the lease's own watcher and the tab's
	// session watcher. The dead entry is reclaimed — nothing lingers.
	waittest.WaitForTimeoutDetail(t, "the pooled connection to be reclaimed after loss", 5*time.Second, func() string {
		return fmt.Sprintf("pool count after loss = %d, want 0 (dead entry reclaimed)", client.pool.Count())
	}, func() bool {
		return client.pool.Count() == 0
	})
	_ = tab.Close() // already released; must be a no-op, not a double release

	// A fresh lease dials a NEW connection rather than the dead entry, and
	// works.
	lease2, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn after loss: %v", err)
	}
	defer func() { _ = lease2.Close() }()
	target := startEchoTarget(t)
	remote, err := lease2.Dial(target)
	if err != nil {
		t.Fatalf("Dial on fresh lease: %v", err)
	}
	defer func() { _ = remote.Close() }()
}

// TestTunnelConn_CloseIsNotConnectionLoss pins the semantics the strategy
// depends on: an intentional Close while the connection is still shared must
// NOT close Done — only real transport shutdown does. A strategy that
// treated Close as loss would report "connection lost" for a user stop.
func TestTunnelConn_CloseIsNotConnectionLoss(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	// Two leases share one connection so Close leaves it alive.
	leaseA, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn A: %v", err)
	}
	leaseB, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn B: %v", err)
	}
	defer func() { _ = leaseB.Close() }()

	_ = leaseA.Close()

	select {
	case <-leaseA.Done():
		t.Fatal("Done closed on intentional Close while the connection is shared")
	default:
	}

	// Real transport death then closes Done.
	srv.killConns()
	select {
	case <-leaseB.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after real connection loss")
	}
}

// TestTunnelConn_DialAfterCloseFails proves a spent lease refuses further
// streams: Dial fails with the closed error after Close, and with the lost
// error after transport death — never silently opening a channel on a
// connection the forward no longer owns.
func TestTunnelConn_DialAfterCloseFails(t *testing.T) {
	srv := startTestSSHServer(t)
	client := tunnelTestClient(t, srv)
	opts := tunnelConnectOpts(srv)

	target := startEchoTarget(t)

	// After Close: ErrTunnelConnClosed.
	lease, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn: %v", err)
	}
	_ = lease.Close()
	if c, dErr := lease.Dial(target); dErr == nil {
		_ = c.Close()
		t.Fatal("Dial after Close: expected error, got nil")
	} else if !errors.Is(dErr, ErrTunnelConnClosed) {
		t.Fatalf("Dial after Close: err = %v, want ErrTunnelConnClosed", dErr)
	}

	// After loss: ErrTunnelConnLost.
	// The first lease is spent, so the server must be down to nothing before
	// the second is opened: otherwise the count below cannot tell the two
	// apart, and killConns could close a connection this test already
	// finished with while the one it means is still in the backlog.
	srv.waitLiveConns(0)

	lease2, err := client.TunnelConn(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("TunnelConn 2: %v", err)
	}
	// TunnelConn returns on the CLIENT's handshake; the server accepts
	// sequentially and may not have this connection yet (nocx-zlvw).
	srv.waitLiveConns(1)

	srv.killConns()
	select {
	case <-lease2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close")
	}
	if c, dErr := lease2.Dial(target); dErr == nil {
		_ = c.Close()
		t.Fatal("Dial after loss: expected error, got nil")
	} else if !errors.Is(dErr, ErrTunnelConnLost) {
		t.Fatalf("Dial after loss: err = %v, want ErrTunnelConnLost", dErr)
	}
}
