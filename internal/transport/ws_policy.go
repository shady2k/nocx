package transport

// policy.get / policy.set / policy.setRule / policy.forgetRule /
// policy.explain / policy.classify — the ONE global agent policy (ADR-0020 §7 as amended
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
//
// The two RULE methods carry one thing the others do not: WHEN the write takes
// effect for the runs already in flight (policyRunsMode, nocx-r4fh8). A run's
// grant is minted from the whole policy when the run starts and is immutable
// for the run, so a rule taken back here does not reach a run using it — which
// is correct, and was silent. The default timing writes nothing when live runs
// would be left behind and answers with how many; the person then chooses to
// leave them running or to stop them. runsUnreachedByRuleWrite is where that
// count is computed and where the reading of "using it" is written down.

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
//
// AwaitingReview is the third half of it, and it is on the wire for exactly
// the reason Live is. A rule that is inert because it was saved under an
// earlier reading of commands is a permission that has quietly stopped
// working, and a page that did not say so would be the soft degrade AGENTS.md
// forbids. Working it out needs the reading of commands running NOW — a
// version number no result carries — joined to two facts about the rule, and
// content.RulesNeedingConfirmation is the ONE implementation of that join. A
// renderer that compared evaluatorVersion for itself would be a second one,
// agreeing everywhere anyone looked and disagreeing somewhere nobody did,
// while telling a person their permission works.
type policyResult struct {
	Policy         content.EffectPolicy `json:"policy"`
	Live           []content.Effect     `json:"live"`
	AwaitingReview []string             `json:"awaitingReview"`
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
	if w.AwaitingReview == nil {
		w.AwaitingReview = []string{}
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

// policyClassifyParams is the policy.classify params: one command line a
// person typed, and nothing else.
//
// The absence of an effect is the whole difference between this method and
// policy.explain, and it is worth saying plainly here because the two look
// alike. policy.explain is about a call that HAPPENED: the effect it
// classified as is a fact the caller was told, and deriving a second one would
// explain a decision nobody took. policy.classify is about a command NOBODY
// HAS RUN, so there is no call to have classified anything and DERIVING the
// effect is the entire point — it is the fact the person is being shown before
// they widen a permission, and the fact the rule then carries in grantedUnder.
// A caller that could state it here could state anything, which would make the
// permit typed after all.
type policyClassifyParams struct {
	Command string `json:"command"`
}

// policyClassifyResult is the READING of that command: what a run would make
// of it, and whether a standing rule may be written over it at all.
//
// It carries no decision. What the policy currently decides about a command is
// policy.explain's question and this method does not answer it: the surface
// asking here is about to CHANGE the policy, and an answer from before the
// change would be read as an answer about after it.
//
// Program and Commands are the two selector shapes a rule can be written with,
// and they come from here rather than from the caller for one reason: the
// renderer would otherwise have to split a command line into words, which is
// a parser, which is the second reading this whole design exists to prevent.
type policyClassifyResult struct {
	// Program is the command word a Program selector would name. Empty when
	// the reading was refused.
	Program string `json:"program"`
	// Commands is the canonical parse an Exact selector would name — the
	// same one content.StandingRule would save. Empty when the reading was
	// refused.
	Commands [][]string `json:"commands"`
	// Effect is the row that governs this command, derived from the tool
	// declaration table's reachable set. Absent when the reading was
	// refused: an effect for a command the parser could not resolve is a
	// guess, and a guess is what a permit must never be minted from.
	Effect content.Effect `json:"effect,omitempty"`
	// Features are the semantic facts the classifier recorded about this
	// command, from content's closed vocabulary — what a narrowing rule may
	// match instead of the spelling of a token. Always an array.
	Features []string `json:"features"`
	// Eligible reports that a standing rule may be written over this
	// reading at all.
	Eligible bool `json:"eligible"`
	// Reason is why not, in content's own words, and empty when eligible. A
	// command refused without one is a surface that stopped and did not say
	// why, which is the silent degrade AGENTS.md forbids.
	Reason string `json:"reason"`
}

// MarshalJSON sends unset lists as [] rather than null, for policyResult's
// reason: "never a null" belongs to the shape, not to every place that builds
// one, and a refused reading builds neither list.
func (r policyClassifyResult) MarshalJSON() ([]byte, error) {
	type wire policyClassifyResult // no MarshalJSON of its own: the recursion break
	w := wire(r)
	if w.Commands == nil {
		w.Commands = [][]string{}
	}
	if w.Features == nil {
		w.Features = []string{}
	}
	return json.Marshal(w)
}

// validatePolicyClassifyRaw checks the envelope and the command's bound, at
// the same bound policy.explain reads a command line to: a truncated command
// would be a classification of a different command, and this one is about to
// be widened into a permission.
func validatePolicyClassifyRaw(raw json.RawMessage) string {
	var p policyClassifyParams
	if msg := decodeObject(raw, &p, "command"); msg != "" {
		return msg
	}
	if p.Command == "" {
		return "command is required"
	}
	return boundedRunes("command", p.Command, policyCommandBound)
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

// policySetRuleParams is the policy.setRule params: ONE rule under "rule",
// and WHEN it takes effect for the work already running.
type policySetRuleParams struct {
	Rule *policyRuleParams `json:"rule"`
	Runs policyRunsMode    `json:"runs,omitempty"`
}

// policyForgetRuleParams is the policy.forgetRule params: ONE id, and WHEN
// forgetting it takes effect for the work already running.
type policyForgetRuleParams struct {
	ID   string         `json:"id"`
	Runs policyRunsMode `json:"runs,omitempty"`
}

// policyRunsMode is WHEN a rule write takes effect for the runs already in
// flight, and it exists because a grant is immutable for its run (ADR-0020
// decision 5). A run's authority is minted when the run starts, so an answer
// taken back on the settings page does not reach a run already using it. That
// is correct — it is what makes a grant a grant — and the defect it left is
// that NOBODY WAS TOLD: a person forgets an answer, believes the assistant can
// no longer do the thing, and a run started thirty seconds ago goes on doing
// it. Silence is worst exactly here, because the person acted specifically to
// stop something.
//
// So revocation has a time and the person chooses it. The three values are one
// question and its two answers, not three settings.
type policyRunsMode string

const (
	// runsAsk applies the write when it reaches every live run, and when it
	// does not, changes NOTHING and answers with how many it would leave
	// behind. It is the default, and the default is what makes the question
	// honest: a write that had already landed could only be reported, and a
	// person told "3 runs are still using the answer you just deleted" has
	// been handed a fact instead of a choice.
	runsAsk policyRunsMode = "ask"
	// runsFuture applies the write and leaves the running work alone. The
	// runs in flight finish under the answer they were minted with, and the
	// next run gets the new one.
	runsFuture policyRunsMode = "future"
	// runsStop applies the write and then terminalizes the runs it does not
	// reach, through the path agent.cancel takes, with a sentence that names
	// the answer that was taken back.
	runsStop policyRunsMode = "stop"
)

func (m policyRunsMode) valid() bool {
	switch m {
	case "", runsAsk, runsFuture, runsStop:
		return true
	}
	return false
}

// orAsk resolves an unstated mode. Absent is "ask" rather than "future"
// because the safe end of this question is the one that cannot leave a person
// believing they stopped something they did not.
func (m policyRunsMode) orAsk() policyRunsMode {
	if m == "" {
		return runsAsk
	}
	return m
}

// policyRuleWriteRuns is what every rule write says about the work already
// running: whether the write landed at all, how many live runs it does not
// reach, and — when the person chose to stop them — what actually happened to
// those runs.
//
// StoppedRuns and FinishedBeforeStop are two halves of one true sentence, and
// neither is a failure. A run can reach a terminal state on its own between
// the count and the stop; reporting it as stopped would credit this gesture
// with an ending it did not cause, and a person who is told "3 stopped" when
// one finished by itself has been told something false about their own work.
type policyRuleWriteRuns struct {
	// Applied says the write landed. It is false only in the ask mode with
	// live runs left behind: the store is untouched and the person has a
	// question to answer.
	Applied bool `json:"applied"`
	// AffectedRuns is how many live runs would go on deciding under the old
	// answer — counted at the moment of the call, over the runs' own grants.
	// See runsUnreachedByRuleWrite for what "using it" was taken to mean.
	AffectedRuns int `json:"affectedRuns"`
	// StoppedRuns is how many of those this call terminalized. Zero in every
	// mode but stop.
	StoppedRuns int `json:"stoppedRuns"`
	// FinishedBeforeStop is how many had already ended by the time the stop
	// reached them.
	FinishedBeforeStop int `json:"finishedBeforeStop"`
}

// policySetRuleResult names the rule the store now holds. The id is the whole
// reason it exists: a caller that sent none learns the one it was given, and
// that is the only place it can learn it without a second read. Added
// separates the two things one method does — a rule appended, or a rule
// replaced in place.
type policySetRuleResult struct {
	// ID is empty exactly when Applied is false: nothing was written, so
	// nothing was named. Added is meaningless in that case for the same
	// reason.
	ID    string `json:"id"`
	Added bool   `json:"added"`
	policyRuleWriteRuns
}

// policyForgetRuleResult says whether a rule was there to remove. An id
// naming nothing is not an error — the rule is already gone, which is what
// was asked for — so the answer carries the fact rather than raising it.
type policyForgetRuleResult struct {
	// Removed is false both when there was no such rule and when the write
	// did not happen at all; Applied is what tells the two apart, and the
	// difference matters — one means "already as you wanted", the other
	// means "you have a question to answer first".
	Removed bool `json:"removed"`
	policyRuleWriteRuns
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
	return validatePolicyRunsMode(p.Runs)
}

// validatePolicyRunsMode refuses a timing nobody declared. An unknown value is
// invalid params rather than a silent fall back to the default: "runs":"stopp"
// falling through to ask would leave a person's runs alive after they asked
// for them to stop, which is the one outcome this whole method exists to make
// impossible to get by accident.
func validatePolicyRunsMode(m policyRunsMode) string {
	if !m.valid() {
		return `runs must be "ask", "future" or "stop"`
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
	if msg := configIDRunes("id", p.ID); msg != "" {
		return msg
	}
	return validatePolicyRunsMode(p.Runs)
}

// policyHandlers serves policy.get and policy.set off one store seam, plus
// the live list the root named (static for the process's life, so it is
// copied in with the store rather than looked up per call).
type policyHandlers struct {
	store assistant.GlobalPolicy
	live  []content.Effect
	wired bool
	// runs is how a policy write reaches the work already in flight. It is
	// an interface rather than the registry itself because this file must
	// not learn how a run is held or how one is ended: it asks a question
	// and states an intent, and the agent lane answers both through the one
	// terminalization path (ws_agent.go). Nil is "no live runs", which is
	// what a server registered without the agent methods honestly has.
	runs liveRunRegistry
	r    Responder
}

// liveRunRegistry is what a policy write needs to know about the runs already
// in flight, and nothing else: which of them a write does not reach, and how
// to end those through the path a person's own stop takes.
//
// *WSServer implements it — it owns the run registry — and the registration
// below is where it is handed over. The alternative was for this file to read
// s.pendingRuns and close a run itself, which is a second terminal path and
// therefore a second set of half-terminal states.
type liveRunRegistry interface {
	// RunsUnreachedByRuleWrite reports the live runs whose grant would
	// decide differently once this write lands, ascending by run id.
	RunsUnreachedByRuleWrite(w content.RuleWrite) []int64
	// StopRunsForRevokedAnswer terminalizes those runs with the sentence a
	// person will read, and reports which it stopped and which had already
	// ended by the time it got there.
	StopRunsForRevokedAnswer(ctx context.Context, r Responder, ids []int64, sentence string) (stopped, alreadyFinished []int64)
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
		// The two rule methods, and the only two that carry the run
		// registry: a rule write is the one policy gesture a run already in
		// flight can be left behind by, because a grant is minted from the
		// whole policy and then frozen (ADR-0020 decision 5).
		regResponder(s.lane, "policy.setRule", params(validatePolicySetRuleRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, runs: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicySetRule(ctx, req) }
		}),
		regResponder(s.lane, "policy.forgetRule", params(validatePolicyForgetRuleRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, runs: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyForgetRule(ctx, req) }
		}),
		regResponder(s.lane, "policy.explain", params(validatePolicyExplainRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyExplain(ctx, req) }
		}),
		// policy.classify carries the live list for the same reason
		// policy.get does, and uses it for a different thing: policy.get
		// SHOWS which rows govern anything, and this DERIVES the effect a
		// command classifies as, which is bounded by the same declared set.
		// It is the mirror image of policy.explain above — that method
		// deliberately does not derive an effect because the caller's is the
		// fact; here there is no call, so deriving it is the whole point.
		regResponder(s.lane, "policy.classify", params(validatePolicyClassifyRaw), func(r Responder) handlerFunc {
			h := policyHandlers{store: s.agentPolicy, live: s.liveEffects, wired: s.agentPolicy != nil, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePolicyClassify(ctx, req) }
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
	// ONE read of the store, and both halves of the answer taken from it.
	// Two reads could straddle a write from the approval prompt and serve a
	// rule list and an inert-rule list that never coexisted.
	policy := h.store.Policy()
	awaiting := []string{}
	for _, rule := range policy.RulesNeedingConfirmation() {
		awaiting = append(awaiting, rule.ID)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policyResult{
		Policy:         policy,
		Live:           h.live,
		AwaitingReview: awaiting,
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

// handlePolicyClassify READS one command line. It never runs it, and nothing
// in this path could: the only thing that touches the text is
// assistant.CanonicalInvocation, which is a pure parse over a string, and
// content.StandingRule, which is a pure predicate over that parse. There is no
// exec, no shell, no filesystem call and no session — and that is the property
// the method exists for, asserted directly in
// TestPolicyClassify_ReadsTheCommandAndNeverRunsIt.
//
// Why a reading at all: a permit written over a loose selector is a claim about
// what a command DOES, and a person typing a word into a box has made no such
// claim. `find` is a read-shaped word and `find . -delete` is a destructive
// call; the only honest route to "any find command" is to have the backend read
// a representative command, show what the resulting rule would and would not
// reach, and then save a rule carrying the effect that reading found. This
// method is the reading half of that gesture.
//
// Three things it deliberately does not do. It does not decide whether the
// command is fit to be a rule — content.StandingRule already answers that, in
// words, for every shape that is unfit, and a second opinion here would be a
// second answer to drift from the first. It does not derive the effect from
// anything but the tool declaration table's reachable set, which the
// composition root handed in. And it answers no DECISION: what the policy
// currently decides is policy.explain's question, and an answer from before the
// change a caller is about to make would be read as an answer about after it.
func (h policyHandlers) handlePolicyClassify(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "policy.classify not available"})
		return
	}
	var p policyClassifyParams
	if msg := decodeObject(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "policy.classify: " + msg})
		return
	}
	inv := assistant.CanonicalInvocation(p.Command)
	rule, why := content.StandingRule(inv)
	if why != "" {
		_ = h.r.TryResult(req.ID, mustMarshal(policyClassifyResult{Reason: why}))
		return
	}
	effect := assistant.ClassifyInvocation(inv, h.live)
	if !content.LatticeEffect(effect) {
		// The declaration table reaches no class at all, so there is no row
		// for a permit to be bound to. It is a build-time state rather than
		// anything a person did, and it is still answered in words: a
		// surface that offered a permit here would write a rule the gate
		// refuses, and one that said nothing would be a control that does
		// nothing for no stated reason.
		_ = h.r.TryResult(req.ID, mustMarshal(policyClassifyResult{
			Reason: "nocx has not declared anything the assistant can do, " +
				"so there is nothing to allow in advance",
		}))
		return
	}
	// The selector halves come from the rule content.StandingRule built, not
	// from a second walk over the parse: the exact selector a standing answer
	// would carry IS the canonical parse, and taking it from anywhere else
	// would be two spellings of one fact.
	_ = h.r.TryResult(req.ID, mustMarshal(policyClassifyResult{
		Program:  rule.Selector.Exact[0][0],
		Commands: rule.Selector.Exact,
		Effect:   effect,
		Features: inv.Resources.Features,
		Eligible: true,
	}))
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
	rule := content.InvocationRule{
		ID:           p.Rule.ID,
		Selector:     p.Rule.Selector,
		Decision:     p.Rule.Decision,
		GrantedUnder: p.Rule.GrantedUnder,
		// The write is made under the reading of commands running NOW,
		// which is the claim the person is making by making it. Source and
		// createdAt are the store's to default or preserve.
		EvaluatorVersion: content.EvaluatorVersion,
	}
	// The SAME rule value is counted against and written, so what the person
	// is told about and what the store ends up holding cannot differ. A count
	// taken from a differently-stamped copy would answer about a rule nobody
	// was going to write.
	write := content.RuleWrite{ID: p.Rule.ID, Rule: &rule}
	affected := h.runsUnreachedByRuleWrite(write)
	mode := p.Runs.orAsk()
	if mode == runsAsk && len(affected) > 0 {
		_ = h.r.TryResult(req.ID, mustMarshal(policySetRuleResult{
			policyRuleWriteRuns: policyRuleWriteRuns{AffectedRuns: len(affected)},
		}))
		return
	}
	stored, err := h.store.SetRule(rule)
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
		policyRuleWriteRuns: h.settleRuns(ctx, mode, affected,
			"a standing answer this run was using was changed: "+stored.Label()),
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
	// The sentence is taken BEFORE the write, from the document that still
	// has the rule in it: a rule already forgotten cannot say what it was,
	// and a person reading "this run was stopped because you took back an
	// answer" is owed the answer's name.
	forgotten := ""
	for _, rule := range h.store.Policy().Rules {
		if rule.ID == p.ID {
			forgotten = rule.Label()
			break
		}
	}
	write := content.RuleWrite{ID: p.ID}
	affected := h.runsUnreachedByRuleWrite(write)
	mode := p.Runs.orAsk()
	if mode == runsAsk && len(affected) > 0 {
		_ = h.r.TryResult(req.ID, mustMarshal(policyForgetRuleResult{
			policyRuleWriteRuns: policyRuleWriteRuns{AffectedRuns: len(affected)},
		}))
		return
	}
	removed, err := h.store.ForgetRule(p.ID)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "policy.forgetRule: " + err.Error()})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(policyForgetRuleResult{
		Removed: removed,
		policyRuleWriteRuns: h.settleRuns(ctx, mode, affected,
			"a standing answer this run was using was taken back: "+forgotten),
	}))
}

// ── the runs a rule write leaves behind (nocx-r4fh8) ──────────────────────

// runsUnreachedByRuleWrite counts the live runs a rule write does not reach,
// and THIS COMMENT IS THE CHOICE OF READING, because the count is the part of
// this feature a person is asked to act on and a number they cannot trust is
// worse than no number at all.
//
// Every live run holds a grant minted from the whole policy as it stood when
// the run started, and immutable for the run (ADR-0020 decision 5). So "which
// runs use this answer" has two honest readings:
//
//   - EVERY LIVE RUN, because every one of their grants was minted from a
//     policy that contained the rule. It needs nothing but a count of the
//     registry, and it is useless: a person who forgets an answer about `df`
//     is told that six runs are affected when the answer governs none of
//     them, learns within a day that the number means "how many runs exist",
//     and dismisses the question from then on. That is worse than silence,
//     because it trains the dismissal that criterion 2 exists to prevent.
//
//   - ONLY THE RUNS WHOSE GRANT WOULD DECIDE DIFFERENTLY WITHOUT IT. It is
//     precise, it is what the words mean, and it is computable: the run's own
//     frozen policy is asked what it decides about the command lines the rule
//     speaks about, with the rule and without it, through the SAME evaluator
//     the run's enforcement uses (content.ChangedByRuleWrite, which is where
//     the probe and the layers it measures are written down). A rule the run
//     never matched, one shadowed by a stricter answer, and one whose permit
//     was granted under an effect this policy refuses outright all decide
//     nothing and are all correctly counted out.
//
// The second is what this implements. The cost is real and bounded — at most
// two probe invocations and seven effect rows per live run, all pure
// comparison over frozen values — and it buys a number a person can act on.
//
// WHAT IT DOES NOT CLAIM. It is a count at the moment of the call, not a
// promise about the next one: a run can finish on its own a microsecond later,
// which is why the stop path reports what it actually stopped separately from
// what it set out to stop.
func (h policyHandlers) runsUnreachedByRuleWrite(w content.RuleWrite) []int64 {
	if h.runs == nil {
		return nil
	}
	return h.runs.RunsUnreachedByRuleWrite(w)
}

// settleRuns performs the run half of the gesture, AFTER the policy write has
// already landed, and answers what happened.
//
// THE ORDER OF THE TWO ACTS IS THE POLICY WRITE FIRST, THEN THE STOPPING, and
// it is a decision rather than an accident.
//
// A revocation is the person's primary gesture: they came to take an answer
// back, and stopping the work is the consequence they chose for it. Writing
// first means a failure can only ever leave them with LESS than they asked
// for, never with destroyed work in exchange for nothing. If the store write
// fails, this function is never reached: no run is stopped, the method answers
// the error, and the policy and every run are exactly as they were — the
// interval closes where it opened.
//
// The other order fails the other way, and that failure is a lie. Stopping
// first and writing second can end three runs and then fail to forget the
// answer, leaving a person who was told "stopped 3 runs" holding a permission
// they believe they removed, with the work that would have finished under it
// destroyed. There is no sentence that makes that outcome true.
//
// Between the two acts, the state is: the answer is revoked and durable, and
// some runs are still running under the old one. That is precisely the state
// the "leave them running" answer chooses on purpose, so a failure here lands
// the person in a state the product already has a name for — and the counts
// this returns say which runs are in it.
func (h policyHandlers) settleRuns(
	ctx context.Context, mode policyRunsMode, affected []int64, sentence string,
) policyRuleWriteRuns {
	out := policyRuleWriteRuns{Applied: true, AffectedRuns: len(affected)}
	if mode != runsStop || len(affected) == 0 || h.runs == nil {
		return out
	}
	stopped, finished := h.runs.StopRunsForRevokedAnswer(ctx, h.r, affected, sentence)
	out.StoppedRuns = len(stopped)
	out.FinishedBeforeStop = len(finished)
	return out
}
