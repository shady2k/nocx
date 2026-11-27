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

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/operation/catalog"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
)

// ── ingress bounds (design §7: "this domain validates params and bounds
//    sizes") ───────────────────────────────────────────────────────────────

const (
	// maxQuestionRunes bounds the question text.
	maxQuestionRunes = 8_000
	// maxAttachedContent bounds the number of item grants one ask may carry.
	maxAttachedContent = 32
	// maxAttachedCommandRunes bounds the descriptive command carried with a grant.
	maxAttachedCommandRunes = 8_000
	// maxIDRunes bounds client-supplied ask and attached-item ids.
	maxIDRunes = 128
	// maxCwdRunes bounds the renderer-supplied cwd.
	maxCwdRunes = 4_096
	// maxAttachedWindowLines mirrors contracts/tools/session.read.schema.json:
	// session.read permits any nonnegative start but caps count at 2000.
	// Therefore only count is bounded here; start has no ceiling, and the
	// check below rejects integer overflow.
	maxAttachedWindowLines = 2_000
)

// ── wire shapes ───────────────────────────────────────────────────────────

type agentAskParams struct {
	AskID           string          `json:"askId"`
	SessionID       string          `json:"sessionId"`
	Question        string          `json:"question"`
	AttachedContent json.RawMessage `json:"attachedContent"`
	Cwd             string          `json:"cwd"`
}

type agentAttachedContentWire struct {
	ItemID    string `json:"itemId"`
	Command   string `json:"command"`
	State     string `json:"state"`
	Start     *int   `json:"start,omitempty"`
	Count     *int   `json:"count,omitempty"`
	Automatic bool   `json:"automatic,omitempty"`
}
type agentCancelParams struct {
	RunID int64 `json:"runId"`
}

// agentSteerParams is a person's message to the turn that is already running:
// the run it belongs to, and the words. Nothing else travels — the message is
// not a question, so it carries no grants and no cwd.
type agentSteerParams struct {
	RunID int64  `json:"runId"`
	Text  string `json:"text"`
}

// agentCancelResponse is the real result of agent.cancel. cancelled is true
// only when this request closed a live run; an already-terminal or unknown
// run is answered as an error rather than being reported as stopped.
type agentCancelResponse struct {
	RunID         int64  `json:"runId"`
	State         string `json:"state"`
	Cancelled     bool   `json:"cancelled"`
	Error         string `json:"error,omitempty"`
	DroppedDeltas int    `json:"droppedDeltas,omitempty"`
	// DroppedSteers are the person's messages the cancelled turn's agent had
	// queued and dropped. The reserved response carries them as well as the
	// runState notification, because that notification may be dropped under
	// outbound pressure and these are messages a person must not lose.
	DroppedSteers []string `json:"droppedSteers,omitempty"`
	UnarmedBounds []string `json:"unarmedBounds,omitempty"`
}

// What happened to a person's message. Two values and no third: it went into
// the turn the person was watching, or it will be asked as that conversation's
// next question (ADR-anntnxdj decision 4).
const (
	// steerSteered is the message the agent took inside the running turn.
	steerSteered = "steered"
	// steerNextQuestion is the message the turn could not take: the agent does
	// not steer, the turn ended first, or the agent did not accept it in time.
	steerNextQuestion = "next_question"
)

// agentSteerResponse is the real result of agent.steer: the run the message was
// about, and what became of it. The outcome is stated rather than derived from
// a flag, because the block shows the message differently for each — inline in
// the answer, or inline and waiting to be asked next.
type agentSteerResponse struct {
	RunID   int64  `json:"runId"`
	Outcome string `json:"outcome"`
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
	mu                 sync.Mutex
	eventsMu           sync.Mutex
	drive              *agentRunDrive
	runLeases          map[*runLease]struct{}
	cancelled          bool
	terminalized       bool
	droppedDeltas      int
	cancelState        content.RunState
	cancelReason       content.TerminationReason
	cancelSentence     string
	cancelHasDetails   bool
	cancelDone         chan struct{}
	driveDone          chan struct{}
	usage              *content.RunUsage
	answeringModel     string
	actorTerminalState content.RunState
	// steerer is the running turn's way in for a person's message — the
	// harness Turn askHarness started. It is attached once and never
	// replaced: a steer addresses the turn that is answering, and a turn that
	// has ended refuses the message itself.
	steerer agentharness.Steerer
	// steerBoundary writes a person's message into the turn's record, in order
	// with the answer's runs (nocx-a62cl.6.4). Nil until the stream installs it.
	steerBoundary func(ctx context.Context, text string) error
	// turnCutoff is the turn's arrivals counter, installed with the run's tool
	// chain: the calls a person's answer covers are the ones queued by the time
	// it settles (ADR-anntnxdj §2).
	turnCutoff func() uint64
	// droppedSteers is what a cancelled turn's agent had queued and dropped:
	// messages nobody used, which become the conversation's next questions
	// (ADR-4khgzeyp decision 3). Set when the turn reports its terminal.
	droppedSteers []string
}

type agentRunDrive struct {
	// cancel ends the drive WITH ITS CAUSE: the reason the run is ending
	// travels with the cancellation, so a tool call the end caught closes its
	// attempt with the same word the run closes with — the person's stop, an
	// answer taken back — rather than as a tool failure or a lost connection
	// (nocx-a62cl.4.1).
	cancel context.CancelCauseFunc
}

type agentRunControlContextKey struct{}

func (c *agentRunControl) beginDrive(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	drive := &agentRunDrive{cancel: cancel}
	c.mu.Lock()
	if c.cancelled || c.terminalized {
		cancel(context.Canceled)
	} else {
		c.drive = drive
	}
	c.mu.Unlock()
	ctx = context.WithValue(ctx, agentRunControlContextKey{}, c)
	return ctx, func() {
		cancel(context.Canceled)
		c.mu.Lock()
		if c.drive == drive {
			c.drive = nil
		}
		c.mu.Unlock()
	}
}

func agentRunControlFromContext(ctx context.Context) *agentRunControl {
	control, _ := ctx.Value(agentRunControlContextKey{}).(*agentRunControl)
	return control
}

// attachSteerer names the running turn a person's message goes into. The
// harness Turn is the only thing that can deliver it: the actor owns the
// agent process, and asking the connector instead would be a second route to
// the same session.
func (c *agentRunControl) attachSteerer(steerer agentharness.Steerer) {
	c.mu.Lock()
	c.steerer = steerer
	c.mu.Unlock()
}

// attachSteerBoundary names the stream's way of writing a person's message into
// the turn's record. It is installed by the stream and cleared when the stream
// ends, because it is the stream that owns the order the answer's runs land in.
func (c *agentRunControl) attachSteerBoundary(boundary func(ctx context.Context, text string) error) {
	c.mu.Lock()
	c.steerBoundary = boundary
	c.mu.Unlock()
}

func (c *agentRunControl) attachedSteerBoundary() func(ctx context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.steerBoundary
}

// attachTurnCutoff names the turn's arrivals counter: how many of its calls
// have arrived so far, the boundary a person's answer covers (ADR-anntnxdj §2).
// Nil for a run whose chain has no order to cover.
func (c *agentRunControl) attachTurnCutoff(cutoff func() uint64) {
	c.mu.Lock()
	c.turnCutoff = cutoff
	c.mu.Unlock()
}

func (c *agentRunControl) attachedTurnCutoff() func() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnCutoff
}

func (c *agentRunControl) attachedSteerer() agentharness.Steerer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.steerer
}

func (c *agentRunControl) attachRunLease(lease *runLease) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled || c.terminalized {
		return false
	}
	if c.runLeases == nil {
		c.runLeases = make(map[*runLease]struct{})
	}
	c.runLeases[lease] = struct{}{}
	return true
}

func (c *agentRunControl) detachRunLease(lease *runLease) {
	c.mu.Lock()
	delete(c.runLeases, lease)
	c.mu.Unlock()
}

func (c *agentRunControl) cancelRunLeases(reason content.TerminationReason) bool {
	c.mu.Lock()
	leases := make([]*runLease, 0, len(c.runLeases))
	for lease := range c.runLeases {
		leases = append(leases, lease)
	}
	c.mu.Unlock()
	unsupported := false
	for _, lease := range leases {
		if lease.cancelExecution(reason) == foregroundUnsupported {
			unsupported = true
		}
	}
	return unsupported
}

func (c *agentRunControl) recordDroppedDelta() {
	c.mu.Lock()
	c.droppedDeltas++
	c.mu.Unlock()
}

func (c *agentRunControl) droppedDeltaCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.droppedDeltas
}

func (c *agentRunControl) beginCancel() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.terminalized || c.cancelled || c.actorTerminalState != "" {
		return false
	}
	c.cancelled = true
	return true
}

// cancelContext ends the drive for ONE stated reason: the person's Stop or an
// answer taken back. A run ended for any other reason — a closed window, a
// cancelled request — reaches the drive through its own parent and carries no
// reason of its own, so what a call records is "interrupted" rather than a
// decision nobody took.
func (c *agentRunControl) cancelContext(reason content.TerminationReason) {
	c.mu.Lock()
	drive := c.drive
	c.mu.Unlock()
	if drive != nil {
		drive.cancel(assistant.TurnEndedCause(reason))
	}
}

func (c *agentRunControl) finishCancel(state content.RunState, reason content.TerminationReason, sentence string, hasDetails bool) {
	c.mu.Lock()
	c.cancelState = state
	c.cancelReason = reason
	c.cancelSentence = sentence
	c.cancelHasDetails = hasDetails
	close(c.cancelDone)
	c.mu.Unlock()
}

func (c *agentRunControl) cancelOutcome() (content.RunState, content.TerminationReason, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelState == "" {
		return content.RunCancelled, content.TermUserKilled, "the person stopped this answer", false
	}
	return c.cancelState, c.cancelReason, c.cancelSentence, c.cancelHasDetails
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
// overlapping asks — "the current answer" is not an identity. seq ascends per
// run from 0 and is the renderer's ordering key; a question blocks the call
// that asked (ADR-anntnxdj §3), so one drive emits every number and nothing
// continues an answer on a second one.
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
// state. error is present for failed runs and for cancelled runs only when
// cancellation could not stop an assistant-run child registered to this exact
// turn; a user-started host command is outside agent.cancel. It is a sentence
// a person reads, never a Go error string (design §7). droppedDeltas is present only
// when the live view is incomplete: the wire refused one or more
// agent.runDelta frames (a full outbound queue — outbound's deliberate
// non-blocking policy), so the renderer must not read the block it received
// as a complete answer. The durable answer is whole either way — every
// chunk was persisted before the notify — so the marker is a live-view
// bound, never a reason to treat the run as failed (nocx-dw3.1). unarmedBounds
// is present only when shell integration was unavailable; each item is a
// human-readable sentence naming one bound that did not apply.
type agentRunState struct {
	Usage         *content.RunUsage `json:"usage,omitempty"`
	RunID         int64             `json:"runId"`
	State         string            `json:"state"`
	Error         string            `json:"error,omitempty"`
	DroppedDeltas int               `json:"droppedDeltas,omitempty"`
	UnarmedBounds []string          `json:"unarmedBounds,omitempty"`
	// DroppedSteers are the person's messages the cancelled turn's agent had
	// queued and dropped: nobody used them, so they are asked as this
	// conversation's next questions (ADR-4khgzeyp decision 3). Present only
	// when at least one was dropped.
	DroppedSteers []string `json:"droppedSteers,omitempty"`
	// Notices are what the RUN must state about ITSELF — today, only that it
	// stopped asking to widen a scope because it reached the bound on how
	// often one answer may ask (design §5.3). A silent stop is the soft
	// degrade AGENTS.md forbids: the person is told the assistant stopped
	// asking rather than left to infer it from questions that never arrive.
	Notices []string `json:"notices,omitempty"`
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
	Standing agentApprovalStanding     `json:"standing"`
	// Expansion is what the verbatim command's variables currently read as,
	// carried BESIDE the verbatim string and never instead of it
	// (nocx-4h0m7.5, nocx-y47mi SETTLED 1). Every unsafe expansion is left
	// exactly as written and labelled with the reason reading it would have
	// an effect, and a shell that could not be asked at all is its own
	// state — the surface must never present "we refuse to ask" and "we
	// could not ask" as the same fact. Absent for non-command proposals.
	Expansion *assistant.ExpansionFacts `json:"expansion,omitempty"`
	// OutOfScope is present only when a resource of the call fell outside a
	// bound (design §5.3, nocx-b453p). Without it the prompt's three widths
	// — this call, this session, always — widen NOTHING that excluded the
	// resource, so the next identical call asks again, for ever.
	OutOfScope *agentApprovalOutOfScope `json:"outOfScope,omitempty"`
	// Scripts is the whole of every file the proposed command NAMES, read
	// at the moment the question was asked (nocx-872jc.3). Same kind of
	// thing as Expansion and carried for the same reason: `bash deploy.sh`
	// is a name, and approving a name is not approving an act. It is a
	// READING — the string in Arguments is what is sent, byte for byte, and
	// the file can change before it runs. Absent whenever the parse named
	// no file to execute or source, so a proposal with no script draws no
	// empty affordance.
	Scripts []assistant.ScriptReading `json:"scripts,omitempty"`
}

// agentApprovalOutOfScope is what fell outside, which bound it fell outside,
// and whether an answer can move that bound. The offer is carried rather than
// derived from the cause in the renderer for the reason the effect is
// (ADR-0028 decision 4): the backend is what will apply the answer, so the
// backend is what says whether it can be given.
type agentApprovalOutOfScope struct {
	Cause    string                `json:"cause"`
	Resource content.GrantScope    `json:"resource"`
	Widening agentApprovalWidening `json:"widening"`
}

// agentApprovalWidening is the offer, shaped like agentApprovalStanding for
// the same reason: available says whether the prompt may show the answer, and
// reason is what it says instead when it may not.
type agentApprovalWidening struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// wideningOffer decides whether the fourth answer may be shown. An editable
// row scope is the ONLY yes: a fence — the run's own bound, or a narrowed
// capability — cannot be moved by any answer, and offering a question whose
// yes cannot be honoured is the lie §5.3 exists to remove.
func wideningOffer(cause content.OutOfScopeCause) agentApprovalWidening {
	if cause == content.OutOfScopeRowScope {
		return agentApprovalWidening{Available: true}
	}
	return agentApprovalWidening{
		Reason: "this bound is the run's own fence, and no answer here can widen it",
	}
}

// agentStandingAnswerSaved is the agent.standingAnswerSaved notification
// (nocx-2019q, contracts/agent.standingAnswerSaved.schema.json): the standing
// half of an answer reached the store, and the turn that asked the question
// says so where it was asked.
//
// It carries FACTS, not a sentence. The receipt reads in the words of the
// button the person clicked, and those words are built once, on the surface
// that offered the answer — a sentence minted here would be a second spelling
// of one concept, which is the duplication AD-8 exists to prevent.
type agentStandingAnswerSaved struct {
	RunID string `json:"runId"`
	// EntryID is the TURN's ledger entry, and it is NEVER EMPTY. The
	// question the schema used to leave open — "can a receipt come from a
	// run with no entry?" — is answered by the chain that produces one, and
	// the chain has no branch: a receipt exists only where agent.approve
	// found the run in pendingRuns; pendingRuns is filled by handleAsk
	// alone, after the ledger transaction returned the turn's entry
	// (SubmitAgentAsk answers with the ask id, which validateAgentAsk
	// refuses empty); and handleAsk itself is refused when no content store
	// is wired. So there is no producer of an empty one.
	//
	// Which is why notifyStandingAnswer REFUSES to send a receipt without
	// it rather than sending one anyway: the renderer routes by this field
	// and drops anything that is not the block's own, so a receipt with no
	// entry is one nobody can ever see — the exact invisibility nocx-2019q
	// was.
	EntryID  string `json:"entryId"`
	Approved bool   `json:"approved"`
	Scope    string `json:"scope"`
	Rule     string `json:"rule"`
	Effect   string `json:"effect"`
	// RuleID is what an Undo names, and it is empty exactly where an undo
	// by id is not expressible: a session overlay and a matrix row.
	RuleID string `json:"ruleId"`
}

type agentApprovalStanding struct {
	Available bool   `json:"available"`
	Rule      string `json:"rule"`
	Reason    string `json:"reason"`
}

func standingOffer(inv content.Invocation, hasInvocation, commandInvocation bool, effect content.Effect) agentApprovalStanding {
	if !commandInvocation {
		if effect == "" {
			return agentApprovalStanding{Reason: "the question named no effect class"}
		}
		return agentApprovalStanding{Available: true}
	}
	if !hasInvocation {
		_, reason := content.StandingRule(inv)
		return agentApprovalStanding{Reason: reason}
	}
	rule, reason := content.StandingRule(inv)
	if reason != "" {
		return agentApprovalStanding{Reason: reason}
	}
	return agentApprovalStanding{Available: true, Rule: rule.Label()}
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
	// approveScopeExpand is the fourth answer, and it is not a fourth WIDTH
	// (design §5.3): it is the distinct administrative answer to an
	// out-of-scope question — widen the row's scopes to cover the resource
	// that fell outside AND approve this call, as one act. It is offered
	// only where agent.approvalRequested said it could be, and refused
	// everywhere else.
	approveScopeExpand = "expand"
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
// to — streaming, because the call the person answered goes on in the drive it
// was already in. The outcome the renderer draws comes from the
// agent.runState notification.
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

type agentAskEventKind string

const (
	agentAskAnswer      agentAskEventKind = "answer"
	agentAskReasoning   agentAskEventKind = "reasoning"
	agentAskToolCall    agentAskEventKind = "tool_call"
	agentAskToolStarted agentAskEventKind = "tool_started"
	// agentAskSteer is a person's message, delivered into the stream so the
	// ledger records it in the turn's order (nocx-a62cl.6.4). It is not a
	// notification: the renderer drew the line from agent.steer's own answer,
	// and only the durable record needs the seat.
	agentAskSteer agentAskEventKind = "steer"
)

type agentAskEvent struct {
	Kind agentAskEventKind
	Text string
	Call *assistant.ToolCall
}

// ── handlers ──────────────────────────────────────────────────────────────

// agentHandlers answers agent.ask and the related run-control methods. It
// holds the AgentOperation (nil → the ledger is not wired), the connection's
// connState (session ownership and the session facts the environment is
// derived from), the connection's client identity (the idempotency binding of
// ask ids — per connection, so a reconnect mints a new one) and the
// Responder; never the *WSServer.
type agentHandlers struct {
	op         capability.AgentOperation  // nil → content store not wired
	configOp   capability.ConfigOperation // role choices
	noteOp     capability.NoteOperation
	snippetOp  capability.SnippetOperation
	agentTools agenttools.Registry
	log        log.Logger
	client     assistant.Client
	harness    agentharness.Harness
	harnessID  string
	askSub     control.Submission
	// fetcher is the guarded direct-network seam for fetch.url.
	fetcher apifetch.TextFetcher
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
	// requester is the renderer-orchestrated command submission seam.
	requester assistant.RendererRequester
	// screenCapture and sessions are the assistant's backend-owned read seams.
	screenCapture assistant.ScreenCaptureSource
	sessions      assistant.SessionSource
	// filesOpen and filesBind are the assistant's route to the pane's own
	// files (stage 10, ADR-anntnxdj §5): the SAME operations the Files panel
	// runs on, asked per tool call for a short-lived binding of the run's own.
	// Nil in either builds no file capability at all, which is the honest
	// refusal rather than a fallback to this machine's disk.
	filesOpen capability.FilesystemOpenOperation
	filesBind capability.FilesystemBindingOperation
	// expansions is the authenticated-channel seam that answers "what does
	// $HOME read as, in the shell that would run this" for an approval
	// question (nocx-4h0m7.5, assistant.ExpansionSource). The server
	// implements it; see ws_expansion.go for what it can and cannot answer
	// today, and why the honest refusal is the product's own outcome rather
	// than a stub.
	expansions assistant.ExpansionSource
	// workerStore is the worker record a coordinator run may start workers in and ask
	// about (nocx-dkawo.8). Nil until the composition root wires one, which
	// is what an unopened content store leaves.
	workerStore assistant.WorkerRecord
	// paneAccessBinder mints a run's DescendantPaneAccess/SessionReads
	// (assistant.PaneAccessBinder, design §7.1, §7.3, Task 8). Nil is the
	// honest shape for a build with no composition-root wiring yet.
	paneAccessBinder assistant.PaneAccessBinder
	// scripts reads the whole of a file a proposed command names, so the
	// approval question can carry the script and not only its name
	// (nocx-872jc.3). The server implements it; see ws_script.go for what
	// it can and cannot reach.
	scripts assistant.ScriptSource
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
	// pendingRuns maps a run's id to the context its call is blocked in
	// (question, references, resolved endpoint material). Server-scoped —
	// the person answers on the same connection that rendered the question,
	// and the answer must wake the EXACT run.
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
func systemPromptFactsFor(cwd string, env content.Environment, attached []assistant.AttachedContentItem, personal string) assistant.SystemPromptFacts {
	f := assistant.SystemPromptFacts{
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

// markedSessionWindows is the other half of the same envelope: the row spans
// a PERSON marked. They bound a session.read of those items, so a call that
// names the item without naming the window is answered inside the mark
// rather than past it (nocx-hp8p2.15). A whole-block mark carries no span
// and bounds nothing.
//
// AN AUTOMATIC ITEM CAN CARRY A SPAN TOO, and it means the same thing: rows
// selected inside the frozen screen a summon attached narrow THAT item
// rather than adding a second one (nocx-hp8p2.7). Excluding automatic items
// here read `automatic` as "nobody marked this", which is true of the whole
// screen and false of a band of it — the span itself is what says a person
// chose, and an attachment with no span is skipped by the check below either
// way.
func markedSessionWindows(attached []assistant.AttachedContentItem) []assistant.MarkedSessionWindow {
	marks := make([]assistant.MarkedSessionWindow, 0, len(attached))
	for _, item := range attached {
		if item.Start == nil || item.Count == nil || *item.Count <= 0 {
			continue
		}
		marks = append(marks, assistant.MarkedSessionWindow{
			ItemID: item.ItemID, Start: *item.Start, Count: *item.Count,
		})
	}
	if len(marks) == 0 {
		return nil
	}
	return marks
}

func automaticSessionItems(attached []assistant.AttachedContentItem) []string {
	ids := make([]string, 0, len(attached))
	for _, item := range attached {
		if item.Automatic {
			ids = append(ids, item.ItemID)
		}
	}
	return ids
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

// validateAgentAskRaw validates the complete ask envelope before the handler
// can create a run. The semantic validator remains the single owner of the
// field and attached-content rules; this wrapper makes that check part of
// registration rather than a handler-only habit.
func validateAgentAskRaw(raw json.RawMessage) string {
	p, msg := decodeAgentAskParams(raw)
	if msg != "" {
		return msg
	}
	_, _, msg = validateAgentAsk(p)
	return msg
}

// detachedRunContext is the context a streamed run gets: it outlives the
// frame that started it, so the handler's return must not cancel it, while
// rpc.cancel and disconnect still do through the request entry.
func (h agentHandlers) detachedRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	parent := context.WithoutCancel(ctx)
	if conn, ok := connectionContext(ctx); ok {
		parent = conn
	}
	base, cancel := context.WithCancel(parent)
	retainRequestWork(ctx, cancel)
	return base, cancel
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

	if h.harness == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "the answering agent is not available"})
		return
	}
	conversation := string(sid)
	if in.PaneID != nil {
		conversation = *in.PaneID
	}
	model, err := h.answeringModel(ctx, conversation)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprint(err)})
		return
	}
	var ready agentharness.Readiness
	if checker, ok := h.harness.(agentharness.ModelReadiness); ok {
		ready = checker.ReadinessForModel(ctx, model)
	} else {
		ready = h.harness.Readiness(ctx)
	}
	if ready.State != agentharness.Ready {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: ready.Message})
		return
	}
	facts := content.RunFacts{Mode: "explain", Harness: h.harnessID, ConversationID: conversation, Model: model}
	in.Facts = facts

	// Record the turn before the harness starts so streamed events have a durable entry.
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
	// The EXCHANGE names itself for everything downstream: this ask, the
	// approval that blocks it, the answer that wakes it and every effect any
	// of them run all log under one trace, across the several wire frames it
	// takes (internal/log).
	// The run outlives this handler: the response is already sent when the
	// stream starts. Its context must therefore not be the request context,
	// which is cancelled when the worker returns; it is detached from that
	// cancellation and retained on the request entry instead, so rpc.cancel
	// and disconnect still end it.
	runBase, cancelRun := h.detachedRunContext(ctx)
	runCtx, _ := log.StartTrace(runBase, runTrace(askRes.RunID))
	leaseDegradation := assistant.NewRunLeaseDegradation()
	runControl := &agentRunControl{cancelDone: make(chan struct{}), driveDone: make(chan struct{})}
	rc := askRunContext{
		runID:    askRes.RunID,
		control:  runControl,
		entryID:  askRes.EntryID,
		paneID:   in.PaneID,
		question: in.Question,
		model:    facts.Model,
		grant:    runGrant,
		// attempt is the run's attempt — the ledger inserted the run row at
		// attempt 1 (SubmitAgentAsk), and it is the value the approval
		// binding names. The approved call's own execution is the proposal
		// entry's SUBSEQUENT attempt (attempt 2), recorded by the kernel.
		attempt:          1,
		sessionID:        sid,
		leaseDegradation: leaseDegradation,
		// The facts the model is told come from their existing owners: the cwd
		// this question carried and the ledger recorded with it, the pane's
		// environment as environmentForSession already derived it, and the
		// person's own paragraph as the settings document holds it right now.
		promptFacts:           systemPromptFactsFor(in.Cwd, in.Env, attached, h.personalParagraph()),
		automaticSessionItems: automaticSessionItems(attached),
		markedSessionWindows:  markedSessionWindows(attached),
	}
	h.state.mu.Lock()
	if h.state.agentConversations == nil {
		h.state.agentConversations = make(map[string]struct{})
	}
	h.state.agentConversations[conversation] = struct{}{}
	h.state.mu.Unlock()
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
		defer cancelRun()
		h.runAskStream(taskCtx, rc, h.r)
	}}); rej != nil {
		cancelRun()
		// The stream was refused: nothing is in flight. Drop the stored
		// context so a later agent.approve cannot target a run that never
		// started — the run terminalizes below and has no question open.
		h.pendingRunsMu.Lock()
		delete(h.pendingRuns, rc.runID)
		h.pendingRunsMu.Unlock()
		h.terminalize(ctx, rc, content.RunFailed, content.TermFailed,
			"too many answers in flight — try again in a moment", h.r)
	}
}

// askRunContext is the durable run identity and non-secret input handed to the
// stream task. Secret material is resolved by that task and never persisted.
type askRunContext struct {
	runID   int64
	control *agentRunControl
	entryID string
	// paneID is the stable conversation anchor, independent of the live session.
	paneID   *string
	question string
	model    string
	// grant is the run's authority (ADR-0020 decision 5), minted by the
	// workspace policy the composition root named (runGrantFor). Nil: the
	// run executes no tools — the model is offered none.
	grant *content.Grant
	// attempt is the run's attempt — the ledger inserted the run row at
	// attempt 1 (SubmitAgentAsk), and it is the value the approval binding
	// names. The approved call's own execution is the proposal entry's
	// SUBSEQUENT attempt (attempt 2), which the middleware records.
	attempt int
	// promptFacts is what the standing system prompt says about this pane,
	// gathered when the ask arrived from the owners that already hold each
	// fact (systemPromptFactsFor). The FACTS are carried, never the
	// assembled text: the prompt is assembled once per run, so there is
	// nothing to go stale and one place that assembles it.
	promptFacts assistant.SystemPromptFacts
	// automaticSessionItems is pinned from the renderer's attached-content
	// metadata at ask submission.
	automaticSessionItems []string
	// markedSessionWindows is the row spans the person marked, pinned from
	// the same attached-content envelope.
	markedSessionWindows []assistant.MarkedSessionWindow
	// sessionID is the session the run lives in — the session an "allow in
	// this session" answer is about. Carried EXPLICITLY, from the ask that
	// named it, rather than read back out of the grant: the grant's scope
	// union is what the run may reach, which is not a fact about any one
	// row and would stop being the session the moment a row is scoped
	// wider.
	sessionID session.ID
	// leaseDegradation crosses the stream drive so terminalize can report
	// bounds the renderer requester could not arm.
	leaseDegradation *assistant.RunLeaseDegradation
	// pendingReason is which question the run's call is BLOCKED on —
	// "policy" or "egress", empty when nothing is open. Written by the
	// approval service's wait, and read by agent.approve, which refuses a
	// standing answer to an egress question. It is a fact about the OPEN
	// QUESTION, not about the proposal; only the transport knows which of
	// the two asked.
	pendingReason string
	// approvalWait is the question this run's call is BLOCKED on
	// (nocx-a62cl.3.1). The approval service records it before it publishes
	// the question, agent.approve claims it and wakes the very call that
	// asked, and the service clears it when the wait is over.
	approvalWait *answerWait
}

func logAgentStreamEnded(logger log.Logger, runID int64, started time.Time, reasoning, answer, requested, executed int, streamErr error) {
	if logger == nil {
		return
	}
	outcome := "completed"
	if streamErr != nil {
		outcome = "failed"
	}
	logger.Info("agent ask: the stream ended",
		"run", runID,
		"elapsed", time.Since(started).Round(time.Millisecond).String(),
		"reasoning", reasoning,
		"answer", answer,
		"toolCallsRequested", requested,
		"toolCallsExecuted", executed,
		"outcome", outcome)
}

// runAskStream drives the prepared run to completion. Secret material resolves
// first, outside a capability admission and before any external request. A
// sealed vault therefore waits and continues this same durable run.
func (h agentHandlers) runAskStream(ctx context.Context, rc askRunContext, r Responder) {
	if rc.control != nil && rc.control.driveDone != nil {
		defer close(rc.control.driveDone)
	}
	opScope := catalog.BeginInternal(log.WithLogger(ctx, h.log), catalog.OpInternalAgentRun)
	defer opScope.Complete()
	ctx = catalog.WithScope(ctx, opScope)
	if rc.leaseDegradation != nil {
		ctx = assistant.WithRunLeaseDegradation(ctx, rc.leaseDegradation)
	}
	streamStarted := time.Now()
	var reasoningChars, answerChars atomic.Int64
	var toolCallsRequested, toolCallsExecuted atomic.Int64
	var toolStatsMu sync.Mutex
	requestedCalls := make(map[string]struct{})
	recordRequested := func(callID string) {
		toolStatsMu.Lock()
		defer toolStatsMu.Unlock()
		if _, seen := requestedCalls[callID]; !seen {
			requestedCalls[callID] = struct{}{}
			toolCallsRequested.Add(1)
		}
	}
	var streamErr error
	emitStreamEnded := func() {
		logAgentStreamEnded(h.log, rc.runID, streamStarted, int(reasoningChars.Load()), int(answerChars.Load()),
			int(toolCallsRequested.Load()), int(toolCallsExecuted.Load()), streamErr)
	}
	if rc.control != nil {
		var finishDrive func()
		ctx, finishDrive = rc.control.beginDrive(ctx)
		defer finishDrive()
	}

	// WHAT THE RUN SPENT ITSELF ON (nocx-d6gn4.8.1). A run that thinks for
	// four minutes and emits nothing logs NOTHING today — the lines below
	// are written when a program runs or when the run dies, so the owner
	// watched a spinner and the log had no word for it. These three counters
	// and the pair of lines around the stream make "it went off into
	// reasoning" a number instead of an impression.
	h.log.Info("agent ask: streaming", "run", rc.runID, "model", rc.model,
		"question", len(rc.question))
	// The gate deltas may not pass before: a delta persisted before the
	// streaming transition commits would be a delta outside the run's
	// non-terminal span.
	if err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.TransitionRun(ctx, rc.runID, content.RunStreaming)
	}); err != nil {
		streamErr = err
		emitStreamEnded()
		// The transition was refused: the run is already terminal (closed
		// by another path). Nothing to drive; stop.
		return
	}

	// One drive, one answer, numbered from zero. A person's question blocks
	// the call that asked (ADR-anntnxdj §3), so nothing interrupts this
	// stream and nothing resumes it: the numbering has one owner.
	seq := 0
	// The piece of prose the deltas are landing in: empty at the start and
	// after every tool call, so the first delta opens the next block
	// (ADR-0040).
	var prose content.ProseBlock
	requester := h.requester
	if requester != nil {
		bound := runBoundRequester{RendererRequester: requester, control: rc.control, degradation: rc.leaseDegradation}
		requester = bound
		if waiter, ok := bound.RendererRequester.(assistant.RunWaiter); ok {
			requester = runBoundWaitRequester{runBoundRequester: bound, waiter: waiter}
		}
	}
	err := h.askHarness(ctx, rc, assistant.AskParams{
		Grant:         rc.grant,
		AttemptLedger: h.attemptLedger,
		Requester:     requester,
		ScreenCapture: h.screenCapture,
		SessionSource: h.sessions,
		Expansions:    h.expansions,
		Workers:       h.workerStore,
		// Where this pane is: the environmentForSession value the run's
		// prompt facts already carry, which is also what the ledger records
		// for the run — one derivation, read here rather than made again. It
		// travels so a tool that can only act on this machine refuses a pane
		// on another one instead of reading the local disk (ADR-anntnxdj §5,
		// decision 3).
		Environment: rc.promptFacts.Env,
		// The pane's own files, bound per CALL rather than per run: every
		// files.* invocation acquires one short-lived handle, so the registry
		// holds nothing between calls and nothing after a cancelled one.
		Files: h.filesFor(rc.sessionID),
		// The one environment a spawn can reach in this slice: the machine
		// nocx itself runs on. Derived through content's own id rule so the
		// run fence and the tool's resolver name the same string without
		// either restating the other.
		WorkerEnvironment: content.EnvironmentIDFor(content.EnvLocal, ""),
		PaneAccessBinder:  h.paneAccessBinder,
		Scripts:           h.scripts,
		// The run's OWN cwd — the directory this question carried and the
		// ledger recorded with it — so `bash deploy.sh` in the approval
		// window resolves against where the run was asked from and not
		// against a directory the backend picked (nocx-872jc.3).
		Cwd:              rc.promptFacts.Cwd,
		NoteOperation:    h.noteOp,
		SnippetOperation: h.snippetOp,
		Fetcher:          h.fetcher,
		KnownMaterial:    h.knownMaterial,
		Approvals:        h.approvals,
		// The approval service is this run's Approver (nocx-a62cl.3.1): an
		// ask moves the run to awaiting_approval, publishes the question and
		// blocks THIS call on the person's answer, which then goes on through
		// the guards and runs once. It is handed over beside the store, which
		// is the seam the kernel already received its answers through.
		Questioner: h.approvalService(rc),
		// And the fresh grant after an answer (ADR-anntnxdj §3): the policy as
		// it stands when the person answers, minted by the same owner that
		// minted the run's admission grant — the composition root's, over this
		// session's overlay — so "allow in this session" reaches the very call
		// that was just answered.
		FreshGrant: func() *content.Grant {
			if h.grantFor == nil {
				return nil
			}
			return h.grantFor(string(rc.sessionID))
		},
		RunID:     strconv.FormatInt(rc.runID, 10),
		SessionID: string(rc.sessionID),
		Attempt:   rc.attempt,
		// The turn every entry this run causes is joined to (nocx-h1l4o).
		// It comes off the SAME askRunContext the run id does — both were
		// set from one SubmitAgentAsk result — so the relation is written
		// from a fact the backend is already holding, and the renderer
		// never sends an arrangement of its own.
		TurnEntryID:           rc.entryID,
		AutomaticSessionItems: rc.automaticSessionItems,
		MarkedSessionWindows:  rc.markedSessionWindows,
	}, func(ev agentAskEvent) error {
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
		case agentAskToolStarted:
			if ev.Call != nil {
				recordRequested(ev.Call.CallID)
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
			return nil
		case agentAskToolCall:
			// NOT PERSISTED AS PROSE, and that is not an omission: the
			// durable account of a tool call is the LEDGER's action entry,
			// which the middleware wrote before the call ran and whose id
			// rides this notification. Writing it into the prose block would
			// be a second record of one fact, disagreeing with the first the
			// moment either changed.
			if ev.Call == nil {
				return nil
			}
			recordRequested(ev.Call.CallID)
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
				if rc.control != nil {
					rc.control.recordDroppedDelta()
				}
			}
			// Count only after the durable prose boundary succeeds. A seal
			// failure aborts before this point, so the metric distinguishes
			// accepted tool-call events from requests that merely arrived.
			toolCallsExecuted.Add(1)
			return nil
		case agentAskSteer:
			// THE PERSON'S MESSAGE IS A BOUNDARY, like the tool call above and
			// for the same reason: the prose that was streaming ends here, at
			// the point the person spoke, the message takes the next seat, and
			// the next delta opens the next run (ADR-0040, ADR-0019). A
			// failure aborts the stream: a message the person sees but the
			// ledger never held is the live view lying about what was recorded.
			if prose.EntryID != "" {
				sealed := prose
				prose = content.ProseBlock{}
				if sealErr := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
					return svc.SealProse(ctx, sealed.EntryID)
				}); sealErr != nil {
					return sealErr
				}
			}
			return h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
				_, submitErr := svc.SubmitSteer(ctx, rc.entryID, rc.runID, ev.Text)
				return submitErr
			})
		case agentAskReasoning:
			reasoningChars.Add(int64(len(ev.Text)))
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
				if rc.control != nil {
					rc.control.recordDroppedDelta()
				}
			}
			return nil
		}
		text := ev.Text
		answerChars.Add(int64(len(text)))
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
			if rc.control != nil {
				rc.control.recordDroppedDelta()
			}
		}
		seq++
		return nil
	})
	streamErr = err
	if err != nil {
		reason, sentence := classifyAskFailure(err, rc.model)
		h.log.Warn("agent ask: the run failed",
			"run", rc.runID, "reason", string(reason), "sentence", sentence,
			"error", err, "callPath", log.CallPath(0))
		emitStreamEnded()
		state := content.RunFailed
		if errors.Is(err, agentharness.ErrCancelled) {
			state = content.RunCancelled
		}
		if errors.Is(err, context.Canceled) {
			state = content.RunInterrupted
		}
		h.terminalize(ctx, rc, state, reason, sentence, r)
		return
	}
	emitStreamEnded()
	h.terminalize(ctx, rc, content.RunCompleted, content.TermCompleted, "", r)
}

// runBoundRequester carries the turn's lease ownership across external tool
// admission without replacing the admitted request's context or cancellation.
type runBoundRequester struct {
	assistant.RendererRequester
	control     *agentRunControl
	degradation *assistant.RunLeaseDegradation
}

func (r runBoundRequester) runContext(ctx context.Context) context.Context {
	if r.control != nil {
		ctx = context.WithValue(ctx, agentRunControlContextKey{}, r.control)
	}
	if r.degradation != nil {
		ctx = assistant.WithRunLeaseDegradation(ctx, r.degradation)
	}
	return ctx
}

func (r runBoundRequester) RequestRun(ctx context.Context, sessionID, command string) (json.RawMessage, error) {
	body, err := r.RendererRequester.RequestRun(r.runContext(ctx), sessionID, command)
	if err != nil && r.control != nil {
		r.control.mu.Lock()
		cancelled, cancelDone := r.control.cancelled, r.control.cancelDone
		r.control.mu.Unlock()
		if cancelled && cancelDone != nil {
			// Withdrawal wakes the call before the synchronous stop ladder
			// ends. Do not turn that withdrawal into an actor failure.
			<-cancelDone
			return nil, context.Canceled
		}
	}
	return body, err
}

type runBoundWaitRequester struct {
	runBoundRequester
	waiter assistant.RunWaiter
}

func (r runBoundWaitRequester) RequestRunWait(ctx context.Context, runID string, decision assistant.RunDecision) (json.RawMessage, error) {
	return r.waiter.RequestRunWait(r.runContext(ctx), runID, decision)
}

// askHarness owns the ordered prose stream. The agent places tool boundaries;
// the kernel supplies only the details of that same call.
func (h agentHandlers) askHarness(ctx context.Context, rc askRunContext, p assistant.AskParams, emit func(agentAskEvent) error) error {
	type callDetails struct {
		call assistant.ToolCall
		done chan error
	}
	deliveryCtx, stopDelivery := context.WithCancel(ctx)
	defer stopDelivery()
	details := make(chan callDetails)
	if h.harness == nil {
		return errors.New("the answering agent is not available")
	}
	var authority agentharness.TurnAuthority
	var runTools *assistant.RunTools
	var fatal <-chan error
	if p.Grant != nil {
		tools, err := assistant.BindRunTools(ctx, h.client, p, func(call assistant.ToolCall) error {
			detail := callDetails{call: call, done: make(chan error, 1)}
			select {
			case details <- detail:
			case <-deliveryCtx.Done():
				return deliveryCtx.Err()
			}
			select {
			case err := <-detail.done:
				return err
			case <-deliveryCtx.Done():
				return deliveryCtx.Err()
			}
		})
		if err != nil {
			return err
		}
		tools.UseAgentCallIDs()
		runTools = tools
		authority = tools
		fatal = tools.Fatal()
		// The turn's arrivals, for the answers that settle on it: a decline
		// covers the calls queued by then (ADR-anntnxdj §2).
		if rc.control != nil {
			rc.control.attachTurnCutoff(tools.TurnArrivalCutoff)
		}
	}
	conversation := string(rc.sessionID)
	if rc.paneID != nil {
		conversation = *rc.paneID
	}
	turn, err := h.harness.Run(ctx, agentharness.TurnSpec{
		ConversationID: conversation,
		ModelSelected: func(admissionCtx context.Context, model string) error {
			if h.op == nil {
				return nil
			}
			return h.op.Run(admissionCtx, func(ctx context.Context, svc capability.AgentService) error {
				return svc.PinConversationModel(ctx, conversation, h.harnessID, model)
			})
		},
		Text:         rc.question + assistant.TurnContext(rc.promptFacts),
		Model:        rc.model,
		SystemPrompt: assistant.StandingSystemPrompt(rc.promptFacts.PersonalInstructions),
		Authority:    authority,
	})
	if err != nil {
		return err
	}
	if steerer, ok := turn.(agentharness.Steerer); ok && rc.control != nil {
		rc.control.attachSteerer(steerer)
	}
	// A person's message is written into the turn's record from HERE, on this
	// stream's own goroutine, so it lands in the order the answer's runs do
	// (nocx-a62cl.6.4). agent.steer runs on another goroutine, so it hands the
	// text over and waits for the ledger rather than writing out of order.
	steers := make(chan steerBoundaryWrite)
	// The stream can END while a steer is being delivered — a failed run takes
	// its fatal and leaves — and a boundary write to a stream nobody is reading
	// would hold the handler until the connection died. The write says so
	// instead: the agent already has the message, and the record is what is lost,
	// which the caller logs.
	streamDone := make(chan struct{})
	defer close(streamDone)
	if rc.control != nil {
		rc.control.attachSteerBoundary(func(ctx context.Context, text string) error {
			write := steerBoundaryWrite{text: text, ack: make(chan error, 1)}
			select {
			case steers <- write:
			case <-streamDone:
				return errSteerStreamGone
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case ackErr := <-write.ack:
				return ackErr
			case <-streamDone:
				return errSteerStreamGone
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		defer rc.control.attachSteerBoundary(nil)
	}
	var fatalErr error
	var emissionErr error
	started := make(map[string]struct{})
stream:
	for {
		var event agentharness.Event
		select {
		case write := <-steers:
			write.ack <- emit(agentAskEvent{Kind: agentAskSteer, Text: write.text})
		case detail := <-details:
			var detailErr error
			if _, ok := started[detail.call.CallID]; !ok {
				detailErr = errors.New("tool details have no ordered start")
			} else {
				detailErr = emit(agentAskEvent{Kind: agentAskToolCall, Call: &detail.call})
			}
			detail.done <- detailErr
			if detailErr != nil {
				_ = turn.Cancel(context.WithoutCancel(ctx))
				emissionErr = detailErr
				break stream
			}
			continue
		case fatalErr = <-fatal:
			_ = turn.Cancel(context.WithoutCancel(ctx))
			break stream
		case next, ok := <-turn.Events():
			if !ok {
				break stream
			}
			event = next
		}
		var ev agentAskEvent
		switch event.Kind {
		case agentharness.EventToolStarted:
			if event.Tool == nil || runTools == nil {
				continue
			}
			started[event.Tool.ID] = struct{}{}
			ev = agentAskEvent{Kind: agentAskToolStarted, Call: &assistant.ToolCall{CallID: event.Tool.ID}}
		case agentharness.EventToolFinished:
			if event.Tool != nil {
				delete(started, event.Tool.ID)
			}
			continue
		case agentharness.EventAnswerDelta:
			ev = agentAskEvent{Kind: agentAskAnswer, Text: event.Text}
		case agentharness.EventReasoningDelta:
			ev = agentAskEvent{Kind: agentAskReasoning, Text: event.Text}
		default:
			continue
		}
		if emitErr := emit(ev); emitErr != nil {
			_ = turn.Cancel(context.WithoutCancel(ctx))
			emissionErr = emitErr
			break stream
		}
		if event.Kind == agentharness.EventToolStarted {
			runTools.StartToolCall(event.Tool.ID)
		}
	}
	stopDelivery()
	outcome, err := turn.Wait(context.WithoutCancel(ctx))
	// A cancelled turn hands back exactly the messages its agent had queued and
	// dropped (ADR-4khgzeyp decision 3): they were never used, so the block says
	// they are asked next and the renderer asks them. Read after Wait, which is
	// when the agent has finished dropping them.
	var dropped []string
	if handback, ok := turn.(agentharness.SteerHandback); ok {
		dropped = handback.DroppedSteers()
	}
	if rc.control != nil {
		rc.control.mu.Lock()
		rc.control.answeringModel = outcome.Model
		rc.control.droppedSteers = dropped
		if u := outcome.Usage; u != nil {
			rc.control.usage = &content.RunUsage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens, CacheWrite: u.CacheWriteTokens}
		}
		rc.control.mu.Unlock()
	}
	if emissionErr != nil {
		return emissionErr
	}
	// A terminal event may race the screen notification. Settlement guarantees
	// every accepted call has reported its fatal result before Wait returns.
	if fatalErr == nil {
		select {
		case fatalErr = <-fatal:
		default:
		}
	}
	if fatalErr != nil {
		err = fatalErr
	}
	if rc.control != nil {
		rc.control.mu.Lock()
		// The actor cancels for both Stop and socket loss. Only an accepted
		// Stop is a user cancellation; a lost transport interrupts the run.
		if errors.Is(err, agentharness.ErrCancelled) && !rc.control.cancelled {
			err = context.Canceled
		}
		// The actor owns terminal precedence. A non-cancelled terminal
		// was taken before cancellation, even if Stop raced queued events.
		if !errors.Is(err, agentharness.ErrCancelled) && !errors.Is(err, context.Canceled) {
			rc.control.cancelled = false
			rc.control.actorTerminalState = content.RunCompleted
			if err != nil {
				rc.control.actorTerminalState = content.RunFailed
			}
		}
		rc.control.mu.Unlock()
	}
	return err
}

// handleCancel stops one prepared, streaming or awaiting-approval assistant
// turn. It never signals the session's foreground command: command interruption
// belongs exclusively to session.signal and its existing focus owners. The
// terminal ledger write completes before the result is answered, so no stream
// event can overtake the person's stop decision.
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
	ctx, _ = log.StartTrace(ctx, runTrace(p.RunID))
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
	state := content.RunCancelled
	reason := content.TermUserKilled
	sentence := "the person stopped this answer"
	hasDetails := false
	// Stop only RequestRun executions registered by this assistant turn. The
	// synchronous ladder completes before provider cancellation, durable
	// terminalization and the reserved response; user/summoned commands have
	// no registration and are never signalled here.
	if rc.control.cancelRunLeases(reason) {
		sentence = "no local process group to signal — the execution keeps running"
		hasDetails = true
	}
	rc.control.cancelContext(reason)
	rc.control.finishCancel(state, reason, sentence, hasDetails)
	if rc.control.driveDone != nil {
		<-rc.control.driveDone
	}
	h.terminalize(ctx, rc, state, reason, sentence, h.r)
	rc.control.mu.Lock()
	cancelAccepted := rc.control.cancelled
	actorTerminalState := rc.control.actorTerminalState
	droppedSteers := append([]string(nil), rc.control.droppedSteers...)
	rc.control.mu.Unlock()
	result := agentCancelResponse{
		RunID:         p.RunID,
		State:         string(content.RunCancelled),
		Cancelled:     cancelAccepted,
		DroppedDeltas: rc.control.droppedDeltaCount(),
		DroppedSteers: droppedSteers,
		UnarmedBounds: unarmedRunLeaseSentences(rc),
	}
	if !cancelAccepted {
		result.State = string(actorTerminalState)
	}
	if hasDetails {
		result.Error = sentence
	}
	_ = h.r.TryResult(req.ID, mustMarshal(result))
}

// handleSteer delivers a person's message into the run that is answering it
// (ADR-anntnxdj decision 4). It answers only once the agent took the message:
// a message the turn ended under is refused, and says so, rather than being
// reported delivered into a block that has already closed.
func (h agentHandlers) handleSteer(ctx context.Context, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	var p agentSteerParams
	if msg := decodeParamsStrict(req.Params, &p); msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
		return
	}
	ctx, _ = log.StartTrace(ctx, runTrace(p.RunID))
	// The run registry is the one owner of "which turn is live": a run whose
	// terminal was written is gone from it, and its message has nowhere to go.
	h.pendingRunsMu.Lock()
	rc, ok := h.pendingRuns[p.RunID]
	h.pendingRunsMu.Unlock()
	if !ok || rc.control == nil {
		// THE RUN MAY HAVE CLOSED BETWEEN THE RENDERER DRAWING ITS TURN AND
		// THIS FRAME ARRIVING. A message naming a run of THIS conversation that
		// has terminalized is not an error: the turn was on screen when the
		// person typed, and the message is asked as the conversation's next
		// question (ADR-4khgzeyp decision 3, nocx-a62cl.6.1). Anything else —
		// an id that names nothing, another window's run — is still refused.
		if h.steerTargetIsAClosedRunOfThisConversation(ctx, p.RunID) {
			h.log.Info("agent steer: the run had already ended, so the message is asked next", "run", p.RunID)
			_ = h.r.TryResult(req.ID, mustMarshal(agentSteerResponse{RunID: p.RunID, Outcome: steerNextQuestion}))
			return
		}
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: run is not active (it may already be terminal or never existed)",
		})
		return
	}
	// A message typed while a call awaits approval ANSWERS that question
	// (ADR-anntnxdj decision 4): the call is declined in the person's own
	// words, the calls already queued behind it are refused with the same
	// sentence by the turn's own order, and only then does the message reach
	// the agent. The question's wait sends agent.approvalRetired on its way
	// out, so the prompt leaves the block at once.
	if wait := h.takePendingAnswerWait(p.RunID); wait != nil {
		answered := wait.settleLocked(assistant.Declined{Reason: assistant.PersonMessageResult(wait.tool, p.Text)})
		wait.mu.Unlock()
		if !answered {
			h.log.Warn("agent steer: the open question was settled by somebody else", "run", p.RunID, "tool", wait.tool)
		}
	}
	// WHAT THE CONNECTOR DOES WITH A MESSAGE IS ITS OWN STATEMENT, not something
	// this handler finds out by trying (ADR-anntnxdj decision 4). An agent that
	// cannot steer still gets the person's answer to the question above; the
	// message itself is kept and asked as the conversation's next question.
	if h.harness != nil && h.harness.Facts().Steering == agentharness.QueuesNextQuestion {
		h.log.Info("agent steer: the agent queues the message as the next question", "run", p.RunID)
		_ = h.r.TryResult(req.ID, mustMarshal(agentSteerResponse{RunID: p.RunID, Outcome: steerNextQuestion}))
		return
	}
	steerer := rc.control.attachedSteerer()
	if steerer != nil {
		if err := steerer.Steer(ctx, p.Text); err == nil {
			h.log.Info("agent steer: the agent took the person's message", "run", p.RunID)
			// AND THE TURN'S RECORD TAKES IT (nocx-a62cl.6.4): the ledger is
			// the record of what is shown (ADR-0019), so the message is written
			// into the turn's order here, where the person sent it. The agent
			// already has it, so a record that cannot be written is logged
			// rather than reported as a failed steer: the message was said.
			if boundary := rc.control.attachedSteerBoundary(); boundary != nil {
				if recordErr := boundary(ctx, p.Text); recordErr != nil {
					h.log.Warn("agent steer: the message could not be written into the turn's record",
						"run", p.RunID, "error", recordErr)
				}
			} else {
				h.log.Warn("agent steer: the turn has no stream to write the message into", "run", p.RunID)
			}
			_ = h.r.TryResult(req.ID, mustMarshal(agentSteerResponse{RunID: p.RunID, Outcome: steerSteered}))
			return
		} else if !isSteerNotTaken(err) {
			h.log.Warn("agent steer: the message was not delivered", "run", p.RunID, "reason", err)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: fmt.Sprint(err)})
			return
		}
	}
	// The turn did not take it — it is not reaching the agent yet, it ended
	// first, it was cancelled, or the agent did not accept it within the turn's
	// bound. The message is not lost: it becomes the next question
	// (ADR-4khgzeyp decision 3).
	h.log.Info("agent steer: the turn did not take the message, so it is asked next", "run", p.RunID)
	_ = h.r.TryResult(req.ID, mustMarshal(agentSteerResponse{RunID: p.RunID, Outcome: steerNextQuestion}))
}

// errSteerStreamGone is the boundary write's answer when the run's stream has
// already ended: the agent has the message, and the turn's record cannot take it
// — a lost record, never a lost message.
var errSteerStreamGone = errors.New("the turn's stream has ended")

// steerBoundaryWrite is one person's message handed to the stream, and the
// answer: the ledger holds it, or it does not.
type steerBoundaryWrite struct {
	text string
	ack  chan error
}

// steerTargetIsAClosedRunOfThisConversation reports whether runID names an agent
// run that has terminalized and belongs to a session this connection holds: the
// one case where a message that arrived after the turn is still this
// conversation's, and is asked next rather than refused (nocx-a62cl.6.1).
func (h agentHandlers) steerTargetIsAClosedRunOfThisConversation(ctx context.Context, runID int64) bool {
	if h.op == nil || h.state == nil {
		return false
	}
	var target content.SteerTarget
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.AgentService) error {
		found, targetErr := svc.SteerTarget(ctx, runID)
		target = found
		return targetErr
	})
	if err != nil || !target.Terminal || target.SessionID == "" {
		return false
	}
	return h.state.has(session.ID(target.SessionID))
}

// isSteerNotTaken reports whether a turn's refusal of a person's message means
// "ask it next" rather than a failure the person must read as an error: no turn
// left to land in, an agent whose protocol has no steering, or a turn that
// ended before the agent accepted it.
func isSteerNotTaken(err error) bool {
	return errors.Is(err, agentharness.ErrSteerNotTaken) ||
		errors.Is(err, agentharness.ErrTurnNotRunning) ||
		errors.Is(err, agentharness.ErrNotSteerable)
}

// handleApprove answers agent.approve — the person's decision on one exact
// proposal (design §7.2, acceptance criteria 2, 7, 8). The answer WAKES the
// very call that asked (ADR-anntnxdj §3): a yes lets it go on and run as the
// proposal's subsequent attempt, and a no (nocx-uvac6.1) is that call's
// result — except on the egress gate, where the pipeline ends the run because
// a withheld result has nothing to continue with. A stale or unknown approval
// id is answered honestly and wakes nothing.
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
	// THE CLAIM (nocx-a62cl.3.1). A call blocked in the approval service is
	// waiting for exactly one answer, so the handler that claims the wait is
	// the only one that may settle the store: the wait is taken BEFORE any
	// write, and every path that refuses the answer below settles it as
	// unanswered rather than leaving the call waiting for a decision the
	// server did not record. A nil wait names no question this run is
	// waiting on — a stale binding, or an answer to a question already
	// answered — and is refused.
	wait := h.takeAnswerWait(runID, ap)
	if wait == nil {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32602,
			Message: "Invalid params: unknown approval — no call is waiting on this binding",
		})
		return
	}
	defer wait.mu.Unlock()
	waitSettled := false
	waitRestored := false
	defer func() {
		if waitSettled || waitRestored {
			return
		}
		wait.settleLocked(assistant.Unanswered{Err: errors.New("the server could not record the answer, so the call was refused")})
	}()
	if !p.Approved {
		// "No, and widen it" is not an answer anybody can mean: the widening
		// answer IS an approval. Refused rather than silently read as a
		// plain decline, which would apply a decision the person did not
		// give — an answer to a question they were not asked.
		if p.Scope == approveScopeExpand {
			_ = h.r.TryError(req.ID, RPCError{
				Code:    -32602,
				Message: "Invalid params: a widening is an approval — a decline is answered with once, session or always",
			})
			return
		}
		// The person declined (nocx-uvac6.1): the run is NOT over — the
		// refusal becomes this call's result and the model answers it in
		// words ("a refusal is an answer", systemprompt.go). The decline
		// is settled against the exact proposal before any standing policy
		// write, so a responder that loses the race cannot mutate policy or
		// resume a run it no longer owns.
		//
		// An EGRESS decline is the one no that ENDS the run, and the call
		// that asked is what ends it: the pipeline's ScreenDeclinedError
		// reaches the stream, which closes the run as agent-declined. The
		// answer below settles the wait like any other no; the assistant is
		// what knows a withheld result has nothing to continue with.
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
		// A no to a WIDENING question refuses that (effect, resource) for the
		// rest of this run's life — not longer: the record is keyed by the
		// run, so the next run asks again (design §5.3, both ends). Recorded
		// after the decline settled, for the same reason the standing part is.
		h.declineWidening(p, ap)
		// The standing part is recorded only after the decline settled,
		// so a loser can never write a row for a question it did not win.
		outcome := h.applyStandingAnswer(p, ap, rc.sessionID)
		warning := outcome.warning
		if warning != "" && p.Scope != approveScopeOnce {
			// The standing write did not stick. The refusal is still
			// this call's result, but it must not claim permanence the
			// policy will not honour.
			h.approvals.DowngradeDeclined(ap)
		}
		// A standing NO is a standing answer: "never ask me to run this
		// again" configured something exactly as much as "always" did, and
		// gets its receipt in the same words for the same reason. When the
		// receipt cannot be routed, its sentence joins the response — and
		// never the downgrade above, which is for a save that did NOT stick
		// and is settled before this line by construction (`saved` and
		// `warning` are the two faces of one outcome and never both set).
		if msg := h.notifyStandingAnswer(ap, p, outcome, rc); msg != "" {
			warning = msg
		}
		// The call that asked is waiting: the no reaches IT, in the person's
		// own words, and the model goes on in the same stream — except on the
		// egress arm, where the pipeline knows a withheld result has nothing
		// to continue with and ends the run.
		waitSettled = wait.settleLocked(assistant.Declined{
			Reason: assistant.PersonDeclineResult(p.Tool, declineKindForScope(p.Scope)),
		})
		_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunStreaming), Warning: warning}))
		return
	}

	// The offer is what makes the widening answer legitimate, and it is
	// checked BEFORE the proposal is settled: an answer to a question that
	// never offered it must leave the question exactly as it was.
	if p.Scope == approveScopeExpand {
		if msg := h.wideningRefusal(ap); msg != "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: msg})
			return
		}
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
	var warning string
	if p.Scope == approveScopeExpand {
		if msg := h.applyWidening(ap); msg != "" {
			// NEITHER half applied (design §5.3): the widening and the
			// approval are one answer, so a store that refused the write
			// must not leave a run resumed on an approval whose standing
			// half vanished. The approval is revoked, which puts the
			// question back to pending — the person answers it again,
			// differently if they like — and this is therefore the one
			// answer that is REFUSED rather than warned about.
			h.approvals.Revoke(ap)
			// The question is still OPEN: put the wait back where the call
			// is blocking on it, so the person answers again rather than
			// being refused for the store's failure. The call has not been
			// woken with anything, and no second one can claim this wait.
			h.restoreAnswerWait(runID, wait, rc.pendingReason)
			waitRestored = true
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: msg})
			return
		}
	} else {
		outcome := h.applyStandingAnswer(p, ap, rc.sessionID)
		warning = outcome.warning
		// THE RECEIPT (nocx-2019q). A standing answer that disappeared into
		// the store with nothing on screen is a thing a person configured
		// and cannot see: they learn about it later, by being un-asked a
		// question they have forgotten answering. It is sent only when the
		// write actually happened, so a receipt can never claim a rule the
		// store refused — that failure travels in `warning`, on the
		// response, and is the surface's to show — as is the one degrade
		// this notification has of its own, a receipt with no turn to land
		// on.
		if msg := h.notifyStandingAnswer(ap, p, outcome, rc); msg != "" {
			warning = msg
		}
	}
	// The call that asked is waiting: the yes reaches IT, and the guards that
	// follow read the answer and the evidence as they stand now.
	waitSettled = wait.settleLocked(assistant.Approved{})
	_ = h.r.TryResult(req.ID, mustMarshal(agentApproveResponse{State: string(content.RunStreaming), Warning: warning}))
}

// wideningRefusal answers "may this proposal be widened at all", in the words
// the person's surface would use. Empty means yes.
//
// The pending record is the source of truth, not the answer: a renderer that
// sent scope "expand" for a question that never offered it is answering a
// question nobody asked, and the row must not grow on the strength of it.
func (h agentHandlers) wideningRefusal(ap assistant.Approval) string {
	fact, ok := h.approvals.OutOfScopeFor(ap)
	if !ok {
		return "Invalid params: this question offered no widening — nothing about this call fell outside a scope"
	}
	if fact.Cause != content.OutOfScopeRowScope {
		return "Invalid params: this question offered no widening — the bound it fell outside is the run's own fence, which no answer can move"
	}
	if _, effectKnown := h.approvals.EffectFor(ap); !effectKnown {
		return "Invalid params: this question named no effect class, so there is no row to widen"
	}
	if h.globalPolicy == nil {
		return "Invalid params: there is no policy store to widen a row in"
	}
	return ""
}

// applyWidening is the widening half of the answer: ONE store write that adds
// the resource that fell outside to the row it fell outside of. It returns the
// sentence the answer is refused with, or empty on success.
//
// The row is the GLOBAL matrix row, and deliberately so: a session overlay
// carries decisions and invocation rules and has no scopes to grow, so a
// "widen for this session only" would be an answer the storage cannot hold.
// §5.3 says the answer widens the row's scopes, and the row is one document
// with one owner — the same one the settings page writes through.
func (h agentHandlers) applyWidening(ap assistant.Approval) string {
	fact, ok := h.approvals.OutOfScopeFor(ap)
	if !ok {
		return "the widening could not be applied: the question no longer names what fell outside"
	}
	effect, ok := h.approvals.EffectFor(ap)
	if !ok {
		return "the widening could not be applied: the question named no effect class"
	}
	next, err := assistant.WidenRowScope(h.globalPolicy.Policy(), effect, fact.Resource)
	if err == nil {
		err = h.globalPolicy.SetPolicy(next)
	}
	if err != nil {
		h.log.Warn("agent.approve: the row could not be widened, so nothing was approved",
			"run", ap.RunID, "tool", ap.Tool, "effect", string(effect), "error", err)
		return "the row could not be widened, so this call was not approved either: " + err.Error()
	}
	return ""
}

// declineWidening records a no to a widening question against the RUN. A
// question that offered no widening records nothing — a plain decline is
// already carried by the approval store's DeclineKind.
func (h agentHandlers) declineWidening(p approveParams, ap assistant.Approval) {
	fact, ok := h.approvals.OutOfScopeFor(ap)
	if !ok || fact.Cause != content.OutOfScopeRowScope {
		return
	}
	effect, ok := h.approvals.EffectFor(ap)
	if !ok {
		return
	}
	h.approvals.DeclineWidening(p.RunID, effect, fact.Resource)
}

// applyStandingAnswer records the part of a decision that outlives the
// proposal it was given on: command proposals save an invocation rule, while
// non-command proposals save the classified effect row. "in this session"
// writes to the run's session overlay, "always" writes to the global policy,
// and "once" writes nothing anywhere.
func (h agentHandlers) applyStandingAnswer(p approveParams, ap assistant.Approval, sid session.ID) standingAnswerOutcome {
	if p.Scope == approveScopeOnce {
		return standingAnswerOutcome{}
	}
	invocation, _, commandInvocation := h.approvals.InvocationFor(ap)
	d := content.DecisionRefuse
	if p.Approved {
		d = content.DecisionPermit
	}
	if !commandInvocation {
		effect, ok := h.approvals.EffectFor(ap)
		if !ok {
			return refusedStandingAnswer("the question named no effect class")
		}
		if p.Scope == approveScopeSession {
			h.sessionPolicy.Set(sid, effect, d)
			return standingAnswerOutcome{saved: true}
		}
		if h.globalPolicy == nil {
			return standingAnswerOutcome{warning: noPolicyStoreWarning}
		}
		// Through the store's OWN locked seam, not a read, an edit on the
		// copy and a whole-document write: the settings page and a second
		// prompt write the same document, and whichever wrote last used to
		// win with everything the other had said in it.
		if err := h.globalPolicy.SetRowDecision(effect, d); err != nil {
			return refusedStandingAnswer(err.Error())
		}
		return standingAnswerOutcome{saved: true}
	}
	rule, standingReason := content.StandingRule(invocation)
	if standingReason != "" {
		h.log.Warn("agent.approve: the standing answer was not recorded",
			"run", p.RunID, "tool", p.Tool, "scope", p.Scope, "reason", standingReason)
		return refusedStandingAnswer(standingReason)
	}
	rule.Decision = d
	if p.Scope == approveScopeSession {
		h.sessionPolicy.SetRule(sid, rule)
		return standingAnswerOutcome{saved: true, rule: rule.Label()}
	}
	if h.globalPolicy == nil {
		return standingAnswerOutcome{warning: noPolicyStoreWarning}
	}
	// The same locked seam, and the same reason — plus the one this path
	// needs on top of it: the STORED rule comes back, so the id the
	// receipt's Undo names is the id the document actually wears rather
	// than one this side minted and hoped for (AD-7).
	stored, err := h.globalPolicy.SetRule(rule)
	if err != nil {
		return refusedStandingAnswer(err.Error())
	}
	return standingAnswerOutcome{saved: true, ruleID: stored.ID, rule: stored.Label()}
}

// standingAnswerOutcome is what became of the part of an answer that outlives
// the proposal: whether it was written, what it covers, and the sentence the
// person is owed when it was not.
//
// It is one value rather than three returns because the three are one fact
// with three faces, and a caller that could see the warning without the save
// — or the id without either — is a caller that can draw a receipt for a rule
// that is not in the store.
type standingAnswerOutcome struct {
	// warning is the sentence to show when the standing part could not be
	// recorded. The decision itself always stood; a store problem is not
	// the person's to pay for.
	warning string
	// saved says a standing answer was WRITTEN — the whole of what a
	// receipt may be drawn on.
	saved bool
	// ruleID is the stored invocation rule's id, and therefore what an
	// Undo can name exactly. Empty for the answers no id addresses: a
	// session overlay, which dies with its session, and a matrix row,
	// which is edited rather than removed.
	ruleID string
	// rule is the canonical invocation the answer covers, in the spelling
	// the question offered it in. Empty for a non-command answer, whose
	// coverage is the effect row.
	rule string
}

// noPolicyStoreWarning is the one sentence for "there is nowhere to save
// this", written once because two spellings of it would drift.
const noPolicyStoreWarning = "the decision was applied to this call, but there is no policy store to save it as a standing answer in"

// refusedStandingAnswer wraps the reason one save failed in the sentence a
// person reads. One owner of the words: every failure path here says the same
// thing about what DID happen — the decision stood — and differs only in why
// the standing half did not.
func refusedStandingAnswer(reason string) standingAnswerOutcome {
	return standingAnswerOutcome{
		warning: "the decision was applied to this call, but could not be saved as a standing answer: " + reason,
	}
}

// notifyStandingAnswer tells the turn that asked that a standing answer is now
// in the store (nocx-2019q). It carries the same facts the QUESTION carried —
// the direction, the width and the binding — and no sentence: the words a
// receipt shows are the words of the button that was clicked, and that surface
// is their one owner (AD-8).
//
// Nothing is sent when nothing was written, which is what makes a receipt on
// screen true: "once" saves nothing, an egress question is refused a width
// before it reaches here, and a refused write leaves `saved` false and its
// sentence on the response instead.
//
// It ANSWERS with a sentence rather than sending in the one case where the
// write stuck and the receipt cannot be routed — no turn entry — because a
// receipt the renderer must drop is a thing the person configured and cannot
// see, which is the soft degrade AGENTS.md forbids. The empty string means
// there is nothing to report.
//
// IT IS ROUTED BY THE TURN, LIKE EVERY OTHER NOTIFICATION ABOUT A RUN, and
// that is why `rc` is a parameter (nocx-2019q). The entry it carries used to
// be the approvals store's — `EntryIDFor`, the PROPOSAL's ledger row, the one
// the approved call runs as a subsequent attempt of. That is the entry this
// file already sends beside a tool call as `actionEntryId`, and it is not a
// routing key: the renderer compares `entryId` with the turn block's own and
// drops anything else, exactly as it drops a stray delta (agent-ask.ts). So a
// receipt addressed by the proposal was silently discarded for every run that
// had a ledger — which is every run — and nobody ever saw one. Two entries,
// two jobs (see agentRunToolCall above); this one wants the turn's.
func (h agentHandlers) notifyStandingAnswer(ap assistant.Approval, p approveParams, outcome standingAnswerOutcome, rc askRunContext) string {
	if !outcome.saved {
		return ""
	}
	// No turn to land on: the receipt is REFUSED rather than sent, and the
	// person is told on the response instead. Sending it would be the soft
	// degrade AGENTS.md forbids — the renderer would drop it, the store
	// would still hold the rule, and the only visible trace of an answer
	// somebody gave would be their not being asked again months later. The
	// trace above says nothing produces this, so it is logged at ERROR: it
	// is a broken invariant, not a condition.
	if rc.entryID == "" {
		h.log.Error("agent.approve: the standing answer was saved but its receipt has no turn to land on",
			"run", p.RunID, "tool", p.Tool, "scope", p.Scope)
		return receiptWithoutTurnWarning
	}
	effect, _ := h.approvals.EffectFor(ap)
	_ = h.r.TryNotify("agent.standingAnswerSaved", mustMarshal(agentStandingAnswerSaved{
		RunID:    p.RunID,
		EntryID:  rc.entryID,
		Approved: p.Approved,
		Scope:    p.Scope,
		Rule:     outcome.rule,
		Effect:   string(effect),
		RuleID:   outcome.ruleID,
	}))
	return ""
}

// receiptWithoutTurnWarning is what a person reads when the answer stuck and
// the receipt could not be routed. It says the true thing about what
// happened — the answer IS saved — and names the surface that can show it,
// because the turn no longer can.
const receiptWithoutTurnWarning = "the answer was saved, but this turn could not be told about it — you can see and undo it in Manage permissions"

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
	case approveScopeOnce, approveScopeSession, approveScopeAlways, approveScopeExpand:
	default:
		// No default: an answer with no scope is not "once". A default
		// here would be a standing decision nobody expressed, and the
		// schema requires the field for the same reason.
		return "scope is required and must be one of once, session, always, expand"
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

func validateAgentSteerRaw(raw json.RawMessage) string {
	var p agentSteerParams
	// Strict: the message and the run it belongs to, and nothing else. A
	// typo'd field would otherwise be an empty message delivered to a turn.
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if p.RunID <= 0 {
		return "runId must be the backend-minted run id"
	}
	if strings.TrimSpace(p.Text) == "" || utf8.RuneCountInString(p.Text) > maxQuestionRunes {
		return "text is required and bounded"
	}
	return ""
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
// The run control's drop count is read only after beginTerminal excludes
// racing callbacks. The notification carries that authoritative whole-run
// count so the renderer marks the gap without changing the earned state.
func (h agentHandlers) terminalize(ctx context.Context, rc askRunContext, state content.RunState, reason content.TerminationReason, sentence string, r Responder) {
	wireError := state != content.RunCancelled
	cancelled := false
	dropped := 0
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
		dropped = rc.control.droppedDeltaCount()
	}
	if cancelled {
		if rc.control.cancelDone != nil {
			<-rc.control.cancelDone
		}
		state, reason, sentence, wireError = rc.control.cancelOutcome()
	}
	// The run is closing: nothing may wake a call it accepted. Drop the
	// stored stream context so a late agent.approve finds no open question.
	// Ask drops its own per-run state on every outcome IT returns from; this
	// is the one funnel for the outcomes it does not — the run was closed
	// while its question was still open.
	h.pendingRunsMu.Lock()
	delete(h.pendingRuns, rc.runID)
	h.pendingRunsMu.Unlock()
	if h.client != nil {
		h.client.Discard(strconv.FormatInt(rc.runID, 10))
	}
	model := rc.model
	var usage *content.RunUsage
	var droppedSteers []string
	if rc.control != nil {
		rc.control.mu.Lock()
		usage = rc.control.usage
		droppedSteers = append([]string(nil), rc.control.droppedSteers...)
		if rc.control.answeringModel != "" {
			model = rc.control.answeringModel
		}
		rc.control.mu.Unlock()
	}
	tctx := context.WithoutCancel(ctx)
	err := h.op.Run(tctx, func(ctx context.Context, svc capability.AgentService) error {
		return svc.FinishAgentRun(ctx, rc.runID, content.FinishAgentRun{
			Model:             model,
			Usage:             usage,
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
	unarmedBounds := unarmedRunLeaseSentences(rc)
	notices := h.runNotices(rc.runID)
	if model == "" && h.harness != nil {
		notices = append(notices, "the agent did not report which model answered; this conversation cannot promise to keep the same model")
	}
	notification := agentRunState{
		Usage:         usage,
		RunID:         rc.runID,
		State:         string(state),
		DroppedDeltas: dropped,
		DroppedSteers: droppedSteers,
		UnarmedBounds: unarmedBounds,
		Notices:       notices,
	}
	if wireError {
		notification.Error = sentence
	}
	_ = r.TryNotify("agent.runState", mustMarshal(notification))
}

// runNotices is what this run must say about itself. Read at the terminal
// rather than emitted when the bound was reached, because that is the moment
// the person turns back to the answer — and because the fact is about the
// whole run, not about the call that happened to be refused when it latched.
func (h agentHandlers) runNotices(runID int64) []string {
	if h.approvals == nil {
		return nil
	}
	return h.approvals.RunNotices(strconv.FormatInt(runID, 10))
}

func unarmedRunLeaseSentences(rc askRunContext) []string {
	if rc.leaseDegradation == nil {
		return nil
	}
	bounds := rc.leaseDegradation.Bounds()
	if len(bounds) == 0 {
		return nil
	}
	sentences := make([]string, 0, len(bounds))
	for _, bound := range bounds {
		sentences = append(sentences, assistant.RunLeaseUnavailableSentence(bound))
	}
	return sentences
}

// classifyAskFailure maps harness and tool-authority failures to a durable
// termination reason and a sentence safe to display on the wire.
func classifyAskFailure(err error, _ string) (content.TerminationReason, string) {
	var agentFailure *agentharness.TurnError
	if errors.As(err, &agentFailure) {
		return content.TermFailed, agentFailure.Reason
	}
	if errors.Is(err, agentharness.ErrCancelled) {
		return content.TermUserKilled, "the person stopped this answer"
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
		return leaseErr.Reason, assistant.RunLeaseSentence(leaseErr.Reason, leaseErr.SubmissionExpired)
	}
	// The broker reports a renderer death directly when its disconnect
	// lifecycle wins the race with the ask context's cancellation. It is the
	// same transport-loss fact as context.Canceled, and must not fall through
	// to the generic failed sentence.
	if errors.Is(err, ErrRequestDisconnected) || errors.Is(err, ErrRequestUndelivered) {
		return content.TermTransportGone, "the connection was lost while the answer was streaming"
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
	// A person's no to a SCREEN's question about a result: the bytes exist
	// and will not leave, so there is nothing to continue the call with and
	// the run ends (ADR-anntnxdj §3). It is not a failure of the agent — it
	// is the person's decision — and the run says so as agent-declined.
	var declined *assistant.ScreenDeclinedError
	if errors.As(err, &declined) {
		return content.TermAgentDeclined, "the person declined to send the result to the model"
	}
	// Cause 1c: the egress gate could not inspect a tool result, so the
	// policy withheld it and failed the run closed. This arm follows the
	// tool-failure arm because both happen after execution, but it must name
	// the screening failure rather than expose eino's wrapper. The sealed
	// vault case is checked by its sentinel, not by error text, so only that
	// repair sends the person to unlock the vault.
	//
	// The credential path stopped needing this arm when nocx-k41yv gave
	// resolution a stance: an Operation read raises the unlock and WAITS, so
	// a sealed vault never reaches this classifier from there. The egress
	// gate is not on that seam yet (nocx-1mf3v.2 puts it there), and even
	// once it is, a headless backend with no renderer to ask still answers
	// ErrVaultSealed — so this arm keeps its sentinel rather than assuming
	// the prompt is always reachable.
	var screeningErr *assistant.EgressScreeningError
	if errors.As(err, &screeningErr) {
		if errors.Is(err, vault.ErrVaultSealed) {
			return content.TermFailed, "the vault is sealed. Unlock it in Settings → Vault, then ask again."
		}
		return content.TermFailed, "nocx could not check the tool result for secrets, so it was not sent to the agent. Unlock the vault and ask again."
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
	// A cancellation this process CAUSED says which one it was. Checked
	// before the context.Canceled arm below, and the arm is what that one
	// could never be: context.Canceled cannot tell "the socket went away"
	// from "we killed the program on purpose", and it answered the first
	// for both — a run ended by an approval resume told the person their
	// connection had dropped (nocx-d6gn4.8.1).
	if sentence, ok := assistant.ProgramEndedSentence(err); ok {
		return content.TermFailed, sentence
	}
	switch {
	case errors.Is(err, context.Canceled):
		return content.TermTransportGone, "the connection was lost while the answer was streaming"
	case errors.Is(err, context.DeadlineExceeded):
		return content.TermTimeout, "the model did not answer in time"
	}
	return content.TermFailed, "the agent could not finish this answer. Check the agent in Settings, then ask again."
}

// answerError maps ask transaction failures to JSON-RPC errors. A conflict
// in an id supplied by the renderer is invalid params; a gate refusal keeps
// answerOperationRefusal's shape; anything else is an
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
		if (grant.Start == nil) != (grant.Count == nil) {
			return empty(fmt.Sprintf("attachedContent[%d] requires both start and count", i))
		}
		if grant.Start != nil {
			if *grant.Start < 0 || *grant.Count <= 0 || *grant.Count > maxAttachedWindowLines ||
				*grant.Start > int(^uint(0)>>1)-*grant.Count {
				return empty(fmt.Sprintf("attachedContent[%d] start/count is out of bounds", i))
			}
		}
		attached = append(attached, assistant.AttachedContentItem{
			ItemID: grant.ItemID, Command: grant.Command, State: grant.State,
			Start: grant.Start, Count: grant.Count, Automatic: grant.Automatic,
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

// ── registration ──────────────────────────────────────────────────────────

// agentSpecs declares the agent.* control methods on the CONTENT operation
// queue (the ask transaction is the ledger — ADR-0019's one writer — so it
// shares the content domain's gate and queue).
func (s *WSServer) agentSpecs(contentSub control.Submission, contentGate control.Admission, configOp capability.ConfigOperation, noteOp capability.NoteOperation, snippetOp capability.SnippetOperation, agentTools agenttools.Registry, client assistant.Client, askSub control.Submission, files filesRoute) []methodSpec {
	var agentOp capability.AgentOperation
	if s.contentDB != nil {
		agentOp = capability.NewAgentOperation(contentGate, s.contentDB)
	}
	var attemptLedger assistant.AttemptLedger
	if s.contentDB != nil {
		attemptLedger = s.contentDB.Ledger()
	}
	// The assistant's route to the pane's files is the same two operations —
	// or neither, when no provider factory is wired. See
	// filesOperationsForAgent: the honest refusal, not a nil dereference.
	filesOpen, filesBind := filesOperationsForAgent(files)
	// buildFor is every dependency an agent handler has EXCEPT the two that
	// only a connection can supply. It is split out because the run-stopping
	// path has a caller that is not on the agent lane: policy.setRule and
	// policy.forgetRule stop the runs a revoked answer left behind
	// (stopRunsForRevokedAnswer), and they must terminalize through the same
	// handler agent.cancel does. A second, partially-filled agentHandlers
	// built beside this one would degrade silently the day terminalize learns
	// to read another field.
	buildFor := func(r Responder) agentHandlers {
		return agentHandlers{
			op: agentOp, configOp: configOp,
			noteOp: noteOp, snippetOp: snippetOp, agentTools: agentTools,
			client: client, askSub: askSub,
			harness: s.agentHarness, harnessID: s.agentHarnessID,
			fetcher: s.agentFetcher, attemptLedger: attemptLedger, grantFor: s.runGrantFor,
			requester: s, screenCapture: s, sessions: s, expansions: s, scripts: s, knownMaterial: s.agentKnownMaterial,
			filesOpen: filesOpen, filesBind: filesBind,
			workerStore: s.workerStore, paneAccessBinder: s.paneAccessBinder,
			approvals: s.agentApprovals, pendingRuns: s.pendingRuns,
			pendingRunsMu:        &s.pendingRunsMu,
			personalInstructions: s.personalInstructionsText,
			sessionPolicy:        s.sessionPolicy, globalPolicy: s.agentPolicy,
			log: s.log, r: r,
		}
	}
	// The registration is the composition root's own hand-off point for the
	// revocation path: this is where the agent handler's dependencies are
	// assembled, so it is where the policy handlers are given the ONE way to
	// terminalize a run. Reading pendingRuns from ws_policy.go and closing a
	// run there would be a second terminal path, which is a second set of
	// half-terminal states.
	s.agentTerminalizer = buildFor
	build := func(w *wsConn, state *connState, r Responder) agentHandlers {
		// clientID is the CONNECTION identity, deliberately: it binds ask
		// idempotency to the connection (a reconnect mints a new one), never
		// to a renderer-minted tab.
		h := buildFor(r)
		h.state = state
		h.clientID = connectionID(w)
		return h
	}
	return []methodSpec{
		reg(contentSub, catalog.OpAgentStatus, params(validateHarnessStatusRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return h.handleHarnessStatus
		}),
		reg(contentSub, catalog.OpAgentAsk, params(validateAgentAskRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAsk(ctx, req) }
		}),
		reg(contentSub, catalog.OpAgentCancel, params(validateAgentCancelRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCancel(ctx, req) }
		}),
		reg(contentSub, catalog.OpAgentSteer, params(validateAgentSteerRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSteer(ctx, req) }
		}),
		reg(contentSub, catalog.OpAgentApprove, params(validateAgentApproveRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleApprove(ctx, req) }
		}),
	}
}

// ── the runs a revoked answer left behind (nocx-r4fh8, nocx-4yjwk.8) ──────

// RunsUnreachedByRuleWrite reports the LIVE runs whose grant would decide
// differently once one rule write lands — the runs a change or a forget does
// not reach.
//
// A run's grant is minted when the run starts and is immutable for the run
// (ADR-0020 decision 5), so a policy write never reaches one. This asks each
// live run's OWN frozen policy the question, through
// content.ChangedByRuleWrite; the reading of "using it" that produces, and the
// reading it rejects, are written down at the count in ws_policy.go.
//
// It is a method on the server because the server owns the run registry. It
// takes the registry's lock for the length of the walk and evaluates under it:
// the walk is seven decisions over at most two probe invocations per run, all
// of it pure comparison over frozen values, and holding the lock is what makes
// the answer a snapshot rather than a set that never coexisted.
func (s *WSServer) RunsUnreachedByRuleWrite(w content.RuleWrite) []int64 {
	s.pendingRunsMu.Lock()
	defer s.pendingRunsMu.Unlock()
	var out []int64
	for id, rc := range s.pendingRuns {
		if rc.grant == nil {
			// The run carries no grant, so it executes no tools and there
			// is no decision for a rule to change. Not affected, and it is
			// the honest answer rather than a conservative one: telling a
			// person a run is affected when nothing it can do is governed
			// is the imprecise reading this design rejected.
			continue
		}
		if rc.grant.Policy.ChangedByRuleWrite(w) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RunsUnreachedByRowWrite reports the LIVE runs whose authority would decide
// differently once ONE matrix write lands — the runs a moved effect row does
// not reach (nocx-4yjwk.8).
//
// It is RunsUnreachedByRuleWrite's twin and shares everything it can: the same
// registry, the same "no grant, nothing to decide" answer, the same ascending
// ids, the same snapshot discipline. What it cannot share is the question put
// to each run, because a row write has no selector to probe — see
// content/rowwrite.go, which is where that derivation is written down.
//
// It answers with the ROWS as well as the runs, and they come out of the same
// walk on purpose: the sentence a stopped run records must name the change the
// count was taken over. Those are not always the rows the DOCUMENT moved — the
// approval prompt writes a row too (GlobalPolicyStore.SetRowDecision), so a
// person can save a matrix that moves nothing in the document while a run
// started before that prompt is still deciding under the old row. The rows a
// person is told about are therefore the rows that moved for the runs, unioned
// over them and put back into the lattice's order.
//
// The one thing it does that the rule twin does not is MINT AGAIN. A run's
// rows have had the session overlay resolved into them and the run fence
// folded into them, so the only comparable "after" is that same run's
// authority minted from the document the write leaves behind. runGrantFrom is
// the mint both this and the real run cross.
//
// The registry is snapshotted under its lock and the minting happens after it
// is released, which is the one place this differs from the rule twin's
// evaluate-under-the-lock. The mint reads the session policy store and the
// settings store; taking their locks under pendingRunsMu would be a lock order
// this file is the only place to state, and the snapshot is still what makes
// the set of runs one that coexisted.
func (s *WSServer) RunsUnreachedByRowWrite(after content.EffectPolicy) ([]int64, []content.Effect) {
	type held struct {
		id      int64
		session string
		policy  content.EffectPolicy
	}
	var runs []held
	s.pendingRunsMu.Lock()
	for id, rc := range s.pendingRuns {
		if rc.grant == nil {
			// No grant, so no tool and no decision for a row to move. The
			// honest answer rather than the conservative one, for the
			// reason the rule twin gives.
			continue
		}
		runs = append(runs, held{id: id, session: string(rc.sessionID), policy: rc.grant.Policy})
	}
	s.pendingRunsMu.Unlock()

	var out []int64
	movedSet := map[content.Effect]bool{}
	for _, run := range runs {
		next := s.runGrantFrom(after, run.session)
		if next == nil {
			// The server has no policy store, so nothing here was minted
			// from one and no write can move it.
			continue
		}
		moved := run.policy.RowsMovedByRowWrite(next.Policy)
		if len(moved) == 0 {
			continue
		}
		out = append(out, run.id)
		for _, e := range moved {
			movedSet[e] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	var movedRows []content.Effect
	for _, e := range content.LatticeEffects() {
		if movedSet[e] {
			movedRows = append(movedRows, e)
		}
	}
	return out, movedRows
}

// StopRunsForRevokedAnswer terminalizes the named runs because a person took
// back an answer they were running under, and reports which it actually
// stopped and which had already finished.
//
// It goes through handleCancel's path and adds nothing to it: the run is taken
// out of the registry under pendingRunsMu, beginCancel claims it, the run
// leases and the context are cancelled, finishCancel records the outcome and
// terminalize writes the terminal close. A second way to end a run would be a
// second set of half-terminal states, so there is no second way.
//
// THE REASON IS TermAnswerRevoked, AND IT IS NOT TermUserKilled (nocx-4yjwk.7).
// The two are different facts: user-killed is a decision about THIS RUN — the
// Stop button on this answer — and answer-revoked is a decision about a
// PERMISSION, of which this run happened to be a consequence. A history query
// can now tell them apart instead of reading every revocation as somebody
// pressing Stop.
//
// It used to be TermUserKilled, and not because anybody judged them the same:
// the durable vocabulary is closed by a CHECK constraint on
// executions.termination_reason, so until that CHECK was widened by a rung on
// the migration ladder the honest name could not be recorded at all.
//
// THE REASON IS METADATA AND THE SENTENCE IS THE WORDS. `answer-revoked` cannot
// say "df -h", so the sentence still names WHICH answer was taken back, and it
// is the part a person actually reads. The reason never replaces it.
//
// THE TWO OUTCOMES ARE BOTH REPORTED, and neither is a failure. A run that is
// gone from the registry, or one beginCancel refuses, has already reached a
// terminal state between the count and this call — the person asked for it to
// stop and it has stopped, for its own reasons — and saying "stopped 3" when
// one of them finished by itself would be a sentence that is not true. What
// this cannot report is a terminal LEDGER write that fails: terminalize logs
// it and the startup sweep repairs the run as interrupted, exactly as it does
// for agent.cancel, and the run is cancelled from the engine's and the
// person's point of view either way. That degrade is agent.cancel's, unchanged
// and shared, rather than a second reporting channel invented here.
func (s *WSServer) StopRunsForRevokedAnswer(
	ctx context.Context, r Responder, ids []int64, sentence string,
) (stopped, alreadyFinished []int64) {
	if s.agentTerminalizer == nil {
		// No agent methods are registered on this server, so no run in this
		// list can be live. Every id is reported as already finished rather
		// than as stopped: the caller's sentence must not claim a stop that
		// nothing performed.
		return nil, append([]int64(nil), ids...)
	}
	h := s.agentTerminalizer(r)
	for _, id := range ids {
		s.pendingRunsMu.Lock()
		rc, ok := s.pendingRuns[id]
		s.pendingRunsMu.Unlock()
		if !ok || rc.control == nil || !rc.control.beginCancel() {
			alreadyFinished = append(alreadyFinished, id)
			continue
		}
		runCtx, _ := log.StartTrace(ctx, runTrace(id))
		rc.control.cancelRunLeases(content.TermAnswerRevoked)
		rc.control.cancelContext(content.TermAnswerRevoked)
		rc.control.finishCancel(content.RunCancelled, content.TermAnswerRevoked, sentence, true)
		h.terminalize(runCtx, rc, content.RunCancelled, content.TermAnswerRevoked, sentence, r)
		stopped = append(stopped, id)
	}
	return stopped, alreadyFinished
}

// runTrace is the id of one agent exchange — the run — as every log line
// belonging to it names it. One derivation, so the ask, the question and the
// answer cannot disagree about which exchange they are part of.
//
// It is DERIVED rather than minted because the exchange outlives the frame:
// an ask parks, a person answers eight seconds later on a different request,
// and there is nowhere between the two to keep an id. The run id is the name
// the exchange already has, and internal/log hashes it into the W3C shape so
// the result is joinable by something that is not us (nocx-4l2a5.1).
func runTrace(runID int64) string {
	return log.DeterministicTraceID("run-" + strconv.FormatInt(runID, 10))
}
