// Package mcpstdio adapts the local newline-delimited JSON-RPC endpoint to MCP
// stdio. The package owns only protocol translation and transport lifecycle;
// the endpoint remains the source of available methods and their schemas.
package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	nocxlog "github.com/shady2k/nocx/internal/log"
)

const (
	maxMessageBytes = 256 << 10
	latestVersion   = "2025-11-25"
)

var supportedVersions = [...]string{
	"2024-11-05",
	"2025-03-26",
	"2025-06-18",
	latestVersion,
}

var (
	ErrInvalidConfig       = errors.New("mcpstdio: invalid configuration")
	ErrEndpointUnavailable = errors.New("mcpstdio: tool endpoint unavailable")
)

// Dialer is the narrow connection seam used to reach the local endpoint.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Config supplies the adapter's transport and identity. ServerName and
// ServerVersion are reported during MCP initialization and do not affect the
// endpoint protocol.
type Config struct {
	Socket        string
	Dialer        Dialer
	ServerName    string
	ServerVersion string
}

// Server translates MCP messages from Input and writes MCP messages to Output.
type Server struct {
	socket        string
	dialer        Dialer
	serverName    string
	serverVersion string
}

// New validates configuration and constructs an adapter.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Socket) == "" {
		return nil, fmt.Errorf("%w: socket is empty", ErrInvalidConfig)
	}
	if cfg.Dialer == nil {
		cfg.Dialer = &net.Dialer{}
	}
	if cfg.ServerName == "" {
		cfg.ServerName = "mcpstdio"
	}
	if cfg.ServerVersion == "" {
		cfg.ServerVersion = "1"
	}
	return &Server{
		socket:        cfg.Socket,
		dialer:        cfg.Dialer,
		serverName:    cfg.ServerName,
		serverVersion: cfg.ServerVersion,
	}, nil
}

// Serve runs an adapter until Input reaches EOF or ctx is canceled.
func Serve(ctx context.Context, input io.Reader, output io.Writer, socket string) error {
	server, err := New(Config{Socket: socket})
	if err != nil {
		return err
	}
	return server.Serve(ctx, input, output)
}

// Serve runs the configured adapter over the supplied streams.
func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return fmt.Errorf("%w: nil stream", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	lines := make(chan readResult, 1)
	go readMessages(input, lines)
	writer := &lineWriter{output: output}
	active := newCalls()
	// ONE ENDPOINT CONNECTION FOR THE LIFE OF THIS SESSION. A connection IS
	// the endpoint's admission interval (ADR-0058): toolendpoint's serve calls
	// Auth.Admit exactly once per connection, and the composition root hands
	// out one caller slot per session for that connection's whole lifetime
	// (internal/app's caller-slot bookkeeping), released only when its
	// serve loop ends. Dialling per call opened and closed an authority
	// interval around every tool call, so the second of two overlapping calls
	// was refused by the slot the first one still held — a refusal the wire
	// words as a problem with the caller, though the peer was never in
	// question. One shared connection is that interval expressed literally,
	// and the endpoint already serves concurrent requests on it.
	link := newEndpointLink(s.socket, s.dialer)
	var requests sync.WaitGroup
	defer func() {
		// Cancel first, then close, then wait. Cancelling is what every
		// in-flight call sees through its own context; closing is what
		// reaches a call already inside a write, which no context can
		// interrupt. The session is over either way — the input reached EOF
		// or the context was cancelled — so no answer is owed to anybody, and
		// waiting before closing could park the last caller far enough into a
		// full socket buffer to hold the session open.
		active.cancelAll()
		link.close()
		requests.Wait()
	}()
	state := &sessionState{}
	// THE ONLY ORDER THAT IS REAL. initialize and tools/list write session
	// state that a later tools/call reads — the negotiated version deciding
	// structuredContent, and the catalogue deciding membership — so a request
	// waits for a state-writing request that was in flight when it arrived,
	// and for nothing else. Two calls in flight together are two exchanges on
	// the endpoint's own concurrent path and MUST NOT queue behind each other:
	// the call a coordinator most needs beside a long wait is the one asking
	// what that wait is doing.
	gate := newSessionGate()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case read := <-lines:
			if read.err != nil {
				if errors.Is(read.err, io.EOF) {
					return nil
				}
				return read.err
			}
			request, err := decodeMessage(read.line)
			if err != nil {
				if writeErr := writer.errorResponse(nil, -32700, "parse error", "request is not valid JSON"); writeErr != nil {
					return writeErr
				}
				continue
			}
			if request.notification() {
				if request.Method == "notifications/cancelled" {
					active.cancel(request.cancelID())
				}
				continue
			}
			if err := validateRequest(request); err != nil {
				if writeErr := writer.errorResponse(request.ID, -32600, "invalid request", err.Error()); writeErr != nil {
					return writeErr
				}
				continue
			}

			callCtx, cancel := context.WithCancel(ctx)
			active.add(request.ID, cancel)
			requests.Add(1)
			wait, leave := gate.enter(writesSessionState(request.Method))
			go func(request requestMessage, callCtx context.Context, cancel context.CancelFunc, wait <-chan struct{}, leave func(bool)) {
				defer cancel()
				defer requests.Done()
				defer active.remove(request.ID)
				select {
				case <-wait:
				case <-callCtx.Done():
					leave(false)
					return
				}
				// A request cancelled before its turn comes never runs. The
				// select above cannot decide that on its own: when the
				// predecessor finished AND the cancellation landed, both cases
				// are ready and it picks at random — and the request that gets
				// through is a mutation nobody is waiting for any more.
				if callCtx.Err() != nil {
					leave(false)
					return
				}
				defer leave(true)
				if err := s.handle(callCtx, writer, state, link, request); err != nil && callCtx.Err() == nil {
					_ = writer.errorResponse(request.ID, -32603, "internal error", err.Error())
				}
			}(request, callCtx, cancel, wait, leave)
		}
	}
}

// writesSessionState reports whether a request writes session state a later
// request reads: initialize fixes the negotiated protocol version that decides
// whether a result carries structuredContent, and tools/list writes the
// catalogue that tools/call checks membership against.
func writesSessionState(method string) bool {
	return method == "initialize" || method == "tools/list"
}

// sessionGate is the order a session needs, and the whole of it: a request
// waits for a state-writing request that was in flight when it arrived, and a
// request that writes state is a barrier for the requests that arrive while it
// runs. Nothing else waits for anything — two calls in flight together are two
// exchanges on the endpoint's own concurrent path.
//
// It is a type rather than four lines inside the read loop because the rule
// that is easy to get wrong is invisible from the outside: a writer that never
// ran must not open its turn. That, and not the barrier itself, is what the
// tests here can prove deterministically.
type sessionGate struct {
	mu   sync.Mutex
	open chan struct{}
}

func newSessionGate() *sessionGate {
	gate := &sessionGate{open: make(chan struct{})}
	// Nothing is in flight, so nobody waits: a closed channel is the barrier
	// that lets the first request through.
	close(gate.open)
	return gate
}

// enter takes this request's turn. wait closes once the state this request must
// see has settled; leave reports whether the request RAN, and opens the turn
// this one installed for the requests that arrived while it did.
func (g *sessionGate) enter(writer bool) (<-chan struct{}, func(ran bool)) {
	g.mu.Lock()
	wait := g.open
	var done chan struct{}
	if writer {
		done = make(chan struct{})
		g.open = done
	}
	g.mu.Unlock()
	if done == nil {
		return wait, func(bool) {}
	}
	return wait, func(ran bool) {
		if !ran {
			// A WRITER THAT NEVER RAN WROTE NO STATE, so it must not open the
			// turn it installed: the requests behind it would read the session
			// as if this writer had finished, and a cancelled tools/list would
			// let the next one install its catalogue ahead of the list that is
			// still running. They follow the writer this one was queued behind
			// instead.
			<-wait
		}
		close(done)
	}
}

type sessionState struct {
	mu        sync.RWMutex
	version   string
	catalogue map[string]struct{}
	// generation counts catalogue installs. A load reads it before it asks the
	// endpoint and installs only if it has not moved: the catalogue is session
	// state, and two loads in flight are two answers, of which the one the
	// endpoint gave LAST is the one to keep.
	generation uint64
}

func (s *sessionState) setVersion(version string) {
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
}

func (s *sessionState) versionOrLatest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.version == "" {
		return latestVersion
	}
	return s.version
}

// catalogueToken marks the point a load starts from.
func (s *sessionState) catalogueToken() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// installCatalogue writes names only when no other load has installed a
// catalogue since this one started, and reports whether it wrote. The install
// is conditional because a load can be overtaken: a call's own refresh can be
// held at the endpoint while a tools/list redials, loads and installs, and
// letting the older answer land afterwards would leave every later membership
// check reading a catalogue the endpoint has already replaced. The map lock
// cannot see that — it is a lost update, not a data race.
func (s *sessionState) installCatalogue(token uint64, names map[string]struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != token {
		return false
	}
	s.catalogue = names
	s.generation++
	return true
}

func (s *sessionState) hasTool(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.catalogue[name]
	return ok
}

type readResult struct {
	line []byte
	err  error
}

func readMessages(input io.Reader, lines chan<- readResult) {
	reader := bufio.NewReaderSize(input, maxMessageBytes)
	for {
		line, err := readLine(reader)
		if err != nil {
			lines <- readResult{err: err}
			return
		}
		lines <- readResult{line: line}
	}
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxMessageBytes {
			return nil, errors.New("mcpstdio: message exceeds size bound")
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
			return nil, errors.New("mcpstdio: message did not end with a newline")
		}
		return nil, err
	}
}

type requestMessage struct {
	JSONRPC json.RawMessage
	ID      json.RawMessage
	Method  string
	Params  json.RawMessage
}

func decodeMessage(line []byte) (requestMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		return requestMessage{}, err
	}
	if object == nil {
		return requestMessage{}, errors.New("request must be an object")
	}
	var request requestMessage
	request.JSONRPC = object["jsonrpc"]
	request.ID = object["id"]
	if raw := object["method"]; raw != nil {
		if err := json.Unmarshal(raw, &request.Method); err != nil {
			return requestMessage{}, errors.New("method must be a string")
		}
	}
	request.Params = object["params"]
	return request, nil
}

func (r requestMessage) notification() bool {
	return len(r.ID) == 0 || bytes.Equal(bytes.TrimSpace(r.ID), []byte("null"))
}

func (r requestMessage) cancelID() string {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(r.Params, &params) != nil {
		return ""
	}
	return string(bytes.TrimSpace(params.RequestID))
}

func validateRequest(request requestMessage) error {
	if string(request.JSONRPC) != `"2.0"` || request.Method == "" {
		return errors.New("request is not a valid JSON-RPC request")
	}
	var id any
	decoder := json.NewDecoder(bytes.NewReader(request.ID))
	decoder.UseNumber()
	if err := decoder.Decode(&id); err != nil {
		return errors.New("request id is invalid")
	}
	switch id.(type) {
	case string, json.Number:
		return nil
	default:
		return errors.New("request id must be a string or number")
	}
}

func (s *Server) handle(ctx context.Context, writer *lineWriter, state *sessionState, link *endpointLink, request requestMessage) error {
	// WHERE THE EXCHANGE IS BORN. This process is the first thing in nocx an
	// agent's tool call reaches, so the trace starts here and travels to the
	// backend as a traceparent on the endpoint request (nocx-4l2a5.3). Without
	// it the coordinator's call, this hop and the backend's dispatch are three
	// sets of log lines with nothing in common.
	//
	// Every method, not only tools/call: initialize and tools/list fail too,
	// and a failure with no trace is the one this epic exists to remove.
	ctx, _ = nocxlog.StartSpan(ctx)
	switch request.Method {
	case "initialize":
		version := negotiateVersion(initializeVersion(request.Params))
		state.setVersion(version)
		return writer.result(request.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.serverName, "version": s.serverVersion},
		})
	case "tools/list":
		return s.handleList(ctx, writer, state, link, request.ID)
	case "tools/call":
		return s.handleCall(ctx, writer, state, link, request.ID, request.Params)
	default:
		return writer.errorResponse(request.ID, -32601, "method not found", "MCP method is not supported")
	}
}

func initializeVersion(params json.RawMessage) string {
	var values struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &values) != nil {
		return ""
	}
	return values.ProtocolVersion
}

func negotiateVersion(requested string) string {
	for _, version := range supportedVersions {
		if requested == version {
			return version
		}
	}
	return latestVersion
}

func supportsStructuredContent(version string) bool {
	return version == "2025-06-18" || version == latestVersion
}

type catalogueEntry struct {
	Name    string
	Summary string
	Params  json.RawMessage
	Result  json.RawMessage
}

type catalogueData struct {
	entries []catalogueEntry
	names   map[string]struct{}
}

func (s *Server) loadCatalogue(ctx context.Context, link *endpointLink) (catalogueData, *upstreamError, error) {
	result, upstream, err := link.call(ctx, "tools.catalogue", json.RawMessage(`{}`))
	if err != nil || upstream != nil {
		return catalogueData{}, upstream, err
	}
	var wire struct {
		Tools []catalogueEntry `json:"tools"`
	}
	if err := json.Unmarshal(result, &wire); err != nil || wire.Tools == nil {
		return catalogueData{}, nil, errors.New("tool endpoint returned an invalid catalogue")
	}
	data := catalogueData{
		entries: make([]catalogueEntry, 0, len(wire.Tools)),
		names:   make(map[string]struct{}, len(wire.Tools)),
	}
	for _, item := range wire.Tools {
		if item.Name == "" || !json.Valid(item.Params) || !json.Valid(item.Result) {
			return catalogueData{}, nil, errors.New("tool endpoint returned an invalid catalogue")
		}
		data.entries = append(data.entries, item)
		data.names[item.Name] = struct{}{}
	}
	return data, nil, nil
}

func (s *Server) handleList(ctx context.Context, writer *lineWriter, state *sessionState, link *endpointLink, id json.RawMessage) error {
	token := state.catalogueToken()
	data, upstream, err := s.loadCatalogue(ctx, link)
	if err != nil {
		// A request the caller cancelled gets no answer, whichever way the
		// load failed — MCP carries no response for a cancelled request.
		if ctx.Err() != nil {
			return nil
		}
		return writer.errorResponse(id, -32000, "tool endpoint unavailable", err.Error())
	}
	if upstream != nil {
		if ctx.Err() != nil {
			return nil
		}
		return writer.errorResponse(id, upstream.Code, upstream.Message, upstream.reason())
	}
	// The catalogue is on its way when the cancellation lands, or already here:
	// either way MCP carries no response for a cancelled request, and state
	// loaded for a call nobody is waiting for is state the session did not ask
	// for.
	if ctx.Err() != nil {
		return nil
	}
	state.installCatalogue(token, data.names)
	tools := make([]map[string]any, 0, len(data.entries))
	for _, item := range data.entries {
		tools = append(tools, map[string]any{
			"name":         item.Name,
			"description":  item.Summary,
			"inputSchema":  item.Params,
			"outputSchema": item.Result,
		})
	}
	return writer.result(id, map[string]any{"tools": tools})
}

func (s *Server) handleCall(ctx context.Context, writer *lineWriter, state *sessionState, link *endpointLink, id json.RawMessage, params json.RawMessage) error {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
		return writer.errorResponse(id, -32602, "invalid params", "tools/call requires a name")
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
		return writer.errorResponse(id, -32602, "invalid params", "tools/call arguments must be an object")
	}
	if !state.hasTool(call.Name) {
		token := state.catalogueToken()
		data, upstream, err := s.loadCatalogue(ctx, link)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return writer.toolError(id, fmt.Sprintf("tool %q is unavailable: %v", call.Name, err))
		}
		if upstream != nil {
			return writer.toolError(id, fmt.Sprintf("tool %q cannot be checked: %s", call.Name, upstream.reason()))
		}
		state.installCatalogue(token, data.names)
		if !state.hasTool(call.Name) {
			return writer.toolError(id, fmt.Sprintf("tool %q is not offered by the endpoint", call.Name))
		}
	}
	result, upstream, err := link.call(ctx, call.Name, call.Arguments)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return writer.toolError(id, err.Error())
	}
	if upstream != nil {
		if upstream.refusal() {
			return writer.toolError(id, upstream.reason())
		}
		return writer.errorResponse(id, upstream.Code, upstream.Message, upstream.reason())
	}
	// Same window, one step later: the endpoint answered and the caller called
	// the call off while that answer was in flight.
	if ctx.Err() != nil {
		return nil
	}
	if !json.Valid(result) {
		return writer.errorResponse(id, -32603, "internal error", "tool endpoint returned invalid result")
	}
	return writer.toolResult(id, result, supportsStructuredContent(state.versionOrLatest()))
}

type upstreamError struct {
	Code    int                        `json:"code"`
	Message string                     `json:"message"`
	Data    map[string]json.RawMessage `json:"data"`
}

func (e *upstreamError) reason() string {
	if raw := e.Data["reason"]; raw != nil {
		var reason string
		if json.Unmarshal(raw, &reason) == nil && reason != "" {
			return reason
		}
	}
	if e.Message != "" {
		return e.Message
	}
	return "tool endpoint refused the request"
}

func (e *upstreamError) refusal() bool {
	return e.Code == -32000 || e.Code == -32001
}

// endpointLink is the adapter's ONE connection to the local tool endpoint,
// multiplexed for the life of a stdio session.
//
// A connection here is not merely a socket. toolendpoint.Endpoint.serve calls
// Auth.Admit exactly ONCE per connection, and the composition root's
// active-caller bookkeeping hands out one caller slot per session for that
// connection's whole lifetime, released when its serve loop ends. The
// connection IS the admission interval ADR-0058 makes the unit of authority,
// and dialling per call opened and closed that interval around every tool
// call — which is what turned a coordinator's second call into that refusal,
// naming a peer identity that was never in question.
//
// THE FIX IS NOT A MUTEX. The endpoint serves concurrent requests on one
// connection (its own inFlight counter, WaitGroup and dropped read deadline),
// so serialising the requests here would reproduce the defect one layer up:
// the long-poll would hold the lock, and the call a coordinator needs to run
// beside a wait would be exactly the call that waits.
type endpointLink struct {
	socket string
	dialer Dialer

	mu      sync.Mutex
	conn    *endpointConn
	nextID  int64
	stopped bool
}

func newEndpointLink(socket string, dialer Dialer) *endpointLink {
	return &endpointLink{socket: socket, dialer: dialer}
}

// close releases the shared connection when the stdio session ends. The
// endpoint sees the close and releases the session's caller slot with it.
func (l *endpointLink) close() {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.stopped = true
	l.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

// attach returns the live connection and the id this request will carry,
// dialling when there is none. An idle connection the endpoint has already
// closed is replaced rather than reported: the endpoint holds a read deadline
// of requestReadWindow while nothing is in flight, so an idle coordinator
// connection is expected to be gone by the next call, and a coordinator whose
// call died with it would rather have the call than the error.
func (l *endpointLink) attach(ctx context.Context) (*endpointConn, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return nil, 0, errors.New("endpoint link is closed")
	}
	if l.conn == nil || l.conn.gone() {
		if l.conn != nil {
			l.conn.close()
			l.conn = nil
		}
		raw, err := l.dialer.DialContext(ctx, "unix", l.socket)
		if err != nil {
			return nil, 0, err
		}
		l.conn = newEndpointConn(raw)
	}
	l.nextID++
	return l.conn, l.nextID, nil
}

// call sends one request over the shared connection and waits for the answer
// carrying ITS id. Waiting is per request: a cancelled caller stops waiting
// for its own answer and leaves the connection, and every other call on it,
// exactly as they were.
func (l *endpointLink) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *upstreamError, error) {
	for round := 0; ; round++ {
		// A REQUEST THE CALLER ALREADY CANCELLED IS NOT SENT. On the parent a
		// fresh DialContext(ctx) refused this before anything left the
		// process; a shared connection must refuse it here, or a cancelled
		// mutation reaches the endpoint and runs.
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		conn, id, err := l.attach(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, err)
		}
		reply := conn.expect(id)
		arrived, writeErr := conn.send(ctx, id, method, params)
		if writeErr != nil {
			conn.forget(id)
			if err := ctx.Err(); err != nil {
				// The write failed because this caller was cancelled — the
				// deadline that interrupts a stuck write is set from its
				// context — so the cancellation is the reason to report, not
				// the socket error it produced.
				return nil, nil, err
			}
			// A REFUSAL ARRIVES IN WHICHEVER ORDER THE RACE FALLS. The endpoint
			// refuses an admission and closes, and the request this caller sent
			// may have gone out before that close or after it — so the write
			// can be the thing that fails. The reason is still in the reader's
			// hands, because it is what ended the connection, and waiting for
			// the connection to settle is what makes both orders report the
			// same answer rather than one of them reporting an endpoint that
			// merely vanished. A write that delivered NOTHING is what makes the
			// wait safe: the socket has already errored, so the reader is at
			// its end. (The caller's own context is the way out of the one
			// shape that would not settle — a peer that half-closes in
			// silence — and this endpoint never does that.)
			if !arrived {
				if !conn.gone() {
					select {
					case <-conn.closed:
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					}
				}
				if upstream, refused := conn.refusal(); refused {
					return nil, upstream, nil
				}
				if round == 0 {
					// A retry is safe exactly when NO WHOLE FRAME left this
					// process: the endpoint reads newline-delimited JSON, so a
					// frame it never received whole is a request it cannot have
					// run — including a truncated one, which it buffers and never
					// parses. A write that delivered the whole frame and still
					// failed is the ambiguous case, and it is NOT retried: the
					// endpoint may hold the request, and running it twice is how a
					// mutation becomes two of itself.
					conn.close()
					continue
				}
			}
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, writeErr)
		}
		select {
		case answer := <-reply:
			conn.forget(id)
			// THE CANCELLATION DECIDES, not the select. When the answer and
			// the cancellation are both ready — which is what a cancellation
			// arriving mid-flight looks like — a select picks at random, and
			// the half it can pick here is a response for a request the caller
			// called off. MCP carries no response for a cancelled request, so
			// the answer is dropped rather than left to chance.
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			return answer.result, answer.upstream, answer.err
		case <-conn.closed:
			// The answer can arrive in the same instant the connection ends —
			// an endpoint that answers and closes, which is how a session
			// times out. A delivery already made is still the answer, so it
			// wins over the close that followed it.
			select {
			case answer := <-reply:
				conn.forget(id)
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				return answer.result, answer.upstream, answer.err
			default:
			}
			conn.forget(id)
			// A REFUSAL THE ENDPOINT WROTE FOR THE CONNECTION travels as the
			// refusal it is, so the sentence it carries reaches whoever asked.
			// Reported as a lost endpoint instead, "you are not enrolled" and
			// "another caller holds the slot" arrive as "the endpoint is
			// unavailable", which is the one answer that tells the caller
			// nothing about what to do next.
			if upstream, refused := conn.refusal(); refused {
				return nil, upstream, nil
			}
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, conn.failure())
		case <-ctx.Done():
			// The endpoint keeps working on the call; this caller stops
			// waiting for its answer. Nothing here touches the connection,
			// because closing it is what used to cancel the siblings: on a
			// shared connection one caller's cancellation is not another
			// caller's failure (nocx-tlaft). The answer, if it arrives, is
			// dropped — and the call goes on executing at the endpoint, which
			// is nocx-98qho and deliberately not fixed here: the endpoint's
			// request context belongs to the CONNECTION, so cancelling one
			// call would take its siblings with it.
			conn.forget(id)
			return nil, nil, ctx.Err()
		}
	}
}

// endpointConn is one connection to the endpoint with its responses
// demultiplexed by request id. The endpoint answers what it finishes first,
// not what was sent first, so the id is the only thing that says whose answer
// is whose — which is why the constant 1 had to go.
type endpointConn struct {
	conn net.Conn

	mu      sync.Mutex
	pending map[int64]chan endpointAnswer
	err     error
	once    sync.Once
	closed  chan struct{}
}

type endpointAnswer struct {
	result   json.RawMessage
	upstream *upstreamError
	err      error
}

func newEndpointConn(conn net.Conn) *endpointConn {
	connection := &endpointConn{
		conn:    conn,
		pending: make(map[int64]chan endpointAnswer),
		closed:  make(chan struct{}),
	}
	go connection.read()
	return connection
}

func (c *endpointConn) expect(id int64) chan endpointAnswer {
	reply := make(chan endpointAnswer, 1)
	c.mu.Lock()
	c.pending[id] = reply
	c.mu.Unlock()
	return reply
}

func (c *endpointConn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *endpointConn) gone() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// refusal reports the connection-level refusal the endpoint wrote, if that is
// what ended this connection. It is asked on both paths — the answer that never
// arrived, and the write that failed — so that whichever of them a caller is
// standing on, a refused admission reaches it as the refusal it is.
func (c *endpointConn) refusal() (*upstreamError, bool) {
	var refusal *endpointRefusal
	if errors.As(c.failure(), &refusal) {
		return refusal.upstream, true
	}
	return nil, false
}

func (c *endpointConn) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return errors.New("endpoint closed the connection")
	}
	return c.err
}

// close ends the connection. Once, whatever else reports it end.
func (c *endpointConn) close() {
	c.fail(net.ErrClosed)
}

// fail records why the connection ended — the first reason wins — releases
// every caller waiting on it, and closes the socket. A connection that ended
// with a request in flight cannot answer that request, and the caller is told
// so rather than retried: a call the endpoint may already have dispatched must
// not run a second time.
//
// THE SOCKET GOES WITH THE CONNECTION. A reader that stopped — malformed JSON,
// an oversized response — used to leave the descriptor open: the endpoint's
// admission interval, and with it the session's caller slot, survived until
// some later call happened to redial, and an endpoint mid-write found a
// connection nobody was reading. There is one owner of that close and this is
// it, so every path that ends a connection ends it the same way.
func (c *endpointConn) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.pending = make(map[int64]chan endpointAnswer)
		c.mu.Unlock()
		_ = c.conn.Close()
		close(c.closed)
	})
}

// endpointRefusal is a failure the endpoint attached to the CONNECTION rather
// than to a request. An admission failure is written with a null id, because
// at that point the endpoint has read no request to attach it to: there is no
// waiter to hand it to, and dropping it turned "not enrolled", "not approved"
// and "another caller holds the slot" into an endpoint that looked merely
// unavailable — the one answer that names no action.
type endpointRefusal struct{ upstream *upstreamError }

func (e *endpointRefusal) Error() string { return e.upstream.reason() }

// read demultiplexes every response the connection delivers to the caller
// waiting for that id, and ends the connection when the endpoint stops
// answering on it.
func (c *endpointConn) read() {
	reader := bufio.NewReaderSize(c.conn, maxMessageBytes)
	for {
		line, err := readLine(reader)
		if err != nil {
			c.fail(err)
			return
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *upstreamError  `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			// The framing is line-delimited JSON, so a line that is not a
			// response means this connection can no longer be trusted to carry
			// answers. It ends here, and every waiter is told why.
			c.fail(fmt.Errorf("endpoint returned invalid JSON: %w", err))
			return
		}
		answer := endpointAnswer{result: response.Result}
		switch {
		case response.Error != nil:
			answer = endpointAnswer{upstream: response.Error}
		case len(response.Result) == 0 || !json.Valid(response.Result):
			answer = endpointAnswer{err: errors.New("endpoint returned no result")}
		}
		// DELIVERED UNDER THE LOCK, which is what makes the answer atomic with
		// the removal: a connection that ends between the unlock and the send
		// would close `closed` first, and the waiting caller — seeing the
		// close, finding an empty channel — would report a lost endpoint for a
		// request the endpoint answered. The channel is buffered and written
		// once, so the send never blocks.
		c.mu.Lock()
		delivered := false
		if id, ok := responseID(response.ID); ok {
			if reply := c.pending[id]; reply != nil {
				delete(c.pending, id)
				reply <- answer
				delivered = true
			}
		}
		c.mu.Unlock()
		if delivered {
			continue
		}
		if response.Error != nil && idless(response.ID) {
			// No id at all, so this is not an answer to anything this adapter
			// sent: it is the endpoint refusing the CONNECTION — an admission
			// failure, an envelope it could not read. The connection is what
			// it refused, so the reason ends it and every caller reads that
			// reason rather than "the endpoint is unavailable".
			c.fail(&endpointRefusal{upstream: response.Error})
			return
		}
		// An answer to a call this adapter cancelled — the id is one nobody is
		// waiting for — dropped, so a late answer can never resurrect it.
	}
}

// responseID reads the id a response was written for. It reports false for
// anything that is not a number, which nothing in this protocol writes.
func responseID(raw json.RawMessage) (int64, bool) {
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, false
	}
	return id, true
}

// idless reports whether a response carries no id at all. json.Unmarshal reads
// `null` into an int64 without complaining — it leaves it zero — so the two
// have to be told apart deliberately, and the difference is load-bearing: an
// id-less error is a refusal of the connection, while an error carrying an id
// nobody is waiting for is a late answer to a cancelled call.
func idless(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// send writes one request and reports whether the WHOLE frame left this
// process. The endpoint reads newline-delimited JSON, so one Write of a whole
// frame is what keeps two concurrent callers from interleaving halves of two
// requests on the one connection — and the answer is what decides whether a
// failed write may be retried: a frame that never arrived whole cannot have
// been parsed, while one that did may already have run.
//
// THE WRITE IS INTERRUPTIBLE BY THE CALLER'S CONTEXT. A write to a peer that
// stopped reading blocks until the socket buffer drains, and on a shared
// connection it blocks that caller AND every other writer behind the same
// descriptor — which is why the interrupted-caller case cannot be answered by
// the select that follows the write. A context cannot reach a write already in
// progress; a deadline can.
func (c *endpointConn) send(ctx context.Context, id int64, method string, params json.RawMessage) (bool, error) {
	envelope := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	// The far side opens a CHILD of this frame, which is what makes the hop
	// visible in the tree rather than collapsing two processes into one span.
	// A context carrying no span sends no header, and the endpoint serves the
	// call anyway under a trace of its own.
	if header := nocxlog.SpanFrom(ctx).Traceparent(); header != "" {
		envelope["traceparent"] = header
	}
	request, err := json.Marshal(envelope)
	if err != nil {
		return false, err
	}
	frame := append(request, '\n')

	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.conn.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	written, writeErr := c.conn.Write(frame)
	if !stop() {
		// The interrupt has fired or is in progress; waiting for it is what
		// keeps the clear below from racing the set that failed the write —
		// and a deadline left in the past would fail the next writer on the
		// shared connection instead of the caller it was meant for.
		<-interrupted
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	if written < len(frame) && writeErr == nil {
		// io.Writer owes an error for a short write. Reported as success it
		// would leave this caller waiting for the answer to a request that was
		// cut off, on a connection nothing else is wrong with — so the
		// contract violation is named here rather than waited out.
		writeErr = io.ErrShortWrite
	}
	return written == len(frame), writeErr
}

type lineWriter struct {
	mu     sync.Mutex
	output io.Writer
}

func (w *lineWriter) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.output.Write(append(data, '\n'))
	return err
}

func (w *lineWriter) result(id json.RawMessage, result any) error {
	return w.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (w *lineWriter) errorResponse(id json.RawMessage, code int, message, reason string) error {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		id = json.RawMessage("null")
	}
	errValue := map[string]any{"code": code, "message": message}
	if reason != "" {
		errValue["data"] = map[string]string{"reason": reason}
	}
	return w.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": errValue})
}

func (w *lineWriter) toolError(id json.RawMessage, reason string) error {
	return w.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": reason}},
			"isError": true,
		},
	})
}

func (w *lineWriter) toolResult(id json.RawMessage, result json.RawMessage, structured bool) error {
	value := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(result)}},
			"isError": false,
		},
	}
	if structured {
		if payload, ok := value["result"].(map[string]any); ok {
			payload["structuredContent"] = result
		}
	}
	return w.write(value)
}

type activeCalls struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newCalls() *activeCalls {
	return &activeCalls{cancels: make(map[string]context.CancelFunc)}
}

func (c *activeCalls) add(id json.RawMessage, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels[string(bytes.TrimSpace(id))] = cancel
}

func (c *activeCalls) remove(id json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cancels, string(bytes.TrimSpace(id)))
}

func (c *activeCalls) cancel(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	cancel := c.cancels[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *activeCalls) cancelAll() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.cancels))
	for _, cancel := range c.cancels {
		cancels = append(cancels, cancel)
	}
	c.cancels = make(map[string]context.CancelFunc)
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
