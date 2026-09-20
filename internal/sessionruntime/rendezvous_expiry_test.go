package sessionruntime

import (
	"sync"
	"testing"
	"time"
)

// The bounded missing-fence wait (design §6.4), driven by an INJECTED
// scheduler: a test fires the wait itself and observes a STATE — never a
// duration. The production scheduler is time.AfterFunc, handed at New when
// the config leaves the seam nil; the duration is the config's to choose and
// no assertion here reads a clock.

// expiryScheduler is the controllable stand-in for time.AfterFunc: it
// records what was armed, keeps only the LIVE trigger (a rearm supersedes the
// one before, exactly as a stopped timer does) and lets a test fire it.
type expiryScheduler struct {
	mu      sync.Mutex
	armed   int
	stopped int
	pending func()
	// all is every trigger in arming order, live or superseded: firing an
	// earlier one after a later was armed is exactly the stale-callback
	// race a real timer can produce (fire-while-stop-is-being-replaced).
	all []func()
}

func (e *expiryScheduler) afterFunc(_ time.Duration, f func()) func() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.armed++
	e.pending = f
	e.all = append(e.all, f)
	return func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.stopped++
		e.pending = nil
		return true
	}
}

// fire runs the live trigger, if one is armed. The trigger is
// [Session.ExpireRendezvous]'s own call, which takes the session lock
// itself, so nothing here may hold it.
func (e *expiryScheduler) fire() {
	e.mu.Lock()
	f := e.pending
	e.mu.Unlock()
	if f != nil {
		f()
	}
}

// fireAt runs trigger i by arming order, including a superseded one.
func (e *expiryScheduler) fireAt(i int) {
	e.mu.Lock()
	f := e.all[i]
	e.mu.Unlock()
	if f != nil {
		f()
	}
}

func (e *expiryScheduler) live() (armed, stopped int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.armed, e.stopped
}

// withExpiry hands a realRuntime the injected scheduler and a duration no
// test reads.
func withExpiry(s *expiryScheduler) func(*Config) {
	return func(c *Config) {
		c.RendezvousExpiry = time.Hour
		c.ExpireAfter = s.afterFunc
	}
}

// TestACompletionWhoseFenceNeverArrivesEndsExpired is the bounded wait
// elapsing with the sighting half missing: the rendezvous ends EXPIRED, the
// capture is marked no-fence rather than complete, and nothing is pinned —
// an interval with no authenticated boundary may not be described as the
// command's complete output.
func TestACompletionWhoseFenceNeverArrivesEndsExpired(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))

	nonce := fenceNonceFromString(t, fenceNonceHex)
	s.Completed(s.Incarnation(), nonce, 0)
	if rv := rendezvousOf(s, nonce); rv.State != RendezvousAwaitingSighting {
		t.Fatalf("after the completion the rendezvous is %s, want awaiting-sighting", rendezvousStateName(rv.State))
	}

	sched.fire()

	rv := rendezvousOf(s, nonce)
	if rv.State != RendezvousExpired {
		t.Fatalf("after the wait elapsed the rendezvous is %s, want expired", rendezvousStateName(rv.State))
	}
	if len(rv.PinnedSource) != 0 {
		t.Fatalf("an expired rendezvous still pins %q, want nothing pinned", rv.PinnedSource)
	}
	if got := s.Completeness(); got != CompletenessNoFence {
		t.Fatalf("completeness is %v after the rendezvous expired, want no-fence: the interval has no authenticated boundary", got)
	}
}

// TestTheWaitRearmsForTheNextRendezvous pins that the wait follows the
// STATE, not the first fence: a completed rendezvous's trigger is inert, and
// the NEXT pending rendezvous gets its own — a wait that armed once and
// never again would leave every later fence alone with no policy at all.
func TestTheWaitRearmsForTheNextRendezvous(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))

	// First meeting: fence sighted, completion closes it.
	if err := s.Ingest([]byte("out" + fenceSeq(fenceNonceHex))); err != nil {
		t.Fatalf("ingest the fenced output: %v", err)
	}
	if armed, _ := sched.live(); armed != 1 {
		t.Fatalf("a parked rendezvous armed the wait %d times, want exactly one", armed)
	}
	s.Completed(s.Incarnation(), fenceNonceFromString(t, fenceNonceHex), 0)
	if _, stopped := sched.live(); stopped != 1 {
		t.Fatalf("a completed rendezvous stopped the wait %d times, want exactly one", stopped)
	}
	sched.fire() // the stopped trigger must be inert
	if rv := rendezvousOf(s, fenceNonceFromString(t, fenceNonceHex)); rv.State != RendezvousComplete {
		t.Fatalf("firing a stopped trigger left the rendezvous %s, want complete", rendezvousStateName(rv.State))
	}

	// Second meeting: its own fence, its own wait.
	const second = "cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02cd02"
	if err := s.Ingest([]byte("more" + fenceSeq(second))); err != nil {
		t.Fatalf("ingest the second fenced output: %v", err)
	}
	if armed, _ := sched.live(); armed != 2 {
		t.Fatalf("the second rendezvous re-armed the wait (armed=%d), want two", armed)
	}
	sched.fire()
	if rv := rendezvousOf(s, fenceNonceFromString(t, second)); rv.State != RendezvousExpired {
		t.Fatalf("the second rendezvous is %s after its wait elapsed, want expired", rendezvousStateName(rv.State))
	}
}

// TestNoConfiguredExpirySchedulesNothing pins the zero value: a runtime
// built without the policy arms nothing, which is why the contract's own
// schedules can drive [Session.ExpireRendezvous] by hand without a timer
// arriving underneath them.
func TestNoConfiguredExpirySchedulesNothing(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, func(c *Config) { c.ExpireAfter = sched.afterFunc })

	if err := s.Ingest([]byte("out" + fenceSeq(fenceNonceHex))); err != nil {
		t.Fatalf("ingest the fenced output: %v", err)
	}
	if armed, stopped := sched.live(); armed != 0 || stopped != 0 {
		t.Fatalf("a runtime with no configured expiry touched the scheduler (armed=%d stopped=%d), want untouched", armed, stopped)
	}
}

// TestAStaleWaitDoesNotExpireTheNextRendezvous: a one-shot wait whose
// callback fires while it is being replaced must not spend the interval of
// the rendezvous that replaced it — the second fence's pending window
// belongs to the second wait alone.
func TestAStaleWaitDoesNotExpireTheNextRendezvous(t *testing.T) {
	sched := &expiryScheduler{}
	s, _, _ := realRuntime(t, withExpiry(sched))

	if err := s.Ingest([]byte("out" + fenceSeq(fenceNonceHex))); err != nil {
		t.Fatalf("ingest the first fenced output: %v", err)
	}
	first := fenceNonceFromString(t, fenceNonceHex)
	s.Completed(s.Incarnation(), first, 0)
	if rv := rendezvousOf(s, first); rv.State != RendezvousComplete {
		t.Fatalf("the first rendezvous is %s, want complete", rendezvousStateName(rv.State))
	}

	const stale = "ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03ef03"
	second := fenceNonceFromString(t, stale)
	if err := s.Ingest([]byte("more" + fenceSeq(stale))); err != nil {
		t.Fatalf("ingest the second fenced output: %v", err)
	}
	if rv := rendezvousOf(s, second); rv.State != RendezvousAwaitingAuthenticated {
		t.Fatalf("the second rendezvous is %s, want awaiting-authenticated", rendezvousStateName(rv.State))
	}

	sched.fireAt(0) // the FIRST wait: disarmed when its meeting completed
	if rv := rendezvousOf(s, second); rv.State != RendezvousAwaitingAuthenticated {
		t.Fatalf("a stale wait expired the second rendezvous (%s), want it still parked: its interval belongs to its own wait", rendezvousStateName(rv.State))
	}

	sched.fireAt(1) // the second wait: the one this rendezvous armed
	if rv := rendezvousOf(s, second); rv.State != RendezvousExpired {
		t.Fatalf("the second rendezvous is %s after ITS wait fired, want expired", rendezvousStateName(rv.State))
	}
}
