package transport

// The emitting view, off the real socket (AGENTS.md testing rule 5) and
// through the production wiring: a real panegrid Store fed real bytes, the
// real watcher, the real shipped claude rule, and the frame a renderer would
// receive validated against the contract.
//
// What a person gets that they could not before: they open Settings, pick the
// pane their agent is running in, and see the screen the rule is reading
// together with the rule's own reading of it — which anchor bound where, which
// branch answered, and for a branch that did not, the predicate it stopped at.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
)

// claudeIdleScreen is the chrome the shipped rule reads: a token meter, two
// full-width rules around a row that opens with the input marker at column
// zero exactly, and a mode line under it. Painted through a real emulator by
// the Store, so the frame under test is one the product produces.
func claudeIdleScreen(cols int) string {
	rule := strings.Repeat("─", cols)
	return "\x1b[2J\x1b[7;1H              0 tokens" +
		"\x1b[8;1H" + rule + "\x1b[9;1H❯ \x1b[10;1H" + rule +
		"\x1b[12;1H  ⏵⏵ auto mode on\x1b[9;3H"
}

type emittingEnv struct {
	env  *lifecycleTestEnv
	grid *panegrid.Store
	sid  string
}

func newEmittingEnv(t *testing.T) *emittingEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	grid := panegrid.New(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	watcher := paneobserve.New(logger, grid, rules)
	env := newLifecycleTestEnv(t,
		WithPaneGrid(grid), WithPaneObserver(watcher), WithAgentRules(rules))
	watcher.SetEmitter(env.ws.EmitPaneObservation)
	sid := env.openSession(t, 1)

	// The enrolment act, as the pane enroller performs it at the composition
	// root: the grid first, then the observation beside it.
	if err := grid.Enrol(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw(sid) })
	watcher.Watch(sid, "claude")
	grid.Feed(sid, []byte(claudeIdleScreen(40)))
	return &emittingEnv{env: env, grid: grid, sid: sid}
}

func (e *emittingEnv) call(t *testing.T, params any, id int) agentEmittingResult {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, "agent.emitting", params, id)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("agent.emitting error: %+v", env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.emitting.schema.json"), env.Result, "agent.emitting wire")
	var out agentEmittingResult
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// TestAgentEmitting_OverTheWireConformsToContract is the acceptance check: a
// real result off a real socket, validated against the schema, carrying BOTH
// halves — the frame and the rule's reading of it. A payload the test built
// itself would prove the struct is well-formed, not that the server sends it.
func TestAgentEmitting_OverTheWireConformsToContract(t *testing.T) {
	e := newEmittingEnv(t)
	got := e.call(t, map[string]any{"sessionId": e.sid}, 2)

	if len(got.Panes) != 1 || got.Panes[0].SessionID != e.sid || got.Panes[0].Agent != "claude" {
		t.Fatalf("panes = %+v, want the one enrolled pane named as claude", got.Panes)
	}
	if got.Reading == nil {
		t.Fatal("no reading for the pane that was named and is enrolled")
	}
	r := got.Reading
	if !r.HasRule {
		t.Fatal("hasRule is false for claude, which ships a rule")
	}
	if r.State != string(agentdriver.StateFreeText) {
		t.Fatalf("state = %q, want free_text for an idle input box", r.State)
	}
	// THE OTHER HALF, and the one the acceptance criterion is falsified by:
	// raw text with no reading is a terminal, and the person already has one.
	if len(r.Anchors) == 0 || len(r.Branches) == 0 {
		t.Fatalf("reading carries %d anchors and %d branches; a screen with no reading is a screen",
			len(r.Anchors), len(r.Branches))
	}
	// The rule's own arithmetic, on the screen: the input box was found by
	// two full-width rules, and the reading says which rows they are.
	rules := 0
	for _, line := range r.Frame.Lines {
		if line.Rule != "" {
			rules++
		}
	}
	if rules != 2 {
		t.Fatalf("%d rows reported as full-width rules, want the box's two", rules)
	}
	// Column geometry survives the crossing, which is what both of the
	// amendment's positional powers rest on (ADR-0041).
	if r.Frame.Cols != 40 || len(r.Frame.Lines) != r.Frame.Rows {
		t.Fatalf("frame = %dx%d with %d lines", r.Frame.Cols, r.Frame.Rows, len(r.Frame.Lines))
	}
	for y, line := range r.Frame.Lines {
		if len(line.Cells) != r.Frame.Cols {
			t.Fatalf("row %d carries %d cells for %d columns", y, len(line.Cells), r.Frame.Cols)
		}
	}
	if r.SessionID != e.sid || r.InstanceID == "" || r.SessionEpoch == 0 {
		t.Fatalf("reading identity = %+v; a reading with no incarnation can be drawn over the wrong pane", r)
	}
}

// TestAgentEmittingNamesTheBranchAndWhereEachStopped is the acceptance
// criterion's own sentence, over the wire: the value AND the branch that
// produced it, and for a branch that did not match, the predicate it stopped
// at. An idle claude screen falls through every branch to the default, so
// every branch here reports where it stopped.
func TestAgentEmittingNamesTheBranchAndWhereEachStopped(t *testing.T) {
	e := newEmittingEnv(t)
	r := e.call(t, map[string]any{"sessionId": e.sid}, 2).Reading
	if r == nil {
		t.Fatal("no reading")
	}
	if r.MatchedBranch != nil {
		t.Fatalf("matchedBranch = %d on a screen that falls through to the default", *r.MatchedBranch)
	}
	if r.Fallback != string(agentdriver.StateFreeText) {
		t.Fatalf("fallback = %q, want the document's own default", r.Fallback)
	}
	stopped := false
	for _, b := range r.Branches {
		if b.Matched {
			t.Fatalf("a branch is reported matched while matchedBranch is absent: %+v", b)
		}
		if !b.Reached {
			t.Fatalf("a branch is reported unreached while nothing matched: %+v", b)
		}
		for i, p := range b.Predicates {
			if p.Evaluated && !p.Held {
				stopped = true
				// Everything after the failure was never asked, and saying
				// otherwise points a person at the wrong line.
				for _, after := range b.Predicates[i+1:] {
					if after.Evaluated {
						t.Fatalf("branch %+v reports a predicate evaluated after it stopped", b)
					}
				}
				break
			}
		}
	}
	if !stopped {
		t.Fatal("no branch reports the predicate it stopped at, which is what an unknown has to say")
	}
}

// TestAgentEmittingWithoutASessionListsThePanes: the surface asks before a
// person has picked anything, and the list is what it draws.
func TestAgentEmittingWithoutASessionListsThePanes(t *testing.T) {
	e := newEmittingEnv(t)
	got := e.call(t, map[string]any{}, 2)
	if len(got.Panes) != 1 || got.Panes[0].SessionID != e.sid {
		t.Fatalf("panes = %+v, want the enrolled pane", got.Panes)
	}
	if got.Reading != nil {
		t.Fatal("a reading was produced for a request that named no pane")
	}
}

// TestAgentEmittingForAnUnwatchedPaneIsNotAnError. The observation closes when
// its session ends, and a surface polling through that moment finds out one
// answer later. Answering with an error there would make the ordinary end of
// an interval look like a caller's mistake — and it is the ordinary end that
// this view has to survive, since a person watches a pane until the agent in
// it is done.
func TestAgentEmittingForAnUnwatchedPaneIsNotAnError(t *testing.T) {
	e := newEmittingEnv(t)
	got := e.call(t, map[string]any{"sessionId": "no-such-pane"}, 2)
	if got.Reading != nil {
		t.Fatal("a reading was produced for a pane nobody is watching")
	}
	if len(got.Panes) != 1 {
		t.Fatalf("panes = %+v; the list still says what IS watched", got.Panes)
	}
}

// TestAgentEmittingCreatesNoEnrolment is the AD-6 containment, asserted rather
// than argued: asking about a pane is not a way to start watching one.
// Enrolment is an ACT on the authenticated channel, and if this method could
// produce one it would be a route around that.
func TestAgentEmittingCreatesNoEnrolment(t *testing.T) {
	e := newEmittingEnv(t)
	other := e.env.openSession(t, 2)
	before := e.grid.Count()
	got := e.call(t, map[string]any{"sessionId": other}, 3)
	if got.Reading != nil {
		t.Fatal("a session that was never enrolled produced a reading")
	}
	if e.grid.Count() != before {
		t.Fatalf("grids = %d after asking about an unenrolled pane, was %d", e.grid.Count(), before)
	}
	if e.grid.Enrolled(other) {
		t.Fatal("asking about a pane enrolled it, which is the inference the amendment forbids")
	}
}

// TestAgentEmittingDoesNotDecideAnything: the reading is a read-out, and the
// two powers the amendment grants are unmoved by it. The pane's classification
// is what the indicator shows and what gates typing, and it is identical
// either side of a read.
func TestAgentEmittingDoesNotDecideAnything(t *testing.T) {
	e := newEmittingEnv(t)
	first := e.call(t, map[string]any{"sessionId": e.sid}, 2).Reading
	second := e.call(t, map[string]any{"sessionId": e.sid}, 3).Reading
	if first == nil || second == nil {
		t.Fatal("no reading")
	}
	if first.State != second.State {
		t.Fatalf("state moved from %q to %q across a read", first.State, second.State)
	}
}

// TestAgentEmittingIsNotAvailableUnwired. The half of the view the design says
// may not be missing is the rule's reading, so a build with a grid and no
// rules answers "the method is not here" rather than a screen with nothing
// beside it — a person shown no anchors and no branches would go and repair a
// document that is fine.
func TestAgentEmittingIsNotAvailableUnwired(t *testing.T) {
	env := newLifecycleTestEnv(t)
	resp := jsonrpcCallWithID(t, env.conn, "agent.emitting", map[string]any{}, 1)
	var got rpcEnvelope
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32601 {
		t.Fatalf("unwired agent.emitting answered %+v, want method not found", got)
	}
}
