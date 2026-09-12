package transport

// A PERSON'S OWN RULES FOR AN AGENT: which rule reads a pane, switchable off,
// and where the document lives (nocx-y6w66; contracts/agent.rules.schema.json).
//
// # What this surface is for
//
// The registry used to be compile-time: a person who ran an agent nocx read
// wrongly could do nothing but wait for a release. This is the half of the fix
// a person reaches — the other half is internal/agentrule, which owns the
// documents, and internal/agentdriver, which owns what a rule IS.
//
// # Four states, and why the wire carries them rather than a pair of booleans
//
// shipped, user, disabled and unreadable are not two axes: "their document
// could not be read" is neither on nor off, and a surface given a bool would
// have to invent which. The set is closed, so a state nobody wrote a branch
// for is refused by the contract rather than drawn as whichever branch was
// written last.
//
// # Every write answers with the whole set
//
// The same rule agent.calibration follows and for the same reason: a write IS
// a read of the state it produced. A page that saved a document and then asked
// again would draw a list one round trip behind the person's own edit, and
// what it shows is a rule that decides whether nocx types into a pane.
//
// # What this cannot do
//
// It cannot name an agent this build ships no rule for (a rule REPLACES one,
// and there is nothing to replace), it cannot write a document that does not
// compile, and it cannot reach the build's own rules — those are in the
// binary. Deleting is the one way back to them, and switching detection off is
// deliberately not that.

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/agentrule"
)

// agentRuleStore is the transport's half of the rule-document seam (AD-8).
// There is no method here that reads a pane or classifies a frame: the
// transport may list, write, switch and delete a person's documents, and the
// rule those documents carry reaches the pane through the registry alone.
type agentRuleStore interface {
	// Directory is where the documents live, for a person who would rather
	// edit the file.
	Directory() string
	// Rules is every agent this build ships a rule for, with the state of the
	// person's half beside it.
	Rules() []agentrule.Entry
	// Set records a person's own rule for an agent, replacing whatever they
	// had. A document that could not answer is refused.
	Set(agent, document string) error
	// SetEnabled switches detection, which is a state and not a deletion.
	SetEnabled(agent string, enabled bool) error
	// Delete removes their document, which restores the shipped rule.
	Delete(agent string) error
}

// WithAgentRuleStore attaches where a person's own rules live.
//
// Unwired, the methods answer "not found" rather than an empty list of agents:
// a page that showed nobody to write a rule for would send a person looking
// for a control that this build does not have.
func WithAgentRuleStore(s agentRuleStore) WSServerOption {
	return func(server *WSServer) { server.agentRuleStore = s }
}

// maxRuleDocumentRunes bounds a rule document on the wire. The shipped claude
// rule is under 6k characters, and a rule is a document a person reads; the
// bound is here so that a payload cannot become one this backend holds in
// memory to compile it.
const maxRuleDocumentRunes = 64 * 1024

type agentRulesSetParams struct {
	Agent    string `json:"agent"`
	Document string `json:"document"`
}

// agentRulesSetEnabledParams names the switch as a POINTER, and the pointer is
// the whole of it: absent must be refused rather than read as false, or a
// caller that sent only an agent would switch detection off by omission — and
// the contract beside this says the field is required.
type agentRulesSetEnabledParams struct {
	Agent   string `json:"agent"`
	Enabled *bool  `json:"enabled"`
}

type agentRulesDeleteParams struct {
	Agent string `json:"agent"`
}

func validateAgentRulesSetRaw(raw json.RawMessage) string {
	var p agentRulesSetParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.Agent == "" || utf8.RuneCountInString(p.Agent) > maxIDRunes {
		return "agent is required and bounded"
	}
	if p.Document == "" {
		return "document is required; an empty document is not a rule"
	}
	if utf8.RuneCountInString(p.Document) > maxRuleDocumentRunes {
		return "document is bounded"
	}
	return ""
}

func validateAgentRulesSetEnabledRaw(raw json.RawMessage) string {
	var p agentRulesSetEnabledParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.Agent == "" || utf8.RuneCountInString(p.Agent) > maxIDRunes {
		return "agent is required and bounded"
	}
	if p.Enabled == nil {
		return "enabled is required"
	}
	return ""
}

func validateAgentRulesDeleteRaw(raw json.RawMessage) string {
	var p agentRulesDeleteParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.Agent == "" || utf8.RuneCountInString(p.Agent) > maxIDRunes {
		return "agent is required and bounded"
	}
	return ""
}

// agentRulesResult is every agent this build ships a rule for, and where the
// person's documents live. It is one shape for all four methods.
type agentRulesResult struct {
	Directory string         `json:"directory"`
	Rules     []agentRuleRow `json:"rules"`
}

type agentRuleRow struct {
	Agent string `json:"agent"`
	State string `json:"state"`
	// Document is the rule text as it stands: their own document, or the
	// shipped rule's text when they have not written one, so the page has
	// something to edit from on an install nobody has touched. Empty in the
	// unreadable state, deliberately — see internal/agentrule.
	Document string `json:"document"`
	// Problem is why their document could not be used. ABSENT when there is
	// none, because "no problem" and "a problem with an empty description"
	// are two different claims and only one of them is ever true.
	Problem string `json:"problem,omitempty"`
}

func (s *WSServer) handleAgentRules(_ context.Context, req jsonrpcRequest, r Responder) {
	_ = r.TryResult(req.ID, mustMarshal(s.agentRuleList()))
}

func (s *WSServer) handleAgentRulesSet(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentRulesSetParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "agent.rules.set: " + msg})
		return
	}
	s.agentRuleWrite(req, r, "agent.rules.set", func() error { return s.agentRuleStore.Set(p.Agent, p.Document) })
}

func (s *WSServer) handleAgentRulesSetEnabled(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentRulesSetEnabledParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "agent.rules.setEnabled: " + msg})
		return
	}
	s.agentRuleWrite(req, r, "agent.rules.setEnabled", func() error {
		return s.agentRuleStore.SetEnabled(p.Agent, *p.Enabled)
	})
}

func (s *WSServer) handleAgentRulesDelete(_ context.Context, req jsonrpcRequest, r Responder) {
	var p agentRulesDeleteParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "agent.rules.delete: " + msg})
		return
	}
	s.agentRuleWrite(req, r, "agent.rules.delete", func() error { return s.agentRuleStore.Delete(p.Agent) })
}

// agentRuleWrite is the shared half of the three writes: do the one thing this
// method does, then answer with the state it produced. The refusal is the
// store's own clause, and it reaches the surface as an error the page shows
// beside the document it was about — the person is looking at the text when it
// is refused, which is where a refusal from a validator belongs.
func (s *WSServer) agentRuleWrite(req jsonrpcRequest, r Responder, method string, write func() error) {
	if err := write(); err != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: method + ": " + err.Error()})
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(s.agentRuleList()))
}

// agentRules projects the store onto the wire. Never a nil list: an empty
// array and a missing one are two ways to say nothing, and the renderer's
// `.map` is what finds out the difference.
func (s *WSServer) agentRuleList() agentRulesResult {
	entries := s.agentRuleStore.Rules()
	out := agentRulesResult{
		Directory: s.agentRuleStore.Directory(),
		Rules:     make([]agentRuleRow, 0, len(entries)),
	}
	for _, entry := range entries {
		out.Rules = append(out.Rules, agentRuleRow{
			Agent:    entry.Agent,
			State:    string(entry.State),
			Document: entry.Document,
			Problem:  entry.Problem,
		})
	}
	return out
}

// agentRulesAvailable gates all four methods on the store being wired: a
// surface that could read which rule is in force and not edit one would be a
// page about a decision the person cannot make.
func (s *WSServer) agentRulesAvailable() bool { return s.agentRuleStore != nil }

// agentRuleStoreSpecs registers the four methods on the ORDINARY lane. Three of
// them write one small document at human pace and none blocks on anything: a
// person editing a rule is not a reason for the domain that is busy to wait,
// and it is not a reason for them to either.
func (s *WSServer) agentRuleStoreSpecs() []methodSpec {
	unavailable := "method not found: agent rules not wired"
	return []methodSpec{
		whenAvailable(
			regResponder(s.lane, "agent.rules", noParams(), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentRules(ctx, req, r) }
			}),
			s.agentRulesAvailable, unavailable),
		whenAvailable(
			regResponder(s.lane, "agent.rules.set", params(validateAgentRulesSetRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentRulesSet(ctx, req, r) }
			}),
			s.agentRulesAvailable, unavailable),
		whenAvailable(
			regResponder(s.lane, "agent.rules.setEnabled", params(validateAgentRulesSetEnabledRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentRulesSetEnabled(ctx, req, r) }
			}),
			s.agentRulesAvailable, unavailable),
		whenAvailable(
			regResponder(s.lane, "agent.rules.delete", params(validateAgentRulesDeleteRaw), func(r Responder) handlerFunc {
				return func(ctx context.Context, req jsonrpcRequest) { s.handleAgentRulesDelete(ctx, req, r) }
			}),
			s.agentRulesAvailable, unavailable),
	}
}
