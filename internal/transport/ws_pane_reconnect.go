package transport

// pane.reconnect registers one operation on the connection supervisor. The
// supervisor closes the socket before dialing; its return event replaces each
// held shell through the existing open path. The RPC never waits for a shell.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/session"
)

// PaneConnectionPolicy receives pane lifetime notices. The composition root
// releases the helper supervisor hold and the driver binding together.
type PaneConnectionPolicy interface {
	// PaneClosed says this pane no longer exists. The policy owner drops its
	// binding — and with it any automatic retry — because an observation of a
	// connection the pane no longer rides has nobody to be about.
	PaneClosed(paneID string)
}

// paneShellPolicy is the pane action owner's failure and retry boundary.
type paneShellPolicy interface {
	ShellFailed(context.Context, string, uint64, int, string, any)
	RequestShellRetry(context.Context, string, uint64) error
}

// WithPaneConnectionPolicy wires the connection policy's notices.
func WithPaneConnectionPolicy(p PaneConnectionPolicy) WSServerOption {
	return func(s *WSServer) { s.paneConnectionPolicy = p }
}

// The reconnect's refusals, each a different thing for a surface to do.
var (
	// errPaneNotBound is a reconnect for a pane this connection did not open.
	errPaneNotBound = errors.New("transport: this pane has no open behind it")
	// errPaneReconnectBusy is a second reconnect while one is still dialing.
	errPaneReconnectBusy = errors.New("transport: a reconnect is already running for this pane")
)

// paneOpened is an open answer delivered on the pane's owning socket.
type paneOpened struct {
	PaneID     string     `json:"paneId"`
	Generation uint64     `json:"generation"`
	Open       openResult `json:"open"`
}

type paneReconnectResult struct {
	PaneID     string `json:"paneId"`
	Registered bool   `json:"registered"`
}

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

// ReconnectPane replaces a held shell after the supervisor restores transport.
func (s *WSServer) ReconnectPane(ctx context.Context, paneID string, expectedBinding uint64) (resultErr error) {
	s.paneMu.Lock()
	rec := s.paneRecords[paneID]
	if rec == nil || expectedBinding == 0 || rec.bindingGeneration != expectedBinding {
		s.paneMu.Unlock()
		return errPaneNotBound
	}
	if rec.registering != nil {
		registered := rec.registering
		s.paneMu.Unlock()
		select {
		case <-registered:
			return s.ReconnectPane(ctx, paneID, expectedBinding)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if rec.busy {
		s.paneMu.Unlock()
		return errPaneReconnectBusy
	}
	rec.busy = true
	attemptGeneration := expectedBinding
	s.paneMu.Unlock()
	defer func() { s.paneMu.Lock(); rec.busy = false; s.paneMu.Unlock() }()
	if s.opener == nil {
		return errOpenShuttingDown
	}
	s.paneMu.Lock()
	if s.paneRecords[paneID] != rec || rec.bindingGeneration != expectedBinding || ctx.Err() != nil || rec.ctx.Err() != nil {
		s.paneMu.Unlock()
		return context.Canceled
	}
	defer func() {
		if resultErr == nil || ctx.Err() != nil || rec.ctx.Err() != nil {
			return
		}
		s.paneMu.Lock()
		live := s.paneRecords[paneID] == rec
		s.paneMu.Unlock()
		if !live {
			return
		}
		if policy, ok := s.paneConnectionPolicy.(paneShellPolicy); ok {
			capture := &openFailureCapture{}
			(openHandlers{}).answerOpenFailure(ctx, capture, jsonrpcRequest{}, resultErr)
			policy.ShellFailed(rec.ctx, paneID, attemptGeneration, capture.failure.Code, capture.failure.Message, capture.failure.Data)
		}
	}()
	var replaced []session.Session
	for _, sess := range s.registry.List() {
		if sess.PaneID() == paneID {
			replaced = append(replaced, sess)
		}
	}
	rec.state.mu.Lock()
	for _, sess := range replaced {
		delete(rec.state.sessions, sess.ID())
	}
	rec.state.mu.Unlock()
	s.paneMu.Unlock()
	// Only the captured lifetime's sessions may end. A new pane with the same
	// id can open while an external Close is waiting.
	for _, sess := range replaced {
		sid := sess.ID()
		s.closeLane(sid)
		s.markCloseRequested(sid)
		if err := s.registry.EndSession(sid); err != nil {
			s.log.Warn("closing the session a reconnect replaces", "session_id", string(sid), "error", err)
		}
		s.closeSession(sid, sess)
	}
	h := openHandlers{opener: s.opener, sess: s, log: s.log, intents: s.paneIntentSource}
	openCtx, cancel := context.WithCancel(rec.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	bindCtx := context.WithValue(openCtx, paneBindingKey{}, func(generation uint64) {
		attemptGeneration = generation
	})
	_, err := h.openInto(context.WithValue(bindCtx, paneReconnectOpenKey{}, rec), rec.wconn, rec.state, rec.params, func(result openResult) {
		s.paneMu.Lock()
		defer s.paneMu.Unlock()
		if s.paneRecords[paneID] != rec || rec.bindingGeneration != attemptGeneration || openCtx.Err() != nil || rec.ctx.Err() != nil {
			cancel()
			return
		}
		if err := rec.wconn.TryNotify("pane.opened", mustMarshal(paneOpened{PaneID: paneID, Generation: attemptGeneration, Open: result})); err != nil {
			cancel()
		}
	})
	return err
}

// helperReconnectRegistrar registers the supervisor operation, not a shell.
type helperReconnectRegistrar interface {
	RegisterReconnect(context.Context, session.Config, uint64) error
}

func (o *sessionOpener) registerReconnect(ctx context.Context, params openParams, sequence uint64) error {
	if _, err := o.workspaceForOpen(ctx, params.PaneID); err != nil {
		return refuse(-32602, "Invalid params: "+fmt.Sprint(err))
	}
	cfg := session.Config{PaneID: params.PaneID, Kind: session.KindRemote}
	spec := OpenSpec{PaneID: params.PaneID, Kind: params.Kind, ProfileID: params.ProfileID, Host: params.Host, User: params.User, DesiredMode: params.DesiredMode, Shell: params.Shell}
	if err := o.op.Prepare(ctx, func(ctx context.Context, svc capability.OpenService) error {
		return o.resolveRemote(ctx, svc, spec, &cfg)
	}); err != nil {
		return err
	}
	registrar, ok := o.helper.(helperReconnectRegistrar)
	if !ok {
		return refuse(-32603, "SSH reconnect is unavailable (no connection supervisor wired)")
	}
	return registrar.RegisterReconnect(ctx, cfg, sequence)
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

	h.machine.paneMu.Lock()
	rec := h.machine.paneRecords[params.PaneID]
	var sequence uint64
	var shellOnly bool
	var refusal string
	switch {
	case rec == nil || rec.wconn != h.w:
		refusal = "unknown paneId"
	case params.ConnectionEpoch != h.machine.paneEpochs[params.PaneID]:
		refusal = "stale connectionEpoch"
	case !rec.offer:
		refusal = "no reconnect is offered for this pane"
	case rec.busy:
		refusal = "a reconnect is already running for this pane"
	default:
		shellOnly = rec.shellFailure
		rec.busy = true
		rec.registering = make(chan struct{})
		sequence = rec.connectionSequence
	}
	h.machine.paneMu.Unlock()
	if refusal != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+refusal))
		return
	}
	defer func() {
		h.machine.paneMu.Lock()
		rec.busy = false
		close(rec.registering)
		rec.registering = nil
		h.machine.paneMu.Unlock()
	}()
	if shellOnly {
		policy, ok := h.machine.paneConnectionPolicy.(paneShellPolicy)
		if !ok {
			(openHandlers{}).answerOpenFailure(ctx, h.r, req, refuse(-32603, "Shell retry is unavailable (no pane driver wired)"))
			return
		}
		if err := policy.RequestShellRetry(rec.ctx, params.PaneID, params.ConnectionEpoch); err != nil {
			(openHandlers{}).answerOpenFailure(ctx, h.r, req, err)
			return
		}
		raw, _ := json.Marshal(paneReconnectResult{params.PaneID, true})
		_ = respond(h.r, newJSONRPCResult(req.ID, raw))
		return
	}
	if h.machine.opener == nil {
		(openHandlers{}).answerOpenFailure(ctx, h.r, req, errOpenShuttingDown)
		return
	}
	registerCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(rec.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := h.machine.opener.registerReconnect(registerCtx, rec.params, sequence); err != nil {
		(openHandlers{}).answerOpenFailure(ctx, h.r, req, err)
		return
	}
	raw, _ := json.Marshal(paneReconnectResult{params.PaneID, true})
	_ = respond(h.r, newJSONRPCResult(req.ID, raw))
}

// openFailureCapture reuses the synchronous open's actionable error mapping.
type openFailureCapture struct{ failure RPCError }

func (c *openFailureCapture) TryResult(json.RawMessage, json.RawMessage) error { return nil }
func (c *openFailureCapture) TryNotify(string, json.RawMessage) error          { return nil }
func (c *openFailureCapture) TryError(_ json.RawMessage, e RPCError) error     { c.failure = e; return nil }
