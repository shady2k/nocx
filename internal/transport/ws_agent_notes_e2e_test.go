package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/note"
)

type noteCompletionRequest struct {
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type noteProvider struct {
	mu       sync.Mutex
	requests []noteCompletionRequest
}

func (p *noteProvider) handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request", http.StatusInternalServerError)
		return
	}
	var req noteCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, req)
	requestNumber := len(p.requests)
	p.mu.Unlock()

	if requestNumber == 1 {
		var article string
		for _, message := range req.Messages {
			if message.Role == "user" {
				article = message.Content
			}
		}
		args, _ := json.Marshal(map[string]string{"body": article})
		streamNoteToolCall(w, "notes.create", string(args))
		return
	}
	streamNoteAnswer(w, "saved the text as a note")
}

func (p *noteProvider) snapshot() []noteCompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]noteCompletionRequest(nil), p.requests...)
}

func streamNoteToolCall(w http.ResponseWriter, name, args string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	payload := map[string]any{
		"id":      "chatcmpl-note",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "note-model",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":       "call_note",
					"type":     "function",
					"function": map[string]string{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
	encoded, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func streamNoteAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	payload := map[string]any{
		"id":      "chatcmpl-note",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "note-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	}
	encoded, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func createNoteTestEndpoint(t *testing.T, h *askHarness, baseURL string) {
	t.Helper()
	endpoint, code := decodeEndpointResult(t, jsonrpcCall(t, h.conn, "endpoints.create", map[string]any{
		"name":    "Notes",
		"baseUrl": baseURL,
		"schema":  "openai-compatible",
		"key":     "sk-test-123",
		"models":  []map[string]any{{"name": "note-model"}},
	}))
	if code != 0 {
		t.Fatalf("endpoints.create: code %d", code)
	}
	if isErrorResponse(t, jsonrpcCall(t, h.conn, "roles.assign", map[string]any{
		"role":       "answering",
		"endpointId": endpoint.ID,
		"model":      "note-model",
	})) {
		t.Fatal("roles.assign refused the test endpoint")
	}
}

func TestAgentAsk_BareTextCreatesAndSearchesNoteThroughProvider(t *testing.T) {
	const article = "Debian 11 LTS reaches end of life in June 2024."
	provider := &noteProvider{}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()

	client, _, err := assistant.NewClientAndRegistry(nil, nil, content.Floor{}, nil)
	if err != nil {
		t.Fatalf("assistant.NewClientAndRegistry: %v", err)
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	noteStore, err := note.Open(t.Context(), note.Config{
		Path: filepath.Join(t.TempDir(), "notes.db"),
		Key:  key,
	})
	if err != nil {
		t.Fatalf("note.Open: %v", err)
	}
	t.Cleanup(func() { _ = noteStore.Close() })
	var nextID atomic.Int64
	noteSvc := note.NewService(noteStore, func() string {
		return fmt.Sprintf("bare-note-%d", nextID.Add(1))
	}, nil)

	h := newAskHarnessWithOpts(t, client,
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithNotes(noteSvc),
	)
	createNoteTestEndpoint(t, h, server.URL)
	sid := openLocalSession(t, h.conn)
	result, errObj := askOverWire(t, h.conn, map[string]any{
		"askId":     "bare-text-note",
		"sessionId": sid,
		"question":  article,
		"cwd":       "/repo",
	}, 2)
	if errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}
	if result.State != "prepared" {
		t.Fatalf("agent.ask state = %q, want prepared", result.State)
	}
	stateRaw := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatalf("runState: %v", err)
	}
	if state.State != string(content.RunCompleted) {
		t.Fatalf("runState = %q, want completed", state.State)
	}

	requests := provider.snapshot()
	if len(requests) < 2 {
		t.Fatalf("provider received %d requests, want the tool call and follow-up answer", len(requests))
	}
	var offeredNoteCreate bool
	for _, tool := range requests[0].Tools {
		if tool.Function.Name == "notes.create" {
			offeredNoteCreate = true
			break
		}
	}
	if !offeredNoteCreate {
		t.Fatalf("first provider request did not offer notes.create")
	}

	listRaw := jsonrpcCallWithID(t, h.conn, "notes.list", map[string]any{}, 3)
	var listed struct {
		Result struct {
			Notes []struct {
				ID string `json:"id"`
			} `json:"notes"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(listRaw, &listed); err != nil {
		t.Fatalf("notes.list: %v", err)
	}
	if listed.Error != nil {
		t.Fatalf("notes.list: %+v", listed.Error)
	}
	if len(listed.Result.Notes) != 1 || listed.Result.Notes[0].ID != "bare-note-1" {
		t.Fatalf("notes.list = %+v, want the created note", listed.Result.Notes)
	}

	searchRaw := jsonrpcCallWithID(t, h.conn, "notes.search", map[string]any{"query": "Debian 11 LTS"}, 4)
	var searched struct {
		Result struct {
			Matches []struct {
				ID      string `json:"id"`
				Excerpt string `json:"excerpt"`
			} `json:"matches"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(searchRaw, &searched); err != nil {
		t.Fatalf("notes.search: %v", err)
	}
	if searched.Error != nil {
		t.Fatalf("notes.search: %+v", searched.Error)
	}
	if len(searched.Result.Matches) != 1 || searched.Result.Matches[0].ID != "bare-note-1" {
		t.Fatalf("notes.search = %+v, want the created note", searched.Result.Matches)
	}
	if !strings.Contains(searched.Result.Matches[0].Excerpt, "Debian 11 LTS") {
		t.Fatalf("notes.search excerpt = %q, want the article text", searched.Result.Matches[0].Excerpt)
	}
}
