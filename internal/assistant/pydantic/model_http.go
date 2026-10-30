package pydantic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/jsoncheck"
)

type httpStream struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	body        io.ReadCloser
	owner       string
	outstanding int
	sent        int64
	closed      bool
	ended       bool
	credit      chan struct{}
}

func (o *operation) openHTTP(e envelope) error {
	var request modelOpen
	if err := decodeStrict(e.Payload, &request); err != nil {
		return err
	}
	if request.Method != http.MethodPost || request.URL != "https://nocx.invalid/v1/chat/completions" || len(request.Body) > maxObject {
		return errProtocol
	}
	o.mu.Lock()
	binding, ok := o.bindings[request.OwnerRequestID]
	o.mu.Unlock()
	if !ok {
		return errProtocol
	}
	if err := validateModelBody(request.Body, binding); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(assistant.WithModelHeaders(binding.ctx, binding.target.Headers))
	stop := context.AfterFunc(o.ctx, cancel)
	stream := &httpStream{cancel: func() { stop(); cancel() }, owner: request.OwnerRequestID, credit: make(chan struct{}, 1)}
	o.mu.Lock()
	for _, existing := range o.streams {
		existing.mu.Lock()
		active := existing.owner == request.OwnerRequestID && !existing.ended && !existing.closed
		existing.mu.Unlock()
		if active {
			o.mu.Unlock()
			stream.cancel()
			return errProtocol
		}
	}
	o.streams[*e.RequestID] = stream
	o.mu.Unlock()
	go o.serveHTTP(ctx, *e.RequestID, request, binding.target, stream)
	return nil
}

func validateModelBody(body []byte, binding modelBinding) error {
	if !utf8.Valid(body) || jsoncheck.UniqueKeys(body) != nil {
		return errProtocol
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || request == nil {
		return errProtocol
	}
	// No provider-native tool, arbitrary route or request configuration is
	// delegated to Python. These are the native Chat adapter's wire settings.
	for key := range request {
		switch key {
		case "model", "messages", "stream", "stream_options", "tools", "tool_choice", "parallel_tool_calls":
		default:
			return errProtocol
		}
	}
	var model string
	var stream bool
	if json.Unmarshal(request["model"], &model) != nil || model != binding.target.Model || json.Unmarshal(request["stream"], &stream) != nil || !stream {
		return errProtocol
	}
	var messages []json.RawMessage
	if json.Unmarshal(request["messages"], &messages) != nil || len(messages) == 0 {
		return errProtocol
	}
	type function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict,omitempty"`
	}
	type declaration struct {
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	var offered []declaration
	if tools, ok := request["tools"]; ok {
		if decodeStrict(tools, &offered) != nil {
			return errProtocol
		}
	}
	visible := make(map[string]toolDefinition, len(binding.catalog.VisibleTools))
	for _, name := range binding.catalog.VisibleTools {
		for _, definition := range binding.catalog.Tools {
			if definition.Name == name {
				visible[name] = definition
				break
			}
		}
	}
	if len(offered) != len(visible) {
		return errProtocol
	}
	for _, tool := range offered {
		expected, ok := visible[tool.Function.Name]
		if !ok || tool.Type != "function" || tool.Function.Description != expected.Description || tool.Function.Strict != nil && *tool.Function.Strict {
			return errProtocol
		}
		var want, got any
		if json.Unmarshal(tool.Function.Parameters, &got) != nil || json.Unmarshal(expected.Parameters, &want) != nil || !reflect.DeepEqual(want, got) {
			return errProtocol
		}
		delete(visible, tool.Function.Name)
	}
	if choice, ok := request["tool_choice"]; ok {
		var value string
		if json.Unmarshal(choice, &value) != nil || value != "auto" && value != "none" {
			return errProtocol
		}
		if len(offered) == 0 && value != "none" {
			return errProtocol
		}
	}
	if parallel, ok := request["parallel_tool_calls"]; ok {
		var value bool
		if json.Unmarshal(parallel, &value) != nil || value {
			return errProtocol
		}
	}
	return nil
}

func completionURL(base string) string {
	// Match the existing OpenAI adapter's suffix semantics exactly.
	// URL and redirect policy remain with the shared guarded HTTP owner.
	return strings.TrimRight(base, "/") + "/chat/completions"
}

func (o *operation) serveHTTP(ctx context.Context, id string, open modelOpen, target assistant.ModelTarget, s *httpStream) {
	defer s.cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(target.BaseURL), bytes.NewReader(open.Body))
	if err != nil {
		o.sendFailure("error", &id, errorPayload{Code: "provider"})
		return
	}
	for _, header := range open.Headers {
		switch http.CanonicalHeaderKey(header.Name) {
		case "Content-Type", "Accept":
			req.Header.Set(header.Name, header.Value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if err = target.Key.Use(func(secret []byte) error {
		if len(secret) > 0 {
			req.Header.Set("Authorization", "Bearer "+string(secret))
		}
		return nil
	}); err != nil {
		o.sendFailure("error", &id, errorPayload{Code: "provider"})
		return
	}
	// Configured custom headers override defaults, exactly as the existing
	// adapter does. The guard's context identifies them for origin stripping.
	for _, header := range target.Headers {
		req.Header.Set(header.Name, header.Value)
	}
	response, err := o.client.http.Do(req)
	if err != nil {
		o.sendFailure("error", &id, errorPayload{Code: "provider"})
		return
	}
	// Stream termination is reported separately; closing an exhausted or
	// cancelled response must not send a second terminal outcome.
	defer func() { _ = response.Body.Close() }()
	s.mu.Lock()
	s.body = response.Body
	closed := s.closed
	s.mu.Unlock()
	if response.StatusCode >= 400 {
		o.recordHTTPFailure(open.OwnerRequestID, response.StatusCode, nil)
	}
	var headers []wireHeader
	for _, name := range []string{"Content-Type", "Content-Length", "Retry-After"} {
		if value := response.Header.Get(name); value != "" {
			headers = append(headers, wireHeader{name, value})
		}
	}
	if headers == nil {
		headers = []wireHeader{}
	}
	o.sendFailure("model.headers", &id, modelHeaders{response.StatusCode, headers})
	if closed {
		o.endHTTP(id, s, nil)
		return
	}
	buffer := make([]byte, chunkBytes)
	var total int64
	var errorBody []byte
	for {
		capacity, err := s.capacity(ctx)
		if err != nil {
			code := "transport"
			o.endHTTP(id, s, &code)
			return
		}
		n, readErr := response.Body.Read(buffer[:capacity])
		if n > 0 {
			if response.StatusCode >= 400 && total+int64(n) > streamWindow {
				code := "body_limit"
				o.endHTTP(id, s, &code)
				return
			}
			if response.StatusCode >= 400 {
				errorBody = append(errorBody, buffer[:n]...)
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				o.endHTTP(id, s, nil)
				return
			}
			offset := s.sent
			s.sent += int64(n)
			s.outstanding += n
			s.mu.Unlock()
			o.sendFailure("model.chunk", nil, modelChunk{id, buffer[:n], offset})
			total += int64(n)
		}
		if readErr != nil {
			if response.StatusCode >= 400 {
				o.recordHTTPFailure(open.OwnerRequestID, response.StatusCode, errorBody)
			}
			var code *string
			if readErr != io.EOF {
				text := "transport"
				code = &text
			}
			o.endHTTP(id, s, code)
			return
		}
	}
}

func (o *operation) recordHTTPFailure(owner string, status int, body []byte) {
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	code := ""
	if parsed.Error.Code == "model_not_found" {
		code = parsed.Error.Code
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.httpFailures == nil {
		o.httpFailures = make(map[string]*assistant.EndpointHTTPError)
	}
	o.httpFailures[owner] = &assistant.EndpointHTTPError{StatusCode: status, Code: code}
}

func (s *httpStream) capacity(ctx context.Context) (int, error) {
	for {
		s.mu.Lock()
		available := streamWindow - s.outstanding
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return 0, context.Canceled
		}
		if available > 0 {
			return min(available, chunkBytes), nil
		}
		select {
		case <-s.credit:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (o *operation) endHTTP(id string, s *httpStream, code *string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()
	o.sendFailure("model.end", nil, modelEnd{id, code})
}

func (o *operation) ackHTTP(e envelope) error {
	var ack modelAck
	if err := decodeStrict(e.Payload, &ack); err != nil {
		return err
	}
	o.mu.Lock()
	s := o.streams[ack.StreamID]
	o.mu.Unlock()
	if s == nil || ack.AckBytes <= 0 {
		return errProtocol
	}
	s.mu.Lock()
	if ack.AckBytes > s.outstanding {
		s.mu.Unlock()
		return errProtocol
	}
	s.outstanding -= ack.AckBytes
	s.mu.Unlock()
	select {
	case s.credit <- struct{}{}:
	default:
	}
	return nil
}

func (o *operation) closeHTTP(e envelope) error {
	var close modelClose
	if err := decodeStrict(e.Payload, &close); err != nil {
		return err
	}
	o.mu.Lock()
	s := o.streams[close.StreamID]
	o.mu.Unlock()
	if s == nil {
		return errProtocol
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errProtocol
	}
	s.closed = true
	body := s.body
	s.mu.Unlock()
	s.cancel()
	if body != nil {
		_ = body.Close()
	}
	return nil
}
