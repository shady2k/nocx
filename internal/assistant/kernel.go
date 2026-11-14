package assistant

// The effect kernel (design §6, ADR-0028 decision 2): the one owner of what
// an effect IS, reachable by any carrier and tied to none.
//
// IT KNOWS NO FRAMEWORK, and that is load-bearing rather than tidy. This
// pipeline used to exist only as an eino middleware — adk's
// WrapInvokableToolCall — so a second way of proposing an effect had to
// import the framework or copy the kernel, and AD-8 forbids both in one
// sentence: a new implementation is added "without editing a switch and
// without copying lines". A question is therefore stated in OUR words — the
// chain's Approver takes an Ask and returns an Answer, and a person's no is a
// result — so the framework's own vocabulary never reaches this layer.
// over the RECEIVER: no file declaring a kernel method may import eino.
//
// This layer SEQUENCES AND ENFORCES; it does not implement. Masking has an
// owner, the audit has an owner, usage has an owner. What is ours, and only
// what is ours: the permit/ask/refuse decision, the attempt record before
// the call, and the narrowed capability.
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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/masking"
	"github.com/shady2k/nocx/internal/session"
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
	// RefusedByFloor: a fixed safety floor rejected the call before policy,
	// standing answers, or session overlays could consider it.
	RefusedByFloor PolicyRefusalReason = "refused-by-floor"
	// RefusedFileChanged: the approved path no longer has the version the
	// person agreed to, so the old approval does not cover this execution.
	RefusedFileChanged PolicyRefusalReason = "refused-file-changed"
	// RefusedExpired: the run's authority grant has passed its deadline
	// (ADR-0020 §5 — the grant is a versioned, EXPIRING capability). Unlike
	// every other reason here it is not about this call: nothing the model
	// could propose has authority any more, because the interval the grant
	// was issued for has closed. The capability constructor enforces it
	// (agenttools wraps every Narrow, so an expired grant yields no
	// capability at all); this reason is what turns that into an answer the
	// model can explain instead of a terminal error about a constructor,
	// and it is why the refusal comes BEFORE the attempt is opened rather
	// than after.
	RefusedExpired PolicyRefusalReason = "refused-expired"
	// RefusedExpansionChanged: a value the person was shown beside the
	// verbatim command moved between the question and this call, or could
	// not be read again to check (nocx-4h0m7.5). Without substitution there
	// is a window between reading a value and running the command; this is
	// the DETECTOR that turns "silently did something else" into "loudly
	// refused", which is the trade this repo makes everywhere else.
	RefusedExpansionChanged PolicyRefusalReason = "refused-expansion-changed"
	// The three bounds on the WIDENING ask (design §5.3, nocx-b453p). An
	// out-of-scope resource inside an editable row scope is a question, and
	// a question is an approval-fatigue channel: a model that keeps
	// proposing out-of-scope resources can walk a person into widening a
	// scope one prompt at a time. So the asking is bounded, and each bound
	// has its own sentence, because "we already asked", "you said no" and
	// "we have stopped asking" are three different facts about the run.
	//
	// RefusedWideningAsked: this exact (effect, resource) was already put to
	// a person in this run. The second occurrence is answered, not re-asked.
	RefusedWideningAsked PolicyRefusalReason = "refused-widening-asked"
	// RefusedWideningDeclined: the person refused to widen the row for this
	// (effect, resource), and that no holds for the rest of the run.
	RefusedWideningDeclined PolicyRefusalReason = "refused-widening-declined"
	// RefusedWideningCapped: this answer has already asked to widen a scope
	// as often as one answer may. The person is told, in the run itself
	// (WideningCapSentence) — a silent stop is a soft degrade.
	RefusedWideningCapped PolicyRefusalReason = "refused-widening-capped"
)

// ToolFailedError is the FOURTH cause a run can end on, and the one the
// nocx-avogl.3 brief did not name: the policy permitted the call, the tool
// ran, and it failed. Its message is already the product's — the renderer's
// "could not capture the screen", a command that could not start — and until
// this type existed it reached the block only because the transport's default
// arm concatenated err.Error(), which is what dragged eino's
// "[NodeRunError] … node path: […]" onto the screen with it. Deleting that
// sentence gets a field of its own instead.
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

// capabilityRefusal reports whether a tool's error is the NARROWED CAPABILITY
// refusing the call as outside what it holds — the carve-out from
// ToolFailedError above, and the one place that question is decided.
//
// The distinction is the whole of nocx-4yjwk.3. A tool that FAILED tried and
// could not: the file was unreadable, the command would not start, the
// endpoint was down. A capability that REFUSED never tried, because the call
// it was handed is not one it can express at all — and that is a policy fact
// in the capability's hands (ADR-0028 decision 4: the dispatcher narrows, it
// does not check), so it is a refusal, and "a refusal is an answer"
// (nocx-uvac6.1) applies to it exactly as it applies to the gate's own no.
//
// It reaches here at all only when the LEXICAL predicate the policy uses said
// yes and the capability's canonical identity said no — a symlink inside the
// fence resolving outside it. The policy asked, a person answered, and the
// call then met the enforcement. Nothing about that ordering is wrong: the
// lexical check is advisory by design and the capability is the enforcement.
// What would be wrong is ending the run over it.
//
// Sentinel, never text: the message names a path and belongs to the provider,
// and a predicate reading it would break the first time a message was
// reworded. The list grows by addition — a capability that gains its own
// out-of-scope sentinel joins it here rather than growing a second answer to
// this question somewhere else.
func capabilityRefusal(err error) bool {
	return errors.Is(err, filesystem.ErrOutOfScope)
}

// EgressScreeningError marks a result withheld because the egress gate could
// not inspect it. Tool identifies which tool produced the withheld result;
// Gate identifies the gate that made it unavailable; Err preserves the
// detector's typed cause for classification and retry guidance. It is
// distinct from ToolFailedError: the tool may have completed, but its result
// was not safe to release.
type EgressScreeningError struct {
	Tool string
	Gate string
	Err  error
}

func (e *EgressScreeningError) Error() string {
	prefix := fmt.Sprintf("agent tool %q: egress screening failed", e.Tool)
	if e.Err == nil {
		return prefix + " — the result was withheld"
	}
	return fmt.Sprintf("%s — the result was withheld: %v", prefix, e.Err)
}

func (e *EgressScreeningError) Unwrap() error { return e.Err }

// ErrMalformedModelOutput marks a tool call that corresponds to no declared
// tool or whose arguments do not match the schema the model was shown. Not a
// refusal — there is nothing to call; the model produced output the engine
// cannot act on. Terminal, where a refusal is now a tool result.
var ErrMalformedModelOutput = errors.New("agent policy: malformed model output")

// modelResult is the kernel's explicit classification of text returned toward
// a model. Refusals are nocx messages, not tool output; every other successful
// invocation is tool output and may be framed by the carrier. Keeping this
// bit at the kernel boundary prevents model-facing adapters from guessing from
// the returned text.
type modelResultKind uint8

const (
	modelToolOutput modelResultKind = iota
	modelNocxMessage
)

type modelResult struct {
	text string
	kind modelResultKind
}

func (r modelResult) forModel(frame func(string) string) string {
	if r.kind == modelNocxMessage {
		return r.text
	}
	return frame(r.text)
}

// ApprovalRequest is the user-facing ask (design §7.2): what was proposed,
// bound to the exact proposal. The surface shows it, and the approval store's
// record decides whether the same call goes on.
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
	// Effect is the effect class the gate decided on.
	Effect content.Effect `json:"effect"`
	// DerivedEffects are every class the call's own resources derived, which
	// for a command naming resources of more than one kind is more than one
	// (nocx-jxq97). Effect above remains the single row that governs the
	// decision (ADR-0020 §7); this is what lets the surface also say that the
	// call reached a host AND wrote a file, so a person is not answering
	// about the network while a file is written on the strength of that
	// answer. It is a complete account or empty — never partial — and it is
	// empty for a call whose resources did not fully resolve, whose
	// unresolved parts are carried with their reasons in Invocation.
	//
	// Internal state, like Resources above, until the notification contract
	// grows the shape that carries it to the renderer.
	DerivedEffects []content.Effect `json:"-"`
	// Resources are every resource resolved from the validated call. They are
	// internal state; Resource remains the singular wire projection until the
	// notification contract grows its multi-resource shape.
	Resources []agenttools.ResourceRef `json:"-"`
	// Resource is the first resolved resource for the current wire contract,
	// or nil when the declaration names no resource at all.
	Resource *content.GrantScope `json:"resource,omitempty"`
	// EntryID is the ledger entry that recorded the proposal — what the
	// approved call runs as a SUBSEQUENT attempt of.
	EntryID string `json:"-"`
	// Invocation is the parser result used by the effect and rule gates. It
	// is internal state, not renderer input.
	Invocation content.Invocation `json:"-"`
	// CommandInvocation preserves command-vs-non-command provenance for the
	// approval surface, including malformed command parses.
	CommandInvocation bool `json:"-"`
	// Expansion sits BESIDE the verbatim command, never instead of it
	// (nocx-4h0m7.5, nocx-y47mi SETTLED 1): what a live shell said each safe
	// expansion currently reads as, which expansions were left exactly as
	// written because reading them would have an effect, and — when no
	// shell could be asked at all — that fact and its reason. Absent for
	// every non-command proposal.
	Expansion *ExpansionFacts `json:"expansion,omitempty"`
	// OutOfScope is present only when a resource of this call fell outside a
	// bound (design §5.3). It is what makes the widening answer offerable,
	// and its cause is what decides whether an offer may be made at all.
	OutOfScope *OutOfScopeFact `json:"outOfScope,omitempty"`
	// Scripts is the whole of every file the command NAMES, read now
	// (nocx-872jc.3). It sits beside the verbatim command for the same
	// reason Expansion does and with the same disclaimer: it is a READING,
	// the string in Arguments is what runs, and the file can change between
	// this question and that run. Absent for a proposal whose parse named
	// no file to execute or source — which is every non-command proposal
	// and most command ones.
	//
	// It is NOT on the Approval record, and that asymmetry is deliberate.
	// ExpansionValues are there because they are re-read and compared before
	// the call runs; a script is advisory only, so binding one would change
	// what approving means (see internal/assistant/script.go).
	Scripts []ScriptReading `json:"scripts,omitempty"`
}

// OutOfScopeFact is the ask's account of the resource that fell outside a
// bound, and of WHICH bound (design §5.3, nocx-b453p).
//
// It exists because the three widths an answer has — this call, this session,
// always — widen NOTHING. A person shown an out-of-scope question without this
// fact can press every button the prompt has and the next identical call asks
// again, for ever; an ask a person cannot usefully answer is worse than a
// refusal. With it the surface can offer the ONE answer that changes the
// outcome, and the backend can apply that answer from the question alone.
//
// Cause is content.OutOfScopeRowScope when the bound is an operator's row
// selector — editable, so the answer may widen it — and content.OutOfScopeFence
// when it is the run fence or a narrowed capability, which no answer can widen.
// A fence fact therefore carries no offer: offering a question whose yes cannot
// be honoured is the lie this shape exists to remove.
//
// Resource is the scope the row would have to GROW TO COVER, in the form a row
// states it in, so the widening is written from the verdict rather than
// re-derived from the command line a second time.
type OutOfScopeFact struct {
	Cause    content.OutOfScopeCause `json:"cause"`
	Resource content.GrantScope      `json:"resource"`
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
	// CaptureOutput records a BODY for an entry, through the one gate a
	// durable body passes: output retention, the entry's sensitivity and
	// the environment's criticality (design §7.4). The action entry of a
	// tool call gets its result this way (nocx-hp8p2.13) — ADR-0040's tree
	// gives every other block kind a body and left `action` with none, so
	// "what came back" had nowhere to be asked from.
	CaptureOutput(ctx context.Context, in content.CaptureOutput) (bool, error)
}

// maxArgsBytes bounds the model's argument JSON — the ingress size bound of
// design §6.2. A path is a few hundred bytes; anything larger is malformed.
const maxArgsBytes = 64 << 10

// effectKernel is the pipeline for ONE run (one Ask): it holds the run's
// grant, the assembled registry, the ledger seam, the approval store, the
// egress vault comparison and the run's identity — everything the
// permit/ask/refuse decision, the attempt record and the egress gate need. A
// fresh instance per run; the grant is immutable once execution starts
// (ADR-0020 decision 5), and only a new attempt carries a different one.
type effectKernel struct {
	log       log.Logger
	grantMu   sync.RWMutex
	grant     content.Grant
	registry  agenttools.Registry
	ledger    AttemptLedger
	approvals *ApprovalStore
	known     KnownMaterial
	runID     string
	runCtx    agenttools.RunContext
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
	// onCall announces a call that is ABOUT TO RUN (nocx-shxv0). It lives
	// here rather than in the engine's event loop because this is the only
	// place that holds all four facts at once: the declaration the gate
	// decided with, the resolved resources derived from the validated
	// arguments, the ledger entry the attempt was recorded under, and the
	// knowledge that the call will actually execute. The engine's loop sees
	// only the messages, and would have to derive the resource a second
	// time from a raw arguments blob — the defect AGENTS.md spends a
	// section on.
	//
	// Nil is "nobody is listening", which is every non-transport caller.
	onCall     func(ToolCall) error
	runSeams   toolSeams
	validators map[string]*jsonschema.Schema
	// results is the compiled result schema per tool — what the executor
	// must actually produce, checked after it runs. The params validator
	// above disciplines what the MODEL sends; this disciplines what WE
	// send back, and the two exist for the same reason: the description
	// the model was shown has to be true, and the only way to keep a
	// description true is to check it (nocx-d6gn4.8.1).
	results    map[string]*jsonschema.Schema
	dispatcher *dispatchOperation
	// questioner is the person, when a service owns asking (ADR-anntnxdj §3).
	// Nil is a caller with no person to ask: every ask is refused.
	questioner Questioner
	// order is the order of this run's calls (ADR-anntnxdj §2): one at a time,
	// in the order the model proposed them, and a call behind a declined one
	// refused with the same reason. One kernel is one run, so one order is one
	// turn's.
	order *TurnOrder
}

// PaneAccessBinder mints, for ONE run, the DescendantPaneAccess and
// PaneReader-backed SessionReads/PaneKeys-backed SessionKeys/PaneMessages-
// backed SessionMessages that run needs to reach the panes of workers IT
// spawned (design §7.1, §7.3, §6.4-§6.5, §8), plus the run's OWN controller
// identity — the kernel's side of the same quintuple worker_auth.go's Admit
// binds for the tool endpoint (ControllerIdentity, PaneAccess, SessionReads,
// SessionKeys, and now SessionMessages alongside them, Task 10).
// internal/app's composition root supplies the real closure, over its own
// hub, PaneReader, PaneKeys, PaneMessages and session registry, called
// fresh per run with THIS run's own runID (bound as KernelAuthority there)
// — never a value fixed at start-up, because a run's own authority interval
// is its own run. paneAccess/sessionReads/sessionKeys/sessionMessages are
// `any` for the reason RunContext.PaneAccess already is: this package sits
// below internal/app and cannot name any of their concrete types; identity
// is session.Identity because RunContext.ControllerIdentity already is —
// both packages import internal/session directly. A nil binder (a build
// before Task 8's wiring, or a caller that never set one) leaves everything
// zero, which every reader already treats as "no descendant authority
// granted".
type PaneAccessBinder func(runID, sessionID string) (paneAccess any, sessionReads any, sessionKeys any, sessionMessages any, identity session.Identity)

// newEffectKernel builds the pipeline for one run. A schema that does
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
// onCall is the seam the visible tool call leaves through (nocx-shxv0);
// nil means nobody is listening and nothing is announced.
//
// turnEntryID is the turn every entry this run causes is joined to
// (nocx-h1l4o); empty is the un-bound caller shape and joins nothing.
// logger may be nil for the same callers — the only thing it reports is a
// relation that could not be written, which is a degrade the reader already
// handles.
func newEffectKernel(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID, sessionID string, attempt int, turnEntryID string, requester RendererRequester, attached Attachments, onCall func(ToolCall) error, seams ...toolSeams) (*effectKernel, error) {
	if known == nil {
		return nil, errors.New("agent run: no egress vault comparison wired — a run that may execute tools must screen its results against known vault material (design §7.1)")
	}
	runSeams := toolSeams{}
	if len(seams) > 0 {
		runSeams = seams[0]
	}
	// PaneAccess/SessionReads/ControllerIdentity are bound HERE, for this
	// run's OWN runID — never inferred from a call's parameters (design
	// §7.1) — the kernel's side of the same binding worker_auth.go's Admit
	// does for the tool endpoint (nocx-6q1uh.8's second obligation). A run
	// with no binder wired (a build before Task 8's composition-root
	// wiring, or a caller that never set one) leaves everything zero,
	// which every reader of them must already treat as "no descendant
	// authority granted" — the same safe default PaneAccess already
	// documents.
	var paneAccess, sessionReads, sessionKeys, sessionMessages any
	var controllerIdentity session.Identity
	if runSeams.paneAccessBinder != nil {
		paneAccess, sessionReads, sessionKeys, sessionMessages, controllerIdentity = runSeams.paneAccessBinder(runID, sessionID)
	}
	m := &effectKernel{
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
		runCtx: agenttools.RunContext{
			RunID:                 runID,
			Session:               sessionID,
			AutomaticSessionItems: append([]string(nil), attached.AutomaticItems...),
			MarkedSessionWindows:  markedWindows(attached.MarkedWindows),
			ControllerIdentity:    controllerIdentity,
			PaneAccess:            paneAccess,
			SessionReads:          sessionReads,
			SessionKeys:           sessionKeys,
			SessionMessages:       sessionMessages,
		},
		validators: make(map[string]*jsonschema.Schema, len(registry.All())),
		results:    make(map[string]*jsonschema.Schema, len(registry.All())),
		onCall:     onCall,
		runSeams:   runSeams,
		questioner: runSeams.questioner,
		order:      newTurnOrder(),
	}
	for _, t := range registry.All() {
		v, err := compileToolSchema(t)
		if err != nil {
			return nil, err
		}
		m.validators[t.Name] = v
		if len(t.ResultSchema) == 0 {
			// A row that cannot execute declares no result (registry.go
			// says why), and there is nothing to check.
			continue
		}
		r, err := compileResultSchema(t)
		if err != nil {
			return nil, err
		}
		m.results[t.Name] = r
	}
	m.dispatcher = &dispatchOperation{
		registry:   registry,
		validators: m.validators,
		results:    m.results,
	}
	return m, nil
}

// compileToolSchema compiles one tool's params schema — the same file the
// model was shown — into the validator the middleware applies to the model's
// arguments (design §6.2: the model is a LESS trusted source than the
// renderer and gets the same discipline, never a weaker one).
func compileToolSchema(t agenttools.Tool) (*jsonschema.Schema, error) {
	url := "https://nocx.local/contracts/tools/" + t.Name + ".schema.json"
	s, err := compiledSchema(url, t.ParamsSchema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: params schema: %w", t.Name, err)
	}
	return s, nil
}

// compileResultSchema compiles what a tool says it RETURNS — the $defs/result
// half of the same contract document — into the check applied to the
// executor's output. The url is distinct from the params one because they are
// two schemas, not two readings of one.
func compileResultSchema(t agenttools.Tool) (*jsonschema.Schema, error) {
	url := "https://nocx.local/contracts/tools/" + t.Name + ".result.schema.json"
	sch, err := compiledSchema(url, t.ResultSchema)
	if err != nil {
		return nil, fmt.Errorf("tool %s: result schema: %w", t.Name, err)
	}
	return sch, nil
}

// classifyCall is the one proposal-to-effect conversion. A declaration may
// name the validated argument carrying a shell command; every other tool keeps
// its declared effect. The registry owns which tools carry commands, so this
// path does not grow a second tool-name table. The returned invocation is the
// canonical parser result consumed by the policy rules.
func classifyCall(decl agenttools.Tool, args map[string]any) (agenttools.Tool, content.Invocation) {
	if decl.CommandArg == "" {
		return decl, content.Invocation{}
	}
	command, ok := args[decl.CommandArg].(string)
	if !ok {
		return decl, content.Invocation{}
	}
	invocation := parseCanonicalInvocation(command)
	decl.Effect = commandEffect(invocation, decl.Declaration.Effect)
	return decl, invocation
}

// ── the kernel's points ───────────────────────────────────────────────────

// kernelCall is the state ONE call's points share. The kernel composes its
// chain per call — it always has, because the chain is built where the call's
// id is known — so a point may keep the few facts that are produced rather
// than decided: the ledger entry and execution the attempt opened, the
// out-of-scope fact the policy found, and a gate that could not see. None of
// it is a decision: every decision is a point's own answer, and nothing here
// can allow a call.
type kernelCall struct {
	kernel     *effectKernel
	entryID    string
	execID     int64
	attempt    int
	outOfScope *OutOfScopeFact
	// egressGateErr is a screen that could not see. A Verdict can only pass,
	// refuse or ask, so the kernel records the failure here and reports it as
	// the run's failure instead of answering the call with the refusal.
	egressGateErr error
}

// authorityChain is the kernel's composition for one call: one policy point,
// the approver, the fresh grant after an answer, the guards that can only
// refuse, the screens, and the attempt step. The order is the pipeline's, so a
// person's answer is followed by the guards that read the grant as it stands
// then — a file changed while the question was open fails its evidence, and a
// grant that expired while it was open is replaced.
//
// The approver is the transport's approval service, and a chain with none has
// no approver at all: an ask nobody can be shown authorises nothing. The order
// is the turn's one call at a time (ADR-anntnxdj §2), which replaced eino's
// batch latch.
func (k *effectKernel) authorityChain(state *kernelCall) AuthorityChain {
	var approver Approver
	if k.questioner != nil {
		approver = serviceApprover{state: state, questioner: k.questioner}
	}
	return AuthorityChain{
		PreCalls: []PreCall{kernelPolicy{state: state}},
		Approver: approver,
		Order:    k.order,
		MintGrant: func(context.Context, Call) (content.Grant, bool) {
			return k.mintGrantAfterAnswer()
		},
		Guards: []Guard{
			kernelGrantGuard{kernel: k},
			kernelPolicyGuard{state: state},
			kernelFloorGuard{kernel: k},
			kernelEvidenceGuard{state: state},
		},
		// The egress screen reads the final outbound bytes — the tool's result
		// and its error — and can only pass, refuse or ask (ADR-anntnxdj §2,
		// "A screen reads what actually leaves").
		Screens:  []Screen{egressScreen{kernel: k, state: state}},
		Attempts: kernelAttempts{state: state},
		execute: func(ctx context.Context, call Call, decl agenttools.Tool, capability agenttools.Capability, raw []byte) (string, error) {
			if err := k.announce(call, state); err != nil {
				return "", err
			}
			return k.run(decl, ctx, capability, raw)
		},
	}
}

// mintGrantAfterAnswer is the fresh grant of ADR-anntnxdj §3: after a person
// answers, the authority the call goes on under is minted AGAIN from the policy
// as it stands now. Two facts make that necessary and one makes it safe. A
// question can be open for ten minutes, and the grant minted at admission has
// an hour to live; an answer that writes a standing rule ("allow in this
// session") is written by the very call that answers, so a grant minted before
// it could not see it. What it does not do is change what the model was
// OFFERED: the tool declaration is fixed for the run (ADR-anntnxdj §8), and a
// narrower grant refuses a call rather than hiding a tool.
//
// A caller with no mint keeps its admission grant, and an expired one is then
// refused by kernelGrantGuard with the sentence that says so.
func (k *effectKernel) mintGrantAfterAnswer() (content.Grant, bool) {
	if k.runSeams.freshGrant == nil {
		return content.Grant{}, false
	}
	fresh := k.runSeams.freshGrant()
	if fresh == nil {
		return content.Grant{}, false
	}
	// The run's own grant is REPLACED, never mutated (ADR-0020 §5): the call
	// that follows the answer is a new unit of work under a new authority,
	// and the calls after it are minted from the same policy. A run with no
	// fresh grant is left exactly as it was.
	k.grantMu.Lock()
	k.grant = copyGrant(*fresh)
	k.grantMu.Unlock()
	return k.currentGrant(), true
}

// announce is the visible tool call: the one place that holds the
// declaration the gate decided with, the resolved resources derived from the
// validated arguments and the ledger entry the attempt was recorded under.
// It runs in the caller's own runtime, after the capability is constructed,
// which is where it has always run.
func (k *effectKernel) announce(call Call, state *kernelCall) error {
	if k.onCall == nil {
		return nil
	}
	decl := call.Declaration()
	resources, _ := call.Resources()
	return k.onCall(ToolCall{
		Tool:       decl.Name,
		CallID:     call.CallID(),
		Args:       call.Arguments(),
		EntryID:    state.entryID,
		Effect:     decl.Effect,
		Resource:   matchedResource(resources),
		OpensBlock: decl.OpensBlock,
	})
}

// kernelPolicy is the kernel's one policy point: command classification is a
// fact preparation derived, and this point reads it together with the row
// scope, the matrix, the standing rules and the standing declines a person
// gave. It answers Allow, Deny or Ask.
type kernelPolicy struct{ state *kernelCall }

func (p kernelPolicy) Decide(ctx context.Context, call Call) Decision {
	return p.decide(ctx, call, false)
}

func (p kernelPolicy) decide(ctx context.Context, call Call, refusalOnly bool) Decision {
	k := p.state.kernel
	decl := call.Declaration()
	resources, resourceDeclaration := call.Resources()
	invocation := call.Invocation()
	tool := decl.Name
	rawArgs := string(call.RawParams())
	approved := k.proposalApproved(tool, call.CallID(), rawArgs)

	// A person's own no is read before the matrix: it is the strongest answer
	// and the most specific. A command's no is matched on its canonical
	// invocation, because an effect-wide refusal would cover unrelated
	// commands; a non-command tool's no stands for its effect class.
	if k.approvals != nil {
		if kind, declined := k.approvals.DeclinedKind(k.proposal(tool, call.CallID(), rawArgs)); declined {
			return Deny{Reason: refusalResult(tool, RefusedByPerson, kind)}
		}
		if decl.CommandArg == "" {
			if kind, standing := k.approvals.DeclinedEffect(call.RunID(), decl.Effect); standing {
				return Deny{Reason: refusalResult(tool, RefusedByPerson, kind)}
			}
		} else if kind, standing := k.approvals.DeclinedInvocation(call.RunID(), invocation); standing {
			return Deny{Reason: refusalResult(tool, RefusedByPerson, kind)}
		}
	}

	outcome, reason, verdict := k.decideInvocationWithReason(decl, resources, resourceDeclaration, invocation)
	if refusalOnly {
		if outcome == policyRefuse {
			return Deny{Reason: refusalResult(tool, reason, "")}
		}
		return Allow{}
	}
	outOfScope := outOfScopeFact(verdict)
	// askNow raises the question this call needs — unless the floor refuses
	// the call first, because a question whose yes the floor would refuse is
	// the lie this shape exists to remove: the person would be walked through
	// answering something that can never run. A PERMITTED call does not come
	// through here, and reaches the chain's floor Guard instead
	// (kernelFloorGuard), which is the enforcement that survives an answer and
	// the one the record names. One implementation, read twice, exactly as the
	// tool endpoint's policy point and its gate share one effect reading.
	//
	// The GRANT's interval is not read here, and the asymmetry is the
	// design's: the floor is standing, so no answer can move it, while the
	// grant is an interval the answer renews (ADR-anntnxdj decision 3 mints a
	// fresh grant after an answer). The guards read the grant as it stands
	// after the person spoke — a call that waited does not run on authority
	// that expired while it waited.
	askNow := func() Decision {
		if reason, refused := (kernelFloorGuard{kernel: k}).Refuse(ctx, call); refused {
			return Deny{Reason: reason}
		}
		return Ask{Reason: kernelAskReason(tool, decl.Effect, outOfScope)}
	}
	if outcome == policyRefuse {
		return Deny{Reason: refusalResult(tool, reason, "")}
	}
	if outcome == policyAsk {
		if approved {
			// The person answered this exact proposal; the guards that
			// follow read that answer.
			return Allow{}
		}
		if bounded := k.boundWideningAsk(tool, decl.Effect, outOfScope); bounded != nil {
			return Deny{Reason: bounded.text}
		}
		p.state.outOfScope = outOfScope
		return askNow()
	}
	return Allow{}
}

// kernelAskReason is the sentence the pipeline carries beside a question. The
// question a person sees is the approval request's own shape; this names why
// the call is being asked about, and is what a chain with no approver would
// report.
func kernelAskReason(tool string, effect content.Effect, outOfScope *OutOfScopeFact) string {
	reason := fmt.Sprintf("nocx asks the person before %q runs, because this run's policy asks about %s", tool, effect)
	if outOfScope != nil {
		reason += fmt.Sprintf("; it named %s %q, which is outside what this question may reach", outOfScope.Resource.Kind, outOfScope.Resource.ID)
	}
	return reason
}

// Questioner puts one prepared question to a person and returns their answer
// (ADR-anntnxdj §3). It is the kernel's authority seam to the transport's
// approval service, handed over exactly where the approval store is
// (AskParams.Questioner), and it BLOCKS: the service moves the run to
// awaiting_approval, publishes the question, waits for agent.approve and
// returns that answer to this same call. No answer — a declared limit passed, a
// cancelled call, a closed connection — is a refusal, never a yes.
//
// Both kinds of question go through it. AskEgress is the screen's question
// about bytes a call already produced; it is a separate method rather than one
// carrying either request because the surface's answers differ (an egress
// answer is never standing), and the service that owns that difference is
// given the kind rather than asked to sniff it out of a union.
type Questioner interface {
	Ask(ctx context.Context, req *ApprovalRequest) Answer
	AskEgress(ctx context.Context, req *EgressRequest) Answer
}

// serviceApprover is the chain's Approver (ADR-anntnxdj §2, §3): the
// transport's approval service puts every question to a person and the answer
// comes back to this very call, which then goes on through the guards and runs
// once. Both kinds of question go the same way — a policy ask before the call
// runs, and a screen's ask about the bytes a call already produced — because
// one person, one surface and one wait answer both.
//
// A chain with no service has no approver at all, and the pipeline refuses
// every ask: "no approver, or no answer, is a refusal".
type serviceApprover struct {
	state      *kernelCall
	questioner Questioner
}

func (a serviceApprover) Answer(ctx context.Context, call Call, ask Ask) Answer {
	if ask.Egress != nil {
		return a.questioner.AskEgress(ctx, a.prepareEgress(call, ask.Egress))
	}
	req, err := a.state.kernel.prepareQuestion(ctx, call, a.state)
	if err != nil {
		// A ledger failure is not a question: its cause is the run's to
		// report, so it travels as the unanswered answer's error and fails
		// the run.
		return Unanswered{Err: err}
	}
	return a.questioner.Ask(ctx, req)
}

// prepareEgress puts the screen's question in the shape the approval surface
// renders: the findings, never the material. The proposal is recorded in the
// store so the answer names the exact call whose bytes were withheld.
func (a serviceApprover) prepareEgress(call Call, ask *EgressAsk) *EgressRequest {
	k := a.state.kernel
	decl := call.Declaration()
	resources, _ := call.Resources()
	req := k.egressRequest(decl, call.CallID(), string(call.RawParams()), resources, ask.Findings, ask.WasError)
	if k.approvals != nil {
		ap := k.proposal(decl.Name, call.CallID(), string(call.RawParams()))
		ap.EntryID = a.state.entryID
		// Egress approval is about bytes that already exist; it does not
		// authorise another file operation.
		ap.FileVersionState = FileVersionBindingNotApplicable
		k.approvals.Request(ap)
	}
	return req
}

// prepareQuestion is the one builder of the question a person is shown: the
// proposal is recorded in the ledger as an action entry, its file versions and
// expansion values are bound, the store holds it pending, and the request
// carries what the surface renders.
func (m *effectKernel) prepareQuestion(ctx context.Context, call Call, state *kernelCall) (*ApprovalRequest, error) {
	decl := call.Declaration()
	resources, _ := call.Resources()
	invocation := call.Invocation()
	callID := call.CallID()
	rawArgs := string(call.RawParams())
	return m.escalate(ctx, decl, callID, rawArgs, call.Arguments(), resources, invocation, state.outOfScope)
}

// kernelPolicyGuard reuses the policy's refusal after the wait. An Ask is
// already answered; a new refusal is not authority a person's yes can override.
type kernelPolicyGuard struct{ state *kernelCall }

func (g kernelPolicyGuard) Refuse(ctx context.Context, call Call) (string, bool) {
	if deny, ok := kernelPolicy(g).decide(ctx, call, true).(Deny); ok {
		return deny.Reason, true
	}
	return "", false
}

// kernelGrantGuard is the grant's own interval: past its deadline there is no
// authority for any row to read a decision out of. It is a Guard rather than a
// PreCall because the interval is read AFTER the answer, where ADR-anntnxdj
// decision 3 mints the grant the call will actually run under: a call that
// waited does not run on authority that expired while it waited. It cannot
// allow either — no answer turns an expired grant back into permission — and
// the capability constructor enforces the same bound independently
// (agenttools.withinGrantLifetime).
type kernelGrantGuard struct{ kernel *effectKernel }

func (g kernelGrantGuard) Refuse(_ context.Context, call Call) (string, bool) {
	// The CALL's grant, not the run's: a call that waited is answered under
	// the grant the pipeline minted after the answer (ADR-anntnxdj §3,
	// "refreshed after an answer"), and this guard is what reads it before
	// the capability is constructed.
	if !call.Grant().Expired(time.Now()) {
		return "", false
	}
	return refusalResult(call.Tool(), RefusedExpired, ""), true
}

// kernelFloorGuard is the fixed floor and the raw-command floor: the operations
// no policy may enable. It can only refuse, so no answer a person gives can
// turn one back into permission.
type kernelFloorGuard struct{ kernel *effectKernel }

func (g kernelFloorGuard) Refuse(_ context.Context, call Call) (string, bool) {
	invocation := call.Invocation()
	resources, _ := call.Resources()
	if reason, denied := g.kernel.floorRefusal(invocation, resources); denied {
		return refusalResult(call.Tool(), RefusedByFloor, "", reason), true
	}
	decl := call.Declaration()
	if decl.CommandArg == "" {
		return "", false
	}
	command, ok := call.Arguments()[decl.CommandArg].(string)
	if !ok {
		return "", false
	}
	if reason, denied := g.kernel.grant.Policy.FloorRawCommandRefusal(command); denied {
		return refusalResult(call.Tool(), RefusedByFloor, "", reason), true
	}
	return "", false
}

// kernelEvidenceGuard is what the wait can invalidate: the file versions and
// the expansion values the person was shown, re-read now, after the answer and
// before the capability exists. A file that changed while the question was open
// no longer has the version the answer covered, and an approval that covered
// different bytes must not run.
type kernelEvidenceGuard struct{ state *kernelCall }

func (g kernelEvidenceGuard) Refuse(ctx context.Context, call Call) (string, bool) {
	k := g.state.kernel
	if k.approvals == nil {
		return "", false
	}
	proposal := k.proposal(call.Tool(), call.CallID(), string(call.RawParams()))
	if !k.approvals.IsApproved(proposal) {
		return "", false
	}
	if err := k.approvals.VerifyApprovedFileVersions(proposal); err != nil {
		return refusalResult(call.Tool(), RefusedFileChanged, "", err.Error()), true
	}
	if shown, ok := k.approvals.ApprovedExpansions(proposal); ok && len(shown) > 0 {
		if err := VerifyExpansions(ctx, k.runSeams.expansions, k.runCtx.Session, shown); err != nil {
			return refusalResult(call.Tool(), RefusedExpansionChanged, "", err.Error()), true
		}
	}
	return "", false
}

// kernelAttempts is the durable attempt: it exists from before the effect until
// the caller records its outcome. A failed write stops the call, because a call
// that cannot be recorded does not run.
type kernelAttempts struct{ state *kernelCall }

func (a kernelAttempts) Open(ctx context.Context, call Call) error {
	k := a.state.kernel
	decl := call.Declaration()
	resources, _ := call.Resources()
	execID, entryID, err := k.openAttempt(ctx, decl, call.CallID(), string(call.RawParams()), resources)
	if err != nil {
		return fmt.Errorf("agent tool %q: record attempt: %w", decl.Name, err)
	}
	a.state.execID, a.state.entryID = execID, entryID
	a.state.attempt = 1
	if k.approvals != nil {
		if _, ok := k.approvals.EntryIDFor(k.proposal(call.Tool(), call.CallID(), string(call.RawParams()))); ok {
			a.state.attempt = 2
		}
	}
	return nil
}

// Continue records the approved release as the same entry's next attempt.
// The tool's retained result stays on the stack; no capability is rebuilt.
func (a kernelAttempts) Continue(ctx context.Context, call Call) error {
	k := a.state.kernel
	if err := k.closeAttempt(ctx, a.state.execID, content.TermInterrupted, content.EntryInterrupted); err != nil {
		return err
	}
	a.state.execID = 0
	a.state.attempt++
	execID, err := startAttemptExecution(ctx, k.ledger, call.Grant(), a.state.entryID, a.state.attempt)
	if err != nil {
		return err
	}
	a.state.execID = execID
	return nil
}

// ── policy ────────────────────────────────────────────────────────────────

type policyOutcome int

const (
	policyPermit policyOutcome = iota
	policyAsk
	policyRefuse
)

// The third result is the VERDICT the answer came from, and it is returned
// rather than recomputed because the ask needs its cause and its resource: an
// out-of-scope question that does not name what fell outside cannot offer the
// only answer that would change it (design §5.3).
//
// What this answers is what the person's ROWS say. The grant's own interval and
// the fixed floors are Guards, not part of this: both can only refuse, both
// must survive a person's yes, and both are read after the answer — see
// kernelGrantGuard and kernelFloorGuard.
func (m *effectKernel) decideInvocationWithReason(t agenttools.Tool, resources []agenttools.ResourceRef, resourceDeclaration bool, invocation content.Invocation) (policyOutcome, PolicyRefusalReason, content.Verdict) {
	verdict := m.verdict(t, resources, resourceDeclaration, invocation)
	switch verdict.Decision {
	case content.DecisionRefuse:
		// The two refusals have two sentences, and the CAUSE picks which:
		// a resource outside an immutable bound is RefusedOutOfScope, and
		// a row that simply refuses this effect is RefusedByDecision. A
		// resource outside an EDITABLE row scope is no longer either — it
		// is the ask below (design §5.3).
		if verdict.Cause == content.OutOfScopeFence {
			return policyRefuse, RefusedOutOfScope, verdict
		}
		return policyRefuse, RefusedByDecision, verdict
	case content.DecisionPermit:
		return policyPermit, "", verdict
	default:
		return policyAsk, "", verdict
	}
}

// outOfScopeFact is the ask's half of a verdict: a row-scope miss is the only
// cause a question may be built on, because it is the only one an answer can
// change. A fence miss never reaches here — resourceVerdict turns it into a
// refusal — and an ask with nothing outside carries no fact at all.
func outOfScopeFact(verdict content.Verdict) *OutOfScopeFact {
	if verdict.Cause == "" {
		return nil
	}
	return &OutOfScopeFact{Cause: verdict.Cause, Resource: verdict.Resource}
}

// boundWideningAsk is the approval-fatigue bound on the widening question
// (design §5.3): one ask per (effect, resource) per run, a cap per run, and a
// declined widening that refuses that pair until the run ends. It returns the
// refusal that must REPLACE the ask, or nil when the ask may be raised — and
// it records the ask when it returns nil, so the count is of questions a
// person actually met.
//
// Without an approval store there is nowhere to remember any of it, and the
// unbounded ask is the old behaviour rather than a silent permit.
func (m *effectKernel) boundWideningAsk(tool string, effect content.Effect, fact *OutOfScopeFact) *modelResult {
	if fact == nil || fact.Cause != content.OutOfScopeRowScope || m.approvals == nil {
		return nil
	}
	var reason PolicyRefusalReason
	switch m.approvals.BeginWideningAsk(m.runID, effect, fact.Resource) {
	case WideningAskRaise:
		return nil
	case WideningAskDuplicate:
		reason = RefusedWideningAsked
	case WideningAskDeclined:
		reason = RefusedWideningDeclined
	default:
		reason = RefusedWideningCapped
	}
	return &modelResult{text: refusalResult(tool, reason, ""), kind: modelNocxMessage}
}

// verdict is the kernel's whole policy answer for one call, and it computes
// none of it: the command half is content.EffectPolicy.EvaluateInvocation and
// the DECLARED half is EvaluateResources, which is the same evaluator over
// resources a declaration resolved rather than a parser inferred. The kernel
// used to own a second containment predicate here (inScope), and owning two
// is how the two paths came to answer one question differently.
//
// The declared half receives the command half's decision, so it can only make
// the answer more restrictive; it replaces the verdict exactly when it found
// something outside, because that is when it has a cause the command half
// does not.
func (m *effectKernel) verdict(t agenttools.Tool, resources []agenttools.ResourceRef, resourceDeclaration bool, invocation content.Invocation) content.Verdict {
	fence := m.grant.Policy.RunFence()
	v := m.invocationVerdict(t, invocation, fence)
	// The resolver's nil/non-nil distinction is preserved: nil means the
	// declaration names no resource, while an empty resolved list is a
	// resource-bearing declaration whose current call touches none.
	if !resourceDeclaration {
		return v
	}
	declared := make([]content.GrantScope, 0, len(resources))
	for _, resource := range resources {
		declared = append(declared, content.GrantScope{Kind: resource.Kind, ID: resource.ID})
	}
	if d := m.declaredVerdict(t, v.Decision, declared, fence); d.Cause != "" {
		return d
	}
	return v
}

// invocationVerdict is the effect layer, over whichever rows the declaration
// reaches. An ALTERNATIVE set has one selected row and that row decides —
// ADR-0053's whole mechanism, and the reason refusing mutate-destructive does
// not take lsblk away with rm. A CONJUNCTION reaches every row on every
// successful call, so the STRICTEST of them decides: one refusal refuses the
// call, and one ask asks about it, whatever the other rows say.
//
// Taking it on WorstEffect alone was wrong in both directions. Refusing the
// write while permitting the fetch still asked, because cross-boundary is the
// higher row and the higher row was permitting; and had the rows been the
// other way round, an ask on the lower row would have been silently upgraded
// to a permit.
//
// The strictest verdict is carried WHOLE and not reduced to its decision: a
// refusal keeps the cause and the resource that produced it, which is what
// decides between "no answer can move this" and a question worth asking.
func (m *effectKernel) invocationVerdict(t agenttools.Tool, invocation content.Invocation, fence []content.GrantScope) content.Verdict {
	if t.EffectRelation != agenttools.EffectsConjunctive {
		return m.grant.Policy.EvaluateInvocation(t.Effect, invocation, fence)
	}
	strictest := content.Verdict{Decision: content.DecisionPermit}
	for _, effect := range t.Declaration.Effect {
		strictest = stricterVerdict(strictest, m.grant.Policy.EvaluateInvocation(effect, invocation, fence))
	}
	return strictest
}

// declaredVerdict is the resource layer for resources a DECLARATION resolved.
// For a conjunction the rows are asked TOGETHER — content.EvaluateResourcesAcross
// carries the argument for the union — because the destination belongs to the
// fetching row and the content item to the writing one, and asking each row
// alone would refuse an install whose two halves were both granted.
func (m *effectKernel) declaredVerdict(t agenttools.Tool, decision content.Decision, declared, fence []content.GrantScope) content.Verdict {
	if t.EffectRelation != agenttools.EffectsConjunctive {
		return m.grant.Policy.EvaluateResources(t.Effect, decision, declared, fence)
	}
	return m.grant.Policy.EvaluateResourcesAcross(t.Declaration.Effect, decision, declared, fence)
}

// stricterVerdict keeps whichever of two verdicts allows less, whole. It is
// spelled on top of stricterDecision rather than beside it so the two rankings
// cannot drift; ties keep the left, which is the accumulator, so a run of
// permits does not churn the cause.
func stricterVerdict(left, right content.Verdict) content.Verdict {
	if stricterDecision(left.Decision, right.Decision) == right.Decision && right.Decision != left.Decision {
		return right
	}
	return left
}

// stricterDecision orders the three answers by how little they allow. It is
// spelled out rather than derived from the lattice because it is a different
// order: the lattice ranks ACTS by severity, this ranks ANSWERS by permission.
func stricterDecision(left, right content.Decision) content.Decision {
	rank := func(d content.Decision) int {
		switch d {
		case content.DecisionRefuse:
			return 2
		case content.DecisionAsk:
			return 1
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}

func (m *effectKernel) floorRefusal(invocation content.Invocation, resources []agenttools.ResourceRef) (string, bool) {
	scopes := make([]content.GrantScope, 0, len(resources))
	for _, resource := range resources {
		scopes = append(scopes, content.GrantScope{Kind: resource.Kind, ID: resource.ID})
	}
	return m.grant.Policy.FloorRefusal(invocation, scopes)
}

// matchedResource is the singular wire projection of the first resolved
// resource. The complete Resources slice remains on ApprovalRequest and in
// the persisted proposal payload.
func matchedResource(resources []agenttools.ResourceRef) *content.GrantScope {
	if len(resources) == 0 || resources[0].Kind == "" || resources[0].ID == "" {
		return nil
	}
	return &content.GrantScope{Kind: resources[0].Kind, ID: resources[0].ID}
}

// bindApprovalFileVersions captures path identities before an approval is
// requested. files.create treats ENOENT as NotApplicable because creating a
// missing path is its successful precondition; every other capture failure
// remains Required and therefore fails closed at execution.
func bindApprovalFileVersions(ap *Approval, decl agenttools.Tool, resources []agenttools.ResourceRef) {
	pathCount := 0
	versions := make([]FileVersion, 0, len(resources))
	for _, resource := range resources {
		if resource.Kind != content.ResourcePath {
			continue
		}
		pathCount++
		version, err := CaptureFileVersion(resource.ID)
		if err != nil {
			if decl.Name == "files.create" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			ap.FileVersionState = FileVersionBindingRequired
			return
		}
		versions = append(versions, version)
	}
	if pathCount == 0 || (decl.Name == "files.create" && len(versions) == 0) {
		ap.FileVersionState = FileVersionBindingNotApplicable
		return
	}
	if len(versions) != pathCount {
		ap.FileVersionState = FileVersionBindingRequired
		return
	}
	ap.FileVersionState = FileVersionBindingCaptured
	ap.FileVersions = versions
}

// commandOf returns the verbatim command a command-carrying declaration
// names, exactly as the model produced it. It is the ONE place the raw
// string is read out of the proposal for expansion purposes, and it copies
// nothing: what comes back is what would run.
func commandOf(decl agenttools.Tool, rawArgs string) (string, bool) {
	if decl.CommandArg == "" {
		return "", false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "", false
	}
	command, ok := args[decl.CommandArg].(string)
	return command, ok && command != ""
}

// bindApprovalExpansions puts the expansion facts on BOTH halves of an
// escalation: the request the person is shown, and the approval record the
// submission is later checked against (nocx-4h0m7.5).
//
// It asks ONE query per approval, never one per variable, and only ever for
// expansions expansionsIn classified as pure reads — a `$(…)` is never
// evaluated to build a question. Where no shell can be asked, the facts say
// so and the run is unaffected: failing to expand is a thinner window, never
// a refusal.
func (m *effectKernel) bindApprovalExpansions(ctx context.Context, ap *Approval, req *ApprovalRequest, decl agenttools.Tool, rawArgs string) {
	command, ok := commandOf(decl, rawArgs)
	if !ok {
		return
	}
	facts := ExpansionFactsFor(ctx, m.runSeams.expansions, m.runCtx.Session, command)
	ap.ExpansionValues = facts.Values
	if req != nil {
		req.Expansion = &facts
	}
}

// bindApprovalScripts puts the whole of every file the command names into the
// question the person is shown (nocx-872jc.3).
//
// ONE HALF, not both, and that is the difference between this and
// bindApprovalExpansions. The expansion goes on the Approval record too
// because it is re-read and compared before the call runs; a script reading
// is advisory, nothing compares it to anything later, and putting it on the
// record would be the first half of a fence this bead did not ask for.
//
// It asks the INVOCATION which files the command names, never the raw string:
// internal/content's parser already owns that question and a second answer to
// it could disagree with the one the policy gate just used.
func (m *effectKernel) bindApprovalScripts(ctx context.Context, req *ApprovalRequest, invocation content.Invocation) {
	if req == nil {
		return
	}
	req.Scripts = ScriptReadingsFor(ctx, m.runSeams.scripts, m.runCtx.Session, m.runSeams.cwd, invocation)
}

// ── the ask ───────────────────────────────────────────────────────────────

// escalate prepares the question about a call that has not run — the
// proposal, the ledger entry and attempt that record it, its file versions and
// expansion values, and the request the surface renders. What the approver
// does with it — block the call on a person's answer — is the approver's
// ending, not this builder's.
//
// The proposal is RECORDED, not only held in memory (nocx-5dldy): it is an
// action entry in the ledger with its own interrupted attempt — "the
// proposal, the decision, the attempt and the result are one readable
// thread" — and the approved call runs as a SUBSEQUENT attempt of that same
// entry (ADR-0020 decision 4: a retry after approval is an execution of the
// same intent, never a new intent).
func (m *effectKernel) escalate(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, args map[string]any, resources []agenttools.ResourceRef, invocation content.Invocation, outOfScope *OutOfScopeFact) (*ApprovalRequest, error) {
	ap := m.proposalWithInvocation(decl.Name, callID, rawArgs, invocation)
	ap.CommandInvocation = decl.CommandArg != ""
	// The pending record carries the fact too: the answer arrives on another
	// method, on another connection's turn, and by then only the record
	// remembers which resource the widening would have to cover.
	ap.OutOfScope = outOfScope
	bindApprovalFileVersions(&ap, decl, resources)
	var entryID string
	if m.ledger != nil {
		id, err := m.recordProposal(ctx, decl, rawArgs, resources, ap)
		if err != nil {
			return nil, err
		}
		entryID = id
	}
	req := m.request(decl, callID, rawArgs, resources)
	req.OutOfScope = outOfScope
	req.CommandInvocation = decl.CommandArg != ""
	req.Invocation = cloneInvocation(invocation)
	req.DerivedEffects = commandSelection(invocation, decl.Declaration.Effect).Candidates
	req.ArgHash = ap.ArgHash
	req.EntryID = entryID
	// After the ledger record and before the answer: the person is shown the
	// values, and the SAME values are what the record binds.
	m.bindApprovalExpansions(ctx, &ap, req, decl, rawArgs)
	m.bindApprovalScripts(ctx, req, invocation)
	if m.approvals != nil {
		ap.EntryID = entryID
		m.approvals.Request(ap)
	}
	return req, nil
}

// request builds the ask the escalation site sends. It takes the
// DECLARATION, not the tool name, because the effect and the resource come
// off the declaration the gate just decided with.
func (m *effectKernel) request(decl agenttools.Tool, callID, rawArgs string, resources []agenttools.ResourceRef) *ApprovalRequest {
	return &ApprovalRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		Effect:    decl.Effect,
		Resources: append([]agenttools.ResourceRef(nil), resources...),
		Resource:  matchedResource(resources),
	}
}

func (m *effectKernel) proposal(toolName, callID, rawArgs string) Approval {
	return Approval{
		RunID:   m.runID,
		Attempt: m.attempt,
		Tool:    toolName,
		CallID:  callID,
		ArgHash: canonicalArgHash(rawArgs),
	}
}

func (m *effectKernel) proposalWithInvocation(toolName, callID, rawArgs string, invocation content.Invocation) Approval {
	ap := m.proposal(toolName, callID, rawArgs)
	ap.Invocation = cloneInvocation(invocation)
	return ap
}

// egressRequest builds the egress ask. It carries the effect and the resource
// too, off the same declaration and by the same derivation as a policy ask:
// the surface offers only allow/deny once here, but the notification is ONE
// shape on the wire, and a required field absent on one path is how a schema
// failure becomes a missing fact on the surface.
func (m *effectKernel) egressRequest(decl agenttools.Tool, callID, rawArgs string, resources []agenttools.ResourceRef, findings []EgressFinding, wasError bool) *EgressRequest {
	return &EgressRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		ArgHash:   canonicalArgHash(rawArgs),
		Effect:    decl.Effect,
		Resource:  matchedResource(resources),
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

// withheldToolResult is the product-visible answer when offer-time filtering
// removes every class a declared tool can reach. The rows are named so a person
// can understand the omission and change the policy, rather than seeing a
// completed turn that silently skipped the tool.
func withheldToolResult(tool string, effects []content.Effect) string {
	rows := make([]string, 0, len(effects))
	for _, effect := range effects {
		rows = append(rows, string(effect))
	}
	return fmt.Sprintf("REFUSED: nocx did not offer tool %q: every effect it can reach is refused by policy (%s).", tool, strings.Join(rows, ", "))
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
// detail is supplied only by the fixed floor, whose sentence names the
// operation rather than exposing policy internals.
func refusalResult(tool string, reason PolicyRefusalReason, kind DeclineKind, detail ...string) string {
	switch reason {
	case RefusedOutOfScope:
		return "REFUSED: nocx did not run your call to " + tool + ": it named something outside what this question is allowed to reach. Say what you wanted in words, or propose a call within what you were given — never a different spelling of the same call."
	case RefusedByFloor:
		floorReason := "This operation is protected by the floor and can never be enabled by policy."
		if len(detail) > 0 && detail[0] != "" {
			floorReason = detail[0] + " It can never be enabled by policy."
		}
		return "REFUSED: nocx did not run your call to " + tool + ": " + floorReason
	case RefusedByPerson:
		switch kind {
		case DeclineCallSession:
			return "REFUSED: the person declined your call to " + tool + ", and refused this kind of call in this session. Do not propose it again in this session."
		case DeclineCallAlways:
			return "REFUSED: the person declined your call to " + tool + ", and refused this kind of call from now on. Do not propose it again."
		default:
			return "REFUSED: the person declined your call to " + tool + " — it did not run. Say what you needed in words instead."
		}
	case RefusedWideningAsked:
		return "REFUSED: nocx did not run your call to " + tool + ": it named something outside what this question may reach, and that exact resource was already put to the person in this answer. Do not propose it a third time — say what you needed in words."
	case RefusedWideningDeclined:
		return "REFUSED: nocx did not run your call to " + tool + ": the person refused to widen what this answer may reach to cover that resource. Do not propose it again in this answer."
	case RefusedWideningCapped:
		return "REFUSED: nocx did not run your call to " + tool + ": this answer has already asked the person to widen what it may reach as often as one answer may, so nocx has stopped asking. Work within what you were given, or say what you would need."
	case RefusedExpired:
		// The sentence says the authority ended, not that the call was
		// wrong: nothing the model proposes next will run either, and
		// telling it to propose something else would be an invitation to
		// keep trying. Only the person can start a turn, so the answer is
		// to say so in words.
		return "REFUSED: nocx did not run your call to " + tool + ": the authority this question was given has expired. Nothing further will run under it. Say what you were about to do, in words, so the person can ask again."
	case RefusedFileChanged:
		reason := "the file changed since approval, so the old approval no longer applies"
		if len(detail) > 0 && detail[0] != "" {
			reason = detail[0]
		}
		return "REFUSED: nocx did not run your call to " + tool + ": " + reason
	case RefusedExpansionChanged:
		// The sentence NAMES the variable and states nothing about its
		// value: the person saw both values in the window they answered,
		// and the model needs the fact, not the contents of the machine.
		reason := "a value the command depends on changed between the approval and the call"
		if len(detail) > 0 && detail[0] != "" {
			reason = detail[0]
		}
		return "REFUSED: nocx did not run your call to " + tool + ": " + reason + ". Propose the call again if you still want it; do not work around the check."
	default: // RefusedByDecision — the matrix row, standing by nature
		return "REFUSED: nocx did not run your call to " + tool + ": this kind of action is refused by the policy this question runs under, and that refusal stands. Do not propose it again, or try a different spelling of the same call."
	}
}

// PersonDeclineResult is the sentence a person's NO puts in the refused
// call's own slot. It is exported because the answer now arrives through the
// transport's approval service (nocx-a62cl.3.1), which must hand the pipeline
// the same words a resumed refusal used to produce — one spelling of "the
// person said no", wherever the answer came from.
func PersonDeclineResult(tool string, kind DeclineKind) string {
	return refusalResult(tool, RefusedByPerson, kind)
}

// NoAnswerResult is the sentence an ask's declared limit puts in the call's
// slot when nobody answered. It is a refusal — no answer is never a yes — and
// it is written here, beside every other refusal sentence, so the model reads
// one vocabulary. The limit is stated because a person who walked away and came
// back should be able to tell "refused" from "nobody was there".
func NoAnswerResult(tool string, limit time.Duration) string {
	minutes := int(limit.Round(time.Minute) / time.Minute)
	return fmt.Sprintf("REFUSED: nocx did not run your call to %s: nobody answered within %d minutes, so the question was closed. Say what you needed in words instead.", tool, minutes)
}

// leaseResult is the model-visible answer for a run lease outcome.
// It follows refusalResult's contract: the tool call receives our sentence
// as a result, so the model can explain the real outcome and continue rather
// than receiving a framework error with no call result.
func leaseResult(tool string, leaseErr *RunLeaseError) string {
	sentence := RunLeaseSentence(leaseErr.Reason, leaseErr.SubmissionExpired)
	if leaseErr.SubmissionExpired {
		return "NOT RUN: nocx did not run your call to " + tool + ": " + sentence + ". Do not treat this as a command result or retry it without explaining why."
	}
	return "TERMINATED: nocx ended your call to " + tool + ": " + sentence + ". Do not treat this as a command result or retry it without explaining why."
}

// runNotWaitingResult is the model-visible answer for a continuation that
// named a run which is no longer waiting. It follows leaseResult's contract:
// the sentence is the CALL'S RESULT, so the model reads what happened and
// carries on, instead of the whole turn dying over a race it could not win.
// failureChangedNothing answers the one question that decides whether a
// failed call ends the run: could it have left anything behind? It is read
// off the DECLARATION rather than off a list of tool names, so a new tool
// gets the right answer by classifying itself, which the registry already
// makes compulsory (content.Effect's doc: "forgot to classify a tool" stops
// compiling).
//
// It is TRUE for a cross-boundary call that mutates nothing — a read of
// something off this machine, which is every cross-boundary tool nocx
// declares: fetch.url. A read that failed leaves this
// machine exactly as it was, and the thing that failed is somebody else's
// server, which is precisely the case a model can route around. A tool that
// ever POSTS somewhere carries a mutate class as well and is therefore not in
// this set; nor is a disclosure, a privilege change or a delegation, each of
// which may have half-happened.
//
// A PURELY LOCAL Observe failure is deliberately NOT in this set, and that
// is a boundary rather than an oversight (nocx-i9j5e). A failed session.read
// already ends the run and three tests in internal/transport say so on
// purpose. Whether it should is a real question and a separate one: it has
// its own bead, because deciding it as a side effect of this fix is how a
// settled question stops being settled.
func failureChangedNothing(tool agenttools.Tool) bool {
	crossBoundary := false
	for _, effect := range tool.Declaration.Effect {
		switch effect {
		case content.EffectCrossBoundary:
			crossBoundary = true
		case content.EffectObserve:
		default:
			return false
		}
	}
	return crossBoundary
}

// toolFailedResult is what such a failure reads as to the model. It names the
// tool, carries what actually went wrong — the model cannot choose another
// way without knowing which way failed — and says outright that nothing
// changed, so trying something else is not a second attempt at a half-done
// thing. Modelled on runNotWaitingResult, whose situation is the same one.
func toolFailedResult(tool string, err error) string {
	return "FAILED: nocx ran your call to " + tool + " and it did not succeed: " + err.Error() +
		". Nothing was changed by this call. You may try another way of getting what you needed, " +
		"or tell the person what stopped you."
}

func runNotWaitingResult(tool string) string {
	return "NOT WAITING: nocx did not run your call to " + tool +
		": that command is no longer waiting to be answered about — it finished on its own, or it was already stopped. " +
		"Nothing was changed by this call. Do not answer about it again; if you need to know how it ended, read the session."
}

// ── the attempt ───────────────────────────────────────────────────────────

// recordProposal writes the escalation's ledger facts BEFORE the question is
// put (nocx-5dldy: "an escalation is recorded, not only held in memory"): the
// proposal is an action entry whose payload names the exact binding — tool,
// effect, arguments, and the run/attempt/callId/argHash the approval store
// keys — and its own attempt, closed interrupted: the call that is asking has
// NOT run (the escalation is before next). The approved call runs as a
// SUBSEQUENT attempt of this same entry. A failed write fails the run: a
// question whose answer would authorise nothing is the hole the thread
// criterion exists to close.
func (m *effectKernel) recordProposal(ctx context.Context, decl agenttools.Tool, rawArgs string, resources []agenttools.ResourceRef, ap Approval) (string, error) {
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
	// The complete resolved resource list is stored with the proposal so a
	// restored turn never derives it again from raw arguments. Keep the
	// singular first-resource field for existing readers.
	if len(resources) > 0 {
		payloadBody["resources"] = resources
		payloadBody["resource"] = matchedResource(resources)
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
func (m *effectKernel) openAttempt(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, resources []agenttools.ResourceRef) (int64, string, error) {
	if m.ledger == nil {
		return 0, "", errors.New("no attempt ledger wired — a tool call may not run without a durable attempt (design §6.4)")
	}
	if err := prepareAttemptEnvironment(ctx, m.ledger); err != nil {
		return 0, "", err
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
		var err error
		entryID, err = submitAttemptEntry(ctx, m.ledger, m.grant, decl, decl.Effect, m.runID, rawArgs, resources)
		if err != nil {
			return 0, "", err
		}
		// A new intent is a new cause of this turn. An APPROVED call is
		// NOT: it runs as a subsequent attempt of the proposal's entry,
		// which took its position when the person was asked — joining it
		// again would move it to after everything that followed the
		// question. (AddCause is idempotent on the pair, so this is belt
		// and braces rather than the only guard.)
		m.noteCause(ctx, entryID)
	}
	execID, err := startAttemptExecution(ctx, m.ledger, m.grant, entryID, attempt)
	if err != nil {
		return 0, "", err
	}
	return execID, entryID, nil
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
func (m *effectKernel) noteCause(ctx context.Context, causedEntryID string) {
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
func (m *effectKernel) closeAttempt(ctx context.Context, execID int64, reason content.TerminationReason, status content.EntryStatus) error {
	if m.ledger == nil || execID == 0 {
		return nil
	}
	// Same defect, same fix as attemptRecordingDispatcher.Dispatch
	// (nocx-uhii1): by the time closeAttempt runs, the tool call it is
	// closing out has already produced its terminal outcome, and this write
	// must not be able to turn that into a failure just because the turn's
	// own context ended in the meantime (a person closing the tab, or the
	// turn's own deadline). detachedFinishContext carries this context's
	// values but not its cancellation, bounded by its own short timeout.
	finishCtx, cancel := detachedFinishContext(ctx)
	defer cancel()
	return m.ledger.FinishExecution(finishCtx, execID, content.FinishExecution{
		EndedAt:           time.Now().UnixMilli(),
		TerminationReason: reason,
		Status:            status,
	})
}

// closeFailedAttempt closes the attempt of a call that did not succeed, and it
// separates ONE fact from failure: the call was STOPPED (nocx-a62cl.4.1). The
// tool did not break — the person stopped the answer, or took back the answer
// the call ran under — and "failed" would say it did.
//
// Two shapes of the same fact, because a call can be stopped in two places. A
// tool whose executor observes the turn's own context sees it end, and the
// ending carries the reason somebody chose (TurnEndedCause: the person's stop,
// a revoked answer); a run ended for any other reason — the window closed, the
// request was cancelled — carries none and records TermInterrupted, which is
// what the run records too. A call that runs IN THE RENDERER (session.run,
// session.wait) is stopped by its run lease withdrawing the request before the
// turn's context is cancelled — deliberately, because the ladder must name the
// process group before the withdrawal (nocx-uvac6.11) — so what reaches here is
// the request's own cancellation and the attempt records TermInterrupted. The
// run's row is what says the person pressed Stop.
func (m *effectKernel) closeFailedAttempt(ctx context.Context, execID int64, reason content.TerminationReason, cause error) error {
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) {
		ended := content.TermInterrupted
		if stated, ok := TurnEndedReason(context.Cause(ctx)); ok {
			ended = stated
		}
		return m.closeAttempt(ctx, execID, ended, content.EntryInterrupted)
	}
	return m.closeAttempt(ctx, execID, reason, content.EntryFailure)
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
func (m *effectKernel) run(decl agenttools.Tool, ctx context.Context, capability agenttools.Capability, rawArgs []byte) (string, error) {
	if !decl.ResultBound.Valid() {
		return "", fmt.Errorf("tool %q has no valid result bound", decl.Name)
	}
	runCtx := ctx
	cancel := func() {}
	if decl.Deadline > 0 {
		runCtx, cancel = context.WithTimeout(ctx, decl.Deadline)
	}
	defer cancel()
	runCtx = withToolBound(runCtx, decl.ResultBound)
	switch decl.Executes {
	case agenttools.Dynamic:
		reader, ok := capability.(*agenttools.SessionDescendantCapability)
		if !ok {
			return "", fmt.Errorf("tool %q: capability is %T, not dynamic session capability", decl.Name, capability)
		}
		return executeSessionRead(runCtx, reader, m.runSeams.sessions, m.runSeams.screenCapture, rawArgs)
	case agenttools.InRenderer:
		return m.executeInRenderer(runCtx, decl, capability, rawArgs)
	}
	fn, ok := executors[decl.Name]
	if !ok {
		return "", fmt.Errorf("tool %q has a capability constructor but no executor — a registration that cannot run", decl.Name)
	}
	return fn(runCtx, capability, rawArgs, m.seams())
}

// seams is the run's wiring handed to an InGo executor: what a tool needs
// that is neither its arguments nor its authority. Session records and screen
// capture are separate backend-owned sources; neither is a renderer request.
func (m *effectKernel) seams() toolSeams {
	seams := m.runSeams
	if m.requester != nil {
		if sessions, ok := m.requester.(SessionSource); ok {
			seams.sessions = sessions
		}
	}
	return seams
}

// executeInRenderer runs one InRenderer tool: the capability is the narrowed
// session authority (agenttools.Runner for run), and the renderer request
// goes through the run's requester seam. The capability check happens BEFORE
// the request: a session outside the grant is refused here and the renderer
// is never asked (criterion 4 — asserted by trying, not by inspecting). The
// type switch is the exhaustiveness proof: a second InRenderer tool extends
// the switch or it does not compile.
//
// `run` and its continuation `wait` are the rows here. readScreen was another, until session.read
// took its job (nocx-2ryxf.1) — and session.read is Dynamic, not InRenderer,
// because which side owns the answer depends on whether the item is still
// running. Its arm of that switch, and the ScreenReader capability it
// consumed, were left behind by that change and are gone now.
func (m *effectKernel) executeInRenderer(ctx context.Context, decl agenttools.Tool, capability agenttools.Capability, rawArgs []byte) (string, error) {
	if m.requester == nil {
		return "", fmt.Errorf("tool %q executes in the renderer but no renderer requester is wired for this run", decl.Name)
	}
	switch cap := capability.(type) {
	case *agenttools.Runner:
		return executeRun(ctx, cap, m.requester, rawArgs, func(entryID string) {
			// Lease cancellation has already ended the execution context;
			// durable cause bookkeeping must still close the command→turn
			// interval.
			m.noteCause(context.WithoutCancel(ctx), entryID)
		})
	case *agenttools.RunWatcher:
		return executeRunWait(ctx, cap, m.requester, rawArgs)
	default:
		return "", fmt.Errorf("tool %q: capability is %T, not a renderer-executable capability", decl.Name, capability)
	}
}

// Invoke runs ONE proposed effect through the whole pipeline and returns what
// the caller must hand back to whoever proposed it: the framed tool result,
// or the refusal — which is a RESULT and not an error (nocx-uvac6.1).
//
// It is the carrier-neutral entry point, and the only one. A caller that is
// not eino gets the identical decision, the identical attempt interval and
// the identical ledger shape, because there is no second copy of any of it.
//
// A PERSON'S ANSWER IS THE PIPELINE'S OWN, in our vocabulary rather than a
// framework's: the chain's Approver blocks the call on an answer, and what
// comes back is Allow, a no, or nobody-answered. Nothing here suspends, and
// nothing here names a framework type — which is what lets a caller that is
// not eino get the identical decision.
func (k *effectKernel) Invoke(ctx context.Context, name, callID, rawArgs string) (string, error) {
	out, err := k.invokeClassified(ctx, name, callID, rawArgs)
	if err != nil {
		return "", err
	}
	return out.text, nil
}

func (k *effectKernel) invokeClassified(ctx context.Context, name, callID, rawArgs string) (modelResult, error) {
	// The call runs through the authority pipeline (nocx-a62cl.3.2). What the
	// kernel used to do in one gate closure is the chain's points now:
	// command classification is a fact preparation derived, the matrix,
	// standing rules, standing declines and row scope are the policy PreCall,
	// the floors and the file-version/expansion evidence are Guards, the
	// attempt opens between them and the narrowed capability, and the
	// approver is an adapter that keeps the kernel's own suspension exactly
	// until nocx-a62cl.3.4 replaces it with a service that blocks.
	release, declined, enterErr := k.order.enter(ctx)
	if enterErr != nil {
		return modelResult{}, &PipelineRefusal{
			Call:   Call{runID: k.runID, tool: name},
			Reason: "the call was cancelled while it waited for its turn: " + errText(enterErr),
			Cause:  enterErr,
		}
	}
	defer release()
	if declined != nil {
		reason := declined.Reason
		if reason == "" {
			reason = "a person declined the call before this one"
		}
		return modelResult{text: reason, kind: modelNocxMessage}, nil
	}
	state := &kernelCall{kernel: k}
	chain := k.authorityChain(state)
	// The kernel retains the slot through outcome recording, beyond run.
	pipeline, pipelineErr := NewPipeline(k.dispatcher, chain)
	if pipelineErr != nil {
		return modelResult{}, pipelineErr
	}
	request := ToolInvocation{
		Context:    ctx,
		Method:     name,
		RunContext: k.runCtx,
		Grant:      k.grant,
		RawParams:  []byte(rawArgs),
	}
	outcome, dispatchErr := pipeline.runOrdered(request, k.attempt, callID, true)
	if dispatchErr != nil {
		// A screen that could not see is a run failure, never an answer:
		// nothing leaves when the screen cannot see, and it reaches here
		// as the screen's Refuse: a Verdict cannot carry an error, so the
		// screen recorded it and the kernel reports it.
		if state.egressGateErr != nil {
			if state.execID != 0 {
				_ = k.closeAttempt(ctx, state.execID, content.TermFailed, content.EntryFailure)
			}
			var refusal *PipelineRefusal
			tool := name
			if errors.As(dispatchErr, &refusal) && refusal.Call.Tool() != "" {
				tool = refusal.Call.Tool()
			}
			return modelResult{}, &EgressScreeningError{Tool: tool, Gate: "egress", Err: state.egressGateErr}
		}
		// A point's refusal is an ANSWER (nocx-uvac6.1): the sentence is
		// ours, it rides the refused call's own slot, and the run continues
		// until it reaches a terminal state of its own accord. A refusal
		// that carries a cause is a cancellation, a failure underneath, an
		// escalation's suspension or a person's no to a RESULT, and stays an
		// error.
		var refusal *PipelineRefusal
		if errors.As(dispatchErr, &refusal) && refusal.Cause == nil {
			return modelResult{text: refusal.Reason, kind: modelNocxMessage}, nil
		}
		// A person's no to a SCREEN's question is about a result that already
		// exists, so there is nothing to continue the call with: the run
		// ends (ADR-anntnxdj §3, "a declined egress ends the run"). The
		// attempt closes as the person's decision — the effect happened and
		// its result never left.
		var screenDeclined *ScreenDeclinedError
		if errors.As(dispatchErr, &screenDeclined) {
			if state.execID != 0 {
				_ = k.closeAttempt(ctx, state.execID, content.TermAgentDeclined, content.EntryInterrupted)
			}
			return modelResult{}, dispatchErr
		}
		if state.execID != 0 {
			_ = k.closeFailedAttempt(ctx, state.execID, content.TermFailed, dispatchErr)
		}
		if errors.Is(dispatchErr, ErrInvalidResult) && outcome.prepared != nil {
			decl := outcome.prepared.decl
			// The detail is the schema library's own diagnostic, which quotes
			// the result's property names: it belongs in the log, never in
			// the message a model or a person reads (ResultContractError).
			detail := dispatchErr
			var contract *ResultContractError
			if errors.As(dispatchErr, &contract) {
				detail = contract.Detail
			}
			k.warn("agent tool: the result does not match its own contract",
				"tool", decl.Name, "call", callID, "error", detail)
			return modelResult{}, &ToolFailedError{Tool: decl.Name, Err: dispatchErr}
		}

		if errors.Is(dispatchErr, ErrUnknownMethod) || errors.Is(dispatchErr, ErrInvalidParams) {
			return modelResult{}, fmt.Errorf("%w: tool %q: %v", ErrMalformedModelOutput, name, dispatchErr)
		}
		return modelResult{}, dispatchErr
	}
	prepared := outcome.prepared
	decl := prepared.decl
	out := outcome.output
	runErr := outcome.runErr

	// 6c. A CAPABILITY REFUSAL IS AN ANSWER, NOT A FAULT (nocx-4yjwk.3).
	// The narrowed capability refused a call it cannot express, which is
	// the same kind of no the policy gate makes at step 3 — so it takes
	// the same shape: OUR sentence, in the refused call's own slot, and
	// the run continues. Before this, it left as a *ToolFailedError and
	// ended the run on the provider's sentence about a path, which is what
	// a person got back after being asked a question and answering it.
	//
	// BEFORE THE EGRESS GATE, deliberately. Nothing was read, so there is
	// no result to screen; what would reach the gate is the capability's
	// error text, whose only content is the path the model already named.
	// Screening it could put a question to a person about bytes that were
	// never produced. Every other refusal in this pipeline returns unscreened
	// for the same reason: the sentence is ours, and it says nothing the
	// policy keeps from the person.
	//
	// The attempt closes here rather than being left open — the interval
	// ends with a reason, always. TermFailed is the honest existing
	// reason: this attempt did not do what it was opened for.
	// TermAgentDeclined names the agent's own no, which this is not, and
	// content owns that vocabulary.
	if runErr != nil && capabilityRefusal(runErr) {
		_ = k.closeAttempt(ctx, state.execID, content.TermFailed, content.EntryFailure)
		return modelResult{text: refusalResult(decl.Name, RefusedOutOfScope, ""), kind: modelNocxMessage}, nil
	}

	// The egress screen ran inside the pipeline, after the tool and before
	// this point (ADR-anntnxdj §2): it read the final outbound bytes — the
	// result and the error alike — and passed them, withheld them behind a
	// person's answer, or refused. An approved resume released the retained
	// bytes there, so what reaches this line is already what may leave.

	// WHAT CAME BACK, RECORDED WHERE IT CAN BE ASKED FOR (nocx-hp8p2.13).
	// A tool call announced itself and then said nothing about its outcome:
	// agent.runToolCall carries the arguments and deliberately not the
	// result, naming actionEntryId as "the handle a later 'show me what it
	// returned' reaches through, rather than a second copy of the bytes".
	// The handle reached nothing, because nothing was ever written there —
	// ADR-0040 gives every block kind a body artifact and drew `action`
	// with none. This is that body.
	//
	// AFTER THE EGRESS GATE, DELIBERATELY. The gate has already screened
	// this result and either passed it or asked a person about it; recording
	// before it would put bytes in the store that the gate might still
	// refuse to let leave. On an APPROVED question the bytes do carry
	// findings, which is why the record is masked as well as gated — the
	// belt matters at exactly the one moment the gate has stood down.
	//
	// NOT FOR A CALL THAT OPENED A BLOCK. ADR-0040 is explicit that the
	// block the command opened IS the account of that call, and the turn
	// draws no child for it at all — so a body here would be a second copy
	// of that command's own output, stored for a surface that will never
	// ask for it.
	if runErr == nil && !decl.OpensBlock {
		k.recordToolResult(ctx, state.entryID, out)
	}

	// A person's Stop is a successful tool exchange carrying an explicit
	// renderer fact, not a tool error. Preserve that fact in the action
	// ledger: the command's own exit code may also be 130, so it cannot
	// select the user-killed reason. The result remains model-visible so the
	// model is told not to retry it.
	if runErr == nil && runResultStopped(decl.Name, out) {
		if err := k.closeAttempt(ctx, state.execID, content.TermUserKilled, content.EntryInterrupted); err != nil {
			return modelResult{}, fmt.Errorf("agent tool %q: record stopped outcome: %w", decl.Name, err)
		}
		return modelResult{text: out, kind: modelToolOutput}, nil
	}

	// A delivered lease bound is a product-caused outcome, so return it in
	// the tool's slot and let the model explain it instead of failing the
	// whole stream. An undelivered submission is different: no command
	// existed, so it remains a run-level failure and its sentence belongs in
	// agent.runState.Error.
	if runErr != nil {
		// THE QUIET BOUND'S QUESTION, which is not a failure: nothing was
		// terminalized, the command is still executing, and the model is
		// being asked whether to keep waiting (ADR-0020 decision 2's
		// renewable clause, nocx-6dzxq). It reaches the model the way a
		// refusal does — as the CALL'S RESULT — because a framework error
		// would end the stream and there would be nobody left to answer.
		//
		// The attempt closes as completed, not failed: this call did what
		// it was asked (it submitted the command and it waited), and the
		// command's own outcome belongs to the block the renderer opened,
		// not to this attempt. A renewal is therefore a ledger fact by the
		// route ADR-0020 decision 4 already provides — each continuation is
		// its own attempt of its own action entry — and needs no new
		// termination reason and no change to the executions CHECK set.
		var stillRunning *RunStillRunningError
		if errors.As(runErr, &stillRunning) {
			_ = k.closeAttempt(ctx, state.execID, content.TermCompleted, content.EntrySuccess)
			return modelResult{
				text: RunStillRunningSentence(decl.Name, stillRunning),
				kind: modelNocxMessage,
			}, nil
		}
		// A CONTINUATION THAT ARRIVED TOO LATE is the model losing a race
		// nocx started: the command finished between the question and the
		// answer. The call did nothing and changed nothing, so it is a
		// result the model reads — never a failed run.
		if errors.Is(runErr, ErrRunNotWaiting) {
			_ = k.closeAttempt(ctx, state.execID, content.TermFailed, content.EntryFailure)
			return modelResult{text: runNotWaitingResult(decl.Name), kind: modelNocxMessage}, nil
		}
		var leaseErr *RunLeaseError
		if errors.As(runErr, &leaseErr) {
			_ = k.closeAttempt(ctx, state.execID, leaseErr.Reason, content.EntryFailure)
			if leaseErr.SubmissionExpired {
				return modelResult{}, leaseErr
			}
			return modelResult{text: leaseResult(decl.Name, leaseErr), kind: modelToolOutput}, nil
		}
		_ = k.closeFailedAttempt(ctx, state.execID, terminationReasonOf(runErr), runErr)
		// A CALL THAT CHANGED NOTHING IS A RESULT, NOT THE END OF THE TURN
		// (nocx-y0h3h). Two arms above already draw this line for the two
		// cases session.run happens to produce; the line itself is not about
		// session.run. A read that reached another machine and came back
		// with nothing declares only cross-boundary, so a forge answering
		// 504 changed nothing and the model must be able to try again.
		//
		// The bytes are safe to hand over because the pipeline's egress
		// screen has already read them: it screens the error string as well
		// as the result (ADR-anntnxdj §2).
		if failureChangedNothing(decl) {
			return modelResult{text: toolFailedResult(decl.Name, runErr), kind: modelNocxMessage}, nil
		}
		// Named, so the transport can say WHICH tool failed without
		// stringifying the framework's wrapper around it.
		return modelResult{}, &ToolFailedError{Tool: decl.Name, Err: runErr}
	}

	if err := k.closeAttempt(ctx, state.execID, content.TermCompleted, content.EntrySuccess); err != nil {
		return modelResult{}, fmt.Errorf("agent tool %q: record outcome: %w", decl.Name, err)
	}
	// The RESULT, unframed. Marking a result as untrusted data for a model to
	// read is a statement addressed to a model, and the retained declared-call
	// adapter applies that projection before returning it to the model.
	return modelResult{text: out, kind: modelToolOutput}, nil
}

// maxToolResultRecordBytes bounds the body kept for one tool call. It is the
// tools' own declared result bound (agenttools.ResultBound.MaxBytes), so in
// practice nothing is cut here — a result the model received was already
// bounded before it got this far. The check exists because a bound that
// holds "in practice" is not a bound: masking rewrites the text, and one
// chunk is what this write is.
const maxToolResultRecordBytes = 64 << 10

// recordToolResult writes the tool's result as the action entry's own body.
//
// MASKED BY THE ONE OWNER, AND FAIL CLOSED. internal/masking is the durable
// path's masker (ADR-0021, nocx-a21v), and a body it could not read is not
// written at all: the attempt row still says the call happened and carries
// no body, which is exactly the state retention leaves behind and which
// every reader already draws. Writing raw text because detection failed
// would make this pane a second, unscreened path to bytes the ledger masks
// everywhere else.
//
// NOTHING HERE FAILS THE CALL. The result has already been produced and is
// on its way to the model; a body that could not be stored is a missing
// body, not a failed tool. Said in the log, never in the run.
func (k *effectKernel) recordToolResult(ctx context.Context, entryID, result string) {
	if k.ledger == nil || entryID == "" || result == "" {
		return
	}
	masked, _, _, err := masking.MaskWithSegments(result)
	if err != nil {
		k.warn("agent tool: the result could not be screened for the record — the call is recorded without its body",
			"entry", entryID, "error", err)
		return
	}
	var truncated *content.Truncation
	if len(masked) > maxToolResultRecordBytes {
		masked = truncateRunes(masked, maxToolResultRecordBytes)
		cut := content.TruncCap
		truncated = &cut
	}
	if _, err := k.ledger.CaptureOutput(ctx, content.CaptureOutput{
		EntryID:    entryID,
		ArtifactID: uuid.NewString(),
		MediaType:  content.MediaText,
		// The tool produced this text; nobody read it off a terminal grid.
		CaptureMethod:  content.CaptureRawOutput,
		CaptureVersion: 1,
		Truncated:      truncated,
		Seq:            1,
		Body:           []byte(masked),
	}); err != nil {
		k.warn("agent tool: the result could not be recorded — the call is recorded without its body",
			"entry", entryID, "error", err)
	}
}

// truncateRunes cuts s to at most n bytes without splitting a rune. A body
// cut mid-rune would reach a reader as a replacement character it cannot
// tell from one the tool really printed.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// warn is the kernel's logger with the nil check the whole package needs. A
// client built without a logger is the ordinary shape in a test, and a
// diagnostic that panics a run is worse than no diagnostic at all.

func (k *effectKernel) warn(msg string, args ...any) {
	if k.log != nil {
		k.log.Warn(msg, args...)
	}
}

// FrameForModel marks a result as untrusted data before a carrier puts it in
// front of a model (agenttools.Declaration.FrameToolResult). It is a
// projection of the declaration and decides nothing; a carrier that does not
// feed a model never calls it.
func (k *effectKernel) FrameForModel(tool, result string) string {
	decl, ok := k.registry.Lookup(tool)
	if !ok {
		return result
	}
	return decl.FrameToolResult(result)
}

// proposalApproved reports whether this exact proposal already carries a
// human's approval, so the resumed pass does not ask about a call the person
// just answered.
func (k *effectKernel) proposalApproved(toolName, callID, rawArgs string) bool {
	if k.approvals == nil {
		return false
	}
	return k.approvals.IsApproved(k.proposal(toolName, callID, rawArgs))
}

// markedWindows converts the ask's marks into the tool layer's vocabulary.
// A copy, so a later mutation of the caller's slice cannot change what this
// run may read.
func markedWindows(in []MarkedSessionWindow) []agenttools.MarkedSessionWindow {
	if len(in) == 0 {
		return nil
	}
	out := make([]agenttools.MarkedSessionWindow, 0, len(in))
	for _, mark := range in {
		out = append(out, agenttools.MarkedSessionWindow{
			ItemID: mark.ItemID, Start: mark.Start, Count: mark.Count,
		})
	}
	return out
}

// currentGrant is the snapshot for catalogue readers outside the turn slot.
func (k *effectKernel) currentGrant() content.Grant {
	k.grantMu.RLock()
	defer k.grantMu.RUnlock()
	return copyGrant(k.grant)
}
