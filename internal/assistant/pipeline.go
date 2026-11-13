package assistant

// Authority is a pipeline with named points around every tool call
// (ADR-anntnxdj decision 2). The pipeline owns what must not be skippable: the
// call's identity, schema validation before any policy, the declaration's
// narrowing, the one execution of the tool and the result's own contract. Each
// point owns one question, and its type says what its answer may be. No point
// receives a continuation, so none can skip, repeat or replace the execution:
// the pipeline calls the next point itself.
//
// This file is the seam later stages plug into. The kernel's in-process calls
// already run through it (nocx-a62cl.2.3), with its own preparation, policy and
// pre-execute steps as links until stage 2 rewrites them onto the points; the
// tool endpoint follows in nocx-a62cl.2.2.

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// Call is one exact proposal, as every point sees it: the run, the attempt, the
// tool, the model's own id for the call, a hash of the canonical arguments and
// the facts preparation derived from those arguments. Every field is a copy
// taken when the call entered the pipeline and there is no way to change one
// afterwards, so what a person approved is what executes.
//
// The facts are read-only and they are the SAME for every point, which is what
// lets a point decide on what the call actually is rather than on the model's
// spelling: the declaration with its per-call classification applied, the
// validated arguments, the resources they resolved, the canonical command
// parse and the grant. A point that reads them reads what preparation
// validated and what the tool will run with.
type Call struct {
	runID   string
	attempt int
	tool    string
	callID  string
	argHash string
	facts   *callFacts
}

// callFacts is what preparation derived from one call. The pipeline builds it
// once, before any point runs, and never changes it afterwards.
type callFacts struct {
	decl              agenttools.Tool
	args              map[string]any
	resources         []agenttools.ResourceRef
	resourceDeclared  bool
	invocation        content.Invocation
	invocationEffects []content.Effect
	grant             content.Grant
	rawParams         []byte
}

// RunID is the run this call belongs to.
func (c Call) RunID() string { return c.runID }

// Attempt is the attempt of the proposal's entry this call is.
func (c Call) Attempt() int { return c.attempt }

// Tool is the declaration's name.
func (c Call) Tool() string { return c.tool }

// CallID is the model's own id for this call.
func (c Call) CallID() string { return c.callID }

// ArgHash is the hash of the canonical arguments — the same value an approval
// is bound by, so a changed argument never matches an earlier yes.
func (c Call) ArgHash() string { return c.argHash }

// Declaration is the tool the call reaches, with its per-call classification
// applied: a declaration that names a shell command has the effect that
// command actually reaches, and every point sees that same declaration.
func (c Call) Declaration() agenttools.Tool {
	if c.facts == nil {
		return agenttools.Tool{}
	}
	return c.facts.decl
}

// Arguments is the validated argument object the declaration's own schema
// accepted. It is a copy, so a point cannot change what the tool runs with.
func (c Call) Arguments() map[string]any {
	if c.facts == nil || c.facts.args == nil {
		return nil
	}
	out := make(map[string]any, len(c.facts.args))
	for name, value := range c.facts.args {
		out[name] = value
	}
	return out
}

// Resources is every resource preparation resolved from the validated
// arguments. declares is false when the declaration names no resource at all,
// which is a different fact from a resource-bearing declaration whose current
// call touches none.
func (c Call) Resources() (resources []agenttools.ResourceRef, declares bool) {
	if c.facts == nil {
		return nil, false
	}
	return append([]agenttools.ResourceRef(nil), c.facts.resources...), c.facts.resourceDeclared
}

// Invocation is the canonical command parse the policy reads: the one
// derivation of what the command names, shared by every point.
func (c Call) Invocation() content.Invocation {
	if c.facts == nil {
		return content.Invocation{}
	}
	return cloneInvocation(c.facts.invocation)
}

// InvocationEffects is the effect set THIS call reaches when the declaration
// derives one from its arguments — nil when the declared set stands.
func (c Call) InvocationEffects() []content.Effect {
	if c.facts == nil {
		return nil
	}
	return append([]content.Effect(nil), c.facts.invocationEffects...)
}

// Grant is the authority this call runs under. It is a fact of the call, not a
// decision about it: a point reads the grant to know what the rows say.
func (c Call) Grant() content.Grant {
	if c.facts == nil {
		return content.Grant{}
	}
	return c.facts.grant
}

// RawParams is the exact argument bytes the model sent, copied. It is what an
// approval is bound by and what the tool executes.
func (c Call) RawParams() []byte {
	if c.facts == nil {
		return nil
	}
	return append([]byte(nil), c.facts.rawParams...)
}

// withGrant restates the same call under a freshly minted grant, and it is the
// ONE field of a call the pipeline changes after the call entered it: the
// authority a call runs under is read after the answer, because a person's
// answer can write the policy it is read from and because a long wait can
// outlive the grant minted at admission (ADR-anntnxdj §3). Everything a person
// answered about — the tool, the arguments, the resources, the call id — is the
// same value.
func (c Call) withGrant(grant content.Grant) Call {
	facts := callFacts{}
	if c.facts != nil {
		facts = *c.facts
	}
	facts.grant = grant
	c.facts = &facts
	return c
}

// Decision is a PreCall's answer about one call. The interface is sealed, so
// the set is exactly Allow, Deny and Ask: no package outside this one can add a
// fourth answer that the pipeline would have to guess at.
type Decision interface{ decision() }

// Allow lets the call go on to the next point.
type Allow struct{}

// Deny refuses the call, and Reason says why in words a person and a model can
// both read. Cause carries the error underneath a refusal that had one — a
// call whose context had already ended, or a gate that could not see — for a
// caller that classifies it; it never changes the answer, which is still a no.
type Deny struct {
	Reason string
	Cause  error
}

// Ask says the call needs a person's answer before it may go on. Facts are what
// that person is shown beside the question — advisory evidence, never a
// decision. A Screen raises the same Ask about a result, so one question has one
// vocabulary whichever end of the call it is asked at.
type Ask struct {
	Reason string
	Facts  []string
	// Egress is a Screen's structured evidence: what was found in the bytes,
	// where, and whether it was in the tool's error text. It is carried on
	// the question rather than through Facts for the reason the approval's
	// scripts are: the surface renders the findings themselves, and a
	// []string would lose the source, the kind and the span. It is facts,
	// never the material.
	Egress *EgressAsk
}

// EgressAsk is the evidence a Screen put to a person about a result it
// withheld. One Approver answers both kinds of question, so the kind travels
// with the question.
type EgressAsk struct {
	Findings []EgressFinding
	WasError bool
}

func (Allow) decision() {}
func (Deny) decision()  {}
func (Ask) decision()   {}

// Answer is a person's answer to an Ask. Approved and Declined are the two a
// person gives; Unanswered is an approver that could not reach one at all, and
// it is a refusal — no answer is never a yes.
type Answer interface{ answer() }

// Approved releases the call to the points that follow the question.
type Approved struct{}

// Declined refuses the call, and Reason is the person's own no.
type Declined struct{ Reason string }

// Unanswered is an approver that could not put the question to anybody, or
// whose answer never arrived. Err says what went wrong, when the approver knows.
type Unanswered struct{ Err error }

func (Approved) answer()   {}
func (Declined) answer()   {}
func (Unanswered) answer() {}

// Verdict is a Screen's answer about the bytes that are about to leave. It is
// Pass, Refuse or Ask and it never carries replacement bytes: a screen decides
// whether the result goes, never what it says.
type Verdict interface{ verdict() }

// Pass releases the result as the tool produced it.
type Pass struct{}

// Refuse withholds the result, and Reason says why.
type Refuse struct{ Reason string }

func (Ask) verdict() {}

func (Pass) verdict()   {}
func (Refuse) verdict() {}

// ScreenDeclinedError is a person's no to a Screen's question, and it is the
// reason a declined result ENDS THE RUN rather than riding the call's slot as
// a refusal sentence. A PreCall's Ask is about a call that has not run, so its
// no is an answer the model can read. A Screen's Ask is about a result that
// already exists: the result IS what the screen decides about, so a no leaves
// the call with nothing to continue with (ADR-anntnxdj §3, "a declined egress
// ends the run"). It travels as the refusal's Cause so no caller mistakes it
// for an answer.
type ScreenDeclinedError struct{ Reason string }

func (e *ScreenDeclinedError) Error() string {
	if e.Reason == "" {
		return "the person declined to release the result"
	}
	return e.Reason
}

// Outbound is what actually leaves the tool: the result text, or the tool's
// error text when it failed. An error is output too — it carries paths, hosts
// and names — and a screen that sees only successes has closed the wide door
// and left the narrow one open.
type Outbound struct {
	Text     string
	WasError bool
}

// PreCall decides whether a call may go on: classification, row scope, the
// policy matrix and standing declines are its work. It is consulted before any
// capability is constructed and before the tool can run.
type PreCall interface {
	Decide(ctx context.Context, c Call) Decision
}

// Approver puts an Ask to a person and returns their answer. The pipeline calls
// it only on an Ask, and only once per call.
type Approver interface {
	Answer(ctx context.Context, c Call, a Ask) Answer
}

// Guard can only refuse. Its result is a reason and a bool, and there is no
// allow: no ordering can turn a refusal back into permission, and no guard can
// grant what a PreCall did not. TestAuthorityPointsOfferNoAllowToGuardsAndNoContinuation
// is the test that fails the day this interface grows an allow result, or any
// point grows a continuation.
type Guard interface {
	Refuse(ctx context.Context, c Call) (reason string, refused bool)
}

// Screen inspects the bytes that are about to leave the tool, after it has run
// and before the caller sees them. It cannot replace them: Pass and an approved
// Ask release the exact bytes, and Refuse withholds them.
type Screen interface {
	Inspect(ctx context.Context, c Call, r Outbound) Verdict
}

// Attempts opens the durable record of one call's execution. The pipeline
// calls it after every point that may refuse and before the capability is
// constructed, so the attempt exists from before the effect until the caller
// records its outcome — the interval ADR-0028 states with both ends. An error
// fails the run: a call that cannot be recorded does not run.
//
// The pipeline owns WHEN it opens; the caller owns what is written, because
// what an attempt is made of belongs to the caller's own store.
type Attempts interface {
	Open(ctx context.Context, c Call) error
}

// AuthorityChain is one kind of caller's points, in the order they run. An
// empty field means that kind of caller has no such point: a chain with no
// PreCall allows every call that reaches it, a chain with no approver refuses
// every Ask, and a chain with no Guard, Screen or attempt step adds nothing.
type AuthorityChain struct {
	PreCalls []PreCall
	Approver Approver
	// Order is the turn's order when this chain serves a turn's calls: one
	// call at a time, in order, and a call behind a declined one refused with
	// the same reason (ADR-anntnxdj §2). Nil is a caller with no turn to
	// order — the tool endpoint, whose callers are independent.
	Order *TurnOrder
	// MintGrant mints the authority a call goes on under AFTER a person's
	// answer (ADR-anntnxdj §3, "refreshed after an answer"). The pipeline
	// calls it only on a call that was asked about, and the guards and the
	// capability that follow read what it returns. Nil leaves the call on the
	// grant it was admitted with.
	MintGrant func(ctx context.Context, c Call) (content.Grant, bool)
	Guards    []Guard
	Screens   []Screen
	Attempts  Attempts

	// execute is the caller's own runtime for the one tool call — the kernel's
	// announcing runtime, the tool endpoint's declared executors. Nil uses the
	// operation's executor.
	execute dispatchExecutor
}

// Pipeline runs calls through one chain. It is the fixed half of the authority
// contract: a caller composes the points, and the pipeline guarantees that every
// one of them runs in order on an immutable call, and that the tool runs exactly
// once between the guards and the screens.
type Pipeline struct {
	operation *dispatchOperation
	chain     AuthorityChain
}

// NewPipeline binds one caller kind's chain to a dispatcher's own operation, so
// the pipeline prepares, narrows and executes exactly as that dispatcher does —
// one implementation of schema validation and of "the tool runs once", shared
// rather than copied. A dispatcher this package did not assemble has no
// operation to share and is refused rather than run unprepared.
func NewPipeline(dispatcher ToolDispatcher, chain AuthorityChain) (*Pipeline, error) {
	operation, ok := dispatcher.(*dispatchOperation)
	if !ok {
		return nil, fmt.Errorf("assistant pipeline: %T does not carry a dispatch operation", dispatcher)
	}
	return &Pipeline{operation: operation, chain: chain}, nil
}

// PipelineRefusal is a call the pipeline did not run, or whose result it
// withheld: a point said no, nobody answered, or the call was cancelled. It is
// an answer rather than a fault — Reason is written to be read by the model and
// by a person — and Cause carries the error underneath it (a cancelled context,
// an approver that failed) for a caller that classifies it.
type PipelineRefusal struct {
	Call   Call
	Reason string
	Cause  error
}

func (e *PipelineRefusal) Error() string {
	return fmt.Sprintf("assistant pipeline: %s: %s", e.Call.Tool(), e.Reason)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *PipelineRefusal) Unwrap() error { return e.Cause }

// Dispatch runs one call through the chain, in the only order there is: the
// turn's order, schema and the call's facts, PreCall*, the approver on an Ask,
// the fresh grant after an answer, Guard*, the attempt, the declaration's
// Narrow, the tool exactly once, Screen*.
//
// A call whose context ended BEFORE it entered the pipeline is its caller's
// own business: the caller may still need the call's outcome recorded — the
// kernel's run lease is exactly that, a run whose context ended as an outcome
// to write (run_test.go's
// TestMiddleware_RunLeaseOutcomeRecordsCauseAfterCancellation) — so the
// pipeline does not refuse it on the caller's behalf. A chain that wants such
// a call refused says so with its own point (the tool endpoint's first
// PreCall). A context that ends WHILE the call is in the pipeline is a
// refusal wherever the pipeline notices it: an answer that arrived after the
// caller gave up does not authorise anything.
func (p *Pipeline) Dispatch(invocation ToolInvocation, attempt int, callID string) (string, error) {
	outcome, err := p.run(invocation, attempt, callID)
	if err != nil {
		return "", err
	}
	return outcome.output, outcome.runErr
}

// run is Dispatch's whole body, returning the dispatch outcome as well: the
// prepared call a carrier must screen after the tool ran, and the tool's own
// error apart from a refusal, which the kernel's caller needs to record the
// attempt's outcome. The tool endpoint sees only Dispatch.
func (p *Pipeline) run(invocation ToolInvocation, attempt int, callID string) (dispatchOutcome, error) {
	invocation.RawParams = append([]byte(nil), invocation.RawParams...)
	ctx := invocation.Context
	// The turn's order comes first: a call that is not this turn's turn has
	// not started, and a call queued behind a declined one is refused with
	// that decline rather than run on its own (ADR-anntnxdj §2).
	if p.chain.Order != nil {
		release, declined, err := p.chain.Order.enter(ctx)
		if err != nil {
			return dispatchOutcome{}, &PipelineRefusal{
				Call:   Call{runID: invocation.RunContext.RunID, tool: invocation.Method},
				Reason: "the call was cancelled while it waited for its turn: " + errText(err),
				Cause:  err,
			}
		}
		defer release()
		if declined != nil {
			reason := declined.Reason
			if reason == "" {
				reason = "a person declined the call before this one"
			}
			return dispatchOutcome{}, &PipelineRefusal{
				Call:   Call{runID: invocation.RunContext.RunID, tool: invocation.Method},
				Reason: reason,
			}
		}
	}
	prepared, err := p.operation.prepare(invocation)
	if err != nil {
		return dispatchOutcome{}, err
	}
	call := Call{
		runID:   invocation.RunContext.RunID,
		attempt: attempt,
		tool:    prepared.decl.Name,
		callID:  callID,
		argHash: canonicalArgHash(string(invocation.RawParams)),
		facts: &callFacts{
			decl:              prepared.decl,
			args:              prepared.args,
			resources:         prepared.resources,
			resourceDeclared:  prepared.resourceDeclaration,
			invocation:        prepared.invocation,
			invocationEffects: prepared.invocationEffects,
			grant:             invocation.Grant,
			rawParams:         invocation.RawParams,
		},
	}
	// enteredCancelled is the caller's own context, read once: a context that
	// ended before this call reached the pipeline is not the pipeline's to
	// refuse (see Dispatch's comment).
	enteredCancelled := ctx.Err() != nil
	if refusal := cancelled(ctx, call, enteredCancelled, "before any policy decided it"); refusal != nil {
		return dispatchOutcome{}, refusal
	}
	asked, err := p.decide(ctx, call, nil, enteredCancelled)
	if err != nil {
		return dispatchOutcome{}, err
	}
	if asked != nil {
		if refusal := p.answer(ctx, call, *asked, enteredCancelled); refusal != nil {
			return dispatchOutcome{}, refusal
		}
		// A person's answer is not the last word (ADR-anntnxdj §2): the
		// authority the call goes on under is minted again from the policy as
		// it stands now, and the guards and the capability below read it.
		if p.chain.MintGrant != nil {
			if fresh, ok := p.chain.MintGrant(ctx, call); ok {
				call = call.withGrant(fresh)
				prepared.request.Grant = fresh
			}
		}
	}
	for _, guard := range p.chain.Guards {
		reason, refused := guard.Refuse(ctx, call)
		if !refused {
			continue
		}
		if reason == "" {
			reason = "a guard refused the call and gave no reason"
		}
		return dispatchOutcome{prepared: &prepared}, &PipelineRefusal{Call: call, Reason: reason}
	}
	if refusal := cancelled(ctx, call, enteredCancelled, "before the tool could run"); refusal != nil {
		return dispatchOutcome{}, refusal
	}
	if p.chain.Attempts != nil {
		if openErr := p.chain.Attempts.Open(ctx, call); openErr != nil {
			return dispatchOutcome{prepared: &prepared}, openErr
		}
	}
	beforeExecute := func(*preparedInvocation, agenttools.Capability) error {
		return cancelled(ctx, call, enteredCancelled, "immediately before execution")
	}
	outcome, err := p.operation.executePrepared(&prepared, call, beforeExecute, p.chain.execute)
	if err != nil {
		return outcome, err
	}
	outbound := Outbound{Text: outcome.output, WasError: outcome.runErr != nil}
	if outcome.runErr != nil {
		outbound.Text = outcome.runErr.Error()
	}
	for _, screen := range p.chain.Screens {
		if refusal := p.inspect(ctx, call, screen, outbound, enteredCancelled); refusal != nil {
			return dispatchOutcome{}, refusal
		}
	}
	// The result's own contract is checked AFTER the screens, and reported
	// only when they passed: an invalid result is still bytes that would have
	// left, and a screen that never saw it could not withhold material it
	// carries. What is reported names the tool and not the result
	// (ResultContractError).
	if outcome.resultErr != nil {
		return outcome, outcome.resultErr
	}
	return outcome, nil
}

// decide runs every PreCall in order. A Deny refuses the call at once. Every
// pending Ask contributes its reason and facts to one approval request, so an
// approval answers all questions rather than silently losing later questions.
func (p *Pipeline) decide(ctx context.Context, call Call, asked *Ask, enteredCancelled bool) (*Ask, error) {
	for _, precall := range p.chain.PreCalls {
		switch decision := precall.Decide(ctx, call).(type) {
		case Allow:
			// the call goes on to the next point
		case Deny:
			reason := decision.Reason
			if reason == "" {
				reason = "a policy point denied the call and gave no reason"
			}
			return nil, &PipelineRefusal{Call: call, Reason: reason, Cause: decision.Cause}
		case Ask:
			if asked == nil {
				asked = &Ask{Reason: decision.Reason, Facts: append([]string(nil), decision.Facts...)}
			} else {
				asked.Reason += "\n" + decision.Reason
				asked.Facts = append(asked.Facts, decision.Facts...)
			}
		default:
			return nil, &PipelineRefusal{Call: call, Reason: fmt.Sprintf("%T answered neither allow, deny nor ask, and an unanswered decision is a refusal", precall)}
		}
	}
	return asked, nil
}

// answer puts one PreCall's Ask to the chain's approver. No approver, an
// approver that could not reach anybody, a decline and a context that ended
// during the wait are all refusals: "no approver, or no answer, is a refusal",
// and a call that waited ten minutes does not run on a context that ended while
// it waited.
func (p *Pipeline) answer(ctx context.Context, call Call, ask Ask, enteredCancelled bool) error {
	given, refusal := p.put(ctx, call, ask, enteredCancelled)
	if refusal != nil {
		return refusal
	}
	switch answer := given.(type) {
	case Approved:
		return nil
	case Declined:
		reason := answer.Reason
		if reason == "" {
			reason = "the person declined the call"
		}
		// The calls queued behind this one are refused with the same reason
		// rather than started: a person who said no to this call did not say
		// yes to the next (ADR-anntnxdj §2).
		if p.chain.Order != nil {
			p.chain.Order.declinedBy(answer)
		}
		return &PipelineRefusal{Call: call, Reason: reason}
	case Unanswered:
		return &PipelineRefusal{Call: call, Reason: "nobody answered whether this call may run: " + errText(answer.Err), Cause: answer.Err}
	default:
		return &PipelineRefusal{Call: call, Reason: "nobody answered whether this call may run, and an unanswered ask is a refusal"}
	}
}

// put asks the chain's approver one question and returns its answer. A chain
// with no approver and a context that ended before or during the wait are
// refusals here, before any answer can be read: an Ask nobody could be shown
// authorises nothing. What an answer MEANS is the caller's, because a no to a
// PreCall and a no to a Screen are different facts about the run.
func (p *Pipeline) put(ctx context.Context, call Call, ask Ask, enteredCancelled bool) (Answer, error) {
	if refusal := cancelled(ctx, call, enteredCancelled, "before anyone could be asked"); refusal != nil {
		return nil, refusal
	}
	if p.chain.Approver == nil {
		return nil, &PipelineRefusal{
			Call:   call,
			Reason: fmt.Sprintf("nobody could be asked whether this call may run (%s): this chain has no approver, and an unanswered ask is a refusal", ask.Reason),
		}
	}
	given := p.chain.Approver.Answer(ctx, call, ask)
	if refusal := cancelled(ctx, call, enteredCancelled, "while it waited for an answer"); refusal != nil {
		return nil, refusal
	}
	return given, nil
}

// inspect runs one screen over the bytes that are about to leave. A screen
// decides whether they go, never what they say: Pass and an approved Ask release
// the exact result the tool produced, and Refuse withholds it.
func (p *Pipeline) inspect(ctx context.Context, call Call, screen Screen, outbound Outbound, enteredCancelled bool) error {
	switch verdict := screen.Inspect(ctx, call, outbound).(type) {
	case Pass:
		return nil
	case Refuse:
		reason := verdict.Reason
		if reason == "" {
			reason = "a screen refused the result and gave no reason"
		}
		return &PipelineRefusal{Call: call, Reason: reason}
	case Ask:
		given, refusal := p.put(ctx, call, verdict, enteredCancelled)
		if refusal != nil {
			return refusal
		}
		switch answer := given.(type) {
		case Approved:
			return nil
		case Declined:
			// The bytes ARE the call's result, and a no means they never
			// leave. There is nothing to continue the call with, so this
			// refusal carries a cause and the caller ends the run instead of
			// answering the call's slot (ScreenDeclinedError says why).
			reason := answer.Reason
			if reason == "" {
				reason = "the person declined to release the result"
			}
			if p.chain.Order != nil {
				p.chain.Order.declinedBy(answer)
			}
			return &PipelineRefusal{Call: call, Reason: reason, Cause: &ScreenDeclinedError{Reason: reason}}
		case Unanswered:
			return &PipelineRefusal{Call: call, Reason: "nobody answered whether this result may leave: " + errText(answer.Err), Cause: answer.Err}
		default:
			return &PipelineRefusal{Call: call, Reason: "nobody answered whether this result may leave, and an unanswered ask is a refusal"}
		}
	default:
		return &PipelineRefusal{Call: call, Reason: fmt.Sprintf("%T answered neither pass, refuse nor ask, and an unanswered verdict is a refusal", screen)}
	}
}

// cancelled is the pipeline's own observation of ctx, beside the one every point
// makes. A context that ends while the call is in the pipeline is a refusal
// that names when it ended, whether the point that would have run next noticed
// or not. A context that had already ended when the call arrived is its
// caller's own outcome to record, and is not refused here.
func cancelled(ctx context.Context, call Call, enteredCancelled bool, when string) error {
	if enteredCancelled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return &PipelineRefusal{Call: call, Reason: fmt.Sprintf("the call was cancelled %s: %v", when, err), Cause: err}
	}
	return nil
}

// errText renders an approver's failure, including the one that carries no error
// at all — "nobody answered" must never read as if somebody had.
func errText(err error) string {
	if err == nil {
		return "the approver failed without saying why"
	}
	return fmt.Sprint(err)
}
