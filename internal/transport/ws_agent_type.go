package transport

// agent.type — the door from a surface to the typing primitive
// (nocx-dkawo.1; contracts/agent.type.schema.json).
//
// # What crosses, and what deliberately does not
//
// A pane, some text, and whether the submit key is pressed. NOT the agent, and
// not the state: both are read on this side, from the enrolment act and from
// the pane's own live grid, at the instant the request is handled. A caller
// that could name the agent could name one whose rule verifies while the pane
// runs something else, and the frame would then be read by a rule that was
// never about it — the same shape as the calibration's missing `label`, and
// refused for the same reason.
//
// # THE REFUSAL TRAVELS IN THE RESULT
//
// A submission into a pane that is asking a person to approve a tool is not a
// malformed request: the params were well formed and the pane was real. It is
// not a success either, and a control that silently does nothing is
// indistinguishable from a broken one. So the outcome and the state that
// decided it come back in the result, where the surface reads them and tells
// the person — the same choice session.signal made, and for the same reason.
//
// # Authority, and why it is the same as a keystroke's
//
// The connection must hold the session (AD-9: a connection may act only on the
// sessions it attached), exactly as the data plane requires before it will
// carry a byte the person typed. This method puts bytes on the SAME input
// queue, so it may not be reachable from a connection the keyboard is not.
//
// # It is not a third power
//
// The AD-6 amendment grants an enrolled pane's grid exactly two powers, and
// this exercises the first of them by name: whether nocx may write into this
// pane. It opens no enrolment, moves no wave state, lights no indicator, and
// decides nothing here — the decision is internal/agenttyping's, twice, on two
// frames it reads itself.

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agenttyping"
	"github.com/shady2k/nocx/internal/session"
)

// agentTypist is the transport's half of the typing seam (AD-8). Two methods
// and no third: the transport may ask for text to be typed, with or without
// the key that submits it, and may not classify a screen, verify a rule, or
// reach a pane's input any other way.
type agentTypist interface {
	Type(paneID, text string) agenttyping.Result
	Submit(paneID, text string) agenttyping.Result
}

// WithAgentTypist attaches the typing primitive. Unwired, the method answers
// "method not found": a surface offering to type into a pane through a backend
// that cannot must be told so, rather than shown a refusal it would read as the
// rule declining.
func WithAgentTypist(t agentTypist) WSServerOption {
	return func(s *WSServer) { s.agentTypist = t }
}

type agentTypeParams struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
	// Submit says whether the key that submits is pressed after the text. It
	// defaults to FALSE, and the default is the safe direction on purpose: a
	// caller that forgot the field leaves its text in the input region for a
	// person to look at, rather than starting a turn nobody asked for.
	Submit bool `json:"submit,omitempty"`
}

// agentTypeResult is the wire's answer (contracts/agent.type.schema.json). The
// pane is echoed so a surface that fired two of these cannot mix the answers
// up, and the agent because "which rule refused" is half of what a person needs
// to repair it.
type agentTypeResult struct {
	SessionID string `json:"sessionId"`
	Agent     string `json:"agent"`
	Outcome   string `json:"outcome"`
	State     string `json:"state"`
	// Reason is omitted for a submission that was wholly accepted. Empty
	// would be a claim that there was a reason and it was nothing.
	Reason string `json:"reason,omitempty"`
}

func validateAgentTypeRaw(raw json.RawMessage) string {
	var p agentTypeParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.SessionID == "" || utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId is required and bounded"
	}
	// The bound is the package's, read from it rather than restated: a
	// validator that admitted more than the primitive types would refuse in
	// the handler with a different sentence, which is two answers to one
	// question.
	if p.Text == "" || len(p.Text) > agenttyping.MaxText {
		return "text is required and bounded"
	}
	return ""
}

func (s *WSServer) handleAgentType(_ context.Context, state *connState, req jsonrpcRequest, r Responder) {
	var p agentTypeParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	// The same two checks a keystroke passes, in the same order: the
	// connection must hold the session (authority), and the registry must
	// still have it (existence). Neither is redundant.
	sid := session.ID(p.SessionID)
	if !state.has(sid) {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	if _, err := s.registry.Get(sid); err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	// One call or the other, never both. Type is not a prefix of Submit — it
	// is the same submission with the second segment left off — so asking for
	// both puts the text into the pane twice, which the over-the-wire test
	// caught by counting the writes that arrived.
	var out agenttyping.Result
	if p.Submit {
		out = s.agentTypist.Submit(p.SessionID, p.Text)
	} else {
		out = s.agentTypist.Type(p.SessionID, p.Text)
	}
	_ = r.TryResult(req.ID, mustMarshal(agentTypeResult{
		SessionID: p.SessionID,
		Agent:     out.Agent,
		Outcome:   string(out.Outcome),
		State:     string(out.State),
		Reason:    out.Reason,
	}))
}

// agentTypeSpecs registers agent.type on the ORDINARY lane. What it does is
// two frame reads and two non-blocking queue submissions; nothing here waits on
// a process, a disk or a network, so it belongs behind no domain queue — and a
// wake that queued behind one would be a wake that arrived after the screen it
// was decided on had moved.
func (s *WSServer) agentTypeSpecs() []methodSpec {
	return []methodSpec{
		whenAvailable(
			reg(s.lane, "agent.type", params(validateAgentTypeRaw),
				func(_ *wsConn, state *connState, r Responder) handlerFunc {
					return func(ctx context.Context, req jsonrpcRequest) {
						s.handleAgentType(ctx, state, req, r)
					}
				}),
			func() bool { return s.agentTypist != nil },
			"method not found: typing into a pane is not wired"),
	}
}
