package transport

// run — the broker's run request (nocx-tjppv, design §4.1): the agent runs
// a command through the same submit path a person uses. The request travels
// broker -> renderer as agent.runRequest; the renderer submits the command
// through its ordinary orchestration (block, ledger entry, attempt, output
// artifact — all minted at submit, at the renderer), waits for the
// completion, and answers agent.runResolved with the entry id, the exit
// status and a window of the output. The backend never writes to the PTY
// (design §2.1 — rejected, not open for re-litigation): session.Write
// exists and is the shortest path, and it is deliberately not used, because
// a byte written straight to the pty would exist with no entry — a second
// input surface, and an invisible one.
//
// The broker mechanism (request_broker.go) owns id minting, correlation,
// timeouts and terminalization; this file is one RequestKind plus the
// WSServer's seams for it: the Conns snapshot, the per-connection Deliver,
// the read-loop Resolve registration and the ConnectionLost signal in
// connection teardown (ws.go) — the same four seams readScreen uses, with a
// different effect and a different resolution payload.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// runRequestTimeout bounds one run request WHEN THE LEASE IS DISABLED
// (every bound zero in the server's RunLeaseConfig): the pre-lease
// fallback, kept so the path before ADR-0020 decision 2 is still bounded.
// Under the lease — the production state — the wall-clock deadline IS the
// bound (and the escalation kills the execution rather than abandoning
// it), so RequestRun sets the kind's Timeout to 0: a broker timeout that
// could fire before the lease would recreate the wedged-command gap this
// bead closes.
const runRequestTimeout = 10 * time.Minute

// maxRunOutputWindowChars is the renderer-side clamp on the output window
// text one run resolution carries: the model reads this much output per
// command, and the honest window statement says how much more the block
// holds. The output budget of the lease (ADR-0020 decision 2) is its own
// bead; this is the wire bound that exists today.
const maxRunOutputWindowChars = 64 << 10 // 64 KiB of output text

// errRunNoRenderer is the run kind's no-client answer: there is no renderer
// attached to submit the command. Named, the way readScreen names its
// no-client outcome.
var errRunNoRenderer = errors.New("no renderer connected to run the command")

// ── wire shapes ────────────────────────────────────────────────────────────

// runRequestParams is what the broker sends the renderer (with the minted
// requestId merged in — marshalWithRequestID). sessionId is the lane the
// command runs in (already narrowed by the run's grant before the request
// was sent); command is exactly what a person would type.
type runRequestParams struct {
	SessionID string `json:"sessionId"`
	Command   string `json:"command"`
}

// runResolvedParams is the renderer's answer: a closed outcome —
// "completed", carrying the run body (entry id, exit status, output
// window), or "failed", carrying why. The run body's status is the block's
// own frozen status vocabulary (success | failure | entered | unknown); an
// entered block (an environment transition — the local `ssh` block) carries
// no exit code, honestly.
//
// AND IT CARRIES NO ARRANGEMENT (nocx-h1l4o). The command this names is
// joined to the turn that ran it — a `caused-by` edge with a position inside
// that turn — and none of that is on this wire. The backend owns the run: it
// is holding the turn's entry id already (askRunContext.entryID, beside the
// run id it passes to the pipeline) and it receives the command's entry id
// here, so it makes the join itself. This is ledger.open's paneId precedent
// applied to a relation instead of an anchor, and for the same reason stated
// there: a second copy on the wire would put one fact under a second owner,
// and the renderer's copy would be the one nobody checked.
// THE ENTRY ID IS THE STORE'S (nocx-9sqii). A command the renderer submits
// becomes a ledger row when its record is written, and that row's id is the
// only id anything here can use: the caused-by edge above is a foreign key
// into entries, so an id minted anywhere else is refused by the store and
// the relation is lost in a log line. Empty is the honest answer when the
// store wrote no row at all.
type runResolvedParams struct {
	RequestID string `json:"requestId"`
	Outcome   string `json:"outcome"` // "completed" | "failed"
	Error     string `json:"error,omitempty"`
	EntryID   string `json:"entryId"`
	ExitCode  *int   `json:"exitCode"`
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Text      string `json:"text"`
}

// runResolvedBody is the resolved result the broker's Request decodes into:
// the run body only, requestId and outcome consumed by the correlation. The
// assistant executor reads this shape (its own minimal consumer view) to
// build the tool's windowed return.
type runResolvedBody struct {
	EntryID  string `json:"entryId"`
	ExitCode *int   `json:"exitCode"`
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Text     string `json:"text"`
}

// ── the kind ──────────────────────────────────────────────────────────────

// runKind is the request exchange for one command submission. The renderer
// half of the wire contract lives here, as data — the mechanism's
// RequestKind design — so the WSServer's RequestRun writes no request
// machinery. The resolution bound is budgetDocument: the output window
// legitimately carries a large text payload (bounded by the renderer's
// maxRunOutputWindowChars clamp and this wire budget), far beyond the 1 KiB
// that bounds the closed-outcome resolvers.
func runKind() RequestKind {
	return RequestKind{
		NotifyMethod:       "agent.runRequest",
		ResolveMethod:      "agent.runResolved",
		NoClientErr:        errRunNoRenderer,
		Timeout:            runRequestTimeout,
		MaxResolutionBytes: budgetDocument,
		Resolve:            resolveRun,
	}
}

// resolveRun maps an accepted resolution to the run body result, or to the
// terminal error of a failed submission. The outcome was validated on the
// ingress (validateRunResolvedRaw), so this is the meaning of the outcome,
// not a second shape check.
func resolveRun(raw json.RawMessage) (json.RawMessage, error) {
	var p runResolvedParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("run: resolution: %w", err)
	}
	if p.Outcome == "failed" {
		return nil, fmt.Errorf("run: the renderer could not run the command: %s", p.Error)
	}
	body, err := json.Marshal(runResolvedBody{
		EntryID: p.EntryID, ExitCode: p.ExitCode, Status: p.Status,
		Total: p.Total, Start: p.Start, End: p.End, Text: p.Text,
	})
	if err != nil {
		return nil, fmt.Errorf("run: body: %w", err)
	}
	return body, nil
}

// ── ingress validation ─────────────────────────────────────────────────────

// validateRunResolvedRaw is the resolution's per-field shape check, applied
// on the read-loop ingress before the broker consumes the request: the
// closed outcome, the run body when the outcome is completed (entry id,
// status vocabulary, span within the block), the failure sentence when it
// is failed. A refused resolution leaves the pending request in place for a
// corrected retry.
func validateRunResolvedRaw(raw json.RawMessage) string {
	var p runResolvedParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	switch p.Outcome {
	case "completed":
		if p.Error != "" {
			return "a completed outcome carries no error"
		}
		// An EMPTY entry id is a real answer, not a malformed one: the
		// store wrote no row for this command (History is off, or the
		// record was dropped), so the renderer has no entry to name
		// (nocx-9sqii). The command ran either way and its output is the
		// tool's result; refusing the resolution here would leave the
		// request pending to the broker's timeout and fail a command that
		// had already executed, over a relation that is an arrangement.
		// What must stay bounded is an id that IS there.
		if utf8.RuneCountInString(p.EntryID) > maxIDRunes {
			return "an entry id must be within the id length bound"
		}
		switch p.Status {
		case "success", "failure", "entered", "unknown":
		default:
			return "status must be one of success, failure, entered, unknown"
		}
		if p.Total < 0 || p.Start < 0 || p.End < p.Start || p.End > p.Total {
			return "the returned window must be a span inside [0, total]"
		}
		if utf8.RuneCountInString(p.Text) > maxRunOutputWindowChars {
			return "the output window text exceeds the length bound"
		}
		return ""
	case "failed":
		if p.Error == "" {
			return "a failed outcome requires an error"
		}
		if utf8.RuneCountInString(p.Error) > maxResolutionErrorRunes {
			return "error exceeds the length bound"
		}
		return ""
	default:
		return "outcome must be one of completed, failed"
	}
}

// ── the WSServer seams ─────────────────────────────────────────────────────

// RequestRun implements assistant.RendererRequester: the transport side of
// the run tool. The grant has already narrowed the session (the capability
// the executor holds refuses out-of-grant sessions BEFORE this call), so
// the request names only the lane the run may use. The broker mints the
// request id, delivers the notification to every attached renderer and
// waits for the resolution — under the lease (ADR-0020 decision 2): a
// wall-clock deadline, an inactivity deadline and an output budget bound
// the execution, and a bound that fires escalates INT → TERM → KILL against
// the execution's process group before the request is terminalized, so a
// command that never finishes is killed, not merely abandoned.
func (s *WSServer) RequestRun(ctx context.Context, sessionID string, command string) (json.RawMessage, error) {
	if s.broker == nil {
		return nil, errors.New("run: no renderer request broker is wired")
	}
	// The lane's awaiting-takeover transition (ADR-0020 decision 3) is
	// decided HERE, in Go, from the renderer's buffer-kind report: while a
	// program owns the terminal the agent is demoted, not evicted — it
	// loses write authority, so a new run is refused; reading
	// (RequestScreen) is untouched.
	sid := session.ID(sessionID)
	if s.laneInteractivity.awaitingTakeover(sid) {
		return nil, errors.New("run: the lane is awaiting takeover — the agent may not write while a program owns the terminal")
	}

	cfg := s.effectiveRunLease()
	kind := runKind()
	if cfg.WallClock <= 0 && cfg.Inactivity <= 0 && cfg.OutputBudget <= 0 {
		// Lease disabled: the pre-lease broker bound applies unchanged.
		kind.Timeout = runRequestTimeout
	} else {
		// The lease is the ONLY bound. A broker timeout that could fire
		// before the lease — or while the lease is suspended by a takeover,
		// when the human legitimately owns the terminal past every bound —
		// would terminalize the run without killing the execution: the
		// exact gap this bead closes.
		kind.Timeout = 0
	}

	lease := s.newRunLease(sid, cfg)
	var body json.RawMessage
	err := lease.supervise(ctx, func(ctx context.Context) error {
		return s.broker.Request(ctx, kind, runRequestParams{SessionID: sessionID, Command: command}, &body)
	})
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return body, nil
}

// ── agent.laneInteractivity — the renderer's interactivity report ─────────

// laneInteractivityParams is the renderer's report of the lane's buffer
// kind (ADR-0020 decision 3): the one interactivity fact the backend cannot
// see for itself (AD-6 forbids sniffing the byte stream; the renderer owns
// the grid and its buffer kind). bufferKind is the capture identity's own
// vocabulary — "normal" | "alternate" — reported on every buffer change.
type laneInteractivityParams struct {
	SessionID  string `json:"sessionId"`
	BufferKind string `json:"bufferKind"`
}

// validateLaneInteractivityRaw is the ingress shape check: a bounded
// session id and the closed buffer-kind vocabulary. A malformed report is
// refused, never silently defaulted — the transition it would have caused
// is a decision the backend makes from facts it can trust.
func validateLaneInteractivityRaw(raw json.RawMessage) string {
	var p laneInteractivityParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if p.SessionID == "" || utf8.RuneCountInString(p.SessionID) > maxIDRunes {
		return "sessionId is required and bounded"
	}
	switch p.BufferKind {
	case "normal", "alternate":
		return ""
	default:
		return "bufferKind must be one of normal, alternate"
	}
}

// laneInteractivitySpec registers agent.laneInteractivity on the
// ingress-critical immediate set: the report feeds the lane state the run
// lease waits on, so it must never wait behind the control lane — a queued
// report would delay the awaiting-takeover transition, and a lease that
// has not seen the transition would keep enforcing its bounds on a TUI the
// human now owns. The handler is a mutex-guarded state update — exactly
// the microseconds immediate exists for.
func (s *WSServer) laneInteractivitySpec(immediate control.ImmediateSubmission) methodSpec {
	return reg(immediate, "agent.laneInteractivity", params(validateLaneInteractivityRaw),
		func(w *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				var p laneInteractivityParams
				if err := json.Unmarshal(req.Params, &p); err != nil {
					_ = w.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
					return
				}
				if msg := validateLaneInteractivityRaw(req.Params); msg != "" {
					_ = w.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + msg})
					return
				}
				s.laneInteractivity.note(session.ID(p.SessionID), p.BufferKind)
				_ = r.TryResult(req.ID, json.RawMessage(`{}`))
			}
		})
}

// runResolutionSpec registers agent.runResolved alongside readScreen's
// resolution: the broker's Resolve on the read-loop ingress (see
// brokerSpecs in ws_readscreen.go for the disposition).
func (s *WSServer) runResolutionSpec(immediate control.ImmediateSubmission) methodSpec {
	return reg(immediate, "agent.runResolved", params(validateRunResolvedRaw),
		func(w *wsConn, _ *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				perr := s.broker.Resolve("agent.runResolved", req.Params, w)
				if perr.Code != 0 {
					_ = w.TryError(req.ID, perr)
					return
				}
				_ = w.TryResult(req.ID, json.RawMessage(`{}`))
			}
		})
}
