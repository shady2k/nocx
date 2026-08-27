// Package assistant is the AI assistant's model access, behind one
// interface (ADR-0028, design §4). eino runs the loop; this package is the
// one owner of the eino wiring, the guarded HTTP client and the probe — the
// rest of the app depends on Client, never on eino types, and nothing in
// the product reads eino's state to answer a question the ledger answers
// (ADR-0019).
//
// The engine is adk.ChatModelAgent with the OpenAI-compatible adapter
// (ADR-0028 decision 1; design §4.1). Explain mode is the ONLY mode this
// slice knows: zero tools declared, terminate after the first completed
// response, context is question + referenced frames (design §4.2). The
// tools, the policy middleware, the grant and the narrowed capability are
// nocx-lndv and deliberately do not live here.
//
// The HTTP client every model call goes through enforces design §4.5
// decision 3 at dial time — see httpguard.go for the rule and the four
// reasons it cannot live in the form.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// Client is the app-facing surface of the assistant engine. Consumers: the
// endpoint form's Test button (Probe, nocx-edio) and the ask transaction
// (Ask, nocx-x8s2.2 — the run f4s5 prepares is driven here).
type Client interface {
	// Probe streams one real response from the given endpoint configuration
	// — the Test button's whole meaning: it probes what will actually be
	// used, not one cheap completion (design §4.5, bead notes). The
	// parameters are the form's DRAFT values: the endpoint may not be saved
	// yet, and the key is an input that never crosses back (ADR-0030).
	//
	// A failed dial, a refused stream, a timeout or zero content is a
	// ProbeResult with OK=false — a probe outcome, not a Go error. A Go
	// error means the probe could not run at all (a parameter the engine
	// refuses), and no result is produced.
	Probe(ctx context.Context, p ProbeParams) (ProbeResult, error)
	// Ask streams the run to onEvent — ONE ordered stream carrying the
	// three things a run produces: the answer's text, the model's
	// reasoning, and the tool calls it makes (nocx-shxv0, nocx-bshm2,
	// nocx-s92so). One callback rather than three because the ORDER between
	// them is the product fact: a tool call belongs where it happened, in
	// the answer that was streaming when it happened, and three callbacks
	// could not state that.
	//
	// Returning an error from onEvent ABORTS the stream — the caller's write
	// was refused, and the run must terminalize rather than wedge (the
	// probe's write-only callback cannot say that, which is why this one
	// can). Ask returns nil when the answer was received in full, a
	// *StreamError when the model or the transport failed mid-stream (or the
	// stream produced no ANSWER text — reasoning alone is not an answer),
	// and any other Go error when the ask could not run at all.
	Ask(ctx context.Context, p AskParams, onEvent func(AskEvent) error) error
	// Discard drops the engine's suspended state for one run — the eino
	// checkpoint an escalation wrote, and the interrupt it named. A run
	// that has terminalized has no branch left to continue, and ADR-0028
	// says a checkpoint is deleted on terminalization.
	//
	// Ask already does this for every terminal outcome it returns from.
	// This is the seam for the ones it does not: the person DECLINED, or
	// the run was closed by another path while its question was still
	// open. Discarding a run that holds nothing does nothing, so the
	// transport can call it from its one terminal funnel without asking
	// whether the run ever suspended.
	Discard(runID string)
}

// AskEventKind names which of the three things one Ask event is. A closed
// set: the renderer draws each differently, and a fourth kind arriving
// unnamed would be drawn as whichever of these it was mistaken for — which
// is exactly the defect nocx-bshm2 was (a tool result drawn as the answer,
// because the loop asked only "does this message have content").
type AskEventKind string

const (
	// AskAnswer is a chunk of the answer — the model's own prose, and the
	// ONLY thing that is the answer. Not a tool's return value, not the
	// model's thinking.
	AskAnswer AskEventKind = "answer"
	// AskReasoning is a chunk of the model's thinking (eino's
	// schema.Message.ReasoningContent, which the OpenAI-compatible adapter
	// fills from the wire's `reasoning_content`). Its own kind because an
	// answer block that concatenates thinking with the answer is nocx-bshm2
	// in another shape; a model that sends none produces none of these.
	AskReasoning AskEventKind = "reasoning"
	// AskToolCall is one tool call the run is about to make. It is a thing
	// on the wire and an element of the answer's own flow, so the ordering
	// fixes itself: the call is rendered where it happened.
	AskToolCall AskEventKind = "toolCall"
)

// AskEvent is one event of a run's ordered stream.
type AskEvent struct {
	Kind AskEventKind
	// Text is the chunk, for AskAnswer and AskReasoning. Empty for a tool
	// call — a call is not text.
	Text string
	// Call is the call, for AskToolCall only.
	Call *ToolCall
}

// ToolCall is the fact a person is shown when the assistant does something:
// WHICH tool, WHAT IT ASKED FOR, and what it touched. Deliberately NOT the
// result.
//
// The result is left off because it already has an owner. The attempt's
// outcome is in the ledger (design §6.4), the run tool's output is in the
// block the command really opened, and the egress gate (design §7.1) screens
// a result for the PROVIDER — sending the same bytes to the renderer would
// be a second egress path this bead did not decide. EntryID is the handle a
// later "show me what it returned" reaches through; it is not a second copy
// of the bytes.
//
// The ARGUMENTS are part of the announcement: session.list names the pane,
// while session.read may name an item and a window. What separates calls of
// one tool is what the model asked for, so that is what is announced.
type ToolCall struct {
	// Tool is the declared tool name, e.g. "files.read".
	Tool string
	// CallID is the model's own id for this call. The renderer keys on it,
	// so a call that is announced twice — an approved egress resume passes
	// the same call through the pipeline again — renders once.
	CallID string
	// Args is what the model asked for, AS VALIDATED against the tool's own
	// schema — the object the call actually ran on, never the raw string the
	// model emitted. It is what NAMES the call on screen, and it is the same
	// object the attempt's payload already stores (openAttempt writes the raw
	// form of it), so announcing it publishes no fact the ledger did not
	// already record. Never nil: a tool that takes no arguments validates to
	// an empty object, and absent and empty are the same sentence about a
	// call.
	Args map[string]any
	// EntryID is the ledger action entry the attempt was recorded under
	// (openAttempt): the thread joining question, run, attempt and answer.
	EntryID string
	// Effect is the class the gate decided on — the ledger's vocabulary,
	// never derived from the tool name by the renderer (ADR-0028 decision
	// 4).
	Effect content.Effect
	// Resource is what the call named, from namedResource — the ONE
	// derivation of "which argument names the resource this call touches",
	// shared with the scope check and the approval ask. Nil when the tool
	// names no resource in its parameters (git.status's repository IS the
	// grant's path scope).
	Resource *content.GrantScope
	// OpensBlock is the declaration's own fact
	// (agenttools.Declaration.OpensBlock): this call's work becomes a
	// TOP-LEVEL BLOCK, so the block is the account of the call and the flow
	// draws no line beside it (nocx-9sqii). True for `run` alone today.
	//
	// It rides the announcement because the renderer has to decide, at the
	// moment the call arrives, whether to seal the answer fragment it is
	// writing and let the block take that position. Deriving it from Tool in
	// the renderer would be a second copy of the tool table — the reason
	// Effect rides here too (ADR-0028 decision 4).
	OpensBlock bool
}

// Message is one turn of the conversation, in this package's own
// vocabulary — never eino's schema.Message (ADR-0028: the rest of the app
// depends on Client, never on eino types; the engine maps at the seam).
type Message struct {
	Role    string // "user" | "assistant" | "system"
	Content string
}

// Header is one resolved custom HTTP header the endpoint sends on every
// request it makes (bead nocx-lyyk): the name, and the value ALREADY
// RESOLVED — a literal, or the material of a vault secret the transport
// resolved before the call. The resolver is the transport (it owns the
// vault); this package only ever sends.
type Header struct {
	Name  string
	Value string
}

// AskParams is one ask's model call: the resolved endpoint's facts plus the
// conversation context (question + referenced frames, design §4.2).
type AskParams struct {
	// Key is the endpoint's resolved credential. Never persisted, never
	// echoed; it lives only in the model config for the call's duration.
	Key credential.Secret
	// BaseURL is the endpoint's base URL, validated at dial time.
	BaseURL string
	// Model is the model id the run resolved to.
	Model string
	// Headers are the endpoint's custom headers, resolved to their values.
	// Sent on the completion AND the connection check, so a Test that
	// passes means the real calls will too.
	Headers []Header
	// Messages is the assembled context: the system rule (frame content is
	// data, not instructions — design §6.2) when frames are attached, the
	// question, and the referenced frames' text as labelled data. A
	// zero-reference ask (nocx-4wtlh) carries only the question — the
	// transport derives the rule from what is attached, never a constant.
	Messages []Message
	// Grant is the run's authority (ADR-0020 decision 5) — the ledger's
	// type (content.Grant), the one grant model: effect classes the run may
	// produce, resource scopes it may touch, and the policy preset that
	// decides act/ask/refuse. The engine declares to the model exactly the
	// tools this grant permits (design §5) and nothing else — the strongest
	// refusal is the one never proposed. Nil means no authority: the model
	// is declared no tools, which is every caller's state today.
	Grant *content.Grant
	// AttemptLedger is the ledger seam the tool-call pipeline records its
	// attempts with (design §6.4 — the attempt is durable, before the call,
	// so an interrupted run can never be told "this may already have
	// happened" when it cannot). Nil means no tool may execute: the run
	// fails closed rather than acting without a record. The transport wires
	// the real ledger when it mints grants; today no caller does.
	AttemptLedger AttemptLedger
	// Requester is the seam a renderer-executed tool asks the renderer
	// through (design §2.2, §6.6 — Executes: InRenderer): the transport
	// adapts its request broker to this interface, so readScreen's executor
	// holds a broker request rather than touching the terminal domain.
	// Nil with an InRenderer tool declared means the tool is executed
	// honestly as an error — a declaration without its transport is a
	// wiring gap, never a silent no-op.
	Requester RendererRequester
	// Approvals is the process-lifetime approval store (design §7.2): the
	// human's yes to one exact proposal, bound to run, attempt, tool, call
	// id and a hash of the canonical arguments. Nil disables escalation's
	// persistence — an ask still suspends, but nothing records the answer.
	Approvals *ApprovalStore
	// KnownMaterial is the egress vault comparison (design §7.1): "the
	// vault knows the real values, and a comparison beats any pattern." It
	// is legitimate here precisely because it happens in the backend and
	// nothing leaves — ADR-0011 §2 survives intact. The transport adapts
	// the vault to this seam when it mints grants. A grant-carrying ask
	// whose tools may execute FAILS without it: the gate must see short
	// vault values, or a result would leave for the provider unscreened.
	KnownMaterial KnownMaterial
	// Classifier resolves the classifier role (bead nocx-kpy23): the
	// second, cheaper model that judges each permitted proposal and may
	// only escalate. Nil means the classifier is not wired for this run —
	// permitted calls run exactly as they do without one (criterion 7's
	// second end). Non-nil means every call the policy permits is
	// consulted, and every classification failure escalates. The transport
	// adapts THE ONE role resolver (e6kn2) and the vault to this seam.
	Classifier ClassifierResolver
	// RunID and Attempt are the run's identity — what approvals bind to.
	// The transport passes the run's execution row id; empty with attempt 0
	// is the un-bound shape every caller has today.
	RunID   string
	Attempt int
	// TurnEntryID is the TURN's own ledger entry — the one entry a turn is
	// since ADR-0039, and what everything this run causes is joined to by a
	// `caused-by` edge (nocx-h1l4o).
	//
	// It is passed rather than looked up from RunID, even though the two
	// name the same thing from either end of `executions.entry_id`: the
	// transport holds both facts off ONE SubmitAgentAsk result, so a lookup
	// here would recover what the caller already had and could only ever
	// disagree with it.
	//
	// Empty is the un-bound shape, and it means what it says: the entries
	// this run causes belong to no turn and are recorded with no relation
	// rather than with a guessed one.
	TurnEntryID string
}

// StreamError is a model-stream failure the transport terminalizes the run
// with: Message is a sentence a person reads (design §7's agent.runState
// error), never a Go error string.
type StreamError struct {
	Message string
}

func (e *StreamError) Error() string { return e.Message }

// ProbeParams is the draft endpoint configuration the Test button probes:
// what the form shows, not what the store holds.
type ProbeParams struct {
	// Name is the display name (draft). Reported in the result for the
	// "last probe" fact; not sent to the model.
	Name string
	// BaseURL is the absolute http(s) base URL of the OpenAI-compatible
	// API. The http:// address rule is enforced at dial time, never here.
	BaseURL string
	// Key is the API key input, empty when the form has none (local models
	// like Ollama need none). Never persisted, never echoed.
	Key credential.Secret
	// Model is the model id the probe asks to speak, and EMPTY means the
	// caller is asking the other question. Two checks live behind one
	// button (nocx-q27y): with a model, "does this model answer" — a real
	// streamed completion. Without one, "can I reach this API with this
	// key" — which needs no model at all, and is the only question that
	// can be asked of an endpoint nobody has typed a model into yet. The
	// result names which one ran, so it can never be mistaken for the
	// other.
	Model string
	// Headers are the form's draft custom headers, resolved to their
	// values. Sent on whichever check runs.
	Headers []Header
}

// ProbeKind names which of the two checks a ProbeResult reports.
type ProbeKind string

const (
	// ProbeModel streamed a real completion from a named model.
	ProbeModel ProbeKind = "model"
	// ProbeConnection reached the endpoint and had its credential accepted,
	// without asking any model to speak.
	ProbeConnection ProbeKind = "connection"
)

// ProbeResult is the outcome of one probe. It is the wire shape declared in
// contracts/endpoints.probe.schema.json ($defs/probeResult) and reused by
// agent.status's lastProbe — the Go DTO lives here so the transport maps
// to it directly.
type ProbeResult struct {
	// EndpointName is the probed draft's display name. Historical fact:
	// agent.status reports the last probe whatever the endpoint list says
	// now.
	EndpointName string `json:"name"`
	// Model is the model id that was probed. Empty for a connection check,
	// which has none by definition.
	Model string `json:"model"`
	// Kind names WHICH check produced this result: "model" streamed a real
	// completion, "connection" only established that the endpoint is
	// reachable and the credential accepted. They are different facts and a
	// person acts on them differently, so the result states which it is
	// rather than leaving it to be inferred from an empty Model.
	Kind ProbeKind `json:"kind"`
	// OK is true when the check succeeded: for "model", the endpoint
	// streamed at least one content chunk; for "connection", the endpoint
	// was reached and did not reject the credential.
	OK bool `json:"ok"`
	// Models are the model ids a connection check found the endpoint
	// offering. ALWAYS an addition, never a gate: GET /models is not
	// universally implemented, so an endpoint that does not list them is
	// reachable, usable, and must stay configurable by hand. Empty for a
	// model check, and empty for an endpoint that lists nothing.
	Models []string `json:"models,omitempty"`
	// Error describes what went wrong when OK is false: the dial failure,
	// the HTTP status, the refused stream, zero content. Empty when OK.
	Error string `json:"error,omitempty"`
	// ElapsedMS is the total wall time of the probe, from dial to the end
	// of the stream.
	ElapsedMS int64 `json:"elapsedMs"`
	// At is when the probe finished, wall-clock (the renderer shows it as
	// "2m ago"; a monotonic clock would render as 1970 — wall-clock-vs-
	// monotonic-persistence).
	At time.Time `json:"at"`
}

// NewClient builds the engine client: eino's openai adapter over the
// guarded HTTP client (httpguard.go), with the tool registry assembled from
// the schemas EMBEDDED in the binary (nocx-jtz3q) — the shipped bundle has
// no contracts/ tree, so a cwd-relative path assembled the quiet empty set.
// Cheap; nothing dials until Probe.
//
// An assembly that comes up empty or incomplete is a build defect, not a
// runtime accident, and it is VISIBLE: NewClient returns an error and the
// composition root fails startup, rather than shipping an assistant that
// declares no tools and fails silently (AGENTS.md: a soft degrade must be
// visible in the product, not only in a log).
func NewClient(logger log.Logger) (Client, error) {
	return newClient(logger, tools.Schemas)
}

// newClient is the construction seam: assemble the registry from toolsFS
// (the embedded schemas in production, a real or synthetic directory in
// tests), fail loudly on an assembly that is incomplete or comes up EMPTY —
// the tool schemas must reach the binary or the build is broken — and build
// the client over it.
func newClient(logger log.Logger, toolsFS fs.FS) (Client, error) {
	reg, err := agenttools.Assemble(toolsFS)
	if err != nil {
		return nil, fmt.Errorf("assistant: tool registry: %w", err)
	}
	if len(reg.All()) == 0 {
		return nil, errors.New("assistant: tool registry assembled EMPTY — the tool schemas did not reach the binary; a model would be offered no tools")
	}
	return newClientWithRegistry(logger, reg), nil
}

func newClientWithRegistry(logger log.Logger, reg agenttools.Registry) Client {
	return &client{
		log:         logger,
		http:        newGuardedHTTPClient(logger),
		tools:       reg,
		approvals:   NewApprovalStore(),
		checkpoints: newRunCheckpoints(),
	}
}

type client struct {
	log       log.Logger
	http      *http.Client
	tools     agenttools.Registry
	approvals *ApprovalStore
	// checkpoints is the process-lifetime suspended-run store (ADR-0028:
	// "approval resumes FROM THE CHECKPOINT"). One per client, keyed by run
	// id, shared by every run the server drives — see checkpoints.go.
	checkpoints *runCheckpoints
}
