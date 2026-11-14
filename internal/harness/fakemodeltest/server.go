// Package fakemodeltest provides a scripted OpenAI-compatible streaming chat handler.
// It has no dependency on testing and can also be mounted by an e2e stand.
package fakemodeltest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
)

// ToolCall is one complete function call emitted as a streaming delta.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Delta is one ordered text or reasoning chunk. Block gates this chunk until
// released or the request is cancelled.
type Delta struct {
	Text     string
	Thinking string
	Block    <-chan struct{}
}

// Usage supplies OpenAI token accounting on the final streaming chunk.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
}

// Response scripts one request. Thinking and Text are emitted in slice order,
// followed by ToolCalls. Block delays headers until released or the caller cancels.
// A nonzero StatusCode sends an OpenAI JSON error instead of a stream.
type Response struct {
	Deltas     []Delta
	Usage      *Usage
	Thinking   []string
	Text       []string
	ToolCalls  []ToolCall
	StatusCode int
	Error      string
	Block      <-chan struct{}
}

// Request retains the original body and headers as well as common chat fields.
// Messages and Tools retain arbitrary provider-specific JSON fields.
type Request struct {
	Body     []byte            `json:"-"`
	Header   http.Header       `json:"-"`
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

// Server handles POST /v1/chat/completions. Valid requests consume responses in
// arrival order; an exhausted script returns HTTP 500 rather than inventing output.
type Server struct {
	mu        sync.Mutex
	responses []Response
	requests  []Request
	changed   chan struct{}
}

// New returns an HTTP handler with an independent copy of the script.
func New(responses ...Response) *Server {
	script := slices.Clone(responses)
	for i := range script {
		script[i].Deltas = slices.Clone(script[i].Deltas)
		if script[i].Usage != nil {
			usage := *script[i].Usage
			script[i].Usage = &usage
		}
		script[i].Thinking = slices.Clone(script[i].Thinking)
		script[i].Text = slices.Clone(script[i].Text)
		script[i].ToolCalls = slices.Clone(script[i].ToolCalls)
	}
	return &Server{responses: script, changed: make(chan struct{})}
}

// Requests returns a deep snapshot, safe to inspect while the handler is running.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Server) snapshot() []Request {
	result := slices.Clone(s.requests)
	for i := range result {
		result[i].Body = slices.Clone(result[i].Body)
		result[i].Header = result[i].Header.Clone()
		result[i].Messages = cloneJSON(result[i].Messages)
		result[i].Tools = cloneJSON(result[i].Tools)
	}
	return result
}

func cloneJSON(values []json.RawMessage) []json.RawMessage {
	result := slices.Clone(values)
	for i := range result {
		result[i] = slices.Clone(result[i])
	}
	return result
}

// WaitRequests waits until at least n valid requests have been captured. Capture
// occurs before Block is awaited, allowing tests to release a response without sleeps.
func (s *Server) WaitRequests(ctx context.Context, n int) ([]Request, error) {
	for {
		s.mu.Lock()
		if len(s.requests) >= n {
			result := s.snapshot()
			s.mu.Unlock()
			return result, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read request body")
		return
	}
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid chat request JSON")
		return
	}
	if !request.Stream {
		writeError(w, http.StatusBadRequest, "stream must be true")
		return
	}
	request.Body = body
	request.Header = r.Header.Clone()
	s.mu.Lock()
	index := len(s.requests)
	s.requests = append(s.requests, request)
	close(s.changed)
	s.changed = make(chan struct{})
	var response Response
	available := index < len(s.responses)
	if available {
		response = s.responses[index]
	}
	s.mu.Unlock()
	if !available {
		writeError(w, http.StatusInternalServerError, "fake model script exhausted")
		return
	}
	if response.Block != nil {
		select {
		case <-response.Block:
		case <-r.Context().Done():
			return
		}
	}
	if response.StatusCode != 0 {
		writeError(w, response.StatusCode, response.Error)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	emit := func(delta map[string]any, finish any) bool {
		chunk := map[string]any{
			"id": fmt.Sprintf("chatcmpl-fake-%d", index+1), "object": "chat.completion.chunk",
			"created": int64(0), "model": request.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if finish != nil && response.Usage != nil {
			u := response.Usage
			chunk["usage"] = map[string]any{"prompt_tokens": u.InputTokens + u.CacheReadTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.CacheReadTokens + u.OutputTokens, "prompt_tokens_details": map[string]int{"cached_tokens": u.CacheReadTokens}}
		}
		data, _ := json.Marshal(chunk)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !emit(map[string]any{"role": "assistant"}, nil) {
		return
	}
	for _, delta := range response.Deltas {
		if delta.Block != nil {
			select {
			case <-delta.Block:
			case <-r.Context().Done():
				return
			}
		}
		if delta.Text != "" && !emit(map[string]any{"content": delta.Text}, nil) {
			return
		}
		if delta.Thinking != "" && !emit(map[string]any{"reasoning_content": delta.Thinking}, nil) {
			return
		}
	}
	for _, text := range response.Thinking {
		if !emit(map[string]any{"reasoning_content": text}, nil) {
			return
		}
	}
	for _, text := range response.Text {
		if !emit(map[string]any{"content": text}, nil) {
			return
		}
	}
	for i, call := range response.ToolCalls {
		delta := map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": call.ID, "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
		}}}
		if !emit(delta, nil) {
			return
		}
	}
	finish := "stop"
	if len(response.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	if !emit(map[string]any{}, finish) {
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "fake_model_error"}})
}
