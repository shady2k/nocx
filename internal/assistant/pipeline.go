package assistant

// Authority is a pipeline with named points around every tool call
// (ADR-anntnxdj decision 2). The pipeline owns what must not be skippable: the
// call's identity, schema validation before any policy, the declaration's
// narrowing, the one execution of the tool and the result's own contract. Each
// point owns one question, and its type says what its answer may be. No point
// receives a continuation, so none can skip, repeat or replace the execution:
// the pipeline calls the next point itself.
//
// This file is the seam later stages plug into. Today's callers still pass
// dispatch.go's ad-hoc transform, gate and beforeExecute steps; moving the tool
// endpoint and the kernel onto this pipeline is nocx-a62cl.2.2 and
// nocx-a62cl.2.3.

import (
	"context"
	"fmt"
)

// Call is one exact proposal, as every point sees it: the run, the attempt, the
// tool, the model's own id for the call and a hash of the canonical arguments.
// Every field is a copy taken when the call entered the pipeline and there is no
// way to change one afterwards, so what a person approved is what executes.
type Call struct {
	runID   string
	attempt int
	tool    string
	callID  string
	argHash string
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

// Decision is a PreCall's answer about one call. The interface is sealed, so
// the set is exactly Allow, Deny and Ask: no package outside this one can add a
// fourth answer that the pipeline would have to guess at.
type Decision interface{ decision() }

// Allow lets the call go on to the next point.
type Allow struct{}

// Deny refuses the call, and Reason says why in words a person and a model can
// both read.
type Deny struct{ Reason string }

// Ask says the call needs a person's answer before it may go on. Facts are what
// that person is shown beside the question — advisory evidence, never a
// decision. A Screen raises the same Ask about a result, so one question has one
// vocabulary whichever end of the call it is asked at.
type Ask struct {
	Reason string
	Facts  []string
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

// AuthorityChain is one kind of caller's points, in the order they run. An
// empty field means that kind of caller has no such point: a chain with no
// PreCall allows every call that reaches it, a chain with no approver refuses
// every Ask, and a chain with no Guard or Screen adds nothing.
type AuthorityChain struct {
	PreCalls []PreCall
	Approver Approver
	Guards   []Guard
	Screens  []Screen
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

// Dispatch runs one call through the chain, in the only order there is: schema,
// PreCall*, the approver on an Ask, Guard*, the declaration's Narrow, the tool
// exactly once, Screen*. Every point observes ctx, and a context that ended is
// a refusal wherever the pipeline notices it.
func (p *Pipeline) Dispatch(invocation ToolInvocation, attempt int, callID string) (string, error) {
	ctx := invocation.Context
	prepared, err := p.operation.prepare(invocation, nil)
	if err != nil {
		return "", err
	}
	call := Call{
		runID:   invocation.RunContext.RunID,
		attempt: attempt,
		tool:    prepared.decl.Name,
		callID:  callID,
		argHash: canonicalArgHash(string(invocation.RawParams)),
	}
	if refusal := cancelled(ctx, call, "before any policy decided it"); refusal != nil {
		return "", refusal
	}
	asked, err := p.decide(ctx, call)
	if err != nil {
		return "", err
	}
	if asked != nil {
		if refusal := p.answer(ctx, call, *asked); refusal != nil {
			return "", refusal
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
		return "", &PipelineRefusal{Call: call, Reason: reason}
	}
	if refusal := cancelled(ctx, call, "before the tool could run"); refusal != nil {
		return "", refusal
	}
	outcome, err := p.operation.executePrepared(&prepared, nil, nil)
	if err != nil {
		return "", err
	}
	outbound := Outbound{Text: outcome.output, WasError: outcome.runErr != nil}
	if outcome.runErr != nil {
		outbound.Text = outcome.runErr.Error()
	}
	for _, screen := range p.chain.Screens {
		if refusal := p.inspect(ctx, call, screen, outbound); refusal != nil {
			return "", refusal
		}
	}
	return outcome.output, outcome.runErr
}

// decide runs every PreCall in order. A Deny refuses the call at once. The first
// Ask is remembered and the remaining PreCalls still run, because a later point
// may refuse the call outright and a question nobody needs to answer is worse
// than none — at most one question is put to a person for one call.
func (p *Pipeline) decide(ctx context.Context, call Call) (*Ask, error) {
	var asked *Ask
	for _, precall := range p.chain.PreCalls {
		switch decision := precall.Decide(ctx, call).(type) {
		case Allow:
			// the call goes on to the next point
		case Deny:
			reason := decision.Reason
			if reason == "" {
				reason = "a policy point denied the call and gave no reason"
			}
			return nil, &PipelineRefusal{Call: call, Reason: reason}
		case Ask:
			if asked == nil {
				asked = &decision
			}
		default:
			return nil, &PipelineRefusal{Call: call, Reason: fmt.Sprintf("%T answered neither allow, deny nor ask, and an unanswered decision is a refusal", precall)}
		}
	}
	return asked, nil
}

// answer puts one Ask to the chain's approver. No approver, an approver that
// could not reach anybody, a decline and a context that ended during the wait
// are all refusals: "no approver, or no answer, is a refusal", and a call that
// waited ten minutes does not run on a context that ended while it waited.
func (p *Pipeline) answer(ctx context.Context, call Call, ask Ask) error {
	if refusal := cancelled(ctx, call, "before anyone could be asked"); refusal != nil {
		return refusal
	}
	if p.chain.Approver == nil {
		return &PipelineRefusal{
			Call:   call,
			Reason: fmt.Sprintf("nobody could be asked whether this call may run (%s): this chain has no approver, and an unanswered ask is a refusal", ask.Reason),
		}
	}
	given := p.chain.Approver.Answer(ctx, call, ask)
	if refusal := cancelled(ctx, call, "while it waited for an answer"); refusal != nil {
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
		return &PipelineRefusal{Call: call, Reason: reason}
	case Unanswered:
		return &PipelineRefusal{Call: call, Reason: "nobody answered whether this call may run: " + errText(answer.Err), Cause: answer.Err}
	default:
		return &PipelineRefusal{Call: call, Reason: "nobody answered whether this call may run, and an unanswered ask is a refusal"}
	}
}

// inspect runs one screen over the bytes that are about to leave. A screen
// decides whether they go, never what they say: Pass and an approved Ask release
// the exact result the tool produced, and Refuse withholds it.
func (p *Pipeline) inspect(ctx context.Context, call Call, screen Screen, outbound Outbound) error {
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
		return p.answer(ctx, call, verdict)
	default:
		return &PipelineRefusal{Call: call, Reason: fmt.Sprintf("%T answered neither pass, refuse nor ask, and an unanswered verdict is a refusal", screen)}
	}
}

// cancelled is the pipeline's own observation of ctx, beside the one every point
// makes. A call whose context ended is a refusal that names when it ended,
// whether the point that would have run next noticed or not.
func cancelled(ctx context.Context, call Call, when string) error {
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
