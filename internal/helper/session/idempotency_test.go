package session_test

// L7 of the local-helper design: the pane's claim on a session is durable
// BEFORE the spawn, or the spawn is not ours. The helper mints the
// authoritative session id, so a coordinator that dies between the spawn and
// its durable binding leaves a live PTY no pane claims — and with D2
// unimplemented the daemon holds it forever. The idempotency key is what makes
// the claim writable first: the coordinator mints it, records it, and only
// then spawns, so the retry after a crash reaches the SAME session instead of
// forking a second one.
//
// The interval these tests state, both ends named: a key names its session
// from before the fork until the row leaves the inventory. Nothing here waits
// on a duration.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// gatedSpawner is a spawner the test opens by hand: every Spawn parks until
// the test releases it. It is what makes "the claim exists before the fork"
// observable — with two spawns in flight and neither able to complete, the
// count of forks is the whole assertion.
type gatedSpawner struct {
	inner   *fakeSpawner
	entered chan struct{}
	release chan struct{}
}

func newGatedSpawner() *gatedSpawner {
	return &gatedSpawner{
		inner:   &fakeSpawner{},
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (g *gatedSpawner) Spawn(req session.SpawnRequest) (session.Process, error) {
	g.entered <- struct{}{}
	<-g.release
	return g.inner.Spawn(req)
}

func (g *gatedSpawner) forks() int {
	g.inner.mu.Lock()
	defer g.inner.mu.Unlock()
	return len(g.inner.reqs)
}

func spawnWithKey(t *testing.T, svc *session.Service, key string) proto.SessionEntry {
	t.Helper()
	return call[proto.SpawnResult](t, svc, proto.OpSpawn,
		proto.SpawnParams{Cols: 80, Rows: 24, IdempotencyKey: key}).Entry
}

// TestASpawnRepeatedWithOneKeyIsOneSession is the acceptance criterion itself:
// the retry that follows a coordinator crash must reach the session the first
// attempt made, not a second shell beside it.
func TestASpawnRepeatedWithOneKeyIsOneSession(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})

	first := spawnWithKey(t, svc, "pane-7")
	second := spawnWithKey(t, svc, "pane-7")

	if first.Session != second.Session {
		t.Fatalf("one key produced two sessions: %v and %v", first.Session, second.Session)
	}
	spawner.mu.Lock()
	forked := len(spawner.reqs)
	spawner.mu.Unlock()
	if forked != 1 {
		t.Fatalf("%d shells were forked for one key, want 1", forked)
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("the inventory holds %d sessions for one key, want 1", len(inv.Sessions))
	}
	// The repeat is an ANSWER about an existing session and not a second
	// reservation: the budget was committed once.
	if used, want := svc.WindowBytesInUse(), inv.Sessions[0].Launch.WindowBytes; used != want {
		t.Fatalf("budget in use = %d, want %d: the repeated spawn reserved a second window", used, want)
	}
}

// TestTwoKeysAreTwoSessions — the key deduplicates a RETRY and nothing else.
// Two panes are two claims and must be two shells.
func TestTwoKeysAreTwoSessions(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})

	a := spawnWithKey(t, svc, "pane-a")
	b := spawnWithKey(t, svc, "pane-b")
	if a.Session == b.Session {
		t.Fatalf("two keys resolved to one session: %v", a.Session)
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 2 {
		t.Fatalf("the inventory holds %d sessions for two keys, want 2", len(inv.Sessions))
	}
}

// TestASpawnWithNoKeyPromisesNothing — the key is CALLER-MINTED, so a caller
// that minted none gets no promise. Two keyless spawns are two sessions, which
// is what every level-1 caller has always got and must go on getting.
func TestASpawnWithNoKeyPromisesNothing(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})

	a := spawnOne(t, svc)
	b := spawnOne(t, svc)
	if a.Session == b.Session {
		t.Fatalf("two keyless spawns collapsed into one session: %v", a.Session)
	}
}

// TestTheKeyIsClaimedBeforeTheForkSoAConcurrentRepeatCannotForkTwice is the
// ORDER, which is the whole of L7. A key checked after the fork would be a key
// that deduplicates everything except the case it exists for: the retry that
// arrives while the first attempt is still in the spawner.
func TestTheKeyIsClaimedBeforeTheForkSoAConcurrentRepeatCannotForkTwice(t *testing.T) {
	spawner := newGatedSpawner()
	svc := newService(t, newSink(), spawner, session.Limits{})

	var wg sync.WaitGroup
	entries := make([]proto.SessionEntry, 2)
	errs := make([]error, 2)
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.Call(callCtx(), proto.OpSpawn,
				mustJSON(t, proto.SpawnParams{Cols: 80, Rows: 24, IdempotencyKey: "pane-race"}))
			if err != nil {
				errs[i] = err
				return
			}
			spawned, ok := res.(proto.SpawnResult)
			if !ok {
				errs[i] = fmt.Errorf("spawn answered %T, want a SpawnResult", res)
				return
			}
			entries[i] = spawned.Entry
		}(i)
	}

	// One spawn has reached the spawner. The second is behind the claim, and
	// the claim is what this test is about: it exists before the fork.
	<-spawner.entered
	close(spawner.release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent spawn %d failed: %v", i, err)
		}
	}
	if entries[0].Session != entries[1].Session {
		t.Fatalf("a concurrent repeat produced two sessions: %v and %v", entries[0].Session, entries[1].Session)
	}
	if forked := spawner.forks(); forked != 1 {
		t.Fatalf("%d shells were forked for one key, want 1", forked)
	}
}

// TestAFailedSpawnReleasesItsKey — the closing end of the interval on the
// failure side. A key held by a spawn that produced nothing would wedge that
// pane forever: every retry would be answered with a session that does not
// exist, or refused for a session that was never made.
func TestAFailedSpawnReleasesItsKey(t *testing.T) {
	spawner := &fakeSpawner{err: errors.New("openpty: too many open files")}
	svc := newService(t, newSink(), spawner, session.Limits{})

	if _, err := svc.Call(context.Background(), proto.OpSpawn,
		mustJSON(t, proto.SpawnParams{IdempotencyKey: "pane-9"})); err == nil {
		t.Fatal("a spawn whose PTY failed reported success")
	}

	spawner.mu.Lock()
	spawner.err = nil
	spawner.mu.Unlock()

	entry := spawnWithKey(t, svc, "pane-9")
	if entry.Session.Session == "" {
		t.Fatal("the key stayed claimed by a spawn that produced nothing")
	}
	spawner.mu.Lock()
	forked := len(spawner.reqs)
	spawner.mu.Unlock()
	if forked != 2 {
		t.Fatalf("the spawner was called %d times, want 2: the retry after a failure must fork", forked)
	}
}

// TestClosingTheSessionReleasesItsKey is the closing end on the success side,
// and it is the ROW leaving the inventory rather than the process ending.
// close-session is the one verb that removes a row.
func TestClosingTheSessionReleasesItsKey(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})

	first := spawnWithKey(t, svc, "pane-4")
	call[proto.CloseSessionResult](t, svc, proto.OpCloseSession, proto.CloseSessionParams{Session: first.Session})

	second := spawnWithKey(t, svc, "pane-4")
	if second.Session == first.Session {
		t.Fatalf("the key still names the closed session %v", first.Session)
	}
	spawner.mu.Lock()
	forked := len(spawner.reqs)
	spawner.mu.Unlock()
	if forked != 2 {
		t.Fatalf("the spawner was called %d times, want 2: a key whose row is gone must fork again", forked)
	}
}

// TestAnExitedSessionStillAnswersToItsKey pins which end closes the interval,
// because the two candidates differ exactly here. The process exiting does NOT
// remove the row — the exit status is part of the inventory the coordinator
// reconciles against (D5) — so the key must go on naming that row. If the
// process were the end, a retry would fork a live shell over a session the
// coordinator has not yet read the exit status of.
func TestAnExitedSessionStillAnswersToItsKey(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})

	first := spawnWithKey(t, svc, "pane-exit")
	spawner.last().exit(nil)
	awaitSink(t, sink, "the exit notification", func() bool {
		return len(sink.notifications(proto.EventSessionExit)) == 1
	})

	second := spawnWithKey(t, svc, "pane-exit")
	if second.Session != first.Session {
		t.Fatalf("an exited session stopped answering to its key: %v then %v", first.Session, second.Session)
	}
	if second.Exit == nil {
		t.Fatal("the repeated spawn answered with a session carrying no exit status")
	}
	spawner.mu.Lock()
	forked := len(spawner.reqs)
	spawner.mu.Unlock()
	if forked != 1 {
		t.Fatalf("%d shells were forked, want 1: the key must not fork over a row still in the inventory", forked)
	}
}

// TestAnOverlongKeyIsRefusedAndNothingIsForked — the bound the contract
// declares is enforced where the params are read, or the contract is theatre.
// The paired half is every other test in this file: on an ordinary key it
// succeeds.
func TestAnOverlongKeyIsRefusedAndNothingIsForked(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})

	_, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{
		IdempotencyKey: strings.Repeat("k", proto.MaxIdempotencyKey+1),
	}))
	if err == nil {
		t.Fatal("a key past the declared bound was accepted")
	}
	if !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("the refusal does not name what was wrong: %v", err)
	}
	spawner.mu.Lock()
	forked := len(spawner.reqs)
	spawner.mu.Unlock()
	if forked != 0 {
		t.Fatalf("%d shells were forked by a refused spawn", forked)
	}
	if used := svc.WindowBytesInUse(); used != 0 {
		t.Fatalf("a refused spawn consumed %d bytes of the window budget", used)
	}
}
