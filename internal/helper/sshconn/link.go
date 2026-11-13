//go:build nocx_local_ssh

package sshconn

// The connection, as the supervisor holds it: one pooled reference, the
// transport's end, and the three things the closing policy asks of a live
// connection — a probe, a nudge, and a look at the host.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// keepaliveRequest is the request this carrier probes and nudges with. It is
// OpenSSH's own keepalive name: a server answers it with a failure message,
// which is still an answer (liveness is "it replied"), and a nudge sends it
// with no reply wanted.
const keepaliveRequest = "keepalive@openssh.com"

// link is one pooled connection under a supervisor. It owns the ONE pooled
// reference the supervisor holds; holders get a lease on the supervisor and do
// not each own a reference of their own (design §5.1.1's second trap: the count
// that decides when a connection closes belongs to the connection).
type link struct {
	pool *ssh.PooledConn
	// lookAddr is where a look-only dial goes: the same destination this
	// connection was dialled to. empty means the look is not available for
	// this connection (a routed one, whose version exchange would have to
	// happen through a hop this carrier does not dial from here) and the
	// look is this connection's look-only dial, built by the carrier for the
	// plan it dialled (a direct look, or one through the first hop of a
	// route).
	look func(ctx context.Context) error

	done chan struct{}
	mu   sync.Mutex
	err  error

	closeOne sync.Once
	closeErr error
}

// newLink watches the client's own end. It is the only thing in the process
// that learns a transport died: x/crypto stores the read error and returns it
// from every later write, so a connection whose peer is gone looks alive to
// everyone who is not writing to it.
func newLink(pool *ssh.PooledConn, look func(ctx context.Context) error) *link {
	l := &link{pool: pool, look: look, done: make(chan struct{})}
	go func() {
		err := pool.Client().Wait()
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.done)
	}()
	return l
}

// Done is closed when the transport has ended.
func (l *link) Done() <-chan struct{} { return l.done }

// Dead reports whether the transport has ended, answered by the pool's own
// marker: the library sets it when it closes the socket, inside its teardown
// and before Wait returns, so admission never has to wait for this file's
// watcher goroutine (review finding 5).
func (l *link) Dead() bool {
	if l == nil || l.pool == nil {
		return false
	}
	return l.pool.Dead()
}

// Tainted reports whether the pool has retired this connection from new
// admission (a detached writer's cap, a credential rotation). The supervisor
// honours it: one owner decides "may this connection be reused", and it is the
// pool.
func (l *link) Tainted() bool {
	if l == nil || l.pool == nil {
		return false
	}
	return l.pool.Tainted()
}

// Err is why it ended, and is meaningful only once Done is closed.
func (l *link) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close ends the connection and gives its pooled reference back. On a
// transport that has already ended this is what takes the entry out of the
// pool's table: the supervisor's reference is the connection's only one, so
// releasing it is removing it, and every later acquire dials afresh.
func (l *link) Close() error {
	if l == nil {
		return nil
	}
	l.closeOne.Do(func() { l.closeErr = l.pool.Close() })
	return l.closeErr
}

// Probe asks the far side to answer, and reports how long it took.
//
// It takes no context and no deadline ON PURPOSE. x/crypto's SendRequest holds
// the mux's send lock for the whole wait and offers no way to interrupt it, so
// a probe that stops answering can only stay parked; the supervisor's soft
// deadline reports `stalled` while the call remains the evidence that will
// notice the answer (design §5.3). A context that timed out here would abandon
// a goroutine holding the lock and leave the next probe queued behind a call
// nobody can see.
func (l *link) Probe(context.Context) (time.Duration, error) {
	started := time.Now()
	_, _, err := l.pool.Client().SendRequest(keepaliveRequest, true, nil)
	if err != nil {
		return 0, err
	}
	return time.Since(started), nil
}

// Nudge sends the keepalive with NO reply wanted: it does not take the mux's
// send lock, so it passes a parked probe, and a host that was rebooted answers
// it with a reset — a transport error, and an unaided reconnect. A host that
// still holds the flow acknowledges it and nothing changes, so a nudge can end
// no live shell (design §5.3).
func (l *link) Nudge() error {
	_, _, err := l.pool.Client().SendRequest(keepaliveRequest, false, nil)
	if err != nil {
		return fmt.Errorf("nudge: %w", err)
	}
	return nil
}

// Look makes a look-only dial, built for this connection by the carrier: a
// direct connection's is TCP and the version exchange on a NEW connection (no
// key exchange, no sign-in), and a routed connection's goes through the first
// hop to the destination (design §5.3; review verification, finding 11).
func (l *link) Look(ctx context.Context) error {
	if l == nil || l.look == nil {
		return errors.New("sshconn: no look-only dial is available for this connection")
	}
	return l.look(ctx)
}

// lookFor answers this connection's look-only dial.
//
// §5.3 asks for the look "on the same route", and the review's verification is
// exact about what that means (finding 11): a look at the first bastion proves
// nothing about the destination or the rest of the route. So a ROUTED look goes
// THROUGH the supervised bastion — a direct-tcpip channel to the destination,
// whose version line the destination sends before any key exchange — and a
// silent bastion makes the look fail, because the channel never opens.
func lookFor(spec ssh.PooledSpec, hops []*ssh.PooledConn) func(ctx context.Context) error {
	dest := net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port))
	if len(spec.Route) == 0 {
		return func(ctx context.Context) error { return lookDirect(ctx, dest) }
	}
	return func(ctx context.Context) error {
		if len(hops) == 0 {
			return errors.New("sshconn: this routed connection holds no first hop to look through")
		}
		return lookThrough(ctx, hops[0].Client(), dest)
	}
}

// lookThrough opens one direct-tcpip channel through hop and reads the
// DESTINATION's version line on it. The destination sends it before the key
// exchange, so nothing here authenticates to the destination or signs in.
func lookThrough(ctx context.Context, hop *gossh.Client, dest string) error {
	if hop == nil {
		return errors.New("sshconn: no hop connection to look through")
	}
	conn, err := hop.DialContext(ctx, "tcp", dest)
	if err != nil {
		return fmt.Errorf("look %s through the first hop: %w", dest, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	line, err := readVersionLine(conn)
	if err != nil {
		return fmt.Errorf("look %s through the first hop: %w", dest, err)
	}
	if len(line) < len("SSH-") || string(line[:len("SSH-")]) != "SSH-" {
		return fmt.Errorf("look %s through the first hop: the peer sent %q, which is not an SSH version line", dest, line)
	}
	return nil
}

// lookDirect is a direct connection's look: TCP and the server's version line
// on a new connection.
func lookDirect(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: handshakeBound(ctx), KeepAlive: -1}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("look %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	line, err := readVersionLine(conn)
	if err != nil {
		return fmt.Errorf("look %s: %w", addr, err)
	}
	if len(line) < len("SSH-") || string(line[:len("SSH-")]) != "SSH-" {
		return fmt.Errorf("look %s: the peer sent %q, which is not an SSH version line", addr, line)
	}
	return nil
}

// readVersionLine reads one CRLF-terminated version line, bounded the way the
// protocol bounds it: a peer that sends a novel instead of a version string has
// not answered the look.
//
// A line that did not arrive whole is not an answer (review comment): a peer
// that sends "SSH-" and then hangs or errors has said nothing, and counting its
// partial bytes as an answered version exchange would make a look evidence of a
// host that is not there.
func readVersionLine(conn net.Conn) ([]byte, error) {
	r := bufio.NewReader(io.LimitReader(conn, 512))
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("the peer's version line ended without CRLF (%d bytes)", len(line))
	}
	return line, nil
}

// handshakeBound answers how long a look may take when the caller's context
// carries no deadline of its own.
func handshakeBound(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 {
			return d
		}
	}
	return 15 * time.Second
}

// Pooled answers the pooled connection behind a link, for the channel kinds
// that have not moved behind the carrier yet (design §5.1.2 defers Files, Git,
// ports and tunnels to their own work).
//
// It is a type assertion with a bool rather than an interface method because it
// names x/crypto: the seam must not grow a method whose only caller can be the
// library it is meant to be replaceable for.
func Pooled(l hostconn.Link) (*ssh.PooledConn, bool) {
	if l == nil {
		return nil, false
	}
	x, ok := l.(*link)
	if !ok || x.pool == nil {
		return nil, false
	}
	return x.pool, true
}
