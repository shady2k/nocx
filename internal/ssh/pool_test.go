package ssh

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// fakeClient is a minimal ssh.Client stand-in for pool testing.
type fakeClient struct {
	closed     bool
	closeCount int
	mu         sync.Mutex
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.closeCount++
	return nil
}

func (f *fakeClient) getCloseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCount
}

func TestPoolAcquireCreatesAndReuses(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "example.com", user: "alice"}

	var dialCount int
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialCount++
		return &fakeClient{}, nil
	}

	c1, err := pool.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("after first Acquire, dialCount = %d, want 1", dialCount)
	}

	_, err = pool.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	if dialCount != 1 {
		t.Errorf("after second Acquire (same key), dialCount = %d, want 1 (reused)", dialCount)
	}

	_ = c1
}

func TestPoolReleaseClosesOnLastRef(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "example.com", user: "alice"}

	var fc *fakeClient
	pool.dial = func(key poolKey) (sshClientConn, error) {
		fc = &fakeClient{}
		return fc, nil
	}

	c1, _ := pool.Acquire(context.Background(), key)
	c2, _ := pool.Acquire(context.Background(), key)
	pool.Release(c1)
	// Should still be open (2 refs → 1).
	if fc.getCloseCount() != 0 {
		t.Error("connection closed before last ref released")
	}

	pool.Release(c2)
	// Should be closed now (last ref).
	if fc.getCloseCount() != 1 {
		t.Errorf("connection not closed on last ref, closeCount = %d", fc.getCloseCount())
	}
}

func TestPoolDifferentKeysDialSeparately(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key1 := poolKey{host: "h1", user: "u1"}
	key2 := poolKey{host: "h2", user: "u2"}

	var dialCount int
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialCount++
		return &fakeClient{}, nil
	}

	_, _ = pool.Acquire(context.Background(), key1)
	_, _ = pool.Acquire(context.Background(), key2)

	if dialCount != 2 {
		t.Errorf("dialCount = %d, want 2 (different keys = different conns)", dialCount)
	}
}

func TestPoolAcquireAfterReleaseReusesUntilClosed(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u"}

	var dialCount int
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialCount++
		return &fakeClient{}, nil
	}

	c1, _ := pool.Acquire(context.Background(), key)
	pool.Release(c1)
	// After release (last ref), the conn is closed. New acquire should dial.
	c2, _ := pool.Acquire(context.Background(), key)
	if dialCount != 2 {
		t.Errorf("dialCount = %d, want 2 (should re-dial after close)", dialCount)
	}
	_ = c2
}

func TestPoolConcurrentAcquireSameKey(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u"}

	var dialCount int
	var dialMu sync.Mutex
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialMu.Lock()
		dialCount++
		dialMu.Unlock()
		time.Sleep(10 * time.Millisecond) // simulate slow dial
		return &fakeClient{}, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.Acquire(context.Background(), key)
		}()
	}
	wg.Wait()

	if dialCount != 1 {
		t.Errorf("dialCount = %d, want 1 (concurrent acquire should dedup)", dialCount)
	}
}

func TestPoolKeyByHostAndIdentity(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))

	var dialed []poolKey
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialed = append(dialed, key)
		return &fakeClient{}, nil
	}

	// Same host+user, different port → different key.
	_, _ = pool.Acquire(context.Background(), poolKey{host: "h", user: "u", port: 22})
	_, _ = pool.Acquire(context.Background(), poolKey{host: "h", user: "u", port: 2222})

	if len(dialed) != 2 {
		t.Errorf("expected 2 dials (different port), got %d", len(dialed))
	}
}

func TestPoolReleaseUnknownHandleNoOp(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	// Releasing a handle not in the pool should not panic.
	pool.Release(&poolHandle{key: poolKey{host: "unknown"}, ref: &refCount{}, pool: pool})
}

// TestPoolDoubleReleaseCannotCloseLiveChannel proves the per-handle once
// guard. Two handles share one connection (refcount 2). Double-releasing
// handle A must NOT close the connection while handle B is still live: A's
// second Release is a no-op, so the refcount stays at 1 and the connection
// remains open for B. Without the once guard, A's second release would drop
// the refcount to 0 and close the connection underneath B's live channel.
//
// This test FAILS on an implementation without the per-handle once guard
// (where Release decrements unconditionally): the double release closes the
// connection, so fc.getCloseCount() == 1 before B releases, and the final
// close count is 2 instead of 1.
func TestPoolDoubleReleaseCannotCloseLiveChannel(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u"}

	var fc *fakeClient
	pool.dial = func(key poolKey) (sshClientConn, error) {
		fc = &fakeClient{}
		return fc, nil
	}

	a, _ := pool.Acquire(context.Background(), key)
	b, _ := pool.Acquire(context.Background(), key) // refcount now 2

	// Double-release A. With the once guard, only the first takes effect.
	pool.Release(a)
	pool.Release(a)

	// B is still live — the connection must NOT be closed yet.
	if got := fc.getCloseCount(); got != 0 {
		t.Fatalf("double-release of A closed the connection beneath live B: closeCount=%d, want 0", got)
	}

	// Releasing B (the last real ref) closes the connection exactly once.
	pool.Release(b)
	if got := fc.getCloseCount(); got != 1 {
		t.Fatalf("after releasing B, closeCount=%d, want 1", got)
	}
}

// TestPoolDoubleReleaseIsIdempotentPerHandle pins the idempotence contract:
// calling Release on the same handle many times is equivalent to calling it
// once. The refcount drops by exactly one per handle, never more.
func TestPoolDoubleReleaseIsIdempotentPerHandle(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u"}

	var fc *fakeClient
	pool.dial = func(key poolKey) (sshClientConn, error) {
		fc = &fakeClient{}
		return fc, nil
	}

	h, _ := pool.Acquire(context.Background(), key) // refcount 1
	for range 5 {
		pool.Release(h)
	}
	if got := fc.getCloseCount(); got != 1 {
		t.Fatalf("after 5 releases of one handle, closeCount=%d, want 1 (idempotent per handle)", got)
	}
	// Pool must be empty — the one real ref released and closed.
	if pool.Count() != 0 {
		t.Fatalf("pool.Count()=%d, want 0 after last ref released", pool.Count())
	}
}

// TestPoolDistinctIdentitiesGetSeparateConnections is the authorization
// invariant (AD-4): the same host+user+port with TWO DIFFERENT stored
// identities must get two separate connections. Sharing would mean one
// credential's authenticated session carries another's traffic. The identity
// field of poolKey is the principal isolation boundary; widening the key
// (dropping identity) is the authorization bug this test exists to catch.
func TestPoolDistinctIdentitiesGetSeparateConnections(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))

	var dialed []poolKey
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialed = append(dialed, key)
		return &fakeClient{}, nil
	}

	base := poolKey{host: "prod.example.com", user: "ops", port: 22}
	_, _ = pool.Acquire(context.Background(), poolKey{host: base.host, port: base.port, user: base.user, identity: "cred:victim:1"})
	_, _ = pool.Acquire(context.Background(), poolKey{host: base.host, port: base.port, user: base.user, identity: "cred:attacker:1"})

	if len(dialed) != 2 {
		t.Fatalf("distinct identities dialed %d connections, want 2 (principal isolation)", len(dialed))
	}
	if dialed[0].identity == dialed[1].identity {
		t.Fatalf("expected two different identities, both = %q", dialed[0].identity)
	}
}

// TestPoolSameIdentitySharesOneConnection is the complement: two tabs with
// the SAME identity on the same host+user+port share one connection (the
// resource win AD-4 is for). One dial, two handles, refcount 2.
func TestPoolSameIdentitySharesOneConnection(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))

	var dialCount int
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialCount++
		return &fakeClient{}, nil
	}

	key := poolKey{host: "prod.example.com", user: "ops", port: 22, identity: "cred:ops:1"}
	a, _ := pool.Acquire(context.Background(), key)
	b, _ := pool.Acquire(context.Background(), key)

	if dialCount != 1 {
		t.Fatalf("same identity dialed %d times, want 1 (shared connection)", dialCount)
	}
	if a.ref != b.ref {
		t.Fatal("two handles for the same identity do not share a refcount")
	}
	if a.ref.n != 2 {
		t.Fatalf("shared refcount = %d, want 2", a.ref.n)
	}
}

// TestPoolJumpRouteSeparatesFromDirect pins the jump-route component of the
// key: the same target dialed directly vs. through a bastion gets two
// entries. Pooling them together would let the bastion's transport carry
// direct traffic and vice versa.
func TestPoolJumpRouteSeparatesFromDirect(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))

	var dialed []poolKey
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialed = append(dialed, key)
		return &fakeClient{}, nil
	}

	direct := poolKey{host: "t", user: "u", port: 22, identity: "id", jumpRoute: ""}
	viaJump := poolKey{host: "t", user: "u", port: 22, identity: "id", jumpRoute: "bastion@b:22/id"}
	_, _ = pool.Acquire(context.Background(), direct)
	_, _ = pool.Acquire(context.Background(), viaJump)

	if len(dialed) != 2 {
		t.Fatalf("direct vs jump-routed dialed %d, want 2 (route isolation)", len(dialed))
	}
}

// TestPoolJumpTransportClosesWithLastTarget proves the jump-lifetime model:
// a target dialed via a bastion holds a ref on the bastion's own pool entry.
// When the last target through that bastion closes, the bastion's refcount
// drops to zero and the bastion connection closes. This uses a real
// pooledSSHConn with a release hook (as production does) to verify the wiring
// end-to-end at the pool level.
func TestPoolJumpTransportClosesWithLastTarget(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))

	var bastionConn, targetConn *fakeClient
	var bastionHandle *poolHandle

	jumpKey := poolKey{host: "bastion", user: "jump", port: 22, identity: "cred:jump:1"}
	targetKey := poolKey{host: "target", user: "ops", port: 22, identity: "cred:ops:1", jumpRoute: "jump@bastion:22/cred:jump:1"}

	// Bastion dial: a bare pooledSSHConn (no release hook — the bastion's
	// lifetime is governed by its own pool refcount).
	pool.dial = func(key poolKey) (sshClientConn, error) {
		if key.host == "bastion" {
			bastionConn = &fakeClient{}
			return &pooledSSHConn{client: bastionConn}, nil
		}
		// Target dial: acquire the bastion from the pool, return a
		// pooledSSHConn whose Close releases the bastion handle.
		bh, err := pool.Acquire(context.Background(), jumpKey)
		if err != nil {
			return nil, err
		}
		bastionHandle = bh
		targetConn = &fakeClient{}
		return &pooledSSHConn{
			client:  targetConn,
			release: func() { pool.Release(bastionHandle) },
		}, nil
	}

	// Acquire the target — this dials the bastion (refcount 1) then the
	// target (refcount 1), and the target holds a bastion ref.
	target, err := pool.Acquire(context.Background(), targetKey)
	if err != nil {
		t.Fatalf("Acquire target: %v", err)
	}

	// A second target through the same bastion shares the bastion connection.
	target2, err := pool.Acquire(context.Background(), targetKey)
	if err != nil {
		t.Fatalf("Acquire target2: %v", err)
	}

	// Bastion is open (two targets reference it), neither connection closed.
	if got := bastionConn.getCloseCount(); got != 0 {
		t.Fatalf("bastion closed before last target: closeCount=%d", got)
	}
	if got := targetConn.getCloseCount(); got != 0 {
		t.Fatalf("target closed while live: closeCount=%d", got)
	}

	// Close the first target — bastion must stay open (target2 still needs it).
	pool.Release(target)
	if got := bastionConn.getCloseCount(); got != 0 {
		t.Fatalf("bastion closed with a target still live: closeCount=%d", got)
	}

	// Close the last target — the target closes, AND the bastion closes
	// because its refcount drops to zero. This is the AD-4 jump-lifetime
	// requirement: the jump transport closes with the last target that
	// needed it.
	pool.Release(target2)
	if got := targetConn.getCloseCount(); got != 1 {
		t.Fatalf("target not closed on last ref: closeCount=%d, want 1", got)
	}
	if got := bastionConn.getCloseCount(); got != 1 {
		t.Fatalf("bastion not closed with last target: closeCount=%d, want 1", got)
	}
	if pool.Count() != 0 {
		t.Fatalf("pool not empty after last target released: Count=%d", pool.Count())
	}
}

// TestPoolConcurrentAcquireRelease contends Acquire/Release across goroutines
// and is run under -race. A test that serialises its goroutines proves
// nothing about a refcount; this one actually contends on the same key.
func TestPoolConcurrentAcquireRelease(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u", identity: "id"}

	var dialCount int
	var dialMu sync.Mutex
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialMu.Lock()
		dialCount++
		dialMu.Unlock()
		time.Sleep(10 * time.Millisecond) // slow dial so Acquire calls overlap
		return &fakeClient{}, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := pool.Acquire(context.Background(), key)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			// Hold the ref briefly so Release contends with other Acquires.
			time.Sleep(time.Millisecond)
			pool.Release(h)
		}()
	}
	wg.Wait()

	// All refs released — pool must be empty and the connection closed.
	if pool.Count() != 0 {
		t.Fatalf("pool.Count()=%d after all releases, want 0", pool.Count())
	}
}

// TestPoolConcurrentDoubleRelease contends double-releases under -race. Each
// goroutine acquires a handle and releases it twice from two goroutines;
// the once guard must make exactly one decrement stick, so the connection
// closes exactly once when the last handle's real ref drops.
func TestPoolConcurrentDoubleRelease(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	key := poolKey{host: "h", user: "u", identity: "id"}

	var fc *fakeClient
	pool.dial = func(key poolKey) (sshClientConn, error) {
		fc = &fakeClient{}
		return fc, nil
	}

	const goroutines = 10
	handles := make([]*poolHandle, goroutines)
	for i := range handles {
		handles[i], _ = pool.Acquire(context.Background(), key)
	}

	var wg sync.WaitGroup
	for _, h := range handles {
		wg.Add(2)
		go func(h *poolHandle) {
			defer wg.Done()
			pool.Release(h)
		}(h)
		go func(h *poolHandle) {
			defer wg.Done()
			pool.Release(h)
		}(h)
	}
	wg.Wait()

	// Every handle's real ref released exactly once → refcount 0 → one close.
	if got := fc.getCloseCount(); got != 1 {
		t.Fatalf("after concurrent double-release of %d handles, closeCount=%d, want 1", goroutines, got)
	}
	if pool.Count() != 0 {
		t.Fatalf("pool.Count()=%d, want 0", pool.Count())
	}
}

// TestPoolDrainClosesMatchingEntries verifies that Drain closes all
// connections matching the predicate and leaves non-matching ones intact.
func TestPoolDrainClosesMatchingEntries(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	dialCount := 0
	pool.dial = func(key poolKey) (sshClientConn, error) {
		dialCount++
		return &fakeClient{}, nil
	}

	// Acquire three connections with different identities.
	h1, err := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id1"})
	if err != nil {
		t.Fatalf("Acquire h1: %v", err)
	}
	h2, err := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id2"})
	if err != nil {
		t.Fatalf("Acquire h2: %v", err)
	}
	h3, err := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id3"})
	if err != nil {
		t.Fatalf("Acquire h3: %v", err)
	}

	if pool.Count() != 3 {
		t.Fatalf("pool.Count()=%d, want 3", pool.Count())
	}

	// Drain only identity "id1".
	closed := pool.Drain(func(key poolKey) bool {
		return key.identity == "id1"
	})
	if closed != 1 {
		t.Fatalf("Drain returned %d, want 1", closed)
	}
	if pool.Count() != 2 {
		t.Fatalf("after drain, pool.Count()=%d, want 2", pool.Count())
	}

	// h2 and h3 still work.
	if _, err := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id2"}); err != nil {
		t.Fatalf("Acquire h2 after drain: %v", err)
	}
	if _, err := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id3"}); err != nil {
		t.Fatalf("Acquire h3 after drain: %v", err)
	}

	// Release cleanup.
	pool.Release(h1)
	pool.Release(h2)
	pool.Release(h3)
}

// TestPoolDrainByRoute verifies Drain can match by jump route component.
func TestPoolDrainByRoute(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	pool.dial = func(key poolKey) (sshClientConn, error) {
		return &fakeClient{}, nil
	}

	h1, _ := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id", jumpRoute: "jump-a"})
	h2, _ := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id", jumpRoute: "jump-b"})
	h3, _ := pool.Acquire(context.Background(), poolKey{host: "h", port: 22, user: "u", identity: "id", jumpRoute: ""})

	if pool.Count() != 3 {
		t.Fatalf("pool.Count()=%d, want 3", pool.Count())
	}

	// Drain connections with the jump-a route.
	closed := pool.Drain(func(key poolKey) bool {
		return key.jumpRoute == "jump-a"
	})
	if closed != 1 {
		t.Fatalf("Drain returned %d, want 1", closed)
	}
	if pool.Count() != 2 {
		t.Fatalf("after drain, pool.Count()=%d, want 2", pool.Count())
	}

	pool.Release(h1)
	pool.Release(h2)
	pool.Release(h3)
}

// TestPoolDrainEmptyIsSafe verifies that Drain on an empty pool is a no-op.
func TestPoolDrainEmptyIsSafe(t *testing.T) {
	pool := NewConnPool(log.NewSlogAdapter(nil))
	closed := pool.Drain(func(key poolKey) bool { return true })
	if closed != 0 {
		t.Fatalf("Drain empty pool returned %d, want 0", closed)
	}
}
