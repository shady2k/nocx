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
	gate := make(chan struct{})
	close(gate)
	var gateMu sync.Mutex

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
			gateMu.Lock()
			wait := gate
			var done chan struct{}
			if writesSessionState(request.Method) {
				// A state-writer is a barrier for the requests that arrive while
				// it runs, including a later state-writer: their turns follow
				// the state, not each other.
				done = make(chan struct{})
				gate = done
			}
			gateMu.Unlock()
			go func(request requestMessage, callCtx context.Context, cancel context.CancelFunc, wait <-chan struct{}, done chan struct{}) {
				defer cancel()
				defer requests.Done()
				defer active.remove(request.ID)
				if done != nil {
					defer close(done)
				}
				select {
				case <-wait:
				case <-callCtx.Done():
					return
				}
				if err := s.handle(callCtx, writer, state, link, request); err != nil && callCtx.Err() == nil {
					_ = writer.errorResponse(request.ID, -32603, "internal error", err.Error())
				}
			}(request, callCtx, cancel, wait, done)
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

type sessionState struct {
	mu        sync.RWMutex
	version   string
	catalogue map[string]struct{}
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

func (s *sessionState) setCatalogue(names map[string]struct{}) {
	s.mu.Lock()
	s.catalogue = names
	s.mu.Unlock()
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
	data, upstream, err := s.loadCatalogue(ctx, link)
	if err != nil {
		return writer.errorResponse(id, -32000, "tool endpoint unavailable", err.Error())
	}
	if upstream != nil {
		return writer.errorResponse(id, upstream.Code, upstream.Message, upstream.reason())
	}
	state.setCatalogue(data.names)
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
		state.setCatalogue(data.names)
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
		conn, id, err := l.attach(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, err)
		}
		reply := conn.expect(id)
		writeErr := conn.send(ctx, id, method, params)
		if writeErr != nil {
			conn.forget(id)
			if ctx.Err() == nil && round == 0 {
				// The write failed, so the endpoint never received a whole
				// request and the retry cannot run anything twice. Whatever
				// ended the connection — an endpoint that timed the session
				// out, a socket that moved — the next attempt dials again.
				conn.close()
				continue
			}
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, writeErr)
		}
		select {
		case answer := <-reply:
			conn.forget(id)
			return answer.result, answer.upstream, answer.err
		case <-conn.closed:
			// The answer can arrive in the same instant the connection ends —
			// an endpoint that answers and closes, which is how a session
			// times out. A delivery already made is still the answer, so it
			// wins over the close that followed it.
			select {
			case answer := <-reply:
				conn.forget(id)
				return answer.result, answer.upstream, answer.err
			default:
			}
			conn.forget(id)
			return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, conn.failure())
		case <-ctx.Done():
			// The endpoint keeps working on the call; this caller stops
			// waiting for its answer. Nothing here touches the connection,
			// because closing it is what used to cancel the siblings: on a
			// shared connection one caller's cancellation is not another
			// caller's failure (nocx-tlaft). The answer, if it arrives, is
			// dropped — MCP's cancellation carries no response for the
			// cancelled request.
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
	_ = c.conn.Close()
}

// fail records why the connection ended — the first reason wins — and releases
// every caller waiting on it. A connection that ended with a request in flight
// cannot answer that request, and the caller is told so rather than retried: a
// call the endpoint may already have dispatched must not run a second time.
func (c *endpointConn) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.pending = make(map[int64]chan endpointAnswer)
		c.mu.Unlock()
		close(c.closed)
	})
}

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
		var id int64
		if err := json.Unmarshal(response.ID, &id); err != nil {
			continue
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
		if reply := c.pending[id]; reply != nil {
			delete(c.pending, id)
			reply <- answer
		}
		// An answer nobody is waiting for — a call this adapter cancelled, or
		// an error the endpoint wrote against an id it could not read — is
		// dropped here, so a late answer can never resurrect a cancelled call.
		c.mu.Unlock()
	}
}

// send writes one request. The endpoint reads newline-delimited JSON, so one
// Write of a whole frame is what keeps two concurrent callers from
// interleaving halves of two requests on the one connection.
func (c *endpointConn) send(ctx context.Context, id int64, method string, params json.RawMessage) error {
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
		return err
	}
	_, err = c.conn.Write(append(request, '\n'))
	return err
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
