package client

// The carrier for an endpoint on THIS machine (level-1 design D11): the
// coordinator connects to the helper's private Unix socket directly, and
// speaks exactly what it speaks over the ssh exec lane — the same hello, the
// same sentinel, the same frames, through the same Dial. Remotely the bytes
// take one more hop (`nocx-helper bridge` on the far side, connected to the
// same kind of socket); nothing above this type can tell the difference, and
// nothing above it branches on which one it got.

import (
	"errors"
	"io"
	"net"
	"sync"
)

// ErrNoCommandOnASocket is a socket carrier handed a command to launch. It is
// a refusal rather than a shrug because the two carriers differ in exactly
// this: the exec lane LAUNCHES the helper (and the command names which
// binary), while the socket is already being served and the generation was
// decided by which socket was dialled. A caller passing a command here has
// confused the two, and a silent no-op would run the wrong generation's
// sessions and look like it worked.
var ErrNoCommandOnASocket = errors.New("helper: this carrier is a socket; there is no command to launch")

// SocketConn is a HelperConn over one connection to a helper endpoint. It is
// the local half of D11 and it is not a "local mode": it is one of the two
// carriers that reach the one endpoint, and it implements the same interface
// the ssh exec lane does so that Dial, the handshake and every request above
// them have a single implementation.
type SocketConn struct {
	conn net.Conn

	mu      sync.Mutex
	closing bool
	lostErr error

	done     chan struct{}
	over     chan struct{}
	doneOnce sync.Once
	overOnce sync.Once
}

// NewSocketConn adapts a connection to a helper endpoint (internal/helper/
// endpoint.Dial gives you one) to the carrier Dial takes. The connection is
// owned by the returned carrier from here until its Close.
func NewSocketConn(conn net.Conn) *SocketConn {
	return &SocketConn{conn: conn, done: make(chan struct{}), over: make(chan struct{})}
}

// Stdin is the frame output. A socket is one stream in both directions, so
// this is the same connection Stdout reads — which is exactly what the exec
// lane's two pipes add up to.
func (c *SocketConn) Stdin() io.WriteCloser { return writeHalf{c} }

// Stdout is the wire.
func (c *SocketConn) Stdout() io.Reader { return readHalf{c} }

// Stderr is empty and stays empty. The exec lane carries the helper's
// diagnostics on a second stream because ssh gives it one; a socket has no
// second stream, and the helper serving it logs to its own stderr where it
// was started. Returning an empty reader rather than nil keeps the caller
// from having to know which carrier it holds.
func (c *SocketConn) Stderr() io.Reader { return eofReader{} }

// Start refuses a command: see ErrNoCommandOnASocket. An empty command is the
// no-op it should be — the endpoint is already serving.
func (c *SocketConn) Start(command string) error {
	if command != "" {
		return ErrNoCommandOnASocket
	}
	return nil
}

// Wait answers the question the exec lane answers with an exit status: how
// did the peer end. A socket has no exit status, so this reports the stream
// ending and nothing more — which is the honest answer, and is why the
// version-mismatch code (42) is a fact only the exec lane can carry. Over a
// socket a mismatch cannot arise unnoticed anyway: the socket names its
// generation, and the handshake checks the content hash against what was
// installed (D21).
func (c *SocketConn) Wait() (int, error) {
	<-c.over
	return 0, nil
}

// Done closes when the connection shut down under us, and NOT when this side
// closed it: a caller's own Close is not transport loss, and the client's
// loss watcher must not fire on it.
func (c *SocketConn) Done() <-chan struct{} { return c.done }

// LostErr reports why the connection shut down, once Done has closed.
func (c *SocketConn) LostErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lostErr
}

// Close ends the connection. The endpoint stays up and every session on it
// survives: this is one attachment going away, which is precisely the
// identity D2 makes disposable.
func (c *SocketConn) Close() error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	err := c.conn.Close()
	c.overOnce.Do(func() { close(c.over) })
	return err
}

// ended records the end of the stream: it releases Wait either way, and
// closes Done only when the end was not ours.
func (c *SocketConn) ended(err error) {
	c.mu.Lock()
	ours := c.closing
	if !ours && c.lostErr == nil {
		c.lostErr = err
	}
	c.mu.Unlock()
	c.overOnce.Do(func() { close(c.over) })
	if !ours {
		c.doneOnce.Do(func() { close(c.done) })
	}
}

type readHalf struct{ c *SocketConn }

func (r readHalf) Read(p []byte) (int, error) {
	n, err := r.c.conn.Read(p)
	if err != nil {
		r.c.ended(err)
	}
	return n, err
}

type writeHalf struct{ c *SocketConn }

func (w writeHalf) Write(p []byte) (int, error) { return w.c.conn.Write(p) }

// Close half-closes the write direction where the connection supports it, so
// the helper sees the EOF that ends its read loop while this side goes on
// draining what is still in flight. That is the same shape stdin closing has
// on the exec lane.
func (w writeHalf) Close() error {
	if cw, ok := w.c.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return w.c.Close()
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }
