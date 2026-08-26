package transport

// ledger.open / ledger.bind / ledger.close — the write path of the one
// authoritative ledger (nocx-rtg0.3, design §6.2 and §6.3).
//
// WHAT CROSSES, AND WHY IT MAY. These three methods carry facts the RENDERER
// derived from OSC 133 markers it already owns — that a command was
// submitted, that its execution was confirmed, that it ended and how. AD-1 as
// amended by nocx-m64b permits exactly that: raw OSC/VT sequences never cross
// and the backend never parses them (AD-6 unchanged), while typed facts
// derived by the frontend may cross as explicit, schema-checked ledger
// events. The backend RECORDS these; it never infers them, and not one ledger
// byte goes on the binary data plane.
//
// The close's exit code is the same permission one step stricter: since
// ADR-0024 it is not read out of the stream at all but taken from the
// authenticated execution attempt, which is the only thing allowed to
// complete a record. So the backend records a fact the renderer holds under
// authentication — it does not sniff, and it does not second-guess.
//
// WHAT DOES NOT CROSS. The environment. AD-7 makes session identity
// server-authoritative, and environmentForSession (ws_agent.go) is already
// this repo's one derivation of "where is this session" — the backend never
// trusts the renderer's idea of where it is. So the envelope names the
// SESSION and the backend derives the environment from the session's own
// facts, exactly as the ask transaction does. Design §6.2 writes the envelope
// field as `environment`; the reason it gives for the envelope — that a close
// must be able to create the row, whose environment_id, cwd, kind and intent
// are all NOT NULL — is fully served this way, because the session yields the
// environment and the other three ride the envelope. The one case it does not
// serve is an outbox replay after the session is gone; the outbox is
// nocx-rtg0.4 and that is where the question belongs.
//
// ADR-0019 §4: the ledger is the ONE authoritative store, and nothing may
// write two tables. This commit adds the ledger's write path beside
// command_history, which remains the live history path until nocx-rtg0.19
// removes it. The two do not share a row, a table or a writer.
//
// ADR-0020: StartExecution.Grant is the authority recorded on a run, not an
// enforcement object. Nothing here mints or checks a grant.
//
// The result shapes are declared once in contracts/ledger.*.schema.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── wire shapes ───────────────────────────────────────────────────────────

// ledgerEnvelopeWire is the IMMUTABLE envelope every lifecycle event repeats
// (design §6.2). The repetition is the point, not redundancy: v2 of this
// design sent {id, status, facts, durationMs} on close and claimed it could
// upsert a missing row, which it cannot — the row needs environment_id, cwd,
// kind and intent, all NOT NULL, and "create it with an empty intent"
// supplies one of four. A few hundred bytes per command removes an entire
// class of lost-entry failure.
//
// clientSeq is the RENDERER's ordering key for its outbox (§6.4). The backend
// does not order by it — seq is the total order and the backend assigns it —
// but it is echoed on every ack so the outbox knows which unacknowledged
// event was acknowledged.
type ledgerEnvelopeWire struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	Kind        string `json:"kind"`
	Intent      string `json:"intent"`
	Sensitivity string `json:"sensitivity"`
	ClientSeq   int64  `json:"clientSeq"`
}

type ledgerOpenParams struct {
	Envelope ledgerEnvelopeWire `json:"envelope"`
}

// ledgerBindFacts is what the renderer knows at OSC 133 C that the execution
// row has a column for. Nothing here is inferred by the backend, and nothing
// the repository cannot hold is accepted: a field that would be taken and
// then dropped is worse than a field that is refused.
type ledgerBindFacts struct {
	Interactivity string `json:"interactivity"`
	Executor      string `json:"executor"`
}

type ledgerBindParams struct {
	Envelope ledgerEnvelopeWire `json:"envelope"`
	Facts    ledgerBindFacts    `json:"facts"`
}

// ledgerCloseFacts is what the renderer knows when a command ends.
//
// terminationReason is the execution's own fact: which of the five outcomes a
// status plus an exit code cannot separate (ADR-0020 §4) this run had.
// Required — the column's CHECK names a closed set and there is no honest
// default.
//
// exitCode is the SHELL fact, and it lands in the entry's kind payload rather
// than a column of its own (design §3.2: hoisting it would make every other
// kind carry nulls). Null is a real value — an interrupted command has no exit
// code — so absent and null mean the same thing and both are recorded.
//
// `trusted` and `markers`, which design §3.3 puts beside exitCode in the shell
// arm, are NOT here. ADR-0024 deleted the trusted boolean, its laundering rule
// and trusted as a field crossing to history.record, and with it the anonymous
// marker cycle a MarkerTrace was read from. Neither has a source in the
// renderer any more, so accepting them would be asking for a guess.
type ledgerCloseFacts struct {
	TerminationReason string `json:"terminationReason"`
	ExitCode          *int   `json:"exitCode"`
}

// ledgerCloseParams is the close event. startedAt and durationMs are the
// entry's terminal facts the store had nowhere to put until nocx-rtg0.23:
// startedAt is the renderer's wall clock at submit, carried so a close whose
// open was lost still lands a row with a start; durationMs is the renderer's
// own measurement of how long the command took, which is never derived from
// the difference of two clocks.
type ledgerCloseParams struct {
	Envelope   ledgerEnvelopeWire `json:"envelope"`
	Status     string             `json:"status"`
	Facts      ledgerCloseFacts   `json:"facts"`
	DurationMs *int64             `json:"durationMs"`
	StartedAt  *int64             `json:"startedAt"`
}

// ledgerEventResponse is the ack all three methods answer with. It is one
// shape on purpose: the renderer's outbox drains events of three kinds
// through one path, and an ack it has to switch on is a second ordering
// implementation waiting to be written.
//
// outcome is what makes §6.3 visible rather than merely logged: `applied`
// changed the row, `replay` was a re-delivery in the phase the row already
// held, `dropped` would have moved the phase backwards. phase is the row's
// phase AFTER the event — unchanged for replay and dropped.
type ledgerEventResponse struct {
	ID          string `json:"id"`
	ClientSeq   int64  `json:"clientSeq"`
	Seq         int64  `json:"seq"`
	SubmittedAt int64  `json:"submittedAt"`
	Phase       string `json:"phase"`
	Outcome     string `json:"outcome"`
}

const (
	ledgerApplied = "applied"
	ledgerReplay  = "replay"
	ledgerDropped = "dropped"
)

// ── ingress bounds ────────────────────────────────────────────────────────

// maxLedgerIntentRunes bounds the envelope's intent. It is deliberately
// maxRecordCommandRunes: intent is the same product object history.record
// calls `command`, and two bounds on one concept is how they drift apart.
const maxLedgerIntentRunes = maxRecordCommandRunes

// maxExecutorRunes bounds the bind's executor identity — a name, never a
// document.
const maxExecutorRunes = 256

// ── phase ordering ────────────────────────────────────────────────────────

// ledgerPhaseRank ranks the lifecycle so "forwards" is expressible. The
// vocabulary is closed (schemaV1's CHECK), so an unknown value cannot reach
// here: every caller passes a Phase this file constructed.
func ledgerPhaseRank(p content.Phase) int {
	switch p {
	case content.PhaseOpen:
		return 0
	case content.PhaseBound:
		return 1
	case content.PhaseClosed:
		return 2
	}
	return -1
}

// ── shared, server-scoped state ───────────────────────────────────────────

// ledgerShared is the state the three methods share across every connection.
//
// mu SERIALIZES the phase decision. Deciding whether an event may be applied
// is a read-modify-write over one row — read the phase, compare, write — and
// the store offers no compare-and-set for it. Two connections submitting the
// same entry id would otherwise both read "no row" and both create a run.
// The cost is nothing that existed: every mutation in this store already goes
// through ONE writer goroutine (design §5.3), so ledger events were never
// parallel below this point.
//
// environments memoises which environment identities have been ensured AND
// observed in this process. StartExecution refuses an environment with no
// observation — there would be nothing to pin — and observations are
// append-only, so recording one per command would grow a version per command.
// One per environment per process is the right cadence: a restart is exactly
// when the mutable facts may have changed.
type ledgerShared struct {
	mu           sync.Mutex
	environments map[string]struct{}
}

func newLedgerShared() *ledgerShared {
	return &ledgerShared{environments: make(map[string]struct{})}
}

// ── the handler ───────────────────────────────────────────────────────────

// ledgerHandlers answers the three ledger lifecycle methods. It holds the
// LedgerOperation (nil → the content store is not wired), the connection's
// connState (session ownership, and the session facts the environment is
// derived from), the connection's client identity (the idempotency binding of
// the renderer-minted entry id) and the Responder; never the *WSServer.
type ledgerHandlers struct {
	op       capability.LedgerOperation // nil → content store not wired
	log      log.Logger
	shared   *ledgerShared
	state    *connState
	clientID string
	r        Responder
}

// ledgerCommand is one decoded, validated event: the phase it would move the
// row to, and everything the store needs to get there. Built by the three
// handlers, applied by one.
type ledgerCommand struct {
	target    content.Phase
	env       content.Environment
	entry     content.SubmitEntry
	start     content.StartExecution
	finish    content.FinishExecution
	clientSeq int64
}

func (h ledgerHandlers) handleOpen(ctx context.Context, req jsonrpcRequest) {
	var p ledgerOpenParams
	if msg := decodeParams(req.Params, &p); msg != "" {
		h.invalid(req, msg)
		return
	}
	cmd, msg := h.command(p.Envelope, content.PhaseOpen)
	if msg != "" {
		h.invalid(req, msg)
		return
	}
	h.apply(ctx, req, cmd)
}

func (h ledgerHandlers) handleBind(ctx context.Context, req jsonrpcRequest) {
	var p ledgerBindParams
	if msg := decodeParams(req.Params, &p); msg != "" {
		h.invalid(req, msg)
		return
	}
	cmd, msg := h.command(p.Envelope, content.PhaseBound)
	if msg != "" {
		h.invalid(req, msg)
		return
	}
	cmd.start.Interactivity = content.Interactivity(p.Facts.Interactivity)
	if p.Facts.Executor != "" {
		executor := p.Facts.Executor
		cmd.start.Executor = &executor
	}
	h.apply(ctx, req, cmd)
}

func (h ledgerHandlers) handleClose(ctx context.Context, req jsonrpcRequest) {
	var p ledgerCloseParams
	if msg := decodeParams(req.Params, &p); msg != "" {
		h.invalid(req, msg)
		return
	}
	cmd, msg := h.command(p.Envelope, content.PhaseClosed)
	if msg != "" {
		h.invalid(req, msg)
		return
	}
	// The entry's terminal facts ride FinishExecution and NOT the submitted
	// entry, so the two close paths — one on a row that exists, one creating
	// its own — write the same columns through the same statement. Putting a
	// copy on cmd.entry would give the created row a second writer, and two
	// writers of one fact go out of step the moment one of them changes.
	cmd.finish = content.FinishExecution{
		// The end is the BACKEND wall clock, like every other stamp the store
		// writes: retention judges by it, and what the store judges by the
		// store owns (ADR-0019). durationMs is the renderer's measurement and
		// the two are never mixed into one number (design §3.2).
		EndedAt:           time.Now().UnixMilli(),
		TerminationReason: content.TerminationReason(p.Facts.TerminationReason),
		Status:            content.EntryStatus(p.Status),
		StartedAt:         p.StartedAt,
		DurationMs:        p.DurationMs,
	}
	// The kind payload. Only the shell arm exists (design §3.3), and a close
	// on any other kind leaves the column alone rather than writing an empty
	// arm that claims to be a shell fact.
	//
	// The arm ALONE, deliberately: the store merges it into whatever payload
	// the row holds (FinishExecution's json_patch), so the receipt the open
	// wrote survives untouched. Resending the receipt here would be worse
	// than pointless — the envelope is immutable, so a close after a capture
	// save would resend the span that save consumed and put it back, and a
	// retried save would then replace text at offsets the rewrite has already
	// moved. The row's receipt is the open's fact plus the rewrite's; the
	// close has nothing to say about it.
	if content.EntryKind(p.Envelope.Kind) == content.EntryShell {
		payload := content.ShellPayloadJSON(p.Facts.ExitCode)
		cmd.finish.Payload = &payload
	}
	h.apply(ctx, req, cmd)
}

// command turns the envelope into the store's own shapes: the environment
// derived from the SESSION (never from the renderer's claim), and the entry
// the row is created from. The returned message is empty when the envelope is
// usable; it is never a repaired envelope.
func (h ledgerHandlers) command(e ledgerEnvelopeWire, target content.Phase) (ledgerCommand, string) {
	sid := session.ID(e.SessionID)
	if !h.state.has(sid) {
		return ledgerCommand{}, "envelope.sessionId names no session on this connection"
	}
	sess, ok := h.state.get(sid)
	if !ok {
		return ledgerCommand{}, "envelope.sessionId names no session on this connection"
	}
	env := environmentForSession(sess)

	// Mask before the text is durable. history.record already writes command
	// text to this database and masks it at the wire "in exactly one place,
	// because the durable command is always the masked one"; this method is
	// the second durable writer of the same product object, so it masks
	// through the SAME owner rather than growing a second policy. A detection
	// failure fails CLOSED — the raw text must not reach a row.
	maskedResult, err := maskLedgerCommand(e.Intent)
	if err != nil {
		return ledgerCommand{}, "intent could not be screened for secrets; not recorded"
	}
	masked, findings, segs := maskedResult.text, maskedResult.findings, maskedResult.segments

	// And keep the RECEIPT, which this method used to throw away
	// (`masked, _, _, err`): how many secrets were taken out, of which kinds,
	// and where the masks sit. history.query's contract declares all three on
	// every entry, and everything built on them — the "3 secrets masked"
	// line, the recall overlay's unresolved chips, and the vault save that
	// turns one span into a reference — is unanswerable without them.
	//
	// It rides entries.payload rather than columns of its own: a column is a
	// schemaV1 change, a schemaV1 change bumps schemaVersion, and a bump
	// DROPs command_history with the user's real history in it. nocx-rtg0.19
	// pays that once; this must not make anyone pay it twice
	// (internal/content/redaction.go carries the whole argument).
	//
	// The receipt is written for every intent, including the empty one of a
	// clean command: absent then means "recorded by a build that kept no
	// receipts", which is a different fact from "nothing was masked".
	receipt := content.EntryMasking{
		MaskedCount: len(findings),
		MaskedKinds: maskedKindsOf(findings),
		Redactions:  redactionsOf(findings, segs),
	}
	payload, err := content.WithEntryMasking("{}", receipt)
	if err != nil {
		// Unreachable for a freshly marshalled receipt, and it still fails
		// closed rather than storing an intent whose receipt was lost: a row
		// that cannot say what was masked out of it is the soft degrade this
		// whole path exists to prevent.
		return ledgerCommand{}, "intent could not be screened for secrets; not recorded"
	}

	return ledgerCommand{
		target:    target,
		env:       env,
		clientSeq: e.ClientSeq,
		entry: content.SubmitEntry{
			ID:            e.ID,
			Client:        h.clientID,
			EnvironmentID: env.ID,
			// The block's DURABLE anchor (nocx-rtg0.28, design §6.1), and it
			// comes from the SESSION for the same reason the environment
			// above does: open already resolved which pane this session is
			// the pipe of, and refused the open if that pane did not exist.
			// A paneId on the envelope would put the same input under a
			// second owner, and the renderer's copy would be the one nobody
			// checked. Nil when the session is attached to no recorded pane
			// — the ordinary state until every tab mints one — which costs
			// the restore hint and no recall.
			PaneID: panePtr(sess.PaneID()),
			// SessionID stays nil: entries.session_id is a foreign key into
			// the ledger's own sessions table, and nothing creates a row
			// there yet (a session row needs a workspace — nocx-49d4 owns
			// that question). The column is nullable by design — an entry
			// outlives its session (ADR-0019 §5) and sessionId is never a
			// recall key (design §3.1) — and from nocx-rtg0.28 the store
			// actively nulls it at every start, because the pipe it names
			// died with the backend that opened it.
			Cwd:         e.Cwd,
			Kind:        content.EntryKind(e.Kind),
			Intent:      masked,
			Sensitivity: content.Sensitivity(e.Sensitivity),
			Payload:     payload,
		},
		start: content.StartExecution{EntryID: e.ID},
	}, ""
}

// panePtr turns the session's pane into the column's nullable value: empty
// means "attached to no recorded pane", and that must reach the store as NULL
// rather than as an empty string, which would be an id naming nothing and
// would be refused by the chain lookup.
func panePtr(paneID string) *string {
	if paneID == "" {
		return nil
	}
	return &paneID
}

// apply is the whole of §6.3, in one place because the four rules are one
// decision: what phase is this row in, and does this event move it forwards?
//
//  1. seq is the total order and the backend assigns it (Submit).
//  2. Phase is monotonic — an event that would move it backwards is dropped
//     and logged.
//  3. A close for an unknown id creates the row, closed, from its envelope —
//     and so does a bind, by the same reasoning and the same envelope.
//  4. Re-delivery of any event for a row already in that phase is a no-op.
func (h ledgerHandlers) apply(ctx context.Context, req jsonrpcRequest, cmd ledgerCommand) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "method not found: content store not wired"})
		return
	}
	var out ledgerEventResponse
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.LedgerService) error {
		h.shared.mu.Lock()
		defer h.shared.mu.Unlock()

		row, err := svc.Entry(ctx, cmd.entry.ID)
		if err != nil {
			return err
		}
		if envErr := h.ensureEnvironment(ctx, svc, cmd.env); envErr != nil {
			return envErr
		}
		if row == nil {
			out, err = h.create(ctx, svc, cmd)
			return err
		}

		out = ledgerEventResponse{
			ID:          row.ID,
			ClientSeq:   cmd.clientSeq,
			Seq:         row.IngestSeq,
			SubmittedAt: row.SubmittedAt,
			Phase:       string(row.Phase),
		}
		switch {
		case ledgerPhaseRank(cmd.target) < ledgerPhaseRank(row.Phase):
			// Rule 2. Dropped, and never silent: an event the product
			// discarded is a fact the next person debugging a missing block
			// needs, and the ack says so too.
			h.log.Warn("ledger: event dropped — phase is monotonic",
				"entry", row.ID, "phase", string(row.Phase), "event", string(cmd.target),
				"clientSeq", cmd.clientSeq)
			out.Outcome = ledgerDropped
			return nil
		case ledgerPhaseRank(cmd.target) == ledgerPhaseRank(row.Phase):
			// Rule 4.
			out.Outcome = ledgerReplay
			return nil
		}
		out.Outcome = ledgerApplied
		return h.advance(ctx, svc, cmd, row, &out)
	})
	if err != nil {
		h.answerError(req, err)
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(out))
}

// create writes a row that does not exist yet and walks it to the event's
// phase in one go. Rule 3 lives here: the envelope carries everything the
// four NOT NULL columns need, so a close whose open was lost still lands.
func (h ledgerHandlers) create(ctx context.Context, svc capability.LedgerService, cmd ledgerCommand) (ledgerEventResponse, error) {
	res, err := svc.Submit(ctx, cmd.entry)
	if err != nil {
		return ledgerEventResponse{}, err
	}
	out := ledgerEventResponse{
		ID:          res.ID,
		ClientSeq:   cmd.clientSeq,
		Seq:         res.IngestSeq,
		SubmittedAt: res.SubmittedAt,
		Phase:       string(content.PhaseOpen),
		Outcome:     ledgerApplied,
	}
	if cmd.target == content.PhaseOpen {
		return out, nil
	}
	execID, err := svc.StartExecution(ctx, cmd.start)
	if err != nil {
		return ledgerEventResponse{}, err
	}
	out.Phase = string(content.PhaseBound)
	if cmd.target == content.PhaseBound {
		return out, nil
	}
	if err := svc.FinishExecution(ctx, execID, cmd.finish); err != nil {
		return ledgerEventResponse{}, err
	}
	out.Phase = string(content.PhaseClosed)
	return out, nil
}

// advance moves an existing row forwards. open→bound starts the run;
// open→closed starts one and ends it, because a closed entry with no
// execution has nowhere to record how it ended; bound→closed ends the run
// that is already live.
func (h ledgerHandlers) advance(ctx context.Context, svc capability.LedgerService, cmd ledgerCommand, row *content.LedgerEntry, out *ledgerEventResponse) error {
	execID, ok := liveExecutionOf(row)
	if !ok {
		id, err := svc.StartExecution(ctx, cmd.start)
		if err != nil {
			return err
		}
		execID = id
		out.Phase = string(content.PhaseBound)
	}
	if cmd.target == content.PhaseBound {
		out.Phase = string(content.PhaseBound)
		return nil
	}
	if err := svc.FinishExecution(ctx, execID, cmd.finish); err != nil {
		return err
	}
	out.Phase = string(content.PhaseClosed)
	return nil
}

// liveExecutionOf picks the run a close should end: the newest execution that
// has not ended. A row can carry several (ADR-0020 §4 — a rerun, a retry and
// a takeover are executions of one entry), and ending an already-ended one
// would rewrite a finished run's outcome.
func liveExecutionOf(row *content.LedgerEntry) (int64, bool) {
	var id int64
	found := false
	for _, ex := range row.Executions {
		if ex.EndedAt != nil {
			continue
		}
		if !found || ex.ID > id {
			id, found = ex.ID, true
		}
	}
	return id, found
}

// ensureEnvironment records the environment's durable identity and one
// observation, once per environment per process. StartExecution refuses an
// environment with no observation — an unpinned execution would be
// reinterpreted later with today's facts — so this is the precondition of
// every bind and close, not bookkeeping.
//
// The observation's facets are deliberately empty. Criticality is user-owned
// (design §3.1: no derivation can tell a production host from a staging one,
// and guessing wrong either way is worse than asking once), so it takes its
// documented default rather than a guess, and the confidence map says nothing
// because nothing has been claimed.
func (h ledgerHandlers) ensureEnvironment(ctx context.Context, svc capability.LedgerService, env content.Environment) error {
	if _, done := h.shared.environments[env.ID]; done {
		return nil
	}
	if err := svc.EnsureEnvironment(ctx, env); err != nil {
		return err
	}
	if _, err := svc.RecordObservation(ctx, content.Observation{
		EnvironmentID: env.ID,
		Confidence:    "{}",
		Criticality:   content.CriticalityRoutine,
		Payload:       "{}",
	}); err != nil {
		return err
	}
	h.shared.environments[env.ID] = struct{}{}
	return nil
}

func (h ledgerHandlers) invalid(req jsonrpcRequest, msg string) {
	_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
}

// answerError maps the write path's failures. An id already used by a
// different submission is reachable from the renderer (a client that reused a
// UUID) and is invalid params, never a server fault; a gate refusal keeps its
// control.saturated shape; anything else is an internal error.
func (h ledgerHandlers) answerError(req jsonrpcRequest, err error) {
	switch {
	case errors.Is(err, content.ErrIDConflict),
		errors.Is(err, capability.ErrOperationInactive):
		h.invalid(req, err.Error())
	default:
		answerOperationRefusal(h.r, req, err)
	}
}

// ── validation ────────────────────────────────────────────────────────────

// validateLedgerEnvelope checks every reachable field of the immutable
// envelope. It is the params validator's half of the check — the session
// lookup needs the connection and happens in the handler.
func validateLedgerEnvelope(e ledgerEnvelopeWire) string {
	if strings.TrimSpace(e.ID) == "" || utf8.RuneCountInString(e.ID) > maxIDRunes {
		return "envelope.id is required and bounded"
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return "envelope.sessionId is required"
	}
	if strings.TrimSpace(e.Cwd) == "" || utf8.RuneCountInString(e.Cwd) > maxCwdRunes {
		return "envelope.cwd is required and bounded"
	}
	switch content.EntryKind(e.Kind) {
	case content.EntryShell, content.EntryAsk, content.EntryAction, content.EntryText, content.EntryFrame:
	default:
		return "envelope.kind must be one of shell, ask, action, text, frame"
	}
	// Intent may be EMPTY and that is a product state, not a defect: an
	// orphan OSC 133 C is an entry with no intent (design §4.4), and refusing
	// it would drop the very execution the fail-open rule exists to keep.
	if utf8.RuneCountInString(e.Intent) > maxLedgerIntentRunes {
		return fmt.Sprintf("envelope.intent exceeds %d characters", maxLedgerIntentRunes)
	}
	switch content.Sensitivity(e.Sensitivity) {
	case content.SensitivityNormal, content.SensitivitySensitive:
	default:
		return "envelope.sensitivity must be one of normal, sensitive"
	}
	if e.ClientSeq < 0 {
		return "envelope.clientSeq must not be negative"
	}
	return ""
}

func validateLedgerOpenRaw(raw json.RawMessage) string {
	var p ledgerOpenParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	return validateLedgerEnvelope(p.Envelope)
}

func validateLedgerBindRaw(raw json.RawMessage) string {
	var p ledgerBindParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateLedgerEnvelope(p.Envelope); msg != "" {
		return msg
	}
	// Absent is legitimate: the shell driver knows nothing about
	// interactivity at OSC 133 C, and the column's own default is `none`.
	switch content.Interactivity(p.Facts.Interactivity) {
	case "", content.InteractivityNone, content.InteractivityStdin,
		content.InteractivityTTY, content.InteractivityAwaitTakeover:
	default:
		return "facts.interactivity must be one of none, stdin, tty, awaiting-takeover"
	}
	if utf8.RuneCountInString(p.Facts.Executor) > maxExecutorRunes {
		return fmt.Sprintf("facts.executor exceeds %d characters", maxExecutorRunes)
	}
	return ""
}

func validateLedgerCloseRaw(raw json.RawMessage) string {
	var p ledgerCloseParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if msg := validateLedgerEnvelope(p.Envelope); msg != "" {
		return msg
	}
	switch content.EntryStatus(p.Status) {
	case content.EntryPending, content.EntryRunning, content.EntrySuccess,
		content.EntryFailure, content.EntryInterrupted, content.EntryUnknown:
	default:
		return "status must be one of pending, running, success, failure, interrupted, unknown"
	}
	switch content.TerminationReason(p.Facts.TerminationReason) {
	case content.TermCompleted, content.TermFailed, content.TermTimeout,
		content.TermTransportGone, content.TermUserKilled, content.TermAgentDeclined,
		content.TermInterrupted:
	default:
		return "facts.terminationReason must be one of completed, failed, timeout, transport-gone, user-killed, agent-declined, interrupted"
	}
	// A duration is measured, never a difference of wall times (design §3.2),
	// so a negative one is a broken clock rather than a long command.
	if p.DurationMs != nil && *p.DurationMs < 0 {
		return "durationMs must not be negative"
	}
	// startedAt is a WALL clock and is checked by the floor history.record
	// already uses, because it is the same product fact reaching the same
	// database: a performance.now() reading lands in January 1970, and there
	// the retention sweep deletes the row microseconds after it is written
	// (nocx-rtg0.16). One owner for "is this a wall clock", not two.
	if p.StartedAt != nil && *p.StartedAt < epochFloor {
		return fmt.Sprintf("startedAt must be epoch milliseconds on or after 2020-01-01 (got %d)", *p.StartedAt)
	}
	// An exit code is a shell fact. On any other kind it would be accepted and
	// then dropped, since only the shell arm of the kind payload holds one.
	if p.Facts.ExitCode != nil && content.EntryKind(p.Envelope.Kind) != content.EntryShell {
		return "facts.exitCode is a shell fact and is only accepted on envelope.kind = shell"
	}
	return ""
}

// ── registration ──────────────────────────────────────────────────────────

// ledgerSpecs declares the ledger lifecycle methods on the CONTENT operation
// queue: the ledger is content's schema v1 (ADR-0019), so it shares the
// content domain's gate and queue with history.* and agent.*.
func (s *WSServer) ledgerSpecs(contentSub control.Submission, lane control.Admission, contentGate control.Admission) []methodSpec {
	var op capability.LedgerOperation
	if s.contentDB != nil {
		op = capability.NewLedgerOperation(contentGate, lane, s.contentDB)
	}
	// One shared state for all three methods and every connection: the phase
	// decision is serialized across the process, not per connection.
	shared := newLedgerShared()
	build := func(w *wsConn, state *connState, r Responder) ledgerHandlers {
		return ledgerHandlers{
			op: op, log: s.log, shared: shared, state: state,
			// The CONNECTION identity binds the untrusted entry id, exactly
			// as it binds the ask's: a reconnect mints a new one, and the
			// store refuses a replayed id whose content changed.
			clientID: connectionID(w),
			r:        r,
		}
	}
	return []methodSpec{
		reg(contentSub, "ledger.open", params(validateLedgerOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleOpen(ctx, req) }
		}),
		reg(contentSub, "ledger.bind", params(validateLedgerBindRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleBind(ctx, req) }
		}),
		reg(contentSub, "ledger.close", params(validateLedgerCloseRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := build(w, state, r)
			return func(ctx context.Context, req jsonrpcRequest) { h.handleClose(ctx, req) }
		}),
		// The read path (nocx-rtg0.20). It takes no connection and no
		// connState: recall is a question about the ledger, not about this
		// tab — an entry outlives its session (ADR-0019 §5) and sessionId is
		// never a recall key.
		regResponder(contentSub, "ledger.query", params(validateLedgerQueryRaw), func(r Responder) handlerFunc {
			h := ledgerReadHandlers{op: op, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleQuery(ctx, req) }
		}),
		regResponder(contentSub, "ledger.get", params(validateLedgerGetRaw), func(r Responder) handlerFunc {
			h := ledgerReadHandlers{op: op, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleGet(ctx, req) }
		}),
		// The capture path (nocx-2f0f). It takes no connection either, for
		// the same reason: a body belongs to an ENTRY, and an entry outlives
		// the session it ran in.
		regResponder(contentSub, "ledger.artifact", params(validateLedgerArtifactRaw), func(r Responder) handlerFunc {
			h := ledgerReadHandlers{op: op, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleArtifact(ctx, req) }
		}),
		regResponder(contentSub, "ledger.capture", params(validateLedgerCaptureRaw), func(r Responder) handlerFunc {
			h := ledgerCaptureHandlers{op: op, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handle(ctx, req) }
		}),
	}
}
