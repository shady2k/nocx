//go:build nocx_local_ssh

package sshconn

// The look's work, what frees it, and how long it keeps the look busy
// (nocx-oalo1.4.3).

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	endOnce   sync.Once
	closes    atomic.Int32

	mu   sync.Mutex
	wire int
}

func newChannelConn() *channelConn {
	return &channelConn{
		transport: make(chan struct{}),
		release:   make(chan struct{}),
		closeSent: make(chan struct{}),
	}
}

func (c *channelConn) Read([]byte) (int, error) {
	c.mu.Lock()
	c.wire++
	c.mu.Unlock()
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
// abandoned read and a parked close. Idempotent: the tests end it to assert, and
// again at cleanup.
func (c *channelConn) endTransport() {
	c.endOnce.Do(func() {
		close(c.release)
		close(c.transport)
	})
}

// TestALookStaysBusyUntilItsAbandonedReadEnds is the bound the guard reads: the
// caller is given up at once, and the look keeps reporting itself busy until the
// read it left behind finally ends — which through a silent hop is when the hop's
// connection does.
func TestALookStaysBusyUntilItsAbandonedReadEnds(t *testing.T) {
	conn := newChannelConn()
	t.Cleanup(conn.endTransport)

	l := &link{look: func(ctx context.Context, done func()) error {
		_, err := readVersionLineWithin(ctx, conn, done)
		return err
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- l.Look(ctx) }()

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
	waittest.WaitFor(t, "the close packet to be sent", func() bool { return conn.closeCount() == 1 })
	if !l.LookBusy() {
		t.Fatal("the look reported itself free while its read was still parked")
	}

	conn.endTransport()
	waittest.WaitFor(t, "the look to report itself free once its read ended", func() bool { return !l.LookBusy() })
}

// TestACancelledDialKeepsTheLookBusyUntilItReturns is the other half of the same
// work: a channel open parks on a silent hop where nothing on this side can see
// it, so the look keeps its bound until the dial itself is over — and the channel
// it then hands over is closed without waiting for the packet.
func TestACancelledDialKeepsTheLookBusyUntilItReturns(t *testing.T) {
	handed := &countedConn{}
	hop := &parkedDialer{opened: make(chan struct{}), release: func() net.Conn { return handed }}

	var doneCalls atomic.Int32
	done := func() { doneCalls.Add(1) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() {
		_, err := dialThrough(ctx, hop, "db.example:22", done)
		errs <- err
	}()

	cancel()
	waittest.WaitFor(t, "the caller to be given up", func() bool {
		select {
		case err := <-errs:
			return err != nil
		default:
			return false
		}
	})
	if got := doneCalls.Load(); got != 0 {
		t.Fatalf("the look reported its dial over %d time(s) while the channel open was still parked", got)
	}

	hop.releaseDial()
	waittest.WaitFor(t, "the abandoned dial to be reported over", func() bool { return doneCalls.Load() == 1 })
	waittest.WaitFor(t, "the channel it handed over to be closed", func() bool { return handed.closes.Load() == 1 })
}

// parkedDialer is a hop whose channel open parks until the test releases it —
// the shape x/crypto's Client.Dial has on a hop that has gone quiet.
type parkedDialer struct {
	once    sync.Once
	opened  chan struct{}
	release func() net.Conn
}

func (p *parkedDialer) Dial(string, string) (net.Conn, error) {
	<-p.opened
	return p.release(), nil
}

func (p *parkedDialer) releaseDial() { p.once.Do(func() { close(p.opened) }) }

// countedConn is a connection that records its own close. Nothing reads it: a
// cancelled look only has to drop what the dial eventually handed over.
type countedConn struct{ closes atomic.Int32 }

func (c *countedConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (c *countedConn) Write([]byte) (int, error)        { return 0, net.ErrClosed }
func (c *countedConn) Close() error                     { c.closes.Add(1); return nil }
func (c *countedConn) LocalAddr() net.Addr              { return nil }
func (c *countedConn) RemoteAddr() net.Addr             { return nil }
func (c *countedConn) SetDeadline(time.Time) error      { return nil }
func (c *countedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *countedConn) SetWriteDeadline(time.Time) error { return nil }

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
	line, err := readVersionLineWithin(ctx, client, func() {})
	if err != nil {
		t.Fatalf("readVersionLineWithin: %v", err)
	}
	if got := string(line); got != "SSH-2.0-nocx_fixture\r\n" {
		t.Fatalf("line = %q", got)
	}
}
