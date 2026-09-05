package waveendpoint

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
)

const (
	waveSocketName    = "wave.sock"
	maxEnvelopeBytes  = 256 << 10
	requestReadWindow = 11 * time.Minute
)

var (
	errOversizedEnvelope  = errors.New("waveendpoint: request envelope exceeds the size bound")
	errIncompleteEnvelope = errors.New("waveendpoint: request did not end with a newline")
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
	// Dispatch runs the common wave declaration and executor pipeline.
	Dispatch assistant.WaveDispatcher
	// Logger receives lifecycle diagnostics and never receives request payloads.
	Logger *slog.Logger
}

// Endpoint owns the local wave JSON-RPC socket.
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
		return nil, errors.New("waveendpoint: no runtime directory")
	case !filepath.IsAbs(cfg.Dir):
		return nil, fmt.Errorf("waveendpoint: runtime directory %q is not absolute", cfg.Dir)
	case cfg.Peers == nil:
		return nil, errors.New("waveendpoint: no peer credentials")
	case cfg.Owner == nil:
		return nil, errors.New("waveendpoint: no path owner")
	case isNilDependency(cfg.Auth):
		return nil, errors.New("waveendpoint: no authorizer")
	case isNilDependency(cfg.Dispatch):
		return nil, errors.New("waveendpoint: no wave dispatcher")
	case cfg.Logger == nil:
		return nil, errors.New("waveendpoint: no logger")
	}
	return &Endpoint{
		cfg:    cfg,
		socket: filepath.Join(cfg.Dir, waveSocketName),
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

// Start prepares the shared runtime directory, publishes wave.sock atomically
// through the coordinator's path-security answer, and begins accepting peers.
func (e *Endpoint) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("waveendpoint: endpoint is closed")
	}
	if e.listener != nil {
		return errors.New("waveendpoint: endpoint is already started")
	}
	if err := coordinator.PrepareRuntimeDir(e.cfg.Dir, e.cfg.Owner, e.cfg.SelfUID); err != nil {
		return err
	}
	listener, err := coordinator.BindSocket(e.cfg.Dir, waveSocketName)
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
		return fmt.Errorf("waveendpoint: remove socket: %w", err)
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
			e.cfg.Logger.Warn("waveendpoint: accept failed")
			return
		}

		// Kernel identity is read synchronously in the accept loop, before the
		// connection is handed to a request parser. No request bytes are read
		// until both uid and (when available) pid are established.
		peer, err := e.peer(conn)
		if err != nil {
			e.writeError(conn, nil, rpcPeerRefused, "peer credentials unavailable", "peer credentials unavailable")
			_ = conn.Close()
			continue
		}
		if peer.UID != e.cfg.SelfUID {
			e.cfg.Logger.Warn("waveendpoint: refusing foreign peer")
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

func (e *Endpoint) serve(conn *net.UnixConn, peer Peer) {
	defer e.untrack(conn)
	defer func() { _ = conn.Close() }()

	invocation, err := e.cfg.Auth.Admit(peer)
	if err != nil {
		code, message, reason := rpcErrorFor(err)
		e.writeError(conn, nil, code, message, reason)
		return
	}
	base := invocation.Context
	if base == nil {
		base = context.Background()
	}
	connectionCtx, cancel := context.WithCancel(base)
	defer cancel()

	reader := bufio.NewReaderSize(conn, maxEnvelopeBytes)
	var requests sync.WaitGroup
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
			e.writeError(conn, request.ID, rpcInvalidRequest, "invalid request", "wave notifications are not supported")
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
			result, dispatchErr := e.cfg.Dispatch.Dispatch(requestInvocation)
			if dispatchErr != nil {
				code, message, reason := rpcErrorFor(dispatchErr)
				e.writeError(conn, request.ID, code, message, reason)
				return
			}
			if !json.Valid([]byte(result)) {
				e.writeError(conn, request.ID, rpcInternalError, "internal error", "dispatcher returned an invalid result")
				return
			}
			e.writeResult(conn, request.ID, json.RawMessage(result))
		}(request)

	}
	cancel()
	requests.Wait()
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
	// concurrent wave calls could interleave two otherwise valid JSON lines.
	connWriteMu.Lock()
	defer connWriteMu.Unlock()
	_, _ = conn.Write(line)
}

var connWriteMu sync.Mutex

func rpcErrorFor(err error) (code int, message, reason string) {
	switch {
	case errors.Is(err, assistant.ErrUnknownMethod):
		return rpcMethodNotFound, "method not found", "method is not assembled"
	case errors.Is(err, assistant.ErrInvalidParams):
		return rpcInvalidParams, "invalid params", "params do not match the method contract"
	case errors.Is(err, assistant.ErrUnreachableMethod):
		return rpcDomainError, "wave request refused", "method is not reachable for the bound grant"
	case errors.Is(err, assistant.ErrInvalidResult):
		return rpcDomainError, "wave request refused", "dispatcher returned an invalid result"
	case errors.Is(err, ErrNotEnrolled):
		return rpcPeerRefused, "wave caller refused", "caller is not in an enrolled process tree"
	default:
		return rpcInternalError, "internal error", "wave request failed"
	}
}
