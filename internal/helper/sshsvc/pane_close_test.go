//go:build nocx_local_ssh

package sshsvc

// THE TOOL SOCKET ENDS THE FORWARDS IT IS CARRYING (nocx-50w7p.16).
//
// This is a test of the MECHANISM, and it is here rather than only at the far
// end of the ssh fixture because the fixture cannot see the difference: its own
// `cancel-streamlocal-forward` closes the connections already accepted on the
// listener, so an end-to-end test passes whether or not the socket ends them
// itself. Measured, not assumed — removing the cancellation below leaves the
// end-to-end test green and this one red.
//
// What the fixture does not model is the case that matters: a real sshd's
// cancellation removes the LISTENER, and the channels already open on it stay
// open until the connection or the channel ends. So a socket whose session is
// over must end its own forwards, or a far agent keeps a pipe into a
// coordinator that has forgotten the pane.
//
// The subject is `toolSocket` rather than the pane that owns one (nocx-e2bws):
// the pane and the `ssh.tool-socket` op are two callers of one implementation,
// and this half of it — the listener, the forwards it produced and their end —
// is the same value in both.

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testToolSocket is one tool socket with no listener behind it: these tests
// drive the FORWARD half, which is where the lifetime decisions are.
func testToolSocket(target string) *toolSocket {
	return newToolSocket(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil,
		"/far/tool.sock", target, "0f9b4d7159d38afee9648a843654516f")
}

// TestClosingAToolSocketEndsTheForwardsItIsCarrying — the paired assertion: the
// connection is live before the socket closes and ended by it, with nobody on
// the far side closing anything.
func TestClosingAToolSocketEndsTheForwardsItIsCarrying(t *testing.T) {
	sock := testToolSocket("/local/tool.sock")
	far, local := net.Pipe()
	defer func() { _ = local.Close() }()

	if !sock.track(far) {
		t.Fatal("an open tool socket refused to carry a connection")
	}

	// PAIRED SUCCESS FIRST: the connection works before the socket ends.
	go func() { _, _ = local.Write([]byte("still here\n")) }()
	buf := make([]byte, len("still here\n"))
	if err := far.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := io.ReadFull(far, buf); err != nil {
		t.Fatalf("the connection was not carried before the socket closed: %v", err)
	}

	// The deadline is set BEFORE the close and not after it: a closed
	// connection refuses SetReadDeadline, and the point of a deadline here is
	// to bound what follows — a read that was NOT ended by the close would
	// otherwise park the test until the package's own alarm.
	if err := far.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := sock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	switch _, err := far.Read(buf); {
	case err == nil:
		t.Fatal("a forward outlived the socket that was carrying it")
	case errors.Is(err, os.ErrDeadlineExceeded):
		t.Fatal("the connection was still open after its socket closed: the read waited out its deadline instead of ending")
	}
}

// TestClosingAToolSocketEndsAForwardParkedOnTheEndpoint — the LIFETIME half, and
// the one a pump can defeat.
//
// A forward is two pumps, and ending the far side ends only the one reading from
// it. The other one writes INTO the endpoint, and if the endpoint is not reading
// — a coordinator that has stopped draining, a socket whose reader is elsewhere
// — that pump parks in Write and stays there. Closing only the far connection
// leaves it parked, and with it this connection and this socket's work.
//
// The endpoint here accepts and reads nothing, so the pump parks by
// construction rather than by timing: the far agent cannot finish writing
// either, because its own writer is behind the same pump. Both are checked for
// being still in flight, and that is an absence — the one shape a duration may
// bound, because nothing has to ARRIVE for it to be true.
func TestClosingAToolSocketEndsAForwardParkedOnTheEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	sock := testToolSocket(path)

	far, agent := net.Pipe()
	defer func() { _ = agent.Close() }()
	if !sock.track(far) {
		t.Fatal("an open tool socket refused to carry a connection")
	}

	returned := make(chan struct{})
	go func() { sock.forward(far); close(returned) }()

	var endpoint net.Conn
	select {
	case endpoint = <-accepted:
		defer func() { _ = endpoint.Close() }()
	case <-time.After(10 * time.Second):
		t.Fatal("the forward never reached the endpoint")
	}

	// The far agent writes more than any buffer holds, and nothing reads it.
	written := make(chan int, 1)
	go func() {
		n, _ := agent.Write(make([]byte, 8<<20))
		written <- n
	}()
	select {
	case n := <-written:
		t.Fatalf("the far agent finished writing %d bytes: the endpoint-side pump was not parked", n)
	case <-time.After(300 * time.Millisecond):
		// Still in flight: the pump is parked in its write, which is the state
		// this test needs and the reason the duration is here at all.
	}

	if err := sock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the forward did not end: a pump parked on the endpoint outlived its socket")
	}
}

// TestAToolSocketThatHasClosedRefusesFurtherForwards — the OTHER half of the
// same decision, and the one a listener alone cannot make: between the Accept
// that returned and the registration that follows it, the socket can close, and
// a connection registered after that is one nobody will ever end. It is refused
// instead, so the accept loop closes it.
func TestAToolSocketThatHasClosedRefusesFurtherForwards(t *testing.T) {
	sock := testToolSocket("/local/tool.sock")
	if err := sock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	far, local := net.Pipe()
	defer func() { _ = far.Close() }()
	defer func() { _ = local.Close() }()

	if sock.track(far) {
		t.Fatal("a closed tool socket accepted a connection it will never end")
	}
}
