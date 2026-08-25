package assistant

// The tool-call pipeline (design §6, ADR-0028 decision 2), at eino's own
// seam — adk.ChatModelAgentMiddleware.WrapInvokableToolCall, called at
// request time with the tool's name, call id and arguments before it runs.
//
// This layer SEQUENCES AND ENFORCES; it does not implement. Masking has an
// owner, the audit has an owner, usage has an owner. What is ours, and only
// what is ours: the permit/ask/refuse decision, the attempt record before
// the call, the narrowed capability, and the batch latch.
//
// The order is the design's order, and two of its three invariants are
// stated with both ends:
//
//   - Validation precedes policy. A policy reading arguments that have not
//     been validated is deciding about something that may not be what
//     executes.
//   - The attempt exists from before the effect until the outcome or a
//     terminal reason is recorded. Not "the attempt is written before the
//     call" — that names a moment; the interval is the thing.
//   - A refusal is an answer (nocx-uvac6.1), not a fault. The policy's no
//     is returned as a TOOL RESULT in our words — a non-error string the
//     model reads in the refused call's own slot — and the run continues
//     until it reaches a terminal state of its own accord. The system
//     prompt promises this, and the promise is kept at this seam.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/masking"
)

// PolicyRefusalReason is WHY a call was refused — the branches decide takes,
// and no more: a closed set, because the product's sentence for a refusal is
// written per reason and an unnamed third one would silently fall back to
// the vaguest of them.
type PolicyRefusalReason string

const (
	// RefusedByDecision: the grant's policy matrix decides refuse for this
	// tool's effect class. Under an unbypassed declaration path the tool is
	// never offered to the model at all (ForGrant filters), so this is the
	// defense that holds if the declaration path is bypassed. A matrix row
	// is standing by nature: the person set it, and a retry of the same
	// call in the same turn is refused again.
	RefusedByDecision PolicyRefusalReason = "refused-by-decision"
	// RefusedOutOfScope: the call named a resource outside the SELECTED
	// effect's row scopes. This is the reachable one, and it is the owner's
	// screenshot: a model that invents a session id fails the exact identity
	// match (inScope, ResourceSession).
	RefusedOutOfScope PolicyRefusalReason = "refused-out-of-scope"
	// RefusedByPerson: the EXACT proposal was declined by the person
	// (nocx-uvac6.1). Decided by the declined-proposal check, not by
	// decide: the person's no to one call is a different sentence from the
	// policy's no, and the standing half of it ("in this session", "from
	// now on") is carried by the approval store's DeclineKind.
	RefusedByPerson PolicyRefusalReason = "refused-by-person"
)

// ToolFailedError is the FOURTH cause a run can end on, and the one the
// nocx-avogl.3 brief did not name: the policy permitted the call, the tool
// ran, and it failed. Its message is already the product's — the renderer's
// "could not capture the screen", a command that could not start — and until
// this type existed it reached the block only because the transport's default
// arm concatenated err.Error(), which is what dragged eino's
// "[NodeRunError] … node path: […]" onto the screen with it. Deleting that
// concatenation without this type would have silently swallowed a working
// sentence, so the sentence gets a carrier of its own instead.
//
// Unwrap returns the tool's error, so terminationReasonOf's lease check and
// every other errors.Is/As over the inner error are unchanged.
type ToolFailedError struct {
	Tool string
	Err  error
}

func (e *ToolFailedError) Error() string {
	return fmt.Sprintf("agent tool %q failed: %v", e.Tool, e.Err)
}

func (e *ToolFailedError) Unwrap() error { return e.Err }

// Message is the tool's own text, without this wrapper's framing: what the
// transport puts in front of a person. It is the EXECUTOR's string and never
// the framework's — eino wraps outside this error, never inside it.
func (e *ToolFailedError) Message() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

// ErrMalformedModelOutput marks a tool call that corresponds to no declared
// tool or whose arguments do not match the schema the model was shown. Not a
// refusal — there is nothing to call; the model produced output the engine
// cannot act on. Terminal, where a refusal is now a tool result.
var ErrMalformedModelOutput = errors.New("agent policy: malformed model output")

// ApprovalRequestedError is what Ask returns when the run suspended for
// human approval: the run is NOT failed — it is awaiting_approval, and
// Request is what the approval surface renders and the resume re-validates.
// It is exported because the transport renders it (nocx-z9hj4), matching the
// egress gate's EgressRequestedError: a person meets one kind of question.
type ApprovalRequestedError struct {
	Request *ApprovalRequest
}

func (e *ApprovalRequestedError) Error() string {
	if e.Request == nil {
		return "agent run suspended for approval"
	}
	return fmt.Sprintf("agent run suspended for approval: %s %s", e.Request.Tool, e.Request.CallID)
}

// ApprovalRequest is the user-facing ask (design §7.2): what was proposed,
// bound to the exact proposal. The surface shows it; the resume re-runs the
// pipeline and the approval record decides. It is also the interrupt state
// the checkpoint persists, so it is gob-registered: checkpoints are
// serialized, and an unregistered type fails the run at the suspension.
type ApprovalRequest struct {
	RunID     string `json:"runId"`
	Attempt   int    `json:"attempt"`
	Tool      string `json:"tool"`
	CallID    string `json:"callId"`
	Arguments string `json:"arguments"`
	// ArgHash is the canonical-argument hash of the binding (design §7.2):
	// the surface echoes it back on agent.approve so the decision names the
	// exact proposal. It is NOT derived from Arguments by the renderer —
	// the backend computes it once, and a changed argument must not resume
	// under the old approval.
	ArgHash string `json:"argHash"`
	// Effect is the effect class the gate decided on — the row a standing
	// answer writes. It is SENT rather than derived, because deriving it in
	// the renderer would be a rule keyed by a tool name in everything but
	// storage, which ADR-0028 decision 4 forbids.
	Effect content.Effect `json:"effect"`
	// Resource is what the gate matched the call against, or nil when the
	// call named none. A fact for the person reading the question; a
	// standing answer is over the effect, never over this.
	Resource *content.GrantScope `json:"resource,omitempty"`
	// EntryID is the ledger entry that recorded the proposal — what the
	// approved call runs as a SUBSEQUENT attempt of (ADR-0020 decision 4).
	// A carrier for the resume, never displayed.
	EntryID string `json:"-"`
}

func init() {
	gob.Register(ApprovalRequest{})
}

// attemptLedger is the slice of the ledger one tool attempt needs (design
// §6.4 — the attempt is durable, before the call). The full
// LedgerRepository is not the seam: a test must be able to fail exactly the
// write the invariant names (nocx-m4r3m's StartExecution) without
// implementing the other twenty methods.
type AttemptLedger interface {
	EnsureEnvironment(ctx context.Context, env content.Environment) error
	RecordObservation(ctx context.Context, obs content.Observation) (int64, error)
	Submit(ctx context.Context, in content.SubmitEntry) (content.SubmitResult, error)
	StartExecution(ctx context.Context, in content.StartExecution) (int64, error)
	FinishExecution(ctx context.Context, executionID int64, end content.FinishExecution) error
	// AddCause seats an entry this turn caused as the next child of the
	// turn's own entry (nocx-h1l4o, ADR-0040). The store assigns the seat;
	// see content.LedgerRepository.AddCause for why the caller may not.
	AddCause(ctx context.Context, turnID, causedID string) (int, error)
}

// maxArgsBytes bounds the model's argument JSON — the ingress size bound of
// design §6.2. A path is a few hundred bytes; anything larger is malformed.
const maxArgsBytes = 64 << 10

// maxToolResultBytes is the ingest bound of design §6.7, a defense for a
// tool that violates the window contract (§4.4: every tool that returns text
// returns a window) — files.read's window is filesReadWindowBytes, far below
// this.
const maxToolResultBytes = 1 << 20

// policyMiddleware is the pipeline for ONE run (one Ask): it holds the run's
// grant, the assembled registry, the ledger seam, the approval store, the
// egress vault comparison and the run's identity — everything the
// permit/ask/refuse decision, the attempt record and the egress gate need. A
// fresh instance per run; the grant is immutable once execution starts
// (ADR-0020 decision 5), and only a new attempt carries a different one.
type policyMiddleware struct {
	adk.BaseChatModelAgentMiddleware

	log       log.Logger
	grant     content.Grant
	registry  agenttools.Registry
	ledger    AttemptLedger
	approvals *ApprovalStore
	known     KnownMaterial
	runID     string
	attempt   int
	// turnEntryID is the TURN's own ledger entry — the thing every entry
	// this run causes is joined to (nocx-h1l4o, ADR-0039's closing
	// sentence).
	//
	// HOW A RUN ID MAPS TO ITS TURN, since the run id is what this
	// middleware has always carried: `executions.entry_id`. The run id IS
	// an execution row id (the transport formats rc.runID with strconv),
	// and an execution belongs to exactly one entry — the turn. The
	// transport holds BOTH facts side by side (rc.runID and rc.entryID, set
	// from the same SubmitAgentAsk result), so the turn arrives here as the
	// fact it already is rather than as a lookup: a read that recovers what
	// the caller was holding is a second derivation, and the two can only
	// ever disagree.
	//
	// EMPTY IS A REAL SHAPE and not a defect: an un-bound caller (AskParams
	// with no turn) causes entries that belong to no turn, and they are
	// recorded with no relation rather than with a guessed one — the same
	// rule the run id already follows on the attempt payload.
	turnEntryID string
	requester   RendererRequester
	// classifier is the second model that judges permitted proposals (bead
	// nocx-kpy23). Nil = not wired for this run: permitted calls run
	// exactly as they do without one. Consulted ONLY where the policy says
	// permit; every failure and every suspect verdict escalates.
	classifier CallClassifier
	// onCall announces a call that is ABOUT TO RUN (nocx-shxv0). It lives
	// here rather than in the engine's event loop because this is the only
	// place that holds all four facts at once: the declaration the gate
	// decided with, the resource namedResource derived from the validated
	// arguments, the ledger entry the attempt was recorded under, and the
	// knowledge that the call will actually execute. The engine's loop sees
	// only the messages, and would have to derive the resource a second
	// time from a raw arguments blob — the defect AGENTS.md spends a
	// section on.
	//
	// Nil is "nobody is listening", which is every non-transport caller.
	onCall     func(ToolCall) error
	validators map[string]*jsonschema.Schema
}

// newPolicyMiddleware builds the pipeline for one run. A schema that does
// not compile is a broken declaration — the run fails here, loudly, rather
// than at the call. requester is the renderer-request seam for
// Executes: InRenderer tools (design §6.6 — the only step that differs
// differs by the declaration row); it may be nil when no InRenderer tool can
// be reached under this build, which the run branch reports honestly.
//
// known is the egress vault comparison (design §7.1) and it is REQUIRED: a
// run that may execute tools must carry it, or the gate cannot see short
// vault values and a result would leave for the provider unscreened. The
// fail-closed check is here, at construction, so a wiring gap fails the run
// before any tool runs — never as a silent weaker gate.
//
// classifier is the second model that judges permitted proposals (bead
// nocx-kpy23); nil means not wired for this run — permitted calls run
// exactly as they do without one.
//
// onCall is the seam the visible tool call leaves through (nocx-shxv0);
// nil means nobody is listening and nothing is announced.
//
// turnEntryID is the turn every entry this run causes is joined to
// (nocx-h1l4o); empty is the un-bound caller shape and joins nothing.
// logger may be nil for the same callers — the only thing it reports is a
// relation that could not be written, which is a degrade the reader already
// handles.
func newPolicyMiddleware(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID string, attempt int, turnEntryID string, requester RendererRequester, classifier CallClassifier, onCall func(ToolCall) error) (*policyMiddleware, error) {
	if known == nil {
		return nil, errors.New("agent run: no egress vault comparison wired — a run that may execute tools must screen its results against known vault material (design §7.1)")
	}
	m := &policyMiddleware{
		log:         logger,
		grant:       grant,
		registry:    registry,
		ledger:      ledger,
		approvals:   approvals,
		known:       known,
		runID:       runID,
		attempt:     attempt,
		turnEntryID: turnEntryID,
		requester:   requester,
		classifier:  classifier,
		onCall:      onCall,
		validators:  make(map[string]*jsonschema.Schema, len(registry.All())),
	}
	for _, t := range registry.All() {
		v, err := compileToolSchema(t)
		if err != nil {
			return nil, err
		}
		m.validators[t.Name] = v
	}
	return m, nil
}

// compileToolSchema compiles one tool's params schema — the same file the
// model was shown — into the validator the middleware applies to the model's
// arguments (design §6.2: the model is a LESS trusted source than the
// renderer and gets the same discipline, never a weaker one).
func compileToolSchema(t agenttools.Tool) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(t.ParamsSchema))
	if err != nil {
		return nil, fmt.Errorf("tool %s: params schema: %w", t.Name, err)
	}
	url := "https://nocx.local/contracts/tools/" + t.Name + ".schema.json"
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(url, doc); addErr != nil {
		return nil, fmt.Errorf("tool %s: params schema: %w", t.Name, addErr)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("tool %s: params schema: %w", t.Name, err)
	}
	return s, nil
}

// BeforeAgent mints the batch latch for this run and installs it in the run
// context (the batch latch is ours, not the framework's — ADR-0028 decision
// 2). It runs on every Run AND every Resume: a resumed attempt is a new
// attempt with a fresh latch.
func (m *policyMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	return withLatch(ctx, &batchLatch{}), runCtx, nil
}

// WrapInvokableToolCall installs the pipeline on one tool call.
func (m *policyMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, rawArgs string, _ ...tool.Option) (string, error) {
		// The batch latch, before anything else: once a call in this model
		// response refused or escalated, every later call returns immediately
		// without calling next. sequentialRunToolCall loops every task and
		// never inspects tasks[i].err, so without the latch a refused call
		// would not stop the next one from running.
		if l := latchFrom(ctx); l != nil {
			if reason := l.tripped(); reason != nil {
				return "", m.deferred(ctx, tCtx, reason)
			}
		}

		// 1. Declaration lookup. A name absent from the registry is malformed
		// model output, not a refusal — there is nothing to call.
		decl, ok := m.registry.Lookup(tCtx.Name)
		if !ok {
			return "", fmt.Errorf("%w: unknown tool %q", ErrMalformedModelOutput, tCtx.Name)
		}

		// 2. Parameter validation against the tool's schema: the file the
		// model was shown, byte for byte, plus the ingress size bound.
		if len(rawArgs) > maxArgsBytes {
			return "", fmt.Errorf("%w: tool %q: arguments exceed the %d-byte bound", ErrMalformedModelOutput, decl.Name, maxArgsBytes)
		}
		args, err := m.validate(decl, rawArgs)
		if err != nil {
			return "", fmt.Errorf("%w: tool %q: %v", ErrMalformedModelOutput, decl.Name, err)
		}
		// The mechanical call classifier is deliberately after validation and
		// before every policy/approval/ledger path. Unlike the model classifier
		// below, it may lower a declared worst case: CommandEffect retains that
		// worst case for every disqualified command.
		decl = classifyCall(decl, args)

		// 3. Policy — permit / ask / refuse over the ADR-0020 lattice.
		//    FIRST, the person's own no (nocx-uvac6.1): the resume re-runs
		//    this very call through the pipeline, and the refusal is the
		//    call's result — the call must not run and must not be asked
		//    about again (the approval was answered; a re-ask would be the
		//    ask-forever loop the resume exists to end). Checked BEFORE
		//    decide, and the exact proposal FIRST: a standing no is answered
		//    with the person's own sentence, not the matrix's.
		if m.approvals != nil {
			if kind, declined := m.approvals.DeclinedKind(m.proposal(decl.Name, tCtx.CallID, rawArgs)); declined {
				return refusalResult(decl.Name, RefusedByPerson, kind), nil
			}
			// A STANDING no also answers any later call of the same effect
			// class in this run: "deny in this session" / "deny always"
			// said "this kind of call", so a retry of the same kind —
			// which mints a new call id and therefore misses the exact
			if kind, standing := m.approvals.DeclinedEffect(m.runID, decl.Effect); standing {
				return refusalResult(decl.Name, RefusedByPerson, kind), nil
			}
		}
		outcome, refusal := m.decide(decl, args)
		switch outcome {
		case policyRefuse:
			// (nocx-uvac6.1) The refusal IS the call's result: a tool
			// result with no error is text the model reads and answers —
			// the system prompt promises exactly this ("A refusal is an
			// answer"). ADR-0028 decision 2 previously treated refusal as
			// a terminal error; this is the deliberate refinement at the
			// SAME seam: the refusal category is still ours and still
			// precedes the attempt — it just returns the refusal as the
			// outcome instead of ending the run. No latch trip: the run
			// continues, and every other call in this response is decided
			// on its own merits.
			return refusalResult(decl.Name, refusal, ""), nil
		case policyAsk:
			// Approval binds to the exact proposal: an approved call skips
			// the ask; a changed argument hashes differently and does NOT
			// resume under the old approval (design §7.2).
			ap := m.proposal(decl.Name, tCtx.CallID, rawArgs)
			if m.approvals != nil && m.approvals.IsApproved(ap) {
				break // the exact proposal was approved; execute it
			}
			tripLatch(ctx, &ApprovalRequestedError{Request: m.request(decl, tCtx.CallID, rawArgs, args)})
			return "", m.escalate(ctx, decl, tCtx.CallID, rawArgs, args)
		}

		// 3b. The classifier (bead nocx-kpy23): a second, cheaper model
		// judges the proposed call and may only RAISE suspicion — permit →
		// ask — never lower it. Consulted ONLY where the policy says permit
		// (an ask or refuse cannot be changed by its verdict, and its
		// latency must stay off a path where a person is already waiting),
		// and skipped for the exact proposal a person already approved —
		// the approval covers the proposal INCLUDING its classification,
		// and consulting the classifier again on the approved resume could
		// ask forever. Failure is escalation, always: unreachable, timed
		// out, unparseable and role-unassigned each escalate, and the
		// classifier is never silently skipped.
		var classifierFact *classifierFact
		if m.classifier != nil && !m.proposalApproved(decl.Name, tCtx.CallID, rawArgs) {
			ask, fact, classifyErr := m.classifyProposal(ctx, decl, tCtx.CallID, rawArgs, args)
			if classifyErr != nil {
				// The classifier's INPUT gate could not see (the recognizer
				// failed closed): nothing decides this call unseen and
				// nothing leaves for the classifier — the run fails with a
				// terminal error, exactly as the result gate fails the run
				// when IT cannot see (step 7's screenErr path).
				return "", fmt.Errorf("agent tool %q: classifier gate: %w", decl.Name, err)
			}
			if ask != nil {
				tripLatch(ctx, &ApprovalRequestedError{Request: ask})
				return "", m.escalateClassifier(ctx, decl, tCtx.CallID, rawArgs, ask, fact)
			}
			classifierFact = fact
		}

		// 4. The attempt is written BEFORE the call. If that write fails, no
		// capability is constructed, next is not called, and the run fails
		// with a terminal infrastructure error — an interrupted run can
		// never be told "this may already have happened" when it cannot.
		execID, entryID, err := m.openAttempt(ctx, decl, tCtx.CallID, rawArgs, matchedResource(decl, args), classifierFact)
		if err != nil {
			return "", fmt.Errorf("agent tool %q: record attempt: %w", decl.Name, err)
		}

		// 5. The narrowed capability is constructed. The tool holds only
		// this; it cannot exceed the grant because it never has more
		// (ADR-0028 decision 4 — a check would leave it holding a full
		// manager). A tool with no Narrow is declared-but-not-executable
		// and is refused here, honestly.
		if decl.Narrow == nil {
			_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return "", fmt.Errorf("agent tool %q is declared but not executable: no capability constructor is wired", decl.Name)
		}
		capability, err := decl.Narrow(m.grant)
		if err != nil {
			_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return "", fmt.Errorf("agent tool %q: construct capability: %w", decl.Name, err)
		}
		// 5b. The call becomes VISIBLE (nocx-shxv0), here and not earlier
		// or later. Not earlier, because a call that is refused, malformed
		// or escalated has not happened — and an escalated one already has
		// a surface of its own, the approval prompt, which two surfaces
		// owning one input is exactly what AGENTS.md forbids. Not later,
		// because a person must see what the assistant is doing WHILE it
		// does it: a run tool's command can take a minute, and an account
		// written after the fact is what the owner saw on 2026-08-22 —
		// the block sitting below the answer written from it.
		//
		// It carries the arguments the tool is about to run on and the
		// resource namedResource derived from them, and never the result (see
		// ToolCall's doc for why the result is left off). The arguments are
		// the VALIDATED object from step 2, not the raw string: what is
		// announced is what ran, and step 2 is where "what ran" was settled.
		// Announced once per EXECUTION, so an approved egress resume — which
		// passes the same call through this pipeline a second time —
		// announces the same CallID again; the renderer keys on it and
		// renders one call.
		if m.onCall != nil {
			if err := m.onCall(ToolCall{
				Tool:       decl.Name,
				CallID:     tCtx.CallID,
				Args:       args,
				EntryID:    entryID,
				Effect:     decl.Effect,
				Resource:   matchedResource(decl, args),
				OpensBlock: decl.OpensBlock,
			}); err != nil {
				// The caller refused the write, which is the one thing that
				// stops a run: the same contract onEvent has for a delta.
				// The attempt is closed rather than left open — the
				// interval closes with a terminal reason, never silently.
				_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
				return "", err
			}
		}

		// 6. Execution — in Go, against the narrowed capability. An
		// APPROVED egress resume does not re-run the tool: the result that
		// was withheld and shown to the person is retained (design §7.1's
		// "send it as it is"), and re-running would repeat the effect and
		// could produce a different result than the one approved.
		out, runErr := m.runWithRetained(decl, tCtx.CallID, ctx, capability, []byte(rawArgs))

		// 7. Result ingest — the egress gate (design §7.1) FIRST, then the
		// window and the size bound. The gate screens EVERY return path
		// before the bytes leave for the provider, the success and the
		// error alike: an error string is output too — it carries paths,
		// hostnames and names, and a gate that screens successes and not
		// failures has closed the wide door and left the narrow one open.
		egress, screenErr := m.screenResult(ctx, out, runErr)
		if screenErr != nil {
			// Detection failed: the result is withheld and the run fails —
			// the masking service's fail-closed contract, and the gate's:
			// nothing leaves when the gate cannot see.
			_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return "", fmt.Errorf("agent tool %q: egress screening failed — the result was withheld: %w", decl.Name, screenErr)
		}
		if len(egress) > 0 {
			ap := m.proposal(decl.Name, tCtx.CallID, rawArgs)
			// The approved egress resume: the EXACT result the person
			// approved sending is what was screened. Nothing re-ran; the
			// bytes go as decided and the retention is dropped. An
			// approval of the POLICY gate is not an approval of this gate —
			// a call approved at the policy step whose result carries a
			// finding still suspends here (design §7.3: two gates, one
			// surface; each asks once).
			approvedResume := false
			if m.approvals != nil {
				if _, _, retained := m.approvals.RetainedResult(ap); retained {
					m.approvals.ClearRetained(ap)
					approvedResume = true
				}
			}
			if !approvedResume {
				// A finding REFUSES AND ASKS (design §7.1): nothing is
				// sent, the run suspends carrying the findings, and a
				// person is shown what was found and where. It never
				// silently masks and continues — off-machine a miss is
				// invisible and permanent, and an honest redaction that
				// says nothing is indistinguishable from there having
				// been nothing to redact (ADR-0021). The ask binds to the
				// exact proposal through the existing approval machinery;
				// the run is NOT failed — it is awaiting the decision the
				// surface renders.
				req := m.egressRequest(decl, tCtx.CallID, rawArgs, args, egress, runErr != nil)
				if m.approvals != nil {
					ap.EntryID = entryID
					m.approvals.Request(ap)
					// The withheld result is retained so the approved
					// resume sends the EXACT bytes the person was shown —
					// never a re-run's freshly produced ones.
					m.approvals.Retain(ap, out, runErr != nil)
				}
				tripLatch(ctx, &EgressRequestedError{Request: req})
				// The attempt of THIS pass closes as interrupted: the call
				// ran and its result was withheld pending the decision; the
				// approved call is a SUBSEQUENT attempt of the same entry.
				_ = m.closeAttempt(ctx, execID, content.TermInterrupted, content.EntryInterrupted)
				return "", compose.StatefulInterrupt(ctx, req, req)
			}
		}

		// 8. The outcome is recorded on the attempt — the interval closes
		// with the outcome or the terminal reason, never before.
		if runErr != nil {
			_ = m.closeAttempt(ctx, execID, terminationReasonOf(runErr), content.EntryFailure)
			// Named, so the transport can say WHICH tool failed without
			// stringifying the framework's wrapper around it.
			return "", &ToolFailedError{Tool: decl.Name, Err: runErr}
		}

		if len(out) > maxToolResultBytes {
			_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return "", fmt.Errorf("agent tool %q: result exceeds the %d-byte bound — a tool that returns text must return a window (design §4.4)", decl.Name, maxToolResultBytes)
		}

		// The window and the size bound. The executor windows its own
		// return (design §4.4); this is the bound that holds even when a
		// tool forgets.
		if len(out) > maxToolResultBytes {
			_ = m.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return "", fmt.Errorf("agent tool %q: result exceeds the %d-byte bound — a tool that returns text must return a window (design §4.4)", decl.Name, maxToolResultBytes)
		}
		if err := m.closeAttempt(ctx, execID, content.TermCompleted, content.EntrySuccess); err != nil {
			return "", fmt.Errorf("agent tool %q: record outcome: %w", decl.Name, err)
		}
		return decl.FrameToolResult(out), nil
	}, nil
}

// validate applies the tool's compiled schema to the model's raw arguments.
// The result is the parsed object the policy evaluates — the same object the
// executor will receive, so the policy never decides about something that
// may not be what executes.
func (m *policyMiddleware) validate(decl agenttools.Tool, raw string) (map[string]any, error) {
	v := m.validators[decl.Name]
	if v == nil {
		return nil, errors.New("no validator compiled for this tool")
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("arguments are not JSON: %w", err)
	}
	if err := v.Validate(doc); err != nil {
		return nil, fmt.Errorf("arguments do not match the schema the model was shown: %w", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("arguments are not an object")
	}
	return obj, nil
}

// classifyCall is the one proposal-to-effect conversion. A declaration may
// name the validated argument carrying a shell command; every other tool keeps
// its declared effect. The registry owns which tools carry commands, so this
// path does not grow a second tool-name table.
func classifyCall(decl agenttools.Tool, args map[string]any) agenttools.Tool {
	if decl.CommandArg == "" {
		return decl
	}
	command, ok := args[decl.CommandArg].(string)
	if !ok {
		return decl
	}
	decl.Effect = CommandEffect(command, decl.Effect)
	return decl
}

// ── policy ────────────────────────────────────────────────────────────────

type policyOutcome int

const (
	policyPermit policyOutcome = iota
	policyAsk
	policyRefuse
)

// decide is the permit/ask/refuse function over the ADR-0020 lattice
// (decision 6) as amended (2026-08-16, amendment pending owner approval):
// the grant's policy MATRIX carries one decision per effect class, and two
// rules hold under any matrix — a call naming a resource outside the
// SELECTED EFFECT's row scopes is refused (the tool's contract is "within
// the grant's paths"; widening the grant is a NEW grant on a NEW attempt,
// never a mid-run question), and an effect the matrix refuses is refused
// (the tool should never have been declared under this grant — ForGrant
// filters — and this is the defense that holds if the declaration path is
// bypassed).
//
// The second return value is meaningful ONLY for policyRefuse: it is the
// reason, and it exists because the product must tell a person WHY a call did
// not run. It is returned from here rather than re-derived at the transport —
// the two branches below are the whole of "why", and a second derivation of
// one question is the defect AGENTS.md spends a section on.
func (m *policyMiddleware) decide(t agenttools.Tool, args map[string]any) (policyOutcome, PolicyRefusalReason) {
	if m.grant.Policy.DecisionFor(t.Effect) == content.DecisionRefuse {
		return policyRefuse, RefusedByDecision
	}
	if !m.inScope(t, args) {
		return policyRefuse, RefusedOutOfScope
	}
	switch m.grant.Policy.DecisionFor(t.Effect) {
	case content.DecisionPermit:
		return policyPermit, ""
	default:
		// Unstated rows, and a grant without a matrix, decide ASK: the
		// fail-toward-asking default (an empty matrix is a policy that
		// asks), and a silent permit is how a feature that was never
		// configured survives a release.
		return policyAsk, ""
	}
}

// inScope is the policy's scope check: the resource the call names must be
// inside the SELECTED EFFECT's row scopes. The run bound was folded into
// every row at mint (EffectPolicy.WithRunScopes), so the row is the whole
// resource authority here; Grant.Scopes is deliberately NOT consulted — it
// is the derived all-rows union that exists for the declaration filter's
// resource-kind coverage, and a decision that consulted it would let one
// effect's scopes leak into another effect's call (an observe row scoped to
// /home refusing /etc nowhere it matters). This is NOT the enforcement — the
// capability is the enforcement (ADR-0028 decision 4) — and it is
// deliberately the advisory lexical approximation of it: the capability
// resolves canonical identity, the policy compares the spelled path. A call
// this check lets through can still be refused by the capability; a call it
// refuses never reaches the capability.
func (m *policyMiddleware) inScope(t agenttools.Tool, args map[string]any) bool {
	named, declares := namedResource(t, args)
	if !declares {
		// The tool names no resource in its parameters; its scope is the
		// grant's own scope for the kinds it declares.
		return true
	}
	if named == nil {
		return false // validation already required it; refuse to be sure
	}
	for _, s := range m.grant.Policy.RowScopes(t.Effect) {
		// A path scope is a lexical containment test (pathUnder — both ends
		// absolute); a session scope is an exact identity match: the
		// spelled sessionId IS the resource, there is no containment to
		// approximate. A call this check lets through can still be refused
		// by the capability; a call it refuses never reaches it.
		switch s.Kind {
		case content.ResourcePath:
			if pathUnder(named.ID, s.ID) {
				return true
			}
		case content.ResourceSession:
			if named.ID == s.ID {
				return true
			}
		}
	}
	return false
}

// namedResource is the ONE derivation of "which argument names the resource
// this call touches, and what did the call spell there". Two callers ask it:
// the scope check, which compares the answer against the row's scopes, and
// the approval ask, which shows the answer to the person. They were written
// as one deliberately — a second derivation of a single question agrees
// everywhere anyone looks and disagrees somewhere nobody did, which is the
// defect AGENTS.md spends a section on.
//
// declares is false only when the tool names no resource in its parameters
// at ALL: git.status's repository IS the grant's path scope, so there is
// nothing to compare and nothing to show. It is true with a nil scope when
// the declared argument is absent or not a string — validation already
// required it, so the scope check refuses rather than guessing, and the ask
// shows no resource. The kind is the DECLARATION's, never inferred from the
// value: a path and a session id are both strings.
func namedResource(t agenttools.Tool, args map[string]any) (named *content.GrantScope, declares bool) {
	if t.ResourceArg == "" {
		return nil, false
	}
	id, ok := args[t.ResourceArg].(string)
	if !ok {
		return nil, true
	}
	var kind content.ResourceKind
	if len(t.Resources) > 0 {
		kind = t.Resources[0]
	}
	return &content.GrantScope{Kind: kind, ID: id}, true
}

// matchedResource is what the ask carries: the resource the call named, or
// nil when it named none. The wire declares kind and id both non-empty, so a
// half-named resource is no resource — never a scope with an empty half.
func matchedResource(t agenttools.Tool, args map[string]any) *content.GrantScope {
	named, _ := namedResource(t, args)
	if named == nil || named.Kind == "" || named.ID == "" {
		return nil
	}
	return named
}

// pathUnder is the lexical containment test of the policy's scope check: the
// path is the spelled argument, the scope is the grant's spelled scope. Both
// ends are absolute; the capability's canonical check is what actually
// decides whether the read happens.
func pathUnder(path, scope string) bool {
	if scope == "" {
		return false
	}
	if path == scope {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(scope, "/")+"/")
}

// ── the ask and the latch ─────────────────────────────────────────────────

// escalate suspends the run BEFORE next — the call that is asking has not
// run, and no call after it in this model response will. The escalation is
// RECORDED, not only held in memory (nocx-5dldy): the proposal put to a
// person is an action entry in the ledger with its own interrupted attempt —
// "the proposal, the decision, the attempt and the result are one readable
// thread" — and the approved call runs as a SUBSEQUENT attempt of that same
// entry (ADR-0020 decision 4: a retry after approval is an execution of the
// same intent, never a new intent). The persisted interrupt state is the
// proposal itself: the resume re-runs the pipeline and the approval record
// decides whether the exact proposal may execute.
func (m *policyMiddleware) escalate(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, args map[string]any) error {
	ap := m.proposal(decl.Name, callID, rawArgs)
	var entryID string
	if m.ledger != nil {
		id, err := m.recordProposal(ctx, decl, rawArgs, matchedResource(decl, args), ap, nil)
		if err != nil {
			// The proposal could not be recorded: the run fails rather than
			// asking a question whose answer would resume nothing — a
			// question with no thread is the hole the epic names.
			return err
		}
		entryID = id
	}
	req := m.request(decl, callID, rawArgs, args)
	req.ArgHash = ap.ArgHash
	req.EntryID = entryID
	if m.approvals != nil {
		ap.EntryID = entryID
		m.approvals.Request(ap)
	}
	return compose.StatefulInterrupt(ctx, req, req)
}

// escalateClassifier is escalate with the classifier's ledger fact: the
// ask the classifier caused — suspect, failed, or its input withheld by
// the egress gate — suspends the run BEFORE next, is RECORDED with the
// classifier block on the proposal (criterion 6: "why was this asked" is
// answerable from the ledger), and resumes through the SAME approval
// machinery as a policy ask: the person's yes covers the proposal
// INCLUDING its classification, and the resume skips a second
// consultation (the loop property).
func (m *policyMiddleware) escalateClassifier(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, ask *ApprovalRequest, fact *classifierFact) error {
	ap := m.proposal(decl.Name, callID, rawArgs)
	var entryID string
	if m.ledger != nil {
		// The classifier arm's ask already carries the SAME derivation
		// (request() built it with matchedResource), so the record takes it
		// off the ask rather than deriving it a second time.
		id, err := m.recordProposal(ctx, decl, rawArgs, ask.Resource, ap, fact)
		if err != nil {
			return err
		}
		entryID = id
	}
	ask.ArgHash = ap.ArgHash
	ask.EntryID = entryID
	if m.approvals != nil {
		ap.EntryID = entryID
		m.approvals.Request(ap)
	}
	return compose.StatefulInterrupt(ctx, ask, ask)
}

// request builds the ask BOTH escalation sites send — the policy arm and the
// classifier arm alike. It takes the DECLARATION, not the tool name, because
// the effect and the resource come off the declaration the gate just decided
// with: one builder is what keeps a classifier ask from reaching the surface
// without an effect, which the notification's schema requires.
func (m *policyMiddleware) request(decl agenttools.Tool, callID, rawArgs string, args map[string]any) *ApprovalRequest {
	return &ApprovalRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		Effect:    decl.Effect,
		Resource:  matchedResource(decl, args),
	}
}

func (m *policyMiddleware) proposal(toolName, callID, rawArgs string) Approval {
	return Approval{
		RunID:   m.runID,
		Attempt: m.attempt,
		Tool:    toolName,
		CallID:  callID,
		ArgHash: canonicalArgHash(rawArgs),
	}
}

// screenResult runs the egress gate (design §7.1) over one tool's return —
// the success output or the error string alike — and returns the findings.
// Two detectors contribute, and both are the gate's, not second
// implementations (one recognizer, two policies): the masking service's
// heuristic pass, and the vault comparison through the run's KnownMaterial
// seam. A detection failure is an error: the caller withholds the result
// and fails the run — nothing leaves when the gate cannot see.
func (m *policyMiddleware) screenResult(ctx context.Context, out string, runErr error) ([]EgressFinding, error) {
	result := out
	if runErr != nil {
		result = runErr.Error()
	}
	heuristic, err := masking.Detect(result)
	if err != nil {
		return nil, err
	}
	findings := make([]EgressFinding, 0, len(heuristic))
	for _, f := range heuristic {
		findings = append(findings, EgressFinding{
			Source: EgressFindingHeuristic,
			Kind:   f.Kind,
			Start:  f.Start,
			End:    f.End,
		})
	}
	known, err := m.known.FindKnown(ctx, result)
	if err != nil {
		return nil, err
	}
	for _, k := range known {
		findings = append(findings, EgressFinding{
			Source:     EgressFindingKnown,
			SecretName: k.SecretName,
			Start:      k.Start,
			End:        k.End,
		})
	}
	if len(findings) == 0 {
		return nil, nil
	}
	return findings, nil
}

// egressRequest builds the egress ask. It carries the effect and the resource
// too, off the same declaration and by the same derivation as a policy ask:
// the surface offers only allow/deny once here, but the notification is ONE
// shape on the wire, and a required field absent on one path is how a schema
// stops being a contract.
func (m *policyMiddleware) egressRequest(decl agenttools.Tool, callID, rawArgs string, args map[string]any, findings []EgressFinding, wasError bool) *EgressRequest {
	return &EgressRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		ArgHash:   canonicalArgHash(rawArgs),
		Effect:    decl.Effect,
		Resource:  matchedResource(decl, args),
		Findings:  findings,
		WasError:  wasError,
	}
}

// canonicalArgHash hashes the CANONICAL form of the arguments — JSON with
// sorted object keys — so a re-serialized equivalent is the same proposal.
func canonicalArgHash(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		h := sha256.Sum256([]byte(raw)) // unparseable: hash the bytes as-is
		return hex.EncodeToString(h[:])
	}
	b, err := json.Marshal(v)
	if err != nil {
		h := sha256.Sum256([]byte(raw))
		return hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// deferred returns what a latched call returns: an interrupt error, so the
// batch still suspends cleanly when the trigger was an escalation (a plain
// error here would fail the run instead of suspending it). Only
// ESCALATIONS trip the latch now — a refusal is one call's answer, not the
// batch's (nocx-uvac6.1) — so the deferred shape is the interrupt and
// nothing else. Either way: next is not called, the tool does not run, and
// the human is told the truth about the call that asked.
func (m *policyMiddleware) deferred(ctx context.Context, tCtx *adk.ToolContext, reason error) error {
	info := fmt.Sprintf("call %q (%s) did not run: a prior call in this response %v", tCtx.Name, tCtx.CallID, reason)
	return compose.Interrupt(ctx, info)
}

// refusalResult is the TOOL RESULT a refused call returns to the model
// (nocx-uvac6.1): the refusal is an answer, not a fault. The system prompt
// promises exactly this — "A refusal is an answer: say what you could not do
// and what you would need" — and a non-error tool result is how the promise
// is kept: the refused call's own slot in the conversation carries the
// refusal, the run continues, and the model answers in words, or proposes
// something else. The text is OURS, written per reason (nocx-avogl.3: never
// the framework's stringification), and it says nothing the policy keeps
// from the person — no effect lattice, no scope list, no error internals.
//
// kind is the declined proposal's standing half (the person's own no); empty
// for a policy refusal, which is standing only when the matrix row says so.
func refusalResult(tool string, reason PolicyRefusalReason, kind DeclineKind) string {
	switch reason {
	case RefusedOutOfScope:
		return "REFUSED: nocx did not run your call to " + tool + ": it named something outside what this question is allowed to reach. Say what you wanted in words, or propose a call within what you were given — never a different spelling of the same call."
	case RefusedByPerson:
		switch kind {
		case DeclineCallSession:
			return "REFUSED: the person declined your call to " + tool + ", and refused this kind of call in this session. Do not propose it again in this session."
		case DeclineCallAlways:
			return "REFUSED: the person declined your call to " + tool + ", and refused this kind of call from now on. Do not propose it again."
		default:
			return "REFUSED: the person declined your call to " + tool + " — it did not run. Say what you needed in words instead."
		}
	default: // RefusedByDecision — the matrix row, standing by nature
		return "REFUSED: nocx did not run your call to " + tool + ": this kind of action is refused by the policy this question runs under, and that refusal stands. Do not propose it again, or try a different spelling of the same call."
	}
}

// approvalRequestFrom finds the pipeline's own ask among an interrupt
// event's contexts: the asking call carries our *ApprovalRequest as its
// info; the latched, deferred calls carry a plain string ("a prior call
// ..."). The first ask is the one the human decides about.
//
// It returns the interrupt's ID with it — adk's fully-qualified address of
// THIS suspended branch — because that is what a resume must name
// (adk.ResumeParams.Targets). The two facts come off one context and are
// found by one walk: a request whose branch we could not name is a
// suspension nobody could ever answer.
func approvalRequestFrom(info *adk.InterruptInfo) (*ApprovalRequest, string) {
	if info == nil {
		return nil, ""
	}
	for _, ic := range info.InterruptContexts {
		if req, ok := ic.Info.(*ApprovalRequest); ok {
			return req, ic.ID
		}
	}
	return nil, ""
}

// ── the attempt ───────────────────────────────────────────────────────────

// recordProposal writes the escalation's ledger facts BEFORE the run
// suspends (nocx-5dldy: "an escalation is recorded, not only held in
// memory"): the proposal is an action entry whose payload names the exact
// binding — tool, effect, arguments, and the run/attempt/callId/argHash the
// approval store keys — and its own attempt, closed interrupted: the call
// that is asking has NOT run (the escalation is before next). The approved
// call runs as a SUBSEQUENT attempt of this same entry. A failed write
// fails the run: a question whose answer would resume nothing is the hole
// the thread criterion exists to close.
func (m *policyMiddleware) recordProposal(ctx context.Context, decl agenttools.Tool, rawArgs string, resource *content.GrantScope, ap Approval, fact *classifierFact) (string, error) {
	envID := content.EnvironmentIDFor(content.EnvLocal, "")
	if err := m.ledger.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		return "", fmt.Errorf("proposal environment: %w", err)
	}
	if _, err := m.ledger.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID,
		Criticality:   content.CriticalityRoutine,
	}); err != nil {
		return "", fmt.Errorf("proposal observation: %w", err)
	}
	payloadBody := map[string]any{
		"tool":   decl.Name,
		"effect": decl.Effect,
		"args":   json.RawMessage(rawArgs),
		"approval": map[string]any{
			"runId":   ap.RunID,
			"attempt": ap.Attempt,
			"tool":    ap.Tool,
			"callId":  ap.CallID,
			"argHash": ap.ArgHash,
		},
	}
	// The resource the call named, derived ONCE (matchedResource, shared
	// with the scope check, the approval prompt and the visible announcement
	// of the call) and stored with the proposal. A restored turn draws the
	// call from the record; without the resource on the record the renderer
	// would have to derive it from the raw arguments a second time, which
	// is the defect AGENTS.md spends a section on. Absent when the tool
	// names no resource at all — never an empty scope.
	if resource != nil {
		payloadBody["resource"] = resource
	}
	// The classifier block (bead nocx-kpy23, criterion 6): when this
	// escalation was caused by the classifier — suspect, failed, or an
	// input the gate withheld — the reason lives on the PROPOSAL, so "why
	// was this asked" is answerable from the ledger, not from a log.
	if fact != nil {
		payloadBody["classifier"] = fact
	}
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return "", fmt.Errorf("proposal payload: %w", err)
	}
	res, err := m.ledger.Submit(ctx, content.SubmitEntry{
		ID:            uuid.NewString(),
		Client:        "agent",
		EnvironmentID: envID,
		Cwd:           "/",
		Kind:          content.EntryAction,
		// The assistant's call was submitted by the assistant, and approval
		// does not change that: a person who allows the call authorised
		// somebody else's intent, they did not submit it (schemaV1's
		// source comment). Naming it here is what keeps the store's
		// empty→user default — the ordinary shell path — off agent rows.
		Source:  content.SourceAssistant,
		Intent:  decl.Name,
		Payload: string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("proposal submit: %w", err)
	}
	// The proposal is a thing the turn caused, exactly as a granted call is:
	// a person was asked something at this point in the answer, and a
	// restored turn shows it where it happened.
	m.noteCause(ctx, res.ID)
	execID, err := m.ledger.StartExecution(ctx, content.StartExecution{
		EntryID:  res.ID,
		Attempt:  1, // the escalation itself: recorded, never run
		Executor: new("agent"),
		Grant:    &m.grant,
	})
	if err != nil {
		return "", fmt.Errorf("proposal start execution: %w", err)
	}
	if err := m.ledger.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt:           time.Now().UnixMilli(),
		TerminationReason: content.TermInterrupted,
		Status:            content.EntryInterrupted,
	}); err != nil {
		return "", fmt.Errorf("proposal close: %w", err)
	}
	return res.ID, nil
}

// openAttempt writes the durable attempt BEFORE the call: the environment,
// the action entry (the audit row — kind='action', design §3.2) and the
// execution that records the grant. The entry's payload names the tool, the
// effect and the arguments, and — when the middleware holds a run id (the
// transport's ask always does) — the run id, so a granted call's attempt
// joins its run's thread exactly as an escalated call's approval block does
// (nocx-dw3.4). The grant recorded is the run's grant: "what was this
// allowed to do" is a query over the record, not a reconstruction
// (ADR-0020 decision 5).
//
// An APPROVED call does not create a new intent: it runs as its own
// SUBSEQUENT attempt of the proposal's own entry (ADR-0020 decision 4,
// nocx-5dldy) — the entry the escalation recorded, found through the
// approval store. The returned entryID is what the egress gate's request
// carries into the store, so the same rule holds for a finding's approval.
func (m *policyMiddleware) openAttempt(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, resource *content.GrantScope, classifierFact *classifierFact) (int64, string, error) {
	if m.ledger == nil {
		return 0, "", errors.New("no attempt ledger wired — a tool call may not run without a durable attempt (design §6.4)")
	}
	envID := content.EnvironmentIDFor(content.EnvLocal, "")
	if err := m.ledger.EnsureEnvironment(ctx, content.Environment{ID: envID, Kind: content.EnvLocal}); err != nil {
		return 0, "", fmt.Errorf("environment: %w", err)
	}
	if _, err := m.ledger.RecordObservation(ctx, content.Observation{
		EnvironmentID: envID,
		Criticality:   content.CriticalityRoutine,
	}); err != nil {
		return 0, "", fmt.Errorf("observation: %w", err)
	}

	entryID := ""
	attempt := 1
	if m.approvals != nil {
		if id, ok := m.approvals.EntryIDFor(m.proposal(decl.Name, callID, rawArgs)); ok {
			entryID = id
			attempt = 2 // the approved call is the escalation's subsequent attempt
		}
	}
	if entryID == "" {
		payloadBody := map[string]any{
			"tool":   decl.Name,
			"effect": decl.Effect,
			"args":   json.RawMessage(rawArgs),
		}
		// The run id joins the attempt to its run (nocx-dw3.4). Where the
		// grant permits the call and nobody is asked, the ledger is the
		// ONLY account of what happened — so the attempt carries the run it
		// happened in, and a reader joins question, run, attempt and answer
		// into one thread exactly as the approval block does for an
		// escalated call. Empty (the un-bound caller shape, AskParams) is
		// recorded as no link rather than a misleading empty one.
		if m.runID != "" {
			payloadBody["runId"] = m.runID
		}
		// The resource the call named, derived ONCE (matchedResource) and
		// stored so a RESTORED turn can draw this call's line without the
		// renderer deriving it a second time from the arguments blob. See
		// recordProposal for the same field on the escalation's own entry.
		if resource != nil {
			payloadBody["resource"] = resource
		}
		// Whether this call's work becomes a top-level BLOCK — the
		// declaration's own fact (nocx-9sqii). Stored with the attempt so a
		// restored turn knows the block is the account of this call and
		// draws no line beside it; the reader must not match the tool name,
		// which would be a second copy of the tool table.
		if decl.OpensBlock {
			payloadBody["opensBlock"] = true
		}
		// The classifier block (bead nocx-kpy23, criterion 6): when the
		// classifier was consulted and cleared the call, the attempt's own
		// record carries the verdict and the model, so the audit shows
		// which model saw the call and said clear. Without a classifier the
		// payload is tool, effect, args and the run id — nothing else.
		if classifierFact != nil {
			payloadBody["classifier"] = classifierFact
		}
		payload, err := json.Marshal(payloadBody)
		if err != nil {
			return 0, "", fmt.Errorf("payload: %w", err)
		}
		res, err := m.ledger.Submit(ctx, content.SubmitEntry{
			ID:            uuid.NewString(),
			Client:        "agent",
			EnvironmentID: envID,
			Cwd:           "/",
			Kind:          content.EntryAction,
			Source:        content.SourceAssistant,
			Intent:        decl.Name,
			Payload:       string(payload),
		})
		if err != nil {
			return 0, "", fmt.Errorf("submit: %w", err)
		}
		entryID = res.ID
		// A new intent is a new cause of this turn. An APPROVED call is
		// NOT: it runs as a subsequent attempt of the proposal's entry,
		// which took its position when the person was asked — joining it
		// again would move it to after everything that followed the
		// question. (AddCause is idempotent on the pair, so this is belt
		// and braces rather than the only guard.)
		m.noteCause(ctx, entryID)
	}
	execID, err := m.ledger.StartExecution(ctx, content.StartExecution{
		EntryID:  entryID,
		Attempt:  attempt,
		Executor: new("agent"),
		Grant:    &m.grant,
	})
	if err != nil {
		return 0, "", fmt.Errorf("start execution: %w", err)
	}
	return execID, entryID, nil
}

// runWithRetained executes the tool — unless the EXACT proposal's result is
// retained from an egress suspension (design §7.1): then the withheld bytes
// the person approved sending are returned instead of re-running the tool,
// which would repeat the effect and could produce a different result than
// the one approved.
func (m *policyMiddleware) runWithRetained(decl agenttools.Tool, callID string, ctx context.Context, capability agenttools.Capability, rawArgs []byte) (string, error) {
	if m.approvals != nil {
		if out, wasError, ok := m.approvals.RetainedResult(m.proposal(decl.Name, callID, string(rawArgs))); ok {
			if wasError {
				return "", errors.New(out)
			}
			return out, nil
		}
	}
	return m.run(decl, ctx, capability, rawArgs)
}

// noteCause seats one entry this turn caused under the turn (nocx-h1l4o,
// ADR-0040): the command a `run` call opened, the action entry of any other
// call, the proposal entry of an escalation. The seat inside the turn is the
// store's — see content.LedgerRepository.AddCause.
//
// A TURN THAT IS NOT THERE JOINS NOTHING. The un-bound caller shape has no
// turn, and an entry with no cause is recorded with no relation rather than
// with a guessed one: the reader's answer for a missing relation is plain
// ledger order and an independent agent block, which is honest, where a
// guessed parent is not.
//
// A FAILED WRITE NEVER FAILS THE CALL, and this is the one deliberate
// asymmetry with the attempt beside it. The attempt is the RECORD — that the
// call happened at all — and a record that cannot be written stops the run.
// The edge is the ARRANGEMENT — where a reader draws it — and its absence is
// a state the restore already handles. Failing a call to preserve a drawing
// order would trade a real capability for a cosmetic one, and for the run
// tool it would fail AFTER the command had already run.
func (m *policyMiddleware) noteCause(ctx context.Context, causedEntryID string) {
	if m.ledger == nil || m.turnEntryID == "" || causedEntryID == "" {
		return
	}
	if _, err := m.ledger.AddCause(ctx, m.turnEntryID, causedEntryID); err != nil && m.log != nil {
		m.log.Warn("agent run: the containment relation could not be recorded — this entry will restore in plain ledger order",
			"turn", m.turnEntryID, "caused", causedEntryID, "error", err)
	}
}

// closeAttempt records the outcome on the attempt — the closing event of the
// interval "the attempt exists from before the effect until the outcome or a
// terminal reason is recorded".
func (m *policyMiddleware) closeAttempt(ctx context.Context, execID int64, reason content.TerminationReason, status content.EntryStatus) error {
	if m.ledger == nil {
		return nil
	}
	return m.ledger.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt:           time.Now().UnixMilli(),
		TerminationReason: reason,
		Status:            status,
	})
}

// terminationReasonOf maps a tool call's terminal error to the reason the
// attempt records. A run the LEASE terminalized (ADR-0020 decision 2) is
// the one case where the generic "failed" is the wrong fact: the ledger
// must say WHICH bound ended the run — wall-clock, inactivity or the
// output budget — so "the command was terminalized, and here is the bound
// that did it" stays answerable from the record, never reconstructed.
// Everything else keeps TermFailed.
func terminationReasonOf(err error) content.TerminationReason {
	var leaseErr *RunLeaseError
	if errors.As(err, &leaseErr) {
		return leaseErr.Reason
	}
	return content.TermFailed
}

// run dispatches one executable tool to its executor. The capability and the
// executor stay paired by the declaration row: the same tool name looked up
// here is the name the middleware narrowed the capability with. Execution
// differs by exactly one field of the declaration (design §6.6): an InGo
// tool runs against its narrowed capability in-process; an InRenderer tool
// is asked of the renderer through the run's requester seam.
func (m *policyMiddleware) run(decl agenttools.Tool, ctx context.Context, capability agenttools.Capability, rawArgs []byte) (string, error) {
	switch decl.Executes {
	case agenttools.Dynamic:
		reader, ok := capability.(*agenttools.SessionReader)
		if !ok {
			return "", fmt.Errorf("tool %q: capability is %T, not dynamic session capability", decl.Name, capability)
		}
		var sessions SessionSource
		if m.requester != nil {
			sessions, _ = m.requester.(SessionSource)
		}
		return executeSessionRead(ctx, reader, sessions, m.requester, rawArgs)
	case agenttools.InRenderer:
		return m.executeInRenderer(ctx, decl, capability, rawArgs)
	}
	fn, ok := executors[decl.Name]
	if !ok {
		return "", fmt.Errorf("tool %q has a capability constructor but no executor — a registration that cannot run", decl.Name)
	}
	return fn(ctx, capability, rawArgs, m.seams())
}

// seams is the run's wiring handed to an InGo executor: what a tool needs
// that is neither its arguments nor its authority. The session record rides
// on the same value the renderer requests do, because the transport adapts one
// object for both (requester.go); a run with no requester wired hands over a
// nil source, and session.list says so rather than answering empty.
func (m *policyMiddleware) seams() toolSeams {
	if m.requester == nil {
		return toolSeams{}
	}
	sessions, _ := m.requester.(SessionSource)
	return toolSeams{sessions: sessions}
}

// executeInRenderer runs one InRenderer tool: the capability is the narrowed
// session authority (agenttools.Runner for run), and the renderer request
// goes through the run's requester seam. The capability check happens BEFORE
// the request: a session outside the grant is refused here and the renderer
// is never asked (criterion 4 — asserted by trying, not by inspecting). The
// type switch is the exhaustiveness proof: a second InRenderer tool extends
// the switch or it does not compile.
//
// `run` is the only row here. readScreen was the other, until session.read
// took its job (nocx-2ryxf.1) — and session.read is Dynamic, not InRenderer,
// because which side owns the answer depends on whether the item is still
// running. Its arm of that switch, and the ScreenReader capability it
// consumed, were left behind by that change and are gone now.
func (m *policyMiddleware) executeInRenderer(ctx context.Context, decl agenttools.Tool, capability agenttools.Capability, rawArgs []byte) (string, error) {
	if m.requester == nil {
		return "", fmt.Errorf("tool %q executes in the renderer but no renderer requester is wired for this run", decl.Name)
	}
	switch cap := capability.(type) {
	case *agenttools.Runner:
		return executeRun(ctx, cap, m.requester, rawArgs, func(entryID string) {
			m.noteCause(ctx, entryID)
		})
	default:
		return "", fmt.Errorf("tool %q: capability is %T, not a renderer-executable capability", decl.Name, capability)
	}
}

// ── the batch latch ───────────────────────────────────────────────────────

// batchLatch is per-run state shared by every call in one model response:
// once one call refuses or escalates, the others return without calling
// next. It lives in the run context, installed by BeforeAgent — which is why
// a resumed attempt gets a fresh one: the resume is a new attempt.
type batchLatch struct {
	mu     sync.Mutex
	reason error
}

func (l *batchLatch) trip(reason error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reason == nil {
		l.reason = reason
	}
}

func (l *batchLatch) tripped() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reason
}

type latchKey struct{}

func withLatch(ctx context.Context, l *batchLatch) context.Context {
	return context.WithValue(ctx, latchKey{}, l)
}

func latchFrom(ctx context.Context) *batchLatch {
	l, _ := ctx.Value(latchKey{}).(*batchLatch)
	return l
}

func tripLatch(ctx context.Context, reason error) {
	if l := latchFrom(ctx); l != nil {
		l.trip(reason)
	}
}
