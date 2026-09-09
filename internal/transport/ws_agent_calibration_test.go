package transport

// The guided calibration, off the real socket (AGENTS.md testing rule 5) and
// through the production wiring: a real panegrid Store fed real bytes, the
// real watcher, the real calibration writing a real set into a directory the
// test owns.
//
// What a person gets that they could not before: they open Settings, pick the
// pane their agent is running in, are asked for one named state at a time, and
// end with a labelled set on disk that a rule can be verified against.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
)

type calibrationEnv struct {
	env   *lifecycleTestEnv
	grid  *panegrid.Store
	store agentcalib.Store
	sid   string
}

func newCalibrationEnv(t *testing.T) *calibrationEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	grid := panegrid.New(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	watcher := paneobserve.New(logger, grid, rules)
	store, err := agentcalib.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	env := newLifecycleTestEnv(t,
		WithPaneGrid(grid), WithPaneObserver(watcher), WithAgentRules(rules),
		WithAgentCalibration(agentcalib.New(logger, grid, store, rules)))
	watcher.SetEmitter(env.ws.EmitPaneObservation)
	sid := env.openSession(t, 1)
	if err := grid.Enrol(sid, 40, 14); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw(sid) })
	watcher.Watch(sid, "claude")
	grid.Feed(sid, []byte(claudeIdleScreen(40)))
	return &calibrationEnv{env: env, grid: grid, store: store, sid: sid}
}

func (e *calibrationEnv) call(t *testing.T, method string, params any, id int) agentCalibrationResult {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, method, params, id)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("%s error: %+v", method, env.Error)
	}
	validateJSON(t, loadSchema(t, "agent.calibration.schema.json"), env.Result, method+" wire")
	var out agentCalibrationResult
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

func (e *calibrationEnv) callErr(t *testing.T, method string, params any, id int) *jsonrpcErrorObj {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, method, params, id)
	var env rpcEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env.Error
}

// TestAgentCalibration_OverTheWireConformsToContract walks a whole calibration
// through the socket and ends with a labelled set on disk — the bead's
// acceptance criterion, watched end to end rather than asserted about a
// payload the test built itself.
func TestAgentCalibration_OverTheWireConformsToContract(t *testing.T) {
	e := newCalibrationEnv(t)
	id := 2

	got := e.call(t, "agent.calibration", map[string]any{"sessionId": e.sid}, id)
	id++
	if len(got.Panes) != 1 || got.Panes[0].SessionID != e.sid || got.Panes[0].Agent != "claude" {
		t.Fatalf("panes = %+v, want the one enrolled pane named as claude", got.Panes)
	}
	if got.Calibration == nil {
		t.Fatal("no calibration for the pane that was named and is enrolled")
	}
	if len(got.Calibration.Steps) != len(agentcalib.Steps()) {
		t.Fatalf("%d steps on the wire, want the closed list's %d",
			len(got.Calibration.Steps), len(agentcalib.Steps()))
	}
	if got.Calibration.Walk != nil {
		t.Fatal("a walk is in progress before anybody began one")
	}
	if got.Calibration.Stored != nil {
		t.Fatal("a set exists for an agent nobody has calibrated")
	}

	got = e.call(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "begin"}, id)
	id++
	if got.Calibration.Walk == nil || got.Calibration.Walk.Pending != 0 {
		t.Fatalf("after begin the walk is %+v, want it asking for step 0", got.Calibration.Walk)
	}

	// Every step answered: captured for the required three, and the optional
	// ones split so the set carries both outcomes.
	skipped := map[string]bool{"error": true, "menu-open": true}
	for {
		st := got.Calibration
		if st.Walk == nil {
			break
		}
		step := st.Steps[st.Walk.Pending]
		action := "capture"
		if skipped[step.Label] {
			action = "skip"
		} else {
			// The person drives their agent to the state they were asked
			// for; the frame is read from the grid by the backend, never
			// sent by this test.
			e.grid.Feed(e.sid, []byte("\x1b[2J\x1b[2;1Hstate "+step.Label+"\x1b[2;1H"))
		}
		got = e.call(t, "agent.calibration.answer", map[string]any{
			"sessionId": e.sid, "action": action, "step": st.Walk.Pending,
		}, id)
		id++
	}
	if got.Calibration.Stored == nil || !got.Calibration.Stored.Complete {
		t.Fatalf("after answering every step the stored set is %+v, want a complete one",
			got.Calibration.Stored)
	}

	// A complete set is not authority (nocx-jse6x). These screens are plain
	// text with none of the chrome the shipped rule reads, so the rule answers
	// unknown for every one of them and earns nothing — which is the direction
	// that matters: a set nobody could verify against leaves nocx lighting a
	// dot rather than typing.
	if v := got.Calibration.Verified; v.MayType {
		t.Fatalf("a complete set of screens the rule cannot read granted typing: %+v", v)
	}
	if v := got.Calibration.Verified; len(v.Disagreements) != v.Labelled-v.Agreed || v.Agreed != 0 {
		t.Fatalf("verdict %+v does not account for every labelled state it was checked against", v)
	}

	// And the set is on disk, replayable, with the frames the person produced.
	set, found, err := e.store.Load("claude")
	if err != nil || !found {
		t.Fatalf("load the set: found=%v err=%v", found, err)
	}
	frames, err := set.Frames(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("replay the set: %v", err)
	}
	if len(frames) != len(agentcalib.Steps())-len(skipped) {
		t.Fatalf("%d labelled frames replayed, want the %d that were captured",
			len(frames), len(agentcalib.Steps())-len(skipped))
	}
	for _, lf := range frames {
		if !strings.Contains(lf.Frame.Text(1), "state "+string(lf.Label)) {
			t.Fatalf("the frame labelled %s replays to %q, which is not the screen produced for it",
				lf.Label, lf.Frame.Text(1))
		}
	}
	if rec, asked := set.Record(agentcalib.LabelError); !asked || !rec.Skipped {
		t.Fatalf("the skipped label reads back as %+v (asked=%v)", rec, asked)
	}
}

// TestAgentCalibrationCarriesNoLabelOnTheWire is the first falsifier, checked
// where it would have to be broken. A caller cannot name a label because there
// is no field to name one in — the strict decoder refuses the payload rather
// than ignoring the field, which is the difference between a guard and a
// comment.
func TestAgentCalibrationCarriesNoLabelOnTheWire(t *testing.T) {
	e := newCalibrationEnv(t)
	for i, params := range []map[string]any{
		{"sessionId": e.sid, "action": "capture", "step": 0, "label": "asks-you"},
		{"sessionId": e.sid, "action": "capture", "label": "asks-you"},
	} {
		err := e.callErr(t, "agent.calibration.answer", params, 100+i)
		if err == nil {
			t.Fatalf("params %+v were accepted; a label reached the backend", params)
		}
		if err.Code != -32602 {
			t.Fatalf("params %+v answered %+v, want invalid params", params, err)
		}
	}
}

// TestAgentCalibrationRefusesASkippedRequiredStep is the second falsifier over
// the wire: a completed calibration cannot lack one of the three, so skipping
// one is refused rather than recorded.
func TestAgentCalibrationRefusesASkippedRequiredStep(t *testing.T) {
	e := newCalibrationEnv(t)
	got := e.call(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "begin"}, 2)
	step := got.Calibration.Steps[got.Calibration.Walk.Pending]
	if !step.Required {
		t.Fatalf("the walk opens on %s, which is optional", step.Label)
	}
	err := e.callErr(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "skip", "step": 0}, 3)
	if err == nil {
		t.Fatal("skipping a required step over the wire succeeded")
	}
	if !strings.Contains(err.Message, step.Label) {
		t.Fatalf("refusal %q does not name the step it refused", err.Message)
	}
}

// TestAgentCalibrationRefusesAStaleAnswer is the other half of binding a label
// to a step: a surface that redrew late is refused rather than answered into
// the wrong label.
func TestAgentCalibrationRefusesAStaleAnswer(t *testing.T) {
	e := newCalibrationEnv(t)
	e.call(t, "agent.calibration.answer", map[string]any{"sessionId": e.sid, "action": "begin"}, 2)
	e.call(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "capture", "step": 0}, 3)
	err := e.callErr(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "capture", "step": 0}, 4)
	if err == nil {
		t.Fatal("answering step 0 twice succeeded; a label was re-pointed at a later frame")
	}
}

// TestAgentCalibrationUnwiredIsNotFound keeps the availability gate honest: a
// surface shown six steps it can never answer would send a person to drive
// their agent for nothing.
func TestAgentCalibrationUnwiredIsNotFound(t *testing.T) {
	env := newLifecycleTestEnv(t)
	for i, method := range []string{"agent.calibration", "agent.calibration.answer"} {
		resp := jsonrpcCallWithID(t, env.conn, method, map[string]any{}, 200+i)
		var got rpcEnvelope
		if err := json.Unmarshal(resp, &got); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if got.Error == nil || got.Error.Code != -32601 {
			t.Fatalf("unwired %s answered %+v, want method not found", method, got.Error)
		}
	}
}

// ── the verdict: what the rule has EARNED against that set (nocx-jse6x) ────

// claudeWorkingScreen is the idle chrome with a live spinner in the status
// stack — the contiguous run of rows directly above the token meter. The
// grammar is what separates a live spinner from a finished turn's summary,
// which lands in the same slot: a live one carries "… (" and closes it.
func claudeWorkingScreen(cols int) string {
	rule := strings.Repeat("─", cols)
	return "\x1b[2J\x1b[6;1H* Misting… (2s)\x1b[7;1H              0 tokens" +
		"\x1b[8;1H" + rule + "\x1b[9;1H❯ \x1b[10;1H" + rule +
		"\x1b[12;1H  ⏵⏵ auto mode on\x1b[9;3H"
}

// claudePermissionScreen is the tool-approval dialog, which REPLACES the input
// box rather than overlaying it. The cursor parks on the selected option, and
// that is the marker the agent's own output cannot forge.
func claudePermissionScreen(_ int) string {
	return "\x1b[2J\x1b[6;1H Do you want to create note.txt?" +
		"\x1b[7;1H ❯ 1. Yes\x1b[8;1H   2. No\x1b[7;2H"
}

// walkWith answers every step: it paints the screen named for that label and
// captures, or declines the step when no screen is named for it.
func (e *calibrationEnv) walkWith(t *testing.T, screens map[string]string, id int) agentCalibrationResult {
	t.Helper()
	got := e.call(t, "agent.calibration.answer",
		map[string]any{"sessionId": e.sid, "action": "begin"}, id)
	id++
	for got.Calibration.Walk != nil {
		st := got.Calibration
		step := st.Steps[st.Walk.Pending]
		action := "skip"
		if screen, named := screens[step.Label]; named {
			// The person drives their agent into the state they were asked
			// for. The frame is read off the grid by the backend at the
			// instant the answer arrives; this test never sends one.
			e.grid.Feed(e.sid, []byte(screen))
			action = "capture"
		}
		got = e.call(t, "agent.calibration.answer", map[string]any{
			"sessionId": e.sid, "action": action, "step": st.Walk.Pending,
		}, id)
		id++
	}
	return got
}

// requiredScreens is the three states a calibration cannot complete without,
// each painted as the SHIPPED claude rule reads it. Optional states are
// declined, so what is verified is exactly what was produced.
func requiredScreens() map[string]string {
	return map[string]string{
		"idle":     claudeIdleScreen(40),
		"working":  claudeWorkingScreen(40),
		"asks-you": claudePermissionScreen(40),
	}
}

// TestAgentCalibrationVerdictIsOnTheWireWithItsConsequence is this bead's
// acceptance criterion watched end to end, off the real socket, through the
// shipped claude rule: a person walks the three required states with their
// agent really in them, and the answer says the rule may now be typed against.
//
// It also pins the state BEFORE the walk, which is the one a person is in on
// first opening the page: no set, and therefore no authority — with a reason
// rather than a bare false, because a surface has to state the consequence.
func TestAgentCalibrationVerdictIsOnTheWireWithItsConsequence(t *testing.T) {
	e := newCalibrationEnv(t)

	before := e.call(t, "agent.calibration", map[string]any{"sessionId": e.sid}, 2)
	if before.Calibration.Verified.MayType {
		t.Fatal("an agent nobody has calibrated may be typed into")
	}
	if before.Calibration.Verified.Reason == "" {
		t.Fatal("an unverified verdict crossed the wire with no reason in it")
	}

	got := e.walkWith(t, requiredScreens(), 3)
	v := got.Calibration.Verified
	if !v.MayType {
		t.Fatalf("after a correct walk the rule may not type: reason=%q disagreements=%+v",
			v.Reason, v.Disagreements)
	}
	if v.Labelled != len(requiredScreens()) || v.Agreed != v.Labelled {
		t.Fatalf("verdict is %d of %d, want all %d states that were produced",
			v.Agreed, v.Labelled, len(requiredScreens()))
	}
	if v.Reason != "" {
		t.Fatalf("a verified verdict carries a reason: %q", v.Reason)
	}
}

// And the same rule loses that authority when a label stops classifying —
// which is the shape an agent's update takes. Nothing about the rule changes
// here: the set does, and the verdict follows it. The shipped rule is subject
// to its own verification exactly like any other.
func TestAgentCalibrationRevokesTypingWhenALabelStopsClassifying(t *testing.T) {
	e := newCalibrationEnv(t)
	if got := e.walkWith(t, requiredScreens(), 2); !got.Calibration.Verified.MayType {
		t.Fatalf("the rule did not verify before the set was changed: %+v", got.Calibration.Verified)
	}

	// The two labels swap places on disk, the marks staying where they are:
	// every frame is still one the person produced, and only what they are
	// said to be has moved. The screen labelled idle is now the approval
	// dialog, which is the direction that ends in an approved tool call.
	set, found, err := e.store.Load("claude")
	if err != nil || !found {
		t.Fatalf("load the set: found=%v err=%v", found, err)
	}
	idle, asks := -1, -1
	for i, rec := range set.Labels {
		switch rec.Label {
		case agentcalib.LabelIdle:
			idle = i
		case agentcalib.LabelAsksYou:
			asks = i
		}
	}
	set.Labels[idle].Label, set.Labels[asks].Label = agentcalib.LabelAsksYou, agentcalib.LabelIdle
	if err := e.store.Save(set); err != nil {
		t.Fatalf("save: %v", err)
	}

	after := e.call(t, "agent.calibration", map[string]any{"sessionId": e.sid}, 90)
	v := after.Calibration.Verified
	if v.MayType {
		t.Fatal("a rule whose labels no longer classify kept its typing authority")
	}
	if len(v.Disagreements) != 2 {
		t.Fatalf("disagreements = %+v, want both swapped labels", v.Disagreements)
	}
	if v.Reason == "" {
		t.Fatal("a revoked verdict crossed the wire with no reason in it")
	}
	for _, d := range v.Disagreements {
		if d.Expected == d.Got {
			t.Fatalf("disagreement %+v names the same state twice", d)
		}
	}
}
