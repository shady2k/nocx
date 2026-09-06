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
	var requests sync.WaitGroup
	defer func() {
		active.cancelAll()
		requests.Wait()
	}()
	state := &sessionState{}
	lastDone := make(chan struct{})
	close(lastDone)

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
			previous := lastDone
			lastDone = make(chan struct{})
			done := lastDone
			requests.Add(1)
			go func(request requestMessage, callCtx context.Context, cancel context.CancelFunc, previous <-chan struct{}, done chan struct{}) {
				defer cancel()
				defer requests.Done()
				defer active.remove(request.ID)
				defer close(done)
				select {
				case <-previous:
				case <-callCtx.Done():
					return
				}
				if err := s.handle(callCtx, writer, state, request); err != nil && callCtx.Err() == nil {
					_ = writer.errorResponse(request.ID, -32603, "internal error", err.Error())
				}
			}(request, callCtx, cancel, previous, done)
		}
	}
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

func (s *Server) handle(ctx context.Context, writer *lineWriter, state *sessionState, request requestMessage) error {
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
		return s.handleList(ctx, writer, state, request.ID)
	case "tools/call":
		return s.handleCall(ctx, writer, state, request.ID, request.Params)
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

func (s *Server) loadCatalogue(ctx context.Context) (catalogueData, *upstreamError, error) {
	result, upstream, err := s.endpointCall(ctx, "tools.catalogue", json.RawMessage(`{}`))
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

func (s *Server) handleList(ctx context.Context, writer *lineWriter, state *sessionState, id json.RawMessage) error {
	data, upstream, err := s.loadCatalogue(ctx)
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
			"inputSchema":  json.RawMessage(item.Params),
			"outputSchema": json.RawMessage(item.Result),
		})
	}
	return writer.result(id, map[string]any{"tools": tools})
}

func (s *Server) handleCall(ctx context.Context, writer *lineWriter, state *sessionState, id json.RawMessage, params json.RawMessage) error {
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
		data, upstream, err := s.loadCatalogue(ctx)
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
	result, upstream, err := s.endpointCall(ctx, call.Name, call.Arguments)
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

func (s *Server) endpointCall(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *upstreamError, error) {
	conn, err := s.dialer.DialContext(ctx, "unix", s.socket)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()

	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  json.RawMessage(params),
	})
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, err)
	}
	line, err := readLine(bufio.NewReaderSize(conn, maxMessageBytes))
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrEndpointUnavailable, err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *upstreamError  `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, nil, fmt.Errorf("endpoint returned invalid JSON: %w", err)
	}
	if response.Error != nil {
		return nil, response.Error, nil
	}
	if len(response.Result) == 0 || !json.Valid(response.Result) {
		return nil, nil, errors.New("endpoint returned no result")
	}
	return response.Result, nil, nil
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
	return w.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (w *lineWriter) errorResponse(id json.RawMessage, code int, message, reason string) error {
	if len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		id = json.RawMessage("null")
	}
	errValue := map[string]any{"code": code, "message": message}
	if reason != "" {
		errValue["data"] = map[string]string{"reason": reason}
	}
	return w.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": errValue})
}

func (w *lineWriter) toolError(id json.RawMessage, reason string) error {
	return w.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": reason}},
			"isError": true,
		},
	})
}

func (w *lineWriter) toolResult(id json.RawMessage, result json.RawMessage, structured bool) error {
	value := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(result)}},
			"isError": false,
		},
	}
	if structured {
		value["result"].(map[string]any)["structuredContent"] = json.RawMessage(result)
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
