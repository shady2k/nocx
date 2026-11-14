package panedriver_test

// The mapping a pane's state comes from (design §5.6, nocx-oalo1.5.3).
//
// These tests assert the coordinator's half of the contract: which pane is told
// what when a connection says something, the epoch each observation carries, and
// the two refusals that keep a pane's state from moving backwards.

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/waittest"
)

type sent struct {
	mu   sync.Mutex
	list []panedriver.Snapshot
}

func (s *sent) add(snap panedriver.Snapshot) {
	s.mu.Lock()
	s.list = append(s.list, snap)
	s.mu.Unlock()
}

func (s *sent) all() []panedriver.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]panedriver.Snapshot(nil), s.list...)
}

func (s *sent) last() panedriver.Snapshot {
	all := s.all()
	return all[len(all)-1]
}

// apply feeds one observation with the test's helper instance.
func apply(d *panedriver.Driver, ev proto.ConnectionStateEvent) { d.ApplyFrom("helper-test", ev) }

func newDriver(t *testing.T) (*panedriver.Driver, *sent) {
	t.Helper()
	return newDriverWith(t)
}

// newDriverWith is newDriver with options (a policy, a scheduler, an action).
func newDriverWith(t *testing.T, opts ...panedriver.Option) (*panedriver.Driver, *sent) {
	t.Helper()
	out := &sent{}
	d := panedriver.New("instance-1", log.NewSlogAdapter(nil), opts...)
	d.SetPublisher(out.add)
	return d, out
}

func connected(seq uint64, shells int) proto.ConnectionStateEvent {
	return proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: seq, State: proto.ConnectionConnected,
		StateMs: 1200, PlainShells: shells,
	}
}

func opening(seq uint64, stage string) proto.ConnectionStateEvent {
	return proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: seq, State: proto.ConnectionOpening,
		Stage: stage, StateMs: 0,
	}
}

// TestABindTellsThePaneTheStateItsConnectionIsAlreadyIn: a second pane on a
// host somebody else is connected to must be told `connected` at once, not at
// the next probe.
func TestABindTellsThePaneTheStateItsConnectionIsAlreadyIn(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	d.Bind("pane-1", "conn-a")

	snap := out.last()
	if snap.PaneID != "pane-1" || snap.ConnectionID != "conn-a" {
		t.Fatalf("snapshot names %q/%q, want pane-1/conn-a", snap.PaneID, snap.ConnectionID)
	}
	if snap.Transport.State != proto.ConnectionConnected {
		t.Fatalf("state = %q, want connected", snap.Transport.State)
	}
	if !snap.Transport.HasPlainShells || snap.Transport.PlainShells != 1 {
		t.Fatalf("plainShells = %+v, want 1 present", snap.Transport)
	}
	if snap.ConnectionEpoch != 1 {
		t.Fatalf("epoch = %d, want 1", snap.ConnectionEpoch)
	}
	if snap.InstanceID != "instance-1" {
		t.Fatalf("instanceId = %q", snap.InstanceID)
	}
	if snap.Attempt.Shown != "none" || snap.Attempt.Candidate != "none" {
		t.Fatalf("attempt = %+v, want the declared none", snap.Attempt)
	}
	if snap.ObservedAt.IsZero() {
		t.Fatal("observedAt is zero")
	}
}

// TestABindBeforeAnyObservationPublishesNothing: the coordinator binds the pane
// a moment before the helper's first observation arrives, and it must not
// invent a state it has not been told.
func TestABindBeforeAnyObservationPublishesNothing(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	if got := len(out.all()); got != 0 {
		t.Fatalf("%d snapshot(s) published before any observation", got)
	}
	apply(d, opening(1, "resolve"))
	if got := len(out.all()); got != 1 {
		t.Fatalf("%d snapshot(s) after the first observation, want 1", got)
	}
}

// TestTheFirstObservationOfAnOpenIsOpeningWithAStage is design §5.5's fact: the
// pane is told about the dial before there is a shell.
func TestTheFirstObservationOfAnOpenIsOpeningWithAStage(t *testing.T) {
	d, out := newDriver(t)
	apply(d, opening(1, "tcp"))
	d.Bind("pane-1", "conn-a")

	snap := out.last()
	if snap.Transport.State != proto.ConnectionOpening {
		t.Fatalf("state = %q, want opening", snap.Transport.State)
	}
	if snap.Transport.Stage != "tcp" {
		t.Fatalf("stage = %q, want tcp", snap.Transport.Stage)
	}
	if snap.SessionID != "" {
		t.Fatalf("sessionId = %q before any shell exists", snap.SessionID)
	}
}

// TestEveryPaneOnAConnectionIsToldAndEachHasItsOwnEpoch is the sharing rule:
// two panes, one connection, two independent observations.
func TestEveryPaneOnAConnectionIsToldAndEachHasItsOwnEpoch(t *testing.T) {
	d, out := newDriver(t)
	apply(d, opening(1, "resolve"))
	d.Bind("pane-1", "conn-a")
	d.Bind("pane-2", "conn-a")
	apply(d, connected(2, 2))

	all := out.all()
	if len(all) != 4 {
		t.Fatalf("%d snapshots, want 4 (2 binds + 2 panes observing one event)", len(all))
	}
	byEpoch := map[string][]uint64{}
	for _, snap := range all {
		byEpoch[snap.PaneID] = append(byEpoch[snap.PaneID], snap.ConnectionEpoch)
	}
	for _, pane := range []string{"pane-1", "pane-2"} {
		epochs := byEpoch[pane]
		if len(epochs) != 2 || epochs[0] != 1 || epochs[1] != 2 {
			t.Fatalf("%s epochs = %v, want 1,2", pane, epochs)
		}
	}
	if last := out.last(); last.Transport.State != proto.ConnectionConnected || last.Transport.PlainShells != 2 {
		t.Fatalf("last = %+v, want connected with 2 plain shells", last.Transport)
	}
}

// TestALateObservationIsRefused: a sequence that is not newer describes an
// older moment, whatever order it arrived in.
func TestALateObservationIsRefused(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	apply(d, connected(2, 1))
	before := len(out.all())
	apply(d, opening(1, "tcp"))
	if got := len(out.all()); got != before {
		t.Fatalf("a late observation produced %d snapshot(s)", got-before)
	}
}

// TestAnObservationForAnotherConnectionDoesNotMoveThePane: the pane rides one
// connection, and a fact about another is not about it.
func TestAnObservationForAnotherConnectionDoesNotMoveThePane(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	before := len(out.all())
	apply(d, proto.ConnectionStateEvent{
		ConnectionID: "conn-b", Seq: 1, State: proto.ConnectionConnected, PlainShells: 1,
	})
	if got := len(out.all()); got != before {
		t.Fatalf("an observation of another connection produced %d snapshot(s)", got-before)
	}
}

// TestAdoptSessionNamesTheSessionItBecame: the session id exists once, when a
// shell is adopted, and travels on the same notification.
func TestAdoptSessionNamesTheSessionItBecame(t *testing.T) {
	d, out := newDriver(t)
	apply(d, opening(1, "shell"))
	d.Bind("pane-1", "conn-a")
	d.AdoptSession("pane-1", "conn-a", "sess-1", "instance-1")

	last := out.last()
	if last.SessionID != "sess-1" {
		t.Fatalf("sessionId = %q, want sess-1", last.SessionID)
	}
	if last.ConnectionEpoch != 2 {
		t.Fatalf("epoch = %d, want 2: an adoption is an observation", last.ConnectionEpoch)
	}
	if last.Transport.State != proto.ConnectionOpening {
		t.Fatalf("state = %q: adoption is not a state change", last.Transport.State)
	}
}

// TestRebindingClearsThePreviousConnectionsSession is finding 2 of the 5.3
// review: a pane moved to another connection must not publish the old
// connection's shell as if it were the new one's.
func TestRebindingClearsThePreviousConnectionsSession(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	d.Bind("pane-1", "conn-a")
	d.AdoptSession("pane-1", "conn-a", "sess-a", "instance-1")
	if snap := out.last(); snap.SessionID != "sess-a" {
		t.Fatalf("sessionId = %q, want sess-a", snap.SessionID)
	}

	apply(d, proto.ConnectionStateEvent{ConnectionID: "conn-b", Seq: 1, State: proto.ConnectionConnected, PlainShells: 1})
	d.Bind("pane-1", "conn-b")
	snap := out.last()
	if snap.ConnectionID != "conn-b" {
		t.Fatalf("connection = %q, want conn-b", snap.ConnectionID)
	}
	if snap.SessionID != "" {
		t.Fatalf("the rebind carried the previous connection's session %q", snap.SessionID)
	}
}

// TestADelayedAdoptionFromTheOldConnectionIsRefused is the same defect's second
// half: an adoption that completes after the pane moved on belongs to the
// connection it named, not to the pane it used to be.
func TestADelayedAdoptionFromTheOldConnectionIsRefused(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	d.Bind("pane-1", "conn-a")
	apply(d, proto.ConnectionStateEvent{ConnectionID: "conn-b", Seq: 1, State: proto.ConnectionConnected, PlainShells: 1})
	d.Bind("pane-1", "conn-b")
	before := len(out.all())

	d.AdoptSession("pane-1", "conn-a", "sess-a", "instance-1")
	if got := len(out.all()); got != before {
		t.Fatalf("a stale adoption published %d snapshot(s)", got-before)
	}
	if snap := out.last(); snap.SessionID != "" {
		t.Fatalf("the stale adoption set sessionId %q", snap.SessionID)
	}

	d.AdoptSession("pane-1", "conn-b", "sess-b", "instance-1")
	if snap := out.last(); snap.SessionID != "sess-b" {
		t.Fatalf("the current connection's adoption set sessionId %q", snap.SessionID)
	}
}

// TestAReplacedHelperStartsItsOwnSequences is finding 3 of the 5.3 review: a
// connection id is a destination digest and outlives a helper process, while the
// sequence in each observation belongs to one supervisor's lifetime. A
// replacement's observations must be accepted, not discarded until they pass the
// old helper's last number, and the old helper's late observations must be
// refused.
func TestAReplacedHelperStartsItsOwnSequences(t *testing.T) {
	d, out := newDriver(t)
	d.ApplyFrom("helper-1", connected(100, 1))
	d.Bind("pane-1", "conn-a")
	if snap := out.last(); snap.Transport.State != proto.ConnectionConnected {
		t.Fatalf("state = %q, want connected", snap.Transport.State)
	}

	d.SetSource("helper-2")
	// The old helper's late observation is refused: its sequence names a
	// supervisor that no longer exists.
	before := len(out.all())
	d.ApplyFrom("helper-1", opening(101, "tcp"))
	if got := len(out.all()); got != before {
		t.Fatalf("an observation from the replaced helper published %d snapshot(s)", got-before)
	}
	// The replacement's first observation is accepted even though it is lower
	// than the old helper's last.
	d.ApplyFrom("helper-2", opening(1, "tcp"))
	if got := len(out.all()); got != before+1 {
		t.Fatalf("the replacement's first observation was refused")
	}
	if snap := out.last(); snap.Transport.State != proto.ConnectionOpening || snap.Transport.Stage != "tcp" {
		t.Fatalf("state = %+v, want opening(tcp)", snap.Transport)
	}
}

// TestABindAfterAHelperReplacementReplaysNothing: the cached state belonged to a
// helper that is gone, so a pane binding before the replacement speaks is told
// nothing rather than the previous helper's last observation.
func TestABindAfterAHelperReplacementReplaysNothing(t *testing.T) {
	d, out := newDriver(t)
	d.ApplyFrom("helper-1", connected(4, 1))
	d.SetSource("helper-2")
	d.Bind("pane-1", "conn-a")
	if got := len(out.all()); got != 0 {
		t.Fatalf("a bind after the replacement replayed %d cached snapshot(s)", got)
	}
}

// TestUnbindStopsThePaneBeingTold is finding 4's other half: with no binding,
// an observation of a shared connection has nobody to be about.
func TestUnbindStopsThePaneBeingTold(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	d.Bind("pane-1", "conn-a")
	before := len(out.all())
	d.Unbind("pane-1")
	apply(d, opening(2, "tcp"))
	if got := len(out.all()); got != before {
		t.Fatalf("an unbound pane received %d snapshot(s)", got-before)
	}
	// A rebind gets the cached state, which is the same observation a fresh
	// pane on a live connection is owed.
	d.Bind("pane-1", "conn-a")
	if snap := out.last(); snap.Transport.State != proto.ConnectionOpening {
		t.Fatalf("state = %q, want opening", snap.Transport.State)
	}
}

// TestTheStateCarriesTheMeasurements: the stall's look answer and the round
// trip's grade reach the pane with their own presence rules.
func TestTheStateCarriesTheMeasurements(t *testing.T) {
	d, out := newDriver(t)
	at := time.Now().Add(-20 * time.Second).UTC().Truncate(time.Second)
	apply(d, proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: 1, State: proto.ConnectionStalled,
		StateMs: 45000, PlainShells: 1,
		LookAnsweredAt: at.Format(time.RFC3339),
		RoundTripMs:    600, Slow: true,
	})
	d.Bind("pane-1", "conn-a")

	snap := out.last()
	if snap.Transport.StateMs != 45000 {
		t.Fatalf("stateMs = %d, want 45000", snap.Transport.StateMs)
	}
	if !snap.Transport.HasLook || !snap.Transport.LookAnsweredAt.Equal(at) {
		t.Fatalf("lookAnsweredAt = %v (present %v), want %v", snap.Transport.LookAnsweredAt, snap.Transport.HasLook, at)
	}
	if snap.Quality == nil || snap.Quality.RoundTripMs != 600 || !snap.Quality.Slow {
		t.Fatalf("quality = %+v, want 600ms slow", snap.Quality)
	}
}

// TestAStateWithoutItsCompanionIsRefused: the helper's event is fenced on what
// a state makes true, and a malformed one is dropped rather than drawn.
func TestAStateWithoutItsCompanionIsRefused(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	before := len(out.all())
	for _, ev := range []proto.ConnectionStateEvent{
		{ConnectionID: "conn-a", Seq: 5, State: proto.ConnectionOpening},
		{ConnectionID: "conn-a", Seq: 6, State: proto.ConnectionFailed},
		{ConnectionID: "conn-a", Seq: 7, State: "plain"},
		// Finding 6: a field the state does not own is refused, not copied.
		{ConnectionID: "conn-a", Seq: 8, State: proto.ConnectionConnected, Stage: "tcp"},
		{ConnectionID: "conn-a", Seq: 9, State: proto.ConnectionConnected, Cause: "released"},
		{ConnectionID: "conn-a", Seq: 10, State: proto.ConnectionWaitingPerson, Cause: "auth-rejected"},
		{ConnectionID: "conn-a", Seq: 11, State: proto.ConnectionReconnecting, Attempt: 1},
	} {
		apply(d, ev)
	}
	if got := len(out.all()); got != before {
		t.Fatalf("malformed events produced %d snapshot(s)", got-before)
	}
	// And a valid observation after the refusals still lands, so a bad frame
	// does not wedge the connection.
	apply(d, connected(8, 0))
	waittest.WaitFor(t, "the later observation", func() bool {
		return out.last().Transport.State == proto.ConnectionConnected
	})
}

// ── the reconnect offer and the policy (nocx-oalo1.5.4) ──────────────────

func stalled(seq uint64, shells int) proto.ConnectionStateEvent {
	at := time.Now().Add(-45 * time.Second).UTC().Format(time.RFC3339)
	return proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: seq, State: proto.ConnectionStalled,
		StateMs: 45000, PlainShells: shells, LookAnsweredAt: at,
	}
}

func reconnecting(seq uint64, attempt int, cause string) proto.ConnectionStateEvent {
	return proto.ConnectionStateEvent{
		ConnectionID: "conn-a", Seq: seq, State: proto.ConnectionReconnecting,
		Attempt: attempt, Cause: cause, StateMs: 100,
	}
}

// TestAHealthyPaneIsOfferedNothing is the ordinary case: a connected pane has
// no button, because there is nothing to reconnect.
func TestAHealthyPaneIsOfferedNothing(t *testing.T) {
	d, out := newDriver(t)
	apply(d, connected(1, 1))
	d.Bind("pane-1", "conn-a")
	if snap := out.last(); snap.Reconnect != nil {
		t.Fatalf("a connected pane offers %+v", snap.Reconnect)
	}
}

// TestAStalledPlainShellOffersAReconnectNamingItsShells is Q3's other half and
// design 5.4: the flow stopped answering, closing it would end the person's
// work, so the person decides — and is told how many shells that is.
func TestAStalledPlainShellOffersAReconnectNamingItsShells(t *testing.T) {
	d, out := newDriver(t)
	apply(d, stalled(1, 1))
	d.Bind("pane-1", "conn-a")
	snap := out.last()
	if snap.Reconnect == nil {
		t.Fatal("a stalled plain-shell connection offers no reconnect")
	}
	if snap.Reconnect.EndsShells != 1 {
		t.Fatalf("endsShells = %d, want 1", snap.Reconnect.EndsShells)
	}
}

// TestStalledWithNoPlainShellOffersNothing: a helper-owned connection is the
// supervisor's to put back; there is no work of the person's to end.
func TestStalledWithNoPlainShellOffersNothing(t *testing.T) {
	d, out := newDriver(t)
	apply(d, stalled(1, 0))
	d.Bind("pane-1", "conn-a")
	if snap := out.last(); snap.Reconnect != nil {
		t.Fatalf("a stalled connection with no shell offers %+v", snap.Reconnect)
	}
}

// TestADroppedPaneOffersTheButtonUnderEveryPolicy: ssh.reconnect decides what
// the BACKEND does by itself, not whether a person may choose (design 5.4).
func TestADroppedPaneOffersTheButtonUnderEveryPolicy(t *testing.T) {
	for _, policy := range []string{panedriver.PolicyAuto, panedriver.PolicyAsk, panedriver.PolicyNever} {
		t.Run(policy, func(t *testing.T) {
			d, out := newDriverWith(t, panedriver.WithPolicy(func() string { return policy }))
			apply(d, reconnecting(1, 1, "lost-link"))
			d.Bind("pane-1", "conn-a")
			if snap := out.last(); snap.Reconnect == nil {
				t.Fatalf("policy %q leaves a dropped pane with no button", policy)
			}
		})
	}
}
