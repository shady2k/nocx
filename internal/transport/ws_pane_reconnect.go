package transport

// pane.reconnect — the one action a person takes on a pane's connection, and the
// one path that carries it out (design §5.3, §5.4, §5.6; nocx-oalo1.5.4).
//
// # Why this is not the renderer's open again
//
// Until now a reconnect was the renderer calling `open` a second time for the
// same pane, with its own attempt count, its own backoff and its own reading of
// whether a retry could help. ADR-ba6xet1b makes that a second owner of a
// connection fact. So the backend answers it here, and it is deliberately not a
// second open: `openInto` is the one open path, and this runs it with one extra
// instruction — close the pane's connection first, because a stalled one is
// still pooled (design §5.3, "Reconnect closes the socket first").
//
// # What is closed, and what is opened
//
// The OLD SESSION goes first, through the same closer the layout uses when a
// pane leaves it: the id is retired, the exit is marked expected, and the
// transport tears the session down. Then the open runs, which asks the helper
// to close the pane's connection before it dials and gives the pane a new shell
// at the same endpoint. Nothing about the old shell is replayed, and no command
// is re-run: what comes back is a new shell under a divider the renderer draws
// (nocx-oalo1.5.5), exactly as the renderer's own reconnect did before.
//
// # What this does not decide
//
// WHETHER an offer stands is the policy owner's answer (internal/panedriver),
// published on pane.connection; this file only refuses a press when the offer it
// last told the renderer is not standing, and when the epoch is stale. Whether
// to reconnect by itself under `auto` is the same owner's decision; the seam
// below is how it tells this package that a press is already being carried out,
// so the automatic retry does not race the person's.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/log"
)

// PaneReconnections is the policy owner's notice of a reconnect in flight.
//
// It is a seam this package DECLARES and the composition root adapts to
// internal/panedriver (design §5.1.1): the transport knows how to carry a
// reconnect out, and panedriver knows whether one should happen — including its
// own automatic retry, which must not start a second one while a press is
// running. A nil seam is a server with no connection policy wired; every offer
// is then absent, because only the policy owner publishes one.
type PaneReconnections interface {
	// ReconnectStarted notes that this pane's reconnect is being carried out.
	// It must be cheap and must not block: it runs inside the request.
	ReconnectStarted(paneID string)
	// ReconnectFinished ends that interval, whether the reconnect succeeded or
	// not. It is safe to call once per Started.
	ReconnectFinished(paneID string)
}

// WithPaneReconnections wires the connection policy's in-flight notice.
func WithPaneReconnections(p PaneReconnections) WSServerOption {
	return func(s *WSServer) { s.paneReconnections = p }
}

// erPaneNotBound is a reconnect for a pane this connection did not open.
var errPaneNotBound = errors.New("transport: this pane has no open behind it")

// paneReconnectParams is the request, declared once
// (contracts/pane.reconnect.params.schema.json) and pinned by the contract
// tests. The epoch is the one from the notification that offered the button: a
// press carrying any other epoch is about a connection state the person is no
// longer looking at.
type paneReconnectParams struct {
	PaneID          string `json:"paneId"`
	ConnectionEpoch uint64 `json:"connectionEpoch"`
}

// closePaneSessions ends every session that is the pipe of one pane, through
// the same closer the layout uses. Split out of closeSessionsForPanes so both
// callers run one sequence rather than two that could drift.
func (s *WSServer) closePaneSessions(paneID string) {
	if paneID == "" {
		return
	}
	for _, sess := range s.registry.List() {
		if sess.PaneID() != paneID {
			continue
		}
		sid := sess.ID()
		s.closeLane(sid)
		s.markCloseRequested(sid)
		if err := s.registry.EndSession(sid); err != nil {
			s.log.Warn("closing the session a reconnect replaces", "session_id", string(sid), "error", err)
		}
		s.closeSession(sid, sess)
	}
}

// ReconnectPane carries out one pane's reconnect: the pane's old session is
// closed, its connection is closed by the helper, and a new shell is opened for
// the same pane at the same endpoint. It is the backend's own door — the
// automatic retry under `auto` — and it learns the outcome the same way the
// person's press does: from pane.connection.
func (s *WSServer) ReconnectPane(ctx context.Context, paneID string) error {
	_, err := s.reconnectPaneWithAck(ctx, paneID, nil)
	return err
}

// reconnectPaneWithAck is the one implementation of the action. The RPC calls
// it with the responder as its acknowledgement; the automatic retry passes
// none, because the new session reaches it on pane.connection.
func (s *WSServer) reconnectPaneWithAck(ctx context.Context, paneID string, ack func(openResult)) (openResult, error) {
	rec := s.paneRecordOf(paneID)
	if rec == nil {
		return openResult{}, errPaneNotBound
	}
	if s.opener == nil {
		return openResult{}, errOpenShuttingDown
	}
	// THE OLD SESSION GOES FIRST, and its connection with it: the open that
	// follows is told this is a reconnect, which closes the helper's side
	// before anything dials.
	s.closePaneSessions(paneID)
	rec.state.mu.Lock()
	for sid := range rec.state.sessions {
		if sess, ok := rec.state.sessions[sid]; ok && sess.PaneID() == paneID {
			delete(rec.state.sessions, sid)
		}
	}
	rec.state.mu.Unlock()

	h := openHandlers{opener: s.opener, sess: s, log: s.log, intents: s.paneIntentSource}
	if ack == nil {
		ack = func(openResult) {}
	}
	return h.openInto(ctx, rec.wconn, rec.state, rec.params, true, ack)
}

// paneReconnectHandlers answers `pane.reconnect`. It holds the connection's
// state and identity rather than the whole server, like every other handler.
type paneReconnectHandlers struct {
	machine *WSServer
	r       Responder
	w       *wsConn
}

// handleReconnect answers `pane.reconnect`: validate the press against the
// offer the backend last published, then carry it out on the one path.
func (h paneReconnectHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	var params paneReconnectParams
	if msg := decodeObject(req.Params, &params, "paneId", "connectionEpoch"); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if msg := requireFields(req.Params, "paneId", "connectionEpoch"); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if msg := validateStringBound("paneId", params.PaneID, maxIDRunes); msg != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+msg))
		return
	}
	if params.ConnectionEpoch == 0 {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: connectionEpoch must be at least 1"))
		return
	}

	rec := h.machine.paneRecordOf(params.PaneID)
	if rec == nil || rec.wconn != h.w {
		// A pane this connection did not open has no offer it could be
		// pressing: the notification that carried the button went somewhere
		// else.
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: unknown paneId"))
		return
	}
	h.machine.paneMu.Lock()
	lastEpoch, lastOffer := h.machine.paneEpochs[params.PaneID], rec.offer
	h.machine.paneMu.Unlock()
	if params.ConnectionEpoch != lastEpoch {
		// A press for an older observation is about a state the person is no
		// longer looking at. Refusing it is what stops a queue of presses
		// from opening a shell each.
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: stale connectionEpoch"))
		return
	}
	if !lastOffer {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: no reconnect is offered for this pane"))
		return
	}

	if seam := h.machine.paneReconnections; seam != nil {
		seam.ReconnectStarted(params.PaneID)
		defer seam.ReconnectFinished(params.PaneID)
	}
	acknowledged := false
	responded := func(result openResult) {
		acknowledged = true
		raw, _ := json.Marshal(result)
		_ = respond(h.r, newJSONRPCResult(req.ID, raw))
	}
	result, err := h.machine.reconnectPaneWithAck(ctx, params.PaneID, responded)
	if err != nil {
		// The reconnect failed before it could answer. The pane is not lost to
		// the person: the failure is the next observation's to say, and the
		// offer will stand again if it still should.
		log.From(ctx).Warn("pane reconnect failed", "pane_id", params.PaneID, "error", err)
		_ = respond(h.r, newJSONRPCError(req.ID, -32603, "Internal error: the reconnect did not open a shell"))
		return
	}
	if !acknowledged {
		raw, _ := json.Marshal(result)
		_ = respond(h.r, newJSONRPCResult(req.ID, raw))
	}
}
