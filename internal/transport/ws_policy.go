package transport

// policy.get / policy.set / policy.setRule / policy.forgetRule /
// policy.explain — the ONE global agent policy (ADR-0020 §7 as amended
// 2026-08-16, accepted), ON THE WIRE because the settings surface edits it:
// the matrix the run grants are minted from (runGrantFor). The result shape is declared once in
// contracts/policy.get.schema.json, generated into the renderer, and the Go
// side is validated against it (DTO + over the socket, ws_contract_test.go).
//
// The set path is the person's configuration surface, and the tool-name rule
// (ADR-0028 decision 4: the grant is over resources and effects, never over
// tool names) is enforced HERE, by trying: the validator parses the policy
// with content.ParseEffectPolicy, and unknown keys — a tool name where an
// effect goes — are an invalid params error. There is no second vocabulary in
// which a tool name could be expressed.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// policyResult is the policy.get result: the resolved global policy, plus
// which of its rows govern anything at all. The matrix marshals as the
// canonical seven effect rows (EffectPolicy's wire form); the world gets no
// preset enum here — a person reads what a run may do, one effect at a time.
//
// Live is the second half of that sentence and the reason it is on the wire:
// seven rows are drawn, and today only two have a declared tool behind them.
// The settings surface cannot derive which — nothing in the matrix says —
// and the renderer must never map a tool name to an effect (ADR-0028
// decision 4), so the tool declaration table answers, once. This file does
// not read that table: the composition root does, and hands the answer in
// through WithLiveEffects. A control that governs nothing must not look like
// one that does.
type policyResult struct {
	Policy content.EffectPolicy `json:"policy"`
	Live   []content.Effect     `json:"live"`
}

// MarshalJSON sends an unset Live as [] rather than null. The contract
// declares live an array with no null branch, and "never a null" belongs to
// the shape rather than to every place that builds one — a nil slice
// reaching the wire is the defect the schemas caught the first time they
// ran, when vault.status marshalled providers as null.
func (p policyResult) MarshalJSON() ([]byte, error) {
	type wire policyResult // no MarshalJSON of its own: this is the recursion break
	w := wire(p)
	if w.Live == nil {
		w.Live = []content.Effect{}
	}
	return json.Marshal(w)
}

// policySetParams is the policy.set params: the matrix under "policy".
type policySetParams struct {
	Policy json.RawMessage `json:"policy"`
}

// policySetResult acknowledges a persisted policy.
type policySetResult struct {
	OK bool `json:"ok"`
}

// validatePolicySetRaw is the policy.set params validator: the envelope must
// parse, and the policy must BE a policy — strict matrix parse (unknown keys,
// a tool name, a bad decision or a bad scope are all refused here, so a
// config path that names a tool cannot exist).
func validatePolicySetRaw(raw json.RawMessage) string {
	var p policySetParams
	if msg := decodeObject(raw, &p); msg != "" {
		return msg
	}
	if len(p.Policy) == 0 {
		return "policy is required"
	}
	if policyDocumentNamesRules(p.Policy) {
		return policySetNamesRules
	}
	if _, err := content.ParseEffectPolicy(p.Policy); err != nil {
		return "policy: " + err.Error()
	}
	return ""
}

// policySetNamesRules is the refusal that makes policy.set a MATRIX write and
// nothing else.
//
// A whole-document write is how a person's standing answers were deleted once
// already (nocx-39bly): the page saves the matrix while holding a document it
// read a minute ago, and the prompt has written a rule into the store since.
// The patch that followed read "an absent rules field means nothing to say",
// which holds only while the renderer never sends the key — and `rules: []`
// is exactly what JSON.stringify of an empty list sends. It is not absent, it
// says "no rules", and it would be obeyed.
//
// So the document cannot say it at all. A gesture that is about one rule
// writes one rule, through a method whose name says so, and the refusal names
// that method rather than leaving a caller to guess which key offended.
const policySetNamesRules = "policy: a matrix write may not carry rules; " +
	"one rule is written or forgotten at a time, with policy.setRule and policy.forgetRule"

// policyDocumentNamesRules reports that a policy document states the rules
// key at all — present-and-empty included, which is the whole point. The
// validator and the handler ask the same function so the refusal cannot hold
// at one door and not the other.
func policyDocumentNamesRules(policy json.RawMessage) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(policy, &doc); err != nil {
		return false // not an object: ParseEffectPolicy answers for it
	}
	_, named := doc["rules"]
	return named
}

// policyExplainParams is the policy.explain params: one command line and the
// effect the call classified as.
//
// The effect is the CALLER's, not this handler's to derive, and that is
// deliberate: the effect a call classifies as depends on the tool's declared
// reachable set, which the approval prompt was told (agent.approvalRequested
// carries it) and which this method has no run to ask. Deriving a second
// answer here would explain a decision nobody took.
type policyExplainParams struct {
	Command string         `json:"command"`
	Effect  content.Effect `json:"effect"`
}

// policyExplainResult is what the policy decides about that command, and HOW
// it got there — the steps the evaluator took, in the order it took them.
//
// The trace is the point. The Verdict alone cannot say whether a person's
// standing rule lost or was never read, and a page that worked it out from the
// policy document would be a second implementation of the precedence order in
// TypeScript: the two would agree everywhere anyone looked and disagree
// somewhere nobody did. So the evaluator records its own steps and this method
// carries them; nothing on this side derives one.
type policyExplainResult struct {
	Effect   content.Effect          `json:"effect"`
	Decision content.Decision        `json:"decision"`
	Cause    content.OutOfScopeCause `json:"cause,omitempty"`
	Resource *content.GrantScope     `json:"resource,omitempty"`
	Trace    []content.TraceStep     `json:"trace"`
}

// MarshalJSON sends an empty trace as [] rather than null, for the reason
// policyResult does: "never a null" belongs to the shape rather than to every
// place that builds one. A sealed trace always has at least one step, so this
// guards the shape and not a case the evaluator produces.
func (r policyExplainResult) MarshalJSON() ([]byte, error) {
	type wire policyExplainResult // no MarshalJSON of its own: the recursion break
	w := wire(r)
	if w.Trace == nil {
		w.Trace = []content.TraceStep{}
	}
	return json.Marshal(w)
}

// policyCommandBound is how long a command line policy.explain will read. It
// is generous rather than tight: the thing being explained is a command a
// person is looking at, and truncating one would explain a different command.
const policyCommandBound = 4096

// validatePolicyExplainRaw checks the envelope, the command's bound and that
// the effect is one the matrix HAS A ROW FOR. The last is the one that
// matters: an effect outside the lattice has no row, so the evaluator would
// answer with the ask an absent row decides, and the caller would read that as
// a policy decision rather than as a typo. content.LatticeEffect is the same
// predicate content's own rule gate uses — there is no second list of the
// seven here.
func validatePolicyExplainRaw(raw json.RawMessage) string {
	var p policyExplainParams
	if msg := decodeObject(raw, &p, "command", "effect"); msg != "" {
		return msg
	}
	if p.Command == "" {
		return "command is required"
	}
	if msg := boundedRunes("command", p.Command, policyCommandBound); msg != "" {
		return msg
	}
	if !content.LatticeEffect(p.Effect) {
		return "effect must name an effect class the matrix has a row for"
	}
	return ""
}

// policyRuleParams is the wire form of ONE invocation rule a caller writes:
// what the rule SAYS, and nothing about where it came from.
//
// Provenance is deliberately absent. The id is minted by the backend (AD-7)
// and may only be restated to name a rule that already exists; createdAt,
// source and evaluatorVersion are facts the store records about the write,
// not claims a caller gets to make — a renderer that could set createdAt or
// source could dress a rule it wrote as one a person answered.
type policyRuleParams struct {
	ID           string                     `json:"id,omitempty"`
	Selector     content.InvocationSelector `json:"selector"`
	Decision     content.Decision           `json:"decision"`
	GrantedUnder content.Effect             `json:"grantedUnder,omitempty"`
}

// policySetRuleParams is the policy.setRule params: ONE rule under "rule".
type policySetRuleParams struct {
	Rule *policyRuleParams `json:"rule"`
}

// policyForgetRuleParams is the policy.forgetRule params: ONE id.
type policyForgetRuleParams struct {
	ID string `json:"id"`
}

// policySetRuleResult names the rule the store now holds. The id is the whole
// reason it exists: a caller that sent none learns the one it was given, and
// that is the only place it can learn it without a second read. Added
// separates the two things one method does — a rule appended, or a rule
// replaced in place.
type policySetRuleResult struct {
	ID    string `json:"id"`
	Added bool   `json:"added"`
}

// policyForgetRuleResult says whether a rule was there to remove. An id
// naming nothing is not an error — the rule is already gone, which is what
// was asked for — so the answer carries the fact rather than raising it.
type policyForgetRuleResult struct {
	Removed bool `json:"removed"`
}

// validatePolicySetRuleRaw checks the envelope and the shape of the one rule.
// It deliberately does NOT decide whether the rule is safe: that is
// content's gate (validateInvocationRules, through ParseEffectPolicy in the
// store), and a second reading of the asymmetry here would be a second answer
// to drift from the first.
func validatePolicySetRuleRaw(raw json.RawMessage) string {
	var p policySetRuleParams
	if msg := decodeObject(raw, &p, "rule"); msg != "" {
		return msg
	}
	if p.Rule == nil {
		return "rule is required"
	}
	if msg := configIDRunes("rule.id", p.Rule.ID); msg != "" {
		return msg
	}
	if p.Rule.Decision == "" {
		return "rule.decision is required"
	}
	return ""
}

// validatePolicyForgetRuleRaw checks the envelope and the id's bound.
func validatePolicyForgetRuleRaw(raw json.RawMessage) string {
	var p policyForgetRuleParams
	if msg := decodeObject(raw, &p, "id"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

// policyHandlers serves policy.get and policy.set off one store seam, plus
// the live list the root named (static for the process's life, so it is
// copied in with the store rather than looked up per call).
type policyHandlers struct {
	store assistant.GlobalPolicy
	live  []content.Effect
	wired bool
	r     Responder
}

// policySpecs registers policy.get and policy.set on the lane submission:
// config-shaped work, no domain gate of its own (the lane is the admission).
func (s *WSServer) policySpecs() []methodSpec {
	return []methodSpec{
		regResponder(s.lane, "policy.get", noParams(), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, live: s.liveEffects, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyGet(ctx, req) }
		}),
		regResponder(s.lane, "policy.set", params(validatePolicySetRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicySet(ctx, req) }
		}),
		regResponder(s.lane, "policy.setRule", params(validatePolicySetRuleRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicySetRule(ctx, req) }
		}),
		regResponder(s.lane, "policy.forgetRule", params(validatePolicyForgetRuleRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyForgetRule(ctx, req) }
		}),
		regResponder(s.lane, "policy.explain", params(validatePolicyExplainRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyExplain(ctx, req) }
		}),
	}
}

// handlePolicyGet answers with the resolved policy (global default now; the
// workspace override resolves inside the same content.ResolvePolicy the mint
// uses, so the settings surface and the runs it configures read one value).
func (h policyHandlers) handlePolicyGet(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.get not available"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policyResult{
		Policy: h.store.Policy(),
		Live:   h.live,
	}))
}

// handlePolicySet persists a validated policy. The next ask run's grant is
// minted from it — no restart, the run mint reads the store live.
func (h policyHandlers) handlePolicySet(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.set not available"})
		return
	}
	var p policySetParams
	if msg := decodeObject(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.set: " + msg})
		return
	}
	if policyDocumentNamesRules(p.Policy) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.set: " + policySetNamesRules})
		return
	}
	policy, err := content.ParseEffectPolicy(p.Policy)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.set: " + err.Error()})
		return
	}
	if err := h.store.SetPolicy(policy); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "policy.set: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policySetResult{OK: true}))
}

// handlePolicyExplain answers what the stored policy decides about one
// command, and every step it took to decide it.
//
// Two things it deliberately does NOT do. It does not parse the command a
// second way — assistant.CanonicalInvocation is the parser a run uses, so the
// explanation is of the same reading the enforcement had. And it does not walk
// the policy itself: content.ExplainInvocation records the steps as it takes
// them, and this handler only carries them. The moment either of those became
// a local reimplementation, the page would be explaining a decision the
// evaluator did not make.
func (h policyHandlers) handlePolicyExplain(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.explain not available"})
		return
	}
	var p policyExplainParams
	if msg := decodeObject(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.explain: " + msg})
		return
	}
	verdict := h.store.Policy().ExplainInvocation(p.Effect, assistant.CanonicalInvocation(p.Command))
	result := policyExplainResult{
		Effect:   p.Effect,
		Decision: verdict.Decision,
		Cause:    verdict.Cause,
		Trace:    verdict.Trace,
	}
	if verdict.Cause != "" {
		// The resource is the one that fell outside, and it rides along
		// only with the cause that names it: a verdict the resource layer
		// answered "inside" has no resource to point at.
		resource := verdict.Resource
		result.Resource = &resource
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result))
}

// handlePolicySetRule writes ONE rule and leaves the rest of the document
// exactly as it was.
//
// The read-modify-write happens in the STORE, under the store's own lock, and
// not here. Reading the policy in this handler, editing it and writing it
// back would be the very race this method exists to remove, moved one layer
// up: the prompt writes rules while the settings page holds a document it
// read a minute ago, so a merge on the read side is merging a copy that was
// already stale when it arrived.
//
// An id naming no stored rule is invalid params rather than an internal
// error, and that is the AD-7 line drawn where a person can see it: a
// renderer may replace a rule it can SEE, and may not choose the identity of
// a new one — leaving the id out is how a new rule is written, and the answer
// carries the id the mint gave it.
func (h policyHandlers) handlePolicySetRule(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.setRule not available"})
		return
	}
	var p policySetRuleParams
	if msg := decodeObject(req.Params, &p); msg != "" || p.Rule == nil {
		if msg == "" {
			msg = "rule is required"
		}
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.setRule: " + msg})
		return
	}
	stored, err := h.store.SetRule(content.InvocationRule{
		ID:           p.Rule.ID,
		Selector:     p.Rule.Selector,
		Decision:     p.Rule.Decision,
		GrantedUnder: p.Rule.GrantedUnder,
		// The write is made under the reading of commands running NOW,
		// which is the claim the person is making by making it. Source and
		// createdAt are the store's to default or preserve.
		EvaluatorVersion: content.EvaluatorVersion,
	})
	switch {
	case errors.Is(err, assistant.ErrNoSuchRule):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.setRule: " + err.Error() +
			"; leave the id out to write a new rule — the backend mints it"})
		return
	case errors.Is(err, content.ErrPolicySyntax):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.setRule: " + err.Error()})
		return
	case err != nil:
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "policy.setRule: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policySetRuleResult{
		ID:    stored.ID,
		Added: p.Rule.ID == "",
	}))
}

// handlePolicyForgetRule removes ONE rule by id, in the store and under its
// lock, for the same reason setRule does.
//
// An unknown id succeeds with removed:false. It is not an error because the
// person's gesture — "stop applying this rule" — is already satisfied, and a
// page whose read predates somebody else's forget would otherwise raise a
// danger toast about a state it wanted.
func (h policyHandlers) handlePolicyForgetRule(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.forgetRule not available"})
		return
	}
	var p policyForgetRuleParams
	if msg := decodeObject(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.forgetRule: " + msg})
		return
	}
	removed, err := h.store.ForgetRule(p.ID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "policy.forgetRule: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policyForgetRuleResult{Removed: removed}))
}
