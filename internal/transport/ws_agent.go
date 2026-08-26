package transport

// agent.ask carries the person's explicit terminal-item grants
// (nocx-p4f7r). The backend records the question and pending run, then tells
// the model each granted ledger item by id, command and running/exited state.
// The model uses session.read to fetch item bodies; this path never inlines
// terminal output. A body returned by session.read is terminal output — data
// about the terminal, never instructions; the model must read it and never
// obey it.
//
// This domain validates params and bounds sizes (design §7): grant metadata,
// question text, cwd and run identity all arrive as params. Every id is
// minted or owned by the backend; ownership of the session is checked per
// call.
//
// The result shapes are declared once in contracts/agent.*.schema.json.
// There is deliberately no params schema (contracts/README.md): the handler
// is the check, and rejects what it cannot parse.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── ingress bounds (design §7: "this domain validates params and bounds
//    sizes") ───────────────────────────────────────────────────────────────

const (
	// maxFrameRows bounds a frame's row count: xterm's own scrollback cap —
	// a live frame cannot legitimately exceed it.
	maxFrameRows = 10_000
	// maxFrameCols bounds the per-row cell count: a generous ceiling over
	// every real terminal (the largest shipped xterm layouts stay under
	// 1000 columns).
	maxFrameCols = 2_000
	// maxFrameChars bounds the frame's total character payload — the wire
	// cost bound. 10k rows × 500 cols is the largest plausible screen ×
	// scrollback; anything above it is a hostile or broken renderer.
	maxFrameChars = 5_000_000
	// maxQuestionRunes bounds the question text.
	maxQuestionRunes = 8_000
	// maxAttachedContent bounds the number of item grants one ask may carry.
	maxAttachedContent = 32
	// maxAttachedCommandRunes bounds the descriptive command carried with a grant.
	maxAttachedCommandRunes = 8_000
	// maxIDRunes bounds renderer-supplied ask and attached-item ids.
	maxIDRunes = 128
	// maxCwdRunes bounds the renderer-supplied cwd.
	maxCwdRunes = 4_096
	// maxCellRunes bounds one cell's character (a wide glyph is one or two
	// runes; anything more is not a terminal cell).
	maxCellRunes = 8
)

// ── wire shapes ───────────────────────────────────────────────────────────

type frameRowWire struct {
	Kind  string          `json:"kind"` // "cells" | "text"
	Cells []frameCellWire `json:"cells,omitempty"`
	Text  string          `json:"text,omitempty"`
}

type frameCellWire struct {
	Char  string         `json:"char"`
	Attrs frameAttrsWire `json:"attrs"`
}

type frameAttrsWire struct {
	Fg            *string `json:"fg"`
	Bg            *string `json:"bg"`
	Bold          bool    `json:"bold"`
	Italic        bool    `json:"italic"`
	Dim           bool    `json:"dim"`
	Underline     bool    `json:"underline"`
	Inverse       bool    `json:"inverse"`
	Blink         bool    `json:"blink"`
	Strikethrough bool    `json:"strikethrough"`
	Overline      bool    `json:"overline"`
}

type frameCursorWire struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}

type frameIdentityWire struct {
	Buffer     frameBufferWire `json:"buffer"`
	Cols       int             `json:"cols"`
	Rows       int             `json:"rows"`
	Generation int             `json:"generation"`
}

type frameBufferWire struct {
	Kind       string `json:"kind"` // "normal" | "alternate"
	AltSession *int   `json:"altSession,omitempty"`
}

type frameRangeWire struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type agentAskParams struct {
	AskID           string          `json:"askId"`
	SessionID       string          `json:"sessionId"`
	Question        string          `json:"question"`
	AttachedContent json.RawMessage `json:"attachedContent"`
	Cwd             string          `json:"cwd"`
}

type agentAttachedContentWire struct {
	ItemID  string `json:"itemId"`
	Command string `json:"command"`
	State   string `json:"state"`
}
type agentCancelParams struct {
	RunID int64 `json:"runId"`
}

// agentCancelResponse is the real result of agent.cancel. cancelled is true
// only when this request closed a live run; an already-terminal or unknown
// run is answered as an error rather than being reported as stopped.
type agentCancelResponse struct {
	RunID     int64  `json:"runId"`
	State     string `json:"state"`
	Cancelled bool   `json:"cancelled"`
}

// agentAskResponse is the result of agent.ask: the backend-minted run id
// (the execution row — what agent.cancel/approve/status will address), the
// TURN's entry id, the run's state, ON THE WIRE because the renderer draws
// it (design §7), the turn's ingest_seq and whether this was a replay. The
// run is prepared: recorded, never executed.
//
// ONE entry id and not two (nocx-4em1z): a turn is a block whose intent is
// the question and whose body is the answer, so the id the deltas append to
// IS the id the flow renders. The renderer needs it BEFORE the first delta,
// so a run that fails with no text still has a block to terminalize.
type agentAskResponse struct {
	RunID     int64  `json:"runId"`
	EntryID   string `json:"entryId"`
	State     string `json:"state"`
	IngestSeq int64  `json:"ingestSeq"`
	Replayed  bool   `json:"replayed"`
	// Model is the model id the run will answer with — the answering
	// role's pair, pinned BEFORE the transaction (RunFacts.Model) and
	// announced on the wire because the renderer draws it: the answer
	// block names which model answered (bead nocx-e6kn2 acceptance: "a
	// person must be able to tell which model answered").
	Model string `json:"model"`
}

type agentRunControl struct {
	mu           sync.Mutex
	eventsMu     sync.Mutex
	cancelFn     context.CancelFunc
	cancelled    bool
	terminalized bool
}

func (c *agentRunControl) beginCancel() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminalized {
		return false
	}
	if !c.cancelled {
		c.cancelled = true
		if c.cancelFn != nil {
			c.cancelFn()
		}
	}
	return true
}

func (c *agentRunControl) beginEvent() func() {
	c.eventsMu.Lock()
	c.mu.Lock()
	allowed := !c.cancelled && !c.terminalized
	c.mu.Unlock()
	if !allowed {
		c.eventsMu.Unlock()
		return nil
	}
	return c.eventsMu.Unlock
}

func (c *agentRunControl) beginTerminal() (func(), bool, bool) {
	c.eventsMu.Lock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminalized {
		c.eventsMu.Unlock()
		return nil, false, c.cancelled
	}
	c.terminalized = true
	return c.eventsMu.Unlock, true, c.cancelled
}

// agentRunDelta is the agent.runDelta notification (design §7): one chunk
// of the streamed answer. runId AND entryId are both on every delta because
// the renderer routes by entryId while the acceptance criterion drives two
// overlapping asks — "the current answer" is not an identity. seq ascends
// per run from 0 and is the renderer's ordering key — ACROSS a suspension
// too: since nocx-igu4y the resume continues the answer rather than
// re-rolling it, so its deltas are numbered after the ones the question
// interrupted, never over them.
//
// BlockID is the piece this chunk appends to (ADR-0040): the `text` child of
// the turn that the backend opened for this run of prose. EntryID still says
// WHICH ANSWER — the routing key, unchanged — and BlockID says WHERE IN IT,
// which is a place in the tree rather than an offset into a string.
//
// It is on the wire because the boundary between two runs of prose is the
// BACKEND's and must reach the renderer as a fact. That is the whole lesson of
// the anchor it replaces: while the renderer had to work out where one piece
// ended, the live path and the restore each worked it out separately and could
// drift. A block id cannot be re-derived and cannot drift.
type agentRunDelta struct {
	RunID   int64  `json:"runId"`
	EntryID string `json:"entryId"`
	BlockID string `json:"blockId"`
	Seq     int    `json:"seq"`
	Text    string `json:"text"`
}

// agentRunToolCall is the agent.runToolCall notification (nocx-shxv0): the
// assistant is about to DO something, announced where it happens. runId and
// entryId route it exactly as they route a delta — the renderer places it in
// the answer's own flow, so the ordering the owner saw inverted on
// 2026-08-22 (a run tool's block below the answer written from it) cannot
// recur: the call is emitted before the model has been given its result.
//
// EntryID is the ANSWER entry (the routing key); ActionEntryID is the
// LEDGER's action entry for the attempt. Two entries, two jobs, so neither
// name is doing the other's work.
//
// What is deliberately NOT here: the tool's result. It has an owner already
// (the ledger's attempt, the run tool's own block) and sending it here would
// be a second egress path the gate of design §7.1 never screened for this
// destination.
//
// The ARGUMENTS are here, and the field's absence is what this notification
// was corrected for (ADR-0040). They were left off on the argument that their
// readable half is the RESOURCE, derived once by namedResource — and the
// resource IS the readable half exactly while it differs between calls. For a
// session-scoped tool it never does, so one turn announced readScreen,
// blocks.list and two blocks.read of different finished commands as four
// announcements naming one pane. The resource keeps its own job beside them:
// it says WHICH argument holds the session, so the renderer can put the
// pane's name in that argument's place without guessing from the key.
type agentRunToolCall struct {
	RunID         int64               `json:"runId"`
	EntryID       string              `json:"entryId"`
	CallID        string              `json:"callId"`
	Tool          string              `json:"tool"`
	Args          map[string]any      `json:"args"`
	Effect        string              `json:"effect"`
	ActionEntryID string              `json:"actionEntryId"`
	Resource      *content.GrantScope `json:"resource,omitempty"`
	// OpensBlock says the call's work becomes a BLOCK of its own — the
	// declaration's fact, sent so the renderer draws that block in the
	// turn's next seat rather than a second child restating the command,
	// the output and the exit status the block already shows. Never derived
	// here from the tool name, and never derivable there: it is one more
	// fact of the tool table, like the effect beside it (ADR-0028 decision
	// 4).
	OpensBlock bool `json:"opensBlock"`
}

// agentRunReasoning is the agent.runReasoning notification (nocx-s92so): one
// chunk of the model's thinking, on its own wire. Never a field of
// agent.runDelta — an answer block that concatenates thinking with the
// answer is the tool-result defect in another shape — and never persisted,
// because the durable answer is the answer.
type agentRunReasoning struct {
	RunID   int64  `json:"runId"`
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
}

// agentRunState is the agent.runState notification: the run's terminal
// state. error is present only for failed, and it is a sentence a person
// reads, never a Go error string (design §7). droppedDeltas is present only
// when the live view is incomplete: the wire refused one or more
// agent.runDelta frames (a full outbound queue — outbound's deliberate
// non-blocking policy), so the renderer must not read the block it received
// as a complete answer. The durable answer is whole either way — every
// chunk was persisted before the notify — so the marker is a live-view
// bound, never a reason to treat the run as failed (nocx-dw3.1).
type agentRunState struct {
	RunID         int64  `json:"runId"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	DroppedDeltas int    `json:"droppedDeltas,omitempty"`
}

// agentApprovalRequested is the agent.approvalRequested notification (design
// §7.2, §7.3): a question reached a person. One kind of question whether the
// risk was an effect coming in (a policy escalation) or a secret going out
// (an egress finding) — the surface meets the same shape either way. It
// carries the full binding (run, attempt, tool, callId, argHash — what the
// answer names), the arguments the person is deciding about, the reason the
// gate asked, and the egress findings when the gate that asked was the
// egress gate. Findings are facts, never the material.
type agentApprovalRequested struct {
	RunID     string `json:"runId"`
	Attempt   int    `json:"attempt"`
	Tool      string `json:"tool"`
	CallID    string `json:"callId"`
	ArgHash   string `json:"argHash"`
	Arguments string `json:"arguments"`
	Reason    string `json:"reason"` // "policy" | "egress"
	// Effect is the effect class the gate decided on — the row a standing
	// answer writes. It crosses the wire because the renderer must never
	// derive an effect from a tool name (ADR-0028 decision 4); it is filled
	// on BOTH arms, so the notification is one shape whichever gate asked.
	Effect string `json:"effect"`
	// Resource is what the gate matched the call against, omitted when the
	// call named none — a fact for the person, never what an answer is over.
	Resource *content.GrantScope       `json:"resource,omitempty"`
	WasError bool                      `json:"wasError,omitempty"`
	Findings []assistant.EgressFinding `json:"findings,omitempty"`
}

// The three widths an answer can have (nocx-ki305, design "The prompt grows
// six answers"). They are how FAR the answer reaches, never what it is over:
// the effect the standing part is written against comes from the proposal
// the backend itself classified, so no wire value can name a tool
// (ADR-0028 decision 4).
const (
	approveScopeOnce    = "once"
	approveScopeSession = "session"
	approveScopeAlways  = "always"
)

// approveParams is the agent.approve request (design §7.2): the full binding
// — run, attempt, tool, call id and the canonical-argument hash — plus the
// person's decision and how far it reaches. The schema
// (contracts/agent.approve.schema.json) pins it: additionalProperties false,
// every field required.
type approveParams struct {
	RunID    string `json:"runId"`
	Attempt  int    `json:"attempt"`
	Tool     string `json:"tool"`
	CallID   string `json:"callId"`
	ArgHash  string `json:"argHash"`
	Approved bool   `json:"approved"`
	// Scope is how far the answer reaches: this proposal only, every call
	// of the same effect in this session, or the standing policy. It has no
	// default — an absent scope is refused, because a default here would be
	// a standing decision nobody expressed.
	Scope string `json:"scope"`
}

// agentApproveResponse is the agent.approve result: the state the run moved
// to — streaming (the resumed drive is in flight, for a yes or a policy
// decline) or failed (the egress decline's terminal close was persisted).
// The outcome the renderer draws comes from the agent.runState notification
// either way.
type agentApproveResponse struct {
	State string `json:"state"`
	// Warning is the sentence to show when the part of the answer that was
	// meant to OUTLIVE this proposal could not be recorded. Empty when
	// there was nothing to record or it was recorded. The decision itself
	// always stood: a store problem is not the person's to pay for, so the
	// call is not refused — the surface says the standing part did not
	// stick, and that is the whole difference.
	Warning string `json:"warning,omitempty"`
}

// ── handlers ──────────────────────────────────────────────────────────────

// agentHandlers answers agent.ask and the related run-control methods. It
// holds the AgentOperation (nil → the ledger is not wired), the connection's
// connState (session ownership and the session facts the environment is
// derived from), the connection's client identity (the idempotency binding of
// ask ids — per connection, so a reconnect mints a new one) and the
// Responder; never the *WSServer.
type agentHandlers struct {
	op capability.AgentOperation // nil → content store not wired
	// configOp resolves the endpoint the run uses (the ONE config operation,
	// shared with the config handlers — AD-8). nil → no endpoint store.
	configOp capability.ConfigOperation
	log      log.Logger
	// endpointWired is the config handlers' "endpoints not available" gate:
	// with no endpoint repository, ListEndpoints would nil-panic inside the
	// service, so the ask refuses before the call.
	endpointWired bool
	credentials   credential.Resolver
	client        assistant.Client
	askSub        control.Submission
	// attemptLedger is the ledger seam the tool-call pipeline records its
	// attempts with (design §6.4 — the attempt is durable, before the
	// call). The real ledger when the content store is wired; nil otherwise,
	// which disables tool execution (the middleware refuses to run a tool
	// without a durable attempt).
	attemptLedger assistant.AttemptLedger
	// grantFor mints the run's default grant from the workspace policy the
	// composition root named (ADR-0020 §7; runGrantFor). Nil when no policy
	// is named — the run carries no grant and the model is offered no tools.
	grantFor func(sessionID string) *content.Grant
	// requester is the broker-backed seam a renderer-executed tool asks
	// through (assistant.RendererRequester); nil when the broker is not
	// wired, which disables InRenderer tools.
	requester assistant.RendererRequester
	// knownMaterial is the egress gate's vault comparison (design §7.1,
	// assistant.KnownMaterial) — the seam that answers "does this tool
	// result contain a value the vault holds", in the backend, nothing
	// leaving. The composition root wires the vault adapter; a grant run
	// without it fails closed at the middleware's construction.
	knownMaterial assistant.KnownMaterial
	// approvals is the server's process-lifetime approval store (design
	// §7.2): passed on every Ask so the run that escalated and the run
	// that resumes consult the same decisions, and the source of truth
	// agent.approve's stale-id answer is checked against.
	approvals *assistant.ApprovalStore
	// pendingRuns maps a suspended run's id to the stream context the
	// resume re-drives (question, references, resolved endpoint material).
	// Server-scoped — the person answers on the same connection that
	// rendered the question, and the answer must resume the EXACT run.
	pendingRuns   map[int64]askRunContext
	pendingRunsMu *sync.Mutex
	// personalInstructions reads the person's own paragraph from the
	// settings document, on the request path, per ask. A function rather
	// than a value because it must be READ when the question is asked: a
	// value captured at registration would be whatever the field held when
	// the connection opened, and the field is written while the app runs.
	// The registration builder supplies one that answers "" when no
	// settings registry is wired; personalParagraph tolerates a handler
	// built without it, which is what a unit test constructing the struct
	// directly has.
	personalInstructions func() string
	// sessionPolicy is where "allow in this session" lands and where it
	// dies (ws_sessionpolicy.go). Never nil: the server constructs one.
	sessionPolicy *sessionPolicyStore
	// globalPolicy is where "always" lands — the SAME store the settings
	// page writes through, so a standing answer given at the prompt and one
	// given on the page are one document with one owner. Nil when no policy
	// was named, which is the state in which no run carries a grant and
	// therefore nothing can escalate.
	globalPolicy assistant.GlobalPolicy
	state        *connState
	clientID     string
	r            Responder
}

// environmentForSession derives the ledger environment from the session's
// own facts (the backend never trusts the renderer's idea of where it is).
// The endpoint facet is the session's remote hostname for an ssh session,
// nil for local; the canonical user@host:port endpoint is the ssh
// resolver's refinement and lands with the ledger cutover (nocx-rtg0.3) —
// recorded here so the identity is not mistaken for final.
func environmentForSession(sess session.Session) content.Environment {
	kind := content.EnvLocal
	var endpoint *string
	if sess.Kind() == session.KindRemote {
		kind = content.EnvSSH
		host := sess.Host()
		endpoint = &host
	}
	return content.Environment{
		ID:       content.EnvironmentIDFor(kind, sess.Host()),
		Kind:     kind,
		Endpoint: endpoint,
	}
}

// systemPromptFactsFor gathers what the standing system prompt is allowed to
// say about one pane (assistant.SystemPrompt, design §1). It DERIVES nothing:
// local-vs-ssh and the host come from environmentForSession, which already
// owns that question, and the rest is handed in by the caller that holds it.
//
// Two facts the design lists are deliberately absent, because no owner in
// this process can answer them for this pane and a plausible guess is worse
// than silence:
//
//   - The OS of an ssh session's far host. Nothing here has ever learned it
//     — the connect path does not ask and the integration hello does not
//     report it — so runtime.GOOS is filled only for a local pane, where it
//     is the OS of the shell the model would be writing commands for.
//   - The shell. session.Session does not expose it, and the integration
//     axis holds it only for the sessions that asked for shell integration,
//     so it is not a fact about every pane. Telling the model the wrong
//     shell buys wrong syntax stated confidently.
//   - The person's own paragraph is NOT absent: it has an owner, and the
//     owner is the settings document (nocx-avogl.4). It is read here, on
//     the request path, and handed in — the prompt function never looks a
//     setting up. Read fresh per ask, so a change on the settings screen
//     governs the next question with no restart and nothing to invalidate.
func systemPromptFactsFor(sessionID, cwd string, env content.Environment, attached []assistant.AttachedContentItem, personal string) assistant.SystemPromptFacts {
	f := assistant.SystemPromptFacts{
		SessionID:            sessionID,
		Cwd:                  cwd,
		Env:                  env,
		AttachedContent:      attached,
		PersonalInstructions: personal,
	}
	if env.Kind != content.EnvSSH {
		f.OS = runtime.GOOS
	}
	return f
}

// personalParagraph is the seam read, or "" when this handler was built
// without it — a unit test constructing agentHandlers directly, never the
// registration builder.
func (h agentHandlers) personalParagraph() string {
	if h.personalInstructions == nil {
		return ""
	}
	return h.personalInstructions()
}

// personalInstructionsText reads the person's own paragraph out of the
// settings registry — the document that owns it. Nil registry (a server
// built without settings) and a rejected read are both "the person has
// added nothing", which is the same state as an empty field: the prompt
// then says nothing about it at all, rather than claiming they wrote
// something they did not.
func (s *WSServer) personalInstructionsText() string {
	if s.settings == nil {
		return ""
	}
	v, err := s.settings.GetString(settings.AssistantPersonalInstructions)
	if err != nil {
		return ""
	}
	return v
}

// errNoEndpoint is the ask's no-endpoint refusal: a renderable condition
// (the surface says "configure an endpoint"), not a server fault — the
// message IS the report, and agent.status carries the persistent readiness
// line.
var errNoEndpoint = errors.New("no endpoint configured")

func decodeAgentAskParams(raw json.RawMessage) (agentAskParams, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return agentAskParams{}, "params must be an object"
	}
	for name := range fields {
		switch name {
		case "askId", "sessionId", "question", "attachedContent", "cwd":
		default:
			return agentAskParams{}, fmt.Sprintf("unsupported field %q; use attachedContent", name)
		}
	}
	var p agentAskParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return agentAskParams{}, "params must be an object"
	}
	return p, ""
}

func (h agentHandlers) handleAsk(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	p, msg := decodeAgentAskParams(req.Params)
	if msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	sid := session.ID(p.SessionID)
	if !h.state.has(sid) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	in, attached, msg := validateAgentAsk(p)
	if msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	in.Client = h.clientID
	sess, _ := h.state.get(sid)
	in.Env = environmentForSession(sess)
	// The turn's DURABLE ANCHOR, from the SAME owner the shell path reads it
	// from (ws_ledger.go's open): the backend already resolved which pane
	// this session is the pipe of. A paneId on the ask's params would put
	// one input under two owners, and the renderer's copy would be the one
	// nobody checked — which is the rule that path states in its own
	// comment. Nil when the session is attached to no recorded pane, which
	// costs the restore hint and nothing else (nocx-4em1z).
	in.PaneID = panePtr(sess.PaneID())
	// The run's authority is minted here, with the question: the workspace
	// policy preset the composition root named, scoped to the run's own
	// session and the observe effect class (ADR-0020 decision 5 — the grant
	// is immutable once execution starts, so it is decided before the
	// stream begins). Nil when no policy is named: the run executes no
	// tools, which is the state before readScreen.
	runGrant := h.grantFor(p.SessionID)

	// The endpoint the run will use comes from the ANSWERING ROLE (bead
	// nocx-e6kn2): the one resolver maps the role to its assigned
	// (endpoint, model) pair, resolved here so the run pins "endpoint and
	// model as they were at the time" (design §5) and the refusal is
	// visible before anything is recorded. With the role unassigned — or
	// the assigned pair gone (endpoint deleted, model removed) — the ask
	// is refused with the reason: a role is NEVER silently re-pointed at
	// another endpoint or model, because then nobody can tell which model
	// answered. Nothing lands in the ledger on a refusal (there is no run
	// to record: the ask never started).
	var endpoint profile.Endpoint
	var facts content.RunFacts
	if !h.endpointWired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: errNoEndpoint.Error()})
		return
	}
	err := h.configOp.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
		ep, model, err := svc.ResolveRole(profile.RoleAnswering)
		if err != nil {
			return err
		}
		endpoint = ep
		facts = content.RunFacts{
			Mode:       "explain",
			EndpointID: ep.ID,
			BaseURL:    ep.BaseURL,
			Model:      model,
		}
		return nil
	})
	if err != nil {
		// A role refusal is a RENDERABLE condition with a repair: the
		// surface shows the sentence (and its way out), not a server
		// fault. Anything else is an internal error on the ordinary path.
		switch {
		case errors.Is(err, profile.ErrRoleUnassigned):
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "the answering role has no model assigned — assign one in Settings → Roles"})
		case errors.Is(err, profile.ErrRoleEndpointGone), errors.Is(err, profile.ErrRoleModelGone):
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error() + " — reassign the answering role in Settings → Roles"})
		default:
			h.answerError(req, err)
		}
		return
	}
	in.Facts = facts

	// Endpoint material (the credential and secret-valued headers) is NOT
	// resolved here: it resolves inside the stream task (runAskStream), so
	// plaintext never sits in the durable pending-run record and a resume
	// re-resolves it from the endpoint the run was pinned to. A sealed vault
	// therefore waits inside the stream task — the Operation stance raises
	// one coalesced unlock and continues this same durable run — instead of
	// failing the request after its response was already sent, which is the
	// dead end that sent the person hunting for a door the surface could not
	// open.
	//
	// The transaction records the turn + run + body + edges in ONE commit,
	// and the response is sent with the turn's identity BEFORE the stream
	// starts — the renderer places the block from the response, then
	// appends deltas to it.
	var askRes content.AgentAskResult
	err = h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		res, submitErr := svc.SubmitAsk(ctx, in)
		if submitErr == nil {
			askRes = res
		}
		return submitErr
	})
	if err != nil {
		h.answerError(req, err)
		return
	}

	// The STREAM runs off the read loop on its own admission (the ask
	// response is already sent). The task context derives from the
	// connection, so a disconnect cancels the stream and the run
	// terminalizes — a refused socket write never wedges it. A refused
	// submit (the stream capacity is exhausted) terminalizes the run
	// failed with a renderable reason; the run is never left prepared.
	runCtx, runCancel := context.WithCancel(ctx)
	runControl := &agentRunControl{cancelFn: runCancel}
	rc := askRunContext{
		runID:    askRes.RunID,
		control:  runControl,
		entryID:  askRes.EntryID,
		paneID:   in.PaneID,
		question: in.Question,
		endpoint: endpoint,
		model:    facts.Model,
		grant:    runGrant,
		// attempt is the run's attempt — the ledger inserted the run row at
		// attempt 1 (SubmitAgentAsk), and it is the value the approval
		// binding names. The resume passes the SAME attempt, so the
		// middleware's re-built proposal matches the one the person
		// approved; the approved call's own execution is the entry's
		// SUBSEQUENT attempt (attempt 2), recorded by the middleware.
		attempt:   1,
		sessionID: sid,
		// The facts the model is told, from their existing owners: the
		// session id exactly as the ask spelled it (the string the tools'
		// sessionId parameter is matched against), the cwd this question
		// carried and the ledger recorded with it, the pane's environment
		// as environmentForSession already derived it, and the person's
		// own paragraph as the settings document holds it right now.
		promptFacts: systemPromptFactsFor(p.SessionID, in.Cwd, in.Env, attached, h.personalParagraph()),
	}
	h.pendingRunsMu.Lock()
	h.pendingRuns[rc.runID] = rc
	h.pendingRunsMu.Unlock()
	_ = h.r.TryResult(req.ID, mustMarshal(agentAskResponse{
		RunID:     askRes.RunID,
		EntryID:   askRes.EntryID,
		State:     string(content.RunPrepared),
		IngestSeq: askRes.IngestSeq,
		Replayed:  askRes.Replayed,
		Model:     facts.Model,
	}))
	if rej := h.askSub.TrySubmit(runCtx, control.Task{Run: func(taskCtx context.Context) {
		h.runAskStream(taskCtx, rc, h.r)
	}}); rej != nil {
		// The stream was refused: nothing is in flight. Drop the stored
		// context so a later agent.approve cannot target a run that never
		// started — the run terminalizes below and has no question open.
		h.pendingRunsMu.Lock()
		delete(h.pendingRuns, rc.runID)
		h.pendingRunsMu.Unlock()
		h.terminalize(ctx, rc, 0, content.RunFailed, content.TermFailed,
			"too many answers in flight — try again in a moment", h.r)
	}
}

// resolveEndpointMaterial resolves everything the stream needs: the endpoint
// credential and secret-valued headers. Operation stance makes the vault raise
// one coalesced unlock, wait, and continue independently of the wire carrier.
// Missing credentials and headers remain distinct, renderable failures.
func (h agentHandlers) resolveEndpointMaterial(
	ctx context.Context,
	endpoint profile.Endpoint,
) (credential.Secret, []assistant.Header, error) {
	secret := credential.Secret{}
	if endpoint.NeedsCredential() {
		if h.credentials == nil {
			return credential.Secret{}, nil, errors.New("the endpoint's credential is missing")
		}
		resolved, err := h.credentials.Resolve(
			ctx, credential.SecretID(endpoint.CredentialRef), credential.Operation("answer the ask"))
		if err != nil {
			// A sealed vault (headless), a dismissed unlock and a deleted
			// secret are terminalized by runAskStream, which owns the mapping
			// to the run's state; collapsing them into one "missing" sentence
			// here would hide a cancellation from the caller.
			return credential.Secret{}, nil, err
		}
		if resolved.IsEmpty() {
			return credential.Secret{}, nil, errors.New("the endpoint's credential is missing")
		}
		secret = resolved
	}

	headers := make([]assistant.Header, 0, len(endpoint.Headers))
	for _, hd := range endpoint.Headers {
		if hd.Value != nil {
			headers = append(headers, assistant.Header{Name: hd.Name, Value: *hd.Value})
			continue
		}
		if h.credentials == nil {
			return credential.Secret{}, nil,
				fmt.Errorf("the header %q references a missing secret", hd.Name)
		}
		hSecret, hErr := h.credentials.Resolve(
			ctx, credential.SecretID(hd.ValueRef), credential.Operation("answer the ask"))
		if hErr != nil {
			return credential.Secret{}, nil, hErr
		}
		if hSecret.IsEmpty() {
			return credential.Secret{}, nil,
				fmt.Errorf("the header %q references a missing secret", hd.Name)
		}
		var value string
		_ = hSecret.Use(func(b []byte) error {
			value = string(b)
			return nil
		})
		headers = append(headers, assistant.Header{Name: hd.Name, Value: value})
	}
	return secret, headers, nil
}

// askRunContext is the durable run identity and non-secret input handed to the
// stream task. Secret material is resolved by that task and never persisted.
type askRunContext struct {
	runID   int64
	control *agentRunControl
	entryID string
	// paneID is the pane this turn is anchored to — the THREAD the earlier
	// turns of this conversation are read from (PriorTurn). It is the same
	// value the turn was recorded with, carried from the ask rather than
	// re-resolved on the stream: the session may be gone by then, and the
	// anchor is what outlives it (nocx-4em1z). Nil when the session is the
	// pipe of no recorded pane, which costs the conversation context and
	// nothing else.
	paneID *string
	// prose is the run of prose currently OPEN — the `text` child the next
	// delta appends to, and the artifact its text lands in (ADR-0040). The
	// zero value is "no block is open", which is the state a run starts in
	// and the state a tool call leaves behind: a call that arrives before the
	// model has said anything must open no empty block.
	//
	// It lives on the run context rather than only in runAskStream's frame
	// because it has to CROSS A SUSPENSION, for the same reason nextSeq does:
	// the resume continues the answer instead of re-rolling it (nocx-igu4y),
	// so the prose it writes belongs to the block the question interrupted,
	// not to a second one opened beside it. Written by suspendForApproval into
	// the pendingRuns copy; never wire-facing as a struct.
	prose    content.ProseBlock
	question string
	endpoint profile.Endpoint
	model    string
	// grant is the run's authority (ADR-0020 decision 5), minted by the
	// workspace policy the composition root named (runGrantFor). Nil: the
	// run executes no tools — the model is offered none.
	grant *content.Grant
	// droppedBefore is the live-view drop count recorded before a
	// suspension: deltas the wire refused while THIS stream ran. The
	// resume re-drives the same run (agent.approve), so the count must
	// survive the boundary — the visible gap describes the whole answer,
	// not just the last Ask invocation. Written by suspendForApproval into
	// the pendingRuns copy; never wire-facing.
	droppedBefore int
	// attempt is the run's attempt — the ledger inserted the run row at
	// attempt 1 (SubmitAgentAsk), and it is the value the approval binding
	// names. The resume passes the SAME attempt so the middleware's
	// re-built proposal matches the one the person approved; the approved
	// call's own execution is the proposal entry's SUBSEQUENT attempt
	// (attempt 2), which the middleware records.
	attempt int
	// promptFacts is what the standing system prompt says about this pane,
	// gathered when the ask arrived from the owners that already hold each
	// fact (systemPromptFactsFor). The FACTS are carried, never the
	// assembled text: the prompt is rebuilt on every drive of this run, so
	// there is nothing to go stale and one place that assembles it. Pinned
	// at ask time for the same reason the grant and the endpoint are — a
	// resume must re-drive the run the person answered about, not a run
	// described differently.
	promptFacts assistant.SystemPromptFacts
	// sessionID is the session the run lives in — the session an "allow in
	// this session" answer is about. Carried EXPLICITLY, from the ask that
	// named it, rather than read back out of the grant: the grant's scope
	// union is what the run may reach, which is not a fact about any one
	// row and would stop being the session the moment a row is scoped
	// wider.
	sessionID session.ID
	// nextSeq is the delta sequence the NEXT drive of this run starts
	// from — written by suspendForApproval, read by runAskStream.
	//
	// It exists because the resume is now a REAL checkpoint resume
	// (nocx-igu4y): the model is not asked to produce the answer again, so
	// the deltas that follow the approval are the CONTINUATION of the ones
	// before it, not a second copy of them. While the resume re-rolled the
	// whole answer, restarting at 0 was harmless — a retried delta is a
	// no-op on the store's (artifact_id, seq) key, and the renderer
	// re-received what it already had. With a real resume it would be a
	// collision: new text written over the persisted chunks of the text
	// before the question, and a renderer routing new deltas onto old
	// numbers. The count must ascend across the whole run, so it crosses
	// the suspension exactly as droppedBefore does.
	nextSeq int
	// pendingReason is which gate the run is currently suspended on —
	// "policy" or "egress", empty when it is not suspended. Written by
	// suspendForApproval into the pendingRuns copy, and read by
	// agent.approve, which refuses a standing answer to an egress question.
	// It is a fact about the OPEN QUESTION, not about the proposal, which
	// is why it lives beside droppedBefore rather than in the approval
	// store: only the transport knows which of the two gates asked.
	pendingReason string
}

// runAskStream drives the prepared run to completion. Secret material resolves
// first, outside a capability admission and before any external request. A
// sealed vault therefore waits and continues this same durable run.
func (h agentHandlers) runAskStream(ctx context.Context, rc askRunContext, r Responder) {
	// Secret material resolves first, outside a capability admission and
	// before any external request. A sealed vault therefore waits and
	// continues this same durable run, and a dismissed unlock cancels it.
	secret, headers, materialErr := h.resolveEndpointMaterial(ctx, rc.endpoint)
	if materialErr != nil {
		if errors.Is(materialErr, ErrUnlockCancelled) {
			h.terminalize(ctx, rc, rc.droppedBefore, content.RunCancelled, content.TermUserKilled, "", r)
			return
		}
		h.terminalize(ctx, rc, rc.droppedBefore, content.RunFailed, content.TermFailed, materialErr.Error(), r)
		return
	}

	// dropped counts the deltas the wire refused while THIS stream ran. It
	// is declared before the context assembly because the reference-failure
	// path terminalizes below it. The stream is re-driven by a resume with
	// the SAME run (agent.approve), so the count starts from what a prior
	// attempt recorded — the visible gap describes the whole answer, never
	// just the last Ask invocation.
	dropped := rc.droppedBefore
	// The gate deltas may not pass before: a delta persisted before the
	// streaming transition commits would be a delta outside the run's
	// non-terminal span.
	if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, rc.runID, content.RunStreaming)
	}); err != nil {
		// The transition was refused: the run is already terminal (closed
		// by another path). Nothing to drive; stop.
		return
	}

	// Resolution is complete before the streaming transition. The stream
	// never reaches for the vault again, so a later seal cannot strand it.

	// Context assembly. The standing prompt (design §1) rides EVERY ask —
	// it is what tells the model where it is, and without it a tool call
	// naming this session is a guess the policy's exact scope match refuses
	// terminally (nocx-avogl.1). It is assembled here, from the facts
	// pinned when the ask arrived, and never stored: the same run resumed
	// after an approval rebuilds the same text from the same facts.
	promptFacts := rc.promptFacts
	msgs := make([]assistant.Message, 0, 4)
	//
	msgs = append(msgs, assistant.Message{
		Role:    "system",
		Content: assistant.SystemPrompt(promptFacts),
	})
	// THE CONVERSATION, and it is read from the ledger rather than remembered
	// in this process (ADR-0040's closing consequence). The turn before this
	// one in this pane is what "what did we just say" means: its question, and
	// the prose of the run that answered it, in seat order and as ONE message.
	//
	// It is one turn and not the whole thread on purpose — the whole
	// conversation assembled from the ledger is nocx-0s2gh.2, and it is the
	// same read at a larger scale rather than a second one. What this must not
	// do is grow a second assembler beside that; the arrangement stays in the
	// ledger (PriorTurn), and what happens here is only the mapping from a
	// stored turn to two Messages.
	//
	// A FAILED READ of the previous turn is not fatal: it is context the
	// backend added, so losing it degrades the answer instead of invalidating
	// the question. It is logged rather than swallowed — a conversation that
	// silently stopped being multi-turn is the failure that would take
	// longest to notice.
	if prior, priorErr := h.priorTurn(ctx, rc); priorErr != nil {
		h.log.Warn("the previous turn could not be read; answering without it",
			"run", rc.runID, "entry", rc.entryID, "error", priorErr)
	} else if prior != nil {
		msgs = append(msgs, assistant.Message{Role: "user", Content: prior.Question})
		if answer, ok := priorAnswerMessage(prior.Prose); ok {
			msgs = append(msgs, assistant.Message{Role: "assistant", Content: answer})
		}
	}
	msgs = append(msgs, assistant.Message{Role: "user", Content: rc.question})

	// The deltas continue where the last drive of this run stopped: a
	// resumed run is one answer, numbered once (see askRunContext.nextSeq).
	seq := rc.nextSeq
	// And so does the PIECE they are landing in. A resume writes the
	// continuation of the prose the question interrupted, not a second run of
	// prose beside it, so the open block crosses the suspension with the
	// numbering (askRunContext.prose). Empty on a fresh run and after every
	// tool call: the first delta then opens the next block.
	prose := rc.prose
	err := h.client.Ask(ctx, assistant.AskParams{
		Key:           secret,
		BaseURL:       rc.endpoint.BaseURL,
		Model:         rc.model,
		Headers:       headers,
		Messages:      msgs,
		Grant:         rc.grant,
		AttemptLedger: h.attemptLedger,
		Requester:     h.requester,
		KnownMaterial: h.knownMaterial,
		Approvals:     h.approvals,
		RunID:         strconv.FormatInt(rc.runID, 10),
		Attempt:       rc.attempt,
		// The turn every entry this run causes is joined to (nocx-h1l4o).
		// It comes off the SAME askRunContext the run id does — both were
		// set from one SubmitAgentAsk result — so the relation is written
		// from a fact the backend is already holding, and the renderer
		// never sends an arrangement of its own.
		TurnEntryID: rc.entryID,
	}, func(ev assistant.AskEvent) error {
		if rc.control != nil {
			// Declared inside the branch, like the other two: outside it
			// there is no event to release, and a no-op standing in for a
			// release is one that silently does nothing the day somebody
			// restructures the branch.
			releaseEvent := rc.control.beginEvent()
			if releaseEvent == nil {
				return context.Canceled
			}
			defer releaseEvent()
		}
		// ONE ordered stream, three notifications (nocx-shxv0, nocx-bshm2,
		// nocx-s92so). The order between them is the product fact this
		// bead is about: the call is rendered where it happened, inside the
		// answer that was streaming when it happened, and the socket
		// delivers what this callback emits in the order it emits it.
		switch ev.Kind {
		case assistant.AskToolCall:
			// NOT PERSISTED AS PROSE, and that is not an omission: the
			// durable account of a tool call is the LEDGER's action entry,
			// which the middleware wrote before the call ran and whose id
			// rides this notification. Writing it into the prose block would
			// be a second record of one fact, disagreeing with the first the
			// moment either changed.
			if ev.Call == nil {
				return nil
			}
			// THE BOUNDARY, and it is the backend's (ADR-0040). The prose
			// that was streaming ends HERE, where the call arrived, because
			// a sentence written before a command explains why the command
			// was run and a sentence written after it is a conclusion drawn
			// from its output. Sealing the block is how that boundary
			// becomes a durable fact instead of an offset the renderer has
			// to cut at — and the next delta opens the next block, so the
			// live path and the restore read one list rather than two
			// projections of one string.
			//
			// A call that arrives before any prose seals NOTHING and opens
			// nothing: the zero block is "the model has not spoken yet", and
			// an empty `text` child would draw as a paragraph that was never
			// written.
			//
			// A seal that fails ABORTS the stream, exactly as a delta that
			// cannot be persisted does: the alternative is a run that goes on
			// appending to a block the flow has already moved past.
			if prose.EntryID != "" {
				sealed := prose
				prose = content.ProseBlock{}
				if sealErr := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
					return svc.SealProse(ctx, sealed.EntryID)
				}); sealErr != nil {
					return sealErr
				}
			}
			// The wire declares `args` as an OBJECT and requires it, so a
			// call that carried none is announced as `{}` rather than as
			// null: absent and empty are the same sentence about a call, and
			// a nil map would say neither of them — it would say the wrong
			// JSON type.
			callArgs := ev.Call.Args
			if callArgs == nil {
				callArgs = map[string]any{}
			}
			if err := r.TryNotify("agent.runToolCall", mustMarshal(agentRunToolCall{
				RunID:         rc.runID,
				EntryID:       rc.entryID,
				CallID:        ev.Call.CallID,
				Tool:          ev.Call.Tool,
				Args:          callArgs,
				Effect:        string(ev.Call.Effect),
				ActionEntryID: ev.Call.EntryID,
				Resource:      ev.Call.Resource,
				OpensBlock:    ev.Call.OpensBlock,
			})); err != nil {
				// Counted with the delta drops, and into the SAME counter,
				// because it is the same fact about the live view: a call
				// that happened is missing from the flow the person is
				// reading, and the answer they see is not the whole of what
				// the run did. The durable side is whole either way — the
				// ledger has the attempt — so this never aborts the stream.
				dropped++
			}
			return nil
		case assistant.AskReasoning:
			// Also not persisted, and for the stronger reason: the durable
			// answer is the ANSWER, and appending the thinking to the open
			// prose block would put it back inside the answer by another
			// route — the defect this bead removed from the live path
			// (nocx-s92so). It does not open a block either: a run whose
			// only output was thinking printed nothing. A reasoning chunk the
			// wire refuses is a live-view gap like any other.
			if err := r.TryNotify("agent.runReasoning", mustMarshal(agentRunReasoning{
				RunID:   rc.runID,
				EntryID: rc.entryID,
				Text:    ev.Text,
			})); err != nil {
				dropped++
			}
			return nil
		}
		text := ev.Text
		// The other half of the boundary: the FIRST delta after a call opens
		// the next run of prose — a `text` child of the turn at the next free
		// seat, with a body of its own (ADR-0040). Opened lazily and never up
		// front, which is what makes "a call before any prose opens no empty
		// block" true by construction rather than by a later cleanup.
		//
		// A failure to open aborts the stream for the same reason a failed
		// persist does: there is nowhere to put the text, and a delta the
		// person sees but the ledger never held is the live view lying about
		// what was recorded.
		if prose.EntryID == "" {
			if openErr := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
				opened, e := svc.OpenProse(ctx, rc.entryID, rc.runID)
				if e == nil {
					prose = opened
				}
				return e
			}); openErr != nil {
				return openErr
			}
		}
		// Persist BEFORE emitting: a delta the renderer lost is still in
		// the ledger, and a persist failure aborts the stream.
		//
		// The chunk is numbered from 1 while the notification's seq starts at
		// 0, and the offset is deliberate rather than tidy: chunk numbering
		// is the store's (artifact_id, seq) key, which is what makes a
		// retried delta a no-op, and the notification's seq is what the
		// renderer routes on. Renumbering either to match the other would
		// change a contract to save an addition.
		//
		// seq stays RUN-scoped now that the artifact is per block, so one
		// block's chunk numbers ascend without being contiguous — 1,2,3 then
		// 7,8 after a call. That is exactly what the key needs: the read
		// orders by seq and the retry lands on the number it landed on
		// before. Renumbering per artifact would give two blocks of one run
		// the same numbers and buy nothing.
		if persistErr := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
			return svc.AppendRunDelta(ctx, prose.ArtifactID, seq+1, []byte(text))
		}); persistErr != nil {
			return persistErr
		}
		// The wire may refuse the delta: a full outbound queue or an
		// exhausted byte budget takes outbound's deliberate overflow path
		// and the frame is dropped. That is safe BECAUSE the ledger
		// persisted the chunk above — the answer on disk is whole. What
		// must never happen is the drop going unnoticed: a hole in the
		// live view that reads as a complete answer. Count it — the
		// terminal agent.runState carries the count, so the renderer can
		// mark the gap (a visible bound is the feature; a silent
		// truncation is the defect, the bead's criterion 1). The drop must
		// not abort the stream: returning the error would kill the run
		// over a dropped REFRESHABLE notification.
		if err := r.TryNotify("agent.runDelta", mustMarshal(agentRunDelta{
			RunID:   rc.runID,
			EntryID: rc.entryID,
			BlockID: prose.EntryID,
			Seq:     seq,
			Text:    text,
		})); err != nil {
			dropped++
		}
		seq++
		return nil
	})
	if err != nil {
		// A suspension is NOT a failure (criterion 1): the policy or the
		// egress gate asked a person a question, the run moves to
		// awaiting_approval, and the question reaches the renderer. The
		// classifyAskFailure path that would report it as a model failure
		// is asserted against here — it must never see a suspension.
		var apErr *assistant.ApprovalRequestedError
		var egErr *assistant.EgressRequestedError
		if errors.As(err, &apErr) && apErr.Request != nil {
			h.suspendForApproval(ctx, rc, r, dropped, seq, prose, apErr.Request, nil)
			return
		}
		if errors.As(err, &egErr) && egErr.Request != nil {
			h.suspendForApproval(ctx, rc, r, dropped, seq, prose, nil, egErr.Request)
			return
		}
		reason, sentence := classifyAskFailure(err)
		// This is the boundary that catches the framework's error, so this
		// is where its text is kept — once, with the run id, and nowhere
		// else (nocx-avogl.3). The wire carries the sentence; the log
		// carries eino's "[NodeRunError] … node path: [node_1, ToolNode]"
		// and whatever else the engine wrapped, which is the only place
		// that trace survives at all.
		h.log.Warn("agent ask: the run failed",
			"run", rc.runID, "reason", string(reason), "sentence", sentence, "error", err)
		h.terminalize(ctx, rc, dropped, content.RunFailed, reason, sentence, r)
		return
	}
	h.terminalize(ctx, rc, dropped, content.RunCompleted, content.TermCompleted, "", r)
}

// suspendForApproval moves the run to awaiting_approval and sends the
// question. The DURABLE state is the honest part (criterion 4): a
// reconnecting renderer reads awaiting_approval — distinguishable from a run
// NOT terminalized: the person's answer resumes it — a yes, or a policy no
// whose refusal is the call's result (nocx-uvac6.1); only an egress no
// terminalizes it (agent-declined).
//
// dropped is the live-view drop count the suspending stream accumulated;
// it is recorded into the stored stream context so the resume's or the
// decline's terminal close carries the WHOLE run's count — a gap observed
// before the question reached the person is still a gap in the live view
// after the resume's terminal close. nextSeq crosses the same boundary for
// the same reason and is the stronger case: since nocx-igu4y the resume
// CONTINUES the answer instead of re-rolling it, so its deltas must be
// numbered after these, not over them.
//
// prose crosses with it, and for the identical reason one step further in
// (ADR-0040): a resume that continues the answer continues the PIECE of it
// that was open, so the block the interrupted stream was writing into is
// handed to the next drive rather than left behind for a second block to be
// opened beside it. A run suspended before it said anything carries the zero
// block, which is "nothing is open" — the resume's first delta then opens the
// first one, exactly as a fresh run would.
func (h agentHandlers) suspendForApproval(ctx context.Context, rc askRunContext, r Responder, dropped, nextSeq int, prose content.ProseBlock, ap *assistant.ApprovalRequest, eg *assistant.EgressRequest) {
	if rc.control != nil {
		// Declared HERE and nowhere else, for the reason terminalize's
		// release is: outside this branch there is no event to release, and
		// a no-op standing in for one is a release that silently does
		// nothing the day the branch is restructured.
		releaseEvent := rc.control.beginEvent()
		if releaseEvent == nil {
			h.terminalize(ctx, rc, dropped, content.RunCancelled, content.TermUserKilled,
				"the person stopped this answer", r)
			return
		}
		defer releaseEvent()
	}
	if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, rc.runID, content.RunAwaitingApproval)
	}); err != nil {
		// The transition was refused: the run is already terminal (closed
		// by another path). The question is moot; nothing to render.
		return
	}
	n := agentApprovalRequested{Reason: "policy"}
	if ap != nil {
		n.RunID, n.Attempt, n.Tool, n.CallID, n.ArgHash, n.Arguments = ap.RunID, ap.Attempt, ap.Tool, ap.CallID, ap.ArgHash, ap.Arguments
		n.Effect, n.Resource = string(ap.Effect), ap.Resource
	} else {
		n.RunID, n.Attempt, n.Tool, n.CallID, n.ArgHash, n.Arguments = eg.RunID, eg.Attempt, eg.Tool, eg.CallID, eg.ArgHash, eg.Arguments
		n.Reason, n.WasError, n.Findings = "egress", eg.WasError, eg.Findings
		// The egress arm fills the same two fields off the same
		// declaration: the surface ignores them here, but the wire is not
		// two shapes and the schema requires the effect on both.
		n.Effect, n.Resource = string(eg.Effect), eg.Resource
	}
	// Carry the drops across the suspension: the resume re-drives this
	// same context (runAskStream starts from rc.droppedBefore), and the
	// egress decline's terminal close carries it the same way. The gate
	// that asked is carried the same way and for the same reason —
	// agent.approve refuses a standing answer to an egress question, and
	// by then only this record remembers which of the two gates produced
	// it.
	h.pendingRunsMu.Lock()
	if stored, ok := h.pendingRuns[rc.runID]; ok {
		stored.droppedBefore = dropped
		stored.nextSeq = nextSeq
		stored.prose = prose
		stored.pendingReason = n.Reason
		h.pendingRuns[rc.runID] = stored
	}
	h.pendingRunsMu.Unlock()
	// The pending record is the wire's source of truth for criterion 7. The
	// middleware records it at escalation in the real flow; the transport
	// records it here when the wire received the question without it (a
	// suspension that surfaced by any path) — and NEVER overwrites the
	// middleware's record, which carries the proposal's ledger entry id the
	// approved resume must run as a subsequent attempt of.
	proposal := assistant.Approval{
		RunID: n.RunID, Attempt: n.Attempt, Tool: n.Tool, CallID: n.CallID, ArgHash: n.ArgHash,
		Effect: content.Effect(n.Effect),
	}
	if !h.approvals.IsPending(proposal) {
		if ap != nil {
			proposal.EntryID = ap.EntryID
		}
		h.approvals.Request(proposal)
	}
	// And the effect, unconditionally — including onto the record the
	// middleware already made, which is the ORDINARY path. The middleware
	// records the proposal at escalation, where the store call carries the
	// binding and the ledger entry; the effect only reaches here, off the
	// same declaration the notification is built from. Noting it inside the
	// branch above would leave the effect recorded in exactly the flows a
	// scripted-suspension test exercises and missing in the real one, which
	// is a green suite over an "always" that writes no row.
	h.approvals.NoteEffect(proposal)
	_ = r.TryNotify("agent.approvalRequested", mustMarshal(n))
}

// handleCancel stops one prepared, streaming or awaiting-approval run. The
// run context is cancelled first, then the terminal ledger write is completed
// before the result is answered, so no stream event can overtake the person's
// stop decision.
func (h agentHandlers) handleCancel(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	var p agentCancelParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
		return
	}
	h.pendingRunsMu.Lock()
	rc, ok := h.pendingRuns[p.RunID]
	h.pendingRunsMu.Unlock()
	if !ok || rc.control == nil || !rc.control.beginCancel() {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: run is not active (it may already be terminal or never existed)",
		})
		return
	}
	h.terminalize(ctx, rc, rc.droppedBefore, content.RunCancelled, content.TermUserKilled,
		"the person stopped this answer", h.r)
	_ = h.r.TryResult(req.ID, mustMarshal(agentCancelResponse{
		RunID:     p.RunID,
		State:     string(content.RunCancelled),
		Cancelled: true,
	}))
}

// handleApprove answers agent.approve — the person's decision on one exact
// proposal (design §7.2, acceptance criteria 2, 7, 8). Yes resumes the run
// as a NEW attempt of the same entry (the middleware runs the approved call
// as the proposal's subsequent attempt); no (nocx-uvac6.1) resumes the run
// with the refusal as that call's result — except on the egress gate, where
// a no ends the run as agent-declined. A stale or unknown approval id is
// answered honestly and resumes nothing.
func (h agentHandlers) handleApprove(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	if h.approvals == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "approval store not wired"})
		return
	}
	var p approveParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
		return
	}
	ap := assistant.Approval{RunID: p.RunID, Attempt: p.Attempt, Tool: p.Tool, CallID: p.CallID, ArgHash: p.ArgHash}
	// Criterion 7's source of truth: the store is what was ASKED. An id
	// that is not pending was never asked, or was already answered — a
	// second answer to a settled question is not a decision.
	if !h.approvals.IsPending(ap) {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: unknown approval — nothing pending for this proposal (it was never asked, or was already answered)",
		})
		return
	}
	runID, err := strconv.ParseInt(p.RunID, 10, 64)
	if err != nil || runID <= 0 {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: runId must be the backend-minted run id"})
		return
	}
	h.pendingRunsMu.Lock()
	rc, ok := h.pendingRuns[runID]
	h.pendingRunsMu.Unlock()
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: no pending question for this run",
		})
		return
	}
	// Egress keeps TWO answers, once only (design, "The egress prompt keeps
	// two answers"). An egress ask means a tool result carried
	// secret-shaped material and nothing has been sent to the provider yet;
	// "always" there would mean always send secrets to the provider, which
	// is not a standing decision anyone should make by clicking a button
	// next to five others. Refused here rather than in the validator
	// because only the pending question knows which gate asked.
	if rc.pendingReason == "egress" && p.Scope != approveScopeOnce {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: an egress decision covers this result only — sending secret-shaped material to the model provider is never a standing answer",
		})
		return
	}
	if !p.Approved {
		// The person declined (nocx-uvac6.1): the run is NOT over — the
		// refusal becomes this call's result and the model answers it in
		// words ("a refusal is an answer", systemprompt.go). The decline
		// is settled against the exact proposal before any standing policy
		// write, so a responder that loses the race cannot mutate policy or
		// resume a run it no longer owns.
		//
		// The EGRESS gate is the one decline that still ends the run: its
		// question is whether the withheld result may LEAVE for the
		// provider, and a no means it never will — there is no result to
		// continue with. The withheld bytes are dropped and the run closes
		// as agent-declined, which is what "refuse and ask" (design §7.1)
		// means at its other end. This is deliberate: the refusal-as-result
		// contract is for calls that did not run, and an egress decline is
		// about a result that DID run but will not be sent.
		if rc.pendingReason == "egress" {
			h.approvals.ClearRetained(ap)
			h.terminalize(ctx, rc, rc.droppedBefore, content.RunFailed, content.TermAgentDeclined,
				"the person declined to send the result to the model", h.r)
			_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunFailed)}))
			return
		}
		if !h.approvals.Decline(ap, declineKindForScope(p.Scope)) {
			// The pending check passed but the decline lost the race
			// (another connection answered first). Honest refusal: no
			// standing row was written and nothing resumed.
			_ = h.r.TryError(req.ID, RPCError{
				Code:    -32602,
				Message: "Invalid params: unknown approval — it was already answered",
			})
			return
		}
		// The standing part is recorded only after the decline settled,
		// so a loser can never write a row for a question it did not win.
		warning := h.applyStandingAnswer(p, ap, rc.sessionID)
		if warning != "" && p.Scope != approveScopeOnce {
			// The standing write did not stick. The refusal is still
			// this call's result, but it must not claim permanence the
			// policy will not honour.
			h.approvals.DowngradeDeclined(ap)
		}
		if rej := h.askSub.TrySubmit(ctx, control.Task{Run: func(taskCtx context.Context) {
			h.resumeRunDeclined(taskCtx, rc, h.r)
		}}); rej != nil {
			h.terminalize(ctx, rc, rc.droppedBefore, content.RunFailed, content.TermFailed,
				"too many answers in flight — try again in a moment", h.r)
			_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunFailed), Warning: warning}))
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunStreaming), Warning: warning}))
		return
	}
	if !h.approvals.Approve(ap) {
		// The pending check passed but the approve lost the race (another
		// connection answered first). Honest refusal: nothing resumed.
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: unknown approval — it was already answered",
		})
		return
	}
	// The yes is recorded, and only now is the part of it that outlives
	// this proposal: an answer the server went on to refuse — the race
	// above — must not leave a standing rule behind it.
	warning := h.applyStandingAnswer(p, ap, rc.sessionID)
	// The resume: the same run, the same stream context, the same binding —
	// the middleware sees the approval and runs the call as the proposal's
	// SUBSEQUENT attempt. The approval store is passed again, so the yes
	// crosses the suspension.
	if rej := h.askSub.TrySubmit(ctx, control.Task{Run: func(taskCtx context.Context) {
		h.resumeRun(taskCtx, rc, h.r)
	}}); rej != nil {
		h.terminalize(ctx, rc, rc.droppedBefore, content.RunFailed, content.TermFailed,
			"too many answers in flight — try again in a moment", h.r)
		_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunFailed), Warning: warning}))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunStreaming), Warning: warning}))
}

// applyStandingAnswer records the part of a decision that outlives the
// proposal it was given on: "in this session" writes the run's session's
// overlay, "always" writes ONE row of the global matrix. "once" writes
// nothing anywhere, which is the behaviour every answer had before this
// existed. The row is the one the BACKEND classified the proposal under —
// never anything the wire named, so no answer can express a rule over a tool
// name (ADR-0028 decision 4).
//
// It returns the sentence to report when the standing part could not be
// recorded, and empty when there was nothing to record or it was recorded.
// A failure here never refuses the call: the person said yes, and punishing
// them for a store problem is the wrong end to fail toward. The run resumes,
// or the decline stands, and the result says the standing part did not
// stick.
func (h agentHandlers) applyStandingAnswer(p approveParams, ap assistant.Approval, sid session.ID) string {
	if p.Scope == approveScopeOnce {
		return ""
	}
	effect, ok := h.approvals.EffectFor(ap)
	if !ok {
		// No row was recorded for this proposal, so there is no row to
		// write. Fail toward asking: the answer covers this call, and the
		// next call of the same kind asks again.
		h.log.Warn("agent.approve: the proposal names no effect class; the standing part of the answer was not recorded",
			"run", p.RunID, "tool", p.Tool, "scope", p.Scope)
		return "the decision was applied to this call, but could not be saved as a standing answer: the question named no effect class"
	}
	d := content.DecisionRefuse
	if p.Approved {
		d = content.DecisionPermit
	}
	if p.Scope == approveScopeSession {
		h.sessionPolicy.Set(sid, effect, d)
		return ""
	}
	if h.globalPolicy == nil {
		return "the decision was applied to this call, but there is no policy store to save it as a standing answer in"
	}
	// SetRowDecision replaces the row's DECISION and keeps its scopes: a
	// standing answer changes what happens, never what the row is bound to.
	// The write goes through the store the settings page writes through, so
	// the two surfaces are one owner of one document rather than a
	// read-modify-write race between them.
	next := h.globalPolicy.Policy().SetRowDecision(effect, d)
	if err := h.globalPolicy.SetPolicy(next); err != nil {
		return "the decision was applied to this call, but could not be saved as a standing answer: " + err.Error()
	}
	return ""
}

// resumeRun re-drives a suspended run after the person's yes: the run
// streams again (awaiting_approval → streaming), the Ask resumes from the
// engine's checkpoint with the approval stored, and the stream runs to its
// terminal state like any other.
//
// The grant is MINTED AGAIN here (nocx-v94ne), and that is the whole of what
// makes "allow in this session" mean anything inside the run it was given
// in. The run's grant was minted once, when the question was asked; the
// answer that follows writes the session overlay (applyStandingAnswer →
// sessionPolicyStore), and a grant minted before that write cannot see it.
// So every further call of the same effect class in the same run asked
// again — which is exactly what the person's answer said to stop doing.
//
// ADR-0020 §5 forbids MUTATING a running grant, and this does not: rc is
// this function's own copy, nothing running holds it, and the middleware
// for the resumed attempt is built fresh from the new value. ADR-0028 says
// it in as many words — approval resumes "as a new attempt with a new
// grant". The approval BINDING is untouched: its Attempt still names the
// interrupted proposal, which is what the person answered about.
func (h agentHandlers) resumeRun(ctx context.Context, rc askRunContext, r Responder) {
	if h.grantFor != nil {
		rc.grant = h.grantFor(string(rc.sessionID))
	}
	if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, rc.runID, content.RunStreaming)
	}); err != nil {
		// The move was refused: the run is already terminal (closed by
		// another path). Nothing to resume; the approval stands harmless.
		return
	}
	h.runAskStream(ctx, rc, r)
}

// validateAgentApproveRaw checks agent.approve's params BEFORE the handler
// runs (registration.go — the ONE place params are checked): the full
// binding — run, attempt, tool, call id, argument hash — is required and
// bounded, exactly as the schema declares (additionalProperties false,
// every field required).
func validateAgentApproveRaw(raw json.RawMessage) string {
	var p approveParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.RunID) == "" || utf8.RuneCountInString(p.RunID) > maxIDRunes {
		return "runId is required and bounded"
	}
	if p.Attempt <= 0 {
		return "attempt must be positive"
	}
	if strings.TrimSpace(p.Tool) == "" || utf8.RuneCountInString(p.Tool) > maxIDRunes {
		return "tool is required and bounded"
	}
	if strings.TrimSpace(p.CallID) == "" || utf8.RuneCountInString(p.CallID) > maxIDRunes {
		return "callId is required and bounded"
	}

	if strings.TrimSpace(p.ArgHash) == "" || utf8.RuneCountInString(p.ArgHash) > 128 {
		return "argHash is required and bounded"
	}
	switch p.Scope {
	case approveScopeOnce, approveScopeSession, approveScopeAlways:
	default:
		// No default: an answer with no scope is not "once". A default
		// here would be a standing decision nobody expressed, and the
		// schema requires the field for the same reason.
		return "scope is required and must be one of once, session, always"
	}
	return ""
}

func validateAgentCancelRaw(raw json.RawMessage) string {
	var p agentCancelParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.RunID <= 0 {
		return "runId must be the backend-minted run id"
	}
	return ""
}

// resumeRunDeclined is resumeRun for the person's NO (nocx-uvac6.1). The
// grant is deliberately NOT re-minted here, and that is the point: the
// standing half of a no has already written the matrix row / session overlay
// (applyStandingAnswer), and a re-minted grant would REFUSE the effect the
// suspended call belongs to — which undeclares its tool (ADR-0028 decision
// 3: a refused effect is never declared), and an undeclared tool is a
// checkpoint whose branch nobody can restore. The declined record IS the
// run's authority for the call it was given on — and for a standing no, for
// every retry of the same effect class in this run (DeclinedEffect) — so
// the refusal is enforced at the middleware with the person's own sentence,
// while the written row governs the runs that come after this one.
func (h agentHandlers) resumeRunDeclined(ctx context.Context, rc askRunContext, r Responder) {
	if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, rc.runID, content.RunStreaming)
	}); err != nil {
		// The move was refused: the run is already terminal (closed by
		// another path). Nothing to resume; the decline stands harmless.
		return
	}
	h.runAskStream(ctx, rc, r)
}

// declineKindForScope maps the wire's answer scope to the declined record's
// standing half — the sentence the model is refused with. "once" covers the
// call only; "session" and "always" are the standing noes the middleware
// also applies to a retry of the same effect class in this run.
func declineKindForScope(scope string) assistant.DeclineKind {
	switch scope {
	case approveScopeSession:
		return assistant.DeclineCallSession
	case approveScopeAlways:
		return assistant.DeclineCallAlways
	default:
		return assistant.DeclineCallOnce
	}
}

// terminalize persists the run's terminal state AND its entries in one
// transaction (FinishAgentRun), then notifies the wire. The notification may
// go nowhere — the connection may be gone — but the ledger is the record,
// and the terminal close MUST NOT depend on the connection: a disconnect
// cancels the stream's ctx (that is how the run got here), and a cancelled
// ctx would fail the very write that closes the run. WithoutCancel keeps
// the terminal close independent of the connection's fate.
//
// dropped is the run's live-view drop count (deltas the wire refused): the
// notification carries it so the renderer can mark the gap. It is a
// live-view bound, never a terminal-state change — the durable answer is
// whole, so the run still closes with the state it earned.
func (h agentHandlers) terminalize(ctx context.Context, rc askRunContext, dropped int, state content.RunState, reason content.TerminationReason, sentence string, r Responder) {
	cancelled := false
	if rc.control != nil {
		// Declared HERE and nowhere else: outside this branch there is no
		// terminal to release, and a no-op standing in for one is a release
		// that silently does nothing if the branch is ever restructured.
		releaseTerminal, ok, c := rc.control.beginTerminal()
		if !ok {
			return
		}
		cancelled = c
		defer releaseTerminal()
	}
	if cancelled {
		state = content.RunCancelled
		reason = content.TermUserKilled
		sentence = "the person stopped this answer"
	}
	// The run is closing: nothing may resume it. Drop the stored stream
	// context so a late agent.approve finds no pending question — and the
	// engine's continuation with it (nocx-igu4y). Ask drops its own
	// checkpoint on every terminal outcome IT returns from; this is the
	// one funnel for the outcomes it does not — the person declined, the
	// stream could not be submitted, the run was closed while its question
	// was still open. A checkpoint nobody may resume is a copy of the
	// run's messages held for the life of the process.
	h.pendingRunsMu.Lock()
	delete(h.pendingRuns, rc.runID)
	h.pendingRunsMu.Unlock()
	if h.client != nil {
		h.client.Discard(strconv.FormatInt(rc.runID, 10))
	}
	tctx := context.WithoutCancel(ctx)
	err := h.op.Run(tctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.FinishAgentRun(ctx, rc.runID, content.FinishAgentRun{
			State:             state,
			TerminationReason: reason,
			Error:             sentence,
			EndedAt:           time.Now().UnixMilli(),
		})
	})
	if err != nil {
		// The close failed: the run stays non-terminal, and the startup
		// sweep repairs it as interrupted at the next start. Logged, never
		// silent — a run that never closes is exactly the "the block stays
		// open forever" failure the sweep exists to bound.
		h.log.Warn("agent ask: terminal close failed; the startup sweep will repair the run",
			"run", rc.runID, "state", string(state), "error", err)
		return
	}
	_ = r.TryNotify("agent.runState", mustMarshal(agentRunState{
		RunID:         rc.runID,
		State:         string(state),
		Error:         sentence,
		DroppedDeltas: dropped,
	}))
}

// classifyAskFailure turns any Ask error into the run's termination reason
// and the sentence agent.runState carries — one owner of the engine-error →
// wire-sentence mapping (design §7: "a sentence a person reads, not a Go
// error string").
//
// Nothing here appends err.Error() (nocx-avogl.3). It used to, in the default
// arm, and what a person saw was eino's own stringification:
//
//	the model failed to answer: [NodeRunError] failed to stream tool call
//	call_7947...: agent policy: tool call refused
//	node path: [node_1, ToolNode]
//
// `NodeRunError`, `node path`, `ToolNode` and the call id are the framework's
// internals; they say nothing a person can act on and they are not ours. The
// framework's text is not thrown away — runAskStream logs it once, with the
// run id, at the boundary that catches it — but it never travels on the wire.
//
// A cause per sentence, deliberately: one message for three causes is how a
// cause stops being findable. Every arm is reached by a TYPE, never by a
// string match against the framework's text — the typed chain survives eino.
// A REFUSAL has no arm here: since nocx-uvac6.1 a refusal is the call's
// result, not an error, so it never reaches this function at all.
func classifyAskFailure(err error) (content.TerminationReason, string) {
	// The engine's own failure sentence: the endpoint answered, and did not
	// answer (a StreamError is nocx's type, never the framework's). It is
	// the FIRST arm because it is the most specific thing Ask can return
	// about itself.
	var se *assistant.StreamError
	if errors.As(err, &se) {
		return content.TermFailed, se.Message
	}
	// The run lease (ADR-0020 decision 2) fired: the sentence names WHICH
	// bound ended the run, so the block says why — the ledger already
	// records the reason on the attempt; this is the same fact in the
	// human's words. Checked BEFORE the context.Canceled case below: the
	// lease's terminalization cancels the request, so the error also
	// unwraps to context.Canceled — mapping that first would report the
	// run as a lost connection, which is the wrong lie in the wrong
	// direction.
	var leaseErr *assistant.RunLeaseError
	if errors.As(err, &leaseErr) {
		return leaseErr.Reason, runLeaseSentence(leaseErr.Reason)
	}
	// Cause 1b: the policy permitted the call, the tool RAN, and it failed.
	// Its message is already the product's own — the renderer's "could not
	// capture the screen", a command that could not start — so it is passed
	// through, named by the tool it came from. Checked after the lease arm
	// above: a lease that fires during a tool call arrives wrapped in this,
	// and the lease's bound is the more specific fact.
	var toolErr *assistant.ToolFailedError
	if errors.As(err, &toolErr) {
		return content.TermFailed, "the assistant's " + toolErr.Tool + " call did not finish: " + toolErr.Message()
	}
	// Cause 2: the model produced a tool call the engine cannot act on — a
	// name that is not a tool, or arguments the schema it was shown does not
	// allow. NOT a refusal: there was nothing to refuse.
	//
	// MEASURED, 2026-08-21: only the ARGUMENTS half reaches this arm. A tool
	// name the model invents is rejected by eino's own index lookup ("tool %s
	// not found in toolsNode indexes") BEFORE the policy middleware is
	// entered, so ErrMalformedModelOutput's unknown-tool branch is
	// unreachable through the engine and that failure lands on the default
	// sentence below. It is framework-free there, so nothing leaks; naming it
	// properly needs a handle eino does not give us, and is its own task.
	if errors.Is(err, assistant.ErrMalformedModelOutput) {
		return content.TermFailed, "the model asked for a tool call nocx could not act on: it did not match any tool the model was offered. Ask again — a different model may handle tools better."
	}
	switch {
	case errors.Is(err, context.Canceled):
		return content.TermTransportGone, "the connection was lost while the answer was streaming"
	case errors.Is(err, context.DeadlineExceeded):
		return content.TermTimeout, "the model did not answer in time"
	}
	// Cause 3: the request to the model endpoint never completed — a dial
	// that failed, a TLS handshake that did not, a connection that dropped
	// mid-request. Checked AFTER context.Canceled and context.DeadlineExceeded
	// for the same reason the lease is checked before them: a cancelled or
	// timed-out request in flight comes back as a *url.Error WRAPPING the
	// context error, so matching *url.Error first would report every lost
	// connection and every deadline as an unreachable endpoint.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return content.TermFailed, "nocx could not reach the model endpoint. Check that the endpoint's address is right and that it is running, then ask again."
	}
	// Anything else. It still says nothing eino wrote: the trace is in the
	// log, named there so a person can find it.
	return content.TermFailed, "the model failed to answer. The details are in nocx's log."
}

// runLeaseSentence is the human-readable statement of one lease bound, for
// the runState error a block shows. A visible bound is the feature; a
// silent truncation is the defect (the bead's criterion 4).
func runLeaseSentence(reason content.TerminationReason) string {
	switch reason {
	case content.TermTimeout:
		return "the command did not finish within its wall-clock deadline and was terminalized"
	case content.TermInactivity:
		return "the command was terminalized for inactivity: it produced no output for too long"
	case content.TermOutputBudget:
		return "the command was terminalized: its output exceeded the budget, and was bounded rather than truncated"
	default:
		return "the command was terminalized by its lease"
	}
}

// answerError maps ask transaction failures to JSON-RPC errors. A conflict
// in an id supplied by the renderer is invalid params; a gate refusal keeps
// its control.saturated shape (answerOperationRefusal); anything else is an
// internal error.
func (h agentHandlers) answerError(req jsonrpcRequest, err error) {
	switch {
	case errors.Is(err, content.ErrIDConflict),
		errors.Is(err, capability.ErrOperationInactive):
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + err.Error()})
	default:
		answerOperationRefusal(h.r, req, err)
	}
}

// ── validation ────────────────────────────────────────────────────────────

// validateLiveFrameBody checks the LIVE half of a frame — the identity, the
// buffer row range, the cursor and every row's cells against the bounds — and
// returns the first refusal message, or "" with the row-char total when the
// body is valid. The readScreen pull resolution is the sole caller, so a rule
// added here applies to that direction (AD-8 — one owner per behaviour).
func validateLiveFrameBody(rows []frameRowWire, cursor *frameCursorWire, identity *frameIdentityWire, rng *frameRangeWire) (string, int) {
	if cursor == nil {
		return "a live frame requires a cursor", 0
	}
	if identity == nil {
		return "a live frame requires the capture identity", 0
	}
	if rng == nil {
		return "a live frame requires the buffer row range", 0
	}
	id := identity
	if id.Cols < 1 || id.Cols > maxFrameCols {
		return "identity cols are out of bounds", 0
	}
	if id.Rows < 1 || id.Rows > maxFrameRows {
		return "identity rows are out of bounds", 0
	}
	if id.Generation < 0 {
		return "identity generation must not be negative", 0
	}
	switch id.Buffer.Kind {
	case "normal":
	case "alternate":
		if id.Buffer.AltSession == nil || *id.Buffer.AltSession < 0 {
			return "an alternate buffer identity requires a non-negative altSession", 0
		}
	default:
		return "buffer kind must be normal or alternate", 0
	}
	if rng.Start < 0 || rng.End <= rng.Start || rng.End-rng.Start != len(rows) {
		return "range must be non-negative and span exactly the frame's rows", 0
	}
	// The cursor is an absolute buffer line: at most scrollback cap +
	// screen height. Col is within the frame's geometry.
	if cursor.Col < 0 || cursor.Col >= id.Cols ||
		cursor.Line < 0 || cursor.Line >= maxFrameRows+id.Rows {
		return "cursor is out of bounds", 0
	}
	var totalChars int
	for _, row := range rows {
		if row.Kind != "cells" {
			return "a live frame row must be cells", 0
		}
		if len(row.Cells) != id.Cols {
			return "a live frame row must carry exactly identity.cols cells", 0
		}
		for _, c := range row.Cells {
			if utf8.RuneCountInString(c.Char) > maxCellRunes {
				return "a cell carries more than a terminal glyph", 0
			}
			totalChars += utf8.RuneCountInString(c.Char)
			if n := utf8.RuneCountInString(derefOrEmpty(c.Attrs.Fg)); n > 64 {
				return "a cell attribute exceeds the length bound", 0
			}
			if n := utf8.RuneCountInString(derefOrEmpty(c.Attrs.Bg)); n > 64 {
				return "a cell attribute exceeds the length bound", 0
			}
		}
	}
	if totalChars > maxFrameChars {
		return "frame is too large: character budget exceeded", 0
	}
	return "", totalChars
}

// validateAgentAsk maps the wire ask onto the ledger's AgentAsk.
func validateAgentAsk(p agentAskParams) (content.AgentAsk, []assistant.AttachedContentItem, string) {
	empty := func(msg string) (content.AgentAsk, []assistant.AttachedContentItem, string) {
		return content.AgentAsk{}, nil, msg
	}
	if strings.TrimSpace(p.AskID) == "" || utf8.RuneCountInString(p.AskID) > maxIDRunes {
		return empty("askId is required and bounded")
	}
	if p.SessionID == "" {
		return empty("sessionId is required")
	}
	if strings.TrimSpace(p.Question) == "" || utf8.RuneCountInString(p.Question) > maxQuestionRunes {
		return empty("question is required and bounded")
	}
	if strings.TrimSpace(p.Cwd) == "" || utf8.RuneCountInString(p.Cwd) > maxCwdRunes {
		return empty("cwd is required and bounded")
	}
	raw := bytes.TrimSpace(p.AttachedContent)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return empty("attachedContent is required and must be an array")
	}
	var rawGrants []json.RawMessage
	if err := json.Unmarshal(raw, &rawGrants); err != nil || rawGrants == nil {
		return empty("attachedContent must be an array of grant objects")
	}
	if len(rawGrants) > maxAttachedContent {
		return empty("attachedContent must carry at most " + strconv.Itoa(maxAttachedContent) + " items")
	}
	attached := make([]assistant.AttachedContentItem, 0, len(rawGrants))
	for i, rawGrant := range rawGrants {
		var grant agentAttachedContentWire
		decoder := json.NewDecoder(bytes.NewReader(rawGrant))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&grant); err != nil {
			return empty(fmt.Sprintf("attachedContent[%d] must be a grant object: %v", i, err))
		}
		if strings.TrimSpace(grant.ItemID) == "" || utf8.RuneCountInString(grant.ItemID) > maxIDRunes {
			return empty(fmt.Sprintf("attachedContent[%d].itemId is required and bounded", i))
		}
		if strings.TrimSpace(grant.Command) == "" || utf8.RuneCountInString(grant.Command) > maxAttachedCommandRunes {
			return empty(fmt.Sprintf("attachedContent[%d].command is required and bounded", i))
		}
		switch grant.State {
		case "running", "exited":
		default:
			return empty(fmt.Sprintf("attachedContent[%d].state must be running or exited", i))
		}
		attached = append(attached, assistant.AttachedContentItem{
			ItemID: grant.ItemID, Command: grant.Command, State: grant.State,
		})
	}
	in := content.AgentAsk{
		ID:         p.AskID,
		SessionID:  new(p.SessionID),
		Cwd:        p.Cwd,
		Question:   p.Question,
		References: []content.AgentReference{},
	}
	return in, attached, ""
}

// priorTurn reads the turn before this one in this run's pane — the
// conversation the follow-up question is a follow-up TO. Nil, and no error,
// when there is nothing before it, and nil when the session is the pipe of no
// recorded pane: a turn with no anchor has no thread, and answering from every
// pane's turns would put another tab's conversation into this one.
//
// The read goes through the operation like every other store touch on this
// path — one short acquisition, the gate never held across the stream.
func (h agentHandlers) priorTurn(ctx context.Context, rc askRunContext) (*content.PriorTurn, error) {
	if rc.paneID == nil || *rc.paneID == "" {
		return nil, nil
	}
	var prior *content.PriorTurn
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		var e error
		prior, e = svc.PriorTurn(ctx, *rc.paneID, rc.entryID)
		return e
	})
	if err != nil {
		return nil, err
	}
	return prior, nil
}

// proseGoneNotice is what an earlier turn says instead of its answer when
// retention has taken the text. It is a STATED ABSENCE and not a hole: the
// question above it is in the conversation either way, so leaving the answer
// out entirely would read as "that question was never answered", and inventing
// or paraphrasing one would be worse still. AGENTS.md: a soft degrade must be
// visible, not silent — and the surface it has to be visible on here is the
// model's own context, because that is who reads this message.
const proseGoneNotice = "[the text of this answer is no longer stored: retention evicted it]"

// proseCutShortNotice marks a partial answer AS partial. Which it is — a real
// message or an unfinished attempt — is a fact about the run's state and never
// about how much text there is: an interrupted run leaves exactly the rows a
// finished one leaves, so a reader with only the text cannot tell.
const proseCutShortNotice = "[this answer was cut short: the run did not finish]"

// priorAnswerMessage is one earlier turn's answer as the model is told it, and
// the bool is whether there is anything to tell. It is the whole of the
// mapping from a stored turn to a Message — the ARRANGEMENT (which run, what
// order, whether the text survives) is the ledger's and has already happened.
//
// Four states, and each is said rather than inferred:
//
//   - the run finished and printed prose → the prose, whole, in seat order.
//   - the run printed prose and did NOT finish → the prose, marked partial.
//   - the prose was evicted → the sentence that says so, never a hole.
//   - the run printed nothing → nothing. Not a degrade and not a loss: an
//     answer that was never written has no text to miss, and a marker there
//     would claim one was.
func priorAnswerMessage(p content.TurnProse) (string, bool) {
	if p.Evicted {
		return proseGoneNotice, true
	}
	if p.Text == "" {
		return "", false
	}
	if p.State == content.RunCompleted {
		return p.Text, true
	}
	return p.Text + "\n\n" + proseCutShortNotice, true
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── registration ──────────────────────────────────────────────────────────

// agentSpecs declares the agent.* control methods on the CONTENT operation
// queue (the ask transaction is the ledger — ADR-0019's one writer — so it
// shares the content domain's gate and queue).
func (s *WSServer) agentSpecs(contentSub control.Submission, lane control.Admission, contentGate control.Admission, configOp capability.ConfigOperation, endpointWired bool, credentials credential.Resolver, client assistant.Client, askSub control.Submission) []methodSpec {
	var agentOp capability.AgentOperation
	if s.contentDB != nil {
		agentOp = capability.NewAgentOperation(contentGate, lane, s.contentDB)
	}
	var attemptLedger assistant.AttemptLedger
	if s.contentDB != nil {
		attemptLedger = s.contentDB.Ledger()
	}
	build := func(w *wsConn, state *connState, r Responder) agentHandlers {
		// clientID is the CONNECTION identity, deliberately: it binds ask
		// idempotency to the connection (a reconnect mints a new one), never
		// to a renderer-minted tab.
		return agentHandlers{
			op: agentOp, configOp: configOp, endpointWired: endpointWired,
			credentials: credentials, client: client, askSub: askSub,
			attemptLedger: attemptLedger, grantFor: s.runGrantFor,
			requester: s, knownMaterial: s.agentKnownMaterial,
			approvals: s.agentApprovals, pendingRuns: s.pendingRuns,
			pendingRunsMu:        &s.pendingRunsMu,
			personalInstructions: s.personalInstructionsText,
			sessionPolicy:        s.sessionPolicy, globalPolicy: s.agentPolicy,
			log: s.log, state: state, clientID: connectionID(w), r: r,
		}
	}
	return []methodSpec{
		reg(contentSub, "agent.ask", genericObject("per-field validation pending nocx-VALID"), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAsk(ctx, req) }
		}),
		reg(contentSub, "agent.cancel", params(validateAgentCancelRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCancel(ctx, req) }
		}),
		reg(contentSub, "agent.approve", params(validateAgentApproveRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleApprove(ctx, req) }
		}),
	}
}
