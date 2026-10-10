package app

// THE COORDINATOR'S OWN LIFECYCLE CURSOR, end to end over the shared helper
// daemon (ADR-0077). Each coordinator stores, as it applies them, the offset
// of the last lifecycle frame it applied; the next one resumes the helper's
// stream there. So a command's end the shell spoke while nobody was attached
// is applied exactly once — by the coordinator that comes back — and a frame
// one coordinator applied is never applied again by the next, however many
// times the coordinator is replaced around the same command.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// memCursorStore is the binding row's cursor, kept in memory across the
// test's coordinators the way content.db keeps it across processes. It
// records every write, so a test can wait on "this frame's cursor is
// stored" rather than on a duration.
type memCursorStore struct {
	mu      sync.Mutex
	cursor  map[string]uint64
	history map[string][]uint64
}

func newMemCursorStore() *memCursorStore {
	return &memCursorStore{cursor: map[string]uint64{}, history: map[string][]uint64{}}
}

// ApplyLifecycleFrame applies the frame and then records its cursor — the
// store's own frame, minus the transaction this test has no rows for.
func (m *memCursorStore) ApplyLifecycleFrame(ctx context.Context, sid string, offset uint64, apply func(context.Context) error) error {
	if err := apply(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.cursor[sid]; ok && offset <= cur {
		return nil
	}
	m.cursor[sid] = offset
	m.history[sid] = append(m.history[sid], offset)
	return nil
}

func (m *memCursorStore) stored(sid string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.history[sid])
}

// quitAndWaitGone ends c the way a process ending does and waits until its
// lifecycle leg has seen its carrier go — the fixture's quit closes the
// connection, and until the leg has read that, the helper can still hand it a
// frame the test means for nobody.
func quitAndWaitGone(t *testing.T, c *integratedCoordinator) {
	t.Helper()
	c.quit()
	waittest.WaitFor(t, "the departed coordinator's lifecycle leg saw its carrier go", func() bool {
		for _, f := range c.emitter.any() {
			if f.Lifecycle == lifecyclepub.LifecycleLost {
				return true
			}
		}
		return false
	})
}

// bindingFor is the binding as the store carries it over: the route the
// first open recorded, and the cursor the last coordinator stored.
func (m *memCursorStore) bindingFor(p content.PendingSession) content.PendingSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.cursor[p.SessionID]; ok {
		c := cur
		p.LifecycleApplied = &c
	}
	return p
}

func newCursorKeepingCoordinator(t *testing.T, provider *fakeLaneProvider, store *memCursorStore) *integratedCoordinator {
	t.Helper()
	c := newIntegratedCoordinator(t, provider)
	c.reg.lifecycleCursors = store
	return c
}

// takeBack re-adopts the binding into c through the shipped pass.
func takeBack(t *testing.T, c *integratedCoordinator, binding content.PendingSession) {
	t.Helper()
	adopter := &stubAdopter{}
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, c.reg.inventories(),
		readoptFixture(t, c.coordinator, routesFor(binding), adopter), time.Hour, quietLogger(t))
	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back: %v", adopter.failure())
	}
}

func completedCommands(facts []lifecyclepub.Fact) []string {
	var out []string
	for _, f := range facts {
		out = append(out, f.Attempt.Command)
	}
	return out
}

// runBarrier has the shell draw its prompt, run one more command, and waits
// until c has completed it: every frame the shell spoke before it has been
// applied by then, in stream order, so what c has published is final.
func runBarrier(t *testing.T, shell *shellSide, c *integratedCoordinator, name string) {
	t.Helper()
	id := lifecycle.AttemptID("shell-" + name)
	code := 0
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: name}})
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{9, 9}},
	})
	waittest.WaitFor(t, "the barrier command "+name+" completed", func() bool {
		for _, f := range c.emitter.completed() {
			if f.Attempt.Command == name {
				return true
			}
		}
		return false
	})
}

func TestACommandThatEndedWhileNoCoordinatorWasAttachedIsAppliedOnceOnReturn(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	store := newMemCursorStore()

	first := newCursorKeepingCoordinator(t, provider, store)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	build := lifecycle.AttemptID("shell-build")
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &build, Command: "make build"}})
	// Two frames applied, two cursors stored: the start's effect and its
	// cursor are both in before the coordinator goes.
	waittest.WaitFor(t, "the first coordinator stored the start's cursor", func() bool {
		return store.stored(binding.SessionID) == 2
	})
	quitAndWaitGone(t, first)

	// THE COMMAND ENDS WHILE NO COORDINATOR IS ATTACHED.
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{4}},
	})

	second := newCursorKeepingCoordinator(t, provider, store)
	takeBack(t, second, store.bindingFor(binding))
	runBarrier(t, shell, second, "echo after")

	done := completedCommands(second.emitter.completed())
	if len(done) != 2 || done[1] != "echo after" {
		t.Fatalf("the returned coordinator completed %q, want the command that ended while it was away, once, and then the barrier", done)
	}
	for _, f := range second.emitter.any() {
		if f.Attempt != nil && f.Attempt.ID == string(build) && f.Attempt.State == lifecyclepub.AttemptOpen {
			t.Fatal("the start the first coordinator applied was applied again by the second")
		}
	}
}

func TestACommandCrossingTwoRestartsHasEachLifecycleEventAppliedOnce(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helperWithIntegratedShells(t, spawner)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	store := newMemCursorStore()

	first := newCursorKeepingCoordinator(t, provider, store)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	build := lifecycle.AttemptID("shell-build")
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &build, Command: "make build"}})
	waittest.WaitFor(t, "the first coordinator stored the start's cursor", func() bool {
		return store.stored(binding.SessionID) == 2
	})
	quitAndWaitGone(t, first)

	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{5}},
	})

	// THE FIRST RETURN applies the end the shell spoke while nobody was
	// attached, stores its cursor, and goes away again.
	second := newCursorKeepingCoordinator(t, provider, store)
	takeBack(t, second, store.bindingFor(binding))
	waittest.WaitFor(t, "the second coordinator applied the end and stored its cursor", func() bool {
		return len(second.emitter.completed()) == 1 && store.stored(binding.SessionID) == 3
	})
	quitAndWaitGone(t, second)

	// THE SECOND RETURN resumes past everything the first return applied.
	third := newCursorKeepingCoordinator(t, provider, store)
	takeBack(t, third, store.bindingFor(binding))
	runBarrier(t, shell, third, "echo after")

	if done := completedCommands(third.emitter.completed()); len(done) != 1 || done[0] != "echo after" {
		t.Fatalf("the third coordinator completed %q, want only the barrier: the end the second one applied was applied again", done)
	}
	for _, f := range third.emitter.any() {
		if f.Attempt != nil && f.Attempt.ID == string(build) {
			t.Fatalf("the third coordinator applied an event of the command two coordinators before it: %+v", f.Attempt)
		}
	}
}

// rangeLostSink is the block rows seam with the lost-range settle recorded.
type rangeLostSink struct {
	*fakeSink
	mu   sync.Mutex
	lost []session.ID
}

func (s *rangeLostSink) LifecycleRangeLost(sid session.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost = append(s.lost, sid)
}

func (s *rangeLostSink) lostRanges() []session.ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.ID(nil), s.lost...)
}

// THE HELPER NO LONGER HOLDS THE CURSOR. While nobody was attached the shell
// said more than the helper's lifecycle window keeps — the command's end
// among it — so the window's base has moved past the cursor the first
// coordinator stored. The re-adopt is answered from the base; the stretch
// between is gone, and the block it could have settled is settled from the
// helper's own statement of that loss rather than left running.
func TestACursorTheHelperNoLongerHoldsSettlesTheBlockTheLostRangeCouldHaveEnded(t *testing.T) {
	spawner := &lifecycleSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		Log:        discardLogger(t),
		// The smallest window the helper allows, so the test can out-talk it.
		Limits: helpersession.Limits{MinWindowBytes: 1, DefaultWindowBytes: 1, MaxWindowBytes: 1},
	})
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	store := newMemCursorStore()

	first := newCursorKeepingCoordinator(t, provider, store)
	binding := openHostedFixture(t, first.coordinator, "pane-1")
	shell, _ := spawner.theShell(t)
	shell.drain()
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}})
	build := lifecycle.AttemptID("shell-build")
	shell.send(t, lifecycle.Event{Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &build, Command: "make build"}})
	waittest.WaitFor(t, "the first coordinator stored the start's cursor", func() bool {
		return store.stored(binding.SessionID) == 2
	})
	quitAndWaitGone(t, first)
	cursor := *store.bindingFor(binding).LifecycleApplied

	// The command ends, and the shell goes on talking while nobody listens —
	// prompts, until the helper's window has reclaimed past the cursor.
	code := 0
	shell.send(t, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{6}},
	})
	for base := uint64(0); base <= cursor; {
		for range 64 {
			shell.send(t, lifecycle.Event{Kind: lifecycle.KindPromptReady, PromptReady: &lifecycle.PromptReady{}})
		}
		base = lifecycleBaseOf(t, svc, binding.SessionID)
	}
	inv := newCursorKeepingCoordinator(t, provider, store)

	sink := &rangeLostSink{fakeSink: &fakeSink{answer: func(uint64, int) (uint64, bool) { return 0, false }}}
	adopter := &stubAdopter{}
	t.Cleanup(adopter.endAdopted)
	rec := &recordingReconciler{pending: []content.PendingSession{store.bindingFor(binding)}}
	pass := readoptFixture(t, inv.coordinator, routesFor(binding), adopter)
	pass.blockRows = sink
	reconcileSessions(context.Background(), rec, inv.reg.inventories(), pass, time.Hour, quietLogger(t))
	if adopted := adopter.adoptedIDs(); len(adopted) != 1 {
		t.Fatalf("the session was not taken back: %v", adopter.failure())
	}
	if lost := sink.lostRanges(); len(lost) != 1 || string(lost[0]) != binding.SessionID {
		t.Fatalf("lost-range settles = %v, want the session's open block settled once: the helper said the range holding its end is gone", lost)
	}
}

// lifecycleBaseOf asks the helper, over a probe client of the test's own
// that neither attaches nor adopts, where its lifecycle window now starts.
func lifecycleBaseOf(t *testing.T, svc *helpersession.Service, sid string) uint64 {
	t.Helper()
	conn := newFakeLaneConn(sharedHelperPeer(t, svc))
	defer func() { _ = conn.Close() }()
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/scripted/helper", ExpectHash: syntheticArtifactHash, Log: discardLogger(t),
	})
	if err != nil {
		t.Fatalf("the probe client: %v", err)
	}
	defer func() { _ = c.Close() }()
	entries, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatalf("ask the helper what it holds: %v", err)
	}
	for _, e := range entries {
		if e.HostSessionID.Session == sid {
			return e.LifecycleWindow.Base
		}
	}
	t.Fatalf("the helper reports no session %s", sid)
	return 0
}
