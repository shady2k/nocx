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
	// supervisor is told so by Capabilities... rather, by the error Look
	// answers.
	lookAddr string

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
func newLink(pool *ssh.PooledConn, lookAddr string) *link {
	l := &link{pool: pool, lookAddr: lookAddr, done: make(chan struct{})}
	go func() {
		err := pool.Client().Wait()
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.done)
	}()
	return l
}

// lookAddress is where a look-only dial for this plan goes: the FIRST endpoint
// on the route, which for a direct connection is the destination itself.
//
// §5.3 asks for the look "on the same route" (review finding 11). A routed
// destination cannot be spoken to without a key exchange on the bastion, and a
// look is TCP and the version line and nothing else, so the furthest endpoint
// this side can ask that way is the first hop: its answer says the path and the
// bastion are up, which is evidence about the route the connection uses. A host
// BEHIND the bastion is looked at through the bastion's own session once
// channels move behind the carrier (design §5.1.2) — claiming otherwise here
// would be evidence about the wrong machine.
func lookAddress(plan hostconn.Plan, spec ssh.PooledSpec) string {
	if len(spec.Route) > 0 {
		first := spec.Route[0]
		return net.JoinHostPort(first.Host, strconv.Itoa(first.Port))
	}
	return net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port))
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

// Look makes a look-only dial: TCP and the SSH version exchange on a NEW
// connection, no key exchange and no sign-in. It answers whether the host
// accepts new connections, which is evidence about the host and never a verdict
// on the flow (design §5.3).
func (l *link) Look(ctx context.Context) error {
	if l == nil || l.lookAddr == "" {
		return errors.New("sshconn: no look-only dial is available for this connection")
	}
	d := net.Dialer{Timeout: handshakeBound(ctx), KeepAlive: -1}
	conn, err := d.DialContext(ctx, "tcp", l.lookAddr)
	if err != nil {
		return fmt.Errorf("look %s: %w", l.lookAddr, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// The server speaks FIRST in SSH: the version line is the first thing it
	// sends, and nothing above the socket can see it without a handshake this
	// dial deliberately does not start.
	line, err := readVersionLine(conn)
	if err != nil {
		return fmt.Errorf("look %s: %w", l.lookAddr, err)
	}
	if len(line) < len("SSH-") || string(line[:len("SSH-")]) != "SSH-" {
		return fmt.Errorf("look %s: the peer sent %q, which is not an SSH version line", l.lookAddr, line)
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
