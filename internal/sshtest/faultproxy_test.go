package sshtest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

const (
	// readBound is how long a read that SHOULD carry bytes may take. It is a
	// ceiling for a slow machine, never a statement about the fault.
	readBound = 10 * time.Second
	// silenceWindow is how long a read that should carry nothing is left
	// waiting. It only has to outlast this machine's scheduling; nothing is
	// asserted about what might arrive after it.
	silenceWindow = 250 * time.Millisecond
)

// upstream is a TCP server the proxy stands in front of. It hands every
// accepted connection to the test, so the test can speak from the server's own
// side and assert what actually arrived there — a fault staged in one
// direction is only proven by looking at the end that should not have heard
// anything.
type upstream struct {
	addr     string
	accepted chan net.Conn
}

func startUpstream(t *testing.T) *upstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	u := &upstream{addr: ln.Addr().String(), accepted: make(chan net.Conn, 8)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u.accepted <- c
		}
	}()
	return u
}

// dialPair points a client at the proxy and returns both ends of the flow: the
// client's socket and the connection the upstream accepted for it.
func dialPair(t *testing.T, p *FaultProxy, u *upstream) (client, server net.Conn) {
	t.Helper()
	client, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	waittest.WaitFor(t, "the proxy to connect the client to the upstream", func() bool {
		select {
		case server = <-u.accepted:
			return true
		default:
			return false
		}
	})
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

// roundTrip carries bytes in both directions and leaves both sockets drained,
// so a test that switches the wire next observes only what the switch did.
func roundTrip(t *testing.T, client, server net.Conn, payload []byte) {
	t.Helper()
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("the client could not write: %v", err)
	}
	mustRead(t, server, payload, "the server's view of the request")
	reply := append([]byte("reply:"), payload...)
	if _, err := server.Write(reply); err != nil {
		t.Fatalf("the server could not write: %v", err)
	}
	mustRead(t, client, reply, "the client's view of the reply")
}

// mustRead requires exactly want, byte for byte.
func mustRead(t *testing.T, c net.Conn, want []byte, what string) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(readBound)); err != nil {
		t.Fatalf("%s: set read deadline: %v", what, err)
	}
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("%s: %v (read %q, want %q)", what, err, got, want)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// expectSilence requires that nothing is delivered on c. A fault that withholds
// delivery is observed as a read with nothing to read, which is the only
// evidence a socket can give for an absence.
func expectSilence(t *testing.T, c net.Conn, what string) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(silenceWindow)); err != nil {
		t.Fatalf("%s: set read deadline: %v", what, err)
	}
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	var one [1]byte
	n, err := c.Read(one[:])
	if n > 0 {
		t.Fatalf("%s: read %q", what, one[:n])
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("%s: read ended with %v, want a read that had nothing to read", what, err)
	}
}

func TestFaultProxy_PassRelaysBytesUnchangedInBothDirections(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)

	// Bytes a line-oriented relay would mangle: a NUL, CR LF, and the high
	// half that a text conversion folds away.
	request := []byte("PUT /a b?c\r\n\x00\xfe\xff")
	if _, err := client.Write(request); err != nil {
		t.Fatalf("client write: %v", err)
	}
	mustRead(t, server, request, "the server's view of the client's bytes")

	reply := []byte("HTTP/1.1 200 OK\r\n\r\n\x00\x01\xfe")
	if _, err := server.Write(reply); err != nil {
		t.Fatalf("server write: %v", err)
	}
	mustRead(t, client, reply, "the client's view of the server's bytes")
}

func TestFaultProxy_BlackholeDeliversNothingEitherWay(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)
	roundTrip(t, client, server, []byte("alive"))

	// Raised under a live flow, because that is the shape of a NAT that drops
	// it: the connection was working a moment ago.
	p.Blackhole()

	// The writes SUCCEED. That is half of what makes a blackhole quiet: a
	// sender whose write failed would learn about the fault at once.
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("a black-holed flow refused the client's write: %v", err)
	}
	if _, err := server.Write([]byte("pong")); err != nil {
		t.Fatalf("a black-holed flow refused the server's write: %v", err)
	}

	expectSilence(t, client, "the client heard something through a blackhole")
	expectSilence(t, server, "the server heard something through a blackhole")

	// And it is a switch, not a death: the flow the blackhole kept open
	// carries bytes again once it is lifted.
	p.Pass()
	roundTrip(t, client, server, []byte("back"))
}

// TestFaultProxy_SilenceEstablishedDeafensOnlyTheFlowsOpenNow is the condition
// a look-only dial is evidence about: the established connection stops
// answering while the same host still accepts a new one. Blackhole cannot stage
// it — it silences the new connection too, which is a host that is gone rather
// than one whose session process was starved.
func TestFaultProxy_SilenceEstablishedDeafensOnlyTheFlowsOpenNow(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)
	roundTrip(t, client, server, []byte("alive"))

	p.SilenceEstablished()

	// The established flow is deaf in both directions, and both writers still
	// succeed — what a starved session process looks like from this side.
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("a silenced flow refused the client's write: %v", err)
	}
	if _, err := server.Write([]byte("pong")); err != nil {
		t.Fatalf("a silenced flow refused the server's write: %v", err)
	}
	expectSilence(t, client, "the client heard the silenced server")
	expectSilence(t, server, "the server heard the silenced client")

	// A connection made AFTER the call is not silenced: this is the same host,
	// still answering, which is the whole reason the established flow is the
	// evidence and not a verdict.
	lateClient, lateServer := dialPair(t, p, up)
	roundTrip(t, lateClient, lateServer, []byte("late"))

	// The first flow stays deaf while the second speaks.
	if _, err := client.Write([]byte("again")); err != nil {
		t.Fatalf("the silenced flow refused a write: %v", err)
	}
	expectSilence(t, server, "the first flow spoke while silenced")

	// Pass delivers everything the silence kept, in the order it was written:
	// the client's two writes reach the server, and the server's reply reaches
	// the client. That is the "and then the host answers" half a probe needs.
	p.Pass()
	mustRead(t, server, []byte("pingagain"), "the bytes the silence kept from the client")
	mustRead(t, client, []byte("pong"), "the bytes the silence kept from the server")

	// And the flow is a working one again.
	roundTrip(t, client, server, []byte("back"))
}

func TestFaultProxy_LossOneWayDeliversOnlyTheOtherDirection(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)

	p.LossOneWay(ClientToServer)
	if _, err := client.Write([]byte("up")); err != nil {
		t.Fatalf("the lost direction refused the write, so the sender learned of it at once: %v", err)
	}
	if _, err := server.Write([]byte("down")); err != nil {
		t.Fatalf("the surviving direction refused the write: %v", err)
	}

	mustRead(t, client, []byte("down"), "the reply half of a one-way loss")
	expectSilence(t, server, "the upstream heard the direction that is lost")

	p.Pass()
	roundTrip(t, client, server, []byte("up again"))
}

func TestFaultProxy_HoldBannerDeliversNothingUntilRelease(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)

	// Raised before the dial, which is the shape of a host that accepts the
	// TCP connection and never completes the SSH version exchange.
	p.HoldBanner()
	client, server := dialPair(t, p, up)

	fromClient := []byte("SSH-2.0-nocx_test_client")
	fromServer := []byte("SSH-2.0-the_server_banner")
	if _, err := client.Write(fromClient); err != nil {
		t.Fatalf("a held flow refused the client's write: %v", err)
	}
	if _, err := server.Write(fromServer); err != nil {
		t.Fatalf("a held flow refused the server's write: %v", err)
	}

	expectSilence(t, client, "the client heard the held server banner")
	expectSilence(t, server, "the upstream heard the held client banner")

	p.Release()

	// Everything held, and only what was held: each side receives exactly
	// what the other wrote while the gate was shut.
	mustRead(t, client, fromServer, "the banner the hold kept")
	mustRead(t, server, fromClient, "the greeting the hold kept")
}

func TestFaultProxy_ResetMakesBothPeersReadFailWithAReset(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)
	roundTrip(t, client, server, []byte("alive"))

	p.Reset()

	assertReset(t, client, "the client")
	assertReset(t, server, "the upstream")
}

// assertReset requires that reading c fails with a connection reset rather than
// an orderly end of file — the difference between a host that was rebooted
// behind the flow and one that closed it.
func assertReset(t *testing.T, c net.Conn, who string) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(readBound)); err != nil {
		t.Fatalf("%s: set read deadline: %v", who, err)
	}
	var one [1]byte
	n, err := c.Read(one[:])
	if err == nil {
		t.Fatalf("%s read %q across a reset, want an error", who, one[:n])
	}
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("%s read error = %v, want a connection reset", who, err)
	}
}

func TestFaultProxy_RefuseFailsNewDialsAndKeepsExistingFlows(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)
	roundTrip(t, client, server, []byte("alive"))

	p.Refuse()

	refused, err := net.Dial("tcp", p.Addr())
	if err == nil {
		_ = refused.Close()
		t.Fatal("a new dial succeeded while the proxy was refusing connections")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("new dial error = %v, want a refused connection", err)
	}

	// The flow that was already relayed never lost its sockets.
	roundTrip(t, client, server, []byte("still here"))

	// And it is a switch: the host that went away comes back on its address.
	p.Pass()
	again, serverAgain := dialPair(t, p, up)
	roundTrip(t, again, serverAgain, []byte("welcome back"))
}

func TestFaultProxy_BandwidthPacesTheStreamToItsRate(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)

	const rate = 64 * 1024
	p.Bandwidth(rate)

	payload := bytes.Repeat([]byte("x"), 32*1024)
	started := time.Now()
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	// Everything arrives, in order: a rate limit is not a loss.
	mustRead(t, server, payload, "the paced stream")

	// 32 KiB at 64 KiB/s takes half a second by construction, so a stream
	// that arrived materially sooner was not paced at all. The floor is well
	// under the paced time, which is what keeps a slow machine from tripping
	// it: only an unpaced transfer can come in under it.
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
		t.Fatalf("32 KiB at %d B/s arrived in %v, want at least 300ms", rate, elapsed)
	}
}

func TestFaultProxy_SlowDeliversEverythingLate(t *testing.T) {
	up := startUpstream(t)
	p := NewFaultProxy(t, up.addr)
	client, server := dialPair(t, p, up)

	const perChunk = 50 * time.Millisecond
	p.Slow(perChunk)

	started := time.Now()
	roundTrip(t, client, server, []byte("hello"))
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("a round trip under Slow(%v) took %v: each direction's chunk must be held on its way", perChunk, elapsed)
	}
}
