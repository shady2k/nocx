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

// paneShellPolicy is the pane action owner's failure and reopen boundary.
type paneShellPolicy interface {
	// retryable says the refusal is a WAIT rather than an answer (design
	// §5.4, C16): nobody could be asked whether the shell is still there, so
	// the automatic attempt loop keeps trying instead of leaving the pane to a
	// person's press.
	ShellFailed(ctx context.Context, paneID string, generation uint64, code int, message string, data any, retryable bool)
	RequestReopen(context.Context, string, uint64) error
}

// PaneShellWait is the refusal a reattacher answers when it could not ASK: no
// connection to the helper, an inventory that timed out, a daemon that would
// not answer. It is not a verdict about the shell (C16's whole point) and not
// something a person can fix: it is the state in which the pane waits and the
// automatic attempt loop tries again.
type PaneShellWait struct {
	Reason string
}

func (w *PaneShellWait) Error() string { return w.Reason }

// WithPaneConnectionPolicy wires the connection policy's notices.
func WithPaneConnectionPolicy(p PaneConnectionPolicy) WSServerOption {
	return func(s *WSServer) { s.paneConnectionPolicy = p }
}

// PaneShellRef names the helper-held shell a pane had before its transport
// was lost, as far as the TRANSPORT knows it: which shell, which helper
// generation may answer about it, and the host and account that shell was on.
//
// IT IS THE PANE'S MEMORY, NOT A ROUTE. The route itself belongs to the
// coordinator — a far helper's hosted carrier holds the options the open
// dialled with, and a shell this machine's helper holds is described by the
// binding its open recorded. What travels here is what the pane record already
// keeps so the question can be asked at all: a transport loss removes the
// session object, and with it everything that named the shell.
type PaneShellRef struct {
	Session    session.ID
	Host       string
	Account    string
	Generation string
}

// PaneShellReattach is one shell taken back: the session the helper still
// holds, the facts a caller finishes its half with, and nothing else — the
// transport builds the pane's answer itself, from the session it is given.
type PaneShellReattach struct {
	Session session.Session
	Config  session.Config
	Hosted  HostedSessionOpen
}

// PaneShellReattacher is the coordinator's half of design §5.4's first answer:
// "the far helper holds the shell — reattach, same shell, no divider".
//
// THE TRANSPORT ASKS AND THE COORDINATOR ANSWERS, because the two own
// different halves: the transport knows which shell this pane had and builds
// the answer the renderer reads, while the helper connection, the inventory
// question and the adoption all belong to the process that opened it. Folding
// either half into the other would put a helper client in transport or a
// second open path in app, and both are what AD-8 forbids.
//
// THE ANSWER IS THREE-VALUED AND C16 IS WHY (ADR-ydcx00yd). "The helper answered and does not
// hold it" is the ONLY answer that justifies another shell; "nobody could be
// asked" must leave the pane waiting, because a slow helper would otherwise
// cost a person a second shell beside a live one.
type PaneShellReattacher interface {
	// ReattachPaneShell takes the shell spec.Reattach names back for this
	// open.
	//
	//	(r, true, nil)   taken back — the registry holds it again
	//	(zero, false, nil)  the helper ANSWERED and does not hold it
	//	(zero, false, err)  nobody could be asked, or the route no longer
	//	                    leads there: nothing may be opened
	//
	// It also BINDS the pane's restored connection, through
	// BindPaneConnection, and that is not optional: the generation it answers
	// is the one this open's answer carries, and an open whose binding did not
	// move is refused as one that no longer owns its pane.
	ReattachPaneShell(ctx context.Context, spec OpenSpec, cfg session.Config) (PaneShellReattach, bool, error)
}

// SetPaneShellReattacher attaches the coordinator's re-adoption half after the
// server exists, which is the order the composition root has: the reattacher
// adopts THROUGH this server (ReadoptHostedSession), so it cannot be built
// before it.
func (s *WSServer) SetPaneShellReattacher(r PaneShellReattacher) {
	s.paneShellReattacher = r
	if s.opener != nil {
		s.opener.reattacher = r
	}
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
	// NewShell says whether this replacement is a SECOND shell beside the one
	// the pane had (design §5.4): true when the previous shell was confirmed
	// gone and a new one was opened, false when the coordinator took the
	// running shell back and the person is where they were.
	//
	// IT IS THE DIVIDER'S OWN FACT, and it is deliberately not derived from
	// the session id: a reattach after a transport loss re-adopts the same
	// helper session and so keeps its id, but a helper that answers "gone"
	// and a coordinator that could not ask at all both replace a shell whose
	// id is new — only the second of which means the old shell may still be
	// running. The reattacher is the only thing that knows which happened.
	NewShell bool `json:"newShell"`
	// ReplayFrom is the output-stream offset this pane's next bytes begin at
	// (AD-9). A fresh shell answers 0; a shell taken back answers the offset
	// the re-attachment resumed the host's stream at, which is where the ring
	// on this side starts too. The renderer adopts it as its own cursor, so
	// one stream has one coordinate across a transport loss.
	ReplayFrom uint64 `json:"replayFrom"`
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
		s.endPaneSession(sess)
	}
}

func (s *WSServer) endPaneSession(sess session.Session) {
	if !s.claimClose(sess) {
		return
	}
	sid := sess.ID()
	defer s.releaseClose(sid)
	s.closeLane(sid)
	s.markCloseRequested(sid)
	if err := s.registry.EndSession(sid); err != nil {
		s.log.Warn("ending pane session", "session_id", string(sid), "error", err)
	}
	s.closeSession(sid, sess)
}

// ReconnectPane replaces a held shell after the supervisor restores transport.
func (s *WSServer) ReconnectPane(ctx context.Context, paneID string, expectedBinding uint64) (resultErr error) {
	// THE RECONNECT IS AN OPEN, and the transport entry a supervisor calls
	// reaches the opener directly rather than through a method submission.
	// Without this registration Stop could return while a reconnect was still
	// dialing, and the helper session it spawns would have no owner left to
	// close it. begin refuses work once shutdown has started.
	tctx, _, release, ok := s.inflight.begin(ctx)
	if !ok {
		return errOpenShuttingDown
	}
	defer release()
	ctx = tctx
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
			// A WAIT IS NOT A REFUSAL TO KEEP (design §5.4, C16). A helper
			// nobody could ask leaves the pane on the automatic loop; a route
			// that no longer leads there, or a helper that answered and would
			// not give the shell back, needs a person and stops.
			var waiting *PaneShellWait
			retryable := errors.As(resultErr, &waiting)
			policy.ShellFailed(rec.ctx, paneID, attemptGeneration, capture.failure.Code, capture.failure.Message, capture.failure.Data, retryable)
		}
	}()
	var replaced []session.Session
	for _, sess := range s.registry.List() {
		if sess.PaneID() == paneID {
			replaced = append(replaced, sess)
		}
	}
	// A PANE THAT MAY STILL HAVE A RUNNING SHELL IS NOT TORN DOWN FIRST
	// (§5.4, C16). Ending a helper-held session asks its helper to end the
	// SHELL, so end-then-open could only ever produce a replacement: the shell
	// would be gone before anything asked whether it was still there. Such a
	// pane keeps its sessions until the open has answered, and a session that
	// came back is the same id and is left alone.
	//
	// A pane with nothing to ask about — a local shell, a plain one, one that
	// never adopted a helper-held shell — replaces as it always did: end, then
	// open. The two orders are one question and it is asked once, here.
	askFirst := rec.reconnectShell() != nil
	if !askFirst {
		rec.state.mu.Lock()
		for _, sess := range replaced {
			delete(rec.state.sessions, sess.ID())
		}
		rec.state.mu.Unlock()
	}
	s.paneMu.Unlock()
	if !askFirst {
		// Only the captured lifetime's sessions may end, and only outside the
		// lock: ending one waits on its own teardown. A new pane with the same
		// id can open while an external Close is waiting.
		for _, sess := range replaced {
			s.endPaneSession(sess)
		}
		replaced = nil
	}
	h := openHandlers{opener: s.opener, sess: s, log: s.log, intents: s.paneIntentSource}
	openCtx, cancel := context.WithCancel(rec.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	bindCtx := context.WithValue(openCtx, paneBindingKey{}, func(generation uint64) {
		attemptGeneration = generation
	})
	answered := openResult{}
	_, err := h.openInto(context.WithValue(bindCtx, paneReconnectOpenKey{}, rec), rec.wconn, rec.state, rec.params, func(result openResult) error {
		// openInto holds the pane binding and shutdown fences across this
		// enqueue, just as it does for an RPC result.
		answered = result
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return rec.wconn.TryNotify("pane.opened", mustMarshal(paneOpened{
			PaneID: paneID, Generation: attemptGeneration, Open: result,
			NewShell: result.NewShell, ReplayFrom: result.ReplayFrom,
		}))
	})
	if err != nil {
		// NOTHING WAS ENDED, so a helper that could not answer — or that still
		// holds the shell and would not give it back — leaves the pane exactly
		// as it was: waiting, with the reason, and one shell.
		return err
	}
	// WHAT THE PANE HELD AND DID NOT GET BACK. A closed pane's session can
	// outlive the attempt that replaced it, so this runs on the captured
	// lifetime's sessions and skips the one the open answered with.
	for _, sess := range replaced {
		if answered.SessionID != "" && sess.ID() == session.ID(answered.SessionID) {
			// THE SAME SHELL CAME BACK, so it must not be ended: it is the
			// pane's shell, and ending it would close what the person is
			// typing into.
			continue
		}
		s.endPaneSession(sess)
	}
	return nil
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
	spec := OpenSpec{PaneID: params.PaneID, Kind: params.Kind, ProfileID: params.ProfileID, Host: params.Host, User: params.User, Shell: params.Shell}
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
	var reopen bool
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
		reopen = rec.reopen
		rec.busy = true
		rec.registering = make(chan struct{})
		sequence = rec.connectionSequence
	}
	h.machine.paneMu.Unlock()
	if refusal != "" {
		_ = respond(h.r, newJSONRPCError(req.ID, -32602, "Invalid params: "+refusal))
		return
	}
	// release ends the pane's guard, and it runs BEFORE the answer, never
	// after. The client may press again as soon as it reads the reply, and
	// that press arrives on another request; an answer written with the
	// guard still held refuses it with "a reconnect is already running" for
	// work that is already over. The flag makes the deferred call a no-op
	// once a branch has released, so a retry that has since taken the guard
	// is not released by its predecessor.
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		h.machine.paneMu.Lock()
		rec.busy = false
		close(rec.registering)
		rec.registering = nil
		h.machine.paneMu.Unlock()
	}
	defer release()
	if reopen {
		policy, ok := h.machine.paneConnectionPolicy.(paneShellPolicy)
		if !ok {
			release()
			(openHandlers{}).answerOpenFailure(ctx, h.r, req, refuse(-32603, "Shell retry is unavailable (no pane driver wired)"))
			return
		}
		if err := policy.RequestReopen(rec.ctx, params.PaneID, params.ConnectionEpoch); err != nil {
			release()
			(openHandlers{}).answerOpenFailure(ctx, h.r, req, err)
			return
		}
		release()
		raw, _ := json.Marshal(paneReconnectResult{params.PaneID, true})
		_ = respond(h.r, newJSONRPCResult(req.ID, raw))
		return
	}
	if h.machine.opener == nil {
		release()
		(openHandlers{}).answerOpenFailure(ctx, h.r, req, errOpenShuttingDown)
		return
	}
	registerCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(rec.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := h.machine.opener.registerReconnect(registerCtx, rec.params, sequence); err != nil {
		release()
		(openHandlers{}).answerOpenFailure(ctx, h.r, req, err)
		return
	}
	release()
	raw, _ := json.Marshal(paneReconnectResult{params.PaneID, true})
	_ = respond(h.r, newJSONRPCResult(req.ID, raw))
}

// openFailureCapture reuses the synchronous open's actionable error mapping.
type openFailureCapture struct{ failure RPCError }

func (c *openFailureCapture) TryResult(json.RawMessage, json.RawMessage) error { return nil }
func (c *openFailureCapture) TryNotify(string, json.RawMessage) error          { return nil }
func (c *openFailureCapture) TryError(_ json.RawMessage, e RPCError) error     { c.failure = e; return nil }
