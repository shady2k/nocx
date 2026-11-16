//go:build nocx_local_ssh

package ssh

// Review finding 5: a pooled entry whose transport has ended is never handed
// out — in the window before any watcher goroutine runs, and when a bastion's
// extra references keep it alive in the table.

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// deadEntry answers a pool entry holding ONE connection whose own end marker
// says the transport is over, with the given number of references.
func deadEntry(p *ConnPool, key poolKey, refs int) *pooledSSHConn {
	ended := &atomic.Bool{}
	conn := &pooledSSHConn{client: &fakeClient{}, ended: ended}
	p.pool[key] = &poolEntry{conn: conn, ref: &refCount{n: refs}}
	ended.Store(true)
	return conn
}

func TestAPoolEntryWhoseTransportEndedIsNotHandedOut(t *testing.T) {
	p := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "example.com", port: 22, user: "alice"}
	dead := deadEntry(p, key, 1)

	var dialed int32
	p.dial = func(poolKey) (sshClientConn, error) {
		atomic.AddInt32(&dialed, 1)
		return &fakeClient{}, nil
	}

	h, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer p.Release(h)
	if h.conn == sshClientConn(dead) {
		t.Fatal("the pool handed out a connection whose transport had ended")
	}
	if got := atomic.LoadInt32(&dialed); got != 1 {
		t.Fatalf("%d dials, want 1: the corpse was not evicted", got)
	}
}

func TestABastionsExtraReferencesDoNotLetTheCorpseBeReacquired(t *testing.T) {
	p := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "bastion.example", port: 22, user: "jump"}
	dead := deadEntry(p, key, 2) // e.g. a hop the target still holds

	var dialed int32
	p.dial = func(poolKey) (sshClientConn, error) {
		atomic.AddInt32(&dialed, 1)
		return &fakeClient{}, nil
	}

	fresh, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("Acquire over a dead bastion: %v", err)
	}
	if fresh.conn == sshClientConn(dead) {
		t.Fatal("a fresh dial reacquired the bastion's corpse because its references were above one")
	}
	// The new entry is what the table holds; releasing the fresh reference
	// closes it, and the old generation's release finds nothing of its own.
	p.Release(fresh)
	if got := atomic.LoadInt32(&dialed); got != 1 {
		t.Fatalf("%d dials, want 1", got)
	}
	if p.Count() != 0 {
		t.Fatalf("%d entries after the fresh reference was released, want 0", p.Count())
	}
}

// blockingCloseConn is a net.Conn whose Close waits on a gate, so a test can
// ask what the dead mark says while the physical close is still in progress.
type blockingCloseConn struct {
	net.Conn
	gate  chan struct{}
	begun atomic.Bool
}

func (c *blockingCloseConn) Close() error {
	c.begun.Store(true)
	<-c.gate
	return nil
}

// TestTheDeadMarkPrecedesThePhysicalClose — the verification's finding 5: the
// mark must be set BEFORE the underlying close, or admission can see a socket
// that is closed while its connection still reads alive.
func TestTheDeadMarkPrecedesThePhysicalClose(t *testing.T) {
	gate := make(chan struct{})
	inner := &blockingCloseConn{gate: gate}
	ended := &atomic.Bool{}
	marked := &endMarked{Conn: inner, ended: ended}

	done := make(chan struct{})
	go func() {
		_ = marked.Close()
		close(done)
	}()
	waittest.WaitFor(t, "the physical close to be entered", inner.begun.Load)
	if !ended.Load() {
		t.Fatal("the dead mark was not set before the underlying close")
	}
	close(gate)
	<-done
}
