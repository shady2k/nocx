package ssh

import (
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// TestKeepaliveTickerStopsViaStopFn proves that the keepalive goroutine exits
// when the stop function returned by startKeepalive is called. A leaked ticker
// per connection is a worse bug than the one keepalive fixes.
func TestKeepaliveTickerStopsViaStopFn(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	client := dialTestClient(t, srv)
	defer func() { _ = client.Close() }()

	// Start keepalive with a short interval so the ticker fires if it leaks.
	stop, done := startKeepalive(client, 10*time.Millisecond, 3)
	if stop == nil {
		t.Fatal("startKeepalive returned nil stop for non-zero interval")
	}

	// Stop the goroutine and verify it terminates promptly.
	stop()

	select {
	case <-done:
		// Goroutine exited — success.
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive goroutine did not stop within 5s — ticker leak detected")
	}
}

// TestKeepaliveTickerStopsOnClientClose proves that the keepalive goroutine
// also exits when the underlying gossh.Client is closed (simulating what
// pooledSSHConn.Close does: call stopKeepalive before closing the transport).
// This is the end-to-end path: the pool closes the connection, then the
// keepalive goroutine's stop channel is closed via the stopKeepalive func.
func TestKeepaliveTickerStopsOnClientClose(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	client := dialTestClient(t, srv)

	stop, done := startKeepalive(client, 10*time.Millisecond, 3)
	if stop == nil {
		t.Fatal("startKeepalive returned nil stop for non-zero interval")
	}

	// Simulate what pooledSSHConn.Close does: stop keepalive then close client.
	stop()
	_ = client.Close()

	select {
	case <-done:
		// Goroutine exited — success.
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive goroutine did not stop within 5s after Close")
	}
}

// TestKeepaliveDisabledNilStop verifies that a zero interval returns nil stop
// and nil done, so the caller can safely branch on stop == nil.
func TestKeepaliveDisabledNilStop(t *testing.T) {
	stop, done := startKeepalive(nil, 0, 3)
	if stop != nil {
		t.Fatal("expected nil stop for zero interval")
	}
	if done != nil {
		t.Fatal("expected nil done for zero interval")
	}
}

// dialTestClient opens a direct *gossh.Client to the test server using the
// server's own user signer (both host and user auth use the same key in
// startTestSSHServer). This produces a real client usable with startKeepalive.
func dialTestClient(t *testing.T, srv *testSSHServer) *gossh.Client {
	t.Helper()
	cfg := &gossh.ClientConfig{
		User: "test",
		Auth: []gossh.AuthMethod{
			gossh.PublicKeys(srv.userSigner),
		},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         5 * time.Second,
	}
	client, err := gossh.Dial("tcp", srv.addr, cfg)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	return client
}
