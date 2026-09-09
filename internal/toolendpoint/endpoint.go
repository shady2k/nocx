package toolendpoint

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/coordinator"
	"github.com/shady2k/nocx/internal/workers"
)

const (
	toolSocketName    = "tool.sock"
	maxEnvelopeBytes  = 256 << 10
	requestReadWindow = 11 * time.Minute
)

var (
	errOversizedEnvelope  = errors.New("toolendpoint: request envelope exceeds the size bound")
	errIncompleteEnvelope = errors.New("toolendpoint: request did not end with a newline")
)

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
	rpcDomainError    = -32000
	rpcPeerRefused    = -32001
)

// ObservationKind names the endpoint event that the composition root may
// project onto a user-visible session fact.
type ObservationKind string

const (
	ObservationAdmitted  ObservationKind = "admitted"
	ObservationCatalogue ObservationKind = "catalogue"
	ObservationRefusal   ObservationKind = "refusal"
)

// Observation is what the endpoint saw on one launch's tool-surface path.
// SessionID is empty when a peer was refused before the authorizer could bind
// it to a session; an authorizer may implement PeerSessionResolver to provide
// that binding for refusal observations.
type Observation struct {
	SessionID string
	Kind      ObservationKind
	Reason    string
}

// Observer receives endpoint observations. The endpoint reports facts only;
// the composition root owns deadlines, deduplication and presentation.
type Observer interface {
	Observe(Observation)
}

// PeerSessionResolver optionally supplies the session a refused peer was
// attempting to reach. It is separate from Authorizer so existing authorizers
// remain valid and refusals stay fail-closed when no binding is available.
type PeerSessionResolver interface {
	SessionForPeer(Peer) (string, bool)
}

// Config is everything the endpoint needs, supplied by the composition root.
// A nil dependency is an error rather than a permissive default: an endpoint
// without an authorizer would turn same-uid membership into authority.
type Config struct {
	// Dir is the runtime directory shared with the coordinator discovery socket.
	Dir string
	// Peers reads kernel-stamped identity assertions from accepted connections.
	Peers coordinator.PeerCredentials
	// Owner checks ownership of Dir without following its final symlink.
	Owner coordinator.PathOwner
	// SelfUID is the uid that an accepted local peer must have before admission.
	SelfUID uint32
	// Auth binds an accepted peer to a session, grant and run context.
	Auth Authorizer
	// Dispatch runs the common worker declaration and executor pipeline.
	Dispatch assistant.ToolDispatcher
	// Observer receives admission, refusal and catalogue observations for the
	// composition root's session-level tool-surface monitor.
	Observer Observer
	// Logger receives lifecycle diagnostics and never receives request payloads.
	Logger *slog.Logger
}

// Endpoint owns the local worker JSON-RPC socket.
type Endpoint struct {
	cfg    Config
	socket string

	mu       sync.Mutex
	listener *net.UnixListener
	closed   bool
	conns    map[*net.UnixConn]struct{}
	wait     sync.WaitGroup
}

// New validates the endpoint configuration and computes its socket path. It
// touches no filesystem; failures against the runtime directory happen in
// Start, after the caller is ready to handle them.
func New(cfg Config) (*Endpoint, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("toolendpoint: no runtime directory")
	case !filepath.IsAbs(cfg.Dir):
		return nil, fmt.Errorf("toolendpoint: runtime directory %q is not absolute", cfg.Dir)
	case cfg.Peers == nil:
		return nil, errors.New("toolendpoint: no peer credentials")
	case cfg.Owner == nil:
		return nil, errors.New("toolendpoint: no path owner")
	case isNilDependency(cfg.Auth):
		return nil, errors.New("toolendpoint: no authorizer")
	case isNilDependency(cfg.Dispatch):
		return nil, errors.New("toolendpoint: no worker dispatcher")
	// The catalogue is what lets a caller obey "an unreachable tool is not
	// offered": without it the endpoint can execute calls it cannot enumerate,
	// and a caller has to guess its own eligibility. Required at composition
	// rather than discovered on the first tools.catalogue, because a
	// capability found missing at call time is a soft degrade nobody sees —
	// the caller gets one refused method and goes on believing the surface is
	// whole.
	case !implementsCatalogue(cfg.Dispatch):
		return nil, errors.New("toolendpoint: dispatcher cannot enumerate what a grant admits")
	case cfg.Logger == nil:
		return nil, errors.New("toolendpoint: no logger")
	}
	return &Endpoint{
		cfg:    cfg,
		socket: filepath.Join(cfg.Dir, toolSocketName),
		conns:  make(map[*net.UnixConn]struct{}),
	}, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// SocketPath returns the endpoint path before or after Start.
func (e *Endpoint) SocketPath() string { return e.socket }

// Start prepares the shared runtime directory, publishes tool.sock atomically
// through the coordinator's path-security answer, and begins accepting peers.
func (e *Endpoint) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("toolendpoint: endpoint is closed")
	}
	if e.listener != nil {
		return errors.New("toolendpoint: endpoint is already started")
	}
	if err := coordinator.PrepareRuntimeDir(e.cfg.Dir, e.cfg.Owner, e.cfg.SelfUID); err != nil {
		return err
	}
	listener, err := coordinator.BindSocket(e.cfg.Dir, toolSocketName)
	if err != nil {
		return err
	}
	e.listener = listener
	e.wait.Add(1)
	go e.accept(listener)
	return nil
}

// Close stops accepting, closes active connections so their request contexts
// are canceled, waits for handlers, and removes the published socket. It is
// idempotent.
func (e *Endpoint) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	listener := e.listener
	started := listener != nil
	e.listener = nil
	connections := make([]*net.UnixConn, 0, len(e.conns))
	for conn := range e.conns {
		connections = append(connections, conn)
	}
	e.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	e.wait.Wait()
	if !started {
		return nil
	}
	if err := os.Remove(e.socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("toolendpoint: remove socket: %w", err)
	}
	return nil
}

func (e *Endpoint) accept(listener *net.UnixListener) {
	defer e.wait.Done()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			e.cfg.Logger.Warn("toolendpoint: accept failed")
			return
		}

		// Kernel identity is read synchronously in the accept loop, before the
		// connection is handed to a request parser. No request bytes are read
		// until both uid and (when available) pid are established.
		peer, err := e.peer(conn)
		if err != nil {
			e.observePeerRefusal(Peer{}, "peer credentials unavailable")
			e.writeError(conn, nil, rpcPeerRefused, "peer credentials unavailable", "peer credentials unavailable")
			_ = conn.Close()
			continue
		}
		if peer.UID != e.cfg.SelfUID {
			e.cfg.Logger.Warn("toolendpoint: refusing foreign peer")
			e.observePeerRefusal(peer, "peer uid is not permitted")
			e.writeError(conn, nil, rpcPeerRefused, "peer uid is not permitted", "peer uid is not permitted")
			_ = conn.Close()
			continue
		}
		if !e.track(conn) {
			_ = conn.Close()
			return
		}
		go func() {
			defer e.wait.Done()
			e.serve(conn, peer)
		}()
	}
}

func (e *Endpoint) peer(conn *net.UnixConn) (Peer, error) {
	uid, err := e.cfg.Peers.PeerUID(conn)
	if err != nil {
		return Peer{}, err
	}
	if uid != e.cfg.SelfUID {
		return Peer{UID: uid}, nil
	}
	pid := 0
	if process, ok := e.cfg.Peers.(coordinator.PeerProcess); ok {
		pid, err = process.PeerPID(conn)
		if err != nil {
			return Peer{}, err
		}
	}
	return Peer{UID: uid, PID: pid}, nil
}

func (e *Endpoint) track(conn *net.UnixConn) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.conns[conn] = struct{}{}
	e.wait.Add(1)
	return true
}

func (e *Endpoint) untrack(conn *net.UnixConn) {
	e.mu.Lock()
	delete(e.conns, conn)
	e.mu.Unlock()
}

func (e *Endpoint) observe(observation Observation) {
	if e.cfg.Observer != nil {
		e.cfg.Observer.Observe(observation)
	}
}

func (e *Endpoint) observePeerRefusal(peer Peer, reason string) {
	observation := Observation{Kind: ObservationRefusal, Reason: reason}
	if resolver, ok := e.cfg.Auth.(PeerSessionResolver); ok {
		if sid, found := resolver.SessionForPeer(peer); found {
			observation.SessionID = sid
		}
	}
	e.observe(observation)
}

func (e *Endpoint) serve(conn *net.UnixConn, peer Peer) {
	defer e.untrack(conn)
	defer func() { _ = conn.Close() }()

	invocation, release, err := e.cfg.Auth.Admit(peer)
	if err != nil {
		code, message, reason := rpcErrorFor(err)
		e.observePeerRefusal(peer, reason)
		e.writeError(conn, nil, code, message, reason)
		return
	}
	base := invocation.Context
	if base == nil {
		base = context.Background()
	}
	e.observe(Observation{
		SessionID: invocation.RunContext.Session,
		Kind:      ObservationAdmitted,
	})
	connectionCtx, cancel := context.WithCancel(base)

	reader := bufio.NewReaderSize(conn, maxEnvelopeBytes)
	var requests sync.WaitGroup
	defer func() {
		cancel()
		requests.Wait()
		if release != nil {
			release()
		}
	}()
	var inFlight atomic.Int32
	for {
		if inFlight.Load() == 0 {
			if err := conn.SetReadDeadline(time.Now().Add(requestReadWindow)); err != nil {
				return
			}
		} else if err := conn.SetReadDeadline(time.Time{}); err != nil {
			return
		}
		line, err := readEnvelope(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errOversizedEnvelope) {
			e.writeError(conn, nil, rpcInvalidRequest, "invalid request", "request envelope exceeds the size bound")
			break
		}
		if errors.Is(err, errIncompleteEnvelope) {
			e.writeError(conn, nil, rpcParseError, "parse error", "request did not end with a newline")
			break
		}
		if err != nil {
			break
		}

		request, parseErr := decodeRequest(line)
		if parseErr != nil {
			e.writeError(conn, nil, rpcParseError, "parse error", "request is not valid JSON")
			continue
		}
		if requestNotification(request) {
			e.writeError(conn, request.ID, rpcInvalidRequest, "invalid request", "worker notifications are not supported")
			continue
		}
		if validationErr := validateRequest(request); validationErr != nil {
			e.writeError(conn, request.ID, rpcInvalidRequest, "invalid request", "request is not a valid JSON-RPC request")
			continue
		}

		requests.Add(1)
		inFlight.Add(1)
		go func(request rpcRequest) {
			defer requests.Done()
			defer func() {
				if inFlight.Add(-1) == 0 {
					_ = conn.SetReadDeadline(time.Now().Add(requestReadWindow))
				}
			}()
			requestCtx, requestCancel := context.WithCancel(connectionCtx)
			defer requestCancel()
			requestInvocation := invocation
			requestInvocation.Context = requestCtx
			requestInvocation.Method = request.Method
			requestInvocation.RawParams = append(json.RawMessage(nil), request.Params...)
			var result string
			var dispatchErr error
			if request.Method == "tools.catalogue" {
				var catalogueResult []byte
				catalogueResult, dispatchErr = buildCatalogue(e.cfg.Dispatch, requestInvocation.Grant)
				result = string(catalogueResult)
			} else {
				result, dispatchErr = e.cfg.Dispatch.Dispatch(requestInvocation)
			}
			if dispatchErr != nil {
				code, message, reason := rpcErrorFor(dispatchErr)
				if code == rpcInternalError {
					// THE ONE ERROR NOBODY CLASSIFIED, and it used to be the
					// only one that left no trace on either side: the caller
					// got two words naming no cause, and nothing was written
					// here. So the case that most needs diagnosis was the
					// case with the least evidence (nocx-1w3my).
					//
					// The wire answer stays generic on purpose — an
					// unclassified error must not spell a backend's internals
					// to an agent — which is exactly why the log has to carry
					// it.
					e.log().Error("tool endpoint: unclassified dispatch failure",
						"method", request.Method,
						"session_id", requestInvocation.RunContext.Session,
						"participant", requestInvocation.RunContext.Participant,
						"error", dispatchErr)
				}
				e.writeError(conn, request.ID, code, message, reason)
				return
			}
			if !json.Valid([]byte(result)) {
				e.writeError(conn, request.ID, rpcInternalError, "internal error", "dispatcher returned an invalid result")
				return
			}
			if request.Method == "tools.catalogue" {
				e.observe(Observation{
					SessionID: requestInvocation.RunContext.Session,
					Kind:      ObservationCatalogue,
				})
			}
			e.writeResult(conn, request.ID, json.RawMessage(result))
		}(request)

	}
}

func readEnvelope(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxEnvelopeBytes {
			return nil, errOversizedEnvelope
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errIncompleteEnvelope
		}
		return nil, err
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func decodeRequest(line []byte) (rpcRequest, error) {
	var request rpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return rpcRequest{}, err
	}
	return request, nil
}

func requestNotification(request rpcRequest) bool {
	return len(request.ID) == 0 || bytes.Equal(bytes.TrimSpace(request.ID), []byte("null"))
}

func validateRequest(request rpcRequest) error {
	if request.JSONRPC != "2.0" || request.Method == "" || requestNotification(request) {
		return errors.New("invalid JSON-RPC request")
	}
	var id any
	if err := json.Unmarshal(request.ID, &id); err != nil {
		return err
	}
	switch id.(type) {
	case string, float64:
		return nil
	default:
		return errors.New("JSON-RPC id must be a string or number")
	}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *rpcErrorData `json:"data,omitempty"`
}

type rpcErrorData struct {
	Reason string `json:"reason"`
}

func (e *Endpoint) writeResult(conn *net.UnixConn, id json.RawMessage, result json.RawMessage) {
	e.write(conn, rpcResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Result: result})
}

func (e *Endpoint) writeError(conn *net.UnixConn, id json.RawMessage, code int, message, reason string) {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		id = json.RawMessage("null")
	}
	response := rpcResponse{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...), Error: &rpcError{Code: code, Message: message}}
	if reason != "" {
		response.Error.Data = &rpcErrorData{Reason: reason}
	}
	e.write(conn, response)
}

func (e *Endpoint) write(conn *net.UnixConn, response rpcResponse) {
	line, err := json.Marshal(response)
	if err != nil {
		return
	}
	line = append(line, '\n')
	// net.UnixConn writes are serialized per connection. Without this mutex,
	// concurrent worker calls could interleave two otherwise valid JSON lines.
	connWriteMu.Lock()
	defer connWriteMu.Unlock()
	_, _ = conn.Write(line)
}

var connWriteMu sync.Mutex

// rpcErrorFor turns a dispatch failure into what an AGENT reads.
//
// THE REASON IS AN INSTRUCTION, NOT A LABEL. Every sentence here says three
// things: what happened, why, and what the caller should do next. That is not
// a preference — it is the standard this product already holds its own model
// to. internal/assistant's refusalResult answers the built-in assistant with
// "REFUSED: the person declined your call to X — it did not run. Say what you
// needed in words instead", and an external coordinator was getting "method is
// not assembled". A caller that cannot tell "you may never do this" from "try
// again" does the wrong one, and the wrong one is usually the retry.
//
// What the model actually sees: on a domain refusal the MCP adapter shows the
// reason (mcpstdio handleCall -> toolError); on everything else it shows the
// message. Both are written for that reader.
func (e *Endpoint) log() *slog.Logger {
	if e != nil && e.cfg.Logger != nil {
		return e.cfg.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func rpcErrorFor(err error) (code int, message, reason string) {
	switch {
	case errors.Is(err, assistant.ErrUnknownMethod):
		return rpcMethodNotFound, "method not found",
			"nocx has no tool by that name. Call tools.catalogue to see what this session is offered, and use one of those names."
	case errors.Is(err, assistant.ErrInvalidParams):
		return rpcInvalidParams, "invalid params",
			"the arguments do not match this tool's schema. Read the tool's schema in tools.catalogue and call it again with corrected arguments."
	case errors.Is(err, assistant.ErrUnreachableMethod):
		return rpcDomainError, "worker request refused",
			"this tool exists but is not offered to this session, so calling it again will fail the same way. Work with the tools tools.catalogue lists for you, or say in words what you needed it for."
	case errors.Is(err, assistant.ErrInvalidResult):
		return rpcDomainError, "worker request refused",
			"the tool ran and produced a result nocx could not read, so nothing can be reported about it. Do not repeat the call; say what you were trying to find out."
	case errors.Is(err, workers.ErrNotHeld):
		// OWNERSHIP. Split from the state refusal below (nocx-e5e8q): one
		// error used to carry both facts and the wire spelled them with this
		// sentence, so a coordinator refused because a delegation had ended
		// was told the participant belonged to somebody else — which it will
		// not question, and which workers.holdings contradicts on the next
		// call.
		return rpcDomainError, "worker request refused",
			"that participant is held by another session, so this session may not act on it. Call workers.holdings to see the participants that are yours."
	case errors.Is(err, workers.ErrNotDelegated):
		// STATE. The participant IS this caller's; the authority over it is
		// no longer active.
		return rpcDomainError, "worker request refused",
			"that participant is yours, but its delegation is no longer active, so it can no longer be acted on. Call workers.holdings to see its state; a participant that has ended needs nothing further from you."
	case errors.Is(err, ErrSessionCallerActive):
		return rpcPeerRefused, "worker caller refused",
			"another call from this session is still running, and nocx serves one at a time. Wait for that call to answer, then make this one."
	case errors.Is(err, ErrNotEnrolled):
		return rpcPeerRefused, "worker caller refused",
			"this process is not in a pane nocx has enrolled, so it has no worker tools. Nothing you can call will change that; tell the person their pane is not orchestrated."
	default:
		// Deliberately generic on the wire and fully logged at the call site:
		// an error nobody classified must not spell a backend's internals to
		// an agent. What it MUST do is stop the agent from retrying a call
		// that failed for a reason it cannot affect.
		return rpcInternalError, "internal error",
			"nocx failed to complete this call for a reason inside nocx, not in what you sent. Do not repeat it; carry on without this tool and say what you could not do."
	}
}
