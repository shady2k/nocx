package fakemodeltest_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func post(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer fixture-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func chunks(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response = %d %v", resp.StatusCode, resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	var result []map[string]any
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("not SSE: %q", line)
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk["object"] != "chat.completion.chunk" || chunk["model"] != "fake-model" {
			t.Fatalf("chunk = %v", chunk)
		}
		result = append(result, chunk)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("missing [DONE]")
	}
	return result
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", value)
	}
	return result
}

func choice(t *testing.T, chunk map[string]any) map[string]any {
	t.Helper()
	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices = %v", chunk["choices"])
	}
	return object(t, choices[0])
}

func TestScriptStreamsTextAndToolCallsAndCapturesRequests(t *testing.T) {
	fake := fakemodel.New(
		fakemodel.Response{Thinking: []string{"Let me", " think"}, Text: []string{"Hello", " world"}},
		fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{ID: "call-1", Name: "run_command", Arguments: `{"command":"pwd"}`}, {ID: "call-2", Name: "read_file", Arguments: `{"path":"a"}`}}},
	)
	server := httptest.NewServer(fake)
	defer server.Close()
	body := `{"model":"fake-model","stream":true,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"run_command"}}]}`
	first := chunks(t, post(t, server.URL, body))
	var text, thinking string
	for _, chunk := range first {
		delta := object(t, choice(t, chunk)["delta"])
		if content, ok := delta["content"].(string); ok {
			text += content
		}
		if content, ok := delta["reasoning_content"].(string); ok {
			thinking += content
		}
	}
	if text != "Hello world" || thinking != "Let me think" || choice(t, first[len(first)-1])["finish_reason"] != "stop" {
		t.Fatalf("text=%q thinking=%q chunks=%v", text, thinking, first)
	}
	second := chunks(t, post(t, server.URL, body))
	var calls []any
	for _, chunk := range second {
		if values, ok := object(t, choice(t, chunk)["delta"])["tool_calls"].([]any); ok {
			calls = append(calls, values...)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("tool calls = %v", calls)
	}
	call := object(t, calls[0])
	function := object(t, call["function"])
	if call["index"] != float64(0) || call["id"] != "call-1" || call["type"] != "function" || function["name"] != "run_command" || function["arguments"] != `{"command":"pwd"}` {
		t.Fatalf("tool call = %v", call)
	}
	if choice(t, second[len(second)-1])["finish_reason"] != "tool_calls" {
		t.Fatalf("finish = %v", second)
	}
	requests := fake.Requests()
	if len(requests) != 2 || string(requests[0].Body) != body || requests[0].Model != "fake-model" || !requests[0].Stream || len(requests[0].Messages) != 1 || len(requests[0].Tools) != 1 || requests[0].Header.Get("Authorization") != "Bearer fixture-key" {
		t.Fatalf("requests=%+v", requests)
	}
	requests[0].Body[0] = '!'
	requests[0].Header.Set("Authorization", "changed")
	requests[0].Messages[0][0] = '!'
	if fresh := fake.Requests()[0]; string(fresh.Body) != body || fresh.Header.Get("Authorization") != "Bearer fixture-key" || fresh.Messages[0][0] != '{' {
		t.Fatal("request snapshot aliases captured state")
	}
}

func TestErrorAndExhaustedScript(t *testing.T) {
	fake := fakemodel.New(fakemodel.Response{StatusCode: http.StatusTooManyRequests, Error: "rate limited"})
	server := httptest.NewServer(fake)
	defer server.Close()
	resp := post(t, server.URL, `{"model":"fake-model","stream":true}`)
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || body.Error.Message != "rate limited" {
		t.Fatalf("error = %d %+v", resp.StatusCode, body)
	}
	exhausted := post(t, server.URL, `{"stream":true}`)
	if exhausted.StatusCode != http.StatusInternalServerError {
		t.Fatalf("exhausted status = %d", exhausted.StatusCode)
	}
}

func TestBlockingResponseReleasesAndCancellationDoesNotHang(t *testing.T) {
	gate := make(chan struct{})
	fake := fakemodel.New(fakemodel.Response{Text: []string{"released"}, Block: gate}, fakemodel.Response{Block: make(chan struct{})})
	server := httptest.NewServer(fake)
	defer server.Close()
	response := make(chan *http.Response, 1)
	go func() { response <- post(t, server.URL, `{"model":"fake-model","stream":true}`) }()
	if _, err := fake.WaitRequests(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	close(gate)
	got := chunks(t, <-response)
	if len(got) == 0 {
		t.Fatal("no released response")
	}
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		finished <- err
	}()
	if _, err := fake.WaitRequests(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("canceled request succeeded")
	}
	canceled, cancelWait := context.WithCancel(t.Context())
	cancelWait()
	if _, err := fake.WaitRequests(canceled, 3); err == nil {
		t.Fatal("canceled wait succeeded")
	}
}

func TestInvalidRequestsDoNotConsumeScript(t *testing.T) {
	fake := fakemodel.New(fakemodel.Response{Text: []string{"still available"}})
	server := httptest.NewServer(fake)
	defer server.Close()
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/v1/chat/completions", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/other", "{}", http.StatusNotFound},
		{http.MethodPost, "/v1/chat/completions", "{", http.StatusBadRequest},
		{http.MethodPost, "/v1/chat/completions", `{"stream":false}`, http.StatusBadRequest},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}
	chunks(t, post(t, server.URL, `{"model":"fake-model","stream":true}`))
}

func TestOrderedDeltasGateAndUsage(t *testing.T) {
	gate := make(chan struct{})
	fake := fakemodel.New(fakemodel.Response{Deltas: []fakemodel.Delta{
		{Text: "first"}, {Thinking: "thought", Block: gate}, {Text: "last"},
	}, Usage: &fakemodel.Usage{InputTokens: 3, OutputTokens: 5, CacheReadTokens: 2}})
	server := httptest.NewServer(fake)
	defer server.Close()
	resp := post(t, server.URL, `{"model":"fake-model","stream":true}`)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"content":"first"`) {
			break
		}
	}
	close(gate)
	var rest strings.Builder
	for scanner.Scan() {
		rest.WriteString(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	body := rest.String()
	if !strings.Contains(body, `"reasoning_content":"thought"`) || !strings.Contains(body, `"content":"last"`) || strings.Index(body, `"reasoning_content":"thought"`) > strings.Index(body, `"content":"last"`) || !strings.Contains(body, `"cached_tokens":2`) || !strings.Contains(body, `"completion_tokens":5`) {
		t.Fatalf("stream = %s", body)
	}
}
