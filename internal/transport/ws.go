package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// sessionRx wraps a session's output ring together with the current attached
// subscriber. It lives connection-independently so the ring survives a
// disconnect and a reattached connection becomes the new subscriber. Exactly
// one monitorExit goroutine is started per session (via monitorOnce).
type sessionRx struct {
	ring        *outputRing
	mu          sync.Mutex // protects subscriber, subState
	subscriber  *wsConn    // current attached connection (nil if none)
	subState    *connState // subscriber's connection-scoped state
	monitorOnce sync.Once
}

func (rx *sessionRx) setSubscriber(wconn *wsConn, state *connState) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	rx.subscriber = wconn
	rx.subState = state
}

func (rx *sessionRx) getSubscriber() (*wsConn, *connState) {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	return rx.subscriber, rx.subState
}

type WSServer struct {
	log      log.Logger
	registry session.Registry
	server   *http.Server
	port     int
	listener net.Listener
	upgrader websocket.Upgrader

	// ringsMu protects rx and stopped. One sessionRx per session;
	// keyed by session.ID. When stopped is true, getOrCreateRx returns nil
	// so no new rings are created after the server begins shutting down.
	ringsMu sync.Mutex
	rx      map[session.ID]*sessionRx
	stopped bool
}

func NewWSServer(logger log.Logger, reg session.Registry) *WSServer {
	return &WSServer{
		log:      logger,
		registry: reg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		rx: make(map[session.ID]*sessionRx),
	}
}

func (s *WSServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handleSession)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("ws listen: %w", err)
	}
	s.listener = listener
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return fmt.Errorf("ws listen: not a TCP address")
	}
	s.port = tcpAddr.Port

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 0,
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.Error("ws server error", "error", err)
		}
	}()

	s.log.Info("ws server started", "port", s.port)
	return nil
}

func (s *WSServer) Stop(ctx context.Context) error {
	// Mark stopped first so getOrCreateRx refuses new rings while
	// hijacked WebSocket handlers are still running. Do NOT nil s.rx —
	// the map stays usable for lookups until the final goroutine exits.
	s.ringsMu.Lock()
	s.stopped = true
	s.ringsMu.Unlock()

	// Shutdown stops accepting new connections but hijacked WebSocket
	// handlers can still run after it returns. That is why the stopped
	// flag, not map-nilling, is the safety mechanism.
	var shutdownErr error
	if s.server != nil {
		shutdownErr = s.server.Shutdown(ctx)
	}

	// Close all rings so blocked writers and waiters unblock.
	s.ringsMu.Lock()
	for _, rx := range s.rx {
		rx.ring.close()
	}
	s.ringsMu.Unlock()

	for _, sess := range s.registry.List() {
		_ = s.registry.Close(sess.ID())
	}

	return shutdownErr
}

func (s *WSServer) Port() int {
	return s.port
}

// --- ring helpers (connection-independent, keyed by session.ID) ----------

func (s *WSServer) getRx(id session.ID) *sessionRx {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()
	return s.rx[id]
}

func (s *WSServer) getOrCreateRx(id session.ID) *sessionRx {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()

	if s.stopped {
		return nil
	}

	if rx, ok := s.rx[id]; ok {
		return rx
	}
	rx := &sessionRx{ring: newOutputRing()}
	s.rx[id] = rx
	return rx
}

func (s *WSServer) removeRx(id session.ID) {
	s.ringsMu.Lock()
	defer s.ringsMu.Unlock()
	delete(s.rx, id)
}

// --- WebSocket connection -------------------------------------------------

// wsConn wraps a gorilla/websocket.Conn with a mutex to serialize writes.
// The gorilla package does not support concurrent writes — callers must
// serialize writes to a single *websocket.Conn. The mutex here provides that
// serialization across ringToConn, monitorExit, and handleOpen/handleAttach.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func newWSConn(conn *websocket.Conn) *wsConn {
	return &wsConn{conn: conn}
}

func (w *wsConn) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

func (w *wsConn) writeMessage(msgType int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(msgType, data)
}

// connState tracks sessions this connection is attached to (opened or
// reattached). On disconnect the entries are discarded — sessions and their
// rings survive (AD-9). It still gates data-frame/resize/close so a
// connection cannot touch a session it has not opened or reattached to.
type connState struct {
	mu       sync.Mutex
	sessions map[session.ID]session.Session
}

func newConnState() *connState {
	return &connState{sessions: make(map[session.ID]session.Session)}
}

func (c *connState) add(sess session.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sess.ID()] = sess
}

func (c *connState) remove(id session.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, id)
}

func (c *connState) has(id session.ID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sessions[id]
	return ok
}

// --- JSON-RPC types -------------------------------------------------------

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *jsonrpcErrorObj `json:"error,omitempty"`
}

type jsonrpcErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newJSONRPCError(id json.RawMessage, code int, msg string) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcErrorObj{Code: code, Message: msg},
	}
}

func newJSONRPCResult(id json.RawMessage, result json.RawMessage) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// isJSONObject returns true if data, ignoring leading whitespace, starts with
// an opening brace ('{').
func isJSONObject(data []byte) bool {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// openParams is the payload of the "open" RPC method.
type openParams struct {
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	XPixel uint16 `json:"xpixel"`
	YPixel uint16 `json:"ypixel"`
}

// resizeParams is the payload of the "resize" RPC method.
type resizeParams struct {
	SessionID string `json:"sessionId"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
	XPixel    uint16 `json:"xpixel"`
	YPixel    uint16 `json:"ypixel"`
}

// closeParams is the payload of the "close" RPC method.
type closeParams struct {
	SessionID string `json:"sessionId"`
}

// attachParams is the payload of the "attach" RPC method (AD-9 reconnect).
type attachParams struct {
	SessionID string `json:"sessionId"`
	Offset    uint64 `json:"offset"`
}

// ackParams is the payload of the "ack" notification (AD-9 trimming).
type ackParams struct {
	SessionID string `json:"sessionId"`
	Offset    uint64 `json:"offset"`
}

// --- HTTP handler ---------------------------------------------------------

func (s *WSServer) handleSession(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	// Derive a cancel context so that when handleSession returns,
	// ringToConn goroutines blocked in waitForData receive ctx.Done()
	// and exit. r.Context() is NOT reliably cancelled for hijacked
	// WebSocket connections.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	wconn := newWSConn(conn)
	state := newConnState()

	readErr := make(chan error, 1)
	go s.readLoop(ctx, wconn, state, readErr)

	<-readErr

	// Connection dropped. Wake any ring waiters blocked on this
	// connection's sessions. The cancel above also fires (via defer)
	// which is the primary exit signal for ringToConn.
	state.mu.Lock()
	for sid := range state.sessions {
		if rx := s.getRx(sid); rx != nil {
			rx.ring.wake()
		}
	}
	state.mu.Unlock()
}

func (s *WSServer) readLoop(ctx context.Context, wconn *wsConn, state *connState, readErr chan<- error) {
	defer func() { readErr <- nil }()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, data, err := wconn.conn.ReadMessage()
		if err != nil {
			return
		}

		switch msgType {
		case websocket.TextMessage:
			s.handleControlFrame(ctx, wconn, state, data)
		case websocket.BinaryMessage:
			s.handleDataFrame(state, data)
		}
	}
}

func (s *WSServer) handleControlFrame(ctx context.Context, wconn *wsConn, state *connState, data []byte) {
	if !isJSONObject(data) {
		s.log.Warn("jsonrpc invalid request", "data", string(data))
		resp := newJSONRPCError(json.RawMessage("null"), -32600, "Invalid Request")
		_ = wconn.writeJSON(resp)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.log.Warn("jsonrpc parse error", "data", string(data))
		resp := newJSONRPCError(json.RawMessage("null"), -32700, "Parse error")
		_ = wconn.writeJSON(resp)
		return
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		resp := newJSONRPCError(req.ID, -32600, "Invalid Request")
		_ = wconn.writeJSON(resp)
		return
	}

	switch req.Method {
	case "open":
		s.handleOpen(ctx, wconn, state, req)
	case "resize":
		s.handleResize(wconn, state, req)
	case "close":
		s.handleClose(wconn, state, req)
	case "attach":
		s.handleAttach(ctx, wconn, state, req)
	case "ack":
		s.handleAck(req)
	default:
		resp := newJSONRPCError(req.ID, -32601, "Method not found")
		_ = wconn.writeJSON(resp)
	}
}

// --- control-plane handlers -----------------------------------------------

// handleOpen creates a new session and output ring.
//
// Per AD-7: the server assigns the authoritative session-id. The JSON-RPC
// request id serves as the correlation-id — we do NOT add a second
// correlationId field, because two correlation identifiers for one exchange
// is redundant state with two owners.
func (s *WSServer) handleOpen(ctx context.Context, wconn *wsConn, state *connState, req jsonrpcRequest) {
	var params openParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Cols == 0 || params.Rows == 0 {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: cols and rows required")
		_ = wconn.writeJSON(resp)
		return
	}

	sess, err := s.registry.Open(ctx, session.Config{
		Kind:   session.KindLocal,
		Cols:   params.Cols,
		Rows:   params.Rows,
		XPixel: params.XPixel,
		YPixel: params.YPixel,
	})
	if err != nil {
		s.log.Error("failed to open session", "error", err)
		resp := newJSONRPCError(req.ID, -32603, "Internal error")
		_ = wconn.writeJSON(resp)
		return
	}

	state.add(sess)

	rx := s.getOrCreateRx(sess.ID())
	if rx == nil {
		state.remove(sess.ID())
		_ = s.registry.Close(sess.ID())
		resp := newJSONRPCError(req.ID, -32603, "Internal error: server shutting down")
		_ = wconn.writeJSON(resp)
		return
	}
	rx.setSubscriber(wconn, state)

	// cwd rides the open result so the tab has a name before any program sets
	// a title (nocx-9vr). It is the starting directory only — following `cd`
	// needs OSC 7 (nocx-5mn.2).
	result := map[string]string{
		"sessionId": string(sess.ID()),
		"cwd":       sess.Cwd(),
	}
	resultJSON, _ := json.Marshal(result)
	resp := newJSONRPCResult(req.ID, resultJSON)
	_ = wconn.writeJSON(resp)

	// Start the PTY → ring output pump only after the ack is sent.
	// AD-7: the ack must precede the session's own traffic in both
	// directions, otherwise the first prompt races the open result and
	// the client drops it (its sessionId is still null).
	// Uses background context so the pump outlives the connection (AD-9).
	go s.pumpToRing(context.Background(), sess, rx.ring)

	// Start exactly one monitorExit goroutine per session (DEFECT 2).
	rx.monitorOnce.Do(func() {
		go s.monitorExit(rx, sess)
	})

	sidBytes, _ := session.IDToBytes(sess.ID())
	go s.ringToConn(ctx, wconn, sidBytes, rx.ring, 0)
}

func (s *WSServer) handleResize(wconn *wsConn, state *connState, req jsonrpcRequest) {
	var params resizeParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" || params.Cols == 0 || params.Rows == 0 {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId, cols, and rows required")
		_ = wconn.writeJSON(resp)
		return
	}

	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	sess, err := s.registry.Get(sid)
	if err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	if err := sess.Resize(context.Background(), params.Cols, params.Rows, params.XPixel, params.YPixel); err != nil {
		resp := newJSONRPCError(req.ID, -32603, "Internal error")
		_ = wconn.writeJSON(resp)
		return
	}

	result, _ := json.Marshal(map[string]any{})
	resp := newJSONRPCResult(req.ID, result)
	_ = wconn.writeJSON(resp)
}

func (s *WSServer) handleClose(wconn *wsConn, state *connState, req jsonrpcRequest) {
	var params closeParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId required")
		_ = wconn.writeJSON(resp)
		return
	}

	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	s.closeSession(sid)

	state.remove(sid)

	result, _ := json.Marshal(map[string]any{})
	resp := newJSONRPCResult(req.ID, result)
	_ = wconn.writeJSON(resp)
}

// handleAttach reattaches a connection to a session's output ring at the
// given byte offset (AD-9 reconnect).
//
//	--> {"jsonrpc":"2.0","id":N,"method":"attach","params":{"sessionId":"...","offset":1234}}
//
// Result when offset is still in the ring:
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"resumed":true,"from":1234}}
//
// Result when offset is too old (ring has advanced past it):
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"reset":true,"from":5678}}
//
// Unknown sessionId → JSON-RPC error.
// Offset ahead of written → JSON-RPC error (DEFECT 4).
// Duplicate attach on the same connection → JSON-RPC error (DEFECT 3).
func (s *WSServer) handleAttach(ctx context.Context, wconn *wsConn, state *connState, req jsonrpcRequest) {
	var params attachParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId and offset required")
		_ = wconn.writeJSON(resp)
		return
	}

	sid := session.ID(params.SessionID)

	sess, err := s.registry.Get(sid)
	if err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	// Reject duplicate attach on the same connection (DEFECT 3).
	// Without this guard, handleOpen already started a ringToConn for the
	// open connection; a second attach on the same session would start
	// another ringToConn, doubling every output byte for that subscriber.
	if state.has(sid) {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: already attached to this session")
		_ = wconn.writeJSON(resp)
		return
	}

	rx := s.getRx(sid)
	if rx == nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	// Reject offsets that run ahead of what the ring has produced (DEFECT 4).
	// ring.ack already validates this; attach must be equally distrustful.
	// An offset > written means the client claims to have received bytes
	// that were never produced — a silent data skip waiting to happen.
	// Uses the locking accessor rather than reaching into the ring's mu.
	w := rx.ring.writtenLocked()
	if params.Offset > w {
		resp := newJSONRPCError(req.ID, -32602, fmt.Sprintf("Invalid params: offset %d exceeds written %d", params.Offset, w))
		_ = wconn.writeJSON(resp)
		return
	}

	_, from, needsReset := rx.ring.snapshot(params.Offset)

	state.add(sess)
	rx.setSubscriber(wconn, state)

	if needsReset {
		respJSON, _ := json.Marshal(map[string]any{"reset": true, "from": from})
		resp := newJSONRPCResult(req.ID, respJSON)
		_ = wconn.writeJSON(resp)
	} else {
		respJSON, _ := json.Marshal(map[string]any{"resumed": true, "from": from})
		resp := newJSONRPCResult(req.ID, respJSON)
		_ = wconn.writeJSON(resp)
	}

	sidBytes, _ := session.IDToBytes(sid)
	go s.ringToConn(ctx, wconn, sidBytes, rx.ring, from)
}

// handleAck processes an ack notification (AD-9 trimming).
//
//	<-- {"jsonrpc":"2.0","method":"ack","params":{"sessionId":"...","offset":1234}}
//
// Offsets that run ahead of what was produced or go backwards are rejected
// with a warn — the server never trusts the client blindly.
func (s *WSServer) handleAck(req jsonrpcRequest) {
	var params ackParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		s.log.Warn("ack invalid params")
		return
	}

	sid := session.ID(params.SessionID)

	rx := s.getRx(sid)
	if rx == nil {
		s.log.Warn("ack for unknown session", "session_id", string(sid))
		return
	}

	if err := rx.ring.ack(params.Offset); err != nil {
		s.log.Warn("ack rejected", "session_id", string(sid), "error", err)
	}
}

// handleDataFrame routes an inbound binary frame to the correct session.
func (s *WSServer) handleDataFrame(state *connState, data []byte) {
	frame, err := DecodeFrame(data)
	if err != nil {
		s.log.Warn("bad data frame", "error", err, "len", len(data))
		return
	}

	switch frame.MsgType {
	case MsgTypeData:
		sid := session.IDFromBytes(frame.SessionID)
		if !state.has(sid) {
			s.log.Warn("data frame for unknown session", "session_id", string(sid))
			return
		}
		sess, err := s.registry.Get(sid)
		if err != nil {
			s.log.Warn("data frame for unknown session", "session_id", string(sid))
			return
		}
		if _, err := sess.Write(frame.Payload); err != nil {
			s.log.Debug("session write error", "session_id", string(sid), "error", err)
		}
	case MsgTypeMetadata:
		s.log.Info("metadata frame received (reserved for Phase-2 — dropped)")
	default:
		s.log.Warn("unknown msg-type in data frame", "msgType", frame.MsgType)
	}
}

// --- session / ring plumbing ----------------------------------------------

// pumpToRing reads PTY output and writes it into the replay ring.
// Uses background context so the pump outlives any single WebSocket
// connection (AD-9). Blocks on ring.write when the ring is full and
// nothing has been acked — that is the AD-10 backpressure seam.
func (s *WSServer) pumpToRing(ctx context.Context, sess session.Session, ring *outputRing) {
	err := sess.StartOutput(ctx, func(data []byte) error {
		return ring.write(data)
	})
	if err != nil {
		s.log.Debug("session output ended", "session_id", string(sess.ID()), "error", err)
	}
}

// ringToConn streams the output ring to a WebSocket connection starting at
// the given byte offset. Exits when the connection drops or the ring closes.
//
// Enforces AD-10 credit-based flow control: a subscriber stops sending once
// unacked bytes reach CreditLimit and resumes when an ack frees room. Each
// send is capped at FairChunk bytes so a flooding session releases the
// shared wsConn write mutex between chunks, giving other sessions a chance
// to send (cross-tab fairness).
func (s *WSServer) ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, ring *outputRing, startOffset uint64) {
	var pending []byte
	pos := startOffset

	for {
		// Wait until the in-flight window has room (AD-10). The ring owns
		// the predicate; acked may legitimately exceed pos after a large
		// ack on reattach, which counts as no bytes unacked.
		if ring.waitForCredit(ctx, pos, CreditLimit) {
			return
		}

		if len(pending) == 0 {
			var data []byte
			data, _, _ = ring.snapshot(pos)
			if len(data) == 0 {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if ring.waitForData(ctx, pos) {
					return
				}
				continue
			}
			pending = data
		}

		// Cap each frame at FairChunk for cross-session fairness (AD-10).
		// Splitting one PTY read (~32 KB) into ≤4 frames lets other
		// sessions grab the wsConn mutex between chunks.
		chunk := pending
		if len(chunk) > FairChunk {
			chunk = chunk[:FairChunk]
		}

		f := Frame{
			Version:   FrameVersion,
			MsgType:   MsgTypeData,
			SessionID: sidBytes,
			Payload:   chunk,
		}
		if err := wconn.writeMessage(websocket.BinaryMessage, f.Encode()); err != nil {
			return
		}
		pos += uint64(len(chunk))
		pending = pending[len(chunk):]
	}
}

// monitorExit waits for the PTY to exit, then cleans up the ring and
// session and notifies the current subscriber. Exactly one instance runs
// per session (enforced by sessionRx.monitorOnce). Uses background context
// so it fires even after the WebSocket connection drops (AD-9).
func (s *WSServer) monitorExit(rx *sessionRx, sess session.Session) {
	<-sess.Done()

	wconn, state := rx.getSubscriber()
	if state != nil {
		state.remove(sess.ID())
	}
	rx.ring.close()
	s.removeRx(sess.ID())
	_ = s.registry.Close(sess.ID())

	if wconn == nil {
		return
	}

	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "exit",
		"params": map[string]string{
			"sessionId": string(sess.ID()),
		},
	}
	notifJSON, err := json.Marshal(notif)
	if err != nil {
		s.log.Error("marshal exit notification", "error", err)
		return
	}
	if err := wconn.writeMessage(websocket.TextMessage, notifJSON); err != nil {
		s.log.Debug("write exit notification", "error", err)
	}
}

// closeSession tears down the session and its ring. Looks up the ring
// instead of creating one — closing a session that has no ring is a no-op
// for the ring path (DEFECT 6).
func (s *WSServer) closeSession(sid session.ID) {
	rx := s.getRx(sid)
	if rx != nil {
		rx.ring.close()
	}
	s.removeRx(sid)
	_ = s.registry.Close(sid)
}
