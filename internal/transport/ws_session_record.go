package transport

// The session output recorder (nocx-22k1c.1): the backend consuming its own
// replay ring, so a session with no client attached keeps running and its
// bytes end up on disk.
//
// ── The stall this removes ───────────────────────────────────────────────
//
// ring.write BLOCKS when the ring is full and nothing has been acked. That is
// deliberate and it is AD-10: throttle the source, never drop. But the acks
// come from an ATTACHED CLIENT, and a session that outlives its window has
// none — so nothing acked, and a detached session froze after RingCapacity
// bytes. The daemon stalled the very work it exists to protect.
//
// The fix is not to weaken AD-10, and this does not: the backend becomes the
// ring's consumer. The bytes go to the content store instead of to a socket,
// the ring is freed against a cursor that is nobody's acknowledgement, and
// AD-10 holds as written — nothing dropped, nothing unbounded.
//
// ── What it does not do ──────────────────────────────────────────────────
//
// It does not interpret a byte. A stream has no width, so there is no size,
// no grid and no VT here: the loop moves what the ring already holds into a
// store that keys it by the offset the ring already assigned. AD-6 is
// untouched — storing what you already forward is not sniffing it.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// recorderRetryInterval is how long a recorder waits before re-reading the
// stance after the store declined to keep anything.
//
// It is a POLL because the thing it is waiting for has no event: History is a
// live setting (app.go's registry notifier) and a person may turn output
// retention back on at any moment, with nothing in this package told about
// it. The alternative — exiting for the life of the session — would leave the
// product saying recording is on while the sessions already running were
// still stalling, which is the silent contradiction the status surface exists
// to prevent. Two seconds is far below human patience for a settings change
// and costs one timer per session, only while retention is off.
const recorderRetryInterval = 2 * time.Second

// SessionOutputRecorder is the transport's view of the durable sink: the
// three things this package asks of it and nothing else. content's own
// repository satisfies it; a test's fake satisfies it without a database.
//
// The read is on the SAME interface as the write, deliberately (nocx-22k1c.2).
// One store owns a session's recording — it is the single writer of both its
// tables and the only thing that knows what the retention bound dropped — so
// a second seam for reading it would be a second view of one owner's state
// (AD-8), wired separately and therefore able to be absent while the writer
// is present. A recording nothing can read is not durable in any sense worth
// the word; writing it and reading it are two halves of one capability and
// they arrive together or not at all.
type SessionOutputRecorder interface {
	// Append records one run of bytes at its stream offset. A result with
	// Kept false is a refusal — retention is off — and not a failure.
	Append(ctx context.Context, in content.SessionOutputAppend) (content.SessionOutputResult, error)
	// Read returns everything kept for a session, in stream order, with the
	// byte ranges the bound dropped. An unknown session is an empty
	// recording and not an error.
	Read(ctx context.Context, sessionID string) (content.SessionOutputRecording, error)
	// Stance says whether output produced now would be kept, and why not.
	Stance() content.SessionOutputStance
}

// WithSessionOutputRecorder attaches the durable sink for session output.
// Without it the server records nothing, the ring is freed by client acks
// alone, and a detached session throttles once the ring fills — which is the
// pre-recorder behaviour, and is stated in the product through
// history.status rather than only in a log.
func WithSessionOutputRecorder(r SessionOutputRecorder) WSServerOption {
	return func(s *WSServer) { s.sessionRecorder = r }
}

// recordSessionOutput is the recorder's loop: one goroutine per session,
// started by pumpToRing and ending when the ring closes.
//
// The invariant it maintains, with both ends named: from the moment the last
// client detaches until one attaches, the ring's consumer is this loop, and
// the source is never throttled while it is keeping up. It is a CONSUMER of
// the ring and never a subscriber — it reads by offset and moves the
// persistence cursor, which is not an acknowledgement and never becomes one.
func (s *WSServer) recordSessionOutput(ctx context.Context, sid session.ID, ring *outputRing) {
	rec := s.sessionRecorder
	if rec == nil {
		return
	}
	// The flag is what makes trim respect this loop: while it is set, the
	// ring may not free bytes this cursor has not passed. Cleared on every
	// exit, so a recorder that dies hands the ring back to the acks rather
	// than wedging it against a cursor nothing will move again.
	ring.setRecording(true)
	defer ring.setRecording(false)

	var pos uint64
	for {
		data, from, needsReset := ring.snapshot(pos)
		if needsReset {
			// Unreachable while the flag above is set — trim never passes
			// the persistence cursor — and checked rather than assumed
			// because the cost of being wrong is a recording that claims
			// bytes it does not hold. Recording that session stops here;
			// what is already on disk stays, and stays honest.
			s.log.Warn("session recording lost its place in the ring; recording stops for this session",
				"session_id", string(sid), "at", pos, "ring_from", from)
			return
		}
		if len(data) == 0 {
			if ring.waitForData(ctx, pos) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}

		res, err := rec.Append(ctx, content.SessionOutputAppend{
			SessionID: string(sid),
			Offset:    pos,
			Body:      data,
		})
		if err != nil {
			// The store refused the write. Say it where the product says
			// every other history write failure — history.status already
			// owns "the store is open and refusing writes", raises once per
			// episode rather than once per loss, and has a closing event.
			if s.historyStatus != nil {
				s.historyStatus.Raise(HistoryDegradeWriteFailed, err.Error())
			}
			s.log.Warn("session recording failed; the ring falls back to client acks",
				"session_id", string(sid), "at", pos, "error", err)
			return
		}
		if !res.Kept {
			// Retention is off. Nothing is durable, so the cursor does not
			// move and the flag comes down: the ring goes back to being
			// freed by acks alone, which is the degrade the History section
			// states. Re-read the stance in a moment — the setting is live.
			ring.setRecording(false)
			// The session's end is checked HERE and not at the top of the
			// loop, because everywhere else the loop is already waiting on
			// the ring and a close wakes it. This is the one wait that is
			// not on the ring, so without this line a recorder that had
			// nothing to keep would go on polling a session that ended.
			if ring.isClosed() {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(recorderRetryInterval):
			}
			continue
		}
		ring.setRecording(true)
		if s.historyStatus != nil {
			// The closing event for an episode this loop opened. ClearReason
			// and not Clear: a runtime success does not disprove a startup
			// degrade, and clearing unconditionally would erase "the content
			// key could not be read" — a sentence still true and one nothing
			// else would ever say again.
			s.historyStatus.ClearReason(HistoryDegradeWriteFailed)
		}
		if res.Dropped > 0 {
			s.log.Info("session recording dropped its oldest bytes to stay inside the output cap",
				"session_id", string(sid), "bytes", res.Dropped)
		}
		pos += uint64(len(data))
		if err := ring.recordTo(pos); err != nil {
			s.log.Warn("session recording could not advance its cursor",
				"session_id", string(sid), "at", pos, "error", err)
			return
		}
	}
}

// sessionRecordingStance is what the product is told about a detached
// session's output: whether it is being kept, and if not, which switch says
// so. A server with no recorder wired is not making a claim about a setting —
// there is no sink at all — and answers with the same "history is off"
// stance, which is what history.status.available already elaborates.
func (s *WSServer) sessionRecordingStance() content.SessionOutputStance {
	if s.sessionRecorder == nil {
		return content.SessionOutputHistoryOff
	}
	return s.sessionRecorder.Stance()
}

// ── session.output: handing a recording back ─────────────────────────────
//
// The read half of the recording (nocx-22k1c.2). The write half above is
// what makes a detached session survive; this is what makes it worth
// surviving — before it, `Read` had no production caller and a client
// joining an hour into a run could recover only what the ring still held,
// which is 256 KiB and about ten screens (ring.go).
//
// THE CLIENT ASKS BY OFFSET, and by the offset it already speaks: the AD-9
// ack's coordinate, which the ring keys on and the recorder wrote against.
// One origin for the stream means a recording, a ring replay and a client's
// own cursor are joined by arithmetic and never by translation. Inventing a
// second coordinate — a line, a block, a screen — would put a converter
// between two things that already agree.
//
// AD-6 IS UNTOUCHED. What goes back is bytes and a size. The backend holds
// no grid, no cursor and no rows; the renderer feeds the bytes to its own VT
// at the size the backend decided the session runs at (nocx-eidfb.1), and
// two clients agree because the two inputs are equal — not because anything
// here decided what a screen looks like.
//
// WHERE IT MEETS THE RING, both ends of the interval named: while a recorder
// is running, the ring may not free a byte the persistence cursor has not
// passed (ring.go's trim and reclaimRecorded), so from the first append
// until the recorder stops, the recording's end offset is at or after the
// ring's oldest retained byte. A client therefore reads to `produced` and
// attaches THERE, and the two halves meet with nothing between them. When
// recording is off the recording stops advancing while acks go on freeing
// the ring, and the client — which can see both numbers, `produced` here and
// `replayFrom` on sessions.live — attaches at the later of them and knows
// the difference is a hole. That comparison is deliberately the renderer's:
// the ring's oldest offset belongs to sessions.live and whether output is
// recorded at all belongs to history.status, and a copy of either here would
// be the second owner AD-8 forbids.

// maxSessionOutputBytes bounds ONE answer's worth of recorded bytes.
//
// A recording is bounded by the user's per-command cap, which reaches 4 MiB
// (settings.HistoryOutputCapKB), and base64 on the wire makes that a third
// larger again — one JSON-RPC message nobody can make progress against. So
// an answer carries at most this much and says where it stopped: `produced`
// is above the last run's end whenever there is more, and the caller pages
// by asking again from there.
//
// 256 KiB is the DEFAULT cap expressed in the unit this code works in, so
// the ordinary recording arrives whole in one call and paging is the
// exception a raised setting buys, rather than the rule.
const maxSessionOutputBytes = 256 << 10

// sessionOutputParams is the request. It names the session COMPLETELY, in
// the same three words attach uses (nocx-3oupk): a bare id re-resolves to
// whatever holds it now, and a client that kept a binding across a
// coordinator restart must be told its binding is stale rather than that it
// guessed a bad id.
type sessionOutputParams struct {
	SessionID  string `json:"sessionId"`
	InstanceID string `json:"instanceId,omitempty"`
	// A POINTER, so "absent" and "zero" stay different facts — the epoch is
	// minted from 1, so an explicit 0 names no incarnation. Same rule, same
	// reason as attachParams.
	SessionEpoch *uint64 `json:"sessionEpoch,omitempty"`
	// From is the stream offset to answer from; absent is 0, the start of
	// the recording. It is the ack's coordinate and not a new one.
	From uint64 `json:"from"`
}

// claimedEpoch is the epoch the caller claimed, or 0 for "claimed none" —
// the form judgeClaim takes.
func (p sessionOutputParams) claimedEpoch() uint64 {
	if p.SessionEpoch == nil {
		return 0
	}
	return *p.SessionEpoch
}

// sessionOutputResult is the session.output payload, declared once
// (contracts/session.output.schema.json) and pinned by the DTO and
// over-the-wire contract tests.
type sessionOutputResult struct {
	SessionID string `json:"sessionId"`
	// EffectiveSize is the geometry the BACKEND decided this session runs at
	// (nocx-eidfb.1). It rides the recording because bytes alone do not make
	// a screen: the same stream at two widths wraps differently, so a client
	// rendering a recovered recording at its own guess would disagree with
	// the client that watched it live.
	EffectiveSize sizeResult `json:"effectiveSize"`
	From          uint64     `json:"from"`
	// Runs and Gaps are NEVER nil on the wire — the renderer maps over both,
	// and a nil slice marshals as null (the defect this repo's contract
	// directory found on its first run).
	Runs []sessionOutputRunResult `json:"runs"`
	Gaps []sessionOutputGapResult `json:"gaps"`
	// Produced is the recording's end offset: how many bytes the session has
	// printed in total, INCLUDING what the bound dropped. It is where the
	// next page starts and where the attach starts, and it is what makes a
	// hole measurable rather than invisible.
	Produced uint64 `json:"produced"`
}

// sessionOutputRunResult is one contiguous stretch of recorded bytes. Body
// is a []byte so encoding/json renders it base64: the backend never decodes
// the stream (AD-6), and a rune can straddle any boundary the recorder
// happened to write at, so handing back a string would mean decoding it here
// with something that does not own the decoder.
type sessionOutputRunResult struct {
	Offset uint64 `json:"offset"`
	Body   []byte `json:"body"`
}

// sessionOutputGapResult is one byte range the recording no longer holds, in
// the ledger's own gap words (contracts/ledger.get.schema.json#/$defs/gap) —
// so "what is missing" is said the same way here as on an artifact.
type sessionOutputGapResult struct {
	Start  uint64 `json:"start"`
	End    uint64 `json:"end"`
	Reason string `json:"reason"`
}

// sessionOutputHandlers answers session.output. It holds the per-session
// operation factory, the store, this backend's instance identity and its
// Responder — never the *WSServer.
type sessionOutputHandlers struct {
	ops      *capability.SessionOperations
	store    SessionOutputRecorder
	instance session.InstanceID
	r        Responder
	log      log.Logger
}

// handleSessionOutput answers with the recorded bytes of one session from a
// stream offset.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"session.output","params":{"sessionId":"…","from":0}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"sessionId":"…","effectiveSize":{…},"from":0,
//	     "runs":[{"offset":0,"body":"…"}],"gaps":[],"produced":1048576}}
//
// The session gate is held for exactly as long as the claim needs judging
// and the size needs reading, and the STORE is read outside it. The two
// belong to different owners with different costs: the registry read is a
// map lookup, and the store read is a decrypting query against a file. A
// disk read under the session gate would make every resize and close on this
// backend wait behind somebody's hour of scrollback.
func (h sessionOutputHandlers) handleSessionOutput(ctx context.Context, req jsonrpcRequest) {
	var params sessionOutputParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId is required"})
		return
	}
	sid := session.ID(params.SessionID)

	// The instance is judged before the session is looked up at all, so a
	// binding carried across a coordinator restart is answered "that is a
	// different backend" rather than "no such session" (judgeClaim).
	if foreignInstance(h.instance, params.InstanceID) {
		_ = h.r.TryError(req.ID, foreignInstanceRefusal())
		return
	}
	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = h.r.TryError(req.ID, refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId"))
		return
	}

	var size session.Size
	var refusal *RPCError
	runErr := op.Run(ctx, func(_ context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			sess = nil
		}
		if r := judgeClaim(h.instance, params.InstanceID, params.claimedEpoch(), sess); r != nil {
			refusal = r
			return nil
		}
		size = sess.EffectiveSize()
		return nil
	})
	if runErr != nil {
		answerOperationRefusal(h.r, req, runErr)
		return
	}
	if refusal != nil {
		_ = h.r.TryError(req.ID, *refusal)
		return
	}

	rec, err := h.store.Read(ctx, string(sid))
	if err != nil {
		// The one external call this handler makes. It is answered as a
		// failure and NEVER as an empty recording: "the store could not be
		// read" and "this session printed nothing" are different facts, and
		// a client told the second would draw a blank screen over an hour of
		// work and never ask again.
		h.log.Warn("session output could not be read",
			"session_id", string(sid), "error", err)
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
		return
	}

	_ = h.r.TryResult(req.ID, mustMarshal(projectSessionOutput(string(sid), size, params.From, rec)))
}

// projectSessionOutput renders a recording onto the wire for one requested
// span. Its own function because it is the whole of what this method decides
// and it decides it from data alone — no store, no socket, no clock.
//
// The rule it keeps: within the span it answers, every byte is accounted
// for, as a run when it is there and as a gap when it is not. A shorter
// answer that said nothing about the shortfall would be indistinguishable
// from a shorter session, which is the one wrong answer here.
//
// Gaps are derived from the runs that are actually being returned, not
// copied from the recording's own list. Two reasons, and the second is why
// this is not merely equivalent: a derived hole cannot drift from the runs
// beside it, and — because an answer is bounded — a copied list would report
// a hole the caller has not paged as far as yet. A hole is a claim that the
// bytes are gone; running out of budget is not that claim, and the caller
// tells the two apart by `produced` still being ahead.
func projectSessionOutput(sid string, size session.Size, from uint64, rec content.SessionOutputRecording) sessionOutputResult {
	out := sessionOutputResult{
		SessionID:     sid,
		EffectiveSize: sizeResultOf(size),
		From:          from,
		Runs:          []sessionOutputRunResult{},
		Gaps:          []sessionOutputGapResult{},
		Produced:      rec.Produced,
	}

	budget := maxSessionOutputBytes
	cursor := from
	for _, run := range rec.Runs {
		if budget <= 0 {
			break
		}
		start, body := run.Offset, run.Body
		end := start + uint64(len(body))
		if end <= from {
			continue
		}
		if start < from {
			body = body[from-start:]
			start = from
		}
		if len(body) > budget {
			body = body[:budget]
		}
		if start > cursor {
			out.Gaps = append(out.Gaps, sessionOutputGapResult{
				Start:  cursor,
				End:    start,
				Reason: sessionOutputGapReason(cursor, start, rec.Gaps),
			})
		}
		out.Runs = append(out.Runs, sessionOutputRunResult{Offset: start, Body: body})
		budget -= len(body)
		cursor = start + uint64(len(body))
	}
	return out
}

// sessionOutputGapReason names why [start,end) is missing, in the store's
// own word where the store has one. The fallback is the bound's word rather
// than a generic one: the retention cap is the only thing that drops bytes
// from a recording, so a hole with no recorded reason is still that hole —
// and a vaguer word would tell a reader less than the code already knows.
func sessionOutputGapReason(start, end uint64, gaps []content.Gap) string {
	for _, g := range gaps {
		if g.Start < 0 || g.End < 0 {
			continue
		}
		// Offsets are byte counts of a stream one process produced; the
		// store holds them signed and they cannot be negative here.
		gs, ge := uint64(g.Start), uint64(g.End) //nolint:gosec
		if gs < end && ge > start && g.Reason != "" {
			return g.Reason
		}
	}
	return string(content.TruncCap)
}

// validateSessionOutputRaw is the registered validator for session.output.
// The sessionId is server-minted, so the 32-hex shape is the honest check —
// an id that is not one can never resolve — and a rejected request never
// reaches the registry or the store.
func validateSessionOutputRaw(raw json.RawMessage) string {
	var p sessionOutputParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	if p.InstanceID != "" {
		if msg := validateSessionIDShape(p.InstanceID); msg != "" {
			return "instanceId " + msg
		}
	}
	if p.SessionEpoch != nil && *p.SessionEpoch == 0 {
		return "sessionEpoch starts at 1"
	}
	return ""
}
