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

type WSServer struct {
	log      log.Logger
	registry session.Registry
	server   *http.Server
	port     int
	listener net.Listener
	upgrader websocket.Upgrader
}

func NewWSServer(logger log.Logger, reg session.Registry) *WSServer {
	return &WSServer{
		log:      logger,
		registry: reg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
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
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *WSServer) Port() int {
	return s.port
}

// wsConn wraps a gorilla/websocket.Conn with a mutex to serialize writes.
// The gorilla package does not support concurrent writes.
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

// connState tracks sessions opened on a single WebSocket connection so they
// can be closed when the connection drops (no goroutine or PTY leak).
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

func (c *connState) snapshot() []session.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]session.ID, 0, len(c.sessions))
	for id := range c.sessions {
		ids = append(ids, id)
	}
	return ids
}

func (c *connState) closeAll(reg session.Registry, logger log.Logger) {
	ids := c.snapshot()
	for _, id := range ids {
		if err := reg.Close(id); err != nil {
			logger.Debug("error closing session on disconnect", "session_id", string(id), "error", err)
		}
	}
}

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

// isJSONObject returns true if data starts with '{', ignoring leading whitespace.
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

func (s *WSServer) handleSession(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	wconn := newWSConn(conn)
	ctx := r.Context()
	state := newConnState()
	defer state.closeAll(s.registry, s.log)

	readErr := make(chan error, 1)
	go s.readLoop(ctx, wconn, state, readErr)

	<-readErr
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
	default:
		resp := newJSONRPCError(req.ID, -32601, "Method not found")
		_ = wconn.writeJSON(resp)
	}
}

// handleOpen creates a new session.
//
// Per AD-7: the server assigns the authoritative session-id. The JSON-RPC
// request id serves as the correlation-id — we do not add a second
// correlationId field because two correlation identifiers for one exchange
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

	result := map[string]string{"sessionId": string(sess.ID())}
	resultJSON, _ := json.Marshal(result)
	resp := newJSONRPCResult(req.ID, resultJSON)
	_ = wconn.writeJSON(resp)

	// Start the output pump and exit monitor only after the ack is
	// sent. AD-7: the ack must precede the session's own traffic in both
	// directions, otherwise the first prompt races the open result and
	// the client drops it (its sessionId is still null).
	sidBytes, _ := session.IDToBytes(sess.ID())
	go s.sessionOutput(ctx, wconn, sess, sidBytes)

	go s.monitorExit(ctx, wconn, sess, state)
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

	if err := s.registry.Close(sid); err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = wconn.writeJSON(resp)
		return
	}

	state.remove(sid)

	result, _ := json.Marshal(map[string]any{})
	resp := newJSONRPCResult(req.ID, result)
	_ = wconn.writeJSON(resp)
}

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

func (s *WSServer) sessionOutput(ctx context.Context, wconn *wsConn, sess session.Session, sidBytes [16]byte) {
	err := sess.StartOutput(ctx, func(data []byte) error {
		f := Frame{
			Version:   FrameVersion,
			MsgType:   MsgTypeData,
			SessionID: sidBytes,
			Payload:   data,
		}
		return wconn.writeMessage(websocket.BinaryMessage, f.Encode())
	})
	if err != nil {
		s.log.Debug("session output ended", "session_id", string(sess.ID()), "error", err)
	}
}

// monitorExit watches the session's Done channel and sends an exit notification
// when the PTY process exits.
func (s *WSServer) monitorExit(ctx context.Context, wconn *wsConn, sess session.Session, state *connState) {
	select {
	case <-sess.Done():
	case <-ctx.Done():
		return
	}

	state.remove(sess.ID())
	_ = s.registry.Close(sess.ID())

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
