package session_test

// nocx-isjh4, closers 3 and 4: an exited session nobody has claimed does not
// live for the life of the helper (D-amendment 3), and a spawn that would
// otherwise be refused for the aggregate budget first closes exited,
// unattached sessions — oldest exit first — before it is refused
// (D-amendment 4). Neither ever touches a live shell or an exited session a
// coordinator is still attached to.
//
// Every test here drives a FAKE clock rather than sleeping: the "timer" is a
// comparison against Service's own clock seam (s.now), evaluated lazily
// wherever staleness would otherwise be observable — WindowBytesInUse, the
// inventory read, and a spawn's own budget check.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeClock is the injected clock seam: Advance moves it without anybody
// sleeping, which is the whole point — a test asserting a 24-hour boundary
// must not cost 24 hours, or even one.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newServiceWithClock is newService (service_test.go) plus the one seam it
// does not expose: a caller that must control what s.now answers.
func newServiceWithClock(t *testing.T, sink session.Sink, spawner session.Spawner, limits session.Limits, now func() time.Time) *session.Service {
	t.Helper()
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		Log:        discardLog(),
		Limits:     limits,
		Now:        now,
	})
	release := bindTo(svc, sink)
	t.Cleanup(func() {
		release()
		svc.Close()
	})
	return svc
}

func awaitExitCount(t *testing.T, sink *recordingSink, n int) {
	t.Helper()
	awaitSink(t, sink, "an exit notification", func() bool {
		return len(sink.notifications(proto.EventSessionExit)) >= n
	})
}

// TestAnExitedUnattachedSessionExpiresAfterItsTTLAndNotBefore is
// acceptance test (a): spawn, exit, advance the fake clock past the TTL, and
// the session is gone from the inventory with its budget released. A tick
// short of the TTL it is still there — the pairing that makes the boundary
// an assertion rather than a guess.
func TestAnExitedUnattachedSessionExpiresAfterItsTTLAndNotBefore(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ttl := 24 * time.Hour
	svc := newServiceWithClock(t, sink, spawner, session.Limits{
		DefaultWindowBytes:  256 << 10,
		MinWindowBytes:      256 << 10,
		MaxWindowBytes:      256 << 10,
		BudgetBytes:         1 << 20,
		UnclaimedSessionTTL: ttl,
	}, clock.Now)

	spawnOne(t, svc)
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)

	clock.Advance(ttl - time.Nanosecond)
	if used := svc.WindowBytesInUse(); used == 0 {
		t.Fatal("the session's budget was released a tick before its TTL elapsed")
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("the session left the inventory before its TTL elapsed: %+v", inv.Sessions)
	}

	clock.Advance(time.Nanosecond)
	if used := svc.WindowBytesInUse(); used != 0 {
		t.Fatalf("window bytes in use = %d once the TTL elapsed, want 0", used)
	}
	inv = call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 0 {
		t.Fatalf("an unclaimed session outlived its TTL: %+v", inv.Sessions)
	}
}

// TestASpawnEvictsExitedUnattachedSessionsOldestExitFirst is acceptance test
// (b): the budget is filled with exited, unattached sessions and the next
// spawn succeeds anyway — the OLDEST exit is the one evicted, never the
// newer one, so a coordinator that comes back for the newer session's result
// still finds it.
func TestASpawnEvictsExitedUnattachedSessionsOldestExitFirst(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	// The window sizes are pinned at D8's floor (2*creditLimit, session.go)
	// rather than a round-looking number below it: a smaller ask is silently
	// raised to the floor, and a test that ignored that would be asserting a
	// budget arithmetic it never actually ran under.
	const windowBytes = 2 * 64 << 10 // 128 KiB, the floor
	svc := newServiceWithClock(t, sink, spawner, session.Limits{
		DefaultWindowBytes:  windowBytes,
		MinWindowBytes:      windowBytes,
		MaxWindowBytes:      windowBytes,
		BudgetBytes:         2 * windowBytes, // room for exactly two sessions
		UnclaimedSessionTTL: 24 * time.Hour,
	}, clock.Now)

	oldest := spawnOne(t, svc)
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)

	// A distinct, later exit time, so "oldest first" is an assertion the
	// test can fail rather than something true by accident of ordering.
	clock.Advance(time.Minute)
	newer := spawnOne(t, svc)
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 2)

	// The budget is now exactly full with two exited, unattached sessions;
	// a third spawn is impossible without evicting one of them.
	third := spawnOne(t, svc)
	if third.Session.Session == "" {
		t.Fatal("a spawn over a budget held entirely by exited, unattached sessions was refused")
	}

	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	present := map[string]bool{}
	for _, e := range inv.Sessions {
		present[e.Session.Session] = true
	}
	if present[oldest.Session.Session] {
		t.Error("the oldest exit was still in the inventory; eviction did not free it")
	}
	if !present[newer.Session.Session] {
		t.Error("the newer exited session was evicted instead of the oldest one")
	}
	if !present[third.Session.Session] {
		t.Error("the spawn that eviction made room for is missing from the inventory")
	}
	if len(inv.Sessions) != 2 {
		t.Fatalf("inventory has %d sessions, want 2 (newer + third): %+v", len(inv.Sessions), inv.Sessions)
	}
}

// TestASpawnNeverEvictsALiveSession is acceptance test (c), paired with the
// normal-machine success it is the refusal half of: a budget held entirely by
// LIVE shells refuses the spawn and evicts nothing, and once the shell
// exits — still with nobody attached — the identical spawn succeeds.
func TestASpawnNeverEvictsALiveSession(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	const windowBytes = 2 * 64 << 10 // 128 KiB, D8's floor (session.go)
	svc := newService(t, sink, spawner, session.Limits{
		DefaultWindowBytes: windowBytes,
		MinWindowBytes:     windowBytes,
		MaxWindowBytes:     windowBytes,
		BudgetBytes:        windowBytes, // room for exactly one
	})

	live := spawnOne(t, svc)
	if _, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{})); err == nil {
		t.Fatal("a spawn over a budget held by a live session was accepted")
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session.Session != live.Session.Session {
		t.Fatalf("the live session was not left exactly as it was: %+v", inv.Sessions)
	}
	spawner.mu.Lock()
	forked := len(spawner.procs)
	spawner.mu.Unlock()
	if forked != 1 {
		t.Fatalf("%d processes were forked, want 1: the refused spawn started a shell it then abandoned", forked)
	}

	// And on a normal machine it succeeds: once the shell exits (still
	// unattached), eviction may take it — no TTL wait required, because
	// eviction-under-pressure and expiry are two different closers.
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)
	next := spawnOne(t, svc)
	if next.Session.Session == "" {
		t.Fatal("a spawn after the blocking session exited was still refused")
	}
}

// TestASpawnNeverEvictsAnExitedSessionACoordinatorIsAttachedTo is acceptance
// test (d), paired with the normal-machine success once the attachment is
// released: an exited session a coordinator still holds may be mid-read of
// the very result it came back for (D5's surviving half), so eviction must
// leave it alone even under budget pressure — and once nobody is attached to
// it any more, the same spawn succeeds.
func TestASpawnNeverEvictsAnExitedSessionACoordinatorIsAttachedTo(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	const windowBytes = 2 * 64 << 10 // 128 KiB, D8's floor (session.go)
	svc := newService(t, sink, spawner, session.Limits{
		DefaultWindowBytes: windowBytes,
		MinWindowBytes:     windowBytes,
		MaxWindowBytes:     windowBytes,
		BudgetBytes:        windowBytes,
	})

	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("11111111111111111111111111111111")
	att := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session,
	})
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)

	if _, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{})); err == nil {
		t.Fatal("a spawn evicted an exited session a coordinator is still attached to")
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session.Session != entry.Session.Session {
		t.Fatalf("the attached, exited session was not left exactly as it was: %+v", inv.Sessions)
	}

	// And on a normal machine it succeeds: detach releases the one fact that
	// made this session ineligible, and the same spawn now goes through.
	call[proto.DetachResult](t, svc, proto.OpDetach, proto.DetachParams{Attachment: att.Attachment})
	next := spawnOne(t, svc)
	if next.Session.Session == "" {
		t.Fatal("a spawn after the attachment was released was still refused")
	}
}

// TestTheScheduledSweepReleasesAnOrphanedSessionOnItsOwn is nocx-isjh4's
// coordinator review, gap 2: a sweep that only ran when WindowBytesInUse,
// the inventory or a spawn happened to be asked something never runs at all
// for a helper nobody calls again — exactly the orphan case D-amendment 3
// exists for, and memory would be held indefinitely. The Service now runs
// sweepExpired on ITS OWN schedule (Options.SweepInterval), started with
// the Service and stopped by Close.
//
// The wait below is on OBSERVABLE STATE — WindowBytesInUse settling to
// zero — polled with a bound, never a fixed sleep-then-assert: a session
// that never expires (a defect in sweepExpired itself, not in the
// schedule) fails this test exactly as slowly as the timeout, and nothing
// here depends on how fast this machine happens to be.
func TestTheScheduledSweepReleasesAnOrphanedSessionOnItsOwn(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	const windowBytes = 2 * 64 << 10 // 128 KiB, D8's floor (session.go)
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		Log:        discardLog(),
		Now:        clock.Now,
		Limits: session.Limits{
			DefaultWindowBytes:  windowBytes,
			MinWindowBytes:      windowBytes,
			MaxWindowBytes:      windowBytes,
			BudgetBytes:         windowBytes,
			UnclaimedSessionTTL: time.Hour,
		},
		// Real wall-clock time, deliberately short — this is the SCHEDULE,
		// answered independently of the fake clock, which only ever answers
		// what sweepExpired compares a session's age against.
		SweepInterval: 5 * time.Millisecond,
	})
	release := bindTo(svc, sink)
	t.Cleanup(func() {
		release()
		svc.Close()
	})

	spawnOne(t, svc)
	spawner.last().exit(nil)
	awaitExitCount(t, sink, 1)

	// Already past the TTL on the fake clock before any tick of the real
	// schedule has a chance to look — so the very first scheduled tick is
	// what has to notice, with nobody in this test calling anything that
	// would sweep it by hand.
	clock.Advance(2 * time.Hour)

	waittest.WaitFor(t, "the scheduled sweep to release the orphaned session's budget",
		func() bool { return svc.WindowBytesInUse() == 0 })
}
