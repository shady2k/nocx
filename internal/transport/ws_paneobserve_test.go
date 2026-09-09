package transport

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

func newObservedWS(t *testing.T) (*WSServer, *panegrid.Store, *paneobserve.Watcher, *feedablePTY) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	store := panegrid.New(logger)
	drivers, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	watch := paneobserve.New(logger, store, drivers)
	ws := NewWSServer(logger, reg, WithPaneGrid(store), WithPaneObserver(watch))
	watch.SetEmitter(ws.EmitPaneObservation)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx); _ = term.Close() })
	return ws, store, watch, term
}

// An idle Claude input box, in the chrome the driver reads: a token meter, the
// two full-width rules that bound the box, the input marker between them, and
// the mode line under it.
func claudeIdleChrome(cols int) string {
	rule := strings.Repeat("─", cols)
	return "\x1b[2J\x1b[7;1H  0 tokens\x1b[8;1H" + rule +
		"\x1b[9;1H❯ \x1b[10;1H" + rule + "\x1b[12;1H  ⏵⏵ auto mode on\x1b[9;3H"
}

// The struct marshals to something the schema accepts.
func TestSessionObservationChangedDTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.observationChanged.schema.json")
	raw, err := json.Marshal(observationChangedParams{
		SessionID:    "sess-1",
		InstanceID:   "inst-1",
		SessionEpoch: 1,
		Agent:        "claude",
		State:        string(agentdriver.StateFreeText),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	validateJSON(t, schema, raw, "session.observationChanged DTO")
}

// And THE REAL NOTIFICATION, off the real socket. A test that validates a
// payload the test itself built proves the struct is well-formed, never that
// the server sends it: session.observationChanged is server-initiated, so
// nothing at a call site would ever notice if it were not.
//
// Note what the test waits on: the notification. Not the sweep interval, and
// not a duration.
func TestSessionObservationChangedOverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "session.observationChanged.schema.json")
	ws, store, watch, term := newObservedWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	if err := store.Enrol(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")
	term.emit(t, claudeIdleChrome(40))

	raw := readNotification(t, conn, "session.observationChanged", wantWithin)
	validateJSON(t, schema, raw, "session.observationChanged params (real socket, idle agent pane)")

	var got observationChangedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", got.SessionID, sid)
	}
	if got.Agent != "claude" {
		t.Errorf("agent = %q, want claude", got.Agent)
	}
	if got.State != string(agentdriver.StateFreeText) {
		t.Errorf("state = %q, want %q", got.State, agentdriver.StateFreeText)
	}
	// Bound to the incarnation (AD-7), so a late observation from a previous
	// one cannot overwrite a current one.
	if got.InstanceID == "" || got.SessionEpoch == 0 {
		t.Errorf("observation carries no identity: %+v", got)
	}
}

// The task panel, drawn where claude draws it: under the mode line, the pane's
// own row first and one row per child below it.
func claudeSubagentChrome(cols int) string {
	return claudeIdleChrome(cols) +
		"\x1b[14;1H  ● main" +
		"\x1b[15;1H  ◯ Explore  List files in directory" +
		"\x1b[9;3H"
}

// THE HAPPY PATH, off the real socket: a person watching a pane whose agent
// spawned a child is sent that child's name and what it is doing.
//
// It is a separate test from the idle one above rather than an extra assertion
// in it, because the interesting half is a payload shape the idle case never
// produces — and validating a payload the test itself built proves the struct
// is well-formed, never that the server sends it.
//
// Nothing here waits on a duration: it reads notifications until one carries
// children, and the read itself is what has the deadline.
func TestSessionObservationChangedCarriesTheChildRowsOverTheWire(t *testing.T) {
	schema := loadSchema(t, "session.observationChanged.schema.json")
	ws, store, watch, term := newObservedWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)

	if err := store.Enrol(sid, 60, 18); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")
	term.emit(t, claudeSubagentChrome(60))

	// The screen is fed in one write, but the session read path may split it,
	// so a sweep can land on a half-painted panel and report the pane before
	// the panel is whole. Every observation that arrives is validated; the one
	// being waited for is the one that names a child, and the wait ends on
	// THAT ARRIVING under a single deadline — never on a duration elapsing.
	var got observationChangedParams
	deadline := time.Now().Add(wantWithin)
	for len(got.Children) == 0 {
		msg, err := awaitFrame(conn, deadline, isNotification("session.observationChanged"))
		if err != nil {
			t.Fatalf("the panel was on screen and no observation ever named a child (last: %+v): %v", got, err)
		}
		f, ok := decodeFrame(msg)
		if !ok {
			t.Fatalf("undecodable notification: %s", msg)
		}
		validateJSON(t, schema, f.Params, "session.observationChanged params (real socket, task panel drawn)")
		got = observationChangedParams{}
		if err := json.Unmarshal(f.Params, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	if len(got.Children) != 1 {
		t.Fatalf("children = %+v, want exactly the one child the panel drew", got.Children)
	}
	if got.Children[0].Name != "Explore" {
		t.Errorf("child name = %q, want %q", got.Children[0].Name, "Explore")
	}
	if got.Children[0].Task != "List files in directory" {
		t.Errorf("child task = %q, want %q", got.Children[0].Task, "List files in directory")
	}
	// The pane's OWN row heads that panel and is not a child of itself.
	for _, c := range got.Children {
		if c.Name == "main" {
			t.Errorf("the pane's own row crossed as a child: %+v", got.Children)
		}
	}
	// And the parent's own state is decided by its chrome, not by the row:
	// a backgrounded agent keeps the input box live and shows no spinner, so
	// this is exactly the frame a rule without the panel calls idle.
	if got.State != string(agentdriver.StateWorking) {
		t.Errorf("state = %q, want %q", got.State, agentdriver.StateWorking)
	}
}

// A pane with no children carries NO children field at all, rather than an
// empty array. The two are different claims — "the panel is not on screen"
// against "the panel is on screen and names nobody" — and the schema refuses
// the second, so this is the assertion that keeps `omitempty` from being
// quietly dropped as tidiness.
func TestAPaneWithNoChildrenOmitsTheFieldEntirely(t *testing.T) {
	raw, err := json.Marshal(observationChangedParams{
		SessionID:    "sess-1",
		InstanceID:   "inst-1",
		SessionEpoch: 1,
		Agent:        "claude",
		State:        string(agentdriver.StateFreeText),
		Children:     observationChildren(nil),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "children") {
		t.Errorf("a pane with no children said so on the wire: %s", raw)
	}
	validateJSON(t, loadSchema(t, "session.observationChanged.schema.json"), raw, "session.observationChanged DTO, no children")
}

// The DTO carrying children conforms too, including a child with no task — a
// row the screen has drawn but not yet described. Absent, never empty.
func TestSessionObservationChangedChildrenDTOConformsToContract(t *testing.T) {
	raw, err := json.Marshal(observationChangedParams{
		SessionID:    "sess-1",
		InstanceID:   "inst-1",
		SessionEpoch: 1,
		Agent:        "claude",
		State:        string(agentdriver.StateWorking),
		Children: observationChildren([]agentdriver.Subagent{
			{Name: "Explore", Task: "List files in directory"},
			{Name: "Plan"},
		}),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"task":""`) {
		t.Errorf("a child with no task claimed an empty one: %s", raw)
	}
	validateJSON(t, loadSchema(t, "session.observationChanged.schema.json"), raw, "session.observationChanged DTO with children")
}

// A pane that is enrolled but NOT watched produces nothing. The control for
// the test above: without it a green run could mean "the classification
// crossed" or "this socket says something whenever bytes move".
func TestAnUnwatchedPaneSendsNoObservation(t *testing.T) {
	ws, store, watch, term := newObservedWS(t)
	conn := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, conn, 1)
	if err := store.Enrol(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	term.emit(t, claudeIdleChrome(40))

	// The grid proves the bytes really arrived, so the silence below is the
	// observation's and not the pane's.
	waittest.WaitForTimeout(t, "the chrome to reach the grid", wantWithin, func() bool {
		f, err := store.Frame(sid)
		return err == nil && strings.HasPrefix(strings.TrimLeft(f.Text(8), " "), "❯")
	})
	// Driven explicitly, so the silence is a fact about the sweep that ran
	// with the chrome on screen rather than about how long the test waited:
	// Sweep emits inline, so if it emitted nothing here it never will.
	watch.Sweep()
	if raw := tryReadNotification(t, conn, "session.observationChanged", 300*time.Millisecond); raw != nil {
		t.Fatalf("an unwatched pane reported %s", raw)
	}
}

// A state is not an event, and this is the end of that invariant.
//
// Only CHANGES are pushed, so a renderer that attaches to a pane which settled
// before it connected would otherwise wait forever for a transition that is
// never coming — and an indicator showing nothing for a pane nocx is actively
// watching is exactly the soft degrade the UI contradicts.
func TestAReattachingClientIsToldWhatThePaneAlreadyIs(t *testing.T) {
	ws, store, watch, term := newObservedWS(t)
	connA := connectWS(t, ws)
	sid := openSessionOnConn(t, ws, connA, 1)
	if err := store.Enrol(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	watch.Watch(sid, "claude")
	term.emit(t, claudeIdleChrome(40))
	readNotification(t, connA, "session.observationChanged", wantWithin)

	// The pane does not move again. Everything the second client learns has
	// to come from the replay.
	_ = connA.Close()
	connB := connectWS(t, ws)
	if resp := jsonrpcCallWithID(t, connB, "attach", map[string]any{"sessionId": sid, "offset": 0}, 2); resp == nil {
		t.Fatal("attach returned no result")
	}
	raw := readNotification(t, connB, "session.observationChanged", wantWithin)
	var got observationChangedParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.State != string(agentdriver.StateFreeText) || got.Agent != "claude" {
		t.Errorf("replayed observation = %+v, want claude/free_text", got)
	}
}

// countingObserver records how many sweeps it was asked for.
type countingObserver struct {
	mu     sync.Mutex
	sweeps int
}

func (c *countingObserver) Touch(string)   {}
func (c *countingObserver) Unwatch(string) {}
func (c *countingObserver) Sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweeps++
}

func (c *countingObserver) Snapshot(string) (paneobserve.Observation, bool) {
	return paneobserve.Observation{}, false
}

func (c *countingObserver) Watching() []paneobserve.Enrolled { return nil }

func (c *countingObserver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sweeps
}

// THE COALESCER'S INTERVAL HAS BOTH ENDS, and the second one is Stop.
//
// Its first version ended only on the context passed to Start. That is not the
// server's lifetime: in this package's own tests it is the background context,
// so every server that wired an observer left a ticker firing every 120ms for
// the remainder of the run. This package's 30-second timeouts already move
// between test names under constrained scheduling (nocx-2h08), so a leaked
// periodic goroutine here is not a tidiness question.
//
// Stop WAITS for it, so this asserts on the goroutine having returned rather
// than on a duration: if the sweep were still running, Stop would not return.
func TestStopEndsThePaneObservationSweep(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	term := newFeedablePTY()
	reg := session.New(logger, &feedableFactory{p: term})
	obs := &countingObserver{}
	ws := NewWSServer(logger, reg, WithPaneObserver(obs))
	// Owner: this test; closing event: the Stop below. Deliberately the
	// background context — that it is NOT the server's lifetime is the whole
	// point here.
	if err := ws.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })

	waittest.WaitForTimeout(t, "the coalescer to run at least once", wantWithin, func() bool {
		return obs.count() > 0
	})

	// The assertion is that this RETURNS. Stop waits for the coalescer, so a
	// sweep loop that ignored its second end would block here until the test
	// binary's own timeout — which is how this test fails if the interval
	// loses its close, and it is why nothing here waits on a duration.
	if err := ws.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
