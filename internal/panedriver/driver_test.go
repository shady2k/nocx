package panedriver_test

// The mapping a pane's state comes from (design §5.6, nocx-oalo1.5.3).
//
// These tests assert the coordinator's half of the contract: which pane is told
// what when a connection says something, the epoch each observation carries, and
// the two refusals that keep a pane's state from moving backwards.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
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
	out := &sent{}
	_, logger := logtest.New(t)
	d := panedriver.New("instance-1", logger)
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

// TestABindBeforeAnyObservationPublishesTheOpening: the pane is painted before
// any network. The bind IS the start of the open, so `opening/resolve` is what
// the coordinator knows at that moment — and the renderer must not have to
// invent the same stage to keep its first frame honest.
func TestABindBeforeAnyObservationPublishesTheOpening(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	if got := len(out.all()); got != 1 {
		t.Fatalf("%d snapshot(s) at bind, want the opening", got)
	}
	snap := out.last()
	if snap.Transport.State != proto.ConnectionOpening || snap.Transport.Stage != "resolve" {
		t.Fatalf("bind published %+v, want opening/resolve", snap.Transport)
	}
	if snap.Transport.StateMs != 0 {
		t.Fatalf("stateMs = %d at the first frame, want 0", snap.Transport.StateMs)
	}
	if snap.SessionID != "" {
		t.Fatalf("sessionId = %q before any shell exists", snap.SessionID)
	}
	if snap.ConnectionEpoch != 1 {
		t.Fatalf("epoch = %d, want 1", snap.ConnectionEpoch)
	}
	apply(d, opening(1, "resolve"))
	if got := len(out.all()); got != 2 {
		t.Fatalf("%d snapshot(s) after the first observation, want 2", got)
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
	d.AdoptSession("pane-1", "conn-a", "sess-1", "instance-1", 1)

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
	d.AdoptSession("pane-1", "conn-a", "sess-a", "instance-1", 1)
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

	d.AdoptSession("pane-1", "conn-a", "sess-a", "instance-1", 1)
	if got := len(out.all()); got != before {
		t.Fatalf("a stale adoption published %d snapshot(s)", got-before)
	}
	if snap := out.last(); snap.SessionID != "" {
		t.Fatalf("the stale adoption set sessionId %q", snap.SessionID)
	}

	d.AdoptSession("pane-1", "conn-b", "sess-b", "instance-1", 2)
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
// the opening it is holding, never the previous helper's last observation.
func TestABindAfterAHelperReplacementReplaysNothing(t *testing.T) {
	d, out := newDriver(t)
	d.ApplyFrom("helper-1", connected(4, 1))
	d.SetSource("helper-2")
	d.Bind("pane-1", "conn-a")
	if got := len(out.all()); got != 1 {
		t.Fatalf("a bind after the replacement published %d snapshot(s), want this bind's opening", got)
	}
	if snap := out.last(); snap.Transport.State != proto.ConnectionOpening || snap.Transport.Stage != "resolve" {
		t.Fatalf("bind published %+v, want this bind's opening/resolve", snap.Transport)
	}
}

// TestFailOpeningSettlesOnlyTheProvisionalBinding: the opening the transport
// publishes before the opener is asked anything must not outlive a failed
// open, and a real connection's failure is its own.
func TestFailOpeningSettlesOnlyTheProvisionalBinding(t *testing.T) {
	d, out := newDriver(t)
	generation := d.Bind("pane-1", "pending:pane-1")
	if snap := out.last(); snap.Transport.State != proto.ConnectionOpening {
		t.Fatalf("provisional bind published %+v", snap.Transport)
	}
	d.FailOpening("pane-1", generation)
	snap := out.last()
	if snap.Transport.State != proto.ConnectionEnded || snap.Transport.Cause != "lost-link" {
		t.Fatalf("settle published %+v, want ended/lost-link", snap.Transport)
	}
	if snap.Reconnect == nil || snap.Reconnect.EndsShells != 0 {
		t.Fatalf("ended opening offers %+v, want the shell-less reconnect offer", snap.Reconnect)
	}

	// A real binding is not the provisional one; its failure is the
	// supervisor's to report and this must not overwrite it.
	before := len(out.all())
	d.Bind("pane-1", "conn-a")
	d.FailOpening("pane-1", generation)
	if got := len(out.all()); got != before+1 {
		t.Fatalf("settling a real binding published %d extra notification(s)", got-before-1)
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
		{ConnectionID: "conn-a", Seq: 12, State: proto.ConnectionReconnecting, Attempt: 1, Cause: "auth-rejected"},
		{ConnectionID: "conn-a", Seq: 13, State: proto.ConnectionFailed, Cause: "lost-link"},
		{ConnectionID: "conn-a", Seq: 14, State: proto.ConnectionEnded, Cause: "host-key-changed"},
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

func TestADroppedPaneOffersTheButton(t *testing.T) {
	d, out := newDriver(t)
	apply(d, reconnecting(1, 1, "lost-link"))
	d.Bind("pane-1", "conn-a")
	if snap := out.last(); snap.Reconnect == nil {
		t.Fatal("a dropped pane has no reconnect offer")
	}
}

func TestOlderOpeningFailureCannotSettleNewerOpening(t *testing.T) {
	d, out := newDriver(t)
	older := d.Bind("pane-1", "pending:pane-1")
	newer := d.Bind("pane-1", "pending:pane-1")
	before := len(out.all())
	d.FailOpening("pane-1", older)
	if len(out.all()) != before {
		t.Fatal("stale failure settled newer opening")
	}
	d.FailOpening("pane-1", newer)
	if out.last().Transport.State != proto.ConnectionEnded {
		t.Fatal("current failure did not settle opening")
	}
}

// ── the far helper's axis on the connection ────────────────────────────────

// helperAxisAhead is what a source answers before anything has been decided.
func helperAxisAhead(connectionID string) panedriver.Helper {
	return panedriver.Helper{State: panedriver.HelperStarting, Action: panedriver.HelperActionNone}
}

// fakeHelper is the axis source a test drives: it holds one status per
// connection and reports every later write to its watchers, which is exactly
// the contract internal/farhelper.Registry offers.
type fakeHelper struct {
	mu       sync.Mutex
	axes     map[string]panedriver.Helper
	watchers map[uint64]func(string, panedriver.Helper)
	next     uint64
	asks     []string
	refresh  error
}

func newFakeHelper() *fakeHelper {
	return &fakeHelper{
		axes:     make(map[string]panedriver.Helper),
		watchers: make(map[uint64]func(string, panedriver.Helper)),
	}
}

func (f *fakeHelper) Axis(connectionID string) (panedriver.Helper, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	helper, ok := f.axes[connectionID]
	return helper, ok
}

func (f *fakeHelper) Watch(fn func(string, panedriver.Helper)) func() {
	f.mu.Lock()
	f.next++
	id := f.next
	f.watchers[id] = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.watchers, id)
		f.mu.Unlock()
	}
}

func (f *fakeHelper) Refresh(_ context.Context, connectionID string) error {
	f.mu.Lock()
	f.asks = append(f.asks, connectionID)
	err := f.refresh
	f.mu.Unlock()
	return err
}

// put stores a status and reports it, exactly as a registry publish does.
func (f *fakeHelper) put(connectionID string, helper panedriver.Helper) {
	f.mu.Lock()
	f.axes[connectionID] = helper
	watchers := make([]func(string, panedriver.Helper), 0, len(f.watchers))
	for _, watcher := range f.watchers {
		watchers = append(watchers, watcher)
	}
	f.mu.Unlock()
	for _, watcher := range watchers {
		watcher(connectionID, helper)
	}
}

func (f *fakeHelper) refreshAsks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asks...)
}

// TestAPaneIsToldTheHelperAxisTheConnectionHas: the helper member is required on
// every pane.connection, so a pane bound before anybody decided must carry the
// axis's own starting state rather than an absent object.
func TestAPaneIsToldTheHelperAxisTheConnectionHas(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	source.put("conn-a", panedriver.Helper{
		State: panedriver.HelperUnavailable, Reason: "mode-raw", Action: panedriver.HelperActionHostSettings,
	})
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")

	snap := out.last()
	if snap.Helper.State != panedriver.HelperUnavailable ||
		snap.Helper.Reason != "mode-raw" ||
		snap.Helper.Action != panedriver.HelperActionHostSettings {
		t.Fatalf("helper = %+v, want unavailable(mode-raw, host-settings)", snap.Helper)
	}
}

func TestAPaneWithNoHelperSourceStillCarriesAStartingAxis(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	snap := out.last()
	if snap.Helper != helperAxisAhead("conn-a") {
		t.Fatalf("helper = %+v, want starting/none", snap.Helper)
	}
}

// TestAnAxisChangeReachesEveryPaneOnTheConnection: the axis is the
// connection's, so one helper-lost is one observation per pane riding it and
// none for a pane on another connection. Every one of them gets its own epoch.
func TestAnAxisChangeReachesEveryPaneOnTheConnection(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")
	d.Bind("pane-2", "conn-a")
	d.Bind("pane-3", "conn-b")
	first := map[string]uint64{}
	for _, snap := range out.all() {
		first[snap.PaneID] = snap.ConnectionEpoch
	}
	before := len(out.all())

	lost := panedriver.Helper{
		State: panedriver.HelperUnavailable, Reason: "helper-lost", Action: panedriver.HelperActionRefresh,
	}
	source.put("conn-a", lost)

	fresh := out.all()[before:]
	if len(fresh) != 2 {
		t.Fatalf("%d snapshot(s) after one axis change, want two panes", len(fresh))
	}
	seen := map[string]bool{}
	for _, snap := range fresh {
		seen[snap.PaneID] = true
		if snap.Helper != lost {
			t.Fatalf("pane %s helper = %+v, want %+v", snap.PaneID, snap.Helper, lost)
		}
		if snap.ConnectionEpoch <= first[snap.PaneID] {
			t.Fatalf("pane %s epoch = %d, want newer than %d", snap.PaneID, snap.ConnectionEpoch, first[snap.PaneID])
		}
	}
	if !seen["pane-1"] || !seen["pane-2"] || seen["pane-3"] {
		t.Fatalf("axis change reached %v, want pane-1 and pane-2 only", seen)
	}
}

// TestAPaneBoundBeforeTheSourceIsToldOnceItArrives: the source is installed
// after a pane can already exist — in the composition root it is not, but the
// interface does not require the order, and a pane left at `starting` for ever
// would be a fact nobody published.
func TestAPaneBoundBeforeTheSourceIsToldOnceItArrives(t *testing.T) {
	d, out := newDriver(t)
	d.Bind("pane-1", "conn-a")
	if out.last().Helper.State != panedriver.HelperStarting {
		t.Fatalf("bind without a source = %+v, want starting", out.last().Helper)
	}
	source := newFakeHelper()
	serving := panedriver.Helper{State: panedriver.HelperServing, Action: panedriver.HelperActionNone}
	source.axes["conn-a"] = serving
	d.SetHelperSource(source)
	if got := out.last().Helper; got != serving {
		t.Fatalf("after the source arrived = %+v, want %+v", got, serving)
	}
}

// TestAnUnchangedAxisCostsNoFrame: a status that is already what the pane was
// told is not an observation.
func TestAnUnchangedAxisCostsNoFrame(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	serving := panedriver.Helper{State: panedriver.HelperServing, Action: panedriver.HelperActionNone}
	source.put("conn-a", serving)
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")
	before := len(out.all())

	source.put("conn-a", serving)
	if got := len(out.all()); got != before {
		t.Fatalf("%d snapshot(s) for an unchanged axis, want %d", got, before)
	}
}

// TestRefreshIsOfferedOnlyForHelperLost is the action's own rule
// (ADR-727g63tr decision 2): refresh redials a helper that was serving and is
// gone, and it is never offered for an absent decision.
func TestRefreshIsOfferedOnlyForHelperLost(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")

	for _, helper := range []panedriver.Helper{
		{State: panedriver.HelperServing, Action: panedriver.HelperActionNone},
		{State: panedriver.HelperUnavailable, Reason: "install-failed", Action: panedriver.HelperActionNone},
		{State: panedriver.HelperUnavailable, Reason: "mode-raw", Action: panedriver.HelperActionHostSettings},
		{State: panedriver.HelperPaused, Reason: "connection", Action: panedriver.HelperActionNone},
	} {
		source.put("conn-a", helper)
		err := d.RefreshHelper(context.Background(), "pane-1", out.last().ConnectionEpoch)
		if !errors.Is(err, panedriver.ErrNoHelperRefresh) {
			t.Fatalf("refresh on %+v = %v, want ErrNoHelperRefresh", helper, err)
		}
	}
	if got := source.refreshAsks(); len(got) != 0 {
		t.Fatalf("refusals asked the source to refresh %v", got)
	}
}

func TestRefreshRedialsTheConnectionsHelper(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")
	lost := panedriver.Helper{
		State: panedriver.HelperUnavailable, Reason: "helper-lost", Action: panedriver.HelperActionRefresh,
	}
	source.put("conn-a", lost)

	err := d.RefreshHelper(context.Background(), "pane-1", out.last().ConnectionEpoch)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := source.refreshAsks(); len(got) != 1 || got[0] != "conn-a" {
		t.Fatalf("refreshes = %v, want one for conn-a", got)
	}
}

func TestRefreshRefusesAStaleEpoch(t *testing.T) {
	d, out := newDriver(t)
	source := newFakeHelper()
	d.SetHelperSource(source)
	d.Bind("pane-1", "conn-a")
	source.put("conn-a", panedriver.Helper{
		State: panedriver.HelperUnavailable, Reason: "helper-lost", Action: panedriver.HelperActionRefresh,
	})
	stale := out.last().ConnectionEpoch
	// A second observation of the same connection moves the pane's epoch, so
	// the press names a frame the person is no longer looking at.
	source.put("conn-a", panedriver.Helper{
		State: panedriver.HelperServing, Action: panedriver.HelperActionNone,
	})
	if out.last().ConnectionEpoch == stale {
		t.Fatal("the second observation did not move the epoch")
	}
	err := d.RefreshHelper(context.Background(), "pane-1", stale)
	if !errors.Is(err, panedriver.ErrStaleEpoch) {
		t.Fatalf("stale refresh = %v, want ErrStaleEpoch", err)
	}
	if got := source.refreshAsks(); len(got) != 0 {
		t.Fatalf("a stale press asked the source to refresh %v", got)
	}
}
