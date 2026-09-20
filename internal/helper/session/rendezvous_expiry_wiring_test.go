package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// The bounded missing-fence wait is a PRODUCTION WIRING fact (design §6.4):
// the state machine exists, but nothing would observe it if a spawned
// session's runtime were built without the policy. This file proves the
// wiring: what spawn builds hands the service's wait and scheduler to
// sessionruntime, so a pending rendezvous on a REAL session expires when the
// trigger fires — and the trigger here is injected, never a clock.

// expiryTrigger is the test-controlled scheduler: it keeps only the live
// trigger, exactly as a rearming timer does, and lets a test fire it.
type expiryTrigger struct {
	mu      sync.Mutex
	pending func()
}

func (e *expiryTrigger) afterFunc(_ time.Duration, f func()) func() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending = f
	return func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.pending = nil
		return true
	}
}

func (e *expiryTrigger) fire() {
	e.mu.Lock()
	f := e.pending
	e.mu.Unlock()
	if f != nil {
		f()
	}
}

// TestTheSpawnedRuntimeCarriesTheBoundedRendezvousWait: a session this
// service spawned, over the PTY a real command will own, holds a runtime
// whose rendezvous expires when the injected wait fires. The wait is fired
// by hand and the assertion is a STATE of the rendezvous — nothing here
// reads a clock.
func TestTheSpawnedRuntimeCarriesTheBoundedRendezvousWait(t *testing.T) {
	trig := &expiryTrigger{}
	svc := New(Options{
		Generation:            "gen-under-test",
		Spawner:               &lcSpawner{},
		Log:                   lcTestLog(),
		RendezvousExpiry:      time.Hour,
		RendezvousExpireAfter: trig.afterFunc,
	})
	res, err := svc.spawn(context.Background(), proto.SpawnParams{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(svc.Close)
	id := res.Entry.Session

	var fence sessionruntime.FenceNonce
	fence[0] = 0xEE
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	trig.fire()

	rv, ok := svc.TestLifecycleRendezvous(id)
	if !ok {
		t.Fatal("the spawned session names no rendezvous")
	}
	if rv.State != sessionruntime.RendezvousExpired {
		t.Fatalf("after the injected wait fired the rendezvous is %v, want expired: the spawned runtime carries no bounded missing-fence policy (design §6.4)", rv.State)
	}
}
