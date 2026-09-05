package assistant

// The effect kernel (design §6, ADR-0028 decision 2): the one owner of what
// an effect IS, reachable by any carrier and tied to none.
//
// IT KNOWS NO FRAMEWORK, and that is load-bearing rather than tidy. This
// pipeline used to exist only as an eino middleware — adk's
// WrapInvokableToolCall — so a second way of proposing an effect had to
// import the framework or copy the kernel, and AD-8 forbids both in one
// sentence: a new implementation is added "without editing a switch and
// without copying lines". Suspension is therefore stated in OUR words —
// Invoke returns *ApprovalRequestedError or *EgressRequestedError — and the
// retained declared-call adapter translates that into eino's interrupt.
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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
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
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/masking"
	"github.com/shady2k/nocx/internal/skill"
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
	// RefusedExpansionChanged: a value the person was shown beside the
	// verbatim command moved between the question and this call, or could
	// not be read again to check (nocx-4h0m7.5). Without substitution there
	// is a window between reading a value and running the command; this is
	// the DETECTOR that turns "silently did something else" into "loudly
	// refused", which is the trade this repo makes everywhere else.
	RefusedExpansionChanged PolicyRefusalReason = "refused-expansion-changed"
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
	// Effect is the effect class the gate decided on.
	Effect content.Effect `json:"effect"`
	// Resources are every resource resolved from the validated call. They are
	// internal checkpoint state; Resource remains the singular wire projection
	// until the notification contract grows its multi-resource shape.
	Resources []agenttools.ResourceRef `json:"-"`
	// Resource is the first resolved resource for the current wire contract,
	// or nil when the declaration names no resource at all.
	Resource *content.GrantScope `json:"resource,omitempty"`
	// EntryID is the ledger entry that recorded the proposal — what the
	// approved call runs as a SUBSEQUENT attempt of.
	EntryID string `json:"-"`
	// Invocation is the parser result used by the effect and rule gates. It
	// is internal checkpoint state, not renderer input.
	Invocation content.Invocation `json:"-"`
	// CommandInvocation preserves command-vs-non-command provenance for the
	// approval surface, including malformed command parses.
	CommandInvocation bool `json:"-"`
	// Finding is the first static-scan finding in a skills.create or
	// skills.update body, carried to the person with the exact proposal.
	Finding *SkillScanFinding `json:"finding,omitempty"`
	// Classifier is the classifier's verdict or bounded failure fact for a
	// skills write. It is absent for ordinary policy approvals.
	Classifier *ApprovalClassifier `json:"classifier,omitempty"`
	// Expansion sits BESIDE the verbatim command, never instead of it
	// (nocx-4h0m7.5, nocx-y47mi SETTLED 1): what a live shell said each safe
	// expansion currently reads as, which expansions were left exactly as
	// written because reading them would have an effect, and — when no
	// shell could be asked at all — that fact and its reason. Absent for
	// every non-command proposal.
	Expansion *ExpansionFacts `json:"expansion,omitempty"`
}

// SkillScanFinding is the first suspicious instruction pattern found in a
// proposed skill body. It is evidence for the person approving the exact
// bytes, never the body itself.
type SkillScanFinding struct {
	PatternID  string `json:"patternId"`
	Line       string `json:"line"`
	LineNumber int    `json:"lineNumber"`
}

// ApprovalClassifier is the classifier gate's fact carried with an approval
// question. A failed consultation has no verdict or model, but still carries
// its bounded failure reason so the gate cannot disappear silently.
type ApprovalClassifier struct {
	Consulted bool              `json:"consulted"`
	Verdict   ClassifierVerdict `json:"verdict,omitempty"`
	Model     string            `json:"model,omitempty"`
	Reason    string            `json:"reason"`
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
	// classifier is the second model that judges permitted proposals (bead
	// nocx-kpy23). Nil = not wired for this run: permitted calls run
	// exactly as they do without one. Consulted ONLY where the policy says
	// permit; every failure and every suspect verdict escalates.
	classifier CallClassifier
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
	results map[string]*jsonschema.Schema
}

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
func newEffectKernel(logger log.Logger, grant content.Grant, registry agenttools.Registry, ledger AttemptLedger, approvals *ApprovalStore, known KnownMaterial, runID, sessionID string, attempt int, turnEntryID string, requester RendererRequester, attached Attachments, classifier CallClassifier, onCall func(ToolCall) error, seams ...toolSeams) (*effectKernel, error) {
	if known == nil {
		return nil, errors.New("agent run: no egress vault comparison wired — a run that may execute tools must screen its results against known vault material (design §7.1)")
	}
	runSeams := toolSeams{}
	if len(seams) > 0 {
		runSeams = seams[0]
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
		classifier:  classifier,
		runCtx: agenttools.RunContext{
			RunID:                 runID,
			Session:               sessionID,
			AutomaticSessionItems: append([]string(nil), attached.AutomaticItems...),
			MarkedSessionWindows:  markedWindows(attached.MarkedWindows),
		},
		validators: make(map[string]*jsonschema.Schema, len(registry.All())),
		results:    make(map[string]*jsonschema.Schema, len(registry.All())),
		onCall:     onCall,
		runSeams:   runSeams,
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

// compileResultSchema compiles what a tool says it RETURNS — the $defs/result
// half of the same contract document — into the check applied to the
// executor's output. The url is distinct from the params one because they are
// two schemas, not two readings of one.
func compileResultSchema(t agenttools.Tool) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(t.ResultSchema))
	if err != nil {
		return nil, fmt.Errorf("tool %s: result schema: %w", t.Name, err)
	}
	url := "https://nocx.local/contracts/tools/" + t.Name + ".result.schema.json"
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(url, doc); addErr != nil {
		return nil, fmt.Errorf("tool %s: result schema: %w", t.Name, addErr)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("tool %s: result schema: %w", t.Name, err)
	}
	return sch, nil
}

// checkResult holds the executor to the shape its contract declares. A
// mismatch is OURS, never the model's: it means the description the model
// was shown is a lie, and the honest outcome is a failed tool rather than a
// result whose keys nobody can rely on.
//
// Applied at the one seam every carrier passes through, deliberately: a check
// inside each executor would be several enforcement sites and the next
// executor would be the one that forgot.
func (m *effectKernel) checkResult(tool, out string) error {
	v := m.results[tool]
	if v == nil {
		return nil
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("agent tool %q returned something that is not JSON: %w", tool, err)
	}
	if err := v.Validate(doc); err != nil {
		return fmt.Errorf("agent tool %q returned a result its own contract does not allow, so what the model was told it returns is not true: %w", tool, err)
	}
	return nil
}

// validate applies the tool's compiled schema to the model's raw arguments.
// The result is the parsed object the policy evaluates — the same object the
// executor will receive, so the policy never decides about something that
// may not be what executes.
func (m *effectKernel) validate(decl agenttools.Tool, raw string) (map[string]any, error) {
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

// ── policy ────────────────────────────────────────────────────────────────

type policyOutcome int

const (
	policyPermit policyOutcome = iota
	policyAsk
	policyRefuse
)

func (m *effectKernel) decideInvocationWithReason(t agenttools.Tool, resources []agenttools.ResourceRef, resourceDeclaration bool, invocation content.Invocation) (policyOutcome, PolicyRefusalReason, string) {
	if reason, denied := m.floorRefusal(invocation, resources); denied {
		return policyRefuse, RefusedByFloor, reason
	}
	decision := m.grant.Policy.DecisionForInvocation(t.Effect, invocation)
	if decision == content.DecisionRefuse {
		return policyRefuse, RefusedByDecision, ""
	}
	if !m.inScope(t, resources, resourceDeclaration) {
		return policyRefuse, RefusedOutOfScope, ""
	}
	if decision == content.DecisionPermit && isSkillMutationTool(t) {
		return policyAsk, "", ""
	}
	if decision == content.DecisionPermit {
		return policyPermit, "", ""
	}
	return policyAsk, "", ""
}

func isSkillMutationTool(tool agenttools.Tool) bool {
	if tool.ScopeFamily != "skill" {
		return false
	}
	for _, effect := range tool.Declaration.Effect {
		if effect != content.EffectObserve {
			return true
		}
	}
	return false
}

func (m *effectKernel) floorRefusal(invocation content.Invocation, resources []agenttools.ResourceRef) (string, bool) {
	scopes := make([]content.GrantScope, 0, len(resources))
	for _, resource := range resources {
		scopes = append(scopes, content.GrantScope{Kind: resource.Kind, ID: resource.ID})
	}
	return m.grant.Policy.FloorRefusal(invocation, scopes)
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
// resolveResources is the one call-resource derivation. A nil resolver means
// the declaration names no resource at all; a non-nil resolver may validly
// return zero refs for this call. Resolver failures are malformed model
// output because arguments were validated but could not identify the
// declared authority.
func (m *effectKernel) resolveResources(decl agenttools.Tool, args map[string]any) ([]agenttools.ResourceRef, bool, error) {
	if decl.ResolveResources == nil {
		return nil, false, nil
	}
	resources, err := decl.ResolveResources(args, m.runCtx)
	if err != nil {
		return nil, true, err
	}
	for _, resource := range resources {
		if resource.Kind == "" || resource.ID == "" {
			return nil, true, fmt.Errorf("resource resolver returned an incomplete resource")
		}
		declared := false
		for _, kind := range decl.ResourceKinds {
			if resource.Kind == kind {
				declared = true
				break
			}
		}
		if !declared {
			return nil, true, fmt.Errorf("resource resolver returned undeclared kind %q", resource.Kind)
		}
	}
	return resources, true, nil
}

// inScope checks EVERY resolved resource against the selected effect row.
// The resolver's nil/non-nil distinction is preserved: nil means the
// declaration names no resource, while an empty resolved list is a
// resource-bearing declaration whose current call touches none.
//
// This is the DECLARED half only. The resources a COMMAND names have no
// resolver — they are the parser's report — and they meet the same row
// inside content.EffectPolicy.DecisionForInvocation, which states the
// composition order for both. Do not grow a second command-resource check
// here.
func (m *effectKernel) inScope(t agenttools.Tool, resources []agenttools.ResourceRef, resourceDeclaration bool) bool {
	if !resourceDeclaration {
		return true
	}
	for _, resource := range resources {
		inside := false
		for _, scope := range m.grant.Policy.RowScopes(t.Effect) {
			inside = (content.GrantScope{Kind: scope.Kind, ID: scope.ID}).Contains(
				content.GrantScope{Kind: resource.Kind, ID: resource.ID},
			)
			if inside {
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
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
func (m *effectKernel) escalate(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, args map[string]any, resources []agenttools.ResourceRef, invocation content.Invocation) error {
	ap := m.proposalWithInvocation(decl.Name, callID, rawArgs, invocation)
	ap.CommandInvocation = decl.CommandArg != ""
	bindApprovalFileVersions(&ap, decl, resources)
	var entryID string
	if m.ledger != nil {
		id, err := m.recordProposal(ctx, decl, rawArgs, resources, ap, nil)
		if err != nil {
			return err
		}
		entryID = id
	}
	req := m.request(decl, callID, rawArgs, resources)
	req.CommandInvocation = decl.CommandArg != ""
	req.Invocation = cloneInvocation(invocation)
	req.ArgHash = ap.ArgHash
	req.EntryID = entryID
	// After the ledger record and before the latch: the person is shown the
	// values, and the SAME values are what the record binds.
	m.bindApprovalExpansions(ctx, &ap, req, decl, rawArgs)
	tripLatch(ctx, &ApprovalRequestedError{Request: req})
	if m.approvals != nil {
		ap.EntryID = entryID
		m.approvals.Request(ap)
	}
	return &ApprovalRequestedError{Request: req}
}

// escalateClassifier is escalate with the classifier's ledger fact: the
// ask the classifier caused — suspect, failed, or its input withheld by
// the egress gate — suspends the run BEFORE next, is RECORDED with the
// classifier block on the proposal (criterion 6: "why was this asked" is
// answerable from the ledger), and resumes through the SAME approval
// machinery as a policy ask: the person's yes covers the proposal
// INCLUDING its classification, and the resume skips a second
// consultation (the loop property).
func (m *effectKernel) escalateClassifier(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, ask *ApprovalRequest, fact *classifierFact, resources []agenttools.ResourceRef, invocation content.Invocation) error {
	ap := m.proposalWithInvocation(decl.Name, callID, rawArgs, invocation)
	ap.CommandInvocation = decl.CommandArg != ""
	bindApprovalFileVersions(&ap, decl, resources)
	var entryID string
	if m.ledger != nil {
		id, err := m.recordProposal(ctx, decl, rawArgs, resources, ap, fact)
		if err != nil {
			return err
		}
		entryID = id
	}
	ask.ArgHash = ap.ArgHash
	ask.CommandInvocation = decl.CommandArg != ""
	ask.Invocation = cloneInvocation(invocation)
	ask.Classifier = approvalClassifier(fact)
	ask.EntryID = entryID
	m.bindApprovalExpansions(ctx, &ap, ask, decl, rawArgs)
	if m.approvals != nil {
		ap.EntryID = entryID
		m.approvals.Request(ap)
	}
	return &ApprovalRequestedError{Request: ask}
}

// request builds the ask BOTH escalation sites send — the policy arm and the
// classifier arm alike. It takes the DECLARATION, not the tool name, because
// the effect and the resource come off the declaration the gate just decided
// with: one builder is what keeps a classifier ask from reaching the surface
// without an effect, which the notification's schema requires.
func (m *effectKernel) request(decl agenttools.Tool, callID, rawArgs string, resources []agenttools.ResourceRef) *ApprovalRequest {
	req := &ApprovalRequest{
		RunID:     m.runID,
		Attempt:   m.attempt,
		Tool:      decl.Name,
		CallID:    callID,
		Arguments: rawArgs,
		Effect:    decl.Effect,
		Resources: append([]agenttools.ResourceRef(nil), resources...),
		Resource:  matchedResource(resources),
	}
	if decl.Name == "skills.create" || decl.Name == "skills.update" {
		var params struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(rawArgs), &params); err == nil {
			if findings := skill.Scan([]byte(params.Body)); len(findings) > 0 {
				finding := findings[0]
				req.Finding = &SkillScanFinding{
					PatternID: finding.PatternID, Line: finding.Line, LineNumber: finding.LineNumber,
				}
			}
		}
	}
	return req
}

func approvalClassifier(fact *classifierFact) *ApprovalClassifier {
	if fact == nil {
		return nil
	}
	return &ApprovalClassifier{
		Consulted: fact.Consulted,
		Verdict:   fact.Verdict,
		Model:     fact.Model,
		Reason:    fact.Reason,
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

// screenResult runs the egress gate (design §7.1) over one tool's return —
// the success output or the error string alike — and returns the findings.
// Two detectors contribute, and both are the gate's, not second
// implementations (one recognizer, two policies): the masking service's
// heuristic pass, and the vault comparison through the run's KnownMaterial
// seam. A detection failure is an error: the caller withholds the result
// and fails the run — nothing leaves when the gate cannot see.
func (m *effectKernel) screenResult(ctx context.Context, out string, runErr error) ([]EgressFinding, error) {
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
func runNotWaitingResult(tool string) string {
	return "NOT WAITING: nocx did not run your call to " + tool +
		": that command is no longer waiting to be answered about — it finished on its own, or it was already stopped. " +
		"Nothing was changed by this call. Do not answer about it again; if you need to know how it ended, read the session."
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
func (m *effectKernel) recordProposal(ctx context.Context, decl agenttools.Tool, rawArgs string, resources []agenttools.ResourceRef, ap Approval, fact *classifierFact) (string, error) {
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
func (m *effectKernel) openAttempt(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, resources []agenttools.ResourceRef, classifierFact *classifierFact) (int64, string, error) {
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
		// The complete resolved resource list is stored with the attempt.
		// Keep the singular first-resource field for existing readers.
		if len(resources) > 0 {
			payloadBody["resources"] = resources
			payloadBody["resource"] = matchedResource(resources)
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
func (m *effectKernel) runWithRetained(decl agenttools.Tool, callID string, ctx context.Context, capability agenttools.Capability, rawArgs []byte) (string, error) {
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
		reader, ok := capability.(*agenttools.SessionReader)
		if !ok {
			return "", fmt.Errorf("tool %q: capability is %T, not dynamic session capability", decl.Name, capability)
		}
		var sessions SessionSource
		if m.requester != nil {
			sessions, _ = m.requester.(SessionSource)
		}
		return executeSessionRead(runCtx, reader, sessions, m.requester, rawArgs)
	case agenttools.InRenderer:
		return m.executeInRenderer(runCtx, decl, capability, rawArgs)
	case agenttools.InMCP:
		return executeMCP(runCtx, capability, rawArgs, m.runSeams.mcpRuntime)
	}
	fn, ok := executors[decl.Name]
	if !ok {
		return "", fmt.Errorf("tool %q has a capability constructor but no executor — a registration that cannot run", decl.Name)
	}
	return fn(runCtx, capability, rawArgs, m.seams())
}

// seams is the run's wiring handed to an InGo executor: what a tool needs
// that is neither its arguments nor its authority. The session record rides
// on the same value the renderer requests do, because the transport adapts one
// object for both (requester.go); a run with no requester wired hands over a
// nil source, and session.list says so rather than answering empty.
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

// Invoke runs ONE proposed effect through the whole pipeline and returns what
// the caller must hand back to whoever proposed it: the framed tool result,
// or the refusal — which is a RESULT and not an error (nocx-uvac6.1).
//
// It is the carrier-neutral entry point, and the only one. A caller that is
// not eino gets the identical decision, the identical attempt interval and
// the identical ledger shape, because there is no second copy of any of it.
//
// SUSPENSION IS AN ERROR VALUE, in our vocabulary rather than a framework's:
// *ApprovalRequestedError when a person must answer before the call may run,
// *EgressRequestedError when the call ran and its result may not leave until
// a person has seen what was found in it. Both carry the request the surface
// renders. The retained declared-call adapter translates those values into
// eino interrupts; that framework detail is not the kernel's business.
func (k *effectKernel) Invoke(ctx context.Context, name, callID, rawArgs string) (string, error) {
	out, err := k.invokeClassified(ctx, name, callID, rawArgs)
	if err != nil {
		return "", err
	}
	return out.text, nil
}

func (k *effectKernel) invokeClassified(ctx context.Context, name, callID, rawArgs string) (modelResult, error) {
	// 1. Declaration lookup. A name absent from the registry is malformed
	// model output, not a refusal — there is nothing to call.
	decl, ok := k.registry.Lookup(name)
	if !ok {
		return modelResult{}, fmt.Errorf("%w: unknown tool %q", ErrMalformedModelOutput, name)
	}
	if decl.Name == "skills.create" && k.runSeams.skillDraft != nil {
		generated, err := k.runSeams.skillDraft.arguments(ctx, k.runSeams.skillDraftHTTP)
		if err != nil {
			k.warn("agent tool: skill draft could not be generated", "error", err)
			return modelResult{
				text: "I could not draft this skill for approval because the summarizing model was unavailable or returned an unusable draft.",
				kind: modelNocxMessage,
			}, nil
		}
		rawArgs = generated
	}

	// 2. Parameter validation against the tool's schema: the file the
	// model was shown, byte for byte, plus the ingress size bound.
	if len(rawArgs) > maxArgsBytes {
		return modelResult{}, fmt.Errorf("%w: tool %q: arguments exceed the %d-byte bound", ErrMalformedModelOutput, decl.Name, maxArgsBytes)
	}
	args, err := k.validate(decl, rawArgs)
	if err != nil {
		return modelResult{}, fmt.Errorf("%w: tool %q: %v", ErrMalformedModelOutput, decl.Name, err)
	}
	// The mechanical call classifier is deliberately after validation and
	// before every policy/approval/ledger path. It selects one effect from the
	// declaration's reachable set; unresolved input keeps the set's worst
	// member. The returned invocation is the parser result reused by rule
	// matching; it is never re-tokenized.
	var invocation content.Invocation
	decl, invocation = classifyCall(decl, args)
	if decl.CommandArg == "" {
		// Non-command tools have no invocation rules, but their existing
		// matrix path remains unchanged.
		invocation.Parsed = true
	}

	resources, resourceDeclaration, err := k.resolveResources(decl, args)
	if err != nil {
		return modelResult{}, fmt.Errorf("%w: tool %q: resolve resources: %v", ErrMalformedModelOutput, decl.Name, err)
	}
	if reason, denied := k.floorRefusal(invocation, resources); denied {
		return modelResult{text: refusalResult(decl.Name, RefusedByFloor, "", reason), kind: modelNocxMessage}, nil
	}
	if decl.CommandArg != "" {
		if command, ok := args[decl.CommandArg].(string); ok {
			// The canonical parser intentionally splits shell operators for
			// rule matching. Re-check the raw command only for the floor's
			// exact self-replication signature, which would otherwise lose
			// those operator bytes during tokenization.
			if reason, denied := k.grant.Policy.FloorRawCommandRefusal(command); denied {
				return modelResult{text: refusalResult(decl.Name, RefusedByFloor, "", reason), kind: modelNocxMessage}, nil
			}
		}
	}
	// 3. Policy — permit / ask / refuse over the ADR-0020 lattice.
	//    FIRST, the person's own no (nocx-uvac6.1): the resume re-runs
	//    this very call through the pipeline, and the refusal is the
	//    call's result — the call must not run and must not be asked
	//    about again (the approval was answered; a re-ask would be the
	//    ask-forever loop the resume exists to end). Checked BEFORE
	//    decide, and the exact proposal FIRST: a standing no is answered
	//    with the person's own sentence, not the matrix's.
	if k.approvals != nil {
		if kind, declined := k.approvals.DeclinedKind(k.proposal(decl.Name, callID, rawArgs)); declined {
			return modelResult{text: refusalResult(decl.Name, RefusedByPerson, kind), kind: modelNocxMessage}, nil
		}
		if decl.CommandArg == "" {
			if kind, standing := k.approvals.DeclinedEffect(k.runID, decl.Effect); standing {
				return modelResult{text: refusalResult(decl.Name, RefusedByPerson, kind), kind: modelNocxMessage}, nil
			}
		} else if kind, standing := k.approvals.DeclinedInvocation(k.runID, invocation); standing {
			// A command standing no matches only the canonical
			// invocation that the person answered about.
			return modelResult{text: refusalResult(decl.Name, RefusedByPerson, kind), kind: modelNocxMessage}, nil
		}
	}
	outcome, refusal, floorReason := k.decideInvocationWithReason(decl, resources, resourceDeclaration, invocation)
	skillMutation := isSkillMutationTool(decl)
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
		return modelResult{text: refusalResult(decl.Name, refusal, "", floorReason), kind: modelNocxMessage}, nil
	case policyAsk:
		// Approval binds to the exact proposal: an approved call skips
		// the ask; a changed argument hashes differently and does NOT
		// resume under the old approval (design §7.2).
		if !skillMutation {
			ap := k.proposal(decl.Name, callID, rawArgs)
			if k.approvals != nil && k.approvals.IsApproved(ap) {
				break // the exact proposal was approved; verify before dispatch
			}
			return modelResult{}, k.escalate(ctx, decl, callID, rawArgs, args, resources, invocation)
		}

	}
	// 3b. The classifier (bead nocx-kpy23): a second, cheaper model
	// judges the proposed call and may only RAISE suspicion — permit →
	// ask — never lower it. Ordinary calls are consulted only where the
	// policy says permit; skill mutations are deliberately consulted
	// before their mandatory approval even when policy says ask. Refused
	// calls are never changed by a verdict, and their latency stays off a
	// path where a person is already waiting.
	var classifierFact *classifierFact
	if k.classifier != nil && !k.proposalApproved(decl.Name, callID, rawArgs) {
		ask, fact, classifyErr := k.classifyProposal(ctx, decl, callID, rawArgs, args, resources)
		if classifyErr != nil {
			// The classifier's INPUT gate could not see (the recognizer
			// failed closed): nothing decides this call unseen and
			// nothing leaves for the classifier — the run fails with a
			// terminal error, exactly as the result gate fails the run
			// when IT cannot see (step 7's screenErr path).
			return modelResult{}, fmt.Errorf("agent tool %q: classifier gate: %w", decl.Name, classifyErr)
		}
		if ask != nil {
			return modelResult{}, k.escalateClassifier(ctx, decl, callID, rawArgs, ask, fact, resources, invocation)
		}
		classifierFact = fact
	}
	if skillMutation && !k.proposalApproved(decl.Name, callID, rawArgs) {
		if classifierFact != nil {
			return modelResult{}, k.escalateClassifier(ctx, decl, callID, rawArgs, k.request(decl, callID, rawArgs, resources), classifierFact, resources, invocation)
		}
		return modelResult{}, k.escalate(ctx, decl, callID, rawArgs, args, resources, invocation)
	}

	// An approved proposal may reach here through either policyAsk or the
	// classifier's ask path; both must verify the captured versions now.
	if k.approvals != nil {
		ap := k.proposal(decl.Name, callID, rawArgs)
		if k.approvals.IsApproved(ap) {
			if verifyErr := k.approvals.VerifyApprovedFileVersions(ap); verifyErr != nil {
				return modelResult{text: refusalResult(decl.Name, RefusedFileChanged, "", verifyErr.Error()), kind: modelNocxMessage}, nil
			}
			// AND THE VALUES, IMMEDIATELY BEFORE SUBMITTING (nocx-4h0m7.5).
			// The verbatim command is what runs, so between reading a
			// variable for the question and running the command there is a
			// window. This closes it the only way that is not a rewrite:
			// read the values AGAIN and compare with what the person was
			// shown. It is a detector, not a fix — but it turns "silently
			// did something else" into "loudly refused", which is the trade
			// this repo makes everywhere else. Nothing expanded means
			// nothing to compare, and the call proceeds.
			if shown, ok := k.approvals.ApprovedExpansions(ap); ok && len(shown) > 0 {
				if verifyErr := VerifyExpansions(ctx, k.runSeams.expansions, k.runCtx.Session, shown); verifyErr != nil {
					return modelResult{text: refusalResult(decl.Name, RefusedExpansionChanged, "", verifyErr.Error()), kind: modelNocxMessage}, nil
				}
			}
		}
	}

	// 4. The attempt is written BEFORE the call. If that write fails, no
	// capability is constructed, next is not called, and the run fails
	// with a terminal infrastructure error — an interrupted run can
	// never be told "this may already have happened" when it cannot.
	execID, entryID, err := k.openAttempt(ctx, decl, callID, rawArgs, resources, classifierFact)
	if err != nil {
		return modelResult{}, fmt.Errorf("agent tool %q: record attempt: %w", decl.Name, err)
	}

	// 5. The narrowed capability is constructed. The tool holds only
	// this; it cannot exceed the grant because it never has more
	// (ADR-0028 decision 4 — a check would leave it holding a full
	// manager). A tool with no Narrow is declared-but-not-executable
	// and is refused here, honestly.
	if decl.Narrow == nil {
		_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
		return modelResult{}, fmt.Errorf("agent tool %q is declared but not executable: no capability constructor is wired", decl.Name)
	}
	capability, err := decl.Narrow(k.grant, resources, k.runCtx)
	if err != nil {
		_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
		return modelResult{}, fmt.Errorf("agent tool %q: construct capability: %w", decl.Name, err)
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
	// resolved resources derived from them, and never the result (see
	// ToolCall's doc for why the result is left off). The arguments are
	// the VALIDATED object from step 2, not the raw string: what is
	// announced is what ran, and step 2 is where "what ran" was settled.
	// Announced once per EXECUTION, so an approved egress resume — which
	// passes the same call through this pipeline a second time —
	// announces the same CallID again; the renderer keys on it and
	// renders one call.
	if k.onCall != nil {
		if err := k.onCall(ToolCall{
			Tool:       decl.Name,
			CallID:     callID,
			Args:       args,
			EntryID:    entryID,
			Effect:     decl.Effect,
			Resource:   matchedResource(resources),
			OpensBlock: decl.OpensBlock,
		}); err != nil {
			// The caller refused the write, which is the one thing that
			// stops a run: the same contract onEvent has for a delta.
			// The attempt is closed rather than left open — the
			// interval closes with a terminal reason, never silently.
			_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return modelResult{}, err
		}
	}

	// 6. Execution — in Go, against the narrowed capability. An
	// APPROVED egress resume does not re-run the tool: the result that
	// was withheld and shown to the person is retained (design §7.1's
	// "send it as it is"), and re-running would repeat the effect and
	// could produce a different result than the one approved.
	out, runErr := k.runWithRetained(decl, callID, ctx, capability, []byte(rawArgs))
	// 6b. The result is held to what the tool DECLARES it returns, before
	// anything reads it — the egress gate or the model. Only a successful call
	// has a result to check.
	if runErr == nil {
		if err := k.checkResult(decl.Name, out); err != nil {
			k.warn("agent tool: the result does not match its own contract",
				"tool", decl.Name, "call", callID, "error", err)
			_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return modelResult{}, &ToolFailedError{Tool: decl.Name, Err: err}
		}
	}

	// 7. Result ingest — the egress gate (design §7.1) FIRST, then the
	// window and the size bound. The gate screens EVERY return path
	// before the bytes leave for the provider, the success and the
	// error alike: an error string is output too — it carries paths,
	// hostnames and names, and a gate that screens successes and not
	// failures has closed the wide door and left the narrow one open.
	egress, screenErr := k.screenResult(ctx, out, runErr)
	if screenErr != nil {
		// Detection failed: the result is withheld and the run fails —
		// the masking service's fail-closed contract, and the gate's:
		// nothing leaves when the gate cannot see.
		_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
		return modelResult{}, &EgressScreeningError{Tool: decl.Name, Gate: "egress", Err: screenErr}
	}
	if len(egress) > 0 {
		ap := k.proposal(decl.Name, callID, rawArgs)
		// The approved egress resume: the EXACT result the person
		// approved sending is what was screened. Nothing re-ran; the
		// bytes go as decided and the retention is dropped. An
		// approval of the POLICY gate is not an approval of this gate —
		// a call approved at the policy step whose result carries a
		// finding still suspends here (design §7.3: two gates, one
		// surface; each asks once).
		approvedResume := false
		if k.approvals != nil {
			if _, _, retained := k.approvals.RetainedResult(ap); retained {
				k.approvals.ClearRetained(ap)
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
			req := k.egressRequest(decl, callID, rawArgs, resources, egress, runErr != nil)
			if k.approvals != nil {
				ap.EntryID = entryID
				// Egress approval releases an already-retained result; it
				// does not authorise another file operation.
				ap.FileVersionState = FileVersionBindingNotApplicable
				k.approvals.Request(ap)
				// The withheld result is retained so the approved
				// resume sends the EXACT bytes the person was shown —
				// never a re-run's freshly produced ones.
				k.approvals.Retain(ap, out, runErr != nil)
			}
			// The attempt of THIS pass closes as interrupted: the call
			// ran and its result was withheld pending the decision; the
			// approved call is a SUBSEQUENT attempt of the same entry.
			_ = k.closeAttempt(ctx, execID, content.TermInterrupted, content.EntryInterrupted)
			return modelResult{}, &EgressRequestedError{Request: req}
		}
	}

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
	// this result and either passed it or suspended the run; recording
	// before it would put bytes in the store that the gate might still
	// refuse to let leave. On an APPROVED resume the bytes do carry
	// findings, which is why the record is masked as well as gated — the
	// belt matters at exactly the one moment the gate has stood down.
	//
	// NOT FOR A CALL THAT OPENED A BLOCK. ADR-0040 is explicit that the
	// block the command opened IS the account of that call, and the turn
	// draws no child for it at all — so a body here would be a second copy
	// of that command's own output, stored for a surface that will never
	// ask for it.
	if runErr == nil && !decl.OpensBlock {
		k.recordToolResult(ctx, entryID, out)
	}

	// A person's Stop is a successful tool exchange carrying an explicit
	// renderer fact, not a tool error. Preserve that fact in the action
	// ledger: the command's own exit code may also be 130, so it cannot
	// select the user-killed reason. The result remains model-visible so the
	// model is told not to retry it.
	if runErr == nil && runResultStopped(decl.Name, out) {
		if err := k.closeAttempt(ctx, execID, content.TermUserKilled, content.EntryInterrupted); err != nil {
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
			_ = k.closeAttempt(ctx, execID, content.TermCompleted, content.EntrySuccess)
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
			_ = k.closeAttempt(ctx, execID, content.TermFailed, content.EntryFailure)
			return modelResult{text: runNotWaitingResult(decl.Name), kind: modelNocxMessage}, nil
		}
		var leaseErr *RunLeaseError
		if errors.As(runErr, &leaseErr) {
			_ = k.closeAttempt(ctx, execID, leaseErr.Reason, content.EntryFailure)
			if leaseErr.SubmissionExpired {
				return modelResult{}, leaseErr
			}
			return modelResult{text: leaseResult(decl.Name, leaseErr), kind: modelToolOutput}, nil
		}
		_ = k.closeAttempt(ctx, execID, terminationReasonOf(runErr), content.EntryFailure)
		// Named, so the transport can say WHICH tool failed without
		// stringifying the framework's wrapper around it.
		return modelResult{}, &ToolFailedError{Tool: decl.Name, Err: runErr}
	}

	if err := k.closeAttempt(ctx, execID, content.TermCompleted, content.EntrySuccess); err != nil {
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

// The two kernel methods below live HERE and not in classifier.go, where
// they were written. That file talks to the classifier model and therefore
// imports eino; a kernel method declared in it would put the framework back
// inside the kernel's own boundary, which kernel_test.go's guard reads over
// the RECEIVER precisely so that a move like this is what it catches.
// proposalApproved reports whether this exact proposal already carries a
// human's approval. The approval covers the proposal INCLUDING its
// classification — a person approved the call as proposed — so the resumed
// pass must not consult the classifier again: a second suspect verdict
// would ask about a call the person just answered, forever.
func (k *effectKernel) proposalApproved(toolName, callID, rawArgs string) bool {
	if k.approvals == nil {
		return false
	}
	return k.approvals.IsApproved(k.proposal(toolName, callID, rawArgs))
}

// classifyProposal runs the classifier gate over one permitted call. The
// order is the invariant's own:
//
//  1. The egress gate on the INPUT (invariant 2) — the classifier is an
//     egress point, so its arguments pass the SAME gate that screens the
//     answering model's input: the same recognizer, the same vault
//     comparison. A finding means the classifier CANNOT be shown the
//     arguments — the call escalates with the findings recorded, and the
//     material never leaves.
//  2. The consultation. Any error — unreachable, timed out, unparseable,
//     unresolved role — escalates with the failure sentence recorded. A
//     verdict other than ClassifierClear escalates with the verdict, the
//     model and the masked reason. ClassifierClear alone lets the call
//     run.
//
// The returned ask is the suspension's approval request; the returned fact
// is what the ledger records. A nil ask means the verdict was clear.
func (k *effectKernel) classifyProposal(ctx context.Context, decl agenttools.Tool, callID, rawArgs string, args map[string]any, resources []agenttools.ResourceRef) (*ApprovalRequest, *classifierFact, error) {
	findings, err := k.screenResult(ctx, rawArgs, nil)
	if err != nil {
		return nil, nil, err
	}
	if len(findings) > 0 {
		fact := &classifierFact{
			Findings: findings,
			Reason:   "the classifier could not be consulted: " + findingsSentence(findings),
		}
		return k.request(decl, callID, rawArgs, resources), fact, nil
	}
	classification, err := k.classifier.Classify(ctx, ClassifyInput{Tool: decl.Name, CallID: callID, Arguments: rawArgs})
	if err != nil {
		fact := &classifierFact{
			Reason: maskClassifierReason("the classifier could not be consulted: " + summarizeClassifierError(err)),
		}
		return k.request(decl, callID, rawArgs, resources), fact, nil
	}
	if classification.Verdict != ClassifierClear {
		fact := &classifierFact{
			Consulted: true,
			Verdict:   classification.Verdict,
			Model:     classification.Model,
			Reason:    maskClassifierReason(classification.Reason),
		}
		return k.request(decl, callID, rawArgs, resources), fact, nil
	}
	return nil, &classifierFact{
		Consulted: true,
		Verdict:   ClassifierClear,
		Model:     classification.Model,
	}, nil
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
