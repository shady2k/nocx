//go:build nocx_local_ssh

package sshconn

// The look's read, and what frees it (nocx-oalo1.4.3).

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/waittest"
)

// parkedConn is a connection a deadline cannot free: Read parks until Close,
// which is the shape a direct-tcpip channel has — x/crypto's channel conn
// answers SetDeadline without honouring it.
type parkedConn struct {
	closed   chan struct{}
	closeOne sync.Once
}

func newParkedConn() *parkedConn { return &parkedConn{closed: make(chan struct{})} }

func (c *parkedConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.ErrClosedPipe
}

func (c *parkedConn) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func (c *parkedConn) Close() error {
	c.closeOne.Do(func() { close(c.closed) })
	return nil
}

// TestAVersionReadOnAConnectionWithNoDeadlineReturnsWhenCancelled is the look
// through a hop: the destination accepts the channel and says nothing, and the
// guard's deadline has to free the read or the ONE look slot stays occupied and
// no later look ever runs.
func TestAVersionReadOnAConnectionWithNoDeadlineReturnsWhenCancelled(t *testing.T) {
	conn := newParkedConn()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := readVersionLineWithin(ctx, conn)
		errs <- err
	}()

	cancel()

	waittest.WaitFor(t, "the version read to return once the context is cancelled", func() bool {
		select {
		case err := <-errs:
			if err == nil {
				t.Error("a read that was closed under it answered a version line")
			}
			return true
		default:
			return false
		}
	})
}

// TestAVersionReadStillAnswersALineThatArrives is the paired success: the close
// is a guard, not a deadline that fires — a peer that speaks is read as it
// always was.
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
