package transport

// THE GUIDED CALIBRATION: nocx asks for a state, the person produces it, and
// the frame is labelled with the state that was asked for
// (nocx-etejh; contracts/agent.calibration.schema.json).
//
// # The label never crosses this boundary, and that is the design
//
// Verifying a rule against a recording it happened to be pointed at proves
// nothing, so the person produces the evidence — and the bead is falsified if
// a label can be attached to a frame they did not produce for it. The wire is
// where that would happen, so the wire carries no label: the params name a
// pane, an action, and the step the surface believes it is showing. Which
// label is written comes from the walk's pending step in internal/agentcalib,
// and the frame comes from the pane's live grid at the instant the request is
// handled. Neither is a value a caller can supply, which is what makes this
// structural rather than a matter of UI discipline.
//
// `step` is a staleness guard and never a selector: an answer that names a
// step other than the pending one is refused, so a surface that redrew late
// cannot answer a question it is no longer being asked.
//
// # One write method, because it is one state machine
//
// begin, capture, skip, redo and abandon are one `action` on one method rather
// than five methods, so the walk advances through a single ordered lane. Two
// methods could interleave — a capture arriving between an abandon and its
// answer — and the ordering that keeps a label on the right screen would then
// depend on which handler ran first.
//
// # It is not a third power
//
// The AD-6 amendment grants an enrolled pane's grid exactly two powers, and
// this exercises neither: it enrols nothing, types nothing, and lights no
// indicator. It reads a screen the pane's own operator is looking at and
// writes a file under the app directory that a later bead reads to decide
// whether a rule may be typed against. The decision is that bead's; this one
// only records what the person showed us.

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agentcalib"
)

// agentCalibrator is the transport's half of the calibration seam (AD-8).
// Four methods and no fifth: the transport may drive a walk and read its
// state, and may not name a label, hand in a frame, or write a set.
type agentCalibrator interface {
	Status(pane, agent string) (agentcalib.Status, error)
	Begin(pane, agent string) (agentcalib.Status, error)
	Answer(pane string, step int, answer agentcalib.Answer) (agentcalib.Status, error)
	Abandon(pane string)
}

// WithAgentCalibration attaches the calibration the guided walk runs through.
// Unwired, the methods answer "not found" rather than an empty step list: a
// surface shown six steps it can never answer would send a person to drive
// their agent for nothing.
func WithAgentCalibration(c agentCalibrator) WSServerOption {
	return func(s *WSServer) { s.agentCalibration = c }
}

type agentCalibrationParams struct {
	SessionID string `json:"sessionId"`
}

type agentCalibrationAnswerParams struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`
	// Step is the step the surface believes it is showing. Absent for begin
	// and abandon, which are the walk's ends rather than answers to a step.
	Step *int `json:"step,omitempty"`
}

func validateAgentCalibrationRaw(raw json.RawMessage) string {
	var p agentCalibrationParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	// Absent is legitimate: a surface asks for the pane list before a person
	// has picked one. Present and empty is not.
	if p.SessionID != "" && utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId is bounded"
	}
	return ""
}

func validateAgentCalibrationAnswerRaw(raw json.RawMessage) string {
	var p agentCalibrationAnswerParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.SessionID == "" || utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId is required and bounded"
	}
	switch p.Action {
	case "begin", "abandon":
		if p.Step != nil {
			return "step names an answer to a question; begin and abandon answer none"
		}
	case string(agentcalib.AnswerCapture), string(agentcalib.AnswerSkip), string(agentcalib.AnswerRedo):
		// The step is what makes a stale surface refusable rather than
		// answerable, so it is required exactly where it can guard.
		if p.Step == nil || *p.Step < 0 || *p.Step >= len(agentcalib.Steps()) {
			return "step is required and names one of the calibration's steps"
		}
	default:
		return "action is one of begin, capture, skip, redo, abandon"
	}
	return ""
}

// agentCalibrationResult carries the pane list with every answer for the same
// reason the emitting view does: one round trip refreshes both, and a pane
// whose observation closed leaves the list on the next answer, which is how
// the surface learns it has nothing left to calibrate.
type agentCalibrationResult struct {
	Panes       []agentCalibrationPane `json:"panes"`
	Calibration *agentCalibrationState `json:"calibration,omitempty"`
}

type agentCalibrationPane struct {
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
}

type agentCalibrationState struct {
	SessionID string                  `json:"sessionId"`
	Agent     string                  `json:"agent"`
	Steps     []agentCalibrationStep  `json:"steps"`
	Walk      *agentCalibrationWalk   `json:"walk,omitempty"`
	Stored    *agentCalibrationStored `json:"stored,omitempty"`
}

type agentCalibrationStep struct {
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Ask      string `json:"ask"`
	Expect   string `json:"expect"`
}

type agentCalibrationWalk struct {
	Pending int                      `json:"pending"`
	Given   []agentCalibrationRecord `json:"given"`
}

type agentCalibrationStored struct {
	Complete bool                     `json:"complete"`
	Labels   []agentCalibrationRecord `json:"labels"`
}

// agentCalibrationRecord carries no mark for a skipped label, because there is
// no frame behind it: absent and zero are different claims and only one of
// them is true.
type agentCalibrationRecord struct {
	Label   string `json:"label"`
	Skipped bool   `json:"skipped"`
	AtMs    *int64 `json:"atMs,omitempty"`
}

func (s *WSServer) handleAgentCalibration(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentCalibrationParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	out, agent, found := s.calibrationPanes(p.SessionID)
	if found {
		state, err := s.agentCalibration.Status(p.SessionID, agent)
		if err != nil {
			_ = r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return
		}
		out.Calibration = calibrationState(p.SessionID, state)
	}
	_ = r.TryResult(req.ID, mustMarshal(out))
}

func (s *WSServer) handleAgentCalibrationAnswer(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentCalibrationAnswerParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	out, agent, found := s.calibrationPanes(p.SessionID)
	if !found {
		// The pane stopped being watched between the surface's last answer
		// and this one. It is an error rather than a silent no-op: the
		// person is mid-walk and has to be told the walk has nowhere to go.
		_ = r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: nocx is not watching that pane, so there is no agent to calibrate",
		})
		return
	}
	state, err := s.applyCalibrationAction(p, agent)
	if err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + err.Error()})
		return
	}
	out.Calibration = calibrationState(p.SessionID, state)
	_ = r.TryResult(req.ID, mustMarshal(out))
}

func (s *WSServer) applyCalibrationAction(p agentCalibrationAnswerParams, agent string) (agentcalib.Status, error) {
	switch p.Action {
	case "begin":
		return s.agentCalibration.Begin(p.SessionID, agent)
	case "abandon":
		s.agentCalibration.Abandon(p.SessionID)
		return s.agentCalibration.Status(p.SessionID, agent)
	default:
		return s.agentCalibration.Answer(p.SessionID, *p.Step, agentcalib.Answer(p.Action))
	}
}

// calibrationPanes lists what nocx is watching and reports whether the named
// pane is one of them, with the agent the enrolment act named.
func (s *WSServer) calibrationPanes(sid string) (agentCalibrationResult, string, bool) {
	watching := s.paneObserver.Watching()
	out := agentCalibrationResult{Panes: make([]agentCalibrationPane, 0, len(watching))}
	agent, found := "", false
	for _, w := range watching {
		out.Panes = append(out.Panes, agentCalibrationPane{SessionID: w.PaneID, Agent: w.Agent})
		if sid != "" && w.PaneID == sid {
			agent, found = w.Agent, true
		}
	}
	return out, agent, found
}

func calibrationState(sid string, in agentcalib.Status) *agentCalibrationState {
	out := &agentCalibrationState{
		SessionID: sid,
		Agent:     in.Agent,
		Steps:     make([]agentCalibrationStep, 0, len(in.Steps)),
	}
	for _, step := range in.Steps {
		out.Steps = append(out.Steps, agentCalibrationStep{
			Label: string(step.Label), Required: step.Required,
			Ask: step.Ask, Expect: string(step.Expect),
		})
	}
	if in.Walk != nil {
		out.Walk = &agentCalibrationWalk{Pending: in.Walk.Pending, Given: calibrationRecords(in.Walk.Given)}
	}
	if in.Stored != nil {
		out.Stored = &agentCalibrationStored{Complete: in.Stored.Complete, Labels: calibrationRecords(in.Stored.Labels)}
	}
	return out
}

func calibrationRecords(in []agentcalib.Record) []agentCalibrationRecord {
	out := make([]agentCalibrationRecord, 0, len(in))
	for _, rec := range in {
		row := agentCalibrationRecord{Label: string(rec.Label), Skipped: rec.Skipped}
		if rec.AtMs != nil {
			at := *rec.AtMs
			row.AtMs = &at
		}
		out = append(out, row)
	}
	return out
}

// calibrationAvailable gates both methods on the whole chain. Both or neither:
// a surface that could read a calibration and not walk one would show a person
// six steps and no way to answer them.
func (s *WSServer) calibrationAvailable() bool {
	return s.paneObserver != nil && s.agentCalibration != nil
}

// agentCalibrationSpecs registers the two methods on the ORDINARY lane. The
// read is a mutex read and one file; the write paints one frame and writes two
// small files, and it happens at human pace — neither belongs behind a domain
// queue, and a person mid-walk must not wait on one.
func (s *WSServer) agentCalibrationSpecs() []methodSpec {
	return []methodSpec{
		whenAvailable(
			regResponder(s.lane, "agent.calibration", params(validateAgentCalibrationRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentCalibration(ctx, req, r) }
			}),
			s.calibrationAvailable,
			"method not found: pane observation not wired"),
		whenAvailable(
			regResponder(s.lane, "agent.calibration.answer", params(validateAgentCalibrationAnswerRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentCalibrationAnswer(ctx, req, r) }
			}),
			s.calibrationAvailable,
			"method not found: pane observation not wired"),
	}
}
