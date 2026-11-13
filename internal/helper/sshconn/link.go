//go:build nocx_local_ssh

package sshconn

// The connection, as the supervisor holds it.

import (
	"sync"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

// link is one pooled connection under a supervisor. It owns the ONE pooled
// reference the supervisor holds; holders get a lease on the supervisor and do
// not each own a reference of their own (design §5.1.1's second trap: the count
// that decides when a connection closes belongs to the connection).
type link struct {
	pool *ssh.PooledConn

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
func newLink(pool *ssh.PooledConn) *link {
	l := &link{pool: pool, done: make(chan struct{})}
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
