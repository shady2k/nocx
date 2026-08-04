package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/importer"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/sandbox"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
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
	// Optional profile/group stores for the connection-manager control
	// plane (profiles.*, groups.*). When nil, those methods return a
	// JSON-RPC error.
	profiles    profile.ProfileRepository
	groups      profile.GroupRepository
	credentials credential.SecretStore
	// Vault lifecycle for vault.* RPC methods. When nil, those methods return a
	// JSON-RPC error.
	vaultLifecycle VaultLifecycle
	vaultReset     VaultResetService
	// Native dialog capability (dialog.* RPCs). When nil, those methods
	// return -32601: the dev-web harness has no Wails runtime to open a
	// dialog with. Set post-construction from main.go's WailsApp.startup,
	// which is the only place the Wails context exists; guarded because the
	// handler may read it while startup assigns it.
	dialogMu      sync.RWMutex
	dialogService DialogService

	// Profile resolver maps profile IDs to SSH connect configs.
	resolver ProfileResolver

	// Profile service provides a single validated write path for profiles
	// and groups through the domain layer.
	profileSvc *profile.ProfileService

	// settings registry backs the settings.* JSON-RPC methods.
	settings   *settings.Registry
	resolverOK bool

	// sandboxSvc is the per-tab filesystem sandbox backend (ADR-0019),
	// answering sandbox.status and preparing sandboxed open requests. The
	// transport never renders policy — it validates the request and maps the
	// backend's typed errors to reserved codes.
	sandboxSvc sandbox.Service

	// SSH config resolver and config path for the ssh.listAliases RPC.
	// When nil, the handler returns a JSON-RPC error. The resolver
	// answers values via ssh -G; enumeration reads Host patterns from
	// the config file directly (see internal/ssh/aliases.go for the
	// mechanics).
	sshConfigResolver ssh.ConfigResolver
	sshConfigPath     string

	// Pending-capture registry: the backend-side holder of submitted
	// credentials awaiting a save decision (internal/credential). Created
	// at construction; nil only when its fingerprint key could not be
	// minted, in which case no offers are made and saves are refused.
	captures *credential.CaptureRegistry
	// nextConnID assigns the per-connection (per-tab) identity captures
	// are scoped to.
	nextConnID atomic.Uint64
	// prober validates credentials without opening a session (connections.test).
	// When nil, the handler returns a JSON-RPC error.
	prober Prober
	// hostKeyTruster appends offered host keys to known_hosts
	// (connections.trustHostKey — accept-on-first-use). When nil, the
	// handler returns a JSON-RPC error.
	hostKeyTruster HostKeyTruster
	// probeResultStore records probe outcomes as operational evidence.
	// When nil, probe results are not stored (the probe still runs and
	// returns its outcome to the caller).
	probeResultStore *ProbeResultStore

	// profileUsage tracks last-used timestamps for the sessions.status RPC.
	// When nil, the handler reports live-state from the registry but
	// last-used timestamps are unavailable (nocx-uxs5.4).
	profileUsage session.ProfileUsageTracker

	// When nil, export.* methods return a JSON-RPC error.
	// The fields are populated by WithPaths, WithContentDB.
	// The credential.CredentialStore is deliberately absent —
	// no export mode may resolve a secret (ADR-0011 §2).
	exportPaths     storage.Paths
	exportContentDB content.ContentDB

	// contentDB is the durable content store backing history.query. When
	// nil, the method answers source=session — the overlay then labels what
	// it shows "this session only" instead of presenting the in-memory
	// ledger as all history (contracts/history.query.schema.json).
	contentDB content.ContentDB

	// ringsMu protects rx and stopped. One sessionRx per session;
	// keyed by session.ID. When stopped is true, getOrCreateRx returns nil
	// so no new rings are created after the server begins shutting down.
	ringsMu sync.Mutex
	rx      map[session.ID]*sessionRx
	stopped bool

	// connsMu protects conns. One entry per active WebSocket connection.
	connsMu sync.Mutex
	conns   map[*wsConn]struct{}

	// planMu guards planStore. Plans are decrypted import plans keyed by
	// opaque token, stored server-side so secrets never reach the renderer.
	planMu    sync.Mutex
	planStore map[string]*planEntry
}

// ── Tabby import plan store (server-side, never reaches renderer) ─────────

// planEntry holds a decrypted import plan for one-time execution.
// inProgress prevents concurrent execute calls for the same token.
type planEntry struct {
	plan       *importPlan
	createdAt  time.Time
	inProgress bool
}

// importPlan is the complete decrypted plan for a Tabby import.
// Stored server-side by opaque token; secrets never leave the backend.
type importPlan struct {
	profiles []profile.SSHProfile
	groups   []profile.ProfileGroup
	creds    []credentialPlan
}

// credentialPlan pairs a decrypted tabby secret with the connection target it
// was keyed to. For a password, target* records the connection the tabby vault
// keyed the secret to — the profile whose options match carries the binding
// (ADR-0017). Passphrases have no target: a passphrase belongs to a private
// key, not to a connection, and stays an unbound vault row the editor can
// bind.
type credentialPlan struct {
	name         string
	secret       string
	isPassphrase bool
	targetUser   string
	targetHost   string
	targetPort   int
}

// planTTL is how long a plan remains valid after creation.
const planTTL = 10 * time.Minute

// maxPlans bounds the in-memory plan map to prevent unbounded accumulation.
const maxPlans = 100

// storePlan stores a plan and returns an opaque token.
func (s *WSServer) storePlan(plan *importPlan) (string, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()

	// Lazy init.
	if s.planStore == nil {
		s.planStore = make(map[string]*planEntry)
	}

	// Evict expired entries before adding.
	now := time.Now()
	for k, e := range s.planStore {
		if now.Sub(e.createdAt) > planTTL {
			delete(s.planStore, k)
		}
	}

	if len(s.planStore) >= maxPlans {
		return "", errors.New("plan store full")
	}

	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf[:])
	s.planStore[token] = &planEntry{plan: plan, createdAt: now}
	return token, nil
}

// claimPlan marks a plan as in-progress and returns it. Returns nil if not
// found, expired, or already claimed by a concurrent caller.
func (s *WSServer) claimPlan(token string) *importPlan {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	e, ok := s.planStore[token]
	if !ok || e.inProgress {
		return nil
	}
	if time.Since(e.createdAt) > planTTL {
		delete(s.planStore, token)
		return nil
	}
	e.inProgress = true
	return e.plan
}

// releasePlan clears the in-progress flag so the plan can be retried (e.g.
// after vault setup/unlock). No-op if the token does not exist.
func (s *WSServer) releasePlan(token string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if e, ok := s.planStore[token]; ok {
		e.inProgress = false
	}
}

// finishPlan removes a completed plan from the store. No-op if not found.
func (s *WSServer) finishPlan(token string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	delete(s.planStore, token)
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

// WithSSHConfigResolver attaches the SSH config resolver and config path
// for the ssh.listAliases RPC. The resolver answers values via ssh -G;
// the config path is used to enumerate Host patterns (see aliases.go for
// the split rationale). When not wired, ssh.listAliases returns a JSON-RPC
// error.
func WithSSHConfigResolver(resolver ssh.ConfigResolver, configPath string) WSServerOption {
	return func(s *WSServer) { s.sshConfigResolver = resolver; s.sshConfigPath = configPath }
}

// WithProbeResultStore attaches a probe result store for recording outcomes
// of connections.test probes. When nil, probe outcomes are still returned to
// the caller but not persisted in memory.
func WithProbeResultStore(s *ProbeResultStore) WSServerOption {
	return func(ws *WSServer) { ws.probeResultStore = s }
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

// WithCredentialStore attaches a credential store, enabling the
// secrets.* and vault.* secret operations.
func WithCredentialStore(cs credential.SecretStore) WSServerOption {
	return func(s *WSServer) { s.credentials = cs }
}

// WithCredentialStore attaches a credential store, enabling the

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

// WithSandboxService attaches the filesystem sandbox backend, enabling
// sandbox.status and sandboxed open requests. Without it, sandbox.status
// reports -32601 and a sandboxed open fails closed at -32010 (no service to
// confirm the flag).
func WithSandboxService(svc sandbox.Service) WSServerOption {
	return func(s *WSServer) { s.sandboxSvc = svc }
}

// sandboxEnabled reports whether the opt-in feature flag is on. The flag is
// the sole gate: a sandbox request while it is off is rejected (-32010), so
// UI and wire behavior agree even if the renderer is stale (design spec
// §3.1). A missing registry fails closed.
func (s *WSServer) sandboxEnabled() bool {
	if s.settings == nil {
		return false
	}
	on, err := s.settings.GetBool(settings.SandboxEnabled)
	return err == nil && on
}

// WithExportPaths attaches storage path resolution for the export.backup
// JSON-RPC method (same-machine backup manifest).
func WithExportPaths(p storage.Paths) WSServerOption {
	return func(s *WSServer) { s.exportPaths = p }
}

// WithExportContentDB attaches a content database for the
// export.portableEncrypted JSON-RPC method. A stub is correct when
// content.db has not yet been created (ADR-0011 §5).
func WithExportContentDB(db content.ContentDB) WSServerOption {
	return func(s *WSServer) { s.exportContentDB = db }
}

// WithContentDB attaches the durable content store backing history.query.
// When absent, the method answers source=session so the overlay labels what
// it shows "this session only" instead of presenting the in-memory ledger
// as all history (contracts/history.query.schema.json). The composition
// root passes the same ContentDB it hands WithExportContentDB.
func WithContentDB(db content.ContentDB) WSServerOption {
	return func(s *WSServer) { s.contentDB = db }
}

// WithProfileService attaches a profile domain service for import
// operations, providing a single validated write path and atomic imports.
func WithProfileService(svc *profile.ProfileService) WSServerOption {
	return func(s *WSServer) { s.profileSvc = svc }
}

// WithCaptureRegistry injects the pending-capture registry. Test seam:
// production constructs its own. The injected registry must not be shared
// across servers.
func WithCaptureRegistry(r *credential.CaptureRegistry) WSServerOption {
	return func(s *WSServer) {
		s.captures = r
	}
}

// WithVaultLifecycle attaches the vault seal-lifecycle surface, enabling the
// vault.* JSON-RPC methods.
func WithVaultLifecycle(vl VaultLifecycle) WSServerOption {
	return func(s *WSServer) { s.vaultLifecycle = vl }
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
	if caps, err := credential.NewCaptureRegistry(); err != nil {
		// No entropy for the fingerprint key: no offers are made and
		// capture saves are refused. A predictable fingerprint key would be
		// worse than none — the equality facts would be forgeable.
		logger.Error("capture registry unavailable; no offers will be made", "error", err)
	} else {
		s.captures = caps
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

	// Application shutdown destroys every pending capture (the contract's
	// list names it).
	if s.captures != nil {
		s.captures.DestroyAll()
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
//
// id is the per-connection (per-tab) identity the capture registry scopes
// captures to: backend-assigned, monotonic, and never reused.
type wsConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
	id   uint64
}

func newWSConn(conn *websocket.Conn, id uint64) *wsConn {
	return &wsConn{conn: conn, id: id}
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
// generation is the tab's command-submission counter: it is what makes
// "a superseding submission from that tab" a backend fact — the capture
// scope rides on it (the renderer's session-local command ids never cross
// the wire).
type connState struct {
	mu         sync.Mutex
	sessions   map[session.ID]session.Session
	generation uint64
}

func newConnState() *connState {
	return &connState{sessions: make(map[session.ID]session.Session)}
}

// nextGeneration advances and returns the tab's submission counter.
func (c *connState) nextGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	return c.generation
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
	Data    any    `json:"data,omitempty"`
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
	// Host opens a direct SSH connection without a saved profile. When set
	// (and ProfileID is empty), the backend resolves the host through
	// ~/.ssh/config (ssh -G) and opens a direct session. User optionally
	// overrides the resolved user.
	Host string `json:"host,omitempty"`
	User string `json:"user,omitempty"`
	// Sandbox is the opt-in filesystem sandbox request (ADR-0019). Presence
	// is the sole wire opt-in; the renderer supplies only the workspace, the
	// backend canonicalizes and owns policy and enforcement.
	Sandbox *openSandboxParams `json:"sandbox,omitempty"`
}

// openSandboxParams is the sandbox block of open (design spec §4.1).
type openSandboxParams struct {
	Workspace string `json:"workspace"`
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

	wconn := newWSConn(conn, s.nextConnID.Add(1))
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
	case "sandbox.status":
		s.handleSandboxStatus(wconn, req)
	case "dialog.openFile", "dialog.openDirectory":
		s.handleDialogMethod(wconn, req)
	case "profiles.list", "profiles.create", "profiles.update", "profiles.delete",
		"profiles.effective", "profiles.patch":
		s.handleProfileMethod(wconn, req)
	case "profiles.importTabby":
		s.handleImportTabby(wconn, req)
	case "profiles.tabbyPreview":
		s.handleTabbyPreview(wconn, req)
	case "profiles.tabbyExecute":
		s.handleTabbyExecute(wconn, req)
	case "profiles.moveImpact":
		s.handleProfileMoveImpact(wconn, req)
	case "secrets.usage":
		s.handleSecretUsageMethod(wconn, req)
	case "secrets.savePassword", "secrets.saveKeyMaterial", "secrets.saveKeyPassphrase":
		s.handleSecretMintMethod(wconn, req)
	case "secrets.detect":
		s.handleSecretsDetect(wconn, req)
	case "secrets.captureSave":
		s.handleCaptureSave(wconn, req)
	case "secrets.captureDismiss":
		s.handleCaptureDismiss(wconn, req)
	case "groups.impact":
		s.handleGroupImpact(wconn, req)
	case "groups.apply":
		s.handleGroupApply(wconn, req)
	case "groups.list", "groups.create", "groups.update", "groups.delete":
		s.handleGroupMethod(wconn, req)
	case "settings.describe", "settings.getSnapshot", "settings.set", "settings.reset",
		"settings.secretSet", "settings.secretDelete", "settings.secretExists":
		s.handleSettingsMethod(wconn, req)
	case "sessions.status":
		s.handleSessionsStatus(wconn, req)
	case "export.manifest", "export.configExport", "export.portableEncrypted",
		"export.backup", "export.import", "export.importPortable":
		s.handleExportMethod(ctx, wconn, req)
	case "connections.test":
		s.handleConnectionsTest(wconn, req)
	case "connections.trustHostKey":
		s.handleConnectionsTrustHostKey(wconn, req)
	case "sshConfig.aliases":
		s.handleSSHConfigAliases(wconn, req)
	case "sshConfig.path":
		s.handleSSHConfigPath(wconn, req)
	case "history.query":
		s.handleHistoryQuery(ctx, wconn, req)
	case "history.record":
		s.handleHistoryRecord(ctx, wconn, state, req)
	case "fs.complete":
		s.handleFsComplete(wconn, req)
	case "vault.status", "vault.setup", "vault.unseal", "vault.seal",
		"vault.changePassphrase", "vault.regenerateRecovery", "vault.setDefaultProvider",
		"vault.setAutoSeal", "vault.activity", "vault.inventory",
		"vault.createSecret", "vault.renameSecret", "vault.replaceSecret",
		"vault.deleteSecret", "vault.resolveLine":
		s.handleVaultMethod(wconn, req)
	// Not routed through handleVaultMethod: that gate refuses when the vault
	// lifecycle is absent, and a reset must work on a vault that is broken or
	// half-built — which is the only state it is ever wanted in.
	case "vault.resetPreview":
		s.handleVaultResetPreview(wconn, req)
	case "vault.reset":
		s.handleVaultReset(wconn, req)
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

	// Sandbox validation (design spec §4.1): local-only, feature flag on,
	// workspace canonicalized once. Failures are -32602 for invalid params,
	// -32010 when the flag is off.
	var sandboxReq *sandbox.Request
	if params.Sandbox != nil {
		if params.Kind == "ssh" || params.ProfileID != "" || params.Host != "" {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: sandbox is only valid for local sessions")
			_ = wconn.writeJSON(resp)
			return
		}
		if !s.sandboxEnabled() {
			resp := newJSONRPCError(req.ID, -32010, "Filesystem sandbox is disabled")
			resp.Error.Data = map[string]any{"reason": "feature-disabled"}
			_ = wconn.writeJSON(resp)
			return
		}
		canon, err := sandbox.CanonicalizeWorkspace(params.Sandbox.Workspace)
		if err != nil {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: "+err.Error())
			_ = wconn.writeJSON(resp)
			return
		}
		sandboxReq = &sandbox.Request{Workspace: canon}
	}

	cfg := session.Config{
		Kind:     session.KindLocal,
		Cols:     params.Cols,
		Rows:     params.Rows,
		XPixel:   params.XPixel,
		YPixel:   params.YPixel,
		Enhanced: params.Enhanced,
		Sandbox:  sandboxReq,
	}
	// ProfileID is deliberately NOT set here. It is recorded below, only once
	// the resolver has accepted it, because a local PTY has no profile and
	// setting it up front lets a renderer attach any profile id to a local
	// session it opens. sessions.status would then report that profile live and
	// the connection list would draw a row as connected with nothing behind it
	// (nocx-uxs5.4).

	// SSH session — when kind="ssh", open a remote channel instead of local PTY.
	if params.Kind == "ssh" {
		var host string
		var remote *ssh.ConnectConfig

		if params.ProfileID != "" {
			// Profile-based resolution: look up the stored profile, resolve
			// credentials and jump hosts through the profile resolver.
			if !s.resolverOK {
				resp := newJSONRPCError(req.ID, -32603, "SSH sessions not available (no profile resolver wired)")
				_ = wconn.writeJSON(resp)
				return
			}

			var err error
			host, remote, err = s.resolver.Resolve(params.ProfileID)
			if err != nil {
				s.log.Error("profile resolve failed", "profileId", params.ProfileID, "error", err)
				// Resolving reads the stored password, so a sealed vault surfaces
				// here — the renderer needs the reason to offer an unlock.
				resp := rpcErrorFor(req.ID, -32603, "", err)
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
			// Recorded here and nowhere else: the resolver has just accepted this
			// id, so the association is the backend's own conclusion rather than
			// the renderer's claim.
			cfg.ProfileID = params.ProfileID
			// CredentialID from the resolver: scoped revocation matches
			// sessions by credential. Empty for sessions with no linked
			// credential (inline auth).
			cfg.CredentialID = remote.CredentialID

		} else if params.Host != "" {
			// Direct host resolution: resolve through ~/.ssh/config (ssh -G)
			// and build a minimal ConnectConfig. Used for SSH aliases from
			// the config file — no stored profile involved.
			if s.sshConfigResolver == nil {
				resp := newJSONRPCError(req.ID, -32603, "SSH config resolver not available")
				_ = wconn.writeJSON(resp)
				return
			}

			resolved, err := s.sshConfigResolver.ResolveConfig(ctx, params.Host)
			if err != nil {
				s.log.Warn("SSH config resolution degraded for direct host", "host", params.Host, "error", err)
			}

			user := params.User
			if user == "" && resolved != nil && resolved.User != "" {
				user = resolved.User
			}
			port := 0
			if resolved != nil && resolved.Port > 0 {
				port = resolved.Port
			}
			remoteHost := params.Host
			if resolved != nil && resolved.HostName != "" {
				remoteHost = resolved.HostName
			}

			var keyFile string
			if resolved != nil {
				keyFile = resolved.IdentityFile
			}
			remote = &ssh.ConnectConfig{
				User:    user,
				Port:    port,
				KeyFile: keyFile,
				Cols:    params.Cols,
				Rows:    params.Rows,
			}

			s.log.Info("SSH open via direct host", "host", params.Host, "resolvedHost", remoteHost, "user", user)

			cfg.Kind = session.KindRemote
			cfg.Host = remoteHost
			cfg.Remote = remote
			// No ProfileID — this is not a saved profile. The usage tracker
			// does not record it.
		} else {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: profileId or host required for ssh session")
			_ = wconn.writeJSON(resp)
			return
		}
	}

	sess, err := s.registry.Open(ctx, cfg)
	if err != nil {
		s.log.Error("failed to open session", "error", err)
		// Sandbox failures are typed: -32011 carries the backend status
		// reason, -32012 a setup failure (policy, helper handshake, native
		// launch). The logs above carry backend/reason only — never paths.
		var statusErr *sandbox.StatusError
		var setupErr *sandbox.SetupError
		switch {
		case errors.As(err, &statusErr):
			resp := newJSONRPCError(req.ID, -32011, statusErr.Status.Reason)
			resp.Error.Data = map[string]any{"reason": statusErr.Status.Reason}
			_ = wconn.writeJSON(resp)
			return
		case errors.As(err, &setupErr):
			resp := newJSONRPCError(req.ID, -32012, "sandbox setup failed")
			resp.Error.Data = map[string]any{"reason": "setup-failed"}
			_ = wconn.writeJSON(resp)
			return
		}
		// A sealed vault surfaces here for EVERY connection that needs it —
		// this is still a vault access, and the renderer must get the reason
		// so the vault-owned unlock prompt appears instead of an error
		// (the dispatcher intercepts reason="vault-sealed" on any RPC).
		if errors.Is(err, vault.ErrVaultSealed) || errors.Is(err, vault.ErrVaultUninitialized) {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "", err))
			return
		}
		// Classify the SSH error through the same taxonomy the probe uses
		// so the user sees what actually failed, not "Internal error".
		pr := classifyProbeError(err)
		var msg string
		if pr.err == nil {
			msg = string(pr.outcome) + ": " + pr.detail
		} else {
			msg = err.Error() // unclassifiable — use the raw wrapped error
		}
		resp := newJSONRPCError(req.ID, -32603, msg)
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
	// needs OSC 7 (nocx-5mn.2). A sandboxed session additionally carries its
	// immutable {backend, workspace, writableRoots} metadata; ordinary and
	// SSH results omit it (design spec §4.5).
	result := map[string]any{
		"sessionId": string(sess.ID()),
		"cwd":       sess.Cwd(),
	}
	if si := sess.SandboxInfo(); si != nil {
		result["sandbox"] = si
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

// --- profile/group control-plane handlers -------------------------------
//
// These methods back the connection-manager JSON-RPC calls (AD-1 control
// plane). Each returns a JSON-RPC error (-32601) when the relevant store is
// not wired (WithProfileRepository/WithGroupRepository not called).

func (s *WSServer) handleProfileMethod(wconn *wsConn, req jsonrpcRequest) {
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
		// Secret references stay backend-owned: hand the renderer row handles.
		for i := range profs {
			profs[i] = wireProfile(profs[i])
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(profs)))
	case "profiles.create":
		var p profile.SSHProfile
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		// Mint an ID when the renderer sends none.
		if p.ID == "" {
			p.ID = profile.NewProfileID("ssh", p.Name)
		}
		// The renderer names secrets by row handle; resolve to references
		// before storage.
		var wireErr error
		if p.Options, wireErr = s.optionsFromWire(p.Options); wireErr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, wireErr.Error()))
			return
		}
		if err := s.profiles.CreateProfile(p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(wireProfile(p))))
	case "profiles.update":
		var p profile.SSHProfile
		if err := json.Unmarshal(req.Params, &p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if p.ID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "id required"))
			return
		}
		var wireErr error
		if p.Options, wireErr = s.optionsFromWire(p.Options); wireErr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, wireErr.Error()))
			return
		}
		if err := s.profiles.UpdateProfile(p); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(wireProfile(p))))
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
	case "profiles.effective":
		s.handleEffective(wconn, req)
	case "profiles.patch":
		s.handlePatch(wconn, req)
	}
}

func (s *WSServer) handleGroupMethod(wconn *wsConn, req jsonrpcRequest) {
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
		// The renderer addresses secret bindings by row handle (ADR-0011 §2):
		// convert every stored reference in the defaults before marshaling.
		for i := range groups {
			groups[i] = wireGroup(groups[i])
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(groups)))
	case "groups.create":
		var g profile.ProfileGroup
		if err := json.Unmarshal(req.Params, &g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		// Mint an ID when the renderer sends none, as profiles.create does.
		if g.ID == "" {
			g.ID = profile.NewGroupID(g.Name)
		}
		// Resolve the defaults' row handles to stored references (ADR-0011 §2)
		// so storage never holds a secrow handle.
		wg, werr := s.groupFromWire(g)
		if werr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, werr.Error()))
			return
		}
		g = wg
		if err := s.groups.CreateGroup(g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(wireGroup(g))))
	case "groups.update":
		var g profile.ProfileGroup
		if err := json.Unmarshal(req.Params, &g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if g.ID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "id required"))
			return
		}
		// Resolve the defaults' row handles before comparing against storage,
		// or the guard below would see every secret binding as a change.
		wg, werr := s.groupFromWire(g)
		if werr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, werr.Error()))
			return
		}
		g = wg
		// Guard: ParentGroupID and Defaults cannot be changed through generic
		// CRUD — the renderer MUST use groups.impact + groups.apply.
		allGroups, loadErr := s.groups.LoadGroups()
		if loadErr != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, loadErr.Error()))
			return
		}
		var stored *profile.ProfileGroup
		for i := range allGroups {
			if allGroups[i].ID == g.ID {
				stored = &allGroups[i]
				break
			}
		}
		if stored == nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "group not found"))
			return
		}
		if g.ParentGroupID != stored.ParentGroupID {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602,
				"ParentGroupId can only be changed through groups.apply, not groups.update"))
			return
		}
		if defaultsChanged(stored.Defaults, g.Defaults) {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602,
				"Defaults can only be changed through groups.apply, not groups.update"))
			return
		}
		if err := s.groups.UpdateGroup(g); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(wireGroup(g))))
	case "groups.delete":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params"))
			return
		}
		if params.ID == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "id required"))
			return
		}

		// Use atomic delete (promotes children to root).
		ad, ok := s.groups.(interface{ DeleteGroupAtomic(string) error })
		if !ok {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "group store does not support atomic delete"))
			return
		}
		if err := ad.DeleteGroupAtomic(params.ID); err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, profileMethodErrorCode(err), err.Error()))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(true)))
	}
}

// profileMethodErrorCode maps a store error to a JSON-RPC code, same pattern
// as credentialErrorCode.
func profileMethodErrorCode(err error) int {
	switch {
	case errors.Is(err, profile.ErrProfileExists),
		errors.Is(err, profile.ErrProfileNotFound),
		errors.Is(err, profile.ErrProfileIDRequired),
		errors.Is(err, profile.ErrGroupExists),
		errors.Is(err, profile.ErrGroupNotFound),
		errors.Is(err, profile.ErrGroupIDRequired):
		return -32602
	default:
		return -32603
	}
}

// defaultsChanged reports whether two ProfileDefaults blocks differ.
// Both nil and empty defaults are treated as equivalent.
func defaultsChanged(a, b *profile.ProfileDefaults) bool {
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil {
		return true
	}
	aJSON, errA := a.MarshalJSON()
	bJSON, errB := b.MarshalJSON()
	if errA != nil || errB != nil {
		return true
	}
	return string(aJSON) != string(bJSON)
}

// createSecret stores a secret with its catalogue metadata (ADR-0016): the
// display name generated by the connection editor (or asked of the user on
// the Secrets page) and the kind, both riding the vault's create sequence.
// When the vault lifecycle is not wired, the plain store is used and the
// secret records namelessly, rendering by fallback.
func (s *WSServer) createSecret(ctx context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	if s.vaultLifecycle != nil {
		return s.vaultLifecycle.CreateNamed(ctx, value, meta)
	}
	return s.credentials.Create(ctx, value)
}

// errInvalidKeyMaterial is returned when key text does not parse as a private
// key. It maps to error.data.reason: "invalid-key" in the JSON-RPC response.
type errInvalidKeyMaterial struct{ msg string }

// errInvalidKeyPassphrase is returned when a key passphrase cannot be stored:
// it does not open the stored key, or there is no stored key to verify it
// against. Maps to error.data.reason: "invalid-key-passphrase".
type errInvalidKeyPassphrase struct{ msg string }

func (e *errInvalidKeyPassphrase) Error() string { return e.msg }

func (e *errInvalidKeyMaterial) Error() string { return e.msg }

func parsePrivateKeyMaterial(keyText string) (fingerprint string, passphraseWanted bool, err error) {
	keyBytes := []byte(keyText)
	parsed, parseErr := gossh.ParseRawPrivateKey(keyBytes)
	if parseErr == nil {
		// Unencrypted key — wrap as signer and extract fingerprint.
		signer, signerErr := gossh.NewSignerFromKey(parsed)
		if signerErr != nil {
			return "", false, &errInvalidKeyMaterial{msg: fmt.Sprintf("cannot create signer from key: %v", signerErr)}
		}
		return gossh.FingerprintSHA256(signer.PublicKey()), false, nil
	}

	// Encrypted or otherwise unparseable.
	var passErr *gossh.PassphraseMissingError
	if errors.As(parseErr, &passErr) {
		if passErr.PublicKey != nil {
			// OpenSSH format encrypted key — public half is readable.
			return gossh.FingerprintSHA256(passErr.PublicKey), true, nil
		}
		// Traditional PEM-encrypted key: readable, usable, and its public half
		// is behind the passphrase we were not given.
		//
		// This used to be rejected, telling the user to convert the key. That
		// was wrong twice. The key works — ssh_auth.go already opens exactly
		// this shape with ParsePrivateKeyWithPassphrase, and it works in every
		// other client — so refusing it turned "I cannot compute a fingerprint
		// yet" into "your key is invalid". And the remedy quoted was not one:
		// RFC4716 is a PUBLIC key format, so the command would not have
		// converted the private key at all.
		//
		// The fingerprint is left empty, which this function's own contract
		// has always permitted. Empty means unknown-until-unlocked, not
		// absent: nothing downstream may treat it as an identity. The renderer
		// is told it wants a passphrase, so the empty fingerprint is never a
		// silent absence.
		return "", true, nil
	}

	return "", false, &errInvalidKeyMaterial{msg: fmt.Sprintf("not a valid private key: %v", parseErr)}
}

// handleImportTabby parses a Tabby config YAML and imports SSH profiles,
// groups, and optionally vault secrets into the wired profile/group
// repositories and SecretStore.
//
// When the config carries an encrypted vault section and passphrase is provided,
// every decrypted secret is stored via Vault.Create and bound to the profile
// whose connection target the tabby vault keyed it to, by host+port+user. An
// encrypted vault without a passphrase is an error.
//
// Order (ADR-0011 §4): secrets are written first, then the metadata that
// references them. A crash mid-import orphans secrets — reconciliation
// recovers on next start. The reverse would leave metadata pointing at
// nothing, which is a broken profile the user must repair by hand.
//
// Collision policy (nocx-y910.1): profiles and groups overwrite on duplicate
// ID.
//
// The passphrase is a parameter of the operation, asked once, stored nowhere
// — no field, no cache, no package variable.
// privateKeyLabel names the secret that carries an imported private-key
// passphrase. Tabby identifies the key only by a hash, which is unreadable on
// its own, so the label says what it is and keeps enough of the hash to tell
// two of them apart.
// ── Tabby import preview types ──────────────────────────────────────────

// TabbyPreviewResponse is returned by profiles.tabbyPreview.
type TabbyPreviewResponse struct {
	ProfilesToImport int             `json:"profilesToImport"`
	GroupsToImport   int             `json:"groupsToImport"`
	SecretsToImport  int             `json:"secretsToImport"`
	ProfileEntries   []ProfileEntry  `json:"profileEntries,omitempty"`
	GroupNames       []string        `json:"groupNames,omitempty"`
	SecretEntries    []SecretEntry   `json:"secretEntries,omitempty"`
	SkippedSecrets   []SkippedInfo   `json:"skippedSecrets,omitempty"`
	Collisions       []CollisionInfo `json:"collisions,omitempty"`
	SecretProvider   string          `json:"secretProvider"`
	PlanToken        string          `json:"planToken"`
}

// ProfileEntry describes one profile the import would create or modify.
type ProfileEntry struct {
	Name   string `json:"name"`
	Action string `json:"action"` // "new", "overwrite", "needs-review"
}

// SecretEntry describes one secret the import would create.
type SecretEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "password" or "passphrase"
}

// SkippedInfo describes one skipped secret and why.
type SkippedInfo struct {
	SecretType string `json:"secretType"`
	Reason     string `json:"reason"`
}

// CollisionInfo describes one collision in an import plan.
type CollisionInfo struct {
	Kind   string `json:"kind"`   // "profile", "group", "credential"
	Name   string `json:"name"`   // the identifier that collides
	Policy string `json:"policy"` // "overwrite", "refuse", "needs-review"
}

// tabbyExecuteParams is the payload for profiles.tabbyExecute.
type tabbyExecuteParams struct {
	PlanToken string `json:"planToken"`
}

// planTabbyImport parses a Tabby config, decrypts its vault (if passphrase
// supplied), and plans every profile, group, and secret WITHOUT writing
// anything. Returns the full importPlan for execution and a preview response
// for the renderer. The plan is stored server-side by the returned token.
func (s *WSServer) planTabbyImport(configYAML, passphrase string) (*importPlan, *TabbyPreviewResponse, error) {
	cfg, err := importer.ParseTabbyConfig([]byte(configYAML))
	if err != nil {
		return nil, nil, err
	}

	// Decrypt vault and build secret plans. Each password plan carries
	// the connection target the tabby vault keyed it to, so execution can
	// bind the minted secret onto the right profile (ADR-0017 §1).
	var credentials []credentialPlan
	skipped := make([]SkippedInfo, 0)

	if cfg.Vault != nil && cfg.Vault.Encrypted {
		if passphrase == "" {
			return nil, nil, errors.New("vault is encrypted: passphrase required")
		}
		vaultContents, decryptErr := importer.DecryptTabbyVault(cfg.Vault, passphrase)
		if decryptErr != nil {
			return nil, nil, decryptErr
		}

		for _, sec := range vaultContents.DecodedSecrets() {
			var val string
			umErr := json.Unmarshal(sec.Value, &val)
			if umErr != nil || val == "" {
				skipped = append(skipped, SkippedInfo{
					SecretType: sec.Type,
					Reason:     "unreadable value",
				})
				continue
			}
			switch sec.Type {
			case "ssh:password":
				var t struct {
					User string `json:"user"`
					Host string `json:"host"`
					Port int    `json:"port"`
				}
				umErr = json.Unmarshal(sec.Key, &t)
				if umErr != nil || t.Host == "" {
					skipped = append(skipped, SkippedInfo{
						SecretType: sec.Type,
						Reason:     "unreadable key (missing host)",
					})
					continue
				}
				name := t.User + "@" + t.Host
				credentials = append(credentials, credentialPlan{
					name:       name,
					secret:     val,
					targetUser: t.User,
					targetHost: t.Host,
					targetPort: t.Port,
				})

			case "ssh:key-passphrase":
				var k struct {
					Hash string `json:"hash"`
				}
				umErr = json.Unmarshal(sec.Key, &k)
				if umErr != nil || k.Hash == "" {
					skipped = append(skipped, SkippedInfo{
						SecretType: sec.Type,
						Reason:     "unreadable key (missing hash)",
					})
					continue
				}
				keyName := privateKeyLabel(k.Hash)
				credentials = append(credentials, credentialPlan{
					name:         keyName,
					secret:       val,
					isPassphrase: true,
				})

			default:
				skipped = append(skipped, SkippedInfo{
					SecretType: sec.Type,
					Reason:     "unhandled secret type",
				})
			}
		}
	}

	// Convert profiles.
	var profiles []profile.SSHProfile
	for _, tp := range cfg.Profiles {
		if tp.Type != "ssh" {
			continue
		}
		p := importer.ConvertProfile(tp)
		// Profiles no longer link to credentials (ADR-0017): a profile's
		// secret references are backend-owned, and an import brings none.
		profiles = append(profiles, p)
	}

	// Convert groups.
	var groups []profile.ProfileGroup
	for _, tg := range cfg.Groups {
		var defaults *profile.ProfileDefaults
		if tg.Defaults != nil {
			d, decodeErr := profile.DecodeDefaults(tg.Defaults)
			if decodeErr != nil {
				return nil, nil, fmt.Errorf("group %q defaults: %w", tg.Name, decodeErr)
			}
			defaults = &d
		}
		groups = append(groups, profile.ProfileGroup{
			ID:            tg.ID,
			ParentGroupID: tg.ParentGroupID,
			Name:          tg.Name,
			Icon:          tg.Icon,
			Color:         tg.Color,
			Defaults:      defaults,
			Editable:      true,
		})
	}

	// Build per-entry preview lists.
	profileEntries := make([]ProfileEntry, 0, len(profiles))
	groupNames := make([]string, 0, len(groups))
	secretEntries := make([]SecretEntry, 0, len(credentials))

	// Determine which profiles collide (for setting their action).
	existingProfileIDs := make(map[string]bool)
	if s.profiles != nil {
		existingProfs, _ := s.profiles.LoadProfiles()
		for _, p := range existingProfs {
			existingProfileIDs[p.ID] = true
		}
	}
	for _, p := range profiles {
		action := "new"
		if p.ID != "" && existingProfileIDs[p.ID] {
			action = "overwrite"
		}
		// No import-time credential linking remains (ADR-0017): a profile's
		// secret references are backend-owned and imports carry none.
		profileEntries = append(profileEntries, ProfileEntry{Name: p.Name, Action: action})
	}
	for _, g := range groups {
		groupNames = append(groupNames, g.Name)
	}
	for _, cp := range credentials {
		typ := "password"
		if cp.isPassphrase {
			typ = "passphrase"
		}
		secretEntries = append(secretEntries, SecretEntry{Name: cp.name, Type: typ})
	}

	// Build preview response with collision info.
	preview := &TabbyPreviewResponse{
		ProfilesToImport: len(profiles),
		GroupsToImport:   len(groups),
		SecretsToImport:  len(credentials),
		ProfileEntries:   profileEntries,
		GroupNames:       groupNames,
		SecretEntries:    secretEntries,
		SkippedSecrets:   skipped,
	}

	// Detect collisions by checking against current store state.
	if s.profiles != nil {
		existingProfs, _ := s.profiles.LoadProfiles()
		existingIDs := make(map[string]bool, len(existingProfs))
		for _, p := range existingProfs {
			existingIDs[p.ID] = true
		}
		for _, p := range profiles {
			if p.ID != "" && existingIDs[p.ID] {
				preview.Collisions = append(preview.Collisions, CollisionInfo{
					Kind:   "profile",
					Name:   p.Name,
					Policy: "overwrite",
				})
			}
		}
	}

	if s.groups != nil {
		existingGroups, _ := s.groups.LoadGroups()
		existingIDs := make(map[string]bool, len(existingGroups))
		for _, g := range existingGroups {
			existingIDs[g.ID] = true
		}
		for _, g := range groups {
			if g.ID != "" && existingIDs[g.ID] {
				preview.Collisions = append(preview.Collisions, CollisionInfo{
					Kind:   "group",
					Name:   g.Name,
					Policy: "overwrite",
				})
			}
		}
	}

	// Determine secret provider.
	preview.SecretProvider = s.secretProviderName()

	// Build the plan and store it.
	plan := &importPlan{
		profiles: profiles,
		groups:   groups,
		creds:    credentials,
	}
	token, err := s.storePlan(plan)
	if err != nil {
		return nil, nil, fmt.Errorf("store plan: %w", err)
	}
	preview.PlanToken = token

	return plan, preview, nil
}

// secretProviderName returns a human-readable name for where secrets would be
// stored. Uses the vault lifecycle if wired, otherwise returns "secret store".
func (s *WSServer) secretProviderName() string {
	if s.vaultLifecycle == nil {
		return "secret store"
	}
	snap := s.vaultLifecycle.Snapshot(context.Background())
	for _, p := range snap.Providers {
		if string(p.ID) == "system" && p.Writable && p.Ready {
			return "OS keychain"
		}
	}
	return "encrypted file"
}

// handleTabbyPreview parses a Tabby config and returns a preview of what
// would be imported, without writing anything. Uses planTabbyImport for the
// shared planning logic.
func (s *WSServer) handleTabbyPreview(wconn *wsConn, req jsonrpcRequest) {
	if s.profiles == nil || s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}
	var params struct {
		Config     string `json:"config"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Config == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: config (YAML string) required"))
		return
	}

	plan, preview, err := s.planTabbyImport(params.Config, params.Passphrase)
	if err != nil {
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "Tabby preview: ", err))
		return
	}
	_ = plan // stored server-side by preview.PlanToken
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(preview)))
}

// handleTabbyExecute executes a previously previewed Tabby import plan.
// Takes the plan token from the preview response.
func (s *WSServer) handleTabbyExecute(wconn *wsConn, req jsonrpcRequest) {
	if s.profiles == nil || s.groups == nil || s.credentials == nil || s.profileSvc == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "import not available"))
		return
	}
	var params tabbyExecuteParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.PlanToken == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: planToken required"))
		return
	}

	// Claim the plan so concurrent calls for the same token are rejected.
	plan := s.claimPlan(params.PlanToken)
	if plan == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Plan not found, expired, or already in progress. Please preview again."))
		return
	}

	// On any failure, release the plan for retry (vault setup/unlock flow).
	var succeeded bool
	defer func() {
		if !succeeded {
			s.releasePlan(params.PlanToken)
		}
	}()

	// Mint every secret, binding each password onto the profile whose options
	// match the target the tabby vault keyed it to (ADR-0017 §1). Passphrases
	// are minted as unbound rows: a passphrase belongs to a private key the
	// import cannot fingerprint, and the connection editor binds it.
	ctx := context.Background()
	for _, cp := range plan.creds {
		kind := vault.KindPassword
		if cp.isPassphrase {
			kind = vault.KindKeyPassphrase
		}
		secretID, err := s.createSecret(ctx, credential.NewSecret(cp.secret),
			vault.SecretMeta{Name: cp.name, Kind: kind})
		if err != nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "Store secret: ", err))
			return
		}
		if cp.isPassphrase {
			continue
		}
		for i := range plan.profiles {
			o := &plan.profiles[i].Options
			port := 0
			if o.Port != nil {
				port = *o.Port
			}
			user := ""
			if o.User != nil {
				user = *o.User
			}
			if user == cp.targetUser && o.Host == cp.targetHost && port == cp.targetPort {
				o.PasswordSecret = string(secretID)
				break
			}
		}
	}

	// No credential records are imported: the bindings live on the profiles.
	result := s.profileSvc.AtomicImport(plan.profiles, plan.groups)
	if len(result.ImportErrors) > 0 {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Import failed: "+result.ImportErrors[0]))
		return
	}

	// All writes succeeded — remove the plan permanently.
	s.finishPlan(params.PlanToken)
	succeeded = true
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(result)))
}

func privateKeyLabel(hash string) string {
	short := hash
	if len(short) > 8 {
		short = short[:8]
	}
	return "Tabby key " + short
}

func (s *WSServer) handleImportTabby(wconn *wsConn, req jsonrpcRequest) {
	if s.profiles == nil || s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "profiles not available"))
		return
	}
	var params struct {
		Config     string `json:"config"`
		Passphrase string `json:"passphrase,omitempty"`
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

	// Decrypt vault and build credentials + profile matching.
	// Profiles carry their secret bindings directly (ADR-0017): the minted
	// password reference goes into the profile's own options, matched by the
	// connection target the tabby vault keyed it to.
	type pwKey struct {
		user, host string
		port       int
	}
	pwLookup := make(map[pwKey]credential.SecretID)

	if cfg.Vault != nil && cfg.Vault.Encrypted {
		if s.credentials == nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "Store secret: ", errors.New("credential store not available")))
			return
		}
		if params.Passphrase == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Vault is encrypted: passphrase required"))
			return
		}
		vaultContents, err := importer.DecryptTabbyVault(cfg.Vault, params.Passphrase)
		if err != nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "Decrypt vault: ", err))
			return
		}

		// Plan every secret before creating any, so a shape we cannot read
		// never leaves an orphaned secret behind.
		//
		// A secret we cannot interpret is SKIPPED, never fatal. Tabby's vault
		// is shared by every plugin the user has installed, so an unknown type
		// is normal rather than exceptional — and aborting on one would throw
		// away the profiles and groups that imported fine. The shapes below are
		// verified against tabby-ssh/src/services/passwordStorage.service.ts.
		type secretPlan struct {
			ts        importer.TabbySecret
			val       string
			keyName   string // private-key identifier (key-passphrase)
			keyTarget *pwKey // connection target (password)
		}
		plans := make([]secretPlan, 0, len(vaultContents.DecodedSecrets()))
		skipped := 0
		for _, sec := range vaultContents.DecodedSecrets() {
			var val string
			if err := json.Unmarshal(sec.Value, &val); err != nil || val == "" {
				s.log.Warn("tabby import: skipping secret with unreadable value", "type", sec.Type)
				skipped++
				continue
			}
			switch sec.Type {
			case "ssh:password":
				// getVaultKeyForConnection → {user, host, port}
				var t struct {
					User string `json:"user"`
					Host string `json:"host"`
					Port int    `json:"port"`
				}
				if err := json.Unmarshal(sec.Key, &t); err != nil || t.Host == "" {
					s.log.Warn("tabby import: skipping password secret with unreadable key")
					skipped++
					continue
				}
				plans = append(plans, secretPlan{
					ts:        sec,
					val:       val,
					keyTarget: &pwKey{user: t.User, host: t.Host, port: t.Port},
				})
			case "ssh:key-passphrase":
				// getVaultKeyForPrivateKey → {hash: id}. It is an object, not a
				// string: reading it as a string failed for every real Tabby
				// vault and, before this, aborted the whole import.
				var k struct {
					Hash string `json:"hash"`
				}
				if err := json.Unmarshal(sec.Key, &k); err != nil || k.Hash == "" {
					s.log.Warn("tabby import: skipping key-passphrase secret with unreadable key")
					skipped++
					continue
				}
				plans = append(plans, secretPlan{ts: sec, val: val, keyName: privateKeyLabel(k.Hash)})
			default:
				// Everything else, including Tabby's "file" secrets. Those hold
				// base64 file CONTENT — usually a private key — which is not a
				// credential secret and does not belong in a password slot.
				// Importing key material is its own feature, not a side effect
				// of this one.
				s.log.Info("tabby import: skipping secret of unhandled type", "type", sec.Type)
				skipped++
			}
		}
		if skipped > 0 {
			s.log.Info("tabby import: some vault secrets were not imported", "skipped", skipped, "imported", len(plans))
		}

		// All secrets validated. Create each one in the SecretStore, carrying
		// the name the credential will bear (ADR-0016: the secret owns its
		// name, and an import mints both together).
		ctx := context.Background()
		for _, p := range plans {
			name := p.keyName
			kind := vault.KindKeyPassphrase
			if p.ts.Type == "ssh:password" {
				name = p.keyTarget.user + "@" + p.keyTarget.host
				kind = vault.KindPassword
			}
			secretID, err := s.createSecret(ctx, credential.NewSecret(p.val),
				vault.SecretMeta{Name: name, Kind: kind})
			if err != nil {
				_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "Store secret: ", err))
				return
			}
			switch p.ts.Type {
			case "ssh:password":
				// The secret is bound to the connection it belongs to; no
				// credential record is minted (ADR-0017 §1).
				pwLookup[*p.keyTarget] = secretID
			case "ssh:key-passphrase":
				// Passphrases stay unbound rows: a passphrase belongs to a
				// private key, and the imported key is a path whose
				// fingerprint is not readable at import time. The connection
				// editor's secret picker binds it where the user chooses.
				_ = p.keyName
			}
		}
	}

	// Domain service path: atomic import.
	var profiles []profile.SSHProfile
	for _, tp := range cfg.Profiles {
		if tp.Type != "ssh" {
			continue
		}
		p := importer.ConvertProfile(tp)
		if p.Options.User != nil && p.Options.Host != "" {
			port := 0
			if p.Options.Port != nil {
				port = *p.Options.Port
			}
			user := ""
			if p.Options.User != nil {
				user = *p.Options.User
			}
			if secretID, ok := pwLookup[pwKey{user: user, host: p.Options.Host, port: port}]; ok {
				p.Options.PasswordSecret = string(secretID)
			}
		}
		profiles = append(profiles, p)
	}

	var groups []profile.ProfileGroup
	for _, tg := range cfg.Groups {
		var defaults *profile.ProfileDefaults
		if tg.Defaults != nil {
			d, err := profile.DecodeDefaults(tg.Defaults)
			if err != nil {
				_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, fmt.Sprintf("Import failed: group %q defaults: %v", tg.Name, err)))
				return
			}
			defaults = &d
		}
		groups = append(groups, profile.ProfileGroup{
			ID:            tg.ID,
			ParentGroupID: tg.ParentGroupID,
			Name:          tg.Name,
			Icon:          tg.Icon,
			Color:         tg.Color,
			Defaults:      defaults,
			Editable:      true,
		})
	}

	result := s.profileSvc.AtomicImport(profiles, groups)
	if len(result.ImportErrors) > 0 {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Import failed: "+result.ImportErrors[0]))
		return
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(result.ProfilesImported)))
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
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "settings.secretSet: ", err))
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
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "settings.secretDelete: ", err))
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
		panic("marshal: " + err.Error())
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

// unregisterConn removes a connection from the broadcast set and destroys
// its pending captures: a transport disconnect and a tab closure are both
// on the capture contract's destruction list.
func (s *WSServer) unregisterConn(wc *wsConn) {
	s.connsMu.Lock()
	delete(s.conns, wc)
	s.connsMu.Unlock()
	if s.captures != nil {
		s.captures.DestroyTab(strconv.FormatUint(wc.id, 10))
	}
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
