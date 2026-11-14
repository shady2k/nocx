//go:build nocx_local_ssh

package sshconn

// The look's read, and what frees it (nocx-oalo1.4.3).

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/waittest"
)

// channelConn models the connection a look actually reads on, x/crypto v0.54.0's
// channel conn — and models it honestly, because the first version of this test
// did not:
//
//   - Read parks until the TRANSPORT ends. A close only SENDS a close packet
//     (ssh/channel.go:594); a local read is released by the peer's EOF, by the
//     peer's close arriving, or by the transport ending (ssh/channel.go:427).
//   - Close parks too, until the test lets it go: on a hop that has gone quiet
//     the packet has nowhere to write, which is the case the look exists for.
//
// A fake whose Close freed its Read would prove the fake.
type channelConn struct {
	transport chan struct{} // closed when the transport ends
	release   chan struct{} // closed when the test lets Close return
	closeSent chan struct{}
	closeOnce sync.Once
	closes    atomic.Int32
}

func newChannelConn() *channelConn {
	return &channelConn{
		transport: make(chan struct{}),
		release:   make(chan struct{}),
		closeSent: make(chan struct{}),
	}
}

func (c *channelConn) Read([]byte) (int, error) {
	<-c.transport
	return 0, net.ErrClosed
}

func (c *channelConn) Write([]byte) (int, error) { return 0, net.ErrClosed }

func (c *channelConn) Close() error {
	c.closes.Add(1)
	c.closeOnce.Do(func() { close(c.closeSent) })
	<-c.release
	return nil
}

func (c *channelConn) closeCount() int { return int(c.closes.Load()) }

// endTransport is the connection ending, which is the only thing that frees an
// abandoned read and a parked close.
func (c *channelConn) endTransport() {
	close(c.release)
	close(c.transport)
}

// TestALookIsGivenUpWhenCancelledEvenThoughNoCloseCanFreeItsRead is the slot
// this look owes the guard: the guard has ONE, and a destination that accepts the
// channel and never speaks must not hold it for the life of the connection.
//
// It asserts the whole shape, not just "it returned": the caller returns, the
// close packet is SENT without the caller waiting for it (the fake's Close parks
// and the caller has already returned), and nothing here claims the read was
// freed — it ends with the transport, which the test ends at cleanup.
func TestALookIsGivenUpWhenCancelledEvenThoughNoCloseCanFreeItsRead(t *testing.T) {
	conn := newChannelConn()
	t.Cleanup(conn.endTransport)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		_, err := readVersionLineWithin(ctx, conn)
		errs <- err
	}()

	cancel()

	waittest.WaitFor(t, "the caller to be given up once the context ends", func() bool {
		select {
		case err := <-errs:
			if err == nil {
				t.Error("a read that was never answered returned a version line")
			}
			return true
		default:
			return false
		}
	})
	// The close was asked for, and the caller did not wait for it: the fake
	// parks in Close until the transport ends.
	waittest.WaitFor(t, "the close packet to be sent", func() bool { return conn.closeCount() == 1 })
}

// TestAVersionReadStillAnswersALineThatArrives is the paired success: a peer
// that speaks is read as before, and the close that follows does not wait.
func TestAVersionReadStillAnswersALineThatArrives(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	go func() {
		_, _ = server.Write([]byte("SSH-2.0-nocx_fixture\r\n"))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	line, err := readVersionLineWithin(ctx, client)
	if err != nil {
		t.Fatalf("readVersionLineWithin: %v", err)
	}
	if got := string(line); got != "SSH-2.0-nocx_fixture\r\n" {
		t.Fatalf("line = %q", got)
	}
}
