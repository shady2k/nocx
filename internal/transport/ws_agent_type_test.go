package transport

// agent.type, off the real socket (AGENTS.md testing rule 5) and through the
// production wiring: a real panegrid Store fed real bytes, the real watcher,
// the real shipped claude rule, a real calibration replayed against the real
// corpus, and a real session whose input queue is what the bytes land on.
//
// What a person gets that they could not before: nocx puts a line into an
// agent's input box for them, and refuses — out loud, naming the state that
// refused — when that pane is asking them to approve a tool, working, showing
// a menu, showing an error, or showing anything the rule cannot read.

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/agentcalib"
	"github.com/shady2k/nocx/internal/agentcapture"
	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panegrid"
	"github.com/shady2k/nocx/internal/paneobserve"
	"github.com/shady2k/nocx/internal/session"
)

// typingEnv is the emitting view's environment plus the two things typing
// needs: a calibration that has actually verified, and somewhere for the bytes
// to land.
type typingEnv struct {
	env  *lifecycleTestEnv
	grid *panegrid.Store
	sid  string
	sent *sentInput
}

// sentInput is the pane's input queue, recorded. It stands in for the session
// registry's EnqueueWrite so a test can say "nothing at all" and mean it; what
// the composition root wires is internal/app.paneInput, which is the same one
// method over the real session.
type sentInput struct{ jobs [][]byte }

func (s *sentInput) Accept(_ string, b []byte) bool {
	s.jobs = append(s.jobs, append([]byte(nil), b...))
	return true
}

// enrolledAs answers the enrolment act's question for one pane, which is where
// the agent name comes from — never from the wire.
type enrolledAs struct {
	pane  string
	agent string
}

func (e enrolledAs) AgentOn(paneID string) (string, bool) {
	if paneID != e.pane {
		return "", false
	}
	return e.agent, true
}

// corpusFrame replays one moment of the driver's own corpus. The captures live
// in internal/agentdriver because the rule was written from them, and are
// replayed through internal/agentcapture, which owns the format.
func corpusFrame(t *testing.T, name string, atMs int64) panegrid.Frame {
	t.Helper()
	path := filepath.Join("..", "agentdriver", "testdata", "captures", name+".jsonl")
	header, chunks, err := agentcapture.Read(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	moments, err := agentcapture.Frames(log.NewSlogAdapter(nil), header, chunks, []int64{atMs})
	if err != nil {
		t.Fatalf("replay %s to %dms: %v", path, atMs, err)
	}
	return moments[0].Frame
}

// walkFrames hands a calibration walk one frame per read, in order.
type walkFrames struct {
	frames []panegrid.Frame
	at     int
}

func (w *walkFrames) Frame(string) (panegrid.Frame, error) {
	f := w.frames[w.at]
	if w.at < len(w.frames)-1 {
		w.at++
	}
	return f, nil
}

// verifiedCalibration drives a REAL calibration to completion against the
// shipped claude rule, using the corpus's own frames for the three states a
// person is asked to produce. There is no other way to obtain a verdict that
// permits typing: agentcalib.Verdict's backing field is unexported and written
// in one statement, so a test that faked this would be faking the gate.
func verifiedCalibration(t *testing.T, rules *agentdriver.Registry) *agentcalib.Calibrations {
	t.Helper()
	store, err := agentcalib.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("calibration store: %v", err)
	}
	screens := &walkFrames{frames: []panegrid.Frame{
		corpusFrame(t, "claude-idle", 11000),       // Begin: the geometry
		corpusFrame(t, "claude-idle", 11000),       // idle     → free_text
		corpusFrame(t, "claude-working", 17000),    // working  → working
		corpusFrame(t, "claude-permission", 49000), // asks-you → permission_choice
	}}
	calib := agentcalib.New(log.NewSlogAdapter(nil), screens, store, rules)
	const walkPane = "calibration"
	if _, err := calib.Begin(walkPane, "claude"); err != nil {
		t.Fatalf("begin calibration: %v", err)
	}
	for i, step := range agentcalib.Steps() {
		answer := agentcalib.AnswerCapture
		if !step.Required {
			answer = agentcalib.AnswerSkip
		}
		if _, err := calib.Answer(walkPane, i, answer); err != nil {
			t.Fatalf("answer step %d (%s): %v", i, step.Label, err)
		}
	}
	if v := calib.Verify("claude"); !v.MayType() {
		t.Fatalf("the shipped rule did not verify against the corpus it was written from: %+v", v)
	}
	return calib
}

func newTypingEnv(t *testing.T) *typingEnv {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	grid := panegrid.New(logger)
	rules, err := agentdriver.NewRegistry(agentdriver.Claude())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	watcher := paneobserve.New(logger, grid, rules)
	sent := &sentInput{}

	// The pane id is not known until the session is open, so the enrolment
	// seam is bound through a pointer the environment fills in — the same
	// order the composition root has, where the typist is built before any
	// session exists.
	enrol := &enrolledAs{agent: "claude"}
	typist := agenttyping.New(logger, grid, rules, verifiedCalibration(t, rules), enrol, sent)

	env := newLifecycleTestEnv(t,
		WithPaneGrid(grid), WithPaneObserver(watcher), WithAgentTypist(typist))
	watcher.SetEmitter(env.ws.EmitPaneObservation)
	sid := env.openSession(t, 1)
	enrol.pane = sid

	// The enrolment act, as the pane enroller performs it: the grid first,
	// then the observation beside it.
	if err := grid.Enrol(sid, 120, 40); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	t.Cleanup(func() { grid.Withdraw(sid) })
	watcher.Watch(sid, "claude")
	return &typingEnv{env: env, grid: grid, sid: sid, sent: sent}
}

// show feeds the pane the bytes of one moment of the corpus, so the screen the
// backend classifies is a screen a real claude actually drew.
func (e *typingEnv) show(t *testing.T, name string, atMs int64) {
	t.Helper()
	e.grid.Feed(e.sid, agentcapture.Paint(corpusFrame(t, name, atMs)))
}

func (e *typingEnv) call(t *testing.T, params any, id int) agentTypeResult {
	t.Helper()
	resp := jsonrpcCallWithID(t, e.env.conn, "agent.type", params, id)
	var envelope rpcEnvelope
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("agent.type error: %+v", envelope.Error)
	}
	validateJSON(t, loadSchema(t, "agent.type.schema.json"), envelope.Result, "agent.type wire")
	var out agentTypeResult
	if err := json.Unmarshal(envelope.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// TestAgentType_OverTheWireConformsToContract is the acceptance check: a real
// result off a real socket, validated against the schema, for a pane in
// free_text — and the bytes that reached the pane are the text framed as a
// bracketed paste plus the submit key as a SEPARATE write.
func TestAgentType_OverTheWireConformsToContract(t *testing.T) {
	e := newTypingEnv(t)
	e.show(t, "claude-idle", 11000)

	got := e.call(t, map[string]any{"sessionId": e.sid, "text": "wake up", "submit": true}, 2)
	if got.Outcome != string(agenttyping.OutcomeSubmitted) {
		t.Fatalf("outcome = %q (%s), want submitted", got.Outcome, got.Reason)
	}
	if got.State != string(agentdriver.StateFreeText) || got.Agent != "claude" {
		t.Fatalf("state/agent = %q/%q, want free_text/claude", got.State, got.Agent)
	}
	if got.Reason != "" {
		t.Fatalf("a wholly accepted submission carried a reason: %q", got.Reason)
	}
	if len(e.sent.jobs) != 2 {
		t.Fatalf("%d writes reached the pane, want the text and the submit key as two", len(e.sent.jobs))
	}
	if want := "\x1b[200~wake up\x1b[201~"; string(e.sent.jobs[0]) != want {
		t.Fatalf("first write = %q, want the bracketed paste %q", e.sent.jobs[0], want)
	}
	if string(e.sent.jobs[1]) != "\r" {
		t.Fatalf("second write = %q, want the submit key on its own", e.sent.jobs[1])
	}
}

// THE REFUSAL IS A RESULT, and it is the shape that matters most here: a pane
// showing a tool-approval dialog answers over the same socket, against the same
// schema, with the state that refused — and nothing reaches the pane.
func TestAgentType_APaneAskingForApprovalIsRefusedInTheResult(t *testing.T) {
	e := newTypingEnv(t)
	e.show(t, "claude-permission", 49000)

	got := e.call(t, map[string]any{"sessionId": e.sid, "text": "wake up", "submit": true}, 2)
	if got.Outcome != string(agenttyping.OutcomeRefused) {
		t.Fatalf("outcome = %q, want refused", got.Outcome)
	}
	if got.State != string(agentdriver.StatePermissionChoice) {
		t.Fatalf("state = %q, want the state that refused", got.State)
	}
	if got.Reason == "" {
		t.Fatal("a refusal nobody can read is how this degrades into typing blindly")
	}
	if len(e.sent.jobs) != 0 {
		t.Fatalf("%d writes reached a pane asking a person to approve a tool: %q",
			len(e.sent.jobs), e.sent.jobs)
	}
}

// Without `submit` the text lands and nothing is pressed. Absent means false,
// and the default is the safe direction: a caller that forgot the field leaves
// its text in the input region rather than starting a turn nobody asked for.
func TestAgentType_WithoutSubmitPressesNothing(t *testing.T) {
	e := newTypingEnv(t)
	e.show(t, "claude-idle", 11000)

	got := e.call(t, map[string]any{"sessionId": e.sid, "text": "wake up"}, 2)
	if got.Outcome != string(agenttyping.OutcomeTyped) {
		t.Fatalf("outcome = %q (%s), want typed", got.Outcome, got.Reason)
	}
	if len(e.sent.jobs) != 1 {
		t.Fatalf("%d writes reached the pane, want only the text", len(e.sent.jobs))
	}
}

// A session this connection does not hold is refused before anything reads a
// screen — the same check the data plane makes before it will carry a byte the
// person typed (AD-9).
func TestAgentType_ASessionTheConnectionDoesNotHoldIsRefused(t *testing.T) {
	e := newTypingEnv(t)
	e.show(t, "claude-idle", 11000)

	resp := jsonrpcCallWithID(t, e.env.conn, "agent.type", map[string]any{
		"sessionId": string(session.NewID()), "text": "wake up", "submit": true,
	}, 2)
	var envelope rpcEnvelope
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602 for a session this connection does not hold", envelope.Error)
	}
	if len(e.sent.jobs) != 0 {
		t.Fatalf("%d writes reached a pane the connection does not hold", len(e.sent.jobs))
	}
}

// And the method is not there at all when the typist is not wired, so a
// surface can tell "nocx cannot type here" from "the rule refused" — only one
// of those is repairable by calibrating.
func TestAgentType_IsNotFoundWhenTheTypistIsNotWired(t *testing.T) {
	env := newLifecycleTestEnv(t)
	sid := env.openSession(t, 1)
	resp := jsonrpcCallWithID(t, env.conn, "agent.type", map[string]any{
		"sessionId": sid, "text": "wake up",
	}, 2)
	var envelope rpcEnvelope
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != -32601 {
		t.Fatalf("error = %+v, want -32601 method not found", envelope.Error)
	}
}
