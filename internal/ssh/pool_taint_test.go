//go:build nocx_local_ssh

package ssh

// Taint / CloseTainted (spec §5.7, nocx-6q1uh.3): the pool's half of "a
// detached writer's connection admits no new channel, and a helper holds at
// most maxDetachedWriters of them at once." owner_ssh.go and spawn_ssh.go
// (internal/helper/session) are what actually decide a writer is stuck; this
// file is the pool-level contract those callers rely on, tested directly
// against a fake dial so it needs no real network and no real blocked write.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// TestATaintedConnectionTakesNoNewChannels is the plan's own acceptance
// criterion: after Taint, a new Acquire for the SAME key dials a new
// connection (the old one is never handed out again), and the tainted
// connection is untouched — closing only when its last reference releases,
// exactly like any other entry at ref zero.
func TestATaintedConnectionTakesNoNewChannels(t *testing.T) {
	p := NewConnPool(log.NewSlogAdapter(nil))
	var dials int
	var built []*fakeClient
	p.dial = func(poolKey) (sshClientConn, error) {
		dials++
		fc := &fakeClient{}
		built = append(built, fc)
		return fc, nil
	}
	key := poolKey{host: "h", port: 22, user: "u"}

	h1, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d after the first acquire, want 1", dials)
	}

	p.Taint(h1)

	// The tainted connection is not closed by Taint alone: it is still
	// referenced (h1 has not released), so the first sibling's own traffic
	// is unaffected.
	if built[0].getCloseCount() != 0 {
		t.Fatal("Taint closed the connection outright; it must wait for the last release")
	}

	h2, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d after a second acquire on a tainted key, want 2 (the tainted entry must never be reused)", dials)
	}
	if h2.conn == h1.conn {
		t.Fatal("the second acquire was handed the tainted connection")
	}

	// The tainted connection closes when ITS last reference releases — h1's
	// own Release, since nothing else was ever handed it.
	p.Release(h1)
	if built[0].getCloseCount() != 1 {
		t.Fatalf("the tainted connection's close count is %d after its last release, want 1", built[0].getCloseCount())
	}
	// The second (untainted) connection is unaffected.
	if built[1].getCloseCount() != 0 {
		t.Fatal("releasing the tainted connection's last reference closed the NEW connection instead")
	}
}

// TestTheNinthDetachClosesItsConnection is the plan's own acceptance
// criterion for the cap: eight detached writers are tolerated (Taint alone);
// the ninth, anywhere in the pool, closes its own connection AT ONCE
// (CloseTainted, ReasonDetachedWriterCap) rather than waiting for its last
// release — and a SIBLING handle sharing that ninth connection reads the
// reason back, which is what lets its own session report it on exit
// (sshsvc.ShellChannel.wait).
func TestTheNinthDetachClosesItsConnection(t *testing.T) {
	p := NewConnPool(log.NewSlogAdapter(nil))
	var built []*fakeClient
	p.dial = func(poolKey) (sshClientConn, error) {
		fc := &fakeClient{}
		built = append(built, fc)
		return fc, nil
	}

	// Eight independent connections, each detaching once. Different keys:
	// the cap is counted per HELPER PROCESS (one pool), not per connection.
	var handles []*poolHandle
	for i := 0; i < maxDetachedWriters; i++ {
		key := poolKey{host: "h", port: 22, user: "u", identity: string(rune('a' + i))}
		h, err := p.Acquire(context.Background(), key)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		handles = append(handles, h)
		p.Taint(h)
	}
	for i, fc := range built {
		if fc.getCloseCount() != 0 {
			t.Fatalf("connection %d closed after only %d detaches, want none closed before the cap", i, maxDetachedWriters)
		}
	}

	// The ninth: a NEW connection with a sibling handle already on it, so
	// the reason can be read back from something other than the handle that
	// tripped the cap.
	ninthKey := poolKey{host: "h", port: 22, user: "u", identity: "ninth"}
	ninth, err := p.Acquire(context.Background(), ninthKey)
	if err != nil {
		t.Fatalf("acquire ninth: %v", err)
	}
	sibling, err := p.Acquire(context.Background(), ninthKey)
	if err != nil {
		t.Fatalf("acquire ninth's sibling: %v", err)
	}
	if sibling.conn != ninth.conn {
		t.Fatal("the sibling acquire did not share the ninth connection")
	}

	p.Taint(ninth)

	ninthClient := built[len(built)-1]
	if ninthClient.getCloseCount() != 1 {
		t.Fatalf("the ninth connection's close count is %d, want 1: the cap must close it at once", ninthClient.getCloseCount())
	}
	if got := sibling.closeReason(); got != ReasonDetachedWriterCap {
		t.Fatalf("the sibling's close reason is %q, want %q", got, ReasonDetachedWriterCap)
	}
	if got := ninth.closeReason(); got != ReasonDetachedWriterCap {
		t.Fatalf("the tainting handle's own close reason is %q, want %q", got, ReasonDetachedWriterCap)
	}

	// The first eight are unaffected: none of them were pushed past the cap.
	for i := 0; i < maxDetachedWriters; i++ {
		if built[i].getCloseCount() != 0 {
			t.Fatalf("connection %d closed by the ninth's cap overflow; only the ninth's own connection may close", i)
		}
		_ = handles[i]
	}
}
