//go:build nocx_local_ssh

package tunnelchan_test

// The lease's own contract, driven end to end: the coordinator asks THIS
// MACHINE'S HELPER for channels, the helper dials a real ssh server, and every
// assertion is about what a person's forward would see.
//
// Each success is paired with the failure it has to be told apart from, which
// is the pairing the acceptance asks for: a dial refused by the far side, a
// listener refused by the server's policy, and a stream lost mid-flight — the
// three ways one of these can fail without the lease itself being over.

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestDialCarriesBytesThroughTheHelper is the -L/-D transport: a direct-tcpip
// channel the COORDINATOR asked for, opened by the helper on a connection the
// helper dialed, carrying bytes to a target on the far side's network.
func TestDialCarriesBytesThroughTheHelper(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	target := echoTarget(t)
	conn, err := lease.Dial(target)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	payload := []byte("ping through the helper's channel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}

	// The password crossed ONE hop and in ONE direction: the helper asked the
	// coordinator for it (proto.OpSecret) because it must PRESENT it, and the
	// fixture saw exactly one attempt.
	if attempts := f.authAttempts(); len(attempts) != 1 {
		t.Fatalf("the fixture saw %d password attempts, want 1: %q", len(attempts), attempts)
	}
}

// TestDialRefusedByTheFarSideDoesNotEndTheLease pairs the refusal with the
// success above, and pins the property spec §7.1 trap 4 is about: one stream
// failing — a target nobody listens on — leaves the lease and every other
// stream of it untouched.
func TestDialRefusedByTheFarSideDoesNotEndTheLease(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	conn, err := lease.Dial(deadTarget(t))
	if err == nil {
		// The far side may also accept the open and then fail the connection;
		// either shape is a refusal of that stream, and neither may end the
		// lease below.
		buf := make([]byte, 1)
		if _, rerr := conn.Read(buf); rerr == nil {
			_ = conn.Close()
			t.Fatal("a stream to a refused target read successfully")
		}
		_ = conn.Close()
	}

	select {
	case <-lease.Done():
		t.Fatalf("the lease ended when ONE stream was refused (LostErr = %v)", lease.LostErr())
	default:
	}

	// And the very next channel on the same lease works.
	target := echoTarget(t)
	good, err := lease.Dial(target)
	if err != nil {
		t.Fatalf("Dial after a refused stream: %v", err)
	}
	defer func() { _ = good.Close() }()
	if _, err := good.Write([]byte("still here")); err != nil {
		t.Fatalf("write after a refused stream: %v", err)
	}
	buf := make([]byte, len("still here"))
	if _, err := io.ReadFull(good, buf); err != nil {
		t.Fatalf("read after a refused stream: %v", err)
	}
}

// TestListenCarriesBytesAndReportsTheServersPort is the -R transport, and the
// remote lifecycle channel's: a listener the far side opened at the helper's
// request, with an accepted connection delivered back as a channel.
func TestListenCarriesBytesAndReportsTheServersPort(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	ln, err := lease.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// The concrete type is load-bearing: the remote lifecycle adapter asserts
	// on *net.TCPAddr to read the port the shell must connect to.
	actual, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener Addr = %T, want *net.TCPAddr", ln.Addr())
	}
	if wantHost, wantPort := f.lastBind(); wantHost != "127.0.0.1" || wantPort == 0 || actual.Port != wantPort {
		t.Fatalf("listen reported %s but the server bound %q:%d", ln.Addr(), wantHost, wantPort)
	}

	// The "remote machine" dials the server's listener.
	remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(actual.Port)))
	if err != nil {
		t.Fatalf("remote dial to the server's listener: %v", err)
	}
	defer func() { _ = remote.Close() }()
	_ = remote.SetDeadline(time.Now().Add(10 * time.Second))

	accepted, err := acceptWithin(t, ln, 10*time.Second)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer func() { _ = accepted.Close() }()

	local, err := net.Dial("tcp", echoTarget(t))
	if err != nil {
		t.Fatalf("local destination dial: %v", err)
	}
	defer func() { _ = local.Close() }()
	go relay(t, accepted, local)

	payload := "ping through the helper's forward"
	if _, err := remote.Write([]byte(payload)); err != nil {
		t.Fatalf("write from the remote side: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(remote, buf); err != nil {
		t.Fatalf("read the echo on the remote side: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("round trip = %q, want %q", buf, payload)
	}
}

// TestListenRefusedByPolicy pairs the success above: the server refuses the
// forward — AllowTcpForwarding off, or a bind outside PermitListen — and the
// refusal reaches the caller as a refusal, not as a dial failure.
//
// Both policies are exercised because they are the SAME wire signal, which is
// exactly what the -R strategy's error has always disclosed by naming both.
func TestListenRefusedByPolicy(t *testing.T) {
	t.Run("allow-forwarding-off", func(t *testing.T) {
		f := startFixture(t)
		f.setAllowForward(false)
		s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
		lease := s.lease(t)

		ln, err := lease.Listen("127.0.0.1:0")
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen: expected a refusal, got a listener")
		}
		if !strings.Contains(err.Error(), "denied by peer") {
			t.Fatalf("Listen error = %q, want the server's own refusal in it", err)
		}
		// Nothing was left listening on the far side.
		if _, port := f.lastBind(); port != 0 {
			t.Fatalf("the fixture recorded a bind (%d) for a refused request", port)
		}
	})

	t.Run("permit-listen-mismatch", func(t *testing.T) {
		f := startFixture(t)
		f.setPermitListen(func(string, int) bool { return false })
		s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
		lease := s.lease(t)

		ln, err := lease.Listen("127.0.0.1:0")
		if err == nil {
			_ = ln.Close()
			t.Fatal("Listen: expected a refusal, got a listener")
		}
		if !strings.Contains(err.Error(), "denied by peer") {
			t.Fatalf("Listen error = %q, want the server's own refusal in it", err)
		}
	})
}

// TestListenHostnameBindReportsTheAddressTheServerChose pins the transport's
// answer for a hostname bind: the server resolves it, and the address that
// comes back is the server's rather than a verified bind — the fact the -R
// bind caveat exists to disclose.
func TestListenHostnameBindReportsTheAddressTheServerChose(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	ln, err := lease.Listen("localhost:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("parse the listener's Addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse the listener's port: %v", err)
	}
	if host != "0.0.0.0" {
		t.Fatalf("hostname bind reported host %q, want 0.0.0.0 (unverifiable on the wire)", host)
	}
	if _, wantPort := f.lastBind(); port != wantPort {
		t.Fatalf("listener reported port %d but the server allocated %d", port, wantPort)
	}
}

// TestCloseEndsThisLeaseAndNotTheHelpersConnection is the lifetime invariant
// the ownership model exists for (spec §7.3), restated for the helper: a lease
// is a GROUP of streams, and closing one must not take the transport away from
// the next. What used to be "the tab's reference" is now the helper's pooled
// connection, which a coordinator no longer holds at all — so the property to
// prove is that the second lease still works after the first is released.
func TestCloseEndsThisLeaseAndNotTheHelpersConnection(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	first := s.lease(t)
	second := s.lease(t)

	target := echoTarget(t)
	open, err := first.Dial(target)
	if err != nil {
		t.Fatalf("Dial on the first lease: %v", err)
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	// The stream the lease held is over...
	if _, writeErr := open.Write([]byte("x")); writeErr == nil {
		buf := make([]byte, 1)
		if _, rerr := open.Read(buf); rerr == nil {
			_ = open.Close()
			t.Fatal("a stream of a closed lease read successfully")
		}
	}
	_ = open.Close()

	// ...and the next lease, on the same helper connection and the same pooled
	// ssh connection, is untouched.
	conn, err := second.Dial(target)
	if err != nil {
		t.Fatalf("Dial on the second lease after the first was closed: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write on the second lease: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read on the second lease: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("round trip = %q, want %q", buf, "ping")
	}
}

// TestASpentLeaseRefusesAndAnIntentionalCloseIsNotLoss pins the two states a
// strategy switches on: after Close, Dial refuses with the closed error and
// Done does NOT close (a user stop must not be reported as connection loss).
func TestASpentLeaseRefusesAndAnIntentionalCloseIsNotLoss(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-lease.Done():
		t.Fatal("Done closed on an intentional Close")
	default:
	}
	if conn, err := lease.Dial(echoTarget(t)); err == nil {
		_ = conn.Close()
		t.Fatal("Dial after Close: expected a refusal, got a connection")
	} else if !errors.Is(err, ssh.ErrTunnelConnClosed) {
		t.Fatalf("Dial after Close: err = %v, want ErrTunnelConnClosed", err)
	}
}

// TestLosingTheHelperClosesDoneAndRefusesDials is the coordinator-side loss:
// the connection to this machine's daemon ends, so nothing this lease holds can
// work — Done closes with the cause and a later Dial refuses rather than
// waiting for an answer that is not coming.
func TestLosingTheHelperClosesDoneAndRefusesDials(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	s.killHelperTransport()

	waittest.WaitForTimeoutDetail(t, "the lease's Done to close", 5*time.Second,
		func() string { return "Done is still open after the helper connection was closed" },
		func() bool {
			select {
			case <-lease.Done():
				return true
			default:
				return false
			}
		})
	if err := lease.LostErr(); err == nil {
		t.Fatal("LostErr = nil after the helper connection was lost")
	}
	if conn, err := lease.Dial(echoTarget(t)); err == nil {
		_ = conn.Close()
		t.Fatal("Dial after the helper was lost: expected a refusal")
	} else if !errors.Is(err, ssh.ErrTunnelConnLost) {
		t.Fatalf("Dial after loss: err = %v, want ErrTunnelConnLost", err)
	}
}

// TestAStreamLostMidFlightEndsWithItsCause is the failure path a person meets
// when the far side's network dies with a forward running: the connection
// under the channel goes away, the stream ENDS rather than parking, and the
// lease's Done stays open.
//
// The end is an EOF and not a named cause, and that is faithful rather than
// lossy: x/crypto/ssh reports a channel's end that way whether the peer closed
// it or the transport died under it, and the coordinator read exactly this
// signal from its own pooled lease before the dial moved to the helper. What
// names a lost transport is a watcher — the listener's (asserted below) and
// the helper connection's (asserted in the loss test above).
func TestAStreamLostMidFlightEndsWithItsCause(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	conn, err := lease.Dial(echoTarget(t))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Proof the stream is live BEFORE the loss: the echo comes back through
	// the helper. (Reading only after the kill would be answered by bytes
	// still in flight, which is the drain-before-end rule and not a defect.)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	f.waitLiveConns(1)

	f.killConns()

	// The next read reports the end. It runs on its own goroutine because this
	// transport answers SetReadDeadline with "not supported" (channelConn's own
	// note: the pool lease it replaced did the same), so the bound has to come
	// from the test.
	reads := make(chan error, 1)
	go func() {
		_, rerr := conn.Read(make([]byte, 1))
		reads <- rerr
	}()
	select {
	case rerr := <-reads:
		if rerr == nil {
			t.Fatal("a read on a lost stream returned a byte")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a read on a lost stream is still parked: the end was never reported")
	}

	select {
	case <-lease.Done():
		t.Fatal("the lease itself ended when one of its streams was lost")
	default:
	}
}

// TestAListenerEndedByTheFarSideIsALoss is the one loss a lease DOES report:
// the listener is what the caller asked for, the far side's connection died
// under it, and a caller holding Accept would otherwise wait for ever. Both
// ends of that are asserted — the lease's Done and the listener's Accept.
func TestAListenerEndedByTheFarSideIsALoss(t *testing.T) {
	f := startFixture(t)
	s := newStand(t, f, &coordinator{verdict: proto.HostKeyTrusted})
	lease := s.lease(t)

	ln, err := lease.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	f.waitLiveConns(1)

	f.killConns()

	waittest.WaitForTimeoutDetail(t, "the lease's Done to close", 5*time.Second,
		func() string {
			return "Done is still open after the listener's connection died"
		},
		func() bool {
			select {
			case <-lease.Done():
				return true
			default:
				return false
			}
		})
	if err := lease.LostErr(); err == nil {
		t.Fatal("LostErr = nil after the listener was lost")
	}
	// Accept is released rather than parked for ever.
	done := make(chan error, 1)
	go func() {
		_, aerr := ln.Accept()
		done <- aerr
	}()
	select {
	case aerr := <-done:
		if aerr == nil {
			t.Fatal("Accept returned a connection after the listener's connection died")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Accept is still parked after the listener's connection died")
	}
}
