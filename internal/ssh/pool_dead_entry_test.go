package ssh

// A pooled connection that is already closed must never be handed to a new
// caller (AD-4).
//
// The window is real and it is not the steady state: when a connection dies,
// the sessions on it unwind and the last Release removes the entry, so the
// pool heals ITSELF a moment later. What it does not do is refuse in the
// meantime. Between "the transport is closed" and "the last session has
// finished noticing", a tab opened onto the same host is handed the corpse and
// fails to start for a reason that names nothing the user did.
//
// The window widens with the number of sessions sharing the connection, which
// is exactly the case a person meets after a suspend: eight tabs on one host,
// all unwinding at once, and the ninth is opened by the person wondering why
// the others went quiet.
//
// The check has to be a MARK rather than a probe. Asking the connection
// whether it is alive would mean a round trip on the pool's mutex — the same
// blocking-call-in-the-wrong-place defect the keepalive prober was just cured
// of. A closed connection knows it is closed; the pool reads a flag.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

type fakeDialCount struct{ n int }

func TestPool_DoesNotHandOutAClosedConnection(t *testing.T) {
	p := NewConnPool(log.NewSlogAdapter(nil))
	var dials fakeDialCount
	p.dial = func(_ poolKey) (sshClientConn, error) {
		dials.n++
		return &pooledSSHConn{client: &fakeClient{}}, nil
	}
	key := poolKey{host: "h", port: 22, user: "u"}

	// A first caller holds the connection — this is what keeps the entry
	// alive after the transport dies, and it is the whole window.
	holder, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// The transport dies underneath it: the keepalive prober gave up, or the
	// far end went away. The pool has not been told, because nobody has
	// released anything yet.
	if cerr := holder.conn.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// A new tab opens onto the same host. It must NOT be given the corpse.
	second, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if second.conn == holder.conn {
		t.Fatal("the pool handed out the connection that had already been closed")
	}
	if dials.n != 2 {
		t.Errorf("dials = %d, want 2 — the pool reused a dead entry instead of dialing", dials.n)
	}
}
