package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
)

type fetchE2ECall struct {
	URL      string `json:"url"`
	Start    *int64 `json:"start,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type fetchE2EResult struct {
	URL      string `json:"url"`
	Total    int64  `json:"total"`
	Revision string `json:"revision"`
	Window   struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"window"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type fetchE2EProvider struct {
	mu          sync.Mutex
	url         string
	secondCall  bool
	answer      string
	marker      string
	requests    int
	calls       []fetchE2ECall
	results     []fetchE2EResult
	failureText string
}

func (p *fetchE2EProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.fail(fmt.Sprintf("read provider request: %v", err))
		streamAnswerChunk(w, "provider failed to read its request")
		return
	}

	p.mu.Lock()
	p.requests++
	requestNumber := p.requests
	p.mu.Unlock()

	switch {
	case requestNumber == 1:
		p.emitCall(w, fetchE2ECall{URL: p.url})
	case requestNumber == 2 && p.secondCall:
		result, decodeErr := fetchE2EResultFromBody(string(body))
		if decodeErr != nil {
			p.fail(fmt.Sprintf("decode first fetch result: %v", decodeErr))
			streamAnswerChunk(w, "provider could not decode the first fetch result")
			return
		}
		p.recordResult(result)
		p.emitCall(w, fetchE2ECall{
			URL:      p.url,
			Start:    &result.Window.End,
			Revision: result.Revision,
		})
	default:
		result, decodeErr := fetchE2EResultFromBody(string(body))
		if decodeErr != nil {
			p.fail(fmt.Sprintf("decode fetch result: %v", decodeErr))
			streamAnswerChunk(w, "provider could not decode the fetch result")
			return
		}
		p.recordResult(result)
		if p.marker != "" && strings.Contains(result.Text, p.marker) {
			streamAnswerChunk(w, p.marker)
			return
		}
		streamAnswerChunk(w, p.answer)
	}
}

func (p *fetchE2EProvider) emitCall(w http.ResponseWriter, call fetchE2ECall) {
	raw, err := json.Marshal(call)
	if err != nil {
		p.fail(fmt.Sprintf("marshal fetch call: %v", err))
		streamAnswerChunk(w, "provider could not marshal its fetch call")
		return
	}
	var decoded fetchE2ECall
	if err := json.Unmarshal(raw, &decoded); err != nil {
		p.fail(fmt.Sprintf("decode emitted fetch call: %v", err))
		streamAnswerChunk(w, "provider could not decode its fetch call")
		return
	}
	p.mu.Lock()
	p.calls = append(p.calls, decoded)
	p.mu.Unlock()
	streamToolCallChunk(w, "fetch.url", string(raw))
}

func (p *fetchE2EProvider) recordResult(result fetchE2EResult) {
	p.mu.Lock()
	p.results = append(p.results, result)
	p.mu.Unlock()
}

func (p *fetchE2EProvider) fail(message string) {
	p.mu.Lock()
	if p.failureText == "" {
		p.failureText = message
	}
	p.mu.Unlock()
}

func (p *fetchE2EProvider) snapshot() (calls []fetchE2ECall, results []fetchE2EResult, failure string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls = append([]fetchE2ECall(nil), p.calls...)
	results = append([]fetchE2EResult(nil), p.results...)
	return calls, results, p.failureText
}

func fetchE2EResultFromBody(body string) (fetchE2EResult, error) {
	toolResult := lifecycleBlockReadToolResult(body)
	const openTag = "<tool-output>\n"
	const closeTag = "\n</tool-output>"
	start := strings.Index(toolResult, openTag)
	end := strings.LastIndex(toolResult, closeTag)
	if start < 0 || end <= start+len(openTag) {
		return fetchE2EResult{}, fmt.Errorf("tool result has no tool-output envelope")
	}
	var result fetchE2EResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(toolResult[start+len(openTag):end])), &result); err != nil {
		return fetchE2EResult{}, err
	}
	return result, nil
}

func directFetchE2EClient() *apifetch.Client {
	return apifetch.New(apisend.NewRoutes(nil), nil)
}

func legacyWindows1251RSSBody() []byte {
	body := append([]byte(`<?xml version="1.0" encoding="windows-1251"?><rss><channel><title>`), []byte{0xcd, 0xee, 0xe2, 0xee, 0xf1, 0xf2, 0xe8}...)
	body = append(body, []byte(`</title><item><title>`)...)
	body = append(body, []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	return append(body, []byte(`</title><link>https://example.test/item</link></item></channel></rss>`)...)
}

func TestAgentAsk_FetchURLDecodesLegacyRSSEndToEnd(t *testing.T) {
	var feedRequests atomic.Int64
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feedRequests.Add(1)
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(legacyWindows1251RSSBody())
	}))
	defer feed.Close()

	provider := &fetchE2EProvider{url: feed.URL, answer: "the feed item is Привет"}
	model := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer model.Close()

	h := newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithAgentFetcher(directFetchE2EClient()),
	)
	h.createEndpointAt(model.URL)
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "fetch-rss-e2e", "sessionId": sid,
		"question": "fetch the feed and tell me its item title", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	call := readNotification(t, h.conn, "agent.runToolCall", 15*time.Second)
	var announced struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(call, &announced); err != nil {
		t.Fatalf("decode fetch.url call: %v", err)
	}
	if announced.Tool != "fetch.url" {
		t.Fatalf("announced tool = %q, want fetch.url", announced.Tool)
	}
	answer := readNotification(t, h.conn, "agent.runDelta", 15*time.Second)
	if !strings.Contains(string(answer), "Привет") {
		t.Fatalf("answer delta = %s, want the decoded item title", answer)
	}
	state := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	if !strings.Contains(string(state), `"state":"completed"`) {
		t.Fatalf("runState = %s, want completed", state)
	}

	calls, results, failure := provider.snapshot()
	if failure != "" {
		t.Fatalf("provider failure: %s", failure)
	}
	if len(calls) != 1 || calls[0].URL != feed.URL || calls[0].Start != nil || calls[0].Revision != "" {
		t.Fatalf("fetch.url calls = %+v, want one initial call for %q", calls, feed.URL)
	}
	if len(results) != 1 {
		t.Fatalf("fetch.url results = %d, want one", len(results))
	}
	if !strings.Contains(results[0].Text, "Привет") {
		t.Fatalf("fetch.url result text = %q, want decoded item title", results[0].Text)
	}
	if strings.Contains(results[0].Text, "<rss") {
		t.Fatalf("fetch.url result text = %q, contains raw RSS markup", results[0].Text)
	}
	if results[0].Truncated {
		t.Fatalf("fetch.url result = %+v, want complete feed window", results[0])
	}
	if got := feedRequests.Load(); got != 1 {
		t.Fatalf("feed HTTP requests = %d, want one", got)
	}
}

func TestAgentAsk_FetchURLReadsTailFromOneSnapshot(t *testing.T) {
	const marker = "TAIL_FACT_ONLY_IN_SECOND_WINDOW"
	var document strings.Builder
	for document.Len() <= 64<<10 {
		document.WriteString("padding line that is absent from the requested fact\n")
	}
	document.WriteString(marker)
	body := []byte(document.String())

	var fetchRequests atomic.Int64
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchRequests.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer feed.Close()

	provider := &fetchE2EProvider{
		url:        feed.URL,
		secondCall: true,
		answer:     "the fact was not in the returned tail",
		marker:     marker,
	}
	model := httptest.NewServer(http.HandlerFunc(provider.serve))
	defer model.Close()

	h := newAskHarnessWithOpts(t, mustClient(t),
		WithAgentPolicy(autonomousPolicyStore(t)),
		WithAgentFetcher(directFetchE2EClient()),
	)
	h.createEndpointAt(model.URL)
	sid := openLocalSession(t, h.conn)
	if _, errObj := askOverWire(t, h.conn, map[string]any{
		"askId": "fetch-window-e2e", "sessionId": sid,
		"question": "fetch the document and tell me the fact at its tail", "cwd": "/repo",
	}, 2); errObj != nil {
		t.Fatalf("agent.ask: %+v", errObj)
	}

	for range 2 {
		call := readNotification(t, h.conn, "agent.runToolCall", 15*time.Second)
		var announced struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(call, &announced); err != nil {
			t.Fatalf("decode fetch.url call: %v", err)
		}
		if announced.Tool != "fetch.url" {
			t.Fatalf("announced tool = %q, want fetch.url", announced.Tool)
		}
	}
	answer := readNotification(t, h.conn, "agent.runDelta", 15*time.Second)
	if !strings.Contains(string(answer), marker) {
		t.Fatalf("answer delta = %s, want the tail fact", answer)
	}
	state := readNotification(t, h.conn, "agent.runState", 15*time.Second)
	if !strings.Contains(string(state), `"state":"completed"`) {
		t.Fatalf("runState = %s, want completed", state)
	}

	calls, results, failure := provider.snapshot()
	if failure != "" {
		t.Fatalf("provider failure: %s", failure)
	}
	if len(calls) != 2 {
		t.Fatalf("fetch.url calls = %d, want first window plus one continuation", len(calls))
	}
	if calls[0].URL != feed.URL || calls[0].Start != nil || calls[0].Revision != "" {
		t.Fatalf("first fetch.url call = %+v, want initial call for %q", calls[0], feed.URL)
	}
	if calls[1].URL != feed.URL || calls[1].Start == nil || calls[1].Revision == "" {
		t.Fatalf("second fetch.url call = %+v, want continuation with start and revision", calls[1])
	}
	if len(results) != 2 {
		t.Fatalf("fetch.url results = %d, want one result per model continuation", len(results))
	}
	if !results[0].Truncated || results[0].Window.End != *calls[1].Start {
		t.Fatalf("first fetch.url result = %+v, want truncated window ending at second start %d", results[0], *calls[1].Start)
	}
	if results[1].Revision != results[0].Revision || calls[1].Revision != results[0].Revision {
		t.Fatalf("snapshot revisions = first %q, second result %q, second call %q; want one revision", results[0].Revision, results[1].Revision, calls[1].Revision)
	}
	if !strings.Contains(results[1].Text, marker) {
		t.Fatalf("second fetch.url result text = %q, want the tail fact", results[1].Text)
	}
	if strings.Contains(results[0].Text, marker) {
		t.Fatalf("first fetch.url result text contains tail fact: %q; want fact only in second window", results[0].Text)
	}
	if got := fetchRequests.Load(); got != 1 {
		t.Fatalf("feed HTTP requests = %d, want exactly one snapshot fetch", got)
	}
}
