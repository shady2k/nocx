package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/importer"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/ssh"
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

	// Per-launch capability token (bead nocx-hl3).
	token       string
	tokenSource io.Reader // entropy source; crypto/rand default
	origins     OriginPolicy

	// Override the listen address. Default 127.0.0.1:0.
	listenAddr string
	listener   net.Listener
	upgrader   websocket.Upgrader

	// Optional profile/group/credential stores for the connection-manager
	// control plane (profiles.*, groups.*, credentials.*). When nil, those
	// methods return a JSON-RPC error.
	profiles    profile.ProfileRepository
	groups      profile.GroupRepository
	credMeta    profile.CredentialMetadataRepository
	credentials credential.CredentialStore

	// Profile resolver maps profile IDs to SSH connect configs.
	resolver ProfileResolver

	// settings registry backs the settings.* JSON-RPC methods.
	settings   *settings.Registry
	resolverOK bool

	// Backup service (ADR-0015). When nil, backup.* methods return -32601.
	backupService *backup.Service

	// configMu serialises configuration mutations (backup create/preview/restore,
	// profile CRUD, group CRUD, credential CRUD, settings mutations, open).
	// backup handlers take exclusive; others take shared.
	configMu  sync.RWMutex
	configErr error // non-nil → config gate is poisoned; all config methods fail

	// ringsMu protects rx and stopped. One sessionRx per session;
	// keyed by session.ID. When stopped is true, getOrCreateRx returns nil
	// so no new rings are created after the server begins shutting down.
	ringsMu sync.Mutex
	rx      map[session.ID]*sessionRx
	stopped bool

	// connsMu protects conns. One entry per active WebSocket connection.
	connsMu sync.Mutex
	conns   map[*wsConn]struct{}
}

// ProfileResolver maps a profile ID to an SSH host and connect config.
// Passwords are never carried in the returned config — they are late-bound
// via the credential store wired into ConnectConfig.
type ProfileResolver interface {
	Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
}

// WithProfileResolver attaches a profile resolver for SSH connection setup.
func WithProfileResolver(r ProfileResolver) WSServerOption {
	return func(s *WSServer) { s.resolver = r; s.resolverOK = true }
}

// WSServerOption configures a WSServer.
type WSServerOption func(*WSServer)

// WithProfileRepository attaches a profile repository to the server, enabling
// the profiles.* JSON-RPC methods.
func WithProfileRepository(pr profile.ProfileRepository) WSServerOption {
	return func(s *WSServer) { s.profiles = pr }
}

// WithGroupRepository attaches a group repository to the server, enabling the
// groups.* JSON-RPC methods.
func WithGroupRepository(gr profile.GroupRepository) WSServerOption {
	return func(s *WSServer) { s.groups = gr }
}

// WithCredentialMetadataRepository attaches a credential metadata repository
// to the server, enabling the credentials.create/update/delete/list JSON-RPC
// methods.
func WithCredentialMetadataRepository(cmr profile.CredentialMetadataRepository) WSServerOption {
	return func(s *WSServer) { s.credMeta = cmr }
}

// WithCredentialStore attaches a credential store, enabling the
// credentials.* JSON-RPC methods.
func WithCredentialStore(cs credential.CredentialStore) WSServerOption {
	return func(s *WSServer) { s.credentials = cs }
}

// WithSettingsRegistry attaches a settings registry to the server, enabling
// the settings.* JSON-RPC methods. Also wires the registry's change notifier
// so every connected client receives settings.changed broadcasts.
func WithSettingsRegistry(r *settings.Registry) WSServerOption {
	return func(s *WSServer) {
		s.settings = r
		r.SetNotifier(func(revision int, keys []string) {
			s.broadcastSettingsChanged(revision, keys)
		})
	}
}

// WithBackupService attaches the backup service for backup.create/preview/restore
// JSON-RPC methods (ADR-0015).
func WithBackupService(svc *backup.Service) WSServerOption {
	return func(s *WSServer) { s.backupService = svc }
}

func NewWSServer(logger log.Logger, reg session.Registry, opts ...WSServerOption) *WSServer {
	s := &WSServer{
		log:      logger,
		registry: reg,
		upgrader: websocket.Upgrader{
			// CheckOrigin is always permissive; our own authorize
			// call handles origin/host policy before the upgrade.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		rx:      make(map[session.ID]*sessionRx),
		conns:   make(map[*wsConn]struct{}),
		origins: LoopbackOriginPolicy{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *WSServer) Start(ctx context.Context) error {
	// Fail closed: no entropy → no token → no connections.
	if err := s.mintToken(); err != nil {
		return err
	}

	// Configure the upgrader to accept only our token as the subprotocol,
	// so it echoes the selected protocol on upgrade (RFC 6455).
	s.upgrader.Subprotocols = []string{tokenProtocol(s.token)}

	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handleSession)

	addr := s.listenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
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
	Cols     uint16 `json:"cols"`
	Rows     uint16 `json:"rows"`
	XPixel   uint16 `json:"xpixel"`
	YPixel   uint16 `json:"ypixel"`
	Enhanced bool   `json:"enhanced"`

	// SSH fields — when Kind="ssh", the session opens an SSH channel.
	// ProfileID identifies the SSH profile to connect to; the backend
	// resolves host, credentials and jump host from the profile store.
	// Passwords are never carried in the open params — they are late-bound
	// inside the SSH auth chain from the credential store.
	Kind      string `json:"kind,omitempty"`
	ProfileID string `json:"profileId,omitempty"`
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
	if !s.authorize(w, r) {
		return
	}
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
	s.registerConn(wconn)
	defer s.unregisterConn(wconn)
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
		s.log.Warn("jsonrpc invalid request", "len", len(data))
		resp := newJSONRPCError(json.RawMessage("null"), -32600, "Invalid Request")
		_ = wconn.writeJSON(resp)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		// Transport-wide rule: any control frame may carry a secret, so none
		// are logged verbatim. Log size and error category only.
		category := "parse_error"
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			category = "syntax_error"
		case errors.As(err, &typeErr):
			category = "type_error"
		}
		s.log.Warn("jsonrpc parse error", "len", len(data), "category", category)
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
	case "profiles.list", "profiles.create", "profiles.update", "profiles.delete":
		s.handleProfileMethod(wconn, req)
	case "profiles.importTabby":
		s.handleImportTabby(wconn, req)
	case "groups.list", "groups.create", "groups.update", "groups.delete":
		s.handleGroupMethod(wconn, req)
	case "credentials.list", "credentials.create", "credentials.update", "credentials.delete":
		s.handleCredentialCRUDMethod(wconn, req)
	case "credentials.savePassword", "credentials.deletePassword",
		"credentials.hasPassword",
		"credentials.saveKeyPassphrase", "credentials.deleteKeyPassphrase":
		s.handleCredentialMethod(wconn, req)
	case "settings.describe", "settings.getSnapshot", "settings.set", "settings.reset",
		"settings.secretSet", "settings.secretDelete", "settings.secretExists":
		s.handleSettingsMethod(wconn, req)
	case "backup.create", "backup.preview", "backup.restore", "backup.saveToFile":
		s.handleBackupMethod(wconn, req)
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
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	var params openParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Cols == 0 || params.Rows == 0 {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: cols and rows required")
		_ = wconn.writeJSON(resp)
		return
	}

	cfg := session.Config{
		Kind:     session.KindLocal,
		Cols:     params.Cols,
		Rows:     params.Rows,
		XPixel:   params.XPixel,
		YPixel:   params.YPixel,
		Enhanced: params.Enhanced,
	}

	// SSH session — when kind="ssh", open a remote channel instead of local PTY.
	if params.Kind == "ssh" {
		if !s.resolverOK {
			resp := newJSONRPCError(req.ID, -32603, "SSH sessions not available (no profile resolver wired)")
			_ = wconn.writeJSON(resp)
			return
		}
		if params.ProfileID == "" {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: profileId required for ssh session")
			_ = wconn.writeJSON(resp)
			return
		}

		host, remote, err := s.resolver.Resolve(params.ProfileID)
		if err != nil {
			s.log.Error("profile resolve failed", "profileId", params.ProfileID, "error", err)
			resp := newJSONRPCError(req.ID, -32603, err.Error())
			_ = wconn.writeJSON(resp)
			return
		}

		remote.Cols = params.Cols
		remote.Rows = params.Rows
		remote.XPixel = params.XPixel
		remote.YPixel = params.YPixel

		s.log.Info("SSH open via profile", "profileId", params.ProfileID, "host", host, "user", remote.User)

		cfg.Kind = session.KindRemote
		cfg.Host = host
		cfg.Remote = remote
	}

	sess, err := s.registry.Open(ctx, cfg)
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

// --- profile/group/credential control-plane handlers ---------------------
//
// These methods back the connection-manager JSON-RPC calls (AD-1 control
// plane). Each returns a JSON-RPC error (-32601) when the relevant store is
// not wired (WithProfileRepository/WithGroupRepository/WithCredentialMetadataRepository not called).

func (s *WSServer) handleProfileMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}
	if s.profiles == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}
	switch req.Method {
	case "profiles.list":
		profs, err := s.profiles.LoadProfiles()
		if err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(profs)))
	case "profiles.create", "profiles.update":
		var p profile.SSHProfile
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if err := s.profiles.SaveProfile(p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(p)))
	case "profiles.delete":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if err := s.profiles.DeleteProfile(params.ID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	}
}

func (s *WSServer) handleGroupMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	if s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "groups not available"))
		return
	}
	switch req.Method {
	case "groups.list":
		groups, err := s.groups.LoadGroups()
		if err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(groups)))
	case "groups.create", "groups.update":
		var g profile.ProfileGroup
		if err := json.Unmarshal(req.Params, &g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if err := s.groups.SaveGroup(g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(g)))
	case "groups.delete":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if err := s.groups.DeleteGroup(params.ID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	}
}

func (s *WSServer) handleCredentialCRUDMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	if s.credMeta == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}
	switch req.Method {
	case "credentials.list":
		creds, err := s.credMeta.LoadCredentials()
		if err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		// Strip SecretID fields — the renderer must never see them (ADR-0011 SS2).
		for i := range creds {
			creds[i].SecretID = ""
			creds[i].PassphraseSecretID = ""
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(creds)))
	case "credentials.create", "credentials.update":
		var c profile.Credential
		if err := json.Unmarshal(req.Params, &c); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		// Reject renderer-supplied SecretIDs — the backend owns them exclusively.
		if c.SecretID != "" || c.PassphraseSecretID != "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "secretId/passphraseSecretId are backend-owned"))
			return
		}
		if c.ID == "" {
			c.ID = profile.NewCredentialID(c.Name)
		}
		if err := s.credMeta.SaveCredential(c); err != nil {
			// A missing host binding is the caller's mistake, not ours, and the
			// renderer has to tell the user which field to fix — so it travels
			// as Invalid params rather than Internal error (nocx-wd2m).
			code := -32603
			if errors.Is(err, profile.ErrCredentialHostRequired) {
				code = -32602
			}
			_ = wconn.writeJSON(newJSONRPCError(req.ID, code, err.Error()))
			return
		}
		c.SecretID = ""
		c.PassphraseSecretID = ""
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(c)))
	case "credentials.delete":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: id required"))
			return
		}
		if err := s.deleteCredentialCascade(params.ID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	}
}

func (s *WSServer) handleCredentialMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	if s.credentials == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "credentials not available"))
		return
	}
	switch req.Method {
	case "credentials.savePassword":
		var params struct {
			CredentialID string `json:"credentialId"`
			Password     string `json:"password"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.CredentialID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: credentialId required"))
			return
		}
		if err := s.savePasswordForCredential(params.CredentialID, params.Password); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	case "credentials.deletePassword":
		var params struct {
			CredentialID string `json:"credentialId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.CredentialID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: credentialId required"))
			return
		}
		if err := s.deletePasswordForCredential(params.CredentialID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	case "credentials.hasPassword":
		var params struct {
			CredentialID string `json:"credentialId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.CredentialID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: credentialId required"))
			return
		}
		has, err := s.hasPasswordForCredential(params.CredentialID)
		if err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(has)))
	case "credentials.saveKeyPassphrase":
		var params struct {
			CredentialID string `json:"credentialId"`
			Passphrase   string `json:"passphrase"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.CredentialID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: credentialId required"))
			return
		}
		if err := s.savePassphraseForCredential(params.CredentialID, params.Passphrase); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	case "credentials.deleteKeyPassphrase":
		var params struct {
			CredentialID string `json:"credentialId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.CredentialID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: credentialId required"))
			return
		}
		if err := s.deletePassphraseForCredential(params.CredentialID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	}
}

// savePasswordForCredential stores a password secret and repoints the
// credential's SecretID. The new secret is written under a fresh ID first,
// then metadata is updated, then the old secret (if any) is best-effort
// deleted — write-before-repoint prevents a crash from orphaning the new
// secret.
func (s *WSServer) savePasswordForCredential(credID, password string) error {
	if s.credMeta == nil {
		return errors.New("profiles not available")
	}
	cred, ok, err := s.findCredentialByID(credID)
	if err != nil {
		return fmt.Errorf("load credential %s: %w", credID, err)
	}
	if !ok {
		return fmt.Errorf("credential %s not found", credID)
	}

	newID := credential.NewSecretID()
	if err := s.credentials.Set(newID, credential.NewSecret(password)); err != nil {
		return fmt.Errorf("store secret: %w", err)
	}

	oldID := credential.SecretID(cred.SecretID)
	cred.SecretID = string(newID)
	if err := s.credMeta.SaveCredential(cred); err != nil {
		return fmt.Errorf("save credential metadata: %w", err)
	}

	// Best-effort delete of the old secret. The new one is already stored
	// and metadata points at it, so this is purely garbage collection.
	if oldID != "" {
		_ = s.credentials.Delete(oldID)
	}
	return nil
}

// deletePasswordForCredential removes the stored password secret and clears
// the credential's SecretID. The metadata update is the authoritative step;
// secret deletion is best-effort afterward.
func (s *WSServer) deletePasswordForCredential(credID string) error {
	if s.credMeta == nil {
		return errors.New("profiles not available")
	}
	cred, ok, err := s.findCredentialByID(credID)
	if err != nil {
		return fmt.Errorf("load credential %s: %w", credID, err)
	}
	if !ok {
		return fmt.Errorf("credential %s not found", credID)
	}

	oldID := credential.SecretID(cred.SecretID)
	cred.SecretID = ""
	if err := s.credMeta.SaveCredential(cred); err != nil {
		return fmt.Errorf("save credential metadata: %w", err)
	}

	if oldID != "" {
		_ = s.credentials.Delete(oldID)
	}
	return nil
}

// hasPasswordForCredential checks whether a password secret exists for the
// credential. Returns false when the credential is not found.
func (s *WSServer) hasPasswordForCredential(credID string) (bool, error) {
	if s.credMeta == nil {
		return false, nil
	}
	cred, ok, err := s.findCredentialByID(credID)
	if err != nil {
		return false, err
	}
	if !ok || cred.SecretID == "" {
		return false, nil
	}
	return s.credentials.Exists(credential.SecretID(cred.SecretID))
}

// savePassphraseForCredential stores a key passphrase secret and repoints
// the credential's PassphraseSecretID. Same write-before-repoint pattern as
// savePasswordForCredential.
func (s *WSServer) savePassphraseForCredential(credID, passphrase string) error {
	if s.credMeta == nil {
		return errors.New("profiles not available")
	}
	cred, ok, err := s.findCredentialByID(credID)
	if err != nil {
		return fmt.Errorf("load credential %s: %w", credID, err)
	}
	if !ok {
		return fmt.Errorf("credential %s not found", credID)
	}

	newID := credential.NewSecretID()
	if err := s.credentials.Set(newID, credential.NewSecret(passphrase)); err != nil {
		return fmt.Errorf("store passphrase: %w", err)
	}

	oldID := credential.SecretID(cred.PassphraseSecretID)
	cred.PassphraseSecretID = string(newID)
	if err := s.credMeta.SaveCredential(cred); err != nil {
		return fmt.Errorf("save credential metadata: %w", err)
	}

	if oldID != "" {
		_ = s.credentials.Delete(oldID)
	}
	return nil
}

// deletePassphraseForCredential removes the stored key passphrase secret and
// clears the credential's PassphraseSecretID.
func (s *WSServer) deletePassphraseForCredential(credID string) error {
	if s.credMeta == nil {
		return errors.New("profiles not available")
	}
	cred, ok, err := s.findCredentialByID(credID)
	if err != nil {
		return fmt.Errorf("load credential %s: %w", credID, err)
	}
	if !ok {
		return fmt.Errorf("credential %s not found", credID)
	}

	oldID := credential.SecretID(cred.PassphraseSecretID)
	cred.PassphraseSecretID = ""
	if err := s.credMeta.SaveCredential(cred); err != nil {
		return fmt.Errorf("save credential metadata: %w", err)
	}

	if oldID != "" {
		_ = s.credentials.Delete(oldID)
	}
	return nil
}

// deleteCredentialCascade removes a credential's metadata and every secret
// it references, in an order that cannot strand a secret.
//
// # Order: metadata-first, then best-effort secrets (ADR-0011 §4)
//
// Metadata is deleted first and its deletion stands; secret deletion is
// best-effort afterwards. A brief unreachable orphan (secret with no
// metadata pointing at it) is safer than metadata pointing at a secret
// that is gone. With SecretID, both secrets are reachable by their stable
// IDs from the metadata — the private key file no longer needs to exist.
//
// # Missing secrets are not errors
//
// Deleting a credential that never had a password (or whose passphrase was
// never saved) must succeed. SecretStore.Delete treats "already absent" as
// success, so no special-casing is needed.
func (s *WSServer) deleteCredentialCascade(id string) error {
	// Load the metadata BEFORE deleting it: SecretID and PassphraseSecretID
	// are needed to reach the keychain entries, and once the row is gone
	// they are unrecoverable.
	cred, ok, err := s.findCredentialByID(id)
	if err != nil {
		return fmt.Errorf("load credential %s: %w", id, err)
	}

	// Read every SecretID BEFORE deleting metadata.
	var pwID, ppID credential.SecretID
	if ok {
		pwID = credential.SecretID(cred.SecretID)
		ppID = credential.SecretID(cred.PassphraseSecretID)
	}

	// Delete metadata first. Its deletion stands regardless of secret
	// deletion outcome (ADR-0011 §4: a brief orphan beats a dangling ref).
	if err := s.credMeta.DeleteCredential(id); err != nil {
		return fmt.Errorf("delete credential metadata %s: %w", id, err)
	}

	// No metadata found → nothing referenced any secret. Idempotent.
	if !ok {
		return nil
	}

	// Best-effort secret deletion. Attempt BOTH deletions even if one
	// fails. Errors are aggregated; metadata is already gone.
	if s.credentials == nil {
		return nil
	}
	var errs []error
	if pwID != "" {
		if err := s.credentials.Delete(pwID); err != nil {
			errs = append(errs, fmt.Errorf("delete password for %s: %w", id, err))
		}
	}
	if ppID != "" {
		if err := s.credentials.Delete(ppID); err != nil {
			errs = append(errs, fmt.Errorf("delete key passphrase for %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// findCredentialByID loads a single credential by ID from the profile store.
// ok is false (not an error) when no credential with that ID exists — that
// is the idempotent-delete path. A store error is returned as-is.
func (s *WSServer) findCredentialByID(id string) (profile.Credential, bool, error) {
	creds, err := s.credMeta.LoadCredentials()
	if err != nil {
		return profile.Credential{}, false, err
	}
	for _, c := range creds {
		if c.ID == id {
			return c, true, nil
		}
	}
	return profile.Credential{}, false, nil
}

// handleImportTabby parses a Tabby config YAML and imports SSH profiles +
// groups into the wired profile and group repositories. Returns the number
// of profiles imported.
func (s *WSServer) handleImportTabby(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	if s.profiles == nil || s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}
	var params struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Config == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: config (YAML string) required"))
		return
	}

	cfg, err := importer.ParseTabbyConfig([]byte(params.Config))
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Parse Tabby config: "+err.Error()))
		return
	}

	countBefore, _ := s.profiles.LoadProfiles()
	before := len(countBefore)
	if err := importer.ImportGroups(cfg, s.groups); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Import groups: "+err.Error()))
		return
	}
	if err := importer.ImportProfiles(cfg, s.profiles, "ssh"); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Import profiles: "+err.Error()))
		return
	}
	after, _ := s.profiles.LoadProfiles()
	imported := len(after) - before
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(imported)))
}

// --- settings control-plane handlers -------------------------------------

// findDescriptor looks up a setting declaration by key.
func (s *WSServer) findDescriptor(key string) settings.Descriptor {
	for _, d := range s.settings.Descriptors() {
		if d.Key() == key {
			return d
		}
	}
	return nil
}

// handleSettingsMethod dispatches settings.* RPCs. Returns -32601 when the
func (s *WSServer) handleSettingsMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	if s.settings == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "Method not found"))
		return
	}
	switch req.Method {
	case "settings.describe":
		s.handleSettingsDescribe(wconn, req)
	case "settings.getSnapshot":
		s.handleSettingsGetSnapshot(wconn, req)
	case "settings.set":
		s.handleSettingsSet(wconn, req)
	case "settings.reset":
		s.handleSettingsReset(wconn, req)
	case "settings.secretSet":
		s.handleSettingsSecretSet(wconn, req)
	case "settings.secretDelete":
		s.handleSettingsSecretDelete(wconn, req)
	case "settings.secretExists":
		s.handleSettingsSecretExists(wconn, req)
	}
}

func (s *WSServer) handleSettingsDescribe(wconn *wsConn, req jsonrpcRequest) {
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]any{
		"declarations": s.settings.Declarations(),
	})))
}

func (s *WSServer) handleSettingsGetSnapshot(wconn *wsConn, req jsonrpcRequest) {
	snap, err := s.settings.GetSnapshot()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "settings.getSnapshot: "+err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]any{
		"values":     snap.Values,
		"overridden": snap.Overridden,
		"revision":   snap.Revision,
	})))
}

// settingsSetParams carries the key and the untyped value.
type settingsSetParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (s *WSServer) handleSettingsSet(wconn *wsConn, req jsonrpcRequest) {
	var p settingsSetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	desc := s.findDescriptor(p.Key)
	if desc == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Unknown setting: "+p.Key))
		return
	}
	if desc.Control() == settings.ControlSecret {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Secret settings must use settings.secretSet"))
		return
	}

	var setErr error
	switch desc.Control() {
	case settings.ControlToggle:
		var b bool
		if err := json.Unmarshal(p.Value, &b); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid value: expected boolean"))
			return
		}
		bk, ok := desc.(*settings.Bool)
		if !ok {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as a toggle but is not a Bool key"))
			return
		}
		setErr = s.settings.SetBool(bk, b)
	case settings.ControlText:
		var str string
		if err := json.Unmarshal(p.Value, &str); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid value: expected string"))
			return
		}
		sk, ok := desc.(*settings.String)
		if !ok {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as text but is not a String key"))
			return
		}
		setErr = s.settings.SetString(sk, str)
	case settings.ControlNumber:
		var n float64
		if err := json.Unmarshal(p.Value, &n); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid value: expected number"))
			return
		}
		nk, ok := desc.(*settings.Number)
		if !ok {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as a number but is not a Number key"))
			return
		}
		setErr = s.settings.SetNumber(nk, n)
	case settings.ControlSelect:
		var str string
		if err := json.Unmarshal(p.Value, &str); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid value: expected string"))
			return
		}
		sk, ok := desc.(*settings.Select)
		if !ok {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as a select but is not a Select key"))
			return
		}
		setErr = s.settings.SetSelect(sk, str)
	}

	if setErr != nil {
		if errors.Is(setErr, settings.ErrValidation) {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, setErr.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, setErr.Error()))
		return
	}

	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]bool{"ok": true})))
}

// settingsResetParams carries the key to reset.
type settingsResetParams struct {
	Key string `json:"key"`
}

func (s *WSServer) handleSettingsReset(wconn *wsConn, req jsonrpcRequest) {
	var p settingsResetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	desc := s.findDescriptor(p.Key)
	if desc == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Unknown setting: "+p.Key))
		return
	}

	if err := s.settings.Reset(desc); err != nil {
		if errors.Is(err, settings.ErrValidation) {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "settings.reset: "+err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]bool{"ok": true})))
}

// settingsSecretSetParams carries the key and the secret value.
type settingsSecretSetParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *WSServer) handleSettingsSecretSet(wconn *wsConn, req jsonrpcRequest) {
	var p settingsSecretSetParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	desc := s.findDescriptor(p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Not a secret setting: "+p.Key))
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as secret but is not a Secret key"))
		return
	}
	if err := s.settings.SecretSet(sk, p.Value); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "settings.secretSet: "+err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]bool{"ok": true})))
}

// settingsSecretDeleteParams carries the key to delete.
type settingsSecretDeleteParams struct {
	Key string `json:"key"`
}

func (s *WSServer) handleSettingsSecretDelete(wconn *wsConn, req jsonrpcRequest) {
	var p settingsSecretDeleteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	desc := s.findDescriptor(p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Not a secret setting: "+p.Key))
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as secret but is not a Secret key"))
		return
	}
	if err := s.settings.SecretDelete(sk); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "settings.secretDelete: "+err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]bool{"ok": true})))
}

// settingsSecretExistsParams carries the key to check.
type settingsSecretExistsParams struct {
	Key string `json:"key"`
}

func (s *WSServer) handleSettingsSecretExists(wconn *wsConn, req jsonrpcRequest) {
	var p settingsSecretExistsParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
		return
	}

	desc := s.findDescriptor(p.Key)
	if desc == nil || desc.Control() != settings.ControlSecret {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Not a secret setting: "+p.Key))
		return
	}

	sk, ok := desc.(*settings.Secret)
	if !ok {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Setting "+p.Key+" is declared as secret but is not a Secret key"))
		return
	}
	exists, err := s.settings.SecretExists(sk)
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "settings.secretExists: "+err.Error()))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(map[string]bool{"exists": exists})))
}

// mustMarshal serializes v to JSON, panicking on error (only used for
// values we construct ourselves, which are always serializable).
func mustMarshal(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

// --- connection registry ---------------------------------------------------

// registerConn adds a connection to the broadcast set.
func (s *WSServer) registerConn(wc *wsConn) {
	s.connsMu.Lock()
	s.conns[wc] = struct{}{}
	s.connsMu.Unlock()
}

// unregisterConn removes a connection from the broadcast set.
func (s *WSServer) unregisterConn(wc *wsConn) {
	s.connsMu.Lock()
	delete(s.conns, wc)
	s.connsMu.Unlock()
}

// broadcastSettingsChanged sends a settings.changed notification to every
// connected client. Best-effort: a write failure on one connection does not
// prevent writes to others.
func (s *WSServer) broadcastSettingsChanged(revision int, keys []string) {
	s.connsMu.Lock()
	// Copy under lock so writes happen outside the critical section.
	conns := make([]*wsConn, 0, len(s.conns))
	for wc := range s.conns {
		conns = append(conns, wc)
	}
	s.connsMu.Unlock()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "settings.changed",
		"params": map[string]any{
			"revision": revision,
			"keys":     keys,
		},
	}
	for _, wc := range conns {
		_ = wc.writeJSON(msg)
	}
}
